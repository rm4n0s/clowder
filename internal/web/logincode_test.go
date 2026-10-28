package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/store"
)

// codeEnv is a testEnv whose SSH server is on and where alice has a key that
// fetched a code and the SSH code turned on.
type codeEnv struct {
	*testEnv
	alice, bob  app.User
	fingerprint map[string]string
	sign        *app.SignIn
}

func newCodeEnv(t *testing.T, mutate ...func(*config.Config)) *codeEnv {
	t.Helper()
	e := newTestEnv(t, func(c *config.Config) {
		c.SSHAddr = ":2222"
		c.SSHPublicHost = "chat.example.org"
		for _, m := range mutate {
			m(c)
		}
	})
	c := &codeEnv{testEnv: e, fingerprint: map[string]string{}, sign: app.NewSignIn(e.st, e.clk, nil)}
	c.alice = e.addUser("alice", "Alice", store.RoleMember)
	c.bob = e.addUser("bob", "Bob", store.RoleMember)
	for _, u := range []app.User{c.alice, c.bob} {
		key, err := e.sshKeys.Add(t.Context(), actorOfUser(u), "laptop", newKeyLine(t), testPassword, "192.0.2.1")
		if err != nil {
			t.Fatalf("Add: %+v", err)
		}
		c.fingerprint[u.Username] = key.Fingerprint
	}
	c.fetch(c.alice)
	if err := e.sshKeys.EnableSSHCode(t.Context(), actorOfUser(c.alice), testPassword, "192.0.2.1"); err != nil {
		t.Fatalf("EnableSSHCode: %+v", err)
	}
	return c
}

// actorOfUser makes the actor of a user.
func actorOfUser(u app.User) app.Actor {
	return app.Actor{UserID: u.ID, Username: u.Username, Role: u.Role}
}

// fetch fetches a code for the user, as the SSH server does, and returns it
// formatted.
func (c *codeEnv) fetch(u app.User) string {
	c.t.Helper()
	issued, err := c.sign.IssueCode(c.t.Context(), u.Username, c.fingerprint[u.Username], "203.0.113.5")
	if err != nil {
		c.t.Fatalf("IssueCode: %+v", err)
	}
	return issued.Formatted()
}

// codeSession logs the user in through both steps, with a fresh code, and
// returns the cookie token of the session.
func (c *codeEnv) codeSession(username string) string {
	c.t.Helper()
	res, err := c.accounts.Login(c.t.Context(), username, testPassword, "192.0.2.201")
	if err != nil {
		c.t.Fatalf("Login: %+v", err)
	}
	if res.Pending == nil {
		return res.Token
	}
	res, err = c.accounts.CompleteLogin(c.t.Context(), res.Pending.Token, c.fetch(c.alice), "192.0.2.201", "test")
	if err != nil {
		c.t.Fatalf("CompleteLogin: %+v", err)
	}
	return res.Token
}

// passwordLogin posts the login form of alice and returns the pending cookie
// of the answer.
func (c *codeEnv) passwordLogin(username string, next string, opts ...reqOpt) (*httptest.ResponseRecorder, *http.Cookie) {
	c.t.Helper()
	form := loginForm(username, testPassword)
	if next != "" {
		form.Set("next", next)
	}
	rec := c.send(postForm("/login", form, opts...))
	return rec, cookieOf(rec, pendingCookieName)
}

// cookieOf returns the cookie of the name that the answer sets, or nil.
func cookieOf(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == name {
			return ck
		}
	}
	return nil
}

// withPending sends the pending cookie.
func withPending(token string) reqOpt {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: pendingCookieName, Value: token}) }
}

func codeForm(code string) url.Values { return url.Values{"code": {code}} }

func TestLoginAsksForTheCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		datastar   bool
		mutate     func(*config.Config)
		wantSecure bool
	}{
		{name: "plain post", datastar: false},
		{name: "datastar", datastar: true},
		{name: "https makes the cookie Secure", datastar: true,
			mutate: func(c *config.Config) { c.BaseURL = "https://clowder.example.com" }, wantSecure: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var mutate []func(*config.Config)
			if tt.mutate != nil {
				mutate = append(mutate, tt.mutate)
			}
			c := newCodeEnv(t, mutate...)
			var opts []reqOpt
			if tt.datastar {
				opts = append(opts, asDatastar())
			}
			rec, pending := c.passwordLogin("alice", "", opts...)
			requireRedirectTo(t, rec, tt.datastar, "/login/code")
			if sessionCookieOf(rec) != nil {
				t.Error("the password alone started a session")
			}
			if pending == nil {
				t.Fatalf("no pending cookie; headers: %v", rec.Header())
			}
			if len(pending.Value) != 43 || !pending.HttpOnly || pending.SameSite != http.SameSiteStrictMode ||
				pending.Path != "/login" || pending.MaxAge != 300 || pending.Domain != "" || pending.Secure != tt.wantSecure {
				t.Errorf("cookie = %+v", pending)
			}
			if strings.Contains(c.log.String(), pending.Value) {
				t.Error("the pending token is in the log")
			}
			// The token is stored as its hash only.
			if info, err := c.accounts.PendingLoginInfo(t.Context(), pending.Value); err != nil || info.Username != "alice" {
				t.Errorf("PendingLoginInfo = %+v, %v", info, err)
			}
		})
	}
}

func TestLoginWithoutTheCodeNeeded(t *testing.T) {
	t.Parallel()
	c := newCodeEnv(t)
	// bob did not turn the code on.
	rec, pending := c.passwordLogin("bob", "")
	if pending != nil || sessionCookieOf(rec) == nil {
		t.Errorf("pending = %v, session = %v, want a session only", pending, sessionCookieOf(rec))
	}
	if pending := cookieOf(rec, pendingCookieName); pending != nil {
		t.Errorf("a login without a pending cookie sets one: %+v", pending)
	}
	// A wrong password gives no pending login, with the usual answer.
	rec = c.send(postForm("/login", loginForm("alice", "wrong password!"), asDatastar()))
	if cookieOf(rec, pendingCookieName) != nil || sessionCookieOf(rec) != nil {
		t.Error("a wrong password set a cookie")
	}
	requireBody(t, rec, []string{"Wrong username or password."}, "/login/code")
	// With the server off the password alone logs in.
	off := newTestEnv(t, nil)
	alice := off.addUser("alice", "Alice", store.RoleMember)
	if _, err := off.sshKeys.Add(t.Context(), actorOfUser(alice), "k", newKeyLine(t), testPassword, "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	rec = off.send(postForm("/login", loginForm("alice", testPassword)))
	if sessionCookieOf(rec) == nil || cookieOf(rec, pendingCookieName) != nil {
		t.Error("with the server off the password alone should log in")
	}
}

func TestLoginCodePage(t *testing.T) {
	t.Parallel()
	c := newCodeEnv(t)
	_, pending := c.passwordLogin("alice", "")

	t.Run("the form", func(t *testing.T) {
		t.Parallel()
		rec := c.send(get("/login/code", withPending(pending.Value)))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d:\n%s", rec.Code, rec.Body)
		}
		requireBody(t, rec, []string{
			`<form id="login-code-form" method="post" action="/login/code"`,
			`<code id="login-code-command" class="ssh-command">ssh -p 2222 alice@chat.example.org</code>`,
			`autocomplete="one-time-code"`, `autocapitalize="characters"`, `spellcheck="false"`, `maxlength="9"`,
			`name="code"`, `>Log in</button>`, `<a id="login-code-restart" href="/login/code/restart">Start again</a>`,
		}, `name="username"`, `name="password"`)
	})
	t.Run("without a cookie", func(t *testing.T) {
		t.Parallel()
		rec := c.send(get("/login/code"))
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
			t.Errorf("answer = %d to %q, want 302 to /login", rec.Code, rec.Header().Get("Location"))
		}
	})
	t.Run("a forged cookie", func(t *testing.T) {
		t.Parallel()
		rec := c.send(get("/login/code", withPending(strings.Repeat("a", 43))))
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login?expired=1" {
			t.Errorf("answer = %d to %q", rec.Code, rec.Header().Get("Location"))
		}
		if ck := cookieOf(rec, pendingCookieName); ck == nil || ck.MaxAge >= 0 {
			t.Errorf("the forged cookie was not cleared: %+v", ck)
		}
	})
	t.Run("the session cookie is not a pending login", func(t *testing.T) {
		t.Parallel()
		rec := c.send(get("/login/code", withPending(c.session("bob"))))
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login?expired=1" {
			t.Errorf("answer = %d to %q", rec.Code, rec.Header().Get("Location"))
		}
	})
	t.Run("an expired pending login", func(t *testing.T) {
		t.Parallel()
		d := newCodeEnv(t)
		_, p := d.passwordLogin("alice", "")
		d.clk.Advance(5 * time.Minute)
		rec := d.send(get("/login/code", withPending(p.Value)))
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login?expired=1" {
			t.Errorf("answer = %d to %q", rec.Code, rec.Header().Get("Location"))
		}
	})
	t.Run("a person who is logged in goes home", func(t *testing.T) {
		t.Parallel()
		rec := c.send(get("/login/code", withPending(pending.Value), withCookie(c.session("bob"))))
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
			t.Errorf("answer = %d to %q", rec.Code, rec.Header().Get("Location"))
		}
	})
	t.Run("the login page says the sign-in expired", func(t *testing.T) {
		t.Parallel()
		rec := c.send(get("/login?expired=1&next=%2Frooms%2F1"))
		requireBody(t, rec, []string{app.MessageSignInExpired, `<input type="hidden" name="next" value="/rooms/1">`})
		rec = c.send(get("/login"))
		requireBody(t, rec, nil, app.MessageSignInExpired)
	})
}

