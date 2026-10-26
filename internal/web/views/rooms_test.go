package views_test

import (
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
)

func TestSidebarRooms(t *testing.T) {
	user := app.User{DisplayName: "Alice", Role: app.RoleMember}
	rooms := []app.Room{
		{ID: 1, Name: "general", Visibility: app.VisibilityVisible},
		{ID: 2, Name: "secret", Visibility: app.VisibilityPrivate},
		{ID: 3, Name: "<b>x</b>", Visibility: app.VisibilityVisible},
	}
	tests := []struct {
		name    string
		data    views.SidebarData
		want    []string
		notWant []string
	}{
		{
			name: "rooms with a lock for the private one and the current one marked",
			data: views.SidebarData{User: user, Rooms: rooms, CurrentRoomID: 2},
			want: []string{
				`<h2 class="sidebar-heading">Rooms</h2>`,
				`href="/rooms/1"`, `data-room="general"`,
				`href="/rooms/2" class="current" data-room="secret" aria-current="page"`,
				`<svg class="lock" role="img" aria-label="Private"`,
				`&lt;b&gt;x&lt;/b&gt;`,
				`<a href="/rooms">Browse rooms</a>`, `<a href="/rooms/new">New room</a>`,
			},
			notWant: []string{"<b>x</b>"},
		},
		{
			name:    "no current room",
			data:    views.SidebarData{User: user, Rooms: rooms[:1]},
			want:    []string{`data-room="general"`, `Browse rooms`},
			notWant: []string{`aria-current`, `class="current"`, "svg"},
		},
		{
			name: "no rooms still has the links",
			data: views.SidebarData{User: user},
			want: []string{`<a href="/rooms">Browse rooms</a>`, `<a href="/rooms/new">New room</a>`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireContains(t, render(t, views.Sidebar(tt.data)), tt.want, tt.notWant)
		})
	}
}

func requireContains(t *testing.T, html string, want, notWant []string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(html, w) {
			t.Errorf("does not contain %q:\n%s", w, html)
		}
	}
	for _, w := range notWant {
		if strings.Contains(html, w) {
			t.Errorf("contains %q:\n%s", w, html)
		}
	}
}

