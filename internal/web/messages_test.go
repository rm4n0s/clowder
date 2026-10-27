package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
)

// direct starts the direct conversation of two users.
func (e *roomsEnv) direct(from, to string) uint64 {
	e.t.Helper()
	id, err := e.msgs.StartDirect(e.t.Context(), e.actor(e.users(from)), e.users(to).ID)
	if err != nil {
		e.t.Fatalf("StartDirect %s %s: %+v", from, to, err)
	}
	return id
}

// group starts a group of the first user and the others.
func (e *roomsEnv) group(name, from string, others ...string) uint64 {
	e.t.Helper()
	ids := make([]uint64, len(others))
	for i, o := range others {
		ids[i] = e.users(o).ID
	}
	id, err := e.msgs.StartGroup(e.t.Context(), e.actor(e.users(from)), ids, name)
	if err != nil {
		e.t.Fatalf("StartGroup: %+v", err)
	}
	return id
}

// say sends a text message through the service.
func (e *roomsEnv) say(user string, conv uint64, body string) app.Message {
	e.t.Helper()
	return e.sayIn(user, conv, app.MessageInput{Body: body})
}

// sayIn sends a message through the service.
func (e *roomsEnv) sayIn(user string, conv uint64, in app.MessageInput) app.Message {
	e.t.Helper()
	m, err := e.msgs.Send(e.t.Context(), e.actor(e.users(user)), conv, in)
	if err != nil {
		e.t.Fatalf("Send %q: %+v", in.Body, err)
	}
	return m
}

// history returns the newest messages of the conversation as the user sees
// them.
func (e *roomsEnv) history(user string, conv uint64) []app.Message {
	e.t.Helper()
	page, err := e.msgs.History(e.t.Context(), e.actor(e.users(user)), conv, 0, 100)
	if err != nil {
		e.t.Fatalf("History: %+v", err)
	}
	return page.Messages
}

// unreadConversations is the number of conversations of the user with unread
// messages.
func (e *roomsEnv) unreadConversations(user string) int {
	e.t.Helper()
	n, err := e.msgs.UnreadCount(e.t.Context(), e.actor(e.users(user)))
	if err != nil {
		e.t.Fatalf("UnreadCount: %+v", err)
	}
	return n
}

// convPath is the path of the page of a conversation.
func convPath(id uint64) string { return "/messages/" + idText(id) }

// msgPath is the path of an action on a message.
func msgPath(id uint64, action string) string { return views.MessageActionPath(id, action) }

// leave has the user leave the group.
func (e *roomsEnv) leave(user string, conv uint64) {
	e.t.Helper()
	if err := e.msgs.Leave(e.t.Context(), e.actor(e.users(user)), conv); err != nil {
		e.t.Fatalf("Leave: %+v", err)
	}
}

// disable disables the user.
func (e *roomsEnv) disable(user string) {
	e.t.Helper()
	if err := e.accounts.SetDisabled(e.t.Context(), e.actor(e.admin), e.users(user).ID, true); err != nil {
		e.t.Fatalf("SetDisabled: %+v", err)
	}
}

// requireRedirectTo fails unless the answer sends the browser to target: a 303
// for a plain post, the redirect script of an SSE answer with status 200 for a
// Datastar request.
func requireRedirectTo(t *testing.T, rec *httptest.ResponseRecorder, datastar bool, target string) {
	t.Helper()
	if datastar {
		requireSSE(t, rec)
		requireBody(t, rec, []string{redirectScript(target)})
		return
	}
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != target {
		t.Fatalf("answer = %d to %q, want 303 to %q; body:\n%s", rec.Code, rec.Header().Get("Location"), target, rec.Body)
	}
}

