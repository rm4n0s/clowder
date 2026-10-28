package calls

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/rm4n0s/clowder/internal/calls/callstest"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/errors"
)

// These tests run two media servers in one test as the two processes of a
// cluster (SPEC D65): they share the state of the calls and the hub, as the
// processes share the database and Redis, and they are joined by an in-memory
// relay. The same path on real Redis is tested in e2e_cluster_test.go.

// memBus joins the relays of the processes of a test and records what they
// send.
type memBus struct {
	mu    sync.Mutex
	procs map[string]*memRelay
	sent  []Envelope
	// dead are the processes whose envelopes are lost: a process that crashed.
	dead map[string]bool
}

func newMemBus() *memBus { return &memBus{procs: map[string]*memRelay{}, dead: map[string]bool{}} }

// relay makes the relay of the process.
func (b *memBus) relay(id string) *memRelay {
	r := &memRelay{bus: b, id: id, in: make(chan Envelope, 256)}
	b.mu.Lock()
	b.procs[id] = r
	b.mu.Unlock()
	return r
}

// kill makes the process lose every envelope sent to it, without the senders
// being told.
func (b *memBus) kill(id string) {
	b.mu.Lock()
	b.dead[id] = true
	b.mu.Unlock()
}

// kinds returns the kinds of the envelopes sent so far, in order.
func (b *memBus) kinds() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.sent))
	for i, e := range b.sent {
		out[i] = e.Kind
	}
	return out
}

// count returns how many envelopes of the kind were sent.
func (b *memBus) count(kind string) int {
	n := 0
	for _, k := range b.kinds() {
		if k == kind {
			n++
		}
	}
	return n
}

// memRelay is the Relay of one process of a memBus.
type memRelay struct {
	bus *memBus
	id  string
	in  chan Envelope
}

func (m *memRelay) Messages() <-chan Envelope { return m.in }

func (m *memRelay) Send(ctx context.Context, to string, env Envelope) error {
	m.bus.mu.Lock()
	dest, ok := m.bus.procs[to]
	dead := m.bus.dead[to]
	m.bus.sent = append(m.bus.sent, env)
	m.bus.mu.Unlock()
	if !ok {
		return errors.New("TestNobodyListening", "no process listens", "to", to)
	}
	if dead {
		return nil
	}
	select {
	case dest.in <- env:
		return nil
	case <-ctx.Done():
		return errors.NewErr("TestSendCancelled", ctx.Err())
	}
}

// inject delivers an envelope to the process as if another one had sent it.
func (m *memRelay) inject(env Envelope) { m.in <- env }

// pair is two media servers that stand for two processes.
type pair struct {
	bus        *memBus
	ctl        *fakeCtl
	hub        *live.Hub
	home, edge *rig
}

const (
	homeID = "home-process"
	edgeID = "edge-process"
)

// newPair starts the home and the edge. cfg changes the configuration of
// both.
func newPair(t *testing.T, cfg Config) *pair {
	t.Helper()
	hub := live.New()
	t.Cleanup(hub.Close)
	p := &pair{bus: newMemBus(), ctl: newFakeCtl(hub), hub: hub}
	homeCfg, edgeCfg := cfg, cfg
	homeCfg.ProcessID, homeCfg.Relay = homeID, p.bus.relay(homeID)
	edgeCfg.ProcessID, edgeCfg.Relay = edgeID, p.bus.relay(edgeID)
	p.home = newRigOn(t, homeCfg, p.ctl, hub)
	p.edge = newRigOn(t, edgeCfg, p.ctl, hub)
	return p
}

// homeURL is the signalling URL on the home of the user in the call.
func (p *pair) homeURL(user uint64) string {
	return p.home.urlHeldBy("", callID, callerID, calleeID, user)
}

// edgeURL is the signalling URL on the edge, whose call is held by the home.
func (p *pair) edgeURL(user uint64) string {
	return p.edge.urlHeldBy(homeID, callID, callerID, calleeID, user)
}

