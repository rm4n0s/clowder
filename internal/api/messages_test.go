package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
)

const thumbsUp = "%F0%9F%91%8D"

// convs starts a conversation of the user with the others through the API
// (by username) and returns its ID.
func (f *forum) convs(user string, others ...string) uint64 {
	f.t.Helper()
	got := f.ok(user, http.MethodPost, "/api/v1/conversations", map[string]any{"usernames": others}, http.StatusCreated)
	return idOf(f.t, got)
}

// say sends a message through the API and returns the message.
func (f *forum) msg(user string, conv uint64, body string) map[string]any {
	f.t.Helper()
	return f.ok(user, http.MethodPost, path("/conversations/%d/messages", conv), map[string]any{"body": body}, http.StatusCreated)
}

// history reads the page of messages of the conversation as the user.
func (f *forum) history(user string, conv uint64, query string) map[string]any {
	f.t.Helper()
	return f.ok(user, http.MethodGet, path("/conversations/%d/messages", conv)+query, nil, http.StatusOK)
}

// messageIDs reads the IDs of the messages of a page.
func messageIDs(t *testing.T, page map[string]any) []uint64 {
	t.Helper()
	ids := []uint64{}
	for _, m := range list(t, page["messages"]) {
		ids = append(ids, idOf(t, m))
	}
	return ids
}

func TestStartConversation(t *testing.T) {
	t.Parallel()
	t.Run("a direct conversation is made once", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		bob := f.userID("bob")
		first := f.call("alice", http.MethodPost, "/api/v1/conversations", map[string]any{"user_ids": []uint64{bob}})
		requireStatus(t, first, http.StatusCreated)
		conv := decode(t, first)
		id := idOf(t, conv)
		if got, want := first.Header().Get("Location"), path("/conversations/%d", id); got != want {
			t.Errorf("Location = %q, want %q", got, want)
		}
		if conv["kind"] != "direct" || conv["name"] != nil || conv["display_name"] != "Bob" || conv["can_send"] != true ||
			conv["unread"] != false || conv["last_message"] != nil || conv["seen_up_to_id"] != nil || len(list(t, conv["members"])) != 2 {
			t.Errorf("conversation = %v", conv)
		}
		for _, tt := range []struct {
			user string
			body map[string]any
		}{
			{"alice", map[string]any{"user_ids": []uint64{bob}}},
			{"alice", map[string]any{"usernames": []string{"BOB"}}},
			{"bob", map[string]any{"usernames": []string{"alice"}}},
			{"bob", map[string]any{"usernames": []string{"alice", "bob"}}}, // one's own name is not counted
			{"alice", map[string]any{"user_ids": []uint64{bob, f.userID("alice"), bob}}},
		} {
			rec := f.call(tt.user, http.MethodPost, "/api/v1/conversations", tt.body)
			requireStatus(t, rec, http.StatusOK)
			if idOf(t, decode(t, rec)) != id {
				t.Errorf("%s %v made another conversation", tt.user, tt.body)
			}
		}
		list, err := f.msgs.Conversations(t.Context(), actorOf(f.testEnv, "alice"))
		if err != nil || len(list) != 1 {
			t.Errorf("conversations = %v, %v; want one", list, err)
		}
	})
	t.Run("an admin starts one like anyone", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		requireStatus(t, f.call("admin", http.MethodPost, "/api/v1/conversations", map[string]any{"usernames": []string{"bob"}}), http.StatusCreated)
	})
	t.Run("a group", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		rec := f.call("alice", http.MethodPost, "/api/v1/conversations", map[string]any{"usernames": []string{"alice", "bob", "carol"}, "name": "  crew "})
		requireStatus(t, rec, http.StatusCreated)
		conv := decode(t, rec)
		if conv["kind"] != "group" || conv["name"] != "crew" || conv["display_name"] != "crew" || len(list(t, conv["members"])) != 3 {
			t.Errorf("conversation = %v", conv)
		}
		// Another group of the same people is another conversation, 201 again.
		again := f.call("alice", http.MethodPost, "/api/v1/conversations", map[string]any{"user_ids": []uint64{f.userID("bob"), f.userID("carol")}})
		requireStatus(t, again, http.StatusCreated)
		if idOf(t, decode(t, again)) == idOf(t, conv) {
			t.Error("a group was made twice")
		}
		if conv["name"] == nil || decode(t, again)["name"] != nil {
			t.Error("a group with no name has one")
		}
	})
	mistakes := []struct {
		name  string
		body  func(f *forum) any
		field string
		setup func(f *forum)
	}{
		{"both lists", func(f *forum) any {
			return map[string]any{"user_ids": []uint64{f.userID("bob")}, "usernames": []string{"bob"}}
		}, "user_ids", nil},
		{"neither list", func(*forum) any { return map[string]any{"name": "x"} }, "user_ids", nil},
		{"empty lists", func(*forum) any { return map[string]any{"user_ids": []uint64{}, "usernames": []string{}} }, "user_ids", nil},
		{"only oneself by ID", func(f *forum) any { return map[string]any{"user_ids": []uint64{f.userID("alice")}} }, "user_ids", nil},
		{"only oneself by username", func(*forum) any { return map[string]any{"usernames": []string{"alice", " ALICE "}} }, "usernames", nil},
		{"an unknown ID", func(*forum) any { return map[string]any{"user_ids": []uint64{9999}} }, "user_ids", nil},
		{"an unknown username", func(*forum) any { return map[string]any{"usernames": []string{"nobody"}} }, "usernames", nil},
		{"blank usernames", func(*forum) any { return map[string]any{"usernames": []string{"  "}} }, "usernames", nil},
		{"an unknown ID in a group", func(f *forum) any {
			return map[string]any{"user_ids": []uint64{9999, f.userID("carol")}}
		}, "user_ids", nil},
		{"a disabled user by ID", func(f *forum) any { return map[string]any{"user_ids": []uint64{f.userID("bob")}} }, "user_ids",
			func(f *forum) { disable(f, "bob") }},
		{"a disabled user by username", func(*forum) any { return map[string]any{"usernames": []string{"bob"}} }, "usernames",
			func(f *forum) { disable(f, "bob") }},
		{"a disabled user in a group", func(*forum) any { return map[string]any{"usernames": []string{"bob", "carol"}} }, "usernames",
			func(f *forum) { disable(f, "bob") }},
		{"a name for a direct conversation", func(*forum) any {
			return map[string]any{"usernames": []string{"bob"}, "name": "pals"}
		}, "name", nil},
		{"a name that is too long", func(*forum) any {
			return map[string]any{"usernames": []string{"bob", "carol"}, "name": strings.Repeat("é", 81)}
		}, "name", nil},
		{"ids that are not numbers", func(*forum) any { return `{"user_ids": ["2"]}` }, "user_ids.0", nil},
		{"a field that is not known", func(*forum) any { return `{"users": [2]}` }, "users", nil},
	}
	for _, tt := range mistakes {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			if tt.setup != nil {
				tt.setup(f)
			}
			before := len(f.conversationList("alice"))
			rec := f.call("alice", http.MethodPost, "/api/v1/conversations", tt.body(f))
			requireStatus(t, rec, http.StatusUnprocessableEntity)
			if code := errorCode(t, rec); code != "invalid_input" {
				t.Errorf("code = %q", code)
			}
			if errorFields(t, rec)[tt.field] == nil {
				t.Errorf("fields = %v, want %q", errorFields(t, rec), tt.field)
			}
			if after := len(f.conversationList("alice")); after != before {
				t.Errorf("a refused call made a conversation")
			}
		})
	}
	t.Run("too many people", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		var ids []uint64
		for i := 0; i < 50; i++ {
			ids = append(ids, f.addUser(fmt.Sprintf("user%02d", i), store.RoleMember).ID)
		}
		rec := f.call("alice", http.MethodPost, "/api/v1/conversations", map[string]any{"user_ids": ids})
		requireStatus(t, rec, http.StatusUnprocessableEntity)
		if errorFields(t, rec)["user_ids"] == nil {
			t.Errorf("fields = %v", errorFields(t, rec))
		}
		requireStatus(t, f.call("alice", http.MethodPost, "/api/v1/conversations", map[string]any{"user_ids": ids[:49]}), http.StatusCreated)
	})
}

