package app

import (
	"context"
	"runtime"

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
}

// newGate makes a gate with n places; n below 1 is 1.
func newGate(n int) *gate {
	return &gate{slots: make(chan struct{}, max(n, 1))}
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
