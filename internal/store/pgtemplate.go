package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/errors"
)

// PostgresTestEnv is the environment variable that makes the tests run on
// PostgreSQL: the URL of a server where the user may create databases, for
// example postgres://clowder:clowder@127.0.0.1:5432/postgres?sslmode=disable
// (SPEC D39). Without it the tests run on SQLite.
const PostgresTestEnv = "CLOWDER_TEST_POSTGRES"

const (
	// pgTemplatePrefix starts the name of the migrated template database that
	// NewPostgresTemplate makes; pgCopyPrefix starts the name of each copy.
	// The name goes on with the Unix time of its creation in seconds and a
	// random suffix, so that the leftovers of a crashed run can be found by
	// their prefix and age, and two test binaries never collide.
	pgTemplatePrefix = "clowder_tmpl_"
	pgCopyPrefix     = "clowder_t_"
	// pgLeftoverAge is how old a database of those prefixes must be before the
	// next run drops it as the leftover of a crashed one. A test binary lives
	// for minutes, so no running binary has a database this old.
	pgLeftoverAge = time.Hour
	// pgAdminConns bounds the connections of the administration pool.
	pgAdminConns = 4
	// pgDropTimeout bounds the removal of a database in the function that
	// Open returns.
	pgDropTimeout = 30 * time.Second
	// sqlStateObjectInUse is the SQLSTATE of PostgreSQL for "source database
	// is being accessed by other users".
	sqlStateObjectInUse = "55006"
	// pgCreateRetries and pgCreateWait bound the retries of CREATE DATABASE
	// while another session still holds the source: the backend of a closed
	// connection ends a moment after the client closed it.
	pgCreateRetries = 100
	pgCreateWait    = 50 * time.Millisecond
)

// PostgresTemplate is the PostgreSQL counterpart of Template, for tests: one
// migrated database on a server, from which Open makes a fresh database for
// each test with CREATE DATABASE … TEMPLATE, which copies it at the speed of a
// file copy instead of migrating again. It needs a user that may create and
// drop databases. A PostgresTemplate is safe for concurrent use; the copies
// are made one at a time, because PostgreSQL refuses to copy a database that
// another session is using, and a concurrent copy is such a session. Leftover
// databases of a crashed run are dropped, by name prefix and age, when a new
// PostgresTemplate is made. It is for tests (see package storetest); the
// template database itself stays until the next run drops it, because the
// binary that made it has no hook to run at its end.
type PostgresTemplate struct {
	admin *sql.DB
	// base is the server's URL; databaseURL changes its database name.
	base *url.URL
	// name is the template database.
	name string
	// mu serializes the copies.
	mu sync.Mutex
}

// NewPostgresTemplate connects to the server at serverURL (postgres:// or
// postgresql://, any database name: it is only the door), drops the leftover
// databases of earlier runs, makes a template database and migrates it with
// OpenPostgres. The URL holds the password: it never appears in an error, only
// with the password replaced by "xxxxx". It fails with TemplateURLInvalid when
// the URL is not one of PostgreSQL, TemplateServerFailed when the server does
// not answer, TemplateSweepFailed, TemplateCreateFailed and TemplateDropFailed
// when a database cannot be listed, made or dropped, and with the tags of
// OpenPostgres when the migration fails.
func NewPostgresTemplate(ctx context.Context, serverURL string) (*PostgresTemplate, error) {
	base, urlErr := url.Parse(serverURL)
	connConfig, cfgErr := pgx.ParseConfig(serverURL)
	if urlErr != nil || cfgErr != nil || (base.Scheme != "postgres" && base.Scheme != "postgresql") {
		// The messages of url and pgx can quote the URL: keep them out.
		return nil, errors.New(tagTemplateURLInvalid, "the URL is not a PostgreSQL URL",
			"databaseURL", config.RedactURL(serverURL))
	}
	admin := stdlib.OpenDB(*connConfig)
	admin.SetMaxOpenConns(pgAdminConns)
	t := &PostgresTemplate{admin: admin, base: base}
	if err := admin.PingContext(ctx); err != nil {
		_ = admin.Close()
		return nil, errors.NewErr(tagTemplateServerFailed, err).SetMetadata("databaseURL", config.RedactURL(serverURL))
	}
	if err := t.sweep(ctx); err != nil {
		_ = admin.Close()
		return nil, err
	}
	t.name = newDatabaseName(pgTemplatePrefix)
	if err := t.createDatabase(ctx, t.name, ""); err != nil {
		_ = admin.Close()
		return nil, err
	}
	s, err := OpenPostgres(ctx, t.databaseURL(t.name), clock.Real(), nil)
	if err != nil {
		_ = t.dropDatabase(ctx, t.name)
		_ = admin.Close()
		return nil, err
	}
	// The copies need a template that no session uses.
	if err := s.Close(); err != nil {
		_ = t.dropDatabase(ctx, t.name)
		_ = admin.Close()
		return nil, err
	}
	return t, nil
}

