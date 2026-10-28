package store

import (
	"context"
	"strings"
	"time"

	"github.com/rm4n0s/errors"
)

// messageIndexSQLite is the SQL of the index text of the message m in SQLite:
// its body without the characters that mark a match. Migration 0006 holds the
// copy that shipped.
const messageIndexSQLite = `replace(replace(m.body, char(57344), ''), char(57345), '')`

// messageIndexPostgres is messageIndexSQLite for PostgreSQL: the same text.
const messageIndexPostgres = `replace(replace(m.body, chr(57344), ''), chr(57345), '')`

// messageIndexText is the SQL expression of the index text of the message m:
// its body without the characters that mark a match. The text is the same on
// both databases. Only a message that is not deleted and has a body is in the
// index (messageIndexable).
func messageIndexText(d dialect) string {
	if d == dialectPostgres {
		return messageIndexPostgres
	}
	return messageIndexSQLite
}

// messageIndexable is true for a message (the alias m) that belongs in the
// index: it is not deleted, has text and is no call entry (SPEC §7.18).
const messageIndexable = `m.deleted_at IS NULL AND m.body <> '' AND m.kind = 'text'`

// messageSearchIndex names the full-text index table of private messages of a
// dialect and its column of message IDs: messages_fts(rowid) in SQLite,
// messages_search(message_id) in PostgreSQL.
func messageSearchIndex(d dialect) (table, idColumn string) {
	if d == dialectPostgres {
		return "messages_search", "message_id"
	}
	return "messages_fts", "rowid"
}

// ReindexMessage makes the full-text index of private messages hold the
// current text of the message id: it deletes the row of the message and, when
// the message exists, is not deleted and has a body, inserts the body. A
// missing or deleted message is simply removed from the index. CreateMessage,
// UpdateMessageBody and MarkMessageDeleted call it, so the index follows the
// message in the transaction they run in.
func (s *Store) ReindexMessage(ctx context.Context, id uint64) error {
	table, idColumn := messageSearchIndex(s.dialect)
	if err := s.db(ctx).Exec("DELETE FROM "+table+" WHERE "+idColumn+" = ?", id).Error; err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "reindexMessage", "messageID", id)
	}
	err := s.db(ctx).Exec(`INSERT INTO `+table+` (`+idColumn+`, text)
SELECT m.id, `+messageIndexText(s.dialect)+` FROM messages m WHERE m.id = ? AND `+messageIndexable, id).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "reindexMessage", "messageID", id)
	}
	return nil
}

// RebuildMessageSearchIndex makes the full-text index of private messages
// hold every message that is not deleted and has a body, in one statement: it
// empties the index and fills it again. The large demo calls it after it
// wrote rows in bulk. Call it in a transaction.
func (s *Store) RebuildMessageSearchIndex(ctx context.Context) error {
	table, idColumn := messageSearchIndex(s.dialect)
	if err := s.db(ctx).Exec("DELETE FROM " + table).Error; err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "rebuildMessageSearchIndex", "step", "delete")
	}
	err := s.db(ctx).Exec(`INSERT INTO ` + table + ` (` + idColumn + `, text)
SELECT m.id, ` + messageIndexText(s.dialect) + ` FROM messages m WHERE ` + messageIndexable).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "rebuildMessageSearchIndex", "step", "insert")
	}
	return nil
}

// MessageIndexRows returns the text that the full-text index of private
// messages holds for each message ID. It is for diagnostics and tests:
// searches go through SearchMessages.
func (s *Store) MessageIndexRows(ctx context.Context) (map[uint64]string, error) {
	var rows []struct {
		ID   uint64
		Text string
	}
	table, idColumn := messageSearchIndex(s.dialect)
	if err := s.db(ctx).Raw("SELECT " + idColumn + " AS id, text FROM " + table).Scan(&rows).Error; err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "messageIndexRows")
	}
	out := make(map[uint64]string, len(rows))
	for _, r := range rows {
		out[r.ID] = r.Text
	}
	return out, nil
}

// MessageSearchQuery is what SearchMessages looks for.
type MessageSearchQuery struct {
	// ReaderID is the user who searches: only the messages of the
	// conversations the user is a member of now are searched. It is required.
	ReaderID uint64
	// Terms are the words and phrases; all of them have to match. Without
	// terms nothing matches.
	Terms []SearchTerm
	// Before keeps the messages with a smaller ID; 0 keeps all.
	Before uint64
	// Limit is the most hits returned.
	Limit int
}

// MessageSearchHit is a private message that matches a search.
type MessageSearchHit struct {
	// MessageID is the ID of the message.
	MessageID uint64
	// ConversationID is the conversation of the message.
	ConversationID uint64
	// ConversationKind says whether the conversation is direct or a group.
	ConversationKind ConversationKind
	// ConversationName is the name of a group as set; empty for none and for
	// a direct conversation.
	ConversationName string
	// AuthorID is the author of the message.
	AuthorID uint64
	// AuthorUsername is the username of the author.
	AuthorUsername string
	// AuthorDisplayName is the display name of the author.
	AuthorDisplayName string
	// AuthorAvatarKey is the avatar key of the author; empty for none.
	AuthorAvatarKey string
	// CreatedAt is when the message was sent.
	CreatedAt time.Time
	// Snippet is the matching text, not escaped: a piece of the message with
	// the matches between SearchMarkStart and SearchMarkEnd.
	Snippet string
}