// disable disables the user with the service, as an admin who is not the user.
func disable(f *forum, username string) {
	f.t.Helper()
	by := "admin"
	if username == "admin" {
		by = "root"
	}
	if err := f.accounts.SetDisabled(f.t.Context(), actorOf(f.testEnv, by), f.userID(username), true); err != nil {
		f.t.Fatalf("SetDisabled %s: %+v", username, err)
	}
}

// conversationList reads the conversations of the user through the API.
func (f *forum) conversationList(user string) []any {
	f.t.Helper()
	return list(f.t, f.ok(user, http.MethodGet, "/api/v1/conversations", nil, http.StatusOK)["conversations"])
}

func TestConversationList(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	if got := f.conversationList("alice"); len(got) != 0 {
		t.Fatalf("a new user has conversations: %v", got)
	}
	quiet := f.convs("alice", "carol")
	f.clk.Advance(time.Minute)
	talk := f.convs("alice", "bob")
	f.clk.Advance(time.Minute)
	m1 := f.msg("bob", talk, "hello <alice>")
	f.clk.Advance(time.Minute)
	group := f.convs("carol", "alice", "bob")
	if err := f.msgs.Rename(t.Context(), actorOf(f.testEnv, "carol"), group, "crew"); err != nil {
		t.Fatal(err)
	}

	got := f.conversationList("alice")
	var order []uint64
	for _, c := range got {
		order = append(order, idOf(t, c))
	}
	if want := []uint64{group, talk, quiet}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v (the newest activity first)", order, want)
	}
	byID := map[uint64]map[string]any{}
	for _, c := range got {
		byID[idOf(t, c)] = obj(t, c)
	}
	d := byID[talk]
	last := obj(t, d["last_message"])
	if d["unread"] != true || d["display_name"] != "Bob" || last["id"] != m1["id"] || last["preview"] != "hello <alice>" || last["deleted"] != false {
		t.Errorf("the direct conversation = %v", d)
	}
	if obj(t, last["author"])["username"] != "bob" {
		t.Errorf("last author = %v", last["author"])
	}
	if g := byID[group]; g["kind"] != "group" || g["name"] != "crew" || g["unread"] != false || g["last_message"] != nil {
		t.Errorf("the group = %v", g)
	}
	// Bob wrote it, so for him it is read; for alice it is unread until she reads.
	if bob := f.conversationList("bob"); len(bob) != 2 {
		t.Errorf("bob has %d conversations, want 2", len(bob))
	}
	if me := f.ok("alice", http.MethodGet, "/api/v1/me", nil, http.StatusOK); me["unread_conversations"] != float64(1) {
		t.Errorf("unread_conversations = %v", me["unread_conversations"])
	}
	f.ok("alice", http.MethodPost, path("/conversations/%d/read", talk), map[string]any{"up_to_id": m1["id"]}, http.StatusNoContent)
	if me := f.ok("alice", http.MethodGet, "/api/v1/me", nil, http.StatusOK); me["unread_conversations"] != float64(0) {
		t.Errorf("unread_conversations after reading = %v", me["unread_conversations"])
	}
	if d := obj(t, f.conversationList("alice")[1]); d["unread"] != false {
		t.Errorf("still unread: %v", d)
	}
	// Admins see only their own conversations.
	if got := f.conversationList("admin"); len(got) != 0 {
		t.Errorf("admin sees %v", got)
	}
}

