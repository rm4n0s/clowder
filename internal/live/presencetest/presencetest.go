// Package presencetest holds the table of tests that every live.Presence
// implementation must pass, so that the in-memory one and the one over Redis
// behave the same.
package presencetest

import (
	"slices"
	"testing"

	"github.com/rm4n0s/clowder/internal/live"
)

// Factory makes the processes of one test: each call returns a new Presence
// that is another process sharing the same store with the others made by the
// same Factory (for the in-memory implementation they share nothing, and the
// cases that need sharing are skipped through shared == false).
type Factory func(t *testing.T) (processes func() live.Presence)

// Run runs the shared table of tests. newProcesses returns, for one test, a
// function that makes a process; shared says whether processes made by it see
// each other (true for Redis, false for memory).
func Run(t *testing.T, shared bool, newProcesses Factory) {
	t.Helper()
	cases := []struct {
		name string
		// run uses the processes the test makes.
		run func(t *testing.T, process func() live.Presence)
		// needsShared marks a case that needs two processes to see each other.
		needsShared bool
	}{
		{name: "nobody is online at first", run: func(t *testing.T, process func() live.Presence) {
			p := process()
			assertOnline(t, p, []uint64{1, 2}, nil)
		}},
		{name: "a connected user is online until the last connection closes", run: func(t *testing.T, process func() live.Presence) {
			p := process()
			p.Connect(7)
			p.Connect(7)
			assertOnline(t, p, []uint64{7, 8}, []uint64{7})
			p.Disconnect(7)
			assertOnline(t, p, []uint64{7, 8}, []uint64{7})
			p.Disconnect(7)
			assertOnline(t, p, []uint64{7, 8}, nil)
		}},
		{name: "a disconnect without a connect does nothing", run: func(t *testing.T, process func() live.Presence) {
			p := process()
			p.Disconnect(3)
			assertOnline(t, p, []uint64{3}, nil)
			p.Connect(3)
			assertOnline(t, p, []uint64{3}, []uint64{3})
			p.Disconnect(3)
			p.Disconnect(3)
			assertOnline(t, p, []uint64{3}, nil)
		}},
		{name: "several users are told apart", run: func(t *testing.T, process func() live.Presence) {
			p := process()
			p.Connect(1)
			p.Connect(3)
			assertOnline(t, p, []uint64{1, 2, 3, 4}, []uint64{1, 3})
			assertOnline(t, p, nil, nil)
		}},
		{name: "a user online in another process is online here", needsShared: true, run: func(t *testing.T, process func() live.Presence) {
			a, b := process(), process()
			a.Connect(5)
			assertOnline(t, b, []uint64{5, 6}, []uint64{5})
			b.Connect(6)
			assertOnline(t, a, []uint64{5, 6}, []uint64{5, 6})
			a.Disconnect(5)
			assertOnline(t, b, []uint64{5, 6}, []uint64{6})
		}},
		{name: "a user online in two processes stays online until both close", needsShared: true, run: func(t *testing.T, process func() live.Presence) {
			a, b := process(), process()
			a.Connect(9)
			b.Connect(9)
			a.Disconnect(9)
			assertOnline(t, a, []uint64{9}, []uint64{9})
			b.Disconnect(9)
			assertOnline(t, a, []uint64{9}, nil)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.needsShared && !shared {
				t.Skip("the processes of this implementation share nothing")
			}
			tc.run(t, newProcesses(t))
		})
	}
}

// assertOnline checks that exactly the users want, among ids, are online.
func assertOnline(t *testing.T, p live.Presence, ids, want []uint64) {
	t.Helper()
	got, err := p.Online(t.Context(), ids)
	if err != nil {
		t.Fatalf("Online(%v): %+v", ids, err)
	}
	var online []uint64
	for id, ok := range got {
		if ok {
			online = append(online, id)
		}
	}
	slices.Sort(online)
	slices.Sort(want)
	if !slices.Equal(online, want) {
		t.Errorf("Online(%v) = %v, want %v", ids, online, want)
	}
}
