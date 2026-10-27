package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/store"
)

// writePost makes a post of the user in the room through the service and fails
// the test on an error.
func (e *roomsEnv) writePost(user, room, body string, tags ...string) app.Post {
	e.t.Helper()
	p, err := e.posts.Create(e.t.Context(), e.actor(e.users(user)), e.room(room).ID,
		app.PostInput{Format: app.FormatText, Body: body, Tags: tags})
	if err != nil {
		e.t.Fatalf("Create %q: %+v", body, err)
	}
	return p
}

// writeMarkdown is writePost with a markdown body.
func (e *roomsEnv) writeMarkdown(user, room, body string, tags ...string) app.Post {
	e.t.Helper()
	p, err := e.posts.Create(e.t.Context(), e.actor(e.users(user)), e.room(room).ID,
		app.PostInput{Format: app.FormatMarkdown, Body: body, Tags: tags})
	if err != nil {
		e.t.Fatalf("Create %q: %+v", body, err)
	}
	return p
}

// reply inserts a comment under the post through the store, as step 5.1 will
// through the service.
func (e *roomsEnv) reply(parent app.Post, user string) {
	e.t.Helper()
	root := parent.ID
	c := store.Post{
		RoomID: parent.RoomID, AuthorID: e.users(user).ID, ParentID: &parent.ID, RootID: &root,
		Format: store.FormatText, Body: "a reply",
	}
	if err := e.st.CreatePost(e.t.Context(), &c); err != nil {
		e.t.Fatalf("CreatePost reply: %+v", err)
	}
}

// writeMany makes n posts of the user in the room, oldest first.
func (e *roomsEnv) writeMany(user, room string, n int) []app.Post {
	e.t.Helper()
	made := make([]app.Post, 0, n)
	for i := 0; i < n; i++ {
		made = append(made, e.writePost(user, room, "post number "+strconv.Itoa(i)))
	}
	return made
}

// feed returns the posts of the room as the user sees them, newest first.
func (e *roomsEnv) feed(user, room string) []app.Post {
	e.t.Helper()
	page, err := e.posts.Feed(e.t.Context(), e.actor(e.users(user)), e.room(room).ID, 0, 50)
	if err != nil {
		e.t.Fatalf("Feed: %+v", err)
	}
	return page.Posts
}

// getAs sends a GET as the user, plain or by Datastar.
func (e *roomsEnv) getAs(user, path string, datastar bool) *httptest.ResponseRecorder {
	e.t.Helper()
	opts := []reqOpt{e.cookies[user]}
	if datastar {
		opts = append(opts, asDatastar())
	}
	return e.send(get(path, opts...))
}

// postForm is the form the editor sends.
func editorForm(format, body, tags string) url.Values {
	return url.Values{"format": {format}, "body": {body}, "tags": {tags}}
}

// postPath is the path of an action on a post.
func postPathOf(p app.Post, action string) string {
	path := "/posts/" + idText(p.ID)
	if action != "" {
		path += "/" + action
	}
	return path
}

func TestPostCreate(t *testing.T) {
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		rec := e.post("alice", "/rooms/"+idText(e.general.ID)+"/posts",
			editorForm("markdown", "Hello **team**", "design, Release"), datastar)
		posts := e.feed("alice", "general")
		if len(posts) != 1 {
			t.Fatalf("%d posts after the create, want 1", len(posts))
		}
		p := posts[0]
		if p.Body != "Hello **team**" || p.Format != app.FormatMarkdown || strings.Join(p.Tags, ",") != "design,release" || p.Author.Username != "alice" {
			t.Errorf("stored post = %+v", p)
		}
		if !datastar {
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/rooms/"+idText(e.general.ID) {
				t.Fatalf("answer = %d %q, want 303 to the room", rec.Code, rec.Header().Get("Location"))
			}
			return
		}
		requireSSE(t, rec)
		requireBody(t, rec, []string{
			"data: selector #feed-posts", "data: mode prepend",
			`id="post-` + idText(p.ID) + `"`, "<strong>team</strong>", `href="/search?q=%23design"`, `href="/search?q=%23release"`,
			// A fresh editor replaces the old one, empty.
			"data: selector #post-editor-new", "data: mode replace", `id="post-editor-new"`, `action="/rooms/` + idText(e.general.ID) + `/posts"`,
		}, "Hello **team**")
	})
}

