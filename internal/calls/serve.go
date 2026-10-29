package calls

import (
	"context"
	"log/slog"
	"time"

	"github.com/coder/websocket"
	"github.com/rm4n0s/errors"
)

// Serve runs the signalling socket of one participant until it is over: the
// call has ended, the client or the server closed the connection, or the
// session ended. The handler that accepted the WebSocket calls it and returns
// when it does; Serve closes the connection. Only a user who is a participant
// of the call, and whom the service allows to join it now, may be given to
// Serve: it checks nothing of that itself.
//
// The server is the offerer: Serve joins the socket to the call, which makes
// the PeerConnection of the participant and sends the offer. The client
// answers, exchanges ICE candidates in both directions, sends its state
// (microphone and camera) and may hang up. A second socket of the same
// participant replaces the first, and with it the PeerConnection. A socket
// that closes while the call is active gives the participant the reconnect
// grace to open a new one before the call ends as failed.
//
// When the call is held by another process (Join.Home, SPEC D65) this process
// is the participant's edge: it keeps the socket, its pings, its session
// checks and its close codes, and relays the messages to the home through the
// Relay; the media of the participant goes straight to the home's own port.
func (s *Server) Serve(ctx context.Context, conn *websocket.Conn, j Join) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		closeConn(conn, websocket.StatusGoingAway, "going away", s.logger)
		return
	}
	s.serving.Add(1)
	s.mu.Unlock()
	defer s.serving.Done()
	s.cfg.SocketsOpen.Inc()
	defer s.cfg.SocketsOpen.Dec()

	conn.SetReadLimit(readLimit)
	// The socket does not end with the server's context: the call closes when
	// the server stops, tells the client and closes the socket.
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sock := newSocket(conn, j, s.cfg)
	started := s.goFunc(func() { sock.writeLoop(context.WithoutCancel(sctx)) })
	if !started {
		closeConn(conn, websocket.StatusGoingAway, "going away", s.logger)
		return
	}
	defer func() { <-sock.writerDone }()
	if !s.goFunc(func() { sock.pingLoop(sctx) }) {
		sock.finish(websocket.StatusGoingAway, "going away")
		return
	}

	if s.heldElsewhere(j) {
		s.serveEdge(sctx, sock, j)
		return
	}

	c, ok := s.callFor(j)
	if !ok {
		sock.finish(websocket.StatusGoingAway, "going away")
		return
	}
	attached := make(chan error, 1)
	c.post(func() { attached <- c.attach(sock) })
	select {
	case err := <-attached:
		if err != nil {
			s.logger.Debug("joining the call failed", "call_id", j.CallID, "user_id", j.UserID, "err", err)
			sock.send(serverMessage{Type: msgError, Code: codeOf(err), Message: "The call cannot be joined."})
			sock.finish(websocket.StatusInternalError, "cannot join")
			return
		}
	case <-c.done:
		sock.send(serverMessage{Type: msgEnded, Reason: orElse(c.reason, reasonEnded)})
		sock.finish(websocket.StatusNormalClosure, "call ended")
		return
	case <-sctx.Done():
		sock.finish(websocket.StatusGoingAway, "going away")
		return
	}
	s.readLoop(sctx, sock, j.CallID, func(msg clientMessage, _ []byte) {
		c.post(func() { c.onMessage(sock, msg) })
	})
	c.post(func() { c.socketClosed(sock) })
	sock.finish(websocket.StatusNormalClosure, "bye")
}

// readLoop reads the messages of the client and hands them to handle, until
// the connection ends. handle gets the message and the text it was parsed
// from, which is only good until it returns. A message that is JSON but not a
// message of the protocol is answered with an error message and the socket
// stays; a message that is not text closes it with 1003.
//
// A client may send at most messageBurst messages at once and messageRate a
// second after that (step 24.1): a real client sends a few dozen at the start
// (the answer and its ICE candidates) and a few a minute after. One that sends
// more is closed with 1008, so that it cannot make the server parse, answer,
// write to the database and publish events as fast as it can write.
func (s *Server) readLoop(ctx context.Context, sock *socket, callID uint64, handle func(msg clientMessage, raw []byte)) {
	limit := newMessageBucket(time.Now())
	for {
		typ, data, err := sock.conn.Read(ctx)
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Debug("signalling read ended", "call_id", callID, "user_id", sock.userID,
					"close_code", int(websocket.CloseStatus(err)))
			}
			return
		}
		if typ != websocket.MessageText {
			sock.finish(websocket.StatusUnsupportedData, "text messages only")
			return
		}
		if !limit.allow(time.Now()) {
			s.logger.Debug("a signalling socket sent too many messages", "call_id", callID, "user_id", sock.userID)
			sock.finish(websocket.StatusPolicyViolation, "too many messages")
			return
		}
		msg, err := parseClientMessage(data)
		if err != nil {
			sock.send(serverMessage{Type: msgError, Code: CodeInvalidMessage, Message: "That is not a message of the protocol."})
			continue
		}
		handle(msg, data)
	}
}

// codeOf is the code of the error message for a failure to join.
func codeOf(err error) string {
	if appErr, ok := errors.FromError(err); ok && appErr.Tag == TagNegotiationFailed {
		return CodeNegotiationFailed
	}
	return CodeInternal
}

// closeConn closes a connection that no socket owns, with the handshake.
func closeConn(conn *websocket.Conn, code websocket.StatusCode, reason string, logger *slog.Logger) {
	if err := conn.Close(code, reason); err != nil {
		logger.Debug("closing the signalling socket failed", "err", err)
	}
}
