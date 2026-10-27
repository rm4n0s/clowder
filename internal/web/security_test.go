package web

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/store"
)

// The tests of this file are the regression tests of the security review of
// step 13.2 (docs/SECURITY.md): each one fails on the code before its fix.

// TestRedirectCannotInjectMarkup is finding S1: the "next" of the login is a
// path of this site, but a path may hold "<" and ">". The redirect that the
// login answers to a Datastar request is a fragment of HTML that the browser
// parses, so a "next" with "</script>" or a quote must stay inside the
// expression it is written in.
func TestRedirectCannotInjectMarkup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		next string
	}{
		{"closing script tag", `/</script><img src=x onerror=alert(1)>`},
		{"html comment", `/<!--`},
		{"double quote", `/" onmouseover="alert(1)`},
		{"single quote", `/'+alert(1)+'`},
		{"backtick", "/`+alert(1)+`"},
		{"datastar action", `/@get('/x')`},
		{"signal", `/$foo`},
		{"backslash escape", `/\u0022`},
		{"space", `/a b`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			e.addUser("alice", "Alice", store.RoleMember)
			form := url.Values{"username": {"alice"}, "password": {testPassword}, "next": {tt.next}}
			rec := e.send(postForm("/login", form, asDatastar()))
			requireSSE(t, rec)
			body := rec.Body.String()
			for _, bad := range []string{"<img", "</script", "<!--", "onerror=", "onmouseover=", "<script"} {
				if strings.Contains(body, bad) {
					t.Errorf("the answer holds %q:\n%s", bad, body)
				}
			}
			// The redirect never runs as an inline script, which the
			// Content-Security-Policy does not allow.
			if strings.Contains(body, "<script") {
				t.Errorf("the redirect is an inline script:\n%s", body)
			}
		})
	}
}

// TestRedirectJSIsOneStringLiteral checks the JavaScript of a redirect: the
// target is one double-quoted literal, whatever characters it has.
func TestRedirectJSIsOneStringLiteral(t *testing.T) {
	t.Parallel()
	tests := []struct {
		target string
		want   string
	}{
		{"/rooms/1", `setTimeout(() => window.location.href = "/rooms/1")`},
		{"/a?x=1&y=2", `setTimeout(() => window.location.href = "/a?x=1&y=2")`},
		{"/</script>", `setTimeout(() => window.location.href = "/\u003c/script>")`},
		{`/"x`, `setTimeout(() => window.location.href = "/\"x")`},
		{"/@get(", `setTimeout(() => window.location.href = "/\u0040get(")`},
		{"/a\u2028b", `setTimeout(() => window.location.href = "/a\u2028b")`},
	}
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			if got := redirectJS(tt.target); got != tt.want {
				t.Errorf("redirectJS(%q) = %s, want %s", tt.target, got, tt.want)
			}
		})
	}
}

// TestSafeNextRefusesMarkupCharacters: a "next" with a character that has no
// place in a path of this site is the home page.
func TestSafeNextRefusesMarkupCharacters(t *testing.T) {
	t.Parallel()
	for _, next := range []string{"/<", "/>", `/"`, "/'", "/`", "/ ", "/{", "/}", "/|", "/^"} {
		if got := safeNext(next); got != "/" {
			t.Errorf("safeNext(%q) = %q, want /", next, got)
		}
	}
	for _, next := range []string{"/rooms/1?tab=board&x=%3Cb%3E", "/search?q=%23tag", "/rooms/7#post-3"} {
		if got := safeNext(next); got != next {
			t.Errorf("safeNext(%q) = %q, want it unchanged", next, got)
		}
	}
}

// directive returns the directive of a Content-Security-Policy with the name.
func directive(csp, name string) string {
	for _, d := range strings.Split(csp, ";") {
		if d = strings.TrimSpace(d); d == name || strings.HasPrefix(d, name+" ") {
			return d
		}
	}
	return ""
}

// wantCSP are the directives every page must carry.
var wantCSP = []string{
	"default-src 'self'",
	"script-src 'self' 'unsafe-eval'",
	"object-src 'none'",
	"base-uri 'none'",
	"form-action 'self'",
	"frame-ancestors 'none'",
}

