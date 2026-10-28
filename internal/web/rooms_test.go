package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/web/views"
	"github.com/rm4n0s/errors"
)

// roomsEnv is a testEnv with these users, rooms and memberships:
//
//	admin  admin, made general and random, a member of both
//	alice  member, a member of general and made secret
//	bob    member, a member of general and secret (added by alice)
//	carol  member, a member of nothing
//	general  visible: admin, alice, bob
//	random   visible: admin
//	secret   private: alice, bob
type roomsEnv struct {
	*testEnv
	admin, alice, bob, carol app.User
	general, random, secret  app.Room
	cookies                  map[string]reqOpt
}

func newRoomsEnv(t *testing.T) *roomsEnv {
	t.Helper()
	return newRoomsEnvWith(t, nil)
}

// newRoomsEnvWith is newRoomsEnv with a change to the configuration.
func newRoomsEnvWith(t *testing.T, mutate func(*config.Config)) *roomsEnv {
	t.Helper()
	e := &roomsEnv{testEnv: newTestEnv(t, mutate), cookies: map[string]reqOpt{}}
	e.admin = e.addUser("admin", "Admin", app.RoleAdmin)
	e.alice = e.addUser("alice", "Alice", app.RoleMember)
	e.bob = e.addUser("bob", "Bob", app.RoleMember)
	e.carol = e.addUser("carol", "Carol", app.RoleMember)
	for _, name := range []string{"admin", "alice", "bob", "carol"} {
		e.cookies[name] = withCookie(e.session(name))
	}
	var err error
	ctx := t.Context()
	if e.general, err = e.rooms.Create(ctx, e.actor(e.admin), app.RoomInput{Name: "general", Description: "Everything", Visibility: app.VisibilityVisible}); err != nil {
		t.Fatalf("Create general: %+v", err)
	}
	if e.random, err = e.rooms.Create(ctx, e.actor(e.admin), app.RoomInput{Name: "random", Visibility: app.VisibilityVisible}); err != nil {
		t.Fatalf("Create random: %+v", err)
	}
	for _, u := range []app.User{e.alice, e.bob} {
		if err := e.rooms.Join(ctx, e.actor(u), e.general.ID); err != nil {
			t.Fatalf("Join general: %+v", err)
		}
	}
	if e.secret, err = e.rooms.Create(ctx, e.actor(e.alice), app.RoomInput{Name: "secret", Description: "Just us", Visibility: app.VisibilityPrivate}); err != nil {
		t.Fatalf("Create secret: %+v", err)
	}
	if err := e.rooms.AddMember(ctx, e.actor(e.alice), e.secret.ID, e.bob.ID); err != nil {
		t.Fatalf("AddMember: %+v", err)
	}
	return e
}

// actor makes the Actor of a user.
func (e *roomsEnv) actor(u app.User) app.Actor {
	return app.Actor{UserID: u.ID, Username: u.Username, Role: u.Role}
}

// users returns the user of the name.
func (e *roomsEnv) users(name string) app.User {
	switch name {
	case "admin":
		return e.admin
	case "alice":
		return e.alice
	case "bob":
		return e.bob
	case "carol":
		return e.carol
	}
	e.t.Fatalf("no user %q", name)
	return app.User{}
}

// room returns the room of the name.
func (e *roomsEnv) room(name string) app.Room {
	switch name {
	case "general":
		return e.general
	case "random":
		return e.random
	case "secret":
		return e.secret
	}
	e.t.Fatalf("no room %q", name)
	return app.Room{}
}

// members returns the usernames of the members of the room, read as a user who
// may read it.
func (e *roomsEnv) members(room string, as string) []string {
	e.t.Helper()
	list, err := e.rooms.Members(e.t.Context(), e.actor(e.users(as)), e.room(room).ID)
	if err != nil {
		e.t.Fatalf("Members: %+v", err)
	}
	names := []string{}
	for _, m := range list {
		names = append(names, m.Username)
	}
	return names
}

// isMember reports whether the user is a member of the room.
func (e *roomsEnv) isMember(user, room string) bool {
	e.t.Helper()
	mine, err := e.rooms.Mine(e.t.Context(), e.actor(e.users(user)))
	if err != nil {
		e.t.Fatalf("Mine: %+v", err)
	}
	for _, r := range mine {
		if r.ID == e.room(room).ID {
			return true
		}
	}
	return false
}

// exists reports whether the room still exists, asking as the admin.
func (e *roomsEnv) exists(room string) bool {
	e.t.Helper()
	for _, r := range mustAll(e.t, e) {
		if r.ID == e.room(room).ID {
			return true
		}
	}
	return false
}

// post sends a POST as the user, plain or by Datastar.
func (e *roomsEnv) post(user, path string, form url.Values, datastar bool) *httptest.ResponseRecorder {
	e.t.Helper()
	opts := []reqOpt{e.cookies[user]}
	if datastar {
		opts = append(opts, asDatastar())
	}
	return e.send(postForm(path, form, opts...))
}

// idText is the decimal text of an ID.
func idText(id uint64) string { return strconv.FormatUint(id, 10) }

