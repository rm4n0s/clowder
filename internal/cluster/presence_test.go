package cluster

import (
	"fmt"
	"log/slog"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/live/presencetest"
	"github.com/rm4n0s/errors"
)

// abandon stops the refresher and closes the connection WITHOUT removing the
// data of the process from Redis, as a process that died would leave it. It
// is for tests only: it lives in a _test file so that no other code can call
// it.
func (p *Presence) abandon() {
	p.closeOnce.Do(func() {
		p.closed.Store(true)
		p.cancel()
		p.wg.Wait()
		_ = p.backend.close()
	})
}

// presenceProcessIDs numbers the processes of the tests.
var presenceProcessIDs atomic.Int64

// newFakeProcess starts a process over the store with a fake backend. The
// test closes it. The refresh interval is short, because the refresher runs on
// the real clock while the store has a clock of its own.
func newFakeProcess(t *testing.T, store *fakePresenceStore, logs *syncBuffer, opts presenceOptions) (*Presence, *fakePresenceBackend) {
	t.Helper()
	if opts.process == "" {
		opts.process = fmt.Sprintf("p%d", presenceProcessIDs.Add(1))
	}
	if opts.refreshEvery == 0 {
		opts.refreshEvery = 5 * time.Millisecond
	}
	var logger *slog.Logger
	if logs != nil {
		logger = slog.New(slog.NewJSONHandler(logs, nil))
	} else {
		logger = slog.New(slog.DiscardHandler)
	}
	b := store.backend()
	p := newPresence(b, logger, opts)
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return p, b
}

