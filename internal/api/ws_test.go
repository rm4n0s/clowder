package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/rm4n0s/errors"
)

// wsWait is how long a test waits for a message that should come.
const wsWait = 5 * time.Second

// wsQuiet is how long a test waits to be sure that a message does not come.
const wsQuiet = 150 * time.Millisecond

// wsForum is a forum with its API on a real port, for the WebSocket.
type wsForum struct {
	*forum
	ts *httptest.Server
}

// newWSForum makes a forum served on a local port.
func newWSForum(t *testing.T) *wsForum {
	t.Helper()
	return wsForumOf(t, newForum(t))
}

// wsForumOf serves a forum on a local port.
func wsForumOf(t *testing.T, f *forum) *wsForum {
	t.Helper()
	ts := httptest.NewServer(f.handler)
	t.Cleanup(ts.Close)
	return &wsForum{forum: f, ts: ts}
}

// url is the address of the WebSocket.
func (w *wsForum) url() string {
	return "ws" + strings.TrimPrefix(w.ts.URL, "http") + Prefix + "/ws"
}

// dialWith opens the WebSocket with the headers and returns the connection or
// the error and the answer of the handshake.
func (w *wsForum) dialWith(header http.Header) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(w.t.Context(), wsWait)
	defer cancel()
	return websocket.Dial(ctx, w.url(), &websocket.DialOptions{HTTPHeader: header})
}

// dialToken opens the WebSocket with the bearer token.
func (w *wsForum) dialToken(token string) *wsClient {
	w.t.Helper()
	conn, resp, err := w.dialWith(http.Header{"Authorization": {"Bearer " + token}})
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		w.t.Fatalf("Dial: %v (status %d)", err, status)
	}
	c := newWSClient(w.t, conn)
	w.t.Cleanup(func() {
		_ = conn.CloseNow()
		if w.t.Failed() {
			w.t.Logf("server log:\n%s", w.log.String())
		}
	})
	return c
}

// dial opens the WebSocket as the user, with the token of the forum.
func (w *wsForum) dial(user string) *wsClient {
	w.t.Helper()
	return w.dialToken(w.as(user))
}

// wsClient is the client side of a WebSocket in a test. Every message it reads
// is checked against WsServerMessage (SPEC D45). A goroutine reads the
// connection all the time (a read that times out closes a WebSocket), and the
// methods wait for what it has read.
type wsClient struct {
	t      *testing.T
	conn   *websocket.Conn
	nextID uint64
	frames chan wsClientFrame
	// closeErr is the error that ended the connection, once it has.
	closeErr error
}

// wsClientFrame is a message the reader goroutine of a client has read, or the
// error that ended the connection.
type wsClientFrame struct {
	typ websocket.MessageType
	raw []byte
	err error
}

// newWSClient makes the client of a connection and starts its reader. The
// goroutine ends with the test.
func newWSClient(t *testing.T, conn *websocket.Conn) *wsClient {
	c := &wsClient{t: t, conn: conn, frames: make(chan wsClientFrame, 4096)}
	go func() {
		for {
			typ, raw, err := conn.Read(t.Context())
			c.frames <- wsClientFrame{typ: typ, raw: raw, err: err}
			if err != nil {
				return
			}
		}
	}()
	return c
}

// id returns a new request id.
func (c *wsClient) id() uint64 {
	c.nextID++
	return c.nextID
}

// send writes a message as JSON.
func (c *wsClient) send(v any) {
	c.t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		c.t.Fatal(err)
	}
	c.sendText(string(raw))
}

// sendText writes a text message as it is.
func (c *wsClient) sendText(text string) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(c.t.Context(), wsWait)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageText, []byte(text)); err != nil {
		c.t.Fatalf("Write: %v", err)
	}
}

// next returns the next frame the client has read, waiting at most timeout;
// ok is false when none came.
func (c *wsClient) next(timeout time.Duration) (f wsClientFrame, ok bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case f = <-c.frames:
		return f, true
	case <-timer.C:
		return f, false
	}
}

