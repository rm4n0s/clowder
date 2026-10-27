package web

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
)

// searchEnv is a roomsEnv with posts to search, all written at 2026-10-26
// 09:30 (the start of the fake clock):
//
//	alice in general  "Release notes for the new design"  tags design, release
//	bob   in general  "Design review on Friday"           tag design
//	bob   in general  "Lunch menu"                        no tags
//	alice in secret   "The zebra plan"                    tag design (alice and bob are members)
type searchEnv struct {
	*roomsEnv
	notes, review, lunch, zebra app.Post
}

func newSearchEnv(t *testing.T) *searchEnv {
	t.Helper()
	e := &searchEnv{roomsEnv: newRoomsEnv(t)}
	e.notes = e.writePost("alice", "general", "Release notes for the new design", "design", "release")
	e.review = e.writePost("bob", "general", "Design review on Friday", "design")
	e.lunch = e.writePost("bob", "general", "Lunch menu")
	e.zebra = e.writePost("alice", "secret", "The zebra plan", "design")
	return e
}

// searchAs gets /search?q=q as the user, plain, and returns the page.
func (e *searchEnv) searchAs(user, q string) string {
	e.t.Helper()
	rec := e.getAs(user, "/search?q="+url.QueryEscape(q), false)
	if rec.Code != http.StatusOK {
		e.t.Fatalf("GET /search?q=%s: status = %d, want 200; body:\n%s", q, rec.Code, rec.Body)
	}
	return rec.Body.String()
}

// resultIDs returns the IDs of the results of a search page, in order.
func resultIDs(body string) []string {
	ids := []string{}
	for _, m := range regexp.MustCompile(`<li class="search-result" id="result-(\d+)"`).FindAllStringSubmatch(body, -1) {
		ids = append(ids, m[1])
	}
	return ids
}

// idsOf returns the ID texts of posts.
func idsOf(posts ...app.Post) []string {
	ids := make([]string, len(posts))
	for i, p := range posts {
		ids[i] = idText(p.ID)
	}
	return ids
}

// requireResultSet fails the test unless the page has the results of exactly
// these posts, in any order.
func requireResultSet(t *testing.T, body string, want ...app.Post) {
	t.Helper()
	got, wantIDs := resultIDs(body), idsOf(want...)
	sort.Strings(got)
	sort.Strings(wantIDs)
	if strings.Join(got, ",") != strings.Join(wantIDs, ",") {
		t.Errorf("results = %v, want %v; body:\n%s", got, wantIDs, body)
	}
}

// chipLinks returns, for every chip of a search page, its label and the query
// "q" of its remove link.
func chipLinks(t *testing.T, body string) map[string]string {
	t.Helper()
	out := map[string]string{}
	re := regexp.MustCompile(`<span class="chip-label">(.*?)</span>\s*<a class="chip-remove" href="([^"]*)"`)
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		u, err := url.Parse(html.UnescapeString(m[2]))
		if err != nil {
			t.Fatalf("chip link %q: %v", m[2], err)
		}
		if u.Path != "/search" {
			t.Errorf("chip link %q is not a search", m[2])
		}
		out[html.UnescapeString(m[1])] = u.Query().Get("q")
	}
	return out
}

func TestSearchEmptyQuery(t *testing.T) {
	t.Parallel()
	e := newSearchEnv(t)
	for _, path := range []string{"/search", "/search?q=", "/search?q=+++", "/search?q=%22%22"} {
		t.Run(path, func(t *testing.T) {
			rec := e.getAs("alice", path, false)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			requireBody(t, rec, []string{
				"<title>Search · Clowder</title>", `<h1>Search</h1>`, `id="search-form"`, `role="search"`, `method="get" action="/search"`,
				`name="q" type="search"`, `id="search-panel"`,
				`Words, &#34;phrases&#34;, #tags, room:name, from:user, after:2026-10-01, before:…, is:post, is:comment`,
				// The sidebar is there, but the page has its own box.
				`id="sidebar-user">Alice</p>`, `id="logout-form"`,
			}, "Nothing found.", "search-result", `class="chips"`, `id="sidebar-search"`, "field-error", `class="pager"`)
		})
	}
}

