package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
)

// requireCount fails unless the patch is the badge with n unread.
func requireCount(t *testing.T, p patch, n int) {
	t.Helper()
	requirePatch(t, p, "", "outer", []string{badge(n)}, nil)
}

// messageBadge is the badge of the sidebar for n conversations with unread
// messages.
func messageBadge(n int) string {
	if n == 0 {
		return `<span id="message-count" class="count-badge"></span>`
	}
	title := numText(n) + " conversations with unread messages"
	if n == 1 {
		title = "1 conversation with unread messages"
	}
	return `<span id="message-count" class="count-badge" title="` + title + `">` + numText(n) + `</span>`
}

// requireMessageCount fails unless the patch is the badge with n conversations
// with unread messages.
func requireMessageCount(t *testing.T, p patch, n int) {
	t.Helper()
	requirePatch(t, p, "", "outer", []string{messageBadge(n)}, nil)
}

// requireCounts reads the two patches of the counts of the sidebar, the
// notifications and then the messages (none unread), and checks the first.
func requireCounts(t *testing.T, c *streamClient, n int) {
	t.Helper()
	got := c.patches(2)
	requireCount(t, got[0], n)
	requireMessageCount(t, got[1], 0)
}

func TestMeStreamSendsTheCountFirst(t *testing.T) {
	e := newRoomsEnv(t)
	e.mentionBob("general", "one")
	ss := e.serveStreams()
	tests := []struct {
		name string
		user string
		want int
	}{
		{"bob has a few", "bob", 2},
		{"carol has none", "carol", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := ss.open(tt.user, "/me/stream", asDatastar())
			if c.status != http.StatusOK {
				t.Fatalf("status = %d, want 200", c.status)
			}
			requireCounts(t, c, tt.want)
			c.requireQuiet()
		})
	}
}

func TestMeStreamPatchesTheCount(t *testing.T) {
	e := newRoomsEnv(t)
	c := e.serveStreams().open("bob", "/me/stream", asDatastar())
	requireCounts(t, c, 1) // the addition to "secret"

	// A notification raises it.
	e.mentionBob("general", "hello")
	requireCounts(t, c, 2)
	c.requireQuiet()

	// Reading lowers it. Nothing else is patched: the sidebar is left alone.
	if err := e.notifs.MarkRead(t.Context(), e.actor(e.bob), e.notifications("bob")[0].ID); err != nil {
		t.Fatal(err)
	}
	requireCounts(t, c, 0)
	c.requireQuiet()

	// Someone else's notification is not for this stream.
	e.writePost("bob", "general", "Hi @alice")
	c.requireQuiet()
}

func TestMeStreamPatchesTheRoomList(t *testing.T) {
	e := newRoomsEnv(t)
	c := e.serveStreams().open("carol", "/me/stream", asDatastar())
	requireCounts(t, c, 0)

	// Alice adds carol to "secret": her room list gets it, and the count the
	// notification.
	if err := e.rooms.AddMember(t.Context(), e.actor(e.alice), e.secret.ID, e.carol.ID); err != nil {
		t.Fatal(err)
	}
	var sidebar, count bool
	for _, p := range c.drain() {
		switch {
		case p.selector == "#sidebar":
			requirePatch(t, p, "#sidebar", "inner", []string{`data-room="secret"`}, []string{"aria-current"})
			sidebar = true
		case strings.Contains(p.html, `id="notification-count"`):
			requireCount(t, p, 1)
			count = true
		case strings.Contains(p.html, `id="message-count"`):
			requireMessageCount(t, p, 0)
		default:
			t.Errorf("unexpected patch: %+v", p)
		}
	}
	if !sidebar || !count {
		t.Errorf("sidebar patched = %v, count patched = %v, want both", sidebar, count)
	}

	// Leaving takes it away.
	if err := e.rooms.Leave(t.Context(), e.actor(e.carol), e.secret.ID); err != nil {
		t.Fatal(err)
	}
	got := c.drain()
	if len(got) == 0 {
		t.Fatal("no patch after leaving")
	}
	requirePatch(t, got[len(got)-1], "#sidebar", "inner", []string{`<ul class="sidebar-room-list"></ul>`}, []string{`data-room=`})
}