// modes runs fn once for a Datastar request and once for a plain post.
func modes(t *testing.T, fn func(t *testing.T, datastar bool)) {
	t.Helper()
	t.Run("datastar", func(t *testing.T) { fn(t, true) })
	t.Run("plain", func(t *testing.T) { fn(t, false) })
}

// requireNotFound fails unless the answer is the 404 page (plain post) or the
// redirect to the page of the room, which is the 404 page (Datastar).
func requireNotFound(t *testing.T, rec *httptest.ResponseRecorder, datastar bool, roomPath string) {
	t.Helper()
	if datastar {
		requireSSE(t, rec)
		requireBody(t, rec, []string{redirectScript(roomPath)}, "Forbidden", "not allowed")
		return
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (not 403); body:\n%s", rec.Code, rec.Body)
	}
	requireBody(t, rec, []string{"Page not found"}, "not allowed", "Forbidden")
}

func TestSidebarListsTheRoomsOfTheUser(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	gen, sec := idText(e.general.ID), idText(e.secret.ID)
	tests := []struct {
		name    string
		user    string
		page    string
		want    []string
		notWant []string
	}{
		{
			name: "home page of alice", user: "alice", page: "/",
			want: []string{
				`id="sidebar-rooms"`, `href="/rooms/` + gen + `"`, `data-room="general"`,
				`href="/rooms/` + sec + `"`, `data-room="secret"`, `aria-label="Private"`,
				`<a href="/rooms">Browse rooms</a>`, `<a href="/rooms/new">New room</a>`,
				`<a class="logo" href="/">`, `id="logout-form"`,
			},
			notWant: []string{`data-room="random"`, "aria-current"},
		},
		{
			name: "carol has no rooms yet", user: "carol", page: "/",
			want:    []string{`id="sidebar-rooms"`, "Browse rooms", "New room"},
			notWant: []string{"data-room="},
		},
		{
			name: "the current room is highlighted", user: "alice", page: "/rooms/" + sec,
			want:    []string{`href="/rooms/` + sec + `" class="current" data-room="secret" aria-current="page"`},
			notWant: []string{`href="/rooms/` + gen + `" class="current"`},
		},
		{
			name: "the browse page has no current room", user: "alice", page: "/rooms",
			want:    []string{`data-room="secret"`},
			notWant: []string{"aria-current"},
		},
		{
			name: "the admin page has the sidebar too", user: "admin", page: "/admin/users",
			want:    []string{`data-room="general"`, `data-room="random"`, `<a class="user-menu-item" href="/admin/users">`, "Admin</a>"},
			notWant: []string{`data-room="secret"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(get(tt.page, e.cookies[tt.user]))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			requireBody(t, rec, tt.want, tt.notWant...)
		})
	}

	// A new membership shows in the sidebar at once.
	if rec := e.post("carol", "/rooms/"+gen+"/join", nil, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("join status = %d", rec.Code)
	}
	requireBody(t, e.send(get("/", e.cookies["carol"])), []string{`data-room="general"`})
}

func TestSidebarOnAFailedAdminInvitation(t *testing.T) {
	t.Parallel()
	// A plain post that fails shows the whole admin page, with the sidebar.
	e := newRoomsEnv(t)
	rec := e.post("admin", "/admin/invitations", url.Values{"role": {"owner"}}, false)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	requireBody(t, rec, []string{`data-room="general"`, `id="users-panel"`})
}

func TestRoomsBrowse(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	tests := []struct {
		name    string
		user    string
		want    []string
		notWant []string
	}{
		{
			name: "alice joined general and can join random", user: "alice",
			want: []string{
				"<title id=\"page-title\">Rooms · Clowder</title>", "<h1>Browse rooms</h1>", `id="rooms-panel"`,
				"Everything", "3 members", "1 member",
				`<span class="joined">Joined</span>`, `href="/rooms/` + idText(e.general.ID) + `"`,
				`action="/rooms/` + idText(e.random.ID) + `/join"`, `name="from" value="browse"`,
			},
			notWant: []string{`action="/rooms/` + idText(e.general.ID) + `/join"`, `id="room-` + idText(e.secret.ID) + `"`},
		},
		{
			name: "carol can join both", user: "carol",
			want:    []string{`action="/rooms/` + idText(e.general.ID) + `/join"`, `action="/rooms/` + idText(e.random.ID) + `/join"`},
			notWant: []string{"Joined", "secret"},
		},
		{
			name: "an admin does not see private rooms of others", user: "admin",
			want:    []string{"general", "random"},
			notWant: []string{"secret"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(get("/rooms", e.cookies[tt.user]))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			requireBody(t, rec, tt.want, tt.notWant...)
		})
	}
}

func TestRoomNewPage(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	tests := []struct {
		name    string
		user    string
		want    []string
		notWant []string
	}{
		{
			name: "a member makes private rooms", user: "alice",
			want:    []string{"<title id=\"page-title\">New room · Clowder</title>", `id="room-new-form"`, `name="visibility" value="private"`, "Rooms you make are private", `name="name"`, `name="description"`},
			notWant: []string{"<select"},
		},
		{
			name: "an admin chooses", user: "admin",
			want:    []string{`<select id="room-visibility" name="visibility">`, `<option value="visible"`, `<option value="private" selected>`},
			notWant: []string{"Rooms you make are private", `type="hidden"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(get("/rooms/new", e.cookies[tt.user]))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			requireBody(t, rec, tt.want, tt.notWant...)
		})
	}
}

