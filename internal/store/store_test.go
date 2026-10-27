package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/errors"
	"gorm.io/gorm"
)

func TestOpenPragmas(t *testing.T) {
	s, _, _ := testStore(t, nil)
	tests := []struct {
		pragma string
		want   string
	}{
		{"foreign_keys", "1"},
		{"journal_mode", "wal"},
		{"busy_timeout", "5000"},
		{"synchronous", "1"}, // NORMAL
	}
	for _, tt := range tests {
		t.Run(tt.pragma, func(t *testing.T) {
			var got string
			if err := s.db(t.Context()).Raw("PRAGMA " + tt.pragma).Scan(&got).Error; err != nil {
				t.Fatalf("PRAGMA %s: %v", tt.pragma, err)
			}
			if got != tt.want {
				t.Errorf("PRAGMA %s = %q, want %q", tt.pragma, got, tt.want)
			}
		})
	}
}

// TestPragmasOnEveryConnection checks that the settings are not lost when the
// pool opens another connection: two connections are held open at once.
func TestPragmasOnEveryConnection(t *testing.T) {
	s, _, _ := testStore(t, nil)
	sqlDB, err := s.gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	var conns []interface{ Close() error }
	for range 3 {
		c, err := sqlDB.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		var fk int
		if err := c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatal(err)
		}
		if fk != 1 {
			t.Errorf("foreign_keys = %d on connection %d, want 1", fk, len(conns))
		}
	}
	for _, c := range conns {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}
}

func TestForeignKeysAreEnforced(t *testing.T) {
	s, _, _ := testStore(t, nil)
	ctx := t.Context()
	steps := []string{
		"CREATE TABLE parent (id INTEGER PRIMARY KEY)",
		"CREATE TABLE child (id INTEGER PRIMARY KEY, parent_id INTEGER NOT NULL REFERENCES parent(id))",
	}
	for _, q := range steps {
		if err := s.db(ctx).Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db(ctx).Exec("INSERT INTO child (parent_id) VALUES (42)").Error; err == nil {
		t.Fatal("inserting a child without a parent succeeded, foreign keys are off")
	}
}

func TestBuildDSN(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantMemory bool
		contain    []string
		notContain []string
	}{
		{
			name:    "file",
			path:    "/tmp/a/clowder.db",
			contain: []string{"file:/tmp/a/clowder.db?", "journal_mode%28WAL%29", "_txlock=immediate", "foreign_keys%281%29"},
		},
		{
			name:    "file with characters of a URI",
			path:    "/tmp/a b/c?d#e%f.db",
			contain: []string{"file:/tmp/a b/c%3Fd%23e%25f.db?"},
		},
		{
			name:       "memory",
			path:       "file:x?mode=memory&cache=shared",
			wantMemory: true,
			contain:    []string{"file:x?mode=memory&cache=shared&", "_txlock=immediate"},
			notContain: []string{"journal_mode"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dsn, memory := buildDSN(tt.path)
			if memory != tt.wantMemory {
				t.Errorf("memory = %v, want %v", memory, tt.wantMemory)
			}
			for _, c := range tt.contain {
				if !strings.Contains(dsn, c) {
					t.Errorf("dsn %q does not contain %q", dsn, c)
				}
			}
			for _, c := range tt.notContain {
				if strings.Contains(dsn, c) {
					t.Errorf("dsn %q contains %q", dsn, c)
				}
			}
		})
	}
}

