package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/sshcode"
	"github.com/rm4n0s/errors"
	"golang.org/x/crypto/ssh"
)

// sshFixture is the API with the SSH code on for alice, and the SSH server
// running on the same services.
type sshFixture struct {
	base    string
	sshAddr string
	keyPath string
	hostFP  string
}

// startSSHFixture runs the API and the SSH server of the sign-in codes on one
// store. alice has a key (written to keyPath as an OpenSSH private key) and,
// when codeOn is true, has turned the SSH code on.
func startSSHFixture(t *testing.T, codeOn bool) *sshFixture {
	t.Helper()
	base, _, parts := startAPIFull(t, nil, true)
	alice, err := parts.st.UserByUsername(t.Context(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	actor := app.Actor{UserID: alice.ID, Username: "alice", Role: alice.Role}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "apicheck test")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	line := string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	if _, err := parts.keys.Add(t.Context(), actor, "test", line, testPassword, "192.0.2.1"); err != nil {
		t.Fatal(err)
	}

	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostKey, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	srv := sshcode.New(sshcode.Config{HostKey: hostKey}, app.NewSignIn(parts.st, parts.clk, nil), app.NewLoginLimiter(parts.clk), nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})

	if codeOn {
		// A key proves itself by fetching a code once.
		if _, err := app.NewSignIn(parts.st, parts.clk, nil).IssueCode(t.Context(), "alice", ssh.FingerprintSHA256(signer.PublicKey()), "192.0.2.1"); err != nil {
			t.Fatal(err)
		}
		if err := parts.keys.EnableSSHCode(t.Context(), actor, testPassword, "192.0.2.1"); err != nil {
			t.Fatal(err)
		}
	}
	return &sshFixture{base: base, sshAddr: ln.Addr().String(), keyPath: keyPath, hostFP: ssh.FingerprintSHA256(hostKey.PublicKey())}
}

func TestRunWithAnSSHKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		codeOn   bool
		wantCode int
		wantOut  []string
		notWant  []string
	}{
		{
			name: "a user with the SSH code on", codeOn: true, wantCode: 0,
			wantOut: []string{
				"== session:", "> POST /api/v1/auth/login", "< 401 Unauthorized", `"code": "ssh_code_required"`, "< WWW-Authenticate: Bearer",
				"> ssh -i ", " alice@", " json", "< host key: ssh-ed25519 SHA256:", `< {"code":"<code>","expires_at":"2026-10-27T`,
				`"ssh_code": "<code>"`, "< 200 OK", `"token": "<token>"`, "> GET /api/v1/me", "ok session",
			},
		},
		{
			name: "a user without the SSH code", codeOn: false, wantCode: 0,
			wantOut: []string{"== session:", "< 200 OK", "ok session"},
			notWant: []string{"ssh_code_required", "> ssh -i", "<code>"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := startSSHFixture(t, tt.codeOn)
			var out, errOut bytes.Buffer
			code := run(t.Context(), []string{
				"-base-url", f.base, "-username", "alice", "-password", testPassword, "-only", "session",
				"-ssh-key", f.keyPath, "-ssh-addr", f.sshAddr,
			}, func(string) string { return "" }, &out, &errOut)
			if code != tt.wantCode {
				t.Fatalf("exit status %d, want %d\nstdout:\n%s\nstderr:\n%s", code, tt.wantCode, out.String(), errOut.String())
			}
			for _, want := range tt.wantOut {
				if !strings.Contains(out.String(), want) {
					t.Errorf("the output does not contain %q:\n%s", want, out.String())
				}
			}
			for _, bad := range append([]string{testPassword}, tt.notWant...) {
				if strings.Contains(out.String(), bad) {
					t.Errorf("the output contains %q:\n%s", bad, out.String())
				}
			}
			if tt.codeOn && !strings.Contains(out.String(), "< host key: ssh-ed25519 "+f.hostFP) {
				t.Errorf("the output does not show the host key fingerprint %s:\n%s", f.hostFP, out.String())
			}
		})
	}
}

func TestRunWithoutTheKeyAsksForTheCode(t *testing.T) {
	t.Parallel()
	f := startSSHFixture(t, true)
	var out, errOut bytes.Buffer
	code := run(t.Context(), []string{
		"-base-url", f.base, "-username", "alice", "-password", testPassword, "-only", "session",
	}, func(string) string { return "" }, &out, &errOut)
	if code != 1 || !strings.Contains(out.String(), "< 401 Unauthorized") || !strings.Contains(out.String(), `"code": "ssh_code_required"`) {
		t.Errorf("exit status %d, want 1 and the 401:\n%s", code, out.String())
	}
}

func TestRunWithAnSSHKeyFailures(t *testing.T) {
	t.Parallel()
	f := startSSHFixture(t, true)
	notAKey := filepath.Join(t.TempDir(), "nokey")
	if err := os.WriteFile(notAKey, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	// An SSH server that is not there.
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := closed.Addr().String()
	_ = closed.Close()

	tests := []struct {
		name    string
		extra   []string
		wantErr string
	}{
		{"a file that is not a key", []string{"-ssh-key", notAKey, "-ssh-addr", f.sshAddr}, "SSHKeyUnreadable"},
		{"a file that is not there", []string{"-ssh-key", filepath.Join(t.TempDir(), "missing"), "-ssh-addr", f.sshAddr}, "SSHKeyUnreadable"},
		{"an SSH server that is not there", []string{"-ssh-key", f.keyPath, "-ssh-addr", deadAddr}, "SSHFailed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var out, errOut bytes.Buffer
			args := append([]string{"-base-url", f.base, "-username", "alice", "-password", testPassword, "-only", "session"}, tt.extra...)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			if code := run(ctx, args, func(string) string { return "" }, &out, &errOut); code != 1 {
				t.Errorf("exit status %d, want 1:\n%s", code, out.String())
			}
			if !strings.Contains(errOut.String(), tt.wantErr) {
				t.Errorf("stderr = %q, want it to name %s", errOut.String(), tt.wantErr)
			}
		})
	}
}

func TestDefaultSSHAddr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		base, want, wantTag string
	}{
		{"http://localhost:8080", "localhost:2222", ""},
		{"https://chat.example.org/", "chat.example.org:2222", ""},
		{"http://[::1]:8080", "[::1]:2222", ""},
		{"not a url", "", tagSSHAddrUnknown},
		{"", "", tagSSHAddrUnknown},
	}
	for _, tt := range tests {
		got, err := defaultSSHAddr(tt.base)
		if tt.wantTag != "" {
			appErr, ok := errors.FromError(err)
			if !ok || appErr.Tag != tt.wantTag || !appErr.HasRoute("defaultSSHAddr.SSHAddrUnknown") {
				t.Errorf("defaultSSHAddr(%q) error = %v", tt.base, err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("defaultSSHAddr(%q) = %q, %v, want %q", tt.base, got, err, tt.want)
		}
	}
}

func TestRedactSSHCode(t *testing.T) {
	t.Parallel()
	got := (&redactor{}).compact([]byte(`{"username":"a","ssh_code":"K7QF-9MZP"}`))
	if strings.Contains(got, "K7QF") || !strings.Contains(got, `"ssh_code":"<code>"`) {
		t.Errorf("compact = %s", got)
	}
}