// message decodes a frame that must be a message of the server, checking it
// against its schema.
func (c *wsClient) message(f wsClientFrame) map[string]any {
	c.t.Helper()
	if f.err != nil {
		c.t.Fatalf("Read: %v", f.err)
	}
	if f.typ != websocket.MessageText {
		c.t.Fatalf("the server sent a message of type %v", f.typ)
	}
	checkSchema(c.t, "WsServerMessage", f.raw)
	var m map[string]any
	if err := json.Unmarshal(f.raw, &m); err != nil {
		c.t.Fatalf("the message is not an object: %v: %s", err, f.raw)
	}
	return m
}

// read reads the next message, checks it against the schema of the messages of
// the server and returns it as an object.
func (c *wsClient) read() map[string]any {
	c.t.Helper()
	f, ok := c.next(wsWait)
	if !ok {
		c.t.Fatalf("no message within %s", wsWait)
	}
	return c.message(f)
}

// request sends a message with a new id and returns the next message.
func (c *wsClient) request(typ string, extra map[string]any) map[string]any {
	c.t.Helper()
	msg := map[string]any{"type": typ, "id": c.id()}
	for k, v := range extra {
		msg[k] = v
	}
	c.send(msg)
	return c.read()
}

// subscribe subscribes to the topics and returns the answer.
func (c *wsClient) subscribe(topics ...string) map[string]any {
	c.t.Helper()
	return c.request(wsSubscribe, map[string]any{"topics": topics})
}

// mustSubscribe subscribes to the topics and fails the test unless they are
// acked.
func (c *wsClient) mustSubscribe(topics ...string) {
	c.t.Helper()
	if got := c.subscribe(topics...); got["type"] != wsAck {
		c.t.Fatalf("subscribe %v = %v, want an ack", topics, got)
	}
}

// await reads messages until the event of the kind on the topic comes, and
// returns it. The other messages are skipped (they are checked all the same).
func (c *wsClient) await(topic, kind string) map[string]any {
	c.t.Helper()
	var seen []string
	deadline := time.Now().Add(wsWait)
	for {
		f, ok := c.next(time.Until(deadline))
		if !ok {
			c.t.Fatalf("no %s on %s within %s (seen %v)", kind, topic, wsWait, seen)
		}
		if f.err != nil {
			c.t.Fatalf("waiting for %s on %s: %v (seen %v)", kind, topic, f.err, seen)
		}
		m := c.message(f)
		if m["type"] == wsEventType && m["topic"] == topic && m["kind"] == kind {
			return m
		}
		seen = append(seen, describeMessage(m))
	}
}

// describeMessage is the short form of a message for a failure.
func describeMessage(m map[string]any) string {
	switch m["type"] {
	case wsEventType:
		return "event " + m["topic"].(string) + " " + m["kind"].(string)
	case wsError:
		raw, _ := json.Marshal(m)
		return string(raw)
	}
	return m["type"].(string)
}

// requireQuiet fails the test if the server sends a message now. It pings
// first, so that a message that is on its way comes before the pong.
func (c *wsClient) requireQuiet() {
	c.t.Helper()
	pong := c.request(wsPing, nil)
	if pong["type"] != wsPong {
		c.t.Fatalf("expected the pong, got %v", describeMessage(pong))
	}
	if f, ok := c.next(wsQuiet); ok {
		c.t.Fatalf("the server sent what was not expected: %s %v", f.raw, f.err)
	}
}

// requireClosed reads until the connection ends and fails the test unless it
// was closed with the code. Messages before the close frame are returned.
func (c *wsClient) requireClosed(code websocket.StatusCode) []map[string]any {
	c.t.Helper()
	before, err := c.untilClosed()
	if got := websocket.CloseStatus(err); got != code {
		c.t.Fatalf("the connection ended with %v (%d), want %d (messages before: %v)", err, got, code, before)
	}
	return before
}

// closeReason is the reason of the close frame that ended the connection, ""
// when there was none.
func (c *wsClient) closeReason() string {
	var closeErr websocket.CloseError
	if errors.As(c.closeErr, &closeErr) {
		return closeErr.Reason
	}
	return ""
}

