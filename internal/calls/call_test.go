package calls

import (
	"net"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/rm4n0s/clowder/internal/calls/callstest"
	"github.com/rm4n0s/clowder/internal/live"
)

const (
	callerID = 10
	calleeID = 20
	callID   = 1
)

// startCall puts an active call in the rig and joins both clients.
func startCall(t *testing.T, r *rig, a, b callstest.Options) (alice, bob *callstest.Client) {
	t.Helper()
	r.ctl.add(callID, callerID, calleeID, true)
	alice = r.dial("alice", r.url(callID, callerID, calleeID, callerID), a)
	bob = r.dial("bob", r.url(callID, callerID, calleeID, calleeID), b)
	return alice, bob
}

func TestTwoClientsReceiveEachOthersMedia(t *testing.T) {
	r := newRig(t, Config{})
	alice, bob := startCall(t, r, callstest.Options{}, callstest.Options{})
	alice.Receiving(t, 25, 15*time.Second)
	bob.Receiving(t, 25, 15*time.Second)
	t.Logf("alice received audio %d video %d; bob received audio %d video %d",
		alice.AudioIn(), alice.VideoIn(), bob.AudioIn(), bob.VideoIn())

	// Both PeerConnections got the video limit in the offer (SPEC D64).
	for _, c := range []*callstest.Client{alice, bob} {
		offer := c.LastOffer()
		if !strings.Contains(offer, "b=AS:2500\r\n") || !strings.Contains(offer, "b=TIAS:2500000\r\n") {
			t.Errorf("%s: the offer has no bandwidth lines:\n%s", c.Name(), offer)
		}
		if audio := offer[:strings.Index(offer, "m=video")]; strings.Contains(audio, "b=AS") {
			t.Errorf("%s: the audio section has a bandwidth line", c.Name())
		}
		if strings.Contains(offer, "H264") {
			t.Errorf("%s: the offer has H.264", c.Name())
		}
		if !strings.Contains(offer, "opus/48000/2") || !strings.Contains(offer, "VP8/90000") {
			t.Errorf("%s: the offer lacks Opus or VP8", c.Name())
		}
	}
	// And REMB says it on the wire.
	callstest.WaitFor(t, "REMB", 10*time.Second, func() bool { return len(alice.REMBs()) > 0 })
	if got := alice.REMBs()[0]; got < 2.4e6 || got > 2.6e6 {
		t.Errorf("REMB = %v, want about 2.5 Mbit/s", got)
	}

	// Nothing of the media, no SDP and no candidate is logged above debug.
	for _, line := range strings.Split(r.logs.String(), "\n") {
		if strings.Contains(line, "level=INFO") && (strings.Contains(line, "candidate") || strings.Contains(line, "v=0") || strings.Contains(line, "a=")) {
			t.Errorf("an info line mentions the media: %s", line)
		}
	}
	if !strings.Contains(r.logs.String(), "participant joined") || !strings.Contains(r.logs.String(), "state=connected") {
		t.Errorf("the log does not tell of the join and the connection:\n%s", r.logs.String())
	}
}

func TestScreenShareKeepsTheOtherSideReceiving(t *testing.T) {
	tests := []struct {
		name string
		// replace puts a screen in place of the camera on the transceiver.
		replace func(c *callstest.Client) error
	}{
		{"a track that goes on numbering the packets, as a browser does", (*callstest.Client).ShareScreenRTP},
		{"a track with sequence numbers and timestamps of its own", (*callstest.Client).ShareScreenSample},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, Config{})
			alice, bob := startCall(t, r, callstest.Options{}, callstest.Options{})
			bob.Receiving(t, 20, 15*time.Second)
			offers := alice.Offers()
			if err := tt.replace(alice); err != nil {
				t.Fatalf("replacing the camera: %v", err)
			}
			before := bob.VideoIn()
			callstest.WaitFor(t, "bob to keep receiving video", 10*time.Second, func() bool { return bob.VideoIn()-before >= 40 })
			if alice.Offers() != offers {
				t.Errorf("the screen share caused %d new offers, want none", alice.Offers()-offers)
			}
			// The packets of both sources leave the server on one SSRC.
			if got := bob.VideoSSRCs(); len(got) != 1 {
				t.Errorf("bob saw the video on %d SSRCs, want 1: %v", len(got), got)
			}
		})
	}
}