func TestSearchWordsHighlightAndResultFields(t *testing.T) {
	t.Parallel()
	e := newSearchEnv(t)
	body := e.searchAs("alice", "design")
	// Tags are not words: the post of the secret room has the tag only.
	requireResultSet(t, body, e.notes, e.review)
	requireContains(t, body, []string{
		// The matching text is highlighted.
		`<mark>design</mark>`, `<mark>Design</mark> review on Friday`,
		// Room (link), author (display name and username), time, the link to the post.
		`<a class="search-room" href="/rooms/` + idText(e.general.ID) + `">general</a>`,
		`<span class="post-author">Bob</span>`, `<span class="post-username">@bob</span>`,
		`<span class="post-author">Alice</span>`, `<span class="post-username">@alice</span>`,
		`<time class="post-time" datetime="2026-10-26T09:30:00Z"`, `>2026-10-26 09:30</time>`,
		`<a class="search-open" href="/posts/` + idText(e.review.ID) + `">`,
		// Tags are links to their search.
		`<a class="tag" href="/search?q=%23release">#release</a>`, `<a class="tag" href="/search?q=%23design">#design</a>`,
		// The box keeps the query.
		`value="design"`,
	}, []string{"Lunch menu", `class="badge">comment`, `class="chips"`, "Nothing found.", `id="sidebar-search"`})

	// The post of a private room is found by its words by a member.
	requireContains(t, e.searchAs("alice", "zebra"), []string{`<a class="search-room" href="/rooms/` + idText(e.secret.ID) + `">secret</a>`, `<mark>zebra</mark> plan`}, nil)

	// A prefix matches, and several words all have to match.
	requireResultSet(t, e.searchAs("alice", "desi"), e.notes, e.review)
	requireResultSet(t, e.searchAs("alice", "design friday"), e.review)
	requireResultSet(t, e.searchAs("alice", `"release notes"`), e.notes)
	requireContains(t, e.searchAs("alice", `"release notes"`), []string{`<mark>Release notes</mark> for the new design`}, nil)
}

func TestSearchFromATagLink(t *testing.T) {
	t.Parallel()
	e := newSearchEnv(t)
	// The tag link of a post card is what the user clicks.
	feed := e.getAs("bob", "/rooms/"+idText(e.general.ID), false).Body.String()
	link := `href="/search?q=%23release"`
	if !strings.Contains(feed, link) {
		t.Fatalf("the room page has no tag link %s", link)
	}
	body := e.getAs("bob", "/search?q=%23release", false).Body.String()
	requireResultSet(t, body, e.notes)
	requireContains(t, body, []string{`value="#release"`, `<span class="chip-label">#release</span>`}, []string{"Nothing found."})
	// The chip removes the tag: nothing is left to search.
	if got := chipLinks(t, body); len(got) != 1 || got["#release"] != "" {
		t.Errorf("chips = %v, want #release with an empty query", got)
	}
	requireContains(t, body, []string{`href="/search"`}, nil)
}

func TestSearchFiltersAndChips(t *testing.T) {
	t.Parallel()
	e := newSearchEnv(t)
	tests := []struct {
		name  string
		q     string
		chips map[string]string // label -> the query that removes it
		want  []app.Post
	}{
		{
			name: "two tags", q: "#design #release",
			chips: map[string]string{"#design": "#release", "#release": "#design"},
			want:  []app.Post{e.notes},
		},
		{
			name: "words and a tag", q: "friday #design",
			chips: map[string]string{"#design": "friday"},
			want:  []app.Post{e.review},
		},
		{
			name: "a phrase is kept in quotes", q: `"release notes" #design`,
			chips: map[string]string{"#design": `"release notes"`},
			want:  []app.Post{e.notes},
		},
		{
			name: "room", q: "design room:general",
			chips: map[string]string{"room:general": "design"},
			want:  []app.Post{e.notes, e.review},
		},
		{
			name: "from, with the @ and the case dropped", q: "FROM:@Bob",
			chips: map[string]string{"from:bob": ""},
			want:  []app.Post{e.review, e.lunch},
		},
		{
			name: "dates", q: "after:2026-10-01 before:2026-12-31 lunch",
			chips: map[string]string{
				"after:2026-10-01":  "lunch before:2026-12-31",
				"before:2026-12-31": "lunch after:2026-10-01",
			},
			want: []app.Post{e.lunch},
		},
		{
			name: "posts only", q: "is:post menu",
			chips: map[string]string{"is:post": "menu"},
			want:  []app.Post{e.lunch},
		},
		{
			name: "all of them", q: "design #design #release room:general from:alice is:post after:2026-10-26 before:2026-10-27",
			chips: map[string]string{
				"#design":           "design #release room:general from:alice after:2026-10-26 before:2026-10-27 is:post",
				"#release":          "design #design room:general from:alice after:2026-10-26 before:2026-10-27 is:post",
				"room:general":      "design #design #release from:alice after:2026-10-26 before:2026-10-27 is:post",
				"from:alice":        "design #design #release room:general after:2026-10-26 before:2026-10-27 is:post",
				"after:2026-10-26":  "design #design #release room:general from:alice before:2026-10-27 is:post",
				"before:2026-10-27": "design #design #release room:general from:alice after:2026-10-26 is:post",
				"is:post":           "design #design #release room:general from:alice after:2026-10-26 before:2026-10-27",
			},
			want: []app.Post{e.notes},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := e.searchAs("alice", tt.q)
			requireResultSet(t, body, tt.want...)
			got := chipLinks(t, body)
			if len(got) != len(tt.chips) {
				t.Errorf("chips = %v, want %v", got, tt.chips)
			}
			for label, want := range tt.chips {
				if got[label] != want {
					t.Errorf("removing %s gives %q, want %q", label, got[label], want)
				}
				// The query a chip leads to is a search of its own that parses
				// and keeps the other filters.
				if rest := e.getAs("alice", "/search?q="+url.QueryEscape(got[label]), false); rest.Code != http.StatusOK {
					t.Errorf("the page without %s: status = %d", label, rest.Code)
				}
			}
		})
	}

	t.Run("a room with a space is quoted", func(t *testing.T) {
		if _, err := e.rooms.Create(t.Context(), e.actor(e.admin), app.RoomInput{Name: "team news", Visibility: app.VisibilityVisible}); err != nil {
			t.Fatal(err)
		}
		body := e.searchAs("admin", `room:"team news" hello`)
		if got := chipLinks(t, body); got[`room:"team news"`] != "hello" || len(got) != 1 {
			t.Errorf("chips = %v", got)
		}
	})

	t.Run("a chip is also a Datastar link", func(t *testing.T) {
		body := e.searchAs("alice", "friday #design")
		requireContains(t, body, []string{`aria-label="Remove #design"`, `; @get(&#39;/search?q=friday&#39;)"`, `new URL(&#39;/search?q=friday&#39;, location.origin)`}, nil)
	})
}