func TestSeenAndCanSend(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	conv := f.convs("alice", "bob")
	m1 := f.msg("alice", conv, "one")
	m2 := f.msg("alice", conv, "two")
	read := func(user string) map[string]any {
		return f.ok(user, http.MethodGet, path("/conversations/%d", conv), nil, http.StatusOK)
	}
	if got := read("alice")["seen_up_to_id"]; got != nil {
		t.Errorf("seen_up_to_id = %v before bob read anything", got)
	}
	f.ok("bob", http.MethodPost, path("/conversations/%d/read", conv), map[string]any{"up_to_id": m1["id"]}, http.StatusNoContent)
	if got := read("alice")["seen_up_to_id"]; got != m1["id"] {
		t.Errorf("seen_up_to_id = %v, want %v", got, m1["id"])
	}
	// What alice has read is what bob sees: she wrote both.
	if got := read("bob")["seen_up_to_id"]; got != m2["id"] {
		t.Errorf("bob's seen_up_to_id = %v, want %v", got, m2["id"])
	}
	// The list says the same.
	if got := obj(t, f.conversationList("alice")[0])["seen_up_to_id"]; got != m1["id"] {
		t.Errorf("seen_up_to_id in the list = %v", got)
	}
	// Reading never moves back.
	f.ok("bob", http.MethodPost, path("/conversations/%d/read", conv), map[string]any{"up_to_id": m2["id"]}, http.StatusNoContent)
	f.ok("bob", http.MethodPost, path("/conversations/%d/read", conv), map[string]any{"up_to_id": m1["id"]}, http.StatusNoContent)
	if got := read("alice")["seen_up_to_id"]; got != m2["id"] {
		t.Errorf("seen_up_to_id after reading an older one = %v, want %v", got, m2["id"])
	}
	// A group has no "seen".
	group := f.convs("alice", "bob", "carol")
	gm := f.msg("alice", group, "hi")
	f.ok("bob", http.MethodPost, path("/conversations/%d/read", group), map[string]any{"up_to_id": gm["id"]}, http.StatusNoContent)
	if got := f.ok("alice", http.MethodGet, path("/conversations/%d", group), nil, http.StatusOK); got["seen_up_to_id"] != nil || got["can_send"] != true {
		t.Errorf("group = %v", got)
	}

	// can_send is false with the other user disabled; the messages are read but not sent.
	disable(f, "bob")
	if got := read("alice"); got["can_send"] != false {
		t.Errorf("can_send = %v with bob disabled", got["can_send"])
	}
	for _, c := range f.conversationList("alice") {
		if idOf(t, c) == conv && obj(t, c)["can_send"] != false {
			t.Errorf("can_send in the list = %v", obj(t, c)["can_send"])
		}
	}
	rec := f.call("alice", http.MethodPost, path("/conversations/%d/messages", conv), map[string]any{"body": "still there?"})
	requireStatus(t, rec, http.StatusUnprocessableEntity)
	if errorFields(t, rec)["body"] == nil {
		t.Errorf("fields = %v", errorFields(t, rec))
	}
	if got := f.history("alice", conv, ""); len(list(t, got["messages"])) != 2 {
		t.Errorf("history = %v", got)
	}
	// Opening a conversation again with a disabled user is refused.
	requireStatus(t, f.call("alice", http.MethodPost, "/api/v1/conversations", map[string]any{"usernames": []string{"bob"}}), http.StatusUnprocessableEntity)
}

// convRoutes are the routes of a conversation, to try as each kind of caller.
func convRoutes(conv uint64, upTo float64) []struct {
	name, method, path string
	body               any
	status             int
} {
	return []struct {
		name, method, path string
		body               any
		status             int
	}{
		{"get", http.MethodGet, path("/conversations/%d", conv), nil, http.StatusOK},
		{"history", http.MethodGet, path("/conversations/%d/messages", conv), nil, http.StatusOK},
		{"send", http.MethodPost, path("/conversations/%d/messages", conv), map[string]any{"body": "hello"}, http.StatusCreated},
		{"read", http.MethodPost, path("/conversations/%d/read", conv), map[string]any{"up_to_id": upTo}, http.StatusNoContent},
		{"rename", http.MethodPatch, path("/conversations/%d", conv), map[string]any{"name": "renamed"}, http.StatusOK},
		{"add", http.MethodPost, path("/conversations/%d/members", conv), map[string]any{"usernames": []string{"root"}}, http.StatusOK},
		{"leave", http.MethodPost, path("/conversations/%d/leave", conv), nil, http.StatusNoContent},
	}
}

// TestConversationsArePrivate is the table of who may use a conversation: its
// members; anyone else, admins included, and a member who left, get 404.
func TestConversationsArePrivate(t *testing.T) {
	t.Parallel()
	callers := []struct {
		user string
		want int // 0: the status of the route
	}{
		{"alice", 0}, {"bob", 0}, {"admin", http.StatusNotFound}, {"root", http.StatusNotFound},
		{"carol", http.StatusNotFound}, // never a member
		{"dave", http.StatusNotFound},  // left
	}
	for _, rt := range convRoutes(1, 1) {
		for _, c := range callers {
			t.Run(rt.name+" as "+c.user, func(t *testing.T) {
				t.Parallel()
				f := newForum(t)
				f.addUser("dave", store.RoleMember)
				conv := f.convs("alice", "bob", "dave")
				m := f.msg("alice", conv, "secret plans")
				f.ok("dave", http.MethodPost, path("/conversations/%d/leave", conv), nil, http.StatusNoContent)
				rt := convRoutes(conv, m["id"].(float64))[routeIndex(rt.name)]
				rec := f.call(c.user, rt.method, rt.path, rt.body)
				want := c.want
				if want == 0 {
					want = rt.status
				}
				requireStatus(t, rec, want)
				if want == http.StatusNotFound {
					if code := errorCode(t, rec); code != "not_found" {
						t.Errorf("code = %q", code)
					}
					if strings.Contains(rec.Body.String(), "secret") {
						t.Error("the answer leaks the conversation")
					}
					// The same answer as for a conversation that does not exist.
					missing := convRoutes(999999, 1)[routeIndex(rt.name)]
					gone := f.call(c.user, missing.method, missing.path, missing.body)
					if gone.Body.String() != rec.Body.String() {
						t.Errorf("a private conversation answers %q, a missing one %q", rec.Body, gone.Body)
					}
				}
			})
		}
	}
}

// routeIndex is the place of the named route in convRoutes.
func routeIndex(name string) int {
	for i, rt := range convRoutes(1, 1) {
		if rt.name == name {
			return i
		}
	}
	panic("no route " + name)
}

