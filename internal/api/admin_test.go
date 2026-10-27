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

// TestAdminRoutesAreForAdmins checks that a member gets 403 forbidden from
// every admin route, before anything is looked at, that nothing changes, that
// an admin is let in, and that a disabled admin is 401.
func TestAdminRoutesAreForAdmins(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, method string
		target       func(f *forum) string
		body         func(f *forum) any
		want         int // for an admin
	}{
		{"users", http.MethodGet, func(*forum) string { return "/api/v1/admin/users" }, nil, http.StatusOK},
		{"change a user", http.MethodPatch, func(f *forum) string { return path("/admin/users/%d", f.userID("bob")) },
			func(*forum) any { return map[string]any{"role": "admin", "disabled": true} }, http.StatusOK},
		{"invitation", http.MethodPost, func(*forum) string { return "/api/v1/admin/invitations" },
			func(*forum) any { return map[string]any{"role": "member"} }, http.StatusCreated},
		{"rooms", http.MethodGet, func(*forum) string { return "/api/v1/admin/rooms" }, nil, http.StatusOK},
		{"room members", http.MethodGet, func(f *forum) string { return path("/admin/rooms/%d/members", f.den.ID) }, nil, http.StatusOK},
		{"audit", http.MethodGet, func(*forum) string { return "/api/v1/admin/audit" }, nil, http.StatusOK},
	}
	for _, rt := range cases {
		t.Run(rt.name, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			var body any
			if rt.body != nil {
				body = rt.body(f)
			}
			target := rt.target(f)
			before := len(f.audit())
			for _, user := range []string{"alice", "bob", "carol"} {
				rec := f.call(user, rt.method, target, body)
				requireStatus(t, rec, http.StatusForbidden)
				if code := errorCode(t, rec); code != "forbidden" {
					t.Errorf("%s: code = %q", user, code)
				}
			}
			f.requireAudit(before)
			if bob, err := f.st.UserByUsername(t.Context(), "bob"); err != nil || bob.Role != "member" || bob.DisabledAt != nil {
				t.Errorf("bob = %+v, %v after refused changes", bob, err)
			}
			requireStatus(t, f.call("root", rt.method, target, body), rt.want)
			token := f.as("admin")
			disable(f, "admin")
			requireStatus(t, f.sendTo(rt.method, target, body, token), http.StatusUnauthorized)
		})
	}
}

// sendTo sends a request with the bearer token, through the checked path.
func (f *forum) sendTo(method, path string, body any, token string) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.send(request(method, path, body, bearer(token)))
}

func TestAdminUsers(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	disable(f, "carol")
	got := f.ok("root", http.MethodGet, "/api/v1/admin/users", nil, http.StatusOK)
	users := list(t, got["users"])
	var names []string
	byName := map[string]map[string]any{}
	for _, u := range users {
		m := obj(t, u)
		names = append(names, m["username"].(string))
		byName[m["username"].(string)] = m
	}
	if want := []string{"admin", "alice", "bob", "carol", "root"}; !reflect.DeepEqual(names, want) {
		t.Errorf("users = %v, want %v (by username)", names, want)
	}
	if byName["carol"]["disabled"] != true || byName["bob"]["disabled"] != false ||
		byName["root"]["role"] != "admin" || byName["bob"]["role"] != "member" || byName["bob"]["display_name"] != "Bob" {
		t.Errorf("users = %v", byName)
	}
	if _, ok := byName["bob"]["password_hash"]; ok {
		t.Error("a password hash is in the list")
	}
}

