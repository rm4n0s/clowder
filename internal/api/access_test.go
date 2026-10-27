package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/store"
)

// securedRoutes are the requests of the routes that need a login. A later step
// adds its own to the list; TestEverySecuredRouteIsListed makes sure none is
// forgotten.
func securedRoutes() []struct {
	name, method, path string
	status             int
} {
	return []struct {
		name, method, path string
		status             int
	}{
		{"me", http.MethodGet, "/api/v1/me", http.StatusOK},
		{"sessions", http.MethodGet, "/api/v1/me/sessions", http.StatusOK},
		{"users", http.MethodGet, "/api/v1/users", http.StatusOK},
		{"users with a query", http.MethodGet, "/api/v1/users?q=al", http.StatusOK},
		{"end a session", http.MethodDelete, "/api/v1/me/sessions/999999", http.StatusNotFound},
		{"logout", http.MethodPost, "/api/v1/auth/logout", http.StatusNoContent},
	}
}

// TestEverySecuredRouteIsListed checks that the table above holds every route
// of the API that needs a login: the routes that are public are the ones
// listed here and nothing else.
func TestEverySecuredRouteIsListed(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	public := map[string]bool{
		"POST /api/v1/auth/login":              true,
		"POST /api/v1/auth/signup":             true,
		"GET /api/v1/auth/invitations/{token}": true,
		"GET /api/v1/openapi.json":             true,
		"/api/":                                true,
	}
	listed := map[string]bool{}
	for _, rt := range securedRoutes() {
		// Find the pattern the request matches.
		r := request(rt.method, rt.path, nil)
		e.sendRaw(r)
		listed[r.Pattern] = true
	}
	for _, rt := range e.srv.Routes() {
		if public[rt.Pattern] {
			continue
		}
		if !listed[rt.Pattern] {
			t.Errorf("the route %q needs a login but is not in securedRoutes", rt.Pattern)
		}
	}
	// The public routes are public in the document too.
	for _, op := range operations(t) {
		isPublic := public[op.method+" "+Prefix+op.path]
		sec, hasSec := op.spec["security"].([]any)
		if isPublic != (hasSec && len(sec) == 0) {
			t.Errorf("%s %s: public in the code = %v, public in the document = %v", op.method, op.path, isPublic, hasSec && len(sec) == 0)
		}
	}
}

