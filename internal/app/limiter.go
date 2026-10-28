package app

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/rm4n0s/clowder/internal/clock"
)

// The limits of SPEC §7.1: this many failed logins for one username, or from
// one client address, within the window block further attempts for it.
const (
	loginMaxFailures = 5
	loginWindow      = 15 * time.Minute
	// limiterSweepInterval is how often the whole map of the in-memory counter
	// is cleaned of keys whose failures have all left the window.
	limiterSweepInterval = time.Minute
	// limiterKeyMaxLength cuts the keys, so that a huge username does not
	// fill the memory.
	limiterKeyMaxLength = 128
)

// LoginWindow is the window of the login limiter: failures older than this
// are forgotten. The API sends it as the Retry-After of a refused login, an
// upper bound of the wait, because the limiter does not know the remaining
// time.
const LoginWindow = loginWindow

// The namespaces of the keys that LoginLimiter gives a FailureCounter: the
// failures of a username and the failures of a client address are counted
// apart.
const (
	// CounterUser is the namespace of the failures per username.
	CounterUser = "user"
	// CounterIP is the namespace of the failures per client address.
	CounterIP = "ip"
)

// FailureCounter keeps the times of the failed logins per key, for
// LoginLimiter. The in-memory counter is the default; with several processes
// the counter of internal/cluster keeps them in Redis, so that a limit holds
// across processes (SPEC D41, §8.1). Every method may fail, and then the
// limiter fails open (see LoginLimiter). An implementation is safe for
// concurrent use.
type FailureCounter interface {
	// Count returns how many failures of key in the namespace ns are newer
	// than now minus window.
	Count(ctx context.Context, ns, key string, now time.Time, window time.Duration) (int, error)
	// Add records a failure of key in ns at now. The failures older than
	// window are forgotten, and so are all but the latest keep ones.
	Add(ctx context.Context, ns, key string, now time.Time, window time.Duration, keep int) error
	// Clear forgets every failure of key in ns.
	Clear(ctx context.Context, ns, key string) error
}

// LoginLimiter counts the failed logins per username and per client address
// in a sliding window and says when a further attempt must wait (SPEC §7.1).
// The counting is done by a FailureCounter: in memory by default, so that a
// restart forgets the failures, or in Redis when several processes share a
// limit. When the counter fails, the limiter fails open: it logs a warning,
// allows the attempt and counts nothing, because an outage of Redis must not
// lock everybody out (SPEC §8.1); the password is still checked. It is safe
// for concurrent use. Make it with NewLoginLimiter or NewLoginLimiterOn.
type LoginLimiter struct {
	clk         clock.Clock
	counter     FailureCounter
	logger      *slog.Logger
	maxFailures int
	window      time.Duration
}

// NewLoginLimiter makes a limiter that blocks after 5 failures within 15
// minutes, counted in memory on clk. A nil clk is the real clock.
func NewLoginLimiter(clk clock.Clock) *LoginLimiter {
	return NewLoginLimiterOn(clk, nil, nil)
}