// roomNamed finds the room of a user by name, through the service.
func (e *roomsEnv) roomNamed(user, name string) (app.Room, bool) {
	e.t.Helper()
	mine, err := e.rooms.Mine(e.t.Context(), e.actor(e.users(user)))
	if err != nil {
		e.t.Fatalf("Mine: %+v", err)
	}
	for _, r := range mine {
		if r.Name == name {
			return r, true
		}
	}
	return app.Room{}, false
}

func TestRoomCreate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		user       string
		form       url.Values
		wantVis    app.Visibility
		wantMember bool
	}{
		{"a member makes a private room", "alice", url.Values{"name": {" plans "}, "description": {"Next steps"}, "visibility": {"private"}}, app.VisibilityPrivate, true},
		{"an admin makes a visible room", "admin", url.Values{"name": {"plans"}, "visibility": {"visible"}}, app.VisibilityVisible, true},
		{"an admin makes a private room", "admin", url.Values{"name": {"plans"}, "visibility": {"private"}}, app.VisibilityPrivate, true},
	}
	for _, tt := range tests {
		modes(t, func(t *testing.T, datastar bool) {
			t.Run(tt.name, func(t *testing.T) {
				e := newRoomsEnv(t)
				rec := e.post(tt.user, "/rooms", tt.form, datastar)
				room, ok := e.roomNamed(tt.user, "plans")
				if !ok {
					t.Fatalf("the room was not made; body:\n%s", rec.Body)
				}
				if room.Visibility != tt.wantVis || !room.IsMember {
					t.Errorf("room = %+v", room)
				}
				target := "/rooms/" + idText(room.ID)
				if datastar {
					requireSSE(t, rec)
					requireBody(t, rec, []string{redirectScript(target)})
				} else if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != target {
					t.Errorf("status = %d, Location = %q; want 303 to %s", rec.Code, rec.Header().Get("Location"), target)
				}
				// The room opens and its sidebar link is there.
				page := e.send(get(target, e.cookies[tt.user]))
				requireBody(t, page, []string{"<h1>plans</h1>", `data-room="plans"`})
			})
		})
	}
}

func TestRoomCreateFailures(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 501)
	tests := []struct {
		name       string
		user       string
		form       url.Values
		wantStatus int // of a plain post
		wantText   []string
		notWant    []string
		wantNoRoom string // a room that must not exist after it
	}{
		{
			name: "a member cannot make a visible room", user: "alice",
			form:       url.Values{"name": {"plans"}, "visibility": {"visible"}},
			wantStatus: http.StatusForbidden, wantText: []string{messageForbidden, `value="plans"`}, wantNoRoom: "plans",
		},
		{
			name: "no name", user: "alice",
			form:       url.Values{"name": {"  "}, "description": {"keep me"}, "visibility": {"private"}},
			wantStatus: http.StatusUnprocessableEntity,
			wantText:   []string{`id="room-name-error"`, "Use 1 to 64 characters.", ">keep me</textarea>"},
		},
		{
			name: "a name that is taken, without regard to case", user: "alice",
			form:       url.Values{"name": {"GENERAL"}, "visibility": {"private"}},
			wantStatus: http.StatusUnprocessableEntity,
			wantText:   []string{"This name is taken.", `value="GENERAL"`},
		},
		{
			name: "a description that is too long", user: "alice",
			form:       url.Values{"name": {"plans"}, "description": {long}, "visibility": {"private"}},
			wantStatus: http.StatusUnprocessableEntity,
			wantText:   []string{`id="room-description-error"`, "Use at most 500 characters.", `value="plans"`}, wantNoRoom: "plans",
		},
		{
			name: "no visibility", user: "alice",
			form:       url.Values{"name": {"plans"}},
			wantStatus: http.StatusUnprocessableEntity,
			wantText:   []string{"Choose visible or private."}, wantNoRoom: "plans",
		},
		{
			name: "an admin with a visibility that does not exist", user: "admin",
			form:       url.Values{"name": {"plans"}, "visibility": {"secret"}},
			wantStatus: http.StatusUnprocessableEntity,
			wantText:   []string{"Choose visible or private.", `<select id="room-visibility"`}, wantNoRoom: "plans",
		},
	}
	for _, tt := range tests {
		for _, datastar := range []bool{true, false} {
			name := tt.name + map[bool]string{true: "/datastar", false: "/plain"}[datastar]
			t.Run(name, func(t *testing.T) {
				e := newRoomsEnv(t)
				before := len(mustAll(t, e))
				rec := e.post(tt.user, "/rooms", tt.form, datastar)
				want := append([]string{`id="room-new-form"`}, tt.wantText...)
				if datastar {
					requireSSE(t, rec)
					requireBody(t, rec, want, "<aside", "window.location")
				} else {
					if rec.Code != tt.wantStatus {
						t.Fatalf("status = %d, want %d; body:\n%s", rec.Code, tt.wantStatus, rec.Body)
					}
					requireBody(t, rec, append(want, `<aside id="sidebar">`, `data-room="general"`))
				}
				if tt.wantNoRoom != "" {
					if _, ok := e.roomNamed(tt.user, tt.wantNoRoom); ok {
						t.Errorf("room %q was made", tt.wantNoRoom)
					}
				}
				if got := len(mustAll(t, e)); got != before {
					t.Errorf("%d rooms, want %d", got, before)
				}
			})
		}
	}
}

