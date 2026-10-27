package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/errors"
)

// requireResync fails the test unless the messages end with a resync of the
// reason, and the connection was closed with 1013.
func requireResync(t *testing.T, c *wsClient, reason string) {
	t.Helper()
	before := c.requireClosed(websocket.StatusTryAgainLater)
	if len(before) == 0 {
		t.Fatalf("no resync before the close")
	}
	last := before[len(before)-1]
	if last["type"] != wsResync || last["reason"] != reason {
		t.Fatalf("the last message before the close is %v, want a resync of %s", last, reason)
	}
}

func TestWSOverflowSendsResync(t *testing.T) {
	t.Parallel()
	w := newWSForum(t)
	alice := w.userID("alice")
	c := w.dial("alice")
	c.mustSubscribe(topicOf(topicRoom, w.den.ID))
	// The connection reads the counts for each of these; the burst is over
	// before it has read 64 of them, and its queue of the hub overflows.
	for range 500 {
		w.hub.Publish(live.Event{Topic: live.UserTopic(alice), Kind: live.KindNotificationRead, UserID: alice})
	}
	requireResync(t, c, "overflow")
}

func TestWSDropAllSendsResync(t *testing.T) {
	t.Parallel()
	w := newWSForum(t)
	c := w.dial("alice")
	c.mustSubscribe(topicOf(topicRoom, w.den.ID))
	w.hub.DropAll()
	requireResync(t, c, "reconnect")
	// The hub is still open: a client connects again.
	c = w.dial("alice")
	c.mustSubscribe(topicOf(topicRoom, w.den.ID))
	w.write("bob", "den", "after the reconnect")
	c.await(topicOf(topicRoom, w.den.ID), "post.created")
}

func TestWSSessionChanges(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// change changes the session or the user of the connection of bob.
		change func(w *wsForum, token string)
		code   websocket.StatusCode
		reason string
	}{
		{"the session logs out", func(w *wsForum, token string) {
			requireStatus(w.t, w.send(request(http.MethodPost, "/api/v1/auth/logout", nil, bearer(token))), http.StatusNoContent)
		}, closeSessionEnded, reasonSessionEnded},
		{"the session is ended from another", func(w *wsForum, token string) {
			sessions := list(w.t, w.ok("bob", http.MethodGet, "/api/v1/me/sessions", nil, http.StatusOK)["sessions"])
			for _, s := range sessions {
				if obj(w.t, s)["current"] != true {
					w.ok("bob", http.MethodDelete, path("/me/sessions/%d", idOf(w.t, s)), nil, http.StatusNoContent)
				}
			}
		}, closeSessionEnded, reasonSessionEnded},
		{"the user is disabled", func(w *wsForum, _ string) {
			w.ok("admin", http.MethodPatch, path("/admin/users/%d", w.userID("bob")), map[string]any{"disabled": true}, http.StatusOK)
		}, closeSessionEnded, reasonSessionEnded},
		{"the role changes", func(w *wsForum, _ string) {
			w.ok("admin", http.MethodPatch, path("/admin/users/%d", w.userID("bob")), map[string]any{"role": "admin"}, http.StatusOK)
		}, closeSessionChanged, reasonSessionChanged},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := newWSForum(t)
			// The first session of bob is the one the test reads sessions as;
			// the connection has a session of its own.
			w.as("bob")
			token := w.token("bob")
			c := w.dialToken(token)
			c.mustSubscribe(topicOf(topicRoom, w.lobby.ID))
			tt.change(w, token)
			before := c.requireClosed(tt.code)
			for _, m := range before {
				if m["type"] == wsEventType && m["kind"] == live.KindUserChanged {
					t.Errorf("user.changed is no event for clients: %v", m)
				}
			}
			if got := c.closeReason(); got != tt.reason {
				t.Errorf("reason = %q, want %q", got, tt.reason)
			}
		})
	}

	t.Run("another user's account changes nothing", func(t *testing.T) {
		t.Parallel()
		w := newWSForum(t)
		c := w.dial("alice")
		w.ok("admin", http.MethodPatch, path("/admin/users/%d", w.userID("bob")), map[string]any{"role": "admin"}, http.StatusOK)
		w.ok("admin", http.MethodPatch, path("/admin/users/%d", w.userID("carol")), map[string]any{"disabled": true}, http.StatusOK)
		c.requireQuiet()
	})

	t.Run("a role that is the same is no change", func(t *testing.T) {
		t.Parallel()
		w := newWSForum(t)
		c := w.dial("admin")
		// Another session of the same user logs out: the connection has its
		// own session and stays.
		other := w.token("admin")
		requireStatus(t, w.send(request(http.MethodPost, "/api/v1/auth/logout", nil, bearer(other))), http.StatusNoContent)
		c.requireQuiet()
	})

	t.Run("a connection can be opened again after the role changed", func(t *testing.T) {
		t.Parallel()
		w := newWSForum(t)
		token := w.token("bob")
		c := w.dialToken(token)
		w.ok("admin", http.MethodPatch, path("/admin/users/%d", w.userID("bob")), map[string]any{"role": "admin"}, http.StatusOK)
		c.requireClosed(closeSessionChanged)
		again := w.dialToken(token)
		again.mustSubscribe(topicOf(topicRoom, w.lobby.ID))
	})
}

