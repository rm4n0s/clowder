package api

import (
	"net/http"
	"reflect"
	"slices"
	"testing"

	"github.com/rm4n0s/clowder/internal/live"
)

// sameJSON fails the test unless the two decoded JSON values are equal.
func sameJSON(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got  %v\n want %v", what, got, want)
	}
}

// TestWSEvents sends every kind of event that the services publish and checks
// that it arrives on its topic, with the resource as the REST API returns it
// to the user. Every message is also validated against WsServerMessage by the
// client.
func TestWSEvents(t *testing.T) {
	t.Parallel()
	w := newWSForum(t)
	alice, bob := w.userID("alice"), w.userID("bob")
	room, board := topicOf(topicRoom, w.den.ID), topicOf(topicBoard, w.den.ID)
	own := topicOf(topicUser, alice)
	root := w.write("bob", "den", "the thread")
	thread := topicOf(topicPost, root.ID)
	direct := w.convs("alice", "bob")
	conv := topicOf(topicConversation, direct)
	groupID := w.convs("bob", "alice", "carol")
	group := topicOf(topicConversation, groupID)

	c := w.dial("alice")
	c.mustSubscribe(room, board, thread, conv, group)

	// A post that mentions alice: the room, and her notifications.
	created := w.ok("bob", http.MethodPost, path("/rooms/%d/posts", w.den.ID), map[string]any{"body": "fresh, @alice"}, http.StatusCreated)
	postID := idOf(t, created)
	ev := c.await(room, "post.created")
	if ev["room_id"] != float64(w.den.ID) || ev["post_id"] != float64(postID) {
		t.Errorf("post.created = %v", ev)
	}
	sameJSON(t, "post.created data", ev["data"], w.ok("alice", http.MethodGet, path("/posts/%d", postID), nil, http.StatusOK))
	if data := obj(t, ev["data"]); data["can_edit"] != false || data["body"] != "fresh, @alice" {
		t.Errorf("the post is not as alice sees it: %v", data)
	}
	ev = c.await(own, "notification.created")
	notification := obj(t, ev["data"])
	if notification["kind"] != "mention" || notification["post_id"] != float64(postID) {
		t.Errorf("notification.created data = %v", notification)
	}
	mentioned := idOf(t, notification)
	notifications := list(t, w.ok("alice", http.MethodGet, "/api/v1/notifications", nil, http.StatusOK)["notifications"])
	sameJSON(t, "notification.created data", notification, notifications[0])

	// Alice reads her notifications.
	w.ok("alice", http.MethodPost, "/api/v1/notifications/read", map[string]any{"up_to_id": mentioned}, http.StatusNoContent)
	ev = c.await(own, "notification.read")
	if counts := obj(t, ev["data"]); counts["unread_notifications"] != float64(0) {
		t.Errorf("notification.read data = %v", counts)
	}

	// A comment: the thread, and the root on the room.
	comment := w.ok("bob", http.MethodPost, path("/posts/%d/comments", root.ID), map[string]any{"body": "first comment"}, http.StatusCreated)
	commentID := idOf(t, comment)
	ev = c.await(thread, "comment.created")
	if ev["root_id"] != float64(root.ID) || ev["post_id"] != float64(commentID) {
		t.Errorf("comment.created = %v", ev)
	}
	sameJSON(t, "comment.created data", ev["data"], w.ok("alice", http.MethodGet, path("/posts/%d", commentID), nil, http.StatusOK))
	ev = c.await(room, "post.updated")
	if data := obj(t, ev["data"]); data["id"] != float64(root.ID) || data["comment_count"] != float64(1) {
		t.Errorf("post.updated data = %v", data)
	}

	w.ok("bob", http.MethodPatch, path("/posts/%d", commentID), map[string]any{"body": "edited comment"}, http.StatusOK)
	ev = c.await(thread, "comment.updated")
	if data := obj(t, ev["data"]); data["body"] != "edited comment" || data["edited_at"] == nil {
		t.Errorf("comment.updated data = %v", data)
	}

	w.ok("bob", http.MethodPatch, path("/posts/%d", root.ID), map[string]any{"body": "edited root"}, http.StatusOK)
	c.await(thread, "post.updated")

	// Reactions are shown to each user as they see them: the mark "mine" is
	// alice's.
	w.ok("bob", http.MethodPut, path("/posts/%d/reactions/", root.ID)+thumbsUp, nil, http.StatusOK)
	for _, topic := range []string{room, thread} {
		ev = c.await(topic, "reaction.changed")
		reactions := list(t, obj(t, ev["data"])["reactions"])
		if len(reactions) != 1 || obj(t, reactions[0])["count"] != float64(1) || obj(t, reactions[0])["mine"] != false {
			t.Errorf("reaction.changed on %s data = %v", topic, ev["data"])
		}
	}
	reacted := w.ok("alice", http.MethodPut, path("/posts/%d/reactions/", root.ID)+thumbsUp, nil, http.StatusOK)
	ev = c.await(room, "reaction.changed")
	sameJSON(t, "reaction.changed data", ev["data"], reacted)
	if reactions := list(t, obj(t, ev["data"])["reactions"]); obj(t, reactions[0])["mine"] != true {
		t.Errorf("alice's own reaction is not marked: %v", ev["data"])
	}

	// The board.
	w.ok("bob", http.MethodPost, path("/rooms/%d/columns", w.den.ID), map[string]any{"name": "Review"}, http.StatusCreated)
	ev = c.await(board, "board.changed")
	sameJSON(t, "board.changed data", ev["data"], w.boardOf("alice", "den"))

	// The room: renamed, a member comes and goes.
	w.ok("alice", http.MethodPatch, path("/rooms/%d", w.den.ID), map[string]any{"name": "cave"}, http.StatusOK)
	ev = c.await(room, "room.updated")
	sameJSON(t, "room.updated data", ev["data"], w.ok("alice", http.MethodGet, path("/rooms/%d", w.den.ID), nil, http.StatusOK))
	if obj(t, ev["data"])["name"] != "cave" {
		t.Errorf("room.updated data = %v", ev["data"])
	}
	carol := w.userID("carol")
	w.ok("alice", http.MethodPost, path("/rooms/%d/members", w.den.ID), map[string]any{"user_id": carol}, http.StatusNoContent)
	ev = c.await(room, "member.joined")
	if ev["user_id"] != float64(carol) || obj(t, obj(t, ev["data"])["user"])["username"] != "carol" {
		t.Errorf("member.joined = %v", ev)
	}
	w.ok("admin", http.MethodDelete, path("/rooms/%d/members/%d", w.den.ID, carol), nil, http.StatusNoContent)
	ev = c.await(room, "member.left")
	if ev["user_id"] != float64(carol) || ev["data"] != nil {
		t.Errorf("member.left = %v", ev)
	}

	// Deleting: no data.
	w.ok("bob", http.MethodDelete, path("/posts/%d", commentID), nil, http.StatusNoContent)
	if ev = c.await(thread, "comment.deleted"); ev["data"] != nil || ev["post_id"] != float64(commentID) {
		t.Errorf("comment.deleted = %v", ev)
	}
	w.ok("bob", http.MethodDelete, path("/posts/%d", postID), nil, http.StatusNoContent)
	if ev = c.await(room, "post.deleted"); ev["data"] != nil || ev["post_id"] != float64(postID) {
		t.Errorf("post.deleted = %v", ev)
	}

	// Messages.
	sent := w.msg("bob", direct, "Hello, alice")
	msgID := idOf(t, sent)
	ev = c.await(conv, "message.created")
	if ev["message_id"] != float64(msgID) || ev["conversation_id"] != float64(direct) {
		t.Errorf("message.created = %v", ev)
	}
	sameJSON(t, "message.created data", ev["data"], w.ok("alice", http.MethodGet, path("/conversations/%d/messages", direct), nil, http.StatusOK)["messages"].([]any)[0])
	if data := obj(t, ev["data"]); data["can_edit"] != false || data["body"] != "Hello, alice" {
		t.Errorf("message.created data = %v", data)
	}
	ev = c.await(own, "conversation.changed")
	if data := obj(t, ev["data"]); data["id"] != float64(direct) || data["unread"] != true {
		t.Errorf("conversation.changed data = %v", data)
	}
	sameJSON(t, "conversation.changed data", ev["data"], w.ok("alice", http.MethodGet, path("/conversations/%d", direct), nil, http.StatusOK))

	w.ok("bob", http.MethodPatch, path("/messages/%d", msgID), map[string]any{"body": "Hello, Alice"}, http.StatusOK)
	if data := obj(t, c.await(conv, "message.updated")["data"]); data["body"] != "Hello, Alice" {
		t.Errorf("message.updated data = %v", data)
	}
	w.ok("bob", http.MethodPut, path("/messages/%d/reactions/", msgID)+thumbsUp, nil, http.StatusOK)
	ev = c.await(conv, "message.reaction")
	if reactions := list(t, obj(t, ev["data"])["reactions"]); len(reactions) != 1 || obj(t, reactions[0])["mine"] != false {
		t.Errorf("message.reaction data = %v", ev["data"])
	}

	// Reading: alice's own reading moves her counts; bob's moves "Seen".
	w.ok("alice", http.MethodPost, path("/conversations/%d/read", direct), map[string]any{"up_to_id": msgID}, http.StatusNoContent)
	ev = c.await(own, "conversation.read")
	if counts := obj(t, ev["data"]); counts["unread_conversations"] != float64(0) {
		t.Errorf("conversation.read on the user topic, data = %v", counts)
	}
	answer := idOf(t, w.msg("alice", direct, "Hi, bob"))
	w.ok("bob", http.MethodPost, path("/conversations/%d/read", direct), map[string]any{"up_to_id": answer}, http.StatusNoContent)
	ev = c.await(conv, "conversation.read")
	sameJSON(t, "conversation.read data", ev["data"], map[string]any{
		"conversation_id": float64(direct), "user_id": float64(bob), "up_to_id": float64(answer)})

	w.ok("bob", http.MethodDelete, path("/messages/%d", msgID), nil, http.StatusNoContent)
	if ev = c.await(conv, "message.deleted"); ev["data"] != nil || ev["message_id"] != float64(msgID) {
		t.Errorf("message.deleted = %v", ev)
	}

	// A group is renamed.
	w.ok("bob", http.MethodPatch, path("/conversations/%d", groupID), map[string]any{"name": "team"}, http.StatusOK)
	ev = c.await(group, "conversation.updated")
	sameJSON(t, "conversation.updated data", ev["data"], w.ok("alice", http.MethodGet, path("/conversations/%d", groupID), nil, http.StatusOK))
	if obj(t, ev["data"])["name"] != "team" {
		t.Errorf("conversation.updated data = %v", ev["data"])
	}
}