// Open makes a new database as a copy of the template and opens it like
// OpenPostgres does, without migrating the models: the copy is current. The time of every
// row comes from clk. A nil logger discards the log. The returned function
// closes the Store and drops the database, waiting for at most 30 seconds, and
// is called once, when the test ends (it does not depend on a context that the
// end of the test has canceled). It fails with TemplateCreateFailed when the
// copy cannot be made and with the tags of OpenPostgres when it cannot be
// opened.
func (t *PostgresTemplate) Open(ctx context.Context, clk clock.Clock, logger *slog.Logger) (*Store, func() error, error) {
	name := newDatabaseName(pgCopyPrefix)
	t.mu.Lock()
	err := t.createDatabase(ctx, name, t.name)
	t.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	// No models and no raw migrations, like Template.Open: the copy is
	// current, and checking every table and column costs most of the time of
	// a test.
	s, err := open(ctx, t.databaseURL(name), clk, logger, options{dialect: dialectPostgres, slowQuery: slowQueryThreshold})
	if err != nil {
		dropCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pgDropTimeout)
		defer cancel()
		_ = t.dropDatabase(dropCtx, name)
		return nil, nil, err
	}
	release := func() error {
		closeErr := s.Close()
		dropCtx, cancel := context.WithTimeout(context.Background(), pgDropTimeout)
		defer cancel()
		if err := t.dropDatabase(dropCtx, name); err != nil {
			return err
		}
		return closeErr
	}
	return s, release, nil
}

// NewPostgresDatabase makes a new, empty database on the server at serverURL
// (postgres:// or postgresql://, any database name: it is only the door) and
// returns its URL, with the password the caller gave in serverURL, and the
// function that drops it, ending the sessions that still use it, and closes
// the connection used to make it. It is for tests and tools that start more
// than one process on one database, such as the browser tests of the
// processes of a cluster (SPEC D41); it needs a user that may create and drop
// databases. The name starts like the copies of a PostgresTemplate, so that
// the next NewPostgresTemplate drops it if a crash left it behind. The URL
// holds the password: never log it. It fails with TemplateURLInvalid when
// serverURL is not one of PostgreSQL, TemplateServerFailed when the server
// does not answer and TemplateCreateFailed when the database cannot be made;
// the function fails with TemplateDropFailed.
func NewPostgresDatabase(ctx context.Context, serverURL string) (string, func() error, error) {
	base, urlErr := url.Parse(serverURL)
	connConfig, cfgErr := pgx.ParseConfig(serverURL)
	if urlErr != nil || cfgErr != nil || (base.Scheme != "postgres" && base.Scheme != "postgresql") {
		// The messages of url and pgx can quote the URL: keep them out.
		return "", nil, errors.New(tagTemplateURLInvalid, "the URL is not a PostgreSQL URL",
			"databaseURL", config.RedactURL(serverURL))
	}
	admin := stdlib.OpenDB(*connConfig)
	admin.SetMaxOpenConns(pgAdminConns)
	t := &PostgresTemplate{admin: admin, base: base}
	if err := admin.PingContext(ctx); err != nil {
		_ = admin.Close()
		return "", nil, errors.NewErr(tagTemplateServerFailed, err).SetMetadata("databaseURL", config.RedactURL(serverURL))
	}
	name := newDatabaseName(pgCopyPrefix)
	if err := t.createDatabase(ctx, name, ""); err != nil {
		_ = admin.Close()
		return "", nil, err
	}
	drop := func() error {
		dropCtx, cancel := context.WithTimeout(context.Background(), pgDropTimeout)
		defer cancel()
		dropErr := t.dropDatabase(dropCtx, name)
		if closeErr := admin.Close(); closeErr != nil && dropErr == nil {
			return errors.NewErr(tagTemplateDropFailed, closeErr).SetMetadata("database", name)
		}
		return dropErr
	}
	return t.databaseURL(name), drop, nil
}

