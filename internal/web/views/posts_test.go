package views_test

import (
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
)

func TestPostEditor(t *testing.T) {
	edited := app.Post{ID: 12, Format: app.FormatText, Body: "<b>old</b> & more", Tags: []string{"a", "b"}}
	tests := []struct {
		name    string
		data    views.PostEditorData
		want    []string
		notWant []string
	}{
		{
			name: "the editor of a new post",
			data: views.NewPostEditorData(7),
			want: []string{
				`<form id="post-editor-new" class="post-editor" method="post" action="/rooms/7/posts"`,
				"@post(&#39;/rooms/7/posts&#39;, {contentType: &#39;form&#39;})",
				`<input type="hidden" name="editor" value="new">`,
				`<label for="post-body-new">Message</label>`, `<textarea id="post-body-new" name="body" rows="5"></textarea>`,
				// Markdown is the default.
				`name="format" value="markdown" checked`, `name="format" value="text"> Text`,
				`<label for="post-tags-new">Tags</label>`, `id="post-tags-new" name="tags" type="text" value=""`,
				"data-on:input__debounce.200ms=", "/tags/suggest?editor=new&amp;q=", "encodeURIComponent",
				`<ul id="post-tag-suggest-new" class="tag-suggest"`,
				`id="post-preview-new" class="post-preview" hidden`,
				`>Post</button>`, `data-on:click="@post(&#39;/preview&#39;, {contentType: &#39;form&#39;})">Preview</button>`,
			},
			notWant: []string{"Cancel", `name="from"`, "field-error", "form-error", "aria-invalid", "required"},
		},
		{
			name: "the editor of an edit in place keeps the post and offers Cancel",
			data: views.EditPostEditorData(edited, ""),
			want: []string{
				`id="post-editor-edit-12"`, `action="/posts/12/edit"`,
				// The body is escaped.
				`&lt;b&gt;old&lt;/b&gt; &amp; more</textarea>`, `name="format" value="text" checked`,
				`value="a b"`, `>Save</button>`, `href="/posts/12"`, "@get(&#39;/posts/12/view&#39;)", `>Cancel</a>`,
				`id="post-preview-edit-12"`, `id="post-tag-suggest-edit-12"`,
			},
			notWant: []string{"<b>old</b>", `name="from"`},
		},
		{
			name: "an edit on the page of the post",
			data: views.EditPostEditorData(edited, views.FromPost),
			want: []string{`<input type="hidden" name="from" value="post">`, "@get(&#39;/posts/12/view?from=post&#39;)"},
		},
		{
			name: "problems next to their fields, with the input kept",
			data: func() views.PostEditorData {
				d := views.NewPostEditorData(7)
				d.Format, d.Body, d.Tags = "text", "kept body", "kept-tag"
				d.Errors = views.FormErrors{
					Form: "Something went wrong. Try again.",
					Fields: map[string]string{
						"body": "Write something.", "format": "Choose text or markdown.", "tags": "At most 10 tags.",
					},
				}
				return d
			}(),
			want: []string{
				`<p class="form-error" role="alert">Something went wrong. Try again.</p>`,
				`aria-invalid="true" aria-describedby="post-body-new-error"`, `<p class="field-error" id="post-body-new-error" role="alert">Write something.</p>`,
				`id="post-format-new-error" role="alert">Choose text or markdown.</p>`,
				`aria-describedby="post-tags-new-error"`, `<p class="field-error" id="post-tags-new-error" role="alert">At most 10 tags.</p>`,
				`>kept body</textarea>`, `name="format" value="text" checked`, `value="kept-tag"`,
			},
			notWant: []string{"Separate tags with spaces"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireContains(t, render(t, views.PostEditor(tt.data)), tt.want, tt.notWant)
		})
	}
}

