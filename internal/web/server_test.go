package web

import (
	"bytes"
	"context"
	"encoding/json"
	stdErrors "errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/markdown"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/clowder/internal/store/storetest"
	"github.com/rm4n0s/clowder/internal/web/static"
	"github.com/rm4n0s/errors"
)

// syncBuffer is a bytes.Buffer that is safe for the logger of a server running
// in another goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// logEntry is one JSON line of the test logger.
type logEntry struct {
	Msg        string            `json:"msg"`
	Level      string            `json:"level"`
	Method     string            `json:"method"`
	Pattern    string            `json:"pattern"`
	Status     int               `json:"status"`
	DurationMS *float64          `json:"duration_ms"`
	Err        *errors.ErrorJson `json:"err"`
}

// logEntries parses the JSON lines written to buf.
func logEntries(t *testing.T, buf fmtStringer) []logEntry {
	t.Helper()
	var entries []logEntry
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var e logEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", line, err)
		}
		entries = append(entries, e)
	}
	return entries
}

type fmtStringer interface{ String() string }

// testPassword is the password of the users the tests make.
const testPassword = "correct horse battery"

// testStart is the time the fake clock of the tests starts at.
var testStart = time.Date(2026, 10, 26, 9, 30, 0, 0, time.UTC)

// testEnv is a server on a fresh in-memory database, with the real accounts
// service, the fast password hasher and the fake clock.
type testEnv struct {
	t        *testing.T
	srv      *Server
	log      *syncBuffer
	accounts *app.Accounts
	rooms    *app.Rooms
	audit    *app.Audit
	posts    *app.Posts
	board    *app.Board
	search   *app.Search
	notifs   *app.Notifications
	hub      *live.Hub
	st       *store.Store
	clk      *clock.Fake
	// filesDir is the directory of the files of the attachments.
	filesDir string
}

// newTestEnv makes a testEnv. mutate changes the configuration before the
// server is made; extra routes are added to the server's.
func newTestEnv(t *testing.T, mutate func(*config.Config), extra ...route) *testEnv {
	t.Helper()
	buf := &syncBuffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	clk := clock.NewFake(testStart)
	st := storetest.Open(t, clk)
	cfg := config.Default()
	if mutate != nil {
		mutate(&cfg)
	}
	accounts := app.NewAccounts(st, clk, auth.FastHasher(), app.AccountsConfig{
		InvitationLifetime: cfg.InvitationLifetime,
		SessionLifetime:    cfg.SessionLifetime,
	})
	filesDir := filepath.Join(t.TempDir(), "files")
	fileStore, err := files.New(filesDir)
	if err != nil {
		t.Fatalf("files.New: %+v", err)
	}
	hub := live.New()
	t.Cleanup(hub.Close)
	rooms := app.NewRooms(st, fileStore, logger, hub)
	audit := app.NewAudit(st)
	board := app.NewBoard(st, clk, hub)
	posts := app.NewPosts(st, clk, markdown.New(), fileStore, app.PostsConfig{
		MaxAttachmentSize: cfg.MaxAttachmentSize,
		MaxAttachments:    cfg.MaxAttachmentsPerPost,
		Logger:            logger,
	}, hub)
	search := app.NewSearch(st)
	notifs := app.NewNotifications(st, hub)
	s, err := newServer(cfg, logger, Services{Accounts: accounts, Rooms: rooms, Audit: audit, Posts: posts, Board: board, Search: search, Notifications: notifs, Hub: hub}, static.FS, extra...)
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	return &testEnv{t: t, srv: s, log: buf, accounts: accounts, rooms: rooms, audit: audit, posts: posts, board: board, search: search, notifs: notifs, hub: hub, st: st, clk: clk, filesDir: filesDir}
}

// newTestServer makes a server whose logger writes JSON to the returned buffer.
func newTestServer(t *testing.T, extra ...route) (*Server, *syncBuffer) {
	t.Helper()
	e := newTestEnv(t, nil, extra...)
	return e.srv, e.log
}