// mustAll returns every room, as the admin.
func mustAll(t *testing.T, e *roomsEnv) []app.Room {
	t.Helper()
	all, err := e.rooms.AllRooms(t.Context(), e.actor(e.admin))
	if err != nil {
		t.Fatalf("AllRooms: %+v", err)
	}
	return all
}

func TestRoomCreateFailuresOtherThanInput(t *testing.T) {
	t.Parallel()
	// A name that is taken at the same moment is a conflict; a database
	// failure is a 500. Neither shows the error; both keep the form.
	e := newRoomsEnv(t)
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantMsg    string
	}{
		{"conflict", errors.New(app.TagConflict, "x"), http.StatusConflict, messageConflict},
		{"unexpected", errors.New("Boom", "x"), http.StatusInternalServerError, messageTryAgain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			e.srv.formFailed(rec, get("/rooms"), tt.err, func(p views.FormErrors) formView {
				d := views.RoomNewData{Name: "plans", Errors: p}
				return formView{fragment: views.RoomNewForm(d), page: views.RoomNewPage(d)}
			})
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			requireBody(t, rec, []string{tt.wantMsg, `value="plans"`}, "Boom")
		})
	}
}

func TestRoomPage(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	gen, sec := idText(e.general.ID), idText(e.secret.ID)
	tests := []struct {
		name    string
		user    string
		room    string
		want    []string
		notWant []string
	}{
		{
			name: "a member of a private room, who made it", user: "alice", room: "secret",
			want: []string{
				"<title id=\"page-title\">secret · Clowder</title>", `id="room-header"`, "<h1>secret</h1>", "badge-private", "Private", "Just us", "2 members",
				"No posts yet",
				`id="leave-form"`, `action="/rooms/` + sec + `/leave"`, "confirm(", `id="delete-form"`, `id="room-edit"`,
				`id="members-panel"`, `data-username="alice"`, `data-username="bob"`, "added by Alice", "Bob", "@bob",
				`id="add-member-form"`, `<option value="` + idText(e.carol.ID) + `">Carol (@carol)</option>`, `<option value="` + idText(e.admin.ID) + `">Admin (@admin)</option>`,
			},
			notWant: []string{`/join"`, "/remove", `<option value="` + idText(e.bob.ID) + `"`, `<option value="` + idText(e.alice.ID) + `"`},
		},
		{
			name: "a member of a private room who did not make it", user: "bob", room: "secret",
			want:    []string{"<h1>secret</h1>", `id="leave-form"`, `id="add-member-form"`},
			notWant: []string{`id="delete-form"`, `id="room-edit"`, "/remove"},
		},
		{
			name: "a member of a visible room", user: "bob", room: "general",
			want:    []string{"<h1>general</h1>", "badge-visible", "Visible", "Everything", "3 members", `id="leave-form"`, `id="add-member-form"`},
			notWant: []string{`/join"`, `id="delete-form"`, "/remove"},
		},
		{
			name: "a non-member reads a visible room and can join it", user: "carol", room: "general",
			want:    []string{"<h1>general</h1>", `action="/rooms/` + gen + `/join"`, `name="from" value="room"`, `id="members-panel"`, `data-username="bob"`},
			notWant: []string{`id="leave-form"`, `id="add-member-form"`, `id="delete-form"`, "/remove"},
		},
		{
			name: "an admin who is a member sees the remove buttons and the management", user: "admin", room: "general",
			want:    []string{`id="leave-form"`, `id="delete-form"`, `id="room-edit"`, `action="/rooms/` + gen + `/members/` + idText(e.alice.ID) + `/remove"`, `id="add-member-form"`},
			notWant: []string{`/join"`},
		},
		{
			name: "an admin who left a visible room manages it but cannot add", user: "admin", room: "random",
			want:    []string{`action="/rooms/` + idText(e.random.ID) + `/join"`, `id="delete-form"`, `id="room-edit"`},
			notWant: []string{`id="add-member-form"`, `id="leave-form"`},
		},
	}
	if err := e.rooms.Leave(t.Context(), e.actor(e.admin), e.random.ID); err != nil {
		t.Fatalf("Leave: %+v", err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(get("/rooms/"+idText(e.room(tt.room).ID), e.cookies[tt.user]))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body:\n%s", rec.Code, rec.Body)
			}
			requireBody(t, rec, tt.want, tt.notWant...)
		})
	}
}

func TestRoomPageIsNotFoundForWhoMayNotReadIt(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	tests := []struct {
		name string
		user string
		path string
	}{
		{"a non-member of a private room", "carol", "/rooms/" + idText(e.secret.ID)},
		{"an admin who is not a member of a private room", "admin", "/rooms/" + idText(e.secret.ID)},
		{"a room that does not exist", "alice", "/rooms/9999"},
		{"an ID that is not a number", "alice", "/rooms/abc"},
		{"the ID 0", "alice", "/rooms/0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(get(tt.path, e.cookies[tt.user]))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404; body:\n%s", rec.Code, rec.Body)
			}
			requireBody(t, rec, []string{"Page not found"}, "secret", "Just us", "members-panel", "Forbidden")
		})
	}
}

