package app

import (
	"sync"

	"github.com/rm4n0s/clowder/internal/metrics"
	"github.com/rm4n0s/errors"
)

// MaxConnectionsPerUser is how many live connections one user may hold in one
// process, event streams of the pages and WebSockets together (SPEC D62).
const MaxConnectionsPerUser = 30

// ConnectionsRetryAfterSeconds is the Retry-After of the 429 that refuses a
// stream or a WebSocket past the limit.
const ConnectionsRetryAfterSeconds = 30

// TagConnectionsLimited is the tag of a stream or a WebSocket refused because
// the user already holds MaxConnectionsPerUser of them in this process. The
// handlers answer 429 with Retry-After (ConnectionsRetryAfterSeconds); the
// metadata "limit" is the limit.
const TagConnectionsLimited = "ConnectionsLimited"

// Connections counts the live connections of each user in this process, for
// the limit of SPEC D62: the event streams of the pages, the user's WebSocket
// of the API and the signalling sockets of calls are all counted, together.
// The count is in memory and per process, not per cluster. Make it with
// NewConnections; the server that serve makes owns it and hands the one value
// to the pages and the API. It is safe for concurrent use. A nil *Connections
// counts and limits nothing.
type Connections struct {
	max     int
	refused *metrics.Counter

	mu     sync.Mutex
	counts map[uint64]int
}

// NewConnections makes a counter that allows max connections per user; a max
// below 1 is MaxConnectionsPerUser. Each refusal is added to refused, which
// may be nil.
func NewConnections(max int, refused *metrics.Counter) *Connections {
	if max < 1 {
		max = MaxConnectionsPerUser
	}
	return &Connections{max: max, refused: refused, counts: map[uint64]int{}}
}

// Acquire counts one more live connection of the user and returns the function
// that gives it back, which the caller defers right away. Calling it more than
// once gives back only one. When the user already holds the most it counts
// nothing and fails with TagConnectionsLimited (and the refusal is counted in
// the metrics). A user with no ID (0) is not counted. On a nil Connections it
// always succeeds.
func (c *Connections) Acquire(userID uint64) (release func(), err error) {
	if c == nil || userID == 0 {
		return func() {}, nil
	}
	c.mu.Lock()
	if c.counts[userID] >= c.max {
		c.mu.Unlock()
		c.refused.Inc()
		return nil, errors.New(TagConnectionsLimited, "the user holds too many live connections", "limit", c.max)
	}
	c.counts[userID]++
	c.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { c.release(userID) }) }, nil
}

// release gives back one connection of the user and forgets a user who holds
// none.
func (c *Connections) release(userID uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counts[userID] <= 1 {
		delete(c.counts, userID)
		return
	}
	c.counts[userID]--
}

// Open returns how many live connections the user holds now.
func (c *Connections) Open(userID uint64) int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[userID]
}

// Users returns how many users hold at least one live connection. It is 0 when
// every connection has ended, which the tests use to see that nothing leaked.
func (c *Connections) Users() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.counts)
}
