package calls

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/rm4n0s/clowder/internal/live"
)

// fakeCtl is the service of the calls as the media server sees it: it keeps
// the state of the calls in memory and publishes the end of a call to a hub,
// as internal/app does.
type fakeCtl struct {
	hub *live.Hub

	mu     sync.Mutex
	calls  map[uint64]*fakeCall
	failed []uint64
	media  []mediaState
	left   []uint64
}

// fakeCall is a call of fakeCtl.
type fakeCall struct {
	caller, callee uint64
	active, open   bool
	reason         string
}

// mediaState is a call to SetMedia.
type mediaState struct {
	call, user uint64
	mic, cam   bool
}

func newFakeCtl(hub *live.Hub) *fakeCtl {
	return &fakeCtl{hub: hub, calls: map[uint64]*fakeCall{}}
}

// add makes a call between the two users, ringing (active false) or active.
func (f *fakeCtl) add(id, caller, callee uint64, active bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[id] = &fakeCall{caller: caller, callee: callee, open: true, active: active}
}

// answer moves the call to active and publishes call.started.
func (f *fakeCtl) answer(id uint64) {
	f.mu.Lock()
	c := f.calls[id]
	c.active = true
	f.mu.Unlock()
	f.publish(live.KindCallStarted, id, c)
}

// end ends the call with the reason and publishes call.ended.
func (f *fakeCtl) end(id uint64, reason string) {
	f.mu.Lock()
	c := f.calls[id]
	if !c.open {
		f.mu.Unlock()
		return
	}
	c.open, c.reason = false, reason
	f.mu.Unlock()
	f.publish(live.KindCallEnded, id, c)
}

func (f *fakeCtl) publish(kind string, id uint64, c *fakeCall) {
	for _, u := range []uint64{c.caller, c.callee} {
		f.hub.Publish(live.Event{Topic: live.UserTopic(u), Kind: kind, UserID: u, CallID: id})
	}
}

func (f *fakeCtl) Status(_ context.Context, id uint64) (Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.calls[id]
	if !ok {
		return Status{}, nil
	}
	return Status{Open: c.open, Active: c.open && c.active, Reason: c.reason}, nil
}

func (f *fakeCtl) Fail(_ context.Context, id uint64) error {
	f.mu.Lock()
	f.failed = append(f.failed, id)
	f.mu.Unlock()
	f.end(id, "failed")
	return nil
}

func (f *fakeCtl) SetMedia(_ context.Context, id, user uint64, mic, cam bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.media = append(f.media, mediaState{id, user, mic, cam})
	return nil
}

func (f *fakeCtl) Leave(_ context.Context, id, user uint64) error {
	f.mu.Lock()
	f.left = append(f.left, user)
	f.mu.Unlock()
	f.end(id, "hangup")
	return nil
}

// failedCalls returns the calls the server ended as failed.
func (f *fakeCtl) failedCalls() []uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uint64(nil), f.failed...)
}

// rig is a media server on loopback behind an httptest server whose handler
// joins the socket of the user named in the query to the call.
type rig struct {
	t      *testing.T
	hub    *live.Hub
	ctl    *fakeCtl
	srv    *Server
	http   *httptest.Server
	logs   *logBuffer
	closed bool

	// sessions are the session checks of the users, by user ID; a user with
	// none keeps a good session.
	smu      sync.Mutex
	sessions map[uint64]func() (websocket.StatusCode, string)
}

// endSession makes the session check of the user fail with the code.
func (r *rig) endSession(user uint64, code websocket.StatusCode, reason string) {
	r.smu.Lock()
	defer r.smu.Unlock()
	r.sessions[user] = func() (websocket.StatusCode, string) { return code, reason }
}

// logBuffer collects the log of the server so that a test can look at it.
type logBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// newRig starts a server with cfg (Addr and IncludeLoopback are set here).
func newRig(t *testing.T, cfg Config) *rig {
	t.Helper()
	r := &rig{t: t, hub: live.New(), logs: &logBuffer{}, sessions: map[uint64]func() (websocket.StatusCode, string){}}
	r.ctl = newFakeCtl(r.hub)
	cfg.Addr = "127.0.0.1:0"
	cfg.IncludeLoopback = true
	cfg.Logger = slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srv, err := New(t.Context(), cfg, r.ctl, r.hub)
	if err != nil {
		t.Fatalf("New: %+v", err)
	}
	r.srv = srv
	r.http = httptest.NewServer(http.HandlerFunc(r.handle))
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("log of the media server:\n%s", r.logs.String())
		}
	})
	t.Cleanup(r.close)
	return r
}

