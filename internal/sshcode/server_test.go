package sshcode_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/sshcode"
	"github.com/rm4n0s/errors"
	"golang.org/x/crypto/ssh"
)

const testCode = "K7QF9MZP"

// newSigner makes an ed25519 key pair.
func newSigner(t testing.TB) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("NewSignerFromKey: %v", err)
	}
	return s
}

// fakeSignIn knows which fingerprint belongs to which enabled user.
type fakeSignIn struct {
	mu      sync.Mutex
	allowed map[string]string // fingerprint -> username
	issued  []issueCall
	err     error
}

// issueCall is one call of IssueCode.
type issueCall struct{ username, fingerprint, addr string }

func (f *fakeSignIn) KeyAllowed(_ context.Context, username, fingerprint string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.allowed[fingerprint] == username
}

func (f *fakeSignIn) IssueCode(_ context.Context, username, fingerprint, addr string) (app.IssuedSSHCode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issued = append(f.issued, issueCall{username, fingerprint, addr})
	if f.err != nil {
		return app.IssuedSSHCode{}, f.err
	}
	return app.IssuedSSHCode{Code: testCode, ExpiresAt: time.Date(2026, 10, 28, 12, 1, 0, 0, time.UTC)}, nil
}

func (f *fakeSignIn) calls() []issueCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]issueCall(nil), f.issued...)
}

// countingLimiter wraps a limiter and counts the failures.
type countingLimiter struct {
	app.LoginLimiter
	inner *app.LoginLimiter
	mu    sync.Mutex
	fails int
}

func (l *countingLimiter) AllowAddr(ctx context.Context, addr string) bool {
	return l.inner.AllowAddr(ctx, addr)
}

func (l *countingLimiter) FailAddr(ctx context.Context, addr string) {
	l.mu.Lock()
	l.fails++
	l.mu.Unlock()
	l.inner.FailAddr(ctx, addr)
}

func (l *countingLimiter) failures() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fails
}

// rig is a running server, the users it knows and a client to it.
type rig struct {
	t       *testing.T
	addr    string
	host    ssh.PublicKey
	signIn  *fakeSignIn
	limiter *countingLimiter
	logs    *syncBuffer
	alice   ssh.Signer
	bob     ssh.Signer
	dina    ssh.Signer // the key of a disabled user: unknown to the service
	stop    func() error
}

