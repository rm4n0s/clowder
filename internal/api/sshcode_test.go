package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/store"
)

// codeAPIEnv is a testEnv with the SSH server on, an admin, alice (with a key
// that fetched a code) and bob.
type codeAPIEnv struct {
	*testEnv
	alice, bob, admin app.User
	fingerprint       map[string]string
	sign              *app.SignIn
}

func newCodeAPIEnv(t *testing.T) *codeAPIEnv {
	t.Helper()
	e := newTestEnv(t, nil)
	c := &codeAPIEnv{testEnv: e, fingerprint: map[string]string{}, sign: app.NewSignIn(e.st, e.clk, nil)}
	c.admin = e.addUser("admin", store.RoleAdmin)
	c.alice = e.addUser("alice", store.RoleMember)
	c.bob = e.addUser("bob", store.RoleMember)
	for _, user := range []string{"alice", "bob"} {
		key := e.addKey(user, "laptop", keyLine(t), testPassword)
		c.fingerprint[user], _ = key["fingerprint"].(string)
	}
	return c
}

// fetch fetches a code for the user, as the SSH server does.
func (c *codeAPIEnv) fetch(username string) string {
	c.t.Helper()
	issued, err := c.sign.IssueCode(c.t.Context(), username, c.fingerprint[username], "203.0.113.5")
	if err != nil {
		c.t.Fatalf("IssueCode: %+v", err)
	}
	return issued.Formatted()
}

// turnOn fetches a code and turns the SSH code on for the user, through the
// API with the token the user has.
func (c *codeAPIEnv) turnOn(username string) {
	c.t.Helper()
	c.fetch(username)
	rec := c.send(request(http.MethodPut, "/api/v1/me/ssh-code", map[string]any{"current_password": testPassword}, bearer(c.token(username))))
	requireStatus(c.t, rec, http.StatusNoContent)
}

// login posts to /auth/login.
func (c *codeAPIEnv) login(body map[string]any, opts ...reqOpt) *httptest.ResponseRecorder {
	c.t.Helper()
	return c.send(request(http.MethodPost, "/api/v1/auth/login", body, opts...))
}

func loginBody(username, password, code string) map[string]any {
	body := map[string]any{"username": username, "password": password, "client_name": "Desktop"}
	if code != "" {
		body["ssh_code"] = code
	}
	return body
}

func TestLoginWithSSHCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		password string
		code     func(c *codeAPIEnv) string
		status   int
		errCode  string
	}{
		{name: "the right code", code: func(c *codeAPIEnv) string { return c.fetch("alice") }, status: http.StatusOK},
		{name: "the code in lower case without the dash", code: func(c *codeAPIEnv) string {
			return strings.ToLower(strings.ReplaceAll(c.fetch("alice"), "-", ""))
		}, status: http.StatusOK},
		{name: "no code", code: func(c *codeAPIEnv) string { return "" }, status: http.StatusUnauthorized, errCode: "ssh_code_required"},
		{name: "a wrong code", code: func(c *codeAPIEnv) string { c.fetch("alice"); return "0000-0000" },
			status: http.StatusUnauthorized, errCode: "unauthenticated"},
		{name: "an expired code", code: func(c *codeAPIEnv) string {
			code := c.fetch("alice")
			c.clk.Advance(61 * time.Second)
			return code
		}, status: http.StatusUnauthorized, errCode: "unauthenticated"},
		{name: "the code of another user", code: func(c *codeAPIEnv) string { c.fetch("alice"); return c.fetch("bob") },
			status: http.StatusUnauthorized, errCode: "unauthenticated"},
		{name: "a replaced code", code: func(c *codeAPIEnv) string {
			first := c.fetch("alice")
			c.fetch("alice")
			return first
		}, status: http.StatusUnauthorized, errCode: "unauthenticated"},
		{name: "a wrong password and no code", password: "wrong password!", code: func(c *codeAPIEnv) string { return "" },
			status: http.StatusUnauthorized, errCode: "unauthenticated"},
		{name: "a wrong password and the right code", password: "wrong password!", code: func(c *codeAPIEnv) string { return c.fetch("alice") },
			status: http.StatusUnauthorized, errCode: "unauthenticated"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := newCodeAPIEnv(t)
			c.turnOn("alice")
			password := tt.password
			if password == "" {
				password = testPassword
			}
			rec := c.login(loginBody("alice", password, tt.code(c)))
			requireStatus(t, rec, tt.status)
			if tt.status != http.StatusOK {
				if got := errorCode(t, rec); got != tt.errCode {
					t.Errorf("error.code = %q, want %q", got, tt.errCode)
				}
				if rec.Header().Get("WWW-Authenticate") == "" {
					t.Error("no WWW-Authenticate")
				}
				// A wrong password says nothing about the code.
				if tt.password != "" && strings.Contains(rec.Body.String(), "code") && !strings.Contains(rec.Body.String(), "Wrong username or password.") {
					t.Errorf("a wrong password gave %s", rec.Body)
				}
				return
			}
			token, _ := decode(t, rec)["token"].(string)
			me := c.send(request(http.MethodGet, "/api/v1/me", nil, bearer(token)))
			requireStatus(t, me, http.StatusOK)
		})
	}
}