func TestRenameAddAndLeave(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	f.addUser("dave", store.RoleMember)
	group := f.convs("alice", "bob", "carol")
	direct := f.convs("alice", "bob")
	url := path("/conversations/%d", group)

	t.Run("rename", func(t *testing.T) {
		got := f.ok("bob", http.MethodPatch, url, map[string]any{"name": " the crew "}, http.StatusOK)
		if got["name"] != "the crew" || got["display_name"] != "the crew" {
			t.Errorf("conversation = %v", got)
		}
		got = f.ok("carol", http.MethodPatch, url, map[string]any{"name": ""}, http.StatusOK)
		if got["name"] != nil || got["display_name"] == "" {
			t.Errorf("conversation without a name = %v", got)
		}
		for _, tt := range []struct {
			name, field string
			target      string
			body        any
		}{
			{"too long", "name", url, map[string]any{"name": strings.Repeat("x", 81)}},
			{"a direct conversation", "name", path("/conversations/%d", direct), map[string]any{"name": "pals"}},
			{"no name", "name", url, map[string]any{}},
			{"a null name", "name", url, map[string]any{"name": nil}},
			{"an unknown field", "title", url, map[string]any{"title": "x"}},
		} {
			rec := f.call("alice", http.MethodPatch, tt.target, tt.body)
			requireStatus(t, rec, http.StatusUnprocessableEntity)
			if errorFields(t, rec)[tt.field] == nil {
				t.Errorf("%s: fields = %v", tt.name, errorFields(t, rec))
			}
		}
	})
	t.Run("add people", func(t *testing.T) {
		got := f.ok("alice", http.MethodPost, url+"/members", map[string]any{"usernames": []string{"dave", "alice"}}, http.StatusOK)
		if len(list(t, got["members"])) != 4 {
			t.Errorf("members = %v", got["members"])
		}
		// Adding someone who is there is not an error.
		got = f.ok("alice", http.MethodPost, url+"/members", map[string]any{"user_ids": []uint64{f.userID("dave")}}, http.StatusOK)
		if len(list(t, got["members"])) != 4 {
			t.Errorf("members = %v", got["members"])
		}
		for _, tt := range []struct {
			name, field, target string
			body                any
		}{
			{"both lists", "user_ids", url + "/members", map[string]any{"user_ids": []uint64{1}, "usernames": []string{"root"}}},
			{"neither list", "user_ids", url + "/members", map[string]any{}},
			{"an unknown username", "usernames", url + "/members", map[string]any{"usernames": []string{"nobody"}}},
			{"an unknown ID", "user_ids", url + "/members", map[string]any{"user_ids": []uint64{99999}}},
			{"only the caller", "user_ids", url + "/members", map[string]any{"user_ids": []uint64{f.userID("alice")}}},
			{"a direct conversation", "user_ids", path("/conversations/%d/members", direct), map[string]any{"user_ids": []uint64{f.userID("dave")}}},
		} {
			rec := f.call("alice", http.MethodPost, tt.target, tt.body)
			requireStatus(t, rec, http.StatusUnprocessableEntity)
			if errorFields(t, rec)[tt.field] == nil && errorFields(t, rec)["usernames"] == nil {
				t.Errorf("%s: fields = %v", tt.name, errorFields(t, rec))
			}
		}
	})
	t.Run("leave", func(t *testing.T) {
		rec := f.call("carol", http.MethodPost, url+"/leave", nil)
		requireStatus(t, rec, http.StatusNoContent)
		requireStatus(t, f.call("carol", http.MethodGet, url, nil), http.StatusNotFound)
		requireStatus(t, f.call("carol", http.MethodPost, url+"/leave", nil), http.StatusNotFound)
		got := f.ok("alice", http.MethodGet, url, nil, http.StatusOK)
		if len(list(t, got["members"])) != 3 {
			t.Errorf("members after carol left = %v", got["members"])
		}
		// Nobody leaves a direct conversation.
		requireStatus(t, f.call("alice", http.MethodPost, path("/conversations/%d/leave", direct), nil), http.StatusUnprocessableEntity)
		// She can be added again and sees the whole history.
		f.msg("alice", group, "before")
		f.ok("alice", http.MethodPost, url+"/members", map[string]any{"usernames": []string{"carol"}}, http.StatusOK)
		if got := f.history("carol", group, ""); len(list(t, got["messages"])) != 1 {
			t.Errorf("history of a returning member = %v", got)
		}
	})
}

