package live

import (
	"context"
	"sync"
)

// Presence says who is online (SPEC §7.15). A user is online while at least
// one page stream or WebSocket of theirs is open, in any process: the owner of
// the hub tells it when one opens (Connect) and when it closes (Disconnect),
// and anyone can ask who of some users is online now.
//
// MemoryPresence counts in memory, for one process; cluster.Presence keeps
// the data in Redis, so that the answer covers every process that shares it.
// Both pass the same tests (package presencetest).
type Presence interface {
	// Connect records one more open connection of the user.
	Connect(userID uint64)
	// Disconnect records that one connection of the user closed. A
	// Disconnect without a Connect does nothing.
	Disconnect(userID uint64)
	// Online returns, for each of the given users who is online now, true.
	// A user who is not online has no key in the map. It fails only when the
	// shared store cannot be read; the in-memory one never fails.
	Online(ctx context.Context, userIDs []uint64) (map[uint64]bool, error)
}

// MemoryPresence is a Presence that counts the connections of the users of
// one process in memory. Make it with NewMemoryPresence. It is safe for
// concurrent use and starts no goroutine.
type MemoryPresence struct {
	mu     sync.Mutex
	counts map[uint64]int
}

// NewMemoryPresence makes a presence with nobody online.
func NewMemoryPresence() *MemoryPresence {
	return &MemoryPresence{counts: map[uint64]int{}}
}

// Connect implements Presence.
func (p *MemoryPresence) Connect(userID uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.counts[userID]++
}

// Disconnect implements Presence.
func (p *MemoryPresence) Disconnect(userID uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch n := p.counts[userID]; {
	case n > 1:
		p.counts[userID] = n - 1
	case n == 1:
		delete(p.counts, userID)
	}
}

// Online implements Presence. It never fails.
func (p *MemoryPresence) Online(_ context.Context, userIDs []uint64) (map[uint64]bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[uint64]bool, len(userIDs))
	for _, id := range userIDs {
		if p.counts[id] > 0 {
			out[id] = true
		}
	}
	return out, nil
}

// Local returns the users who are online in this process, for a Presence that
// keeps the rest elsewhere: each user once, in no particular order.
func (p *MemoryPresence) Local() []uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]uint64, 0, len(p.counts))
	for id := range p.counts {
		out = append(out, id)
	}
	return out
}