func TestOpenFileWithOddName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a?b#c.db")
	s, err := Open(t.Context(), path, clock.NewFake(fakeStart), nil)
	if err != nil {
		t.Fatalf("Open: %+v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenTwiceMigratesIdempotently(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "twice.db")
	clk := clock.NewFake(fakeStart)
	opts := defaultOptions()
	opts.models = []any{&widget{}}
	opts.migrations = []migration{{ID: "0001_extra", SQL: "CREATE TABLE extra (id INTEGER PRIMARY KEY)"}}

	for i := range 2 {
		s, err := open(ctx, path, clk, nil, opts)
		if err != nil {
			t.Fatalf("open #%d: %+v", i+1, err)
		}
		if i == 0 {
			if err := s.createWidget(ctx, &widget{Name: "kept"}); err != nil {
				t.Fatal(err)
			}
		}
		if n := countWidgets(t, s); n != 1 {
			t.Errorf("open #%d: %d widgets, want 1", i+1, n)
		}
		var applied int64
		if err := s.db(ctx).Model(&schemaMigration{}).Count(&applied).Error; err != nil {
			t.Fatal(err)
		}
		if applied != 1 {
			t.Errorf("open #%d: %d recorded migrations, want 1", i+1, applied)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRawMigrations(t *testing.T) {
	tests := []struct {
		name       string
		migrations []migration
		wantTables []string
		wantIDs    []string
	}{
		{name: "none"},
		{
			name: "two in order",
			migrations: []migration{
				{ID: "0001_a", SQL: "CREATE TABLE a (id INTEGER PRIMARY KEY)"},
				{ID: "0002_b", SQL: "CREATE TABLE b (id INTEGER PRIMARY KEY); INSERT INTO a (id) VALUES (1)"},
			},
			wantTables: []string{"a", "b"},
			wantIDs:    []string{"0001_a", "0002_b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "raw.db")
			clk := clock.NewFake(fakeStart)
			opts := defaultOptions()
			opts.migrations = tt.migrations
			// Opening three times: a migration that ran must not run again
			// (CREATE TABLE would fail the second time).
			for i := range 3 {
				s, err := open(ctx, path, clk, nil, opts)
				if err != nil {
					t.Fatalf("open #%d: %+v", i+1, err)
				}
				var ids []string
				if err := s.db(ctx).Model(&schemaMigration{}).Order("applied_at, id").Pluck("id", &ids).Error; err != nil {
					t.Fatal(err)
				}
				if strings.Join(ids, ",") != strings.Join(tt.wantIDs, ",") {
					t.Errorf("open #%d: recorded %v, want %v", i+1, ids, tt.wantIDs)
				}
				for _, table := range tt.wantTables {
					var n int64
					err := s.db(ctx).Raw("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&n).Error
					if err != nil {
						t.Fatal(err)
					}
					if n != 1 {
						t.Errorf("open #%d: table %s missing", i+1, table)
					}
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestMigrationRecordsAppliedTime(t *testing.T) {
	clk := clock.NewFake(fakeStart.Add(36 * time.Hour))
	opts := defaultOptions()
	opts.migrations = []migration{{ID: "0001_a", SQL: "CREATE TABLE a (id INTEGER PRIMARY KEY)"}}
	s, err := open(t.Context(), filepath.Join(t.TempDir(), "t.db"), clk, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	var rec schemaMigration
	if err := s.db(t.Context()).First(&rec, "id = ?", "0001_a").Error; err != nil {
		t.Fatal(err)
	}
	if !rec.AppliedAt.Equal(clk.Now()) {
		t.Errorf("applied_at = %v, want %v", rec.AppliedAt, clk.Now())
	}
}

func TestFailedMigrationRollsBack(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "fail.db")
	opts := defaultOptions()
	opts.migrations = []migration{
		{ID: "0001_ok", SQL: "CREATE TABLE ok (id INTEGER PRIMARY KEY)"},
		{ID: "0002_bad", SQL: "CREATE TABLE half (id INTEGER PRIMARY KEY); THIS IS NOT SQL"},
	}
	_, err := open(ctx, path, clock.NewFake(fakeStart), nil, opts)
	appErr := requireRoute(t, err, "applyMigration.MigrationFailed")
	if appErr.Tag != tagMigrationFailed || appErr.Metadata["migration"] != "0002_bad" {
		t.Errorf("tag = %s, metadata = %v", appErr.Tag, appErr.Metadata)
	}

	// The first migration stayed; the second left nothing behind.
	opts.migrations = opts.migrations[:1]
	s, err := open(ctx, path, clock.NewFake(fakeStart), nil, opts)
	if err != nil {
		t.Fatalf("reopen: %+v", err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	var n int64
	if err := s.db(ctx).Raw("SELECT count(*) FROM sqlite_master WHERE name = 'half'").Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("table half exists: the failed migration was not rolled back")
	}
}

func TestInvalidMigrations(t *testing.T) {
	tests := []struct {
		name       string
		migrations []migration
	}{
		{"empty id", []migration{{ID: "", SQL: "SELECT 1"}}},
		{"empty sql", []migration{{ID: "a", SQL: ""}}},
		{"duplicate id", []migration{{ID: "a", SQL: "SELECT 1"}, {ID: "a", SQL: "SELECT 2"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := defaultOptions()
			opts.migrations = tt.migrations
			_, err := open(t.Context(), filepath.Join(t.TempDir(), "x.db"), clock.NewFake(fakeStart), nil, opts)
			appErr := requireRoute(t, err, "Store.migrate.MigrationInvalid")
			if appErr.Tag != tagMigrationInvalid {
				t.Errorf("tag = %s", appErr.Tag)
			}
		})
	}
}

func TestOpenFailures(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	tests := []struct {
		name  string
		ctx   context.Context
		path  string
		route string
	}{
		{
			name:  "directory does not exist",
			ctx:   t.Context(),
			path:  filepath.Join(t.TempDir(), "missing", "dir", "x.db"),
			route: "open.StoreOpenFailed",
		},
		{
			name:  "context is done",
			ctx:   canceled,
			path:  filepath.Join(t.TempDir(), "x.db"),
			route: "open->pingAndMigrate.StoreOpenFailed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := Open(tt.ctx, tt.path, clock.NewFake(fakeStart), nil)
			if err == nil {
				_ = s.Close()
				t.Fatal("Open succeeded")
			}
			appErr := requireRoute(t, err, tt.route)
			if appErr.Tag != tagOpenFailed {
				t.Errorf("tag = %s", appErr.Tag)
			}
		})
	}
}

type failingCloser struct{}

func (failingCloser) Close() error { return fmt.Errorf("disk on fire") }

func TestCloseFailure(t *testing.T) {
	s := &Store{closer: failingCloser{}}
	err := s.Close()
	appErr := requireRoute(t, err, "Store.Close.StoreCloseFailed")
	if appErr.Tag != tagCloseFailed {
		t.Errorf("tag = %s", appErr.Tag)
	}
}

func TestPing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		closeDB   bool
		noHandle  bool
		wantRoute string
		wantStep  string
	}{
		{name: "working database"},
		{name: "closed database", closeDB: true, wantRoute: "Store.Ping.StorePingFailed", wantStep: "ping"},
		{name: "no database handle", noHandle: true, wantRoute: "Store.Ping.StorePingFailed", wantStep: "handle"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, _, _ := testStore(t, nil)
			if tt.closeDB {
				if err := s.Close(); err != nil {
					t.Fatalf("Close: %v", err)
				}
			}
			if tt.noHandle {
				// A Store whose GORM handle has no connection pool of database/sql.
				s = &Store{gdb: &gorm.DB{Config: &gorm.Config{}}}
			}
			err := s.Ping(t.Context())
			if tt.wantRoute == "" {
				if err != nil {
					t.Fatalf("Ping: %+v", err)
				}
				return
			}
			appErr := requireRoute(t, err, tt.wantRoute)
			if appErr.Tag != tagPingFailed {
				t.Errorf("tag = %s", appErr.Tag)
			}
			if appErr.Metadata["step"] != tt.wantStep {
				t.Errorf("step = %v, want %s", appErr.Metadata["step"], tt.wantStep)
			}
		})
	}
}

func TestPingHonorsContext(t *testing.T) {
	t.Parallel()
	s, _, _ := testStore(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_ = requireRoute(t, s.Ping(ctx), "Store.Ping.StorePingFailed")
}

func TestCloseInsideTxDoesNothing(t *testing.T) {
	s, _, _ := testStore(t, nil)
	err := s.Tx(t.Context(), func(tx *Store) error { return tx.Close() })
	if err != nil {
		t.Fatalf("Tx: %v", err)
	}
	if n := countWidgets(t, s); n != 0 {
		t.Errorf("%d widgets", n)
	}
}

func TestTx(t *testing.T) {
	boom := errors.New("Boom", "the function failed", "why", "test")
	tests := []struct {
		name string
		fn   func(ctx context.Context, tx *Store) error
		// wantErr is the very error Tx must return, when it is not a store error.
		wantErr error
		// wantRoute is the route of the store error Tx must return, if any.
		wantRoute string
		wantTag   string
		wantCount int64
	}{
		{
			name: "commit",
			fn: func(ctx context.Context, tx *Store) error {
				return tx.createWidget(ctx, &widget{Name: "one"})
			},
			wantCount: 1,
		},
		{
			name: "error rolls back",
			fn: func(ctx context.Context, tx *Store) error {
				if err := tx.createWidget(ctx, &widget{Name: "one"}); err != nil {
					return err
				}
				return boom
			},
			wantErr: boom,
		},
		{
			name: "store error rolls back and keeps its tag",
			fn: func(ctx context.Context, tx *Store) error {
				if err := tx.createWidget(ctx, &widget{Name: "one"}); err != nil {
					return err
				}
				return tx.createWidget(ctx, &widget{Name: "one"})
			},
			wantRoute: "Store.createWidget.Conflict",
			wantTag:   TagConflict,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _, _ := testStore(t, nil)
			ctx := t.Context()
			err := s.Tx(ctx, func(tx *Store) error { return tt.fn(ctx, tx) })
			switch {
			case tt.wantErr != nil:
				if err != tt.wantErr { //nolint:errorlint // identity is the point
					t.Errorf("Tx returned %v, want the very error the function returned", err)
				}
			case tt.wantRoute != "":
				appErr := requireRoute(t, err, tt.wantRoute)
				if appErr.Tag != tt.wantTag {
					t.Errorf("tag = %s, want %s", appErr.Tag, tt.wantTag)
				}
			case err != nil:
				t.Errorf("Tx returned %v", err)
			}
			if n := countWidgets(t, s); n != tt.wantCount {
				t.Errorf("%d widgets after Tx, want %d", n, tt.wantCount)
			}
		})
	}
}

func TestTxErrorIsNotWrapped(t *testing.T) {
	s, _, _ := testStore(t, nil)
	err := s.Tx(t.Context(), func(tx *Store) error {
		return tx.brokenQuery(t.Context())
	})
	appErr := requireRoute(t, err, "Store.brokenQuery.QueryFailed")
	if appErr.Tag != TagQueryFailed {
		t.Errorf("tag = %s, want the inner tag", appErr.Tag)
	}
	// Tx is not in the route: the error was made inside brokenQuery.
	if strings.Contains(appErr.Route(), "TransactionFailed") {
		t.Errorf("route = %s", appErr.Route())
	}
}

func TestTxPanicRollsBackAndRepanics(t *testing.T) {
	s, _, _ := testStore(t, nil)
	ctx := t.Context()
	func() {
		defer func() {
			if r := recover(); r != "kaboom" {
				t.Errorf("recovered %v, want kaboom", r)
			}
		}()
		_ = s.Tx(ctx, func(tx *Store) error {
			if err := tx.createWidget(ctx, &widget{Name: "one"}); err != nil {
				return err
			}
			panic("kaboom")
		})
		t.Error("Tx returned after a panic")
	}()
	if n := countWidgets(t, s); n != 0 {
		t.Errorf("%d widgets after the panic, want 0", n)
	}
	// The connection went back to the pool unlocked.
	if err := s.Tx(ctx, func(tx *Store) error { return tx.createWidget(ctx, &widget{Name: "two"}) }); err != nil {
		t.Fatalf("Tx after the panic: %v", err)
	}
}

func TestTxBeginFailure(t *testing.T) {
	s, _, _ := testStore(t, nil)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	err := s.Tx(canceled, func(tx *Store) error { return nil })
	appErr := requireRoute(t, err, "Store.Tx.TransactionFailed")
	if appErr.Tag != tagTxFailed {
		t.Errorf("tag = %s", appErr.Tag)
	}
}

func TestNestedTx(t *testing.T) {
	s, _, _ := testStore(t, nil)
	ctx := t.Context()
	boom := errors.New("Boom", "inner failed")
	err := s.Tx(ctx, func(outer *Store) error {
		if err := outer.createWidget(ctx, &widget{Name: "outer"}); err != nil {
			return err
		}
		innerErr := outer.Tx(ctx, func(inner *Store) error {
			if err := inner.createWidget(ctx, &widget{Name: "inner"}); err != nil {
				return err
			}
			return boom
		})
		if innerErr != boom { //nolint:errorlint // identity is the point
			t.Errorf("inner Tx returned %v", innerErr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := countWidgets(t, s); n != 1 {
		t.Errorf("%d widgets, want 1 (the inner one rolled back)", n)
	}
}

func TestErrorTags(t *testing.T) {
	tests := []struct {
		name     string
		run      func(ctx context.Context, s *Store) error
		route    string
		wantTag  string
		wantMeta string
	}{
		{
			name:     "not found",
			run:      func(ctx context.Context, s *Store) error { _, err := s.getWidget(ctx, 404); return err },
			route:    "Store.getWidget.NotFound",
			wantTag:  TagNotFound,
			wantMeta: "getWidget",
		},
		{
			name: "conflict on a unique column",
			run: func(ctx context.Context, s *Store) error {
				if err := s.createWidget(ctx, &widget{Name: "same"}); err != nil {
					return err
				}
				return s.createWidget(ctx, &widget{Name: "same"})
			},
			route:    "Store.createWidget.Conflict",
			wantTag:  TagConflict,
			wantMeta: "createWidget",
		},
		{
			name: "conflict on the primary key",
			run: func(ctx context.Context, s *Store) error {
				if err := s.createWidget(ctx, &widget{ID: 7, Name: "a"}); err != nil {
					return err
				}
				return s.createWidget(ctx, &widget{ID: 7, Name: "b"})
			},
			route:    "Store.createWidget.Conflict",
			wantTag:  TagConflict,
			wantMeta: "createWidget",
		},
		{
			name:     "query failed",
			run:      func(ctx context.Context, s *Store) error { return s.brokenQuery(ctx) },
			route:    "Store.brokenQuery.QueryFailed",
			wantTag:  TagQueryFailed,
			wantMeta: "brokenQuery",
		},
		{
			name: "query failed on a cancelled context",
			run: func(ctx context.Context, s *Store) error {
				c, cancel := context.WithCancel(ctx)
				cancel()
				_, err := s.getWidget(c, 1)
				return err
			},
			route:    "Store.getWidget.QueryFailed",
			wantTag:  TagQueryFailed,
			wantMeta: "getWidget",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _, _ := testStore(t, nil)
			err := tt.run(t.Context(), s)
			appErr := requireRoute(t, err, tt.route)
			if appErr.Tag != tt.wantTag {
				t.Errorf("tag = %s, want %s", appErr.Tag, tt.wantTag)
			}
			if appErr.Metadata["op"] != tt.wantMeta {
				t.Errorf("metadata = %v", appErr.Metadata)
			}
			if appErr.OriginalErr == nil {
				t.Error("the database error is lost")
			}
		})
	}
}

func TestClassify(t *testing.T) {
	s, _, _ := testStore(t, nil)
	ctx := t.Context()
	if err := s.createWidget(ctx, &widget{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	var w widget
	notFound := s.db(ctx).First(&w, 999).Error
	conflict := s.db(ctx).Create(&widget{Name: "a"}).Error
	other := s.db(ctx).Exec("SELECT * FROM nope").Error
	tests := []struct {
		name string
		err  error
		want failure
	}{
		{"not found", notFound, failureNotFound},
		{"conflict", conflict, failureConflict},
		{"other", other, failureOther},
		{"wrapped not found", errors.NewErr("X", notFound), failureNotFound},
		{"nil", nil, failureOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classify(tt.err); got != tt.want {
				t.Errorf("classify = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestConcurrentWriters(t *testing.T) {
	s, _, _ := testStore(t, nil)
	ctx := t.Context()
	const writers, perWriter = 8, 10
	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perWriter {
				errs <- s.Tx(ctx, func(tx *Store) error {
					// Read, then write: with a deferred lock this would fail
					// with SQLITE_BUSY at the upgrade.
					var n int64
					if err := tx.db(ctx).Model(&widget{}).Count(&n).Error; err != nil {
						return err
					}
					return tx.createWidget(ctx, &widget{Name: fmt.Sprintf("w%d-%d", w, i)})
				})
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("transaction failed: %+v", err)
		}
	}
	if n := countWidgets(t, s); n != writers*perWriter {
		t.Errorf("%d widgets, want %d", n, writers*perWriter)
	}
}

func TestNowFuncUsesTheClock(t *testing.T) {
	s, clk, _ := testStore(t, nil)
	ctx := t.Context()
	w := widget{Name: "timed"}
	if err := s.createWidget(ctx, &w); err != nil {
		t.Fatal(err)
	}
	if !w.CreatedAt.Equal(fakeStart) || w.CreatedAt.Location() != time.UTC {
		t.Errorf("CreatedAt = %v (%v), want %v UTC", w.CreatedAt, w.CreatedAt.Location(), fakeStart)
	}
	clk.Advance(3*time.Hour + 4*time.Nanosecond)
	got, err := s.getWidget(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CreatedAt.Equal(fakeStart) || got.CreatedAt.Location() != time.UTC {
		t.Errorf("CreatedAt read back = %v (%v), want %v UTC", got.CreatedAt, got.CreatedAt.Location(), fakeStart)
	}
	if err := s.db(ctx).Model(&got).Update("name", "renamed").Error; err != nil {
		t.Fatal(err)
	}
	got, err = s.getWidget(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := clk.Now(); !got.UpdatedAt.Equal(want) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, want)
	}
}

func TestGormLogger(t *testing.T) {
	const secret = "the-body-of-a-post"
	tests := []struct {
		name     string
		slow     time.Duration
		wantLogs bool
	}{
		{"fast queries are not logged", time.Hour, false},
		{"slow queries are logged", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _, logs := testStore(t, func(o *options) { o.slowQuery = tt.slow })
			logs.Reset() // the migration's own queries
			ctx := t.Context()
			if err := s.createWidget(ctx, &widget{Name: secret}); err != nil {
				t.Fatal(err)
			}
			// A not-found query and a failing one are errors, not slow queries.
			if _, err := s.getWidget(ctx, 404); err == nil {
				t.Fatal("expected not found")
			}
			if err := s.brokenQuery(ctx); err == nil {
				t.Fatal("expected a failing query")
			}
			out := logs.String()
			if strings.Contains(out, secret) {
				t.Errorf("the log has a query argument: %s", out)
			}
			if got := strings.Contains(out, `"slow query"`); got != tt.wantLogs {
				t.Errorf("slow query logged = %v, want %v\n%s", got, tt.wantLogs, out)
			}
			if !tt.wantLogs && out != "" {
				t.Errorf("unexpected log: %s", out)
			}
			if tt.wantLogs && !strings.Contains(out, `"level":"WARN"`) {
				t.Errorf("not a warning: %s", out)
			}
		})
	}
}

func TestGormLoggerOnlyLogsSlowQueries(t *testing.T) {
	var out strings.Builder
	l := newGormLogger(newTestLogger(&out), 200*time.Millisecond)
	ctx := t.Context()
	l.Info(ctx, "info")
	l.Warn(ctx, "warn")
	l.Error(ctx, "error")
	l.LogMode(4).Info(ctx, "info again")
	fc := func() (string, int64) { return "SELECT 1", 1 }
	l.Trace(ctx, time.Now(), fc, nil)
	l.Trace(ctx, time.Now(), fc, errors.New("Boom", "failed"))
	if out.Len() != 0 {
		t.Fatalf("logged: %s", out.String())
	}
	l.Trace(ctx, time.Now().Add(-time.Second), fc, nil)
	if !strings.Contains(out.String(), "SELECT 1") {
		t.Errorf("a one-second query was not logged: %q", out.String())
	}
}

// TestOpenRestrictsTheDatabaseFile is finding S8: the database file, which
// holds the password hashes, is readable by its owner only, whether it is made
// by Open or was there before with wider permissions, and so are the files that
// SQLite adds to it.
func TestOpenRestrictsTheDatabaseFile(t *testing.T) {
	tests := []struct {
		name string
		// before is the mode of a file that exists already; 0 for none.
		before os.FileMode
	}{
		{"a new database", 0},
		{"an old database readable by everyone", 0o644},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "x.db")
			if tt.before != 0 {
				if err := os.WriteFile(path, nil, tt.before); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, tt.before); err != nil {
					t.Fatal(err)
				}
			}
			s, err := Open(t.Context(), path, clock.NewFake(fakeStart), nil)
			if err != nil {
				t.Fatalf("Open: %+v", err)
			}
			defer func() { _ = s.Close() }()
			for _, name := range []string{path, path + "-wal", path + "-shm"} {
				info, err := os.Stat(name)
				if err != nil {
					if name == path {
						t.Fatal(err)
					}
					continue // not made by this SQLite build
				}
				if info.Mode().Perm()&0o077 != 0 {
					t.Errorf("%s has the mode %v, want no access for group and others", filepath.Base(name), info.Mode().Perm())
				}
			}
		})
	}
}
