package calls

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/rm4n0s/clowder/internal/live"
)

// fuzzFixture is a media server and a call that is never started, so that a
// fuzz run can make the PeerConnections of its participants without a client
// and give them what a client would send. The functions that the call's
// goroutine would run are run by a goroutine of the fixture under mu, which
// the caller also holds while it uses a peer.
type fuzzFixture struct {
	srv  *Server
	call *call
	mu   sync.Mutex
}

// newFuzzFixture makes the fixture; it ends with the test.
func newFuzzFixture(tb testing.TB) *fuzzFixture {
	tb.Helper()
	hub := live.New()
	ctl := newFakeCtl(hub)
	ctl.add(1, 1, 2, true)
	cfg := Config{
		Addr: "127.0.0.1:0", IncludeLoopback: true, Logger: slog.New(slog.DiscardHandler),
		ICETimeout: time.Hour, DisconnectTimeout: time.Hour, JoinTimeout: time.Hour,
	}
	srv, err := New(tb.Context(), cfg, ctl, hub)
	if err != nil {
		tb.Fatalf("New: %+v", err)
	}
	fx := &fuzzFixture{srv: srv}
	fx.call = newCall(srv, Join{CallID: 1, ConversationID: 1, CallerID: 1, CalleeID: 2})
	for i := range fx.call.out {
		if fx.call.out[i], err = newOutTracks(); err != nil {
			tb.Fatal(err)
		}
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case f := <-fx.call.inbox:
				fx.mu.Lock()
				f()
				fx.mu.Unlock()
			case <-stop:
				return
			}
		}
	}()
	tb.Cleanup(func() {
		fx.call.cancel()
		close(stop)
		<-done
		if err := srv.Close(); err != nil {
			tb.Errorf("Close: %+v", err)
		}
		hub.Close()
	})
	return fx
}

// newPeer makes the peer of the first participant, with its offer made and
// set, and returns it with the recording socket that got the offer.
func (fx *fuzzFixture) newPeer(tb testing.TB) (*peer, *recordingSocket) {
	tb.Helper()
	sock := &recordingSocket{}
	fx.mu.Lock()
	defer fx.mu.Unlock()
	pe, err := newPeer(fx.call.parts[0], sock)
	if err != nil {
		tb.Fatalf("newPeer: %+v", err)
	}
	if err := pe.sendOffer(false); err != nil {
		pe.close()
		tb.Fatalf("sendOffer: %+v", err)
	}
	return pe, sock
}

// realNegotiation makes what a client would send: the offer of the server, the
// answer of a real pion client to it, and the candidates the client gathers.
func realNegotiation(tb testing.TB, fx *fuzzFixture) (offer, answer string, candidates []string) {
	tb.Helper()
	pe, sock := fx.newPeer(tb)
	defer func() {
		fx.mu.Lock()
		pe.close()
		fx.mu.Unlock()
	}()
	sock.mu.Lock()
	for _, m := range sock.sent {
		if m.Type == msgOffer {
			offer = m.SDP
		}
	}
	sock.mu.Unlock()
	if offer == "" {
		tb.Fatal("the server made no offer")
	}
	me := &webrtc.MediaEngine{}
	for _, c := range serverCodecs() {
		if err := me.RegisterCodec(c.params, c.kind); err != nil {
			tb.Fatal(err)
		}
	}
	client, err := webrtc.NewAPI(webrtc.WithMediaEngine(me)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		tb.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	var cmu sync.Mutex
	client.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			cmu.Lock()
			candidates = append(candidates, c.ToJSON().Candidate)
			cmu.Unlock()
		}
	})
	if err := client.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		tb.Fatal(err)
	}
	ans, err := client.CreateAnswer(nil)
	if err != nil {
		tb.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(client)
	if err := client.SetLocalDescription(ans); err != nil {
		tb.Fatal(err)
	}
	select {
	case <-gathered:
	case <-time.After(10 * time.Second):
	}
	cmu.Lock()
	defer cmu.Unlock()
	return offer, client.LocalDescription().SDP, append([]string(nil), candidates...)
}

// TestARealAnswerAndItsCandidatesAreTaken is the check of the seeds of
// FuzzSignalMessage: what a real client sends goes through the same code as
// the fuzz inputs, is accepted, and brings the PeerConnection to the state a
// negotiation reaches.
func TestARealAnswerAndItsCandidatesAreTaken(t *testing.T) {
	fx := newFuzzFixture(t)
	_, answer, candidates := realNegotiation(t, fx)
	pe, _ := fx.newPeer(t)
	fx.mu.Lock()
	defer fx.mu.Unlock()
	// The answer of another negotiation has other credentials but is a good
	// answer for the shape of the offer; pion takes the answer for this offer
	// as long as the media sections agree.
	pe.onAnswer(answer)
	if !pe.answered || !pe.remoteSet {
		t.Fatalf("the answer of a real client was not taken (answered %v, remote set %v)", pe.answered, pe.remoteSet)
	}
	if len(candidates) == 0 {
		t.Fatal("the client gathered no candidate")
	}
	for _, c := range candidates {
		pe.onCandidate(clientMessage{Type: msgCandidate, Candidate: c})
	}
	pe.close()
}

