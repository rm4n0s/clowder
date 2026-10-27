package app_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/clowder/internal/store/storetest"
)

const (
	loginIP        = "192.0.2.10"
	wrongLoginText = "Wrong username or password."
)

func TestLogin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		username string
		password string
		prepare  func(e *env, u app.User)
		wantErr  bool
	}{
		{name: "right password", username: "alice", password: goodPassword},
		{name: "username in capitals", username: "ALICE", password: goodPassword},
		{name: "username with spaces", username: "  alice\t", password: goodPassword},
		{name: "wrong password", username: "alice", password: "not the password", wantErr: true},
		{name: "empty password", username: "alice", password: "", wantErr: true},
		{name: "password in other case", username: "alice", password: strings.ToUpper(goodPassword), wantErr: true},
		{name: "password too long", username: "alice", password: strings.Repeat("x", 2000), wantErr: true},
		{name: "unknown user", username: "nobody", password: goodPassword, wantErr: true},
		{name: "empty username", username: "", password: goodPassword, wantErr: true},
		{
			name: "disabled user with the right password", username: "alice", password: goodPassword, wantErr: true,
			prepare: func(e *env, u app.User) {
				if err := e.st.SetUserDisabled(e.t.Context(), u.ID, true); err != nil {
					e.t.Fatal(err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			alice := e.signUp("alice", store.RoleMember)
			if tt.prepare != nil {
				tt.prepare(e, alice)
			}

			token, user, err := e.svc.Login(t.Context(), tt.username, tt.password, loginIP)
			if tt.wantErr {
				appErr := requireTag(t, err, app.TagInvalidInput, "Accounts.Login.InvalidInput")
				fields := fieldsOf(t, appErr)
				if len(fields) != 1 || fields[app.FieldForm] != wrongLoginText {
					t.Errorf("fields = %v, want only %s = %q", fields, app.FieldForm, wrongLoginText)
				}
				if token != "" || user != (app.User{}) {
					t.Errorf("a failed login returned token %q and user %+v", token, user)
				}
				if n, err := e.st.CountSessionsOfUser(t.Context(), alice.ID); err != nil || n != 0 {
					t.Errorf("sessions = %d, %v, want none", n, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Login: %+v", err)
			}
			if user.ID != alice.ID || user.Username != "alice" || user.DisplayName != "Alice" || user.Role != store.RoleMember {
				t.Errorf("user = %+v", user)
			}
			if len(token) != 43 {
				t.Errorf("token %q has %d characters, want 43", token, len(token))
			}
			// Only the hash is stored, with the expiry from the session lifetime.
			sess, err := e.st.SessionByTokenHash(t.Context(), auth.HashToken(token))
			if err != nil {
				t.Fatalf("the session is not stored by the hash of the token: %+v", err)
			}
			if sess.TokenHash == token {
				t.Error("the token itself is stored")
			}
			if sess.UserID != alice.ID || !sess.LastSeenAt.Equal(start) || !sess.ExpiresAt.Equal(start.Add(sessionLifetime)) {
				t.Errorf("session = %+v, want user %d, seen %v, expiring %v", sess, alice.ID, start, start.Add(sessionLifetime))
			}
		})
	}
}

func TestLoginErrorsAreTheSame(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	alice := e.signUp("alice", store.RoleMember)
	e.signUp("dora", store.RoleMember)
	if err := e.st.SetUserDisabled(t.Context(), alice.ID, true); err != nil {
		t.Fatal(err)
	}

	var messages []string
	for _, c := range []struct{ user, password string }{
		{"dora", "wrong password"},   // wrong password
		{"nobody", "wrong password"}, // unknown user
		{"alice", goodPassword},      // disabled user
	} {
		_, _, err := e.svc.Login(t.Context(), c.user, c.password, loginIP)
		appErr := requireTag(t, err, app.TagInvalidInput, "Accounts.Login.InvalidInput")
		messages = append(messages, appErr.Message+"|"+fieldsOf(t, appErr)[app.FieldForm])
	}
	if messages[0] != messages[1] || messages[1] != messages[2] {
		t.Errorf("the three failures differ: %q", messages)
	}
}

func TestLoginTwiceMakesTwoSessions(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	alice := e.signUp("alice", store.RoleMember)
	t1, _, err := e.svc.Login(t.Context(), "alice", goodPassword, loginIP)
	if err != nil {
		t.Fatal(err)
	}
	t2, _, err := e.svc.Login(t.Context(), "alice", goodPassword, loginIP)
	if err != nil {
		t.Fatal(err)
	}
	if t1 == t2 {
		t.Error("two logins returned the same token")
	}
	if n, err := e.st.CountSessionsOfUser(t.Context(), alice.ID); err != nil || n != 2 {
		t.Errorf("sessions = %d, %v, want 2", n, err)
	}
}

func TestLoginRateLimit(t *testing.T) {
	t.Parallel()
	failN := func(e *env, n int, user, ip string) {
		e.t.Helper()
		for range n {
			_, _, err := e.svc.Login(e.t.Context(), user, "wrong password", ip)
			_ = requireTag(e.t, err, app.TagInvalidInput, "Accounts.Login.InvalidInput")
		}
	}
	tests := []struct {
		name string
		run  func(t *testing.T, e *env)
	}{
		{"five failures for a username block it, even with the right password", func(t *testing.T, e *env) {
			for i := range 5 {
				failN(e, 1, "alice", "192.0.2."+string(rune('1'+i))) // five different addresses
			}
			_, _, err := e.svc.Login(t.Context(), "alice", goodPassword, "192.0.2.99")
			_ = requireTag(t, err, app.TagRateLimited, "Accounts.Login.RateLimited")
			// Another user from the same fresh address is fine.
			if _, _, err := e.svc.Login(t.Context(), "bob", goodPassword, "192.0.2.99"); err != nil {
				t.Errorf("another user was blocked: %+v", err)
			}
		}},
		{"five failures from an address block it for every username", func(t *testing.T, e *env) {
			for _, u := range []string{"u1", "u2", "u3", "u4", "u5"} {
				failN(e, 1, u, loginIP)
			}
			_, _, err := e.svc.Login(t.Context(), "bob", goodPassword, loginIP)
			_ = requireTag(t, err, app.TagRateLimited, "Accounts.Login.RateLimited")
			if _, _, err := e.svc.Login(t.Context(), "bob", goodPassword, "192.0.2.77"); err != nil {
				t.Errorf("another address was blocked: %+v", err)
			}
		}},
		{"four failures do not block", func(t *testing.T, e *env) {
			failN(e, 4, "alice", loginIP)
			if _, _, err := e.svc.Login(t.Context(), "alice", goodPassword, loginIP); err != nil {
				t.Errorf("Login after 4 failures: %+v", err)
			}
		}},
		{"the block ends when the window passes", func(t *testing.T, e *env) {
			failN(e, 5, "alice", loginIP)
			e.clk.Advance(14*time.Minute + 59*time.Second)
			_, _, err := e.svc.Login(t.Context(), "alice", goodPassword, loginIP)
			_ = requireTag(t, err, app.TagRateLimited, "Accounts.Login.RateLimited")
			e.clk.Advance(time.Second)
			if _, _, err := e.svc.Login(t.Context(), "alice", goodPassword, loginIP); err != nil {
				t.Errorf("Login after the window: %+v", err)
			}
		}},
		{"a refused attempt does not extend the block", func(t *testing.T, e *env) {
			failN(e, 5, "alice", loginIP)
			for range 10 {
				e.clk.Advance(time.Minute)
				_, _, err := e.svc.Login(t.Context(), "alice", goodPassword, loginIP)
				if i := e.clk.Now().Sub(start); i < 15*time.Minute {
					_ = requireTag(t, err, app.TagRateLimited, "Accounts.Login.RateLimited")
				}
			}
			e.clk.Advance(6 * time.Minute)
			if _, _, err := e.svc.Login(t.Context(), "alice", goodPassword, loginIP); err != nil {
				t.Errorf("Login after the window: %+v", err)
			}
		}},
		{"a success resets the failures of the username", func(t *testing.T, e *env) {
			failN(e, 4, "alice", loginIP)
			if _, _, err := e.svc.Login(t.Context(), "alice", goodPassword, loginIP); err != nil {
				t.Fatal(err)
			}
			// The four failures of the address count: one more is the fifth.
			failN(e, 1, "alice", "192.0.2.50")
			failN(e, 3, "alice", "192.0.2.51") // alice has 4 now, not 8
			if _, _, err := e.svc.Login(t.Context(), "alice", goodPassword, "192.0.2.52"); err != nil {
				t.Errorf("Login: the success should have cleared the earlier failures: %+v", err)
			}
		}},
		{"a disabled user's attempts count", func(t *testing.T, e *env) {
			u, err := e.st.UserByUsername(t.Context(), "alice")
			if err != nil {
				t.Fatal(err)
			}
			if err := e.st.SetUserDisabled(t.Context(), u.ID, true); err != nil {
				t.Fatal(err)
			}
			for range 5 {
				_, _, err := e.svc.Login(t.Context(), "alice", goodPassword, loginIP)
				_ = requireTag(t, err, app.TagInvalidInput, "Accounts.Login.InvalidInput")
			}
			_, _, err = e.svc.Login(t.Context(), "alice", goodPassword, loginIP)
			_ = requireTag(t, err, app.TagRateLimited, "Accounts.Login.RateLimited")
		}},
		{"the username is normalized for the limiter", func(t *testing.T, e *env) {
			for _, u := range []string{"alice", "ALICE", " Alice ", "aLiCe", "alice"} {
				failN(e, 1, u, "")
			}
			_, _, err := e.svc.Login(t.Context(), "alice", goodPassword, "")
			_ = requireTag(t, err, app.TagRateLimited, "Accounts.Login.RateLimited")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.signUp("alice", store.RoleMember)
			e.signUp("bob", store.RoleMember)
			tt.run(t, e)
		})
	}
}

func TestLoginUsesTheInjectedLimiter(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(start)
	st := storetest.Open(t, clk)
	limiter := app.NewLoginLimiter(clk)
	svc := app.NewAccounts(st, clk, auth.FastHasher(), app.AccountsConfig{
		InvitationLifetime: lifetime, SessionLifetime: sessionLifetime, Limiter: limiter,
	})
	for range 5 {
		limiter.Fail("ghost", "")
	}
	_, _, err := svc.Login(t.Context(), "ghost", goodPassword, "")
	_ = requireTag(t, err, app.TagRateLimited, "Accounts.Login.RateLimited")
}

func TestLoginStoreErrorsPassUp(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.signUp("alice", store.RoleMember)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := e.svc.Login(ctx, "alice", goodPassword, loginIP)
	_ = requireTag(t, err, store.TagQueryFailed, "Accounts.Login->Store.UserByUsername.QueryFailed")
}

// login logs in as the user and returns the token.
func (e *env) login(username string) string {
	e.t.Helper()
	token, _, err := e.svc.Login(e.t.Context(), username, goodPassword, loginIP)
	if err != nil {
		e.t.Fatalf("Login %s: %+v", username, err)
	}
	return token
}

func TestSessionActor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		token     func(e *env, u app.User) string
		wantRoute string // empty: success
	}{
		{name: "valid", token: func(e *env, _ app.User) string { return e.login("alice") }},
		{name: "empty token", token: func(*env, app.User) string { return "" }, wantRoute: "Accounts.SessionActor.Unauthenticated"},
		{name: "unknown token", token: func(*env, app.User) string { return "no-such-token" }, wantRoute: "Accounts.SessionActor.Unauthenticated"},
		{
			name: "expired", wantRoute: "Accounts.SessionActor.Unauthenticated",
			token: func(e *env, _ app.User) string {
				tok := e.login("alice")
				e.clk.Advance(sessionLifetime)
				return tok
			},
		},
		{
			name: "one second before it expires",
			token: func(e *env, _ app.User) string {
				tok := e.login("alice")
				e.clk.Advance(sessionLifetime - time.Second)
				return tok
			},
		},
		{
			name: "user disabled after the login", wantRoute: "Accounts.SessionActor.Unauthenticated",
			token: func(e *env, u app.User) string {
				tok := e.login("alice")
				// SetDisabled deletes the sessions; disabling in the store
				// leaves them, so the check of the user is what refuses.
				if err := e.st.SetUserDisabled(e.t.Context(), u.ID, true); err != nil {
					e.t.Fatal(err)
				}
				return tok
			},
		},
		{
			name: "user disabled by an admin", wantRoute: "Accounts.SessionActor.Unauthenticated",
			token: func(e *env, u app.User) string {
				tok := e.login("alice")
				root := e.signUp("root", store.RoleAdmin)
				if err := e.svc.SetDisabled(e.t.Context(), actor(root), u.ID, true); err != nil {
					e.t.Fatal(err)
				}
				return tok
			},
		},
		{
			name: "after logout", wantRoute: "Accounts.SessionActor.Unauthenticated",
			token: func(e *env, _ app.User) string {
				tok := e.login("alice")
				if err := e.svc.Logout(e.t.Context(), tok); err != nil {
					e.t.Fatal(err)
				}
				return tok
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			alice := e.signUp("alice", store.RoleMember)
			token := tt.token(e, alice)

			sess, err := e.svc.SessionActor(t.Context(), token)
			act, user := sess.Actor, sess.User
			if tt.wantRoute != "" {
				_ = requireTag(t, err, app.TagUnauthenticated, tt.wantRoute)
				if act != (app.Actor{}) || user != (app.User{}) {
					t.Errorf("a refused session returned %+v and %+v", act, user)
				}
				return
			}
			if err != nil {
				t.Fatalf("SessionActor: %+v", err)
			}
			if act != (app.Actor{UserID: alice.ID, Username: "alice", Role: store.RoleMember}) {
				t.Errorf("actor = %+v", act)
			}
			if user.ID != alice.ID || user.DisplayName != "Alice" {
				t.Errorf("user = %+v", user)
			}
		})
	}
}

func TestSessionActorReasons(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	alice := e.signUp("alice", store.RoleMember)
	tok := e.login("alice")

	_, err := e.svc.SessionActor(t.Context(), "")
	if got := requireTag(t, err, app.TagUnauthenticated, "Accounts.SessionActor.Unauthenticated").Metadata["reason"]; got != "none" {
		t.Errorf("reason = %v, want none", got)
	}
	_, err = e.svc.SessionActor(t.Context(), "x")
	if got := requireTag(t, err, app.TagUnauthenticated, "Accounts.SessionActor.Unauthenticated").Metadata["reason"]; got != "unknown" {
		t.Errorf("reason = %v, want unknown", got)
	}
	if err := e.st.SetUserDisabled(t.Context(), alice.ID, true); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.SessionActor(t.Context(), tok)
	if got := requireTag(t, err, app.TagUnauthenticated, "Accounts.SessionActor.Unauthenticated").Metadata["reason"]; got != "disabled" {
		t.Errorf("reason = %v, want disabled", got)
	}
	if err := e.st.SetUserDisabled(t.Context(), alice.ID, false); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(sessionLifetime)
	_, err = e.svc.SessionActor(t.Context(), tok)
	if got := requireTag(t, err, app.TagUnauthenticated, "Accounts.SessionActor.Unauthenticated").Metadata["reason"]; got != "expired" {
		t.Errorf("reason = %v, want expired", got)
	}
}

func TestSessionActorSlidingExpiry(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.signUp("alice", store.RoleMember)
	tok := e.login("alice")
	read := func() store.Session {
		t.Helper()
		s, err := e.st.SessionByTokenHash(t.Context(), auth.HashToken(tok))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	use := func() {
		t.Helper()
		if _, err := e.svc.SessionActor(t.Context(), tok); err != nil {
			t.Fatalf("SessionActor: %+v", err)
		}
	}

	// Within a minute of the last use nothing is written.
	for _, d := range []time.Duration{0, 30 * time.Second, 30 * time.Second} { // 0s, 30s, 60s
		e.clk.Advance(d)
		use()
		if s := read(); !s.LastSeenAt.Equal(start) || !s.ExpiresAt.Equal(start.Add(sessionLifetime)) {
			t.Fatalf("at +%v the session was written: seen %v, expires %v", e.clk.Now().Sub(start), s.LastSeenAt, s.ExpiresAt)
		}
	}

	// Older than a minute: the use and the expiry move.
	e.clk.Advance(time.Second)
	use()
	now := e.clk.Now()
	if s := read(); !s.LastSeenAt.Equal(now) || !s.ExpiresAt.Equal(now.Add(sessionLifetime)) {
		t.Errorf("after 61s: seen %v (want %v), expires %v (want %v)", s.LastSeenAt, now, s.ExpiresAt, now.Add(sessionLifetime))
	}

	// The lifetime counts from the last use: used every 20 days, the session
	// outlives the original 30 days.
	e.clk.Advance(20 * 24 * time.Hour)
	use()
	e.clk.Advance(20 * 24 * time.Hour)
	use() // 40 days after the login
	// ...and an unused session still ends 30 days after its last use.
	e.clk.Advance(sessionLifetime)
	_, err := e.svc.SessionActor(t.Context(), tok)
	_ = requireTag(t, err, app.TagUnauthenticated, "Accounts.SessionActor.Unauthenticated")
}

func TestSessionActorStoreErrorsPassUp(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.signUp("alice", store.RoleMember)
	tok := e.login("alice")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := e.svc.SessionActor(ctx, tok)
	_ = requireTag(t, err, store.TagQueryFailed, "Accounts.SessionActor->Store.SessionByTokenHash.QueryFailed")
}

func TestLogout(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	alice := e.signUp("alice", store.RoleMember)
	e.signUp("bob", store.RoleMember)
	tokA1, tokA2, tokB := e.login("alice"), e.login("alice"), e.login("bob")

	if err := e.svc.Logout(t.Context(), tokA1); err != nil {
		t.Fatalf("Logout: %+v", err)
	}
	_, err := e.svc.SessionActor(t.Context(), tokA1)
	_ = requireTag(t, err, app.TagUnauthenticated, "Accounts.SessionActor.Unauthenticated")
	for name, tok := range map[string]string{"the other session of the user": tokA2, "the session of another user": tokB} {
		if _, err := e.svc.SessionActor(t.Context(), tok); err != nil {
			t.Errorf("%s was logged out too: %+v", name, err)
		}
	}
	if n, err := e.st.CountSessionsOfUser(t.Context(), alice.ID); err != nil || n != 1 {
		t.Errorf("sessions of alice = %d, %v, want 1", n, err)
	}

	// An unknown, an empty and an already deleted token are not errors.
	for _, tok := range []string{"unknown", "", tokA1} {
		if err := e.svc.Logout(t.Context(), tok); err != nil {
			t.Errorf("Logout(%q): %+v", tok, err)
		}
	}
}

func TestLogoutStoreErrorsPassUp(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := e.svc.Logout(ctx, "token")
	_ = requireTag(t, err, store.TagQueryFailed, "Accounts.Logout->Store.SessionByTokenHash.QueryFailed")
}

func TestRateLimitedIsExported(t *testing.T) {
	t.Parallel()
	if app.TagRateLimited == "" || app.TagRateLimited == app.TagUnauthenticated {
		t.Errorf("TagRateLimited = %q", app.TagRateLimited)
	}
}

func TestSessionActorRefreshed(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.signUp("alice", store.RoleMember)
	tok := e.login("alice")
	use := func() app.Session {
		t.Helper()
		sess, err := e.svc.SessionActor(t.Context(), tok)
		if err != nil {
			t.Fatalf("SessionActor: %+v", err)
		}
		return sess
	}

	// Within a minute of the last use: not refreshed, the expiry is the old one.
	e.clk.Advance(time.Minute)
	if sess := use(); sess.Refreshed || !sess.ExpiresAt.Equal(start.Add(sessionLifetime)) {
		t.Errorf("after 60s: Refreshed = %v, ExpiresAt = %v; want false, %v", sess.Refreshed, sess.ExpiresAt, start.Add(sessionLifetime))
	}
	// Older than a minute: refreshed, the expiry is a full lifetime from now.
	e.clk.Advance(time.Second)
	want := e.clk.Now().Add(sessionLifetime)
	if sess := use(); !sess.Refreshed || !sess.ExpiresAt.Equal(want) {
		t.Errorf("after 61s: Refreshed = %v, ExpiresAt = %v; want true, %v", sess.Refreshed, sess.ExpiresAt, want)
	}
	// The write moved the last use: the next call is within the minute again.
	e.clk.Advance(30 * time.Second)
	if sess := use(); sess.Refreshed || !sess.ExpiresAt.Equal(want) {
		t.Errorf("30s after the refresh: Refreshed = %v, ExpiresAt = %v; want false, %v", sess.Refreshed, sess.ExpiresAt, want)
	}
}

func TestExpiredSessionIsDeletedWhenFound(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	alice := e.signUp("alice", store.RoleMember)
	tok, other := e.login("alice"), e.login("alice")
	e.clk.Advance(sessionLifetime)
	// "other" is used, then both expire; only the one that is looked up goes.
	_, err := e.svc.SessionActor(t.Context(), tok)
	_ = requireTag(t, err, app.TagUnauthenticated, "Accounts.SessionActor.Unauthenticated")
	if _, err := e.st.SessionByTokenHash(t.Context(), auth.HashToken(tok)); err == nil {
		t.Error("the expired session is still stored")
	}
	if _, err := e.st.SessionByTokenHash(t.Context(), auth.HashToken(other)); err != nil {
		t.Errorf("another session was deleted: %v", err)
	}
	if n, err := e.st.CountSessionsOfUser(t.Context(), alice.ID); err != nil || n != 1 {
		t.Errorf("sessions = %d, %v, want 1", n, err)
	}
	// Asking again is the same answer: the session is unknown now.
	_, err = e.svc.SessionActor(t.Context(), tok)
	appErr := requireTag(t, err, app.TagUnauthenticated, "Accounts.SessionActor.Unauthenticated")
	if appErr.Metadata["reason"] != "unknown" {
		t.Errorf("reason = %v, want unknown", appErr.Metadata["reason"])
	}
}

func TestLoginDeletesTheExpiredSessionsOfTheUser(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	alice := e.signUp("alice", store.RoleMember)
	bob := e.signUp("bob", store.RoleMember)
	old := e.login("alice")
	oldBob := e.login("bob")
	e.clk.Advance(20 * 24 * time.Hour)
	middle := e.login("alice")         // expires 20 days after "old"
	e.clk.Advance(11 * 24 * time.Hour) // "old" and "oldBob" are expired now, "middle" is not
	fresh := e.login("alice")

	has := func(tok string) bool {
		_, err := e.st.SessionByTokenHash(t.Context(), auth.HashToken(tok))
		return err == nil
	}
	if has(old) {
		t.Error("the expired session of the user survived the login")
	}
	if !has(middle) || !has(fresh) {
		t.Error("a live session of the user was deleted")
	}
	if !has(oldBob) {
		t.Error("the expired session of another user was deleted")
	}
	if n, err := e.st.CountSessionsOfUser(t.Context(), alice.ID); err != nil || n != 2 {
		t.Errorf("sessions of alice = %d, %v, want 2", n, err)
	}
	if n, err := e.st.CountSessionsOfUser(t.Context(), bob.ID); err != nil || n != 1 {
		t.Errorf("sessions of bob = %d, %v, want 1", n, err)
	}
}