func TestPostPreviewAndSuggestions(t *testing.T) {
	tests := []struct {
		name    string
		c       templ.Component
		want    []string
		notWant []string
	}{
		{
			name: "the box before the first preview is hidden and empty",
			c:    views.PostPreview(views.PostPreviewData{EditorID: "new", Hidden: true}),
			want: []string{`<div id="post-preview-new" class="post-preview" hidden></div>`},
		},
		{
			name:    "a preview shows the rendered body and a Close button",
			c:       views.PostPreview(views.PostPreviewData{EditorID: "edit-3", HTML: template.HTML("<p><strong>hi</strong></p>")}),
			want:    []string{`id="post-preview-edit-3"`, "<p><strong>hi</strong></p>", ">Close</button>", ".hidden = true"},
			notWant: []string{"Nothing to preview", `class="post-preview" hidden`},
		},
		{
			name: "an empty body",
			c:    views.PostPreview(views.PostPreviewData{EditorID: "new"}),
			want: []string{"Nothing to preview."},
		},
		{
			name:    "a message instead of a preview",
			c:       views.PostPreview(views.PostPreviewData{EditorID: "new", Message: "Use at most 20,000 characters.", HTML: "<p>ignored</p>"}),
			want:    []string{`role="alert">Use at most 20,000 characters.</p>`},
			notWant: []string{"ignored"},
		},
		{
			name: "suggestions complete the tag",
			c:    views.TagSuggestions("new", []string{"design", "desktop"}),
			want: []string{
				`<ul id="post-tag-suggest-new" class="tag-suggest"`, `data-tag="design"`, `>#design</button>`, `>#desktop</button>`,
				"el.dataset.tag", "lastIndexOf",
			},
		},
		{
			name:    "no suggestions is an empty list",
			c:       views.TagSuggestions("edit-9", nil),
			want:    []string{`<ul id="post-tag-suggest-edit-9" class="tag-suggest"`},
			notWant: []string{"<li>"},
		},
		{
			name:    "a tag is escaped",
			c:       views.TagSuggestions("new", []string{`"><script>`}),
			notWant: []string{"<script>"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireContains(t, render(t, tt.c), tt.want, tt.notWant)
		})
	}
}

func TestPostCard(t *testing.T) {
	created := time.Date(2026, 10, 26, 9, 5, 0, 0, time.FixedZone("EEST", 3*3600))
	edited := created.Add(time.Hour)
	post := app.Post{
		ID: 5, RoomID: 2, RoomName: "general", Author: app.UserRef{ID: 1, Username: "alice", DisplayName: "Alice <A>"},
		Format: app.FormatMarkdown, Body: "x", HTML: template.HTML("<p>Hello <strong>you</strong></p>"),
		Tags: []string{"design", "ünï"}, CommentCount: 3, CreatedAt: created, CanEdit: true, CanDelete: true,
	}
	withEdit := post
	withEdit.EditedAt = &edited
	other := post
	other.CanEdit, other.CanDelete = false, false
	deleted := app.Post{ID: 6, RoomID: 2, RoomName: "general", CommentCount: 1, Deleted: true}
	tests := []struct {
		name    string
		data    views.PostCardData
		want    []string
		notWant []string
	}{
		{
			name: "a post",
			data: views.PostCardData{Post: post},
			want: []string{
				`<article id="post-5" class="post" data-post="5">`,
				`<span class="post-author">Alice &lt;A&gt;</span>`, `@alice`,
				// UTC, whatever the zone of the stored time.
				`<time datetime="2026-10-26T06:05:00Z" title="2026-10-26T06:05:00Z">2026-10-26 06:05</time>`, `href="/posts/5"`,
				"<p>Hello <strong>you</strong></p>",
				`href="/search?q=%23design"`, `>#design</a>`, `href="/search?q=%23%C3%BCn%C3%AF"`,
				`<a class="post-comments" href="/posts/5">3 comments</a>`,
				`class="post-edit" href="/posts/5/edit"`, "@get(&#39;/posts/5/edit&#39;)",
				`action="/posts/5/delete"`, "confirm(&#39;Delete this post? This cannot be undone.&#39;)",
			},
			notWant: []string{"post-edited", `name="from"`, "form-error", "<A>"},
		},
		{
			name: "an edited post",
			data: views.PostCardData{Post: withEdit},
			want: []string{`<span class="post-edited" title="2026-10-26T07:05:00Z">edited</span>`},
		},
		{
			name:    "a post of someone else",
			data:    views.PostCardData{Post: other},
			want:    []string{`3 comments`},
			notWant: []string{`class="post-edit"`, "/delete"},
		},
		{
			name: "on its own page",
			data: views.PostCardData{Post: post, From: views.FromPost},
			want: []string{`href="/posts/5/edit?from=post"`, `@get(&#39;/posts/5/edit?from=post&#39;)`, `<input type="hidden" name="from" value="post">`},
		},
		{
			name: "with a message",
			data: views.PostCardData{Post: post, Message: "You are not allowed to do that"},
			want: []string{`<p class="form-error" role="alert">You are not allowed to do that</p>`},
		},
		{
			name:    "the placeholder of a deleted post",
			data:    views.PostCardData{Post: deleted},
			want:    []string{`class="post post-deleted"`, "This post was deleted.", `<a class="post-comments" href="/posts/6">1 comment</a>`},
			notWant: []string{"post-author", "post-meta", "post-body", "tags", "post-edit", "/delete"},
		},
		{
			name: "one comment, no tags",
			data: views.PostCardData{Post: app.Post{ID: 8, CommentCount: 1, HTML: "<p>x</p>"}},
			want: []string{"1 comment</a>"}, notWant: []string{`class="tags"`, "1 comments"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireContains(t, render(t, views.PostCard(tt.data)), tt.want, tt.notWant)
		})
	}
}

