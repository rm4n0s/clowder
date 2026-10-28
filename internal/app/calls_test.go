package app_test

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/live"
)

// callEnv is the calls service on the database of msgEnv, with the accounts
// service publishing to the same recorder, so that the events of disabling a
// user and of ending a session are seen.
type callEnv struct {
	*msgEnv
	calls *app.Calls
	// ab is the direct conversation of alice and bob, cd that of carol and dave.
	ab, cd uint64
	// trio is a group of alice, bob and carol.
	trio uint64
}

const callTestProcess = "0123456789abcdef"

func newCallEnv(t *testing.T) *callEnv {
	t.Helper()
	return newCallEnvWith(t, app.CallsConfig{ProcessID: callTestProcess, Enabled: true, MaxCalls: 20})
}

func newCallEnvWith(t *testing.T, cfg app.CallsConfig) *callEnv {
	t.Helper()
	e := &callEnv{msgEnv: newMsgEnv(t)}
	e.svc = app.NewAccounts(e.st, e.clk, auth.FastHasher(), app.AccountsConfig{
		InvitationLifetime: lifetime, SessionLifetime: sessionLifetime, Publisher: e.rec,
	})
	e.calls = app.NewCalls(e.st, e.clk, cfg, e.rec)
	e.ab = e.direct("alice", "bob")
	e.cd = e.direct("carol", "dave")
	e.trio = e.group("trio", "alice", "bob", "carol")
	e.rec.Reset()
	return e
}

// start starts a call and fails the test on an error.
func (e *callEnv) start(from string, conv uint64, video bool) app.Call {
	e.t.Helper()
	c, err := e.calls.Start(e.t.Context(), e.as(from), conv, video)
	if err != nil {
		e.t.Fatalf("Start %s: %+v", from, err)
	}
	return c
}

// answer answers the call as the user and fails the test on an error.
func (e *callEnv) answer(user string, id uint64) app.Call {
	e.t.Helper()
	c, err := e.calls.Answer(e.t.Context(), e.as(user), id, true)
	if err != nil {
		e.t.Fatalf("Answer %s: %+v", user, err)
	}
	return c
}

// kinds returns the kinds of the recorded events, in order.
func (e *callEnv) kinds() []string {
	var out []string
	for _, ev := range e.rec.Events() {
		out = append(out, ev.Kind)
	}
	return out
}

