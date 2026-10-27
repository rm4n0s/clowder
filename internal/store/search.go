package store

import (
	"context"
	"strings"
	"time"

	"github.com/rm4n0s/errors"
)

const (
	// SearchMarkStart comes before each matched word in SearchHit.Snippet. It
	// is a private-use character, and the index never holds it, so user text
	// cannot fake a match.
	SearchMarkStart = "\ue000"
	// SearchMarkEnd comes after each matched word in SearchHit.Snippet; see
	// SearchMarkStart.
	SearchMarkEnd = "\ue001"

	// snippetTokens is how many words around the match a snippet holds.
	snippetTokens = 24
	// plainSnippetRunes is the length of the snippet of a search without words.
	plainSnippetRunes = 200
)

// ftsTextSQLite is the SQL expression of the index text of the post p in
// SQLite: its body, the names of its attachments and the titles of its tasks,
// one per line, without the characters that mark a match. Migration 0001
// holds the copy that shipped.
const ftsTextSQLite = `trim(replace(replace(
  p.body
  || coalesce((SELECT char(10) || group_concat(file_name, char(10))
               FROM (SELECT file_name FROM attachments WHERE post_id = p.id ORDER BY id)), '')
  || coalesce((SELECT char(10) || group_concat(title, char(10))
               FROM (SELECT title FROM tasks WHERE post_id = p.id ORDER BY id)), ''),
  char(57344), ''), char(57345), ''), char(10))`

// ftsTextPostgres is ftsTextSQLite for PostgreSQL: the same text.
const ftsTextPostgres = `btrim(replace(replace(
  p.body
  || coalesce((SELECT chr(10) || string_agg(file_name, chr(10) ORDER BY id) FROM attachments WHERE post_id = p.id), '')
  || coalesce((SELECT chr(10) || string_agg(title, chr(10) ORDER BY id) FROM tasks WHERE post_id = p.id), ''),
  chr(57344), ''), chr(57345), ''), chr(10))`

// ftsText is the SQL expression of the index text of the post p: its body,
// the names of its attachments and the titles of its tasks, one per line,
// without the characters that mark a match. The text is the same on both
// databases.
func ftsText(d dialect) string {
	if d == dialectPostgres {
		return ftsTextPostgres
	}
	return ftsTextSQLite
}

// searchIndex names the full-text index table of a dialect and its column of
// post IDs: posts_fts(rowid) in SQLite, posts_search(post_id) in PostgreSQL.
func searchIndex(d dialect) (table, idColumn string) {
	if d == dialectPostgres {
		return "posts_search", "post_id"
	}
	return "posts_fts", "rowid"
}

// ReindexPost makes the full-text index hold the current text of the post
// id: it deletes the row of the post and, when the post exists and is not
// deleted, inserts its body, attachment names and task titles. A missing or
// deleted post is simply removed from the index. Call it in the transaction
// of every change that touches those texts.
func (s *Store) ReindexPost(ctx context.Context, id uint64) error {
	table, idColumn := searchIndex(s.dialect)
	if err := s.db(ctx).Exec("DELETE FROM "+table+" WHERE "+idColumn+" = ?", id).Error; err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "reindexPost", "postID", id)
	}
	err := s.db(ctx).Exec(`INSERT INTO `+table+` (`+idColumn+`, text)
SELECT p.id, `+ftsText(s.dialect)+` FROM posts p WHERE p.id = ? AND p.deleted_at IS NULL`, id).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "reindexPost", "postID", id)
	}
	return nil
}

// UnindexRoom removes every post and comment of the room from the full-text
// index. Call it in the transaction that deletes the room, before the room
// goes.
func (s *Store) UnindexRoom(ctx context.Context, roomID uint64) error {
	table, idColumn := searchIndex(s.dialect)
	err := s.db(ctx).Exec("DELETE FROM "+table+" WHERE "+idColumn+" IN (SELECT id FROM posts WHERE room_id = ?)", roomID).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "unindexRoom", "roomID", roomID)
	}
	return nil
}