func TestPostCardRendersOnlyTheRenderedBodyAsHTML(t *testing.T) {
	// Everything but Post.HTML is text, whatever it contains.
	evil := `<img src=x onerror=alert(1)>`
	post := app.Post{
		ID: 1, RoomName: evil, Author: app.UserRef{Username: evil, DisplayName: evil}, Body: evil,
		HTML: template.HTML("<p>safe</p>"), Tags: []string{evil},
	}
	html := render(t, views.PostCard(views.PostCardData{Post: post, Message: evil}))
	requireContains(t, html, []string{"<p>safe</p>", "&lt;img"}, []string{"<img"})
}

func TestFeedViews(t *testing.T) {
	posts := []app.Post{{ID: 9, HTML: "<p>nine</p>"}, {ID: 8, HTML: "<p>eight</p>"}}
	tests := []struct {
		name    string
		c       templ.Component
		want    []string
		notWant []string
	}{
		{
			name: "a feed with more posts has the sentinel and the link",
			c:    views.Feed(views.FeedData{RoomID: 4, Page: app.FeedPage{Posts: posts, HasNext: true, NextBefore: 8}}),
			want: []string{
				`id="feed-posts"`, `id="post-9"`, `id="post-8"`, `id="feed-empty"`,
				`<div id="feed-more" class="feed-more" data-on-intersect__once="@get(&#39;/rooms/4/feed?before=8&#39;)">`,
				`<a href="/rooms/4?before=8">Older posts</a>`,
			},
			notWant: []string{"Back to the newest posts"},
		},
		{
			name:    "the last page has an empty end",
			c:       views.Feed(views.FeedData{RoomID: 4, Before: 8, Page: app.FeedPage{Posts: posts[1:]}}),
			want:    []string{`<div id="feed-more" class="feed-more"></div>`, `<a href="/rooms/4">Back to the newest posts</a>`},
			notWant: []string{"data-on-intersect", "Older posts"},
		},
		{
			name: "an empty feed says so",
			c:    views.Feed(views.FeedData{RoomID: 4}),
			want: []string{`<p id="feed-empty" class="empty">No posts yet</p>`, `<div id="feed-posts" class="feed-posts"></div>`},
		},
		{
			name:    "the posts alone, to append",
			c:       views.FeedPosts(views.FeedData{RoomID: 4, Page: app.FeedPage{Posts: posts}}),
			want:    []string{`id="post-9"`, `id="post-8"`},
			notWant: []string{"feed-more", "feed-posts"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireContains(t, render(t, tt.c), tt.want, tt.notWant)
		})
	}
}

