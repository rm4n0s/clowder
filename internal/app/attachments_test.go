package app_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/clowder/internal/markdown"
	"github.com/rm4n0s/errors"
)

// pngData is a PNG of w by h pixels, small enough for the test limit.
func pngData(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	if b.Len() > testMaxUpload {
		t.Fatalf("test image is %d bytes, over the limit %d", b.Len(), testMaxUpload)
	}
	return b.Bytes()
}

// upload makes the user upload a file and fails the test on an error.
func (e *postsEnv) upload(user, name string, data []byte) app.Upload {
	e.t.Helper()
	u, err := e.posts.Upload(e.t.Context(), e.as(user), name, bytes.NewReader(data))
	if err != nil {
		e.t.Fatalf("Upload %q: %+v", name, err)
	}
	return u
}

// uploadText uploads a text file with the name as its content.
func (e *postsEnv) uploadText(user, name string) app.Upload {
	e.t.Helper()
	return e.upload(user, name, []byte("content of "+name))
}

// onDisk returns the keys of the files in the files directory, sorted.
func (e *postsEnv) onDisk() []string {
	e.t.Helper()
	entries, err := os.ReadDir(e.filesDir)
	if err != nil {
		e.t.Fatalf("ReadDir: %v", err)
	}
	keys := []string{}
	for _, en := range entries {
		if en.Name() != "tmp" && en.Name() != "notes" {
			keys = append(keys, en.Name())
		}
	}
	slices.Sort(keys)
	return keys
}

// requireOnDisk fails the test unless exactly the files of the uploads are on
// disk.
func (e *postsEnv) requireOnDisk(uploads ...app.Upload) {
	e.t.Helper()
	want := []string{}
	for _, u := range uploads {
		want = append(want, u.Key)
	}
	slices.Sort(want)
	if got := e.onDisk(); !slices.Equal(got, want) {
		e.t.Errorf("files on disk = %v, want %v", got, want)
	}
}

// writeWith makes the user write a text post with the uploads.
func (e *postsEnv) writeWith(user, room, body string, uploads ...app.Upload) app.Post {
	e.t.Helper()
	p, err := e.posts.Create(e.t.Context(), e.as(user), e.room(room).ID,
		app.PostInput{Format: app.FormatText, Body: body, Uploads: uploads})
	if err != nil {
		e.t.Fatalf("Create %q: %+v", body, err)
	}
	return p
}

// attachmentIDs returns the IDs of the attachments of the view.
func attachmentIDs(p app.Post) []uint64 {
	ids := []uint64{}
	for _, a := range p.Attachments {
		ids = append(ids, a.ID)
	}
	return ids
}

// attachmentNames returns the file names of the attachments of the view.
func attachmentNames(p app.Post) []string {
	names := []string{}
	for _, a := range p.Attachments {
		names = append(names, a.FileName)
	}
	return names
}

func TestPostsTagTooLargeIsTheFilesTag(t *testing.T) {
	t.Parallel()
	if app.TagTooLarge != files.TagTooLarge {
		t.Errorf("TagTooLarge = %q, want %q", app.TagTooLarge, files.TagTooLarge)
	}
}

func TestPostsUploadCleansTheName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "photo.png", "photo.png"},
		{"spaces inside are kept", "my photo 1.png", "my photo 1.png"},
		{"unix path", "/etc/passwd", "passwd"},
		{"relative path", "../../secret/key.txt", "key.txt"},
		{"windows path", `C:\Users\me\report.pdf`, "report.pdf"},
		{"mixed separators", `a/b\c/d.txt`, "d.txt"},
		{"control characters are removed", "a\x00b\n\t.txt", "ab.txt"},
		{"a separator hidden by a control character", "dir/\x01../x.txt", "x.txt"},
		{"surrounding space", "  notes.txt \n", "notes.txt"},
		{"invalid utf-8", "a\xffb.txt", "ab.txt"},
		{"unicode", "φωτογραφία 日本.png", "φωτογραφία 日本.png"},
		{"empty", "", "file"},
		{"blank", "  \t ", "file"},
		{"only control characters", "\x00\x01", "file"},
		{"a directory", "some/dir/", "file"},
		{"dot", ".", "file"},
		{"dot dot", "..", "file"},
		{"dot dot after a path", "a/..", "file"},
		{"hidden file", ".bashrc", ".bashrc"},
		{"255 bytes", strings.Repeat("a", 255), strings.Repeat("a", 255)},
		{"256 bytes", strings.Repeat("a", 256), strings.Repeat("a", 255)},
		{"long", strings.Repeat("b", 1000), strings.Repeat("b", 255)},
		{"multibyte cut at a character", strings.Repeat("é", 200), strings.Repeat("é", 127)},
		{"multibyte just fits", strings.Repeat("é", 127) + "a", strings.Repeat("é", 127) + "a"},
	}
	e := newPostsEnv(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := e.posts.Upload(t.Context(), e.as("bob"), tt.in, strings.NewReader("data"))
			if err != nil {
				t.Fatalf("Upload: %+v", err)
			}
			if u.FileName != tt.want {
				t.Errorf("FileName = %q, want %q", u.FileName, tt.want)
			}
			if len(u.FileName) > 255 || !utf8.ValidString(u.FileName) {
				t.Errorf("FileName %q is too long or not valid", u.FileName)
			}
		})
	}
}

func TestPostsUploadResult(t *testing.T) {
	t.Parallel()
	e := newPostsEnv(t)
	data := pngData(t, 30, 20)
	u := e.upload("bob", "dir/pic.png", data)
	if len(u.Key) != 64 || u.FileName != "pic.png" || u.ContentType != "image/png" || u.Size != int64(len(data)) || u.Width != 30 || u.Height != 20 {
		t.Errorf("Upload = %+v", u)
	}
	txt := e.upload("bob", "a.txt", []byte("hello"))
	if txt.ContentType != "text/plain; charset=utf-8" || txt.Size != 5 || txt.Width != 0 || txt.Height != 0 {
		t.Errorf("Upload = %+v", txt)
	}
	e.requireOnDisk(u, txt)
}

func TestPostsUploadPermissions(t *testing.T) {
	t.Parallel()
	e := newPostsEnv(t)
	for _, user := range []string{"admin", "root", "alice", "bob", "carol", "dave"} {
		t.Run(user, func(t *testing.T) {
			if _, err := e.posts.Upload(t.Context(), e.as(user), "a.txt", strings.NewReader("x")); err != nil {
				t.Errorf("got %+v, want success", err)
			}
		})
	}
	t.Run("nobody", func(t *testing.T) {
		before := len(e.onDisk())
		_, err := e.posts.Upload(t.Context(), app.Actor{}, "a.txt", strings.NewReader("x"))
		_ = requireTag(t, err, app.TagUnauthenticated, "Posts.Upload.Unauthenticated")
		if got := len(e.onDisk()); got != before {
			t.Errorf("a refused upload left %d files", got-before)
		}
	})
}

