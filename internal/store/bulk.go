package store

import (
	"context"
	"strings"

	"github.com/rm4n0s/errors"
	"gorm.io/gorm"
)

// bulkBatchSize is how many rows BulkCreate writes in one INSERT. It stays far
// below SQLite's limit of bound variables (32,766) for the widest model.
const bulkBatchSize = 500

// BulkRow is the set of models that BulkCreate writes and NextID counts. They
// are the tables that the large demo (SPEC §10, §14) fills in bulk.
type BulkRow interface {
	User | RoomMember | Post | Tag | PostTag | Attachment | Reaction | Mention |
		Task | TaskAssignee | AuditEvent
}

// BulkCreate inserts rows in batches of 500, in the order they are given. It
// is for the large demo and for tools that load many rows at once, where one
// service call per row would take too long. A row with a zero CreatedAt gets
// the time of the Store's clock; one with a primary key keeps it, which lets
// the caller link rows to each other before they are written. Call it inside
// Store.Tx to make the load atomic. It does no work for an empty list, and
// fails with TagConflict when a unique constraint is violated and with
// TagQueryFailed for any other database error.
func BulkCreate[T BulkRow](ctx context.Context, s *Store, rows []T) error {
	if len(rows) == 0 {
		return nil
	}
	err := s.db(ctx).CreateInBatches(rows, bulkBatchSize).Error
	if err != nil {
		switch classify(err) {
		case failureConflict:
			return errors.NewErr(TagConflict, err).SetMetadata("op", "bulkCreate", "rows", len(rows))
		default:
			return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "bulkCreate", "rows", len(rows))
		}
	}
	if s.dialect == dialectPostgres {
		// Rows written with their own primary keys leave the sequence of the
		// table behind them: the next insert without a key would take a key
		// that exists. SQLite takes the next key from the table itself.
		stmt := &gorm.Statement{DB: s.gdb}
		if parseErr := stmt.Parse(new(T)); parseErr != nil {
			return errors.NewErr(TagQueryFailed, parseErr).SetMetadata("op", "bulkCreate", "step", "schema")
		}
		if pk := stmt.Schema.PrioritizedPrimaryField; pk != nil && pk.AutoIncrement {
			err := s.db(ctx).Exec(`SELECT setval(pg_get_serial_sequence(?, ?), (SELECT COALESCE(MAX(`+quoteIdent(pk.DBName)+`), 1) FROM `+quoteIdent(stmt.Schema.Table)+`))`,
				stmt.Schema.Table, pk.DBName).Error
			if err != nil {
				return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "bulkCreate", "step", "sequence")
			}
		}
	}
	return nil
}

// quoteIdent quotes a table or column name that comes from a GORM schema (not
// from a user) for PostgreSQL.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// NextID returns the primary key that follows the largest one of the table of
// T: 1 for an empty table. A caller that writes rows with BulkCreate, giving
// each its primary key, starts from it.
func NextID[T BulkRow](ctx context.Context, s *Store) (uint64, error) {
	var last uint64
	err := s.db(ctx).Model(new(T)).Select("COALESCE(MAX(id), 0)").Scan(&last).Error
	if err != nil {
		return 0, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "nextID")
	}
	return last + 1, nil
}

// ListTags returns every tag, ordered by ID.
func (s *Store) ListTags(ctx context.Context) ([]Tag, error) {
	var tags []Tag
	if err := s.db(ctx).Order("id").Find(&tags).Error; err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "listTags")
	}
	return tags, nil
}

// RebuildSearchIndex makes the full-text index hold every post and comment
// that is not deleted, with the text ReindexPost would give it, in one
// statement: it empties the index and fills it again. The large demo calls it
// after BulkCreate wrote posts, attachments and tasks, which would otherwise
// each need a ReindexPost. Call it in a transaction.
func (s *Store) RebuildSearchIndex(ctx context.Context) error {
	table, idColumn := searchIndex(s.dialect)
	if err := s.db(ctx).Exec("DELETE FROM " + table).Error; err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "rebuildSearchIndex", "step", "delete")
	}
	err := s.db(ctx).Exec(`INSERT INTO ` + table + ` (` + idColumn + `, text)
SELECT p.id, ` + ftsText(s.dialect) + ` FROM posts p WHERE p.deleted_at IS NULL`).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "rebuildSearchIndex", "step", "insert")
	}
	return nil
}