func TestSendMessage(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	conv := f.convs("alice", "bob")
	other := f.convs("alice", "carol")
	url := path("/conversations/%d/messages", conv)

	rec := f.call("alice", http.MethodPost, url, map[string]any{"body": "  <b>hi</b> bob\nsee https://example.com/x?a=1&b=2 \U0001F44D "})
	requireStatus(t, rec, http.StatusCreated)
	m := decode(t, rec)
	html, _ := m["html"].(string)
	if m["body"] != "<b>hi</b> bob\nsee https://example.com/x?a=1&b=2 \U0001F44D" || m["conversation_id"] != float64(conv) ||
		obj(t, m["author"])["username"] != "alice" || m["deleted"] != false || m["edited_at"] != nil || m["reply_to"] != nil ||
		m["can_edit"] != true || m["can_delete"] != true || len(list(t, m["attachments"])) != 0 || len(list(t, m["reactions"])) != 0 {
		t.Errorf("message = %v", m)
	}
	if strings.Contains(html, "<b>") || !strings.Contains(html, "&lt;b&gt;hi&lt;/b&gt;") || !strings.Contains(html, "<br") ||
		!strings.Contains(html, `href="https://example.com/x?a=1&amp;b=2"`) || !strings.Contains(html, "\U0001F44D") {
		t.Errorf("html = %q", html)
	}
	// What bob reads is the same message without the rights over it.
	bobs := list(t, f.history("bob", conv, "")["messages"])
	if len(bobs) != 1 || obj(t, bobs[0])["can_edit"] != false || obj(t, bobs[0])["can_delete"] != false || idOf(t, bobs[0]) != idOf(t, m) {
		t.Errorf("as bob = %v", bobs)
	}
	// The sender has read it; the other has an unread conversation.
	if me := f.ok("bob", http.MethodGet, "/api/v1/me", nil, http.StatusOK); me["unread_conversations"] != float64(1) {
		t.Errorf("bob's unread = %v", me["unread_conversations"])
	}

	for _, tt := range []struct {
		name, field string
		body        any
	}{
		{"an empty body", "body", map[string]any{"body": "   "}},
		{"no body", "body", map[string]any{}},
		{"a body that is too long", "body", map[string]any{"body": strings.Repeat("é", 4001)}},
		{"a reply to a message that does not exist", "reply_to_id", map[string]any{"body": "x", "reply_to_id": 99999}},
		{"an upload that is not one", "upload_ids", map[string]any{"body": "x", "upload_ids": []string{"nope"}}},
		{"a field that is not known", "text", map[string]any{"text": "x"}},
		{"a body that is not text", "body", map[string]any{"body": 5}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.call("alice", http.MethodPost, url, tt.body)
			requireStatus(t, rec, http.StatusUnprocessableEntity)
			if errorFields(t, rec)[tt.field] == nil {
				t.Errorf("fields = %v", errorFields(t, rec))
			}
		})
	}
	t.Run("the longest message", func(t *testing.T) {
		requireStatus(t, f.call("alice", http.MethodPost, url, map[string]any{"body": strings.Repeat("é", 4000)}), http.StatusCreated)
	})
	t.Run("replies", func(t *testing.T) {
		elsewhere := f.msg("alice", other, "in another conversation")
		rec := f.call("bob", http.MethodPost, url, map[string]any{"body": "to you", "reply_to_id": elsewhere["id"]})
		requireStatus(t, rec, http.StatusUnprocessableEntity)
		if errorFields(t, rec)["reply_to_id"] == nil {
			t.Errorf("fields = %v", errorFields(t, rec))
		}
		rec = f.call("bob", http.MethodPost, url, map[string]any{"body": "the answer", "reply_to_id": m["id"]})
		requireStatus(t, rec, http.StatusCreated)
		reply := decode(t, rec)
		quote := obj(t, reply["reply_to"])
		if quote["id"] != m["id"] || quote["deleted"] != false || obj(t, quote["author"])["username"] != "alice" ||
			!strings.HasPrefix(quote["excerpt"].(string), "<b>hi</b> bob") {
			t.Errorf("reply_to = %v", quote)
		}
		// A deleted message is not replied to, and its quote shows no text.
		f.ok("alice", http.MethodDelete, path("/messages/%d", uint64(m["id"].(float64))), nil, http.StatusNoContent)
		rec = f.call("bob", http.MethodPost, url, map[string]any{"body": "too late", "reply_to_id": m["id"]})
		requireStatus(t, rec, http.StatusUnprocessableEntity)
		page := list(t, f.history("alice", conv, "")["messages"])
		var shown map[string]any
		for _, p := range page {
			if idOf(t, p) == idOf(t, reply) {
				shown = obj(t, p)
			}
		}
		if q := obj(t, shown["reply_to"]); q["deleted"] != true || q["excerpt"] != app.MessageDeletedPreview {
			t.Errorf("quote of a deleted message = %v", q)
		}
	})
}

func TestEditAndDeleteMessage(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T) (*forum, uint64, uint64) {
		f := newForum(t)
		conv := f.convs("alice", "bob")
		m := f.msg("alice", conv, "first")
		f.clk.Advance(time.Hour)
		return f, conv, uint64(m["id"].(float64))
	}
	t.Run("the author edits", func(t *testing.T) {
		t.Parallel()
		f, conv, id := setup(t)
		got := f.ok("alice", http.MethodPatch, path("/messages/%d", id), map[string]any{"body": " second *one* "}, http.StatusOK)
		if got["body"] != "second *one*" || got["edited_at"] == nil || got["html"] == "" || idOf(t, got) != id {
			t.Errorf("message = %v", got)
		}
		// Messages are text: no markdown.
		if strings.Contains(got["html"].(string), "<em>") {
			t.Errorf("html = %q", got["html"])
		}
		if edited := obj(t, list(t, f.history("bob", conv, "")["messages"])[0]); edited["edited_at"] == nil {
			t.Errorf("bob sees %v", edited)
		}
		// The same text is not an edit.
		again := f.ok("alice", http.MethodPatch, path("/messages/%d", id), map[string]any{"body": "second *one*"}, http.StatusOK)
		if again["edited_at"] != got["edited_at"] {
			t.Errorf("edited_at moved: %v -> %v", got["edited_at"], again["edited_at"])
		}
	})
	t.Run("who may edit and delete", func(t *testing.T) {
		t.Parallel()
		f, _, id := setup(t)
		for _, tt := range []struct {
			user string
			want int
		}{{"bob", http.StatusForbidden}, {"admin", http.StatusNotFound}, {"root", http.StatusNotFound}, {"carol", http.StatusNotFound}} {
			requireStatus(t, f.call(tt.user, http.MethodPatch, path("/messages/%d", id), map[string]any{"body": "hijacked"}), tt.want)
			requireStatus(t, f.call(tt.user, http.MethodDelete, path("/messages/%d", id), nil), tt.want)
		}
		got := f.history("alice", 1, "")
		if m := obj(t, list(t, got["messages"])[0]); m["body"] != "first" || m["deleted"] != false {
			t.Errorf("message changed: %v", m)
		}
	})
	t.Run("a member who left", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		group := f.convs("alice", "bob", "carol")
		m := f.msg("carol", group, "mine")
		f.ok("carol", http.MethodPost, path("/conversations/%d/leave", group), nil, http.StatusNoContent)
		id := uint64(m["id"].(float64))
		requireStatus(t, f.call("carol", http.MethodPatch, path("/messages/%d", id), map[string]any{"body": "still"}), http.StatusNotFound)
		requireStatus(t, f.call("carol", http.MethodDelete, path("/messages/%d", id), nil), http.StatusNotFound)
	})
	t.Run("mistakes", func(t *testing.T) {
		t.Parallel()
		f, _, id := setup(t)
		for _, tt := range []struct {
			name string
			body any
		}{
			{"empty", map[string]any{"body": ""}},
			{"too long", map[string]any{"body": strings.Repeat("é", 4001)}},
			{"missing", map[string]any{}},
			{"null", map[string]any{"body": nil}},
		} {
			rec := f.call("alice", http.MethodPatch, path("/messages/%d", id), tt.body)
			requireStatus(t, rec, http.StatusUnprocessableEntity)
			if errorFields(t, rec)["body"] == nil {
				t.Errorf("%s: fields = %v", tt.name, errorFields(t, rec))
			}
		}
		requireStatus(t, f.call("alice", http.MethodPatch, path("/messages/%d", 999999), map[string]any{"body": "x"}), http.StatusNotFound)
	})
	t.Run("delete leaves a placeholder", func(t *testing.T) {
		t.Parallel()
		f, conv, id := setup(t)
		f.ok("bob", http.MethodPut, path("/messages/%d/reactions/", id)+thumbsUp, nil, http.StatusOK)
		requireStatus(t, f.call("alice", http.MethodDelete, path("/messages/%d", id), nil), http.StatusNoContent)
		page := list(t, f.history("bob", conv, "")["messages"])
		if len(page) != 1 {
			t.Fatalf("history = %v", page)
		}
		m := obj(t, page[0])
		if m["deleted"] != true || m["body"] != nil || m["html"] != nil || m["edited_at"] != nil || m["can_edit"] != false ||
			m["can_delete"] != false || len(list(t, m["attachments"])) != 0 || len(list(t, m["reactions"])) != 0 ||
			obj(t, m["author"])["username"] != "alice" || m["created_at"] == nil {
			t.Errorf("deleted message = %v", m)
		}
		if c := obj(t, f.conversationList("bob")[0]); obj(t, c["last_message"])["deleted"] != true || obj(t, c["last_message"])["preview"] != app.MessageDeletedPreview {
			t.Errorf("conversation = %v", c)
		}
		// Gone for the rest.
		requireStatus(t, f.call("alice", http.MethodDelete, path("/messages/%d", id), nil), http.StatusNotFound)
		requireStatus(t, f.call("alice", http.MethodPatch, path("/messages/%d", id), map[string]any{"body": "x"}), http.StatusNotFound)
		requireStatus(t, f.call("bob", http.MethodPut, path("/messages/%d/reactions/", id)+thumbsUp, nil), http.StatusNotFound)
	})
}

