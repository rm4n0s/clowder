package calls

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/rm4n0s/clowder/internal/calls/callstest"
)

// recordingSocket is a signalSocket that keeps what it is sent.
type recordingSocket struct {
	mu   sync.Mutex
	sent []serverMessage
}

func (*recordingSocket) uid() uint64                         { return 1 }
func (*recordingSocket) finish(websocket.StatusCode, string) {}
func (*recordingSocket) checkSession(context.Context)        {}
func (r *recordingSocket) send(m serverMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, m)
}

// TestCandidatesAreCapped: a client that sends thousands of candidates before
// its answer does not make the server keep them (step 24.1).
func TestCandidatesAreCapped(t *testing.T) {
	t.Parallel()
	sock := &recordingSocket{}
	pe := &peer{sock: sock}
	for i := range 5000 {
		pe.onCandidate(clientMessage{Type: msgCandidate, Candidate: fmt.Sprintf("candidate:%d 1 udp 2113937151 192.0.2.1 %d typ host", i, 10000+i%50000)})
	}
	if len(pe.pending) != maxRemoteCandidates {
		t.Errorf("the server keeps %d candidates, want %d", len(pe.pending), maxRemoteCandidates)
	}
	if len(sock.sent) != 1 || sock.sent[0].Type != msgError || sock.sent[0].Code != CodeInvalidMessage {
		t.Errorf("the client was told %+v, want one error", sock.sent)
	}
}

func TestMessageBucket(t *testing.T) {
	t.Parallel()
	start := time.Unix(1_700_000_000, 0)
	b := newMessageBucket(start)
	for i := range messageBurst {
		if !b.allow(start) {
			t.Fatalf("message %d of the burst refused", i+1)
		}
	}
	if b.allow(start) {
		t.Error("a message past the burst was allowed")
	}
	// A second gives messageRate more, and no more.
	later := start.Add(time.Second)
	allowed := 0
	for range 3 * messageRate {
		if b.allow(later) {
			allowed++
		}
	}
	if allowed != messageRate {
		t.Errorf("a second gave %d messages, want %d", allowed, messageRate)
	}
	// A long pause refills to the burst and not beyond.
	much := later.Add(time.Hour)
	allowed = 0
	for range 3 * messageBurst {
		if b.allow(much) {
			allowed++
		}
	}
	if allowed != messageBurst {
		t.Errorf("an hour gave %d messages, want %d", allowed, messageBurst)
	}
}

// TestAFloodOfMessagesClosesTheSocket: a participant who writes messages as
// fast as they can is closed with 1008 after the burst, and the server did
// not act on every message (step 24.1).
func TestAFloodOfMessagesClosesTheSocket(t *testing.T) {
	r := newRig(t, Config{})
	alice, bob := startCall(t, r, callstest.Options{}, callstest.Options{})
	bob.Receiving(t, 5, 15*time.Second)
	const flood = 300
	for range flood {
		alice.WriteRaw(websocket.MessageText, []byte(`{"type":"state","microphone":true,"camera":false}`))
	}
	callstest.WaitFor(t, "the socket to close", 10*time.Second, func() bool { return alice.CloseCode() == int(websocket.StatusPolicyViolation) })
	r.ctl.mu.Lock()
	n := len(r.ctl.media)
	r.ctl.mu.Unlock()
	if n > messageBurst+messageRate {
		t.Errorf("the server acted on %d of %d messages, want at most %d", n, flood, messageBurst+messageRate)
	}
}

// TestAHonestStartIsNotLimited: the answer and a few dozen candidates are far
// under the limit.
func TestAHonestStartIsNotLimited(t *testing.T) {
	r := newRig(t, Config{})
	alice, bob := startCall(t, r, callstest.Options{}, callstest.Options{})
	for _, c := range []*callstest.Client{alice, bob} {
		c.Receiving(t, 10, 15*time.Second)
		if c.CloseCode() != 0 {
			t.Errorf("%s was closed with %d", c.Name(), c.CloseCode())
		}
	}
}

// stunBindingRequest is a STUN Binding request with the USERNAME given.
func stunBindingRequest(username string) []byte {
	attr := []byte(username)
	pad := (4 - len(attr)%4) % 4
	msg := make([]byte, 20, 20+4+len(attr)+pad)
	binary.BigEndian.PutUint16(msg[0:], 0x0001) // Binding request
	binary.BigEndian.PutUint16(msg[2:], uint16(4+len(attr)+pad))
	binary.BigEndian.PutUint32(msg[4:], 0x2112A442) // magic cookie
	copy(msg[8:20], "0123456789ab")
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint16(hdr[0:], 0x0006) // USERNAME
	binary.BigEndian.PutUint16(hdr[2:], uint16(len(attr)))
	msg = append(msg, hdr...)
	msg = append(msg, attr...)
	msg = append(msg, make([]byte, pad)...)
	return msg
}

