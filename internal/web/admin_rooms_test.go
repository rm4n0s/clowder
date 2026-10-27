package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
)

// adminRoomPath is the path of an action on a room of the admin rooms page.
func adminRoomPath(r app.Room, suffix string) string {
	return "/admin/rooms/" + idText(r.ID) + suffix
}

// rowHTML returns the HTML of the row of the room in the body.
func rowHTML(t *testing.T, body string, r app.Room) string {
	t.Helper()
	re := regexp.MustCompile(`(?s)<li class="admin-room" id="room-` + idText(r.ID) + `".*?</li>\s*(?:</ul>|<li class="admin-room")`)
	row := re.FindString(body)
	if row == "" {
		t.Fatalf("no row for room %s in:\n%s", r.Name, body)
	}
	return row
}

func TestAdminRoomsPage(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	rec := e.send(get("/admin/rooms", e.cookies["admin"]))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	requireBody(t, rec, []string{
		"<title>Rooms · Clowder</title>", `<h1>Rooms</h1>`, `id="admin-rooms"`,
		// The tab bar, with this tab current.
		`<nav class="admin-tabs" aria-label="Admin">`, `href="/admin/users"`, `href="/admin/audit"`,
		`class="current" aria-current="page">Rooms</a>`,
		// The normal sidebar.
		`id="sidebar-user">Admin</p>`, `id="logout-form"`, `<a href="/rooms">Browse rooms</a>`,
	})
	body := rec.Body.String()

	tests := []struct {
		room app.Room
		want []string
	}{
		{e.general, []string{"<h2>general</h2>", "Visible", "3 members", `<span class="room-creator">Admin</span>`, "2026-10-26", "Everything"}},
		{e.random, []string{"<h2>random</h2>", "Visible", "1 member", `<span class="room-creator">Admin</span>`}},
		// The private room of alice, which the admin is not a member of.
		{e.secret, []string{"<h2>secret</h2>", "Private", "2 members", `<span class="room-creator">Alice</span>`, "Just us"}},
	}
	for _, tt := range tests {
		t.Run(tt.room.Name, func(t *testing.T) {
			row := rowHTML(t, body, tt.room)
			for _, w := range tt.want {
				if !strings.Contains(row, w) {
					t.Errorf("row lacks %q:\n%s", w, row)
				}
			}
			for _, w := range []string{
				adminRoomPath(tt.room, "/rename"), adminRoomPath(tt.room, "/delete"), adminRoomPath(tt.room, "/members"),
				"Rename or describe", "Delete</button>", "Members</summary>",
			} {
				if !strings.Contains(row, w) {
					t.Errorf("row lacks %q:\n%s", w, row)
				}
			}
			// The members are not loaded before the toggle is opened.
			if strings.Contains(row, "Remove</button>") || strings.Contains(row, `class="member-list"`) {
				t.Errorf("the members are loaded:\n%s", row)
			}
		})
	}
	// Ordered by name.
	if g, r, s := strings.Index(body, "<h2>general"), strings.Index(body, "<h2>random"), strings.Index(body, "<h2>secret"); g >= r || r >= s {
		t.Errorf("rooms are not ordered by name: %d %d %d", g, r, s)
	}
}

func TestAdminRoomsPageOpensTheMembersOfOneRoom(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	tests := []struct {
		name    string
		query   string
		want    []string
		notWant []string
	}{
		{
			name: "a private room the admin is not in", query: "?members=" + idText(e.secret.ID),
			want: []string{
				`id="room-` + idText(e.secret.ID) + `-member-` + idText(e.bob.ID) + `"`, `data-username="alice"`, `data-username="bob"`,
				adminRoomPath(e.secret, "/members/"+idText(e.bob.ID)+"/remove"), "Remove</button>", "<details", " open",
			},
			notWant: []string{`data-username="admin"`},
		},
		{name: "not an ID", query: "?members=bob", notWant: []string{`class="member-list"`, "Remove</button>"}},
		{name: "a room that does not exist", query: "?members=9999", notWant: []string{`class="member-list"`, "Remove</button>"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(get("/admin/rooms"+tt.query, e.cookies["admin"]))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			requireBody(t, rec, tt.want, tt.notWant...)
		})
	}
}

