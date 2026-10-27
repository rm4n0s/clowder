package app_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/store"
)

// namesOf returns the names of the rooms.
func namesOf(rooms []app.Room) []string {
	names := []string{}
	for _, r := range rooms {
		names = append(names, r.Name)
	}
	return names
}

// TestRoomsAvailable is the table of who sees which rooms in the list: every
// visible room, and the private rooms the actor is a member of. No actor,
// admins included, sees a private room they are not in (D29).
func TestRoomsAvailable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		actor      string
		want       []string
		wantMember map[string]bool
	}{
		{"admin", []string{"lobby"}, map[string]bool{"lobby": true}},
		{"root", []string{"lobby"}, map[string]bool{"lobby": false}},
		{"alice", []string{"den", "lobby"}, map[string]bool{"den": true, "lobby": false}},
		{"bob", []string{"den", "lobby"}, map[string]bool{"den": true, "lobby": true}},
		{"carol", []string{"lobby"}, map[string]bool{"lobby": false}},
	}
	for _, tt := range tests {
		t.Run(tt.actor, func(t *testing.T) {
			t.Parallel()
			e := newRoomsEnv(t)
			got, err := e.rooms.Available(t.Context(), e.as(tt.actor))
			if err != nil {
				t.Fatalf("Available: %+v", err)
			}
			if !reflect.DeepEqual(namesOf(got), tt.want) {
				t.Fatalf("rooms = %v, want %v", namesOf(got), tt.want)
			}
			for _, r := range got {
				if r.IsMember != tt.wantMember[r.Name] {
					t.Errorf("%s: IsMember = %v, want %v", r.Name, r.IsMember, tt.wantMember[r.Name])
				}
				if r.Creator.ID != r.CreatedBy || r.Creator.Username == "" || r.Creator.DisplayName == "" {
					t.Errorf("%s: creator = %+v for user %d", r.Name, r.Creator, r.CreatedBy)
				}
			}
		})
	}
}