func TestRoomJoin(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		path := "/rooms/" + idText(e.random.ID)
		for _, user := range []string{"carol", "carol"} { // the second join changes nothing
			rec := e.post(user, path+"/join", url.Values{"from": {"browse"}}, datastar)
			if datastar {
				requireSSE(t, rec)
				requireBody(t, rec, []string{redirectScript(path)})
			} else if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != path {
				t.Errorf("status = %d, Location = %q", rec.Code, rec.Header().Get("Location"))
			}
		}
		if !e.isMember("carol", "random") {
			t.Error("carol did not join")
		}
		if got := e.members("random", "carol"); len(got) != 2 {
			t.Errorf("members = %v, want admin and carol once", got)
		}
	})
}

func TestRoomLeave(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		for _, tt := range []struct{ user, room string }{{"bob", "general"}, {"bob", "secret"}} {
			rec := e.post(tt.user, "/rooms/"+idText(e.room(tt.room).ID)+"/leave", nil, datastar)
			if datastar {
				requireSSE(t, rec)
				requireBody(t, rec, []string{redirectScript("/rooms")})
			} else if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/rooms" {
				t.Errorf("status = %d, Location = %q", rec.Code, rec.Header().Get("Location"))
			}
			if e.isMember(tt.user, tt.room) {
				t.Errorf("%s is still a member of %s", tt.user, tt.room)
			}
		}
		// Having left a private room, bob does not know it any more.
		rec := e.send(get("/rooms/"+idText(e.secret.ID), e.cookies["bob"]))
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
		// Leaving a visible room one is not in changes nothing.
		rec = e.post("carol", "/rooms/"+idText(e.general.ID)+"/leave", nil, datastar)
		if !datastar && rec.Code != http.StatusSeeOther {
			t.Errorf("status = %d, want 303", rec.Code)
		}
	})
}

func TestRoomAddMember(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		path := "/rooms/" + idText(e.secret.ID)
		rec := e.post("alice", path+"/members", url.Values{"user": {idText(e.carol.ID)}}, datastar)
		if datastar {
			requireSSE(t, rec)
			requireBody(t, rec, []string{`id="members-panel"`, `data-username="carol"`, "added by Alice"}, "form-error", "window.location")
			// The patched form no longer offers carol.
			requireBody(t, rec, []string{`<option value="` + idText(e.admin.ID) + `">`}, `<option value="`+idText(e.carol.ID)+`"`)
		} else if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != path {
			t.Errorf("status = %d, Location = %q; want 303 to %s", rec.Code, rec.Header().Get("Location"), path)
		}
		if !e.isMember("carol", "secret") {
			t.Error("carol was not added")
		}
		// Carol now reads the room and has it in her sidebar.
		requireBody(t, e.send(get(path, e.cookies["carol"])), []string{"<h1>secret</h1>", `data-room="secret"`})
	})
}

func TestRoomAddMemberFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		user       string
		room       string
		form       func(e *roomsEnv) url.Values
		wantStatus int // of a plain post
		wantText   string
	}{
		{
			name: "no user chosen", user: "alice", room: "secret",
			form:       func(*roomsEnv) url.Values { return url.Values{"user": {""}} },
			wantStatus: http.StatusUnprocessableEntity, wantText: "Choose a user.",
		},
		{
			name: "a value that is not an ID", user: "alice", room: "secret",
			form:       func(*roomsEnv) url.Values { return url.Values{"user": {"carol"}} },
			wantStatus: http.StatusUnprocessableEntity, wantText: "Choose a user.",
		},
		{
			name: "a user that does not exist", user: "alice", room: "secret",
			form:       func(*roomsEnv) url.Values { return url.Values{"user": {"9999"}} },
			wantStatus: http.StatusUnprocessableEntity, wantText: "Choose a user.",
		},
		{
			name: "a disabled user", user: "alice", room: "secret",
			form: func(e *roomsEnv) url.Values {
				if err := e.accounts.SetDisabled(e.t.Context(), e.actor(e.admin), e.carol.ID, true); err != nil {
					e.t.Fatal(err)
				}
				return url.Values{"user": {idText(e.carol.ID)}}
			},
			wantStatus: http.StatusUnprocessableEntity, wantText: "This user is disabled.",
		},
		{
			name: "a person who has not joined a visible room", user: "carol", room: "general",
			form:       func(e *roomsEnv) url.Values { return url.Values{"user": {idText(e.admin.ID)}} },
			wantStatus: http.StatusForbidden, wantText: messageForbidden,
		},
	}
	for _, tt := range tests {
		modes(t, func(t *testing.T, datastar bool) {
			t.Run(tt.name, func(t *testing.T) {
				e := newRoomsEnv(t)
				room := e.room(tt.room)
				form := tt.form(e)
				before := e.members(tt.room, "alice")
				rec := e.post(tt.user, "/rooms/"+idText(room.ID)+"/members", form, datastar)
				if datastar {
					requireSSE(t, rec)
					requireBody(t, rec, []string{`id="members-panel"`, `<p class="form-error" role="alert">` + tt.wantText + `</p>`}, "window.location", "<aside")
				} else {
					if rec.Code != tt.wantStatus {
						t.Fatalf("status = %d, want %d; body:\n%s", rec.Code, tt.wantStatus, rec.Body)
					}
					requireBody(t, rec, []string{`id="members-panel"`, tt.wantText, `<aside id="sidebar">`})
				}
				if got := e.members(tt.room, "alice"); strings.Join(got, ",") != strings.Join(before, ",") {
					t.Errorf("members changed from %v to %v", before, got)
				}
			})
		})
	}
}

