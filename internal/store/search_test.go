package store

import (
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/errors"
)

// ftsIDs returns the IDs the index matches for the FTS5 expression, sorted.
func (f *fix) ftsIDs(match string) []uint64 {
	f.tb.Helper()
	var ids []uint64
	if err := f.s.db(f.ctx).Raw("SELECT rowid FROM posts_fts WHERE posts_fts MATCH ?", match).Scan(&ids).Error; err != nil {
		f.tb.Fatalf("MATCH %s: %v", match, err)
	}
	slices.Sort(ids)
	return ids
}

// reindex has the store reindex the posts and fails the test on an error.
func (f *fix) reindex(posts ...*Post) {
	f.tb.Helper()
	for _, p := range posts {
		if err := f.s.ReindexPost(f.ctx, p.ID); err != nil {
			f.tb.Fatalf("ReindexPost: %+v", err)
		}
	}
}

// indexRows returns the index as a map from post ID to text.
func (f *fix) indexRows() map[uint64]string {
	f.tb.Helper()
	rows, err := f.s.IndexRows(f.ctx)
	if err != nil {
		f.tb.Fatalf("IndexRows: %+v", err)
	}
	return rows
}

func TestFTS5IsAvailable(t *testing.T) {
	f := newFix(t)
	err := f.s.db(f.ctx).Exec("CREATE VIRTUAL TABLE fts5_probe USING fts5(text)").Error
	if err != nil {
		t.Fatalf("the bundled SQLite has no FTS5: %v", err)
	}
	f.exec("INSERT INTO fts5_probe (rowid, text) VALUES (1, 'hello world')")
	var n int64
	if err := f.s.db(f.ctx).Raw("SELECT count(*) FROM fts5_probe WHERE fts5_probe MATCH 'hel*'").Scan(&n).Error; err != nil || n != 1 {
		t.Fatalf("an FTS5 prefix query found %d rows, err %v", n, err)
	}
	// The real index exists, is recorded and folds diacritics.
	var applied int64
	if err := f.s.db(f.ctx).Model(&schemaMigration{}).Where("id = ?", "0001_posts_fts").Count(&applied).Error; err != nil || applied != 1 {
		t.Errorf("migration 0001_posts_fts recorded %d times, err %v", applied, err)
	}
	var def string
	if err := f.s.db(f.ctx).Raw("SELECT sql FROM sqlite_master WHERE name = 'posts_fts'").Scan(&def).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(def, "remove_diacritics 2") {
		t.Errorf("posts_fts is %q, want the unicode61 tokenizer that removes diacritics", def)
	}
}

