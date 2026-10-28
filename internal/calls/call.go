package calls

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/errors"
)

// The end reasons the media server gives to a client that the service did not
// name.
const (
	reasonEnded         = "ended"
	reasonFailed        = "failed"
	reasonServerStopped = "server_stopped"
)

// outTracks are the two local tracks that carry the media of one participant
// to the other, and the rewriters that keep their packets continuous. They
// belong to the call, not to a PeerConnection, so that a participant who joins
// again with a new PeerConnection gets the same tracks.
type outTracks struct {
	audio, video     *webrtc.TrackLocalStaticRTP
	audioRW, videoRW *rewriter
}

// newOutTracks makes the tracks that carry media to one participant.
func newOutTracks() (*outTracks, error) {
	audio, err := webrtc.NewTrackLocalStaticRTP(serverCodecs()[0].params.RTPCodecCapability, "audio", "clowder")
	if err != nil {
		return nil, err
	}
	video, err := webrtc.NewTrackLocalStaticRTP(serverCodecs()[1].params.RTPCodecCapability, "video", "clowder")
	if err != nil {
		return nil, err
	}
	// One frame is 20 ms of Opus at 48 kHz and 1/30 s of VP8 at 90 kHz.
	return &outTracks{audio: audio, video: video, audioRW: newRewriter(960), videoRW: newRewriter(3000)}, nil
}

// participant is one of the two sides of a call.
type participant struct {
	c      *call
	idx    int
	userID uint64
	// cur is the PeerConnection of the participant now; nil before the socket
	// joins. The forwarders of the other side read it.
	cur atomic.Pointer[peer]

	// The fields below belong to the goroutine of the call.
	sock       signalSocket
	stateKnown bool
	mic, cam   bool
	grace      *time.Timer
}

// call is the media of one call: the two participants and the tracks between
// them. One goroutine (run) owns its state; the callbacks of pion and the
// sockets hand it work through the inbox.
type call struct {
	s      *Server
	id     uint64
	convID uint64
	logger *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	inbox  chan func()
	// done is closed when the call has closed.
	done chan struct{}

	parts [2]*participant
	// out[i] carries media to participant i.
	out [2]*outTracks

	// The fields below belong to the goroutine of the call.
	active bool
	ended  bool
	reason string
	join   *time.Timer
	once   sync.Once
}

// newCall makes the call of a join; its goroutine is started by the server.
func newCall(s *Server, j Join) *call {
	ctx, cancel := context.WithCancel(s.ctx)
	c := &call{
		s: s, id: j.CallID, convID: j.ConversationID, ctx: ctx, cancel: cancel,
		logger: s.logger.With("call_id", j.CallID),
		inbox:  make(chan func(), 256), done: make(chan struct{}),
	}
	c.parts[0] = &participant{c: c, idx: 0, userID: j.CallerID}
	c.parts[1] = &participant{c: c, idx: 1, userID: j.CalleeID}
	return c
}

// post hands f to the goroutine of the call. It drops f when the call has
// closed.
func (c *call) post(f func()) {
	select {
	case c.inbox <- f:
	case <-c.ctx.Done():
	}
}

// partOf returns the participant who is the user, or nil.
func (c *call) partOf(userID uint64) *participant {
	for _, p := range c.parts {
		if p.userID == userID {
			return p
		}
	}
	return nil
}

// run is the goroutine of the call: it owns the state of the call, listens to
// the live events of the two users for the end of the call and of their
// sessions, and announces the limit of video. It returns when the call has
// closed.
func (c *call) run() {
	defer c.closeAll()
	for i := range c.out {
		out, err := newOutTracks()
		if err != nil {
			c.logger.Error("making the tracks of the call failed", "err", err)
			c.ended, c.reason = true, reasonFailed
			return
		}
		c.out[i] = out
	}
	sub := c.subscribe()
	if c.ended {
		return
	}
	remb := time.NewTicker(defaultREMBInterval)
	defer remb.Stop()
	for !c.ended {
		var events <-chan live.Event
		if sub != nil {
			events = sub.C()
		}
		select {
		case <-c.ctx.Done():
			c.ended, c.reason = true, reasonServerStopped
		case f := <-c.inbox:
			f()
		case <-remb.C:
			c.announceVideoLimit()
		case ev, ok := <-events:
			if !ok {
				if sub.DropReason() == "" {
					// The hub is closed: the server is stopping.
					c.ended, c.reason = true, reasonServerStopped
					break
				}
				sub = c.subscribe()
				break
			}
			c.onEvent(ev)
		}
	}
	if sub != nil {
		sub.Close()
	}
}

