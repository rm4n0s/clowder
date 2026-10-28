package api

import (
	"context"
	"slices"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/errors"
)

// wsCountsJSON is the data of the events that change the unread counts of the
// user: the same two numbers as GET /me.
type wsCountsJSON struct {
	UnreadNotifications int `json:"unread_notifications"`
	UnreadConversations int `json:"unread_conversations"`
}

// wsConversationReadJSON is the data of a conversation.read event on a
// conversation topic: who has read the conversation up to which message.
type wsConversationReadJSON struct {
	ConversationID uint64 `json:"conversation_id"`
	UserID         uint64 `json:"user_id"`
	UpToID         uint64 `json:"up_to_id"`
}

// wsPayload is what an event of a topic is sent with.
type wsPayload struct {
	// data is the resource, nil for an event that has none.
	data any
	// checked is true when building data has called a service that checks that
	// the user may read the topic, so that no separate check is needed.
	checked bool
	// skip is true for an event that is not sent: its subject is gone, or the
	// kind is not one that a client gets.
	skip bool
}

// onEvent handles one event of the hub. The topics the client dropped since the
// event was queued are ignored. The checks are the ones of the pages' streams:
// the user may still read what the event is about, or the event is not sent
// (and the topic is dropped when the user lost it, SPEC §7.13).
func (ws *wsSession) onEvent(ctx context.Context, ev live.Event) error {
	if ev.Topic == live.UserTopic(ws.user.ID) {
		return ws.onUserEvent(ctx, ev)
	}
	t, ok := ws.topics[ev.Topic]
	if !ok {
		return nil
	}
	if ev.Kind == live.KindRoomDeleted {
		// The room is gone, so nothing can be checked: the client is told, and
		// every topic of the room is dropped.
		if err := ws.sendEvent(ctx, ev, nil); err != nil {
			return err
		}
		return ws.recheck(ctx, sameRoom(ev.RoomID))
	}
	p, err := ws.dataOf(ctx, ev)
	if err == nil && !p.skip && !p.checked {
		_, err = ws.resolve(ctx, t)
	}
	if err != nil {
		if isNotFound(err) {
			// Either the user lost the topic, which is dropped, or only the
			// thing the event is about is gone, and the event is not sent.
			return ws.recheck(ctx, scopeOf(t))
		}
		return err
	}
	if p.skip {
		return nil
	}
	return ws.sendEvent(ctx, ev, p.data)
}

// scopeOf matches the topics that a loss of t may take with it: those of the
// same room, or the topic alone when it has none.
func scopeOf(t wsTopic) func(wsTopic) bool {
	if t.roomID != 0 {
		return sameRoom(t.roomID)
	}
	return only(t.name)
}

