package store

import (
	"context"
	"time"

	"github.com/rm4n0s/errors"
)

// AuditFilter narrows ListAuditEvents (SPEC §7.10). A zero field does not
// filter; the filters that are set all have to match.
type AuditFilter struct {
	// RoomID keeps the events of this room. The room may be gone.
	RoomID uint64
	// ActorID keeps the events made by this user.
	ActorID uint64
	// EntityType keeps the events about this kind of entity.
	EntityType EntityType
	// Action keeps the events with this action.
	Action AuditAction
	// From keeps the events created at or after this time.
	From time.Time
	// To keeps the events created before this time.
	To time.Time
}

// CreateAuditEvent inserts ev and sets its ID and CreatedAt (the time of the
// Store's clock). Services call it inside the transaction of the change it
// records. It fails with TagConflict when the ID is taken, and with
// TagQueryFailed when the actor does not exist or Details is not valid JSON.
func (s *Store) CreateAuditEvent(ctx context.Context, ev *AuditEvent) error {
	err := s.db(ctx).Create(ev).Error
	if err != nil {
		switch classify(err) {
		case failureConflict:
			return errors.NewErr(TagConflict, err).SetMetadata("op", "createAuditEvent")
		default:
			return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "createAuditEvent")
		}
	}
	return nil
}

// ListAuditEvents returns the events that match filter, newest first (by ID),
// at most limit of them after skipping offset. Each event has its Actor
// loaded. The room and actor filters use the indexes of the table.
func (s *Store) ListAuditEvents(ctx context.Context, filter AuditFilter, limit, offset int) ([]AuditEvent, error) {
	q := s.db(ctx).Model(&AuditEvent{})
	if filter.RoomID != 0 {
		q = q.Where("room_id = ?", filter.RoomID)
	}
	if filter.ActorID != 0 {
		q = q.Where("actor_id = ?", filter.ActorID)
	}
	if filter.EntityType != "" {
		q = q.Where("entity_type = ?", filter.EntityType)
	}
	if filter.Action != "" {
		q = q.Where("action = ?", filter.Action)
	}
	if !filter.From.IsZero() {
		q = q.Where("created_at >= ?", filter.From.UTC())
	}
	if !filter.To.IsZero() {
		q = q.Where("created_at < ?", filter.To.UTC())
	}
	var events []AuditEvent
	err := q.Preload("Actor").Order("id DESC").Limit(limit).Offset(offset).Find(&events).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "listAuditEvents")
	}
	return events, nil
}