func TestAdminRoomMembersToggle(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	t.Run("datastar patches the row with the members", func(t *testing.T) {
		rec := e.send(get(adminRoomPath(e.secret, "/members"), e.cookies["admin"], asDatastar()))
		requireSSE(t, rec)
		requireBody(t, rec, []string{
			`id="room-` + idText(e.secret.ID) + `"`, `data-username="alice"`, `data-username="bob"`, "Remove</button>", "2 members",
		}, "window.location", `data-username="admin"`, `id="room-`+idText(e.general.ID)+`"`)
	})
	t.Run("an empty private room says so", func(t *testing.T) {
		e := newRoomsEnv(t)
		for _, u := range []app.User{e.alice, e.bob} {
			if err := e.rooms.Leave(t.Context(), e.actor(u), e.secret.ID); err != nil {
				t.Fatal(err)
			}
		}
		rec := e.send(get(adminRoomPath(e.secret, "/members"), e.cookies["admin"], asDatastar()))
		requireSSE(t, rec)
		requireBody(t, rec, []string{"This room has no members.", "0 members"}, "Remove</button>")
	})
	t.Run("plain is sent to the page with the members open", func(t *testing.T) {
		rec := e.send(get(adminRoomPath(e.secret, "/members"), e.cookies["admin"]))
		want := "/admin/rooms?members=" + idText(e.secret.ID) + "#room-" + idText(e.secret.ID)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
			t.Errorf("status = %d, Location = %q, want 303 to %s", rec.Code, rec.Header().Get("Location"), want)
		}
	})
	t.Run("a room that is gone removes the row", func(t *testing.T) {
		rec := e.send(get("/admin/rooms/9999/members", e.cookies["admin"], asDatastar()))
		requireSSE(t, rec)
		requireBody(t, rec, []string{"mode remove", "#room-9999"})
	})
	t.Run("an ID that is not a number", func(t *testing.T) {
		if rec := e.send(get("/admin/rooms/x/members", e.cookies["admin"], asDatastar())); rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})
}

func TestAdminRoomRename(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		// A private room of someone else, which the admin is not a member of.
		form := url.Values{"name": {"vault"}, "description": {"Locked"}}
		rec := e.post("admin", adminRoomPath(e.secret, "/rename"), form, datastar)
		if datastar {
			requireSSE(t, rec)
			requireBody(t, rec, []string{`id="room-` + idText(e.secret.ID) + `"`, "<h2>vault</h2>", "Locked", `value="vault"`}, "form-error", "field-error", "<h2>secret</h2>")
		} else if want := "/admin/rooms#room-" + idText(e.secret.ID); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
			t.Errorf("status = %d, Location = %q, want 303 to %s", rec.Code, rec.Header().Get("Location"), want)
		}
		got, ok := e.roomNamed("alice", "vault")
		if !ok || got.Description != "Locked" || got.ID != e.secret.ID {
			t.Errorf("room after the rename = %+v, %v", got, ok)
		}
		// The change is audited with the admin as the actor.
		rec = e.send(get("/admin/audit", e.cookies["admin"]))
		requireBody(t, rec, []string{"Admin renamed #secret to #vault and changed its description"})
	})
}

func TestAdminRoomRenameFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		form       url.Values
		wantStatus int
		wantBody   []string
	}{
		{
			name: "an empty name", form: url.Values{"name": {" "}, "description": {"typed"}},
			wantStatus: http.StatusUnprocessableEntity,
			wantBody:   []string{"Use 1 to 64 characters.", "typed", "<details class=\"room-edit\" open>"},
		},
		{
			name: "a name that is taken", form: url.Values{"name": {"GENERAL"}, "description": {"x"}},
			wantStatus: http.StatusUnprocessableEntity,
			wantBody:   []string{"This name is taken.", `value="GENERAL"`},
		},
		{
			name: "a description that is too long", form: url.Values{"name": {"fine"}, "description": {strings.Repeat("x", 501)}},
			wantStatus: http.StatusUnprocessableEntity,
			wantBody:   []string{"Use at most 500 characters.", `value="fine"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modes(t, func(t *testing.T, datastar bool) {
				e := newRoomsEnv(t)
				rec := e.post("admin", adminRoomPath(e.secret, "/rename"), tt.form, datastar)
				if datastar {
					requireSSE(t, rec)
				} else if rec.Code != tt.wantStatus {
					t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
				}
				requireBody(t, rec, append(tt.wantBody, `id="room-`+idText(e.secret.ID)+`"`))
				if _, ok := e.roomNamed("alice", "secret"); !ok {
					t.Error("the room was renamed")
				}
			})
		})
	}
}

func TestAdminRoomDelete(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		rec := e.post("admin", adminRoomPath(e.secret, "/delete"), nil, datastar)
		if datastar {
			requireSSE(t, rec)
			requireBody(t, rec, []string{"mode remove", "#room-" + idText(e.secret.ID)})
		} else if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/rooms" {
			t.Errorf("status = %d, Location = %q", rec.Code, rec.Header().Get("Location"))
		}
		if e.exists("secret") || !e.exists("general") {
			t.Errorf("secret exists %v, general exists %v", e.exists("secret"), e.exists("general"))
		}
		rec = e.send(get("/admin/rooms", e.cookies["admin"]))
		requireBody(t, rec, []string{"<h2>general</h2>"}, "<h2>secret</h2>")
		// The deletion is in the audit log, with the room as plain text.
		rec = e.send(get("/admin/audit", e.cookies["admin"]))
		requireBody(t, rec, []string{"Admin deleted #secret"})
	})
}

func TestAdminRoomActionsOnARoomThatIsGone(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"/rename", "/delete", "/members/" + "1" + "/remove"} {
		t.Run(action, func(t *testing.T) {
			e := newRoomsEnv(t)
			form := url.Values{"name": {"x"}}
			rec := e.post("admin", "/admin/rooms/9999"+action, form, true)
			requireSSE(t, rec)
			requireBody(t, rec, []string{"mode remove", "#room-9999"})
			rec = e.post("admin", "/admin/rooms/9999"+action, form, false)
			if rec.Code != http.StatusNotFound {
				t.Errorf("plain status = %d, want 404", rec.Code)
			}
		})
	}
}

func TestAdminMemberRemove(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		// bob in the private room secret, which the admin is not a member of.
		path := adminRoomPath(e.secret, "/members/"+idText(e.bob.ID)+"/remove")
		rec := e.post("admin", path, nil, datastar)
		if datastar {
			requireSSE(t, rec)
			requireBody(t, rec, []string{`id="room-` + idText(e.secret.ID) + `"`, `data-username="alice"`, "1 member"},
				`data-username="bob"`, "form-error", "window.location")
		} else {
			want := "/admin/rooms?members=" + idText(e.secret.ID) + "#room-" + idText(e.secret.ID)
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
				t.Errorf("status = %d, Location = %q, want 303 to %s", rec.Code, rec.Header().Get("Location"), want)
			}
		}
		if e.isMember("bob", "secret") {
			t.Error("bob is still a member of secret")
		}
		if !e.isMember("bob", "general") {
			t.Error("bob lost another room")
		}
		rec = e.send(get("/admin/audit", e.cookies["admin"]))
		requireBody(t, rec, []string{"Admin removed Bob from #secret", "member.removed"})
	})
}

func TestAdminMemberRemoveOfANonMemberChangesNothing(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	rec := e.post("admin", adminRoomPath(e.secret, "/members/"+idText(e.carol.ID)+"/remove"), nil, true)
	requireSSE(t, rec)
	requireBody(t, rec, []string{`data-username="alice"`, `data-username="bob"`, "2 members"}, "form-error")
	if rec := e.post("admin", adminRoomPath(e.secret, "/members/x/remove"), nil, true); rec.Code != http.StatusNotFound {
		t.Errorf("an ID that is not a number: status = %d, want 404", rec.Code)
	}
}

func TestAdminRoomsAreInvisibleToMembers(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	form := url.Values{"name": {"x"}}
	remove := "/members/" + idText(e.bob.ID) + "/remove"
	tests := []struct {
		name    string
		request func(user string, datastar bool) *http.Request
	}{
		{"page", func(u string, d bool) *http.Request { return reqAs(e, get("/admin/rooms", e.cookies[u]), d) }},
		{"page with members", func(u string, d bool) *http.Request {
			return reqAs(e, get("/admin/rooms?members="+idText(e.secret.ID), e.cookies[u]), d)
		}},
		{"members", func(u string, d bool) *http.Request {
			return reqAs(e, get(adminRoomPath(e.secret, "/members"), e.cookies[u]), d)
		}},
		{"rename", func(u string, d bool) *http.Request {
			return reqAs(e, postForm(adminRoomPath(e.secret, "/rename"), form, e.cookies[u]), d)
		}},
		{"delete", func(u string, d bool) *http.Request {
			return reqAs(e, postForm(adminRoomPath(e.secret, "/delete"), nil, e.cookies[u]), d)
		}},
		{"remove", func(u string, d bool) *http.Request {
			return reqAs(e, postForm(adminRoomPath(e.secret, remove), nil, e.cookies[u]), d)
		}},
		{"rename of a room that does not exist", func(u string, d bool) *http.Request {
			return reqAs(e, postForm("/admin/rooms/9999/rename", form, e.cookies[u]), d)
		}},
	}
	for _, tt := range tests {
		for _, user := range []string{"alice", "carol"} {
			for _, datastar := range []bool{false, true} {
				t.Run(tt.name+" as "+user+map[bool]string{false: "", true: " by Datastar"}[datastar], func(t *testing.T) {
					rec := e.send(tt.request(user, datastar))
					if rec.Code != http.StatusNotFound {
						t.Fatalf("status = %d, want 404 (not 403); body:\n%s", rec.Code, rec.Body)
					}
					requireBody(t, rec, []string{"Page not found"}, "admin-room", "secret", "Forbidden", "not allowed", "mode remove")
				})
			}
		}
	}
	// Nothing changed.
	if !e.exists("secret") || !e.isMember("bob", "secret") {
		t.Error("a member changed a room")
	}
	if _, ok := e.roomNamed("alice", "secret"); !ok {
		t.Error("a member renamed the room")
	}
}

// reqAs marks the request as sent by Datastar when datastar is true.
func reqAs(_ *roomsEnv, r *http.Request, datastar bool) *http.Request {
	if datastar {
		asDatastar()(r)
	}
	return r
}

func TestAdminRoomsNeedASession(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	tests := []struct {
		name    string
		request *http.Request
	}{
		{"page", get("/admin/rooms")},
		{"members", get(adminRoomPath(e.secret, "/members"))},
		{"rename", postForm(adminRoomPath(e.secret, "/rename"), url.Values{"name": {"x"}})},
		{"delete", postForm(adminRoomPath(e.secret, "/delete"), nil)},
		{"remove", postForm(adminRoomPath(e.secret, "/members/1/remove"), nil)},
		{"audit", get("/admin/audit")},
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

func TestAdminTabsOnTheUsersPage(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	rec := e.send(get("/admin/users", e.cookies["admin"]))
	requireBody(t, rec, []string{
		`<nav class="admin-tabs" aria-label="Admin">`, `class="current" aria-current="page">Users</a>`,
		`href="/admin/rooms"`, `href="/admin/audit"`,
	})
}
