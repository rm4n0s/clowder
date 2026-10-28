package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/errors"
)

// The bridge is a Publisher of the services.
var _ app.Publisher = (*Bridge)(nil)

// wait is how long a test waits for something that should happen.
const wait = 5 * time.Second

// kindFence is the kind of the events that mark the end of what a test sent.
const kindFence = "test.fence"

// syncBuffer is a log destination that the bridge goroutines and the test use
// at the same time.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// logEntry is one line of the JSON log.
type logEntry struct {
	Level   string            `json:"level"`
	Msg     string            `json:"msg"`
	Err     *errors.ErrorJson `json:"err"`
	Skipped uint64            `json:"skipped_logs"`
}

// entries parses the log lines, keeping those whose message contains msg.
func (b *syncBuffer) entries(t *testing.T, msg string) []logEntry {
	t.Helper()
	var out []logEntry
	for _, line := range strings.Split(strings.TrimSpace(b.String()), "\n") {
		if line == "" {
			continue
		}
		var e logEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if strings.Contains(e.Msg, msg) {
			out = append(out, e)
		}
	}
	return out
}

// node is one Clowder process: a hub, a bridge, its connection to the broker
// and its log.
type node struct {
	hub    *live.Hub
	bridge *Bridge
	client *fakeClient
	logs   *syncBuffer
	clock  *clock.Fake
}

// newNode starts a process on the broker. The test closes it at its end.
func newNode(t *testing.T, broker *fakeBroker, opts bridgeOptions) *node {
	t.Helper()
	n := &node{
		hub:    live.New(),
		client: newFakeClient(broker),
		logs:   &syncBuffer{},
		clock:  clock.NewFake(time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)),
	}
	if opts.clock == nil {
		opts.clock = n.clock
	}
	logger := slog.New(slog.NewJSONHandler(n.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	b, err := newBridge(t.Context(), n.client, n.hub, logger, opts)
	if err != nil {
		t.Fatalf("newBridge: %v", err)
	}
	n.bridge = b
	t.Cleanup(func() {
		if err := b.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
		n.hub.Close()
	})
	return n
}

// collect reads events from s until the fence arrives and returns all of them,
// the fence included.
func collect(t *testing.T, s *live.Subscription) []live.Event {
	t.Helper()
	var got []live.Event
	timeout := time.After(wait)
	for {
		select {
		case ev, ok := <-s.C():
			if !ok {
				t.Fatalf("the subscription closed; got %+v", got)
			}
			got = append(got, ev)
			if ev.Kind == kindFence {
				return got
			}
		case <-timeout:
			t.Fatalf("no fence within %s; got %+v", wait, got)
		}
	}
}

// requireClosed fails the test unless the subscription closes within the wait.
func requireClosed(t *testing.T, s *live.Subscription) {
	t.Helper()
	timeout := time.After(wait)
	for {
		select {
		case _, ok := <-s.C():
			if !ok {
				return
			}
		case <-timeout:
			t.Fatalf("the subscription is still open")
		}
	}
}

// eventN makes a distinct event on the topic.
func eventN(topic string, n uint64) live.Event {
	return live.Event{
		Topic: topic, Kind: live.KindPostCreated,
		RoomID: n, PostID: n + 1, RootID: n + 2, UserID: n + 3, ConversationID: n + 4, MessageID: n + 5,
		NotificationID: n + 6, Ring: true, Sound: "chirp", Browser: true,
	}
}

func TestEventsReachTheOtherProcessOnce(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		events []live.Event
	}{
		{"one event with every field", []live.Event{eventN("room:1", 10)}},
		{"several events keep their order", []live.Event{eventN("room:1", 1), eventN("room:1", 2), eventN("room:1", 3)}},
		{"an event with only a topic and a kind", []live.Event{{Topic: "room:1", Kind: live.KindRoomDeleted}}},
		{"large ids", []live.Event{{Topic: "room:1", Kind: live.KindPostCreated, RoomID: 1<<64 - 1, PostID: 1<<63 + 5}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			broker := &fakeBroker{}
			a := newNode(t, broker, bridgeOptions{})
			b := newNode(t, broker, bridgeOptions{})
			subA := a.hub.Subscribe(t.Context(), "room:1")
			subB := b.hub.Subscribe(t.Context(), "room:1")

			for _, ev := range tt.events {
				a.bridge.Publish(ev)
			}
			fence := live.Event{Topic: "room:1", Kind: kindFence}
			a.bridge.Publish(fence)
			want := append(append([]live.Event(nil), tt.events...), fence)

			if got := collect(t, subB); !equalEvents(got, want) {
				t.Errorf("the other process got %+v, want %+v", got, want)
			}
			// The fence of B travels after everything A sent, so when A has it,
			// A's receiver has seen (and must have skipped) the echo of A's events.
			fence2 := live.Event{Topic: "room:1", Kind: kindFence, PostID: 99}
			b.bridge.Publish(fence2)
			want = append(want, fence2)
			var gotA []live.Event
			for len(gotA) < len(want) || gotA[len(gotA)-1] != fence2 {
				gotA = append(gotA, nextEvent(t, subA))
			}
			if !equalEvents(gotA, want) {
				t.Errorf("the publishing process got %+v, want exactly %+v (no echo)", gotA, want)
			}
		})
	}
}