func TestMessageHistoryPaging(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	conv := f.convs("alice", "bob")
	var made []uint64
	for i := 0; i < 7; i++ {
		made = append(made, uint64(f.msg([]string{"alice", "bob"}[i%2], conv, fmt.Sprintf("m%d", i))["id"].(float64)))
		f.clk.Advance(time.Minute)
	}
	t.Run("the default page", func(t *testing.T) {
		got := f.history("bob", conv, "")
		if ids := messageIDs(t, got); !reflect.DeepEqual(ids, made) {
			t.Errorf("ids = %v, want %v (oldest first)", ids, made)
		}
		if got["next_before"] != nil {
			t.Errorf("next_before = %v on the only page", got["next_before"])
		}
	})
	t.Run("pages of three, from the newest", func(t *testing.T) {
		var pages [][]uint64
		query := "?limit=3"
		for {
			got := f.history("alice", conv, query)
			pages = append(pages, messageIDs(t, got))
			if got["next_before"] == nil {
				break
			}
			if got["next_before"] != float64(messageIDs(t, got)[0]) {
				t.Fatalf("next_before = %v, want the oldest ID %d", got["next_before"], messageIDs(t, got)[0])
			}
			query = fmt.Sprintf("?limit=3&before=%d", uint64(got["next_before"].(float64)))
		}
		want := [][]uint64{made[4:7], made[1:4], made[0:1]}
		if !reflect.DeepEqual(pages, want) {
			t.Errorf("pages = %v, want %v", pages, want)
		}
	})
	t.Run("the largest page", func(t *testing.T) {
		if got := f.history("alice", conv, "?limit=100"); len(list(t, got["messages"])) != 7 {
			t.Errorf("messages = %v", got["messages"])
		}
	})
	for _, query := range []string{"?limit=0", "?limit=101", "?limit=x", "?limit=", "?before=0", "?before=abc", "?before=-1", "?before="} {
		t.Run(query, func(t *testing.T) {
			rec := f.call("alice", http.MethodGet, path("/conversations/%d/messages", conv)+query, nil)
			requireStatus(t, rec, http.StatusUnprocessableEntity)
			if fields := errorFields(t, rec); fields["limit"] == nil && fields["before"] == nil {
				t.Errorf("fields = %v", fields)
			}
		})
	}
	t.Run("an empty conversation", func(t *testing.T) {
		empty := f.convs("alice", "carol")
		got := f.history("alice", empty, "")
		if got["messages"] == nil || len(list(t, got["messages"])) != 0 || got["next_before"] != nil {
			t.Errorf("history = %v", got)
		}
	})
}

func TestMessageReactions(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	conv := f.convs("alice", "bob", "carol")
	m := f.msg("alice", conv, "react to me")
	url := path("/messages/%d/reactions/", uint64(m["id"].(float64))) + thumbsUp
	f.rec.Reset()

	got := f.ok("bob", http.MethodPut, url, nil, http.StatusOK)
	r := obj(t, list(t, got["reactions"])[0])
	if r["emoji"] != "\U0001F44D" || r["count"] != float64(1) || r["mine"] != true || len(list(t, r["users"])) != 1 {
		t.Errorf("reactions = %v", got)
	}
	want := []live.Event{{Topic: live.ConversationTopic(conv), Kind: live.KindMessageReaction, ConversationID: conv, MessageID: uint64(m["id"].(float64))}}
	if ev := f.rec.Events(); !reflect.DeepEqual(ev, want) {
		t.Errorf("published %+v, want %+v", ev, want)
	}
	// A second time changes nothing.
	f.rec.Reset()
	got = f.ok("bob", http.MethodPut, url, nil, http.StatusOK)
	if r := obj(t, list(t, got["reactions"])[0]); r["count"] != float64(1) {
		t.Errorf("reactions after putting twice = %v", got)
	}
	if ev := f.rec.Events(); len(ev) != 0 {
		t.Errorf("a repeat published %+v", ev)
	}
	got = f.ok("alice", http.MethodPut, url, nil, http.StatusOK)
	if r := obj(t, list(t, got["reactions"])[0]); r["count"] != float64(2) || r["mine"] != true {
		t.Errorf("as alice = %v", got)
	}
	// Seen from the history, per reader.
	if carols := obj(t, list(t, f.history("carol", conv, "")["messages"])[0]); obj(t, list(t, carols["reactions"])[0])["mine"] != false {
		t.Errorf("as carol = %v", carols["reactions"])
	}
	got = f.ok("bob", http.MethodDelete, url, nil, http.StatusOK)
	if r := obj(t, list(t, got["reactions"])[0]); r["count"] != float64(1) || r["mine"] != false {
		t.Errorf("after bob took his off = %v", got)
	}
	// Taking off one that is not there is fine.
	got = f.ok("bob", http.MethodDelete, url, nil, http.StatusOK)
	if len(list(t, got["reactions"])) != 1 {
		t.Errorf("reactions = %v", got)
	}
	// Who may.
	for _, tt := range []struct {
		user string
		want int
	}{{"admin", http.StatusNotFound}, {"root", http.StatusNotFound}, {"dave", http.StatusNotFound}} {
		if tt.user == "dave" {
			f.addUser("dave", store.RoleMember)
		}
		requireStatus(t, f.call(tt.user, http.MethodPut, url, nil), tt.want)
		requireStatus(t, f.call(tt.user, http.MethodDelete, url, nil), tt.want)
	}
	// Mistakes.
	for _, emoji := range []string{"x", "ab", thumbsUp + thumbsUp, "%20"} {
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			rec := f.call("bob", method, path("/messages/%d/reactions/", uint64(m["id"].(float64)))+emoji, nil)
			requireStatus(t, rec, http.StatusUnprocessableEntity)
			if errorFields(t, rec)["emoji"] == nil {
				t.Errorf("%s %s: fields = %v", method, emoji, errorFields(t, rec))
			}
		}
	}
}

