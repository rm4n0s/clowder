//go:build clowder_browser

package browsertest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rm4n0s/errors"
)

const (
	// defaultStartTimeout is how long StartServer waits for the server to log
	// that it is listening.
	defaultStartTimeout = 30 * time.Second
	// defaultStopTimeout is how long Stop waits for the server to exit after
	// SIGINT before it sends SIGKILL (SPEC §8: the server waits up to 10 s for
	// running requests).
	defaultStopTimeout = 10 * time.Second
	// waitDelay bounds how long Wait lingers on the stderr copy after the
	// process has exited.
	waitDelay = 2 * time.Second
	// tailSize is how much of the server's stderr is kept for error messages.
	tailSize = 4096
	// listenMessage is the message of the log entry that reports the address.
	listenMessage = "server listening"
	// baseURL is the -base-url the server is started with. It names no port, so
	// the invitation links of a test server are wrong.
	// TODO(step 2.2): the port is only known after the server has started;
	// revisit when invitation links are tested in a browser.
	baseURL = "http://127.0.0.1"
)

// Options configure StartServer. The zero value is valid.
type Options struct {
	// Stderr receives the stderr of the server (its JSON log), including the
	// "server listening" entry. Nil discards it.
	Stderr io.Writer
	// Binary is the path of a clowder binary to run. Empty builds
	// ./cmd/clowder of the module with go build.
	Binary string
	// ModuleRoot is the directory of go.mod that Binary is built from. Empty
	// finds it from the source location of this package.
	ModuleRoot string
	// StartTimeout is how long to wait until the server listens. Zero is 30 s.
	StartTimeout time.Duration
	// StopTimeout is how long Stop waits after SIGINT before SIGKILL. Zero is
	// 10 s.
	StopTimeout time.Duration
}

// Server is a running clowder server on a temporary data directory. Make it
// with StartServer and stop it with Stop.
type Server struct {
	// URL is the base URL of the server, such as http://127.0.0.1:43211, with no
	// trailing slash.
	URL string
	// DataDir is the data directory the server runs on. Stop removes it.
	DataDir string

	tmpDir      string
	stopTimeout time.Duration
	cmd         *exec.Cmd
	done        chan struct{} // closed when the process has been reaped
	waitErr     error         // the result of cmd.Wait, set before done closes
	stopOnce    sync.Once
	stopErr     error
}

// StartServer builds clowder (unless opts.Binary is set), prepares a temporary
// data directory with "clowder demo", and starts "clowder serve" on a free
// port in its own process group. It returns once the server has logged the
// address it listens on. On any error nothing is left running or on disk. The
// caller must call Stop on success.
func StartServer(ctx context.Context, opts Options) (*Server, error) {
	tmpDir, err := os.MkdirTemp("", "clowder-browsertest-")
	if err != nil {
		return nil, errors.NewErr(tagTempDirFailed, err)
	}
	srv, err := startServer(ctx, opts, tmpDir)
	if err != nil {
		if rmErr := os.RemoveAll(tmpDir); rmErr != nil {
			return nil, errors.NewErr(tagTempDirFailed, rmErr).SetMetadata("dir", tmpDir, "after", err.Error())
		}
		return nil, err
	}
	return srv, nil
}

// startServer is StartServer on the temporary directory tmpDir.
func startServer(ctx context.Context, opts Options, tmpDir string) (*Server, error) {
	bin := opts.Binary
	if bin == "" {
		root := opts.ModuleRoot
		if root == "" {
			var err error
			root, err = ModuleRoot()
			if err != nil {
				return nil, err
			}
		}
		bin = filepath.Join(tmpDir, "clowder")
		build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/clowder")
		build.Dir = root
		if out, err := build.CombinedOutput(); err != nil {
			return nil, errors.NewErr(tagServerBuildFailed, err).SetMetadata("dir", root, "output", string(out))
		}
	}

	dataDir := filepath.Join(tmpDir, "data")
	demo := exec.CommandContext(ctx, bin, "demo", "-data", dataDir)
	if out, err := demo.CombinedOutput(); err != nil {
		return nil, errors.NewErr(tagDemoFailed, err).SetMetadata("data", dataDir, "output", string(out))
	}

	w := newListenWatcher(opts.Stderr)
	cmd := exec.Command(bin, "serve", "-addr", "127.0.0.1:0", "-data", dataDir, "-base-url", baseURL)
	cmd.Stderr = w
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = waitDelay
	if err := cmd.Start(); err != nil {
		return nil, errors.NewErr(tagServerStartFailed, err).SetMetadata("binary", bin)
	}
	stopTimeout := opts.StopTimeout
	if stopTimeout <= 0 {
		stopTimeout = defaultStopTimeout
	}
	srv := &Server{
		DataDir:     dataDir,
		tmpDir:      tmpDir,
		stopTimeout: stopTimeout,
		cmd:         cmd,
		done:        make(chan struct{}),
	}
	go func() {
		srv.waitErr = cmd.Wait()
		close(srv.done)
	}()

	startTimeout := opts.StartTimeout
	if startTimeout <= 0 {
		startTimeout = defaultStartTimeout
	}
	timer := time.NewTimer(startTimeout)
	defer timer.Stop()
	select {
	case addr := <-w.addr:
		srv.URL = "http://" + addr
		return srv, nil
	case <-srv.done:
		stopErr := srv.stopProcess() // kills what the process left in its group
		return nil, errors.New(tagServerExitedEarly, "the server exited before it listened",
			"exit", errString(srv.waitErr), "stderr", w.Tail(), "stop", errString(stopErr))
	case <-timer.C:
		stopErr := srv.stopProcess()
		return nil, errors.New(tagServerStartTimeout, "the server did not listen in time",
			"timeout", startTimeout.String(), "stderr", w.Tail(), "stop", errString(stopErr))
	case <-ctx.Done():
		stopErr := srv.stopProcess()
		return nil, errors.NewErr(tagServerStartCanceled, ctx.Err()).
			SetMetadata("stderr", w.Tail(), "stop", errString(stopErr))
	}
}

