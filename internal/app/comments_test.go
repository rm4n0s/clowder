package app_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
)

// say makes the user write a text comment under the post or comment parent and
// fails the test on an error.
func (e *postsEnv) say(user string, parent uint64, body string, tags ...string) app.Post {
	e.t.Helper()
	c, err := e.posts.Reply(e.t.Context(), e.as(user), parent,
		app.PostInput{Format: app.FormatText, Body: body, Tags: tags})
	if err != nil {
		e.t.Fatalf("Reply %q: %+v", body, err)
	}
	return c
}

// thread returns the thread of the post or comment as the user sees it.
func (e *postsEnv) thread(user string, id uint64) app.Thread {
	e.t.Helper()
	th, err := e.posts.Thread(e.t.Context(), e.as(user), id)
	if err != nil {
		e.t.Fatalf("Thread %d: %+v", id, err)
	}
	return th
}

// rowExists reports whether the post or comment is still in the database.
func (e *postsEnv) rowExists(id uint64) bool {
	e.t.Helper()
	_, err := e.st.PostByID(e.t.Context(), id)
	return err == nil
}

// runCommentPermissions has bob write a post in the room of each case and a
// comment under it, runs call as the actor of the case on a fresh fixture and
// checks the result. A failed call writes no audit event.
func runCommentPermissions(t *testing.T, routes map[string]string, cases []postCase, call func(e *postsEnv, a app.Actor, p, c app.Post) error) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.actor+" on "+tc.room, func(t *testing.T) {
			e := newPostsEnv(t)
			p := e.write("bob", tc.room, "bob's post")
			c := e.say("bob", p.ID, "bob's comment")
			before := len(e.events())
			err := call(e, e.as(tc.actor), p, c)
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

func TestCommentsReplyPermissions(t *testing.T) {
	routes := map[string]string{
		app.TagNotFound:  notFoundPrivate,
		app.TagForbidden: "Posts.Reply.func1.Forbidden",
	}
	cases := []postCase{
		{"admin", "lobby", ""}, {"bob", "lobby", ""},
		{"root", "lobby", app.TagForbidden}, {"alice", "lobby", app.TagForbidden}, {"carol", "lobby", app.TagForbidden},
		{"alice", "den", ""}, {"bob", "den", ""},
		{"admin", "den", app.TagNotFound}, {"root", "den", app.TagNotFound}, {"carol", "den", app.TagNotFound},
	}
	for _, under := range []string{"a post", "a comment"} {
		t.Run(under, func(t *testing.T) {
			runCommentPermissions(t, routes, cases, func(e *postsEnv, a app.Actor, p, c app.Post) error {
				parent := p
				if under == "a comment" {
					parent = c
				}
				before := len(e.events())
				c, err := e.posts.Reply(t.Context(), a, parent.ID, app.PostInput{Format: app.FormatText, Body: "an answer"})
				if err != nil {
					return err
				}
				if c.Author.ID != a.UserID || c.ParentID != parent.ID || c.RootID != p.ID {
					t.Errorf("comment = %+v", c)
				}
				if n := len(e.events()); n != before+1 {
					t.Errorf("Reply wrote %d events, want 1", n-before)
				}
				return nil
			})
		})
	}
}

func TestCommentsReplyMissing(t *testing.T) {
	e := newPostsEnv(t)
	before := len(e.events())
	_, err := e.posts.Reply(t.Context(), e.as("admin"), 9999, app.PostInput{Format: app.FormatText, Body: "x"})
	_ = requireTag(t, err, app.TagNotFound, "Posts.Reply.func1->Store.PostByID.NotFound")
	if n := len(e.events()); n != before {
		t.Errorf("a failed call wrote %d events", n-before)
	}
}

func TestCommentsReplyValidation(t *testing.T) {
	long := strings.Repeat("a", 20_000)
	many := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("t%d", i)
		}
		return out
	}
	const badTagMsg = "is not a tag: use 1 to 50 letters, digits, - or _."
	tests := []struct {
		name       string
		in         app.PostInput
		wantBody   string
		wantTags   []string
		wantFields map[string]string
	}{
		{name: "plain text", in: app.PostInput{Format: app.FormatText, Body: "hi"}, wantBody: "hi", wantTags: []string{}},
		{name: "trailing whitespace is dropped", in: app.PostInput{Format: app.FormatText, Body: "hi \n\t "}, wantBody: "hi", wantTags: []string{}},
		{name: "markdown keeps leading whitespace", in: app.PostInput{Format: app.FormatMarkdown, Body: "  \n    code"}, wantBody: "  \n    code", wantTags: []string{}},
		{name: "20000 characters", in: app.PostInput{Format: app.FormatText, Body: long}, wantBody: long, wantTags: []string{}},
		{name: "tags are normalized", in: app.PostInput{Format: app.FormatText, Body: "x", Tags: []string{" #Go ", "RUST", "go"}}, wantBody: "x", wantTags: []string{"go", "rust"}},
		{name: "10 tags", in: app.PostInput{Format: app.FormatText, Body: "x", Tags: many(10)}, wantBody: "x", wantTags: many(10)},
		{name: "empty body", in: app.PostInput{Format: app.FormatText}, wantFields: map[string]string{app.FieldBody: "Write something."}},
		{name: "blank body", in: app.PostInput{Format: app.FormatText, Body: " \n\t "}, wantFields: map[string]string{app.FieldBody: "Write something."}},
		{name: "20001 characters", in: app.PostInput{Format: app.FormatText, Body: long + "a"}, wantFields: map[string]string{app.FieldBody: "Use at most 20,000 characters."}},
		{name: "invalid utf-8", in: app.PostInput{Format: app.FormatText, Body: "a\xffb"}, wantFields: map[string]string{app.FieldBody: "Use valid text."}},
		{name: "unknown format", in: app.PostInput{Format: "html", Body: "x"}, wantFields: map[string]string{app.FieldFormat: "Choose text or markdown."}},
		{name: "no format", in: app.PostInput{Body: "x"}, wantFields: map[string]string{app.FieldFormat: "Choose text or markdown."}},
		{name: "11 tags", in: app.PostInput{Format: app.FormatText, Body: "x", Tags: many(11)}, wantFields: map[string]string{app.FieldTags: "At most 10 tags."}},
		{name: "a dot in a tag", in: app.PostInput{Format: app.FormatText, Body: "x", Tags: []string{"ok", "a.b"}}, wantFields: map[string]string{app.FieldTags: "\"a.b\" " + badTagMsg}},
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
	e := newPostsEnv(t)
	p := e.write("bob", "lobby", "the post")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := len(e.events())
			c, err := e.posts.Reply(t.Context(), e.as("bob"), p.ID, tt.in)
			if tt.wantFields != nil {
				appErr := requireTag(t, err, app.TagInvalidInput, "Posts.Reply.func1.InvalidInput")
				if got := fieldsOf(t, appErr); !reflect.DeepEqual(got, tt.wantFields) {
					t.Errorf("fields = %q, want %q", got, tt.wantFields)
				}
				if n := len(e.events()); n != before {
					t.Errorf("a failed call wrote %d events", n-before)
				}
				return
			}
			if err != nil {
				t.Fatalf("Reply: %+v", err)
			}
			if c.Body != tt.wantBody {
				t.Errorf("Body is %d characters, want %d", len([]rune(c.Body)), len([]rune(tt.wantBody)))
			}
			if !reflect.DeepEqual(c.Tags, tt.wantTags) || !reflect.DeepEqual(nonNilTags(e.storedTags(c.ID)), tt.wantTags) {
				t.Errorf("Tags = %q, stored %q, want %q", c.Tags, e.storedTags(c.ID), tt.wantTags)
			}
		})
	}
	// A refused reply left nothing behind: only the successful rows are there.
	ok := 0
	for _, tt := range tests {
		if tt.wantFields == nil {
			ok++
		}
	}
	if th := e.thread("bob", p.ID); len(th.Comments) != ok || th.Root.CommentCount != ok {
		t.Errorf("thread has %d comments (count %d), want %d", len(th.Comments), th.Root.CommentCount, ok)
	}
}

