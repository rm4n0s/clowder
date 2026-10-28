package api

import (
	"net/http"
	"strings"
	"testing"
)

// defaultPrefs are the notification preferences of a place nobody changed.
func defaultPrefs() map[string]any {
	return map[string]any{
		"notify": "default", "sound": "default", "ignore_group_mentions": false, "mark_unread": "all", "muted": false,
	}
}

// prefsWith returns the defaults with the changes.
func prefsWith(kv ...any) map[string]any {
	out := defaultPrefs()
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

func TestNotificationPrefsRoutes(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	group := f.convs("bob", "alice", "carol")
	direct := f.convs("alice", "bob")

	// who reads and changes the preferences of which place.
	tests := []struct {
		name   string
		user   string
		place  string
		status int
	}{
		{"a member of a visible room", "bob", path("/rooms/%d", f.lobby.ID), http.StatusOK},
		{"an admin who is a member", "admin", path("/rooms/%d", f.lobby.ID), http.StatusOK},
		{"a member of a private room", "alice", path("/rooms/%d", f.den.ID), http.StatusOK},
		{"a visible room that was not joined", "alice", path("/rooms/%d", f.lobby.ID), http.StatusForbidden},
		{"an admin of a visible room that was not joined", "root", path("/rooms/%d", f.lobby.ID), http.StatusForbidden},
		{"a private room of others", "carol", path("/rooms/%d", f.den.ID), http.StatusNotFound},
		{"an admin outside a private room", "admin", path("/rooms/%d", f.den.ID), http.StatusNotFound},
		{"a room that does not exist", "bob", "/api/v1/rooms/999999", http.StatusNotFound},
		{"a member of a group", "carol", path("/conversations/%d", group), http.StatusOK},
		{"a member of a direct conversation", "bob", path("/conversations/%d", direct), http.StatusOK},
		{"outside a conversation", "carol", path("/conversations/%d", direct), http.StatusNotFound},
		{"an admin outside a conversation", "admin", path("/conversations/%d", group), http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := tt.place + "/notification-prefs"
			got := f.call(tt.user, http.MethodGet, url, nil)
			requireStatus(t, got, tt.status)
			put := f.call(tt.user, http.MethodPut, url, map[string]any{"muted": true})
			requireStatus(t, put, tt.status)
			if tt.status == http.StatusOK {
				sameJSON(t, "GET", decode(t, got), defaultPrefs())
				sameJSON(t, "PUT", decode(t, put), prefsWith("muted", true))
				sameJSON(t, "GET after PUT", decode(t, f.call(tt.user, http.MethodGet, url, nil)), prefsWith("muted", true))
			} else if errorCode(t, put) == "" {
				t.Error("the error has no code")
			}
		})
	}
}

func TestNotificationPrefsChanges(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	group := f.convs("bob", "alice", "carol")
	direct := f.convs("alice", "bob")
	room := path("/rooms/%d", f.lobby.ID) + "/notification-prefs"
	conv := path("/conversations/%d", group) + "/notification-prefs"
	pair := path("/conversations/%d", direct) + "/notification-prefs"

	tests := []struct {
		name   string
		url    string
		body   any
		status int
		want   map[string]any
		field  string
	}{
		{"a group to mentions", conv, map[string]any{"notify": "mentions"}, 200, prefsWith("notify", "mentions"), ""},
		{"the sound and the group mentions", conv, map[string]any{"sound": "off", "ignore_group_mentions": true}, 200,
			prefsWith("notify", "mentions", "sound", "off", "ignore_group_mentions", true), ""},
		{"nulls keep the values", conv, `{"notify": null, "muted": true}`, 200,
			prefsWith("notify", "mentions", "sound", "off", "ignore_group_mentions", true, "muted", true), ""},
		{"unmuting keeps the choices", conv, map[string]any{"muted": false}, 200,
			prefsWith("notify", "mentions", "sound", "off", "ignore_group_mentions", true), ""},
		{"back to the defaults", conv, map[string]any{"notify": "default", "sound": "default", "ignore_group_mentions": false}, 200, defaultPrefs(), ""},
		{"a room counts mentions only", room, map[string]any{"mark_unread": "mentions"}, 200, prefsWith("mark_unread", "mentions"), ""},
		{"a room to nothing", room, map[string]any{"notify": "nothing", "muted": true}, 200, prefsWith("mark_unread", "mentions", "notify", "nothing", "muted", true), ""},
		{"an unknown notify", room, map[string]any{"notify": "some"}, 422, nil, "notify"},
		{"an unknown sound", room, map[string]any{"sound": "loud"}, 422, nil, "sound"},
		{"an unknown mark", room, map[string]any{"mark_unread": "none"}, 422, nil, "mark_unread"},
		{"a mark for a conversation", conv, map[string]any{"mark_unread": "mentions"}, 422, nil, "mark_unread"},
		{"group mentions in a direct conversation", pair, map[string]any{"ignore_group_mentions": true}, 422, nil, "ignore_group_mentions"},
		{"no preference", room, map[string]any{}, 422, nil, "muted"},
		{"only nulls", room, `{"muted": null}`, 422, nil, "notify"},
		{"a wrong type", room, `{"muted": "yes"}`, 422, nil, "muted"},
		{"an unknown field", room, map[string]any{"volume": 3}, 422, nil, "volume"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.call("bob", http.MethodPut, tt.url, tt.body)
			requireStatus(t, rec, tt.status)
			if tt.status != http.StatusOK {
				if _, ok := errorFields(t, rec)[tt.field]; !ok {
					t.Errorf("fields = %v, want %q", errorFields(t, rec), tt.field)
				}
				return
			}
			sameJSON(t, "answer", decode(t, rec), tt.want)
			sameJSON(t, "stored", decode(t, f.call("bob", http.MethodGet, tt.url, nil)), tt.want)
		})
	}
	// alice's own are the defaults still.
	sameJSON(t, "alice's group", decode(t, f.call("alice", http.MethodGet, conv, nil)), defaultPrefs())
}

