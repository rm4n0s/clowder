package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/errors"
)

const (
	// wsReadLimit is the largest message a client may send (SPEC §7.13); a
	// bigger one closes the connection with 1009.
	wsReadLimit = 16 << 10
	// wsMaxTopics is the most topics a connection holds besides its own user
	// topic.
	wsMaxTopics = 200
	// wsWriteTimeout is how long one write to a client may take. A client that
	// does not read is closed when it expires.
	wsWriteTimeout = 10 * time.Second
	// wsPingInterval is how often the server pings a client, and wsPingTimeout
	// how long the client has to answer.
	wsPingInterval = 30 * time.Second
	wsPingTimeout  = 15 * time.Second
)

// The close codes the server sends besides the standard ones (SPEC §7.13).
const (
	// closeSessionEnded is sent when the session of the token is gone: a
	// logout, a disabled user, an expired session.
	closeSessionEnded websocket.StatusCode = 4001
	// closeSessionChanged is sent when the role of the user changed. A client
	// connects again.
	closeSessionChanged websocket.StatusCode = 4002
)

// The types of the messages (SPEC §7.13).
const (
	wsSubscribe   = "subscribe"
	wsUnsubscribe = "unsubscribe"
	wsPing        = "ping"
	wsAck         = "ack"
	wsError       = "error"
	wsPong        = "pong"
	wsEventType   = "event"
	wsResync      = "resync"
)

// The reasons the server gives when it closes a connection.
const (
	reasonGoingAway      = "going away"
	reasonSessionEnded   = "session ended"
	reasonSessionChanged = "session changed"
	reasonTryAgain       = "try again later"
	reasonInternal       = "internal error"
	reasonTextOnly       = "text messages only"
	reasonNotJSON        = "not JSON"
)

// The problems of a message of the client, as the "fields" of its error.
const (
	messageSocketNotObject  = "A message is one JSON object."
	messageSocketUnknown    = "Use subscribe, unsubscribe or ping."
	messageSocketNeedsID    = "Send the number of your request, the answer repeats it."
	messageSocketNoTopics   = "Name at least one topic."
	messageSocketUnexpected = "Only subscribe and unsubscribe have topics."
)

// wsClientMessage is a message of the client. The types subscribe and
// unsubscribe carry topics; every type carries the id the answer repeats.
type wsClientMessage struct {
	Type   string   `json:"type"`
	ID     *uint64  `json:"id"`
	Topics []string `json:"topics"`
}

// wsAckMessage answers a subscribe or unsubscribe that worked.
type wsAckMessage struct {
	Type   string   `json:"type"`
	ID     uint64   `json:"id"`
	Topics []string `json:"topics"`
}

// wsErrorMessage answers a request that did not work, or tells the client that
// a topic was dropped. Topic names the topic the error is about, when it is
// about one.
type wsErrorMessage struct {
	Type  string      `json:"type"`
	ID    *uint64     `json:"id,omitempty"`
	Topic string      `json:"topic,omitempty"`
	Error errorObject `json:"error"`
}

// wsPongMessage answers a ping.
type wsPongMessage struct {
	Type string `json:"type"`
	ID   uint64 `json:"id"`
}

// wsEventMessage is a live event for the client: the event of the hub with the
// resource it concerns as the REST API returns it to this user.
type wsEventMessage struct {
	Type           string `json:"type"`
	Topic          string `json:"topic"`
	Kind           string `json:"kind"`
	RoomID         uint64 `json:"room_id,omitempty"`
	PostID         uint64 `json:"post_id,omitempty"`
	RootID         uint64 `json:"root_id,omitempty"`
	UserID         uint64 `json:"user_id,omitempty"`
	ConversationID uint64 `json:"conversation_id,omitempty"`
	MessageID      uint64 `json:"message_id,omitempty"`
	CallID         uint64 `json:"call_id,omitempty"`
	// Microphone and Camera are the state of the participant UserID in a
	// call.updated event: true when on.
	Microphone *bool `json:"microphone,omitempty"`
	Camera     *bool `json:"camera,omitempty"`
	// Ring is true on an event of the user's own topic that rings for the
	// user (SPEC §7.15): the client plays Sound, and shows a notification of
	// the system when Browser is true. The server decided it from the user's
	// preferences.
	Ring    bool   `json:"ring,omitempty"`
	Sound   string `json:"sound,omitempty"`
	Browser bool   `json:"browser,omitempty"`
	Data    any    `json:"data,omitempty"`
}