// IndexRows returns the text that the full-text index holds for each post ID.
// It is for diagnostics and tests: searches go through SearchPosts.
func (s *Store) IndexRows(ctx context.Context) (map[uint64]string, error) {
	var rows []struct {
		ID   uint64
		Text string
	}
	table, idColumn := searchIndex(s.dialect)
	if err := s.db(ctx).Raw("SELECT " + idColumn + " AS id, text FROM " + table).Scan(&rows).Error; err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "indexRows")
	}
	out := make(map[uint64]string, len(rows))
	for _, r := range rows {
		out[r.ID] = r.Text
	}
	return out, nil
}

// SearchTerm is a word or a phrase of a search.
type SearchTerm struct {
	// Text is the word or the phrase, as typed. It is never interpreted as
	// FTS5 syntax: SearchPosts quotes it.
	Text string
	// Phrase is true for an exact phrase; a word matches as a prefix.
	Phrase bool
}

// SearchQuery is what SearchPosts looks for. A zero field does not filter.
type SearchQuery struct {
	// ReaderID is the user who searches: only rooms that are visible, or that
	// the user is a member of, are searched. It is required.
	ReaderID uint64
	// Terms are the words and phrases; all of them have to match.
	Terms []SearchTerm
	// Tags are normalized tag names; the post has to carry all of them.
	Tags []string
	// Room keeps the posts of the room with this name (case does not matter).
	Room string
	// Author keeps the posts of the user with this (lower-case) username.
	Author string
	// After keeps the posts created at or after this time.
	After time.Time
	// Before keeps the posts created before this time.
	Before time.Time
	// OnlyPosts keeps the top-level posts.
	OnlyPosts bool
	// OnlyComments keeps the comments.
	OnlyComments bool
	// Limit is the most hits returned.
	Limit int
	// Offset is how many hits to skip.
	Offset int
}

// SearchHit is a post or comment that matches a search.
type SearchHit struct {
	// PostID is the ID of the post or comment.
	PostID uint64
	// RootID is the top-level post of a comment; 0 for a top-level post.
	RootID uint64
	// IsComment is true for a comment.
	IsComment bool
	// RoomID is the room of the post.
	RoomID uint64
	// RoomName is the name of the room.
	RoomName string
	// AuthorID is the author of the post.
	AuthorID uint64
	// AuthorUsername is the username of the author.
	AuthorUsername string
	// AuthorDisplayName is the display name of the author.
	AuthorDisplayName string
	// AuthorAvatarKey is the avatar key of the author; empty for none.
	AuthorAvatarKey string
	// CreatedAt is when the post was written.
	CreatedAt time.Time
	// Snippet is the matching text, not escaped: with words, a piece of the
	// indexed text with the matches between SearchMarkStart and
	// SearchMarkEnd; without, the start of it, cut at 200 characters.
	Snippet string
}

// matchExpression is the FTS5 MATCH expression of the terms. Every term is a
// quoted string with its quotes doubled, so nothing a user types is an
// operator; a word gets a * after its quotes to match as a prefix.
func matchExpression(terms []SearchTerm) string {
	parts := make([]string, len(terms))
	for i, t := range terms {
		q := `"` + strings.ReplaceAll(t.Text, `"`, `""`) + `"`
		if !t.Phrase {
			q += "*"
		}
		parts[i] = q
	}
	return strings.Join(parts, " ")
}

