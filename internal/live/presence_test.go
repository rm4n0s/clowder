package live_test

import (
	"sync"
	"testing"

	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/live/presencetest"
)

// TestMemoryPresence runs the shared table of tests on the in-memory
// presence.
func TestMemoryPresence(t *testing.T) {
	t.Parallel()
	presencetest.Run(t, false, func(*testing.T) func() live.Presence {
		return func() live.Presence { return live.NewMemoryPresence() }
	})
}

// TestMemoryPresenceLocalAndRace checks Local and that concurrent use is
// safe.
func TestMemoryPresenceLocalAndRace(t *testing.T) {
	t.Parallel()
	p := live.NewMemoryPresence()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := uint64(i%4 + 1)
			p.Connect(id)
			if _, err := p.Online(t.Context(), []uint64{id}); err != nil {
				t.Errorf("Online: %v", err)
			}
			p.Disconnect(id)
		}()
	}
	wg.Wait()
	if got := p.Local(); len(got) != 0 {
		t.Errorf("Local() = %v after every connection closed, want none", got)
	}
	p.Connect(2)
	p.Connect(2)
	p.Connect(5)
	if got := p.Local(); len(got) != 2 {
		t.Errorf("Local() = %v, want the users 2 and 5", got)
	}
}
