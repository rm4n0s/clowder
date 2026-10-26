package store

import "time"

// Role is what a user may do in Clowder (SPEC D14).
type Role string

const (
	// RoleAdmin is a user who manages users, invitations and rooms.
	RoleAdmin Role = "admin"
	// RoleMember is an ordinary user.
	RoleMember Role = "member"
)

// User is a person who can log in. Users are never deleted: an admin disables
// them, so every foreign key to a user restricts its deletion.
type User struct {
	ID        uint64
	CreatedAt time.Time
	// Username is unique and stored in lower case (the database refuses
	// anything else).
	Username    string `gorm:"not null;uniqueIndex:uq_users_username;check:chk_users_username_lower,username = lower(username)"`
	DisplayName string `gorm:"not null"`
	// PasswordHash is the argon2id hash in the PHC string format.
	PasswordHash string `gorm:"not null"`
	Role         Role   `gorm:"not null;check:chk_users_role,role IN ('admin','member')"`
	// DisabledAt is when an admin disabled the user; nil for an active user.
	DisabledAt *time.Time
}

// Invitation is a one-use link that lets a person sign up (SPEC §7.1).
type Invitation struct {
	ID        uint64
	CreatedAt time.Time
	// TokenHash is the SHA-256 of the token in the link; the token itself is
	// never stored.
	TokenHash string `gorm:"not null;uniqueIndex:uq_invitations_token_hash"`
	// Role is the role the new user gets.
	Role Role `gorm:"not null;check:chk_invitations_role,role IN ('admin','member')"`
	// CreatedBy is the admin who made the invitation.
	CreatedBy uint64    `gorm:"not null"`
	ExpiresAt time.Time `gorm:"not null"`
	// UsedBy is the user the invitation made; nil until it is used.
	UsedBy *uint64
	// UsedAt is when the invitation was used; nil until then.
	UsedAt *time.Time

	// Relations: declared so that GORM creates the foreign keys. The store
	// never loads or saves them.
	Creator *User `gorm:"foreignKey:CreatedBy;constraint:OnDelete:RESTRICT"`
	User    *User `gorm:"foreignKey:UsedBy;constraint:OnDelete:RESTRICT"`
}

// Session is a login of a user, found by the SHA-256 of its cookie token.
type Session struct {
	ID        uint64
	CreatedAt time.Time
	// TokenHash is the SHA-256 of the cookie token; the token is never stored.
	TokenHash string    `gorm:"not null;uniqueIndex:uq_sessions_token_hash"`
	UserID    uint64    `gorm:"not null;index:idx_sessions_user_id"`
	ExpiresAt time.Time `gorm:"not null"`
	// LastSeenAt is when the session was last used; a session lasts a set time
	// from this moment (SPEC §7.1).
	LastSeenAt time.Time `gorm:"not null"`

	// Relation: declared so that GORM creates the foreign key.
	User *User `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT"`
}