// presenceEventually fails the test unless cond holds within the wait.
func presenceEventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen within %s", what, wait)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// onlineOf returns the sorted users, among ids, who p says are online.
func onlineOf(t *testing.T, p *Presence, ids ...uint64) []uint64 {
	t.Helper()
	got, err := p.Online(t.Context(), ids)
	if err != nil {
		t.Fatalf("Online(%v): %+v", ids, err)
	}
	var out []uint64
	for id, ok := range got {
		if ok {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

func requireOnline(t *testing.T, p *Presence, want []uint64, ids ...uint64) {
	t.Helper()
	if got := onlineOf(t, p, ids...); !slices.Equal(got, want) {
		t.Errorf("Online(%v) = %v, want %v", ids, got, want)
	}
}

func TestPresenceOverFakeSharedTable(t *testing.T) {
	t.Parallel()
	presencetest.Run(t, true, func(t *testing.T) func() live.Presence {
		store := newFakePresenceStore()
		return func() live.Presence {
			p, _ := newFakeProcess(t, store, nil, presenceOptions{ttl: time.Minute})
			return p
		}
	})
}

func TestPresenceOverRedisSharedTable(t *testing.T) {
	url := testRedisURL(t)
	presencetest.Run(t, true, func(t *testing.T) func() live.Presence {
		return func() live.Presence {
			p, err := NewPresence(t.Context(), url, slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatalf("NewPresence: %v", err)
			}
			t.Cleanup(func() {
				if err := p.Close(); err != nil {
					t.Errorf("Close: %v", err)
				}
			})
			return p
		}
	})
}

func TestPresenceDeadProcessExpires(t *testing.T) {
	t.Parallel()
	const ttl = time.Minute
	store := newFakePresenceStore()
	a, _ := newFakeProcess(t, store, nil, presenceOptions{ttl: ttl, process: "a"})
	b, _ := newFakeProcess(t, store, nil, presenceOptions{ttl: ttl, process: "b"})
	a.Connect(5)
	requireOnline(t, b, []uint64{5}, 5, 6)
	a.abandon()

	// Within the ttl the data of the dead process is still believed.
	store.advance(ttl - time.Second)
	requireOnline(t, b, []uint64{5}, 5)
	// Past it, the heartbeat is too old and the set has expired.
	store.advance(2 * time.Second)
	requireOnline(t, b, nil, 5)

	// The refresh of a live process forgets heartbeats older than twice the ttl.
	store.advance(ttl)
	presenceEventually(t, "the heartbeat of the dead process to be forgotten", func() bool {
		_, heartbeat := store.has("a")
		return !heartbeat
	})
}

func TestPresenceRefresherKeepsUsersOnline(t *testing.T) {
	t.Parallel()
	const ttl = time.Minute
	store := newFakePresenceStore()
	a, ab := newFakeProcess(t, store, nil, presenceOptions{ttl: ttl})
	b, _ := newFakeProcess(t, store, nil, presenceOptions{ttl: ttl})
	a.Connect(5)
	// Three times the ttl passes, in steps; each step waits for a refresh.
	for range 9 {
		before := ab.refreshes.Load()
		store.advance(ttl / 3)
		presenceEventually(t, "a refresh", func() bool { return ab.refreshes.Load() > before })
		requireOnline(t, b, []uint64{5}, 5)
	}
}

func TestPresenceRefreshRestoresALostSet(t *testing.T) {
	t.Parallel()
	store := newFakePresenceStore()
	// A refresh every 50 ms, slow enough to see the set missing.
	a, _ := newFakeProcess(t, store, nil, presenceOptions{process: "a", refreshEvery: 50 * time.Millisecond})
	b, _ := newFakeProcess(t, store, nil, presenceOptions{refreshEvery: time.Hour})
	a.Connect(5)
	a.Connect(6)
	store.wipe("a")
	requireOnline(t, b, nil, 5, 6)
	presenceEventually(t, "the users to come back", func() bool {
		return slices.Equal(onlineOf(t, b, 5, 6), []uint64{5, 6})
	})
}

func TestPresenceIdleProcessStaysListed(t *testing.T) {
	t.Parallel()
	store := newFakePresenceStore()
	_, ab := newFakeProcess(t, store, nil, presenceOptions{process: "a"})
	presenceEventually(t, "a refresh", func() bool { return ab.refreshes.Load() > 0 })
	if _, heartbeat := store.has("a"); !heartbeat {
		t.Error("an idle process has no heartbeat")
	}
}

func TestPresenceClose(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		stop     func(p *Presence) error
		wantGone bool
	}{
		{"Close removes the data at once", func(p *Presence) error { return p.Close() }, true},
		{"abandon leaves the data for the ttl", func(p *Presence) error { p.abandon(); return nil }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := newFakePresenceStore()
			a, ab := newFakeProcess(t, store, nil, presenceOptions{process: "a"})
			b, _ := newFakeProcess(t, store, nil, presenceOptions{})
			a.Connect(5)
			requireOnline(t, b, []uint64{5}, 5)

			if err := tt.stop(a); err != nil {
				t.Fatalf("stop: %v", err)
			}
			if set, heartbeat := store.has("a"); (set || heartbeat) == tt.wantGone {
				t.Errorf("set = %v, heartbeat = %v after stopping, want gone = %v", set, heartbeat, tt.wantGone)
			}
			if got, want := onlineOf(t, b, 5), map[bool][]uint64{true: nil, false: {5}}[tt.wantGone]; !slices.Equal(got, want) {
				t.Errorf("the other process sees %v, want %v", got, want)
			}
			if gotDrops := ab.drops.Load(); (gotDrops == 1) != tt.wantGone {
				t.Errorf("drop called %d times", gotDrops)
			}
			if !ab.closed.Load() {
				t.Error("the backend is not closed")
			}

			// The refresher is stopped and waited for: no call reaches Redis
			// any more, however long the intervals pass.
			calls := ab.refreshes.Load()
			time.Sleep(40 * time.Millisecond) // eight refresh intervals
			if got := ab.refreshes.Load(); got != calls {
				t.Errorf("refresh called %d times after stopping", got-calls)
			}
			// Again is fine.
			if err := a.Close(); err != nil {
				t.Errorf("second Close: %v", err)
			}
		})
	}
}

func TestPresenceAfterCloseWorksLocally(t *testing.T) {
	t.Parallel()
	store := newFakePresenceStore()
	a, ab := newFakeProcess(t, store, nil, presenceOptions{})
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	adds, onlines := ab.adds.Load(), ab.onlines.Load()
	a.Connect(5)
	requireOnline(t, a, []uint64{5}, 5, 6)
	a.Disconnect(5)
	requireOnline(t, a, nil, 5, 6)
	if ab.adds.Load() != adds || ab.removes.Load() != 0 || ab.onlines.Load() != onlines {
		t.Error("Redis was touched after Close")
	}
}

func TestPresenceConnectionsAreCountedWithoutRedisCalls(t *testing.T) {
	t.Parallel()
	store := newFakePresenceStore()
	a, ab := newFakeProcess(t, store, nil, presenceOptions{refreshEvery: time.Hour})
	a.Connect(5)
	a.Connect(5)
	a.Connect(5)
	a.Disconnect(5)
	a.Disconnect(5)
	if ab.adds.Load() != 1 || ab.removes.Load() != 0 {
		t.Errorf("adds = %d, removes = %d, want 1 and 0 while a connection is open", ab.adds.Load(), ab.removes.Load())
	}
	a.Disconnect(5)
	a.Disconnect(5) // without a connect
	if ab.adds.Load() != 1 || ab.removes.Load() != 1 {
		t.Errorf("adds = %d, removes = %d, want 1 and 1", ab.adds.Load(), ab.removes.Load())
	}
	// Users of this process are answered without asking Redis.
	a.Connect(6)
	before := ab.onlines.Load()
	requireOnline(t, a, []uint64{6}, 6, 6)
	if ab.onlines.Load() != before {
		t.Error("Redis was asked about a user of this process")
	}
}

func TestPresenceRedisFailure(t *testing.T) {
	t.Parallel()
	store := newFakePresenceStore()
	logs := &syncBuffer{}
	a, ab := newFakeProcess(t, store, logs, presenceOptions{refreshEvery: 5 * time.Millisecond})
	ab.fail.Store(true)

	// Connect and Disconnect do not fail; the failure is logged.
	a.Connect(5)
	a.Connect(6)
	a.Disconnect(6)
	if entries := logs.entries(t, "Redis failed"); len(entries) != 1 {
		t.Errorf("%d log lines about the change, want 1 (rate limited): %s", len(entries), logs.String())
	} else if e := entries[0]; e.Err == nil || e.Err.Tag != TagRedisPresenceFailed {
		t.Errorf("logged error = %+v", e.Err)
	}
	presenceEventually(t, "a failing refresh to be logged", func() bool { return len(logs.entries(t, "refreshing")) > 0 })

	// Online keeps the local answer and returns the error.
	got, err := a.Online(t.Context(), []uint64{5, 6, 7})
	if want := map[uint64]bool{5: true}; !slices.Equal(keys(got), keys(want)) {
		t.Errorf("Online = %v, want %v", got, want)
	}
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T: %v", err, err)
	}
	if !appErr.HasRoute("Presence.Online->fakePresenceBackend.onlineUsers.RedisPresenceFailed") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}
	if appErr.Metadata["op"] != "online" {
		t.Errorf("metadata = %v", appErr.Metadata)
	}

	// Redis comes back: the next refresh puts the user there.
	ab.fail.Store(false)
	b, _ := newFakeProcess(t, store, nil, presenceOptions{refreshEvery: time.Hour})
	presenceEventually(t, "the refresh to repair the set", func() bool { return slices.Equal(onlineOf(t, b, 5, 6), []uint64{5}) })
}