func TestLoginWithSSHCodeIsUsedOnce(t *testing.T) {
	t.Parallel()
	c := newCodeAPIEnv(t)
	c.turnOn("alice")
	code := c.fetch("alice")
	requireStatus(t, c.login(loginBody("alice", testPassword, code)), http.StatusOK)
	rec := c.login(loginBody("alice", testPassword, code))
	requireStatus(t, rec, http.StatusUnauthorized)
	if errorCode(t, rec) != "unauthenticated" {
		t.Errorf("error.code = %q", errorCode(t, rec))
	}
}

func TestLoginWithSSHCodeCountsInTheLimiter(t *testing.T) {
	t.Parallel()
	c := newCodeAPIEnv(t)
	c.turnOn("alice")
	for i := 0; i < 5; i++ {
		rec := c.login(loginBody("alice", testPassword, "0000-0000"))
		requireStatus(t, rec, http.StatusUnauthorized)
	}
	rec := c.login(loginBody("alice", testPassword, c.fetch("alice")))
	requireStatus(t, rec, http.StatusTooManyRequests)
	if errorCode(t, rec) != "rate_limited" || rec.Header().Get("Retry-After") == "" {
		t.Errorf("answer = %s, Retry-After %q", rec.Body, rec.Header().Get("Retry-After"))
	}
}

func TestLoginWithoutTheCodeOnAndServerOff(t *testing.T) {
	t.Parallel()
	// bob did not turn it on: a code is not asked for, and not required.
	c := newCodeAPIEnv(t)
	requireStatus(t, c.login(loginBody("bob", testPassword, "")), http.StatusOK)
	// With the server off, a user with the code on logs in with the password.
	off := newTestEnvSSH(t, nil, app.SSHServerInfo{})
	alice := off.addUser("alice", store.RoleMember)
	if _, err := off.st.SetSSHCodeRequired(t.Context(), alice.ID, true); err != nil {
		t.Fatal(err)
	}
	rec := off.send(request(http.MethodPost, "/api/v1/auth/login", loginBody("alice", testPassword, ""), nil...))
	requireStatus(t, rec, http.StatusOK)
}

