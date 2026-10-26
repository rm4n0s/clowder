package app_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/markdown"
	"github.com/rm4n0s/clowder/internal/store"
)

// postsEnv is the posts service on the database of roomsEnv: lobby is visible
// with the members admin and bob; den is private with the members alice and
// bob; root is an admin who is a member of nothing; carol and dave are members
// of nothing.
type postsEnv struct {
	*roomsEnv
	posts *app.Posts
}

func newPostsEnv(t *testing.T) *postsEnv {
	t.Helper()
	e := &postsEnv{roomsEnv: newRoomsEnv(t)}
	e.posts = app.NewPosts(e.st, e.clk, markdown.New())
	return e
}

// write makes a text post of the user in the room and fails the test on an
// error.
func (e *postsEnv) write(user, room, body string, tags ...string) app.Post {
	e.t.Helper()
	p, err := e.posts.Create(e.t.Context(), e.as(user), e.room(room).ID,
		app.PostInput{Format: app.FormatText, Body: body, Tags: tags})
	if err != nil {
		e.t.Fatalf("Create %q: %+v", body, err)
	}
	return p
}

// reply inserts a comment under the post through the store, as step 5.1 will
// through the service.
func (e *postsEnv) reply(parent app.Post, user string) store.Post {
	e.t.Helper()
	root := parent.ID
	c := store.Post{
		RoomID: parent.RoomID, AuthorID: e.users[user].ID, ParentID: &parent.ID, RootID: &root,
		Format: store.FormatText, Body: "a reply",
	}
	if err := e.st.CreatePost(e.t.Context(), &c); err != nil {
		e.t.Fatalf("CreatePost reply: %+v", err)
	}
	return c
}

// storedTags returns the tag names linked to the post in the database.
func (e *postsEnv) storedTags(postID uint64) []string {
	e.t.Helper()
	m, err := e.st.TagNamesOfPosts(e.t.Context(), []uint64{postID})
	if err != nil {
		e.t.Fatalf("TagNamesOfPosts: %+v", err)
	}
	return m[postID]
}

// postCase is one row of a permission table of a method on a post: who calls
// it on a post that bob wrote in which room, and the tag it must fail with.
type postCase struct {
	actor string
	room  string
	want  string
}

// runPostPermissions has bob write a post in the room of each case, runs call
// as the actor of the case on a fresh fixture and checks the result. A failed
// call writes no audit event.
func runPostPermissions(t *testing.T, routes map[string]string, cases []postCase, call func(e *postsEnv, a app.Actor, p app.Post) error) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.actor+" on "+tc.room, func(t *testing.T) {
			e := newPostsEnv(t)
			p := e.write("bob", tc.room, "bob's post")
			before := len(e.events())
			err := call(e, e.as(tc.actor), p)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("got %+v, want success", err)
				}
				return
			}
			_ = requireTag(t, err, tc.want, routes[tc.want])
			if n := len(e.events()); n != before {
				t.Errorf("a failed call wrote %d events", n-before)
			}
		})
	}
}

// requireNoBody fails the test if the text of a body appears in any audit
// event.
func (e *postsEnv) requireNoBody(body string) {
	e.t.Helper()
	for _, ev := range e.events() {
		if strings.Contains(ev.Details, body) || strings.Contains(ev.Summary, body) {
			e.t.Errorf("event %s holds the body: %s / %s", ev.Action, ev.Summary, ev.Details)
		}
	}
}

const missingPost = "Store.PostByID.NotFound"

func TestPostsAliasesMatchTheStore(t *testing.T) {
	if app.FormatText != store.FormatText || app.FormatMarkdown != store.FormatMarkdown {
		t.Error("the Format constants differ from the store's")
	}
	if markdown.Format(app.FormatText) != markdown.FormatText || markdown.Format(app.FormatMarkdown) != markdown.FormatMarkdown {
		t.Error("the Format constants differ from the markdown package's")
	}
}

