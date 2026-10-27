package api

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// roomNames reads the names of the rooms of an answer of GET /rooms.
func roomNames(t *testing.T, body map[string]any) []string {
	t.Helper()
	names := []string{}
	for _, r := range list(t, body["rooms"]) {
		names = append(names, obj(t, r)["name"].(string))
	}
	return names
}

func TestListRooms(t *testing.T) {
	t.Parallel()
	tests := []struct {
		user   string
		want   []string
		member map[string]bool
		manage map[string]bool
	}{
		{"admin", []string{"lobby"}, map[string]bool{"lobby": true}, map[string]bool{"lobby": true}},
		{"root", []string{"lobby"}, map[string]bool{"lobby": false}, map[string]bool{"lobby": true}},
		{"alice", []string{"den", "lobby"}, map[string]bool{"den": true, "lobby": false}, map[string]bool{"den": true, "lobby": false}},
		{"bob", []string{"den", "lobby"}, map[string]bool{"den": true, "lobby": true}, map[string]bool{"den": false, "lobby": false}},
		{"carol", []string{"lobby"}, map[string]bool{"lobby": false}, map[string]bool{"lobby": false}},
	}
	f := newForum(t)
	for _, tt := range tests {
		t.Run(tt.user, func(t *testing.T) {
			body := f.ok(tt.user, http.MethodGet, "/api/v1/rooms", nil, http.StatusOK)
			if got := roomNames(t, body); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("rooms = %v, want %v", got, tt.want)
			}
			for _, item := range list(t, body["rooms"]) {
				r := obj(t, item)
				name := r["name"].(string)
				if r["is_member"] != tt.member[name] || r["can_manage"] != tt.manage[name] {
					t.Errorf("%s: is_member %v, can_manage %v, want %v, %v", name, r["is_member"], r["can_manage"], tt.member[name], tt.manage[name])
				}
			}
		})
	}
}

func TestRoomShape(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	body := f.ok("bob", http.MethodGet, path("/rooms/%d", f.den.ID), nil, http.StatusOK)
	want := map[string]any{
		"id": float64(f.den.ID), "name": "den", "description": "", "visibility": "private",
		"created_at": "2026-10-26T09:30:00Z", "member_count": float64(2), "is_member": true, "can_manage": false,
		"created_by": map[string]any{"id": float64(f.userID("alice")), "username": "alice", "display_name": "Alice", "avatar_url": nil},
	}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("room = %v\nwant %v", body, want)
	}
}

