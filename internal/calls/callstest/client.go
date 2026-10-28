package callstest

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// Message is a message of the signalling socket, as the server sends it. Only
// the fields of its type are set.
type Message struct {
	Type          string  `json:"type"`
	SDP           string  `json:"sdp,omitempty"`
	Candidate     string  `json:"candidate,omitempty"`
	SDPMid        *string `json:"sdpMid,omitempty"`
	SDPMLineIndex *uint16 `json:"sdpMLineIndex,omitempty"`
	Microphone    *bool   `json:"microphone,omitempty"`
	Camera        *bool   `json:"camera,omitempty"`
	Reason        string  `json:"reason,omitempty"`
	Code          string  `json:"code,omitempty"`
	Message       string  `json:"message,omitempty"`
}

// Options choose how a client connects.
type Options struct {
	// Networks limits the network types of the client's ICE agent; nil is UDP4.
	Networks []webrtc.NetworkType
	// NoCandidates keeps the client from sending its candidates.
	NoCandidates bool
	// NoVideo makes an audio-only client.
	NoVideo bool
	// Header is sent with the WebSocket handshake (a bearer token, a cookie).
	Header http.Header
	// Origin, when set, is sent as the Origin header.
	Origin string
	// Log, when set, receives the problems of the client instead of the test.
	Log func(format string, args ...any)
}

// Client is a pion peer in a call.
type Client struct {
	t    testing.TB
	name string
	pc   *webrtc.PeerConnection
	ws   *websocket.Conn
	ctx  context.Context
	stop context.CancelFunc
	wg   sync.WaitGroup
	opts Options

	audio *webrtc.TrackLocalStaticSample
	// video is the track the client writes its video to: an RTP track, so
	// that the client numbers the packets itself, as a browser does when it
	// replaces the camera with a screen. videoSample replaces it in a test of
	// a source with a sequencer of its own.
	video       atomic.Pointer[webrtc.TrackLocalStaticRTP]
	videoSample atomic.Pointer[webrtc.TrackLocalStaticSample]
	// vseq and vts number the video packets the client writes.
	vseq uint16
	vts  uint32

	audioIn, videoIn atomic.Int64
	offers           atomic.Int64
	plis             atomic.Int64
	closeCode        atomic.Int32
	lastOffer        atomic.Value

	mu         sync.Mutex
	videoSSRCs map[uint32]int
	peerStates []Message
	errs       []Message
	rembs      []float32
	candidates []string
	ended      chan string
	connected  chan struct{}
	conOnce    sync.Once
	closed     chan struct{}
	closeOnce  sync.Once
}

// Dial connects a client to the signalling socket at url and starts the
// exchange: it answers every offer. The client is closed when the test ends.
func Dial(tb testing.TB, name, url string, opts Options) (*Client, *http.Response, error) {
	tb.Helper()
	ctx, cancel := context.WithCancel(tb.Context())
	c := &Client{t: tb, name: name, ctx: ctx, stop: cancel, opts: opts, videoSSRCs: map[uint32]int{},
		ended: make(chan string, 1), connected: make(chan struct{}), closed: make(chan struct{})}
	dial := &websocket.DialOptions{HTTPHeader: opts.Header.Clone()}
	if opts.Origin != "" {
		if dial.HTTPHeader == nil {
			dial.HTTPHeader = http.Header{}
		}
		dial.HTTPHeader.Set("Origin", opts.Origin)
	}
	ws, resp, err := websocket.Dial(ctx, url, dial)
	if err != nil {
		cancel()
		return nil, resp, err
	}
	c.ws = ws
	c.newPeerConnection()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer close(c.closed)
		c.readLoop()
	}()
	tb.Cleanup(c.Close)
	return c, resp, nil
}

// MustDial is Dial that fails the test when the handshake does not work.
func MustDial(tb testing.TB, name, url string, opts Options) *Client {
	tb.Helper()
	c, resp, err := Dial(tb, name, url, opts)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		tb.Fatalf("%s: dial: %v (status %d)", name, err, status)
	}
	return c
}