// untilClosed reads until the connection ends and returns the messages before
// the end and the error that ended it.
func (c *wsClient) untilClosed() ([]map[string]any, error) {
	c.t.Helper()
	var before []map[string]any
	deadline := time.Now().Add(wsWait)
	for {
		f, ok := c.next(time.Until(deadline))
		if !ok {
			c.t.Fatalf("the connection did not end within %s (messages before: %v)", wsWait, before)
		}
		if f.err != nil {
			c.closeErr = f.err
			return before, f.err
		}
		before = append(before, c.message(f))
	}
}

// errorOf returns the error object of an error message, failing the test for
// any other message.
func errorOf(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	if m["type"] != wsError {
		t.Fatalf("got %s, want an error message", describeMessage(m))
	}
	return obj(t, m["error"])
}

// topicOf is the name of a topic: topicOf("room", 3) is "room:3".
func topicOf(kind string, id uint64) string {
	return kind + ":" + strconv.FormatUint(id, 10)
}

func TestWSHandshake(t *testing.T) {
	t.Parallel()
	w := newWSForum(t)
	host := strings.TrimPrefix(w.ts.URL, "http://")
	tests := []struct {
		name   string
		header func() http.Header
		status int
	}{
		{"no token", func() http.Header { return nil }, http.StatusUnauthorized},
		{"a token nobody has", func() http.Header { return http.Header{"Authorization": {"Bearer nope"}} }, http.StatusUnauthorized},
		{"another scheme", func() http.Header { return http.Header{"Authorization": {"Basic " + w.as("alice")}} }, http.StatusUnauthorized},
		{"the cookie of a browser", func() http.Header {
			return http.Header{"Cookie": {sessionCookie + "=" + w.browserToken("alice")}}
		}, http.StatusUnauthorized},
		{"the token of a browser as the bearer", func() http.Header {
			return http.Header{"Authorization": {"Bearer " + w.browserToken("alice")}}
		}, http.StatusUnauthorized},
		{"another site", func() http.Header {
			return http.Header{"Authorization": {"Bearer " + w.as("alice")}, "Origin": {"https://evil.example"}}
		}, http.StatusForbidden},
		{"the same site", func() http.Header {
			return http.Header{"Authorization": {"Bearer " + w.as("alice")}, "Origin": {"http://" + host}}
		}, http.StatusSwitchingProtocols},
		{"no origin, like a desktop client", func() http.Header {
			return http.Header{"Authorization": {"Bearer " + w.as("alice")}}
		}, http.StatusSwitchingProtocols},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, resp, err := w.dialWith(tt.header())
			if tt.status == http.StatusSwitchingProtocols {
				if err != nil {
					t.Fatalf("Dial: %v", err)
				}
				_ = conn.CloseNow()
				return
			}
			if err == nil {
				_ = conn.CloseNow()
				t.Fatal("the handshake worked")
			}
			if resp == nil || resp.StatusCode != tt.status {
				t.Fatalf("status = %v, want %d (%v)", resp, tt.status, err)
			}
			if tt.status == http.StatusUnauthorized {
				if resp.Header.Get("WWW-Authenticate") == "" {
					t.Error("no WWW-Authenticate")
				}
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
				checkSchema(t, "Error", body)
			}
		})
	}
}

// sessionCookie is the name of the cookie of the pages' session.
const sessionCookie = "clowder_session"

// TestWSNotAnUpgrade checks the answer of the route to a plain GET, which the
// document describes (the handler tests use the recorder).
func TestWSNotAnUpgrade(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	rec := f.call("alice", http.MethodGet, "/api/v1/ws", nil)
	requireStatus(t, rec, http.StatusUpgradeRequired)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q", ct)
	}
	rec = f.call("alice", http.MethodGet, "/api/v1/ws", nil, header("Connection", "Upgrade"), header("Upgrade", "websocket"), header("Sec-WebSocket-Version", "12"))
	requireStatus(t, rec, http.StatusBadRequest)
	// The route has the one method.
	requireStatus(t, f.call("alice", http.MethodPost, "/api/v1/ws", nil), http.StatusMethodNotAllowed)
}

