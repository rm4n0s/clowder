package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
)

// apiToken logs the user in as an API client and returns the bearer token.
func (e *testEnv) apiToken(username, client string) string {
	e.t.Helper()
	res, err := e.accounts.LoginClient(e.t.Context(), app.ClientLogin{
		Username: username, Password: testPassword, ClientIP: "192.0.2.201", Kind: app.SessionAPI, ClientName: client,
	})
	if err != nil {
		e.t.Fatalf("LoginClient: %+v", err)
	}
	return res.Token
}

// sessionsOf lists the sessions of the user of the cookie token.
func (e *testEnv) sessionsOf(current string) []app.SessionInfo {
	e.t.Helper()
	sess, err := e.accounts.SessionActor(e.t.Context(), current)
	if err != nil {
		e.t.Fatalf("SessionActor: %+v", err)
	}
	list, err := e.accounts.Sessions(e.t.Context(), sess.Actor, sess.SessionID)
	if err != nil {
		e.t.Fatalf("Sessions: %+v", err)
	}
	return list
}

var sessionRowID = regexp.MustCompile(`<tr id="session-(\d+)"`)

func TestSessionsPage(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", "Alice", store.RoleMember)
	e.addUser("bob", "Bob", store.RoleMember)
	browser := e.session("alice")
	e.clk.Advance(time.Hour)
	e.apiToken("alice", "Clowder Desktop <b>on</b> laptop")
	e.apiToken("bob", "bobs-secret-app")

	rec := e.send(get("/me/sessions", withCookie(browser)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body:\n%s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	requireBody(t, rec, []string{
		"<title>Sessions · Clowder</title>", "<h1>Sessions</h1>", `<table class="sessions">`,
		// The app session first (the newest), with its name escaped.
		`data-kind="api"`, `<td class="kind">App</td>`, "Clowder Desktop &lt;b&gt;on&lt;/b&gt; laptop",
		`data-kind="browser"`, `<td class="kind">Browser</td>`,
		"This browser", `datetime="2026-10-26T09:30:00Z"`, `datetime="2026-10-26T10:30:00Z"`,
		`id="sidebar-user">Alice</p>`, `<a href="/me/sessions">Sessions</a>`,
	}, "bobs-secret-app", "<b>on</b>", "token")
	ids := sessionRowID.FindAllStringSubmatch(body, -1)
	if len(ids) != 2 {
		t.Fatalf("%d rows, want alice's 2:\n%s", len(ids), body)
	}
	if strings.Count(body, "This browser") != 1 {
		t.Errorf("This browser appears %d times, want once", strings.Count(body, "This browser"))
	}
	// The current row is the browser one.
	cur := regexp.MustCompile(`<tr id="session-\d+" data-kind="browser" class="current">`)
	if !cur.MatchString(body) {
		t.Errorf("the browser row is not the current one:\n%s", body)
	}
	for _, id := range ids {
		requireBody(t, rec, []string{`action="/me/sessions/` + id[1] + `/end"`, `method="post"`, ">End</button>"})
	}
}

func TestSessionsPageNeedsALogin(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", "Alice", store.RoleMember)
	tests := []struct {
		name string
		req  *http.Request
	}{
		{"no cookie", get("/me/sessions")},
		{"an unknown cookie", get("/me/sessions", withCookie("nope"))},
		{"an api token as the cookie", get("/me/sessions", withCookie(e.apiToken("alice", "app")))},
		{"a bearer header only", get("/me/sessions", withHeader("Authorization", "Bearer "+e.apiToken("alice", "app")))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(tt.req)
			if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login?next=%2Fme%2Fsessions" {
				t.Errorf("status %d Location %q, want a redirect to the login page", rec.Code, rec.Header().Get("Location"))
			}
		})
	}
}

func TestSessionsPageShowsTheSidebarLink(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", "Alice", store.RoleMember)
	rec := e.send(get("/", withCookie(e.session("alice"))))
	requireBody(t, rec, []string{`id="sidebar-user">Alice</p>`, `<p class="sidebar-account"><a href="/me/sessions">Sessions</a></p>`})
}

