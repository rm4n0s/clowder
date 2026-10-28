package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
)

// callsOff makes the pages of the environment think that this server has no
// media server.
func (e *roomsEnv) callsOff() {
	e.srv.svc.Calls = app.NewCalls(e.st, e.clk, app.CallsConfig{ProcessID: "0123456789abcdef"}, e.hub)
}

// startCall has the user call the other member of the direct conversation.
func (e *roomsEnv) startCall(conv uint64, from string, video bool) app.Call {
	e.t.Helper()
	call, err := e.calls.Start(e.t.Context(), e.actor(e.users(from)), conv, video)
	if err != nil {
		e.t.Fatalf("Start: %+v", err)
	}
	return call
}

// callPath is the path of a call route of the conversation.
func callPath(conv uint64, action string) string {
	if action == "" {
		return convPath(conv) + "/call"
	}
	return convPath(conv) + "/call/" + action
}

// videoForm is the form of the buttons Video call and Answer with video.
func videoForm(video bool) url.Values {
	if video {
		return url.Values{"video": {"true"}}
	}
	return url.Values{"video": {"false"}}
}

func TestTheCallButtonsAreInADirectConversationOnly(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	direct := e.direct("alice", "bob")
	group := e.group("trio", "alice", "bob", "carol")
	buttons := []string{
		`id="call-buttons"`, `data-call-start="video"`, `data-call-start="audio"`, `action="` + callPath(direct, "") + `"`,
		`name="video" value="true"`, `name="video" value="false"`, "Video call", "Audio call", `id="call-area"`,
		`/static/call.js`,
	}
	none := []string{`data-call-start`, `id="call-buttons"`, `id="call-area"`, `/static/call.js`}

	t.Run("a direct conversation has both", func(t *testing.T) {
		rec := e.send(get(convPath(direct), e.cookies["alice"]))
		requireBody(t, rec, buttons)
		// The buttons are in the row of actions, at the right of the header.
		body := rec.Body.String()
		if i, j := strings.Index(body, `class="conversation-actions"`), strings.Index(body, `data-call-start="video"`); i < 0 || j < i {
			t.Errorf("the buttons are not in the row of actions")
		}
	})
	t.Run("a group has none", func(t *testing.T) {
		requireBody(t, e.send(get(convPath(group), e.cookies["alice"])), nil, none...)
	})
	t.Run("a server with calls off has none", func(t *testing.T) {
		off := newRoomsEnv(t)
		off.callsOff()
		conv := off.direct("alice", "bob")
		requireBody(t, off.send(get(convPath(conv), off.cookies["alice"])), nil, none...)
	})
	t.Run("the buttons give way to the state of the call", func(t *testing.T) {
		e := newRoomsEnv(t)
		conv := e.direct("alice", "bob")
		call := e.startCall(conv, "alice", true)
		cid := idText(call.ID)
		// The caller sees "Calling Bob…" with Cancel.
		alice := e.send(get(convPath(conv), e.cookies["alice"]))
		requireBody(t, alice, []string{
			`id="call-panel"`, `data-call="` + cid + `"`, `data-state="ringing"`, `data-role="caller"`, "Calling Bob…", "Video call",
			`data-call-cancel`, `action="` + callPath(conv, "cancel") + `"`, `data-socket="/calls/` + idText(conv) + `/ws"`,
		}, `data-call-start`, `call-controls`)
		// The callee sees no panel (the dialog is in the page) and no buttons.
		bob := e.send(get(convPath(conv), e.cookies["bob"]))
		requireBody(t, bob, []string{`id="call-area"`, `id="call-dialog"`}, `data-call-start`, `id="call-panel"`, "Calling")

		// Once answered both see the call view, with its controls.
		if _, err := e.calls.Answer(t.Context(), e.actor(e.bob), call.ID, true); err != nil {
			t.Fatal(err)
		}
		controls := []string{
			`data-call-control="mic"`, `data-call-control="camera"`, `data-call-control="screen"`, `data-call-control="minimize"`,
			`data-call-device="audioinput"`, `data-call-device="videoinput"`, `data-call-hangup`, `id="call-remote"`, `id="call-local"`,
			`class="call-duration"`, `class="call-mark call-mark-mic"`, `class="call-mark call-mark-cam"`, `data-state="active"`,
			`action="` + callPath(conv, "hangup") + `"`,
		}
		for _, who := range []string{"alice", "bob"} {
			rec := e.send(get(convPath(conv), e.cookies[who]))
			requireBody(t, rec, controls, `data-call-start`, "Calling Bob", `id="call-dialog"`)
		}
		// Only the page the user answered in takes part: it carries the choice.
		plain := e.send(get(convPath(conv), e.cookies["bob"])).Body.String()
		if !strings.Contains(plain, `data-answer=""`) {
			t.Errorf("a page that is not the one of the answer has data-answer: %s", plain)
		}
		for way, wantVideo := range map[string]string{"video": "true", "audio": "false"} {
			rec := e.send(get(convPath(conv)+"?call="+way, e.cookies["bob"]))
			requireBody(t, rec, []string{`data-answer="` + way + `"`, `data-role="callee"`, `data-video="` + wantVideo + `"`})
		}
		// The caller is never sent there by an answer: the query means nothing to them.
		requireBody(t, e.send(get(convPath(conv)+"?call=video", e.cookies["alice"])), []string{`data-answer=""`})
		// A call that ended leaves the buttons again.
		if _, err := e.calls.HangUp(t.Context(), e.actor(e.alice), call.ID); err != nil {
			t.Fatal(err)
		}
		requireBody(t, e.send(get(convPath(conv), e.cookies["alice"])), buttons[:4], `id="call-panel"`)
	})
}

