package views_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
)

func TestSidebarRoomCounts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		room    app.Room
		want    []string
		notWant []string
	}{
		{
			name:    "nothing waiting shows nothing extra",
			room:    app.Room{ID: 3, Name: "general"},
			want:    []string{`<li id="sidebar-room-3" class="">`, `data-room="general"`},
			notWant: []string{"count-badge", "unread"},
		},
		{
			name: "unread posts: bold and the number",
			room: app.Room{ID: 3, Name: "general", UnreadPosts: 5},
			want: []string{`<li id="sidebar-room-3" class="unread">`, `<span class="count-badge room-unread" title="5 unread posts">5</span>`},
			notWant: []string{
				"mention-badge",
			},
		},
		{
			name: "one unread post",
			room: app.Room{ID: 3, Name: "general", UnreadPosts: 1},
			want: []string{`title="1 unread post">1</span>`},
		},
		{
			name: "99 is shown as it is",
			room: app.Room{ID: 3, Name: "general", UnreadPosts: 99},
			want: []string{`>99</span>`},
		},
		{
			name:    "above 99 is 99+",
			room:    app.Room{ID: 3, Name: "general", UnreadPosts: 100},
			want:    []string{`title="More than 99 unread posts">99+</span>`},
			notWant: []string{">100<"},
		},
		{
			name: "unread posts and mentions: two badges",
			room: app.Room{ID: 3, Name: "general", UnreadPosts: 12, UnreadMentions: 2},
			want: []string{
				`<span class="count-badge room-unread" title="12 unread posts">12</span>`,
				`<span class="count-badge mention-badge" title="2 unread mentions">2</span>`,
			},
		},
		{
			name: "one unread mention",
			room: app.Room{ID: 3, Name: "general", UnreadMentions: 1},
			want: []string{`title="1 unread mention">1</span>`, `<li id="sidebar-room-3" class="">`},
			notWant: []string{
				"room-unread", `class="unread"`,
			},
		},
		{
			name: "only mentions: not bold, no post count, the mention badge",
			room: app.Room{ID: 3, Name: "general", UnreadPosts: 9, UnreadMentions: 4, MentionsOnly: true},
			want: []string{`<li id="sidebar-room-3" class="">`, `<span class="count-badge mention-badge" title="4 unread mentions">4</span>`},
			notWant: []string{
				"room-unread", `class="unread"`, "unread posts",
			},
		},
		{
			name: "only mentions with none: nothing",
			room: app.Room{ID: 3, Name: "general", UnreadPosts: 9, MentionsOnly: true},
			notWant: []string{
				"count-badge", `class="unread"`,
			},
		},
		{
			name: "muted: dimmed, no post count, only the mention badge",
			room: app.Room{ID: 3, Name: "general", UnreadPosts: 9, UnreadMentions: 1, Muted: true},
			want: []string{`<li id="sidebar-room-3" class="muted">`, `class="muted-icon"`, `<span class="count-badge mention-badge" title="1 unread mention">1</span>`},
			notWant: []string{
				"room-unread", `unread"`,
			},
		},
		{
			name:    "muted with no mention: no badge at all",
			room:    app.Room{ID: 3, Name: "general", UnreadPosts: 9, Muted: true},
			want:    []string{`class="muted"`},
			notWant: []string{"count-badge"},
		},
		{
			name: "the room of the page is marked current",
			room: app.Room{ID: 3, Name: "general"},
			want: []string{`class="current"`, `aria-current="page"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			current := uint64(0)
			if strings.Contains(tt.name, "current") {
				current = tt.room.ID
			}
			got := render(t, views.SidebarRoom(tt.room, current))
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(got, w) {
					t.Errorf("unexpected %q in:\n%s", w, got)
				}
			}
		})
	}
}

func TestWithRoomRead(t *testing.T) {
	t.Parallel()
	d := views.SidebarData{Rooms: []app.Room{
		{ID: 1, UnreadPosts: 3, UnreadMentions: 1}, {ID: 2, UnreadPosts: 4, UnreadMentions: 2},
	}}
	got := d.WithRoomRead(1)
	if got.Rooms[0].UnreadPosts != 0 || got.Rooms[0].UnreadMentions != 0 || got.Rooms[1].UnreadPosts != 4 || got.Rooms[1].UnreadMentions != 2 {
		t.Errorf("WithRoomRead(1) = %+v, want only room 1 cleared", got.Rooms)
	}
	if d.Rooms[0].UnreadPosts != 3 {
		t.Errorf("the receiver was changed: %+v", d.Rooms)
	}
	if one := views.WithRoomRead(app.Room{ID: 5, UnreadPosts: 2, UnreadMentions: 1}); one.UnreadPosts != 0 || one.UnreadMentions != 0 || one.ID != 5 {
		t.Errorf("WithRoomRead(room) = %+v", one)
	}
}

func TestTabTitle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		unread int
		base   string
		want   string
	}{
		{"nothing", 0, "Home · Clowder", "Home · Clowder"},
		{"some", 3, "Home · Clowder", "(3) Home · Clowder"},
		{"99", 99, "Home · Clowder", "(99) Home · Clowder"},
		{"above 99", 100, "Home · Clowder", "(99+) Home · Clowder"},
		{"negative", -1, "Home · Clowder", "Home · Clowder"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := views.TabTitle(tt.unread, tt.base); got != tt.want {
				t.Errorf("TabTitle = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSplitTitle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		title     string
		wantLabel string
		wantBase  string
	}{
		{"Home · Clowder", "", "Home · Clowder"},
		{"(3) Home · Clowder", "3", "Home · Clowder"},
		{"(99+) Home · Clowder", "99+", "Home · Clowder"},
		{"(0) Home · Clowder", "", "(0) Home · Clowder"},
		{"(100) Home · Clowder", "", "(100) Home · Clowder"},
		{"(x) Home · Clowder", "", "(x) Home · Clowder"},
		{"(a room) Home · Clowder", "", "(a room) Home · Clowder"},
		{"(3)Home", "", "(3)Home"},
		{"", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			t.Parallel()
			label, base := views.SplitTitle(tt.title)
			if label != tt.wantLabel || base != tt.wantBase {
				t.Errorf("SplitTitle(%q) = %q, %q; want %q, %q", tt.title, label, base, tt.wantLabel, tt.wantBase)
			}
		})
	}
}

func TestPageTitleAndFavicon(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		c    templ.Component
		want string
	}{
		{"title with nothing waiting", views.PageTitle(0, "Home · Clowder"), `<title id="page-title">Home · Clowder</title>`},
		{"title with some waiting", views.PageTitle(4, "Home · Clowder"), `<title id="page-title">(4) Home · Clowder</title>`},
		{"title is escaped", views.PageTitle(0, "<b>&"), `<title id="page-title">&lt;b&gt;&amp;</title>`},
		{"plain favicon", views.Favicon(0), `<link id="favicon" rel="icon" type="image/svg+xml" href="/static/favicon.svg">`},
		{"favicon with a dot", views.Favicon(2), `<link id="favicon" rel="icon" type="image/svg+xml" href="/static/favicon-dot.svg">`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := render(t, tt.c); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
	if got := (views.SidebarData{Unread: 2, UnreadMessages: 3}).UnreadTotal(); got != 5 {
		t.Errorf("UnreadTotal = %d, want 5", got)
	}
}

func TestLayoutTitleAndFavicon(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		unread int
		want   []string
	}{
		{"none", 0, []string{`<title id="page-title">T · Clowder</title>`, `href="/static/favicon.svg"`}},
		{"some", 7, []string{`<title id="page-title">(7) T · Clowder</title>`, `href="/static/favicon-dot.svg"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := render(t, views.Layout("T", tt.unread, nil))
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
		})
	}
}

