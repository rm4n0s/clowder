package app_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
)

// resetEnv is a profileEnv with a second admin whose account gets disabled,
// for the permission table.
type resetEnv struct {
	*profileEnv
	disabledAdmin app.User
}

func newResetEnv(t *testing.T) *resetEnv {
	t.Helper()
	p := newProfileEnv(t)
	r := &resetEnv{profileEnv: p, disabledAdmin: p.signUp("zoe", store.RoleAdmin)}
	if err := p.svc.SetDisabled(t.Context(), actor(p.admin), r.disabledAdmin.ID, true); err != nil {
		t.Fatalf("SetDisabled: %+v", err)
	}
	p.rec.Reset()
	return r
}

// resetLinkEvents returns the audit events of the reset links.
func (r *resetEnv) resetEvents() []store.AuditEvent {
	r.t.Helper()
	var out []store.AuditEvent
	for _, ev := range r.auditUserEvents() {
		if ev.Action == store.ActionUserResetLinkCreated || ev.Action == store.ActionUserPasswordReset {
			out = append(out, ev)
		}
	}
	return out
}

func TestCreateResetLink(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		actor     func(r *resetEnv) app.Actor
		target    func(r *resetEnv) uint64
		wantTag   string
		wantRoute string
	}{
		{
			name:   "admin for a member",
			actor:  func(r *resetEnv) app.Actor { return actor(r.admin) },
			target: func(r *resetEnv) uint64 { return r.alice.ID },
		},
		{
			name:   "admin for themselves",
			actor:  func(r *resetEnv) app.Actor { return actor(r.admin) },
			target: func(r *resetEnv) uint64 { return r.admin.ID },
		},
		{
			name:    "member",
			actor:   func(r *resetEnv) app.Actor { return actor(r.alice) },
			target:  func(r *resetEnv) uint64 { return r.bob.ID },
			wantTag: app.TagForbidden, wantRoute: "Accounts.CreateResetLink.Forbidden",
		},
		{
			name:    "member for themselves",
			actor:   func(r *resetEnv) app.Actor { return actor(r.alice) },
			target:  func(r *resetEnv) uint64 { return r.alice.ID },
			wantTag: app.TagForbidden, wantRoute: "Accounts.CreateResetLink.Forbidden",
		},
		{
			name:    "admin whose account is disabled",
			actor:   func(r *resetEnv) app.Actor { return actor(r.disabledAdmin) },
			target:  func(r *resetEnv) uint64 { return r.alice.ID },
			wantTag: app.TagUnauthenticated, wantRoute: "Accounts.CreateResetLink.func1->Accounts.activeUserOn.Unauthenticated",
		},
		{
			name: "an actor whose role was taken away since",
			actor: func(r *resetEnv) app.Actor {
				return app.Actor{UserID: r.bob.ID, Username: "bob", Role: store.RoleAdmin}
			},
			target:  func(r *resetEnv) uint64 { return r.alice.ID },
			wantTag: app.TagForbidden, wantRoute: "Accounts.CreateResetLink.func1.Forbidden",
		},
		{
			name:    "target is disabled",
			actor:   func(r *resetEnv) app.Actor { return actor(r.admin) },
			target:  func(r *resetEnv) uint64 { return r.disabled.ID },
			wantTag: app.TagConflict, wantRoute: "Accounts.CreateResetLink.func1.Conflict",
		},
		{
			name:    "target is unknown",
			actor:   func(r *resetEnv) app.Actor { return actor(r.admin) },
			target:  func(*resetEnv) uint64 { return 9999 },
			wantTag: app.TagNotFound, wantRoute: "Accounts.CreateResetLink.func1->Store.UserByID.NotFound",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := newResetEnv(t)
			got, err := r.svc.CreateResetLink(t.Context(), tt.actor(r), tt.target(r))
			if tt.wantTag != "" {
				_ = requireTag(t, err, tt.wantTag, tt.wantRoute)
				if got.Token != "" {
					t.Errorf("a refused call returned a token")
				}
				if n := len(r.resetEvents()); n != 0 {
					t.Errorf("a refused call wrote %d audit events", n)
				}
				return
			}
			if err != nil {
				t.Fatalf("CreateResetLink: %+v", err)
			}
			target := tt.target(r)
			if got.Token == "" || got.UserID != target || got.ExpiresAt != r.clk.Now().Add(app.ResetLinkLifetime) {
				t.Errorf("link = %+v", got)
			}
			if err := r.svc.ResetLinkValid(t.Context(), got.Token); err != nil {
				t.Errorf("ResetLinkValid: %+v", err)
			}
			// The database keeps the hash of the token, never the token.
			row, err := r.st.PasswordResetByTokenHash(t.Context(), auth.HashToken(got.Token))
			if err != nil {
				t.Fatal(err)
			}
			a := tt.actor(r)
			if row.TokenHash == got.Token || row.UserID != target || row.CreatedBy == nil || *row.CreatedBy != a.UserID || row.UsedAt != nil {
				t.Errorf("row = %+v", row)
			}
			events := r.resetEvents()
			if len(events) != 1 {
				t.Fatalf("%d audit events, want 1", len(events))
			}
			ev := events[0]
			if ev.Action != store.ActionUserResetLinkCreated || ev.ActorID != a.UserID || ev.EntityID != target ||
				ev.EntityType != store.EntityUser || ev.RoomID != store.NoRoom ||
				!strings.Contains(ev.Summary, "made a password reset link for") ||
				strings.Contains(ev.Summary+ev.Details, got.Token) {
				t.Errorf("audit event = %+v", ev)
			}
		})
	}
}

