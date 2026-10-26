package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/errors"
)

// adminEnv is a testEnv with an admin, a member and a second member, and the
// session cookie of each.
type adminEnv struct {
	*testEnv
	admin, alice, bob app.User
	adminSession      reqOpt
	aliceSession      reqOpt
}

func newAdminEnv(t *testing.T) *adminEnv {
	t.Helper()
	e := newTestEnv(t, nil)
	a := &adminEnv{testEnv: e}
	a.admin = e.addUser("admin", "Admin", app.RoleAdmin)
	a.alice = e.addUser("alice", "Alice", app.RoleMember)
	a.bob = e.addUser("bob", "Bob", app.RoleMember)
	a.adminSession = withCookie(e.session("admin"))
	a.aliceSession = withCookie(e.session("alice"))
	return a
}

// roleOf returns the role of the user, read through the service.
func (e *adminEnv) roleOf(username string) app.Role {
	e.t.Helper()
	return e.stateOf(username).Role
}

// disabled reports whether the user is disabled.
func (e *adminEnv) disabled(username string) bool {
	e.t.Helper()
	return e.stateOf(username).Disabled
}

func (e *adminEnv) stateOf(username string) app.User {
	e.t.Helper()
	actor := app.Actor{UserID: e.admin.ID, Username: "admin", Role: app.RoleAdmin}
	users, err := e.accounts.Users(e.t.Context(), actor)
	if err != nil {
		e.t.Fatalf("Users: %+v", err)
	}
	for _, u := range users {
		if u.Username == username {
			return u
		}
	}
	e.t.Fatalf("no user %q", username)
	return app.User{}
}

// path returns the path of an action on the user.
func (e *adminEnv) path(u app.User, action string) string {
	return "/admin/users/" + strconv.FormatUint(u.ID, 10) + "/" + action
}

// rowOf returns the HTML of the row of the user in the body.
func rowOf(t *testing.T, body, username string) string {
	t.Helper()
	re := regexp.MustCompile(`(?s)<tr id="user-\d+" data-username="` + username + `">.*?</tr>`)
	row := re.FindString(body)
	if row == "" {
		t.Fatalf("no row for %s in:\n%s", username, body)
	}
	return row
}

func TestAdminUsersPage(t *testing.T) {
	e := newAdminEnv(t)
	if err := e.accounts.SetDisabled(e.t.Context(), app.Actor{UserID: e.admin.ID, Role: app.RoleAdmin}, e.bob.ID, true); err != nil {
		t.Fatalf("SetDisabled: %+v", err)
	}

	rec := e.send(get("/admin/users", e.adminSession))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	requireBody(t, rec, []string{
		"<title>Users · Clowder</title>", `id="users-panel"`, `<table class="users">`,
		"Username", "Display name", "Role", "State", "Created", "2026-10-26",
		`id="invite-form"`, `action="/admin/invitations"`, `<option value="member" selected>`, "Invite someone",
		// The sidebar.
		`id="sidebar-user">Admin</p>`, `<a href="/admin/users">Admin</a>`, `<a href="/">Home</a>`, `id="logout-form"`,
	}, "form-error", "invitation-box")
	body := rec.Body.String()

	adminRow := rowOf(t, body, "admin")
	for _, want := range []string{`class="role">admin`, "Active", "Make member", `value="member"`, e.path(e.admin, "role")} {
		if !strings.Contains(adminRow, want) {
			t.Errorf("admin row lacks %q:\n%s", want, adminRow)
		}
	}
	if strings.Contains(adminRow, "Disable") || strings.Contains(adminRow, "/disabled") {
		t.Errorf("the admin's own row has a disable button:\n%s", adminRow)
	}

	aliceRow := rowOf(t, body, "alice")
	for _, want := range []string{
		"<td>Alice</td>", `class="role">member`, "Active", "Make admin", `value="admin"`,
		e.path(e.alice, "role"), "Disable</button>", `value="true"`, e.path(e.alice, "disabled"),
		`data-on:submit="@post(&#39;` + e.path(e.alice, "disabled") + `&#39;`,
	} {
		if !strings.Contains(aliceRow, want) {
			t.Errorf("alice row lacks %q:\n%s", want, aliceRow)
		}
	}

	bobRow := rowOf(t, body, "bob")
	for _, want := range []string{"Disabled", "Enable</button>", `value="false"`} {
		if !strings.Contains(bobRow, want) {
			t.Errorf("bob row lacks %q:\n%s", want, bobRow)
		}
	}
}