func TestWSPingSubscribeUnsubscribe(t *testing.T) {
	t.Parallel()
	w := newWSForum(t)
	c := w.dial("alice")

	pong := c.request(wsPing, nil)
	if pong["type"] != wsPong || pong["id"] != float64(c.nextID) {
		t.Errorf("pong = %v", pong)
	}

	lobby, den := topicOf(topicRoom, w.lobby.ID), topicOf(topicRoom, w.den.ID)
	// Repeats are left out of the ack, and a visible room can be listened to
	// by someone who has not joined it.
	ack := c.subscribe(lobby, den, lobby)
	if ack["type"] != wsAck || ack["id"] != float64(c.nextID) {
		t.Fatalf("ack = %v", ack)
	}
	topics := list(t, ack["topics"])
	if len(topics) != 2 || topics[0] != lobby || topics[1] != den {
		t.Errorf("ack topics = %v", topics)
	}
	// The same topics again are fine.
	if got := c.subscribe(den); got["type"] != wsAck {
		t.Errorf("subscribing again = %v", got)
	}
	// The user topic of the user is always there; asking for it is fine.
	own := topicOf(topicUser, w.userID("alice"))
	if got := c.subscribe(own); got["type"] != wsAck {
		t.Errorf("subscribing to the own user topic = %v", got)
	}

	w.write("bob", "den", "first")
	c.await(den, "post.created")
	if ack := c.request(wsUnsubscribe, map[string]any{"topics": []string{den, "room:999999", den}}); ack["type"] != wsAck {
		t.Fatalf("unsubscribe = %v", ack)
	}
	w.write("bob", "den", "second")
	c.requireQuiet()
	// The lobby is still there.
	w.write("admin", "lobby", "third")
	c.await(lobby, "post.created")
}

func TestWSInvalidMessages(t *testing.T) {
	t.Parallel()
	w := newWSForum(t)
	tests := []struct {
		name string
		text string
		// field is the problem of the error, id the id it repeats (-1 for none).
		field string
		id    float64
	}{
		{"an unknown type", `{"type":"nope","id":4}`, "type", 4},
		{"no type", `{"id":4}`, "type", 4},
		{"null", `null`, "type", -1},
		{"a number", `5`, "body", -1},
		{"a list", `[]`, "body", -1},
		{"a string", `"ping"`, "body", -1},
		{"no id", `{"type":"ping"}`, "id", -1},
		{"an id that is not a number", `{"type":"ping","id":"x"}`, "id", -1},
		{"a negative id", `{"type":"ping","id":-1}`, "id", -1},
		{"a fractional id", `{"type":"ping","id":1.5}`, "id", -1},
		{"an unknown field", `{"type":"ping","id":4,"extra":true}`, "extra", 4},
		{"a ping with topics", `{"type":"ping","id":4,"topics":[]}`, "topics", 4},
		{"a subscribe with no topics", `{"type":"subscribe","id":4}`, "topics", 4},
		{"a subscribe with an empty list", `{"type":"subscribe","id":4,"topics":[]}`, "topics", 4},
		{"an unsubscribe with no topics", `{"type":"unsubscribe","id":4}`, "topics", 4},
		{"topics that are not a list", `{"type":"subscribe","id":4,"topics":"room:1"}`, "topics", 4},
		{"a topic that is not a string", `{"type":"subscribe","id":4,"topics":[3]}`, "topics.0", 4},
		{"a type that is not a string", `{"type":4,"id":4}`, "type", 4},
		{"exactly 16 KiB", `{"type":"ping","id":4,"padding":"` + strings.Repeat("a", wsReadLimit-len(`{"type":"ping","id":4,"padding":""}`)) + `"}`, "padding", 4},
	}
	c := w.dial("alice")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c.t = t
			c.sendText(tt.text)
			m := c.read()
			e := errorOf(t, m)
			if e["code"] != "invalid_input" {
				t.Errorf("code = %v, want invalid_input", e["code"])
			}
			if fields := obj(t, e["fields"]); fields[tt.field] == nil {
				t.Errorf("fields = %v, want one for %q", fields, tt.field)
			}
			if tt.id >= 0 && m["id"] != tt.id {
				t.Errorf("id = %v, want %v", m["id"], tt.id)
			}
			if tt.id < 0 && m["id"] != nil {
				t.Errorf("id = %v, want none", m["id"])
			}
		})
	}
	// The connection is still open after all of them.
	c.t = t
	if pong := c.request(wsPing, nil); pong["type"] != wsPong {
		t.Errorf("pong = %v", pong)
	}
}

