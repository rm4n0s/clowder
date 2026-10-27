//go:build clowder_browser

package web_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/clowder/internal/browsertest"
)

// badgeOf returns the number on the badge of the Notifications link, 0 when it
// has none.
func badgeOf(t *testing.T, ctx context.Context) int {
	t.Helper()
	n, err := chromedp.Run(ctx, chromedp.Evaluate[int](`parseInt(document.querySelector('#notification-count').textContent || '0', 10)`))
	if err != nil {
		t.Fatalf("read the badge: %v", err)
	}
	return n
}

// waitForBadge waits until the badge shows n.
func waitForBadge(t *testing.T, ctx context.Context, n int) {
	t.Helper()
	if err := waitFor(ctx, `parseInt(document.querySelector('#notification-count').textContent || '0', 10) === `+strconv.Itoa(n)); err != nil {
		t.Fatalf("the badge does not show %d (it shows %d): %v", n, badgeOf(t, ctx), err)
	}
}

// alicePostsAMention has alice write a post in "general" that mentions bob.
func alicePostsAMention(t *testing.T, alice context.Context, text string) {
	t.Helper()
	err := chromedp.Do(alice,
		browsertest.Fill("#post-body-new", "Hey @bob, "+text),
		chromedp.Click(`#post-editor-new button[type="submit"]`),
	)
	if err != nil {
		t.Fatalf("alice posts: %v", err)
	}
}

// TestBellGoesUpOnTheRoomPage has bob read "general" while alice mentions him
// in a post there: his badge goes up without a reload.
func TestBellGoesUpOnTheRoomPage(t *testing.T) {
	srv := browsertest.Start(t)
	bob := secondBrowser(t, srv, "bob")
	alice := secondBrowser(t, srv, "alice")
	openGeneral(t, bob)
	openGeneral(t, alice)
	before := badgeOf(t, bob)
	runJS(t, bob, `window.notReloaded = true; true`)
	if err := chromedp.Do(bob, chromedp.Sleep(streamSettle)); err != nil {
		t.Fatal(err)
	}

	alicePostsAMention(t, alice, "look at this")
	waitForBadge(t, bob, before+1)
	if ok, err := chromedp.Run(bob, chromedp.Evaluate[bool](`window.notReloaded === true`)); err != nil || !ok {
		t.Errorf("bob's page was reloaded (%v)", err)
	}
}

// TestBellGoesUpOnThePagesWithoutAStream has bob on the search page, which has
// no stream of its own: it opens /me/stream, and the badge goes up there too.
func TestBellGoesUpOnThePagesWithoutAStream(t *testing.T) {
	srv := browsertest.Start(t)
	bob := secondBrowser(t, srv, "bob")
	alice := secondBrowser(t, srv, "alice")
	if err := chromedp.Do(bob, chromedp.Navigate(srv.URL+"/search"), chromedp.WaitVisible("#search-form")); err != nil {
		t.Fatalf("bob opens the search: %v", err)
	}
	openGeneral(t, alice)
	before := badgeOf(t, bob)
	runJS(t, bob, `window.notReloaded = true; true`)
	if err := chromedp.Do(bob, chromedp.Sleep(streamSettle)); err != nil {
		t.Fatal(err)
	}

	alicePostsAMention(t, alice, "on the search page")
	waitForBadge(t, bob, before+1)
	if ok, err := chromedp.Run(bob, chromedp.Evaluate[bool](`window.notReloaded === true`)); err != nil || !ok {
		t.Errorf("bob's page was reloaded (%v)", err)
	}
}