func TestMessageUploads(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	conv := f.convs("alice", "bob")
	url := path("/conversations/%d/messages", conv)

	t.Run("one upload serves a message", func(t *testing.T) {
		id := f.uploadID("alice", "../plan.txt", "the plan")
		rec := f.call("alice", http.MethodPost, url, map[string]any{"upload_ids": []string{id}})
		requireStatus(t, rec, http.StatusCreated)
		m := decode(t, rec)
		atts := list(t, m["attachments"])
		if len(atts) != 1 || m["body"] != "" {
			t.Fatalf("message = %v", m)
		}
		att := obj(t, atts[0])
		if att["file_name"] != "plan.txt" || att["size"] != float64(8) || att["width"] != nil ||
			att["url"] != path("/messages/attachments/%d", uint64(att["id"].(float64))) {
			t.Errorf("attachment = %v", att)
		}
		// The ID is spent; the note is gone.
		if notes := f.notes(); len(notes) != 0 {
			t.Errorf("notes left: %v", notes)
		}
		rec = f.call("alice", http.MethodPost, url, map[string]any{"body": "again", "upload_ids": []string{id}})
		requireStatus(t, rec, http.StatusUnprocessableEntity)
		if errorFields(t, rec)["upload_ids"] == nil {
			t.Errorf("fields = %v", errorFields(t, rec))
		}
		// And it downloads for the members.
		dl := f.call("bob", http.MethodGet, att["url"].(string), nil)
		requireStatus(t, dl, http.StatusOK)
		if dl.Body.String() != "the plan" {
			t.Errorf("file = %q", dl.Body)
		}
	})
	t.Run("an image has its size", func(t *testing.T) {
		id := f.uploadID("alice", "dot.png", pngBytes(t, 3, 2))
		m := f.ok("alice", http.MethodPost, url, map[string]any{"body": "look", "upload_ids": []string{id}}, http.StatusCreated)
		if att := obj(t, list(t, m["attachments"])[0]); att["width"] != float64(3) || att["height"] != float64(2) || att["content_type"] != "image/png" {
			t.Errorf("attachment = %v", att)
		}
	})
	t.Run("another user's upload is refused", func(t *testing.T) {
		id := f.uploadID("bob", "b.txt", "x")
		rec := f.call("alice", http.MethodPost, url, map[string]any{"body": "mine now", "upload_ids": []string{id}})
		requireStatus(t, rec, http.StatusUnprocessableEntity)
		if errorFields(t, rec)["upload_ids"] == nil {
			t.Errorf("fields = %v", errorFields(t, rec))
		}
		// It is still bob's.
		requireStatus(t, f.call("bob", http.MethodPost, url, map[string]any{"upload_ids": []string{id}}), http.StatusCreated)
	})
	t.Run("a refused message removes its files", func(t *testing.T) {
		before := len(f.onDisk())
		id := f.uploadID("alice", "c.txt", "x")
		rec := f.call("alice", http.MethodPost, url, map[string]any{"body": "x", "upload_ids": []string{id}, "reply_to_id": 99999})
		requireStatus(t, rec, http.StatusUnprocessableEntity)
		if disk := f.onDisk(); len(disk) != before {
			t.Errorf("files on disk = %v, want %d", disk, before)
		}
		if notes := f.notes(); len(notes) != 0 {
			t.Errorf("notes left: %v", notes)
		}
		// Not a member of the conversation.
		id = f.uploadID("carol", "d.txt", "x")
		requireStatus(t, f.call("carol", http.MethodPost, url, map[string]any{"upload_ids": []string{id}}), http.StatusNotFound)
		if disk := f.onDisk(); len(disk) != before {
			t.Errorf("files on disk = %v after a non-member's message", disk)
		}
	})
	t.Run("too many files", func(t *testing.T) {
		var ids []string
		for i := 0; i < 5; i++ {
			ids = append(ids, f.uploadID("alice", fmt.Sprintf("f%d.txt", i), "x"))
		}
		rec := f.call("alice", http.MethodPost, url, map[string]any{"upload_ids": ids})
		requireStatus(t, rec, http.StatusUnprocessableEntity)
		if errorFields(t, rec)["upload_ids"] == nil {
			t.Errorf("fields = %v", errorFields(t, rec))
		}
	})
}

