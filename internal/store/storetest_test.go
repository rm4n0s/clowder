package store_test

import (
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/clowder/internal/store/storetest"
	"github.com/rm4n0s/errors"
)

// cleanupTB is a testing.TB that keeps its cleanup functions, so a test can run
// them before the real test ends.
type cleanupTB struct {
	testing.TB
	cleanups []func()
}

func (c *cleanupTB) Cleanup(f func()) {
	c.cleanups = append(c.cleanups, f)
}

func (c *cleanupTB) runCleanups() {
	for i := len(c.cleanups) - 1; i >= 0; i-- {
		c.cleanups[i]()
	}
}

func TestOpenGivesIndependentDatabases(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC))
	a := storetest.Open(t, clk)
	b := storetest.Open(t, clk)
	if a == b {
		t.Fatal("two calls returned the same store")
	}
	ctx := t.Context()
	const create = "CREATE TABLE marker (id INTEGER PRIMARY KEY)"
	if err := a.DB(ctx).Exec(create).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB(ctx).Exec("INSERT INTO marker (id) VALUES (1)").Error; err != nil {
		t.Fatal(err)
	}
	// The same statement works in b: it has no such table.
	if err := b.DB(ctx).Exec(create).Error; err != nil {
		t.Fatalf("b sees the table of a: %v", err)
	}
	var n int64
	if err := b.DB(ctx).Raw("SELECT count(*) FROM marker").Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("b has %d rows from a", n)
	}
}

func TestOpenSubtestsAndParallel(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC))
	for _, name := range []string{"first/one", "second two", "third?#%"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := storetest.Open(t, clk)
			err := s.Tx(t.Context(), func(tx *store.Store) error { return nil })
			if err != nil {
				t.Fatalf("Tx: %v", err)
			}
		})
	}
}

func TestOpenClosesInCleanup(t *testing.T) {
	tb := &cleanupTB{TB: t}
	s := storetest.Open(tb, clock.NewFake(time.Unix(0, 0)))
	ctx := t.Context()
	if err := s.Tx(ctx, func(tx *store.Store) error { return nil }); err != nil {
		t.Fatalf("Tx before cleanup: %v", err)
	}
	tb.runCleanups()
	err := s.Tx(ctx, func(tx *store.Store) error { return nil })
	if _, ok := errors.FromError(err); !ok {
		t.Errorf("Tx after cleanup returned %v, want a tagged error from the closed database", err)
	}
}

// TestOpenOnPostgres is TestOpenGivesIndependentDatabases for the PostgreSQL
// path of storetest.Open: every call gets a database of its own, copied from
// the template, with the schema and with no rows of the other, and the database
// is dropped when the test ends. It runs only when CLOWDER_TEST_POSTGRES is set.
func TestOpenOnPostgres(t *testing.T) {
	if !storetest.Postgres() {
		t.Skipf("set %s (scripts/test-docker.sh does) to run the PostgreSQL path of storetest.Open", store.PostgresTestEnv)
	}
	clk := clock.NewFake(time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC))
	ctx := t.Context()
	database := func(s *store.Store) string {
		t.Helper()
		var name string
		if err := s.DB(ctx).Raw("SELECT current_database()").Scan(&name).Error; err != nil {
			t.Fatal(err)
		}
		return name
	}

	a, b := storetest.Open(t, clk), storetest.Open(t, clk)
	nameA, nameB := database(a), database(b)
	if nameA == nameB {
		t.Fatalf("both stores are on the database %s", nameA)
	}
	u := &store.User{Username: "ann", DisplayName: "Ann", PasswordHash: "h", Role: store.RoleMember}
	if err := a.CreateUser(ctx, u); err != nil {
		t.Fatalf("CreateUser: %+v", err)
	}
	if _, err := a.UserByID(ctx, u.ID); err != nil {
		t.Errorf("the store that wrote the user lost it: %+v", err)
	}
	if _, err := b.UserByID(ctx, u.ID); err == nil {
		t.Error("b sees the user of a")
	}
	var users int64
	if err := b.DB(ctx).Raw("SELECT count(*) FROM users").Scan(&users).Error; err != nil || users != 0 {
		t.Errorf("b has %d users (%v), want the empty schema", users, err)
	}

	// The cleanup closes the store and drops its database.
	tb := &cleanupTB{TB: t}
	c := storetest.Open(tb, clk)
	nameC := database(c)
	tb.runCleanups()
	var left int64
	err := a.DB(ctx).Raw("SELECT count(*) FROM pg_database WHERE datname = ?", nameC).Scan(&left).Error
	if err != nil || left != 0 {
		t.Errorf("the database %s still exists after the cleanup (%d, %v)", nameC, left, err)
	}
}