// TestBobReadsHisNotifications has bob open the notifications page from the
// bell: the mention is there with its unread mark, the badge drops to 0, a new
// mention comes in at the top live, and "Mark all as read" clears the marks.
func TestBobReadsHisNotifications(t *testing.T) {
	srv := browsertest.Start(t)
	bob := secondBrowser(t, srv, "bob")
	alice := secondBrowser(t, srv, "alice")
	openGeneral(t, alice)
	alicePostsAMention(t, alice, "a note for you")
	if err := chromedp.Do(alice, chromedp.WaitVisible(`#feed-posts article.post:first-child`)); err != nil {
		t.Fatal(err)
	}

	if err := chromedp.Do(bob, chromedp.Navigate(srv.URL+"/"), chromedp.WaitVisible("#notification-count")); err != nil {
		t.Fatalf("bob opens the home page: %v", err)
	}
	if err := waitFor(bob, `parseInt(document.querySelector('#notification-count').textContent || '0', 10) > 0`); err != nil {
		t.Fatalf("bob's badge is empty: %v", err)
	}
	err := chromedp.Do(bob, chromedp.Click(`aside a.sidebar-notifications`), chromedp.WaitVisible("#notification-list .notification"))
	if err != nil {
		t.Fatalf("bob opens the notifications: %v", err)
	}
	if got := locationOf(t, bob); got != "/notifications" {
		t.Errorf("location = %q", got)
	}
	// The newest one is the mention by alice, with its mark; the badge is 0.
	first := `#notification-list .notification:first-child`
	if got := textOf(t, bob, first+" .notification-text"); got != "Alice @alice mentioned you in general" {
		t.Errorf("first notification = %q", got)
	}
	if n := countOf(t, bob, first+".unread"); n != 1 {
		t.Errorf("the first notification is not marked unread")
	}
	if n := countOf(t, bob, first+" .notification-mark"); n != 1 {
		t.Errorf("the unread mark is missing")
	}
	waitForBadge(t, bob, 0)

	// Its link leads to the post.
	if got, err := chromedp.Run(bob, chromedp.Evaluate[string](`document.querySelector('`+first+` a.notification-link').getAttribute('href')`)); err != nil || len(got) < len("/posts/1#post-1") || got[:7] != "/posts/" {
		t.Errorf("link = %q (%v)", got, err)
	}

	// A new mention comes in at the top, unread, and raises the badge; the
	// page is not reloaded.
	runJS(t, bob, `window.notReloaded = true; true`)
	if err := chromedp.Do(bob, chromedp.Sleep(streamSettle)); err != nil {
		t.Fatal(err)
	}
	before := countOf(t, bob, "#notification-list .notification")
	alicePostsAMention(t, alice, "and another")
	if err := waitFor(bob, `document.querySelectorAll('#notification-list .notification').length === `+strconv.Itoa(before+1)); err != nil {
		t.Fatalf("the new notification did not come in: %v", err)
	}
	if n := countOf(t, bob, first+".unread"); n != 1 {
		t.Errorf("the new notification on top is not marked unread")
	}
	waitForBadge(t, bob, 1)
	if ok, err := chromedp.Run(bob, chromedp.Evaluate[bool](`window.notReloaded === true`)); err != nil || !ok {
		t.Errorf("bob's page was reloaded (%v)", err)
	}

	// Mark all as read: no marks, badge 0, still the same page.
	if err := chromedp.Do(bob, chromedp.Click(`#mark-all-form button`)); err != nil {
		t.Fatalf("bob clicks Mark all as read: %v", err)
	}
	if err := waitFor(bob, `document.querySelectorAll('#notification-list .notification').length === `+strconv.Itoa(before+1)+` && [...document.querySelectorAll('#notification-list .notification')].every(li => !li.classList.contains('unread'))`); err != nil {
		t.Fatalf("the unread marks did not go: %v", err)
	}
	waitForBadge(t, bob, 0)
	if err := waitFor(bob, `[...document.querySelectorAll('.notification-mark')].every(m => getComputedStyle(m).display === 'none')`); err != nil {
		t.Errorf("an unread mark is still shown: %v", err)
	}
	if ok, err := chromedp.Run(bob, chromedp.Evaluate[bool](`window.notReloaded === true`)); err != nil || !ok {
		t.Errorf("bob's page was reloaded (%v)", err)
	}
}

// TestAssignmentOnTheBoardRaisesTheBadge has bob on the board of "general"
// while alice adds him to a card: his badge goes up live.
func TestAssignmentOnTheBoardRaisesTheBadge(t *testing.T) {
	srv := browsertest.Start(t)
	bob := secondBrowser(t, srv, "bob")
	alice := secondBrowser(t, srv, "alice")
	openBoard(t, bob)
	openBoard(t, alice)
	before := badgeOf(t, bob)

	const card = "Tag the release"
	err := chromedp.Do(alice, chromedp.Click(cardSel(t, alice, card)+` a.card-details`), chromedp.WaitVisible("#board-editor #board-card-form"))
	if err != nil {
		t.Fatalf("alice opens the card: %v", err)
	}
	runJS(t, alice, `(() => {
		const sel = document.querySelector('#board-card-form select[name="assignees"]');
		[...sel.options].forEach(o => { if (o.textContent.includes('@bob')) { o.selected = true; } });
		return [...sel.options].some(o => o.selected && o.textContent.includes('@bob'));
	})()`)
	if err := chromedp.Do(alice, chromedp.Click(`#board-card-form button[type="submit"]`)); err != nil {
		t.Fatalf("alice saves the card: %v", err)
	}
	waitForBadge(t, bob, before+1)

	// And bob's notifications say so.
	if err := chromedp.Do(bob, chromedp.Navigate(srv.URL+"/notifications"), chromedp.WaitVisible("#notification-list .notification")); err != nil {
		t.Fatalf("bob opens the notifications: %v", err)
	}
	if got := textOf(t, bob, `#notification-list .notification:first-child .notification-text`); got != `Alice @alice assigned you "Tag the release" in general` {
		t.Errorf("first notification = %q", got)
	}
}