func TestWSMessagesThatCloseTheConnection(t *testing.T) {
	t.Parallel()
	w := newWSForum(t)
	tests := []struct {
		name   string
		typ    websocket.MessageType
		text   string
		code   websocket.StatusCode
		reason string
	}{
		{"invalid JSON", websocket.MessageText, `{"type":`, websocket.StatusUnsupportedData, reasonNotJSON},
		{"not JSON", websocket.MessageText, `ping`, websocket.StatusUnsupportedData, reasonNotJSON},
		{"an empty message", websocket.MessageText, ``, websocket.StatusUnsupportedData, reasonNotJSON},
		{"two values", websocket.MessageText, `{"type":"ping","id":1} {"type":"ping","id":2}`, websocket.StatusUnsupportedData, reasonNotJSON},
		{"text after the value", websocket.MessageText, `{"type":"ping","id":1} x`, websocket.StatusUnsupportedData, reasonNotJSON},
		{"a binary message", websocket.MessageBinary, `{"type":"ping","id":1}`, websocket.StatusUnsupportedData, reasonTextOnly},
		{"over 16 KiB", websocket.MessageText, `{"type":"ping","id":1,"padding":"` + strings.Repeat("a", wsReadLimit) + `"}`, websocket.StatusMessageTooBig, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := w.dial("alice")
			ctx, cancel := context.WithTimeout(t.Context(), wsWait)
			defer cancel()
			// A write of a message over the limit may fail when the server
			// has closed the connection already.
			_ = c.conn.Write(ctx, tt.typ, []byte(tt.text))
			_, err := c.untilClosed()
			if got := websocket.CloseStatus(err); got != tt.code {
				t.Fatalf("closed with %d (%v), want %d", got, err, tt.code)
			}
			var closeErr websocket.CloseError
			if !errors.As(err, &closeErr) {
				t.Fatalf("%v is no close error", err)
			}
			if tt.reason != "" && closeErr.Reason != tt.reason {
				t.Errorf("reason = %q, want %q", closeErr.Reason, tt.reason)
			}
		})
	}
}

