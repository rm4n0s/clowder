package store

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSessionKindAndClientName(t *testing.T) {
	f := newFix(t)
	u := f.user()
	rows := []*Session{
		{TokenHash: "b", UserID: u.ID, ExpiresAt: day(time.Hour), LastSeenAt: day(0), Kind: SessionBrowser, ClientName: "Firefox on Linux"},
		{TokenHash: "a", UserID: u.ID, ExpiresAt: day(time.Hour), LastSeenAt: day(0), Kind: SessionAPI, ClientName: strings.Repeat("é", MaxClientNameRunes)},
	}
	for _, s := range rows {
		if err := f.s.CreateSession(f.ctx, s); err != nil {
			t.Fatalf("CreateSession: %+v", err)
		}
	}
	for _, want := range rows {
		got, err := f.s.SessionByTokenHash(f.ctx, want.TokenHash)
		if err != nil {
			t.Fatal(err)
		}
		if got.Kind != want.Kind || got.ClientName != want.ClientName {
			t.Errorf("session %s: kind %q client %q, want %q %q", want.TokenHash, got.Kind, got.ClientName, want.Kind, want.ClientName)
		}
	}
	// A session made without saying: the zero kind is not valid in the
	// database's eyes only through the default, so check what GORM stores.
	plain := &Session{TokenHash: "c", UserID: u.ID, ExpiresAt: day(time.Hour), LastSeenAt: day(0)}
	if err := f.s.CreateSession(f.ctx, plain); err != nil {
		t.Fatal(err)
	}
	got, err := f.s.SessionByTokenHash(f.ctx, "c")
	if err != nil || got.Kind != SessionBrowser || got.ClientName != "" {
		t.Errorf("a session without a kind = %+v, %v, want the browser default", got, err)
	}
}

// TestSessionsOfOneDotZeroBecomeBrowserSessions checks the migration: the
// sessions that exist before the columns do (version 1.0) get the kind
// browser and an empty client name when the schema is brought up to date.
func TestSessionsOfOneDotZeroBecomeBrowserSessions(t *testing.T) {
	f := newFix(t)
	u := f.user()
	f.exec("ALTER TABLE sessions DROP COLUMN kind")
	f.exec("ALTER TABLE sessions DROP COLUMN client_name")
	f.exec("INSERT INTO sessions (created_at, token_hash, user_id, expires_at, last_seen_at) VALUES (?, ?, ?, ?, ?)",
		day(0), "old", u.ID, day(time.Hour), day(0))
	if err := f.s.migrate(f.ctx, allModels(), nil); err != nil {
		t.Fatalf("migrate: %+v", err)
	}
	got, err := f.s.SessionByTokenHash(f.ctx, "old")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != SessionBrowser || got.ClientName != "" {
		t.Errorf("old session = kind %q client %q, want browser and empty", got.Kind, got.ClientName)
	}
	// And a new one can be made.
	if err := f.s.CreateSession(f.ctx, &Session{TokenHash: "new", UserID: u.ID, ExpiresAt: day(time.Hour), LastSeenAt: day(0), Kind: SessionAPI, ClientName: "app"}); err != nil {
		t.Fatalf("CreateSession: %+v", err)
	}
}

