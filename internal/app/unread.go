package app

import (
	"context"

	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// UnreadPostsCap is the largest number of unread posts that a room counts: 100
// means 100 or more (the pages show "99+" above 99). Counting stops there, so
// that a room nobody read for a long time costs no more than a read one.
const UnreadPostsCap = store.UnreadPostsCap

// FieldPostID names the post or comment ID of Rooms.MarkRead in the metadata
// "fields" of a TagInvalidInput error.
const FieldPostID = "post_id"

// RoomUnread holds the unread counts of one room for one member (SPEC §7.17,
// D55). They are the raw numbers: whether a muted room or a room that counts
// only mentions shows them is for the page (Room.Muted, Room.MentionsOnly).
type RoomUnread struct {
	// RoomID is the room.
	RoomID uint64
	// Posts is the number of top-level posts of the room that are not deleted,
	// were written by someone else, and are newer than the member's mark, up to
	// UnreadPostsCap.
	Posts int
	// Mentions is the number of the member's unread notifications in the room
	// that are mentions: mention, group_mention and keyword.
	Mentions int
}

// unreadEvent is the event that tells a member that the unread counts of a
// room changed for them.
func unreadEvent(userID, roomID uint64) live.Event {
	return live.Event{Topic: live.UserTopic(userID), Kind: live.KindRoomUnread, RoomID: roomID, UserID: userID}
}

// unreadUsers returns the set of the users that the room.unread events among
// events are for.
func unreadUsers(events []live.Event) map[uint64]bool {
	set := map[uint64]bool{}
	for _, ev := range events {
		if ev.Kind == live.KindRoomUnread {
			set[ev.UserID] = true
		}
	}
	return set
}

// isMentionKind reports whether a notification of the kind counts as a mention
// in the unread counts of a room (SPEC §7.17).
func isMentionKind(k NotificationKind) bool {
	return k == NotificationMention || k == NotificationGroupMention || k == NotificationKeyword
}

// fillUnread sets UnreadPosts and UnreadMentions of the views from the counts
// of the actor. A view of a room the actor is not a member of gets 0. One view
// reads the counts of its room alone; more read the counts of all the actor's
// rooms in one query.
func fillUnread(ctx context.Context, st *store.Store, actor Actor, views []Room) error {
	if len(views) == 0 {
		return nil
	}
	if len(views) == 1 {
		got, ok, err := st.RoomUnreadOf(ctx, actor.UserID, views[0].ID)
		if err != nil {
			return err
		}
		if ok {
			views[0].UnreadPosts, views[0].UnreadMentions = got.Posts, got.Mentions
		}
		return nil
	}
	counts, err := st.RoomUnreadCounts(ctx, actor.UserID)
	if err != nil {
		return err
	}
	byRoom := make(map[uint64]store.RoomUnread, len(counts))
	for _, c := range counts {
		byRoom[c.RoomID] = c
	}
	for i := range views {
		c := byRoom[views[i].ID]
		views[i].UnreadPosts, views[i].UnreadMentions = c.Posts, c.Mentions
	}
	return nil
}

// Unread returns the unread counts of the actor in one room, and false when
// the actor is not a member of it (a room that does not exist, or that the
// actor left, has none). It is one small query. It is what a stream or a
// client reads when it hears room.unread.
func (r *Rooms) Unread(ctx context.Context, actor Actor, roomID uint64) (RoomUnread, bool, error) {
	got, ok, err := r.st.RoomUnreadOf(ctx, actor.UserID, roomID)
	if err != nil || !ok {
		return RoomUnread{}, false, err
	}
	return RoomUnread{RoomID: got.RoomID, Posts: got.Posts, Mentions: got.Mentions}, true, nil
}

// MineOne returns the room as Mine shows it in the sidebar of the actor: with
// Muted, MentionsOnly and the unread counts. It returns false when the actor
// is not a member of the room. It is a few small queries, for the stream that
// patches one entry of the sidebar.
func (r *Rooms) MineOne(ctx context.Context, actor Actor, roomID uint64) (Room, bool, error) {
	counts, ok, err := r.st.RoomUnreadOf(ctx, actor.UserID, roomID)
	if err != nil || !ok {
		return Room{}, false, err
	}
	row, err := r.st.RoomByID(ctx, roomID)
	if err != nil {
		return Room{}, false, err
	}
	views, err := roomViews(ctx, r.st, actor, []store.Room{row})
	if err != nil {
		return Room{}, false, err
	}
	pref, _, err := r.st.PlacePrefOf(ctx, actor.UserID, store.RoomPlace(roomID))
	if err != nil {
		return Room{}, false, err
	}
	room := views[0]
	room.Muted = pref.Muted
	room.MentionsOnly = pref.MarkUnread == store.MarkUnreadMentions
	room.UnreadPosts, room.UnreadMentions = counts.Posts, counts.Mentions
	return room, true, nil
}

// MarkRead moves the actor's mark in the room forward to the post or comment
// upToID, which marks every post and comment up to it as seen (SPEC §7.17). It
// never moves the mark back, and caps it at the room's highest ID. It
// publishes room.unread to the actor when the mark moved. It does not touch
// the actor's notifications (Visit does).
//
// A room that the actor may not read fails with TagNotFound; a visible room
// that the actor has not joined with TagForbidden. A post that does not exist
// or is not of this room fails with TagInvalidInput, with the problem in the
// metadata "fields" (FieldPostID).
func (r *Rooms) MarkRead(ctx context.Context, actor Actor, roomID, upToID uint64) error {
	var events []live.Event
	err := r.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		room, err := loadReadableRoom(ctx, tx, actor, roomID)
		if err != nil {
			return err
		}
		member, err := isMember(ctx, tx, actor.UserID, room.ID)
		if err != nil {
			return err
		}
		if !member {
			return errors.New(TagForbidden, "only members mark a room as read",
				"userID", actor.UserID, "roomID", room.ID)
		}
		post, err := tx.PostByID(ctx, upToID)
		if err != nil {
			if appErr, ok := errors.FromError(err); ok && appErr.Tag == TagNotFound {
				return errors.New(TagInvalidInput, "the post does not exist",
					"fields", map[string]string{FieldPostID: "Choose a post or comment of this room."})
			}
			return err
		}
		if post.RoomID != room.ID {
			return errors.New(TagInvalidInput, "the post is not of this room",
				"fields", map[string]string{FieldPostID: "Choose a post or comment of this room."})
		}
		newest, err := tx.RoomMaxPostID(ctx, room.ID)
		if err != nil {
			return err
		}
		moved, err := tx.MoveRoomRead(ctx, actor.UserID, room.ID, min(upToID, newest))
		if err != nil {
			return err
		}
		if moved {
			events = []live.Event{unreadEvent(actor.UserID, room.ID)}
		}
		return nil
	})
	if err != nil {
		return err
	}
	publishAll(r.pub, events)
	return nil
}

