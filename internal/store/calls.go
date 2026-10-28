package store

import (
	"context"
	"slices"
	"time"

	"github.com/rm4n0s/errors"
	"gorm.io/gorm/clause"
)

// CallState says where a call is (SPEC §7.18).
type CallState string

const (
	// CallRinging is a call that the callee has not answered yet.
	CallRinging CallState = "ringing"
	// CallActive is a call that the callee answered and that has not ended.
	CallActive CallState = "active"
	// CallEnded is a call that is over; EndReason says how it ended.
	CallEnded CallState = "ended"
)

// CallEndReason says how a call ended.
type CallEndReason string

const (
	// CallHangup is a call that one side ended while it was active.
	CallHangup CallEndReason = "hangup"
	// CallCancelled is a call that the caller ended while it rang.
	CallCancelled CallEndReason = "cancelled"
	// CallDeclined is a call that the callee refused.
	CallDeclined CallEndReason = "declined"
	// CallMissed is a call that nobody answered within the ringing time.
	CallMissed CallEndReason = "missed"
	// CallBusy is a call to someone who was in another call.
	CallBusy CallEndReason = "busy"
	// CallFailed is a call whose media connection failed or was lost.
	CallFailed CallEndReason = "failed"
	// CallDisabled is a call that ended because a participant was disabled.
	CallDisabled CallEndReason = "disabled"
	// CallSessionEnded is a call that ended because a participant's session
	// ended (a logout, a session ended from the list, a password change).
	CallSessionEnded CallEndReason = "session_ended"
	// CallServerStopped is a call that ended because the process that held
	// its media stopped.
	CallServerStopped CallEndReason = "server_stopped"
)

// Call is a voice or video call between the two users of a direct
// conversation (SPEC §7.18, D63). Its row is made when the call starts and
// changes when it is answered and when it ends; a call that ended leaves its
// entry in the conversation (a Message of kind call).
type Call struct {
	ID        uint64
	CreatedAt time.Time
	// ConversationID is the direct conversation of the call.
	ConversationID uint64 `gorm:"not null;index:idx_calls_conversation,priority:1"`
	// CallerID started the call and CalleeID was called.
	CallerID uint64 `gorm:"not null"`
	CalleeID uint64 `gorm:"not null"`
	// Video is true when the caller started the call with video.
	Video bool `gorm:"not null"`
	// State is where the call is. Raw migration 0007 allows one open call
	// (ringing or active) per caller and one per callee.
	State CallState `gorm:"not null;check:chk_calls_state,state IN ('ringing','active','ended')"`
	// AnsweredAt is when the callee answered; nil if never.
	AnsweredAt *time.Time
	// EndedAt is when the call ended; nil while it is open.
	EndedAt *time.Time `gorm:"check:chk_calls_ended,(state = 'ended') = (ended_at IS NOT NULL)"`
	// EndReason is how the call ended; empty while it is open.
	EndReason CallEndReason `gorm:"not null;default:'';check:chk_calls_end_reason,end_reason IN ('','hangup','cancelled','declined','missed','busy','failed','disabled','session_ended','server_stopped')"`
	// EndedBy is the user who ended the call (hung up, cancelled, declined);
	// nil when the system ended it.
	EndedBy *uint64
	// Home is the ID of the process that holds the media of the call (SPEC
	// D65).
	Home string `gorm:"not null"`

	// Relations: declared so that GORM creates the foreign keys.
	Conversation *Conversation `gorm:"foreignKey:ConversationID;constraint:OnDelete:CASCADE"`
	Caller       *User         `gorm:"foreignKey:CallerID;constraint:OnDelete:RESTRICT"`
	Callee       *User         `gorm:"foreignKey:CalleeID;constraint:OnDelete:RESTRICT"`
	Ender        *User         `gorm:"foreignKey:EndedBy;constraint:OnDelete:RESTRICT"`
}

// Open reports whether the call is ringing or active.
func (c Call) Open() bool { return c.State != CallEnded }

// LockUsersForCall makes the rest of the transaction the only one that starts
// or answers a call of these users: on PostgreSQL it locks their rows, in the
// order of their IDs so that two transactions cannot wait for each other, and
// another transaction that locks one of them waits until this one ends. A
// start reads the open calls of both users and then writes only after it
// called this, so that two calls started at the same time cannot both ring.
// Call it inside Tx. On SQLite it does nothing, because a write transaction is
// already alone (BEGIN IMMEDIATE).
func (s *Store) LockUsersForCall(ctx context.Context, userIDs ...uint64) error {
	if s.dialect != dialectPostgres {
		return nil
	}
	ids := slices.Clone(userIDs)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	var locked []uint64
	err := s.db(ctx).Model(&User{}).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ?", ids).Order("id ASC").Pluck("id", &locked).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "lockUsersForCall")
	}
	return nil
}