func TestAdminPagesAreInvisibleToMembers(t *testing.T) {
	e := newAdminEnv(t)
	form := url.Values{"role": {"admin"}, "disabled": {"true"}}
	tests := []struct {
		name    string
		request func() *http.Request
	}{
		{"users page", func() *http.Request { return get("/admin/users", e.aliceSession) }},
		{"role", func() *http.Request { return postForm(e.path(e.bob, "role"), form, e.aliceSession) }},
		{"role by Datastar", func() *http.Request { return postForm(e.path(e.bob, "role"), form, e.aliceSession, asDatastar()) }},
		{"disabled", func() *http.Request { return postForm(e.path(e.bob, "disabled"), form, e.aliceSession) }},
		{"disabled by Datastar", func() *http.Request { return postForm(e.path(e.bob, "disabled"), form, e.aliceSession, asDatastar()) }},
		{"role of themselves", func() *http.Request { return postForm(e.path(e.alice, "role"), form, e.aliceSession) }},
		{"invitation", func() *http.Request { return postForm("/admin/invitations", form, e.aliceSession) }},
		{"invitation by Datastar", func() *http.Request { return postForm("/admin/invitations", form, e.aliceSession, asDatastar()) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(tt.request())
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (not 403); body:\n%s", rec.Code, rec.Body)
			}
			requireBody(t, rec, []string{"Page not found"}, "Users", "users-panel", "invitation", "Forbidden", "not allowed")
		})
	}
	if e.roleOf("bob") != app.RoleMember || e.disabled("bob") {
		t.Errorf("a member changed bob: role %s, disabled %v", e.roleOf("bob"), e.disabled("bob"))
	}
}

func TestAdminPagesNeedASession(t *testing.T) {
	e := newAdminEnv(t)
	tests := []struct {
		name    string
		request *http.Request
	}{
		{"users page", get("/admin/users")},
		{"role", postForm("/admin/users/1/role", url.Values{"role": {"member"}})},
		{"disabled", postForm("/admin/users/1/disabled", url.Values{"disabled": {"true"}})},
		{"invitation", postForm("/admin/invitations", url.Values{"role": {"member"}})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(tt.request)
			if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
				t.Errorf("status = %d, Location = %q; want a redirect to the login page", rec.Code, rec.Header().Get("Location"))
			}
		})
	}
}

func TestSidebarAdminLinkIsForAdminsOnly(t *testing.T) {
	e := newAdminEnv(t)
	tests := []struct {
		name    string
		cookie  reqOpt
		page    string
		want    []string
		notWant []string
	}{
		{"admin on the home page", e.adminSession, "/", []string{`<a href="/admin/users">Admin</a>`, `<a href="/">Home</a>`, `id="sidebar-user">Admin</p>`}, nil},
		{"member on the home page", e.aliceSession, "/", []string{`<a href="/">Home</a>`, `id="sidebar-user">Alice</p>`, `id="logout-form"`}, []string{"/admin/users", ">Admin</a>"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(get(tt.page, tt.cookie))
			requireBody(t, rec, tt.want, tt.notWant...)
		})
	}
}