func TestCommentsReplyToDeletedRow(t *testing.T) {
	e := newPostsEnv(t)
	post := e.write("bob", "lobby", "the post")
	c1 := e.say("bob", post.ID, "first")
	e.say("admin", c1.ID, "second") // keeps c1 as a placeholder
	if err := e.posts.Delete(t.Context(), e.as("bob"), c1.ID); err != nil {
		t.Fatalf("Delete: %+v", err)
	}
	gone := e.write("bob", "lobby", "also going")
	e.say("admin", gone.ID, "a reply") // keeps the post as a placeholder
	if err := e.posts.Delete(t.Context(), e.as("bob"), gone.ID); err != nil {
		t.Fatalf("Delete: %+v", err)
	}
	for name, parent := range map[string]uint64{"a deleted comment": c1.ID, "a deleted post": gone.ID} {
		t.Run(name, func(t *testing.T) {
			before := len(e.events())
			_, err := e.posts.Reply(t.Context(), e.as("admin"), parent, app.PostInput{Format: app.FormatText, Body: "x"})
			appErr := requireTag(t, err, app.TagNotFound, "Posts.Reply.func1.NotFound")
			if appErr.Metadata["reason"] != "deleted" {
				t.Errorf("metadata = %v", appErr.Metadata)
			}
			if n := len(e.events()); n != before {
				t.Errorf("a failed call wrote %d events", n-before)
			}
		})
	}
}