func keys(m map[uint64]bool) []uint64 {
	var out []uint64
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestPresenceHangingRedisDoesNotBlock(t *testing.T) {
	t.Parallel()
	store := newFakePresenceStore()
	a, ab := newFakeProcess(t, store, nil, presenceOptions{timeout: 50 * time.Millisecond, refreshEvery: time.Hour})
	ab.hang.Store(true)
	start := time.Now()
	a.Connect(5)
	a.Disconnect(5)
	a.Connect(5)
	_, err := a.Online(t.Context(), []uint64{1})
	if elapsed := time.Since(start); elapsed > wait {
		t.Errorf("took %s", elapsed)
	}
	if _, ok := errors.FromError(err); !ok {
		t.Errorf("Online error = %v, want a tagged error", err)
	}
	requireOnline(t, a, []uint64{5}, 5)
	ab.hang.Store(false)
}

func TestNewPresenceFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		url       func(t *testing.T) string
		wantRoute string
	}{
		{"nothing listens", func(t *testing.T) string { return deadRedisURL(t, "secret") }, "NewPresence->openRedis.RedisPingFailed"},
		{"the URL does not parse", func(*testing.T) string { return "redis://:secret@localhost:notaport/0" }, "NewPresence->openRedis.RedisURLUnparseable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p, err := NewPresence(t.Context(), tt.url(t), slog.New(slog.DiscardHandler))
			if p != nil {
				t.Fatal("a presence came back with the error")
			}
			requireRoute(t, err, tt.wantRoute)
		})
	}
}

func TestRedisClientPresenceFailureRoutes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		call  func(t *testing.T, c *redisClient) error
		route string
	}{
		{"addUser", func(t *testing.T, c *redisClient) error { return c.addUser(t.Context(), "p", 1, time.Minute) }, "redisClient.addUser.RedisPresenceFailed"},
		{"removeUser", func(t *testing.T, c *redisClient) error { return c.removeUser(t.Context(), "p", 1) }, "redisClient.removeUser.RedisPresenceFailed"},
		{"refreshProcess", func(t *testing.T, c *redisClient) error {
			return c.refreshProcess(t.Context(), "p", []uint64{1}, time.Minute)
		}, "redisClient.refreshProcess.RedisPresenceFailed"},
		{"dropProcess", func(t *testing.T, c *redisClient) error { return c.dropProcess(t.Context(), "p") }, "redisClient.dropProcess.RedisPresenceFailed"},
		{"onlineUsers", func(t *testing.T, c *redisClient) error {
			_, err := c.onlineUsers(t.Context(), []uint64{1}, time.Minute)
			return err
		}, "redisClient.onlineUsers.RedisPresenceFailed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := deadClient(t)
			defer func() { _ = c.close() }()
			err := tt.call(t, c)
			requireRoute(t, err, tt.route)
			if appErr, _ := errors.FromError(err); appErr.Metadata["op"] == nil {
				t.Errorf("no op in the metadata: %v", appErr.Metadata)
			}
		})
	}
}

