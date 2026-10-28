package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/live"
)

// callOK sends a request of the calls and decodes the call in the answer,
// which it checks against the schema Call.
func (f *forum) callOK(user, method, p string, body any, want int) map[string]any {
	f.t.Helper()
	rec := f.call(user, method, p, body)
	requireStatus(f.t, rec, want)
	checkSchema(f.t, "Call", rec.Body.Bytes())
	return decode(f.t, rec)
}

func TestCallRoutes(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	conv := f.convs("alice", "bob")
	callPath := path("/conversations/%d/call", conv)

	// Nothing is going on.
	requireStatus(t, f.call("alice", http.MethodGet, callPath, nil), http.StatusNotFound)

	// The caller starts a call; the answer says what the caller sees.
	rec := f.call("alice", http.MethodPost, callPath, map[string]any{"video": true})
	requireStatus(t, rec, http.StatusCreated)
	checkSchema(t, "Call", rec.Body.Bytes())
	if got := rec.Header().Get("Location"); got != callPath {
		t.Errorf("Location = %q, want %q", got, callPath)
	}
	started := decode(t, rec)
	if started["state"] != "ringing" || started["video"] != true || started["incoming"] != false ||
		started["conversation_id"] != float64(conv) || started["answered_at"] != nil || started["ended_at"] != nil ||
		started["end_reason"] != nil || started["ended_by"] != nil {
		t.Errorf("started call = %v", started)
	}
	if obj(t, started["caller"])["username"] != "alice" || obj(t, started["callee"])["username"] != "bob" {
		t.Errorf("participants = %v, %v", started["caller"], started["callee"])
	}

	// Both see it; the callee sees it as incoming. A stranger sees nothing.
	if got := f.callOK("bob", http.MethodGet, callPath, nil, http.StatusOK); got["incoming"] != true || got["id"] != started["id"] {
		t.Errorf("bob sees %v", got)
	}
	requireStatus(t, f.call("carol", http.MethodGet, callPath, nil), http.StatusNotFound)

	// Who may do what while it rings.
	for _, tt := range []struct {
		name, user, action string
		body               any
		want               int
	}{
		{"the caller answers", "alice", "answer", map[string]any{"video": true}, http.StatusForbidden},
		{"the callee cancels", "bob", "cancel", nil, http.StatusForbidden},
		{"a stranger declines", "carol", "decline", nil, http.StatusNotFound},
		{"the caller declines", "alice", "decline", nil, http.StatusForbidden},
		{"nobody hangs up a call that rings", "bob", "hangup", nil, http.StatusConflict},
		{"an answer needs a body that is JSON", "bob", "answer", "not json", http.StatusUnprocessableEntity},
	} {
		rec := f.call(tt.user, http.MethodPost, callPath+"/"+tt.action, tt.body)
		if rec.Code != tt.want {
			t.Errorf("%s: status = %d, want %d; body:\n%s", tt.name, rec.Code, tt.want, rec.Body)
		}
	}

	// A second call while one rings: either side is busy.
	for _, user := range []string{"alice", "bob"} {
		rec := f.call(user, http.MethodPost, callPath, map[string]any{"video": false})
		requireStatus(t, rec, http.StatusConflict)
		if code := errorCode(t, rec); code != "conflict" {
			t.Errorf("%s: code = %q", user, code)
		}
		if msg := errorFields(t, rec)["form"]; msg != "You are in another call." {
			t.Errorf("%s: message = %v", user, msg)
		}
	}

	// The callee answers, with audio only.
	answered := f.callOK("bob", http.MethodPost, callPath+"/answer", map[string]any{"video": false}, http.StatusOK)
	if answered["state"] != "active" || answered["answered_at"] == nil || answered["incoming"] != true {
		t.Errorf("answered call = %v", answered)
	}
	requireStatus(t, f.call("bob", http.MethodPost, callPath+"/answer", map[string]any{"video": false}), http.StatusConflict)
	requireStatus(t, f.call("alice", http.MethodPost, callPath+"/cancel", nil), http.StatusConflict)

	// The caller hangs up.
	ended := f.callOK("alice", http.MethodPost, callPath+"/hangup", nil, http.StatusOK)
	if ended["state"] != "ended" || ended["end_reason"] != "hangup" || ended["ended_by"] != float64(f.userID("alice")) || ended["ended_at"] == nil {
		t.Errorf("ended call = %v", ended)
	}
	requireStatus(t, f.call("alice", http.MethodGet, callPath, nil), http.StatusNotFound)
	requireStatus(t, f.call("alice", http.MethodPost, callPath+"/hangup", nil), http.StatusConflict)
}