func TestAdminUserActions(t *testing.T) {
	tests := []struct {
		name      string
		action    string
		target    string
		form      url.Values
		setup     func(t *testing.T, e *adminEnv, admin app.Actor)
		datastar  bool
		check     func(t *testing.T, e *adminEnv)
		wantInRow string // in the row of the target, in the patched table
	}{
		{
			name: "make a member an admin", action: "role", target: "alice", form: url.Values{"role": {"admin"}}, datastar: true,
			check:     func(t *testing.T, e *adminEnv) { requireRole(t, e, "alice", app.RoleAdmin) },
			wantInRow: "Make member",
		},
		{
			name: "make an admin a member", action: "role", target: "bob", form: url.Values{"role": {"member"}}, datastar: true,
			setup: func(t *testing.T, e *adminEnv, admin app.Actor) {
				if err := e.accounts.SetRole(t.Context(), admin, e.bob.ID, app.RoleAdmin); err != nil {
					t.Fatalf("SetRole: %+v", err)
				}
			},
			check:     func(t *testing.T, e *adminEnv) { requireRole(t, e, "bob", app.RoleMember) },
			wantInRow: "Make admin",
		},
		{
			name: "disable a member", action: "disabled", target: "alice", form: url.Values{"disabled": {"true"}}, datastar: true,
			check:     func(t *testing.T, e *adminEnv) { requireDisabled(t, e, "alice", true) },
			wantInRow: "Enable</button>",
		},
		{
			name: "enable a member", action: "disabled", target: "bob", form: url.Values{"disabled": {"false"}}, datastar: true,
			setup: func(t *testing.T, e *adminEnv, admin app.Actor) {
				if err := e.accounts.SetDisabled(t.Context(), admin, e.bob.ID, true); err != nil {
					t.Fatalf("SetDisabled: %+v", err)
				}
			},
			check:     func(t *testing.T, e *adminEnv) { requireDisabled(t, e, "bob", false) },
			wantInRow: "Disable</button>",
		},
		{
			name: "role by a plain post is redirected back", action: "role", target: "alice", form: url.Values{"role": {"admin"}},
			check: func(t *testing.T, e *adminEnv) { requireRole(t, e, "alice", app.RoleAdmin) },
		},
		{
			name: "disable by a plain post is redirected back", action: "disabled", target: "alice", form: url.Values{"disabled": {"true"}},
			check: func(t *testing.T, e *adminEnv) { requireDisabled(t, e, "alice", true) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAdminEnv(t)
			if tt.setup != nil {
				tt.setup(t, e, app.Actor{UserID: e.admin.ID, Username: "admin", Role: app.RoleAdmin})
			}
			target := map[string]app.User{"alice": e.alice, "bob": e.bob}[tt.target]
			opts := []reqOpt{e.adminSession}
			if tt.datastar {
				opts = append(opts, asDatastar())
			}
			rec := e.send(postForm(e.path(target, tt.action), tt.form, opts...))

			if tt.datastar {
				requireSSE(t, rec)
				requireBody(t, rec, []string{`id="users-panel"`, "<table", tt.target}, "form-error", `id="invite"`)
				if row := rowOf(t, rec.Body.String(), tt.target); !strings.Contains(row, tt.wantInRow) {
					t.Errorf("the patched row of %s lacks %q:\n%s", tt.target, tt.wantInRow, row)
				}
			} else if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/users" {
				t.Errorf("status = %d, Location = %q; want 303 to /admin/users", rec.Code, rec.Header().Get("Location"))
			}
			tt.check(t, e)
		})
	}
}

func requireDisabled(t *testing.T, e *adminEnv, username string, want bool) {
	t.Helper()
	if got := e.disabled(username); got != want {
		t.Errorf("disabled of %s = %v, want %v", username, got, want)
	}
}

func requireRole(t *testing.T, e *adminEnv, username string, want app.Role) {
	t.Helper()
	if got := e.roleOf(username); got != want {
		t.Errorf("role of %s = %s, want %s", username, got, want)
	}
}