// FuzzSignalMessage throws bytes at the messages of the signalling socket as
// the server reads them (step 24.1, SPEC D64): the JSON of the client, the
// SDP of an answer (the parser of the SDP, then pion's
// SetRemoteDescription on a real PeerConnection that has made its offer) and
// an ICE candidate (AddICECandidate). It must never panic, hang or let a
// message past a limit; a message that is refused says nothing of its text.
// The seeds are the messages of a real negotiation (the offer of the server,
// the answer of a pion client to it and its candidates, each as the client
// would send it), the messages of every type and malformed ones; more are in
// testdata/fuzz/FuzzSignalMessage.
func FuzzSignalMessage(f *testing.F) {
	fx := newFuzzFixture(f)
	offer, answer, candidates := realNegotiation(f, fx)
	msg := func(v map[string]any) string {
		b, err := json.Marshal(v)
		if err != nil {
			f.Fatal(err)
		}
		return string(b)
	}
	seeds := []string{
		msg(map[string]any{"type": "answer", "sdp": answer}),
		msg(map[string]any{"type": "answer", "sdp": offer}),
		msg(map[string]any{"type": "answer", "sdp": strings.Replace(answer, "a=setup:active", "a=setup:passive", 1)}),
		msg(map[string]any{"type": "answer", "sdp": strings.ReplaceAll(answer, "\r\n", "\n")}),
		msg(map[string]any{"type": "answer", "sdp": answer[:len(answer)/2]}),
		msg(map[string]any{"type": "answer", "sdp": "v=0\r\n"}),
		msg(map[string]any{"type": "answer", "sdp": ""}),
		msg(map[string]any{"type": "state", "microphone": true, "camera": false}),
		msg(map[string]any{"type": "state", "microphone": true}),
		msg(map[string]any{"type": "hangup"}),
		msg(map[string]any{"type": "hangup", "extra": 1}),
		msg(map[string]any{"type": "offer", "sdp": offer}),
		msg(map[string]any{"type": "candidate", "candidate": "", "sdpMid": "0", "sdpMLineIndex": 0}),
		msg(map[string]any{"type": "candidate", "candidate": "candidate:1 1 tcp 1518280447 192.0.2.7 9 typ host tcptype active", "sdpMid": "0", "sdpMLineIndex": 0}),
		msg(map[string]any{"type": "candidate", "candidate": "candidate:2 1 udp 2113937151 abc.local 54321 typ host", "sdpMid": "1"}),
		msg(map[string]any{"type": "candidate", "candidate": "candidate:3 1 udp 1 ::1 1 typ srflx raddr :: rport 0", "sdpMLineIndex": 65535}),
		msg(map[string]any{"type": "candidate", "candidate": "candidate:4 99999999999 udp x y z typ"}),
		msg(map[string]any{"type": "candidate", "candidate": strings.Repeat("a", 3000)}),
		`{"type":"answer","sdp":"x"}{"type":"hangup"}`,
		`{"type":"candidate","candidate":"a","sdpMLineIndex":-1}`,
		`{"type":"candidate","candidate":"a","sdpMLineIndex":70000}`,
		`{"type":"state","microphone":"yes","camera":1}`,
		`[{"type":"hangup"}]`, `null`, `"hangup"`, `1`, `{`, ``, "\x00", `{"type":"hangup"} x`,
		`{"type":"hangup","type":"state"}`, `{"Type":"HANGUP"}`, `{"type":"answer","sdp":"\u0000"}`,
	}
	for _, c := range candidates {
		seeds = append(seeds, msg(map[string]any{"type": "candidate", "candidate": c, "sdpMid": "0", "sdpMLineIndex": 0}))
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		start := time.Now()
		m, err := parseClientMessage(data)
		if err != nil {
			if len(data) > 8 && strings.Contains(err.Error(), string(data)) {
				t.Fatalf("the error repeats the text: %v", err)
			}
			return
		}
		switch m.Type {
		case msgAnswer:
			if len(m.SDP) > maxSDP || m.SDP == "" {
				t.Fatalf("an answer past its limits was taken: %d bytes", len(m.SDP))
			}
		case msgCandidate:
			if len(m.Candidate) > 2048 || m.Candidate == "" {
				t.Fatalf("a candidate past its limits was taken: %d bytes", len(m.Candidate))
			}
		case msgState:
			if m.Microphone == nil || m.Camera == nil {
				t.Fatal("a state without its fields was taken")
			}
		case msgHangup:
		default:
			t.Fatalf("a message of type %q was taken", m.Type)
		}
		if m.Type == msgAnswer || m.Type == msgCandidate {
			// The parser of the SDP that adds the limit of video, which also
			// runs on the offers the server makes.
			_, _ = limitVideoBandwidth(m.SDP, DefaultMaxVideoBitrate)
			pe, _ := fx.newPeer(t)
			fx.mu.Lock()
			if m.Type == msgAnswer {
				pe.onAnswer(m.SDP)
			} else {
				pe.onCandidate(m)
				// A candidate that waited is given to the PeerConnection too.
				pe.remoteSet = true
				for _, c := range pe.pending {
					pe.addCandidate(c)
				}
			}
			pe.close()
			fx.mu.Unlock()
		}
		if took := time.Since(start); took > 5*time.Second {
			t.Fatalf("a message took %v", took)
		}
	})
}