func TestKeyFrameRequestsGoToTheSender(t *testing.T) {
	r := newRig(t, Config{})
	alice, bob := startCall(t, r, callstest.Options{}, callstest.Options{})
	alice.Receiving(t, 10, 15*time.Second)
	bob.Receiving(t, 10, 15*time.Second)
	// The server asked both senders for a key frame when the connections came up.
	callstest.WaitFor(t, "the key frame request of the connection", 10*time.Second, func() bool { return alice.PLIs() > 0 && bob.PLIs() > 0 })
	// bob's own request for a key frame goes to alice.
	before := alice.PLIs()
	var ssrc uint32
	for s := range bob.VideoSSRCs() {
		ssrc = s
	}
	time.Sleep(400 * time.Millisecond) // past the gap between two requests
	if err := bob.PC().WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: ssrc}}); err != nil {
		t.Fatal(err)
	}
	callstest.WaitFor(t, "alice to get bob's PLI", 10*time.Second, func() bool { return alice.PLIs() > before })
}

func TestAudioOnlyClientsKeepTheCallUp(t *testing.T) {
	r := newRig(t, Config{})
	alice, bob := startCall(t, r, callstest.Options{NoVideo: true}, callstest.Options{NoVideo: true})
	a0, b0 := alice.AudioIn(), bob.AudioIn()
	callstest.WaitFor(t, "audio both ways", 15*time.Second, func() bool {
		return alice.AudioIn()-a0 >= 25 && bob.AudioIn()-b0 >= 25
	})
	if alice.VideoIn() != 0 || bob.VideoIn() != 0 {
		t.Errorf("video packets in an audio call: %d %d", alice.VideoIn(), bob.VideoIn())
	}
}

func TestICETCPAlone(t *testing.T) {
	r := newRig(t, Config{})
	tcp := []webrtc.NetworkType{webrtc.NetworkTypeTCP4}
	alice, bob := startCall(t, r, callstest.Options{Networks: tcp}, callstest.Options{Networks: tcp})
	alice.Receiving(t, 20, 20*time.Second)
	bob.Receiving(t, 20, 20*time.Second)
}

func TestICEThatNeverConnectsEndsTheCall(t *testing.T) {
	r := newRig(t, Config{ICETimeout: 400 * time.Millisecond})
	r.ctl.add(callID, callerID, calleeID, true)
	alice := r.dial("alice", r.url(callID, callerID, calleeID, callerID), callstest.Options{NoCandidates: true})
	started := time.Now()
	callstest.WaitFor(t, "the call to fail", 10*time.Second, func() bool { return len(r.ctl.failedCalls()) == 1 })
	if took := time.Since(started); took < 300*time.Millisecond {
		t.Errorf("the call failed after %v, before the deadline", took)
	}
	select {
	case reason := <-alice.Ended():
		if reason != "failed" {
			t.Errorf("ended reason = %q, want failed", reason)
		}
	case <-time.After(5 * time.Second):
		t.Error("alice was not told that the call ended")
	}
	callstest.WaitFor(t, "the socket to close", 5*time.Second, func() bool { return alice.CloseCode() == int(websocket.StatusNormalClosure) })
	callstest.WaitFor(t, "the server to forget the call", 5*time.Second, func() bool { return r.srv.Calls() == 0 })
}

func TestAParticipantWhoNeverJoinsFailsTheCall(t *testing.T) {
	r := newRig(t, Config{JoinTimeout: 400 * time.Millisecond})
	r.ctl.add(callID, callerID, calleeID, true)
	alice := r.dial("alice", r.url(callID, callerID, calleeID, callerID), callstest.Options{})
	callstest.WaitFor(t, "the call to fail", 10*time.Second, func() bool { return len(r.ctl.failedCalls()) == 1 })
	if reason := <-alice.Ended(); reason != "failed" {
		t.Errorf("reason = %q", reason)
	}
}