func TestPostCreateMistakes(t *testing.T) {
	tags11 := "t1 t2 t3 t4 t5 t6 t7 t8 t9 t10 t11"
	tests := []struct {
		name  string
		form  url.Values
		want  []string // the message and the input kept
		field string
	}{
		{
			name: "empty body", form: editorForm("text", " \n ", "keep-me"),
			want: []string{"Write something.", `id="post-body-new-error"`, `value="keep-me"`, `value="text" checked`, `aria-invalid="true"`},
		},
		{
			name: "too many tags", form: editorForm("markdown", "Some **body**", tags11),
			want: []string{"At most 10 tags.", `id="post-tags-new-error"`, `value="` + tags11 + `"`, ">Some **body**</textarea>"},
		},
		{
			name: "a bad tag", form: editorForm("markdown", "Some body", "good bad!tag"),
			want: []string{"is not a tag: use 1 to 50 letters, digits, - or _.", `value="good bad!tag"`, ">Some body</textarea>"},
		},
		{
			name: "no format", form: url.Values{"body": {"Some body"}},
			want: []string{"Choose text or markdown.", `id="post-format-new-error"`, ">Some body</textarea>"},
		},
		{
			name: "everything at once", form: editorForm("rich", "", "!!"),
			want: []string{"Choose text or markdown.", "Write something.", "is not a tag", `value="!!"`},
		},
	}
	for _, tt := range tests {
		modes(t, func(t *testing.T, datastar bool) {
			e := newRoomsEnv(t)
			rec := e.post("alice", "/rooms/"+idText(e.general.ID)+"/posts", tt.form, datastar)
			if datastar {
				requireSSE(t, rec)
				requireBody(t, rec, append([]string{`id="post-editor-new"`}, tt.want...), "data: mode prepend")
			} else {
				if rec.Code != http.StatusUnprocessableEntity {
					t.Fatalf("status = %d, want 422; body:\n%s", rec.Code, rec.Body)
				}
				// The whole page: the sidebar, the room and the editor.
				requireBody(t, rec, append([]string{`id="sidebar-rooms"`, `id="room-header"`, `id="post-editor-new"`}, tt.want...))
			}
			if n := len(e.feed("alice", "general")); n != 0 {
				t.Errorf("%d posts after a refused create", n)
			}
		})
	}
}

func TestPostCreateNotAllowed(t *testing.T) {
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		form := editorForm("text", "let me in", "")
		// A visible room that carol has not joined: she reads it, but may not post.
		rec := e.post("carol", "/rooms/"+idText(e.general.ID)+"/posts", form, datastar)
		if datastar {
			requireSSE(t, rec)
			requireBody(t, rec, []string{"You are not allowed to do that", `id="post-editor-new"`})
		} else {
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", rec.Code)
			}
			requireBody(t, rec, []string{"You are not allowed to do that", "Join this room to post"}, `id="post-editor-new"`)
		}
		if n := len(e.feed("alice", "general")); n != 0 {
			t.Errorf("%d posts after a refused create", n)
		}
		// A private room of others is not found: carol, and the admin too.
		for _, user := range []string{"carol", "admin"} {
			rec := e.post(user, "/rooms/"+idText(e.secret.ID)+"/posts", form, datastar)
			requireNotFound(t, rec, datastar, "/rooms/"+idText(e.secret.ID))
		}
		if n := len(e.feed("alice", "secret")); n != 0 {
			t.Errorf("%d posts in the private room after refused creates", n)
		}
	})
}

func TestPostPreview(t *testing.T) {
	tooLong := strings.Repeat("a", 20_001)
	tests := []struct {
		name    string
		form    url.Values
		want    []string
		notWant []string
	}{
		{
			name: "markdown is rendered", form: url.Values{"editor": {"new"}, "format": {"markdown"}, "body": {"Hi **there**\n\n- one\n- two"}},
			want: []string{`id="post-preview-new"`, "<strong>there</strong>", "<li>one</li>", `class="post-preview"`}, notWant: []string{`class="post-preview" hidden`, "Nothing to preview"},
		},
		{
			name: "markdown drops scripts", form: url.Values{"editor": {"new"}, "format": {"markdown"}, "body": {"<script>alert(1)</script>\n\nok"}},
			want: []string{"<p>ok</p>"}, notWant: []string{"<script>"},
		},
		{
			name: "text is escaped", form: url.Values{"editor": {"new"}, "format": {"text"}, "body": {"<b>bold</b> **plain**\nnext"}},
			want: []string{"&lt;b&gt;bold&lt;/b&gt; **plain**", "<br>"}, notWant: []string{"<b>", "<strong>"},
		},
		{
			name: "the box of the editor that asked", form: url.Values{"editor": {"edit-12"}, "format": {"text"}, "body": {"x"}},
			want: []string{`id="post-preview-edit-12"`},
		},
		{
			name: "an editor id that is not one", form: url.Values{"editor": {`"><script>`}, "format": {"text"}, "body": {"x"}},
			want: []string{`id="post-preview-new"`}, notWant: []string{"<script>"},
		},
		{
			name: "an empty body", form: url.Values{"editor": {"new"}, "format": {"text"}, "body": {"  "}},
			want: []string{"Nothing to preview."},
		},
		{
			name: "a body that is too long", form: url.Values{"editor": {"new"}, "format": {"text"}, "body": {tooLong}},
			want: []string{"Use at most 20,000 characters.", `role="alert"`}, notWant: []string{"aaaaaaaa"},
		},
		{
			name: "a format that is not one", form: url.Values{"editor": {"new"}, "format": {"html"}, "body": {"x"}},
			want: []string{"Choose text or markdown."},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newRoomsEnv(t)
			// Carol is a member of no room: any logged-in person may preview.
			rec := e.post("carol", "/preview", tt.form, true)
			requireSSE(t, rec)
			requireBody(t, rec, tt.want, tt.notWant...)
		})
	}
}