func TestCommentsRootAndParentAtEveryDepth(t *testing.T) {
	e := newPostsEnv(t)
	post := e.write("bob", "lobby", "the post")
	chain := []app.Post{post}
	for depth := 1; depth <= 4; depth++ {
		parent := chain[len(chain)-1]
		chain = append(chain, e.say("bob", parent.ID, fmt.Sprintf("depth %d", depth)))
	}
	if post.IsComment || post.ParentID != 0 || post.RootID != 0 {
		t.Errorf("the post = %+v, want no parent and no root", post)
	}
	for depth := 1; depth <= 4; depth++ {
		c, parent := chain[depth], chain[depth-1]
		t.Run(fmt.Sprintf("depth %d", depth), func(t *testing.T) {
			if !c.IsComment || c.ParentID != parent.ID || c.RootID != post.ID {
				t.Errorf("view = %+v, want parent %d and root %d", c, parent.ID, post.ID)
			}
			row, err := e.st.PostByID(t.Context(), c.ID)
			if err != nil || row.ParentID == nil || *row.ParentID != parent.ID || row.RootID == nil || *row.RootID != post.ID {
				t.Errorf("row = %+v, %v", row, err)
			}
			got := mustGet(t, e, "carol", c.ID)
			if !got.IsComment || got.ParentID != parent.ID || got.RootID != post.ID || got.CommentCount != 0 {
				t.Errorf("Get = %+v", got)
			}
		})
	}
	// Only the post is in the feed, and it counts all four.
	page, err := e.posts.Feed(t.Context(), e.as("carol"), e.lobby.ID, 0, 30)
	if err != nil || len(page.Posts) != 1 || page.Posts[0].ID != post.ID || page.Posts[0].CommentCount != 4 {
		t.Errorf("Feed = %+v, %v", page.Posts, err)
	}
}

func TestCommentsReplyView(t *testing.T) {
	e := newPostsEnv(t)
	post := e.write("alice", "den", "the post")
	e.clk.Advance(3 * time.Minute)
	c, err := e.posts.Reply(t.Context(), e.as("bob"), post.ID,
		app.PostInput{Format: app.FormatMarkdown, Body: "an **answer**\n", Tags: []string{"#Go", "go", "Help"}})
	if err != nil {
		t.Fatalf("Reply: %+v", err)
	}
	if want := (app.UserRef{ID: e.users["bob"].ID, Username: "bob", DisplayName: "Bob"}); c.Author != want {
		t.Errorf("Author = %+v, want %+v", c.Author, want)
	}
	if c.RoomID != e.den.ID || c.RoomName != "den" || c.Format != app.FormatMarkdown || c.Body != "an **answer**" ||
		!strings.Contains(string(c.HTML), "<strong>answer</strong>") || !reflect.DeepEqual(c.Tags, []string{"go", "help"}) {
		t.Errorf("comment = %+v", c)
	}
	if !c.CreatedAt.Equal(e.clk.Now()) || c.EditedAt != nil || c.Deleted || !c.CanEdit || !c.CanDelete {
		t.Errorf("comment = %+v", c)
	}
}

func TestCommentsReplyAudit(t *testing.T) {
	e := newPostsEnv(t)
	post := e.write("alice", "den", "the post")
	e.clk.Advance(time.Minute)
	c1 := e.say("alice", post.ID, "first")
	before := len(e.events())
	e.clk.Advance(time.Minute)
	c2, err := e.posts.Reply(t.Context(), e.as("bob"), c1.ID,
		app.PostInput{Format: app.FormatMarkdown, Body: "a deeper secret answer", Tags: []string{"x", "y"}})
	if err != nil {
		t.Fatalf("Reply: %+v", err)
	}
	e.requireNewEvents(before, wantEvent{
		actor: "bob", action: app.ActionPostCreated, room: e.den.ID, entityType: app.EntityComment, entityID: c2.ID,
		summary: "Bob commented in #den",
		details: map[string]any{
			"format": "markdown", "length": float64(len("a deeper secret answer")), "tags": []any{"x", "y"},
			"rootID": float64(post.ID), "parentID": float64(c1.ID),
		},
	})
	e.requireNoBody("secret answer")
}

