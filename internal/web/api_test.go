package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/clowder/internal/web/static"
)

// jsonPost makes a POST request with a JSON body.
func jsonPost(target, body string, opts ...reqOpt) *http.Request {
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for _, o := range opts {
		o(r)
	}
	return r
}

func TestAPIThroughTheServer(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	alice := e.addUser("alice", "Alice", store.RoleMember)
	rec := e.send(jsonPost("/api/v1/auth/login", `{"username":"alice","password":"`+testPassword+`","client_name":"web test"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("login: status = %d; body:\n%s", rec.Code, rec.Body)
	}
	var login struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &login); err != nil || login.Token == "" {
		t.Fatalf("login body: %v\n%s", err, rec.Body)
	}
	if c := sessionCookieOf(rec); c != nil {
		t.Errorf("the API set a session cookie: %+v", c)
	}

	me := e.send(get("/api/v1/me", withHeader("Authorization", "Bearer "+login.Token)))
	if me.Code != http.StatusOK {
		t.Fatalf("me: status = %d; body:\n%s", me.Code, me.Body)
	}
	if ct := me.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	// The security headers of the pages (D36) cover the API too.
	for name, want := range map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"Cache-Control":                "no-store",
		"Referrer-Policy":              "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
		"X-Frame-Options":              "DENY",
	} {
		if got := me.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if me.Header().Get("Content-Security-Policy") == "" {
		t.Error("no Content-Security-Policy")
	}
	if me.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("the API sends CORS headers")
	}

	// The request log names the pattern of the API and the user, never the
	// token.
	var logged *logEntry
	for _, le := range logEntries(t, e.log) {
		if le.Msg == "request" && le.Pattern == "GET /api/v1/me" {
			logged = &le
		}
	}
	if logged == nil {
		t.Fatalf("no request entry for GET /api/v1/me:\n%s", e.log.String())
	}
	if logged.Status != http.StatusOK {
		t.Errorf("logged status = %d", logged.Status)
	}
	for _, line := range strings.Split(e.log.String(), "\n") {
		if strings.Contains(line, "GET /api/v1/me") && !strings.Contains(line, `"user_id":`+strconv.FormatUint(alice.ID, 10)) {
			t.Errorf("the log line does not name the user: %s", line)
		}
	}
	if strings.Contains(e.log.String(), login.Token) || strings.Contains(e.log.String(), testPassword) {
		t.Error("the log holds a token or a password")
	}
}

func TestAPIIgnoresThePageCookie(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", "Alice", store.RoleMember)
	cookieToken := e.session("alice")
	for _, target := range []string{"/api/v1/me", "/api/v1/me/sessions", "/api/v1/users"} {
		for name, opts := range map[string][]reqOpt{
			"the cookie only":                {withCookie(cookieToken)},
			"the cookie token as the bearer": {withHeader("Authorization", "Bearer "+cookieToken)},
			"the cookie and a wrong bearer":  {withCookie(cookieToken), withHeader("Authorization", "Bearer nope")},
		} {
			rec := e.send(get(target, opts...))
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s with %s: status = %d, want 401", target, name, rec.Code)
			}
			if c := sessionCookieOf(rec); c != nil {
				t.Errorf("%s with %s: the answer touched the cookie: %+v", target, name, c)
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="clowder"` {
				t.Errorf("WWW-Authenticate = %q", got)
			}
		}
	}
	// The cookie still works for the pages after all that.
	if rec := e.send(get("/", withCookie(cookieToken))); rec.Code != http.StatusOK {
		t.Errorf("the page with the cookie: status = %d", rec.Code)
	}
}

// TestUnknownAPIPathsAreJSON checks that the pages' 404 page never answers a
// path under /api/, and that a wrong method is a JSON 405.
func TestUnknownAPIPathsAreJSON(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	tests := []struct {
		name   string
		req    *http.Request
		status int
		code   string
	}{
		{"an unknown path", get("/api/v1/nope"), http.StatusNotFound, "not_found"},
		{"the API root", get("/api/"), http.StatusNotFound, "not_found"},
		{"another version", get("/api/v2/me"), http.StatusNotFound, "not_found"},
		{"a wrong method", httptest.NewRequest(http.MethodPut, "/api/v1/me", nil), http.StatusMethodNotAllowed, "method_not_allowed"},
		{"a post to a get route", jsonPost("/api/v1/me", `{}`), http.StatusMethodNotAllowed, "method_not_allowed"},
		{"a get to the login", get("/api/v1/auth/login"), http.StatusMethodNotAllowed, "method_not_allowed"},
		{"an unknown path with a cookie", get("/api/v1/nope", withCookie("x")), http.StatusNotFound, "not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(tt.req)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d; body:\n%s", rec.Code, tt.status, rec.Body)
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error.Code != tt.code {
				t.Errorf("body = %s (%v), want code %s", rec.Body, err, tt.code)
			}
			if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") || strings.Contains(rec.Body.String(), "<html") {
				t.Errorf("the answer is not JSON: %q", rec.Header().Get("Content-Type"))
			}
			if tt.status == http.StatusMethodNotAllowed && rec.Header().Get("Allow") == "" {
				t.Error("no Allow header")
			}
		})
	}
	// A path outside /api/ is still the page-not-found error page.
	rec := e.send(get("/apix"))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "<h1>404</h1>") {
		t.Errorf("/apix: status %d body %s", rec.Code, rec.Body)
	}
}

