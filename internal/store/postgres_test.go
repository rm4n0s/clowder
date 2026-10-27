package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rm4n0s/clowder/internal/clock"
	"gorm.io/gorm"
)

func TestMigrationListsHaveTheSameIDs(t *testing.T) {
	t.Parallel()
	ids := func(d dialect) []string {
		var out []string
		for _, m := range rawMigrations(d) {
			if m.ID == "" || m.SQL == "" {
				t.Errorf("%s: migration %q has no ID or no SQL", d, m.ID)
			}
			out = append(out, m.ID)
		}
		return out
	}
	sqliteIDs, postgresIDs := ids(dialectSQLite), ids(dialectPostgres)
	if len(sqliteIDs) == 0 {
		t.Fatal("no raw migrations")
	}
	if !slices.Equal(sqliteIDs, postgresIDs) {
		t.Errorf("migration IDs differ: sqlite %v, postgres %v", sqliteIDs, postgresIDs)
	}
	for _, m := range migrationList() {
		if m.SQLite == "" || m.Postgres == "" {
			t.Errorf("migration %s lacks the text of a dialect", m.ID)
		}
		if m.SQLite == m.Postgres {
			t.Errorf("migration %s has the same text for both dialects", m.ID)
		}
	}
	// What each Open applies is the text of its dialect.
	for i, m := range migrationList() {
		if rawMigrations(dialectSQLite)[i].SQL != m.SQLite || rawMigrations(dialectPostgres)[i].SQL != m.Postgres {
			t.Errorf("migration %s: rawMigrations returns the wrong text", m.ID)
		}
	}
}

func TestDefaultOptionsOfEachDialect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		opts options
		want dialect
	}{
		{"sqlite", defaultOptions(), dialectSQLite},
		{"postgres", postgresOptions(), dialectPostgres},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.opts.dialect != tt.want {
				t.Errorf("dialect = %v, want %v", tt.opts.dialect, tt.want)
			}
			if got, want := len(tt.opts.migrations), len(migrationList()); got != want {
				t.Errorf("%d migrations, want %d", got, want)
			}
			if len(tt.opts.models) == 0 {
				t.Error("no models")
			}
		})
	}
}

func TestDialectString(t *testing.T) {
	t.Parallel()
	if got := dialectSQLite.String(); got != "sqlite" {
		t.Errorf("sqlite = %q", got)
	}
	if got := dialectPostgres.String(); got != "postgres" {
		t.Errorf("postgres = %q", got)
	}
	if dialect(0) != dialectSQLite {
		t.Error("the zero dialect is not SQLite")
	}
}

