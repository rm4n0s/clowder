//go:build clowder_browser

package web_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/clowder/internal/browsertest"
)

// TestPostMenuOpensAndItsActionsWork: the "more" menu of a post is a details
// element without script. It opens, offers Follow (and Edit and Delete to the
// author), keeps its place when the answer to Follow patches the card, and
// closes with a click outside it.
func TestPostMenuOpensAndItsActionsWork(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()

	if err := browsertest.LogIn(ctx, srv, "alice"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}
	openGeneral(t, ctx)
	first := "#feed-posts article.post:first-of-type"
	if err := waitFor(ctx, `document.querySelectorAll('#feed-posts article.post').length === 5`); err != nil {
		t.Fatalf("the demo posts are not in the feed: %v", err)
	}
	// Closed, the panel is not drawn; the button has a name.
	if err := waitFor(ctx, `document.querySelector('`+first+` .post-menu > summary').getAttribute('aria-label') === 'More actions' && !document.querySelector('`+first+` .post-menu').open`); err != nil {
		t.Fatalf("the menu is not closed with its name: %v", err)
	}
	if err := chromedp.Do(ctx, openPostMenu(first), chromedp.WaitVisible(first+" .post-follow button")); err != nil {
		t.Fatalf("open the menu: %v", err)
	}
	before := textOf(t, ctx, first+" .post-follow button")
	want := map[string]string{"Follow": "Unfollow", "Unfollow": "Follow"}[before]
	if want == "" {
		t.Fatalf("the follow button says %q", before)
	}
	if err := chromedp.Do(ctx, chromedp.Click(first+" .post-follow button")); err != nil {
		t.Fatalf("click %s: %v", before, err)
	}
	if err := waitFor(ctx, `document.querySelector('`+first+` .post-follow button') && document.querySelector('`+first+` .post-follow button').textContent.trim() === '`+want+`'`); err != nil {
		t.Fatalf("the button did not change from %s to %s: %v", before, want, err)
	}
	if err := waitFor(ctx, `document.querySelector('`+first+` .post-menu').open`); err != nil {
		t.Errorf("the answer closed the menu: %v", err)
	}
	// A click outside the menu closes it.
	if err := chromedp.Do(ctx, chromedp.Click("#room-header h1")); err != nil {
		t.Fatalf("click outside: %v", err)
	}
	if err := waitFor(ctx, `!document.querySelector('`+first+` .post-menu').open`); err != nil {
		t.Errorf("the menu stayed open after a click outside it: %v", err)
	}
}

// TestFeedAndPostFitAPhone: at 390 by 844 neither the feed (with the composer,
// a reaction picker open) nor a post page with comments is wider than the
// screen.
func TestFeedAndPostFitAPhone(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()

	if err := browsertest.LogIn(ctx, srv, "alice"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}
	if err := chromedp.Do(ctx, chromedp.EmulateViewport(390, 844), chromedp.Navigate(srv.URL+"/rooms")); err != nil {
		t.Fatalf("resize: %v", err)
	}
	const fits = `document.documentElement.scrollWidth <= document.documentElement.clientWidth`
	if err := waitFor(ctx, `document.querySelector('.room-card') !== null && `+fits); err != nil {
		t.Errorf("the room list is wider than the screen: %v", err)
	}
	// On a phone the rooms are in the drawer that the menu button opens (21.5).
	if err := chromedp.Do(ctx, chromedp.Click("#menu-button")); err != nil {
		t.Fatalf("open the room menu: %v", err)
	}
	if err := waitFor(ctx, drawerOpen); err != nil {
		t.Fatalf("the room menu did not open: %v", err)
	}
	if err := chromedp.Do(ctx, chromedp.Click(`aside a[data-room="general"]`), chromedp.WaitVisible("#post-editor-new")); err != nil {
		t.Fatalf("open general: %v", err)
	}
	if err := waitFor(ctx, `document.querySelectorAll('#feed-posts article.post').length === 5 && `+fits); err != nil {
		t.Fatalf("the feed is wider than the screen (%v)", err)
	}
	runJS(t, ctx, `document.querySelector('#feed-posts .reaction-add summary').click(); true`)
	if err := waitFor(ctx, `document.querySelector('#feed-posts .reaction-add[open] .reaction-picker') !== null && `+fits); err != nil {
		t.Errorf("the open reaction picker makes the page wider than the screen: %v", err)
	}
	runJS(t, ctx, `[...document.querySelectorAll('a.post-comments')].find(a => !a.textContent.startsWith('0 ')).id = 'to-comments'; true`)
	if err := chromedp.Do(ctx, chromedp.Click("#to-comments"), chromedp.WaitVisible("#thread .comment")); err != nil {
		t.Fatalf("open a post with comments: %v", err)
	}
	if err := waitFor(ctx, fits); err != nil {
		t.Errorf("the post page is wider than the screen: %v", err)
	}
	runJS(t, ctx, `document.querySelector('#thread .post-reply').click(); true`)
	if err := waitFor(ctx, `document.querySelector('#thread .reply-slot .post-editor') !== null && `+fits); err != nil {
		t.Errorf("the reply editor makes the page wider than the screen: %v", err)
	}
}