func TestPostsUploadLimit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		size int
		ok   bool
	}{
		{"empty", 0, true},
		{"at the limit", testMaxUpload, true},
		{"one over", testMaxUpload + 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newPostsEnv(t)
			u, err := e.posts.Upload(t.Context(), e.as("bob"), "big.bin", bytes.NewReader(bytes.Repeat([]byte{'a'}, tt.size)))
			if tt.ok {
				if err != nil || u.Size != int64(tt.size) {
					t.Fatalf("Upload = %+v, %v", u, err)
				}
				e.requireOnDisk(u)
				return
			}
			appErr := requireTag(t, err, app.TagTooLarge, "Posts.Upload->Store.Save.FileTooLarge")
			if appErr.Metadata["limit"] != int64(testMaxUpload) {
				t.Errorf("limit = %v", appErr.Metadata["limit"])
			}
			e.requireOnDisk()
		})
	}
}

func TestPostsUploadDefaults(t *testing.T) {
	t.Parallel()
	e := newPostsEnv(t)
	posts := app.NewPosts(e.st, e.clk, markdown.New(), e.files, app.PostsConfig{}, nil)
	// The default limit is 25 MB: 2 KB is far under it, and the count default
	// is 10.
	var ups []app.Upload
	for range 10 {
		u, err := posts.Upload(t.Context(), e.as("bob"), "a.txt", bytes.NewReader(bytes.Repeat([]byte{'a'}, 3000)))
		if err != nil {
			t.Fatalf("Upload: %+v", err)
		}
		ups = append(ups, u)
	}
	if _, err := posts.Create(t.Context(), e.as("bob"), e.lobby.ID, app.PostInput{Format: app.FormatText, Body: "x", Uploads: ups}); err != nil {
		t.Fatalf("Create with 10 files: %+v", err)
	}
	extra, err := posts.Upload(t.Context(), e.as("bob"), "a.txt", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = posts.Create(t.Context(), e.as("bob"), e.lobby.ID, app.PostInput{Format: app.FormatText, Body: "x", Uploads: append(ups, extra)})
	appErr := requireTag(t, err, app.TagInvalidInput, "Posts.Create.func1.InvalidInput")
	if got := fieldsOf(t, appErr)[app.FieldAttachments]; got != "Attach at most 10 files." {
		t.Errorf("attachments field = %q", got)
	}
}

func TestPostsCreateWithAttachments(t *testing.T) {
	t.Parallel()
	e := newPostsEnv(t)
	img := e.upload("bob", "pic.png", pngData(t, 30, 20))
	txt := e.uploadText("bob", "notes.txt")
	before := len(e.events())
	p, err := e.posts.Create(t.Context(), e.as("bob"), e.lobby.ID,
		app.PostInput{Format: app.FormatText, Body: "look", Uploads: []app.Upload{img, txt}})
	if err != nil {
		t.Fatalf("Create: %+v", err)
	}
	if len(p.Attachments) != 2 {
		t.Fatalf("Attachments = %+v", p.Attachments)
	}
	a, b := p.Attachments[0], p.Attachments[1]
	if a.ID == 0 || a.FileName != "pic.png" || a.ContentType != "image/png" || a.Size != img.Size || !a.IsImage || a.Width != 30 || a.Height != 20 {
		t.Errorf("first attachment = %+v", a)
	}
	if b.ID == 0 || b.ID == a.ID || b.FileName != "notes.txt" || b.ContentType != "text/plain; charset=utf-8" || b.Size != txt.Size || b.IsImage || b.Width != 0 || b.Height != 0 {
		t.Errorf("second attachment = %+v", b)
	}
	e.requireOnDisk(img, txt)

	rows, err := e.st.AttachmentsOfPosts(t.Context(), []uint64{p.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows[p.ID] {
		if r.UploaderID != e.users["bob"].ID || r.PostID != p.ID {
			t.Errorf("row = %+v", r)
		}
	}
	e.requireNewEvents(before, wantEvent{
		actor: "bob", action: app.ActionPostCreated, room: e.lobby.ID, entityType: app.EntityPost, entityID: p.ID,
		summary: "Bob posted in #lobby",
		details: map[string]any{"format": "text", "length": float64(4), "tags": []any{}, "attachments": float64(2)},
	})
}

func TestPostsImageTypes(t *testing.T) {
	t.Parallel()
	e := newPostsEnv(t)
	webp := []byte("RIFF\x1a\x00\x00\x00WEBPVP8L\x0d\x00\x00\x00\x2f\x09\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00")
	tests := []struct {
		name     string
		data     []byte
		isImage  bool
		w, h     int
		wantType string
	}{
		{"png", pngData(t, 5, 7), true, 5, 7, "image/png"},
		{"webp", webp, true, 10, 1, "image/webp"},
		{"text", []byte("plain"), false, 0, 0, "text/plain; charset=utf-8"},
		{"html is not an image", []byte("<html><body>x</body></html>"), false, 0, 0, "text/html; charset=utf-8"},
		{"svg is not an image", []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`), false, 0, 0, "text/xml; charset=utf-8"},
		{"binary", []byte{0, 1, 2, 3, 0xff}, false, 0, 0, "application/octet-stream"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := e.upload("bob", "f", tt.data)
			p := e.writeWith("bob", "lobby", "x", u)
			a := p.Attachments[0]
			if a.ContentType != tt.wantType || a.IsImage != tt.isImage || a.Width != tt.w || a.Height != tt.h {
				t.Errorf("attachment = %+v", a)
			}
		})
	}
}

func TestPostsAttachmentsAllowAnEmptyBody(t *testing.T) {
	t.Parallel()
	e := newPostsEnv(t)
	u := e.uploadText("bob", "a.txt")
	p, err := e.posts.Create(t.Context(), e.as("bob"), e.lobby.ID, app.PostInput{Format: app.FormatText, Body: " \n", Uploads: []app.Upload{u}})
	if err != nil {
		t.Fatalf("Create: %+v", err)
	}
	if p.Body != "" || len(p.Attachments) != 1 {
		t.Errorf("post = %+v", p)
	}
	c, err := e.posts.Reply(t.Context(), e.as("bob"), p.ID, app.PostInput{Format: app.FormatText, Uploads: []app.Upload{e.uploadText("bob", "b.txt")}})
	if err != nil {
		t.Fatalf("Reply: %+v", err)
	}
	if c.Body != "" || len(c.Attachments) != 1 || !c.IsComment {
		t.Errorf("comment = %+v", c)
	}

	// Without a file the body is still needed.
	_, err = e.posts.Create(t.Context(), e.as("bob"), e.lobby.ID, app.PostInput{Format: app.FormatText, Body: " "})
	appErr := requireTag(t, err, app.TagInvalidInput, "Posts.Create.func1.InvalidInput")
	if got := fieldsOf(t, appErr); !reflect.DeepEqual(got, map[string]string{app.FieldBody: "Write something."}) {
		t.Errorf("fields = %v", got)
	}
}

func TestPostsReplyWithAttachments(t *testing.T) {
	t.Parallel()
	e := newPostsEnv(t)
	post := e.write("alice", "den", "the post")
	u := e.upload("bob", "pic.png", pngData(t, 4, 4))
	before := len(e.events())
	c, err := e.posts.Reply(t.Context(), e.as("bob"), post.ID, app.PostInput{Format: app.FormatText, Body: "see", Uploads: []app.Upload{u}})
	if err != nil {
		t.Fatalf("Reply: %+v", err)
	}
	if len(c.Attachments) != 1 || c.Attachments[0].FileName != "pic.png" || !c.Attachments[0].IsImage {
		t.Errorf("Attachments = %+v", c.Attachments)
	}
	e.requireNewEvents(before, wantEvent{
		actor: "bob", action: app.ActionPostCreated, room: e.den.ID, entityType: app.EntityComment, entityID: c.ID,
		summary: "Bob commented in #den",
		details: map[string]any{
			"format": "text", "length": float64(3), "tags": []any{},
			"rootID": float64(post.ID), "parentID": float64(post.ID), "attachments": float64(1),
		},
	})
}

func TestPostsAttachmentCountLimit(t *testing.T) {
	t.Parallel()
	e := newPostsEnv(t)
	var ups []app.Upload
	for _, n := range []string{"a", "b", "c", "d"} {
		ups = append(ups, e.uploadText("bob", n))
	}
	// At the limit works.
	p, err := e.posts.Create(t.Context(), e.as("bob"), e.lobby.ID, app.PostInput{Format: app.FormatText, Body: "x", Uploads: ups[:3]})
	if err != nil {
		t.Fatalf("Create with %d files: %+v", testMaxAttachments, err)
	}
	// One over fails, in Create and in Reply, and its file goes.
	extra := e.uploadText("bob", "extra")
	before := len(e.events())
	for name, call := range map[string]func() error{
		"Create": func() error {
			_, err := e.posts.Create(t.Context(), e.as("bob"), e.lobby.ID, app.PostInput{Format: app.FormatText, Body: "x", Uploads: []app.Upload{ups[3], extra, e.uploadText("bob", "e"), e.uploadText("bob", "f")}})
			return err
		},
		"Reply": func() error {
			_, err := e.posts.Reply(t.Context(), e.as("bob"), p.ID, app.PostInput{Format: app.FormatText, Body: "x", Uploads: []app.Upload{ups[3], extra, e.uploadText("bob", "e"), e.uploadText("bob", "f")}})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := call()
			appErr := requireTag(t, err, app.TagInvalidInput, "Posts."+name+".func1.InvalidInput")
			if got := fieldsOf(t, appErr)[app.FieldAttachments]; got != "Attach at most 3 files." {
				t.Errorf("attachments field = %q", got)
			}
		})
	}
	if n := len(e.events()); n != before {
		t.Errorf("failed calls wrote %d events", n-before)
	}
	// Only the files of the post that was made are left.
	rows, err := e.st.AttachmentsOfPosts(t.Context(), []uint64{p.ID})
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, r := range rows[p.ID] {
		kept = append(kept, r.StorageKey)
	}
	slices.Sort(kept)
	if got := e.onDisk(); !slices.Equal(got, kept) || len(got) != 3 {
		t.Errorf("files on disk = %v, want the 3 of the post %v", got, kept)
	}
}

func TestPostsFailedCallsRemoveTheirUploads(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		call func(e *postsEnv, ups []app.Upload) error
		tag  string
	}{
		{"create with a bad tag", func(e *postsEnv, ups []app.Upload) error {
			_, err := e.posts.Create(t.Context(), e.as("bob"), e.lobby.ID, app.PostInput{Format: app.FormatText, Body: "x", Tags: []string{"bad tag!"}, Uploads: ups})
			return err
		}, app.TagInvalidInput},
		{"create in a room the actor has not joined", func(e *postsEnv, ups []app.Upload) error {
			_, err := e.posts.Create(t.Context(), e.as("carol"), e.lobby.ID, app.PostInput{Format: app.FormatText, Body: "x", Uploads: ups})
			return err
		}, app.TagForbidden},
		{"create in a private room", func(e *postsEnv, ups []app.Upload) error {
			_, err := e.posts.Create(t.Context(), e.as("carol"), e.den.ID, app.PostInput{Format: app.FormatText, Body: "x", Uploads: ups})
			return err
		}, app.TagNotFound},
		{"create in a missing room", func(e *postsEnv, ups []app.Upload) error {
			_, err := e.posts.Create(t.Context(), e.as("bob"), 9999, app.PostInput{Format: app.FormatText, Body: "x", Uploads: ups})
			return err
		}, app.TagNotFound},
		{"reply to a missing post", func(e *postsEnv, ups []app.Upload) error {
			_, err := e.posts.Reply(t.Context(), e.as("bob"), 9999, app.PostInput{Format: app.FormatText, Body: "x", Uploads: ups})
			return err
		}, app.TagNotFound},
		{"reply with an unknown format", func(e *postsEnv, ups []app.Upload) error {
			p := e.write("bob", "lobby", "post")
			_, err := e.posts.Reply(t.Context(), e.as("bob"), p.ID, app.PostInput{Format: "html", Body: "x", Uploads: ups})
			return err
		}, app.TagInvalidInput},
		{"edit by another user", func(e *postsEnv, ups []app.Upload) error {
			p := e.write("bob", "lobby", "post")
			_, err := e.posts.Edit(t.Context(), e.as("admin"), p.ID, app.PostInput{Format: app.FormatText, Body: "y", Uploads: ups})
			return err
		}, app.TagForbidden},
		{"edit of a missing post", func(e *postsEnv, ups []app.Upload) error {
			_, err := e.posts.Edit(t.Context(), e.as("bob"), 9999, app.PostInput{Format: app.FormatText, Body: "y", Uploads: ups})
			return err
		}, app.TagNotFound},
		{"edit with a bad body", func(e *postsEnv, ups []app.Upload) error {
			p := e.write("bob", "lobby", "post")
			_, err := e.posts.Edit(t.Context(), e.as("bob"), p.ID, app.PostInput{Format: app.FormatText, Body: "a\xff", Uploads: ups})
			return err
		}, app.TagInvalidInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newPostsEnv(t)
			ups := []app.Upload{e.uploadText("bob", "a.txt"), e.upload("bob", "b.png", pngData(t, 3, 3))}
			before := len(e.events())
			err := tt.call(e, ups)
			appErr, ok := errors.FromError(err)
			if !ok || appErr.Tag != tt.tag {
				t.Fatalf("err = %+v, want tag %s", err, tt.tag)
			}
			if got := e.onDisk(); len(got) != 0 {
				t.Errorf("files on disk after a failure = %v", got)
			}
			// Calls that wrote their own post wrote its event; no more.
			if n := len(e.events()) - before; n > 1 {
				t.Errorf("failed call wrote %d events", n)
			}
		})
	}
}

func TestPostsForgedUploadsAreRefused(t *testing.T) {
	t.Parallel()
	missing := strings.Repeat("0", 64)
	tests := []struct {
		name  string
		build func(e *postsEnv, good app.Upload, other app.Post) []app.Upload
	}{
		{"a key with no file", func(e *postsEnv, good app.Upload, _ app.Post) []app.Upload {
			return []app.Upload{{Key: missing, FileName: "x", ContentType: "text/plain", Size: 1}}
		}},
		{"a path as a key", func(e *postsEnv, good app.Upload, _ app.Post) []app.Upload {
			return []app.Upload{{Key: "../x", FileName: "x", ContentType: "text/plain", Size: 1}}
		}},
		{"an empty key", func(e *postsEnv, good app.Upload, _ app.Post) []app.Upload {
			return []app.Upload{{FileName: "x", ContentType: "text/plain", Size: 1}}
		}},
		{"a wrong size", func(e *postsEnv, good app.Upload, _ app.Post) []app.Upload {
			good.Size++
			return []app.Upload{good}
		}},
		{"a negative size", func(e *postsEnv, good app.Upload, _ app.Post) []app.Upload {
			good.Size = -1
			return []app.Upload{good}
		}},
		{"the same upload twice", func(e *postsEnv, good app.Upload, _ app.Post) []app.Upload {
			return []app.Upload{good, good}
		}},
		{"a file that another post uses", func(e *postsEnv, good app.Upload, other app.Post) []app.Upload {
			rows, err := e.st.AttachmentsOfPosts(e.t.Context(), []uint64{other.ID})
			if err != nil {
				e.t.Fatal(err)
			}
			a := rows[other.ID][0]
			return []app.Upload{{Key: a.StorageKey, FileName: a.FileName, ContentType: a.ContentType, Size: a.Size}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newPostsEnv(t)
			keep := e.uploadText("alice", "kept.txt")
			other := e.writeWith("alice", "den", "other post", keep)
			good := e.uploadText("bob", "good.txt")
			ups := tt.build(e, good, other)
			before := len(e.events())

			_, err := e.posts.Create(t.Context(), e.as("bob"), e.lobby.ID, app.PostInput{Format: app.FormatText, Body: "x", Uploads: ups})
			appErr := requireTag(t, err, app.TagInvalidInput, "Posts.Create.func1.InvalidInput")
			if got := fieldsOf(t, appErr)[app.FieldAttachments]; got != "Upload the files again." {
				t.Errorf("attachments field = %q", got)
			}
			if n := len(e.events()); n != before {
				t.Errorf("a refused call wrote %d events", n-before)
			}
			// The file of the other post is never removed because a call
			// named its key; the good upload is removed with the failed call
			// when it was in the list.
			if _, err := e.files.Open(keep.Key); err != nil {
				t.Errorf("the file of the other post is gone: %+v", err)
			}
			rows, err := e.st.AttachmentsOfPosts(t.Context(), []uint64{other.ID})
			if err != nil || len(rows[other.ID]) != 1 {
				t.Errorf("the other post's attachments = %v, %v", rows, err)
			}
		})
	}
}

func TestPostsUploadsMustBeOnDiskWhenTheDiskFails(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not apply")
	}
	e := newPostsEnv(t)
	u := e.uploadText("bob", "a.txt")
	if err := os.Chmod(e.filesDir, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(e.filesDir, 0o750) })
	_, err := e.posts.Create(t.Context(), e.as("bob"), e.lobby.ID, app.PostInput{Format: app.FormatText, Body: "x", Uploads: []app.Upload{u}})
	_ = requireTag(t, err, "FileStatFailed", "Posts.Create.func1->Posts.checkUploads->checkUploadFiles->Store.Size.FileStatFailed")
}

func TestPostsEditAttachments(t *testing.T) {
	t.Parallel()
	e := newPostsEnv(t)
	a, b, c := e.uploadText("bob", "a.txt"), e.uploadText("bob", "b.txt"), e.uploadText("bob", "c.txt")
	p := e.writeWith("bob", "lobby", "post", a, b)
	ids := attachmentIDs(p)
	e.requireOnDisk(a, b, c)

	// Keep a, drop b, add c.
	before := len(e.events())
	// One microsecond is the finest step that PostgreSQL keeps (SQLite keeps
	// nanoseconds), so the edit has a time of its own on both.
	e.clk.Advance(time.Microsecond)
	edited, err := e.posts.Edit(t.Context(), e.as("bob"), p.ID, app.PostInput{
		Format: app.FormatText, Body: "post", KeepAttachments: []uint64{ids[0]}, Uploads: []app.Upload{c},
	})
	if err != nil {
		t.Fatalf("Edit: %+v", err)
	}
	if got := attachmentNames(edited); !slices.Equal(got, []string{"a.txt", "c.txt"}) {
		t.Errorf("attachments = %v", got)
	}
	if edited.Attachments[0].ID != ids[0] {
		t.Errorf("the kept attachment changed its ID")
	}
	if edited.EditedAt == nil {
		t.Error("EditedAt is nil")
	}
	e.requireOnDisk(a, c) // b went after the commit
	e.requireNewEvents(before, wantEvent{
		actor: "bob", action: app.ActionPostEdited, room: e.lobby.ID, entityType: app.EntityPost, entityID: p.ID,
		summary: "Bob edited a post in #lobby",
		details: map[string]any{"attachments": map[string]any{"added": []any{"c.txt"}, "removed": []any{"b.txt"}}},
	})

	// Only removing: keep nothing, the body stays.
	before = len(e.events())
	edited, err = e.posts.Edit(t.Context(), e.as("bob"), p.ID, app.PostInput{Format: app.FormatText, Body: "post"})
	if err != nil {
		t.Fatalf("Edit: %+v", err)
	}
	if len(edited.Attachments) != 0 {
		t.Errorf("attachments = %v", edited.Attachments)
	}
	e.requireOnDisk()
	e.requireNewEvents(before, wantEvent{
		actor: "bob", action: app.ActionPostEdited, room: e.lobby.ID, entityType: app.EntityPost, entityID: p.ID,
		summary: "Bob edited a post in #lobby",
		details: map[string]any{"attachments": map[string]any{"added": []any{}, "removed": []any{"a.txt", "c.txt"}}},
	})
}

func TestPostsEditWithAttachmentsAndOtherChanges(t *testing.T) {
	t.Parallel()
	e := newPostsEnv(t)
	a := e.uploadText("bob", "a.txt")
	p := e.writeWith("bob", "lobby", "post", a)
	before := len(e.events())
	_, err := e.posts.Edit(t.Context(), e.as("bob"), p.ID, app.PostInput{
		Format: app.FormatText, Body: "longer post", KeepAttachments: attachmentIDs(p),
	})
	if err != nil {
		t.Fatalf("Edit: %+v", err)
	}
	evs := e.events()[before:]
	if len(evs) != 1 {
		t.Fatalf("%d events", len(evs))
	}
	var details map[string]any
	if err := json.Unmarshal([]byte(evs[0].Details), &details); err != nil {
		t.Fatal(err)
	}
	if _, ok := details["attachments"]; ok {
		t.Errorf("details mention the attachments although they did not change: %v", details)
	}
	if _, ok := details["body"]; !ok {
		t.Errorf("details lack the body: %v", details)
	}
	e.requireOnDisk(a)
}

func TestPostsEditWithNothingChangedWritesNothing(t *testing.T) {
	t.Parallel()
	e := newPostsEnv(t)
	a := e.uploadText("bob", "a.txt")
	p := e.writeWith("bob", "lobby", "post", a)
	before := len(e.events())
	edited, err := e.posts.Edit(t.Context(), e.as("bob"), p.ID, app.PostInput{Format: app.FormatText, Body: "post", KeepAttachments: attachmentIDs(p)})
	if err != nil {
		t.Fatalf("Edit: %+v", err)
	}
	if n := len(e.events()); n != before || edited.EditedAt != nil || len(edited.Attachments) != 1 {
		t.Errorf("events %d->%d, EditedAt %v, attachments %v", before, n, edited.EditedAt, edited.Attachments)
	}
	e.requireOnDisk(a)
}

func TestPostsEditAttachmentRules(t *testing.T) {
	t.Parallel()
	t.Run("count limit counts the kept ones", func(t *testing.T) {
		e := newPostsEnv(t)
		a, b := e.uploadText("bob", "a"), e.uploadText("bob", "b")
		p := e.writeWith("bob", "lobby", "post", a, b)
		c, d := e.uploadText("bob", "c"), e.uploadText("bob", "d")
		// 2 kept + 2 new = 4 > 3.
		_, err := e.posts.Edit(t.Context(), e.as("bob"), p.ID, app.PostInput{
			Format: app.FormatText, Body: "post", KeepAttachments: attachmentIDs(p), Uploads: []app.Upload{c, d},
		})
		appErr := requireTag(t, err, app.TagInvalidInput, "Posts.Edit.func1.InvalidInput")
		if got := fieldsOf(t, appErr)[app.FieldAttachments]; got != "Attach at most 3 files." {
			t.Errorf("attachments field = %q", got)
		}
		e.requireOnDisk(a, b) // the new files went, the old ones stay
		// Removing one makes room: 1 kept + 2 new = 3.
		c, d = e.uploadText("bob", "c"), e.uploadText("bob", "d")
		edited, err := e.posts.Edit(t.Context(), e.as("bob"), p.ID, app.PostInput{
			Format: app.FormatText, Body: "post", KeepAttachments: attachmentIDs(p)[:1], Uploads: []app.Upload{c, d},
		})
		if err != nil {
			t.Fatalf("Edit: %+v", err)
		}
		if got := attachmentNames(edited); !slices.Equal(got, []string{"a", "c", "d"}) {
			t.Errorf("attachments = %v", got)
		}
		e.requireOnDisk(a, c, d)
	})

	t.Run("the body may be empty when an attachment stays or comes", func(t *testing.T) {
		e := newPostsEnv(t)
		a := e.uploadText("bob", "a")
		p := e.writeWith("bob", "lobby", "post", a)
		edited, err := e.posts.Edit(t.Context(), e.as("bob"), p.ID, app.PostInput{Format: app.FormatText, KeepAttachments: attachmentIDs(p)})
		if err != nil {
			t.Fatalf("Edit: %+v", err)
		}
		if edited.Body != "" || len(edited.Attachments) != 1 {
			t.Errorf("post = %+v", edited)
		}
		b := e.uploadText("bob", "b")
		edited, err = e.posts.Edit(t.Context(), e.as("bob"), p.ID, app.PostInput{Format: app.FormatText, Uploads: []app.Upload{b}})
		if err != nil {
			t.Fatalf("Edit: %+v", err)
		}
		if got := attachmentNames(edited); !slices.Equal(got, []string{"b"}) {
			t.Errorf("attachments = %v", got)
		}
		e.requireOnDisk(b)
	})

	t.Run("removing the last attachment of a post with no body fails and keeps the file", func(t *testing.T) {
		e := newPostsEnv(t)
		a := e.uploadText("bob", "a")
		p := e.writeWith("bob", "lobby", "", a)
		_, err := e.posts.Edit(t.Context(), e.as("bob"), p.ID, app.PostInput{Format: app.FormatText})
		appErr := requireTag(t, err, app.TagInvalidInput, "Posts.Edit.func1.InvalidInput")
		if got := fieldsOf(t, appErr); !reflect.DeepEqual(got, map[string]string{app.FieldBody: "Write something."}) {
			t.Errorf("fields = %v", got)
		}
		e.requireOnDisk(a)
	})

	t.Run("an attachment of another post cannot be kept", func(t *testing.T) {
		e := newPostsEnv(t)
		a, b := e.uploadText("bob", "a"), e.uploadText("bob", "b")
		p1 := e.writeWith("bob", "lobby", "one", a)
		p2 := e.writeWith("bob", "lobby", "two", b)
		_, err := e.posts.Edit(t.Context(), e.as("bob"), p1.ID, app.PostInput{
			Format: app.FormatText, Body: "one", KeepAttachments: append(attachmentIDs(p1), attachmentIDs(p2)...),
		})
		appErr := requireTag(t, err, app.TagInvalidInput, "Posts.Edit.func1.InvalidInput")
		if got := fieldsOf(t, appErr)[app.FieldAttachments]; got != "Upload the files again." {
			t.Errorf("attachments field = %q", got)
		}
		_, err = e.posts.Edit(t.Context(), e.as("bob"), p1.ID, app.PostInput{Format: app.FormatText, Body: "one", KeepAttachments: []uint64{9999}})
		_ = requireTag(t, err, app.TagInvalidInput, "Posts.Edit.func1.InvalidInput")
		e.requireOnDisk(a, b)
		if got, err := e.posts.Get(t.Context(), e.as("bob"), p2.ID); err != nil || len(got.Attachments) != 1 {
			t.Errorf("the other post = %+v, %v", got, err)
		}
	})

	t.Run("a repeated ID is kept once", func(t *testing.T) {
		e := newPostsEnv(t)
		a := e.uploadText("bob", "a")
		p := e.writeWith("bob", "lobby", "post", a)
		id := attachmentIDs(p)[0]
		edited, err := e.posts.Edit(t.Context(), e.as("bob"), p.ID, app.PostInput{Format: app.FormatText, Body: "post", KeepAttachments: []uint64{id, id}})
		if err != nil || len(edited.Attachments) != 1 {
			t.Errorf("Edit = %+v, %v", edited, err)
		}
	})

	t.Run("a comment edits its attachments too", func(t *testing.T) {
		e := newPostsEnv(t)
		post := e.write("bob", "lobby", "post")
		a, b := e.uploadText("bob", "a"), e.uploadText("bob", "b")
		c, err := e.posts.Reply(t.Context(), e.as("bob"), post.ID, app.PostInput{Format: app.FormatText, Body: "c", Uploads: []app.Upload{a}})
		if err != nil {
			t.Fatal(err)
		}
		before := len(e.events())
		if _, err := e.posts.Edit(t.Context(), e.as("bob"), c.ID, app.PostInput{Format: app.FormatText, Body: "c", Uploads: []app.Upload{b}}); err != nil {
			t.Fatalf("Edit: %+v", err)
		}
		e.requireNewEvents(before, wantEvent{
			actor: "bob", action: app.ActionPostEdited, room: e.lobby.ID, entityType: app.EntityComment, entityID: c.ID,
			summary: "Bob edited a comment in #lobby",
			details: map[string]any{"attachments": map[string]any{"added": []any{"b"}, "removed": []any{"a"}}},
		})
		e.requireOnDisk(b)
	})
}

func TestPostsEditByOthersKeepsTheFiles(t *testing.T) {
	t.Parallel()
	e := newPostsEnv(t)
	a := e.uploadText("bob", "a")
	p := e.writeWith("bob", "lobby", "post", a)
	_, err := e.posts.Edit(t.Context(), e.as("admin"), p.ID, app.PostInput{Format: app.FormatText, Body: "x"})
	_ = requireTag(t, err, app.TagForbidden, "Posts.Edit.func1.Forbidden")
	e.requireOnDisk(a)
	got, err := e.posts.Get(t.Context(), e.as("bob"), p.ID)
	if err != nil || len(got.Attachments) != 1 {
		t.Errorf("Get = %+v, %v", got, err)
	}
}

func TestPostsDeleteRemovesTheFiles(t *testing.T) {
	t.Parallel()
	t.Run("a post without replies", func(t *testing.T) {
		e := newPostsEnv(t)
		a, b := e.uploadText("bob", "a"), e.uploadText("bob", "b")
		other := e.uploadText("bob", "other")
		p := e.writeWith("bob", "lobby", "post", a, b)
		e.writeWith("bob", "lobby", "second post", other)
		if err := e.posts.Delete(t.Context(), e.as("bob"), p.ID); err != nil {
			t.Fatalf("Delete: %+v", err)
		}
		e.requireOnDisk(other)
	})

	t.Run("an admin deletes it", func(t *testing.T) {
		e := newPostsEnv(t)
		a := e.uploadText("bob", "a")
		p := e.writeWith("bob", "lobby", "post", a)
		if err := e.posts.Delete(t.Context(), e.as("admin"), p.ID); err != nil {
			t.Fatalf("Delete: %+v", err)
		}
		e.requireOnDisk()
	})

	t.Run("a post with replies stays as a placeholder without its files", func(t *testing.T) {
		e := newPostsEnv(t)
		a := e.uploadText("bob", "a")
		p := e.writeWith("bob", "lobby", "post", a)
		e.say("admin", p.ID, "reply")
		if err := e.posts.Delete(t.Context(), e.as("bob"), p.ID); err != nil {
			t.Fatalf("Delete: %+v", err)
		}
		e.requireOnDisk()
		got, err := e.posts.Get(t.Context(), e.as("bob"), p.ID)
		if err != nil || !got.Deleted || len(got.Attachments) != 0 || got.Attachments == nil {
			t.Errorf("Get = %+v, %v", got, err)
		}
	})

	t.Run("a comment's files go with it", func(t *testing.T) {
		e := newPostsEnv(t)
		a := e.uploadText("bob", "a")
		post := e.write("bob", "lobby", "post")
		c, err := e.posts.Reply(t.Context(), e.as("bob"), post.ID, app.PostInput{Format: app.FormatText, Body: "c", Uploads: []app.Upload{a}})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.posts.Delete(t.Context(), e.as("bob"), c.ID); err != nil {
			t.Fatalf("Delete: %+v", err)
		}
		e.requireOnDisk()
	})

	t.Run("a placeholder chain", func(t *testing.T) {
		e := newPostsEnv(t)
		a, b, c := e.uploadText("bob", "a"), e.uploadText("bob", "b"), e.uploadText("bob", "c")
		root := e.writeWith("bob", "lobby", "root", a)
		mid, err := e.posts.Reply(t.Context(), e.as("bob"), root.ID, app.PostInput{Format: app.FormatText, Body: "mid", Uploads: []app.Upload{b}})
		if err != nil {
			t.Fatal(err)
		}
		leaf, err := e.posts.Reply(t.Context(), e.as("bob"), mid.ID, app.PostInput{Format: app.FormatText, Body: "leaf", Uploads: []app.Upload{c}})
		if err != nil {
			t.Fatal(err)
		}
		// The two above the leaf become placeholders; their files go now.
		for _, id := range []uint64{root.ID, mid.ID} {
			if err := e.posts.Delete(t.Context(), e.as("bob"), id); err != nil {
				t.Fatalf("Delete %d: %+v", id, err)
			}
		}
		e.requireOnDisk(c)
		// Deleting the leaf removes the whole chain.
		if err := e.posts.Delete(t.Context(), e.as("bob"), leaf.ID); err != nil {
			t.Fatalf("Delete leaf: %+v", err)
		}
		e.requireOnDisk()
		if e.rowExists(root.ID) || e.rowExists(mid.ID) || e.rowExists(leaf.ID) {
			t.Error("part of the chain is still in the database")
		}
	})

	t.Run("a refused delete keeps the files", func(t *testing.T) {
		e := newPostsEnv(t)
		a := e.uploadText("bob", "a")
		p := e.writeWith("bob", "lobby", "post", a)
		err := e.posts.Delete(t.Context(), e.as("alice"), p.ID)
		_ = requireTag(t, err, app.TagForbidden, "Posts.Delete.func1.Forbidden")
		e.requireOnDisk(a)
	})
}

func TestRoomsDeleteRemovesTheFiles(t *testing.T) {
	t.Parallel()
	e := newPostsEnv(t)
	inLobby := e.uploadText("bob", "lobby-post")
	inLobbyComment := e.uploadText("bob", "lobby-comment")
	inLobbyGone := e.uploadText("bob", "lobby-placeholder")
	inDen := e.uploadText("bob", "den-post")
	p := e.writeWith("bob", "lobby", "post", inLobby)
	if _, err := e.posts.Reply(t.Context(), e.as("bob"), p.ID, app.PostInput{Format: app.FormatText, Body: "c", Uploads: []app.Upload{inLobbyComment}}); err != nil {
		t.Fatal(err)
	}
	q := e.writeWith("bob", "lobby", "second", inLobbyGone)
	e.say("admin", q.ID, "reply keeps the placeholder")
	if err := e.posts.Delete(t.Context(), e.as("bob"), q.ID); err != nil {
		t.Fatal(err)
	}
	e.writeWith("bob", "den", "den post", inDen)
	e.requireOnDisk(inLobby, inLobbyComment, inDen)

	if err := e.rooms.Delete(t.Context(), e.as("admin"), e.lobby.ID); err != nil {
		t.Fatalf("Delete: %+v", err)
	}
	e.requireOnDisk(inDen)

	// A refused delete removes nothing.
	err := e.rooms.Delete(t.Context(), e.as("bob"), e.den.ID)
	_ = requireTag(t, err, app.TagForbidden, "Rooms.Delete.func1->loadManageableRoom.Forbidden")
	e.requireOnDisk(inDen)
	if err := e.rooms.Delete(t.Context(), e.as("alice"), e.den.ID); err != nil {
		t.Fatalf("Delete den: %+v", err)
	}
	e.requireOnDisk()
}

// logLines decodes the JSON log lines in buf.
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	dec := json.NewDecoder(buf)
	for dec.More() {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func TestPostsFilesThatCannotBeRemovedAreLogged(t *testing.T) {
	t.Parallel()
	e := newPostsEnv(t)
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	posts := app.NewPosts(e.st, e.clk, markdown.New(), e.files, app.PostsConfig{Logger: logger}, nil)
	u, err := posts.Upload(t.Context(), e.as("bob"), "a", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := posts.Create(t.Context(), e.as("bob"), e.lobby.ID, app.PostInput{Format: app.FormatText, Body: "x", Uploads: []app.Upload{u}})
	if err != nil {
		t.Fatal(err)
	}
	// Make the file impossible to remove: a directory with a file in it.
	path := filepath.Join(e.filesDir, u.Key)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "inner"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := posts.Delete(t.Context(), e.as("bob"), p.ID); err != nil {
		t.Fatalf("Delete must succeed although the file stays: %+v", err)
	}
	lines := logLines(t, &buf)
	if len(lines) != 1 || lines[0]["msg"] != "removing a file failed" {
		t.Fatalf("log = %v", lines)
	}
	errAttr, ok := lines[0]["err"].(map[string]any)
	if !ok || errAttr["Tag"] != "FileRemoveFailed" {
		t.Errorf("err = %v", lines[0]["err"])
	}
}

func TestPostsAttachmentPermissions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		actor string
		room  string
		want  string
	}{
		{"bob", "lobby", ""},   // a member
		{"admin", "lobby", ""}, // a member
		{"carol", "lobby", ""}, // not a member of a visible room: may read
		{"root", "lobby", ""},  // an admin who is not a member
		{"dave", "lobby", ""},  // any user
		{"alice", "den", ""},   // a member of the private room
		{"bob", "den", ""},     // a member of the private room
		{"carol", "den", app.TagNotFound},
		{"admin", "den", app.TagNotFound},
		{"root", "den", app.TagNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.actor+" on "+tc.room, func(t *testing.T) {
			e := newPostsEnv(t)
			u := e.upload("bob", "doc.txt", []byte("the content"))
			p := e.writeWith("bob", tc.room, "post", u)
			id := p.Attachments[0].ID
			view, f, err := e.posts.Attachment(t.Context(), e.as(tc.actor), id)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("got %+v, want success", err)
				}
				defer func() {
					if err := f.Close(); err != nil {
						t.Error(err)
					}
				}()
				if view.ID != id || view.FileName != "doc.txt" || view.Size != int64(len("the content")) || view.IsImage {
					t.Errorf("view = %+v", view)
				}
				b, err := io.ReadAll(f)
				if err != nil || string(b) != "the content" {
					t.Errorf("content = %q, %v", b, err)
				}
				if _, err := f.Seek(0, io.SeekStart); err != nil {
					t.Errorf("Seek: %v", err)
				}
				return
			}
			_ = requireTag(t, err, tc.want, "Posts.Attachment->loadReadableRoom.NotFound")
			if f != nil {
				t.Error("a refused call returned a file")
			}
		})
	}
}

func TestPostsAttachmentMissingAndDeleted(t *testing.T) {
	t.Parallel()
	t.Run("a missing attachment", func(t *testing.T) {
		e := newPostsEnv(t)
		_, _, err := e.posts.Attachment(t.Context(), e.as("bob"), 9999)
		_ = requireTag(t, err, app.TagNotFound, "Posts.Attachment->Store.AttachmentByID.NotFound")
	})

	t.Run("an attachment of a deleted post", func(t *testing.T) {
		e := newPostsEnv(t)
		u := e.uploadText("bob", "a.txt")
		p := e.writeWith("bob", "lobby", "post", u)
		id := p.Attachments[0].ID
		e.say("admin", p.ID, "reply")
		if err := e.posts.Delete(t.Context(), e.as("bob"), p.ID); err != nil {
			t.Fatal(err)
		}
		_, _, err := e.posts.Attachment(t.Context(), e.as("bob"), id)
		_ = requireTag(t, err, app.TagNotFound, "Posts.Attachment->Store.AttachmentByID.NotFound")
	})

	t.Run("a row that outlives the deletion of its post", func(t *testing.T) {
		// The service removes the rows of a deleted post; this makes the
		// check of Attachment itself reachable.
		e := newPostsEnv(t)
		u := e.uploadText("bob", "a.txt")
		p := e.writeWith("bob", "lobby", "post", u)
		e.say("admin", p.ID, "reply")
		if err := e.st.MarkPostDeleted(t.Context(), p.ID, e.clk.Now()); err != nil {
			t.Fatal(err)
		}
		_, _, err := e.posts.Attachment(t.Context(), e.as("bob"), p.Attachments[0].ID)
		_ = requireTag(t, err, app.TagNotFound, "Posts.Attachment.NotFound")
	})

	t.Run("the file is gone from the disk", func(t *testing.T) {
		e := newPostsEnv(t)
		u := e.uploadText("bob", "a.txt")
		p := e.writeWith("bob", "lobby", "post", u)
		if err := os.Remove(filepath.Join(e.filesDir, u.Key)); err != nil {
			t.Fatal(err)
		}
		_, _, err := e.posts.Attachment(t.Context(), e.as("bob"), p.Attachments[0].ID)
		_ = requireTag(t, err, files.TagNotFound, "Posts.Attachment->Store.Open.FileNotFound")
	})
}

// withoutTimes returns the views with CreatedAt zeroed, after checking that
// each has one.
func withoutTimes(views []app.AttachmentView) []app.AttachmentView {
	out := make([]app.AttachmentView, len(views))
	for i, v := range views {
		if v.CreatedAt.IsZero() {
			panic("an attachment view without CreatedAt")
		}
		v.CreatedAt = time.Time{}
		out[i] = v
	}
	return out
}

func TestPostsViewsCarryTheAttachments(t *testing.T) {
	t.Parallel()
	e := newPostsEnv(t)
	img := e.upload("bob", "pic.png", pngData(t, 8, 6))
	txt := e.uploadText("bob", "a.txt")
	cu := e.uploadText("admin", "reply.txt")
	withFiles := e.writeWith("bob", "lobby", "with files", img, txt)
	plain := e.write("bob", "lobby", "plain")
	comment, err := e.posts.Reply(t.Context(), e.as("admin"), withFiles.ID, app.PostInput{Format: app.FormatText, Body: "re", Uploads: []app.Upload{cu}})
	if err != nil {
		t.Fatal(err)
	}
	want := []app.AttachmentView{
		{ID: withFiles.Attachments[0].ID, FileName: "pic.png", ContentType: "image/png", Size: img.Size, IsImage: true, Width: 8, Height: 6},
		{ID: withFiles.Attachments[1].ID, FileName: "a.txt", ContentType: "text/plain; charset=utf-8", Size: txt.Size},
	}

	page, err := e.posts.Feed(t.Context(), e.as("carol"), e.lobby.ID, 0, 10)
	if err != nil || len(page.Posts) != 2 {
		t.Fatalf("Feed = %+v, %v", page, err)
	}
	if got := page.Posts[1].Attachments; !reflect.DeepEqual(withoutTimes(got), want) {
		t.Errorf("Feed attachments = %+v, want %+v", got, want)
	}
	if got := page.Posts[0].Attachments; page.Posts[0].ID != plain.ID || got == nil || len(got) != 0 {
		t.Errorf("a post without files has %#v", got)
	}

	got, err := e.posts.Get(t.Context(), e.as("carol"), withFiles.ID)
	if err != nil || !reflect.DeepEqual(withoutTimes(got.Attachments), want) {
		t.Errorf("Get = %+v, %v", got.Attachments, err)
	}

	th := e.thread("carol", comment.ID)
	if !reflect.DeepEqual(withoutTimes(th.Root.Attachments), want) {
		t.Errorf("Thread root attachments = %+v", th.Root.Attachments)
	}
	if len(th.Comments) != 1 || len(th.Comments[0].Attachments) != 1 || th.Comments[0].Attachments[0].FileName != "reply.txt" {
		t.Errorf("Thread comments = %+v", th.Comments)
	}

	// A deleted comment that stays shows no files.
	e.say("bob", comment.ID, "reply to the reply")
	if err := e.posts.Delete(t.Context(), e.as("admin"), comment.ID); err != nil {
		t.Fatal(err)
	}
	th = e.thread("carol", withFiles.ID)
	if len(th.Comments) < 1 || !th.Comments[0].Deleted || th.Comments[0].Attachments == nil || len(th.Comments[0].Attachments) != 0 {
		t.Errorf("deleted comment = %+v", th.Comments)
	}
}

func TestPostsDiscardUploads(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		user string
		// wantGone is true when the unused upload is removed from the disk.
		wantGone bool
	}{
		{"a member", "bob", true},
		{"an admin", "admin", true},
		{"nobody", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newPostsEnv(t)
			free := e.uploadText("bob", "free.txt")
			used := e.uploadText("bob", "used.txt")
			e.writeWith("bob", "lobby", "post", used)
			actor := app.Actor{}
			if tt.user != "" {
				actor = e.as(tt.user)
			}
			e.posts.DiscardUploads(t.Context(), actor, []app.Upload{free, used})
			onDisk := e.onDisk()
			if got := !slices.Contains(onDisk, free.Key); got != tt.wantGone {
				t.Errorf("free upload removed = %v, want %v (on disk %v)", got, tt.wantGone, onDisk)
			}
			// The file of an attachment is never removed.
			if !slices.Contains(onDisk, used.Key) {
				t.Errorf("the file of an attachment was removed")
			}
		})
	}
}
