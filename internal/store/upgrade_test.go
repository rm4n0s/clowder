package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/clock"
)

// TestOpenUpgradesTheSessionsOfA10Database checks D46: a SQLite database made
// by 1.0, whose sessions have neither a kind nor a client name, is brought up
// to date by opening it, and the sessions that were live stay: they become
// browser sessions with no client name. The 1.0 table is made by dropping the
// two columns that 1.1 added.
func TestOpenUpgradesTheSessionsOfA10Database(t *testing.T) {
	if onPostgres() {
		t.Skip("the upgrade of a 1.0 database is a SQLite case: PostgreSQL is new in 1.1")
	}
	ctx := t.Context()
	clk := clock.NewFake(fakeStart)
	path := filepath.Join(t.TempDir(), "clowder.db")

	s, err := Open(ctx, path, clk, nil)
	if err != nil {
		t.Fatalf("Open: %+v", err)
	}
	user := User{Username: "alice", DisplayName: "Alice", PasswordHash: "h", Role: RoleMember}
	if err := s.CreateUser(ctx, &user); err != nil {
		t.Fatalf("CreateUser: %+v", err)
	}
	for _, stmt := range []string{"ALTER TABLE sessions DROP COLUMN kind", "ALTER TABLE sessions DROP COLUMN client_name"} {
		if err := s.db(ctx).Exec(stmt).Error; err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	err = s.db(ctx).Exec("INSERT INTO sessions (token_hash, user_id, expires_at, last_seen_at, created_at) VALUES (?, ?, ?, ?, ?)",
		"legacy-token-hash", user.ID, fakeStart.Add(24*time.Hour), fakeStart, fakeStart).Error
	if err != nil {
		t.Fatalf("insert a 1.0 session: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s, err = Open(ctx, path, clk, nil)
	if err != nil {
		t.Fatalf("Open the 1.0 database: %+v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	var sess Session
	if err := s.db(ctx).Where("token_hash = ?", "legacy-token-hash").First(&sess).Error; err != nil {
		t.Fatalf("the 1.0 session is gone: %v", err)
	}
	if sess.Kind != SessionBrowser || sess.ClientName != "" || sess.UserID != user.ID {
		t.Errorf("session = kind %q, client name %q, user %d; want a browser session with no client name", sess.Kind, sess.ClientName, sess.UserID)
	}
}

// TestOpenUpgradesTheSettingsOf181 checks that a SQLite database made by step
// 18.1, whose user_settings has neither notify, sound_name nor replies, is
// brought up to date by opening it: the rows stay and get the defaults.
func TestOpenUpgradesTheSettingsOf181(t *testing.T) {
	if onPostgres() {
		t.Skip("the upgrade of an earlier 1.2 database is a SQLite case")
	}
	ctx := t.Context()
	clk := clock.NewFake(fakeStart)
	path := filepath.Join(t.TempDir(), "clowder.db")

	s, err := Open(ctx, path, clk, nil)
	if err != nil {
		t.Fatalf("Open: %+v", err)
	}
	user := User{Username: "alice", DisplayName: "Alice", PasswordHash: "h", Role: RoleMember}
	if err := s.CreateUser(ctx, &user); err != nil {
		t.Fatalf("CreateUser: %+v", err)
	}
	for _, stmt := range []string{
		"DROP TABLE user_settings",
		"CREATE TABLE `user_settings` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`user_id` integer NOT NULL," +
			"`theme` text NOT NULL,`sound` numeric NOT NULL,`browser_notifications` numeric NOT NULL," +
			"CONSTRAINT `fk_user_settings_user` FOREIGN KEY (`user_id`) REFERENCES `users`(`id`) ON DELETE RESTRICT," +
			"CONSTRAINT `chk_user_settings_theme` CHECK (theme IN ('system','light','dark')))",
		"CREATE UNIQUE INDEX `uq_user_settings_user_id` ON `user_settings`(`user_id`)",
	} {
		if err := s.db(ctx).Exec(stmt).Error; err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	err = s.db(ctx).Exec("INSERT INTO user_settings (user_id, theme, sound, browser_notifications, created_at) VALUES (?, 'dark', 0, 1, ?)",
		user.ID, fakeStart).Error
	if err != nil {
		t.Fatalf("insert an 18.1 row: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s, err = Open(ctx, path, clk, nil)
	if err != nil {
		t.Fatalf("Open the 18.1 database: %+v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	got, err := s.UserSettingsOf(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Theme != ThemeDark || got.Sound || !got.BrowserNotifications ||
		got.Notify != NotifyMentions || got.SoundName != SoundPurr || got.Replies != RepliesFollowing {
		t.Errorf("settings = %+v, want the 18.1 values and the defaults of the new fields", got)
	}
	got.Notify = NotifyAll
	if err := s.SaveUserSettings(ctx, &got); err != nil {
		t.Fatalf("SaveUserSettings: %+v", err)
	}
	bad := got
	bad.Replies = "some"
	if err := s.SaveUserSettings(ctx, &bad); err == nil {
		t.Error("a value outside the check of an upgraded table was accepted")
	}
}
