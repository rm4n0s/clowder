//go:build clowder_browser

package web_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/clowder/internal/browsertest"
)

// streamSettle is how long a test lets a page open its live stream before
// another user acts. A change made before that is not lost (the stream finds
// out that the page is behind), but these tests are about the live updates.
const streamSettle = 700 * time.Millisecond

// secondBrowser starts another headless Chromium, logged in as the user, and
// closes it when the test ends.
func secondBrowser(t *testing.T, srv *browsertest.Server, user string) context.Context {
	t.Helper()
	browser, cancel, err := browsertest.NewBrowser(context.Background())
	if err != nil {
		t.Fatalf("second browser: %v", err)
	}
	t.Cleanup(cancel)
	ctx, cancelCtx := context.WithTimeout(browser, pageTimeout)
	t.Cleanup(cancelCtx)
	if err := browsertest.LogIn(ctx, srv, user); err != nil {
		t.Fatalf("LogIn %s: %+v", user, err)
	}
	return ctx
}

// countOf counts the elements that match the selector.
func countOf(t *testing.T, ctx context.Context, sel string) int {
	t.Helper()
	n, err := chromedp.Run(ctx, chromedp.Evaluate[int](`document.querySelectorAll(`+strconv.Quote(sel)+`).length`))
	if err != nil {
		t.Fatalf("count %s: %v", sel, err)
	}
	return n
}

// TestLiveNewPostReachesAnotherPage has alice and bob in "general": alice
// writes a post and bob sees it at the top of his feed without a reload, while
// alice sees it exactly once.
func TestLiveNewPostReachesAnotherPage(t *testing.T) {
	srv := browsertest.Start(t)
	bob := secondBrowser(t, srv, "bob")
	alice := secondBrowser(t, srv, "alice")
	for _, ctx := range []context.Context{bob, alice} {
		openGeneral(t, ctx)
	}
	// Bob's page must not reload: this marker would be gone.
	runJS(t, bob, `window.notReloaded = true; true`)
	if err := chromedp.Do(bob, chromedp.Sleep(streamSettle)); err != nil {
		t.Fatal(err)
	}

	const text = "Live from alice, no reload needed"
	err := chromedp.Do(alice,
		browsertest.Fill("#post-body-new", text),
		chromedp.Click(`#post-editor-new button[type="submit"]`),
	)
	if err != nil {
		t.Fatalf("alice posts: %v", err)
	}
	first := `#feed-posts article.post:first-child`
	if err := waitFor(bob, `document.querySelector('`+first+` .post-body') !== null && document.querySelector('`+first+` .post-body').textContent.includes(`+strconv.Quote(text)+`)`); err != nil {
		t.Fatalf("bob does not see the post at the top of his feed: %v", err)
	}
	if got := textOf(t, bob, first+" .post-author"); got != "Alice" {
		t.Errorf("author seen by bob = %q", got)
	}
	// Bob is not the author: no Edit or Delete on the card.
	if n := countOf(t, bob, first+" .post-edit, "+first+" .post-delete"); n != 0 {
		t.Errorf("bob's card of alice's post has %d author buttons", n)
	}
	if ok, err := chromedp.Run(bob, chromedp.Evaluate[bool](`window.notReloaded === true`)); err != nil || !ok {
		t.Errorf("bob's page was reloaded (%v)", err)
	}

	// Alice has the post once: the answer to her form and the event of the
	// stream both brought it. Wait for both to have arrived.
	if err := waitFor(alice, `document.querySelectorAll('#feed-posts article.post').length === 6`); err != nil {
		t.Fatalf("alice's feed does not hold six posts: %v", err)
	}
	if err := chromedp.Do(alice, chromedp.Sleep(streamSettle)); err != nil {
		t.Fatal(err)
	}
	if got := countOf(t, alice, `#feed-posts article.post`); got != 6 {
		t.Errorf("alice's feed holds %d posts, want 6", got)
	}
	if got, err := chromedp.Run(alice, chromedp.Evaluate[int](
		`[...document.querySelectorAll('#feed-posts article.post')].filter(a => a.textContent.includes(`+strconv.Quote(text)+`)).length`)); err != nil || got != 1 {
		t.Errorf("alice's feed shows the post %d times, want once (%v)", got, err)
	}
	if got, err := chromedp.Run(alice, chromedp.Evaluate[int](`new Set([...document.querySelectorAll('#feed-posts article.post')].map(a => a.id)).size`)); err != nil || got != 6 {
		t.Errorf("alice's feed has %d different cards, want 6 (%v)", got, err)
	}
}