func TestMatchExpression(t *testing.T) {
	tests := []struct {
		name  string
		terms []SearchTerm
		want  string
	}{
		{"one word is a quoted prefix", []SearchTerm{{Text: "deploy"}}, `"deploy"*`},
		{"words are anded", []SearchTerm{{Text: "a"}, {Text: "b"}}, `"a"* "b"*`},
		{"a phrase has no star", []SearchTerm{{Text: "exact phrase", Phrase: true}}, `"exact phrase"`},
		{"quotes are doubled", []SearchTerm{{Text: `say"hi`}}, `"say""hi"*`},
		{"operators stay inside quotes", []SearchTerm{{Text: "OR"}, {Text: "NOT"}, {Text: "NEAR(a b)"}, {Text: "^x"}, {Text: "a:b"}},
			`"OR"* "NOT"* "NEAR(a b)"* "^x"* "a:b"*`},
		{"none", nil, ``},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchExpression(tt.terms); got != tt.want {
				t.Errorf("matchExpression = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestCutRunes(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"", 3, ""},
		{"abc", 3, "abc"},
		{"abcd", 3, "abc…"},
		{"héllo wörld", 5, "héllo…"},
		{"日本語のテキスト", 3, "日本語…"},
	}
	for _, tt := range tests {
		if got := cutRunes(tt.in, tt.n); got != tt.want {
			t.Errorf("cutRunes(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}

func TestReindexPost(t *testing.T) {
	f := newFix(t)
	alice := f.user()
	room := f.room(alice)
	col := f.column(room)

	t.Run("the text is the body, the attachment names and the task titles", func(t *testing.T) {
		p := f.post(room, alice)
		f.exec("UPDATE posts SET body = ? WHERE id = ?", "The quarterly report", p.ID)
		f.create(&Attachment{PostID: p.ID, UploaderID: alice.ID, FileName: "budget.xlsx", ContentType: "x", StorageKey: "k1"})
		f.create(&Attachment{PostID: p.ID, UploaderID: alice.ID, FileName: "notes.txt", ContentType: "x", StorageKey: "k2"})
		for _, title := range []string{"Send invoices", "Call Ünal"} {
			f.create(&Task{RoomID: room.ID, PostID: p.ID, ColumnID: col.ID, Position: f.next(), Title: title})
		}
		f.reindex(p)
		want := "The quarterly report\nbudget.xlsx\nnotes.txt\nSend invoices\nCall Ünal"
		if got := f.indexRows()[p.ID]; got != want {
			t.Errorf("indexed text = %q, want %q", got, want)
		}
		for _, q := range []string{`"quarterly"`, `"budget"`, `"xlsx"`, `"invoices"`, `"unal"`} {
			if got := f.ftsIDs(q); !reflect.DeepEqual(got, []uint64{p.ID}) {
				t.Errorf("MATCH %s = %v, want [%d]", q, got, p.ID)
			}
		}
	})

	t.Run("an empty body leaves no blank lines at the ends", func(t *testing.T) {
		p := f.post(room, alice)
		f.exec("UPDATE posts SET body = '' WHERE id = ?", p.ID)
		f.create(&Attachment{PostID: p.ID, UploaderID: alice.ID, FileName: "only.png", ContentType: "x", StorageKey: "k3"})
		f.reindex(p)
		if got := f.indexRows()[p.ID]; got != "only.png" {
			t.Errorf("indexed text = %q, want only.png", got)
		}
	})

	t.Run("the match markers are dropped", func(t *testing.T) {
		p := f.post(room, alice)
		f.exec("UPDATE posts SET body = ? WHERE id = ?", "a\ue000b\ue001c", p.ID)
		f.reindex(p)
		if got := f.indexRows()[p.ID]; got != "abc" {
			t.Errorf("indexed text = %q, want abc", got)
		}
	})

	t.Run("reindexing again replaces the row", func(t *testing.T) {
		p := f.post(room, alice)
		f.exec("UPDATE posts SET body = 'first version' WHERE id = ?", p.ID)
		f.reindex(p, p)
		f.exec("UPDATE posts SET body = 'second version' WHERE id = ?", p.ID)
		f.reindex(p)
		if got := f.indexRows()[p.ID]; got != "second version" {
			t.Errorf("indexed text = %q", got)
		}
		if got := f.ftsIDs(`"first"`); len(got) != 0 {
			t.Errorf("the old text still matches: %v", got)
		}
		var n int64
		if err := f.s.db(f.ctx).Raw("SELECT count(*) FROM posts_fts WHERE rowid = ?", p.ID).Scan(&n).Error; err != nil || n != 1 {
			t.Errorf("%d rows for the post, err %v", n, err)
		}
	})

	t.Run("a deleted placeholder and a missing post are not indexed", func(t *testing.T) {
		p := f.post(room, alice)
		f.exec("UPDATE posts SET body = 'will be a placeholder' WHERE id = ?", p.ID)
		f.reindex(p)
		if _, ok := f.indexRows()[p.ID]; !ok {
			t.Fatal("the post is not indexed")
		}
		if err := f.s.MarkPostDeleted(f.ctx, p.ID, day(time.Hour)); err != nil {
			t.Fatal(err)
		}
		f.reindex(p)
		if _, ok := f.indexRows()[p.ID]; ok {
			t.Error("the placeholder is still indexed")
		}
		gone := f.post(room, alice)
		f.reindex(gone)
		if err := f.s.DeletePost(f.ctx, gone.ID); err != nil {
			t.Fatal(err)
		}
		f.reindex(gone)
		if _, ok := f.indexRows()[gone.ID]; ok {
			t.Error("a deleted row is still indexed")
		}
		if err := f.s.ReindexPost(f.ctx, 999_999); err != nil {
			t.Errorf("ReindexPost of a missing post = %+v, want nil", err)
		}
	})
}

func TestUnindexRoom(t *testing.T) {
	f := newFix(t)
	alice := f.user()
	a, b := f.room(alice), f.room(alice)
	pa := f.post(a, alice)
	ca := f.comment(pa, alice)
	pb := f.post(b, alice)
	f.reindex(pa, ca, pb)
	if err := f.s.UnindexRoom(f.ctx, a.ID); err != nil {
		t.Fatalf("UnindexRoom: %+v", err)
	}
	rows := f.indexRows()
	if len(rows) != 1 || rows[pb.ID] == "" {
		t.Errorf("index after UnindexRoom = %v, want only post %d", rows, pb.ID)
	}
	if err := f.s.UnindexRoom(f.ctx, 999_999); err != nil {
		t.Errorf("UnindexRoom of a missing room = %+v, want nil", err)
	}
}

// TestBackfill runs the migrations in two phases: the posts exist before the
// index does, as in a database made by an earlier version.
func TestBackfill(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "backfill.db")
	clk := clock.NewFake(fakeStart)

	before := defaultOptions()
	before.migrations = []migration{}
	s, err := open(ctx, path, clk, nil, before)
	if err != nil {
		t.Fatalf("open without the index: %+v", err)
	}
	f := &fix{tb: t, ctx: ctx, s: s, clk: clk}
	alice := f.user()
	room := f.room(alice)
	col := f.column(room)
	plain := f.post(room, alice)
	rich := f.post(room, alice)
	f.exec("UPDATE posts SET body = ? WHERE id = ?", "Rich café\ue000 post", rich.ID)
	f.create(&Attachment{PostID: rich.ID, UploaderID: alice.ID, FileName: "plan.pdf", ContentType: "x", StorageKey: "k1"})
	f.create(&Task{RoomID: room.ID, PostID: rich.ID, ColumnID: col.ID, Position: 0, Title: "Ship it"})
	comment := f.comment(rich, alice)
	placeholder := f.post(room, alice)
	f.exec("UPDATE posts SET deleted_at = ?, body = '' WHERE id = ?", day(time.Hour).UTC(), placeholder.ID)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = open(ctx, path, clk, nil, defaultOptions())
	if err != nil {
		t.Fatalf("open with the index: %+v", err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	f = &fix{tb: t, ctx: ctx, s: s, clk: clk}
	got := f.indexRows()
	want := map[uint64]string{
		plain.ID:   plain.Body,
		rich.ID:    "Rich café post\nplan.pdf\nShip it",
		comment.ID: comment.Body,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("backfilled index = %v, want %v", got, want)
	}
	if ids := f.ftsIDs(`"cafe"`); !reflect.DeepEqual(ids, []uint64{rich.ID}) {
		t.Errorf("MATCH cafe = %v, want [%d]", ids, rich.ID)
	}
	// What the backfill wrote is what ReindexPost writes.
	f.reindex(plain, rich, comment)
	if again := f.indexRows(); !reflect.DeepEqual(again, want) {
		t.Errorf("index after ReindexPost = %v, want %v", again, want)
	}
}

// searchFixture is a store with posts to search: one visible room, one
// private room with a member, and users who read them.
type searchFixture struct {
	*fix
	alice, bob, carol *User
	lobby, den        *Room
}

func newSearchFixture(t *testing.T) *searchFixture {
	t.Helper()
	f := newFix(t)
	sf := &searchFixture{fix: f, alice: f.user(), bob: f.user(), carol: f.user()}
	sf.lobby = f.room(sf.alice)
	sf.den = f.room(sf.alice)
	f.exec("UPDATE rooms SET visibility = ?, name = 'Den' WHERE id = ?", VisibilityPrivate, sf.den.ID)
	f.exec("UPDATE rooms SET name = 'Lobby' WHERE id = ?", sf.lobby.ID)
	f.create(&RoomMember{RoomID: sf.den.ID, UserID: sf.alice.ID})
	return sf
}

// write inserts and indexes a post with the body.
func (sf *searchFixture) write(room *Room, author *User, body string) *Post {
	sf.tb.Helper()
	p := &Post{RoomID: room.ID, AuthorID: author.ID, Format: FormatText, Body: body}
	sf.create(p)
	sf.reindex(p)
	return p
}

func (sf *searchFixture) search(q SearchQuery) []SearchHit {
	sf.tb.Helper()
	if q.Limit == 0 {
		q.Limit = 100
	}
	if q.ReaderID == 0 {
		q.ReaderID = sf.bob.ID
	}
	hits, err := sf.s.SearchPosts(sf.ctx, q)
	if err != nil {
		sf.tb.Fatalf("SearchPosts(%+v): %+v", q, err)
	}
	return hits
}

func hitIDs(hits []SearchHit) []uint64 {
	out := []uint64{}
	for _, h := range hits {
		out = append(out, h.PostID)
	}
	return out
}

func TestSearchPosts(t *testing.T) {
	sf := newSearchFixture(t)
	deploy := sf.write(sf.lobby, sf.alice, "Deploying the new release tonight")
	secret := sf.write(sf.den, sf.alice, "Secret deploy plan")
	reply := &Post{RoomID: sf.lobby.ID, AuthorID: sf.bob.ID, ParentID: &deploy.ID, RootID: &deploy.ID, Format: FormatText, Body: "Release notes are ready"}
	sf.create(reply)
	sf.reindex(reply)
	cafe := sf.write(sf.lobby, sf.bob, "Lunch at the Café Münster")
	sf.exec("UPDATE posts SET created_at = ? WHERE id = ?", time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), deploy.ID)
	sf.exec("UPDATE posts SET created_at = ? WHERE id = ?", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), reply.ID)
	sf.exec("UPDATE posts SET created_at = ? WHERE id = ?", time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC), cafe.ID)
	tagged := sf.tag()
	other := sf.tag()
	sf.create(&PostTag{PostID: deploy.ID, TagID: tagged.ID})
	sf.create(&PostTag{PostID: deploy.ID, TagID: other.ID})
	sf.create(&PostTag{PostID: cafe.ID, TagID: tagged.ID})

	tests := []struct {
		name string
		q    SearchQuery
		want []uint64
	}{
		{"a word as a prefix", SearchQuery{Terms: []SearchTerm{{Text: "deplo"}}}, []uint64{deploy.ID}},
		{"two words need both", SearchQuery{Terms: []SearchTerm{{Text: "release"}, {Text: "tonight"}}}, []uint64{deploy.ID}},
		{"a phrase", SearchQuery{Terms: []SearchTerm{{Text: "new release", Phrase: true}}}, []uint64{deploy.ID}},
		{"a phrase out of order finds nothing", SearchQuery{Terms: []SearchTerm{{Text: "release new", Phrase: true}}}, []uint64{}},
		{"a word is not a suffix match", SearchQuery{Terms: []SearchTerm{{Text: "ploy"}}}, []uint64{}},
		{"diacritics fold both ways", SearchQuery{Terms: []SearchTerm{{Text: "cafe"}, {Text: "munster"}}}, []uint64{cafe.ID}},
		{"diacritics typed", SearchQuery{Terms: []SearchTerm{{Text: "Café"}}}, []uint64{cafe.ID}},
		{"a private room of others is not searched", SearchQuery{Terms: []SearchTerm{{Text: "secret"}}}, []uint64{}},
		{"a member reads the private room", SearchQuery{ReaderID: sf.alice.ID, Terms: []SearchTerm{{Text: "secret"}}}, []uint64{secret.ID}},
		{"both rooms for the member, newest first", SearchQuery{ReaderID: sf.alice.ID, Terms: []SearchTerm{{Text: "deploy"}}}, []uint64{secret.ID, deploy.ID}},
		{"filters alone: newest first", SearchQuery{Room: "lobby"}, []uint64{cafe.ID, reply.ID, deploy.ID}},
		{"the room name ignores case", SearchQuery{Room: "LOBBY", OnlyPosts: true}, []uint64{cafe.ID, deploy.ID}},
		{"the private room by name for a non-member", SearchQuery{Room: "den"}, []uint64{}},
		{"author", SearchQuery{Author: sf.bob.Username}, []uint64{cafe.ID, reply.ID}},
		{"after is inclusive", SearchQuery{After: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}, []uint64{cafe.ID, reply.ID}},
		{"before is exclusive", SearchQuery{Before: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}, []uint64{deploy.ID}},
		{"a date range", SearchQuery{After: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), Before: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)}, []uint64{reply.ID}},
		{"only posts", SearchQuery{OnlyPosts: true}, []uint64{cafe.ID, deploy.ID}},
		{"only comments", SearchQuery{OnlyComments: true}, []uint64{reply.ID}},
		{"posts and comments at once", SearchQuery{OnlyPosts: true, OnlyComments: true}, []uint64{}},
		{"one tag", SearchQuery{Tags: []string{tagged.Name}}, []uint64{cafe.ID, deploy.ID}},
		{"all tags are required", SearchQuery{Tags: []string{tagged.Name, other.Name}}, []uint64{deploy.ID}},
		{"an unknown tag", SearchQuery{Tags: []string{"nope"}}, []uint64{}},
		{"words and a tag", SearchQuery{Terms: []SearchTerm{{Text: "lunch"}}, Tags: []string{tagged.Name}}, []uint64{cafe.ID}},
		{"everything together", SearchQuery{
			Terms: []SearchTerm{{Text: "release"}}, Tags: []string{tagged.Name}, Room: "lobby", Author: sf.alice.Username,
			After: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Before: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), OnlyPosts: true,
		}, []uint64{deploy.ID}},
		{"operators are plain text", SearchQuery{Terms: []SearchTerm{{Text: "OR"}, {Text: "NOT"}, {Text: "NEAR"}}}, []uint64{}},
		{"limit and offset", SearchQuery{Limit: 1, Offset: 1}, []uint64{reply.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hitIDs(sf.search(tt.q)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("hits = %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("a hit carries the room, author, kind and time", func(t *testing.T) {
		hits := sf.search(SearchQuery{OnlyComments: true})
		if len(hits) != 1 {
			t.Fatalf("hits = %+v", hits)
		}
		h := hits[0]
		if h.PostID != reply.ID || h.RootID != deploy.ID || !h.IsComment || h.RoomID != sf.lobby.ID || h.RoomName != "Lobby" ||
			h.AuthorID != sf.bob.ID || h.AuthorUsername != sf.bob.Username || h.AuthorDisplayName != sf.bob.DisplayName {
			t.Errorf("hit = %+v", h)
		}
		requireSameTime(t, "CreatedAt", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), h.CreatedAt)
		top := sf.search(SearchQuery{Terms: []SearchTerm{{Text: "deploying"}}})
		if len(top) != 1 || top[0].IsComment || top[0].RootID != 0 {
			t.Errorf("top-level hit = %+v", top)
		}
	})

	t.Run("a deleted post is never found", func(t *testing.T) {
		gone := sf.write(sf.lobby, sf.alice, "ephemeral words")
		if err := sf.s.MarkPostDeleted(sf.ctx, gone.ID, day(time.Hour)); err != nil {
			t.Fatal(err)
		}
		// The row is still in the index (nothing reindexed the post), and the
		// search drops it all the same.
		if got := sf.search(SearchQuery{Terms: []SearchTerm{{Text: "ephemeral"}}}); len(got) != 0 {
			t.Errorf("hits = %+v", got)
		}
		if got := sf.search(SearchQuery{Room: "lobby", Author: sf.alice.Username, OnlyPosts: true}); slices.Contains(hitIDs(got), gone.ID) {
			t.Errorf("a filter-only search found the deleted post: %v", hitIDs(got))
		}
	})

	t.Run("the best match comes first, then the newest", func(t *testing.T) {
		short := sf.write(sf.lobby, sf.alice, "zebra")
		long := sf.write(sf.lobby, sf.alice, "zebra "+strings.Repeat("filler ", 40))
		same := sf.write(sf.lobby, sf.alice, "zebra")
		got := hitIDs(sf.search(SearchQuery{Terms: []SearchTerm{{Text: "zebra"}}}))
		if want := []uint64{same.ID, short.ID, long.ID}; !reflect.DeepEqual(got, want) {
			t.Errorf("order = %v, want %v", got, want)
		}
	})
}

func TestSearchPostsSnippets(t *testing.T) {
	sf := newSearchFixture(t)
	p := sf.write(sf.lobby, sf.alice, "Hello <script>alert(1)</script> world")
	long := sf.write(sf.lobby, sf.alice, strings.Repeat("é", 150)+" end "+strings.Repeat("x", 100)+" "+strings.Repeat("word ", 60)+"needle "+strings.Repeat("more ", 60))

	t.Run("matches are between the markers", func(t *testing.T) {
		hits := sf.search(SearchQuery{Terms: []SearchTerm{{Text: "hel"}}})
		want := SearchMarkStart + "Hello" + SearchMarkEnd + " <script>alert(1)</script> world"
		if len(hits) != 1 || hits[0].Snippet != want {
			t.Errorf("snippet = %+v, want %q", hits, want)
		}
	})

	t.Run("a long text is cut around the match", func(t *testing.T) {
		hits := sf.search(SearchQuery{Terms: []SearchTerm{{Text: "needle"}}})
		if len(hits) != 1 || hits[0].PostID != long.ID {
			t.Fatalf("hits = %+v", hits)
		}
		sn := hits[0].Snippet
		if !strings.Contains(sn, SearchMarkStart+"needle"+SearchMarkEnd) || !strings.HasPrefix(sn, "…") || !strings.HasSuffix(sn, "…") ||
			len(strings.Fields(sn)) > 30 {
			t.Errorf("snippet = %q", sn)
		}
	})

	t.Run("without words it is the start of the text", func(t *testing.T) {
		hits := sf.search(SearchQuery{Room: "lobby"})
		if len(hits) != 2 {
			t.Fatalf("hits = %+v", hits)
		}
		if got, want := hits[0].Snippet, strings.Repeat("é", 150)+" end "+strings.Repeat("x", 45)+"…"; got != want {
			t.Errorf("long snippet = %q (%d runes), want %q", got, len([]rune(got)), want)
		}
		if hits[1].PostID != p.ID || hits[1].Snippet != p.Body {
			t.Errorf("short snippet = %+v", hits[1])
		}
		if strings.ContainsAny(hits[0].Snippet+hits[1].Snippet, SearchMarkStart+SearchMarkEnd) {
			t.Error("a snippet without words has markers")
		}
	})
}

// TestSearchPostsInjection feeds FTS5 syntax as words: none of it may be an
// operator, an error, or a way to see what the words do not match.
func TestSearchPostsInjection(t *testing.T) {
	sf := newSearchFixture(t)
	p := sf.write(sf.lobby, sf.alice, "alpha beta gamma")
	sf.write(sf.lobby, sf.alice, "delta")
	tests := []struct {
		name  string
		terms []SearchTerm
		want  []uint64
	}{
		{"OR", []SearchTerm{{Text: "alpha OR delta"}}, []uint64{}},
		{"OR as words", []SearchTerm{{Text: "alpha"}, {Text: "OR"}, {Text: "delta"}}, []uint64{}},
		{"NOT", []SearchTerm{{Text: "NOT"}, {Text: "alpha"}}, []uint64{}},
		{"NEAR", []SearchTerm{{Text: "NEAR(alpha beta)"}}, []uint64{}},
		{"a column filter", []SearchTerm{{Text: "text:alpha"}}, []uint64{}},
		{"a column filter, the word after the colon", []SearchTerm{{Text: "text"}, {Text: "alpha"}}, []uint64{}},
		{"a star", []SearchTerm{{Text: "*"}, {Text: "alpha"}}, []uint64{p.ID}},
		{"a caret", []SearchTerm{{Text: "^alpha"}}, []uint64{p.ID}},
		{"a minus", []SearchTerm{{Text: "-alpha"}}, []uint64{p.ID}},
		{"parentheses", []SearchTerm{{Text: "(alpha"}, {Text: "beta)"}}, []uint64{p.ID}},
		{"a quote inside", []SearchTerm{{Text: `al"pha`}}, []uint64{}},
		{"a closing quote then an operator", []SearchTerm{{Text: `alpha" OR "delta`}}, []uint64{}},
		{"only punctuation", []SearchTerm{{Text: "***"}}, []uint64{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sf.s.SearchPosts(sf.ctx, SearchQuery{ReaderID: sf.bob.ID, Terms: tt.terms, Limit: 10})
			if err != nil {
				t.Fatalf("SearchPosts: %+v", err)
			}
			if ids := hitIDs(got); !reflect.DeepEqual(ids, tt.want) {
				t.Errorf("hits = %v, want %v", ids, tt.want)
			}
		})
	}
}

func TestSearchErrors(t *testing.T) {
	f := newFix(t)
	alice := f.user()
	room := f.room(alice)
	p := f.post(room, alice)
	f.exec("DROP TABLE posts_fts")

	tests := []struct {
		name string
		call func() error
		want string
		op   string
	}{
		{"ReindexPost", func() error { return f.s.ReindexPost(f.ctx, p.ID) }, "Store.ReindexPost.QueryFailed", "reindexPost"},
		{"UnindexRoom", func() error { return f.s.UnindexRoom(f.ctx, room.ID) }, "Store.UnindexRoom.QueryFailed", "unindexRoom"},
		{"IndexRows", func() error { _, err := f.s.IndexRows(f.ctx); return err }, "Store.IndexRows.QueryFailed", "indexRows"},
		{"SearchPosts with words", func() error {
			_, err := f.s.SearchPosts(f.ctx, SearchQuery{ReaderID: alice.ID, Terms: []SearchTerm{{Text: "x"}}, Limit: 5})
			return err
		}, "Store.SearchPosts.QueryFailed", "searchPosts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			appErr := requireRoute(t, tt.call(), tt.want)
			if appErr.Tag != TagQueryFailed || appErr.Metadata["op"] != tt.op {
				t.Errorf("tag %q op %v, want %s %s", appErr.Tag, appErr.Metadata["op"], TagQueryFailed, tt.op)
			}
		})
	}
}

// TestIndexIsInTheTransaction shows that the index follows the transaction:
// a rolled-back change leaves no row and no text.
func TestIndexIsInTheTransaction(t *testing.T) {
	f := newFix(t)
	alice := f.user()
	room := f.room(alice)
	kept := f.post(room, alice)
	f.reindex(kept)
	before := f.indexRows()

	var created *Post
	err := f.s.Tx(f.ctx, func(tx *Store) error {
		created = &Post{RoomID: room.ID, AuthorID: alice.ID, Format: FormatText, Body: "rolled back"}
		if err := tx.CreatePost(f.ctx, created); err != nil {
			return err
		}
		if err := tx.ReindexPost(f.ctx, created.ID); err != nil {
			return err
		}
		if err := tx.UnindexRoom(f.ctx, room.ID); err != nil {
			return err
		}
		if rows, err := tx.IndexRows(f.ctx); err != nil || len(rows) != 0 {
			t.Errorf("inside the transaction: rows %v, err %v", rows, err)
		}
		return errors.New("RollbackForTest", "roll back")
	})
	if !errors.Is(err, &errors.Error{Tag: "RollbackForTest"}) {
		t.Fatalf("Tx = %v, want the error of the function", err)
	}
	if after := f.indexRows(); !reflect.DeepEqual(after, before) {
		t.Errorf("index after the rollback = %v, want %v", after, before)
	}
}
