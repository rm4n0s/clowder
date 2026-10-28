package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
)

// roomItem is the start of the list item of a room in the sidebar.
func roomItem(e *roomsEnv, room string) string {
	return `<li id="sidebar-room-` + idText(e.room(room).ID) + `"`
}

// roomItemOf returns the list item of the room in the HTML of a page or a patch.
func roomItemOf(t *testing.T, html string, e *roomsEnv, room string) string {
	t.Helper()
	start := strings.Index(html, roomItem(e, room))
	if start < 0 {
		t.Fatalf("no sidebar item for %s in:\n%s", room, html)
	}
	end := strings.Index(html[start:], "</li>")
	if end < 0 {
		t.Fatalf("unterminated item for %s in:\n%s", room, html[start:])
	}
	return html[start : start+end]
}

func TestSidebarShowsTheUnreadPostsOfARoom(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	e.writePost("alice", "general", "one")
	e.writePost("alice", "general", "two")
	e.writePost("bob", "general", "bob's own")

	// Bob sees the two of alice, not his own; the pages that do not read the
	// room show it.
	for _, path := range []string{"/", "/rooms", "/notifications"} {
		item := roomItemOf(t, e.getAs("bob", path, false).Body.String(), e, "general")
		for _, w := range []string{`class="unread"`, `<span class="count-badge room-unread" title="2 unread posts">2</span>`} {
			if !strings.Contains(item, w) {
				t.Errorf("%s: bob's item lacks %q: %s", path, w, item)
			}
		}
		if strings.Contains(item, "mention-badge") {
			t.Errorf("%s: a mention badge without a mention: %s", path, item)
		}
	}
	// A room with nothing waiting shows nothing extra.
	item := roomItemOf(t, e.getAs("bob", "/", false).Body.String(), e, "secret")
	if strings.Contains(item, "count-badge") || strings.Contains(item, "unread") {
		t.Errorf("secret shows something waiting: %s", item)
	}
	// The author has only the post of bob unread, not her own.
	item = roomItemOf(t, e.getAs("alice", "/", false).Body.String(), e, "general")
	if !strings.Contains(item, `title="1 unread post">1</span>`) {
		t.Errorf("alice's own posts count: %s", item)
	}
}

func TestSidebarShowsMentionsAndMutedRooms(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	e.mentionBob("general", "one")
	e.writePost("alice", "general", "two")
	general := app.RoomRef(e.general.ID)
	bob := e.actor(e.bob)

	// The default: the posts and, separately, the mention.
	item := roomItemOf(t, e.getAs("bob", "/", false).Body.String(), e, "general")
	for _, w := range []string{`class="unread"`, `title="2 unread posts">2</span>`, `<span class="count-badge mention-badge" title="1 unread mention">1</span>`} {
		if !strings.Contains(item, w) {
			t.Errorf("default: lacks %q: %s", w, item)
		}
	}

	// Only mentions: not bold, only the mention badge.
	only := app.MarkUnreadMentions
	if _, err := e.prefs.UpdatePlace(t.Context(), bob, general, app.PlaceUpdate{MarkUnread: &only}); err != nil {
		t.Fatal(err)
	}
	item = roomItemOf(t, e.getAs("bob", "/", false).Body.String(), e, "general")
	if !strings.Contains(item, `mention-badge" title="1 unread mention">1</span>`) || strings.Contains(item, "room-unread") || strings.Contains(item, `class="unread"`) {
		t.Errorf("only mentions: %s", item)
	}

	// Muted: dimmed, no count, the mention badge.
	muted, all := true, app.MarkUnreadAll
	if _, err := e.prefs.UpdatePlace(t.Context(), bob, general, app.PlaceUpdate{MarkUnread: &all, Muted: &muted}); err != nil {
		t.Fatal(err)
	}
	item = roomItemOf(t, e.getAs("bob", "/", false).Body.String(), e, "general")
	if !strings.Contains(item, `class="muted"`) || !strings.Contains(item, `mention-badge" title="1 unread mention">1</span>`) || strings.Contains(item, "room-unread") {
		t.Errorf("muted: %s", item)
	}
}

