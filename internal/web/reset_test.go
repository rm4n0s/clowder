package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/store"
)

// resetLinkRE finds the reset link in a page or a patch.
var resetLinkRE = regexp.MustCompile(`value="(http://localhost:8080/reset/([A-Za-z0-9_-]+))"`)

// resetForm is the form of the reset page.
func resetForm(password, confirm string) url.Values {
	return url.Values{"password": {password}, "passwordConfirm": {confirm}}
}

// resetToken makes a reset link for the user as the admin and returns its token.
func (e *adminEnv) resetToken(u app.User) string {
	e.t.Helper()
	link, err := e.accounts.CreateResetLink(e.t.Context(), app.Actor{UserID: e.admin.ID, Username: "admin", Role: app.RoleAdmin}, u.ID)
	if err != nil {
		e.t.Fatalf("CreateResetLink: %+v", err)
	}
	return link.Token
}

func TestAdminResetLink(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		datastar bool
		target   func(e *adminEnv) app.User
	}{
		{"a member by Datastar", true, func(e *adminEnv) app.User { return e.alice }},
		{"a member by a plain post", false, func(e *adminEnv) app.User { return e.alice }},
		{"the admin themselves", true, func(e *adminEnv) app.User { return e.admin }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newAdminEnv(t)
			target := tt.target(e)
			opts := []reqOpt{e.adminSession}
			if tt.datastar {
				opts = append(opts, asDatastar())
			}
			rec := e.send(postForm(e.path(target, "reset-link"), nil, opts...))
			if tt.datastar {
				requireSSE(t, rec)
				requireBody(t, rec, []string{`id="users-panel"`, "<table"})
			} else {
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200; body:\n%s", rec.Code, rec.Body)
				}
				// The whole page, so a browser without JavaScript sees the link.
				requireBody(t, rec, []string{"<title>Users · Clowder</title>", `id="invite-form"`})
			}
			// The box: for whom, the link, the expiry (24 hours after the fake
			// clock's start) and the copy button.
			requireBody(t, rec, []string{
				`id="reset-link-box"`, "Give this link to " + target.Username, `id="reset-link"`, "readonly",
				"Expires 2026-10-27 09:30 UTC", `id="copy-reset-link"`,
				"navigator.clipboard.writeText(document.getElementById('reset-link').value).then(() => { el.textContent = 'Copied' })",
				"Copy link", "shown only now",
			})
			m := resetLinkRE.FindStringSubmatch(rec.Body.String())
			if m == nil {
				t.Fatalf("no reset link in:\n%s", rec.Body)
			}
			if err := e.accounts.ResetLinkValid(t.Context(), m[2]); err != nil {
				t.Fatalf("ResetLinkValid(%q) = %v; the link %s is not a valid reset link", m[2], err, m[1])
			}

			// The link is not shown again: the page and the next action show none.
			again := e.send(get("/admin/users", e.adminSession))
			requireBody(t, again, []string{`id="users-panel"`}, "reset-link-box", m[2])

			// A newer link replaces it.
			second := e.send(postForm(e.path(target, "reset-link"), nil, opts...))
			m2 := resetLinkRE.FindStringSubmatch(second.Body.String())
			if m2 == nil || m2[2] == m[2] {
				t.Fatalf("second link %v, first %v", m2, m)
			}
			if err := e.accounts.ResetLinkValid(t.Context(), m[2]); !isTag(err, app.TagNotFound) {
				t.Errorf("the first link still works: %v", err)
			}
		})
	}
}

func TestAdminResetLinkUsesTheBaseURL(t *testing.T) {
	t.Parallel()
	tests := []struct{ baseURL, want string }{
		{"https://clowder.example.com", `value="https://clowder.example.com/reset/`},
		{"https://clowder.example.com/", `value="https://clowder.example.com/reset/`},
		{"https://example.com/chat", `value="https://example.com/chat/reset/`},
	}
	for _, tt := range tests {
		t.Run(tt.baseURL, func(t *testing.T) {
			t.Parallel()
			e := newTestEnv(t, func(c *config.Config) { c.BaseURL = tt.baseURL })
			admin := e.addUser("admin", "Admin", app.RoleAdmin)
			rec := e.send(postForm("/admin/users/"+idText(admin.ID)+"/reset-link", nil, withCookie(e.session("admin")), asDatastar()))
			requireSSE(t, rec)
			requireBody(t, rec, []string{tt.want})
		})
	}
}

func TestAdminResetLinkFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		target     func(e *adminEnv) string // the path
		wantStatus int                      // of a plain post
		wantText   string
	}{
		{
			name: "a user who is disabled",
			target: func(e *adminEnv) string {
				if err := e.accounts.SetDisabled(e.t.Context(), app.Actor{UserID: e.admin.ID, Role: app.RoleAdmin}, e.bob.ID, true); err != nil {
					e.t.Fatalf("SetDisabled: %+v", err)
				}
				return e.path(e.bob, "reset-link")
			},
			wantStatus: http.StatusConflict, wantText: messageResetUserDisabled,
		},
		{
			name:       "a user that does not exist",
			target:     func(*adminEnv) string { return "/admin/users/9999/reset-link" },
			wantStatus: http.StatusNotFound, wantText: messageUserGone,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newAdminEnv(t)
			target := tt.target(e)

			rec := e.send(postForm(target, nil, e.adminSession, asDatastar()))
			requireSSE(t, rec)
			requireBody(t, rec, []string{`id="users-panel"`, `<p class="form-error" role="alert">` + tt.wantText + `</p>`}, "reset-link-box")

			rec = e.send(postForm(target, nil, e.adminSession))
			if rec.Code != tt.wantStatus {
				t.Fatalf("plain status = %d, want %d; body:\n%s", rec.Code, tt.wantStatus, rec.Body)
			}
			requireBody(t, rec, []string{"<title>Users · Clowder</title>", tt.wantText}, "reset-link-box")
			if n := len(e.resetEvents()); n != 0 {
				t.Errorf("a refused call wrote %d audit events", n)
			}
		})
	}
	t.Run("a bad ID", func(t *testing.T) {
		t.Parallel()
		e := newAdminEnv(t)
		for _, id := range []string{"abc", "-1", "1.5", "99999999999999999999999"} {
			rec := e.send(postForm("/admin/users/"+id+"/reset-link", nil, e.adminSession, asDatastar()))
			if rec.Code != http.StatusNotFound {
				t.Errorf("id %q: status = %d, want 404", id, rec.Code)
			}
		}
	})
}

// resetEvents returns the audit events about the reset links.
func (e *adminEnv) resetEvents() []store.AuditEvent {
	e.t.Helper()
	rows, err := e.st.ListAuditEvents(e.t.Context(), store.AuditFilter{EntityType: store.EntityUser}, 50, 0)
	if err != nil {
		e.t.Fatalf("ListAuditEvents: %+v", err)
	}
	return rows
}

func TestResetPage(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	used := e.resetToken(e.bob)
	if _, err := e.accounts.ResetPassword(t.Context(), app.ResetInput{Token: used, NewPassword: "a brand new passphrase"}); err != nil {
		t.Fatal(err)
	}
	expired := e.resetToken(e.admin)
	e.clk.Advance(app.ResetLinkLifetime)
	replaced := e.resetToken(e.alice)
	valid := e.resetToken(e.alice) // replaces the one before

	tests := []struct {
		name       string
		token      string
		opts       []reqOpt
		wantStatus int
		wantBody   []string
		notWant    []string
	}{
		{
			name: "valid", token: valid, wantStatus: http.StatusOK,
			wantBody: []string{
				"<title>Set a new password · Clowder</title>", "<h1>Set a new password</h1>",
				`<form id="reset-form" method="post" action="/reset/` + valid + `"`,
				`data-on:submit="@post(&#39;/reset/` + valid + `&#39;, {contentType: &#39;form&#39;})"`,
				`name="password"`, `name="passwordConfirm"`, `autocomplete="new-password"`, "At least 12 characters.",
				"Set the password and log in</button>",
			},
			notWant: []string{"field-error", "form-error", "<aside", valid + "<"},
		},
		{
			name: "valid, for a person who is logged in", token: valid, opts: []reqOpt{e.aliceSession}, wantStatus: http.StatusOK,
			wantBody: []string{`id="reset-form"`},
		},
		{name: "used", token: used, wantStatus: http.StatusNotFound, wantBody: []string{"<h1>404</h1>", "This reset link is not valid."}, notWant: []string{"reset-form"}},
		{name: "expired", token: expired, wantStatus: http.StatusNotFound, wantBody: []string{"This reset link is not valid."}},
		{name: "replaced", token: replaced, wantStatus: http.StatusNotFound, wantBody: []string{"This reset link is not valid."}},
		{name: "unknown", token: "no-such-token", wantStatus: http.StatusNotFound, wantBody: []string{"This reset link is not valid."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(get("/reset/"+tt.token, tt.opts...))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			requireBody(t, rec, tt.wantBody, tt.notWant...)
			if tt.wantStatus == http.StatusNotFound && strings.Contains(rec.Body.String(), tt.token) {
				t.Errorf("the page repeats the token")
			}
			if h := rec.Header().Get("Cache-Control"); h != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", h)
			}
		})
	}
}

