package api

import (
	"net/http"
	"testing"
)

// roomOf finds the room by name in the answer of GET /rooms.
func roomEntry(t *testing.T, body map[string]any, name string) map[string]any {
	t.Helper()
	for _, item := range list(t, body["rooms"]) {
		if r := obj(t, item); r["name"] == name {
			return r
		}
	}
	t.Fatalf("no room %q in %v", name, body)
	return nil
}

func TestRoomsListUnreadCounts(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	f.write("admin", "lobby", "one")
	f.write("admin", "lobby", "hello @bob")
	f.write("bob", "lobby", "bob's own")
	f.write("alice", "den", "secret")

	tests := []struct {
		name         string
		user         string
		room         string
		wantPosts    float64
		wantMentions float64
	}{
		{"bob in lobby: the posts of others and the mention", "bob", "lobby", 2, 1},
		{"bob in den", "bob", "den", 1, 0},
		{"admin in lobby: only the post of bob", "admin", "lobby", 1, 0},
		{"alice in den: her own post", "alice", "den", 0, 0},
		{"carol has not joined the lobby", "carol", "lobby", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := roomEntry(t, f.ok(tt.user, http.MethodGet, "/api/v1/rooms", nil, http.StatusOK), tt.room)
			if r["unread_posts"] != tt.wantPosts || r["unread_mentions"] != tt.wantMentions {
				t.Errorf("unread = %v posts, %v mentions; want %v, %v", r["unread_posts"], r["unread_mentions"], tt.wantPosts, tt.wantMentions)
			}
			one := f.ok(tt.user, http.MethodGet, path("/rooms/%d", f.room(tt.room).ID), nil, http.StatusOK)
			if one["unread_posts"] != tt.wantPosts || one["unread_mentions"] != tt.wantMentions {
				t.Errorf("GET /rooms/{id}: unread = %v, %v; want %v, %v", one["unread_posts"], one["unread_mentions"], tt.wantPosts, tt.wantMentions)
			}
		})
	}
}

func TestMarkRoomRead(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		user string
		room string
		// post names the room of the post whose ID is sent: "" for the newest
		// of the room, "den" or "lobby" for the newest of that room, "none"
		// for an ID that does not exist.
		post string
		body any
		want int
		code string
		// field is the field of a 422.
		field string
	}{
		{name: "a member of a visible room", user: "bob", room: "lobby", want: http.StatusNoContent},
		{name: "a member of a private room", user: "alice", room: "den", want: http.StatusNoContent},
		{name: "an admin who is a member", user: "admin", room: "lobby", want: http.StatusNoContent},
		{name: "a visible room that was not joined", user: "carol", room: "lobby", want: http.StatusForbidden, code: "forbidden"},
		{name: "an admin who has not joined", user: "root", room: "lobby", want: http.StatusForbidden, code: "forbidden"},
		{name: "a private room of others", user: "carol", room: "den", want: http.StatusNotFound, code: "not_found"},
		{name: "a private room of others, by an admin", user: "root", room: "den", want: http.StatusNotFound, code: "not_found"},
		{name: "a post of another room", user: "bob", room: "lobby", post: "den", want: http.StatusUnprocessableEntity, code: "invalid_input", field: "post_id"},
		{name: "a post that does not exist", user: "bob", room: "lobby", post: "none", want: http.StatusUnprocessableEntity, code: "invalid_input", field: "post_id"},
		{name: "no post", user: "bob", room: "lobby", body: map[string]any{}, want: http.StatusUnprocessableEntity, code: "invalid_input", field: "post_id"},
		{name: "a body that is not JSON", user: "bob", room: "lobby", body: "nope", want: http.StatusUnprocessableEntity, code: "invalid_input"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			lobby := f.write("admin", "lobby", "in the lobby")
			den := f.write("alice", "den", "in the den")
			id := map[string]uint64{"": f.write("admin", "lobby", "newest").ID, "lobby": lobby.ID, "den": den.ID, "none": 999999}[tt.post]
			if tt.post == "" {
				if tt.room == "den" {
					id = den.ID
				}
			}
			body := tt.body
			if body == nil {
				body = map[string]any{"post_id": id}
			}
			rec := f.call(tt.user, http.MethodPost, path("/rooms/%d/read", f.room(tt.room).ID), body)
			requireStatus(t, rec, tt.want)
			if tt.want == http.StatusNoContent {
				return
			}
			e := obj(t, decode(t, rec)["error"])
			if e["code"] != tt.code {
				t.Errorf("code = %v, want %v", e["code"], tt.code)
			}
			if tt.field != "" {
				if fields := obj(t, e["fields"]); fields[tt.field] == nil {
					t.Errorf("fields = %v, want %s", fields, tt.field)
				}
			}
		})
	}

	t.Run("the mark moves forward only and the counts follow", func(t *testing.T) {
		t.Parallel()
		f := newForum(t)
		p1 := f.write("admin", "lobby", "one")
		p2 := f.write("admin", "lobby", "two")
		c := f.say("admin", p2.ID, "a comment")
		p3 := f.write("admin", "lobby", "three")
		unread := func() float64 {
			return roomEntry(t, f.ok("bob", http.MethodGet, "/api/v1/rooms", nil, http.StatusOK), "lobby")["unread_posts"].(float64)
		}
		read := func(id uint64) {
			f.ok("bob", http.MethodPost, path("/rooms/%d/read", f.lobby.ID), map[string]any{"post_id": id}, http.StatusNoContent)
		}
		if got := unread(); got != 3 {
			t.Fatalf("unread = %v, want 3", got)
		}
		read(p1.ID)
		if got := unread(); got != 2 {
			t.Errorf("after the first post: %v, want 2", got)
		}
		read(c.ID) // a comment covers the posts before it
		if got := unread(); got != 1 {
			t.Errorf("after the comment: %v, want 1 (the third post)", got)
		}
		read(p1.ID) // back: nothing changes
		if got := unread(); got != 1 {
			t.Errorf("after reading an older post again: %v, want 1", got)
		}
		read(p3.ID)
		if got := unread(); got != 0 {
			t.Errorf("after the last post: %v, want 0", got)
		}
	})
}

