package web

import (
	"bytes"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/config"
)

// upload is a file part of a multipart form.
type upload struct {
	// name is the name of the form field; "" means "files".
	name string
	// filename is the file name of the part.
	filename string
	data     []byte
}

// pngOf is a PNG of w by h pixels.
func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// multipartBody makes the body of a multipart form with the text fields (in
// the order of the keys, then of the values) and the files.
func multipartBody(t *testing.T, fields url.Values, files []upload) (body *bytes.Buffer, contentType string) {
	t.Helper()
	body = &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		for _, v := range fields[k] {
			if err := mw.WriteField(k, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, f := range files {
		name := f.name
		if name == "" {
			name = "files"
		}
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="`+name+`"; filename="`+f.filename+`"`)
		h.Set("Content-Type", "application/octet-stream")
		pw, err := mw.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pw.Write(f.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return body, mw.FormDataContentType()
}

// postFiles sends a multipart POST as the user: the way the editor sends its
// form. The browser's empty part for a file input with no file is not added.
func (e *roomsEnv) postFiles(user, path string, fields url.Values, files []upload, datastar bool) *httptest.ResponseRecorder {
	e.t.Helper()
	body, ct := multipartBody(e.t, fields, files)
	return e.sendMultipart(user, path, body, ct, datastar)
}

// sendMultipart sends the body as a multipart POST as the user.
func (e *roomsEnv) sendMultipart(user, path string, body io.Reader, contentType string, datastar bool) *httptest.ResponseRecorder {
	e.t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, body)
	r.Header.Set("Content-Type", contentType)
	e.cookies[user](r)
	if datastar {
		asDatastar()(r)
	}
	return e.send(r)
}

// onDisk returns the keys of the files of the attachments on disk.
func (e *roomsEnv) onDisk() []string {
	e.t.Helper()
	entries, err := os.ReadDir(e.filesDir)
	if err != nil {
		e.t.Fatalf("ReadDir: %v", err)
	}
	keys := []string{}
	for _, en := range entries {
		if en.Name() != "tmp" {
			keys = append(keys, en.Name())
		}
	}
	return keys
}

// tmpFiles returns the names in the temporary directory of the files.
func (e *roomsEnv) tmpFiles() []string {
	e.t.Helper()
	entries, err := os.ReadDir(e.filesDir + "/tmp")
	if err != nil {
		e.t.Fatalf("ReadDir: %v", err)
	}
	var names []string
	for _, en := range entries {
		names = append(names, en.Name())
	}
	return names
}

// attachmentNames returns the file names of the attachments of the post.
func attachmentNamesOf(p app.Post) []string {
	names := []string{}
	for _, a := range p.Attachments {
		names = append(names, a.FileName)
	}
	return names
}

// smallFiles is the configuration of the tests of the limits: files of at most
// 1 KB, two to a post.
func smallFiles(c *config.Config) {
	c.MaxAttachmentSize = 1024
	c.MaxAttachmentsPerPost = 2
}

// getPost reads the post as the user.
func (e *roomsEnv) getPost(user string, id uint64) app.Post {
	e.t.Helper()
	p, err := e.posts.Get(e.t.Context(), e.actor(e.users(user)), id)
	if err != nil {
		e.t.Fatalf("Get: %+v", err)
	}
	return p
}

func TestUploadCreateWithFiles(t *testing.T) {
	img := pngOf(t, 40, 30)
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		rec := e.postFiles("alice", "/rooms/"+idText(e.general.ID)+"/posts",
			editorForm("text", "See the files", "design"),
			[]upload{{filename: "mock up.png", data: img}, {filename: "notes.txt", data: []byte("some notes")}}, datastar)
		posts := e.feed("alice", "general")
		if len(posts) != 1 {
			t.Fatalf("%d posts, want 1; answer %d:\n%s", len(posts), rec.Code, rec.Body)
		}
		p := posts[0]
		if got := attachmentNamesOf(p); !slices.Equal(got, []string{"mock up.png", "notes.txt"}) {
			t.Errorf("attachments = %v", got)
		}
		if p.Body != "See the files" || strings.Join(p.Tags, ",") != "design" {
			t.Errorf("post = %+v", p)
		}
		a := p.Attachments[0]
		if !a.IsImage || a.Width != 40 || a.Height != 30 || a.ContentType != "image/png" {
			t.Errorf("image attachment = %+v", a)
		}
		if got := len(e.onDisk()); got != 2 {
			t.Errorf("%d files on disk, want 2", got)
		}
		if !datastar {
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/rooms/"+idText(e.general.ID) {
				t.Fatalf("answer = %d %q, want 303 to the room", rec.Code, rec.Header().Get("Location"))
			}
			return
		}
		requireSSE(t, rec)
		requireBody(t, rec, []string{
			"data: selector #feed-posts", "data: mode prepend",
			`<img src="/files/` + idText(a.ID) + `/mock%20up.png" alt="mock up.png" loading="lazy" width="40" height="30">`,
			`<a href="/files/` + idText(p.Attachments[1].ID) + `/notes.txt">notes.txt</a>`, "10 B",
			// The editor is fresh again, with no files in it.
			"data: selector #post-editor-new", `id="post-editor-new"`,
		})
	})
}

func TestUploadBodyMayBeEmptyWithFiles(t *testing.T) {
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		rec := e.postFiles("alice", "/rooms/"+idText(e.general.ID)+"/posts",
			editorForm("text", "  ", ""), []upload{{filename: "a.txt", data: []byte("a")}}, datastar)
		posts := e.feed("alice", "general")
		if len(posts) != 1 || posts[0].Body != "" || len(posts[0].Attachments) != 1 {
			t.Fatalf("posts = %+v; answer %d:\n%s", posts, rec.Code, rec.Body)
		}
		// Without files the same body is a mistake.
		rec = e.postFiles("alice", "/rooms/"+idText(e.general.ID)+"/posts", editorForm("text", "  ", ""), nil, datastar)
		requireBody(t, rec, []string{"Write something."})
	})
}

func TestUploadIgnoresTheEmptyFilePartOfABrowser(t *testing.T) {
	e := newRoomsEnv(t)
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	for k, v := range map[string]string{"format": "text", "body": "no file chosen", "tags": ""} {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	// What a browser sends for a file input with no file.
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="files"; filename=""`)
	h.Set("Content-Type", "application/octet-stream")
	if _, err := mw.CreatePart(h); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	rec := e.sendMultipart("alice", "/rooms/"+idText(e.general.ID)+"/posts", body, mw.FormDataContentType(), false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d:\n%s", rec.Code, rec.Body)
	}
	posts := e.feed("alice", "general")
	if len(posts) != 1 || len(posts[0].Attachments) != 0 || len(e.onDisk()) != 0 {
		t.Errorf("posts = %+v, files on disk %v", posts, e.onDisk())
	}
}

func TestUploadReplyWithFiles(t *testing.T) {
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		p := e.writePost("alice", "general", "root")
		rec := e.postFiles("bob", postPathOf(p, "replies"), editorForm("text", "with a file", ""),
			[]upload{{filename: "reply.txt", data: []byte("reply")}}, datastar)
		page, err := e.posts.Thread(t.Context(), e.actor(e.bob), p.ID)
		if err != nil || len(page.Comments) != 1 {
			t.Fatalf("thread = %+v, %v; answer %d:\n%s", page, err, rec.Code, rec.Body)
		}
		if got := attachmentNamesOf(page.Comments[0].Post); !slices.Equal(got, []string{"reply.txt"}) {
			t.Errorf("comment attachments = %v", got)
		}
		if datastar {
			requireSSE(t, rec)
			requireBody(t, rec, []string{`>reply.txt</a>`})
		} else if rec.Code != http.StatusSeeOther {
			t.Errorf("status = %d, want 303", rec.Code)
		}
	})
}

