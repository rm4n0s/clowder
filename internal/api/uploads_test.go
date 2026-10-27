package api

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/files"
)

// formPart is a part of a multipart form a test sends. A part with a file name
// is a file.
type formPart struct {
	name, fileName, content string
}

// multipartRequest makes a request whose body is the form of the parts, with
// the Content-Type that names its boundary.
func multipartRequest(method, target string, parts []formPart, opts ...reqOpt) *http.Request {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, p := range parts {
		h := textproto.MIMEHeader{}
		disposition := fmt.Sprintf(`form-data; name=%q`, p.name)
		if p.fileName != "" {
			disposition += fmt.Sprintf(`; filename=%q`, p.fileName)
		}
		h.Set("Content-Disposition", disposition)
		part, err := w.CreatePart(h)
		if err != nil {
			panic(err)
		}
		if _, err := io.WriteString(part, p.content); err != nil {
			panic(err)
		}
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	r := httptest.NewRequest(method, target, &body)
	r.Header.Set("Content-Type", w.FormDataContentType())
	for _, o := range opts {
		o(r)
	}
	return r
}

// pngBytes is a PNG of w by h pixels.
func pngBytes(t *testing.T, w, h int) string {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// upload sends the file as the user and returns the answer.
func (f *forum) upload(user, fileName, content string) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.send(multipartRequest(http.MethodPost, "/api/v1/uploads",
		[]formPart{{name: "file", fileName: fileName, content: content}}, bearer(f.as(user))))
}

// uploadID uploads a file and returns its upload ID, failing the test unless
// it is stored.
func (f *forum) uploadID(user, fileName, content string) string {
	f.t.Helper()
	rec := f.upload(user, fileName, content)
	requireStatus(f.t, rec, http.StatusCreated)
	id, _ := decode(f.t, rec)["id"].(string)
	if id == "" {
		f.t.Fatalf("no id in %s", rec.Body)
	}
	return id
}

// onDisk returns the keys of the files stored, sorted.
func (f *forum) onDisk() []string {
	f.t.Helper()
	entries, err := os.ReadDir(f.filesDir)
	if err != nil {
		f.t.Fatalf("ReadDir: %v", err)
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

// notes returns the keys of the notes of the uploads waiting, sorted.
func (f *forum) notes() []string {
	f.t.Helper()
	entries, err := os.ReadDir(f.filesDir + "/notes")
	if os.IsNotExist(err) {
		return []string{}
	}
	if err != nil {
		f.t.Fatalf("ReadDir: %v", err)
	}
	keys := []string{}
	for _, en := range entries {
		keys = append(keys, en.Name())
	}
	slices.Sort(keys)
	return keys
}

func TestUpload(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	t.Run("a text file", func(t *testing.T) {
		rec := f.upload("bob", "notes.txt", "Hello, this is a text file.")
		requireStatus(t, rec, http.StatusCreated)
		got := decode(t, rec)
		id, _ := got["id"].(string)
		if len(id) != 64 || got["file_name"] != "notes.txt" || got["size"] != float64(27) || got["width"] != nil || got["height"] != nil {
			t.Errorf("answer = %v", got)
		}
		if ct, _ := got["content_type"].(string); !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("content_type = %v", got["content_type"])
		}
		if disk := f.onDisk(); len(disk) == 0 || !slices.Contains(disk, id) {
			t.Errorf("the file is not stored under its id: %v", disk)
		}
	})
	t.Run("an image has its size in pixels", func(t *testing.T) {
		rec := f.upload("bob", "dot.png", pngBytes(t, 3, 2))
		requireStatus(t, rec, http.StatusCreated)
		got := decode(t, rec)
		if got["content_type"] != "image/png" || got["width"] != float64(3) || got["height"] != float64(2) {
			t.Errorf("answer = %v", got)
		}
	})
	t.Run("an empty file", func(t *testing.T) {
		rec := f.upload("bob", "empty.txt", "")
		requireStatus(t, rec, http.StatusCreated)
		if got := decode(t, rec); got["size"] != float64(0) {
			t.Errorf("answer = %v", got)
		}
	})
	t.Run("the name is cleaned", func(t *testing.T) {
		rec := f.upload("bob", `..\..\secret\report.txt`, "x")
		requireStatus(t, rec, http.StatusCreated)
		if got := decode(t, rec)["file_name"]; got != "report.txt" {
			t.Errorf("file_name = %v", got)
		}
	})
	t.Run("any logged-in user may upload", func(t *testing.T) {
		for _, user := range []string{"admin", "root", "alice", "bob", "carol"} {
			requireStatus(t, f.upload(user, "a.txt", "x"), http.StatusCreated)
		}
	})
	t.Run("a note of the upload is kept for the later request", func(t *testing.T) {
		id := f.uploadID("carol", "kept.txt", "x")
		if !slices.Contains(f.notes(), id) {
			t.Errorf("notes = %v, want one for %s", f.notes(), id)
		}
	})
}

// TestUploadMistakes is the table of forms that are not one file in the field
// file: each is refused and leaves nothing on disk.
func TestUploadMistakes(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("x", 1<<20+1) // one byte over the limit of the tests
	tests := []struct {
		name   string
		parts  []formPart
		status int
		code   string
		field  string
	}{
		{"no part", nil, http.StatusUnprocessableEntity, "invalid_input", "file"},
		{"another field name", []formPart{{"document", "a.txt", "x"}}, http.StatusUnprocessableEntity, "invalid_input", "file"},
		{"the field file as text", []formPart{{"file", "", "x"}}, http.StatusUnprocessableEntity, "invalid_input", "file"},
		{"a text field before the file", []formPart{{"note", "", "x"}, {"file", "a.txt", "x"}}, http.StatusUnprocessableEntity, "invalid_input", "file"},
		{"a text field after the file", []formPart{{"file", "a.txt", "x"}, {"note", "", "x"}}, http.StatusUnprocessableEntity, "invalid_input", "file"},
		{"two files", []formPart{{"file", "a.txt", "x"}, {"file", "b.txt", "y"}}, http.StatusUnprocessableEntity, "invalid_input", "file"},
		{"two files in other fields", []formPart{{"file", "a.txt", "x"}, {"files", "b.txt", "y"}}, http.StatusUnprocessableEntity, "invalid_input", "file"},
		{"a file over the limit", []formPart{{"file", "big.bin", big}}, http.StatusRequestEntityTooLarge, "too_large", ""},
		{"a file far over the limit", []formPart{{"file", "big.bin", strings.Repeat("x", 3<<20)}}, http.StatusRequestEntityTooLarge, "too_large", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			rec := f.send(multipartRequest(http.MethodPost, "/api/v1/uploads", tt.parts, bearer(f.as("bob"))))
			requireStatus(t, rec, tt.status)
			if got := errorCode(t, rec); got != tt.code {
				t.Errorf("code = %q, want %q", got, tt.code)
			}
			if tt.field != "" && errorFields(t, rec)[tt.field] == nil {
				t.Errorf("fields = %v, want %q", errorFields(t, rec), tt.field)
			}
			if disk := f.onDisk(); len(disk) != 0 {
				t.Errorf("a refused upload left %v on disk", disk)
			}
			if notes := f.notes(); len(notes) != 0 {
				t.Errorf("a refused upload left the notes %v", notes)
			}
		})
	}
	t.Run("a file at the limit", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		requireStatus(t, f.upload("bob", "edge.bin", strings.Repeat("x", 1<<20)), http.StatusCreated)
	})
	t.Run("not a form", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		for name, contentType := range map[string]string{
			"json": "application/json", "urlencoded": "application/x-www-form-urlencoded", "text": "text/plain", "none": "", "garbage": ";;;",
		} {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", strings.NewReader("file=1"))
			if contentType != "" {
				r.Header.Set("Content-Type", contentType)
			}
			r.Header.Set("Authorization", "Bearer "+f.as("bob"))
			rec := f.send(r)
			requireStatus(t, rec, http.StatusUnsupportedMediaType)
			if got := errorCode(t, rec); got != "unsupported_media_type" {
				t.Errorf("%s: code = %q", name, got)
			}
		}
	})
	t.Run("a form with no boundary", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		r := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", strings.NewReader("x"))
		r.Header.Set("Content-Type", "multipart/form-data")
		rec := f.send(request(http.MethodPost, "/api/v1/uploads", nil, func(q *http.Request) { *q = *r }, bearer(f.as("bob"))))
		requireStatus(t, rec, http.StatusUnprocessableEntity)
		if errorFields(t, rec)["file"] == nil {
			t.Errorf("fields = %v", errorFields(t, rec))
		}
	})
	// A long preamble before the first boundary is skipped by the reader, and
	// counts against the cap of the body.
	rawForm := func(preamble, content string) *http.Request {
		body := preamble + "--b\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.txt\"\r\n\r\n" + content + "\r\n--b--\r\n"
		r := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", strings.NewReader(body))
		r.Header.Set("Content-Type", "multipart/form-data; boundary=b")
		return r
	}
	t.Run("a preamble over the cap", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		r := rawForm(preambleOf(3<<20), "small")
		r.Header.Set("Authorization", "Bearer "+f.as("bob"))
		rec := f.send(r)
		requireStatus(t, rec, http.StatusRequestEntityTooLarge)
		if len(f.onDisk()) != 0 {
			t.Errorf("left %v on disk", f.onDisk())
		}
	})
	t.Run("a file under the limit that runs the body over the cap", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		r := rawForm(preambleOf(1500<<10), strings.Repeat("y", 700<<10))
		r.Header.Set("Authorization", "Bearer "+f.as("bob"))
		rec := f.send(r)
		requireStatus(t, rec, http.StatusRequestEntityTooLarge)
		if len(f.onDisk()) != 0 || len(f.notes()) != 0 {
			t.Errorf("left %v and the notes %v", f.onDisk(), f.notes())
		}
		// The same two parts without the preamble are a good upload.
		r = rawForm("", strings.Repeat("y", 700<<10))
		r.Header.Set("Authorization", "Bearer "+f.as("bob"))
		requireStatus(t, f.send(r), http.StatusCreated)
	})
	t.Run("a body that ends in the middle of the file", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		body := "--b\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.txt\"\r\n\r\nhalf a fi"
		r := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", strings.NewReader(body))
		r.Header.Set("Content-Type", "multipart/form-data; boundary=b")
		r.Header.Set("Authorization", "Bearer "+f.as("bob"))
		rec := f.send(r)
		requireStatus(t, rec, http.StatusUnprocessableEntity)
		if errorFields(t, rec)["file"] == nil {
			t.Errorf("fields = %v", errorFields(t, rec))
		}
		if len(f.onDisk()) != 0 || len(f.notes()) != 0 {
			t.Errorf("a broken upload left %v and the notes %v", f.onDisk(), f.notes())
		}
	})
}

