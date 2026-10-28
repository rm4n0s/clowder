package calls

import (
	"context"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/rm4n0s/clowder/internal/live"
)

const (
	callerID = 10
	calleeID = 20
	callID   = 1
)

// startCall puts an active call in the rig and joins both clients.
func startCall(t *testing.T, r *rig, a, b clientOptions) (alice, bob *client) {
	t.Helper()
	r.ctl.add(callID, callerID, calleeID, true)
	alice = r.dial("alice", r.url(callID, callerID, calleeID, callerID), a)
	bob = r.dial("bob", r.url(callID, callerID, calleeID, calleeID), b)
	return alice, bob
}

func TestTwoClientsReceiveEachOthersMedia(t *testing.T) {
	r := newRig(t, Config{})
	alice, bob := startCall(t, r, clientOptions{}, clientOptions{})
	alice.receiving(t, 25, 15*time.Second)
	bob.receiving(t, 25, 15*time.Second)
	t.Logf("alice received audio %d video %d; bob received audio %d video %d",
		alice.audioIn.Load(), alice.videoIn.Load(), bob.audioIn.Load(), bob.videoIn.Load())

	// Both PeerConnections got the video limit in the offer (SPEC D64).
	for _, c := range []*client{alice, bob} {
		offer, _ := c.lastOffer.Load().(string)
		if !strings.Contains(offer, "b=AS:2500\r\n") || !strings.Contains(offer, "b=TIAS:2500000\r\n") {
			t.Errorf("%s: the offer has no bandwidth lines:\n%s", c.name, offer)
		}
		if audio := offer[:strings.Index(offer, "m=video")]; strings.Contains(audio, "b=AS") {
			t.Errorf("%s: the audio section has a bandwidth line", c.name)
		}
		if strings.Contains(offer, "H264") {
			t.Errorf("%s: the offer has H.264", c.name)
		}
		if !strings.Contains(offer, "opus/48000/2") || !strings.Contains(offer, "VP8/90000") {
			t.Errorf("%s: the offer lacks Opus or VP8", c.name)
		}
	}
	// And REMB says it on the wire.
	waitFor(t, "REMB", 10*time.Second, func() bool {
		alice.mu.Lock()
		defer alice.mu.Unlock()
		return len(alice.rembs) > 0
	})
	alice.mu.Lock()
	if got := alice.rembs[0]; got < 2.4e6 || got > 2.6e6 {
		t.Errorf("REMB = %v, want about 2.5 Mbit/s", got)
	}
	alice.mu.Unlock()

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

// videoSender returns the RTPSender of the client's video.
func videoSender(t *testing.T, c *client) *webrtc.RTPSender {
	t.Helper()
	for _, s := range c.pc.GetSenders() {
		if s.Track() != nil && s.Track().Kind() == webrtc.RTPCodecTypeVideo {
			return s
		}
	}
	t.Fatalf("%s has no video sender", c.name)
	return nil
}

func TestScreenShareKeepsTheOtherSideReceiving(t *testing.T) {
	tests := []struct {
		name string
		// replace puts a screen in place of the camera on the transceiver.
		replace func(t *testing.T, c *client)
	}{
		{"a track that goes on numbering the packets, as a browser does", func(t *testing.T, c *client) {
			screen, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}, "screen", "alice")
			if err != nil {
				t.Fatal(err)
			}
			if err := videoSender(t, c).ReplaceTrack(screen); err != nil {
				t.Fatalf("ReplaceTrack: %v", err)
			}
			c.video.Store(screen)
		}},
		{"a track with sequence numbers and timestamps of its own", func(t *testing.T, c *client) {
			screen, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}, "screen", "alice")
			if err != nil {
				t.Fatal(err)
			}
			if err := videoSender(t, c).ReplaceTrack(screen); err != nil {
				t.Fatalf("ReplaceTrack: %v", err)
			}
			c.videoSample.Store(screen)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, Config{})
			alice, bob := startCall(t, r, clientOptions{}, clientOptions{})
			bob.receiving(t, 20, 15*time.Second)
			offers := alice.offers.Load()
			tt.replace(t, alice)
			before := bob.videoIn.Load()
			waitFor(t, "bob to keep receiving video", 10*time.Second, func() bool { return bob.videoIn.Load()-before >= 40 })
			if alice.offers.Load() != offers {
				t.Errorf("the screen share caused %d new offers, want none", alice.offers.Load()-offers)
			}
			// The packets of both sources leave the server on one SSRC.
			bob.mu.Lock()
			defer bob.mu.Unlock()
			if len(bob.videoSSRCs) != 1 {
				t.Errorf("bob saw the video on %d SSRCs, want 1: %v", len(bob.videoSSRCs), bob.videoSSRCs)
			}
		})
	}
}

