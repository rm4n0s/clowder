package web

import (
	"bytes"
	"encoding/json"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/rm4n0s/clowder/internal/web/static"
	"github.com/rm4n0s/errors"
)

// manifest is the part of the web app manifest the tests read.
type manifest struct {
	Name            string `json:"name"`
	ShortName       string `json:"short_name"`
	Description     string `json:"description"`
	StartURL        string `json:"start_url"`
	Scope           string `json:"scope"`
	Display         string `json:"display"`
	BackgroundColor string `json:"background_color"`
	ThemeColor      string `json:"theme_color"`
	Icons           []struct {
		Src     string `json:"src"`
		Sizes   string `json:"sizes"`
		Type    string `json:"type"`
		Purpose string `json:"purpose"`
	} `json:"icons"`
}

// TestManifest: /manifest.webmanifest is served at the root for a visitor
// without a login, as application/manifest+json with the headers of D36, it is
// valid JSON with the fields of D58, and every icon it names is served as a PNG
// of the size it says, for each purpose.
func TestManifest(t *testing.T) {
	t.Parallel()
	s, _ := newTestServer(t)
	h := s.Handler()

	rec := do(h, httptest.NewRequest(http.MethodGet, "/manifest.webmanifest", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/manifest+json" {
		t.Errorf("Content-Type = %q, want application/manifest+json", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != pageCSP {
		t.Errorf("Content-Security-Policy = %q, want the page policy", got)
	}
	var m manifest
	dec := json.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("the manifest is not valid JSON of the expected shape: %v\n%s", err, rec.Body.String())
	}
	fields := []struct {
		name, got, want string
	}{
		{"name", m.Name, "Clowder"},
		{"short_name", m.ShortName, "Clowder"},
		{"start_url", m.StartURL, "/"},
		{"scope", m.Scope, "/"},
		{"display", m.Display, "standalone"},
		{"background_color", m.BackgroundColor, "#fbf6ee"},
		{"theme_color", m.ThemeColor, "#c2410c"},
	}
	for _, f := range fields {
		if f.got != f.want {
			t.Errorf("%s = %q, want %q", f.name, f.got, f.want)
		}
	}

	// The icons are named with the version of the static files, as the pages name
	// the files they use.
	files, err := newStaticHandler(static.FS, staticPrefix, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rec.Body.String(), versionPlaceholder) {
		t.Errorf("the placeholder %s is still in the manifest", versionPlaceholder)
	}
	for _, ic := range m.Icons {
		if !strings.HasSuffix(ic.Src, "?v="+files.version) {
			t.Errorf("the icon %q does not end with ?v=%s", ic.Src, files.version)
		}
	}

	wantIcons := map[string]bool{"192x192/any": true, "512x512/any": true, "192x192/maskable": true, "512x512/maskable": true}
	for _, ic := range m.Icons {
		delete(wantIcons, ic.Sizes+"/"+ic.Purpose)
		t.Run(ic.Src, func(t *testing.T) {
			if ic.Type != "image/png" {
				t.Errorf("type = %q, want image/png", ic.Type)
			}
			iconRec := do(h, httptest.NewRequest(http.MethodGet, ic.Src, nil))
			if iconRec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", iconRec.Code)
			}
			if got := iconRec.Header().Get("Content-Type"); got != "image/png" {
				t.Errorf("Content-Type = %q, want image/png", got)
			}
			cfg, err := png.DecodeConfig(bytes.NewReader(iconRec.Body.Bytes()))
			if err != nil {
				t.Fatalf("the icon is not a PNG: %v", err)
			}
			if got := strconv.Itoa(cfg.Width) + "x" + strconv.Itoa(cfg.Height); got != ic.Sizes {
				t.Errorf("the PNG is %s, the manifest says %s", got, ic.Sizes)
			}
		})
	}
	for missing := range wantIcons {
		t.Errorf("the manifest has no icon %s", missing)
	}
}

// TestManifestAndServiceWorkerMethods: only GET and HEAD are allowed.
func TestManifestAndServiceWorkerMethods(t *testing.T) {
	t.Parallel()
	s, _ := newTestServer(t)
	for _, path := range []string{"/manifest.webmanifest", "/sw.js"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			if rec := do(s.Handler(), httptest.NewRequest(method, path, nil)); rec.Code != http.StatusOK {
				t.Errorf("%s %s: status = %d, want 200", method, path, rec.Code)
			}
		}
		rec := do(s.Handler(), httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s: status = %d, want 405", path, rec.Code)
		}
	}
}

