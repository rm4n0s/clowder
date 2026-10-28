package cluster

import (
	"context"
	"crypto/rand"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/live"
)

const (
	// presenceKeyPrefix starts the key of the set of online users of one
	// process in Redis: clowder:presence:<process id>.
	presenceKeyPrefix = "clowder:presence:"
	// presenceProcessesKey is the sorted set of the live processes: the
	// members are process ids, the scores the time of their last heartbeat in
	// milliseconds of the clock of Redis.
	presenceProcessesKey = presenceKeyPrefix + "processes"
	// presenceTTL is how long the set of a process lives in Redis without a
	// refresh, and how old a heartbeat may be for the process to count as
	// alive. A process that dies without cleaning up disappears after this.
	presenceTTL = 60 * time.Second
	// presenceRefreshEvery is how often a process writes its users again.
	presenceRefreshEvery = 20 * time.Second
	// presenceTimeout bounds one call to Redis made for Connect, Disconnect or
	// Online, so that a Redis that hangs delays them by this much at most.
	presenceTimeout = 2 * time.Second
	// presenceDropTimeout bounds the removal of the data of the process at
	// Close.
	presenceDropTimeout = 2 * time.Second
)

// presenceBackend is the part of the Redis client that Presence uses.
// redisClient is the implementation over go-redis; tests use an in-memory
// fake. Every error a backend returns is already tagged.
type presenceBackend interface {
	// addUser puts the user in the set of the process, makes the set expire after
	// ttl and writes the heartbeat of the process, in one atomic step.
	addUser(ctx context.Context, process string, userID uint64, ttl time.Duration) error
	// removeUser takes the user out of the set of the process.
	removeUser(ctx context.Context, process string, userID uint64) error
	// refreshProcess replaces the set of the process by users, makes it expire after
	// ttl, writes the heartbeat (also when there are no users, so that an idle
	// process stays listed) and forgets the processes whose heartbeat is older
	// than twice ttl, in one atomic step.
	refreshProcess(ctx context.Context, process string, users []uint64, ttl time.Duration) error
	// dropProcess deletes the set of the process and its heartbeat.
	dropProcess(ctx context.Context, process string) error
	// onlineUsers returns the users, among userIDs, who are in the set of a process
	// whose heartbeat is not older than ttl.
	onlineUsers(ctx context.Context, userIDs []uint64, ttl time.Duration) (map[uint64]bool, error)
	// close closes the backend.
	close() error
}

// Presence says who is online across all the processes that share one Redis
// (SPEC §7.15). It implements live.Presence. Each process keeps in Redis the
// set of its online users, with a time to live, and refreshes it from a
// goroutine every few seconds, so that a process that dies disappears by
// itself after the time to live; a heartbeat in a sorted set, taken from the
// clock of Redis, tells which processes are alive. Make it with NewPresence
// and stop it with Close. It is safe for concurrent use.
//
// Connect and Disconnect never fail and never block longer than a couple of
// seconds: when Redis does not answer, the failure is logged and the next
// refresh repairs the set. Online fails only when Redis cannot be read, and
// then still returns the users known to be online in this process.
type Presence struct {
	process string
	logger  *slog.Logger
	backend presenceBackend
	ttl     time.Duration
	// timeout bounds one call to Redis of Connect, Disconnect and Online.
	timeout time.Duration

	// redisMu is held during every call to Redis that changes the set of this
	// process, and by the refresh from before its snapshot of the users until
	// its call returns, so that a refresh never misses or undoes a concurrent
	// Connect or Disconnect. It is taken before mu.
	redisMu sync.Mutex
	// mu guards counts, the number of open connections of each user here.
	mu     sync.Mutex
	counts map[uint64]int

	refreshEvery time.Duration
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	closeOnce    sync.Once
	closed       atomic.Bool

	changeLog, refreshLog *logGate
}

// presenceOptions are the settings of newPresence that tests change.
type presenceOptions struct {
	// process is the id of this process; empty means a random one.
	process string
	// ttl is the time to live of the data of the process; zero means
	// presenceTTL.
	ttl time.Duration
	// refreshEvery is the time between two refreshes; zero means
	// presenceRefreshEvery.
	refreshEvery time.Duration
	// timeout bounds a call to Redis of Connect, Disconnect and Online; zero
	// means presenceTimeout.
	timeout time.Duration
	// clock times the rate limit of the log lines.
	clock clock.Clock
}

// NewPresence connects to Redis at redisURL (redis:// or rediss://), checks
// the connection with a PING and starts the goroutine that refreshes the
// users of this process. ctx bounds the connecting only; the goroutine runs
// until Close. The error never holds the password of the URL. It fails with
// TagRedisURLUnparseable and TagRedisPingFailed.
func NewPresence(ctx context.Context, redisURL string, logger *slog.Logger) (*Presence, error) {
	c, err := openRedis(ctx, redisURL)
	if err != nil {
		return nil, err
	}
	return newPresence(c, logger, presenceOptions{}), nil
}