func TestRoomViews(t *testing.T) {
	admin := app.User{ID: 1, DisplayName: "Admin", Username: "admin", Role: app.RoleAdmin}
	alice := app.User{ID: 2, DisplayName: "Alice", Username: "alice", Role: app.RoleMember}
	adder := uint64(2)
	private := app.Room{ID: 7, Name: "secret", Description: "Just us", Visibility: app.VisibilityPrivate, MemberCount: 2, IsMember: true, CanManage: true}
	members := []app.Member{
		{UserID: 2, Username: "alice", DisplayName: "Alice"},
		{UserID: 3, Username: "bob", DisplayName: "Bob", AddedBy: &adder, AddedByDisplayName: "Alice"},
	}
	candidates := []app.User{{ID: 4, Username: "carol", DisplayName: "Carol"}}
	tests := []struct {
		name    string
		c       templ.Component
		want    []string
		notWant []string
	}{
		{
			name: "room page of a member who manages a private room",
			c: views.RoomPage(views.RoomData{
				Sidebar: views.SidebarData{User: alice, CurrentRoomID: 7}, Viewer: alice,
				Room: private, Members: members, Candidates: candidates,
			}),
			want: []string{
				"<title>secret · Clowder</title>", `id="room-header"`, "<h1>secret</h1>", "badge-private", "Private",
				"Just us", "2 members", "No posts yet",
				`id="leave-form"`, "confirm(&#39;Leave this room?&#39;) &amp;&amp; @post(&#39;/rooms/7/leave&#39;",
				`id="delete-form"`, "/rooms/7/delete",
				`id="room-edit"`, `action="/rooms/7/rename"`, `value="secret"`,
				`id="members-panel"`, `data-username="alice"`, `data-username="bob"`, "added by Alice", "joined", "@bob",
				`id="add-member-form"`, `action="/rooms/7/members"`, `<option value="4">Carol (@carol)</option>`,
			},
			notWant: []string{"/join", "/remove", "form-error", "<details id=\"room-edit\" class=\"room-edit\" open"},
		},
		{
			name: "admin sees the remove buttons",
			c:    views.MembersPanel(views.RoomData{Viewer: admin, Room: private, Members: members}),
			want: []string{`action="/rooms/7/members/2/remove"`, `action="/rooms/7/members/3/remove"`, "Remove</button>", "Everyone is a member already."},
		},
		{
			name:    "non-member of a visible room joins",
			c:       views.RoomHeader(views.RoomData{Viewer: alice, Room: app.Room{ID: 9, Name: "general", Visibility: app.VisibilityVisible, MemberCount: 1}}),
			want:    []string{"badge-visible", "1 member", `action="/rooms/9/join"`, `name="from" value="room"`},
			notWant: []string{"/leave", "/delete", "room-edit", "lock"},
		},
		{
			name:    "members panel of a non-member has no add form",
			c:       views.MembersPanel(views.RoomData{Viewer: alice, Room: app.Room{ID: 9}, Members: members}),
			want:    []string{`id="members-panel"`},
			notWant: []string{"add-member-form", "/remove"},
		},
		{
			name: "members message",
			c:    views.MembersPanel(views.RoomData{Viewer: alice, Room: private, Members: members, MembersMessage: "Choose a user."}),
			want: []string{`<p class="form-error" role="alert">Choose a user.</p>`},
		},
		{
			name: "edit form after a failed submit is open and keeps what was typed",
			c: views.RoomEditForm(views.RoomData{Room: private, Edit: views.RoomEditData{
				Name: "", Description: "x", Open: true,
				Errors: views.FormErrors{Fields: map[string]string{"name": "Use 1 to 64 characters."}},
			}}),
			want: []string{`<details id="room-edit" class="room-edit" open>`, `id="edit-name-error"`, "Use 1 to 64 characters.", ">x</textarea>"},
		},
		{
			name:    "new room form of a member makes private rooms",
			c:       views.RoomNewForm(views.RoomNewData{Name: "n"}),
			want:    []string{`id="room-new-form"`, `action="/rooms"`, `name="visibility" value="private"`, "Rooms you make are private", `value="n"`},
			notWant: []string{"<select"},
		},
		{
			name:    "new room form of an admin chooses the visibility",
			c:       views.RoomNewForm(views.RoomNewData{Admin: true, Visibility: "visible"}),
			want:    []string{`<select id="room-visibility" name="visibility">`, `<option value="visible" selected>`},
			notWant: []string{`type="hidden"`, "Rooms you make are private"},
		},
		{
			name: "browse page",
			c: views.RoomsBrowsePage(views.RoomsBrowseData{
				Sidebar: views.SidebarData{User: alice},
				Rooms: []app.Room{
					{ID: 1, Name: "general", Description: "Everything", MemberCount: 3, IsMember: true},
					{ID: 2, Name: "random", MemberCount: 1},
				},
			}),
			want: []string{
				"<h1>Browse rooms</h1>", `id="rooms-panel"`, `href="/rooms/1"`, "Everything", "3 members",
				`<span class="joined">Joined</span>`, ">Open</a>",
				"1 member", `action="/rooms/2/join"`, `name="from" value="browse"`,
			},
			notWant: []string{`action="/rooms/1/join"`},
		},
		{
			name: "browse page without rooms",
			c:    views.RoomsPanel(views.RoomsBrowseData{Message: "Page not found"}),
			want: []string{"There are no visible rooms yet.", `class="form-error"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireContains(t, render(t, tt.c), tt.want, tt.notWant)
		})
	}
}

func TestFormPostConfirm(t *testing.T) {
	got := views.FormPostConfirm("/rooms/1/delete", "Sure? it's final")
	want := `confirm('Sure? it%27s final') && @post('/rooms/1/delete', {contentType: 'form'})`
	if got != want {
		t.Errorf("FormPostConfirm = %q, want %q", got, want)
	}
	if got := views.RoomPath(12); got != "/rooms/12" {
		t.Errorf("RoomPath = %q", got)
	}
}