// nextEvent returns the next event of s, or fails the test after the wait.
func nextEvent(t *testing.T, s *live.Subscription) live.Event {
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

func equalEvents(a, b []live.Event) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestOtherTopicsAreNotDelivered(t *testing.T) {
	t.Parallel()
	broker := &fakeBroker{}
	a := newNode(t, broker, bridgeOptions{})
	b := newNode(t, broker, bridgeOptions{})
	sub := b.hub.Subscribe(t.Context(), "room:2")
	a.bridge.Publish(live.Event{Topic: "room:1", Kind: live.KindPostCreated})
	a.bridge.Publish(live.Event{Topic: "room:2", Kind: kindFence})
	got := collect(t, sub)
	if len(got) != 1 || got[0].Topic != "room:2" {
		t.Errorf("got %+v, want only the event of room:2", got)
	}
}

func TestEventOfEveryProcessReachesEveryOther(t *testing.T) {
	t.Parallel()
	broker := &fakeBroker{}
	nodes := []*node{newNode(t, broker, bridgeOptions{}), newNode(t, broker, bridgeOptions{}), newNode(t, broker, bridgeOptions{})}
	subs := make([]*live.Subscription, len(nodes))
	for i, n := range nodes {
		subs[i] = n.hub.Subscribe(t.Context(), "room:1")
	}
	nodes[1].bridge.Publish(live.Event{Topic: "room:1", Kind: kindFence, PostID: 7})
	for i, s := range subs {
		got := collect(t, s)
		if len(got) != 1 || got[0].PostID != 7 {
			t.Errorf("process %d got %+v", i, got)
		}
	}
}

func TestMalformedMessagesAreSkippedAndLogged(t *testing.T) {
	t.Parallel()
	const secret = "s3cr3t-body-text"
	tests := []struct {
		name    string
		payload string
	}{
		{"not JSON", "not json " + secret},
		{"a JSON array", `["` + secret + `"]`},
		{"a number", "42"},
		{"truncated JSON", `{"origin":"x","event":{"topic":"room:1","kind":"` + secret},
		{"no origin", `{"event":{"topic":"room:1","kind":"post.created","note":"` + secret + `"}}`},
		{"no topic", `{"origin":"x","event":{"kind":"post.created"}}`},
		{"no kind", `{"origin":"x","event":{"topic":"room:1"}}`},
		{"wrong type of an id", `{"origin":"x","event":{"topic":"room:1","kind":"k","room_id":"` + secret + `"}}`},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			broker := &fakeBroker{}
			a := newNode(t, broker, bridgeOptions{})
			b := newNode(t, broker, bridgeOptions{})
			sub := b.hub.Subscribe(t.Context(), "room:1")

			b.client.mine().inject(tt.payload)
			a.bridge.Publish(live.Event{Topic: "room:1", Kind: kindFence, PostID: 5})

			got := collect(t, sub)
			if len(got) != 1 || got[0].PostID != 5 {
				t.Errorf("got %+v, want only the valid event", got)
			}
			entries := b.logs.entries(t, "malformed")
			if len(entries) != 1 {
				t.Fatalf("%d log lines about the malformed message, want 1:\n%s", len(entries), b.logs.String())
			}
			e := entries[0]
			if e.Level != "WARN" {
				t.Errorf("level = %s, want WARN", e.Level)
			}
			if e.Err == nil || e.Err.Tag != TagEventMalformed {
				t.Fatalf("logged error = %+v, want tag %s", e.Err, TagEventMalformed)
			}
			if !e.Err.HasRoute("decodeMessage.EventMalformed") {
				t.Errorf("unexpected route %s", e.Err.Route())
			}
			if strings.Contains(b.logs.String(), secret) {
				t.Errorf("the log holds the payload:\n%s", b.logs.String())
			}
		})
	}
}