func TestSearchNothingFound(t *testing.T) {
	t.Parallel()
	e := newSearchEnv(t)
	for _, q := range []string{"nonexistentword", "design room:random", "#nosuchtag", "is:comment lunch"} {
		t.Run(q, func(t *testing.T) {
			body := e.searchAs("alice", q)
			requireContains(t, body, []string{"Nothing found.", `id="search-panel"`}, []string{"search-result", `class="pager"`})
		})
	}
	// With filters only, the chips stay so that they can be removed.
	requireContains(t, e.searchAs("alice", "#nosuchtag"), []string{`<span class="chip-label">#nosuchtag</span>`}, nil)
}

func TestSearchBadDate(t *testing.T) {
	t.Parallel()
	e := newSearchEnv(t)
	q := "design after:soon"
	t.Run("plain", func(t *testing.T) {
		rec := e.getAs("alice", "/search?q="+url.QueryEscape(q), false)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422; body:\n%s", rec.Code, rec.Body)
		}
		requireBody(t, rec, []string{
			`<p class="field-error search-error" id="search-error" role="alert">Use dates like 2026-10-27.</p>`,
			`id="search-form"`, `value="design after:soon"`, `id="sidebar-user">Alice</p>`,
		}, "Nothing found.", "search-result", `class="chips"`)
		// The message is directly under the box and above the help.
		body := rec.Body.String()
		form, msg, help := strings.Index(body, `id="search-form"`), strings.Index(body, `id="search-error"`), strings.Index(body, `id="search-help"`)
		if form >= msg || msg >= help {
			t.Errorf("the message is not between the box and the help:\n%s", body)
		}
	})
	t.Run("datastar", func(t *testing.T) {
		rec := e.getAs("alice", "/search?q="+url.QueryEscape(q), true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{`id="search-panel"`, `id="search-error"`, "Use dates like 2026-10-27."}, "<aside", `id="search-form"`, "<html")
	})
	t.Run("the other date and a month that does not exist", func(t *testing.T) {
		for _, bad := range []string{"before:tomorrow", "after:2026-13-01", "after:2026-02-30"} {
			rec := e.getAs("alice", "/search?q="+url.QueryEscape(bad), false)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("%s: status = %d, want 422", bad, rec.Code)
			}
		}
	})
}