func TestOpeningTheFeedMarksTheRoomRead(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	e.writePost("alice", "general", "one")
	p := e.mentionBob("general", "two")
	roomPath := "/rooms/" + idText(e.general.ID)

	// The post page shows the count and moves nothing.
	if rec := e.getAs("bob", "/posts/"+idText(p.ID), false); rec.Code != http.StatusOK {
		t.Fatalf("post page = %d", rec.Code)
	}
	if got := e.unreadPosts("bob", "general"); got != 2 {
		t.Fatalf("after the post page bob has %d unread, want 2", got)
	}
	if got := e.unread("bob"); got != 2 { // the mention and the addition to secret
		t.Fatalf("after the post page bob has %d notifications, want 2", got)
	}
	// An older page of the feed moves nothing either.
	if rec := e.getAs("bob", roomPath+"?before="+idText(p.ID), false); rec.Code != http.StatusOK {
		t.Fatalf("older page = %d", rec.Code)
	}
	if got := e.unreadPosts("bob", "general"); got != 2 {
		t.Fatalf("after an older page bob has %d unread, want 2", got)
	}

	// The feed: the page itself shows nothing waiting in the room, the bell
	// lost the mention of the room, the title and favicon follow.
	rec := e.getAs("bob", roomPath, false)
	item := roomItemOf(t, rec.Body.String(), e, "general")
	if strings.Contains(item, "count-badge") || strings.Contains(item, `class="unread"`) {
		t.Errorf("the open room shows something waiting: %s", item)
	}
	requireBody(t, rec, []string{badge(1), `<title id="page-title">(1) general · Clowder</title>`, `href="/static/favicon-dot.svg?v=`})
	if got := e.unreadPosts("bob", "general"); got != 0 {
		t.Errorf("after the feed bob has %d unread, want 0", got)
	}
	if got := e.unread("bob"); got != 1 {
		t.Errorf("after the feed bob has %d notifications, want 1 (the addition to secret)", got)
	}
	// Other pages agree.
	item = roomItemOf(t, e.getAs("bob", "/", false).Body.String(), e, "general")
	if strings.Contains(item, "count-badge") {
		t.Errorf("home still counts: %s", item)
	}
}

func TestOpeningTheBoardMarksTheRoomRead(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	e.writePost("alice", "general", "one")
	e.mentionBob("general", "two")
	rec := e.getAs("bob", "/rooms/"+idText(e.general.ID)+"/board", false)
	item := roomItemOf(t, rec.Body.String(), e, "general")
	if strings.Contains(item, "count-badge") {
		t.Errorf("the board shows something waiting: %s", item)
	}
	if got := e.unreadPosts("bob", "general"); got != 0 {
		t.Errorf("after the board bob has %d unread, want 0", got)
	}
	// A visitor who has not joined marks nothing and has no counts.
	rec = e.getAs("carol", "/rooms/"+idText(e.general.ID)+"/board", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("carol's board = %d", rec.Code)
	}
}

// unreadPosts is the number of unread posts of the user in the room.
func (e *roomsEnv) unreadPosts(user, room string) int {
	e.t.Helper()
	got, _, err := e.rooms.Unread(e.t.Context(), e.actor(e.users(user)), e.room(room).ID)
	if err != nil {
		e.t.Fatalf("Unread: %+v", err)
	}
	return got.Posts
}