func TestLoginCodeSubmit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		datastar bool
		next     string
		// code makes what is typed.
		code     func(c *codeEnv) string
		wantNext string
	}{
		{name: "the right code, Datastar", datastar: true, code: func(c *codeEnv) string { return c.fetch(c.alice) }, wantNext: "/"},
		{name: "the right code, plain", code: func(c *codeEnv) string { return c.fetch(c.alice) }, wantNext: "/"},
		{name: "typed in lower case without the dash", datastar: true, wantNext: "/", code: func(c *codeEnv) string {
			return strings.ToLower(strings.ReplaceAll(c.fetch(c.alice), "-", ""))
		}},
		{name: "next is kept", datastar: true, next: "/rooms/1?x=y", wantNext: "/rooms/1?x=y",
			code: func(c *codeEnv) string { return c.fetch(c.alice) }},
		{name: "an outside next is not followed", datastar: true, next: "//evil.example", wantNext: "/",
			code: func(c *codeEnv) string { return c.fetch(c.alice) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := newCodeEnv(t)
			_, pending := c.passwordLogin("alice", tt.next)
			var opts []reqOpt
			if tt.datastar {
				opts = append(opts, asDatastar())
			}
			rec := c.send(postForm("/login/code", codeForm(tt.code(c)), append([]reqOpt{withPending(pending.Value)}, opts...)...))
			requireRedirectTo(t, rec, tt.datastar, tt.wantNext)
			session := sessionCookieOf(rec)
			if session == nil || session.Path != "/" || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode {
				t.Fatalf("session cookie = %+v", session)
			}
			if got, err := c.accounts.SessionActor(t.Context(), session.Value); err != nil || got.User.ID != c.alice.ID {
				t.Errorf("SessionActor = %+v, %v", got, err)
			}
			if ck := cookieOf(rec, pendingCookieName); ck == nil || ck.MaxAge >= 0 || ck.Path != "/login" {
				t.Errorf("the pending cookie was not cleared: %+v", ck)
			}
			// The pending login is used up.
			rec = c.send(get("/login/code", withPending(pending.Value)))
			if rec.Header().Get("Location") != "/login?expired=1" {
				t.Errorf("after the login, /login/code goes to %q", rec.Header().Get("Location"))
			}
			if strings.Contains(c.log.String(), session.Value) || strings.Contains(c.log.String(), pending.Value) {
				t.Error("a token is in the log")
			}
		})
	}
}