func TestMalformedMessageLogsAreRateLimited(t *testing.T) {
	t.Parallel()
	broker := &fakeBroker{}
	a := newNode(t, broker, bridgeOptions{})
	b := newNode(t, broker, bridgeOptions{})
	sub := b.hub.Subscribe(t.Context(), "room:1")
	flush := func(id uint64) {
		a.bridge.Publish(live.Event{Topic: "room:1", Kind: kindFence, PostID: id})
		collect(t, sub)
	}
	for range 5 {
		b.client.mine().inject("garbage")
	}
	flush(1)
	if got := len(b.logs.entries(t, "malformed")); got != 1 {
		t.Fatalf("%d lines for 5 malformed messages in one second, want 1", got)
	}
	b.clock.Advance(2 * time.Second)
	b.client.mine().inject("garbage")
	flush(2)
	entries := b.logs.entries(t, "malformed")
	if len(entries) != 2 {
		t.Fatalf("%d lines after the second, want 2", len(entries))
	}
	if entries[1].Skipped != 4 {
		t.Errorf("skipped_logs = %d, want 4", entries[1].Skipped)
	}
}

func TestReconnectDropsTheLocalSubscriptions(t *testing.T) {
	t.Parallel()
	broker := &fakeBroker{}
	a := newNode(t, broker, bridgeOptions{})
	b := newNode(t, broker, bridgeOptions{})
	subA := a.hub.Subscribe(t.Context(), "room:1")
	subB1 := b.hub.Subscribe(t.Context(), "room:1")
	subB2 := b.hub.Subscribe(t.Context(), "user:3", "room:1")

	// Traffic and the first subscription alone drop nothing.
	a.bridge.Publish(live.Event{Topic: "room:1", Kind: kindFence})
	collect(t, subB1)
	collect(t, subB2)
	if subB1.Dropped() || subB2.Dropped() {
		t.Fatalf("a subscription is dropped without a reconnect")
	}

	b.client.mine().reconnect()
	requireClosed(t, subB1)
	requireClosed(t, subB2)
	if !subB1.Dropped() || !subB2.Dropped() {
		t.Errorf("Dropped() = %v, %v after a reconnect, want true, true", subB1.Dropped(), subB2.Dropped())
	}
	if subA.Dropped() {
		t.Errorf("the subscription of the other process is dropped")
	}
	if got := len(b.logs.entries(t, "came back")); got != 1 {
		t.Errorf("%d info lines about the reconnect, want 1", got)
	}

	// New subscriptions work, and the bridge goes on delivering.
	fresh := b.hub.Subscribe(t.Context(), "room:1")
	a.bridge.Publish(live.Event{Topic: "room:1", Kind: kindFence, PostID: 9})
	if got := collect(t, fresh); len(got) != 1 || got[0].PostID != 9 {
		t.Errorf("after the reconnect got %+v", got)
	}
}

func TestReceiveErrorIsLoggedAndRetried(t *testing.T) {
	t.Parallel()
	broker := &fakeBroker{}
	a := newNode(t, broker, bridgeOptions{})
	b := newNode(t, broker, bridgeOptions{})
	sub := b.hub.Subscribe(t.Context(), "room:1")
	b.client.mine().failReceives(
		errors.New("RedisReceiveFailed", "connection reset"),
		errors.New("RedisReceiveFailed", "connection refused"),
	)
	// The receiver is waiting on the subscription; wake it so that it
	// takes the failures.
	b.client.mine().inject("garbage")
	a.bridge.Publish(live.Event{Topic: "room:1", Kind: kindFence, PostID: 3})
	if got := collect(t, sub); len(got) != 1 || got[0].PostID != 3 {
		t.Errorf("got %+v", got)
	}
	entries := b.logs.entries(t, "receiving live events")
	if len(entries) != 1 {
		t.Fatalf("%d lines about the failed receives, want 1 (rate limited):\n%s", len(entries), b.logs.String())
	}
	if entries[0].Level != "WARN" || entries[0].Err == nil || entries[0].Err.Tag != "RedisReceiveFailed" {
		t.Errorf("entry = %+v", entries[0])
	}
}