// eventsOf returns the recorded events of the kind.
func (e *callEnv) eventsOf(kind string) []live.Event {
	var out []live.Event
	for _, ev := range e.rec.Events() {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

// entries returns the call entries of the conversation as the user reads them.
func (e *callEnv) entries(user string, conv uint64) []app.Message {
	e.t.Helper()
	var out []app.Message
	for _, m := range e.history(user, conv) {
		if m.Kind == app.MessageCall {
			out = append(out, m)
		}
	}
	return out
}

func TestCallsStart(t *testing.T) {
	t.Parallel()
	e := newCallEnv(t)
	call := e.start("alice", e.ab, true)
	if call.ID == 0 || call.ConversationID != e.ab || call.Caller.ID != e.uid("alice") || call.Callee.ID != e.uid("bob") ||
		!call.Video || call.State != app.CallRinging || !call.CreatedAt.Equal(start) || call.AnsweredAt != nil ||
		call.EndedAt != nil || call.EndReason != "" || call.Home != callTestProcess || call.Incoming {
		t.Errorf("call = %+v", call)
	}
	// The event kinds and who hears them: the callee rings, both start.
	want := []live.Event{
		{Topic: live.UserTopic(e.uid("bob")), Kind: live.KindCallRinging, UserID: e.uid("bob"), ConversationID: e.ab, CallID: call.ID},
		{Topic: live.UserTopic(e.uid("alice")), Kind: live.KindCallStarted, UserID: e.uid("alice"), ConversationID: e.ab, CallID: call.ID},
		{Topic: live.UserTopic(e.uid("bob")), Kind: live.KindCallStarted, UserID: e.uid("bob"), ConversationID: e.ab, CallID: call.ID},
	}
	got := e.rec.Events()
	if len(got) != len(want) {
		t.Fatalf("events = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// The other side reads the same call as incoming.
	seen, ok, err := e.calls.Current(t.Context(), e.as("bob"), e.ab)
	if err != nil || !ok || seen.ID != call.ID || !seen.Incoming {
		t.Errorf("Current as bob = %+v, %v, %v", seen, ok, err)
	}
	if _, ok, err := e.calls.Current(t.Context(), e.as("carol"), e.cd); err != nil || ok {
		t.Errorf("Current of a conversation with no call = %v, %v", ok, err)
	}
	if other := call.Other(e.uid("alice")); other.ID != e.uid("bob") {
		t.Errorf("Other = %+v", other)
	}
	// The call is not a message until it ends.
	if got := e.entries("alice", e.ab); len(got) != 0 {
		t.Errorf("entries while ringing = %+v", got)
	}
}

func TestCallsRefusals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		cfg   app.CallsConfig
		setup func(e *callEnv)
		from  string
		conv  func(e *callEnv) uint64
		tag   string
		route string
		// message is what the refusal tells people.
		message string
	}{
		{
			name: "a group conversation", from: "alice", conv: func(e *callEnv) uint64 { return e.trio },
			tag: app.TagCallGroup, route: "Calls.Start.func1.CallGroup", message: "Calls are for conversations of two people.",
		},
		{
			name: "calls are off", cfg: app.CallsConfig{ProcessID: callTestProcess}, from: "alice", conv: func(e *callEnv) uint64 { return e.ab },
			tag: app.TagCallsOff, route: "Calls.Start.func1.CallsOff", message: "Calls are turned off on this server.",
		},
		{
			name: "a disabled callee", setup: func(e *callEnv) { e.disable("bob") }, from: "alice", conv: func(e *callEnv) uint64 { return e.ab },
			tag: app.TagCallCalleeDisabled, route: "Calls.Start.func1.CallCalleeDisabled", message: "Bob is disabled.",
		},
		{
			name: "a caller in another call", setup: func(e *callEnv) { e.start("alice", e.ab, false) },
			from: "alice", conv: func(e *callEnv) uint64 { return e.ab },
			tag: app.TagCallCallerBusy, route: "Calls.Start.func1.CallCallerBusy", message: "You are in another call.",
		},
		{
			name: "the callee calls back while the call rings", setup: func(e *callEnv) { e.start("alice", e.ab, false) },
			from: "bob", conv: func(e *callEnv) uint64 { return e.ab },
			tag: app.TagCallCallerBusy, route: "Calls.Start.func1.CallCallerBusy", message: "You are in another call.",
		},
		{
			name: "a callee in another call", setup: func(e *callEnv) { e.start("carol", e.cd, false) },
			from: "dave", conv: func(e *callEnv) uint64 { return e.cd },
			tag: app.TagCallCallerBusy, route: "Calls.Start.func1.CallCallerBusy", message: "You are in another call.",
		},
		{
			name: "too many calls", cfg: app.CallsConfig{ProcessID: callTestProcess, Enabled: true, MaxCalls: 1},
			setup: func(e *callEnv) { e.start("carol", e.cd, false) }, from: "alice", conv: func(e *callEnv) uint64 { return e.ab },
			tag: app.TagCallServerFull, route: "Calls.Start.func1.CallServerFull", message: "Too many calls on this server right now.",
		},
		{
			name: "a conversation that is not there", from: "alice", conv: func(e *callEnv) uint64 { return 9999 },
			tag: app.TagNotFound, route: "Calls.Start.func1->loadConversation->Store.ConversationByID.NotFound",
		},
		{
			name: "a conversation of two others", from: "alice", conv: func(e *callEnv) uint64 { return e.cd },
			tag: app.TagNotFound, route: "Calls.Start.func1->loadConversation.NotFound",
		},
		{
			name: "an admin who is not a member", from: "admin", conv: func(e *callEnv) uint64 { return e.ab },
			tag: app.TagNotFound, route: "Calls.Start.func1->loadConversation.NotFound",
		},
		{
			name: "a disabled caller", setup: func(e *callEnv) { e.disable("alice") }, from: "alice", conv: func(e *callEnv) uint64 { return e.ab },
			tag: app.TagUnauthenticated, route: "Calls.Start.func1->checkActive.Unauthenticated",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := tt.cfg
			if cfg == (app.CallsConfig{}) {
				cfg = app.CallsConfig{ProcessID: callTestProcess, Enabled: true, MaxCalls: 20}
			}
			e := newCallEnvWith(t, cfg)
			if tt.setup != nil {
				tt.setup(e)
			}
			e.rec.Reset()
			_, err := e.calls.Start(t.Context(), e.as(tt.from), tt.conv(e), true)
			appErr := requireTag(t, err, tt.tag, tt.route)
			if tt.message != "" {
				if got := fieldsOf(t, appErr)[app.FieldForm]; got != tt.message {
					t.Errorf("message = %q, want %q", got, tt.message)
				}
				if !app.IsCallRefusal(appErr.Tag) {
					t.Errorf("%s is not a refusal of the calls", appErr.Tag)
				}
			}
			if got := e.kinds(); len(got) != 0 {
				t.Errorf("a refused start published %v", got)
			}
		})
	}
}

func TestCallsBusyCalleeGetsAMissedCall(t *testing.T) {
	t.Parallel()
	e := newCallEnv(t)
	// bob is in a call with carol; alice calls bob.
	bobAndCarol := e.direct("bob", "carol")
	e.start("bob", bobAndCarol, false)
	e.rec.Reset()

	_, err := e.calls.Start(t.Context(), e.as("alice"), e.ab, true)
	appErr := requireTag(t, err, app.TagCallCalleeBusy, "Calls.Start.func1.CallCalleeBusy")
	if got := fieldsOf(t, appErr)[app.FieldForm]; got != "Bob is in another call." {
		t.Errorf("message = %q", got)
	}
	// The call was written, as ended and busy, with its entry.
	if _, ok, err := e.calls.Current(t.Context(), e.as("alice"), e.ab); err != nil || ok {
		t.Errorf("Current = %v, %v; want no call in progress", ok, err)
	}
	if got := e.entries("bob", e.ab); len(got) != 1 || got[0].Call == nil || got[0].Call.Text != "Missed video call" || got[0].Call.Outcome != app.CallBusyOutcome {
		t.Errorf("entry for the callee = %+v", got)
	}
	if got := e.entries("alice", e.ab); len(got) != 1 || got[0].Call.Text != "Video call · busy" {
		t.Errorf("entry for the caller = %+v", got)
	}
	// The callee's unread count rose like for a message; the caller's did not.
	if n := e.unread("bob"); n != 1 {
		t.Errorf("unread of bob = %d, want 1", n)
	}
	if n := e.unread("alice"); n != 0 {
		t.Errorf("unread of alice = %d, want 0", n)
	}
	// The conversation hears the entry; nothing rings.
	if got := e.eventsOf(live.KindCallRinging); len(got) != 0 {
		t.Errorf("a busy call rang: %+v", got)
	}
	if got := e.eventsOf(live.KindMessageCreated); len(got) != 1 {
		t.Errorf("message.created events = %+v, want 1", got)
	}
}

// callStates puts a call in a state, as the actors alice (caller) and bob.
func (e *callEnv) inState(state string) app.Call {
	e.t.Helper()
	call := e.start("alice", e.ab, true)
	switch state {
	case "ringing":
	case "active":
		call = e.answer("bob", call.ID)
	case "ended":
		call = e.answer("bob", call.ID)
		c, err := e.calls.HangUp(e.t.Context(), e.as("alice"), call.ID)
		if err != nil {
			e.t.Fatalf("HangUp: %+v", err)
		}
		call = c
	default:
		e.t.Fatalf("no state %q", state)
	}
	e.rec.Reset()
	return call
}

func TestCallsPermissions(t *testing.T) {
	t.Parallel()
	type method struct {
		name  string
		state string
		call  func(e *callEnv, a app.Actor, c app.Call) error
		// who maps the actors to the tag they get, "" for success.
		want map[string]string
		// route is the route of a refusal that is not the 404 of a stranger.
		forbiddenRoute string
	}
	notFound := app.TagNotFound
	strangers := map[string]string{"carol": notFound, "admin": notFound, "erin": notFound}
	merge := func(ms ...map[string]string) map[string]string {
		out := map[string]string{}
		for _, m := range ms {
			for k, v := range m {
				out[k] = v
			}
		}
		return out
	}
	methods := []method{
		{"Answer", "ringing", func(e *callEnv, a app.Actor, c app.Call) error {
			_, err := e.calls.Answer(t.Context(), a, c.ID, true)
			return err
		},
			merge(strangers, map[string]string{"alice": app.TagForbidden, "bob": ""}), "Calls.Answer.func1.Forbidden"},
		{"Decline", "ringing", func(e *callEnv, a app.Actor, c app.Call) error {
			_, err := e.calls.Decline(t.Context(), a, c.ID)
			return err
		},
			merge(strangers, map[string]string{"alice": app.TagForbidden, "bob": ""}), "Calls.finish.func1.Forbidden"},
		{"Cancel", "ringing", func(e *callEnv, a app.Actor, c app.Call) error {
			_, err := e.calls.Cancel(t.Context(), a, c.ID)
			return err
		},
			merge(strangers, map[string]string{"alice": "", "bob": app.TagForbidden}), "Calls.finish.func1.Forbidden"},
		{"HangUp", "active", func(e *callEnv, a app.Actor, c app.Call) error {
			_, err := e.calls.HangUp(t.Context(), a, c.ID)
			return err
		},
			merge(strangers, map[string]string{"alice": "", "bob": ""}), ""},
		{"Get", "ringing", func(e *callEnv, a app.Actor, c app.Call) error {
			_, err := e.calls.Get(t.Context(), a, c.ID)
			return err
		},
			merge(strangers, map[string]string{"alice": "", "bob": ""}), ""},
		{"SetMedia", "active", func(e *callEnv, a app.Actor, c app.Call) error {
			return e.calls.SetMedia(t.Context(), a, c.ID, true, false)
		}, merge(strangers, map[string]string{"alice": "", "bob": ""}), ""},
		{"Current", "ringing", func(e *callEnv, a app.Actor, c app.Call) error {
			_, _, err := e.calls.Current(t.Context(), a, c.ConversationID)
			return err
		}, merge(strangers, map[string]string{"alice": "", "bob": ""}), ""},
		{"Open", "ringing", func(e *callEnv, a app.Actor, c app.Call) error {
			_, err := e.calls.Open(t.Context(), a, c.ConversationID)
			return err
		}, merge(strangers, map[string]string{"alice": "", "bob": ""}), ""},
		{"Start", "", func(e *callEnv, a app.Actor, c app.Call) error {
			_, err := e.calls.Start(t.Context(), a, e.ab, true)
			return err
		}, merge(strangers, map[string]string{"alice": "", "bob": ""}), ""},
	}
	for _, m := range methods {
		for _, who := range []string{"alice", "bob", "carol", "admin", "erin"} {
			t.Run(m.name+" as "+who, func(t *testing.T) {
				t.Parallel()
				e := newCallEnv(t)
				var call app.Call
				if m.state != "" {
					call = e.inState(m.state)
				}
				err := m.call(e, e.as(who), call)
				switch want := m.want[who]; want {
				case "":
					if err != nil {
						t.Fatalf("got %+v, want success", err)
					}
				case app.TagForbidden:
					_ = requireTag(t, err, want, m.forbiddenRoute)
				default:
					// A stranger gets "not found" with no hint that the call exists.
					appErr := requireTag(t, err, want, "")
					if appErr.Tag != app.TagNotFound {
						t.Errorf("tag = %s", appErr.Tag)
					}
				}
			})
		}
	}

	// The members of a group conversation have no calls; a disabled user is
	// not served.
	t.Run("a group conversation", func(t *testing.T) {
		t.Parallel()
		e := newCallEnv(t)
		_, _, err := e.calls.Current(t.Context(), e.as("alice"), e.trio)
		_ = requireTag(t, err, app.TagCallGroup, "Calls.Current.CallGroup")
		_, err = e.calls.Open(t.Context(), e.as("alice"), e.trio)
		_ = requireTag(t, err, app.TagCallGroup, "Calls.Open->Calls.Current.CallGroup")
		_, err = e.calls.Start(t.Context(), e.as("carol"), e.trio, true)
		_ = requireTag(t, err, app.TagCallGroup, "Calls.Start.func1.CallGroup")
	})
	for _, name := range []string{"Answer", "Decline", "Cancel", "HangUp", "Get", "SetMedia", "Current", "Start"} {
		t.Run(name+" as a disabled user", func(t *testing.T) {
			t.Parallel()
			e := newCallEnv(t)
			call := e.inState("active")
			e.disable("alice")
			a := e.as("alice")
			ctx := t.Context()
			var err error
			switch name {
			case "Answer":
				_, err = e.calls.Answer(ctx, a, call.ID, true)
			case "Decline":
				_, err = e.calls.Decline(ctx, a, call.ID)
			case "Cancel":
				_, err = e.calls.Cancel(ctx, a, call.ID)
			case "HangUp":
				_, err = e.calls.HangUp(ctx, a, call.ID)
			case "Get":
				_, err = e.calls.Get(ctx, a, call.ID)
			case "SetMedia":
				err = e.calls.SetMedia(ctx, a, call.ID, true, true)
			case "Current":
				_, _, err = e.calls.Current(ctx, a, e.ab)
			case "Start":
				_, err = e.calls.Start(ctx, a, e.ab, true)
			}
			appErr := requireTag(t, err, app.TagUnauthenticated, "checkActive.Unauthenticated")
			_ = appErr
		})
	}
}

func TestCallsStateMachine(t *testing.T) {
	t.Parallel()
	answer := func(who string) func(e *callEnv, c app.Call) (app.Call, error) {
		return func(e *callEnv, c app.Call) (app.Call, error) {
			return e.calls.Answer(t.Context(), e.as(who), c.ID, true)
		}
	}
	decline := func(who string) func(e *callEnv, c app.Call) (app.Call, error) {
		return func(e *callEnv, c app.Call) (app.Call, error) { return e.calls.Decline(t.Context(), e.as(who), c.ID) }
	}
	cancel := func(who string) func(e *callEnv, c app.Call) (app.Call, error) {
		return func(e *callEnv, c app.Call) (app.Call, error) { return e.calls.Cancel(t.Context(), e.as(who), c.ID) }
	}
	hangUp := func(who string, after time.Duration) func(e *callEnv, c app.Call) (app.Call, error) {
		return func(e *callEnv, c app.Call) (app.Call, error) {
			e.clk.Advance(after)
			return e.calls.HangUp(t.Context(), e.as(who), c.ID)
		}
	}
	tests := []struct {
		name  string
		state string
		do    func(e *callEnv, c app.Call) (app.Call, error)
		// wantTag is the refusal, "" for success.
		wantTag    string
		wantState  app.CallState
		wantReason app.CallEndReason
		wantEnder  string
		// alice and bob are the entries the two read afterwards ("" for none).
		alice, bob string
		// bobUnread is the number of unread conversations of bob afterwards.
		bobUnread int
	}{
		{name: "answer a ringing call", state: "ringing", do: answer("bob"), wantState: app.CallActive},
		{name: "decline a ringing call", state: "ringing", do: decline("bob"), wantState: app.CallEnded, wantReason: app.CallEndDeclined,
			wantEnder: "bob", alice: "Declined video call", bob: "Declined video call"},
		{name: "cancel a ringing call", state: "ringing", do: cancel("alice"), wantState: app.CallEnded, wantReason: app.CallEndCancelled,
			wantEnder: "alice", alice: "Cancelled video call", bob: "Missed video call", bobUnread: 1},
		{name: "hang up a call that rings", state: "ringing", do: hangUp("alice", 0), wantTag: app.TagCallWrongState},
		{name: "answer an active call", state: "active", do: answer("bob"), wantTag: app.TagCallWrongState},
		{name: "decline an active call", state: "active", do: decline("bob"), wantTag: app.TagCallWrongState},
		{name: "cancel an active call", state: "active", do: cancel("alice"), wantTag: app.TagCallWrongState},
		{name: "the caller hangs up an active call", state: "active", do: hangUp("alice", 12*time.Minute+30*time.Second),
			wantState: app.CallEnded, wantReason: app.CallEndHangup, wantEnder: "alice", alice: "Video call · 12 min", bob: "Video call · 12 min"},
		{name: "the callee hangs up an active call", state: "active", do: hangUp("bob", 45*time.Second),
			wantState: app.CallEnded, wantReason: app.CallEndHangup, wantEnder: "bob", alice: "Video call · 45 s", bob: "Video call · 45 s"},
		{name: "answer an ended call", state: "ended", do: answer("bob"), wantTag: app.TagCallWrongState},
		{name: "decline an ended call", state: "ended", do: decline("bob"), wantTag: app.TagCallWrongState},
		{name: "cancel an ended call", state: "ended", do: cancel("alice"), wantTag: app.TagCallWrongState},
		{name: "hang up an ended call", state: "ended", do: hangUp("alice", 0), wantTag: app.TagCallWrongState},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newCallEnv(t)
			call := e.inState(tt.state)
			entriesBefore := len(e.entries("alice", e.ab))
			got, err := tt.do(e, call)
			if tt.wantTag != "" {
				appErr := requireTag(t, err, tt.wantTag, "CallWrongState")
				if msg := fieldsOf(t, appErr)[app.FieldForm]; !strings.HasPrefix(msg, "This call is not ") {
					t.Errorf("message = %q", msg)
				}
				if kinds := e.kinds(); len(kinds) != 0 {
					t.Errorf("a refused change published %v", kinds)
				}
				if n := len(e.entries("alice", e.ab)); n != entriesBefore {
					t.Errorf("a refused change wrote an entry")
				}
				return
			}
			if err != nil {
				t.Fatalf("got %+v", err)
			}
			if got.State != tt.wantState || got.EndReason != tt.wantReason || got.ID != call.ID {
				t.Errorf("call = %+v, want state %s reason %q", got, tt.wantState, tt.wantReason)
			}
			if tt.wantEnder != "" && got.EndedBy != e.uid(tt.wantEnder) {
				t.Errorf("ended by %d, want %s", got.EndedBy, tt.wantEnder)
			}
			if tt.wantState == app.CallEnded && got.EndedAt == nil {
				t.Error("an ended call has no end time")
			}
			for who, want := range map[string]string{"alice": tt.alice, "bob": tt.bob} {
				entries := e.entries(who, e.ab)
				if want == "" {
					if len(entries) != entriesBefore {
						t.Errorf("%s reads %d entries, want none new", who, len(entries))
					}
					continue
				}
				if len(entries) != entriesBefore+1 {
					t.Fatalf("%s reads %d entries, want one new", who, len(entries))
				}
				m := entries[len(entries)-1]
				if m.Call == nil || m.Call.Text != want || m.Body != "" || m.Author.ID != e.uid("alice") || m.CanEdit || m.CanDelete || m.CanReact || m.Call.CallID != call.ID {
					t.Errorf("%s reads %+v (call %+v), want the entry %q written by alice", who, m, m.Call, want)
				}
			}
			if n := e.unread("bob"); n != tt.bobUnread {
				t.Errorf("unread of bob = %d, want %d", n, tt.bobUnread)
			}
			kinds := e.kinds()
			switch tt.wantState {
			case app.CallActive:
				if strings.Join(kinds, ",") != live.KindCallStarted+","+live.KindCallStarted {
					t.Errorf("events = %v, want two call.started", kinds)
				}
			case app.CallEnded:
				if n := len(e.eventsOf(live.KindCallEnded)); n != 2 {
					t.Errorf("%d call.ended events, want 2 (%v)", n, kinds)
				}
				if n := len(e.eventsOf(live.KindMessageCreated)); n != 1 {
					t.Errorf("%d message.created events, want 1 (%v)", n, kinds)
				}
				if n := len(e.eventsOf(live.KindConversationChanged)); n != 2 {
					t.Errorf("%d conversation.changed events, want 2 (%v)", n, kinds)
				}
			}
			// The user is free to call again once the call is over.
			if tt.wantState == app.CallEnded {
				again, err := e.calls.Start(t.Context(), e.as("bob"), e.ab, false)
				if err != nil || again.ID == call.ID || again.Video {
					t.Errorf("a new call after the end = %+v, %v", again, err)
				}
			}
		})
	}
}

