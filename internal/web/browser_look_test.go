//go:build clowder_browser

package web_test

import (
	"context"
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