func TestSessionsOfUserQuery(t *testing.T) {
	f := newFix(t)
	u, other := f.user(), f.user()
	add := func(hash string, owner *User, at time.Duration) *Session {
		t.Helper()
		f.clk.Set(day(at))
		s := &Session{TokenHash: hash, UserID: owner.ID, ExpiresAt: day(24 * time.Hour), LastSeenAt: day(at), Kind: SessionAPI, ClientName: hash}
		if err := f.s.CreateSession(f.ctx, s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	add("first", u, time.Minute)
	add("second", u, 2*time.Minute)
	add("third", u, 3*time.Minute)
	add("theirs", other, 4*time.Minute)
	got, err := f.s.SessionsOfUser(f.ctx, u.ID)
	if err != nil {
		t.Fatalf("SessionsOfUser: %+v", err)
	}
	var names []string
	for _, s := range got {
		names = append(names, s.ClientName)
	}
	if !slices.Equal(names, []string{"third", "second", "first"}) {
		t.Errorf("sessions = %v, want the newest first and only the user's", names)
	}
	none, err := f.s.SessionsOfUser(f.ctx, 99999)
	if err != nil || len(none) != 0 {
		t.Errorf("SessionsOfUser of nobody = %v, %v", none, err)
	}
}

func TestDeleteSessionOfUserQuery(t *testing.T) {
	f := newFix(t)
	u, other := f.user(), f.user()
	mine := &Session{TokenHash: "mine", UserID: u.ID, ExpiresAt: day(time.Hour), LastSeenAt: day(0), Kind: SessionAPI}
	theirs := &Session{TokenHash: "theirs", UserID: other.ID, ExpiresAt: day(time.Hour), LastSeenAt: day(0), Kind: SessionAPI}
	for _, s := range []*Session{mine, theirs} {
		if err := f.s.CreateSession(f.ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	// Someone else's session is not found, and stays.
	requireTagged(t, f.s.DeleteSessionOfUser(f.ctx, u.ID, theirs.ID), TagNotFound, "Store.DeleteSessionOfUser.NotFound", "deleteSessionOfUser")
	if _, err := f.s.SessionByTokenHash(f.ctx, "theirs"); err != nil {
		t.Errorf("the other user's session was deleted: %v", err)
	}
	requireTagged(t, f.s.DeleteSessionOfUser(f.ctx, u.ID, 99999), TagNotFound, "Store.DeleteSessionOfUser.NotFound", "deleteSessionOfUser")
	requireTagged(t, f.s.DeleteSessionOfUser(canceled(t), u.ID, mine.ID), TagQueryFailed, "Store.DeleteSessionOfUser.QueryFailed", "deleteSessionOfUser")
	if err := f.s.DeleteSessionOfUser(f.ctx, u.ID, mine.ID); err != nil {
		t.Fatalf("DeleteSessionOfUser: %+v", err)
	}
	if _, err := f.s.SessionByTokenHash(f.ctx, "mine"); err == nil {
		t.Error("the session is still there")
	}
	requireTagged(t, f.s.DeleteSessionOfUser(f.ctx, u.ID, mine.ID), TagNotFound, "Store.DeleteSessionOfUser.NotFound", "deleteSessionOfUser")
}

func TestSessionsOfUserQueryFails(t *testing.T) {
	f := newFix(t)
	_, err := f.s.SessionsOfUser(canceled(t), 1)
	requireTagged(t, err, TagQueryFailed, "Store.SessionsOfUser.QueryFailed", "sessionsOfUser")
}

func TestUsersByPrefix(t *testing.T) {
	f := newFix(t)
	mk := func(username, display string) *User {
		t.Helper()
		u := &User{Username: username, DisplayName: display, PasswordHash: "h", Role: RoleMember}
		f.create(u)
		return u
	}
	mk("alice", "Alice A")
	mk("albert", "Albert")
	mk("bob", "Alma Bob")
	mk("carol", "Carol")
	off := mk("alina", "Alina")
	mk("x_y", "Under Score")
	mk("xay", "Plain")
	mk("pct", "100% Done")
	mk("slash", `Back\slash`)
	f.exec("UPDATE users SET disabled_at = ? WHERE id = ?", day(0), off.ID)
	names := func(prefix string, limit int) []string {
		t.Helper()
		users, err := f.s.UsersByPrefix(f.ctx, prefix, limit)
		if err != nil {
			t.Fatalf("UsersByPrefix(%q): %+v", prefix, err)
		}
		out := make([]string, len(users))
		for i, u := range users {
			out[i] = u.Username
		}
		return out
	}
	tests := []struct {
		prefix string
		limit  int
		want   []string
	}{
		{"al", 20, []string{"albert", "alice", "bob"}},
		{"AL", 20, []string{"albert", "alice", "bob"}},
		{"alm", 20, []string{"bob"}},
		{"alice a", 20, []string{"alice"}},
		{"ALICE A", 20, []string{"alice"}},
		{"alin", 20, nil},
		{"ice", 20, nil},
		{"x_", 20, []string{"x_y"}},
		{"under", 20, []string{"x_y"}},
		{"x%", 20, nil},
		{"100%", 20, []string{"pct"}},
		{"%", 20, nil},
		{"_", 20, nil},
		{`back\`, 20, []string{"slash"}},
		{`\`, 20, nil},
		{"nobody", 20, nil},
		{"al", 2, []string{"albert", "alice"}},
		{"", 3, []string{"albert", "alice", "bob"}},
	}
	for _, tt := range tests {
		if got := names(tt.prefix, tt.limit); !slices.Equal(got, tt.want) {
			t.Errorf("UsersByPrefix(%q, %d) = %v, want %v", tt.prefix, tt.limit, got, tt.want)
		}
	}
	all := names("", 100)
	if slices.Contains(all, "alina") || len(all) != 8 {
		t.Errorf("UsersByPrefix(\"\") = %v, want the 8 enabled users", all)
	}
	_, err := f.s.UsersByPrefix(canceled(t), "a", 5)
	requireTagged(t, err, TagQueryFailed, "Store.UsersByPrefix.QueryFailed", "usersByPrefix")
}
