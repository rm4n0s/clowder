package app_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
)

// otherIP is another client address than loginIP.
const otherIP = "198.51.100.77"

// codeEnv is an sshEnv whose accounts ask for the SSH code (the SSH server is
// on), where alice, bob and root have a key each.
type codeEnv struct {
	*sshEnv
	fp map[string]string // the fingerprint of the key of each username
}

func newCodeEnv(t *testing.T) *codeEnv {
	t.Helper()
	return newCodeEnvOn(t, true)
}

// newCodeEnvOn is newCodeEnv with the server on or off.
func newCodeEnvOn(t *testing.T, serverOn bool) *codeEnv {
	t.Helper()
	c := &codeEnv{sshEnv: newSSHEnvOn(t, serverOn), fp: map[string]string{}}
	if !serverOn {
		c.keys = app.NewSSHKeys(c.st, c.clk, c.svc, app.SSHServerInfo{})
	}
	for _, u := range []app.User{c.alice, c.bob, c.admin} {
		line := newEd25519Line(t)
		c.add(u, "laptop", line)
		c.fp[u.Username] = fingerprintOf(t, line)
	}
	return c
}

// fetch fetches a code for the user with their key, as the SSH server does,
// and returns it formatted with the dash.
func (c *codeEnv) fetch(u app.User) string {
	c.t.Helper()
	issued, err := c.sign.IssueCode(c.t.Context(), u.Username, c.fp[u.Username], "203.0.113.5")
	if err != nil {
		c.t.Fatalf("IssueCode %s: %+v", u.Username, err)
	}
	return issued.Formatted()
}

// turnOn fetches a code (which proves a key works) and turns the SSH code on
// for the user. The fetched code is still valid afterwards, until the next
// fetch.
func (c *codeEnv) turnOn(u app.User) {
	c.t.Helper()
	c.fetch(u)
	if err := c.keys.EnableSSHCode(c.t.Context(), actor(u), goodPassword, loginIP); err != nil {
		c.t.Fatalf("EnableSSHCode %s: %+v", u.Username, err)
	}
}

// pending logs the user in with the password and returns the pending login.
func (c *codeEnv) pending(u app.User, next string) app.PendingLogin {
	c.t.Helper()
	res, err := c.svc.LoginClient(c.t.Context(), app.ClientLogin{
		Username: u.Username, Password: goodPassword, ClientIP: loginIP, Kind: app.SessionBrowser, ClientName: "Firefox", Next: next,
	})
	if err != nil {
		c.t.Fatalf("LoginClient %s: %+v", u.Username, err)
	}
	if res.Pending == nil || res.Token != "" {
		c.t.Fatalf("LoginClient %s = %+v, want a pending login and no token", u.Username, res)
	}
	return *res.Pending
}

// sessions counts the sessions of the user.
func (c *codeEnv) sessions(u app.User) int64 {
	c.t.Helper()
	n, err := c.st.CountSessionsOfUser(c.t.Context(), u.ID)
	if err != nil {
		c.t.Fatalf("CountSessionsOfUser: %+v", err)
	}
	return n
}

// pendingRows counts the pending logins of the database.
func (c *codeEnv) pendingRows() int {
	c.t.Helper()
	n, err := c.st.CountPendingLogins(c.t.Context())
	if err != nil {
		c.t.Fatalf("CountPendingLogins: %+v", err)
	}
	return int(n)
}

// lowerWithDash writes a formatted code the way a careless person types it.
func lowerWithDash(code string) string {
	return strings.ToLower(strings.ReplaceAll(code, "-", "")[:4] + "-" + strings.ReplaceAll(code, "-", "")[4:])
}

// TestLoginAPIWithSSHCode is the login table of the API: the password and the
// code in one call.
func TestLoginAPIWithSSHCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		password string
		// code makes the code to send; it may fetch codes.
		code      func(c *codeEnv) string
		wantTag   string
		wantRoute string
	}{
		{name: "the right code", code: func(c *codeEnv) string { return c.fetch(c.alice) }},
		{name: "the code without the dash", code: func(c *codeEnv) string { return strings.ReplaceAll(c.fetch(c.alice), "-", "") }},
		{name: "the code with a dash, in lower case", code: func(c *codeEnv) string { return lowerWithDash(c.fetch(c.alice)) }},
		{name: "the code at its last second", code: func(c *codeEnv) string {
			code := c.fetch(c.alice)
			c.clk.Advance(59 * time.Second)
			return code
		}},
		{name: "the code of another key of the user", code: func(c *codeEnv) string {
			line := newEd25519Line(c.t)
			c.add(c.alice, "phone", line)
			issued, err := c.sign.IssueCode(c.t.Context(), "alice", fingerprintOf(c.t, line), "203.0.113.5")
			if err != nil {
				c.t.Fatal(err)
			}
			return issued.Code
		}},
		{name: "no code", code: func(c *codeEnv) string { return "" },
			wantTag: app.TagSSHCodeRequired, wantRoute: "Accounts.LoginClient.SSHCodeRequired"},
		{name: "a wrong code", code: func(c *codeEnv) string { c.fetch(c.alice); return "0000-0000" },
			wantTag: app.TagUnauthenticated, wantRoute: "Accounts.LoginClient.Unauthenticated"},
		{name: "a code of the wrong length", code: func(c *codeEnv) string { c.fetch(c.alice); return strings.Repeat("A", 5000) },
			wantTag: app.TagUnauthenticated, wantRoute: "Accounts.LoginClient.Unauthenticated"},
		{name: "an expired code", code: func(c *codeEnv) string {
			code := c.fetch(c.alice)
			c.clk.Advance(61 * time.Second)
			return code
		}, wantTag: app.TagUnauthenticated, wantRoute: "Accounts.LoginClient.Unauthenticated"},
		{name: "a used code", code: func(c *codeEnv) string {
			code := c.fetch(c.alice)
			if _, err := c.svc.LoginClient(c.t.Context(), app.ClientLogin{
				Username: "alice", Password: goodPassword, ClientIP: loginIP, Kind: app.SessionAPI, ClientName: "first", SSHCode: code,
			}); err != nil {
				c.t.Fatalf("first login: %+v", err)
			}
			return code
		}, wantTag: app.TagUnauthenticated, wantRoute: "Accounts.LoginClient.Unauthenticated"},
		{name: "a replaced code", code: func(c *codeEnv) string {
			first := c.fetch(c.alice)
			c.fetch(c.alice)
			return first
		}, wantTag: app.TagUnauthenticated, wantRoute: "Accounts.LoginClient.Unauthenticated"},
		{name: "the code of another user", code: func(c *codeEnv) string {
			c.fetch(c.alice)
			return c.fetch(c.bob)
		}, wantTag: app.TagUnauthenticated, wantRoute: "Accounts.LoginClient.Unauthenticated"},
		{name: "a wrong password with a right code", password: "not the password", code: func(c *codeEnv) string { return c.fetch(c.alice) },
			wantTag: app.TagInvalidInput, wantRoute: "Accounts.LoginClient.InvalidInput"},
		{name: "a wrong password and no code", password: "not the password", code: func(c *codeEnv) string { return "" },
			wantTag: app.TagInvalidInput, wantRoute: "Accounts.LoginClient.InvalidInput"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := newCodeEnv(t)
			c.turnOn(c.alice)
			password := tt.password
			if password == "" {
				password = goodPassword
			}
			code := tt.code(c)
			events := len(c.auditUserEvents())
			sessions := c.sessions(c.alice)

			res, err := c.svc.LoginClient(t.Context(), app.ClientLogin{
				Username: "alice", Password: password, ClientIP: loginIP, Kind: app.SessionAPI, ClientName: "Desktop", SSHCode: code,
			})
			if n := len(c.auditUserEvents()); n != events {
				t.Errorf("the login wrote %d audit events", n-events)
			}
			if tt.wantTag != "" {
				_ = requireTag(t, err, tt.wantTag, tt.wantRoute)
				if res.Token != "" || res.Pending != nil {
					t.Errorf("a refused login returned %+v", res)
				}
				if n := c.sessions(c.alice); n != sessions {
					t.Errorf("a refused login made %d sessions", n-sessions)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoginClient: %+v", err)
			}
			if res.Token == "" || res.Pending != nil || res.User.ID != c.alice.ID {
				t.Errorf("result = %+v", res)
			}
			if got, err := c.svc.APISessionActor(t.Context(), res.Token); err != nil || got.User.ID != c.alice.ID {
				t.Errorf("APISessionActor = %+v, %v", got, err)
			}
		})
	}
}