func TestPostPreviewNeedsALogin(t *testing.T) {
	e := newRoomsEnv(t)
	rec := e.send(postForm("/preview", url.Values{"format": {"text"}, "body": {"x"}}, asDatastar()))
	requireSSE(t, rec)
	requireBody(t, rec, []string{redirectScript("/login")}, "<p>")
}

func TestTagSuggest(t *testing.T) {
	e := newRoomsEnv(t)
	e.writePost("bob", "general", "1", "design", "desktop", "release")
	e.writePost("alice", "secret", "2", "design-private", "secret-plan")
	tests := []struct {
		name    string
		user    string
		query   string
		want    []string
		notWant []string
	}{
		{"a prefix", "carol", "de", []string{">#design<", ">#desktop<", `data-tag="design"`}, []string{"release", "secret-plan", "design-private"}},
		{"the private tags of a member", "alice", "de", []string{">#design<", ">#design-private<"}, nil},
		{"not the private tags of someone outside", "carol", "secret", nil, []string{"secret-plan", "<li>"}},
		{"not even for an admin who is no member", "admin", "secret", nil, []string{"secret-plan", "<li>"}},
		{"only the last piece counts", "carol", "release design, rel", []string{">#release<"}, []string{"design"}},
		{"the editor's list", "carol", "re", []string{`id="post-tag-suggest-edit-3"`}, nil},
		{"nothing typed", "carol", "", []string{`<ul id="post-tag-suggest-new"`}, []string{"<li>"}},
		{"a space after the last tag", "carol", "release ", []string{`<ul id="post-tag-suggest-new"`}, []string{"<li>"}},
		{"no match", "carol", "zzz", nil, []string{"<li>"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			editor := "new"
			if strings.Contains(tt.name, "editor's") {
				editor = "edit-3"
			}
			path := "/tags/suggest?editor=" + editor + "&q=" + url.QueryEscape(tt.query)
			rec := e.getAs(tt.user, path, true)
			requireSSE(t, rec)
			requireBody(t, rec, append([]string{`class="tag-suggest"`}, tt.want...), tt.notWant...)
		})
	}
}

// pageNumber is the number of posts in a full page of the feed.
const pageSize = 30

func TestFeedPages(t *testing.T) {
	e := newRoomsEnv(t)
	made := e.writeMany("alice", "general", pageSize+5)
	newest, thirtieth := made[len(made)-1], made[len(made)-pageSize]
	oldest := made[0]
	room := "/rooms/" + idText(e.general.ID)

	t.Run("first page", func(t *testing.T) {
		rec := e.getAs("alice", room, false)
		requireBody(t, rec, []string{
			`id="feed-posts"`, `id="post-` + idText(newest.ID) + `"`, `id="post-` + idText(thirtieth.ID) + `"`,
			`id="feed-more"`, `data-on-intersect__once="@get(&#39;` + room + `/feed?before=` + idText(thirtieth.ID) + `&#39;)"`,
			`<a href="` + room + `?before=` + idText(thirtieth.ID) + `">Older posts</a>`,
		}, `id="post-`+idText(oldest.ID)+`"`)
		if n := strings.Count(rec.Body.String(), `<article id="post-`); n != pageSize {
			t.Errorf("%d posts on the first page, want %d", n, pageSize)
		}
		body := rec.Body.String()
		if strings.Index(body, `id="post-`+idText(newest.ID)+`"`) > strings.Index(body, `id="post-`+idText(thirtieth.ID)+`"`) {
			t.Error("the posts are not newest first")
		}
	})
	t.Run("the next page by Datastar is appended", func(t *testing.T) {
		rec := e.getAs("alice", room+"/feed?before="+idText(thirtieth.ID), true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{
			"data: selector #feed-posts", "data: mode append",
			`id="post-` + idText(oldest.ID) + `"`, `id="post-` + idText(made[len(made)-pageSize-1].ID) + `"`,
			// The last page: the end of the feed is replaced by an empty one.
			"data: selector #feed-more", "data: mode replace", `<div id="feed-more" class="feed-more"></div>`,
		}, "data-on-intersect", `id="post-`+idText(thirtieth.ID)+`"`)
		if n := strings.Count(rec.Body.String(), `<article id="post-`); n != 5 {
			t.Errorf("%d posts on the last page, want 5", n)
		}
	})
	t.Run("a middle page carries a new sentinel", func(t *testing.T) {
		e := newRoomsEnv(t)
		made := e.writeMany("alice", "general", 2*pageSize+5)
		first := made[len(made)-pageSize] // the last post of the first page
		secondEnd := made[len(made)-2*pageSize]
		rec := e.getAs("alice", room+"/feed?before="+idText(first.ID), true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{
			"data: selector #feed-posts", "data: mode append", "data: selector #feed-more", "data: mode replace",
			`data-on-intersect__once="@get(&#39;` + room + `/feed?before=` + idText(secondEnd.ID) + `&#39;)"`,
		})
		if n := strings.Count(rec.Body.String(), `<article id="post-`); n != pageSize {
			t.Errorf("%d posts on the second page, want %d", n, pageSize)
		}
	})
	t.Run("the next page without JavaScript", func(t *testing.T) {
		e := newRoomsEnv(t)
		made := e.writeMany("alice", "general", pageSize+5)
		before := made[len(made)-pageSize].ID
		rec := e.getAs("alice", room+"?before="+idText(before), false)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		requireBody(t, rec, []string{`id="post-` + idText(made[0].ID) + `"`, "Back to the newest posts", `<div id="feed-more" class="feed-more"></div>`},
			"data-on-intersect", "Older posts", `id="post-`+idText(before)+`"`)
		if n := strings.Count(rec.Body.String(), `<article id="post-`); n != 5 {
			t.Errorf("%d posts on the second page, want 5", n)
		}
		// A plain request for the fragment is sent to the page that shows it.
		rec = e.getAs("alice", room+"/feed?before="+idText(before), false)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != room+"?before="+idText(before) {
			t.Errorf("plain feed request = %d %q", rec.Code, rec.Header().Get("Location"))
		}
	})
	t.Run("a short feed has no sentinel", func(t *testing.T) {
		rec := e.getAs("alice", "/rooms/"+idText(e.random.ID), false)
		requireBody(t, rec, []string{`id="feed-empty"`, "No posts yet", `<div id="feed-more" class="feed-more"></div>`}, "data-on-intersect", "Older posts", `class="post"`)
	})
	t.Run("a wrong before starts at the newest", func(t *testing.T) {
		rec := e.getAs("alice", room+"?before=abc", false)
		requireBody(t, rec, []string{`id="post-` + idText(newest.ID) + `"`})
	})
}

