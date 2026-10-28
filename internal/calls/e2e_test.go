package calls_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/rm4n0s/clowder/internal/api"
	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/calls"
	"github.com/rm4n0s/clowder/internal/calls/callstest"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/cluster"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/markdown"
	"github.com/rm4n0s/clowder/internal/store/storetest"
	"github.com/rm4n0s/clowder/internal/web"
)

// These tests join the pieces of step 22.1: the real services on a database,
// the real handlers of the pages and of the API behind an httptest server, the
// real media server on loopback and pion clients that take part in the calls
// through the two signalling sockets.

const (
	password    = "correct horse battery"
	sessionName = "clowder_session"
)

var epoch = time.Date(2026, 10, 26, 9, 30, 0, 0, time.UTC)

// controller gives the media server what it asks of the service, as
// cmd/clowder does.
type controller struct{ svc *app.Calls }

func (c controller) Status(ctx context.Context, id uint64) (calls.Status, error) {
	st, err := c.svc.Status(ctx, id)
	if err != nil {
		return calls.Status{}, err
	}
	return calls.Status{Open: st.Open, Active: st.Active, Reason: string(st.Reason)}, nil
}

func (c controller) Fail(ctx context.Context, id uint64) error { return c.svc.Fail(ctx, id) }

func (c controller) SetMedia(ctx context.Context, id, user uint64, mic, cam bool) error {
	return c.svc.SetMedia(ctx, app.Actor{UserID: user}, id, mic, cam)
}

func (c controller) Leave(ctx context.Context, id, user uint64) error {
	return c.svc.Leave(ctx, app.Actor{UserID: user}, id)
}

// stack is a whole Clowder with calls: services, handlers, media server. Two
// stacks on one database and one Redis are the two processes of a cluster
// (cluster_test.go).
type stack struct {
	t        *testing.T
	clk      *clock.Fake
	st       *storetest.Store
	hub      *live.Hub
	accounts *app.Accounts
	msgs     *app.Messages
	calls    *app.Calls
	media    *calls.Server
	link     *cluster.CallLink
	ts       *httptest.Server
	id       string
	users    map[string]app.User
	api      map[string]string
	cookies  map[string]string
}

// stackOptions choose how newStackWith builds a stack.
type stackOptions struct {
	// mediaCfg changes the configuration of the media server.
	mediaCfg func(*calls.Config)
	// logger receives the logs of the process; nil discards them.
	logger *slog.Logger
	// processID is the ID of the process; empty takes a fixed one.
	processID string
	// first is another process of the same cluster: the new stack shares its
	// database and its users, and makes services, media server and handlers of
	// its own.
	first *stack
	// redisURL, when set, makes the process a part of a cluster: its events go
	// through the Redis bridge and its calls through the link (the relay, the
	// key of the process and the check whether another is alive).
	redisURL string
	// link changes the configuration of the link of the process.
	link func(*cluster.CallLinkConfig)
	// orphanGrace is CallsConfig.OrphanGrace of the process (zero: the default).
	orphanGrace time.Duration
	// directory, when set, wraps what the process asks about the other
	// processes, so that a test can make Redis lose every key.
	directory func(app.ProcessDirectory) app.ProcessDirectory
	// silent keeps the process from announcing itself in Redis, as one that
	// died without a trace.
	silent bool
}

// newStack starts everything. mediaCfg changes the configuration of the media
// server.
func newStack(t *testing.T, mediaCfg func(*calls.Config)) *stack {
	t.Helper()
	return newStackWith(t, stackOptions{mediaCfg: mediaCfg})
}