// TestLoginAPICodeIsUsedOnce checks that the code of a right login cannot log
// in a second time, and that a wrong password does not use the code up.
func TestLoginAPICodeIsUsedOnce(t *testing.T) {
	t.Parallel()
	c := newCodeEnv(t)
	c.turnOn(c.alice)
	code := c.fetch(c.alice)
	login := func(password string) error {
		_, err := c.svc.LoginClient(t.Context(), app.ClientLogin{
			Username: "alice", Password: password, ClientIP: loginIP, Kind: app.SessionAPI, ClientName: "Desktop", SSHCode: code,
		})
		return err
	}
	if err := login("not the password"); err == nil {
		t.Fatal("a wrong password logged in")
	}
	if err := login(goodPassword); err != nil {
		t.Fatalf("the code was used by a login with a wrong password: %+v", err)
	}
	_ = requireTag(t, login(goodPassword), app.TagUnauthenticated, "Accounts.LoginClient.Unauthenticated")
}

// TestLoginServerOffAsksForPasswordOnly checks that a user who turned the SSH
// code on logs in with the password alone while this process runs no SSH
// server, in the pages and the API (SPEC §7.16).
func TestLoginServerOffAsksForPasswordOnly(t *testing.T) {
	t.Parallel()
	on := newCodeEnv(t)
	on.turnOn(on.alice)
	off := app.NewAccounts(on.st, on.clk, auth.FastHasher(), app.AccountsConfig{SessionLifetime: sessionLifetime})
	for _, kind := range []app.SessionKind{app.SessionBrowser, app.SessionAPI} {
		res, err := off.LoginClient(t.Context(), app.ClientLogin{
			Username: "alice", Password: goodPassword, ClientIP: loginIP, Kind: kind, ClientName: "x",
		})
		if err != nil || res.Token == "" || res.Pending != nil {
			t.Errorf("%s login with the server off = %+v, %v, want a session", kind, res, err)
		}
	}
	// With the server on, the same user is asked.
	_ = on.pending(on.alice, "")
}

// TestLoginWithoutTheCodeOn checks that a user who did not turn the code on is
// never asked for it, whatever keys they have.
func TestLoginWithoutTheCodeOn(t *testing.T) {
	t.Parallel()
	c := newCodeEnv(t)
	c.fetch(c.alice)
	for _, kind := range []app.SessionKind{app.SessionBrowser, app.SessionAPI} {
		res, err := c.svc.LoginClient(t.Context(), app.ClientLogin{
			Username: "alice", Password: goodPassword, ClientIP: loginIP, Kind: kind, ClientName: "x",
		})
		if err != nil || res.Token == "" || res.Pending != nil {
			t.Errorf("%s login = %+v, %v, want a session", kind, res, err)
		}
	}
}

// TestLoginMakesAPendingLogin checks what the browser login stores and returns
// for a user who needs the code: no session, a token that is stored as its
// SHA-256, five minutes, and the path to go to.
func TestLoginMakesAPendingLogin(t *testing.T) {
	t.Parallel()
	c := newCodeEnv(t)
	c.turnOn(c.alice)
	p := c.pending(c.alice, "/rooms/3")
	if p.Username != "alice" || p.Next != "/rooms/3" || !p.ExpiresAt.Equal(start.Add(5*time.Minute)) || len(p.Token) < 40 {
		t.Errorf("pending = %+v", p)
	}
	if n := c.sessions(c.alice); n != 0 {
		t.Errorf("%d sessions before the code", n)
	}
	row, err := c.st.PendingLoginByTokenHash(t.Context(), auth.HashToken(p.Token))
	if err != nil {
		t.Fatalf("PendingLoginByTokenHash: %+v", err)
	}
	if row.UserID != c.alice.ID || row.TokenHash == p.Token || row.WrongTries != 0 || row.Next != "/rooms/3" {
		t.Errorf("row = %+v", row)
	}
	info, err := c.svc.PendingLoginInfo(t.Context(), p.Token)
	if err != nil || info.Token != "" || info.Username != "alice" || info.Next != "/rooms/3" {
		t.Errorf("PendingLoginInfo = %+v, %v", info, err)
	}

	// Expired pending logins are deleted when a new one is made.
	c.clk.Advance(6 * time.Minute)
	_ = c.pending(c.alice, "")
	if n := c.pendingRows(); n != 1 {
		t.Errorf("%d pending logins, want only the new one", n)
	}
}