func TestAdminUserActionFailures(t *testing.T) {
	tests := []struct {
		name       string
		action     string
		target     func(e *adminEnv) string // the path
		form       url.Values
		wantStatus int // of a plain post
		wantText   string
	}{
		{
			name: "the last admin cannot become a member", action: "role",
			target:     func(e *adminEnv) string { return e.path(e.admin, "role") },
			form:       url.Values{"role": {"member"}},
			wantStatus: http.StatusConflict, wantText: messageLastAdmin,
		},
		{
			name: "an admin cannot disable themselves", action: "disabled",
			target:     func(e *adminEnv) string { return e.path(e.admin, "disabled") },
			form:       url.Values{"disabled": {"true"}},
			wantStatus: http.StatusUnprocessableEntity, wantText: "You cannot disable your own account.",
		},
		{
			name: "a role that does not exist", action: "role",
			target:     func(e *adminEnv) string { return e.path(e.alice, "role") },
			form:       url.Values{"role": {"owner"}},
			wantStatus: http.StatusUnprocessableEntity, wantText: "Choose admin or member.",
		},
		{
			name: "a disabled value that is not true or false", action: "disabled",
			target:     func(e *adminEnv) string { return e.path(e.alice, "disabled") },
			form:       url.Values{"disabled": {"maybe"}},
			wantStatus: http.StatusUnprocessableEntity, wantText: messageActionInvalid,
		},
		{
			name: "no value", action: "disabled",
			target:     func(e *adminEnv) string { return e.path(e.alice, "disabled") },
			form:       url.Values{},
			wantStatus: http.StatusUnprocessableEntity, wantText: messageActionInvalid,
		},
		{
			name: "a user that does not exist (role)", action: "role",
			target:     func(*adminEnv) string { return "/admin/users/9999/role" },
			form:       url.Values{"role": {"admin"}},
			wantStatus: http.StatusNotFound, wantText: messageUserGone,
		},
		{
			name: "a user that does not exist (disabled)", action: "disabled",
			target:     func(*adminEnv) string { return "/admin/users/9999/disabled" },
			form:       url.Values{"disabled": {"true"}},
			wantStatus: http.StatusNotFound, wantText: messageUserGone,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAdminEnv(t)

			// A Datastar request gets the table patched, with the message
			// above it, and status 200.
			rec := e.send(postForm(tt.target(e), tt.form, e.adminSession, asDatastar()))
			requireSSE(t, rec)
			requireBody(t, rec, []string{`id="users-panel"`, `<p class="form-error" role="alert">` + tt.wantText + `</p>`, "<table"})
			if i, j := strings.Index(rec.Body.String(), "form-error"), strings.Index(rec.Body.String(), "<table"); i > j {
				t.Errorf("the message is not above the table")
			}

			// A plain post gets the whole page with the status of the error.
			rec = e.send(postForm(tt.target(e), tt.form, e.adminSession))
			if rec.Code != tt.wantStatus {
				t.Fatalf("plain status = %d, want %d; body:\n%s", rec.Code, tt.wantStatus, rec.Body)
			}
			requireBody(t, rec, []string{"<title>Users · Clowder</title>", tt.wantText, `id="invite-form"`})

			// Nothing changed.
			if e.roleOf("admin") != app.RoleAdmin || e.roleOf("alice") != app.RoleMember || e.disabled("alice") || e.disabled("admin") {
				t.Error("a failed action changed a user")
			}
		})
	}
}

func TestAdminUserActionWithABadID(t *testing.T) {
	e := newAdminEnv(t)
	for _, id := range []string{"abc", "-1", "1.5", "99999999999999999999999"} {
		for _, action := range []string{"role", "disabled"} {
			t.Run(action+" "+id, func(t *testing.T) {
				rec := e.send(postForm("/admin/users/"+id+"/"+action, url.Values{"role": {"member"}, "disabled": {"true"}}, e.adminSession, asDatastar()))
				if rec.Code != http.StatusNotFound {
					t.Errorf("status = %d, want 404", rec.Code)
				}
			})
		}
	}
}

func TestAdminUserActionOtherFailures(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantText   string
		wantLogged bool
		wantLogin  bool
	}{
		{"forbidden", errors.New(app.TagForbidden, "secret detail"), http.StatusForbidden, messageForbidden, false, false},
		{"other error", errors.New("Boom", "secret detail"), http.StatusInternalServerError, messageTryAgain, true, false},
		{"unauthenticated", errors.New(app.TagUnauthenticated, "secret detail"), http.StatusFound, "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAdminEnv(t)
			srv := e.srv
			actor := app.Actor{UserID: e.admin.ID, Username: "admin", Role: app.RoleAdmin}
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				srv.userChangeFailed(w, r, tt.err, actor, e.admin)
			})
			for _, datastar := range []bool{true, false} {
				var opts []reqOpt
				if datastar {
					opts = append(opts, asDatastar())
				}
				rec := do(h, postForm("/admin/users/1/role", url.Values{}, opts...).WithContext(withSession(e.t.Context(), actor, e.admin)))
				switch {
				case tt.wantLogin:
					if datastar {
						requireSSE(t, rec)
					} else if rec.Code != http.StatusFound {
						t.Errorf("status = %d, want a redirect", rec.Code)
					}
				case datastar:
					requireSSE(t, rec)
					requireBody(t, rec, []string{tt.wantText}, "secret detail")
				default:
					if rec.Code != tt.wantStatus {
						t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
					}
					requireBody(t, rec, []string{tt.wantText}, "secret detail")
				}
			}
			logged := false
			for _, le := range logEntries(t, e.log) {
				if le.Msg == "request failed" {
					logged = true
				}
			}
			if logged != tt.wantLogged {
				t.Errorf("logged = %v, want %v", logged, tt.wantLogged)
			}
		})
	}
}

