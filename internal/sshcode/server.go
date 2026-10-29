package sshcode

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/errors"
	"golang.org/x/crypto/ssh"
)

// The limits of SPEC §7.16. A Config field that is zero takes the default.
const (
	// DefaultMaxConns is the most connections one process serves at once.
	DefaultMaxConns = 64
	// DefaultMaxConnsPerAddr is the most connections one client address may
	// hold at once (step 24.1): without it one address could keep every place
	// of DefaultMaxConns for the length of the handshake deadline and lock
	// everybody else out of the server. A person needs one connection at a
	// time.
	DefaultMaxConnsPerAddr = 8
	// DefaultHandshakeTimeout is how long a client has for the handshake,
	// the authentication included.
	DefaultHandshakeTimeout = 10 * time.Second
	// DefaultConnTimeout is the total time a connection lives.
	DefaultConnTimeout = 30 * time.Second
	// maxAuthTries is the number of authentication attempts per connection.
	maxAuthTries = 3
	// maxRefusedRequests is how many requests of a session are refused before
	// the session is closed: a client that sends endless env requests gets
	// nowhere.
	maxRefusedRequests = 8
	// serverVersion is the SSH version string, the banner of the server.
	serverVersion = "SSH-2.0-Clowder"
)

// The texts the client sees. They are fixed.
const (
	textOnlyCodes   = "This server only gives sign-in codes.\r\n"
	textNoCode      = "Could not make a code; try again later.\r\n"
	execCommandJSON = "json"
)

// SignIn is what the server asks of the services of internal/app.
// *app.SignIn satisfies it.
type SignIn interface {
	// KeyAllowed reports whether the key with the fingerprint belongs to the
	// enabled user with the username. It says nothing about why not.
	KeyAllowed(ctx context.Context, username, fingerprint string) bool
	// IssueCode makes a code for the user and the key, for the client at addr.
	IssueCode(ctx context.Context, username, fingerprint, addr string) (app.IssuedSSHCode, error)
}

// Limiter counts the failed logins of client addresses. *app.LoginLimiter
// satisfies it; it is the limiter of the web login too, so that the limit of
// SPEC §7.1 covers both.
type Limiter interface {
	// AllowAddr reports whether the address may connect.
	AllowAddr(ctx context.Context, addr string) bool
	// FailAddr records a failed login of the address.
	FailAddr(ctx context.Context, addr string)
}

// Config holds the settings of the server.
type Config struct {
	// HostKey is the host key of the server (see LoadHostKey).
	HostKey ssh.Signer
	// MaxConns is the most connections at once; more are closed when they
	// are accepted. Zero is DefaultMaxConns.
	MaxConns int
	// MaxConnsPerAddr is the most connections one client address may hold at
	// once; more are closed when they are accepted. Zero is
	// DefaultMaxConnsPerAddr.
	MaxConnsPerAddr int
	// HandshakeTimeout is the time for the handshake. Zero is
	// DefaultHandshakeTimeout.
	HandshakeTimeout time.Duration
	// ConnTimeout is the total time of a connection. Zero is
	// DefaultConnTimeout.
	ConnTimeout time.Duration
}

// Server is the SSH server of the sign-in codes. Make it with New and run it
// with Serve. It is safe for concurrent use.
type Server struct {
	cfg     Config
	signIn  SignIn
	limiter Limiter
	logger  *slog.Logger

	mu     sync.Mutex
	active int
	// byAddr counts the open connections of each client address.
	byAddr map[string]int
}

// New makes a server. A nil logger discards the log.
func New(cfg Config, signIn SignIn, limiter Limiter, logger *slog.Logger) *Server {
	if cfg.MaxConns <= 0 {
		cfg.MaxConns = DefaultMaxConns
	}
	if cfg.MaxConnsPerAddr <= 0 {
		cfg.MaxConnsPerAddr = DefaultMaxConnsPerAddr
	}
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = DefaultHandshakeTimeout
	}
	if cfg.ConnTimeout <= 0 {
		cfg.ConnTimeout = DefaultConnTimeout
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Server{cfg: cfg, signIn: signIn, limiter: limiter, logger: logger, byAddr: map[string]int{}}
}

