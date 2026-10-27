package web

import (
	"context"
	stdErrors "errors"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/clowder/internal/web/views"
	"github.com/rm4n0s/errors"
)

// reqOpt changes a request a test sends.
type reqOpt func(*http.Request)

// withCookie sends the session cookie with the token.
func withCookie(token string) reqOpt {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token}) }
}

// asDatastar marks the request as sent by the Datastar client.
func asDatastar() reqOpt {
	return func(r *http.Request) { r.Header.Set("Datastar-Request", "true") }
}

// withHeader sets a header.
func withHeader(key, value string) reqOpt {
	return func(r *http.Request) { r.Header.Set(key, value) }
}

// fromAddr sets the address of the connection.
func fromAddr(addr string) reqOpt {
	return func(r *http.Request) { r.RemoteAddr = addr }
}

// get makes a GET request.
func get(target string, opts ...reqOpt) *http.Request {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	for _, o := range opts {
		o(r)
	}
	return r
}

// postForm makes a POST request with a urlencoded form, as the Datastar client
// and a browser send it.
func postForm(target string, form url.Values, opts ...reqOpt) *http.Request {
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, o := range opts {
		o(r)
	}
	return r
}

// send runs the request through the whole handler stack.
func (e *testEnv) send(r *http.Request) *httptest.ResponseRecorder {
	e.t.Helper()
	return do(e.srv.Handler(), r)
}

// addUser makes a user with the password testPassword and returns it.
func (e *testEnv) addUser(username, displayName string, role store.Role) app.User {
	e.t.Helper()
	inv, err := e.accounts.CreateSystemInvitation(e.t.Context(), role)
	if err != nil {
		e.t.Fatalf("CreateSystemInvitation: %+v", err)
	}
	u, err := e.accounts.SignUp(e.t.Context(), inv.Token, username, displayName, testPassword)
	if err != nil {
		e.t.Fatalf("SignUp: %+v", err)
	}
	return u
}

// invitation makes an invitation and returns its token.
func (e *testEnv) invitation() string {
	e.t.Helper()
	inv, err := e.accounts.CreateSystemInvitation(e.t.Context(), store.RoleMember)
	if err != nil {
		e.t.Fatalf("CreateSystemInvitation: %+v", err)
	}
	return inv.Token
}

// session logs the user in with the service and returns the cookie token.
func (e *testEnv) session(username string) string {
	e.t.Helper()
	token, _, err := e.accounts.Login(e.t.Context(), username, testPassword, "192.0.2.200")
	if err != nil {
		e.t.Fatalf("Login: %+v", err)
	}
	return token
}

// loginForm is the form of a login.
func loginForm(username, password string) url.Values {
	return url.Values{"username": {username}, "password": {password}}
}

// sessionCookieOf returns the session cookie the answer sets, or nil.
func sessionCookieOf(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	return nil
}

// requireBody fails the test unless the body of rec contains every want and
// none of notWant.
func requireBody(t *testing.T, rec *httptest.ResponseRecorder, want []string, notWant ...string) {
	t.Helper()
	body := rec.Body.String()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("body does not contain %q:\n%s", w, body)
		}
	}
	for _, w := range notWant {
		if strings.Contains(body, w) {
			t.Errorf("body contains %q:\n%s", w, body)
		}
	}
}

// requireSSE fails the test unless rec is a Datastar event stream with status
// 200 (the client drops the body of any other status).
func requireSSE(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body:\n%s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
}

// redirectScript is how the server redirects a Datastar client, as it sits in
// the data-init attribute of the patch (see runInBrowser).
func redirectScript(target string) string {
	return html.EscapeString("window.location.href = " + jsString(target))
}

func TestLoginPage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		target   string
		wantBody []string
		notWant  []string
	}{
		{
			name:   "form",
			target: "/login",
			wantBody: []string{
				"<title>Log in · Clowder</title>", "<h1>Log in</h1>",
				`<form id="login-form" method="post" action="/login"`,
				`data-on:submit="@post(&#39;/login&#39;, {contentType: &#39;form&#39;})"`,
				`name="username"`, `name="password"`, `type="password"`, "Log in</button>",
			},
			notWant: []string{`name="next"`, "<aside", "form-error", "field-error"},
		},
		{
			name:     "next is carried in the form",
			target:   "/login?next=%2Frooms%2F1%3Fa%3Db",
			wantBody: []string{`<input type="hidden" name="next" value="/rooms/1?a=b">`},
		},
		{
			name:    "an unsafe next is dropped",
			target:  "/login?next=https%3A%2F%2Fevil.example",
			notWant: []string{`name="next"`, "evil.example"},
		},
		{
			name:    "a protocol-relative next is dropped",
			target:  "/login?next=%2F%2Fevil.example",
			notWant: []string{`name="next"`, "evil.example"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			rec := e.send(get(tt.target))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			requireBody(t, rec, tt.wantBody, tt.notWant...)
		})
	}
}

func TestLoginPageRedirectsALoggedInUser(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", "Alice", store.RoleMember)
	rec := e.send(get("/login?next=%2Frooms", withCookie(e.session("alice"))))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Errorf("status = %d, Location = %q; want 302 to /", rec.Code, rec.Header().Get("Location"))
	}
}

func TestHome(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", "Alice", store.RoleMember)
	e.addUser("mallory", "<script>alert(1)</script>", store.RoleMember)

	tests := []struct {
		name     string
		user     string
		wantBody []string
		notWant  []string
	}{
		{
			name: "greets the user by display name", user: "alice",
			wantBody: []string{
				"Signed in as Alice", `<aside id="sidebar">`, `<form id="logout-form" method="post" action="/logout"`,
				`data-on:submit="@post(&#39;/logout&#39;, {contentType: &#39;form&#39;})"`, "Log out</button>",
			},
		},
		{
			name: "escapes the display name", user: "mallory",
			wantBody: []string{"Signed in as &lt;script&gt;alert(1)&lt;/script&gt;"},
			notWant:  []string{"<script>alert(1)</script>"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(get("/", withCookie(e.session(tt.user))))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			requireBody(t, rec, tt.wantBody, tt.notWant...)
		})
	}
}

