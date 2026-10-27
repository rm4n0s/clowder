package store

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/rm4n0s/errors"
)

// searchConfig is the text search configuration of the PostgreSQL index: the
// "simple" configuration with the unaccent dictionary (migration 0001).
const searchConfig = "clowder_search"

// headlineOptions are the options of ts_headline: the matches between the
// two private-use markers, at most snippetTokens words in one fragment (the
// length of the snippet() of FTS5). ShortWord=0 keeps short words at the ends
// of the fragment, and MinWords=snippetTokens/2 lets a match near the start of
// a long text show the words around it.
var headlineOptions = "StartSel=" + SearchMarkStart + ", StopSel=" + SearchMarkEnd +
	", MaxFragments=0, MaxWords=" + strconv.Itoa(snippetTokens) +
	", MinWords=" + strconv.Itoa(snippetTokens/2) + ", ShortWord=0"

// tsQueryTokens splits text into the words that the index holds: the runs of
// letters, digits and combining marks, like the unicode61 tokenizer of FTS5.
// Anything else, punctuation included, only separates words. A token is safe
// to put in a tsquery without quotes.
func tsQueryTokens(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r)
	})
}

// tsQueryExpression is the to_tsquery expression of the terms, the PostgreSQL
// counterpart of matchExpression: every term is a phrase of its tokens
// (joined by the adjacency operator <->), a word matches as a prefix (:* on
// its last token), and all terms have to match (&). Nothing a user types is an
// operator: only letters, digits and marks reach the expression. A term with
// no token (only punctuation) is dropped, as FTS5 treats an empty phrase; ok
// is false when no term is left, and then nothing matches.
func tsQueryExpression(terms []SearchTerm) (expr string, ok bool) {
	parts := make([]string, 0, len(terms))
	for _, t := range terms {
		tokens := tsQueryTokens(t.Text)
		if len(tokens) == 0 {
			continue
		}
		if !t.Phrase {
			tokens[len(tokens)-1] += ":*"
		}
		parts = append(parts, "("+strings.Join(tokens, " <-> ")+")")
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, " & "), true
}

// searchPostsPostgres is SearchPosts for PostgreSQL. With terms the matching
// rows are found with the tsvector and the GIN index, ordered by ts_rank
// (normalized by the length of the text, as bm25 is) and then newest first;
// the snippet is made by ts_headline for the rows of the page only, with an
// ellipsis at each end that does not reach the end of the text, as snippet()
// does. Without terms it is the same query as in SQLite.
func (s *Store) searchPostsPostgres(ctx context.Context, q SearchQuery) ([]SearchHit, error) {
	var sb strings.Builder
	var args []any
	words := len(q.Terms) > 0
	columns := `p.id AS post_id, COALESCE(p.root_id, 0) AS root_id, (p.parent_id IS NOT NULL) AS is_comment,
  p.room_id AS room_id, r.name AS room_name, p.author_id AS author_id,
  u.username AS author_username, u.display_name AS author_display_name, p.created_at AS created_at`
	if words {
		expr, ok := tsQueryExpression(q.Terms)
		if !ok {
			return []SearchHit{}, nil
		}
		sb.WriteString("WITH q AS (SELECT to_tsquery('" + searchConfig + "', ?) AS query), hits AS (SELECT ")
		args = append(args, expr)
		sb.WriteString(columns + `, ps.text AS body_text, q.query AS query, ts_rank(ps.tsv, q.query, 1) AS rank
FROM posts_search ps JOIN posts p ON p.id = ps.post_id CROSS JOIN q `)
	} else {
		sb.WriteString("SELECT " + columns + ", substr(ps.text, 1, ?) AS snippet FROM posts p LEFT JOIN posts_search ps ON ps.post_id = p.id ")
		args = append(args, plainSnippetRunes+1)
	}
	sb.WriteString(`JOIN rooms r ON r.id = p.room_id JOIN users u ON u.id = p.author_id
WHERE p.deleted_at IS NULL
  AND (r.visibility = ? OR EXISTS (SELECT 1 FROM room_members rm WHERE rm.room_id = r.id AND rm.user_id = ?))`)
	args = append(args, VisibilityVisible, q.ReaderID)
	if words {
		sb.WriteString(" AND ps.tsv @@ q.query")
	}
	args = appendSearchFilters(&sb, args, q)
	if words {
		sb.WriteString(" ORDER BY rank DESC, p.id DESC LIMIT ? OFFSET ?)")
		args = append(args, q.Limit, q.Offset)
		// ts_headline reads the text with the parser of PostgreSQL, which keeps
		// "Budget-2026.xlsx", an address or a tag as one token (and drops a tag
		// from the output). So it reads the text with every ASCII punctuation
		// character turned into a space, which keeps every position, and the
		// snippet is made again from the original text (see snippetOf).
		sb.WriteString(`
SELECT h.post_id, h.root_id, h.is_comment, h.room_id, h.room_name, h.author_id, h.author_username,
  h.author_display_name, h.created_at, x.headline AS headline,
  strpos(z.norm, y.plain) AS fragment_pos, char_length(y.plain) AS fragment_len,
  substr(h.body_text, strpos(z.norm, y.plain), char_length(y.plain)) AS fragment,
  char_length(h.body_text) AS text_len
FROM hits h
CROSS JOIN LATERAL (SELECT regexp_replace(h.body_text, '` + asciiPunctuation + `', ' ', 'g') AS norm) z
CROSS JOIN LATERAL (SELECT ts_headline('` + searchConfig + `', z.norm, h.query, ?) AS headline) x
CROSS JOIN LATERAL (SELECT replace(replace(x.headline, ?, ''), ?, '') AS plain) y
ORDER BY h.rank DESC, h.post_id DESC`)
		args = append(args, headlineOptions, SearchMarkStart, SearchMarkEnd)
	} else {
		sb.WriteString(" ORDER BY p.id DESC LIMIT ? OFFSET ?")
		args = append(args, q.Limit, q.Offset)
	}

	if !words {
		var hits []SearchHit
		if err := s.db(ctx).Raw(sb.String(), args...).Scan(&hits).Error; err != nil {
			return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "searchPosts", "userID", q.ReaderID)
		}
		for i := range hits {
			hits[i].Snippet = cutRunes(hits[i].Snippet, plainSnippetRunes)
		}
		return hits, nil
	}
	var rows []postgresSearchRow
	if err := s.db(ctx).Raw(sb.String(), args...).Scan(&rows).Error; err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "searchPosts", "userID", q.ReaderID)
	}
	phrase := hasPhrase(q.Terms)
	hits := make([]SearchHit, len(rows))
	for i, r := range rows {
		hits[i] = r.SearchHit
		hits[i].Snippet = snippetOf(r, phrase)
	}
	return hits, nil
}

