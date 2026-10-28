package app

import (
	"context"

	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// PresenceMaxUsers is the most users one call of Presence.Online may ask
// about.
const PresenceMaxUsers = 100

// FieldUserIDs names the list of users that Presence.Online was given in the
// metadata "fields" of a TagInvalidInput error.
const FieldUserIDs = "user_ids"

// Presence is the service that tells who is online (SPEC §7.15): a user is
// online while a page stream or a WebSocket of theirs is open, in any process.
// Presence is not private: any user who is logged in may ask about any users.
// Make it with NewPresence. It is safe for concurrent use.
type Presence struct {
	st       *store.Store
	presence live.Presence
}

// NewPresence makes the service over the presence of the process (or of the
// cluster); nil means that nobody is online.
func NewPresence(st *store.Store, presence live.Presence) *Presence {
	return &Presence{st: st, presence: orMemoryPresence(presence)}
}

// Online returns the set of those among userIDs who are online now and whose
// account is not disabled; a user who is offline, disabled or unknown has no
// key. At most 100 IDs may be asked: more fails with TagInvalidInput, with the
// message under FieldUserIDs. A disabled or missing account fails with
// TagUnauthenticated. When the shared presence cannot be read (Redis) it fails
// with the error of the presence.
func (p *Presence) Online(ctx context.Context, actor Actor, userIDs []uint64) (map[uint64]bool, error) {
	if _, err := checkActive(ctx, p.st, actor); err != nil {
		return nil, err
	}
	if len(userIDs) > PresenceMaxUsers {
		return nil, errors.New(TagInvalidInput, "too many users", "fields",
			map[string]string{FieldUserIDs: "Ask about at most 100 users."})
	}
	online, err := p.presence.Online(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	var ids []uint64
	for id, ok := range online {
		if ok {
			ids = append(ids, id)
		}
	}
	enabled, err := p.st.EnabledUserSet(ctx, ids)
	if err != nil {
		return nil, err
	}
	return enabled, nil
}

// Connect records one more open page stream or WebSocket of the user. The
// streams call it when they open. On a nil service it does nothing.
func (p *Presence) Connect(userID uint64) {
	if p != nil {
		p.presence.Connect(userID)
	}
}

// Disconnect records that one of the open connections of the user closed. On a
// nil service it does nothing.
func (p *Presence) Disconnect(userID uint64) {
	if p != nil {
		p.presence.Disconnect(userID)
	}
}

// OnlineIn returns the set of those among the members of a place who are
// online now, for the dots of a members list. It does not check any
// permission: the caller has read the members already. A nil service, and a
// presence that cannot be read, say that nobody is online.
func (p *Presence) OnlineIn(ctx context.Context, userIDs []uint64) map[uint64]bool {
	if p == nil {
		return nil
	}
	online, err := p.presence.Online(ctx, userIDs)
	if err != nil {
		return nil
	}
	return online
}
