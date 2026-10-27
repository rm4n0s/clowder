package cluster

import (
	"context"
	"sync"

	"github.com/rm4n0s/errors"
)

// fakeBroker is an in-memory Redis: a publish reaches every subscription of
// its channel, the publisher's own included, as Redis does.
type fakeBroker struct {
	mu   sync.Mutex
	subs []*fakeSubscription
}

// deliver hands the payload to every open subscription.
func (b *fakeBroker) deliver(payload []byte) {
	b.mu.Lock()
	subs := append([]*fakeSubscription(nil), b.subs...)
	b.mu.Unlock()
	for _, s := range subs {
		s.push(incoming{payload: payload})
	}
}

// fakeClient is one process's connection to a fakeBroker.
type fakeClient struct {
	broker *fakeBroker

	mu            sync.Mutex
	closed        bool
	published     [][]byte
	publishErr    error
	publishGate   chan struct{} // when set, publish waits for it to close
	subscribeErr  error
	subscriptions []*fakeSubscription
}

// newFakeClient connects a client to the broker.
func newFakeClient(b *fakeBroker) *fakeClient {
	return &fakeClient{broker: b}
}

// publish records the payload and delivers it, unless it is set to fail or
// to wait.
func (c *fakeClient) publish(ctx context.Context, _ string, payload []byte) error {
	c.mu.Lock()
	gate, failure := c.publishGate, c.publishErr
	c.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return errors.New("FakePublishCancelled", "publish cancelled", "err", ctx.Err().Error())
		}
	}
	if failure != nil {
		return failure
	}
	c.mu.Lock()
	c.published = append(c.published, payload)
	c.mu.Unlock()
	c.broker.deliver(payload)
	return nil
}

// subscribe makes a subscription on the broker.
func (c *fakeClient) subscribe(context.Context, string) (subscription, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.subscribeErr != nil {
		return nil, c.subscribeErr
	}
	s := &fakeSubscription{ch: make(chan incoming, 4096), done: make(chan struct{})}
	c.subscriptions = append(c.subscriptions, s)
	c.broker.mu.Lock()
	c.broker.subs = append(c.broker.subs, s)
	c.broker.mu.Unlock()
	return s, nil
}

// close marks the client closed.
func (c *fakeClient) close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

// isClosed reports whether close was called.
func (c *fakeClient) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// publishedCount is the number of payloads published through this client.
func (c *fakeClient) publishedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.published)
}

// mine returns the subscription this client made (the first one).
func (c *fakeClient) mine() *fakeSubscription {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.subscriptions[0]
}

// fakeSubscription is the stream of one process.
type fakeSubscription struct {
	ch   chan incoming
	done chan struct{}

	mu       sync.Mutex
	closed   bool
	receives int
	failNext []error // errors the next receives return, oldest first
}

// push queues something for the receiver.
func (s *fakeSubscription) push(in incoming) {
	select {
	case s.ch <- in:
	default:
	}
}

// reconnect makes the receiver see the signal of a reconnect.
func (s *fakeSubscription) reconnect() {
	s.push(incoming{resubscribed: true})
}

// inject makes the receiver see a raw message.
func (s *fakeSubscription) inject(payload string) {
	s.push(incoming{payload: []byte(payload)})
}

// failReceives makes the next receives return the errors.
func (s *fakeSubscription) failReceives(errs ...error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext = append(s.failNext, errs...)
}

// receive waits for the next thing, or for close.
func (s *fakeSubscription) receive(ctx context.Context) (incoming, error) {
	s.mu.Lock()
	s.receives++
	if len(s.failNext) > 0 {
		err := s.failNext[0]
		s.failNext = s.failNext[1:]
		s.mu.Unlock()
		return incoming{}, err
	}
	s.mu.Unlock()
	select {
	case in := <-s.ch:
		return in, nil
	case <-s.done:
		return incoming{}, errors.New("FakeSubscriptionClosed", "subscription closed")
	case <-ctx.Done():
		return incoming{}, errors.New("FakeReceiveCancelled", "receive cancelled")
	}
}

// close ends the subscription.
func (s *fakeSubscription) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.done)
	}
	return nil
}

// isClosed reports whether close was called.
func (s *fakeSubscription) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}