func TestMessagesLinkAndBadgeInTheSidebar(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	conv := e.direct("alice", "bob")
	e.say("alice", conv, "one")
	other := e.direct("alice", "carol")
	e.say("alice", other, "two")
	e.say("carol", other, "mine") // alice's own message: nothing unread for her
	roomPath := "/rooms/" + idText(e.general.ID)
	post := e.writePost("alice", "general", "root")
	tests := []struct {
		name   string
		user   string
		page   string
		want   int
		stream string
	}{
		{"home", "bob", "/", 1, views.MeStreamPath},
		{"rooms", "bob", "/rooms", 1, views.MeStreamPath},
		{"search", "bob", "/search", 1, views.MeStreamPath},
		{"notifications", "bob", "/notifications", 1, views.MeStreamPath},
		{"a room", "bob", roomPath, 1, roomPath + "/stream"},
		{"a board", "bob", roomPath + "/board", 1, roomPath + "/board/stream"},
		{"a post", "bob", "/posts/" + idText(post.ID), 1, "/posts/" + idText(post.ID) + "/stream"},
		// carol's reply is alice's to read; carol read hers by writing it.
		{"carol's answer is alice's to read", "alice", "/", 1, views.MeStreamPath},
		{"carol has answered", "carol", "/", 0, views.MeStreamPath},
		{"nothing at all", "admin", "/", 0, views.MeStreamPath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(get(tt.page, e.cookies[tt.user]))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			requireBody(t, rec, []string{`<a class="sidebar-messages" href="/messages"><span>Messages</span>` + messageBadge(tt.want) + `</a>`, `@get(&#39;` + tt.stream})
		})
	}
}

func TestMessagesPageListsTheConversations(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	direct := e.direct("alice", "bob")
	e.say("alice", direct, "Hello <Bob>")
	crew := e.group("Launch crew", "alice", "bob", "carol")
	e.say("bob", crew, "See you soon")
	empty := e.direct("alice", "carol")
	e.group("", "carol", "alice", "bob")

	t.Run("bob's list", func(t *testing.T) {
		rec := e.send(get("/messages", e.cookies["bob"]))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		requireBody(t, rec, []string{
			"<title>Messages · Clowder</title>", "<h1>Messages</h1>",
			`<div id="conversation-list" class="conversation-list">`,
			`<li id="conversation-` + idText(direct) + `" class="conversation unread" data-conversation="` + idText(direct) + `" data-members="alice bob">`,
			`<a class="conversation-link" href="/messages/` + idText(direct) + `">`,
			`<span class="conversation-title">Alice</span>`,
			`<span class="conversation-unread">Unread</span>`,
			`<span class="conversation-preview">Alice: Hello &lt;Bob&gt;</span>`,
			// A group with a name, written by bob: no unread mark.
			`<li id="conversation-` + idText(crew) + `" class="conversation" data-conversation="` + idText(crew) + `" data-members="alice bob carol">`,
			`<span class="conversation-title">Launch crew</span>`, `<span class="conversation-kind">Group of 3</span>`,
			`<span class="conversation-preview">You: See you soon</span>`,
			// A group with no name is titled by the others.
			`<span class="conversation-title">Carol, Alice</span>`, `<span class="conversation-preview">No messages yet</span>`,
			`<time class="conversation-time" datetime="2026-10-26T09:30:00Z"`,
			// The form that starts one, and the stream of the page.
			`<form id="new-conversation-form"`, `name="users"`, `name="name"`, `data-on:submit="@post(&#39;/messages&#39;, {contentType: &#39;form&#39;})"`,
			`@get(&#39;/me/stream?messages=1&#39;`,
		}, "Hello <Bob>", `href="/messages/`+idText(empty)+`"`)
		if got := strings.Count(rec.Body.String(), `class="conversation unread"`); got != 1 {
			t.Errorf("%d unread conversations shown, want 1", got)
		}
	})
	t.Run("opening the list reads nothing", func(t *testing.T) {
		if got := e.unreadConversations("bob"); got != 1 {
			t.Errorf("bob's unread conversations = %d, want 1", got)
		}
	})
	t.Run("only the member's own", func(t *testing.T) {
		rec := e.send(get("/messages", e.cookies["carol"]))
		requireBody(t, rec, []string{`href="/messages/` + idText(crew) + `"`, `href="/messages/` + idText(empty) + `"`},
			`href="/messages/`+idText(direct)+`"`)
	})
	t.Run("nothing yet", func(t *testing.T) {
		rec := e.send(get("/messages", e.cookies["admin"]))
		requireBody(t, rec, []string{`<p class="empty" id="conversations-empty">No conversations yet.</p>`}, `<li id="conversation-`, `class="conversation-items"`)
	})
	t.Run("a group the person left is gone from the list", func(t *testing.T) {
		e.leave("carol", crew)
		rec := e.send(get("/messages", e.cookies["carol"]))
		requireBody(t, rec, nil, `href="/messages/`+idText(crew)+`"`)
	})
}

