package views_test

import (
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
)

func TestNotifyEvent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		n       app.Notice
		want    []string
		notWant []string
	}{
		{"a mention with the browser", app.Notice{
			Kind: "notification.created", ID: "n7", Text: "Bob mentioned you in #general", RoomID: 3, PostID: 9, RootID: 4,
			Sound: app.SoundBell, Browser: true,
		}, []string{
			`<div id="notify-n7" class="notify-event" data-id="n7" data-kind="notification.created"`,
			`data-notice="Bob mentioned you in #general"`, `data-sound="bell"`, `data-place="room:3"`, `data-url="/posts/4#post-9"`, `data-browser="1"`,
		}, nil},
		{"a message without a sound or the browser", app.Notice{
			Kind: "message.created", ID: "m2", Text: "Bob sent you a message", ConversationID: 5, MessageID: 2,
		}, []string{`data-id="m2"`, `data-sound=""`, `data-place="conversation:5"`, `data-url="/messages/5"`}, []string{"data-browser"}},
		{"an addition goes to the room", app.Notice{
			Kind: "notification.created", ID: "n1", Text: "Bob added you to #design", RoomID: 8, NotificationKind: app.NotificationAddedToRoom,
		}, []string{`data-url="/rooms/8"`}, nil},
		{"the text is escaped", app.Notice{
			Kind: "post.created", ID: "p1", Text: `Bob "the <b>" posted in #x`, RoomID: 1, PostID: 1, RootID: 1,
		}, []string{`data-notice="Bob &#34;the &lt;b&gt;&#34; posted in #x"`}, []string{`<b>`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			requireHTML(t, renderHTML(t, views.NotifyEvent(tt.n)), tt.want, tt.notWant)
		})
	}
}

func TestPlacePrefsSummary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		p    app.PlacePrefs
		want string
	}{
		{"nothing set", app.PlacePrefs{Notify: app.PlaceNotifyDefault, Sound: app.PlaceSoundDefault, MarkUnread: app.MarkUnreadAll}, ""},
		{"muted", app.PlacePrefs{Notify: app.PlaceNotifyDefault, Sound: app.PlaceSoundDefault, Muted: true}, "Muted"},
		{"everything", app.PlacePrefs{
			Notify: app.PlaceNotifyMentions, Sound: app.PlaceSoundOff, Muted: true, IgnoreGroupMentions: true, MarkUnread: app.MarkUnreadMentions,
		}, "Muted, Notify: mentions, Sound off, Ignores @room and @here, Counts mentions only"},
		{"sound on", app.PlacePrefs{Notify: app.PlaceNotifyAll, Sound: app.PlaceSoundOn}, "Notify: all new activity, Sound on"},
	}
	for _, tt := range tests {
		if got := views.PlacePrefsSummary(tt.p); got != tt.want {
			t.Errorf("%s: PlacePrefsSummary = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestPlaceKeys(t *testing.T) {
	t.Parallel()
	if got := views.PlaceKey(app.RoomRef(3)); got != "room:3" {
		t.Errorf("PlaceKey(room) = %q", got)
	}
	if got := views.PlaceKey(app.ConversationRef(5)); got != "conversation:5" {
		t.Errorf("PlaceKey(conversation) = %q", got)
	}
	if got := views.RoomPrefsPath(3); got != "/rooms/3/notifications" {
		t.Errorf("RoomPrefsPath = %q", got)
	}
	if got := views.ConversationPrefsPath(5); got != "/messages/5/notifications" {
		t.Errorf("ConversationPrefsPath = %q", got)
	}
}

func TestPlacePrefsDialog(t *testing.T) {
	t.Parallel()
	room := views.PlacePrefsData{
		Prefs:  app.PlacePrefs{Place: app.RoomRef(3), Notify: app.PlaceNotifyAll, Sound: app.PlaceSoundDefault, MarkUnread: app.MarkUnreadAll, Muted: true},
		Title:  "general",
		Action: "/rooms/3/notifications",
	}
	pair := views.PlacePrefsData{
		Prefs:  app.PlacePrefs{Place: app.ConversationRef(5), Direct: true, Notify: app.PlaceNotifyDefault, Sound: app.PlaceSoundDefault, MarkUnread: app.MarkUnreadAll},
		Action: "/messages/5/notifications",
		Errors: views.FormErrors{Fields: map[string]string{"ignoreGroupMentions": "A conversation of two has no group mentions."}},
	}
	requireHTML(t, renderHTML(t, views.PlacePrefsDialog(room)), []string{
		`<dialog id="place-prefs-dialog"`, `<h2 id="place-prefs-title">Notification preferences</h2>`, `general`,
		`name="notify" value="all" checked`, `name="muted" value="on" checked`, `name="markUnread" value="all" checked`, `name="ignoreGroupMentions"`,
		`<form method="dialog"`,
	}, nil)
	requireHTML(t, renderHTML(t, views.PlacePrefsButton(room)), []string{`id="place-prefs-open"`, `aria-label="Muted"`, `Notification preferences`}, nil)
	html := renderHTML(t, views.PlacePrefsForm(pair))
	requireHTML(t, html, []string{`A conversation of two has no group mentions.`}, []string{`name="ignoreGroupMentions"`, `name="markUnread"`})
	// A viewer with no preferences here gets neither the dialog nor the button.
	none := views.PlacePrefsData{}
	if got := renderHTML(t, views.PlacePrefsDialog(none)) + renderHTML(t, views.PlacePrefsButton(none)); got != "" {
		t.Errorf("a viewer without preferences got %q", got)
	}
}

func TestNotifyPrefsSection(t *testing.T) {
	t.Parallel()
	d := views.NotifyPrefsData{
		Settings: app.Settings{Theme: app.ThemeSystem, Sound: false, BrowserNotifications: true, Notify: app.NotifyNothing, SoundName: app.SoundMeow, Replies: app.RepliesDirect},
		Places: []views.PlaceListItem{
			{Name: "general", Room: true, Path: "/rooms/1?prefs=1", Summary: "Muted"},
			{Name: "Feeder team", Path: "/messages/2?prefs=1", Summary: "Sound off"},
		},
		Errors: views.FormErrors{Form: "Try again.", Fields: map[string]string{"notify": "Pick one."}},
		Saved:  true,
	}
	requireHTML(t, renderHTML(t, views.NotifyPrefsSection(d)), []string{
		`<p class="form-error" role="alert">Try again.</p>`, `Pick one.`,
		`name="notify" value="nothing" checked`, `name="soundName" value="meow" checked`, `name="replies" value="direct" checked`,
		`id="notify-browser" type="checkbox" name="browserNotifications" value="on" checked`,
		`<p class="saved" role="status">Saved</p>`,
		`<a href="/rooms/1?prefs=1">`, `#general`, `Muted`, `<a href="/messages/2?prefs=1">`, `Feeder team`,
	}, []string{`name="sound" value="on" checked`, `id="notify-places-empty"`})
}