func TestCompleteLogin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// code prepares the codes and returns what is typed.
		code      func(c *codeEnv) string
		wantTag   string
		wantRoute string
	}{
		{name: "the right code", code: func(c *codeEnv) string { return c.fetch(c.alice) }},
		{name: "the code typed in lower case with a dash", code: func(c *codeEnv) string { return lowerWithDash(c.fetch(c.alice)) }},
		{name: "the code typed with spaces around it", code: func(c *codeEnv) string { return "  " + c.fetch(c.alice) + "\n" }},
		{name: "the code of another key of the user", code: func(c *codeEnv) string {
			line := newEd25519Line(c.t)
			c.add(c.alice, "phone", line)
			issued, err := c.sign.IssueCode(c.t.Context(), "alice", fingerprintOf(c.t, line), "203.0.113.5")
			if err != nil {
				c.t.Fatal(err)
			}
			return issued.Code
		}},
		{name: "no code", code: func(c *codeEnv) string { c.fetch(c.alice); return "" },
			wantTag: app.TagSSHCodeWrong, wantRoute: "Accounts.CompleteLogin.SSHCodeWrong"},
		{name: "a wrong code", code: func(c *codeEnv) string { c.fetch(c.alice); return "0000-0000" },
			wantTag: app.TagSSHCodeWrong, wantRoute: "Accounts.CompleteLogin.SSHCodeWrong"},
		{name: "a code that is too long", code: func(c *codeEnv) string { c.fetch(c.alice); return strings.Repeat("A", 9000) },
			wantTag: app.TagSSHCodeWrong, wantRoute: "Accounts.CompleteLogin.SSHCodeWrong"},
		{name: "an expired code", code: func(c *codeEnv) string {
			code := c.fetch(c.alice)
			c.clk.Advance(61 * time.Second)
			return code
		}, wantTag: app.TagSSHCodeWrong, wantRoute: "Accounts.CompleteLogin.SSHCodeWrong"},
		{name: "a used code", code: func(c *codeEnv) string {
			code := c.fetch(c.alice)
			if _, err := c.svc.LoginClient(c.t.Context(), app.ClientLogin{
				Username: "alice", Password: goodPassword, ClientIP: loginIP, Kind: app.SessionAPI, ClientName: "x", SSHCode: code,
			}); err != nil {
				c.t.Fatal(err)
			}
			return code
		}, wantTag: app.TagSSHCodeWrong, wantRoute: "Accounts.CompleteLogin.SSHCodeWrong"},
		{name: "a replaced code", code: func(c *codeEnv) string {
			first := c.fetch(c.alice)
			c.fetch(c.alice)
			return first
		}, wantTag: app.TagSSHCodeWrong, wantRoute: "Accounts.CompleteLogin.SSHCodeWrong"},
		{name: "the code of another user", code: func(c *codeEnv) string {
			c.fetch(c.alice)
			return c.fetch(c.bob)
		}, wantTag: app.TagSSHCodeWrong, wantRoute: "Accounts.CompleteLogin.SSHCodeWrong"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := newCodeEnv(t)
			c.turnOn(c.alice)
			p := c.pending(c.alice, "/rooms/3")
			code := tt.code(c)
			events := len(c.auditUserEvents())
			sessions := c.sessions(c.alice)

			res, err := c.svc.CompleteLogin(t.Context(), p.Token, code, loginIP, "Firefox on Linux")
			if n := len(c.auditUserEvents()); n != events {
				t.Errorf("the login wrote %d audit events", n-events)
			}
			if tt.wantTag != "" {
				appErr := requireTag(t, err, tt.wantTag, tt.wantRoute)
				if f := fieldsOf(t, appErr); f[app.FieldSSHCode] != app.MessageSSHCodeWrong {
					t.Errorf("fields = %v", f)
				}
				if res.Token != "" {
					t.Errorf("a refused login returned %+v", res)
				}
				if n := c.sessions(c.alice); n != sessions {
					t.Errorf("a wrong code made %d sessions", n-sessions)
				}
				row, err := c.st.PendingLoginByTokenHash(t.Context(), auth.HashToken(p.Token))
				if err != nil || row.WrongTries != 1 {
					t.Errorf("pending = %+v, %v, want one wrong try", row, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("CompleteLogin: %+v", err)
			}
			if res.Token == "" || res.Pending != nil || res.User.ID != c.alice.ID || res.Next != "/rooms/3" {
				t.Errorf("result = %+v", res)
			}
			got, err := c.svc.SessionActor(t.Context(), res.Token)
			if err != nil || got.User.ID != c.alice.ID {
				t.Errorf("SessionActor = %+v, %v", got, err)
			}
			infos, _ := c.svc.Sessions(t.Context(), actor(c.alice), 0)
			if len(infos) != 1 || infos[0].Kind != app.SessionBrowser || infos[0].ClientName != "Firefox on Linux" {
				t.Errorf("sessions = %+v", infos)
			}
			if n := c.pendingRows(); n != 0 {
				t.Errorf("%d pending logins after the login", n)
			}
			// The pending login and the code are used up.
			_, err = c.svc.CompleteLogin(t.Context(), p.Token, code, loginIP, "Firefox")
			_ = requireTag(t, err, app.TagUnauthenticated, "Accounts.CompleteLogin->Accounts.usablePendingLogin.Unauthenticated")
		})
	}
}

