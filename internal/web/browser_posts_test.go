//go:build clowder_browser

package web_test

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/browsertest"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/markdown"
	"github.com/rm4n0s/clowder/internal/store"
)

// runJS runs the JavaScript expression in the page.
func runJS(t *testing.T, ctx context.Context, js string) {
	t.Helper()
	if _, err := chromedp.Run(ctx, chromedp.Evaluate[bool](js)); err != nil {
		t.Fatalf("run %q: %v", js, err)
	}
}

// openGeneral clicks the room "general" in the sidebar and waits for its page.
func openGeneral(t *testing.T, ctx context.Context) {
	t.Helper()
	err := chromedp.Do(ctx, chromedp.Click(`aside a[data-room="general"]`), chromedp.WaitVisible("#post-editor-new"))
	if err != nil {
		t.Fatalf("open general: %v", err)
	}
}

// TestAliceWritesAndEditsAPost writes a markdown post with two tags in
// "general" through the editor (a mistake first, the preview, the suggestions),
// sees it at the top of the feed rendered, with its tags, edits it in place
// and sees "edited", then deletes it.
func TestAliceWritesAndEditsAPost(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()

	if err := browsertest.LogIn(ctx, srv, "alice"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}
	openGeneral(t, ctx)
	// The demo's posts are there, newest first, and nothing in the editor.
	if err := waitFor(ctx, `document.querySelectorAll('#feed-posts article.post').length === 5`); err != nil {
		t.Fatalf("the demo posts are not in the feed: %v", err)
	}

	// An empty body shows the problem next to the field and keeps the tags.
	submit := `#post-editor-new button[type="submit"]`
	err := chromedp.Do(ctx, browsertest.Fill("#post-tags-new", "keepme"), chromedp.Click(submit))
	if err != nil {
		t.Fatalf("submit the empty editor: %v", err)
	}
	if err := waitFor(ctx, `document.getElementById('post-body-new-error') !== null`); err != nil {
		t.Fatalf("the field error did not appear: %v", err)
	}
	if got := textOf(t, ctx, "#post-body-new-error"); got != "Write something." {
		t.Errorf("body error = %q", got)
	}
	if got := valueOf(t, ctx, "#post-tags-new"); got != "keepme" {
		t.Errorf("tags after the failure = %q, want keepme", got)
	}

	// The preview renders the markdown on the server and can be closed.
	err = chromedp.Do(ctx,
		browsertest.Fill("#post-body-new", "Hello **browser** world"),
		chromedp.Click(`#post-editor-new .post-editor-actions button[type="button"]`),
	)
	if err != nil {
		t.Fatalf("ask for the preview: %v", err)
	}
	if err := waitFor(ctx, `document.querySelector('#post-preview-new strong') !== null && !document.getElementById('post-preview-new').hidden`); err != nil {
		t.Fatalf("the preview did not appear: %v", err)
	}
	if got := textOf(t, ctx, "#post-preview-new strong"); got != "browser" {
		t.Errorf("preview bold text = %q", got)
	}
	if err := chromedp.Do(ctx, chromedp.Click(`#post-preview-new .post-preview-bar button`)); err != nil {
		t.Fatalf("close the preview: %v", err)
	}
	if err := waitFor(ctx, `document.getElementById('post-preview-new').hidden === true`); err != nil {
		t.Fatalf("the preview did not close: %v", err)
	}

	// Typing the start of a tag suggests the demo's "welcome"; clicking it
	// completes the tag.
	err = chromedp.Do(ctx, browsertest.Fill("#post-tags-new", "wel"))
	if err != nil {
		t.Fatalf("type the tag: %v", err)
	}
	if err := waitFor(ctx, `document.querySelector('#post-tag-suggest-new button[data-tag="welcome"]') !== null`); err != nil {
		t.Fatalf("the suggestion did not appear: %v", err)
	}
	if err := chromedp.Do(ctx, chromedp.Click(`#post-tag-suggest-new button[data-tag="welcome"]`)); err != nil {
		t.Fatalf("click the suggestion: %v", err)
	}
	if got := valueOf(t, ctx, "#post-tags-new"); got != "welcome " {
		t.Errorf("tags after the suggestion = %q, want %q", got, "welcome ")
	}
	if err := waitFor(ctx, `document.querySelectorAll('#post-tag-suggest-new li').length === 0`); err != nil {
		t.Errorf("the suggestions stayed after the click: %v", err)
	}
	if err := chromedp.Do(ctx, chromedp.SendKeys("#post-tags-new", "browser"), chromedp.Click(submit)); err != nil {
		t.Fatalf("submit the post: %v", err)
	}

	// The post is at the top of the feed, rendered, with its two tags.
	first := `#feed-posts article.post:first-child`
	if err := waitFor(ctx, `document.querySelector('`+first+` strong') !== null && document.querySelector('`+first+` strong').textContent === 'browser'`); err != nil {
		t.Fatalf("the new post is not at the top of the feed: %v", err)
	}
	if err := waitFor(ctx, `document.querySelectorAll('#feed-posts article.post').length === 6`); err != nil {
		t.Errorf("the feed does not hold six posts: %v", err)
	}
	tags, err := chromedp.Run(ctx, chromedp.Evaluate[string](
		`[...document.querySelectorAll('`+first+` a.tag')].map(a => a.textContent + '=' + a.getAttribute('href')).join(' ')`))
	if err != nil {
		t.Fatalf("read the tags: %v", err)
	}
	if want := "#welcome=/search?q=%23welcome #browser=/search?q=%23browser"; tags != want {
		t.Errorf("tags = %q, want %q", tags, want)
	}
	if got := textOf(t, ctx, first+" .post-author"); got != "Alice" {
		t.Errorf("author = %q", got)
	}
	// The editor is fresh again.
	if err := waitFor(ctx, `document.getElementById('post-body-new').value === '' && document.getElementById('post-tags-new').value === ''`); err != nil {
		t.Errorf("the editor kept the text of the post: %v", err)
	}
	if err := waitFor(ctx, `document.querySelector('`+first+` .post-edited') === null`); err != nil {
		t.Errorf("a new post says edited: %v", err)
	}

	// Edit in place: the editor comes filled; Cancel restores the post.
	err = chromedp.Do(ctx, chromedp.Click(first+" .post-edit"), chromedp.WaitVisible(first+" textarea"))
	if err != nil {
		t.Fatalf("open the editor of the post: %v", err)
	}
	if got := valueOf(t, ctx, first+" textarea"); got != "Hello **browser** world" {
		t.Errorf("body in the editor = %q", got)
	}
	if got := valueOf(t, ctx, first+" input[name=tags]"); got != "welcome browser" {
		t.Errorf("tags in the editor = %q", got)
	}
	if err := chromedp.Do(ctx, chromedp.Click(first+" .post-editor-actions a")); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := waitFor(ctx, `document.querySelector('`+first+` textarea') === null && document.querySelector('`+first+` strong') !== null`); err != nil {
		t.Fatalf("Cancel did not restore the post: %v", err)
	}

	// Edit and save: the post comes back changed and says "edited".
	err = chromedp.Do(ctx,
		chromedp.Click(first+" .post-edit"), chromedp.WaitVisible(first+" textarea"),
		browsertest.Fill(first+" textarea", "Hello **edited** world"),
		chromedp.Click(first+` button[type="submit"]`),
	)
	if err != nil {
		t.Fatalf("edit the post: %v", err)
	}
	if err := waitFor(ctx, `document.querySelector('`+first+` .post-edited') !== null`); err != nil {
		t.Fatalf("the post does not say edited: %v", err)
	}
	if got := textOf(t, ctx, first+" strong"); got != "edited" {
		t.Errorf("bold text after the edit = %q", got)
	}
	if got := textOf(t, ctx, first+" .post-edited"); got != "edited" {
		t.Errorf("edited mark = %q", got)
	}

	// Delete (the confirm dialog is answered yes): the post is gone.
	runJS(t, ctx, `window.confirm = () => true; true`)
	if err := chromedp.Do(ctx, chromedp.Click(first+` .post-delete button`)); err != nil {
		t.Fatalf("delete the post: %v", err)
	}
	if err := waitFor(ctx, `document.querySelectorAll('#feed-posts article.post').length === 5 && ![...document.querySelectorAll('#feed-posts strong')].some(s => s.textContent === 'edited')`); err != nil {
		t.Fatalf("the post did not disappear: %v", err)
	}
}