// handle is the handler of the signalling socket: ?call=&user=&caller=&callee=.
func (r *rig) handle(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	num := func(name string) uint64 {
		n, _ := strconv.ParseUint(q.Get(name), 10, 64)
		return n
	}
	conn, err := websocket.Accept(w, req, nil)
	if err != nil {
		return
	}
	user := num("user")
	r.srv.Serve(context.WithoutCancel(req.Context()), conn, Join{
		CallID: num("call"), ConversationID: 7, CallerID: num("caller"), CalleeID: num("callee"), UserID: user,
		Session: func(context.Context) (websocket.StatusCode, string) {
			r.smu.Lock()
			check := r.sessions[user]
			r.smu.Unlock()
			if check == nil {
				return 0, ""
			}
			return check()
		},
	})
}

// close stops the HTTP server and the media server.
func (r *rig) close() {
	if r.closed {
		return
	}
	r.closed = true
	r.http.CloseClientConnections()
	r.http.Close()
	if err := r.srv.Close(); err != nil {
		r.t.Errorf("Close: %+v", err)
	}
	r.hub.Close()
}

// url is the signalling URL of a user in a call between caller and callee.
func (r *rig) url(call, caller, callee, user uint64) string {
	return "ws" + strings.TrimPrefix(r.http.URL, "http") +
		fmt.Sprintf("/?call=%d&caller=%d&callee=%d&user=%d", call, caller, callee, user)
}

// clientOptions choose how a test client connects.
type clientOptions struct {
	// networks limits the network types of the client's ICE agent; nil is UDP4.
	networks []webrtc.NetworkType
	// noCandidates keeps the client from sending its candidates.
	noCandidates bool
	// video is false for an audio-only client.
	noVideo bool
}

// client is a pion peer that takes part in a call through the signalling
// socket, sending synthetic Opus and VP8 samples and counting what it gets.
type client struct {
	t    *testing.T
	name string
	pc   *webrtc.PeerConnection
	ws   *websocket.Conn
	ctx  context.Context
	stop context.CancelFunc
	wg   sync.WaitGroup

	audio *webrtc.TrackLocalStaticSample
	// video is the track the client writes its video to: an RTP track, so
	// that the client numbers the packets itself, as a browser does when it
	// replaces the camera with a screen. videoSample replaces it in a test of
	// a source with a sequencer of its own.
	video       atomic.Pointer[webrtc.TrackLocalStaticRTP]
	videoSample atomic.Pointer[webrtc.TrackLocalStaticSample]

	audioIn, videoIn atomic.Int64
	// videoSSRCs are the SSRCs of the video packets received.
	mu         sync.Mutex
	videoSSRCs map[uint32]int
	peerStates []serverMessage
	ended      chan string
	offers     atomic.Int64
	connected  chan struct{}
	conOnce    sync.Once
	opts       clientOptions
	closeCode  atomic.Int32
	closed     chan struct{}
	// lastOffer is the SDP of the last offer, as the server sent it.
	lastOffer atomic.Value
	// vseq and vts number the video packets the client writes.
	vseq uint16
	vts  uint32
	// plis counts the PLI and FIR the client's video sender received; rembs
	// holds the bitrates of the REMB it received.
	plis  atomic.Int64
	rembs []float32
	errs  []serverMessage
	// candidates are the candidate lines the server sent.
	candidates []string
}

// dial connects a client to the signalling socket and starts the exchange.
func (r *rig) dial(name, url string, opts clientOptions) *client {
	r.t.Helper()
	ctx, cancel := context.WithCancel(r.t.Context())
	c := &client{t: r.t, name: name, ctx: ctx, stop: cancel, opts: opts, videoSSRCs: map[uint32]int{},
		ended: make(chan string, 1), connected: make(chan struct{}), closed: make(chan struct{})}
	ws, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		r.t.Fatalf("%s: dial: %v", name, err)
	}
	c.ws = ws
	c.newPeerConnection()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer close(c.closed)
		c.readLoop()
	}()
	r.t.Cleanup(c.close)
	return c
}

// newPeerConnection makes the client's PeerConnection.
func (c *client) newPeerConnection() {
	me := &webrtc.MediaEngine{}
	if err := me.RegisterDefaultCodecs(); err != nil {
		c.t.Fatal(err)
	}
	se := webrtc.SettingEngine{}
	se.SetIncludeLoopbackCandidate(true)
	networks := c.opts.networks
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
		if cand == nil || c.opts.noCandidates {
			return
		}
		init := cand.ToJSON()
		c.send(map[string]any{"type": "candidate", "candidate": init.Candidate, "sdpMid": init.SDPMid, "sdpMLineIndex": init.SDPMLineIndex})
	})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateConnected {
			c.conOnce.Do(func() { close(c.connected) })
		}
	})
}