func TestProtectedRoutesNeedASession(t *testing.T) {
	t.Parallel()
	stranger := func(*testEnv) reqOpt { return func(*http.Request) {} }
	tests := []struct {
		name         string
		request      func() *http.Request
		cookie       func(e *testEnv) reqOpt
		wantStatus   int
		wantLocation string // for a redirect
		wantScript   string // for a Datastar request
		wantCleared  bool
	}{
		{
			name: "home without a cookie", request: func() *http.Request { return get("/") }, cookie: stranger,
			wantStatus: http.StatusFound, wantLocation: "/login?next=%2F",
		},
		{
			name: "nested path keeps its query in next", request: func() *http.Request { return get("/rooms/7?tab=board&x=1") }, cookie: stranger,
			wantStatus: http.StatusFound, wantLocation: "/login?next=%2Frooms%2F7%3Ftab%3Dboard%26x%3D1",
		},
		{
			name: "POST has no next", request: func() *http.Request { return postForm("/logout", nil) }, cookie: stranger,
			wantStatus: http.StatusFound, wantLocation: "/login",
		},
		{
			name: "unknown cookie is cleared", request: func() *http.Request { return get("/") },
			cookie:     func(*testEnv) reqOpt { return withCookie("not-a-session") },
			wantStatus: http.StatusFound, wantLocation: "/login?next=%2F", wantCleared: true,
		},
		{
			name: "expired session is cleared", request: func() *http.Request { return get("/") },
			cookie: func(e *testEnv) reqOpt {
				tok := e.session("alice")
				e.clk.Advance(e.srv.cfg.SessionLifetime)
				return withCookie(tok)
			},
			wantStatus: http.StatusFound, wantLocation: "/login?next=%2F", wantCleared: true,
		},
		{
			name: "disabled user", request: func() *http.Request { return get("/") },
			cookie: func(e *testEnv) reqOpt {
				tok := e.session("alice")
				root := e.addUser("root", "Root", store.RoleAdmin)
				err := e.accounts.SetDisabled(e.t.Context(), app.Actor{UserID: root.ID, Username: "root", Role: store.RoleAdmin},
					e.userID("alice"), true)
				if err != nil {
					e.t.Fatal(err)
				}
				return withCookie(tok)
			},
			wantStatus: http.StatusFound, wantLocation: "/login?next=%2F", wantCleared: true,
		},
		{
			name: "Datastar request without a cookie goes to the page it came from",
			request: func() *http.Request {
				return get("/rooms/7/stream", asDatastar(), withHeader("Referer", "http://example.com/rooms/7?tab=board"))
			},
			cookie:     stranger,
			wantStatus: http.StatusOK, wantScript: redirectScript("/login?next=%2Frooms%2F7%3Ftab%3Dboard"),
		},
		{
			name:       "Datastar request without a referer goes to the login page",
			request:    func() *http.Request { return get("/rooms/7/stream", asDatastar()) },
			cookie:     stranger,
			wantStatus: http.StatusOK, wantScript: redirectScript("/login"),
		},
		{
			name: "Datastar request from another site is not trusted for next",
			request: func() *http.Request {
				return get("/rooms/7/stream", asDatastar(), withHeader("Referer", "https://evil.example/x"))
			},
			cookie:     stranger,
			wantStatus: http.StatusOK, wantScript: redirectScript("/login"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			e.addUser("alice", "Alice", store.RoleMember)
			req := tt.request()
			tt.cookie(e)(req)
			rec := e.send(req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body:\n%s", rec.Code, tt.wantStatus, rec.Body)
			}
			if tt.wantLocation != "" && rec.Header().Get("Location") != tt.wantLocation {
				t.Errorf("Location = %q, want %q", rec.Header().Get("Location"), tt.wantLocation)
			}
			if tt.wantScript != "" {
				requireSSE(t, rec)
				requireBody(t, rec, []string{tt.wantScript}, "STREAM-HANDLER")
			}
			c := sessionCookieOf(rec)
			if tt.wantCleared != (c != nil) {
				t.Fatalf("session cookie set = %v, want %v", c != nil, tt.wantCleared)
			}
			if c != nil && (c.Value != "" || c.MaxAge >= 0 || c.Path != "/") {
				t.Errorf("cookie = %+v, want an empty cookie for / with a negative Max-Age", c)
			}
		})
	}
}

// userID returns the ID of the user, found through the service.
func (e *testEnv) userID(username string) uint64 {
	e.t.Helper()
	token := e.session(username)
	sess, err := e.accounts.SessionActor(e.t.Context(), token)
	user := sess.User
	if err != nil {
		e.t.Fatalf("SessionActor: %+v", err)
	}
	return user.ID
}

func TestPublicRoutesNeedNoSession(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	token := e.invitation()
	for _, target := range []string{"/login", "/invite/" + token, "/static/app.css", "/static/datastar.js", "/nope"} {
		t.Run(target, func(t *testing.T) {
			rec := e.send(get(target))
			if rec.Code == http.StatusFound || rec.Code == http.StatusUnauthorized {
				t.Errorf("status = %d: %s asks for a login", rec.Code, target)
			}
		})
	}
}

func TestStaleCookieOnAPublicPage(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	rec := e.send(get("/login", withCookie("stale")))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if c := sessionCookieOf(rec); c == nil || c.MaxAge >= 0 {
		t.Errorf("the stale cookie was not cleared: %+v", c)
	}
}

func TestSessionLookupFailureIs500(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := e.send(get("/", withCookie("x")).WithContext(ctx))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	// The page cannot be rendered on a canceled context either: the plain
	// fallback of render answers. Neither shows the error.
	requireBody(t, rec, nil, "canceled", "QueryFailed")
	var logged *logEntry
	for _, le := range logEntries(t, e.log) {
		if le.Msg == "request failed" {
			logged = &le
		}
	}
	if logged == nil || logged.Err == nil || logged.Err.Tag != store.TagQueryFailed {
		t.Fatalf("the failure was not logged with its tag: %s", e.log.String())
	}
	if !logged.Err.HasRoute("Accounts.SessionActor->Accounts.sessionActor->Store.SessionByTokenHash.QueryFailed") {
		t.Errorf("route = %s", logged.Err.Route())
	}
}