func TestCreateRoom(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		user       string
		body       any
		wantStatus int
		wantFields []string
	}{
		{"a private room", "alice", map[string]any{"name": "ideas", "description": " For ideas ", "visibility": "private"}, http.StatusCreated, nil},
		{"a visible room by an admin", "admin", map[string]any{"name": "news", "visibility": "visible"}, http.StatusCreated, nil},
		{"a visible room by a member", "alice", map[string]any{"name": "news", "visibility": "visible"}, http.StatusForbidden, nil},
		{"no name", "alice", map[string]any{"visibility": "private"}, http.StatusUnprocessableEntity, []string{"name"}},
		{"a name that is taken", "alice", map[string]any{"name": "LOBBY", "visibility": "private"}, http.StatusUnprocessableEntity, []string{"name"}},
		{"no visibility", "alice", map[string]any{"name": "ideas"}, http.StatusUnprocessableEntity, []string{"visibility"}},
		{"a wrong visibility", "alice", map[string]any{"name": "ideas", "visibility": "secret"}, http.StatusUnprocessableEntity, []string{"visibility"}},
		{"everything wrong", "alice", map[string]any{"name": " ", "description": strings.Repeat("x", 501), "visibility": "x"}, http.StatusUnprocessableEntity, []string{"description", "name", "visibility"}},
		{"an unknown field", "alice", map[string]any{"name": "ideas", "visibility": "private", "color": "red"}, http.StatusUnprocessableEntity, []string{"color"}},
		{"not an object", "alice", `["ideas"]`, http.StatusUnprocessableEntity, []string{"body"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			before := len(f.audit())
			rec := f.call(tt.user, http.MethodPost, "/api/v1/rooms", tt.body)
			requireStatus(t, rec, tt.wantStatus)
			switch tt.wantStatus {
			case http.StatusCreated:
				room := decode(t, rec)
				if loc := rec.Header().Get("Location"); loc != fmt.Sprintf("/api/v1/rooms/%d", uint64(room["id"].(float64))) {
					t.Errorf("Location = %q", loc)
				}
				if room["member_count"] != float64(1) || room["is_member"] != true || room["can_manage"] != true {
					t.Errorf("room = %v", room)
				}
				if room["description"] != strings.TrimSpace(fmt.Sprint(tt.body.(map[string]any)["description"])) && tt.body.(map[string]any)["description"] != nil {
					t.Errorf("description = %q", room["description"])
				}
				f.requireAudit(before, "room.created room")
			case http.StatusUnprocessableEntity:
				var got []string
				for name := range errorFields(t, rec) {
					got = append(got, name)
				}
				if len(got) != len(tt.wantFields) {
					t.Errorf("fields = %v, want %v", got, tt.wantFields)
				}
				for _, name := range tt.wantFields {
					if _, ok := errorFields(t, rec)[name]; !ok {
						t.Errorf("no problem for %q in %v", name, got)
					}
				}
				f.requireAudit(before)
			default:
				f.requireAudit(before)
			}
		})
	}
	t.Run("the creator is the first member and the room has the default columns", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		created := f.ok("alice", http.MethodPost, "/api/v1/rooms", map[string]any{"name": "ideas", "visibility": "private"}, http.StatusCreated)
		members := f.ok("alice", http.MethodGet, path("/rooms/%d/members", uint64(created["id"].(float64))), nil, http.StatusOK)
		ms := list(t, members["members"])
		if len(ms) != 1 || obj(t, obj(t, ms[0])["user"])["username"] != "alice" || obj(t, ms[0])["added_by"] != nil {
			t.Errorf("members = %v", ms)
		}
	})
	t.Run("a body that is not JSON is 415", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		rec := f.call("alice", http.MethodPost, "/api/v1/rooms", `{"name":"x","visibility":"private"}`, header("Content-Type", "text/plain"))
		requireStatus(t, rec, http.StatusUnsupportedMediaType)
	})
}

// roomStatuses is the table of who gets which status from a route that reads a
// room: a visible room is read by everyone, a private one by its members only,
// admins included (D29).
var roomReadStatuses = []struct {
	user, room string
	want       int
}{
	{"admin", "lobby", http.StatusOK}, {"root", "lobby", http.StatusOK}, {"bob", "lobby", http.StatusOK},
	{"alice", "lobby", http.StatusOK}, {"carol", "lobby", http.StatusOK},
	{"alice", "den", http.StatusOK}, {"bob", "den", http.StatusOK},
	{"admin", "den", http.StatusNotFound}, {"root", "den", http.StatusNotFound}, {"carol", "den", http.StatusNotFound},
}

func TestGetRoomPermissions(t *testing.T) {
	t.Parallel()
	for _, tt := range roomReadStatuses {
		t.Run(tt.user+" on "+tt.room, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			rec := f.call(tt.user, http.MethodGet, path("/rooms/%d", f.room(tt.room).ID), nil)
			requireStatus(t, rec, tt.want)
		})
	}
	f := newForum(t)
	for _, id := range []string{"999999", "0", "abc", "-1", "1.5", "18446744073709551616"} {
		t.Run("the ID "+id, func(t *testing.T) {
			rec := f.call("alice", http.MethodGet, "/api/v1/rooms/"+id, nil)
			requireStatus(t, rec, http.StatusNotFound)
			if code := errorCode(t, rec); code != "not_found" {
				t.Errorf("code = %q", code)
			}
		})
	}
}