// openDesignNotesThread opens the thread of the demo post about the design
// notes and returns the id of its article.
func openDesignNotesThread(t *testing.T, ctx context.Context) string {
	t.Helper()
	openGeneral(t, ctx)
	postID := idOfArticle(t, ctx, ".post", "where do we keep the design notes")
	if err := chromedp.Do(ctx, chromedp.Click("#"+postID+" a.post-comments"), chromedp.WaitVisible("#comments")); err != nil {
		t.Fatalf("open the thread: %v", err)
	}
	return postID
}

// TestLiveCommentReachesAThreadAndKeepsAnOpenEditor has bob on the thread of a
// post with his reply editor open and half written, and alice commenting: bob
// sees the comment in its place at once and still has his editor and his text.
func TestLiveCommentReachesAThreadAndKeepsAnOpenEditor(t *testing.T) {
	srv := browsertest.Start(t)
	bob := secondBrowser(t, srv, "bob")
	alice := secondBrowser(t, srv, "alice")
	postID := openDesignNotesThread(t, bob)
	openDesignNotesThread(t, alice)
	if err := waitFor(bob, `document.querySelectorAll('article.comment').length === 5`); err != nil {
		t.Fatalf("the demo thread does not hold five comments: %v", err)
	}

	// Bob starts a reply to the post and types, without sending it.
	slot := "#" + postID + " .reply-slot"
	err := chromedp.Do(bob,
		chromedp.Click("#"+postID+" .post-reply"),
		chromedp.WaitVisible(slot+" textarea"),
		browsertest.Fill(slot+" textarea", "Half a thought"),
	)
	if err != nil {
		t.Fatalf("bob opens his reply editor: %v", err)
	}
	runJS(t, bob, `window.notReloaded = true; true`)
	if err := chromedp.Do(bob, chromedp.Sleep(streamSettle)); err != nil {
		t.Fatal(err)
	}

	// Alice answers the last comment of the thread.
	lastID, err := chromedp.Run(alice, chromedp.Evaluate[string](`[...document.querySelectorAll('article.comment')].at(-1).id`))
	if err != nil {
		t.Fatalf("find the last comment: %v", err)
	}
	replyTo(t, alice, lastID, "Alice live comment")

	if err := waitFor(bob, `[...document.querySelectorAll('article.comment')].some(a => a.textContent.includes('Alice live comment'))`); err != nil {
		t.Fatalf("bob does not see alice's comment: %v", err)
	}
	if got := countOf(t, bob, "article.comment"); got != 6 {
		t.Errorf("bob's thread holds %d comments, want 6", got)
	}
	// It sits right after the comment it answers, one level deeper.
	newID := idOfArticle(t, bob, ".comment", "Alice live comment")
	if err := waitFor(bob, `document.getElementById(`+strconv.Quote(lastID)+`).nextElementSibling.id === `+strconv.Quote(newID)); err != nil {
		t.Errorf("the new comment does not follow the one it answers: %v", err)
	}
	if a, b := leftOf(t, bob, lastID), leftOf(t, bob, newID); b <= a {
		t.Errorf("the new comment (left %v) is not indented under its parent (left %v)", b, a)
	}
	if err := waitFor(bob, `document.querySelector('#`+postID+` a.post-comments').textContent === '5 comments'`); err != nil {
		t.Errorf("the comment count of the post did not grow: %v", err)
	}
	// His editor is still open, with his text.
	if got := valueOf(t, bob, slot+" textarea"); got != "Half a thought" {
		t.Errorf("bob's reply editor holds %q, want his text", got)
	}
	if ok, err := chromedp.Run(bob, chromedp.Evaluate[bool](`window.notReloaded === true`)); err != nil || !ok {
		t.Errorf("bob's page was reloaded (%v)", err)
	}
}

