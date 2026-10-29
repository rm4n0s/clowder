package app_test

import (
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/metrics"
	"github.com/rm4n0s/clowder/internal/store"
)

// signUpFrom is a sign-up from the address with the token.
func (e *env) signUpFrom(ip, token, username string) error {
	e.t.Helper()
	_, err := e.svc.SignUp(e.t.Context(), token, username, "Name", goodPassword, app.FromAddress(ip))
	return err
}

// TestSignUpCountsInvalidInvitations shows the limit of SPEC D62: a sign-up
// with an unknown, used or expired invitation counts as a failed login of the
// address, five of them block the address, and then both the sign-up and a
// login from it are 429. A valid invitation never counts.
func TestSignUpCountsInvalidInvitations(t *testing.T) {
	t.Parallel()
	const attacker = "203.0.113.9"
	tests := []struct {
		name  string
		token func(e *env) string
		tag   string
		route string
	}{
		{"unknown", func(*env) string { return "no such token" }, app.TagNotFound,
			"Accounts.SignUp->Accounts.usableInvitation->Store.InvitationByTokenHash.NotFound"},
		{"used", func(e *env) string {
			inv, err := e.svc.CreateSystemInvitation(e.t.Context(), store.RoleMember)
			if err != nil {
				e.t.Fatal(err)
			}
			if _, err := e.svc.SignUp(e.t.Context(), inv.Token, "first", "First", goodPassword); err != nil {
				e.t.Fatal(err)
			}
			return inv.Token
		}, app.TagNotFound, "Accounts.SignUp->Accounts.usableInvitation.NotFound"},
		{"expired", func(e *env) string {
			inv, err := e.svc.CreateSystemInvitation(e.t.Context(), store.RoleMember)
			if err != nil {
				e.t.Fatal(err)
			}
			e.clk.Advance(lifetime)
			return inv.Token
		}, app.TagNotFound, "Accounts.SignUp->Accounts.usableInvitation.NotFound"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			set, err := metrics.NewSet(metrics.SetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			e := newEnv(t)
			e.svc = app.NewAccounts(e.st, e.clk, e.hasher, app.AccountsConfig{
				InvitationLifetime: lifetime, SessionLifetime: sessionLifetime,
				SignUpsRefused: set.LimitsRefused.With(metrics.LimitSignUp),
			})
			e.signUp("alice", store.RoleMember)
			token := tt.token(e)
			for i := 1; i <= 5; i++ {
				_ = requireTag(t, e.signUpFrom(attacker, token, "mallory"), tt.tag, tt.route)
				if i < 5 {
					// Still within the limit: the login of the address works.
					if _, err := e.svc.Login(t.Context(), "alice", goodPassword, attacker); err != nil {
						t.Fatalf("after %d failures the login was refused: %+v", i, err)
					}
				}
			}
			// The sixth is refused before the invitation is looked at, and so
			// is a login from the address, even with the right password.
			err = e.signUpFrom(attacker, token, "mallory")
			_ = requireTag(t, err, app.TagRateLimited, "Accounts.SignUp.RateLimited")
			_, err = e.svc.Login(t.Context(), "alice", goodPassword, attacker)
			_ = requireTag(t, err, app.TagRateLimited, "Accounts.Login->Accounts.LoginClient.RateLimited")
			// A valid invitation from the blocked address is refused too.
			valid, err := e.svc.CreateSystemInvitation(t.Context(), store.RoleMember)
			if err != nil {
				t.Fatal(err)
			}
			err = e.signUpFrom(attacker, valid.Token, "bob")
			_ = requireTag(t, err, app.TagRateLimited, "Accounts.SignUp.RateLimited")
			// Another address is not blocked.
			if err := e.signUpFrom("198.51.100.4", valid.Token, "bob"); err != nil {
				t.Errorf("another address refused: %+v", err)
			}
			if got := set.LimitsRefused.With(metrics.LimitSignUp).Value(); got != 2 {
				t.Errorf("refusals counted = %d, want 2", got)
			}
			// The window passes, and the address may go on.
			e.clk.Advance(app.LoginWindow)
			if _, err := e.svc.Login(t.Context(), "alice", goodPassword, attacker); err != nil {
				t.Errorf("login after the window: %+v", err)
			}
		})
	}
}

