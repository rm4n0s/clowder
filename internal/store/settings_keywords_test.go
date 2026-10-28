package store

import (
	"reflect"
	"testing"
)

// noWords reports whether the keywords are none: nil or empty.
func noWords(words []string) bool { return len(words) == 0 }

// sameWords compares two keyword lists, nil and empty being the same.
func sameWords(got, want []string) bool {
	return (noWords(got) && noWords(want)) || reflect.DeepEqual(got, want)
}

func TestUserSettingsKeywordsAccessors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		set      func(*UserSettings)
		wantJSON string
		wantList []string
	}{
		{"in order", func(u *UserSettings) { u.SetKeywords([]string{"deploy", "feeder schedule", "Μάνος"}) },
			`["deploy","feeder schedule","Μάνος"]`, []string{"deploy", "feeder schedule", "Μάνος"}},
		{"a quote and a backslash", func(u *UserSettings) { u.SetKeywords([]string{`say "hi"`, `a\b`}) },
			`["say \"hi\"","a\\b"]`, []string{`say "hi"`, `a\b`}},
		{"an empty list", func(u *UserSettings) { u.SetKeywords([]string{}) }, "[]", nil},
		{"a nil list", func(u *UserSettings) { u.SetKeywords(nil) }, "[]", nil},
		{"emptied after keywords", func(u *UserSettings) {
			u.SetKeywords([]string{"a"})
			u.SetKeywords(nil)
		}, "[]", nil},
		{"damaged", func(u *UserSettings) { u.KeywordsJSON = "not json" }, "not json", nil},
		{"not a list of strings", func(u *UserSettings) { u.KeywordsJSON = `{"a":1}` }, `{"a":1}`, nil},
		{"never set", func(u *UserSettings) {}, "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var u UserSettings
			tt.set(&u)
			if u.KeywordsJSON != tt.wantJSON {
				t.Errorf("KeywordsJSON = %q, want %q", u.KeywordsJSON, tt.wantJSON)
			}
			if got := u.Keywords(); !sameWords(got, tt.wantList) {
				t.Errorf("Keywords = %#v, want %#v", got, tt.wantList)
			}
		})
	}

	t.Run("group mentions are stored inverted", func(t *testing.T) {
		t.Parallel()
		var u UserSettings
		if !u.GroupMentions() {
			t.Error("a zero value has the group mentions off")
		}
		u.SetGroupMentions(false)
		if u.GroupMentions() || !u.GroupMentionsOff {
			t.Errorf("after off: %+v", u)
		}
		u.SetGroupMentions(true)
		if !u.GroupMentions() || u.GroupMentionsOff {
			t.Errorf("after on: %+v", u)
		}
	})
}

