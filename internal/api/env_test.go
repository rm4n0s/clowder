package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/markdown"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/clowder/internal/store/storetest"
	"github.com/rm4n0s/errors"
)

// testPassword is the password of the users the tests make.
const testPassword = "correct horse battery"

// testStart is the time the fake clock of the tests starts at.
var testStart = time.Date(2026, 10, 26, 9, 30, 0, 0, time.UTC)

// syncBuffer is a bytes.Buffer that is safe for a logger used by many
// goroutines.
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

// testEnv is the API on a fresh database, with the real services, the fast
// password hasher and the fake clock. Requests go through a mux that holds the
// routes of the API, as the server of internal/web mounts them.
type testEnv struct {
	t        *testing.T
	srv      *Server
	handler  http.Handler
	accounts *app.Accounts
	msgs     *app.Messages
	rooms    *app.Rooms
	posts    *app.Posts
	board    *app.Board
	calls    *app.Calls
	// opts are the options the server was made with.
	opts Options
	// filesDir is where the files of the attachments are.
	filesDir string
	st       *store.Store
	clk      *clock.Fake
	rec      *live.Recorder
	// hub is the hub the services publish to, which the WebSocket listens to;
	// rec records the same events.
	hub *live.Hub
	// presence is shared by the services and the server; a test connects users.
	presence *live.MemoryPresence
	log      *syncBuffer
	// noted are the user IDs the NoteUser hook received.
	noted *noteLog
}

// noteLog records the calls of the NoteUser hook.
type noteLog struct {
	mu  sync.Mutex
	ids []uint64
}

func (n *noteLog) add(id uint64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.ids = append(n.ids, id)
}

func (n *noteLog) all() []uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]uint64(nil), n.ids...)
}

// testSSHServer is the SSH server that the API of a test env shows.
var testSSHServer = app.SSHServerInfo{Addr: ":2222", PublicHost: "chat.example.org", HostFingerprint: "SHA256:testhostkeyfingerprint"}

// newTestEnv makes a testEnv. mutate changes the options before the server is
// made.
func newTestEnv(t *testing.T, mutate func(*Options)) *testEnv {
	t.Helper()
	return newTestEnvSSH(t, mutate, testSSHServer)
}

// newTestEnvSSH is newTestEnv with the SSH server that the API shows: the zero
// value is a Clowder whose SSH server is off.
func newTestEnvSSH(t *testing.T, mutate func(*Options), sshServer app.SSHServerInfo) *testEnv {
	t.Helper()
	buf := &syncBuffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	clk := clock.NewFake(testStart)
	st := storetest.Open(t, clk)
	rec := &live.Recorder{}
	presence := live.NewMemoryPresence()
	hub := live.New()
	t.Cleanup(hub.Close)
	pub := fanout{rec, hub}
	avatars, err := files.New(filepath.Join(t.TempDir(), "avatars"))
	if err != nil {
		t.Fatalf("files.New: %+v", err)
	}
	accounts := app.NewAccounts(st, clk, auth.FastHasher(), app.AccountsConfig{
		InvitationLifetime: 7 * 24 * time.Hour,
		SessionLifetime:    30 * 24 * time.Hour,
		Publisher:          pub,
		Avatars:            avatars,
		SSHServer:          sshServer.Addr != "",
	})
	filesDir := filepath.Join(t.TempDir(), "files")
	fileStore, err := files.New(filesDir)
	if err != nil {
		t.Fatalf("files.New: %+v", err)
	}
	msgs := app.NewMessages(st, clk, fileStore, app.MessagesConfig{MaxAttachmentSize: 1 << 20, MaxAttachments: 4, Presence: presence}, pub)
	notifs := app.NewNotifications(st, pub)
	rooms := app.NewRooms(st, fileStore, logger, pub)
	posts := app.NewPosts(st, clk, markdown.New(), fileStore, app.PostsConfig{MaxAttachmentSize: 1 << 20, MaxAttachments: 4, Logger: logger, Presence: presence}, pub)
	board := app.NewBoard(st, clk, pub)
	callSvc := app.NewCalls(st, clk, app.CallsConfig{ProcessID: "0123456789abcdef", Enabled: true, Logger: logger}, pub)
	noted := &noteLog{}
	opts := Options{
		Services:      Services{Accounts: accounts, Notifications: notifs, Messages: msgs, Rooms: rooms, Posts: posts, Board: board, Search: app.NewSearch(st), Audit: app.NewAudit(st), Prefs: app.NewPrefs(st, pub), Presence: app.NewPresence(st, presence), SSHKeys: app.NewSSHKeys(st, clk, accounts, sshServer), Calls: callSvc, Hub: hub},
		Logger:        logger,
		NoteUser:      func(_ *http.Request, id uint64) { noted.add(id) },
		MaxUploadSize: 1 << 20,
	}
	if mutate != nil {
		mutate(&opts)
	}
	srv, err := New(opts)
	if err != nil {
		t.Fatalf("New: %+v", err)
	}
	mux := http.NewServeMux()
	for _, rt := range srv.Routes() {
		mux.Handle(rt.Pattern, rt.Handler)
	}
	return &testEnv{t: t, srv: srv, handler: mux, accounts: accounts, msgs: msgs, rooms: rooms, posts: posts, board: board, calls: callSvc, opts: opts, filesDir: filesDir, st: st, clk: clk, rec: rec, hub: hub, presence: presence, log: buf, noted: noted}
}

