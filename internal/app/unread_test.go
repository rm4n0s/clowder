package app_test

import (
	"reflect"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/live"
)

// unreadOf returns the counts of the user in the room as the sidebar reads
// them (Mine) and fails the test when the room is not there.
func (e *prefsEnv) unreadOf(user, room string) app.Room {
	e.t.Helper()
	rooms, err := e.rooms.Mine(e.t.Context(), e.as(user))
	if err != nil {
		e.t.Fatalf("Mine(%s): %+v", user, err)
	}
	for _, r := range rooms {
		if r.ID == e.room(room).ID {
			return r
		}
	}
	e.t.Fatalf("%s has no room %s in Mine", user, room)
	return app.Room{}
}

// say writes a text post and returns it.
func (e *prefsEnv) say(user, room, body string) app.Post {
	e.t.Helper()
	p, err := e.posts.Create(e.t.Context(), e.as(user), e.room(room).ID, app.PostInput{Format: app.FormatText, Body: body})
	if err != nil {
		e.t.Fatalf("Create %q: %+v", body, err)
	}
	return p
}

// visit has the user open the room and fails the test on an error.
func (e *prefsEnv) visit(user, room string) uint64 {
	e.t.Helper()
	before, err := e.rooms.Visit(e.t.Context(), e.as(user), e.room(room).ID)
	if err != nil {
		e.t.Fatalf("Visit(%s): %+v", user, err)
	}
	return before
}

// TestUnreadCountsTable is the table of the counts of SPEC §7.17, read as the
// sidebar reads them, for admin in the lobby (members: admin, bob, carol).
func TestUnreadCountsTable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		run          func(e *prefsEnv)
		wantPosts    int
		wantMentions int
	}{
		{"nothing is new", func(e *prefsEnv) {}, 0, 0},
		{"posts of others", func(e *prefsEnv) {
			e.say("bob", "lobby", "one")
			e.say("carol", "lobby", "two")
		}, 2, 0},
		{"own posts are not counted", func(e *prefsEnv) {
			e.say("admin", "lobby", "mine")
			e.say("bob", "lobby", "bob's")
		}, 1, 0},
		{"comments are not counted", func(e *prefsEnv) {
			p := e.say("admin", "lobby", "mine")
			for range 2 {
				if _, err := e.posts.Reply(e.t.Context(), e.as("bob"), p.ID, app.PostInput{Format: app.FormatText, Body: "re"}); err != nil {
					e.t.Fatalf("Reply: %+v", err)
				}
			}
		}, 0, 0},
		{"a deleted post is not counted", func(e *prefsEnv) {
			p := e.say("bob", "lobby", "gone")
			e.say("bob", "lobby", "stays")
			if err := e.posts.Delete(e.t.Context(), e.as("bob"), p.ID); err != nil {
				e.t.Fatalf("Delete: %+v", err)
			}
		}, 1, 0},
		{"a deleted placeholder is not counted", func(e *prefsEnv) {
			p := e.say("bob", "lobby", "gone")
			if _, err := e.posts.Reply(e.t.Context(), e.as("carol"), p.ID, app.PostInput{Format: app.FormatText, Body: "re"}); err != nil {
				e.t.Fatalf("Reply: %+v", err)
			}
			if err := e.posts.Delete(e.t.Context(), e.as("bob"), p.ID); err != nil {
				e.t.Fatalf("Delete: %+v", err)
			}
		}, 0, 0},
		{"a post before the mark and one after", func(e *prefsEnv) {
			e.say("bob", "lobby", "old")
			e.visit("admin", "lobby")
			e.say("bob", "lobby", "new")
		}, 1, 0},
		{"a room left and joined again", func(e *prefsEnv) {
			e.say("bob", "lobby", "while a member")
			if err := e.rooms.Leave(e.t.Context(), e.as("admin"), e.lobby.ID); err != nil {
				e.t.Fatalf("Leave: %+v", err)
			}
			e.say("bob", "lobby", "while away")
			if err := e.rooms.Join(e.t.Context(), e.as("admin"), e.lobby.ID); err != nil {
				e.t.Fatalf("Join: %+v", err)
			}
		}, 0, 0},
		{"a mention", func(e *prefsEnv) {
			e.say("bob", "lobby", "hello @admin")
		}, 1, 1},
		{"a group mention", func(e *prefsEnv) {
			e.say("bob", "lobby", "hello @room")
		}, 1, 1},
		{"a keyword", func(e *prefsEnv) {
			kw := []string{"deploy"}
			e.settings("admin", app.SettingsUpdate{Keywords: &kw})
			e.say("bob", "lobby", "the deploy is done")
		}, 1, 1},
		{"a group mention that is ignored in the room", func(e *prefsEnv) {
			ignore := true
			e.set("admin", app.RoomRef(e.lobby.ID), app.PlaceUpdate{IgnoreGroupMentions: &ignore})
			e.say("bob", "lobby", "hello @room")
		}, 1, 0},
		{"mentions cleared by opening the feed", func(e *prefsEnv) {
			e.say("bob", "lobby", "hello @admin")
			e.say("bob", "lobby", "and @room")
			e.visit("admin", "lobby")
		}, 0, 0},
		{"mentions read on the notifications page", func(e *prefsEnv) {
			e.say("bob", "lobby", "hello @admin")
			if err := e.notes.MarkRead(e.t.Context(), e.as("admin"), 1<<40); err != nil {
				e.t.Fatalf("MarkRead: %+v", err)
			}
		}, 1, 0},
		{"a mention in another room is not counted here", func(e *prefsEnv) {
			e.say("alice", "den", "psst @bob")
		}, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newPrefsEnv(t)
			tt.run(e)
			got := e.unreadOf("admin", "lobby")
			if got.UnreadPosts != tt.wantPosts || got.UnreadMentions != tt.wantMentions {
				t.Errorf("unread = %d posts, %d mentions; want %d, %d", got.UnreadPosts, got.UnreadMentions, tt.wantPosts, tt.wantMentions)
			}
		})
	}
}

