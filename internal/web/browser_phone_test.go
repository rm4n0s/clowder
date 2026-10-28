//go:build clowder_browser

package web_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strconv"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/rm4n0s/clowder/internal/browsertest"
	"golang.org/x/crypto/ssh"
)

// phoneSizes are the two widths every page must fit (SPEC D58).
var phoneSizes = []struct {
	name string
	w, h int64
}{{"390x844", 390, 844}, {"360x740", 360, 740}}

// phoneBrowser starts a browser of the given size, logged in as user (not
// logged in when user is empty), and closes it when the test ends.
func phoneBrowser(t *testing.T, srv *browsertest.Server, user string, w, h int64) context.Context {
	t.Helper()
	var ctx context.Context
	if user == "" {
		browser, cancel, err := browsertest.NewBrowser(context.Background())
		if err != nil {
			t.Fatalf("browser: %v", err)
		}
		t.Cleanup(cancel)
		var cancelCtx context.CancelFunc
		ctx, cancelCtx = context.WithTimeout(browser, pageTimeout)
		t.Cleanup(cancelCtx)
	} else {
		ctx = secondBrowser(t, srv, user)
	}
	if err := chromedp.Do(ctx, chromedp.EmulateViewport(w, h)); err != nil {
		t.Fatalf("resize: %v", err)
	}
	return ctx
}

// hrefOf returns the href of the first element that matches the selector on
// the page the browser has open.
func hrefOf(t *testing.T, ctx context.Context, sel string) string {
	t.Helper()
	href, err := chromedp.Run(ctx, chromedp.Evaluate[string](`document.querySelector(`+strconv.Quote(sel)+`).getAttribute('href')`))
	if err != nil {
		t.Fatalf("href of %s: %v", sel, err)
	}
	return href
}

// smallTargets is the JavaScript that lists what is touched and is smaller than
// 44 px in height or width: links, buttons, fields, summaries and the labels of
// radio buttons, checkboxes and file fields, which are the targets of those. A
// link that is part of a line of text (display inline) is left out, and so is
// what is not drawn.
const smallTargets = `(() => {
  const out = [];
  const drawn = (e) => { const r = e.getBoundingClientRect(), cs = getComputedStyle(e); return r.width > 1 && r.height > 1 && cs.visibility !== 'hidden' && cs.display !== 'none' && cs.opacity !== '0'; };
  const name = (e) => e.tagName.toLowerCase() + (e.id ? '#' + e.id : '') + (e.className && typeof e.className === 'string' ? '.' + e.className.trim().split(/\s+/).join('.') : '');
  for (const e of document.querySelectorAll('a[href], button, input, select, textarea, summary')) {
    if (e.matches('input[type=hidden]') || e.disabled || !drawn(e)) { continue; }
    let closed = false;
    for (let d = e.parentElement && e.parentElement.closest('details'); d; d = d.parentElement && d.parentElement.closest('details')) {
      const summary = d.querySelector(':scope > summary');
      if (!d.open && !(summary && summary.contains(e))) { closed = true; }
    }
    if (closed) { continue; }
    const cs = getComputedStyle(e);
    if (e.tagName === 'A' && cs.display === 'inline') { continue; }
    let target = e;
    if (e.matches('input[type=radio], input[type=checkbox]')) {
      const label = e.closest('label') || (e.id ? document.querySelector('label[for="' + e.id + '"]') : null);
      if (label && drawn(label)) { target = label; } else if (!drawn(e) || e.getBoundingClientRect().width <= 2) { continue; }
    }
    const r = target.getBoundingClientRect();
    let w = r.width, h = r.height;
    // A hit area drawn by an absolutely positioned ::after counts (21.2's way for a small link).
    const after = getComputedStyle(target, '::after');
    if (after.content !== 'none' && after.position === 'absolute') { w = Math.max(w, parseFloat(after.width) || 0); h = Math.max(h, parseFloat(after.height) || 0); }
    if (h < 43.5 || w < 43.5) { out.push(name(target) + ' ' + Math.round(w) + 'x' + Math.round(h) + ' "' + (target.textContent || '').trim().slice(0, 20) + '"'); }
  }
  return out.join('\n');
})()`

