package web

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
)

// filesEnv is a roomsEnv with a post in "general" and one in "secret", each
// with an image, a text file and an HTML file.
type filesEnv struct {
	*roomsEnv
	general, secret app.Post
}

// withFiles makes a post of alice in the room with an image, a text file and a
// page whose names are given.
func (e *roomsEnv) withFiles(room string, names ...string) app.Post {
	e.t.Helper()
	var ups []app.Upload
	contents := map[string][]byte{
		"pic.png":   pngOf(e.t, 8, 6),
		"notes.txt": []byte("Hello, this is a text file."),
		"page.html": []byte("<html><body><script>alert(1)</script></body></html>"),
	}
	for _, name := range names {
		data, ok := contents[name]
		if !ok {
			data = []byte("content of " + name)
		}
		u, err := e.posts.Upload(e.t.Context(), e.actor(e.alice), name, strings.NewReader(string(data)))
		if err != nil {
			e.t.Fatalf("Upload %q: %+v", name, err)
		}
		ups = append(ups, u)
	}
	p, err := e.posts.Create(e.t.Context(), e.actor(e.alice), e.room(room).ID,
		app.PostInput{Format: app.FormatText, Body: "files", Uploads: ups})
	if err != nil {
		e.t.Fatalf("Create: %+v", err)
	}
	return p
}

func newFilesEnv(t *testing.T) *filesEnv {
	t.Helper()
	e := newRoomsEnv(t)
	return &filesEnv{
		roomsEnv: e,
		general:  e.withFiles("general", "pic.png", "notes.txt", "page.html"),
		secret:   e.withFiles("secret", "pic.png", "notes.txt"),
	}
}

// attachmentPath is the path that downloads the n-th attachment of the post.
func attachmentPath(p app.Post, n int) string {
	return "/files/" + idText(p.Attachments[n].ID) + "/" + p.Attachments[n].FileName
}

func TestDownloadHeaders(t *testing.T) {
	e := newFilesEnv(t)
	tests := []struct {
		name        string
		attachment  int
		contentType string
		disposition string
		body        string
	}{
		{"an image is shown inline with its own type", 0, "image/png", `inline; filename="pic.png"`, ""},
		{"a text file is a download of unknown type", 1, "application/octet-stream", `attachment; filename="notes.txt"`, "Hello, this is a text file."},
		{"a web page is a download of unknown type", 2, "application/octet-stream", `attachment; filename="page.html"`, "<html><body><script>alert(1)</script></body></html>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.getAs("alice", attachmentPath(e.general, tt.attachment), false)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			want := map[string]string{
				"Content-Type":            tt.contentType,
				"Content-Disposition":     tt.disposition,
				"X-Content-Type-Options":  "nosniff",
				"Content-Security-Policy": "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; sandbox",
				"Cache-Control":           "private, max-age=86400",
				"Last-Modified":           "Mon, 26 Oct 2026 09:30:00 GMT",
				"Accept-Ranges":           "bytes",
			}
			for k, v := range want {
				if got := rec.Header().Get(k); got != v {
					t.Errorf("%s = %q, want %q", k, got, v)
				}
			}
			if tt.body != "" && rec.Body.String() != tt.body {
				t.Errorf("body = %q, want %q", rec.Body.String(), tt.body)
			}
			if got, want := rec.Body.Len(), int(e.general.Attachments[tt.attachment].Size); got != want {
				t.Errorf("body is %d bytes, want %d", got, want)
			}
			if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(rec.Body.Len()) {
				t.Errorf("Content-Length = %q", got)
			}
		})
	}
}

func TestDownloadStoredTypeOfEachInlineImage(t *testing.T) {
	// The inline types keep their own type; a file that merely has an image
	// name but other content is a download.
	e := newRoomsEnv(t)
	gif := []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;")
	tests := []struct {
		name        string
		data        []byte
		contentType string
		disposition string
	}{
		{"photo.gif", gif, "image/gif", `inline; filename="photo.gif"`},
		{"fake.png", []byte("<html>not an image</html>"), "application/octet-stream", `attachment; filename="fake.png"`},
		{"pic.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), "application/octet-stream", `attachment; filename="pic.svg"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := e.posts.Upload(t.Context(), e.actor(e.alice), tt.name, strings.NewReader(string(tt.data)))
			if err != nil {
				t.Fatal(err)
			}
			p, err := e.posts.Create(t.Context(), e.actor(e.alice), e.general.ID, app.PostInput{Format: app.FormatText, Body: "x", Uploads: []app.Upload{u}})
			if err != nil {
				t.Fatal(err)
			}
			rec := e.getAs("bob", attachmentPath(p, 0), false)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != tt.contentType {
				t.Errorf("Content-Type = %q, want %q", got, tt.contentType)
			}
			if got := rec.Header().Get("Content-Disposition"); got != tt.disposition {
				t.Errorf("Content-Disposition = %q, want %q", got, tt.disposition)
			}
			if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Errorf("nosniff is missing")
			}
		})
	}
}

