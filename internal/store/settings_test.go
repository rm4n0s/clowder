package store

import (
	"testing"
	"time"
)

func TestUserSettingsDefaultsAndSave(t *testing.T) {
	f := newFix(t)
	u := f.user()

	got, err := f.s.UserSettingsOf(f.ctx, u.ID)
	if err != nil {
		t.Fatalf("UserSettingsOf: %+v", err)
	}
	if got.Theme != ThemeSystem || !got.Sound || got.BrowserNotifications || got.UserID != u.ID {
		t.Errorf("defaults = %+v, want system, sound on, browser notifications off", got)
	}
	if n := f.count("user_settings"); n != 0 {
		t.Errorf("reading the defaults made %d rows, want 0", n)
	}

	tests := []struct {
		name string
		in   UserSettings
	}{
		{"dark, no sound, notifications", UserSettings{UserID: u.ID, Theme: ThemeDark, Sound: false, BrowserNotifications: true}},
		{"light, sound", UserSettings{UserID: u.ID, Theme: ThemeLight, Sound: true}},
		{"back to the defaults", DefaultUserSettings(u.ID)},
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
			if got.Theme != tt.in.Theme || got.Sound != tt.in.Sound || got.BrowserNotifications != tt.in.BrowserNotifications {
				t.Errorf("settings = %+v, want %+v", got, tt.in)
			}
			if n := f.count("user_settings"); n != 1 {
				t.Errorf("%d rows, want one per user", n)
			}
		})
	}

	other := f.user()
	if got, _ := f.s.UserSettingsOf(f.ctx, other.ID); got.Theme != ThemeSystem || !got.Sound {
		t.Errorf("another user's settings = %+v, want the defaults", got)
	}
}

func TestUserSettingsFailures(t *testing.T) {
	f := newFix(t)
	u := f.user()
	tests := []struct {
		name      string
		call      func() error
		wantRoute string
		wantOp    string
	}{
		{"unknown theme", func() error {
			return f.s.SaveUserSettings(f.ctx, &UserSettings{UserID: u.ID, Theme: "neon", Sound: true})
		}, "Store.SaveUserSettings.QueryFailed", "saveUserSettings"},
		{"unknown user", func() error {
			return f.s.SaveUserSettings(f.ctx, &UserSettings{UserID: 999, Theme: ThemeSystem})
		}, "Store.SaveUserSettings.QueryFailed", "saveUserSettings"},
		{"canceled save", func() error {
			return f.s.SaveUserSettings(canceled(t), &UserSettings{UserID: u.ID, Theme: ThemeSystem})
		}, "Store.SaveUserSettings.QueryFailed", "saveUserSettings"},
		{"canceled read", func() error {
			_, err := f.s.UserSettingsOf(canceled(t), u.ID)
			return err
		}, "Store.UserSettingsOf.QueryFailed", "userSettingsOf"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireTagged(t, tt.call(), TagQueryFailed, tt.wantRoute, tt.wantOp)
		})
	}
}

func TestProfileQueries(t *testing.T) {
	f := newFix(t)
	u := f.user()

	if err := f.s.SetUserDisplayName(f.ctx, u.ID, "New Name"); err != nil {
		t.Fatalf("SetUserDisplayName: %+v", err)
	}
	if err := f.s.SetUserAvatarKey(f.ctx, u.ID, "abc"); err != nil {
		t.Fatalf("SetUserAvatarKey: %+v", err)
	}
	if err := f.s.SetUserPasswordHash(f.ctx, u.ID, "newhash"); err != nil {
		t.Fatalf("SetUserPasswordHash: %+v", err)
	}
	got, err := f.s.UserByID(f.ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "New Name" || got.AvatarKey != "abc" || got.PasswordHash != "newhash" {
		t.Errorf("user = %+v", got)
	}
	if err := f.s.SetUserAvatarKey(f.ctx, u.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.s.UserByID(f.ctx, u.ID); got.AvatarKey != "" {
		t.Errorf("AvatarKey = %q after the removal, want empty", got.AvatarKey)
	}

	tests := []struct {
		name      string
		call      func() error
		wantTag   string
		wantRoute string
		wantOp    string
	}{
		{"display name of an unknown user", func() error { return f.s.SetUserDisplayName(f.ctx, 999, "x") },
			TagNotFound, "Store.SetUserDisplayName.NotFound", "setUserDisplayName"},
		{"avatar of an unknown user", func() error { return f.s.SetUserAvatarKey(f.ctx, 999, "x") },
			TagNotFound, "Store.SetUserAvatarKey.NotFound", "setUserAvatarKey"},
		{"password of an unknown user", func() error { return f.s.SetUserPasswordHash(f.ctx, 999, "x") },
			TagNotFound, "Store.SetUserPasswordHash.NotFound", "setUserPasswordHash"},
		{"canceled display name", func() error { return f.s.SetUserDisplayName(canceled(t), u.ID, "x") },
			TagQueryFailed, "Store.SetUserDisplayName.QueryFailed", "setUserDisplayName"},
		{"canceled avatar", func() error { return f.s.SetUserAvatarKey(canceled(t), u.ID, "x") },
			TagQueryFailed, "Store.SetUserAvatarKey.QueryFailed", "setUserAvatarKey"},
		{"canceled password", func() error { return f.s.SetUserPasswordHash(canceled(t), u.ID, "x") },
			TagQueryFailed, "Store.SetUserPasswordHash.QueryFailed", "setUserPasswordHash"},
		{"canceled delete of the other sessions", func() error { _, err := f.s.DeleteSessionsOfUserExcept(canceled(t), u.ID, 1); return err },
			TagQueryFailed, "Store.DeleteSessionsOfUserExcept.QueryFailed", "deleteSessionsOfUserExcept"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireTagged(t, tt.call(), tt.wantTag, tt.wantRoute, tt.wantOp)
		})
	}
}

func TestDeleteSessionsOfUserExcept(t *testing.T) {
	f := newFix(t)
	a, b := f.user(), f.user()
	mk := func(u *User, hash string) *Session {
		s := &Session{TokenHash: hash, UserID: u.ID, ExpiresAt: day(24 * time.Hour), LastSeenAt: day(0), Kind: SessionBrowser}
		f.create(s)
		return s
	}
	keep := mk(a, "a1")
	mk(a, "a2")
	mk(a, "a3")
	other := mk(b, "b1")

	if _, err := f.s.DeleteSessionsOfUserExcept(f.ctx, a.ID, keep.ID); err != nil {
		t.Fatalf("DeleteSessionsOfUserExcept: %+v", err)
	}
	if n, _ := f.s.CountSessionsOfUser(f.ctx, a.ID); n != 1 {
		t.Errorf("%d sessions of the user are left, want 1", n)
	}
	if _, err := f.s.SessionByTokenHash(f.ctx, "a1"); err != nil {
		t.Errorf("the kept session is gone: %v", err)
	}
	if _, err := f.s.SessionByTokenHash(f.ctx, "b1"); err != nil {
		t.Errorf("the session of another user (%d) is gone: %v", other.ID, err)
	}
}
