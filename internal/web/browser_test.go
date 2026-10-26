//go:build clowder_browser

package web_test

import (
	"context"
	"strings"
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

// TestLoggedInSidebar logs in as alice with browsertest.LogIn and sees her
// name in the sidebar, without the Admin link.
func TestLoggedInSidebar(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()

	if err := browsertest.LogIn(ctx, srv, "alice"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}
	if got := textOf(t, ctx, "aside .sidebar-user"); got != "Alice" {
		t.Errorf("sidebar name = %q, want Alice", got)
	}
	if err := waitFor(ctx, `document.querySelector('aside a[href="/admin/users"]') === null`); err != nil {
		t.Errorf("a member sees the Admin link: %v", err)
	}

	// The admin pages are invisible to a member.
	if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/admin/users"), chromedp.WaitVisible("h1")); err != nil {
		t.Fatalf("open the admin page: %v", err)
	}
	if got := textOf(t, ctx, "h1"); got != "404" {
		t.Errorf("heading of the admin page for a member = %q, want 404", got)
	}
}

// TestAdminDisablesAUser logs in as the admin, opens the users page from the
// sidebar, disables carol and sees her row say disabled; then enables her
// again and makes an invitation link.
func TestAdminDisablesAUser(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()

	if err := browsertest.LogIn(ctx, srv, "admin"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}
	err := chromedp.Do(ctx, chromedp.Click(`aside a[href="/admin/users"]`), chromedp.WaitVisible("#users-panel"))
	if err != nil {
		t.Fatalf("open the users page: %v", err)
	}
	carol := `tr[data-username="carol"]`
	if got := textOf(t, ctx, carol+" .state"); got != "Active" {
		t.Fatalf("state of carol = %q, want Active", got)
	}
	// The admin's own row has no disable button.
	if err := waitFor(ctx, `document.querySelector('tr[data-username="admin"] form[action$="/disabled"]') === null`); err != nil {
		t.Errorf("the admin's row has a disable button: %v", err)
	}

	if err := chromedp.Do(ctx, chromedp.Click(carol+` form[action$="/disabled"] button`)); err != nil {
		t.Fatalf("click Disable: %v", err)
	}
	if err := waitFor(ctx, `document.querySelector('tr[data-username="carol"] .state').textContent.trim() === 'Disabled'`); err != nil {
		t.Fatalf("carol's row did not say disabled: %v", err)
	}
	if got := textOf(t, ctx, carol+` form[action$="/disabled"] button`); got != "Enable" {
		t.Errorf("button of carol = %q, want Enable", got)
	}

	// Enable her again.
	if err := chromedp.Do(ctx, chromedp.Click(carol+` form[action$="/disabled"] button`)); err != nil {
		t.Fatalf("click Enable: %v", err)
	}
	if err := waitFor(ctx, `document.querySelector('tr[data-username="carol"] .state').textContent.trim() === 'Active'`); err != nil {
		t.Fatalf("carol's row did not say active: %v", err)
	}

	// The last admin cannot lose the role: the message shows above the table.
	if err := chromedp.Do(ctx, chromedp.Click(`tr[data-username="admin"] form[action$="/role"] button`)); err != nil {
		t.Fatalf("click Make member: %v", err)
	}
	if err := waitFor(ctx, `document.querySelector('#users-panel .form-error') !== null`); err != nil {
		t.Fatalf("the last-admin message did not appear: %v", err)
	}
	if got := textOf(t, ctx, "#users-panel .form-error"); !strings.Contains(got, "one active admin") {
		t.Errorf("message = %q", got)
	}

	// An invitation link for an admin appears with its expiry.
	err = chromedp.Do(ctx,
		chromedp.SetValue("#invite-role", "admin"),
		chromedp.Click(`#invite-form button[type="submit"]`),
		chromedp.WaitVisible("#invitation-link"),
	)
	if err != nil {
		t.Fatalf("make an invitation: %v", err)
	}
	link := valueOf(t, ctx, "#invitation-link")
	if !strings.HasPrefix(link, srv.URL+"/invite/") {
		t.Errorf("invitation link = %q, want it to start with %s/invite/", link, srv.URL)
	}
	if got := textOf(t, ctx, "#invitation-box .hint"); !strings.HasPrefix(got, "Expires ") {
		t.Errorf("expiry = %q", got)
	}
	// The copy button copies the link and says so.
	if err := chromedp.Do(ctx, chromedp.Click("#copy-invitation")); err != nil {
		t.Fatalf("click Copy link: %v", err)
	}
	if err := waitFor(ctx, `document.getElementById('copy-invitation').textContent === 'Copied'`); err != nil {
		t.Errorf("the copy button did not say Copied: %v", err)
	}
	// The link opens the sign-up page, in a browser that is not logged in.
	other, cancelOther, err := browsertest.NewBrowser(context.Background())
	if err != nil {
		t.Fatalf("second browser: %v", err)
	}
	defer cancelOther()
	otherCtx, cancelCtx := context.WithTimeout(other, pageTimeout)
	defer cancelCtx()
	if err := chromedp.Do(otherCtx, chromedp.Navigate(link), chromedp.WaitVisible("#signup-form")); err != nil {
		t.Fatalf("open the invitation link: %v", err)
	}
}