// TestRoomsAvailableIsOrderedByNameWithoutDuplicates checks that a visible
// room the actor has joined comes once, and the list is ordered by name
// without regard to case.
func TestRoomsAvailableIsOrderedByNameWithoutDuplicates(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	for _, name := range []string{"Zeta", "alpha", "Mid"} {
		if _, err := e.rooms.Create(t.Context(), e.as("bob"), app.RoomInput{Name: name, Visibility: app.VisibilityPrivate}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := e.rooms.Available(t.Context(), e.as("bob"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"alpha", "den", "lobby", "Mid", "Zeta"}; !reflect.DeepEqual(namesOf(got), want) {
		t.Errorf("rooms = %v, want %v", namesOf(got), want)
	}
}

// TestRoomsPatchPermissions is the permission table of Patch: the rules of
// Update.
func TestRoomsPatchPermissions(t *testing.T) {
	t.Parallel()
	routes := map[string]string{
		app.TagNotFound:  "Rooms.Patch->loadManageableRoom->" + notFoundPrivate,
		app.TagForbidden: "Rooms.Patch->loadManageableRoom.Forbidden",
	}
	cases := []permCase{
		{"admin", "lobby", ""}, {"root", "lobby", ""},
		{"alice", "lobby", app.TagForbidden}, {"bob", "lobby", app.TagForbidden}, {"carol", "lobby", app.TagForbidden},
		{"admin", "den", ""}, {"root", "den", ""}, {"alice", "den", ""},
		{"bob", "den", app.TagForbidden}, {"carol", "den", app.TagNotFound},
	}
	name := "renamed"
	runPermissions(t, func(e *roomsEnv, a app.Actor, r app.Room) error {
		_, err := e.rooms.Patch(t.Context(), a, r.ID, app.RoomPatch{Name: &name})
		return err
	}, routes, cases)
}

// TestRoomsPatch checks which fields change, what comes back and the audit
// event.
func TestRoomsPatch(t *testing.T) {
	t.Parallel()
	str := func(s string) *string { return &s }
	tests := []struct {
		name     string
		patch    app.RoomPatch
		wantName string
		wantDesc string
		wantEvt  bool
	}{
		{"nothing", app.RoomPatch{}, "den", "Where we meet", false},
		{"the name", app.RoomPatch{Name: str("cave")}, "cave", "Where we meet", true},
		{"the description", app.RoomPatch{Description: str("Under the hill")}, "den", "Under the hill", true},
		{"both", app.RoomPatch{Name: str("cave"), Description: str("")}, "cave", "", true},
		{"the same values", app.RoomPatch{Name: str("den"), Description: str("Where we meet")}, "den", "Where we meet", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newRoomsEnv(t)
			if err := e.rooms.Update(t.Context(), e.as("alice"), e.den.ID, app.RoomInput{Name: "den", Description: "Where we meet"}); err != nil {
				t.Fatal(err)
			}
			before := len(e.events())
			got, err := e.rooms.Patch(t.Context(), e.as("alice"), e.den.ID, tt.patch)
			if err != nil {
				t.Fatalf("Patch: %+v", err)
			}
			if got.Name != tt.wantName || got.Description != tt.wantDesc || !got.IsMember || !got.CanManage || got.MemberCount != 2 {
				t.Errorf("room = %+v", got)
			}
			if tt.wantEvt {
				if n := len(e.events()) - before; n != 1 {
					t.Errorf("%d new audit events, want 1", n)
				}
			} else if n := len(e.events()) - before; n != 0 {
				t.Errorf("%d new audit events, want none", n)
			}
		})
	}
}

// TestRoomsPatchByAnAdminOutsideAPrivateRoom checks that an admin who is not a
// member renames a private room (D29) and gets the room back, not a member.
func TestRoomsPatchByAnAdminOutsideAPrivateRoom(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	name := "cave"
	got, err := e.rooms.Patch(t.Context(), e.as("root"), e.den.ID, app.RoomPatch{Name: &name})
	if err != nil {
		t.Fatalf("Patch: %+v", err)
	}
	if got.Name != "cave" || got.IsMember || !got.CanManage {
		t.Errorf("room = %+v", got)
	}
}

// TestRoomsPatchFailures checks the errors that come from the rules, with
// their routes.
func TestRoomsPatchFailures(t *testing.T) {
	t.Parallel()
	str := func(s string) *string { return &s }
	t.Run("an empty name", func(t *testing.T) {
		t.Parallel()
		e := newRoomsEnv(t)
		before := len(e.events())
		_, err := e.rooms.Patch(t.Context(), e.as("alice"), e.den.ID, app.RoomPatch{Name: str("  ")})
		appErr := requireTag(t, err, app.TagInvalidInput, "Rooms.Patch->Rooms.Update->Store.Tx->DB.Transaction->Store.Tx.func1->Rooms.Update.func1.InvalidInput")
		if fieldsOf(t, appErr)[app.FieldName] == "" {
			t.Errorf("fields = %v", appErr.Metadata)
		}
		e.requireNewEvents(before)
	})
	t.Run("a name that is taken", func(t *testing.T) {
		t.Parallel()
		e := newRoomsEnv(t)
		_, err := e.rooms.Patch(t.Context(), e.as("alice"), e.den.ID, app.RoomPatch{Name: str("LOBBY")})
		appErr := requireTag(t, err, app.TagInvalidInput, "Rooms.Patch->Rooms.Update->Store.Tx->DB.Transaction->Store.Tx.func1->Rooms.Update.func1.InvalidInput")
		if fieldsOf(t, appErr)[app.FieldName] != "This name is taken." {
			t.Errorf("fields = %v", appErr.Metadata)
		}
	})
	t.Run("a missing room", func(t *testing.T) {
		t.Parallel()
		e := newRoomsEnv(t)
		_, err := e.rooms.Patch(t.Context(), e.as("admin"), 9999, app.RoomPatch{Name: str("x")})
		_ = requireTag(t, err, app.TagNotFound, "Rooms.Patch->loadManageableRoom->Store.RoomByID.NotFound")
	})
}

// TestRoomsSearchCandidatesPermissions is the permission table of
// SearchCandidates: the rules of Candidates.
func TestRoomsSearchCandidatesPermissions(t *testing.T) {
	t.Parallel()
	routes := map[string]string{
		app.TagNotFound:  "Rooms.SearchCandidates->Rooms.Candidates->" + notFoundPrivate,
		app.TagForbidden: "Rooms.SearchCandidates->Rooms.Candidates.Forbidden",
	}
	cases := []permCase{
		{"admin", "lobby", ""}, {"bob", "lobby", ""},
		{"root", "lobby", app.TagForbidden}, {"alice", "lobby", app.TagForbidden}, {"carol", "lobby", app.TagForbidden},
		{"alice", "den", ""}, {"bob", "den", ""},
		{"admin", "den", app.TagNotFound}, {"root", "den", app.TagNotFound}, {"carol", "den", app.TagNotFound},
	}
	runPermissions(t, func(e *roomsEnv, a app.Actor, r app.Room) error {
		_, err := e.rooms.SearchCandidates(t.Context(), a, r.ID, "")
		return err
	}, routes, cases)
}

// TestRoomsSearchCandidates checks the prefix match on both names and the
// limit of 20.
func TestRoomsSearchCandidates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		prefix string
		want   []string
	}{
		{"no prefix", "", []string{"admin", "carol", "dave", "root"}},
		{"blank prefix", "   ", []string{"admin", "carol", "dave", "root"}},
		{"a username prefix", "ca", []string{"carol"}},
		{"in any case", "CA", []string{"carol"}},
		{"a display name prefix", "Da", []string{"dave"}},
		{"no match", "zz", []string{}},
		{"a member is not a candidate", "al", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newRoomsEnv(t)
			users, err := e.rooms.SearchCandidates(t.Context(), e.as("alice"), e.den.ID, tt.prefix)
			if err != nil {
				t.Fatal(err)
			}
			got := []string{}
			for _, u := range users {
				got = append(got, u.Username)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("candidates = %v, want %v", got, tt.want)
			}
		})
	}
	t.Run("at most 20", func(t *testing.T) {
		t.Parallel()
		e := newRoomsEnv(t)
		for i := 0; i < 25; i++ {
			e.signUp(fmt.Sprintf("extra%02d", i), store.RoleMember)
		}
		users, err := e.rooms.SearchCandidates(t.Context(), e.as("alice"), e.den.ID, "extra")
		if err != nil || len(users) != 20 || users[0].Username != "extra00" {
			t.Errorf("candidates = %d, %v, first %v", len(users), err, users)
		}
	})
}