// TestSecurityHeaders is finding S2: every answer carries the headers of the
// security review, errors and redirects included.
func TestSecurityHeaders(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	p := e.writePost("alice", "general", "hello")
	panicking := route{pattern: "GET /boom", handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })}
	e2 := newTestEnv(t, nil, panicking)
	e2.addUser("alice", "Alice", store.RoleMember)
	tests := []struct {
		name string
		send func() *httptest.ResponseRecorder
	}{
		{"home", func() *httptest.ResponseRecorder { return e.send(get("/", e.cookies["alice"])) }},
		{"room", func() *httptest.ResponseRecorder {
			return e.send(get("/rooms/"+idText(e.general.ID), e.cookies["alice"]))
		}},
		{"post", func() *httptest.ResponseRecorder { return e.send(get(postPathOf(p, ""), e.cookies["alice"])) }},
		{"login", func() *httptest.ResponseRecorder { return e.send(get("/login")) }},
		{"404", func() *httptest.ResponseRecorder { return e.send(get("/nope")) }},
		{"404 of a private room", func() *httptest.ResponseRecorder {
			return e.send(get("/rooms/"+idText(e.secret.ID), e.cookies["carol"]))
		}},
		{"405", func() *httptest.ResponseRecorder { return e.send(httptest.NewRequest(http.MethodPut, "/login", nil)) }},
		{"redirect to the login", func() *httptest.ResponseRecorder { return e.send(get("/rooms")) }},
		{"datastar form answer", func() *httptest.ResponseRecorder {
			return e.send(postForm("/login", loginForm("alice", "wrong password"), asDatastar()))
		}},
		{"cross-origin refusal", func() *httptest.ResponseRecorder {
			return e.send(postForm("/login", loginForm("alice", "x"), withHeader("Sec-Fetch-Site", "cross-site")))
		}},
		{"500 after a panic", func() *httptest.ResponseRecorder { return e2.send(get("/boom", withCookie(e2.session("alice")))) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := tt.send().Header()
			csp := h.Get("Content-Security-Policy")
			for _, want := range wantCSP {
				if !strings.Contains(csp, want) {
					t.Errorf("Content-Security-Policy = %q, missing %q", csp, want)
				}
			}
			if d := directive(csp, "script-src"); strings.Contains(d, "unsafe-inline") {
				t.Errorf("%q allows inline scripts", d)
			}
			want := map[string]string{
				"X-Content-Type-Options": "nosniff",
				"Referrer-Policy":        "same-origin",
				"X-Frame-Options":        "DENY",
				"Cache-Control":          "no-store",
			}
			for k, v := range want {
				if got := h.Get(k); got != v {
					t.Errorf("%s = %q, want %q", k, got, v)
				}
			}
			if got := h.Get("Strict-Transport-Security"); got != "" {
				t.Errorf("Strict-Transport-Security = %q on a server whose base URL is http", got)
			}
		})
	}
}

// TestSecurityHeadersOnStreams: a stream carries the same headers, and is not
// cached.
func TestSecurityHeadersOnStreams(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	c := e.serveStreams().open("alice", e.streamOfRoom("general", "seen=0"), asDatastar())
	for k, v := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "same-origin",
		"X-Frame-Options":        "DENY",
		"Cache-Control":          "no-store",
	} {
		if got := c.header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if !strings.Contains(c.header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Errorf("Content-Security-Policy = %q", c.header.Get("Content-Security-Policy"))
	}
}