func TestPresenceOverADeadRedis(t *testing.T) {
	t.Parallel()
	logs := &syncBuffer{}
	p := newPresence(deadClient(t), slog.New(slog.NewJSONHandler(logs, nil)), presenceOptions{refreshEvery: time.Hour})
	defer func() { _ = p.Close() }()
	p.Connect(5)
	got, err := p.Online(t.Context(), []uint64{5, 6})
	if !got[5] || got[6] {
		t.Errorf("Online = %v, want only user 5", got)
	}
	requireRoute(t, err, "Presence.Online->redisClient.onlineUsers.RedisPresenceFailed")
	if len(logs.entries(t, "Redis failed")) != 1 {
		t.Errorf("log: %s", logs.String())
	}
}

// newRealProcess starts a process over a real Redis with the options. The
// test closes it. The user ids of these tests start at a million so that they
// do not meet the users of the shared table.
func newRealProcess(t *testing.T, url string, opts presenceOptions) *Presence {
	t.Helper()
	c, err := openRedis(t.Context(), url)
	if err != nil {
		t.Fatalf("openRedis: %v", err)
	}
	if opts.process == "" {
		opts.process = fmt.Sprintf("test-%d-%d", time.Now().UnixNano(), presenceProcessIDs.Add(1))
	}
	p := newPresence(c, slog.New(slog.DiscardHandler), opts)
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
		// An abandoned process left its data; remove it.
		admin := openTestAdmin(t, url)
		admin.Del(t.Context(), presenceKey(opts.process))
		admin.ZRem(t.Context(), presenceProcessesKey, opts.process)
	})
	return p
}

// openTestAdmin returns a go-redis client for the test, closed with it.
func openTestAdmin(t *testing.T, url string) *redis.Client {
	t.Helper()
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	c := redis.NewClient(opt)
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("closing the admin client: %v", err)
		}
	})
	return c
}

func TestRedisPresenceDeadProcessExpires(t *testing.T) {
	url := testRedisURL(t)
	opts := presenceOptions{ttl: 400 * time.Millisecond, refreshEvery: 100 * time.Millisecond}
	a, b := newRealProcess(t, url, opts), newRealProcess(t, url, opts)
	const user = 1_000_001
	a.Connect(user)
	requireOnline(t, b, []uint64{user}, user)
	a.abandon()
	presenceEventually(t, "the user of the dead process to go offline", func() bool {
		return len(onlineOf(t, b, user)) == 0
	})
}

func TestRedisPresenceRefresherKeepsUsersOnline(t *testing.T) {
	url := testRedisURL(t)
	opts := presenceOptions{ttl: 600 * time.Millisecond, refreshEvery: 100 * time.Millisecond}
	a, b := newRealProcess(t, url, opts), newRealProcess(t, url, opts)
	const user = 1_000_002
	a.Connect(user)
	// Well past two ttl, the user is online all the time.
	for end := time.Now().Add(1300 * time.Millisecond); time.Now().Before(end); time.Sleep(25 * time.Millisecond) {
		requireOnline(t, b, []uint64{user}, user)
	}
}

func TestRedisPresenceRefreshRestoresALostSet(t *testing.T) {
	url := testRedisURL(t)
	opts := presenceOptions{ttl: time.Minute, refreshEvery: 100 * time.Millisecond}
	a, b := newRealProcess(t, url, opts), newRealProcess(t, url, opts)
	const user = 1_000_003
	a.Connect(user)
	requireOnline(t, b, []uint64{user}, user)
	admin := openTestAdmin(t, url)
	if err := admin.Del(t.Context(), presenceKey(a.process)).Err(); err != nil {
		t.Fatal(err)
	}
	presenceEventually(t, "the user to come back", func() bool { return len(onlineOf(t, b, user)) == 1 })
}

func TestRedisPresenceClose(t *testing.T) {
	url := testRedisURL(t)
	opts := presenceOptions{ttl: time.Minute, refreshEvery: 50 * time.Millisecond}
	a, b := newRealProcess(t, url, opts), newRealProcess(t, url, opts)
	const user = 1_000_004
	a.Connect(user)
	requireOnline(t, b, []uint64{user}, user)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	// Gone at once, not after the ttl.
	requireOnline(t, b, nil, user)
	if err := a.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	admin := openTestAdmin(t, url)
	if n, err := admin.Exists(t.Context(), presenceKey(a.process)).Result(); err != nil || n != 0 {
		t.Errorf("the set still exists: %d, %v", n, err)
	}
	if _, err := admin.ZScore(t.Context(), presenceProcessesKey, a.process).Result(); err != redis.Nil {
		t.Errorf("the heartbeat still exists: %v", err)
	}
}
