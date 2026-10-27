//go:build clowder_browser

package browsertest

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rm4n0s/errors"
)

func TestParseListenAddr(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantAddr string
		wantOK   bool
	}{
		{
			name:     "the listening entry",
			line:     `{"time":"2026-10-26T21:50:51+02:00","level":"INFO","msg":"server listening","addr":"127.0.0.1:39095"}`,
			wantAddr: "127.0.0.1:39095",
			wantOK:   true,
		},
		{name: "with trailing newline", line: `{"msg":"server listening","addr":"[::1]:80"}` + "\n", wantAddr: "[::1]:80", wantOK: true},
		{name: "another message", line: `{"msg":"request","addr":"127.0.0.1:1"}`},
		{name: "no address", line: `{"msg":"server listening"}`},
		{name: "not JSON", line: `server listening on 127.0.0.1:1`},
		{name: "empty", line: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, ok := ParseListenAddr(tt.line)
			if addr != tt.wantAddr || ok != tt.wantOK {
				t.Errorf("ParseListenAddr = %q, %v; want %q, %v", addr, ok, tt.wantAddr, tt.wantOK)
			}
		})
	}
}

func TestParseInviteToken(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		wantToken string
		wantOK    bool
	}{
		{"the link and the expiry", "http://127.0.0.1/invite/AbC-_123\nexpires 2026-11-02T10:00:00Z\n", "AbC-_123", true},
		{"link only", "http://localhost:8080/invite/tok", "tok", true},
		{"link with a trailing slash in the base URL", "https://x.example/chat/invite/tok\n", "tok", true},
		{"windows line ending", "http://h/invite/tok\r\n", "tok", true},
		{"no link", "expires soon\n", "", false},
		{"link on the second line only", "hello\nhttp://h/invite/tok\n", "", false},
		{"empty token", "http://h/invite/\n", "", false},
		{"token with a path", "http://h/invite/a/b\n", "", false},
		{"token with a query", "http://h/invite/a?b\n", "", false},
		{"empty", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, ok := ParseInviteToken(tt.output)
			if token != tt.wantToken || ok != tt.wantOK {
				t.Errorf("ParseInviteToken(%q) = %q, %v; want %q, %v", tt.output, token, ok, tt.wantToken, tt.wantOK)
			}
		})
	}
}

func TestListenWatcher(t *testing.T) {
	var out bytes.Buffer
	w := newListenWatcher(&out)
	// The entry arrives in pieces, after a line that is not the entry.
	for _, chunk := range []string{
		`{"msg":"other"}` + "\n" + `{"msg":"server lis`,
		`tening","addr":"127.0.0.1:5"}` + "\n",
		`{"msg":"server listening","addr":"127.0.0.1:6"}` + "\n",
	} {
		if n, err := w.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	select {
	case addr := <-w.addr:
		if addr != "127.0.0.1:5" {
			t.Errorf("addr = %q, want 127.0.0.1:5", addr)
		}
	default:
		t.Fatal("no address reported")
	}
	select {
	case addr := <-w.addr:
		t.Errorf("a second address %q was reported", addr)
	default:
	}
	if !strings.Contains(out.String(), `"127.0.0.1:6"`) || !strings.Contains(w.Tail(), `"other"`) {
		t.Errorf("output not forwarded or kept: out=%q tail=%q", out.String(), w.Tail())
	}
}

func TestFindModuleRoot(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := findModuleRoot(deep)
	if err != nil || got != root {
		t.Errorf("findModuleRoot(%s) = %q, %v; want %q", deep, got, err, root)
	}

	// The real module: this repository.
	real, err := ModuleRoot()
	if err != nil {
		t.Fatalf("ModuleRoot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(real, "cmd", "clowder")); err != nil {
		t.Errorf("ModuleRoot = %s, which has no cmd/clowder: %v", real, err)
	}

	// No go.mod anywhere above a directory: the filesystem root has none.
	_, err = findModuleRoot("/proc/self")
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T: %v", err, err)
	}
	if appErr.Tag != tagModuleRootNotFound || !appErr.HasRoute("findModuleRoot.ModuleRootNotFound") {
		t.Errorf("tag = %q, route = %s", appErr.Tag, appErr.Route())
	}
}

func TestFindChromium(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	_, err := FindChromium()
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T: %v", err, err)
	}
	if appErr.Tag != TagChromiumNotFound || !appErr.HasRoute("FindChromium.ChromiumNotFound") {
		t.Errorf("tag = %q, route = %s", appErr.Tag, appErr.Route())
	}

	_, _, err = NewBrowser(context.Background())
	appErr, ok = errors.FromError(err)
	if !ok || appErr.Tag != TagChromiumNotFound || !appErr.HasRoute("NewBrowser->FindChromium.ChromiumNotFound") {
		t.Errorf("NewBrowser without Chromium: %v", err)
	}

	// The second name on the list is found when the first is missing.
	script := filepath.Join(dir, "chromium")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := FindChromium()
	if err != nil || got != script {
		t.Errorf("FindChromium = %q, %v; want %q", got, err, script)
	}
}