func TestCommentsEditAndDeleteAudit(t *testing.T) {
	e := newPostsEnv(t)
	post := e.write("bob", "lobby", "the post")
	c := e.say("bob", post.ID, "the old comment text", "a")
	e.say("admin", c.ID, "a reply")
	e.clk.Advance(time.Minute)

	before := len(e.events())
	edited, err := e.posts.Edit(t.Context(), e.as("bob"), c.ID, app.PostInput{Format: app.FormatText, Body: "the new comment text", Tags: []string{"a", "b"}})
	if err != nil {
		t.Fatalf("Edit: %+v", err)
	}
	if !edited.IsComment || edited.ParentID != post.ID || edited.RootID != post.ID || edited.EditedAt == nil {
		t.Errorf("edited = %+v", edited)
	}
	evs := e.events()[before:]
	if len(evs) != 1 || evs[0].Action != app.ActionPostEdited || evs[0].EntityType != app.EntityComment ||
		evs[0].EntityID != c.ID || evs[0].Summary != "Bob edited a comment in #lobby" {
		t.Fatalf("events = %+v", evs)
	}
	e.requireNoBody("comment text")

	before = len(e.events())
	if err := e.posts.Delete(t.Context(), e.as("admin"), c.ID); err != nil {
		t.Fatalf("Delete: %+v", err)
	}
	e.requireNewEvents(before, wantEvent{
		actor: "admin", action: app.ActionPostDeleted, room: e.lobby.ID, entityType: app.EntityComment, entityID: c.ID,
		summary: "Admin deleted a comment in #lobby", details: map[string]any{"kept": true},
	})
	e.requireNoBody("comment text")
}

func TestCommentsEditPermissions(t *testing.T) {
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
	runCommentPermissions(t, routes, cases, func(e *postsEnv, a app.Actor, p, c app.Post) error {
		_, err := e.posts.Edit(t.Context(), a, c.ID, app.PostInput{Format: app.FormatText, Body: "changed"})
		return err
	})
}

func TestCommentsDeletePermissions(t *testing.T) {
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
	runCommentPermissions(t, routes, cases, func(e *postsEnv, a app.Actor, p, c app.Post) error {
		return e.posts.Delete(t.Context(), a, c.ID)
	})
}

func TestCommentsGetPermissions(t *testing.T) {
	routes := map[string]string{app.TagNotFound: notFoundPrivate}
	cases := []postCase{
		{"bob", "lobby", ""}, {"admin", "lobby", ""}, {"root", "lobby", ""}, {"alice", "lobby", ""}, {"carol", "lobby", ""},
		{"bob", "den", ""}, {"alice", "den", ""},
		{"admin", "den", app.TagNotFound}, {"root", "den", app.TagNotFound}, {"carol", "den", app.TagNotFound},
	}
	runCommentPermissions(t, routes, cases, func(e *postsEnv, a app.Actor, p, c app.Post) error {
		got, err := e.posts.Get(t.Context(), a, c.ID)
		if err == nil && (got.ID != c.ID || !got.IsComment) {
			t.Errorf("Get returned %+v, want comment %d", got, c.ID)
		}
		return err
	})
}

func TestCommentsThreadPermissions(t *testing.T) {
	routes := map[string]string{app.TagNotFound: notFoundPrivate}
	cases := []postCase{
		{"bob", "lobby", ""}, {"admin", "lobby", ""}, {"root", "lobby", ""}, {"alice", "lobby", ""}, {"carol", "lobby", ""},
		{"bob", "den", ""}, {"alice", "den", ""},
		{"admin", "den", app.TagNotFound}, {"root", "den", app.TagNotFound}, {"carol", "den", app.TagNotFound},
	}
	for _, by := range []string{"the post", "a comment"} {
		t.Run(by, func(t *testing.T) {
			runCommentPermissions(t, routes, cases, func(e *postsEnv, a app.Actor, p, c app.Post) error {
				ask := p.ID
				if by == "a comment" {
					ask = c.ID
				}
				th, err := e.posts.Thread(t.Context(), a, ask)
				if err == nil && (th.Root.ID != p.ID || len(th.Comments) != 1 || th.Comments[0].ID != c.ID) {
					t.Errorf("Thread = %+v", th)
				}
				return err
			})
		})
	}
}

func TestCommentsThreadMissing(t *testing.T) {
	e := newPostsEnv(t)
	_, err := e.posts.Thread(t.Context(), e.as("admin"), 9999)
	_ = requireTag(t, err, app.TagNotFound, "Posts.Thread->Store.PostByID.NotFound")
}