// preambleOf makes lines of text, n bytes of them, to put before the first
// boundary of a form: the reader skips them.
func preambleOf(n int) string {
	line := strings.Repeat("x", 78) + "\r\n"
	return strings.Repeat(line, n/len(line)+1)
}

// countingBody is a request body that counts the bytes read from it.
type countingBody struct {
	r io.Reader
	n int
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// TestUploadBodyIsCutEarly checks that a body far over the limit is not read to
// its end: the cap is set before the form is read.
func TestUploadBodyIsCutEarly(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	var form bytes.Buffer
	w := multipart.NewWriter(&form)
	part, err := w.CreateFormFile("file", "huge.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(bytes.Repeat([]byte("x"), 20<<20)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	counter := &countingBody{r: &form}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", counter)
	r.Header.Set("Content-Type", w.FormDataContentType())
	r.Header.Set("Authorization", "Bearer "+f.as("bob"))
	rec := f.send(r)
	requireStatus(t, rec, http.StatusRequestEntityTooLarge)
	if counter.n > 3<<20 {
		t.Errorf("%d bytes of the body were read, want it cut at about the limit (1 MiB, and a megabyte for the form)", counter.n)
	}
	if disk := f.onDisk(); len(disk) != 0 {
		t.Errorf("left %v on disk", disk)
	}
}

func TestUploadedFilesBecomeAttachments(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	text := f.uploadID("bob", "notes.txt", "Hello, this is a text file.")
	image := f.uploadID("bob", "dot.png", pngBytes(t, 3, 2))
	before := len(f.audit())
	rec := f.call("bob", http.MethodPost, path("/rooms/%d/posts", f.lobby.ID), map[string]any{
		"body": "files", "upload_ids": []string{text, image},
		"tasks": []map[string]any{{"title": "Read it"}},
	})
	requireStatus(t, rec, http.StatusCreated)
	post := decode(t, rec)
	atts := list(t, post["attachments"])
	if len(atts) != 2 {
		t.Fatalf("attachments = %v", atts)
	}
	first, second := obj(t, atts[0]), obj(t, atts[1])
	if first["file_name"] != "notes.txt" || second["file_name"] != "dot.png" || second["width"] != float64(3) || second["height"] != float64(2) {
		t.Errorf("attachments = %v", atts)
	}
	if want := fmt.Sprintf("/api/v1/attachments/%d", uint64(first["id"].(float64))); first["url"] != want {
		t.Errorf("url = %v, want %s", first["url"], want)
	}
	f.requireAudit(before, "post.created post", "task.created task")
	if got := f.notes(); len(got) != 0 {
		t.Errorf("the notes of the taken uploads are still there: %v", got)
	}

	t.Run("the files are downloaded as they were uploaded", func(t *testing.T) {
		rec := f.call("carol", http.MethodGet, first["url"].(string), nil)
		requireStatus(t, rec, http.StatusOK)
		if rec.Body.String() != "Hello, this is a text file." {
			t.Errorf("body = %q", rec.Body)
		}
		rec = f.call("carol", http.MethodGet, second["url"].(string), nil)
		requireStatus(t, rec, http.StatusOK)
		if rec.Body.String() != pngBytes(t, 3, 2) {
			t.Error("the image changed")
		}
	})
	t.Run("an upload is taken once", func(t *testing.T) {
		rec := f.call("bob", http.MethodPost, path("/rooms/%d/posts", f.lobby.ID), map[string]any{"body": "again", "upload_ids": []string{text}})
		requireStatus(t, rec, http.StatusUnprocessableEntity)
		if errorFields(t, rec)["upload_ids"] == nil {
			t.Errorf("fields = %v", errorFields(t, rec))
		}
		// The file of the first post is still there.
		requireStatus(t, f.call("carol", http.MethodGet, first["url"].(string), nil), http.StatusOK)
	})
	t.Run("a comment takes uploads too", func(t *testing.T) {
		id := f.uploadID("admin", "reply.txt", "reply")
		rec := f.call("admin", http.MethodPost, path("/posts/%d/comments", uint64(post["id"].(float64))), map[string]any{"upload_ids": []string{id}})
		requireStatus(t, rec, http.StatusCreated)
		if atts := list(t, decode(t, rec)["attachments"]); len(atts) != 1 || obj(t, atts[0])["file_name"] != "reply.txt" {
			t.Errorf("attachments = %v", atts)
		}
	})
}

func TestUploadedFilesOfAnEdit(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	old := f.uploadID("bob", "old.txt", "old")
	created := decode(t, f.call("bob", http.MethodPost, path("/rooms/%d/posts", f.lobby.ID), map[string]any{"body": "p", "upload_ids": []string{old}}))
	postID := uint64(created["id"].(float64))
	oldAttachment := idOf(t, list(t, created["attachments"])[0])

	added := f.uploadID("bob", "added.txt", "added")
	rec := f.call("bob", http.MethodPatch, path("/posts/%d", postID), map[string]any{"upload_ids": []string{added}})
	requireStatus(t, rec, http.StatusOK)
	atts := list(t, decode(t, rec)["attachments"])
	if len(atts) != 2 || obj(t, atts[0])["file_name"] != "old.txt" || obj(t, atts[1])["file_name"] != "added.txt" {
		t.Fatalf("an edit that leaves attachment_ids out keeps the old files: %v", atts)
	}

	third := f.uploadID("bob", "third.txt", "third")
	rec = f.call("bob", http.MethodPatch, path("/posts/%d", postID), map[string]any{"attachment_ids": []uint64{oldAttachment}, "upload_ids": []string{third}})
	requireStatus(t, rec, http.StatusOK)
	atts = list(t, decode(t, rec)["attachments"])
	if len(atts) != 2 || obj(t, atts[0])["file_name"] != "old.txt" || obj(t, atts[1])["file_name"] != "third.txt" {
		t.Errorf("attachments = %v, want old.txt and third.txt", atts)
	}
	if got := f.notes(); len(got) != 0 {
		t.Errorf("notes left: %v", got)
	}
	t.Run("someone else's upload is refused on an edit", func(t *testing.T) {
		theirs := f.uploadID("admin", "theirs.txt", "x")
		rec := f.call("bob", http.MethodPatch, path("/posts/%d", postID), map[string]any{"upload_ids": []string{theirs}})
		requireStatus(t, rec, http.StatusUnprocessableEntity)
		if errorFields(t, rec)["upload_ids"] == nil {
			t.Errorf("fields = %v", errorFields(t, rec))
		}
	})
}

func TestUploadsOfAnotherUserAreRefused(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	bobs := f.uploadID("bob", "bobs.txt", "x")
	for _, user := range []string{"admin", "root", "alice", "carol"} {
		t.Run(user, func(t *testing.T) {
			room := "lobby"
			if user == "alice" {
				room = "den"
			}
			rec := f.call(user, http.MethodPost, path("/rooms/%d/posts", f.room(room).ID), map[string]any{"body": "mine now", "upload_ids": []string{bobs}})
			if user == "admin" || user == "alice" {
				requireStatus(t, rec, http.StatusUnprocessableEntity)
				if errorFields(t, rec)["upload_ids"] == nil {
					t.Errorf("fields = %v", errorFields(t, rec))
				}
			} else {
				// Not members: the post is refused before it looks at the file, or the file is.
				if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnprocessableEntity {
					t.Errorf("status = %d", rec.Code)
				}
			}
		})
	}
	// The upload is still bob's.
	rec := f.call("bob", http.MethodPost, path("/rooms/%d/posts", f.lobby.ID), map[string]any{"body": "mine", "upload_ids": []string{bobs}})
	requireStatus(t, rec, http.StatusCreated)
}

func TestUploadIDsThatAreNotUploads(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	good := f.uploadID("bob", "a.txt", "x")
	for _, tt := range []struct {
		name string
		ids  []string
	}{
		{"unknown", []string{strings.Repeat("0", 64)}},
		{"not a key", []string{"../etc/passwd"}},
		{"empty", []string{""}},
		{"too long", []string{good + "00"}},
		{"upper case", []string{strings.ToUpper(good)}},
		{"one good and one unknown", []string{good, strings.Repeat("1", 64)}},
		{"the same twice", []string{good, good}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.call("bob", http.MethodPost, path("/rooms/%d/posts", f.lobby.ID), map[string]any{"body": "x", "upload_ids": tt.ids})
			requireStatus(t, rec, http.StatusUnprocessableEntity)
			if errorFields(t, rec)["upload_ids"] == nil {
				t.Errorf("fields = %v", errorFields(t, rec))
			}
		})
	}
	t.Run("too many for a post", func(t *testing.T) {
		var ids []string
		for i := 0; i < 5; i++ { // the tests allow 4
			ids = append(ids, f.uploadID("bob", fmt.Sprintf("f%d.txt", i), "x"))
		}
		rec := f.call("bob", http.MethodPost, path("/rooms/%d/posts", f.lobby.ID), map[string]any{"body": "x", "upload_ids": ids})
		requireStatus(t, rec, http.StatusUnprocessableEntity)
		if errorFields(t, rec)["upload_ids"] == nil {
			t.Errorf("fields = %v", errorFields(t, rec))
		}
	})
}