func TestUploadEditKeepsAndRemovesFiles(t *testing.T) {
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		ctx := t.Context()
		var ups []app.Upload
		for _, name := range []string{"one.txt", "two.txt", "three.txt"} {
			u, err := e.posts.Upload(ctx, e.actor(e.alice), name, strings.NewReader("content of "+name))
			if err != nil {
				t.Fatal(err)
			}
			ups = append(ups, u)
		}
		p, err := e.posts.Create(ctx, e.actor(e.alice), e.general.ID, app.PostInput{Format: app.FormatText, Body: "post", Uploads: ups})
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, len(p.Attachments))
		for i, a := range p.Attachments {
			ids[i] = idText(a.ID)
		}

		// The editor lists them: a keep and a Remove box each.
		rec := e.getAs("alice", postPathOf(p, "edit"), true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{
			`enctype="multipart/form-data"`, `<ul class="attachment-edit"`,
			`name="keep" value="` + ids[0] + `"`, `name="remove" value="` + ids[0] + `"`,
			`name="keep" value="` + ids[2] + `"`, "three.txt",
		})

		// Save: keep all three, remove the middle one, add a file.
		form := editorForm("text", "post", "")
		form["keep"] = ids
		form["remove"] = []string{ids[1]}
		rec = e.postFiles("alice", postPathOf(p, "edit"), form, []upload{{filename: "four.txt", data: []byte("four")}}, datastar)
		got := e.getPost("alice", p.ID)
		if names := attachmentNamesOf(got); !slices.Equal(names, []string{"one.txt", "three.txt", "four.txt"}) {
			t.Fatalf("attachments after the edit = %v; answer %d:\n%s", names, rec.Code, rec.Body)
		}
		if n := len(e.onDisk()); n != 3 {
			t.Errorf("%d files on disk, want 3", n)
		}
		if datastar {
			requireSSE(t, rec)
			requireBody(t, rec, []string{"four.txt", "one.txt"}, "two.txt")
		} else if rec.Code != http.StatusSeeOther {
			t.Errorf("status = %d, want 303", rec.Code)
		}
	})
}