// fakeBinary writes a shell script that stands for clowder: "demo" succeeds
// and "serve" runs serve.
func fakeBinary(t *testing.T, serve string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clowder")
	script := "#!/bin/sh\nif [ \"$1\" = demo ]; then exit 0; fi\n" +
		"if [ \"$1\" = invite ]; then echo http://127.0.0.1/invite/fake-token; echo expires soon; exit 0; fi\n" + serve + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// listenLine makes the fake server report the address it was given
// ("serve -addr <addr> ..." makes it the third argument).
const listenLine = `echo "{\"msg\":\"server listening\",\"addr\":\"$3\"}" >&2`

func TestStartServerErrors(t *testing.T) {
	noGoMod := t.TempDir()
	badDemo := filepath.Join(t.TempDir(), "clowder")
	if err := os.WriteFile(badDemo, []byte("#!/bin/sh\necho no demo >&2\nexit 2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name      string
		ctx       context.Context
		opts      Options
		wantTag   string
		wantRoute string
		wantMeta  string // a substring of the metadata "stderr" or "output"
		metaKey   string
	}{
		{
			name:      "module without the command",
			ctx:       context.Background(),
			opts:      Options{ModuleRoot: noGoMod},
			wantTag:   tagServerBuildFailed,
			wantRoute: "StartServer->startServer->buildBinary.ServerBuildFailed",
		},
		{
			name:      "demo fails",
			ctx:       context.Background(),
			opts:      Options{Binary: badDemo},
			wantTag:   tagDemoFailed,
			wantRoute: "StartServer->startServer->prepare.DemoFailed",
			metaKey:   "output",
			wantMeta:  "no demo",
		},
		{
			name:      "server exits before it listens",
			ctx:       context.Background(),
			opts:      Options{Binary: fakeBinary(t, "echo boom >&2; exit 3")},
			wantTag:   tagServerExitedEarly,
			wantRoute: "StartServer->startServer->launch.ServerExitedEarly",
			metaKey:   "stderr",
			wantMeta:  "boom",
		},
		{
			name:      "server never listens",
			ctx:       context.Background(),
			opts:      Options{Binary: fakeBinary(t, "echo waiting >&2; exec sleep 60"), StartTimeout: 300 * time.Millisecond},
			wantTag:   tagServerStartTimeout,
			wantRoute: "StartServer->startServer->launch.ServerStartTimeout",
			metaKey:   "stderr",
			wantMeta:  "waiting",
		},
		{
			name:      "context canceled",
			ctx:       canceled,
			opts:      Options{Binary: fakeBinary(t, "exec sleep 60")},
			wantTag:   tagDemoFailed, // the canceled context stops the demo command first
			wantRoute: "StartServer->startServer->prepare.DemoFailed",
		},
		{
			name:      "binary that is not executable",
			ctx:       context.Background(),
			opts:      Options{Binary: filepath.Join(t.TempDir(), "missing")},
			wantTag:   tagDemoFailed,
			wantRoute: "StartServer->startServer->prepare.DemoFailed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, err := StartServer(tt.ctx, tt.opts)
			if err == nil {
				_ = srv.Stop()
				t.Fatal("StartServer succeeded")
			}
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T: %v", err, err)
			}
			if appErr.Tag != tt.wantTag {
				t.Errorf("tag = %q, want %q (route %s)", appErr.Tag, tt.wantTag, appErr.Route())
			}
			if !appErr.HasRoute(tt.wantRoute) {
				t.Errorf("unexpected failure path: %s, want %s", appErr.Route(), tt.wantRoute)
			}
			if tt.wantMeta != "" {
				if got, _ := appErr.Metadata[tt.metaKey].(string); !strings.Contains(got, tt.wantMeta) {
					t.Errorf("metadata %q = %q, want it to contain %q", tt.metaKey, got, tt.wantMeta)
				}
			}
		})
	}
}