// newPresence makes a presence over a backend, which it owns from then on,
// and starts its refresher.
func newPresence(b presenceBackend, logger *slog.Logger, opts presenceOptions) *Presence {
	if opts.process == "" {
		opts.process = rand.Text()
	}
	if opts.ttl <= 0 {
		opts.ttl = presenceTTL
	}
	if opts.refreshEvery <= 0 {
		opts.refreshEvery = presenceRefreshEvery
	}
	if opts.timeout <= 0 {
		opts.timeout = presenceTimeout
	}
	if opts.clock == nil {
		opts.clock = clock.Real()
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Presence{
		process:      opts.process,
		logger:       logger,
		backend:      b,
		ttl:          opts.ttl,
		timeout:      opts.timeout,
		counts:       map[uint64]int{},
		refreshEvery: opts.refreshEvery,
		cancel:       cancel,
		changeLog:    newLogGate(opts.clock, logEvery),
		refreshLog:   newLogGate(opts.clock, logEvery),
	}
	p.wg.Add(1)
	go p.runRefresher(ctx)
	return p
}

// Connect implements live.Presence. The first connection of a user in this
// process puts the user in Redis.
func (p *Presence) Connect(userID uint64) {
	p.mu.Lock()
	if p.counts[userID] > 0 {
		p.counts[userID]++
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()

	// The user may be new here: the count changes with the set in Redis.
	p.redisMu.Lock()
	defer p.redisMu.Unlock()
	p.mu.Lock()
	p.counts[userID]++
	first := p.counts[userID] == 1
	p.mu.Unlock()
	if !first || p.closed.Load() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()
	if err := p.backend.addUser(ctx, p.process, userID, p.ttl); err != nil {
		p.logChange("recording an online user in Redis failed", err)
	}
}

// Disconnect implements live.Presence. The last connection of a user in this
// process takes the user out of Redis.
func (p *Presence) Disconnect(userID uint64) {
	p.mu.Lock()
	switch n := p.counts[userID]; {
	case n == 0:
		p.mu.Unlock()
		return
	case n > 1:
		p.counts[userID] = n - 1
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()

	// The user may be going offline: the count changes with the set in Redis.
	p.redisMu.Lock()
	defer p.redisMu.Unlock()
	p.mu.Lock()
	n := p.counts[userID]
	switch {
	case n > 1:
		p.counts[userID] = n - 1
	case n == 1:
		delete(p.counts, userID)
	}
	p.mu.Unlock()
	if n != 1 || p.closed.Load() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()
	if err := p.backend.removeUser(ctx, p.process, userID); err != nil {
		p.logChange("removing an offline user from Redis failed", err)
	}
}

// Online implements live.Presence. The users with a connection in this
// process are online without asking Redis; the others are looked up in the
// sets of all the live processes. When Redis cannot be read, Online returns
// the users known to be online here together with the error.
func (p *Presence) Online(ctx context.Context, userIDs []uint64) (map[uint64]bool, error) {
	out := make(map[uint64]bool, len(userIDs))
	var rest []uint64
	seen := make(map[uint64]bool, len(userIDs))
	p.mu.Lock()
	for _, id := range userIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if p.counts[id] > 0 {
			out[id] = true
		} else {
			rest = append(rest, id)
		}
	}
	p.mu.Unlock()
	if len(rest) == 0 || p.closed.Load() {
		return out, nil
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	remote, err := p.backend.onlineUsers(ctx, rest, p.ttl)
	if err != nil {
		return out, err
	}
	for id, ok := range remote {
		if ok {
			out[id] = true
		}
	}
	return out, nil
}

// Close stops the refresher and waits for it, deletes the data of this
// process from Redis at once (a process that dies without Close disappears
// after the time to live instead) and closes the connection to Redis. It is
// safe to call more than once and from several goroutines; calls after the
// first return nil. After Close, Connect, Disconnect and Online work on this
// process alone and do not touch Redis.
func (p *Presence) Close() error {
	var err error
	p.closeOnce.Do(func() {
		p.closed.Store(true)
		p.cancel()
		p.wg.Wait()
		// A Connect or a Disconnect in flight ends before the data goes.
		p.redisMu.Lock()
		defer p.redisMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), presenceDropTimeout)
		defer cancel()
		if dropErr := p.backend.dropProcess(ctx, p.process); dropErr != nil {
			p.logger.Warn("removing the presence of this process from Redis failed; it expires by itself", "err", dropErr)
		}
		err = p.backend.close()
	})
	return err
}

// runRefresher writes the users of this process to Redis every refreshEvery
// until ctx is done.
func (p *Presence) runRefresher(ctx context.Context) {
	defer p.wg.Done()
	ticker := time.NewTicker(p.refreshEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.refresh(ctx)
		}
	}
}

// refresh writes the users of this process to Redis, so that they live for
// another ttl and a set lost in the meantime comes back.
func (p *Presence) refresh(ctx context.Context) {
	p.redisMu.Lock()
	defer p.redisMu.Unlock()
	if p.closed.Load() {
		return
	}
	p.mu.Lock()
	users := make([]uint64, 0, len(p.counts))
	for id := range p.counts {
		users = append(users, id)
	}
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	if err := p.backend.refreshProcess(ctx, p.process, users, p.ttl); err != nil && ctx.Err() == nil {
		if ok, skipped := p.refreshLog.allow(); ok {
			p.logger.Warn("refreshing the online users in Redis failed", "err", err, "skipped_logs", skipped)
		}
	}
}

// logChange logs the failure of a Connect or a Disconnect, rate limited.
func (p *Presence) logChange(msg string, err error) {
	if ok, skipped := p.changeLog.allow(); ok {
		p.logger.Warn(msg, "err", err, "skipped_logs", skipped)
	}
}

// Presence is a live.Presence.
var _ live.Presence = (*Presence)(nil)
