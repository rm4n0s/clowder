package store

// dialectMigration is a raw migration in its two forms: the SQL of SQLite and
// the SQL of PostgreSQL. Both do the same work, so the list of IDs is the
// same for both dialects (SPEC D38).
type dialectMigration struct {
	// ID names the migration; see migration.ID.
	ID string
	// SQLite is the SQL for SQLite. Never change it once it has shipped.
	SQLite string
	// Postgres is the SQL for PostgreSQL. Never change it once it has shipped.
	Postgres string
}

// migrationList is the list of raw SQL migrations, in order. Never change or
// reorder one that has shipped: add a new one at the end, with both texts.
func migrationList() []dialectMigration {
	return []dialectMigration{
		{ID: "0001_posts_fts", SQLite: migrationPostsFTS, Postgres: migrationPostsSearchPostgres},
		{ID: "0002_posts_feed_roots", SQLite: migrationPostsFeedRootsSQLite, Postgres: migrationPostsFeedRootsPostgres},
		{ID: "0003_audit_entity_user", SQLite: migrationAuditEntityUserSQLite, Postgres: migrationAuditEntityUserPostgres},
		{ID: "0004_notification_kinds", SQLite: migrationNotificationKindsSQLite, Postgres: migrationNotificationKindsPostgres},
		{ID: "0005_room_reads", SQLite: migrationRoomReadsSQLite, Postgres: migrationRoomReadsPostgres},
		{ID: "0006_messages_search", SQLite: migrationMessagesSearchSQLite, Postgres: migrationMessagesSearchPostgres},
		{ID: "0007_calls_open", SQLite: migrationCallsOpenSQLite, Postgres: migrationCallsOpenPostgres},
	}
}

// rawMigrations is the list of raw SQL migrations that Open applies for the
// dialect d, in order.
func rawMigrations(d dialect) []migration {
	list := migrationList()
	out := make([]migration, len(list))
	for i, m := range list {
		out[i] = migration{ID: m.ID, SQL: m.SQLite}
		if d == dialectPostgres {
			out[i].SQL = m.Postgres
		}
	}
	return out
}

// migrationPostsFTS creates the full-text index of posts and comments (SPEC
// §7.8) and fills it from the posts that exist. The index text of a post is
// its body, the names of its attachments and the titles of its tasks, one per
// line, without the two private-use characters that mark the matches in a
// snippet (see ftsText in search.go, which does the same for later changes;
// this copy stays as it shipped).
const migrationPostsFTS = `
CREATE VIRTUAL TABLE posts_fts USING fts5(text, tokenize = 'unicode61 remove_diacritics 2');
INSERT INTO posts_fts (rowid, text)
SELECT p.id,
  trim(replace(replace(
    p.body
    || coalesce((SELECT char(10) || group_concat(file_name, char(10))
                 FROM (SELECT file_name FROM attachments WHERE post_id = p.id ORDER BY id)), '')
    || coalesce((SELECT char(10) || group_concat(title, char(10))
                 FROM (SELECT title FROM tasks WHERE post_id = p.id ORDER BY id)), ''),
    char(57344), ''), char(57345), ''), char(10))
FROM posts p
WHERE p.deleted_at IS NULL;
`

// migrationPostsSearchPostgres is migrationPostsFTS for PostgreSQL (SPEC D38):
// the table posts_search holds the index text of each post and its tsvector,
// with a GIN index on the vector.
//
// The search configuration clowder_search is the "simple" configuration (no
// stemming, no stop words, like the unicode61 tokenizer of FTS5) with the
// unaccent dictionary in front of it for the word tokens, which is what
// remove_diacritics does in FTS5. unaccent() as a function is not IMMUTABLE,
// so it cannot appear in a generated column or an index; as a dictionary of a
// configuration it can, and to_tsvector('clowder_search', ...) is IMMUTABLE.
// That is why tsv is a generated column: it can never disagree with text, and
// no statement has to compute it. ts_headline with the same configuration
// ignores accents when it marks the matches in the original text.
//
// The default parser of PostgreSQL keeps "report.pdf", an e-mail address or a
// URL as one token, where FTS5 splits at every punctuation character, so a
// search for "report" would not find "report.pdf". The regexp_replace turns
// every ASCII character that is not a letter or a digit into a space before
// the text is parsed (an explicit list of ranges, because the classes
// [[:alnum:]] depend on the locale of the database, and a C locale would
// split the Greek or Cyrillic words).
//
// The index text is filled like migration 0001 of SQLite fills posts_fts (see
// ftsText in search.go for the later changes; this copy stays as it shipped).
const migrationPostsSearchPostgres = `
CREATE EXTENSION IF NOT EXISTS unaccent;
CREATE TEXT SEARCH CONFIGURATION clowder_search (COPY = simple);
ALTER TEXT SEARCH CONFIGURATION clowder_search
  ALTER MAPPING FOR asciiword, word, numword, asciihword, hword, numhword,
                    hword_asciipart, hword_part, hword_numpart
  WITH unaccent, simple;
CREATE TABLE posts_search (
  post_id bigint PRIMARY KEY,
  text text NOT NULL,
  tsv tsvector GENERATED ALWAYS AS (
    to_tsvector('clowder_search', regexp_replace(text, '[\x01-\x2F\x3A-\x40\x5B-\x60\x7B-\x7E]', ' ', 'g'))
  ) STORED
);
CREATE INDEX posts_search_tsv_idx ON posts_search USING GIN (tsv);
INSERT INTO posts_search (post_id, text)
SELECT p.id,
  btrim(replace(replace(
    p.body
    || coalesce((SELECT chr(10) || string_agg(file_name, chr(10) ORDER BY id) FROM attachments WHERE post_id = p.id), '')
    || coalesce((SELECT chr(10) || string_agg(title, chr(10) ORDER BY id) FROM tasks WHERE post_id = p.id), ''),
    chr(57344), ''), chr(57345), ''), chr(10))
FROM posts p
WHERE p.deleted_at IS NULL;
`