func TestCreateSystemResetLink(t *testing.T) {
	t.Parallel()
	t.Run("a link", func(t *testing.T) {
		t.Parallel()
		r := newResetEnv(t)
		got, err := r.svc.CreateSystemResetLink(t.Context(), "  BOB ")
		if err != nil {
			t.Fatalf("CreateSystemResetLink: %+v", err)
		}
		if got.UserID != r.bob.ID || got.Username != "bob" || got.ExpiresAt != r.clk.Now().Add(app.ResetLinkLifetime) {
			t.Errorf("link = %+v", got)
		}
		row, err := r.st.PasswordResetByTokenHash(t.Context(), auth.HashToken(got.Token))
		if err != nil || row.CreatedBy != nil || row.UserID != r.bob.ID {
			t.Errorf("row = %+v, %v", row, err)
		}
		events := r.resetEvents()
		if len(events) != 1 {
			t.Fatalf("%d audit events, want 1", len(events))
		}
		// The command line has no user: the event names the user of the link.
		ev := events[0]
		if ev.Action != store.ActionUserResetLinkCreated || ev.ActorID != r.bob.ID || ev.EntityID != r.bob.ID ||
			ev.Summary != "A password reset link for Bob was made on the command line" ||
			!strings.Contains(ev.Details, `"via":"command line"`) || strings.Contains(ev.Summary+ev.Details, got.Token) {
			t.Errorf("audit event = %+v", ev)
		}
		if err := r.svc.ResetLinkValid(t.Context(), got.Token); err != nil {
			t.Errorf("ResetLinkValid: %+v", err)
		}
	})
	failures := []struct {
		name, username, wantRoute, wantTag string
	}{
		{"unknown user", "nobody", "Accounts.CreateSystemResetLink.func1.NotFound", app.TagNotFound},
		{"empty username", "  ", "Accounts.CreateSystemResetLink.func1.NotFound", app.TagNotFound},
		{"disabled user", "dina", "Accounts.CreateSystemResetLink.func1.Conflict", app.TagConflict},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := newResetEnv(t)
			_, err := r.svc.CreateSystemResetLink(t.Context(), tt.username)
			_ = requireTag(t, err, tt.wantTag, tt.wantRoute)
			if n := len(r.resetEvents()); n != 0 {
				t.Errorf("a refused call wrote %d audit events", n)
			}
		})
	}
}