// wsResyncMessage tells the client that it missed events and must load again
// what it shows; the connection is closed right after it.
type wsResyncMessage struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

// wsFrame is what the reader goroutine hands to the loop: a message, or the
// error that ended the reading.
type wsFrame struct {
	typ  websocket.MessageType
	data []byte
	err  error
}

// wsEnd says how a connection ends: the close code and reason, or that the
// connection is already dead (the client went away, a write failed) and only
// has to be dropped.
type wsEnd struct {
	code   websocket.StatusCode
	reason string
	// gone is true when no close handshake is possible.
	gone bool
}

// wsSession is one open WebSocket of a user. Its loop is the only writer of
// messages; the reader and the pinger are goroutines that the loop starts and
// waits for.
type wsSession struct {
	s     *Server
	conn  *websocket.Conn
	actor app.Actor
	user  app.User
	// token is the bearer token of the connection, kept to read the session
	// again (SPEC D37). It is never logged.
	token string
	sub   *live.Subscription
	// topics are the topics the user subscribed to, by name; the loop is the
	// only one that changes them.
	topics map[string]wsTopic
	// lastNotification is the ID of the newest notification the connection has
	// seen, so that a notification.created event sends the ones after it.
	lastNotification uint64
	end              *wsEnd
	in, out          int
}

// newSession makes the session of a connection. sub already listens to the
// user topic.
func (s *Server) newSession(conn *websocket.Conn, c *caller, token string, sub *live.Subscription) *wsSession {
	return &wsSession{s: s, conn: conn, actor: c.actor, user: c.user, token: token, sub: sub, topics: map[string]wsTopic{}}
}

// wsRoutes are the routes of the WebSocket.
func (s *Server) wsRoutes() []Route {
	return []Route{
		s.route(http.MethodGet, "/ws", false, s.websocket),
	}
}

// websocket answers GET /ws: the bearer token has been checked, and the
// connection becomes a WebSocket (SPEC §7.13). The library checks the Origin of
// a browser; a desktop client sends none. The handler returns when the
// connection is over, and waits for its goroutines first.
func (s *Server) websocket(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	token, err := bearerToken(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	// Counted from here, so that Drain also waits for an upgrade that is under
	// way.
	s.socketOpened()
	defer s.socketClosed()
	conn, err := s.accept(w, r)
	if err != nil {
		// The library has answered the request.
		s.logger.Debug("websocket refused", "pattern", r.Pattern, "user_id", c.user.ID, "err", err)
		return
	}
	s.serveSocket(context.WithoutCancel(r.Context()), conn, c, token)
}

// accept upgrades the request. On a failure the library has already answered
// it (a plain text 4xx).
func (s *Server) accept(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return nil, errors.NewErr(tagSocketRejected, err)
	}
	return conn, nil
}

// serveSocket runs the connection until it is over. It owns the goroutines it
// starts and returns when they have ended.
func (s *Server) serveSocket(parent context.Context, conn *websocket.Conn, c *caller, token string) {
	started := time.Now()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	conn.SetReadLimit(wsReadLimit)
	// Subscribe before anything is read, so that no change falls between the
	// read and the subscription.
	sub := s.svc.Hub.Subscribe(ctx, live.UserTopic(c.user.ID))
	defer sub.Close()
	ws := s.newSession(conn, c, token, sub)
	// The user is online while a WebSocket of theirs is open (SPEC §7.15).
	s.svc.Presence.Connect(c.user.ID)
	defer s.svc.Presence.Disconnect(c.user.ID)
	s.logger.Debug("websocket connected", "user_id", c.user.ID)

	var wg sync.WaitGroup
	frames := make(chan wsFrame)
	pings := make(chan error, 1)
	if err := ws.start(ctx); err != nil {
		ws.failed(err)
	} else {
		wg.Add(2)
		go func() {
			defer wg.Done()
			ws.readLoop(ctx, frames)
		}()
		go func() {
			defer wg.Done()
			ws.pingLoop(ctx, pings)
		}()
		ws.loop(ctx, frames, pings)
	}
	ws.finish()
	cancel()
	wg.Wait()
	s.logger.Debug("websocket closed", "user_id", c.user.ID, "code", int(ws.closeCode()), "duration_ms",
		float64(time.Since(started))/float64(time.Millisecond), "messages_in", ws.in, "messages_out", ws.out)
}