func TestMessageDownload(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T) (*forum, uint64, uint64) {
		f := newForum(t)
		f.addUser("dave", store.RoleMember)
		conv := f.convs("alice", "bob", "dave")
		id := f.uploadID("alice", "secret.txt", "secret content")
		m := f.ok("alice", http.MethodPost, path("/conversations/%d/messages", conv), map[string]any{"upload_ids": []string{id}}, http.StatusCreated)
		return f, conv, idOf(t, list(t, m["attachments"])[0])
	}
	for _, tt := range []struct {
		user string
		want int
	}{{"alice", http.StatusOK}, {"bob", http.StatusOK}, {"dave", http.StatusOK},
		{"carol", http.StatusNotFound}, {"admin", http.StatusNotFound}, {"root", http.StatusNotFound}} {
		t.Run(tt.user, func(t *testing.T) {
			t.Parallel()
			f, _, att := setup(t)
			rec := f.call(tt.user, http.MethodGet, path("/messages/attachments/%d", att), nil)
			requireStatus(t, rec, tt.want)
			if tt.want == http.StatusOK {
				for name, want := range map[string]string{
					"Content-Type":            "application/octet-stream",
					"Content-Disposition":     `attachment; filename="secret.txt"`,
					"X-Content-Type-Options":  "nosniff",
					"Content-Security-Policy": "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; sandbox",
					"Cache-Control":           "private, max-age=86400",
				} {
					if got := rec.Header().Get(name); got != want {
						t.Errorf("%s = %q, want %q", name, got, want)
					}
				}
				return
			}
			missing := f.call(tt.user, http.MethodGet, path("/messages/attachments/%d", att+1000), nil)
			if rec.Body.String() != missing.Body.String() || strings.Contains(rec.Body.String(), "secret") {
				t.Errorf("a file of a private conversation answers %q, a missing one %q", rec.Body, missing.Body)
			}
		})
	}
	t.Run("a member who left", func(t *testing.T) {
		t.Parallel()
		f, conv, att := setup(t)
		f.ok("dave", http.MethodPost, path("/conversations/%d/leave", conv), nil, http.StatusNoContent)
		requireStatus(t, f.call("dave", http.MethodGet, path("/messages/attachments/%d", att), nil), http.StatusNotFound)
	})
	t.Run("a deleted message", func(t *testing.T) {
		t.Parallel()
		f, conv, att := setup(t)
		m := list(t, f.history("alice", conv, "")["messages"])[0]
		f.ok("alice", http.MethodDelete, path("/messages/%d", idOf(t, m)), nil, http.StatusNoContent)
		requireStatus(t, f.call("bob", http.MethodGet, path("/messages/attachments/%d", att), nil), http.StatusNotFound)
		if disk := f.onDisk(); len(disk) != 0 {
			t.Errorf("the file stays on disk: %v", disk)
		}
	})
	t.Run("the two kinds of attachment have their own paths", func(t *testing.T) {
		t.Parallel()
		// More files in one table than in the other, so that the last ID of
		// the larger one is not an ID of the smaller.
		for _, tt := range []struct {
			name         string
			posts, files int
			dir          string
		}{
			{"a post's file is no message's", 2, 1, "/messages/attachments/%d"},
			{"a message's file is no post's", 1, 2, "/attachments/%d"},
		} {
			f := newForum(t)
			conv := f.convs("alice", "bob")
			var postIDs []uint64
			for i := 0; i < tt.posts; i++ {
				postIDs = append(postIDs, f.attachmentsOf("bob", "lobby", [2]string{fmt.Sprintf("p%d.txt", i), "x"})...)
			}
			var msgIDs []uint64
			for i := 0; i < tt.files; i++ {
				id := f.uploadID("alice", fmt.Sprintf("m%d.txt", i), "x")
				m := f.ok("alice", http.MethodPost, path("/conversations/%d/messages", conv), map[string]any{"upload_ids": []string{id}}, http.StatusCreated)
				msgIDs = append(msgIDs, idOf(t, list(t, m["attachments"])[0]))
			}
			ids := postIDs
			if tt.dir == "/attachments/%d" {
				ids = msgIDs
			}
			requireStatus(t, f.call("alice", http.MethodGet, path(tt.dir, ids[len(ids)-1]), nil), http.StatusNotFound)
			requireStatus(t, f.call("bob", http.MethodGet, path(tt.dir, ids[len(ids)-1]), nil), http.StatusNotFound)
		}
	})
	t.Run("IDs that are no attachment", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		for _, id := range []string{"999999", "0", "abc", "-1", "1.5"} {
			rec := f.call("alice", http.MethodGet, "/api/v1/messages/attachments/"+id, nil)
			requireStatus(t, rec, http.StatusNotFound)
		}
	})
}

// TestMessageRoutesOfTheErrors checks the route of the tags that the messages
// routes make in this package.
func TestMessageRoutesOfTheErrors(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	alice := actorOf(f.testEnv, "alice")
	people := func(ids []uint64, names []string) error {
		_, _, err := f.srv.peopleOf(httptest.NewRequest(http.MethodPost, "/x", nil), alice, ids, names)
		return err
	}
	tests := []struct {
		name  string
		err   error
		tag   string
		route string
	}{
		{"both lists", people([]uint64{2}, []string{"bob"}), tagListsInvalid, "Server.peopleOf.ListsInvalid"},
		{"neither list", people(nil, nil), tagListsInvalid, "Server.peopleOf.ListsInvalid"},
		{"no one but the caller", checkStart(nil, "user_ids", ""), tagNoOthers, "checkStart.NoOtherPeople"},
		{"a name for a direct conversation", checkStart([]uint64{2}, "user_ids", "x"), tagDirectName, "checkStart.DirectNameInvalid"},
		{"a missing text", func() error { _, err := requiredText("name", nil); return err }(), tagFieldMissing, "requiredText.FieldMissing"},
		{"nothing to change", checkUserPatch(adminUserPatch{}), tagAdminUserInvalid, "checkUserPatch.AdminUserChangeEmpty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			appErr := requireRoute(t, tt.err, tt.tag, tt.route)
			if fields, _ := appErr.Metadata["fields"].(map[string]string); len(fields) == 0 {
				t.Errorf("no fields in %v", appErr.Metadata)
			}
			p, internal := problemOf(tt.err)
			if internal || p.status != http.StatusUnprocessableEntity || p.object.Code != "invalid_input" {
				t.Errorf("problemOf = %+v, %v; want 422 invalid_input", p, internal)
			}
		})
	}
	if _, err := requiredText("name", new(string)); err != nil {
		t.Errorf("an empty text is there: %v", err)
	}
	if err := checkStart([]uint64{2, 3}, "user_ids", "crew"); err != nil {
		t.Errorf("a named group: %v", err)
	}
	if err := checkUserPatch(adminUserPatch{Disabled: new(bool)}); err != nil {
		t.Errorf("a change of disabled: %v", err)
	}
}
