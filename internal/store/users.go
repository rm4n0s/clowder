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
	// AvatarKey is the key of the user's avatar in the avatar store (a PNG of
	// 256x256 pixels); empty when the user has none (SPEC §7.14). A new avatar
	// gets a new key, which is what makes its address new.
	AvatarKey string `gorm:"not null;default:''"`
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
	// CreatedBy is the admin who made the invitation; nil for one made by
	// "clowder invite" on the command line.
	CreatedBy *uint64
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

// PasswordReset is a one-use link that lets a user who forgot their password
// set a new one (SPEC §7.1, D51). A user has at most one unused link: a newer
// link replaces the older one.
type PasswordReset struct {
	ID        uint64
	CreatedAt time.Time
	// UserID is the user who may set a new password with the link.
	UserID uint64 `gorm:"not null;index:idx_password_resets_user_id"`
	// TokenHash is the SHA-256 of the token in the link; the token itself is
	// never stored.
	TokenHash string `gorm:"not null;uniqueIndex:uq_password_resets_token_hash"`
	// CreatedBy is the admin who made the link; nil for one made by
	// "clowder reset-password" on the command line.
	CreatedBy *uint64
	ExpiresAt time.Time `gorm:"not null"`
	// UsedAt is when the link was used; nil until then.
	UsedAt *time.Time

	// Relations: declared so that GORM creates the foreign keys. The store
	// never loads or saves them.
	User    *User `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT"`
	Creator *User `gorm:"foreignKey:CreatedBy;constraint:OnDelete:RESTRICT"`
}

// SessionKind says where a session was made and where it is accepted (SPEC
// D43).
type SessionKind string

const (
	// SessionBrowser is a session made by the login or sign-up page; its token
	// is the session cookie, accepted by the pages only.
	SessionBrowser SessionKind = "browser"
	// SessionAPI is a session made by the login of the API; its token is a
	// bearer token, accepted by the API only.
	SessionAPI SessionKind = "api"
)

// MaxClientNameRunes is the longest ClientName of a session, in characters.
const MaxClientNameRunes = 80

// Session is a login of a user, found by the SHA-256 of its token.
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
	// Kind is where the session was made: a session of the pages is a
	// browser session, one of the API an api session. The default makes the
	// sessions of version 1.0 browser sessions.
	Kind SessionKind `gorm:"not null;default:browser"`
	// ClientName names the client of the session: the user agent of a
	// browser, cut to MaxClientNameRunes characters, or the name an API
	// client gave when it logged in. It is empty for the sessions of
	// version 1.0.
	ClientName string `gorm:"not null;default:''"`

	// Relation: declared so that GORM creates the foreign key.
	User *User `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT"`
}