func TestPatchRoomPermissions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		user, room string
		want       int
	}{
		{"admin", "lobby", http.StatusOK}, {"root", "lobby", http.StatusOK},
		{"alice", "lobby", http.StatusForbidden}, {"bob", "lobby", http.StatusForbidden}, {"carol", "lobby", http.StatusForbidden},
		{"alice", "den", http.StatusOK}, {"admin", "den", http.StatusOK}, {"root", "den", http.StatusOK},
		{"bob", "den", http.StatusForbidden}, {"carol", "den", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.user+" on "+tt.room, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			before := len(f.audit())
			rec := f.call(tt.user, http.MethodPatch, path("/rooms/%d", f.room(tt.room).ID), map[string]any{"name": "renamed"})
			requireStatus(t, rec, tt.want)
			if tt.want == http.StatusOK {
				if got := decode(t, rec); got["name"] != "renamed" || got["can_manage"] != true {
					t.Errorf("room = %v", got)
				}
				f.requireAudit(before, "room.renamed room")
			} else {
				f.requireAudit(before)
			}
		})
	}
}

func TestPatchRoomKeepsWhatIsLeftOut(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		body     any
		wantName string
		wantDesc string
		wantAud  []string
	}{
		{"nothing", map[string]any{}, "den", "Where we meet", nil},
		{"null fields", map[string]any{"name": nil, "description": nil}, "den", "Where we meet", nil},
		{"only the name", map[string]any{"name": "cave"}, "cave", "Where we meet", []string{"room.renamed room"}},
		{"only the description", map[string]any{"description": "Under the hill"}, "den", "Under the hill", []string{"room.renamed room"}},
		{"an empty description clears it", map[string]any{"description": ""}, "den", "", []string{"room.renamed room"}},
		{"both", map[string]any{"name": "cave", "description": "x"}, "cave", "x", []string{"room.renamed room"}},
		{"the same values", map[string]any{"name": "den", "description": "Where we meet"}, "den", "Where we meet", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			f.ok("alice", http.MethodPatch, path("/rooms/%d", f.den.ID), map[string]any{"description": "Where we meet"}, http.StatusOK)
			before := len(f.audit())
			got := f.ok("alice", http.MethodPatch, path("/rooms/%d", f.den.ID), tt.body, http.StatusOK)
			if got["name"] != tt.wantName || got["description"] != tt.wantDesc {
				t.Errorf("room = %v", got)
			}
			f.requireAudit(before, tt.wantAud...)
			again := f.ok("bob", http.MethodGet, path("/rooms/%d", f.den.ID), nil, http.StatusOK)
			if again["name"] != tt.wantName || again["description"] != tt.wantDesc {
				t.Errorf("the room then = %v", again)
			}
		})
	}
	t.Run("mistakes", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		for name, body := range map[string]map[string]any{
			"name": {"name": "  "}, "description": {"description": strings.Repeat("x", 501)}, "taken": {"name": "Lobby"},
		} {
			rec := f.call("alice", http.MethodPatch, path("/rooms/%d", f.den.ID), body)
			requireStatus(t, rec, http.StatusUnprocessableEntity)
			want := name
			if name == "taken" {
				want = "name"
			}
			if _, ok := errorFields(t, rec)[want]; !ok {
				t.Errorf("%s: fields = %v", name, errorFields(t, rec))
			}
		}
		rec := f.call("alice", http.MethodPatch, path("/rooms/%d", f.den.ID), map[string]any{"visibility": "visible"})
		requireStatus(t, rec, http.StatusUnprocessableEntity)
		if _, ok := errorFields(t, rec)["visibility"]; !ok {
			t.Errorf("fields = %v: the visibility of a room does not change", errorFields(t, rec))
		}
		f.requireAudit(len(f.audit()))
	})
}

func TestDeleteRoom(t *testing.T) {
	t.Parallel()
	tests := []struct {
		user, room string
		want       int
	}{
		{"admin", "lobby", http.StatusNoContent}, {"root", "lobby", http.StatusNoContent},
		{"alice", "lobby", http.StatusForbidden}, {"bob", "lobby", http.StatusForbidden}, {"carol", "lobby", http.StatusForbidden},
		{"alice", "den", http.StatusNoContent}, {"admin", "den", http.StatusNoContent}, {"root", "den", http.StatusNoContent},
		{"bob", "den", http.StatusForbidden}, {"carol", "den", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.user+" on "+tt.room, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			f.write("bob", tt.room, "a post to go")
			before := len(f.audit())
			rec := f.call(tt.user, http.MethodDelete, path("/rooms/%d", f.room(tt.room).ID), nil)
			requireStatus(t, rec, tt.want)
			if tt.want == http.StatusNoContent {
				f.requireAudit(before, "room.deleted room")
				requireStatus(t, f.call("bob", http.MethodGet, path("/rooms/%d", f.room(tt.room).ID), nil), http.StatusNotFound)
			} else {
				f.requireAudit(before)
			}
		})
	}
}