// Serve accepts connections on ln until ctx is done. When ctx is done it
// closes ln and every open connection, waits for every goroutine it started
// and returns nil. It returns an error when ln fails for another reason, after
// it has closed the connections and waited the same way. The caller does not
// close ln.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	// The watcher closes the listener, which ends Accept, when ctx is done.
	// Each connection closes itself on ctx (see handle).
	wg.Go(func() {
		<-ctx.Done()
		_ = ln.Close()
	})
	s.logger.Info("the SSH server is listening", "addr", ln.Addr().String(),
		"hostKey", ssh.FingerprintSHA256(s.cfg.HostKey.PublicKey()))

	var serveErr error
	for {
		nc, err := ln.Accept()
		if err != nil {
			if ctx.Err() == nil {
				serveErr = errors.NewErr(tagAcceptFailed, err).SetMetadata("addr", ln.Addr().String())
			}
			break
		}
		host := hostOf(nc)
		switch s.track(host) {
		case trackFull:
			s.logger.Warn("an SSH connection was refused: too many connections", "max", s.cfg.MaxConns)
			_ = nc.Close()
			continue
		case trackAddrFull:
			s.logger.Info("an SSH connection was refused: the address holds too many", "addr", host,
				"max", s.cfg.MaxConnsPerAddr)
			_ = nc.Close()
			continue
		}
		wg.Go(func() { s.handle(ctx, nc, host) })
	}
	cancel()
	wg.Wait()
	return serveErr
}

// trackResult says whether track took a connection.
type trackResult int

const (
	trackOK trackResult = iota
	// trackFull is the answer when the server has its maximum already.
	trackFull
	// trackAddrFull is the answer when the address has its maximum already.
	trackAddrFull
)

// hostOf returns the client address of the connection without its port.
func hostOf(nc net.Conn) string {
	host, _, err := net.SplitHostPort(nc.RemoteAddr().String())
	if err != nil {
		return nc.RemoteAddr().String()
	}
	return host
}

// track counts a connection of the address as open, unless the server or the
// address has its maximum already.
func (s *Server) track(host string) trackResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active >= s.cfg.MaxConns {
		return trackFull
	}
	if s.byAddr[host] >= s.cfg.MaxConnsPerAddr {
		return trackAddrFull
	}
	s.active++
	s.byAddr[host]++
	return trackOK
}

// untrack counts a connection of the address as closed.
func (s *Server) untrack(host string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active--
	if s.byAddr[host]--; s.byAddr[host] <= 0 {
		delete(s.byAddr, host)
	}
}

// config makes the configuration of one connection: public-key
// authentication only, three attempts, and the callback that asks the service.
// ctx is the context of the connection; offered is set when the client offers
// a public key.
func (s *Server) config(ctx context.Context, offered *atomic.Bool) *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		MaxAuthTries:  maxAuthTries,
		ServerVersion: serverVersion,
		// The library answers the query of a client that only asks whether a
		// key would be accepted ("is this public key allowed for this user?")
		// with this callback, before the client has proved that it holds the
		// private key. Answering from the service there would tell anybody,
		// who needs only a public key (they are published), which keys belong
		// to which usernames. So this callback accepts every key, and the
		// check of the service is made after the signature was verified
		// (VerifiedPublicKeyCallback): a key is allowed or not only for the
		// one who signs with it (step 24.1).
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			offered.Store(true)
			return &ssh.Permissions{}, nil
		},
		VerifiedPublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey, _ *ssh.Permissions, _ string) (*ssh.Permissions, error) {
			fingerprint := ssh.FingerprintSHA256(key)
			if !s.signIn.KeyAllowed(ctx, conn.User(), fingerprint) {
				// The same answer for every reason, and the client is not
				// told which.
				return nil, errKeyNotAllowed
			}
			return &ssh.Permissions{Extensions: map[string]string{extFingerprint: fingerprint}}, nil
		},
	}
	cfg.AddHostKey(s.cfg.HostKey)
	return cfg
}