// TestUnreadPrivateAndPreferences checks the counts of a private room, that
// a non-member has none, and the flags the sidebar needs.
func TestUnreadPrivateAndPreferences(t *testing.T) {
	t.Parallel()
	e := newPrefsEnv(t)
	e.say("alice", "den", "secret")
	e.say("bob", "lobby", "hello")

	if got := e.unreadOf("bob", "den"); got.UnreadPosts != 1 || got.Muted || got.MentionsOnly {
		t.Errorf("bob in den = %+v, want 1 unread post and no flags", got)
	}
	if got := e.unreadOf("alice", "den"); got.UnreadPosts != 0 {
		t.Errorf("alice read her own post: %d unread", got.UnreadPosts)
	}
	// Carol is not a member of den: it is not in her rooms, and Unread says so.
	if _, ok, err := e.rooms.Unread(t.Context(), e.as("carol"), e.den.ID); err != nil || ok {
		t.Errorf("Unread of a non-member = found %v, err %v; want not found", ok, err)
	}
	// GET /rooms: only the rooms of the user have counts.
	list, err := e.rooms.Available(t.Context(), e.as("carol"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range list {
		want := 0
		if r.ID == e.lobby.ID {
			want = 1
		}
		if r.UnreadPosts != want {
			t.Errorf("Available: %s has %d unread posts for carol, want %d", r.Name, r.UnreadPosts, want)
		}
	}
	room, err := e.rooms.Get(t.Context(), e.as("carol"), e.lobby.ID)
	if err != nil || room.UnreadPosts != 1 {
		t.Errorf("Get = %d unread, %v; want 1", room.UnreadPosts, err)
	}
	room, err = e.rooms.Get(t.Context(), e.as("dave"), e.lobby.ID)
	if err != nil || room.UnreadPosts != 0 || room.IsMember {
		t.Errorf("Get as a non-member = %+v, %v; want 0 unread", room, err)
	}

	// Added by someone else to a room that has posts: nothing is unread.
	if err := e.rooms.AddMember(t.Context(), e.as("alice"), e.den.ID, e.uid("admin")); err != nil {
		t.Fatalf("AddMember: %+v", err)
	}
	if got := e.unreadOf("admin", "den"); got.UnreadPosts != 0 {
		t.Errorf("admin added to den has %d unread posts, want 0", got.UnreadPosts)
	}

	muted, only := true, app.MarkUnreadMentions
	e.set("carol", app.RoomRef(e.lobby.ID), app.PlaceUpdate{Muted: &muted})
	e.set("bob", app.RoomRef(e.den.ID), app.PlaceUpdate{MarkUnread: &only})
	if got := e.unreadOf("carol", "lobby"); !got.Muted || got.UnreadPosts != 1 {
		t.Errorf("carol in lobby = %+v, want muted with the raw count", got)
	}
	if got := e.unreadOf("bob", "den"); !got.MentionsOnly || got.Muted {
		t.Errorf("bob in den = %+v, want only mentions", got)
	}

	one, ok, err := e.rooms.MineOne(t.Context(), e.as("carol"), e.lobby.ID)
	if err != nil || !ok || !one.Muted || one.UnreadPosts != 1 || one.Name != "lobby" {
		t.Errorf("MineOne = %+v, %v, %v", one, ok, err)
	}
	if _, ok, err := e.rooms.MineOne(t.Context(), e.as("carol"), e.den.ID); err != nil || ok {
		t.Errorf("MineOne of a room the user is not in: found %v, err %v", ok, err)
	}
}

// TestVisit checks what opening a room does: the mark moves to the newest ID,
// the previous mark comes back, mentions are read, and the events.
func TestVisit(t *testing.T) {
	t.Parallel()
	e := newPrefsEnv(t)
	e.say("bob", "lobby", "first")
	second := e.say("bob", "lobby", "second @admin")
	e.rec.Reset()

	// The lobby had no post when admin made it: the mark from then is 0.
	if before := e.visit("admin", "lobby"); before != 0 {
		t.Errorf("Visit returned %d, want 0, the mark from the join", before)
	}
	got := e.rec.Events()
	want := []live.Event{
		{Topic: live.UserTopic(e.uid("admin")), Kind: live.KindRoomUnread, RoomID: e.lobby.ID, UserID: e.uid("admin")},
		{Topic: live.UserTopic(e.uid("admin")), Kind: live.KindNotificationRead, UserID: e.uid("admin")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("published %+v, want %+v", got, want)
	}
	if n, err := e.notes.UnreadCount(t.Context(), e.as("admin")); err != nil || n != 0 {
		t.Errorf("unread notifications after Visit = %d, %v; want 0", n, err)
	}

	// The second visit returns the mark the first one set, and publishes nothing.
	e.rec.Reset()
	if before := e.visit("admin", "lobby"); before != second.ID {
		t.Errorf("second Visit returned %d, want %d", before, second.ID)
	}
	if got := e.rec.Events(); len(got) != 0 {
		t.Errorf("a Visit that changed nothing published %+v", got)
	}

	// A visit by a non-member of a visible room changes nothing.
	if before := e.visit("dave", "lobby"); before != 0 {
		t.Errorf("Visit as a non-member returned %d, want 0", before)
	}
	if got := e.rec.Events(); len(got) != 0 {
		t.Errorf("Visit as a non-member published %+v", got)
	}
	// A private room of others is not found.
	_, err := e.rooms.Visit(t.Context(), e.as("dave"), e.den.ID)
	_ = requireTag(t, err, app.TagNotFound, "Rooms.Visit.func1->loadReadableRoom.NotFound")
}

// TestMarkRead checks the mark of a client: who may move it, that it moves
// forward only, that the ID must be of the room, and the events.
func TestMarkRead(t *testing.T) {
	t.Parallel()
	const (
		route = "Rooms.MarkRead.func1."
	)
	tests := []struct {
		name  string
		actor string
		room  string
		// post is the room of the post whose ID is sent; "" for an ID that
		// does not exist.
		post string
		want string
	}{
		{"admin who is a member", "admin", "lobby", "lobby", ""},
		{"a room member", "bob", "lobby", "lobby", ""},
		{"a member of the private room", "alice", "den", "den", ""},
		{"an admin who is not a member of a visible room", "root", "lobby", "lobby", app.TagForbidden},
		{"a non-member of a visible room", "dave", "lobby", "lobby", app.TagForbidden},
		{"a disabled user who is not a member", "dina", "lobby", "lobby", app.TagForbidden},
		{"a non-member of a private room", "dave", "den", "den", app.TagNotFound},
		{"an admin who is not a member of a private room", "root", "den", "den", app.TagNotFound},
		{"a post of another room", "bob", "lobby", "den", app.TagInvalidInput},
		{"a post that does not exist", "bob", "lobby", "", app.TagInvalidInput},
	}
	routes := map[string]string{
		app.TagForbidden:    route + "Forbidden",
		app.TagNotFound:     "Rooms.MarkRead.func1->loadReadableRoom.NotFound",
		app.TagInvalidInput: route + "InvalidInput",
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newPrefsEnv(t)
			lobbyPost := e.say("bob", "lobby", "in the lobby")
			denPost := e.say("alice", "den", "in the den")
			id := uint64(99999)
			switch tt.post {
			case "lobby":
				id = lobbyPost.ID
			case "den":
				id = denPost.ID
			}
			before := e.rec.Events()
			err := e.rooms.MarkRead(t.Context(), e.as(tt.actor), e.room(tt.room).ID, id)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("got %+v, want success", err)
				}
				return
			}
			appErr := requireTag(t, err, tt.want, routes[tt.want])
			if tt.want == app.TagInvalidInput {
				if fields, _ := appErr.Metadata["fields"].(map[string]string); fields[app.FieldPostID] == "" {
					t.Errorf("fields = %v, want a problem with %s", appErr.Metadata["fields"], app.FieldPostID)
				}
			}
			if after := e.rec.Events(); len(after) != len(before) {
				t.Errorf("a failed call published %d events", len(after)-len(before))
			}
		})
	}

	t.Run("moves forward only and publishes once", func(t *testing.T) {
		t.Parallel()
		e := newPrefsEnv(t)
		p1 := e.say("bob", "lobby", "one")
		p2 := e.say("bob", "lobby", "two")
		p3 := e.say("bob", "lobby", "three")
		e.rec.Reset()
		step := func(id uint64, wantUnread, wantEvents int) {
			t.Helper()
			e.rec.Reset()
			if err := e.rooms.MarkRead(t.Context(), e.as("admin"), e.lobby.ID, id); err != nil {
				t.Fatalf("MarkRead(%d): %+v", id, err)
			}
			if got := e.unreadOf("admin", "lobby").UnreadPosts; got != wantUnread {
				t.Errorf("after MarkRead(%d): %d unread, want %d", id, got, wantUnread)
			}
			if got := len(e.userEvents("admin", live.KindRoomUnread)); got != wantEvents {
				t.Errorf("after MarkRead(%d): %d room.unread events, want %d", id, got, wantEvents)
			}
		}
		step(p2.ID, 1, 1)
		step(p1.ID, 1, 0) // back: nothing changes
		step(p2.ID, 1, 0) // the same place
		step(p3.ID, 0, 1)
	})

	t.Run("a comment of the room marks everything up to it", func(t *testing.T) {
		t.Parallel()
		e := newPrefsEnv(t)
		p1 := e.say("bob", "lobby", "one")
		c, err := e.posts.Reply(t.Context(), e.as("carol"), p1.ID, app.PostInput{Format: app.FormatText, Body: "re"})
		if err != nil {
			t.Fatal(err)
		}
		e.say("bob", "lobby", "two")
		if err := e.rooms.MarkRead(t.Context(), e.as("admin"), e.lobby.ID, c.ID); err != nil {
			t.Fatalf("MarkRead: %+v", err)
		}
		if got := e.unreadOf("admin", "lobby").UnreadPosts; got != 1 {
			t.Errorf("%d unread after marking up to the comment, want the 1 after it", got)
		}
	})
}