// subscribe listens to the topics of the two users and then reads the state of
// the call, so that nothing falls between the two: a call that is over closes
// at once, and one that is active starts its deadlines.
func (c *call) subscribe() *live.Subscription {
	sub := c.s.events.Subscribe(c.ctx, live.UserTopic(c.parts[0].userID), live.UserTopic(c.parts[1].userID))
	st, err := c.s.ctl.Status(c.ctx, c.id)
	switch {
	case err != nil:
		c.logger.Warn("reading the state of the call failed", "err", err)
	case !st.Open:
		c.ended, c.reason = true, orElse(st.Reason, reasonEnded)
	case st.Active:
		c.activate()
	}
	return sub
}

// orElse returns s, or def when s is empty.
func orElse(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// onEvent handles a live event of one of the two users.
func (c *call) onEvent(ev live.Event) {
	switch ev.Kind {
	case live.KindCallEnded:
		if ev.CallID != c.id {
			return
		}
		reason := reasonEnded
		if st, err := c.s.ctl.Status(c.ctx, c.id); err == nil {
			reason = orElse(st.Reason, reasonEnded)
		}
		c.ended, c.reason = true, reason
	case live.KindCallStarted:
		if ev.CallID == c.id {
			c.activate()
		}
	case live.KindUserChanged:
		if p := c.partOf(ev.UserID); p != nil && p.sock != nil {
			p.sock.checkSession(c.ctx)
		}
	}
}

// activate notes that the callee has answered: both participants must answer
// the offer of the server within the join timeout.
func (c *call) activate() {
	if c.active {
		return
	}
	c.active = true
	c.logger.Info("call active")
	c.join = time.AfterFunc(c.s.cfg.JoinTimeout, func() {
		c.post(func() {
			for _, p := range c.parts {
				if pe := p.cur.Load(); pe == nil || !pe.answered {
					c.fail("a participant did not join", p.userID)
					return
				}
			}
		})
	})
}

// fail ends the call as failed: the service writes it down (the call.ended
// event closes the call too, but it is closed here at once).
func (c *call) fail(why string, userID uint64) {
	if c.ended {
		return
	}
	c.logger.Warn("call failed", "why", why, "user_id", userID)
	if err := c.s.ctl.Fail(context.WithoutCancel(c.ctx), c.id); err != nil {
		c.logger.Error("ending the call as failed failed", "err", err)
	}
	c.ended, c.reason = true, reasonFailed
}

// announceVideoLimit tells the sender of each participant, with REMB, how much
// video the server forwards (SPEC D64).
func (c *call) announceVideoLimit() {
	for _, p := range c.parts {
		pe := p.cur.Load()
		if pe == nil || pe.closed || pe.pc.ConnectionState() != webrtc.PeerConnectionStateConnected {
			continue
		}
		ssrc := pe.videoSSRC.Load()
		if ssrc == 0 {
			continue
		}
		err := pe.pc.WriteRTCP([]rtcp.Packet{&rtcp.ReceiverEstimatedMaximumBitrate{
			SenderSSRC: 1, Bitrate: float32(c.s.cfg.MaxVideoBitrate), SSRCs: []uint32{ssrc},
		}})
		if err != nil {
			c.logger.Debug("sending the video limit failed", "err", err, "user_id", p.userID)
		}
	}
}

// requestKeyFrames asks both senders for a key frame.
func (c *call) requestKeyFrames() {
	for _, p := range c.parts {
		if pe := p.cur.Load(); pe != nil {
			pe.requestKeyFrame()
		}
	}
}

// attach joins the socket to the call as its participant: any earlier socket
// and PeerConnection of the participant are closed, a new PeerConnection is
// made and the offer is sent. It runs in the goroutine of the call.
func (c *call) attach(sock signalSocket) error {
	if c.ended {
		return errors.New(TagCallClosed, "the call has ended", "call_id", c.id)
	}
	p := c.partOf(sock.uid())
	if p == nil {
		return errors.New(TagNotParticipant, "the user is not a participant of the call", "call_id", c.id, "user_id", sock.uid())
	}
	if p.sock != nil {
		p.sock.finish(closeReplaced, "replaced by a newer connection")
	}
	if old := p.cur.Swap(nil); old != nil {
		old.close()
	}
	stopTimer(&p.grace)
	p.sock = sock
	pe, err := newPeer(p, sock)
	if err != nil {
		p.sock = nil
		return err
	}
	p.cur.Store(pe)
	if other := c.parts[1-p.idx]; other.stateKnown {
		sock.send(peerStateMessage(other.mic, other.cam))
	}
	if err := pe.sendOffer(false); err != nil {
		p.cur.Store(nil)
		pe.close()
		p.sock = nil
		return err
	}
	c.logger.Info("participant joined", "user_id", p.userID)
	return nil
}

// socketClosed notes that a socket has ended. The PeerConnection stays, so
// that the media goes on while the participant joins again; an active call
// gives them the reconnect grace.
func (c *call) socketClosed(sock signalSocket) {
	p := c.partOf(sock.uid())
	if p == nil || p.sock != sock {
		return
	}
	p.sock = nil
	if c.ended || !c.active {
		return
	}
	stopTimer(&p.grace)
	p.grace = time.AfterFunc(c.s.cfg.ReconnectGrace, func() {
		c.post(func() {
			if p.sock == nil {
				c.fail("the signalling socket was not opened again", p.userID)
			}
		})
	})
}

// onMessage handles a message of the client of a socket.
func (c *call) onMessage(sock signalSocket, msg clientMessage) {
	p := c.partOf(sock.uid())
	if p == nil || p.sock != sock || c.ended {
		return
	}
	pe := p.cur.Load()
	switch msg.Type {
	case msgAnswer:
		if pe != nil {
			pe.onAnswer(msg.SDP)
		}
	case msgCandidate:
		if pe != nil {
			pe.onCandidate(msg)
		}
	case msgState:
		p.stateKnown, p.mic, p.cam = true, *msg.Microphone, *msg.Camera
		if other := c.parts[1-p.idx]; other.sock != nil {
			other.sock.send(peerStateMessage(p.mic, p.cam))
		}
		if err := c.s.ctl.SetMedia(c.ctx, c.id, p.userID, p.mic, p.cam); err != nil {
			c.logger.Debug("publishing the state of the media failed", "err", err, "user_id", p.userID)
		}
	case msgHangup:
		if err := c.s.ctl.Leave(c.ctx, c.id, p.userID); err != nil {
			c.logger.Debug("leaving the call failed", "err", err, "user_id", p.userID)
			sock.send(serverMessage{Type: msgError, Code: CodeWrongState, Message: "The call cannot be ended now."})
		}
	}
}

// closeAll closes the call: every participant is told how it ended, the
// sockets and the PeerConnections are closed and the server forgets the call.
// It runs once, when run returns.
func (c *call) closeAll() {
	c.once.Do(func() {
		reason := orElse(c.reason, reasonEnded)
		c.ended = true
		stopTimer(&c.join)
		for _, p := range c.parts {
			stopTimer(&p.grace)
			if p.sock != nil {
				p.sock.send(serverMessage{Type: msgEnded, Reason: reason})
				p.sock.finish(websocket.StatusNormalClosure, "call ended")
			}
			if pe := p.cur.Swap(nil); pe != nil {
				pe.close()
			}
		}
		c.cancel()
		c.s.remove(c)
		close(c.done)
		c.logger.Info("call closed", "reason", reason)
	})
}

// stopTimer stops the timer and forgets it.
func stopTimer(t **time.Timer) {
	if *t != nil {
		(*t).Stop()
		*t = nil
	}
}

// peerStateMessage is the message that tells a client the state of the other
// participant.
func peerStateMessage(mic, cam bool) serverMessage {
	return serverMessage{Type: msgPeerState, Microphone: &mic, Camera: &cam}
}

const (
	// TagCallClosed is the tag of a socket that joins a call whose media has
	// closed already: the call ended.
	TagCallClosed = "MediaCallClosed"
	// TagNotParticipant is the tag of a socket of a user who is neither the
	// caller nor the callee of the call.
	TagNotParticipant = "MediaNotParticipant"
)