func TestRoomRemoveMember(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		path := "/rooms/" + idText(e.general.ID)
		rec := e.post("admin", path+"/members/"+idText(e.bob.ID)+"/remove", nil, datastar)
		if datastar {
			requireSSE(t, rec)
			requireBody(t, rec, []string{`id="members-panel"`, `data-username="alice"`}, `data-username="bob"`, "form-error")
		} else if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != path {
			t.Errorf("status = %d, Location = %q", rec.Code, rec.Header().Get("Location"))
		}
		if e.isMember("bob", "general") {
			t.Error("bob is still a member")
		}
		if !e.isMember("bob", "secret") {
			t.Error("bob lost another room")
		}
	})
}

func TestRoomRemoveMemberFailures(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		path := "/rooms/" + idText(e.general.ID)
		tests := []struct {
			name       string
			user       string
			target     string
			wantStatus int
			wantText   string
		}{
			{"a member is not an admin", "alice", idText(e.bob.ID), http.StatusForbidden, messageForbidden},
			{"a person removes themselves", "bob", idText(e.bob.ID), http.StatusForbidden, messageForbidden},
			{"an ID that is not a number", "admin", "bob", http.StatusUnprocessableEntity, messageActionInvalid},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				rec := e.post(tt.user, path+"/members/"+tt.target+"/remove", nil, datastar)
				if datastar {
					requireSSE(t, rec)
					requireBody(t, rec, []string{`id="members-panel"`, `<p class="form-error" role="alert">` + tt.wantText + `</p>`}, "window.location")
				} else {
					if rec.Code != tt.wantStatus {
						t.Fatalf("status = %d, want %d; body:\n%s", rec.Code, tt.wantStatus, rec.Body)
					}
					requireBody(t, rec, []string{tt.wantText, `id="members-panel"`})
				}
				if got := e.members("general", "alice"); len(got) != 3 {
					t.Errorf("members = %v, want all three", got)
				}
			})
		}
	})
}

func TestRoomRename(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		user string
		room string
		left bool // the admin leaves the room first
	}{
		{"the creator of a private room", "alice", "secret", false},
		{"an admin who made a visible room", "admin", "general", false},
		{"an admin who left a visible room", "admin", "random", true},
	}
	for _, tt := range tests {
		modes(t, func(t *testing.T, datastar bool) {
			t.Run(tt.name, func(t *testing.T) {
				e := newRoomsEnv(t)
				if tt.left {
					if err := e.rooms.Leave(t.Context(), e.actor(e.admin), e.room(tt.room).ID); err != nil {
						t.Fatalf("Leave: %+v", err)
					}
				}
				path := "/rooms/" + idText(e.room(tt.room).ID)
				rec := e.post(tt.user, path+"/rename", url.Values{"name": {" renamed "}, "description": {"New words"}}, datastar)
				if datastar {
					requireSSE(t, rec)
					requireBody(t, rec, []string{redirectScript(path)})
				} else if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != path {
					t.Errorf("status = %d, Location = %q", rec.Code, rec.Header().Get("Location"))
				}
				requireBody(t, e.send(get(path, e.cookies[tt.user])), []string{"<h1>renamed</h1>", "New words"})
			})
		})
	}
}

func TestRoomRenameFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		user       string
		form       url.Values
		wantStatus int
		wantID     string // the element that the Datastar answer patches
		wantText   []string
	}{
		{
			name: "an empty name", user: "alice", form: url.Values{"name": {""}, "description": {"typed"}},
			wantStatus: http.StatusUnprocessableEntity, wantID: `id="room-edit"`,
			wantText: []string{`id="edit-name-error"`, "Use 1 to 64 characters.", ">typed</textarea>", `<details id="room-edit" class="room-edit" open>`},
		},
		{
			name: "a name that is taken", user: "alice", form: url.Values{"name": {"General"}},
			wantStatus: http.StatusUnprocessableEntity, wantID: `id="room-edit"`,
			wantText: []string{"This name is taken.", `value="General"`},
		},
		{
			name: "a member who did not make the room", user: "bob", form: url.Values{"name": {"mine"}},
			wantStatus: http.StatusForbidden, wantID: `id="room-header"`,
			wantText: []string{`<p class="form-error" role="alert">` + messageForbidden + `</p>`},
		},
	}
	for _, tt := range tests {
		modes(t, func(t *testing.T, datastar bool) {
			t.Run(tt.name, func(t *testing.T) {
				e := newRoomsEnv(t)
				room := e.secret
				path := "/rooms/" + idText(room.ID)
				rec := e.post(tt.user, path+"/rename", tt.form, datastar)
				if datastar {
					requireSSE(t, rec)
					requireBody(t, rec, append([]string{tt.wantID}, tt.wantText...), "window.location")
				} else {
					if rec.Code != tt.wantStatus {
						t.Fatalf("status = %d, want %d; body:\n%s", rec.Code, tt.wantStatus, rec.Body)
					}
					requireBody(t, rec, append([]string{`id="room-header"`, `<aside id="sidebar">`}, tt.wantText...))
				}
				// Nothing changed.
				got, err := e.rooms.Get(t.Context(), e.actor(e.alice), room.ID)
				if err != nil || got.Name != "secret" || got.Description != "Just us" {
					t.Errorf("room = %+v, %v; want it unchanged", got, err)
				}
			})
		})
	}
}

