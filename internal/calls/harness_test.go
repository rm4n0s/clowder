package calls

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
	"github.com/rm4n0s/clowder/internal/calls/callstest"
	"github.com/rm4n0s/clowder/internal/live"
)

// fakeCtl is the service of the calls as the media server sees it: it keeps
// the state of the calls in memory and publishes the end of a call to a hub,
// as internal/app does.
type fakeCtl struct {
	hub *live.Hub

	mu     sync.Mutex
	calls  map[uint64]*fakeCall
	failed []uint64
	media  []mediaState
	left   []uint64
}

// fakeCall is a call of fakeCtl.
type fakeCall struct {
	caller, callee uint64
	active, open   bool
	reason         string
}

// mediaState is a call to SetMedia.
type mediaState struct {
	call, user uint64
	mic, cam   bool
}

func newFakeCtl(hub *live.Hub) *fakeCtl {
	return &fakeCtl{hub: hub, calls: map[uint64]*fakeCall{}}
}

// add makes a call between the two users, ringing (active false) or active.
func (f *fakeCtl) add(id, caller, callee uint64, active bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[id] = &fakeCall{caller: caller, callee: callee, open: true, active: active}
}

// answer moves the call to active and publishes call.started.
func (f *fakeCtl) answer(id uint64) {
	f.mu.Lock()
	c := f.calls[id]
	c.active = true
	f.mu.Unlock()
	f.publish(live.KindCallStarted, id, c)
}

// end ends the call with the reason and publishes call.ended.
func (f *fakeCtl) end(id uint64, reason string) {
	f.mu.Lock()
	c := f.calls[id]
	if !c.open {
		f.mu.Unlock()
		return
	}
	c.open, c.reason = false, reason
	f.mu.Unlock()
	f.publish(live.KindCallEnded, id, c)
}

func (f *fakeCtl) publish(kind string, id uint64, c *fakeCall) {
	for _, u := range []uint64{c.caller, c.callee} {
		f.hub.Publish(live.Event{Topic: live.UserTopic(u), Kind: kind, UserID: u, CallID: id})
	}
}

func (f *fakeCtl) Status(_ context.Context, id uint64) (Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.calls[id]
	if !ok {
		return Status{}, nil
	}
	return Status{Open: c.open, Active: c.open && c.active, Reason: c.reason}, nil
}

func (f *fakeCtl) Fail(_ context.Context, id uint64) error {
	f.mu.Lock()
	f.failed = append(f.failed, id)
	f.mu.Unlock()
	f.end(id, "failed")
	return nil
}

func (f *fakeCtl) SetMedia(_ context.Context, id, user uint64, mic, cam bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.media = append(f.media, mediaState{id, user, mic, cam})
	return nil
}

func (f *fakeCtl) Leave(_ context.Context, id, user uint64) error {
	f.mu.Lock()
	f.left = append(f.left, user)
	f.mu.Unlock()
	f.end(id, "hangup")
	return nil
}

// failedCalls returns the calls the server ended as failed.
func (f *fakeCtl) failedCalls() []uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uint64(nil), f.failed...)
}

// rig is a media server on loopback behind an httptest server whose handler
// joins the socket of the user named in the query to the call.
type rig struct {
	t      *testing.T
	hub    *live.Hub
	ctl    *fakeCtl
	srv    *Server
	http   *httptest.Server
	logs   *logBuffer
	closed bool
	// ownsHub says that closing the rig closes the hub too; a rig on a hub
	// that the test shares leaves it to the test.
	ownsHub bool

	// sessions are the session checks of the users, by user ID; a user with
	// none keeps a good session.
	smu      sync.Mutex
	sessions map[uint64]func() (websocket.StatusCode, string)
}

// endSession makes the session check of the user fail with the code.
func (r *rig) endSession(user uint64, code websocket.StatusCode, reason string) {
	r.smu.Lock()
	defer r.smu.Unlock()
	r.sessions[user] = func() (websocket.StatusCode, string) { return code, reason }
}

// logBuffer collects the log of the server so that a test can look at it.
type logBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// newRig starts a server with cfg (Addr and IncludeLoopback are set here).
func newRig(t *testing.T, cfg Config) *rig {
	t.Helper()
	hub := live.New()
	r := newRigOn(t, cfg, newFakeCtl(hub), hub)
	r.ownsHub = true
	return r
}

// newRigOn starts a server on the given service and hub, so that two servers
// can stand for two processes of one cluster: they share the state of the
// calls and the events, as the processes share the database and Redis.
func newRigOn(t *testing.T, cfg Config, ctl *fakeCtl, hub *live.Hub) *rig {
	t.Helper()
	r := &rig{t: t, hub: hub, ctl: ctl, logs: &logBuffer{}, sessions: map[uint64]func() (websocket.StatusCode, string){}}
	cfg.Addr = "127.0.0.1:0"
	cfg.IncludeLoopback = true
	cfg.Logger = slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srv, err := New(t.Context(), cfg, r.ctl, r.hub)
	if err != nil {
		t.Fatalf("New: %+v", err)
	}
	r.srv = srv
	r.http = httptest.NewServer(http.HandlerFunc(r.handle))
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("log of the media server:\n%s", r.logs.String())
		}
	})
	t.Cleanup(r.close)
	return r
}

// handle is the handler of the signalling socket: ?call=&user=&caller=&callee=
// and, when the call is held by another process, &home=.
func (r *rig) handle(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	num := func(name string) uint64 {
		n, _ := strconv.ParseUint(q.Get(name), 10, 64)
		return n
	}
	conn, err := websocket.Accept(w, req, nil)
	if err != nil {
		return
	}
	user := num("user")
	r.srv.Serve(context.WithoutCancel(req.Context()), conn, Join{
		CallID: num("call"), ConversationID: 7, CallerID: num("caller"), CalleeID: num("callee"), UserID: user,
		Home: q.Get("home"),
		Session: func(context.Context) (websocket.StatusCode, string) {
			r.smu.Lock()
			check := r.sessions[user]
			r.smu.Unlock()
			if check == nil {
				return 0, ""
			}
			return check()
		},
	})
}

// close stops the HTTP server and the media server.
func (r *rig) close() {
	if r.closed {
		return
	}
	r.closed = true
	r.http.CloseClientConnections()
	r.http.Close()
	if err := r.srv.Close(); err != nil {
		r.t.Errorf("Close: %+v", err)
	}
	if r.ownsHub {
		r.hub.Close()
	}
}

// url is the signalling URL of a user in a call between caller and callee.
func (r *rig) url(call, caller, callee, user uint64) string {
	return "ws" + strings.TrimPrefix(r.http.URL, "http") +
		fmt.Sprintf("/?call=%d&caller=%d&callee=%d&user=%d", call, caller, callee, user)
}

// urlHeldBy is url for a call whose home is another process.
func (r *rig) urlHeldBy(home string, call, caller, callee, user uint64) string {
	return r.url(call, caller, callee, user) + "&home=" + home
}

// dial connects a client of callstest to the signalling socket at url.
func (r *rig) dial(name, url string, opts callstest.Options) *callstest.Client {
	r.t.Helper()
	return callstest.MustDial(r.t, name, url, opts)
}