func TestUploadEditWithNoKeepRemovesTheFiles(t *testing.T) {
	e := newRoomsEnv(t)
	u, err := e.posts.Upload(t.Context(), e.actor(e.alice), "a.txt", strings.NewReader("a"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.posts.Create(t.Context(), e.actor(e.alice), e.general.ID, app.PostInput{Format: app.FormatText, Body: "post", Uploads: []app.Upload{u}})
	if err != nil {
		t.Fatal(err)
	}
	rec := e.postFiles("alice", postPathOf(p, "edit"), editorForm("text", "post", ""), nil, true)
	requireSSE(t, rec)
	if got := e.getPost("alice", p.ID); len(got.Attachments) != 0 || len(e.onDisk()) != 0 {
		t.Errorf("attachments %v, files on disk %v", attachmentNamesOf(got), e.onDisk())
	}
}

func TestUploadEditProblemsKeepTheEditor(t *testing.T) {
	e := newRoomsEnvWith(t, smallFiles)
	u, err := e.posts.Upload(t.Context(), e.actor(e.alice), "a.txt", strings.NewReader("a"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.posts.Create(t.Context(), e.actor(e.alice), e.general.ID, app.PostInput{Format: app.FormatText, Body: "post", Uploads: []app.Upload{u}})
	if err != nil {
		t.Fatal(err)
	}
	id := idText(p.Attachments[0].ID)

	t.Run("a file that is too large", func(t *testing.T) {
		form := editorForm("text", "typed body", "kept")
		form["keep"] = []string{id}
		form["remove"] = []string{id}
		rec := e.postFiles("alice", postPathOf(p, "edit"), form, []upload{{filename: "big.bin", data: make([]byte, 2000)}}, true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{
			"Each file can be at most 1 KB.", ">typed body</textarea>", `value="kept"`, "a.txt",
			// The box that was ticked stays ticked.
			`name="remove" value="` + id + `" checked`,
		})
		if got := e.getPost("alice", p.ID); len(got.Attachments) != 1 || len(e.onDisk()) != 1 {
			t.Errorf("attachments %v, files on disk %v", attachmentNamesOf(got), e.onDisk())
		}
	})
	t.Run("a keep that is not an attachment of the post", func(t *testing.T) {
		form := editorForm("text", "typed", "")
		form["keep"] = []string{id, "99999", "abc"}
		rec := e.postFiles("alice", postPathOf(p, "edit"), form, []upload{{filename: "b.txt", data: []byte("b")}}, true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{"Upload the files again.", `id="post-files-edit-` + idText(p.ID) + `-error"`})
		if got := e.getPost("alice", p.ID); len(got.Attachments) != 1 || len(e.onDisk()) != 1 {
			t.Errorf("attachments %v, files on disk %v", attachmentNamesOf(got), e.onDisk())
		}
	})
	t.Run("more files than the limit with the kept one", func(t *testing.T) {
		form := editorForm("text", "typed", "")
		form["keep"] = []string{id}
		rec := e.postFiles("alice", postPathOf(p, "edit"), form,
			[]upload{{filename: "b.txt", data: []byte("b")}, {filename: "c.txt", data: []byte("c")}}, false)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", rec.Code)
		}
		requireBody(t, rec, []string{"Attach at most 2 files."})
		if len(e.onDisk()) != 1 {
			t.Errorf("files on disk %v", e.onDisk())
		}
	})
}

func TestUploadTooLarge(t *testing.T) {
	big := make([]byte, 5000)
	tests := []struct {
		name string
		// path makes the path of the request.
		path func(e *roomsEnv) string
		user string
	}{
		{"create", func(e *roomsEnv) string { return "/rooms/" + idText(e.general.ID) + "/posts" }, "alice"},
		{"reply", func(e *roomsEnv) string {
			return postPathOf(e.writePost("bob", "general", "root"), "replies")
		}, "alice"},
		{"edit", func(e *roomsEnv) string {
			return postPathOf(e.writePost("alice", "general", "root"), "edit")
		}, "alice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modes(t, func(t *testing.T, datastar bool) {
				e := newRoomsEnvWith(t, smallFiles)
				path := tt.path(e)
				before := len(e.feed("alice", "general"))
				form := editorForm("text", "my careful text", "tag-one")
				// A fine file first, then the large one, then the text after them
				// is still read.
				rec := e.postFiles(tt.user, path, form, []upload{
					{filename: "ok.txt", data: []byte("fine")}, {filename: "big.bin", data: big},
				}, datastar)
				want := []string{"Each file can be at most 1 KB.", `id="post-files-`, "my careful text", `value="tag-one"`}
				if datastar {
					requireSSE(t, rec)
					requireBody(t, rec, want)
				} else {
					if rec.Code != http.StatusRequestEntityTooLarge {
						t.Fatalf("status = %d, want 413", rec.Code)
					}
					requireBody(t, rec, want)
				}
				if got := e.onDisk(); len(got) != 0 {
					t.Errorf("files left on disk: %v", got)
				}
				if names := e.tmpFiles(); len(names) != 0 {
					t.Errorf("temporary files left: %v", names)
				}
				if tt.name == "create" {
					if got := len(e.feed("alice", "general")); got != before {
						t.Errorf("%d posts, want %d", got, before)
					}
				}
			})
		})
	}
}

