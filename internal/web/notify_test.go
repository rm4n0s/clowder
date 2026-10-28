package web

import (
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
)

// notifyEnv is the rooms fixture with a group conversation "crew" of alice, bob
// and carol and a direct conversation of alice and bob.
type notifyEnv struct {
	*roomsEnv
	crew, pair uint64
}

func newNotifyEnv(t *testing.T) *notifyEnv {
	t.Helper()
	e := &notifyEnv{roomsEnv: newRoomsEnv(t)}
	var err error
	if e.crew, err = e.msgs.StartGroup(t.Context(), e.actor(e.alice), []uint64{e.bob.ID, e.carol.ID}, "Feeder team"); err != nil {
		t.Fatalf("StartGroup: %+v", err)
	}
	if e.pair, err = e.msgs.StartDirect(t.Context(), e.actor(e.alice), e.bob.ID); err != nil {
		t.Fatalf("StartDirect: %+v", err)
	}
	return e
}

// prefsOf reads the stored preferences of a user in a place.
func (e *notifyEnv) prefsOf(user string, ref app.PlaceRef) app.PlacePrefs {
	e.t.Helper()
	p, err := e.prefs.Place(e.t.Context(), e.actor(e.users(user)), ref)
	if err != nil {
		e.t.Fatalf("Place: %+v", err)
	}
	return p
}

func TestSettingsNotificationsSection(t *testing.T) {
	t.Parallel()
	e := newNotifyEnv(t)
	rec := e.send(get("/me/settings", e.cookies["bob"]))
	requireBody(t, rec, []string{
		`<a href="#notifications">Notifications</a>`,
		`<section id="notifications" class="settings-section">`,
		`<form id="notifications-form" method="post" action="/me/settings/notifications"`,
		`name="notify" value="all"`, `name="notify" value="mentions" checked`, `name="notify" value="nothing"`,
		`Mentions, direct messages and group messages`,
		`name="sound" value="on" checked`,
		`name="soundName" value="purr" checked`, `name="soundName" value="chirp"`, `name="soundName" value="bell"`, `name="soundName" value="meow"`,
		`data-play-sound="purr"`, `data-play-sound="chirp"`, `data-play-sound="bell"`, `data-play-sound="meow"`,
		`id="notify-browser"`, `name="replies" value="following" checked`, `name="replies" value="direct"`,
		`id="notify-places-empty"`, `<div id="notify-events" hidden></div>`, `<script src="/static/notify.js?v=`,
	}, `id="notify-places"`, "TODO")

	// Rooms and conversations that have their own preferences are listed with a
	// link to the dialog.
	e.set(t, "bob", app.RoomRef(e.general.ID), app.PlaceUpdate{Muted: ptr(true), Notify: ptr(app.PlaceNotifyMentions)})
	e.set(t, "bob", app.ConversationRef(e.crew), app.PlaceUpdate{Sound: ptr(app.PlaceSoundOff)})
	rec = e.send(get("/me/settings", e.cookies["bob"]))
	requireBody(t, rec, []string{
		`id="notify-places"`,
		`<a href="/rooms/` + idText(e.general.ID) + `?prefs=1">`, `Muted, Notify: mentions`,
		`<a href="/messages/` + idText(e.crew) + `?prefs=1">`, `Feeder team`, `Sound off`,
	}, `id="notify-places-empty"`)
	if strings.Contains(rec.Body.String(), "random") {
		t.Error("a room without preferences is listed")
	}
}

// set changes the preferences of a user in a place.
func (e *notifyEnv) set(t *testing.T, user string, ref app.PlaceRef, update app.PlaceUpdate) {
	t.Helper()
	if _, err := e.prefs.UpdatePlace(t.Context(), e.actor(e.users(user)), ref, update); err != nil {
		t.Fatalf("UpdatePlace: %+v", err)
	}
}

func ptr[T any](v T) *T { return &v }

