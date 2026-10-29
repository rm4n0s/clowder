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

// BenchmarkLargeSidebar measures what the sidebar of every page reads for the
// unread counts (SPEC §7.17, step 20.2): Rooms.Mine, the rooms of alice with
// their counts, and the one small query Rooms.Unread that a stream reads when
// a room's count changes (step 24.1). It skips like BenchmarkLargeSearch.
func BenchmarkLargeSidebar(b *testing.B) {
	st := storetest.OpenLarge(b, clock.Real())
	alice, err := st.UserByUsername(b.Context(), "alice")
	if err != nil {
		b.Fatalf("UserByUsername: %+v", err)
	}
	actor := app.Actor{UserID: alice.ID, Username: alice.Username, Role: alice.Role}
	rooms := app.NewRooms(st, nil, nil, nil)
	b.Run("Rooms.Mine", func(b *testing.B) {
		mine, err := rooms.Mine(b.Context(), actor)
		if err != nil || len(mine) == 0 {
			b.Fatalf("Mine = %d rooms, %v", len(mine), err)
		}
		unread := 0
		for _, r := range mine {
			unread += r.UnreadPosts
		}
		b.Logf("%d rooms, %d unread posts in all", len(mine), unread)
		b.ResetTimer()
		for b.Loop() {
			if _, err := rooms.Mine(b.Context(), actor); err != nil {
				b.Fatalf("Mine: %+v", err)
			}
		}
	})
	b.Run("Rooms.Unread, one room", func(b *testing.B) {
		mine, err := rooms.Mine(b.Context(), actor)
		if err != nil || len(mine) == 0 {
			b.Fatalf("Mine = %d rooms, %v", len(mine), err)
		}
		id := mine[0].ID
		b.ResetTimer()
		for b.Loop() {
			if _, _, err := rooms.Unread(b.Context(), actor, id); err != nil {
				b.Fatalf("Unread: %+v", err)
			}
		}
	})
}
