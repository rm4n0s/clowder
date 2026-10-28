package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"time"

	"github.com/rm4n0s/errors"
	"golang.org/x/crypto/ssh"
)

// sshDialTimeout bounds the connection and the handshake with the SSH server.
const sshDialTimeout = 10 * time.Second

// defaultSSHPort is the port of the SSH server of Clowder when -ssh-addr is
// not given.
const defaultSSHPort = "2222"

// defaultSSHAddr returns host:2222 for the host of the base URL, the address
// of the SSH server when -ssh-addr is not given.
func defaultSSHAddr(baseURL string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		return "", errors.New(tagSSHAddrUnknown, "the SSH address cannot be taken from the base URL; give -ssh-addr",
			"baseURL", baseURL)
	}
	return net.JoinHostPort(u.Hostname(), defaultSSHPort), nil
}

// loadSSHSigner reads the unencrypted OpenSSH private key at path. It never
// reads anything else from the disk.
func loadSSHSigner(path string) (ssh.Signer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.NewErr(tagSSHKeyUnreadable, err).SetMetadata("path", path)
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return nil, errors.NewErr(tagSSHKeyUnreadable, err).SetMetadata("path", path)
	}
	return signer, nil
}

// fetchSSHCode asks the SSH server for a sign-in code as the user: it
// connects with the key (the command json), prints the fingerprint of the host
// key, which it does not pin, and the code as <code>, and returns the code.
// It tells the redactor the code, so that it is never printed.
func (r *runner) fetchSSHCode(ctx context.Context, username string) (string, error) {
	signer, err := loadSSHSigner(r.sshKey)
	if err != nil {
		return "", err
	}
	out := r.c.out
	_, _ = fmt.Fprintf(out, "> ssh -i %s %s@%s json\n", r.sshKey, username, r.sshAddr)
	cfg := &ssh.ClientConfig{
		User: username,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		// The host key is not pinned: apicheck shows what a client sees, and
		// prints the fingerprint so that a person can compare it.
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			_, _ = fmt.Fprintf(out, "< host key: %s %s\n", key.Type(), ssh.FingerprintSHA256(key))
			return nil
		},
		Timeout: sshDialTimeout,
	}
	dialer := net.Dialer{Timeout: sshDialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", r.sshAddr)
	if err != nil {
		return "", errors.NewErr(tagSSHFailed, err).SetMetadata("addr", r.sshAddr)
	}
	_ = conn.SetDeadline(time.Now().Add(2 * sshDialTimeout))
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, r.sshAddr, cfg)
	if err != nil {
		_ = conn.Close()
		return "", errors.NewErr(tagSSHFailed, err).SetMetadata("addr", r.sshAddr)
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	defer func() { _ = client.Close() }()
	session, err := client.NewSession()
	if err != nil {
		return "", errors.NewErr(tagSSHFailed, err).SetMetadata("addr", r.sshAddr)
	}
	defer func() { _ = session.Close() }()
	raw, err := session.Output("json")
	if err != nil {
		return "", errors.NewErr(tagSSHFailed, err).SetMetadata("addr", r.sshAddr)
	}
	var issued struct {
		Code      string `json:"code"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(raw, &issued); err != nil || issued.Code == "" {
		return "", errors.New(tagSSHFailed, "the SSH server did not answer with a code", "addr", r.sshAddr)
	}
	r.c.redac.add(issued.Code)
	_, _ = fmt.Fprintf(out, "< {\"code\":\"<code>\",\"expires_at\":%q}\n\n", issued.ExpiresAt)
	return issued.Code, nil
}

// loginWithSSHCode logs in as the runner's user with the SSH code, for a run
// that has -ssh-key. It sends the password alone first: a user who turned on
// the SSH code gets 401 ssh_code_required (the password was right), fetches a
// code over SSH and logs in again with ssh_code; a user without the code logs
// in at once. It returns the answer of the login that worked.
func (r *runner) loginWithSSHCode(ctx context.Context, c *client, username, password string) (answer, error) {
	body := map[string]string{"username": username, "password": password, "client_name": r.clientName}
	a, err := c.do(ctx, "POST", "/api/v1/auth/login", body, 200, 401)
	if err != nil {
		return a, err
	}
	if a.status == 401 {
		if got := a.field("error", "code"); got != "ssh_code_required" {
			return a, errors.New(tagAnswerUnexpected, "the login was refused for another reason than the missing SSH code",
				"got", got)
		}
		code, err := r.fetchSSHCode(ctx, username)
		if err != nil {
			return a, err
		}
		body["ssh_code"] = code
		if a, err = c.do(ctx, "POST", "/api/v1/auth/login", body, 200); err != nil {
			return a, err
		}
	}
	return a, nil
}

const (
	tagSSHAddrUnknown   = "SSHAddrUnknown"
	tagSSHKeyUnreadable = "SSHKeyUnreadable"
	tagSSHFailed        = "SSHFailed"
)
