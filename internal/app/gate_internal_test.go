package app

import (
	"testing"

	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/clock"
)

// TestGateCallsIdleWhenTheLastCallerLeaves checks that the memory of the
// hashing is given back once, when nobody is inside any more, and not while
// another caller hashes.
func TestGateCallsIdleWhenTheLastCallerLeaves(t *testing.T) {
	t.Parallel()
	calls := 0
	g := newGate(2)
	g.idle = func() { calls++ }
	ctx := t.Context()
	for _, step := range []struct {
		name  string
		do    func() error
		calls int
	}{
		{"first enters", func() error { return g.enter(ctx) }, 0},
		{"second enters", func() error { return g.enter(ctx) }, 0},
		{"second leaves, first is inside", func() error { g.leave(); return nil }, 0},
		{"first leaves, nobody is inside", func() error { g.leave(); return nil }, 1},
		{"a lone caller", func() error { return g.enter(ctx) }, 1},
		{"leaves again", func() error { g.leave(); return nil }, 2},
	} {
		if err := step.do(); err != nil {
			t.Fatalf("%s: %+v", step.name, err)
		}
		if calls != step.calls {
			t.Errorf("%s: idle was called %d times, want %d", step.name, calls, step.calls)
		}
	}
	// A gate with no idle function is fine.
	plain := newGate(1)
	if err := plain.enter(ctx); err != nil {
		t.Fatal(err)
	}
	plain.leave()
}

// TestOnlyTheRealHasherGivesMemoryBack checks which hashers the service
// releases memory for: argon2id with 64 MiB yes, the fast hasher of the tests
// no (a forced garbage collection after each of their thousands of logins would
// only slow them).
func TestOnlyTheRealHasherGivesMemoryBack(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		hasher auth.Hasher
		want   bool
	}{
		{"the default hasher", auth.DefaultHasher(), true},
		{"the fast hasher", auth.FastHasher(), false},
	}
	for _, tt := range tests {
		a := NewAccounts(nil, clock.NewFake(clock.Real().Now()), tt.hasher, AccountsConfig{})
		if got := a.hashes.idle != nil; got != tt.want {
			t.Errorf("%s: idle set = %v, want %v", tt.name, got, tt.want)
		}
	}
}