// syncBuffer is a bytes.Buffer safe for concurrent writers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startRig starts a server with cfg (its HostKey is filled in) and returns it.
// The server is stopped, and its goroutines waited for, when the test ends or
// when the test calls stop.
func startRig(t *testing.T, cfg sshcode.Config) *rig {
	t.Helper()
	r := &rig{t: t, alice: newSigner(t), bob: newSigner(t), dina: newSigner(t), logs: &syncBuffer{}}
	r.signIn = &fakeSignIn{allowed: map[string]string{
		ssh.FingerprintSHA256(r.alice.PublicKey()): "alice",
		ssh.FingerprintSHA256(r.bob.PublicKey()):   "bob",
	}}
	r.limiter = &countingLimiter{inner: app.NewLoginLimiter(nil)}
	hostKey := newSigner(t)
	cfg.HostKey = hostKey
	r.host = hostKey.PublicKey()
	logger := slog.New(slog.NewJSONHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srv := sshcode.New(cfg, r.signIn, r.limiter, logger)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	r.addr = ln.Addr().String()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	var once sync.Once
	var stopErr error
	r.stop = func() error {
		once.Do(func() {
			cancel()
			select {
			case stopErr = <-done:
			case <-time.After(10 * time.Second):
				stopErr = errors.New("ServeTimeout", "Serve did not return")
			}
		})
		return stopErr
	}
	t.Cleanup(func() {
		if err := r.stop(); err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return r
}

// clientConfig makes the configuration of a client of the user with the
// authentication methods.
func (r *rig) clientConfig(user string, auth ...ssh.AuthMethod) *ssh.ClientConfig {
	return &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
		HostKeyCallback: ssh.FixedHostKey(r.host),
		Timeout:         5 * time.Second,
	}
}

// dial connects as user with the keys.
func (r *rig) dial(user string, keys ...ssh.Signer) (*ssh.Client, error) {
	return ssh.Dial("tcp", r.addr, r.clientConfig(user, ssh.PublicKeys(keys...)))
}

// mustDial is dial that fails the test.
func (r *rig) mustDial(user string, keys ...ssh.Signer) *ssh.Client {
	r.t.Helper()
	c, err := r.dial(user, keys...)
	if err != nil {
		r.t.Fatalf("Dial as %s: %v", user, err)
	}
	r.t.Cleanup(func() { _ = c.Close() })
	return c
}

// run runs the command on a new session and returns its stdout, stderr and the
// error of Run.
func run(t *testing.T, c *ssh.Client, command string) (stdout, stderr string, err error) {
	t.Helper()
	s, err := c.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = s.Close() }()
	var out, errOut bytes.Buffer
	s.Stdout, s.Stderr = &out, &errOut
	err = s.Run(command)
	return out.String(), errOut.String(), err
}

// exitStatusOf returns the exit status inside err, -1 when err has none.
func exitStatusOf(err error) int {
	var exit *ssh.ExitError
	if errors.As(err, &exit) {
		return exit.ExitStatus()
	}
	return -1
}

func TestServerGivesTheCode(t *testing.T) {
	t.Parallel()
	r := startRig(t, sshcode.Config{})
	t.Run("json", func(t *testing.T) {
		c := r.mustDial("alice", r.alice)
		out, errOut, err := run(t, c, "json")
		if err != nil {
			t.Fatalf("Run json: %v (stderr %q)", err, errOut)
		}
		if out != `{"code":"K7QF9MZP","expires_at":"2026-10-28T12:01:00Z"}`+"\n" {
			t.Errorf("stdout = %q", out)
		}
		var got struct {
			Code      string `json:"code"`
			ExpiresAt string `json:"expires_at"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil || got.Code != testCode {
			t.Errorf("json = %+v, %v", got, err)
		}
		if _, err := time.Parse(time.RFC3339, got.ExpiresAt); err != nil {
			t.Errorf("expires_at = %q: %v", got.ExpiresAt, err)
		}
	})
	t.Run("shell", func(t *testing.T) {
		c := r.mustDial("alice", r.alice)
		s, err := c.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		var out bytes.Buffer
		s.Stdout = &out
		if err := s.Shell(); err != nil {
			t.Fatalf("Shell: %v", err)
		}
		if err := s.Wait(); err != nil {
			t.Fatalf("Wait: %v", err)
		}
		if want := "Your Clowder sign-in code: K7QF-9MZP (valid for 60 seconds)\r\n"; out.String() != want {
			t.Errorf("stdout = %q, want %q", out.String(), want)
		}
	})
	t.Run("the server closes the connection", func(t *testing.T) {
		c := r.mustDial("bob", r.bob)
		if _, _, err := run(t, c, "json"); err != nil {
			t.Fatal(err)
		}
		waited := make(chan error, 1)
		go func() { waited <- c.Wait() }()
		select {
		case <-waited:
		case <-time.After(5 * time.Second):
			t.Error("the connection stays open after the code")
		}
	})
	calls := r.signIn.calls()
	if len(calls) != 3 {
		t.Fatalf("%d calls of IssueCode, want 3: %+v", len(calls), calls)
	}
	if calls[0].username != "alice" || calls[0].fingerprint != ssh.FingerprintSHA256(r.alice.PublicKey()) ||
		!strings.HasPrefix(calls[0].addr, "127.0.0.1:") {
		t.Errorf("call = %+v", calls[0])
	}
	if log := r.logs.String(); strings.Contains(log, testCode) {
		t.Errorf("the log holds the code: %s", log)
	}
}

func TestServerCodeIsAskedForOnlyWhenUsed(t *testing.T) {
	t.Parallel()
	r := startRig(t, sshcode.Config{})
	c := r.mustDial("alice", r.alice)
	if n := len(r.signIn.calls()); n != 0 {
		t.Errorf("authenticating made %d codes", n)
	}
	_ = c.Close()
}

func TestServerRefusesAuthentication(t *testing.T) {
	t.Parallel()
	r := startRig(t, sshcode.Config{})
	// Each case has a server of its own: a refused key counts as a failed
	// login of 127.0.0.1, and five of them block the address. A connection
	// that offers no key is not counted (SPEC §7.16).
	tests := []struct {
		name         string
		user         string
		auth         []ssh.AuthMethod
		wantFailures int
	}{
		{"another user's key", "alice", []ssh.AuthMethod{ssh.PublicKeys(r.bob)}, 1},
		{"an unknown user", "nobody", []ssh.AuthMethod{ssh.PublicKeys(r.alice)}, 1},
		{"a disabled user", "dina", []ssh.AuthMethod{ssh.PublicKeys(r.dina)}, 1},
		{"a key nobody registered", "alice", []ssh.AuthMethod{ssh.PublicKeys(newSigner(t))}, 1},
		{"a password", "alice", []ssh.AuthMethod{ssh.Password("correct horse battery")}, 0},
		{"keyboard-interactive", "alice", []ssh.AuthMethod{ssh.KeyboardInteractive(
			func(string, string, []string, []bool) ([]string, error) { return []string{"x"}, nil })}, 0},
		{"no authentication", "alice", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := startRig(t, sshcode.Config{})
			c, err := ssh.Dial("tcp", r.addr, r.clientConfig(tt.user, tt.auth...))
			if err == nil {
				_ = c.Close()
				t.Fatal("the connection was accepted")
			}
			if !strings.Contains(err.Error(), "unable to authenticate") {
				t.Errorf("error = %v, want an authentication failure", err)
			}
			// The answer does not say why: no user, key or password in it.
			for _, leak := range []string{"disabled", "unknown user", "registered"} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("error %q leaks %q", err, leak)
				}
			}
			if tt.wantFailures > 0 {
				waitFor(t, func() bool { return r.limiter.failures() == tt.wantFailures }, "the failed login to be counted")
			} else {
				time.Sleep(100 * time.Millisecond)
				if n := r.limiter.failures(); n != 0 {
					t.Errorf("%d failed logins counted for a connection that offered no key, want 0", n)
				}
			}
		})
	}
}

func TestServerRefusesWhatIsNotACode(t *testing.T) {
	t.Parallel()
	r := startRig(t, sshcode.Config{})
	t.Run("another command", func(t *testing.T) {
		c := r.mustDial("alice", r.alice)
		if _, _, err := run(t, c, "ls -la"); err == nil {
			t.Fatal("the command ran")
		}
		// What the client sees on the channel, read by hand on a new
		// connection (the server ended the first one): the message on stderr,
		// the status 1 and the refusal of the request.
		c = r.mustDial("alice", r.alice)
		ch, reqs, err := c.OpenChannel("session", nil)
		if err != nil {
			t.Fatal(err)
		}
		ok, err := ch.SendRequest("exec", true, ssh.Marshal(struct{ Command string }{"ls -la"}))
		if err != nil || ok {
			t.Fatalf("exec = %v, %v; want it refused", ok, err)
		}
		stderr, err := io.ReadAll(ch.Stderr())
		if err != nil || string(stderr) != "This server only gives sign-in codes.\r\n" {
			t.Errorf("stderr = %q, %v", stderr, err)
		}
		status := -1
		for req := range reqs {
			if req.Type == "exit-status" {
				var p struct{ Status uint32 }
				if err := ssh.Unmarshal(req.Payload, &p); err == nil {
					status = int(p.Status)
				}
			}
		}
		if status != 1 {
			t.Errorf("exit status = %d, want 1", status)
		}
	})
	t.Run("a pty", func(t *testing.T) {
		c := r.mustDial("alice", r.alice)
		s, err := c.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		if err := s.RequestPty("xterm", 24, 80, ssh.TerminalModes{}); err == nil {
			t.Fatal("the pty was granted")
		}
		// The refusal does not end the session, because OpenSSH sends the
		// shell right behind its pty request and carries on when it fails.
		var out bytes.Buffer
		s.Stdout = &out
		if err := s.Shell(); err != nil {
			t.Fatalf("Shell after the refused pty: %v", err)
		}
		if err := s.Wait(); err != nil {
			t.Fatalf("Wait: %v", err)
		}
		if !strings.Contains(out.String(), "Your Clowder sign-in code: K7QF-9MZP") {
			t.Errorf("stdout = %q", out.String())
		}
	})
	t.Run("an environment variable", func(t *testing.T) {
		c := r.mustDial("alice", r.alice)
		s, err := c.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		if err := s.Setenv("LANG", "C"); err == nil {
			t.Fatal("the variable was accepted")
		}
	})
	t.Run("a subsystem", func(t *testing.T) {
		c := r.mustDial("alice", r.alice)
		s, err := c.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		var errOut bytes.Buffer
		s.Stderr = &errOut
		if err := s.RequestSubsystem("sftp"); err == nil {
			t.Fatal("the subsystem was accepted")
		}
	})
	t.Run("port forwarding", func(t *testing.T) {
		c := r.mustDial("alice", r.alice)
		if conn, err := c.Dial("tcp", "127.0.0.1:22"); err == nil {
			_ = conn.Close()
			t.Error("direct-tcpip was accepted")
		}
	})
	t.Run("a remote forward", func(t *testing.T) {
		c := r.mustDial("alice", r.alice)
		if ln, err := c.Listen("tcp", "127.0.0.1:0"); err == nil {
			_ = ln.Close()
			t.Error("tcpip-forward was accepted")
		}
	})
	t.Run("a second session", func(t *testing.T) {
		c := r.mustDial("alice", r.alice)
		s1, err := c.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s1.Close() }()
		if s2, err := c.NewSession(); err == nil {
			_ = s2.Close()
			t.Error("a second session was accepted")
		}
	})
	t.Run("endless environment variables", func(t *testing.T) {
		c := r.mustDial("alice", r.alice)
		s, err := c.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		for range 20 {
			if err := s.Setenv("A", "b"); err == nil {
				t.Fatal("a variable was accepted")
			}
		}
		// After a few refusals the server ends the session.
		if err := s.Shell(); err == nil {
			t.Error("the session lives on after 20 refused requests")
		}
	})
	if n := len(r.signIn.calls()); n != 1 {
		t.Errorf("%d codes were made, want the one of the pty test", n)
	}
}

func TestServerReportsAFailureToMakeACode(t *testing.T) {
	t.Parallel()
	r := startRig(t, sshcode.Config{})
	r.signIn.err = &appErr{tag: app.TagSSHCodeLimit}
	c := r.mustDial("alice", r.alice)
	out, errOut, err := run(t, c, "json")
	if exitStatusOf(err) != 1 {
		t.Errorf("err = %v, want exit status 1", err)
	}
	if out != "" || errOut != "Could not make a code; try again later.\r\n" {
		t.Errorf("stdout %q, stderr %q", out, errOut)
	}
	if log := r.logs.String(); !strings.Contains(log, "making an SSH sign-in code failed") || !strings.Contains(log, app.TagSSHCodeLimit) {
		t.Errorf("log = %s, want the failure with its tag", log)
	}
}

// appErr is an error with a tag, for the log.
type appErr struct{ tag string }

func (e *appErr) Error() string { return e.tag }

func (e *appErr) LogValue() slog.Value { return slog.GroupValue(slog.String("Tag", e.tag)) }

func TestServerCountsOneFailurePerConnection(t *testing.T) {
	t.Parallel()
	r := startRig(t, sshcode.Config{})
	// Three wrong keys in one connection (an agent full of keys) are one
	// failed login, not three.
	k1, k2, k3 := newSigner(t), newSigner(t), newSigner(t)
	if c, err := r.dial("alice", k1, k2, k3); err == nil {
		_ = c.Close()
		t.Fatal("accepted")
	}
	waitFor(t, func() bool { return r.limiter.failures() == 1 }, "one failed login")
	// A connection that authenticates is not a failure.
	c := r.mustDial("alice", k1, r.alice)
	if _, _, err := run(t, c, "json"); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	time.Sleep(100 * time.Millisecond)
	if n := r.limiter.failures(); n != 1 {
		t.Errorf("%d failures after a good login, want 1", n)
	}
	// A client that offers a wrong key first and then the right one signs in.
	if n := len(r.signIn.calls()); n != 1 {
		t.Errorf("%d codes", n)
	}
}

func TestServerBlocksAnAddressThatFailed(t *testing.T) {
	t.Parallel()
	r := startRig(t, sshcode.Config{})
	wrong := newSigner(t)
	for i := range 5 {
		if c, err := r.dial("alice", wrong); err == nil {
			_ = c.Close()
			t.Fatalf("attempt %d was accepted", i)
		}
		want := i + 1
		waitFor(t, func() bool { return r.limiter.failures() == want }, "the failure to be counted")
	}
	// The sixth connection is closed before the handshake, even with the
	// right key.
	_, err := r.dial("alice", r.alice)
	if err == nil {
		t.Fatal("the blocked address connected")
	}
	if strings.Contains(err.Error(), "unable to authenticate") {
		t.Errorf("error = %v: the handshake went on", err)
	}
	if n := len(r.signIn.calls()); n != 0 {
		t.Errorf("%d codes for a blocked address", n)
	}
}

func TestServerHandshakeDeadline(t *testing.T) {
	t.Parallel()
	r := startRig(t, sshcode.Config{HandshakeTimeout: 300 * time.Millisecond})
	conn, err := net.Dial("tcp", r.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	start := time.Now()
	// The client never sends anything; the server writes its banner and then
	// closes the connection when the deadline passes.
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("ReadAll: %v (the server did not close the connection)", err)
	}
	if !strings.HasPrefix(string(data), "SSH-2.0-") {
		t.Errorf("banner = %q", data)
	}
	if took := time.Since(start); took < 250*time.Millisecond || took > 3*time.Second {
		t.Errorf("the connection lasted %v, want about the deadline of 300ms", took)
	}
	// It offered no key, so it is not a failed login: a port scan or a banner
	// check must not lock out the web logins of its address.
	time.Sleep(100 * time.Millisecond)
	if n := r.limiter.failures(); n != 0 {
		t.Errorf("%d failed logins counted for a silent connection, want 0", n)
	}
}

func TestServerConnectionDeadline(t *testing.T) {
	t.Parallel()
	r := startRig(t, sshcode.Config{ConnTimeout: 600 * time.Millisecond})
	c := r.mustDial("alice", r.alice)
	// The client authenticates and then does nothing.
	waited := make(chan struct{})
	go func() { _ = c.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("an idle connection lives past its total deadline")
	}
}

func TestServerCapsTheConnections(t *testing.T) {
	t.Parallel()
	r := startRig(t, sshcode.Config{HandshakeTimeout: time.Minute, ConnTimeout: time.Minute})
	var held []net.Conn
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()
	for i := range sshcode.DefaultMaxConns {
		c, err := net.Dial("tcp", r.addr)
		if err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		held = append(held, c)
		// Wait for the banner: the server has the connection by then.
		buf := make([]byte, 8)
		if _, err := io.ReadFull(c, buf); err != nil {
			t.Fatalf("connection %d: no banner: %v", i, err)
		}
	}
	over, err := net.Dial("tcp", r.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = over.Close() }()
	if err := over.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if data, err := io.ReadAll(over); err != nil || len(data) != 0 {
		t.Errorf("the 65th connection got %q, %v; want it closed with nothing", data, err)
	}
	// A place is free again when a connection ends.
	_ = held[0].Close()
	held = held[1:]
	waitFor(t, func() bool {
		c, err := net.Dial("tcp", r.addr)
		if err != nil {
			return false
		}
		defer func() { _ = c.Close() }()
		if err := c.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			return false
		}
		_, err = io.ReadFull(c, make([]byte, 8))
		return err == nil
	}, "a free place")
}

func TestServeReturnsOnCancelWithNoGoroutineLeft(t *testing.T) {
	// Not parallel: it counts the goroutines of the whole process.
	before := runtime.NumGoroutine()
	r := startRig(t, sshcode.Config{HandshakeTimeout: time.Minute, ConnTimeout: time.Minute})
	c := r.mustDial("alice", r.alice)
	idle, err := net.Dial("tcp", r.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = idle.Close() }()
	silent, err := net.Dial("tcp", r.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = silent.Close() }()
	if _, err := io.ReadFull(idle, make([]byte, 8)); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if err := r.stop(); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("Serve took %v to return", took)
	}
	// The open connections were closed by the server.
	if err := c.Wait(); err == nil {
		t.Log("the client saw a clean end")
	}
	_ = c.Close()
	_ = idle.Close()
	_ = silent.Close()
	// Nothing of the server is left: allow the goroutines of the clients and
	// of the test a moment to end.
	deadline := time.Now().Add(5 * time.Second)
	var after int
	for {
		after = runtime.NumGoroutine()
		if after <= before || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if after > before {
		buf := make([]byte, 1<<16)
		buf = buf[:runtime.Stack(buf, true)]
		t.Errorf("%d goroutines after Serve, %d before:\n%s", after, before, buf)
	}
}

func TestServeReturnsAnAcceptError(t *testing.T) {
	t.Parallel()
	srv := sshcode.New(sshcode.Config{HostKey: newSigner(t)}, &fakeSignIn{}, &countingLimiter{inner: app.NewLoginLimiter(nil)}, nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// The listener fails by itself, not through the context.
	_ = ln.Close()
	err = srv.Serve(t.Context(), ln)
	appErr, ok := asTagged(err)
	if !ok || appErr != "SSHAcceptFailed" {
		t.Errorf("Serve = %v, want the tag SSHAcceptFailed", err)
	}
}

// waitFor polls cond until it holds or the test times out.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// asTaggedError returns err as a tagged error.
func asTaggedError(err error) (*errors.Error, bool) {
	return errors.FromError(err)
}

// asTagged returns the tag of a tagged error.
func asTagged(err error) (string, bool) {
	appErr, ok := errors.FromError(err)
	if !ok {
		return "", false
	}
	return appErr.Tag, true
}