func TestFeedContent(t *testing.T) {
	e := newRoomsEnv(t)
	plain := e.writePost("alice", "general", "Hello <b>team</b>\nsecond line", "design", "ünï")
	md := e.writeMarkdown("bob", "general", "# Big\n\n**bold** and [a link](https://example.com)")
	e.clk.Advance(2 * time.Hour)
	if _, err := e.posts.Edit(t.Context(), e.actor(e.alice), plain.ID, app.PostInput{Format: app.FormatText, Body: "Hello <b>team</b>\nsecond line, edited", Tags: []string{"design", "ünï"}}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	e.reply(plain, "bob")
	post := func(p app.Post) string { return `<article id="post-` + idText(p.ID) + `"` }

	t.Run("the posts of the author", func(t *testing.T) {
		rec := e.getAs("alice", "/rooms/"+idText(e.general.ID), false)
		body := rec.Body.String()
		requireBody(t, rec, []string{
			// Editor on top for a member.
			`id="post-editor-new"`, `action="/rooms/` + idText(e.general.ID) + `/posts"`, `name="body"`, `name="tags"`,
			`name="format" value="markdown" checked`, `name="format" value="text"`, "Preview",
			// Author, user name, time, edited.
			"Alice", "@alice", `datetime="2026-10-26T09:30:00Z"`, ">2026-10-26 09:30<", `class="post-edited"`,
			// Text is escaped, markdown is rendered with headings moved down.
			"Hello &lt;b&gt;team&lt;/b&gt;", "second line, edited", "<strong>bold</strong>", "<h3>Big</h3>", `href="https://example.com"`,
			// Tags link to the search.
			`href="/search?q=%23design"`, ">#design<", ">#ünï<", `href="/search?q=%23%C3%BCn%C3%AF"`,
			// The comment count links to the post.
			`<a class="post-comments" href="/posts/` + idText(plain.ID) + `#comments">1 comment</a>`, `<a class="post-comments" href="/posts/` + idText(md.ID) + `#comments">0 comments</a>`,
		}, "<b>team</b>")
		// Newest first: bob's markdown post is above alice's.
		if strings.Index(body, post(md)) > strings.Index(body, post(plain)) {
			t.Error("the posts are not newest first")
		}
		// Alice may edit and delete hers; bob's has neither Edit nor Delete for her.
		aliceCard := body[strings.Index(body, post(plain)):]
		aliceCard = aliceCard[:strings.Index(aliceCard, "</article>")]
		requireContains(t, aliceCard, []string{`class="post-edit" href="/posts/` + idText(plain.ID) + `/edit"`, `action="/posts/` + idText(plain.ID) + `/delete"`}, nil)
		bobCard := body[strings.Index(body, post(md)):]
		bobCard = bobCard[:strings.Index(bobCard, "</article>")]
		requireContains(t, bobCard, nil, []string{`class="post-edit"`, "/delete", "post-edited"})
	})
	t.Run("an admin deletes but does not edit", func(t *testing.T) {
		rec := e.getAs("admin", "/rooms/"+idText(e.general.ID), false)
		body := rec.Body.String()
		card := body[strings.Index(body, post(plain)):]
		card = card[:strings.Index(card, "</article>")]
		requireContains(t, card, []string{`action="/posts/` + idText(plain.ID) + `/delete"`}, []string{`class="post-edit"`})
	})
	t.Run("a non-member of a visible room reads but cannot write", func(t *testing.T) {
		rec := e.getAs("carol", "/rooms/"+idText(e.general.ID), false)
		requireBody(t, rec, []string{"Join this room to post", `id="join-to-post"`, "Hello &lt;b&gt;team&lt;/b&gt;", "<strong>bold</strong>"},
			`id="post-editor-new"`, `name="body"`, `name="tags"`, `class="post-edit"`, "/delete")
	})
	t.Run("a deleted post that stays", func(t *testing.T) {
		if err := e.posts.Delete(t.Context(), e.actor(e.alice), plain.ID); err != nil {
			t.Fatal(err)
		}
		rec := e.getAs("bob", "/rooms/"+idText(e.general.ID), false)
		body := rec.Body.String()
		card := body[strings.Index(body, post(plain)):]
		card = card[:strings.Index(card, "</article>")]
		requireContains(t, card, []string{"post-deleted", "This post was deleted.", "1 comment"}, []string{"Alice", "@alice", "second line", "#design", `class="post-edit"`, "/delete"})
	})
}

func requireContains(t *testing.T, s string, want, notWant []string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(s, w) {
			t.Errorf("does not contain %q:\n%s", w, s)
		}
	}
	for _, w := range notWant {
		if strings.Contains(s, w) {
			t.Errorf("contains %q:\n%s", w, s)
		}
	}
}