func TestMessagesNeedALogin(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	conv := e.direct("alice", "bob")
	msg := e.say("alice", conv, "hi")
	type route struct {
		method, path string
	}
	routes := []route{
		{http.MethodGet, "/messages"},
		{http.MethodPost, "/messages"},
		{http.MethodGet, convPath(conv)},
		{http.MethodGet, convPath(conv) + "/older?before=5"},
		{http.MethodGet, convPath(conv) + "/stream"},
		{http.MethodPost, convPath(conv)},
		{http.MethodPost, convPath(conv) + "/members"},
		{http.MethodPost, convPath(conv) + "/rename"},
		{http.MethodPost, convPath(conv) + "/leave"},
		{http.MethodGet, msgPath(msg.ID, "view")},
		{http.MethodGet, msgPath(msg.ID, "edit")},
		{http.MethodPost, msgPath(msg.ID, "edit")},
		{http.MethodPost, msgPath(msg.ID, "delete")},
		{http.MethodPost, msgPath(msg.ID, "react")},
		{http.MethodGet, msgPath(msg.ID, "reply")},
		{http.MethodGet, "/messages/files/1/a.txt"},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			// A plain request goes to the login page.
			req := httptest.NewRequest(rt.method, rt.path, nil)
			rec := e.send(req)
			if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
				t.Errorf("plain answer = %d to %q, want a redirect to the login page", rec.Code, rec.Header().Get("Location"))
			}
			// A Datastar request is told to go there.
			req = httptest.NewRequest(rt.method, rt.path, nil)
			asDatastar()(req)
			rec = e.send(req)
			requireSSE(t, rec)
			requireBody(t, rec, []string{`window.location.href = &#34;/login`})
		})
	}
}

func TestMessagesRefuseWhoIsNoMember(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	crew := e.group("crew", "alice", "bob", "carol")
	e.leave("carol", crew)
	msg := e.sayIn("alice", crew, app.MessageInput{Body: "secret words"})
	// The same for a person who is no member of a direct conversation.
	direct := e.direct("alice", "bob")
	dmsg := e.say("alice", direct, "private words")
	attach := e.sayIn("alice", crew, app.MessageInput{Uploads: []app.Upload{e.uploadMessage("alice", "f.txt")}})
	file := "/messages/files/" + idText(attach.Attachments[0].ID) + "/f.txt"

	type route struct {
		name         string
		method, path string
		form         url.Values
		// target is where a Datastar request is sent: the conversation, or the
		// list of conversations for the routes about a message.
		target string
	}
	conv := func(conv uint64, rest string) string { return convPath(conv) + rest }
	var routes []route
	for _, c := range []struct {
		id  uint64
		msg app.Message
	}{{crew, msg}, {direct, dmsg}} {
		routes = append(routes,
			route{"page", "GET", conv(c.id, ""), nil, convPath(c.id)},
			route{"older", "GET", conv(c.id, "/older?before=99"), nil, convPath(c.id)},
			route{"stream", "GET", conv(c.id, "/stream"), nil, convPath(c.id)},
			route{"send", "POST", conv(c.id, ""), url.Values{"body": {"hello"}}, convPath(c.id)},
			route{"add members", "POST", conv(c.id, "/members"), url.Values{"users": {"carol"}}, convPath(c.id)},
			route{"rename", "POST", conv(c.id, "/rename"), url.Values{"name": {"x"}}, convPath(c.id)},
			route{"leave", "POST", conv(c.id, "/leave"), nil, convPath(c.id)},
			route{"view", "GET", msgPath(c.msg.ID, "view"), nil, "/messages"},
			route{"edit form", "GET", msgPath(c.msg.ID, "edit"), nil, "/messages"},
			route{"edit", "POST", msgPath(c.msg.ID, "edit"), url.Values{"body": {"hijacked"}}, "/messages"},
			route{"delete", "POST", msgPath(c.msg.ID, "delete"), nil, "/messages"},
			route{"react", "POST", msgPath(c.msg.ID, "react"), url.Values{"emoji": {"👍"}}, "/messages"},
			route{"reply", "GET", msgPath(c.msg.ID, "reply"), nil, "/messages"},
		)
	}
	routes = append(routes, route{"file", "GET", file, nil, ""})
	// carol left the group and is no member of the direct conversation; the
	// admin is in neither.
	for _, rt := range routes {
		for _, who := range []string{"carol", "admin"} {
			t.Run(rt.name+" as "+who+" "+rt.method+" "+rt.path, func(t *testing.T) {
				for _, datastar := range []bool{false, true} {
					opts := []reqOpt{e.cookies[who]}
					if datastar {
						opts = append(opts, asDatastar())
					}
					var req *http.Request
					if rt.method == "GET" {
						req = get(rt.path, opts...)
					} else {
						req = postForm(rt.path, rt.form, opts...)
					}
					rec := e.send(req)
					switch {
					case datastar && rt.target != "":
						requireRedirectTo(t, rec, true, rt.target)
					case rt.name == "older" && !datastar:
						// A plain request is sent to the page, which is the 404.
						requireRedirectTo(t, rec, false, rt.path[:strings.Index(rt.path, "/older")]+"?before=99")
						page := e.send(get(rt.path[:strings.Index(rt.path, "/older")]+"?before=99", opts...))
						if page.Code != http.StatusNotFound {
							t.Fatalf("the page answers %d, want 404", page.Code)
						}
					default:
						if rec.Code != http.StatusNotFound {
							t.Fatalf("status = %d, want 404 (not 403); body:\n%s", rec.Code, rec.Body)
						}
					}
					requireBody(t, rec, nil, "secret words", "private words", "hijacked", "not allowed", "Forbidden")
				}
			})
		}
	}
	// Nothing changed.
	if got := e.history("alice", crew); len(got) != 2 || got[0].Body != "secret words" || got[0].EditedAt != nil || got[0].Deleted {
		t.Errorf("the group changed: %+v", got)
	}
	if got := e.history("alice", direct); len(got) != 1 || got[0].Body != "private words" || got[0].Deleted || len(got[0].Reactions) != 0 {
		t.Errorf("the direct conversation changed: %+v", got)
	}
}

