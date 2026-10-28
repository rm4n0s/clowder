package live

import "strconv"

// The kinds of events. A kind says what happened; the topic of the event says
// where.
const (
	// KindPostCreated says a top-level post was written. It is also published
	// on UserTopic of each room member whose preferences make it ring (those
	// whose choice for the room is All, SPEC §7.15), with Ring and Sound set;
	// UserID is that member.
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
	// of the notification, PostID its post or comment when it has one and
	// NotificationID the notification. Ring, Sound and Browser say whether it
	// rings for that user.
	KindNotificationCreated = "notification.created"
	// KindNotificationChanged says a notification of a user changed: a reply
	// was folded into it, so that it names a newer actor and counts more
	// people (SPEC §7.15). It is published on UserTopic of that user; UserID
	// is the user, RoomID the room, PostID the post of the notification and
	// NotificationID the notification. It never rings.
	KindNotificationChanged = "notification.changed"
	// KindNotificationRead says a user marked notifications as read. It is
	// published on UserTopic of that user; UserID is the user.
	KindNotificationRead = "notification.read"
	// KindMessageCreated says a message was sent in a conversation. It is
	// published on ConversationTopic; MessageID is the message. It is also
	// published on UserTopic of each member whose preferences make the message
	// ring (SPEC §7.15), with Ring and Sound set; UserID is that member.
	KindMessageCreated = "message.created"
	// KindMessageUpdated says a message was edited. It is published on
	// ConversationTopic; MessageID is the message.
	KindMessageUpdated = "message.updated"
	// KindMessageDeleted says a message was deleted and now shows as "message
	// deleted". It is published on ConversationTopic; MessageID is the message.
	KindMessageDeleted = "message.deleted"
	// KindMessageReaction says a reaction was added to or removed from a
	// message. It is published on ConversationTopic; MessageID is the message.
	KindMessageReaction = "message.reaction"
	// KindConversationUpdated says a conversation was renamed or its members
	// changed. It is published on ConversationTopic.
	KindConversationUpdated = "conversation.updated"
	// KindConversationChanged says something changed in a conversation of a
	// user that the list of conversations and the unread count show. It is
	// published on UserTopic of every member (a member who left or was added
	// included); UserID is that user and ConversationID the conversation.
	KindConversationChanged = "conversation.changed"
	// KindUserChanged says the account of a user changed in a way that open
	// streams of the user must notice: the user was disabled, got another
	// role, or a session of the user ended (a logout). It is published on
	// UserTopic of that user; UserID is the user. The streams read the session
	// again and end when it is gone or its role differs.
	KindUserChanged = "user.changed"
	// KindUserUpdated says the profile of a user changed: the display name or
	// the avatar. It is published on UserTopic of that user only (SPEC §7.14);
	// UserID is the user. The pages of the user show the new profile in the
	// sidebar; a client of the WebSocket gets the user object.
	KindUserUpdated = "user.updated"
	// KindSettingsChanged says the settings of a user changed (the theme, the
	// sound, the browser notifications, what notifies). It is published on
	// UserTopic of that user; UserID is the user. A client of the WebSocket
	// gets the settings.
	KindSettingsChanged = "settings.changed"
	// KindPlacePrefsChanged says the notification preferences of a user in one
	// room or conversation changed (SPEC §7.15). It is published on UserTopic
	// of that user; UserID is the user, RoomID the room or ConversationID the
	// conversation. The sidebar and the list of conversations refresh, and a
	// client of the WebSocket gets the preferences.
	KindPlacePrefsChanged = "place_prefs.changed"
	// KindConversationRead says a member read a conversation up to a message.
	// It is published on ConversationTopic, which drives "Seen" (UserID is the
	// member, MessageID the message), and on UserTopic of the member, which
	// drives the unread count.
	KindConversationRead = "conversation.read"
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
	// ConversationID is the ID of the private conversation concerned; 0 when
	// none.
	ConversationID uint64
	// MessageID is the ID of the private message concerned; 0 when none.
	MessageID uint64
	// NotificationID is the ID of the notification of a notification.created
	// event; 0 for the others.
	NotificationID uint64
	// Ring is true when the event, published on the topic of one user,
	// rings for that user: the page plays the sound and shows the browser
	// notification as Sound and Browser say. The server decides it per
	// recipient from the preferences (SPEC §7.15); the page only obeys.
	Ring bool
	// Sound is the name of the sound to play when Ring is true ("purr",
	// "chirp", "bell" or "meow"); empty for no sound.
	Sound string
	// Browser is true when Ring is true and the user wants notifications of
	// the browser.
	Browser bool
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