// TestStartServerCanceledWhileStarting cancels the context after the server
// process started but before it listens.
func TestStartServerCanceledWhileStarting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	_, err := StartServer(ctx, Options{Binary: fakeBinary(t, "exec sleep 60")})
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T: %v", err, err)
	}
	if appErr.Tag != tagServerStartCanceled || !appErr.HasRoute("StartServer->startServer->launch.ServerStartCanceled") {
		t.Errorf("tag = %q, route = %s", appErr.Tag, appErr.Route())
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error does not wrap context.Canceled: %v", err)
	}
}

func TestStopServer(t *testing.T) {
	tests := []struct {
		name      string
		serve     string
		wantErr   bool // Stop reports that the server did not stop cleanly
		wantRoute string
	}{
		{
			name: "exits on SIGINT",
			// The trap comes before the listen line: Stop may signal as soon as it sees the line.
			serve: "trap 'exit 0' INT; " + listenLine + "; while true; do sleep 0.05; done",
		},
		{
			name:      "ignores SIGINT and is killed",
			serve:     "trap '' INT; " + listenLine + "; while true; do sleep 0.05; done",
			wantErr:   true,
			wantRoute: "Server.stop->Server.stopProcess.ServerStopFailed",
		},
		{
			name:      "dies of SIGINT",
			serve:     listenLine + "; while true; do sleep 0.05; done",
			wantErr:   true,
			wantRoute: "Server.stop->Server.stopProcess.ServerStopFailed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			srv, err := StartServer(context.Background(), Options{
				Binary:      fakeBinary(t, tt.serve),
				Stderr:      &stderr,
				StopTimeout: 300 * time.Millisecond,
			})
			if err != nil {
				t.Fatalf("StartServer: %v", err)
			}
			if want := "http://" + srv.cmd.Args[3]; srv.URL != want {
				t.Errorf("URL = %q, want %q", srv.URL, want)
			}
			pid := srv.cmd.Process.Pid
			err = srv.Stop()
			if !strings.Contains(stderr.String(), "server listening") {
				t.Errorf("stderr was not forwarded: %q", stderr.String())
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("Stop = %v, want error: %v", err, tt.wantErr)
			}
			if tt.wantErr {
				appErr, ok := errors.FromError(err)
				if !ok || appErr.Tag != tagServerStopFailed || !appErr.HasRoute(tt.wantRoute) {
					t.Errorf("unexpected stop error: %v (route %s)", err, appErr.Route())
				}
			}
			// Nothing of the group is left, and Stop is repeatable. A child
			// of the killed shell (the sleep) is reaped by init or the
			// session's subreaper, not by Stop, so it may linger for a moment
			// as a zombie after the kill: wait for the group to go.
			groupGone := false
			for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
				if syscall.Kill(-pid, 0) != nil {
					groupGone = true
					break
				}
			}
			if !groupGone {
				t.Errorf("process group %d still exists after Stop", pid)
			}
			if _, statErr := os.Stat(srv.tmpDir); !os.IsNotExist(statErr) {
				t.Errorf("temporary directory %s was not removed: %v", srv.tmpDir, statErr)
			}
			if again := srv.Stop(); (again != nil) != tt.wantErr {
				t.Errorf("second Stop = %v", again)
			}
		})
	}
}