func TestPostEditInPlace(t *testing.T) {
	e := newRoomsEnv(t)
	p := e.writeMarkdown("alice", "general", "Original **body**", "alpha", "beta")
	card := `id="post-` + idText(p.ID) + `"`

	t.Run("Edit swaps the post for the editor filled with it", func(t *testing.T) {
		rec := e.getAs("alice", postPathOf(p, "edit"), true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{
			card, "post-editing", `id="post-editor-edit-` + idText(p.ID) + `"`, `action="/posts/` + idText(p.ID) + `/edit"`,
			">Original **body**</textarea>", `name="format" value="markdown" checked`, `name="tags" type="text" value="alpha beta"`,
			`>Save</button>`, `href="/posts/` + idText(p.ID) + `" data-on:click__prevent="@get(&#39;/posts/` + idText(p.ID) + `/view&#39;)"`,
			`id="post-tag-suggest-edit-` + idText(p.ID) + `"`, `id="post-preview-edit-` + idText(p.ID) + `"`,
		}, `name="from"`)
	})
	t.Run("without JavaScript the page of the post opens with the editor", func(t *testing.T) {
		rec := e.getAs("alice", postPathOf(p, "edit"), false)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		requireBody(t, rec, []string{`id="sidebar-rooms"`, "post-editing", ">Original **body**</textarea>", `id="comments"`})
	})
	t.Run("Cancel restores the post", func(t *testing.T) {
		rec := e.getAs("alice", postPathOf(p, "view"), true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{card, "<strong>body</strong>", "Original "}, "post-editing", "<textarea")
		plain := e.getAs("alice", postPathOf(p, "view"), false)
		if plain.Code != http.StatusSeeOther || plain.Header().Get("Location") != "/posts/"+idText(p.ID) {
			t.Errorf("plain Cancel = %d %q", plain.Code, plain.Header().Get("Location"))
		}
	})
	t.Run("a mistake keeps the input and shows the problem", func(t *testing.T) {
		modes(t, func(t *testing.T, datastar bool) {
			rec := e.post("alice", postPathOf(p, "edit"), editorForm("text", "", "kept-tag, !bad"), datastar)
			want := []string{"Write something.", "is not a tag", `value="kept-tag, !bad"`, `value="text" checked`, `id="post-editor-edit-` + idText(p.ID) + `"`}
			if datastar {
				requireSSE(t, rec)
				requireBody(t, rec, append(want, card, "post-editing"))
			} else {
				if rec.Code != http.StatusUnprocessableEntity {
					t.Fatalf("status = %d, want 422", rec.Code)
				}
				requireBody(t, rec, append(want, `id="sidebar-rooms"`, "post-editing"))
			}
			got, err := e.posts.Get(t.Context(), e.actor(e.alice), p.ID)
			if err != nil || got.Body != "Original **body**" || got.EditedAt != nil {
				t.Errorf("post after a refused save = %+v, %v", got, err)
			}
		})
	})
	t.Run("Save patches the updated post back", func(t *testing.T) {
		e.clk.Advance(time.Hour)
		rec := e.post("alice", postPathOf(p, "edit"), editorForm("markdown", "Changed *text*", "beta gamma"), true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{card, "<em>text</em>", `class="post-edited"`, `href="/search?q=%23gamma"`, `class="post-edit"`}, "<textarea", "alpha", "Original")
		got, err := e.posts.Get(t.Context(), e.actor(e.alice), p.ID)
		if err != nil || got.Body != "Changed *text*" || got.EditedAt == nil || strings.Join(got.Tags, " ") != "beta gamma" {
			t.Errorf("post after the save = %+v, %v", got, err)
		}
	})
	t.Run("a plain save goes to the page of the post", func(t *testing.T) {
		rec := e.post("alice", postPathOf(p, "edit"), editorForm("markdown", "Changed again", "beta"), false)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/posts/"+idText(p.ID) {
			t.Fatalf("answer = %d %q", rec.Code, rec.Header().Get("Location"))
		}
	})
	t.Run("on the page of the post the answers know it", func(t *testing.T) {
		rec := e.getAs("alice", postPathOf(p, "edit")+"?from=post", true)
		requireBody(t, rec, []string{`name="from" value="post"`, `/view?from=post`})
		rec = e.post("alice", postPathOf(p, "edit"), url.Values{"format": {"markdown"}, "body": {"On the page"}, "tags": {""}, "from": {"post"}}, true)
		requireBody(t, rec, []string{"On the page", `/posts/` + idText(p.ID) + `/edit?from=post`, `name="from" value="post"`})
	})
	t.Run("only the author edits", func(t *testing.T) {
		for _, user := range []string{"bob", "admin"} {
			rec := e.getAs(user, postPathOf(p, "edit"), true)
			requireSSE(t, rec)
			requireBody(t, rec, []string{card, "You are not allowed to do that"}, "<textarea")
			rec = e.getAs(user, postPathOf(p, "edit"), false)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s: plain Edit status = %d, want 403", user, rec.Code)
			}
			rec = e.post(user, postPathOf(p, "edit"), editorForm("text", "hijack", ""), true)
			requireSSE(t, rec)
			requireBody(t, rec, []string{"You are not allowed to do that", "post-editing"})
			rec = e.post(user, postPathOf(p, "edit"), editorForm("text", "hijack", ""), false)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s: plain save status = %d, want 403", user, rec.Code)
			}
		}
		got, err := e.posts.Get(t.Context(), e.actor(e.alice), p.ID)
		if err != nil || got.Body != "On the page" {
			t.Errorf("post after the refused edits = %+v, %v", got, err)
		}
	})
	t.Run("a deleted post or a missing one is not found", func(t *testing.T) {
		gone := e.writePost("alice", "general", "going")
		if err := e.posts.Delete(t.Context(), e.actor(e.alice), gone.ID); err != nil {
			t.Fatal(err)
		}
		kept := e.writePost("alice", "general", "kept")
		e.reply(kept, "bob")
		if err := e.posts.Delete(t.Context(), e.actor(e.alice), kept.ID); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{idText(gone.ID), idText(kept.ID), "99999", "0", "abc"} {
			rec := e.getAs("alice", "/posts/"+id+"/edit", false)
			if rec.Code != http.StatusNotFound {
				t.Errorf("GET /posts/%s/edit = %d, want 404", id, rec.Code)
			}
		}
	})
}