func TestEndSession(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", "Alice", store.RoleMember)
	e.addUser("bob", "Bob", store.RoleMember)
	browser := e.session("alice")
	app1 := e.apiToken("alice", "app one")
	bobs := e.apiToken("bob", "bobs app")
	bobBrowser := e.session("bob")

	idOf := func(token string, current bool, kind app.SessionKind) uint64 {
		t.Helper()
		for _, s := range e.sessionsOf(token) {
			if s.Current == current && s.Kind == kind {
				return s.ID
			}
		}
		t.Fatalf("no session current=%v kind=%s", current, kind)
		return 0
	}
	otherID := idOf(browser, false, app.SessionAPI)
	currentID := idOf(browser, true, app.SessionBrowser)
	bobsID := idOf(bobBrowser, false, app.SessionAPI)
	end := func(id string, token string, opts ...reqOpt) *httptest.ResponseRecorder {
		opts = append([]reqOpt{withCookie(token)}, opts...)
		return e.send(postForm("/me/sessions/"+id+"/end", url.Values{}, opts...))
	}

	// Someone else's session, an unknown one and a wrong one are 404, and
	// nothing changes.
	for _, id := range []string{strconv.FormatUint(bobsID, 10), "99999", "abc", "-1", "0", "99999999999999999999999"} {
		rec := end(id, browser)
		if rec.Code != http.StatusNotFound {
			t.Errorf("ending %s: status = %d, want 404", id, rec.Code)
		}
		requireBody(t, rec, []string{"<h1>404</h1>"})
	}
	if _, err := e.accounts.APISessionActor(t.Context(), bobs); err != nil {
		t.Errorf("bob's session was ended: %+v", err)
	}

	// Another session of the person: the person goes back to the list.
	rec := end(strconv.FormatUint(otherID, 10), browser)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/me/sessions" {
		t.Fatalf("status %d Location %q, want 303 to /me/sessions", rec.Code, rec.Header().Get("Location"))
	}
	if c := sessionCookieOf(rec); c != nil {
		t.Errorf("a cookie was set: %+v", c)
	}
	if _, err := e.accounts.APISessionActor(t.Context(), app1); !isTag(err, app.TagUnauthenticated) {
		t.Errorf("the ended api session still works: %v", err)
	}
	if _, err := e.accounts.SessionActor(t.Context(), browser); err != nil {
		t.Errorf("the browser session was ended: %v", err)
	}

	// The session of the page itself: the cookie is cleared and the person goes
	// to the login page.
	rec = end(strconv.FormatUint(currentID, 10), browser)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("status %d Location %q, want 303 to /login", rec.Code, rec.Header().Get("Location"))
	}
	if c := sessionCookieOf(rec); c == nil || c.Value != "" || c.MaxAge >= 0 {
		t.Errorf("cookie = %+v, want it cleared", c)
	}
	if _, err := e.accounts.SessionActor(t.Context(), browser); !isTag(err, app.TagUnauthenticated) {
		t.Errorf("the ended browser session still works: %v", err)
	}

	// Without a login there is nothing to end.
	rec = e.send(postForm("/me/sessions/1/end", url.Values{}))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Errorf("status %d Location %q, want a redirect to the login page", rec.Code, rec.Header().Get("Location"))
	}
}

func TestEndSessionFromDatastarRedirectsInBand(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", "Alice", store.RoleMember)
	browser := e.session("alice")
	e.apiToken("alice", "app")
	var otherID uint64
	for _, s := range e.sessionsOf(browser) {
		if !s.Current {
			otherID = s.ID
		}
	}
	rec := e.send(postForm("/me/sessions/"+strconv.FormatUint(otherID, 10)+"/end", url.Values{}, withCookie(browser), asDatastar()))
	requireSSE(t, rec)
	requireBody(t, rec, []string{redirectScript("/me/sessions")})
}

