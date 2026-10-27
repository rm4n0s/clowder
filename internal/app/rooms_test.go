package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/clowder/internal/store"
)

// roomsEnv is the rooms service on a database with these users and rooms:
//
//	admin  admin, made lobby, a member of lobby only
//	root   admin, a member of nothing
//	alice  member, made den, a member of den only
//	bob    member, a member of lobby and den
//	carol  member, a member of nothing
//	dave   member, a member of nothing; the user that tests add
//	lobby  visible, members admin and bob
//	den    private, members alice and bob (added by alice)
type roomsEnv struct {
	*env
	rooms *app.Rooms
	// files holds the attachments, in filesDir.
	files    *files.Store
	filesDir string
	users    map[string]app.User
	lobby    app.Room
	den      app.Room
}

func newRoomsEnv(t *testing.T) *roomsEnv {
	t.Helper()
	e := &roomsEnv{env: newEnv(t), users: map[string]app.User{}}
	e.filesDir = filepath.Join(t.TempDir(), "files")
	var err error
	if e.files, err = files.New(e.filesDir); err != nil {
		t.Fatalf("files.New: %+v", err)
	}
	e.rooms = app.NewRooms(e.st, e.files, nil, nil)
	for _, u := range []struct {
		name string
		role store.Role
	}{
		{"admin", store.RoleAdmin}, {"root", store.RoleAdmin},
		{"alice", store.RoleMember}, {"bob", store.RoleMember}, {"carol", store.RoleMember}, {"dave", store.RoleMember},
	} {
		e.users[u.name] = e.signUp(u.name, u.role)
	}
	if e.lobby, err = e.rooms.Create(e.t.Context(), e.as("admin"), app.RoomInput{Name: "lobby", Description: "Everyone", Visibility: app.VisibilityVisible}); err != nil {
		t.Fatalf("Create lobby: %+v", err)
	}
	if err := e.rooms.Join(e.t.Context(), e.as("bob"), e.lobby.ID); err != nil {
		t.Fatalf("Join lobby: %+v", err)
	}
	if e.den, err = e.rooms.Create(e.t.Context(), e.as("alice"), app.RoomInput{Name: "den", Visibility: app.VisibilityPrivate}); err != nil {
		t.Fatalf("Create den: %+v", err)
	}
	if err := e.rooms.AddMember(e.t.Context(), e.as("alice"), e.den.ID, e.users["bob"].ID); err != nil {
		t.Fatalf("AddMember den: %+v", err)
	}
	return e
}

// as returns the Actor of the named user.
func (e *roomsEnv) as(name string) app.Actor {
	e.t.Helper()
	u, ok := e.users[name]
	if !ok {
		e.t.Fatalf("no user %q", name)
	}
	return actor(u)
}

// room returns the named room of the fixture.
func (e *roomsEnv) room(name string) app.Room {
	e.t.Helper()
	switch name {
	case "lobby":
		return e.lobby
	case "den":
		return e.den
	}
	e.t.Fatalf("no room %q", name)
	return app.Room{}
}

// events returns every audit event, oldest first.
func (e *roomsEnv) events() []store.AuditEvent {
	e.t.Helper()
	rows, err := e.st.ListAuditEvents(e.t.Context(), store.AuditFilter{}, 1000, 0)
	if err != nil {
		e.t.Fatalf("ListAuditEvents: %+v", err)
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows
}

// wantEvent is what a test expects of one audit event. EntityID 0 means "any
// but zero".
type wantEvent struct {
	actor      string
	action     app.AuditAction
	room       uint64
	entityType app.EntityType
	entityID   uint64
	summary    string
	details    map[string]any
}

// requireNewEvents fails the test unless the events after the first before
// ones are exactly want.
func (e *roomsEnv) requireNewEvents(before int, want ...wantEvent) {
	e.t.Helper()
	all := e.events()
	if len(all) < before {
		e.t.Fatalf("%d events, fewer than the %d before", len(all), before)
	}
	got := all[before:]
	if len(got) != len(want) {
		var lines []string
		for _, ev := range got {
			lines = append(lines, ev.Summary)
		}
		e.t.Fatalf("%d new events %q, want %d", len(got), lines, len(want))
	}
	for i, w := range want {
		ev := got[i]
		if ev.ActorID != e.users[w.actor].ID || ev.Action != w.action || ev.RoomID != w.room ||
			ev.EntityType != w.entityType || ev.Summary != w.summary {
			e.t.Errorf("event %d = {actor %d, %s, room %d, %s, %q}, want {actor %d, %s, room %d, %s, %q}", i,
				ev.ActorID, ev.Action, ev.RoomID, ev.EntityType, ev.Summary,
				e.users[w.actor].ID, w.action, w.room, w.entityType, w.summary)
		}
		if (w.entityID == 0 && ev.EntityID == 0) || (w.entityID != 0 && ev.EntityID != w.entityID) {
			e.t.Errorf("event %d: EntityID = %d, want %d", i, ev.EntityID, w.entityID)
		}
		var details map[string]any
		if err := json.Unmarshal([]byte(ev.Details), &details); err != nil {
			e.t.Fatalf("event %d: details %q: %v", i, ev.Details, err)
		}
		if !reflect.DeepEqual(details, w.details) {
			e.t.Errorf("event %d: details = %v, want %v", i, details, w.details)
		}
		if !ev.CreatedAt.Equal(e.clk.Now()) {
			e.t.Errorf("event %d: CreatedAt = %v, want the clock's %v", i, ev.CreatedAt, e.clk.Now())
		}
	}
}

// membershipID returns the ID of the membership of the user in the room.
func (e *roomsEnv) membershipID(room uint64, user string) uint64 {
	e.t.Helper()
	m, err := e.st.RoomMemberOf(e.t.Context(), room, e.users[user].ID)
	if err != nil {
		e.t.Fatalf("RoomMemberOf %s: %+v", user, err)
	}
	return m.ID
}

// memberNames returns the usernames of the members of the room, for the
// admin who may read what the room holds only as a member: it asks the store.
func (e *roomsEnv) memberNames(room uint64) []string {
	e.t.Helper()
	rows, err := e.st.ListRoomMembers(e.t.Context(), room)
	if err != nil {
		e.t.Fatalf("ListRoomMembers: %+v", err)
	}
	names := []string{}
	for _, m := range rows {
		names = append(names, m.User.Username)
	}
	return names
}

// permCase is one row of a permission table: who calls the method on which
// room, and the tag the call must fail with ("" when it must succeed).
type permCase struct {
	actor string
	room  string
	want  string
}

// runPermissions runs call as every actor of cases on a fresh fixture and
// checks the result. routes gives, for each tag a case expects, the part of
// the route that the error must have. A failed call writes no audit event.
func runPermissions(t *testing.T, call func(e *roomsEnv, a app.Actor, room app.Room) error, routes map[string]string, cases []permCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.actor+" on "+tc.room, func(t *testing.T) {
			e := newRoomsEnv(t)
			before := len(e.events())
			err := call(e, e.as(tc.actor), e.room(tc.room))
			if tc.want == "" {
				if err != nil {
					t.Fatalf("got %+v, want success", err)
				}
				return
			}
			_ = requireTag(t, err, tc.want, routes[tc.want])
			if n := len(e.events()); n != before {
				t.Errorf("a failed call wrote %d events", n-before)
			}
		})
	}
}

const (
	notFoundPrivate = "loadReadableRoom.NotFound"
	notFoundMissing = "Store.RoomByID.NotFound"
)

func TestRoomsAliasesMatchTheStore(t *testing.T) {
	t.Parallel()
	if app.VisibilityVisible != store.VisibilityVisible || app.VisibilityPrivate != store.VisibilityPrivate {
		t.Error("the Visibility constants differ from the store's")
	}
}