func TestKeyFrameRequestsGoToTheSender(t *testing.T) {
	r := newRig(t, Config{})
	alice, bob := startCall(t, r, clientOptions{}, clientOptions{})
	alice.receiving(t, 10, 15*time.Second)
	bob.receiving(t, 10, 15*time.Second)
	// The server asked both senders for a key frame when the connections came up.
	waitFor(t, "the key frame request of the connection", 10*time.Second, func() bool { return alice.plis.Load() > 0 && bob.plis.Load() > 0 })
	// bob's own request for a key frame goes to alice.
	before := alice.plis.Load()
	var ssrc uint32
	bob.mu.Lock()
	for s := range bob.videoSSRCs {
		ssrc = s
	}
	bob.mu.Unlock()
	time.Sleep(400 * time.Millisecond) // past the gap between two requests
	if err := bob.pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: ssrc}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "alice to get bob's PLI", 10*time.Second, func() bool { return alice.plis.Load() > before })
}

func TestAudioOnlyClientsKeepTheCallUp(t *testing.T) {
	r := newRig(t, Config{})
	alice, bob := startCall(t, r, clientOptions{noVideo: true}, clientOptions{noVideo: true})
	a0, b0 := alice.audioIn.Load(), bob.audioIn.Load()
	waitFor(t, "audio both ways", 15*time.Second, func() bool {
		return alice.audioIn.Load()-a0 >= 25 && bob.audioIn.Load()-b0 >= 25
	})
	if alice.videoIn.Load() != 0 || bob.videoIn.Load() != 0 {
		t.Errorf("video packets in an audio call: %d %d", alice.videoIn.Load(), bob.videoIn.Load())
	}
}

func TestICETCPAlone(t *testing.T) {
	r := newRig(t, Config{})
	tcp := []webrtc.NetworkType{webrtc.NetworkTypeTCP4}
	alice, bob := startCall(t, r, clientOptions{networks: tcp}, clientOptions{networks: tcp})
	alice.receiving(t, 20, 20*time.Second)
	bob.receiving(t, 20, 20*time.Second)
}

func TestICEThatNeverConnectsEndsTheCall(t *testing.T) {
	r := newRig(t, Config{ICETimeout: 400 * time.Millisecond})
	r.ctl.add(callID, callerID, calleeID, true)
	alice := r.dial("alice", r.url(callID, callerID, calleeID, callerID), clientOptions{noCandidates: true})
	started := time.Now()
	waitFor(t, "the call to fail", 10*time.Second, func() bool { return len(r.ctl.failedCalls()) == 1 })
	if took := time.Since(started); took < 300*time.Millisecond {
		t.Errorf("the call failed after %v, before the deadline", took)
	}
	select {
	case reason := <-alice.ended:
		if reason != "failed" {
			t.Errorf("ended reason = %q, want failed", reason)
		}
	case <-time.After(5 * time.Second):
		t.Error("alice was not told that the call ended")
	}
	waitFor(t, "the socket to close", 5*time.Second, func() bool { return alice.closeCode.Load() == int32(websocket.StatusNormalClosure) })
	waitFor(t, "the server to forget the call", 5*time.Second, func() bool { return r.srv.Calls() == 0 })
}