func TestTheCallRoutes(t *testing.T) {
	t.Parallel()
	type result struct {
		status int
		// body is what a plain post and a Datastar request show.
		plain, datastar []string
		// notWant is what neither may show.
		notWant []string
		// location is where a redirect goes.
		location string
	}
	tests := []struct {
		name   string
		route  string
		form   url.Values
		user   string
		prep   func(e *roomsEnv, conv uint64) // before the request
		inConv func(e *roomsEnv) uint64       // the conversation of the request, default the direct one
		want   result
	}{
		// Start.
		{name: "alice calls bob", route: "", form: videoForm(true), user: "alice",
			want: result{status: http.StatusSeeOther, location: "/messages/{conv}", datastar: []string{`id="call-area"`, "Calling Bob…", `id="call-buttons"`}}},
		{name: "alice calls bob with audio only", route: "", form: videoForm(false), user: "alice",
			want: result{status: http.StatusSeeOther, location: "/messages/{conv}", datastar: []string{"Audio call", "Calling Bob…"}}},
		{name: "bob calls alice", route: "", form: videoForm(true), user: "bob",
			want: result{status: http.StatusSeeOther, location: "/messages/{conv}", datastar: []string{"Calling Alice…"}}},
		{name: "a stranger", route: "", form: videoForm(true), user: "carol",
			want: result{status: http.StatusNotFound}},
		{name: "an admin who is no member", route: "", form: videoForm(true), user: "admin",
			want: result{status: http.StatusNotFound}},
		{name: "a group", route: "", form: videoForm(true), user: "alice",
			inConv: func(e *roomsEnv) uint64 { return e.group("trio", "alice", "bob", "carol") },
			want:   result{status: http.StatusConflict, plain: []string{"Calls are for conversations of two people."}, datastar: []string{"Calls are for conversations of two people."}}},
		{name: "bob is in another call", route: "", form: videoForm(true), user: "alice",
			prep: func(e *roomsEnv, conv uint64) { e.startCall(e.direct("bob", "carol"), "bob", false) },
			want: result{status: http.StatusConflict, plain: []string{"Bob is in another call."}, datastar: []string{"Bob is in another call."}}},
		{name: "alice is in another call", route: "", form: videoForm(true), user: "alice",
			prep: func(e *roomsEnv, conv uint64) { e.startCall(e.direct("alice", "carol"), "alice", false) },
			want: result{status: http.StatusConflict, plain: []string{"You are in another call."}, datastar: []string{"You are in another call."}}},
		{name: "the callee is disabled", route: "", form: videoForm(true), user: "alice",
			prep: func(e *roomsEnv, conv uint64) { e.disable("bob") },
			want: result{status: http.StatusConflict, plain: []string{"Bob is disabled."}, datastar: []string{"Bob is disabled."}}},
		// Answer.
		{name: "bob answers with video", route: "answer", form: videoForm(true), user: "bob",
			prep: func(e *roomsEnv, conv uint64) { e.startCall(conv, "alice", true) },
			want: result{status: http.StatusSeeOther, location: "/messages/{conv}?call=video"}},
		{name: "bob answers with audio", route: "answer", form: videoForm(false), user: "bob",
			prep: func(e *roomsEnv, conv uint64) { e.startCall(conv, "alice", true) },
			want: result{status: http.StatusSeeOther, location: "/messages/{conv}?call=audio"}},
		{name: "the caller answers", route: "answer", form: videoForm(true), user: "alice",
			prep: func(e *roomsEnv, conv uint64) { e.startCall(conv, "alice", true) },
			want: result{status: http.StatusForbidden}},
		{name: "answering a call that is active", route: "answer", form: videoForm(true), user: "bob",
			prep: func(e *roomsEnv, conv uint64) { e.callBetween(conv, "alice", "bob", true) },
			want: result{status: http.StatusConflict, plain: []string{"This call is not ringing."}, datastar: []string{"This call is not ringing."}}},
		{name: "answering with no call", route: "answer", form: videoForm(true), user: "bob",
			want: result{status: http.StatusConflict, plain: []string{"There is no call in this conversation."}, datastar: []string{"There is no call in this conversation."}}},
		{name: "a stranger answers", route: "answer", form: videoForm(true), user: "carol",
			prep: func(e *roomsEnv, conv uint64) { e.startCall(conv, "alice", true) },
			want: result{status: http.StatusNotFound}},
		// Decline.
		{name: "bob declines", route: "decline", user: "bob",
			prep: func(e *roomsEnv, conv uint64) { e.startCall(conv, "alice", true) },
			want: result{status: http.StatusSeeOther, location: "/messages/{conv}", datastar: []string{`id="call-ringing"`}, notWant: []string{"call-dialog"}}},
		{name: "the caller declines", route: "decline", user: "alice",
			prep: func(e *roomsEnv, conv uint64) { e.startCall(conv, "alice", true) },
			want: result{status: http.StatusForbidden}},
		{name: "a stranger declines", route: "decline", user: "carol",
			prep: func(e *roomsEnv, conv uint64) { e.startCall(conv, "alice", true) },
			want: result{status: http.StatusNotFound}},
		// Cancel.
		{name: "alice cancels", route: "cancel", user: "alice",
			prep: func(e *roomsEnv, conv uint64) { e.startCall(conv, "alice", true) },
			want: result{status: http.StatusSeeOther, location: "/messages/{conv}", datastar: []string{`id="call-buttons"`, "data-call-start"}, notWant: []string{"Calling"}}},
		{name: "the callee cancels", route: "cancel", user: "bob",
			prep: func(e *roomsEnv, conv uint64) { e.startCall(conv, "alice", true) },
			want: result{status: http.StatusForbidden}},
		{name: "cancelling a call that is active", route: "cancel", user: "alice",
			prep: func(e *roomsEnv, conv uint64) { e.callBetween(conv, "alice", "bob", true) },
			want: result{status: http.StatusConflict, plain: []string{"This call is not ringing."}, datastar: []string{"This call is not ringing."}}},
		{name: "a stranger cancels", route: "cancel", user: "admin",
			prep: func(e *roomsEnv, conv uint64) { e.startCall(conv, "alice", true) },
			want: result{status: http.StatusNotFound}},
		// Hang up.
		{name: "alice hangs up", route: "hangup", user: "alice",
			prep: func(e *roomsEnv, conv uint64) { e.callBetween(conv, "alice", "bob", true) },
			want: result{status: http.StatusSeeOther, location: "/messages/{conv}", datastar: []string{`id="call-buttons"`, "data-call-start"}, notWant: []string{"call-controls"}}},
		{name: "bob hangs up", route: "hangup", user: "bob",
			prep: func(e *roomsEnv, conv uint64) { e.callBetween(conv, "alice", "bob", true) },
			want: result{status: http.StatusSeeOther, location: "/messages/{conv}", datastar: []string{"data-call-start"}}},
		{name: "hanging up a call that rings", route: "hangup", user: "alice",
			prep: func(e *roomsEnv, conv uint64) { e.startCall(conv, "alice", true) },
			want: result{status: http.StatusConflict, plain: []string{"This call is not active."}, datastar: []string{"This call is not active."}}},
		{name: "a stranger hangs up", route: "hangup", user: "carol",
			prep: func(e *roomsEnv, conv uint64) { e.callBetween(conv, "alice", "bob", true) },
			want: result{status: http.StatusNotFound}},
	}
	for _, tt := range tests {
		for _, datastar := range []bool{false, true} {
			name := tt.name + " (plain)"
			if datastar {
				name = tt.name + " (datastar)"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				e := newRoomsEnv(t)
				conv := e.direct("alice", "bob")
				if tt.inConv != nil {
					conv = tt.inConv(e)
				}
				if tt.prep != nil {
					tt.prep(e, conv)
				}
				opts := []reqOpt{e.cookies[tt.user]}
				if datastar {
					opts = append(opts, asDatastar())
				}
				rec := e.send(postForm(callPath(conv, tt.route), tt.form, opts...))
				want := tt.want
				location := strings.ReplaceAll(want.location, "{conv}", idText(conv))
				switch {
				case want.status == http.StatusNotFound && datastar:
					// A Datastar request for a conversation that is no one's is sent to its page, which says so.
					requireSSE(t, rec)
					requireBody(t, rec, []string{redirectScript(convPath(conv))}, "Forbidden")
				case want.status == http.StatusSeeOther && datastar && tt.route == "answer":
					// The answer takes the page to the conversation, which opens the call.
					requireSSE(t, rec)
					requireBody(t, rec, []string{redirectScript(location)})
				case want.status == http.StatusSeeOther && datastar:
					requireSSE(t, rec)
					requireBody(t, rec, want.datastar, want.notWant...)
				case datastar:
					// A refusal is shown in the header of the conversation, not as an error status.
					requireSSE(t, rec)
					requireBody(t, rec, append([]string{`id="conversation-header"`}, want.datastar...), "Page not found")
				default:
					if rec.Code != want.status {
						t.Fatalf("status = %d, want %d; body:\n%s", rec.Code, want.status, rec.Body)
					}
					if want.status == http.StatusSeeOther {
						if got := rec.Header().Get("Location"); got != location {
							t.Errorf("Location = %q, want %q", got, location)
						}
					} else {
						requireBody(t, rec, want.plain)
					}
				}
			})
		}
	}
}