// startRelayedCall makes an active call in which alice is on the home and bob
// on the edge.
func startRelayedCall(t *testing.T, p *pair) (alice, bob *callstest.Client) {
	t.Helper()
	p.ctl.add(callID, callerID, calleeID, true)
	alice = p.home.dial("alice", p.homeURL(callerID), callstest.Options{})
	bob = p.edge.dial("bob", p.edgeURL(calleeID), callstest.Options{})
	return alice, bob
}

func TestAParticipantOnAnotherProcessTakesPartInTheCall(t *testing.T) {
	p := newPair(t, Config{})
	alice, bob := startRelayedCall(t, p)
	alice.Receiving(t, 25, 15*time.Second)
	bob.Receiving(t, 25, 15*time.Second)
	t.Logf("alice (on the home) received audio %d video %d; bob (on the edge) received audio %d video %d",
		alice.AudioIn(), alice.VideoIn(), bob.AudioIn(), bob.VideoIn())

	// The home holds the call and the media of both; the edge holds a socket.
	if p.home.srv.Calls() != 1 || p.edge.srv.Calls() != 0 {
		t.Errorf("calls: %d on the home, %d on the edge, want 1 and 0", p.home.srv.Calls(), p.edge.srv.Calls())
	}
	// The media goes straight to the home: the candidates that bob got name the
	// home's own port, the edge did not touch them.
	homeUDP, homeTCP := p.home.srv.Addr()
	edgeUDP, edgeTCP := p.edge.srv.Addr()
	candidates := strings.Join(bob.Candidates(), "\n")
	if len(bob.Candidates()) == 0 {
		t.Fatal("bob got no candidates")
	}
	for _, want := range []string{strconv.Itoa(portOf(homeUDP.String())), strconv.Itoa(portOf(homeTCP.String()))} {
		if !strings.Contains(candidates, " "+want+" ") {
			t.Errorf("the candidates of bob do not name the home's port %s:\n%s", want, candidates)
		}
	}
	for _, other := range []string{strconv.Itoa(portOf(edgeUDP.String())), strconv.Itoa(portOf(edgeTCP.String()))} {
		if strings.Contains(candidates, " "+other+" ") {
			t.Errorf("the candidates of bob name the edge's port %s:\n%s", other, candidates)
		}
	}
	// Nothing but the signalling went through the relay.
	for _, kind := range p.bus.kinds() {
		switch kind {
		case EnvJoin, EnvClient, EnvServer, EnvLeave, EnvClose:
		default:
			t.Errorf("an envelope of kind %q", kind)
		}
	}
	if p.bus.count(EnvJoin) != 1 {
		t.Errorf("%d joins, want 1", p.bus.count(EnvJoin))
	}
	// The edge logged the join and the messages, by type, and the logs hold no
	// SDP, no candidate and nothing of the media above the debug level.
	for name, logs := range map[string]string{"home": p.home.logs.String(), "edge": p.edge.logs.String()} {
		for _, line := range strings.Split(logs, "\n") {
			if strings.Contains(line, "level=INFO") && (strings.Contains(line, "candidate") || strings.Contains(line, "v=0") || strings.Contains(line, "a=")) {
				t.Errorf("%s: an info line mentions the media: %s", name, line)
			}
			if strings.Contains(line, "relay:") && (strings.Contains(line, "v=0") || strings.Contains(line, "a=candidate") || strings.Contains(line, "sdp")) {
				t.Errorf("%s: a relay line holds a message: %s", name, line)
			}
		}
	}
	for _, want := range []string{"relay: join sent", "relay: client message sent", "relay: server message received"} {
		if !strings.Contains(p.edge.logs.String(), want) {
			t.Errorf("the edge did not log %q", want)
		}
	}
	for _, want := range []string{"relay: join received", "relay: client message received", "relay: server message sent"} {
		if !strings.Contains(p.home.logs.String(), want) {
			t.Errorf("the home did not log %q", want)
		}
	}
}

