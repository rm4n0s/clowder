package app

import (
	"context"
	"encoding/json"
	"time"
	"unicode/utf8"

	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// EntityType is the kind of thing an audit event is about. It is the store's
// type, so the web layer can name the kinds without importing the store.
type EntityType = store.EntityType

// The kinds of entity of SPEC §7.10.
const (
	// EntityRoom is a room.
	EntityRoom = store.EntityRoom
	// EntityPost is a post.
	EntityPost = store.EntityPost
	// EntityComment is a comment: a post with a parent.
	EntityComment = store.EntityComment
	// EntityTask is a task.
	EntityTask = store.EntityTask
	// EntityColumn is a board column.
	EntityColumn = store.EntityColumn
	// EntityMember is a room membership.
	EntityMember = store.EntityMember
)

// AuditAction is what an audit event records. It is the store's type, so the
// web layer can name the actions without importing the store.
type AuditAction = store.AuditAction

// The actions of SPEC §7.10.
const (
	// ActionPostCreated is a new post or comment.
	ActionPostCreated = store.ActionPostCreated
	// ActionPostEdited is an edit of a post or comment.
	ActionPostEdited = store.ActionPostEdited
	// ActionPostDeleted is the deletion of a post or comment.
	ActionPostDeleted = store.ActionPostDeleted
	// ActionTaskCreated is a new task.
	ActionTaskCreated = store.ActionTaskCreated
	// ActionTaskEdited is a change of the title, assignees or due date of a task.
	ActionTaskEdited = store.ActionTaskEdited
	// ActionTaskMoved is a task moved to another column or position.
	ActionTaskMoved = store.ActionTaskMoved
	// ActionTaskDeleted is the deletion of a task.
	ActionTaskDeleted = store.ActionTaskDeleted
	// ActionColumnCreated is a new board column.
	ActionColumnCreated = store.ActionColumnCreated
	// ActionColumnRenamed is a renamed board column.
	ActionColumnRenamed = store.ActionColumnRenamed
	// ActionColumnMoved is a board column moved to another position.
	ActionColumnMoved = store.ActionColumnMoved
	// ActionColumnDeleted is the deletion of a board column.
	ActionColumnDeleted = store.ActionColumnDeleted
	// ActionMemberJoined is a user who joined a visible room.
	ActionMemberJoined = store.ActionMemberJoined
	// ActionMemberAdded is a user added to a room by another member.
	ActionMemberAdded = store.ActionMemberAdded
	// ActionMemberLeft is a member who left a room.
	ActionMemberLeft = store.ActionMemberLeft
	// ActionMemberRemoved is a member removed from a room by an admin.
	ActionMemberRemoved = store.ActionMemberRemoved
	// ActionRoomCreated is a new room.
	ActionRoomCreated = store.ActionRoomCreated
	// ActionRoomRenamed is a change of the name or description of a room.
	ActionRoomRenamed = store.ActionRoomRenamed
	// ActionRoomDeleted is the deletion of a room.
	ActionRoomDeleted = store.ActionRoomDeleted
)

// AuditFilter narrows Audit.Events (SPEC §7.10). A zero field does not
// filter; the filters that are set all have to match.
type AuditFilter = store.AuditFilter

// auditPageSize is the number of events on a page of the log (SPEC §7.10).
const auditPageSize = 50

// auditMaxPage is the largest page number that is queried; a larger one is an
// empty page. It keeps the offset far from overflowing.
const auditMaxPage = 1_000_000

// auditEntry is what a service gives recordAudit about one change.
type auditEntry struct {
	// ActorID is the user who made the change.
	ActorID uint64
	// Action is what happened.
	Action AuditAction
	// RoomID is the room of the change. The room may be deleted by this very
	// change.
	RoomID uint64
	// EntityType is the kind of the entity that changed.
	EntityType EntityType
	// EntityID is the ID of the entity that changed.
	EntityID uint64
	// Summary is a sentence that reads on its own after the entity is gone,
	// such as "Alice added Bob to #design".
	Summary string
	// Details holds the changed fields as {"old": ..., "new": ...}; for the
	// body of a post or comment only bodyChangeDetails, never the text. Nil is
	// stored as an empty object.
	Details map[string]any
}

// recordAudit writes the audit event of a change. A service method calls it
// inside its transaction, right after the change, with the transaction's
// Store, so that the event is committed or rolled back with the change (SPEC
// D24). It fails with the tag AuditDetailsFailed when Details cannot be made
// into JSON, and with the store's tags when the insert fails.
func recordAudit(ctx context.Context, tx *store.Store, ev auditEntry) error {
	details := ev.Details
	if details == nil {
		details = map[string]any{}
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return errors.NewErr(tagAuditDetailsFailed, err).
			SetMetadata("action", string(ev.Action), "entityType", string(ev.EntityType), "entityID", ev.EntityID)
	}
	return tx.CreateAuditEvent(ctx, &store.AuditEvent{
		ActorID:    ev.ActorID,
		Action:     ev.Action,
		RoomID:     ev.RoomID,
		EntityType: ev.EntityType,
		EntityID:   ev.EntityID,
		Summary:    ev.Summary,
		Details:    string(raw),
	})
}

// bodyChangeDetails is the Details of an event about the body of a post or
// comment: that it changed and how long the new body is in characters, never
// the text (SPEC §7.10).
func bodyChangeDetails(body string) map[string]any {
	return map[string]any{"bodyChanged": true, "length": utf8.RuneCountInString(body)}
}

// AuditView is an audit event as the admin pages show it.
type AuditView struct {
	// ID orders the log: newer events have larger IDs.
	ID uint64
	// CreatedAt is when the change was made.
	CreatedAt time.Time
	// ActorID is the user who made the change.
	ActorID uint64
	// ActorUsername is the username of the actor.
	ActorUsername string
	// ActorDisplayName is the display name of the actor.
	ActorDisplayName string
	// Action is what happened.
	Action AuditAction
	// RoomID is the room of the change. The room may be gone.
	RoomID uint64
	// EntityType is the kind of the entity that changed.
	EntityType EntityType
	// EntityID is the ID of the entity that changed. The entity may be gone.
	EntityID uint64
	// Summary reads on its own after the entity is gone.
	Summary string
	// Details is the decoded JSON of the event: the changed fields with their
	// old and new values. Numbers are float64.
	Details map[string]any
}

// AuditPage is one page of the audit log.
type AuditPage struct {
	// Events are the events of the page, newest first, at most 50.
	Events []AuditView
	// Page is the number of the page, starting at 1.
	Page int
	// HasNext is true when an older page exists.
	HasNext bool
}

// Audit is the service that reads the audit log (SPEC §7.10). Events are
// written by the other services, in their transactions, and nothing edits or
// deletes them. Make it with NewAudit. It is safe for concurrent use.
type Audit struct {
	st *store.Store
}

// NewAudit makes the service.
func NewAudit(st *store.Store) *Audit {
	return &Audit{st: st}
}

// Events returns a page of the audit log: the events that match filter, newest
// first, 50 a page. The first page is 1; a page below 1 is page 1, and a page
// past the last one is empty. Only admins may; anyone else gets TagForbidden.
// The log covers private rooms and rooms that are gone.
func (a *Audit) Events(ctx context.Context, actor Actor, filter AuditFilter, page int) (AuditPage, error) {
	if !actor.IsAdmin() {
		return AuditPage{}, errors.New(TagForbidden, "only admins read the audit log", "userID", actor.UserID)
	}
	if page < 1 {
		page = 1
	}
	if page > auditMaxPage {
		return AuditPage{Events: []AuditView{}, Page: page}, nil
	}
	// One more than a page tells whether a next page exists.
	rows, err := a.st.ListAuditEvents(ctx, filter, auditPageSize+1, (page-1)*auditPageSize)
	if err != nil {
		return AuditPage{}, err
	}
	hasNext := len(rows) > auditPageSize
	if hasNext {
		rows = rows[:auditPageSize]
	}
	views := make([]AuditView, len(rows))
	for i, row := range rows {
		views[i], err = auditViewOf(row)
		if err != nil {
			return AuditPage{}, err
		}
	}
	return AuditPage{Events: views, Page: page, HasNext: hasNext}, nil
}

// auditViewOf makes the view of a stored event, decoding its details. It
// fails with the tag AuditDetailsInvalid when the details are not a JSON
// object, which only a bug can store.
func auditViewOf(row store.AuditEvent) (AuditView, error) {
	var details map[string]any
	if err := json.Unmarshal([]byte(row.Details), &details); err != nil {
		return AuditView{}, errors.NewErr(tagAuditDetailsInvalid, err).SetMetadata("eventID", row.ID)
	}
	if details == nil {
		details = map[string]any{}
	}
	v := AuditView{
		ID:         row.ID,
		CreatedAt:  row.CreatedAt,
		ActorID:    row.ActorID,
		Action:     row.Action,
		RoomID:     row.RoomID,
		EntityType: row.EntityType,
		EntityID:   row.EntityID,
		Summary:    row.Summary,
		Details:    details,
	}
	if row.Actor != nil {
		v.ActorUsername = row.Actor.Username
		v.ActorDisplayName = row.Actor.DisplayName
	}
	return v, nil
}

const (
	tagAuditDetailsFailed  = "AuditDetailsFailed"
	tagAuditDetailsInvalid = "AuditDetailsInvalid"
)