func TestAJoinTimerStartsWhenTheCallIsAnswered(t *testing.T) {
	r := newRig(t, Config{JoinTimeout: 500 * time.Millisecond, ICETimeout: 30 * time.Second})
	// The call rings: nobody is waited for.
	r.ctl.add(callID, callerID, calleeID, false)
	alice := r.dial("alice", r.url(callID, callerID, calleeID, callerID), callstest.Options{})
	callstest.WaitFor(t, "alice to connect", 10*time.Second, func() bool {
		select {
		case <-alice.Connected():
			return true
		default:
			return false
		}
	})
	time.Sleep(800 * time.Millisecond)
	if n := len(r.ctl.failedCalls()); n != 0 {
		t.Fatalf("a ringing call failed (%d)", n)
	}
	// Answered, and bob never joins.
	r.ctl.answer(callID)
	callstest.WaitFor(t, "the call to fail", 10*time.Second, func() bool { return len(r.ctl.failedCalls()) == 1 })
}

func TestAClosedSocketGetsTheReconnectGrace(t *testing.T) {
	t.Run("not opened again", func(t *testing.T) {
		r := newRig(t, Config{ReconnectGrace: 400 * time.Millisecond})
		alice, bob := startCall(t, r, callstest.Options{}, callstest.Options{})
		alice.Receiving(t, 10, 15*time.Second)
		bob.Receiving(t, 10, 15*time.Second)
		started := time.Now()
		alice.Close()
		callstest.WaitFor(t, "the call to fail", 10*time.Second, func() bool { return len(r.ctl.failedCalls()) == 1 })
		if took := time.Since(started); took < 300*time.Millisecond {
			t.Errorf("failed after %v, before the grace", took)
		}
		if reason := <-bob.Ended(); reason != "failed" {
			t.Errorf("bob was told %q", reason)
		}
	})
	t.Run("opened again", func(t *testing.T) {
		r := newRig(t, Config{ReconnectGrace: 5 * time.Second})
		alice, bob := startCall(t, r, callstest.Options{}, callstest.Options{})
		alice.Receiving(t, 10, 15*time.Second)
		bob.Receiving(t, 10, 15*time.Second)
		alice.Close()
		time.Sleep(300 * time.Millisecond)
		again := r.dial("alice again", r.url(callID, callerID, calleeID, callerID), callstest.Options{})
		again.Receiving(t, 25, 15*time.Second)
		bob.Receiving(t, 25, 15*time.Second)
		time.Sleep(500 * time.Millisecond)
		if n := len(r.ctl.failedCalls()); n != 0 {
			t.Errorf("the call failed although alice came back")
		}
	})
}

func TestASecondSocketReplacesTheFirst(t *testing.T) {
	r := newRig(t, Config{})
	alice, bob := startCall(t, r, callstest.Options{}, callstest.Options{})
	alice.Receiving(t, 10, 15*time.Second)
	second := r.dial("alice's other tab", r.url(callID, callerID, calleeID, callerID), callstest.Options{})
	callstest.WaitFor(t, "the first socket to close", 10*time.Second, func() bool { return alice.CloseCode() == int(closeReplaced) })
	second.Receiving(t, 20, 15*time.Second)
	bob.Receiving(t, 20, 15*time.Second)
	if n := r.srv.Calls(); n != 1 {
		t.Errorf("%d calls, want 1", n)
	}
}