func TestCommentsThreadOrder(t *testing.T) {
	// Each case writes its comments in the order given; a comment names its
	// parent by the label of an earlier one ("" for the post). The thread must
	// read depth first, siblings oldest first, whatever the write order.
	type spec struct{ label, parent string }
	tests := []struct {
		name  string
		specs []spec
		want  []string // "label:depth" in reading order
	}{
		{name: "no comments"},
		{name: "one comment", specs: []spec{{"a", ""}}, want: []string{"a:1"}},
		{name: "a chain", specs: []spec{{"a", ""}, {"b", "a"}, {"c", "b"}, {"d", "c"}}, want: []string{"a:1", "b:2", "c:3", "d:4"}},
		{name: "siblings", specs: []spec{{"a", ""}, {"b", ""}, {"c", ""}}, want: []string{"a:1", "b:1", "c:1"}},
		{
			name:  "interleaved branches",
			specs: []spec{{"a", ""}, {"b", ""}, {"c", "a"}, {"f", "b"}, {"d", "a"}, {"e", "c"}, {"g", ""}},
			want:  []string{"a:1", "c:2", "e:3", "d:2", "b:1", "f:2", "g:1"},
		},
		{
			name:  "a late reply to an old comment stays under it",
			specs: []spec{{"a", ""}, {"b", "a"}, {"c", ""}, {"d", "a"}, {"e", "b"}},
			want:  []string{"a:1", "b:2", "e:3", "d:2", "c:1"},
		},
		{
			name:  "deeper than the page indents",
			specs: []spec{{"a", ""}, {"b", "a"}, {"c", "b"}, {"d", "c"}, {"e", "d"}, {"f", "e"}, {"g", "f"}, {"h", "g"}, {"z", "a"}},
			want:  []string{"a:1", "b:2", "c:3", "d:4", "e:5", "f:6", "g:7", "h:8", "z:2"},
		},
	}
	e := newPostsEnv(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			post := e.write("bob", "lobby", "post of "+tt.name)
			ids := map[string]uint64{"": post.ID}
			labels := map[uint64]string{}
			for _, s := range tt.specs {
				c := e.say("bob", ids[s.parent], "comment "+s.label)
				ids[s.label] = c.ID
				labels[c.ID] = s.label
			}
			check := func(th app.Thread, focus uint64) {
				t.Helper()
				got := []string{}
				for _, c := range th.Comments {
					got = append(got, fmt.Sprintf("%s:%d", labels[c.ID], c.Depth))
					if c.ParentID != c.Post.ParentID || !c.IsComment || c.RootID != post.ID {
						t.Errorf("comment %s = %+v", labels[c.ID], c)
					}
				}
				if (len(got) != 0 || len(tt.want) != 0) && !reflect.DeepEqual(got, tt.want) {
					t.Errorf("thread = %v, want %v", got, tt.want)
				}
				if th.Root.ID != post.ID || th.Root.IsComment || th.Root.CommentCount != len(tt.specs) || th.Focus != focus {
					t.Errorf("Root, Focus = %+v, %d, want post %d, %d and %d comments", th.Root, th.Focus, post.ID, focus, len(tt.specs))
				}
			}
			check(e.thread("carol", post.ID), 0)
			// Asking by any comment gives the same thread, focused on it.
			for _, s := range tt.specs {
				check(e.thread("carol", ids[s.label]), ids[s.label])
			}
		})
	}
}