func TestSettingsNotificationsSave(t *testing.T) {
	t.Parallel()
	all := url.Values{
		"notify": {"all"}, "sound": {"on"}, "soundName": {"meow"}, "browserNotifications": {"on"}, "replies": {"direct"},
	}
	tests := []struct {
		name     string
		form     url.Values
		opts     []reqOpt
		wantCode int
		want     []string
		stored   app.Settings
	}{
		{name: "datastar saves", form: all, opts: []reqOpt{asDatastar()}, wantCode: http.StatusOK,
			want:   []string{`id="notifications"`, `<p class="saved" role="status">Saved</p>`, `name="notify" value="all" checked`, `name="soundName" value="meow" checked`},
			stored: app.Settings{Theme: app.ThemeSystem, Sound: true, BrowserNotifications: true, Notify: app.NotifyAll, SoundName: app.SoundMeow, Replies: app.RepliesDirect, Keywords: []string{}}},
		{name: "plain redirects back", form: all, wantCode: http.StatusSeeOther,
			stored: app.Settings{Theme: app.ThemeSystem, Sound: true, BrowserNotifications: true, Notify: app.NotifyAll, SoundName: app.SoundMeow, Replies: app.RepliesDirect, Keywords: []string{}}},
		{name: "a checkbox that is not there is off", form: url.Values{"notify": {"nothing"}, "soundName": {"bell"}, "replies": {"following"}},
			opts: []reqOpt{asDatastar()}, wantCode: http.StatusOK, want: []string{`name="notify" value="nothing" checked`},
			stored: app.Settings{Theme: app.ThemeSystem, Notify: app.NotifyNothing, SoundName: app.SoundBell, Replies: app.RepliesFollowing, Keywords: []string{}}},
		{name: "datastar unknown notify", form: url.Values{"notify": {"some"}, "sound": {"on"}}, opts: []reqOpt{asDatastar()}, wantCode: http.StatusOK,
			want:   []string{`id="notifications"`, "Choose all new activity, mentions or nothing."},
			stored: app.Settings{Theme: app.ThemeSystem, Sound: true, Notify: app.NotifyMentions, SoundName: app.SoundPurr, Replies: app.RepliesFollowing, Keywords: []string{}, GroupMentions: true}},
		{name: "plain unknown sound shows the page", form: url.Values{"soundName": {"moo"}, "sound": {"on"}}, wantCode: http.StatusUnprocessableEntity,
			want:   []string{"<title id=\"page-title\">(1) Settings · Clowder</title>", "Choose Purr, Chirp, Bell or Meow."},
			stored: app.Settings{Theme: app.ThemeSystem, Sound: true, Notify: app.NotifyMentions, SoundName: app.SoundPurr, Replies: app.RepliesFollowing, Keywords: []string{}, GroupMentions: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newNotifyEnv(t)
			rec := e.send(postForm("/me/settings/notifications", tt.form, append([]reqOpt{e.cookies["bob"]}, tt.opts...)...))
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d:\n%s", rec.Code, tt.wantCode, rec.Body)
			}
			requireBody(t, rec, tt.want)
			if tt.wantCode == http.StatusSeeOther && rec.Header().Get("Location") != "/me/settings?saved=notifications#notifications" {
				t.Errorf("Location = %q", rec.Header().Get("Location"))
			}
			got, err := e.accounts.Settings(t.Context(), e.actor(e.bob))
			if err != nil || !reflect.DeepEqual(got, tt.stored) {
				t.Errorf("settings = %+v, %v; want %+v", got, err, tt.stored)
			}
		})
	}
	t.Run("the saved note shows on the page", func(t *testing.T) {
		t.Parallel()
		e := newNotifyEnv(t)
		rec := e.send(get("/me/settings?saved=notifications", e.cookies["bob"]))
		requireBody(t, rec, []string{`<p class="saved" role="status">Saved</p>`})
	})
}

