package live

import (
	"context"
	"sync"
	"sync/atomic"
)

// subscriberBuffer is the number of events a subscription holds. When it is
// full the hub closes the subscription instead of waiting for it.
const subscriberBuffer = 64

// Hub delivers events to the subscribers of their topic. Make it with New. It
// is safe for concurrent use. It starts no goroutine: Publish delivers under
// a lock with non-blocking sends.
type Hub struct {
	mu     sync.Mutex
	closed bool
	// subs maps a topic to the subscriptions that listen to it.
	subs map[string]map[*Subscription]struct{}
}

// New makes an empty hub.
func New() *Hub {
	return &Hub{subs: map[string]map[*Subscription]struct{}{}}
}

// Subscription is one listener of the hub. Read events from C until it is
// closed. The channel is closed when the context ends, when Close is called,
// when the hub is closed or when the subscriber is too slow (see Dropped).
type Subscription struct {
	hub     *Hub
	topics  []string
	ch      chan Event
	dropped atomic.Bool
	// closed and stop are guarded by hub.mu.
	closed bool
	stop   func() bool
}

// Publish delivers the event to every subscriber of ev.Topic. It never
// blocks: a subscriber whose buffer is full is closed (Dropped reports true)
// and the event is not delivered to it. After Close it does nothing.
func (h *Hub) Publish(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	for s := range h.subs[ev.Topic] {
		select {
		case s.ch <- ev:
		default:
			s.dropped.Store(true)
			h.removeLocked(s)
		}
	}
}

// Subscribe listens to the topics. Cancelling ctx closes the subscription and
// removes it from the hub. On a closed hub, or with a ctx that is done, the
// subscription comes back already closed.
func (h *Hub) Subscribe(ctx context.Context, topics ...string) *Subscription {
	s := &Subscription{hub: h, ch: make(chan Event, subscriberBuffer)}
	seen := make(map[string]bool, len(topics))
	for _, t := range topics {
		if !seen[t] {
			seen[t] = true
			s.topics = append(s.topics, t)
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || ctx.Err() != nil {
		s.closed = true
		close(s.ch)
		return s
	}
	for _, t := range s.topics {
		if h.subs[t] == nil {
			h.subs[t] = map[*Subscription]struct{}{}
		}
		h.subs[t][s] = struct{}{}
	}
	// The function waits for the lock, so it runs after Subscribe returned
	// even when ctx ends at once. It is a no-op once the subscription is
	// closed.
	s.stop = context.AfterFunc(ctx, s.Close)
	return s
}

// Close closes every subscription and makes later Subscribe calls return
// closed subscriptions and later Publish calls do nothing. It is safe to call
// more than once.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	all := map[*Subscription]struct{}{}
	for _, set := range h.subs {
		for s := range set {
			all[s] = struct{}{}
		}
	}
	for s := range all {
		h.removeLocked(s)
	}
}

// removeLocked closes the subscription and takes it out of the hub. The
// caller holds h.mu.
func (h *Hub) removeLocked(s *Subscription) {
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
	if s.stop != nil {
		s.stop()
	}
	for _, t := range s.topics {
		delete(h.subs[t], s)
		if len(h.subs[t]) == 0 {
			delete(h.subs, t)
		}
	}
}

// C is the channel of events. It is closed when the subscription ends.
func (s *Subscription) C() <-chan Event {
	return s.ch
}

// Close ends the subscription: the channel is closed and the hub forgets it.
// It is safe to call more than once and from several goroutines.
func (s *Subscription) Close() {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	s.hub.removeLocked(s)
}

// Dropped reports whether the hub closed the subscription because its buffer
// was full. The subscriber should reconnect and reload what it shows.
func (s *Subscription) Dropped() bool {
	return s.dropped.Load()
}
