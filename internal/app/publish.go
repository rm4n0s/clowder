package app

import (
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
)

// Publisher receives the live events of the services (SPEC §8). The hub of
// package live is one; so is live.Recorder, for tests. Publish must not block.
type Publisher interface {
	// Publish delivers the event to whoever listens to its topic.
	Publish(ev live.Event)
}

// nopPublisher is the Publisher of a service made with a nil one.
type nopPublisher struct{}

// Publish does nothing.
func (nopPublisher) Publish(live.Event) {}

// orNop returns pub, or a Publisher that does nothing when pub is nil.
func orNop(pub Publisher) Publisher {
	if pub == nil {
		return nopPublisher{}
	}
	return pub
}

// publishAll publishes the events in order. A method collects its events
// while its transaction runs and calls this only after the transaction
// returned nil.
func publishAll(pub Publisher, events []live.Event) {
	for _, ev := range events {
		pub.Publish(ev)
	}
}

// postRef is what postEvents needs to know of a post or comment.
type postRef struct {
	id, roomID uint64
	// rootID is the ID of the top-level post of a comment; 0 for a top-level
	// post.
	rootID uint64
}

// refOf makes the postRef of a row.
func refOf(row store.Post) postRef {
	ref := postRef{id: row.ID, roomID: row.RoomID}
	if row.RootID != nil {
		ref.rootID = *row.RootID
	}
	return ref
}

// postEvents are the live events of a change to a post or comment. A
// top-level post is announced to its room and to its own thread, with
// postKind (post.*). A comment is announced to the thread of its root with
// commentKind (comment.*), and to the room as an update of the root, so that
// the comment count of the feed refreshes.
func postEvents(ref postRef, postKind, commentKind string) []live.Event {
	if ref.rootID == 0 {
		return []live.Event{
			{Topic: live.RoomTopic(ref.roomID), Kind: postKind, RoomID: ref.roomID, PostID: ref.id},
			{Topic: live.PostTopic(ref.id), Kind: postKind, RoomID: ref.roomID, PostID: ref.id},
		}
	}
	return []live.Event{
		{Topic: live.PostTopic(ref.rootID), Kind: commentKind, RoomID: ref.roomID, PostID: ref.id, RootID: ref.rootID},
		{Topic: live.RoomTopic(ref.roomID), Kind: live.KindPostUpdated, RoomID: ref.roomID, PostID: ref.rootID, RootID: ref.rootID},
	}
}

// memberEvents are the live events of a user who joined or left a room: kind
// is live.KindMemberJoined or live.KindMemberLeft. The room hears of it, and
// so does the user, whose sidebar refreshes.
func memberEvents(roomID, userID uint64, kind string) []live.Event {
	return []live.Event{
		{Topic: live.RoomTopic(roomID), Kind: kind, RoomID: roomID, UserID: userID},
		{Topic: live.UserTopic(userID), Kind: kind, RoomID: roomID, UserID: userID},
	}
}