func TestCommentsThreadContent(t *testing.T) {
	e := newPostsEnv(t)
	post := e.write("alice", "den", "the post", "p")
	c1 := e.say("bob", post.ID, "from bob", "x", "y")
	c2 := e.say("alice", c1.ID, "from alice")
	c3 := e.say("bob", c2.ID, "from bob again")
	other := e.write("alice", "den", "another post")
	e.say("bob", other.ID, "not in this thread")
	// A deleted comment with a reply: its replies' ParentAuthor is empty.
	c4 := e.say("bob", post.ID, "going away")
	c5 := e.say("alice", c4.ID, "reply to a deleted one")
	if err := e.posts.Delete(t.Context(), e.as("bob"), c4.ID); err != nil {
		t.Fatal(err)
	}

	th := e.thread("alice", c3.ID)
	if th.Focus != c3.ID || th.Root.ID != post.ID || !reflect.DeepEqual(th.Root.Tags, []string{"p"}) || th.Root.CommentCount != 4 {
		t.Errorf("Root, Focus = %+v, %d", th.Root, th.Focus)
	}
	wantIDs := []uint64{c1.ID, c2.ID, c3.ID, c4.ID, c5.ID}
	got := []uint64{}
	for _, c := range th.Comments {
		got = append(got, c.ID)
	}
	if !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("comments = %v, want %v", got, wantIDs)
	}
	bob := app.UserRef{ID: e.users["bob"].ID, Username: "bob", DisplayName: "Bob"}
	alice := app.UserRef{ID: e.users["alice"].ID, Username: "alice", DisplayName: "Alice"}
	tests := []struct {
		name         string
		c            app.Comment
		depth        int
		parentID     uint64
		parentAuthor app.UserRef
		author       app.UserRef
		tags         []string
		canEdit      bool
		canDelete    bool
		deleted      bool
	}{
		{"reply to the post", th.Comments[0], 1, post.ID, alice, bob, []string{"x", "y"}, false, false, false},
		{"reply to a comment", th.Comments[1], 2, c1.ID, bob, alice, []string{}, true, true, false},
		{"third level", th.Comments[2], 3, c2.ID, alice, bob, []string{}, false, false, false},
		{"deleted comment", th.Comments[3], 1, post.ID, alice, app.UserRef{}, []string{}, false, false, true},
		{"reply to a deleted comment", th.Comments[4], 2, c4.ID, app.UserRef{}, alice, []string{}, true, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.c
			if c.Depth != tt.depth || c.ParentID != tt.parentID || c.ParentAuthor != tt.parentAuthor || c.Author != tt.author {
				t.Errorf("depth %d, parent %d, parent author %+v, author %+v; want %d, %d, %+v, %+v",
					c.Depth, c.ParentID, c.ParentAuthor, c.Author, tt.depth, tt.parentID, tt.parentAuthor, tt.author)
			}
			if !reflect.DeepEqual(c.Tags, tt.tags) || c.CanEdit != tt.canEdit || c.CanDelete != tt.canDelete || c.Deleted != tt.deleted {
				t.Errorf("tags %q, CanEdit %v, CanDelete %v, Deleted %v; want %q, %v, %v, %v",
					c.Tags, c.CanEdit, c.CanDelete, c.Deleted, tt.tags, tt.canEdit, tt.canDelete, tt.deleted)
			}
			if c.RoomName != "den" || c.RoomID != e.den.ID {
				t.Errorf("room = %d %q", c.RoomID, c.RoomName)
			}
			if tt.deleted && (c.Body != "" || c.HTML != "") || !tt.deleted && (c.Body == "" || c.HTML == "") {
				t.Errorf("Body, HTML = %q, %q", c.Body, c.HTML)
			}
		})
	}
	// The viewer decides CanEdit and CanDelete: the roots' too.
	if th := e.thread("bob", post.ID); th.Root.CanEdit || th.Root.CanDelete {
		t.Errorf("bob may change alice's post: %+v", th.Root)
	}
}

func TestCommentsThreadWithADeletedRoot(t *testing.T) {
	e := newPostsEnv(t)
	post := e.write("bob", "lobby", "the post", "x")
	c1 := e.say("admin", post.ID, "a comment")
	if err := e.posts.Delete(t.Context(), e.as("bob"), post.ID); err != nil {
		t.Fatal(err)
	}
	th := e.thread("carol", c1.ID)
	if !th.Root.Deleted || th.Root.ID != post.ID || th.Root.Author != (app.UserRef{}) || th.Root.Body != "" || th.Root.CommentCount != 1 {
		t.Errorf("Root = %+v, want a deleted placeholder with one comment", th.Root)
	}
	if len(th.Comments) != 1 || th.Comments[0].ParentAuthor != (app.UserRef{}) || th.Comments[0].ParentID != post.ID || th.Focus != c1.ID {
		t.Errorf("Comments, Focus = %+v, %d", th.Comments, th.Focus)
	}
}

