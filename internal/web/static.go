package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/rm4n0s/errors"
)

// staticFile is one embedded file, read once when the handler is made.
type staticFile struct {
	data        []byte
	etag        string
	contentType string
}

// staticHandler serves the files of an fs.FS from memory. The ETag of each
// file is the hash of its content, computed once in newStaticHandler. Browsers
// are told to revalidate (Cache-Control: no-cache) and get 304 when the file
// has not changed.
type staticHandler struct {
	prefix   string
	files    map[string]staticFile
	notFound http.Handler
}

// newStaticHandler reads and hashes every file of fsys. Requests whose path
// starts with prefix are answered from the files; a name that is not a file
// goes to notFound.
func newStaticHandler(fsys fs.FS, prefix string, notFound http.Handler) (http.Handler, error) {
	var names []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			names = append(names, p)
		}
		return nil
	})
	if err != nil {
		return nil, errors.NewErr(tagStaticHashFailed, err).SetMetadata("step", "walk")
	}
	files := make(map[string]staticFile, len(names))
	for _, name := range names {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, errors.NewErr(tagStaticHashFailed, err).SetMetadata("step", "read", "file", name)
		}
		sum := sha256.Sum256(data)
		files[name] = staticFile{
			data:        data,
			etag:        `"` + hex.EncodeToString(sum[:16]) + `"`,
			contentType: staticContentType(name),
		}
	}
	return &staticHandler{prefix: prefix, files: files, notFound: notFound}, nil
}

// ServeHTTP answers GET and HEAD requests for one embedded file.
func (h *staticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, h.prefix)
	f, ok := h.files[name]
	if !ok {
		h.notFound.ServeHTTP(w, r)
		return
	}
	hdr := w.Header()
	hdr.Set("ETag", f.etag)
	hdr.Set("Cache-Control", "no-cache")
	if f.contentType != "" {
		hdr.Set("Content-Type", f.contentType)
	}
	// ServeContent answers If-None-Match with 304 by comparing it to the ETag
	// header set above. A zero modification time sends no Last-Modified.
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(f.data))
}

// staticContentType returns the content type of the file types the server
// sends, so the answer does not depend on the system's MIME database. It is
// empty for any other type (ServeContent then looks at the extension).
func staticContentType(name string) string {
	switch path.Ext(name) {
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".woff2":
		return "font/woff2"
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".md":
		return "text/markdown; charset=utf-8"
	default:
		return ""
	}
}

const tagStaticHashFailed = "StaticHashFailed"
