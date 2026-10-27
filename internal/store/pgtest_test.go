package store

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/clock"
)

// onPostgres reports whether the tests run on PostgreSQL.
func onPostgres() bool {
	return os.Getenv(PostgresTestEnv) != ""
}

// skipOnPostgres skips a test of something that only SQLite has, with the
// reason.
func skipOnPostgres(tb testing.TB, reason string) {
	tb.Helper()
	if onPostgres() {
		tb.Skipf("SQLite only: %s", reason)
	}
}

// skipUnlessPostgres skips a test of something that needs the server of
// CLOWDER_TEST_POSTGRES.
func skipUnlessPostgres(tb testing.TB) {
	tb.Helper()
	if !onPostgres() {
		tb.Skipf("set %s (scripts/test-docker.sh does) to run this test on PostgreSQL", PostgresTestEnv)
	}
}

// testPostgresTemplate is the migrated template database of this test binary
// (package storetest, which package store cannot import, has its own).
var testPostgresTemplate = sync.OnceValues(func() (*PostgresTemplate, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return NewPostgresTemplate(ctx, os.Getenv(PostgresTestEnv))
})

// openPostgresCopy opens a new database copied from the template. The store
// is closed and the database dropped when the test ends.
func openPostgresCopy(tb testing.TB, clk clock.Clock) *Store {
	tb.Helper()
	tmpl, err := testPostgresTemplate()
	if err != nil {
		tb.Fatalf("postgres template: %+v", err)
	}
	s, release, err := tmpl.Open(tb.Context(), clk, nil)
	if err != nil {
		tb.Fatalf("open postgres: %+v", err)
	}
	tb.Cleanup(func() {
		if err := release(); err != nil {
			tb.Errorf("release: %+v", err)
		}
	})
	return s
}

// queryPlan is what the database says about how it runs a query.
type queryPlan struct {
	// details are the lines of SQLite's EXPLAIN QUERY PLAN, or, on PostgreSQL,
	// "Node Type index-name" for every node of EXPLAIN (FORMAT JSON).
	details []string
	// sorts reports whether the plan sorts rows that an index could have
	// delivered in order (a TEMP B-TREE of SQLite, a Sort node of PostgreSQL).
	sorts bool
}

// uses reports whether the plan reads the index.
func (p queryPlan) uses(index string) bool {
	return slices.ContainsFunc(p.details, func(d string) bool { return strings.Contains(d, index) })
}

// explain returns the plan of query. On PostgreSQL the planner of a database
// with empty tables prefers a sequential scan, which says nothing about the
// indexes: the plan is made with sequential scans disabled for the
// transaction, so that it shows whether the index can serve the query.
func explain(tb testing.TB, s *Store, query string, args ...any) queryPlan {
	tb.Helper()
	ctx := tb.Context()
	if !onPostgres() {
		var rows []struct{ Detail string }
		if err := s.db(ctx).Raw("EXPLAIN QUERY PLAN "+query, args...).Scan(&rows).Error; err != nil {
			tb.Fatalf("explain: %v", err)
		}
		var p queryPlan
		for _, r := range rows {
			p.details = append(p.details, r.Detail)
			p.sorts = p.sorts || strings.Contains(r.Detail, "TEMP B-TREE")
		}
		return p
	}
	var doc string
	err := s.Tx(ctx, func(tx *Store) error {
		if err := tx.db(ctx).Exec("SET LOCAL enable_seqscan = off").Error; err != nil {
			return err
		}
		return tx.db(ctx).Raw("EXPLAIN (FORMAT JSON) "+query, args...).Scan(&doc).Error
	})
	if err != nil {
		tb.Fatalf("explain: %+v", err)
	}
	var top []struct{ Plan map[string]any }
	if err := json.Unmarshal([]byte(doc), &top); err != nil || len(top) != 1 {
		tb.Fatalf("explain: cannot read %q: %v", doc, err)
	}
	var p queryPlan
	var walk func(node map[string]any)
	walk = func(node map[string]any) {
		nodeType, _ := node["Node Type"].(string)
		index, _ := node["Index Name"].(string)
		p.details = append(p.details, strings.TrimSpace(nodeType+" "+index))
		p.sorts = p.sorts || nodeType == "Sort"
		children, _ := node["Plans"].([]any)
		for _, c := range children {
			if m, ok := c.(map[string]any); ok {
				walk(m)
			}
		}
	}
	walk(top[0].Plan)
	return p
}