// publicRoute is an extra route of a test that never looks at the session.
func publicRoute(pattern string, h http.Handler) route {
	return route{pattern: pattern, handler: h, access: noSession}
}

// do sends a request to the handler and returns the recorded answer.
func do(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHandlerPages(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantBody   []string
	}{
		{
			name: "login", method: http.MethodGet, path: "/login",
			wantStatus: http.StatusOK,
			wantBody:   []string{"<!doctype html>", "<h1>Log in</h1>", "<title>Log in · Clowder</title>"},
		},
		{
			name: "unknown path", method: http.MethodGet, path: "/nope",
			wantStatus: http.StatusNotFound,
			wantBody:   []string{"<h1>404</h1>", "Page not found", "<title>404 Not Found · Clowder</title>"},
		},
		{
			name: "unknown nested path", method: http.MethodGet, path: "/rooms/1/nothing",
			wantStatus: http.StatusNotFound,
			wantBody:   []string{"<h1>404</h1>"},
		},
		{
			name: "unknown path with POST", method: http.MethodPost, path: "/nope",
			wantStatus: http.StatusNotFound,
			wantBody:   []string{"<h1>404</h1>"},
		},
		{
			name: "unknown static file", method: http.MethodGet, path: "/static/missing.js",
			wantStatus: http.StatusNotFound,
			wantBody:   []string{"<h1>404</h1>"},
		},
		{
			name: "static directory is not listed", method: http.MethodGet, path: "/static/",
			wantStatus: http.StatusNotFound,
			wantBody:   []string{"<h1>404</h1>"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newTestServer(t)
			rec := do(s.Handler(), httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body:\n%s", rec.Code, tt.wantStatus, rec.Body)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
				t.Errorf("Content-Type = %q, want text/html; charset=utf-8", ct)
			}
			body := strings.ToLower(rec.Body.String())
			for _, w := range tt.wantBody {
				if !strings.Contains(body, strings.ToLower(w)) {
					t.Errorf("body does not contain %q:\n%s", w, rec.Body)
				}
			}
		})
	}
}

func TestMethodNotAllowedStaysWithTheMux(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		path      string
		wantAllow string
	}{
		{"POST home", http.MethodPost, "/", "GET, HEAD"},
		{"DELETE static file", http.MethodDelete, "/static/app.css", "GET, HEAD"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newTestServer(t)
			rec := do(s.Handler(), httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want 405", rec.Code)
			}
			if got := rec.Header().Get("Allow"); got != tt.wantAllow {
				t.Errorf("Allow = %q, want %q", got, tt.wantAllow)
			}
		})
	}
}

func TestStaticFiles(t *testing.T) {
	tests := []struct {
		path            string
		wantContentType string
	}{
		{"/static/app.css", "text/css; charset=utf-8"},
		{"/static/datastar.js", "text/javascript; charset=utf-8"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			s, _ := newTestServer(t)
			want, err := static.FS.ReadFile(strings.TrimPrefix(tt.path, "/static/"))
			if err != nil {
				t.Fatal(err)
			}

			rec := do(s.Handler(), httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != tt.wantContentType {
				t.Errorf("Content-Type = %q, want %q", got, tt.wantContentType)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
				t.Errorf("Cache-Control = %q, want no-cache", got)
			}
			etag := rec.Header().Get("ETag")
			if len(etag) < 4 || etag[0] != '"' || etag[len(etag)-1] != '"' {
				t.Errorf("ETag = %q, want a quoted value", etag)
			}
			if !bytes.Equal(rec.Body.Bytes(), want) {
				t.Errorf("body differs from the embedded file (%d bytes, want %d)", rec.Body.Len(), len(want))
			}

			// The same ETag is sent again by a second server: it is the hash of the content.
			other, _ := newTestServer(t)
			rec2 := do(other.Handler(), httptest.NewRequest(http.MethodGet, tt.path, nil))
			if got := rec2.Header().Get("ETag"); got != etag {
				t.Errorf("ETag of a second server = %q, want %q", got, etag)
			}

			// Revalidation: the same ETag gets 304 without a body.
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Header.Set("If-None-Match", etag)
			rec = do(s.Handler(), req)
			if rec.Code != http.StatusNotModified {
				t.Errorf("status with If-None-Match = %d, want 304", rec.Code)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("304 has a body of %d bytes", rec.Body.Len())
			}
			if got := rec.Header().Get("ETag"); got != etag {
				t.Errorf("304 ETag = %q, want %q", got, etag)
			}

			// A different ETag gets the file.
			req = httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Header.Set("If-None-Match", `"other"`)
			if rec = do(s.Handler(), req); rec.Code != http.StatusOK {
				t.Errorf("status with another ETag = %d, want 200", rec.Code)
			}
		})
	}
}