// uploadMessage uploads a small file through the messages service for a message
// to take.
func (e *roomsEnv) uploadMessage(user, name string) app.Upload {
	e.t.Helper()
	up, err := e.msgs.Upload(e.t.Context(), e.actor(e.users(user)), name, strings.NewReader("contents of "+name))
	if err != nil {
		e.t.Fatalf("Upload: %+v", err)
	}
	return up
}

func TestMessagesBadIDsAreNotFound(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	for _, path := range []string{
		"/messages/abc", "/messages/0", "/messages/9999", "/messages/abc/stream", "/messages/abc/older",
		"/messages/m/x/view", "/messages/m/0/edit", "/messages/m/9999/edit", "/messages/m/9999/view", "/messages/m/9999/reply",
		"/messages/files/x/a.txt", "/messages/files/0/a.txt", "/messages/files/9999/a.txt",
	} {
		t.Run(path, func(t *testing.T) {
			rec := e.send(get(path, e.cookies["alice"]))
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", rec.Code)
			}
		})
	}
	for _, path := range []string{
		"/messages/abc/members", "/messages/0/rename", "/messages/9999/leave", "/messages/abc",
		"/messages/m/x/edit", "/messages/m/0/delete", "/messages/m/9999/react", "/messages/m/9999/edit",
	} {
		t.Run("POST "+path, func(t *testing.T) {
			rec := e.send(postForm(path, url.Values{"body": {"x"}, "users": {"bob"}, "name": {"x"}, "emoji": {"👍"}}, e.cookies["alice"]))
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", rec.Code)
			}
		})
	}
}