// newStackWith starts everything as opts say.
func newStackWith(t *testing.T, opts stackOptions) *stack {
	t.Helper()
	logger := opts.logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	processID := opts.processID
	if processID == "" {
		processID = "0123456789abcdef"
	}
	var (
		clk *clock.Fake
		st  *storetest.Store
	)
	if opts.first != nil {
		clk, st = opts.first.clk, opts.first.st
	} else {
		clk = clock.NewFake(epoch)
		st = storetest.Open(t, clk)
	}
	hub := live.New()
	t.Cleanup(hub.Close)
	var pub app.Publisher = hub
	var link *cluster.CallLink
	if opts.redisURL != "" {
		bridge, err := cluster.NewBridge(t.Context(), opts.redisURL, hub, logger)
		if err != nil {
			t.Fatalf("NewBridge: %+v", err)
		}
		t.Cleanup(func() {
			if err := bridge.Close(); err != nil {
				t.Errorf("bridge.Close: %+v", err)
			}
		})
		pub = bridge
		linkCfg := cluster.CallLinkConfig{RedisURL: opts.redisURL, ProcessID: processID, Logger: logger}
		if opts.link != nil {
			opts.link(&linkCfg)
		}
		link, err = cluster.NewCallLink(t.Context(), linkCfg)
		if err != nil {
			t.Fatalf("NewCallLink: %+v", err)
		}
		t.Cleanup(func() {
			if err := link.Close(); err != nil {
				t.Errorf("link.Close: %+v", err)
			}
		})
	}
	avatars, err := files.New(filepath.Join(t.TempDir(), "avatars"))
	if err != nil {
		t.Fatalf("files.New: %+v", err)
	}
	fileStore, err := files.New(filepath.Join(t.TempDir(), "files"))
	if err != nil {
		t.Fatalf("files.New: %+v", err)
	}
	accounts := app.NewAccounts(st, clk, auth.FastHasher(), app.AccountsConfig{
		InvitationLifetime: 7 * 24 * time.Hour, SessionLifetime: 30 * 24 * time.Hour, Publisher: pub, Avatars: avatars,
	})
	msgs := app.NewMessages(st, clk, fileStore, app.MessagesConfig{MaxAttachmentSize: 1 << 20, MaxAttachments: 4}, pub)
	callsCfg := app.CallsConfig{ProcessID: processID, Enabled: true, Logger: logger, OrphanGrace: opts.orphanGrace}
	if link != nil {
		callsCfg.Processes = link
		if opts.directory != nil {
			callsCfg.Processes = opts.directory(link)
		}
	}
	callSvc := app.NewCalls(st, clk, callsCfg, pub)
	rooms := app.NewRooms(st, fileStore, logger, pub)
	posts := app.NewPosts(st, clk, markdown.New(), fileStore, app.PostsConfig{MaxAttachmentSize: 1 << 20, MaxAttachments: 4, Logger: logger}, pub)
	board := app.NewBoard(st, clk, pub)
	search := app.NewSearch(st)
	audit := app.NewAudit(st)
	notifs := app.NewNotifications(st, pub)
	prefs := app.NewPrefs(st, pub)
	keys := app.NewSSHKeys(st, clk, accounts, app.SSHServerInfo{})
	cfg := calls.Config{Addr: "127.0.0.1:0", IncludeLoopback: true, Logger: logger}
	if link != nil {
		cfg.ProcessID, cfg.Relay = processID, link
	}
	if opts.mediaCfg != nil {
		opts.mediaCfg(&cfg)
	}
	media, err := calls.New(t.Context(), cfg, controller{svc: callSvc}, hub)
	if err != nil {
		t.Fatalf("calls.New: %+v", err)
	}
	t.Cleanup(func() {
		if err := media.Close(); err != nil {
			t.Errorf("media.Close: %+v", err)
		}
	})
	if link != nil && !opts.silent {
		udp, _ := media.Addr()
		if err := link.Announce(t.Context(), udp.String()); err != nil {
			t.Fatalf("Announce: %+v", err)
		}
	}
	apiServer, err := api.New(api.Options{
		Services: api.Services{Accounts: accounts, Notifications: notifs, Messages: msgs, Rooms: rooms, Posts: posts, Board: board,
			Search: search, Audit: audit, Prefs: prefs, SSHKeys: keys, Calls: callSvc, Media: media, Hub: hub},
		Logger:        logger,
		MaxUploadSize: 1 << 20,
	})
	if err != nil {
		t.Fatalf("api.New: %+v", err)
	}
	webServer, err := web.New(config.Default(), logger, web.Services{
		Accounts: accounts, Rooms: rooms, Audit: audit, Posts: posts, Board: board, Search: search, Notifications: notifs,
		Prefs: prefs, Messages: msgs, SSHKeys: keys, Calls: callSvc, Media: media, Health: app.NewHealth(st), API: apiServer, Hub: hub,
	})
	if err != nil {
		t.Fatalf("web.New: %+v", err)
	}
	ts := httptest.NewServer(webServer.Handler())
	t.Cleanup(func() {
		ts.CloseClientConnections()
		ts.Close()
	})
	s := &stack{t: t, clk: clk, st: st, hub: hub, accounts: accounts, msgs: msgs, calls: callSvc, media: media, link: link, ts: ts, id: processID,
		users: map[string]app.User{}, api: map[string]string{}, cookies: map[string]string{}}
	if opts.first != nil {
		s.users, s.api, s.cookies = opts.first.users, opts.first.api, opts.first.cookies
		return s
	}
	for _, name := range []string{"admin", "alice", "bob", "carol"} {
		// The roles are untyped constants: this package never imports the store.
		inv, err := accounts.CreateSystemInvitation(t.Context(), "member")
		if name == "admin" {
			inv, err = accounts.CreateSystemInvitation(t.Context(), "admin")
		}
		if err != nil {
			t.Fatalf("CreateSystemInvitation: %+v", err)
		}
		u, err := accounts.SignUp(t.Context(), inv.Token, name, strings.ToUpper(name[:1])+name[1:], password)
		if err != nil {
			t.Fatalf("SignUp %s: %+v", name, err)
		}
		s.users[name] = u
		res, err := accounts.LoginClient(t.Context(), app.ClientLogin{Username: name, Password: password, ClientIP: "192.0.2.10", Kind: app.SessionAPI, ClientName: "e2e"})
		if err != nil {
			t.Fatalf("LoginClient %s: %+v", name, err)
		}
		s.api[name] = res.Token
		res, err = accounts.Login(t.Context(), name, password, "192.0.2.11")
		if err != nil {
			t.Fatalf("Login %s: %+v", name, err)
		}
		s.cookies[name] = res.Token
	}
	return s
}

