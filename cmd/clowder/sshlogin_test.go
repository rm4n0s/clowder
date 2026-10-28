package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/store"
	"golang.org/x/crypto/ssh"
)

// serveAndPrepare starts serve on a data directory in which alice has a key
// that fetched a code and has turned the SSH code on, with the SSH server on
// (sshOn) or off, and returns the address of the web server and a function
// that stops serve.
func serveAndPrepare(t *testing.T, sshOn bool) (addr string, stop func()) {
	t.Helper()
	dataDir := t.TempDir()
	ctx := t.Context()
	// The data is made first, in the database serve opens afterwards.
	st, err := store.Open(ctx, filepath.Join(dataDir, databaseFile), clock.Real(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	accounts := app.NewAccounts(st, clock.Real(), auth.FastHasher(), app.AccountsConfig{
		InvitationLifetime: time.Hour, SessionLifetime: time.Hour, SSHServer: true,
	})
	inv, err := accounts.CreateSystemInvitation(ctx, store.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	alice, err := accounts.SignUp(ctx, inv.Token, "alice", "Alice", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	actor := app.Actor{UserID: alice.ID, Username: "alice", Role: alice.Role}
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	keys := app.NewSSHKeys(st, clock.Real(), accounts, app.SSHServerInfo{Addr: ":2222"})
	added, err := keys.Add(ctx, actor, "laptop", string(ssh.MarshalAuthorizedKey(key)), "correct horse battery", "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.NewSignIn(st, clock.Real(), nil).IssueCode(ctx, "alice", added.Fingerprint, "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	if err := keys.EnableSSHCode(ctx, actor, "correct horse battery", "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	args := []string{"-data", dataDir, "-addr", "127.0.0.1:0"}
	if sshOn {
		args = append(args, "-ssh-addr", "127.0.0.1:0")
	}
	sctx, cancel := context.WithCancel(ctx)
	var stderr syncBuffer
	done := make(chan error, 1)
	go func() { done <- runServe(sctx, args, mapEnv(nil), &stderr) }()
	deadline := time.Now().Add(10 * time.Second)
	for addr == "" {
		for line := range strings.SplitSeq(stderr.String(), "\n") {
			var entry struct {
				Msg  string `json:"msg"`
				Addr string `json:"addr"`
			}
			if json.Unmarshal([]byte(line), &entry) == nil && entry.Msg == "server listening" {
				addr = entry.Addr
			}
		}
		select {
		case err := <-done:
			t.Fatalf("serve ended early: %+v", err)
		default:
		}
		if addr == "" && time.Now().After(deadline) {
			t.Fatalf("serve did not listen; stderr:\n%s", stderr.String())
		}
		if addr == "" {
			time.Sleep(10 * time.Millisecond)
		}
	}
	return addr, func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("serve returned %+v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("serve did not stop")
		}
	}
}

// TestServeAsksForTheSSHCodeOnlyWithTheServerOn checks that serve wires
// -ssh-addr into the login (SPEC §7.16): with the server on, alice, who turned
// the code on, gets 401 ssh_code_required after her password; with it off the
// password alone logs her in.
func TestServeAsksForTheSSHCodeOnlyWithTheServerOn(t *testing.T) {
	tests := []struct {
		name       string
		sshOn      bool
		wantStatus int
		wantCode   string
	}{
		{"the SSH server on", true, http.StatusUnauthorized, "ssh_code_required"},
		{"the SSH server off", false, http.StatusOK, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, stop := serveAndPrepare(t, tt.sshOn)
			defer stop()
			body, err := json.Marshal(map[string]string{"username": "alice", "password": "correct horse battery", "client_name": "test"})
			if err != nil {
				t.Fatal(err)
			}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+addr+"/api/v1/auth/login", bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			raw, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", resp.StatusCode, tt.wantStatus, raw)
			}
			if tt.wantCode != "" && !strings.Contains(string(raw), `"code":"`+tt.wantCode+`"`) {
				t.Errorf("body = %s, want the code %s", raw, tt.wantCode)
			}
		})
	}
}
