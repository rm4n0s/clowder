package api

import (
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/rm4n0s/clowder/internal/metrics"
)

// TestWebSocketGaugeFollowsTheSockets checks that the gauge of SPEC D60 goes
// up with each open WebSocket and down when it closes.
func TestWebSocketGaugeFollowsTheSockets(t *testing.T) {
	t.Parallel()
	set, err := metrics.NewSet(metrics.SetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	gauge := set.WebSockets.With(metrics.WebSocketKindAPI)
	w := wsForumOf(t, forumWith(t, func(o *Options) { o.WebSockets = gauge }))
	waitGauge := func(want int64) {
		t.Helper()
		deadline := time.Now().Add(wsWait)
		for gauge.Value() != want {
			if time.Now().After(deadline) {
				t.Fatalf("gauge = %d, want %d", gauge.Value(), want)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if gauge.Value() != 0 {
		t.Fatalf("gauge before any socket = %d", gauge.Value())
	}
	alice := w.dial("alice")
	alice.mustSubscribe(topicOf(topicRoom, w.den.ID))
	bob := w.dial("bob")
	bob.mustSubscribe(topicOf(topicRoom, w.den.ID))
	waitGauge(2)
	if err := alice.conn.Close(websocket.StatusNormalClosure, "bye"); err != nil {
		t.Logf("close: %v", err)
	}
	waitGauge(1)
	if err := bob.conn.Close(websocket.StatusNormalClosure, "bye"); err != nil {
		t.Logf("close: %v", err)
	}
	waitGauge(0)
}
