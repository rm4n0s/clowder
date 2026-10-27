package store

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/errors"
	"gorm.io/gorm"
)

const (
	// busyTimeoutMillis is how long a connection waits for a lock held by
	// another one before it fails with SQLITE_BUSY (SPEC §17).
	busyTimeoutMillis = "5000"
	// slowQueryThreshold is the duration above which a query is logged.
	slowQueryThreshold = 200 * time.Millisecond
	// memoryMarker is the DSN parameter that makes a database live in memory.
	memoryMarker = "mode=memory"
	// minOpenConns and maxOpenConns bound the size of the connection pool of
	// a file database; the pool has two connections per CPU within these
	// bounds (see poolSize).
	minOpenConns = 8
	maxOpenConns = 32
)

// poolSize returns the number of connections the pool of a file database may
// open, all of which it keeps open. database/sql keeps only two idle
// connections by default and opens a new one whenever a query finds none
// free, and every new SQLite connection parses the whole schema: with 200
// open streams that each read the same post, the server spent half its CPU
// opening connections and held a page cache for each (13.1, docs/PERFORMANCE.md).
// A bounded pool queues the queries instead.
func poolSize(cpus int) int {
	return max(minOpenConns, min(2*cpus, maxOpenConns))
}

// Store is the database of Clowder: the only thing that knows GORM and SQLite
// (SPEC D6). Make it with Open and release it with Close. A Store is safe for
// concurrent use.
type Store struct {
	gdb    *gorm.DB
	clk    clock.Clock
	logger *slog.Logger
	// closer closes the connection pool. It is nil in the Store that Tx hands
	// to its function, which must not close the database.
	closer io.Closer
}

// options holds what Open fixes and the tests change.
type options struct {
	// models are the GORM models that AutoMigrate creates and updates.
	models []any
	// migrations are the raw SQL migrations, in the order they are applied.
	migrations []migration
	// slowQuery is the duration above which a query is logged.
	slowQuery time.Duration
}

// defaultOptions are the options of Open.
func defaultOptions() options {
	return options{
		models:     allModels(),
		migrations: rawMigrations(),
		slowQuery:  slowQueryThreshold,
	}
}

// Open opens the SQLite database in the file path, creating it if needed, and
// brings its schema up to date. The time of every row comes from clk, in UTC.
// A query slower than 200 ms is logged to logger as a warning, without its
// arguments. path may also be a SQLite "file:" URI, whose own parameters are
// kept. The caller closes the Store with Close.
func Open(ctx context.Context, path string, clk clock.Clock, logger *slog.Logger) (*Store, error) {
	return open(ctx, path, clk, logger, defaultOptions())
}

// open is Open with the options as a parameter.
func open(ctx context.Context, path string, clk clock.Clock, logger *slog.Logger, opts options) (*Store, error) {
	if clk == nil {
		clk = clock.Real()
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	dsn, memory := buildDSN(path)
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		NowFunc:        func() time.Time { return clk.Now().UTC() },
		TranslateError: true,
		Logger:         newGormLogger(logger, opts.slowQuery),
	})
	if err != nil {
		return nil, errors.NewErr(tagOpenFailed, err).SetMetadata("path", path)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, errors.NewErr(tagOpenFailed, err).SetMetadata("path", path)
	}
	if memory {
		// A shared-cache in-memory database lives while a connection is open
		// and locks by table, not by busy timeout: one connection keeps it
		// alive and avoids SQLITE_LOCKED.
		sqlDB.SetMaxOpenConns(1)
	} else {
		n := poolSize(runtime.NumCPU())
		sqlDB.SetMaxOpenConns(n)
		sqlDB.SetMaxIdleConns(n)
	}
	s := &Store{gdb: gdb, clk: clk, logger: logger, closer: sqlDB}
	if err := pingAndMigrate(ctx, s, sqlDB, opts); err != nil {
		if closeErr := sqlDB.Close(); closeErr != nil {
			logger.Warn("closing the database after a failed open failed", "err", closeErr)
		}
		return nil, err
	}
	return s, nil
}

// pingAndMigrate checks that the database answers and migrates it. A failed
// ping is tagged here, where it first appears.
func pingAndMigrate(ctx context.Context, s *Store, sqlDB *sql.DB, opts options) error {
	if err := sqlDB.PingContext(ctx); err != nil {
		return errors.NewErr(tagOpenFailed, err).SetMetadata("step", "ping")
	}
	return s.migrate(ctx, opts.models, opts.migrations)
}

// buildDSN makes the driver's DSN from path and reports whether the database
// lives in memory. Every connection of the pool gets these settings:
//
//   - foreign_keys(1): foreign keys are enforced (SQLite ignores them by default)
//   - busy_timeout(5000): a connection waits up to 5 s for a lock
//   - synchronous(NORMAL): safe with WAL and faster than the default
//   - journal_mode(WAL): readers do not block the writer (file databases only)
//   - _txlock=immediate: a transaction takes the write lock at BEGIN, so it
//     never fails later when a read is upgraded to a write
func buildDSN(path string) (dsn string, memory bool) {
	memory = strings.Contains(path, memoryMarker)
	pragmas := []string{
		"busy_timeout(" + busyTimeoutMillis + ")",
		"foreign_keys(1)",
		"synchronous(NORMAL)",
	}
	if !memory {
		pragmas = append(pragmas, "journal_mode(WAL)")
	}
	params := url.Values{"_pragma": pragmas, "_txlock": {"immediate"}}.Encode()

	base := path
	if !strings.HasPrefix(path, "file:") {
		base = "file:" + uriPathEscaper.Replace(path)
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + params, memory
}

// uriPathEscaper escapes what has a meaning in the path of a SQLite URI.
var uriPathEscaper = strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23")

// Close closes the database. Call it once, after everything that uses the
// Store has stopped. On the Store that Tx passes to its function it does
// nothing.
func (s *Store) Close() error {
	if s.closer == nil {
		return nil
	}
	if err := s.closer.Close(); err != nil {
		return errors.NewErr(tagCloseFailed, err)
	}
	return nil
}

// db returns the GORM handle for one operation, bound to ctx. It is
// unexported: only this package touches GORM.
func (s *Store) db(ctx context.Context) *gorm.DB {
	return s.gdb.WithContext(ctx)
}

// Tx runs fn in one transaction. It commits when fn returns nil. When fn
// returns an error, Tx rolls back and returns that error as it is, without
// wrapping it. When fn panics, Tx rolls back and the panic goes on. fn must
// use the Store it is given, not the one Tx was called on. Called on that
// Store, Tx nests: the inner transaction is a savepoint.
func (s *Store) Tx(ctx context.Context, fn func(tx *Store) error) error {
	var fnErr error
	err := s.db(ctx).Transaction(func(g *gorm.DB) error {
		fnErr = fn(&Store{gdb: g, clk: s.clk, logger: s.logger})
		return fnErr
	})
	if err == nil {
		return nil
	}
	if fnErr != nil {
		return fnErr
	}
	// The transaction could not begin or commit.
	return errors.NewErr(tagTxFailed, err)
}

const (
	tagOpenFailed  = "StoreOpenFailed"
	tagCloseFailed = "StoreCloseFailed"
	tagTxFailed    = "TransactionFailed"
)
