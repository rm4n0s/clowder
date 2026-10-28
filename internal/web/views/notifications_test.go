package views_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
)

// renderHTML renders a component to a string.
func renderHTML(t *testing.T, c templ.Component) string {
	t.Helper()
	var buf bytes.Buffer
	if err := c.Render(context.Background(), &buf); err != nil {
		t.Fatalf("render: %v", err)
	}
	return buf.String()
}

func TestNotificationCount(t *testing.T) {
	tests := []struct {
		name    string
		n       int
		want    []string
		notWant []string
	}{
		{"none is an empty element", 0, []string{`<span id="notification-count" class="count-badge"></span>`}, []string{"title="}},
		{"one", 1, []string{`<span id="notification-count" class="count-badge" title="1 unread notification">1</span>`}, nil},
		{"several", 12, []string{`title="12 unread notifications">12</span>`}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			html := renderHTML(t, views.NotificationCount(tt.n))
			for _, w := range tt.want {
				if !strings.Contains(html, w) {
					t.Errorf("html does not contain %q:\n%s", w, html)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(html, w) {
					t.Errorf("html contains %q:\n%s", w, html)
				}
			}
		})
	}
}

func TestHeaderHasTheBell(t *testing.T) {
	user := app.User{DisplayName: "Bob", Username: "bob", Role: app.RoleMember}
	tests := []struct {
		name   string
		unread int
		want   string
	}{
		{"with a count", 4, `<span class="visually-hidden">Notifications</span><span id="notification-count" class="count-badge" title="4 unread notifications">4</span></a>`},
		{"without", 0, `<span class="visually-hidden">Notifications</span><span id="notification-count" class="count-badge"></span></a>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			html := renderHTML(t, views.Header(views.SidebarData{User: user, Unread: tt.unread}))
			for _, want := range []string{`<a class="header-link header-notifications" href="/notifications">`, tt.want} {
				if !strings.Contains(html, want) {
					t.Errorf("html does not contain %q:\n%s", want, html)
				}
			}
		})
	}
}

func TestNotificationItem(t *testing.T) {
	at := time.Date(2026, 10, 27, 9, 30, 0, 0, time.UTC)
	base := app.Notification{
		ID: 7, Actor: app.UserRef{ID: 2, Username: "alice", DisplayName: "Alice"},
		RoomID: 3, RoomName: "general", PostID: 11, RootID: 10, CreatedAt: at,
	}
	with := func(f func(n *app.Notification)) app.Notification {
		n := base
		f(&n)
		return n
	}
	actor := `<span class="notification-actor">Alice</span> <span class="notification-username">@alice</span> `
	room := ` <a class="notification-room" href="/rooms/3">general</a>`
	tests := []struct {
		name    string
		n       app.Notification
		want    []string
		notWant []string
	}{
		{
			name: "mention, unread", n: with(func(n *app.Notification) { n.Kind = app.NotificationMention }),
			want: []string{
				`<li id="notification-7" class="notification unread" data-kind="mention" data-class:unread="$readUpTo &lt; 7">`,
				actor + "mentioned you in" + room,
				`<a class="notification-link" href="/posts/10#post-11">Open post</a>`,
				`<span class="notification-mark" data-show="$readUpTo &lt; 7">Unread</span>`,
				`<time class="post-time" datetime="2026-10-27T09:30:00Z"`,
			},
		},
		{
			name: "reply, read", n: with(func(n *app.Notification) { n.Kind, n.Read = app.NotificationReply, true }),
			want:    []string{`class="notification" data-kind="reply"`, actor + "replied in" + room, `href="/posts/10#post-11"`},
			notWant: []string{"unread", "notification-mark", "data-class", "data-show"},
		},
		{
			name: "assigned shows the task", n: with(func(n *app.Notification) {
				n.Kind, n.TaskID, n.TaskTitle = app.NotificationAssigned, 5, `Write "docs"`
			}),
			want: []string{`data-kind="assigned"`, actor + `assigned you &#34;Write &#34;docs&#34;&#34; in` + room, `href="/posts/10#post-11"`},
		},
		{
			name: "added to a room links to the room", n: with(func(n *app.Notification) {
				n.Kind, n.PostID, n.RootID = app.NotificationAddedToRoom, 0, 0
			}),
			want: []string{
				`data-kind="added_to_room"`, actor + "added you to" + room,
				`<a class="notification-link" href="/rooms/3">Open room</a>`,
			},
			notWant: []string{"Open post"},
		},
		{
			name: "a top-level post links to itself", n: with(func(n *app.Notification) {
				n.Kind, n.PostID, n.RootID = app.NotificationMention, 10, 10
			}),
			want: []string{`href="/posts/10#post-10"`},
		},
		{
			name: "names are escaped", n: with(func(n *app.Notification) {
				n.Kind, n.Actor.DisplayName, n.RoomName = app.NotificationMention, "<b>Al</b>", "<i>room</i>"
			}),
			want:    []string{"&lt;b&gt;Al&lt;/b&gt;", "&lt;i&gt;room&lt;/i&gt;"},
			notWant: []string{"<b>Al", "<i>room"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			html := renderHTML(t, views.NotificationItem(tt.n))
			for _, w := range tt.want {
				if !strings.Contains(html, w) {
					t.Errorf("html does not contain %q:\n%s", w, html)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(html, w) {
					t.Errorf("html contains %q:\n%s", w, html)
				}
			}
		})
	}
}

func TestNotificationPaths(t *testing.T) {
	if got, want := views.NotificationsOlderPath(42), "/notifications?before=42"; got != want {
		t.Errorf("NotificationsOlderPath = %q, want %q", got, want)
	}
	tests := []struct {
		seen uint64
		live bool
		want string
	}{
		{0, true, "/me/stream?notifications=0"},
		{9, true, "/me/stream?notifications=9"},
		{9, false, "/me/stream"},
	}
	for _, tt := range tests {
		if got := views.NotificationsStreamPath(tt.seen, tt.live); got != tt.want {
			t.Errorf("NotificationsStreamPath(%d, %v) = %q, want %q", tt.seen, tt.live, got, tt.want)
		}
	}
}

func TestNotificationsPage(t *testing.T) {
	user := app.User{DisplayName: "Bob", Username: "bob", Role: app.RoleMember}
	n := app.Notification{ID: 4, Kind: app.NotificationMention, Actor: app.UserRef{Username: "alice", DisplayName: "Alice"}, RoomID: 1, RoomName: "general", PostID: 2, RootID: 2}
	tests := []struct {
		name    string
		d       views.NotificationsData
		want    []string
		notWant []string
	}{
		{
			name: "empty", d: views.NotificationsData{Sidebar: views.SidebarData{User: user}},
			want:    []string{`id="notifications-empty">No notifications yet.`, `id="notification-list"`, "Mark all as read", `@get(&#39;/me/stream?notifications=0&#39;`},
			notWant: []string{"Older"},
		},
		{
			name: "one page with a next", d: views.NotificationsData{Sidebar: views.SidebarData{User: user}, Items: []app.Notification{n}, HasNext: true, NextBefore: 4},
			want:    []string{`id="notification-4"`, `<a class="older" href="/notifications?before=4">Older</a>`, `notifications=4`},
			notWant: []string{"No notifications yet."},
		},
		{
			name: "an older page keeps no list live", d: views.NotificationsData{Sidebar: views.SidebarData{User: user}, Items: []app.Notification{n}, Before: 9},
			want:    []string{`@get(&#39;/me/stream&#39;`},
			notWant: []string{"notifications=4"},
		},
		{
			name: "a message", d: views.NotificationsData{Sidebar: views.SidebarData{User: user}, Message: "Something went wrong."},
			want: []string{`<p class="field-error" id="notifications-message" role="alert">Something went wrong.</p>`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			html := renderHTML(t, views.NotificationsPage(tt.d))
			for _, w := range tt.want {
				if !strings.Contains(html, w) {
					t.Errorf("html does not contain %q:\n%s", w, html)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(html, w) {
					t.Errorf("html contains %q:\n%s", w, html)
				}
			}
		})
	}
}