func TestTheCallRoutesNeedALogin(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	conv := e.direct("alice", "bob")
	for _, action := range []string{"", "answer", "decline", "cancel", "hangup"} {
		rec := e.send(postForm(callPath(conv, action), videoForm(true)))
		if rec.Code != http.StatusSeeOther && rec.Code != http.StatusFound {
			t.Errorf("%q: status = %d, want a redirect to the login", action, rec.Code)
		}
		if !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
			t.Errorf("%q: Location = %q", action, rec.Header().Get("Location"))
		}
	}
}

func TestACallOnAServerWithCallsOff(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	e.callsOff()
	conv := e.direct("alice", "bob")
	for _, action := range []string{"", "answer", "decline", "cancel", "hangup"} {
		// Starting says why; the others find no call.
		message := "There is no call in this conversation."
		if action == "" {
			message = "Calls are turned off on this server."
		}
		rec := e.send(postForm(callPath(conv, action), videoForm(true), e.cookies["alice"]))
		if rec.Code != http.StatusConflict {
			t.Errorf("%q: status = %d, want 409", action, rec.Code)
		}
		requireBody(t, rec, []string{message})
		rec = e.send(postForm(callPath(conv, action), videoForm(true), e.cookies["alice"], asDatastar()))
		requireSSE(t, rec)
		requireBody(t, rec, []string{message, `id="conversation-header"`})
	}
	// A stranger still gets "not found".
	rec := e.send(postForm(callPath(conv, ""), videoForm(true), e.cookies["carol"]))
	if rec.Code != http.StatusNotFound {
		t.Errorf("a stranger: status = %d, want 404", rec.Code)
	}
}