// TestCompleteLoginNeedsAPendingLogin is the table of the tokens that are not
// a pending login that can be completed.
func TestCompleteLoginNeedsAPendingLogin(t *testing.T) {
	t.Parallel()
	const route = "Accounts.CompleteLogin->Accounts.usablePendingLogin.Unauthenticated"
	tests := []struct {
		name string
		// token prepares the case and returns the token to use and the code.
		token func(c *codeEnv) (token, code string)
		route string
	}{
		{"an empty token", func(c *codeEnv) (string, string) { return "", c.fetch(c.alice) }, route},
		{"a forged token", func(c *codeEnv) (string, string) {
			c.turnOn(c.alice)
			_ = c.pending(c.alice, "")
			return "forged-token-forged-token-forged-token-forged", c.fetch(c.alice)
		}, route},
		{"the session token of the user", func(c *codeEnv) (string, string) {
			c.turnOn(c.alice)
			return c.loginAs("bob", app.SessionBrowser, "x").Token, c.fetch(c.alice)
		}, route},
		{"a pending login that expired", func(c *codeEnv) (string, string) {
			c.turnOn(c.alice)
			p := c.pending(c.alice, "")
			c.clk.Advance(5 * time.Minute)
			return p.Token, c.fetch(c.alice)
		}, route},
		{"a user who was disabled in between, through the service", func(c *codeEnv) (string, string) {
			c.turnOn(c.alice)
			p := c.pending(c.alice, "")
			code := c.fetch(c.alice)
			if err := c.svc.SetDisabled(c.t.Context(), actor(c.admin), c.alice.ID, true); err != nil {
				c.t.Fatal(err)
			}
			return p.Token, code
		}, route},
		{"a user who was disabled in between, behind the service's back", func(c *codeEnv) (string, string) {
			c.turnOn(c.alice)
			p := c.pending(c.alice, "")
			code := c.fetch(c.alice)
			if err := c.st.SetUserDisabled(c.t.Context(), c.alice.ID, true); err != nil {
				c.t.Fatal(err)
			}
			return p.Token, code
		}, route},
		{"a password that was changed in between", func(c *codeEnv) (string, string) {
			c.turnOn(c.alice)
			p := c.pending(c.alice, "")
			err := c.svc.ChangePassword(c.t.Context(), actor(c.alice), 0, app.PasswordChange{
				CurrentPassword: goodPassword, NewPassword: newPassword, ClientIP: loginIP,
			})
			if err != nil {
				c.t.Fatal(err)
			}
			return p.Token, c.fetch(c.alice)
		}, route},
		{"a password reset in between", func(c *codeEnv) (string, string) {
			c.turnOn(c.alice)
			p := c.pending(c.alice, "")
			link, err := c.svc.CreateResetLink(c.t.Context(), actor(c.admin), c.alice.ID)
			if err != nil {
				c.t.Fatal(err)
			}
			if _, err := c.svc.ResetPassword(c.t.Context(), app.ResetInput{Token: link.Token, NewPassword: newPassword}); err != nil {
				c.t.Fatal(err)
			}
			return p.Token, c.fetch(c.alice)
		}, route},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := newCodeEnv(t)
			token, code := tt.token(c)
			sessions := c.sessions(c.alice)
			_, err := c.svc.CompleteLogin(t.Context(), token, code, loginIP, "Firefox")
			appErr := requireTag(t, err, app.TagUnauthenticated, tt.route)
			if appErr.Message != app.MessageSignInExpired {
				t.Errorf("message = %q", appErr.Message)
			}
			if n := c.sessions(c.alice); n != sessions {
				t.Errorf("%d sessions were made", n-sessions)
			}
			// The code was not used: a right pending login still takes it.
			if tt.name == "an empty token" || tt.name == "a forged token" {
				return
			}
			if n := c.pendingRows(); n != 0 {
				t.Errorf("%d pending logins are left", n)
			}
		})
	}
}

// TestCompleteLoginFiveWrongCodes checks that the fifth wrong code deletes the
// pending login and that nothing, not even the right code, completes it then.
func TestCompleteLoginFiveWrongCodes(t *testing.T) {
	t.Parallel()
	c := newCodeEnv(t)
	c.turnOn(c.alice)
	p := c.pending(c.alice, "")
	right := c.fetch(c.alice)
	for i := 1; i <= 4; i++ {
		_, err := c.svc.CompleteLogin(t.Context(), p.Token, "0000-0000", loginIP, "x")
		_ = requireTag(t, err, app.TagSSHCodeWrong, "Accounts.CompleteLogin.SSHCodeWrong")
	}
	if row, err := c.st.PendingLoginByTokenHash(t.Context(), auth.HashToken(p.Token)); err != nil || row.WrongTries != 4 {
		t.Fatalf("pending = %+v, %v, want 4 wrong tries", row, err)
	}
	_, err := c.svc.CompleteLogin(t.Context(), p.Token, "0000-0000", loginIP, "x")
	_ = requireTag(t, err, app.TagUnauthenticated, "Accounts.CompleteLogin.Unauthenticated")
	if n := c.pendingRows(); n != 0 {
		t.Errorf("%d pending logins after the fifth wrong code", n)
	}
	_, err = c.svc.CompleteLogin(t.Context(), p.Token, right, loginIP, "x")
	_ = requireTag(t, err, app.TagUnauthenticated, "Accounts.CompleteLogin->Accounts.usablePendingLogin.Unauthenticated")
	if n := c.sessions(c.alice); n != 0 {
		t.Errorf("%d sessions", n)
	}
	// The code was not used up by the refused tries: the API takes it.
	if _, err := c.svc.LoginClient(t.Context(), app.ClientLogin{
		Username: "alice", Password: goodPassword, ClientIP: otherIP, Kind: app.SessionAPI, ClientName: "x", SSHCode: right,
	}); err == nil {
		t.Error("the limiter did not block five failures of the username")
	}
}

