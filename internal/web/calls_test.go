package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
)

// callBetween makes a call of the caller to the callee in their direct
// conversation, answers it when answer is true and returns it.
func (e *roomsEnv) callBetween(conv uint64, from, to string, answer bool) app.Call {
	e.t.Helper()
	call, err := e.calls.Start(e.t.Context(), e.actor(e.users(from)), conv, true)
	if err != nil {
		e.t.Fatalf("Start: %+v", err)
	}
	if answer {
		if call, err = e.calls.Answer(e.t.Context(), e.actor(e.users(to)), call.ID, true); err != nil {
			e.t.Fatalf("Answer: %+v", err)
		}
	}
	return call
}

func TestTheConversationPageShowsTheEntryOfACall(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	conv := e.direct("alice", "bob")
	hello := e.say("alice", conv, "Hello")
	call := e.callBetween(conv, "alice", "bob", true)
	e.clk.Advance(125 * time.Second)
	if _, err := e.calls.HangUp(t.Context(), e.actor(e.users("bob")), call.ID); err != nil {
		t.Fatalf("HangUp: %+v", err)
	}

	for _, user := range []string{"alice", "bob"} {
		rec := e.send(get(convPath(conv), e.cookies[user]))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", user, rec.Code)
		}
		body := rec.Body.String()
		entries := strings.Count(body, `class="message-call-entry"`)
		if entries != 1 {
			t.Fatalf("%s: the page has %d call entries, want 1:\n%s", user, entries, body)
		}
		at := strings.Index(body, `class="message-call-entry"`)
		item := body[strings.LastIndex(body[:at], `<li id="msg-`):]
		item = item[:strings.Index(item, "</li>")]
		requireContains(t, item, []string{`data-call="` + idText(call.ID) + `"`, "Video call", "2 min"},
			// A call entry is one line: nothing to reply to, edit, delete or react to.
			[]string{"message-reply-link", "message-edit-link", "message-delete", "reaction-bar", "message-body"})
		if strings.Index(body, `id="msg-`+idText(hello.ID)+`"`) > at {
			t.Errorf("%s: the entry comes before the message that was written before the call", user)
		}
	}
}

func TestTheCallSocketRefusesWhatItShould(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	conv := e.direct("alice", "bob")
	group := e.group("trio", "alice", "bob", "carol")
	socket := func(id uint64) string { return "/calls/" + idText(id) + "/ws" }

	t.Run("no session goes to the login page", func(t *testing.T) {
		rec := e.send(get(socket(conv)))
		if rec.Code != http.StatusSeeOther && rec.Code != http.StatusFound && rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d", rec.Code)
		}
		if rec.Code != http.StatusUnauthorized && !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
			t.Errorf("Location = %q", rec.Header().Get("Location"))
		}
	})

	tests := []struct {
		name  string
		user  string
		conv  uint64
		setup func()
		want  int
	}{
		{name: "a conversation with no call", user: "alice", conv: conv, want: http.StatusConflict},
		{name: "a group", user: "alice", conv: group, want: http.StatusConflict},
		{name: "a conversation that is not there", user: "alice", conv: 9999, want: http.StatusNotFound},
		{
			name: "a stranger", user: "carol", conv: conv, want: http.StatusNotFound,
			setup: func() { e.callBetween(conv, "alice", "bob", false) },
		},
		{name: "the callee of a call that rings", user: "bob", conv: conv, want: http.StatusConflict},
		{name: "a server with no media server", user: "alice", conv: conv, want: http.StatusConflict},
	}
	for _, tt := range tests {
		if tt.setup != nil {
			tt.setup()
		}
		rec := e.send(get(socket(tt.conv), e.cookies[tt.user]))
		if rec.Code != tt.want {
			t.Errorf("%s: status = %d, want %d", tt.name, rec.Code, tt.want)
			continue
		}
	}

	t.Run("a path that is not a number", func(t *testing.T) {
		rec := e.send(get("/calls/abc/ws", e.cookies["alice"]))
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})
}