func TestSearchPagination(t *testing.T) {
	t.Parallel()
	e := newSearchEnv(t)
	for i := 0; i < 24; i++ {
		e.writePost("bob", "general", "paginate item "+strconv.Itoa(i))
	}
	q := `paginate "item" room:general`
	enc := url.QueryEscape(q)

	t.Run("first page", func(t *testing.T) {
		body := e.searchAs("alice", q)
		if n := len(resultIDs(body)); n != 20 {
			t.Errorf("results = %d, want 20", n)
		}
		requireContains(t, body, []string{
			`<a class="older" href="/search?page=2&amp;q=` + html.EscapeString(enc) + `"`, "Page 1",
		}, []string{`class="newer"`})
	})
	t.Run("second page", func(t *testing.T) {
		body := e.searchAs("alice", q+"&page=2")
		// "&page=2" was inside the query: a real page parameter is below.
		_ = body
		rec := e.getAs("alice", "/search?q="+enc+"&page=2", false)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		page2 := rec.Body.String()
		if n := len(resultIDs(page2)); n != 4 {
			t.Errorf("results = %d, want 4", n)
		}
		requireContains(t, page2, []string{
			`<a class="newer" href="/search?q=` + html.EscapeString(enc) + `"`, "Page 2",
			// The chip leads to the first page.
			`<span class="chip-label">room:general</span>`,
		}, []string{`class="older"`})
		// No result of the second page is on the first.
		first := map[string]bool{}
		for _, id := range resultIDs(e.searchAs("alice", q)) {
			first[id] = true
		}
		for _, id := range resultIDs(page2) {
			if first[id] {
				t.Errorf("result %s is on both pages", id)
			}
		}
	})
	t.Run("a page past the end is empty", func(t *testing.T) {
		rec := e.getAs("alice", "/search?q="+enc+"&page=9", false)
		requireBody(t, rec, []string{"Nothing found.", "Page 9", `class="newer"`}, "search-result")
	})
	t.Run("a bad page is the first", func(t *testing.T) {
		for _, p := range []string{"0", "-3", "x", ""} {
			rec := e.getAs("alice", "/search?q="+enc+"&page="+p, false)
			if n := len(resultIDs(rec.Body.String())); n != 20 {
				t.Errorf("page=%q: %d results, want 20", p, n)
			}
		}
	})
	t.Run("datastar keeps the address", func(t *testing.T) {
		rec := e.getAs("alice", "/search?q="+enc+"&page=2&datastar=%7B%7D", true)
		requireSSE(t, rec)
		body := rec.Body.String()
		for _, want := range []string{`id="search-panel"`, `class="newer"`, "page=2"} {
			if !strings.Contains(body, want) {
				t.Errorf("body lacks %q:\n%s", want, body)
			}
		}
		if strings.Contains(body, "datastar=") {
			t.Errorf("the address keeps the datastar parameter:\n%s", body)
		}
		if strings.Contains(body, "<aside") || strings.Contains(body, `id="search-form"`) {
			t.Errorf("a patch has the page around the panel:\n%s", body)
		}
	})
}