// dataOf builds the data of an event of a topic: the resource as the REST API
// returns it to this user now, made by the functions the handlers use. A
// deletion has none. A failure with app.TagNotFound means that the user may not
// read the subject (any more).
func (ws *wsSession) dataOf(ctx context.Context, ev live.Event) (wsPayload, error) {
	switch ev.Kind {
	case live.KindPostCreated, live.KindPostUpdated, live.KindCommentCreated, live.KindCommentUpdated:
		post, err := ws.s.svc.Posts.Get(ctx, ws.actor, ev.PostID)
		if err != nil {
			return wsPayload{}, err
		}
		return wsPayload{data: postOf(post), checked: true}, nil
	case live.KindPostDeleted, live.KindCommentDeleted, live.KindMemberLeft, live.KindMessageDeleted:
		return wsPayload{}, nil
	case live.KindReactionChanged:
		post, err := ws.s.svc.Posts.Get(ctx, ws.actor, ev.PostID)
		if err != nil {
			return wsPayload{}, err
		}
		if post.Deleted {
			return wsPayload{checked: true, skip: true}, nil
		}
		return wsPayload{data: reactionsResponse{Reactions: reactionsOf(post.Reactions)}, checked: true}, nil
	case live.KindBoardChanged:
		view, err := ws.s.svc.Board.Board(ctx, ws.actor, ev.RoomID)
		if err != nil {
			return wsPayload{}, err
		}
		out := boardResponse{RoomID: view.Room.ID, Columns: make([]columnJSON, len(view.Columns))}
		for i, col := range view.Columns {
			out.Columns[i] = columnOf(col)
		}
		return wsPayload{data: out, checked: true}, nil
	case live.KindRoomUpdated:
		room, err := ws.s.svc.Rooms.Get(ctx, ws.actor, ev.RoomID)
		if err != nil {
			return wsPayload{}, err
		}
		return wsPayload{data: roomOf(room), checked: true}, nil
	case live.KindMemberJoined:
		return ws.memberData(ctx, ev)
	case live.KindMessageCreated, live.KindMessageUpdated:
		msg, err := ws.s.svc.Messages.Message(ctx, ws.actor, ev.MessageID)
		if err != nil {
			return wsPayload{}, err
		}
		out, err := ws.s.messageOf(msg)
		if err != nil {
			return wsPayload{}, err
		}
		return wsPayload{data: out, checked: true}, nil
	case live.KindMessageReaction:
		msg, err := ws.s.svc.Messages.Message(ctx, ws.actor, ev.MessageID)
		if err != nil {
			return wsPayload{}, err
		}
		if msg.Deleted {
			return wsPayload{checked: true, skip: true}, nil
		}
		return wsPayload{data: reactionsResponse{Reactions: reactionsOf(msg.Reactions)}, checked: true}, nil
	case live.KindConversationUpdated:
		conv, err := ws.s.svc.Messages.Summary(ctx, ws.actor, ev.ConversationID)
		if err != nil {
			return wsPayload{}, err
		}
		return wsPayload{data: conversationOf(conv), checked: true}, nil
	case live.KindConversationRead:
		return wsPayload{data: wsConversationReadJSON{ConversationID: ev.ConversationID, UserID: ev.UserID, UpToID: ev.MessageID}}, nil
	}
	// A kind a later version adds: the client of this version does not get it.
	return wsPayload{skip: true}, nil
}

// memberData is the data of a member.joined event: the member as the list of
// the members of the room shows them. A member who is gone again has none, and
// the event is not sent.
func (ws *wsSession) memberData(ctx context.Context, ev live.Event) (wsPayload, error) {
	list, err := ws.s.svc.Rooms.Members(ctx, ws.actor, ev.RoomID)
	if err != nil {
		return wsPayload{}, err
	}
	i := slices.IndexFunc(list, func(m app.Member) bool { return m.UserID == ev.UserID })
	if i < 0 {
		return wsPayload{checked: true, skip: true}, nil
	}
	return wsPayload{data: memberOf(list[i]), checked: true}, nil
}

// sendEvent sends an event of the hub with its data.
func (ws *wsSession) sendEvent(ctx context.Context, ev live.Event, data any) error {
	return ws.send(ctx, wsEventMessage{
		Type: wsEventType, Topic: ev.Topic, Kind: ev.Kind,
		RoomID: ev.RoomID, PostID: ev.PostID, RootID: ev.RootID, UserID: ev.UserID,
		ConversationID: ev.ConversationID, MessageID: ev.MessageID,
		Ring: ev.Ring, Sound: ev.Sound, Browser: ev.Browser,
		Data: data,
	})
}