func TestRequestLog(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		target      string
		wantPattern string
		wantStatus  int
	}{
		{"home without a session", http.MethodGet, "/", "GET /{$}", http.StatusFound},
		{"login page", http.MethodGet, "/login", "GET /login", http.StatusOK},
		{"static", http.MethodGet, "/static/app.css", "GET /static/", http.StatusOK},
		{"not found logs the path", http.MethodGet, "/nope", "/nope", http.StatusNotFound},
		{"query string is not logged", http.MethodGet, "/?token=secret", "GET /{$}", http.StatusFound},
		{"not allowed logs the path", http.MethodPost, "/", "/", http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, buf := newTestServer(t)
			do(s.Handler(), httptest.NewRequest(tt.method, tt.target, nil))

			entries := logEntries(t, buf)
			if len(entries) != 1 {
				t.Fatalf("got %d log entries, want 1:\n%s", len(entries), buf.String())
			}
			e := entries[0]
			if e.Msg != "request" {
				t.Errorf("msg = %q, want request", e.Msg)
			}
			if e.Method != tt.method {
				t.Errorf("method = %q, want %q", e.Method, tt.method)
			}
			if e.Pattern != tt.wantPattern {
				t.Errorf("pattern = %q, want %q", e.Pattern, tt.wantPattern)
			}
			if e.Status != tt.wantStatus {
				t.Errorf("status = %d, want %d", e.Status, tt.wantStatus)
			}
			if e.DurationMS == nil || *e.DurationMS < 0 {
				t.Errorf("duration_ms = %v, want a number >= 0", e.DurationMS)
			}
			if strings.Contains(buf.String(), "secret") {
				t.Errorf("the query string is in the log:\n%s", buf.String())
			}
		})
	}
}

func TestPanicRecovery(t *testing.T) {
	tests := []struct {
		name       string
		handler    http.HandlerFunc
		wantStatus int
		wantPanic  string
		wantBody   bool
	}{
		{
			name:       "string panic",
			handler:    func(http.ResponseWriter, *http.Request) { panic("boom") },
			wantStatus: http.StatusInternalServerError,
			wantPanic:  "boom",
			wantBody:   true,
		},
		{
			name:       "error panic",
			handler:    func(http.ResponseWriter, *http.Request) { panic(stdErrors.New("disk on fire")) },
			wantStatus: http.StatusInternalServerError,
			wantPanic:  "disk on fire",
			wantBody:   true,
		},
		{
			name: "panic after the answer started",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusAccepted)
				panic("late")
			},
			wantStatus: http.StatusAccepted,
			wantPanic:  "late",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, buf := newTestServer(t, publicRoute("GET /panic", tt.handler))
			rec := do(s.Handler(), httptest.NewRequest(http.MethodGet, "/panic", nil))
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			body := rec.Body.String()
			if tt.wantBody {
				if !strings.Contains(body, "<h1>500</h1>") || !strings.Contains(body, "Something went wrong") {
					t.Errorf("body is not the 500 page:\n%s", body)
				}
			}
			if strings.Contains(body, tt.wantPanic) {
				t.Errorf("the panic value is in the page:\n%s", body)
			}

			entries := logEntries(t, buf)
			if len(entries) != 2 {
				t.Fatalf("got %d log entries, want 2 (the panic and the request):\n%s", len(entries), buf.String())
			}
			panicked, request := entries[0], entries[1]
			if panicked.Err == nil {
				t.Fatalf("the panic entry has no err attribute:\n%s", buf.String())
			}
			// Replay the logged error the way a test of a bug report would.
			logged := errors.FromJson(*panicked.Err)
			if logged.Tag != tagHandlerPanicked {
				t.Errorf("tag = %q, want %q", logged.Tag, tagHandlerPanicked)
			}
			if !panicked.Err.HasRoute("Server.recoverPanic.HandlerPanicked") {
				t.Errorf("unexpected route: %s", panicked.Err.Route())
			}
			if got, _ := logged.Metadata["panic"].(string); got != tt.wantPanic {
				t.Errorf("metadata panic = %q, want %q", got, tt.wantPanic)
			}
			stack, _ := logged.Metadata["stack"].(string)
			if !strings.Contains(stack, "goroutine") || !strings.Contains(stack, "server_test.go") {
				t.Errorf("metadata stack does not show the panicking handler:\n%s", stack)
			}
			if request.Msg != "request" || request.Status != tt.wantStatus || request.Pattern != "GET /panic" {
				t.Errorf("request entry = %+v, want status %d on GET /panic", request, tt.wantStatus)
			}
		})
	}
}