// CreateCall inserts the call and sets its ID and CreatedAt. It fails with
// TagConflict when the caller or the callee already has an open call: raw
// migration 0007 makes that a unique violation, the last guard behind the
// check that Calls.Start makes.
func (s *Store) CreateCall(ctx context.Context, c *Call) error {
	err := s.db(ctx).Create(c).Error
	if err != nil {
		switch classify(err) {
		case failureConflict:
			return errors.NewErr(TagConflict, err).SetMetadata("op", "createCall", "conversationID", c.ConversationID)
		default:
			return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "createCall", "conversationID", c.ConversationID)
		}
	}
	return nil
}

// CallByID returns the call with the given ID. It fails with TagNotFound when
// there is none.
func (s *Store) CallByID(ctx context.Context, id uint64) (Call, error) {
	var c Call
	err := s.db(ctx).First(&c, id).Error
	if err != nil {
		switch classify(err) {
		case failureNotFound:
			return Call{}, errors.NewErr(TagNotFound, err).SetMetadata("op", "callByID", "callID", id)
		default:
			return Call{}, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "callByID", "callID", id)
		}
	}
	return c, nil
}

// CallsByIDs returns the calls with these IDs, by ID, with one query. An ID
// that is no call has no key in the map.
func (s *Store) CallsByIDs(ctx context.Context, ids []uint64) (map[uint64]Call, error) {
	out := map[uint64]Call{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []Call
	if err := s.db(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "callsByIDs")
	}
	for _, c := range rows {
		out[c.ID] = c
	}
	return out, nil
}

// OpenCallOfConversation returns the call of the conversation that is ringing
// or active. It fails with TagNotFound when there is none. A direct
// conversation has at most one: both its users are in one call at most.
func (s *Store) OpenCallOfConversation(ctx context.Context, conversationID uint64) (Call, error) {
	var c Call
	err := s.db(ctx).Where("conversation_id = ? AND state <> ?", conversationID, CallEnded).
		Order("id DESC").First(&c).Error
	if err != nil {
		switch classify(err) {
		case failureNotFound:
			return Call{}, errors.NewErr(TagNotFound, err).SetMetadata("op", "openCallOfConversation", "conversationID", conversationID)
		default:
			return Call{}, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "openCallOfConversation", "conversationID", conversationID)
		}
	}
	return c, nil
}

// OpenCallsOfUsers returns the calls, ringing or active, in which any of
// these users is the caller or the callee, oldest first.
func (s *Store) OpenCallsOfUsers(ctx context.Context, userIDs ...uint64) ([]Call, error) {
	var rows []Call
	err := s.db(ctx).Where("state <> ? AND (caller_id IN ? OR callee_id IN ?)", CallEnded, userIDs, userIDs).
		Order("id ASC").Find(&rows).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "openCallsOfUsers")
	}
	return rows, nil
}

// CountOpenCallsOfHome returns the number of open calls whose media the
// process home holds.
func (s *Store) CountOpenCallsOfHome(ctx context.Context, home string) (int64, error) {
	var n int64
	err := s.db(ctx).Model(&Call{}).Where("state <> ? AND home = ?", CallEnded, home).Count(&n).Error
	if err != nil {
		return 0, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "countOpenCallsOfHome")
	}
	return n, nil
}

// OpenCalls returns every open call, oldest first. With home set it returns
// the open calls of that process only.
func (s *Store) OpenCalls(ctx context.Context, home string) ([]Call, error) {
	q := s.db(ctx).Where("state <> ?", CallEnded)
	if home != "" {
		q = q.Where("home = ?", home)
	}
	var rows []Call
	if err := q.Order("id ASC").Find(&rows).Error; err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "openCalls")
	}
	return rows, nil
}

// RingingCallsBefore returns the calls that have been ringing since before
// the time, oldest first: those that Calls.Expire ends as missed.
func (s *Store) RingingCallsBefore(ctx context.Context, before time.Time) ([]Call, error) {
	var rows []Call
	err := s.db(ctx).Where("state = ? AND created_at <= ?", CallRinging, before.UTC()).
		Order("id ASC").Find(&rows).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "ringingCallsBefore")
	}
	return rows, nil
}

// MarkCallAnswered moves the call from ringing to active and sets AnsweredAt.
// It returns false, and changes nothing, when the call is not ringing.
func (s *Store) MarkCallAnswered(ctx context.Context, id uint64, at time.Time) (bool, error) {
	res := s.db(ctx).Model(&Call{}).Where("id = ? AND state = ?", id, CallRinging).
		Updates(map[string]any{"state": CallActive, "answered_at": at.UTC()})
	if res.Error != nil {
		return false, errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "markCallAnswered", "callID", id)
	}
	return res.RowsAffected > 0, nil
}

// MarkCallEnded ends the call: state, time, reason and who ended it (nil when
// the system did). It returns false, and changes nothing, when the call has
// ended already.
func (s *Store) MarkCallEnded(ctx context.Context, id uint64, reason CallEndReason, by *uint64, at time.Time) (bool, error) {
	res := s.db(ctx).Model(&Call{}).Where("id = ? AND state <> ?", id, CallEnded).
		Updates(map[string]any{"state": CallEnded, "ended_at": at.UTC(), "end_reason": reason, "ended_by": by})
	if res.Error != nil {
		return false, errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "markCallEnded", "callID", id)
	}
	return res.RowsAffected > 0, nil
}