func TestParseTags(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", []string{}},
		{"blank", "  \t ", []string{}},
		{"spaces", "a b  c", []string{"a", "b", "c"}},
		{"commas", "a,b,,c", []string{"a", "b", "c"}},
		{"both", " a, b ,c\td\n#e ", []string{"a", "b", "c", "d", "#e"}},
		{"unicode space", "a b", []string{"a", "b"}},
		{"keeps case and hash", "#Go Rust", []string{"#Go", "Rust"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := app.ParseTags(tt.in)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseTags(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestPostsCreatePermissions(t *testing.T) {
	routes := map[string]string{
		app.TagNotFound:  notFoundPrivate,
		app.TagForbidden: "Posts.Create.func1.Forbidden",
	}
	cases := []permCase{
		{"admin", "lobby", ""}, {"bob", "lobby", ""},
		{"root", "lobby", app.TagForbidden}, {"alice", "lobby", app.TagForbidden}, {"carol", "lobby", app.TagForbidden},
		{"alice", "den", ""}, {"bob", "den", ""},
		{"admin", "den", app.TagNotFound}, {"root", "den", app.TagNotFound}, {"carol", "den", app.TagNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.actor+" on "+tc.room, func(t *testing.T) {
			e := newPostsEnv(t)
			before := len(e.events())
			p, err := e.posts.Create(t.Context(), e.as(tc.actor), e.room(tc.room).ID,
				app.PostInput{Format: app.FormatText, Body: "hello"})
			if tc.want == "" {
				if err != nil {
					t.Fatalf("got %+v, want success", err)
				}
				if p.Author.ID != e.users[tc.actor].ID {
					t.Errorf("author = %+v", p.Author)
				}
				return
			}
			_ = requireTag(t, err, tc.want, routes[tc.want])
			if n := len(e.events()); n != before {
				t.Errorf("a failed call wrote %d events", n-before)
			}
			if page, err := e.posts.Feed(t.Context(), e.as("bob"), e.room(tc.room).ID, 0, 10); err != nil || len(page.Posts) != 0 {
				t.Errorf("a refused Create left posts: %v, %v", page.Posts, err)
			}
		})
	}
}

func TestPostsCreateMissingRoom(t *testing.T) {
	e := newPostsEnv(t)
	_, err := e.posts.Create(t.Context(), e.as("admin"), 9999, app.PostInput{Format: app.FormatText, Body: "x"})
	_ = requireTag(t, err, app.TagNotFound, notFoundMissing)
}

func TestPostsCreate(t *testing.T) {
	e := newPostsEnv(t)
	e.clk.Advance(5 * time.Minute)
	before := len(e.events())
	body := "Release **notes** go here\n"
	p, err := e.posts.Create(t.Context(), e.as("bob"), e.lobby.ID, app.PostInput{
		Format: app.FormatMarkdown, Body: body + "  \n", Tags: []string{"#Release", "Go", "release"},
	})
	if err != nil {
		t.Fatalf("Create: %+v", err)
	}
	if p.ID == 0 || p.RoomID != e.lobby.ID || p.RoomName != "lobby" {
		t.Errorf("post = %+v", p)
	}
	if want := (app.UserRef{ID: e.users["bob"].ID, Username: "bob", DisplayName: "Bob"}); p.Author != want {
		t.Errorf("Author = %+v, want %+v", p.Author, want)
	}
	if p.Format != app.FormatMarkdown || p.Body != "Release **notes** go here" {
		t.Errorf("Format, Body = %q, %q", p.Format, p.Body)
	}
	if !strings.Contains(string(p.HTML), "<strong>notes</strong>") {
		t.Errorf("HTML = %q, want the markdown rendered", p.HTML)
	}
	if !reflect.DeepEqual(p.Tags, []string{"release", "go"}) {
		t.Errorf("Tags = %q", p.Tags)
	}
	if !p.CreatedAt.Equal(e.clk.Now()) || p.EditedAt != nil || p.Deleted || p.CommentCount != 0 || !p.CanEdit || !p.CanDelete {
		t.Errorf("post = %+v", p)
	}
	if got := e.storedTags(p.ID); !reflect.DeepEqual(got, []string{"release", "go"}) {
		t.Errorf("stored tags = %q", got)
	}
	e.requireNewEvents(before, wantEvent{
		actor: "bob", action: app.ActionPostCreated, room: e.lobby.ID, entityType: app.EntityPost, entityID: p.ID,
		summary: "Bob posted in #lobby",
		details: map[string]any{"format": "markdown", "length": float64(len("Release **notes** go here")), "tags": []any{"release", "go"}},
	})
	e.requireNoBody("Release")
}

func TestPostsCreateReusesTags(t *testing.T) {
	e := newPostsEnv(t)
	a := e.write("bob", "lobby", "one", "shared")
	b := e.write("alice", "den", "two", "shared", "extra")
	if !reflect.DeepEqual(a.Tags, []string{"shared"}) || !reflect.DeepEqual(b.Tags, []string{"shared", "extra"}) {
		t.Errorf("tags = %q, %q", a.Tags, b.Tags)
	}
	// A second row for "shared" would break the unique index of the names.
	if got, err := e.posts.SuggestTags(t.Context(), e.as("bob"), ""); err != nil || !reflect.DeepEqual(got, []string{"extra", "shared"}) {
		t.Errorf("SuggestTags = %q, %v", got, err)
	}
}

func TestPostsHTMLByFormat(t *testing.T) {
	e := newPostsEnv(t)
	tests := []struct {
		name   string
		format app.Format
		body   string
		want   string
		not    string
	}{
		{"markdown renders", app.FormatMarkdown, "**bold** and `code`", "<strong>bold</strong>", ""},
		{"markdown drops scripts", app.FormatMarkdown, "<script>alert(1)</script>", "", "<script>"},
		{"text escapes", app.FormatText, "<b>not bold</b> **plain**", "&lt;b&gt;not bold&lt;/b&gt; **plain**", "<b>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := e.posts.Create(t.Context(), e.as("bob"), e.lobby.ID, app.PostInput{Format: tt.format, Body: tt.body})
			if err != nil {
				t.Fatalf("Create: %+v", err)
			}
			if tt.want != "" && !strings.Contains(string(p.HTML), tt.want) {
				t.Errorf("HTML = %q, want it to contain %q", p.HTML, tt.want)
			}
			if tt.not != "" && strings.Contains(string(p.HTML), tt.not) {
				t.Errorf("HTML = %q, must not contain %q", p.HTML, tt.not)
			}
			got, err := e.posts.Get(t.Context(), e.as("bob"), p.ID)
			if err != nil || got.HTML != p.HTML || got.Body != tt.body {
				t.Errorf("Get = %+v, %v", got, err)
			}
		})
	}
}

func TestPostsCreateValidation(t *testing.T) {
	long := strings.Repeat("a", 20_000)
	longUnicode := strings.Repeat("é", 20_000) // 40,000 bytes, 20,000 characters
	tagOf := func(n int) string { return strings.Repeat("a", n) }
	many := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("t%d", i)
		}
		return out
	}
	const (
		badTagMsg = "is not a tag: use 1 to 50 letters, digits, - or _."
		tooMany   = "At most 10 tags."
	)
	tests := []struct {
		name       string
		in         app.PostInput
		wantBody   string   // the stored body, for a success
		wantTags   []string // the stored tags, for a success
		wantFields map[string]string
	}{
		{name: "plain text", in: app.PostInput{Format: app.FormatText, Body: "hi"}, wantBody: "hi", wantTags: []string{}},
		{name: "trailing whitespace is dropped", in: app.PostInput{Format: app.FormatText, Body: "hi \n\t "}, wantBody: "hi", wantTags: []string{}},
		{name: "leading whitespace is kept", in: app.PostInput{Format: app.FormatMarkdown, Body: "  \n    code"}, wantBody: "  \n    code", wantTags: []string{}},
		{name: "20000 characters", in: app.PostInput{Format: app.FormatText, Body: long}, wantBody: long, wantTags: []string{}},
		{name: "20000 unicode characters", in: app.PostInput{Format: app.FormatText, Body: longUnicode}, wantBody: longUnicode, wantTags: []string{}},
		{name: "20000 characters and trailing space", in: app.PostInput{Format: app.FormatText, Body: long + "   "}, wantBody: long, wantTags: []string{}},
		{name: "empty body", in: app.PostInput{Format: app.FormatText}, wantFields: map[string]string{app.FieldBody: "Write something."}},
		{name: "blank body", in: app.PostInput{Format: app.FormatText, Body: " \n\t "}, wantFields: map[string]string{app.FieldBody: "Write something."}},
		{name: "20001 characters", in: app.PostInput{Format: app.FormatText, Body: long + "a"}, wantFields: map[string]string{app.FieldBody: "Use at most 20,000 characters."}},
		{name: "20001 unicode characters", in: app.PostInput{Format: app.FormatText, Body: longUnicode + "é"}, wantFields: map[string]string{app.FieldBody: "Use at most 20,000 characters."}},
		{name: "invalid utf-8", in: app.PostInput{Format: app.FormatText, Body: "a\xffb"}, wantFields: map[string]string{app.FieldBody: "Use valid text."}},
		{name: "unknown format", in: app.PostInput{Format: "html", Body: "x"}, wantFields: map[string]string{app.FieldFormat: "Choose text or markdown."}},
		{name: "no format", in: app.PostInput{Body: "x"}, wantFields: map[string]string{app.FieldFormat: "Choose text or markdown."}},

		{name: "tags are normalized", in: app.PostInput{Format: app.FormatText, Body: "x", Tags: []string{" #Go ", "RUST", "a-b_c9"}}, wantBody: "x", wantTags: []string{"go", "rust", "a-b_c9"}},
		{name: "tags lose duplicates", in: app.PostInput{Format: app.FormatText, Body: "x", Tags: []string{"go", "#GO", "Go", "x"}}, wantBody: "x", wantTags: []string{"go", "x"}},
		{name: "blank tags are skipped", in: app.PostInput{Format: app.FormatText, Body: "x", Tags: []string{"", "  ", "a"}}, wantBody: "x", wantTags: []string{"a"}},
		{name: "unicode letters", in: app.PostInput{Format: app.FormatText, Body: "x", Tags: []string{"Ünï", "日本語", "ΑΒΓ", "#Ж2"}}, wantBody: "x", wantTags: []string{"ünï", "日本語", "αβγ", "ж2"}},
		{name: "50 characters", in: app.PostInput{Format: app.FormatText, Body: "x", Tags: []string{tagOf(50)}}, wantBody: "x", wantTags: []string{tagOf(50)}},
		{name: "50 unicode characters", in: app.PostInput{Format: app.FormatText, Body: "x", Tags: []string{strings.Repeat("é", 50)}}, wantBody: "x", wantTags: []string{strings.Repeat("é", 50)}},
		{name: "10 tags", in: app.PostInput{Format: app.FormatText, Body: "x", Tags: many(10)}, wantBody: "x", wantTags: many(10)},
		{name: "10 tags and duplicates", in: app.PostInput{Format: app.FormatText, Body: "x", Tags: append(many(10), "T1", "#t2")}, wantBody: "x", wantTags: many(10)},
		{name: "51 characters", in: app.PostInput{Format: app.FormatText, Body: "x", Tags: []string{tagOf(51)}}, wantFields: map[string]string{app.FieldTags: "\"" + tagOf(20) + "…\" " + badTagMsg}},
		{name: "11 tags", in: app.PostInput{Format: app.FormatText, Body: "x", Tags: many(11)}, wantFields: map[string]string{app.FieldTags: tooMany}},
		{name: "a dot", in: app.PostInput{Format: app.FormatText, Body: "x", Tags: []string{"ok", "a.b"}}, wantFields: map[string]string{app.FieldTags: "\"a.b\" " + badTagMsg}},
		{name: "an inner space", in: app.PostInput{Format: app.FormatText, Body: "x", Tags: []string{"a b"}}, wantFields: map[string]string{app.FieldTags: "\"a b\" " + badTagMsg}},
		{name: "only a hash", in: app.PostInput{Format: app.FormatText, Body: "x", Tags: []string{"#"}}, wantFields: map[string]string{app.FieldTags: "\"#\" " + badTagMsg}},
		{name: "a symbol", in: app.PostInput{Format: app.FormatText, Body: "x", Tags: []string{"fire🔥"}}, wantFields: map[string]string{app.FieldTags: "\"fire🔥\" " + badTagMsg}},
		{name: "an invalid utf-8 tag", in: app.PostInput{Format: app.FormatText, Body: "x", Tags: []string{"a\xff"}}, wantFields: map[string]string{app.FieldTags: "\"a\uFFFD\" " + badTagMsg}},
		{
			name: "every problem at once",
			in:   app.PostInput{Format: "x", Body: "", Tags: []string{"a/b"}},
			wantFields: map[string]string{
				app.FieldFormat: "Choose text or markdown.",
				app.FieldBody:   "Write something.",
				app.FieldTags:   "\"a/b\" " + badTagMsg,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newPostsEnv(t)
			before := len(e.events())
			p, err := e.posts.Create(t.Context(), e.as("bob"), e.lobby.ID, tt.in)
			if tt.wantFields != nil {
				appErr := requireTag(t, err, app.TagInvalidInput, "Posts.Create.func1.InvalidInput")
				if got := fieldsOf(t, appErr); !reflect.DeepEqual(got, tt.wantFields) {
					t.Errorf("fields = %q, want %q", got, tt.wantFields)
				}
				if n := len(e.events()); n != before {
					t.Errorf("a failed call wrote %d events", n-before)
				}
				if page, err := e.posts.Feed(t.Context(), e.as("bob"), e.lobby.ID, 0, 30); err != nil || len(page.Posts) != 0 {
					t.Errorf("%d posts after a refused Create (%v)", len(page.Posts), err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Create: %+v", err)
			}
			if p.Body != tt.wantBody {
				t.Errorf("Body is %d characters, want %d", len([]rune(p.Body)), len([]rune(tt.wantBody)))
			}
			if !reflect.DeepEqual(p.Tags, tt.wantTags) || !reflect.DeepEqual(nonNilTags(e.storedTags(p.ID)), tt.wantTags) {
				t.Errorf("Tags = %q, stored %q, want %q", p.Tags, e.storedTags(p.ID), tt.wantTags)
			}
		})
	}
}

// nonNilTags returns s, or an empty slice for nil.
func nonNilTags(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func TestPostsEditPermissions(t *testing.T) {
	routes := map[string]string{
		app.TagNotFound:  notFoundPrivate,
		app.TagForbidden: "Posts.Edit.func1.Forbidden",
	}
	cases := []postCase{
		{"bob", "lobby", ""},
		{"admin", "lobby", app.TagForbidden}, {"root", "lobby", app.TagForbidden},
		{"alice", "lobby", app.TagForbidden}, {"carol", "lobby", app.TagForbidden},
		{"bob", "den", ""},
		{"alice", "den", app.TagForbidden},
		{"admin", "den", app.TagNotFound}, {"root", "den", app.TagNotFound}, {"carol", "den", app.TagNotFound},
	}
	runPostPermissions(t, routes, cases, func(e *postsEnv, a app.Actor, p app.Post) error {
		_, err := e.posts.Edit(t.Context(), a, p.ID, app.PostInput{Format: app.FormatText, Body: "changed"})
		return err
	})
}

func TestPostsEditMissingAndDeleted(t *testing.T) {
	e := newPostsEnv(t)
	in := app.PostInput{Format: app.FormatText, Body: "x"}
	_, err := e.posts.Edit(t.Context(), e.as("bob"), 9999, in)
	_ = requireTag(t, err, app.TagNotFound, "Posts.Edit.func1->Store.PostByID.NotFound")

	p := e.write("bob", "lobby", "gone soon")
	e.reply(p, "admin")
	if err := e.posts.Delete(t.Context(), e.as("bob"), p.ID); err != nil {
		t.Fatalf("Delete: %+v", err)
	}
	before := len(e.events())
	_, err = e.posts.Edit(t.Context(), e.as("bob"), p.ID, in)
	appErr := requireTag(t, err, app.TagNotFound, "Posts.Edit.func1.NotFound")
	if appErr.Metadata["reason"] != "deleted" {
		t.Errorf("metadata = %v", appErr.Metadata)
	}
	if n := len(e.events()); n != before {
		t.Errorf("a failed call wrote %d events", n-before)
	}
}

func TestPostsEditValidation(t *testing.T) {
	e := newPostsEnv(t)
	p := e.write("bob", "lobby", "original", "keep")
	before := len(e.events())
	_, err := e.posts.Edit(t.Context(), e.as("bob"), p.ID, app.PostInput{Format: "x", Body: " ", Tags: many11()})
	appErr := requireTag(t, err, app.TagInvalidInput, "Posts.Edit.func1.InvalidInput")
	fields := fieldsOf(t, appErr)
	if len(fields) != 3 {
		t.Errorf("fields = %q, want format, body and tags", fields)
	}
	if n := len(e.events()); n != before {
		t.Errorf("a failed call wrote %d events", n-before)
	}
	got, err := e.posts.Get(t.Context(), e.as("bob"), p.ID)
	if err != nil || got.Body != "original" || got.EditedAt != nil || !reflect.DeepEqual(got.Tags, []string{"keep"}) {
		t.Errorf("post after a refused Edit = %+v, %v", got, err)
	}
}

// many11 returns 11 distinct tags.
func many11() []string {
	out := make([]string, 11)
	for i := range out {
		out[i] = fmt.Sprintf("t%d", i)
	}
	return out
}

func TestPostsEditAudit(t *testing.T) {
	const secretOld, secretNew = "the old secret text", "the new secret text"
	tests := []struct {
		name    string
		in      app.PostInput
		details map[string]any // nil: no change, no event
		summary string
	}{
		{
			name:    "body",
			in:      app.PostInput{Format: app.FormatText, Body: secretNew, Tags: []string{"a", "b"}},
			details: map[string]any{"body": map[string]any{"bodyChanged": true, "length": float64(len(secretNew))}},
		},
		{
			name:    "format",
			in:      app.PostInput{Format: app.FormatMarkdown, Body: secretOld, Tags: []string{"a", "b"}},
			details: map[string]any{"format": map[string]any{"old": "text", "new": "markdown"}},
		},
		{
			name:    "tags",
			in:      app.PostInput{Format: app.FormatText, Body: secretOld, Tags: []string{"b", "c", "#D"}},
			details: map[string]any{"tags": map[string]any{"old": []any{"a", "b"}, "new": []any{"b", "c", "d"}}},
		},
		{
			name:    "tags removed",
			in:      app.PostInput{Format: app.FormatText, Body: secretOld},
			details: map[string]any{"tags": map[string]any{"old": []any{"a", "b"}, "new": []any{}}},
		},
		{
			name: "everything",
			in:   app.PostInput{Format: app.FormatMarkdown, Body: secretNew, Tags: []string{"z"}},
			details: map[string]any{
				"body":   map[string]any{"bodyChanged": true, "length": float64(len(secretNew))},
				"format": map[string]any{"old": "text", "new": "markdown"},
				"tags":   map[string]any{"old": []any{"a", "b"}, "new": []any{"z"}},
			},
		},
		{name: "nothing", in: app.PostInput{Format: app.FormatText, Body: secretOld, Tags: []string{"a", "b"}}},
		{name: "trailing whitespace only", in: app.PostInput{Format: app.FormatText, Body: secretOld + " \n", Tags: []string{"a", "b"}}},
		{name: "tags reordered and respelled", in: app.PostInput{Format: app.FormatText, Body: secretOld, Tags: []string{"#B", "A", "a"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newPostsEnv(t)
			p := e.write("bob", "lobby", secretOld, "a", "b")
			created := p.CreatedAt
			e.clk.Advance(time.Hour)
			before := len(e.events())
			got, err := e.posts.Edit(t.Context(), e.as("bob"), p.ID, tt.in)
			if err != nil {
				t.Fatalf("Edit: %+v", err)
			}
			if tt.details == nil {
				e.requireNewEvents(before)
				if got.EditedAt != nil {
					t.Errorf("EditedAt = %v after an edit that changed nothing", got.EditedAt)
				}
				if !reflect.DeepEqual(got.Tags, []string{"a", "b"}) {
					t.Errorf("Tags = %q, want the old order kept", got.Tags)
				}
				return
			}
			e.requireNewEvents(before, wantEvent{
				actor: "bob", action: app.ActionPostEdited, room: e.lobby.ID, entityType: app.EntityPost, entityID: p.ID,
				summary: "Bob edited a post in #lobby", details: tt.details,
			})
			if got.EditedAt == nil || !got.EditedAt.Equal(e.clk.Now()) || !got.CreatedAt.Equal(created) {
				t.Errorf("EditedAt = %v, CreatedAt = %v, want the clock's %v and %v", got.EditedAt, got.CreatedAt, e.clk.Now(), created)
			}
			wantBody := strings.TrimRight(tt.in.Body, " \n")
			if got.Body != wantBody || got.Format != tt.in.Format {
				t.Errorf("post = %q %q, want %q %q", got.Format, got.Body, tt.in.Format, wantBody)
			}
			if stored := nonNilTags(e.storedTags(p.ID)); !reflect.DeepEqual(stored, got.Tags) {
				t.Errorf("stored tags %q differ from the view's %q", stored, got.Tags)
			}
			e.requireNoBody("secret text")
		})
	}
}

func TestPostsDeletePermissions(t *testing.T) {
	routes := map[string]string{
		app.TagNotFound:  notFoundPrivate,
		app.TagForbidden: "Posts.Delete.1.Forbidden",
	}
	cases := []postCase{
		{"bob", "lobby", ""}, {"admin", "lobby", ""}, {"root", "lobby", ""},
		{"alice", "lobby", app.TagForbidden}, {"carol", "lobby", app.TagForbidden},
		{"bob", "den", ""}, {"alice", "den", app.TagForbidden},
		{"admin", "den", app.TagNotFound}, {"root", "den", app.TagNotFound}, {"carol", "den", app.TagNotFound},
	}
	runPostPermissions(t, routes, cases, func(e *postsEnv, a app.Actor, p app.Post) error {
		return e.posts.Delete(t.Context(), a, p.ID)
	})
}

func TestPostsDeleteMissing(t *testing.T) {
	e := newPostsEnv(t)
	err := e.posts.Delete(t.Context(), e.as("admin"), 9999)
	_ = requireTag(t, err, app.TagNotFound, "Posts.Delete.func1->Store.PostByID.NotFound")
}

func TestPostsDeleteWithoutRepliesRemovesTheRow(t *testing.T) {
	e := newPostsEnv(t)
	p := e.write("bob", "lobby", "private words", "x", "y")
	other := e.write("bob", "lobby", "other", "x")
	e.clk.Advance(time.Minute)
	before := len(e.events())
	if err := e.posts.Delete(t.Context(), e.as("admin"), p.ID); err != nil {
		t.Fatalf("Delete: %+v", err)
	}
	if _, err := e.st.PostByID(t.Context(), p.ID); err == nil {
		t.Error("the row is still there")
	}
	if got := e.storedTags(p.ID); len(got) != 0 {
		t.Errorf("tag links remain: %q", got)
	}
	if got := e.storedTags(other.ID); !reflect.DeepEqual(got, []string{"x"}) {
		t.Errorf("the other post's tags = %q", got)
	}
	_, err := e.posts.Get(t.Context(), e.as("bob"), p.ID)
	_ = requireTag(t, err, app.TagNotFound, missingPost)
	e.requireNewEvents(before, wantEvent{
		actor: "admin", action: app.ActionPostDeleted, room: e.lobby.ID, entityType: app.EntityPost, entityID: p.ID,
		summary: "Admin deleted a post in #lobby", details: map[string]any{"kept": false},
	})
	e.requireNoBody("private words")
	page, err := e.posts.Feed(t.Context(), e.as("bob"), e.lobby.ID, 0, 30)
	if err != nil || len(page.Posts) != 1 || page.Posts[0].ID != other.ID {
		t.Errorf("Feed = %+v, %v", page.Posts, err)
	}
}

func TestPostsDeleteWithRepliesKeepsAPlaceholder(t *testing.T) {
	e := newPostsEnv(t)
	p := e.write("bob", "lobby", "words that go away", "x", "y")
	e.write("bob", "lobby", "newer")
	reply := e.reply(p, "admin")
	e.clk.Advance(time.Minute)
	before := len(e.events())
	if err := e.posts.Delete(t.Context(), e.as("bob"), p.ID); err != nil {
		t.Fatalf("Delete: %+v", err)
	}

	row, err := e.st.PostByID(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("the row is gone: %+v", err)
	}
	if row.DeletedAt == nil || !row.DeletedAt.Equal(e.clk.Now()) || row.Body != "" {
		t.Errorf("row = %+v, want DeletedAt set and no body", row)
	}
	if got := e.storedTags(p.ID); len(got) != 0 {
		t.Errorf("tag links remain: %q", got)
	}
	if _, err := e.st.PostByID(t.Context(), reply.ID); err != nil {
		t.Errorf("the reply is gone: %+v", err)
	}
	e.requireNewEvents(before, wantEvent{
		actor: "bob", action: app.ActionPostDeleted, room: e.lobby.ID, entityType: app.EntityPost, entityID: p.ID,
		summary: "Bob deleted a post in #lobby", details: map[string]any{"kept": true},
	})
	e.requireNoBody("words that go away")

	// It shows as deleted, with no author, body, HTML or tags, and its comment
	// count; in the feed and on its own.
	page, err := e.posts.Feed(t.Context(), e.as("carol"), e.lobby.ID, 0, 30)
	if err != nil || len(page.Posts) != 2 {
		t.Fatalf("Feed = %+v, %v", page.Posts, err)
	}
	for name, got := range map[string]app.Post{"feed": page.Posts[1], "get": mustGet(t, e, "carol", p.ID)} {
		if got.ID != p.ID || !got.Deleted || got.Author != (app.UserRef{}) || got.Body != "" || got.HTML != "" ||
			len(got.Tags) != 0 || got.CommentCount != 1 || got.CanEdit || got.CanDelete {
			t.Errorf("%s: post = %+v, want a deleted placeholder with 1 comment", name, got)
		}
		if got.Tags == nil {
			t.Errorf("%s: Tags is nil, want empty", name)
		}
	}
	// Not even its author or an admin edits or deletes it again.
	for _, who := range []string{"bob", "admin"} {
		_, err := e.posts.Edit(t.Context(), e.as(who), p.ID, app.PostInput{Format: app.FormatText, Body: "x"})
		_ = requireTag(t, err, app.TagNotFound, "Posts.Edit.func1.NotFound")
		err = e.posts.Delete(t.Context(), e.as(who), p.ID)
		_ = requireTag(t, err, app.TagNotFound, "Posts.Delete.func1.NotFound")
	}
}

// mustGet returns the post as the user sees it.
func mustGet(t *testing.T, e *postsEnv, user string, id uint64) app.Post {
	t.Helper()
	p, err := e.posts.Get(t.Context(), e.as(user), id)
	if err != nil {
		t.Fatalf("Get: %+v", err)
	}
	return p
}

func TestPostsGetPermissions(t *testing.T) {
	routes := map[string]string{app.TagNotFound: notFoundPrivate}
	cases := []postCase{
		{"bob", "lobby", ""}, {"admin", "lobby", ""}, {"root", "lobby", ""}, {"alice", "lobby", ""}, {"carol", "lobby", ""},
		{"bob", "den", ""}, {"alice", "den", ""},
		{"admin", "den", app.TagNotFound}, {"root", "den", app.TagNotFound}, {"carol", "den", app.TagNotFound},
	}
	runPostPermissions(t, routes, cases, func(e *postsEnv, a app.Actor, p app.Post) error {
		got, err := e.posts.Get(t.Context(), a, p.ID)
		if err == nil && got.ID != p.ID {
			t.Errorf("Get returned post %d, want %d", got.ID, p.ID)
		}
		return err
	})
}

func TestPostsGetView(t *testing.T) {
	e := newPostsEnv(t)
	p := e.write("bob", "lobby", "hello", "x")
	e.reply(p, "admin")
	e.reply(p, "bob")
	tests := []struct {
		viewer     string
		canEdit    bool
		canDelete  bool
		wantAuthor string
	}{
		{"bob", true, true, "Bob"},
		{"admin", false, true, "Bob"},
		{"root", false, true, "Bob"},
		{"alice", false, false, "Bob"},
		{"carol", false, false, "Bob"},
	}
	for _, tt := range tests {
		t.Run(tt.viewer, func(t *testing.T) {
			got := mustGet(t, e, tt.viewer, p.ID)
			if got.CanEdit != tt.canEdit || got.CanDelete != tt.canDelete || got.Author.DisplayName != tt.wantAuthor {
				t.Errorf("post = %+v", got)
			}
			if got.CommentCount != 2 || got.RoomName != "lobby" || !reflect.DeepEqual(got.Tags, []string{"x"}) {
				t.Errorf("post = %+v, want 2 comments in lobby tagged x", got)
			}
		})
	}
}

func TestPostsFeedPermissions(t *testing.T) {
	routes := map[string]string{app.TagNotFound: notFoundPrivate}
	cases := []postCase{
		{"bob", "lobby", ""}, {"admin", "lobby", ""}, {"root", "lobby", ""}, {"alice", "lobby", ""}, {"carol", "lobby", ""},
		{"bob", "den", ""}, {"alice", "den", ""},
		{"admin", "den", app.TagNotFound}, {"root", "den", app.TagNotFound}, {"carol", "den", app.TagNotFound},
	}
	runPostPermissions(t, routes, cases, func(e *postsEnv, a app.Actor, p app.Post) error {
		page, err := e.posts.Feed(t.Context(), a, p.RoomID, 0, 30)
		if err == nil && (len(page.Posts) != 1 || page.Posts[0].ID != p.ID) {
			t.Errorf("Feed = %+v", page.Posts)
		}
		return err
	})
}

func TestPostsFeedMissingRoom(t *testing.T) {
	e := newPostsEnv(t)
	_, err := e.posts.Feed(t.Context(), e.as("admin"), 9999, 0, 30)
	_ = requireTag(t, err, app.TagNotFound, "Posts.Feed->loadReadableRoom->Store.RoomByID.NotFound")
}

func TestPostsFeedPaging(t *testing.T) {
	tests := []struct {
		total int
		limit int
		pages []int // the sizes of the pages, newest first
	}{
		{0, 0, []int{0}},
		{30, 0, []int{30}},
		{31, 0, []int{30, 1}},
		{61, 0, []int{30, 30, 1}},
		{61, 30, []int{30, 30, 1}},
		{7, 3, []int{3, 3, 1}},
		{6, 3, []int{3, 3}},
		{120, 100, []int{50, 50, 20}}, // above the largest page
		{5, -4, []int{5}},             // below 1 is the default
		{3, 1, []int{1, 1, 1}},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d posts, limit %d", tt.total, tt.limit), func(t *testing.T) {
			e := newPostsEnv(t)
			for i := 0; i < tt.total; i++ {
				// Through the store: Create is tested above, and this is about paging.
				row := store.Post{RoomID: e.lobby.ID, AuthorID: e.users["bob"].ID, Format: store.FormatText, Body: fmt.Sprintf("post %d", i)}
				if err := e.st.CreatePost(t.Context(), &row); err != nil {
					t.Fatalf("CreatePost: %+v", err)
				}
			}
			var seen []uint64
			var before uint64
			for i, size := range tt.pages {
				page, err := e.posts.Feed(t.Context(), e.as("carol"), e.lobby.ID, before, tt.limit)
				if err != nil {
					t.Fatalf("Feed page %d: %+v", i, err)
				}
				if len(page.Posts) != size {
					t.Fatalf("page %d has %d posts, want %d", i, len(page.Posts), size)
				}
				last := i == len(tt.pages)-1
				if page.HasNext == last {
					t.Errorf("page %d: HasNext = %v", i, page.HasNext)
				}
				if page.HasNext && page.NextBefore != page.Posts[len(page.Posts)-1].ID {
					t.Errorf("page %d: NextBefore = %d, want the last ID %d", i, page.NextBefore, page.Posts[len(page.Posts)-1].ID)
				}
				if !page.HasNext && page.NextBefore != 0 {
					t.Errorf("page %d: NextBefore = %d on the last page", i, page.NextBefore)
				}
				for _, p := range page.Posts {
					seen = append(seen, p.ID)
				}
				before = page.NextBefore
			}
			if len(seen) != tt.total {
				t.Fatalf("saw %d posts, want %d", len(seen), tt.total)
			}
			for i := 1; i < len(seen); i++ {
				if seen[i] >= seen[i-1] {
					t.Fatalf("posts are not newest first: %v", seen)
				}
			}
		})
	}
}

func TestPostsFeedContent(t *testing.T) {
	e := newPostsEnv(t)
	first := e.write("bob", "lobby", "first", "a", "b")
	e.write("alice", "den", "in another room", "secret")
	second := e.write("admin", "lobby", "second")
	third := e.write("bob", "lobby", "third", "c")
	// Comments are no posts of the feed, but they count: at any depth.
	c1 := e.reply(second, "bob")
	deeper := store.Post{
		RoomID: e.lobby.ID, AuthorID: e.users["admin"].ID, ParentID: &c1.ID, RootID: &second.ID,
		Format: store.FormatText, Body: "deeper",
	}
	if err := e.st.CreatePost(t.Context(), &deeper); err != nil {
		t.Fatal(err)
	}

	page, err := e.posts.Feed(t.Context(), e.as("carol"), e.lobby.ID, 0, 30)
	if err != nil {
		t.Fatalf("Feed: %+v", err)
	}
	type row struct {
		id       uint64
		author   string
		tags     []string
		comments int
	}
	var got []row
	for _, p := range page.Posts {
		got = append(got, row{p.ID, p.Author.Username, p.Tags, p.CommentCount})
	}
	want := []row{
		{third.ID, "bob", []string{"c"}, 0},
		{second.ID, "admin", []string{}, 2},
		{first.ID, "bob", []string{"a", "b"}, 0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("feed = %v, want %v", got, want)
	}
	if page.HasNext {
		t.Error("HasNext on a short feed")
	}
	for _, p := range page.Posts {
		if p.RoomID != e.lobby.ID || p.RoomName != "lobby" || p.HTML == "" || p.Body == "" {
			t.Errorf("post = %+v", p)
		}
	}
	// The post after beforeID, exclusive.
	page, err = e.posts.Feed(t.Context(), e.as("carol"), e.lobby.ID, second.ID, 30)
	if err != nil || len(page.Posts) != 1 || page.Posts[0].ID != first.ID {
		t.Errorf("Feed before %d = %+v, %v", second.ID, page.Posts, err)
	}
	// The viewer decides CanEdit and CanDelete.
	page, err = e.posts.Feed(t.Context(), e.as("bob"), e.lobby.ID, 0, 30)
	if err != nil {
		t.Fatal(err)
	}
	flags := []bool{}
	for _, p := range page.Posts {
		flags = append(flags, p.CanEdit, p.CanDelete)
	}
	if want := []bool{true, true, false, false, true, true}; !reflect.DeepEqual(flags, want) {
		t.Errorf("bob's CanEdit, CanDelete = %v, want %v", flags, want)
	}
}

func TestPostsSuggestTags(t *testing.T) {
	e := newPostsEnv(t)
	e.write("bob", "lobby", "1", "design", "desktop", "release", "a_b", "ünï")
	e.write("admin", "lobby", "2", "design", "DEBUG")
	e.write("alice", "den", "3", "design-private", "secret-plan", "design")
	e.write("bob", "den", "4", "dev-only")

	tests := []struct {
		name   string
		actor  string
		prefix string
		want   []string
	}{
		{"visible tags for a non-member", "carol", "de", []string{"debug", "design", "desktop"}},
		{"private tags for a member", "bob", "de", []string{"debug", "design", "design-private", "desktop", "dev-only"}},
		{"another member", "alice", "des", []string{"design", "design-private", "desktop"}},
		{"an admin who is no member", "root", "de", []string{"debug", "design", "desktop"}},
		{"a member of a private room only", "alice", "secret", []string{"secret-plan"}},
		{"not the tags of a room one cannot read", "carol", "secret", []string{}},
		{"normalized prefix", "bob", " #DES ", []string{"design", "design-private", "desktop"}},
		{"exact name", "carol", "design", []string{"design"}},
		{"no match", "bob", "zzz", []string{}},
		{"underscore is no wildcard", "bob", "_", []string{}},
		{"percent is no wildcard", "bob", "%", []string{}},
		{"underscore inside", "bob", "a_", []string{"a_b"}},
		{"unicode prefix", "carol", "Ü", []string{"ünï"}},
		{"empty prefix", "carol", "", []string{"a_b", "debug", "design", "desktop", "release", "ünï"}},
		{"only a hash", "carol", "#", []string{"a_b", "debug", "design", "desktop", "release", "ünï"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := e.posts.SuggestTags(t.Context(), e.as(tt.actor), tt.prefix)
			if err != nil {
				t.Fatalf("SuggestTags: %+v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("SuggestTags(%q) = %q, want %q", tt.prefix, got, tt.want)
			}
		})
	}
}

func TestPostsSuggestTagsAreLimitedToTen(t *testing.T) {
	e := newPostsEnv(t)
	for i := 0; i < 12; i += 6 {
		var names []string
		for j := i; j < i+6; j++ {
			names = append(names, fmt.Sprintf("t%02d", j))
		}
		e.write("bob", "lobby", "tags", names...)
	}
	got, err := e.posts.SuggestTags(t.Context(), e.as("carol"), "t")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"t00", "t01", "t02", "t03", "t04", "t05", "t06", "t07", "t08", "t09"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SuggestTags = %q, want the first ten %q", got, want)
	}
}

func TestPostsSuggestTagsForgetsDeletedPosts(t *testing.T) {
	e := newPostsEnv(t)
	gone := e.write("bob", "lobby", "gone", "gone-tag")
	kept := e.write("bob", "lobby", "kept", "kept-tag")
	e.reply(kept, "admin")
	if err := e.posts.Delete(t.Context(), e.as("bob"), gone.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.posts.Delete(t.Context(), e.as("bob"), kept.ID); err != nil {
		t.Fatal(err)
	}
	got, err := e.posts.SuggestTags(t.Context(), e.as("carol"), "")
	if err != nil || len(got) != 0 {
		t.Errorf("SuggestTags = %q, %v, want none: the posts are deleted", got, err)
	}
	// Editing away a tag forgets it as well.
	p := e.write("bob", "lobby", "tagged", "old-tag")
	if _, err := e.posts.Edit(t.Context(), e.as("bob"), p.ID, app.PostInput{Format: app.FormatText, Body: "tagged", Tags: []string{"new-tag"}}); err != nil {
		t.Fatal(err)
	}
	got, err = e.posts.SuggestTags(t.Context(), e.as("carol"), "")
	if err != nil || !reflect.DeepEqual(got, []string{"new-tag"}) {
		t.Errorf("SuggestTags = %q, %v, want new-tag only", got, err)
	}
}

func TestPostsGetMissing(t *testing.T) {
	e := newPostsEnv(t)
	_, err := e.posts.Get(t.Context(), e.as("admin"), 9999)
	_ = requireTag(t, err, app.TagNotFound, "Posts.Get->Store.PostByID.NotFound")
}