// TestUnreadEvents checks who is told when the counts change.
func TestUnreadEvents(t *testing.T) {
	t.Parallel()
	unreadFor := func(e *prefsEnv) map[uint64]int {
		out := map[uint64]int{}
		for _, ev := range e.rec.Events() {
			if ev.Kind == live.KindRoomUnread {
				if ev.Topic != live.UserTopic(ev.UserID) || ev.RoomID == 0 {
					e.t.Errorf("malformed event %+v", ev)
				}
				out[ev.UserID]++
			}
		}
		return out
	}
	t.Run("a post tells the other members once", func(t *testing.T) {
		t.Parallel()
		e := newPrefsEnv(t)
		e.say("bob", "lobby", "hello")
		want := map[uint64]int{e.uid("admin"): 1, e.uid("carol"): 1}
		if got := unreadFor(e); !reflect.DeepEqual(got, want) {
			t.Errorf("room.unread for %v, want %v", got, want)
		}
	})
	t.Run("a mention is one event, not two", func(t *testing.T) {
		t.Parallel()
		e := newPrefsEnv(t)
		e.say("bob", "lobby", "hello @admin")
		want := map[uint64]int{e.uid("admin"): 1, e.uid("carol"): 1}
		if got := unreadFor(e); !reflect.DeepEqual(got, want) {
			t.Errorf("room.unread for %v, want %v", got, want)
		}
	})
	t.Run("a muted member and a member who counts only mentions are not told of a post", func(t *testing.T) {
		t.Parallel()
		e := newPrefsEnv(t)
		muted, only := true, app.MarkUnreadMentions
		e.set("admin", app.RoomRef(e.lobby.ID), app.PlaceUpdate{Muted: &muted})
		e.set("carol", app.RoomRef(e.lobby.ID), app.PlaceUpdate{MarkUnread: &only})
		e.rec.Reset()
		e.say("bob", "lobby", "hello")
		if got := unreadFor(e); len(got) != 0 {
			t.Errorf("room.unread for %v, want nobody", got)
		}
	})
	t.Run("a mention tells a muted member", func(t *testing.T) {
		t.Parallel()
		e := newPrefsEnv(t)
		muted := true
		e.set("admin", app.RoomRef(e.lobby.ID), app.PlaceUpdate{Muted: &muted})
		e.rec.Reset()
		e.say("bob", "lobby", "hello @admin")
		if got := unreadFor(e)[e.uid("admin")]; got != 1 {
			t.Errorf("room.unread for admin = %d, want 1", got)
		}
	})
	t.Run("deleting a post tells the other members", func(t *testing.T) {
		t.Parallel()
		e := newPrefsEnv(t)
		p := e.say("bob", "lobby", "hello")
		e.rec.Reset()
		if err := e.posts.Delete(t.Context(), e.as("bob"), p.ID); err != nil {
			t.Fatal(err)
		}
		want := map[uint64]int{e.uid("admin"): 1, e.uid("carol"): 1}
		if got := unreadFor(e); !reflect.DeepEqual(got, want) {
			t.Errorf("room.unread for %v, want %v", got, want)
		}
	})
	t.Run("deleting a comment tells nobody", func(t *testing.T) {
		t.Parallel()
		e := newPrefsEnv(t)
		p := e.say("bob", "lobby", "hello")
		c, err := e.posts.Reply(t.Context(), e.as("carol"), p.ID, app.PostInput{Format: app.FormatText, Body: "re"})
		if err != nil {
			t.Fatal(err)
		}
		e.rec.Reset()
		if err := e.posts.Delete(t.Context(), e.as("carol"), c.ID); err != nil {
			t.Fatal(err)
		}
		if got := unreadFor(e); len(got) != 0 {
			t.Errorf("room.unread for %v, want nobody", got)
		}
	})
	t.Run("reading the notifications tells the user of the rooms with mentions", func(t *testing.T) {
		t.Parallel()
		e := newPrefsEnv(t)
		e.say("bob", "lobby", "hello @admin")
		e.rec.Reset()
		if err := e.notes.MarkRead(t.Context(), e.as("admin"), 1<<40); err != nil {
			t.Fatal(err)
		}
		if got := unreadFor(e); !reflect.DeepEqual(got, map[uint64]int{e.uid("admin"): 1}) {
			t.Errorf("room.unread for %v, want admin once", got)
		}
		// Nothing left to read: no event.
		e.rec.Reset()
		if err := e.notes.MarkRead(t.Context(), e.as("admin"), 1<<40); err != nil {
			t.Fatal(err)
		}
		if got := unreadFor(e); len(got) != 0 {
			t.Errorf("a second MarkRead published room.unread for %v", got)
		}
	})
}