// migrationPostsFeedRootsPostgres adds the index that serves the feed of a
// room on PostgreSQL. The feed reads the posts with parent_id IS NULL, newest
// first. The index idx_posts_feed (room_id, parent_id, id DESC) finds them, but
// the planner of PostgreSQL treats IS NULL as a filter and not as an equality,
// so it cannot tell that the rows come out in id order and sorts all the root
// posts of the room before it takes the page. A partial index on the root posts
// delivers them in order, and the page costs the same as a page of a short feed
// (found in step 14.2, by the test of the plan of the feed query).
const migrationPostsFeedRootsPostgres = `
CREATE INDEX idx_posts_feed_roots ON posts (room_id, id DESC) WHERE parent_id IS NULL;
`

// migrationPostsFeedRootsSQLite is the migration 0002 of SQLite: nothing to do.
// SQLite treats IS NULL as an equality and reads the feed from idx_posts_feed
// in order, without a sort.
const migrationPostsFeedRootsSQLite = `
SELECT 1;
`

// migrationAuditEntityUserPostgres lets the audit log hold events about
// accounts (SPEC §7.10, 1.2): the entity type "user" joins the others in the
// check constraint. The constraint is dropped and added again; the rows are
// not touched. Where the table was made by this version the constraint is
// there already, and the two statements change nothing.
const migrationAuditEntityUserPostgres = `
ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS chk_audit_events_entity_type;
ALTER TABLE audit_events ADD CONSTRAINT chk_audit_events_entity_type
  CHECK (entity_type IN ('room','post','comment','task','column','member','user'));
`

// migrationAuditEntityUserSQLite is migrationAuditEntityUserPostgres for
// SQLite, which cannot change a check constraint: the table is made again
// under another name with the new constraint, every row is copied with its ID
// and its time, the old table is dropped, the new one takes its name and the
// indexes are made again. It all runs in the transaction of the migration, so
// a failure leaves the old table. The text of the new table is what GORM makes
// for the model, so that AutoMigrate finds nothing to change at the next
// start. No other table has a foreign key to audit_events. A table made by
// this version is rebuilt too, which costs nothing while it is empty. This is
// the one place where audit events are copied; the log stays append-only for
// everything else.
const migrationAuditEntityUserSQLite = "" +
	"CREATE TABLE audit_events_new (" +
	"`id` integer PRIMARY KEY AUTOINCREMENT," +
	"`created_at` datetime," +
	"`actor_id` integer NOT NULL," +
	"`action` text NOT NULL," +
	"`room_id` integer NOT NULL," +
	"`entity_type` text NOT NULL," +
	"`entity_id` integer NOT NULL," +
	"`summary` text NOT NULL," +
	"`details` text NOT NULL," +
	"CONSTRAINT `fk_audit_events_actor` FOREIGN KEY (`actor_id`) REFERENCES `users`(`id`) ON DELETE RESTRICT," +
	"CONSTRAINT `chk_audit_events_entity_type` CHECK (entity_type IN ('room','post','comment','task','column','member','user'))," +
	"CONSTRAINT `chk_audit_events_details_json` CHECK (json_valid(details)));\n" +
	"INSERT INTO audit_events_new (id, created_at, actor_id, action, room_id, entity_type, entity_id, summary, details) " +
	"SELECT id, created_at, actor_id, action, room_id, entity_type, entity_id, summary, details FROM audit_events ORDER BY id;\n" +
	"DROP TABLE audit_events;\n" +
	"ALTER TABLE audit_events_new RENAME TO audit_events;\n" +
	"CREATE INDEX `idx_audit_events_created_at` ON `audit_events`(`created_at`);\n" +
	"CREATE INDEX `idx_audit_events_action` ON `audit_events`(`action`,`id`);\n" +
	"CREATE INDEX `idx_audit_events_actor` ON `audit_events`(`actor_id`,`id`);\n" +
	"CREATE INDEX `idx_audit_events_room` ON `audit_events`(`room_id`,`id`);\n"