func TestResetPageOfADisabledUser(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	token := e.resetToken(e.bob)
	if err := e.accounts.SetDisabled(t.Context(), app.Actor{UserID: e.admin.ID, Role: app.RoleAdmin}, e.bob.ID, true); err != nil {
		t.Fatal(err)
	}
	if rec := e.send(get("/reset/" + token)); rec.Code != http.StatusNotFound {
		t.Errorf("GET: status = %d, want 404", rec.Code)
	}
	rec := e.send(postForm("/reset/"+token, resetForm(testPassword+"x", testPassword+"x")))
	if rec.Code != http.StatusNotFound || sessionCookieOf(rec) != nil {
		t.Errorf("POST: status = %d, cookie = %v; want 404 and none", rec.Code, sessionCookieOf(rec))
	}
}

func TestResetSuccess(t *testing.T) {
	t.Parallel()
	const fresh = "a brand new passphrase"
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
			t.Parallel()
			e := newAdminEnv(t)
			oldBrowser := e.session("alice")
			oldClient := e.apiToken("alice", "Desktop")
			bobSession := e.session("bob")
			token := e.resetToken(e.alice)
			opts := []reqOpt{withHeader("User-Agent", "Firefox/200")}
			if tt.datastar {
				opts = append(opts, asDatastar())
			}
			rec := e.send(postForm("/reset/"+token, resetForm(fresh, fresh), opts...))
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
			if err != nil || sess.User.Username != "alice" {
				t.Fatalf("the cookie is not a session of alice: %+v, %v", sess, err)
			}
			// Every session that was open ended, the one of bob did not.
			if _, err := e.accounts.SessionActor(t.Context(), oldBrowser); !isTag(err, app.TagUnauthenticated) {
				t.Errorf("the old browser session survives: %v", err)
			}
			if _, err := e.accounts.APISessionActor(t.Context(), oldClient); !isTag(err, app.TagUnauthenticated) {
				t.Errorf("the old API session survives: %v", err)
			}
			if _, err := e.accounts.SessionActor(t.Context(), bobSession); err != nil {
				t.Errorf("the session of bob ended: %+v", err)
			}
			list := e.sessionsOf(c.Value)
			if len(list) != 1 || list[0].ClientName != "Firefox/200" {
				t.Errorf("sessions = %+v, want the new one named by the user agent", list)
			}
			// The new session opens the home page; the old password is gone.
			requireBody(t, e.send(get("/", withCookie(c.Value))), []string{"Signed in as Alice"})
			if _, _, err := e.accounts.Login(t.Context(), "alice", fresh, "192.0.2.9"); err != nil {
				t.Errorf("login with the new password: %+v", err)
			}
			if _, _, err := e.accounts.Login(t.Context(), "alice", testPassword, "192.0.2.9"); err == nil {
				t.Error("login with the old password works")
			}
			// The link is used up.
			if again := e.send(get("/reset/" + token)); again.Code != http.StatusNotFound {
				t.Errorf("the used link: status = %d, want 404", again.Code)
			}
		})
	}
}