func TestJoinAndLeaveRoom(t *testing.T) {
	t.Parallel()
	t.Run("join a visible room, twice", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		before := len(f.audit())
		requireStatus(t, f.call("carol", http.MethodPost, path("/rooms/%d/join", f.lobby.ID), nil), http.StatusNoContent)
		requireStatus(t, f.call("carol", http.MethodPost, path("/rooms/%d/join", f.lobby.ID), nil), http.StatusNoContent)
		f.requireAudit(before, "member.joined member")
		room := f.ok("carol", http.MethodGet, path("/rooms/%d", f.lobby.ID), nil, http.StatusOK)
		if room["is_member"] != true || room["member_count"] != float64(3) {
			t.Errorf("room = %v", room)
		}
	})
	t.Run("join a private room", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		before := len(f.audit())
		for _, user := range []string{"carol", "admin", "root"} {
			requireStatus(t, f.call(user, http.MethodPost, path("/rooms/%d/join", f.den.ID), nil), http.StatusNotFound)
		}
		f.requireAudit(before)
	})
	t.Run("leave a room, twice", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		before := len(f.audit())
		requireStatus(t, f.call("bob", http.MethodPost, path("/rooms/%d/leave", f.lobby.ID), nil), http.StatusNoContent)
		requireStatus(t, f.call("bob", http.MethodPost, path("/rooms/%d/leave", f.lobby.ID), nil), http.StatusNoContent)
		f.requireAudit(before, "member.left member")
		if room := f.ok("bob", http.MethodGet, path("/rooms/%d", f.lobby.ID), nil, http.StatusOK); room["is_member"] != false {
			t.Errorf("room = %v", room)
		}
	})
	t.Run("leave a room the user is not in", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		before := len(f.audit())
		requireStatus(t, f.call("carol", http.MethodPost, path("/rooms/%d/leave", f.lobby.ID), nil), http.StatusNoContent)
		requireStatus(t, f.call("carol", http.MethodPost, path("/rooms/%d/leave", f.den.ID), nil), http.StatusNotFound)
		f.requireAudit(before)
	})
	t.Run("a room that left is unknown again", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		requireStatus(t, f.call("bob", http.MethodPost, path("/rooms/%d/leave", f.den.ID), nil), http.StatusNoContent)
		requireStatus(t, f.call("bob", http.MethodGet, path("/rooms/%d", f.den.ID), nil), http.StatusNotFound)
	})
}

func TestRoomMembers(t *testing.T) {
	t.Parallel()
	for _, tt := range roomReadStatuses {
		t.Run(tt.user+" on "+tt.room, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			rec := f.call(tt.user, http.MethodGet, path("/rooms/%d/members", f.room(tt.room).ID), nil)
			requireStatus(t, rec, tt.want)
		})
	}
	t.Run("the shape", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		members := list(t, f.ok("carol", http.MethodGet, path("/rooms/%d/members", f.lobby.ID), nil, http.StatusOK)["members"])
		want := []any{
			map[string]any{"user": map[string]any{"id": float64(f.userID("admin")), "username": "admin", "display_name": "Admin", "avatar_url": nil}, "added_by": nil, "joined_at": "2026-10-26T09:30:00Z"},
			map[string]any{"user": map[string]any{"id": float64(f.userID("bob")), "username": "bob", "display_name": "Bob", "avatar_url": nil}, "added_by": nil, "joined_at": "2026-10-26T09:30:00Z"},
		}
		if !reflect.DeepEqual(members, want) {
			t.Errorf("members = %v\nwant %v", members, want)
		}
		den := list(t, f.ok("alice", http.MethodGet, path("/rooms/%d/members", f.den.ID), nil, http.StatusOK)["members"])
		bob := obj(t, den[1])
		if by := obj(t, bob["added_by"]); by["username"] != "alice" || by["display_name"] != "Alice" || by["id"] != float64(f.userID("alice")) {
			t.Errorf("added_by = %v", by)
		}
	})
}