func (s *stack) actor(name string) app.Actor {
	u := s.users[name]
	return app.Actor{UserID: u.ID, Username: u.Username, Role: u.Role}
}

// direct makes the direct conversation of two users.
func (s *stack) direct(a, b string) uint64 {
	s.t.Helper()
	id, err := s.msgs.StartDirect(s.t.Context(), s.actor(a), s.users[b].ID)
	if err != nil {
		s.t.Fatalf("StartDirect: %+v", err)
	}
	return id
}

// request sends a request to the API as the user and returns the status and
// the body.
func (s *stack) request(user, method, path string, body any) (int, []byte) {
	s.t.Helper()
	var rd io.Reader = http.NoBody
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			s.t.Fatal(err)
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(s.t.Context(), method, s.ts.URL+"/api/v1"+path, rd)
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+s.api[user])
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		s.t.Fatal(err)
	}
	return res.StatusCode, data
}

// apiSocket is the URL of the signalling socket of the API for a conversation.
func (s *stack) apiSocket(conv uint64) string {
	return "ws" + strings.TrimPrefix(s.ts.URL, "http") + "/api/v1/conversations/" + strconv.FormatUint(conv, 10) + "/call/ws"
}

// pageSocket is the URL of the signalling socket of the pages.
func (s *stack) pageSocket(conv uint64) string {
	return "ws" + strings.TrimPrefix(s.ts.URL, "http") + "/calls/" + strconv.FormatUint(conv, 10) + "/ws"
}

// asAPI are the options of a client that logs in with the bearer token.
func (s *stack) asAPI(user string, o callstest.Options) callstest.Options {
	o.Header = http.Header{"Authorization": {"Bearer " + s.api[user]}}
	return o
}

// asPage are the options of a client that logs in with the session cookie.
func (s *stack) asPage(user string, o callstest.Options) callstest.Options {
	o.Header = http.Header{"Cookie": {sessionName + "=" + s.cookies[user]}}
	return o
}

// callJSON is the part of a call that the tests read.
type callJSON struct {
	ID       uint64  `json:"id"`
	State    string  `json:"state"`
	Video    bool    `json:"video"`
	Incoming bool    `json:"incoming"`
	Reason   *string `json:"end_reason"`
}

func decodeCall(t *testing.T, data []byte) callJSON {
	t.Helper()
	var c callJSON
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("not a call: %v: %s", err, data)
	}
	return c
}