// fitsScreen is the JavaScript test that the page is no wider than the screen.
const fitsScreen = `document.documentElement.scrollWidth <= document.documentElement.clientWidth`

// TestBoardScrollsInsideItsOwnBox: when the columns do not fit, the box of the
// board (.board-scroll) scrolls sideways and the page does not, on a phone and
// on a desktop, and each column keeps its own scroll down.
func TestBoardScrollsInsideItsOwnBox(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()

	if err := browsertest.LogIn(ctx, srv, "alice"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}
	openGeneral(t, ctx)
	if err := chromedp.Do(ctx, chromedp.Click(`.room-nav a[href$="/board"]`), chromedp.WaitVisible("#board-columns .board-card")); err != nil {
		t.Fatalf("open the board: %v", err)
	}
	// Two more columns, so that they do not fit even a wide screen.
	for _, name := range []string{"Review", "Blocked"} {
		before := countOf(t, ctx, ".board-column")
		if err := chromedp.Do(ctx, browsertest.Fill("#board-add-name", name), chromedp.Click(`#board-add-form button[type="submit"]`)); err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
		if err := waitFor(ctx, `document.querySelectorAll('.board-column').length === `+strconv.Itoa(before+1)); err != nil {
			t.Fatalf("the column %s did not come: %v", name, err)
		}
	}
	for _, size := range []struct {
		name string
		w, h int64
	}{{"a phone", 390, 844}, {"a desktop", 1280, 800}} {
		if err := chromedp.Do(ctx, chromedp.EmulateViewport(size.w, size.h)); err != nil {
			t.Fatalf("resize to %s: %v", size.name, err)
		}
		box := `document.querySelector('.board-scroll')`
		if err := waitFor(ctx, box+`.scrollWidth > `+box+`.clientWidth + 50 && getComputedStyle(`+box+`).overflowX === 'auto'`); err != nil {
			t.Errorf("%s: the board box does not scroll sideways: %v", size.name, err)
		}
		if err := waitFor(ctx, fitsScreen); err != nil {
			t.Errorf("%s: the page scrolls sideways: %v", size.name, err)
		}
		// The box can be scrolled and the page still fits.
		runJS(t, ctx, box+`.scrollLeft = 10000; true`)
		if err := waitFor(ctx, box+`.scrollLeft > 0 && `+fitsScreen); err != nil {
			t.Errorf("%s: scrolling the box moved the page: %v", size.name, err)
		}
		runJS(t, ctx, box+`.scrollLeft = 0; true`)
	}
	// A column is a box that scrolls down by itself: its list of cards has
	// overflow-y auto and a column is never taller than the screen.
	if err := waitFor(ctx, `getComputedStyle(document.querySelector('.board-cards')).overflowY === 'auto' && document.querySelector('.board-column').getBoundingClientRect().height <= window.innerHeight`); err != nil {
		t.Errorf("a column does not scroll inside itself: %v", err)
	}
}

// TestBoardDragStatesAreVisible: the card in the hand, the column under the
// pointer and the place where it would land have their own look (board.js sets
// the classes dragging, drop-over, drop-before and drop-end).
func TestBoardDragStatesAreVisible(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()

	if err := browsertest.LogIn(ctx, srv, "alice"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}
	openGeneral(t, ctx)
	if err := chromedp.Do(ctx, chromedp.Click(`.room-nav a[href$="/board"]`), chromedp.WaitVisible("#board-columns .board-card")); err != nil {
		t.Fatalf("open the board: %v", err)
	}
	runJS(t, ctx, `
		const list = document.querySelectorAll('.board-cards')[1];
		list.classList.add('drop-over');
		list.children[0].classList.add('drop-before');
		document.querySelector('.board-card').classList.add('dragging');
		true`)
	checks := map[string]string{
		"the card in the hand is faded":             `parseFloat(getComputedStyle(document.querySelector('.board-card.dragging')).opacity) < 0.8`,
		"the column under the pointer has the ring": `getComputedStyle(document.querySelectorAll('.board-column')[1]).boxShadow.includes('2px')`,
		"the list has the outline":                  `getComputedStyle(document.querySelector('.board-cards.drop-over')).outlineStyle === 'dashed'`,
		"the card after the place has the bar":      `getComputedStyle(document.querySelector('.board-card.drop-before')).boxShadow.startsWith('rgb') && getComputedStyle(document.querySelector('.board-card.drop-before')).boxShadow.includes('-4px')`,
	}
	for what, expr := range checks {
		if err := waitFor(ctx, expr); err != nil {
			t.Errorf("%s: %v", what, err)
		}
	}
}

