package live_test

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/live"
)

// wait is how long a test waits for something that should happen.
const wait = 2 * time.Second

// closedWithin reports whether the channel is closed (after draining) within
// the wait.
func closedWithin(c <-chan live.Event) bool {
	timeout := time.After(wait)
	for {
		select {
		case _, ok := <-c:
			if !ok {
				return true
			}
		case <-timeout:
			return false
		}
	}
}

// next returns the next event, or fails the test after the wait.
func next(t *testing.T, s *live.Subscription) live.Event {
	t.Helper()
	select {
	case ev, ok := <-s.C():
		if !ok {
			t.Fatalf("the subscription is closed")
		}
		return ev
	case <-time.After(wait):
		t.Fatalf("no event within %s", wait)
	}
	return live.Event{}
}

// requireNone fails the test if an event is waiting on the subscription.
func requireNone(t *testing.T, s *live.Subscription) {
	t.Helper()
	select {
	case ev, ok := <-s.C():
		if ok {
			t.Fatalf("unexpected event %+v", ev)
		}
		t.Fatalf("the subscription is closed")
	default:
	}
}

func TestTopics(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"room", live.RoomTopic(7), "room:7"},
		{"post", live.PostTopic(12), "post:12"},
		{"board", live.BoardTopic(3), "board:3"},
		{"conversation", live.ConversationTopic(9), "conversation:9"},
		{"user", live.UserTopic(1), "user:1"},
		{"large", live.RoomTopic(18446744073709551615), "room:18446744073709551615"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestFanOut(t *testing.T) {
	tests := []struct {
		name string
		// subscribe lists the topics of each subscriber.
		subscribe [][]string
		publish   live.Event
		// want says which subscribers get the event.
		want []bool
	}{
		{"every subscriber of the topic", [][]string{{"room:1"}, {"room:1"}, {"room:1"}}, live.Event{Topic: "room:1"}, []bool{true, true, true}},
		{"not the other topics", [][]string{{"room:1"}, {"room:2"}, {"post:1"}}, live.Event{Topic: "room:1"}, []bool{true, false, false}},
		{"several topics of one subscription", [][]string{{"room:1", "post:5"}, {"post:5"}}, live.Event{Topic: "post:5"}, []bool{true, true}},
		{"a repeated topic delivers once", [][]string{{"room:1", "room:1"}}, live.Event{Topic: "room:1"}, []bool{true}},
		{"no subscribers", nil, live.Event{Topic: "room:1"}, nil},
		{"no topics", [][]string{{}}, live.Event{Topic: "room:1"}, []bool{false}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := live.New()
			defer h.Close()
			subs := make([]*live.Subscription, len(tc.subscribe))
			for i, topics := range tc.subscribe {
				subs[i] = h.Subscribe(t.Context(), topics...)
			}
			ev := tc.publish
			ev.Kind, ev.RoomID, ev.PostID, ev.RootID, ev.UserID = live.KindPostCreated, 1, 2, 3, 4
			h.Publish(ev)
			for i, s := range subs {
				if tc.want[i] {
					if got := next(t, s); got != ev {
						t.Errorf("subscriber %d got %+v, want %+v", i, got, ev)
					}
				}
				requireNone(t, s)
			}
		})
	}
}

func TestOrderIsKept(t *testing.T) {
	h := live.New()
	defer h.Close()
	s := h.Subscribe(t.Context(), "room:1", "post:1")
	for i := uint64(1); i <= 10; i++ {
		topic := "room:1"
		if i%2 == 0 {
			topic = "post:1"
		}
		h.Publish(live.Event{Topic: topic, PostID: i})
	}
	for i := uint64(1); i <= 10; i++ {
		if got := next(t, s); got.PostID != i {
			t.Fatalf("event %d has PostID %d", i, got.PostID)
		}
	}
}