// TestWithoutAnAPIThePathsAreThePageNotFound checks that a server made
// without the API serves no /api/ route.
func TestWithoutAnAPIThePathsAreThePageNotFound(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	srv, err := newServer(e.srv.cfg, e.srv.logger, Services{Accounts: e.accounts, Hub: e.hub}, static.FS)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, get("/api/v1/me"))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "<h1>404</h1>") {
		t.Errorf("status %d body %s, want the page-not-found page", rec.Code, rec.Body)
	}
}

// TestAPIPostsAreProtectedAgainstOtherSites checks that a request the browser
// marks cross-site is refused before the handler, as it is for the pages. A
// desktop client sends neither header.
func TestAPIPostsAreProtectedAgainstOtherSites(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", "Alice", store.RoleMember)
	body := `{"username":"alice","password":"` + testPassword + `","client_name":"x"}`
	for name, opt := range map[string]reqOpt{
		"cross-site":   withHeader("Sec-Fetch-Site", "cross-site"),
		"other origin": withHeader("Origin", "https://evil.example"),
	} {
		rec := e.send(jsonPost("/api/v1/auth/login", body, opt))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", name, rec.Code)
		}
	}
	// A client without those headers, and a same-origin browser, pass.
	for name, opt := range map[string]reqOpt{
		"a desktop client": func(*http.Request) {},
		"same origin":      withHeader("Sec-Fetch-Site", "same-origin"),
	} {
		if rec := e.send(jsonPost("/api/v1/auth/login", body, opt)); rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", name, rec.Code)
		}
	}
}

// TestAPILoginUsesTheClientAddress checks that the login limiter of the API
// counts the address as the pages do, including behind a trusted proxy.
func TestAPILoginUsesTheClientAddress(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(c *config.Config) { c.TrustProxyHeaders = true })
	e.addUser("alice", "Alice", store.RoleMember)
	e.addUser("bob", "Bob", store.RoleMember)
	wrong := func(user, ip string) *httptest.ResponseRecorder {
		return e.send(jsonPost("/api/v1/auth/login", `{"username":"`+user+`","password":"wrong password!","client_name":"x"}`,
			fromAddr("10.0.0.1:5000"), withHeader("X-Forwarded-For", "203.0.113.9, "+ip)))
	}
	for _, name := range []string{"alice", "bob", "carol", "dave", "erin"} {
		if rec := wrong(name, "198.51.100.7"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401", name, rec.Code)
		}
	}
	// The address the proxy appended is blocked, whoever logs in from it.
	rec := e.send(jsonPost("/api/v1/auth/login", `{"username":"bob","password":"`+testPassword+`","client_name":"x"}`,
		fromAddr("10.0.0.1:5000"), withHeader("X-Forwarded-For", "203.0.113.9, 198.51.100.7")))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "900" {
		t.Errorf("status %d Retry-After %q, want 429 and 900", rec.Code, rec.Header().Get("Retry-After"))
	}
	// Another address behind the same proxy is not.
	rec = e.send(jsonPost("/api/v1/auth/login", `{"username":"bob","password":"`+testPassword+`","client_name":"x"}`,
		fromAddr("10.0.0.1:5000"), withHeader("X-Forwarded-For", "203.0.113.9, 198.51.100.8")))
	if rec.Code != http.StatusOK {
		t.Errorf("another address: status = %d, want 200", rec.Code)
	}
}

func TestNoteUserAndClientIPFunc(t *testing.T) {
	t.Parallel()
	// A request that did not go through the server is left alone.
	NoteUser(httptest.NewRequest(http.MethodGet, "/", nil), 7)

	tests := []struct {
		name  string
		trust bool
		req   func() *http.Request
		want  string
	}{
		{"the connection", false, func() *http.Request { return get("/", fromAddr("192.0.2.1:1234")) }, "192.0.2.1"},
		{"a header that is not trusted", false, func() *http.Request {
			return get("/", fromAddr("192.0.2.1:1234"), withHeader("X-Forwarded-For", "198.51.100.1"))
		}, "192.0.2.1"},
		{"a trusted header", true, func() *http.Request {
			return get("/", fromAddr("192.0.2.1:1234"), withHeader("X-Forwarded-For", "1.1.1.1, 198.51.100.1"))
		}, "198.51.100.1"},
	}
	for _, tt := range tests {
		if got := ClientIPFunc(tt.trust)(tt.req()); got != tt.want {
			t.Errorf("%s: client IP = %q, want %q", tt.name, got, tt.want)
		}
	}
}
