package store

import (
	"testing"
)

func TestUserSettingsNotificationFields(t *testing.T) {
	f := newFix(t)
	u := f.user()

	got, err := f.s.UserSettingsOf(f.ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Notify != NotifyMentions || got.SoundName != SoundPurr || got.Replies != RepliesFollowing {
		t.Errorf("defaults = %+v, want mentions, purr, following", got)
	}
	tests := []struct {
		name string
		in   UserSettings
	}{
		{"all, chirp, direct", UserSettings{UserID: u.ID, Theme: ThemeSystem, Sound: true, Notify: NotifyAll, SoundName: SoundChirp, Replies: RepliesDirect}},
		{"nothing, bell, no sound", UserSettings{UserID: u.ID, Theme: ThemeDark, Notify: NotifyNothing, SoundName: SoundBell, Replies: RepliesFollowing}},
		{"meow, browser", UserSettings{UserID: u.ID, Theme: ThemeLight, Sound: true, BrowserNotifications: true, Notify: NotifyMentions, SoundName: SoundMeow, Replies: RepliesFollowing}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := tt.in
			if err := f.s.SaveUserSettings(f.ctx, &in); err != nil {
				t.Fatalf("SaveUserSettings: %+v", err)
			}
			got, err := f.s.UserSettingsOf(f.ctx, u.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Notify != tt.in.Notify || got.SoundName != tt.in.SoundName || got.Replies != tt.in.Replies ||
				got.Sound != tt.in.Sound || got.BrowserNotifications != tt.in.BrowserNotifications || got.Theme != tt.in.Theme {
				t.Errorf("settings = %+v, want %+v", got, tt.in)
			}
		})
	}
	bad := []struct {
		name string
		in   UserSettings
	}{
		{"unknown notify", UserSettings{UserID: u.ID, Theme: ThemeSystem, Notify: "some", SoundName: SoundPurr, Replies: RepliesFollowing}},
		{"unknown sound", UserSettings{UserID: u.ID, Theme: ThemeSystem, Notify: NotifyAll, SoundName: "moo", Replies: RepliesFollowing}},
		{"unknown replies", UserSettings{UserID: u.ID, Theme: ThemeSystem, Notify: NotifyAll, SoundName: SoundPurr, Replies: "some"}},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			in := tt.in
			requireTagged(t, f.s.SaveUserSettings(f.ctx, &in), TagQueryFailed, "Store.SaveUserSettings.QueryFailed", "saveUserSettings")
		})
	}
}

func TestPlacePrefs(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()
	room := f.room(alice)
	conv := f.groupWith(alice, bob)

	// Defaults, and no row made by a read.
	for _, place := range []PlaceKey{RoomPlace(room.ID), ConversationPlace(conv.ID)} {
		got, found, err := f.s.PlacePrefOf(f.ctx, alice.ID, place)
		if err != nil || found {
			t.Fatalf("PlacePrefOf = found %v, %v; want the defaults and no row", found, err)
		}
		if got.Notify != PlaceNotifyDefault || got.Sound != PlaceSoundDefault || got.MarkUnread != MarkUnreadAll || got.Muted || got.IgnoreGroupMentions {
			t.Errorf("defaults = %+v", got)
		}
	}
	if n := f.count("place_prefs"); n != 0 {
		t.Fatalf("reads made %d rows", n)
	}

	// Save, replace, read.
	for _, place := range []PlaceKey{RoomPlace(room.ID), ConversationPlace(conv.ID)} {
		p := DefaultPlacePref(alice.ID, place)
		p.Notify, p.Sound, p.Muted, p.IgnoreGroupMentions, p.MarkUnread = PlaceNotifyAll, PlaceSoundOff, true, true, MarkUnreadMentions
		if err := f.s.SavePlacePref(f.ctx, &p); err != nil {
			t.Fatalf("SavePlacePref: %+v", err)
		}
		q := DefaultPlacePref(alice.ID, place)
		q.Notify = PlaceNotifyNothing
		if err := f.s.SavePlacePref(f.ctx, &q); err != nil {
			t.Fatalf("SavePlacePref again: %+v", err)
		}
		got, found, err := f.s.PlacePrefOf(f.ctx, alice.ID, place)
		if err != nil || !found {
			t.Fatalf("PlacePrefOf = found %v, %v", found, err)
		}
		if got.Notify != PlaceNotifyNothing || got.Sound != PlaceSoundDefault || got.Muted || got.MarkUnread != MarkUnreadAll {
			t.Errorf("after the replacement = %+v, want only notify changed", got)
		}
	}
	if n := f.count("place_prefs"); n != 2 {
		t.Errorf("%d rows, want one per user and place", n)
	}

	// Another user's rows are separate.
	other := DefaultPlacePref(bob.ID, RoomPlace(room.ID))
	other.Muted = true
	if err := f.s.SavePlacePref(f.ctx, &other); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := f.s.PlacePrefOf(f.ctx, alice.ID, RoomPlace(room.ID)); got.Muted {
		t.Error("bob's mute reached alice")
	}
	rows, err := f.s.PlacePrefsOfUser(f.ctx, alice.ID)
	if err != nil || len(rows) != 2 || rows[0].RoomID == nil || rows[1].ConversationID == nil {
		t.Fatalf("PlacePrefsOfUser = %+v, %v; want the room then the conversation", rows, err)
	}

	// Delete.
	if err := f.s.DeletePlacePref(f.ctx, alice.ID, RoomPlace(room.ID)); err != nil {
		t.Fatal(err)
	}
	if err := f.s.DeletePlacePref(f.ctx, alice.ID, RoomPlace(room.ID)); err != nil {
		t.Fatalf("deleting nothing is not an error: %+v", err)
	}
	if _, found, _ := f.s.PlacePrefOf(f.ctx, alice.ID, RoomPlace(room.ID)); found {
		t.Error("the row is still there")
	}
	if _, found, _ := f.s.PlacePrefOf(f.ctx, alice.ID, ConversationPlace(conv.ID)); !found {
		t.Error("the conversation's row went with the room's")
	}

	// The rows go with the room.
	if err := f.s.DeleteRoom(f.ctx, room.ID); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := f.s.PlacePrefOf(f.ctx, bob.ID, RoomPlace(room.ID)); found {
		t.Error("a row outlived its room")
	}
}