func TestTheStateOfTheMicrophoneAndTheCameraReachesTheOtherSide(t *testing.T) {
	r := newRig(t, Config{})
	alice, bob := startCall(t, r, callstest.Options{}, callstest.Options{})
	bob.Receiving(t, 5, 15*time.Second)
	alice.Send(map[string]any{"type": "state", "microphone": false, "camera": true})
	callstest.WaitFor(t, "bob to hear", 10*time.Second, func() bool { return len(bob.PeerStates()) > 0 })
	states := bob.PeerStates()
	got := states[len(states)-1]
	if got.Microphone == nil || *got.Microphone || got.Camera == nil || !*got.Camera {
		t.Errorf("bob was told %+v", got)
	}
	r.ctl.mu.Lock()
	media := append([]mediaState(nil), r.ctl.media...)
	r.ctl.mu.Unlock()
	if len(media) != 1 || media[0] != (mediaState{callID, callerID, false, true}) {
		t.Errorf("SetMedia calls = %+v", media)
	}
	// Someone who joins later learns the state at once.
	late := r.dial("bob again", r.url(callID, callerID, calleeID, calleeID), callstest.Options{})
	callstest.WaitFor(t, "the late joiner to hear", 10*time.Second, func() bool {
		return len(late.PeerStates()) == 0 // bob never sent a state: nothing to hear
	})
	late.Send(map[string]any{"type": "state", "microphone": true, "camera": true})
	callstest.WaitFor(t, "alice to hear bob", 10*time.Second, func() bool { return len(alice.PeerStates()) > 0 })
	third := r.dial("alice again", r.url(callID, callerID, calleeID, callerID), callstest.Options{})
	callstest.WaitFor(t, "the new socket of alice to learn bob's state", 10*time.Second, func() bool { return len(third.PeerStates()) > 0 })
}

func TestHangupFromTheSocketEndsTheCallForBoth(t *testing.T) {
	r := newRig(t, Config{})
	alice, bob := startCall(t, r, callstest.Options{}, callstest.Options{})
	alice.Receiving(t, 5, 15*time.Second)
	bob.Receiving(t, 5, 15*time.Second)
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
	r.ctl.mu.Lock()
	left := append([]uint64(nil), r.ctl.left...)
	r.ctl.mu.Unlock()
	if len(left) != 1 || left[0] != calleeID {
		t.Errorf("Leave was called for %v, want bob", left)
	}
	callstest.WaitFor(t, "the server to forget the call", 5*time.Second, func() bool { return r.srv.Calls() == 0 })
}

func TestTheEndOfTheCallFromTheServiceClosesTheSockets(t *testing.T) {
	r := newRig(t, Config{})
	alice, bob := startCall(t, r, callstest.Options{}, callstest.Options{})
	alice.Receiving(t, 5, 15*time.Second)
	bob.Receiving(t, 5, 15*time.Second)
	// The service ends it (a disabled user, a timeout, a session).
	r.ctl.end(callID, "disabled")
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
	callstest.WaitFor(t, "the server to forget the call", 5*time.Second, func() bool { return r.srv.Calls() == 0 })
	if n := len(r.ctl.failedCalls()); n != 0 {
		t.Errorf("the media server failed a call that the service had ended")
	}
}

func TestAnEndingSessionClosesTheSocket(t *testing.T) {
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
			r := newRig(t, Config{ReconnectGrace: 10 * time.Second})
			alice, bob := startCall(t, r, callstest.Options{}, callstest.Options{})
			alice.Receiving(t, 5, 15*time.Second)
			r.endSession(callerID, tt.code, tt.reason)
			// user.changed is what tells the server to look again.
			r.hub.Publish(live.Event{Topic: live.UserTopic(callerID), Kind: live.KindUserChanged, UserID: callerID})
			callstest.WaitFor(t, "alice's socket to close", 10*time.Second, func() bool { return alice.CloseCode() == int(tt.code) })
			// bob's goes on.
			bob.Receiving(t, 5, 15*time.Second)
			if bob.CloseCode() != 0 {
				t.Errorf("bob's socket closed with %d", bob.CloseCode())
			}
		})
	}
}

func TestAPingChecksTheSessionAgain(t *testing.T) {
	r := newRig(t, Config{PingInterval: 200 * time.Millisecond, PingTimeout: 2 * time.Second})
	alice, _ := startCall(t, r, callstest.Options{}, callstest.Options{})
	alice.Receiving(t, 5, 15*time.Second)
	r.endSession(callerID, CloseSessionEnded, "session ended")
	callstest.WaitFor(t, "alice's socket to close", 10*time.Second, func() bool { return alice.CloseCode() == int(CloseSessionEnded) })
}