// linkRE finds the invitation link in a page or a patch.
var linkRE = regexp.MustCompile(`value="(http://localhost:8080/invite/([A-Za-z0-9_-]+))"`)

func TestAdminInvitation(t *testing.T) {
	tests := []struct {
		name     string
		role     string
		datastar bool
		wantRole app.Role
	}{
		{"a member by Datastar", "member", true, app.RoleMember},
		{"an admin by Datastar", "admin", true, app.RoleAdmin},
		{"a member by a plain post", "member", false, app.RoleMember},
		{"an admin by a plain post", "admin", false, app.RoleAdmin},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAdminEnv(t)
			opts := []reqOpt{e.adminSession}
			if tt.datastar {
				opts = append(opts, asDatastar())
			}
			rec := e.send(postForm("/admin/invitations", url.Values{"role": {tt.role}}, opts...))
			if tt.datastar {
				requireSSE(t, rec)
				requireBody(t, rec, []string{`id="invite"`}, "<table", `id="users-panel"`)
			} else {
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200", rec.Code)
				}
				// The whole page, so a browser without JavaScript sees the link.
				requireBody(t, rec, []string{"<title>Users · Clowder</title>", `id="users-panel"`})
			}
			// The box: the link, the expiry (7 days after the fake clock's
			// start) and the copy button.
			requireBody(t, rec, []string{
				`id="invitation-box"`, `id="invitation-link"`, "readonly", "Expires 2026-11-02 09:30 UTC",
				`id="copy-invitation"`, "navigator.clipboard.writeText(document.getElementById('invitation-link').value).then(() => { el.textContent = 'Copied' })",
				"Copy link", "<option value=\"" + tt.role + "\" selected>",
			})

			m := linkRE.FindStringSubmatch(rec.Body.String())
			if m == nil {
				t.Fatalf("no invitation link in:\n%s", rec.Body)
			}
			if err := e.accounts.InvitationValid(e.t.Context(), m[2]); err != nil {
				t.Fatalf("InvitationValid(%q) = %v; the link %s is not a valid invitation", m[2], err, m[1])
			}
			// The link signs up a user with the chosen role.
			u, err := e.accounts.SignUp(e.t.Context(), m[2], "newcomer", "Newcomer", testPassword)
			if err != nil {
				t.Fatalf("SignUp: %+v", err)
			}
			if u.Role != tt.wantRole {
				t.Errorf("role of the new user = %s, want %s", u.Role, tt.wantRole)
			}
		})
	}
}

func TestAdminInvitationLinkUsesTheBaseURL(t *testing.T) {
	tests := []struct {
		baseURL string
		want    string
	}{
		{"https://clowder.example.com", `value="https://clowder.example.com/invite/`},
		{"https://clowder.example.com/", `value="https://clowder.example.com/invite/`},
		{"https://example.com/chat", `value="https://example.com/chat/invite/`},
	}
	for _, tt := range tests {
		t.Run(tt.baseURL, func(t *testing.T) {
			e := newTestEnv(t, func(c *config.Config) { c.BaseURL = tt.baseURL })
			e.addUser("admin", "Admin", app.RoleAdmin)
			rec := e.send(postForm("/admin/invitations", url.Values{"role": {"member"}}, withCookie(e.session("admin")), asDatastar()))
			requireSSE(t, rec)
			requireBody(t, rec, []string{tt.want})
		})
	}
}

func TestAdminInvitationFailures(t *testing.T) {
	tests := []struct {
		name       string
		datastar   bool
		wantStatus int
		wantPage   bool
	}{
		{"by Datastar", true, http.StatusOK, false},
		{"by a plain post", false, http.StatusUnprocessableEntity, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAdminEnv(t)
			opts := []reqOpt{e.adminSession}
			if tt.datastar {
				opts = append(opts, asDatastar())
			}
			rec := e.send(postForm("/admin/invitations", url.Values{"role": {"owner"}}, opts...))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body:\n%s", rec.Code, tt.wantStatus, rec.Body)
			}
			if tt.datastar {
				requireSSE(t, rec)
			}
			requireBody(t, rec, []string{`id="invite-form"`, "Choose admin or member.", `<p class="field-error" role="alert">`}, "invitation-box", "invitation-link")
			if tt.wantPage {
				requireBody(t, rec, []string{`id="users-panel"`})
			} else {
				requireBody(t, rec, nil, `id="users-panel"`)
			}
		})
	}
}
