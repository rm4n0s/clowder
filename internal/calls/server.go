package calls

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"

	"github.com/coder/websocket"
	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"
	"github.com/rm4n0s/errors"
)

// Join says which call a signalling socket joins and who joins it. The web and
// the API handlers fill it after they have authenticated the user and found,
// with the service of the calls, that the user is a participant of the call
// and may join it now.
type Join struct {
	// CallID is the call and ConversationID its direct conversation.
	CallID         uint64
	ConversationID uint64
	// CallerID and CalleeID are the two participants; UserID, who joins, is
	// one of them.
	CallerID uint64
	CalleeID uint64
	UserID   uint64
	// Home is the ID of the process that holds the media of the call (SPEC
	// D65). When it is not this process (Config.ProcessID) the socket is
	// relayed to it; empty means this process.
	Home string
	// Session checks the session of the socket again, as D44 says. It returns
	// 0 while the session is good, else the close code that ends the socket
	// (4001 session ended, 4002 session changed) and its reason. Nil means
	// the session is never checked.
	Session func(ctx context.Context) (code websocket.StatusCode, reason string)
}

// Server is the media server: it holds the UDP and TCP ports of the calls,
// the one webrtc.API that makes every PeerConnection, and the calls that are
// being forwarded. Make it with New and stop it with Close, which closes every
// PeerConnection and socket and waits for every goroutine it started.
type Server struct {
	cfg    Config
	logger *slog.Logger
	ctl    Controller
	events Events
	api    *webrtc.API

	udpMux  io.Closer
	tcpMux  io.Closer
	udpAddr net.Addr
	tcpAddr net.Addr

	ctx    context.Context
	cancel context.CancelFunc
	// wg counts the goroutines of the calls, the sockets and the forwarders.
	wg sync.WaitGroup
	// serving counts the handlers inside Serve.
	serving sync.WaitGroup

	mu     sync.Mutex
	calls  map[uint64]*call
	closed bool
	// edges are the sockets of this process whose call is held by another
	// one, by socket ID (relay.go).
	edges map[string]*edgeSocket
	// relay is Config.Relay, nil when the process runs alone.
	relay Relay
}

// New opens the UDP and TCP ports of cfg.Addr, makes the one webrtc.API of the
// server and returns the server. ctx owns the server: when it is done the
// calls are closed, and Close must still be called to wait for them. It fails
// with TagListenFailed when the ports cannot be opened and with TagSetupFailed
// when the API cannot be made.
func New(ctx context.Context, cfg Config, ctl Controller, events Events) (*Server, error) {
	cfg = cfg.withDefaults()
	if cfg.Relay != nil && cfg.ProcessID == "" {
		return nil, errors.New(TagSetupFailed, "a relay needs the ID of the process", "step", "relay")
	}
	ports, err := listenMedia(ctx, cfg.Addr, cfg.Logger)
	if err != nil {
		return nil, err
	}
	engine := webrtc.SettingEngine{}
	engine.LoggerFactory = slogFactory{logger: cfg.Logger}
	engine.SetICEUDPMux(ports.udp)
	engine.SetICETCPMux(ports.tcp)
	// pion offers UDP only unless it is told otherwise; ICE-TCP on the same
	// port is for networks that block UDP (SPEC D64).
	engine.SetNetworkTypes([]webrtc.NetworkType{
		webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6, webrtc.NetworkTypeTCP4, webrtc.NetworkTypeTCP6,
	})
	loopback := cfg.IncludeLoopback
	if cfg.PublicIP != "" {
		// The server announces the public address instead of its own
		// (pion's replacement for NAT1To1IPs).
		if err := engine.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{
			External: []string{cfg.PublicIP}, AsCandidateType: webrtc.ICECandidateTypeHost,
		}); err != nil {
			ports.close()
			return nil, errors.NewErr(TagSetupFailed, err).SetMetadata("step", "address rewrite", "public_ip", cfg.PublicIP)
		}
		if ip := net.ParseIP(cfg.PublicIP); ip != nil && ip.IsLoopback() {
			loopback = true
		}
	}
	engine.SetIncludeLoopbackCandidate(loopback)
	api, err := newAPI(serverCodecs(), engine)
	if err != nil {
		ports.close()
		return nil, err
	}
	sctx, cancel := context.WithCancel(ctx)
	s := &Server{
		cfg: cfg, logger: cfg.Logger, ctl: ctl, events: events, api: api,
		udpMux: ports.udp, tcpMux: ports.tcp, udpAddr: ports.udpAddr, tcpAddr: ports.tcpAddr,
		ctx: sctx, cancel: cancel, calls: map[uint64]*call{}, edges: map[string]*edgeSocket{}, relay: cfg.Relay,
	}
	if s.relay != nil {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.runRelay(s.relay.Messages())
		}()
	}
	return s, nil
}

// Addr returns the UDP and TCP addresses the server listens on, which differ
// only when the configured port was 0 and the system chose.
func (s *Server) Addr() (udp, tcp net.Addr) {
	return s.udpAddr, s.tcpAddr
}

// Calls returns the number of calls the server is forwarding.
func (s *Server) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// Close stops the server: every call is closed (its PeerConnections and
// sockets), the ports are released and every goroutine that the server started
// has ended when it returns. It is safe to call twice. The service of the
// calls ends the calls in the database (Calls.EndOpen); the media server does
// not.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	s.serving.Wait()
	s.wg.Wait()
	var first error
	for _, c := range []io.Closer{s.tcpMux, s.udpMux} {
		if err := c.Close(); err != nil && first == nil {
			first = errors.NewErr(tagCloseFailed, err)
		}
	}
	return first
}

