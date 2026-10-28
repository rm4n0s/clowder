package calls

import (
	"context"
	"log/slog"
	"time"

	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/metrics"
)

// The defaults of the durations and limits of Config (SPEC D64).
const (
	// DefaultICETimeout is how long a participant has to connect, counted from
	// its answer to the offer of the server.
	DefaultICETimeout = 10 * time.Second
	// DefaultDisconnectTimeout is how long the transport of a participant may
	// stay disconnected or failed before it is closed and the call ends.
	DefaultDisconnectTimeout = 15 * time.Second
	// DefaultReconnectGrace is how long a participant of an active call has
	// to open a new signalling socket after its socket closed.
	DefaultReconnectGrace = 15 * time.Second
	// DefaultJoinTimeout is how long a participant has, from the moment the
	// call is answered, to answer the offer of the server.
	DefaultJoinTimeout = 30 * time.Second
	// DefaultMaxVideoBitrate is the most video, in bits per second, that a
	// sender may send (2.5 Mbit/s).
	DefaultMaxVideoBitrate = 2_500_000
	// DefaultPingInterval is how often the server pings a signalling socket,
	// and DefaultPingTimeout how long the client has to answer.
	DefaultPingInterval = 30 * time.Second
	DefaultPingTimeout  = 15 * time.Second
	// DefaultWriteTimeout is how long one write to a signalling socket may
	// take.
	DefaultWriteTimeout = 10 * time.Second
	// defaultREMBInterval is how often the limit of video is announced.
	defaultREMBInterval = time.Second
)

// Config holds the settings of the media server.
type Config struct {
	// Addr is the address to listen on, as host:port, such as ":7882": one
	// UDP port and one TCP port with that number (SPEC D64). A port of 0
	// chooses one; Server.Addr tells which.
	Addr string
	// PublicIP is announced as the address of the server in the host
	// candidates, when the server runs behind NAT; empty announces the
	// addresses of the machine.
	PublicIP string
	// IncludeLoopback makes the server announce loopback addresses too, which
	// the tests and a local trial on 127.0.0.1 need. A loopback PublicIP does
	// it as well.
	IncludeLoopback bool
	// ProcessID is the ID of this process, the one the service of the calls
	// records as the home of the calls it starts (SPEC D65). A socket whose
	// call has another home is relayed to that process (Relay). Empty means a
	// process that is alone: every call is its own.
	ProcessID string
	// Relay carries the signalling of the participants whose socket reached
	// this process while their call is held by another one (SPEC D65); nil
	// when the process runs alone. It needs ProcessID.
	Relay Relay
	// Logger receives the state changes and the failures; nil logs nothing.
	// Nothing of the media, no SDP and no candidate is logged at info level.
	Logger *slog.Logger
	// ICETimeout, DisconnectTimeout, ReconnectGrace and JoinTimeout are the
	// deadlines of SPEC D64; zero takes the default. Tests make them short.
	ICETimeout        time.Duration
	DisconnectTimeout time.Duration
	ReconnectGrace    time.Duration
	JoinTimeout       time.Duration
	// MaxVideoBitrate is the limit of video per sender, in bits per second;
	// zero takes the default.
	MaxVideoBitrate uint64
	// SocketsOpen counts the signalling sockets that are open (the gauge of
	// kind "call" of clowder_websockets_open, SPEC D60). Nil counts nothing.
	SocketsOpen *metrics.Gauge
	// Outcomes counts the calls whose media this process held, when each
	// closes, by how it ended (clowder_calls_total); its one label is the
	// outcome. Nil counts nothing.
	Outcomes *metrics.CounterVec
	// PingInterval, PingTimeout and WriteTimeout bound a signalling socket;
	// zero takes the default.
	PingInterval time.Duration
	PingTimeout  time.Duration
	WriteTimeout time.Duration
}

// withDefaults fills the zero values with the defaults.
func (c Config) withDefaults() Config {
	def := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&c.ICETimeout, DefaultICETimeout)
	def(&c.DisconnectTimeout, DefaultDisconnectTimeout)
	def(&c.ReconnectGrace, DefaultReconnectGrace)
	def(&c.JoinTimeout, DefaultJoinTimeout)
	def(&c.PingInterval, DefaultPingInterval)
	def(&c.PingTimeout, DefaultPingTimeout)
	def(&c.WriteTimeout, DefaultWriteTimeout)
	if c.MaxVideoBitrate == 0 {
		c.MaxVideoBitrate = DefaultMaxVideoBitrate
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.DiscardHandler)
	}
	return c
}

// Status is what the media server asks about a call.
type Status struct {
	// Open is true while the call is ringing or active.
	Open bool
	// Active is true once the callee has answered and while the call is open.
	Active bool
	// Reason is how the call ended (hangup, declined, failed, ...); empty
	// while it is open.
	Reason string
}

// Controller is what the media server asks of the service of the calls
// (package app implements it): the state of a call, and the changes that the
// media server causes. It makes no permission check of its own; the user has
// been checked before the socket was attached.
type Controller interface {
	// Status reads the state of the call. A call that does not exist is not
	// open.
	Status(ctx context.Context, callID uint64) (Status, error)
	// Fail ends the call as failed: its media did not connect or was lost.
	Fail(ctx context.Context, callID uint64) error
	// SetMedia tells both users that a participant turned the microphone or the
	// camera on or off.
	SetMedia(ctx context.Context, callID, userID uint64, microphone, camera bool) error
	// Leave is a participant ending the call from the signalling socket: the
	// caller cancels a ringing call and either side hangs up an active one.
	Leave(ctx context.Context, callID, userID uint64) error
}

// Events delivers the live events of the topics (the hub of package live is
// one). The media server listens to the user topics of the two participants of
// each call for the end of the call and for the end of a session.
type Events interface {
	// Subscribe returns a subscription to the topics that ends with ctx.
	Subscribe(ctx context.Context, topics ...string) *live.Subscription
}