func TestUploadTooManyFiles(t *testing.T) {
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnvWith(t, smallFiles)
		rec := e.postFiles("alice", "/rooms/"+idText(e.general.ID)+"/posts", editorForm("text", "kept words", "t"), []upload{
			{filename: "1.txt", data: []byte("1")}, {filename: "2.txt", data: []byte("2")}, {filename: "3.txt", data: []byte("3")},
		}, datastar)
		want := []string{"Attach at most 2 files.", "kept words", `id="post-files-new-error"`}
		if datastar {
			requireSSE(t, rec)
		} else if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", rec.Code)
		}
		requireBody(t, rec, want)
		if posts := e.feed("alice", "general"); len(posts) != 0 {
			t.Errorf("a post was written: %+v", posts)
		}
		if got := e.onDisk(); len(got) != 0 {
			t.Errorf("files left on disk: %v", got)
		}
	})
}

func TestUploadRefusedCallsLeaveNoFiles(t *testing.T) {
	tests := []struct {
		name       string
		user       string
		path       func(e *roomsEnv) string
		form       url.Values
		wantStatus int
	}{
		{"a person who has not joined", "carol", func(e *roomsEnv) string { return "/rooms/" + idText(e.general.ID) + "/posts" },
			editorForm("text", "x", ""), http.StatusForbidden},
		{"a mistake in the tags", "alice", func(e *roomsEnv) string { return "/rooms/" + idText(e.general.ID) + "/posts" },
			editorForm("text", "x", "!bad"), http.StatusUnprocessableEntity},
		{"a private room", "carol", func(e *roomsEnv) string { return "/rooms/" + idText(e.secret.ID) + "/posts" },
			editorForm("text", "x", ""), http.StatusNotFound},
		{"someone else's post", "bob", func(e *roomsEnv) string { return postPathOf(e.writePost("alice", "general", "mine"), "edit") },
			editorForm("text", "x", ""), http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newRoomsEnv(t)
			rec := e.postFiles(tt.user, tt.path(e), tt.form, []upload{{filename: "a.txt", data: []byte("a")}}, false)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := e.onDisk(); len(got) != 0 {
				t.Errorf("files left on disk: %v", got)
			}
		})
	}
}

func TestUploadRequestIsCapped(t *testing.T) {
	e := newRoomsEnvWith(t, smallFiles)
	// The cap is the files of a post at their largest and a megabyte.
	wantCap := int64(2*1024 + 1<<20)
	if got := e.srv.requestBodyLimit(); got != wantCap {
		t.Fatalf("requestBodyLimit = %d, want %d", got, wantCap)
	}
	body, ct := multipartBody(t, editorForm("text", "words", ""), []upload{{filename: "huge.bin", data: make([]byte, 4<<20)}})
	counted := &countingReader{r: body}
	rec := e.sendMultipart("alice", "/rooms/"+idText(e.general.ID)+"/posts", counted, ct, false)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	requireBody(t, rec, []string{"Each file can be at most 1 KB.", "words"})
	if counted.n > wantCap+4096 {
		t.Errorf("the handler read %d bytes of a body capped at %d", counted.n, wantCap)
	}
	if got := e.onDisk(); len(got) != 0 {
		t.Errorf("files left on disk: %v", got)
	}
}