// SearchMessages returns the messages that match q, newest first (by ID), in
// the conversations that q.ReaderID is a member of now, never a deleted one.
// The membership is part of the query, so a page holds q.Limit hits whenever
// that many exist: a message of a conversation the reader left, or never
// joined, is not a candidate. Every member of a conversation reads all its
// messages, the ones from before they joined included (SPEC §7.11), so those
// are found too.
func (s *Store) SearchMessages(ctx context.Context, q MessageSearchQuery) ([]MessageSearchHit, error) {
	if len(q.Terms) == 0 {
		return []MessageSearchHit{}, nil
	}
	if s.dialect == dialectPostgres {
		return s.searchMessagesPostgres(ctx, q)
	}
	var sb strings.Builder
	args := []any{SearchMarkStart, SearchMarkEnd, snippetTokens, q.ReaderID, matchExpression(q.Terms)}
	sb.WriteString(`SELECT m.id AS message_id, m.conversation_id AS conversation_id, c.kind AS conversation_kind,
  c.name AS conversation_name, m.author_id AS author_id, u.username AS author_username,
  u.display_name AS author_display_name, u.avatar_key AS author_avatar_key, m.created_at AS created_at,
  snippet(messages_fts, 0, ?, ?, '…', ?) AS snippet
FROM messages_fts
JOIN messages m ON m.id = messages_fts.rowid
JOIN conversation_members cm ON cm.conversation_id = m.conversation_id AND cm.user_id = ? AND cm.left_at IS NULL
JOIN conversations c ON c.id = m.conversation_id
JOIN users u ON u.id = m.author_id
WHERE messages_fts MATCH ? AND m.deleted_at IS NULL`)
	if q.Before != 0 {
		sb.WriteString(" AND m.id < ?")
		args = append(args, q.Before)
	}
	sb.WriteString(" ORDER BY m.id DESC LIMIT ?")
	args = append(args, q.Limit)
	var hits []MessageSearchHit
	if err := s.db(ctx).Raw(sb.String(), args...).Scan(&hits).Error; err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "searchMessages", "userID", q.ReaderID)
	}
	return hits, nil
}

// messagePostgresRow is a row of the search of messages on PostgreSQL: the hit
// and what snippetOf needs to make its snippet.
type messagePostgresRow struct {
	MessageSearchHit
	// Headline, Fragment, FragmentPos and TextLen are those of
	// postgresSearchRow.
	Headline    string
	Fragment    string
	FragmentPos int
	TextLen     int
}

// searchMessagesPostgres is SearchMessages for PostgreSQL: the matching rows
// are found with the tsvector and the GIN index and ordered newest first; the
// snippet is made by ts_headline for the rows of the page only and rebuilt in
// Go as for posts (see searchPostsPostgres).
func (s *Store) searchMessagesPostgres(ctx context.Context, q MessageSearchQuery) ([]MessageSearchHit, error) {
	expr, ok := tsQueryExpression(q.Terms)
	if !ok {
		return []MessageSearchHit{}, nil
	}
	var sb strings.Builder
	args := []any{expr, q.ReaderID}
	sb.WriteString(`WITH q AS (SELECT to_tsquery('` + searchConfig + `', ?) AS query), hits AS (
SELECT m.id AS message_id, m.conversation_id AS conversation_id, c.kind AS conversation_kind,
  c.name AS conversation_name, m.author_id AS author_id, u.username AS author_username,
  u.display_name AS author_display_name, u.avatar_key AS author_avatar_key, m.created_at AS created_at,
  ms.text AS body_text, q.query AS query
FROM messages_search ms
JOIN messages m ON m.id = ms.message_id
CROSS JOIN q
JOIN conversation_members cm ON cm.conversation_id = m.conversation_id AND cm.user_id = ? AND cm.left_at IS NULL
JOIN conversations c ON c.id = m.conversation_id
JOIN users u ON u.id = m.author_id
WHERE m.deleted_at IS NULL AND ms.tsv @@ q.query`)
	if q.Before != 0 {
		sb.WriteString(" AND m.id < ?")
		args = append(args, q.Before)
	}
	sb.WriteString(" ORDER BY m.id DESC LIMIT ?)")
	args = append(args, q.Limit)
	// The headline is made as in searchPostsPostgres: from the text with the
	// ASCII punctuation turned into spaces, and the snippet is made again from
	// the original text.
	sb.WriteString(`
SELECT h.message_id, h.conversation_id, h.conversation_kind, h.conversation_name, h.author_id,
  h.author_username, h.author_display_name, h.author_avatar_key, h.created_at, x.headline AS headline,
  strpos(z.norm, y.plain) AS fragment_pos,
  substr(h.body_text, strpos(z.norm, y.plain), char_length(y.plain)) AS fragment,
  char_length(h.body_text) AS text_len
FROM hits h
CROSS JOIN LATERAL (SELECT regexp_replace(h.body_text, '` + asciiPunctuation + `', ' ', 'g') AS norm) z
CROSS JOIN LATERAL (SELECT ts_headline('` + searchConfig + `', z.norm, h.query, ?) AS headline) x
CROSS JOIN LATERAL (SELECT replace(replace(x.headline, ?, ''), ?, '') AS plain) y
ORDER BY h.message_id DESC`)
	args = append(args, headlineOptions, SearchMarkStart, SearchMarkEnd)
	var rows []messagePostgresRow
	if err := s.db(ctx).Raw(sb.String(), args...).Scan(&rows).Error; err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "searchMessages", "userID", q.ReaderID)
	}
	phrase := hasPhrase(q.Terms)
	hits := make([]MessageSearchHit, len(rows))
	for i, r := range rows {
		hits[i] = r.MessageSearchHit
		hits[i].Snippet = snippetOf(postgresSearchRow{
			Headline: r.Headline, Fragment: r.Fragment, FragmentPos: r.FragmentPos, TextLen: r.TextLen,
		}, phrase)
	}
	return hits, nil
}
