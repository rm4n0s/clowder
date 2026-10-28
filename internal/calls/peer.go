package calls

import (
	"io"
	"sync/atomic"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/rm4n0s/errors"
)

// keyFrameGap is the shortest time between two requests for a key frame to the
// same sender.
const keyFrameGap = 300 * time.Millisecond

// peer is one PeerConnection of a participant, made for one signalling
// socket. The server is its offerer: the PeerConnection has an audio and a
// video transceiver that send and receive, each sending the local track that
// carries the media of the other participant (SPEC D63).
type peer struct {
	p    *participant
	c    *call
	pc   *webrtc.PeerConnection
	sock signalSocket

	// The fields below belong to the goroutine of the call.
	closed    bool
	answered  bool
	remoteSet bool
	pending   []webrtc.ICECandidateInit
	restarted bool
	iceTimer  *time.Timer
	discTimer *time.Timer

	// everConnected is true once the PeerConnection has been connected.
	everConnected atomic.Bool
	// videoSSRC is the SSRC of the video that the participant sends; 0 until
	// the first video packet.
	videoSSRC atomic.Uint32
	// lastKeyFrame is the time of the last request for a key frame to this
	// sender, in nanoseconds.
	lastKeyFrame atomic.Int64
}

// newPeer makes the PeerConnection of a participant with its transceivers and
// handlers. It runs in the goroutine of the call.
func newPeer(p *participant, sock signalSocket) (*peer, error) {
	c := p.c
	pc, err := c.s.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, errors.NewErr(TagNegotiationFailed, err).SetMetadata("step", "peer connection", "call_id", c.id)
	}
	pe := &peer{p: p, c: c, pc: pc, sock: sock}
	out := c.out[p.idx]
	sendrecv := webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendrecv}
	for _, track := range []*webrtc.TrackLocalStaticRTP{out.audio, out.video} {
		if _, err := pc.AddTransceiverFromTrack(track, sendrecv); err != nil {
			_ = pc.Close()
			return nil, errors.NewErr(TagNegotiationFailed, err).SetMetadata("step", "transceiver", "call_id", c.id)
		}
	}
	pc.OnTrack(pe.onTrack)
	pc.OnICECandidate(func(cand *webrtc.ICECandidate) {
		if cand == nil {
			return
		}
		init := cand.ToJSON()
		c.post(func() {
			if pe.closed {
				return
			}
			pe.sock.send(serverMessage{Type: msgCandidate, Candidate: init.Candidate, SDPMid: init.SDPMid, SDPMLineIndex: init.SDPMLineIndex})
		})
	})
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		c.post(func() { pe.onState(state) })
	})
	for _, sender := range pc.GetSenders() {
		video := sender.Track() != nil && sender.Track().Kind() == webrtc.RTPCodecTypeVideo
		c.s.goFunc(func() { pe.readSenderRTCP(sender, video) })
	}
	return pe, nil
}

// sendOffer creates the offer of the server, sets it as the local description
// and sends it with the limit of video (SPEC D64). With restart it asks for new ICE
// credentials, which is how a connection that failed is brought back. It does
// nothing while an earlier offer waits for its answer.
func (pe *peer) sendOffer(restart bool) error {
	if pe.pc.SignalingState() == webrtc.SignalingStateHaveLocalOffer {
		return nil
	}
	offer, err := pe.pc.CreateOffer(&webrtc.OfferOptions{ICERestart: restart})
	if err != nil {
		return errors.NewErr(TagNegotiationFailed, err).SetMetadata("step", "create offer", "call_id", pe.c.id)
	}
	// pion refuses a local description that differs from the offer it made, so
	// the offer is set as it is and the bandwidth lines are added to the text
	// that is sent: the client reads them, and the answer does not need them.
	limited, err := limitVideoBandwidth(offer.SDP, pe.c.s.cfg.MaxVideoBitrate)
	if err != nil {
		return err
	}
	if err := pe.pc.SetLocalDescription(offer); err != nil {
		return errors.NewErr(TagNegotiationFailed, err).SetMetadata("step", "set local description", "call_id", pe.c.id)
	}
	pe.sock.send(serverMessage{Type: msgOffer, SDP: limited})
	return nil
}

