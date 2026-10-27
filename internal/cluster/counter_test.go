package cluster

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rm4n0s/errors"
)

// fakeSortedSets is an in-memory counterBackend with the semantics of the
// Redis commands the real one sends: a sorted set per key.
type fakeSortedSets struct {
	mu     sync.Mutex
	sets   map[string]map[string]int64 // key -> member -> score
	ttl    map[string]time.Duration
	err    error
	closed bool
	// deadline is the deadline of the context of the last call, zero when it had none.
	deadline time.Time
}

func newFakeSortedSets() *fakeSortedSets {
	return &fakeSortedSets{sets: map[string]map[string]int64{}, ttl: map[string]time.Duration{}}
}

func (f *fakeSortedSets) trim(key string, cutoff int64) {
	for member, score := range f.sets[key] {
		if score <= cutoff {
			delete(f.sets[key], member)
		}
	}
}

func (f *fakeSortedSets) count(ctx context.Context, key string, cutoff int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deadline, _ = ctx.Deadline()
	if f.err != nil {
		return 0, f.err
	}
	f.trim(key, cutoff)
	return len(f.sets[key]), nil
}

func (f *fakeSortedSets) add(ctx context.Context, key, member string, now, cutoff int64, keep int, ttl time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deadline, _ = ctx.Deadline()
	if f.err != nil {
		return f.err
	}
	f.trim(key, cutoff)
	if f.sets[key] == nil {
		f.sets[key] = map[string]int64{}
	}
	f.sets[key][member] = now
	for len(f.sets[key]) > keep {
		oldest, oldestScore := "", int64(1<<62)
		for m, s := range f.sets[key] {
			if s < oldestScore || (s == oldestScore && m < oldest) {
				oldest, oldestScore = m, s
			}
		}
		delete(f.sets[key], oldest)
	}
	f.ttl[key] = ttl
	return nil
}

func (f *fakeSortedSets) clear(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	delete(f.sets, key)
	return nil
}

func (f *fakeSortedSets) close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

var counterStart = time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)

const (
	testWindow = 15 * time.Minute
	testKeep   = 5
)

func TestLoginCounterOnFake(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(t *testing.T, c *LoginCounter, f *fakeSortedSets)
	}{
		{"counts what it added", func(t *testing.T, c *LoginCounter, _ *fakeSortedSets) {
			add(t, c, "user", "ana", counterStart, 3)
			if got := count(t, c, "user", "ana", counterStart); got != 3 {
				t.Errorf("count = %d, want 3", got)
			}
		}},
		{"two failures of the same instant both count", func(t *testing.T, c *LoginCounter, _ *fakeSortedSets) {
			add(t, c, "user", "ana", counterStart, 2)
			if got := count(t, c, "user", "ana", counterStart); got != 2 {
				t.Errorf("count = %d, want 2", got)
			}
		}},
		{"the namespaces and the keys are apart", func(t *testing.T, c *LoginCounter, _ *fakeSortedSets) {
			add(t, c, "user", "ana", counterStart, 2)
			add(t, c, "ip", "ana", counterStart, 1)
			if got := count(t, c, "user", "ana", counterStart); got != 2 {
				t.Errorf("user count = %d, want 2", got)
			}
			if got := count(t, c, "ip", "ana", counterStart); got != 1 {
				t.Errorf("ip count = %d, want 1", got)
			}
			if got := count(t, c, "user", "bob", counterStart); got != 0 {
				t.Errorf("another key counts %d", got)
			}
		}},
		{"failures leave the window", func(t *testing.T, c *LoginCounter, _ *fakeSortedSets) {
			add(t, c, "user", "ana", counterStart, 3)
			if got := count(t, c, "user", "ana", counterStart.Add(testWindow-time.Second)); got != 3 {
				t.Errorf("one second before the window ends: count = %d, want 3", got)
			}
			if got := count(t, c, "user", "ana", counterStart.Add(testWindow)); got != 0 {
				t.Errorf("when the window has passed: count = %d, want 0", got)
			}
		}},
		{"only the latest keep failures are kept", func(t *testing.T, c *LoginCounter, _ *fakeSortedSets) {
			for i := range 9 {
				add(t, c, "user", "ana", counterStart.Add(time.Duration(i)*time.Second), 1)
			}
			if got := count(t, c, "user", "ana", counterStart.Add(9*time.Second)); got != testKeep {
				t.Errorf("count = %d, want %d", got, testKeep)
			}
		}},
		{"the key expires with the window", func(t *testing.T, c *LoginCounter, f *fakeSortedSets) {
			add(t, c, "user", "ana", counterStart, 1)
			if got := f.ttl[counterKey("user", "ana")]; got != testWindow {
				t.Errorf("ttl = %v, want %v", got, testWindow)
			}
		}},
		{"clear forgets the key", func(t *testing.T, c *LoginCounter, _ *fakeSortedSets) {
			add(t, c, "user", "ana", counterStart, 3)
			if err := c.Clear(t.Context(), "user", "ana"); err != nil {
				t.Fatal(err)
			}
			if got := count(t, c, "user", "ana", counterStart); got != 0 {
				t.Errorf("count = %d after Clear", got)
			}
		}},
		{"a call is bounded by a timeout", func(t *testing.T, c *LoginCounter, f *fakeSortedSets) {
			count(t, c, "user", "ana", counterStart)
			if f.deadline.IsZero() {
				t.Error("the call to Redis has no deadline")
			}
		}},
		{"close closes the backend", func(t *testing.T, c *LoginCounter, f *fakeSortedSets) {
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			if !f.closed {
				t.Error("the backend is not closed")
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeSortedSets()
			tt.run(t, newLoginCounter(f), f)
		})
	}
}

