package api

import (
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
)

// notificationsOf reads a page of the notifications of the user.
func (f *forum) notificationsOf(user, query string) map[string]any {
	f.t.Helper()
	return f.ok(user, http.MethodGet, "/api/v1/notifications"+query, nil, http.StatusOK)
}

// kindsOf returns the kinds of the notifications of an answer, newest first.
func kindsOf(t *testing.T, page map[string]any) []string {
	t.Helper()
	kinds := []string{}
	for _, n := range list(t, page["notifications"]) {
		kinds = append(kinds, obj(t, n)["kind"].(string))
	}
	return kinds
}

// notificationIDs returns the IDs of the notifications of an answer.
func notificationIDs(t *testing.T, page map[string]any) []uint64 {
	t.Helper()
	ids := []uint64{}
	for _, n := range list(t, page["notifications"]) {
		ids = append(ids, idOf(t, n))
	}
	return ids
}

// unread returns the unread notifications of the user from GET /me.
func (f *forum) unread(user string) float64 {
	f.t.Helper()
	return f.ok(user, http.MethodGet, "/api/v1/me", nil, http.StatusOK)["unread_notifications"].(float64)
}

// notified makes bob get, in this order, an addition to the private room (when
// the forum was made), a mention, a reply and an assignment.
func (f *forum) notified() (mention, reply, assigned app.Post) {
	f.t.Helper()
	f.clk.Advance(time.Minute)
	mention = f.write("admin", "lobby", "Hello @bob, welcome")
	f.clk.Advance(time.Minute)
	mine := f.write("bob", "lobby", "my post")
	f.clk.Advance(time.Minute)
	reply = f.say("admin", mine.ID, "nice one")
	f.clk.Advance(time.Minute)
	var err error
	assigned, err = f.posts.Create(f.t.Context(), actorOf(f.testEnv, "admin"), f.lobby.ID, app.PostInput{
		Format: app.FormatText, Body: "work to do",
		Tasks: []app.TaskInput{{Title: "Write the report", Assignees: []uint64{f.userID("bob")}}},
	})
	if err != nil {
		f.t.Fatalf("Create: %+v", err)
	}
	return mention, reply, assigned
}

func TestNotifications(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	mention, reply, assigned := f.notified()
	page := f.notificationsOf("bob", "")
	if got := kindsOf(t, page); !reflect.DeepEqual(got, []string{"assigned", "reply", "mention", "added_to_room"}) {
		t.Fatalf("kinds = %v", got)
	}
	if page["next_before"] != nil {
		t.Errorf("next_before = %v on the only page", page["next_before"])
	}
	items := list(t, page["notifications"])
	byKind := map[string]map[string]any{}
	for _, n := range items {
		byKind[obj(t, n)["kind"].(string)] = obj(t, n)
	}
	a := byKind["assigned"]
	if a["post_id"] != float64(assigned.ID) || a["task_id"] != float64(assigned.Tasks[0].ID) || a["read"] != false ||
		obj(t, a["actor"])["username"] != "admin" || obj(t, a["room"])["name"] != "lobby" || obj(t, a["room"])["id"] != float64(f.lobby.ID) {
		t.Errorf("assigned = %v", a)
	}
	if r := byKind["reply"]; r["post_id"] != float64(reply.ID) || r["task_id"] != nil || obj(t, r["actor"])["username"] != "admin" {
		t.Errorf("reply = %v", r)
	}
	if m := byKind["mention"]; m["post_id"] != float64(mention.ID) || m["task_id"] != nil {
		t.Errorf("mention = %v", m)
	}
	added := byKind["added_to_room"]
	if added["post_id"] != nil || added["task_id"] != nil || obj(t, added["room"])["name"] != "den" || obj(t, added["actor"])["username"] != "alice" {
		t.Errorf("added_to_room = %v", added)
	}
	for _, n := range items {
		if _, err := time.Parse(time.RFC3339, obj(t, n)["created_at"].(string)); err != nil {
			t.Errorf("created_at = %v", obj(t, n)["created_at"])
		}
	}
	// The post of a notification opens as a thread.
	f.ok("bob", http.MethodGet, path("/posts/%d/thread", uint64(a["post_id"].(float64))), nil, http.StatusOK)

	t.Run("only one's own", func(t *testing.T) {
		for _, user := range []string{"admin", "alice", "carol", "root"} {
			for _, n := range list(t, f.notificationsOf(user, "")["notifications"]) {
				if obj(t, n)["kind"] == "assigned" || obj(t, n)["kind"] == "mention" {
					t.Errorf("%s sees %v", user, n)
				}
			}
		}
		if got := kindsOf(t, f.notificationsOf("alice", "")); len(got) != 0 {
			t.Errorf("alice sees %v", got)
		}
	})
	t.Run("the count in /me", func(t *testing.T) {
		if got := f.unread("bob"); got != 4 {
			t.Errorf("unread = %v, want 4", got)
		}
	})
}

