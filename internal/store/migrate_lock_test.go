package store

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/clock"
)

// emptyPostgres makes a new empty database on the server of the tests and
// drops it when the test ends. It returns its URL.
func emptyPostgres(t *testing.T) string {
	t.Helper()
	skipUnlessPostgres(t)
	dbURL, drop, err := NewPostgresDatabase(t.Context(), os.Getenv(PostgresTestEnv))
	if err != nil {
		t.Fatalf("NewPostgresDatabase: %+v", err)
	}
	t.Cleanup(func() {
		if err := drop(); err != nil {
			t.Errorf("drop: %+v", err)
		}
	})
	return dbURL
}

// advisoryLockFree reports whether another session can take the migration
// lock now, using a connection of s.
func advisoryLockFree(t *testing.T, s *Store) bool {
	t.Helper()
	ctx := t.Context()
	sqlDB, err := s.gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	var got bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", migrationLockKey).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got {
		if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", migrationLockKey); err != nil {
			t.Fatal(err)
		}
	}
	return got
}

// TestConcurrentOpenOfAnEmptyDatabase starts four stores at once on one new,
// empty PostgreSQL database, as four processes that start together do: every
// open succeeds, the migrations ran once each, and the lock is free after.
func TestConcurrentOpenOfAnEmptyDatabase(t *testing.T) {
	dbURL := emptyPostgres(t)
	ctx := t.Context()
	const n = 4
	stores := make([]*Store, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			stores[i], errs[i] = OpenPostgres(ctx, dbURL, clock.NewFake(fakeStart), nil)
		})
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("store %d: %+v", i, errs[i])
		}
		t.Cleanup(func() {
			if err := stores[i].Close(); err != nil {
				t.Errorf("close %d: %v", i, err)
			}
		})
	}
	var applied int64
	if err := stores[0].db(ctx).Model(&schemaMigration{}).Count(&applied).Error; err != nil {
		t.Fatal(err)
	}
	if want := int64(len(rawMigrations(dialectPostgres))); applied != want {
		t.Errorf("%d migrations recorded, want %d", applied, want)
	}
	u := &User{Username: "ann", DisplayName: "Ann", PasswordHash: "h", Role: RoleMember}
	if err := stores[1].CreateUser(ctx, u); err != nil {
		t.Fatalf("CreateUser: %+v", err)
	}
	if got, err := stores[3].UserByID(ctx, u.ID); err != nil || got.Username != "ann" {
		t.Errorf("a user written through one store is not read through another: %v, %v", got, err)
	}
	if !advisoryLockFree(t, stores[2]) {
		t.Error("the migration lock is still held after every open returned")
	}
}

// holdMigrationLock takes the lock on a session of its own and returns the
// function that releases it.
func holdMigrationLock(t *testing.T, s *Store) func() {
	t.Helper()
	ctx := t.Context()
	sqlDB, err := s.gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", migrationLockKey); err != nil {
			t.Errorf("unlock: %v", err)
		}
		if err := conn.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}
}

func TestMigrateWaitsForTheLock(t *testing.T) {
	dbURL := emptyPostgres(t)
	ctx := t.Context()
	s, err := OpenPostgres(ctx, dbURL, clock.NewFake(fakeStart), nil)
	if err != nil {
		t.Fatalf("OpenPostgres: %+v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	release := holdMigrationLock(t, s)

	done := make(chan error, 1)
	go func() { done <- s.migrate(ctx, allModels(), rawMigrations(dialectPostgres)) }()
	select {
	case err := <-done:
		t.Fatalf("migrate returned while the lock was held: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("migrate after the release: %+v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("migrate did not go on after the lock was released")
	}
	if !advisoryLockFree(t, s) {
		t.Error("the lock is still held after migrate")
	}
}

func TestMigrateLockFailures(t *testing.T) {
	dbURL := emptyPostgres(t)
	s, err := OpenPostgres(t.Context(), dbURL, clock.NewFake(fakeStart), nil)
	if err != nil {
		t.Fatalf("OpenPostgres: %+v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	t.Run("waiting for the lock ends with the context", func(t *testing.T) {
		release := holdMigrationLock(t, s)
		defer release()
		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		defer cancel()
		err := s.migrate(ctx, allModels(), rawMigrations(dialectPostgres))
		_ = requireRoute(t, err, "Store.migrate->Store.lockMigrations.MigrationLockFailed")
	})
	t.Run("no connection for a canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := s.lockMigrations(ctx)
		_ = requireRoute(t, err, "Store.lockMigrations.MigrationLockFailed")
	})
	t.Run("the lock is not held by the session", func(t *testing.T) {
		sqlDB, err := s.gdb.DB()
		if err != nil {
			t.Fatal(err)
		}
		conn, err := sqlDB.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		err = s.unlockMigrations(conn)
		_ = requireRoute(t, err, "Store.unlockMigrations.MigrationUnlockFailed")
	})
	t.Run("the connection is closed", func(t *testing.T) {
		sqlDB, err := s.gdb.DB()
		if err != nil {
			t.Fatal(err)
		}
		conn, err := sqlDB.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		err = s.unlockMigrations(conn)
		_ = requireRoute(t, err, "Store.unlockMigrations.MigrationUnlockFailed")
	})
	t.Run("a failed lock leaves the pool usable", func(t *testing.T) {
		if err := s.migrate(t.Context(), allModels(), rawMigrations(dialectPostgres)); err != nil {
			t.Fatalf("migrate: %+v", err)
		}
		if !advisoryLockFree(t, s) {
			t.Error("the lock is held")
		}
	})
}
