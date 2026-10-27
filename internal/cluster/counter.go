package cluster

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

const (
	// counterKeyPrefix starts every key of the login counter in Redis; the
	// namespace and the hash of the key follow: clowder:login:user:<hash>.
	counterKeyPrefix = "clowder:login:"
	// counterTimeout bounds one call to Redis made for the limiter, so that a
	// Redis that hangs delays a login by this much at most.
	counterTimeout = 2 * time.Second
)

// counterBackend is the part of the Redis client that the login counter uses.
// redisClient is the implementation over go-redis; tests use an in-memory
// fake. Every error a backend returns is already tagged. The times are in
// milliseconds since the Unix epoch.
type counterBackend interface {
	// count forgets the members of the sorted set key whose score is at most
	// cutoff and returns how many are left, in one atomic step.
	count(ctx context.Context, key string, cutoff int64) (int, error)
	// add, in one atomic step, forgets the members of key whose score is at
	// most cutoff, adds member with the score now, keeps only the latest keep
	// members and makes the key expire after ttl.
	add(ctx context.Context, key, member string, now, cutoff int64, keep int, ttl time.Duration) error
	// clear deletes the key.
	clear(ctx context.Context, key string) error
	// close closes the backend.
	close() error
}

// LoginCounter counts the failed logins in Redis, so that the limit of SPEC
// §7.1 holds across all the processes that share it (SPEC D41, §8.1). It
// implements the interface app.FailureCounter. Each key is a sorted set whose
// scores are the times of the failures; the key expires when the window has
// passed. The times come from the caller, not from Redis, so that a test can
// use a fake clock. A LoginCounter is safe for concurrent use. Make it with
// NewLoginCounter and release it with Close.
type LoginCounter struct {
	backend counterBackend
	// timeout bounds one call to Redis.
	timeout time.Duration
	// text returns a random string; rand.Text, except in a test.
	text func() string
}

// NewLoginCounter connects to Redis at redisURL (redis:// or rediss://) with a
// client of its own and checks the connection with a PING. ctx bounds the
// connecting only. The error never holds the password of the URL. It fails
// with TagRedisURLUnparseable and TagRedisPingFailed.
func NewLoginCounter(ctx context.Context, redisURL string) (*LoginCounter, error) {
	c, err := openRedis(ctx, redisURL)
	if err != nil {
		return nil, err
	}
	return newLoginCounter(c), nil
}

// newLoginCounter makes a counter over a backend.
func newLoginCounter(b counterBackend) *LoginCounter {
	return &LoginCounter{backend: b, timeout: counterTimeout, text: rand.Text}
}

// Close closes the connection to Redis.
func (c *LoginCounter) Close() error {
	return c.backend.close()
}

// Count returns how many failures of key in the namespace ns are newer than
// now minus window. The older ones are forgotten.
func (c *LoginCounter) Count(ctx context.Context, ns, key string, now time.Time, window time.Duration) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return c.backend.count(ctx, counterKey(ns, key), now.Add(-window).UnixMilli())
}

// Add records a failure of key in ns at now. The failures older than window
// are forgotten, and so are all but the latest keep ones, so that a key never
// holds more than keep entries. The key expires after window.
func (c *LoginCounter) Add(ctx context.Context, ns, key string, now time.Time, window time.Duration, keep int) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	// Two failures of the same millisecond must both count, so the member is
	// unique: the time and a random string.
	member := strconv.FormatInt(now.UnixNano(), 10) + "-" + c.text()
	return c.backend.add(ctx, counterKey(ns, key), member, now.UnixMilli(), now.Add(-window).UnixMilli(), keep, window)
}

// Clear forgets every failure of key in ns.
func (c *LoginCounter) Clear(ctx context.Context, ns, key string) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return c.backend.clear(ctx, counterKey(ns, key))
}

// counterKey returns the Redis key of key in ns. The key is hashed: a username
// or an address is user input, and in a Redis key it could carry the
// separator, a glob pattern or any bytes.
func counterKey(ns, key string) string {
	sum := sha256.Sum256([]byte(key))
	return counterKeyPrefix + ns + ":" + hex.EncodeToString(sum[:])
}