func TestAdminPatchUser(t *testing.T) {
	t.Parallel()
	url := func(f *forum, name string) string { return path("/admin/users/%d", f.userID(name)) }

	t.Run("role", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		f.rec.Reset()
		got := f.ok("admin", http.MethodPatch, url(f, "bob"), map[string]any{"role": "admin"}, http.StatusOK)
		if got["role"] != "admin" || got["username"] != "bob" || got["disabled"] != false {
			t.Errorf("user = %v", got)
		}
		want := []live.Event{{Topic: live.UserTopic(f.userID("bob")), Kind: live.KindUserChanged, UserID: f.userID("bob")}}
		if ev := f.rec.Events(); !reflect.DeepEqual(ev, want) {
			t.Errorf("published %+v, want %+v", ev, want)
		}
		// The new admin can use the admin routes at once.
		requireStatus(t, f.call("bob", http.MethodGet, "/api/v1/admin/users", nil), http.StatusOK)
		// The same role again is not a change.
		f.rec.Reset()
		f.ok("admin", http.MethodPatch, url(f, "bob"), map[string]any{"role": "admin"}, http.StatusOK)
		if ev := f.rec.Events(); len(ev) != 0 {
			t.Errorf("a change of nothing published %+v", ev)
		}
		got = f.ok("admin", http.MethodPatch, url(f, "bob"), map[string]any{"role": "member"}, http.StatusOK)
		if got["role"] != "member" {
			t.Errorf("user = %v", got)
		}
		requireStatus(t, f.call("bob", http.MethodGet, "/api/v1/admin/users", nil), http.StatusForbidden)
	})
	t.Run("disabling ends the sessions, tokens included", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		token := f.as("bob")
		_ = f.browserToken("bob")
		requireStatus(t, f.sendTo(http.MethodGet, "/api/v1/me", nil, token), http.StatusOK)
		f.rec.Reset()
		got := f.ok("admin", http.MethodPatch, url(f, "bob"), map[string]any{"disabled": true}, http.StatusOK)
		if got["disabled"] != true {
			t.Errorf("user = %v", got)
		}
		want := []live.Event{{Topic: live.UserTopic(f.userID("bob")), Kind: live.KindUserChanged, UserID: f.userID("bob")}}
		if ev := f.rec.Events(); !reflect.DeepEqual(ev, want) {
			t.Errorf("published %+v, want %+v", ev, want)
		}
		rec := f.sendTo(http.MethodGet, "/api/v1/me", nil, token)
		requireStatus(t, rec, http.StatusUnauthorized)
		if code := errorCode(t, rec); code != "unauthenticated" {
			t.Errorf("code = %q", code)
		}
		if _, _, err := f.accounts.Login(t.Context(), "bob", testPassword, "192.0.2.50"); err == nil {
			t.Error("a disabled user logged in")
		}
		if n, err := f.st.CountSessionsOfUser(t.Context(), f.userID("bob")); err != nil || n != 0 {
			t.Errorf("sessions = %d, %v after disabling", n, err)
		}
		// Enabling lets the user in again, with a new login.
		got = f.ok("admin", http.MethodPatch, url(f, "bob"), map[string]any{"disabled": false}, http.StatusOK)
		if got["disabled"] != false {
			t.Errorf("user = %v", got)
		}
		requireStatus(t, f.sendTo(http.MethodGet, "/api/v1/me", nil, token), http.StatusUnauthorized)
		requireStatus(t, f.sendTo(http.MethodGet, "/api/v1/me", nil, f.token("bob")), http.StatusOK)
	})
	t.Run("both at once", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		got := f.ok("admin", http.MethodPatch, url(f, "carol"), map[string]any{"role": "admin", "disabled": true}, http.StatusOK)
		if got["role"] != "admin" || got["disabled"] != true {
			t.Errorf("user = %v", got)
		}
	})
	t.Run("the last admin", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		// root is disabled, so admin is the only active admin.
		f.ok("admin", http.MethodPatch, url(f, "root"), map[string]any{"disabled": true}, http.StatusOK)
		for _, body := range []map[string]any{{"role": "member"}, {"disabled": true}} {
			rec := f.call("admin", http.MethodPatch, url(f, "admin"), body)
			status := http.StatusConflict
			if body["disabled"] == true {
				status = http.StatusUnprocessableEntity // an admin does not disable themselves
			}
			requireStatus(t, rec, status)
		}
		if got := f.ok("admin", http.MethodPatch, url(f, "admin"), map[string]any{"role": "admin"}, http.StatusOK); got["role"] != "admin" {
			t.Errorf("user = %v", got)
		}
		// A disabled admin who is enabled again counts, and then the first can step down.
		f.ok("admin", http.MethodPatch, url(f, "root"), map[string]any{"disabled": false}, http.StatusOK)
		f.ok("admin", http.MethodPatch, url(f, "admin"), map[string]any{"role": "member"}, http.StatusOK)
		// root is now the last: it cannot lose the role, and cannot be disabled by itself.
		rec := f.call("root", http.MethodPatch, url(f, "root"), map[string]any{"role": "member"})
		requireStatus(t, rec, http.StatusConflict)
		if code := errorCode(t, rec); code != "conflict" {
			t.Errorf("code = %q", code)
		}
	})
	t.Run("a disabled admin does not count", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		f.ok("root", http.MethodPatch, url(f, "admin"), map[string]any{"disabled": true}, http.StatusOK)
		// admin is disabled; root is the last. Make bob an admin who disables root.
		f.ok("root", http.MethodPatch, url(f, "bob"), map[string]any{"role": "admin"}, http.StatusOK)
		f.ok("bob", http.MethodPatch, url(f, "root"), map[string]any{"disabled": true}, http.StatusOK)
		requireStatus(t, f.call("bob", http.MethodPatch, url(f, "root"), map[string]any{"role": "member"}), http.StatusOK)
		rec := f.call("bob", http.MethodPatch, url(f, "bob"), map[string]any{"role": "member"})
		requireStatus(t, rec, http.StatusConflict)
	})
	t.Run("a disabled admin cannot disable themselves", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		rec := f.call("admin", http.MethodPatch, url(f, "admin"), map[string]any{"disabled": true})
		requireStatus(t, rec, http.StatusUnprocessableEntity)
		if errorFields(t, rec)["disabled"] == nil {
			t.Errorf("fields = %v", errorFields(t, rec))
		}
	})
	mistakes := []struct {
		name   string
		target func(f *forum) string
		body   any
		status int
		field  string
	}{
		{"nothing to change", func(f *forum) string { return url(f, "bob") }, map[string]any{}, http.StatusUnprocessableEntity, "role"},
		{"only nulls", func(f *forum) string { return url(f, "bob") }, map[string]any{"role": nil, "disabled": nil}, http.StatusUnprocessableEntity, "disabled"},
		{"a role that does not exist", func(f *forum) string { return url(f, "bob") }, map[string]any{"role": "owner"}, http.StatusUnprocessableEntity, "role"},
		{"disabled that is not a boolean", func(f *forum) string { return url(f, "bob") }, `{"disabled": "yes"}`, http.StatusUnprocessableEntity, "disabled"},
		{"a field that is not known", func(f *forum) string { return url(f, "bob") }, map[string]any{"username": "x"}, http.StatusUnprocessableEntity, "username"},
		{"an unknown user (role)", func(*forum) string { return path("/admin/users/%d", 99999) }, map[string]any{"role": "member"}, http.StatusNotFound, ""},
		{"an unknown user (disabled)", func(*forum) string { return path("/admin/users/%d", 99999) }, map[string]any{"disabled": true}, http.StatusNotFound, ""},
		{"an ID that is not one", func(*forum) string { return "/api/v1/admin/users/abc" }, map[string]any{"disabled": true}, http.StatusNotFound, ""},
	}
	for _, tt := range mistakes {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			rec := f.call("admin", http.MethodPatch, tt.target(f), tt.body)
			requireStatus(t, rec, tt.status)
			if tt.field != "" && errorFields(t, rec)[tt.field] == nil {
				t.Errorf("fields = %v, want %q", errorFields(t, rec), tt.field)
			}
			if bob, err := f.st.UserByUsername(t.Context(), "bob"); err != nil || bob.Role != "member" || bob.DisabledAt != nil {
				t.Errorf("bob = %+v, %v after a refused change", bob, err)
			}
		})
	}
}

