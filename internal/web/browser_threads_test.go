//go:build clowder_browser

package web_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/clowder/internal/browsertest"
)

// idOfArticle returns the id of the first article whose text contains text and
// whose class list contains class ("" for any).
func idOfArticle(t *testing.T, ctx context.Context, class, text string) string {
	t.Helper()
	js := `(() => { const a = [...document.querySelectorAll('article` + class + `')].find(a => a.textContent.includes(` + strconv.Quote(text) + `)); return a ? a.id : ""; })()`
	id, err := chromedp.Run(ctx, chromedp.Evaluate[string](js))
	if err != nil || id == "" {
		t.Fatalf("no article%s with %q (id %q): %v", class, text, id, err)
	}
	return id
}

// leftOf returns the distance of the left edge of the article from the left
// edge of the page.
func leftOf(t *testing.T, ctx context.Context, id string) float64 {
	t.Helper()
	left, err := chromedp.Run(ctx, chromedp.Evaluate[float64](`document.getElementById(`+strconv.Quote(id)+`).getBoundingClientRect().left`))
	if err != nil {
		t.Fatalf("position of %s: %v", id, err)
	}
	return left
}

// replyTo opens the reply editor under the item, types the text and submits it,
// and waits until the thread shows the new comment and the editor is closed.
func replyTo(t *testing.T, ctx context.Context, itemID, text string) {
	t.Helper()
	slot := "#" + itemID + " .reply-slot"
	err := chromedp.Do(ctx,
		chromedp.Click("#"+itemID+" .post-reply"),
		chromedp.WaitVisible(slot+" textarea"),
		browsertest.Fill(slot+" textarea", text),
		chromedp.Click(slot+` button[type="submit"]`),
	)
	if err != nil {
		t.Fatalf("reply to %s: %v", itemID, err)
	}
	if err := waitFor(ctx, `[...document.querySelectorAll('article.comment')].some(a => a.textContent.includes(`+strconv.Quote(text)+`))`); err != nil {
		t.Fatalf("the reply %q is not in the thread: %v", text, err)
	}
	if err := waitFor(ctx, `document.querySelectorAll('.reply-slot form').length === 0`); err != nil {
		t.Errorf("a reply editor stayed open: %v", err)
	}
}