func TestPlacePrefsDialogIsOnTheRoomAndConversationPages(t *testing.T) {
	t.Parallel()
	e := newNotifyEnv(t)
	tests := []struct {
		name    string
		user    string
		path    string
		want    []string
		notWant []string
	}{
		{"a room member", "bob", "/rooms/" + idText(e.general.ID), []string{
			`<dialog id="place-prefs-dialog"`, `<form id="place-prefs-form" method="post" action="/rooms/` + idText(e.general.ID) + `/notifications"`,
			`id="place-prefs-open"`, `Notification preferences`, `name="notify" value="default" checked`, `name="notify" value="all"`,
			`name="sound" value="default" checked`, `name="markUnread" value="all" checked`, `name="markUnread" value="mentions"`,
			`name="ignoreGroupMentions"`, `name="muted"`, `<div id="notify-place" hidden data-place="room:` + idText(e.general.ID) + `">`,
		}, nil},
		{"a visible room that was not joined", "carol", "/rooms/" + idText(e.general.ID), nil, []string{`place-prefs`, `Notification preferences`}},
		{"the page of a post", "bob", "", []string{`<div id="notify-place" hidden data-place="room:` + idText(e.general.ID) + `">`}, []string{`place-prefs-dialog`}},
		{"a group", "bob", "/messages/" + idText(e.crew), []string{
			`<dialog id="place-prefs-dialog"`, `action="/messages/` + idText(e.crew) + `/notifications"`, `name="ignoreGroupMentions"`,
			`<div id="notify-place" hidden data-place="conversation:` + idText(e.crew) + `">`,
		}, []string{`name="markUnread"`}},
		{"a direct conversation", "bob", "/messages/" + idText(e.pair), []string{`<dialog id="place-prefs-dialog"`}, []string{`name="ignoreGroupMentions"`, `name="markUnread"`}},
		{"the board", "bob", "/rooms/" + idText(e.general.ID) + "/board", []string{`<div id="notify-place" hidden data-place="room:` + idText(e.general.ID) + `">`}, nil},
	}
	post := e.writePost("alice", "general", "hello everyone")
	for i := range tests {
		if tests[i].path == "" {
			tests[i].path = "/posts/" + idText(post.ID)
		}
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := e.send(get(tt.path, e.cookies[tt.user]))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d:\n%s", rec.Code, rec.Body)
			}
			requireBody(t, rec, tt.want, tt.notWant...)
		})
	}
}

func TestPlacePrefsSave(t *testing.T) {
	t.Parallel()
	e := newNotifyEnv(t)
	room, group, pair, secret := app.RoomRef(e.general.ID), app.ConversationRef(e.crew), app.ConversationRef(e.pair), app.RoomRef(e.secret.ID)
	tests := []struct {
		name     string
		user     string
		path     string
		form     url.Values
		opts     []reqOpt
		wantCode int
		want     []string
		check    func(t *testing.T)
	}{
		{name: "a room, datastar", user: "bob", path: "/rooms/" + idText(e.general.ID) + "/notifications",
			form: url.Values{"notify": {"mentions"}, "sound": {"off"}, "markUnread": {"mentions"}, "muted": {"on"}}, opts: []reqOpt{asDatastar()}, wantCode: http.StatusOK,
			want: []string{`id="place-prefs-form"`, `<p class="saved" role="status">Saved</p>`, `name="notify" value="mentions" checked`, `name="muted" value="on" checked`},
			check: func(t *testing.T) {
				if got := e.prefsOf("bob", room); got.Notify != app.PlaceNotifyMentions || got.Sound != app.PlaceSoundOff || got.MarkUnread != app.MarkUnreadMentions || !got.Muted || got.IgnoreGroupMentions {
					t.Errorf("stored = %+v", got)
				}
			}},
		{name: "a group, plain", user: "carol", path: "/messages/" + idText(e.crew) + "/notifications",
			form: url.Values{"notify": {"nothing"}, "ignoreGroupMentions": {"on"}}, wantCode: http.StatusSeeOther,
			check: func(t *testing.T) {
				if got := e.prefsOf("carol", group); got.Notify != app.PlaceNotifyNothing || !got.IgnoreGroupMentions || got.Muted {
					t.Errorf("stored = %+v", got)
				}
			}},
		{name: "a checkbox that is not there is off", user: "bob", path: "/rooms/" + idText(e.general.ID) + "/notifications",
			form: url.Values{"notify": {"default"}}, opts: []reqOpt{asDatastar()}, wantCode: http.StatusOK,
			check: func(t *testing.T) {
				if got := e.prefsOf("bob", room); got.Muted || got.Notify != app.PlaceNotifyDefault {
					t.Errorf("stored = %+v, want the mute lifted", got)
				}
			}},
		{name: "an unknown choice, datastar", user: "bob", path: "/messages/" + idText(e.crew) + "/notifications",
			form: url.Values{"notify": {"some"}}, opts: []reqOpt{asDatastar()}, wantCode: http.StatusOK,
			want: []string{`id="place-prefs-form"`, "Choose default, all new activity, mentions or nothing."},
			check: func(t *testing.T) {
				if got := e.prefsOf("bob", group); got.Notify != app.PlaceNotifyDefault {
					t.Errorf("stored = %+v", got)
				}
			}},
		{name: "group mentions in a direct conversation, datastar", user: "bob", path: "/messages/" + idText(e.pair) + "/notifications",
			form: url.Values{"ignoreGroupMentions": {"on"}}, opts: []reqOpt{asDatastar()}, wantCode: http.StatusOK,
			want: []string{"A conversation of two has no group mentions."},
			check: func(t *testing.T) {
				if got := e.prefsOf("bob", pair); got.IgnoreGroupMentions {
					t.Errorf("stored = %+v", got)
				}
			}},
		{name: "a visible room that was not joined, plain", user: "carol", path: "/rooms/" + idText(e.general.ID) + "/notifications",
			form: url.Values{"muted": {"on"}}, wantCode: http.StatusForbidden},
		{name: "a visible room that was not joined, datastar", user: "carol", path: "/rooms/" + idText(e.general.ID) + "/notifications",
			form: url.Values{"muted": {"on"}}, opts: []reqOpt{asDatastar()}, wantCode: http.StatusOK, want: []string{"You are not allowed to do that"}},
		{name: "a private room of others", user: "carol", path: "/rooms/" + idText(e.secret.ID) + "/notifications",
			form: url.Values{"muted": {"on"}}, wantCode: http.StatusNotFound},
		{name: "a conversation of others", user: "carol", path: "/messages/" + idText(e.pair) + "/notifications",
			form: url.Values{"muted": {"on"}}, wantCode: http.StatusNotFound},
		{name: "a number that is not one", user: "bob", path: "/rooms/x/notifications", form: url.Values{"muted": {"on"}}, wantCode: http.StatusNotFound},
		{name: "a conversation number that is not one", user: "bob", path: "/messages/x/notifications", form: url.Values{"muted": {"on"}}, wantCode: http.StatusNotFound},
		{name: "nobody", user: "", path: "/rooms/" + idText(e.general.ID) + "/notifications", form: url.Values{"muted": {"on"}}, wantCode: http.StatusFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := tt.opts
			if tt.user != "" {
				opts = append([]reqOpt{e.cookies[tt.user]}, opts...)
			}
			rec := e.send(postForm(tt.path, tt.form, opts...))
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d:\n%s", rec.Code, tt.wantCode, rec.Body)
			}
			requireBody(t, rec, tt.want)
			if tt.wantCode == http.StatusSeeOther && tt.user != "" {
				want := strings.TrimSuffix(tt.path, "/notifications")
				if rec.Header().Get("Location") != want {
					t.Errorf("Location = %q, want %q", rec.Header().Get("Location"), want)
				}
			}
			if tt.check != nil {
				tt.check(t)
			}
		})
	}
	// A refused request saved nothing for the private room.
	if _, err := e.prefs.Place(t.Context(), e.actor(e.carol), secret); err == nil {
		t.Error("carol reads the preferences of a private room")
	}
}