// TestSSHCodeLimiter checks that wrong codes count as failed logins of the
// username and of the address, block the logins, and that a right login clears
// the failures of the username.
func TestSSHCodeLimiter(t *testing.T) {
	t.Parallel()

	t.Run("the username", func(t *testing.T) {
		t.Parallel()
		c := newCodeEnv(t)
		c.turnOn(c.alice)
		a, b := c.pending(c.alice, ""), c.pending(c.alice, "")
		for i := 0; i < 3; i++ {
			_, err := c.svc.CompleteLogin(t.Context(), a.Token, "0000-0000", "192.0.2.1", "x")
			_ = requireTag(t, err, app.TagSSHCodeWrong, "Accounts.CompleteLogin.SSHCodeWrong")
		}
		for i := 0; i < 2; i++ {
			_, err := c.svc.CompleteLogin(t.Context(), b.Token, "0000-0000", "192.0.2.2", "x")
			_ = requireTag(t, err, app.TagSSHCodeWrong, "Accounts.CompleteLogin.SSHCodeWrong")
		}
		// Five failures of the username: the right code is not looked at.
		code := c.fetch(c.alice)
		_, err := c.svc.CompleteLogin(t.Context(), a.Token, code, "192.0.2.3", "x")
		_ = requireTag(t, err, app.TagRateLimited, "Accounts.CompleteLogin.RateLimited")
		_, err = c.svc.LoginClient(t.Context(), app.ClientLogin{
			Username: "alice", Password: goodPassword, ClientIP: "192.0.2.4", Kind: app.SessionBrowser, ClientName: "x",
		})
		_ = requireTag(t, err, app.TagRateLimited, "Accounts.LoginClient.RateLimited")
		// The window passes.
		c.clk.Advance(16 * time.Minute)
		c.turnOnAgain(c.alice)
		p := c.pending(c.alice, "")
		if _, err := c.svc.CompleteLogin(t.Context(), p.Token, c.fetch(c.alice), loginIP, "x"); err != nil {
			t.Errorf("after the window: %+v", err)
		}
	})

	t.Run("the address", func(t *testing.T) {
		t.Parallel()
		c := newCodeEnv(t)
		c.turnOn(c.alice)
		c.turnOn(c.bob)
		a, b := c.pending(c.alice, ""), c.pending(c.bob, "")
		for i := 0; i < 3; i++ {
			_, err := c.svc.CompleteLogin(t.Context(), a.Token, "0000-0000", otherIP, "x")
			_ = requireTag(t, err, app.TagSSHCodeWrong, "Accounts.CompleteLogin.SSHCodeWrong")
		}
		for i := 0; i < 2; i++ {
			_, err := c.svc.CompleteLogin(t.Context(), b.Token, "0000-0000", otherIP, "x")
			_ = requireTag(t, err, app.TagSSHCodeWrong, "Accounts.CompleteLogin.SSHCodeWrong")
		}
		// The address is blocked for every user, the password too.
		_, err := c.svc.CompleteLogin(t.Context(), b.Token, c.fetch(c.bob), otherIP, "x")
		_ = requireTag(t, err, app.TagRateLimited, "Accounts.CompleteLogin.RateLimited")
		_, err = c.svc.LoginClient(t.Context(), app.ClientLogin{
			Username: "root", Password: goodPassword, ClientIP: otherIP, Kind: app.SessionBrowser, ClientName: "x",
		})
		_ = requireTag(t, err, app.TagRateLimited, "Accounts.LoginClient.RateLimited")
		// Another address is not blocked.
		if _, err := c.svc.CompleteLogin(t.Context(), b.Token, c.fetch(c.bob), "192.0.2.50", "x"); err != nil {
			t.Errorf("from another address: %+v", err)
		}
	})

	t.Run("the API counts a wrong code", func(t *testing.T) {
		t.Parallel()
		c := newCodeEnv(t)
		c.turnOn(c.alice)
		for i := 0; i < 5; i++ {
			_, err := c.svc.LoginClient(t.Context(), app.ClientLogin{
				Username: "alice", Password: goodPassword, ClientIP: otherIP, Kind: app.SessionAPI, ClientName: "x", SSHCode: "0000-0000",
			})
			_ = requireTag(t, err, app.TagUnauthenticated, "Accounts.LoginClient.Unauthenticated")
		}
		_, err := c.svc.LoginClient(t.Context(), app.ClientLogin{
			Username: "alice", Password: goodPassword, ClientIP: otherIP, Kind: app.SessionAPI, ClientName: "x", SSHCode: c.fetch(c.alice),
		})
		_ = requireTag(t, err, app.TagRateLimited, "Accounts.LoginClient.RateLimited")
	})

	t.Run("a missing code is not a failure", func(t *testing.T) {
		t.Parallel()
		c := newCodeEnv(t)
		c.turnOn(c.alice)
		for i := 0; i < 8; i++ {
			_, err := c.svc.LoginClient(t.Context(), app.ClientLogin{
				Username: "alice", Password: goodPassword, ClientIP: otherIP, Kind: app.SessionAPI, ClientName: "x",
			})
			_ = requireTag(t, err, app.TagSSHCodeRequired, "Accounts.LoginClient.SSHCodeRequired")
		}
	})

	t.Run("a right login clears the failures", func(t *testing.T) {
		t.Parallel()
		c := newCodeEnv(t)
		c.turnOn(c.alice)
		p := c.pending(c.alice, "")
		for i := 0; i < 3; i++ {
			_, _ = c.svc.CompleteLogin(t.Context(), p.Token, "0000-0000", loginIP, "x")
		}
		if _, err := c.svc.CompleteLogin(t.Context(), p.Token, c.fetch(c.alice), loginIP, "x"); err != nil {
			t.Fatalf("CompleteLogin: %+v", err)
		}
		// Three more would block if the first three still counted.
		q := c.pending(c.alice, "")
		for i := 0; i < 3; i++ {
			_, err := c.svc.CompleteLogin(t.Context(), q.Token, "0000-0000", otherIP, "x")
			_ = requireTag(t, err, app.TagSSHCodeWrong, "Accounts.CompleteLogin.SSHCodeWrong")
		}
	})
}

// turnOnAgain fetches a new code for the user, whose code is on already (a
// fetch is all the test needs after the clock moved).
func (c *codeEnv) turnOnAgain(u app.User) {
	c.t.Helper()
	c.fetch(u)
}

// TestPendingLoginsEndWithTheAccountChanges checks that a password change, a
// reset link and the disabling of the user delete the pending logins.
func TestPendingLoginsEndWithTheAccountChanges(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(c *codeEnv)
	}{
		{"a password change", func(c *codeEnv) {
			err := c.svc.ChangePassword(c.t.Context(), actor(c.alice), 0, app.PasswordChange{
				CurrentPassword: goodPassword, NewPassword: newPassword, ClientIP: loginIP,
			})
			if err != nil {
				c.t.Fatal(err)
			}
		}},
		{"a password reset", func(c *codeEnv) {
			link, err := c.svc.CreateResetLink(c.t.Context(), actor(c.admin), c.alice.ID)
			if err != nil {
				c.t.Fatal(err)
			}
			if _, err := c.svc.ResetPassword(c.t.Context(), app.ResetInput{Token: link.Token, NewPassword: newPassword}); err != nil {
				c.t.Fatal(err)
			}
		}},
		{"disabling the user", func(c *codeEnv) {
			if err := c.svc.SetDisabled(c.t.Context(), actor(c.admin), c.alice.ID, true); err != nil {
				c.t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := newCodeEnv(t)
			c.turnOn(c.alice)
			c.turnOn(c.bob)
			_ = c.pending(c.alice, "")
			_ = c.pending(c.alice, "/x")
			bobs := c.pending(c.bob, "")
			tt.change(c)
			if n := c.pendingRows(); n != 1 {
				t.Errorf("%d pending logins after %s, want only bob's", n, tt.name)
			}
			if _, err := c.svc.PendingLoginInfo(t.Context(), bobs.Token); err != nil {
				t.Errorf("bob's pending login: %+v", err)
			}
		})
	}
}