func TestEnableAndDisableSSHCode(t *testing.T) {
	t.Parallel()
	body := func(password string) map[string]any { return map[string]any{"current_password": password} }

	t.Run("turn on and off", func(t *testing.T) {
		t.Parallel()
		c := newCodeAPIEnv(t)
		token := c.token("alice")
		c.fetch("alice")
		for i := 0; i < 2; i++ { // the second call changes nothing
			requireStatus(t, c.send(request(http.MethodPut, "/api/v1/me/ssh-code", body(testPassword), bearer(token))), http.StatusNoContent)
		}
		settings := decode(t, c.send(request(http.MethodGet, "/api/v1/me/settings", nil, bearer(token))))
		if settings["ssh_code_required"] != true {
			t.Errorf("settings = %v", settings)
		}
		events, _ := c.st.ListAuditEvents(t.Context(), store.AuditFilter{Action: store.ActionUserSSHCodeEnabled}, 10, 0)
		if len(events) != 1 {
			t.Errorf("%d enabled events, want 1", len(events))
		}
		for i := 0; i < 2; i++ {
			requireStatus(t, c.send(request(http.MethodDelete, "/api/v1/me/ssh-code", body(testPassword), bearer(token))), http.StatusNoContent)
		}
		settings = decode(t, c.send(request(http.MethodGet, "/api/v1/me/settings", nil, bearer(token))))
		if settings["ssh_code_required"] != false {
			t.Errorf("settings = %v", settings)
		}
		events, _ = c.st.ListAuditEvents(t.Context(), store.AuditFilter{Action: store.ActionUserSSHCodeDisabled}, 10, 0)
		if len(events) != 1 {
			t.Errorf("%d disabled events, want 1", len(events))
		}
	})
	t.Run("a wrong password", func(t *testing.T) {
		t.Parallel()
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			c := newCodeAPIEnv(t)
			c.fetch("alice")
			rec := c.send(request(method, "/api/v1/me/ssh-code", body("wrong password!"), bearer(c.token("alice"))))
			requireStatus(t, rec, http.StatusUnprocessableEntity)
			if f := errorFields(t, rec); f["current_password"] == nil {
				t.Errorf("%s fields = %v", method, f)
			}
		}
	})
	t.Run("the limiter", func(t *testing.T) {
		t.Parallel()
		c := newCodeAPIEnv(t)
		token := c.token("alice")
		for i := 0; i < 5; i++ {
			requireStatus(t, c.send(request(http.MethodPut, "/api/v1/me/ssh-code", body("wrong password!"), bearer(token))), http.StatusUnprocessableEntity)
		}
		requireStatus(t, c.send(request(http.MethodPut, "/api/v1/me/ssh-code", body(testPassword), bearer(token))), http.StatusTooManyRequests)
	})
	t.Run("no key has fetched a code", func(t *testing.T) {
		t.Parallel()
		c := newCodeAPIEnv(t)
		rec := c.send(request(http.MethodPut, "/api/v1/me/ssh-code", body(testPassword), bearer(c.token("alice"))))
		requireStatus(t, rec, http.StatusConflict)
		if errorCode(t, rec) != "conflict" || errorFields(t, rec)["form"] != app.MessageSSHCodeNotTried {
			t.Errorf("answer = %s", rec.Body)
		}
	})
	t.Run("the server is off", func(t *testing.T) {
		t.Parallel()
		e := newTestEnvSSH(t, nil, app.SSHServerInfo{})
		e.addUser("alice", store.RoleMember)
		rec := e.send(request(http.MethodPut, "/api/v1/me/ssh-code", body(testPassword), bearer(e.token("alice"))))
		requireStatus(t, rec, http.StatusConflict)
		if errorCode(t, rec) != "conflict" || errorFields(t, rec)["form"] != app.MessageSSHServerOff {
			t.Errorf("answer = %s", rec.Body)
		}
	})
	t.Run("without a token", func(t *testing.T) {
		t.Parallel()
		c := newCodeAPIEnv(t)
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			requireStatus(t, c.send(request(method, "/api/v1/me/ssh-code", body(testPassword))), http.StatusUnauthorized)
		}
	})
	t.Run("a body that is not valid", func(t *testing.T) {
		t.Parallel()
		c := newCodeAPIEnv(t)
		token := c.token("alice")
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			requireStatus(t, c.send(request(method, "/api/v1/me/ssh-code", map[string]any{"current_password": "x", "extra": 1}, bearer(token))), http.StatusUnprocessableEntity)
		}
	})
	t.Run("the settings refuse the field", func(t *testing.T) {
		t.Parallel()
		c := newCodeAPIEnv(t)
		rec := c.send(request(http.MethodPatch, "/api/v1/me/settings", map[string]any{"ssh_code_required": true}, bearer(c.token("alice"))))
		requireStatus(t, rec, http.StatusUnprocessableEntity)
		if f := errorFields(t, rec); f["ssh_code_required"] == nil {
			t.Errorf("fields = %v", f)
		}
	})
}

