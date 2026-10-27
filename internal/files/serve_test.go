package files_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/files"
)

func TestServeDownload(t *testing.T) {
	t.Parallel()
	modified := time.Date(2026, 10, 26, 9, 30, 0, 0, time.UTC)
	tests := []struct {
		name        string
		download    files.Download
		contentType string
		disposition string
	}{
		{"an inline image", files.Download{FileName: "cat.png", ContentType: "image/png", Inline: true, ModTime: modified},
			"image/png", `inline; filename="cat.png"`},
		{"a text file", files.Download{FileName: "notes.txt", ContentType: "text/plain; charset=utf-8", ModTime: modified},
			"application/octet-stream", `attachment; filename="notes.txt"`},
		{"a page is never served as one", files.Download{FileName: "page.html", ContentType: "text/html; charset=utf-8", ModTime: modified},
			"application/octet-stream", `attachment; filename="page.html"`},
		{"a name that needs care", files.Download{FileName: "a \"b\";é.txt", ContentType: "text/plain", ModTime: modified},
			"application/octet-stream", `attachment; filename="a _b___.txt"; filename*=UTF-8''a%20%22b%22%3B%C3%A9.txt`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			files.ServeDownload(rec, httptest.NewRequest(http.MethodGet, "/x", nil), tt.download, strings.NewReader("content"))
			if rec.Code != http.StatusOK || rec.Body.String() != "content" {
				t.Fatalf("answer = %d %q", rec.Code, rec.Body)
			}
			for name, want := range map[string]string{
				"Content-Type":            tt.contentType,
				"Content-Disposition":     tt.disposition,
				"X-Content-Type-Options":  "nosniff",
				"Content-Security-Policy": files.DownloadCSP,
				"Cache-Control":           "private, max-age=86400",
				"Last-Modified":           modified.Format(http.TimeFormat),
			} {
				if got := rec.Header().Get(name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
		})
	}
	t.Run("a range", func(t *testing.T) {
		t.Parallel()
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		r.Header.Set("Range", "bytes=1-3")
		files.ServeDownload(rec, r, files.Download{FileName: "a.txt", ModTime: modified}, strings.NewReader("content"))
		if rec.Code != http.StatusPartialContent || rec.Body.String() != "ont" {
			t.Errorf("answer = %d %q, want 206 ont", rec.Code, rec.Body)
		}
	})
}