func TestRoomsCreatePermissions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		actor      string
		visibility app.Visibility
		want       string
	}{
		{"admin", app.VisibilityVisible, ""},
		{"admin", app.VisibilityPrivate, ""},
		{"alice", app.VisibilityVisible, app.TagForbidden},
		{"alice", app.VisibilityPrivate, ""},
		{"carol", app.VisibilityVisible, app.TagForbidden},
		{"carol", app.VisibilityPrivate, ""},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s makes a %s room", tt.actor, tt.visibility), func(t *testing.T) {
			e := newRoomsEnv(t)
			before := len(e.events())
			room, err := e.rooms.Create(t.Context(), e.as(tt.actor), app.RoomInput{Name: "new", Visibility: tt.visibility})
			if tt.want != "" {
				_ = requireTag(t, err, tt.want, "Rooms.Create.Forbidden")
				if n := len(e.events()); n != before {
					t.Errorf("a failed call wrote %d events", n-before)
				}
				if rooms, err := e.rooms.AllRooms(t.Context(), e.as("admin")); err != nil || len(rooms) != 2 {
					t.Errorf("%d rooms after a refused Create (%v), want 2", len(rooms), err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Create: %+v", err)
			}
			if room.Visibility != tt.visibility || room.CreatedBy != e.users[tt.actor].ID {
				t.Errorf("room = %+v", room)
			}
		})
	}
}

func TestRoomsCreate(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	before := len(e.events())
	e.clk.Advance(5 * time.Minute)
	got, err := e.rooms.Create(t.Context(), e.as("alice"), app.RoomInput{
		Name: "  Design  ", Description: "  Pixels and things \n", Visibility: app.VisibilityPrivate,
	})
	if err != nil {
		t.Fatalf("Create: %+v", err)
	}
	want := app.Room{
		ID: got.ID, Name: "Design", Description: "Pixels and things", Visibility: app.VisibilityPrivate,
		CreatedBy: e.users["alice"].ID, CreatedAt: e.clk.Now(), MemberCount: 1, IsMember: true, CanManage: true,
	}
	if got.ID == 0 || !reflect.DeepEqual(got, want) {
		t.Errorf("room = %+v, want %+v", got, want)
	}

	// The same room is what Get shows.
	again, err := e.rooms.Get(t.Context(), e.as("alice"), got.ID)
	if err != nil || !reflect.DeepEqual(again, want) {
		t.Errorf("Get = %+v, %v, want %+v", again, err, want)
	}

	// The creator is the first member, not added by anyone.
	members, err := e.rooms.Members(t.Context(), e.as("alice"), got.ID)
	if err != nil || len(members) != 1 {
		t.Fatalf("Members = %v, %v", members, err)
	}
	if m := members[0]; m.UserID != e.users["alice"].ID || m.Username != "alice" || m.DisplayName != "Alice" ||
		m.AddedBy != nil || m.AddedByDisplayName != "" || !m.JoinedAt.Equal(e.clk.Now()) {
		t.Errorf("member = %+v", m)
	}

	// The three columns.
	cols, err := e.st.ListBoardColumns(t.Context(), got.ID)
	if err != nil {
		t.Fatal(err)
	}
	var gotCols []string
	for i, c := range cols {
		gotCols = append(gotCols, fmt.Sprintf("%d:%s", c.Position, c.Name))
		if c.Position != i {
			t.Errorf("column %q has position %d, want %d", c.Name, c.Position, i)
		}
	}
	if !reflect.DeepEqual(gotCols, []string{"0:To do", "1:In progress", "2:Done"}) {
		t.Errorf("columns = %v", gotCols)
	}

	// One event, room.created; the creator's membership is part of it.
	e.requireNewEvents(before, wantEvent{
		actor: "alice", action: app.ActionRoomCreated, room: got.ID, entityType: app.EntityRoom, entityID: got.ID,
		summary: "Alice created #Design",
		details: map[string]any{
			"name":        map[string]any{"old": nil, "new": "Design"},
			"description": map[string]any{"old": nil, "new": "Pixels and things"},
			"visibility":  map[string]any{"old": nil, "new": "private"},
		},
	})
}

func TestRoomsCreateValidation(t *testing.T) {
	t.Parallel()
	long := func(r string, n int) string { return strings.Repeat(r, n) }
	tests := []struct {
		name       string
		actor      string
		in         app.RoomInput
		wantFields map[string]string // nil: must succeed
		wantName   string            // the stored name when it succeeds
		rawVis     bool              // keep in.Visibility as it is, even when empty
	}{
		{name: "one character", in: app.RoomInput{Name: "x"}, wantName: "x"},
		{name: "64 characters", in: app.RoomInput{Name: long("a", 64)}, wantName: long("a", 64)},
		{name: "65 characters", in: app.RoomInput{Name: long("a", 65)}, wantFields: map[string]string{app.FieldName: "Use 1 to 64 characters."}},
		{name: "64 multi-byte characters", in: app.RoomInput{Name: long("é", 64)}, wantName: long("é", 64)},
		{name: "65 multi-byte characters", in: app.RoomInput{Name: long("é", 65)}, wantFields: map[string]string{app.FieldName: "Use 1 to 64 characters."}},
		{name: "emoji and CJK count as one", in: app.RoomInput{Name: "デザイン🎨"}, wantName: "デザイン🎨"},
		{name: "empty", in: app.RoomInput{Name: ""}, wantFields: map[string]string{app.FieldName: "Use 1 to 64 characters."}},
		{name: "only spaces", in: app.RoomInput{Name: " \t\n "}, wantFields: map[string]string{app.FieldName: "Use 1 to 64 characters."}},
		{name: "trimmed to 64", in: app.RoomInput{Name: "  " + long("a", 64) + "  "}, wantName: long("a", 64)},
		{name: "not valid UTF-8", in: app.RoomInput{Name: "ab\xff"}, wantFields: map[string]string{app.FieldName: "Use 1 to 64 characters."}},
		{name: "spaces inside stay", in: app.RoomInput{Name: "a  b"}, wantName: "a  b"},
		{name: "500 character description", in: app.RoomInput{Name: "d1", Description: long("ü", 500)}, wantName: "d1"},
		{name: "501 character description", in: app.RoomInput{Name: "d2", Description: long("ü", 501)}, wantFields: map[string]string{app.FieldDescription: "Use at most 500 characters."}},
		{name: "description trimmed to 500", in: app.RoomInput{Name: "d3", Description: " " + long("x", 500) + " "}, wantName: "d3"},
		{name: "not valid UTF-8 description", in: app.RoomInput{Name: "d4", Description: "\xff"}, wantFields: map[string]string{app.FieldDescription: "Use at most 500 characters."}},
		{
			name: "every mistake at once", in: app.RoomInput{Name: "", Description: long("x", 501), Visibility: "secret"}, actor: "admin",
			wantFields: map[string]string{
				app.FieldName: "Use 1 to 64 characters.", app.FieldDescription: "Use at most 500 characters.",
				app.FieldVisibility: "Choose visible or private.",
			},
		},
		{name: "unknown visibility", actor: "admin", in: app.RoomInput{Name: "v1", Visibility: "secret"}, wantFields: map[string]string{app.FieldVisibility: "Choose visible or private."}},
		{name: "no visibility", actor: "alice", rawVis: true, in: app.RoomInput{Name: "v2", Visibility: ""}, wantFields: map[string]string{app.FieldVisibility: "Choose visible or private."}},
		{name: "taken", in: app.RoomInput{Name: "lobby"}, wantFields: map[string]string{app.FieldName: "This name is taken."}},
		{name: "taken in capitals", in: app.RoomInput{Name: "LOBBY"}, wantFields: map[string]string{app.FieldName: "This name is taken."}},
		{name: "taken with spaces around", in: app.RoomInput{Name: "  Lobby "}, wantFields: map[string]string{app.FieldName: "This name is taken."}},
		{name: "taken private room name", in: app.RoomInput{Name: "DEN"}, wantFields: map[string]string{app.FieldName: "This name is taken."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newRoomsEnv(t)
			who := tt.actor
			if who == "" {
				who = "alice"
			}
			in := tt.in
			if in.Visibility == "" && !tt.rawVis {
				in.Visibility = app.VisibilityPrivate
			}
			before := len(e.events())
			room, err := e.rooms.Create(t.Context(), e.as(who), in)
			if tt.wantFields == nil {
				if err != nil {
					t.Fatalf("Create: %+v", err)
				}
				if room.Name != tt.wantName {
					t.Errorf("Name = %q, want %q", room.Name, tt.wantName)
				}
				return
			}
			appErr := requireTag(t, err, app.TagInvalidInput, "Rooms.Create")
			if got := fieldsOf(t, appErr); !reflect.DeepEqual(got, tt.wantFields) {
				t.Errorf("fields = %v, want %v", got, tt.wantFields)
			}
			if n := len(e.events()); n != before {
				t.Errorf("a failed Create wrote %d events", n-before)
			}
			if rooms, err := e.rooms.AllRooms(t.Context(), e.as("admin")); err != nil || len(rooms) != 2 {
				t.Errorf("%d rooms after a failed Create (%v), want 2", len(rooms), err)
			}
		})
	}
}