func TestMutedRoomsAndConversationsAreDimmedAtTheEnd(t *testing.T) {
	t.Parallel()
	e := newNotifyEnv(t)
	e.set(t, "bob", app.RoomRef(e.general.ID), app.PlaceUpdate{Muted: ptr(true)})
	rec := e.send(get("/", e.cookies["bob"]))
	body := rec.Body.String()
	general := strings.Index(body, `data-room="general"`)
	secret := strings.Index(body, `data-room="secret"`)
	if general < 0 || secret < 0 || general < secret {
		t.Errorf("general at %d, secret at %d; want the muted room after the other", general, secret)
	}
	requireBody(t, rec, []string{`<li id="sidebar-room-` + idText(e.general.ID) + `" class="muted"><a href="/rooms/` + idText(e.general.ID) + `"`, `class="muted-icon"`, `aria-label="Muted"`})
	if n := strings.Count(body, `class="muted"`); n != 1 {
		t.Errorf("%d muted rooms in the sidebar, want 1", n)
	}

	// A muted conversation: dimmed, last, no unread mark, and out of the count.
	if _, err := e.msgs.Send(t.Context(), e.actor(e.alice), e.crew, app.MessageInput{Body: "one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.msgs.Send(t.Context(), e.actor(e.alice), e.pair, app.MessageInput{Body: "two"}); err != nil {
		t.Fatal(err)
	}
	e.set(t, "bob", app.ConversationRef(e.crew), app.PlaceUpdate{Muted: ptr(true)})
	rec = e.send(get("/messages", e.cookies["bob"]))
	body = rec.Body.String()
	crew := strings.Index(body, `data-conversation="`+idText(e.crew)+`"`)
	pair := strings.Index(body, `data-conversation="`+idText(e.pair)+`"`)
	if crew < 0 || pair < 0 || crew < pair {
		t.Errorf("crew at %d, pair at %d; want the muted group after the other", crew, pair)
	}
	requireBody(t, rec, []string{`class="conversation unread muted"`, `id="message-count" class="count-badge" title="1 conversation with unread messages"`})
	if n := strings.Count(body, "conversation-unread"); n != 1 {
		t.Errorf("%d unread marks, want 1 (the muted conversation shows none)", n)
	}
}

// ringPatch finds the notice of the given id in the patches of the stream.
func ringAttrs(t *testing.T, p patch) map[string]string {
	t.Helper()
	if p.selector != "#notify-events" || p.mode != "append" {
		t.Fatalf("patch has selector %q and mode %q, want #notify-events and append", p.selector, p.mode)
	}
	out := map[string]string{}
	for _, name := range []string{"data-id", "data-kind", "data-notice", "data-sound", "data-place", "data-url"} {
		const q = `"`
		i := strings.Index(p.html, name+"="+q)
		if i < 0 {
			continue
		}
		rest := p.html[i+len(name)+2:]
		out[name] = strings.NewReplacer("&#39;", "'", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&#34;", `"`).Replace(rest[:strings.Index(rest, q)])
	}
	if strings.Contains(p.html, "data-browser") {
		out["data-browser"] = "1"
	}
	return out
}

func TestStreamsCarryWhatRings(t *testing.T) {
	t.Parallel()
	const secret = "SECRET-WORDS-NOBODY-MAY-SEE-IN-AN-EVENT"
	e := newNotifyEnv(t)
	ss := e.serveStreams()
	if _, err := e.accounts.UpdateSettings(t.Context(), e.actor(e.bob), app.SettingsUpdate{
		Notify: ptr(app.NotifyAll), SoundName: ptr(app.SoundChirp), BrowserNotifications: ptr(true),
	}); err != nil {
		t.Fatal(err)
	}
	c := ss.open("bob", "/me/stream", asDatastar())
	requireCounts(t, c, 1)

	// A mention in a post: one notice, for the notification and not for the post.
	post := e.writePost("alice", "general", "hey @bob "+secret)
	rings := c.ringsWithin(2, 2*time.Second)
	if len(rings) != 1 {
		t.Fatalf("%d notices for a mention, want 1: %+v", len(rings), rings)
	}
	a := ringAttrs(t, rings[0])
	wantURL := fmt.Sprintf("/posts/%d#post-%d", post.ID, post.ID)
	if a["data-kind"] != "notification.created" || a["data-notice"] != "Alice mentioned you in #general" || a["data-sound"] != "chirp" ||
		a["data-browser"] != "1" || a["data-place"] != "room:"+idText(e.general.ID) || a["data-url"] != wantURL || !strings.HasPrefix(a["data-id"], "n") {
		t.Errorf("notice = %v", a)
	}
	if strings.Contains(rings[0].html, secret) {
		t.Error("the text of the post is in the notice")
	}

	// A post without a mention rings for the member who chose All.
	e.writePost("alice", "general", "plain news "+secret)
	rings = c.ringsWithin(1, 2*time.Second)
	if len(rings) != 1 {
		t.Fatalf("%d notices for a post, want 1", len(rings))
	}
	a = ringAttrs(t, rings[0])
	if a["data-kind"] != "post.created" || a["data-notice"] != "Alice posted in #general" || a["data-id"][0] != 'p' {
		t.Errorf("notice = %v", a)
	}
	if strings.Contains(rings[0].html, secret) {
		t.Error("the text of the post is in the notice")
	}

	// A message in the group, and a direct one.
	if _, err := e.msgs.Send(t.Context(), e.actor(e.alice), e.crew, app.MessageInput{Body: secret}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.msgs.Send(t.Context(), e.actor(e.alice), e.pair, app.MessageInput{Body: secret}); err != nil {
		t.Fatal(err)
	}
	rings = c.ringsWithin(2, 2*time.Second)
	if len(rings) != 2 {
		t.Fatalf("%d notices for two messages, want 2", len(rings))
	}
	a, b := ringAttrs(t, rings[0]), ringAttrs(t, rings[1])
	if a["data-notice"] != "Alice wrote in Feeder team" || a["data-place"] != "conversation:"+idText(e.crew) || a["data-url"] != "/messages/"+idText(e.crew) {
		t.Errorf("group notice = %v", a)
	}
	if b["data-notice"] != "Alice sent you a message" || b["data-place"] != "conversation:"+idText(e.pair) {
		t.Errorf("direct notice = %v", b)
	}
	for _, r := range rings {
		if strings.Contains(r.html, secret) {
			t.Error("the text of a message is in a notice")
		}
	}

	// Muted: the mention reaches the bell (the count) but nothing rings, and
	// nothing rings for the post or the message either.
	e.set(t, "bob", app.RoomRef(e.general.ID), app.PlaceUpdate{Muted: ptr(true)})
	e.set(t, "bob", app.ConversationRef(e.crew), app.PlaceUpdate{Muted: ptr(true)})
	c.drain()
	e.writePost("alice", "general", "@bob again")
	e.writePost("alice", "general", "more news")
	if _, err := e.msgs.Send(t.Context(), e.actor(e.alice), e.crew, app.MessageInput{Body: "again"}); err != nil {
		t.Fatal(err)
	}
	if rings := c.ringsWithin(1, quietTime); len(rings) != 0 {
		t.Errorf("a muted place rang: %+v", rings)
	}
	var counted bool
	for _, p := range c.drain() {
		if strings.Contains(p.html, `id="notification-count"`) {
			counted = true
		}
	}
	if !counted {
		t.Error("the mention in the muted room did not reach the count of the bell")
	}
}

func TestEveryStreamOfAPageCarriesTheNotices(t *testing.T) {
	t.Parallel()
	e := newNotifyEnv(t)
	post := e.writePost("alice", "general", "root")
	if _, err := e.accounts.UpdateSettings(t.Context(), e.actor(e.bob), app.SettingsUpdate{Notify: ptr(app.NotifyAll)}); err != nil {
		t.Fatal(err)
	}
	ss := e.serveStreams()
	streams := []struct {
		name string
		path string
	}{
		{"room", "/rooms/" + idText(e.secret.ID) + "/stream"},
		{"thread", "/posts/" + idText(post.ID) + "/stream"},
		{"board", "/rooms/" + idText(e.general.ID) + "/board/stream"},
		{"conversation", "/messages/" + idText(e.pair) + "/stream"},
	}
	for _, s := range streams {
		t.Run(s.name, func(t *testing.T) {
			c := ss.open("bob", s.path, asDatastar())
			if c.status != http.StatusOK {
				t.Fatalf("status = %d", c.status)
			}
			c.drainQuiet()
			// A post in the other room rings, and so does a message of the
			// group that this page does not show.
			e.writePost("alice", "general", "news "+s.name)
			if rings := c.ringsWithin(1, streamTimeout); len(rings) != 1 || !strings.Contains(rings[0].html, "Alice posted in #general") {
				t.Errorf("notices = %+v", rings)
			}
			if _, err := e.msgs.Send(t.Context(), e.actor(e.alice), e.crew, app.MessageInput{Body: "x"}); err != nil {
				t.Fatal(err)
			}
			if rings := c.ringsWithin(1, streamTimeout); len(rings) != 1 || !strings.Contains(rings[0].html, "Alice wrote in Feeder team") {
				t.Errorf("notices = %+v", rings)
			}
		})
	}
}

func TestPlacePrefsChangeRefreshesTheLists(t *testing.T) {
	t.Parallel()
	e := newNotifyEnv(t)
	ss := e.serveStreams()
	c := ss.open("bob", "/me/stream?messages=1", asDatastar())
	c.drainQuiet()
	e.set(t, "bob", app.ConversationRef(e.crew), app.PlaceUpdate{Muted: ptr(true)})
	var sidebar, list bool
	for _, p := range c.drain() {
		switch {
		case strings.Contains(p.html, `id="sidebar"`):
			sidebar = true
		case strings.Contains(p.html, `id="conversation-list"`):
			list = strings.Contains(p.html, `class="conversation unread muted"`) || strings.Contains(p.html, `class="conversation muted"`)
		}
	}
	if !sidebar || !list {
		t.Errorf("sidebar patched = %v, list shows the mute = %v; want both", sidebar, list)
	}
}