func TestPostDelete(t *testing.T) {
	t.Run("a post without replies disappears", func(t *testing.T) {
		modes(t, func(t *testing.T, datastar bool) {
			e := newRoomsEnv(t)
			p := e.writePost("alice", "general", "short lived")
			rec := e.post("alice", postPathOf(p, "delete"), url.Values{}, datastar)
			if datastar {
				requireSSE(t, rec)
				requireBody(t, rec, []string{"data: mode remove", "data: selector #post-" + idText(p.ID)})
			} else if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/rooms/"+idText(e.general.ID) {
				t.Fatalf("answer = %d %q, want 303 to the room", rec.Code, rec.Header().Get("Location"))
			}
			if n := len(e.feed("alice", "general")); n != 0 {
				t.Errorf("%d posts after the delete", n)
			}
		})
	})
	t.Run("a post with replies stays as the placeholder", func(t *testing.T) {
		modes(t, func(t *testing.T, datastar bool) {
			e := newRoomsEnv(t)
			p := e.writePost("alice", "general", "to be kept", "tagged")
			e.reply(p, "bob")
			rec := e.post("alice", postPathOf(p, "delete"), url.Values{}, datastar)
			if datastar {
				requireSSE(t, rec)
				requireBody(t, rec, []string{`id="post-` + idText(p.ID) + `"`, "post-deleted", "This post was deleted.", "1 comment"},
					"data: mode remove", "to be kept", "Alice", "#tagged")
			} else if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303", rec.Code)
			}
			posts := e.feed("alice", "general")
			if len(posts) != 1 || !posts[0].Deleted {
				t.Errorf("feed = %+v, want the deleted placeholder", posts)
			}
		})
	})
	t.Run("on the page of the post", func(t *testing.T) {
		e := newRoomsEnv(t)
		gone := e.writePost("alice", "general", "going")
		kept := e.writePost("alice", "general", "kept")
		e.reply(kept, "bob")
		form := url.Values{"from": {"post"}}
		rec := e.post("alice", postPathOf(gone, "delete"), form, true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{redirectScript("/rooms/" + idText(e.general.ID))}, "data: mode remove")
		rec = e.post("alice", postPathOf(kept, "delete"), form, true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{"This post was deleted.", `id="post-` + idText(kept.ID) + `"`}, "data: mode remove")
	})
	t.Run("who may delete", func(t *testing.T) {
		tests := []struct {
			user string
			ok   bool
		}{
			{"alice", true}, {"admin", true}, {"bob", false}, {"carol", false},
		}
		for _, tt := range tests {
			modes(t, func(t *testing.T, datastar bool) {
				e := newRoomsEnv(t)
				p := e.writePost("alice", "general", "mine")
				rec := e.post(tt.user, postPathOf(p, "delete"), url.Values{}, datastar)
				left := len(e.feed("alice", "general"))
				if tt.ok {
					if left != 0 {
						t.Errorf("%s: the post is still there", tt.user)
					}
					return
				}
				if left != 1 {
					t.Errorf("%s: the post was deleted", tt.user)
				}
				if datastar {
					requireSSE(t, rec)
					requireBody(t, rec, []string{"You are not allowed to do that", `id="post-` + idText(p.ID) + `"`, "mine"})
				} else if rec.Code != http.StatusForbidden {
					t.Errorf("%s: status = %d, want 403", tt.user, rec.Code)
				}
			})
		}
	})
	t.Run("a deleted or missing post is not found", func(t *testing.T) {
		e := newRoomsEnv(t)
		p := e.writePost("alice", "general", "once")
		if err := e.posts.Delete(t.Context(), e.actor(e.alice), p.ID); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{idText(p.ID), "9999", "x"} {
			rec := e.post("alice", "/posts/"+id+"/delete", url.Values{}, false)
			if rec.Code != http.StatusNotFound {
				t.Errorf("POST /posts/%s/delete = %d, want 404", id, rec.Code)
			}
		}
	})
}