func TestCallEntryTexts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		video bool
		end   func(e *callEnv, c app.Call)
		alice string
		bob   string
		// outcome and seconds of the entry.
		outcome app.CallOutcome
		seconds int
	}{
		{"audio call of 3 seconds", false, func(e *callEnv, c app.Call) {
			e.answer("bob", c.ID)
			e.clk.Advance(3 * time.Second)
			mustCall(t)(e.calls.HangUp(t.Context(), e.as("bob"), c.ID))
		}, "Audio call · 3 s", "Audio call · 3 s", app.CallCompleted, 3},
		{"video call of 59 seconds", true, func(e *callEnv, c app.Call) {
			e.answer("bob", c.ID)
			e.clk.Advance(59 * time.Second)
			mustCall(t)(e.calls.HangUp(t.Context(), e.as("bob"), c.ID))
		}, "Video call · 59 s", "Video call · 59 s", app.CallCompleted, 59},
		{"video call of exactly an hour", true, func(e *callEnv, c app.Call) {
			e.answer("bob", c.ID)
			e.clk.Advance(time.Hour)
			mustCall(t)(e.calls.HangUp(t.Context(), e.as("alice"), c.ID))
		}, "Video call · 1 h", "Video call · 1 h", app.CallCompleted, 3600},
		{"video call of an hour and 5 minutes", true, func(e *callEnv, c app.Call) {
			e.answer("bob", c.ID)
			e.clk.Advance(time.Hour + 5*time.Minute + 20*time.Second)
			mustCall(t)(e.calls.HangUp(t.Context(), e.as("alice"), c.ID))
		}, "Video call · 1 h 5 min", "Video call · 1 h 5 min", app.CallCompleted, 3920},
		{"declined audio call", false, func(e *callEnv, c app.Call) {
			mustCall(t)(e.calls.Decline(t.Context(), e.as("bob"), c.ID))
		}, "Declined audio call", "Declined audio call", app.CallDeclinedOutcome, 0},
		{"cancelled audio call", false, func(e *callEnv, c app.Call) {
			mustCall(t)(e.calls.Cancel(t.Context(), e.as("alice"), c.ID))
		}, "Cancelled audio call", "Missed audio call", app.CallCancelledOutcome, 0},
		{"missed video call", true, func(e *callEnv, c app.Call) {
			e.clk.Advance(45 * time.Second)
			if n, err := e.calls.Expire(t.Context()); err != nil || n != 1 {
				t.Fatalf("Expire = %d, %v", n, err)
			}
		}, "No answer", "Missed video call", app.CallMissedOutcome, 0},
		{"a failed connection after the answer", true, func(e *callEnv, c app.Call) {
			e.answer("bob", c.ID)
			e.clk.Advance(2 * time.Second)
			if err := e.calls.Fail(t.Context(), c.ID); err != nil {
				t.Fatalf("Fail: %+v", err)
			}
		}, "Video call · 2 s", "Video call · 2 s", app.CallCompleted, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newCallEnv(t)
			call := e.start("alice", e.ab, tt.video)
			tt.end(e, call)
			for who, want := range map[string]string{"alice": tt.alice, "bob": tt.bob} {
				entries := e.entries(who, e.ab)
				if len(entries) != 1 || entries[0].Call == nil {
					t.Fatalf("%s reads %+v", who, entries)
				}
				c := entries[0].Call
				if c.Text != want || c.Outcome != tt.outcome || c.DurationSeconds != tt.seconds || c.Video != tt.video {
					t.Errorf("%s reads %+v, want %q %s %d s", who, c, want, tt.outcome, tt.seconds)
				}
			}
		})
	}
}