// TestSignUpWithAValidInvitationDoesNotCount shows that mistakes in the form
// of a valid invitation, and sign-ups that succeed, never add to the failures
// of the address, and that a call without an address counts nothing.
func TestSignUpWithAValidInvitationDoesNotCount(t *testing.T) {
	t.Parallel()
	const ip = "203.0.113.20"
	e := newEnv(t)
	inv, err := e.svc.CreateSystemInvitation(t.Context(), store.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	for range 12 {
		// A password that is too short: 422, and the invitation is still valid.
		_, err := e.svc.SignUp(t.Context(), inv.Token, "carol", "Carol", "short", app.FromAddress(ip))
		_ = requireTag(t, err, app.TagInvalidInput, "Accounts.SignUp.InvalidInput")
	}
	if err := e.signUpFrom(ip, inv.Token, "carol"); err != nil {
		t.Fatalf("a valid sign-up after 12 mistakes was refused: %+v", err)
	}
	// Twelve sign-ups with no address, all invalid, count nothing.
	for range 12 {
		_, err := e.svc.SignUp(t.Context(), "no such token", "dave", "Dave", goodPassword)
		_ = requireTag(t, err, app.TagNotFound, "Accounts.SignUp->Accounts.usableInvitation")
	}
	if _, err := e.svc.Login(t.Context(), "carol", goodPassword, ip); err != nil {
		t.Errorf("login refused: %+v", err)
	}
	// Four invalid invitations from the address are still within the limit.
	for range 4 {
		_ = requireTag(t, e.signUpFrom(ip, "no such token", "dave"), app.TagNotFound, "Accounts.SignUp")
	}
	if _, err := e.svc.Login(t.Context(), "carol", goodPassword, ip); err != nil {
		t.Errorf("login refused after four failures: %+v", err)
	}
}

// TestSignUpFailuresAreSharedThroughRedis has two services, as two processes
// have, count the invalid sign-ups of one address in the same Redis: three
// through one and two through the other block the address on both, for the
// sign-up and for the login (SPEC D41, D62).
func TestSignUpFailuresAreSharedThroughRedis(t *testing.T) {
	t.Parallel()
	url := testRedisURL(t)
	prefix := uniquePrefix()
	e := newEnv(t)
	service := func() *app.Accounts {
		limiter := app.NewLoginLimiterOn(e.clk, openRedisCounter(t, url, prefix), nil)
		return app.NewAccounts(e.st, e.clk, e.hasher, app.AccountsConfig{
			InvitationLifetime: lifetime, SessionLifetime: sessionLifetime, Limiter: limiter,
		})
	}
	a, b := service(), service()
	e.svc = a
	e.signUp("alice", store.RoleMember)
	const ip = "203.0.113.77"
	try := func(svc *app.Accounts) error {
		_, err := svc.SignUp(t.Context(), "no such token", "mallory", "Mallory", goodPassword, app.FromAddress(ip))
		return err
	}
	for range 3 {
		_ = requireTag(t, try(a), app.TagNotFound, "Accounts.SignUp")
	}
	for range 2 {
		_ = requireTag(t, try(b), app.TagNotFound, "Accounts.SignUp")
	}
	_ = requireTag(t, try(a), app.TagRateLimited, "Accounts.SignUp.RateLimited")
	_ = requireTag(t, try(b), app.TagRateLimited, "Accounts.SignUp.RateLimited")
	_, err := b.Login(t.Context(), "alice", goodPassword, ip)
	_ = requireTag(t, err, app.TagRateLimited, "Accounts.Login->Accounts.LoginClient.RateLimited")
}