// TestSignUpAndResetNeverAskForTheCode checks SPEC §7.16: a reset link logs in
// without a code and leaves the SSH code as it was, and a new account has none.
func TestSignUpAndResetNeverAskForTheCode(t *testing.T) {
	t.Parallel()
	c := newCodeEnv(t)
	c.turnOn(c.alice)
	link, err := c.svc.CreateResetLink(t.Context(), actor(c.admin), c.alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.svc.ResetPassword(t.Context(), app.ResetInput{Token: link.Token, NewPassword: newPassword, ClientName: "x"})
	if err != nil || res.Token == "" || res.Pending != nil {
		t.Fatalf("ResetPassword = %+v, %v, want a session", res, err)
	}
	settings, err := c.svc.Settings(t.Context(), actor(c.alice))
	if err != nil || !settings.SSHCodeRequired {
		t.Errorf("after the reset: %+v, %v, want the SSH code still on", settings, err)
	}
	// The next login with the new password asks for the code again.
	_, err = c.svc.LoginClient(t.Context(), app.ClientLogin{
		Username: "alice", Password: newPassword, ClientIP: loginIP, Kind: app.SessionAPI, ClientName: "x",
	})
	_ = requireTag(t, err, app.TagSSHCodeRequired, "Accounts.LoginClient.SSHCodeRequired")

	carol := c.signUp("carol", store.RoleMember)
	if res, err := c.svc.Login(t.Context(), carol.Username, goodPassword, loginIP); err != nil || res.Token == "" {
		t.Errorf("a new account's login = %+v, %v", res, err)
	}
}

// TestEnableSSHCode is the table of EnableSSHCode: who may call it, and what it
// needs.
func TestEnableSSHCode(t *testing.T) {
	t.Parallel()
	for _, who := range callers() {
		t.Run(who.name, func(t *testing.T) {
			t.Parallel()
			c := newCodeEnv(t)
			u := who.user(c.profileEnv)
			if u.ID == c.disabled.ID {
				err := c.keys.EnableSSHCode(t.Context(), actor(u), goodPassword, loginIP)
				_ = requireTag(t, err, app.TagUnauthenticated, "SSHKeys.EnableSSHCode->Accounts.activeUser->Accounts.activeUserOn.Unauthenticated")
				return
			}
			c.fetch(u)
			c.rec.Reset()
			if err := c.keys.EnableSSHCode(t.Context(), actor(u), goodPassword, loginIP); err != nil {
				t.Fatalf("EnableSSHCode: %+v", err)
			}
			settings, _ := c.svc.Settings(t.Context(), actor(u))
			if !settings.SSHCodeRequired {
				t.Error("the SSH code is not on")
			}
			events := c.auditUserEvents()
			var enabled []store.AuditEvent
			for _, ev := range events {
				if ev.Action == store.ActionUserSSHCodeEnabled {
					enabled = append(enabled, ev)
				}
			}
			if len(enabled) != 1 || enabled[0].ActorID != u.ID || enabled[0].EntityID != u.ID || enabled[0].EntityType != store.EntityUser ||
				enabled[0].RoomID != store.NoRoom || !strings.Contains(enabled[0].Summary, u.DisplayName) {
				t.Errorf("audit events = %+v", enabled)
			}
			if ev := c.rec.Events(); len(ev) != 1 || ev[0].Topic != live.UserTopic(u.ID) {
				t.Errorf("live events = %+v", ev)
			}
			// On already: no change, no event.
			c.rec.Reset()
			if err := c.keys.EnableSSHCode(t.Context(), actor(u), goodPassword, loginIP); err != nil {
				t.Fatalf("EnableSSHCode again: %+v", err)
			}
			if n := len(c.auditUserEvents()); n != len(events) {
				t.Errorf("an idempotent call wrote %d events", n-len(events))
			}
			if ev := c.rec.Events(); len(ev) != 0 {
				t.Errorf("an idempotent call published %+v", ev)
			}
		})
	}
}

// TestEnableSSHCodeFailures is the table of what turning the SSH code on needs.
func TestEnableSSHCodeFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		serverOn  bool
		prepare   func(c *codeEnv)
		password  string
		wantTag   string
		wantRoute string
		wantField string
	}{
		{name: "a wrong password", serverOn: true, prepare: func(c *codeEnv) { c.fetch(c.alice) }, password: "not the password",
			wantTag: app.TagInvalidInput, wantRoute: "SSHKeys.EnableSSHCode.InvalidInput", wantField: app.FieldCurrentPassword},
		{name: "the server off", serverOn: false, prepare: func(c *codeEnv) { c.fetch(c.alice) },
			wantTag: app.TagSSHServerOff, wantRoute: "SSHKeys.EnableSSHCode.SSHServerOff", wantField: app.FieldForm},
		{name: "no key", serverOn: true, prepare: func(c *codeEnv) {
			keys, _ := c.keys.List(c.t.Context(), actor(c.alice))
			if err := c.keys.Remove(c.t.Context(), actor(c.alice), keys[0].ID, goodPassword, loginIP); err != nil {
				c.t.Fatal(err)
			}
		}, wantTag: app.TagSSHCodeNotTried, wantRoute: "SSHKeys.EnableSSHCode.func1.SSHCodeNotTried", wantField: app.FieldForm},
		{name: "a key that fetched no code", serverOn: true, prepare: func(c *codeEnv) {},
			wantTag: app.TagSSHCodeNotTried, wantRoute: "SSHKeys.EnableSSHCode.func1.SSHCodeNotTried", wantField: app.FieldForm},
		{name: "a code that bob's key fetched", serverOn: true, prepare: func(c *codeEnv) { c.fetch(c.bob) },
			wantTag: app.TagSSHCodeNotTried, wantRoute: "SSHKeys.EnableSSHCode.func1.SSHCodeNotTried", wantField: app.FieldForm},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := newCodeEnvOn(t, tt.serverOn)
			tt.prepare(c)
			password := tt.password
			if password == "" {
				password = goodPassword
			}
			events := len(c.auditUserEvents())
			err := c.keys.EnableSSHCode(t.Context(), actor(c.alice), password, loginIP)
			appErr := requireTag(t, err, tt.wantTag, tt.wantRoute)
			if f := fieldsOf(t, appErr); f[tt.wantField] == "" {
				t.Errorf("fields = %v, want a message for %s", f, tt.wantField)
			}
			if settings, _ := c.svc.Settings(t.Context(), actor(c.alice)); settings.SSHCodeRequired {
				t.Error("the SSH code was turned on")
			}
			if n := len(c.auditUserEvents()); n != events {
				t.Errorf("a refused change wrote %d events", n-events)
			}
		})
	}
}

// TestSSHCodePasswordLimiter checks that a wrong password for the switch counts
// in the limiter, which blocks the sixth.
func TestSSHCodePasswordLimiter(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"EnableSSHCode", "DisableSSHCode"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := newCodeEnv(t)
			c.fetch(c.alice)
			call := func(password string) error {
				if name == "EnableSSHCode" {
					return c.keys.EnableSSHCode(t.Context(), actor(c.alice), password, loginIP)
				}
				return c.keys.DisableSSHCode(t.Context(), actor(c.alice), password, loginIP)
			}
			for i := 0; i < 5; i++ {
				_ = requireTag(t, call("not the password"), app.TagInvalidInput, "SSHKeys."+name+".InvalidInput")
			}
			_ = requireTag(t, call(goodPassword), app.TagRateLimited, "SSHKeys."+name+".RateLimited")
		})
	}
}