// onUserEvent handles an event of the user's own topic: the counts, the
// notifications, the conversations of the user, their memberships of rooms,
// their account (SPEC D37), and their profile and settings (user.updated and
// settings.changed).
func (ws *wsSession) onUserEvent(ctx context.Context, ev live.Event) error {
	switch ev.Kind {
	case live.KindUserChanged:
		return ws.checkSession(ctx)
	case live.KindUserUpdated:
		sess, err := ws.s.svc.Accounts.APISessionActor(ctx, ws.token)
		if err != nil {
			if isTag(err, app.TagUnauthenticated) {
				// The session ended; the user.changed that ended it closes the
				// connection.
				return nil
			}
			return err
		}
		return ws.sendEvent(ctx, ev, userOf(sess.User))
	case live.KindSettingsChanged:
		settings, err := ws.s.svc.Accounts.Settings(ctx, ws.actor)
		if err != nil {
			if isTag(err, app.TagUnauthenticated) {
				return nil
			}
			return err
		}
		return ws.sendEvent(ctx, ev, settingsOf(settings))
	case live.KindNotificationCreated:
		return ws.notificationCreated(ctx, ev)
	case live.KindNotificationChanged:
		return ws.notificationChanged(ctx, ev)
	case live.KindPlacePrefsChanged:
		return ws.placePrefsChanged(ctx, ev)
	case live.KindPostCreated, live.KindMessageCreated:
		return ws.ringNotice(ctx, ev)
	case live.KindNotificationRead, live.KindConversationRead:
		counts, err := ws.counts(ctx)
		if err != nil {
			return err
		}
		return ws.sendEvent(ctx, ev, counts)
	case live.KindConversationChanged:
		var data any
		conv, err := ws.s.svc.Messages.Summary(ctx, ws.actor, ev.ConversationID)
		switch {
		case err == nil:
			data = conversationOf(conv)
		case !isNotFound(err):
			return err
		}
		if err := ws.sendEvent(ctx, ev, data); err != nil {
			return err
		}
		// The user may have left the conversation.
		return ws.recheck(ctx, only(live.ConversationTopic(ev.ConversationID)))
	case live.KindMemberJoined, live.KindMemberLeft, live.KindRoomDeleted:
		var data any
		if ev.Kind == live.KindMemberJoined {
			p, err := ws.memberData(ctx, ev)
			if err != nil && !isNotFound(err) {
				return err
			}
			data = p.data
		}
		if err := ws.sendEvent(ctx, ev, data); err != nil {
			return err
		}
		// The user may have lost the room: its topics are checked again.
		return ws.recheck(ctx, sameRoom(ev.RoomID))
	}
	return nil
}

// counts reads the unread counts of the user.
func (ws *wsSession) counts(ctx context.Context) (wsCountsJSON, error) {
	notifications, err := ws.s.svc.Notifications.UnreadCount(ctx, ws.actor)
	if err != nil {
		return wsCountsJSON{}, err
	}
	conversations, err := ws.s.svc.Messages.UnreadCount(ctx, ws.actor)
	if err != nil {
		return wsCountsJSON{}, err
	}
	return wsCountsJSON{UnreadNotifications: notifications, UnreadConversations: conversations}, nil
}

// notificationChanged handles a notification.changed event: a reply was folded
// into a notification the user has, which now names a newer actor and counts
// more people. The event carries the notification as GET /notifications
// returns it, and never rings. A notification that is gone, or of a room the
// user can no longer read, sends nothing.
func (ws *wsSession) notificationChanged(ctx context.Context, ev live.Event) error {
	// The newest notification with an ID below ID+1 is the notification itself.
	page, err := ws.s.svc.Notifications.List(ctx, ws.actor, ev.NotificationID+1, 1)
	if err != nil {
		return err
	}
	if len(page.Notifications) == 0 || page.Notifications[0].ID != ev.NotificationID {
		return nil
	}
	return ws.sendEvent(ctx, ev, notificationOf(page.Notifications[0]))
}

