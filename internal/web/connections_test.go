package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/metrics"
	"github.com/rm4n0s/clowder/internal/web/views"
)

// waitOpen waits until the user holds n live connections.
func (e *testEnv) waitOpen(user app.User, n int) {
	e.t.Helper()
	deadline := time.Now().Add(streamTimeout)
	for e.conns.Open(user.ID) != n {
		if time.Now().After(deadline) {
			e.t.Fatalf("%s holds %d connections, want %d", user.Username, e.conns.Open(user.ID), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitNoConnections waits until nobody holds a live connection: nothing leaked.
func (e *testEnv) waitNoConnections() {
	e.t.Helper()
	deadline := time.Now().Add(streamTimeout)
	for e.conns.Users() != 0 {
		if time.Now().After(deadline) {
			e.t.Fatalf("%d users still hold connections", e.conns.Users())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// streamPaths are streams of three kinds that alice may open.
func (e *roomsEnv) streamPaths() []string {
	return []string{
		e.streamOfRoom("general", ""),
		"/rooms/" + idText(e.general.ID) + "/board/stream",
		views.MeStreamPath,
	}
}

// TestStreamsOfAUserAreLimited walks the limit of 30 live connections per user
// on the event streams (SPEC D62): the 30th is accepted, the 31st of every kind
// of stream is 429 with Retry-After before it starts, one closed makes room for
// one more, another user is not affected, and when everything has closed
// nothing is left counted.
func TestStreamsOfAUserAreLimited(t *testing.T) {
	t.Parallel()
	set := newMetricsSet(t)
	e := newRoomsEnvOn(t, set, nil)
	ss := e.serveStreams()
	paths := e.streamPaths()

	var open []*streamClient
	for i := range app.MaxConnectionsPerUser {
		c := ss.open("alice", paths[i%len(paths)], asDatastar())
		if c.status != http.StatusOK {
			t.Fatalf("stream %d: status = %d, want 200", i+1, c.status)
		}
		open = append(open, c)
	}
	e.waitOpen(e.alice, app.MaxConnectionsPerUser)
	if got := set.SSEStreams.Value(); got != int64(app.MaxConnectionsPerUser) {
		t.Errorf("open streams in the metrics = %d, want %d", got, app.MaxConnectionsPerUser)
	}

	for _, path := range paths {
		c := ss.open("alice", path, asDatastar())
		if c.status != http.StatusTooManyRequests {
			t.Fatalf("GET %s past the limit: status = %d, want 429", path, c.status)
		}
		if got := c.header.Get("Retry-After"); got != strconv.Itoa(app.ConnectionsRetryAfterSeconds) {
			t.Errorf("GET %s: Retry-After = %q, want 30", path, got)
		}
		if !strings.Contains(c.body, messageTooManyConnections) {
			t.Errorf("GET %s: body = %q", path, c.body)
		}
		if strings.HasPrefix(c.header.Get("Content-Type"), "text/event-stream") {
			t.Errorf("GET %s: a refused stream answered an event stream", path)
		}
		if c.header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("GET %s: the security headers are missing from the 429", path)
		}
	}
	if got := e.conns.Open(e.alice.ID); got != app.MaxConnectionsPerUser {
		t.Errorf("a refusal was counted: alice holds %d", got)
	}
	if got := set.LimitsRefused.With(metrics.LimitConnections).Value(); got != uint64(len(paths)) {
		t.Errorf("refusals in the metrics = %d, want %d", got, len(paths))
	}
	if sample(scrape(t, set), `clowder_limits_refused_total{limit="connections"}`) != strconv.Itoa(len(paths)) {
		t.Errorf("the refusals are not in the scrape:\n%s", scrape(t, set))
	}

	// Another user is not affected, and a page that is not a stream is not
	// limited either.
	bobStream := ss.open("bob", paths[0], asDatastar())
	if bobStream.status != http.StatusOK {
		t.Errorf("bob's stream: status = %d, want 200", bobStream.status)
	}
	if rec := e.send(get("/rooms/"+idText(e.general.ID), e.cookies["alice"])); rec.Code != http.StatusOK {
		t.Errorf("a page of alice at the limit: status = %d, want 200", rec.Code)
	}

	// One closed makes room for exactly one more.
	open[0].cancel()
	e.waitOpen(e.alice, app.MaxConnectionsPerUser-1)
	again := ss.open("alice", paths[0], asDatastar())
	if again.status != http.StatusOK {
		t.Fatalf("after one closed: status = %d, want 200", again.status)
	}
	if c := ss.open("alice", paths[0], asDatastar()); c.status != http.StatusTooManyRequests {
		t.Errorf("after the room was taken again: status = %d, want 429", c.status)
	}

	// Everything closes, also the stream whose client just disappears.
	for _, c := range open[1:] {
		c.cancel()
	}
	again.cancel()
	e.waitOpen(e.alice, 0)
	bobStream.cancel()
	e.waitNoConnections()
}

// TestCallSocketCountsAndIsLimited shows that the signalling socket of a call
// of the pages is among the connections of the user: past the limit the
// handshake is 429 before the call is looked at, and a socket that fails
// (here, a conversation with no call) gives its place back.
func TestCallSocketCountsAndIsLimited(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	conv := e.direct("alice", "bob")
	socket := "/calls/" + idText(conv) + "/ws"

	rec := e.send(get(socket, e.cookies["alice"]))
	if rec.Code != http.StatusConflict {
		t.Fatalf("no call: status = %d, want 409", rec.Code)
	}
	if got := e.conns.Open(e.alice.ID); got != 0 {
		t.Fatalf("a failed socket stays counted: %d", got)
	}

	ss := e.serveStreams()
	var open []*streamClient
	for range app.MaxConnectionsPerUser {
		open = append(open, ss.open("alice", e.streamOfRoom("general", ""), asDatastar()))
	}
	e.waitOpen(e.alice, app.MaxConnectionsPerUser)
	rec = e.send(get(socket, e.cookies["alice"]))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("past the limit: status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "30" {
		t.Errorf("Retry-After = %q, want 30", got)
	}
	// Bob has none of alice's connections.
	if rec := e.send(get(socket, e.cookies["bob"])); rec.Code != http.StatusConflict {
		t.Errorf("bob: status = %d, want 409 (no call)", rec.Code)
	}
	for _, c := range open {
		c.cancel()
	}
	e.waitNoConnections()
}

// dialAPI opens the user's WebSocket of the API on the test server and returns
// the connection, or the status of the answer when it was refused.
func (ss *streamServer) dialAPI(token string) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(ss.e.t.Context(), streamTimeout)
	defer cancel()
	url := "ws" + strings.TrimPrefix(ss.URL, "http") + "/api/v1/ws"
	return websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
}

// TestStreamsAndSocketsShareTheLimit holds 20 streams of the pages and 10
// WebSockets of the API for one user: one more of either is refused with 429
// (the API's answer has the error object), another user is not affected, and
// when everything has closed, also a socket whose client just vanished, nothing
// is left counted.
func TestStreamsAndSocketsShareTheLimit(t *testing.T) {
	t.Parallel()
	set := newMetricsSet(t)
	e := newRoomsEnvOn(t, set, nil)
	ss := e.serveStreams()
	token := e.apiToken("alice", "limits")
	bobToken := e.apiToken("bob", "limits")

	var streams []*streamClient
	for i := range 20 {
		c := ss.open("alice", e.streamPaths()[i%3], asDatastar())
		if c.status != http.StatusOK {
			t.Fatalf("stream %d: status = %d", i+1, c.status)
		}
		streams = append(streams, c)
	}
	var sockets []*websocket.Conn
	for i := range 10 {
		conn, resp, err := ss.dialAPI(token)
		if err != nil {
			t.Fatalf("socket %d: %v (%v)", i+1, err, resp)
		}
		sockets = append(sockets, conn)
	}
	e.waitOpen(e.alice, app.MaxConnectionsPerUser)

	// One more stream.
	if c := ss.open("alice", e.streamOfRoom("general", ""), asDatastar()); c.status != http.StatusTooManyRequests {
		t.Errorf("a 31st stream: status = %d, want 429", c.status)
	}
	// One more socket: 429 before the upgrade, with the error body of the API.
	conn, resp, err := ss.dialAPI(token)
	if err == nil {
		_ = conn.CloseNow()
		t.Fatal("a 31st socket was accepted")
	}
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("a 31st socket: %v, want a 429 answer", resp)
	}
	if got := resp.Header.Get("Retry-After"); got != "30" {
		t.Errorf("Retry-After = %q, want 30", got)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var obj struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &obj); err != nil || obj.Error.Code != "rate_limited" {
		t.Errorf("the 429 body = %q (%v), want the error rate_limited", body, err)
	}
	if got := set.LimitsRefused.With(metrics.LimitConnections).Value(); got != 2 {
		t.Errorf("refusals counted = %d, want 2", got)
	}

	// Bob is not affected, with a socket and a stream.
	bobSocket, resp, err := ss.dialAPI(bobToken)
	if err != nil {
		t.Fatalf("bob's socket: %v (%v)", err, resp)
	}
	bobStream := ss.open("bob", e.streamOfRoom("general", ""), asDatastar())
	if bobStream.status != http.StatusOK {
		t.Errorf("bob's stream: status = %d", bobStream.status)
	}

	// A socket closed makes room for a stream, and a stream closed for a socket.
	if err := sockets[0].Close(websocket.StatusNormalClosure, "bye"); err != nil {
		t.Logf("close: %v", err)
	}
	e.waitOpen(e.alice, app.MaxConnectionsPerUser-1)
	last := ss.open("alice", e.streamOfRoom("general", ""), asDatastar())
	if last.status != http.StatusOK {
		t.Fatalf("a stream after a socket closed: status = %d", last.status)
	}
	streams[0].cancel()
	e.waitOpen(e.alice, app.MaxConnectionsPerUser-1)
	again, resp, err := ss.dialAPI(token)
	if err != nil {
		t.Fatalf("a socket after a stream closed: %v (%v)", err, resp)
	}

	// The clients vanish without a close handshake.
	for _, c := range sockets[1:] {
		_ = c.CloseNow()
	}
	_ = again.CloseNow()
	_ = bobSocket.CloseNow()
	for _, c := range streams[1:] {
		c.cancel()
	}
	last.cancel()
	bobStream.cancel()
	e.waitOpen(e.alice, 0)
	e.waitNoConnections()
}

// TestSignUpLimitOnThePage walks the limit on sign-ups with an invitation that
// is not valid (SPEC D62) on the page: an unknown, a used and an expired token
// each count as a failed login of the address, after five the sign-up form and
// a login from the address answer 429, a valid token does not count, and
// another address is not affected.
func TestSignUpLimitOnThePage(t *testing.T) {
	t.Parallel()
	const limited = "Too many attempts. Try again in a few minutes."
	set := newMetricsSet(t)
	e := newTestEnvOn(t, set, nil)
	e.addUser("alice", "Alice", app.RoleMember)
	used := e.invitation()
	if _, err := e.accounts.SignUp(t.Context(), used, "first", "First", testPassword); err != nil {
		t.Fatal(err)
	}
	expired := e.invitation()
	e.clk.Advance(e.srv.cfg.InvitationLifetime)
	valid := e.invitation()
	const attacker = "203.0.113.50:4000"

	// Mistakes in the form of a valid invitation do not count.
	for range 8 {
		rec := e.send(postForm("/invite/"+valid, signUpForm("dana", "Dana", "short"), asDatastar(), fromAddr(attacker)))
		requireSSE(t, rec)
		requireBody(t, rec, []string{"Use at least 12 characters."})
	}
	tokens := []string{"no-such-token", used, expired, "another-unknown-token", used}
	for i, token := range tokens {
		rec := e.send(postForm("/invite/"+token, signUpForm("dana", "Dana", testPassword), asDatastar(), fromAddr(attacker)))
		requireSSE(t, rec)
		requireBody(t, rec, []string{redirectScript("/invite/" + token)})
		if i < len(tokens)-1 {
			if rec := e.send(postForm("/login", loginForm("alice", testPassword), asDatastar(), fromAddr("203.0.113.51:1"))); !strings.Contains(rec.Body.String(), redirectScript("/")) {
				t.Fatalf("the login of another address failed after %d failures", i+1)
			}
		}
	}
	// Now the address is blocked: Datastar gets the message in the form, a
	// plain post gets 429 and the page; the valid token is not looked at.
	rec := e.send(postForm("/invite/"+valid, signUpForm("dana", "Dana", testPassword), asDatastar(), fromAddr(attacker)))
	requireSSE(t, rec)
	requireBody(t, rec, []string{`<p class="form-error" role="alert">` + limited + `</p>`, `value="dana"`})
	if sessionCookieOf(rec) != nil {
		t.Error("a limited sign-up set a cookie")
	}
	rec = e.send(postForm("/invite/"+valid, signUpForm("dana", "Dana", testPassword), fromAddr(attacker)))
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("plain post: status = %d, want 429", rec.Code)
	}
	requireBody(t, rec, []string{limited, "<!doctype html>"})
	// A login from the address is limited too, with the right password.
	rec = e.send(postForm("/login", loginForm("alice", testPassword), asDatastar(), fromAddr(attacker)))
	requireBody(t, rec, []string{limited})
	if got := set.LimitsRefused.With(metrics.LimitSignUp).Value(); got != 2 {
		t.Errorf("refused sign-ups counted = %d, want 2", got)
	}
	// Another address signs up with the valid token.
	rec = e.send(postForm("/invite/"+valid, signUpForm("dana", "Dana", testPassword), asDatastar(), fromAddr("203.0.113.52:1")))
	requireBody(t, rec, []string{redirectScript("/")})
	// The window passes.
	e.clk.Advance(app.LoginWindow)
	rec = e.send(postForm("/login", loginForm("alice", testPassword), asDatastar(), fromAddr(attacker)))
	requireBody(t, rec, []string{redirectScript("/")})
}
