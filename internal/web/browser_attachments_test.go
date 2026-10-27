//go:build clowder_browser

package web_test

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/browsertest"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/clowder/internal/markdown"
	"github.com/rm4n0s/clowder/internal/store"
)

// writeTestFiles writes a PNG of 40 by 30 pixels and a text file to a
// directory of the test and returns their paths.
func writeTestFiles(t *testing.T) (pngPath, textPath string) {
	t.Helper()
	dir := t.TempDir()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 40, 30))); err != nil {
		t.Fatalf("encode the PNG: %v", err)
	}
	pngPath = filepath.Join(dir, "picture.png")
	if err := os.WriteFile(pngPath, b.Bytes(), 0o600); err != nil {
		t.Fatalf("write the PNG: %v", err)
	}
	textPath = filepath.Join(dir, "readme.txt")
	if err := os.WriteFile(textPath, []byte("read me, please"), 0o600); err != nil {
		t.Fatalf("write the text file: %v", err)
	}
	return pngPath, textPath
}

// seedAttachment writes a post of the user in the room with one text file,
// straight into the database of the running server, and returns the ID of the
// attachment.
func seedAttachment(t *testing.T, srv *browsertest.Server, username, room, name string) uint64 {
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
	fileStore, err := files.New(filepath.Join(srv.DataDir, "files"))
	if err != nil {
		t.Fatalf("open the files of the server: %+v", err)
	}
	posts := app.NewPosts(st, clock.Real(), markdown.New(), fileStore, app.PostsConfig{})
	actor := app.Actor{UserID: user.ID, Username: user.Username, Role: user.Role}
	up, err := posts.Upload(ctx, actor, name, strings.NewReader("only for the room"))
	if err != nil {
		t.Fatalf("Upload: %+v", err)
	}
	post, err := posts.Create(ctx, actor, roomID, app.PostInput{Format: app.FormatText, Body: "with a file", Uploads: []app.Upload{up}})
	if err != nil {
		t.Fatalf("Create: %+v", err)
	}
	return post.Attachments[0].ID
}

// statusOfFetch fetches the path in the page, with the cookies of the session,
// and returns the status of the answer.
func statusOfFetch(t *testing.T, ctx context.Context, path string) int {
	t.Helper()
	runJS(t, ctx, `window.__status = 0; fetch(`+strconv.Quote(path)+`).then(r => { window.__status = r.status; }); true`)
	if err := waitFor(ctx, `window.__status !== 0`); err != nil {
		t.Fatalf("fetch %s: %v", path, err)
	}
	status, err := chromedp.Run(ctx, chromedp.Evaluate[int](`window.__status`))
	if err != nil {
		t.Fatalf("read the status of %s: %v", path, err)
	}
	return status
}