// onAnswer applies the answer of the client to the offer. The first answer
// starts the deadline for the connection (SPEC D64) and lets the candidates
// that came early in.
func (pe *peer) onAnswer(sdp string) {
	if err := pe.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp}); err != nil {
		pe.c.logger.Debug("applying the answer failed", "err", err, "user_id", pe.p.userID)
		pe.sock.send(serverMessage{Type: msgError, Code: CodeNegotiationFailed, Message: "The answer could not be used."})
		return
	}
	pe.remoteSet = true
	for _, cand := range pe.pending {
		pe.addCandidate(cand)
	}
	pe.pending = nil
	if pe.answered {
		return
	}
	pe.answered = true
	pe.iceTimer = time.AfterFunc(pe.c.s.cfg.ICETimeout, func() {
		pe.c.post(func() {
			if !pe.closed && !pe.everConnected.Load() && !pe.c.ended {
				pe.c.fail("the connection did not come up in time", pe.p.userID)
			}
		})
	})
}

// onCandidate applies a candidate of the client, or keeps it until the answer
// has been applied.
func (pe *peer) onCandidate(msg clientMessage) {
	init := webrtc.ICECandidateInit{Candidate: msg.Candidate, SDPMid: msg.SDPMid, SDPMLineIndex: msg.SDPMLineIndex}
	if !pe.remoteSet {
		pe.pending = append(pe.pending, init)
		return
	}
	pe.addCandidate(init)
}

// addCandidate gives a candidate to the PeerConnection. A candidate it cannot
// use is not fatal: ICE goes on with the others.
func (pe *peer) addCandidate(init webrtc.ICECandidateInit) {
	if err := pe.pc.AddICECandidate(init); err != nil {
		pe.c.logger.Debug("adding a candidate failed", "err", err, "user_id", pe.p.userID)
	}
}

// onState handles a change of the state of the connection. A participant whose
// transport is disconnected or failed gets one ICE restart and the deadline of
// D64, after which the call ends.
func (pe *peer) onState(state webrtc.PeerConnectionState) {
	if pe.closed || pe.c.ended {
		return
	}
	pe.c.logger.Info("connection state", "user_id", pe.p.userID, "state", state.String())
	switch state {
	case webrtc.PeerConnectionStateConnected:
		stopTimer(&pe.discTimer)
		pe.restarted = false
		pe.everConnected.Store(true)
		// The senders start to flow to a participant who is connected now: they
		// need a key frame.
		pe.c.requestKeyFrames()
	case webrtc.PeerConnectionStateDisconnected, webrtc.PeerConnectionStateFailed:
		if pe.discTimer == nil {
			pe.discTimer = time.AfterFunc(pe.c.s.cfg.DisconnectTimeout, func() {
				pe.c.post(func() {
					if pe.closed || pe.c.ended || pe.pc.ConnectionState() == webrtc.PeerConnectionStateConnected {
						return
					}
					pe.c.fail("the connection was lost", pe.p.userID)
				})
			})
		}
		if !pe.restarted && pe.answered {
			pe.restarted = true
			if err := pe.sendOffer(true); err != nil {
				pe.c.logger.Debug("asking for new ICE credentials failed", "err", err, "user_id", pe.p.userID)
			}
		}
	case webrtc.PeerConnectionStateNew, webrtc.PeerConnectionStateConnecting, webrtc.PeerConnectionStateClosed:
	}
}

// close closes the PeerConnection and stops its timers.
func (pe *peer) close() {
	if pe.closed {
		return
	}
	pe.closed = true
	stopTimer(&pe.iceTimer)
	stopTimer(&pe.discTimer)
	if err := pe.pc.Close(); err != nil {
		pe.c.logger.Debug("closing the PeerConnection failed", "err", err, "user_id", pe.p.userID)
	}
}