// TestAFailedPostRemovesItsUploads checks that a post that is refused removes
// the files it was to take, and that the upload IDs are then spent.
func TestAFailedPostRemovesItsUploads(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		user   string
		target func(f *forum) string
		body   func(id string) map[string]any
		status int
	}{
		{"a user who is not a member", "carol", func(f *forum) string { return path("/rooms/%d/posts", f.lobby.ID) },
			func(id string) map[string]any { return map[string]any{"body": "x", "upload_ids": []string{id}} }, http.StatusForbidden},
		{"a room that is private", "carol", func(f *forum) string { return path("/rooms/%d/posts", f.den.ID) },
			func(id string) map[string]any { return map[string]any{"body": "x", "upload_ids": []string{id}} }, http.StatusNotFound},
		{"a body that breaks a rule", "bob", func(f *forum) string { return path("/rooms/%d/posts", f.lobby.ID) },
			func(id string) map[string]any {
				return map[string]any{"body": strings.Repeat("é", 20001), "upload_ids": []string{id}}
			}, http.StatusUnprocessableEntity},
		{"a task that breaks a rule", "bob", func(f *forum) string { return path("/rooms/%d/posts", f.lobby.ID) },
			func(id string) map[string]any {
				return map[string]any{"body": "x", "upload_ids": []string{id}, "tasks": []map[string]any{{"title": " "}}}
			}, http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			id := f.uploadID(tt.user, "a.txt", "x")
			before := len(f.audit())
			rec := f.call(tt.user, http.MethodPost, tt.target(f), tt.body(id))
			requireStatus(t, rec, tt.status)
			f.requireAudit(before)
			if disk := f.onDisk(); len(disk) != 0 {
				t.Errorf("the refused post left %v on disk", disk)
			}
			if notes := f.notes(); len(notes) != 0 {
				t.Errorf("the refused post left the notes %v", notes)
			}
			// The ID is spent.
			rec = f.call(tt.user, http.MethodPost, path("/rooms/%d/posts", f.lobby.ID), map[string]any{"body": "again", "upload_ids": []string{id}})
			if tt.user == "bob" {
				requireStatus(t, rec, http.StatusUnprocessableEntity)
				if errorFields(t, rec)["upload_ids"] == nil {
					t.Errorf("fields = %v", errorFields(t, rec))
				}
			}
		})
	}
	t.Run("a comment on a post that is not there", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		id := f.uploadID("bob", "a.txt", "x")
		requireStatus(t, f.call("bob", http.MethodPost, path("/posts/%d/comments", 99999), map[string]any{"upload_ids": []string{id}}), http.StatusNotFound)
		if disk := f.onDisk(); len(disk) != 0 {
			t.Errorf("left %v on disk", disk)
		}
	})
	t.Run("an edit by someone else", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		p := f.write("bob", "lobby", "p")
		id := f.uploadID("admin", "a.txt", "x")
		requireStatus(t, f.call("admin", http.MethodPatch, path("/posts/%d", p.ID), map[string]any{"upload_ids": []string{id}}), http.StatusForbidden)
		if disk := f.onDisk(); len(disk) != 0 {
			t.Errorf("left %v on disk", disk)
		}
	})
}