// start reads what the connection needs to know before the first event: the
// newest notification.
func (ws *wsSession) start(ctx context.Context) error {
	page, err := ws.s.svc.Notifications.List(ctx, ws.actor, 0, 1)
	if err != nil {
		return err
	}
	if len(page.Notifications) > 0 {
		ws.lastNotification = page.Notifications[0].ID
	}
	return nil
}

// loop handles what comes until the connection is over: a message of the
// client, an event of the hub, the result of a ping. It sets ws.end.
func (ws *wsSession) loop(ctx context.Context, frames <-chan wsFrame, pings <-chan error) {
	for ws.end == nil {
		var err error
		select {
		case <-ctx.Done():
			ws.end = &wsEnd{code: websocket.StatusGoingAway, reason: reasonGoingAway}
			return
		case f := <-frames:
			err = ws.onFrame(ctx, f)
		case pingErr := <-pings:
			err = ws.onPing(ctx, pingErr)
		case ev, ok := <-ws.sub.C():
			if !ok {
				err = ws.onSubscriptionClosed(ctx)
				break
			}
			err = ws.onEvent(ctx, ev)
		}
		if err != nil {
			ws.failed(err)
		}
	}
}

// onSubscriptionClosed handles the end of the hub subscription: the client was
// too slow or the bridge to Redis reconnected (resync, then 1013), or the hub
// was closed because the server stops (1001).
func (ws *wsSession) onSubscriptionClosed(ctx context.Context) error {
	reason := ws.sub.DropReason()
	if reason == "" {
		ws.end = &wsEnd{code: websocket.StatusGoingAway, reason: reasonGoingAway}
		return nil
	}
	ws.end = &wsEnd{code: websocket.StatusTryAgainLater, reason: reasonTryAgain}
	return ws.send(ctx, wsResyncMessage{Type: wsResync, Reason: reason})
}

// onPing handles the result of a ping: a client that did not answer is
// dropped; one that did gets its session read again, so that an expired
// session ends the connection and a live one slides.
func (ws *wsSession) onPing(ctx context.Context, pingErr error) error {
	if pingErr != nil {
		ws.s.logger.Debug("websocket ping failed", "user_id", ws.user.ID, "err", pingErr)
		ws.end = &wsEnd{gone: true}
		return nil
	}
	return ws.checkSession(ctx)
}

// failed handles the error of a step of the loop: a connection that can no
// longer be written to is dropped; anything else is logged and closes the
// connection with 1011, so that the client connects again and loads what it
// shows.
func (ws *wsSession) failed(err error) {
	if ws.end != nil {
		return
	}
	if appErr, ok := errors.FromError(err); ok && appErr.Tag == tagSocketWriteFailed {
		ws.s.logger.Debug("websocket write failed", "user_id", ws.user.ID, "err", err)
		ws.end = &wsEnd{gone: true}
		return
	}
	ws.s.logger.Warn("websocket failed", "user_id", ws.user.ID, "err", err)
	ws.end = &wsEnd{code: websocket.StatusInternalError, reason: reasonInternal}
}

// finish closes the connection the way ws.end says.
func (ws *wsSession) finish() {
	if ws.end == nil {
		ws.end = &wsEnd{code: websocket.StatusGoingAway, reason: reasonGoingAway}
	}
	if ws.end.gone {
		if err := ws.conn.CloseNow(); err != nil && !errors.Is(err, net.ErrClosed) {
			ws.s.logger.Debug("closing the websocket failed", "user_id", ws.user.ID, "err", errors.NewErr(tagSocketCloseFailed, err))
		}
		return
	}
	if err := ws.closeConn(ws.end.code, ws.end.reason); err != nil {
		ws.s.logger.Debug("closing the websocket failed", "user_id", ws.user.ID, "err", err)
	}
}