// TestDisableSSHCode is the table of DisableSSHCode.
func TestDisableSSHCode(t *testing.T) {
	t.Parallel()
	for _, who := range callers() {
		t.Run(who.name, func(t *testing.T) {
			t.Parallel()
			c := newCodeEnv(t)
			u := who.user(c.profileEnv)
			if u.ID == c.disabled.ID {
				err := c.keys.DisableSSHCode(t.Context(), actor(u), goodPassword, loginIP)
				_ = requireTag(t, err, app.TagUnauthenticated, "SSHKeys.DisableSSHCode->Accounts.activeUser->Accounts.activeUserOn.Unauthenticated")
				return
			}
			// Off already: no change, no event.
			before := len(c.auditUserEvents())
			if err := c.keys.DisableSSHCode(t.Context(), actor(u), goodPassword, loginIP); err != nil {
				t.Fatalf("DisableSSHCode of an off code: %+v", err)
			}
			if n := len(c.auditUserEvents()); n != before {
				t.Errorf("an idempotent call wrote %d events", n-before)
			}
			c.turnOn(u)
			before = len(c.auditUserEvents())
			err := c.keys.DisableSSHCode(t.Context(), actor(u), "not the password", loginIP)
			_ = requireTag(t, err, app.TagInvalidInput, "SSHKeys.DisableSSHCode.InvalidInput")
			if n := len(c.auditUserEvents()); n != before {
				t.Errorf("a refused change wrote %d events", n-before)
			}
			if settings, _ := c.svc.Settings(t.Context(), actor(u)); !settings.SSHCodeRequired {
				t.Fatal("a wrong password turned the code off")
			}
			c.rec.Reset()
			if err := c.keys.DisableSSHCode(t.Context(), actor(u), goodPassword, loginIP); err != nil {
				t.Fatalf("DisableSSHCode: %+v", err)
			}
			if settings, _ := c.svc.Settings(t.Context(), actor(u)); settings.SSHCodeRequired {
				t.Error("the SSH code is still on")
			}
			events := c.auditUserEvents()
			if len(events) != before+1 || events[0].Action != store.ActionUserSSHCodeDisabled || events[0].ActorID != u.ID || events[0].EntityID != u.ID {
				t.Errorf("audit events = %+v", events)
			}
			if ev := c.rec.Events(); len(ev) != 1 {
				t.Errorf("live events = %+v", ev)
			}
			if _, err := c.svc.Login(t.Context(), u.Username, goodPassword, loginIP); err != nil {
				t.Errorf("login with the code off: %+v", err)
			}
		})
	}
}

// TestDisableSSHCodeWithTheServerOff checks that a user can turn the code off
// while the SSH server is off.
func TestDisableSSHCodeWithTheServerOff(t *testing.T) {
	t.Parallel()
	c := newCodeEnv(t)
	c.turnOn(c.alice)
	off := app.NewSSHKeys(c.st, c.clk, c.svc, app.SSHServerInfo{})
	if err := off.DisableSSHCode(t.Context(), actor(c.alice), goodPassword, loginIP); err != nil {
		t.Fatalf("DisableSSHCode: %+v", err)
	}
	if settings, _ := c.svc.Settings(t.Context(), actor(c.alice)); settings.SSHCodeRequired {
		t.Error("the SSH code is still on")
	}
}

// TestRemoveSSHCode is the permission table of the admin's action.
func TestRemoveSSHCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// who acts and on whom.
		actor     func(c *codeEnv) app.Actor
		target    func(c *codeEnv) uint64
		wantTag   string
		wantRoute string
		changed   bool
	}{
		{name: "an admin on a member", actor: func(c *codeEnv) app.Actor { return actor(c.admin) },
			target: func(c *codeEnv) uint64 { return c.alice.ID }, changed: true},
		{name: "an admin on themselves", actor: func(c *codeEnv) app.Actor { return actor(c.admin) },
			target: func(c *codeEnv) uint64 { return c.admin.ID }, changed: true},
		{name: "an admin on a user whose code is off", actor: func(c *codeEnv) app.Actor { return actor(c.admin) },
			target: func(c *codeEnv) uint64 { return c.bob.ID }},
		{name: "an admin on a disabled user", actor: func(c *codeEnv) app.Actor { return actor(c.admin) },
			target: func(c *codeEnv) uint64 { return c.disabled.ID }},
		{name: "an admin on an unknown user", actor: func(c *codeEnv) app.Actor { return actor(c.admin) },
			target:  func(c *codeEnv) uint64 { return 9999 },
			wantTag: app.TagNotFound, wantRoute: "SSHKeys.RemoveSSHCode.func1->Store.UserByID.NotFound"},
		{name: "a member on themselves", actor: func(c *codeEnv) app.Actor { return actor(c.alice) },
			target:  func(c *codeEnv) uint64 { return c.alice.ID },
			wantTag: app.TagForbidden, wantRoute: "SSHKeys.RemoveSSHCode.Forbidden"},
		{name: "a member on another", actor: func(c *codeEnv) app.Actor { return actor(c.bob) },
			target:  func(c *codeEnv) uint64 { return c.alice.ID },
			wantTag: app.TagForbidden, wantRoute: "SSHKeys.RemoveSSHCode.Forbidden"},
		{name: "a disabled user", actor: func(c *codeEnv) app.Actor { return actor(c.disabled) },
			target:  func(c *codeEnv) uint64 { return c.alice.ID },
			wantTag: app.TagForbidden, wantRoute: "SSHKeys.RemoveSSHCode.Forbidden"},
		{name: "an admin who was demoted since the actor was built", actor: func(c *codeEnv) app.Actor {
			a := actor(c.bob)
			a.Role = store.RoleAdmin
			return a
		}, target: func(c *codeEnv) uint64 { return c.alice.ID },
			wantTag: app.TagForbidden, wantRoute: "SSHKeys.RemoveSSHCode.func1.Forbidden"},
		{name: "an admin who was disabled since the actor was built", actor: func(c *codeEnv) app.Actor {
			a := actor(c.disabled)
			a.Role = store.RoleAdmin
			return a
		}, target: func(c *codeEnv) uint64 { return c.alice.ID },
			wantTag: app.TagUnauthenticated, wantRoute: "SSHKeys.RemoveSSHCode.func1->Accounts.activeUserOn.Unauthenticated"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := newCodeEnv(t)
			c.turnOn(c.alice)
			c.turnOn(c.admin)
			before := len(c.auditUserEvents())
			c.rec.Reset()
			err := c.keys.RemoveSSHCode(t.Context(), tt.actor(c), tt.target(c))
			events := c.auditUserEvents()
			if tt.wantTag != "" {
				_ = requireTag(t, err, tt.wantTag, tt.wantRoute)
				if len(events) != before {
					t.Errorf("a refused change wrote %d events", len(events)-before)
				}
				if settings, _ := c.svc.Settings(t.Context(), actor(c.alice)); !settings.SSHCodeRequired {
					t.Error("a refused change turned the code off")
				}
				return
			}
			if err != nil {
				t.Fatalf("RemoveSSHCode: %+v", err)
			}
			if !tt.changed {
				if len(events) != before {
					t.Errorf("a change that changed nothing wrote %d events", len(events)-before)
				}
				if ev := c.rec.Events(); len(ev) != 0 {
					t.Errorf("published %+v", ev)
				}
				return
			}
			target := tt.target(c)
			if len(events) != before+1 {
				t.Fatalf("%d events, want one more", len(events)-before)
			}
			ev := events[0]
			if ev.Action != store.ActionUserSSHCodeRemoved || ev.ActorID != c.admin.ID || ev.EntityID != target || ev.EntityType != store.EntityUser || ev.RoomID != store.NoRoom {
				t.Errorf("event = %+v", ev)
			}
			if row, _ := c.st.UserSettingsOf(t.Context(), target); row.SSHCodeRequired {
				t.Error("the code is still on")
			}
			if got := c.rec.Events(); len(got) != 1 || got[0].UserID != target {
				t.Errorf("live events = %+v", got)
			}
			// The user logs in with the password alone again.
			username := "alice"
			if target == c.admin.ID {
				username = "root"
			}
			if res, err := c.svc.Login(t.Context(), username, goodPassword, loginIP); err != nil || res.Token == "" {
				t.Errorf("login after the removal = %+v, %v", res, err)
			}
		})
	}
}