// Stop stops the server: SIGINT to its process group, then SIGKILL to the group
// after the stop timeout. It always reaps the process, removes the temporary
// directory, and may be called more than once; every call returns the result of
// the first. The error is non-nil when the server had to be killed or did not
// exit cleanly.
func (s *Server) Stop() error {
	s.stopOnce.Do(s.stop)
	return s.stopErr
}

// stop is the work of the first Stop.
func (s *Server) stop() {
	s.stopErr = s.stopProcess()
	if err := os.RemoveAll(s.tmpDir); err != nil && s.stopErr == nil {
		s.stopErr = errors.NewErr(tagTempDirFailed, err).SetMetadata("dir", s.tmpDir)
	}
}

// stopProcess ends the process group of the server and waits for the process.
func (s *Server) stopProcess() error {
	pgid := s.cmd.Process.Pid
	var result error
	select {
	case <-s.done:
		// Already exited; its group may still hold stragglers.
	default:
		if err := syscall.Kill(-pgid, syscall.SIGINT); err != nil && !errors.Is(err, syscall.ESRCH) {
			result = errors.NewErr(tagServerStopFailed, err).SetMetadata("signal", "SIGINT")
		}
		timer := time.NewTimer(s.stopTimeout)
		defer timer.Stop()
		select {
		case <-s.done:
			var exitErr *exec.ExitError
			if errors.As(s.waitErr, &exitErr) && result == nil {
				result = errors.NewErr(tagServerStopFailed, s.waitErr).SetMetadata("signal", "SIGINT")
			}
		case <-timer.C:
			if result == nil {
				result = errors.New(tagServerStopFailed, "the server did not stop after SIGINT and was killed",
					"timeout", s.stopTimeout.String())
			}
		}
	}
	// Whatever is left in the group goes, and the process is reaped.
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) && result == nil {
		result = errors.NewErr(tagServerStopFailed, err).SetMetadata("signal", "SIGKILL")
	}
	<-s.done
	return result
}

// errString is the text of err, or "" for nil.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ParseListenAddr reads one line of the JSON log of the server. It returns the
// address of the "server listening" entry and true, or false for any other
// line.
func ParseListenAddr(line string) (string, bool) {
	var entry struct {
		Msg  string `json:"msg"`
		Addr string `json:"addr"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &entry); err != nil {
		return "", false
	}
	if entry.Msg != listenMessage || entry.Addr == "" {
		return "", false
	}
	return entry.Addr, true
}

// ModuleRoot returns the directory of the go.mod of the module, found from the
// source location of this package.
func ModuleRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New(tagModuleRootNotFound, "the source location of the package is unknown")
	}
	root, err := findModuleRoot(filepath.Dir(file))
	if err != nil {
		return "", err
	}
	return root, nil
}

// findModuleRoot walks up from dir to the first directory that holds go.mod.
func findModuleRoot(dir string) (string, error) {
	for d := dir; ; {
		info, err := os.Stat(filepath.Join(d, "go.mod"))
		if err == nil && !info.IsDir() {
			return d, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", errors.New(tagModuleRootNotFound, "no go.mod in the directory or its parents", "dir", dir)
		}
		d = parent
	}
}

// listenWatcher is the stderr of the server. It passes everything on to out,
// reads the lines to find the "server listening" entry, and keeps the tail of
// the output for error messages.
type listenWatcher struct {
	out  io.Writer
	addr chan string // gets the address once

	mu    sync.Mutex
	line  []byte // the incomplete last line
	tail  []byte
	found bool
}

// newListenWatcher makes a listenWatcher that forwards to out (nil discards).
func newListenWatcher(out io.Writer) *listenWatcher {
	if out == nil {
		out = io.Discard
	}
	return &listenWatcher{out: out, addr: make(chan string, 1)}
}

// Write implements io.Writer. It reports the forwarding result of out.
func (w *listenWatcher) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tail = append(w.tail, p...)
	if len(w.tail) > tailSize {
		w.tail = w.tail[len(w.tail)-tailSize:]
	}
	if !w.found {
		w.line = append(w.line, p...)
		for {
			i := bytes.IndexByte(w.line, '\n')
			if i < 0 {
				break
			}
			if addr, ok := ParseListenAddr(string(w.line[:i])); ok {
				w.found = true
				w.line = nil
				w.addr <- addr
				break
			}
			w.line = w.line[i+1:]
		}
		if len(w.line) > tailSize {
			w.line = nil // not a log line; do not grow without bound
		}
	}
	// The forwarding error is not the process' business: report the write as
	// done, so that a failing writer does not break the copy of stderr.
	_, _ = w.out.Write(p)
	return len(p), nil
}

// Tail returns the last bytes of the server's stderr.
func (w *listenWatcher) Tail() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.tail)
}

const (
	tagTempDirFailed       = "TempDirFailed"
	tagModuleRootNotFound  = "ModuleRootNotFound"
	tagServerBuildFailed   = "ServerBuildFailed"
	tagDemoFailed          = "DemoFailed"
	tagServerStartFailed   = "ServerStartFailed"
	tagServerExitedEarly   = "ServerExitedEarly"
	tagServerStartTimeout  = "ServerStartTimeout"
	tagServerStartCanceled = "ServerStartCanceled"
	tagServerStopFailed    = "ServerStopFailed"
)
