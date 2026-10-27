package live

import (
	"slices"
	"sync"
)

// Recorder is a test double of the hub: it keeps every event published to it,
// in order. It is safe for concurrent use.
type Recorder struct {
	mu     sync.Mutex
	events []Event
}

// Publish records the event.
func (r *Recorder) Publish(ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

// Events returns a copy of the recorded events, oldest first.
func (r *Recorder) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

// Reset forgets the recorded events.
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = nil
}