func TestRemoveTheLastKeyWhileTheCodeIsOnAPI(t *testing.T) {
	t.Parallel()
	c := newCodeAPIEnv(t)
	token := c.token("alice") // before the code is on: a login would ask for it
	c.turnOn("alice")
	keys := decode(t, c.send(request(http.MethodGet, "/api/v1/me/ssh-keys", nil, bearer(token))))["keys"].([]any)
	id := strconv.FormatUint(uint64(keys[0].(map[string]any)["id"].(float64)), 10)
	rec := c.send(request(http.MethodDelete, "/api/v1/me/ssh-keys/"+id, map[string]any{"current_password": testPassword}, bearer(token)))
	requireStatus(t, rec, http.StatusConflict)
	if errorCode(t, rec) != "conflict" || errorFields(t, rec)["form"] != app.MessageSSHCodeNeedsKey {
		t.Errorf("answer = %s", rec.Body)
	}
	// Turn the code off, and the key goes.
	requireStatus(t, c.send(request(http.MethodDelete, "/api/v1/me/ssh-code", map[string]any{"current_password": testPassword}, bearer(token))), http.StatusNoContent)
	requireStatus(t, c.send(request(http.MethodDelete, "/api/v1/me/ssh-keys/"+id, map[string]any{"current_password": testPassword}, bearer(token))), http.StatusNoContent)
}

func TestAdminRemoveSSHCode(t *testing.T) {
	t.Parallel()
	c := newCodeAPIEnv(t)
	c.turnOn("alice")
	path := "/api/v1/admin/users/" + strconv.FormatUint(c.alice.ID, 10) + "/ssh-code"
	adminToken := c.token("admin")

	t.Run("a member", func(t *testing.T) {
		rec := c.send(request(http.MethodDelete, path, nil, bearer(c.token("bob"))))
		requireStatus(t, rec, http.StatusForbidden)
	})
	t.Run("without a token", func(t *testing.T) {
		requireStatus(t, c.send(request(http.MethodDelete, path, nil)), http.StatusUnauthorized)
	})
	t.Run("an unknown user", func(t *testing.T) {
		requireStatus(t, c.send(request(http.MethodDelete, "/api/v1/admin/users/9999/ssh-code", nil, bearer(adminToken))), http.StatusNotFound)
		requireStatus(t, c.send(request(http.MethodDelete, "/api/v1/admin/users/abc/ssh-code", nil, bearer(adminToken))), http.StatusNotFound)
	})
	t.Run("the list shows who has it on", func(t *testing.T) {
		rec := c.send(request(http.MethodGet, "/api/v1/admin/users", nil, bearer(adminToken)))
		requireStatus(t, rec, http.StatusOK)
		users, _ := decode(t, rec)["users"].([]any)
		got := map[string]any{}
		for _, u := range users {
			m, _ := u.(map[string]any)
			got[m["username"].(string)] = m["ssh_code_required"]
		}
		if got["alice"] != true || got["bob"] != false || got["admin"] != false {
			t.Errorf("ssh_code_required = %v", got)
		}
	})
	t.Run("an admin turns it off", func(t *testing.T) {
		for i := 0; i < 2; i++ { // the second call changes nothing
			requireStatus(t, c.send(request(http.MethodDelete, path, nil, bearer(adminToken))), http.StatusNoContent)
		}
		events, _ := c.st.ListAuditEvents(t.Context(), store.AuditFilter{Action: store.ActionUserSSHCodeRemoved}, 10, 0)
		if len(events) != 1 || events[0].ActorID != c.admin.ID || events[0].EntityID != c.alice.ID {
			t.Errorf("events = %+v", events)
		}
		requireStatus(t, c.login(loginBody("alice", testPassword, "")), http.StatusOK)
	})
}