// TestBobAndCarolReplyInAThread has bob open the demo thread under his
// question, cancel a reply and then reply to alice's comment, sees his reply
// indented under hers; then carol, in a second browser, replies to bob's reply
// and the thread shows three levels, each more indented than the last.
func TestBobAndCarolReplyInAThread(t *testing.T) {
	srv := browsertest.Start(t)
	bob := browsertest.Browser(t, srv)
	bobCtx, cancelBob := context.WithTimeout(bob, pageTimeout)
	defer cancelBob()

	if err := browsertest.LogIn(bobCtx, srv, "bob"); err != nil {
		t.Fatalf("LogIn bob: %+v", err)
	}
	openGeneral(t, bobCtx)
	// Follow the comment count of the post to its thread.
	postID := idOfArticle(t, bobCtx, ".post", "where do we keep the design notes")
	if err := chromedp.Do(bobCtx, chromedp.Click("#"+postID+" a.post-comments"), chromedp.WaitVisible("#comments")); err != nil {
		t.Fatalf("open the thread: %v", err)
	}
	if err := waitFor(bobCtx, `document.querySelectorAll('article.comment').length === 5`); err != nil {
		t.Fatalf("the demo thread does not hold five comments: %v", err)
	}
	// The deleted comment is a placeholder with its reply below it.
	if got := textOf(t, bobCtx, "article.comment.post-deleted .post-deleted-note"); got != "This comment was deleted." {
		t.Errorf("placeholder = %q", got)
	}
	threadPath, err := chromedp.Run(bobCtx, chromedp.Evaluate[string](`location.pathname`))
	if err != nil {
		t.Fatalf("read the path: %v", err)
	}

	// Reply to alice's comment, which answers the deleted one. Cancel first.
	aliceID := idOfArticle(t, bobCtx, ".comment", "Do you remember where to?")
	err = chromedp.Do(bobCtx,
		chromedp.Click("#"+aliceID+" .post-reply"),
		chromedp.WaitVisible("#"+aliceID+" .reply-slot textarea"),
		chromedp.Click("#"+aliceID+" .reply-slot .post-editor-actions a"),
	)
	if err != nil {
		t.Fatalf("open and cancel the reply: %v", err)
	}
	if err := waitFor(bobCtx, `document.querySelectorAll('.reply-slot form').length === 0`); err != nil {
		t.Fatalf("Cancel did not close the editor: %v", err)
	}
	replyTo(t, bobCtx, aliceID, "Bob browser reply")

	bobID := idOfArticle(t, bobCtx, ".comment", "Bob browser reply")
	if got := textOf(t, bobCtx, "#"+bobID+" .post-author"); got != "Bob" {
		t.Errorf("author of the reply = %q", got)
	}
	if err := waitFor(bobCtx, `document.getElementById(`+strconv.Quote(bobID)+`).classList.contains('depth-3')`); err != nil {
		t.Errorf("bob's reply is not at depth 3: %v", err)
	}
	if a, b := leftOf(t, bobCtx, aliceID), leftOf(t, bobCtx, bobID); b <= a {
		t.Errorf("bob's reply (left %v) is not indented under alice's comment (left %v)", b, a)
	}
	// It comes right after her comment.
	if err := waitFor(bobCtx, `document.getElementById(`+strconv.Quote(aliceID)+`).nextElementSibling.id === `+strconv.Quote(bobID)); err != nil {
		t.Errorf("bob's reply does not follow alice's comment: %v", err)
	}

	// Carol, in a second browser, replies to bob's reply.
	carol, cancelCarol, err := browsertest.NewBrowser(context.Background())
	if err != nil {
		t.Fatalf("second browser: %v", err)
	}
	defer cancelCarol()
	carolCtx, cancelCtx := context.WithTimeout(carol, pageTimeout)
	defer cancelCtx()
	if err := browsertest.LogIn(carolCtx, srv, "carol"); err != nil {
		t.Fatalf("LogIn carol: %+v", err)
	}
	if err := chromedp.Do(carolCtx, chromedp.Navigate(srv.URL+threadPath), chromedp.WaitVisible("#comments")); err != nil {
		t.Fatalf("carol opens the thread: %v", err)
	}
	bobSeenByCarol := idOfArticle(t, carolCtx, ".comment", "Bob browser reply")
	replyTo(t, carolCtx, bobSeenByCarol, "Carol browser reply")

	carolID := idOfArticle(t, carolCtx, ".comment", "Carol browser reply")
	if err := waitFor(carolCtx, `document.getElementById(`+strconv.Quote(carolID)+`).classList.contains('depth-4')`); err != nil {
		t.Errorf("carol's reply is not at depth 4: %v", err)
	}
	// Three levels, each more indented than the one above.
	a, b, c := leftOf(t, carolCtx, aliceID), leftOf(t, carolCtx, bobSeenByCarol), leftOf(t, carolCtx, carolID)
	if !(a < b && b < c) {
		t.Errorf("indentation of alice, bob, carol = %v, %v, %v; want it to grow", a, b, c)
	}
	if err := waitFor(carolCtx, `document.getElementById(`+strconv.Quote(bobSeenByCarol)+`).nextElementSibling.id === `+strconv.Quote(carolID)); err != nil {
		t.Errorf("carol's reply does not follow bob's: %v", err)
	}
	// The comment count of the post grew by two.
	if got := textOf(t, carolCtx, "#"+postID+" a.post-comments"); got != "6 comments" {
		t.Errorf("comment count = %q, want 6 comments", got)
	}
}

// TestCommentLinkFocusesTheComment opens the page of a comment: the whole thread
// of its post shows, with that comment highlighted and scrolled into view.
func TestCommentLinkFocusesTheComment(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()
	if err := browsertest.LogIn(ctx, srv, "alice"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}
	openGeneral(t, ctx)
	postID := idOfArticle(t, ctx, ".post", "where do we keep the design notes")
	if err := chromedp.Do(ctx, chromedp.Click("#"+postID+" a.post-comments"), chromedp.WaitVisible("#comments")); err != nil {
		t.Fatalf("open the thread: %v", err)
	}
	// The time of a comment links to it in its thread; open the page of the comment itself.
	commentID := idOfArticle(t, ctx, ".comment", "Found it in the shared folder")
	number := commentID[len("post-"):]
	if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/posts/"+number), chromedp.WaitVisible("#"+commentID)); err != nil {
		t.Fatalf("open the comment: %v", err)
	}
	if err := waitFor(ctx, `document.querySelectorAll('.post-focus').length === 1 && document.querySelector('.post-focus').id === `+strconv.Quote(commentID)); err != nil {
		t.Fatalf("the comment is not the one in focus: %v", err)
	}
	if err := waitFor(ctx, `document.querySelectorAll('article.comment').length === 5`); err != nil {
		t.Errorf("the whole thread is not shown: %v", err)
	}
	if err := waitFor(ctx, `(() => { const r = document.getElementById(`+strconv.Quote(commentID)+`).getBoundingClientRect(); return r.top >= 0 && r.bottom <= window.innerHeight; })()`); err != nil {
		t.Errorf("the comment is not in view: %v", err)
	}
}
