// Package storetest opens throw-away stores for tests. It imports "testing",
// so only test code imports it, never the program.
package storetest

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/store"
)

// template makes the migrated image of an empty database on first use and
// returns the same one afterwards. Migrating costs tens of milliseconds, which
// is most of the time of a test that makes a store; a copy of the image costs
// almost nothing.
var template = sync.OnceValues(func() (*store.Template, error) {
	return store.NewTemplate(context.Background())
})

// postgresTemplate makes the migrated template database on the PostgreSQL
// server of store.PostgresTestEnv on first use and returns the same one
// afterwards, for the same reason as template.
var postgresTemplate = sync.OnceValues(func() (*store.PostgresTemplate, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return store.NewPostgresTemplate(ctx, os.Getenv(store.PostgresTestEnv))
})

// Postgres reports whether the tests run on PostgreSQL, that is whether the
// environment variable store.PostgresTestEnv (CLOWDER_TEST_POSTGRES) is set.
func Postgres() bool {
	return os.Getenv(store.PostgresTestEnv) != ""
}

// Open returns a Store on a new, empty database, migrated and using clk: a
// file in the test's temporary directory on SQLite, or, when Postgres is true,
// a database made with CREATE DATABASE … TEMPLATE on the server of
// CLOWDER_TEST_POSTGRES. Each call gets its own database, so tests do not see
// each other's rows, not even in parallel. The Store is closed, and a
// PostgreSQL database dropped, when the test ends. A failure stops the test.
func Open(tb testing.TB, clk clock.Clock) *store.Store {
	tb.Helper()
	if Postgres() {
		return openPostgres(tb, clk)
	}
	tmpl, err := template()
	if err != nil {
		tb.Fatalf("storetest: template: %+v", err)
	}
	s, err := tmpl.Open(tb.Context(), filepath.Join(tb.TempDir(), "clowder.db"), clk, nil)
	if err != nil {
		tb.Fatalf("storetest: open: %+v", err)
	}
	tb.Cleanup(func() {
		if err := s.Close(); err != nil {
			tb.Errorf("storetest: close: %v", err)
		}
	})
	return s
}

// openPostgres is Open on PostgreSQL.
func openPostgres(tb testing.TB, clk clock.Clock) *store.Store {
	tb.Helper()
	tmpl, err := postgresTemplate()
	if err != nil {
		tb.Fatalf("storetest: postgres template: %+v", err)
	}
	s, release, err := tmpl.Open(tb.Context(), clk, nil)
	if err != nil {
		tb.Fatalf("storetest: open postgres: %+v", err)
	}
	tb.Cleanup(func() {
		if err := release(); err != nil {
			tb.Errorf("storetest: release: %+v", err)
		}
	})
	return s
}

// LargeDemoEnv is the environment variable that names the database file of a
// large demo (`clowder demo -size large`, SPEC §10) for the benchmarks.
const LargeDemoEnv = "CLOWDER_BENCH_DB"

// LargeDemoPostgresEnv is the environment variable that names the PostgreSQL
// database of a large demo (`clowder demo -size large -database-url ...`) for
// the benchmarks, as a postgres:// URL. It wins over LargeDemoEnv.
const LargeDemoPostgresEnv = "CLOWDER_BENCH_DATABASE_URL"

// OpenLarge opens the large demo for a benchmark. With LargeDemoPostgresEnv
// set it opens that PostgreSQL database in place (a benchmark only reads it,
// apart from the session it logs in with); with LargeDemoEnv set it opens a
// copy of that SQLite file in the test's temporary directory, so that the
// benchmark never changes the original. It skips the test or benchmark with a
// message when neither is set, which keeps the benchmarks out of the default
// test run. The Store is closed when the test ends. A failure stops the test.
func OpenLarge(tb testing.TB, clk clock.Clock) *store.Store {
	tb.Helper()
	if url := os.Getenv(LargeDemoPostgresEnv); url != "" {
		s, err := store.OpenPostgres(tb.Context(), url, clk, nil)
		if err != nil {
			tb.Fatalf("storetest: open the large demo on PostgreSQL: %+v", err)
		}
		tb.Cleanup(func() {
			if err := s.Close(); err != nil {
				tb.Errorf("storetest: close: %v", err)
			}
		})
		return s
	}
	if Postgres() {
		tb.Skipf("the large demo benchmarks run on SQLite unless %s is set; unset %s", LargeDemoPostgresEnv, store.PostgresTestEnv)
	}
	src := os.Getenv(LargeDemoEnv)
	if src == "" {
		tb.Skipf("set %s to the clowder.db of `clowder demo -size large` (or %s to its PostgreSQL URL) to run this benchmark", LargeDemoEnv, LargeDemoPostgresEnv)
	}
	in, err := os.Open(src)
	if err != nil {
		tb.Fatalf("storetest: open the large demo: %v", err)
	}
	defer func() { _ = in.Close() }()
	path := filepath.Join(tb.TempDir(), "clowder.db")
	out, err := os.Create(path)
	if err != nil {
		tb.Fatalf("storetest: copy the large demo: %v", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		tb.Fatalf("storetest: copy the large demo: %v", err)
	}
	if err := out.Close(); err != nil {
		tb.Fatalf("storetest: copy the large demo: %v", err)
	}
	s, err := store.Open(tb.Context(), path, clk, nil)
	if err != nil {
		tb.Fatalf("storetest: open the copy: %+v", err)
	}
	tb.Cleanup(func() {
		if err := s.Close(); err != nil {
			tb.Errorf("storetest: close: %v", err)
		}
	})
	return s
}