// migrationNotificationKindsPostgres lets notifications be of the kinds
// group_mention and keyword (SPEC §7.9, §7.15, 1.2): the check constraint on
// the kind is dropped and added again with the two kinds; the rows are not
// touched. Where the table was made by this version the constraint is there
// already, and the two statements change nothing.
const migrationNotificationKindsPostgres = `
ALTER TABLE notifications DROP CONSTRAINT IF EXISTS chk_notifications_kind;
ALTER TABLE notifications ADD CONSTRAINT chk_notifications_kind
  CHECK (kind IN ('mention','reply','assigned','added_to_room','group_mention','keyword'));
`

// migrationNotificationKindsSQLite is migrationNotificationKindsPostgres for
// SQLite, which cannot change a check constraint: the table is made again
// under another name with the new constraint, every row is copied with its ID,
// the old table is dropped, the new one takes its name and the indexes are made
// again, all in the transaction of the migration. The text of the new table is
// what GORM makes for the model, so that AutoMigrate finds nothing to change at
// the next start; the column others was added by AutoMigrate before this
// migration runs. No other table has a foreign key to notifications.
const migrationNotificationKindsSQLite = "" +
	"CREATE TABLE notifications_new (" +
	"`id` integer PRIMARY KEY AUTOINCREMENT," +
	"`created_at` datetime," +
	"`user_id` integer NOT NULL," +
	"`kind` text NOT NULL," +
	"`post_id` integer," +
	"`task_id` integer," +
	"`room_id` integer," +
	"`actor_id` integer NOT NULL," +
	"`read_at` datetime," +
	"`others` integer NOT NULL DEFAULT 0," +
	"CONSTRAINT `fk_notifications_user` FOREIGN KEY (`user_id`) REFERENCES `users`(`id`) ON DELETE RESTRICT," +
	"CONSTRAINT `fk_notifications_post` FOREIGN KEY (`post_id`) REFERENCES `posts`(`id`) ON DELETE CASCADE," +
	"CONSTRAINT `fk_notifications_task` FOREIGN KEY (`task_id`) REFERENCES `tasks`(`id`) ON DELETE CASCADE," +
	"CONSTRAINT `fk_notifications_room` FOREIGN KEY (`room_id`) REFERENCES `rooms`(`id`) ON DELETE CASCADE," +
	"CONSTRAINT `fk_notifications_actor` FOREIGN KEY (`actor_id`) REFERENCES `users`(`id`) ON DELETE RESTRICT," +
	"CONSTRAINT `chk_notifications_kind` CHECK (kind IN ('mention','reply','assigned','added_to_room','group_mention','keyword')));\n" +
	"INSERT INTO notifications_new (id, created_at, user_id, kind, post_id, task_id, room_id, actor_id, read_at, others) " +
	"SELECT id, created_at, user_id, kind, post_id, task_id, room_id, actor_id, read_at, others FROM notifications ORDER BY id;\n" +
	"DROP TABLE notifications;\n" +
	"ALTER TABLE notifications_new RENAME TO notifications;\n" +
	"CREATE INDEX `idx_notifications_room_id` ON `notifications`(`room_id`);\n" +
	"CREATE INDEX `idx_notifications_task_id` ON `notifications`(`task_id`);\n" +
	"CREATE INDEX `idx_notifications_post_id` ON `notifications`(`post_id`);\n" +
	"CREATE INDEX `idx_notifications_user_read` ON `notifications`(`user_id`,`read_at`,`id`);\n"

// migrationRoomReadsSQLite starts every existing member at the newest post or
// comment of the room (SPEC §7.17, D55): nothing is unread when 1.2 starts.
// The table room_reads is made by AutoMigrate before this runs. The rows
// already there (a database that was migrated and then restored) are left
// alone.
const migrationRoomReadsSQLite = `
INSERT INTO room_reads (user_id, room_id, last_seen_id, updated_at)
SELECT m.user_id, m.room_id, COALESCE((SELECT MAX(p.id) FROM posts p WHERE p.room_id = m.room_id), 0), CURRENT_TIMESTAMP
FROM room_members m
WHERE NOT EXISTS (SELECT 1 FROM room_reads r WHERE r.user_id = m.user_id AND r.room_id = m.room_id);
`