// TestFeedLoadsOlderPostsOnScroll puts 35 more posts in "general" (40 with the
// demo's), opens it, sees the first 30, scrolls to the end and sees the other
// ten appear, the sentinel gone.
func TestFeedLoadsOlderPostsOnScroll(t *testing.T) {
	srv := browsertest.Start(t)
	const extra = 35
	seedPosts(t, srv, "alice", "general", extra)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()

	if err := browsertest.LogIn(ctx, srv, "alice"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}
	openGeneral(t, ctx)
	if err := waitFor(ctx, `document.querySelectorAll('#feed-posts article.post').length === 30`); err != nil {
		t.Fatalf("the first page does not hold 30 posts: %v", err)
	}
	if err := waitFor(ctx, `document.querySelector('#feed-more[data-on-intersect__once]') !== null`); err != nil {
		t.Fatalf("the feed has no sentinel: %v", err)
	}
	// The newest is the last seeded one; the demo's posts are further down.
	if got := textOf(t, ctx, `#feed-posts article.post:first-child .post-body`); got != "seeded post "+strconv.Itoa(extra) {
		t.Errorf("newest post = %q", got)
	}
	// Scrolling to the end brings the rest in.
	runJS(t, ctx, `window.scrollTo(0, document.body.scrollHeight); true`)
	if err := waitFor(ctx, `document.querySelectorAll('#feed-posts article.post').length === 40`); err != nil {
		n, _ := chromedp.Run(ctx, chromedp.Evaluate[int](`document.querySelectorAll('#feed-posts article.post').length`))
		t.Fatalf("scrolling did not load the older posts (%d shown): %v", n, err)
	}
	if err := waitFor(ctx, `document.getElementById('feed-more').children.length === 0 && !document.getElementById('feed-more').hasAttribute('data-on-intersect__once')`); err != nil {
		t.Errorf("the sentinel is still there after the last page: %v", err)
	}
	// The oldest post of the demo ends the feed.
	if got := textOf(t, ctx, `#feed-posts article.post:last-child .post-body h3`); got != "Welcome to Clowder" {
		t.Errorf("oldest post heading = %q", got)
	}
}