// dialogPages are pages of a logged-in user that carry the shell, and so the
// ringing dialog.
func dialogPages(e *roomsEnv, conv uint64) []string {
	return []string{
		"/", "/rooms", "/rooms/" + idText(e.general.ID), "/rooms/" + idText(e.general.ID) + "/board", "/search", "/notifications",
		"/messages", convPath(conv), "/me/settings",
	}
}

func TestTheRingingDialogIsInEveryPageWhileACallRings(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	conv := e.direct("alice", "bob")
	pages := dialogPages(e, conv)
	dialog := []string{
		`id="call-ringing"`, `<dialog id="call-dialog"`, `data-preserve-attr="open"`, "Alice is calling you", "Video call",
		`data-call-answer="video"`, "Answer with video", `data-call-answer="audio"`, "Answer with audio", "Decline",
		`action="` + callPath(conv, "answer") + `"`, `action="` + callPath(conv, "decline") + `"`,
		`data-ring="1"`, `data-notice="Alice is calling you"`,
	}
	// Nothing rings: every page has the empty slot.
	for _, page := range pages {
		rec := e.send(get(page, e.cookies["bob"]))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", page, rec.Code)
		}
		requireBody(t, rec, []string{`id="call-ringing"`}, "<dialog id=\"call-dialog\"")
	}

	call := e.startCall(conv, "alice", true)
	for _, page := range pages {
		rec := e.send(get(page, e.cookies["bob"]))
		requireBody(t, rec, dialog)
		// The caller's own pages have no dialog.
		requireBody(t, e.send(get(page, e.cookies["alice"])), []string{`id="call-ringing"`}, "<dialog id=\"call-dialog\"")
	}
	// Nor have the pages of someone who is not called.
	requireBody(t, e.send(get("/", e.cookies["carol"])), []string{`id="call-ringing"`}, "<dialog id=\"call-dialog\"")
	// An audio call says so.
	if _, err := e.calls.Cancel(t.Context(), e.actor(e.alice), call.ID); err != nil {
		t.Fatal(err)
	}
	e.startCall(conv, "alice", false)
	requireBody(t, e.send(get("/", e.cookies["bob"])), []string{"Audio call", `data-call-answer="audio"`})

	// Answered, the dialog is gone from every page.
	rec := e.send(get("/", e.cookies["bob"]))
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	current, ok, err := e.calls.Current(t.Context(), e.actor(e.bob), conv)
	if err != nil || !ok {
		t.Fatalf("Current = %v, %v", ok, err)
	}
	if _, err := e.calls.Answer(t.Context(), e.actor(e.bob), current.ID, true); err != nil {
		t.Fatal(err)
	}
	for _, page := range pages {
		requireBody(t, e.send(get(page, e.cookies["bob"])), []string{`id="call-ringing"`}, "<dialog id=\"call-dialog\"")
	}
}

