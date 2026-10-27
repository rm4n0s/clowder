package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"time"

	"github.com/rm4n0s/errors"
)

// migration is one raw SQL migration. Migrations are applied in the order of
// the list, each once, and each in its own transaction. Never change or
// reorder one that has shipped: add a new one.
type migration struct {
	// ID names the migration and is recorded when it has run. It is unique.
	ID string
	// SQL is the statement or statements to run.
	SQL string
}

// schemaMigration is a row of schema_migrations: a raw migration that has run.
type schemaMigration struct {
	ID        string    `gorm:"primaryKey"`
	AppliedAt time.Time `gorm:"autoCreateTime"`
}

// TableName is the name of the table of schemaMigration.
func (schemaMigration) TableName() string {
	return "schema_migrations"
}

// migrationLockKey is the key of the PostgreSQL advisory lock that one process
// holds while it migrates: the bytes of "clowder".
const migrationLockKey int64 = 0x636c6f77646572

// unlockTimeout bounds the release of the migration lock, which must not
// depend on a context that was canceled.
const unlockTimeout = 10 * time.Second

// migrate brings the schema up to date: first GORM's AutoMigrate for the
// models, then the raw migrations that have not run yet. Running it again on
// an up-to-date database changes nothing. On PostgreSQL it holds an advisory
// lock meanwhile, so that several processes that start together on one
// database migrate one after the other, and the later ones find the schema
// current (SPEC §8.1).
func (s *Store) migrate(ctx context.Context, models []any, migrations []migration) error {
	seen := make(map[string]bool, len(migrations))
	for _, m := range migrations {
		if m.ID == "" || m.SQL == "" || seen[m.ID] {
			return errors.New(tagMigrationInvalid, "a migration has no id or no SQL, or its id is used twice",
				"migration", m.ID)
		}
		seen[m.ID] = true
	}
	if s.dialect != dialectPostgres {
		return s.migrateSchema(ctx, models, migrations)
	}
	conn, err := s.lockMigrations(ctx)
	if err != nil {
		return err
	}
	err = s.migrateSchema(ctx, models, migrations)
	if unlockErr := s.unlockMigrations(conn); unlockErr != nil {
		if err == nil {
			return unlockErr
		}
		s.logger.Error("releasing the migration lock failed", "err", unlockErr)
	}
	return err
}

// lockMigrations takes the advisory lock of the migrations on a connection of
// its own, waiting as long as another process holds it or until ctx ends. The
// lock belongs to the session, so the connection is kept (and returned to
// unlockMigrations) while the migration runs on the other connections of the
// pool.
func (s *Store) lockMigrations(ctx context.Context) (*sql.Conn, error) {
	sqlDB, err := s.gdb.DB()
	if err != nil {
		return nil, errors.NewErr(tagMigrationLockFailed, err).SetMetadata("step", "pool")
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return nil, errors.NewErr(tagMigrationLockFailed, err).SetMetadata("step", "connection")
	}
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		// A canceled lock request may still be granted: close the session
		// instead of returning it to the pool with the lock.
		discardConn(conn)
		return nil, errors.NewErr(tagMigrationLockFailed, err).SetMetadata("step", "lock")
	}
	return conn, nil
}

// unlockMigrations releases the lock that lockMigrations took and returns the
// connection to the pool. When the release fails the connection is closed
// instead, which ends the session and with it the lock.
func (s *Store) unlockMigrations(conn *sql.Conn) error {
	ctx, cancel := context.WithTimeout(context.Background(), unlockTimeout)
	defer cancel()
	var released bool
	err := conn.QueryRowContext(ctx, "SELECT pg_advisory_unlock($1)", migrationLockKey).Scan(&released)
	if err != nil || !released {
		discardConn(conn)
		if err != nil {
			return errors.NewErr(tagMigrationUnlockFailed, err)
		}
		return errors.New(tagMigrationUnlockFailed, "the migration lock was not held by the session")
	}
	if err := conn.Close(); err != nil {
		return errors.NewErr(tagMigrationUnlockFailed, err).SetMetadata("step", "close")
	}
	return nil
}

// discardConn closes the underlying connection of conn instead of returning it
// to the pool, and gives the pool's slot back.
func discardConn(conn *sql.Conn) {
	// Returning ErrBadConn from Raw makes database/sql drop the connection.
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
}

// migrateSchema is the work of migrate, without the lock.
func (s *Store) migrateSchema(ctx context.Context, models []any, migrations []migration) error {
	if s.dialect == dialectPostgres {
		if err := s.db(ctx).Exec(postgresPrelude).Error; err != nil {
			return errors.NewErr(tagMigrationFailed, err).SetMetadata("migration", "prelude")
		}
	}
	all := append([]any{&schemaMigration{}}, models...)
	if err := s.db(ctx).AutoMigrate(all...); err != nil {
		return errors.NewErr(tagMigrationFailed, err).SetMetadata("migration", "automigrate")
	}
	for _, m := range migrations {
		err := s.Tx(ctx, func(tx *Store) error {
			return tx.applyMigration(ctx, m)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// applyMigration runs m unless it was recorded before, and records it. The
// caller runs it in a transaction.
func (s *Store) applyMigration(ctx context.Context, m migration) error {
	var done int64
	err := s.db(ctx).Model(&schemaMigration{}).Where("id = ?", m.ID).Count(&done).Error
	if err != nil {
		return errors.NewErr(tagMigrationFailed, err).SetMetadata("migration", m.ID, "step", "check")
	}
	if done > 0 {
		return nil
	}
	if err := s.db(ctx).Exec(m.SQL).Error; err != nil {
		return errors.NewErr(tagMigrationFailed, err).SetMetadata("migration", m.ID, "step", "run")
	}
	if err := s.db(ctx).Create(&schemaMigration{ID: m.ID}).Error; err != nil {
		return errors.NewErr(tagMigrationFailed, err).SetMetadata("migration", m.ID, "step", "record")
	}
	return nil
}

// postgresPrelude makes PostgreSQL accept the parts of the models that are
// written for SQLite, before AutoMigrate runs. It is idempotent and runs at
// every start:
//
//   - the collation nocase, for the columns declared "text COLLATE NOCASE" (the
//     room names are unique without regard to case) and for ORDER BY ... COLLATE
//     NOCASE: an ICU collation compared at the second level, so that case is
//     ignored and equal-looking names are equal in comparisons and unique
//     indexes (the collation is not deterministic);
//   - the function json_valid(text), which the check on the details of an audit
//     event calls. SQLite has it built in; the function returns NULL for NULL,
//     like SQLite. pg_input_is_valid (PostgreSQL 16 and later) checks the text
//     without the cost of catching an exception.
const postgresPrelude = `
CREATE COLLATION IF NOT EXISTS nocase (provider = icu, locale = 'und-u-ks-level2', deterministic = false);
CREATE OR REPLACE FUNCTION json_valid(text) RETURNS boolean
LANGUAGE sql IMMUTABLE STRICT AS $$ SELECT pg_input_is_valid($1, 'jsonb') $$;
`

const (
	tagMigrationInvalid      = "MigrationInvalid"
	tagMigrationFailed       = "MigrationFailed"
	tagMigrationLockFailed   = "MigrationLockFailed"
	tagMigrationUnlockFailed = "MigrationUnlockFailed"
)