func TestFeedLineNewSinceYourLastVisit(t *testing.T) {
	t.Parallel()
	const marker = `id="feed-new-line"`
	e := newRoomsEnv(t)
	a := e.writePost("alice", "general", "post a")
	b := e.writePost("alice", "general", "post b")
	c := e.writePost("alice", "general", "post c")
	roomPath := "/rooms/" + idText(e.general.ID)
	pos := func(body string, p app.Post) int { return strings.Index(body, `id="post-`+idText(p.ID)+`"`) }

	// Everything is unseen: the line goes after the last post of the page.
	body := e.getAs("bob", roomPath, false).Body.String()
	line := strings.Index(body, marker)
	if line < 0 || !strings.Contains(body, "New since your last visit") {
		t.Fatalf("no line when every post is unseen:\n%s", body)
	}
	if pos(body, c) >= pos(body, b) || pos(body, b) >= pos(body, a) || pos(body, a) >= line {
		t.Errorf("the line is not after the oldest post: c %d, b %d, a %d, line %d", pos(body, c), pos(body, b), pos(body, a), line)
	}

	// Seen: no line.
	body = e.getAs("bob", roomPath, false).Body.String()
	if strings.Contains(body, marker) {
		t.Errorf("a line when nothing is unseen")
	}

	// Two new posts: the line sits between the oldest of them and the newest seen.
	d := e.writePost("alice", "general", "post d")
	f := e.writePost("alice", "general", "post f")
	body = e.getAs("bob", roomPath, false).Body.String()
	line = strings.Index(body, marker)
	if line < 0 {
		t.Fatalf("no line with two new posts")
	}
	if pos(body, f) >= pos(body, d) || pos(body, d) >= line || line >= pos(body, c) {
		t.Errorf("the line is misplaced: f %d, d %d, line %d, c %d", pos(body, f), pos(body, d), line, pos(body, c))
	}
	if got := strings.Count(body, marker); got != 1 {
		t.Errorf("%d lines, want 1", got)
	}
	// The stream of the page is told where the line is.
	requireBody(t, e.getAs("bob", roomPath, false), nil, "&line=")
	e.writePost("alice", "general", "post g")
	body = e.getAs("bob", roomPath, false).Body.String()
	if !strings.Contains(body, "&amp;line="+idText(e.lastPostID("general"))) && !strings.Contains(body, "line=") {
		t.Errorf("the stream path of the page does not carry the line:\n%s", body)
	}

	// Posts of the viewer do not make a line, and an older page never has one.
	e.writePost("bob", "general", "bob's own")
	body = e.getAs("bob", roomPath, false).Body.String()
	if strings.Contains(body, marker) {
		t.Errorf("a line for the viewer's own post")
	}
	e.writePost("alice", "general", "post h")
	body = e.getAs("bob", roomPath+"?before="+idText(c.ID), false).Body.String()
	if strings.Contains(body, marker) {
		t.Errorf("an older page has a line")
	}
	// And the page of older posts that Datastar loads has none either.
	rec := e.getAs("bob", roomPath+"/feed?before="+idText(e.lastPostID("general")), true)
	if strings.Contains(rec.Body.String(), marker) {
		t.Errorf("loaded older posts have a line")
	}
}

// lastPostID is the ID of the newest top-level post of the room.
func (e *roomsEnv) lastPostID(room string) uint64 {
	e.t.Helper()
	page, err := e.posts.Feed(e.t.Context(), e.actor(e.admin), e.room(room).ID, 0, 1)
	if err != nil || len(page.Posts) == 0 {
		e.t.Fatalf("Feed: %v, %v", page, err)
	}
	return page.Posts[0].ID
}

func TestPostsPatchTheSidebarCountLive(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	// Alice has a page that does not read the room; bob's open feed does.
	alice := e.serveStreams().open("alice", "/me/stream", asDatastar())
	requireCounts(t, alice, 0)
	alice.requireQuiet()

	e.writePost("bob", "general", "hello")
	items := alice.itemsWithin(1, streamTimeout)
	if len(items) != 1 {
		t.Fatalf("%d room items, want 1", len(items))
	}
	requirePatch(t, items[0], "", "outer", []string{
		roomItem(e, "general"), `class="unread"`, `<span class="count-badge room-unread" title="1 unread post">1</span>`,
	}, []string{"mention-badge"})

	again := e.writePost("bob", "general", "again")
	items = alice.itemsWithin(1, streamTimeout)
	if len(items) != 1 || !strings.Contains(items[0].html, `title="2 unread posts">2</span>`) {
		t.Fatalf("second post: %+v", items)
	}

	// A mention is one patch with the mention badge, not two.
	e.writePost("bob", "general", "hi @alice")
	items = alice.itemsWithin(2, quietTime*2)
	if len(items) == 0 || !strings.Contains(items[len(items)-1].html, `mention-badge" title="1 unread mention">1</span>`) {
		t.Fatalf("mention: %+v", items)
	}
	if len(items) != 1 {
		t.Errorf("a mention made %d room patches, want 1", len(items))
	}

	// Deleting a post lowers the count (three were written, one is gone).
	if err := e.posts.Delete(t.Context(), e.actor(e.bob), again.ID); err != nil {
		t.Fatal(err)
	}
	items = alice.itemsWithin(1, streamTimeout)
	if len(items) != 1 || !strings.Contains(items[0].html, `title="2 unread posts">2</span>`) || !strings.Contains(items[0].html, "mention-badge") {
		t.Fatalf("after a delete: %+v", items)
	}

	// Reading the notifications lowers the mention badge.
	if err := e.notifs.MarkRead(t.Context(), e.actor(e.alice), 1<<40); err != nil {
		t.Fatal(err)
	}
	items = alice.itemsWithin(1, streamTimeout)
	if len(items) != 1 || strings.Contains(items[0].html, "mention-badge") {
		t.Fatalf("after reading the notifications: %+v", items)
	}
}