// mustCall fails the test when a service call failed.
func mustCall(t *testing.T) func(app.Call, error) {
	return func(_ app.Call, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%+v", err)
		}
	}
}

func TestCallEntriesInTheConversation(t *testing.T) {
	t.Parallel()
	e := newCallEnv(t)
	first := e.send("alice", e.ab, "hello")
	call := e.start("alice", e.ab, true)
	e.clk.Advance(10 * time.Second)
	mustCall(t)(e.calls.Cancel(t.Context(), e.as("alice"), call.ID))
	list, err := e.msgs.Conversations(t.Context(), e.as("bob"))
	if err != nil {
		t.Fatal(err)
	}
	var summary app.ConversationSummary
	for _, s := range list {
		if s.ID == e.ab {
			summary = s
		}
	}
	if summary.LastMessage == nil || summary.LastMessage.Preview != "Missed video call" || !summary.Unread {
		t.Errorf("the list shows %+v, unread %v; want the missed call as the last message", summary.LastMessage, summary.Unread)
	}
	mine, err := e.msgs.Conversations(t.Context(), e.as("alice"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range mine {
		if s.ID == e.ab && (s.LastMessage == nil || s.LastMessage.Preview != "Cancelled video call" || s.Unread) {
			t.Errorf("alice's list shows %+v, unread %v", s.LastMessage, s.Unread)
		}
	}
	// The entry cannot be changed: edited, deleted, reacted to or replied to.
	entries := e.entries("alice", e.ab)
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	id := entries[0].ID
	ctx := t.Context()
	_, err = e.msgs.Edit(ctx, e.as("alice"), id, "text")
	_ = requireTag(t, err, app.TagForbidden, "Messages.Edit.func1.Forbidden")
	err = e.msgs.Delete(ctx, e.as("alice"), id)
	_ = requireTag(t, err, app.TagForbidden, "Messages.Delete.func1.Forbidden")
	_, err = e.msgs.React(ctx, e.as("bob"), id, "\U0001F44D")
	_ = requireTag(t, err, app.TagForbidden, "Messages.React.func1.Forbidden")
	_, err = e.msgs.SetReaction(ctx, e.as("bob"), id, "\U0001F44D", true)
	_ = requireTag(t, err, app.TagForbidden, "Messages.SetReaction.func1.Forbidden")
	_, err = e.msgs.Send(ctx, e.as("bob"), e.ab, app.MessageInput{Body: "re", ReplyToID: id})
	appErr := requireTag(t, err, app.TagInvalidInput, "Messages.Send")
	if fieldsOf(t, appErr)[app.FieldReplyTo] == "" {
		t.Errorf("fields = %v, want a problem with the reply", fieldsOf(t, appErr))
	}
	// A search finds the text messages and no entry.
	if page := e.history("bob", e.ab); len(page) != 2 || page[0].ID != first.ID || page[1].Kind != app.MessageCall || page[0].Kind != app.MessageText {
		t.Errorf("history = %+v", page)
	}
	if got, err := e.msgs.Message(ctx, e.as("bob"), id); err != nil || got.Call == nil || got.Kind != app.MessageCall {
		t.Errorf("Message = %+v, %v", got, err)
	}
}

func TestCallsExpire(t *testing.T) {
	t.Parallel()
	e := newCallEnv(t)
	ringing := e.start("alice", e.ab, true)
	answered := e.start("carol", e.cd, false)
	e.answer("dave", answered.ID)
	e.rec.Reset()

	tests := []struct {
		name    string
		advance time.Duration
		want    int
	}{
		{"just under 45 seconds", 44 * time.Second, 0},
		{"at 45 seconds", time.Second, 1},
		{"again, nothing is left", time.Minute, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e.clk.Advance(tt.advance)
			n, err := e.calls.Expire(t.Context())
			if err != nil || n != tt.want {
				t.Errorf("Expire = %d, %v; want %d", n, err, tt.want)
			}
		})
	}
	got, err := e.calls.Get(t.Context(), e.as("bob"), ringing.ID)
	if err != nil || got.State != app.CallEnded || got.EndReason != app.CallEndMissed || got.EndedBy != 0 {
		t.Errorf("the ringing call = %+v, %v; want ended and missed by the system", got, err)
	}
	if entries := e.entries("bob", e.ab); len(entries) != 1 || entries[0].Call.Text != "Missed video call" {
		t.Errorf("entries of bob = %+v", entries)
	}
	if entries := e.entries("alice", e.ab); len(entries) != 1 || entries[0].Call.Text != "No answer" {
		t.Errorf("entries of alice = %+v", entries)
	}
	still, err := e.calls.Get(t.Context(), e.as("carol"), answered.ID)
	if err != nil || still.State != app.CallActive {
		t.Errorf("the answered call = %+v, %v; want it still active", still, err)
	}
	if n := len(e.eventsOf(live.KindCallEnded)); n != 2 {
		t.Errorf("%d call.ended events, want 2", n)
	}
}

func TestDisablingAUserEndsTheirCall(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		state string
		who   string
		alice string
		bob   string
	}{
		{"the caller of a ringing call", "ringing", "alice", "No answer", "Missed video call"},
		{"the callee of a ringing call", "ringing", "bob", "No answer", "Missed video call"},
		{"the caller of an active call", "active", "alice", "Video call · 0 s", "Video call · 0 s"},
		{"the callee of an active call", "active", "bob", "Video call · 0 s", "Video call · 0 s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newCallEnv(t)
			call := e.inState(tt.state)
			e.disable(tt.who)
			got, err := e.calls.Get(t.Context(), e.as("alice"), call.ID)
			if tt.who == "alice" {
				// The disabled user is not served; the other side reads the call.
				got, err = e.calls.Get(t.Context(), e.as("bob"), call.ID)
			}
			if err != nil || got.State != app.CallEnded || got.EndReason != app.CallEndDisabled {
				t.Fatalf("call = %+v, %v; want ended, disabled", got, err)
			}
			if tt.who == "bob" {
				if entries := e.entries("alice", e.ab); len(entries) != 1 || entries[0].Call.Text != tt.alice {
					t.Errorf("entries of alice = %+v", entries)
				}
			} else if entries := e.entries("bob", e.ab); len(entries) != 1 || entries[0].Call.Text != tt.bob {
				t.Errorf("entries of bob = %+v", entries)
			}
			if n := len(e.eventsOf(live.KindCallEnded)); n != 2 {
				t.Errorf("%d call.ended events, want 2 (%v)", n, e.kinds())
			}
			// A user can call someone again after the other was disabled? No:
			// the disabled callee is refused; the free user can call another.
			if tt.who == "alice" {
				if _, err := e.calls.Start(t.Context(), e.as("bob"), e.ab, true); err == nil {
					t.Error("a call to a disabled user was started")
				}
			}
		})
	}
}