// historyEntry is a message of the history as the tests read it.
type historyEntry struct {
	Kind string `json:"kind"`
	Call *struct {
		Outcome  string `json:"outcome"`
		Video    bool   `json:"video"`
		Duration int    `json:"duration_seconds"`
		Text     string `json:"text"`
	} `json:"call"`
}

// entries returns the call entries of the conversation as the user reads them
// through the API.
func (s *stack) entries(user string, conv uint64) []historyEntry {
	s.t.Helper()
	status, data := s.request(user, http.MethodGet, "/conversations/"+strconv.FormatUint(conv, 10)+"/messages", nil)
	if status != http.StatusOK {
		s.t.Fatalf("history: %d %s", status, data)
	}
	var page struct {
		Messages []historyEntry `json:"messages"`
	}
	if err := json.Unmarshal(data, &page); err != nil {
		s.t.Fatalf("history: %v: %s", err, data)
	}
	var out []historyEntry
	for _, m := range page.Messages {
		if m.Kind == "call" {
			out = append(out, m)
		}
	}
	return out
}

// waitEnded waits for the ended message of the client.
func waitEnded(t *testing.T, c *callstest.Client, want string) {
	t.Helper()
	select {
	case reason := <-c.Ended():
		if reason != want {
			t.Errorf("%s: call ended with %q, want %q", c.Name(), reason, want)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("%s: no ended message", c.Name())
	}
}

// TestACallThroughTheAPI is the whole path of a desktop client: start, ring,
// answer, the signalling socket of the API, media in both directions, hang up
// and the entry in the history.
func TestACallThroughTheAPI(t *testing.T) {
	s := newStack(t, nil)
	conv := s.direct("alice", "bob")
	path := "/conversations/" + strconv.FormatUint(conv, 10) + "/call"

	status, data := s.request("alice", http.MethodPost, path, map[string]any{"video": true})
	if status != http.StatusCreated {
		t.Fatalf("start: %d %s", status, data)
	}
	if c := decodeCall(t, data); c.State != "ringing" || !c.Video || c.Incoming {
		t.Errorf("started call = %+v", c)
	}
	status, data = s.request("bob", http.MethodGet, path, nil)
	if c := decodeCall(t, data); status != http.StatusOK || c.State != "ringing" || !c.Incoming {
		t.Errorf("bob sees %d %s", status, data)
	}
	// The callee cannot join before answering.
	if _, resp, err := callstest.Dial(t, "bob early", s.apiSocket(conv), s.asAPI("bob", callstest.Options{})); err == nil || resp == nil || resp.StatusCode != http.StatusConflict {
		t.Errorf("bob joined a ringing call: err=%v resp=%v", err, resp)
	}
	status, data = s.request("bob", http.MethodPost, path+"/answer", map[string]any{"video": true})
	if status != http.StatusOK || decodeCall(t, data).State != "active" {
		t.Fatalf("answer: %d %s", status, data)
	}

	alice := callstest.MustDial(t, "alice", s.apiSocket(conv), s.asAPI("alice", callstest.Options{}))
	bob := callstest.MustDial(t, "bob", s.apiSocket(conv), s.asAPI("bob", callstest.Options{}))
	for _, c := range []*callstest.Client{alice, bob} {
		select {
		case <-c.Connected():
		case <-time.After(20 * time.Second):
			t.Fatalf("%s did not connect", c.Name())
		}
	}
	alice.Receiving(t, 50, 20*time.Second)
	bob.Receiving(t, 50, 20*time.Second)
	t.Logf("through the API: alice got %d audio and %d video packets, bob got %d and %d",
		alice.AudioIn(), alice.VideoIn(), bob.AudioIn(), bob.VideoIn())

	// While the call is on there is no entry.
	if got := s.entries("alice", conv); len(got) != 0 {
		t.Errorf("entries during the call = %+v", got)
	}
	s.clk.Advance(95 * time.Second)
	status, data = s.request("bob", http.MethodPost, path+"/hangup", nil)
	if status != http.StatusOK || decodeCall(t, data).State != "ended" {
		t.Fatalf("hangup: %d %s", status, data)
	}
	waitEnded(t, alice, "hangup")
	waitEnded(t, bob, "hangup")
	callstest.WaitFor(t, "the sockets to close", 10*time.Second, func() bool { return alice.CloseCode() != 0 && bob.CloseCode() != 0 })

	entries := s.entries("alice", conv)
	if len(entries) != 1 || entries[0].Call == nil || entries[0].Call.Outcome != "completed" || !entries[0].Call.Video || entries[0].Call.Duration != 95 {
		t.Fatalf("entries = %+v", entries)
	}
	t.Logf("the entry in alice's history: %q", entries[0].Call.Text)
	if status, _ := s.request("alice", http.MethodGet, path, nil); status != http.StatusNotFound {
		t.Errorf("the call is over, GET status = %d, want 404", status)
	}
	if s.media.Calls() != 0 {
		t.Errorf("the media server holds %d calls", s.media.Calls())
	}
}

// TestACallThroughThePages is the same with the cookie of a browser, and what
// the page socket refuses.
func TestACallThroughThePages(t *testing.T) {
	s := newStack(t, nil)
	conv := s.direct("alice", "bob")
	trio, err := s.msgs.StartGroup(t.Context(), s.actor("alice"), []uint64{s.users["bob"].ID, s.users["carol"].ID}, "trio")
	if err != nil {
		t.Fatalf("StartGroup: %+v", err)
	}
	call, err := s.calls.Start(t.Context(), s.actor("alice"), conv, false)
	if err != nil {
		t.Fatalf("Start: %+v", err)
	}

	refused := []struct {
		name   string
		url    string
		header http.Header
		origin string
		want   int
	}{
		{"a stranger", s.pageSocket(conv), s.asPage("carol", callstest.Options{}).Header, "", http.StatusNotFound},
		{"a group", s.pageSocket(trio), s.asPage("alice", callstest.Options{}).Header, "", http.StatusConflict},
		{"the callee before answering", s.pageSocket(conv), s.asPage("bob", callstest.Options{}).Header, "", http.StatusConflict},
		{"a page of another site", s.pageSocket(conv), s.asPage("alice", callstest.Options{}).Header, "https://evil.example", http.StatusForbidden},
		{"a conversation that does not exist", s.pageSocket(9999), s.asPage("alice", callstest.Options{}).Header, "", http.StatusNotFound},
	}
	for _, tt := range refused {
		_, resp, err := callstest.Dial(t, tt.name, tt.url, callstest.Options{Header: tt.header, Origin: tt.origin})
		if err == nil || resp == nil || resp.StatusCode != tt.want {
			t.Errorf("%s: err=%v resp=%v, want status %d", tt.name, err, resp, tt.want)
		}
	}
	// No cookie: the page is not for strangers.
	if _, resp, err := callstest.Dial(t, "nobody", s.pageSocket(conv), callstest.Options{}); err == nil || resp == nil || resp.StatusCode == http.StatusSwitchingProtocols {
		t.Errorf("a socket without a session opened: err=%v", err)
	}
	// The bearer token is no cookie.
	if _, resp, err := callstest.Dial(t, "token", s.pageSocket(conv), s.asAPI("alice", callstest.Options{})); err == nil || resp == nil || resp.StatusCode == http.StatusSwitchingProtocols {
		t.Errorf("a bearer token opened the socket of the pages: err=%v", err)
	}

	if _, err := s.calls.Answer(t.Context(), s.actor("bob"), call.ID, false); err != nil {
		t.Fatalf("Answer: %+v", err)
	}
	// An audio call: both clients send no video.
	alice := callstest.MustDial(t, "alice", s.pageSocket(conv), s.asPage("alice", callstest.Options{NoVideo: true}))
	bob := callstest.MustDial(t, "bob", s.pageSocket(conv), s.asPage("bob", callstest.Options{NoVideo: true}))
	alice.ReceivingAudio(t, 50, 20*time.Second)
	bob.ReceivingAudio(t, 50, 20*time.Second)
	t.Logf("through the pages: alice got %d audio packets, bob got %d", alice.AudioIn(), bob.AudioIn())

	// Alice's other tab replaces her socket.
	other := callstest.MustDial(t, "alice's other tab", s.pageSocket(conv), s.asPage("alice", callstest.Options{NoVideo: true}))
	callstest.WaitFor(t, "the first tab to be told", 10*time.Second, func() bool { return alice.CloseCode() == 4003 })
	other.ReceivingAudio(t, 20, 20*time.Second)

	// The microphone and camera of a participant reach the other one.
	other.Send(map[string]any{"type": "state", "microphone": false, "camera": false})
	callstest.WaitFor(t, "bob to hear of alice's microphone", 10*time.Second, func() bool {
		states := bob.PeerStates()
		return len(states) > 0 && states[len(states)-1].Microphone != nil && !*states[len(states)-1].Microphone
	})
	if _, err := s.calls.HangUp(t.Context(), s.actor("alice"), call.ID); err != nil {
		t.Fatalf("HangUp: %+v", err)
	}
	waitEnded(t, bob, "hangup")
	waitEnded(t, other, "hangup")
}

// TestACallThatNobodyAnswers: the callee never answers, the time passes and the
// call is missed, with its entry for both.
func TestACallThatNobodyAnswers(t *testing.T) {
	s := newStack(t, nil)
	conv := s.direct("alice", "bob")
	path := "/conversations/" + strconv.FormatUint(conv, 10) + "/call"
	if status, data := s.request("alice", http.MethodPost, path, map[string]any{"video": false}); status != http.StatusCreated {
		t.Fatalf("start: %d %s", status, data)
	}
	// Busy: the callee is in a call, and so is the caller.
	other := s.direct("bob", "carol")
	if status, data := s.request("carol", http.MethodPost, "/conversations/"+strconv.FormatUint(other, 10)+"/call", map[string]any{"video": false}); status != http.StatusConflict {
		t.Errorf("a call to someone who is called: %d %s", status, data)
	}
	if n, err := s.calls.Expire(t.Context()); err != nil || n != 0 {
		t.Fatalf("Expire at once = %d, %v", n, err)
	}
	s.clk.Advance(46 * time.Second)
	if n, err := s.calls.Expire(t.Context()); err != nil || n != 1 {
		t.Fatalf("Expire after 46 s = %d, %v", n, err)
	}
	for _, user := range []string{"alice", "bob"} {
		entries := s.entries(user, conv)
		if len(entries) != 1 || entries[0].Call.Outcome != "missed" {
			t.Errorf("%s entries = %+v", user, entries)
		}
	}
	// The line is free again.
	if status, data := s.request("alice", http.MethodPost, path, map[string]any{"video": false}); status != http.StatusCreated {
		t.Errorf("a new call: %d %s", status, data)
	}
}

// TestACallEndsWhenAnAccountChanges: disabling a user, or ending a session,
// ends the call and closes the sockets.
func TestACallEndsWhenAnAccountChanges(t *testing.T) {
	tests := []struct {
		name   string
		end    func(t *testing.T, s *stack)
		reason string
	}{
		{"the callee is disabled", func(t *testing.T, s *stack) {
			if err := s.accounts.SetDisabled(t.Context(), s.actor("admin"), s.users["bob"].ID, true); err != nil {
				t.Fatalf("SetDisabled: %+v", err)
			}
		}, "disabled"},
		{"the caller logs out", func(t *testing.T, s *stack) {
			if err := s.accounts.Logout(t.Context(), s.cookies["alice"]); err != nil {
				t.Fatalf("Logout: %+v", err)
			}
		}, "session_ended"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newStack(t, nil)
			conv := s.direct("alice", "bob")
			call, err := s.calls.Start(t.Context(), s.actor("alice"), conv, true)
			if err != nil {
				t.Fatalf("Start: %+v", err)
			}
			if _, err := s.calls.Answer(t.Context(), s.actor("bob"), call.ID, true); err != nil {
				t.Fatalf("Answer: %+v", err)
			}
			alice := callstest.MustDial(t, "alice", s.apiSocket(conv), s.asAPI("alice", callstest.Options{}))
			bob := callstest.MustDial(t, "bob", s.pageSocket(conv), s.asPage("bob", callstest.Options{}))
			alice.Receiving(t, 20, 20*time.Second)
			bob.Receiving(t, 20, 20*time.Second)
			tt.end(t, s)
			waitEnded(t, alice, tt.reason)
			waitEnded(t, bob, tt.reason)
			callstest.WaitFor(t, "alice's socket to close", 10*time.Second, func() bool { return alice.CloseCode() != 0 })
			if entries := s.entries("alice", conv); len(entries) != 1 {
				t.Errorf("entries = %+v", entries)
			}
		})
	}
}

