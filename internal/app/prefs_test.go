package app_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/markdown"
	"github.com/rm4n0s/clowder/internal/store"
)

// prefsEnv is the rooms fixture (lobby: visible, admin and bob; den: private,
// alice and bob) with carol in the lobby, a disabled user dina, a group "crew"
// of alice, bob and carol and a direct conversation of alice and bob, behind a
// Recorder.
type prefsEnv struct {
	*roomsEnv
	posts *app.Posts
	msgs  *app.Messages
	notes *app.Notifications
	prefs *app.Prefs
	rec   *live.Recorder
	crew  uint64
	pair  uint64
}

func newPrefsEnv(t *testing.T) *prefsEnv {
	t.Helper()
	e := &prefsEnv{roomsEnv: newRoomsEnv(t), rec: &live.Recorder{}}
	e.svc = app.NewAccounts(e.st, e.clk, auth.FastHasher(), app.AccountsConfig{
		InvitationLifetime: lifetime, SessionLifetime: sessionLifetime, Publisher: e.rec,
	})
	cfg := app.PostsConfig{MaxAttachmentSize: testMaxUpload, MaxAttachments: testMaxAttachments}
	e.rooms = app.NewRooms(e.st, e.files, nil, e.rec)
	e.posts = app.NewPosts(e.st, e.clk, markdown.New(), e.files, cfg, e.rec)
	e.msgs = app.NewMessages(e.st, e.clk, e.files, cfg, e.rec)
	e.notes = app.NewNotifications(e.st, e.rec)
	e.prefs = app.NewPrefs(e.st, e.rec)
	if err := e.rooms.Join(t.Context(), e.as("carol"), e.lobby.ID); err != nil {
		t.Fatalf("Join: %+v", err)
	}
	e.users["dina"] = e.signUp("dina", store.RoleMember)
	if err := e.svc.SetDisabled(t.Context(), e.as("admin"), e.users["dina"].ID, true); err != nil {
		t.Fatalf("SetDisabled: %+v", err)
	}
	var err error
	if e.crew, err = e.msgs.StartGroup(t.Context(), e.as("alice"), []uint64{e.uid("bob"), e.uid("carol")}, "Feeder team"); err != nil {
		t.Fatalf("StartGroup: %+v", err)
	}
	if e.pair, err = e.msgs.StartDirect(t.Context(), e.as("alice"), e.uid("bob")); err != nil {
		t.Fatalf("StartDirect: %+v", err)
	}
	e.rec.Reset()
	return e
}

// uid is the ID of the named user.
func (e *prefsEnv) uid(name string) uint64 {
	e.t.Helper()
	return e.users[name].ID
}

// set changes the preferences of the user in the place and fails the test on
// an error.
func (e *prefsEnv) set(user string, ref app.PlaceRef, update app.PlaceUpdate) {
	e.t.Helper()
	if _, err := e.prefs.UpdatePlace(e.t.Context(), e.as(user), ref, update); err != nil {
		e.t.Fatalf("UpdatePlace(%s): %+v", user, err)
	}
}

// settings changes the global settings of the user and fails the test on an
// error.
func (e *prefsEnv) settings(user string, update app.SettingsUpdate) {
	e.t.Helper()
	if _, err := e.svc.UpdateSettings(e.t.Context(), e.as(user), update); err != nil {
		e.t.Fatalf("UpdateSettings(%s): %+v", user, err)
	}
}