func TestLoginSuccess(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		form         url.Values
		datastar     bool
		mutate       func(*config.Config)
		wantStatus   int
		wantLocation string // plain requests
		wantScript   string // Datastar requests
		wantSecure   bool
	}{
		{
			name: "Datastar to the home page", form: loginForm("alice", testPassword), datastar: true,
			wantStatus: http.StatusOK, wantScript: redirectScript("/"),
		},
		{
			name: "plain post to the home page", form: loginForm("alice", testPassword),
			wantStatus: http.StatusSeeOther, wantLocation: "/",
		},
		{
			name: "username in capitals with spaces", form: loginForm("  ALICE ", testPassword), datastar: true,
			wantStatus: http.StatusOK, wantScript: redirectScript("/"),
		},
		{
			name:     "next",
			form:     url.Values{"username": {"alice"}, "password": {testPassword}, "next": {"/rooms/1?x=y"}},
			datastar: true, wantStatus: http.StatusOK, wantScript: redirectScript("/rooms/1?x=y"),
		},
		{
			name: "https base URL makes the cookie Secure", form: loginForm("alice", testPassword), datastar: true,
			mutate:     func(c *config.Config) { c.BaseURL = "https://clowder.example.com" },
			wantStatus: http.StatusOK, wantScript: redirectScript("/"), wantSecure: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t, tt.mutate)
			alice := e.addUser("alice", "Alice", store.RoleMember)
			var opts []reqOpt
			if tt.datastar {
				opts = append(opts, asDatastar())
			}
			rec := e.send(postForm("/login", tt.form, opts...))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body:\n%s", rec.Code, tt.wantStatus, rec.Body)
			}
			if tt.datastar {
				requireSSE(t, rec)
				requireBody(t, rec, []string{tt.wantScript})
			} else if rec.Header().Get("Location") != tt.wantLocation {
				t.Errorf("Location = %q, want %q", rec.Header().Get("Location"), tt.wantLocation)
			}

			// Every flag of the cookie (SPEC D11, §11).
			c := sessionCookieOf(rec)
			if c == nil {
				t.Fatalf("no session cookie; headers: %v", rec.Header())
			}
			if c.Name != "clowder_session" || len(c.Value) != 43 {
				t.Errorf("cookie name %q, value of %d characters", c.Name, len(c.Value))
			}
			if !c.HttpOnly {
				t.Error("cookie is not HttpOnly")
			}
			if c.SameSite != http.SameSiteLaxMode {
				t.Errorf("SameSite = %v, want Lax", c.SameSite)
			}
			if c.Path != "/" {
				t.Errorf("Path = %q, want /", c.Path)
			}
			if c.Secure != tt.wantSecure {
				t.Errorf("Secure = %v, want %v", c.Secure, tt.wantSecure)
			}
			if want := int(e.srv.cfg.SessionLifetime / time.Second); c.MaxAge != want {
				t.Errorf("Max-Age = %d, want %d", c.MaxAge, want)
			}
			if c.Domain != "" {
				t.Errorf("Domain = %q, want none", c.Domain)
			}
			// The cookie is a working session of the user.
			sess, err := e.accounts.SessionActor(t.Context(), c.Value)
			user := sess.User
			if err != nil || user.ID != alice.ID {
				t.Errorf("SessionActor = %+v, %v; want alice", user, err)
			}
			// The cookie value never appears in the log.
			if strings.Contains(e.log.String(), c.Value) {
				t.Errorf("the session token is in the log:\n%s", e.log.String())
			}
		})
	}
}

func TestLoginSuccessLogsTheUser(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	alice := e.addUser("alice", "Alice", store.RoleMember)
	e.send(postForm("/login", loginForm("alice", testPassword), asDatastar()))
	e.send(get("/", withCookie(e.session("alice"))))
	var withUser []logEntry
	for _, le := range logEntries(t, e.log) {
		if le.Msg == "request" {
			withUser = append(withUser, le)
		}
	}
	if len(withUser) != 2 {
		t.Fatalf("got %d request entries, want 2:\n%s", len(withUser), e.log.String())
	}
	// The user ID is a number in the log line.
	if !strings.Contains(e.log.String(), `"user_id":`+strconv.FormatUint(alice.ID, 10)) {
		t.Errorf("no user_id in the request log:\n%s", e.log.String())
	}
}

func TestLoginNextValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		next string
		want string
	}{
		{"local path", "/rooms", "/rooms"},
		{"local path with query", "/rooms/1?tab=board", "/rooms/1?tab=board"},
		{"root", "/", "/"},
		{"protocol-relative", "//evil.com", "/"},
		{"protocol-relative with path", "//evil.com/rooms", "/"},
		{"absolute https", "https://evil.com", "/"},
		{"absolute http", "http://evil.com/rooms", "/"},
		{"javascript scheme", "javascript:alert(1)", "/"},
		{"backslash after slash", `/\evil`, "/"},
		{"backslash only", `\\evil.com`, "/"},
		{"slash backslash slash", `/\/evil.com`, "/"},
		{"empty", "", "/"},
		{"relative path", "rooms", "/"},
		{"newline in path", "/rooms\nSet-Cookie: x=y", "/"},
		{"tab in path", "/\t/evil.com", "/"},
		{"carriage return", "/rooms\r", "/"},
		{"DEL character", "/rooms\x7f", "/"},
		{"userinfo", "/@evil.com", "/@evil.com"}, // a path: it stays on this host
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := safeNext(tt.next); got != tt.want {
				t.Errorf("safeNext(%q) = %q, want %q", tt.next, got, tt.want)
			}
			// The same through the login form: the redirect goes where safeNext says.
			e := newTestEnv(t, nil)
			e.addUser("alice", "Alice", store.RoleMember)
			form := url.Values{"username": {"alice"}, "password": {testPassword}, "next": {tt.next}}
			rec := e.send(postForm("/login", form, asDatastar()))
			requireSSE(t, rec)
			requireBody(t, rec, []string{redirectScript(tt.want)})
		})
	}
}