// databaseURL returns the URL of the database name on the server of the
// template, with the password the caller gave. It is never logged.
func (t *PostgresTemplate) databaseURL(name string) string {
	u := *t.base
	u.Path = "/" + name
	u.RawPath = ""
	return u.String()
}

// createDatabase makes the database name, as a copy of the database template
// or, when template is empty, of the server's default. It retries while
// PostgreSQL says that another session still uses the source.
func (t *PostgresTemplate) createDatabase(ctx context.Context, name, template string) error {
	stmt := "CREATE DATABASE " + pgx.Identifier{name}.Sanitize()
	if template != "" {
		stmt += " TEMPLATE " + pgx.Identifier{template}.Sanitize()
	}
	var err error
	for attempt := 0; ; attempt++ {
		_, err = t.admin.ExecContext(ctx, stmt)
		var pgErr *pgconn.PgError
		inUse := errors.As(err, &pgErr) && pgErr.Code == sqlStateObjectInUse
		if err == nil || !inUse || attempt >= pgCreateRetries || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(pgCreateWait):
		}
	}
	if err != nil {
		return errors.NewErr(tagTemplateCreateFailed, err).SetMetadata("database", name, "template", template)
	}
	return nil
}

// dropDatabase drops the database name, if it exists, ending the sessions that
// still use it.
func (t *PostgresTemplate) dropDatabase(ctx context.Context, name string) error {
	_, err := t.admin.ExecContext(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
	if err != nil {
		return errors.NewErr(tagTemplateDropFailed, err).SetMetadata("database", name)
	}
	return nil
}

// sweep drops the databases of the two prefixes whose name says they are older
// than pgLeftoverAge: what a crashed or killed run left behind.
func (t *PostgresTemplate) sweep(ctx context.Context) error {
	rows, err := t.admin.QueryContext(ctx,
		`SELECT datname FROM pg_database WHERE datname LIKE 'clowder\_tmpl\_%' OR datname LIKE 'clowder\_t\_%'`)
	if err != nil {
		return errors.NewErr(tagTemplateSweepFailed, err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return errors.NewErr(tagTemplateSweepFailed, err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return errors.NewErr(tagTemplateSweepFailed, err)
	}
	if err := rows.Close(); err != nil {
		return errors.NewErr(tagTemplateSweepFailed, err)
	}
	now := clock.Real().Now()
	for _, name := range names {
		if now.Sub(databaseCreatedAt(name)) < pgLeftoverAge {
			continue
		}
		if err := t.dropDatabase(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

// newDatabaseName returns prefix, the Unix time in seconds and 8 random
// hexadecimal digits: a name of letters, digits and underscores only.
func newDatabaseName(prefix string) string {
	var b [4]byte
	// crypto/rand.Read never fails on the supported platforms (Go 1.24+).
	_, _ = rand.Read(b[:])
	return prefix + strconv.FormatInt(clock.Real().Now().Unix(), 10) + "_" + hex.EncodeToString(b[:])
}

// databaseCreatedAt reads the creation time that newDatabaseName put in name.
// A name that does not have one gives the current time, so it is never swept.
func databaseCreatedAt(name string) time.Time {
	rest := strings.TrimPrefix(strings.TrimPrefix(name, pgTemplatePrefix), pgCopyPrefix)
	secs, _, _ := strings.Cut(rest, "_")
	n, err := strconv.ParseInt(secs, 10, 64)
	if err != nil {
		return clock.Real().Now()
	}
	return time.Unix(n, 0)
}

const (
	tagTemplateURLInvalid   = "TemplateURLInvalid"
	tagTemplateServerFailed = "TemplateServerFailed"
	tagTemplateSweepFailed  = "TemplateSweepFailed"
	tagTemplateCreateFailed = "TemplateCreateFailed"
	tagTemplateDropFailed   = "TemplateDropFailed"
)