// TestSetReactionPermissions is the permission table of SetReaction, for a
// post and for a comment, putting and taking off: the rules of ToggleReaction.
func TestSetReactionPermissions(t *testing.T) {
	t.Parallel()
	routes := map[string]string{
		app.TagNotFound:  "Posts.SetReaction.func1->" + notFoundPrivate,
		app.TagForbidden: "Posts.SetReaction.func1.Forbidden",
	}
	cases := []postCase{
		{"bob", "lobby", ""}, {"admin", "lobby", ""},
		{"alice", "lobby", app.TagForbidden}, {"carol", "lobby", app.TagForbidden}, {"root", "lobby", app.TagForbidden},
		{"bob", "den", ""}, {"alice", "den", ""},
		{"admin", "den", app.TagNotFound}, {"root", "den", app.TagNotFound}, {"carol", "den", app.TagNotFound},
	}
	for _, present := range []bool{true, false} {
		t.Run(fmt.Sprintf("present=%v", present), func(t *testing.T) {
			runPostPermissions(t, routes, cases, func(e *postsEnv, a app.Actor, p app.Post) error {
				_, err := e.posts.SetReaction(t.Context(), a, p.ID, emojiParty, present)
				return err
			})
			runCommentPermissions(t, routes, cases, func(e *postsEnv, a app.Actor, _, c app.Post) error {
				_, err := e.posts.SetReaction(t.Context(), a, c.ID, emojiParty, present)
				return err
			})
		})
	}
}

