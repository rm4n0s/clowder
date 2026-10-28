package store

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/errors"
	"gorm.io/driver/postgres"
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

// dialect says which database a Store runs on (SPEC D38). Everything that
// differs between them is chosen by it inside this package and nowhere else:
// the raw migrations, the full-text index and search, the pool, the mapping of
// a unique violation, BulkCreate and the stats. The zero value is SQLite.
type dialect int

const (
	// dialectSQLite is SQLite through the pure-Go driver, the default.
	dialectSQLite dialect = iota
	// dialectPostgres is PostgreSQL through pgx.
	dialectPostgres
)

// String names the dialect for logs and test output.
func (d dialect) String() string {
	if d == dialectPostgres {
		return "postgres"
	}
	return "sqlite"
}

// poolSize returns the number of connections the pool of a file database or
// of a PostgreSQL database may open, all of which it keeps open. database/sql
// keeps only two idle connections by default and opens a new one whenever a
// query finds none free, and every new SQLite connection parses the whole
// schema: with 200 open streams that each read the same post, the server spent
// half its CPU opening connections and held a page cache for each (13.1,
// docs/PERFORMANCE.md). A bounded pool queues the queries instead.
func poolSize(cpus int) int {
	return max(minOpenConns, min(2*cpus, maxOpenConns))
}

// Store is the database of Clowder: the only thing that knows GORM, SQLite and
// PostgreSQL (SPEC D6, D38). Make it with Open or OpenPostgres and release it
// with Close. A Store is safe for concurrent use.
type Store struct {
	gdb     *gorm.DB
	clk     clock.Clock
	logger  *slog.Logger
	dialect dialect
	// closer closes the connection pool. It is nil in the Store that Tx hands
	// to its function, which must not close the database.
	closer io.Closer
	// pool is the connection pool, for PoolStats. It is nil in the Store that
	// Tx hands to its function.
	pool *sql.DB
}

// options holds what Open fixes and the tests change.
type options struct {
	// dialect is the database to open; the zero value is SQLite.
	dialect dialect
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
		migrations: rawMigrations(dialectSQLite),
		slowQuery:  slowQueryThreshold,
	}
}

// postgresOptions are the options of OpenPostgres.
func postgresOptions() options {
	return options{
		dialect:    dialectPostgres,
		models:     allModels(),
		migrations: rawMigrations(dialectPostgres),
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

// OpenPostgres opens the PostgreSQL database at databaseURL (postgres:// or
// postgresql://), and brings its schema up to date, like Open does for SQLite:
// the time of every row comes from clk, in UTC; a query slower than 200 ms is
// logged to logger as a warning, without its arguments; the pool has the same
// bounds as the pool of a file database (SPEC D34). The URL holds the
// password: it never appears in an error or a log, only with the password
// replaced by "xxxxx". The caller closes the Store with Close.
func OpenPostgres(ctx context.Context, databaseURL string, clk clock.Clock, logger *slog.Logger) (*Store, error) {
	return open(ctx, databaseURL, clk, logger, postgresOptions())
}

// open is Open and OpenPostgres with the options as a parameter. target is the
// path of the SQLite database or the URL of the PostgreSQL one, by
// opts.dialect.
func open(ctx context.Context, target string, clk clock.Clock, logger *slog.Logger, opts options) (*Store, error) {
	if clk == nil {
		clk = clock.Real()
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	var (
		dialector gorm.Dialector
		memory    bool
		// where names the database in error metadata: the path of the
		// SQLite file, or the PostgreSQL URL without its password.
		whereKey, where = "path", target
	)
	switch opts.dialect {
	case dialectPostgres:
		whereKey, where = "databaseURL", config.RedactURL(target)
		connConfig, err := pgx.ParseConfig(target)
		if err != nil {
			// The message of pgx can quote the URL: keep it out of the error.
			return nil, errors.New(tagOpenFailed, "the database URL cannot be parsed", whereKey, where, "step", "parse")
		}
		// timestamptz comes back in UTC, like the times of SQLite, and not in
		// the zone of the process.
		utc := stdlib.OptionAfterConnect(func(_ context.Context, conn *pgx.Conn) error {
			conn.TypeMap().RegisterType(&pgtype.Type{
				Name: "timestamptz", OID: pgtype.TimestamptzOID,
				Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC},
			})
			return nil
		})
		dialector = postgres.New(postgres.Config{Conn: stdlib.OpenDB(*connConfig, utc)})
	default:
		var dsn string
		dsn, memory = buildDSN(target)
		if !memory && !strings.HasPrefix(target, "file:") {
			// The file holds password hashes and sessions: only its owner reads
			// it, whatever the directory allows. SQLite gives the -wal and -shm
			// files the mode of the database file.
			if err := restrictFile(target); err != nil {
				return nil, errors.NewErr(tagOpenFailed, err).SetMetadata("path", target, "step", "permissions")
			}
		}
		dialector = sqlite.Open(dsn)
	}
	gdb, err := gorm.Open(dialector, &gorm.Config{
		NowFunc:        func() time.Time { return clk.Now().UTC() },
		TranslateError: true,
		Logger:         newGormLogger(logger, opts.slowQuery),
		// GORM's own ping takes no context: for PostgreSQL the ping of
		// pingAndMigrate does it, so that a stop signal ends the wait for a
		// server that does not answer.
		DisableAutomaticPing: opts.dialect == dialectPostgres,
	})
	if err != nil {
		return nil, errors.NewErr(tagOpenFailed, err).SetMetadata(whereKey, where)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, errors.NewErr(tagOpenFailed, err).SetMetadata(whereKey, where)
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
	s := &Store{gdb: gdb, clk: clk, logger: logger, closer: sqlDB, pool: sqlDB, dialect: opts.dialect}
	if err := pingAndMigrate(ctx, s, sqlDB, opts); err != nil {
		if closeErr := sqlDB.Close(); closeErr != nil {
			logger.Warn("closing the database after a failed open failed", "err", closeErr)
		}
		return nil, err
	}
	return s, nil
}

// databaseMode is the mode of the database file: its owner reads and writes it.
const databaseMode = 0o600

// restrictFile creates the file when it is missing, with the mode 0600, and
// takes the group and other permissions from one that exists.
func restrictFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, databaseMode)
	if err != nil {
		return err
	}
	if err := f.Chmod(databaseMode); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
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

// PoolStats returns the statistics of the connection pool: the connections
// open, in use and idle, and how often and how long a query waited for one
// (SPEC D60). It is the zero value in the Store of a transaction.
func (s *Store) PoolStats() sql.DBStats {
	if s.pool == nil {
		return sql.DBStats{}
	}
	return s.pool.Stats()
}

// Ping checks that the database answers, within ctx. It is what the health
// check of the server asks (SPEC D40).
func (s *Store) Ping(ctx context.Context) error {
	sqlDB, err := s.gdb.DB()
	if err != nil {
		return errors.NewErr(tagPingFailed, err).SetMetadata("step", "handle")
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		return errors.NewErr(tagPingFailed, err).SetMetadata("step", "ping")
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
		fnErr = fn(&Store{gdb: g, clk: s.clk, logger: s.logger, dialect: s.dialect})
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
	tagPingFailed  = "StorePingFailed"
	tagTxFailed    = "TransactionFailed"
)