func TestUserSettingsKeywordsAndGroupMentions(t *testing.T) {
	t.Parallel()
	f := newFix(t)
	u := f.user()

	t.Run("the defaults", func(t *testing.T) {
		d := DefaultUserSettings(u.ID)
		if d.KeywordsJSON != "[]" || !noWords(d.Keywords()) || !d.GroupMentions() {
			t.Errorf("default settings = %+v, want no keywords and group mentions on", d)
		}
		got, err := f.s.UserSettingsOf(f.ctx, u.ID)
		if err != nil {
			t.Fatalf("UserSettingsOf: %+v", err)
		}
		if got.KeywordsJSON != "[]" || !noWords(got.Keywords()) || !got.GroupMentions() {
			t.Errorf("settings of a user with none = %+v", got)
		}
		if n := f.count("user_settings"); n != 0 {
			t.Errorf("reading the defaults made %d rows", n)
		}
	})

	save := func(mut func(*UserSettings)) UserSettings {
		t.Helper()
		in := DefaultUserSettings(u.ID)
		mut(&in)
		if err := f.s.SaveUserSettings(f.ctx, &in); err != nil {
			t.Fatalf("SaveUserSettings: %+v", err)
		}
		got, err := f.s.UserSettingsOf(f.ctx, u.ID)
		if err != nil {
			t.Fatalf("UserSettingsOf: %+v", err)
		}
		return got
	}

	steps := []struct {
		name      string
		mut       func(*UserSettings)
		wantWords []string
		wantGroup bool
	}{
		{"keywords in order", func(s *UserSettings) { s.SetKeywords([]string{"zebra", "apple", "feeder schedule"}) },
			[]string{"zebra", "apple", "feeder schedule"}, true},
		{"group mentions off", func(s *UserSettings) {
			s.SetKeywords([]string{"zebra"})
			s.SetGroupMentions(false)
		}, []string{"zebra"}, false},
		{"group mentions off again", func(s *UserSettings) { s.SetGroupMentions(false) }, nil, false},
		{"group mentions back on", func(s *UserSettings) { s.SetGroupMentions(true) }, nil, true},
		{"keywords and group mentions off together", func(s *UserSettings) {
			s.SetKeywords([]string{"Μάνος", "c++"})
			s.SetGroupMentions(false)
		}, []string{"Μάνος", "c++"}, false},
		{"an empty list removes the keywords", func(s *UserSettings) { s.SetKeywords(nil) }, nil, true},
	}
	for _, tt := range steps {
		t.Run(tt.name, func(t *testing.T) {
			got := save(tt.mut)
			if !sameWords(got.Keywords(), tt.wantWords) {
				t.Errorf("Keywords = %#v, want %#v", got.Keywords(), tt.wantWords)
			}
			if got.GroupMentions() != tt.wantGroup {
				t.Errorf("GroupMentions = %v, want %v", got.GroupMentions(), tt.wantGroup)
			}
			if len(tt.wantWords) == 0 && got.KeywordsJSON != "[]" {
				t.Errorf("KeywordsJSON = %q, want []", got.KeywordsJSON)
			}
			if n := f.count("user_settings"); n != 1 {
				t.Errorf("%d rows, want 1", n)
			}
		})
	}

	t.Run("a settings value with no keywords field gets the default", func(t *testing.T) {
		other := f.user()
		if err := f.s.SaveUserSettings(f.ctx, &UserSettings{
			UserID: other.ID, Theme: ThemeLight, Sound: true, Notify: NotifyMentions, SoundName: SoundPurr, Replies: RepliesFollowing,
		}); err != nil {
			t.Fatalf("SaveUserSettings: %+v", err)
		}
		got, err := f.s.UserSettingsOf(f.ctx, other.ID)
		if err != nil || !noWords(got.Keywords()) || !got.GroupMentions() {
			t.Errorf("settings = %+v, %v; want no keywords and group mentions on", got, err)
		}
	})

	t.Run("a damaged list gives no keywords", func(t *testing.T) {
		f.exec("UPDATE user_settings SET keywords = ? WHERE user_id = ?", "not json", u.ID)
		got, err := f.s.UserSettingsOf(f.ctx, u.ID)
		if err != nil {
			t.Fatalf("UserSettingsOf: %+v", err)
		}
		if got.KeywordsJSON != "not json" || !noWords(got.Keywords()) {
			t.Errorf("KeywordsJSON %q, Keywords %#v; want nil", got.KeywordsJSON, got.Keywords())
		}
	})
}

func TestRecipientsCarryKeywordsAndGroupMentions(t *testing.T) {
	t.Parallel()
	f := newFix(t)
	author, keen, quiet, plain := f.user(), f.user(), f.user(), f.user()
	room := f.room(author)
	for _, u := range []*User{author, keen, quiet, plain} {
		f.create(&RoomMember{RoomID: room.ID, UserID: u.ID})
	}
	conv := f.groupWith(author, keen, quiet, plain)

	s := DefaultUserSettings(keen.ID)
	s.SetKeywords([]string{"deploy", "feeder schedule"})
	if err := f.s.SaveUserSettings(f.ctx, &s); err != nil {
		t.Fatal(err)
	}
	s = DefaultUserSettings(quiet.ID)
	s.SetGroupMentions(false)
	if err := f.s.SaveUserSettings(f.ctx, &s); err != nil {
		t.Fatal(err)
	}

	check := func(t *testing.T, rs []Recipient) {
		t.Helper()
		byUser := map[uint64]Recipient{}
		for _, r := range rs {
			byUser[r.UserID] = r
		}
		if len(byUser) != 3 {
			t.Fatalf("%d recipients, want 3 (without the author)", len(byUser))
		}
		if got := byUser[keen.ID].Settings; !sameWords(got.Keywords(), []string{"deploy", "feeder schedule"}) || !got.GroupMentions() {
			t.Errorf("keen = %+v", got)
		}
		if got := byUser[quiet.ID].Settings; !noWords(got.Keywords()) || got.GroupMentions() {
			t.Errorf("quiet = %+v, want no keywords and group mentions off", got)
		}
		if got := byUser[plain.ID].Settings; got.KeywordsJSON != "[]" || !noWords(got.Keywords()) || !got.GroupMentions() {
			t.Errorf("plain (no settings row) = %+v, want the defaults", got)
		}
	}

	tests := []struct {
		name string
		call func() ([]Recipient, error)
	}{
		{"RoomMemberRecipients", func() ([]Recipient, error) { return f.s.RoomMemberRecipients(f.ctx, room.ID, author.ID) }},
		{"ConversationRecipients", func() ([]Recipient, error) { return f.s.ConversationRecipients(f.ctx, conv.ID, author.ID) }},
		{"RecipientsOf", func() ([]Recipient, error) {
			return f.s.RecipientsOf(f.ctx, RoomPlace(room.ID), []uint64{keen.ID, quiet.ID, plain.ID})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs, err := tt.call()
			if err != nil {
				t.Fatalf("%s: %+v", tt.name, err)
			}
			check(t, rs)
		})
	}
}