func TestNotificationsPaging(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	for i := 0; i < 35; i++ {
		f.write("admin", "lobby", fmt.Sprintf("@bob number %d", i))
	}
	// 35 mentions and the addition to den.
	t.Run("30 by default", func(t *testing.T) {
		page := f.notificationsOf("bob", "")
		ids := notificationIDs(t, page)
		if len(ids) != 30 || page["next_before"] != float64(ids[29]) {
			t.Errorf("%d notifications, next_before %v, want 30 and the last ID %d", len(ids), page["next_before"], ids[29])
		}
		for i := 1; i < len(ids); i++ {
			if ids[i] >= ids[i-1] {
				t.Fatalf("not newest first: %v", ids)
			}
		}
	})
	t.Run("pages of ten walk through everything once", func(t *testing.T) {
		var seen []uint64
		query := "?limit=10"
		for pages := 0; ; pages++ {
			if pages > 10 {
				t.Fatal("never ends")
			}
			page := f.notificationsOf("bob", query)
			seen = append(seen, notificationIDs(t, page)...)
			if page["next_before"] == nil {
				break
			}
			query = fmt.Sprintf("?limit=10&before=%d", uint64(page["next_before"].(float64)))
		}
		if len(seen) != 36 {
			t.Errorf("saw %d notifications, want 36", len(seen))
		}
		seenOnce := map[uint64]bool{}
		for _, id := range seen {
			if seenOnce[id] {
				t.Errorf("%d twice", id)
			}
			seenOnce[id] = true
		}
	})
	t.Run("the largest page", func(t *testing.T) {
		page := f.notificationsOf("bob", "?limit=100")
		if len(list(t, page["notifications"])) != 36 || page["next_before"] != nil {
			t.Errorf("page = %d notifications, next_before %v", len(list(t, page["notifications"])), page["next_before"])
		}
	})
	t.Run("before the oldest", func(t *testing.T) {
		all := notificationIDs(t, f.notificationsOf("bob", "?limit=100"))
		page := f.notificationsOf("bob", fmt.Sprintf("?before=%d", all[len(all)-1]))
		if len(list(t, page["notifications"])) != 0 || page["next_before"] != nil {
			t.Errorf("page = %v", page)
		}
	})
	for _, query := range []string{"?limit=0", "?limit=101", "?limit=-1", "?limit=abc", "?limit=", "?before=0", "?before=abc", "?before=-1", "?before="} {
		t.Run(query, func(t *testing.T) {
			rec := f.call("bob", http.MethodGet, "/api/v1/notifications"+query, nil)
			requireStatus(t, rec, http.StatusUnprocessableEntity)
			if len(errorFields(t, rec)) != 1 {
				t.Errorf("fields = %v", errorFields(t, rec))
			}
		})
	}
}

// TestNotificationsOfARoomTheUserCannotRead checks D30: the notifications of a
// room that the user can no longer read are not listed and not counted.
func TestNotificationsOfARoomTheUserCannotRead(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	f.write("alice", "den", "@bob a secret")
	f.write("admin", "lobby", "@bob hello")
	if got := kindsOf(t, f.notificationsOf("bob", "")); !reflect.DeepEqual(got, []string{"mention", "mention", "added_to_room"}) {
		t.Fatalf("kinds = %v", got)
	}
	if got := f.unread("bob"); got != 3 {
		t.Fatalf("unread = %v", got)
	}
	if err := f.rooms.RemoveMember(t.Context(), actorOf(f.testEnv, "admin"), f.den.ID, f.userID("bob")); err != nil {
		t.Fatal(err)
	}
	page := f.notificationsOf("bob", "")
	if got := kindsOf(t, page); !reflect.DeepEqual(got, []string{"mention"}) {
		t.Errorf("kinds = %v, want only the one of the lobby", got)
	}
	if room := obj(t, obj(t, list(t, page["notifications"])[0])["room"]); room["name"] != "lobby" {
		t.Errorf("room = %v", room)
	}
	if got := f.unread("bob"); got != 1 {
		t.Errorf("unread = %v, want 1", got)
	}
}