func TestCommentsDeleteKeepsOrRemoves(t *testing.T) {
	e := newPostsEnv(t)
	post := e.write("bob", "lobby", "the post")
	kept := e.say("bob", post.ID, "kept words", "k")
	e.say("admin", kept.ID, "reply to kept")
	removed := e.say("bob", post.ID, "removed words", "r")
	e.clk.Advance(time.Minute)

	before := len(e.events())
	if err := e.posts.Delete(t.Context(), e.as("bob"), kept.ID); err != nil {
		t.Fatalf("Delete kept: %+v", err)
	}
	row, err := e.st.PostByID(t.Context(), kept.ID)
	if err != nil || row.DeletedAt == nil || !row.DeletedAt.Equal(e.clk.Now()) || row.Body != "" {
		t.Errorf("row = %+v, %v, want a placeholder", row, err)
	}
	if got := e.storedTags(kept.ID); len(got) != 0 {
		t.Errorf("tag links remain: %q", got)
	}

	if err := e.posts.Delete(t.Context(), e.as("admin"), removed.ID); err != nil {
		t.Fatalf("Delete removed: %+v", err)
	}
	if e.rowExists(removed.ID) || len(e.storedTags(removed.ID)) != 0 {
		t.Error("the comment without replies is still there")
	}
	e.requireNewEvents(before,
		wantEvent{
			actor: "bob", action: app.ActionPostDeleted, room: e.lobby.ID, entityType: app.EntityComment, entityID: kept.ID,
			summary: "Bob deleted a comment in #lobby", details: map[string]any{"kept": true},
		},
		wantEvent{
			actor: "admin", action: app.ActionPostDeleted, room: e.lobby.ID, entityType: app.EntityComment, entityID: removed.ID,
			summary: "Admin deleted a comment in #lobby", details: map[string]any{"kept": false},
		})
	e.requireNoBody("words")

	th := e.thread("carol", post.ID)
	if len(th.Comments) != 2 || th.Comments[0].ID != kept.ID || !th.Comments[0].Deleted || th.Comments[1].Deleted {
		t.Errorf("thread = %+v", th.Comments)
	}
	// A kept placeholder is gone for editing, deleting and answering.
	for _, who := range []string{"bob", "admin"} {
		_, err := e.posts.Edit(t.Context(), e.as(who), kept.ID, app.PostInput{Format: app.FormatText, Body: "x"})
		_ = requireTag(t, err, app.TagNotFound, "Posts.Edit.func1.NotFound")
		err = e.posts.Delete(t.Context(), e.as(who), kept.ID)
		_ = requireTag(t, err, app.TagNotFound, "Posts.Delete.func1.NotFound")
	}
	_, err = e.posts.Get(t.Context(), e.as("carol"), removed.ID)
	_ = requireTag(t, err, app.TagNotFound, missingPost)
}