func TestMeStreamReconnectSendsTheSidebar(t *testing.T) {
	e := newRoomsEnv(t)
	e.mentionBob("general", "hello")
	c := e.serveStreams().open("bob", "/me/stream", asDatastar(), withHeader("Last-Event-ID", "1"))
	got := c.patches(1)
	requirePatch(t, got[0], "#sidebar", "inner", []string{`data-room="general"`, `data-room="secret"`, badge(2)}, nil)
}

func TestMeStreamKeepAliveAndEnd(t *testing.T) {
	e := newRoomsEnv(t)
	e.srv.pingInterval = 20 * time.Millisecond
	ss := e.serveStreams()
	c := ss.open("alice", "/me/stream", asDatastar())
	for range 2 {
		if got := c.comment(); got != "ping" {
			t.Errorf("comment = %q, want ping", got)
		}
	}
	// A user who is disabled is sent to the login page.
	bob := ss.open("bob", "/me/stream", asDatastar())
	if err := e.accounts.SetDisabled(t.Context(), e.actor(e.admin), e.bob.ID, true); err != nil {
		t.Fatalf("SetDisabled: %+v", err)
	}
	var last patch
	for _, p := range bob.drainUntilEnd() {
		last = p
	}
	requireRedirect(t, last, "/login")

	// The hub closing ends the others.
	e.hub.Close()
	c.requireEnd()
}

// drainUntilEnd returns the patches that come until the stream ends.
func (c *streamClient) drainUntilEnd() []patch {
	c.t.Helper()
	var out []patch
	for {
		select {
		case b, ok := <-c.blocks:
			if !ok {
				return out
			}
			if b.event != "" {
				out = append(out, patchOf(c.t, b))
			}
		case <-time.After(streamTimeout):
			c.t.Fatal("the stream did not end in time")
		}
	}
}

func TestMeStreamNeedsALogin(t *testing.T) {
	e := newRoomsEnv(t)
	c := e.serveStreams().open("", "/me/stream", asDatastar())
	if c.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", c.status)
	}
	requireRedirect(t, patchOf(t, c.next()), "/login")
	c.requireEnd()

	plain := e.serveStreams().open("", "/me/stream")
	if plain.status != http.StatusFound {
		t.Errorf("plain status = %d, want 302", plain.status)
	}
}

// listItem reports whether the HTML is the item of the notification n.
func listItem(html string, n app.Notification) bool {
	return strings.Contains(html, `<li id="notification-`+idText(n.ID)+`" class="notification unread"`)
}

func TestMeStreamKeepsTheNotificationsListLive(t *testing.T) {
	e := newRoomsEnv(t)
	ss := e.serveStreams()

	t.Run("a new notification goes on top, unread, and raises the count", func(t *testing.T) {
		e := newRoomsEnv(t)
		seen := e.notifications("bob")[0].ID
		c := e.serveStreams().open("bob", "/me/stream?notifications="+idText(seen), asDatastar())
		requireCounts(t, c, 1)
		c.requireQuiet()

		post := e.mentionBob("general", "live")
		got := c.patches(4)
		requireCount(t, got[0], 2)
		requireMessageCount(t, got[1], 0)
		got = got[1:]
		fresh := e.notifications("bob")[0]
		// The removal of a copy of the item, which a reconnection could leave.
		requirePatch(t, got[1], "#notification-"+idText(fresh.ID), "remove", nil, nil)
		requirePatch(t, got[2], "#notification-list", "prepend",
			[]string{`<li id="notification-` + idText(fresh.ID) + `" class="notification unread"`, `mentioned you in`,
				`href="/posts/` + idText(post.ID) + `#post-` + idText(post.ID) + `"`, `data-class:unread="$readUpTo &lt; ` + idText(fresh.ID) + `"`}, nil)
		c.requireQuiet()

		// Reading (the page was marked) changes the count only: no flicker of
		// the list.
		if err := e.notifs.MarkRead(t.Context(), e.actor(e.bob), fresh.ID); err != nil {
			t.Fatal(err)
		}
		requireCounts(t, c, 0)
		c.requireQuiet()
	})

	t.Run("a page that was empty loses its note", func(t *testing.T) {
		c := ss.open("carol", "/me/stream?notifications=0", asDatastar())
		requireCounts(t, c, 0)
		if err := e.rooms.AddMember(t.Context(), e.actor(e.alice), e.secret.ID, e.carol.ID); err != nil {
			t.Fatal(err)
		}
		var removedNote, prepended bool
		for _, p := range c.drain() {
			switch {
			case p.mode == "remove" && p.selector == "#notifications-empty":
				removedNote = true
			case p.selector == "#notification-list" && p.mode == "prepend":
				prepended = strings.Contains(p.html, "added you to")
			}
		}
		if !removedNote || !prepended {
			t.Errorf("note removed = %v, item added = %v, want both", removedNote, prepended)
		}
	})

	t.Run("what came while the page was being made is sent when the stream opens", func(t *testing.T) {
		e := newRoomsEnv(t)
		c0 := e.notifications("bob")[0]
		e.mentionBob("general", "late one")
		e.mentionBob("general", "later one")
		fresh := e.notifications("bob")
		c := e.serveStreams().open("bob", "/me/stream?notifications="+idText(c0.ID), asDatastar())
		got := c.patches(6)
		requireCount(t, got[0], 3)
		requireMessageCount(t, got[1], 0)
		got = got[1:]
		// Oldest first, so that the newest ends on top.
		if !listItem(got[2].html+got[4].html, fresh[0]) || !strings.Contains(got[2].html, idText(fresh[1].ID)) || !strings.Contains(got[4].html, idText(fresh[0].ID)) {
			t.Errorf("items are not oldest first:\n%s\n%s", got[2].html, got[4].html)
		}
		c.requireQuiet()
	})

	t.Run("a page of older notifications keeps its list", func(t *testing.T) {
		e := newRoomsEnv(t)
		c := e.serveStreams().open("bob", "/me/stream", asDatastar())
		requireCounts(t, c, 1)
		e.mentionBob("general", "not on this page")
		requireCounts(t, c, 2)
		c.requireQuiet()
	})
}