func TestPlacePrefConstraints(t *testing.T) {
	f := newFix(t)
	alice := f.user()
	room := f.room(alice)
	conv := f.groupWith(alice)

	tests := []struct {
		name string
		mut  func(p *PlacePref)
	}{
		{"both places", func(p *PlacePref) { p.ConversationID = &conv.ID }},
		{"no place", func(p *PlacePref) { p.RoomID = nil }},
		{"unknown notify", func(p *PlacePref) { p.Notify = "some" }},
		{"unknown sound", func(p *PlacePref) { p.Sound = "loud" }},
		{"unknown mark unread", func(p *PlacePref) { p.MarkUnread = "none" }},
		{"unknown room", func(p *PlacePref) { p.RoomID = ptr(uint64(9999)) }},
		{"unknown user", func(p *PlacePref) { p.UserID = 9999 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := DefaultPlacePref(alice.ID, RoomPlace(room.ID))
			tt.mut(&p)
			requireTagged(t, f.s.SavePlacePref(f.ctx, &p), TagQueryFailed, "Store.SavePlacePref.QueryFailed", "savePlacePref")
		})
	}
	canceledTests := []struct {
		name string
		call func() error
		want string
		op   string
	}{
		{"canceled read", func() error {
			_, _, err := f.s.PlacePrefOf(canceled(t), alice.ID, RoomPlace(room.ID))
			return err
		}, "Store.PlacePrefOf.QueryFailed", "placePrefOf"},
		{"canceled delete", func() error { return f.s.DeletePlacePref(canceled(t), alice.ID, RoomPlace(room.ID)) },
			"Store.DeletePlacePref.QueryFailed", "deletePlacePref"},
		{"canceled list", func() error {
			_, err := f.s.PlacePrefsOfUser(canceled(t), alice.ID)
			return err
		}, "Store.PlacePrefsOfUser.QueryFailed", "placePrefsOfUser"},
		{"canceled room recipients", func() error {
			_, err := f.s.RoomMemberRecipients(canceled(t), room.ID, 0)
			return err
		}, "Store.RoomMemberRecipients->Store.recipients.QueryFailed", "roomMemberRecipients"},
		{"canceled conversation recipients", func() error {
			_, err := f.s.ConversationRecipients(canceled(t), conv.ID, 0)
			return err
		}, "Store.ConversationRecipients->Store.recipients.QueryFailed", "conversationRecipients"},
		{"canceled recipients of", func() error {
			_, err := f.s.RecipientsOf(canceled(t), RoomPlace(room.ID), []uint64{alice.ID})
			return err
		}, "Store.RecipientsOf->Store.recipients.QueryFailed", "recipientsOf"},
	}
	for _, tt := range canceledTests {
		t.Run(tt.name, func(t *testing.T) {
			requireTagged(t, tt.call(), TagQueryFailed, tt.want, tt.op)
		})
	}
}

