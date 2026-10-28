package live

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/rm4n0s/clowder/internal/metrics"
)

// subscriberBuffer is the number of events a subscription holds. When it is
// full the hub closes the subscription instead of waiting for it.
const subscriberBuffer = 64

// The reasons a subscription is dropped (see Subscription.DropReason).
const (
	// DropOverflow is the reason of a subscriber whose buffer was full.
	DropOverflow = "overflow"
	// DropReconnect is the reason of a subscription closed by DropAll: the
	// bridge to Redis came back after a break and events may have been
	// missed.
	DropReconnect = "reconnect"
)

// Hub delivers events to the subscribers of their topic. Make it with New. It
// is safe for concurrent use. It starts no goroutine: Publish delivers under
// a lock with non-blocking sends.
type Hub struct {
	mu     sync.Mutex
	closed bool
	// subs maps a topic to the subscriptions that listen to it.
	subs map[string]map[*Subscription]struct{}
	// droppedOverflow and droppedReconnect count the subscriptions the hub
	// closed for each reason (SPEC D60); nil counts nothing.
	droppedOverflow, droppedReconnect *metrics.Counter
}

// Option changes how New makes a hub.
type Option func(*Hub)

// WithDropCounter makes the hub count the subscriptions it closes, by reason
// (DropOverflow, DropReconnect), in the counter vec, which has one label, the
// reason (SPEC D60). A nil vec counts nothing.
func WithDropCounter(dropped *metrics.CounterVec) Option {
	return func(h *Hub) {
		h.droppedOverflow = dropped.With(DropOverflow)
		h.droppedReconnect = dropped.With(DropReconnect)
	}
}

// New makes an empty hub.
func New(opts ...Option) *Hub {
	h := &Hub{subs: map[string]map[*Subscription]struct{}{}}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Subscription is one listener of the hub. Read events from C until it is
// closed. The channel is closed when the context ends, when Close is called,
// when the hub is closed or when the subscriber is too slow (see Dropped).
type Subscription struct {
	hub *Hub
	ch  chan Event
	// reason is why the hub dropped the subscription ("" until it did).
	reason atomic.Value
	// topics, closed and stop are guarded by hub.mu.
	topics map[string]struct{}
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
			s.reason.Store(DropOverflow)
			h.removeLocked(s)
			h.droppedOverflow.Inc()
		}
	}
}

// Subscribe listens to the topics. Cancelling ctx closes the subscription and
// removes it from the hub. On a closed hub, or with a ctx that is done, the
// subscription comes back already closed.
func (h *Hub) Subscribe(ctx context.Context, topics ...string) *Subscription {
	s := &Subscription{hub: h, ch: make(chan Event, subscriberBuffer), topics: make(map[string]struct{}, len(topics))}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || ctx.Err() != nil {
		s.closed = true
		close(s.ch)
		return s
	}
	for _, t := range topics {
		h.addLocked(s, t)
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
	for s := range h.allLocked() {
		h.removeLocked(s)
	}
}

// DropAll closes every current subscription as if it were too slow: Dropped
// reports true on each and DropReason says DropReconnect, so their subscribers
// reconnect and reload what they show. The hub itself stays open: later
// Subscribe and Publish calls work as before. The bridge between processes calls it when its connection to
// Redis came back after a break, because events may have been missed (SPEC
// D41). On a closed hub it does nothing.
func (h *Hub) DropAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.allLocked() {
		s.reason.Store(DropReconnect)
		h.removeLocked(s)
		h.droppedReconnect.Inc()
	}
}

// addLocked makes s listen to the topic. The caller holds h.mu.
func (h *Hub) addLocked(s *Subscription, topic string) {
	s.topics[topic] = struct{}{}
	if h.subs[topic] == nil {
		h.subs[topic] = map[*Subscription]struct{}{}
	}
	h.subs[topic][s] = struct{}{}
}

// allLocked returns the set of every current subscription. The caller holds
// h.mu.
func (h *Hub) allLocked() map[*Subscription]struct{} {
	all := map[*Subscription]struct{}{}
	for _, set := range h.subs {
		for s := range set {
			all[s] = struct{}{}
		}
	}
	return all
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
	for t := range s.topics {
		h.forgetLocked(s, t)
	}
}

// forgetLocked stops s listening to the topic. The caller holds h.mu.
func (h *Hub) forgetLocked(s *Subscription, topic string) {
	delete(s.topics, topic)
	delete(h.subs[topic], s)
	if len(h.subs[topic]) == 0 {
		delete(h.subs, topic)
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

// AddTopics makes the subscription listen to more topics, with no gap: an event
// published after the call returns is delivered, and nothing is delivered
// twice. A topic it already listens to is left as it is. On a closed
// subscription it does nothing.
func (s *Subscription) AddTopics(topics ...string) {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	if s.closed {
		return
	}
	for _, t := range topics {
		s.hub.addLocked(s, t)
	}
}

// RemoveTopics makes the subscription stop listening to the topics. Events of
// them that are already in the channel are still delivered. A topic it does
// not listen to is ignored. On a closed subscription it does nothing.
func (s *Subscription) RemoveTopics(topics ...string) {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	if s.closed {
		return
	}
	for _, t := range topics {
		s.hub.forgetLocked(s, t)
	}
}

// Dropped reports whether the hub closed the subscription because its buffer
// was full, or because of DropAll. The subscriber should reconnect and reload what it shows.
func (s *Subscription) Dropped() bool {
	return s.DropReason() != ""
}

// DropReason says why the hub closed the subscription: DropOverflow when its
// buffer was full, DropReconnect after DropAll. It is empty for a subscription
// that is open, or that ended by Close, by its context or by the hub's Close.
func (s *Subscription) DropReason() string {
	reason, _ := s.reason.Load().(string)
	return reason
}