func TestPanicAbortHandlerReachesTheServer(t *testing.T) {
	s, buf := newTestServer(t, publicRoute("GET /abort", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	})))
	defer func() {
		if v := recover(); v != http.ErrAbortHandler {
			t.Errorf("recovered %v, want http.ErrAbortHandler", v)
		}
		if strings.Contains(buf.String(), "handler panicked") {
			t.Errorf("an aborted handler was logged as a panic:\n%s", buf.String())
		}
	}()
	do(s.Handler(), httptest.NewRequest(http.MethodGet, "/abort", nil))
	t.Error("the panic did not reach the caller")
}

func TestCrossOriginProtection(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		headers    map[string]string
		wantStatus int
	}{
		{
			name: "cross-site POST is refused", method: http.MethodPost,
			headers:    map[string]string{"Sec-Fetch-Site": "cross-site"},
			wantStatus: http.StatusForbidden,
		},
		{
			name: "cross-origin POST by Origin is refused", method: http.MethodPost,
			headers:    map[string]string{"Origin": "https://evil.example"},
			wantStatus: http.StatusForbidden,
		},
		{
			name: "same-origin POST passes to the router", method: http.MethodPost,
			headers:    map[string]string{"Sec-Fetch-Site": "same-origin"},
			wantStatus: http.StatusMethodNotAllowed, // POST / is not a route: the mux answers
		},
		{
			name: "cross-site GET is allowed", method: http.MethodGet,
			headers:    map[string]string{"Sec-Fetch-Site": "cross-site"},
			wantStatus: http.StatusFound, // GET / needs a login: the redirect shows it passed
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newTestServer(t)
			req := httptest.NewRequest(tt.method, "/", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			if rec := do(s.Handler(), req); rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

// failingWriter is a ResponseWriter whose body write fails.
type failingWriter struct{ header http.Header }

func (w *failingWriter) Header() http.Header       { return w.header }
func (w *failingWriter) WriteHeader(int)           {}
func (w *failingWriter) Write([]byte) (int, error) { return 0, stdErrors.New("broken pipe") }

func TestRenderFailures(t *testing.T) {
	tests := []struct {
		name      string
		w         func() http.ResponseWriter
		ctx       func() (context.Context, context.CancelFunc)
		wantTag   string
		wantLevel string
	}{
		{
			name: "component fails to render",
			w:    func() http.ResponseWriter { return httptest.NewRecorder() },
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel() // templ components stop on a cancelled context
				return ctx, cancel
			},
			wantTag:   tagPageRenderFailed,
			wantLevel: "ERROR",
		},
		{
			name:      "client write fails",
			w:         func() http.ResponseWriter { return &failingWriter{header: http.Header{}} },
			ctx:       func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			wantTag:   tagResponseWriteFailed,
			wantLevel: "DEBUG",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, buf := newTestServer(t)
			ctx, cancel := tt.ctx()
			defer cancel()
			w := tt.w()
			req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
			// The 404 page needs no session and no database, so only the render
			// can fail.
			s.notFound(w, req)

			var entry *logEntry
			for _, e := range logEntries(t, buf) {
				if e.Err != nil {
					entry = &e
				}
			}
			if entry == nil {
				t.Fatalf("no error was logged:\n%s", buf.String())
			}
			if entry.Level != tt.wantLevel {
				t.Errorf("level = %q, want %q", entry.Level, tt.wantLevel)
			}
			if entry.Err.Tag != tt.wantTag {
				t.Errorf("tag = %q, want %q", entry.Err.Tag, tt.wantTag)
			}
			if !entry.Err.HasRoute("Server.render." + tt.wantTag) {
				t.Errorf("unexpected route: %s", entry.Err.Route())
			}
			if rec, ok := w.(*httptest.ResponseRecorder); ok {
				if rec.Code != http.StatusInternalServerError {
					t.Errorf("status = %d, want 500", rec.Code)
				}
				if strings.Contains(rec.Body.String(), "canceled") {
					t.Errorf("the error text is in the answer:\n%s", rec.Body)
				}
			}
		})
	}
}

// errorFS is an fs.FS that holds files but fails to open some of them.
type errorFS struct {
	fs.FS
	failOpen string // the name that cannot be opened; "." fails the walk
}

func (f errorFS) Open(name string) (fs.File, error) {
	if name == f.failOpen {
		return nil, stdErrors.New("disk error")
	}
	return f.FS.Open(name)
}

func TestNewStaticHandlerFailures(t *testing.T) {
	base := fstest.MapFS{"a.css": {Data: []byte("a{}")}, "b.js": {Data: []byte("1")}}
	tests := []struct {
		name     string
		fsys     fs.FS
		wantStep string
	}{
		{"walk fails", errorFS{FS: base, failOpen: "."}, "walk"},
		{"read fails", errorFS{FS: base, failOpen: "b.js"}, "read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newStaticHandler(tt.fsys, "/static/", http.NotFoundHandler())
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T: %v", err, err)
			}
			if appErr.Tag != tagStaticHashFailed {
				t.Errorf("tag = %q, want %q", appErr.Tag, tagStaticHashFailed)
			}
			if !appErr.HasRoute("newStaticHandler.StaticHashFailed") {
				t.Errorf("unexpected failure path: %s", appErr.Route())
			}
			if appErr.Metadata["step"] != tt.wantStep {
				t.Errorf("metadata step = %v, want %q", appErr.Metadata["step"], tt.wantStep)
			}
		})
	}
}

