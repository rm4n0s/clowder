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

// Open returns a Store on a new, empty database in a file of the test's
// temporary directory, migrated and using clk. Each call gets its own
// database, so tests do not see each other's rows, not even in parallel. The
// Store is closed when the test ends. A failure stops the test.
func Open(tb testing.TB, clk clock.Clock) *store.Store {
	tb.Helper()
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

// LargeDemoEnv is the environment variable that names the database file of a
// large demo (`clowder demo -size large`, SPEC §10) for the benchmarks.
const LargeDemoEnv = "CLOWDER_BENCH_DB"

// OpenLarge opens a copy of the large demo database that LargeDemoEnv names,
// in the test's temporary directory, so that the benchmark never changes the
// original. It skips the test or benchmark with a message when the variable is
// not set, which keeps the benchmarks out of the default test run. The Store
// is closed when the test ends. A failure stops the test.
func OpenLarge(tb testing.TB, clk clock.Clock) *store.Store {
	tb.Helper()
	src := os.Getenv(LargeDemoEnv)
	if src == "" {
		tb.Skipf("set %s to the clowder.db of `clowder demo -size large` to run this benchmark", LargeDemoEnv)
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