func TestLoginCodeSubmitFailures(t *testing.T) {
	t.Parallel()
	t.Run("a wrong code, Datastar", func(t *testing.T) {
		t.Parallel()
		c := newCodeEnv(t)
		_, pending := c.passwordLogin("alice", "")
		rec := c.send(postForm("/login/code", codeForm("0000-0000"), withPending(pending.Value), asDatastar()))
		requireSSE(t, rec)
		requireBody(t, rec, []string{
			`id="login-code-form"`, "That code is not right.", `aria-invalid="true"`, "ssh -p 2222 alice@chat.example.org",
		}, "redirect")
		if sessionCookieOf(rec) != nil {
			t.Error("a wrong code started a session")
		}
		if ck := cookieOf(rec, pendingCookieName); ck != nil {
			t.Errorf("a wrong code changed the pending cookie: %+v", ck)
		}
		// The pending login is still good.
		rec = c.send(postForm("/login/code", codeForm(c.fetch(c.alice)), withPending(pending.Value), asDatastar()))
		requireRedirectTo(t, rec, true, "/")
	})
	t.Run("a wrong code, plain", func(t *testing.T) {
		t.Parallel()
		c := newCodeEnv(t)
		_, pending := c.passwordLogin("alice", "")
		rec := c.send(postForm("/login/code", codeForm("0000-0000"), withPending(pending.Value)))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d:\n%s", rec.Code, rec.Body)
		}
		requireBody(t, rec, []string{"<title id=\"page-title\">Log in · Clowder</title>", `id="login-code-form"`, "That code is not right."})
	})
	t.Run("an empty code", func(t *testing.T) {
		t.Parallel()
		c := newCodeEnv(t)
		_, pending := c.passwordLogin("alice", "")
		rec := c.send(postForm("/login/code", codeForm(""), withPending(pending.Value), asDatastar()))
		requireBody(t, rec, []string{"That code is not right."})
	})
	t.Run("five wrong codes end the sign-in", func(t *testing.T) {
		t.Parallel()
		c := newCodeEnv(t)
		_, pending := c.passwordLogin("alice", "/rooms/1")
		right := c.fetch(c.alice)
		for i := 1; i <= 4; i++ {
			rec := c.send(postForm("/login/code", codeForm("0000-0000"), withPending(pending.Value), asDatastar()))
			requireBody(t, rec, []string{"That code is not right."})
		}
		rec := c.send(postForm("/login/code", codeForm("0000-0000"), withPending(pending.Value), asDatastar()))
		requireRedirectTo(t, rec, true, "/login?expired=1&next=%2Frooms%2F1")
		if ck := cookieOf(rec, pendingCookieName); ck == nil || ck.MaxAge >= 0 {
			t.Errorf("the pending cookie was not cleared: %+v", ck)
		}
		// The right code does not work any more.
		rec = c.send(postForm("/login/code", codeForm(right), withPending(pending.Value)))
		if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login?expired=1") || sessionCookieOf(rec) != nil {
			t.Errorf("answer = %d to %q", rec.Code, rec.Header().Get("Location"))
		}
	})
	t.Run("an expired pending login", func(t *testing.T) {
		t.Parallel()
		c := newCodeEnv(t)
		_, pending := c.passwordLogin("alice", "")
		c.clk.Advance(5 * time.Minute)
		rec := c.send(postForm("/login/code", codeForm(c.fetch(c.alice)), withPending(pending.Value), asDatastar()))
		requireRedirectTo(t, rec, true, "/login?expired=1")
		if sessionCookieOf(rec) != nil {
			t.Error("an expired pending login started a session")
		}
	})
	t.Run("without a cookie", func(t *testing.T) {
		t.Parallel()
		c := newCodeEnv(t)
		rec := c.send(postForm("/login/code", codeForm(c.fetch(c.alice))))
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" || sessionCookieOf(rec) != nil {
			t.Errorf("answer = %d to %q", rec.Code, rec.Header().Get("Location"))
		}
	})
	t.Run("a forged cookie", func(t *testing.T) {
		t.Parallel()
		c := newCodeEnv(t)
		rec := c.send(postForm("/login/code", codeForm(c.fetch(c.alice)), withPending(strings.Repeat("b", 43))))
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?expired=1" || sessionCookieOf(rec) != nil {
			t.Errorf("answer = %d to %q", rec.Code, rec.Header().Get("Location"))
		}
	})
	t.Run("the code of another user", func(t *testing.T) {
		t.Parallel()
		c := newCodeEnv(t)
		_, pending := c.passwordLogin("alice", "")
		c.fetch(c.alice)
		rec := c.send(postForm("/login/code", codeForm(c.fetch(c.bob)), withPending(pending.Value), asDatastar()))
		requireBody(t, rec, []string{"That code is not right."})
		if sessionCookieOf(rec) != nil {
			t.Error("another user's code started a session")
		}
	})
	t.Run("the wrong codes are limited", func(t *testing.T) {
		t.Parallel()
		c := newCodeEnv(t)
		_, a := c.passwordLogin("alice", "")
		_, b := c.passwordLogin("alice", "")
		for i := 0; i < 3; i++ {
			c.send(postForm("/login/code", codeForm("0000-0000"), withPending(a.Value), asDatastar()))
		}
		for i := 0; i < 2; i++ {
			c.send(postForm("/login/code", codeForm("0000-0000"), withPending(b.Value), asDatastar()))
		}
		rec := c.send(postForm("/login/code", codeForm(c.fetch(c.alice)), withPending(a.Value)))
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429:\n%s", rec.Code, rec.Body)
		}
		requireBody(t, rec, []string{messageRateLimited})
		rec = c.send(postForm("/login", loginForm("alice", testPassword)))
		if rec.Code != http.StatusTooManyRequests {
			t.Errorf("the password login: status = %d, want 429", rec.Code)
		}
	})
	t.Run("the password changed in between", func(t *testing.T) {
		t.Parallel()
		c := newCodeEnv(t)
		_, pending := c.passwordLogin("alice", "")
		err := c.accounts.ChangePassword(t.Context(), actorOfUser(c.alice), 0, app.PasswordChange{
			CurrentPassword: testPassword, NewPassword: "another long password", ClientIP: "192.0.2.1",
		})
		if err != nil {
			t.Fatal(err)
		}
		rec := c.send(postForm("/login/code", codeForm(c.fetch(c.alice)), withPending(pending.Value), asDatastar()))
		requireRedirectTo(t, rec, true, "/login?expired=1")
	})
	t.Run("the user was disabled in between", func(t *testing.T) {
		t.Parallel()
		c := newCodeEnv(t)
		admin := c.addUser("root", "Root", store.RoleAdmin)
		_, pending := c.passwordLogin("alice", "")
		code := c.fetch(c.alice)
		if err := c.accounts.SetDisabled(t.Context(), actorOfUser(admin), c.alice.ID, true); err != nil {
			t.Fatal(err)
		}
		rec := c.send(postForm("/login/code", codeForm(code), withPending(pending.Value), asDatastar()))
		requireRedirectTo(t, rec, true, "/login?expired=1")
	})
}