// TestSecurityHeadersKeepTheOwnOnesOfStaticFilesAndDownloads: the static files
// are revalidated and a download has its own sandbox policy and cache rule.
func TestSecurityHeadersKeepTheOwnOnesOfStaticFilesAndDownloads(t *testing.T) {
	t.Parallel()
	e := newFilesEnv(t)
	rec := e.send(get("/static/app.css"))
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("static Cache-Control = %q, want no-cache", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("static X-Content-Type-Options = %q", got)
	}
	rec = e.send(get(attachmentPath(e.general, 1), e.cookies["bob"]))
	if rec.Code != http.StatusOK {
		t.Fatalf("download status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != downloadCSP {
		t.Errorf("download Content-Security-Policy = %q, want %q", got, downloadCSP)
	}
	if got := rec.Header().Get("Cache-Control"); got != downloadCacheControl {
		t.Errorf("download Cache-Control = %q, want %q", got, downloadCacheControl)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("download X-Frame-Options = %q", got)
	}
}

// TestHSTSOnlyForHTTPS: Strict-Transport-Security is sent when the base URL is
// https (the proxy terminates TLS), and not when it is http.
func TestHSTSOnlyForHTTPS(t *testing.T) {
	t.Parallel()
	tests := []struct {
		baseURL string
		want    string
	}{
		{"https://chat.example.org", "max-age=31536000"},
		{"http://localhost:8080", ""},
	}
	for _, tt := range tests {
		t.Run(tt.baseURL, func(t *testing.T) {
			e := newTestEnv(t, func(c *config.Config) { c.BaseURL = tt.baseURL })
			if got := e.send(get("/login")).Header().Get("Strict-Transport-Security"); got != tt.want {
				t.Errorf("Strict-Transport-Security = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestStreamsEndAtOnceWhenTheAccountChanges is finding S3: the streams of a
// user who is disabled, or who logs out, end with the redirect to the login
// page at once, not at the next ping, and send nothing of what happens in
// between. A user who gets another role loses the streams too, so that they
// open again with the new role.
func TestStreamsEndAtOnceWhenTheAccountChanges(t *testing.T) {
	t.Parallel()
	streams := []struct {
		name string
		path func(e *roomsEnv) string
	}{
		{"room", func(e *roomsEnv) string { return e.streamOfRoom("general", "seen=0") }},
		{"thread", func(e *roomsEnv) string {
			return "/posts/" + idText(e.writePost("alice", "general", "root").ID) + "/stream?seen=0"
		}},
		{"board", func(e *roomsEnv) string { return e.streamOfBoard("general", "") }},
		{"conversation", func(e *roomsEnv) string { return "/messages/" + idText(e.direct("alice", "bob")) + "/stream" }},
		{"me", func(*roomsEnv) string { return "/me/stream" }},
	}
	changes := []struct {
		name string
		// redirect is true when the stream sends the person to the login page.
		redirect bool
		do       func(e *roomsEnv)
	}{
		{"disabled", true, func(e *roomsEnv) {
			if err := e.accounts.SetDisabled(e.t.Context(), e.actor(e.admin), e.bob.ID, true); err != nil {
				e.t.Fatalf("SetDisabled: %+v", err)
			}
		}},
		{"logged out", true, func(e *roomsEnv) {
			if rec := e.post("bob", "/logout", nil, true); rec.Code != http.StatusOK {
				e.t.Fatalf("logout status = %d", rec.Code)
			}
		}},
		{"made admin", false, func(e *roomsEnv) {
			if err := e.accounts.SetRole(e.t.Context(), e.actor(e.admin), e.bob.ID, app.RoleAdmin); err != nil {
				e.t.Fatalf("SetRole: %+v", err)
			}
		}},
	}
	for _, st := range streams {
		for _, ch := range changes {
			t.Run(st.name+" "+ch.name, func(t *testing.T) {
				e := newRoomsEnv(t)
				// The default ping interval: only the event can end the stream
				// in time.
				path := st.path(e)
				c := e.serveStreams().open("bob", path, asDatastar())
				if c.status != http.StatusOK {
					t.Fatalf("status = %d: %s", c.status, c.body)
				}
				// Whatever the stream starts with is read first.
				c.drain()
				ch.do(e)
				// A change that bob would see if he were still allowed.
				e.writePost("alice", "general", "a secret for the members")
				if ch.redirect {
					requireRedirect(t, patchOf(t, c.next()), "/login")
				}
				c.requireEnd()
			})
		}
	}
}

// TestAccountEventLeavesAValidSessionAlone: a logout of another session of the
// user makes the streams read their session again, but a valid one with the
// same role changes nothing.
func TestAccountEventLeavesAValidSessionAlone(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	ss := e.serveStreams()
	c := ss.open("bob", e.streamOfRoom("general", "seen=0"), asDatastar())
	c.drain()
	other := e.session("bob")
	if err := e.accounts.Logout(e.t.Context(), other); err != nil {
		t.Fatalf("Logout: %+v", err)
	}
	e.writePost("alice", "general", "still here")
	requirePatch(t, c.patches(1)[0], "#feed-posts", "prepend", []string{"still here"}, nil)
}

// TestHTTPServerTimeouts is finding S5: the server cuts a client that is slow
// with its headers or idle, and has no ReadTimeout or WriteTimeout, which would
// end the event streams.
func TestHTTPServerTimeouts(t *testing.T) {
	t.Parallel()
	srv := newTestEnv(t, nil).srv.httpServer()
	if srv.ReadHeaderTimeout <= 0 || srv.IdleTimeout <= 0 || srv.MaxHeaderBytes <= 0 {
		t.Errorf("ReadHeaderTimeout %v, IdleTimeout %v, MaxHeaderBytes %d: want all set", srv.ReadHeaderTimeout, srv.IdleTimeout, srv.MaxHeaderBytes)
	}
	if srv.ReadTimeout != 0 || srv.WriteTimeout != 0 {
		t.Errorf("ReadTimeout %v, WriteTimeout %v: the streams would be cut", srv.ReadTimeout, srv.WriteTimeout)
	}
}

// TestSlowBodyIsCut: a client that sends the headers of a form and then stops
// is answered when the deadline of the body is up, and the connection does not
// stay.
func TestSlowBodyIsCut(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.srv.formTimeout = 150 * time.Millisecond
	ts := httptest.NewServer(e.srv.Handler())
	t.Cleanup(ts.Close)
	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_, err = io.WriteString(conn, "POST /login HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: 100\r\n\r\nusername=ali")
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no answer to a form that stopped: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestBodyDeadlineIsLiftedWhenTheBodyIsRead: a form that arrives in time is
// answered even when the answer takes longer than the deadline.
func TestBodyDeadlineIsLiftedWhenTheBodyIsRead(t *testing.T) {
	t.Parallel()
	slow := route{pattern: "POST /slow", handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-time.After(300 * time.Millisecond):
			w.WriteHeader(http.StatusNoContent)
		case <-r.Context().Done():
			w.WriteHeader(http.StatusInternalServerError)
		}
	}), access: noSession}
	e := newTestEnv(t, nil, slow)
	e.srv.formTimeout = 100 * time.Millisecond
	ts := httptest.NewServer(e.srv.Handler())
	t.Cleanup(ts.Close)
	resp, err := http.Post(ts.URL+"/slow", "application/x-www-form-urlencoded", strings.NewReader("a=b"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
}

// deadlineWriter is a response writer that records the write deadlines set on
// it.
type deadlineWriter struct {
	*httptest.ResponseRecorder
	mu        sync.Mutex
	deadlines []time.Time
}

// SetWriteDeadline records the deadline.
func (w *deadlineWriter) SetWriteDeadline(t time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadlines = append(w.deadlines, t)
	return nil
}

// count returns how many deadlines were set.
func (w *deadlineWriter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.deadlines)
}

// TestStreamWritesHaveADeadline: before the first write, and before every
// ping, a stream gives the write a deadline, so that a client that stops
// reading cannot hold the handler for ever.
func TestStreamWritesHaveADeadline(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	e.srv.pingInterval = 20 * time.Millisecond
	e.srv.writeTimeout = time.Minute
	ctx, cancel := context.WithCancel(t.Context())
	req := get(e.streamOfRoom("general", "seen=0"), e.cookies["alice"], asDatastar()).WithContext(ctx)
	w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.srv.Handler().ServeHTTP(w, req)
	}()
	deadline := time.After(5 * time.Second)
	for w.count() < 3 {
		select {
		case <-deadline:
			t.Fatalf("only %d write deadlines were set", w.count())
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, d := range w.deadlines {
		if d.IsZero() || time.Until(d) > time.Minute+time.Second {
			t.Errorf("deadline = %v, want about a minute from now", d)
		}
	}
}

// TestRequestLogHidesInvitationTokens is finding S6: a request that no route
// matched is logged with its path, but the token of an invitation link is a
// secret, and a path is cut.
func TestRequestLogHidesInvitationTokens(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		method  string
		target  string
		want    string
		notWant string
	}{
		{"wrong method on the sign-up page", http.MethodPut, "/invite/SECRET-TOKEN", "/invite/{token}", "SECRET-TOKEN"},
		{"unknown path below it", http.MethodGet, "/invite/SECRET-TOKEN/more", "/invite/{token}", "SECRET-TOKEN"},
		{"a very long path", http.MethodGet, "/" + strings.Repeat("a", 5000), strings.Repeat("a", 150), strings.Repeat("a", 250)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, buf := newTestServer(t)
			do(s.Handler(), httptest.NewRequest(tt.method, tt.target, nil))
			log := buf.String()
			if !strings.Contains(log, tt.want) || strings.Contains(log, tt.notWant) {
				t.Errorf("the log lacks %q or has %q:\n%.600s", tt.want, tt.notWant, log)
			}
		})
	}
}