// TestScreenShareAndTCP: a participant replaces the camera by a screen with no
// new offer, and a pair that can use TCP only still talks.
func TestScreenShareAndTCP(t *testing.T) {
	tests := []struct {
		name     string
		networks []webrtc.NetworkType
		share    bool
	}{
		{"screen share", nil, true},
		{"ICE-TCP only", []webrtc.NetworkType{webrtc.NetworkTypeTCP4}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newStack(t, nil)
			conv := s.direct("alice", "bob")
			call, err := s.calls.Start(t.Context(), s.actor("alice"), conv, true)
			if err != nil {
				t.Fatalf("Start: %+v", err)
			}
			if _, err := s.calls.Answer(t.Context(), s.actor("bob"), call.ID, true); err != nil {
				t.Fatalf("Answer: %+v", err)
			}
			alice := callstest.MustDial(t, "alice", s.apiSocket(conv), s.asAPI("alice", callstest.Options{Networks: tt.networks}))
			bob := callstest.MustDial(t, "bob", s.pageSocket(conv), s.asPage("bob", callstest.Options{Networks: tt.networks}))
			alice.Receiving(t, 30, 30*time.Second)
			bob.Receiving(t, 30, 30*time.Second)
			t.Logf("%s: alice got %d audio and %d video packets, bob got %d and %d", tt.name, alice.AudioIn(), alice.VideoIn(), bob.AudioIn(), bob.VideoIn())
			if !tt.share {
				return
			}
			offers := alice.Offers()
			if err := alice.ShareScreenRTP(); err != nil {
				t.Fatalf("ShareScreen: %v", err)
			}
			before := bob.VideoIn()
			callstest.WaitFor(t, "bob to go on receiving video", 15*time.Second, func() bool { return bob.VideoIn()-before >= 40 })
			if alice.Offers() != offers {
				t.Errorf("the screen share made %d new offers", alice.Offers()-offers)
			}
			t.Logf("screen share: alice replaced her camera by a screen; bob got %d more video packets, on %d SSRC, and alice saw %d new offers", bob.VideoIn()-before, len(bob.VideoSSRCs()), alice.Offers()-offers)
			if got := len(bob.VideoSSRCs()); got != 1 {
				t.Errorf("bob saw the video on %d SSRCs: %v", got, bob.VideoSSRCs())
			}
		})
	}
}

