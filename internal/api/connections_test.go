package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/clowder/internal/markdown"
	"github.com/rm4n0s/clowder/internal/store"
)

// waitOpen waits until the user holds n live connections.
func (e *testEnv) waitOpen(userID uint64, n int) {
	e.t.Helper()
	deadline := time.Now().Add(wsWait)
	for e.conns.Open(userID) != n {
		if time.Now().After(deadline) {
			e.t.Fatalf("user %d holds %d connections, want %d", userID, e.conns.Open(userID), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// dialRefused dials the WebSocket and fails the test unless the handshake is
// answered with the status.
func (w *wsForum) dialRefused(token string, status int) *http.Response {
	w.t.Helper()
	conn, resp, err := w.dialWith(http.Header{"Authorization": {"Bearer " + token}})
	if err == nil {
		_ = conn.CloseNow()
		w.t.Fatalf("the handshake was accepted, want %d", status)
	}
	if resp == nil || resp.StatusCode != status {
		w.t.Fatalf("the handshake answered %v (%v), want %d", resp, err, status)
	}
	return resp
}

// TestWSConnectionsAreLimited walks the limit of 30 live connections per user
// (SPEC D62) on the user's WebSocket: the 30th is accepted, the 31st is 429
// with the error body of the API and Retry-After before the upgrade, one closed
// makes room for one more, another user is not affected, and when every client
// has gone, with or without a close handshake, nothing is left counted.
func TestWSConnectionsAreLimited(t *testing.T) {
	t.Parallel()
	w := newWSForum(t)
	alice, bob := w.userID("alice"), w.userID("bob")
	var clients []*wsClient
	for i := range app.MaxConnectionsPerUser {
		c := w.dial("alice")
		clients = append(clients, c)
		if i == 0 {
			w.waitOpen(alice, 1)
		}
	}
	w.waitOpen(alice, app.MaxConnectionsPerUser)

	resp := w.dialRefused(w.as("alice"), http.StatusTooManyRequests)
	if got := resp.Header.Get("Retry-After"); got != "30" {
		t.Errorf("Retry-After = %q, want 30", got)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want the JSON error", got)
	}
	_ = resp.Body.Close()
	if got := w.conns.Open(alice); got != app.MaxConnectionsPerUser {
		t.Errorf("a refusal was counted: %d", got)
	}

	// Another user is not affected.
	b := w.dial("bob")
	w.waitOpen(bob, 1)

	// One closed makes room for exactly one more, and not for two.
	if err := clients[0].conn.Close(websocket.StatusNormalClosure, "bye"); err != nil {
		t.Logf("close: %v", err)
	}
	w.waitOpen(alice, app.MaxConnectionsPerUser-1)
	again := w.dial("alice")
	w.waitOpen(alice, app.MaxConnectionsPerUser)
	_ = w.dialRefused(w.as("alice"), http.StatusTooManyRequests).Body.Close()

	// The REST API of a user at the limit still works.
	rec := w.call("alice", http.MethodGet, "/api/v1/me", nil)
	requireStatus(t, rec, http.StatusOK)

	// The clients vanish without a close handshake.
	for _, c := range clients[1:] {
		_ = c.conn.CloseNow()
	}
	_ = again.conn.CloseNow()
	_ = b.conn.CloseNow()
	w.waitOpen(alice, 0)
	w.waitOpen(bob, 0)
	if got := w.conns.Users(); got != 0 {
		t.Errorf("%d users still hold connections", got)
	}
}

// TestCallSocketIsLimitedAndGivesItsPlaceBack shows that the signalling socket
// of a call is among the connections of the user: at the limit the handshake
// is 429 before the call is looked at, and a socket that fails before it is
// accepted (no call, calls off) gives its place back.
func TestCallSocketIsLimitedAndGivesItsPlaceBack(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	alice := f.userID("alice")
	conv := f.convs("alice", "bob")
	socket := path("/conversations/%d/call/ws", conv)

	rec := f.call("alice", http.MethodGet, socket, nil)
	requireStatus(t, rec, http.StatusConflict)
	if got := f.conns.Open(alice); got != 0 {
		t.Fatalf("a socket that failed stays counted: %d", got)
	}

	var releases []func()
	for range app.MaxConnectionsPerUser {
		release, err := f.conns.Acquire(alice)
		if err != nil {
			t.Fatalf("Acquire: %+v", err)
		}
		releases = append(releases, release)
	}
	rec = f.call("alice", http.MethodGet, socket, nil)
	requireStatus(t, rec, http.StatusTooManyRequests)
	if got := errorCode(t, rec); got != "rate_limited" {
		t.Errorf("code = %q, want rate_limited", got)
	}
	if got := rec.Header().Get("Retry-After"); got != "30" {
		t.Errorf("Retry-After = %q, want 30", got)
	}
	// Bob has none of alice's connections: his answer is the conversation's.
	requireStatus(t, f.call("bob", http.MethodGet, socket, nil), http.StatusConflict)

	releases[0]()
	requireStatus(t, f.call("alice", http.MethodGet, socket, nil), http.StatusConflict)
	if got := f.conns.Open(alice); got != app.MaxConnectionsPerUser-1 {
		t.Errorf("Open = %d, want %d: the failed socket kept its place", got, app.MaxConnectionsPerUser-1)
	}
	for _, release := range releases[1:] {
		release()
	}
	if got := f.conns.Users(); got != 0 {
		t.Errorf("%d users still hold connections", got)
	}
}

// TestSignupLimitOnTheAPI walks the limit on sign-ups with an invitation that
// is not valid (SPEC D62) on POST /auth/signup: an unknown, a used and an
// expired token each count as a failed login of the address, after five the
// route and /auth/login answer 429 with Retry-After, and a valid token does
// not count.
func TestSignupLimitOnTheAPI(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", store.RoleMember)
	used := e.invitation(store.RoleMember)
	if _, err := e.accounts.SignUp(t.Context(), used, "first", "First", testPassword); err != nil {
		t.Fatal(err)
	}
	expired := e.invitation(store.RoleMember)
	e.clk.Advance(8 * 24 * time.Hour)
	valid := e.invitation(store.RoleMember)
	signup := func(token, username, addr string) *httptest.ResponseRecorder {
		body := map[string]any{"invitation_token": token, "username": username, "display_name": "Name",
			"password": "a password of 12+", "client_name": "Clowder Desktop"}
		return e.send(request(http.MethodPost, "/api/v1/auth/signup", body, fromAddr(addr)))
	}
	const attacker = "203.0.113.60:5000"

	// Mistakes in the form of a valid invitation do not count.
	for range 8 {
		rec := signup(valid, "N!", attacker)
		requireStatus(t, rec, http.StatusUnprocessableEntity)
	}
	for i, token := range []string{"no-such-token", used, expired, "another", used} {
		rec := signup(token, "mallory", attacker)
		requireStatus(t, rec, http.StatusNotFound)
		if i < 4 {
			rec := e.send(request(http.MethodPost, "/api/v1/auth/login", map[string]any{
				"username": "alice", "password": testPassword, "client_name": "Clowder Desktop"}, fromAddr("203.0.113.61:1")))
			requireStatus(t, rec, http.StatusOK)
		}
	}
	rec := signup(valid, "dana", attacker)
	requireStatus(t, rec, http.StatusTooManyRequests)
	if got := errorCode(t, rec); got != "rate_limited" {
		t.Errorf("code = %q, want rate_limited", got)
	}
	if got := rec.Header().Get("Retry-After"); got != "900" {
		t.Errorf("Retry-After = %q, want 900", got)
	}
	rec = e.send(request(http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "alice", "password": testPassword, "client_name": "Clowder Desktop"}, fromAddr(attacker)))
	requireStatus(t, rec, http.StatusTooManyRequests)
	// Another address signs up with the valid token, and the window passes.
	requireStatus(t, signup(valid, "dana", "203.0.113.62:1"), http.StatusCreated)
	e.clk.Advance(app.LoginWindow)
	requireStatus(t, signup("no-such-token", "mallory", attacker), http.StatusNotFound)
}

// TestUploadStagedLimit walks the limit on what a user has staged (SPEC D62)
// on POST /uploads: ten times the attachment limit is accepted to the byte,
// one byte more is 413 too_large with a message about posting the files first
// and leaves nothing on the disk, another user is not affected, and posting
// the files frees their bytes.
func TestUploadStagedLimit(t *testing.T) {
	t.Parallel()
	const fileLimit = 100
	f := newForum(t)
	fs, err := files.New(f.filesDir)
	if err != nil {
		t.Fatal(err)
	}
	f.remake(func(o *Options) {
		o.MaxUploadSize = fileLimit
		o.Services.Posts = app.NewPosts(f.st, f.clk, markdown.New(), fs,
			app.PostsConfig{MaxAttachmentSize: fileLimit, MaxAttachments: 4}, f.hub)
	})
	full := strings.Repeat("x", fileLimit)
	var ids []string
	for range 9 {
		ids = append(ids, f.uploadID("bob", "a.bin", full))
	}
	ids = append(ids, f.uploadID("bob", "b.bin", full[:fileLimit-1]))
	// One byte reaches the limit exactly.
	ids = append(ids, f.uploadID("bob", "c.bin", "y"))
	disk, notes := len(f.onDisk()), len(f.notes())

	rec := f.upload("bob", "d.bin", "z")
	requireStatus(t, rec, http.StatusRequestEntityTooLarge)
	if got := errorCode(t, rec); got != "too_large" {
		t.Errorf("code = %q, want too_large", got)
	}
	if msg := decode(t, rec)["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "Post them") {
		t.Errorf("message = %q, want it to say to post the staged files", msg)
	}
	if got, got2 := len(f.onDisk()), len(f.notes()); got != disk || got2 != notes {
		t.Errorf("a refused upload left files: %d and %d notes, want %d and %d", got, got2, disk, notes)
	}
	// Another user is not affected.
	requireStatus(t, f.upload("carol", "d.bin", full), http.StatusCreated)

	// Posting frees the bytes of the files the post takes.
	post := f.call("bob", http.MethodPost, path("/rooms/%d/posts", f.lobby.ID), map[string]any{
		"body": "files", "upload_ids": ids[:2],
	})
	requireStatus(t, post, http.StatusCreated)
	requireStatus(t, f.upload("bob", "e.bin", full), http.StatusCreated)
	requireStatus(t, f.upload("bob", "f.bin", full), http.StatusCreated)
	requireStatus(t, f.upload("bob", "g.bin", "z"), http.StatusRequestEntityTooLarge)
}