// countingReader counts the bytes read from it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func TestUploadUnreadableForms(t *testing.T) {
	e := newRoomsEnv(t)
	path := "/rooms/" + idText(e.general.ID) + "/posts"
	huge := strings.Repeat("a", maxPostFormBytes+1)
	tests := []struct {
		name        string
		body        func() (io.Reader, string)
		wantStatus  int
		wantPosts   int
		wantMessage string
	}{
		{"a text field over the limit", func() (io.Reader, string) {
			b, ct := multipartBody(t, editorForm("text", huge, ""), nil)
			return b, ct
		}, http.StatusBadRequest, 0, "That request could not be read"},
		{"text fields that together are over the limit", func() (io.Reader, string) {
			half := strings.Repeat("a", maxPostFormBytes/2+1)
			b, ct := multipartBody(t, url.Values{"body": {half}, "tags": {half}, "format": {"text"}}, nil)
			return b, ct
		}, http.StatusBadRequest, 0, "That request could not be read"},
		{"a body that stops in the middle", func() (io.Reader, string) {
			b, ct := multipartBody(t, editorForm("text", "words", ""), []upload{{filename: "a.txt", data: []byte("abc")}})
			return bytes.NewReader(b.Bytes()[:b.Len()/2]), ct
		}, http.StatusBadRequest, 0, "That request could not be read"},
		{"a multipart form with no boundary", func() (io.Reader, string) {
			return strings.NewReader("x"), "multipart/form-data"
		}, http.StatusBadRequest, 0, "That request could not be read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, ct := tt.body()
			rec := e.sendMultipart("alice", path, body, ct, false)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			requireBody(t, rec, []string{tt.wantMessage})
			if got := len(e.feed("alice", "general")); got != tt.wantPosts {
				t.Errorf("%d posts, want %d", got, tt.wantPosts)
			}
			if got := e.onDisk(); len(got) != 0 {
				t.Errorf("files left on disk: %v", got)
			}
		})
	}
}

func TestUploadOtherFieldsNamedFilesAreText(t *testing.T) {
	// Only the field "files" is a file; a file part with another name is read as
	// text, so that it cannot be saved without the limits of the editor.
	e := newRoomsEnv(t)
	rec := e.postFiles("alice", "/rooms/"+idText(e.general.ID)+"/posts", editorForm("text", "words", ""),
		[]upload{{name: "other", filename: "a.txt", data: []byte("a")}}, false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := e.onDisk(); len(got) != 0 {
		t.Errorf("files on disk: %v", got)
	}
}

func TestUploadPreviewIgnoresFiles(t *testing.T) {
	e := newRoomsEnvWith(t, smallFiles)
	// The preview of a form that carries a file the size of the limit, a file
	// over it and too many files: the files are dropped, not saved.
	form := url.Values{"editor": {"new"}, "format": {"markdown"}, "body": {"Hello **there**"}}
	rec := e.postFiles("alice", "/preview", form, []upload{
		{filename: "a.txt", data: []byte("a")}, {filename: "b.txt", data: make([]byte, 5000)}, {filename: "c.txt", data: []byte("c")},
	}, true)
	requireSSE(t, rec)
	requireBody(t, rec, []string{"<strong>there</strong>", `id="post-preview-new"`})
	if got := e.onDisk(); len(got) != 0 {
		t.Errorf("files on disk after a preview: %v", got)
	}
	// Without files the preview is a multipart form of text only.
	rec = e.postFiles("alice", "/preview", form, nil, true)
	requireBody(t, rec, []string{"<strong>there</strong>"})
}

func TestUploadEditorsCarryTheFileInput(t *testing.T) {
	e := newRoomsEnv(t)
	p := e.writePost("alice", "general", "root")
	for _, tt := range []struct{ name, path, user string }{
		{"the room page", "/rooms/" + idText(e.general.ID), "alice"},
		{"the reply slot", postPathOf(p, "reply"), "bob"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.getAs(tt.user, tt.path, tt.name == "the reply slot")
			requireBody(t, rec, []string{`enctype="multipart/form-data"`, `name="files" type="file" multiple`})
		})
	}
}

// removeFile removes the file at the path.
func removeFile(path string) error {
	return os.Remove(path)
}