// TestPagesOfTheBoardMessagesSearchAndNotificationsFitAPhone: at 390 by 844
// none of these pages is wider than the screen, the message bubbles are at most
// 85% of the list, and what is touched is at least 44 px high.
func TestPagesOfTheBoardMessagesSearchAndNotificationsFitAPhone(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()

	if err := browsertest.LogIn(ctx, srv, "alice"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}
	if err := chromedp.Do(ctx, chromedp.EmulateViewport(390, 844), chromedp.Navigate(srv.URL+"/rooms")); err != nil {
		t.Fatalf("resize: %v", err)
	}
	// tall is the JavaScript test that every touchable element of the page
	// matching the selector is at least 44 px high (and wide, when wide).
	tall := func(sel string) string {
		return `(() => { const els = [...document.querySelectorAll(` + strconv.Quote(sel) + `)]; return els.length > 0 && els.every(e => e.getBoundingClientRect().height >= 43.5); })()`
	}
	pages := []struct {
		name    string
		path    string
		visible string
		ready   string
		touch   []string
	}{
		{"the board", "", "#board-columns .board-card", `document.querySelector('#board-columns .board-card') !== null`,
			[]string{".column-rename-button", ".column-menu > summary", ".card-links a", ".card-move select", ".card-move button", "#board-add-form button"}},
		{"the Messages list", "/messages", "#conversation-list li.conversation", `document.querySelector('#conversation-list li.conversation') !== null`,
			[]string{".conversation-link", "#message-search button", "#new-conversation-form button"}},
		{"a conversation", "", "#message-list li.message", `document.querySelector('#message-list li.message') !== null`,
			[]string{".conversation-menu > summary", ".message-footer a", "#message-composer-form button", ".attach-button"}},
		{"the search", "/search?q=%23design", "#search-panel .search-result", `document.querySelector('#search-panel .search-result') !== null`,
			[]string{"#search-form button", ".search-open"}},
		{"the notifications", "/notifications", "#notification-list .notification", `document.querySelector('#notification-list .notification') !== null`,
			[]string{".notification-link", ".mark-all-form button"}},
	}
	for _, p := range pages {
		switch p.name {
		case "the board":
			if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/rooms"), chromedp.WaitVisible(".room-card")); err != nil {
				t.Fatalf("%s: %v", p.name, err)
			}
			href, err := chromedp.Run(ctx, chromedp.Evaluate[string](`document.querySelector('.room-card[data-room="general"] h2 a').getAttribute('href')`))
			if err != nil {
				t.Fatalf("%s: %v", p.name, err)
			}
			if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+href+"/board")); err != nil {
				t.Fatalf("%s: %v", p.name, err)
			}
		case "a conversation":
			if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/messages"), chromedp.WaitVisible(`#conversation-list li.conversation`)); err != nil {
				t.Fatalf("%s: %v", p.name, err)
			}
			if err := chromedp.Do(ctx, chromedp.Click(`#conversation-list li.conversation[data-members="`+crewConversation+`"] a.conversation-link`)); err != nil {
				t.Fatalf("%s: %v", p.name, err)
			}
		default:
			if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+p.path)); err != nil {
				t.Fatalf("%s: %v", p.name, err)
			}
		}
		// The page has loaded when its first content is there.
		if err := chromedp.Do(ctx, chromedp.WaitVisible(p.visible)); err != nil {
			t.Fatalf("%s did not load: %v", p.name, err)
		}
		if err := waitFor(ctx, p.ready+` && `+fitsScreen); err != nil {
			t.Errorf("%s is wider than the screen (or did not load): %v", p.name, err)
			continue
		}
		for _, sel := range p.touch {
			if err := waitFor(ctx, tall(sel)); err != nil {
				t.Errorf("%s: %s is under 44 px: %v", p.name, sel, err)
			}
		}
		if p.name == "a conversation" {
			if err := waitFor(ctx, `(() => { const list = document.getElementById('message-list').getBoundingClientRect().width; return [...document.querySelectorAll('#message-list li.message')].every(m => m.getBoundingClientRect().width <= list * 0.85 + 1); })()`); err != nil {
				t.Errorf("a message bubble is wider than 85%% of the list: %v", err)
			}
		}
	}
}

