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
)

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
)

// AuditEvent records a change to a post, task, column, membership or room. It
// has no foreign key to the room or the entity: the event outlives them.
// Nothing edits or deletes an event.
type AuditEvent struct {
	// ID orders the log: newer events have larger IDs.
	ID        uint64    `gorm:"primaryKey;index:idx_audit_events_room,priority:2;index:idx_audit_events_actor,priority:2;index:idx_audit_events_action,priority:2"`
	CreatedAt time.Time `gorm:"index:idx_audit_events_created_at"`
	// ActorID is the user who made the change.
	ActorID uint64      `gorm:"not null;index:idx_audit_events_actor,priority:1"`
	Action  AuditAction `gorm:"not null;index:idx_audit_events_action,priority:1"`
	// RoomID is the room of the change. The room may be gone.
	RoomID     uint64     `gorm:"not null;index:idx_audit_events_room,priority:1"`
	EntityType EntityType `gorm:"not null;check:chk_audit_events_entity_type,entity_type IN ('room','post','comment','task','column','member')"`
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
