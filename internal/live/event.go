package live

import "strconv"

// The kinds of events. A kind says what happened; the topic of the event says
// where.
const (
	// KindPostCreated says a top-level post was written.
	KindPostCreated = "post.created"
	// KindPostUpdated says a top-level post changed: it was edited, or its
	// thread got, lost or changed a comment.
	KindPostUpdated = "post.updated"
	// KindPostDeleted says a top-level post was deleted, or went from the
	// room because it was an empty placeholder.
	KindPostDeleted = "post.deleted"
	// KindCommentCreated says a comment was written in a thread.
	KindCommentCreated = "comment.created"
	// KindCommentUpdated says a comment was edited.
	KindCommentUpdated = "comment.updated"
	// KindCommentDeleted says a comment was deleted.
	KindCommentDeleted = "comment.deleted"
	// KindReactionChanged says a reaction was added to or removed from a post
	// or comment; PostID is that post or comment.
	KindReactionChanged = "reaction.changed"
	// KindBoardChanged says a task or a column of the board of a room changed:
	// a task was written, edited, moved or deleted, or a column was added,
	// renamed, moved or deleted. It is published on BoardTopic; PostID is the
	// post or comment of the task when a task is concerned.
	KindBoardChanged = "board.changed"
	// KindRoomUpdated says a room was renamed or its description changed.
	KindRoomUpdated = "room.updated"
	// KindRoomDeleted says a room was deleted.
	KindRoomDeleted = "room.deleted"
	// KindMemberJoined says a user became a member of a room, by joining or
	// by being added.
	KindMemberJoined = "member.joined"
	// KindMemberLeft says a user stopped being a member of a room, by leaving
	// or by being removed.
	KindMemberLeft = "member.left"
	// KindNotificationCreated says a notification was made for a user. It is
	// published on UserTopic of that user; UserID is the user, RoomID the room
	// of the notification and PostID its post or comment when it has one.
	KindNotificationCreated = "notification.created"
	// KindNotificationRead says a user marked notifications as read. It is
	// published on UserTopic of that user; UserID is the user.
	KindNotificationRead = "notification.read"
)

// Event says that something changed. It is a small value and holds no
// content: whoever receives it re-reads what it needs from the services.
type Event struct {
	// Topic is where the event was published, e.g. RoomTopic(3).
	Topic string
	// Kind says what happened; it is one of the Kind constants.
	Kind string
	// RoomID is the ID of the room concerned; 0 when none.
	RoomID uint64
	// PostID is the ID of the post or comment concerned; 0 when none.
	PostID uint64
	// RootID is the ID of the top-level post of the thread of a comment
	// event; 0 when none.
	RootID uint64
	// UserID is the ID of the user concerned (the member who joined or left);
	// 0 when none.
	UserID uint64
}

// RoomTopic is the topic of the feed and the membership of a room.
func RoomTopic(roomID uint64) string { return topic("room:", roomID) }

// PostTopic is the topic of the thread of the top-level post rootID.
func PostTopic(rootID uint64) string { return topic("post:", rootID) }

// BoardTopic is the topic of the Kanban board of a room.
func BoardTopic(roomID uint64) string { return topic("board:", roomID) }

// ConversationTopic is the topic of a private conversation.
func ConversationTopic(conversationID uint64) string { return topic("conversation:", conversationID) }

// UserTopic is the topic of one user: their sidebar, notifications and the
// changes of their memberships.
func UserTopic(userID uint64) string { return topic("user:", userID) }

// topic joins a prefix and an ID.
func topic(prefix string, id uint64) string {
	return prefix + strconv.FormatUint(id, 10)
}