// checkPhonePage waits for the page to show its content and fails the test when
// the page is wider than the screen or something touched is under 44 px. what
// names the page and the size in the message.
func checkPhonePage(t *testing.T, ctx context.Context, what, ready string) {
	t.Helper()
	if err := waitFor(ctx, ready); err != nil {
		t.Errorf("%s did not show its content (%s): %v", what, ready, err)
		return
	}
	if err := waitFor(ctx, fitsScreen); err != nil {
		width, _ := chromedp.Run(ctx, chromedp.Evaluate[string](`document.documentElement.scrollWidth + ' > ' + document.documentElement.clientWidth`))
		wide, _ := chromedp.Run(ctx, chromedp.Evaluate[string](`[...document.querySelectorAll('body *')].filter(e => e.getBoundingClientRect().right > document.documentElement.clientWidth + 1).slice(0, 6).map(e => e.tagName.toLowerCase() + (e.id ? '#' + e.id : '') + '.' + e.className).join(', ')`))
		t.Errorf("%s is wider than the screen (%s); elements past the edge: %s", what, width, wide)
	}
	small, err := chromedp.Run(ctx, chromedp.Evaluate[string](smallTargets))
	if err != nil {
		t.Errorf("%s: audit of the touch targets: %v", what, err)
		return
	}
	if small != "" {
		t.Errorf("%s has touch targets under 44 px:\n%s", what, small)
	}
}

// TestEveryPageFitsAPhone walks the pages of Clowder at 390 by 844 and at 360 by
// 740, as a member, as an admin and logged out, and fails for a page that is
// wider than the screen (the scroll width above the client width) or has a
// target under 44 px (SPEC D58). It holds the 390 px checks of 21.2 to 21.4 in a
// walk of every page.
func TestEveryPageFitsAPhone(t *testing.T) {
	srv := browsertest.StartWith(t, browsertest.Options{InviteMember: true, ResetUser: "bob"})
	for _, size := range phoneSizes {
		t.Run(size.name, func(t *testing.T) {
			t.Run("member", func(t *testing.T) {
				ctx := phoneBrowser(t, srv, "alice", size.w, size.h)
				visit := func(what, path, ready string) {
					t.Helper()
					if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+path)); err != nil {
						t.Errorf("%s: %v", what, err)
						return
					}
					checkPhonePage(t, ctx, what, ready)
				}
				visit("the home page", "/", `document.getElementById('signed-in-as') !== null`)
				visit("the room list", "/rooms", `document.querySelector('.room-card') !== null`)
				room := hrefOf(t, ctx, `.room-card[data-room="general"] h2 a`)
				visit("the new room page", "/rooms/new", `document.querySelector('form') !== null && document.querySelector('main h1') !== null`)
				visit("a room's feed", room, `document.querySelectorAll('#feed-posts article.post').length === 5`)
				post := hrefOf(t, ctx, `#feed-posts a.post-comments`)
				visit("a post", post, `document.querySelector('#thread') !== null`)
				visit("the board", room+"/board", `document.querySelector('#board-columns .board-card') !== null`)
				visit("the Messages list", "/messages", `document.querySelector('#conversation-list li.conversation') !== null`)
				conversation := hrefOf(t, ctx, `#conversation-list li.conversation[data-members="`+crewConversation+`"] a.conversation-link`)
				visit("a conversation", conversation, `document.querySelector('#message-list li.message') !== null`)
				visit("the search", "/search?q=%23design", `document.querySelector('#search-panel .search-result') !== null`)
				visit("the notifications", "/notifications", `document.querySelector('#notification-list .notification') !== null`)
				visit("the settings", "/me/settings", `document.querySelector('#profile-form') !== null`)
				visit("the old sessions address", "/me/sessions", `document.querySelector('#sessions li.session') !== null`)
				visit("a page that does not exist", "/nope", `document.querySelector('.error-card') !== null`)
			})
			t.Run("admin", func(t *testing.T) {
				ctx := phoneBrowser(t, srv, "admin", size.w, size.h)
				for _, p := range []struct{ what, path, ready string }{
					{"the users", "/admin/users", `document.querySelector('#users-panel table.users') !== null`},
					{"the rooms", "/admin/rooms", `document.querySelector('#admin-rooms li.admin-room') !== null`},
					{"the audit log", "/admin/audit", `document.querySelector('#audit-panel table.audit') !== null`},
					{"the settings of an admin", "/me/settings", `document.querySelector('#profile-form') !== null`},
				} {
					if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+p.path)); err != nil {
						t.Errorf("%s: %v", p.what, err)
						continue
					}
					checkPhonePage(t, ctx, p.what, p.ready)
				}
			})
			t.Run("logged out", func(t *testing.T) {
				ctx := phoneBrowser(t, srv, "", size.w, size.h)
				for _, p := range []struct{ what, path, ready string }{
					{"the login page", "/login", `document.querySelector('#login-form') !== null`},
					{"the sign-up page", "/invite/" + srv.InviteToken, `document.querySelector('form') !== null && document.querySelector('.auth-card') !== null`},
					{"the reset page", "/reset/" + srv.ResetToken, `document.querySelector('form') !== null && document.querySelector('.auth-card') !== null`},
					{"an error page", "/nope", `document.querySelector('.error-card') !== null`},
				} {
					if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+p.path)); err != nil {
						t.Errorf("%s: %v", p.what, err)
						continue
					}
					checkPhonePage(t, ctx, p.what, p.ready)
				}
			})
		})
	}
}