func TestLoginFailure(t *testing.T) {
	t.Parallel()
	const wrong = "Wrong username or password."
	tests := []struct {
		name       string
		form       url.Values
		datastar   bool
		prepare    func(e *testEnv)
		wantStatus int
		wantBody   []string
		notWant    []string
		plainPage  bool
	}{
		{
			name: "wrong password", form: loginForm("alice", "not the password"), datastar: true,
			wantStatus: http.StatusOK,
			wantBody:   []string{"event: datastar-patch-elements", `<form id="login-form"`, `<p class="form-error" role="alert">` + wrong + `</p>`, `value="alice"`},
			notWant:    []string{"not the password", `type="password" value`, "<!doctype html>"},
		},
		{
			name: "unknown user", form: loginForm("nobody", "whatever password"), datastar: true,
			wantStatus: http.StatusOK,
			wantBody:   []string{"event: datastar-patch-elements", `<p class="form-error" role="alert">` + wrong + `</p>`, `value="nobody"`},
			notWant:    []string{"whatever password"},
		},
		{
			name: "disabled user", form: loginForm("alice", testPassword), datastar: true,
			prepare: func(e *testEnv) {
				root := e.addUser("root", "Root", store.RoleAdmin)
				err := e.accounts.SetDisabled(e.t.Context(), app.Actor{UserID: root.ID, Username: "root", Role: store.RoleAdmin}, e.userID("alice"), true)
				if err != nil {
					e.t.Fatal(err)
				}
			},
			wantStatus: http.StatusOK,
			wantBody:   []string{`<p class="form-error" role="alert">` + wrong + `</p>`},
		},
		{
			name: "next is kept", datastar: true,
			form:       url.Values{"username": {"alice"}, "password": {"nope nope nope"}, "next": {"/rooms/3"}},
			wantStatus: http.StatusOK,
			wantBody:   []string{`<input type="hidden" name="next" value="/rooms/3">`},
		},
		{
			name: "an unsafe next is not kept", datastar: true,
			form:       url.Values{"username": {"alice"}, "password": {"nope nope nope"}, "next": {"https://evil.example"}},
			wantStatus: http.StatusOK,
			notWant:    []string{"evil.example", `name="next"`},
		},
		{
			name: "plain post shows the whole page with 422", form: loginForm("alice", "not the password"),
			wantStatus: http.StatusUnprocessableEntity, plainPage: true,
			wantBody: []string{"<!doctype html>", "<h1>Log in</h1>", wrong, `value="alice"`},
			notWant:  []string{"not the password"},
		},
		{
			name: "escapes the username", form: loginForm(`"><script>x</script>`, "not the password"), datastar: true,
			wantStatus: http.StatusOK,
			notWant:    []string{"<script>x</script>"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			e.addUser("alice", "Alice", store.RoleMember)
			if tt.prepare != nil {
				tt.prepare(e)
			}
			var opts []reqOpt
			if tt.datastar {
				opts = append(opts, asDatastar())
			}
			rec := e.send(postForm("/login", tt.form, opts...))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body:\n%s", rec.Code, tt.wantStatus, rec.Body)
			}
			if tt.datastar {
				requireSSE(t, rec)
			}
			requireBody(t, rec, tt.wantBody, append(tt.notWant, tt.form.Get("password"))...)
			if c := sessionCookieOf(rec); c != nil {
				t.Errorf("a failed login set a cookie: %+v", c)
			}
		})
	}
}

func TestLoginFailureMessagesAreTheSame(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", "Alice", store.RoleMember)
	// The two answers differ only in the username that was typed.
	wrongPassword := e.send(postForm("/login", loginForm("alice", "not the password"), asDatastar())).Body.String()
	unknownUser := e.send(postForm("/login", loginForm("alice2", "not the password"), asDatastar())).Body.String()
	if strings.ReplaceAll(unknownUser, "alice2", "alice") != wrongPassword {
		t.Errorf("a wrong password and an unknown user are told apart:\n%s\n%s", wrongPassword, unknownUser)
	}
}

func TestLoginRateLimit(t *testing.T) {
	t.Parallel()
	const limited = "Too many attempts. Try again in a few minutes."
	fail := func(e *testEnv, user, addr string, n int, opts ...reqOpt) {
		for range n {
			rec := e.send(postForm("/login", loginForm(user, "not the password"), append([]reqOpt{asDatastar(), fromAddr(addr)}, opts...)...))
			requireBody(t, rec, []string{"Wrong username or password."})
		}
	}
	t.Run("username", func(t *testing.T) {
		e := newTestEnv(t, nil)
		e.addUser("alice", "Alice", store.RoleMember)
		for i := range 5 {
			fail(e, "alice", "192.0.2."+strconv.Itoa(10+i)+":1", 1)
		}
		rec := e.send(postForm("/login", loginForm("alice", testPassword), asDatastar(), fromAddr("192.0.2.99:1")))
		requireSSE(t, rec)
		requireBody(t, rec, []string{`<p class="form-error" role="alert">` + limited + `</p>`, `value="alice"`}, "Wrong username")
		if sessionCookieOf(rec) != nil {
			t.Error("a limited login set a cookie")
		}
		// Plain posts get 429 and the page.
		rec = e.send(postForm("/login", loginForm("alice", testPassword), fromAddr("192.0.2.99:1")))
		if rec.Code != http.StatusTooManyRequests {
			t.Errorf("plain post: status = %d, want 429", rec.Code)
		}
		requireBody(t, rec, []string{limited, "<!doctype html>"})
		// The window passes.
		e.clk.Advance(15 * time.Minute)
		rec = e.send(postForm("/login", loginForm("alice", testPassword), asDatastar(), fromAddr("192.0.2.99:1")))
		requireBody(t, rec, []string{redirectScript("/")})
	})
	t.Run("address", func(t *testing.T) {
		e := newTestEnv(t, nil)
		e.addUser("alice", "Alice", store.RoleMember)
		for _, u := range []string{"u1", "u2", "u3", "u4", "u5"} {
			fail(e, u, "198.51.100.7:4000", 1)
		}
		rec := e.send(postForm("/login", loginForm("alice", testPassword), asDatastar(), fromAddr("198.51.100.7:4001")))
		requireBody(t, rec, []string{limited})
		rec = e.send(postForm("/login", loginForm("alice", testPassword), asDatastar(), fromAddr("198.51.100.8:4001")))
		requireBody(t, rec, []string{redirectScript("/")})
	})
	t.Run("behind a trusted proxy the forwarded address counts", func(t *testing.T) {
		e := newTestEnv(t, func(c *config.Config) { c.TrustProxyHeaders = true })
		e.addUser("alice", "Alice", store.RoleMember)
		for _, u := range []string{"u1", "u2", "u3", "u4", "u5"} {
			// The connection is always the proxy; the client claims other addresses first.
			fail(e, u, "10.0.0.1:1", 1, withHeader("X-Forwarded-For", "1.1.1."+u[1:]+", 203.0.113.9"))
		}
		rec := e.send(postForm("/login", loginForm("alice", testPassword), asDatastar(), fromAddr("10.0.0.1:1"),
			withHeader("X-Forwarded-For", "6.6.6.6, 203.0.113.9")))
		requireBody(t, rec, []string{limited})
		rec = e.send(postForm("/login", loginForm("alice", testPassword), asDatastar(), fromAddr("10.0.0.1:1"),
			withHeader("X-Forwarded-For", "203.0.113.10")))
		requireBody(t, rec, []string{redirectScript("/")})
	})
	t.Run("without the trust flag the header is ignored", func(t *testing.T) {
		e := newTestEnv(t, nil)
		e.addUser("alice", "Alice", store.RoleMember)
		for _, u := range []string{"u1", "u2", "u3", "u4", "u5"} {
			fail(e, u, "10.0.0.1:1", 1, withHeader("X-Forwarded-For", "203.0.113.9"+u))
		}
		// A forged header does not hide the connection's address.
		rec := e.send(postForm("/login", loginForm("alice", testPassword), asDatastar(), fromAddr("10.0.0.1:1"),
			withHeader("X-Forwarded-For", "9.9.9.9")))
		requireBody(t, rec, []string{limited})
	})
}