// TestWSExpiredSessionEndsAtThePing checks that a session that expires while
// the connection is open ends it when the server pings (the session is read
// again then, and a live one slides).
func TestWSExpiredSessionEndsAtThePing(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	f.srv.pingInterval = 40 * time.Millisecond
	w := wsForumOf(t, f)
	c := w.dial("alice")
	f.clk.Advance(31 * 24 * time.Hour)
	c.requireClosed(closeSessionEnded)
}

func TestWSPingsKeepTheConnection(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	f.srv.pingInterval = 20 * time.Millisecond
	f.srv.pingTimeout = time.Second
	w := wsForumOf(t, f)
	c := w.dial("alice")
	c.mustSubscribe(topicOf(topicRoom, w.lobby.ID))
	// The client reads all the time, so it answers the pings.
	time.Sleep(300 * time.Millisecond)
	w.write("admin", "lobby", "still here")
	c.await(topicOf(topicRoom, w.lobby.ID), "post.created")
	if strings.Contains(f.log.String(), "ping failed") {
		t.Errorf("a ping failed:\n%s", f.log.String())
	}
}

func TestWSClientThatDoesNotAnswerPingsIsClosed(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	f.srv.pingInterval = 20 * time.Millisecond
	f.srv.pingTimeout = 100 * time.Millisecond
	w := wsForumOf(t, f)
	// This client never reads, so the library does not answer the pings.
	conn, _, err := w.dialWith(http.Header{"Authorization": {"Bearer " + w.as("alice")}})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	ctx, cancel := context.WithTimeout(t.Context(), wsWait)
	defer cancel()
	// Drain returns when the server has dropped the connection.
	deadline := time.Now().Add(wsWait)
	for !strings.Contains(f.log.String(), "ping failed") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !f.srv.Drain(ctx) {
		t.Fatal("the connection of a client that does not answer was not closed")
	}
	if !strings.Contains(f.log.String(), "ping failed") || !strings.Contains(f.log.String(), tagSocketPingFailed) {
		t.Errorf("the failed ping is not logged:\n%s", f.log.String())
	}
}