// closeCode is the code the connection ended with, 0 for one that was dropped.
func (ws *wsSession) closeCode() websocket.StatusCode {
	if ws.end == nil || ws.end.gone {
		return 0
	}
	return ws.end.code
}

// closeConn does the close handshake with the code and reason. A connection
// that is closed already is not an error.
func (ws *wsSession) closeConn(code websocket.StatusCode, reason string) error {
	if err := ws.conn.Close(code, reason); err != nil && !errors.Is(err, net.ErrClosed) {
		return errors.NewErr(tagSocketCloseFailed, err).SetMetadata("code", int(code))
	}
	return nil
}

// readLoop reads the messages of the client and hands them to the loop until
// the connection ends or ctx is done. The error that ends the reading is
// handed over too.
func (ws *wsSession) readLoop(ctx context.Context, out chan<- wsFrame) {
	for {
		f := ws.read(ctx)
		select {
		case out <- f:
		case <-ctx.Done():
			return
		}
		if f.err != nil {
			return
		}
	}
}

// read reads one message. The library closes the connection itself with 1009
// for a message over the limit.
func (ws *wsSession) read(ctx context.Context) wsFrame {
	typ, data, err := ws.conn.Read(ctx)
	if err != nil {
		return wsFrame{err: errors.NewErr(tagSocketReadFailed, err)}
	}
	return wsFrame{typ: typ, data: data}
}

// pingLoop pings the client every interval and hands the result to the loop,
// until a ping fails or ctx is done.
func (ws *wsSession) pingLoop(ctx context.Context, out chan<- error) {
	ticker := time.NewTicker(ws.s.pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		err := ws.ping(ctx)
		select {
		case out <- err:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

// ping pings the client and waits for the answer for the ping timeout.
func (ws *wsSession) ping(ctx context.Context) error {
	pctx, cancel := context.WithTimeout(ctx, ws.s.pingTimeout)
	defer cancel()
	if err := ws.conn.Ping(pctx); err != nil {
		return errors.NewErr(tagSocketPingFailed, err)
	}
	return nil
}

// send writes one message to the client. The write has the write timeout; the
// loop is the only caller.
func (ws *wsSession) send(ctx context.Context, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return errors.NewErr(tagSocketEncodeFailed, err)
	}
	wctx, cancel := context.WithTimeout(ctx, ws.s.writeTimeout)
	defer cancel()
	if err := ws.conn.Write(wctx, websocket.MessageText, data); err != nil {
		return errors.NewErr(tagSocketWriteFailed, err)
	}
	ws.out++
	return nil
}

// sendProblem answers a request that failed with err as an error message.
// A failure of the server itself is logged and sent as internal, without its
// cause, and the connection stays open.
func (ws *wsSession) sendProblem(ctx context.Context, id *uint64, topic string, err error) error {
	p, internal := problemOf(err)
	if internal {
		ws.s.logger.Warn("websocket request failed", "user_id", ws.user.ID, "err", err)
	}
	return ws.send(ctx, wsErrorMessage{Type: wsError, ID: id, Topic: shortTopic(topic), Error: p.object})
}

// maxTopicEcho is the longest topic name an error message repeats.
const maxTopicEcho = 64

// shortTopic cuts a topic name a client sent, so that an error never echoes a
// long text.
func shortTopic(name string) string {
	if len(name) > maxTopicEcho {
		return name[:maxTopicEcho]
	}
	return name
}

// onFrame handles what the reader read.
func (ws *wsSession) onFrame(ctx context.Context, f wsFrame) error {
	if f.err != nil {
		// The client closed the connection, or it broke, or the message was too
		// big and the library closed it with 1009.
		code := websocket.CloseStatus(f.err)
		ws.s.logger.Debug("websocket read ended", "user_id", ws.user.ID, "close_code", int(code), "err", f.err)
		ws.end = &wsEnd{gone: true}
		return nil
	}
	if f.typ != websocket.MessageText {
		ws.end = &wsEnd{code: websocket.StatusUnsupportedData, reason: reasonTextOnly}
		return nil
	}
	ws.in++
	msg, err := parseClientMessage(f.data)
	if err != nil {
		if appErr, ok := errors.FromError(err); ok && appErr.Tag == tagSocketNotJSON {
			ws.end = &wsEnd{code: websocket.StatusUnsupportedData, reason: reasonNotJSON}
			return nil
		}
		return ws.sendProblem(ctx, msg.ID, "", err)
	}
	switch msg.Type {
	case wsPing:
		return ws.send(ctx, wsPongMessage{Type: wsPong, ID: *msg.ID})
	case wsSubscribe:
		return ws.subscribe(ctx, msg)
	default:
		return ws.unsubscribe(ctx, msg)
	}
}

// parseClientMessage reads a message of the client. A text that is not one JSON
// value fails with tagSocketNotJSON, which closes the connection; one that is
// JSON but not a message (not an object, an unknown field or type, a wrong
// type of a field, no id, no topics where they are needed) fails with
// tagSocketMessageInvalid and the metadata "fields", and is answered with an
// error message. The message comes back as far as it could be read, so that
// the answer can repeat the id.
func parseClientMessage(data []byte) (wsClientMessage, error) {
	var msg wsClientMessage
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&msg); err != nil {
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return msg, errors.NewErr(tagSocketNotJSON, err)
		}
		fields := decodeProblems(err)
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) && typeErr.Field == "" {
			fields = map[string]string{"body": messageSocketNotObject}
		}
		if fields["id"] != "" {
			// An id that is not a number is not repeated.
			msg.ID = nil
		}
		return msg, errors.NewErr(tagSocketMessageInvalid, err).SetMetadata("fields", fields)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return msg, errors.New(tagSocketNotJSON, "the message is more than one JSON value")
	}
	switch msg.Type {
	case wsSubscribe, wsUnsubscribe, wsPing:
	default:
		return msg, errors.New(tagSocketMessageInvalid, "the type of the message is not known",
			"fields", map[string]string{"type": messageSocketUnknown})
	}
	if msg.ID == nil {
		return msg, errors.New(tagSocketMessageInvalid, "the message has no id",
			"fields", map[string]string{"id": messageSocketNeedsID})
	}
	if msg.Type == wsPing && msg.Topics != nil {
		return msg, errors.New(tagSocketMessageInvalid, "a ping has no topics",
			"fields", map[string]string{"topics": messageSocketUnexpected})
	}
	if msg.Type != wsPing && len(msg.Topics) == 0 {
		return msg, errors.New(tagSocketMessageInvalid, "the message names no topics",
			"fields", map[string]string{"topics": messageSocketNoTopics})
	}
	return msg, nil
}

