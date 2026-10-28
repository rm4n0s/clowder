package sshcode_test

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/sshcode"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/clowder/internal/store/storetest"
	"golang.org/x/crypto/ssh"
)

// TestServerWithTheRealServices runs the server on the services of
// internal/app and a real database: a registered key gets a code that can be
// used once, and the keys of other users, of disabled users and of nobody get
// none.
func TestServerWithTheRealServices(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clk := clock.NewFake(time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC))
	st := storetest.Open(t, clk)
	accounts := app.NewAccounts(st, clk, auth.FastHasher(), app.AccountsConfig{
		InvitationLifetime: time.Hour, SessionLifetime: time.Hour,
	})
	signUp := func(username string) app.User {
		inv, err := accounts.CreateSystemInvitation(ctx, store.RoleMember)
		if err != nil {
			t.Fatal(err)
		}
		u, err := accounts.SignUp(ctx, inv.Token, username, username, "correct horse battery")
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	alice, bob, dina := signUp("alice"), signUp("bob"), signUp("dina")
	keys := app.NewSSHKeys(st, clk, accounts, app.SSHServerInfo{})
	register := func(u app.User, name string) ssh.Signer {
		signer := newSigner(t)
		line := string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
		if _, err := keys.Add(ctx, app.Actor{UserID: u.ID, Username: u.Username, Role: u.Role}, name, line, "correct horse battery", "192.0.2.1"); err != nil {
			t.Fatal(err)
		}
		return signer
	}
	aliceKey, bobKey, dinaKey := register(alice, "a"), register(bob, "b"), register(dina, "d")
	if err := st.SetUserDisabled(ctx, dina.ID, true); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	signIn := app.NewSignIn(st, clk, slog.New(slog.NewJSONHandler(&logs, nil)))
	hostKey := newSigner(t)
	srv := sshcode.New(sshcode.Config{HostKey: hostKey}, signIn, app.NewLoginLimiter(clk), nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(sctx, ln) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()

	dial := func(user string, key ssh.Signer) (*ssh.Client, error) {
		return ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{
			User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyCallback: ssh.FixedHostKey(hostKey.PublicKey()), Timeout: 5 * time.Second,
		})
	}
	c, err := dial("alice", aliceKey)
	if err != nil {
		t.Fatalf("alice: %v", err)
	}
	s, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.Output("json")
	_ = c.Close()
	if err != nil {
		t.Fatalf("json: %v", err)
	}
	code := strings.Split(strings.Split(string(out), `"code":"`)[1], `"`)[0]
	if _, err := st.ConsumeSSHCode(ctx, alice.ID, app.SSHCodeHash(code), clk.Now().Add(time.Second)); err != nil {
		t.Errorf("the code from the wire does not work: %+v", err)
	}
	if _, err := st.ConsumeSSHCode(ctx, alice.ID, app.SSHCodeHash(code), clk.Now().Add(time.Second)); err == nil {
		t.Error("the code worked twice")
	}
	if strings.Contains(logs.String(), code) {
		t.Errorf("the code is in the log: %s", logs.String())
	}
	if !strings.Contains(logs.String(), `"user":"alice"`) {
		t.Errorf("log = %s, want the user", logs.String())
	}
	row, err := st.SSHKeyByFingerprint(ctx, ssh.FingerprintSHA256(aliceKey.PublicKey()))
	if err != nil || row.LastUsedAt == nil {
		t.Errorf("key = %+v, %v; want its last use set", row, err)
	}

	for name, tt := range map[string]struct {
		user string
		key  ssh.Signer
	}{
		"another user's key": {"alice", bobKey},
		"a disabled user":    {"dina", dinaKey},
		"an unknown user":    {"nobody", aliceKey},
	} {
		if c, err := dial(tt.user, tt.key); err == nil {
			_ = c.Close()
			t.Errorf("%s: the connection was accepted", name)
		}
	}
}