func TestRoomPageEditorOrJoinNote(t *testing.T) {
	member := app.Room{ID: 7, Name: "secret", IsMember: true}
	outsider := app.Room{ID: 8, Name: "general", Visibility: app.VisibilityVisible}
	user := app.User{ID: 2, DisplayName: "Alice", Username: "alice", Role: app.RoleMember}
	tests := []struct {
		name    string
		room    app.Room
		want    []string
		notWant []string
	}{
		{
			name: "a member sees the editor above the feed",
			room: member,
			want: []string{`id="post-editor-new"`, `action="/rooms/7/posts"`, `id="feed"`},
			notWant: []string{
				"Join this room to post",
			},
		},
		{
			name:    "a non-member is told to join",
			room:    outsider,
			want:    []string{`id="join-to-post"`, "Join this room to post", `id="feed"`},
			notWant: []string{`id="post-editor-new"`, "<textarea"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := views.RoomData{
				Sidebar: views.SidebarData{User: user}, Viewer: user, Room: tt.room,
				Editor: views.NewPostEditorData(tt.room.ID), Feed: views.FeedData{RoomID: tt.room.ID},
			}
			html := render(t, views.RoomPage(d))
			requireContains(t, html, tt.want, tt.notWant)
			if strings.Index(html, `id="post-editor-new"`) > strings.Index(html, `id="feed"`) && tt.room.IsMember {
				t.Error("the editor is below the feed")
			}
		})
	}
}

func TestPostPageView(t *testing.T) {
	user := app.User{ID: 2, DisplayName: "Alice", Username: "alice", Role: app.RoleMember}
	post := app.Post{
		ID: 5, RoomID: 2, RoomName: "general <x>", Author: app.UserRef{ID: 2, Username: "alice", DisplayName: "Alice"},
		Format: app.FormatText, Body: "hello", HTML: "<p>hello</p>", CreatedAt: time.Date(2026, 10, 26, 9, 30, 0, 0, time.UTC),
		CanEdit: true, CanDelete: true,
	}
	sidebar := views.SidebarData{User: user, Rooms: []app.Room{{ID: 2, Name: "general <x>"}}, CurrentRoomID: 2}
	deleted := app.Post{ID: 6, RoomID: 2, RoomName: "general", Deleted: true}
	tests := []struct {
		name    string
		data    views.PostPageData
		want    []string
		notWant []string
	}{
		{
			name: "the post in the layout with the room highlighted and a place for comments",
			data: views.PostPageData{Sidebar: sidebar, Post: post},
			want: []string{
				"<title>Post by Alice in general &lt;x&gt; · Clowder</title>", `<aside>`, `aria-current="page"`,
				`<a href="/rooms/2">general &lt;x&gt;</a>`, `id="post-5"`, "<p>hello</p>",
				`name="from" value="post"`, `<section id="comments" class="comments">`, "No comments yet.",
			},
			notWant: []string{"<textarea"},
		},
		{
			name: "the comment count is shown",
			data: views.PostPageData{Sidebar: sidebar, Post: func() app.Post { p := post; p.CommentCount = 2; return p }()},
			want: []string{"2 comments"},
		},
		{
			name: "editing replaces the post by the editor",
			data: views.PostPageData{Sidebar: sidebar, Post: post, Editing: true, Editor: views.EditPostEditorData(post, views.FromPost)},
			want: []string{`post-editing`, `id="post-editor-edit-5"`, ">hello</textarea>", `id="comments"`},
		},
		{
			name: "a message",
			data: views.PostPageData{Sidebar: sidebar, Post: post, Message: "You are not allowed to do that"},
			want: []string{`form-error`, "You are not allowed to do that"},
		},
		{
			name:    "a deleted post",
			data:    views.PostPageData{Sidebar: sidebar, Post: deleted},
			want:    []string{"<title>Deleted post in general · Clowder</title>", "This post was deleted."},
			notWant: []string{"post-author"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireContains(t, render(t, views.PostPage(tt.data)), tt.want, tt.notWant)
		})
	}
}

func TestPostPaths(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"post", views.PostPath(12), "/posts/12"},
		{"feed", views.FeedPath(3, 40), "/rooms/3/feed?before=40"},
		{"older posts", views.OlderPostsPath(3, 40), "/rooms/3?before=40"},
		{"tag", views.TagSearchPath("design"), "/search?q=%23design"},
		{"tag with unicode", views.TagSearchPath("ünï"), "/search?q=%23%C3%BCn%C3%AF"},
		{"tag with a dash", views.TagSearchPath("a-b_c"), "/search?q=%23a-b_c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}