func TestSlowSubscriberIsDropped(t *testing.T) {
	h := live.New()
	defer h.Close()
	slow := h.Subscribe(t.Context(), "room:1")
	fast := h.Subscribe(t.Context(), "room:1")
	other := h.Subscribe(t.Context(), "room:2")

	const total = 200
	got := make(chan int, 1)
	go func() {
		n := 0
		for range fast.C() {
			n++
			if n == total {
				break
			}
		}
		got <- n
	}()
	for i := range total {
		h.Publish(live.Event{Topic: "room:1", PostID: uint64(i + 1)})
		// Give the fast reader time to keep up; the slow one never reads.
		for len(fast.C()) > 16 {
			runtime.Gosched()
		}
	}
	if n := <-got; n != total {
		t.Errorf("the fast subscriber got %d events, want %d", n, total)
	}
	if !slow.Dropped() {
		t.Errorf("the slow subscriber is not Dropped")
	}
	if fast.Dropped() || other.Dropped() {
		t.Errorf("a subscriber that keeps up was dropped")
	}
	// The slow subscription holds what fitted, then its channel is closed.
	n := 0
	for range slow.C() {
		n++
	}
	if n != 64 {
		t.Errorf("the slow subscriber kept %d events, want its buffer of 64", n)
	}
	// It stays closed and gets nothing more.
	h.Publish(live.Event{Topic: "room:1"})
	if _, ok := <-slow.C(); ok {
		t.Errorf("the dropped subscription received an event")
	}
}

func TestExactlyBufferIsNotDropped(t *testing.T) {
	h := live.New()
	defer h.Close()
	s := h.Subscribe(t.Context(), "room:1")
	for range 64 {
		h.Publish(live.Event{Topic: "room:1"})
	}
	if s.Dropped() {
		t.Fatalf("dropped with a full buffer; it must drop on the next event")
	}
	h.Publish(live.Event{Topic: "room:1"})
	if !s.Dropped() {
		t.Errorf("not dropped by the event after a full buffer")
	}
}

func TestContextCancelRemoves(t *testing.T) {
	h := live.New()
	defer h.Close()
	ctx, cancel := context.WithCancel(t.Context())
	s := h.Subscribe(ctx, "room:1")
	keep := h.Subscribe(t.Context(), "room:1")
	cancel()
	if !closedWithin(s.C()) {
		t.Fatalf("the subscription is not closed after cancel")
	}
	if s.Dropped() {
		t.Errorf("a cancelled subscription is Dropped")
	}
	h.Publish(live.Event{Topic: "room:1"})
	next(t, keep)
}

func TestSubscribeWithDoneContext(t *testing.T) {
	h := live.New()
	defer h.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s := h.Subscribe(ctx, "room:1")
	if !closedWithin(s.C()) {
		t.Errorf("a subscription with a done context is not closed")
	}
	if s.Dropped() {
		t.Errorf("Dropped is true")
	}
}

func TestSubscriptionClose(t *testing.T) {
	h := live.New()
	defer h.Close()
	s := h.Subscribe(t.Context(), "room:1", "post:1")
	keep := h.Subscribe(t.Context(), "room:1")
	s.Close()
	s.Close() // twice is fine
	if !closedWithin(s.C()) {
		t.Fatalf("the channel is not closed")
	}
	if s.Dropped() {
		t.Errorf("Dropped is true after Close")
	}
	h.Publish(live.Event{Topic: "room:1"})
	h.Publish(live.Event{Topic: "post:1"})
	next(t, keep)
	requireNone(t, keep)
}

func TestHubClose(t *testing.T) {
	h := live.New()
	a := h.Subscribe(t.Context(), "room:1")
	b := h.Subscribe(t.Context(), "room:2", "post:1")
	h.Close()
	h.Close() // twice is fine
	for name, s := range map[string]*live.Subscription{"a": a, "b": b} {
		if !closedWithin(s.C()) {
			t.Errorf("subscription %s is not closed by Hub.Close", name)
		}
		if s.Dropped() {
			t.Errorf("subscription %s is Dropped after Hub.Close", name)
		}
	}
	late := h.Subscribe(t.Context(), "room:1")
	if !closedWithin(late.C()) {
		t.Errorf("a subscription made after Close is not closed")
	}
	h.Publish(live.Event{Topic: "room:1"}) // does nothing, does not panic
	late.Close()                           // does not panic
}

