package calls

import (
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/calls/callstest"
	"github.com/rm4n0s/clowder/internal/metrics"
)

// TestACallMovesTheMetrics runs a call with two pion clients and checks that
// the signalling sockets are counted while they are open, that the call is
// counted when it closes, by how it ended, and that nothing is left behind
// (SPEC D60).
func TestACallMovesTheMetrics(t *testing.T) {
	set, err := metrics.NewSet(metrics.SetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sockets := set.WebSockets.With(metrics.WebSocketKindCall)
	r := newRig(t, Config{SocketsOpen: sockets, Outcomes: set.CallsTotal})
	alice, bob := startCall(t, r, callstest.Options{}, callstest.Options{})
	alice.Receiving(t, 5, 15*time.Second)
	bob.Receiving(t, 5, 15*time.Second)
	if got := sockets.Value(); got != 2 {
		t.Errorf("open call sockets = %d, want 2", got)
	}
	if got := r.srv.Calls(); got != 1 {
		t.Errorf("calls held = %d, want 1", got)
	}
	if got := set.CallsTotal.With("hangup").Value(); got != 0 {
		t.Errorf("calls counted while open = %d, want 0", got)
	}
	bob.Send(map[string]any{"type": "hangup"})
	callstest.WaitFor(t, "the call to be counted", 10*time.Second, func() bool {
		return set.CallsTotal.With("hangup").Value() == 1
	})
	callstest.WaitFor(t, "the sockets to be counted out", 10*time.Second, func() bool { return sockets.Value() == 0 })
	callstest.WaitFor(t, "the server to forget the call", 5*time.Second, func() bool { return r.srv.Calls() == 0 })
	for _, outcome := range []string{"ended", "failed", "server_stopped", "other"} {
		if got := set.CallsTotal.With(outcome).Value(); got != 0 {
			t.Errorf("calls{outcome=%s} = %d, want 0", outcome, got)
		}
	}
}

// TestACallThatFailsIsCountedAsFailed checks another outcome: a participant
// who never joins fails the call.
func TestACallThatFailsIsCountedAsFailed(t *testing.T) {
	set, err := metrics.NewSet(metrics.SetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t, Config{JoinTimeout: 400 * time.Millisecond, Outcomes: set.CallsTotal, SocketsOpen: set.WebSockets.With(metrics.WebSocketKindCall)})
	r.ctl.add(callID, callerID, calleeID, true)
	r.dial("alice", r.url(callID, callerID, calleeID, callerID), callstest.Options{})
	callstest.WaitFor(t, "the call to fail", 10*time.Second, func() bool {
		return set.CallsTotal.With("failed").Value() == 1
	})
}
