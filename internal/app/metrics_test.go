package app_test

import (
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/metrics"
	"github.com/rm4n0s/clowder/internal/store"
)

// TestLoginsAndCodesAreCounted walks every result of a login (SPEC D60) and
// checks the counter of each, and the counter of the SSH codes issued.
func TestLoginsAndCodesAreCounted(t *testing.T) {
	t.Parallel()
	set, err := metrics.NewSet(metrics.SetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	e.svc = app.NewAccounts(e.st, e.clk, e.hasher, app.AccountsConfig{
		InvitationLifetime: lifetime, SessionLifetime: sessionLifetime, SSHServer: true, Logins: set.Logins,
	})
	alice := e.signUp("alice", store.RoleMember)
	e.signUp("bob", store.RoleMember)
	keys := app.NewSSHKeys(e.st, e.clk, e.svc, app.SSHServerInfo{Addr: ":2222", PublicHost: "chat.example.org", HostFingerprint: "SHA256:host"})
	sign := app.NewSignIn(e.st, e.clk, nil, app.WithCodesCounter(set.SSHCodes))
	ctx := t.Context()
	login := func(username, password string, kind app.SessionKind, code string) (app.LoginResult, error) {
		return e.svc.LoginClient(ctx, app.ClientLogin{
			Username: username, Password: password, ClientIP: loginIP, Kind: kind, ClientName: "test", SSHCode: code,
		})
	}
	want := map[string]uint64{}
	check := func(step string) {
		t.Helper()
		for _, r := range []string{metrics.LoginOK, metrics.LoginWrong, metrics.LoginLimited, metrics.LoginSSHCodeRequired, metrics.LoginSSHCodeWrong} {
			if got := set.Logins.With(r).Value(); got != want[r] {
				t.Errorf("%s: logins{%s} = %d, want %d", step, r, got, want[r])
			}
		}
	}

	if _, err := login("alice", "not the password", app.SessionBrowser, ""); err == nil {
		t.Fatal("a wrong password logged in")
	}
	want[metrics.LoginWrong]++
	check("wrong password")
	if _, err := login("nobody", goodPassword, app.SessionBrowser, ""); err == nil {
		t.Fatal("an unknown user logged in")
	}
	want[metrics.LoginWrong]++
	check("unknown user")
	if _, err := login("alice", goodPassword, app.SessionBrowser, ""); err != nil {
		t.Fatal(err)
	}
	want[metrics.LoginOK]++
	check("right password")

	// Alice turns the SSH code on.
	line := newEd25519Line(t)
	k, err := keys.Add(ctx, actor(alice), "laptop", line, goodPassword, loginIP)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := sign.IssueCode(ctx, "alice", k.Fingerprint, "203.0.113.5")
	if err != nil {
		t.Fatal(err)
	}
	if got := set.SSHCodes.Value(); got != 1 {
		t.Errorf("codes issued = %d, want 1", got)
	}
	if _, err := sign.IssueCode(ctx, "alice", "SHA256:unknown", "203.0.113.5"); err == nil {
		t.Fatal("a code was issued for an unknown key")
	}
	if got := set.SSHCodes.Value(); got != 1 {
		t.Errorf("a refused code was counted: %d, want 1", got)
	}
	if err := keys.EnableSSHCode(ctx, actor(alice), goodPassword, loginIP); err != nil {
		t.Fatal(err)
	}

	// The browser login now stops at the code.
	res, err := login("alice", goodPassword, app.SessionBrowser, "")
	if err != nil || res.Pending == nil {
		t.Fatalf("login = %+v, %v; want a pending login", res, err)
	}
	want[metrics.LoginSSHCodeRequired]++
	check("pending")
	if _, err := e.svc.CompleteLogin(ctx, res.Pending.Token, "AAAA-AAAA", loginIP, "test"); err == nil {
		t.Fatal("a wrong code logged in")
	}
	want[metrics.LoginSSHCodeWrong]++
	check("wrong code on the page")
	if _, err := e.svc.CompleteLogin(ctx, res.Pending.Token, issued.Formatted(), loginIP, "test"); err != nil {
		t.Fatal(err)
	}
	want[metrics.LoginOK]++
	check("right code on the page")

	// The API: no code, then a wrong one.
	if _, err := login("alice", goodPassword, app.SessionAPI, ""); err == nil {
		t.Fatal("an API login without the code worked")
	}
	want[metrics.LoginSSHCodeRequired]++
	check("API without a code")
	if _, err := login("alice", goodPassword, app.SessionAPI, "BBBB-BBBB"); err == nil {
		t.Fatal("an API login with a wrong code worked")
	}
	want[metrics.LoginSSHCodeWrong]++
	check("API wrong code")

	// Bob fails five times, and the sixth is refused before the password is
	// looked at.
	// (from an address of his own, so that Alice's failures do not count)
	bobLogin := func(password string) error {
		_, err := e.svc.LoginClient(ctx, app.ClientLogin{
			Username: "bob", Password: password, ClientIP: otherIP, Kind: app.SessionBrowser,
		})
		return err
	}
	for range 5 {
		if err := bobLogin("not the password"); err == nil {
			t.Fatal("a wrong password logged in")
		}
		want[metrics.LoginWrong]++
	}
	if err := bobLogin(goodPassword); err == nil {
		t.Fatal("a limited login worked")
	}
	want[metrics.LoginLimited]++
	check("limited")

	// A request that failed before the limiter (a bad client name) counts as
	// nothing.
	if _, err := e.svc.LoginClient(ctx, app.ClientLogin{Username: "alice", Password: goodPassword, ClientIP: loginIP, Kind: app.SessionAPI}); err == nil {
		t.Fatal("an API login without a client name worked")
	}
	check("bad client name")
}

// TestCompleteLoginLimited checks that a blocked username is counted as
// limited on the second step of a login too.
func TestCompleteLoginLimited(t *testing.T) {
	t.Parallel()
	set, err := metrics.NewSet(metrics.SetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := newCodeEnvOn(t, true)
	// newCodeEnvOn made its own Accounts; use a second one on the same store
	// and clock that counts.
	counting := app.NewAccounts(c.st, c.clk, c.hasher, app.AccountsConfig{
		InvitationLifetime: lifetime, SessionLifetime: sessionLifetime, SSHServer: true, Logins: set.Logins,
	})
	c.turnOn(c.alice)
	res, err := counting.LoginClient(t.Context(), app.ClientLogin{
		Username: "alice", Password: goodPassword, ClientIP: loginIP, Kind: app.SessionBrowser,
	})
	if err != nil || res.Pending == nil {
		t.Fatalf("login = %+v, %v", res, err)
	}
	for range 5 {
		if _, err := counting.LoginClient(t.Context(), app.ClientLogin{
			Username: "alice", Password: "not the password", ClientIP: loginIP, Kind: app.SessionBrowser,
		}); err == nil {
			t.Fatal("a wrong password logged in")
		}
	}
	if _, err := counting.CompleteLogin(t.Context(), res.Pending.Token, "AAAA-AAAA", loginIP, "test"); err == nil {
		t.Fatal("a limited second step worked")
	}
	if got := set.Logins.With(metrics.LoginLimited).Value(); got != 1 {
		t.Errorf("logins{limited} = %d, want 1", got)
	}
	if got := set.Logins.With(metrics.LoginSSHCodeWrong).Value(); got != 0 {
		t.Errorf("logins{ssh_code_wrong} = %d, want 0", got)
	}
}
