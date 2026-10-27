package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/store/storetest"
	"github.com/rm4n0s/clowder/internal/web/views"
)

// notifications lists the notifications of the user through the service, the
// newest first.
func (e *roomsEnv) notifications(user string) []app.Notification {
	e.t.Helper()
	page, err := e.notifs.List(e.t.Context(), e.actor(e.users(user)), 0, 50)
	if err != nil {
		e.t.Fatalf("List: %+v", err)
	}
	return page.Notifications
}

// unread is the number of unread notifications of the user.
func (e *roomsEnv) unread(user string) int {
	e.t.Helper()
	n, err := e.notifs.UnreadCount(e.t.Context(), e.actor(e.users(user)))
	if err != nil {
		e.t.Fatalf("UnreadCount: %+v", err)
	}
	return n
}

// mentionBob makes bob a notification: alice mentions him in a post of the
// room. It returns the post.
func (e *roomsEnv) mentionBob(room, text string) app.Post {
	e.t.Helper()
	return e.writePost("alice", room, "Hello @bob, "+text)
}

// badge is the badge of the sidebar for n unread notifications.
func badge(n int) string {
	if n == 0 {
		return `<span id="notification-count" class="count-badge"></span>`
	}
	title := numText(n) + " unread notifications"
	if n == 1 {
		title = "1 unread notification"
	}
	return `<span id="notification-count" class="count-badge" title="` + title + `">` + numText(n) + `</span>`
}

// numText is the decimal text of n.
func numText(n int) string { return idText(uint64(n)) }

func TestSidebarShowsTheUnreadCount(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	post := e.mentionBob("general", "one")
	e.mentionBob("general", "two")
	roomPath := "/rooms/" + idText(e.general.ID)
	// Bob was also added to "secret" by alice, which is a third one.
	if got := e.unread("bob"); got != 3 {
		t.Fatalf("bob's unread = %d, want 3", got)
	}
	tests := []struct {
		name string
		user string
		page string
		want int
		// stream is the stream the page opens; the others do not appear.
		stream string
	}{
		{"home", "bob", "/", 3, views.MeStreamPath},
		{"rooms", "bob", "/rooms", 3, views.MeStreamPath},
		{"new room", "bob", "/rooms/new", 3, views.MeStreamPath},
		{"search", "bob", "/search", 3, views.MeStreamPath},
		{"admin users", "admin", "/admin/users", 0, views.MeStreamPath},
		{"admin rooms", "admin", "/admin/rooms", 0, views.MeStreamPath},
		{"admin audit", "admin", "/admin/audit", 0, views.MeStreamPath},
		{"a room", "bob", roomPath, 3, roomPath + "/stream"},
		{"a board", "bob", roomPath + "/board", 3, roomPath + "/board/stream"},
		{"a post", "bob", "/posts/" + idText(post.ID), 3, "/posts/" + idText(post.ID) + "/stream"},
		{"no notifications", "carol", "/", 0, views.MeStreamPath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(get(tt.page, e.cookies[tt.user]))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			want := []string{`href="/notifications"`, badge(tt.want), `@get(&#39;` + tt.stream}
			notWant := []string{}
			if tt.stream != views.MeStreamPath {
				notWant = append(notWant, views.MeStreamPath)
			}
			requireBody(t, rec, want, notWant...)
			if tt.want == 0 {
				requireBody(t, rec, nil, `title="0 unread`, `class="count-badge" title`)
			}
		})
	}
}

