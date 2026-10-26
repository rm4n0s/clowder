package clock

import (
	"sync"
	"time"
)

// Clock tells the current time. Code that needs the time takes a Clock, so a
// test can control it with Fake.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
}

// realClock is the Clock of the running program.
type realClock struct{}

// Now returns the current time in UTC.
func (realClock) Now() time.Time {
	return time.Now().UTC()
}

// Real returns the Clock of the running program: Now is the system time in UTC.
func Real() Clock {
	return realClock{}
}

// Fake is a Clock that only moves when a test says so. It is safe for
// concurrent use. Make it with NewFake.
type Fake struct {
	mu  sync.Mutex
	now time.Time
}

// NewFake returns a Fake that starts at t, converted to UTC.
func NewFake(t time.Time) *Fake {
	return &Fake{now: t.UTC()}
}

// Now returns the time of the Fake, in UTC.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Set moves the Fake to t, converted to UTC. t may be earlier than the
// current time.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t.UTC()
}

// Advance moves the Fake forward by d. A negative d moves it back.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}
