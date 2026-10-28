package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strconv"
	"strings"

	"github.com/rm4n0s/errors"
	"golang.org/x/crypto/ssh"
)

// sshKeysScenarioName is the name of the SSH keys scenario.
const sshKeysScenarioName = "sshkeys"

// sshKeysScenario is the scenario of step 19.1 for the SSH keys of the user.
// It logs in, reads the keys and what the API says of the SSH server, makes
// an Ed25519 key in memory (nothing is read from the disk, and nothing is
// written), adds it, adds it a second time (409), lists it, tries to remove it
// with a wrong password (422), removes it and lists again, so that the user
// has the keys they had.
func sshKeysScenario(ctx context.Context, r *runner) error {
	if _, err := r.login(ctx); err != nil {
		return err
	}
	a, err := r.c.do(ctx, "GET", "/api/v1/me/ssh-keys", nil, 200)
	if err != nil {
		return err
	}
	before, ok := a.field("keys").([]any)
	if !ok || a.field("server", "enabled") == nil || a.field("server", "command") == nil {
		return errors.New(tagAnswerUnexpected, "the answer has no keys or no server", "got", a.field("keys"))
	}

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return errors.NewErr(tagKeyGenerateFailed, err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		return errors.NewErr(tagKeyGenerateFailed, err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) + " apicheck"
	request := map[string]any{"name": "apicheck", "public_key": line, "current_password": r.password}

	a, err = r.c.do(ctx, "POST", "/api/v1/me/ssh-keys", request, 201)
	if err != nil {
		return err
	}
	id, _ := a.field("id").(float64)
	if id < 1 || a.field("fingerprint") != ssh.FingerprintSHA256(key) || a.field("type") != "ssh-ed25519" || a.field("last_used_at") != nil {
		return errors.New(tagAnswerUnexpected, "the added key is not the one sent", "got", a.field("fingerprint"))
	}
	path := "/api/v1/me/ssh-keys/" + strconv.Itoa(int(id))

	// The same key again is a conflict, and says nothing of whose it is.
	if _, err := r.c.do(ctx, "POST", "/api/v1/me/ssh-keys", request, 409); err != nil {
		return err
	}
	a, err = r.c.do(ctx, "GET", "/api/v1/me/ssh-keys", nil, 200)
	if err != nil {
		return err
	}
	if keys, _ := a.field("keys").([]any); len(keys) != len(before)+1 {
		return errors.New(tagAnswerUnexpected, "the new key is not in the list", "before", len(before), "after", len(keys))
	}

	// A wrong password removes nothing.
	if _, err := r.c.do(ctx, "DELETE", path, map[string]any{"current_password": r.password + "x"}, 422); err != nil {
		return err
	}
	if _, err := r.c.do(ctx, "DELETE", path, map[string]any{"current_password": r.password}, 204); err != nil {
		return err
	}
	a, err = r.c.do(ctx, "GET", "/api/v1/me/ssh-keys", nil, 200)
	if err != nil {
		return err
	}
	if keys, _ := a.field("keys").([]any); len(keys) != len(before) {
		return errors.New(tagAnswerUnexpected, "the key was not removed", "before", len(before), "after", len(keys))
	}
	if _, err := r.c.do(ctx, "DELETE", path, map[string]any{"current_password": r.password}, 404); err != nil {
		return err
	}
	if _, err := r.c.do(ctx, "POST", "/api/v1/auth/logout", nil, 204); err != nil {
		return err
	}
	r.c.token = ""
	return nil
}

const tagKeyGenerateFailed = "KeyGenerateFailed"
