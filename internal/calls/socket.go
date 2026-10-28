package calls

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	// readLimit is the largest message a client may send.
	readLimit = 64 << 10
	// socketQueue is how many messages wait for the writer of a socket; a
	// client that lets more pile up is dropped.
	socketQueue = 64
)

// The close codes of a signalling socket besides the standard ones. 4001 and
// 4002 are those of the WebSocket of the API (SPEC D44).
const (
	// CloseSessionEnded is the code of a socket whose session is gone.
	CloseSessionEnded websocket.StatusCode = 4001
	// CloseSessionChanged is the code of a socket whose session changed (the
	// role of the user); a client may join again.
	CloseSessionChanged websocket.StatusCode = 4002
	// closeReplaced is the code of a socket that a newer socket of the same
	// participant replaced.
	closeReplaced websocket.StatusCode = 4003
)

// socket is the signalling WebSocket of one participant. A goroutine writes
// the messages in order, another pings the client, and the handler of Serve
// reads; Close of the server waits for all three.
type socket struct {
	conn    *websocket.Conn
	userID  uint64
	session func(ctx context.Context) (websocket.StatusCode, string)
	logger  *slog.Logger
	cfg     Config

	mu      sync.Mutex
	closing bool
	code    websocket.StatusCode
	reason  string
	out     chan serverMessage
	// gone is closed when the connection can no longer be written to, so
	// that the pinger and Serve stop.
	gone     chan struct{}
	goneOnce sync.Once
	// writerDone is closed when the writer has closed the connection.
	writerDone chan struct{}
}

// newSocket makes the socket of a connection; the writer is started by
// start.
func newSocket(conn *websocket.Conn, j Join, cfg Config) *socket {
	return &socket{
		conn: conn, userID: j.UserID, session: j.Session, logger: cfg.Logger.With("user_id", j.UserID, "call_id", j.CallID),
		cfg: cfg, out: make(chan serverMessage, socketQueue), gone: make(chan struct{}), writerDone: make(chan struct{}),
	}
}

// send queues a message for the client. A client whose queue is full is too
// slow: the socket is closed. A closing socket sends nothing.
func (k *socket) send(m serverMessage) {
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

// finish closes the socket after the queued messages are written: the writer
// closes the connection with the code and the reason. The first call decides
// them.
func (k *socket) finish(code websocket.StatusCode, reason string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.closeLocked(code, reason)
}

// closeLocked is finish with the lock held.
func (k *socket) closeLocked(code websocket.StatusCode, reason string) {
	if k.closing {
		return
	}
	k.closing = true
	k.code, k.reason = code, reason
	close(k.out)
}

// abort drops the connection without the closing handshake.
func (k *socket) abort() {
	k.finish(websocket.StatusGoingAway, "going away")
	k.goneOnce.Do(func() { close(k.gone) })
	if err := k.conn.CloseNow(); err != nil {
		k.logger.Debug("closing the signalling socket failed", "err", err)
	}
}

// writeLoop writes the queued messages until the socket is closed, then closes
// the connection with the code of the closing.
func (k *socket) writeLoop(ctx context.Context) {
	defer close(k.writerDone)
	broken := false
	for m := range k.out {
		if broken {
			continue
		}
		data, err := json.Marshal(m)
		if err != nil {
			k.logger.Warn("encoding a signalling message failed", "err", err)
			continue
		}
		wctx, cancel := context.WithTimeout(ctx, k.cfg.WriteTimeout)
		err = k.conn.Write(wctx, websocket.MessageText, data)
		cancel()
		if err != nil {
			k.logger.Debug("writing to the signalling socket failed", "err", err)
			broken = true
			k.goneOnce.Do(func() { close(k.gone) })
			if err := k.conn.CloseNow(); err != nil {
				k.logger.Debug("closing the signalling socket failed", "err", err)
			}
		}
	}
	k.mu.Lock()
	code, reason := k.code, k.reason
	k.mu.Unlock()
	if broken {
		return
	}
	if err := k.conn.Close(code, reason); err != nil {
		k.logger.Debug("closing the signalling socket failed", "err", err)
	}
}

// pingLoop pings the client every interval; a client that does not answer
// within the timeout is dropped, and one that does gets its session checked
// again (SPEC D44), so that an expired session ends the socket.
func (k *socket) pingLoop(ctx context.Context) {
	t := time.NewTicker(k.cfg.PingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-k.gone:
			return
		case <-k.writerDone:
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, k.cfg.PingTimeout)
		err := k.conn.Ping(pctx)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				k.logger.Debug("signalling ping failed", "err", err)
				k.abort()
			}
			return
		}
		k.checkSession(ctx)
	}
}

// checkSession reads the session of the socket again; one that is gone ends
// the socket with the close code of D44.
func (k *socket) checkSession(ctx context.Context) {
	if k.session == nil {
		return
	}
	if code, reason := k.session(ctx); code != 0 {
		k.finish(code, reason)
	}
}