func TestNewServerStaticFailure(t *testing.T) {
	base := fstest.MapFS{"a.css": {Data: []byte("a{}")}}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	s, err := newServer(config.Default(), logger, Services{}, errorFS{FS: base, failOpen: "a.css"})
	if s != nil {
		t.Errorf("server = %v, want nil", s)
	}
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T: %v", err, err)
	}
	if appErr.Tag != tagStaticHashFailed {
		t.Errorf("tag = %q, want %q", appErr.Tag, tagStaticHashFailed)
	}
	if !appErr.HasRoute("newServer->newStaticHandler.StaticHashFailed") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}
}

func TestRunListenFailed(t *testing.T) {
	// An address that is already taken.
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = taken.Close() }() // may already be closed

	tests := []struct {
		name string
		addr string
	}{
		{"address in use", taken.Addr().String()},
		{"malformed address", "not an address"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.ListenAddr = tt.addr
			s, err := New(cfg, slog.New(slog.NewJSONHandler(io.Discard, nil)), Services{})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			err = s.Run(context.Background())
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T: %v", err, err)
			}
			if appErr.Tag != tagListenFailed {
				t.Errorf("tag = %q, want %q", appErr.Tag, tagListenFailed)
			}
			if !appErr.HasRoute("Server.Run.ListenFailed") {
				t.Errorf("unexpected failure path: %s", appErr.Route())
			}
			if appErr.Metadata["addr"] != tt.addr {
				t.Errorf("metadata addr = %v, want %q", appErr.Metadata["addr"], tt.addr)
			}
		})
	}
}