// TestTheDialogRingsByThePreferences has the dialog show in every case, and the
// ringtone (data-ring) only when the conversation is not muted and does not say
// Nothing, whatever the sound setting says, and the browser notification
// (data-browser) when the user turned them on and the call rings.
func TestTheDialogRingsByThePreferences(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(e *roomsEnv, conv uint64)
		ring  bool
		// browser is whether the browser notification is wanted.
		browser bool
	}{
		{"defaults", func(e *roomsEnv, conv uint64) {}, true, false},
		{"sound off", func(e *roomsEnv, conv uint64) {
			if _, err := e.accounts.UpdateSettings(e.t.Context(), e.actor(e.bob), app.SettingsUpdate{Sound: new(false)}); err != nil {
				e.t.Fatal(err)
			}
		}, true, false},
		{"browser notifications on", func(e *roomsEnv, conv uint64) {
			if _, err := e.accounts.UpdateSettings(e.t.Context(), e.actor(e.bob), app.SettingsUpdate{BrowserNotifications: new(true)}); err != nil {
				e.t.Fatal(err)
			}
		}, true, true},
		{"muted", func(e *roomsEnv, conv uint64) {
			if _, err := e.prefs.UpdatePlace(e.t.Context(), e.actor(e.bob), app.ConversationRef(conv), app.PlaceUpdate{Muted: new(true)}); err != nil {
				e.t.Fatal(err)
			}
		}, false, false},
		{"nothing", func(e *roomsEnv, conv uint64) {
			if _, err := e.prefs.UpdatePlace(e.t.Context(), e.actor(e.bob), app.ConversationRef(conv), app.PlaceUpdate{Notify: new(app.PlaceNotifyNothing)}); err != nil {
				e.t.Fatal(err)
			}
		}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newRoomsEnv(t)
			conv := e.direct("alice", "bob")
			tt.setup(e, conv)
			e.startCall(conv, "alice", true)
			body := e.send(get("/", e.cookies["bob"])).Body.String()
			for _, want := range []string{`<dialog id="call-dialog"`, "Answer with video", "Decline"} {
				if !strings.Contains(body, want) {
					t.Errorf("the dialog lacks %q", want)
				}
			}
			if got := strings.Contains(body, `data-ring="1"`); got != tt.ring {
				t.Errorf("data-ring = %v, want %v", got, tt.ring)
			}
			if got := strings.Contains(body, `data-browser="1"`); got != tt.browser {
				t.Errorf("data-browser = %v, want %v", got, tt.browser)
			}
		})
	}
}