// TestLoginCodePageFitsAPhone: the second step of the login (the code of an SSH
// key) is no wider than the screen at either width and its targets are 44 px.
func TestLoginCodePageFitsAPhone(t *testing.T) {
	srv := browsertest.StartWith(t, browsertest.Options{SSH: true})
	carol := secondBrowser(t, srv, "carol")
	openSettings(t, carol, srv)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))) + " carol@laptop"
	err = chromedp.Do(carol,
		browsertest.Fill("#ssh-key-name", "laptop"),
		browsertest.Fill("#ssh-key-public", line),
		browsertest.Fill("#ssh-key-password", browsertest.DemoPassword),
		chromedp.Click(`#ssh-key-add-form button[type="submit"]`),
	)
	if err != nil {
		t.Fatalf("add the key: %v", err)
	}
	if err := waitFor(carol, `document.querySelector('#ssh-keys-panel ul.ssh-keys > li') !== null`); err != nil {
		t.Fatalf("the key is not in the list: %v", err)
	}
	fetchSSHCode(t, srv, "carol", signer)
	openSettings(t, carol, srv)
	err = chromedp.Do(carol, browsertest.Fill("#ssh-code-password", browsertest.DemoPassword), chromedp.Click(`#ssh-code-form button[type="submit"]`))
	if err != nil {
		t.Fatal(err)
	}
	if err := waitFor(carol, `document.querySelector('#ssh-code-state strong') !== null && document.querySelector('#ssh-code-state strong').textContent === 'on'`); err != nil {
		t.Fatalf("the code is not on: %v", err)
	}
	if err := chromedp.Do(carol, openUserMenu(), chromedp.Click(`#logout-form button[type="submit"]`), chromedp.WaitVisible("#login-form")); err != nil {
		t.Fatalf("log out: %v", err)
	}
	for _, size := range phoneSizes {
		err = chromedp.Do(carol,
			chromedp.EmulateViewport(size.w, size.h),
			chromedp.Navigate(srv.URL+"/login"),
			chromedp.WaitVisible("#login-form"),
			browsertest.Fill("#login-username", "carol"),
			browsertest.Fill("#login-password", browsertest.DemoPassword),
			chromedp.Click(`#login-form button[type="submit"]`),
			chromedp.WaitVisible("#login-code-form"),
		)
		if err != nil {
			t.Fatalf("%s: the code form does not appear: %v", size.name, err)
		}
		checkPhonePage(t, carol, "the login code page at "+size.name, `document.querySelector('#login-code-form') !== null`)
	}
}