func TestCommentsDeleteCleansUpPlaceholders(t *testing.T) {
	// A thread is a list of comments: label, parent label ("" for the post)
	// and author. Then the labels in "deleted" are deleted in order, and after
	// each deletion the labels in "gone" must be removed from the database and
	// the labels in "left" must remain.
	type spec struct{ label, parent string }
	type step struct {
		delete string
		kept   bool     // the deleted row stays as a placeholder
		gone   []string // rows that no longer exist after this step ("post" is the post)
		left   []string // rows that must still exist
	}
	tests := []struct {
		name  string
		specs []spec
		steps []step
	}{
		{
			name:  "a leaf next to live comments goes alone",
			specs: []spec{{"a", ""}, {"b", "a"}, {"c", ""}},
			steps: []step{{delete: "b", gone: []string{"b"}, left: []string{"post", "a", "c"}}},
		},
		{
			name:  "the chain of placeholders goes up to the post",
			specs: []spec{{"a", ""}, {"b", "a"}, {"c", "b"}},
			steps: []step{
				{delete: "a", kept: true, left: []string{"post", "a", "b", "c"}},
				{delete: "post", kept: true, left: []string{"post", "a", "b", "c"}},
				{delete: "b", kept: true, left: []string{"post", "a", "b", "c"}},
				{delete: "c", gone: []string{"post", "a", "b", "c"}},
			},
		},
		{
			name:  "the chain stops at a live comment",
			specs: []spec{{"a", ""}, {"b", "a"}, {"c", "b"}},
			steps: []step{
				{delete: "b", kept: true, left: []string{"post", "a", "b", "c"}},
				{delete: "c", gone: []string{"b", "c"}, left: []string{"post", "a"}},
			},
		},
		{
			name:  "the chain stops at a placeholder that has another reply",
			specs: []spec{{"a", ""}, {"b", "a"}, {"c", "a"}},
			steps: []step{
				{delete: "a", kept: true, left: []string{"post", "a", "b", "c"}},
				{delete: "b", gone: []string{"b"}, left: []string{"post", "a", "c"}},
				{delete: "c", gone: []string{"a", "c"}, left: []string{"post"}},
			},
		},
		{
			name:  "the chain stops at a live post",
			specs: []spec{{"a", ""}, {"b", "a"}},
			steps: []step{
				{delete: "a", kept: true, left: []string{"post", "a", "b"}},
				{delete: "b", gone: []string{"a", "b"}, left: []string{"post"}},
			},
		},
		{
			name:  "a placeholder post with a live comment stays",
			specs: []spec{{"a", ""}, {"b", ""}},
			steps: []step{
				{delete: "post", kept: true, left: []string{"post", "a", "b"}},
				{delete: "a", gone: []string{"a"}, left: []string{"post", "b"}},
				{delete: "b", gone: []string{"post", "b"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newPostsEnv(t)
			post := e.write("bob", "lobby", "the post", "t")
			ids := map[string]uint64{"post": post.ID, "": post.ID}
			for _, s := range tt.specs {
				ids[s.label] = e.say("bob", ids[s.parent], "comment "+s.label, "t").ID
			}
			for _, st := range tt.steps {
				before := len(e.events())
				if err := e.posts.Delete(t.Context(), e.as("bob"), ids[st.delete]); err != nil {
					t.Fatalf("Delete %s: %+v", st.delete, err)
				}
				entity := app.EntityComment
				if st.delete == "post" {
					entity = app.EntityPost
				}
				// Exactly one event, for the row that was deleted, whatever
				// the cleanup removed.
				e.requireNewEvents(before, wantEvent{
					actor: "bob", action: app.ActionPostDeleted, room: e.lobby.ID, entityType: entity, entityID: ids[st.delete],
					summary: "Bob deleted a " + map[bool]string{true: "post", false: "comment"}[st.delete == "post"] + " in #lobby",
					details: map[string]any{"kept": st.kept},
				})
				for _, label := range st.gone {
					if e.rowExists(ids[label]) {
						t.Errorf("after deleting %s: %s is still there", st.delete, label)
					}
					if len(e.storedTags(ids[label])) != 0 {
						t.Errorf("after deleting %s: %s keeps its tags", st.delete, label)
					}
				}
				for _, label := range st.left {
					if !e.rowExists(ids[label]) {
						t.Errorf("after deleting %s: %s is gone", st.delete, label)
					}
				}
			}
			// The last state shows in the feed: the post is there unless the
			// chain removed it.
			page, err := e.posts.Feed(t.Context(), e.as("carol"), e.lobby.ID, 0, 30)
			if err != nil {
				t.Fatal(err)
			}
			last := tt.steps[len(tt.steps)-1]
			postGone := false
			for _, label := range last.gone {
				postGone = postGone || label == "post"
			}
			if postGone != (len(page.Posts) == 0) {
				t.Errorf("Feed = %+v, post removed = %v", page.Posts, postGone)
			}
		})
	}
}

func TestCommentsCountsExcludeDeleted(t *testing.T) {
	e := newPostsEnv(t)
	post := e.write("bob", "lobby", "the post")
	c1 := e.say("bob", post.ID, "c1")
	c2 := e.say("admin", c1.ID, "c2")
	c3 := e.say("bob", c2.ID, "c3")
	leaf := e.say("admin", post.ID, "leaf")
	lonely := e.write("bob", "lobby", "no comments")

	counts := func() (feed, get, thread int) {
		t.Helper()
		page, err := e.posts.Feed(t.Context(), e.as("carol"), e.lobby.ID, 0, 30)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range page.Posts {
			switch p.ID {
			case post.ID:
				feed = p.CommentCount
			case lonely.ID:
				if p.CommentCount != 0 {
					t.Errorf("the post without comments counts %d", p.CommentCount)
				}
			}
		}
		return feed, mustGet(t, e, "carol", post.ID).CommentCount, e.thread("carol", post.ID).Root.CommentCount
	}
	tests := []struct {
		name   string
		delete app.Post
		by     string
		want   int
	}{
		{"nothing deleted", app.Post{}, "", 4},
		{"a leaf is removed", leaf, "admin", 3},
		{"a comment with replies is kept but no longer counts", c1, "bob", 2},
		{"a leaf under a placeholder is removed", c3, "bob", 1},
		{"the last comment goes with the placeholders", c2, "admin", 0},
	}
	for _, tt := range tests {
		if tt.delete.ID != 0 {
			if err := e.posts.Delete(t.Context(), e.as(tt.by), tt.delete.ID); err != nil {
				t.Fatalf("%s: Delete: %+v", tt.name, err)
			}
		}
		if feed, get, thread := counts(); feed != tt.want || get != tt.want || thread != tt.want {
			t.Errorf("%s: counts in the feed, Get and Thread = %d, %d, %d, want %d", tt.name, feed, get, thread, tt.want)
		}
	}
	// The cleanup removed the placeholder as well.
	if e.rowExists(c1.ID) {
		t.Error("c1 should be gone after the last comment was removed")
	}
}