// remake makes the server again with the options that mutate changes, on the
// same database and hub.
func (e *testEnv) remake(mutate func(*Options)) {
	e.t.Helper()
	opts := e.opts
	mutate(&opts)
	srv, err := New(opts)
	if err != nil {
		e.t.Fatalf("New: %+v", err)
	}
	mux := http.NewServeMux()
	for _, rt := range srv.Routes() {
		mux.Handle(rt.Pattern, rt.Handler)
	}
	e.srv, e.handler, e.opts = srv, mux, opts
}

// fanout is the publisher of the tests: the recorder keeps every event, the
// hub delivers it to the WebSockets.
type fanout struct {
	rec *live.Recorder
	hub *live.Hub
}

// Publish records the event and delivers it.
func (f fanout) Publish(ev live.Event) {
	f.rec.Publish(ev)
	f.hub.Publish(ev)
}

// addUser makes a user with the password testPassword.
func (e *testEnv) addUser(username string, role store.Role) app.User {
	e.t.Helper()
	inv, err := e.accounts.CreateSystemInvitation(e.t.Context(), role)
	if err != nil {
		e.t.Fatalf("CreateSystemInvitation: %+v", err)
	}
	display := strings.ToUpper(username[:1]) + username[1:]
	u, err := e.accounts.SignUp(e.t.Context(), inv.Token, username, display, testPassword)
	if err != nil {
		e.t.Fatalf("SignUp: %+v", err)
	}
	return u
}

// invitation makes an invitation and returns its token.
func (e *testEnv) invitation(role store.Role) string {
	e.t.Helper()
	inv, err := e.accounts.CreateSystemInvitation(e.t.Context(), role)
	if err != nil {
		e.t.Fatalf("CreateSystemInvitation: %+v", err)
	}
	return inv.Token
}

// token logs the user in as an API client with the service and returns the
// bearer token.
func (e *testEnv) token(username string) string {
	e.t.Helper()
	res, err := e.accounts.LoginClient(e.t.Context(), app.ClientLogin{
		Username: username, Password: testPassword, ClientIP: "192.0.2.200", Kind: app.SessionAPI, ClientName: "test client",
	})
	if err != nil {
		e.t.Fatalf("LoginClient: %+v", err)
	}
	return res.Token
}

// browserToken logs the user in as a browser with the service and returns the
// cookie token.
func (e *testEnv) browserToken(username string) string {
	e.t.Helper()
	res, err := e.accounts.Login(e.t.Context(), username, testPassword, "192.0.2.201")
	if err != nil {
		e.t.Fatalf("Login: %+v", err)
	}
	return res.Token
}

// reqOpt changes a request a test sends.
type reqOpt func(*http.Request)

// bearer sends the Authorization header with the token.
func bearer(token string) reqOpt {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
}

// header sets a header.
func header(key, value string) reqOpt {
	return func(r *http.Request) { r.Header.Set(key, value) }
}

// cookie sends a cookie.
func cookie(name, value string) reqOpt {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: name, Value: value}) }
}

// fromAddr sets the address of the connection.
func fromAddr(addr string) reqOpt {
	return func(r *http.Request) { r.RemoteAddr = addr }
}

// request makes a request to path (which starts with /api/v1). A body that is
// not a string or []byte is sent as JSON with the JSON Content-Type.
func request(method, path string, body any, opts ...reqOpt) *http.Request {
	var rd io.Reader
	contentType := ""
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
		contentType = "application/json"
	case []byte:
		rd = bytes.NewReader(b)
		contentType = "application/json"
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			panic(err)
		}
		rd = bytes.NewReader(raw)
		contentType = "application/json"
	}
	r := httptest.NewRequest(method, path, rd)
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// send runs the request through the handler, checks the answer against the
// OpenAPI document (checkResponse) and returns it.
func (e *testEnv) send(r *http.Request) *httptest.ResponseRecorder {
	e.t.Helper()
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, r)
	checkResponse(e.t, r, rec)
	return rec
}

// sendRaw is send without the check against the document, for the tests of
// the check itself.
func (e *testEnv) sendRaw(r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, r)
	return rec
}

// decode reads the JSON body of the answer into a generic value.
func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("the body is not a JSON object: %v\n%s", err, rec.Body)
	}
	return m
}

// errorCode returns error.code of an error answer.
func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	obj, _ := decode(t, rec)["error"].(map[string]any)
	code, _ := obj["code"].(string)
	return code
}

// errorFields returns error.fields of an error answer.
func errorFields(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	obj, _ := decode(t, rec)["error"].(map[string]any)
	fields, _ := obj["fields"].(map[string]any)
	return fields
}

// requireStatus fails the test unless the answer has the status.
func requireStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body:\n%s", rec.Code, want, rec.Body)
	}
}

// requireRoute fails the test unless err is a tagged error with the tag whose
// route contains route.
func requireRoute(t *testing.T, err error, tag, route string) *errors.Error {
	t.Helper()
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T: %v", err, err)
	}
	if appErr.Tag != tag {
		t.Fatalf("tag = %q, want %q (route %s)", appErr.Tag, tag, appErr.Route())
	}
	if !appErr.HasRoute(route) {
		t.Fatalf("route = %s, want it to contain %s", appErr.Route(), route)
	}
	return appErr
}

// actorOf returns the Actor of a user made by addUser, found by username.
func actorOf(e *testEnv, username string) app.Actor {
	e.t.Helper()
	u, err := e.st.UserByUsername(e.t.Context(), username)
	if err != nil {
		e.t.Fatalf("UserByUsername %s: %+v", username, err)
	}
	return app.Actor{UserID: u.ID, Username: u.Username, Role: u.Role}
}

// userID returns the ID of the user with the username.
func (e *testEnv) userID(username string) uint64 {
	e.t.Helper()
	return actorOf(e, username).UserID
}
