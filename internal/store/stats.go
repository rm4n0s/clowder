package store

import (
	"context"

	"github.com/rm4n0s/errors"
)

// Stats are the sizes of the tables and the results of consistency checks, for
// the report of the large demo (SPEC §14) and for tests. A field of the second
// group is the number of rows that break the rule it names: 0 is healthy.
type Stats struct {
	// Users is the number of users.
	Users int64
	// Rooms is the number of rooms; PrivateRooms counts the private ones.
	Rooms, PrivateRooms int64
	// Members is the number of room memberships.
	Members int64
	// Posts is the number of top-level posts, Comments the number of comments.
	// Deleted ones that are kept for their replies are counted too.
	Posts, Comments int64
	// Tags is the number of tags, PostTags the number of links to posts.
	Tags, PostTags int64
	// Reactions, Mentions and Attachments are the numbers of those rows.
	Reactions, Mentions, Attachments int64
	// Columns is the number of board columns, Tasks the number of tasks and
	// Assignees the number of task assignments.
	Columns, Tasks, Assignees int64
	// Conversations and Messages are the numbers of private conversations and
	// messages.
	Conversations, Messages int64
	// AuditEvents is the number of audit events; PostEvents and TaskEvents
	// count those with the actions post.created and task.created.
	AuditEvents, PostEvents, TaskEvents int64
	// Indexed is the number of rows of the full-text index.
	Indexed int64

	// Unindexed counts posts and comments that are not deleted and have no row
	// in the full-text index.
	Unindexed int64
	// StrayIndexRows counts rows of the full-text index whose post is missing
	// or deleted.
	StrayIndexRows int64
	// BadComments counts comments whose root is not a top-level post of their
	// room, or whose parent is not in the same thread.
	BadComments int64
	// NonMemberAuthors counts posts and comments whose author is not a member
	// of their room.
	NonMemberAuthors int64
	// BadColumns counts rooms whose board columns are missing or whose
	// positions are not 0, 1, 2, ... without gaps.
	BadColumns int64
	// TasksInForeignColumn counts tasks whose column or post belongs to
	// another room than the task.
	TasksInForeignColumn int64
	// BadTaskPositions counts columns whose tasks do not have the positions
	// 0, 1, 2, ... without gaps.
	BadTaskPositions int64
}

// statQueries lists, for each field of Stats, the query that computes it.
func statQueries(st *Stats, d dialect) []struct {
	dest *int64
	sql  string
} {
	table, idColumn := searchIndex(d)
	return []struct {
		dest *int64
		sql  string
	}{
		{&st.Users, "SELECT COUNT(*) FROM users"},
		{&st.Rooms, "SELECT COUNT(*) FROM rooms"},
		{&st.PrivateRooms, "SELECT COUNT(*) FROM rooms WHERE visibility = 'private'"},
		{&st.Members, "SELECT COUNT(*) FROM room_members"},
		{&st.Posts, "SELECT COUNT(*) FROM posts WHERE parent_id IS NULL"},
		{&st.Comments, "SELECT COUNT(*) FROM posts WHERE parent_id IS NOT NULL"},
		{&st.Tags, "SELECT COUNT(*) FROM tags"},
		{&st.PostTags, "SELECT COUNT(*) FROM post_tags"},
		{&st.Reactions, "SELECT COUNT(*) FROM reactions"},
		{&st.Mentions, "SELECT COUNT(*) FROM mentions"},
		{&st.Attachments, "SELECT COUNT(*) FROM attachments"},
		{&st.Columns, "SELECT COUNT(*) FROM board_columns"},
		{&st.Tasks, "SELECT COUNT(*) FROM tasks"},
		{&st.Assignees, "SELECT COUNT(*) FROM task_assignees"},
		{&st.Conversations, "SELECT COUNT(*) FROM conversations"},
		{&st.Messages, "SELECT COUNT(*) FROM messages"},
		{&st.AuditEvents, "SELECT COUNT(*) FROM audit_events"},
		{&st.PostEvents, "SELECT COUNT(*) FROM audit_events WHERE action = 'post.created'"},
		{&st.TaskEvents, "SELECT COUNT(*) FROM audit_events WHERE action = 'task.created'"},
		{&st.Indexed, "SELECT COUNT(*) FROM " + table},
		{&st.Unindexed, `SELECT COUNT(*) FROM posts p WHERE p.deleted_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM ` + table + ` f WHERE f.` + idColumn + ` = p.id)`},
		{&st.StrayIndexRows, `SELECT COUNT(*) FROM ` + table + ` f WHERE NOT EXISTS
  (SELECT 1 FROM posts p WHERE p.id = f.` + idColumn + ` AND p.deleted_at IS NULL)`},
		{&st.BadComments, `SELECT COUNT(*) FROM posts c
  JOIN posts r ON r.id = c.root_id JOIN posts a ON a.id = c.parent_id
  WHERE c.parent_id IS NOT NULL
    AND (r.parent_id IS NOT NULL OR r.room_id != c.room_id
         OR a.room_id != c.room_id OR COALESCE(a.root_id, a.id) != c.root_id)`},
		{&st.NonMemberAuthors, `SELECT COUNT(*) FROM posts p WHERE NOT EXISTS
  (SELECT 1 FROM room_members m WHERE m.room_id = p.room_id AND m.user_id = p.author_id)`},
		{&st.BadColumns, `SELECT COUNT(*) FROM rooms r WHERE NOT EXISTS
  (SELECT 1 FROM board_columns c WHERE c.room_id = r.id)
  OR (SELECT MIN(position) != 0 OR MAX(position) != COUNT(*) - 1 OR COUNT(DISTINCT position) != COUNT(*)
      FROM board_columns c WHERE c.room_id = r.id)`},
		{&st.TasksInForeignColumn, `SELECT COUNT(*) FROM tasks t
  JOIN board_columns c ON c.id = t.column_id JOIN posts p ON p.id = t.post_id
  WHERE c.room_id != t.room_id OR p.room_id != t.room_id`},
		{&st.BadTaskPositions, `SELECT COUNT(*) FROM (SELECT column_id FROM tasks GROUP BY column_id
  HAVING MIN(position) != 0 OR MAX(position) != COUNT(*) - 1 OR COUNT(DISTINCT position) != COUNT(*)) AS bad`},
	}
}

// Stats counts the rows of the main tables and runs the consistency checks
// of the Stats fields. On a large database it takes a second or two.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	for _, q := range statQueries(&st, s.dialect) {
		if err := s.db(ctx).Raw(q.sql).Scan(q.dest).Error; err != nil {
			return Stats{}, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "stats", "sql", q.sql)
		}
	}
	return st, nil
}

// RoomPostCount is the number of posts and comments of a room.
type RoomPostCount struct {
	// RoomID is the room.
	RoomID uint64
	// Posts is the number of its posts and comments together.
	Posts int64
}

// RoomPostCounts returns, for each room that has posts or comments, how many
// it has, the busiest room first (a tie goes to the lower room ID). The
// benchmarks use it to pick the room to measure.
func (s *Store) RoomPostCounts(ctx context.Context) ([]RoomPostCount, error) {
	var counts []RoomPostCount
	err := s.db(ctx).Raw(`SELECT room_id AS room_id, COUNT(*) AS posts FROM posts
GROUP BY room_id ORDER BY posts DESC, room_id`).Scan(&counts).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "roomPostCounts")
	}
	return counts, nil
}