// requirePatchWith finds the one patch of the stream that has the text.
func requirePatchWith(t *testing.T, ps []patch, text string) patch {
	t.Helper()
	got := patchesWith(ps, text)
	if len(got) == 0 {
		t.Fatalf("no patch has %q; patches: %+v", text, ps)
	}
	return got[len(got)-1]
}

func TestTheUserStreamPutsTheDialogInAndTakesItOut(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/me/stream", "/rooms/{general}/stream"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			e := newRoomsEnv(t)
			conv := e.direct("alice", "bob")
			path = strings.ReplaceAll(path, "{general}", idText(e.general.ID))
			c := e.serveStreams().open("bob", path, asDatastar())
			if c.status != http.StatusOK {
				t.Fatalf("status = %d", c.status)
			}
			c.drainQuiet()

			// A call that starts to ring puts the dialog in its slot.
			call := e.startCall(conv, "alice", true)
			p := requirePatchWith(t, c.drain(), `id="call-ringing"`)
			requireContains(t, p.html, []string{`<dialog id="call-dialog"`, "Alice is calling you", `data-ring="1"`, `data-call="` + idText(call.ID) + `"`}, nil)

			// Declined, cancelled or answered, the dialog goes.
			if _, err := e.calls.Cancel(t.Context(), e.actor(e.alice), call.ID); err != nil {
				t.Fatal(err)
			}
			p = requirePatchWith(t, c.drain(), `id="call-ringing"`)
			if strings.Contains(p.html, "dialog") {
				t.Errorf("the slot still has the dialog after the call ended: %s", p.html)
			}

			call = e.startCall(conv, "alice", false)
			requirePatchWith(t, c.drain(), `<dialog id="call-dialog"`)
			if _, err := e.calls.Answer(t.Context(), e.actor(e.bob), call.ID, true); err != nil {
				t.Fatal(err)
			}
			p = requirePatchWith(t, c.drain(), `id="call-ringing"`)
			if strings.Contains(p.html, "dialog") {
				t.Errorf("the slot still has the dialog after the call was answered: %s", p.html)
			}
			if _, err := e.calls.HangUp(t.Context(), e.actor(e.alice), call.ID); err != nil {
				t.Fatal(err)
			}
			// The end of the call changes nothing else of this page.
			for _, p := range c.drain() {
				if strings.Contains(p.html, "dialog") {
					t.Errorf("the slot has a dialog after hang up: %s", p.html)
				}
			}
		})
	}
}

func TestTheStreamRingsOnlyByThePreferences(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	conv := e.direct("alice", "bob")
	if _, err := e.prefs.UpdatePlace(t.Context(), e.actor(e.bob), app.ConversationRef(conv), app.PlaceUpdate{Muted: new(true)}); err != nil {
		t.Fatal(err)
	}
	c := e.serveStreams().open("bob", "/me/stream", asDatastar())
	c.drainQuiet()
	e.startCall(conv, "alice", true)
	p := requirePatchWith(t, c.drain(), `<dialog id="call-dialog"`)
	if strings.Contains(p.html, "data-ring") || strings.Contains(p.html, "data-browser") {
		t.Errorf("a call of a muted conversation rings: %s", p.html)
	}
}

func TestTheStreamOfANewPageShowsACallThatRingsAndAReconnectionSyncs(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	conv := e.direct("alice", "bob")
	ss := e.serveStreams()

	// A first connection sends nothing while nothing rings, and the slot while a
	// call does (the page was made a moment ago; a call may have started since).
	c := ss.open("bob", "/me/stream", asDatastar())
	for _, p := range c.drainQuiet() {
		if strings.Contains(p.html, `id="call-ringing"`) {
			t.Errorf("a stream that opens with nothing ringing patched the slot: %s", p.html)
		}
	}
	call := e.startCall(conv, "alice", true)
	requirePatchWith(t, c.drain(), `<dialog id="call-dialog"`)
	c2 := ss.open("bob", "/me/stream", asDatastar())
	requirePatchWith(t, c2.drain(), `<dialog id="call-dialog"`)

	// A reconnection of a user who has no call ringing empties the slot: a call
	// may have ended while the page had no stream.
	if _, err := e.calls.Cancel(t.Context(), e.actor(e.alice), call.ID); err != nil {
		t.Fatal(err)
	}
	c3 := ss.open("bob", "/me/stream", asDatastar(), withHeader("Last-Event-ID", "1"))
	p := requirePatchWith(t, c3.drain(), `id="call-ringing"`)
	if strings.Contains(p.html, "dialog") {
		t.Errorf("the slot has a dialog although nothing rings: %s", p.html)
	}
}

