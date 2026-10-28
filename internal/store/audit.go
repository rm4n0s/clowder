package store

import "time"

// EntityType is the kind of thing that an audit event is about (SPEC §7.10).
type EntityType string

const (
	// EntityRoom is a room.
	EntityRoom EntityType = "room"
	// EntityPost is a post.
	EntityPost EntityType = "post"
	// EntityComment is a comment: a post with a parent.
	EntityComment EntityType = "comment"
	// EntityTask is a task.
	EntityTask EntityType = "task"
	// EntityColumn is a board column.
	EntityColumn EntityType = "column"
	// EntityMember is a room membership.
	EntityMember EntityType = "member"
	// EntityUser is a user's account (SPEC §7.10, 1.2). Its events belong to
	// no room: their RoomID is NoRoom.
	EntityUser EntityType = "user"
)

// NoRoom is the RoomID of an audit event that belongs to no room, such as the
// events about an account. No room has the ID 0, and nothing has a foreign key
// on the column.
const NoRoom uint64 = 0

// AuditAction is what an audit event records (SPEC §7.10).
type AuditAction string

// The actions of SPEC §7.10.
const (
	ActionPostCreated   AuditAction = "post.created"
	ActionPostEdited    AuditAction = "post.edited"
	ActionPostDeleted   AuditAction = "post.deleted"
	ActionTaskCreated   AuditAction = "task.created"
	ActionTaskEdited    AuditAction = "task.edited"
	ActionTaskMoved     AuditAction = "task.moved"
	ActionTaskDeleted   AuditAction = "task.deleted"
	ActionColumnCreated AuditAction = "column.created"
	ActionColumnRenamed AuditAction = "column.renamed"
	ActionColumnMoved   AuditAction = "column.moved"
	ActionColumnDeleted AuditAction = "column.deleted"
	ActionMemberJoined  AuditAction = "member.joined"
	ActionMemberAdded   AuditAction = "member.added"
	ActionMemberLeft    AuditAction = "member.left"
	ActionMemberRemoved AuditAction = "member.removed"
	ActionRoomCreated   AuditAction = "room.created"
	ActionRoomRenamed   AuditAction = "room.renamed"
	ActionRoomDeleted   AuditAction = "room.deleted"
	// ActionUserPasswordChanged is a user who changed their own password
	// (SPEC §7.14).
	ActionUserPasswordChanged AuditAction = "user.password_changed"
	// ActionUserResetLinkCreated is a reset link made for a user by an admin
	// or on the command line (SPEC §7.1, D51).
	ActionUserResetLinkCreated AuditAction = "user.reset_link_created"
	// ActionUserPasswordReset is a user who set a new password with a reset
	// link.
	ActionUserPasswordReset AuditAction = "user.password_reset"
	// ActionUserSSHKeyAdded is a user who registered an SSH key (SPEC §7.16).
	ActionUserSSHKeyAdded AuditAction = "user.ssh_key_added"
	// ActionUserSSHKeyRemoved is a user who removed an SSH key.
	ActionUserSSHKeyRemoved AuditAction = "user.ssh_key_removed"
	// ActionUserSSHCodeEnabled is a user who turned on the SSH code at login
	// (SPEC §7.16).
	ActionUserSSHCodeEnabled AuditAction = "user.ssh_code_enabled"
	// ActionUserSSHCodeDisabled is a user who turned off the SSH code at
	// login.
	ActionUserSSHCodeDisabled AuditAction = "user.ssh_code_disabled"
	// ActionUserSSHCodeRemoved is an admin who turned off the SSH code of
	// another user.
	ActionUserSSHCodeRemoved AuditAction = "user.ssh_code_removed"
)

// AuditEvent records a change to a post, task, column, membership, room or
// account. It
// has no foreign key to the room or the entity: the event outlives them.
// Nothing edits or deletes an event.
type AuditEvent struct {
	// ID orders the log: newer events have larger IDs.
	ID        uint64    `gorm:"primaryKey;index:idx_audit_events_room,priority:2;index:idx_audit_events_actor,priority:2;index:idx_audit_events_action,priority:2"`
	CreatedAt time.Time `gorm:"index:idx_audit_events_created_at"`
	// ActorID is the user who made the change.
	ActorID uint64      `gorm:"not null;index:idx_audit_events_actor,priority:1"`
	Action  AuditAction `gorm:"not null;index:idx_audit_events_action,priority:1"`
	// RoomID is the room of the change. The room may be gone. It is NoRoom for
	// an event about an account.
	RoomID     uint64     `gorm:"not null;index:idx_audit_events_room,priority:1"`
	EntityType EntityType `gorm:"not null;check:chk_audit_events_entity_type,entity_type IN ('room','post','comment','task','column','member','user')"`
	// EntityID is the ID of the entity. The entity may be gone.
	EntityID uint64 `gorm:"not null"`
	// Summary reads on its own after the entity is gone.
	Summary string `gorm:"not null"`
	// Details is a JSON object with the changed fields and their old and new
	// values.
	Details string `gorm:"not null;check:chk_audit_events_details_json,json_valid(details)"`

	// Relation: declared so that GORM creates the foreign key. The only one
	// of the table.
	Actor *User `gorm:"foreignKey:ActorID;constraint:OnDelete:RESTRICT"`
}
