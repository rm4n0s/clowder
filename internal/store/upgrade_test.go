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
