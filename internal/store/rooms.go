package store

import "time"

// Visibility says who sees a room (SPEC §7.2).
type Visibility string

const (
	// VisibilityVisible is a room that every user sees and reads (D26).
	VisibilityVisible Visibility = "visible"
	// VisibilityPrivate is a room that only its members see.
	VisibilityPrivate Visibility = "private"
)

// Room is a feed of posts with a board. Deleting a room deletes everything
// that belongs to it, except its audit events (SPEC §7.10).
type Room struct {
	ID        uint64
	CreatedAt time.Time
	// Name is unique without regard to case (the column collates NOCASE).
	Name        string     `gorm:"type:text COLLATE NOCASE;not null;uniqueIndex:uq_rooms_name"`
	Description string     `gorm:"not null"`
	Visibility  Visibility `gorm:"not null;check:chk_rooms_visibility,visibility IN ('visible','private')"`
	CreatedBy   uint64     `gorm:"not null"`

	// Relation: declared so that GORM creates the foreign key.
	Creator *User `gorm:"foreignKey:CreatedBy;constraint:OnDelete:RESTRICT"`
}

// RoomMember is a user's membership of a room. A member of a visible room
// posts, reacts and changes the board; a member of a private room also sees it.
type RoomMember struct {
	ID        uint64
	CreatedAt time.Time
	RoomID    uint64 `gorm:"not null;uniqueIndex:uq_room_members_room_user,priority:1"`
	UserID    uint64 `gorm:"not null;uniqueIndex:uq_room_members_room_user,priority:2;index:idx_room_members_user_id"`
	// AddedBy is the member who added the user to the room; nil when the user
	// joined by themselves (or created the room).
	AddedBy *uint64

	// Relations: declared so that GORM creates the foreign keys.
	Room  *Room `gorm:"foreignKey:RoomID;constraint:OnDelete:CASCADE"`
	User  *User `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT"`
	Adder *User `gorm:"foreignKey:AddedBy;constraint:OnDelete:RESTRICT"`
}