// notificationCreated handles a notification.created event. The event does not
// say which notification it is, so the connection reads the newest page and
// sends an event for each notification that came after the last one it has
// seen, oldest first, each with the notification as GET /notifications returns
// it. Notifications of a room the user can no longer read are not in the page.
func (ws *wsSession) notificationCreated(ctx context.Context, ev live.Event) error {
	page, err := ws.s.svc.Notifications.List(ctx, ws.actor, 0, 0)
	if err != nil {
		return err
	}
	var fresh []app.Notification
	for _, n := range page.Notifications {
		if n.ID > ws.lastNotification {
			fresh = append(fresh, n)
		}
	}
	// The page is newest first.
	for i := len(fresh) - 1; i >= 0; i-- {
		ws.lastNotification = fresh[i].ID
		sent := ev
		if fresh[i].ID != ev.NotificationID {
			// The decision whether to ring is for the notification the event
			// is about; one that this connection missed rings for nobody.
			sent.Ring, sent.Sound, sent.Browser = false, "", false
		}
		if err := ws.sendEvent(ctx, sent, notificationOf(fresh[i])); err != nil {
			return err
		}
	}
	return nil
}

// wsNoticeJSON is the data of a post.created or message.created event on the
// user's own topic, which rings (SPEC §7.15): who did what and where, never the
// body. The IDs of the post, the room, the message and the conversation are in
// the envelope.
type wsNoticeJSON struct {
	// Text is the text of the notification of the system, for example "Bob
	// posted in #general".
	Text string `json:"text"`
	// PlaceName is the name of the room, or the title of the conversation.
	PlaceName string `json:"place_name"`
	// Author is the user whose action it is.
	Author userRefJSON `json:"author"`
}

// ringNotice handles a post.created or message.created event on the user's own
// topic. The server sends such an event only to a user it rings for; the
// connection sends it with the notice (no body) when the post or message is
// still there and the user may still read it.
func (ws *wsSession) ringNotice(ctx context.Context, ev live.Event) error {
	if !ev.Ring {
		return nil
	}
	n, ok, err := ws.s.svc.Prefs.Notice(ctx, ws.actor, ev)
	if err != nil {
		if isTag(err, app.TagUnauthenticated) {
			return nil
		}
		return err
	}
	if !ok {
		return nil
	}
	return ws.sendEvent(ctx, ev, wsNoticeJSON{Text: n.Text, PlaceName: n.PlaceName, Author: userRefOf(n.Author)})
}

// placePrefsChanged handles a place_prefs.changed event: the data are the
// preferences of the room or conversation as GET .../notification-prefs
// returns them. When the user can no longer have preferences there (they left
// it) the event is not sent.
func (ws *wsSession) placePrefsChanged(ctx context.Context, ev live.Event) error {
	ref := app.RoomRef(ev.RoomID)
	if ev.RoomID == 0 {
		ref = app.ConversationRef(ev.ConversationID)
	}
	prefs, err := ws.s.svc.Prefs.Place(ctx, ws.actor, ref)
	if err != nil {
		if isNotFound(err) || isTag(err, app.TagForbidden) || isTag(err, app.TagUnauthenticated) {
			return nil
		}
		return err
	}
	return ws.sendEvent(ctx, ev, placePrefsOf(prefs))
}

// checkSession reads the session of the token again (SPEC D37). A session that
// is gone (a logout, a disabled user, an expired session) ends the connection
// with 4001, and a changed role with 4002, which a client answers by connecting
// again.
func (ws *wsSession) checkSession(ctx context.Context) error {
	sess, err := ws.s.svc.Accounts.APISessionActor(ctx, ws.token)
	if err != nil {
		if isTag(err, app.TagUnauthenticated) {
			ws.end = &wsEnd{code: closeSessionEnded, reason: reasonSessionEnded}
			return nil
		}
		return err
	}
	if sess.Actor.Role != ws.actor.Role {
		ws.end = &wsEnd{code: closeSessionChanged, reason: reasonSessionChanged}
	}
	return nil
}

// isTag reports whether err is a tagged error with the tag.
func isTag(err error, tag string) bool {
	appErr, ok := errors.FromError(err)
	return ok && appErr.Tag == tag
}