// SearchPosts returns the non-deleted posts and comments that match q, in the
// rooms that q.ReaderID may read. With terms they are ordered by relevance
// (bm25), then newest first (by ID); without, newest first. The room, author,
// date, tag and kind filters use parameters only.
func (s *Store) SearchPosts(ctx context.Context, q SearchQuery) ([]SearchHit, error) {
	if s.dialect == dialectPostgres {
		return s.searchPostsPostgres(ctx, q)
	}
	var sb strings.Builder
	var args []any
	words := len(q.Terms) > 0

	sb.WriteString(`SELECT p.id AS post_id, COALESCE(p.root_id, 0) AS root_id, (p.parent_id IS NOT NULL) AS is_comment,
  p.room_id AS room_id, r.name AS room_name, p.author_id AS author_id,
  u.username AS author_username, u.display_name AS author_display_name, u.avatar_key AS author_avatar_key,
  p.created_at AS created_at, `)
	if words {
		sb.WriteString("snippet(posts_fts, 0, ?, ?, '…', ?) AS snippet FROM posts_fts JOIN posts p ON p.id = posts_fts.rowid ")
		args = append(args, SearchMarkStart, SearchMarkEnd, snippetTokens)
	} else {
		sb.WriteString("substr(f.text, 1, ?) AS snippet FROM posts p LEFT JOIN posts_fts f ON f.rowid = p.id ")
		args = append(args, plainSnippetRunes+1)
	}
	sb.WriteString(`JOIN rooms r ON r.id = p.room_id JOIN users u ON u.id = p.author_id
WHERE p.deleted_at IS NULL
  AND (r.visibility = ? OR EXISTS (SELECT 1 FROM room_members rm WHERE rm.room_id = r.id AND rm.user_id = ?))`)
	args = append(args, VisibilityVisible, q.ReaderID)
	if words {
		sb.WriteString(" AND posts_fts MATCH ?")
		args = append(args, matchExpression(q.Terms))
	}
	args = appendSearchFilters(&sb, args, q)
	if words {
		sb.WriteString(" ORDER BY bm25(posts_fts) ASC, p.id DESC")
	} else {
		sb.WriteString(" ORDER BY p.id DESC")
	}
	sb.WriteString(" LIMIT ? OFFSET ?")
	args = append(args, q.Limit, q.Offset)

	var hits []SearchHit
	if err := s.db(ctx).Raw(sb.String(), args...).Scan(&hits).Error; err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "searchPosts", "userID", q.ReaderID)
	}
	if !words {
		for i := range hits {
			hits[i].Snippet = cutRunes(hits[i].Snippet, plainSnippetRunes)
		}
	}
	return hits, nil
}

// appendSearchFilters writes the conditions of q that do not depend on the
// full-text index (room, author, dates, kind and tags) to sb, each starting
// with " AND ", and returns args with their parameters. The query has the
// aliases p (posts), r (rooms) and u (users).
func appendSearchFilters(sb *strings.Builder, args []any, q SearchQuery) []any {
	if q.Room != "" {
		sb.WriteString(" AND r.name = ?")
		args = append(args, q.Room)
	}
	if q.Author != "" {
		sb.WriteString(" AND u.username = ?")
		args = append(args, q.Author)
	}
	if !q.After.IsZero() {
		sb.WriteString(" AND p.created_at >= ?")
		args = append(args, q.After.UTC())
	}
	if !q.Before.IsZero() {
		sb.WriteString(" AND p.created_at < ?")
		args = append(args, q.Before.UTC())
	}
	if q.OnlyPosts {
		sb.WriteString(" AND p.parent_id IS NULL")
	}
	if q.OnlyComments {
		sb.WriteString(" AND p.parent_id IS NOT NULL")
	}
	// One EXISTS per tag, with the tag's ID looked up once by the scalar
	// subquery: a probe of the unique index (post_id, tag_id) per tag is far
	// cheaper than counting a join of post_tags and tags for every row that
	// the full-text index matches (13.1 measured 138 ms against 62 ms for a
	// search of a very common word with two tags in the large demo). A tag
	// that does not exist has no ID: nothing matches.
	for _, name := range q.Tags {
		sb.WriteString(` AND EXISTS (SELECT 1 FROM post_tags pt WHERE pt.post_id = p.id
  AND pt.tag_id = (SELECT t.id FROM tags t WHERE t.name = ?))`)
		args = append(args, name)
	}
	return args
}

// cutRunes returns the first n characters of s, with an ellipsis when it
// had more.
func cutRunes(s string, n int) string {
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos] + "…"
		}
		i++
	}
	return s
}
