package calls

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/errors"
)

// The kinds of Envelope (SPEC D65). The first three go from the edge to the
// home of the call, the last two from the home to the edge.
const (
	// EnvJoin tells the home that a socket of a participant opened on the
	// edge; the home answers with its offer as for a socket of its own.
	EnvJoin = "join"
	// EnvClient carries a message that the client of the socket sent.
	EnvClient = "client"
	// EnvLeave tells the home that the socket closed.
	EnvLeave = "leave"
	// EnvServer carries a message for the client of the socket.
	EnvServer = "server"
	// EnvClose tells the edge to close the socket with a code and a reason.
	EnvClose = "close"
)

// relayTimeout bounds one send of the relay that is not tied to a request.
const relayTimeout = 2 * time.Second

// Relay carries the signalling between the processes of a cluster (SPEC D65,
// D41). A participant whose socket reached a process that does not hold the
// call is relayed to the home through it, one Envelope for each message in
// each direction. internal/cluster implements it with Redis, so that this
// package stays free of Redis; no media ever goes through it.
type Relay interface {
	// Send delivers the envelope to the process named to. It fails when the
	// transport fails or when no process listens there.
	Send(ctx context.Context, to string, env Envelope) error
	// Messages yields the envelopes addressed to this process, in the order
	// they arrived. It is closed when the relay is.
	Messages() <-chan Envelope
}

