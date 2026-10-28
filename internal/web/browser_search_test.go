//go:build clowder_browser

package web_test

import (
	"context"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/clowder/internal/browsertest"
)

// locationOf returns the path and query of the page.
func locationOf(t *testing.T, ctx context.Context) string {
	t.Helper()
	got, err := chromedp.Run(ctx, chromedp.Evaluate[string](`location.pathname + location.search`))
	if err != nil {
		t.Fatalf("read the location: %v", err)
	}
	return got
}

// TestAliceSearchesFromATagLink has alice click a tag of a post in "general",
// land on the search page with the demo posts that carry the tag, add a word
// (the results change in place, with the match highlighted and the address
// kept in step), and take the tag away with its chip; then carol searches for
// a word that is only in the private room "secret" and finds nothing.
func TestAliceSearchesFromATagLink(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()

	if err := browsertest.LogIn(ctx, srv, "alice"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}
	openGeneral(t, ctx)
	if err := waitFor(ctx, `document.querySelectorAll('#feed-posts article.post').length === 5`); err != nil {
		t.Fatalf("the demo posts are not in the feed: %v", err)
	}
	// The sidebar has the search box on a page that is not the search page.
	if n := countOf(t, ctx, `#site-header #sidebar-search input[name="q"]`); n != 1 {
		t.Errorf("the sidebar has %d search boxes, want 1", n)
	}

	// The tag link of a post opens the search for the tag.
	tag := `#feed-posts a.tag[href="/search?q=%23design"]`
	if err := chromedp.Do(ctx, chromedp.Click(tag), chromedp.WaitVisible("#search-panel .search-result")); err != nil {
		t.Fatalf("open the search from a tag: %v", err)
	}
	if got := locationOf(t, ctx); got != "/search?q=%23design" {
		t.Errorf("location = %q, want /search?q=%%23design", got)
	}
	if got := valueOf(t, ctx, "#search-q"); got != "#design" {
		t.Errorf("box = %q, want #design", got)
	}
	if got := textOf(t, ctx, "#search-panel .chip-label"); got != "#design" {
		t.Errorf("chip = %q, want #design", got)
	}
	// The three demo posts of "general" with the tag; every result carries it.
	if n := countOf(t, ctx, "#search-panel .search-result"); n != 3 {
		t.Errorf("results = %d, want 3", n)
	}
	if n := countOf(t, ctx, `#search-panel .search-result a.tag[href="/search?q=%23design"]`); n != 3 {
		t.Errorf("results with the tag = %d, want 3", n)
	}
	// The sidebar has no second box here.
	if n := countOf(t, ctx, "#sidebar-search"); n != 0 {
		t.Errorf("the search page has %d sidebar boxes, want 0", n)
	}

	// Adding a word changes the results in place: the page is not reloaded,
	// the box keeps what was typed, and the address follows.
	runJS(t, ctx, `window.notReloaded = true; true`)
	if err := chromedp.Do(ctx, browsertest.Fill("#search-q", "#design review")); err != nil {
		t.Fatalf("type a word: %v", err)
	}
	if err := waitFor(ctx, `document.querySelectorAll('#search-panel .search-result').length === 1`); err != nil {
		t.Fatalf("the results did not narrow to the post about the review: %v", err)
	}
	if got := textOf(t, ctx, "#search-panel .search-snippet mark"); got != "review" {
		t.Errorf("highlight = %q, want review", got)
	}
	if got := textOf(t, ctx, "#search-panel .search-snippet"); got == "" {
		t.Error("the snippet is empty")
	}
	if err := waitFor(ctx, `location.search === '?q=%23design+review'`); err != nil {
		t.Errorf("the address did not follow the box: %s", locationOf(t, ctx))
	}
	if got := valueOf(t, ctx, "#search-q"); got != "#design review" {
		t.Errorf("box = %q, want #design review", got)
	}
	if err := waitFor(ctx, `window.notReloaded === true`); err != nil {
		t.Errorf("the page was reloaded: %v", err)
	}

	// The chip takes the tag away: only the word is left, in the box too.
	if err := chromedp.Do(ctx, chromedp.Click(`#search-panel .chip-remove[aria-label="Remove #design"]`)); err != nil {
		t.Fatalf("click the chip: %v", err)
	}
	if err := waitFor(ctx, `document.querySelectorAll('#search-panel .chip').length === 0`); err != nil {
		t.Fatalf("the chip did not go: %v", err)
	}
	if err := waitFor(ctx, `location.search === '?q=review'`); err != nil {
		t.Errorf("the address after the chip = %q, want ?q=review", locationOf(t, ctx))
	}
	if got := valueOf(t, ctx, "#search-q"); got != "review" {
		t.Errorf("box after the chip = %q, want review", got)
	}
	if n := countOf(t, ctx, "#search-panel .search-result"); n < 1 {
		t.Errorf("results after the chip = %d, want at least 1", n)
	}
	if err := waitFor(ctx, `window.notReloaded === true`); err != nil {
		t.Errorf("the page was reloaded: %v", err)
	}

	// The result links to the post.
	if err := chromedp.Do(ctx, chromedp.Click(`#search-panel .search-open`), chromedp.WaitVisible("article.post")); err != nil {
		t.Fatalf("open the result: %v", err)
	}
	if got := locationOf(t, ctx); len(got) < len("/posts/1") || got[:7] != "/posts/" {
		t.Errorf("location after the result = %q, want a post page", got)
	}

	// Carol is not in "secret": a word that only it has finds nothing, and
	// not even the room shows.
	carol := secondBrowser(t, srv, "carol")
	if err := chromedp.Do(carol, chromedp.Navigate(srv.URL+"/search?q=surprise"), chromedp.WaitVisible("#search-panel .empty")); err != nil {
		t.Fatalf("carol searches: %v", err)
	}
	if got := textOf(t, carol, "#search-panel .empty"); got != "Nothing found." {
		t.Errorf("carol sees %q, want Nothing found.", got)
	}
	if n := countOf(t, carol, "#search-panel .search-result"); n != 0 {
		t.Errorf("carol sees %d results", n)
	}
	// Alice, a member, finds it.
	if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/search?q=surprise"), chromedp.WaitVisible("#search-panel .search-result")); err != nil {
		t.Fatalf("alice searches for the surprise: %v", err)
	}
	if got := textOf(t, ctx, "#search-panel .search-room"); got != "secret" {
		t.Errorf("room of the result = %q, want secret", got)
	}
}