func TestAddRoomMember(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		user string
		room string
		body any
		want int
		// field is the problem of a 422.
		field string
	}{
		{"a member of a private room", "alice", "den", map[string]any{"user_id": "carol"}, http.StatusNoContent, ""},
		{"a member of a visible room", "bob", "lobby", map[string]any{"user_id": "carol"}, http.StatusNoContent, ""},
		{"a user who is a member already", "alice", "den", map[string]any{"user_id": "bob"}, http.StatusNoContent, ""},
		{"a visible room the user has not joined", "carol", "lobby", map[string]any{"user_id": "alice"}, http.StatusForbidden, ""},
		{"an admin who is not in the room", "root", "lobby", map[string]any{"user_id": "carol"}, http.StatusForbidden, ""},
		{"a private room the user is not in", "carol", "den", map[string]any{"user_id": "carol"}, http.StatusNotFound, ""},
		{"a private room an admin is not in", "admin", "den", map[string]any{"user_id": "carol"}, http.StatusNotFound, ""},
		{"a user who does not exist", "alice", "den", map[string]any{"user_id": 999999}, http.StatusUnprocessableEntity, "user_id"},
		{"no user", "alice", "den", map[string]any{}, http.StatusUnprocessableEntity, "user_id"},
		{"a user ID that is not a number", "alice", "den", `{"user_id":"x"}`, http.StatusUnprocessableEntity, "user_id"},
		{"an unknown field", "alice", "den", `{"user":3}`, http.StatusUnprocessableEntity, "user"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			body := tt.body
			if m, ok := body.(map[string]any); ok {
				if name, isName := m["user_id"].(string); isName {
					body = map[string]any{"user_id": f.userID(name)}
				}
			}
			before := len(f.audit())
			rec := f.call(tt.user, http.MethodPost, path("/rooms/%d/members", f.room(tt.room).ID), body)
			requireStatus(t, rec, tt.want)
			if tt.field != "" {
				if _, ok := errorFields(t, rec)[tt.field]; !ok {
					t.Errorf("fields = %v, want %q", errorFields(t, rec), tt.field)
				}
			}
			if tt.want == http.StatusNoContent && tt.name != "a user who is a member already" {
				f.requireAudit(before, "member.added member")
				members := list(t, f.ok("alice", http.MethodGet, path("/rooms/%d/members", f.room(tt.room).ID), nil, http.StatusOK)["members"])
				if len(members) != 3 {
					t.Errorf("%d members after the add", len(members))
				}
			} else {
				f.requireAudit(before)
			}
		})
	}
	t.Run("a disabled user", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		if err := f.accounts.SetDisabled(t.Context(), actorOf(f.testEnv, "admin"), f.userID("carol"), true); err != nil {
			t.Fatal(err)
		}
		rec := f.call("alice", http.MethodPost, path("/rooms/%d/members", f.den.ID), map[string]any{"user_id": f.userID("carol")})
		requireStatus(t, rec, http.StatusUnprocessableEntity)
		if _, ok := errorFields(t, rec)["user_id"]; !ok {
			t.Errorf("fields = %v", errorFields(t, rec))
		}
	})
}

