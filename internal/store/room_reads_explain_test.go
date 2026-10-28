package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/clock"
)

// The environment variables of the large demo that the benchmarks use (see
// storetest.OpenLarge, which this file cannot import: storetest imports
// store).
const (
	largeDemoFileEnv = "CLOWDER_BENCH_DB"
	largeDemoPGEnv   = "CLOWDER_BENCH_DATABASE_URL"
)

// openLargeDemo opens the large demo of the environment for the plan and the
// timing of the unread counts: the PostgreSQL database in place, or a copy of
// the SQLite file. It skips the test when neither is set.
func openLargeDemo(t *testing.T) *Store {
	t.Helper()
	if url := os.Getenv(largeDemoPGEnv); url != "" {
		s, err := OpenPostgres(t.Context(), url, clock.Real(), nil)
		if err != nil {
			t.Fatalf("OpenPostgres: %+v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	src := os.Getenv(largeDemoFileEnv)
	if src == "" {
		t.Skipf("set %s (a SQLite large demo) or %s (a PostgreSQL one) to print the plans of the unread counts", largeDemoFileEnv, largeDemoPGEnv)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read the large demo: %v", err)
	}
	path := filepath.Join(t.TempDir(), "clowder.db")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("copy the large demo: %v", err)
	}
	s, err := Open(t.Context(), path, clock.Real(), nil)
	if err != nil {
		t.Fatalf("Open: %+v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// explainQuery returns the plan of the query; on PostgreSQL it runs it (EXPLAIN
// ANALYZE) and shows the timings.
func explainQuery(t *testing.T, s *Store, query string, args ...any) string {
	t.Helper()
	prefix := "EXPLAIN QUERY PLAN "
	if s.dialect == dialectPostgres {
		prefix = "EXPLAIN (ANALYZE, BUFFERS) "
	}
	rows, err := s.db(t.Context()).Raw(prefix+query, args...).Rows()
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		// SQLite: id, parent, notused, detail; PostgreSQL: one text column.
		lines = append(lines, fmt.Sprint(vals[len(vals)-1]))
	}
	return strings.Join(lines, "\n")
}

// timeIt runs f n times and returns the median and the slowest.
func timeIt(t *testing.T, n int, f func() error) (median, slowest time.Duration) {
	t.Helper()
	times := make([]time.Duration, n)
	for i := range times {
		start := time.Now()
		if err := f(); err != nil {
			t.Fatalf("%+v", err)
		}
		times[i] = time.Since(start)
	}
	for i := range times {
		for j := i + 1; j < len(times); j++ {
			if times[j] < times[i] {
				times[i], times[j] = times[j], times[i]
			}
		}
	}
	return times[len(times)/2], times[len(times)-1]
}

// TestLargeDemoUnreadPlans prints the plans and the timings of the queries of
// the unread counts on the large demo of the environment (step 20.2,
// docs/implemented-proof-images/20.2-explain.txt). The user is the one with
// the most memberships, made a member of all 100 rooms; the marks are put
// where the case says. It checks that the query for 100 rooms takes under
// 20 ms (the median) on the worst case, which is the target of the step.
func TestLargeDemoUnreadPlans(t *testing.T) {
	s := openLargeDemo(t)
	ctx := t.Context()
	var userID uint64
	if err := s.db(ctx).Raw(`SELECT user_id FROM room_members GROUP BY user_id ORDER BY COUNT(*) DESC, user_id LIMIT 1`).Scan(&userID).Error; err != nil || userID == 0 {
		t.Fatalf("find the user: %v", err)
	}
	var roomIDs []uint64
	if err := s.db(ctx).Raw(`SELECT id FROM rooms ORDER BY id`).Scan(&roomIDs).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range roomIDs {
		ok, err := s.IsRoomMember(ctx, id, userID)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			if err := s.CreateRoomMember(ctx, &RoomMember{RoomID: id, UserID: userID}); err != nil {
				t.Fatalf("CreateRoomMember: %+v", err)
			}
		}
	}
	// Mentions: 300 unread ones, as a busy user has (once: a second run on
	// the same database finds them).
	var mentioned int64
	if err := s.db(ctx).Raw(`SELECT COUNT(*) FROM notifications WHERE user_id = ? AND kind = 'mention' AND read_at IS NULL`, userID).Scan(&mentioned).Error; err != nil {
		t.Fatal(err)
	}
	if mentioned < 300 {
		if err := s.db(ctx).Exec(`INSERT INTO notifications (user_id, kind, post_id, room_id, actor_id, created_at)
SELECT ?, 'mention', p.id, p.room_id, p.author_id, CURRENT_TIMESTAMP FROM posts p
WHERE p.author_id <> ? AND p.parent_id IS NULL AND p.id % 97 = 0 LIMIT 300`, userID, userID).Error; err != nil {
			t.Fatalf("make mentions: %v", err)
		}
	}
	if s.dialect == dialectPostgres {
		if err := s.Analyze(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var members, topLevel int64
	_ = s.db(ctx).Raw(`SELECT COUNT(*) FROM room_members WHERE user_id = ?`, userID).Scan(&members).Error
	_ = s.db(ctx).Raw(`SELECT COUNT(*) FROM posts WHERE parent_id IS NULL`).Scan(&topLevel).Error
	var busiest uint64
	_ = s.db(ctx).Raw(`SELECT room_id FROM posts GROUP BY room_id ORDER BY COUNT(*) DESC, room_id LIMIT 1`).Scan(&busiest).Error
	fmt.Printf("== unread counts on the large demo (%s): user %d is a member of %d rooms; %d top-level posts; busiest room %d\n\n", s.dialect, userID, members, topLevel, busiest)

	allQuery := fmt.Sprintf(unreadSelect, "", "")
	oneQuery := fmt.Sprintf(unreadSelect, " AND room_id = ?", " AND m.room_id = ?")

	cases := []struct {
		name  string
		reset string // sets the marks
	}{
		{"worst case: nothing seen (every mark 0)", `UPDATE room_reads SET last_seen_id = 0 WHERE user_id = ?`},
		{"half seen (every mark at the middle of the room)", `UPDATE room_reads SET last_seen_id = COALESCE((SELECT (MIN(p.id) + MAX(p.id)) / 2 FROM posts p WHERE p.room_id = room_reads.room_id), 0) WHERE user_id = ?`},
		{"all seen (every mark at the newest ID)", `UPDATE room_reads SET last_seen_id = COALESCE((SELECT MAX(p.id) FROM posts p WHERE p.room_id = room_reads.room_id), 0) WHERE user_id = ?`},
	}
	var worst time.Duration
	for i, c := range cases {
		if err := s.db(ctx).Exec(c.reset, userID).Error; err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if s.dialect == dialectPostgres {
			if err := s.db(ctx).Exec("ANALYZE room_reads").Error; err != nil {
				t.Fatal(err)
			}
		}
		counts, err := s.RoomUnreadCounts(ctx, userID)
		if err != nil {
			t.Fatal(err)
		}
		var posts, mentions int
		for _, u := range counts {
			posts += u.Posts
			mentions += u.Mentions
		}
		fmt.Printf("-- %s\n   %d rooms, %d unread posts, %d unread mentions\n", c.name, len(counts), posts, mentions)
		median, slowest := timeIt(t, 30, func() error { _, err := s.RoomUnreadCounts(ctx, userID); return err })
		fmt.Printf("   RoomUnreadCounts (all %d rooms): median %s, slowest %s over 30 runs\n", len(counts), median.Round(10*time.Microsecond), slowest.Round(10*time.Microsecond))
		if i == 0 {
			worst = median
			// What the query cost before the cap of UnreadPostsCap: the same
			// query with a limit no room reaches.
			uncapped := strings.Replace(allQuery, "LIMIT "+unreadCapSQL, "LIMIT 1000000", 1)
			med, slow := timeIt(t, 30, func() error {
				var rows []RoomUnread
				return s.db(ctx).Raw(uncapped, userID, userID).Scan(&rows).Error
			})
			fmt.Printf("   without the cap (exact counts): median %s, slowest %s over 30 runs\n", med.Round(10*time.Microsecond), slow.Round(10*time.Microsecond))
		}
		median, slowest = timeIt(t, 100, func() error { _, _, err := s.RoomUnreadOf(ctx, userID, busiest); return err })
		fmt.Printf("   RoomUnreadOf (the busiest room): median %s, slowest %s over 100 runs\n", median.Round(10*time.Microsecond), slowest.Round(10*time.Microsecond))
		median, slowest = timeIt(t, 100, func() error { _, err := s.RoomMaxPostID(ctx, busiest); return err })
		fmt.Printf("   RoomMaxPostID (the busiest room): median %s, slowest %s over 100 runs\n", median.Round(10*time.Microsecond), slowest.Round(10*time.Microsecond))
		if i == 0 {
			fmt.Printf("\nPlan of RoomUnreadCounts:\n%s\n\nPlan of RoomUnreadOf (the busiest room):\n%s\n\nPlan of RoomMaxPostID (the busiest room):\n%s\n",
				explainQuery(t, s, allQuery, userID, userID),
				explainQuery(t, s, oneQuery, userID, busiest, userID, busiest),
				explainQuery(t, s, "SELECT id FROM posts WHERE room_id = ? ORDER BY id DESC LIMIT 1", busiest))
		}
		fmt.Println()
	}
	if worst > 20*time.Millisecond {
		t.Errorf("the median of RoomUnreadCounts for %d rooms with nothing seen is %s, over the 20 ms target", len(roomIDs), worst)
	}
}