// TestStartServerReal builds clowder and runs it: the harness against the
// real binary, without a browser.
func TestStartServerReal(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	srv, err := StartServer(context.Background(), Options{InviteMember: true})
	if err != nil {
		t.Fatalf("StartServer: %+v", err)
	}
	defer func() {
		if err := srv.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	}()
	if len(srv.InviteToken) != 43 {
		t.Errorf("InviteToken = %q, want a token of 43 characters", srv.InviteToken)
	}
	if !strings.HasPrefix(srv.URL, "http://127.0.0.1:") || strings.HasSuffix(srv.URL, ":0") {
		t.Errorf("URL = %q, want http://127.0.0.1:<port>", srv.URL)
	}
	if _, err := os.Stat(srv.DataDir); err != nil {
		t.Errorf("data directory: %v", err)
	}
}

func TestStartServerInviteMember(t *testing.T) {
	tests := []struct {
		name      string
		opts      func(t *testing.T) Options
		wantToken string
		wantTag   string
		wantRoute string
		wantMeta  string
	}{
		{
			name: "without the option there is no token",
			opts: func(t *testing.T) Options {
				return Options{Binary: fakeBinary(t, listenLine+"; exec sleep 60"), StopTimeout: 300 * time.Millisecond}
			},
		},
		{
			name: "with the option the token is read from the invite command",
			opts: func(t *testing.T) Options {
				return Options{Binary: fakeBinary(t, listenLine+"; exec sleep 60"), StopTimeout: 300 * time.Millisecond, InviteMember: true}
			},
			wantToken: "fake-token",
		},
		{
			name: "invite fails",
			opts: func(t *testing.T) Options {
				path := filepath.Join(t.TempDir(), "clowder")
				script := "#!/bin/sh\nif [ \"$1\" = invite ]; then echo bad flags >&2; exit 2; fi\nexit 0\n"
				if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
				return Options{Binary: path, InviteMember: true}
			},
			wantTag: tagInviteFailed, wantRoute: "StartServer->startServer->prepare->inviteMember.InviteFailed", wantMeta: "bad flags",
		},
		{
			name: "invite prints no link",
			opts: func(t *testing.T) Options {
				path := filepath.Join(t.TempDir(), "clowder")
				script := "#!/bin/sh\nif [ \"$1\" = invite ]; then echo nothing useful; exit 0; fi\nexit 0\n"
				if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
				return Options{Binary: path, InviteMember: true}
			},
			wantTag: tagInviteOutputInvalid, wantRoute: "StartServer->startServer->prepare->inviteMember.InviteOutputInvalid", wantMeta: "nothing useful",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, err := StartServer(context.Background(), tt.opts(t))
			if tt.wantTag == "" {
				if err != nil {
					t.Fatalf("StartServer: %+v", err)
				}
				defer func() { _ = srv.Stop() }() // killed after the timeout, as the other fakes
				if srv.InviteToken != tt.wantToken {
					t.Errorf("InviteToken = %q, want %q", srv.InviteToken, tt.wantToken)
				}
				return
			}
			if err == nil {
				_ = srv.Stop()
				t.Fatal("StartServer succeeded")
			}
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T: %v", err, err)
			}
			if appErr.Tag != tt.wantTag || !appErr.HasRoute(tt.wantRoute) {
				t.Errorf("tag = %q, route = %s; want %s, %s", appErr.Tag, appErr.Route(), tt.wantTag, tt.wantRoute)
			}
			meta := ""
			for _, k := range []string{"stderr", "output"} {
				if v, ok := appErr.Metadata[k].(string); ok {
					meta += v
				}
			}
			if !strings.Contains(meta, tt.wantMeta) {
				t.Errorf("metadata = %v, want it to contain %q", appErr.Metadata, tt.wantMeta)
			}
		})
	}
}