// TestTheMediaPortAnswersNothingToStrangers: the UDP port answers no STUN
// binding request that does not carry the ICE credentials of a PeerConnection
// the server made, and no other bytes either, so it cannot be used to
// amplify traffic or to learn anything; the TCP port ends a connection that
// does not start with such a request and says nothing to one that does. A
// request is smaller than any answer would be, so "no answer" is also "no more
// out than in" (step 24.1, SPEC D64).
func TestTheMediaPortAnswersNothingToStrangers(t *testing.T) {
	r := newRig(t, Config{})
	udpAddr, tcpAddr := r.srv.Addr()

	randomBytes := make([]byte, 1200)
	for i := range randomBytes {
		randomBytes[i] = byte(i*131 + 7)
	}
	// Looks like DTLS, RTP, RTCP and a STUN message with a broken length.
	dtls := append([]byte{22, 0xfe, 0xfd, 0, 0, 0, 0, 0, 0, 0, 0, 0, 30}, make([]byte, 30)...)
	rtp := append([]byte{0x80, 96, 0, 1, 0, 0, 0, 1, 0, 0, 0, 2}, make([]byte, 100)...)
	rtcp := append([]byte{0x80, 201, 0, 1, 0, 0, 0, 1}, make([]byte, 8)...)
	brokenSTUN := stunBindingRequest("a:b")
	binary.BigEndian.PutUint16(brokenSTUN[2:], 9999)
	packets := map[string][]byte{
		"a binding request, unknown ufrag":      stunBindingRequest("unknownufrag:otherufrag"),
		"a binding request, an ufrag of 1 byte": stunBindingRequest("x:y"),
		"a binding request, no colon":           stunBindingRequest("nocolon"),
		"a binding request, a long username":    stunBindingRequest(strings.Repeat("u", 400) + ":" + strings.Repeat("v", 400)),
		"a binding request, no username":        stunBindingRequest("")[:20],
		"a STUN message with a broken length":   brokenSTUN,
		"random bytes":                          randomBytes,
		"DTLS":                                  dtls,
		"RTP":                                   rtp,
		"RTCP":                                  rtcp,
		"one byte":                              {0},
		"a datagram of 64000 bytes":             make([]byte, 64000),
	}

	t.Run("UDP", func(t *testing.T) {
		conn, err := net.DialUDP("udp", nil, udpAddr.(*net.UDPAddr))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		for name, p := range packets {
			if _, err := conn.Write(p); err != nil {
				t.Logf("%s: not sent: %v", name, err)
				continue
			}
		}
		// An empty datagram too.
		if _, err := conn.Write(nil); err != nil {
			t.Logf("empty datagram: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(700 * time.Millisecond))
		buf := make([]byte, 2048)
		if n, err := conn.Read(buf); err == nil {
			t.Errorf("the media port answered with %d bytes: %x", n, buf[:min(n, 64)])
		}
	})

	t.Run("TCP", func(t *testing.T) {
		framed := func(p []byte) []byte {
			out := make([]byte, 2, 2+len(p))
			binary.BigEndian.PutUint16(out, uint16(len(p)))
			return append(out, p...)
		}
		for name, p := range packets {
			if len(p) > 0xffff {
				continue
			}
			for _, wire := range []struct {
				name string
				data []byte
			}{{"framed", framed(p)}, {"raw", p}} {
				conn, err := net.DialTimeout("tcp", tcpAddr.String(), 2*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = conn.Write(wire.data)
				_ = conn.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
				buf := make([]byte, 2048)
				n, err := conn.Read(buf)
				_ = conn.Close()
				if n > 0 {
					t.Errorf("%s (%s): the media port answered with %d bytes: %x", name, wire.name, n, buf[:min(n, 64)])
				}
				_ = err // a close or a timeout, both are silence
			}
		}
		// A connection that sends nothing is closed within the deadline of the
		// mux, and is never answered.
		conn, err := net.DialTimeout("tcp", tcpAddr.String(), 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		if n, err := io.ReadFull(conn, make([]byte, 1)); n != 0 {
			t.Errorf("a silent connection was answered: %v", err)
		}
	})

	// The server is still up and takes calls.
	r.ctl.add(callID, callerID, calleeID, true)
	alice := r.dial("alice", r.url(callID, callerID, calleeID, callerID), callstest.Options{})
	callstest.WaitFor(t, "an offer", 10*time.Second, func() bool { return alice.Offers() > 0 })
}

// TestATransportThatFailsEndsTheCallAfterTheDeadline: a participant whose
// connection is gone while the signalling socket stays open ends the call as
// failed after DisconnectTimeout (SPEC D64: 15 seconds, short here), not
// before and not never.
func TestATransportThatFailsEndsTheCallAfterTheDeadline(t *testing.T) {
	r := newRig(t, Config{DisconnectTimeout: 1500 * time.Millisecond})
	alice, bob := startCall(t, r, callstest.Options{}, callstest.Options{})
	alice.Receiving(t, 10, 15*time.Second)
	bob.Receiving(t, 10, 15*time.Second)
	if n := len(r.ctl.failedCalls()); n != 0 {
		t.Fatalf("the call failed with both connected (%d)", n)
	}
	// Alice's PeerConnection ends, and her socket stays.
	if err := alice.PC().Close(); err != nil {
		t.Fatalf("closing the client's connection: %v", err)
	}
	closed := time.Now()
	callstest.WaitFor(t, "the call to fail", 20*time.Second, func() bool { return len(r.ctl.failedCalls()) == 1 })
	if took := time.Since(closed); took < time.Second {
		t.Errorf("the call failed %v after the connection was lost, before the deadline", took)
	}
	select {
	case reason := <-bob.Ended():
		if reason != "failed" {
			t.Errorf("bob was told %q, want failed", reason)
		}
	case <-time.After(5 * time.Second):
		t.Error("bob was not told that the call ended")
	}
	callstest.WaitFor(t, "the server to forget the call", 5*time.Second, func() bool { return r.srv.Calls() == 0 })
}
