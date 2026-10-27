package app

import (
	"context"

	"github.com/rm4n0s/clowder/internal/store"
)

// Health is the service behind the health check of the server (SPEC D40).
type Health struct {
	st *store.Store
}

// NewHealth makes the health service on the store.
func NewHealth(st *store.Store) *Health {
	return &Health{st: st}
}

// Check reports whether the database answers a ping within ctx. It returns nil
// when it does. It has no actor and no permission check: it is not an action of
// a user, anyone who can reach the server may ask, and it reveals nothing but
// the answer. A failure is returned as the store made it.
func (h *Health) Check(ctx context.Context) error {
	return h.st.Ping(ctx)
}