// userEvents are the recorded events of the user's own topic of the kind.
func (e *prefsEnv) userEvents(user, kind string) []live.Event {
	var out []live.Event
	for _, ev := range e.rec.Events() {
		if ev.Topic == live.UserTopic(e.uid(user)) && ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

func TestRingFor(t *testing.T) {
	t.Parallel()
	global := func(n store.NotifyLevel, sound bool, browser bool) store.UserSettings {
		return store.UserSettings{Notify: n, Sound: sound, SoundName: store.SoundChirp, BrowserNotifications: browser}
	}
	pref := func(n store.PlaceNotify, s store.PlaceSound, muted bool) store.PlacePref {
		return store.PlacePref{Notify: n, Sound: s, Muted: muted}
	}
	tests := []struct {
		name    string
		global  store.UserSettings
		place   store.PlacePref
		kind    app.PlaceKind
		event   app.EventKind
		mention bool
		want    app.Ring
	}{
		// The bell events (notifications), by the global choice, in a room.
		{"notification, global mentions", global(app.NotifyMentions, true, false), pref("default", "default", false), app.PlaceRoom, app.EventNotification, true, app.Ring{Rings: true, Sound: "chirp"}},
		{"notification, global all", global(app.NotifyAll, true, false), pref("default", "default", false), app.PlaceRoom, app.EventNotification, true, app.Ring{Rings: true, Sound: "chirp"}},
		{"notification, global nothing", global(app.NotifyNothing, true, false), pref("default", "default", false), app.PlaceRoom, app.EventNotification, true, app.Ring{}},
		{"notification, global nothing, room all", global(app.NotifyNothing, true, false), pref("all", "default", false), app.PlaceRoom, app.EventNotification, true, app.Ring{Rings: true, Sound: "chirp"}},
		{"notification, global nothing, room mentions", global(app.NotifyNothing, true, false), pref("mentions", "default", false), app.PlaceRoom, app.EventNotification, true, app.Ring{Rings: true, Sound: "chirp"}},
		{"notification, global all, room nothing", global(app.NotifyAll, true, false), pref("nothing", "default", false), app.PlaceRoom, app.EventNotification, true, app.Ring{}},
		{"notification, muted room", global(app.NotifyAll, true, true), pref("all", "on", true), app.PlaceRoom, app.EventNotification, true, app.Ring{}},
		// New posts, by the global choice and the room's.
		{"post, global mentions", global(app.NotifyMentions, true, false), pref("default", "default", false), app.PlaceRoom, app.EventPost, false, app.Ring{}},
		{"post, global all", global(app.NotifyAll, true, false), pref("default", "default", false), app.PlaceRoom, app.EventPost, false, app.Ring{Rings: true, Sound: "chirp"}},
		{"post, global nothing", global(app.NotifyNothing, true, false), pref("default", "default", false), app.PlaceRoom, app.EventPost, false, app.Ring{}},
		{"post, global mentions, room all", global(app.NotifyMentions, true, false), pref("all", "default", false), app.PlaceRoom, app.EventPost, false, app.Ring{Rings: true, Sound: "chirp"}},
		{"post, global nothing, room all", global(app.NotifyNothing, true, false), pref("all", "default", false), app.PlaceRoom, app.EventPost, false, app.Ring{Rings: true, Sound: "chirp"}},
		{"post, global all, room mentions", global(app.NotifyAll, true, false), pref("mentions", "default", false), app.PlaceRoom, app.EventPost, false, app.Ring{}},
		{"post, global all, room nothing", global(app.NotifyAll, true, false), pref("nothing", "default", false), app.PlaceRoom, app.EventPost, false, app.Ring{}},
		{"post, global all, muted room", global(app.NotifyAll, true, false), pref("default", "default", true), app.PlaceRoom, app.EventPost, false, app.Ring{}},
		// Messages of a group conversation: the default is every message.
		{"group message, global mentions", global(app.NotifyMentions, true, false), pref("default", "default", false), app.PlaceGroup, app.EventMessage, false, app.Ring{Rings: true, Sound: "chirp"}},
		{"group message, global all", global(app.NotifyAll, true, false), pref("default", "default", false), app.PlaceGroup, app.EventMessage, false, app.Ring{Rings: true, Sound: "chirp"}},
		{"group message, global nothing", global(app.NotifyNothing, true, false), pref("default", "default", false), app.PlaceGroup, app.EventMessage, true, app.Ring{}},
		{"group message, global nothing, group all", global(app.NotifyNothing, true, false), pref("all", "default", false), app.PlaceGroup, app.EventMessage, false, app.Ring{Rings: true, Sound: "chirp"}},
		{"group message, group mentions, no mention", global(app.NotifyAll, true, false), pref("mentions", "default", false), app.PlaceGroup, app.EventMessage, false, app.Ring{}},
		{"group message, group mentions, mention", global(app.NotifyAll, true, false), pref("mentions", "default", false), app.PlaceGroup, app.EventMessage, true, app.Ring{Rings: true, Sound: "chirp"}},
		{"group message, group nothing, mention", global(app.NotifyAll, true, false), pref("nothing", "default", false), app.PlaceGroup, app.EventMessage, true, app.Ring{}},
		{"group message, muted, mention", global(app.NotifyAll, true, false), pref("all", "default", true), app.PlaceGroup, app.EventMessage, true, app.Ring{}},
		// Direct messages.
		{"direct message, global mentions", global(app.NotifyMentions, true, false), pref("default", "default", false), app.PlaceDirect, app.EventMessage, false, app.Ring{Rings: true, Sound: "chirp"}},
		{"direct message, global nothing", global(app.NotifyNothing, true, false), pref("default", "default", false), app.PlaceDirect, app.EventMessage, false, app.Ring{}},
		{"direct message, mentions, no mention", global(app.NotifyMentions, true, false), pref("mentions", "default", false), app.PlaceDirect, app.EventMessage, false, app.Ring{}},
		{"direct message, muted", global(app.NotifyMentions, true, false), pref("default", "default", true), app.PlaceDirect, app.EventMessage, false, app.Ring{}},
		// The sound and the browser notification.
		{"sound off globally", global(app.NotifyAll, false, false), pref("default", "default", false), app.PlaceRoom, app.EventPost, false, app.Ring{Rings: true}},
		{"sound on in the place, off globally", global(app.NotifyAll, false, false), pref("default", "on", false), app.PlaceRoom, app.EventPost, false, app.Ring{Rings: true, Sound: "chirp"}},
		{"sound off in the place, on globally", global(app.NotifyAll, true, false), pref("default", "off", false), app.PlaceRoom, app.EventPost, false, app.Ring{Rings: true}},
		{"browser notifications on", global(app.NotifyAll, true, true), pref("default", "default", false), app.PlaceRoom, app.EventPost, false, app.Ring{Rings: true, Sound: "chirp", Browser: true}},
		{"browser notifications, sound off in the place", global(app.NotifyAll, true, true), pref("default", "off", false), app.PlaceRoom, app.EventPost, false, app.Ring{Rings: true, Browser: true}},
		{"no sound name falls back to purr", store.UserSettings{Notify: app.NotifyAll, Sound: true}, pref("default", "default", false), app.PlaceRoom, app.EventPost, false, app.Ring{Rings: true, Sound: "purr"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := app.RingFor(tt.global, tt.place, tt.kind, tt.event, tt.mention); got != tt.want {
				t.Errorf("ringFor = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestRingForEveryCombination checks the decision against a second statement
// of the rules, for every global choice, every choice of the place, mute, kind
// of place, kind of event and mention.
func TestRingForEveryCombination(t *testing.T) {
	t.Parallel()
	globals := []store.NotifyLevel{app.NotifyAll, app.NotifyMentions, app.NotifyNothing}
	places := []store.PlaceNotify{app.PlaceNotifyDefault, app.PlaceNotifyAll, app.PlaceNotifyMentions, app.PlaceNotifyNothing}
	kinds := []app.PlaceKind{app.PlaceRoom, app.PlaceDirect, app.PlaceGroup}
	events := []app.EventKind{app.EventNotification, app.EventPost, app.EventMessage}
	// Each row of coverage is: what the effective choice covers, indexed by
	// event, with a mention and without. Rows: nothing, mentions, all.
	covers := func(level string, event app.EventKind, mention bool) bool {
		switch level {
		case "nothing":
			return false
		case "mentions":
			return event == app.EventNotification || (event == app.EventMessage && mention)
		default:
			return true
		}
	}
	n := 0
	for _, g := range globals {
		for _, p := range places {
			for _, muted := range []bool{false, true} {
				for _, kind := range kinds {
					for _, ev := range events {
						if (ev == app.EventMessage) != (kind != app.PlaceRoom) && ev != app.EventNotification {
							// A post happens in a room, a message in a conversation.
							continue
						}
						if ev == app.EventNotification && kind != app.PlaceRoom {
							continue
						}
						for _, mention := range []bool{false, true} {
							level := string(g)
							if p != app.PlaceNotifyDefault {
								level = string(p)
							} else if kind != app.PlaceRoom && g != app.NotifyNothing {
								level = "all"
							}
							want := !muted && covers(level, ev, mention)
							got := app.RingFor(store.UserSettings{Notify: g, Sound: true, SoundName: store.SoundBell},
								store.PlacePref{Notify: p, Sound: store.PlaceSoundDefault, Muted: muted}, kind, ev, mention)
							n++
							if got.Rings != want {
								t.Errorf("global %s, place %s, muted %v, kind %d, event %d, mention %v: rings %v, want %v",
									g, p, muted, kind, ev, mention, got.Rings, want)
							}
							if got.Rings && got.Sound != "bell" || !got.Rings && (got.Sound != "" || got.Browser) {
								t.Errorf("global %s, place %s, muted %v: outcome %+v is inconsistent", g, p, muted, got)
							}
						}
					}
				}
			}
		}
	}
	if n < 150 {
		t.Errorf("only %d combinations were checked", n)
	}
}

func TestThreadTitle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		body, want string
	}{
		{"Feeder schedule", "Feeder schedule"},
		{"\n\n  # Feeder schedule\nmore", "Feeder schedule"},
		{"> - quoted item", "quoted item"},
		{"", ""},
		{"   \n  ", ""},
		{strings.Repeat("a", 40), strings.Repeat("a", 40)},
		{strings.Repeat("a", 41), strings.Repeat("a", 40) + "…"},
		{strings.Repeat("é", 50), strings.Repeat("é", 40) + "…"},
	}
	for _, tt := range tests {
		if got := app.ThreadTitle(tt.body); got != tt.want {
			t.Errorf("ThreadTitle(%q) = %q, want %q", tt.body, got, tt.want)
		}
	}
}

// placeCases are the places of the permission tables, with the callers' result
// in each: "" for success, else the tag.
func TestPrefsPermissions(t *testing.T) {
	t.Parallel()
	places := []struct {
		name string
		ref  func(e *prefsEnv) app.PlaceRef
		// want is the tag by caller; a caller that is not listed succeeds.
		want map[string]string
	}{
		{"visible room (lobby: admin, bob, carol)", func(e *prefsEnv) app.PlaceRef { return app.RoomRef(e.lobby.ID) },
			map[string]string{"alice": app.TagForbidden, "dave": app.TagForbidden, "dina": app.TagUnauthenticated}},
		{"private room (den: alice, bob)", func(e *prefsEnv) app.PlaceRef { return app.RoomRef(e.den.ID) },
			map[string]string{"admin": app.TagNotFound, "carol": app.TagNotFound, "dave": app.TagNotFound, "dina": app.TagUnauthenticated}},
		{"group conversation (alice, bob, carol)", func(e *prefsEnv) app.PlaceRef { return app.ConversationRef(e.crew) },
			map[string]string{"admin": app.TagNotFound, "dave": app.TagNotFound, "dina": app.TagUnauthenticated}},
		{"direct conversation (alice, bob)", func(e *prefsEnv) app.PlaceRef { return app.ConversationRef(e.pair) },
			map[string]string{"admin": app.TagNotFound, "carol": app.TagNotFound, "dave": app.TagNotFound, "dina": app.TagUnauthenticated}},
	}
	callers := []string{"admin", "alice", "bob", "carol", "dave", "dina"}
	for _, pl := range places {
		for _, who := range callers {
			t.Run(pl.name+"/"+who, func(t *testing.T) {
				t.Parallel()
				e := newPrefsEnv(t)
				ref := pl.ref(e)
				wantTag := pl.want[who]
				_, getErr := e.prefs.Place(t.Context(), e.as(who), ref)
				_, setErr := e.prefs.UpdatePlace(t.Context(), e.as(who), ref, app.PlaceUpdate{Muted: ptr(true)})
				if wantTag == "" {
					if getErr != nil || setErr != nil {
						t.Fatalf("Place = %v, UpdatePlace = %v; want success", getErr, setErr)
					}
					return
				}
				_ = requireTag(t, getErr, wantTag, "Prefs.Place->Prefs.checkPlace")
				_ = requireTag(t, setErr, wantTag, "Prefs.UpdatePlace.func1->Prefs.checkPlace")
				if rows, err := e.st.PlacePrefsOfUser(t.Context(), e.uid(who)); err != nil || len(rows) != 0 {
					t.Errorf("a refused change saved %d rows, %v", len(rows), err)
				}
				if ev := e.userEvents(who, live.KindPlacePrefsChanged); len(ev) != 0 {
					t.Errorf("a refused change published %+v", ev)
				}
			})
		}
	}
}

func TestPrefsPlacesPermissions(t *testing.T) {
	t.Parallel()
	for _, who := range []string{"admin", "alice", "dina"} {
		t.Run(who, func(t *testing.T) {
			t.Parallel()
			e := newPrefsEnv(t)
			_, err := e.prefs.Places(t.Context(), e.as(who))
			if who == "dina" {
				_ = requireTag(t, err, app.TagUnauthenticated, "Prefs.Places.Unauthenticated")
				return
			}
			if err != nil {
				t.Fatalf("Places: %+v", err)
			}
		})
	}
	e := newPrefsEnv(t)
	_, err := e.prefs.Places(t.Context(), app.Actor{UserID: 9999})
	_ = requireTag(t, err, app.TagUnauthenticated, "Prefs.Places.Unauthenticated")
}

func TestUpdatePlace(t *testing.T) {
	t.Parallel()
	t.Run("defaults, then changes, then back to the defaults", func(t *testing.T) {
		t.Parallel()
		e := newPrefsEnv(t)
		ref := app.ConversationRef(e.crew)
		got, err := e.prefs.Place(t.Context(), e.as("bob"), ref)
		if err != nil {
			t.Fatal(err)
		}
		want := app.PlacePrefs{Place: ref, Notify: app.PlaceNotifyDefault, Sound: app.PlaceSoundDefault, MarkUnread: app.MarkUnreadAll}
		if got != want {
			t.Fatalf("defaults = %+v, want %+v", got, want)
		}
		got, err = e.prefs.UpdatePlace(t.Context(), e.as("bob"), ref, app.PlaceUpdate{Notify: ptr(app.PlaceNotifyMentions), Muted: ptr(true), IgnoreGroupMentions: ptr(true)})
		if err != nil {
			t.Fatal(err)
		}
		want.Notify, want.Muted, want.IgnoreGroupMentions = app.PlaceNotifyMentions, true, true
		if got != want {
			t.Fatalf("after the change = %+v, want %+v", got, want)
		}
		// Unmuting brings back the other choices as they were.
		if got, err = e.prefs.UpdatePlace(t.Context(), e.as("bob"), ref, app.PlaceUpdate{Muted: ptr(false)}); err != nil {
			t.Fatal(err)
		}
		want.Muted = false
		if got != want {
			t.Fatalf("after unmuting = %+v, want %+v", got, want)
		}
		places, err := e.prefs.Places(t.Context(), e.as("bob"))
		if err != nil || len(places) != 1 || places[0].Place != ref || places[0].Notify != app.PlaceNotifyMentions {
			t.Fatalf("Places = %+v, %v; want the one group", places, err)
		}
		ev := e.userEvents("bob", live.KindPlacePrefsChanged)
		if len(ev) != 2 || ev[0].ConversationID != e.crew || ev[0].RoomID != 0 || ev[0].UserID != e.uid("bob") {
			t.Fatalf("events = %+v, want two place_prefs.changed of the group on bob's topic", ev)
		}
		// Nothing changes: nothing is published.
		e.rec.Reset()
		if _, err := e.prefs.UpdatePlace(t.Context(), e.as("bob"), ref, app.PlaceUpdate{Muted: ptr(false)}); err != nil {
			t.Fatal(err)
		}
		if ev := e.rec.Events(); len(ev) != 0 {
			t.Errorf("an unchanged preference published %+v", ev)
		}
		// All defaults again: the row is gone.
		e.set("bob", ref, app.PlaceUpdate{Notify: ptr(app.PlaceNotifyDefault), IgnoreGroupMentions: ptr(false)})
		if rows, _ := e.st.PlacePrefsOfUser(t.Context(), e.uid("bob")); len(rows) != 0 {
			t.Errorf("%d rows after the defaults came back, want none", len(rows))
		}
		// Other members are untouched.
		if got, _ := e.prefs.Place(t.Context(), e.as("alice"), ref); got != noPrefs(ref) {
			t.Errorf("alice's preferences = %+v", got)
		}
	})
	t.Run("a room keeps its mark as unread", func(t *testing.T) {
		t.Parallel()
		e := newPrefsEnv(t)
		got, err := e.prefs.UpdatePlace(t.Context(), e.as("bob"), app.RoomRef(e.den.ID), app.PlaceUpdate{MarkUnread: ptr(app.MarkUnreadMentions), Sound: ptr(app.PlaceSoundOff)})
		if err != nil || got.MarkUnread != app.MarkUnreadMentions || got.Sound != app.PlaceSoundOff {
			t.Fatalf("UpdatePlace = %+v, %v", got, err)
		}
	})
	failures := []struct {
		name   string
		ref    func(e *prefsEnv) app.PlaceRef
		update app.PlaceUpdate
		field  string
	}{
		{"unknown notify", func(e *prefsEnv) app.PlaceRef { return app.RoomRef(e.den.ID) }, app.PlaceUpdate{Notify: ptr(app.PlaceNotify("some"))}, app.FieldPlaceNotify},
		{"unknown sound", func(e *prefsEnv) app.PlaceRef { return app.RoomRef(e.den.ID) }, app.PlaceUpdate{Sound: ptr(app.PlaceSound("loud"))}, app.FieldPlaceSound},
		{"unknown mark unread", func(e *prefsEnv) app.PlaceRef { return app.RoomRef(e.den.ID) }, app.PlaceUpdate{MarkUnread: ptr(app.MarkUnread("none"))}, app.FieldMarkUnread},
		{"mark unread in a conversation", func(e *prefsEnv) app.PlaceRef { return app.ConversationRef(e.crew) }, app.PlaceUpdate{MarkUnread: ptr(app.MarkUnreadMentions)}, app.FieldMarkUnread},
		{"group mentions in a direct conversation", func(e *prefsEnv) app.PlaceRef { return app.ConversationRef(e.pair) }, app.PlaceUpdate{IgnoreGroupMentions: ptr(true)}, app.FieldIgnoreGroupMentions},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newPrefsEnv(t)
			_, err := e.prefs.UpdatePlace(t.Context(), e.as("bob"), tt.ref(e), tt.update)
			appErr := requireTag(t, err, app.TagInvalidInput, "Prefs.UpdatePlace.func1.InvalidInput")
			if f := fieldsOf(t, appErr); f[tt.field] == "" {
				t.Errorf("fields = %v, want %s", f, tt.field)
			}
			if rows, _ := e.st.PlacePrefsOfUser(t.Context(), e.uid("bob")); len(rows) != 0 {
				t.Errorf("a refused change saved %d rows", len(rows))
			}
			if ev := e.rec.Events(); len(ev) != 0 {
				t.Errorf("a refused change published %+v", ev)
			}
		})
	}
}

// noPrefs is the preferences of a place nobody changed.
func noPrefs(ref app.PlaceRef) app.PlacePrefs {
	return app.PlacePrefs{Place: ref, Notify: app.PlaceNotifyDefault, Sound: app.PlaceSoundDefault, MarkUnread: app.MarkUnreadAll}
}

func TestPrefsEndWhenTheUserLeaves(t *testing.T) {
	t.Parallel()
	count := func(e *prefsEnv, user string) int {
		rows, err := e.st.PlacePrefsOfUser(e.t.Context(), e.uid(user))
		if err != nil {
			e.t.Fatal(err)
		}
		return len(rows)
	}
	t.Run("leaving a room", func(t *testing.T) {
		t.Parallel()
		e := newPrefsEnv(t)
		e.set("bob", app.RoomRef(e.den.ID), app.PlaceUpdate{Muted: ptr(true)})
		e.set("bob", app.RoomRef(e.lobby.ID), app.PlaceUpdate{Muted: ptr(true)})
		e.set("alice", app.RoomRef(e.den.ID), app.PlaceUpdate{Muted: ptr(true)})
		if err := e.rooms.Leave(t.Context(), e.as("bob"), e.den.ID); err != nil {
			t.Fatal(err)
		}
		if n := count(e, "bob"); n != 1 {
			t.Errorf("bob has %d rows, want only the lobby's", n)
		}
		if n := count(e, "alice"); n != 1 {
			t.Errorf("alice has %d rows, want hers untouched", n)
		}
		// Joining again starts from the defaults.
		if err := e.rooms.AddMember(t.Context(), e.as("alice"), e.den.ID, e.uid("bob")); err != nil {
			t.Fatal(err)
		}
		if got, err := e.prefs.Place(t.Context(), e.as("bob"), app.RoomRef(e.den.ID)); err != nil || got.Muted {
			t.Errorf("after rejoining = %+v, %v; want the defaults", got, err)
		}
	})
	t.Run("being removed from a room", func(t *testing.T) {
		t.Parallel()
		e := newPrefsEnv(t)
		e.set("bob", app.RoomRef(e.den.ID), app.PlaceUpdate{Muted: ptr(true)})
		if err := e.rooms.RemoveMember(t.Context(), e.as("admin"), e.den.ID, e.uid("bob")); err != nil {
			t.Fatal(err)
		}
		if n := count(e, "bob"); n != 0 {
			t.Errorf("bob has %d rows after being removed, want none", n)
		}
	})
	t.Run("leaving a group", func(t *testing.T) {
		t.Parallel()
		e := newPrefsEnv(t)
		e.set("carol", app.ConversationRef(e.crew), app.PlaceUpdate{Muted: ptr(true)})
		e.set("bob", app.ConversationRef(e.crew), app.PlaceUpdate{Muted: ptr(true)})
		if err := e.msgs.Leave(t.Context(), e.as("carol"), e.crew); err != nil {
			t.Fatal(err)
		}
		if n := count(e, "carol"); n != 0 {
			t.Errorf("carol has %d rows after leaving, want none", n)
		}
		if n := count(e, "bob"); n != 1 {
			t.Errorf("bob has %d rows, want his untouched", n)
		}
	})
	t.Run("a failed leave keeps the preferences", func(t *testing.T) {
		t.Parallel()
		e := newPrefsEnv(t)
		e.set("bob", app.ConversationRef(e.pair), app.PlaceUpdate{Muted: ptr(true)})
		if err := e.msgs.Leave(t.Context(), e.as("bob"), e.pair); err == nil {
			t.Fatal("bob left a direct conversation")
		}
		if n := count(e, "bob"); n != 1 {
			t.Errorf("bob has %d rows after a refused leave, want 1", n)
		}
	})
}

// eventsText is the text of the events, for tests that look for what must
// not be in them.
func eventsText(events []live.Event) string {
	return fmt.Sprintf("%+v", events)
}

func TestPostsRingTheMembersWhoChoseAll(t *testing.T) {
	t.Parallel()
	const body = "SECRET-BODY-OF-THE-POST"
	e := newPrefsEnv(t)
	// bob: global All. carol: the lobby set to All, with the sound off there.
	// admin writes. bob's lobby is muted in a second step.
	e.settings("bob", app.SettingsUpdate{Notify: ptr(app.NotifyAll), SoundName: ptr(app.SoundMeow), BrowserNotifications: ptr(true)})
	e.set("carol", app.RoomRef(e.lobby.ID), app.PlaceUpdate{Notify: ptr(app.PlaceNotifyAll), Sound: ptr(app.PlaceSoundOff)})
	e.rec.Reset()
	post, err := e.posts.Create(t.Context(), e.as("admin"), e.lobby.ID, app.PostInput{Format: app.FormatText, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	bob := e.userEvents("bob", live.KindPostCreated)
	carol := e.userEvents("carol", live.KindPostCreated)
	if len(bob) != 1 || !bob[0].Ring || bob[0].Sound != "meow" || !bob[0].Browser || bob[0].PostID != post.ID || bob[0].RoomID != e.lobby.ID {
		t.Errorf("bob's events = %+v, want one ringing with meow and the browser", bob)
	}
	if len(carol) != 1 || !carol[0].Ring || carol[0].Sound != "" || carol[0].Browser {
		t.Errorf("carol's events = %+v, want one ringing without a sound", carol)
	}
	if ev := e.userEvents("admin", live.KindPostCreated); len(ev) != 0 {
		t.Errorf("the author was told: %+v", ev)
	}
	if ev := e.userEvents("alice", live.KindPostCreated); len(ev) != 0 {
		t.Errorf("alice is no member of the lobby: %+v", ev)
	}
	if strings.Contains(eventsText(e.rec.Events()), body) {
		t.Error("the body of the post is in an event")
	}

	// Muted: no event at all.
	e.set("bob", app.RoomRef(e.lobby.ID), app.PlaceUpdate{Muted: ptr(true)})
	e.rec.Reset()
	if _, err := e.posts.Create(t.Context(), e.as("admin"), e.lobby.ID, app.PostInput{Format: app.FormatText, Body: "again"}); err != nil {
		t.Fatal(err)
	}
	if ev := e.userEvents("bob", live.KindPostCreated); len(ev) != 0 {
		t.Errorf("a muted room rang: %+v", ev)
	}
	if ev := e.userEvents("carol", live.KindPostCreated); len(ev) != 1 {
		t.Errorf("carol's events = %+v, want one", ev)
	}

	// A comment is not a post.
	e.rec.Reset()
	if _, err := e.posts.Reply(t.Context(), e.as("admin"), post.ID, app.PostInput{Format: app.FormatText, Body: "comment"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bob", "carol"} {
		if ev := e.userEvents(name, live.KindPostCreated); len(ev) != 0 {
			t.Errorf("a comment rang %s: %+v", name, ev)
		}
	}
}

func TestAMentionInAMutedRoomReachesTheBellWithoutRinging(t *testing.T) {
	t.Parallel()
	e := newPrefsEnv(t)
	e.settings("bob", app.SettingsUpdate{Notify: ptr(app.NotifyAll)})
	e.set("bob", app.RoomRef(e.lobby.ID), app.PlaceUpdate{Muted: ptr(true)})
	e.set("carol", app.RoomRef(e.lobby.ID), app.PlaceUpdate{Notify: ptr(app.PlaceNotifyNothing)})
	for _, name := range []string{"bob", "carol"} {
		if err := e.notes.MarkRead(t.Context(), e.as(name), 1<<40); err != nil {
			t.Fatal(err)
		}
	}
	e.rec.Reset()
	if _, err := e.posts.Create(t.Context(), e.as("admin"), e.lobby.ID, app.PostInput{Format: app.FormatText, Body: "@bob @carol hello"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bob", "carol"} {
		evs := e.userEvents(name, live.KindNotificationCreated)
		if len(evs) != 1 || evs[0].Ring || evs[0].Sound != "" || evs[0].Browser || evs[0].NotificationID == 0 {
			t.Errorf("%s: events = %+v, want one notification.created that does not ring", name, evs)
		}
		page, err := e.notes.List(t.Context(), e.as(name), 0, 0)
		if err != nil || len(page.Notifications) == 0 || page.Notifications[0].Kind != app.NotificationMention {
			t.Errorf("%s: bell = %+v, %v; want the mention", name, page.Notifications, err)
		}
		if n, err := e.notes.UnreadCount(t.Context(), e.as(name)); err != nil || n != 1 {
			t.Errorf("%s: unread = %d, %v; want 1", name, n, err)
		}
		if ev := e.userEvents(name, live.KindPostCreated); len(ev) != 0 {
			t.Errorf("%s: also heard of the post: %+v", name, ev)
		}
	}
	// An unmuted mention rings once, not twice.
	e.set("bob", app.RoomRef(e.lobby.ID), app.PlaceUpdate{Muted: ptr(false)})
	e.rec.Reset()
	if _, err := e.posts.Create(t.Context(), e.as("admin"), e.lobby.ID, app.PostInput{Format: app.FormatText, Body: "@bob again"}); err != nil {
		t.Fatal(err)
	}
	if ev := e.userEvents("bob", live.KindNotificationCreated); len(ev) != 1 || !ev[0].Ring || ev[0].Sound != "purr" {
		t.Errorf("bob's notification = %+v, want it to ring with purr", ev)
	}
	if ev := e.userEvents("bob", live.KindPostCreated); len(ev) != 0 {
		t.Errorf("bob also heard of the post, so it rings twice: %+v", ev)
	}
}

func TestMessagesRingByTheChoiceOfTheConversation(t *testing.T) {
	t.Parallel()
	const body = "SECRET-BODY-OF-THE-MESSAGE"
	e := newPrefsEnv(t)
	ref := app.ConversationRef(e.crew)
	e.set("bob", ref, app.PlaceUpdate{Notify: ptr(app.PlaceNotifyMentions)})
	e.set("carol", ref, app.PlaceUpdate{Muted: ptr(true)})
	send := func(text string) app.Message {
		t.Helper()
		e.rec.Reset()
		m, err := e.msgs.Send(t.Context(), e.as("alice"), e.crew, app.MessageInput{Body: text})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	m := send(body)
	if ev := e.userEvents("bob", live.KindMessageCreated); len(ev) != 0 {
		t.Errorf("bob (mentions) heard a message without a mention: %+v", ev)
	}
	if ev := e.userEvents("carol", live.KindMessageCreated); len(ev) != 0 {
		t.Errorf("carol (muted) heard the message: %+v", ev)
	}
	if ev := e.userEvents("alice", live.KindMessageCreated); len(ev) != 0 {
		t.Errorf("the author heard her own message: %+v", ev)
	}
	// The members still get the conversation's own events.
	if ev := e.userEvents("carol", live.KindConversationChanged); len(ev) != 1 {
		t.Errorf("carol's conversation.changed = %+v, want one", ev)
	}
	if strings.Contains(eventsText(e.rec.Events()), body) {
		t.Error("the body of the message is in an event")
	}
	_ = m

	m = send("hello @Bob")
	ev := e.userEvents("bob", live.KindMessageCreated)
	if len(ev) != 1 || !ev[0].Ring || ev[0].Sound != "purr" || ev[0].MessageID != m.ID || ev[0].ConversationID != e.crew {
		t.Errorf("bob's events = %+v, want one ringing for the mention", ev)
	}
	if ev := e.userEvents("carol", live.KindMessageCreated); len(ev) != 0 {
		t.Errorf("carol (muted) heard a mention: %+v", ev)
	}

	// A direct conversation rings by default; the global Nothing silences it
	// unless the conversation says otherwise.
	e.rec.Reset()
	if _, err := e.msgs.Send(t.Context(), e.as("alice"), e.pair, app.MessageInput{Body: "psst"}); err != nil {
		t.Fatal(err)
	}
	if ev := e.userEvents("bob", live.KindMessageCreated); len(ev) != 1 || !ev[0].Ring {
		t.Errorf("a direct message did not ring: %+v", ev)
	}
	e.settings("bob", app.SettingsUpdate{Notify: ptr(app.NotifyNothing)})
	e.rec.Reset()
	if _, err := e.msgs.Send(t.Context(), e.as("alice"), e.pair, app.MessageInput{Body: "psst"}); err != nil {
		t.Fatal(err)
	}
	if ev := e.userEvents("bob", live.KindMessageCreated); len(ev) != 0 {
		t.Errorf("a direct message rang with the global Nothing: %+v", ev)
	}
	e.set("bob", app.ConversationRef(e.pair), app.PlaceUpdate{Notify: ptr(app.PlaceNotifyAll)})
	e.rec.Reset()
	if _, err := e.msgs.Send(t.Context(), e.as("alice"), e.pair, app.MessageInput{Body: "psst"}); err != nil {
		t.Fatal(err)
	}
	if ev := e.userEvents("bob", live.KindMessageCreated); len(ev) != 1 {
		t.Errorf("a conversation set to All did not ring with the global Nothing: %+v", ev)
	}
}

func TestAMutedConversationLeavesTheUnreadCount(t *testing.T) {
	t.Parallel()
	e := newPrefsEnv(t)
	if _, err := e.msgs.Send(t.Context(), e.as("alice"), e.crew, app.MessageInput{Body: "one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.msgs.Send(t.Context(), e.as("alice"), e.pair, app.MessageInput{Body: "two"}); err != nil {
		t.Fatal(err)
	}
	count := func(user string) int {
		n, err := e.msgs.UnreadCount(t.Context(), e.as(user))
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count("bob"); n != 2 {
		t.Fatalf("bob's unread conversations = %d, want 2", n)
	}
	e.set("bob", app.ConversationRef(e.crew), app.PlaceUpdate{Muted: ptr(true)})
	if n := count("bob"); n != 1 {
		t.Errorf("bob's unread conversations = %d once the group is muted, want 1", n)
	}
	if n := count("carol"); n != 1 {
		t.Errorf("carol's unread conversations = %d, want 1 (bob's mute is bob's)", n)
	}
	list, err := e.msgs.Conversations(t.Context(), e.as("bob"))
	if err != nil || len(list) != 2 || !list[1].Muted || list[0].Muted || list[1].ID != e.crew {
		t.Errorf("Conversations = %+v, %v; want the muted group last", list, err)
	}
	e.set("bob", app.ConversationRef(e.crew), app.PlaceUpdate{Muted: ptr(false)})
	if n := count("bob"); n != 2 {
		t.Errorf("bob's unread conversations = %d once unmuted, want 2", n)
	}
}

func TestMineMovesMutedRoomsToTheEnd(t *testing.T) {
	t.Parallel()
	e := newPrefsEnv(t)
	names := func() []string {
		rooms, err := e.rooms.Mine(t.Context(), e.as("bob"))
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range rooms {
			out = append(out, fmt.Sprintf("%s:%v", r.Name, r.Muted))
		}
		return out
	}
	if got := names(); !slices.Equal(got, []string{"den:false", "lobby:false"}) {
		t.Fatalf("rooms = %v", got)
	}
	e.set("bob", app.RoomRef(e.den.ID), app.PlaceUpdate{Muted: ptr(true)})
	if got := names(); !slices.Equal(got, []string{"lobby:false", "den:true"}) {
		t.Errorf("rooms after muting den = %v, want it last", got)
	}
}

func TestNotice(t *testing.T) {
	t.Parallel()
	e := newPrefsEnv(t)
	e.settings("bob", app.SettingsUpdate{Notify: ptr(app.NotifyAll), BrowserNotifications: ptr(true)})
	long := strings.Repeat("x", 60)
	root, err := e.posts.Create(t.Context(), e.as("bob"), e.lobby.ID, app.PostInput{Format: app.FormatMarkdown, Body: "# Feeder schedule\nsecond line"})
	if err != nil {
		t.Fatal(err)
	}
	longRoot, err := e.posts.Create(t.Context(), e.as("bob"), e.lobby.ID, app.PostInput{Format: app.FormatText, Body: long})
	if err != nil {
		t.Fatal(err)
	}
	e.rec.Reset()
	if _, err := e.posts.Reply(t.Context(), e.as("admin"), root.ID, app.PostInput{Format: app.FormatText, Body: "answer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.posts.Reply(t.Context(), e.as("admin"), longRoot.ID, app.PostInput{Format: app.FormatText, Body: "answer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.posts.Create(t.Context(), e.as("admin"), e.lobby.ID, app.PostInput{Format: app.FormatText, Body: "@bob mention", Tasks: []app.TaskInput{{Title: "Review the retry", Assignees: []uint64{e.uid("bob")}}}}); err != nil {
		t.Fatal(err)
	}
	if err := e.rooms.AddMember(t.Context(), e.as("alice"), e.den.ID, e.uid("carol")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.posts.Create(t.Context(), e.as("admin"), e.lobby.ID, app.PostInput{Format: app.FormatText, Body: "plain news"}); err != nil {
		t.Fatal(err)
	}
	notices := func(user string, kind string) []app.Notice {
		t.Helper()
		var out []app.Notice
		for _, ev := range e.userEvents(user, kind) {
			n, ok, err := e.prefs.Notice(t.Context(), e.as(user), ev)
			if err != nil {
				t.Fatalf("Notice: %+v", err)
			}
			if ok {
				out = append(out, n)
			}
		}
		return out
	}
	var texts []string
	for _, n := range notices("bob", live.KindNotificationCreated) {
		texts = append(texts, n.Text)
		if !n.Ring || !n.Browser || n.Sound != app.SoundPurr || n.PlaceName != "lobby" || n.Author.DisplayName != "Admin" {
			t.Errorf("notice = %+v", n)
		}
	}
	want := []string{
		"Admin replied in «Feeder schedule» in #lobby",
		"Admin replied in «" + strings.Repeat("x", 40) + "…» in #lobby",
		"Admin assigned you «Review the retry» in #lobby",
	}
	if !slices.Equal(texts, want) {
		t.Errorf("notification texts =\n %q\nwant\n %q", texts, want)
	}
	if got := notices("carol", live.KindNotificationCreated); len(got) != 1 || got[0].Text != "Alice added you to #den" || got[0].NotificationKind != app.NotificationAddedToRoom || got[0].RoomID != e.den.ID {
		t.Errorf("carol's notices = %+v, want Alice added you to #den", got)
	}
	if got := notices("bob", live.KindPostCreated); len(got) != 1 || got[0].Text != "Admin posted in #lobby" || got[0].ID[0] != 'p' {
		t.Errorf("bob's post notices = %+v", got)
	}

	postEv := e.userEvents("bob", live.KindPostCreated)[0]
	notifEv := e.userEvents("bob", live.KindNotificationCreated)[0]

	// Messages.
	e.rec.Reset()
	if _, err := e.msgs.Send(t.Context(), e.as("alice"), e.pair, app.MessageInput{Body: "SECRET"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.msgs.Send(t.Context(), e.as("alice"), e.crew, app.MessageInput{Body: "SECRET"}); err != nil {
		t.Fatal(err)
	}
	var messageTexts []string
	for _, n := range notices("bob", live.KindMessageCreated) {
		messageTexts = append(messageTexts, n.Text)
		if strings.Contains(fmt.Sprintf("%+v", n), "SECRET") {
			t.Errorf("the body is in the notice %+v", n)
		}
	}
	if !slices.Equal(messageTexts, []string{"Alice sent you a message", "Alice wrote in Feeder team"}) {
		t.Errorf("message texts = %q", messageTexts)
	}

	// Gone: a deleted post, and a reader who left.
	gone := postEv
	gone.PostID = 99999
	if _, ok, err := e.prefs.Notice(t.Context(), e.as("bob"), gone); err != nil || ok {
		t.Errorf("a missing post: ok %v, %v; want no notice and no error", ok, err)
	}
	gone = notifEv
	gone.NotificationID = 99999
	if _, ok, err := e.prefs.Notice(t.Context(), e.as("bob"), gone); err != nil || ok {
		t.Errorf("a missing notification: ok %v, %v; want no notice and no error", ok, err)
	}
	msg := e.userEvents("bob", live.KindMessageCreated)[0]
	if err := e.rooms.Leave(t.Context(), e.as("bob"), e.lobby.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := e.prefs.Notice(t.Context(), e.as("bob"), postEv); err != nil || ok {
		t.Errorf("a post of a room bob left: ok %v, %v; want no notice", ok, err)
	}
	if _, ok, err := e.prefs.Notice(t.Context(), e.as("carol"), msg); err != nil || ok {
		t.Errorf("a message of someone else's conversation: ok %v, %v; want no notice", ok, err)
	}
	if _, _, err := e.prefs.Notice(t.Context(), app.Actor{UserID: 9999}, msg); err == nil {
		t.Error("an unknown user got a notice")
	} else {
		_ = requireTag(t, err, app.TagUnauthenticated, "Prefs.Notice.Unauthenticated")
	}
}

func TestUpdateSettingsNotificationFields(t *testing.T) {
	t.Parallel()
	e := newPrefsEnv(t)
	got, err := e.svc.UpdateSettings(t.Context(), e.as("bob"), app.SettingsUpdate{
		Notify: ptr(app.NotifyNothing), SoundName: ptr(app.SoundBell), Replies: ptr(app.RepliesDirect),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Notify != app.NotifyNothing || got.SoundName != app.SoundBell || got.Replies != app.RepliesDirect || !got.Sound || got.Theme != app.ThemeSystem {
		t.Errorf("settings = %+v", got)
	}
	if ev := e.userEvents("bob", live.KindSettingsChanged); len(ev) != 1 {
		t.Errorf("events = %+v, want one settings.changed", ev)
	}
	failures := []struct {
		name   string
		update app.SettingsUpdate
		field  string
	}{
		{"unknown notify", app.SettingsUpdate{Notify: ptr(app.NotifyLevel("some"))}, app.FieldNotify},
		{"unknown sound", app.SettingsUpdate{SoundName: ptr(app.SoundName("moo"))}, app.FieldSoundName},
		{"unknown replies", app.SettingsUpdate{Replies: ptr(app.RepliesMode("some"))}, app.FieldReplies},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			e.rec.Reset()
			_, err := e.svc.UpdateSettings(t.Context(), e.as("bob"), tt.update)
			appErr := requireTag(t, err, app.TagInvalidInput, "Accounts.UpdateSettings.InvalidInput")
			if f := fieldsOf(t, appErr); f[tt.field] == "" {
				t.Errorf("fields = %v, want %s", f, tt.field)
			}
			if ev := e.rec.Events(); len(ev) != 0 {
				t.Errorf("a refused change published %+v", ev)
			}
		})
	}
}
