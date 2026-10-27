package app

import (
	"context"
	"html"
	"html/template"
	"strings"
	"time"
	"unicode"

	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// The limits of search (SPEC §7.8).
const (
	// searchPageSize is the number of results on a page.
	searchPageSize = 20
	// searchMaxPage is the last page of results that is looked up; a later one
	// is empty. It keeps a request for page 9223372036854775807 from asking
	// the index to skip that many rows (or the offset from overflowing).
	searchMaxPage = 1000
	// searchMaxQueryRunes is how much of a query counts; the rest is ignored.
	searchMaxQueryRunes = 500
	// searchMaxTerms is how many words and phrases of a query count; the rest
	// are ignored.
	searchMaxTerms = 32
	// FieldQuery names the search box in the metadata "fields" of a
	// TagInvalidInput error.
	FieldQuery = "q"
	// messageSearchDate is what a malformed date in a query is answered with.
	messageSearchDate = "Use dates like 2026-10-27."
	// searchDateLayout is the layout of the dates of after: and before:.
	searchDateLayout = "2006-01-02"
)

// Term is a word or an exact phrase of a search query.
type Term struct {
	// Text is the word or the phrase, without the quotes that made it a
	// phrase.
	Text string
	// Phrase is true for an exact phrase, written in double quotes; a word
	// matches as a prefix.
	Phrase bool
}

// Query is a search query, parsed (SPEC §7.8). ParseQuery makes it.
type Query struct {
	// Terms are the words and phrases; every one has to match.
	Terms []Term
	// Tags are the normalized tags of the #tag terms, without duplicates;
	// the post has to carry all of them.
	Tags []string
	// Room is the name given in room:, empty when there is none. The last
	// room: of a query counts.
	Room string
	// From is the lower-case username given in from:, empty when there is
	// none. The last from: of a query counts.
	From string
	// After is the first instant of the day given in after: (UTC): posts on or
	// after that day. Zero when there is none.
	After time.Time
	// Before is the first instant of the day given in before: (UTC): posts
	// strictly before that day. Zero when there is none.
	Before time.Time
	// PostsOnly is true when the query has is:post.
	PostsOnly bool
	// CommentsOnly is true when the query has is:comment. With is:post too,
	// nothing matches.
	CommentsOnly bool
}

// HasWords reports whether the query has words or phrases to look for.
func (q Query) HasWords() bool {
	return len(q.Terms) > 0
}

// IsEmpty reports whether the query has neither words nor filters. Searching
// for it finds nothing.
func (q Query) IsEmpty() bool {
	return len(q.Terms) == 0 && len(q.Tags) == 0 && q.Room == "" && q.From == "" &&
		q.After.IsZero() && q.Before.IsZero() && !q.PostsOnly && !q.CommentsOnly
}

// ParseQuery reads the text of the search box. It is a pure function, and
// nothing a user types is an error except a malformed date.
//
// The text is split on white space into tokens:
//
//   - a token that starts with a double quote and has a closing quote later in
//     the text is an exact phrase, whatever is between the quotes; a quote
//     with no partner, or in the middle of a word, is an ordinary character
//   - #tag is a tag filter, normalized like the tags of a post (lower case,
//     without the leading #); it may repeat, and all must be on the post. A
//     "#" followed by something that is not a valid tag is a word
//   - room:<name>, from:<username>, after:<YYYY-MM-DD>, before:<YYYY-MM-DD>,
//     is:post and is:comment are filters; the key is not case sensitive and
//     the value may be in double quotes (room:"team news"). after: is on or
//     after that day (UTC) and before: strictly before it. A key with an
//     empty value, an unknown is: value and an unknown key (a:b) are words
//   - everything else is a word, matched as a prefix
//
// Words and phrases have no FTS5 syntax: OR, NOT, NEAR, *, ^, -, : and
// parentheses are plain text. A word with no letter or digit is dropped; at
// most 32 words and phrases, and the first 500 characters of the text, count.
// A malformed date fails with TagInvalidInput and the message "Use dates like
// 2026-10-27." in the metadata "fields" under "q".
func ParseQuery(s string) (Query, error) {
	var q Query
	r := []rune(s)
	if len(r) > searchMaxQueryRunes {
		r = r[:searchMaxQueryRunes]
	}
	for i := 0; i < len(r); {
		if unicode.IsSpace(r[i]) {
			i++
			continue
		}
		if r[i] == '"' {
			if end := indexRune(r, '"', i+1); end >= 0 {
				addTerm(&q, string(r[i+1:end]), true)
				i = end + 1
				continue
			}
		}
		key, value, next, ok := scanFilter(r, i)
		if !ok {
			end := i
			for end < len(r) && !unicode.IsSpace(r[end]) {
				end++
			}
			addWord(&q, string(r[i:end]))
			i = end
			continue
		}
		raw := string(r[i:next])
		i = next
		switch key {
		case "room":
			q.Room = value
		case "from":
			q.From = strings.ToLower(strings.TrimPrefix(value, "@"))
			if q.From == "" {
				addWord(&q, raw)
			}
		case "is":
			switch strings.ToLower(value) {
			case "post":
				q.PostsOnly = true
			case "comment":
				q.CommentsOnly = true
			default:
				addWord(&q, raw)
			}
		case "after", "before":
			day, err := time.Parse(searchDateLayout, value)
			if err != nil {
				return Query{}, errors.NewErr(TagInvalidInput, err).
					SetMetadata("fields", map[string]string{FieldQuery: messageSearchDate}, "key", key)
			}
			if key == "after" {
				q.After = day.UTC()
			} else {
				q.Before = day.UTC()
			}
		}
	}
	return q, nil
}

// scanFilter reads a filter token that starts at r[i]: a known key (letters
// only, any case, lower-cased in the result), a colon and a value that is
// either in double quotes or runs to the next white space. next is the index
// after the token. ok is false when r[i:] does not start a filter or the
// value is empty.
func scanFilter(r []rune, i int) (key, value string, next int, ok bool) {
	j := i
	for j < len(r) && (r[j] >= 'a' && r[j] <= 'z' || r[j] >= 'A' && r[j] <= 'Z') {
		j++
	}
	if j == i || j >= len(r) || r[j] != ':' {
		return "", "", 0, false
	}
	key = strings.ToLower(string(r[i:j]))
	switch key {
	case "room", "from", "is", "after", "before":
	default:
		return "", "", 0, false
	}
	k := j + 1
	if k < len(r) && r[k] == '"' {
		if end := indexRune(r, '"', k+1); end >= 0 {
			value = strings.TrimSpace(string(r[k+1 : end]))
			return key, value, end + 1, value != ""
		}
	}
	end := k
	for end < len(r) && !unicode.IsSpace(r[end]) {
		end++
	}
	value = string(r[k:end])
	return key, value, end, value != ""
}

// indexRune returns the index of the first c in r from index from on, or -1.
func indexRune(r []rune, c rune, from int) int {
	for i := from; i < len(r); i++ {
		if r[i] == c {
			return i
		}
	}
	return -1
}

// addWord adds a token that is not a filter: a tag when it is #tag, a word
// otherwise.
func addWord(q *Query, token string) {
	if strings.HasPrefix(token, "#") {
		name := strings.ToLower(strings.TrimLeft(token, "#"))
		if validTag(name) {
			for _, t := range q.Tags {
				if t == name {
					return
				}
			}
			q.Tags = append(q.Tags, name)
			return
		}
	}
	addTerm(q, token, false)
}

// addTerm adds a word or a phrase to the query unless it is empty, has
// nothing to match (no letter or digit) or the query has enough terms.
func addTerm(q *Query, text string, phrase bool) {
	text = strings.Map(func(c rune) rune {
		switch c {
		case 0, '\ue000', '\ue001': // NUL and the match markers of store.SearchMarkStart and SearchMarkEnd
			return -1
		}
		return c
	}, text)
	text = strings.Join(strings.Fields(text), " ")
	if len(q.Terms) >= searchMaxTerms || !strings.ContainsFunc(text, func(c rune) bool {
		return unicode.IsLetter(c) || unicode.IsNumber(c)
	}) {
		return
	}
	q.Terms = append(q.Terms, Term{Text: text, Phrase: phrase})
}

// SearchResult is a post or comment found by a search.
type SearchResult struct {
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
	// Author is the author of the post.
	Author UserRef
	// CreatedAt is when the post was written.
	CreatedAt time.Time
	// Snippet is the matching text, escaped, with the matches in <mark>; it
	// holds no other HTML. Without words in the query it is the start of the
	// post, cut at 200 characters.
	Snippet template.HTML
	// Segments are Snippet as plain text in pieces: each piece is text and
	// says whether it is a match. Joined, the texts are the snippet without
	// the markup; it is what a client that shows no HTML uses.
	Segments []SnippetSegment
	// Tags are the tags of the post, in the order the author gave them.
	Tags []string
}

// SnippetSegment is a piece of the snippet of a search result.
type SnippetSegment struct {
	// Text is the piece, as written (not escaped).
	Text string
	// Match is true for the words the query found.
	Match bool
}

// SearchPage is a page of search results.
type SearchPage struct {
	// Query is the text that was searched, as typed.
	Query string
	// Parsed is Query, parsed.
	Parsed Query
	// Results are the results of the page: by relevance, then newest first;
	// newest first when the query has no words.
	Results []SearchResult
	// Page is the number of the page, starting at 1.
	Page int
	// HasNext is true when more results follow this page.
	HasNext bool
}

// Search is the service of search (SPEC §7.8). Make it with NewSearch. It is
// safe for concurrent use.
type Search struct {
	st *store.Store
}

// NewSearch makes the service.
func NewSearch(st *store.Store) *Search {
	return &Search{st: st}
}

// Query searches the posts and comments that the actor may read: those in
// visible rooms and in the private rooms the actor is a member of (admins are
// no exception, D29), never a deleted one. q is parsed by ParseQuery, whose
// TagInvalidInput error it returns untouched. A query with neither words nor
// filters finds nothing and is no error. page starts at 1 (a smaller value
// counts as 1); a page has 20 results, and a page past the 1000th is empty.
func (s *Search) Query(ctx context.Context, actor Actor, q string, page int) (SearchPage, error) {
	parsed, err := ParseQuery(q)
	if err != nil {
		return SearchPage{}, err
	}
	if page < 1 {
		page = 1
	}
	out := SearchPage{Query: q, Parsed: parsed, Results: []SearchResult{}, Page: page}
	if parsed.IsEmpty() || page > searchMaxPage {
		return out, nil
	}
	terms := make([]store.SearchTerm, len(parsed.Terms))
	for i, t := range parsed.Terms {
		terms[i] = store.SearchTerm{Text: t.Text, Phrase: t.Phrase}
	}
	hits, err := s.st.SearchPosts(ctx, store.SearchQuery{
		ReaderID:     actor.UserID,
		Terms:        terms,
		Tags:         parsed.Tags,
		Room:         parsed.Room,
		Author:       parsed.From,
		After:        parsed.After,
		Before:       parsed.Before,
		OnlyPosts:    parsed.PostsOnly,
		OnlyComments: parsed.CommentsOnly,
		Limit:        searchPageSize + 1,
		Offset:       (page - 1) * searchPageSize,
	})
	if err != nil {
		return SearchPage{}, err
	}
	if len(hits) > searchPageSize {
		out.HasNext = true
		hits = hits[:searchPageSize]
	}
	ids := make([]uint64, len(hits))
	for i, h := range hits {
		ids[i] = h.PostID
	}
	tags, err := s.st.TagNamesOfPosts(ctx, ids)
	if err != nil {
		return SearchPage{}, err
	}
	for _, h := range hits {
		out.Results = append(out.Results, SearchResult{
			PostID:    h.PostID,
			RootID:    h.RootID,
			IsComment: h.IsComment,
			RoomID:    h.RoomID,
			RoomName:  h.RoomName,
			Author:    UserRef{ID: h.AuthorID, Username: h.AuthorUsername, DisplayName: h.AuthorDisplayName},
			CreatedAt: h.CreatedAt,
			Snippet:   snippetHTML(h.Snippet),
			Segments:  snippetSegments(h.Snippet),
			Tags:      nonNil(tags[h.PostID]),
		})
	}
	return out, nil
}

// snippetHTML escapes the snippet of a hit and turns the match markers into
// <mark> tags. The markers cannot be in user text (the index drops them), so
// the only tags in the result are the ones made here.
func snippetHTML(raw string) template.HTML {
	escaped := html.EscapeString(raw)
	escaped = strings.ReplaceAll(escaped, store.SearchMarkStart, "<mark>")
	escaped = strings.ReplaceAll(escaped, store.SearchMarkEnd, "</mark>")
	return template.HTML(escaped) //nolint:gosec // escaped above; the only markup is the <mark> made here
}

// snippetSegments splits the snippet of a hit at its match markers, in
// plain text. The markers cannot be in user text (the index drops them), so
// each start marker begins a match and the next end marker ends it. A match
// that never ends runs to the end of the text, and an end marker with no start
// is part of the text. Empty pieces are left out.
func snippetSegments(raw string) []SnippetSegment {
	segments := []SnippetSegment{}
	match := false
	for raw != "" {
		marker := store.SearchMarkStart
		if match {
			marker = store.SearchMarkEnd
		}
		text, rest, found := strings.Cut(raw, marker)
		if text != "" {
			segments = append(segments, SnippetSegment{Text: text, Match: match})
		}
		if !found {
			break
		}
		raw = rest
		match = !match
	}
	return segments
}
