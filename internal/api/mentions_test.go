package api

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// countOf reads the group-mention-count of a room as the user.
func (f *forum) countOf(user, room, query string) (int, int) {
	f.t.Helper()
	rec := f.call(user, http.MethodGet, "/api/v1/rooms/"+strconv.FormatUint(f.room(room).ID, 10)+"/group-mention-count"+query, nil)
	if rec.Code != http.StatusOK {
		return 0, rec.Code
	}
	return int(decode(f.t, rec)["count"].(float64)), rec.Code
}

func TestRoomGroupMentionCount(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	// lobby has admin and bob; make a third and a fourth member.
	for _, name := range []string{"carol", "alice"} {
		if err := f.rooms.Join(t.Context(), actorOf(f.testEnv, name), f.lobby.ID); err != nil {
			t.Fatal(err)
		}
	}
	q := func(body, format string) string {
		v := url.Values{"body": {body}}
		if format != "" {
			v.Set("format", format)
		}
		return "?" + v.Encode()
	}
	tests := []struct {
		name      string
		user      string
		room      string
		query     string
		wantCount int
		wantCode  int
	}{
		{"@room counts the other members", "admin", "lobby", q("Hello @room", ""), 3, 200},
		{"@all is the same", "admin", "lobby", q("@all", "text"), 3, 200},
		{"@here counts nobody online", "admin", "lobby", q("@here", ""), 0, 200},
		{"no group mention", "admin", "lobby", q("Hello @bob", ""), 0, 200},
		{"in code it does not count", "admin", "lobby", q("`@room`", "markdown"), 0, 200},
		{"in code of a text body it does", "admin", "lobby", q("`@room`", "text"), 3, 200},
		{"no body", "admin", "lobby", "", 0, 200},
		{"a format that is not one", "admin", "lobby", q("@room", "html"), 0, 422},
		{"a visible room not joined", "root", "lobby", q("@room", ""), 0, 403},
		{"a private room of others", "carol", "den", q("@room", ""), 0, 404},
		{"a private room, even for an admin", "admin", "den", q("@room", ""), 0, 404},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, code := f.countOf(tt.user, tt.room, tt.query)
			if code != tt.wantCode || got != tt.wantCount {
				t.Errorf("count = %d, status = %d; want %d, %d", got, code, tt.wantCount, tt.wantCode)
			}
		})
	}

	// @here counts those who are online, and the settings of a member take
	// them out.
	f.presence.Connect(f.userID("bob"))
	f.presence.Connect(f.userID("carol"))
	if got, _ := f.countOf("admin", "lobby", q("@here", "")); got != 2 {
		t.Errorf("@here with two online = %d, want 2", got)
	}
	f.ok("carol", http.MethodPatch, "/api/v1/me/settings", map[string]any{"group_mentions": false}, http.StatusOK)
	if got, _ := f.countOf("admin", "lobby", q("@here", "")); got != 1 {
		t.Errorf("@here with carol's group mentions off = %d, want 1", got)
	}
	if got, _ := f.countOf("admin", "lobby", q("@room", "")); got != 2 {
		t.Errorf("@room with carol's group mentions off = %d, want 2", got)
	}
	if rec := f.call("carol", http.MethodGet, "/api/v1/rooms/999999/group-mention-count", nil); rec.Code != http.StatusNotFound {
		t.Errorf("a missing room: status = %d, want 404", rec.Code)
	}
	if rec := f.call("carol", http.MethodGet, "/api/v1/rooms/x/group-mention-count", nil); rec.Code != http.StatusNotFound {
		t.Errorf("a room that is no number: status = %d, want 404", rec.Code)
	}
}

func TestConversationGroupMentionCount(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	group := f.convs("alice", "bob", "carol", "admin")
	pair := f.convs("alice", "bob")
	path := func(id uint64) string {
		return "/api/v1/conversations/" + strconv.FormatUint(id, 10) + "/group-mention-count"
	}
	tests := []struct {
		name  string
		user  string
		conv  uint64
		query string
		want  int
		code  int
	}{
		{"@room in a group", "alice", group, "?body=%40room", 3, 200},
		{"a format is accepted and ignored", "alice", group, "?body=%40all&format=html", 3, 200},
		{"nothing to count", "alice", group, "?body=hello", 0, 200},
		{"a direct conversation has none", "alice", pair, "?body=%40room", 0, 200},
		{"not a member", "carol", pair, "?body=%40room", 0, 404},
		{"a missing one", "alice", 999999, "?body=%40room", 0, 404},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.call(tt.user, http.MethodGet, path(tt.conv)+tt.query, nil)
			if rec.Code != tt.code {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.code, rec.Body)
			}
			if tt.code == 200 {
				if got := int(decode(t, rec)["count"].(float64)); got != tt.want {
					t.Errorf("count = %d, want %d", got, tt.want)
				}
			}
		})
	}
}