// remove forgets a call that has closed.
func (s *Server) remove(c *call) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls[c.id] == c {
		delete(s.calls, c.id)
	}
}

// goFunc runs f in a goroutine that Close waits for. It reports false, and
// runs nothing, when the server is closing.
func (s *Server) goFunc(f func()) bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		f()
	}()
	return true
}

// callFor returns the call of the join, making it and starting its goroutine
// when this is its first participant. It reports false when the server is
// closing.
func (s *Server) callFor(j Join) (*call, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, false
	}
	if c, ok := s.calls[j.CallID]; ok {
		return c, true
	}
	c := newCall(s, j)
	s.calls[j.CallID] = c
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		c.run()
	}()
	return c, true
}

// mediaPorts are the ports of the server.
type mediaPorts struct {
	udp     ice.UDPMux
	tcp     ice.TCPMux
	udpAddr net.Addr
	tcpAddr net.Addr
	closers []io.Closer
}

// close releases the ports.
func (m mediaPorts) close() {
	for _, c := range m.closers {
		_ = c.Close()
	}
}

// listenMedia opens the TCP port of addr and the UDP port with the same
// number, and makes the muxes that serve every PeerConnection through them
// (SPEC D64).
func listenMedia(ctx context.Context, addr string, logger *slog.Logger) (mediaPorts, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return mediaPorts{}, errors.NewErr(TagListenFailed, err).SetMetadata("addr", addr, "step", "parse")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return mediaPorts{}, errors.New(TagListenFailed, "the port is not a port", "addr", addr, "step", "parse")
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return mediaPorts{}, errors.NewErr(TagListenFailed, err).SetMetadata("addr", addr, "step", "tcp")
	}
	tcpAddr := ln.Addr()
	tcpPort := tcpAddr.(*net.TCPAddr).Port
	var ports mediaPorts
	ports.tcpAddr = tcpAddr
	factory := slogFactory{logger: logger}
	ports.tcp = webrtc.NewICETCPMux(factory.NewLogger("ice-tcp"), ln, 8)
	ports.closers = append(ports.closers, ports.tcp)

	ip := net.ParseIP(host)
	if ip != nil && !ip.IsUnspecified() {
		conn, err := lc.ListenPacket(ctx, "udp", net.JoinHostPort(host, strconv.Itoa(tcpPort)))
		if err != nil {
			ports.close()
			return mediaPorts{}, errors.NewErr(TagListenFailed, err).SetMetadata("addr", addr, "step", "udp")
		}
		mux := ice.NewUDPMuxDefault(ice.UDPMuxParams{Logger: factory.NewLogger("ice-udp"), UDPConn: conn})
		ports.udp, ports.udpAddr = mux, conn.LocalAddr()
		ports.closers = append(ports.closers, mux)
		return ports, nil
	}
	mux, err := ice.NewMultiUDPMuxFromPort(tcpPort, ice.UDPMuxFromPortWithLogger(factory.NewLogger("ice-udp")))
	if err != nil {
		ports.close()
		return mediaPorts{}, errors.NewErr(TagListenFailed, err).SetMetadata("addr", addr, "step", "udp")
	}
	ports.udp, ports.udpAddr = mux, &net.UDPAddr{IP: net.IPv6unspecified, Port: tcpPort}
	ports.closers = append(ports.closers, mux)
	return ports, nil
}

// codecSpec is a codec the server offers and the kind of media it is for.
type codecSpec struct {
	params webrtc.RTPCodecParameters
	kind   webrtc.RTPCodecType
}

// serverCodecs are the codecs the server offers (SPEC D63, narrowed in step
// 22.1): Opus for audio and VP8 for video, and nothing else, so that
// forwarding needs no transcoding and no choice between codecs. H.264 is not
// offered.
func serverCodecs() []codecSpec {
	return []codecSpec{
		{kind: webrtc.RTPCodecTypeAudio, params: webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
				SDPFmtpLine: "minptime=10;useinbandfec=1",
			},
			PayloadType: 111,
		}},
		{kind: webrtc.RTPCodecTypeVideo, params: webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType: webrtc.MimeTypeVP8, ClockRate: 90000,
				RTCPFeedback: []webrtc.RTCPFeedback{
					{Type: "goog-remb"}, {Type: "ccm", Parameter: "fir"},
					{Type: "nack"}, {Type: "nack", Parameter: "pli"},
				},
			},
			PayloadType: 96,
		}},
	}
}

// newAPI makes the one webrtc.API of the server: the codecs, pion's default
// interceptors (NACK, the RTCP reports, TWCC) and the setting engine.
func newAPI(codecs []codecSpec, engine webrtc.SettingEngine) (*webrtc.API, error) {
	me := &webrtc.MediaEngine{}
	for _, c := range codecs {
		if err := me.RegisterCodec(c.params, c.kind); err != nil {
			return nil, errors.NewErr(TagSetupFailed, err).SetMetadata("step", "codec", "mime", c.params.MimeType)
		}
	}
	registry := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(me, registry); err != nil {
		return nil, errors.NewErr(TagSetupFailed, err).SetMetadata("step", "interceptors")
	}
	return webrtc.NewAPI(webrtc.WithMediaEngine(me), webrtc.WithInterceptorRegistry(registry),
		webrtc.WithSettingEngine(engine)), nil
}

const (
	// TagListenFailed is the tag of a media address whose ports cannot be
	// opened.
	TagListenFailed = "MediaListenFailed"
	// TagSetupFailed is the tag of the failure to make the WebRTC API of the
	// media server.
	TagSetupFailed = "MediaSetupFailed"
	tagCloseFailed = "MediaCloseFailed"
)