func TestRemoveRoomMember(t *testing.T) {
	t.Parallel()
	tests := []struct {
		user, room string
		want       int
	}{
		{"admin", "lobby", http.StatusNoContent}, {"root", "lobby", http.StatusNoContent},
		{"bob", "lobby", http.StatusForbidden}, {"carol", "lobby", http.StatusForbidden},
		{"admin", "den", http.StatusNoContent}, {"root", "den", http.StatusNoContent},
		{"alice", "den", http.StatusForbidden}, {"bob", "den", http.StatusForbidden}, {"carol", "den", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.user+" on "+tt.room, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			before := len(f.audit())
			rec := f.call(tt.user, http.MethodDelete, path("/rooms/%d/members/%d", f.room(tt.room).ID, f.userID("bob")), nil)
			requireStatus(t, rec, tt.want)
			if tt.want == http.StatusNoContent {
				f.requireAudit(before, "member.removed member")
				// Removing again, or a user who is no member, is not an error.
				requireStatus(t, f.call(tt.user, http.MethodDelete, path("/rooms/%d/members/%d", f.room(tt.room).ID, f.userID("bob")), nil), http.StatusNoContent)
				f.requireAudit(before, "member.removed member")
			} else {
				f.requireAudit(before)
			}
		})
	}
	t.Run("a user ID that is not a number", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		requireStatus(t, f.call("admin", http.MethodDelete, path("/rooms/%d/members/x", f.lobby.ID), nil), http.StatusNotFound)
	})
}

func TestRoomCandidates(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		user, room string
		want       int
	}{
		{"admin", "lobby", http.StatusOK}, {"bob", "lobby", http.StatusOK},
		{"root", "lobby", http.StatusForbidden}, {"alice", "lobby", http.StatusForbidden}, {"carol", "lobby", http.StatusForbidden},
		{"alice", "den", http.StatusOK}, {"bob", "den", http.StatusOK},
		{"admin", "den", http.StatusNotFound}, {"root", "den", http.StatusNotFound}, {"carol", "den", http.StatusNotFound},
	} {
		t.Run(tt.user+" on "+tt.room, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			requireStatus(t, f.call(tt.user, http.MethodGet, path("/rooms/%d/candidates", f.room(tt.room).ID), nil), tt.want)
		})
	}
	f := newForum(t)
	tests := []struct {
		q    string
		want []string
	}{
		{"", []string{"admin", "carol", "root"}},
		{"?q=ca", []string{"carol"}},
		{"?q=CA", []string{"carol"}},
		{"?q=Ro", []string{"root"}},
		{"?q=zz", []string{}},
		{"?q=al", []string{}},
	}
	for _, tt := range tests {
		t.Run("q"+tt.q, func(t *testing.T) {
			body := f.ok("alice", http.MethodGet, path("/rooms/%d/candidates", f.den.ID)+tt.q, nil, http.StatusOK)
			got := []string{}
			for _, u := range list(t, body["users"]) {
				m := obj(t, u)
				got = append(got, m["username"].(string))
				if m["role"] == nil || m["created_at"] == nil {
					t.Errorf("user = %v", m)
				}
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("candidates = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestAuditThroughTheAPIForRooms is the check, for every audited action of a
// room, that the call through the API writes its event and a call that fails
// writes none. The events of each action are in the tests above as well.
func TestAuditThroughTheAPIForRooms(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	var trail []string
	step := func(user, method, p string, body any, want int, events ...string) {
		t.Helper()
		before := len(f.audit())
		requireStatus(t, f.call(user, method, p, body), want)
		f.requireAudit(before, events...)
		trail = append(trail, events...)
	}
	created := f.ok("alice", http.MethodPost, "/api/v1/rooms", map[string]any{"name": "ideas", "visibility": "private"}, http.StatusCreated)
	id := uint64(created["id"].(float64))
	step("alice", http.MethodPost, path("/rooms/%d/members", id), map[string]any{"user_id": f.userID("carol")}, http.StatusNoContent, "member.added member")
	step("carol", http.MethodPost, path("/rooms/%d/leave", id), nil, http.StatusNoContent, "member.left member")
	step("admin", http.MethodDelete, path("/rooms/%d/members/%d", id, f.userID("alice")), nil, http.StatusNoContent, "member.removed member")
	step("admin", http.MethodPatch, path("/rooms/%d", id), map[string]any{"name": "thoughts"}, http.StatusOK, "room.renamed room")
	step("carol", http.MethodDelete, path("/rooms/%d", id), nil, http.StatusNotFound)
	step("admin", http.MethodDelete, path("/rooms/%d", id), nil, http.StatusNoContent, "room.deleted room")
	step("carol", http.MethodPost, path("/rooms/%d/join", f.lobby.ID), nil, http.StatusNoContent, "member.joined member")
	if len(trail) != 6 {
		t.Errorf("trail = %v", trail)
	}

}