// TestServiceWorkerIsServed: /sw.js is at the root, for a visitor without a
// login, as JavaScript that is revalidated each time, with the headers of D36
// and the version of the static files in place of the placeholder.
func TestServiceWorkerIsServed(t *testing.T) {
	t.Parallel()
	s, _ := newTestServer(t)
	files, err := newStaticHandler(static.FS, staticPrefix, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}

	rec := do(s.Handler(), httptest.NewRequest(http.MethodGet, "/sw.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	headers := []struct{ name, want string }{
		{"Content-Type", "text/javascript; charset=utf-8"},
		{"Cache-Control", "no-cache"},
		{"Content-Security-Policy", pageCSP},
		{"X-Content-Type-Options", "nosniff"},
		{"X-Frame-Options", "DENY"},
		{"Referrer-Policy", "same-origin"},
		{"Cross-Origin-Resource-Policy", "same-origin"},
	}
	for _, hd := range headers {
		if got := rec.Header().Get(hd.name); got != hd.want {
			t.Errorf("%s = %q, want %q", hd.name, got, hd.want)
		}
	}
	if rec.Header().Get("ETag") == "" {
		t.Errorf("sw.js has no ETag, so a browser cannot ask whether it changed")
	}
	body := rec.Body.String()
	if strings.Contains(body, versionPlaceholder) {
		t.Errorf("the placeholder %s is still in sw.js", versionPlaceholder)
	}
	if want := "const VERSION = '" + files.version + "';"; !strings.Contains(body, want) {
		t.Errorf("sw.js lacks %q", want)
	}
	if len(files.version) != 32 {
		t.Errorf("the version %q is not 32 hex digits", files.version)
	}

	// A second request with the ETag gets 304.
	req := httptest.NewRequest(http.MethodGet, "/sw.js", nil)
	req.Header.Set("If-None-Match", rec.Header().Get("ETag"))
	if rec2 := do(s.Handler(), req); rec2.Code != http.StatusNotModified {
		t.Errorf("status with If-None-Match = %d, want 304", rec2.Code)
	}
}

// TestStaticVersion: the version of the static files changes with any file's
// content, name or number, and stays the same for the same files.
func TestStaticVersion(t *testing.T) {
	t.Parallel()
	base := fstest.MapFS{"app.css": {Data: []byte("a{}")}, "code.js": {Data: []byte("1")}}
	tests := []struct {
		name     string
		fsys     fstest.MapFS
		wantSame bool
	}{
		{"the same files", fstest.MapFS{"app.css": {Data: []byte("a{}")}, "code.js": {Data: []byte("1")}}, true},
		{"a file changed", fstest.MapFS{"app.css": {Data: []byte("a{color:red}")}, "code.js": {Data: []byte("1")}}, false},
		{"a file renamed", fstest.MapFS{"app2.css": {Data: []byte("a{}")}, "code.js": {Data: []byte("1")}}, false},
		{"a file added", fstest.MapFS{"app.css": {Data: []byte("a{}")}, "code.js": {Data: []byte("1")}, "new.png": {Data: []byte("p")}}, false},
		{"a file removed", fstest.MapFS{"app.css": {Data: []byte("a{}")}}, false},
		{"a file in a folder", fstest.MapFS{"app.css": {Data: []byte("a{}")}, "code.js": {Data: []byte("1")}, "fonts/a.woff2": {Data: []byte("f")}}, false},
	}
	ref, err := newStaticHandler(base, staticPrefix, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			other, err := newStaticHandler(tt.fsys, staticPrefix, http.NotFoundHandler())
			if err != nil {
				t.Fatal(err)
			}
			if same := other.version == ref.version; same != tt.wantSame {
				t.Errorf("versions %q and %q: same = %v, want %v", ref.version, other.version, same, tt.wantSame)
			}
		})
	}
}

// TestServiceWorkerVersionFollowsTheFiles: the worker served for two sets of
// static files has two versions in it, so the browser installs a new worker and
// the old cache is dropped.
func TestServiceWorkerVersionFollowsTheFiles(t *testing.T) {
	t.Parallel()
	serve := func(fsys fstest.MapFS) string {
		t.Helper()
		files, err := newStaticHandler(fsys, staticPrefix, http.NotFoundHandler())
		if err != nil {
			t.Fatal(err)
		}
		sw, err := newServiceWorkerHandler(static.ServiceWorker, files.version)
		if err != nil {
			t.Fatal(err)
		}
		return do(sw, httptest.NewRequest(http.MethodGet, "/sw.js", nil)).Body.String()
	}
	a := serve(fstest.MapFS{"app.css": {Data: []byte("a{}")}})
	b := serve(fstest.MapFS{"app.css": {Data: []byte("a{}")}, "datastar.js": {Data: []byte("2")}})
	if a == b {
		t.Errorf("the two workers are the same, with two different sets of static files")
	}
}