func TestResetLinkLife(t *testing.T) {
	t.Parallel()
	const notFound = "Accounts.ResetPassword->Accounts.usableResetLink"
	tests := []struct {
		name string
		// prepare returns the token to use.
		prepare   func(r *resetEnv) string
		wantRoute string // "" for success
	}{
		{
			name:    "valid",
			prepare: func(r *resetEnv) string { return r.link(r.alice.ID) },
		},
		{
			name: "valid until the last second",
			prepare: func(r *resetEnv) string {
				token := r.link(r.alice.ID)
				r.clk.Advance(app.ResetLinkLifetime - time.Second)
				return token
			},
		},
		{
			name: "expired",
			prepare: func(r *resetEnv) string {
				token := r.link(r.alice.ID)
				r.clk.Advance(app.ResetLinkLifetime)
				return token
			},
			wantRoute: notFound + ".NotFound",
		},
		{
			name: "used",
			prepare: func(r *resetEnv) string {
				token := r.link(r.alice.ID)
				if _, err := r.svc.ResetPassword(r.t.Context(), app.ResetInput{Token: token, NewPassword: newPassword}); err != nil {
					r.t.Fatalf("first ResetPassword: %+v", err)
				}
				return token
			},
			wantRoute: notFound + ".NotFound",
		},
		{
			name: "replaced by a newer link",
			prepare: func(r *resetEnv) string {
				token := r.link(r.alice.ID)
				r.clk.Advance(time.Minute)
				r.link(r.alice.ID)
				return token
			},
			wantRoute: notFound + "->Store.PasswordResetByTokenHash.NotFound",
		},
		{
			name:      "unknown",
			prepare:   func(*resetEnv) string { return "no-such-token" },
			wantRoute: notFound + "->Store.PasswordResetByTokenHash.NotFound",
		},
		{
			name:      "empty",
			prepare:   func(*resetEnv) string { return "" },
			wantRoute: notFound + "->Store.PasswordResetByTokenHash.NotFound",
		},
		{
			name: "the user was disabled after the link was made",
			prepare: func(r *resetEnv) string {
				token := r.link(r.alice.ID)
				if err := r.svc.SetDisabled(r.t.Context(), actor(r.admin), r.alice.ID, true); err != nil {
					r.t.Fatalf("SetDisabled: %+v", err)
				}
				return token
			},
			wantRoute: notFound + ".NotFound",
		},
		{
			name: "the link of another user is not touched by a newer link",
			prepare: func(r *resetEnv) string {
				token := r.link(r.alice.ID)
				r.link(r.bob.ID)
				return token
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := newResetEnv(t)
			token := tt.prepare(r)
			before := len(r.resetEvents())

			valid := r.svc.ResetLinkValid(t.Context(), token)
			res, err := r.svc.ResetPassword(t.Context(), app.ResetInput{Token: token, NewPassword: newPassword, ClientName: "Firefox"})
			if tt.wantRoute != "" {
				_ = requireTag(t, valid, app.TagNotFound, "Accounts.ResetLinkValid->Accounts.usableResetLink")
				_ = requireTag(t, err, app.TagNotFound, tt.wantRoute)
				if res.Token != "" {
					t.Error("a refused reset returned a session")
				}
				if n := len(r.resetEvents()); n != before {
					t.Errorf("a refused reset wrote %d audit events", n-before)
				}
				return
			}
			if valid != nil {
				t.Errorf("ResetLinkValid: %+v", valid)
			}
			if err != nil {
				t.Fatalf("ResetPassword: %+v", err)
			}
			if res.User.ID != r.alice.ID {
				t.Errorf("user = %+v", res.User)
			}
			// Used once.
			_, err = r.svc.ResetPassword(t.Context(), app.ResetInput{Token: token, NewPassword: "another new passphrase"})
			_ = requireTag(t, err, app.TagNotFound, "Accounts.ResetPassword->Accounts.usableResetLink.NotFound")
		})
	}
}