func TestNewSinceID(t *testing.T) {
	t.Parallel()
	post := func(id, author uint64) app.Post {
		return app.Post{ID: id, Author: app.UserRef{ID: author}}
	}
	const me = 1
	deleted := post(9, 2)
	deleted.Deleted = true
	tests := []struct {
		name  string
		posts []app.Post // newest first
		seen  uint64
		want  uint64
	}{
		{"some unseen: after the oldest unseen", []app.Post{post(30, 2), post(20, 2), post(10, 2)}, 15, 20},
		{"all unseen: after the last post of the page", []app.Post{post(30, 2), post(20, 2), post(10, 2)}, 0, 10},
		{"none unseen: no line", []app.Post{post(30, 2), post(20, 2), post(10, 2)}, 30, 0},
		{"the mark equals the newest", []app.Post{post(30, 2)}, 30, 0},
		{"only my own posts are new: no line", []app.Post{post(30, me), post(20, me), post(10, 2)}, 15, 0},
		{"my own post among the new ones", []app.Post{post(30, 2), post(25, me), post(20, 2), post(10, 2)}, 15, 20},
		{"a deleted post is not new", []app.Post{post(30, 2), deleted, post(8, 2)}, 8, 30},
		{"an empty page", nil, 5, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := views.NewSinceID(tt.posts, tt.seen, me); got != tt.want {
				t.Errorf("NewSinceID = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestFeedPostsDrawTheLineAfterThePost(t *testing.T) {
	t.Parallel()
	page := app.FeedPage{Posts: []app.Post{
		{ID: 30, Author: app.UserRef{ID: 2}}, {ID: 20, Author: app.UserRef{ID: 2}}, {ID: 10, Author: app.UserRef{ID: 2}},
	}}
	tests := []struct {
		name      string
		newSince  uint64
		wantAfter string
		wantLine  bool
	}{
		{"after the middle post", 20, `id="post-20"`, true},
		{"after the last post", 10, `id="post-10"`, true},
		{"no line", 0, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := render(t, views.FeedPosts(views.FeedData{RoomID: 1, Page: page, NewSince: tt.newSince}))
			line := strings.Index(got, `id="feed-new-line"`)
			if (line >= 0) != tt.wantLine {
				t.Fatalf("line present = %v, want %v:\n%s", line >= 0, tt.wantLine, got)
			}
			if !tt.wantLine {
				return
			}
			if strings.Count(got, `id="feed-new-line"`) != 1 || !strings.Contains(got, "New since your last visit") {
				t.Errorf("want one line with its text:\n%s", got)
			}
			// Newest first: the line comes right after the post it is named for
			// and before every older post.
			after := strings.Index(got, tt.wantAfter)
			if after < 0 || line < after {
				t.Errorf("the line (at %d) is not after %s (at %d)", line, tt.wantAfter, after)
			}
			for _, older := range page.Posts {
				marker := `id="post-` + strconv.FormatUint(older.ID, 10) + `"`
				if older.ID < tt.newSince && strings.Index(got, marker) < line {
					t.Errorf("older post %d is above the line", older.ID)
				}
			}
		})
	}
}