func TestResetByALoggedInPersonReplacesTheSession(t *testing.T) {
	t.Parallel()
	const fresh = "a brand new passphrase"
	t.Run("the person's own link", func(t *testing.T) {
		t.Parallel()
		e := newAdminEnv(t)
		old := e.session("alice")
		rec := e.send(postForm("/reset/"+e.resetToken(e.alice), resetForm(fresh, fresh), withCookie(old), asDatastar()))
		requireSSE(t, rec)
		c := sessionCookieOf(rec)
		if c == nil || c.Value == old {
			t.Fatalf("cookie = %+v, want a new session", c)
		}
		if _, err := e.accounts.SessionActor(t.Context(), old); !isTag(err, app.TagUnauthenticated) {
			t.Errorf("the old session survives: %v", err)
		}
		if _, err := e.accounts.SessionActor(t.Context(), c.Value); err != nil {
			t.Errorf("the new session: %+v", err)
		}
	})
	t.Run("the link of somebody else", func(t *testing.T) {
		t.Parallel()
		e := newAdminEnv(t)
		old := e.session("bob")
		rec := e.send(postForm("/reset/"+e.resetToken(e.alice), resetForm(fresh, fresh), withCookie(old), asDatastar()))
		requireSSE(t, rec)
		c := sessionCookieOf(rec)
		if c == nil || c.Value == old {
			t.Fatalf("cookie = %+v, want a new session", c)
		}
		if sess, err := e.accounts.SessionActor(t.Context(), c.Value); err != nil || sess.User.Username != "alice" {
			t.Errorf("the new session: %+v, %v", sess, err)
		}
		// The session the browser had ends: the cookie was replaced.
		if _, err := e.accounts.SessionActor(t.Context(), old); !isTag(err, app.TagUnauthenticated) {
			t.Errorf("the session of the previous person survives: %v", err)
		}
	})
}

func TestResetFailure(t *testing.T) {
	t.Parallel()
	const fresh = "a brand new passphrase"
	tests := []struct {
		name       string
		form       url.Values
		datastar   bool
		wantStatus int
		wantBody   []string
		notWant    []string
	}{
		{
			name: "a short password", datastar: true, form: resetForm("short", "short"), wantStatus: http.StatusOK,
			wantBody: []string{
				"event: datastar-patch-elements", `<form id="reset-form"`,
				`<p class="field-error" id="reset-password-error" role="alert">Use at least 12 characters.</p>`,
				`aria-describedby="reset-password-error"`,
			},
			notWant: []string{"short\"", "reset-password-confirm-error", "form-error"},
		},
		{
			name: "two passwords that differ", datastar: true, form: resetForm(fresh, fresh+"!"), wantStatus: http.StatusOK,
			wantBody: []string{
				`<p class="field-error" id="reset-password-confirm-error" role="alert">` + messagePasswordsDiffer + `</p>`,
				`aria-describedby="reset-password-confirm-error"`,
			},
			notWant: []string{fresh, "reset-password-error"},
		},
		{
			name: "no passwords", datastar: true, form: url.Values{}, wantStatus: http.StatusOK,
			wantBody: []string{"Use at least 12 characters."},
		},
		{
			name: "a password that is too long", datastar: true,
			form: resetForm(strings.Repeat("a", 1025), strings.Repeat("a", 1025)), wantStatus: http.StatusOK,
			wantBody: []string{"Use at most 1024 bytes."},
		},
		{
			name: "a plain post shows the whole page with 422", form: resetForm("short", "short"),
			wantStatus: http.StatusUnprocessableEntity,
			wantBody:   []string{"<!doctype html>", "<h1>Set a new password</h1>", "Use at least 12 characters."},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newAdminEnv(t)
			session := e.session("alice")
			token := e.resetToken(e.alice)
			var opts []reqOpt
			if tt.datastar {
				opts = append(opts, asDatastar())
			}
			rec := e.send(postForm("/reset/"+token, tt.form, opts...))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body:\n%s", rec.Code, tt.wantStatus, rec.Body)
			}
			if tt.datastar {
				requireSSE(t, rec)
			}
			requireBody(t, rec, tt.wantBody, tt.notWant...)
			if c := sessionCookieOf(rec); c != nil {
				t.Errorf("a failed reset set a cookie: %+v", c)
			}
			// Nothing changed: the link is good for another try, the old
			// password and the old session work.
			if err := e.accounts.ResetLinkValid(t.Context(), token); err != nil {
				t.Errorf("the link was used by a failed reset: %+v", err)
			}
			if _, err := e.accounts.SessionActor(t.Context(), session); err != nil {
				t.Errorf("a failed reset ended a session: %+v", err)
			}
			if n := len(e.resetEvents()); n != 1 {
				t.Errorf("%d audit events about accounts, want only the link's", n)
			}
		})
	}
}