func (c *Client) logf(format string, args ...any) {
	if c.opts.Log != nil {
		c.opts.Log(format, args...)
		return
	}
	c.t.Logf(c.name+": "+format, args...)
}

func (c *Client) errorf(format string, args ...any) {
	if c.ctx.Err() != nil {
		return
	}
	c.t.Errorf(c.name+": "+format, args...)
}

// newPeerConnection makes the client's PeerConnection.
func (c *Client) newPeerConnection() {
	me := &webrtc.MediaEngine{}
	if err := me.RegisterDefaultCodecs(); err != nil {
		c.t.Fatal(err)
	}
	se := webrtc.SettingEngine{}
	se.SetIncludeLoopbackCandidate(true)
	networks := c.opts.Networks
	if networks == nil {
		networks = []webrtc.NetworkType{webrtc.NetworkTypeUDP4}
	}
	se.SetNetworkTypes(networks)
	api := webrtc.NewAPI(webrtc.WithMediaEngine(me), webrtc.WithSettingEngine(se))
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		c.t.Fatal(err)
	}
	c.pc = pc
	pc.OnTrack(func(remote *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			for {
				if _, _, err := receiver.ReadRTCP(); err != nil {
					return
				}
			}
		}()
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			for {
				pkt, _, err := remote.ReadRTP()
				if err != nil {
					return
				}
				c.count(remote, pkt)
			}
		}()
	})
	pc.OnICECandidate(func(cand *webrtc.ICECandidate) {
		if cand == nil || c.opts.NoCandidates {
			return
		}
		init := cand.ToJSON()
		c.Send(map[string]any{"type": "candidate", "candidate": init.Candidate, "sdpMid": init.SDPMid, "sdpMLineIndex": init.SDPMLineIndex})
	})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateConnected {
			c.conOnce.Do(func() { close(c.connected) })
		}
	})
}

// count records a packet received.
func (c *Client) count(remote *webrtc.TrackRemote, pkt *rtp.Packet) {
	if remote.Kind() == webrtc.RTPCodecTypeAudio {
		c.audioIn.Add(1)
		return
	}
	c.videoIn.Add(1)
	c.mu.Lock()
	c.videoSSRCs[pkt.SSRC]++
	c.mu.Unlock()
}

// Send writes a JSON message to the signalling socket.
func (c *Client) Send(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		c.errorf("%v", err)
		return
	}
	c.WriteRaw(websocket.MessageText, data)
}

// WriteRaw writes a message of any type to the signalling socket.
func (c *Client) WriteRaw(typ websocket.MessageType, data []byte) {
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	if err := c.ws.Write(ctx, typ, data); err != nil && c.ctx.Err() == nil {
		c.logf("write: %v", err)
	}
}

// readLoop answers the offers and applies the candidates of the server.
func (c *Client) readLoop() {
	for {
		_, data, err := c.ws.Read(c.ctx)
		if err != nil {
			c.closeCode.Store(int32(websocket.CloseStatus(err)))
			return
		}
		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			c.errorf("bad message %q", data)
			return
		}
		switch msg.Type {
		case "offer":
			c.offers.Add(1)
			c.lastOffer.Store(msg.SDP)
			c.answerOffer(msg.SDP)
		case "candidate":
			c.mu.Lock()
			c.candidates = append(c.candidates, msg.Candidate)
			c.mu.Unlock()
			init := webrtc.ICECandidateInit{Candidate: msg.Candidate, SDPMid: msg.SDPMid, SDPMLineIndex: msg.SDPMLineIndex}
			if err := c.pc.AddICECandidate(init); err != nil {
				c.logf("AddICECandidate: %v", err)
			}
		case "peer-state":
			c.mu.Lock()
			c.peerStates = append(c.peerStates, msg)
			c.mu.Unlock()
		case "error":
			c.mu.Lock()
			c.errs = append(c.errs, msg)
			c.mu.Unlock()
		case "ended":
			select {
			case c.ended <- msg.Reason:
			default:
			}
		}
	}
}