func TestReadNotifications(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	f.notified()
	ids := notificationIDs(t, f.notificationsOf("bob", "")) // newest first
	if len(ids) != 4 || f.unread("bob") != 4 {
		t.Fatalf("ids = %v, unread %v", ids, f.unread("bob"))
	}
	readFlags := func(user string) []bool {
		t.Helper()
		var out []bool
		for _, n := range list(t, f.notificationsOf(user, "")["notifications"]) {
			out = append(out, obj(t, n)["read"].(bool))
		}
		return out
	}

	t.Run("up to an ID", func(t *testing.T) {
		rec := f.call("bob", http.MethodPost, "/api/v1/notifications/read", map[string]any{"up_to_id": ids[2]})
		requireStatus(t, rec, http.StatusNoContent)
		if got := readFlags("bob"); !reflect.DeepEqual(got, []bool{false, false, true, true}) {
			t.Errorf("read = %v, want the two oldest", got)
		}
		if got := f.unread("bob"); got != 2 {
			t.Errorf("unread = %v", got)
		}
	})
	t.Run("again is not an error", func(t *testing.T) {
		requireStatus(t, f.call("bob", http.MethodPost, "/api/v1/notifications/read", map[string]any{"up_to_id": ids[2]}), http.StatusNoContent)
		if got := readFlags("bob"); !reflect.DeepEqual(got, []bool{false, false, true, true}) {
			t.Errorf("read = %v", got)
		}
	})
	t.Run("somebody else's notifications are not touched", func(t *testing.T) {
		before := f.unread("bob")
		requireStatus(t, f.call("carol", http.MethodPost, "/api/v1/notifications/read", map[string]any{"up_to_id": ids[0] + 1000}), http.StatusNoContent)
		requireStatus(t, f.call("alice", http.MethodPost, "/api/v1/notifications/read", map[string]any{"up_to_id": ids[0] + 1000}), http.StatusNoContent)
		if got := f.unread("bob"); got != before {
			t.Errorf("unread = %v, want %v", got, before)
		}
	})
	t.Run("an ID past the newest reads everything", func(t *testing.T) {
		requireStatus(t, f.call("bob", http.MethodPost, "/api/v1/notifications/read", map[string]any{"up_to_id": ids[0] + 1000}), http.StatusNoContent)
		if got := readFlags("bob"); !reflect.DeepEqual(got, []bool{true, true, true, true}) {
			t.Errorf("read = %v", got)
		}
		if got := f.unread("bob"); got != 0 {
			t.Errorf("unread = %v", got)
		}
	})
	t.Run("a new notification is unread", func(t *testing.T) {
		f.write("admin", "lobby", "@bob again")
		if got := readFlags("bob"); len(got) != 5 || got[0] {
			t.Errorf("read = %v", got)
		}
		if got := f.unread("bob"); got != 1 {
			t.Errorf("unread = %v", got)
		}
	})
	for _, tt := range []struct {
		name string
		body any
	}{
		{"no ID", map[string]any{}},
		{"a null ID", `{"up_to_id": null}`},
		{"an ID of 0", map[string]any{"up_to_id": 0}},
		{"an ID that is text", `{"up_to_id": "5"}`},
		{"a negative ID", `{"up_to_id": -1}`},
		{"an unknown field", map[string]any{"up_to_id": 1, "before": 2}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.call("bob", http.MethodPost, "/api/v1/notifications/read", tt.body)
			requireStatus(t, rec, http.StatusUnprocessableEntity)
			if len(errorFields(t, rec)) != 1 {
				t.Errorf("fields = %v", errorFields(t, rec))
			}
		})
	}
}