// Envelope is one message of the relay. The signalling message it carries is
// the JSON of the WebSocket, unchanged.
type Envelope struct {
	// Kind is one of the Env constants.
	Kind string `json:"kind"`
	// From is the ID of the process that sent the envelope: for a join, the
	// edge, to which the home answers.
	From string `json:"from"`
	// CallID and UserID say whose socket it is, and Socket which one: every
	// socket has an ID of its own, made by the edge, so that the home can tell
	// a socket from the one that replaced it.
	CallID uint64 `json:"call_id"`
	UserID uint64 `json:"user_id"`
	Socket string `json:"socket"`
	// Join is set for EnvJoin: the call, as the edge's handler checked it.
	Join *EnvelopeJoin `json:"join,omitempty"`
	// Message is the signalling message of EnvClient and EnvServer.
	Message json.RawMessage `json:"message,omitempty"`
	// Code and Reason are the close code and reason of EnvClose.
	Code   int    `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// EnvelopeJoin is the part of a Join that the home needs to know the call.
type EnvelopeJoin struct {
	ConversationID uint64 `json:"conversation_id"`
	CallerID       uint64 `json:"caller_id"`
	CalleeID       uint64 `json:"callee_id"`
}

// heldElsewhere reports whether the call of the join is held by another
// process than this one, so that its socket has to be relayed.
func (s *Server) heldElsewhere(j Join) bool {
	return j.Home != "" && s.cfg.ProcessID != "" && j.Home != s.cfg.ProcessID
}

// runRelay handles the envelopes addressed to this process one at a time, in
// order, until the server stops or the relay is closed. It never blocks on
// the call: work for a call is handed to the goroutine of the call.
func (s *Server) runRelay(in <-chan Envelope) {
	for {
		select {
		case <-s.ctx.Done():
			return
		case env, ok := <-in:
			if !ok {
				return
			}
			s.onRelay(env)
		}
	}
}

// onRelay handles one envelope.
func (s *Server) onRelay(env Envelope) {
	switch env.Kind {
	case EnvJoin:
		s.relayJoin(env)
	case EnvClient:
		s.relayClient(env)
	case EnvLeave:
		s.relayLeave(env)
	case EnvServer:
		s.relayServer(env)
	case EnvClose:
		s.relayClose(env)
	default:
		s.dropRelay(env, "unknown kind")
	}
}

// dropRelay logs an envelope that is dropped, and why. It is the only record
// of a message for a call that has ended, for a participant the home does not
// know or from a socket that was replaced (SPEC D65).
func (s *Server) dropRelay(env Envelope, why string) {
	s.logger.Debug("relay message dropped", "why", why, "kind", env.Kind, "call_id", env.CallID,
		"user_id", env.UserID, "socket", env.Socket, "from", env.From)
}

// callByID returns the call this process holds, or nil.
func (s *Server) callByID(id uint64) *call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[id]
}

// ---- the home ----

// relayJoin handles a socket that opened on an edge: it joins the call as a
// remote participant, which makes its PeerConnection and sends the offer back
// through the relay.
func (s *Server) relayJoin(env Envelope) {
	if env.Join == nil || env.Socket == "" || env.From == "" || env.CallID == 0 {
		s.dropRelay(env, "malformed join")
		return
	}
	j := Join{
		CallID: env.CallID, ConversationID: env.Join.ConversationID, CallerID: env.Join.CallerID,
		CalleeID: env.Join.CalleeID, UserID: env.UserID, Home: s.cfg.ProcessID,
	}
	if j.UserID != j.CallerID && j.UserID != j.CalleeID {
		s.dropRelay(env, "unknown participant")
		return
	}
	rs := &remoteSocket{
		s: s, edge: env.From, id: env.Socket, callID: env.CallID, userID: env.UserID,
		logger: s.logger.With("call_id", env.CallID, "user_id", env.UserID, "edge", env.From),
		out:    make(chan serverMessage, socketQueue), done: make(chan struct{}),
	}
	if !s.goFunc(rs.writeLoop) {
		return
	}
	c, ok := s.callFor(j)
	if !ok {
		rs.finish(websocket.StatusGoingAway, "going away")
		return
	}
	s.logger.Debug("relay: join received", "call_id", env.CallID, "user_id", env.UserID, "edge", env.From)
	attached := make(chan error, 1)
	c.post(func() { attached <- c.attach(rs) })
	if !s.goFunc(func() {
		select {
		case err := <-attached:
			if err != nil {
				s.logger.Debug("joining the call failed", "call_id", j.CallID, "user_id", j.UserID, "err", err)
				rs.send(serverMessage{Type: msgError, Code: codeOf(err), Message: "The call cannot be joined."})
				rs.finish(websocket.StatusInternalError, "cannot join")
			}
		case <-c.done:
			rs.send(serverMessage{Type: msgEnded, Reason: orElse(c.reason, reasonEnded)})
			rs.finish(websocket.StatusNormalClosure, "call ended")
		case <-s.ctx.Done():
			rs.finish(websocket.StatusGoingAway, "going away")
		}
	}) {
		rs.finish(websocket.StatusGoingAway, "going away")
	}
}

// relayClient hands a message of a remote participant to the call.
func (s *Server) relayClient(env Envelope) {
	c := s.callByID(env.CallID)
	if c == nil {
		s.dropRelay(env, "the call has ended or is not held here")
		return
	}
	msg, err := parseClientMessage(env.Message)
	if err != nil {
		s.dropRelay(env, "not a message of the protocol")
		return
	}
	c.post(func() {
		rs := c.remoteSocket(env)
		if rs == nil {
			return
		}
		s.logger.Debug("relay: client message received", "call_id", env.CallID, "user_id", env.UserID, "type", msg.Type)
		c.onMessage(rs, msg)
	})
}

// relayLeave notes that the socket of a remote participant closed, which for
// an active call starts the reconnect grace as for a socket of this process.
func (s *Server) relayLeave(env Envelope) {
	c := s.callByID(env.CallID)
	if c == nil {
		s.dropRelay(env, "the call has ended or is not held here")
		return
	}
	c.post(func() {
		rs := c.remoteSocket(env)
		if rs == nil {
			return
		}
		s.logger.Debug("relay: leave received", "call_id", env.CallID, "user_id", env.UserID)
		c.socketClosed(rs)
		// The edge has closed it already: stop the writer without a close.
		rs.finish(0, "")
	})
}

// remoteSocket returns the remote socket the envelope is from, when it is the
// current socket of its participant; otherwise the envelope is dropped (and
// logged) and the result is nil. It runs in the goroutine of the call.
func (c *call) remoteSocket(env Envelope) *remoteSocket {
	if c.ended {
		c.s.dropRelay(env, "the call has ended")
		return nil
	}
	p := c.partOf(env.UserID)
	if p == nil {
		c.s.dropRelay(env, "unknown participant")
		return nil
	}
	rs, ok := p.sock.(*remoteSocket)
	if !ok || rs.id != env.Socket {
		c.s.dropRelay(env, "the socket was replaced")
		return nil
	}
	return rs
}

// remoteSocket is the signalling channel of a participant whose WebSocket is on
// another process, the edge: what the call sends it goes to the edge through
// the relay, and the edge's reads come in as envelopes. It has a writer
// goroutine, like socket, so that a slow Redis does not stall the call.
type remoteSocket struct {
	s      *Server
	edge   string
	id     string
	callID uint64
	userID uint64
	logger *slog.Logger

	mu      sync.Mutex
	closing bool
	// code is the close code sent to the edge after the queued messages; 0
	// sends none.
	code   websocket.StatusCode
	reason string
	out    chan serverMessage
	// done is closed when the writer has ended.
	done chan struct{}
}

// uid implements signalSocket.
func (k *remoteSocket) uid() uint64 { return k.userID }

// send implements signalSocket. A queue that is full closes the socket.
func (k *remoteSocket) send(m serverMessage) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closing {
		return
	}
	select {
	case k.out <- m:
	default:
		k.closeLocked(websocket.StatusTryAgainLater, "too slow")
	}
}

// finish implements signalSocket: after the queued messages the edge is told to
// close its socket with the code and the reason; code 0 tells it nothing.
func (k *remoteSocket) finish(code websocket.StatusCode, reason string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.closeLocked(code, reason)
}

func (k *remoteSocket) closeLocked(code websocket.StatusCode, reason string) {
	if k.closing {
		return
	}
	k.closing = true
	k.code, k.reason = code, reason
	close(k.out)
}

// checkSession implements signalSocket. The edge holds the session of the
// socket and checks it (SPEC D44); the home has nothing to check.
func (k *remoteSocket) checkSession(context.Context) {}

// envelope makes an envelope to the edge of this socket.
func (k *remoteSocket) envelope(kind string) Envelope {
	return Envelope{Kind: kind, From: k.s.cfg.ProcessID, CallID: k.callID, UserID: k.userID, Socket: k.id}
}

// writeLoop sends the queued messages to the edge in order, then the close.
func (k *remoteSocket) writeLoop() {
	defer close(k.done)
	for m := range k.out {
		data, err := json.Marshal(m)
		if err != nil {
			k.logger.Warn("encoding a signalling message failed", "err", err)
			continue
		}
		env := k.envelope(EnvServer)
		env.Message = data
		if k.sendToEdge(env) {
			k.logger.Debug("relay: server message sent", "type", m.Type)
		}
	}
	k.mu.Lock()
	code, reason := k.code, k.reason
	k.mu.Unlock()
	if code == 0 {
		return
	}
	env := k.envelope(EnvClose)
	env.Code, env.Reason = int(code), reason
	if k.sendToEdge(env) {
		k.logger.Debug("relay: close sent", "code", int(code))
	}
}

// sendToEdge sends the envelope and says whether it was delivered. A failure
// is logged at the debug level: the edge may be gone, and the call goes on
// without that participant until its grace is over.
func (k *remoteSocket) sendToEdge(env Envelope) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(k.s.ctx), k.s.cfg.WriteTimeout)
	defer cancel()
	if err := k.s.relay.Send(ctx, k.edge, env); err != nil {
		k.logger.Debug("relay: sending to the edge failed", "kind", env.Kind, "err", err)
		return false
	}
	return true
}

// ---- the edge ----

// edgeSocket is a signalling WebSocket of this process whose call is held by
// another one: the process relays its messages to the home (SPEC D65).
type edgeSocket struct {
	id   string
	sock *socket
	j    Join
	// ended is set when the ended message has been sent to the client, so that
	// the one from the home and the one the edge makes itself do not both go.
	ended atomic.Bool
}

// deliver queues a message of the home for the client. The ended message goes
// once.
func (e *edgeSocket) deliver(m serverMessage) {
	if m.Type == msgEnded && !e.ended.CompareAndSwap(false, true) {
		return
	}
	e.sock.send(m)
}

// edgeRelay returns the relay of the process, or fails when it has none: a
// call held by another process cannot be joined then.
func (s *Server) edgeRelay(j Join) (Relay, error) {
	if s.relay == nil {
		return nil, errors.New(TagNoRelay, "the call is held by another process and this one has no relay",
			"call_id", j.CallID, "home", j.Home)
	}
	return s.relay, nil
}

// serveEdge runs the socket of a participant whose call is held by another
// process: it keeps what the socket needs here (the session checks, the
// close codes, the pings) and relays the rest to the home. It returns when the
// socket is over; Serve then waits for the writer.
func (s *Server) serveEdge(ctx context.Context, sock *socket, j Join) {
	relay, err := s.edgeRelay(j)
	if err != nil {
		s.logger.Warn("joining a call of another process failed", "err", err)
		sock.send(serverMessage{Type: msgError, Code: CodeInternal, Message: "The call cannot be joined."})
		sock.finish(websocket.StatusInternalError, "cannot join")
		return
	}
	e := &edgeSocket{id: rand.Text(), sock: sock, j: j}
	// The end of the server ends the socket too: the call is not here to close
	// it. The close handshake ends the read loop; cancelling its context would
	// drop the connection before the client has the code.
	stop := context.AfterFunc(s.ctx, func() { sock.finish(websocket.StatusGoingAway, "going away") })
	defer stop()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		sock.finish(websocket.StatusGoingAway, "going away")
		return
	}
	s.edges[e.id] = e
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.edges, e.id)
		s.mu.Unlock()
	}()

	// Listen before the state is read, so that the end of the call falls in
	// neither gap.
	sub := s.events.Subscribe(ctx, live.UserTopic(j.UserID))
	if !s.goFunc(func() { s.watchEdge(ctx, e, sub) }) {
		sub.Close()
		sock.finish(websocket.StatusGoingAway, "going away")
		return
	}
	if over := s.edgeCallOver(ctx, e); over {
		return
	}

	join := Envelope{
		Kind: EnvJoin, From: s.cfg.ProcessID, CallID: j.CallID, UserID: j.UserID, Socket: e.id,
		Join: &EnvelopeJoin{ConversationID: j.ConversationID, CallerID: j.CallerID, CalleeID: j.CalleeID},
	}
	if err := relay.Send(ctx, j.Home, join); err != nil {
		s.logger.Warn("joining a call of another process failed", "err", err, "call_id", j.CallID, "home", j.Home)
		sock.send(serverMessage{Type: msgError, Code: CodeInternal, Message: "The call cannot be joined."})
		sock.finish(websocket.StatusInternalError, "cannot join")
		return
	}
	s.logger.Debug("relay: join sent", "call_id", j.CallID, "user_id", j.UserID, "home", j.Home, "socket", e.id)

	s.readLoop(ctx, sock, j.CallID, func(msg clientMessage, raw []byte) {
		env := Envelope{Kind: EnvClient, From: s.cfg.ProcessID, CallID: j.CallID, UserID: j.UserID, Socket: e.id, Message: raw}
		if err := relay.Send(ctx, j.Home, env); err != nil {
			s.logger.Debug("relay: sending to the home failed", "type", msg.Type, "call_id", j.CallID, "err", err)
			return
		}
		s.logger.Debug("relay: client message sent", "call_id", j.CallID, "user_id", j.UserID, "type", msg.Type)
	})

	// Tell the home the socket is gone, so that it starts the grace of the
	// participant. The request that opened the socket may be over.
	lctx, lcancel := context.WithTimeout(context.WithoutCancel(ctx), relayTimeout)
	defer lcancel()
	leave := Envelope{Kind: EnvLeave, From: s.cfg.ProcessID, CallID: j.CallID, UserID: j.UserID, Socket: e.id}
	if err := relay.Send(lctx, j.Home, leave); err != nil {
		s.logger.Debug("relay: sending to the home failed", "kind", EnvLeave, "call_id", j.CallID, "err", err)
	} else {
		s.logger.Debug("relay: leave sent", "call_id", j.CallID, "user_id", j.UserID)
	}
	sock.finish(websocket.StatusNormalClosure, "bye")
}

// edgeCallOver reads the state of the call and, when it is over, tells the
// client so and closes the socket. It reports whether it did.
func (s *Server) edgeCallOver(ctx context.Context, e *edgeSocket) bool {
	st, err := s.ctl.Status(ctx, e.j.CallID)
	switch {
	case err != nil:
		s.logger.Warn("reading the state of the call failed", "err", err, "call_id", e.j.CallID)
		return false
	case st.Open:
		return false
	}
	e.deliver(serverMessage{Type: msgEnded, Reason: orElse(st.Reason, reasonEnded)})
	e.sock.finish(websocket.StatusNormalClosure, "call ended")
	return true
}

// watchEdge listens to the events of the user of an edge socket: a changed
// session is checked again (SPEC D44), and the end of the call is told to the
// client at once, which is also how it learns that its home stopped (the home
// cannot say). It ends with ctx.
func (s *Server) watchEdge(ctx context.Context, e *edgeSocket, sub *live.Subscription) {
	for {
		select {
		case <-ctx.Done():
			sub.Close()
			return
		case ev, ok := <-sub.C():
			if !ok {
				if sub.DropReason() == "" {
					return
				}
				// Events may have been missed: listen again and read the state.
				sub = s.events.Subscribe(ctx, live.UserTopic(e.j.UserID))
				s.edgeCallOver(ctx, e)
				continue
			}
			switch ev.Kind {
			case live.KindUserChanged:
				e.sock.checkSession(ctx)
			case live.KindCallEnded:
				if ev.CallID == e.j.CallID {
					s.edgeCallOver(ctx, e)
				}
			}
		}
	}
}

// relayServer hands a message of the home to the socket it is for.
func (s *Server) relayServer(env Envelope) {
	e := s.edge(env.Socket)
	if e == nil {
		s.dropRelay(env, "no such socket")
		return
	}
	var msg serverMessage
	if err := json.Unmarshal(env.Message, &msg); err != nil || msg.Type == "" {
		s.dropRelay(env, "not a message of the protocol")
		return
	}
	s.logger.Debug("relay: server message received", "call_id", env.CallID, "user_id", env.UserID, "type", msg.Type)
	e.deliver(msg)
}

// relayClose closes the socket the home says to close.
func (s *Server) relayClose(env Envelope) {
	e := s.edge(env.Socket)
	if e == nil {
		s.dropRelay(env, "no such socket")
		return
	}
	s.logger.Debug("relay: close received", "call_id", env.CallID, "user_id", env.UserID, "code", env.Code)
	e.sock.finish(websocket.StatusCode(env.Code), env.Reason)
}

// edge returns the socket of this process with the ID, or nil.
func (s *Server) edge(id string) *edgeSocket {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.edges[id]
}

// TagNoRelay is the tag of a socket whose call is held by another process, on a
// server that has no relay to reach it.
const TagNoRelay = "MediaNoRelay"