// answerOffer applies an offer, adds the client's own tracks to the
// transceivers it made and sends the answer.
func (c *Client) answerOffer(sdp string) {
	if err := c.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp}); err != nil {
		c.errorf("SetRemoteDescription: %v", err)
		return
	}
	if c.audio == nil {
		var err error
		c.audio, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", c.name)
		if err != nil {
			c.t.Fatal(err)
		}
		if _, err := c.pc.AddTrack(c.audio); err != nil {
			c.errorf("AddTrack audio: %v", err)
		}
		if !c.opts.NoVideo {
			video, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}, "video", c.name)
			if err != nil {
				c.t.Fatal(err)
			}
			c.video.Store(video)
			if _, err := c.pc.AddTrack(video); err != nil {
				c.errorf("AddTrack video: %v", err)
			}
		}
		c.readSenderRTCP()
		c.startSending()
	}
	answer, err := c.pc.CreateAnswer(nil)
	if err != nil {
		c.errorf("CreateAnswer: %v", err)
		return
	}
	if err := c.pc.SetLocalDescription(answer); err != nil {
		c.errorf("SetLocalDescription: %v", err)
		return
	}
	c.Send(map[string]any{"type": "answer", "sdp": answer.SDP})
}

// readSenderRTCP reads what the server says about the video the client sends:
// requests for key frames and the limit of the bitrate.
func (c *Client) readSenderRTCP() {
	for _, sender := range c.pc.GetSenders() {
		if sender.Track() == nil || sender.Track().Kind() != webrtc.RTPCodecTypeVideo {
			continue
		}
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			for {
				pkts, _, err := sender.ReadRTCP()
				if err != nil {
					return
				}
				for _, p := range pkts {
					switch p := p.(type) {
					case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
						c.plis.Add(1)
					case *rtcp.ReceiverEstimatedMaximumBitrate:
						c.mu.Lock()
						c.rembs = append(c.rembs, p.Bitrate)
						c.mu.Unlock()
					}
				}
			}
		}()
	}
}

// startSending writes a sample of audio every 20 ms and a packet of video
// every 40 ms until the client closes. Writes before the connection is up are
// dropped by the tracks.
func (c *Client) startSending() {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		t := time.NewTicker(20 * time.Millisecond)
		defer t.Stop()
		n := 0
		for {
			select {
			case <-c.ctx.Done():
				return
			case <-t.C:
			}
			n++
			if err := c.audio.WriteSample(media.Sample{Data: []byte{1, 2, 3, 4, 5, 6, 7, 8}, Duration: 20 * time.Millisecond}); err != nil && c.ctx.Err() == nil {
				c.logf("audio sample: %v", err)
			}
			if n%2 != 0 {
				continue
			}
			// A VP8 payload: the first byte is the payload descriptor with the
			// start of a partition.
			data := make([]byte, 600)
			data[0] = 0x10
			if sample := c.videoSample.Load(); sample != nil {
				if err := sample.WriteSample(media.Sample{Data: data, Duration: 40 * time.Millisecond}); err != nil && c.ctx.Err() == nil {
					c.logf("video sample: %v", err)
				}
				continue
			}
			if video := c.video.Load(); video != nil {
				c.vseq++
				c.vts += 3600
				pkt := &rtp.Packet{
					Header:  rtp.Header{Version: 2, Marker: true, SequenceNumber: c.vseq, Timestamp: c.vts},
					Payload: data,
				}
				if err := video.WriteRTP(pkt); err != nil && c.ctx.Err() == nil {
					c.logf("video packet: %v", err)
				}
			}
		}
	}()
}

// VideoSender returns the RTPSender of the client's video.
func (c *Client) VideoSender() *webrtc.RTPSender {
	for _, s := range c.pc.GetSenders() {
		if s.Track() != nil && s.Track().Kind() == webrtc.RTPCodecTypeVideo {
			return s
		}
	}
	c.t.Fatalf("%s has no video sender", c.name)
	return nil
}

