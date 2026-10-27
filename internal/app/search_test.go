package app_test

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/markdown"
)

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func words(texts ...string) []app.Term {
	out := make([]app.Term, len(texts))
	for i, t := range texts {
		out[i] = app.Term{Text: t}
	}
	return out
}

func TestParseQuery(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want app.Query
	}{
		{"empty", "", app.Query{}},
		{"only white space", " \t\n ", app.Query{}},
		{"one word", "deploy", app.Query{Terms: words("deploy")}},
		{"words keep their case and order", "Deploy the API", app.Query{Terms: words("Deploy", "the", "API")}},
		{"extra white space", "  a \t b\n c  ", app.Query{Terms: words("a", "b", "c")}},
		{"a phrase", `"exact phrase"`, app.Query{Terms: []app.Term{{Text: "exact phrase", Phrase: true}}}},
		{"a phrase between words", `before "the middle" after`,
			app.Query{Terms: []app.Term{{Text: "before"}, {Text: "the middle", Phrase: true}, {Text: "after"}}}},
		{"white space in a phrase is squeezed", "\"a   b\n c\"", app.Query{Terms: []app.Term{{Text: "a b c", Phrase: true}}}},
		{"an empty phrase", `"" word`, app.Query{Terms: words("word")}},
		{"an unbalanced quote is an ordinary character", `"unbalanced quote`, app.Query{Terms: words(`"unbalanced`, "quote")}},
		{"a lone quote", `"`, app.Query{}},
		{"a quote inside a word", `say"hi there`, app.Query{Terms: words(`say"hi`, "there")}},
		{"a quote at the end of a word", `word" other`, app.Query{Terms: words(`word"`, "other")}},
		{"a phrase followed by a word", `"two words"tail`, app.Query{Terms: []app.Term{{Text: "two words", Phrase: true}, {Text: "tail"}}}},

		// FTS5 syntax is plain text.
		{"OR", "a OR b", app.Query{Terms: words("a", "OR", "b")}},
		{"NOT", "a NOT b", app.Query{Terms: words("a", "NOT", "b")}},
		{"NEAR", "NEAR(a b)", app.Query{Terms: words("NEAR(a", "b)")}},
		{"AND", "a AND b", app.Query{Terms: words("a", "AND", "b")}},
		{"a star", "pre* *post", app.Query{Terms: words("pre*", "*post")}},
		{"a lone star", "* word", app.Query{Terms: words("word")}},
		{"a caret", "^start", app.Query{Terms: words("^start")}},
		{"a minus", "-excluded", app.Query{Terms: words("-excluded")}},
		{"a column filter", "text:secret", app.Query{Terms: words("text:secret")}},
		{"parentheses", "(a b) c", app.Query{Terms: words("(a", "b)", "c")}},
		{"a phrase with operators", `"a OR b" NOT`, app.Query{Terms: []app.Term{{Text: "a OR b", Phrase: true}, {Text: "NOT"}}}},
		{"only punctuation is dropped", `- ** () "---" !`, app.Query{}},
		{"the match markers and NUL are dropped", "a\ue000b\ue001c\x00d", app.Query{Terms: words("abcd")}},

		// Tags.
		{"a tag", "#go", app.Query{Tags: []string{"go"}}},
		{"tags are lower-cased and repeatable", "#Go #API-v2 #go", app.Query{Tags: []string{"go", "api-v2"}}},
		{"a tag with several hashes", "##Go", app.Query{Tags: []string{"go"}}},
		{"a tag and words", "fix #bug the build", app.Query{Terms: words("fix", "the", "build"), Tags: []string{"bug"}}},
		{"a hash alone is nothing", "#", app.Query{}},
		{"a hash with a bad tag is a word", "#c++ #a.b", app.Query{Terms: words("#c++", "#a.b")}},
		{"a tag of 51 characters is a word", "#" + strings.Repeat("a", 51), app.Query{Terms: words("#" + strings.Repeat("a", 51))}},
		{"a hash inside a word is a word", "c#", app.Query{Terms: words("c#")}},
		{"a tag in unicode", "#café", app.Query{Tags: []string{"café"}}},

		// Filters.
		{"room", "room:lobby", app.Query{Room: "lobby"}},
		{"room keeps its case", "room:Team-News", app.Query{Room: "Team-News"}},
		{"room in quotes", `room:"team news"`, app.Query{Room: "team news"}},
		{"the key ignores case", "ROOM:lobby From:Bob IS:Post", app.Query{Room: "lobby", From: "bob", PostsOnly: true}},
		{"the last room counts", "room:a room:b", app.Query{Room: "b"}},
		{"from is lower-cased", "from:Alice", app.Query{From: "alice"}},
		{"from may have an @", "from:@alice", app.Query{From: "alice"}},
		{"is post", "is:post", app.Query{PostsOnly: true}},
		{"is comment", "is:comment", app.Query{CommentsOnly: true}},
		{"is post and comment", "is:post is:comment", app.Query{PostsOnly: true, CommentsOnly: true}},
		{"after", "after:2026-10-27", app.Query{After: day(2026, 10, 27)}},
		{"before", "before:2026-01-02", app.Query{Before: day(2026, 1, 2)}},
		{"a range", "after:2026-01-01 before:2026-02-01", app.Query{After: day(2026, 1, 1), Before: day(2026, 2, 1)}},
		{"a leap day", "after:2028-02-29", app.Query{After: day(2028, 2, 29)}},
		{"a date in quotes", `after:"2026-10-27"`, app.Query{After: day(2026, 10, 27)}},
		{"everything together", `fix "the build" #bug #ci room:dev from:ann after:2026-03-01 before:2026-04-01 is:post`,
			app.Query{
				Terms: []app.Term{{Text: "fix"}, {Text: "the build", Phrase: true}}, Tags: []string{"bug", "ci"},
				Room: "dev", From: "ann", After: day(2026, 3, 1), Before: day(2026, 4, 1), PostsOnly: true,
			}},

		// Not filters.
		{"an unknown key is a word", "foo:bar", app.Query{Terms: words("foo:bar")}},
		{"an unknown is: value is a word", "is:resolved", app.Query{Terms: words("is:resolved")}},
		{"a known key with no value is a word", "room: from: is:", app.Query{Terms: words("room:", "from:", "is:")}},
		{"an empty value in quotes is a word", `room:""`, app.Query{Terms: words(`room:""`)}},
		{"a key in the middle of a word is a word", "xroom:lobby", app.Query{Terms: words("xroom:lobby")}},
		{"a lone @ in from is a word", "from:@", app.Query{Terms: words("from:@")}},
		{"a key after a hash is a tag or a word", "#room:lobby", app.Query{Terms: words("#room:lobby")}},
		{"a time after the key is a word", "http://example.com", app.Query{Terms: words("http://example.com")}},

		// Limits.
		{"at most 32 terms count", strings.Repeat("w ", 40), app.Query{Terms: slices.Repeat(words("w"), 32)}},
		{"at most 500 characters count", strings.Repeat("a", 499) + " tail", app.Query{Terms: words(strings.Repeat("a", 499))}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := app.ParseQuery(tt.in)
			if err != nil {
				t.Fatalf("ParseQuery(%q) = %+v", tt.in, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseQuery(%q) =\n%+v\nwant\n%+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseQueryEmptiness(t *testing.T) {
	tests := []struct {
		in         string
		words      bool
		wantsEmpty bool
	}{
		{"", false, true},
		{"   ", false, true},
		{"---", false, true},
		{"word", true, false},
		{"#tag", false, false},
		{"room:x", false, false},
		{"from:x", false, false},
		{"after:2026-01-01", false, false},
		{"before:2026-01-01", false, false},
		{"is:post", false, false},
		{"is:comment", false, false},
	}
	for _, tt := range tests {
		q, err := app.ParseQuery(tt.in)
		if err != nil {
			t.Fatalf("ParseQuery(%q) = %+v", tt.in, err)
		}
		if q.HasWords() != tt.words || q.IsEmpty() != tt.wantsEmpty {
			t.Errorf("ParseQuery(%q): HasWords %v IsEmpty %v, want %v %v", tt.in, q.HasWords(), q.IsEmpty(), tt.words, tt.wantsEmpty)
		}
	}
}

func TestParseQueryBadDates(t *testing.T) {
	for _, in := range []string{
		"after:yesterday", "before:tomorrow", "after:2026-13-01", "after:2026-02-30", "after:2026-1-5", "after:26-10-27",
		"before:2026/10/27", "after:2026-10-27T10:00:00Z", "after:2026-10-27x", `before:"not a date"`,
		"fine words after:2026-99-99 more", "AFTER:nope",
	} {
		t.Run(in, func(t *testing.T) {
			_, err := app.ParseQuery(in)
			appErr := requireTag(t, err, app.TagInvalidInput, "ParseQuery.InvalidInput")
			fields := fieldsOf(t, appErr)
			if want := (map[string]string{"q": "Use dates like 2026-10-27."}); !reflect.DeepEqual(fields, want) {
				t.Errorf("fields = %v, want %v", fields, want)
			}
		})
	}
}

// searchEnv is the board fixture (lobby visible with admin and bob, den
// private with alice and bob; root, carol and dave members of nothing) with
// the search service.
type searchEnv struct {
	*boardEnv
	search *app.Search
}

func newSearchEnv(t *testing.T) *searchEnv {
	t.Helper()
	e := &searchEnv{boardEnv: newBoardEnv(t)}
	e.search = app.NewSearch(e.st, markdown.New())
	return e
}

// find runs a search as the user and fails the test on an error.
func (e *searchEnv) find(user, q string, page int) app.SearchPage {
	e.t.Helper()
	res, err := e.search.Query(e.t.Context(), e.as(user), q, page)
	if err != nil {
		e.t.Fatalf("Query(%q) = %+v", q, err)
	}
	return res
}

// found returns the IDs of the results of a search, in order.
func (e *searchEnv) found(user, q string) []uint64 {
	e.t.Helper()
	ids := []uint64{}
	for _, r := range e.find(user, q, 1).Results {
		ids = append(ids, r.PostID)
	}
	return ids
}

// index returns the full-text index: the text of each post by ID.
func (e *searchEnv) index() map[uint64]string {
	e.t.Helper()
	rows, err := e.st.IndexRows(e.t.Context())
	if err != nil {
		e.t.Fatalf("IndexRows: %+v", err)
	}
	return rows
}

// requireIndex fails the test unless the index holds exactly the texts.
func (e *searchEnv) requireIndex(want map[uint64]string) {
	e.t.Helper()
	if got := e.index(); !reflect.DeepEqual(got, want) {
		e.t.Errorf("index = %v, want %v", got, want)
	}
}

func TestSearchQueryReadableRooms(t *testing.T) {
	e := newSearchEnv(t)
	inLobby := e.write("bob", "lobby", "launch plan")
	inDen := e.write("bob", "den", "secret plan")
	comment := e.say("alice", inDen.ID, "another plan in the den")
	tests := []struct {
		actor string
		want  []uint64
	}{
		{"admin", []uint64{inLobby.ID}}, // an admin who is not a member of the den
		{"root", []uint64{inLobby.ID}},  // an admin who is a member of nothing
		{"alice", []uint64{inLobby.ID, inDen.ID, comment.ID}},
		{"bob", []uint64{inLobby.ID, inDen.ID, comment.ID}},
		{"carol", []uint64{inLobby.ID}},
		{"dave", []uint64{inLobby.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.actor, func(t *testing.T) {
			got := e.found(tt.actor, "plan")
			slices.Sort(got)
			if want := slices.Sorted(slices.Values(tt.want)); !reflect.DeepEqual(got, want) {
				t.Errorf("found %v, want %v", got, want)
			}
			// Filters do not open the room either.
			wantDen := []uint64{}
			if slices.Contains(tt.want, inDen.ID) {
				wantDen = []uint64{comment.ID, inDen.ID}
			}
			if got := e.found(tt.actor, "room:den"); !reflect.DeepEqual(got, wantDen) {
				t.Errorf("room:den found %v, want %v", got, wantDen)
			}
			if got := e.found(tt.actor, "from:bob is:post"); slices.Contains(got, inDen.ID) != slices.Contains(tt.want, inDen.ID) {
				t.Errorf("from:bob is:post found %v", got)
			}
		})
	}

	t.Run("a member who leaves stops finding the room", func(t *testing.T) {
		if err := e.rooms.Leave(t.Context(), e.as("bob"), e.den.ID); err != nil {
			t.Fatal(err)
		}
		if got := e.found("bob", "plan"); !reflect.DeepEqual(got, []uint64{inLobby.ID}) {
			t.Errorf("found %v, want only the lobby post", got)
		}
		if err := e.rooms.AddMember(t.Context(), e.as("alice"), e.den.ID, e.uid("carol")); err != nil {
			t.Fatal(err)
		}
		if got := e.found("carol", "secret"); !reflect.DeepEqual(got, []uint64{inDen.ID}) {
			t.Errorf("a new member found %v, want the den post", got)
		}
	})
}

func TestSearchQueryFilters(t *testing.T) {
	e := newSearchEnv(t)
	e.clk.Set(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	old := e.write("admin", "lobby", "Quarterly report ready", "finance", "report")
	e.clk.Set(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	first := e.write("bob", "lobby", "Report on the outage", "ops", "report")
	e.clk.Set(time.Date(2026, 10, 1, 23, 59, 59, 0, time.UTC))
	lastOfDay := e.say("admin", first.ID, "Outage report is wrong", "ops")
	e.clk.Set(time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC))
	den := e.write("alice", "den", "Numbers from the den", "den")
	e.clk.Set(time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC))
	late := e.write("bob", "lobby", "Reporting is done", "ops", "report", "finance")
	e.clk.Set(time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC))
	task := e.withTasks("bob", e.lobby.ID, app.TaskInput{Title: "Fix the rendering bug"})

	tests := []struct {
		name string
		q    string
		want []uint64
	}{
		{"a prefix", "report", []uint64{late.ID, lastOfDay.ID, first.ID, old.ID}},
		{"a longer prefix", "reporti", []uint64{late.ID}},
		{"a task title", "render", []uint64{task.ID}},
		{"a word the posts do not have", "nothing", []uint64{}},
		{"the room, whatever the case", "report room:LOBBY", []uint64{late.ID, lastOfDay.ID, first.ID, old.ID}},
		{"the room's posts without words", "room:lobby", []uint64{task.ID, late.ID, lastOfDay.ID, first.ID, old.ID}},
		{"a private room the reader is in", "room:den numbers", []uint64{den.ID}},
		{"a room that does not exist", "room:nowhere report", []uint64{}},
		{"the author", "from:bob", []uint64{task.ID, late.ID, first.ID}},
		{"the author with an @ and capitals", "from:@BOB report", []uint64{late.ID, first.ID}},
		{"an author with nothing", "from:carol", []uint64{}},
		{"posts only", "report is:post", []uint64{late.ID, first.ID, old.ID}},
		{"comments only", "report is:comment", []uint64{lastOfDay.ID}},
		{"posts and comments", "report is:post is:comment", []uint64{}},
		{"one tag", "#ops", []uint64{late.ID, lastOfDay.ID, first.ID}},
		{"two tags, both required", "#ops #finance", []uint64{late.ID}},
		{"a tag in capitals", "#OPS #Report", []uint64{late.ID, first.ID}},
		{"a tag and a word", "outage #ops", []uint64{lastOfDay.ID, first.ID}},
		{"a tag nobody used", "#nothing", []uint64{}},
		{"a tag and a room", "#report room:lobby", []uint64{late.ID, first.ID, old.ID}},
		{"a tag in the private room", "#den", []uint64{den.ID}},
		{"after is on or after the day", "report after:2026-10-01", []uint64{late.ID, lastOfDay.ID, first.ID}},
		{"after a day later", "report after:2026-10-02", []uint64{late.ID}},
		{"before is strictly before the day", "report before:2026-10-01", []uint64{old.ID}},
		{"before the day after", "report before:2026-10-02", []uint64{lastOfDay.ID, first.ID, old.ID}},
		{"a range of one day", "after:2026-10-01 before:2026-10-02", []uint64{lastOfDay.ID, first.ID}},
		{"an empty range", "after:2026-10-02 before:2026-10-01", []uint64{}},
		{"dates alone, newest first", "after:2026-10-20", []uint64{task.ID, late.ID}},
		{"a day alone", "after:2026-10-02 before:2026-10-03", []uint64{den.ID}},
		{"all of it", `"outage report" #ops room:lobby from:admin is:comment after:2026-10-01 before:2026-10-02`, []uint64{lastOfDay.ID}},
		{"an unknown key is searched as words", "is:resolved", []uint64{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// alice reads the lobby and the den.
			got := e.found("alice", tt.q)
			want := tt.want
			if q, _ := app.ParseQuery(tt.q); q.HasWords() {
				// The order of words is by relevance, which this table does not
				// pin down; TestSearchQueryOrder does.
				got, want = slices.Clone(got), slices.Clone(want)
				slices.Sort(got)
				slices.Sort(want)
			}
			if !slices.Equal(got, want) {
				t.Errorf("Query(%q) found %v, want %v", tt.q, got, tt.want)
			}
		})
	}
}

func TestSearchQueryResultFields(t *testing.T) {
	e := newSearchEnv(t)
	post := e.write("bob", "lobby", "A post with tags", "alpha", "beta")
	comment := e.say("admin", post.ID, "A comment under it")
	deep := e.say("bob", comment.ID, "A reply to the comment")

	res := e.find("bob", "room:lobby", 1)
	if res.Query != "room:lobby" || res.Page != 1 || res.HasNext || res.Parsed.HasWords() || res.Parsed.Room != "lobby" {
		t.Errorf("page = %+v", res)
	}
	if len(res.Results) != 3 {
		t.Fatalf("results = %+v", res.Results)
	}
	byID := map[uint64]app.SearchResult{}
	for _, r := range res.Results {
		byID[r.PostID] = r
	}
	p := byID[post.ID]
	if p.IsComment || p.RootID != 0 || p.RoomID != e.lobby.ID || p.RoomName != "lobby" ||
		p.Author != (app.UserRef{ID: e.uid("bob"), Username: "bob", DisplayName: "Bob"}) ||
		!p.CreatedAt.Equal(post.CreatedAt) || !reflect.DeepEqual(p.Tags, []string{"alpha", "beta"}) || string(p.Snippet) != "A post with tags" {
		t.Errorf("post result = %+v", p)
	}
	c := byID[comment.ID]
	if !c.IsComment || c.RootID != post.ID || c.Author.Username != "admin" || len(c.Tags) != 0 || c.Tags == nil {
		t.Errorf("comment result = %+v", c)
	}
	if d := byID[deep.ID]; !d.IsComment || d.RootID != post.ID {
		t.Errorf("reply result = %+v, want the top-level post as its root", d)
	}
}

func TestSearchQueryEmpty(t *testing.T) {
	e := newSearchEnv(t)
	e.write("bob", "lobby", "something")
	for _, q := range []string{"", "   ", "---", "* ()", `""`, "\ue000"} {
		t.Run(fmt.Sprintf("%q", q), func(t *testing.T) {
			res := e.find("bob", q, 1)
			if res.Results == nil || len(res.Results) != 0 || res.HasNext || res.Page != 1 || res.Query != q {
				t.Errorf("page = %+v, want an empty page", res)
			}
		})
	}
}

func TestSearchQueryBadDate(t *testing.T) {
	e := newSearchEnv(t)
	_, err := e.search.Query(t.Context(), e.as("bob"), "report after:soon", 1)
	appErr := requireTag(t, err, app.TagInvalidInput, "Search.Query->ParseQuery.InvalidInput")
	if got := fieldsOf(t, appErr); got["q"] != "Use dates like 2026-10-27." {
		t.Errorf("fields = %v", got)
	}
}

func TestSearchQueryPages(t *testing.T) {
	tests := []struct {
		total int
		pages []int // the results on page 1, 2, 3, ...
	}{
		{0, []int{0}},
		{1, []int{1}},
		{20, []int{20}},
		{21, []int{20, 1}},
		{40, []int{20, 20}},
		{41, []int{20, 20, 1}},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.total), func(t *testing.T) {
			e := newSearchEnv(t)
			var ids []uint64
			for i := range tt.total {
				ids = append(ids, e.write("bob", "lobby", fmt.Sprintf("paged item %d", i)).ID)
			}
			slices.Reverse(ids) // newest first: equal matches are in that order
			var got []uint64
			for i, n := range tt.pages {
				page := e.find("bob", "paged", i+1)
				if len(page.Results) != n || page.Page != i+1 || page.HasNext != (i < len(tt.pages)-1) {
					t.Fatalf("page %d: %d results, HasNext %v, Page %d; want %d results, HasNext %v",
						i+1, len(page.Results), page.HasNext, page.Page, n, i < len(tt.pages)-1)
				}
				for _, r := range page.Results {
					got = append(got, r.PostID)
				}
			}
			if !slices.Equal(got, ids) && tt.total > 0 {
				t.Errorf("results across pages = %v, want %v", got, ids)
			}
			if beyond := e.find("bob", "paged", len(tt.pages)+1); len(beyond.Results) != 0 || beyond.HasNext {
				t.Errorf("past the last page: %+v", beyond)
			}
			// The same for a search without words.
			if tt.total > 0 {
				if page := e.find("bob", "room:lobby", 1); len(page.Results) != tt.pages[0] || page.HasNext != (len(tt.pages) > 1) {
					t.Errorf("room:lobby page 1 = %d results, HasNext %v", len(page.Results), page.HasNext)
				}
			}
		})
	}

	t.Run("a page below 1 is the first", func(t *testing.T) {
		e := newSearchEnv(t)
		e.write("bob", "lobby", "one post")
		for _, p := range []int{0, -3} {
			if res := e.find("bob", "post", p); res.Page != 1 || len(res.Results) != 1 {
				t.Errorf("page %d = %+v", p, res)
			}
		}
	})
}

func TestSearchQueryOrder(t *testing.T) {
	e := newSearchEnv(t)
	long := e.write("bob", "lobby", "zebra "+strings.Repeat("filler words ", 30))
	short := e.write("bob", "lobby", "zebra")
	same := e.write("bob", "lobby", "zebra")
	none := e.write("bob", "lobby", "horse")
	if got, want := e.found("bob", "zebra"), []uint64{same.ID, short.ID, long.ID}; !slices.Equal(got, want) {
		t.Errorf("by relevance, then newest: %v, want %v", got, want)
	}
	if got, want := e.found("bob", "from:bob"), []uint64{none.ID, same.ID, short.ID, long.ID}; !slices.Equal(got, want) {
		t.Errorf("without words, newest first: %v, want %v", got, want)
	}
}

func TestSearchQueryTextAndSnippets(t *testing.T) {
	e := newSearchEnv(t)
	up := e.uploadText("bob", "Budget-2026.xlsx")
	cafe := e.write("bob", "lobby", "Lunch at the Café Münster")
	script := e.write("bob", "lobby", "Hello <script>alert('x')</script> & \"quotes\" world")
	fake := e.write("bob", "lobby", "see \ue000fake\ue001 marker, then hello there")
	withFile := e.writeWith("bob", "lobby", "", up)
	long := e.write("bob", "lobby", "<b>"+strings.Repeat("long ", 80)+"</b>")

	t.Run("diacritics are folded", func(t *testing.T) {
		for _, q := range []string{"cafe", "CAFÉ", "Café", "munster", "münster", `"cafe munster"`} {
			if got := e.found("bob", q); !slices.Equal(got, []uint64{cafe.ID}) {
				t.Errorf("%q found %v, want [%d]", q, got, cafe.ID)
			}
		}
	})

	t.Run("the highlight", func(t *testing.T) {
		tests := []struct {
			q    string
			id   uint64
			want string
		}{
			{"cafe", cafe.ID, "Lunch at the <mark>Café</mark> Münster"},
			{"cafe munster", cafe.ID, "Lunch at the <mark>Café</mark> <mark>Münster</mark>"},
			{"lun", cafe.ID, "<mark>Lunch</mark> at the Café Münster"},
			{`"at the"`, cafe.ID, "Lunch <mark>at the</mark> Café Münster"},
		}
		for _, tt := range tests {
			res := e.find("bob", tt.q, 1)
			if len(res.Results) != 1 || res.Results[0].PostID != tt.id || string(res.Results[0].Snippet) != tt.want {
				t.Errorf("%q: %+v, want snippet %q", tt.q, res.Results, tt.want)
			}
		}
	})

	t.Run("user text cannot make markup", func(t *testing.T) {
		res := e.find("bob", "world", 1)
		want := "Hello &lt;script&gt;alert(&#39;x&#39;)&lt;/script&gt; &amp; &#34;quotes&#34; <mark>world</mark>"
		if len(res.Results) != 1 || res.Results[0].PostID != script.ID || string(res.Results[0].Snippet) != want {
			t.Errorf("snippet = %q, want %q", res.Results[0].Snippet, want)
		}
		// A match inside the markup is escaped like the rest.
		res = e.find("bob", "script", 1)
		if len(res.Results) != 1 || !strings.Contains(string(res.Results[0].Snippet), "&lt;<mark>script</mark>&gt;") {
			t.Errorf("snippet = %q", res.Results[0].Snippet)
		}
	})

	t.Run("the markers in a body are not marks", func(t *testing.T) {
		res := e.find("bob", "hello", 1)
		var got string
		for _, r := range res.Results {
			if r.PostID == fake.ID {
				got = string(r.Snippet)
			}
		}
		if want := "see fake marker, then <mark>hello</mark> there"; got != want {
			t.Errorf("snippet = %q, want %q", got, want)
		}
		if fakeMatch := e.found("bob", "fake"); !slices.Equal(fakeMatch, []uint64{fake.ID}) {
			t.Errorf("fake found %v", fakeMatch)
		}
		for _, r := range e.find("bob", "room:lobby", 1).Results {
			if strings.ContainsAny(string(r.Snippet), "\ue000\ue001") {
				t.Errorf("a snippet holds a marker: %q", r.Snippet)
			}
		}
	})

	t.Run("attachment names are searched", func(t *testing.T) {
		for _, q := range []string{"budget", "xlsx", "Budget-2026.xlsx", `"budget 2026"`} {
			res := e.find("bob", q, 1)
			if len(res.Results) != 1 || res.Results[0].PostID != withFile.ID {
				t.Errorf("%q found %+v", q, res.Results)
			}
		}
		if got := e.find("bob", "xlsx", 1).Results[0].Snippet; !strings.Contains(string(got), "<mark>xlsx</mark>") {
			t.Errorf("snippet = %q", got)
		}
	})

	t.Run("without words the snippet is the start of the text", func(t *testing.T) {
		res := e.find("bob", "room:lobby", 1)
		var got string
		for _, r := range res.Results {
			if r.PostID == long.ID {
				got = string(r.Snippet)
			}
		}
		want := "&lt;b&gt;" + strings.Repeat("long ", 80)[:200-3] + "…"
		if got != want {
			t.Errorf("snippet = %q, want %q", got, want)
		}
		for _, r := range res.Results {
			if r.PostID == script.ID && string(r.Snippet) != "Hello &lt;script&gt;alert(&#39;x&#39;)&lt;/script&gt; &amp; &#34;quotes&#34; world" {
				t.Errorf("short snippet = %q", r.Snippet)
			}
		}
	})
}

func TestIndexCreate(t *testing.T) {
	e := newSearchEnv(t)
	e.requireIndex(map[uint64]string{})
	up := e.uploadText("bob", "plan.pdf")
	p, err := e.posts.Create(t.Context(), e.as("bob"), e.lobby.ID, app.PostInput{
		Format: app.FormatText, Body: "Release notes", Tags: []string{"notes"}, Uploads: []app.Upload{up},
		Tasks: []app.TaskInput{{Title: "Write changelog"}, {Title: "Tag the release"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.posts.Reply(t.Context(), e.as("alice"), p.ID, app.PostInput{
		Format: app.FormatMarkdown, Body: "**Looks** good", Tasks: []app.TaskInput{{Title: "Review it"}},
	})
	_ = requireTag(t, err, app.TagForbidden, "Posts.Reply")
	if len(e.index()) != 1 {
		t.Errorf("a failed reply changed the index: %v", e.index())
	}
	c, err := e.posts.Reply(t.Context(), e.as("admin"), p.ID, app.PostInput{
		Format: app.FormatMarkdown, Body: "**Looks** good", Tasks: []app.TaskInput{{Title: "Review it"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.requireIndex(map[uint64]string{
		p.ID: "Release notes\nplan.pdf\nWrite changelog\nTag the release",
		c.ID: "**Looks** good\nReview it",
	})

	// A failed create (a mistake in the input) leaves no row.
	_, err = e.posts.Create(t.Context(), e.as("bob"), e.lobby.ID, app.PostInput{Format: app.FormatText, Body: ""})
	_ = requireTag(t, err, app.TagInvalidInput, "Posts.Create")
	if len(e.index()) != 2 {
		t.Errorf("a failed create changed the index: %v", e.index())
	}
}

func TestIndexEdit(t *testing.T) {
	e := newSearchEnv(t)
	a := e.uploadText("bob", "first.txt")
	b := e.uploadText("bob", "second.txt")
	p := e.writeWith("bob", "lobby", "Original body", a)
	created, err := e.posts.Edit(t.Context(), e.as("bob"), p.ID, app.PostInput{
		Format: app.FormatText, Body: "Original body", KeepAttachments: attachmentIDs(p),
		Tasks: []app.TaskInput{{Title: "Task one"}, {Title: "Task two"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.requireIndex(map[uint64]string{p.ID: "Original body\nfirst.txt\nTask one\nTask two"})

	steps := []struct {
		name string
		in   func(cur app.Post) app.PostInput
		want string
	}{
		{"the body", func(cur app.Post) app.PostInput {
			return app.PostInput{Format: app.FormatText, Body: "Rewritten body", KeepAttachments: attachmentIDs(cur), Tasks: taskInputs(cur)}
		}, "Rewritten body\nfirst.txt\nTask one\nTask two"},
		{"the format only", func(cur app.Post) app.PostInput {
			return app.PostInput{Format: app.FormatMarkdown, Body: "Rewritten body", KeepAttachments: attachmentIDs(cur), Tasks: taskInputs(cur)}
		}, "Rewritten body\nfirst.txt\nTask one\nTask two"},
		{"an attachment added", func(cur app.Post) app.PostInput {
			return app.PostInput{Format: app.FormatText, Body: "Rewritten body", KeepAttachments: attachmentIDs(cur), Uploads: []app.Upload{b}, Tasks: taskInputs(cur)}
		}, "Rewritten body\nfirst.txt\nsecond.txt\nTask one\nTask two"},
		{"an attachment removed", func(cur app.Post) app.PostInput {
			return app.PostInput{Format: app.FormatText, Body: "Rewritten body", KeepAttachments: attachmentIDs(cur)[1:], Tasks: taskInputs(cur)}
		}, "Rewritten body\nsecond.txt\nTask one\nTask two"},
		{"a task renamed", func(cur app.Post) app.PostInput {
			ts := taskInputs(cur)
			ts[0].Title = "Task uno"
			return app.PostInput{Format: app.FormatText, Body: "Rewritten body", KeepAttachments: attachmentIDs(cur), Tasks: ts}
		}, "Rewritten body\nsecond.txt\nTask uno\nTask two"},
		{"a task removed and one added", func(cur app.Post) app.PostInput {
			ts := taskInputs(cur)
			return app.PostInput{Format: app.FormatText, Body: "Rewritten body", KeepAttachments: attachmentIDs(cur), Tasks: append(ts[1:], app.TaskInput{Title: "Task three"})}
		}, "Rewritten body\nsecond.txt\nTask two\nTask three"},
		{"everything removed but the body", func(cur app.Post) app.PostInput {
			return app.PostInput{Format: app.FormatText, Body: "Only the body"}
		}, "Only the body"},
		{"an empty body with an attachment", func(cur app.Post) app.PostInput {
			return app.PostInput{Format: app.FormatText, Uploads: []app.Upload{e.uploadText("bob", "third.txt")}}
		}, "third.txt"},
	}
	cur := created
	for _, s := range steps {
		cur, err = e.posts.Edit(t.Context(), e.as("bob"), p.ID, s.in(cur))
		if err != nil {
			t.Fatalf("%s: %+v", s.name, err)
		}
		e.requireIndex(map[uint64]string{p.ID: s.want})
	}

	t.Run("the old text is gone from the matches", func(t *testing.T) {
		if got := e.found("bob", "original"); len(got) != 0 {
			t.Errorf("found %v", got)
		}
		if got := e.found("bob", "third"); !slices.Equal(got, []uint64{p.ID}) {
			t.Errorf("found %v", got)
		}
	})

	t.Run("a failed edit leaves the index alone", func(t *testing.T) {
		_, err := e.posts.Edit(t.Context(), e.as("bob"), p.ID, app.PostInput{Format: app.FormatText, Body: strings.Repeat("x", 20_001)})
		_ = requireTag(t, err, app.TagInvalidInput, "Posts.Edit")
		_, err = e.posts.Edit(t.Context(), e.as("alice"), p.ID, app.PostInput{Format: app.FormatText, Body: "hijack"})
		_ = requireTag(t, err, app.TagForbidden, "Posts.Edit")
		e.requireIndex(map[uint64]string{p.ID: "third.txt"})
	})

	t.Run("a comment is edited like a post", func(t *testing.T) {
		c := e.say("admin", p.ID, "first thoughts")
		if _, err := e.posts.Edit(t.Context(), e.as("admin"), c.ID, app.PostInput{Format: app.FormatText, Body: "second thoughts"}); err != nil {
			t.Fatal(err)
		}
		e.requireIndex(map[uint64]string{p.ID: "third.txt", c.ID: "second thoughts"})
	})
}

// taskInputs makes the editor's task rows of a post as it is.
func taskInputs(p app.Post) []app.TaskInput {
	out := []app.TaskInput{}
	for _, t := range p.Tasks {
		out = append(out, app.TaskInput{ID: t.ID, Title: t.Title})
	}
	return out
}

func TestIndexBoardTaskTitle(t *testing.T) {
	e := newSearchEnv(t)
	p := e.withTasks("bob", e.lobby.ID, app.TaskInput{Title: "Old title"}, app.TaskInput{Title: "Keeper"})
	e.requireIndex(map[uint64]string{p.ID: "a post\nOld title\nKeeper"})

	title := "New title"
	if err := e.board.UpdateTask(t.Context(), e.as("bob"), p.Tasks[0].ID, app.TaskPatch{Title: &title}); err != nil {
		t.Fatal(err)
	}
	e.requireIndex(map[uint64]string{p.ID: "a post\nNew title\nKeeper"})
	if got := e.found("bob", "old"); len(got) != 0 {
		t.Errorf("the old title still matches: %v", got)
	}
	if got := e.found("bob", "new"); !slices.Equal(got, []uint64{p.ID}) {
		t.Errorf("the new title does not match: %v", got)
	}

	// A refused or empty patch changes nothing.
	empty := " "
	err := e.board.UpdateTask(t.Context(), e.as("bob"), p.Tasks[0].ID, app.TaskPatch{Title: &empty})
	_ = requireTag(t, err, app.TagInvalidInput, "Board.UpdateTask")
	err = e.board.UpdateTask(t.Context(), e.as("carol"), p.Tasks[0].ID, app.TaskPatch{Title: &title})
	_ = requireTag(t, err, app.TagForbidden, "Board.UpdateTask")
	e.requireIndex(map[uint64]string{p.ID: "a post\nNew title\nKeeper"})

	// A task of a comment belongs to the comment.
	c, err := e.posts.Reply(t.Context(), e.as("bob"), p.ID, app.PostInput{Format: app.FormatText, Body: "c", Tasks: []app.TaskInput{{Title: "Comment task"}}})
	if err != nil {
		t.Fatal(err)
	}
	renamed := "Renamed comment task"
	if err := e.board.UpdateTask(t.Context(), e.as("bob"), c.Tasks[0].ID, app.TaskPatch{Title: &renamed}); err != nil {
		t.Fatal(err)
	}
	e.requireIndex(map[uint64]string{p.ID: "a post\nNew title\nKeeper", c.ID: "c\nRenamed comment task"})
}

func TestIndexDelete(t *testing.T) {
	t.Run("a post without replies goes", func(t *testing.T) {
		e := newSearchEnv(t)
		keep := e.write("bob", "lobby", "keeper")
		p := e.withTasks("bob", e.lobby.ID, app.TaskInput{Title: "doomed task"})
		if err := e.posts.Delete(t.Context(), e.as("bob"), p.ID); err != nil {
			t.Fatal(err)
		}
		e.requireIndex(map[uint64]string{keep.ID: "keeper"})
	})

	t.Run("a post with replies stays as a placeholder out of the index", func(t *testing.T) {
		e := newSearchEnv(t)
		up := e.uploadText("bob", "gone.txt")
		p, err := e.posts.Create(t.Context(), e.as("bob"), e.lobby.ID, app.PostInput{
			Format: app.FormatText, Body: "will be a placeholder", Uploads: []app.Upload{up}, Tasks: []app.TaskInput{{Title: "its task"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		c := e.say("admin", p.ID, "the reply stays")
		e.requireIndex(map[uint64]string{p.ID: "will be a placeholder\ngone.txt\nits task", c.ID: "the reply stays"})
		if err := e.posts.Delete(t.Context(), e.as("admin"), p.ID); err != nil {
			t.Fatal(err)
		}
		if !e.rowExists(p.ID) {
			t.Fatal("the placeholder is gone")
		}
		e.requireIndex(map[uint64]string{c.ID: "the reply stays"})
		if got := e.found("bob", "placeholder"); len(got) != 0 {
			t.Errorf("the placeholder is found: %v", got)
		}
	})

	t.Run("a clean-up chain takes the placeholders out", func(t *testing.T) {
		e := newSearchEnv(t)
		other := e.write("bob", "lobby", "bystander")
		root := e.write("bob", "lobby", "root post")
		c1 := e.say("admin", root.ID, "comment one")
		c2 := e.say("bob", c1.ID, "comment two")
		c3 := e.say("admin", c2.ID, "comment three")
		for _, id := range []uint64{root.ID, c1.ID, c2.ID} {
			if err := e.posts.Delete(t.Context(), e.as("admin"), id); err != nil {
				t.Fatalf("Delete %d: %+v", id, err)
			}
		}
		e.requireIndex(map[uint64]string{other.ID: "bystander", c3.ID: "comment three"})
		// The last reply goes and takes the three placeholders with it.
		if err := e.posts.Delete(t.Context(), e.as("admin"), c3.ID); err != nil {
			t.Fatal(err)
		}
		for _, id := range []uint64{root.ID, c1.ID, c2.ID, c3.ID} {
			if e.rowExists(id) {
				t.Errorf("post %d still exists", id)
			}
		}
		e.requireIndex(map[uint64]string{other.ID: "bystander"})
	})

	t.Run("a refused delete leaves the index alone", func(t *testing.T) {
		e := newSearchEnv(t)
		p := e.write("bob", "lobby", "mine")
		err := e.posts.Delete(t.Context(), e.as("carol"), p.ID)
		if err == nil {
			t.Fatal("carol deleted a post of the lobby she is not a member of")
		}
		e.requireIndex(map[uint64]string{p.ID: "mine"})
	})
}

func TestIndexRoomDelete(t *testing.T) {
	e := newSearchEnv(t)
	inLobby := e.write("bob", "lobby", "in the lobby")
	lobbyReply := e.say("admin", inLobby.ID, "lobby reply")
	placeholder := e.write("bob", "lobby", "placeholder")
	e.say("admin", placeholder.ID, "keeps it")
	if err := e.posts.Delete(t.Context(), e.as("bob"), placeholder.ID); err != nil {
		t.Fatal(err)
	}
	inDen := e.withTasks("alice", e.den.ID, app.TaskInput{Title: "den task"})
	if len(e.index()) != 4 {
		t.Fatalf("index = %v", e.index())
	}

	err := e.rooms.Delete(t.Context(), e.as("carol"), e.lobby.ID)
	_ = requireTag(t, err, app.TagForbidden, "Rooms.Delete")
	if len(e.index()) != 4 {
		t.Errorf("a refused delete changed the index: %v", e.index())
	}

	if err := e.rooms.Delete(t.Context(), e.as("admin"), e.lobby.ID); err != nil {
		t.Fatal(err)
	}
	e.requireIndex(map[uint64]string{inDen.ID: "a post\nden task"})
	for _, id := range []uint64{inLobby.ID, lobbyReply.ID} {
		if e.rowExists(id) {
			t.Errorf("post %d survived its room", id)
		}
	}
	if err := e.rooms.Delete(t.Context(), e.as("alice"), e.den.ID); err != nil {
		t.Fatal(err)
	}
	e.requireIndex(map[uint64]string{})
}

func TestNewSearch(t *testing.T) {
	e := newSearchEnv(t)
	if e.search == nil {
		t.Fatal("NewSearch returned nil")
	}
}

// TestSearchQueryInjection sends FTS5 syntax through the whole service: it
// is words, so it neither fails nor finds what the words do not match.
func TestSearchQueryInjection(t *testing.T) {
	e := newSearchEnv(t)
	lobby := e.write("bob", "lobby", "alpha beta")
	e.write("alice", "den", "gamma secret")
	for _, q := range []string{
		"alpha OR gamma", "alpha OR secret", "NOT alpha", "alpha NOT beta", "NEAR(alpha beta)", "NEAR(alpha beta, 5)",
		"alpha*", "* OR *", "^alpha", "-alpha", "alpha:beta", "text:gamma", "{text}:gamma", "(alpha", "alpha)", "((((", "))))",
		`"alpha`, `alpha"`, `"alpha" OR "gamma"`, `alpha" OR "gamma`, `"alpha""`, `""""`, `"`, `'`, `alpha' OR '1'='1`,
		"alpha; DROP TABLE posts_fts;--", "alpha\x00gamma", "a\ue000b", "%", "_", `\`, "alpha + gamma", "alpha AND gamma",
		strings.Repeat("(", 300), strings.Repeat("alpha OR ", 100), strings.Repeat(`"`, 301),
	} {
		t.Run(fmt.Sprintf("%.20q", q), func(t *testing.T) {
			res, err := e.search.Query(t.Context(), e.as("carol"), q, 1)
			if err != nil {
				t.Fatalf("Query(%q) = %+v", q, err)
			}
			for _, r := range res.Results {
				if r.PostID != lobby.ID {
					t.Errorf("Query(%q) found post %d, which carol may not read or the words do not match", q, r.PostID)
				}
			}
		})
	}
	// The index survived all of it.
	if got := e.found("bob", "alpha"); !slices.Equal(got, []uint64{lobby.ID}) {
		t.Errorf("found %v after the injections", got)
	}
}