func TestFollowRoutes(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	post := f.write("bob", "lobby", "A post")
	comment := f.say("admin", post.ID, "A comment")
	secret := f.write("alice", "den", "Private")
	pp := func(id uint64) string { return "/api/v1/posts/" + strconv.FormatUint(id, 10) }
	following := func(user string, id uint64) bool {
		return f.ok(user, http.MethodGet, pp(id), nil, http.StatusOK)["following"] == true
	}
	if !following("bob", post.ID) || !following("admin", post.ID) {
		t.Fatal("the author and the commenter follow the post")
	}
	if following("bob", comment.ID) != true {
		t.Error("a comment says whether the root is followed")
	}

	steps := []struct {
		name, user, method string
		id                 uint64
		code               int
		want               *bool // following after, nil when the step fails
	}{
		{"unfollow", "bob", http.MethodDelete, post.ID, 204, ptr(false)},
		{"unfollow again", "bob", http.MethodDelete, post.ID, 204, ptr(false)},
		{"follow", "bob", http.MethodPut, post.ID, 204, ptr(true)},
		{"follow again", "bob", http.MethodPut, post.ID, 204, ptr(true)},
		{"unfollow through a comment", "bob", http.MethodDelete, comment.ID, 204, ptr(false)},
		{"follow through a comment", "bob", http.MethodPut, comment.ID, 204, ptr(true)},
		{"a visible room, not joined", "carol", http.MethodPut, post.ID, 204, ptr(true)},
		{"a private post of others", "carol", http.MethodPut, secret.ID, 404, nil},
		{"a private post of others, unfollow", "carol", http.MethodDelete, secret.ID, 404, nil},
		{"a missing post", "bob", http.MethodPut, 999999, 404, nil},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			rec := f.call(s.user, s.method, pp(s.id)+"/follow", nil)
			if rec.Code != s.code {
				t.Fatalf("status = %d, want %d: %s", rec.Code, s.code, rec.Body)
			}
			if s.want != nil && following(s.user, post.ID) != *s.want {
				t.Errorf("following = %v, want %v", !*s.want, *s.want)
			}
		})
	}
	if rec := f.call("bob", http.MethodPut, "/api/v1/posts/x/follow", nil); rec.Code != http.StatusNotFound {
		t.Errorf("an ID that is no number: status = %d, want 404", rec.Code)
	}
	// The automatic follow of a comment does not undo an unfollow.
	f.ok("carol", http.MethodDelete, pp(post.ID)+"/follow", nil, http.StatusNoContent)
	f.rooms.Join(t.Context(), actorOf(f.testEnv, "carol"), f.lobby.ID) //nolint:errcheck // joined once; the error is the same as in the next line
	f.say("carol", post.ID, "I comment")
	if following("carol", post.ID) {
		t.Error("a comment brought back the follow that carol removed")
	}
}

func ptr[T any](v T) *T { return &v }

func TestPresenceRoute(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	ids := func(names ...string) string {
		var parts []string
		for _, n := range names {
			parts = append(parts, strconv.FormatUint(f.userID(n), 10))
		}
		return strings.Join(parts, ",")
	}
	online := func(user, query string) map[string]any {
		return obj(t, f.ok(user, http.MethodGet, "/api/v1/me/presence"+query, nil, http.StatusOK)["online"])
	}
	if got := online("alice", "?user_ids="+ids("bob", "carol")); len(got) != 0 {
		t.Errorf("online = %v, want nobody", got)
	}
	f.presence.Connect(f.userID("bob"))
	f.presence.Connect(f.userID("carol"))
	// Any user may ask about any users, the asker included.
	got := online("alice", "?user_ids="+ids("bob", "carol", "alice"))
	if len(got) != 2 || got[strconv.FormatUint(f.userID("bob"), 10)] != true || got[strconv.FormatUint(f.userID("carol"), 10)] != true {
		t.Errorf("online = %v, want bob and carol", got)
	}
	// A disabled user is never online.
	if err := f.accounts.SetDisabled(t.Context(), actorOf(f.testEnv, "admin"), f.userID("carol"), true); err != nil {
		t.Fatal(err)
	}
	if got := online("alice", "?user_ids="+ids("bob", "carol")); len(got) != 1 {
		t.Errorf("online = %v, want only bob", got)
	}
	if got := online("alice", ""); len(got) != 0 {
		t.Errorf("no IDs: online = %v", got)
	}
	hundred := make([]string, 100)
	for i := range hundred {
		hundred[i] = strconv.Itoa(i + 1)
	}
	f.ok("alice", http.MethodGet, "/api/v1/me/presence?user_ids="+strings.Join(hundred, ","), nil, http.StatusOK)
	tests := []struct{ name, query, field string }{
		{"101 IDs", "?user_ids=" + strings.Join(append(hundred, "101"), ","), "user_ids"},
		{"not a number", "?user_ids=1,x", "user_ids"},
		{"zero", "?user_ids=0", "user_ids"},
		{"negative", "?user_ids=-3", "user_ids"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.call("alice", http.MethodGet, "/api/v1/me/presence"+tt.query, nil)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body)
			}
			fields := obj(t, obj(t, decode(t, rec)["error"])["fields"])
			if fields[tt.field] == nil {
				t.Errorf("fields = %v, want %s", fields, tt.field)
			}
		})
	}
}

