// Package storetest opens throw-away in-memory stores for tests. It imports
// "testing", so only test code imports it, never the program.
package storetest

import (
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"testing"

	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/store"
)

// Open returns a Store on a new, empty in-memory database, migrated and using
// clk. Each call gets its own database, so tests do not see each other's rows,
// not even in parallel. The Store is closed when the test ends. A failure
// stops the test.
func Open(tb testing.TB, clk clock.Clock) *store.Store {
	tb.Helper()
	// The name is unique per call: the test's name for the reader of a
	// failure, random bytes for the database.
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		tb.Fatalf("storetest: random name: %v", err)
	}
	path := "file:" + url.QueryEscape(tb.Name()) + "-" + hex.EncodeToString(suffix) + "?mode=memory&cache=shared"
	s, err := store.Open(tb.Context(), path, clk, nil)
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