func TestTheConversationStreamPatchesTheCall(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	conv := e.direct("alice", "bob")
	other := e.direct("alice", "carol")
	ss := e.serveStreams()
	alice := ss.open("alice", streamOfConversation(conv, ""), asDatastar())
	bob := ss.open("bob", streamOfConversation(conv, ""), asDatastar())
	alice.drainQuiet()
	bob.drainQuiet()

	call := e.startCall(conv, "alice", true)
	// Alice's page shows "Calling Bob…" in place of the buttons.
	ps := alice.drain()
	requireContains(t, requirePatchWith(t, ps, `id="call-area"`).html, []string{"Calling Bob…", `data-state="ringing"`}, nil)
	if p := requirePatchWith(t, ps, `id="call-buttons"`); strings.Contains(p.html, "data-call-start") {
		t.Errorf("the buttons stay on the page of the caller: %s", p.html)
	}
	// Bob's page gets the dialog, and no panel.
	ps = bob.drain()
	requirePatchWith(t, ps, `<dialog id="call-dialog"`)
	if p := requirePatchWith(t, ps, `id="call-area"`); strings.Contains(p.html, "call-panel") {
		t.Errorf("the callee has a panel while it rings: %s", p.html)
	}

	// Answered: both pages get the call view, and bob's dialog goes.
	if _, err := e.calls.Answer(t.Context(), e.actor(e.bob), call.ID, true); err != nil {
		t.Fatal(err)
	}
	for who, c := range map[string]*streamClient{"alice": alice, "bob": bob} {
		ps := c.drain()
		requireContains(t, requirePatchWith(t, ps, `id="call-area"`).html, []string{`data-state="active"`, "call-controls", `id="call-remote"`}, nil)
		if who == "bob" {
			if p := requirePatchWith(t, ps, `id="call-ringing"`); strings.Contains(p.html, "dialog") {
				t.Errorf("the dialog stays after the answer: %s", p.html)
			}
		}
	}

	// The microphone changing is for the socket, not for the page.
	if err := e.calls.SetMedia(t.Context(), e.actor(e.alice), call.ID, false, true); err != nil {
		t.Fatal(err)
	}
	if got := alice.drainQuiet(); len(got) != 0 {
		t.Errorf("call.updated patched the page: %+v", got)
	}

	// Ended: the call area empties and the buttons come back; the entry arrives
	// as a message.
	if _, err := e.calls.HangUp(t.Context(), e.actor(e.bob), call.ID); err != nil {
		t.Fatal(err)
	}
	ps = alice.drain()
	if p := requirePatchWith(t, ps, `id="call-area"`); strings.Contains(p.html, "call-panel") {
		t.Errorf("the panel stays after the call ended: %s", p.html)
	}
	requireContains(t, requirePatchWith(t, ps, `id="call-buttons"`).html, []string{"data-call-start"}, nil)
	requireContains(t, requirePatchWith(t, ps, "message-call-entry").html, []string{"Video call · 0 s"}, nil)

	// A call of another conversation leaves this page alone.
	e.startCall(other, "alice", false)
	for _, p := range alice.drainQuiet() {
		t.Errorf("a call of another conversation patched this page: %+v", p)
	}
}

func TestACallReconnectionBringsTheCallBack(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	conv := e.direct("alice", "bob")
	ss := e.serveStreams()
	call := e.callBetween(conv, "alice", "bob", true)
	c := ss.open("alice", streamOfConversation(conv, ""), asDatastar(), withHeader("Last-Event-ID", "1"))
	requireContains(t, requirePatchWith(t, c.drain(), `id="call-area"`).html, []string{`data-call="` + idText(call.ID) + `"`, "call-controls"}, nil)
}