func TestClientIP(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		addr  string
		xff   []string
		trust bool
		want  string
	}{
		{"connection address", "192.0.2.1:5000", nil, false, "192.0.2.1"},
		{"IPv6 connection", "[2001:db8::1]:5000", nil, false, "2001:db8::1"},
		{"header ignored when not trusted", "192.0.2.1:5000", []string{"203.0.113.9"}, false, "192.0.2.1"},
		{"list ignored when not trusted", "192.0.2.1:5000", []string{"6.6.6.6, 203.0.113.9"}, false, "192.0.2.1"},
		{"trusted: the one address", "10.0.0.1:5000", []string{"203.0.113.9"}, true, "203.0.113.9"},
		{"trusted: the last of a list", "10.0.0.1:5000", []string{"6.6.6.6, 7.7.7.7,  203.0.113.9"}, true, "203.0.113.9"},
		{"trusted: the last of several headers", "10.0.0.1:5000", []string{"6.6.6.6", "203.0.113.9"}, true, "203.0.113.9"},
		{"trusted: IPv6", "10.0.0.1:5000", []string{"2001:db8::7"}, true, "2001:db8::7"},
		{"trusted: IPv4-mapped IPv6 is unmapped", "10.0.0.1:5000", []string{"::ffff:203.0.113.9"}, true, "203.0.113.9"},
		{"trusted but no header", "10.0.0.1:5000", nil, true, "10.0.0.1"},
		{"trusted but empty header", "10.0.0.1:5000", []string{""}, true, "10.0.0.1"},
		{"trusted but garbage", "10.0.0.1:5000", []string{"unknown"}, true, "10.0.0.1"},
		{"trusted but trailing comma", "10.0.0.1:5000", []string{"203.0.113.9,"}, true, "10.0.0.1"},
		{"trusted but address with a port", "10.0.0.1:5000", []string{"203.0.113.9:1234"}, true, "10.0.0.1"},
		{"address without a port", "192.0.2.1", nil, false, "192.0.2.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tt.addr
			for _, v := range tt.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := clientIP(r, tt.trust); got != tt.want {
				t.Errorf("clientIP = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSignUpPage(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	used := e.invitation()
	if _, err := e.accounts.SignUp(t.Context(), used, "first", "First", testPassword); err != nil {
		t.Fatal(err)
	}
	expired := e.invitation()
	e.clk.Advance(e.srv.cfg.InvitationLifetime)
	valid := e.invitation()

	tests := []struct {
		name       string
		token      string
		wantStatus int
		wantBody   []string
		notWant    []string
	}{
		{
			name: "valid", token: valid, wantStatus: http.StatusOK,
			wantBody: []string{
				"<title>Sign up · Clowder</title>", "<h1>Join Clowder</h1>",
				`<form id="signup-form" method="post" action="/invite/` + valid + `"`,
				`data-on:submit="@post(&#39;/invite/` + valid + `&#39;, {contentType: &#39;form&#39;})"`,
				`name="username"`, `name="displayName"`, `name="password"`, "Create account</button>",
			},
			notWant: []string{"field-error", "form-error", "<aside"},
		},
		{name: "used", token: used, wantStatus: http.StatusNotFound, wantBody: []string{"<h1>404</h1>", "This invitation is not valid."}, notWant: []string{"signup-form"}},
		{name: "expired", token: expired, wantStatus: http.StatusNotFound, wantBody: []string{"This invitation is not valid."}},
		{name: "unknown", token: "no-such-token", wantStatus: http.StatusNotFound, wantBody: []string{"This invitation is not valid."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(get("/invite/" + tt.token))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			requireBody(t, rec, tt.wantBody, tt.notWant...)
			if tt.wantStatus == http.StatusNotFound && strings.Contains(rec.Body.String(), tt.token) {
				t.Errorf("the page repeats the token")
			}
		})
	}
}

func signUpForm(username, displayName, password string) url.Values {
	return url.Values{"username": {username}, "displayName": {displayName}, "password": {password}}
}

func TestSignUpSuccess(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		datastar     bool
		wantStatus   int
		wantLocation string
		wantScript   string
	}{
		{"Datastar", true, http.StatusOK, "", redirectScript("/")},
		{"plain post", false, http.StatusSeeOther, "/", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			token := e.invitation()
			var opts []reqOpt
			if tt.datastar {
				opts = append(opts, asDatastar())
			}
			rec := e.send(postForm("/invite/"+token, signUpForm("  Dana ", " Dana D ", testPassword), opts...))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body:\n%s", rec.Code, tt.wantStatus, rec.Body)
			}
			if tt.datastar {
				requireSSE(t, rec)
				requireBody(t, rec, []string{tt.wantScript})
			} else if rec.Header().Get("Location") != tt.wantLocation {
				t.Errorf("Location = %q, want %q", rec.Header().Get("Location"), tt.wantLocation)
			}
			c := sessionCookieOf(rec)
			if c == nil {
				t.Fatal("no session cookie")
			}
			if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
				t.Errorf("cookie = %+v", c)
			}
			sess, err := e.accounts.SessionActor(t.Context(), c.Value)
			user := sess.User
			if err != nil {
				t.Fatalf("the cookie is not a session: %+v", err)
			}
			if user.Username != "dana" || user.DisplayName != "Dana D" {
				t.Errorf("user = %+v", user)
			}
			// The page of the new user greets them, and the link is used up.
			home := e.send(get("/", withCookie(c.Value)))
			requireBody(t, home, []string{"Signed in as Dana D"})
			if again := e.send(get("/invite/" + token)); again.Code != http.StatusNotFound {
				t.Errorf("the used invitation: status = %d, want 404", again.Code)
			}
		})
	}
}

func TestSignUpFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		form       url.Values
		datastar   bool
		prepare    func(e *testEnv)
		wantStatus int
		wantBody   []string
		notWant    []string
	}{
		{
			name: "mistakes are shown next to their fields", datastar: true,
			form:       signUpForm("ab", "Dana", "short"),
			wantStatus: http.StatusOK,
			wantBody: []string{
				"event: datastar-patch-elements", `<form id="signup-form"`,
				`<p class="field-error" id="signup-username-error" role="alert">Use 3 to 32 characters.</p>`,
				`<p class="field-error" id="signup-password-error" role="alert">Use at least 12 characters.</p>`,
				`aria-describedby="signup-username-error"`,
				// The input is kept, the password is not.
				`value="ab"`, `value="Dana"`,
			},
			notWant: []string{"short", "signup-display-name-error", "form-error"},
		},
		{
			name: "all three fields wrong", datastar: true,
			form:       signUpForm("A B", "", ""),
			wantStatus: http.StatusOK,
			wantBody: []string{
				`id="signup-username-error"`, "Use only the letters a to z, digits, _ and -.",
				`id="signup-display-name-error"`, "Use 1 to 64 characters.",
				`id="signup-password-error"`, "Use at least 12 characters.",
			},
		},
		{
			name: "taken username", datastar: true,
			prepare:    func(e *testEnv) { e.addUser("taken", "Taken", store.RoleMember) },
			form:       signUpForm("Taken", "Other", testPassword),
			wantStatus: http.StatusOK,
			wantBody:   []string{"This username is taken.", `value="Taken"`},
			notWant:    []string{testPassword},
		},
		{
			name:       "plain post shows the whole page with 422",
			form:       signUpForm("ab", "Dana", "short"),
			wantStatus: http.StatusUnprocessableEntity,
			wantBody:   []string{"<!doctype html>", "<h1>Join Clowder</h1>", "Use 3 to 32 characters.", `value="ab"`},
			notWant:    []string{"short"},
		},
		{
			name: "escapes what was typed", datastar: true,
			form:       signUpForm(`"><img src=x>`, `<b>Dana</b>`, "short"),
			wantStatus: http.StatusOK,
			notWant:    []string{"<img src=x>", "<b>Dana</b>"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			if tt.prepare != nil {
				tt.prepare(e)
			}
			token := e.invitation()
			var opts []reqOpt
			if tt.datastar {
				opts = append(opts, asDatastar())
			}
			rec := e.send(postForm("/invite/"+token, tt.form, opts...))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body:\n%s", rec.Code, tt.wantStatus, rec.Body)
			}
			if tt.datastar {
				requireSSE(t, rec)
			}
			requireBody(t, rec, tt.wantBody, tt.notWant...)
			if c := sessionCookieOf(rec); c != nil {
				t.Errorf("a failed sign-up set a cookie: %+v", c)
			}
			// The invitation is still good for another try.
			if err := e.accounts.InvitationValid(t.Context(), token); err != nil {
				t.Errorf("the invitation was used by a failed sign-up: %+v", err)
			}
		})
	}
}

func TestSignUpWithABadInvitation(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	used := e.invitation()
	if _, err := e.accounts.SignUp(t.Context(), used, "first", "First", testPassword); err != nil {
		t.Fatal(err)
	}
	expired := e.invitation()
	e.clk.Advance(e.srv.cfg.InvitationLifetime)

	for name, token := range map[string]string{"unknown": "no-such-token", "used": used, "expired": expired} {
		t.Run(name, func(t *testing.T) {
			// Datastar goes to the invitation page, which says it is not valid.
			rec := e.send(postForm("/invite/"+token, signUpForm("dana", "Dana", testPassword), asDatastar()))
			requireSSE(t, rec)
			requireBody(t, rec, []string{redirectScript("/invite/" + token)})
			if sessionCookieOf(rec) != nil {
				t.Error("a cookie was set")
			}
			// A plain post gets the 404 page.
			rec = e.send(postForm("/invite/"+token, signUpForm("dana", "Dana", testPassword)))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", rec.Code)
			}
			requireBody(t, rec, []string{"This invitation is not valid."})
		})
	}
}

func TestSignUpThenLoginFailsRedirectsToLogin(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	token := e.invitation()
	// The address has failed too often to log in now.
	for range 5 {
		e.send(postForm("/login", loginForm("someone", "not the password"), asDatastar(), fromAddr("192.0.2.5:1")))
	}
	rec := e.send(postForm("/invite/"+token, signUpForm("dana", "Dana", testPassword), asDatastar(), fromAddr("192.0.2.5:1")))
	requireSSE(t, rec)
	requireBody(t, rec, []string{redirectScript("/login")})
	if sessionCookieOf(rec) != nil {
		t.Error("a cookie was set")
	}
	// The account exists, and a login from another address works.
	if _, _, err := e.accounts.Login(t.Context(), "dana", testPassword, "192.0.2.6"); err != nil {
		t.Errorf("the account was not made: %+v", err)
	}
	if !strings.Contains(e.log.String(), "login after sign-up failed") {
		t.Errorf("the failure was not logged:\n%s", e.log.String())
	}
}

func TestLogout(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		datastar     bool
		wantStatus   int
		wantLocation string
		wantScript   string
	}{
		{"plain post", false, http.StatusSeeOther, "/login", ""},
		{"Datastar", true, http.StatusOK, "", redirectScript("/login")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			e.addUser("alice", "Alice", store.RoleMember)
			token, other := e.session("alice"), e.session("alice")
			var opts []reqOpt
			if tt.datastar {
				opts = append(opts, asDatastar())
			}
			rec := e.send(postForm("/logout", nil, append(opts, withCookie(token))...))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.datastar {
				requireSSE(t, rec)
				requireBody(t, rec, []string{tt.wantScript})
			} else if rec.Header().Get("Location") != tt.wantLocation {
				t.Errorf("Location = %q, want %q", rec.Header().Get("Location"), tt.wantLocation)
			}
			c := sessionCookieOf(rec)
			if c == nil || c.Value != "" || c.MaxAge >= 0 || c.Path != "/" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
				t.Errorf("the cookie is not cleared: %+v", c)
			}
			// The session is gone; another session of the user is not.
			if _, err := e.accounts.SessionActor(t.Context(), token); !isTag(err, app.TagUnauthenticated) {
				t.Errorf("the session survives the logout: %v", err)
			}
			if _, err := e.accounts.SessionActor(t.Context(), other); err != nil {
				t.Errorf("another session was deleted: %+v", err)
			}
			if rec := e.send(get("/", withCookie(token))); rec.Code != http.StatusFound {
				t.Errorf("the old cookie still opens the home page: %d", rec.Code)
			}
		})
	}
}