// asciiPunctuation is the regular expression class of the ASCII characters
// that are not letters or digits (control characters and space included): the
// ranges of migration 0001 of PostgreSQL, written out because the classes
// [[:alnum:]] depend on the locale of the database.
const asciiPunctuation = `[\x01-\x2F\x3A-\x40\x5B-\x60\x7B-\x7E]`

// postgresSearchRow is a row of the search with words: the hit and what
// snippetOf needs to make its snippet.
type postgresSearchRow struct {
	SearchHit
	// Headline is the fragment that ts_headline chose, from the text with its
	// punctuation turned into spaces, with the matches between the markers.
	Headline string
	// Fragment is the same fragment cut from the original text, without
	// markers, and FragmentPos is its (1-based) position in the text, which is
	// TextLen characters long. FragmentPos is 0 when the fragment was not found.
	Fragment    string
	FragmentPos int
	TextLen     int
}

// snippetOf makes the snippet of a hit as snippet() of FTS5 does: the fragment
// of the original text with the matches between the markers, and an ellipsis
// at each end that is not the end of the text. With a phrase in the query the
// words of a match that follow each other get one pair of markers (FTS5 marks
// a phrase as a whole, ts_headline marks its words one by one).
func snippetOf(r postgresSearchRow, phrase bool) string {
	text := overlayMarkers(r.Headline, r.Fragment)
	if phrase {
		text = adjacentMarks.ReplaceAllString(text, "$1")
	}
	if r.FragmentPos == 0 {
		return text
	}
	if r.FragmentPos > 1 {
		text = "…" + text
	}
	if r.FragmentPos-1+len([]rune(r.Fragment)) < r.TextLen {
		text += "…"
	}
	return text
}

// adjacentMarks matches the end of a match and the start of the next one with
// only white space between.
var adjacentMarks = regexp.MustCompile(SearchMarkEnd + `(\s+)` + SearchMarkStart)

// overlayMarkers puts the markers of headline on fragment. Both hold the same
// characters, except that headline has the markers and some characters of
// fragment (punctuation) are spaces in it. When their lengths disagree it
// returns headline.
func overlayMarkers(headline, fragment string) string {
	orig := []rune(fragment)
	var sb strings.Builder
	i := 0
	for _, r := range headline {
		if string(r) == SearchMarkStart || string(r) == SearchMarkEnd {
			sb.WriteRune(r)
			continue
		}
		if i >= len(orig) {
			return headline
		}
		sb.WriteRune(orig[i])
		i++
	}
	if i != len(orig) {
		return headline
	}
	return sb.String()
}

// hasPhrase reports whether a term is a phrase of several words.
func hasPhrase(terms []SearchTerm) bool {
	for _, t := range terms {
		if t.Phrase && len(tsQueryTokens(t.Text)) > 1 {
			return true
		}
	}
	return false
}
