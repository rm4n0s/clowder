package cluster

import (
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/metrics"
	"github.com/rm4n0s/errors"
)

// waitFor polls cond until it holds or the wait is over.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// bridgeMetrics makes the counters of a bridge in a registry of its own.
func bridgeMetrics(t *testing.T) BridgeMetrics {
	t.Helper()
	set, err := metrics.NewSet(metrics.SetOptions{Redis: true})
	if err != nil {
		t.Fatal(err)
	}
	return BridgeMetrics{Reconnects: set.RedisReconnects, Published: set.RedisPublished, Received: set.RedisReceived}
}

// TestBridgeCountsItsWork checks the counters of SPEC D60: the events a bridge
// sent to Redis, the events of other processes it delivered (its own echo is
// not counted), and the reconnects; a failed publish is not a published event.
func TestBridgeCountsItsWork(t *testing.T) {
	t.Parallel()
	broker := &fakeBroker{}
	ma, mb := bridgeMetrics(t), bridgeMetrics(t)
	a := newNode(t, broker, bridgeOptions{metrics: ma})
	b := newNode(t, broker, bridgeOptions{metrics: mb})
	subB := b.hub.Subscribe(t.Context(), "room:1")
	subA := a.hub.Subscribe(t.Context(), "room:1")

	for i := range uint64(3) {
		a.bridge.Publish(live.Event{Topic: "room:1", Kind: live.KindPostCreated, PostID: i + 1})
	}
	a.bridge.Publish(live.Event{Topic: "room:1", Kind: kindFence})
	collect(t, subB)
	if got := ma.Published.Value(); got != 4 {
		t.Errorf("a published %d, want 4", got)
	}
	if got := mb.Received.Value(); got != 4 {
		t.Errorf("b received %d, want 4", got)
	}
	if got := ma.Received.Value(); got != 0 {
		t.Errorf("a received %d, want 0 (its own events are skipped)", got)
	}

	// The echo of a's events reached a before b's fence: a skipped them.
	b.bridge.Publish(live.Event{Topic: "room:1", Kind: kindFence, PostID: 99})
	for nextEvent(t, subA).PostID != 99 {
	}
	if got := ma.Received.Value(); got != 1 {
		t.Errorf("a received %d, want 1", got)
	}
	if got := mb.Published.Value(); got != 1 {
		t.Errorf("b published %d, want 1", got)
	}

	if got := mb.Reconnects.Value(); got != 0 {
		t.Errorf("reconnects before a break = %d, want 0", got)
	}
	b.client.mine().reconnect()
	waitFor(t, "the reconnect to be counted", func() bool { return mb.Reconnects.Value() == 1 })
	if got := ma.Reconnects.Value(); got != 0 {
		t.Errorf("a reconnected %d times, want 0", got)
	}

	// A publish that fails counts as lost, not as published.
	a.client.mu.Lock()
	a.client.publishErr = errors.New("RedisPublishFailed", "connection refused")
	a.client.mu.Unlock()
	a.bridge.Publish(live.Event{Topic: "room:1", Kind: live.KindPostCreated, PostID: 5})
	waitFor(t, "the failed publish", func() bool { return a.bridge.Lost() == 1 })
	if got := ma.Published.Value(); got != 4 {
		t.Errorf("a published %d after a failure, want 4", got)
	}
}