// TestSettingsSessionsAndAdminFitAPhone: at 390 by 844 neither the settings
// page (with its sessions), nor the admin pages is wider than the screen, what
// is touched is at least 44 px high, and the table of users, which is wider than
// the screen, scrolls inside its own card.
func TestSettingsSessionsAndAdminFitAPhone(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()

	if err := browsertest.LogIn(ctx, srv, "admin"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}
	if err := chromedp.Do(ctx, chromedp.EmulateViewport(390, 844), chromedp.Navigate(srv.URL+"/rooms")); err != nil {
		t.Fatalf("resize: %v", err)
	}
	tall := func(sel string) string {
		return `(() => { const els = [...document.querySelectorAll(` + strconv.Quote(sel) + `)]; return els.length > 0 && els.every(e => e.getBoundingClientRect().height >= 43.5); })()`
	}
	pages := []struct {
		name    string
		path    string
		visible string
		touch   []string
	}{
		{"the settings", "/me/settings", "#profile-form",
			[]string{".settings-nav a", ".theme-choice", "#profile-form .card-foot button", ".choice-pills .choice-pill", ".switch", "#sessions .session-end button"}},
		{"the old sessions page", "/me/sessions", "#sessions li.session", []string{"#sessions .session-end button"}},
		{"the users", "/admin/users", "#users-panel table.users", []string{".admin-tabs a", ".row-actions button", "#invite-form button"}},
		{"the rooms", "/admin/rooms", "#admin-rooms li.admin-room", []string{".admin-tabs a", ".admin-room summary", ".admin-room .room-actions button"}},
		{"the audit log", "/admin/audit", "#audit-panel table.audit", []string{".admin-tabs a", "#audit-filter button", "#audit-filter .button-link"}},
	}
	for _, p := range pages {
		if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+p.path), chromedp.WaitVisible(p.visible)); err != nil {
			t.Fatalf("%s did not load: %v", p.name, err)
		}
		if err := waitFor(ctx, fitsScreen); err != nil {
			t.Errorf("%s is wider than the screen: %v", p.name, err)
		}
		for _, sel := range p.touch {
			if err := waitFor(ctx, tall(sel)); err != nil {
				t.Errorf("%s: %s is under 44 px: %v", p.name, sel, err)
			}
		}
	}
	// The users and the audit log: the card scrolls sideways, the page does not.
	for _, c := range []struct{ path, wait, card string }{
		{"/admin/users", "#users-panel table.users", "#users-panel .table-card"},
		{"/admin/audit", "#audit-panel table.audit", "#audit-panel .table-card"},
	} {
		if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+c.path), chromedp.WaitVisible(c.wait)); err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		card := `document.querySelector(` + strconv.Quote(c.card) + `)`
		if err := waitFor(ctx, card+`.scrollWidth > `+card+`.clientWidth + 50 && getComputedStyle(`+card+`).overflowX === 'auto' && `+fitsScreen); err != nil {
			t.Errorf("%s: the table does not scroll inside its card: %v", c.path, err)
		}
	}
}

// TestConversationMenuOpensTheNotificationPreferences: the "more" menu of a
// conversation holds the notification preferences; the button opens the dialog
// and closes the menu, and a patch of the header (a rename) leaves the menu
// where it is.
func TestConversationMenuOpensTheNotificationPreferences(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()

	if err := browsertest.LogIn(ctx, srv, "alice"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}
	openConversation(t, ctx, crewConversation)
	if n := countOf(t, ctx, "#conversation-header .conversation-person"); n != 2 {
		t.Errorf("the header shows %d people, want 2 (Bob and Carol)", n)
	}
	menu := ".conversation-actions .conversation-menu"
	if err := waitFor(ctx, `!document.querySelector('`+menu+`').open`); err != nil {
		t.Fatalf("the menu is open at the start: %v", err)
	}
	err := chromedp.Do(ctx, chromedp.Click(menu+" > summary"), chromedp.WaitVisible(menu+"[open] #place-prefs-open"), chromedp.Click("#place-prefs-open"), chromedp.WaitVisible("#place-prefs-dialog[open]"))
	if err != nil {
		t.Fatalf("open the preferences from the menu: %v", err)
	}
	if err := waitFor(ctx, `!document.querySelector('`+menu+`').open`); err != nil {
		t.Errorf("the menu stayed open behind the dialog: %v", err)
	}
	if err := chromedp.Do(ctx, chromedp.Click(`#place-prefs-dialog .place-prefs-close button`)); err != nil {
		t.Fatalf("close the dialog: %v", err)
	}
	// A rename patches the header; the menu is not part of it and is still there.
	err = chromedp.Do(ctx, browsertest.Fill("#conversation-rename-name", "Renamed crew"), chromedp.Click(`#conversation-rename-form button[type="submit"]`))
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := waitFor(ctx, `document.querySelector('#conversation-header h1').textContent === 'Renamed crew' && document.querySelector('#place-prefs-open') !== null`); err != nil {
		t.Errorf("the header did not change, or the preferences went away with it: %v", err)
	}
}