// onTrack handles a track that the participant sends: it is forwarded to the
// other participant. It runs in a goroutine of pion.
func (pe *peer) onTrack(remote *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
	c := pe.c
	to := c.out[1-pe.p.idx]
	var local *webrtc.TrackLocalStaticRTP
	var rw *rewriter
	video := false
	switch remote.Kind() {
	case webrtc.RTPCodecTypeAudio:
		local, rw = to.audio, to.audioRW
	case webrtc.RTPCodecTypeVideo:
		local, rw, video = to.video, to.videoRW, true
		pe.videoSSRC.Store(uint32(remote.SSRC()))
	default:
		return
	}
	c.s.goFunc(func() { pe.forward(remote, local, rw, video) })
	c.s.goFunc(func() { pe.readReceiverRTCP(receiver) })
}

// forward copies the RTP packets of a track of the participant into the local
// track of the other: the header extensions of the sender are dropped (their
// numbers mean something else on the other connection), and the sequence
// numbers and timestamps are kept continuous across a change of source, such
// as a screen share replacing the camera.
func (pe *peer) forward(remote *webrtc.TrackRemote, local *webrtc.TrackLocalStaticRTP, rw *rewriter, video bool) {
	for {
		pkt, _, err := remote.ReadRTP()
		if err != nil {
			return
		}
		pkt.Extension, pkt.Extensions, pkt.ExtensionProfile = false, nil, 0
		pkt.CSRC = nil
		if rw.rewrite(pkt) && video {
			pe.videoSSRC.Store(uint32(remote.SSRC()))
			pe.requestKeyFrame()
		}
		if err := local.WriteRTP(pkt); err != nil && !errors.Is(err, io.ErrClosedPipe) {
			pe.c.logger.Debug("forwarding a packet failed", "err", err, "user_id", pe.p.userID)
		}
	}
}

// readReceiverRTCP reads the RTCP of the sender of a track, which the
// interceptors of pion need to see, until the PeerConnection closes.
func (pe *peer) readReceiverRTCP(receiver *webrtc.RTPReceiver) {
	for {
		if _, _, err := receiver.ReadRTCP(); err != nil {
			return
		}
	}
}

// readSenderRTCP reads the RTCP that the participant sends about what the
// server sends it, until the PeerConnection closes. A request for a key frame
// about the video goes on to the other participant, who is the source of that
// video.
func (pe *peer) readSenderRTCP(sender *webrtc.RTPSender, video bool) {
	for {
		pkts, _, err := sender.ReadRTCP()
		if err != nil {
			return
		}
		if !video {
			continue
		}
		for _, pkt := range pkts {
			switch pkt.(type) {
			case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
				if source := pe.c.parts[1-pe.p.idx].cur.Load(); source != nil {
					source.requestKeyFrame()
				}
			}
		}
	}
}

// requestKeyFrame asks the participant for a key frame of the video they send
// (a PLI). Requests come at most every 300 ms, and nothing is sent before the
// first video packet. It is safe to call from any goroutine.
func (pe *peer) requestKeyFrame() {
	ssrc := pe.videoSSRC.Load()
	if ssrc == 0 {
		return
	}
	now := time.Now().UnixNano()
	last := pe.lastKeyFrame.Load()
	if now-last < int64(keyFrameGap) || !pe.lastKeyFrame.CompareAndSwap(last, now) {
		return
	}
	err := pe.pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{SenderSSRC: 1, MediaSSRC: ssrc}})
	if err != nil {
		pe.c.logger.Debug("asking for a key frame failed", "err", err, "user_id", pe.p.userID)
	}
}

// TagNegotiationFailed is the tag of a failure to make, send or apply an offer
// of the media server.
const TagNegotiationFailed = "MediaNegotiationFailed"