func TestAParticipantWhoNeverJoinsFailsTheCall(t *testing.T) {
	r := newRig(t, Config{JoinTimeout: 400 * time.Millisecond})
	r.ctl.add(callID, callerID, calleeID, true)
	alice := r.dial("alice", r.url(callID, callerID, calleeID, callerID), clientOptions{})
	waitFor(t, "the call to fail", 10*time.Second, func() bool { return len(r.ctl.failedCalls()) == 1 })
	if reason := <-alice.ended; reason != "failed" {
		t.Errorf("reason = %q", reason)
	}
}

func TestAJoinTimerStartsWhenTheCallIsAnswered(t *testing.T) {
	r := newRig(t, Config{JoinTimeout: 500 * time.Millisecond, ICETimeout: 30 * time.Second})
	// The call rings: nobody is waited for.
	r.ctl.add(callID, callerID, calleeID, false)
	alice := r.dial("alice", r.url(callID, callerID, calleeID, callerID), clientOptions{})
	waitFor(t, "alice to connect", 10*time.Second, func() bool {
		select {
		case <-alice.connected:
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
	waitFor(t, "the call to fail", 10*time.Second, func() bool { return len(r.ctl.failedCalls()) == 1 })
}

func TestAClosedSocketGetsTheReconnectGrace(t *testing.T) {
	t.Run("not opened again", func(t *testing.T) {
		r := newRig(t, Config{ReconnectGrace: 400 * time.Millisecond})
		alice, bob := startCall(t, r, clientOptions{}, clientOptions{})
		alice.receiving(t, 10, 15*time.Second)
		bob.receiving(t, 10, 15*time.Second)
		started := time.Now()
		alice.close()
		waitFor(t, "the call to fail", 10*time.Second, func() bool { return len(r.ctl.failedCalls()) == 1 })
		if took := time.Since(started); took < 300*time.Millisecond {
			t.Errorf("failed after %v, before the grace", took)
		}
		if reason := <-bob.ended; reason != "failed" {
			t.Errorf("bob was told %q", reason)
		}
	})
	t.Run("opened again", func(t *testing.T) {
		r := newRig(t, Config{ReconnectGrace: 5 * time.Second})
		alice, bob := startCall(t, r, clientOptions{}, clientOptions{})
		alice.receiving(t, 10, 15*time.Second)
		bob.receiving(t, 10, 15*time.Second)
		alice.close()
		time.Sleep(300 * time.Millisecond)
		again := r.dial("alice again", r.url(callID, callerID, calleeID, callerID), clientOptions{})
		again.receiving(t, 25, 15*time.Second)
		bob.receiving(t, 25, 15*time.Second)
		time.Sleep(500 * time.Millisecond)
		if n := len(r.ctl.failedCalls()); n != 0 {
			t.Errorf("the call failed although alice came back")
		}
	})
}

func TestASecondSocketReplacesTheFirst(t *testing.T) {
	r := newRig(t, Config{})
	alice, bob := startCall(t, r, clientOptions{}, clientOptions{})
	alice.receiving(t, 10, 15*time.Second)
	second := r.dial("alice's other tab", r.url(callID, callerID, calleeID, callerID), clientOptions{})
	waitFor(t, "the first socket to close", 10*time.Second, func() bool { return alice.closeCode.Load() == int32(closeReplaced) })
	second.receiving(t, 20, 15*time.Second)
	bob.receiving(t, 20, 15*time.Second)
	if n := r.srv.Calls(); n != 1 {
		t.Errorf("%d calls, want 1", n)
	}
}

func TestTheStateOfTheMicrophoneAndTheCameraReachesTheOtherSide(t *testing.T) {
	r := newRig(t, Config{})
	alice, bob := startCall(t, r, clientOptions{}, clientOptions{})
	bob.receiving(t, 5, 15*time.Second)
	alice.send(map[string]any{"type": "state", "microphone": false, "camera": true})
	waitFor(t, "bob to hear", 10*time.Second, func() bool {
		bob.mu.Lock()
		defer bob.mu.Unlock()
		return len(bob.peerStates) > 0
	})
	bob.mu.Lock()
	got := bob.peerStates[len(bob.peerStates)-1]
	bob.mu.Unlock()
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
	late := r.dial("bob again", r.url(callID, callerID, calleeID, calleeID), clientOptions{})
	waitFor(t, "the late joiner to hear", 10*time.Second, func() bool {
		late.mu.Lock()
		defer late.mu.Unlock()
		return len(late.peerStates) == 0 // bob never sent a state: nothing to hear
	})
	late.send(map[string]any{"type": "state", "microphone": true, "camera": true})
	waitFor(t, "alice to hear bob", 10*time.Second, func() bool {
		alice.mu.Lock()
		defer alice.mu.Unlock()
		return len(alice.peerStates) > 0
	})
	third := r.dial("alice again", r.url(callID, callerID, calleeID, callerID), clientOptions{})
	waitFor(t, "the new socket of alice to learn bob's state", 10*time.Second, func() bool {
		third.mu.Lock()
		defer third.mu.Unlock()
		return len(third.peerStates) > 0
	})
}

func TestHangupFromTheSocketEndsTheCallForBoth(t *testing.T) {
	r := newRig(t, Config{})
	alice, bob := startCall(t, r, clientOptions{}, clientOptions{})
	alice.receiving(t, 5, 15*time.Second)
	bob.receiving(t, 5, 15*time.Second)
	bob.send(map[string]any{"type": "hangup"})
	for _, c := range []*client{alice, bob} {
		select {
		case reason := <-c.ended:
			if reason != "hangup" {
				t.Errorf("%s was told %q", c.name, reason)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s was not told that the call ended", c.name)
		}
		waitFor(t, c.name+"'s socket to close", 10*time.Second, func() bool { return c.closeCode.Load() == int32(websocket.StatusNormalClosure) })
	}
	r.ctl.mu.Lock()
	left := append([]uint64(nil), r.ctl.left...)
	r.ctl.mu.Unlock()
	if len(left) != 1 || left[0] != calleeID {
		t.Errorf("Leave was called for %v, want bob", left)
	}
	waitFor(t, "the server to forget the call", 5*time.Second, func() bool { return r.srv.Calls() == 0 })
}

func TestTheEndOfTheCallFromTheServiceClosesTheSockets(t *testing.T) {
	r := newRig(t, Config{})
	alice, bob := startCall(t, r, clientOptions{}, clientOptions{})
	alice.receiving(t, 5, 15*time.Second)
	bob.receiving(t, 5, 15*time.Second)
	// The service ends it (a disabled user, a timeout, a session).
	r.ctl.end(callID, "disabled")
	for _, c := range []*client{alice, bob} {
		select {
		case reason := <-c.ended:
			if reason != "disabled" {
				t.Errorf("%s was told %q", c.name, reason)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s was not told that the call ended", c.name)
		}
	}
	waitFor(t, "the server to forget the call", 5*time.Second, func() bool { return r.srv.Calls() == 0 })
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
			alice, bob := startCall(t, r, clientOptions{}, clientOptions{})
			alice.receiving(t, 5, 15*time.Second)
			r.endSession(callerID, tt.code, tt.reason)
			// user.changed is what tells the server to look again.
			r.hub.Publish(live.Event{Topic: live.UserTopic(callerID), Kind: live.KindUserChanged, UserID: callerID})
			waitFor(t, "alice's socket to close", 10*time.Second, func() bool { return alice.closeCode.Load() == int32(tt.code) })
			// bob's goes on.
			bob.receiving(t, 5, 15*time.Second)
			if bob.closeCode.Load() != 0 {
				t.Errorf("bob's socket closed with %d", bob.closeCode.Load())
			}
		})
	}
}

func TestAPingChecksTheSessionAgain(t *testing.T) {
	r := newRig(t, Config{PingInterval: 200 * time.Millisecond, PingTimeout: 2 * time.Second})
	alice, _ := startCall(t, r, clientOptions{}, clientOptions{})
	alice.receiving(t, 5, 15*time.Second)
	r.endSession(callerID, CloseSessionEnded, "session ended")
	waitFor(t, "alice's socket to close", 10*time.Second, func() bool { return alice.closeCode.Load() == int32(CloseSessionEnded) })
}

func TestASocketOfACallThatIsOverIsToldSo(t *testing.T) {
	r := newRig(t, Config{})
	r.ctl.add(callID, callerID, calleeID, true)
	r.ctl.end(callID, "hangup")
	alice := r.dial("alice", r.url(callID, callerID, calleeID, callerID), clientOptions{})
	select {
	case reason := <-alice.ended:
		if reason != "hangup" {
			t.Errorf("reason = %q", reason)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("alice was not told that the call is over")
	}
	waitFor(t, "the socket to close", 10*time.Second, func() bool { return alice.closeCode.Load() == int32(websocket.StatusNormalClosure) })
}

func TestAStrangerCannotJoin(t *testing.T) {
	r := newRig(t, Config{})
	r.ctl.add(callID, callerID, calleeID, true)
	stranger := r.dial("stranger", r.url(callID, callerID, calleeID, 99), clientOptions{})
	waitFor(t, "the socket to close", 10*time.Second, func() bool { return stranger.closeCode.Load() != 0 })
	stranger.mu.Lock()
	defer stranger.mu.Unlock()
	if len(stranger.errs) != 1 || stranger.errs[0].Code != CodeInternal {
		t.Errorf("errors = %+v, want one internal error", stranger.errs)
	}
	if stranger.closeCode.Load() != int32(websocket.StatusInternalError) {
		t.Errorf("close code = %d", stranger.closeCode.Load())
	}
}

func TestAMessageThatIsNotOfTheProtocolIsAnswered(t *testing.T) {
	r := newRig(t, Config{})
	alice, bob := startCall(t, r, clientOptions{}, clientOptions{})
	bob.receiving(t, 5, 15*time.Second)
	for _, m := range []string{`{"type":"nope"}`, `[1]`, `{"type":"answer"}`, `{"type":"state","microphone":true}`, `{"type":"hangup","extra":1}`} {
		if err := alice.ws.Write(alice.ctx, websocket.MessageText, []byte(m)); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "five errors", 10*time.Second, func() bool {
		alice.mu.Lock()
		defer alice.mu.Unlock()
		return len(alice.errs) == 5
	})
	alice.mu.Lock()
	for _, e := range alice.errs {
		if e.Code != CodeInvalidMessage {
			t.Errorf("error = %+v", e)
		}
	}
	alice.mu.Unlock()
	// The socket stays and the call goes on.
	alice.receiving(t, 10, 15*time.Second)
	// A binary message closes it.
	if err := alice.ws.Write(alice.ctx, websocket.MessageBinary, []byte{1}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the socket to close", 10*time.Second, func() bool { return alice.closeCode.Load() == int32(websocket.StatusUnsupportedData) })
}

func TestAnOfferWithICERestartKeepsTheMediaFlowing(t *testing.T) {
	r := newRig(t, Config{})
	alice, bob := startCall(t, r, clientOptions{}, clientOptions{})
	alice.receiving(t, 10, 15*time.Second)
	bob.receiving(t, 10, 15*time.Second)
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
	waitFor(t, "alice to answer the second offer", 10*time.Second, func() bool { return alice.offers.Load() == 2 })
	alice.receiving(t, 25, 15*time.Second)
	bob.receiving(t, 25, 15*time.Second)
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
		alice, bob := startCall(t, r, clientOptions{}, clientOptions{})
		alice.receiving(t, 10, 15*time.Second)
		bob.receiving(t, 10, 15*time.Second)
		// A call that is still running when the server closes.
		alice.close()
		bob.close()
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
	alice, bob := startCall(t, r, clientOptions{}, clientOptions{})
	alice.receiving(t, 5, 15*time.Second)
	bob.receiving(t, 5, 15*time.Second)
	if err := r.srv.Close(); err != nil {
		t.Fatalf("Close: %+v", err)
	}
	for _, c := range []*client{alice, bob} {
		select {
		case reason := <-c.ended:
			if reason != "server_stopped" {
				t.Errorf("%s was told %q", c.name, reason)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s was not told that the server stopped", c.name)
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

var (
	_ = context.Background
	_ = media.Sample{}
)