// add records n failures at now.
func add(t *testing.T, c *LoginCounter, ns, key string, now time.Time, n int) {
	t.Helper()
	for range n {
		if err := c.Add(t.Context(), ns, key, now, testWindow, testKeep); err != nil {
			t.Fatalf("Add: %+v", err)
		}
	}
}

// count counts the failures at now.
func count(t *testing.T, c *LoginCounter, ns, key string, now time.Time) int {
	t.Helper()
	n, err := c.Count(t.Context(), ns, key, now, testWindow)
	if err != nil {
		t.Fatalf("Count: %+v", err)
	}
	return n
}

func TestCounterKeyIsSafe(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"ana", "a:b", "*", "ana\r\nDEL x", strings.Repeat("x", 100000), ""} {
		got := counterKey("user", key)
		if !strings.HasPrefix(got, "clowder:login:user:") || len(got) != len("clowder:login:user:")+64 {
			t.Errorf("counterKey(%q) = %q, want the prefix and 64 hexadecimal digits", key[:min(len(key), 20)], got)
		}
		if strings.ContainsAny(got[len("clowder:login:user:"):], "*?[]:\r\n ") {
			t.Errorf("the hash part of %q holds a pattern or separator character", got)
		}
	}
	if counterKey("user", "ana") == counterKey("ip", "ana") {
		t.Error("the namespaces share a key")
	}
	if counterKey("user", "ana") == counterKey("user", "bob") {
		t.Error("two keys share a Redis key")
	}
}

func TestLoginCounterPassesErrorsUp(t *testing.T) {
	t.Parallel()
	f := newFakeSortedSets()
	f.err = errors.New("FakeDown", "down")
	c := newLoginCounter(f)
	_, err := c.Count(t.Context(), "user", "ana", counterStart, testWindow)
	if err != f.err {
		t.Errorf("Count returned %v, want the error of the backend untouched", err)
	}
	if err := c.Add(t.Context(), "user", "ana", counterStart, testWindow, testKeep); err != f.err {
		t.Errorf("Add returned %v, want the error of the backend untouched", err)
	}
	if err := c.Clear(t.Context(), "user", "ana"); err != f.err {
		t.Errorf("Clear returned %v, want the error of the backend untouched", err)
	}
}

