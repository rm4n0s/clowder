package app

import (
	"context"
	"runtime"
	"runtime/debug"

	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

const (
	// minHashSlots and maxHashSlots bound the default number of passwords
	// that are hashed at the same time.
	minHashSlots = 2
	maxHashSlots = 8
)

// gate lets at most a fixed number of callers inside at once; the others wait
// for a place or for their context to end. Make it with newGate.
type gate struct {
	slots chan struct{}
	// idle, when it is not nil, is called when a caller leaves and nobody
	// else is inside.
	idle func()
}

// newGate makes a gate with n places; n below 1 is 1.
func newGate(n int) *gate {
	return &gate{slots: make(chan struct{}, max(n, 1))}
}

// heavyHashBytes is the memory of one hash from which the gate gives it back to
// the system when the last hash is done (releaseMemory).
const heavyHashBytes = 16 << 20

// releaseMemory returns what the hashing left over to the operating system.
// An argon2id hash takes 64 MiB, which the Go runtime keeps in the process
// after the hash ends and does not give back for minutes, if ever, while the
// server does little else: one login left the server at 95 MiB instead of 30.
// A server with 100 open streams was measured at 155 MiB for it, over the
// target of SPEC §10. debug.FreeOSMemory runs a garbage collection and returns
// the free memory; once per burst of logins is cheap next to a hash.
func releaseMemory() {
	debug.FreeOSMemory()
}

// hashSlots returns the number of places of the gate of the password hashing:
// n when it is positive, else twice the number of CPUs between 2 and 8. Each
// hash needs 64 MiB (auth.DefaultHasher), so a flood of logins must not run
// them all at once.
func hashSlots(n int) int {
	if n > 0 {
		return n
	}
	return min(max(2*runtime.NumCPU(), minHashSlots), maxHashSlots)
}

// enter waits for a place. It fails with the tag HashWaitCanceled when ctx
// ends first; then the caller must not call leave.
func (g *gate) enter(ctx context.Context) error {
	select {
	case g.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return errors.NewErr(tagHashWaitCanceled, ctx.Err())
	}
}

// leave gives the place back.
func (g *gate) leave() {
	<-g.slots
	if g.idle != nil && len(g.slots) == 0 {
		g.idle()
	}
}

// hash hashes the password once a place of the gate is free.
func (a *Accounts) hash(ctx context.Context, password string) (string, error) {
	if err := a.hashes.enter(ctx); err != nil {
		return "", err
	}
	defer a.hashes.leave()
	return a.hasher.Hash(password)
}

// verify checks the password against the hash once a place of the gate is
// free.
func (a *Accounts) verify(ctx context.Context, password, hash string) (bool, error) {
	if err := a.hashes.enter(ctx); err != nil {
		return false, err
	}
	defer a.hashes.leave()
	return a.hasher.Verify(password, hash)
}

// tagHashWaitCanceled marks a request that ended while it waited for its turn
// to hash a password.
const tagHashWaitCanceled = "HashWaitCanceled"

// checkPassword checks the current password of user the way the password
// change does, for the services that need it before a sensitive change. It
// asks the login limiter first: allowed is false when the username or the
// address clientIP has failed too many times lately, and then the password is
// not looked at. Otherwise the password is verified through the hashing gate,
// match says whether it is right and a wrong one counts as a failed login of
// the username and the address. The caller makes the errors, so that their
// routes end in the failing function (an error comes back only for a failed or
// canceled hashing).
func (a *Accounts) checkPassword(ctx context.Context, user store.User, password, clientIP string) (allowed, match bool, err error) {
	if !a.limiter.Allow(ctx, user.Username, clientIP) {
		return false, false, nil
	}
	if len(password) <= passwordMaxBytes {
		match, err = a.verify(ctx, password, user.PasswordHash)
		if err != nil {
			return true, false, err
		}
	}
	if !match {
		a.limiter.Fail(ctx, user.Username, clientIP)
	}
	return true, match, nil
}