// TestWSEventsReachOnlyTheSubscribers checks that a connection gets what it
// asked for and nothing else, and that two connections of a user are both
// served.
func TestWSEventsReachOnlyTheSubscribers(t *testing.T) {
	t.Parallel()
	w := newWSForum(t)
	lobby, den := topicOf(topicRoom, w.lobby.ID), topicOf(topicRoom, w.den.ID)
	onlyLobby := w.dial("bob")
	onlyLobby.mustSubscribe(lobby)
	both, again := w.dial("bob"), w.dial("bob")
	both.mustSubscribe(lobby, den)
	again.mustSubscribe(den)
	nothing := w.dial("carol")

	w.write("admin", "lobby", "for the lobby")
	onlyLobby.await(lobby, "post.created")
	both.await(lobby, "post.created")
	w.write("alice", "den", "for the den")
	both.await(den, "post.created")
	again.await(den, "post.created")

	onlyLobby.requireQuiet() // the den's post and the lobby's were announced once each
	again.requireQuiet()
	nothing.requireQuiet()
}

// TestWSAnEventIsSentForTheSubscribedTopicOnly checks that the topics a client
// removed are not sent any more, even for events queued before.
func TestWSUnsubscribedTopicsAreSilent(t *testing.T) {
	t.Parallel()
	w := newWSForum(t)
	lobby := topicOf(topicRoom, w.lobby.ID)
	c := w.dial("bob")
	c.mustSubscribe(lobby)
	// Events are queued for the connection, then the topic is removed in the
	// same breath: the ones that were queued are not sent after the ack.
	for range 3 {
		w.hub.Publish(live.Event{Topic: lobby, Kind: live.KindRoomUpdated, RoomID: w.lobby.ID})
	}
	c.send(map[string]any{"type": wsUnsubscribe, "id": 100, "topics": []string{lobby}})
	var events int
	for {
		m := c.read()
		if m["type"] == wsEventType {
			events++
			continue
		}
		if m["type"] != wsAck || m["id"] != float64(100) {
			t.Fatalf("got %s", describeMessage(m))
		}
		break
	}
	if events > 3 {
		t.Errorf("%d events", events)
	}
	w.hub.Publish(live.Event{Topic: lobby, Kind: live.KindRoomUpdated, RoomID: w.lobby.ID})
	c.requireQuiet()
}