func TestStartConversation(t *testing.T) {
	t.Parallel()
	t.Run("one user opens the direct conversation, again and again", func(t *testing.T) {
		modes(t, func(t *testing.T, datastar bool) {
			e := newRoomsEnv(t)
			direct := e.direct("alice", "bob")
			rec := e.post("alice", "/messages", url.Values{"users": {"bob"}}, datastar)
			requireRedirectTo(t, rec, datastar, convPath(direct))
			// The other way round it is the same conversation.
			rec = e.post("bob", "/messages", url.Values{"users": {"alice"}}, datastar)
			requireRedirectTo(t, rec, datastar, convPath(direct))
			// And a new pair gets its own.
			rec = e.post("alice", "/messages", url.Values{"users": {"carol"}}, datastar)
			if datastar {
				requireSSE(t, rec)
			}
			if datastar && strings.Contains(rec.Body.String(), convPath(direct)) || !datastar && rec.Header().Get("Location") == convPath(direct) {
				t.Errorf("a new pair got the conversation of another: %s", rec.Header().Get("Location"))
			}
			if got, err := e.msgs.Conversations(t.Context(), e.actor(e.alice)); err != nil || len(got) != 2 {
				t.Errorf("alice has %d conversations (%v), want 2", len(got), err)
			}
		})
	})

	t.Run("how the usernames are written", func(t *testing.T) {
		tests := []struct {
			name  string
			users string
			want  []string
		}{
			{"commas", "bob,carol", []string{"bob", "carol"}},
			{"spaces", "bob carol", []string{"bob", "carol"}},
			{"commas and spaces, tabs and newlines", " bob ,\tcarol\n", []string{"bob", "carol"}},
			{"at signs and capitals", "@Bob @carol", []string{"bob", "carol"}},
			{"repeats", "bob carol bob", []string{"bob", "carol"}},
			{"the person's own name among others", "alice bob carol", []string{"bob", "carol"}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				e := newRoomsEnv(t)
				rec := e.post("alice", "/messages", url.Values{"users": {tt.users}, "name": {"  The crew  "}}, false)
				if rec.Code != http.StatusSeeOther {
					t.Fatalf("status = %d, want 303; body:\n%s", rec.Code, rec.Body)
				}
				list, err := e.msgs.Conversations(t.Context(), e.actor(e.alice))
				if err != nil || len(list) != 1 {
					t.Fatalf("Conversations = %+v, %v", list, err)
				}
				c := list[0]
				var names []string
				for _, m := range c.Members {
					if m.Username != "alice" {
						names = append(names, m.Username)
					}
				}
				if c.Kind != app.ConversationGroup || c.Name != "The crew" || strings.Join(names, " ") != strings.Join(tt.want, " ") {
					t.Errorf("conversation = %+v, want a group of %v called The crew", c, tt.want)
				}
				if got := rec.Header().Get("Location"); got != convPath(c.ID) {
					t.Errorf("Location = %q, want %q", got, convPath(c.ID))
				}
			})
		}
	})

	t.Run("several users make a group, in both modes", func(t *testing.T) {
		modes(t, func(t *testing.T, datastar bool) {
			e := newRoomsEnv(t)
			rec := e.post("alice", "/messages", url.Values{"users": {"bob carol"}, "name": {"Crew"}}, datastar)
			list, err := e.msgs.Conversations(t.Context(), e.actor(e.carol))
			if err != nil || len(list) != 1 || list[0].Name != "Crew" || len(list[0].Members) != 3 {
				t.Fatalf("Conversations = %+v, %v", list, err)
			}
			requireRedirectTo(t, rec, datastar, convPath(list[0].ID))
		})
	})

	t.Run("one user and a name is a direct conversation", func(t *testing.T) {
		e := newRoomsEnv(t)
		e.post("alice", "/messages", url.Values{"users": {"bob"}, "name": {"ignored"}}, false)
		list, _ := e.msgs.Conversations(t.Context(), e.actor(e.alice))
		if len(list) != 1 || list[0].Kind != app.ConversationDirect || list[0].Name != "" {
			t.Errorf("Conversations = %+v", list)
		}
	})

	t.Run("the other person is told live", func(t *testing.T) {
		e := newRoomsEnv(t)
		c := e.serveStreams().open("bob", "/me/stream?messages=1", asDatastar())
		got := c.patches(3)
		requirePatch(t, got[2], "", "outer", []string{`id="conversation-list"`, `id="conversations-empty"`}, nil)
		e.post("alice", "/messages", url.Values{"users": {"bob"}}, true)
		// conversation.changed: the count, and the list.
		var list bool
		for _, p := range c.drain() {
			if strings.Contains(p.html, `id="conversation-list"`) {
				list = strings.Contains(p.html, `data-members="alice bob"`)
			}
		}
		if !list {
			t.Error("bob's list does not show the new conversation")
		}
	})

	problems := []struct {
		name  string
		form  url.Values
		field string
		text  string
		// keeps is what the form shows again.
		keeps []string
		setup func(e *roomsEnv)
	}{
		{"nobody", url.Values{"users": {"  "}, "name": {"x"}}, "users", "Name at least one person.", []string{`value="  "`, `value="x"`}, nil},
		{"an unknown username", url.Values{"users": {"bob nobody"}}, "users", "No active user named nobody.", []string{`value="bob nobody"`}, nil},
		{"two unknown usernames", url.Values{"users": {"ghost, nobody"}}, "users", "No active user named ghost, nobody.", nil, nil},
		{"a disabled user", url.Values{"users": {"carol"}}, "users", "No active user named carol.", nil, func(e *roomsEnv) { e.disable("carol") }},
		{"only yourself", url.Values{"users": {"alice"}}, "users", "Name someone other than yourself.", []string{`value="alice"`}, nil},
		{"yourself twice", url.Values{"users": {"@alice, Alice"}}, "users", "Name someone other than yourself.", nil, nil},
		{"a name that is too long", url.Values{"users": {"bob carol"}, "name": {strings.Repeat("n", 81)}}, "name", "Use at most 80 characters.", []string{`value="bob carol"`}, nil},
	}
	for _, tt := range problems {
		t.Run(tt.name, func(t *testing.T) {
			modes(t, func(t *testing.T, datastar bool) {
				e := newRoomsEnv(t)
				if tt.setup != nil {
					tt.setup(e)
				}
				rec := e.post("alice", "/messages", tt.form, datastar)
				want := append([]string{`id="new-conversation-form"`, tt.text, `aria-invalid="true"`}, tt.keeps...)
				if datastar {
					requireSSE(t, rec)
					requireBody(t, rec, want, "window.location")
				} else {
					if rec.Code != http.StatusUnprocessableEntity {
						t.Fatalf("status = %d, want 422; body:\n%s", rec.Code, rec.Body)
					}
					requireBody(t, rec, append(want, "<h1>Messages</h1>", `id="conversation-list"`))
				}
				if list, _ := e.msgs.Conversations(t.Context(), e.actor(e.alice)); len(list) != 0 {
					t.Errorf("a conversation was made: %+v", list)
				}
			})
		})
	}
}