// TestRemoveTheLastKeyWhileTheCodeIsOn checks that a user cannot remove the key
// they need to fetch the code.
func TestRemoveTheLastKeyWhileTheCodeIsOn(t *testing.T) {
	t.Parallel()
	c := newCodeEnv(t)
	c.turnOn(c.alice)
	second := c.add(c.alice, "phone", newEd25519Line(t))
	keys, _ := c.keys.List(t.Context(), actor(c.alice))
	if len(keys) != 2 {
		t.Fatalf("%d keys", len(keys))
	}
	if err := c.keys.Remove(t.Context(), actor(c.alice), second.ID, goodPassword, loginIP); err != nil {
		t.Fatalf("removing one of two keys: %+v", err)
	}
	events := len(c.auditUserEvents())
	appErr := requireTag(t, c.keys.Remove(t.Context(), actor(c.alice), keys[0].ID, goodPassword, loginIP),
		app.TagSSHCodeNeedsKey, "SSHKeys.Remove.func1.SSHCodeNeedsKey")
	if f := fieldsOf(t, appErr); f[app.FieldForm] != app.MessageSSHCodeNeedsKey {
		t.Errorf("fields = %v", f)
	}
	if left, _ := c.keys.List(t.Context(), actor(c.alice)); len(left) != 1 {
		t.Errorf("%d keys are left, want the last one", len(left))
	}
	if n := len(c.auditUserEvents()); n != events {
		t.Errorf("a refused removal wrote %d events", n-events)
	}
	// Another user's last key goes while their code is off.
	bobKeys, _ := c.keys.List(t.Context(), actor(c.bob))
	if err := c.keys.Remove(t.Context(), actor(c.bob), bobKeys[0].ID, goodPassword, loginIP); err != nil {
		t.Errorf("bob's last key, code off: %+v", err)
	}
	// After the user turns the code off, the last key goes too.
	if err := c.keys.DisableSSHCode(t.Context(), actor(c.alice), goodPassword, loginIP); err != nil {
		t.Fatal(err)
	}
	if err := c.keys.Remove(t.Context(), actor(c.alice), keys[0].ID, goodPassword, loginIP); err != nil {
		t.Errorf("the last key, code off: %+v", err)
	}
}

// TestUsersShowsTheSSHCode checks the flag of the admin's list of users.
func TestUsersShowsTheSSHCode(t *testing.T) {
	t.Parallel()
	c := newCodeEnv(t)
	c.turnOn(c.alice)
	users, err := c.svc.Users(t.Context(), actor(c.admin))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, u := range users {
		got[u.Username] = u.SSHCodeRequired
	}
	if !got["alice"] || got["bob"] || got["root"] {
		t.Errorf("SSHCodeRequired by user = %v", got)
	}
	settings, _ := c.svc.Settings(t.Context(), actor(c.alice))
	if !settings.SSHCodeRequired {
		t.Error("Settings.SSHCodeRequired is false")
	}
	// UpdateSettings keeps the flag.
	dark := app.ThemeDark
	if _, err := c.svc.UpdateSettings(t.Context(), actor(c.alice), app.SettingsUpdate{Theme: &dark}); err != nil {
		t.Fatal(err)
	}
	if settings, _ := c.svc.Settings(t.Context(), actor(c.alice)); !settings.SSHCodeRequired || settings.Theme != app.ThemeDark {
		t.Errorf("settings = %+v", settings)
	}
}

// TestCancelPendingLogin checks "Start again".
func TestCancelPendingLogin(t *testing.T) {
	t.Parallel()
	c := newCodeEnv(t)
	c.turnOn(c.alice)
	p := c.pending(c.alice, "")
	if err := c.svc.CancelPendingLogin(t.Context(), p.Token); err != nil {
		t.Fatal(err)
	}
	if n := c.pendingRows(); n != 0 {
		t.Errorf("%d pending logins", n)
	}
	for _, token := range []string{"", p.Token, "unknown"} {
		if err := c.svc.CancelPendingLogin(t.Context(), token); err != nil {
			t.Errorf("CancelPendingLogin(%q): %+v", token, err)
		}
	}
}

// TestCodeWorksOnceWhenRacing checks that two logins that type the same code
// at once do not both get a session.
func TestCodeWorksOnceWhenRacing(t *testing.T) {
	t.Parallel()
	c := newCodeEnv(t)
	c.turnOn(c.alice)
	const n = 4
	tokens := make([]string, n)
	for i := range tokens {
		tokens[i] = c.pending(c.alice, "").Token
	}
	code := c.fetch(c.alice)
	results := make(chan error, n)
	for _, token := range tokens {
		go func() {
			_, err := c.svc.CompleteLogin(t.Context(), token, code, loginIP, "x")
			results <- err
		}()
	}
	won := 0
	for range tokens {
		if err := <-results; err == nil {
			won++
		}
	}
	if won != 1 {
		t.Errorf("%d logins used the same code, want 1", won)
	}
	if got := c.sessions(c.alice); got != 1 {
		t.Errorf("%d sessions, want 1", got)
	}
}
