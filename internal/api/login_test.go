package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/store"
)

func TestLogin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		body       any
		prepare    func(e *testEnv, alice app.User)
		opts       []reqOpt
		wantStatus int
		wantCode   string
		wantFields []string
		wantMsg    string
	}{
		{
			name: "right password", body: map[string]any{"username": "alice", "password": testPassword, "client_name": "Clowder Desktop on laptop"},
			wantStatus: http.StatusOK,
		},
		{
			name: "username in capitals and spaces", body: map[string]any{"username": "  ALICE ", "password": testPassword, "client_name": "app"},
			wantStatus: http.StatusOK,
		},
		{
			name: "wrong password", body: map[string]any{"username": "alice", "password": "not the password", "client_name": "app"},
			wantStatus: http.StatusUnauthorized, wantCode: "unauthenticated", wantMsg: "Wrong username or password.",
		},
		{
			name: "unknown user says the same", body: map[string]any{"username": "nobody", "password": testPassword, "client_name": "app"},
			wantStatus: http.StatusUnauthorized, wantCode: "unauthenticated", wantMsg: "Wrong username or password.",
		},
		{
			name: "disabled user says the same", body: map[string]any{"username": "alice", "password": testPassword, "client_name": "app"},
			prepare: func(e *testEnv, alice app.User) {
				if err := e.st.SetUserDisabled(e.t.Context(), alice.ID, true); err != nil {
					e.t.Fatal(err)
				}
			},
			wantStatus: http.StatusUnauthorized, wantCode: "unauthenticated", wantMsg: "Wrong username or password.",
		},
		{
			name: "no client name", body: map[string]any{"username": "alice", "password": testPassword},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_input", wantFields: []string{"client_name"},
		},
		{
			name: "blank client name", body: map[string]any{"username": "alice", "password": testPassword, "client_name": "   "},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_input", wantFields: []string{"client_name"},
		},
		{
			name: "client name with a control character", body: map[string]any{"username": "alice", "password": testPassword, "client_name": "a\u0007b"},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_input", wantFields: []string{"client_name"},
		},
		{
			name: "client name of 81 characters", body: map[string]any{"username": "alice", "password": testPassword, "client_name": strings.Repeat("é", 81)},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_input", wantFields: []string{"client_name"},
		},
		{
			name: "client name of 80 characters", body: map[string]any{"username": "alice", "password": testPassword, "client_name": strings.Repeat("é", 80)},
			wantStatus: http.StatusOK,
		},
		{
			name: "an unknown field", body: `{"username":"alice","password":"x","client_name":"a","remember":true}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_input", wantFields: []string{"remember"},
		},
		{
			name: "a field of the wrong type", body: `{"username":"alice","password":5,"client_name":"a"}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_input", wantFields: []string{"password"},
		},
		{
			name: "not JSON", body: `{"username":`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_input", wantFields: []string{"body"},
		},
		{
			name: "an empty body", body: ``,
			wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_input", wantFields: []string{"body"},
		},
		{
			name: "two JSON values", body: `{"username":"alice","password":"x","client_name":"a"}{"a":1}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_input", wantFields: []string{"body"},
		},
		{
			name: "an array instead of an object", body: `[1]`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_input",
		},
		{
			name: "a body over 64 KiB", body: `{"username":"` + strings.Repeat("a", 70<<10) + `"}`,
			wantStatus: http.StatusRequestEntityTooLarge, wantCode: "too_large",
		},
		{
			name: "a form instead of JSON", body: "username=alice", opts: []reqOpt{header("Content-Type", "application/x-www-form-urlencoded")},
			wantStatus: http.StatusUnsupportedMediaType, wantCode: "unsupported_media_type",
		},
		{
			name: "no content type", body: `{}`, opts: []reqOpt{func(r *http.Request) { r.Header.Del("Content-Type") }},
			wantStatus: http.StatusUnsupportedMediaType, wantCode: "unsupported_media_type",
		},
		{
			name: "JSON with a charset", body: map[string]any{"username": "alice", "password": testPassword, "client_name": "app"},
			opts: []reqOpt{header("Content-Type", "application/json; charset=UTF-8")}, wantStatus: http.StatusOK,
		},
		{
			name: "JSON in another charset", body: `{}`, opts: []reqOpt{header("Content-Type", "application/json; charset=latin1")},
			wantStatus: http.StatusUnsupportedMediaType, wantCode: "unsupported_media_type",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newTestEnv(t, nil)
			alice := e.addUser("alice", store.RoleMember)
			if tt.prepare != nil {
				tt.prepare(e, alice)
			}
			rec := e.send(request(http.MethodPost, "/api/v1/auth/login", tt.body, tt.opts...))
			requireStatus(t, rec, tt.wantStatus)
			if tt.wantStatus != http.StatusOK {
				if got := errorCode(t, rec); got != tt.wantCode {
					t.Errorf("code = %q, want %q", got, tt.wantCode)
				}
				for _, f := range tt.wantFields {
					if _, ok := errorFields(t, rec)[f]; !ok {
						t.Errorf("fields = %v, want one for %q", errorFields(t, rec), f)
					}
				}
				if tt.wantMsg != "" {
					msg := decode(t, rec)["error"].(map[string]any)["message"]
					if msg != tt.wantMsg {
						t.Errorf("message = %q, want %q", msg, tt.wantMsg)
					}
				}
				if rec.Code == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") != `Bearer realm="clowder"` {
					t.Errorf("WWW-Authenticate = %q", rec.Header().Get("WWW-Authenticate"))
				}
				if strings.Contains(rec.Body.String(), testPassword) {
					t.Error("the answer holds the password")
				}
				return
			}
			body := decode(t, rec)
			token, _ := body["token"].(string)
			if len(token) < 20 {
				t.Fatalf("token = %q", token)
			}
			if user := body["user"].(map[string]any); user["username"] != "alice" || user["id"] != float64(alice.ID) {
				t.Errorf("user = %v", user)
			}
			if body["expires_at"] != "2026-11-25T09:30:00Z" {
				t.Errorf("expires_at = %v, want 30 days after the login", body["expires_at"])
			}
			// The token works as a bearer, and the session is an api one with
			// the client name.
			me := e.send(request(http.MethodGet, "/api/v1/me/sessions", nil, bearer(token)))
			requireStatus(t, me, http.StatusOK)
			list := decode(t, me)["sessions"].([]any)
			if len(list) != 1 {
				t.Fatalf("sessions = %v", list)
			}
			sess := list[0].(map[string]any)
			if sess["kind"] != "api" || sess["current"] != true {
				t.Errorf("session = %v", sess)
			}
			if name, _ := sess["client_name"].(string); name == "" {
				t.Errorf("client_name = %q", name)
			}
			if got := e.noted.all(); len(got) < 2 || got[0] != alice.ID {
				t.Errorf("NoteUser got %v, want the login and the next request to name alice (%d)", got, alice.ID)
			}
		})
	}
}

func TestLoginStoresTheClientName(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", store.RoleMember)
	rec := e.send(request(http.MethodPost, "/api/v1/auth/login",
		map[string]any{"username": "alice", "password": testPassword, "client_name": "  Clowder on laptop  "}))
	requireStatus(t, rec, http.StatusOK)
	token := decode(t, rec)["token"].(string)
	list := decode(t, e.send(request(http.MethodGet, "/api/v1/me/sessions", nil, bearer(token))))["sessions"].([]any)
	if got := list[0].(map[string]any)["client_name"]; got != "Clowder on laptop" {
		t.Errorf("client_name = %q, want it trimmed", got)
	}
}

func TestLoginLimiter(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", store.RoleMember)
	wrong := map[string]any{"username": "alice", "password": "a wrong password", "client_name": "app"}
	for i := 0; i < 5; i++ {
		rec := e.send(request(http.MethodPost, "/api/v1/auth/login", wrong, fromAddr("192.0.2.9:4000")))
		requireStatus(t, rec, http.StatusUnauthorized)
	}
	// The sixth attempt is refused even with the right password, and says how
	// long to wait: the whole window of 15 minutes.
	right := map[string]any{"username": "alice", "password": testPassword, "client_name": "app"}
	rec := e.send(request(http.MethodPost, "/api/v1/auth/login", right, fromAddr("192.0.2.9:4000")))
	requireStatus(t, rec, http.StatusTooManyRequests)
	if got := errorCode(t, rec); got != "rate_limited" {
		t.Errorf("code = %q", got)
	}
	if got := rec.Header().Get("Retry-After"); got != "900" {
		t.Errorf("Retry-After = %q, want 900", got)
	}
	// Another user from another address is not refused.
	e.addUser("bob", store.RoleMember)
	other := map[string]any{"username": "bob", "password": testPassword, "client_name": "app"}
	requireStatus(t, e.send(request(http.MethodPost, "/api/v1/auth/login", other, fromAddr("192.0.2.77:4000"))), http.StatusOK)
	// After the window the login works again.
	e.clk.Advance(app.LoginWindow + 1)
	requireStatus(t, e.send(request(http.MethodPost, "/api/v1/auth/login", right, fromAddr("192.0.2.9:4000"))), http.StatusOK)
}

func TestLoginUsesTheClientIPOption(t *testing.T) {
	t.Parallel()
	var seen []string
	e := newTestEnv(t, func(o *Options) {
		o.ClientIP = func(r *http.Request) string {
			seen = append(seen, r.Header.Get("X-Test-Client"))
			return r.Header.Get("X-Test-Client")
		}
	})
	e.addUser("alice", store.RoleMember)
	wrong := map[string]any{"username": "alice", "password": "a wrong password", "client_name": "app"}
	// Five failures from one client address block that address, whatever the
	// username; a login of another user from it is refused too.
	e.addUser("bob", store.RoleMember)
	for _, name := range []string{"alice", "bob", "carol", "dave", "erin"} {
		wrong["username"] = name
		requireStatus(t, e.send(request(http.MethodPost, "/api/v1/auth/login", wrong, header("X-Test-Client", "198.51.100.1"))), http.StatusUnauthorized)
	}
	right := map[string]any{"username": "bob", "password": testPassword, "client_name": "app"}
	requireStatus(t, e.send(request(http.MethodPost, "/api/v1/auth/login", right, header("X-Test-Client", "198.51.100.1"))), http.StatusTooManyRequests)
	requireStatus(t, e.send(request(http.MethodPost, "/api/v1/auth/login", right, header("X-Test-Client", "198.51.100.2"))), http.StatusOK)
	if len(seen) != 7 {
		t.Errorf("ClientIP was called %d times, want 7", len(seen))
	}
}

func TestSignup(t *testing.T) {
	t.Parallel()
	valid := func(token string) map[string]any {
		return map[string]any{"invitation_token": token, "username": "newcomer", "display_name": "New Comer",
			"password": "a password of 12+", "client_name": "Clowder Desktop"}
	}
	tests := []struct {
		name       string
		build      func(e *testEnv) any
		wantStatus int
		wantCode   string
		wantFields []string
	}{
		{
			name: "member invitation", build: func(e *testEnv) any { return valid(e.invitation(store.RoleMember)) },
			wantStatus: http.StatusCreated,
		},
		{
			name: "admin invitation", build: func(e *testEnv) any { return valid(e.invitation(store.RoleAdmin)) },
			wantStatus: http.StatusCreated,
		},
		{
			name: "unknown invitation", build: func(e *testEnv) any { return valid("not-a-token") },
			wantStatus: http.StatusNotFound, wantCode: "not_found",
		},
		{
			name: "used invitation", build: func(e *testEnv) any {
				token := e.invitation(store.RoleMember)
				if _, err := e.accounts.SignUp(e.t.Context(), token, "first", "First", testPassword); err != nil {
					e.t.Fatal(err)
				}
				return valid(token)
			},
			wantStatus: http.StatusNotFound, wantCode: "not_found",
		},
		{
			name: "expired invitation", build: func(e *testEnv) any {
				token := e.invitation(store.RoleMember)
				e.clk.Advance(8 * 24 * time.Hour)
				return valid(token)
			},
			wantStatus: http.StatusNotFound, wantCode: "not_found",
		},
		{
			name: "fields are named in snake_case", build: func(e *testEnv) any {
				body := valid(e.invitation(store.RoleMember))
				body["username"] = "N!"
				body["display_name"] = ""
				body["password"] = "short"
				return body
			},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_input", wantFields: []string{"username", "display_name", "password"},
		},
		{
			name: "a username that is taken", build: func(e *testEnv) any {
				e.addUser("newcomer", store.RoleMember)
				return valid(e.invitation(store.RoleMember))
			},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_input", wantFields: []string{"username"},
		},
		{
			name: "a bad client name makes no user", build: func(e *testEnv) any {
				body := valid(e.invitation(store.RoleMember))
				body["client_name"] = ""
				return body
			},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_input", wantFields: []string{"client_name"},
		},
		{
			name: "an unknown field", build: func(e *testEnv) any {
				body := valid(e.invitation(store.RoleMember))
				body["role"] = "admin"
				return body
			},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_input", wantFields: []string{"role"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newTestEnv(t, nil)
			in := tt.build(e)
			rec := e.send(request(http.MethodPost, "/api/v1/auth/signup", in))
			requireStatus(t, rec, tt.wantStatus)
			if tt.wantStatus != http.StatusCreated {
				if got := errorCode(t, rec); got != tt.wantCode {
					t.Errorf("code = %q, want %q", got, tt.wantCode)
				}
				for _, f := range tt.wantFields {
					if _, ok := errorFields(t, rec)[f]; !ok {
						t.Errorf("fields = %v, want one for %q", errorFields(t, rec), f)
					}
				}
				return
			}
			body := decode(t, rec)
			token := body["token"].(string)
			user := body["user"].(map[string]any)
			if user["username"] != "newcomer" || user["display_name"] != "New Comer" {
				t.Errorf("user = %v", user)
			}
			// The token is a working api session.
			me := decode(t, e.send(request(http.MethodGet, "/api/v1/me", nil, bearer(token))))
			if me["user"].(map[string]any)["username"] != "newcomer" {
				t.Errorf("me = %v", me)
			}
			// The invitation is used up.
			used := in.(map[string]any)["invitation_token"].(string)
			requireStatus(t, e.send(request(http.MethodGet, "/api/v1/auth/invitations/"+used, nil)), http.StatusNotFound)
		})
	}
}

func TestSignupWithABadClientNameMakesNoUser(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	token := e.invitation(store.RoleMember)
	body := map[string]any{"invitation_token": token, "username": "newcomer", "display_name": "New Comer",
		"password": "a password of 12+", "client_name": ""}
	requireStatus(t, e.send(request(http.MethodPost, "/api/v1/auth/signup", body)), http.StatusUnprocessableEntity)
	if _, err := e.st.UserByUsername(t.Context(), "newcomer"); err == nil {
		t.Error("the user was made")
	}
	// The invitation is still good for a request that is right.
	body["client_name"] = "app"
	requireStatus(t, e.send(request(http.MethodPost, "/api/v1/auth/signup", body)), http.StatusCreated)
}

func TestSignupMakesAnAPISession(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	body := map[string]any{"invitation_token": e.invitation(store.RoleAdmin), "username": "newcomer", "display_name": "New Comer",
		"password": "a password of 12+", "client_name": "Clowder Desktop"}
	rec := e.send(request(http.MethodPost, "/api/v1/auth/signup", body))
	requireStatus(t, rec, http.StatusCreated)
	token := decode(t, rec)["token"].(string)
	list := decode(t, e.send(request(http.MethodGet, "/api/v1/me/sessions", nil, bearer(token))))["sessions"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["kind"] != "api" || list[0].(map[string]any)["client_name"] != "Clowder Desktop" {
		t.Errorf("sessions = %v", list)
	}
	user := decode(t, rec)["user"].(map[string]any)
	if user["role"] != "admin" {
		t.Errorf("role = %v, want the role of the invitation", user["role"])
	}
}

func TestInvitation(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	member := e.invitation(store.RoleMember)
	admin := e.invitation(store.RoleAdmin)
	used := e.invitation(store.RoleMember)
	if _, err := e.accounts.SignUp(t.Context(), used, "first", "First", testPassword); err != nil {
		t.Fatal(err)
	}
	expired := e.invitation(store.RoleMember)
	e.clk.Advance(8 * 24 * time.Hour)
	fresh := e.invitation(store.RoleMember)
	tests := []struct {
		name     string
		token    string
		status   int
		wantRole string
	}{
		{"member", fresh, http.StatusOK, "member"},
		{"unknown", "nope", http.StatusNotFound, ""},
		{"used", used, http.StatusNotFound, ""},
		{"expired", expired, http.StatusNotFound, ""},
		{"expired member", member, http.StatusNotFound, ""},
		{"expired admin", admin, http.StatusNotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(request(http.MethodGet, "/api/v1/auth/invitations/"+tt.token, nil))
			requireStatus(t, rec, tt.status)
			if tt.status != http.StatusOK {
				if errorCode(t, rec) != "not_found" {
					t.Errorf("code = %q", errorCode(t, rec))
				}
				return
			}
			body := decode(t, rec)
			if body["valid"] != true || body["role"] != tt.wantRole || body["expires_at"] != "2026-11-10T09:30:00Z" {
				t.Errorf("body = %v", body)
			}
		})
	}
	// An admin invitation says admin.
	adminToken := e.invitation(store.RoleAdmin)
	if got := decode(t, e.send(request(http.MethodGet, "/api/v1/auth/invitations/"+adminToken, nil)))["role"]; got != "admin" {
		t.Errorf("role = %v", got)
	}
}