func TestPostPage(t *testing.T) {
	e := newRoomsEnv(t)
	p := e.writeMarkdown("alice", "general", "Page **body**", "page-tag")
	e.reply(p, "alice")

	t.Run("a reader sees the post in the layout", func(t *testing.T) {
		for _, user := range []string{"alice", "bob", "carol", "admin"} {
			rec := e.getAs(user, postPathOf(p, ""), false)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: status = %d", user, rec.Code)
			}
			requireBody(t, rec, []string{
				"<title>Post by Alice in general · Clowder</title>", `id="sidebar-rooms"`,
				`<a href="/rooms/` + idText(e.general.ID) + `">general</a>`,
				`id="post-` + idText(p.ID) + `"`, "<strong>body</strong>", `href="/search?q=%23page-tag"`, "@alice",
				`id="comments"`, "<h2>Comments</h2>", "1 comment",
			})
		}
	})
	t.Run("the room is highlighted in the sidebar", func(t *testing.T) {
		rec := e.getAs("alice", postPathOf(p, ""), false)
		requireBody(t, rec, []string{`href="/rooms/` + idText(e.general.ID) + `" class="current" data-room="general" aria-current="page"`})
	})
	t.Run("edit and delete only for those allowed", func(t *testing.T) {
		rec := e.getAs("alice", postPathOf(p, ""), false)
		requireBody(t, rec, []string{`class="post-edit" href="/posts/` + idText(p.ID) + `/edit?from=post"`, `action="/posts/` + idText(p.ID) + `/delete"`, `name="from" value="post"`})
		rec = e.getAs("bob", postPathOf(p, ""), false)
		requireBody(t, rec, nil, `class="post-edit"`, "/delete")
	})
	t.Run("a deleted post that stays shows the placeholder", func(t *testing.T) {
		q := e.writePost("alice", "general", "secretly gone")
		e.reply(q, "bob")
		if err := e.posts.Delete(t.Context(), e.actor(e.alice), q.ID); err != nil {
			t.Fatal(err)
		}
		rec := e.getAs("bob", postPathOf(q, ""), false)
		requireBody(t, rec, []string{"This post was deleted.", "<title>Deleted post in general · Clowder</title>"}, "secretly gone", "Alice")
	})
	t.Run("not found", func(t *testing.T) {
		gone := e.writePost("alice", "general", "going")
		if err := e.posts.Delete(t.Context(), e.actor(e.alice), gone.ID); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{idText(gone.ID), "99999", "0", "abc"} {
			rec := e.getAs("alice", "/posts/"+id, false)
			if rec.Code != http.StatusNotFound {
				t.Errorf("GET /posts/%s = %d, want 404", id, rec.Code)
			}
			requireBody(t, rec, []string{"Page not found"})
		}
	})
}