func TestWSClientThatGoesAwayIsDropped(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	w := wsForumOf(t, f)
	c := w.dial("alice")
	c.mustSubscribe(topicOf(topicRoom, w.lobby.ID))
	// No close frame: the connection breaks.
	if err := c.conn.CloseNow(); err != nil {
		t.Logf("CloseNow: %v", err)
	}
	w.write("admin", "lobby", "nobody is listening")
	ctx, cancel := context.WithTimeout(t.Context(), wsWait)
	defer cancel()
	if !f.srv.Drain(ctx) {
		t.Fatal("the connection was not dropped")
	}
	if strings.Contains(f.log.String(), `"level":"ERROR"`) || strings.Contains(f.log.String(), `"level":"WARN"`) {
		t.Errorf("a client that left logged a warning or an error:\n%s", f.log.String())
	}
}

// TestWSServerShutdown is not parallel: it looks at the goroutines of the
// whole process, and the parallel tests run only when the others are done.
func TestWSServerShutdown(t *testing.T) {
	w := newWSForum(t)
	clients := []*wsClient{w.dial("alice"), w.dial("alice"), w.dial("bob")}
	clients[0].mustSubscribe(topicOf(topicRoom, w.den.ID))
	// A connection that is not closed within the time is reported.
	short, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if w.srv.Drain(short) {
		t.Error("Drain reports no connection while three are open")
	}

	w.hub.Close()
	for _, c := range clients {
		c.requireClosed(websocket.StatusGoingAway)
	}
	ctx, cancelDrain := context.WithTimeout(t.Context(), wsWait)
	defer cancelDrain()
	if !w.srv.Drain(ctx) {
		t.Fatal("the connections were not all gone")
	}
	// Nothing of a connection runs any more: neither the handler, nor the
	// reader, nor the pinger.
	buf := make([]byte, 1<<20)
	dump := string(buf[:runtime.Stack(buf, true)])
	for _, name := range []string{"(*Server).serveSocket", "(*wsSession)."} {
		if strings.Contains(dump, name) {
			t.Errorf("a goroutine of a connection is still running (%s):\n%s", name, dump)
		}
	}
	// A client that comes after the shutdown is closed at once.
	late := w.dial("alice")
	late.requireClosed(websocket.StatusGoingAway)
}

func TestWSLogs(t *testing.T) {
	t.Parallel()
	w := newWSForum(t)
	token := w.token("alice")
	c := w.dialToken(token)
	c.mustSubscribe(topicOf(topicRoom, w.lobby.ID))
	if err := c.conn.Close(websocket.StatusNormalClosure, "bye"); err != nil {
		t.Logf("Close: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), wsWait)
	defer cancel()
	for strings.Count(w.log.String(), "websocket closed") == 0 && ctx.Err() == nil {
		time.Sleep(10 * time.Millisecond)
	}
	logs := w.log.String()
	for _, want := range []string{"websocket connected", "websocket closed", `"messages_in":1`, `"duration_ms"`, `"user_id"`} {
		if !strings.Contains(logs, want) {
			t.Errorf("the log has no %q:\n%s", want, logs)
		}
	}
	if strings.Contains(logs, token) {
		t.Errorf("the log holds the token:\n%s", logs)
	}
	if strings.Contains(logs, "room:") {
		t.Errorf("the log holds what the client sent:\n%s", logs)
	}
	if strings.Contains(logs, `"level":"ERROR"`) || strings.Contains(logs, `"level":"WARN"`) {
		t.Errorf("a normal connection logged a warning or an error:\n%s", logs)
	}
}

// serverSide makes a WebSocket between a client and the server's side of it,
// without the loop of a session: for the functions of a session that the tests
// call one by one.
func serverSide(t *testing.T, w *wsForum) (*wsSession, *websocket.Conn) {
	t.Helper()
	conns := make(chan *websocket.Conn, 1)
	done := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(rw, r, nil)
		if err != nil {
			return
		}
		conns <- conn
		<-done
	}))
	t.Cleanup(func() {
		close(done)
		ts.Close()
	})
	ctx, cancel := context.WithTimeout(t.Context(), wsWait)
	defer cancel()
	client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.CloseNow() })
	server := <-conns
	t.Cleanup(func() { _ = server.CloseNow() })
	alice := actorOf(w.testEnv, "alice")
	user, err := w.accounts.SessionActor(t.Context(), w.browserToken("alice"))
	if err != nil {
		t.Fatalf("SessionActor: %+v", err)
	}
	return w.srv.newSession(server, &caller{actor: alice, user: user.User}, "", nil), client
}