// TestWhoMayCall is the table of who may call each route that needs a login
// (SPEC §12): a request without a token, with a token of another scheme, with
// one that is unknown, expired or of a browser session, with the session
// cookie only, as a disabled user, and as a member and as an admin.
func TestWhoMayCall(t *testing.T) {
	t.Parallel()
	for _, rt := range securedRoutes() {
		type caller struct {
			name string
			opts func(e *testEnv) []reqOpt
			want int // 0: the status of the route
		}
		callers := []caller{
			{name: "no header", opts: func(*testEnv) []reqOpt { return nil }, want: http.StatusUnauthorized},
			{name: "an empty bearer", opts: func(*testEnv) []reqOpt { return []reqOpt{header("Authorization", "Bearer ")} }, want: http.StatusUnauthorized},
			{name: "only the word Bearer", opts: func(*testEnv) []reqOpt { return []reqOpt{header("Authorization", "Bearer")} }, want: http.StatusUnauthorized},
			{name: "another scheme", opts: func(e *testEnv) []reqOpt {
				return []reqOpt{header("Authorization", "Basic "+e.token("alice"))}
			}, want: http.StatusUnauthorized},
			{name: "the token without a scheme", opts: func(e *testEnv) []reqOpt {
				return []reqOpt{header("Authorization", e.token("alice"))}
			}, want: http.StatusUnauthorized},
			{name: "an unknown token", opts: func(*testEnv) []reqOpt { return []reqOpt{bearer("not-a-token")} }, want: http.StatusUnauthorized},
			{name: "an expired token", opts: func(e *testEnv) []reqOpt {
				token := e.token("alice")
				e.clk.Advance(31 * 24 * time.Hour)
				return []reqOpt{bearer(token)}
			}, want: http.StatusUnauthorized},
			{name: "the token of a browser session", opts: func(e *testEnv) []reqOpt {
				return []reqOpt{bearer(e.browserToken("alice"))}
			}, want: http.StatusUnauthorized},
			{name: "the session cookie and no bearer", opts: func(e *testEnv) []reqOpt {
				token := e.browserToken("alice")
				return []reqOpt{cookie("clowder_session", token)}
			}, want: http.StatusUnauthorized},
			{name: "the token of an api session in the cookie", opts: func(e *testEnv) []reqOpt {
				return []reqOpt{cookie("clowder_session", e.token("alice"))}
			}, want: http.StatusUnauthorized},
			{name: "a valid cookie and an unknown bearer", opts: func(e *testEnv) []reqOpt {
				return []reqOpt{cookie("clowder_session", e.browserToken("alice")), bearer("unknown")}
			}, want: http.StatusUnauthorized},
			{name: "a disabled user", opts: func(e *testEnv) []reqOpt {
				token := e.token("alice")
				if err := e.accounts.SetDisabled(e.t.Context(), actorOf(e, "admin"), e.userID("alice"), true); err != nil {
					e.t.Fatal(err)
				}
				return []reqOpt{bearer(token)}
			}, want: http.StatusUnauthorized},
			{name: "a member", opts: func(e *testEnv) []reqOpt { return []reqOpt{bearer(e.token("alice"))} }},
			{name: "an admin", opts: func(e *testEnv) []reqOpt { return []reqOpt{bearer(e.token("admin"))} }},
			{name: "the scheme in capitals", opts: func(e *testEnv) []reqOpt {
				return []reqOpt{header("Authorization", "BEARER "+e.token("alice"))}
			}},
		}
		for _, c := range callers {
			t.Run(rt.name+"/"+c.name, func(t *testing.T) {
				t.Parallel()
				e := newTestEnv(t, nil)
				e.addUser("admin", store.RoleAdmin)
				e.addUser("alice", store.RoleMember)
				opts := c.opts(e)
				rec := e.send(request(rt.method, rt.path, nil, opts...))
				want := c.want
				if want == 0 {
					want = rt.status
				}
				requireStatus(t, rec, want)
				if want == http.StatusUnauthorized {
					if got := errorCode(t, rec); got != "unauthenticated" {
						t.Errorf("code = %q, want unauthenticated", got)
					}
					if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="clowder"` {
						t.Errorf("WWW-Authenticate = %q", got)
					}
					if strings.Contains(rec.Body.String(), "not-a-token") {
						t.Error("the answer repeats the token")
					}
				}
			})
		}
	}
}

// TestAnExpiredTokenIsDeleted checks that a token found expired is removed, as
// a cookie is, and that a token of the wrong kind is not touched by being
// refused.
func TestAnExpiredTokenIsDeleted(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	alice := e.addUser("alice", store.RoleMember)
	browser := e.browserToken("alice")
	api := e.token("alice")
	requireStatus(t, e.send(request(http.MethodGet, "/api/v1/me", nil, bearer(browser))), http.StatusUnauthorized)
	n, err := e.st.CountSessionsOfUser(t.Context(), alice.ID)
	if err != nil || n != 2 {
		t.Fatalf("sessions after the refused browser token = %d, %v, want 2", n, err)
	}
	e.clk.Advance(31 * 24 * time.Hour)
	requireStatus(t, e.send(request(http.MethodGet, "/api/v1/me", nil, bearer(api))), http.StatusUnauthorized)
	n, err = e.st.CountSessionsOfUser(t.Context(), alice.ID)
	if err != nil || n != 1 {
		t.Fatalf("sessions after the expired api token = %d, %v, want 1 (the browser one)", n, err)
	}
}

// TestASlidingToken checks that using a token moves its expiry on, as it does
// for a cookie.
func TestASlidingToken(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", store.RoleMember)
	token := e.token("alice")
	for i := 0; i < 4; i++ {
		e.clk.Advance(20 * 24 * time.Hour)
		requireStatus(t, e.send(request(http.MethodGet, "/api/v1/me", nil, bearer(token))), http.StatusOK)
	}
}

// TestNoteUser checks that the hook receives the user ID
// of an accepted token and nothing for a refused one.
func TestNoteUser(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	alice := e.addUser("alice", store.RoleMember)
	token := e.token("alice")
	e.send(request(http.MethodGet, "/api/v1/me", nil))
	e.send(request(http.MethodGet, "/api/v1/me", nil, bearer("nope")))
	if got := e.noted.all(); len(got) != 0 {
		t.Errorf("NoteUser got %v for refused requests", got)
	}
	e.send(request(http.MethodGet, "/api/v1/me", nil, bearer(token)))
	if got := e.noted.all(); len(got) != 1 || got[0] != alice.ID {
		t.Errorf("NoteUser got %v, want [%d]", got, alice.ID)
	}
}

// TestNoSecretsInTheLog checks that a request log of the API holds neither
// a token nor a password.
func TestNoSecretsInTheLog(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", store.RoleMember)
	token := e.token("alice")
	e.send(request(http.MethodGet, "/api/v1/me", nil, bearer(token)))
	e.send(request(http.MethodPost, "/api/v1/auth/login",
		map[string]any{"username": "alice", "password": "a wrong password", "client_name": "app"}))
	e.send(request(http.MethodGet, "/api/v1/me", nil, bearer("a-secret-unknown-token")))
	logged := e.log.String()
	for _, secret := range []string{token, "a wrong password", testPassword, "a-secret-unknown-token"} {
		if strings.Contains(logged, secret) {
			t.Errorf("the log holds %q", secret)
		}
	}
}