func TestFullQueueDoesNotBlockPublish(t *testing.T) {
	t.Parallel()
	const total = 40
	broker := &fakeBroker{}
	a := newNode(t, broker, bridgeOptions{queueSize: 4})
	b := newNode(t, broker, bridgeOptions{})
	gate := make(chan struct{})
	a.client.mu.Lock()
	a.client.publishGate = gate
	a.client.mu.Unlock()
	subA := a.hub.Subscribe(t.Context(), "room:1")
	subB := b.hub.Subscribe(t.Context(), "room:1")

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range uint64(total) {
			a.bridge.Publish(live.Event{Topic: "room:1", Kind: live.KindPostCreated, PostID: i})
		}
	}()
	select {
	case <-done:
	case <-time.After(wait):
		t.Fatal("Publish blocks while Redis is slow")
	}
	// The local hub has every event.
	for i := range uint64(total) {
		if got := nextEvent(t, subA); got.PostID != i {
			t.Fatalf("local event %d has PostID %d", i, got.PostID)
		}
	}
	// One event is with the sender and four are queued; the rest was dropped
	// for Redis.
	if lost := a.bridge.Lost(); lost < total-5 || lost > total-4 {
		t.Errorf("Lost() = %d, want %d or %d", lost, total-5, total-4)
	}
	entries := a.logs.entries(t, "queue of live events")
	if len(entries) != 1 {
		t.Errorf("%d lines about the full queue, want 1 (rate limited)", len(entries))
	}
	// When Redis is back the queued events arrive.
	close(gate)
	if err := a.bridge.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	// Every event is either lost or arrives (the one in flight when Close
	// cancelled may be either).
	want := total - int(a.bridge.Lost())
	got := 0
	deadline := time.After(wait)
	for got < want {
		select {
		case <-subB.C():
			got++
		case <-deadline:
			t.Fatalf("the other process got %d events, want %d (Lost() = %d)", got, want, a.bridge.Lost())
		}
	}
	select {
	case ev := <-subB.C():
		t.Errorf("an extra event arrived: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
	if got < 3 {
		t.Errorf("only %d events arrived", got)
	}
}

func TestPublishFailureIsCountedAndLogged(t *testing.T) {
	t.Parallel()
	broker := &fakeBroker{}
	a := newNode(t, broker, bridgeOptions{})
	a.client.mu.Lock()
	a.client.publishErr = errors.New("RedisPublishFailed", "connection refused")
	a.client.mu.Unlock()
	sub := a.hub.Subscribe(t.Context(), "room:1")
	for i := range uint64(3) {
		a.bridge.Publish(live.Event{Topic: "room:1", Kind: live.KindPostCreated, PostID: i})
	}
	for i := range uint64(3) {
		if got := nextEvent(t, sub); got.PostID != i {
			t.Errorf("local event %d has PostID %d", i, got.PostID)
		}
	}
	deadline := time.Now().Add(wait)
	for a.bridge.Lost() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if a.bridge.Lost() != 3 {
		t.Fatalf("Lost() = %d, want 3", a.bridge.Lost())
	}
	entries := a.logs.entries(t, "sending a live event")
	if len(entries) != 1 || entries[0].Err == nil || entries[0].Err.Tag != "RedisPublishFailed" {
		t.Errorf("log entries = %+v", entries)
	}
}

func TestEncodeFailureIsLogged(t *testing.T) {
	t.Parallel()
	broker := &fakeBroker{}
	a := newNode(t, broker, bridgeOptions{marshal: func(any) ([]byte, error) {
		return nil, context.DeadlineExceeded
	}})
	sub := a.hub.Subscribe(t.Context(), "room:1")
	a.bridge.Publish(live.Event{Topic: "room:1", Kind: live.KindPostCreated})
	nextEvent(t, sub)
	deadline := time.Now().Add(wait)
	for len(a.logs.entries(t, "encoding a live event")) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	entries := a.logs.entries(t, "encoding a live event")
	if len(entries) != 1 {
		t.Fatalf("%d log lines, want 1:\n%s", len(entries), a.logs.String())
	}
	e := entries[0]
	if e.Level != "ERROR" || e.Err == nil || e.Err.Tag != TagEventEncodeFailed || !e.Err.HasRoute("Bridge.send.EventEncodeFailed") {
		t.Errorf("entry = %+v", e)
	}
	if a.bridge.Lost() != 1 || a.client.publishedCount() != 0 {
		t.Errorf("Lost() = %d, published = %d", a.bridge.Lost(), a.client.publishedCount())
	}
}

func TestNewBridgeFailures(t *testing.T) {
	t.Parallel()
	t.Run("the subscription fails", func(t *testing.T) {
		t.Parallel()
		c := newFakeClient(&fakeBroker{})
		c.subscribeErr = errors.New("RedisSubscribeFailed", "no server")
		hub := live.New()
		defer hub.Close()
		b, err := newBridge(t.Context(), c, hub, slog.New(slog.DiscardHandler), bridgeOptions{})
		if b != nil {
			t.Errorf("bridge = %v, want nil", b)
		}
		appErr, ok := errors.FromError(err)
		if !ok || appErr.Tag != "RedisSubscribeFailed" {
			t.Errorf("err = %v, want the error of the client untouched", err)
		}
		if !c.isClosed() {
			t.Error("the client was not closed")
		}
	})
	t.Run("no random bytes", func(t *testing.T) {
		t.Parallel()
		c := newFakeClient(&fakeBroker{})
		hub := live.New()
		defer hub.Close()
		_, err := newBridge(t.Context(), c, hub, slog.New(slog.DiscardHandler), bridgeOptions{
			random: func([]byte) (int, error) { return 0, context.Canceled },
		})
		appErr, ok := errors.FromError(err)
		if !ok {
			t.Fatalf("expected *errors.Error, got %T: %v", err, err)
		}
		if !appErr.HasRoute("newBridge.OriginFailed") {
			t.Errorf("unexpected failure path: %s", appErr.Route())
		}
		if !c.isClosed() {
			t.Error("the client was not closed")
		}
	})
}

func TestOriginsDiffer(t *testing.T) {
	t.Parallel()
	broker := &fakeBroker{}
	a := newNode(t, broker, bridgeOptions{})
	b := newNode(t, broker, bridgeOptions{})
	if len(a.bridge.origin) != 32 || a.bridge.origin == b.bridge.origin {
		t.Errorf("origins %q and %q: want 32 hex characters, different", a.bridge.origin, b.bridge.origin)
	}
	a.bridge.Publish(live.Event{Topic: "room:1", Kind: live.KindPostCreated, RoomID: 1})
	deadline := time.Now().Add(wait)
	for a.client.publishedCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	a.client.mu.Lock()
	defer a.client.mu.Unlock()
	var msg map[string]any
	if err := json.Unmarshal(a.client.published[0], &msg); err != nil {
		t.Fatal(err)
	}
	if msg["origin"] != a.bridge.origin {
		t.Errorf("origin on the wire = %v, want %s", msg["origin"], a.bridge.origin)
	}
	ev, ok := msg["event"].(map[string]any)
	if !ok || ev["topic"] != "room:1" || ev["kind"] != live.KindPostCreated || ev["room_id"] != float64(1) {
		t.Errorf("event on the wire = %v", msg["event"])
	}
}

// goroutinesOf counts the goroutines whose stack mentions name.
func goroutinesOf(name string) int {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	return strings.Count(string(buf), name)
}

// eventually reports whether cond becomes true within the wait.
func eventually(cond func() bool) bool {
	deadline := time.Now().Add(wait)
	for !cond() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
	return true
}

// TestCloseStopsTheGoroutines is not parallel, so that no other test has a
// bridge running while it counts goroutines.
func TestCloseStopsTheGoroutines(t *testing.T) {
	broker := &fakeBroker{}
	hub := live.New()
	defer hub.Close()
	c := newFakeClient(broker)
	b, err := newBridge(t.Context(), c, hub, slog.New(slog.DiscardHandler), bridgeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// A goroutine of an earlier test may still be on its way out.
	if !eventually(func() bool {
		return goroutinesOf("(*Bridge).runSender") == 1 && goroutinesOf("(*Bridge).runReceiver") == 1
	}) {
		t.Fatalf("the bridge does not have exactly one sender and one receiver goroutine running")
	}
	sub := hub.Subscribe(t.Context(), "room:1")
	// Events queued just before Close are still sent.
	for i := range uint64(3) {
		b.Publish(live.Event{Topic: "room:1", Kind: live.KindPostCreated, PostID: i})
	}

	closed := make(chan error, 1)
	go func() { closed <- b.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(wait):
		t.Fatal("Close does not return")
	}
	// Close waited for them to be done; give them a moment to leave.
	if !eventually(func() bool { return goroutinesOf("(*Bridge).run") == 0 }) {
		t.Errorf("%d bridge goroutines still run after Close", goroutinesOf("(*Bridge).run"))
	}
	if !c.mine().isClosed() || !c.isClosed() {
		t.Errorf("subscription closed = %v, client closed = %v, want both", c.mine().isClosed(), c.isClosed())
	}
	if got := c.publishedCount(); got != 3 {
		t.Errorf("%d events were sent to Redis before Close returned, want 3", got)
	}
	if err := b.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}

	// After Close the local hub still gets events, Redis does not.
	b.Publish(live.Event{Topic: "room:1", Kind: live.KindPostCreated, PostID: 10})
	var last live.Event
	for range 4 {
		last = nextEvent(t, sub)
	}
	if last.PostID != 10 {
		t.Errorf("last local event has PostID %d, want 10", last.PostID)
	}
	if got := c.publishedCount(); got != 3 {
		t.Errorf("an event was sent to Redis after Close: %d", got)
	}
	if b.Lost() != 0 {
		t.Errorf("Lost() = %d, want 0", b.Lost())
	}
}

func TestCloseIsSafeFromSeveralGoroutines(t *testing.T) {
	t.Parallel()
	broker := &fakeBroker{}
	n := newNode(t, broker, bridgeOptions{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := n.bridge.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestConcurrentPublishAndClose(t *testing.T) {
	t.Parallel()
	broker := &fakeBroker{}
	a := newNode(t, broker, bridgeOptions{queueSize: 8})
	var wg sync.WaitGroup
	for g := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range uint64(200) {
				a.bridge.Publish(live.Event{Topic: "room:1", Kind: live.KindPostCreated, PostID: uint64(g)*1000 + i})
			}
		}()
	}
	if err := a.bridge.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	wg.Wait()
}

func TestLogGate(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC))
	g := newLogGate(clk, time.Second)
	type step struct {
		advance     time.Duration
		wantAllowed bool
		wantSkipped uint64
	}
	for i, s := range []step{
		{0, true, 0},
		{0, false, 0},
		{500 * time.Millisecond, false, 0},
		{499 * time.Millisecond, false, 0},
		{time.Millisecond, true, 3},
		{time.Second, true, 0},
	} {
		clk.Advance(s.advance)
		ok, skipped := g.allow()
		if ok != s.wantAllowed || skipped != s.wantSkipped {
			t.Errorf("step %d: allow() = %v, %d; want %v, %d", i, ok, skipped, s.wantAllowed, s.wantSkipped)
		}
	}
}

func TestCloseDoesNotWaitLongerThanTheFlushTimeout(t *testing.T) {
	t.Parallel()
	broker := &fakeBroker{}
	a := newNode(t, broker, bridgeOptions{})
	// Redis never answers.
	a.client.mu.Lock()
	a.client.publishGate = make(chan struct{})
	a.client.mu.Unlock()
	for i := range uint64(5) {
		a.bridge.Publish(live.Event{Topic: "room:1", Kind: live.KindPostCreated, PostID: i})
	}
	start := time.Now()
	if err := a.bridge.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if took := time.Since(start); took > flushTimeout+2*time.Second {
		t.Errorf("Close took %s, want about %s", took, flushTimeout)
	}
	if a.bridge.Lost() != 5 {
		t.Errorf("Lost() = %d, want 5", a.bridge.Lost())
	}
}