// Visit is what opening the feed or the board of a room does for the actor: it
// moves the mark to the room's highest ID at this moment, and marks the
// actor's unread mentions of the room (the notifications of the kinds
// mention, group_mention and keyword) as read, in one transaction, as
// Mattermost clears a channel's mentions when you view it (SPEC §7.17). It
// returns the mark as it was before, so that the caller can tell which posts
// it had read. It publishes room.unread to the actor when the mark or the
// mentions changed, and notification.read when mentions were read.
//
// The caller reads the page's data first and calls Visit after, so that the
// posts it shows are the ones the mark covers. The actor does not have to be a
// member: for a visible room that they have not joined nothing is marked and
// the mark is 0. A room that the actor may not read fails with TagNotFound.
func (r *Rooms) Visit(ctx context.Context, actor Actor, roomID uint64) (uint64, error) {
	var events []live.Event
	var before uint64
	err := r.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		before = 0
		room, err := loadReadableRoom(ctx, tx, actor, roomID)
		if err != nil {
			return err
		}
		member, err := isMember(ctx, tx, actor.UserID, room.ID)
		if err != nil || !member {
			return err
		}
		before, _, err = tx.RoomLastSeen(ctx, actor.UserID, room.ID)
		if err != nil {
			return err
		}
		newest, err := tx.RoomMaxPostID(ctx, room.ID)
		if err != nil {
			return err
		}
		moved, err := tx.MoveRoomRead(ctx, actor.UserID, room.ID, newest)
		if err != nil {
			return err
		}
		cleared, err := tx.MarkRoomMentionsRead(ctx, actor.UserID, room.ID)
		if err != nil {
			return err
		}
		if moved || cleared > 0 {
			events = append(events, unreadEvent(actor.UserID, room.ID))
		}
		if cleared > 0 {
			events = append(events, live.Event{Topic: live.UserTopic(actor.UserID), Kind: live.KindNotificationRead, UserID: actor.UserID})
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	publishAll(r.pub, events)
	return before, nil
}