func TestResetWithABadLink(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	used := e.resetToken(e.alice)
	if _, err := e.accounts.ResetPassword(t.Context(), app.ResetInput{Token: used, NewPassword: "a brand new passphrase"}); err != nil {
		t.Fatal(err)
	}
	expired := e.resetToken(e.bob)
	e.clk.Advance(app.ResetLinkLifetime)

	for name, token := range map[string]string{"unknown": "no-such-token", "used": used, "expired": expired} {
		for _, form := range []url.Values{resetForm("a brand new passphrase", "a brand new passphrase"), resetForm("a brand new passphrase", "different")} {
			t.Run(name, func(t *testing.T) {
				// Datastar goes to the address of the link, which says it is not valid.
				rec := e.send(postForm("/reset/"+token, form, asDatastar()))
				requireSSE(t, rec)
				requireBody(t, rec, []string{redirectScript("/reset/" + token)})
				if sessionCookieOf(rec) != nil {
					t.Error("a cookie was set")
				}
				// A plain post gets the 404 page.
				rec = e.send(postForm("/reset/"+token, form))
				if rec.Code != http.StatusNotFound {
					t.Fatalf("status = %d, want 404", rec.Code)
				}
				requireBody(t, rec, []string{"This reset link is not valid."})
			})
		}
	}
}

func TestResetCrossOriginPostsAreRefused(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	token := e.resetToken(e.alice)
	for name, hdr := range map[string]map[string]string{
		"cross-site":   {"Sec-Fetch-Site": "cross-site"},
		"same-site":    {"Sec-Fetch-Site": "same-site"},
		"other Origin": {"Origin": "https://evil.example"},
	} {
		t.Run(name, func(t *testing.T) {
			req := postForm("/reset/"+token, resetForm("a brand new passphrase", "a brand new passphrase"), asDatastar())
			for k, v := range hdr {
				req.Header.Set(k, v)
			}
			rec := e.send(req)
			if rec.Code != http.StatusForbidden || sessionCookieOf(rec) != nil {
				t.Errorf("status = %d, cookie = %v; want 403 and none", rec.Code, sessionCookieOf(rec))
			}
		})
	}
	if err := e.accounts.ResetLinkValid(t.Context(), token); err != nil {
		t.Errorf("the link was used: %+v", err)
	}
	// From the same origin it goes through.
	req := postForm("/reset/"+token, resetForm("a brand new passphrase", "a brand new passphrase"), asDatastar())
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if rec := e.send(req); rec.Code != http.StatusOK || sessionCookieOf(rec) == nil {
		t.Errorf("same-origin post: status %d", rec.Code)
	}
}

func TestResetTokenIsNotLogged(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	token := e.resetToken(e.alice)
	e.send(get("/reset/" + token + "/nope"))
	e.send(get("/reset/" + token))
	e.send(postForm("/reset/"+token, resetForm("a brand new passphrase", "a brand new passphrase")))
	if strings.Contains(e.log.String(), token) {
		t.Errorf("the log holds the token:\n%s", e.log.String())
	}
}

func TestResetPathForLog(t *testing.T) {
	t.Parallel()
	if got := pathForLog("/reset/secret/more"); got != "/reset/{token}" {
		t.Errorf("pathForLog = %q", got)
	}
}