func TestRecipientsAreLoadedInOneQuery(t *testing.T) {
	f := newFix(t)
	author := f.user()
	room := f.room(author)
	f.create(&RoomMember{RoomID: room.ID, UserID: author.ID})
	var members []*User
	for range 12 {
		u := f.user()
		f.create(&RoomMember{RoomID: room.ID, UserID: u.ID})
		members = append(members, u)
	}
	// One member has settings and a muted room, one is disabled, one set the
	// room to All.
	s := DefaultUserSettings(members[0].ID)
	s.Notify, s.SoundName, s.BrowserNotifications, s.Sound = NotifyAll, SoundBell, true, false
	if err := f.s.SaveUserSettings(f.ctx, &s); err != nil {
		t.Fatal(err)
	}
	muted := DefaultPlacePref(members[0].ID, RoomPlace(room.ID))
	muted.Muted = true
	if err := f.s.SavePlacePref(f.ctx, &muted); err != nil {
		t.Fatal(err)
	}
	loud := DefaultPlacePref(members[1].ID, RoomPlace(room.ID))
	loud.Notify, loud.Sound = PlaceNotifyAll, PlaceSoundOn
	if err := f.s.SavePlacePref(f.ctx, &loud); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetUserDisabled(f.ctx, members[2].ID, true); err != nil {
		t.Fatal(err)
	}

	n := countQueries(f)
	got, err := f.s.RoomMemberRecipients(f.ctx, room.ID, author.ID)
	if err != nil {
		t.Fatalf("RoomMemberRecipients: %+v", err)
	}
	if n.Load() != 1 {
		t.Errorf("%d queries for %d members, want 1", n.Load(), len(got))
	}
	if len(got) != 11 {
		t.Fatalf("%d recipients, want the 12 members but the disabled one, without the author", len(got))
	}
	first, second, third := got[0], got[1], got[2]
	if first.UserID != members[0].ID || first.Settings.Notify != NotifyAll || first.Settings.SoundName != SoundBell ||
		!first.Settings.BrowserNotifications || first.Settings.Sound || !first.Place.Muted {
		t.Errorf("first = %+v", first)
	}
	if second.UserID != members[1].ID || second.Place.Notify != PlaceNotifyAll || second.Place.Sound != PlaceSoundOn ||
		second.Settings.Notify != NotifyMentions || !second.Settings.Sound {
		t.Errorf("second = %+v, want its own place choices and the default settings", second)
	}
	if third.UserID != members[3].ID || third.Place.Notify != PlaceNotifyDefault {
		t.Errorf("third = %+v, want the disabled member skipped", third)
	}

	// The same for a conversation and for a list of users.
	conv := f.groupWith(author, members[0], members[1])
	n.Store(0)
	rs, err := f.s.ConversationRecipients(f.ctx, conv.ID, author.ID)
	if err != nil || len(rs) != 2 || n.Load() != 1 {
		t.Fatalf("ConversationRecipients = %d, %v, %d queries; want 2 and 1 query", len(rs), err, n.Load())
	}
	n.Store(0)
	rs, err = f.s.RecipientsOf(f.ctx, RoomPlace(room.ID), []uint64{members[0].ID, members[1].ID, members[2].ID, 9999})
	if err != nil || len(rs) != 2 || n.Load() != 1 {
		t.Fatalf("RecipientsOf = %d, %v, %d queries; want 2 (no disabled, no unknown) and 1 query", len(rs), err, n.Load())
	}
	if rs, err := f.s.RecipientsOf(f.ctx, RoomPlace(room.ID), nil); err != nil || len(rs) != 0 {
		t.Errorf("RecipientsOf(nobody) = %v, %v", rs, err)
	}
}

func TestMutedConversationLeavesTheUnreadCount(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()
	conv := f.groupWith(alice, bob)
	f.message(conv, bob)

	count := func() int {
		t.Helper()
		n, err := f.s.CountUnreadConversations(f.ctx, alice.ID)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(); n != 1 {
		t.Fatalf("unread conversations = %d, want 1", n)
	}
	p := DefaultPlacePref(alice.ID, ConversationPlace(conv.ID))
	p.Muted = true
	if err := f.s.SavePlacePref(f.ctx, &p); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 0 {
		t.Errorf("unread conversations = %d once muted, want 0", n)
	}
	items, err := f.s.ListConversations(f.ctx, alice.ID)
	if err != nil || len(items) != 1 || !items[0].Muted || !items[0].Unread {
		t.Errorf("list = %+v, %v; want one muted conversation that still has an unread message", items, err)
	}
	p.Muted = false
	if err := f.s.SavePlacePref(f.ctx, &p); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Errorf("unread conversations = %d once unmuted, want 1", n)
	}
	// Bob's mute does not touch Alice's count.
	b := DefaultPlacePref(bob.ID, ConversationPlace(conv.ID))
	b.Muted = true
	if err := f.s.SavePlacePref(f.ctx, &b); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Errorf("unread conversations = %d after bob muted, want 1", n)
	}
}