func TestSearchDatastarPatchesThePanel(t *testing.T) {
	t.Parallel()
	e := newSearchEnv(t)
	rec := e.getAs("alice", "/search?q=friday+%23design", true)
	requireSSE(t, rec)
	body := rec.Body.String()
	for _, want := range []string{
		`id="search-panel"`, `<mark>Friday</mark>`, `chip-label">#design`,
		`window.history.replaceState({}, &#34;&#34;, &#34;/search?q=friday&#43;%23design&#34;)`,
	} {
		if !strings.Contains(body, want) && !strings.Contains(body, strings.NewReplacer("&#34;", `"`, "&amp;", "&", "&#43;", "+").Replace(want)) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	// An empty query empties the address.
	rec = e.getAs("alice", "/search?q=", true)
	requireSSE(t, rec)
	requireBody(t, rec, []string{`id="search-panel"`, `replaceState({}, "", "/search")`}, "search-result")
}

func TestSearchNeverShowsAPrivateRoomToWhoIsNotAMember(t *testing.T) {
	t.Parallel()
	e := newSearchEnv(t)
	secretComment := e.commentOn(e.zebra, "bob", "zebra comment")
	for _, q := range []string{"zebra", "#design", "room:secret", "from:alice", "is:comment"} {
		for _, user := range []string{"carol", "admin"} {
			t.Run(user+" "+q, func(t *testing.T) {
				body := e.searchAs(user, q)
				for _, p := range []app.Post{e.zebra, secretComment} {
					if strings.Contains(body, `id="result-`+idText(p.ID)+`"`) {
						t.Errorf("%s sees post %d of the private room", user, p.ID)
					}
				}
				requireContains(t, body, nil, []string{"zebra plan", `class="search-room" href="/rooms/` + idText(e.secret.ID) + `"`})
			})
		}
	}
	// A member finds them, and the admin is no exception for the rooms they are in.
	requireResultSet(t, e.searchAs("bob", "zebra"), e.zebra, secretComment)
	requireResultSet(t, e.searchAs("carol", "#design"), e.notes, e.review)
	requireResultSet(t, e.searchAs("admin", "#design"), e.notes, e.review)
}

func TestSearchComments(t *testing.T) {
	t.Parallel()
	e := newSearchEnv(t)
	first := e.commentOn(e.notes, "bob", "the quokka comment")
	second := e.commentOn(first, "alice", "a quokka reply")
	body := e.searchAs("alice", "quokka")
	requireResultSet(t, body, first, second)
	requireContains(t, body, []string{
		`<span class="badge">comment</span>`,
		// A comment links to its place in the thread of its top-level post.
		`<a class="search-open" href="/posts/` + idText(e.notes.ID) + `#post-` + idText(first.ID) + `">`,
		`<a class="search-open" href="/posts/` + idText(e.notes.ID) + `#post-` + idText(second.ID) + `">`,
		"Open the comment", `<mark>quokka</mark> comment`,
	}, []string{"Open the post"})

	// is:post and is:comment narrow the results; a post links to its page.
	requireResultSet(t, e.searchAs("alice", "quokka is:post"))
	requireResultSet(t, e.searchAs("alice", "quokka is:comment"), first, second)
	requireResultSet(t, e.searchAs("alice", "release is:post"), e.notes)
	requireContains(t, e.searchAs("alice", "release is:post"), []string{`href="/posts/` + idText(e.notes.ID) + `">`, "Open the post"}, []string{`class="badge">comment`})

	// The link leads to the thread, which shows the comment.
	rec := e.getAs("alice", "/posts/"+idText(e.notes.ID), false)
	requireBody(t, rec, []string{`id="post-` + idText(first.ID) + `"`})
}

func TestSearchSnippetIsTheServicesHTML(t *testing.T) {
	t.Parallel()
	e := newSearchEnv(t)
	e.writePost("alice", "general", "markup <b>bold</b> & <script>alert(1)</script> gadget")
	body := e.searchAs("alice", "gadget")
	requireContains(t, body, []string{
		// The text is shown as text, once escaped; the match is the only markup.
		"&lt;b&gt;bold&lt;/b&gt; &amp; &lt;script&gt;alert(1)&lt;/script&gt; <mark>gadget</mark>",
	}, []string{"<b>", "<script>alert", "&amp;lt;", "&amp;amp;"})

	// A query with markup is escaped in the box and in the chip.
	body = e.searchAs("alice", `"><script>x</script> #a<b room:"x<y"`)
	requireContains(t, body, nil, []string{"<script>x", "<b ", "x<y"})
}

func TestSearchBoxIsOnTheOtherPages(t *testing.T) {
	t.Parallel()
	e := newSearchEnv(t)
	for _, tt := range []struct{ user, path string }{
		{"alice", "/"}, {"alice", "/rooms"}, {"alice", "/rooms/" + idText(e.general.ID)},
		{"alice", "/posts/" + idText(e.notes.ID)}, {"alice", "/rooms/" + idText(e.general.ID) + "/board"},
		{"admin", "/admin/users"}, {"admin", "/admin/audit"},
	} {
		t.Run(tt.path, func(t *testing.T) {
			rec := e.getAs(tt.user, tt.path, false)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			requireBody(t, rec, []string{
				`<form id="sidebar-search" class="sidebar-search" role="search" method="get" action="/search">`,
				`<input id="sidebar-search-q" name="q" type="search" placeholder="Search"`,
			}, `id="search-form"`)
		})
	}
	// Pages of people who are not logged in have no sidebar and no box.
	rec := e.send(get("/login"))
	requireBody(t, rec, []string{"Log in"}, "sidebar-search")
}

func TestSearchNeedsASession(t *testing.T) {
	t.Parallel()
	e := newSearchEnv(t)
	rec := e.send(get("/search?q=design"))
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if got, want := rec.Header().Get("Location"), "/login?next=%2Fsearch%3Fq%3Ddesign"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
	requireBody(t, rec, nil, "zebra", "Design review")

	// A Datastar request is sent to the login page too.
	rec = e.send(get("/search?q=design", asDatastar()))
	requireSSE(t, rec)
	requireBody(t, rec, []string{"/login"}, "zebra", "Design review")
}

func TestSearchWrongMethod(t *testing.T) {
	t.Parallel()
	e := newSearchEnv(t)
	rec := e.send(postForm("/search", url.Values{"q": {"design"}}, e.cookies["alice"]))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /search: status = %d, want 405", rec.Code)
	}
}