func TestSQLiteStoreCarriesItsDialectIntoTransactions(t *testing.T) {
	t.Parallel()
	f := newFix(t)
	if f.s.dialect != dialectSQLite {
		t.Errorf("dialect = %v, want sqlite", f.s.dialect)
	}
	err := f.s.Tx(f.ctx, func(tx *Store) error {
		if tx.dialect != f.s.dialect {
			t.Errorf("the dialect of the transaction is %v", tx.dialect)
		}
		return tx.Tx(f.ctx, func(inner *Store) error {
			if inner.dialect != f.s.dialect {
				t.Errorf("the dialect of the nested transaction is %v", inner.dialect)
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSearchIndexAndTextPerDialect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		d                dialect
		table, idColumn  string
		textHasAgg       string
		textMustNotHaveS string
	}{
		{dialectSQLite, "posts_fts", "rowid", "group_concat", "string_agg"},
		{dialectPostgres, "posts_search", "post_id", "string_agg", "group_concat"},
	}
	for _, tt := range tests {
		t.Run(tt.d.String(), func(t *testing.T) {
			t.Parallel()
			table, idColumn := searchIndex(tt.d)
			if table != tt.table || idColumn != tt.idColumn {
				t.Errorf("searchIndex = %s, %s", table, idColumn)
			}
			text := ftsText(tt.d)
			if !strings.Contains(text, tt.textHasAgg) || strings.Contains(text, tt.textMustNotHaveS) {
				t.Errorf("ftsText = %s", text)
			}
			// Both build the text from the same three parts and drop the markers.
			for _, part := range []string{"p.body", "file_name", "title", "57344", "57345"} {
				if !strings.Contains(text, part) {
					t.Errorf("ftsText lacks %q", part)
				}
			}
		})
	}
}

func TestOpenPostgresFailures(t *testing.T) {
	t.Parallel()
	const password = "hunter2"
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	tests := []struct {
		name  string
		ctx   context.Context
		url   string
		route string
	}{
		{
			name:  "the URL cannot be parsed",
			ctx:   t.Context(),
			url:   "postgres://clowder:" + password + "@127.0.0.1:5432/clowder?connect_timeout=x",
			route: "OpenPostgres->open.StoreOpenFailed",
		},
		{
			name:  "the server cannot be reached",
			ctx:   t.Context(),
			url:   "postgres://clowder:" + password + "@127.0.0.1:1/clowder?sslmode=disable&connect_timeout=2",
			route: "OpenPostgres->open->pingAndMigrate.StoreOpenFailed",
		},
		{
			name:  "the context is done",
			ctx:   canceled,
			url:   "postgres://clowder:" + password + "@127.0.0.1:1/clowder?sslmode=disable",
			route: "OpenPostgres->open->pingAndMigrate.StoreOpenFailed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, err := OpenPostgres(tt.ctx, tt.url, clock.NewFake(fakeStart), nil)
			if err == nil {
				_ = s.Close()
				t.Fatal("OpenPostgres succeeded")
			}
			appErr := requireRoute(t, err, tt.route)
			if appErr.Tag != tagOpenFailed {
				t.Errorf("tag = %s", appErr.Tag)
			}
			// The URL is in the error, without its password, wherever it could be.
			for _, text := range []string{
				appErr.Message, appErr.Error(), fmt.Sprint(appErr.Metadata), fmt.Sprintf("%+v", appErr.ToJson()),
			} {
				if strings.Contains(text, password) {
					t.Errorf("the error leaks the password: %s", text)
				}
			}
			if tt.route == "OpenPostgres->open.StoreOpenFailed" {
				if got := appErr.Metadata["databaseURL"]; got != "postgres://clowder:xxxxx@127.0.0.1:5432/clowder?connect_timeout=x" {
					t.Errorf("databaseURL = %v, want the URL with xxxxx for the password", got)
				}
			}
		})
	}
}

func TestClassifyUniqueViolationOfPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want failure
	}{
		{"23505", &pgconn.PgError{Code: "23505"}, failureConflict},
		{"23505 wrapped", fmt.Errorf("insert: %w", &pgconn.PgError{Code: "23505"}), failureConflict},
		{"other code", &pgconn.PgError{Code: "23503"}, failureOther},
		{"GORM's translation", gorm.ErrDuplicatedKey, failureConflict},
		{"no rows", gorm.ErrRecordNotFound, failureNotFound},
		{"other", fmt.Errorf("boom"), failureOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := classify(tt.err); got != tt.want {
				t.Errorf("classify = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTSQueryTokens(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want []string
	}{
		{"deploy", []string{"deploy"}},
		{"new release", []string{"new", "release"}},
		{"Budget-2026.xlsx", []string{"Budget", "2026", "xlsx"}},
		{"Café Münster", []string{"Café", "Münster"}},
		{"Καλημέρα κόσμε", []string{"Καλημέρα", "κόσμε"}},
		{"école", []string{"école"}},
		{`al"pha`, []string{"al", "pha"}},
		{"a & b | !c <-> (d):*", []string{"a", "b", "c", "d"}},
		{"***", nil},
		{"", nil},
	}
	for _, tt := range tests {
		if got := tsQueryTokens(tt.in); !slices.Equal(got, tt.want) {
			t.Errorf("tsQueryTokens(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTSQueryExpression(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		terms  []SearchTerm
		want   string
		wantOK bool
	}{
		{"one word is a prefix", []SearchTerm{{Text: "deploy"}}, `(deploy:*)`, true},
		{"words are anded", []SearchTerm{{Text: "a"}, {Text: "b"}}, `(a:*) & (b:*)`, true},
		{"a phrase has no star", []SearchTerm{{Text: "exact phrase", Phrase: true}}, `(exact <-> phrase)`, true},
		{"a word of several tokens is a phrase with a prefix at the end", []SearchTerm{{Text: "Budget-2026.xlsx"}},
			`(Budget <-> 2026 <-> xlsx:*)`, true},
		{"a one-word phrase", []SearchTerm{{Text: "word", Phrase: true}}, `(word)`, true},
		{"operators are plain text", []SearchTerm{{Text: "OR"}, {Text: "NOT"}, {Text: "NEAR(a b)"}, {Text: "^x"}, {Text: "a:b"}},
			`(OR:*) & (NOT:*) & (NEAR <-> a <-> b:*) & (x:*) & (a <-> b:*)`, true},
		{"quotes and a closing operator", []SearchTerm{{Text: `alpha" OR "delta`}}, `(alpha <-> OR <-> delta:*)`, true},
		{"quote injection", []SearchTerm{{Text: `'; DROP TABLE posts; --`}}, `(DROP <-> TABLE <-> posts:*)`, true},
		{"a term without tokens is dropped", []SearchTerm{{Text: "*"}, {Text: "alpha"}}, `(alpha:*)`, true},
		{"only punctuation matches nothing", []SearchTerm{{Text: "***"}}, ``, false},
		{"none", nil, ``, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := tsQueryExpression(tt.terms)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("tsQueryExpression = %q, %v; want %q, %v", got, ok, tt.want, tt.wantOK)
			}
			// Only letters, digits, marks and the operators this function writes.
			for _, r := range got {
				if strings.ContainsRune(`'"\;,`, r) {
					t.Errorf("the expression %q holds %q", got, r)
				}
			}
		})
	}
}

func TestOverlayMarkers(t *testing.T) {
	t.Parallel()
	const s, e = SearchMarkStart, SearchMarkEnd
	tests := []struct {
		name, headline, fragment, want string
	}{
		{"punctuation comes back", s + "xlsx" + e, "xlsx", s + "xlsx" + e},
		{"a file name", "Budget 2026 " + s + "xlsx" + e, "Budget-2026.xlsx", "Budget-2026." + s + "xlsx" + e},
		{"a tag is kept", s + "Hello" + e + punctuationToSpaces(" <script>alert(1)</script> world"),
			"Hello <script>alert(1)</script> world", s + "Hello" + e + " <script>alert(1)</script> world"},
		{"multi-byte characters", "Lunch at the " + s + "Café" + e, "Lunch at the Café", "Lunch at the " + s + "Café" + e},
		{"no match", "plain", "plain", "plain"},
		{"a longer fragment gives the headline", "ab", "abc", "ab"},
		{"a shorter fragment gives the headline", "abc", "ab", "abc"},
		{"empty", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := overlayMarkers(tt.headline, tt.fragment); got != tt.want {
				t.Errorf("overlayMarkers = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSnippetOf(t *testing.T) {
	t.Parallel()
	const s, e = SearchMarkStart, SearchMarkEnd
	tests := []struct {
		name   string
		row    postgresSearchRow
		phrase bool
		want   string
	}{
		{"the whole text", postgresSearchRow{Headline: s + "Hello" + e + " world", Fragment: "Hello world", FragmentPos: 1, TextLen: 11},
			false, s + "Hello" + e + " world"},
		{"cut at both ends", postgresSearchRow{Headline: "b " + s + "c" + e + " d", Fragment: "b c d", FragmentPos: 3, TextLen: 20},
			false, "…b " + s + "c" + e + " d…"},
		{"cut at the start only", postgresSearchRow{Headline: "x " + s + "y" + e, Fragment: "x y", FragmentPos: 4, TextLen: 6},
			false, "…x " + s + "y" + e},
		{"cut at the end only", postgresSearchRow{Headline: s + "x" + e + " y", Fragment: "x y", FragmentPos: 1, TextLen: 9},
			false, s + "x" + e + " y…"},
		{"not found", postgresSearchRow{Headline: s + "x" + e, Fragment: "", FragmentPos: 0, TextLen: 9}, false, s + "x" + e},
		{"words are marked one by one without a phrase", postgresSearchRow{
			Headline: "Lunch " + s + "at" + e + " " + s + "the" + e + " Cafe", Fragment: "Lunch at the Cafe", FragmentPos: 1, TextLen: 17},
			false, "Lunch " + s + "at" + e + " " + s + "the" + e + " Cafe"},
		{"a phrase is marked as a whole", postgresSearchRow{
			Headline: "Lunch " + s + "at" + e + " " + s + "the" + e + " Cafe", Fragment: "Lunch at the Cafe", FragmentPos: 1, TextLen: 17},
			true, "Lunch " + s + "at the" + e + " Cafe"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := snippetOf(tt.row, tt.phrase); got != tt.want {
				t.Errorf("snippetOf = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHasPhrase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		terms []SearchTerm
		want  bool
	}{
		{"words", []SearchTerm{{Text: "a b"}}, false},
		{"a phrase of one word", []SearchTerm{{Text: "word", Phrase: true}}, false},
		{"a phrase of two", []SearchTerm{{Text: "two words", Phrase: true}}, true},
		{"mixed", []SearchTerm{{Text: "a"}, {Text: "b-c", Phrase: true}}, true},
		{"none", nil, false},
	}
	for _, tt := range tests {
		if got := hasPhrase(tt.terms); got != tt.want {
			t.Errorf("%s: hasPhrase = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// punctuationToSpaces is what the search query does to the text before
// ts_headline reads it: every ASCII character that is not a letter or a digit
// becomes a space.
func punctuationToSpaces(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x80 && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return ' '
		}
		return r
	}, s)
}
