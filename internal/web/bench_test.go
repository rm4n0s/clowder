package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/markdown"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/clowder/internal/store/storetest"
	"github.com/rm4n0s/clowder/internal/web/static"
)

// The benchmarks of this file measure the targets of SPEC §10 on the large
// demo. They skip unless CLOWDER_BENCH_DB names its database file, so the
// default test run does not pay for them:
//
//	CLOWDER_BENCH_DB=/path/to/clowder.db go test ./internal/web -run '^$' -bench Large

// benchServer is the server of the benchmarks, on a copy of the large demo.
type benchServer struct {
	handler http.Handler
	cookie  *http.Cookie
	// busiest is the room with the most posts and comments.
	busiest uint64
	// general is the room every user is in.
	general uint64
}

// newBenchServer opens a copy of the large demo, makes the server with the
// real services and logs alice in.
func newBenchServer(b *testing.B) *benchServer {
	b.Helper()
	clk := clock.Real()
	st := storetest.OpenLarge(b, clk)
	cfg := config.Default()
	logger := slog.New(slog.DiscardHandler)
	fileStore, err := files.New(filepath.Join(b.TempDir(), "files"))
	if err != nil {
		b.Fatalf("files.New: %+v", err)
	}
	hub := live.New()
	b.Cleanup(hub.Close)
	postsConfig := app.PostsConfig{MaxAttachmentSize: cfg.MaxAttachmentSize, MaxAttachments: cfg.MaxAttachmentsPerPost, Logger: logger}
	accounts := app.NewAccounts(st, clk, auth.FastHasher(), app.AccountsConfig{
		InvitationLifetime: cfg.InvitationLifetime, SessionLifetime: cfg.SessionLifetime,
	})
	srv, err := newServer(cfg, logger, Services{
		Accounts:      accounts,
		Rooms:         app.NewRooms(st, fileStore, logger, hub),
		Audit:         app.NewAudit(st),
		Board:         app.NewBoard(st, clk, hub),
		Search:        app.NewSearch(st),
		Notifications: app.NewNotifications(st, hub),
		Posts:         app.NewPosts(st, clk, markdown.New(), fileStore, postsConfig, hub),
		Messages:      app.NewMessages(st, clk, fileStore, postsConfig, hub),
		Hub:           hub,
	}, static.FS)
	if err != nil {
		b.Fatalf("newServer: %v", err)
	}
	token, _, err := accounts.Login(b.Context(), "alice", "demo-password-1234", "192.0.2.1")
	if err != nil {
		b.Fatalf("Login: %+v", err)
	}
	counts, err := st.RoomPostCounts(b.Context())
	if err != nil || len(counts) == 0 {
		b.Fatalf("RoomPostCounts = %v, %v", counts, err)
	}
	general := roomIDByName(b, st, "general")
	return &benchServer{
		handler: srv.Handler(),
		cookie:  &http.Cookie{Name: sessionCookieName, Value: token},
		busiest: counts[0].RoomID,
		general: general,
	}
}

// roomIDByName returns the ID of the room with the name.
func roomIDByName(b *testing.B, st *store.Store, name string) uint64 {
	b.Helper()
	rooms, err := st.ListRooms(b.Context())
	if err != nil {
		b.Fatalf("ListRooms: %+v", err)
	}
	for _, r := range rooms {
		if r.Name == name {
			return r.ID
		}
	}
	b.Fatalf("the large demo has no room %q", name)
	return 0
}

// get runs a GET of the target as alice through the whole handler and checks
// that it answers 200.
func (s *benchServer) get(b *testing.B, target string) {
	b.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.AddCookie(s.cookie)
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		b.Fatalf("GET %s = %d", target, rec.Code)
	}
	_, _ = io.Copy(io.Discard, rec.Body)
}

// BenchmarkLargeRoomFeed measures the whole handler of GET /rooms/{id}, the
// room page with its first 30 posts, in the busiest room and in general
// (target: under 50 ms, SPEC §10).
func BenchmarkLargeRoomFeed(b *testing.B) {
	s := newBenchServer(b)
	for _, room := range []struct {
		name string
		id   uint64
	}{{"busiest room", s.busiest}, {"general", s.general}} {
		b.Run(room.name, func(b *testing.B) {
			target := "/rooms/" + strconv.FormatUint(room.id, 10)
			s.get(b, target)
			b.ResetTimer()
			for b.Loop() {
				s.get(b, target)
			}
		})
	}
}

// BenchmarkLargeSearch measures GET /search through the handler, for a search
// with words and two tags and for a few others (target for the first: under
// 100 ms, SPEC §10).
func BenchmarkLargeSearch(b *testing.B) {
	s := newBenchServer(b)
	for _, q := range searchBenchQueries {
		b.Run(q.name, func(b *testing.B) {
			target := "/search?q=" + url.QueryEscape(q.query)
			s.get(b, target)
			b.ResetTimer()
			for b.Loop() {
				s.get(b, target)
			}
		})
	}
}

// searchBenchQueries are the searches of BenchmarkLargeSearch. The first is
// the one of the target: words and two tags.
var searchBenchQueries = []struct{ name, query string }{
	{"words and two tags", "review deploy #release #bug"},
	{"common word and two tags", "the #docs #backend"},
	{"two words", "release plan"},
	{"one tag", "#design"},
}