// TestACalleeWhoNeverOpensTheirSocket: the callee answers but their page never opens
// the socket, so the call fails after the join timeout: the caller is told,
// and the history has the failed call.
func TestACalleeWhoNeverOpensTheirSocket(t *testing.T) {
	s := newStack(t, func(c *calls.Config) { c.JoinTimeout = 1500 * time.Millisecond })
	conv := s.direct("alice", "bob")
	call, err := s.calls.Start(t.Context(), s.actor("alice"), conv, false)
	if err != nil {
		t.Fatalf("Start: %+v", err)
	}
	if _, err := s.calls.Answer(t.Context(), s.actor("bob"), call.ID, false); err != nil {
		t.Fatalf("Answer: %+v", err)
	}
	alice := callstest.MustDial(t, "alice", s.apiSocket(conv), s.asAPI("alice", callstest.Options{NoVideo: true}))
	waitEnded(t, alice, "failed")
	got, err := s.calls.Get(t.Context(), s.actor("bob"), call.ID)
	if err != nil || got.State != "ended" || got.EndReason != "failed" {
		t.Fatalf("call = %+v, %v", got, err)
	}
	callstest.WaitFor(t, "the media server to forget the call", 10*time.Second, func() bool { return s.media.Calls() == 0 })
	// Bob's page now finds no call in progress (409).
	if _, resp, err := callstest.Dial(t, "bob late", s.pageSocket(conv), s.asPage("bob", callstest.Options{})); err == nil || resp == nil || resp.StatusCode != http.StatusConflict {
		t.Errorf("a late socket: err=%v resp=%v, want 409", err, resp)
	}
}