func TestRoomDelete(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		user string
		room string
		left bool // the admin leaves the room first
	}{
		{"the creator of a private room", "alice", "secret", false},
		{"an admin who made a visible room", "admin", "general", false},
		{"an admin who left a visible room", "admin", "random", true},
	}
	for _, tt := range tests {
		modes(t, func(t *testing.T, datastar bool) {
			t.Run(tt.name, func(t *testing.T) {
				e := newRoomsEnv(t)
				if tt.left {
					if err := e.rooms.Leave(t.Context(), e.actor(e.admin), e.room(tt.room).ID); err != nil {
						t.Fatalf("Leave: %+v", err)
					}
				}
				path := "/rooms/" + idText(e.room(tt.room).ID)
				rec := e.post(tt.user, path+"/delete", nil, datastar)
				if datastar {
					requireSSE(t, rec)
					requireBody(t, rec, []string{redirectScript("/")})
				} else if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
					t.Errorf("status = %d, Location = %q", rec.Code, rec.Header().Get("Location"))
				}
				if e.exists(tt.room) {
					t.Error("the room still exists")
				}
				if rec := e.send(get(path, e.cookies[tt.user])); rec.Code != http.StatusNotFound {
					t.Errorf("GET status = %d, want 404", rec.Code)
				}
			})
		})
	}
}

func TestRoomDeleteByAMemberWhoDidNotMakeIt(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		path := "/rooms/" + idText(e.secret.ID)
		rec := e.post("bob", path+"/delete", nil, datastar)
		if datastar {
			requireSSE(t, rec)
			requireBody(t, rec, []string{`id="room-header"`, `<p class="form-error" role="alert">` + messageForbidden + `</p>`}, "window.location")
		} else {
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body:\n%s", rec.Code, rec.Body)
			}
			requireBody(t, rec, []string{messageForbidden, "<h1>secret</h1>"})
		}
		if !e.exists("secret") {
			t.Error("the room was deleted")
		}
	})
}

func TestRoomLeaveAndJoinOfAVisibleRoomByANonMemberStayInPlace(t *testing.T) {
	t.Parallel()
	// Joining a visible room as a non-member from the room page opens the room.
	e := newRoomsEnv(t)
	path := "/rooms/" + idText(e.general.ID)
	rec := e.post("carol", path+"/join", url.Values{"from": {"room"}}, true)
	requireBody(t, rec, []string{redirectScript(path)})
	requireBody(t, e.send(get(path, e.cookies["carol"])), []string{`id="leave-form"`, `data-username="carol"`}, `/join"`)
}

// TestRoomActionsOnAPrivateRoomOfOthersAre404 is the rule of SPEC §9.1 for every
// POST: a person who is not a member of a private room does not know it, admins
// included (D29: they manage it from the admin rooms page).
func TestRoomActionsOnAPrivateRoomOfOthersAre404(t *testing.T) {
	t.Parallel()
	for _, user := range []string{"carol", "admin"} {
		modes(t, func(t *testing.T, datastar bool) {
			e := newRoomsEnv(t)
			path := "/rooms/" + idText(e.secret.ID)
			actions := []struct {
				name string
				path string
				form url.Values
			}{
				{"join", path + "/join", nil},
				{"join from the browse page", path + "/join", url.Values{"from": {"browse"}}},
				{"leave", path + "/leave", nil},
				{"rename", path + "/rename", url.Values{"name": {"mine"}}},
				{"delete", path + "/delete", nil},
				{"add a member", path + "/members", url.Values{"user": {idText(e.carol.ID)}}},
				{"remove a member", path + "/members/" + idText(e.bob.ID) + "/remove", nil},
			}
			for _, a := range actions {
				t.Run(user+" "+a.name, func(t *testing.T) {
					rec := e.post(user, a.path, a.form, datastar)
					requireNotFound(t, rec, datastar, path)
				})
			}
			// Nothing changed.
			if got := e.members("secret", "alice"); strings.Join(got, ",") != "alice,bob" {
				t.Errorf("members = %v, want alice and bob", got)
			}
			got, err := e.rooms.Get(t.Context(), e.actor(e.alice), e.secret.ID)
			if err != nil || got.Name != "secret" {
				t.Errorf("room = %+v, %v", got, err)
			}
		})
	}
}