func TestASocketOfACallThatIsOverIsToldSo(t *testing.T) {
	r := newRig(t, Config{})
	r.ctl.add(callID, callerID, calleeID, true)
	r.ctl.end(callID, "hangup")
	alice := r.dial("alice", r.url(callID, callerID, calleeID, callerID), callstest.Options{})
	select {
	case reason := <-alice.Ended():
		if reason != "hangup" {
			t.Errorf("reason = %q", reason)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("alice was not told that the call is over")
	}
	callstest.WaitFor(t, "the socket to close", 10*time.Second, func() bool { return alice.CloseCode() == int(websocket.StatusNormalClosure) })
}

func TestAStrangerCannotJoin(t *testing.T) {
	r := newRig(t, Config{})
	r.ctl.add(callID, callerID, calleeID, true)
	stranger := r.dial("stranger", r.url(callID, callerID, calleeID, 99), callstest.Options{})
	callstest.WaitFor(t, "the socket to close", 10*time.Second, func() bool { return stranger.CloseCode() != 0 })
	if errs := stranger.Errors(); len(errs) != 1 || errs[0].Code != CodeInternal {
		t.Errorf("errors = %+v, want one internal error", errs)
	}
	if stranger.CloseCode() != int(websocket.StatusInternalError) {
		t.Errorf("close code = %d", stranger.CloseCode())
	}
}

func TestAMessageThatIsNotOfTheProtocolIsAnswered(t *testing.T) {
	r := newRig(t, Config{})
	alice, bob := startCall(t, r, callstest.Options{}, callstest.Options{})
	bob.Receiving(t, 5, 15*time.Second)
	for _, m := range []string{`{"type":"nope"}`, `[1]`, `{"type":"answer"}`, `{"type":"state","microphone":true}`, `{"type":"hangup","extra":1}`} {
		alice.WriteRaw(websocket.MessageText, []byte(m))
	}
	callstest.WaitFor(t, "five errors", 10*time.Second, func() bool { return len(alice.Errors()) == 5 })
	for _, e := range alice.Errors() {
		if e.Code != CodeInvalidMessage {
			t.Errorf("error = %+v", e)
		}
	}
	// The socket stays and the call goes on.
	alice.Receiving(t, 10, 15*time.Second)
	// A binary message closes it.
	alice.WriteRaw(websocket.MessageBinary, []byte{1})
	callstest.WaitFor(t, "the socket to close", 10*time.Second, func() bool { return alice.CloseCode() == int(websocket.StatusUnsupportedData) })
}

func TestAnOfferWithICERestartKeepsTheMediaFlowing(t *testing.T) {
	r := newRig(t, Config{})
	alice, bob := startCall(t, r, callstest.Options{}, callstest.Options{})
	alice.Receiving(t, 10, 15*time.Second)
	bob.Receiving(t, 10, 15*time.Second)
	// The same path as a transport that failed: an offer that restarts ICE.
	c := func() *call {
		r.srv.mu.Lock()
		defer r.srv.mu.Unlock()
		return r.srv.calls[callID]
	}()
	done := make(chan error, 1)
	c.post(func() { done <- c.parts[0].cur.Load().sendOffer(true) })
	if err := <-done; err != nil {
		t.Fatalf("sendOffer: %+v", err)
	}
	callstest.WaitFor(t, "alice to answer the second offer", 10*time.Second, func() bool { return alice.Offers() == 2 })
	alice.Receiving(t, 25, 15*time.Second)
	bob.Receiving(t, 25, 15*time.Second)
}

func TestCloseLeavesNoGoroutine(t *testing.T) {
	settle := func() int {
		deadline := time.Now().Add(10 * time.Second)
		last := runtime.NumGoroutine()
		for time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
			n := runtime.NumGoroutine()
			if n == last {
				return n
			}
			last = n
		}
		return last
	}
	before := settle()
	func() {
		r := newRig(t, Config{})
		alice, bob := startCall(t, r, callstest.Options{}, callstest.Options{})
		alice.Receiving(t, 10, 15*time.Second)
		bob.Receiving(t, 10, 15*time.Second)
		// A call that is still running when the server closes.
		alice.Close()
		bob.Close()
		r.close()
	}()
	deadline := time.Now().Add(15 * time.Second)
	var after int
	for {
		after = runtime.NumGoroutine()
		if after <= before || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if after > before {
		buf := make([]byte, 1<<20)
		buf = buf[:runtime.Stack(buf, true)]
		t.Errorf("%d goroutines before, %d after Close\n%s", before, after, buf)
	}
}

func TestCloseWhileACallIsRunningTellsTheClients(t *testing.T) {
	r := newRig(t, Config{})
	alice, bob := startCall(t, r, callstest.Options{}, callstest.Options{})
	alice.Receiving(t, 5, 15*time.Second)
	bob.Receiving(t, 5, 15*time.Second)
	if err := r.srv.Close(); err != nil {
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
	if err := r.srv.Close(); err != nil {
		t.Errorf("the second Close: %+v", err)
	}
	// A socket after Close is refused.
	conn, _, err := websocket.Dial(t.Context(), r.url(callID, callerID, calleeID, callerID), nil)
	if err == nil {
		_ = conn.CloseNow()
	}
	if n := r.srv.Calls(); n != 0 {
		t.Errorf("%d calls after Close", n)
	}
}

func TestTheServerListensOnUDPAndTCP(t *testing.T) {
	r := newRig(t, Config{})
	udp, tcp := r.srv.Addr()
	if udp == nil || tcp == nil {
		t.Fatal("no address")
	}
	if _, port, _ := net.SplitHostPort(tcp.String()); port == "0" {
		t.Errorf("tcp = %v", tcp)
	}
	conn, err := net.DialTimeout("tcp", tcp.String(), 2*time.Second)
	if err != nil {
		t.Fatalf("the TCP port does not accept: %v", err)
	}
	_ = conn.Close()
	uaddr := udp.(*net.UDPAddr)
	taddr := tcp.(*net.TCPAddr)
	if uaddr.Port != taddr.Port {
		t.Errorf("UDP port %d and TCP port %d differ", uaddr.Port, taddr.Port)
	}
}

func TestPacketsOfAnotherSourceFollowOnWithoutAGap(t *testing.T) {
	mk := func(ssrc uint32, seq uint16, ts uint32) *rtp.Packet {
		return &rtp.Packet{Header: rtp.Header{SSRC: ssrc, SequenceNumber: seq, Timestamp: ts}}
	}
	type packet struct {
		name         string
		in           *rtp.Packet
		wantSwitched bool
		wantSeq      uint16
		wantTS       uint32
	}
	tests := []struct {
		name    string
		step    uint32
		packets []packet
	}{
		{"a source that is replaced by another", 3000, []packet{
			{"the first packet starts the stream", mk(5, 100, 9000), true, 100, 9000},
			{"the next packet of the source is untouched", mk(5, 101, 9003), false, 101, 9003},
			{"a source with other numbers follows on", mk(8, 60000, 5), true, 102, 9003 + 3000},
			{"and goes on", mk(8, 60001, 8), false, 103, 12006},
			{"a packet lost is not a new source", mk(8, 60003, 20), false, 105, 12018},
			{"a jump of the same source is a new source", mk(8, 5000, 90), true, 106, 12018 + 3000},
		}},
		{"the sequence wraps like any other", 960, []packet{
			{"before the wrap", mk(9, 65535, 100), true, 65535, 100},
			{"after the wrap", mk(9, 0, 1060), false, 0, 1060},
			{"and on", mk(9, 1, 2020), false, 1, 2020},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rw := newRewriter(tt.step)
			for _, p := range tt.packets {
				switched := rw.rewrite(p.in)
				if switched != p.wantSwitched || p.in.SequenceNumber != p.wantSeq || p.in.Timestamp != p.wantTS {
					t.Errorf("%s: got switched %v seq %d ts %d, want %v %d %d", p.name, switched, p.in.SequenceNumber, p.in.Timestamp, p.wantSwitched, p.wantSeq, p.wantTS)
				}
			}
		})
	}
}