func TestTheCallEntriesAreWrittenForEachSideAndOutcome(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		end   func(e *roomsEnv, conv uint64)
		alice string
		bob   string
		// missed is whether bob's entry is in the colour of a warning.
		missed bool
	}{
		{"answered and hung up", func(e *roomsEnv, conv uint64) {
			call := e.callBetween(conv, "alice", "bob", true)
			e.clk.Advance(12 * time.Minute)
			if _, err := e.calls.HangUp(e.t.Context(), e.actor(e.bob), call.ID); err != nil {
				e.t.Fatal(err)
			}
		}, "Video call · 12 min", "Video call · 12 min", false},
		{"declined", func(e *roomsEnv, conv uint64) {
			call := e.startCall(conv, "alice", true)
			if _, err := e.calls.Decline(e.t.Context(), e.actor(e.bob), call.ID); err != nil {
				e.t.Fatal(err)
			}
		}, "Declined video call", "Declined video call", false},
		{"cancelled", func(e *roomsEnv, conv uint64) {
			call := e.startCall(conv, "alice", true)
			if _, err := e.calls.Cancel(e.t.Context(), e.actor(e.alice), call.ID); err != nil {
				e.t.Fatal(err)
			}
		}, "Cancelled video call", "Missed video call", true},
		{"missed", func(e *roomsEnv, conv uint64) {
			e.startCall(conv, "alice", false)
			e.clk.Advance(46 * time.Second)
			if n, err := e.calls.Expire(e.t.Context()); err != nil || n != 1 {
				e.t.Fatalf("Expire = %d, %v", n, err)
			}
		}, "No answer", "Missed audio call", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newRoomsEnv(t)
			conv := e.direct("alice", "bob")
			tt.end(e, conv)
			for who, want := range map[string]string{"alice": tt.alice, "bob": tt.bob} {
				body := e.send(get(convPath(conv), e.cookies[who])).Body.String()
				at := strings.Index(body, `class="message-call-entry"`)
				if at < 0 {
					t.Fatalf("%s: no call entry:\n%s", who, body)
				}
				item := body[strings.LastIndex(body[:at], `<li id="msg-`):]
				item = item[:strings.Index(item, "</li>")]
				if !strings.Contains(item, ">"+want+"</span>") {
					t.Errorf("%s: the entry does not say %q:\n%s", who, want, item)
				}
				warned := strings.Contains(item, "call-entry-missed")
				if wantWarned := who == "bob" && tt.missed; warned != wantWarned {
					t.Errorf("%s: call-entry-missed = %v, want %v", who, warned, wantWarned)
				}
				if !strings.Contains(item, `class="icon"`) || strings.Contains(item, "message-reply-link") || strings.Contains(item, "reaction-bar") {
					t.Errorf("%s: the entry is not one quiet line with an icon:\n%s", who, item)
				}
			}
		})
	}
}

func TestACallEntryReachesTheConversationLive(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	conv := e.direct("alice", "bob")
	c := e.serveStreams().open("bob", streamOfConversation(conv, ""), asDatastar())
	c.drainQuiet()
	call := e.startCall(conv, "alice", true)
	if _, err := e.calls.Cancel(t.Context(), e.actor(e.alice), call.ID); err != nil {
		t.Fatal(err)
	}
	p := requirePatchWith(t, c.drain(), "message-call-entry")
	requireContains(t, p.html, []string{"Missed video call", "call-entry-missed", `data-call="` + idText(call.ID) + `"`}, nil)
}

func TestADisabledCalleeIsSentToTheLoginWhileACallRings(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	conv := e.direct("alice", "bob")
	ss := e.serveStreams()
	bob := ss.open("bob", "/me/stream", asDatastar())
	bobInConversation := ss.open("bob", streamOfConversation(conv, ""), asDatastar())
	e.startCall(conv, "alice", true)
	requirePatchWith(t, bob.drain(), `<dialog id="call-dialog"`)
	if err := e.accounts.SetDisabled(t.Context(), e.actor(e.admin), e.bob.ID, true); err != nil {
		t.Fatalf("SetDisabled: %+v", err)
	}
	for name, c := range map[string]*streamClient{"the page": bob, "the conversation": bobInConversation} {
		var last patch
		for _, p := range c.drainUntilEnd() {
			last = p
		}
		if !strings.Contains(last.html, redirectScript("/login")) {
			t.Errorf("%s: the last patch is not a redirect to the login: %+v", name, last)
		}
	}
}