// ShareScreenRTP replaces the camera with a screen on the same transceiver,
// with a track that goes on numbering the packets as a browser does.
func (c *Client) ShareScreenRTP() error {
	screen, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}, "screen", c.name)
	if err != nil {
		return err
	}
	if err := c.VideoSender().ReplaceTrack(screen); err != nil {
		return err
	}
	c.video.Store(screen)
	return nil
}

// ShareScreenSample replaces the camera with a screen whose track has its own
// sequence numbers and timestamps.
func (c *Client) ShareScreenSample() error {
	screen, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}, "screen", c.name)
	if err != nil {
		return err
	}
	if err := c.VideoSender().ReplaceTrack(screen); err != nil {
		return err
	}
	c.videoSample.Store(screen)
	return nil
}

// Close stops the client and waits for its goroutines.
func (c *Client) Close() {
	c.closeOnce.Do(func() {
		c.stop()
		_ = c.ws.CloseNow()
		_ = c.pc.Close()
		c.wg.Wait()
	})
}

// Name is the name the client was given.
func (c *Client) Name() string { return c.name }

// PC is the PeerConnection of the client.
func (c *Client) PC() *webrtc.PeerConnection { return c.pc }

// AudioIn and VideoIn count the audio and video packets received.
func (c *Client) AudioIn() int64 { return c.audioIn.Load() }

// VideoIn counts the video packets received.
func (c *Client) VideoIn() int64 { return c.videoIn.Load() }

// Offers counts the offers received.
func (c *Client) Offers() int64 { return c.offers.Load() }

// PLIs counts the requests for a key frame the client's video sender got.
func (c *Client) PLIs() int64 { return c.plis.Load() }

// CloseCode is the close code of the socket, 0 while it is open and -1 when it
// ended without one.
func (c *Client) CloseCode() int { return int(c.closeCode.Load()) }

// LastOffer is the SDP of the last offer, as the server sent it.
func (c *Client) LastOffer() string {
	s, _ := c.lastOffer.Load().(string)
	return s
}

// Ended receives the reason of the ended message.
func (c *Client) Ended() <-chan string { return c.ended }

// Connected is closed when the PeerConnection is connected.
func (c *Client) Connected() <-chan struct{} { return c.connected }

// Closed is closed when the socket has been read to its end.
func (c *Client) Closed() <-chan struct{} { return c.closed }

// VideoSSRCs returns how many video packets arrived on each SSRC.
func (c *Client) VideoSSRCs() map[uint32]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[uint32]int, len(c.videoSSRCs))
	for k, v := range c.videoSSRCs {
		out[k] = v
	}
	return out
}

// PeerStates returns the peer-state messages received.
func (c *Client) PeerStates() []Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Message(nil), c.peerStates...)
}

// Errors returns the error messages received.
func (c *Client) Errors() []Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Message(nil), c.errs...)
}

// REMBs returns the bitrates of the REMB messages received.
func (c *Client) REMBs() []float32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]float32(nil), c.rembs...)
}

// Candidates returns the candidate lines of the server.
func (c *Client) Candidates() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.candidates...)
}

// WaitFor polls cond until it is true or the deadline passes.
func WaitFor(tb testing.TB, what string, d time.Duration, cond func() bool) {
	tb.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			tb.Fatalf("timed out after %v waiting for %s", d, what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Receiving waits until the client has received n more audio and n more video
// packets than it had when the call was made.
func (c *Client) Receiving(tb testing.TB, n int64, d time.Duration) {
	tb.Helper()
	a0, v0 := c.AudioIn(), c.VideoIn()
	WaitFor(tb, c.name+" to receive audio and video", d, func() bool {
		return c.AudioIn()-a0 >= n && c.VideoIn()-v0 >= n
	})
}

// ReceivingAudio waits until the client has received n more audio packets.
func (c *Client) ReceivingAudio(tb testing.TB, n int64, d time.Duration) {
	tb.Helper()
	a0 := c.AudioIn()
	WaitFor(tb, c.name+" to receive audio", d, func() bool { return c.AudioIn()-a0 >= n })
}