func TestEnablingAUserChangesNoCall(t *testing.T) {
	t.Parallel()
	e := newCallEnv(t)
	call := e.inState("active")
	if err := e.svc.SetDisabled(t.Context(), e.as("admin"), e.uid("carol"), true); err != nil {
		t.Fatal(err)
	}
	got, err := e.calls.Get(t.Context(), e.as("alice"), call.ID)
	if err != nil || got.State != app.CallActive {
		t.Errorf("the call of two others = %+v, %v; want it untouched", got, err)
	}
}

func TestASessionEndingEndsTheCall(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		end  func(e *callEnv, token string, sessionID uint64) error
	}{
		{"logout", func(e *callEnv, token string, _ uint64) error { return e.svc.Logout(t.Context(), token) }},
		{"ended from the list", func(e *callEnv, _ string, id uint64) error { return e.svc.EndSession(t.Context(), e.as("alice"), id) }},
		{"password change with another session", func(e *callEnv, _ string, id uint64) error {
			e.loginAs("alice", app.SessionAPI, "phone")
			return e.svc.ChangePassword(t.Context(), e.as("alice"), id, app.PasswordChange{
				CurrentPassword: goodPassword, NewPassword: "another long password", ClientIP: loginIP,
			})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newCallEnv(t)
			login := e.loginAs("alice", app.SessionBrowser, "laptop")
			id := e.sessionIDOf(login.Token, app.SessionBrowser)
			call := e.inState("active")
			if err := tt.end(e, login.Token, id); err != nil {
				t.Fatalf("%+v", err)
			}
			got, err := e.calls.Get(t.Context(), e.as("bob"), call.ID)
			if err != nil || got.State != app.CallEnded || got.EndReason != app.CallEndSessionEnded {
				t.Fatalf("call = %+v, %v; want ended, session_ended", got, err)
			}
			if n := len(e.eventsOf(live.KindCallEnded)); n != 2 {
				t.Errorf("%d call.ended events, want 2 (%v)", n, e.kinds())
			}
			if n := len(e.eventsOf(live.KindUserChanged)); n == 0 {
				t.Errorf("no user.changed event (%v)", e.kinds())
			}
		})
	}

	t.Run("a password change with no other session keeps the call", func(t *testing.T) {
		t.Parallel()
		e := newCallEnv(t)
		login := e.loginAs("alice", app.SessionBrowser, "laptop")
		id := e.sessionIDOf(login.Token, app.SessionBrowser)
		call := e.inState("active")
		err := e.svc.ChangePassword(t.Context(), e.as("alice"), id, app.PasswordChange{
			CurrentPassword: goodPassword, NewPassword: "another long password", ClientIP: loginIP,
		})
		if err != nil {
			t.Fatalf("%+v", err)
		}
		got, err := e.calls.Get(t.Context(), e.as("bob"), call.ID)
		if err != nil || got.State != app.CallActive {
			t.Errorf("call = %+v, %v; want it active", got, err)
		}
	})

	t.Run("a logout of an unknown session", func(t *testing.T) {
		t.Parallel()
		e := newCallEnv(t)
		call := e.inState("active")
		if err := e.svc.Logout(t.Context(), "no-such-token"); err != nil {
			t.Fatalf("%+v", err)
		}
		got, _ := e.calls.Get(t.Context(), e.as("bob"), call.ID)
		if got.State != app.CallActive {
			t.Errorf("call = %+v, want it active", got)
		}
	})
}