// brokenListener fails Accept with an error that is not temporary.
type brokenListener struct{ net.Listener }

func (brokenListener) Accept() (net.Conn, error) { return nil, stdErrors.New("accept broke") }

func TestServeFailed(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := brokenListener{inner}
	defer func() { _ = ln.Close() }() // Serve closes it

	s, _ := newTestServer(t)
	err = s.serve(context.Background(), ln)
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T: %v", err, err)
	}
	if appErr.Tag != tagServeFailed {
		t.Errorf("tag = %q, want %q", appErr.Tag, tagServeFailed)
	}
	if !appErr.HasRoute("Server.serve.ServeFailed") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}
}

// runServer runs serve on a free local port and returns its address, the
// cancel function and a channel that receives the result of serve.
func runServer(t *testing.T, s *Server) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		done <- s.serve(ctx, ln)
		close(finished)
	}()
	t.Cleanup(func() {
		cancel()
		<-finished
	})
	return "http://" + ln.Addr().String(), cancel, done
}

func TestServeStopsCleanly(t *testing.T) {
	s, buf := newTestServer(t)
	base, cancel, done := runServer(t, s)

	resp, err := http.Get(base + "/") // no session: it follows the redirect to the login page
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if cerr := resp.Body.Close(); cerr != nil {
		t.Errorf("close body: %v", cerr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "<h1>Log in</h1>") {
		t.Fatalf("GET / = %d:\n%s", resp.StatusCode, body)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve returned %v after cancel, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after the context was cancelled")
	}

	var msgs []string
	for _, e := range logEntries(t, buf) {
		msgs = append(msgs, e.Msg)
	}
	want := []string{"server listening", "request", "request", "server shutting down", "server stopped"} // GET / and the login page it redirects to
	if strings.Join(msgs, ",") != strings.Join(want, ",") {
		t.Errorf("log messages = %v, want %v", msgs, want)
	}
}

func TestServeWaitsForRunningRequests(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	s, _ := newTestServer(t, publicRoute("GET /slow", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "finished")
	})))
	base, cancel, done := runServer(t, s)

	type result struct {
		body string
		err  error
	}
	got := make(chan result, 1)
	go func() {
		resp, err := http.Get(base + "/slow")
		if err != nil {
			got <- result{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		got <- result{body: string(b), err: err}
	}()
	<-started

	cancel()
	select {
	case err := <-done:
		t.Fatalf("serve returned (%v) while a request was running", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)

	if r := <-got; r.err != nil || r.body != "finished" {
		t.Errorf("running request = %q, %v; want it to finish", r.body, r.err)
	}
	if err := <-done; err != nil {
		t.Errorf("serve returned %v, want nil", err)
	}
}

func TestServeShutdownFailed(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	s, _ := newTestServer(t, publicRoute("GET /stuck", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(started)
		<-release
	})))
	s.shutdownTimeout = 50 * time.Millisecond
	base, cancel, done := runServer(t, s)

	go func() {
		resp, err := http.Get(base + "/stuck")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-started
	cancel()

	select {
	case err := <-done:
		appErr, ok := errors.FromError(err)
		if !ok {
			t.Fatalf("expected *errors.Error, got %T: %v", err, err)
		}
		if appErr.Tag != tagShutdownFailed {
			t.Errorf("tag = %q, want %q", appErr.Tag, tagShutdownFailed)
		}
		if !appErr.HasRoute("Server.serve.ShutdownFailed") {
			t.Errorf("unexpected failure path: %s", appErr.Route())
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("the cause is %v, want a deadline exceeded", errors.Unwrap(err))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after the shutdown timeout")
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	cfg := config.Default()
	cfg.ListenAddr = "127.0.0.1:0"
	buf := &syncBuffer{}
	s, err := New(cfg, slog.New(slog.NewJSONHandler(buf, nil)), Services{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// Wait until it listens.
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(buf.String(), "server listening") {
		if time.Now().After(deadline) {
			t.Fatal("the server did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}