func TestLoginCodeRestart(t *testing.T) {
	t.Parallel()
	c := newCodeEnv(t)
	_, pending := c.passwordLogin("alice", "")
	rec := c.send(get("/login/code/restart", withPending(pending.Value)))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Errorf("answer = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if ck := cookieOf(rec, pendingCookieName); ck == nil || ck.MaxAge >= 0 || ck.Path != "/login" {
		t.Errorf("the pending cookie was not cleared: %+v", ck)
	}
	rec = c.send(get("/login/code", withPending(pending.Value)))
	if rec.Header().Get("Location") != "/login?expired=1" {
		t.Errorf("the pending login is still there: %q", rec.Header().Get("Location"))
	}
	// Without a cookie it is a harmless redirect.
	rec = c.send(get("/login/code/restart"))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Errorf("answer = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
}

// TestPasswordLoginReplacesThePendingCookie checks that logging in without a
// code clears an old pending cookie.
func TestPasswordLoginClearsThePendingCookie(t *testing.T) {
	t.Parallel()
	c := newCodeEnv(t)
	_, pending := c.passwordLogin("alice", "")
	rec := c.send(postForm("/login", loginForm("bob", testPassword), withPending(pending.Value)))
	if ck := cookieOf(rec, pendingCookieName); ck == nil || ck.MaxAge >= 0 {
		t.Errorf("the pending cookie was not cleared: %+v", ck)
	}
	if sessionCookieOf(rec) == nil {
		t.Error("no session")
	}
}

// TestSignInSwitch tests the switch of the Sign-in section.
func TestSignInSwitch(t *testing.T) {
	t.Parallel()
	t.Run("off, no key has fetched a code", func(t *testing.T) {
		t.Parallel()
		e := newSSHSettingsEnv(t, true)
		e.addKey(e.alice, "laptop")
		rec := e.send(get("/me/settings", withCookie(e.token)))
		requireBody(t, rec, []string{
			"Ask for an SSH code when I log in", `<form id="ssh-code-form" method="post" action="/me/settings/ssh-code"`,
			"Fetch a code with one of your keys first.", "It is <strong>off</strong>", `<button type="submit" disabled>Turn on</button>`,
			`value="true"`,
		}, "Turn off")
	})
	t.Run("off, the server is off", func(t *testing.T) {
		t.Parallel()
		e := newSSHSettingsEnv(t, false)
		rec := e.send(get("/me/settings", withCookie(e.token)))
		requireBody(t, rec, []string{
			`<p class="hint" id="ssh-code-unavailable">The SSH server is not running on this Clowder.</p>`, `<button type="submit" disabled>Turn on</button>`,
		})
	})
	t.Run("off, a key fetched a code", func(t *testing.T) {
		t.Parallel()
		c := newCodeEnv(t)
		if err := c.sshKeys.DisableSSHCode(t.Context(), actorOfUser(c.alice), testPassword, "192.0.2.1"); err != nil {
			t.Fatal(err)
		}
		rec := c.send(get("/me/settings", withCookie(c.codeSession("alice"))))
		requireBody(t, rec, []string{"It is <strong>off</strong>", `<button type="submit">Turn on</button>`}, "Fetch a code with one of your keys first.", "disabled")
	})
	t.Run("on", func(t *testing.T) {
		t.Parallel()
		c := newCodeEnv(t)
		rec := c.send(get("/me/settings", withCookie(c.codeSession("alice"))))
		requireBody(t, rec, []string{"It is <strong>on</strong>", `<button type="submit" class="secondary">Turn off</button>`, `value="false"`}, "Turn on</button>")
	})
	t.Run("on, the server is off", func(t *testing.T) {
		t.Parallel()
		c := newCodeEnv(t)
		// A process without the SSH server shows the switch, which turns off.
		off := newTestEnv(t, nil)
		alice := off.addUser("alice", "Alice", store.RoleMember)
		if _, err := off.st.SetSSHCodeRequired(t.Context(), alice.ID, true); err != nil {
			t.Fatal(err)
		}
		_ = c
		rec := off.send(get("/me/settings", withCookie(off.session("alice"))))
		requireBody(t, rec, []string{"so no code is asked for now", `<button type="submit" class="secondary">Turn off</button>`})
	})
}

func TestSettingsSSHCodePost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		serverOn bool
		// prepare changes the state before the post.
		prepare  func(c *codeEnv)
		form     url.Values
		datastar bool
		wantCode int
		want     []string
		notWant  []string
		wantOn   bool
	}{
		{name: "turn on", serverOn: true, prepare: func(c *codeEnv) {
			if err := c.sshKeys.DisableSSHCode(c.t.Context(), actorOfUser(c.alice), testPassword, "192.0.2.1"); err != nil {
				c.t.Fatal(err)
			}
		}, form: url.Values{"enabled": {"true"}, "currentPassword": {testPassword}}, datastar: true, wantCode: http.StatusOK,
			want: []string{`id="signin"`, "It is <strong>on</strong>", `<p class="saved" role="status">Saved</p>`, "reset()"}, notWant: []string{testPassword}, wantOn: true},
		{name: "turn off", serverOn: true, form: url.Values{"enabled": {"false"}, "currentPassword": {testPassword}}, datastar: true,
			wantCode: http.StatusOK, want: []string{"It is <strong>off</strong>", `Saved</p>`}, wantOn: false},
		{name: "turn off, plain", serverOn: true, form: url.Values{"enabled": {"false"}, "currentPassword": {testPassword}},
			wantCode: http.StatusSeeOther, wantOn: false},
		{name: "a wrong password", serverOn: true, form: url.Values{"enabled": {"false"}, "currentPassword": {"wrong password!"}}, datastar: true,
			wantCode: http.StatusOK, want: []string{"This is not your password.", `aria-invalid="true"`}, notWant: []string{"wrong password!", "Saved</p>"}, wantOn: true},
		{name: "a wrong password, plain", serverOn: true, form: url.Values{"enabled": {"false"}, "currentPassword": {"wrong password!"}},
			wantCode: http.StatusUnprocessableEntity, want: []string{"<title id=\"page-title\">Settings · Clowder</title>", "This is not your password."}, wantOn: true},
		{name: "not true or false", serverOn: true, form: url.Values{"enabled": {"maybe"}, "currentPassword": {testPassword}}, datastar: true,
			wantCode: http.StatusOK, want: []string{messageActionInvalid}, wantOn: true},
		{name: "turn on with no key that fetched a code", serverOn: true, prepare: func(c *codeEnv) {
			ctx, actor := c.t.Context(), actorOfUser(c.alice)
			if err := c.sshKeys.DisableSSHCode(ctx, actor, testPassword, "192.0.2.1"); err != nil {
				c.t.Fatal(err)
			}
			keys, _ := c.sshKeys.List(ctx, actor)
			if err := c.sshKeys.Remove(ctx, actor, keys[0].ID, testPassword, "192.0.2.1"); err != nil {
				c.t.Fatal(err)
			}
			if _, err := c.sshKeys.Add(ctx, actor, "fresh", newKeyLine(c.t), testPassword, "192.0.2.1"); err != nil {
				c.t.Fatal(err)
			}
		}, form: url.Values{"enabled": {"true"}, "currentPassword": {testPassword}}, datastar: true,
			wantCode: http.StatusOK, want: []string{app.MessageSSHCodeNotTried}, wantOn: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := newCodeEnv(t)
			if tt.prepare != nil {
				tt.prepare(c)
			}
			var opts []reqOpt
			if tt.datastar {
				opts = append(opts, asDatastar())
			}
			rec := c.send(postForm("/me/settings/ssh-code", tt.form, append([]reqOpt{withCookie(c.codeSession("alice"))}, opts...)...))
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d:\n%s", rec.Code, tt.wantCode, rec.Body)
			}
			requireBody(t, rec, tt.want, tt.notWant...)
			if tt.wantCode == http.StatusSeeOther && rec.Header().Get("Location") != "/me/settings?saved=signin#signin" {
				t.Errorf("Location = %q", rec.Header().Get("Location"))
			}
			if tt.datastar {
				requireSSE(t, rec)
			}
			settings, _ := c.accounts.Settings(t.Context(), actorOfUser(c.alice))
			if tt.name == "turn on" || tt.name == "turn off" || tt.name == "turn off, plain" {
				if settings.SSHCodeRequired != tt.wantOn {
					t.Errorf("SSHCodeRequired = %v, want %v", settings.SSHCodeRequired, tt.wantOn)
				}
			}
		})
	}
	t.Run("the server off", func(t *testing.T) {
		t.Parallel()
		e := newSSHSettingsEnv(t, false)
		rec := e.send(postForm("/me/settings/ssh-code", url.Values{"enabled": {"true"}, "currentPassword": {testPassword}}, withCookie(e.token), asDatastar()))
		requireBody(t, rec, []string{app.MessageSSHServerOff})
	})
	t.Run("requires login", func(t *testing.T) {
		t.Parallel()
		e := newSSHSettingsEnv(t, true)
		rec := e.send(postForm("/me/settings/ssh-code", url.Values{"enabled": {"false"}}))
		if rec.Code != http.StatusFound && rec.Code != http.StatusSeeOther {
			t.Errorf("status %d", rec.Code)
		}
	})
	t.Run("removing the last key is refused while the code is on", func(t *testing.T) {
		t.Parallel()
		c := newCodeEnv(t)
		keys, _ := c.sshKeys.List(t.Context(), actorOfUser(c.alice))
		rec := c.send(postForm("/me/settings/ssh-keys/"+itoa(keys[0].ID)+"/remove", url.Values{"currentPassword": {testPassword}},
			withCookie(c.codeSession("alice")), asDatastar()))
		requireBody(t, rec, []string{app.MessageSSHCodeNeedsKey})
		if left, _ := c.sshKeys.List(t.Context(), actorOfUser(c.alice)); len(left) != 1 {
			t.Errorf("%d keys left", len(left))
		}
	})
}

