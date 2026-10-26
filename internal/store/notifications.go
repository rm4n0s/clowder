package store

import "time"

// NotificationKind says why a user is notified (SPEC §7.9).
type NotificationKind string

const (
	// NotificationMention is an "@username" in a post or comment.
	NotificationMention NotificationKind = "mention"
	// NotificationReply is a comment under the user's post or comment.
	NotificationReply NotificationKind = "reply"
	// NotificationAssigned is a task assigned to the user.
	NotificationAssigned NotificationKind = "assigned"
	// NotificationAddedToRoom is the user's addition to a private room.
	NotificationAddedToRoom NotificationKind = "added_to_room"
)

// Notification is shown to a user in the bell and on the notifications page.
// It is deleted with the post, task or room it points to.
type Notification struct {
	ID        uint64 `gorm:"primaryKey;index:idx_notifications_user_read,priority:3"`
	CreatedAt time.Time
	UserID    uint64           `gorm:"not null;index:idx_notifications_user_read,priority:1"`
	Kind      NotificationKind `gorm:"not null;check:chk_notifications_kind,kind IN ('mention','reply','assigned','added_to_room')"`
	// PostID is the post or comment of a mention or a reply.
	PostID *uint64 `gorm:"index:idx_notifications_post_id"`
	// TaskID is the task of an assignment.
	TaskID *uint64 `gorm:"index:idx_notifications_task_id"`
	// RoomID is the room of the notification.
	RoomID *uint64 `gorm:"index:idx_notifications_room_id"`
	// ActorID is the user whose action caused the notification.
	ActorID uint64 `gorm:"not null"`
	// ReadAt is when the user read the notification; nil while unread.
	ReadAt *time.Time `gorm:"index:idx_notifications_user_read,priority:2"`

	// Relations: declared so that GORM creates the foreign keys.
	User  *User `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT"`
	Post  *Post `gorm:"foreignKey:PostID;constraint:OnDelete:CASCADE"`
	Task  *Task `gorm:"foreignKey:TaskID;constraint:OnDelete:CASCADE"`
	Room  *Room `gorm:"foreignKey:RoomID;constraint:OnDelete:CASCADE"`
	Actor *User `gorm:"foreignKey:ActorID;constraint:OnDelete:RESTRICT"`
}