// TestServiceWorkerRules reads the source of sw.js for its rules: it handles
// GET requests for /static/ of this site and for nothing else, by one
// respondWith (every other request returns before it), and keeps its cache under
// a name with the version.
func TestServiceWorkerRules(t *testing.T) {
	t.Parallel()
	src := string(static.ServiceWorker)
	tests := []struct {
		name string
		want string
	}{
		{"only GET", `request.method !== 'GET'`},
		{"only /static/", `url.pathname.startsWith('/static/')`},
		{"only this site", `url.origin === self.location.origin`},
		{"the cache has the version", `PREFIX + VERSION`},
		{"old caches are deleted", `caches.delete(n)`},
		{"a fetch that is not static returns", "if (!isStatic(event.request)) {\n    return;\n  }"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(src, tt.want) {
				t.Errorf("sw.js lacks %q", tt.want)
			}
		})
	}
	if n := strings.Count(src, "respondWith("); n != 1 {
		t.Errorf("sw.js calls respondWith %d times, want 1 (for the static files)", n)
	}
	for _, banned := range []string{"/api/", "/files/", "/avatars/", "importScripts", "push", "sync"} {
		if strings.Contains(src, "'"+banned) || strings.Contains(src, `"`+banned) {
			t.Errorf("sw.js mentions %q", banned)
		}
	}
}

// TestServiceWorkerWithoutPlaceholder: a source with no place for the version
// is refused.
func TestServiceWorkerWithoutPlaceholder(t *testing.T) {
	t.Parallel()
	h, err := newServiceWorkerHandler([]byte("self.addEventListener('fetch', () => {});"), "abc")
	if h != nil {
		t.Errorf("handler = %v, want nil", h)
	}
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T: %v", err, err)
	}
	if appErr.Tag != tagServiceWorkerInvalid {
		t.Errorf("tag = %q, want %q", appErr.Tag, tagServiceWorkerInvalid)
	}
	if !appErr.HasRoute("newServiceWorkerHandler.ServiceWorkerInvalid") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}
}

// TestManifestWithoutPlaceholder: a source with no place for the version is
// refused.
func TestManifestWithoutPlaceholder(t *testing.T) {
	t.Parallel()
	h, err := newManifestHandler([]byte(`{"name": "Clowder"}`), "abc")
	if h != nil {
		t.Errorf("handler = %v, want nil", h)
	}
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T: %v", err, err)
	}
	if appErr.Tag != tagManifestInvalid {
		t.Errorf("tag = %q, want %q", appErr.Tag, tagManifestInvalid)
	}
	if !appErr.HasRoute("newManifestHandler.ManifestInvalid") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}
}

// TestStaticQueryIsIgnored: /static/app.css?v=x is served like /static/app.css,
// with the same ETag, and the ETag of the plain address revalidates the
// versioned one with a 304.
func TestStaticQueryIsIgnored(t *testing.T) {
	t.Parallel()
	s, _ := newTestServer(t)
	plain := do(s.Handler(), httptest.NewRequest(http.MethodGet, "/static/app.css", nil))
	versioned := do(s.Handler(), httptest.NewRequest(http.MethodGet, "/static/app.css?v=x", nil))
	if versioned.Code != http.StatusOK || !bytes.Equal(plain.Body.Bytes(), versioned.Body.Bytes()) {
		t.Errorf("/static/app.css?v=x: status %d, body equal = %v", versioned.Code, bytes.Equal(plain.Body.Bytes(), versioned.Body.Bytes()))
	}
	for _, h := range []string{"ETag", "Content-Type", "Cache-Control"} {
		if plain.Header().Get(h) != versioned.Header().Get(h) {
			t.Errorf("%s differs: %q and %q", h, plain.Header().Get(h), versioned.Header().Get(h))
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/static/app.css?v=other", nil)
	req.Header.Set("If-None-Match", plain.Header().Get("ETag"))
	if rec := do(s.Handler(), req); rec.Code != http.StatusNotModified {
		t.Errorf("status with the ETag of the plain address = %d, want 304", rec.Code)
	}
	// A file that does not exist is not found with a query either.
	if rec := do(s.Handler(), httptest.NewRequest(http.MethodGet, "/static/missing.css?v=x", nil)); rec.Code != http.StatusNotFound {
		t.Errorf("a missing file with a query: status = %d, want 404", rec.Code)
	}
}

// TestPagesNameStaticFilesWithTheVersion: a page the server renders names every
// static file of its head, and its favicon, with ?v= and the version that /sw.js
// carries.
func TestPagesNameStaticFilesWithTheVersion(t *testing.T) {
	t.Parallel()
	s, _ := newTestServer(t)
	files, err := newStaticHandler(static.FS, staticPrefix, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	rec := do(s.Handler(), httptest.NewRequest(http.MethodGet, "/login", nil))
	body := rec.Body.String()
	for _, name := range []string{"favicon.svg", "favicon-32.png", "apple-touch-icon.png", "app.css", "code.css", "datastar.js", "code.js"} {
		if want := "/static/" + name + "?v=" + files.version; !strings.Contains(body, want) {
			t.Errorf("the login page lacks %q:\n%s", want, body)
		}
	}
	if sw := do(s.Handler(), httptest.NewRequest(http.MethodGet, "/sw.js", nil)).Body.String(); !strings.Contains(sw, files.version) {
		t.Errorf("/sw.js does not carry the version %s of the pages", files.version)
	}
}