func TestDownloadFileNames(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"plain.txt", `attachment; filename="plain.txt"`},
		{"two words.txt", `attachment; filename="two words.txt"`},
		{`résumé "v2".txt`, `attachment; filename="r_sum_ _v2_.txt"; filename*=UTF-8''r%C3%A9sum%C3%A9%20%22v2%22.txt`},
		{"日本語.txt", `attachment; filename="___.txt"; filename*=UTF-8''%E6%97%A5%E6%9C%AC%E8%AA%9E.txt`},
		{"50%;off.txt", `attachment; filename="50__off.txt"; filename*=UTF-8''50%25%3Boff.txt`},
		{"it's (1).txt", `attachment; filename="it's (1).txt"`},
	}
	e := newRoomsEnv(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := e.withFiles("general", tt.name)
			// The name in the path is cosmetic: any name finds the file.
			rec := e.getAs("bob", "/files/"+idText(p.Attachments[0].ID)+"/whatever", false)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			if got := rec.Header().Get("Content-Disposition"); got != tt.want {
				t.Errorf("Content-Disposition = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDownloadRange(t *testing.T) {
	e := newFilesEnv(t)
	path := attachmentPath(e.general, 1)
	t.Run("a range", func(t *testing.T) {
		rec := e.send(get(path, e.cookies["alice"], withHeader("Range", "bytes=0-4")))
		if rec.Code != http.StatusPartialContent || rec.Body.String() != "Hello" {
			t.Fatalf("answer = %d %q", rec.Code, rec.Body)
		}
		if got := rec.Header().Get("Content-Range"); got != "bytes 0-4/27" {
			t.Errorf("Content-Range = %q", got)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Content-Disposition") == "" {
			t.Errorf("headers of a range answer: %v", rec.Header())
		}
	})
	t.Run("the end of the file", func(t *testing.T) {
		rec := e.send(get(path, e.cookies["alice"], withHeader("Range", "bytes=-5")))
		if rec.Code != http.StatusPartialContent || rec.Body.String() != "file." {
			t.Fatalf("answer = %d %q", rec.Code, rec.Body)
		}
	})
	t.Run("a range beyond the file", func(t *testing.T) {
		rec := e.send(get(path, e.cookies["alice"], withHeader("Range", "bytes=100-200")))
		if rec.Code != http.StatusRequestedRangeNotSatisfiable {
			t.Fatalf("status = %d, want 416", rec.Code)
		}
	})
	t.Run("not modified", func(t *testing.T) {
		rec := e.send(get(path, e.cookies["alice"], withHeader("If-Modified-Since", "Mon, 26 Oct 2026 09:30:00 GMT")))
		if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
			t.Fatalf("answer = %d %q", rec.Code, rec.Body)
		}
	})
	t.Run("modified since", func(t *testing.T) {
		rec := e.send(get(path, e.cookies["alice"], withHeader("If-Modified-Since", "Sun, 25 Oct 2026 09:30:00 GMT")))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	})
	t.Run("a HEAD request", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodHead, path, nil)
		e.cookies["alice"](r)
		rec := e.send(r)
		if rec.Code != http.StatusOK || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "27" {
			t.Fatalf("answer = %d %q %v", rec.Code, rec.Body, rec.Header())
		}
	})
}