func TestLogoutNeedsASession(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	rec := e.send(postForm("/logout", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Errorf("status = %d, Location = %q; want 302 to /login", rec.Code, rec.Header().Get("Location"))
	}
}

func TestCrossOriginPostsAreRefused(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", "Alice", store.RoleMember)
	token := e.invitation()
	session := e.session("alice")

	headers := map[string]map[string]string{
		"cross-site":   {"Sec-Fetch-Site": "cross-site"},
		"same-site":    {"Sec-Fetch-Site": "same-site"},
		"other Origin": {"Origin": "https://evil.example"},
	}
	posts := map[string]func() *http.Request{
		"login":    func() *http.Request { return postForm("/login", loginForm("alice", testPassword)) },
		"signup":   func() *http.Request { return postForm("/invite/"+token, signUpForm("dana", "Dana", testPassword)) },
		"logout":   func() *http.Request { return postForm("/logout", nil, withCookie(session)) },
		"ds login": func() *http.Request { return postForm("/login", loginForm("alice", testPassword), asDatastar()) },
	}
	for hname, hdr := range headers {
		for pname, mk := range posts {
			t.Run(pname+" from "+hname, func(t *testing.T) {
				req := mk()
				for k, v := range hdr {
					req.Header.Set(k, v)
				}
				rec := e.send(req)
				if rec.Code != http.StatusForbidden {
					t.Errorf("status = %d, want 403", rec.Code)
				}
				if sessionCookieOf(rec) != nil {
					t.Error("a refused post set a cookie")
				}
			})
		}
	}
	// Nothing happened: the session is alive, the invitation unused.
	if _, err := e.accounts.SessionActor(t.Context(), session); err != nil {
		t.Errorf("the session was deleted: %+v", err)
	}
	if err := e.accounts.InvitationValid(t.Context(), token); err != nil {
		t.Errorf("the invitation was used: %+v", err)
	}
	// The same posts from the same origin go through.
	for _, hdr := range []map[string]string{{"Sec-Fetch-Site": "same-origin"}, {"Origin": "http://example.com"}} {
		req := postForm("/login", loginForm("alice", testPassword), asDatastar())
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		if rec := e.send(req); rec.Code != http.StatusOK || sessionCookieOf(rec) == nil {
			t.Errorf("same-origin post with %v: status %d", hdr, rec.Code)
		}
	}
}

func TestBadForms(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		req  func(token string) *http.Request
	}{
		{"login with a bad escape", func(string) *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=%zz"))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return r
		}},
		{"login that is too large", func(string) *http.Request {
			body := "username=alice&password=" + strings.Repeat("x", maxFormBytes+1)
			r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return r
		}},
		{"sign-up with a bad escape", func(token string) *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/invite/"+token, strings.NewReader("username=%zz"))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return r
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			rec := e.send(tt.req(e.invitation()))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			requireBody(t, rec, []string{"<h1>400</h1>", messageBadRequest}, "%zz", "invalid URL escape")
			var found bool
			for _, le := range logEntries(t, e.log) {
				if le.Err != nil && le.Err.Tag == tagFormInvalid {
					found = true
					if !le.Err.HasRoute("Server.parseForm.FormInvalid") {
						t.Errorf("route = %s", le.Err.Route())
					}
				}
			}
			if !found {
				t.Errorf("the rejected form was not logged:\n%s", e.log.String())
			}
		})
	}
}

func TestStatusOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"not found", errors.New(app.TagNotFound, "x"), http.StatusNotFound},
		{"forbidden", errors.New(app.TagForbidden, "x"), http.StatusForbidden},
		{"invalid input", errors.New(app.TagInvalidInput, "x"), http.StatusUnprocessableEntity},
		{"conflict", errors.New(app.TagConflict, "x"), http.StatusConflict},
		{"rate limited", errors.New(app.TagRateLimited, "x"), http.StatusTooManyRequests},
		{"too large", errors.New(app.TagTooLarge, "x"), http.StatusRequestEntityTooLarge},
		{"unauthenticated is a redirect", errors.New(app.TagUnauthenticated, "x"), http.StatusFound},
		{"query failed", errors.New(store.TagQueryFailed, "x"), http.StatusInternalServerError},
		{"unknown tag", errors.New("Whatever", "x"), http.StatusInternalServerError},
		{"untagged", context.Canceled, http.StatusInternalServerError},
		{"nil-like foreign error", errors.NewErr("Foreign", context.DeadlineExceeded), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statusOf(tt.err); got != tt.want {
				t.Errorf("statusOf = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestFail(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		err          error
		wantStatus   int
		wantBody     string
		wantLocation string
		wantLogged   bool
	}{
		{"not found", errors.New(app.TagNotFound, "secret detail"), http.StatusNotFound, "Page not found", "", false},
		{"forbidden", errors.New(app.TagForbidden, "secret detail"), http.StatusForbidden, messageForbidden, "", false},
		{"invalid input", errors.New(app.TagInvalidInput, "secret detail"), http.StatusUnprocessableEntity, messageInvalid, "", false},
		{"conflict", errors.New(app.TagConflict, "secret detail"), http.StatusConflict, messageConflict, "", false},
		{"rate limited", errors.New(app.TagRateLimited, "secret detail"), http.StatusTooManyRequests, messageRateLimited, "", false},
		{"too large", errors.New(app.TagTooLarge, "secret detail"), http.StatusRequestEntityTooLarge, messageTooLarge, "", false},
		{"unauthenticated", errors.New(app.TagUnauthenticated, "secret detail"), http.StatusFound, "", "/login?next=%2Ffail", false},
		{"other error", errors.New("Boom", "secret detail", "key", "value"), http.StatusInternalServerError, "Something went wrong", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var srv *Server // set below: the route needs the server it is part of
			e := newTestEnv(t, nil, publicRoute("GET /fail", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				srv.fail(w, r, tt.err)
			})))
			srv = e.srv
			rec := e.send(get("/fail"))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if rec.Header().Get("Location") != tt.wantLocation {
				t.Errorf("Location = %q, want %q", rec.Header().Get("Location"), tt.wantLocation)
			}
			if tt.wantBody != "" {
				requireBody(t, rec, []string{tt.wantBody}, "secret detail")
			}
			var logged *logEntry
			for _, le := range logEntries(t, e.log) {
				if le.Msg == "request failed" {
					logged = &le
				}
			}
			if tt.wantLogged != (logged != nil) {
				t.Fatalf("logged = %v, want %v:\n%s", logged != nil, tt.wantLogged, e.log.String())
			}
			if logged != nil && (logged.Err == nil || logged.Err.Tag != "Boom") {
				t.Errorf("logged error = %+v", logged.Err)
			}
		})
	}
}

// brokenStream is a ResponseWriter that accepts the headers and the flush of
// an event stream but fails to write an event, as a client that went away does.
type brokenStream struct{ *httptest.ResponseRecorder }

func (brokenStream) Write([]byte) (int, error) { return 0, stdErrors.New("broken pipe") }

func TestSSEWriteFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		send  func(s *Server, w http.ResponseWriter, r *http.Request)
		route string
	}{
		{"patch", func(s *Server, w http.ResponseWriter, r *http.Request) {
			s.patch(w, r, views.LoginForm(views.LoginData{}))
		}, "Server.patch.SSEWriteFailed"},
		{"redirect", func(s *Server, w http.ResponseWriter, r *http.Request) {
			s.redirect(w, r, "/", http.StatusSeeOther)
		}, "Server.redirect.SSEWriteFailed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			tt.send(e.srv, brokenStream{httptest.NewRecorder()}, get("/", asDatastar()))

			var entry *logEntry
			for _, le := range logEntries(t, e.log) {
				if le.Err != nil {
					entry = &le
				}
			}
			if entry == nil {
				t.Fatalf("nothing was logged:\n%s", e.log.String())
			}
			if entry.Level != "DEBUG" || entry.Err.Tag != tagSSEWriteFailed || !entry.Err.HasRoute(tt.route) {
				t.Errorf("level %q, tag %q, route %s; want DEBUG, %s, %s", entry.Level, entry.Err.Tag, entry.Err.Route(), tagSSEWriteFailed, tt.route)
			}
		})
	}
}

func TestFormFailedWithOtherErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantMsg    string
		wantLogged bool
	}{
		{"conflict", errors.New(app.TagConflict, "x"), http.StatusConflict, messageConflict, false},
		{"forbidden", errors.New(app.TagForbidden, "x"), http.StatusForbidden, messageForbidden, false},
		{"unexpected", errors.New("Boom", "x"), http.StatusInternalServerError, messageTryAgain, true},
		{"rate limited", errors.New(app.TagRateLimited, "x"), http.StatusTooManyRequests, messageRateLimited, false},
		{"invalid input without fields", errors.New(app.TagInvalidInput, "x"), http.StatusUnprocessableEntity, "", false},
	}
	for _, tt := range tests {
		for _, datastar := range []bool{true, false} {
			t.Run(tt.name+map[bool]string{true: "/datastar", false: "/plain"}[datastar], func(t *testing.T) {
				e := newTestEnv(t, nil)
				var opts []reqOpt
				if datastar {
					opts = append(opts, asDatastar())
				}
				rec := httptest.NewRecorder()
				e.srv.formFailed(rec, get("/login", opts...), tt.err, func(p views.FormErrors) formView {
					d := views.LoginData{Username: "alice", Errors: p}
					return formView{fragment: views.LoginForm(d), page: views.LoginPage(d)}
				})
				wantStatus := tt.wantStatus
				if datastar {
					wantStatus = http.StatusOK
				}
				if rec.Code != wantStatus {
					t.Errorf("status = %d, want %d", rec.Code, wantStatus)
				}
				if tt.wantMsg != "" && !strings.Contains(rec.Body.String(), tt.wantMsg) {
					t.Errorf("body does not contain %q:\n%s", tt.wantMsg, rec.Body)
				}
				if strings.Contains(rec.Body.String(), "Boom") {
					t.Errorf("the error is shown:\n%s", rec.Body)
				}
				if logged := strings.Contains(e.log.String(), `"request failed"`); logged != tt.wantLogged {
					t.Errorf("logged = %v, want %v", logged, tt.wantLogged)
				}
			})
		}
	}
}

func TestSessionInTheContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if _, ok := actorFrom(ctx); ok {
		t.Error("actorFrom found an actor in an empty context")
	}
	if _, ok := userFrom(ctx); ok {
		t.Error("userFrom found a user in an empty context")
	}

	actor := app.Actor{UserID: 7, Username: "alice", Role: store.RoleAdmin}
	user := app.User{ID: 7, Username: "alice", DisplayName: "Alice", Role: store.RoleAdmin}
	ctx = withSession(ctx, actor, user)
	if got, ok := actorFrom(ctx); !ok || got != actor {
		t.Errorf("actorFrom = %+v, %v; want %+v", got, ok, actor)
	}
	if got, ok := userFrom(ctx); !ok || got != user {
		t.Errorf("userFrom = %+v, %v; want %+v", got, ok, user)
	}
}

// TestHandlersSeeTheActor checks what the middleware puts in the context of a
// protected route.
func TestHandlersSeeTheActor(t *testing.T) {
	t.Parallel()
	var gotActor app.Actor
	var gotUser app.User
	e := newTestEnv(t, nil, route{pattern: "GET /whoami", handler: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotActor, _ = actorFrom(r.Context())
		gotUser, _ = userFrom(r.Context())
	})})
	alice := e.addUser("alice", "Alice", store.RoleMember)
	e.send(get("/whoami", withCookie(e.session("alice"))))
	if gotActor != (app.Actor{UserID: alice.ID, Username: "alice", Role: store.RoleMember}) || gotUser.ID != alice.ID {
		t.Errorf("handler saw actor %+v and user %+v", gotActor, gotUser)
	}
}

func TestSessionCookieSlidesWithTheSession(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", "Alice", store.RoleMember)
	token := e.session("alice")
	lifetime := int(e.srv.cfg.SessionLifetime / time.Second)

	// Within a minute of the last use: no new cookie.
	e.clk.Advance(time.Minute)
	if c := sessionCookieOf(e.send(get("/", withCookie(token)))); c != nil {
		t.Errorf("a cookie was sent within the minute: %+v", c)
	}
	// Past a minute: the same token again, with a fresh Max-Age and every flag.
	e.clk.Advance(time.Second)
	rec := e.send(get("/", withCookie(token)))
	c := sessionCookieOf(rec)
	if c == nil {
		t.Fatalf("no cookie after the session was refreshed; headers: %v", rec.Header())
	}
	if c.Value != token || c.MaxAge != lifetime || c.Path != "/" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie = %+v, want the same token with Max-Age %d", c, lifetime)
	}
	// The refresh moved the last use: the next request sends none.
	e.clk.Advance(30 * time.Second)
	if c := sessionCookieOf(e.send(get("/", withCookie(token)))); c != nil {
		t.Errorf("a cookie was sent 30s after the refresh: %+v", c)
	}
	// Public pages with a session slide it too.
	e.clk.Advance(2 * time.Minute)
	if c := sessionCookieOf(e.send(get("/login", withCookie(token)))); c == nil || c.Value != token {
		t.Errorf("the login page did not refresh the cookie: %+v", c)
	}
}

func TestLogoutAfterARefreshEndsWithTheClearingCookie(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", "Alice", store.RoleMember)
	token := e.session("alice")
	e.clk.Advance(2 * time.Minute)
	rec := e.send(postForm("/logout", nil, withCookie(token)))
	var last *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			last = c
		}
	}
	// Browsers apply the cookies in order: the last one decides.
	if last == nil || last.Value != "" || last.MaxAge >= 0 {
		t.Errorf("the last session cookie = %+v, want the clearing one", last)
	}
}
