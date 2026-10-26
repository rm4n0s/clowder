package app

import (
	"sync"
	"time"

	"github.com/rm4n0s/clowder/internal/clock"
)

// The limits of SPEC §7.1: this many failed logins for one username, or from
// one client address, within the window block further attempts for it.
const (
	loginMaxFailures = 5
	loginWindow      = 15 * time.Minute
	// limiterSweepInterval is how often the whole map is cleaned of keys whose
	// failures have all left the window.
	limiterSweepInterval = time.Minute
	// limiterKeyMaxLength cuts the keys, so that a huge username does not
	// fill the memory.
	limiterKeyMaxLength = 128
)

// LoginLimiter counts the failed logins per username and per client address
// in a sliding window and says when a further attempt must wait (SPEC §7.1).
// It lives in memory, so a restart forgets the failures. It is safe for
// concurrent use. Make it with NewLoginLimiter.
type LoginLimiter struct {
	clk         clock.Clock
	maxFailures int
	window      time.Duration

	mu        sync.Mutex
	byUser    map[string][]time.Time // the times of the latest failures, oldest first
	byIP      map[string][]time.Time
	lastSweep time.Time
}

// NewLoginLimiter makes a limiter that blocks after 5 failures within 15
// minutes, counted on clk. A nil clk is the real clock.
func NewLoginLimiter(clk clock.Clock) *LoginLimiter {
	if clk == nil {
		clk = clock.Real()
	}
	return &LoginLimiter{
		clk:         clk,
		maxFailures: loginMaxFailures,
		window:      loginWindow,
		byUser:      map[string][]time.Time{},
		byIP:        map[string][]time.Time{},
		lastSweep:   clk.Now(),
	}
}

// Allow reports whether a login attempt for username from the address ip may
// go on. It is false when either already failed the maximum number of times
// within the window. An empty ip is not counted.
func (l *LoginLimiter) Allow(username, ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clk.Now()
	l.sweepLocked(now)
	if l.recentLocked(l.byUser, limiterKey(username), now) >= l.maxFailures {
		return false
	}
	return ip == "" || l.recentLocked(l.byIP, limiterKey(ip), now) < l.maxFailures
}

// Fail records a failed login attempt for username from the address ip.
func (l *LoginLimiter) Fail(username, ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clk.Now()
	l.sweepLocked(now)
	l.addLocked(l.byUser, limiterKey(username), now)
	if ip != "" {
		l.addLocked(l.byIP, limiterKey(ip), now)
	}
}

// Reset forgets the failures of username, after a successful login. The
// failures of the client address stay: one right password does not make the
// address trustworthy.
func (l *LoginLimiter) Reset(username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byUser, limiterKey(username))
}

// recentLocked returns how many failures of key are still inside the window,
// and drops the older ones (and the key, when none is left).
func (l *LoginLimiter) recentLocked(m map[string][]time.Time, key string, now time.Time) int {
	times, ok := m[key]
	if !ok {
		return 0
	}
	times = l.pruneLocked(times, now)
	if len(times) == 0 {
		delete(m, key)
		return 0
	}
	m[key] = times
	return len(times)
}

// addLocked records a failure of key at now, keeping only the latest
// maxFailures times.
func (l *LoginLimiter) addLocked(m map[string][]time.Time, key string, now time.Time) {
	times := append(l.pruneLocked(m[key], now), now)
	if len(times) > l.maxFailures {
		times = times[len(times)-l.maxFailures:]
	}
	m[key] = times
}

// pruneLocked drops the times that are outside the window.
func (l *LoginLimiter) pruneLocked(times []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-l.window)
	i := 0
	for i < len(times) && !times[i].After(cutoff) {
		i++
	}
	return times[i:]
}

// sweepLocked removes the keys with no failure left in the window. It runs at
// most once per limiterSweepInterval, so the memory holds only the keys that
// failed within the window plus the last interval.
func (l *LoginLimiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < limiterSweepInterval {
		return
	}
	l.lastSweep = now
	for _, m := range []map[string][]time.Time{l.byUser, l.byIP} {
		for key, times := range m {
			if len(l.pruneLocked(times, now)) == 0 {
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