// TestSetReactionIsIdempotent checks that putting twice leaves one reaction,
// taking off what is not there is not an error, and only a change is
// published.
func TestSetReactionIsIdempotent(t *testing.T) {
	t.Parallel()
	e := newLiveEnv(t)
	p := e.write("bob", "lobby", "react to me")
	set := func(user string, emoji string, present bool) app.Reactions {
		t.Helper()
		got, err := e.posts.SetReaction(t.Context(), e.as(user), p.ID, emoji, present)
		if err != nil {
			t.Fatalf("SetReaction(%s, %q, %v): %+v", user, emoji, present, err)
		}
		return got
	}
	e.rec.Reset()
	if got := set("bob", emojiParty, false); len(got) != 0 {
		t.Errorf("taking off what is not there: %+v", got)
	}
	if n := len(e.rec.Events()); n != 0 {
		t.Errorf("%d events for a no-op", n)
	}
	got := set("bob", emojiParty, true)
	want := []app.ReactionCount{{Emoji: emojiParty, Count: 1, Mine: true, Names: []string{"Bob"}, Users: []app.UserRef{e.ref("bob")}}}
	if !reflect.DeepEqual([]app.ReactionCount(got), want) {
		t.Errorf("after the put: %+v", got)
	}
	if n := len(e.rec.Events()); n != 2 {
		t.Errorf("%d events for the put, want 2", n)
	}
	e.rec.Reset()
	if again := set("bob", emojiParty, true); !reflect.DeepEqual(again, got) {
		t.Errorf("a second put: %+v", again)
	}
	if n := len(e.rec.Events()); n != 0 {
		t.Errorf("%d events for a second put", n)
	}
	// Trimmed, like the toggle.
	if again := set("bob", " "+emojiParty+" ", true); len(again) != 1 || again[0].Count != 1 {
		t.Errorf("a trimmed put: %+v", again)
	}
	set("admin", emojiParty, true)
	if got := set("bob", emojiParty, false); len(got) != 1 || got[0].Count != 1 || got[0].Mine {
		t.Errorf("after bob took it off: %+v", got)
	}
	if got := set("bob", emojiParty, false); len(got) != 1 {
		t.Errorf("a second removal changed something: %+v", got)
	}
}

// TestSetReactionFailures checks the failures that are not about permission,
// with their routes, and that nothing is audited.
func TestSetReactionFailures(t *testing.T) {
	t.Parallel()
	t.Run("a missing post", func(t *testing.T) {
		t.Parallel()
		e := newPostsEnv(t)
		_, err := e.posts.SetReaction(t.Context(), e.as("bob"), 9999, emojiParty, true)
		_ = requireTag(t, err, app.TagNotFound, "Posts.SetReaction.func1->Store.PostByID.NotFound")
	})
	t.Run("a deleted post", func(t *testing.T) {
		t.Parallel()
		e := newPostsEnv(t)
		p := e.write("bob", "lobby", "gone soon")
		e.say("admin", p.ID, "reply")
		if err := e.posts.Delete(t.Context(), e.as("bob"), p.ID); err != nil {
			t.Fatal(err)
		}
		_, err := e.posts.SetReaction(t.Context(), e.as("bob"), p.ID, emojiParty, true)
		appErr := requireTag(t, err, app.TagNotFound, "Posts.SetReaction.func1.NotFound")
		if appErr.Metadata["reason"] != "deleted" {
			t.Errorf("metadata = %v", appErr.Metadata)
		}
	})
	t.Run("not an emoji", func(t *testing.T) {
		t.Parallel()
		e := newPostsEnv(t)
		p := e.write("bob", "lobby", "react")
		for _, present := range []bool{true, false} {
			for _, in := range []string{"", "a", "1", "<script>", strings.Repeat(emojiParty, 9)} {
				_, err := e.posts.SetReaction(t.Context(), e.as("bob"), p.ID, in, present)
				appErr := requireTag(t, err, app.TagInvalidInput, "Posts.SetReaction.func1.InvalidInput")
				if fieldsOf(t, appErr)[app.FieldEmoji] == "" {
					t.Errorf("%q: no message for %s: %v", in, app.FieldEmoji, appErr.Metadata)
				}
			}
		}
	})
	t.Run("writes no audit event", func(t *testing.T) {
		t.Parallel()
		e := newPostsEnv(t)
		p := e.write("bob", "lobby", "react")
		before := len(e.events())
		for _, present := range []bool{true, true, false, false} {
			if _, err := e.posts.SetReaction(t.Context(), e.as("bob"), p.ID, emojiParty, present); err != nil {
				t.Fatal(err)
			}
		}
		if n := len(e.events()); n != before {
			t.Errorf("reactions wrote %d audit events", n-before)
		}
	})
}