// migrationRoomReadsPostgres is migrationRoomReadsSQLite for PostgreSQL, with
// the time as now().
const migrationRoomReadsPostgres = `
INSERT INTO room_reads (user_id, room_id, last_seen_id, updated_at)
SELECT m.user_id, m.room_id, COALESCE((SELECT MAX(p.id) FROM posts p WHERE p.room_id = m.room_id), 0), now()
FROM room_members m
WHERE NOT EXISTS (SELECT 1 FROM room_reads r WHERE r.user_id = m.user_id AND r.room_id = m.room_id);
`

// migrationMessagesSearchSQLite creates the full-text index of private
// messages (SPEC §7.11, D56) and fills it from the messages that exist. The
// index text of a message is its body, without the two private-use characters
// that mark the matches in a snippet; a deleted message, and one with no text
// (only files), has no row (see messageIndexText in messages_search.go, which
// does the same for later changes; this copy stays as it shipped). The
// tokenizer is that of posts_fts.
const migrationMessagesSearchSQLite = `
CREATE VIRTUAL TABLE messages_fts USING fts5(text, tokenize = 'unicode61 remove_diacritics 2');
INSERT INTO messages_fts (rowid, text)
SELECT m.id, replace(replace(m.body, char(57344), ''), char(57345), '')
FROM messages m
WHERE m.deleted_at IS NULL AND m.body <> '';
`

// migrationMessagesSearchPostgres is migrationMessagesSearchSQLite for
// PostgreSQL (SPEC D38, D56): the table messages_search holds the index text
// of each message and its tsvector, with a GIN index on the vector, like
// posts_search and with the same search configuration (migration 0001).
const migrationMessagesSearchPostgres = `
CREATE TABLE messages_search (
  message_id bigint PRIMARY KEY,
  text text NOT NULL,
  tsv tsvector GENERATED ALWAYS AS (
    to_tsvector('clowder_search', regexp_replace(text, '[\x01-\x2F\x3A-\x40\x5B-\x60\x7B-\x7E]', ' ', 'g'))
  ) STORED
);
CREATE INDEX messages_search_tsv_idx ON messages_search USING GIN (tsv);
INSERT INTO messages_search (message_id, text)
SELECT m.id, replace(replace(m.body, chr(57344), ''), chr(57345), '')
FROM messages m
WHERE m.deleted_at IS NULL AND m.body <> '';
`

// migrationCallsOpenSQLite makes the database refuse a second open call for
// a user (SPEC §7.18, D63): a unique index on the caller and one on the callee
// of the calls that are ringing or active (a partial index), and an index that
// finds the calls that are ringing, for the timeout. They are the last guard
// behind Calls.Start, which checks both users under a lock; the one case they
// cannot see is a user who is the caller of one call and the callee of
// another, which only that lock stops. The tables calls and the columns kind
// and call_id of messages are made by AutoMigrate before this runs; the
// messages that exist keep the default kind text.
const migrationCallsOpenSQLite = `
CREATE UNIQUE INDEX IF NOT EXISTS uq_calls_open_caller ON calls (caller_id) WHERE state IN ('ringing', 'active');
CREATE UNIQUE INDEX IF NOT EXISTS uq_calls_open_callee ON calls (callee_id) WHERE state IN ('ringing', 'active');
CREATE INDEX IF NOT EXISTS idx_calls_ringing ON calls (created_at) WHERE state = 'ringing';
CREATE INDEX IF NOT EXISTS idx_calls_open_home ON calls (home) WHERE state IN ('ringing', 'active');
`

// migrationCallsOpenPostgres is migrationCallsOpenSQLite for PostgreSQL. The
// predicate of the partial indexes is state <> 'ended', which is the text of
// the queries of the store (OpenCallsOfUsers, CountOpenCallsOfHome) that the
// planner can then match to the index.
const migrationCallsOpenPostgres = `
CREATE UNIQUE INDEX IF NOT EXISTS uq_calls_open_caller ON calls (caller_id) WHERE state <> 'ended';
CREATE UNIQUE INDEX IF NOT EXISTS uq_calls_open_callee ON calls (callee_id) WHERE state <> 'ended';
CREATE INDEX IF NOT EXISTS idx_calls_ringing ON calls (created_at) WHERE state = 'ringing';
CREATE INDEX IF NOT EXISTS idx_calls_open_home ON calls (home) WHERE state <> 'ended';
`
