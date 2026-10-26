//go:build clowder_browser

package web_test

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/clowder/internal/browsertest"
)

// demoPassword is the password of the demo users (clowder demo).
const demoPassword = "demo-password-1234"

// pageTimeout bounds one browser test.
const pageTimeout = 60 * time.Second

// pollTimeout bounds one wait for the page to change.
const pollTimeout = 10 * time.Second

// waitFor waits until the JavaScript expression is true in the page.
func waitFor(ctx context.Context, expr string) error {
	_, err := chromedp.Run(ctx, chromedp.Poll[chromedp.Void](expr, chromedp.WithPollingTimeout(pollTimeout)))
	return err
}

// textOf returns the text of the element.
func textOf(t *testing.T, ctx context.Context, sel string) string {
	t.Helper()
	got, err := chromedp.Run(ctx, chromedp.Text(sel))
	if err != nil {
		t.Fatalf("read %s: %v", sel, err)
	}
	return got
}

// valueOf returns the value of the input.
func valueOf(t *testing.T, ctx context.Context, sel string) string {
	t.Helper()
	got, err := chromedp.Run(ctx, chromedp.Value(sel))
	if err != nil {
		t.Fatalf("read the value of %s: %v", sel, err)
	}
	return got
}

// logIn types the credentials into the login form and submits it.
func logIn(t *testing.T, ctx context.Context, username, password string) {
	t.Helper()
	err := chromedp.Do(ctx,
		browsertest.Fill("#login-username", username),
		browsertest.Fill("#login-password", password),
		chromedp.Click(`#login-form button[type="submit"]`),
	)
	if err != nil {
		t.Fatalf("submit the login form: %v", err)
	}
}

// TestLoginDatastar logs in through the real login form: Datastar sends the
// form, shows a failure in the form, and follows the redirect after a success;
// then it logs out. The home page also proves that datastar.js runs: its
// data-text shows the value of the data-signals.
func TestLoginDatastar(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()

	// A page that needs a login sends the browser to the login page.
	if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/"), chromedp.WaitVisible("#login-form")); err != nil {
		t.Fatalf("open the home page: %v", err)
	}
	if got, err := chromedp.Run(ctx, chromedp.Location()); err != nil || got != srv.URL+"/login?next=%2F" {
		t.Fatalf("location = %q, %v; want the login page with next", got, err)
	}

	// A wrong password shows the problem in the form, with the username kept
	// and no password sent back, without leaving the page.
	logIn(t, ctx, "admin", "not the password")
	if err := waitFor(ctx, `document.querySelector('#login-form .form-error') !== null`); err != nil {
		t.Fatalf("the form problem did not appear: %v", err)
	}
	if got := textOf(t, ctx, "#login-form .form-error"); got != "Wrong username or password." {
		t.Errorf("form problem = %q", got)
	}
	if got := valueOf(t, ctx, "#login-username"); got != "admin" {
		t.Errorf("username after the failure = %q, want admin", got)
	}
	// The server sent no password back (the field keeps what was typed in the
	// browser, as Datastar leaves a typed value alone).
	if err := waitFor(ctx, `document.getElementById('login-password').getAttribute('value') === null`); err != nil {
		t.Errorf("the patched form carries a password value: %v", err)
	}

	// The right one goes to the page that was asked for: the home page.
	logIn(t, ctx, "admin", demoPassword)
	if err := chromedp.Do(ctx, chromedp.WaitVisible("#datastar-check")); err != nil {
		t.Fatalf("the home page did not open: %v", err)
	}
	if err := waitFor(ctx, `document.getElementById('datastar-check').textContent === 'Datastar is running'`); err != nil {
		t.Errorf("datastar.js did not run on the home page: %v", err)
	}
	if got := textOf(t, ctx, "#signed-in-as"); got != "Signed in as Admin" {
		t.Errorf("greeting = %q, want %q", got, "Signed in as Admin")
	}

	// The login page sends a logged-in person home.
	if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/login"), chromedp.WaitVisible("#signed-in-as")); err != nil {
		t.Fatalf("open the login page while logged in: %v", err)
	}

	// Logging out brings the login page back, and the home page needs a login again.
	if err := chromedp.Do(ctx, chromedp.Click("#logout-form button"), chromedp.WaitVisible("#login-form")); err != nil {
		t.Fatalf("log out: %v", err)
	}
	if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/"), chromedp.WaitVisible("#login-form")); err != nil {
		t.Fatalf("open the home page after the logout: %v", err)
	}
}

// TestSignUpDatastar signs up through the real form of an invitation: the
// field errors appear next to the fields, the input is kept, and a valid form
// logs the new member in.
func TestSignUpDatastar(t *testing.T) {
	srv := browsertest.StartWith(t, browsertest.Options{InviteMember: true})
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()

	fill := browsertest.Fill
	err := chromedp.Do(ctx,
		chromedp.Navigate(srv.URL+"/invite/"+srv.InviteToken),
		chromedp.WaitVisible("#signup-form"),
		fill("#signup-username", "ab"),
		fill("#signup-display-name", "Dana"),
		fill("#signup-password", "short"),
		chromedp.Click(`#signup-form button[type="submit"]`),
	)
	if err != nil {
		t.Fatalf("submit the sign-up form: %v", err)
	}
	if err := waitFor(ctx, `document.getElementById('signup-password-error') !== null`); err != nil {
		t.Fatalf("the field errors did not appear: %v", err)
	}
	if got := textOf(t, ctx, "#signup-username-error"); got != "Use 3 to 32 characters." {
		t.Errorf("username error = %q", got)
	}
	if got := textOf(t, ctx, "#signup-password-error"); got != "Use at least 12 characters." {
		t.Errorf("password error = %q", got)
	}
	if got := valueOf(t, ctx, "#signup-username"); got != "ab" {
		t.Errorf("username after the failure = %q, want ab", got)
	}
	if got := valueOf(t, ctx, "#signup-display-name"); got != "Dana" {
		t.Errorf("display name after the failure = %q, want Dana", got)
	}
	if err := waitFor(ctx, `document.getElementById('signup-password').getAttribute('value') === null`); err != nil {
		t.Errorf("the patched form carries a password value: %v", err)
	}

	err = chromedp.Do(ctx,
		fill("#signup-username", "dana"),
		fill("#signup-password", "a long enough password"),
		chromedp.Click(`#signup-form button[type="submit"]`),
		chromedp.WaitVisible("#signed-in-as"),
	)
	if err != nil {
		t.Fatalf("submit the corrected sign-up form: %v", err)
	}
	if got := textOf(t, ctx, "#signed-in-as"); got != "Signed in as Dana" {
		t.Errorf("greeting = %q, want %q", got, "Signed in as Dana")
	}

	// The invitation is used up.
	if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/invite/"+srv.InviteToken), chromedp.WaitVisible("h1")); err != nil {
		t.Fatal(err)
	}
	if got := textOf(t, ctx, "h1"); got != "404" {
		t.Errorf("heading of the used invitation = %q, want 404", got)
	}
}