func TestAdminSSHCode(t *testing.T) {
	t.Parallel()
	t.Run("the users page", func(t *testing.T) {
		t.Parallel()
		c := newCodeEnv(t)
		admin := c.addUser("root", "Root", store.RoleAdmin)
		_ = admin
		rec := c.send(get("/admin/users", withCookie(c.session("root"))))
		requireBody(t, rec, []string{
			`<th scope="col">SSH code</th>`, `<td class="ssh-code">`, "On", "Off",
			`action="/admin/users/` + itoa(c.alice.ID) + `/ssh-code/remove"`, "Turn off SSH code",
		}, "The SSH server is not running", `/admin/users/`+itoa(c.bob.ID)+`/ssh-code/remove`)
	})
	t.Run("no warning when the server is off and nobody has the code on", func(t *testing.T) {
		t.Parallel()
		e := newAdminEnv(t)
		rec := e.send(get("/admin/users", e.adminSession))
		requireBody(t, rec, []string{`id="users-panel"`}, `id="ssh-server-warning"`)
	})
	t.Run("the warning when the server is off and a user has the code on", func(t *testing.T) {
		t.Parallel()
		e := newAdminEnv(t)
		if _, err := e.st.SetSSHCodeRequired(t.Context(), e.alice.ID, true); err != nil {
			t.Fatal(err)
		}
		rec := e.send(get("/admin/users", e.adminSession))
		requireBody(t, rec, []string{
			`id="ssh-server-warning"`, "The SSH server is not running: users who turned on the SSH code log in with their password only.",
		})
	})
	t.Run("turn off", func(t *testing.T) {
		t.Parallel()
		c := newCodeEnv(t)
		c.addUser("root", "Root", store.RoleAdmin)
		path := "/admin/users/" + itoa(c.alice.ID) + "/ssh-code/remove"
		rec := c.send(postForm(path, url.Values{}, withCookie(c.session("root")), asDatastar()))
		requireSSE(t, rec)
		requireBody(t, rec, []string{`id="users-panel"`}, "Turn off SSH code")
		if settings, _ := c.accounts.Settings(t.Context(), actorOfUser(c.alice)); settings.SSHCodeRequired {
			t.Error("the code is still on")
		}
		// Plain post.
		rec = c.send(postForm(path, url.Values{}, withCookie(c.session("root"))))
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/users" {
			t.Errorf("answer = %d to %q", rec.Code, rec.Header().Get("Location"))
		}
		// She logs in with the password alone.
		if rec := c.send(postForm("/login", loginForm("alice", testPassword))); sessionCookieOf(rec) == nil {
			t.Error("alice cannot log in with the password alone")
		}
	})
	t.Run("an unknown user", func(t *testing.T) {
		t.Parallel()
		c := newCodeEnv(t)
		c.addUser("root", "Root", store.RoleAdmin)
		rec := c.send(postForm("/admin/users/9999/ssh-code/remove", url.Values{}, withCookie(c.session("root")), asDatastar()))
		requireBody(t, rec, []string{messageUserGone})
	})
	t.Run("a member gets the 404 page", func(t *testing.T) {
		t.Parallel()
		c := newCodeEnv(t)
		rec := c.send(postForm("/admin/users/"+itoa(c.alice.ID)+"/ssh-code/remove", url.Values{}, withCookie(c.session("bob"))))
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
		if settings, _ := c.accounts.Settings(t.Context(), actorOfUser(c.alice)); !settings.SSHCodeRequired {
			t.Error("a member turned the code off")
		}
	})
	t.Run("without a login", func(t *testing.T) {
		t.Parallel()
		c := newCodeEnv(t)
		rec := c.send(postForm("/admin/users/"+itoa(c.alice.ID)+"/ssh-code/remove", url.Values{}))
		if rec.Code != http.StatusFound && rec.Code != http.StatusSeeOther {
			t.Errorf("status = %d", rec.Code)
		}
	})
}