func TestWebSocketMakesTheUserOnline(t *testing.T) {
	t.Parallel()
	w := newWSForum(t)
	bob := w.userID("bob")
	isOnline := func() bool {
		got, err := w.presence.Online(t.Context(), []uint64{bob})
		if err != nil {
			t.Fatal(err)
		}
		return got[bob]
	}
	waitFor := func(want bool) {
		t.Helper()
		for range 500 {
			if isOnline() == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("bob online = %v, want %v", isOnline(), want)
	}
	waitFor(false)
	c := w.dial("bob")
	waitFor(true)
	_ = c.conn.CloseNow()
	waitFor(false)
}

func TestKeywordsInTheSettings(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	patch := func(body map[string]any, code int) map[string]any {
		return f.ok("bob", http.MethodPatch, "/api/v1/me/settings", body, code)
	}
	got := patch(map[string]any{"keywords": []string{"  deploy ", "Feeder   schedule"}, "group_mentions": false}, 200)
	sameJSON(t, "keywords", got["keywords"], []any{"deploy", "Feeder schedule"})
	if got["group_mentions"] != false {
		t.Errorf("group_mentions = %v", got["group_mentions"])
	}
	again := f.ok("bob", http.MethodGet, "/api/v1/me/settings", nil, http.StatusOK)
	sameJSON(t, "stored", again, got)
	// null keeps, [] clears.
	if kept := patch(map[string]any{"keywords": nil, "sound": false}, 200); len(list(t, kept["keywords"])) != 2 {
		t.Errorf("null changed the keywords: %v", kept["keywords"])
	}
	if cleared := patch(map[string]any{"keywords": []string{}}, 200); len(list(t, cleared["keywords"])) != 0 {
		t.Errorf("[] did not clear: %v", cleared["keywords"])
	}
	tests := []struct {
		name     string
		keywords any
	}{
		{"11 keywords", []string{"a1", "b2", "c3", "d4", "e5", "f6", "g7", "h8", "i9", "j10", "k11"}},
		{"33 characters", []string{strings.Repeat("x", 33)}},
		{"blank", []string{"ok", "   "}},
		{"twice ignoring case", []string{"Deploy", "deploy"}},
		{"not strings", []any{1, 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.call("bob", http.MethodPatch, "/api/v1/me/settings", map[string]any{"keywords": tt.keywords})
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body)
			}
			named := false
			for name := range obj(t, obj(t, decode(t, rec)["error"])["fields"]) {
				named = named || strings.HasPrefix(name, "keywords")
			}
			if !named {
				t.Errorf("the error does not name keywords: %s", rec.Body)
			}
		})
	}
}

func TestGroupMentionNotificationsOverTheAPI(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	f.ok("bob", http.MethodPatch, "/api/v1/me/settings", map[string]any{"keywords": []string{"feeder"}}, http.StatusOK)
	f.write("admin", "lobby", "Hello @room")
	f.clk.Advance(time.Minute)
	f.write("admin", "lobby", "The feeder is empty")
	page := f.notificationsOf("bob", "")
	kinds := kindsOf(t, page)
	if strings.Join(kinds, ",") != "keyword,group_mention,added_to_room" {
		t.Errorf("kinds = %v, want keyword, group_mention and the addition to the den", kinds)
	}
	for _, n := range list(t, page["notifications"]) {
		if obj(t, n)["others"] != float64(0) {
			t.Errorf("others = %v, want 0", obj(t, n)["others"])
		}
	}
}

func TestFoldedReplyOverTheAPIAndTheWebSocket(t *testing.T) {
	t.Parallel()
	w := newWSForum(t)
	post := w.write("bob", "lobby", "A post")
	own := topicOf(topicUser, w.userID("bob"))
	c := w.dial("bob")
	w.say("admin", post.ID, "one")
	created := c.await(own, "notification.created")
	if obj(t, created["data"])["kind"] != "reply" || obj(t, created["data"])["others"] != float64(0) {
		t.Fatalf("notification.created data = %v", created["data"])
	}
	// A second person replies within the minute: the notification is folded.
	if err := w.rooms.Join(t.Context(), actorOf(w.testEnv, "carol"), w.lobby.ID); err != nil {
		t.Fatal(err)
	}
	w.say("carol", post.ID, "two")
	changed := c.await(own, "notification.changed")
	data := obj(t, changed["data"])
	if data["others"] != float64(0) && data["others"] != float64(1) {
		t.Errorf("others = %v", data["others"])
	}
	if data["id"] != obj(t, created["data"])["id"] || obj(t, data["actor"])["username"] != "carol" || data["others"] != float64(1) {
		t.Errorf("notification.changed data = %v, want the same notification, by carol, with 1 other", data)
	}
	if changed["ring"] == true {
		t.Error("a folded reply rings")
	}
	page := w.notificationsOf("bob", "")
	if n := len(list(t, page["notifications"])); n != 2 { // the den addition and the folded reply
		t.Errorf("%d notifications, want 2", n)
	}
	if w.unread("bob") != 2 {
		t.Errorf("unread = %v, want 2", w.unread("bob"))
	}
}