// TestWSLosingAccess checks the rules for a user who loses what they listen
// to: the event is not sent, and the topic is dropped with an error of
// not_found that names it (SPEC §7.13).
func TestWSLosingAccess(t *testing.T) {
	t.Parallel()
	// dropped reads messages until it has an error for each of the topics and
	// returns the events that came before.
	dropped := func(t *testing.T, c *wsClient, topics ...string) []map[string]any {
		t.Helper()
		var events []map[string]any
		want := slices.Clone(topics)
		for len(want) > 0 {
			m := c.read()
			if m["type"] == wsEventType {
				events = append(events, m)
				continue
			}
			e := errorOf(t, m)
			if e["code"] != "not_found" || m["id"] != nil {
				t.Fatalf("got %s", describeMessage(m))
			}
			i := slices.Index(want, m["topic"].(string))
			if i < 0 {
				t.Fatalf("an error for %v, wanted one of %v", m["topic"], want)
			}
			want = slices.Delete(want, i, i+1)
		}
		return events
	}

	t.Run("removed from a private room", func(t *testing.T) {
		t.Parallel()
		w := newWSForum(t)
		post := w.write("alice", "den", "a thread")
		denTopics := []string{topicOf(topicRoom, w.den.ID), topicOf(topicBoard, w.den.ID), topicOf(topicPost, post.ID)}
		lobby := topicOf(topicRoom, w.lobby.ID)
		c := w.dial("bob")
		c.mustSubscribe(append(slices.Clone(denTopics), lobby)...)

		w.ok("admin", http.MethodDelete, path("/rooms/%d/members/%d", w.den.ID, w.userID("bob")), nil, http.StatusNoContent)
		events := dropped(t, c, denTopics...)
		for _, ev := range events {
			if slices.Contains(denTopics, ev["topic"].(string)) {
				t.Errorf("an event of the room that bob lost was sent: %v", ev)
			}
		}
		// Bob is told of it on his own topic.
		c.await(topicOf(topicUser, w.userID("bob")), "member.left")
		// What bob lost is silent now, and the lobby is not.
		w.write("alice", "den", "bob does not hear this")
		w.ok("alice", http.MethodPost, path("/posts/%d/comments", post.ID), map[string]any{"body": "nor this"}, http.StatusCreated)
		c.requireQuiet()
		w.write("admin", "lobby", "bob hears this")
		c.await(lobby, "post.created")
		// The topics can be subscribed to again only when the room is readable.
		e := errorOf(t, c.subscribe(denTopics[0]))
		if e["code"] != "not_found" {
			t.Errorf("code = %v", e["code"])
		}
	})

	t.Run("the event comes before the removal is known", func(t *testing.T) {
		t.Parallel()
		// An event of a room that the user cannot read is checked and not sent,
		// even when the hub still delivers it.
		w := newWSForum(t)
		den := topicOf(topicRoom, w.den.ID)
		c := w.dial("bob")
		c.mustSubscribe(den)
		if err := w.rooms.RemoveMember(t.Context(), actorOf(w.testEnv, "admin"), w.den.ID, w.userID("bob")); err != nil {
			t.Fatal(err)
		}
		// The removal published its events (a topic of the room and the user
		// topic of bob) to the hub, and the connection handled them: the
		// room's topic is gone. Publish more for the room as the hub of a
		// process that lags behind would.
		dropped(t, c, den)
		c.await(topicOf(topicUser, w.userID("bob")), "member.left")
		w.hub.Publish(live.Event{Topic: den, Kind: live.KindPostUpdated, RoomID: w.den.ID, PostID: 1})
		c.requireQuiet()
	})

	t.Run("a visible room that the user leaves is still read", func(t *testing.T) {
		t.Parallel()
		w := newWSForum(t)
		lobby := topicOf(topicRoom, w.lobby.ID)
		c := w.dial("bob")
		c.mustSubscribe(lobby)
		w.ok("bob", http.MethodPost, path("/rooms/%d/leave", w.lobby.ID), nil, http.StatusNoContent)
		ev := c.await(lobby, "member.left")
		if ev["user_id"] != float64(w.userID("bob")) {
			t.Errorf("member.left = %v", ev)
		}
		c.await(topicOf(topicUser, w.userID("bob")), "member.left")
		w.write("admin", "lobby", "bob still hears this")
		c.await(lobby, "post.created")
	})

	t.Run("the room is deleted", func(t *testing.T) {
		t.Parallel()
		w := newWSForum(t)
		post := w.write("alice", "den", "a thread")
		topics := []string{topicOf(topicRoom, w.den.ID), topicOf(topicBoard, w.den.ID), topicOf(topicPost, post.ID)}
		c := w.dial("alice")
		c.mustSubscribe(topics...)
		w.ok("alice", http.MethodDelete, path("/rooms/%d", w.den.ID), nil, http.StatusNoContent)
		events := dropped(t, c, topics...)
		// The room's topic says that the room is gone, with no data.
		found := false
		for _, ev := range events {
			if ev["topic"] == topics[0] && ev["kind"] == "room.deleted" && ev["data"] == nil {
				found = true
			}
		}
		if !found {
			t.Errorf("no room.deleted on the room's topic among %v", events)
		}
		c.await(topicOf(topicUser, w.userID("alice")), "room.deleted")
		c.requireQuiet()
	})

	t.Run("left a group", func(t *testing.T) {
		t.Parallel()
		w := newWSForum(t)
		group := w.convs("bob", "alice", "carol")
		topic := topicOf(topicConversation, group)
		c := w.dial("carol")
		c.mustSubscribe(topic)
		w.msg("bob", group, "carol hears this")
		c.await(topic, "message.created")
		w.ok("carol", http.MethodPost, path("/conversations/%d/leave", group), nil, http.StatusNoContent)
		dropped(t, c, topic)
		c.await(topicOf(topicUser, w.userID("carol")), "conversation.changed")
		w.msg("bob", group, "carol does not")
		c.requireQuiet()
	})

	t.Run("a conversation of someone else is never sent", func(t *testing.T) {
		t.Parallel()
		w := newWSForum(t)
		direct := w.convs("alice", "bob")
		c := w.dial("carol")
		w.msg("alice", direct, "private")
		c.requireQuiet()
	})
}
