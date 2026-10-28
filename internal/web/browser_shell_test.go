//go:build clowder_browser

package web_test

import (
	"context"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/clowder/internal/browsertest"
)

// computed returns the computed value of a CSS property of the first element
// that matches the selector.
func computed(t *testing.T, ctx context.Context, sel, prop string) string {
	t.Helper()
	got, err := chromedp.Run(ctx, chromedp.Evaluate[string](
		`getComputedStyle(document.querySelector(`+jsQuote(sel)+`)).`+prop))
	if err != nil {
		t.Fatalf("read %s of %s: %v", prop, sel, err)
	}
	return got
}

// TestThemeFollowsTheSystemUnlessSet: with no setting the page takes the
// palette of the system (emulated prefers-color-scheme), and the theme form of
// the user menu overrides it, at once and on the next page, without closing the
// menu.
func TestThemeFollowsTheSystemUnlessSet(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()

	const (
		lightGround = "rgb(251, 246, 238)"
		darkGround  = "rgb(26, 21, 18)"
	)
	if err := chromedp.Do(ctx, browsertest.EmulateColorScheme("dark")); err != nil {
		t.Fatalf("emulate dark: %+v", err)
	}
	if err := browsertest.LogIn(ctx, srv, "alice"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}
	if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/"), chromedp.WaitVisible("#user-menu")); err != nil {
		t.Fatalf("open the home page: %v", err)
	}
	if got := computed(t, ctx, "body", "backgroundColor"); got != darkGround {
		t.Errorf("system dark: ground = %s, want %s", got, darkGround)
	}
	if got := textOf(t, ctx, "h1"); got != "Clowder" {
		t.Errorf("heading = %q", got)
	}
	// System is marked, and the menu is closed.
	if n := countOf(t, ctx, `#theme-menu button[value="system"][aria-pressed="true"]`); n != 1 {
		t.Errorf("System is marked %d times, want 1", n)
	}
	if err := waitFor(ctx, `document.getElementById('user-menu').open === false`); err != nil {
		t.Errorf("the user menu is open on load: %v", err)
	}

	// Light in the menu beats the dark system, on the page at once.
	err := chromedp.Do(ctx, openUserMenu(), chromedp.Click(`#theme-menu button[value="light"]`))
	if err != nil {
		t.Fatalf("choose Light: %v", err)
	}
	if err := waitFor(ctx, `document.documentElement.getAttribute('data-theme') === 'light'`); err != nil {
		t.Fatalf("data-theme is not light: %v", err)
	}
	if err := waitFor(ctx, `document.querySelector('#theme-menu button[value="light"]').getAttribute('aria-pressed') === 'true'`); err != nil {
		t.Fatalf("Light is not marked: %v", err)
	}
	if got := computed(t, ctx, "body", "backgroundColor"); got != lightGround {
		t.Errorf("Light chosen: ground = %s, want %s", got, lightGround)
	}
	if err := waitFor(ctx, `document.getElementById('user-menu').open === true`); err != nil {
		t.Errorf("the menu closed after the choice: %v", err)
	}

	// The next page keeps it.
	if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/rooms"), chromedp.WaitVisible("#user-menu")); err != nil {
		t.Fatalf("open the rooms page: %v", err)
	}
	if got := computed(t, ctx, "body", "backgroundColor"); got != lightGround {
		t.Errorf("next page: ground = %s, want %s", got, lightGround)
	}
	if n := countOf(t, ctx, `#theme-menu button[value="light"][aria-pressed="true"]`); n != 1 {
		t.Errorf("Light is marked %d times on the next page, want 1", n)
	}

	// Back to System: the dark palette again.
	err = chromedp.Do(ctx, openUserMenu(), chromedp.Click(`#theme-menu button[value="system"]`))
	if err != nil {
		t.Fatalf("choose System: %v", err)
	}
	if err := waitFor(ctx, `!document.documentElement.hasAttribute('data-theme')`); err != nil {
		t.Fatalf("data-theme stays after System: %v", err)
	}
	if got := computed(t, ctx, "body", "backgroundColor"); got != darkGround {
		t.Errorf("System chosen: ground = %s, want %s", got, darkGround)
	}
}

// TestShellHasTheFocusRingAndTheFonts: a link takes a visible outline when it
// is reached by the keyboard, and the page uses the self-hosted fonts.
func TestShellHasTheFocusRingAndTheFonts(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()
	if err := browsertest.LogIn(ctx, srv, "alice"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}
	if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/"), chromedp.WaitVisible("#user-menu")); err != nil {
		t.Fatal(err)
	}
	// Tab to the first focusable element, the logo link.
	if err := chromedp.Do(ctx, chromedp.KeyEvent("\t")); err != nil {
		t.Fatalf("tab: %v", err)
	}
	if err := waitFor(ctx, `document.activeElement && document.activeElement.classList.contains('logo')`); err != nil {
		t.Fatalf("the logo is not focused: %v", err)
	}
	if got := computed(t, ctx, ".logo", "outlineStyle"); got != "solid" {
		t.Errorf("outline style of the focused logo = %q, want solid", got)
	}
	if got := computed(t, ctx, ".logo", "outlineWidth"); got != "2px" {
		t.Errorf("outline width of the focused logo = %q, want 2px", got)
	}
	// The fonts: the files are loaded from this site and are the ones in use.
	const fonts = `Promise.all([document.fonts.load('800 24px "Bricolage Grotesque"'), document.fonts.load('400 15px Nunito'), document.fonts.load('400 13px "JetBrains Mono"')]).then(r => r.map(x => x.length))`
	got, err := chromedp.Run(ctx, chromedp.Evaluate[[]int](fonts, chromedp.EvalAwaitPromise))
	if err != nil {
		t.Fatalf("load the fonts: %v", err)
	}
	for i, n := range got {
		if n == 0 {
			t.Errorf("font %d has no @font-face that matches", i)
		}
	}
	if got := computed(t, ctx, ".logo-word", "fontFamily"); got != `"Bricolage Grotesque", system-ui, sans-serif` {
		t.Errorf("the logo word's font = %q", got)
	}
}