func TestNotificationsPageText(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	gen := idText(e.general.ID)
	sec := idText(e.secret.ID)
	mention := e.writePost("alice", "general", "Hi @bob")
	reply := e.commentOn(e.writePost("bob", "general", "my post"), "alice", "an answer")
	assigned := e.writeTasks("alice", "general", "Work", app.TaskInput{Title: "Fix <it>", Assignees: []uint64{e.bob.ID}})
	if err := e.rooms.AddMember(t.Context(), e.actor(e.alice), e.secret.ID, e.carol.ID); err != nil {
		t.Fatal(err)
	}
	e.mentionBob("secret", "in the private room")

	tests := []struct {
		name    string
		user    string
		want    []string
		notWant []string
	}{
		{
			name: "mention, reply, assignment and the private room", user: "bob",
			want: []string{
				"<title>Notifications · Clowder</title>", "<h1>Notifications</h1>",
				`<span class="notification-actor">Alice</span> <span class="notification-username">@alice</span> mentioned you in <a class="notification-room" href="/rooms/` + sec + `">secret</a>`,
				`data-kind="mention"`,
				`mentioned you in <a class="notification-room" href="/rooms/` + gen + `">general</a>`,
				`href="/posts/` + idText(mention.ID) + `#post-` + idText(mention.ID) + `"`,
				`data-kind="reply"`, `replied to you in <a class="notification-room" href="/rooms/` + gen + `">general</a>`,
				`href="/posts/` + idText(reply.RootID) + `#post-` + idText(reply.ID) + `"`,
				`data-kind="assigned"`, `assigned you &#34;Fix &lt;it&gt;&#34; in <a class="notification-room"`,
				`href="/posts/` + idText(assigned.ID) + `#post-` + idText(assigned.ID) + `"`,
				`<time class="post-time" datetime="`, `>Open post</a>`,
				// Bob was added to "secret" by alice before the rest.
				`data-kind="added_to_room"`,
			},
			notWant: []string{"No notifications yet.", "Fix <it>"},
		},
		{
			name: "added to a room links to the room", user: "carol",
			want: []string{
				`data-kind="added_to_room"`,
				`<span class="notification-actor">Alice</span> <span class="notification-username">@alice</span> added you to <a class="notification-room" href="/rooms/` + sec + `">secret</a>`,
				`<a class="notification-link" href="/rooms/` + sec + `">`, "Open room",
			},
			notWant: []string{"No notifications yet.", "Open post", "mentioned you"},
		},
		{
			name: "nothing yet", user: "admin",
			want:    []string{`<p class="empty" id="notifications-empty">No notifications yet.</p>`, `<ol id="notification-list" class="notification-list">`},
			notWant: []string{`class="notification `, "Older"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(get("/notifications", e.cookies[tt.user]))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			requireBody(t, rec, tt.want, tt.notWant...)
		})
	}
}

func TestNotificationsPageShowsOnlyOwn(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	e.mentionBob("general", "for bob")
	e.writePost("bob", "general", "Hi @alice")
	rec := e.send(get("/notifications", e.cookies["alice"]))
	requireBody(t, rec, []string{`<span class="notification-actor">Bob</span>`}, `<span class="notification-actor">Alice</span>`)
	rec = e.send(get("/notifications", e.cookies["bob"]))
	requireBody(t, rec, []string{`<span class="notification-actor">Alice</span>`}, `<span class="notification-actor">Bob</span>`)
}

func TestNotificationsPageHidesWhatTheUserCannotRead(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	e.mentionBob("secret", "secret news")
	e.mentionBob("general", "public news")
	// And the addition of bob to "secret" at the start.
	if got := e.unread("bob"); got != 3 {
		t.Fatalf("unread = %d, want 3", got)
	}
	if err := e.rooms.Leave(t.Context(), e.actor(e.bob), e.secret.ID); err != nil {
		t.Fatal(err)
	}
	if got := e.unread("bob"); got != 1 {
		t.Fatalf("unread after leaving = %d, want 1", got)
	}
	secret := "/rooms/" + idText(e.secret.ID)
	rec := e.send(get("/", e.cookies["bob"]))
	requireBody(t, rec, []string{badge(1)})
	rec = e.send(get("/notifications", e.cookies["bob"]))
	requireBody(t, rec, []string{`href="/rooms/` + idText(e.general.ID) + `"`}, secret+`"`, ">secret<")
	if got := strings.Count(rec.Body.String(), `<li id="notification-`); got != 1 {
		t.Errorf("%d notifications shown, want 1", got)
	}
}

