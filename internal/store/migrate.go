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

const (
	tagMigrationInvalid = "MigrationInvalid"
	tagMigrationFailed  = "MigrationFailed"
)