func TestCallEndings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		user, verb string
		reason     string
		by         string
	}{
		{"the callee declines", "bob", "decline", "declined", "bob"},
		{"the caller cancels", "alice", "cancel", "cancelled", "alice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			conv := f.convs("alice", "bob")
			callPath := path("/conversations/%d/call", conv)
			f.callOK("alice", http.MethodPost, callPath, map[string]any{"video": false}, http.StatusCreated)
			got := f.callOK(tt.user, http.MethodPost, callPath+"/"+tt.verb, nil, http.StatusOK)
			if got["state"] != "ended" || got["end_reason"] != tt.reason || got["ended_by"] != float64(f.userID(tt.by)) {
				t.Errorf("call = %v", got)
			}
			// The entry is in the history of both, as a message of kind call.
			for _, user := range []string{"alice", "bob"} {
				msgs := list(t, f.history(user, conv, "")["messages"])
				if len(msgs) != 1 {
					t.Fatalf("%s: messages = %v", user, msgs)
				}
				m := obj(t, msgs[0])
				entry := obj(t, m["call"])
				if m["kind"] != "call" || entry["outcome"] != tt.reason || entry["video"] != false || entry["id"] != got["id"] {
					t.Errorf("%s: message = %v", user, m)
				}
				if entry["text"] == "" {
					t.Errorf("%s: the entry has no text", user)
				}
			}
		})
	}
}

func TestCallRefusals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(f *forum) uint64
		user  string
		want  int
		// message is the text of the form field; empty for a 404.
		message string
	}{
		{"a group", func(f *forum) uint64 { return f.convs("alice", "bob", "carol") }, "alice", http.StatusConflict, "Calls are for conversations of two people."},
		{"a disabled callee", func(f *forum) uint64 {
			conv := f.convs("alice", "bob")
			if err := f.accounts.SetDisabled(f.t.Context(), actorOf(f.testEnv, "admin"), f.userID("bob"), true); err != nil {
				f.t.Fatalf("SetDisabled: %+v", err)
			}
			return conv
		}, "alice", http.StatusConflict, "Bob is disabled."},
		{"a stranger", func(f *forum) uint64 { return f.convs("alice", "bob") }, "carol", http.StatusNotFound, ""},
		{"an admin who is not in the conversation", func(f *forum) uint64 { return f.convs("alice", "bob") }, "admin", http.StatusNotFound, ""},
		{"a conversation that is not there", func(f *forum) uint64 { return 9999 }, "alice", http.StatusNotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			conv := tt.setup(f)
			rec := f.call(tt.user, http.MethodPost, path("/conversations/%d/call", conv), map[string]any{"video": true})
			requireStatus(t, rec, tt.want)
			if tt.message != "" && errorFields(t, rec)["form"] != tt.message {
				t.Errorf("message = %v, want %q", errorFields(t, rec)["form"], tt.message)
			}
		})
	}

	t.Run("calls are turned off", func(t *testing.T) {
		t.Parallel()
		f2 := newForum(t)
		conv := f2.convs("alice", "bob")
		off := app.NewCalls(f2.st, f2.clk, app.CallsConfig{ProcessID: "0123456789abcdef"}, f2.hub)
		f2.remake(func(o *Options) { o.Services.Calls = off })
		rec := f2.call("alice", http.MethodPost, path("/conversations/%d/call", conv), map[string]any{"video": true})
		requireStatus(t, rec, http.StatusConflict)
		if errorFields(t, rec)["form"] != "Calls are turned off on this server." {
			t.Errorf("fields = %v", errorFields(t, rec))
		}
	})

	t.Run("the body", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		conv := f.convs("alice", "bob")
		p := path("/conversations/%d/call", conv)
		for _, body := range []any{"{", `{"video": "yes"}`, `{"video": true, "extra": 1}`} {
			rec := f.call("alice", http.MethodPost, p, body)
			if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("body %v: status = %d", body, rec.Code)
			}
		}
		requireStatus(t, f.call("alice", http.MethodPost, p, "[]"), http.StatusUnprocessableEntity)
	})
}