func TestNotificationsPagePagination(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	var posts []app.Post
	for i := range 35 {
		posts = append(posts, e.mentionBob("general", "number "+numText(i)))
	}
	// And the addition of bob to "secret" at the start.
	all := e.notifications("bob")
	if len(all) != 36 {
		t.Fatalf("%d notifications, want 36", len(all))
	}
	rec := e.send(get("/notifications", e.cookies["bob"]))
	body := rec.Body.String()
	if got := strings.Count(body, `<li id="notification-`); got != 30 {
		t.Errorf("first page has %d notifications, want 30", got)
	}
	// Newest first.
	newest, oldestShown := all[0], all[29]
	if strings.Index(body, `id="notification-`+idText(newest.ID)+`"`) > strings.Index(body, `id="notification-`+idText(oldestShown.ID)+`"`) {
		t.Errorf("the notifications are not newest first")
	}
	older := `<a class="older" href="/notifications?before=` + idText(oldestShown.ID) + `">Older</a>`
	requireBody(t, rec, []string{older, `href="/posts/` + idText(posts[34].ID)}, `id="notification-`+idText(all[30].ID)+`"`)

	rec = e.send(get("/notifications?before="+idText(oldestShown.ID), e.cookies["bob"]))
	body = rec.Body.String()
	if got := strings.Count(body, `<li id="notification-`); got != 6 {
		t.Errorf("second page has %d notifications, want 6", got)
	}
	requireBody(t, rec, []string{`id="notification-` + idText(all[30].ID) + `"`, `id="notification-` + idText(all[35].ID) + `"`},
		"Older", `id="notification-`+idText(oldestShown.ID)+`"`)

	// A before that is not a number starts at the newest, as on the other pages.
	rec = e.send(get("/notifications?before=x", e.cookies["bob"]))
	requireBody(t, rec, []string{`id="notification-` + idText(newest.ID) + `"`})
}

func TestNotificationsPageMarksReadWhatItShows(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	e.mentionBob("general", "one")
	e.mentionBob("general", "two")
	e.mentionBob("general", "three")
	all := e.notifications("bob") // three, two, one
	// A page of older ones: it shows "one" and "two" and marks them, not "three".
	rec := e.send(get("/notifications?before="+idText(all[0].ID), e.cookies["bob"]))
	requireBody(t, rec, []string{
		`id="notification-` + idText(all[1].ID) + `"`, `id="notification-` + idText(all[2].ID) + `"`,
		`class="notification unread"`,
		// The badge is the count after the marking: "three" is still unread.
		badge(1),
	}, `id="notification-`+idText(all[0].ID)+`"`)
	for _, n := range e.notifications("bob") {
		if n.Read != (n.ID != all[0].ID) {
			t.Errorf("notification %d: Read = %v", n.ID, n.Read)
		}
	}

	// The newest page marks all, shows them as they were, and its badge is 0.
	rec = e.send(get("/notifications", e.cookies["bob"]))
	requireBody(t, rec, []string{
		`id="notification-` + idText(all[0].ID) + `" class="notification unread"`,
		`<span class="notification-mark" data-show="$readUpTo &lt; ` + idText(all[0].ID) + `">Unread</span>`,
		badge(0),
	})
	if got := e.unread("bob"); got != 0 {
		t.Errorf("unread = %d, want 0", got)
	}
	// The next visit shows them as read.
	rec = e.send(get("/notifications", e.cookies["bob"]))
	requireBody(t, rec, []string{`id="notification-` + idText(all[0].ID) + `" class="notification"`}, "notification unread", "notification-mark")
}

func TestNotificationsMarkAllRead(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		e.mentionBob("general", "one")
		e.mentionBob("general", "two")
		e.writePost("bob", "general", "Hi @alice")
		newest := e.notifications("bob")[0]
		rec := e.post("bob", "/notifications/read", nil, datastar)
		if datastar {
			requireSSE(t, rec)
			requireBody(t, rec, []string{
				"datastar-patch-signals", `"readUpTo":` + idText(newest.ID),
				"datastar-patch-elements", badge(0),
			})
		} else {
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/notifications" {
				t.Fatalf("status = %d, Location = %q, want 303 to /notifications", rec.Code, rec.Header().Get("Location"))
			}
		}
		if got := e.unread("bob"); got != 0 {
			t.Errorf("bob's unread = %d, want 0", got)
		}
		if got := e.unread("alice"); got != 1 {
			t.Errorf("alice's unread = %d, want 1: a mark of bob touches only his", got)
		}
	})
}