// drawerOpen is the JavaScript test that the room drawer is open and has slid
// into place.
const drawerOpen = `(() => { const s = document.getElementById('sidebar'); return s.matches(':popover-open') && Math.round(s.getBoundingClientRect().left) === 0; })()`

// drawerClosed is the JavaScript test that the room drawer is closed.
const drawerClosed = `(() => { const s = document.getElementById('sidebar'); return !s.matches(':popover-open') && getComputedStyle(s).display === 'none'; })()`

// TestRoomDrawerOnAPhone: at 390 px the rooms are a drawer. It is closed when
// the page loads; the button opens it and the focus goes into it; a room link
// inside it works (and the new page has it closed); Escape, a click on the scrim
// and the close button close it, each giving the focus back to the button; it
// stays open when the stream patches the sidebar (another user posts in a room,
// which also lights the dot of the button); and above 40 rem it is the column
// beside the page, with no button.
func TestRoomDrawerOnAPhone(t *testing.T) {
	srv := browsertest.Start(t)
	bob := secondBrowser(t, srv, "bob")
	alice := secondBrowser(t, srv, "alice")
	// Bob reads "general" once, so that nothing waits for him, and goes to a page
	// without a room, on a phone.
	openGeneral(t, bob)
	for _, room := range []string{"random", "secret"} {
		// The seeded data leaves unread posts in these two rooms.
		if err := chromedp.Do(bob, chromedp.Click(`aside a[data-room="`+room+`"]`), chromedp.WaitVisible("#post-editor-new")); err != nil {
			t.Fatalf("bob opens %s: %v", room, err)
		}
	}
	openGeneral(t, alice)
	if err := chromedp.Do(bob, chromedp.EmulateViewport(390, 844), chromedp.Navigate(srv.URL+"/search"), chromedp.WaitVisible("#search-form")); err != nil {
		t.Fatalf("bob opens the search: %v", err)
	}
	if err := chromedp.Do(bob, chromedp.Sleep(streamSettle)); err != nil {
		t.Fatal(err)
	}

	// Closed on load, with the button in the header and no dot.
	if err := waitFor(bob, drawerClosed); err != nil {
		t.Fatalf("the drawer is not closed on load: %v", err)
	}
	if err := waitFor(bob, `(() => { const b = document.getElementById('menu-button').getBoundingClientRect(); return b.width >= 43.5 && b.height >= 43.5; })()`); err != nil {
		t.Fatalf("the menu button is not drawn at 44 px: %v", err)
	}
	if got, err := chromedp.Run(bob, chromedp.Evaluate[string](`document.getElementById('menu-button').innerText.trim()`)); err != nil || got != "Rooms" {
		t.Errorf("the name of the menu button = %q (%v), want Rooms", got, err)
	}
	if err := waitFor(bob, `getComputedStyle(document.querySelector('.menu-dot')).display === 'none'`); err != nil {
		rooms, _ := chromedp.Run(bob, chromedp.Evaluate[string](`document.querySelector('.sidebar-room-list').innerText`))
		t.Fatalf("the dot shows with nothing unread: %v\n%s", err, rooms)
	}

	// The button opens it; the focus moves into it.
	if err := chromedp.Do(bob, chromedp.Click("#menu-button")); err != nil {
		t.Fatalf("click the menu button: %v", err)
	}
	if err := waitFor(bob, drawerOpen); err != nil {
		t.Fatalf("the button does not open the drawer: %v", err)
	}
	if err := waitFor(bob, `document.activeElement && document.activeElement.classList.contains('drawer-close')`); err != nil {
		t.Errorf("the focus did not move into the drawer: %v", err)
	}
	if err := waitFor(bob, `(() => { const e = document.getElementById('sidebar').getBoundingClientRect(); return e.right <= 390 && e.width >= 280 && e.height >= 800; })()`); err != nil {
		t.Errorf("the drawer is not a column of the screen's height from the left: %v", err)
	}

	if err := waitFor(bob, `[...document.querySelectorAll('#sidebar a, #sidebar button')].every(e => e.getBoundingClientRect().height >= 43.5)`); err != nil {
		t.Errorf("a link or button of the drawer is under 44 px high: %v", err)
	}

	// It stays open through the patch of the sidebar that a post of alice causes,
	// and the dot of the button shows.
	runJS(t, bob, `window.notReloaded = true; true`)
	alicePostsPlain(t, alice, "A post while the drawer is open")
	if err := waitFor(bob, `document.querySelector(`+"`"+generalItem+" .room-unread`"+`)?.textContent.trim() === '1'`); err != nil {
		t.Fatalf("the sidebar did not get the unread count: %v", err)
	}
	if err := chromedp.Do(bob, chromedp.Sleep(streamSettle)); err != nil {
		t.Fatal(err)
	}
	if err := waitFor(bob, drawerOpen+` && window.notReloaded === true`); err != nil {
		t.Errorf("the drawer closed (or the page reloaded) when the stream patched the sidebar: %v", err)
	}
	if err := waitFor(bob, `getComputedStyle(document.querySelector('.menu-dot')).display === 'block' && getComputedStyle(document.querySelector('.menu-unread-name')).display !== 'none'`); err != nil {
		t.Errorf("the dot and the unread name of the button do not show: %v", err)
	}

	// Escape closes it and the focus goes back to the button.
	if err := chromedp.Do(bob, chromedp.KeyEvent(kb.Escape)); err != nil {
		t.Fatal(err)
	}
	if err := waitFor(bob, drawerClosed); err != nil {
		t.Fatalf("Escape does not close the drawer: %v", err)
	}
	if err := waitFor(bob, `document.activeElement && document.activeElement.id === 'menu-button'`); err != nil {
		t.Errorf("the focus did not go back to the button after Escape: %v", err)
	}

	// The scrim closes it: a click on the part of the page the drawer does not cover.
	if err := chromedp.Do(bob, chromedp.Click("#menu-button")); err != nil {
		t.Fatal(err)
	}
	if err := waitFor(bob, drawerOpen); err != nil {
		t.Fatalf("the drawer does not open the second time: %v", err)
	}
	if err := chromedp.Do(bob, chromedp.MouseClickXY(375, 500)); err != nil {
		t.Fatal(err)
	}
	if err := waitFor(bob, drawerClosed); err != nil {
		t.Fatalf("a click on the scrim does not close the drawer: %v", err)
	}

	// The close button closes it.
	if err := chromedp.Do(bob, chromedp.Click("#menu-button")); err != nil {
		t.Fatal(err)
	}
	if err := waitFor(bob, drawerOpen); err != nil {
		t.Fatalf("the drawer does not open the third time: %v", err)
	}
	if err := waitFor(bob, `(() => { const b = document.querySelector('.drawer-close').getBoundingClientRect(); return b.width >= 43.5 && b.height >= 43.5; })()`); err != nil {
		t.Errorf("the close button is under 44 px: %v", err)
	}
	if err := chromedp.Do(bob, chromedp.Click(".drawer-close")); err != nil {
		t.Fatal(err)
	}
	if err := waitFor(bob, drawerClosed); err != nil {
		t.Fatalf("the close button does not close the drawer: %v", err)
	}
	if err := waitFor(bob, `document.activeElement && document.activeElement.id === 'menu-button'`); err != nil {
		t.Errorf("the focus did not go back to the button after the close button: %v", err)
	}

	// A room link inside it works, and the page it opens has the drawer closed.
	if err := chromedp.Do(bob, chromedp.Click("#menu-button")); err != nil {
		t.Fatal(err)
	}
	if err := waitFor(bob, drawerOpen); err != nil {
		t.Fatalf("the drawer does not open the fourth time: %v", err)
	}
	if err := chromedp.Do(bob, chromedp.Click(`#sidebar a[data-room="general"]`), chromedp.WaitVisible("#post-editor-new")); err != nil {
		t.Fatalf("a room link of the drawer does not open the room: %v", err)
	}
	if err := waitFor(bob, drawerClosed); err != nil {
		t.Errorf("the drawer is open on the page of the room: %v", err)
	}
	if err := waitFor(bob, `getComputedStyle(document.querySelector('.menu-dot')).display === 'none'`); err != nil {
		t.Errorf("the dot still shows after the room was read: %v", err)
	}

	// Above 40 rem it is a column beside the page and the button is gone.
	if err := chromedp.Do(bob, chromedp.EmulateViewport(1280, 800), chromedp.Navigate(srv.URL+"/rooms"), chromedp.WaitVisible(".room-card")); err != nil {
		t.Fatal(err)
	}
	const column = `(() => { const s = document.getElementById('sidebar'), m = document.querySelector('main'); const cs = getComputedStyle(s);
		return cs.display === 'block' && cs.position === 'static' && s.getBoundingClientRect().right <= m.getBoundingClientRect().left + 1 && s.getBoundingClientRect().width > 200 &&
		  getComputedStyle(document.getElementById('menu-button')).display === 'none' && getComputedStyle(document.querySelector('.drawer-head')).display === 'none' &&
		  s.querySelector('a[data-room="general"]').getBoundingClientRect().height > 20; })()`
	if err := waitFor(bob, column); err != nil {
		t.Errorf("above 40 rem the sidebar is not a column without a button: %v", err)
	}
}