// attachmentOf uploads the files as the user, writes a post with them in the
// room and returns the IDs of its attachments.
func (f *forum) attachmentsOf(user, room string, uploads ...[2]string) []uint64 {
	f.t.Helper()
	var ids []string
	for _, u := range uploads {
		ids = append(ids, f.uploadID(user, u[0], u[1]))
	}
	rec := f.call(user, http.MethodPost, path("/rooms/%d/posts", f.room(room).ID), map[string]any{"body": "files", "upload_ids": ids})
	requireStatus(f.t, rec, http.StatusCreated)
	var out []uint64
	for _, a := range list(f.t, decode(f.t, rec)["attachments"]) {
		out = append(out, idOf(f.t, a))
	}
	return out
}

func TestDownloadPermissions(t *testing.T) {
	t.Parallel()
	for _, tt := range roomReadStatuses {
		t.Run(tt.user+" on "+tt.room, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			ids := f.attachmentsOf("bob", tt.room, [2]string{"a.txt", "content"})
			rec := f.call(tt.user, http.MethodGet, path("/attachments/%d", ids[0]), nil)
			requireStatus(t, rec, tt.want)
			if tt.want == http.StatusOK && rec.Body.String() != "content" {
				t.Errorf("body = %q", rec.Body)
			}
		})
	}
	t.Run("a private room's file is not found by a non-member, with no hint", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		ids := f.attachmentsOf("bob", "den", [2]string{"secret.txt", "secret"})
		denied := f.call("carol", http.MethodGet, path("/attachments/%d", ids[0]), nil)
		missing := f.call("carol", http.MethodGet, path("/attachments/%d", ids[0]+1000), nil)
		requireStatus(t, denied, http.StatusNotFound)
		requireStatus(t, missing, http.StatusNotFound)
		if denied.Body.String() != missing.Body.String() {
			t.Errorf("a file of a private room answers %q, a missing one %q", denied.Body, missing.Body)
		}
		if strings.Contains(denied.Body.String(), "secret") {
			t.Error("the answer says what the file is")
		}
	})
	t.Run("a member who left the private room loses the file", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		ids := f.attachmentsOf("alice", "den", [2]string{"a.txt", "x"})
		requireStatus(t, f.call("bob", http.MethodGet, path("/attachments/%d", ids[0]), nil), http.StatusOK)
		if err := f.rooms.Leave(t.Context(), actorOf(f.testEnv, "bob"), f.den.ID); err != nil {
			t.Fatal(err)
		}
		requireStatus(t, f.call("bob", http.MethodGet, path("/attachments/%d", ids[0]), nil), http.StatusNotFound)
	})
	t.Run("the file of a deleted post", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		var ids []string
		ids = append(ids, f.uploadID("bob", "a.txt", "x"))
		post := decode(t, f.call("bob", http.MethodPost, path("/rooms/%d/posts", f.lobby.ID), map[string]any{"body": "p", "upload_ids": ids}))
		att := idOf(t, list(t, post["attachments"])[0])
		requireStatus(t, f.call("carol", http.MethodGet, path("/attachments/%d", att), nil), http.StatusOK)
		requireStatus(t, f.call("bob", http.MethodDelete, path("/posts/%d", uint64(post["id"].(float64))), nil), http.StatusNoContent)
		requireStatus(t, f.call("carol", http.MethodGet, path("/attachments/%d", att), nil), http.StatusNotFound)
	})
	t.Run("IDs that are no attachment", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		for _, id := range []string{"999999", "0", "abc", "-1", "1.5", "18446744073709551616"} {
			rec := f.call("alice", http.MethodGet, "/api/v1/attachments/"+id, nil)
			requireStatus(t, rec, http.StatusNotFound)
			if code := errorCode(t, rec); code != "not_found" {
				t.Errorf("%s: code = %q", id, code)
			}
		}
	})
}

