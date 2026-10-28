package cluster

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rm4n0s/errors"
)

// fakePresenceStore is an in-memory Redis for presence: the set of each
// process with its expiry, and the sorted set of the heartbeats, on a clock
// that the test moves by hand. Several fakePresenceBackend share one store.
type fakePresenceStore struct {
	mu    sync.Mutex
	nowMs int64
	sets  map[string]*fakePresenceSet
	procs map[string]int64 // process -> heartbeat in ms
}

// fakePresenceSet is the set of online users of one process.
type fakePresenceSet struct {
	users    map[uint64]bool
	expireAt int64 // in ms; zero means it never expires
}

func newFakePresenceStore() *fakePresenceStore {
	return &fakePresenceStore{nowMs: 1_000_000, sets: map[string]*fakePresenceSet{}, procs: map[string]int64{}}
}

// advance moves the clock of the fake Redis.
func (s *fakePresenceStore) advance(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nowMs += d.Milliseconds()
}

// live returns the set of the process, or nil when it is missing or expired.
// The caller holds the lock.
func (s *fakePresenceStore) live(process string) *fakePresenceSet {
	set := s.sets[process]
	if set == nil {
		return nil
	}
	if set.expireAt != 0 && set.expireAt <= s.nowMs {
		delete(s.sets, process)
		return nil
	}
	return set
}

// wipe deletes the set of the process behind its back, as a lost write would.
func (s *fakePresenceStore) wipe(process string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sets, process)
}

// has reports whether the process has a set and a heartbeat.
func (s *fakePresenceStore) has(process string) (set, heartbeat bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, heartbeat = s.procs[process]
	return s.live(process) != nil, heartbeat
}

// backend returns a connection to the store, the backend of one process.
func (s *fakePresenceStore) backend() *fakePresenceBackend {
	return &fakePresenceBackend{store: s}
}

// fakePresenceBackend is a presenceBackend over a fakePresenceStore. It counts
// its calls and can be told to fail or to hang. Each method makes its own
// error, so that the route of a failure ends in the method that fails.
type fakePresenceBackend struct {
	store *fakePresenceStore

	fail, hang atomic.Bool
	closed     atomic.Bool

	adds, removes, refreshes, drops, onlines atomic.Int64
}

func (b *fakePresenceBackend) addUser(ctx context.Context, process string, userID uint64, ttl time.Duration) error {
	b.adds.Add(1)
	if b.hang.Load() {
		<-ctx.Done()
		return errors.New(TagRedisPresenceFailed, "the fake hangs until the context ends", "op", "add")
	}
	if b.fail.Load() {
		return errors.New(TagRedisPresenceFailed, "the fake is set to fail", "op", "add")
	}
	s := b.store
	s.mu.Lock()
	defer s.mu.Unlock()
	set := s.live(process)
	if set == nil {
		set = &fakePresenceSet{users: map[uint64]bool{}}
		s.sets[process] = set
	}
	set.users[userID] = true
	set.expireAt = s.nowMs + ttl.Milliseconds()
	s.procs[process] = s.nowMs
	return nil
}

func (b *fakePresenceBackend) removeUser(ctx context.Context, process string, userID uint64) error {
	b.removes.Add(1)
	if b.hang.Load() {
		<-ctx.Done()
		return errors.New(TagRedisPresenceFailed, "the fake hangs until the context ends", "op", "remove")
	}
	if b.fail.Load() {
		return errors.New(TagRedisPresenceFailed, "the fake is set to fail", "op", "remove")
	}
	s := b.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if set := s.live(process); set != nil {
		delete(set.users, userID)
	}
	return nil
}

func (b *fakePresenceBackend) refreshProcess(ctx context.Context, process string, users []uint64, ttl time.Duration) error {
	b.refreshes.Add(1)
	if b.hang.Load() {
		<-ctx.Done()
		return errors.New(TagRedisPresenceFailed, "the fake hangs until the context ends", "op", "refresh")
	}
	if b.fail.Load() {
		return errors.New(TagRedisPresenceFailed, "the fake is set to fail", "op", "refresh")
	}
	s := b.store
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sets, process)
	if len(users) > 0 {
		set := &fakePresenceSet{users: map[uint64]bool{}, expireAt: s.nowMs + ttl.Milliseconds()}
		for _, id := range users {
			set.users[id] = true
		}
		s.sets[process] = set
	}
	s.procs[process] = s.nowMs
	for p, beat := range s.procs {
		if beat <= s.nowMs-2*ttl.Milliseconds() {
			delete(s.procs, p)
		}
	}
	return nil
}

func (b *fakePresenceBackend) dropProcess(ctx context.Context, process string) error {
	b.drops.Add(1)
	if b.hang.Load() {
		<-ctx.Done()
		return errors.New(TagRedisPresenceFailed, "the fake hangs until the context ends", "op", "drop")
	}
	if b.fail.Load() {
		return errors.New(TagRedisPresenceFailed, "the fake is set to fail", "op", "drop")
	}
	s := b.store
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sets, process)
	delete(s.procs, process)
	return nil
}

func (b *fakePresenceBackend) onlineUsers(ctx context.Context, userIDs []uint64, ttl time.Duration) (map[uint64]bool, error) {
	b.onlines.Add(1)
	if b.hang.Load() {
		<-ctx.Done()
		return nil, errors.New(TagRedisPresenceFailed, "the fake hangs until the context ends", "op", "online")
	}
	if b.fail.Load() {
		return nil, errors.New(TagRedisPresenceFailed, "the fake is set to fail", "op", "online")
	}
	s := b.store
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[uint64]bool{}
	for process, beat := range s.procs {
		// The minimum is exclusive, as in the real client.
		if beat <= s.nowMs-ttl.Milliseconds() {
			continue
		}
		set := s.live(process)
		if set == nil {
			continue
		}
		for _, id := range userIDs {
			if set.users[id] {
				out[id] = true
			}
		}
	}
	return out, nil
}

func (b *fakePresenceBackend) close() error {
	b.closed.Store(true)
	return nil
}