func TestCallsEndOpenAndFail(t *testing.T) {
	t.Parallel()
	e := newCallEnv(t)
	a := e.start("alice", e.ab, true)
	b := e.start("carol", e.cd, true)
	e.answer("dave", b.ID)
	if open, err := e.calls.IsOpen(t.Context(), a.ID); err != nil || !open {
		t.Errorf("IsOpen = %v, %v", open, err)
	}
	if open, err := e.calls.IsOpen(t.Context(), 9999); err != nil || open {
		t.Errorf("IsOpen of a missing call = %v, %v", open, err)
	}
	e.rec.Reset()
	// Another process's calls are left alone.
	if n, err := e.calls.EndOpen(t.Context(), "ffffffffffffffff"); err != nil || n != 0 {
		t.Errorf("EndOpen(other) = %d, %v", n, err)
	}
	if err := e.calls.Fail(t.Context(), b.ID); err != nil {
		t.Fatalf("Fail: %+v", err)
	}
	got, _ := e.calls.Get(t.Context(), e.as("carol"), b.ID)
	if got.State != app.CallEnded || got.EndReason != app.CallEndFailed || got.EndedBy != 0 {
		t.Errorf("failed call = %+v", got)
	}
	// Failing a call that ended is not an error and publishes nothing more.
	e.rec.Reset()
	if err := e.calls.Fail(t.Context(), b.ID); err != nil {
		t.Errorf("Fail again: %+v", err)
	}
	if kinds := e.kinds(); len(kinds) != 0 {
		t.Errorf("the second Fail published %v", kinds)
	}
	if n, err := e.calls.EndOpen(t.Context(), callTestProcess); err != nil || n != 1 {
		t.Errorf("EndOpen(home) = %d, %v; want 1", n, err)
	}
	got, _ = e.calls.Get(t.Context(), e.as("alice"), a.ID)
	if got.EndReason != app.CallEndServerStopped {
		t.Errorf("call = %+v, want server_stopped", got)
	}
	if err := e.calls.Fail(t.Context(), 9999); err == nil {
		t.Error("Fail of a missing call succeeded")
	} else {
		_ = requireTag(t, err, app.TagNotFound, "Calls.endSystem.func1->Store.CallByID.NotFound")
	}
	c := e.start("alice", e.ab, false)
	if n, err := e.calls.EndOpen(t.Context(), ""); err != nil || n != 1 {
		t.Errorf("EndOpen(all) = %d, %v; want 1", n, err)
	}
	if open, _ := e.calls.IsOpen(t.Context(), c.ID); open {
		t.Error("the call is still open")
	}
}

