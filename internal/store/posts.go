package store

import "time"

// Format says how the body of a post is shown (SPEC §7.3).
type Format string

const (
	// FormatText is a body shown as written: escaped, line breaks kept, links
	// made clickable.
	FormatText Format = "text"
	// FormatMarkdown is a body rendered as markdown.
	FormatMarkdown Format = "markdown"
)

// Post is a post in a room, or a comment: a comment is a post with a parent
// (D16). Everything that hangs on a post is deleted with it, and so are its
// comments.
type Post struct {
	// ID orders the feed and the threads: newer posts have larger IDs.
	ID        uint64 `gorm:"primaryKey;index:idx_posts_feed,priority:3,sort:desc;index:idx_posts_thread,priority:2"`
	CreatedAt time.Time
	RoomID    uint64 `gorm:"not null;index:idx_posts_feed,priority:1"`
	AuthorID  uint64 `gorm:"not null"`
	// ParentID is the post or comment this comment answers; nil for a post.
	ParentID *uint64 `gorm:"index:idx_posts_feed,priority:2;index:idx_posts_parent_id"`
	// RootID is the post a comment belongs to, at any depth; nil for a post.
	RootID *uint64 `gorm:"index:idx_posts_thread,priority:1;check:chk_posts_root_with_parent,(parent_id IS NULL) = (root_id IS NULL)"`
	Format Format  `gorm:"not null;check:chk_posts_format,format IN ('text','markdown')"`
	Body   string  `gorm:"not null"`
	// EditedAt is when the author last edited the post; nil if never.
	EditedAt *time.Time
	// DeletedAt marks a deleted post that stays because it has replies (D21).
	// The application sets it; it is not GORM's soft delete.
	DeletedAt *time.Time

	// Relations: declared so that GORM creates the foreign keys.
	Room   *Room `gorm:"foreignKey:RoomID;constraint:OnDelete:CASCADE"`
	Author *User `gorm:"foreignKey:AuthorID;constraint:OnDelete:RESTRICT"`
	Parent *Post `gorm:"foreignKey:ParentID;constraint:OnDelete:CASCADE"`
	Root   *Post `gorm:"foreignKey:RootID;constraint:OnDelete:CASCADE"`
}

// Tag is a label on posts. Its name is normalized by the application: lower
// case, without a leading "#".
type Tag struct {
	ID        uint64
	CreatedAt time.Time
	Name      string `gorm:"not null;uniqueIndex:uq_tags_name"`
}

// PostTag links a post to a tag.
type PostTag struct {
	ID        uint64
	CreatedAt time.Time
	PostID    uint64 `gorm:"not null;uniqueIndex:uq_post_tags_post_tag,priority:1"`
	TagID     uint64 `gorm:"not null;uniqueIndex:uq_post_tags_post_tag,priority:2;index:idx_post_tags_tag_id"`

	// Relations: declared so that GORM creates the foreign keys.
	Post *Post `gorm:"foreignKey:PostID;constraint:OnDelete:CASCADE"`
	Tag  *Tag  `gorm:"foreignKey:TagID;constraint:OnDelete:CASCADE"`
}

// Attachment is a file uploaded with a post or a comment. The bytes are on
// disk under StorageKey (SPEC §7.5); this row holds what the page shows.
type Attachment struct {
	ID         uint64
	CreatedAt  time.Time
	PostID     uint64 `gorm:"not null;index:idx_attachments_post_id"`
	UploaderID uint64 `gorm:"not null"`
	// FileName is the name the file had when it was uploaded.
	FileName    string `gorm:"not null"`
	ContentType string `gorm:"not null"`
	// Size is the size in bytes.
	Size int64 `gorm:"not null;check:chk_attachments_size,size >= 0"`
	// StorageKey is the random name of the file in the data directory.
	StorageKey string `gorm:"not null;uniqueIndex:uq_attachments_storage_key"`
	// Width and Height are the size in pixels of an image; nil for any other
	// file.
	Width  *int
	Height *int

	// Relations: declared so that GORM creates the foreign keys.
	Post     *Post `gorm:"foreignKey:PostID;constraint:OnDelete:CASCADE"`
	Uploader *User `gorm:"foreignKey:UploaderID;constraint:OnDelete:RESTRICT"`
}

// Reaction is an emoji that a user put on a post or a comment, once per
// emoji.
type Reaction struct {
	ID        uint64
	CreatedAt time.Time
	PostID    uint64 `gorm:"not null;uniqueIndex:uq_reactions_post_user_emoji,priority:1"`
	UserID    uint64 `gorm:"not null;uniqueIndex:uq_reactions_post_user_emoji,priority:2"`
	Emoji     string `gorm:"not null;uniqueIndex:uq_reactions_post_user_emoji,priority:3"`

	// Relations: declared so that GORM creates the foreign keys.
	Post *Post `gorm:"foreignKey:PostID;constraint:OnDelete:CASCADE"`
	User *User `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT"`
}

// MentionKind says how a user is mentioned in a post (SPEC §7.15): by name,
// by a group mention, or by one of their keywords.
type MentionKind string

const (
	// MentionUser is an "@username": it is shown as a link in the body.
	MentionUser MentionKind = "user"
	// MentionGroup is an @room, @all or @here that reached the user.
	MentionGroup MentionKind = "group"
	// MentionKeyword is a keyword of the user found in the body.
	MentionKeyword MentionKind = "keyword"
)

// Mention is a user who is mentioned in the body of a post: by "@username",
// by a group mention or by a keyword (Kind). The application rebuilds a
// post's mentions when the post is edited.
type Mention struct {
	ID        uint64
	CreatedAt time.Time
	PostID    uint64 `gorm:"not null;uniqueIndex:uq_mentions_post_user,priority:1"`
	UserID    uint64 `gorm:"not null;uniqueIndex:uq_mentions_post_user,priority:2"`
	// Kind says how the user is mentioned; the default is MentionUser.
	Kind MentionKind `gorm:"not null;default:user"`

	// Relations: declared so that GORM creates the foreign keys.
	Post *Post `gorm:"foreignKey:PostID;constraint:OnDelete:CASCADE"`
	User *User `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT"`
}