func TestRoomActionsOnRoomsThatDoNotExist(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		for _, action := range []string{"join", "leave", "rename", "delete", "members", "members/1/remove"} {
			for _, id := range []string{"9999", "abc", "0"} {
				t.Run(action+" "+id, func(t *testing.T) {
					rec := e.post("alice", "/rooms/"+id+"/"+action, url.Values{"name": {"x"}}, datastar)
					if id != "9999" {
						// An ID that is no number is not even a room path.
						if rec.Code != http.StatusNotFound {
							t.Fatalf("status = %d, want 404", rec.Code)
						}
						return
					}
					requireNotFound(t, rec, datastar, "/rooms/9999")
				})
			}
		}
	})
}

func TestRoomRoutesNeedASession(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	id := idText(e.general.ID)
	tests := []struct {
		name    string
		request *http.Request
	}{
		{"browse", get("/rooms")},
		{"new", get("/rooms/new")},
		{"room", get("/rooms/" + id)},
		{"create", postForm("/rooms", url.Values{"name": {"x"}})},
		{"join", postForm("/rooms/"+id+"/join", nil)},
		{"leave", postForm("/rooms/"+id+"/leave", nil)},
		{"rename", postForm("/rooms/"+id+"/rename", url.Values{"name": {"x"}})},
		{"delete", postForm("/rooms/"+id+"/delete", nil)},
		{"add", postForm("/rooms/"+id+"/members", url.Values{"user": {"1"}})},
		{"remove", postForm("/rooms/"+id+"/members/1/remove", nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(tt.request)
			if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
				t.Errorf("status = %d, Location = %q; want a redirect to the login page", rec.Code, rec.Header().Get("Location"))
			}
		})
	}
	if !e.isMember("alice", "general") || len(e.members("general", "alice")) != 3 || !e.exists("general") {
		t.Error("an action without a session changed the room")
	}
}

func TestRoomWrongMethods(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	for _, tt := range []struct{ method, path string }{
		{http.MethodGet, "/rooms/1/join"},
		{http.MethodPost, "/rooms/new"},
		{http.MethodPost, "/rooms/1"},
		{http.MethodGet, "/rooms/1/members/2/remove"},
	} {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: e.session("alice")})
		if rec := e.send(req); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: status = %d, want 405", tt.method, tt.path, rec.Code)
		}
	}
}

func TestBadRoomForms(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	path := "/rooms/" + idText(e.general.ID)
	big := strings.Repeat("x", maxFormBytes+1)
	for _, p := range []string{"/rooms", path + "/join", path + "/rename", path + "/members"} {
		t.Run(p, func(t *testing.T) {
			rec := e.post("alice", p, url.Values{"name": {big}}, false)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

// TestRoomActionFailed covers the failures that a database or a race cause and
// that the routes cannot be made to show: each is a message in the right place,
// patched for Datastar (status 200) and the whole page with the status for a
// plain post, and the error itself is never shown.
func TestRoomActionFailed(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	room := e.general
	tests := []struct {
		name       string
		err        error
		target     string
		wantStatus int
		wantMsg    string
		wantLogged bool
	}{
		{"conflict in the header", errors.New(app.TagConflict, "x"), targetHeader, http.StatusConflict, messageConflict, false},
		{"unexpected in the header", errors.New("Boom", "x"), targetHeader, http.StatusInternalServerError, messageTryAgain, true},
		{"forbidden in the members", errors.New(app.TagForbidden, "x"), targetMembers, http.StatusForbidden, messageForbidden, false},
		{"invalid without a message in the members", errors.New(app.TagInvalidInput, "x"), targetMembers, http.StatusUnprocessableEntity, messageActionInvalid, false},
		{"unexpected in the browse list", errors.New("Boom", "x"), targetBrowse, http.StatusInternalServerError, messageTryAgain, true},
		{"forbidden in the browse list", errors.New(app.TagForbidden, "x"), targetBrowse, http.StatusForbidden, messageForbidden, false},
	}
	for _, tt := range tests {
		modes(t, func(t *testing.T, datastar bool) {
			t.Run(tt.name, func(t *testing.T) {
				e.log.buf.Reset()
				var opts []reqOpt
				if datastar {
					opts = append(opts, asDatastar())
				}
				req := get("/rooms/"+idText(room.ID), append(opts, e.cookies["alice"])...)
				rec := httptest.NewRecorder()
				// Run the handler stack's session step by hand: the failure
				// handler reads the session of the request.
				e.srv.withAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					e.srv.roomActionFailed(w, r, tt.err, room.ID, tt.target)
				}), requireSession).ServeHTTP(rec, req)

				wantStatus := tt.wantStatus
				if datastar {
					wantStatus = http.StatusOK
				}
				if rec.Code != wantStatus {
					t.Errorf("status = %d, want %d", rec.Code, wantStatus)
				}
				wantID := map[string]string{targetHeader: `id="room-header"`, targetMembers: `id="members-panel"`, targetBrowse: `id="rooms-panel"`}[tt.target]
				requireBody(t, rec, []string{tt.wantMsg, wantID}, "Boom")
				if logged := strings.Contains(e.log.String(), `"request failed"`); logged != tt.wantLogged {
					t.Errorf("logged = %v, want %v", logged, tt.wantLogged)
				}
			})
		})
	}
}