func TestCallsSetMedia(t *testing.T) {
	t.Parallel()
	e := newCallEnv(t)
	ringing := e.start("alice", e.ab, true)
	err := e.calls.SetMedia(t.Context(), e.as("alice"), ringing.ID, true, true)
	_ = requireTag(t, err, app.TagCallWrongState, "Calls.SetMedia.CallWrongState")
	e.answer("bob", ringing.ID)
	e.rec.Reset()
	if err := e.calls.SetMedia(t.Context(), e.as("bob"), ringing.ID, false, true); err != nil {
		t.Fatalf("%+v", err)
	}
	got := e.eventsOf(live.KindCallUpdated)
	if len(got) != 2 {
		t.Fatalf("events = %+v", got)
	}
	for _, ev := range got {
		if ev.UserID != e.uid("bob") || ev.Microphone || !ev.Camera || ev.CallID != ringing.ID || ev.ConversationID != e.ab {
			t.Errorf("event = %+v, want bob's state (microphone off, camera on)", ev)
		}
	}
	topics := []string{got[0].Topic, got[1].Topic}
	slices.Sort(topics)
	want := []string{live.UserTopic(e.uid("alice")), live.UserTopic(e.uid("bob"))}
	slices.Sort(want)
	if !slices.Equal(topics, want) {
		t.Errorf("topics = %v, want %v", topics, want)
	}
}

func TestCallsMaxCallsCountsOnlyOpenCallsOfTheProcess(t *testing.T) {
	t.Parallel()
	e := newCallEnvWith(t, app.CallsConfig{ProcessID: callTestProcess, Enabled: true, MaxCalls: 2})
	ab := e.start("alice", e.ab, false)
	e.start("carol", e.cd, false)
	ef := e.direct("erin", "admin")
	_, err := e.calls.Start(t.Context(), e.as("erin"), ef, false)
	_ = requireTag(t, err, app.TagCallServerFull, "Calls.Start.func1.CallServerFull")
	// A call that ended frees its place.
	mustCall(t)(e.calls.Cancel(t.Context(), e.as("alice"), ab.ID))
	if _, err := e.calls.Start(t.Context(), e.as("erin"), ef, false); err != nil {
		t.Errorf("Start after a call ended: %+v", err)
	}
	// A call of another process does not count.
	other := app.NewCalls(e.st, e.clk, app.CallsConfig{ProcessID: "ffffffffffffffff", Enabled: true, MaxCalls: 1}, e.rec)
	gh := e.direct("alice", "carol")
	_ = gh
	if _, err := other.Start(t.Context(), e.as("alice"), e.ab, false); err != nil {
		t.Errorf("Start on another process: %+v", err)
	}
}

// TestConcurrentStartsRingOnce starts calls to the same user from several
// callers at the same time: exactly one rings, and the others are refused as
// busy and leave a missed call.
func TestConcurrentStartsRingOnce(t *testing.T) {
	t.Parallel()
	e := newCallEnv(t)
	callers := []string{"alice", "carol", "dave", "erin"}
	convs := map[string]uint64{"alice": e.ab}
	for _, name := range callers[1:] {
		convs[name] = e.direct(name, "bob")
	}
	var wg sync.WaitGroup
	errs := make([]error, len(callers))
	for i, name := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = e.calls.Start(t.Context(), e.as(name), convs[name], true)
		}()
	}
	wg.Wait()
	rang, busy := 0, 0
	for _, err := range errs {
		if err == nil {
			rang++
			continue
		}
		_ = requireTag(t, err, app.TagCallCalleeBusy, "Calls.Start.func1.CallCalleeBusy")
		busy++
	}
	if rang != 1 || busy != len(callers)-1 {
		t.Fatalf("%d calls rang and %d were busy; want 1 and %d (%v)", rang, busy, len(callers)-1, errs)
	}
	open, err := e.st.OpenCallsOfUsers(t.Context(), e.uid("bob"))
	if err != nil || len(open) != 1 {
		t.Errorf("open calls of bob = %+v, %v; want exactly one", open, err)
	}
	if n := len(e.eventsOf(live.KindCallRinging)); n != 1 {
		t.Errorf("%d call.ringing events, want 1", n)
	}
}

// TestACallerWhoIsAlsoCalledStartsOnce checks the case that no index sees: a
// user who is the callee of one call and starts another at the same time.
func TestACallerWhoIsAlsoCalledStartsOnce(t *testing.T) {
	t.Parallel()
	e := newCallEnv(t)
	bc := e.direct("bob", "carol")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	// alice calls bob while bob calls carol.
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = e.calls.Start(t.Context(), e.as("alice"), e.ab, true)
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = e.calls.Start(t.Context(), e.as("bob"), bc, true)
	}()
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		}
	}
	open, err := e.st.OpenCallsOfUsers(t.Context(), e.uid("bob"))
	if err != nil || len(open) != 1 || ok != 1 {
		t.Errorf("%d starts worked, open calls of bob = %+v, %v; want exactly one of each (%v)", ok, open, err, errs)
	}
}