func TestRoomMemberListHasMessageButtons(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	roomPath := "/rooms/" + idText(e.general.ID)
	rec := e.send(get(roomPath, e.cookies["alice"]))
	body := rec.Body.String()
	button := func(user string) []string {
		return []string{
			`<form class="action message-form" method="post" action="/messages" data-on:submit="@post(&#39;/messages&#39;, {contentType: &#39;form&#39;})">`,
			`<input type="hidden" name="users" value="` + user + `">`,
			`<input type="hidden" name="from" value="room-` + idText(e.general.ID) + `">`,
			`title="Send ` + strings.ToUpper(user[:1]) + user[1:] + ` a message">Message</button>`,
		}
	}
	requireContains(t, body, button("bob"), nil)
	requireContains(t, body, button("admin"), nil)
	// Not for the person who looks.
	requireContains(t, body, nil, []string{`name="users" value="alice"`, `Send Alice a message`})
	if got := strings.Count(body, ">Message</button>"); got != 2 {
		t.Errorf("%d Message buttons, want 2 (admin and bob)", got)
	}

	t.Run("it starts the direct conversation", func(t *testing.T) {
		modes(t, func(t *testing.T, datastar bool) {
			rec := e.post("alice", "/messages", url.Values{"users": {"bob"}, "from": {"room-" + idText(e.general.ID)}}, datastar)
			requireRedirectTo(t, rec, datastar, convPath(e.direct("alice", "bob")))
		})
	})
	t.Run("a failure is shown in the member list", func(t *testing.T) {
		e.disable("bob")
		form := url.Values{"users": {"bob"}, "from": {"room-" + idText(e.general.ID)}}
		rec := e.post("alice", "/messages", form, true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{`id="members-panel"`, "No active user named bob."}, "window.location")
		rec = e.post("alice", "/messages", form, false)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", rec.Code)
		}
		requireBody(t, rec, []string{`id="members-panel"`, "No active user named bob.", "<h1>general</h1>"})
	})
}