// link makes a reset link for the user as the admin and returns its token.
func (r *resetEnv) link(userID uint64) string {
	r.t.Helper()
	got, err := r.svc.CreateResetLink(r.t.Context(), actor(r.admin), userID)
	if err != nil {
		r.t.Fatalf("CreateResetLink: %+v", err)
	}
	return got.Token
}

func TestResetPassword(t *testing.T) {
	t.Parallel()
	r := newResetEnv(t)
	ctx := t.Context()
	browser := r.loginAs("alice", app.SessionBrowser, "Firefox")
	other := r.loginAs("alice", app.SessionBrowser, "Chrome")
	client := r.loginAs("alice", app.SessionAPI, "Desktop")
	bobSession := r.loginAs("bob", app.SessionBrowser, "Bob's")
	bobClient := r.loginAs("bob", app.SessionAPI, "Bob's app")
	// Five failed logins block the username; a reset starts clean.
	for range 5 {
		_, _, _ = r.svc.Login(ctx, "alice", "not the password", loginIP)
	}
	if _, _, err := r.svc.Login(ctx, "alice", goodPassword, loginIP); err == nil {
		t.Fatal("the limiter did not block the username")
	}
	token := r.link(r.alice.ID)
	r.rec.Reset()
	r.clk.Advance(time.Hour)

	res, err := r.svc.ResetPassword(ctx, app.ResetInput{Token: token, NewPassword: newPassword, ClientName: "Safari\x00 on a phone"})
	if err != nil {
		t.Fatalf("ResetPassword: %+v", err)
	}

	for name, tok := range map[string]string{"browser": browser.Token, "other browser": other.Token} {
		if _, err := r.svc.SessionActor(ctx, tok); err == nil {
			t.Errorf("the %s session still works", name)
		}
	}
	if _, err := r.svc.APISessionActor(ctx, client.Token); err == nil {
		t.Error("the API session still works")
	}
	if _, err := r.svc.SessionActor(ctx, bobSession.Token); err != nil {
		t.Errorf("the browser session of another user ended: %+v", err)
	}
	if _, err := r.svc.APISessionActor(ctx, bobClient.Token); err != nil {
		t.Errorf("the API session of another user ended: %+v", err)
	}

	// The new session is a browser session of the user, named by the client.
	sess, err := r.svc.SessionActor(ctx, res.Token)
	if err != nil {
		t.Fatalf("the new session: %+v", err)
	}
	if sess.User.ID != r.alice.ID || res.User.ID != r.alice.ID || !res.ExpiresAt.Equal(r.clk.Now().Add(sessionLifetime)) {
		t.Errorf("session = %+v, result = %+v", sess, res)
	}
	if _, err := r.svc.APISessionActor(ctx, res.Token); err == nil {
		t.Error("the new session works as an API token")
	}
	list, err := r.svc.Sessions(ctx, actor(r.alice), sess.SessionID)
	if err != nil || len(list) != 1 || list[0].ClientName != "Safari on a phone" || !list[0].Current {
		t.Errorf("sessions = %+v, %v", list, err)
	}

	// The new password works, the old one does not, and the limiter is clear.
	if _, _, err := r.svc.Login(ctx, "alice", newPassword, loginIP); err != nil {
		t.Errorf("login with the new password: %+v", err)
	}
	if _, _, err := r.svc.Login(ctx, "alice", goodPassword, loginIP); err == nil {
		t.Error("login with the old password still works")
	}

	events := r.rec.Events()
	if len(events) != 1 || events[0].Kind != live.KindUserChanged || events[0].Topic != live.UserTopic(r.alice.ID) {
		t.Errorf("events = %+v, want one user.changed on the user's topic", events)
	}
	audit := r.resetEvents()
	if len(audit) != 2 {
		t.Fatalf("%d audit events, want 2", len(audit))
	}
	// Newest first.
	if a := audit[0]; a.Action != store.ActionUserPasswordReset || a.ActorID != r.alice.ID || a.EntityID != r.alice.ID ||
		a.EntityType != store.EntityUser || a.RoomID != store.NoRoom || a.Details != "{}" ||
		a.Summary != "Alice set a new password with a reset link" ||
		strings.Contains(a.Summary+a.Details, newPassword) || strings.Contains(a.Summary+a.Details, token) {
		t.Errorf("audit event = %+v", a)
	}
	if a := audit[1]; a.Action != store.ActionUserResetLinkCreated || a.ActorID != r.admin.ID {
		t.Errorf("audit event = %+v", a)
	}
}

func TestResetPasswordFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		password string
		route    string
		wantTag  string
	}{
		{"too short", "short", "Accounts.ResetPassword.InvalidInput", app.TagInvalidInput},
		{"eleven characters", strings.Repeat("a", 11), "Accounts.ResetPassword.InvalidInput", app.TagInvalidInput},
		{"empty", "", "Accounts.ResetPassword.InvalidInput", app.TagInvalidInput},
		{"too long", strings.Repeat("a", 1025), "Accounts.ResetPassword.InvalidInput", app.TagInvalidInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := newResetEnv(t)
			ctx := t.Context()
			session := r.loginAs("alice", app.SessionBrowser, "Firefox")
			token := r.link(r.alice.ID)
			r.rec.Reset()
			before := len(r.resetEvents())

			_, err := r.svc.ResetPassword(ctx, app.ResetInput{Token: token, NewPassword: tt.password})
			appErr := requireTag(t, err, tt.wantTag, tt.route)
			if f := fieldsOf(t, appErr); f[app.FieldPassword] == "" {
				t.Errorf("fields = %v, want %q", f, app.FieldPassword)
			}
			// Nothing changed: the link, the password and the sessions.
			if err := r.svc.ResetLinkValid(ctx, token); err != nil {
				t.Errorf("the link was used up: %+v", err)
			}
			if _, err := r.svc.SessionActor(ctx, session.Token); err != nil {
				t.Errorf("a failed reset ended a session: %+v", err)
			}
			if _, _, err := r.svc.Login(ctx, "alice", goodPassword, loginIP); err != nil {
				t.Errorf("the old password stopped working: %+v", err)
			}
			if n := len(r.resetEvents()); n != before {
				t.Errorf("a failed reset wrote %d audit events", n-before)
			}
			if ev := r.rec.Events(); len(ev) != 0 {
				t.Errorf("a failed reset published %+v", ev)
			}
		})
	}

	t.Run("the rule of the sign-up holds at 12 characters and 1024 bytes", func(t *testing.T) {
		t.Parallel()
		for _, pw := range []string{strings.Repeat("a", 12), strings.Repeat("é", 12), strings.Repeat("a", 1024)} {
			r := newResetEnv(t)
			if _, err := r.svc.ResetPassword(t.Context(), app.ResetInput{Token: r.link(r.alice.ID), NewPassword: pw}); err != nil {
				t.Errorf("a password of %d bytes was refused: %+v", len(pw), err)
			}
		}
	})
}

func TestResetPasswordUsedOnceAtATime(t *testing.T) {
	t.Parallel()
	r := newResetEnv(t)
	token := r.link(r.alice.ID)
	const tries = 4
	results := make([]error, tries)
	var wg sync.WaitGroup
	for i := range tries {
		wg.Go(func() {
			_, results[i] = r.svc.ResetPassword(r.t.Context(), app.ResetInput{Token: token, NewPassword: newPassword + string(rune('a'+i))})
		})
	}
	wg.Wait()
	ok := 0
	for _, err := range results {
		if err == nil {
			ok++
			continue
		}
		_ = requireTag(t, err, app.TagNotFound, "Accounts.ResetPassword")
	}
	if ok != 1 {
		t.Errorf("%d resets worked, want exactly 1", ok)
	}
	var passwordEvents int
	for _, ev := range r.resetEvents() {
		if ev.Action == store.ActionUserPasswordReset {
			passwordEvents++
		}
	}
	if passwordEvents != 1 {
		t.Errorf("%d user.password_reset events, want 1", passwordEvents)
	}
}