func TestAdminInvitation(t *testing.T) {
	t.Parallel()
	t.Run("the link uses the base URL", func(t *testing.T) {
		t.Parallel()
		f := forumWith(t, func(o *Options) { o.BaseURL = "https://clowder.example.com/chat/" })
		rec := f.call("admin", http.MethodPost, "/api/v1/admin/invitations", map[string]any{"role": "member"})
		requireStatus(t, rec, http.StatusCreated)
		got := decode(t, rec)
		link, _ := got["url"].(string)
		token, ok := strings.CutPrefix(link, "https://clowder.example.com/chat/invite/")
		if !ok || token == "" || strings.ContainsAny(token, "/?# ") {
			t.Fatalf("url = %q", link)
		}
		if got["role"] != "member" || got["expires_at"] != rfc3339(testStart.Add(7*24*time.Hour)) {
			t.Errorf("invitation = %v", got)
		}
		// The link works, once, with the role.
		inv := f.ok("alice", http.MethodGet, "/api/v1/auth/invitations/"+token, nil, http.StatusOK)
		if inv["role"] != "member" {
			t.Errorf("invitation = %v", inv)
		}
		if _, err := f.accounts.SignUp(t.Context(), token, "newcomer", "Newcomer", testPassword); err != nil {
			t.Fatalf("SignUp: %+v", err)
		}
		requireStatus(t, f.call("alice", http.MethodGet, "/api/v1/auth/invitations/"+token, nil), http.StatusNotFound)
	})
	t.Run("an admin invitation", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		got := f.ok("root", http.MethodPost, "/api/v1/admin/invitations", map[string]any{"role": "admin"}, http.StatusCreated)
		if got["role"] != "admin" || !strings.HasPrefix(got["url"].(string), "/invite/") {
			t.Errorf("invitation = %v (no base URL: a path)", got)
		}
		// Two invitations are two links.
		other := f.ok("root", http.MethodPost, "/api/v1/admin/invitations", map[string]any{"role": "admin"}, http.StatusCreated)
		if other["url"] == got["url"] {
			t.Error("two invitations have the same link")
		}
	})
	for _, tt := range []struct {
		name string
		body any
	}{
		{"no role", map[string]any{}},
		{"a role that does not exist", map[string]any{"role": "owner"}},
		{"an empty role", map[string]any{"role": ""}},
		{"a role that is not text", `{"role": 1}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			rec := f.call("admin", http.MethodPost, "/api/v1/admin/invitations", tt.body)
			requireStatus(t, rec, http.StatusUnprocessableEntity)
			if errorFields(t, rec)["role"] == nil {
				t.Errorf("fields = %v", errorFields(t, rec))
			}
		})
	}
}

func TestAdminRooms(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	got := f.ok("root", http.MethodGet, "/api/v1/admin/rooms", nil, http.StatusOK)
	rooms := list(t, got["rooms"])
	if len(rooms) != 2 {
		t.Fatalf("rooms = %v", rooms)
	}
	den, lobby := obj(t, rooms[0]), obj(t, rooms[1])
	if den["name"] != "den" || den["visibility"] != "private" || den["member_count"] != float64(2) || den["is_member"] != false ||
		obj(t, den["created_by"])["username"] != "alice" || den["created_at"] == nil {
		t.Errorf("den = %v", den)
	}
	if lobby["name"] != "lobby" || lobby["visibility"] != "visible" || lobby["member_count"] != float64(2) {
		t.Errorf("lobby = %v", lobby)
	}
	// A private room is not in the list of a member who is outside it.
	for _, r := range list(t, f.ok("carol", http.MethodGet, "/api/v1/rooms", nil, http.StatusOK)["rooms"]) {
		if obj(t, r)["name"] == "den" {
			t.Error("a non-member lists the private room")
		}
	}

	members := f.ok("root", http.MethodGet, path("/admin/rooms/%d/members", f.den.ID), nil, http.StatusOK)
	var names []string
	for _, m := range list(t, members["members"]) {
		names = append(names, obj(t, obj(t, m)["user"])["username"].(string))
	}
	if !reflect.DeepEqual(names, []string{"alice", "bob"}) {
		t.Errorf("members of den = %v", names)
	}
	// The ordinary route still hides it.
	requireStatus(t, f.call("root", http.MethodGet, path("/rooms/%d/members", f.den.ID), nil), http.StatusNotFound)
	requireStatus(t, f.call("root", http.MethodGet, path("/admin/rooms/%d/members", 99999), nil), http.StatusNotFound)
	requireStatus(t, f.call("root", http.MethodGet, "/api/v1/admin/rooms/abc/members", nil), http.StatusNotFound)
}

// auditIDs reads the IDs of the events of a page.
func auditIDs(t *testing.T, page map[string]any) []uint64 {
	t.Helper()
	ids := []uint64{}
	for _, e := range list(t, page["events"]) {
		ids = append(ids, idOf(t, e))
	}
	return ids
}

func TestAdminAudit(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	// Events of two days: a post in lobby, a post and an edit in den.
	f.write("bob", "lobby", "hello")
	f.clk.Advance(24 * time.Hour)
	denPost := f.write("alice", "den", "secret plans")
	if _, err := f.posts.Edit(t.Context(), actorOf(f.testEnv, "alice"), denPost.ID, app.PostInput{Format: app.FormatText, Body: "changed plans"}); err != nil {
		t.Fatal(err)
	}
	audit := func(query string) map[string]any {
		t.Helper()
		return f.ok("root", http.MethodGet, "/api/v1/admin/audit"+query, nil, http.StatusOK)
	}

	all := audit("")
	events := list(t, all["events"])
	// room.created x2, member.added/joined..., post.created x2, post.edited.
	if all["page"] != float64(1) || all["has_more"] != false || len(events) < 5 {
		t.Fatalf("audit = %v", all)
	}
	ids := auditIDs(t, all)
	for i := 1; i < len(ids); i++ {
		if ids[i] >= ids[i-1] {
			t.Fatalf("ids = %v, want newest first", ids)
		}
	}
	first := obj(t, events[0]) // the edit
	if first["action"] != "post.edited" || first["entity_type"] != "post" || first["entity_id"] != float64(denPost.ID) ||
		first["room_id"] != float64(f.den.ID) || obj(t, first["actor"])["username"] != "alice" || first["created_at"] == nil ||
		first["summary"] == "" {
		t.Errorf("newest event = %v", first)
	}
	if _, ok := first["details"].(map[string]any); !ok {
		t.Errorf("details = %v (%T), want an object", first["details"], first["details"])
	}
	if strings.Contains(fmt.Sprint(all), "changed plans") || strings.Contains(fmt.Sprint(all), "secret plans") {
		t.Error("the text of a post is in the audit log")
	}

	count := func(query string) int { return len(list(t, audit(query)["events"])) }
	tests := []struct {
		name, query string
		want        int
	}{
		{"a room", fmt.Sprintf("?room_id=%d", f.den.ID), 0},
		{"an actor", fmt.Sprintf("?actor_id=%d&action=post.created", f.userID("bob")), 1},
		{"an entity type", "?entity_type=post", 3},
		{"an action", "?action=post.edited", 1},
		{"an action in a room", fmt.Sprintf("?action=post.created&room_id=%d", f.lobby.ID), 1},
		{"no match", fmt.Sprintf("?action=post.deleted&room_id=%d", f.lobby.ID), 0},
		{"a room that is gone", "?room_id=99999", 0},
		{"from the second day", "?action=post.created&from=2026-10-27", 1},
		{"from the first day", "?action=post.created&from=2026-10-26", 2},
		{"to the first day, which is included", "?action=post.created&to=2026-10-26", 1},
		{"to the day before", "?action=post.created&to=2026-10-25", 0},
		{"both days", "?action=post.created&from=2026-10-26&to=2026-10-27", 2},
		{"one day", "?action=post.created&from=2026-10-27&to=2026-10-27", 1},
		{"a page past the end", "?page=2", 0},
	}
	inDen, err := f.st.ListAuditEvents(t.Context(), store.AuditFilter{RoomID: f.den.ID}, 1000, 0)
	if err != nil || len(inDen) < 3 {
		t.Fatalf("den has %d events, %v", len(inDen), err)
	}
	tests[0].want = len(inDen)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := count(tt.query); got != tt.want {
				t.Errorf("%s: %d events, want %d", tt.query, got, tt.want)
			}
		})
	}
}

func TestAdminAuditPaging(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	for i := 0; i < 57; i++ {
		f.write("bob", "lobby", fmt.Sprintf("post %d", i))
		f.clk.Advance(time.Second)
	}
	page := func(n int) map[string]any {
		return f.ok("admin", http.MethodGet, fmt.Sprintf("/api/v1/admin/audit?action=post.created&page=%d", n), nil, http.StatusOK)
	}
	one, two, three := page(1), page(2), page(3)
	if len(list(t, one["events"])) != 50 || one["has_more"] != true || one["page"] != float64(1) {
		t.Errorf("page 1: %d events, has_more %v", len(list(t, one["events"])), one["has_more"])
	}
	if len(list(t, two["events"])) != 7 || two["has_more"] != false || two["page"] != float64(2) {
		t.Errorf("page 2: %d events, has_more %v", len(list(t, two["events"])), two["has_more"])
	}
	if len(list(t, three["events"])) != 0 || three["has_more"] != false || three["events"] == nil {
		t.Errorf("page 3 = %v", three)
	}
	seen := map[uint64]bool{}
	for _, id := range append(auditIDs(t, one), auditIDs(t, two)...) {
		if seen[id] {
			t.Fatalf("event %d is on two pages", id)
		}
		seen[id] = true
	}
	// Without a page it is the first.
	if got := f.ok("admin", http.MethodGet, "/api/v1/admin/audit?action=post.created", nil, http.StatusOK); !reflect.DeepEqual(auditIDs(t, got), auditIDs(t, one)) {
		t.Error("the default page is not page 1")
	}
}

func TestAdminAuditMistakes(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	for _, tt := range []struct {
		query string
		field string
	}{
		{"?page=0", "page"}, {"?page=-1", "page"}, {"?page=abc", "page"}, {"?page=", "page"},
		{"?room_id=0", "room_id"}, {"?room_id=abc", "room_id"}, {"?room_id=", "room_id"}, {"?room_id=-3", "room_id"},
		{"?actor_id=0", "actor_id"}, {"?actor_id=x", "actor_id"},
		{"?entity_type=poke", "entity_type"}, {"?entity_type=", "entity_type"}, {"?entity_type=Post", "entity_type"},
		{"?action=post.exploded", "action"}, {"?action=", "action"},
		{"?from=yesterday", "from"}, {"?from=2026-13-01", "from"}, {"?from=2026-10-26T10:00:00Z", "from"}, {"?from=", "from"},
		{"?to=26/10/2026", "to"}, {"?to=2026-1-5", "to"},
	} {
		t.Run(tt.query, func(t *testing.T) {
			rec := f.call("admin", http.MethodGet, "/api/v1/admin/audit"+tt.query, nil)
			requireStatus(t, rec, http.StatusUnprocessableEntity)
			if errorFields(t, rec)[tt.field] == nil {
				t.Errorf("fields = %v, want %q", errorFields(t, rec), tt.field)
			}
		})
	}
	t.Run("every mistake is named", func(t *testing.T) {
		rec := f.call("admin", http.MethodGet, "/api/v1/admin/audit?room_id=x&actor_id=y&action=z&from=a&to=b&entity_type=c", nil)
		requireStatus(t, rec, http.StatusUnprocessableEntity)
		if got := errorFields(t, rec); len(got) != 6 {
			t.Errorf("fields = %v", got)
		}
	})
}

// TestAuditFiltersAreInTheDocument checks that the values the filters accept
// are the ones the document lists.
func TestAuditFiltersAreInTheDocument(t *testing.T) {
	t.Parallel()
	toStrings := func(v any) []string {
		var out []string
		for _, x := range v.([]any) {
			out = append(out, x.(string))
		}
		return out
	}
	var entities, actions []string
	for _, e := range auditEntities {
		entities = append(entities, string(e))
	}
	for _, a := range auditActions {
		actions = append(actions, string(a))
	}
	props := at(document(), "components", "schemas", "AuditEvent", "properties")
	if got := toStrings(at(props, "entity_type", "enum")); !reflect.DeepEqual(got, entities) {
		t.Errorf("entity types in the document = %v, in the code %v", got, entities)
	}
	if got := toStrings(at(props, "action", "enum")); !reflect.DeepEqual(got, actions) {
		t.Errorf("actions in the document = %v, in the code %v", got, actions)
	}
}

// TestAdminRoutesOfTheErrors checks the route of the tag that the audit
// filters make in this package.
func TestAdminRoutesOfTheErrors(t *testing.T) {
	t.Parallel()
	_, err := auditFilter(request(http.MethodGet, "/api/v1/admin/audit?room_id=x&from=y", nil))
	appErr := requireRoute(t, err, tagFilterInvalid, "auditFilter.FilterInvalid")
	fields, _ := appErr.Metadata["fields"].(map[string]string)
	if len(fields) != 2 || fields["room_id"] == "" || fields["from"] == "" {
		t.Errorf("fields = %v", appErr.Metadata)
	}
	if p, internal := problemOf(err); internal || p.status != http.StatusUnprocessableEntity || p.object.Fields["from"] == "" {
		t.Errorf("problemOf = %+v, %v", p, internal)
	}
	filter, err := auditFilter(request(http.MethodGet, "/api/v1/admin/audit?to=2026-10-26&action=post.created", nil))
	if err != nil || !filter.To.Equal(time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)) || filter.Action != app.ActionPostCreated {
		t.Errorf("filter = %+v, %v; want to the start of the next day", filter, err)
	}
}