// TestCallSocketRefusals checks what the signalling socket refuses before the
// upgrade; the happy path, with real media, is in package calls.
func TestCallSocketRefusals(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	conv := f.convs("alice", "bob")
	group := f.convs("alice", "bob", "carol")
	socket := func(id uint64) string { return path("/conversations/%d/call/ws", id) }

	tests := []struct {
		name string
		user string
		path string
		want int
	}{
		{"no call yet", "alice", socket(conv), http.StatusConflict},
		{"a group", "alice", socket(group), http.StatusConflict},
		{"a stranger", "carol", socket(conv), http.StatusNotFound},
		{"a conversation that is not there", "alice", socket(9999), http.StatusNotFound},
	}
	for _, tt := range tests {
		rec := f.call(tt.user, http.MethodGet, tt.path, nil)
		if rec.Code != tt.want {
			t.Errorf("%s: status = %d, want %d; body:\n%s", tt.name, rec.Code, tt.want, rec.Body)
		}
	}

	// With a call that rings the callee may not join; the caller is let in as
	// far as the missing media server: this server has none.
	f.callOK("alice", http.MethodPost, path("/conversations/%d/call", conv), map[string]any{"video": true}, http.StatusCreated)
	if rec := f.call("bob", http.MethodGet, socket(conv), nil); rec.Code != http.StatusConflict {
		t.Errorf("the callee of a ringing call: status = %d; body:\n%s", rec.Code, rec.Body)
	}
	rec := f.call("alice", http.MethodGet, socket(conv), nil)
	requireStatus(t, rec, http.StatusConflict)
	if errorFields(t, rec)["form"] != "Calls are turned off on this server." || !strings.Contains(rec.Body.String(), "conflict") {
		t.Errorf("a server with no media server answered %s", rec.Body)
	}
}

func TestCallEventsOnTheWebSocket(t *testing.T) {
	t.Parallel()
	w := newWSForum(t)
	conv := w.convs("alice", "bob")
	callPath := path("/conversations/%d/call", conv)
	own := topicOf(topicUser, w.userID("alice"))
	bobOwn := topicOf(topicUser, w.userID("bob"))
	alice, bob := w.dial("alice"), w.dial("bob")

	start := w.callOK("alice", http.MethodPost, callPath, map[string]any{"video": true}, http.StatusCreated)
	ev := bob.await(bobOwn, live.KindCallRinging)
	if ev["call_id"] != start["id"] || ev["conversation_id"] != float64(conv) {
		t.Errorf("call.ringing = %v", ev)
	}
	sameJSON(t, "call.ringing data", ev["data"], w.callOK("bob", http.MethodGet, callPath, nil, http.StatusOK))
	if data := obj(t, ev["data"]); data["incoming"] != true || data["state"] != "ringing" {
		t.Errorf("the ringing call = %v", data)
	}
	// The caller's own sockets hear of it too, as the caller sees the call.
	ev = alice.await(own, live.KindCallStarted)
	if data := obj(t, ev["data"]); data["incoming"] != false || data["state"] != "ringing" {
		t.Errorf("call.started data = %v", data)
	}

	w.callOK("bob", http.MethodPost, callPath+"/answer", map[string]any{"video": true}, http.StatusOK)
	ev = alice.await(own, live.KindCallStarted)
	if data := obj(t, ev["data"]); data["state"] != "active" {
		t.Errorf("call.started (answered) data = %v", data)
	}

	// The media server reports a participant's microphone and camera.
	if err := w.calls.SetMedia(t.Context(), actorOf(w.testEnv, "bob"), uint64(start["id"].(float64)), false, true); err != nil {
		t.Fatalf("SetMedia: %+v", err)
	}
	ev = alice.await(own, live.KindCallUpdated)
	if ev["user_id"] != float64(w.userID("bob")) || ev["microphone"] != false || ev["camera"] != true {
		t.Errorf("call.updated = %v", ev)
	}

	w.callOK("alice", http.MethodPost, callPath+"/hangup", nil, http.StatusOK)
	ev = bob.await(bobOwn, live.KindCallEnded)
	if data := obj(t, ev["data"]); data["state"] != "ended" || data["end_reason"] != "hangup" {
		t.Errorf("call.ended data = %v", data)
	}
}