func TestWSRoomUnread(t *testing.T) {
	t.Parallel()
	w := newWSForum(t)
	c := w.dial("bob")
	own := topicOf(topicUser, w.userID("bob"))

	// A post of the admin in the lobby: bob's counts change.
	w.write("admin", "lobby", "first")
	ev := c.awaitUnread()
	want := map[string]any{"room_id": float64(w.lobby.ID), "unread_posts": float64(1), "unread_mentions": float64(0)}
	sameJSON(t, "room.unread data", ev["data"], want)
	if ev["topic"] != own || ev["kind"] != "room.unread" || ev["room_id"] != float64(w.lobby.ID) {
		t.Errorf("event = %v", ev)
	}

	// A mention: one event with both counts.
	w.write("admin", "lobby", "second, for @bob")
	ev = c.awaitUnread()
	sameJSON(t, "room.unread data", ev["data"], map[string]any{"room_id": float64(w.lobby.ID), "unread_posts": float64(2), "unread_mentions": float64(1)})
	c.await(own, "notification.created")
	c.requireNoUnread()

	// The client marks the room as read: its own connections hear the counts.
	newest := w.write("admin", "lobby", "third")
	c.awaitUnread()
	w.ok("bob", http.MethodPost, path("/rooms/%d/read", w.lobby.ID), map[string]any{"post_id": newest.ID}, http.StatusNoContent)
	ev = c.awaitUnread()
	sameJSON(t, "room.unread data", ev["data"], map[string]any{"room_id": float64(w.lobby.ID), "unread_posts": float64(0), "unread_mentions": float64(1)})

	// Reading the notifications lowers the mention count.
	w.ok("bob", http.MethodPost, "/api/v1/notifications/read", map[string]any{"up_to_id": 1 << 40}, http.StatusNoContent)
	c.await(own, "notification.read")
	ev = c.awaitUnread()
	sameJSON(t, "room.unread data", ev["data"], map[string]any{"room_id": float64(w.lobby.ID), "unread_posts": float64(0), "unread_mentions": float64(0)})

	// A user who is not a member of the room hears nothing.
	other := w.dial("carol")
	w.write("admin", "lobby", "carol is not a member")
	c.awaitUnread()
	other.requireNoUnread()
}