func TestWSTopics(t *testing.T) {
	t.Parallel()
	w := newWSForum(t)
	den := w.write("alice", "den", "a post")
	comment := w.say("bob", den.ID, "a comment")
	lobbyPost := w.write("admin", "lobby", "a post in the lobby")
	conv := w.convs("alice", "bob")
	otherUser := topicOf(topicUser, w.userID("bob"))
	own := topicOf(topicUser, w.userID("alice"))

	tests := []struct {
		name   string
		user   string
		topics []string
		// code is the code of the error; empty means an ack.
		code string
		// topic is the topic the error names; empty for none.
		topic string
	}{
		{"a room, a thread, a board and a conversation of a member", "alice",
			[]string{topicOf(topicRoom, w.den.ID), topicOf(topicPost, den.ID), topicOf(topicBoard, w.den.ID), topicOf(topicConversation, conv)}, "", ""},
		{"a visible room of someone who has not joined it", "carol",
			[]string{topicOf(topicRoom, w.lobby.ID), topicOf(topicBoard, w.lobby.ID), topicOf(topicPost, lobbyPost.ID)}, "", ""},
		{"a private room of a non-member", "carol", []string{topicOf(topicRoom, w.den.ID)}, "not_found", topicOf(topicRoom, w.den.ID)},
		{"the private room of a non-member is not found for an admin either", "admin", []string{topicOf(topicRoom, w.den.ID)}, "not_found", topicOf(topicRoom, w.den.ID)},
		{"the board of a private room", "carol", []string{topicOf(topicBoard, w.den.ID)}, "not_found", topicOf(topicBoard, w.den.ID)},
		{"the thread of a private room", "carol", []string{topicOf(topicPost, den.ID)}, "not_found", topicOf(topicPost, den.ID)},
		{"a conversation of others", "carol", []string{topicOf(topicConversation, conv)}, "not_found", topicOf(topicConversation, conv)},
		{"a conversation of others for an admin", "admin", []string{topicOf(topicConversation, conv)}, "not_found", topicOf(topicConversation, conv)},
		{"a room that does not exist", "alice", []string{"room:999999"}, "not_found", "room:999999"},
		{"a thread that does not exist", "alice", []string{"post:999999"}, "not_found", "post:999999"},
		{"a comment is no thread", "alice", []string{topicOf(topicPost, comment.ID)}, "invalid_input", topicOf(topicPost, comment.ID)},
		{"the user topic of someone else", "alice", []string{otherUser}, "invalid_input", otherUser},
		{"the own user topic", "alice", []string{own}, "", ""},
		{"the first topic that is refused is named", "carol",
			[]string{topicOf(topicRoom, w.lobby.ID), topicOf(topicRoom, w.den.ID), topicOf(topicBoard, w.den.ID)}, "not_found", topicOf(topicRoom, w.den.ID)},
		{"a bad name is found before the permissions are checked", "carol",
			[]string{topicOf(topicRoom, w.den.ID), "room:x"}, "invalid_input", "room:x"},
		{"no kind", "alice", []string{":1"}, "invalid_input", ":1"},
		{"no ID", "alice", []string{"room:"}, "invalid_input", "room:"},
		{"no colon", "alice", []string{"room1"}, "invalid_input", "room1"},
		{"an ID of zero", "alice", []string{"room:0"}, "invalid_input", "room:0"},
		{"a negative ID", "alice", []string{"room:-1"}, "invalid_input", "room:-1"},
		{"an ID with a plus", "alice", []string{"room:+1"}, "invalid_input", "room:+1"},
		{"an ID with a leading zero", "alice", []string{"room:01"}, "invalid_input", "room:01"},
		{"an ID with a space", "alice", []string{"room: 1"}, "invalid_input", "room: 1"},
		{"an ID that is too big", "alice", []string{"room:99999999999999999999999"}, "invalid_input", "room:99999999999999999999999"},
		{"a kind in capitals", "alice", []string{"ROOM:1"}, "invalid_input", "ROOM:1"},
		{"a kind that is not one", "alice", []string{"rooms:1"}, "invalid_input", "rooms:1"},
		{"the all topic", "alice", []string{"*"}, "invalid_input", "*"},
		{"more topics than a connection takes", "alice", manyTopics(wsMaxTopics + 1), "invalid_input", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := w.dial(tt.user)
			got := c.subscribe(tt.topics...)
			if tt.code == "" {
				if got["type"] != wsAck {
					t.Fatalf("got %s, want an ack", describeMessage(got))
				}
				return
			}
			e := errorOf(t, got)
			if e["code"] != tt.code {
				t.Errorf("code = %v, want %s", e["code"], tt.code)
			}
			if got["topic"] != nil && tt.topic == "" {
				t.Errorf("topic = %v, want none", got["topic"])
			}
			if tt.topic != "" && got["topic"] != tt.topic {
				t.Errorf("topic = %v, want %s", got["topic"], tt.topic)
			}
			if got["id"] != float64(1) {
				t.Errorf("id = %v, want 1", got["id"])
			}
			if tt.code == "invalid_input" && e["fields"] == nil {
				t.Errorf("no fields in %v", e)
			}
			// Nothing of the request was subscribed.
			if tt.topic != "" {
				w.write("admin", "lobby", "nobody hears this")
				c.requireQuiet()
			}
		})
	}
}

// manyTopics returns n different room topics.
func manyTopics(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "room:" + strconv.Itoa(i+1)
	}
	return out
}

func TestWSOwnUserTopicCannotBeRemoved(t *testing.T) {
	t.Parallel()
	w := newWSForum(t)
	c := w.dial("alice")
	own := topicOf(topicUser, w.userID("alice"))
	e := errorOf(t, c.request(wsUnsubscribe, map[string]any{"topics": []string{own}}))
	if e["code"] != "invalid_input" {
		t.Errorf("code = %v", e["code"])
	}
	e = errorOf(t, c.request(wsUnsubscribe, map[string]any{"topics": []string{"room:x"}}))
	if e["code"] != "invalid_input" {
		t.Errorf("code = %v", e["code"])
	}
	// The user topic still works: alice is mentioned and reads her notifications.
	w.write("bob", "den", "Hello @alice")
	n := c.await(own, "notification.created")
	id := idOf(t, n["data"])
	w.ok("alice", http.MethodPost, "/api/v1/notifications/read", map[string]any{"up_to_id": id}, http.StatusNoContent)
	c.await(own, "notification.read")
}

