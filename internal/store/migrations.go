package store

// rawMigrations is the list of raw SQL migrations that Open applies, in
// order. Never change or reorder one that has shipped: add a new one at the
// end.
func rawMigrations() []migration {
	return []migration{
		{ID: "0001_posts_fts", SQL: migrationPostsFTS},
	}
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