func TestNotificationPrefsErrorRoutes(t *testing.T) {
	t.Parallel()
	err := checkPrefsPatch(placePrefsPatch{})
	_ = requireRoute(t, err, tagPrefsEmpty, "checkPrefsPatch.NotificationPrefsEmpty")
	if p, internal := problemOf(err); internal || p.status != http.StatusUnprocessableEntity {
		t.Errorf("problemOf = status %d internal %v, want 422", p.status, internal)
	}
}

// TestWSRingingEventsCarryNoBody checks the events of the user's own topic
// that ring: they say that they ring and with which sound, name the place and
// the author, and never hold the text of the post or the message.
func TestWSRingingEventsCarryNoBody(t *testing.T) {
	t.Parallel()
	w := newWSForum(t)
	alice := w.userID("alice")
	own := topicOf(topicUser, alice)
	group := w.convs("bob", "alice", "carol")
	c := w.dial("alice")

	// alice: notify about everything, the sound Chirp, browser notifications.
	w.ok("alice", http.MethodPatch, "/api/v1/me/settings", map[string]any{"notify": "all", "sound_name": "chirp", "browser_notifications": true}, http.StatusOK)
	c.await(own, "settings.changed")

	const secret = "SECRET-TEXT-OF-THE-MESSAGE"
	w.msg("bob", group, secret)
	ev := c.await(own, "message.created")
	if ev["ring"] != true || ev["sound"] != "chirp" || ev["browser"] != true || ev["conversation_id"] != float64(group) {
		t.Errorf("message.created = %v", ev)
	}
	data := obj(t, ev["data"])
	if text, _ := data["text"].(string); !strings.HasPrefix(text, "Bob wrote in ") {
		t.Errorf("message data = %v", data)
	}
	if strings.Contains(describeMessage(ev), secret) || strings.Contains(strings.Join(mapStrings(ev), " "), secret) {
		t.Errorf("the body is in the event %v", ev)
	}

	// A post that rings (the lobby is not alice's: bob's den is).
	w.ok("alice", http.MethodPut, path("/rooms/%d/notification-prefs", w.den.ID), map[string]any{"notify": "all", "sound": "off"}, http.StatusOK)
	c.await(own, "place_prefs.changed")
	const postSecret = "SECRET-TEXT-OF-THE-POST"
	w.write("bob", "den", postSecret)
	ev = c.await(own, "post.created")
	if ev["ring"] != true || ev["browser"] != true || ev["room_id"] != float64(w.den.ID) {
		t.Errorf("post.created = %v", ev)
	}
	if _, has := ev["sound"]; has {
		t.Errorf("post.created has a sound although the room's is off: %v", ev)
	}
	data = obj(t, ev["data"])
	if data["text"] != "Bob posted in #den" || data["place_name"] != "den" || obj(t, data["author"])["username"] != "bob" {
		t.Errorf("post data = %v", data)
	}
	if strings.Contains(strings.Join(mapStrings(ev), " "), postSecret) {
		t.Errorf("the body is in the event %v", ev)
	}

	// A mention rings with the notification; muted, it does not ring but the
	// notification still comes.
	w.ok("alice", http.MethodPut, path("/rooms/%d/notification-prefs", w.den.ID), map[string]any{"muted": true}, http.StatusOK)
	c.await(own, "place_prefs.changed")
	w.write("bob", "den", "@alice psst")
	ev = c.await(own, "notification.created")
	if _, ringing := ev["ring"]; ringing {
		t.Errorf("a mention in a muted room rings: %v", ev)
	}
	if obj(t, ev["data"])["kind"] != "mention" {
		t.Errorf("notification.created data = %v", ev["data"])
	}
}

// mapStrings lists every string in a decoded JSON value, keys and values.
func mapStrings(v any) []string {
	var out []string
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			out = append(out, k)
			out = append(out, mapStrings(e)...)
		}
	case []any:
		for _, e := range x {
			out = append(out, mapStrings(e)...)
		}
	case string:
		out = append(out, x)
	}
	return out
}