// TestWSServerMessagesAreChecked checks the schemas of the messages: they must
// take what the server sends and refuse what it does not.
func TestWSServerMessagesAreChecked(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		schema string
		text   string
		want   string // a part of the first problem; empty means valid
	}{
		{"an ack", "WsServerMessage", `{"type":"ack","id":1,"topics":["room:1"]}`, ""},
		{"an ack with no topics", "WsServerMessage", `{"type":"ack","id":1}`, "oneOf"},
		{"an error to a request", "WsServerMessage", `{"type":"error","id":1,"topic":"room:1","error":{"code":"not_found","message":"Not found."}}`, ""},
		{"an error about a dropped topic", "WsServerMessage", `{"type":"error","topic":"room:1","error":{"code":"not_found","message":"Not found."}}`, ""},
		{"an error with a code that is not one", "WsServerMessage", `{"type":"error","error":{"code":"nope","message":"x"}}`, "oneOf"},
		{"a pong", "WsServerMessage", `{"type":"pong","id":3}`, ""},
		{"a resync", "WsServerMessage", `{"type":"resync","reason":"overflow"}`, ""},
		{"a resync with another reason", "WsServerMessage", `{"type":"resync","reason":"because"}`, "oneOf"},
		{"an event with no data", "WsServerMessage", `{"type":"event","topic":"room:1","kind":"post.deleted","room_id":1,"post_id":2}`, ""},
		{"an event of a kind that does not exist", "WsServerMessage", `{"type":"event","topic":"room:1","kind":"post.exploded"}`, "oneOf"},
		{"an event with a field it does not have", "WsServerMessage", `{"type":"event","topic":"room:1","kind":"post.deleted","token":"x"}`, "oneOf"},
		{"an event with data that is no resource", "WsServerMessage", `{"type":"event","topic":"room:1","kind":"post.created","data":{"id":1}}`, "oneOf"},
		{"an event with the unread counts", "WsServerMessage", `{"type":"event","topic":"user:1","kind":"notification.read","user_id":1,"data":{"unread_notifications":1,"unread_conversations":0}}`, ""},
		{"an event with the reactions", "WsServerMessage", `{"type":"event","topic":"room:1","kind":"reaction.changed","room_id":1,"post_id":2,"data":{"reactions":[]}}`, ""},
		{"an event of a read conversation", "WsServerMessage", `{"type":"event","topic":"conversation:1","kind":"conversation.read","conversation_id":1,"user_id":2,"message_id":3,"data":{"conversation_id":1,"user_id":2,"up_to_id":3}}`, ""},
		{"a message of no type", "WsServerMessage", `{"id":1}`, "oneOf"},
		{"a subscribe", "WsClientMessage", `{"type":"subscribe","id":1,"topics":["room:1"]}`, ""},
		{"a subscribe with no topics", "WsClientMessage", `{"type":"subscribe","id":1,"topics":[]}`, "oneOf"},
		{"an unsubscribe", "WsClientMessage", `{"type":"unsubscribe","id":1,"topics":["room:1"]}`, ""},
		{"a ping", "WsClientMessage", `{"type":"ping","id":1}`, ""},
		{"a ping with a text id", "WsClientMessage", `{"type":"ping","id":"1"}`, "oneOf"},
		{"a message of the server as a message of the client", "WsClientMessage", `{"type":"pong","id":1}`, "oneOf"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			problems := schemaProblems(tt.schema, []byte(tt.text))
			if tt.want == "" {
				if len(problems) != 0 {
					t.Fatalf("problems = %v, want none", problems)
				}
				return
			}
			if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), tt.want) {
				t.Fatalf("problems = %v, want one with %q", problems, tt.want)
			}
		})
	}
}