func TestRoomsCreateNameUniqueness(t *testing.T) {
	t.Parallel()
	// The case of any letter counts, not only a to z (the unique index folds
	// only those, so the service checks).
	tests := []struct{ first, second string }{
		{"Café", "CAFÉ"},
		{"über", "ÜBER"},
		{"Ελληνικά", "ΕΛΛΗΝΙΚΆ"},
		{"design", "DeSiGn"},
	}
	for _, tt := range tests {
		t.Run(tt.first+" and "+tt.second, func(t *testing.T) {
			e := newRoomsEnv(t)
			if _, err := e.rooms.Create(t.Context(), e.as("alice"), app.RoomInput{Name: tt.first, Visibility: app.VisibilityPrivate}); err != nil {
				t.Fatalf("first Create: %+v", err)
			}
			_, err := e.rooms.Create(t.Context(), e.as("bob"), app.RoomInput{Name: tt.second, Visibility: app.VisibilityPrivate})
			appErr := requireTag(t, err, app.TagInvalidInput, "Rooms.Create.func1.InvalidInput")
			if got := fieldsOf(t, appErr); got[app.FieldName] != "This name is taken." {
				t.Errorf("fields = %v", got)
			}
		})
	}
}

func TestRoomsGet(t *testing.T) {
	t.Parallel()
	routes := map[string]string{app.TagNotFound: notFoundPrivate}
	cases := []permCase{
		{"admin", "lobby", ""}, {"root", "lobby", ""}, {"alice", "lobby", ""}, {"bob", "lobby", ""}, {"carol", "lobby", ""},
		// Admins are no exception for reading (D29).
		{"admin", "den", app.TagNotFound}, {"root", "den", app.TagNotFound},
		{"alice", "den", ""}, {"bob", "den", ""}, {"carol", "den", app.TagNotFound},
	}
	runPermissions(t, func(e *roomsEnv, a app.Actor, r app.Room) error {
		_, err := e.rooms.Get(t.Context(), a, r.ID)
		return err
	}, routes, cases)
}

func TestRoomsGetView(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	tests := []struct {
		actor, room string
		want        app.Room
	}{
		{"admin", "lobby", app.Room{MemberCount: 2, IsMember: true, CanManage: true}},
		{"root", "lobby", app.Room{MemberCount: 2, IsMember: false, CanManage: true}},
		{"bob", "lobby", app.Room{MemberCount: 2, IsMember: true, CanManage: false}},
		{"carol", "lobby", app.Room{MemberCount: 2, IsMember: false, CanManage: false}},
		{"alice", "den", app.Room{MemberCount: 2, IsMember: true, CanManage: true}},
		{"bob", "den", app.Room{MemberCount: 2, IsMember: true, CanManage: false}},
	}
	for _, tt := range tests {
		t.Run(tt.actor+" sees "+tt.room, func(t *testing.T) {
			base := e.room(tt.room)
			got, err := e.rooms.Get(t.Context(), e.as(tt.actor), base.ID)
			if err != nil {
				t.Fatalf("Get: %+v", err)
			}
			want := tt.want
			want.ID, want.Name, want.Description, want.Visibility = base.ID, base.Name, base.Description, base.Visibility
			want.CreatedBy, want.CreatedAt = base.CreatedBy, base.CreatedAt
			if !reflect.DeepEqual(got, want) {
				t.Errorf("room = %+v, want %+v", got, want)
			}
		})
	}
}

func TestRoomsMissingRoom(t *testing.T) {
	t.Parallel()
	// A room that does not exist is TagNotFound for every method, for
	// admins too (the store's tag, with the metadata "op").
	const missing = 9999
	calls := map[string]func(e *roomsEnv, a app.Actor) error{
		"Get": func(e *roomsEnv, a app.Actor) error {
			_, err := e.rooms.Get(t.Context(), a, missing)
			return err
		},
		"Members": func(e *roomsEnv, a app.Actor) error {
			_, err := e.rooms.Members(t.Context(), a, missing)
			return err
		},
		"Join":  func(e *roomsEnv, a app.Actor) error { return e.rooms.Join(t.Context(), a, missing) },
		"Leave": func(e *roomsEnv, a app.Actor) error { return e.rooms.Leave(t.Context(), a, missing) },
		"AddMember": func(e *roomsEnv, a app.Actor) error {
			return e.rooms.AddMember(t.Context(), a, missing, e.users["dave"].ID)
		},
		"RemoveMember": func(e *roomsEnv, a app.Actor) error {
			return e.rooms.RemoveMember(t.Context(), a, missing, e.users["bob"].ID)
		},
		"Update": func(e *roomsEnv, a app.Actor) error {
			return e.rooms.Update(t.Context(), a, missing, app.RoomInput{Name: "x"})
		},
		"Delete": func(e *roomsEnv, a app.Actor) error { return e.rooms.Delete(t.Context(), a, missing) },
	}
	for name, call := range calls {
		for _, who := range []string{"admin", "bob"} {
			t.Run(name+" as "+who, func(t *testing.T) {
				e := newRoomsEnv(t)
				before := len(e.events())
				err := call(e, e.as(who))
				appErr := requireTag(t, err, app.TagNotFound, "Rooms."+name)
				if !appErr.HasRoute(notFoundMissing) {
					t.Errorf("route = %s, want it to end in %s", appErr.Route(), notFoundMissing)
				}
				if n := len(e.events()); n != before {
					t.Errorf("wrote %d events", n-before)
				}
			})
		}
	}
}

func TestRoomsPrivateAndMissingRoomsLookAlike(t *testing.T) {
	t.Parallel()
	// The tag is the same; only the metadata tells them apart, for the log.
	e := newRoomsEnv(t)
	_, errPrivate := e.rooms.Get(t.Context(), e.as("carol"), e.den.ID)
	_, errMissing := e.rooms.Get(t.Context(), e.as("carol"), 9999)
	private := requireTag(t, errPrivate, app.TagNotFound, notFoundPrivate)
	missing := requireTag(t, errMissing, app.TagNotFound, notFoundMissing)
	if private.Message == "" || private.Metadata["reason"] != "private" {
		t.Errorf("private metadata = %v", private.Metadata)
	}
	if missing.Metadata["op"] != "roomByID" {
		t.Errorf("missing metadata = %v", missing.Metadata)
	}
}

func TestRoomsMembers(t *testing.T) {
	t.Parallel()
	routes := map[string]string{app.TagNotFound: notFoundPrivate}
	cases := []permCase{
		{"admin", "lobby", ""}, {"root", "lobby", ""}, {"alice", "lobby", ""}, {"bob", "lobby", ""}, {"carol", "lobby", ""},
		{"admin", "den", app.TagNotFound}, {"root", "den", app.TagNotFound},
		{"alice", "den", ""}, {"bob", "den", ""}, {"carol", "den", app.TagNotFound},
	}
	runPermissions(t, func(e *roomsEnv, a app.Actor, r app.Room) error {
		_, err := e.rooms.Members(t.Context(), a, r.ID)
		return err
	}, routes, cases)
}

func TestRoomsMembersView(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	members, err := e.rooms.Members(t.Context(), e.as("bob"), e.den.ID)
	if err != nil {
		t.Fatal(err)
	}
	aliceID, bobID := e.users["alice"].ID, e.users["bob"].ID
	want := []app.Member{
		{UserID: aliceID, Username: "alice", DisplayName: "Alice", JoinedAt: e.clk.Now()},
		{UserID: bobID, Username: "bob", DisplayName: "Bob", AddedBy: &aliceID, AddedByDisplayName: "Alice", JoinedAt: e.clk.Now()},
	}
	if !reflect.DeepEqual(members, want) {
		t.Errorf("members = %+v, want %+v", members, want)
	}

	// Ordered by display name, whatever the order of joining.
	for _, who := range []string{"dave", "carol"} {
		if err := e.rooms.Join(t.Context(), e.as(who), e.lobby.ID); err != nil {
			t.Fatal(err)
		}
	}
	members, err = e.rooms.Members(t.Context(), e.as("carol"), e.lobby.ID)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range members {
		names = append(names, m.DisplayName)
	}
	if !reflect.DeepEqual(names, []string{"Admin", "Bob", "Carol", "Dave"}) {
		t.Errorf("names = %v", names)
	}
}