func TestCallsAreNotInTheSearchOfMessages(t *testing.T) {
	t.Parallel()
	e := newCallEnv(t)
	text := e.send("alice", e.ab, "see you in the standup")
	call := e.start("alice", e.ab, true)
	mustCall(t)(e.calls.Cancel(t.Context(), e.as("alice"), call.ID))
	if n := len(e.entries("bob", e.ab)); n != 1 {
		t.Fatalf("%d entries, want 1", n)
	}
	// The index holds the text message only.
	rows, err := e.st.MessageIndexRows(t.Context())
	if err != nil {
		t.Fatalf("MessageIndexRows: %+v", err)
	}
	if len(rows) != 1 || rows[text.ID] == "" {
		t.Errorf("the index holds %v, want only message %d", rows, text.ID)
	}
}

func TestCallsOpenAndJoin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// join says which method is called: Join, or Open.
		join bool
		// off turns calls off after setup.
		off   bool
		setup func(e *callEnv) uint64
		user  string
		conv  func(e *callEnv) uint64
		// tag and route are the failure; an empty tag means the call is returned.
		tag, route string
	}{
		{name: "Open: the caller of a ringing call", setup: func(e *callEnv) uint64 { return e.start("alice", e.ab, true).ID }, user: "alice"},
		{name: "Join: the caller of a ringing call", join: true, setup: func(e *callEnv) uint64 { return e.start("alice", e.ab, true).ID }, user: "alice"},
		{name: "Open: the callee of a ringing call", setup: func(e *callEnv) uint64 { return e.start("alice", e.ab, true).ID }, user: "bob"},
		{
			name: "Join: the callee of an answered call", join: true, user: "bob",
			setup: func(e *callEnv) uint64 {
				c := e.start("alice", e.ab, true)
				e.answer("bob", c.ID)
				return c.ID
			},
		},
		{
			name: "Join: the callee of a ringing call", join: true, setup: func(e *callEnv) uint64 { return e.start("alice", e.ab, true).ID }, user: "bob",
			tag: app.TagCallWrongState, route: "Calls.Join.CallWrongState",
		},
		{
			name: "Open: a conversation with no call", setup: func(e *callEnv) uint64 { return 0 }, user: "alice",
			tag: app.TagCallNone, route: "Calls.Open.CallNone",
		},
		{
			name: "Join: a conversation with no call", join: true, setup: func(e *callEnv) uint64 { return 0 }, user: "alice",
			tag: app.TagCallNone, route: "Calls.Join->Calls.Open.CallNone",
		},
		{
			name: "Join: calls are off", join: true, off: true,
			setup: func(e *callEnv) uint64 { return e.start("alice", e.ab, true).ID }, user: "alice",
			tag: app.TagCallsOff, route: "Calls.Join.CallsOff",
		},
		{
			name: "Join: a stranger", join: true, setup: func(e *callEnv) uint64 { e.start("alice", e.ab, true); return 0 }, user: "carol",
			tag: app.TagNotFound, route: "Calls.Join->Calls.Open->Calls.Current",
		},
		{
			name: "Join: a group", join: true, setup: func(e *callEnv) uint64 { return 0 }, user: "alice", conv: func(e *callEnv) uint64 { return e.trio },
			tag: app.TagCallGroup, route: "Calls.Join->Calls.Open->Calls.Current.CallGroup",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newCallEnv(t)
			id := tt.setup(e)
			if tt.off {
				// The call was made with calls on; the process is then
				// started again with them off.
				e.calls = app.NewCalls(e.st, e.clk, app.CallsConfig{ProcessID: callTestProcess}, e.rec)
			}
			conv := e.ab
			if tt.conv != nil {
				conv = tt.conv(e)
			}
			var got app.Call
			var err error
			if tt.join {
				got, err = e.calls.Join(t.Context(), e.as(tt.user), conv)
			} else {
				got, err = e.calls.Open(t.Context(), e.as(tt.user), conv)
			}
			if tt.tag == "" {
				if err != nil || (id != 0 && got.ID != id) {
					t.Errorf("got %+v, %v; want call %d", got, err, id)
				}
				return
			}
			_ = requireTag(t, err, tt.tag, tt.route)
		})
	}
}

func TestCallsStatusAndLeave(t *testing.T) {
	t.Parallel()
	e := newCallEnv(t)
	ringing := e.start("alice", e.ab, true)
	if st, err := e.calls.Status(t.Context(), ringing.ID); err != nil || st != (app.CallStatus{Open: true}) {
		t.Errorf("Status of a ringing call = %+v, %v", st, err)
	}
	if st, err := e.calls.Status(t.Context(), 9999); err != nil || st != (app.CallStatus{}) {
		t.Errorf("Status of a missing call = %+v, %v", st, err)
	}
	// The caller leaves a ringing call: it is cancelled.
	if err := e.calls.Leave(t.Context(), e.as("alice"), ringing.ID); err != nil {
		t.Fatalf("Leave: %+v", err)
	}
	if st, _ := e.calls.Status(t.Context(), ringing.ID); st.Open || st.Reason != app.CallEndCancelled {
		t.Errorf("Status after the caller left = %+v", st)
	}
	// Either side leaves an active call: it is a hang-up.
	second := e.start("alice", e.ab, true)
	e.answer("bob", second.ID)
	if st, _ := e.calls.Status(t.Context(), second.ID); !st.Open || !st.Active {
		t.Errorf("Status of an active call = %+v", st)
	}
	if err := e.calls.Leave(t.Context(), e.as("carol"), second.ID); err == nil {
		t.Error("a stranger left a call")
	} else {
		_ = requireTag(t, err, app.TagNotFound, "Calls.Leave->Calls.participantCall.NotFound")
	}
	if err := e.calls.Leave(t.Context(), e.as("bob"), second.ID); err != nil {
		t.Fatalf("Leave: %+v", err)
	}
	if st, _ := e.calls.Status(t.Context(), second.ID); st.Open || st.Reason != app.CallEndHangup {
		t.Errorf("Status after bob left = %+v", st)
	}
	// Nothing is left to leave.
	err := e.calls.Leave(t.Context(), e.as("bob"), second.ID)
	_ = requireTag(t, err, app.TagCallWrongState, "Calls.Leave->Calls.HangUp")
}