func TestEndSessionPublishesUserChanged(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.addUser("alice", "Alice", store.RoleMember)
	browser := e.session("alice")
	e.apiToken("alice", "app")
	var otherID uint64
	for _, s := range e.sessionsOf(browser) {
		if !s.Current {
			otherID = s.ID
		}
	}
	sub := e.hub.Subscribe(t.Context(), live.UserTopic(e.userID("alice")))
	defer sub.Close()
	e.send(postForm("/me/sessions/"+strconv.FormatUint(otherID, 10)+"/end", url.Values{}, withCookie(browser)))
	select {
	case ev := <-sub.C():
		if ev.Kind != live.KindUserChanged {
			t.Errorf("event = %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Error("no user.changed event")
	}
}

// TestLoginStoresTheKindAndTheUserAgent checks that the pages make browser
// sessions named by the user agent, cut to 80 characters.
func TestLoginStoresTheKindAndTheUserAgent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		userAgent string
		want      string
	}{
		{"a user agent", "Mozilla/5.0 (X11; Linux x86_64) Firefox/130.0", "Mozilla/5.0 (X11; Linux x86_64) Firefox/130.0"},
		{"a long user agent", strings.Repeat("ab", 100), strings.Repeat("ab", 40)},
		{"none", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newTestEnv(t, nil)
			e.addUser("alice", "Alice", store.RoleMember)
			opts := []reqOpt{}
			if tt.userAgent != "" {
				opts = append(opts, withHeader("User-Agent", tt.userAgent))
			}
			rec := e.send(postForm("/login", loginForm("alice", testPassword), opts...))
			c := sessionCookieOf(rec)
			if rec.Code != http.StatusSeeOther || c == nil {
				t.Fatalf("login: status %d cookie %v", rec.Code, c)
			}
			list := e.sessionsOf(c.Value)
			if len(list) != 1 || list[0].Kind != app.SessionBrowser || list[0].ClientName != tt.want {
				t.Errorf("sessions = %+v, want one browser session named %q", list, tt.want)
			}
		})
	}
}

func TestSignUpStoresTheKindAndTheUserAgent(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	token := e.invitation()
	form := url.Values{"username": {"newcomer"}, "displayName": {"New Comer"}, "password": {testPassword}}
	rec := e.send(postForm("/invite/"+token, form, withHeader("User-Agent", "Mozilla/5.0 Test")))
	c := sessionCookieOf(rec)
	if rec.Code != http.StatusSeeOther || c == nil {
		t.Fatalf("sign-up: status %d cookie %v", rec.Code, c)
	}
	list := e.sessionsOf(c.Value)
	if len(list) != 1 || list[0].Kind != app.SessionBrowser || list[0].ClientName != "Mozilla/5.0 Test" {
		t.Errorf("sessions = %+v", list)
	}
}

// TestBearerTokensAreIgnoredByThePages checks the other half of the separation
// (SPEC D43): an API token in the cookie, or in the Authorization header, logs
// nobody in on the pages, and the refusal does not delete the session.
func TestBearerTokensAreIgnoredByThePages(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	alice := e.addUser("alice", "Alice", store.RoleMember)
	token := e.apiToken("alice", "app")
	for _, target := range []string{"/", "/rooms", "/me/sessions", "/messages", "/notifications"} {
		for name, req := range map[string]*http.Request{
			"cookie": get(target, withCookie(token)),
			"header": get(target, withHeader("Authorization", "Bearer "+token)),
			"both":   get(target, withCookie(token), withHeader("Authorization", "Bearer "+token)),
		} {
			rec := e.send(req)
			if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
				t.Errorf("%s via %s: status %d Location %q, want a redirect to /login", target, name, rec.Code, rec.Header().Get("Location"))
			}
		}
	}
	// The logout page does not end it either.
	rec := e.send(postForm("/logout", url.Values{}, withCookie(token)))
	if rec.Code != http.StatusFound {
		t.Errorf("logout with an api token: status %d, want the redirect to the login page", rec.Code)
	}
	if _, err := e.accounts.APISessionActor(t.Context(), token); err != nil {
		t.Errorf("the api session was ended by a page: %+v", err)
	}
	if n, _ := e.st.CountSessionsOfUser(t.Context(), alice.ID); n != 1 {
		t.Errorf("%d sessions, want 1", n)
	}
	// The login page itself shows the form to a person with only a token.
	rec = e.send(get("/login", withCookie(token)))
	if rec.Code != http.StatusOK {
		t.Errorf("login page with an api token: status %d, want 200", rec.Code)
	}
}
