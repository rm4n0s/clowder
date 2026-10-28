package store

import (
	"context"
	"strings"

	"github.com/rm4n0s/errors"
)

// LatestMigration returns the ID of the last raw migration that this binary
// knows, such as "0007_calls_open". The IDs start with a zero-padded number,
// so a larger ID is a newer schema. A backup records the ID that the database
// had (SchemaVersion), and a restore refuses a backup whose ID is larger than
// this one (SPEC D59).
func LatestMigration() string {
	list := migrationList()
	return list[len(list)-1].ID
}

// SchemaVersion returns the ID of the last raw migration applied to the
// database, or "" when none has run. Open runs them all, so on a Store it is
// LatestMigration unless a newer binary migrated the database first.
func (s *Store) SchemaVersion(ctx context.Context) (string, error) {
	var version *string
	if err := s.db(ctx).Raw("SELECT MAX(id) FROM schema_migrations").Scan(&version).Error; err != nil {
		return "", errors.NewErr(TagQueryFailed, err).SetMetadata("op", "schemaVersion")
	}
	if version == nil {
		return "", nil
	}
	return *version, nil
}

// BackupCounts are the sizes that the manifest of a backup records.
type BackupCounts struct {
	// Users, Rooms and Messages are the numbers of those rows.
	Users, Rooms, Messages int64
	// Posts is the number of posts and comments together (they share a table).
	Posts int64
}

// BackupCounts counts the rows that a backup reports. It reads four counts and
// nothing else, so unlike Stats it is quick on a large database.
func (s *Store) BackupCounts(ctx context.Context) (BackupCounts, error) {
	var c BackupCounts
	queries := []struct {
		dest *int64
		sql  string
	}{
		{&c.Users, "SELECT COUNT(*) FROM users"},
		{&c.Rooms, "SELECT COUNT(*) FROM rooms"},
		{&c.Posts, "SELECT COUNT(*) FROM posts"},
		{&c.Messages, "SELECT COUNT(*) FROM messages"},
	}
	for _, q := range queries {
		if err := s.db(ctx).Raw(q.sql).Scan(q.dest).Error; err != nil {
			return BackupCounts{}, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "backupCounts", "sql", q.sql)
		}
	}
	return c, nil
}

// IsPostgres reports whether the Store runs on PostgreSQL.
func (s *Store) IsPostgres() bool {
	return s.dialect == dialectPostgres
}

// VacuumInto writes a consistent copy of the SQLite database to path with
// VACUUM INTO. It reads from one snapshot and, in WAL mode, does not stop the
// writers of the server meanwhile. path must not exist or must be an empty
// file. On PostgreSQL it fails: the database is saved with pg_dump.
func (s *Store) VacuumInto(ctx context.Context, path string) error {
	if s.dialect == dialectPostgres {
		return errors.New(TagBackupVacuumFailed, "VACUUM INTO is for SQLite; save PostgreSQL with pg_dump", "path", path)
	}
	quoted := "'" + strings.ReplaceAll(path, "'", "''") + "'"
	if err := s.db(ctx).Exec("VACUUM INTO " + quoted).Error; err != nil {
		return errors.NewErr(TagBackupVacuumFailed, err).SetMetadata("path", path)
	}
	return nil
}

// TagBackupVacuumFailed is the tag of a VACUUM INTO that failed, or that was
// asked of a PostgreSQL store. The metadata "path" is the target.
const TagBackupVacuumFailed = "BackupVacuumFailed"
