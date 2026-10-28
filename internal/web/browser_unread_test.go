//go:build clowder_browser

package web_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/clowder/internal/browsertest"
)

// generalItem is the list item of "general" in the sidebar.
const generalItem = `#sidebar-rooms li:has(a[data-room="general"])`

// alicePostsPlain has alice write a post in "general" that mentions nobody.
func alicePostsPlain(t *testing.T, alice context.Context, text string) {
	t.Helper()
	err := chromedp.Do(alice,
		browsertest.Fill("#post-body-new", text),
		chromedp.Click(`#post-editor-new button[type="submit"]`),
	)
	if err != nil {
		t.Fatalf("alice posts: %v", err)
	}
}

// tabOf returns the title of the page and the href of its favicon.
func tabOf(t *testing.T, ctx context.Context) (title, icon string) {
	t.Helper()
	got, err := chromedp.Run(ctx, chromedp.Evaluate[string](`document.title + '|' + document.getElementById('favicon').getAttribute('href')`))
	if err != nil {
		t.Fatalf("read the tab: %v", err)
	}
	title, icon, _ = strings.Cut(got, "|")
	return title, icon
}

// TestUnreadCountsGoUpLiveAndClearOnOpening has bob on the search page while
// alice posts in "general": the sidebar item of the room shows the unread
// count without a reload; a post that mentions bob adds the mention badge and
// raises the number in the tab and keeps its dot; opening the room clears all of them.
func TestUnreadCountsGoUpLiveAndClearOnOpening(t *testing.T) {
	srv := browsertest.Start(t)
	bob := secondBrowser(t, srv, "bob")
	alice := secondBrowser(t, srv, "alice")
	// The seeded room has posts and mentions that bob has not seen: he opens
	// the room once, so that he starts from nothing unread, and leaves it.
	openGeneral(t, bob)
	if err := chromedp.Do(bob, chromedp.Navigate(srv.URL+"/search"), chromedp.WaitVisible("#search-form")); err != nil {
		t.Fatalf("bob opens the search: %v", err)
	}
	openGeneral(t, alice)
	runJS(t, bob, `window.notReloaded = true; true`)
	if err := chromedp.Do(bob, chromedp.Sleep(streamSettle)); err != nil {
		t.Fatal(err)
	}
	if n := countOf(t, bob, generalItem+" .count-badge"); n != 0 {
		t.Fatalf("badges in general at the start = %d, want 0", n)
	}
	// The seeded data leaves bob with notifications of other rooms: the tab
	// shows them already.
	waiting, err := chromedp.Run(bob, chromedp.Evaluate[int](`parseInt((document.title.match(/^\((\d+)\) /) || [0, '0'])[1], 10)`))
	if err != nil {
		t.Fatalf("read the title: %v", err)
	}
	title, icon := tabOf(t, bob)
	t.Logf("tab at the start: title %q, favicon %q", title, icon)
	if (waiting > 0) != strings.Contains(icon, "dot") {
		t.Fatalf("tab at the start = %q, %q", title, icon)
	}

	alicePostsPlain(t, alice, "Nothing for anyone")
	if err := waitFor(bob, `document.querySelector(`+"`"+generalItem+" .room-unread`"+`)?.textContent.trim() === '1'`); err != nil {
		t.Fatalf("general does not show 1 unread post: %v", err)
	}
	if n := countOf(t, bob, generalItem+".unread"); n != 1 {
		t.Errorf("general is not bold (%d)", n)
	}
	if got, _ := tabOf(t, bob); got != title {
		t.Errorf("a plain post changed the title: %q, was %q", got, title)
	}

	alicePostsAMention(t, alice, "this one is for you")
	if err := waitFor(bob, `document.querySelector(`+"`"+generalItem+" .room-unread`"+`)?.textContent.trim() === '2'`); err != nil {
		t.Fatalf("general does not show 2 unread posts: %v", err)
	}
	if err := waitFor(bob, `document.querySelector(`+"`"+generalItem+" .mention-badge`"+`)?.textContent.trim() === '1'`); err != nil {
		t.Fatalf("general does not show 1 mention: %v", err)
	}
	if err := waitFor(bob, `document.title.startsWith('(`+strconv.Itoa(waiting+1)+`) ') && document.getElementById('favicon').getAttribute('href').includes('dot')`); err != nil {
		title, icon := tabOf(t, bob)
		t.Fatalf("the tab does not show the waiting notification: %q, %q: %v", title, icon, err)
	}

	title, icon = tabOf(t, bob)
	t.Logf("tab after the mention: title %q, favicon %q", title, icon)

	openGeneral(t, bob)
	if err := waitFor(bob, `document.querySelectorAll(`+"`"+generalItem+" .count-badge`"+`).length === 0`); err != nil {
		t.Fatalf("general still shows counts after opening it: %v", err)
	}
	if err := waitFor(bob, `document.title.startsWith(`+strconv.Quote("("+strconv.Itoa(waiting)+") ")+`) || (`+strconv.Itoa(waiting)+` === 0 && !document.title.startsWith('('))`); err != nil {
		title, icon := tabOf(t, bob)
		t.Fatalf("the tab still shows something waiting: %q, %q: %v", title, icon, err)
	}

	title, icon = tabOf(t, bob)
	t.Logf("tab after opening the room: title %q, favicon %q", title, icon)

	// The feed marks where the posts that were new to bob start.
	if n := countOf(t, bob, ".feed-new-line"); n != 1 {
		t.Errorf("lines in the feed = %d, want 1", n)
	}
}