// extFingerprint is the key of the permissions that carries the fingerprint of
// the key that authenticated.
const extFingerprint = "fingerprint"

// errKeyNotAllowed is what the public-key callback returns; the SSH library
// only counts it, it does not send it. It is a sentinel and its text says
// nothing.
var errKeyNotAllowed = &errors.Error{Tag: tagKeyNotAllowed, Message: "key not allowed"}

// handle serves one connection and closes it.
func (s *Server) handle(ctx context.Context, nc net.Conn, host string) {
	defer s.untrack(host)
	defer func() { _ = nc.Close() }()
	// Shutdown closes the connection, however far the handshake has come.
	// AfterFunc runs at once when ctx is done already.
	stopClosing := context.AfterFunc(ctx, func() { _ = nc.Close() })
	defer stopClosing()
	started := time.Now()
	// An address that failed too often is closed before the handshake.
	if !s.limiter.AllowAddr(ctx, host) {
		s.logger.Info("an SSH connection was refused: the address failed too often", "addr", host)
		return
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := nc.SetDeadline(started.Add(s.cfg.HandshakeTimeout)); err != nil {
		s.logger.Debug("setting the handshake deadline failed", "addr", host, "err", errors.NewErr(tagDeadlineFailed, err))
		return
	}
	var offered atomic.Bool
	sc, chans, reqs, err := ssh.NewServerConn(nc, s.config(ctx, &offered))
	if err != nil {
		// A connection that offered a public key and ends without a
		// successful authentication is one failed login of its address,
		// however many keys it offered (SPEC §7.16). A connection that never
		// offered one (a port scan, a banner check, a password attempt) is
		// not counted, so that it cannot lock out the web logins of a shared
		// address.
		if ctx.Err() == nil && offered.Load() {
			s.limiter.FailAddr(ctx, host)
		}
		s.logger.Debug("an SSH handshake failed", "addr", host, "err", errors.NewErr(tagHandshakeFailed, err))
		return
	}
	defer func() { _ = sc.Close() }()
	// The handshake is over; the connection has the rest of its 30 seconds.
	if err := nc.SetDeadline(started.Add(s.cfg.ConnTimeout)); err != nil {
		s.logger.Debug("setting the connection deadline failed", "addr", host, "err", errors.NewErr(tagDeadlineFailed, err))
		return
	}

	var wg sync.WaitGroup
	defer wg.Wait()
	// Every global request (tcpip-forward and the like) is refused.
	wg.Go(func() {
		for req := range reqs {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	})
	sess := &session{
		server: s, ctx: ctx, user: sc.User(), fingerprint: sc.Permissions.Extensions[extFingerprint],
		addr: nc.RemoteAddr().String(),
	}
	taken := false
	for newCh := range chans {
		if newCh.ChannelType() != "session" || taken {
			// direct-tcpip, x11 and a second session are refused.
			_ = newCh.Reject(ssh.UnknownChannelType, "only one session is allowed")
			continue
		}
		taken = true
		ch, requests, err := newCh.Accept()
		if err != nil {
			s.logger.Debug("accepting an SSH session failed", "addr", host, "err", errors.NewErr(tagAcceptChannelFailed, err))
			continue
		}
		wg.Go(func() {
			sess.run(ch, requests)
			// The session is over, and there is nothing else to offer.
			_ = sc.Close()
		})
	}
}

// session is the one session channel of a connection.
type session struct {
	server      *Server
	ctx         context.Context
	user        string
	fingerprint string
	addr        string
}

// execPayload is the payload of an "exec" request.
type execPayload struct {
	Command string
}

// exitStatus is the payload of an "exit-status" request.
type exitStatus struct {
	Status uint32
}

// codeJSON is what the command json writes.
type codeJSON struct {
	Code      string `json:"code"`
	ExpiresAt string `json:"expires_at"`
}

// run answers the requests of the session. A shell or the command json gets a
// code. A pty-req, env or x11-req is refused but the session goes on, because
// OpenSSH sends them before the shell and carries on when they fail. Any other
// command and a subsystem are refused, and end the session with a message and
// the status 1.
func (se *session) run(ch ssh.Channel, requests <-chan *ssh.Request) {
	defer func() { _ = ch.Close() }()
	refused := 0
	for req := range requests {
		switch req.Type {
		case "shell":
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
			se.giveCode(ch, false)
			return
		case "exec":
			var p execPayload
			if err := ssh.Unmarshal(req.Payload, &p); err != nil || p.Command != execCommandJSON {
				se.refuse(ch, req)
				return
			}
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
			se.giveCode(ch, true)
			return
		case "subsystem":
			se.refuse(ch, req)
			return
		default:
			// pty-req, env, x11-req, window-change, signal and the unknown.
			refused++
			if refused >= maxRefusedRequests {
				se.refuse(ch, req)
				return
			}
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

// refuse refuses the request, tells the client what the server is for and ends
// the session with the status 1. The message and the status go out before the
// refusal, so that a client that stops at the refusal (OpenSSH does) has
// printed the message already.
func (se *session) refuse(ch ssh.Channel, req *ssh.Request) {
	_, _ = ch.Stderr().Write([]byte(textOnlyCodes))
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(exitStatus{Status: 1}))
	if req.WantReply {
		_ = req.Reply(false, nil)
	}
}

// giveCode makes a code and writes it as the command json or the shell does,
// then ends the session with the status 0. When the code cannot be made it
// writes a message to stderr, ends with the status 1 and logs the error.
func (se *session) giveCode(ch ssh.Channel, asJSON bool) {
	issued, err := se.server.signIn.IssueCode(se.ctx, se.user, se.fingerprint, se.addr)
	if err != nil {
		se.server.logger.Error("making an SSH sign-in code failed", "user", se.user, "fingerprint", se.fingerprint,
			"addr", se.addr, "err", err)
		_, _ = ch.Stderr().Write([]byte(textNoCode))
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(exitStatus{Status: 1}))
		return
	}
	var out []byte
	if asJSON {
		raw, err := json.Marshal(codeJSON{Code: issued.Code, ExpiresAt: issued.ExpiresAt.UTC().Format(time.RFC3339)})
		if err != nil {
			se.server.logger.Error("encoding an SSH sign-in code failed", "err", errors.NewErr(tagEncodeFailed, err))
			_, _ = ch.Stderr().Write([]byte(textNoCode))
			_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(exitStatus{Status: 1}))
			return
		}
		out = append(raw, '\n')
	} else {
		out = []byte("Your Clowder sign-in code: " + issued.Formatted() + " (valid for 60 seconds)\r\n")
	}
	if _, err := ch.Write(out); err != nil {
		se.server.logger.Debug("writing an SSH sign-in code failed", "addr", se.addr, "err", errors.NewErr(tagWriteFailed, err))
		return
	}
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(exitStatus{Status: 0}))
}

const (
	tagAcceptFailed        = "SSHAcceptFailed"
	tagAcceptChannelFailed = "SSHAcceptChannelFailed"
	tagHandshakeFailed     = "SSHHandshakeFailed"
	tagDeadlineFailed      = "SSHDeadlineFailed"
	tagWriteFailed         = "SSHWriteFailed"
	tagEncodeFailed        = "SSHEncodeFailed"
	tagKeyNotAllowed       = "SSHKeyNotAllowed"
)