// portOf returns the port of a host:port string.
func portOf(addr string) int {
	i := strings.LastIndex(addr, ":")
	n, _ := strconv.Atoi(addr[i+1:])
	return n
}

func TestARemoteParticipantsMessagesReachTheCall(t *testing.T) {
	p := newPair(t, Config{})
	alice, bob := startRelayedCall(t, p)
	alice.Receiving(t, 5, 15*time.Second)
	bob.Receiving(t, 5, 15*time.Second)

	// The microphone and the camera of the remote participant reach the other.
	bob.Send(map[string]any{"type": "state", "microphone": false, "camera": true})
	callstest.WaitFor(t, "alice to hear of bob's microphone", 10*time.Second, func() bool {
		states := alice.PeerStates()
		return len(states) > 0 && states[len(states)-1].Microphone != nil && !*states[len(states)-1].Microphone
	})
	// And the other way, through the home's call to the edge.
	alice.Send(map[string]any{"type": "state", "microphone": true, "camera": false})
	callstest.WaitFor(t, "bob to hear of alice's camera", 10*time.Second, func() bool {
		states := bob.PeerStates()
		return len(states) > 0 && states[len(states)-1].Camera != nil && !*states[len(states)-1].Camera
	})
	// A message that is not of the protocol is answered by the edge itself and
	// is not relayed.
	before := p.bus.count(EnvClient)
	bob.WriteRaw(websocket.MessageText, []byte(`{"type":"nope"}`))
	callstest.WaitFor(t, "the error message", 10*time.Second, func() bool { return len(bob.Errors()) == 1 })
	if errs := bob.Errors(); errs[0].Code != CodeInvalidMessage {
		t.Errorf("error = %+v", errs[0])
	}
	if p.bus.count(EnvClient) != before {
		t.Error("a message that is not of the protocol was relayed")
	}
	// The hang-up of the remote participant ends the call for both.
	bob.Send(map[string]any{"type": "hangup"})
	for _, c := range []*callstest.Client{alice, bob} {
		select {
		case reason := <-c.Ended():
			if reason != "hangup" {
				t.Errorf("%s was told %q", c.Name(), reason)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s was not told that the call ended", c.Name())
		}
		callstest.WaitFor(t, c.Name()+"'s socket to close", 10*time.Second, func() bool { return c.CloseCode() == int(websocket.StatusNormalClosure) })
	}
	p.ctl.mu.Lock()
	left := append([]uint64(nil), p.ctl.left...)
	media := append([]mediaState(nil), p.ctl.media...)
	p.ctl.mu.Unlock()
	if len(left) != 1 || left[0] != calleeID {
		t.Errorf("Leave was called for %v, want bob", left)
	}
	if len(media) != 2 {
		t.Errorf("SetMedia calls = %+v, want one for each participant", media)
	}
	callstest.WaitFor(t, "the home to forget the call", 5*time.Second, func() bool { return p.home.srv.Calls() == 0 })
	callstest.WaitFor(t, "the edge to forget the socket", 5*time.Second, func() bool {
		p.edge.srv.mu.Lock()
		defer p.edge.srv.mu.Unlock()
		return len(p.edge.srv.edges) == 0
	})
}

func TestTheEndOfACallFromTheServiceReachesTheRemoteParticipant(t *testing.T) {
	p := newPair(t, Config{})
	alice, bob := startRelayedCall(t, p)
	alice.Receiving(t, 5, 15*time.Second)
	bob.Receiving(t, 5, 15*time.Second)
	p.ctl.end(callID, "disabled")
	for _, c := range []*callstest.Client{alice, bob} {
		select {
		case reason := <-c.Ended():
			if reason != "disabled" {
				t.Errorf("%s was told %q", c.Name(), reason)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s was not told that the call ended", c.Name())
		}
	}
	callstest.WaitFor(t, "bob's socket to close", 10*time.Second, func() bool { return bob.CloseCode() == int(websocket.StatusNormalClosure) })
}

func TestANewSocketReplacesTheOneOnAnotherProcess(t *testing.T) {
	tests := []struct {
		name string
		// url is where the second socket of bob opens.
		url func(p *pair) string
	}{
		{"on the same edge", func(p *pair) string { return p.edgeURL(calleeID) }},
		{"on the home", func(p *pair) string { return p.homeURL(calleeID) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newPair(t, Config{})
			alice, bob := startRelayedCall(t, p)
			alice.Receiving(t, 10, 15*time.Second)
			bob.Receiving(t, 10, 15*time.Second)
			second := p.home.dial("bob's other tab", tt.url(p), callstest.Options{})
			// The home told the old edge, which closed the first socket with 4003.
			callstest.WaitFor(t, "the first socket to close", 10*time.Second, func() bool { return bob.CloseCode() == int(closeReplaced) })
			second.Receiving(t, 20, 15*time.Second)
			alice.Receiving(t, 20, 15*time.Second)
			if n := p.home.srv.Calls(); n != 1 {
				t.Errorf("%d calls on the home, want 1", n)
			}
			// The first socket's leave reached the home after the new socket had
			// replaced it: it was dropped, so the call has not been failed.
			time.Sleep(300 * time.Millisecond)
			if n := len(p.ctl.failedCalls()); n != 0 {
				t.Errorf("the call failed (%d) although bob is in it", n)
			}
			if !strings.Contains(p.home.logs.String(), "relay message dropped") || !strings.Contains(p.home.logs.String(), "the socket was replaced") {
				t.Errorf("the leave of the replaced socket was not dropped and logged:\n%s", p.home.logs.String())
			}
		})
	}
}

func TestASessionThatChangesOnTheEdgeClosesTheSocketThere(t *testing.T) {
	tests := []struct {
		name   string
		code   websocket.StatusCode
		reason string
	}{
		{"the session ended", CloseSessionEnded, "session ended"},
		{"the session changed", CloseSessionChanged, "session changed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newPair(t, Config{ReconnectGrace: 500 * time.Millisecond})
			alice, bob := startRelayedCall(t, p)
			alice.Receiving(t, 5, 15*time.Second)
			bob.Receiving(t, 5, 15*time.Second)
			// The session is checked where the socket is: on the edge.
			p.edge.endSession(calleeID, tt.code, tt.reason)
			p.hub.Publish(live.Event{Topic: live.UserTopic(calleeID), Kind: live.KindUserChanged, UserID: calleeID})
			callstest.WaitFor(t, "bob's socket to close", 10*time.Second, func() bool { return bob.CloseCode() == int(tt.code) })
			// Alice's socket is untouched.
			if alice.CloseCode() != 0 {
				t.Errorf("alice's socket closed with %d", alice.CloseCode())
			}
			// As on a socket of the home, the participant has the reconnect grace;
			// nobody opens a new socket, so the call fails and alice is told.
			callstest.WaitFor(t, "the call to fail", 10*time.Second, func() bool { return len(p.ctl.failedCalls()) == 1 })
			select {
			case reason := <-alice.Ended():
				if reason != "failed" {
					t.Errorf("alice was told %q", reason)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("alice was not told that the call ended")
			}
		})
	}
	t.Run("the ping checks the session again", func(t *testing.T) {
		p := newPair(t, Config{PingInterval: 200 * time.Millisecond, PingTimeout: 2 * time.Second})
		_, bob := startRelayedCall(t, p)
		bob.Receiving(t, 5, 15*time.Second)
		p.edge.endSession(calleeID, CloseSessionEnded, "session ended")
		callstest.WaitFor(t, "bob's socket to close", 10*time.Second, func() bool { return bob.CloseCode() == int(CloseSessionEnded) })
	})
}

func TestAReconnectedRemoteParticipantKeepsTheCall(t *testing.T) {
	p := newPair(t, Config{ReconnectGrace: 5 * time.Second})
	alice, bob := startRelayedCall(t, p)
	alice.Receiving(t, 10, 15*time.Second)
	bob.Receiving(t, 10, 15*time.Second)
	bob.Close()
	time.Sleep(300 * time.Millisecond)
	again := p.edge.dial("bob again", p.edgeURL(calleeID), callstest.Options{})
	again.Receiving(t, 25, 15*time.Second)
	alice.Receiving(t, 25, 15*time.Second)
	time.Sleep(500 * time.Millisecond)
	if n := len(p.ctl.failedCalls()); n != 0 {
		t.Errorf("the call failed although bob came back")
	}
}

func TestACallThatIsOverIsToldToTheEdgeWithoutJoining(t *testing.T) {
	p := newPair(t, Config{})
	p.ctl.add(callID, callerID, calleeID, true)
	p.ctl.end(callID, "hangup")
	bob := p.edge.dial("bob", p.edgeURL(calleeID), callstest.Options{})
	select {
	case reason := <-bob.Ended():
		if reason != "hangup" {
			t.Errorf("reason = %q", reason)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("bob was not told that the call is over")
	}
	callstest.WaitFor(t, "the socket to close", 10*time.Second, func() bool { return bob.CloseCode() == int(websocket.StatusNormalClosure) })
	if n := p.bus.count(EnvJoin); n != 0 {
		t.Errorf("%d joins were sent for a call that is over", n)
	}
}

func TestAHomeThatIsNotListeningRefusesTheJoin(t *testing.T) {
	p := newPair(t, Config{})
	p.ctl.add(callID, callerID, calleeID, true)
	bob := p.edge.dial("bob", p.edge.urlHeldBy("ghost-process", callID, callerID, calleeID, calleeID), callstest.Options{})
	callstest.WaitFor(t, "the socket to close", 10*time.Second, func() bool { return bob.CloseCode() != 0 })
	if errs := bob.Errors(); len(errs) != 1 || errs[0].Code != CodeInternal {
		t.Errorf("errors = %+v, want one internal error", errs)
	}
	if bob.CloseCode() != int(websocket.StatusInternalError) {
		t.Errorf("close code = %d", bob.CloseCode())
	}
}

func TestAServerWithoutARelayRefusesACallOfAnotherProcess(t *testing.T) {
	r := newRig(t, Config{ProcessID: "alone"})
	r.ctl.add(callID, callerID, calleeID, true)
	bob := r.dial("bob", r.urlHeldBy("someone-else", callID, callerID, calleeID, calleeID), callstest.Options{})
	callstest.WaitFor(t, "the socket to close", 10*time.Second, func() bool { return bob.CloseCode() != 0 })
	if errs := bob.Errors(); len(errs) != 1 || errs[0].Code != CodeInternal {
		t.Errorf("errors = %+v, want one internal error", errs)
	}

	_, err := r.srv.edgeRelay(Join{CallID: callID, Home: "someone-else"})
	appErr, ok := errors.FromError(err)
	if !ok || appErr.Tag != TagNoRelay || !appErr.HasRoute("Server.edgeRelay.MediaNoRelay") || appErr.Metadata["home"] != "someone-else" {
		t.Errorf("edgeRelay = %v", err)
	}
}

func TestARelayNeedsTheIDOfTheProcess(t *testing.T) {
	hub := live.New()
	defer hub.Close()
	_, err := New(t.Context(), Config{Addr: "127.0.0.1:0", Relay: newMemBus().relay("x")}, newFakeCtl(hub), hub)
	appErr, ok := errors.FromError(err)
	if !ok || appErr.Tag != TagSetupFailed || !appErr.HasRoute("New.MediaSetupFailed") || appErr.Metadata["step"] != "relay" {
		t.Errorf("err = %v", err)
	}
}

func TestTheHomeStopping(t *testing.T) {
	t.Run("a home that stops tells the remote participant", func(t *testing.T) {
		p := newPair(t, Config{})
		alice, bob := startRelayedCall(t, p)
		alice.Receiving(t, 5, 15*time.Second)
		bob.Receiving(t, 5, 15*time.Second)
		if err := p.home.srv.Close(); err != nil {
			t.Fatalf("Close: %+v", err)
		}
		for _, c := range []*callstest.Client{alice, bob} {
			select {
			case reason := <-c.Ended():
				if reason != "server_stopped" {
					t.Errorf("%s was told %q", c.Name(), reason)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("%s was not told that the server stopped", c.Name())
			}
		}
		callstest.WaitFor(t, "bob's socket to close", 10*time.Second, func() bool { return bob.CloseCode() == int(websocket.StatusNormalClosure) })
	})
	t.Run("a home that dies is found out through the end of the call", func(t *testing.T) {
		p := newPair(t, Config{})
		alice, bob := startRelayedCall(t, p)
		alice.Receiving(t, 5, 15*time.Second)
		bob.Receiving(t, 5, 15*time.Second)
		// The home is gone and says nothing: another process ends the call in
		// the database, which every hub hears.
		p.bus.kill(homeID)
		if err := p.home.srv.Close(); err != nil {
			t.Fatalf("Close: %+v", err)
		}
		p.ctl.end(callID, "server_stopped")
		select {
		case reason := <-bob.Ended():
			if reason != "server_stopped" {
				t.Errorf("bob was told %q", reason)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("bob was not told that the call ended")
		}
		callstest.WaitFor(t, "bob's socket to close", 10*time.Second, func() bool { return bob.CloseCode() == int(websocket.StatusNormalClosure) })
	})
}

func TestAnEdgeThatStopsClosesItsSockets(t *testing.T) {
	p := newPair(t, Config{ReconnectGrace: 5 * time.Second})
	alice, bob := startRelayedCall(t, p)
	alice.Receiving(t, 5, 15*time.Second)
	bob.Receiving(t, 5, 15*time.Second)
	if err := p.edge.srv.Close(); err != nil {
		t.Fatalf("Close: %+v", err)
	}
	callstest.WaitFor(t, "bob's socket to close", 10*time.Second, func() bool { return bob.CloseCode() == int(websocket.StatusGoingAway) })
	// The call goes on for alice; bob has the grace to come back.
	alice.Receiving(t, 10, 15*time.Second)
	if n := len(p.ctl.failedCalls()); n != 0 {
		t.Errorf("the call failed (%d) within the grace", n)
	}
}

func TestTheRelayDropsWhatIsNotForACallOfTheHome(t *testing.T) {
	p := newPair(t, Config{})
	alice, bob := startRelayedCall(t, p)
	alice.Receiving(t, 5, 15*time.Second)
	bob.Receiving(t, 5, 15*time.Second)
	homeIn := p.bus.procs[homeID]
	edgeIn := p.bus.procs[edgeID]

	hangup := json.RawMessage(`{"type":"hangup"}`)
	// bob's socket id is the one of the join the edge sent.
	var bobSocket string
	p.bus.mu.Lock()
	for _, e := range p.bus.sent {
		if e.Kind == EnvJoin && e.UserID == calleeID {
			bobSocket = e.Socket
		}
	}
	p.bus.mu.Unlock()
	if bobSocket == "" {
		t.Fatal("no join of bob")
	}
	drops := []struct {
		name string
		into *memRelay
		env  Envelope
		why  string
	}{
		{"a message for a call that is not held here", homeIn,
			Envelope{Kind: EnvClient, From: edgeID, CallID: 999, UserID: calleeID, Socket: bobSocket, Message: hangup},
			"the call has ended or is not held here"},
		{"a leave for a call that is not held here", homeIn,
			Envelope{Kind: EnvLeave, From: edgeID, CallID: 999, UserID: calleeID, Socket: bobSocket},
			"the call has ended or is not held here"},
		{"a message of someone who is no participant", homeIn,
			Envelope{Kind: EnvClient, From: edgeID, CallID: callID, UserID: 99, Socket: bobSocket, Message: hangup},
			"unknown participant"},
		{"a message of a socket that was replaced", homeIn,
			Envelope{Kind: EnvClient, From: edgeID, CallID: callID, UserID: calleeID, Socket: "an-old-socket", Message: hangup},
			"the socket was replaced"},
		{"a message of the caller, who has a socket of the home and not a remote one", homeIn,
			Envelope{Kind: EnvClient, From: edgeID, CallID: callID, UserID: callerID, Socket: "x", Message: hangup},
			"the socket was replaced"},
		{"a leave of a socket that was replaced", homeIn,
			Envelope{Kind: EnvLeave, From: edgeID, CallID: callID, UserID: calleeID, Socket: "an-old-socket"},
			"the socket was replaced"},
		{"a message that is not of the protocol", homeIn,
			Envelope{Kind: EnvClient, From: edgeID, CallID: callID, UserID: calleeID, Socket: bobSocket, Message: json.RawMessage(`{"type":"nope"}`)},
			"not a message of the protocol"},
		{"a join without its call", homeIn,
			Envelope{Kind: EnvJoin, From: edgeID, CallID: callID, UserID: calleeID, Socket: "s"},
			"malformed join"},
		{"a join of a user who is no participant", homeIn,
			Envelope{Kind: EnvJoin, From: edgeID, CallID: callID, UserID: 99, Socket: "s", Join: &EnvelopeJoin{ConversationID: 7, CallerID: callerID, CalleeID: calleeID}},
			"unknown participant"},
		{"an envelope of a kind nobody knows", homeIn,
			Envelope{Kind: "hello", From: edgeID, CallID: callID, UserID: calleeID, Socket: bobSocket},
			"unknown kind"},
		{"a message for a socket the edge does not have", edgeIn,
			Envelope{Kind: EnvServer, From: homeID, CallID: callID, UserID: calleeID, Socket: "gone", Message: json.RawMessage(`{"type":"ended","reason":"hangup"}`)},
			"no such socket"},
		{"a close for a socket the edge does not have", edgeIn,
			Envelope{Kind: EnvClose, From: homeID, CallID: callID, UserID: calleeID, Socket: "gone", Code: 1000},
			"no such socket"},
		{"a message for the edge's socket that is not of the protocol", edgeIn,
			Envelope{Kind: EnvServer, From: homeID, CallID: callID, UserID: calleeID, Socket: bobSocket, Message: json.RawMessage(`[1]`)},
			"not a message of the protocol"},
	}
	for _, d := range drops {
		logs := p.home.logs
		if d.into == edgeIn {
			logs = p.edge.logs
		}
		d.into.inject(d.env)
		callstest.WaitFor(t, d.name+" to be dropped", 10*time.Second, func() bool {
			return strings.Contains(logs.String(), `why="`+d.why+`"`)
		})
	}
	// None of them did anything: the call goes on, nobody hung up and the
	// sockets are open.
	alice.Receiving(t, 10, 15*time.Second)
	bob.Receiving(t, 10, 15*time.Second)
	p.ctl.mu.Lock()
	left := len(p.ctl.left)
	p.ctl.mu.Unlock()
	if left != 0 || alice.CloseCode() != 0 || bob.CloseCode() != 0 {
		t.Errorf("a dropped message had an effect: %d hang-ups, sockets %d and %d", left, alice.CloseCode(), bob.CloseCode())
	}

	// And a message for a call that has ended is dropped as well.
	p.ctl.end(callID, "hangup")
	callstest.WaitFor(t, "the home to forget the call", 10*time.Second, func() bool { return p.home.srv.Calls() == 0 })
	before := strings.Count(p.home.logs.String(), "relay message dropped")
	homeIn.inject(Envelope{Kind: EnvClient, From: edgeID, CallID: callID, UserID: calleeID, Socket: bobSocket, Message: hangup})
	callstest.WaitFor(t, "a message for the ended call to be dropped", 10*time.Second, func() bool {
		return strings.Count(p.home.logs.String(), "relay message dropped") > before
	})
}