// TestDownloadHeaders checks the headers of a download, which are those of the
// pages' download (SPEC §7.5, D36): files.ServeDownload sets both.
func TestDownloadHeaders(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	notAnImage := "\x89PNG\r\n\x1a\nthis is not a real image"
	ids := f.attachmentsOf("bob", "lobby",
		[2]string{"dot.png", pngBytes(t, 3, 2)},
		[2]string{"notes.txt", "Hello, this is a text file."},
		[2]string{"page.html", "<html><body><script>alert(1)</script></body></html>"},
		[2]string{"fake.png", notAnImage})
	ids = append(ids, f.attachmentsOf("bob", "lobby", [2]string{"a \"b\";é.txt", "x"})...)
	tests := []struct {
		name        string
		id          uint64
		contentType string
		disposition string
	}{
		{"an image is inline with its own type", ids[0], "image/png", `inline; filename="dot.png"`},
		{"a text file is a download of unknown type", ids[1], "application/octet-stream", `attachment; filename="notes.txt"`},
		{"a web page is a download of unknown type", ids[2], "application/octet-stream", `attachment; filename="page.html"`},
		{"a file that claims to be an image is a download", ids[3], "application/octet-stream", `attachment; filename="fake.png"`},
		{"a name that needs care", ids[4], "application/octet-stream", `attachment; filename="a _b___.txt"; filename*=UTF-8''a%20%22b%22%3B%C3%A9.txt`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.call("carol", http.MethodGet, path("/attachments/%d", tt.id), nil)
			requireStatus(t, rec, http.StatusOK)
			for name, want := range map[string]string{
				"Content-Type":            tt.contentType,
				"Content-Disposition":     tt.disposition,
				"X-Content-Type-Options":  "nosniff",
				"Content-Security-Policy": "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; sandbox",
				"Cache-Control":           "private, max-age=86400",
			} {
				if got := rec.Header().Get(name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
			if rec.Header().Get("Last-Modified") == "" {
				t.Error("no Last-Modified")
			}
		})
	}
	t.Run("a range", func(t *testing.T) {
		rec := f.call("carol", http.MethodGet, path("/attachments/%d", ids[1]), nil, header("Range", "bytes=0-4"))
		requireStatus(t, rec, http.StatusPartialContent)
		if rec.Body.String() != "Hello" {
			t.Errorf("body = %q", rec.Body)
		}
	})
	t.Run("a range that is not in the file", func(t *testing.T) {
		rec := f.call("carol", http.MethodGet, path("/attachments/%d", ids[1]), nil, header("Range", "bytes=5000-6000"))
		requireStatus(t, rec, http.StatusRequestedRangeNotSatisfiable)
	})
	t.Run("not modified", func(t *testing.T) {
		first := f.call("carol", http.MethodGet, path("/attachments/%d", ids[1]), nil)
		since := first.Header().Get("Last-Modified")
		rec := f.call("carol", http.MethodGet, path("/attachments/%d", ids[1]), nil, header("If-Modified-Since", since))
		requireStatus(t, rec, http.StatusNotModified)
		f.clk.Advance(time.Hour)
	})
	t.Run("head", func(t *testing.T) {
		// A HEAD is the GET without a body; the document describes the GET.
		rec := f.sendRaw(request(http.MethodHead, path("/attachments/%d", ids[1]), nil, bearer(f.as("carol"))))
		requireStatus(t, rec, http.StatusOK)
		if rec.Body.Len() != 0 {
			t.Errorf("HEAD sent %d bytes", rec.Body.Len())
		}
	})
	t.Run("the same headers as the pages", func(t *testing.T) {
		if files.DownloadCSP != "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; sandbox" || files.DownloadCacheControl != "private, max-age=86400" {
			t.Errorf("the shared constants changed: %q, %q", files.DownloadCSP, files.DownloadCacheControl)
		}
	})
}

// TestUploadRoutesOfTheErrors checks the route of the tags that the upload
// makes in this package.
func TestUploadRoutesOfTheErrors(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	bob := actorOf(f.testEnv, "bob")
	read := func(r *http.Request) error {
		_, err := f.srv.readUpload(httptest.NewRecorder(), r, bob)
		return err
	}
	tests := []struct {
		name  string
		req   *http.Request
		tag   string
		route string
	}{
		{"not multipart", request(http.MethodPost, "/x", map[string]any{}), tagUnsupportedMediaType, "Server.readUpload.UnsupportedMediaType"},
		{"no boundary", func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("x"))
			r.Header.Set("Content-Type", "multipart/form-data")
			return r
		}(), tagUploadInvalid, "Server.readUpload.UploadInvalid"},
		{"no part", multipartRequest(http.MethodPost, "/x", nil), tagUploadInvalid, "Server.readUpload.UploadInvalid"},
		{"the first part is a field", multipartRequest(http.MethodPost, "/x", []formPart{{"note", "", "x"}}), tagUploadInvalid, "Server.readUpload.UploadInvalid"},
		{"two files", multipartRequest(http.MethodPost, "/x", []formPart{{"file", "a", "x"}, {"file", "b", "x"}}), tagUploadInvalid, "Server.readUpload.UploadInvalid"},
		{"the body ends inside the file", func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("--b\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.txt\"\r\n\r\nhalf a fi"))
			r.Header.Set("Content-Type", "multipart/form-data; boundary=b")
			return r
		}(), tagUploadInvalid, "Server.readUpload.UploadInvalid"},
		{"over the limit", multipartRequest(http.MethodPost, "/x", []formPart{{"file", "a", strings.Repeat("x", 1<<20+1)}}), app.TagTooLarge, "Server.readUpload->Posts.UploadStaged->Posts.Upload->Store.Save.FileTooLarge"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := read(tt.req)
			appErr := requireRoute(t, err, tt.tag, tt.route)
			p, internal := problemOf(err)
			if internal || p.status < 400 || p.status >= 500 {
				t.Errorf("problem = %+v, internal %v", p, internal)
			}
			_ = appErr
		})
	}
	t.Run("a body cut at the cap", func(t *testing.T) {
		raw := func(preamble, content string) *http.Request {
			body := preamble + "--b\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.txt\"\r\n\r\n" + content + "\r\n--b--\r\n"
			r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
			r.Header.Set("Content-Type", "multipart/form-data; boundary=b")
			return r
		}
		// In the file: the file is under its limit and the body is over its cap.
		err := read(raw(preambleOf(1500<<10), strings.Repeat("y", 700<<10)))
		_ = requireRoute(t, err, app.TagTooLarge, "Server.readUpload.FileTooLarge")
		// Before the file.
		err = read(raw(preambleOf(3<<20), "small"))
		_ = requireRoute(t, err, tagBodyTooLarge, "Server.readUpload.BodyTooLarge")
	})
}