type unencodableMessage struct{}

func (unencodableMessage) MarshalJSON() ([]byte, error) { return nil, errors.New("Never", "no") }

// TestWSErrorRoutes checks the route of each error the WebSocket makes.
func TestWSErrorRoutes(t *testing.T) {
	t.Parallel()
	w := newWSForum(t)
	ctx := t.Context()
	tests := []struct {
		name  string
		tag   string
		route string
		run   func(t *testing.T) error
	}{
		{"a handshake that is none", tagSocketRejected, "Server.accept.SocketRejected", func(t *testing.T) error {
			_, err := w.srv.accept(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/ws", nil))
			return err
		}},
		{"a message that is not JSON", tagSocketNotJSON, "parseClientMessage.SocketNotJSON", func(t *testing.T) error {
			_, err := parseClientMessage([]byte(`{"type"`))
			return err
		}},
		{"an empty message", tagSocketNotJSON, "parseClientMessage.SocketNotJSON", func(t *testing.T) error {
			_, err := parseClientMessage(nil)
			return err
		}},
		{"two values", tagSocketNotJSON, "parseClientMessage.SocketNotJSON", func(t *testing.T) error {
			_, err := parseClientMessage([]byte(`{"type":"ping","id":1}{}`))
			return err
		}},
		{"a message with a wrong type of a field", tagSocketMessageInvalid, "parseClientMessage.SocketMessageInvalid", func(t *testing.T) error {
			_, err := parseClientMessage([]byte(`{"type":"ping","id":"x"}`))
			return err
		}},
		{"a message of an unknown type", tagSocketMessageInvalid, "parseClientMessage.SocketMessageInvalid", func(t *testing.T) error {
			_, err := parseClientMessage([]byte(`{"type":"nope","id":1}`))
			return err
		}},
		{"a message with no id", tagSocketMessageInvalid, "parseClientMessage.SocketMessageInvalid", func(t *testing.T) error {
			_, err := parseClientMessage([]byte(`{"type":"ping"}`))
			return err
		}},
		{"a ping with topics", tagSocketMessageInvalid, "parseClientMessage.SocketMessageInvalid", func(t *testing.T) error {
			_, err := parseClientMessage([]byte(`{"type":"ping","id":1,"topics":["room:1"]}`))
			return err
		}},
		{"a subscribe with no topics", tagSocketMessageInvalid, "parseClientMessage.SocketMessageInvalid", func(t *testing.T) error {
			_, err := parseClientMessage([]byte(`{"type":"subscribe","id":1,"topics":[]}`))
			return err
		}},
		{"a topic that is none", tagTopicInvalid, "wsSession.plan->parseTopic.TopicInvalid", func(t *testing.T) error {
			ws, _ := serverSide(t, w)
			_, err := ws.plan(ctx, []string{"room:x"})
			return err
		}},
		{"the user topic of another user", tagTopicInvalid, "wsSession.plan->parseTopic.TopicInvalid", func(t *testing.T) error {
			ws, _ := serverSide(t, w)
			_, err := ws.plan(ctx, []string{topicOf(topicUser, w.userID("bob"))})
			return err
		}},
		{"the thread of a comment", tagTopicInvalid, "wsSession.plan->wsSession.resolve.TopicInvalid", func(t *testing.T) error {
			ws, _ := serverSide(t, w)
			comment := w.say("bob", w.write("alice", "den", "a post").ID, "a comment")
			_, err := ws.plan(ctx, []string{topicOf(topicPost, comment.ID)})
			return err
		}},
		{"removing the user topic", tagTopicInvalid, "wsSession.unplan.TopicInvalid", func(t *testing.T) error {
			ws, _ := serverSide(t, w)
			_, err := ws.unplan([]string{topicOf(topicUser, w.userID("alice"))})
			return err
		}},
		{"removing a name that is none", tagTopicInvalid, "wsSession.unplan->parseTopic.TopicInvalid", func(t *testing.T) error {
			ws, _ := serverSide(t, w)
			_, err := ws.unplan([]string{"x"})
			return err
		}},
		{"more topics than the limit", tagTopicsTooMany, "wsSession.plan.TopicsTooMany", func(t *testing.T) error {
			ws, _ := serverSide(t, w)
			_, err := ws.plan(ctx, manyTopics(wsMaxTopics+1))
			return err
		}},
		{"a topic over the limit with the ones there", tagTopicsTooMany, "wsSession.plan.TopicsTooMany", func(t *testing.T) error {
			ws, _ := serverSide(t, w)
			for i, name := range manyTopics(wsMaxTopics) {
				ws.topics[name] = wsTopic{name: name, kind: topicRoom, id: uint64(i + 1)}
			}
			_, err := ws.plan(ctx, []string{"conversation:1"})
			return err
		}},
		{"a room the user may not read", app.TagNotFound, "wsSession.plan->wsSession.resolve->Rooms.Get", func(t *testing.T) error {
			ws, _ := serverSide(t, w)
			_, err := ws.plan(ctx, []string{topicOf(topicRoom, 99999)})
			return err
		}},
		{"a read of a connection that is gone", tagSocketReadFailed, "wsSession.read.SocketReadFailed", func(t *testing.T) error {
			ws, client := serverSide(t, w)
			_ = client.CloseNow()
			return ws.read(ctx).err
		}},
		{"a write to a connection that is gone", tagSocketWriteFailed, "wsSession.send.SocketWriteFailed", func(t *testing.T) error {
			ws, _ := serverSide(t, w)
			_ = ws.conn.CloseNow()
			return ws.send(ctx, wsPongMessage{Type: wsPong, ID: 1})
		}},
		{"a message that cannot be encoded", tagSocketEncodeFailed, "wsSession.send.SocketEncodeFailed", func(t *testing.T) error {
			ws, _ := serverSide(t, w)
			return ws.send(ctx, unencodableMessage{})
		}},
		{"a ping to a connection that is gone", tagSocketPingFailed, "wsSession.ping.SocketPingFailed", func(t *testing.T) error {
			ws, _ := serverSide(t, w)
			_ = ws.conn.CloseNow()
			return ws.ping(ctx)
		}},
		{"a close with a reason that is too long", tagSocketCloseFailed, "wsSession.closeConn.SocketCloseFailed", func(t *testing.T) error {
			ws, _ := serverSide(t, w)
			return ws.closeConn(websocket.StatusGoingAway, strings.Repeat("x", 200))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.run(t)
			if err == nil {
				t.Fatal("no error")
			}
			_ = requireRoute(t, err, tt.tag, tt.route)
		})
	}

	t.Run("the limit is exact", func(t *testing.T) {
		t.Parallel()
		ws, _ := serverSide(t, w)
		for i, name := range manyTopics(wsMaxTopics - 1) {
			ws.topics[name] = wsTopic{name: name, kind: topicRoom, id: uint64(i + 1)}
		}
		// One more fits: the room does not exist, which is a different error.
		_, err := ws.plan(ctx, []string{"room:99999"})
		if appErr, ok := errors.FromError(err); !ok || appErr.Tag == tagTopicsTooMany {
			t.Errorf("err = %v", err)
		}
		// Two more do not.
		_, err = ws.plan(ctx, []string{"room:99998", "room:99999"})
		_ = requireRoute(t, err, tagTopicsTooMany, "wsSession.plan.TopicsTooMany")
	})
}