// seedPosts writes n text posts of the user in the room straight into the
// database of the running server, through the posts service. The bodies are
// "seeded post 1" to "seeded post <n>", oldest first.
func seedPosts(t *testing.T, srv *browsertest.Server, username, room string, n int) {
	t.Helper()
	ctx := t.Context()
	st, err := store.Open(ctx, filepath.Join(srv.DataDir, "clowder.db"), clock.Real(), nil)
	if err != nil {
		t.Fatalf("open the database of the server: %+v", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Errorf("close the database: %v", err)
		}
	}()
	user, err := st.UserByUsername(ctx, username)
	if err != nil {
		t.Fatalf("UserByUsername: %+v", err)
	}
	rooms, err := st.ListRooms(ctx)
	if err != nil {
		t.Fatalf("ListRooms: %+v", err)
	}
	var roomID uint64
	for _, r := range rooms {
		if r.Name == room {
			roomID = r.ID
		}
	}
	if roomID == 0 {
		t.Fatalf("no room %q", room)
	}
	posts := app.NewPosts(st, clock.Real(), markdown.New())
	actor := app.Actor{UserID: user.ID, Username: user.Username, Role: user.Role}
	for i := 1; i <= n; i++ {
		_, err := posts.Create(ctx, actor, roomID, app.PostInput{Format: app.FormatText, Body: "seeded post " + strconv.Itoa(i)})
		if err != nil {
			t.Fatalf("seed post %d: %+v", i, err)
		}
	}
}