func TestNotificationsMarkAllReadWithNothing(t *testing.T) {
	t.Parallel()
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		rec := e.post("carol", "/notifications/read", nil, datastar)
		if datastar {
			requireSSE(t, rec)
			requireBody(t, rec, []string{`"readUpTo":0`, badge(0)})
		} else if rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, want 303", rec.Code)
		}
	})
}

func TestNotificationsPageForms(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	rec := e.send(get("/notifications", e.cookies["bob"]))
	requireBody(t, rec, []string{
		`<section id="notifications" data-signals="{readUpTo: 0}">`,
		`<form id="mark-all-form" class="mark-all-form" method="post" action="/notifications/read" data-on:submit="@post(&#39;/notifications/read&#39;, {contentType: &#39;form&#39;})">`,
		"Mark all as read",
	})
}

func TestNotificationsRoutesNeedASession(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	tests := []struct {
		name string
		req  func() *http.Request
	}{
		{"page", func() *http.Request { return get("/notifications") }},
		{"mark all, plain", func() *http.Request { return postForm("/notifications/read", url.Values{}) }},
		{"mark all, datastar", func() *http.Request { return postForm("/notifications/read", url.Values{}, asDatastar()) }},
		{"stream, datastar", func() *http.Request { return get("/me/stream", asDatastar()) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(tt.req())
			if isDatastar(tt.req()) {
				requireSSE(t, rec)
				requireBody(t, rec, []string{redirectScript("/login")})
				return
			}
			if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
				t.Errorf("status = %d, Location = %q, want a redirect to the login page", rec.Code, rec.Header().Get("Location"))
			}
		})
	}
}

func TestNotificationsWrongMethod(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	for _, tt := range []struct{ method, path string }{
		{http.MethodPost, "/notifications"}, {http.MethodGet, "/notifications/read"}, {http.MethodPost, "/me/stream"},
	} {
		req, err := http.NewRequest(tt.method, tt.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		e.cookies["bob"](req)
		if rec := e.send(req); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: status = %d, want 405", tt.method, tt.path, rec.Code)
		}
	}
}

// breakNotifications makes the notifications service of the server fail every
// call, as a store that is gone does.
func (e *roomsEnv) breakNotifications() {
	e.t.Helper()
	broken := storetest.Open(e.t, e.clk)
	if err := broken.Close(); err != nil {
		e.t.Fatalf("Close: %v", err)
	}
	e.srv.svc.Notifications = app.NewNotifications(broken, nil)
}

func TestNotificationsMarkAllReadFailure(t *testing.T) {
	t.Parallel()
	t.Run("datastar shows the message with status 200 and logs the failure", func(t *testing.T) {
		e := newRoomsEnv(t)
		e.breakNotifications()
		rec := e.post("bob", "/notifications/read", nil, true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{`<p class="field-error" id="notifications-message" role="alert">` + messageTryAgain + `</p>`}, "readUpTo", "QueryFailed", "closed")
		var logged bool
		for _, le := range logEntries(t, e.log) {
			if le.Msg == "request failed" && le.Err != nil {
				logged = true
				if !le.Err.HasRoute("Notifications.List->Store.ListNotifications.QueryFailed") {
					t.Errorf("route = %s", le.Err.Route())
				}
			}
		}
		if !logged {
			t.Errorf("the failure was not logged: %s", e.log.String())
		}
	})
	t.Run("a plain form gets the error page", func(t *testing.T) {
		e := newRoomsEnv(t)
		e.breakNotifications()
		rec := e.post("bob", "/notifications/read", nil, false)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
		requireBody(t, rec, []string{messageInternalError}, "QueryFailed")
	})
}