func TestRoomsLists(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	ctx := t.Context()
	// More rooms, to see the order (by name, ignoring case) and the filters.
	for _, r := range []struct {
		by, name string
		v        app.Visibility
	}{
		{"admin", "Zebra", app.VisibilityVisible},
		{"admin", "apple", app.VisibilityVisible},
		{"alice", "Beta", app.VisibilityPrivate},
		{"carol", "carol-only", app.VisibilityPrivate},
	} {
		if _, err := e.rooms.Create(ctx, e.as(r.by), app.RoomInput{Name: r.name, Visibility: r.v}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.rooms.Join(ctx, e.as("carol"), e.lobby.ID); err != nil {
		t.Fatal(err)
	}

	type row struct {
		name   string
		member bool
		manage bool
		count  int
	}
	rowsOf := func(rooms []app.Room, err error) []row {
		t.Helper()
		if err != nil {
			t.Fatalf("%+v", err)
		}
		out := []row{}
		for _, r := range rooms {
			out = append(out, row{r.Name, r.IsMember, r.CanManage, r.MemberCount})
		}
		return out
	}
	tests := []struct {
		name string
		got  []row
		want []row
	}{
		{
			name: "Visible for carol",
			got:  rowsOf(e.rooms.Visible(ctx, e.as("carol"))),
			want: []row{{"apple", false, false, 1}, {"lobby", true, false, 3}, {"Zebra", false, false, 1}},
		},
		{
			name: "Visible for admin",
			got:  rowsOf(e.rooms.Visible(ctx, e.as("admin"))),
			want: []row{{"apple", true, true, 1}, {"lobby", true, true, 3}, {"Zebra", true, true, 1}},
		},
		{
			name: "Mine for carol",
			got:  rowsOf(e.rooms.Mine(ctx, e.as("carol"))),
			want: []row{{"carol-only", true, true, 1}, {"lobby", true, false, 3}},
		},
		{
			name: "Mine for alice",
			got:  rowsOf(e.rooms.Mine(ctx, e.as("alice"))),
			want: []row{{"Beta", true, true, 1}, {"den", true, true, 2}},
		},
		{
			name: "Mine for root",
			got:  rowsOf(e.rooms.Mine(ctx, e.as("root"))),
			want: []row{},
		},
		{
			name: "AllRooms for root",
			got:  rowsOf(e.rooms.AllRooms(ctx, e.as("root"))),
			want: []row{
				{"apple", false, true, 1}, {"Beta", false, true, 1}, {"carol-only", false, true, 1}, {"den", false, true, 2},
				{"lobby", false, true, 3}, {"Zebra", false, true, 1},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.got, tt.want) {
				t.Errorf("rows = %v, want %v", tt.got, tt.want)
			}
		})
	}
}

func TestRoomsAllRoomsPermissions(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		actor string
		want  string
	}{
		{"admin", ""}, {"root", ""}, {"alice", app.TagForbidden}, {"bob", app.TagForbidden}, {"carol", app.TagForbidden},
	} {
		t.Run(tt.actor, func(t *testing.T) {
			e := newRoomsEnv(t)
			rooms, err := e.rooms.AllRooms(t.Context(), e.as(tt.actor))
			if tt.want != "" {
				_ = requireTag(t, err, tt.want, "Rooms.AllRooms.Forbidden")
				if rooms != nil {
					t.Errorf("rooms = %v, want none", rooms)
				}
				return
			}
			if err != nil || len(rooms) != 2 {
				t.Errorf("AllRooms = %v, %v, want both rooms", rooms, err)
			}
		})
	}
}

func TestRoomsAdminMembersPermissions(t *testing.T) {
	t.Parallel()
	// Admins list the members of every room, private ones they are not in
	// included (D29); anyone else is forbidden, whatever the room.
	routes := map[string]string{app.TagForbidden: "Rooms.AdminMembers.Forbidden"}
	cases := []permCase{
		{"admin", "lobby", ""}, {"root", "lobby", ""},
		{"alice", "lobby", app.TagForbidden}, {"bob", "lobby", app.TagForbidden}, {"carol", "lobby", app.TagForbidden},
		{"admin", "den", ""}, {"root", "den", ""},
		{"alice", "den", app.TagForbidden}, {"bob", "den", app.TagForbidden}, {"carol", "den", app.TagForbidden},
	}
	runPermissions(t, func(e *roomsEnv, a app.Actor, r app.Room) error {
		members, err := e.rooms.AdminMembers(t.Context(), a, r.ID)
		if err != nil && members != nil {
			t.Errorf("members = %v with an error, want none", members)
		}
		return err
	}, routes, cases)
}