func TestAnOpenFeedKeepsItsCountAtZero(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	first := e.writePost("alice", "general", "first")
	bob := e.serveStreams().open("bob", e.streamOfRoom("general", "seen="+idText(first.ID)), asDatastar())
	// The stream sends the feed at once when posts came after the page: none.
	bob.requireQuiet()
	// Mark as the page did when it was served.
	if _, err := e.rooms.Visit(t.Context(), e.actor(e.bob), e.general.ID); err != nil {
		t.Fatal(err)
	}
	bob.itemsWithin(1, quietTime) // the patch that Visit caused

	second := e.writePost("alice", "general", "second")
	var card bool
	for _, p := range bob.drain() {
		if strings.Contains(p.html, `id="post-`+idText(second.ID)+`"`) {
			card = true
		}
	}
	if !card {
		t.Error("the new post was not put in the feed")
	}
	// Every sidebar patch of the open room shows nothing waiting.
	for _, p := range bob.itemsWithin(5, quietTime) {
		if strings.Contains(p.html, "count-badge") {
			t.Errorf("the open room shows a count: %s", p.html)
		}
	}
	if got := e.unreadPosts("bob", "general"); got != 0 {
		t.Errorf("bob has %d unread posts in the room he is looking at, want 0", got)
	}

	// The thread page of the same room does not mark: its stream shows the count.
	thread := e.serveStreams().open("alice", "/posts/"+idText(first.ID)+"/stream?seen="+idText(first.ID), asDatastar())
	thread.requireQuiet()
	e.writePost("bob", "general", "from bob")
	items := thread.itemsWithin(1, streamTimeout)
	if len(items) != 1 || !strings.Contains(items[0].html, `room-unread" title="1 unread post">1</span>`) {
		t.Errorf("the thread page of alice: %+v", items)
	}
	// A new post by the viewer himself does not move his mark.
	if got := e.unreadPosts("alice", "general"); got != 1 {
		t.Errorf("alice has %d unread, want 1", got)
	}
}