// TestEveryMemberOfTheServicesHasAMark checks the invariant of SPEC §6.3
// through the services: after creating a room, joining, being added, leaving
// and joining again, every membership has a mark, and a member who joins is
// not told that earlier posts are new.
func TestEveryMemberOfTheServicesHasAMark(t *testing.T) {
	t.Parallel()
	e := newPrefsEnv(t)
	room, err := e.rooms.Create(t.Context(), e.as("carol"), app.RoomInput{Name: "fresh", Visibility: app.VisibilityPrivate})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := e.rooms.Unread(t.Context(), e.as("carol"), room.ID); err != nil || !ok {
		t.Fatalf("the creator has no mark: found %v, err %v", ok, err)
	}
	if _, err := e.posts.Create(t.Context(), e.as("carol"), room.ID, app.PostInput{Format: app.FormatText, Body: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := e.rooms.AddMember(t.Context(), e.as("carol"), room.ID, e.uid("dave")); err != nil {
		t.Fatal(err)
	}
	got, ok, err := e.rooms.Unread(t.Context(), e.as("dave"), room.ID)
	if err != nil || !ok || got.Posts != 0 {
		t.Errorf("an added member has %+v (found %v, err %v), want nothing unread", got, ok, err)
	}
	if err := e.rooms.Leave(t.Context(), e.as("dave"), room.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := e.rooms.Unread(t.Context(), e.as("dave"), room.ID); err != nil || ok {
		t.Errorf("a member who left still has counts: found %v, err %v", ok, err)
	}
	if err := e.rooms.RemoveMember(t.Context(), e.as("admin"), room.ID, e.uid("carol")); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := e.rooms.Unread(t.Context(), e.as("carol"), room.ID); err != nil || ok {
		t.Errorf("a removed member still has counts: found %v, err %v", ok, err)
	}
}