func TestPublishNeverBlocks(t *testing.T) {
	h := live.New()
	defer h.Close()
	for range 10 {
		h.Subscribe(t.Context(), "room:1") // nobody reads
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 10_000 {
			h.Publish(live.Event{Topic: "room:1"})
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("Publish blocked")
	}
}

func TestConcurrentUse(t *testing.T) {
	h := live.New()
	const workers = 8
	var wg sync.WaitGroup
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	for w := range workers {
		topic := fmt.Sprintf("room:%d", w%3)
		wg.Add(3)
		go func() { // publisher
			defer wg.Done()
			for i := range 2000 {
				h.Publish(live.Event{Topic: topic, PostID: uint64(i)})
			}
		}()
		go func() { // subscribers that read, cancel by context or by Close
			defer wg.Done()
			for i := range 200 {
				subCtx, cancel := context.WithCancel(ctx)
				s := h.Subscribe(subCtx, topic, "post:1")
				for range i % 5 {
					select {
					case <-s.C():
					default:
					}
				}
				if i%2 == 0 {
					cancel()
				} else {
					s.Close()
					cancel()
				}
			}
		}()
		go func() { // subscribers that never read, so they get dropped
			defer wg.Done()
			for range 20 {
				s := h.Subscribe(ctx, topic)
				_ = s.Dropped()
			}
		}()
	}
	wg.Wait()
	h.Close()
}

func TestNoGoroutineLeak(t *testing.T) {
	before := runtime.NumGoroutine()
	for range 50 {
		h := live.New()
		ctx, cancel := context.WithCancel(t.Context())
		subs := []*live.Subscription{
			h.Subscribe(ctx, "room:1"),
			h.Subscribe(t.Context(), "room:1", "post:2"),
			h.Subscribe(context.Background(), "room:1"),
		}
		for range 100 {
			h.Publish(live.Event{Topic: "room:1"}) // drops all three
		}
		cancel()
		subs[0].Close()
		h.Close()
	}
	// The functions of context.AfterFunc end on their own; give them time.
	deadline := time.Now().Add(wait)
	for runtime.NumGoroutine() > before+2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Errorf("goroutines: %d before, %d after", before, after)
	}
}

func TestRecorder(t *testing.T) {
	var r live.Recorder
	if got := r.Events(); len(got) != 0 {
		t.Fatalf("a new recorder holds %v", got)
	}
	r.Publish(live.Event{Topic: "room:1", Kind: live.KindPostCreated})
	r.Publish(live.Event{Topic: "room:2", Kind: live.KindRoomDeleted})
	got := r.Events()
	if len(got) != 2 || got[0].Topic != "room:1" || got[1].Kind != live.KindRoomDeleted {
		t.Fatalf("Events = %+v", got)
	}
	got[0].Topic = "changed"
	if r.Events()[0].Topic != "room:1" {
		t.Errorf("Events returns the internal slice")
	}
	r.Reset()
	if got := r.Events(); len(got) != 0 {
		t.Errorf("after Reset: %v", got)
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				r.Publish(live.Event{Topic: "room:1"})
				_ = r.Events()
			}
		})
	}
	wg.Wait()
	if n := len(r.Events()); n != 800 {
		t.Errorf("recorded %d events, want 800", n)
	}
}

// Both the hub and the recorder are what the services publish to.
var (
	_ interface{ Publish(live.Event) } = (*live.Hub)(nil)
	_ interface{ Publish(live.Event) } = (*live.Recorder)(nil)
)
