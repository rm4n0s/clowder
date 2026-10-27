package views

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
)

// searchHelp is the line of help under the search box.
const searchHelp = `Words, "phrases", #tags, room:name, from:user, after:2026-10-01, before:…, is:post, is:comment`

// searchDateLayout is how the dates of the filters after: and before: are
// written in a query.
const searchDateLayout = "2006-01-02"

// SearchChip is a filter of the query, shown as a chip with a link that
// removes it.
type SearchChip struct {
	// Label is the filter as it is written in a query, such as "#design" or
	// "room:general".
	Label string
	// Path is the path of the search without this filter: the other words and
	// filters of the query, on the first page.
	Path string
}

// SearchData is what the search page shows. The zero value is the page of an
// empty query.
type SearchData struct {
	// Sidebar is the sidebar of the page.
	Sidebar SidebarData
	// Query is the text of the search box, as typed.
	Query string
	// Message is the problem with the query, such as a date that is not a
	// date; empty shows none.
	Message string
	// Searched is true when the query was searched: it is not empty and has
	// no problem. Without it the page shows no results and no "Nothing
	// found.".
	Searched bool
	// Chips are the filters of the query.
	Chips []SearchChip
	// Results are the results of the page.
	Results []app.SearchResult
	// Page is the number of the page, starting at 1.
	Page int
	// HasNext is true when more results follow this page.
	HasNext bool
}

// SearchPath returns the path of the search for the text of the box, with the
// page number: /search?q=design&page=2. The page 1 and lower is not written,
// and an empty query gives /search.
func SearchPath(q string, page int) string {
	v := url.Values{}
	if q != "" {
		v.Set("q", q)
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	if enc := v.Encode(); enc != "" {
		return "/search?" + enc
	}
	return "/search"
}

// SearchChips returns the chips of the filters of a parsed query, in the
// order #tags, room, from, after, before, is:post, is:comment. The path of
// each chip is the search for the query without that filter.
func SearchChips(q app.Query) []SearchChip {
	var labels []string
	for _, t := range q.Tags {
		labels = append(labels, "#"+t)
	}
	if q.Room != "" {
		labels = append(labels, "room:"+filterValue(q.Room))
	}
	if q.From != "" {
		labels = append(labels, "from:"+filterValue(q.From))
	}
	if !q.After.IsZero() {
		labels = append(labels, "after:"+q.After.UTC().Format(searchDateLayout))
	}
	if !q.Before.IsZero() {
		labels = append(labels, "before:"+q.Before.UTC().Format(searchDateLayout))
	}
	if q.PostsOnly {
		labels = append(labels, "is:post")
	}
	if q.CommentsOnly {
		labels = append(labels, "is:comment")
	}
	chips := make([]SearchChip, len(labels))
	for i, label := range labels {
		parts := termTexts(q.Terms)
		for j, other := range labels {
			if j != i {
				parts = append(parts, other)
			}
		}
		chips[i] = SearchChip{Label: label, Path: SearchPath(strings.Join(parts, " "), 1)}
	}
	return chips
}

// termTexts returns the words and phrases of a query as they are typed: a
// phrase in double quotes.
func termTexts(terms []app.Term) []string {
	out := make([]string, 0, len(terms))
	for _, t := range terms {
		if t.Phrase {
			out = append(out, `"`+t.Text+`"`)
		} else {
			out = append(out, t.Text)
		}
	}
	return out
}

// filterValue is the value of a filter as it is typed: in double quotes when
// it has white space.
func filterValue(v string) string {
	if strings.ContainsFunc(v, func(c rune) bool { return c == ' ' || c == '\t' || c == '\n' }) {
		return `"` + v + `"`
	}
	return v
}

// searchResultPath is where a result links to: the page of the post, or for a
// comment its place in the thread of its post.
func searchResultPath(r app.SearchResult) string {
	if r.IsComment && r.RootID != 0 {
		return CommentPath(r.RootID, r.PostID)
	}
	return PostPath(r.PostID)
}

// searchResultTime is the time of a result as shown, in UTC.
func searchResultTime(t time.Time) string {
	return postTimeText(t)
}

// searchGet returns the Datastar action that fetches the search at path.
func searchGet(path string) string {
	return "@get('" + escapeQuote(path) + "')"
}

// chipAction returns the Datastar action of the remove link of a chip: it puts
// the query of the link in the search box, which a patch of the results leaves
// alone, and fetches the search at path. The path is escaped, so that it can
// sit inside the quotes of the expression.
func chipAction(path string) string {
	p := escapeQuote(path)
	return "document.getElementById('search-q').value = new URL('" + p + "', location.origin).searchParams.get('q') || ''; @get('" + p + "')"
}

// searchBoxAction is the Datastar action that sends the search form it is on
// as the query of a GET.
const searchBoxAction = "@get('/search', {contentType: 'form'})"