// TestHeaderOfAPhoneAt360: at 360 px the menu button, the logo mark, the bell,
// Messages and the user menu are in the header on one row, and the search box is
// on a row of its own, all drawn inside the screen and reachable (at least 44 px).
func TestHeaderOfAPhoneAt360(t *testing.T) {
	srv := browsertest.Start(t)
	ctx := phoneBrowser(t, srv, "alice", 360, 740)
	if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/rooms"), chromedp.WaitVisible(".room-card")); err != nil {
		t.Fatal(err)
	}
	const parts = `['#menu-button', '#site-header .logo', '#site-header .sidebar-search input', '#site-header .header-notifications', '#site-header .header-messages', '#user-menu summary']`
	if err := waitFor(ctx, `(() => `+parts+`.every(sel => { const r = document.querySelector(sel).getBoundingClientRect(); return r.left >= 0 && r.right <= 360 && r.width >= 43.5 && r.height >= 43.5 - (sel.includes('input') ? 2 : 0); }))()`); err != nil {
		bad, _ := chromedp.Run(ctx, chromedp.Evaluate[string](`JSON.stringify(`+parts+`.map(sel => { const r = document.querySelector(sel).getBoundingClientRect(); return [sel, Math.round(r.left), Math.round(r.right), Math.round(r.width), Math.round(r.height)]; }))`))
		t.Fatalf("a part of the header is outside the screen or under 44 px: %v\n%s", err, bad)
	}
	// One row for the buttons: the same top for the menu button and the bell.
	if err := waitFor(ctx, `Math.abs(document.getElementById('menu-button').getBoundingClientRect().top - document.querySelector('#site-header .header-notifications').getBoundingClientRect().top) < 2`); err != nil {
		t.Errorf("the buttons of the header are not on one row: %v", err)
	}
	// The word of the logo is for a screen reader only at this width.
	if err := waitFor(ctx, `document.querySelector('#site-header .logo-word').getBoundingClientRect().width <= 1 && document.querySelector('#site-header .logo-word').textContent === 'clowder'`); err != nil {
		t.Errorf("the word of the logo is drawn at 360 px: %v", err)
	}
	// The user menu opens and its panel is inside the screen.
	if err := chromedp.Do(ctx, openUserMenu(), chromedp.WaitVisible(`#theme-menu`)); err != nil {
		t.Fatal(err)
	}
	if err := waitFor(ctx, `(() => { const r = document.querySelector('.user-menu-panel').getBoundingClientRect(); return r.left >= 0 && r.right <= 360; })()`); err != nil {
		t.Errorf("the user menu is outside the screen: %v", err)
	}
}