// count records a packet received.
func (c *client) count(remote *webrtc.TrackRemote, pkt *rtp.Packet) {
	if remote.Kind() == webrtc.RTPCodecTypeAudio {
		c.audioIn.Add(1)
		return
	}
	c.videoIn.Add(1)
	c.mu.Lock()
	c.videoSSRCs[pkt.SSRC]++
	c.mu.Unlock()
}

// send writes a JSON message to the signalling socket.
func (c *client) send(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		c.t.Errorf("%s: %v", c.name, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	if err := c.ws.Write(ctx, websocket.MessageText, data); err != nil && c.ctx.Err() == nil {
		c.t.Logf("%s: write: %v", c.name, err)
	}
}

// readLoop answers the offers and applies the candidates of the server.
func (c *client) readLoop() {
	for {
		_, data, err := c.ws.Read(c.ctx)
		if err != nil {
			c.closeCode.Store(int32(websocket.CloseStatus(err)))
			return
		}
		var msg serverMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			c.t.Errorf("%s: bad message %q", c.name, data)
			return
		}
		switch msg.Type {
		case msgOffer:
			c.offers.Add(1)
			c.lastOffer.Store(msg.SDP)
			c.answerOffer(msg.SDP)
		case msgError:
			c.mu.Lock()
			c.errs = append(c.errs, msg)
			c.mu.Unlock()
		case msgCandidate:
			c.mu.Lock()
			c.candidates = append(c.candidates, msg.Candidate)
			c.mu.Unlock()
			init := webrtc.ICECandidateInit{Candidate: msg.Candidate, SDPMid: msg.SDPMid, SDPMLineIndex: msg.SDPMLineIndex}
			if err := c.pc.AddICECandidate(init); err != nil {
				c.t.Logf("%s: AddICECandidate: %v", c.name, err)
			}
		case msgPeerState:
			c.mu.Lock()
			c.peerStates = append(c.peerStates, msg)
			c.mu.Unlock()
		case msgEnded:
			select {
			case c.ended <- msg.Reason:
			default:
			}
		}
	}
}

// errorf fails the test with a problem of the client, unless the client is
// being closed (the end of a test closes the PeerConnection while an answer
// may still be under way).
func (c *client) errorf(format string, args ...any) {
	if c.ctx.Err() != nil {
		return
	}
	c.t.Errorf(c.name+": "+format, args...)
}

// answerOffer applies an offer, adds the client's own tracks to the
// transceivers it made and sends the answer.
func (c *client) answerOffer(sdp string) {
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
		defer c.readSenderRTCP()
		if !c.opts.noVideo {
			video, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}, "video", c.name)
			if err != nil {
				c.t.Fatal(err)
			}
			c.video.Store(video)
			if _, err := c.pc.AddTrack(video); err != nil {
				c.errorf("AddTrack video: %v", err)
			}
		}
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
	c.send(map[string]any{"type": "answer", "sdp": answer.SDP})
}

// readSenderRTCP reads what the server says about the video the client sends:
// requests for key frames.
func (c *client) readSenderRTCP() {
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

// startSending writes a sample of each track every 20 ms (audio) and 33 ms
// (video) until the client closes. Writes before the connection is up are
// dropped by the track.
func (c *client) startSending() {
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
				c.t.Logf("%s: audio sample: %v", c.name, err)
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
					c.t.Logf("%s: video sample: %v", c.name, err)
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
					c.t.Logf("%s: video packet: %v", c.name, err)
				}
			}
		}
	}()
}

// close stops the client and waits for its goroutines.
func (c *client) close() {
	c.stop()
	_ = c.ws.CloseNow()
	_ = c.pc.Close()
	c.wg.Wait()
}

// waitFor polls cond until it is true or the deadline passes.
func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", d, what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// receiving waits until the client has received at least n audio and n video
// packets since the counters were at the given values.
func (c *client) receiving(t *testing.T, n int64, d time.Duration) {
	t.Helper()
	a0, v0 := c.audioIn.Load(), c.videoIn.Load()
	waitFor(t, c.name+" to receive audio and video", d, func() bool {
		return c.audioIn.Load()-a0 >= n && c.videoIn.Load()-v0 >= n
	})
}

var _ = io.EOF