// socketOpened counts an open connection, for Drain.
func (s *Server) socketOpened() {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	s.wsActive++
	s.wsBroadcastLocked()
}

// socketClosed counts a connection that is over.
func (s *Server) socketClosed() {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	s.wsActive--
	s.wsBroadcastLocked()
}

// wsBroadcastLocked wakes the callers of Drain. The caller holds s.wsMu.
func (s *Server) wsBroadcastLocked() {
	close(s.wsChanged)
	s.wsChanged = make(chan struct{})
}

// Drain waits until every WebSocket has ended, or ctx is done, and reports
// whether none is left. An upgraded connection belongs to no http.Server, so
// http.Server.Shutdown does not wait for it: the server closes the hub first
// (every connection then closes itself with 1001) and calls Drain, so that the
// process does not end before the clients got their close frame.
func (s *Server) Drain(ctx context.Context) bool {
	for {
		s.wsMu.Lock()
		active, changed := s.wsActive, s.wsChanged
		s.wsMu.Unlock()
		if active == 0 {
			return true
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return false
		}
	}
}

const (
	tagSocketRejected       = "SocketRejected"
	tagSocketReadFailed     = "SocketReadFailed"
	tagSocketWriteFailed    = "SocketWriteFailed"
	tagSocketEncodeFailed   = "SocketEncodeFailed"
	tagSocketPingFailed     = "SocketPingFailed"
	tagSocketCloseFailed    = "SocketCloseFailed"
	tagSocketNotJSON        = "SocketNotJSON"
	tagSocketMessageInvalid = "SocketMessageInvalid"
)
