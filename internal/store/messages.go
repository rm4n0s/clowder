package store

import "time"

// ConversationKind says how many people a conversation has (SPEC §7.11).
type ConversationKind string

const (
	// ConversationDirect is a conversation between two users.
	ConversationDirect ConversationKind = "direct"
	// ConversationGroup is a conversation of up to 50 users.
	ConversationGroup ConversationKind = "group"
)

// Conversation is a stream of private messages between users (D28).
type Conversation struct {
	ID        uint64
	CreatedAt time.Time
	Kind      ConversationKind `gorm:"not null;check:chk_conversations_kind,kind IN ('direct','group')"`
	// Name is the optional name of a group; empty for none.
	Name      string `gorm:"not null"`
	CreatedBy uint64 `gorm:"not null"`
	// DirectKey identifies a direct conversation: the two user IDs, the
	// smaller first ("3-7"). It is unique, so a pair of users has one direct
	// conversation. Nil for a group.
	DirectKey *string `gorm:"uniqueIndex:uq_conversations_direct_key;check:chk_conversations_direct_key,(kind = 'direct') = (direct_key IS NOT NULL)"`

	// Relation: declared so that GORM creates the foreign key.
	Creator *User `gorm:"foreignKey:CreatedBy;constraint:OnDelete:RESTRICT"`
}

// ConversationMember is a user's membership of a conversation.
type ConversationMember struct {
	ID             uint64
	CreatedAt      time.Time
	ConversationID uint64 `gorm:"not null;uniqueIndex:uq_conversation_members_conversation_user,priority:1"`
	UserID         uint64 `gorm:"not null;uniqueIndex:uq_conversation_members_conversation_user,priority:2;index:idx_conversation_members_user_id"`
	// AddedBy is the member who added the user; nil for the member who
	// started the conversation.
	AddedBy *uint64
	// LastReadMessageID is the last message the member has read; nil if none.
	LastReadMessageID *uint64
	// LeftAt is when the member left the group; nil while a member.
	LeftAt *time.Time

	// Relations: declared so that GORM creates the foreign keys.
	Conversation    *Conversation `gorm:"foreignKey:ConversationID;constraint:OnDelete:CASCADE"`
	User            *User         `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT"`
	Adder           *User         `gorm:"foreignKey:AddedBy;constraint:OnDelete:RESTRICT"`
	LastReadMessage *Message      `gorm:"foreignKey:LastReadMessageID;constraint:OnDelete:SET NULL"`
}

// Message is a message of a conversation. It is plain text.
type Message struct {
	// ID orders the messages of a conversation.
	ID             uint64 `gorm:"primaryKey;index:idx_messages_conversation,priority:2"`
	CreatedAt      time.Time
	ConversationID uint64 `gorm:"not null;index:idx_messages_conversation,priority:1"`
	AuthorID       uint64 `gorm:"not null"`
	Body           string `gorm:"not null"`
	// ReplyToID is the earlier message that this one quotes; nil for none, and
	// set to nil when that message is deleted.
	ReplyToID *uint64
	// EditedAt is when the author last edited the message; nil if never.
	EditedAt *time.Time
	// DeletedAt is when the author deleted the message; the row stays so the
	// conversation shows "message deleted". The application sets it.
	DeletedAt *time.Time

	// Relations: declared so that GORM creates the foreign keys.
	Conversation *Conversation `gorm:"foreignKey:ConversationID;constraint:OnDelete:CASCADE"`
	Author       *User         `gorm:"foreignKey:AuthorID;constraint:OnDelete:RESTRICT"`
	ReplyTo      *Message      `gorm:"foreignKey:ReplyToID;constraint:OnDelete:SET NULL"`
}

// MessageAttachment is a file uploaded with a message; it has the fields of
// Attachment.
type MessageAttachment struct {
	ID         uint64
	CreatedAt  time.Time
	MessageID  uint64 `gorm:"not null;index:idx_message_attachments_message_id"`
	UploaderID uint64 `gorm:"not null"`
	// FileName is the name the file had when it was uploaded.
	FileName    string `gorm:"not null"`
	ContentType string `gorm:"not null"`
	// Size is the size in bytes.
	Size int64 `gorm:"not null;check:chk_message_attachments_size,size >= 0"`
	// StorageKey is the random name of the file in the data directory.
	StorageKey string `gorm:"not null;uniqueIndex:uq_message_attachments_storage_key"`
	// Width and Height are the size in pixels of an image; nil for any other
	// file.
	Width  *int
	Height *int

	// Relations: declared so that GORM creates the foreign keys.
	Message  *Message `gorm:"foreignKey:MessageID;constraint:OnDelete:CASCADE"`
	Uploader *User    `gorm:"foreignKey:UploaderID;constraint:OnDelete:RESTRICT"`
}

// MessageReaction is an emoji that a user put on a message, once per emoji.
type MessageReaction struct {
	ID        uint64
	CreatedAt time.Time
	MessageID uint64 `gorm:"not null;uniqueIndex:uq_message_reactions_message_user_emoji,priority:1"`
	UserID    uint64 `gorm:"not null;uniqueIndex:uq_message_reactions_message_user_emoji,priority:2"`
	Emoji     string `gorm:"not null;uniqueIndex:uq_message_reactions_message_user_emoji,priority:3"`

	// Relations: declared so that GORM creates the foreign keys.
	Message *Message `gorm:"foreignKey:MessageID;constraint:OnDelete:CASCADE"`
	User    *User    `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT"`
}