func TestNotificationsPageOpensItsStream(t *testing.T) {
	e := newRoomsEnv(t)
	post := e.mentionBob("general", "x")
	_ = post
	newest := e.notifications("bob")[0]
	tests := []struct {
		name string
		path string
		want string
	}{
		{"the newest", "/notifications", `@get(&#39;/me/stream?notifications=` + idText(newest.ID) + `&#39;`},
		{"older ones", "/notifications?before=" + idText(newest.ID), `@get(&#39;/me/stream&#39;`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(get(tt.path, e.cookies["bob"]))
			requireBody(t, rec, []string{tt.want}, "notifications=0")
		})
	}
	rec := e.send(get("/notifications", e.cookies["carol"]))
	requireBody(t, rec, []string{`@get(&#39;/me/stream?notifications=0&#39;`})
}

func TestPageStreamsKeepTheCountLive(t *testing.T) {
	// The streams of the pages that have their own keep the count of the
	// sidebar live too (SPEC D31): they patch the badge alone, not the sidebar.
	tests := []struct {
		name string
		path func(e *roomsEnv, root app.Post) string
	}{
		{"room", func(e *roomsEnv, root app.Post) string {
			return e.streamOfRoom("general", "seen="+idText(root.ID))
		}},
		{"thread", func(e *roomsEnv, root app.Post) string {
			return "/posts/" + idText(root.ID) + "/stream?seen=" + idText(root.ID)
		}},
		{"board", func(e *roomsEnv, root app.Post) string {
			return e.streamOfBoard("general", "v="+e.boardVersion("bob", "general"))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newRoomsEnv(t)
			root := e.writePost("alice", "general", "root")
			c := e.serveStreams().open("bob", tt.path(e, root), asDatastar())
			c.requireQuiet()

			// A mention elsewhere (a comment would also be an event of the thread).
			e.writePost("alice", "secret", "Hi @bob")
			requireCounts(t, c, 2)
			c.requireQuiet()

			if err := e.notifs.MarkRead(t.Context(), e.actor(e.bob), e.notifications("bob")[0].ID); err != nil {
				t.Fatal(err)
			}
			requireCounts(t, c, 0)
			c.requireQuiet()

			// Someone else's notification is not for this stream.
			e.writePost("bob", "general", "Hi @alice")
			if tt.name == "room" {
				// The room stream shows bob's new post; the count is not touched.
				for _, p := range c.drain() {
					if strings.Contains(p.html, `id="notification-count"`) {
						t.Errorf("a count patch for someone else's notification: %+v", p)
					}
				}
			}
		})
	}
}