// NewLoginLimiterOn is NewLoginLimiter with the counter that keeps the
// failures and the logger for the failures of the counter. A nil counter is
// the in-memory one, a nil clk the real clock and a nil logger discards the
// log.
func NewLoginLimiterOn(clk clock.Clock, counter FailureCounter, logger *slog.Logger) *LoginLimiter {
	if clk == nil {
		clk = clock.Real()
	}
	if counter == nil {
		counter = newMemoryCounter(clk.Now())
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &LoginLimiter{
		clk:         clk,
		counter:     counter,
		logger:      logger,
		maxFailures: loginMaxFailures,
		window:      loginWindow,
	}
}

// Allow reports whether a login attempt for username from the address ip may
// go on. It is false when either already failed the maximum number of times
// within the window. An empty ip is not counted. When the counter fails it is
// true.
func (l *LoginLimiter) Allow(ctx context.Context, username, ip string) bool {
	now := l.clk.Now()
	if l.recent(ctx, CounterUser, limiterKey(username), now) >= l.maxFailures {
		return false
	}
	return ip == "" || l.recent(ctx, CounterIP, limiterKey(ip), now) < l.maxFailures
}

// Fail records a failed login attempt for username from the address ip.
func (l *LoginLimiter) Fail(ctx context.Context, username, ip string) {
	now := l.clk.Now()
	l.add(ctx, CounterUser, limiterKey(username), now)
	if ip != "" {
		l.add(ctx, CounterIP, limiterKey(ip), now)
	}
}

// AllowAddr reports whether a client address may go on, for the attempts that
// have no username to count, such as an SSH connection that offers keys. It is
// false when the address already failed the maximum number of times within the
// window (the failures of logins from the same address count too). An empty
// address is allowed. When the counter fails it is true.
func (l *LoginLimiter) AllowAddr(ctx context.Context, ip string) bool {
	return ip == "" || l.recent(ctx, CounterIP, limiterKey(ip), l.clk.Now()) < l.maxFailures
}

// FailAddr records a failed attempt of a client address that has no username
// to count. An empty address is not counted.
func (l *LoginLimiter) FailAddr(ctx context.Context, ip string) {
	if ip != "" {
		l.add(ctx, CounterIP, limiterKey(ip), l.clk.Now())
	}
}

// Reset forgets the failures of username, after a successful login. The
// failures of the client address stay: one right password does not make the
// address trustworthy.
func (l *LoginLimiter) Reset(ctx context.Context, username string) {
	if err := l.counter.Clear(ctx, CounterUser, limiterKey(username)); err != nil {
		l.logger.Warn("clearing the failed logins failed; the login goes on", "err", err)
	}
}

// recent returns the number of failures of key in the window, 0 when the
// counter fails.
func (l *LoginLimiter) recent(ctx context.Context, ns, key string, now time.Time) int {
	n, err := l.counter.Count(ctx, ns, key, now, l.window)
	if err != nil {
		l.logger.Warn("counting the failed logins failed; the login goes on", "err", err)
		return 0
	}
	return n
}

// add records a failure of key; a failure of the counter is logged.
func (l *LoginLimiter) add(ctx context.Context, ns, key string, now time.Time) {
	if err := l.counter.Add(ctx, ns, key, now, l.window, l.maxFailures); err != nil {
		l.logger.Warn("recording a failed login failed; the login goes on", "err", err)
	}
}

// memoryCounter is the FailureCounter in memory. It never fails.
type memoryCounter struct {
	mu        sync.Mutex
	byNS      map[string]map[string][]time.Time // the times of the latest failures, oldest first
	lastSweep time.Time
}

// newMemoryCounter makes an empty counter whose first sweep is due one
// interval after now.
func newMemoryCounter(now time.Time) *memoryCounter {
	return &memoryCounter{
		byNS:      map[string]map[string][]time.Time{CounterUser: {}, CounterIP: {}},
		lastSweep: now,
	}
}

// mapOf returns the map of the namespace, made when it is new.
func (c *memoryCounter) mapOf(ns string) map[string][]time.Time {
	m, ok := c.byNS[ns]
	if !ok {
		m = map[string][]time.Time{}
		c.byNS[ns] = m
	}
	return m
}

// Count implements FailureCounter. It drops the older failures of the key,
// and the key when none is left.
func (c *memoryCounter) Count(_ context.Context, ns, key string, now time.Time, window time.Duration) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepLocked(now, window)
	m := c.mapOf(ns)
	times, ok := m[key]
	if !ok {
		return 0, nil
	}
	times = pruneTimes(times, now, window)
	if len(times) == 0 {
		delete(m, key)
		return 0, nil
	}
	m[key] = times
	return len(times), nil
}

// Add implements FailureCounter.
func (c *memoryCounter) Add(_ context.Context, ns, key string, now time.Time, window time.Duration, keep int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepLocked(now, window)
	m := c.mapOf(ns)
	times := append(pruneTimes(m[key], now, window), now)
	if len(times) > keep {
		times = times[len(times)-keep:]
	}
	m[key] = times
	return nil
}

// Clear implements FailureCounter.
func (c *memoryCounter) Clear(_ context.Context, ns, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.mapOf(ns), key)
	return nil
}

// pruneTimes drops the times that are outside the window.
func pruneTimes(times []time.Time, now time.Time, window time.Duration) []time.Time {
	cutoff := now.Add(-window)
	i := 0
	for i < len(times) && !times[i].After(cutoff) {
		i++
	}
	return times[i:]
}

// sweepLocked removes the keys with no failure left in the window. It runs at
// most once per limiterSweepInterval, so the memory holds only the keys that
// failed within the window plus the last interval.
func (c *memoryCounter) sweepLocked(now time.Time, window time.Duration) {
	if now.Sub(c.lastSweep) < limiterSweepInterval {
		return
	}
	c.lastSweep = now
	for _, m := range c.byNS {
		for key, times := range m {
			if len(pruneTimes(times, now, window)) == 0 {
				delete(m, key)
			}
		}
	}
}

// limiterKey cuts a key to limiterKeyMaxLength bytes.
func limiterKey(key string) string {
	if len(key) > limiterKeyMaxLength {
		return key[:limiterKeyMaxLength]
	}
	return key
}