func TestDownloadPermissions(t *testing.T) {
	e := newFilesEnv(t)
	tests := []struct {
		user string
		post app.Post
		want int
	}{
		// A visible room: any logged-in person reads it, member or not.
		{"alice", e.general, http.StatusOK},
		{"bob", e.general, http.StatusOK},
		{"carol", e.general, http.StatusOK},
		{"admin", e.general, http.StatusOK},
		// A private room: its members only. Not even an admin; and never 403.
		{"alice", e.secret, http.StatusOK},
		{"bob", e.secret, http.StatusOK},
		{"carol", e.secret, http.StatusNotFound},
		{"admin", e.secret, http.StatusNotFound},
	}
	for _, tt := range tests {
		for i := range tt.post.Attachments {
			t.Run(tt.user+" "+strconv.FormatUint(tt.post.RoomID, 10)+" "+tt.post.Attachments[i].FileName, func(t *testing.T) {
				rec := e.getAs(tt.user, attachmentPath(tt.post, i), false)
				if rec.Code != tt.want {
					t.Fatalf("status = %d, want %d", rec.Code, tt.want)
				}
				if tt.want == http.StatusNotFound {
					requireBody(t, rec, []string{"Page not found"}, "notes", "Hello")
					for _, h := range []string{"Content-Disposition", "Last-Modified"} {
						if rec.Header().Get(h) != "" {
							t.Errorf("the 404 sets %s", h)
						}
					}
				}
			})
		}
	}
	t.Run("a person who leaves the private room loses the files", func(t *testing.T) {
		if err := e.rooms.Leave(t.Context(), e.actor(e.bob), e.secret.RoomID); err != nil {
			t.Fatal(err)
		}
		if rec := e.getAs("bob", attachmentPath(e.secret, 1), false); rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})
}

func TestDownloadNotFound(t *testing.T) {
	e := newFilesEnv(t)
	t.Run("a deleted post", func(t *testing.T) {
		gone := e.withFiles("general", "gone.txt")
		path := attachmentPath(gone, 0)
		if rec := e.getAs("bob", path, false); rec.Code != http.StatusOK {
			t.Fatalf("before the delete: status = %d", rec.Code)
		}
		if err := e.posts.Delete(t.Context(), e.actor(e.alice), gone.ID); err != nil {
			t.Fatal(err)
		}
		if rec := e.getAs("bob", path, false); rec.Code != http.StatusNotFound {
			t.Errorf("after the delete: status = %d, want 404", rec.Code)
		}
	})
	t.Run("a deleted post that stays for its replies", func(t *testing.T) {
		kept := e.withFiles("general", "kept.txt")
		path := attachmentPath(kept, 0)
		e.commentOn(kept, "bob", "a reply")
		if err := e.posts.Delete(t.Context(), e.actor(e.alice), kept.ID); err != nil {
			t.Fatal(err)
		}
		if rec := e.getAs("bob", path, false); rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})
	t.Run("a file removed by an edit", func(t *testing.T) {
		p := e.withFiles("general", "edited.txt")
		path := attachmentPath(p, 0)
		if _, err := e.posts.Edit(t.Context(), e.actor(e.alice), p.ID, app.PostInput{Format: app.FormatText, Body: "files"}); err != nil {
			t.Fatal(err)
		}
		if rec := e.getAs("bob", path, false); rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})
	for _, path := range []string{"/files/99999/a.txt", "/files/0/a.txt", "/files/abc/a.txt", "/files/-1/a.txt", "/files/1.5/a.txt", "/files/18446744073709551616/a.txt"} {
		t.Run(path, func(t *testing.T) {
			rec := e.getAs("alice", path, false)
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", rec.Code)
			}
			requireBody(t, rec, []string{"Page not found"})
		})
	}
	t.Run("no name or an empty one", func(t *testing.T) {
		for _, path := range []string{"/files/" + idText(e.general.Attachments[0].ID), "/files/" + idText(e.general.Attachments[0].ID) + "/"} {
			if rec := e.getAs("alice", path, false); rec.Code != http.StatusNotFound {
				t.Errorf("%s: status = %d, want 404", path, rec.Code)
			}
		}
	})
	t.Run("a file missing from the disk is a server error", func(t *testing.T) {
		p := e.withFiles("general", "lost.txt")
		for _, k := range e.onDisk() {
			if err := removeFile(e.filesDir + "/" + k); err != nil {
				t.Fatal(err)
			}
		}
		rec := e.getAs("bob", attachmentPath(p, 0), false)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
		requireBody(t, rec, nil, "lost.txt", e.filesDir)
	})
}

func TestDownloadOnlyGet(t *testing.T) {
	e := newFilesEnv(t)
	rec := e.send(postForm(attachmentPath(e.general, 0), nil, e.cookies["alice"]))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Code)
	}
}