// TestPrivateRoomPostsAreNotFound has people who are not members of the
// private room (carol, and the admin too) call every route on its post and on
// the room: all of them are "not found", never "forbidden".
func TestPrivateRoomPostsAreNotFound(t *testing.T) {
	e := newRoomsEnv(t)
	p := e.writePost("alice", "secret", "private words", "private-tag")
	comment := e.commentOn(p, "bob", "private reply")
	secret := "/rooms/" + idText(e.secret.ID)
	requests := []struct {
		name   string
		method string
		path   string
		form   url.Values
		// redirectTo is the page a Datastar request is sent to.
		redirectTo string
	}{
		{"room page", http.MethodGet, secret, nil, secret},
		{"room page, older posts", http.MethodGet, secret + "?before=99", nil, secret},
		{"feed fragment", http.MethodGet, secret + "/feed?before=99", nil, secret},
		{"create", http.MethodPost, secret + "/posts", editorForm("text", "x", ""), secret},
		{"post page", http.MethodGet, postPathOf(p, ""), nil, postPathOf(p, "")},
		{"edit form", http.MethodGet, postPathOf(p, "edit"), nil, postPathOf(p, "")},
		{"edit save", http.MethodPost, postPathOf(p, "edit"), editorForm("text", "x", ""), postPathOf(p, "")},
		{"cancel", http.MethodGet, postPathOf(p, "view"), nil, postPathOf(p, "")},
		{"delete", http.MethodPost, postPathOf(p, "delete"), url.Values{}, postPathOf(p, "")},
		{"reply form", http.MethodGet, postPathOf(p, "reply"), nil, postPathOf(p, "")},
		{"reply", http.MethodPost, postPathOf(p, "replies"), editorForm("text", "x", ""), postPathOf(p, "")},
		{"comment page", http.MethodGet, postPathOf(comment, ""), nil, postPathOf(comment, "")},
		{"comment edit form", http.MethodGet, postPathOf(comment, "edit"), nil, postPathOf(comment, "")},
		{"comment delete", http.MethodPost, postPathOf(comment, "delete"), url.Values{}, postPathOf(comment, "")},
	}
	for _, user := range []string{"carol", "admin"} {
		for _, rq := range requests {
			modes(t, func(t *testing.T, datastar bool) {
				if (rq.name == "feed fragment" || rq.name == "cancel") && !datastar {
					t.Skip("a plain request for a fragment is a redirect to the page that shows it, which is not found")
				}
				var rec *httptest.ResponseRecorder
				if rq.method == http.MethodGet {
					rec = e.getAs(user, rq.path, datastar)
				} else {
					rec = e.post(user, rq.path, rq.form, datastar)
				}
				if datastar {
					requireSSE(t, rec)
					requireBody(t, rec, []string{redirectScript(rq.redirectTo)}, "private words", "private-tag", "private reply")
					return
				}
				if rec.Code != http.StatusNotFound {
					t.Fatalf("%s %s %s: status = %d, want 404 (not 403)", user, rq.method, rq.path, rec.Code)
				}
				requireBody(t, rec, []string{"Page not found"}, "private words", "private-tag", "private reply", "not allowed")
			})
		}
	}
	for _, user := range []string{"carol", "admin"} {
		// The tags of the private room are not suggested to them either.
		rec := e.getAs(user, "/tags/suggest?editor=new&q=private", true)
		requireSSE(t, rec)
		requireBody(t, rec, nil, "private-tag")
	}
	if posts := e.feed("alice", "secret"); len(posts) != 1 || posts[0].Body != "private words" {
		t.Errorf("the private feed changed: %+v", posts)
	}
}

// TestPostRoutesNeedASession has a request without a login to each route of
// this step: all of them are sent to the login page.
func TestPostRoutesNeedASession(t *testing.T) {
	e := newRoomsEnv(t)
	p := e.writePost("alice", "general", "hello")
	paths := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/rooms/1/feed?before=3"}, {http.MethodPost, "/rooms/1/posts"},
		{http.MethodGet, postPathOf(p, "")}, {http.MethodGet, postPathOf(p, "edit")}, {http.MethodPost, postPathOf(p, "edit")},
		{http.MethodGet, postPathOf(p, "view")}, {http.MethodPost, postPathOf(p, "delete")},
		{http.MethodGet, postPathOf(p, "reply")}, {http.MethodPost, postPathOf(p, "replies")},
		{http.MethodPost, "/preview"}, {http.MethodGet, "/tags/suggest?q=a"},
		{http.MethodGet, "/files/1/a.txt"},
	}
	for _, tt := range paths {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			var rec *httptest.ResponseRecorder
			if tt.method == http.MethodGet {
				rec = e.send(get(tt.path))
			} else {
				rec = e.send(postForm(tt.path, url.Values{"body": {"x"}}))
			}
			if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
				t.Errorf("answer = %d %q, want a redirect to the login page", rec.Code, rec.Header().Get("Location"))
			}
		})
	}
}

func TestPostFormTooLarge(t *testing.T) {
	e := newRoomsEnv(t)
	// 20,000 characters of four bytes each still fit (the form writes each byte
	// as three characters); a body far beyond that is refused as unreadable.
	four := strings.Repeat("\U0001F600", 20_000)
	rec := e.post("alice", "/rooms/"+idText(e.general.ID)+"/posts", editorForm("text", four, ""), true)
	requireSSE(t, rec)
	requireBody(t, rec, []string{`id="post-`})
	huge := strings.Repeat("a", maxPostFormBytes+1)
	rec = e.post("alice", "/rooms/"+idText(e.general.ID)+"/posts", editorForm("text", huge, ""), false)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
