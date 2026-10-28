package views_test

import (
	"html/template"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
)

func TestSearchPath(t *testing.T) {
	tests := []struct {
		q    string
		page int
		want string
	}{
		{"", 0, "/search"},
		{"", 1, "/search"},
		{"design", 1, "/search?q=design"},
		{"design", 0, "/search?q=design"},
		{"#design release", 1, "/search?q=%23design+release"},
		{`"a b" room:"x y"`, 3, "/search?page=3&q=%22a+b%22+room%3A%22x+y%22"},
		{"", 2, "/search?page=2"},
	}
	for _, tt := range tests {
		if got := views.SearchPath(tt.q, tt.page); got != tt.want {
			t.Errorf("SearchPath(%q, %d) = %q, want %q", tt.q, tt.page, got, tt.want)
		}
	}
}

func TestSearchChips(t *testing.T) {
	day := func(s string) time.Time {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	tests := []struct {
		name string
		q    app.Query
		want map[string]string // label -> the query without the filter, in order below
		keys []string
	}{
		{name: "words alone have no chips", q: app.Query{Terms: []app.Term{{Text: "design"}}}},
		{name: "nothing has no chips", q: app.Query{}},
		{
			name: "every kind, in a fixed order",
			q: app.Query{
				Terms: []app.Term{{Text: "a"}, {Text: "b c", Phrase: true}}, Tags: []string{"x", "y"}, Room: "team news", From: "bob",
				After: day("2026-10-01"), Before: day("2026-11-01"), PostsOnly: true, CommentsOnly: true,
			},
			keys: []string{"#x", "#y", `room:"team news"`, "from:bob", "after:2026-10-01", "before:2026-11-01", "is:post", "is:comment"},
			want: map[string]string{
				"#x":                `a "b c" #y room:"team news" from:bob after:2026-10-01 before:2026-11-01 is:post is:comment`,
				"#y":                `a "b c" #x room:"team news" from:bob after:2026-10-01 before:2026-11-01 is:post is:comment`,
				`room:"team news"`:  `a "b c" #x #y from:bob after:2026-10-01 before:2026-11-01 is:post is:comment`,
				"from:bob":          `a "b c" #x #y room:"team news" after:2026-10-01 before:2026-11-01 is:post is:comment`,
				"after:2026-10-01":  `a "b c" #x #y room:"team news" from:bob before:2026-11-01 is:post is:comment`,
				"before:2026-11-01": `a "b c" #x #y room:"team news" from:bob after:2026-10-01 is:post is:comment`,
				"is:post":           `a "b c" #x #y room:"team news" from:bob after:2026-10-01 before:2026-11-01 is:comment`,
				"is:comment":        `a "b c" #x #y room:"team news" from:bob after:2026-10-01 before:2026-11-01 is:post`,
			},
		},
		{name: "the last filter leaves an empty query", q: app.Query{Tags: []string{"only"}}, keys: []string{"#only"}, want: map[string]string{"#only": ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chips := views.SearchChips(tt.q)
			var labels []string
			for _, c := range chips {
				labels = append(labels, c.Label)
				want := views.SearchPath(tt.want[c.Label], 1)
				if c.Path != want {
					t.Errorf("chip %s: path = %q, want %q", c.Label, c.Path, want)
				}
			}
			if !reflect.DeepEqual(labels, tt.keys) {
				t.Errorf("labels = %v, want %v", labels, tt.keys)
			}
		})
	}

	// The query of every chip parses back to the query without that filter:
	// the other chips and the same words.
	parsed, err := app.ParseQuery(`design "exact words" #a #b room:general from:alice after:2026-10-01 is:post`)
	if err != nil {
		t.Fatal(err)
	}
	chips := views.SearchChips(parsed)
	if len(chips) != 6 {
		t.Fatalf("chips = %v, want 6", chips)
	}
	for _, chip := range chips {
		u, err := url.Parse(chip.Path)
		if err != nil {
			t.Fatal(err)
		}
		rest, err := app.ParseQuery(u.Query().Get("q"))
		if err != nil {
			t.Fatalf("chip %s: %v", chip.Label, err)
		}
		if !reflect.DeepEqual(rest.Terms, parsed.Terms) {
			t.Errorf("chip %s: terms = %v, want %v", chip.Label, rest.Terms, parsed.Terms)
		}
		var labels []string
		for _, c := range views.SearchChips(rest) {
			labels = append(labels, c.Label)
		}
		var want []string
		for _, c := range chips {
			if c.Label != chip.Label {
				want = append(want, c.Label)
			}
		}
		if !reflect.DeepEqual(labels, want) {
			t.Errorf("chip %s leaves %v, want %v", chip.Label, labels, want)
		}
	}
}

func TestSearchPage(t *testing.T) {
	user := app.User{DisplayName: "Alice", Username: "alice", Role: app.RoleMember}
	when := time.Date(2026, 10, 26, 9, 30, 0, 0, time.UTC)
	post := app.SearchResult{
		PostID: 7, RoomID: 2, RoomName: "general <x>", Author: app.UserRef{ID: 3, Username: "bob", DisplayName: "Bob <b>"},
		CreatedAt: when, Snippet: template.HTML("the <mark>design</mark> &amp; more"), Tags: []string{"design", "release"},
	}
	comment := app.SearchResult{
		PostID: 9, RootID: 7, IsComment: true, RoomID: 2, RoomName: "general", Author: app.UserRef{ID: 3, Username: "bob", DisplayName: "Bob"},
		CreatedAt: when, Snippet: template.HTML("a <mark>design</mark> comment"),
	}
	tests := []struct {
		name    string
		d       views.SearchData
		want    []string
		notWant []string
	}{
		{
			name: "an empty query is the box and the help",
			d:    views.SearchData{Sidebar: views.SidebarData{User: user, HideSearch: true}, Page: 1},
			want: []string{
				"<title id=\"page-title\">Search · Clowder</title>", `id="search-form"`, `name="q" type="search" value=""`, `id="search-panel"`,
				"Words, &#34;phrases&#34;, #tags, room:name, from:user, after:2026-10-01, before:…, is:post, is:comment",
			},
			notWant: []string{"Nothing found.", "search-result", `class="chips"`, "field-error", `class="pager"`, `id="sidebar-search"`},
		},
		{
			name: "a result",
			d: views.SearchData{
				Sidebar: views.SidebarData{User: user, HideSearch: true}, Query: "design", Searched: true, Page: 1,
				Results: []app.SearchResult{post},
			},
			want: []string{
				`value="design"`, `<li class="search-result" id="result-7">`,
				// Room (link, escaped), author, username, time.
				`<a class="search-room" href="/rooms/2">general &lt;x&gt;</a>`,
				`<span class="post-author">Bob &lt;b&gt;</span>`, `<span class="post-username">@bob</span>`,
				`datetime="2026-10-26T09:30:00Z"`, `>2026-10-26 09:30</time>`,
				// The snippet is the service's HTML, as it is.
				`<p class="search-snippet">the <mark>design</mark> &amp; more</p>`,
				`<a class="tag" href="/search?q=%23design">#design</a>`, `<a class="tag" href="/search?q=%23release">#release</a>`,
				`<a class="search-open" href="/posts/7">`, "Open the post",
			},
			notWant: []string{`class="badge"`, "Nothing found.", `class="pager"`, `class="chips"`, "&amp;amp;", "<b>"},
		},
		{
			name: "a comment links to its thread",
			d: views.SearchData{
				Sidebar: views.SidebarData{User: user, HideSearch: true}, Query: "design", Searched: true, Page: 1,
				Results: []app.SearchResult{comment},
			},
			want:    []string{`<span class="badge">comment</span>`, `<a class="search-open" href="/posts/7#post-9">`, "Open the comment"},
			notWant: []string{"Open the post", `class="tags"`},
		},
		{
			name: "the chips have a remove link",
			d: views.SearchData{
				Query: "design #release", Searched: true, Page: 1,
				Chips: views.SearchChips(app.Query{Terms: []app.Term{{Text: "design"}}, Tags: []string{"release"}}),
			},
			want: []string{
				`<ul class="chips" aria-label="Filters">`, `<span class="chip-label">#release</span>`,
				`<a class="chip-remove" href="/search?q=design"`, `aria-label="Remove #release"`,
				`data-on:click__prevent="document.getElementById(&#39;search-q&#39;).value = new URL(&#39;/search?q=design&#39;, location.origin).searchParams.get(&#39;q&#39;) || &#39;&#39;; @get(&#39;/search?q=design&#39;)"`, "Nothing found.",
			},
		},
		{
			name: "nothing found",
			d:    views.SearchData{Query: "zzz", Searched: true, Page: 1},
			want: []string{"Nothing found."}, notWant: []string{"search-result", `class="pager"`},
		},
		{
			name:    "a problem is under the box",
			d:       views.SearchData{Query: "after:soon", Message: "Use dates like 2026-10-27.", Page: 1},
			want:    []string{`<p class="field-error search-error" id="search-error" role="alert">Use dates like 2026-10-27.</p>`, `value="after:soon"`},
			notWant: []string{"Nothing found."},
		},
		{
			name: "newer and older keep the query",
			d: views.SearchData{
				Query: "design #x", Searched: true, Page: 2, HasNext: true,
				Results: []app.SearchResult{post},
			},
			want: []string{
				`<a class="newer" href="/search?q=design+%23x"`, `<a class="older" href="/search?page=3&amp;q=design+%23x"`, "Page 2",
			},
		},
		{
			name: "the first page has no newer link",
			d:    views.SearchData{Query: "design", Searched: true, Page: 1, HasNext: true, Results: []app.SearchResult{post}},
			want: []string{`class="older"`, "Page 1"}, notWant: []string{`class="newer"`},
		},
		{
			name: "the box is escaped",
			d:    views.SearchData{Query: `"><script>x</script>`, Page: 1},
			want: []string{`value="&#34;&gt;&lt;script&gt;x&lt;/script&gt;"`}, notWant: []string{"<script>x"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			html := render(t, views.SearchPage(tt.d))
			for _, w := range tt.want {
				if !strings.Contains(html, w) {
					t.Errorf("does not contain %q:\n%s", w, html)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(html, w) {
					t.Errorf("contains %q:\n%s", w, html)
				}
			}
		})
	}
}

func TestSidebarSearchBox(t *testing.T) {
	user := app.User{DisplayName: "Alice", Role: app.RoleMember}
	html := render(t, views.Sidebar(views.SidebarData{User: user}))
	for _, w := range []string{`<form id="sidebar-search" class="sidebar-search" role="search" method="get" action="/search">`, `name="q" type="search" placeholder="Search"`} {
		if !strings.Contains(html, w) {
			t.Errorf("does not contain %q:\n%s", w, html)
		}
	}
	html = render(t, views.Sidebar(views.SidebarData{User: user, HideSearch: true}))
	if strings.Contains(html, "sidebar-search") {
		t.Errorf("the search page's sidebar has a box:\n%s", html)
	}
}
