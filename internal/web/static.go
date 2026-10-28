package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/rm4n0s/clowder/internal/web/views"
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
	// version is the hash of the names and the contents of all the files: it
	// changes whenever any of them does. The service worker names its cache
	// after it (SPEC D58).
	version string
}

// newStaticHandler reads and hashes every file of fsys. Requests whose path
// starts with prefix are answered from the files; a name that is not a file
// goes to notFound.
func newStaticHandler(fsys fs.FS, prefix string, notFound http.Handler) (*staticHandler, error) {
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
	return &staticHandler{prefix: prefix, files: files, notFound: notFound, version: staticVersion(files)}, nil
}

// staticVersion is the version of a set of static files: the first 16 bytes of
// the SHA-256 of their names and ETags (the hash of each content), in the order
// of the names, as hex.
func staticVersion(files map[string]staticFile) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	sum := sha256.New()
	for _, name := range names {
		sum.Write([]byte(name))
		sum.Write([]byte{0})
		sum.Write([]byte(files[name].etag))
		sum.Write([]byte{0})
	}
	return hex.EncodeToString(sum.Sum(nil)[:16])
}

// ServeHTTP answers GET and HEAD requests for one embedded file.
func (h *staticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.serve(w, r, strings.TrimPrefix(r.URL.Path, h.prefix))
}

// serve answers with the file name, or with notFound.
func (h *staticHandler) serve(w http.ResponseWriter, r *http.Request, name string) {
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
	case ".webmanifest":
		return "application/manifest+json"
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

// versionPlaceholder is the text of sw.js and of the manifest that the server
// replaces with the version of the static files.
const versionPlaceholder = "__CLOWDER_STATIC_VERSION__"

// newServiceWorkerHandler makes the handler of /sw.js: the source of the worker
// with versionPlaceholder replaced by version, sent as JavaScript that
// browsers revalidate each time (Cache-Control: no-cache, and an ETag), which is
// how a browser finds a new worker. The worker's scope is the path it is served
// from, so the route is at the root: a worker under /static/ would see none of
// the requests of the pages. It fails when the source has no placeholder.
func newServiceWorkerHandler(source []byte, version string) (http.Handler, error) {
	if !bytes.Contains(source, []byte(versionPlaceholder)) {
		return nil, errors.New(tagServiceWorkerInvalid, "the service worker has no place for the version of the static files", "placeholder", versionPlaceholder)
	}
	return versionedFile("sw.js", "text/javascript; charset=utf-8", source, version), nil
}

// newManifestHandler makes the handler of /manifest.webmanifest: the source of
// the manifest with versionPlaceholder replaced by version, so that its icons
// are asked for under the same versioned addresses as the files of the pages
// (SPEC D58). It fails when the source has no placeholder.
func newManifestHandler(source []byte, version string) (http.Handler, error) {
	if !bytes.Contains(source, []byte(versionPlaceholder)) {
		return nil, errors.New(tagManifestInvalid, "the manifest has no place for the version of the static files", "placeholder", versionPlaceholder)
	}
	return versionedFile("manifest.webmanifest", "application/manifest+json", source, version), nil
}

// versionedFile serves source with versionPlaceholder replaced by version as
// contentType, with an ETag and Cache-Control: no-cache.
func versionedFile(name, contentType string, source []byte, version string) http.Handler {
	body := bytes.ReplaceAll(source, []byte(versionPlaceholder), []byte(version))
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr := w.Header()
		hdr.Set("Content-Type", contentType)
		hdr.Set("Cache-Control", "no-cache")
		hdr.Set("ETag", etag)
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(body))
	})
}

// withStaticVersion puts the version of the static files in the context of
// every request, so that the views name the files with it (views.StaticURL),
// in a page and in the patches of a stream.
func withStaticVersion(version string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(views.WithStaticVersion(r.Context(), version)))
	})
}

const (
	tagStaticHashFailed     = "StaticHashFailed"
	tagServiceWorkerInvalid = "ServiceWorkerInvalid"
	tagManifestInvalid      = "ManifestInvalid"
)
