package app_test

import (
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/store/storetest"
)

// BenchmarkLargeSearch measures Search.Query on the large demo of SPEC §10, as
// alice. The first query, words and two tags, is the one of the target (under
// 100 ms); the others show the cost of a very common word and of a tag alone.
// It skips unless CLOWDER_BENCH_DB names the database file of
// `clowder demo -size large`:
//
//	CLOWDER_BENCH_DB=/path/to/clowder.db go test ./internal/app -run '^$' -bench Large
func BenchmarkLargeSearch(b *testing.B) {
	st := storetest.OpenLarge(b, clock.Real())
	alice, err := st.UserByUsername(b.Context(), "alice")
	if err != nil {
		b.Fatalf("UserByUsername: %+v", err)
	}
	actor := app.Actor{UserID: alice.ID, Username: alice.Username, Role: alice.Role}
	search := app.NewSearch(st)
	for _, q := range []struct{ name, query string }{
		{"words and two tags", "review deploy #release #bug"},
		{"common word and two tags", "the #docs #backend"},
		{"two words", "release plan"},
		{"one tag", "#design"},
	} {
		b.Run(q.name, func(b *testing.B) {
			page, err := search.Query(b.Context(), actor, q.query, 1)
			if err != nil || len(page.Results) == 0 {
				b.Fatalf("Query(%q) = %d results, %v", q.query, len(page.Results), err)
			}
			b.ResetTimer()
			for b.Loop() {
				if _, err := search.Query(b.Context(), actor, q.query, 1); err != nil {
					b.Fatalf("Query: %+v", err)
				}
			}
		})
	}
}
