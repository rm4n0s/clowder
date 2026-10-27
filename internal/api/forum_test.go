package api

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/store"
)

// forum is a testEnv with these users and rooms, the way the tests of the
// services have them:
//
//	admin  admin, made lobby, a member of lobby only
//	root   admin, a member of nothing
//	alice  member, made den, a member of den only
//	bob    member, a member of lobby and den (added to den by alice)
//	carol  member, a member of nothing
//	lobby  visible, members admin and bob
//	den    private, members alice and bob
type forum struct {
	*testEnv
	tokens map[string]string
	lobby  app.Room
	den    app.Room
}

// newForum makes a forum on a fresh database.
func newForum(t *testing.T) *forum {
	t.Helper()
	f := &forum{testEnv: newTestEnv(t, nil), tokens: map[string]string{}}
	for _, u := range []struct {
		name string
		role store.Role
	}{
		{"admin", store.RoleAdmin}, {"root", store.RoleAdmin},
		{"alice", store.RoleMember}, {"bob", store.RoleMember}, {"carol", store.RoleMember},
	} {
		f.addUser(u.name, u.role)
	}
	var err error
	if f.lobby, err = f.rooms.Create(t.Context(), actorOf(f.testEnv, "admin"), app.RoomInput{Name: "lobby", Description: "Everyone", Visibility: app.VisibilityVisible}); err != nil {
		t.Fatalf("Create lobby: %+v", err)
	}
	if err := f.rooms.Join(t.Context(), actorOf(f.testEnv, "bob"), f.lobby.ID); err != nil {
		t.Fatalf("Join lobby: %+v", err)
	}
	if f.den, err = f.rooms.Create(t.Context(), actorOf(f.testEnv, "alice"), app.RoomInput{Name: "den", Visibility: app.VisibilityPrivate}); err != nil {
		t.Fatalf("Create den: %+v", err)
	}
	if err := f.rooms.AddMember(t.Context(), actorOf(f.testEnv, "alice"), f.den.ID, f.userID("bob")); err != nil {
		t.Fatalf("AddMember den: %+v", err)
	}
	return f
}

// room returns the room of the fixture by name.
func (f *forum) room(name string) app.Room {
	f.t.Helper()
	switch name {
	case "lobby":
		return f.lobby
	case "den":
		return f.den
	}
	f.t.Fatalf("no room %q", name)
	return app.Room{}
}

// as returns the bearer token of the user, logged in once.
func (f *forum) as(username string) string {
	f.t.Helper()
	if tok, ok := f.tokens[username]; ok {
		return tok
	}
	tok := f.token(username)
	f.tokens[username] = tok
	return tok
}

// call sends a request as the user through the checked path and returns the
// answer.
func (f *forum) call(username, method, path string, body any, opts ...reqOpt) *httptest.ResponseRecorder {
	f.t.Helper()
	opts = append([]reqOpt{bearer(f.as(username))}, opts...)
	return f.send(request(method, path, body, opts...))
}

// ok sends the request and fails the test unless the status is want.
func (f *forum) ok(username, method, path string, body any, want int) map[string]any {
	f.t.Helper()
	rec := f.call(username, method, path, body)
	requireStatus(f.t, rec, want)
	if rec.Body.Len() == 0 {
		return nil
	}
	return decode(f.t, rec)
}

// write makes a text post of the user in the room with the service.
func (f *forum) write(username, room, body string, tags ...string) app.Post {
	f.t.Helper()
	p, err := f.posts.Create(f.t.Context(), actorOf(f.testEnv, username), f.room(room).ID,
		app.PostInput{Format: app.FormatText, Body: body, Tags: tags})
	if err != nil {
		f.t.Fatalf("Create %q: %+v", body, err)
	}
	return p
}

// say makes a comment of the user under the post with the service.
func (f *forum) say(username string, parent uint64, body string) app.Post {
	f.t.Helper()
	p, err := f.posts.Reply(f.t.Context(), actorOf(f.testEnv, username), parent, app.PostInput{Format: app.FormatText, Body: body})
	if err != nil {
		f.t.Fatalf("Reply %q: %+v", body, err)
	}
	return p
}

// audit lists the audit events written so far, oldest first, as
// "action entity-type".
func (f *forum) audit() []string {
	f.t.Helper()
	rows, err := f.st.ListAuditEvents(f.t.Context(), store.AuditFilter{}, 1000, 0)
	if err != nil {
		f.t.Fatalf("ListAuditEvents: %+v", err)
	}
	out := make([]string, len(rows))
	for i, ev := range rows {
		out[len(rows)-1-i] = string(ev.Action) + " " + string(ev.EntityType)
	}
	return out
}

// requireAudit fails the test unless the audit events written after the first
// before ones are exactly want ("action entity-type").
func (f *forum) requireAudit(before int, want ...string) {
	f.t.Helper()
	all := f.audit()
	if len(all) < before {
		f.t.Fatalf("%d audit events, fewer than the %d before", len(all), before)
	}
	got := all[before:]
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		f.t.Errorf("new audit events = %v, want %v", got, want)
	}
}

// path joins the parts of a path with their IDs: path("/rooms/%d/posts", id).
func path(format string, ids ...uint64) string {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return "/api/v1" + fmt.Sprintf(format, args...)
}

// obj reads a JSON object out of a decoded value.
func obj(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%v (%T) is not a JSON object", v, v)
	}
	return m
}

// list reads a JSON array out of a decoded value.
func list(t *testing.T, v any) []any {
	t.Helper()
	l, ok := v.([]any)
	if !ok {
		t.Fatalf("%v (%T) is not a JSON array", v, v)
	}
	return l
}

// idOf reads the "id" of a decoded object as a number.
func idOf(t *testing.T, v any) uint64 {
	t.Helper()
	n, ok := obj(t, v)["id"].(float64)
	if !ok {
		t.Fatalf("%v has no numeric id", v)
	}
	return uint64(n)
}
