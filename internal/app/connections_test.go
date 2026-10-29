package app_test

import (
	"sync"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/metrics"
)

// TestConnectionsAcquire walks the limit of live connections (SPEC D62) at its
// boundary: the 30th is accepted, the 31st is refused, a released connection
// makes room for one more, and another user is not affected.
func TestConnectionsAcquire(t *testing.T) {
	t.Parallel()
	const alice, bob = 1, 2
	steps := []struct {
		name string
		do   func(t *testing.T, c *app.Connections, held *[]func())
	}{
		{"fill to the limit", func(t *testing.T, c *app.Connections, held *[]func()) {
			for i := range app.MaxConnectionsPerUser {
				release, err := c.Acquire(alice)
				if err != nil {
					t.Fatalf("connection %d refused: %+v", i+1, err)
				}
				*held = append(*held, release)
			}
			if got := c.Open(alice); got != app.MaxConnectionsPerUser {
				t.Errorf("Open = %d, want %d", got, app.MaxConnectionsPerUser)
			}
		}},
		{"the next one is refused", func(t *testing.T, c *app.Connections, held *[]func()) {
			release, err := c.Acquire(alice)
			if release != nil {
				t.Error("a refused Acquire returned a release function")
			}
			appErr := requireTag(t, err, app.TagConnectionsLimited, "Connections.Acquire.ConnectionsLimited")
			if appErr.Metadata["limit"] != app.MaxConnectionsPerUser {
				t.Errorf("metadata = %v, want the limit", appErr.Metadata)
			}
			if got := c.Open(alice); got != app.MaxConnectionsPerUser {
				t.Errorf("a refusal was counted: Open = %d", got)
			}
		}},
		{"another user is not affected", func(t *testing.T, c *app.Connections, held *[]func()) {
			release, err := c.Acquire(bob)
			if err != nil {
				t.Fatalf("another user refused: %+v", err)
			}
			if got := c.Open(bob); got != 1 {
				t.Errorf("Open(bob) = %d, want 1", got)
			}
			release()
		}},
		{"one closed makes room for one more", func(t *testing.T, c *app.Connections, held *[]func()) {
			(*held)[0]()
			// Giving back twice gives back one.
			(*held)[0]()
			if got := c.Open(alice); got != app.MaxConnectionsPerUser-1 {
				t.Fatalf("Open = %d after one release (twice called), want %d", got, app.MaxConnectionsPerUser-1)
			}
			release, err := c.Acquire(alice)
			if err != nil {
				t.Fatalf("refused after a release: %+v", err)
			}
			*held = append(*held, release)
			if _, err := c.Acquire(alice); err == nil {
				t.Error("the limit moved after a release")
			}
		}},
		{"everything closed leaves nothing", func(t *testing.T, c *app.Connections, held *[]func()) {
			for _, release := range *held {
				release()
			}
			if got, users := c.Open(alice), c.Users(); got != 0 || users != 0 {
				t.Errorf("Open(alice) = %d, Users = %d, want 0 and 0", got, users)
			}
		}},
	}
	set, err := metrics.NewSet(metrics.SetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := app.NewConnections(0, set.LimitsRefused.With(metrics.LimitConnections))
	var held []func()
	for _, st := range steps {
		st.do(t, c, &held)
	}
	// Two refusals above: the 31st and the one after the release.
	if got := set.LimitsRefused.With(metrics.LimitConnections).Value(); got != 2 {
		t.Errorf("refusals counted = %d, want 2", got)
	}
}

// TestConnectionsWithoutLimit shows that a nil counter and a user with no ID
// count nothing, and that a custom maximum is used.
func TestConnectionsWithoutLimit(t *testing.T) {
	t.Parallel()
	var none *app.Connections
	for range 3 * app.MaxConnectionsPerUser {
		release, err := none.Acquire(1)
		if err != nil {
			t.Fatalf("a nil Connections refused: %+v", err)
		}
		release()
	}
	if none.Open(1) != 0 || none.Users() != 0 {
		t.Error("a nil Connections counted")
	}
	c := app.NewConnections(2, nil)
	for range 100 {
		release, err := c.Acquire(0)
		if err != nil {
			t.Fatalf("user 0 refused: %+v", err)
		}
		defer release()
	}
	if c.Users() != 0 {
		t.Error("user 0 was counted")
	}
	r1, err1 := c.Acquire(7)
	r2, err2 := c.Acquire(7)
	if err1 != nil || err2 != nil {
		t.Fatalf("within a maximum of 2: %v, %v", err1, err2)
	}
	if _, err := c.Acquire(7); err == nil {
		t.Error("the third connection of a maximum of 2 was accepted")
	}
	r1()
	r2()
}

// TestConnectionsConcurrent has many goroutines take and give back
// connections at once; the limit never passes and nothing is left over.
func TestConnectionsConcurrent(t *testing.T) {
	t.Parallel()
	c := app.NewConnections(5, nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	most := 0
	for g := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			user := uint64(g%2 + 1)
			for range 200 {
				release, err := c.Acquire(user)
				if err != nil {
					continue
				}
				mu.Lock()
				if n := c.Open(user); n > most {
					most = n
				}
				mu.Unlock()
				release()
			}
		}()
	}
	wg.Wait()
	if most > 5 {
		t.Errorf("most held at once = %d, over the limit of 5", most)
	}
	if c.Users() != 0 {
		t.Errorf("Users = %d after everything ended", c.Users())
	}
}