// TestLiveRemovalSendsAMemberAway has bob on the private room "secret" and
// the admin removing him from it: bob's page goes to the home page, which no
// longer lists the room.
func TestLiveRemovalSendsAMemberAway(t *testing.T) {
	srv := browsertest.Start(t)
	bob := secondBrowser(t, srv, "bob")
	admin := secondBrowser(t, srv, "admin")
	err := chromedp.Do(bob, chromedp.Click(`aside a[data-room="secret"]`), chromedp.WaitVisible("#post-editor-new"))
	if err != nil {
		t.Fatalf("bob opens secret: %v", err)
	}
	if err := chromedp.Do(bob, chromedp.Sleep(streamSettle)); err != nil {
		t.Fatal(err)
	}

	// The admin removes him on the rooms page; the dialog that asks is answered yes.
	secret := `li.admin-room[data-room="secret"]`
	bobRow := secret + ` li[data-username="bob"]`
	err = chromedp.Do(admin,
		chromedp.Navigate(srv.URL+"/admin/rooms"), chromedp.WaitVisible("#admin-rooms"),
		chromedp.Click(secret+` details.admin-room-members summary`), chromedp.WaitVisible(bobRow),
	)
	if err != nil {
		t.Fatalf("the admin opens the members of secret: %v", err)
	}
	runJS(t, admin, `window.confirm = () => true; true`)
	if err := chromedp.Do(admin, chromedp.Click(bobRow+` button`)); err != nil {
		t.Fatalf("the admin removes bob: %v", err)
	}

	// The home page is the only one with this element; the wait is for the
	// navigation, which a poll of the old page would not survive.
	if err := chromedp.Do(bob, chromedp.WaitVisible("#signed-in-as")); err != nil {
		t.Fatalf("bob was not sent to the home page: %v", err)
	}
	if n := countOf(t, bob, `aside a[data-room="secret"]`); n != 0 {
		t.Errorf("the sidebar of bob still lists secret")
	}
}

// TestLiveSidebarFollowsMembership has carol on the page of a thread and alice
// adding her to the private room "secret": the sidebar of carol's page lists
// the room without a reload.
func TestLiveSidebarFollowsMembership(t *testing.T) {
	srv := browsertest.Start(t)
	carol := secondBrowser(t, srv, "carol")
	alice := secondBrowser(t, srv, "alice")
	openDesignNotesThread(t, carol)
	if n := countOf(t, carol, `aside a[data-room="secret"]`); n != 0 {
		t.Fatalf("carol sees secret before she is added")
	}
	if err := chromedp.Do(carol, chromedp.Sleep(streamSettle)); err != nil {
		t.Fatal(err)
	}

	err := chromedp.Do(alice, chromedp.Click(`aside a[data-room="secret"]`), chromedp.WaitVisible("#add-member-form"))
	if err != nil {
		t.Fatalf("alice opens secret: %v", err)
	}
	carolID, err := chromedp.Run(alice, chromedp.Evaluate[string](
		`[...document.querySelectorAll('#add-member-user option')].find(o => o.textContent.includes('@carol')).value`))
	if err != nil {
		t.Fatalf("find carol in the candidates: %v", err)
	}
	err = chromedp.Do(alice, chromedp.SetValue("#add-member-user", carolID), chromedp.Click(`#add-member-form button[type="submit"]`))
	if err != nil {
		t.Fatalf("alice adds carol: %v", err)
	}
	if err := waitFor(carol, `document.querySelector('aside a[data-room="secret"]') !== null`); err != nil {
		t.Fatalf("carol's sidebar does not list secret: %v", err)
	}
}