func TestFreePort(t *testing.T) {
	seen := map[int]bool{}
	for range 5 {
		port, err := freePort(context.Background())
		if err != nil {
			t.Fatalf("freePort: %+v", err)
		}
		if port <= 0 || port > 65535 {
			t.Fatalf("port = %d, want a valid port", port)
		}
		seen[port] = true
		// The port is free again: a listener can take it now.
		var lc net.ListenConfig
		ln, err := lc.Listen(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			t.Fatalf("listen on the port %d it returned: %v", port, err)
		}
		if err := ln.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) < 2 {
		t.Errorf("five calls returned the same port: %v", seen)
	}

	_, err := freePortOn(context.Background(), "no.such.host.invalid")
	appErr, ok := errors.FromError(err)
	if !ok || appErr.Tag != tagFreePortFailed || !appErr.HasRoute("freePortOn.FreePortFailed") {
		t.Errorf("freePortOn with a host that cannot be listened on = %v", err)
	}
}

func TestIsBindFailure(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   bool
	}{
		{"port taken", `clowder: ListenFailed: listen tcp 127.0.0.1:80: bind: address already in use`, true},
		{"another failure", `clowder: DataDirCreateFailed: permission denied`, false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBindFailure(tt.stderr); got != tt.want {
				t.Errorf("isBindFailure(%q) = %v, want %v", tt.stderr, got, tt.want)
			}
		})
	}
}

// TestStartServerRetriesWhenThePortIsTaken has the fake server fail to bind
// the first time: StartServer tries once more, with a new port.
func TestStartServerRetriesWhenThePortIsTaken(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "tried")
	serve := `if [ ! -e ` + marker + ` ]; then touch ` + marker + `; echo "clowder: ListenFailed: bind: address already in use" >&2; exit 1; fi; ` +
		listenLine + "; exec sleep 60"
	srv, err := StartServer(context.Background(), Options{Binary: fakeBinary(t, serve), StopTimeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatalf("StartServer: %+v", err)
	}
	if want := "http://" + srv.cmd.Args[3]; srv.URL != want {
		t.Errorf("URL = %q, want %q", srv.URL, want)
	}
	_ = srv.Stop() // the fake does not exit on SIGINT cleanly; only the cleanup matters here
}

func TestStartServerGivesUpAfterTwoBindFailures(t *testing.T) {
	serve := `echo "clowder: ListenFailed: bind: address already in use" >&2; exit 1`
	_, err := StartServer(context.Background(), Options{Binary: fakeBinary(t, serve)})
	appErr, ok := errors.FromError(err)
	if !ok || appErr.Tag != tagServerExitedEarly || !appErr.HasRoute("StartServer->startServer->launch.ServerExitedEarly") {
		t.Fatalf("StartServer = %v, want ServerExitedEarly", err)
	}
}

func TestStartServerOtherFailuresAreNotRetried(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "count")
	serve := `echo x >> ` + marker + `; echo "boom" >&2; exit 1`
	_, err := StartServer(context.Background(), Options{Binary: fakeBinary(t, serve)})
	if _, ok := errors.FromError(err); !ok {
		t.Fatalf("StartServer = %v", err)
	}
	data, readErr := os.ReadFile(marker)
	if readErr != nil || strings.Count(string(data), "x") != 1 {
		t.Errorf("the server was started %q times, want once (%v)", data, readErr)
	}
}

func TestStartServerAddressMismatch(t *testing.T) {
	serve := `echo '{"msg":"server listening","addr":"127.0.0.1:1"}' >&2; exec sleep 60`
	_, err := StartServer(context.Background(), Options{Binary: fakeBinary(t, serve), StopTimeout: 300 * time.Millisecond})
	appErr, ok := errors.FromError(err)
	if !ok || appErr.Tag != tagServerAddrMismatch || !appErr.HasRoute("StartServer->startServer->launch.ServerAddrMismatch") {
		t.Fatalf("StartServer = %v, want ServerAddrMismatch", err)
	}
}