func TestRoomsAdminMembers(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	aliceID, bobID := e.users["alice"].ID, e.users["bob"].ID

	// root is in neither room and reads the members of the private one.
	members, err := e.rooms.AdminMembers(t.Context(), e.as("root"), e.den.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []app.Member{
		{UserID: aliceID, Username: "alice", DisplayName: "Alice", JoinedAt: e.clk.Now()},
		{UserID: bobID, Username: "bob", DisplayName: "Bob", AddedBy: &aliceID, AddedByDisplayName: "Alice", JoinedAt: e.clk.Now()},
	}
	if !reflect.DeepEqual(members, want) {
		t.Errorf("members of den = %+v, want %+v", members, want)
	}
	// The same members as the ones a member of the room sees.
	asMember, err := e.rooms.Members(t.Context(), e.as("bob"), e.den.ID)
	if err != nil || !reflect.DeepEqual(asMember, members) {
		t.Errorf("Members = %+v, %v; want the same as AdminMembers", asMember, err)
	}

	members, err = e.rooms.AdminMembers(t.Context(), e.as("root"), e.lobby.ID)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range members {
		names = append(names, m.Username)
	}
	if !reflect.DeepEqual(names, []string{"admin", "bob"}) {
		t.Errorf("members of lobby = %v, want admin and bob", names)
	}

	// A private room with no members is listed empty, not as an error.
	for _, who := range []string{"alice", "bob"} {
		if err := e.rooms.Leave(t.Context(), e.as(who), e.den.ID); err != nil {
			t.Fatal(err)
		}
	}
	members, err = e.rooms.AdminMembers(t.Context(), e.as("root"), e.den.ID)
	if err != nil || len(members) != 0 {
		t.Errorf("members of the empty room = %v, %v; want none", members, err)
	}

	// A room that does not exist is TagNotFound for an admin; a member is
	// forbidden before the room is looked up.
	_, err = e.rooms.AdminMembers(t.Context(), e.as("root"), 9999)
	appErr := requireTag(t, err, app.TagNotFound, "Rooms.AdminMembers->Store.RoomByID.NotFound")
	if appErr.Metadata["op"] != "roomByID" {
		t.Errorf("metadata = %v", appErr.Metadata)
	}
	_, err = e.rooms.AdminMembers(t.Context(), e.as("bob"), 9999)
	_ = requireTag(t, err, app.TagForbidden, "Rooms.AdminMembers.Forbidden")
}

func TestRoomsJoin(t *testing.T) {
	t.Parallel()
	routes := map[string]string{app.TagNotFound: notFoundPrivate}
	cases := []permCase{
		{"admin", "lobby", ""}, {"root", "lobby", ""}, {"alice", "lobby", ""}, {"bob", "lobby", ""}, {"carol", "lobby", ""},
		{"admin", "den", app.TagNotFound}, {"root", "den", app.TagNotFound}, {"carol", "den", app.TagNotFound},
		// A member of the private room is a member already: nothing to do.
		{"alice", "den", ""}, {"bob", "den", ""},
	}
	runPermissions(t, func(e *roomsEnv, a app.Actor, r app.Room) error {
		return e.rooms.Join(t.Context(), a, r.ID)
	}, routes, cases)
}

func TestRoomsJoinAudit(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	before := len(e.events())
	if err := e.rooms.Join(t.Context(), e.as("carol"), e.lobby.ID); err != nil {
		t.Fatal(err)
	}
	if got := e.memberNames(e.lobby.ID); !reflect.DeepEqual(got, []string{"admin", "bob", "carol"}) {
		t.Errorf("members = %v", got)
	}
	e.requireNewEvents(before, wantEvent{
		actor: "carol", action: app.ActionMemberJoined, room: e.lobby.ID, entityType: app.EntityMember,
		entityID: e.membershipID(e.lobby.ID, "carol"), summary: "Carol joined #lobby",
		details: map[string]any{"userID": float64(e.users["carol"].ID), "username": "carol"},
	})
	m, err := e.st.RoomMemberOf(t.Context(), e.lobby.ID, e.users["carol"].ID)
	if err != nil || m.AddedBy != nil {
		t.Errorf("membership = %+v, %v, want one nobody added", m, err)
	}

	// Joining again is a no-op without an event.
	before = len(e.events())
	if err := e.rooms.Join(t.Context(), e.as("carol"), e.lobby.ID); err != nil {
		t.Fatal(err)
	}
	e.requireNewEvents(before)
	if n := len(e.memberNames(e.lobby.ID)); n != 3 {
		t.Errorf("%d members after a second Join, want 3", n)
	}
}

func TestRoomsLeave(t *testing.T) {
	t.Parallel()
	routes := map[string]string{app.TagNotFound: notFoundPrivate}
	cases := []permCase{
		{"admin", "lobby", ""}, {"root", "lobby", ""}, {"alice", "lobby", ""}, {"bob", "lobby", ""}, {"carol", "lobby", ""},
		{"admin", "den", app.TagNotFound}, {"root", "den", app.TagNotFound}, {"carol", "den", app.TagNotFound},
		{"alice", "den", ""}, {"bob", "den", ""},
	}
	runPermissions(t, func(e *roomsEnv, a app.Actor, r app.Room) error {
		return e.rooms.Leave(t.Context(), a, r.ID)
	}, routes, cases)
}

func TestRoomsLeaveAudit(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	id := e.membershipID(e.lobby.ID, "bob")
	before := len(e.events())
	if err := e.rooms.Leave(t.Context(), e.as("bob"), e.lobby.ID); err != nil {
		t.Fatal(err)
	}
	if got := e.memberNames(e.lobby.ID); !reflect.DeepEqual(got, []string{"admin"}) {
		t.Errorf("members = %v, want only admin", got)
	}
	e.requireNewEvents(before, wantEvent{
		actor: "bob", action: app.ActionMemberLeft, room: e.lobby.ID, entityType: app.EntityMember, entityID: id,
		summary: "Bob left #lobby", details: map[string]any{"userID": float64(e.users["bob"].ID), "username": "bob"},
	})

	// Not a member of a visible room: a no-op, no event.
	before = len(e.events())
	for _, who := range []string{"carol", "bob"} {
		if err := e.rooms.Leave(t.Context(), e.as(who), e.lobby.ID); err != nil {
			t.Fatal(err)
		}
	}
	e.requireNewEvents(before)
}

func TestRoomsPrivateRoomMayEndEmpty(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	for _, who := range []string{"alice", "bob"} {
		if err := e.rooms.Leave(t.Context(), e.as(who), e.den.ID); err != nil {
			t.Fatalf("Leave %s: %+v", who, err)
		}
	}
	// The room is kept: admins list it, with no members, and cannot read it.
	rooms, err := e.rooms.AllRooms(t.Context(), e.as("admin"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rooms {
		if r.ID == e.den.ID {
			found = true
			if r.MemberCount != 0 || r.IsMember {
				t.Errorf("den = %+v, want no members", r)
			}
		}
	}
	if !found {
		t.Fatal("AllRooms lacks the empty private room")
	}
	_, err = e.rooms.Get(t.Context(), e.as("admin"), e.den.ID)
	_ = requireTag(t, err, app.TagNotFound, notFoundPrivate)
	// An admin deletes it.
	if err := e.rooms.Delete(t.Context(), e.as("admin"), e.den.ID); err != nil {
		t.Fatalf("Delete: %+v", err)
	}
}

func TestRoomsAddMember(t *testing.T) {
	t.Parallel()
	routes := map[string]string{app.TagNotFound: notFoundPrivate, app.TagForbidden: "Rooms.AddMember.func1.Forbidden"}
	cases := []permCase{
		// Visible: only members add.
		{"admin", "lobby", ""}, {"bob", "lobby", ""},
		{"root", "lobby", app.TagForbidden}, {"alice", "lobby", app.TagForbidden}, {"carol", "lobby", app.TagForbidden},
		// Private: members add; everyone else does not even know the room.
		{"alice", "den", ""}, {"bob", "den", ""},
		{"admin", "den", app.TagNotFound}, {"root", "den", app.TagNotFound}, {"carol", "den", app.TagNotFound},
	}
	runPermissions(t, func(e *roomsEnv, a app.Actor, r app.Room) error {
		return e.rooms.AddMember(t.Context(), a, r.ID, e.users["dave"].ID)
	}, routes, cases)
}

func TestRoomsCandidatesPermissions(t *testing.T) {
	t.Parallel()
	routes := map[string]string{app.TagNotFound: notFoundPrivate, app.TagForbidden: "Rooms.Candidates.Forbidden"}
	cases := []permCase{
		{"admin", "lobby", ""}, {"bob", "lobby", ""},
		{"root", "lobby", app.TagForbidden}, {"alice", "lobby", app.TagForbidden}, {"carol", "lobby", app.TagForbidden},
		{"alice", "den", ""}, {"bob", "den", ""},
		{"admin", "den", app.TagNotFound}, {"root", "den", app.TagNotFound}, {"carol", "den", app.TagNotFound},
	}
	runPermissions(t, func(e *roomsEnv, a app.Actor, r app.Room) error {
		_, err := e.rooms.Candidates(t.Context(), a, r.ID)
		return err
	}, routes, cases)
}

func TestRoomsCandidates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		actor string
		room  string
		off   []string // users disabled before the call
		want  []string
	}{
		{name: "den without its members", actor: "alice", room: "den", want: []string{"admin", "carol", "dave", "root"}},
		{name: "lobby without its members", actor: "bob", room: "lobby", want: []string{"alice", "carol", "dave", "root"}},
		{name: "disabled users are left out", actor: "alice", room: "den", off: []string{"carol"}, want: []string{"admin", "dave", "root"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newRoomsEnv(t)
			for _, name := range tt.off {
				if err := e.svc.SetDisabled(t.Context(), e.as("admin"), e.users[name].ID, true); err != nil {
					t.Fatal(err)
				}
			}
			users, err := e.rooms.Candidates(t.Context(), e.as(tt.actor), e.room(tt.room).ID)
			if err != nil {
				t.Fatalf("Candidates: %+v", err)
			}
			got := []string{}
			for _, u := range users {
				got = append(got, u.Username)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("candidates = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRoomsAddMemberAudit(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	before := len(e.events())
	if err := e.rooms.AddMember(t.Context(), e.as("bob"), e.den.ID, e.users["carol"].ID); err != nil {
		t.Fatal(err)
	}
	m, err := e.st.RoomMemberOf(t.Context(), e.den.ID, e.users["carol"].ID)
	if err != nil || m.AddedBy == nil || *m.AddedBy != e.users["bob"].ID {
		t.Fatalf("membership = %+v, %v, want one added by bob", m, err)
	}
	e.requireNewEvents(before, wantEvent{
		actor: "bob", action: app.ActionMemberAdded, room: e.den.ID, entityType: app.EntityMember, entityID: m.ID,
		summary: "Bob added Carol to #den", details: map[string]any{"userID": float64(e.users["carol"].ID), "username": "carol"},
	})
	members, err := e.rooms.Members(t.Context(), e.as("carol"), e.den.ID)
	if err != nil || len(members) != 3 {
		t.Fatalf("Members = %v, %v", members, err)
	}
	for _, mm := range members {
		if mm.Username == "carol" && mm.AddedByDisplayName != "Bob" {
			t.Errorf("carol was added by %q, want Bob", mm.AddedByDisplayName)
		}
	}

	// The added user reads the private room now.
	if _, err := e.rooms.Get(t.Context(), e.as("carol"), e.den.ID); err != nil {
		t.Errorf("Get as the added user: %+v", err)
	}

	// Adding a member again, or oneself, is a no-op without an event.
	before = len(e.events())
	for _, target := range []string{"carol", "bob"} {
		if err := e.rooms.AddMember(t.Context(), e.as("bob"), e.den.ID, e.users[target].ID); err != nil {
			t.Fatalf("AddMember %s again: %+v", target, err)
		}
	}
	e.requireNewEvents(before)
	if n := len(e.memberNames(e.den.ID)); n != 3 {
		t.Errorf("%d members, want 3", n)
	}
}

func TestRoomsAddMemberTargets(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		target func(e *roomsEnv) uint64
		route  string
	}{
		{name: "a user that does not exist", target: func(*roomsEnv) uint64 { return 9999 }, route: "Rooms.AddMember.func1.InvalidInput"},
		{name: "no user", target: func(*roomsEnv) uint64 { return 0 }, route: "Rooms.AddMember.func1.InvalidInput"},
		{
			name: "a disabled user",
			target: func(e *roomsEnv) uint64 {
				if err := e.svc.SetDisabled(t.Context(), e.as("admin"), e.users["dave"].ID, true); err != nil {
					t.Fatal(err)
				}
				return e.users["dave"].ID
			},
			route: "Rooms.AddMember.func1.InvalidInput",
		},
	}
	for _, tt := range tests {
		for _, room := range []string{"lobby", "den"} {
			t.Run(tt.name+" in "+room, func(t *testing.T) {
				e := newRoomsEnv(t)
				target := tt.target(e)
				before := len(e.events())
				err := e.rooms.AddMember(t.Context(), e.as("bob"), e.room(room).ID, target)
				appErr := requireTag(t, err, app.TagInvalidInput, tt.route)
				if got := fieldsOf(t, appErr); len(got) != 1 || got[app.FieldUser] == "" {
					t.Errorf("fields = %v, want one for %q", got, app.FieldUser)
				}
				e.requireNewEvents(before)
			})
		}
	}
}

func TestRoomsAddMemberChecksTheActorBeforeTheUser(t *testing.T) {
	t.Parallel()
	// A non-member learns nothing about users, and not that a private room exists.
	e := newRoomsEnv(t)
	err := e.rooms.AddMember(t.Context(), e.as("carol"), e.den.ID, 9999)
	_ = requireTag(t, err, app.TagNotFound, notFoundPrivate)
	err = e.rooms.AddMember(t.Context(), e.as("carol"), e.lobby.ID, 9999)
	_ = requireTag(t, err, app.TagForbidden, "Rooms.AddMember")
}

func TestRoomsRemoveMember(t *testing.T) {
	t.Parallel()
	routes := map[string]string{app.TagNotFound: notFoundPrivate, app.TagForbidden: "Rooms.RemoveMember.func1.Forbidden"}
	cases := []permCase{
		{"admin", "lobby", ""}, {"root", "lobby", ""},
		{"alice", "lobby", app.TagForbidden}, {"bob", "lobby", app.TagForbidden}, {"carol", "lobby", app.TagForbidden},
		// Admins remove from private rooms they cannot read (D29).
		{"admin", "den", ""}, {"root", "den", ""},
		{"alice", "den", app.TagForbidden}, {"bob", "den", app.TagForbidden},
		{"carol", "den", app.TagNotFound},
	}
	runPermissions(t, func(e *roomsEnv, a app.Actor, r app.Room) error {
		return e.rooms.RemoveMember(t.Context(), a, r.ID, e.users["bob"].ID)
	}, routes, cases)
}

func TestRoomsRemoveMemberAudit(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	id := e.membershipID(e.den.ID, "bob")
	before := len(e.events())
	if err := e.rooms.RemoveMember(t.Context(), e.as("admin"), e.den.ID, e.users["bob"].ID); err != nil {
		t.Fatal(err)
	}
	if got := e.memberNames(e.den.ID); !reflect.DeepEqual(got, []string{"alice"}) {
		t.Errorf("members = %v, want only alice", got)
	}
	e.requireNewEvents(before, wantEvent{
		actor: "admin", action: app.ActionMemberRemoved, room: e.den.ID, entityType: app.EntityMember, entityID: id,
		summary: "Admin removed Bob from #den", details: map[string]any{"userID": float64(e.users["bob"].ID), "username": "bob"},
	})
	// Bob no longer reads the private room.
	_, err := e.rooms.Get(t.Context(), e.as("bob"), e.den.ID)
	_ = requireTag(t, err, app.TagNotFound, notFoundPrivate)

	// Not a member: a no-op, no event. Same for a user that does not exist.
	before = len(e.events())
	for _, target := range []uint64{e.users["bob"].ID, e.users["carol"].ID, 9999} {
		if err := e.rooms.RemoveMember(t.Context(), e.as("root"), e.den.ID, target); err != nil {
			t.Fatalf("RemoveMember %d: %+v", target, err)
		}
	}
	e.requireNewEvents(before)

	// An admin may remove themselves and the creator.
	if err := e.rooms.RemoveMember(t.Context(), e.as("root"), e.den.ID, e.users["alice"].ID); err != nil {
		t.Fatal(err)
	}
	if got := e.memberNames(e.den.ID); len(got) != 0 {
		t.Errorf("members = %v, want none", got)
	}
}

func TestRoomsUpdate(t *testing.T) {
	t.Parallel()
	routes := map[string]string{app.TagNotFound: notFoundPrivate, app.TagForbidden: "Rooms.Update.func1->loadManageableRoom.Forbidden"}
	cases := []permCase{
		{"admin", "lobby", ""}, {"root", "lobby", ""},
		{"alice", "lobby", app.TagForbidden}, {"bob", "lobby", app.TagForbidden}, {"carol", "lobby", app.TagForbidden},
		{"admin", "den", ""}, {"root", "den", ""}, {"alice", "den", ""},
		{"bob", "den", app.TagForbidden}, {"carol", "den", app.TagNotFound},
	}
	runPermissions(t, func(e *roomsEnv, a app.Actor, r app.Room) error {
		return e.rooms.Update(t.Context(), a, r.ID, app.RoomInput{Name: "renamed"})
	}, routes, cases)
}

func TestRoomsUpdateByTheCreatorOfAVisibleRoom(t *testing.T) {
	t.Parallel()
	// An admin made the room, then lost the role: still its creator.
	e := newRoomsEnv(t)
	if err := e.svc.SetRole(t.Context(), e.as("root"), e.users["admin"].ID, store.RoleMember); err != nil {
		t.Fatal(err)
	}
	demoted := app.Actor{UserID: e.users["admin"].ID, Username: "admin", Role: store.RoleMember}
	if err := e.rooms.Update(t.Context(), demoted, e.lobby.ID, app.RoomInput{Name: "lounge"}); err != nil {
		t.Fatalf("Update by the creator: %+v", err)
	}
	if err := e.rooms.Delete(t.Context(), demoted, e.lobby.ID); err != nil {
		t.Fatalf("Delete by the creator: %+v", err)
	}
}

func TestRoomsUpdateAndDeleteNeedTheCreatorToStayInAPrivateRoom(t *testing.T) {
	t.Parallel()
	calls := map[string]func(e *roomsEnv, a app.Actor) error{
		"Update": func(e *roomsEnv, a app.Actor) error {
			return e.rooms.Update(t.Context(), a, e.den.ID, app.RoomInput{Name: "gone"})
		},
		"Delete": func(e *roomsEnv, a app.Actor) error { return e.rooms.Delete(t.Context(), a, e.den.ID) },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			e := newRoomsEnv(t)
			if err := e.rooms.Leave(t.Context(), e.as("alice"), e.den.ID); err != nil {
				t.Fatal(err)
			}
			before := len(e.events())
			// The creator left: the room is unknown to them again.
			_ = requireTag(t, call(e, e.as("alice")), app.TagNotFound, notFoundPrivate)
			e.requireNewEvents(before)
			// Admins still manage it.
			if err := call(e, e.as("root")); err != nil {
				t.Fatalf("%s by an admin: %+v", name, err)
			}
		})
	}
}

func TestRoomsUpdateAudit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		in          app.RoomInput
		wantName    string
		wantDesc    string
		wantSummary string
		wantDetails map[string]any // nil: no event
	}{
		{
			name: "rename", in: app.RoomInput{Name: "Product", Description: "Pixels"},
			wantName: "Product", wantDesc: "Pixels",
			wantSummary: "Alice renamed #design to #Product",
			wantDetails: map[string]any{"name": map[string]any{"old": "design", "new": "Product"}},
		},
		{
			name: "description only", in: app.RoomInput{Name: "design", Description: "Colors"},
			wantName: "design", wantDesc: "Colors",
			wantSummary: "Alice changed the description of #design",
			wantDetails: map[string]any{"description": map[string]any{"old": "Pixels", "new": "Colors"}},
		},
		{
			name: "both", in: app.RoomInput{Name: "product", Description: "Colors"},
			wantName: "product", wantDesc: "Colors",
			wantSummary: "Alice renamed #design to #product and changed its description",
			wantDetails: map[string]any{
				"name":        map[string]any{"old": "design", "new": "product"},
				"description": map[string]any{"old": "Pixels", "new": "Colors"},
			},
		},
		{
			name: "only the case of the name", in: app.RoomInput{Name: "Design", Description: "Pixels"},
			wantName: "Design", wantDesc: "Pixels",
			wantSummary: "Alice renamed #design to #Design",
			wantDetails: map[string]any{"name": map[string]any{"old": "design", "new": "Design"}},
		},
		{
			name: "description cleared", in: app.RoomInput{Name: "design"},
			wantName: "design", wantDesc: "",
			wantSummary: "Alice changed the description of #design",
			wantDetails: map[string]any{"description": map[string]any{"old": "Pixels", "new": ""}},
		},
		{
			name: "values trimmed", in: app.RoomInput{Name: "  Product ", Description: " Pixels\n"},
			wantName: "Product", wantDesc: "Pixels",
			wantSummary: "Alice renamed #design to #Product",
			wantDetails: map[string]any{"name": map[string]any{"old": "design", "new": "Product"}},
		},
		{name: "nothing changed", in: app.RoomInput{Name: "design", Description: "Pixels"}, wantName: "design", wantDesc: "Pixels"},
		{name: "nothing changed after trimming", in: app.RoomInput{Name: " design ", Description: " Pixels "}, wantName: "design", wantDesc: "Pixels"},
		{
			name: "the visibility is ignored", in: app.RoomInput{Name: "design", Description: "Pixels", Visibility: app.VisibilityVisible},
			wantName: "design", wantDesc: "Pixels",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newRoomsEnv(t)
			room, err := e.rooms.Create(t.Context(), e.as("alice"), app.RoomInput{Name: "design", Description: "Pixels", Visibility: app.VisibilityPrivate})
			if err != nil {
				t.Fatal(err)
			}
			before := len(e.events())
			if err := e.rooms.Update(t.Context(), e.as("alice"), room.ID, tt.in); err != nil {
				t.Fatalf("Update: %+v", err)
			}
			got, err := e.rooms.Get(t.Context(), e.as("alice"), room.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != tt.wantName || got.Description != tt.wantDesc || got.Visibility != app.VisibilityPrivate {
				t.Errorf("room = %+v, want name %q, description %q, still private", got, tt.wantName, tt.wantDesc)
			}
			if tt.wantDetails == nil {
				e.requireNewEvents(before)
				return
			}
			e.requireNewEvents(before, wantEvent{
				actor: "alice", action: app.ActionRoomRenamed, room: room.ID, entityType: app.EntityRoom, entityID: room.ID,
				summary: tt.wantSummary, details: tt.wantDetails,
			})
		})
	}
}

func TestRoomsUpdateByAnAdmin(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	before := len(e.events())
	if err := e.rooms.Update(t.Context(), e.as("root"), e.den.ID, app.RoomInput{Name: "burrow", Description: "Cozy"}); err != nil {
		t.Fatal(err)
	}
	e.requireNewEvents(before, wantEvent{
		actor: "root", action: app.ActionRoomRenamed, room: e.den.ID, entityType: app.EntityRoom, entityID: e.den.ID,
		summary: "Root renamed #den to #burrow and changed its description",
		details: map[string]any{
			"name":        map[string]any{"old": "den", "new": "burrow"},
			"description": map[string]any{"old": "", "new": "Cozy"},
		},
	})
}

func TestRoomsUpdateValidation(t *testing.T) {
	t.Parallel()
	long := func(r string, n int) string { return strings.Repeat(r, n) }
	nameErr := map[string]string{app.FieldName: "Use 1 to 64 characters."}
	takenErr := map[string]string{app.FieldName: "This name is taken."}
	tests := []struct {
		name       string
		in         app.RoomInput
		wantFields map[string]string // nil: must succeed
	}{
		{name: "64 characters", in: app.RoomInput{Name: long("é", 64)}},
		{name: "65 characters", in: app.RoomInput{Name: long("é", 65)}, wantFields: nameErr},
		{name: "empty", in: app.RoomInput{Name: ""}, wantFields: nameErr},
		{name: "only spaces", in: app.RoomInput{Name: "  "}, wantFields: nameErr},
		{name: "description 500", in: app.RoomInput{Name: "den", Description: long("é", 500)}},
		{name: "description 501", in: app.RoomInput{Name: "den", Description: long("é", 501)}, wantFields: map[string]string{app.FieldDescription: "Use at most 500 characters."}},
		{name: "both wrong", in: app.RoomInput{Name: "", Description: long("x", 501)}, wantFields: map[string]string{
			app.FieldName: "Use 1 to 64 characters.", app.FieldDescription: "Use at most 500 characters.",
		}},
		{name: "another room's name", in: app.RoomInput{Name: "lobby"}, wantFields: takenErr},
		{name: "another room's name in capitals", in: app.RoomInput{Name: "LOBBY"}, wantFields: takenErr},
		{name: "own name is not taken", in: app.RoomInput{Name: "den", Description: "x"}},
		{name: "own name in capitals is not taken", in: app.RoomInput{Name: "DEN"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newRoomsEnv(t)
			before := len(e.events())
			err := e.rooms.Update(t.Context(), e.as("alice"), e.den.ID, tt.in)
			if tt.wantFields == nil {
				if err != nil {
					t.Fatalf("Update: %+v", err)
				}
				return
			}
			appErr := requireTag(t, err, app.TagInvalidInput, "Rooms.Update")
			if got := fieldsOf(t, appErr); !reflect.DeepEqual(got, tt.wantFields) {
				t.Errorf("fields = %v, want %v", got, tt.wantFields)
			}
			e.requireNewEvents(before)
			got, err := e.rooms.Get(t.Context(), e.as("alice"), e.den.ID)
			if err != nil || got.Name != "den" {
				t.Errorf("room after the failed Update = %+v, %v", got, err)
			}
		})
	}
}

func TestRoomsUpdateChecksThePermissionBeforeTheInput(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	bad := app.RoomInput{Name: ""}
	_ = requireTag(t, e.rooms.Update(t.Context(), e.as("carol"), e.den.ID, bad), app.TagNotFound, notFoundPrivate)
	_ = requireTag(t, e.rooms.Update(t.Context(), e.as("bob"), e.den.ID, bad), app.TagForbidden, "Rooms.Update")
}

func TestRoomsDelete(t *testing.T) {
	t.Parallel()
	routes := map[string]string{app.TagNotFound: notFoundPrivate, app.TagForbidden: "Rooms.Delete.func1->loadManageableRoom.Forbidden"}
	cases := []permCase{
		{"admin", "lobby", ""}, {"root", "lobby", ""},
		{"alice", "lobby", app.TagForbidden}, {"bob", "lobby", app.TagForbidden}, {"carol", "lobby", app.TagForbidden},
		{"admin", "den", ""}, {"root", "den", ""}, {"alice", "den", ""},
		{"bob", "den", app.TagForbidden}, {"carol", "den", app.TagNotFound},
	}
	runPermissions(t, func(e *roomsEnv, a app.Actor, r app.Room) error {
		return e.rooms.Delete(t.Context(), a, r.ID)
	}, routes, cases)
}

func TestRoomsDeleteCascadesButTheAuditStays(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	before := len(e.events())
	if err := e.rooms.Delete(t.Context(), e.as("root"), e.den.ID); err != nil {
		t.Fatalf("Delete: %+v", err)
	}
	// The room, its members and its columns are gone.
	if _, err := e.st.RoomByID(t.Context(), e.den.ID); err == nil {
		t.Error("the room is still there")
	}
	if cols, err := e.st.ListBoardColumns(t.Context(), e.den.ID); err != nil || len(cols) != 0 {
		t.Errorf("columns = %v, %v, want none", cols, err)
	}
	if got := e.memberNames(e.den.ID); len(got) != 0 {
		t.Errorf("members = %v, want none", got)
	}
	_, err := e.rooms.Get(t.Context(), e.as("alice"), e.den.ID)
	_ = requireTag(t, err, app.TagNotFound, notFoundMissing)
	if mine, err := e.rooms.Mine(t.Context(), e.as("alice")); err != nil || len(mine) != 0 {
		t.Errorf("Mine = %v, %v, want none", mine, err)
	}
	// The other room is untouched.
	if cols, err := e.st.ListBoardColumns(t.Context(), e.lobby.ID); err != nil || len(cols) != 3 {
		t.Errorf("lobby columns = %v, %v, want 3", cols, err)
	}

	// The events of the room stay: the ones from before the delete and the
	// deletion itself, the last one.
	wantDeleted := wantEvent{
		actor: "root", action: app.ActionRoomDeleted, room: e.den.ID, entityType: app.EntityRoom, entityID: e.den.ID,
		summary: "Root deleted #den",
		details: map[string]any{
			"name":        map[string]any{"old": "den", "new": nil},
			"description": map[string]any{"old": "", "new": nil},
			"visibility":  map[string]any{"old": "private", "new": nil},
		},
	}
	e.requireNewEvents(before, wantDeleted)
	rows, err := e.st.ListAuditEvents(t.Context(), store.AuditFilter{RoomID: e.den.ID}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	var actions []app.AuditAction
	for _, ev := range rows {
		actions = append(actions, ev.Action)
	}
	// Newest first: deleted, member.added (bob), room.created.
	if want := []app.AuditAction{app.ActionRoomDeleted, app.ActionMemberAdded, app.ActionRoomCreated}; !reflect.DeepEqual(actions, want) {
		t.Errorf("events of the deleted room = %v, want %v", actions, want)
	}
}

func TestRoomsEveryEventHasTheRoomAndTheActor(t *testing.T) {
	t.Parallel()
	// One walk through every action, to see that nothing else is written.
	e := newRoomsEnv(t)
	ctx := t.Context()
	before := len(e.events())
	steps := []func() error{
		func() error { return e.rooms.Join(ctx, e.as("carol"), e.lobby.ID) },
		func() error { return e.rooms.AddMember(ctx, e.as("carol"), e.lobby.ID, e.users["dave"].ID) },
		func() error { return e.rooms.RemoveMember(ctx, e.as("admin"), e.lobby.ID, e.users["dave"].ID) },
		func() error { return e.rooms.Leave(ctx, e.as("carol"), e.lobby.ID) },
		func() error {
			return e.rooms.Update(ctx, e.as("admin"), e.lobby.ID, app.RoomInput{Name: "hall", Description: "Everyone"})
		},
		func() error { return e.rooms.Delete(ctx, e.as("admin"), e.lobby.ID) },
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("step %d: %+v", i, err)
		}
	}
	var got []string
	for _, ev := range e.events()[before:] {
		got = append(got, string(ev.Action)+": "+ev.Summary)
		if ev.RoomID != e.lobby.ID {
			t.Errorf("%s: RoomID = %d, want %d", ev.Action, ev.RoomID, e.lobby.ID)
		}
	}
	want := []string{
		"member.joined: Carol joined #lobby",
		"member.added: Carol added Dave to #lobby",
		"member.removed: Admin removed Dave from #lobby",
		"member.left: Carol left #lobby",
		"room.renamed: Admin renamed #lobby to #hall",
		"room.deleted: Admin deleted #hall",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestRoomsActorWithoutAUser(t *testing.T) {
	t.Parallel()
	// An actor whose user does not exist cannot write an event: the change
	// rolls back with the store's TagNotFound.
	e := newRoomsEnv(t)
	ghost := app.Actor{UserID: 9999, Username: "ghost", Role: store.RoleAdmin}
	before := len(e.events())
	_, err := e.rooms.Create(t.Context(), ghost, app.RoomInput{Name: "boo", Visibility: app.VisibilityVisible})
	_ = requireTag(t, err, app.TagNotFound, "Rooms.Create")
	if err := e.rooms.Delete(t.Context(), ghost, e.lobby.ID); err == nil {
		t.Error("Delete by a ghost succeeded")
	}
	e.requireNewEvents(before)
	if rooms, err := e.rooms.AllRooms(t.Context(), e.as("admin")); err != nil || len(rooms) != 2 {
		t.Errorf("%d rooms after the failures (%v), want 2", len(rooms), err)
	}
}

// tagTxFailed is the store's tag of a transaction that cannot begin or commit.
const tagTxFailed = "TransactionFailed"

// canceledContext returns a context that is done already.
func canceledContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return ctx
}

func TestRoomsDatabaseFailuresPassThrough(t *testing.T) {
	t.Parallel()
	// A canceled context fails the first query. The store's error reaches the
	// caller as it is, with its tag and its route from the method down.
	e := newRoomsEnv(t)
	ctx := canceledContext(t)
	a := e.as("admin")
	in := app.RoomInput{Name: "x", Visibility: app.VisibilityPrivate}
	tests := []struct {
		name      string
		call      func() error
		wantTag   string
		wantRoute string
	}{
		{"Create", func() error { _, err := e.rooms.Create(ctx, a, in); return err }, tagTxFailed, "Rooms.Create->Store.Tx.TransactionFailed"},
		{"Visible", func() error { _, err := e.rooms.Visible(ctx, a); return err }, store.TagQueryFailed, "Rooms.Visible->Store.ListVisibleRooms.QueryFailed"},
		{"Mine", func() error { _, err := e.rooms.Mine(ctx, a); return err }, store.TagQueryFailed, "Rooms.Mine->Store.ListRoomsOfUser.QueryFailed"},
		{"Get", func() error { _, err := e.rooms.Get(ctx, a, e.lobby.ID); return err }, store.TagQueryFailed, "Rooms.Get->loadReadableRoom->Store.RoomByID.QueryFailed"},
		{"Members", func() error { _, err := e.rooms.Members(ctx, a, e.lobby.ID); return err }, store.TagQueryFailed, "Rooms.Members->loadReadableRoom->Store.RoomByID.QueryFailed"},
		{"Candidates", func() error { _, err := e.rooms.Candidates(ctx, a, e.lobby.ID); return err }, store.TagQueryFailed, "Rooms.Candidates->loadReadableRoom->Store.RoomByID.QueryFailed"},
		{"Join", func() error { return e.rooms.Join(ctx, a, e.lobby.ID) }, tagTxFailed, "Rooms.Join->Store.Tx.TransactionFailed"},
		{"Leave", func() error { return e.rooms.Leave(ctx, a, e.lobby.ID) }, tagTxFailed, "Rooms.Leave->Store.Tx.TransactionFailed"},
		{"AddMember", func() error { return e.rooms.AddMember(ctx, a, e.lobby.ID, 2) }, tagTxFailed, "Rooms.AddMember->Store.Tx.TransactionFailed"},
		{"RemoveMember", func() error { return e.rooms.RemoveMember(ctx, a, e.lobby.ID, 2) }, tagTxFailed, "Rooms.RemoveMember->Store.Tx.TransactionFailed"},
		{"Update", func() error { return e.rooms.Update(ctx, a, e.lobby.ID, in) }, tagTxFailed, "Rooms.Update->Store.Tx.TransactionFailed"},
		{"Delete", func() error { return e.rooms.Delete(ctx, a, e.lobby.ID) }, tagTxFailed, "Rooms.Delete->Store.Tx.TransactionFailed"},
		{"AllRooms", func() error { _, err := e.rooms.AllRooms(ctx, a); return err }, store.TagQueryFailed, "Rooms.AllRooms->Store.ListRooms.QueryFailed"},
		{"AdminMembers", func() error { _, err := e.rooms.AdminMembers(ctx, a, e.lobby.ID); return err }, store.TagQueryFailed, "Rooms.AdminMembers->Store.RoomByID.QueryFailed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = requireTag(t, tt.call(), tt.wantTag, tt.wantRoute)
		})
	}
}