func TestTheTabShowsWhatIsWaiting(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)

	// The server renders the count in the title and the dot in the favicon.
	rec := e.getAs("bob", "/", false) // the addition to "secret"
	requireBody(t, rec, []string{`<title id="page-title">(1) Home · Clowder</title>`, `<link id="favicon" rel="icon" type="image/svg+xml" href="/static/favicon-dot.svg?v=`})
	rec = e.getAs("carol", "/", false)
	requireBody(t, rec, []string{`<title id="page-title">Home · Clowder</title>`, `<link id="favicon" rel="icon" type="image/svg+xml" href="/static/favicon.svg?v=`}, "(0)")
	// The count is the notifications and the conversations, the numbers of the bell and of Messages.
	e.mentionBob("general", "x")
	if _, err := e.msgs.StartDirect(t.Context(), e.actor(e.alice), e.bob.ID); err != nil {
		t.Fatal(err)
	}
	conv, err := e.msgs.StartDirect(t.Context(), e.actor(e.alice), e.bob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.msgs.Send(t.Context(), e.actor(e.alice), conv, app.MessageInput{Body: "hi"}); err != nil {
		t.Fatal(err)
	}
	rec = e.getAs("bob", "/", false)
	requireBody(t, rec, []string{`<title id="page-title">(3) Home · Clowder</title>`, badge(2), messageBadge(1)})

	// The page tells its stream its title; the stream patches title and favicon
	// when the count changes, and only then.
	title := func(t string) reqOpt { return withHeader("X-Page-Title", url.PathEscape(t)) }
	ss := e.serveStreams()
	carol := ss.open("carol", "/me/stream", asDatastar(), title("Home · Clowder"))
	requireCounts(t, carol, 0)
	if got := carol.tabWithin(1, quietTime); len(got) != 0 {
		t.Errorf("the stream patched the tab of a page that shows the right count: %+v", got)
	}
	if err := e.rooms.AddMember(t.Context(), e.actor(e.alice), e.secret.ID, e.carol.ID); err != nil {
		t.Fatal(err)
	}
	got := carol.tabWithin(2, streamTimeout)
	if len(got) != 2 {
		t.Fatalf("%d tab patches after the first notification, want the favicon and the title: %+v", len(got), got)
	}
	var sawFavicon, sawTitle bool
	for _, p := range got {
		switch {
		case strings.Contains(p.html, `href="/static/favicon-dot.svg?v=`):
			sawFavicon = true
		case strings.Contains(p.html, `<title id="page-title">(1) Home · Clowder</title>`):
			sawTitle = true
		default:
			t.Errorf("unexpected tab patch: %s", p.html)
		}
	}
	if !sawFavicon || !sawTitle {
		t.Errorf("favicon patched = %v, title patched = %v, want both", sawFavicon, sawTitle)
	}
	// A second notification changes the title only: the dot stays.
	e.writePost("alice", "secret", "Hi @carol")
	got = carol.tabWithin(1, streamTimeout)
	if len(got) < 1 || !strings.Contains(got[0].html, `<title id="page-title">(2) Home · Clowder</title>`) {
		t.Fatalf("second notification: %+v", got)
	}
	if more := carol.tabWithin(1, quietTime); len(more) != 0 {
		t.Errorf("more tab patches for a count that stays above 0, want only the title: %+v", more)
	}
	// Reading brings both back.
	if err := e.notifs.MarkRead(t.Context(), e.actor(e.carol), 1<<40); err != nil {
		t.Fatal(err)
	}
	got = carol.tabWithin(2, streamTimeout)
	if len(got) != 2 {
		t.Fatalf("%d tab patches after reading, want 2: %+v", len(got), got)
	}
	for _, p := range got {
		if !strings.Contains(p.html, `href="/static/favicon.svg?v=`) && !strings.Contains(p.html, `<title id="page-title">Home · Clowder</title>`) {
			t.Errorf("unexpected tab patch: %s", p.html)
		}
	}

	// A page that shows (1) already needs no patch for the same count; a
	// stream whose page told no title patches the favicon alone.
	bob := ss.open("bob", "/me/stream", asDatastar(), title("(3) Home · Clowder"))
	counts := bob.patches(2)
	requireCount(t, counts[0], 2)
	requireMessageCount(t, counts[1], 1)
	if got := bob.tabWithin(1, quietTime); len(got) != 0 {
		t.Errorf("bob's tab was patched though it shows (3): %+v", got)
	}
	plain := ss.open("alice", "/me/stream", asDatastar())
	requireCounts(t, plain, 0)
	e.writePost("bob", "secret", "Hi @alice")
	got = plain.tabWithin(1, streamTimeout)
	if len(got) != 1 || !strings.Contains(got[0].html, `href="/static/favicon-dot.svg?v=`) {
		t.Errorf("without a title only the favicon is patched: %+v", got)
	}
	if more := plain.tabWithin(1, quietTime); len(more) != 0 {
		t.Errorf("without a title more than the favicon was patched: %+v", more)
	}
}

func TestAReconnectionPatchesTheTab(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	ss := e.serveStreams()
	// The page showed no count; bob has one. The reconnection says nothing
	// about what the tab shows now, so the stream sends both.
	c := ss.open("bob", "/me/stream", asDatastar(), withHeader("X-Page-Title", url.PathEscape("Home · Clowder")), withHeader("Last-Event-ID", "1"))
	got := c.tabWithin(2, streamTimeout)
	if len(got) != 2 {
		t.Fatalf("%d tab patches after a reconnection, want 2: %+v", len(got), got)
	}
	// A title that is too long or not valid is left alone.
	long := ss.open("bob", "/me/stream", asDatastar(), withHeader("X-Page-Title", strings.Repeat("a", 1000)))
	requireCounts(t, long, 1)
	if got := long.tabWithin(2, quietTime); len(got) != 1 {
		t.Errorf("a stream told a title that is too long patched %d times, want the favicon only: %+v", len(got), got)
	}
}

func TestPageStreamActionCarriesTheTitle(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	rec := e.getAs("bob", "/", false)
	requireBody(t, rec, []string{`headers: {&#39;X-Page-Title&#39;: encodeURIComponent(document.title)}`})
	// The title that a stream is told is read like the page wrote it.
	if label, base := views.SplitTitle("(2) Home · Clowder"); label != "2" || base != "Home · Clowder" {
		t.Errorf("SplitTitle = %q, %q", label, base)
	}
}
