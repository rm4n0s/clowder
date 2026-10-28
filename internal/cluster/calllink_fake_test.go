package cluster

import (
	"context"
	"sync"
	"time"

	"github.com/rm4n0s/errors"
)

// linkBroker is an in-memory Redis for the calls: a publish reaches the
// subscriptions of its channel only, and a key lives for its time to live, on
// the real clock (the tests use short ones).
type linkBroker struct {
	mu   sync.Mutex
	subs map[string][]*fakeSubscription
	keys map[string]linkKey
}

// linkKey is a key of the fake with its value and its expiry.
type linkKey struct {
	value   string
	expires time.Time
}

func newLinkBroker() *linkBroker {
	return &linkBroker{subs: map[string][]*fakeSubscription{}, keys: map[string]linkKey{}}
}

// value returns the value of the key, and whether it is there and not expired.
func (b *linkBroker) value(key string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	k, ok := b.keys[key]
	if !ok || !k.expires.After(time.Now()) {
		return "", false
	}
	return k.value, true
}

// fakeLinkBackend is one process's connection to a linkBroker.
type fakeLinkBackend struct {
	broker *linkBroker

	mu           sync.Mutex
	closed       bool
	sets         int
	deletes      int
	publishErr   error
	subscribeErr error
	setErr       error
	existsErr    error
	deleteErr    error
	closeErr     error
	subscribed   []string
	subs         []*fakeSubscription
}

func newFakeLinkBackend(b *linkBroker) *fakeLinkBackend { return &fakeLinkBackend{broker: b} }

func (f *fakeLinkBackend) publishCount(_ context.Context, channel string, payload []byte) (int64, error) {
	f.mu.Lock()
	failure := f.publishErr
	f.mu.Unlock()
	if failure != nil {
		return 0, failure
	}
	f.broker.mu.Lock()
	subs := append([]*fakeSubscription(nil), f.broker.subs[channel]...)
	f.broker.mu.Unlock()
	for _, s := range subs {
		s.push(incoming{payload: payload})
	}
	return int64(len(subs)), nil
}

func (f *fakeLinkBackend) subscribe(_ context.Context, channel string) (subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subscribeErr != nil {
		return nil, f.subscribeErr
	}
	s := &fakeSubscription{ch: make(chan incoming, 4096), done: make(chan struct{})}
	f.subscribed = append(f.subscribed, channel)
	f.subs = append(f.subs, s)
	f.broker.mu.Lock()
	f.broker.subs[channel] = append(f.broker.subs[channel], s)
	f.broker.mu.Unlock()
	return s, nil
}

func (f *fakeLinkBackend) setKey(_ context.Context, key, value string, ttl time.Duration) error {
	f.mu.Lock()
	failure := f.setErr
	f.sets++
	f.mu.Unlock()
	if failure != nil {
		return failure
	}
	f.broker.mu.Lock()
	f.broker.keys[key] = linkKey{value: value, expires: time.Now().Add(ttl)}
	f.broker.mu.Unlock()
	return nil
}

func (f *fakeLinkBackend) keyExists(_ context.Context, key string) (bool, error) {
	f.mu.Lock()
	failure := f.existsErr
	f.mu.Unlock()
	if failure != nil {
		return false, failure
	}
	_, ok := f.broker.value(key)
	return ok, nil
}

func (f *fakeLinkBackend) deleteKey(_ context.Context, key string) error {
	f.mu.Lock()
	failure := f.deleteErr
	f.deletes++
	f.mu.Unlock()
	if failure != nil {
		return failure
	}
	f.broker.mu.Lock()
	delete(f.broker.keys, key)
	f.broker.mu.Unlock()
	return nil
}

func (f *fakeLinkBackend) close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return f.closeErr
}

func (f *fakeLinkBackend) setCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sets
}

func (f *fakeLinkBackend) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

var errFakeRedis = errors.New("FakeRedisDown", "the fake Redis is down")
