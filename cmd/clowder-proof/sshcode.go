//go:build clowder_browser

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/rm4n0s/clowder/internal/browsertest"
	"github.com/rm4n0s/errors"
	"golang.org/x/crypto/ssh"
)

// sshCodeTimeout bounds the whole preparation of -ssh-code.
const sshCodeTimeout = time.Minute

// prepareSSHCode turns the SSH code at login on for the demo user, so that the
// page shows the second form of the login: through the API of the server it
// logs the user in, adds a freshly made ed25519 key (kept in memory), fetches a
// code over SSH with it (which proves the key), turns "Ask for an SSH code when
// I log in" on and ends the API session. The key is dropped; the proof never
// needs it again.
func prepareSSHCode(ctx context.Context, srv *browsertest.Server, username string) error {
	ctx, cancel := context.WithTimeout(ctx, sshCodeTimeout)
	defer cancel()
	token, err := apiToken(ctx, srv.URL, username)
	if err != nil {
		return err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return errors.NewErr(tagSSHCodeFailed, err).SetMetadata("user", username)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return errors.NewErr(tagSSHCodeFailed, err).SetMetadata("user", username)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))) + " clowder-proof"
	add := map[string]string{"name": "proof", "public_key": line, "current_password": browsertest.DemoPassword}
	if err := apiCall(ctx, srv.URL, token, http.MethodPost, "/api/v1/me/ssh-keys", add, http.StatusCreated); err != nil {
		return err
	}
	if err := fetchCode(ctx, srv.SSHAddr, username, signer); err != nil {
		return err
	}
	enable := map[string]string{"current_password": browsertest.DemoPassword}
	if err := apiCall(ctx, srv.URL, token, http.MethodPut, "/api/v1/me/ssh-code", enable, http.StatusNoContent); err != nil {
		return err
	}
	return apiCall(ctx, srv.URL, token, http.MethodPost, "/api/v1/auth/logout", nil, http.StatusNoContent)
}

// apiToken logs the demo user in through the API and returns the bearer token.
func apiToken(ctx context.Context, baseURL, username string) (string, error) {
	body, err := json.Marshal(map[string]string{
		"username": username, "password": browsertest.DemoPassword, "client_name": "clowder-proof",
	})
	if err != nil {
		return "", errors.NewErr(tagSSHCodeFailed, err).SetMetadata("user", username)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/auth/login", bytes.NewReader(body))
	if err != nil {
		return "", errors.NewErr(tagSSHCodeFailed, err).SetMetadata("user", username)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", errors.NewErr(tagSSHCodeFailed, err).SetMetadata("user", username)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", errors.NewErr(tagSSHCodeFailed, err).SetMetadata("user", username)
	}
	var answer struct {
		Token string `json:"token"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &answer) != nil || answer.Token == "" {
		return "", errors.New(tagSSHCodeFailed, "the API did not accept the login", "user", username, "status", resp.StatusCode)
	}
	return answer.Token, nil
}

// apiCall sends one request of the API as the token and checks the status.
func apiCall(ctx context.Context, baseURL, token, method, path string, body map[string]string, want int) error {
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return errors.NewErr(tagSSHCodeFailed, err).SetMetadata("path", path)
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, payload)
	if err != nil {
		return errors.NewErr(tagSSHCodeFailed, err).SetMetadata("path", path)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return errors.NewErr(tagSSHCodeFailed, err).SetMetadata("path", path)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != want {
		return errors.New(tagSSHCodeFailed, "the API answered with another status",
			"method", method, "path", path, "want", want, "got", resp.StatusCode)
	}
	return nil
}

// fetchCode connects to the SSH server as the user with the signer and runs
// the command json, which hands out a code and so proves the key. The code is
// not kept.
func fetchCode(ctx context.Context, addr, username string, signer ssh.Signer) error {
	cfg := &ssh.ClientConfig{
		User: username,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		// The server was started by this tool a moment ago on the loopback
		// address; there is no host key to compare with.
		HostKeyCallback: func(string, net.Addr, ssh.PublicKey) error { return nil },
		Timeout:         10 * time.Second,
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return errors.NewErr(tagSSHCodeFailed, err).SetMetadata("addr", addr)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		return errors.NewErr(tagSSHCodeFailed, err).SetMetadata("addr", addr)
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	defer func() { _ = client.Close() }()
	session, err := client.NewSession()
	if err != nil {
		return errors.NewErr(tagSSHCodeFailed, err).SetMetadata("addr", addr)
	}
	defer func() { _ = session.Close() }()
	if _, err := session.Output("json"); err != nil {
		return errors.NewErr(tagSSHCodeFailed, err).SetMetadata("addr", addr)
	}
	return nil
}

const tagSSHCodeFailed = "SSHCodeFailed"