// openRealCounter opens the counter on the Redis of the tests; the test closes it.
func openRealCounter(t *testing.T, url string) *LoginCounter {
	t.Helper()
	c, err := NewLoginCounter(t.Context(), url)
	if err != nil {
		t.Fatalf("NewLoginCounter: %+v", err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return c
}

func TestLoginCounterOnRedis(t *testing.T) {
	t.Parallel()
	url := testRedisURL(t)
	// The server is shared: every test works on keys of its own.
	key := func(t *testing.T) string { return fmt.Sprintf("%s-%d", t.Name(), time.Now().UnixNano()) }
	tests := []struct {
		name string
		run  func(t *testing.T, c *LoginCounter, key string)
	}{
		{"counts, with two failures of the same instant", func(t *testing.T, c *LoginCounter, key string) {
			add(t, c, "user", key, counterStart, 3)
			if got := count(t, c, "user", key, counterStart); got != 3 {
				t.Errorf("count = %d, want 3", got)
			}
			if got := count(t, c, "ip", key, counterStart); got != 0 {
				t.Errorf("the other namespace counts %d", got)
			}
		}},
		{"failures leave the window", func(t *testing.T, c *LoginCounter, key string) {
			add(t, c, "user", key, counterStart, 3)
			if got := count(t, c, "user", key, counterStart.Add(testWindow-time.Second)); got != 3 {
				t.Errorf("one second before the window ends: count = %d, want 3", got)
			}
			if got := count(t, c, "user", key, counterStart.Add(testWindow)); got != 0 {
				t.Errorf("when the window has passed: count = %d, want 0", got)
			}
		}},
		{"only the latest keep failures are kept", func(t *testing.T, c *LoginCounter, key string) {
			for i := range 9 {
				add(t, c, "user", key, counterStart.Add(time.Duration(i)*time.Second), 1)
			}
			if got := count(t, c, "user", key, counterStart.Add(9*time.Second)); got != testKeep {
				t.Errorf("count = %d, want %d", got, testKeep)
			}
		}},
		{"the key expires with the window", func(t *testing.T, c *LoginCounter, key string) {
			add(t, c, "user", key, counterStart, 1)
			ttl, err := c.backend.(*redisClient).rc.PTTL(t.Context(), counterKey("user", key)).Result()
			if err != nil {
				t.Fatal(err)
			}
			if ttl <= 0 || ttl > testWindow {
				t.Errorf("PTTL = %v, want within (0, %v]", ttl, testWindow)
			}
		}},
		{"clear forgets the key and only that key", func(t *testing.T, c *LoginCounter, key string) {
			add(t, c, "user", key, counterStart, 3)
			add(t, c, "user", key+"-other", counterStart, 2)
			if err := c.Clear(t.Context(), "user", key); err != nil {
				t.Fatal(err)
			}
			if got := count(t, c, "user", key, counterStart); got != 0 {
				t.Errorf("count = %d after Clear", got)
			}
			if got := count(t, c, "user", key+"-other", counterStart); got != 2 {
				t.Errorf("another key counts %d after Clear, want 2", got)
			}
		}},
		{"a key that looks like a pattern is one key", func(t *testing.T, c *LoginCounter, key string) {
			add(t, c, "user", key+"\r\nDEL *", counterStart, 1)
			if got := count(t, c, "user", key+"\r\nDEL *", counterStart); got != 1 {
				t.Errorf("count = %d, want 1", got)
			}
			if got := count(t, c, "user", "*", counterStart); got != 0 {
				t.Errorf("the key * counts %d", got)
			}
		}},
		{"concurrent failures from several counters all count", func(t *testing.T, c *LoginCounter, key string) {
			other := openRealCounter(t, url)
			var wg sync.WaitGroup
			for i := range 40 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					counter := c
					if i%2 == 1 {
						counter = other
					}
					if err := counter.Add(t.Context(), "user", key, counterStart, testWindow, 100); err != nil {
						t.Errorf("Add: %+v", err)
					}
				}()
			}
			wg.Wait()
			if got := count(t, c, "user", key, counterStart); got != 40 {
				t.Errorf("count = %d, want 40", got)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.run(t, openRealCounter(t, url), key(t))
		})
	}
}

func TestNewLoginCounterFailures(t *testing.T) {
	t.Parallel()
	const password = "hunter2-secret"
	tests := []struct {
		name      string
		url       func(t *testing.T) string
		wantRoute string
	}{
		{"nothing listens", func(t *testing.T) string { return deadRedisURL(t, password) }, "NewLoginCounter->openRedis.RedisPingFailed"},
		{"the URL does not parse", func(*testing.T) string { return "redis://:" + password + "@localhost:notaport/0" }, "NewLoginCounter->openRedis.RedisURLUnparseable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, err := NewLoginCounter(t.Context(), tt.url(t))
			if c != nil {
				t.Fatal("a counter came back with the error")
			}
			requireRoute(t, err, tt.wantRoute)
			if appErr, _ := errors.FromError(err); strings.Contains(fmt.Sprintf("%+v %v", appErr, appErr.Metadata), password) {
				t.Errorf("the error holds the password: %v", appErr.Metadata)
			}
		})
	}
}

func TestRedisCounterFailureRoutes(t *testing.T) {
	t.Parallel()
	c := deadClient(t)
	defer func() { _ = c.close() }()
	counter := newLoginCounter(c)
	counter.timeout = time.Second
	_, err := counter.Count(t.Context(), "user", "ana", counterStart, testWindow)
	requireRoute(t, err, "LoginCounter.Count->redisClient.count.RedisCountFailed")
	requireRoute(t, counter.Add(t.Context(), "user", "ana", counterStart, testWindow, testKeep),
		"LoginCounter.Add->redisClient.add.RedisAddFailed")
	requireRoute(t, counter.Clear(t.Context(), "user", "ana"), "LoginCounter.Clear->redisClient.clear.RedisClearFailed")
}

// compile-time check that the real client is a counter backend.
var _ counterBackend = (*redisClient)(nil)
