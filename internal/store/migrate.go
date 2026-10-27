package store

import (
	"context"
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

// migrate brings the schema up to date: first GORM's AutoMigrate for the
// models, then the raw migrations that have not run yet. Running it again on
// an up-to-date database changes nothing.
func (s *Store) migrate(ctx context.Context, models []any, migrations []migration) error {
	seen := make(map[string]bool, len(migrations))
	for _, m := range migrations {
		if m.ID == "" || m.SQL == "" || seen[m.ID] {
			return errors.New(tagMigrationInvalid, "a migration has no id or no SQL, or its id is used twice",
				"migration", m.ID)
		}
		seen[m.ID] = true
	}
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
	tagMigrationInvalid = "MigrationInvalid"
	tagMigrationFailed  = "MigrationFailed"
)