// TestAliceAttachesFiles has alice attach a PNG and a text file to a new post
// in "general": the preview does not send the files, the image shows inline at
// its size and the text file is a link; she then removes the text file in the
// edit. Carol, who is not in "secret", gets 404 for a file of that room, and
// alice gets it.
func TestAliceAttachesFiles(t *testing.T) {
	srv := browsertest.Start(t)
	secretID := seedAttachment(t, srv, "alice", "secret", "plans.txt")
	pngPath, textPath := writeTestFiles(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()

	if err := browsertest.LogIn(ctx, srv, "alice"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}
	openGeneral(t, ctx)
	err := chromedp.Do(ctx,
		browsertest.Fill("#post-body-new", "Files for **you**"),
		chromedp.SetUploadFiles("#post-files-new", []string{pngPath, textPath}),
	)
	if err != nil {
		t.Fatalf("fill the editor: %v", err)
	}

	// The preview sends the text of the form and not the files.
	runJS(t, ctx, `window.__previews = []; const of = window.fetch;
		window.fetch = (url, opts) => {
			if (String(url).includes('/preview') && opts && opts.body instanceof FormData) {
				window.__previews.push([...opts.body.keys()].join(','));
			}
			return of(url, opts);
		}; true`)
	if err := chromedp.Do(ctx, chromedp.Click(`#post-editor-new .post-editor-actions button[type="button"]`)); err != nil {
		t.Fatalf("ask for the preview: %v", err)
	}
	if err := waitFor(ctx, `document.querySelector('#post-preview-new strong') !== null`); err != nil {
		t.Fatalf("the preview did not appear: %v", err)
	}
	sent, err := chromedp.Run(ctx, chromedp.Evaluate[string](`window.__previews.join(';')`))
	if err != nil {
		t.Fatalf("read the preview request: %v", err)
	}
	if sent == "" || strings.Contains(sent, "files") {
		t.Errorf("the preview sent the form fields %q, want the text fields and no files", sent)
	}
	// The input is back on, with its files, for the post itself.
	if err := waitFor(ctx, `document.getElementById('post-files-new').disabled === false && document.getElementById('post-files-new').files.length === 2`); err != nil {
		t.Fatalf("the file input lost its files after the preview: %v", err)
	}

	// Posting sends the files with the form.
	if err := chromedp.Do(ctx, chromedp.Click(`#post-editor-new button[type="submit"]`)); err != nil {
		t.Fatalf("submit the post: %v", err)
	}
	first := `#feed-posts article.post:first-child`
	if err := waitFor(ctx, `document.querySelector('`+first+` img') !== null`); err != nil {
		t.Fatalf("the image is not in the new post: %v", err)
	}
	// The image has loaded (its natural width is the width of the file) and the
	// page has its size: width and height attributes.
	if err := waitFor(ctx, `(() => { const i = document.querySelector('`+first+` img'); return i.complete && i.naturalWidth === 40 && i.naturalHeight === 30; })()`); err != nil {
		t.Fatalf("the image did not load: %v", err)
	}
	size, err := chromedp.Run(ctx, chromedp.Evaluate[string](
		`(() => { const i = document.querySelector('`+first+` img'); return i.getAttribute('width') + 'x' + i.getAttribute('height') + ' ' + i.getAttribute('loading') + ' ' + i.closest('a').getAttribute('href'); })()`))
	if err != nil {
		t.Fatalf("read the image: %v", err)
	}
	if !strings.HasPrefix(size, "40x30 lazy /files/") || !strings.HasSuffix(size, "/picture.png") {
		t.Errorf("image width, height, loading and link = %q", size)
	}
	// The text file is a link with its size, and the preview is gone with the
	// fresh editor.
	link, err := chromedp.Run(ctx, chromedp.Evaluate[string](
		`(() => { const a = document.querySelector('`+first+` .attachment-files a'); return a.textContent + ' ' + a.getAttribute('href') + ' ' + a.parentElement.textContent; })()`))
	if err != nil {
		t.Fatalf("read the file link: %v", err)
	}
	if !strings.HasPrefix(link, "readme.txt /files/") || !strings.HasSuffix(link, "/readme.txt readme.txt 15 B") {
		t.Errorf("file link = %q", link)
	}
	if got := statusOfFetch(t, ctx, strings.Fields(link)[1]); got != 200 {
		t.Errorf("status of the text file = %d, want 200", got)
	}
	if err := waitFor(ctx, `document.getElementById('post-files-new').files.length === 0`); err != nil {
		t.Errorf("the editor kept the files: %v", err)
	}

	// Edit: the files are listed with Remove boxes; removing the text file
	// leaves the image.
	err = chromedp.Do(ctx, chromedp.Click(first+" .post-edit"), chromedp.WaitVisible(first+" .attachment-edit"))
	if err != nil {
		t.Fatalf("open the editor of the post: %v", err)
	}
	if got := textOf(t, ctx, first+" .attachment-edit"); !strings.Contains(got, "picture.png") || !strings.Contains(got, "readme.txt") {
		t.Errorf("files in the editor = %q", got)
	}
	err = chromedp.Do(ctx,
		chromedp.Click(first+` .attachment-edit li:nth-child(2) input[type="checkbox"]`),
		chromedp.Click(first+` button[type="submit"]`),
	)
	if err != nil {
		t.Fatalf("remove the text file: %v", err)
	}
	if err := waitFor(ctx, `document.querySelector('`+first+` textarea') === null && document.querySelector('`+first+` .attachment-files') === null && document.querySelector('`+first+` img') !== null`); err != nil {
		t.Fatalf("the text file was not removed: %v", err)
	}

	// Carol, who is not in "secret", gets a 404 for its file; alice gets it.
	secretPath := "/files/" + strconv.FormatUint(secretID, 10) + "/plans.txt"
	if got := statusOfFetch(t, ctx, secretPath); got != 200 {
		t.Errorf("alice: status of the file of secret = %d, want 200", got)
	}
	carol, cancelCarolBrowser, err := browsertest.NewBrowser(context.Background())
	if err != nil {
		t.Fatalf("browser of carol: %v", err)
	}
	defer cancelCarolBrowser()
	carolCtx, cancelCarol := context.WithTimeout(carol, pageTimeout)
	defer cancelCarol()
	if err := browsertest.LogIn(carolCtx, srv, "carol"); err != nil {
		t.Fatalf("LogIn carol: %+v", err)
	}
	if got := statusOfFetch(t, carolCtx, secretPath); got != 404 {
		t.Errorf("carol: status of the file of secret = %d, want 404", got)
	}
	if err := chromedp.Do(carolCtx, chromedp.Navigate(srv.URL+secretPath), chromedp.WaitVisible("body")); err != nil {
		t.Fatalf("carol opens the file: %v", err)
	}
	if got := textOf(t, carolCtx, "body"); !strings.Contains(got, "Page not found") || strings.Contains(got, "only for the room") {
		t.Errorf("carol sees %q", got)
	}
}
