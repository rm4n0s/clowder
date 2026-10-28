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
				`<form id="post-editor-new" class="post-editor" method="post" enctype="multipart/form-data" action="/rooms/7/posts"`,
				"@post(&#39;/rooms/7/posts&#39;, {contentType: &#39;form&#39;})",
				`<input type="hidden" name="editor" value="new">`,
				`<label for="post-body-new">Share something with the clowder</label>`, "<textarea id=\"post-body-new\" name=\"body\" rows=\"3\" placeholder=\"Markdown welcome. ```go for code, - [ ] for a task\"></textarea>",
				// Markdown is the default.
				`name="format" value="markdown" checked`, `name="format" value="text"> <span>Text</span>`,
				`<label class="visually-hidden" for="post-tags-new">Tags</label>`, `id="post-tags-new" name="tags" type="text" value=""`,
				"data-on:input__debounce.200ms=", "/tags/suggest?editor=new&amp;q=", "encodeURIComponent",
				`<ul id="post-tag-suggest-new" class="tag-suggest"`,
				`id="post-preview-new" class="post-preview" hidden`,
				`>Post</button>`, `@post(&#39;/preview&#39;, {contentType: &#39;form&#39;}); f.disabled = false">Preview</button>`,
				// The preview leaves the files out: the input is switched off.
				"elements.files; f.disabled = true;",
				// The file input: several files, no list of current ones.
				`<label class="attach-button" for="post-files-new" title="Attach files">`, `<span class="visually-hidden">Attach files</span>`, `<input id="post-files-new" class="attach-input" name="files" type="file" multiple`,
				`class="attach-names" aria-live="polite" data-ignore-morph`,
			},
			notWant: []string{"Cancel", `name="from"`, "field-error", "form-error", "aria-invalid", "required", `name="keep"`, `name="remove"`},
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
			name: "the editor of a reply closes its slot and posts to the replies",
			data: views.ReplyEditorData(12),
			want: []string{
				`<form id="post-editor-reply-12" class="post-editor" method="post" enctype="multipart/form-data" action="/posts/12/replies"`, "@post(&#39;/posts/12/replies&#39;, {contentType: &#39;form&#39;})",
				`name="editor" value="reply-12"`, `name="format" value="markdown" checked`, `>Reply</button>`,
				`<a class="button-link" href="/posts/12" data-on:click__prevent="el.closest(&#39;.reply-slot&#39;).replaceChildren()">Cancel</a>`,
				`id="post-tag-suggest-reply-12"`, `id="post-preview-reply-12"`,
			},
			notWant: []string{"/view", `name="from"`},
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
		{
			name: "the edit lists the current files with a Remove box each",
			data: views.EditPostEditorData(app.Post{
				ID: 12, Format: app.FormatText, Body: "b",
				Attachments: []app.AttachmentView{
					{ID: 5, FileName: "a <b>.png", ContentType: "image/png", Size: 12 * 1024, IsImage: true, Width: 8, Height: 6},
					{ID: 9, FileName: "notes.txt", ContentType: "text/plain", Size: 3_565_158},
				},
			}, ""),
			want: []string{
				`<ul class="attachment-edit" aria-label="Current files">`,
				`<input type="hidden" name="keep" value="5">`, `<span class="attachment-name">a &lt;b&gt;.png</span>`, `<span class="hint">12 KB</span>`,
				`<input type="checkbox" name="remove" value="5">`,
				`<input type="hidden" name="keep" value="9">`, `<span class="hint">3.4 MB</span>`, `<input type="checkbox" name="remove" value="9">`,
			},
			notWant: []string{"<b>.png", "checked name", `value="5" checked`, `value="9" checked`},
		},
		{
			name: "a failed edit keeps the Remove boxes that were ticked and shows the file problem",
			data: func() views.PostEditorData {
				d := views.EditPostEditorData(app.Post{
					ID: 12, Format: app.FormatText, Body: "b",
					Attachments: []app.AttachmentView{{ID: 5, FileName: "a.txt", Size: 1}, {ID: 9, FileName: "b.txt", Size: 1}},
				}, "")
				d.Removing = map[uint64]bool{9: true}
				d.Errors = views.FormErrors{Fields: map[string]string{"attachments": "Each file can be at most 25 MB."}}
				return d
			}(),
			want: []string{
				`<input type="checkbox" name="remove" value="9" checked>`, `<input type="checkbox" name="remove" value="5">`,
				`aria-invalid="true" aria-describedby="post-files-edit-12-error"`,
				`<p class="field-error" id="post-files-edit-12-error" role="alert">Each file can be at most 25 MB.</p>`,
			},
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
				`<a class="post-comments" href="/posts/5#comments">3 comments</a>`,
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
			want:    []string{`class="post post-deleted"`, "This post was deleted.", `<a class="post-comments" href="/posts/6#comments">1 comment</a>`},
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
				"<title id=\"page-title\">Post by Alice in general &lt;x&gt; · Clowder</title>", `<aside id="sidebar" popover>`, `aria-current="page"`,
				`<a href="/rooms/2">general &lt;x&gt;</a>`, `id="post-5"`, "<p>hello</p>",
				`name="from" value="post"`, `<div id="thread" class="thread">`, `<section id="comments" class="comments">`, "No comments yet.",
				// A person who has not joined the room is told to.
				`id="join-to-reply"`, "Join this room to reply",
			},
			notWant: []string{"<textarea", "post-reply", "reply-slot"},
		},
		{
			name:    "a member has the Reply button and its slot",
			data:    views.PostPageData{Sidebar: sidebar, Post: post, CanReply: true},
			want:    []string{`class="post-reply" href="/posts/5/reply"`, "@get(&#39;/posts/5/reply&#39;)", `<div id="reply-slot-5" class="reply-slot"></div>`},
			notWant: []string{"Join this room to reply"},
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
			want:    []string{"<title id=\"page-title\">Deleted post in general · Clowder</title>", "This post was deleted."},
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

// commentOf makes a comment of the thread of post 5.
func commentOf(id uint64, depth int, mutate func(*app.Comment)) app.Comment {
	c := app.Comment{
		Post: app.Post{
			ID: id, RoomID: 2, RoomName: "general", RootID: 5, ParentID: 5, IsComment: true,
			Author: app.UserRef{ID: 3, Username: "bob", DisplayName: "Bob"}, Format: app.FormatText, Body: "x",
			HTML: template.HTML("<p>a comment</p>"), Tags: []string{"mine"}, CreatedAt: time.Date(2026, 10, 26, 9, 5, 0, 0, time.UTC),
			CanEdit: true, CanDelete: true,
		},
		Depth: depth, ParentID: 5, ParentAuthor: app.UserRef{ID: 2, Username: "alice", DisplayName: "Alice"},
	}
	if mutate != nil {
		mutate(&c)
	}
	return c
}

func TestCommentCard(t *testing.T) {
	card := func(c app.Comment, mutate func(*views.PostCardData)) views.PostCardData {
		d := views.PostCardData{Post: c.Post, Depth: c.Depth, ParentAuthor: c.ParentAuthor, From: views.FromPost, CanReply: true}
		if mutate != nil {
			mutate(&d)
		}
		return d
	}
	deleted := commentOf(11, 2, func(c *app.Comment) {
		c.Deleted, c.Author, c.HTML, c.Body, c.Tags, c.CanEdit, c.CanDelete = true, app.UserRef{}, "", "", []string{}, false, false
	})
	tests := []struct {
		name    string
		data    views.PostCardData
		want    []string
		notWant []string
	}{
		{
			name: "a comment is indented to its depth and has Reply, Edit and Delete",
			data: card(commentOf(9, 3, nil), nil),
			want: []string{
				`<article id="post-9" class="post comment depth-3" data-post="9">`, `<span class="post-author">Bob</span>`, "<p>a comment</p>", `href="/search?q=%23mine"`,
				// The time links to the comment in its thread.
				`<a class="post-time" href="/posts/5#post-9">`,
				`class="post-reply" href="/posts/9/reply"`, "@get(&#39;/posts/9/reply&#39;)",
				`class="post-edit" href="/posts/9/edit?from=post"`, `action="/posts/9/delete"`, "Delete this comment? This cannot be undone.", `name="from" value="post"`,
				`<div id="reply-slot-9" class="reply-slot"></div>`,
			},
			notWant: []string{"post-comments", "comment-replying", "post-focus", "data-init", "Delete this post"},
		},
		{
			name: "the first level",
			data: card(commentOf(9, 1, nil), nil),
			want: []string{`class="post comment depth-1"`},
		},
		{
			name:    "the deepest level that is drawn",
			data:    card(commentOf(9, 6, nil), nil),
			want:    []string{`class="post comment depth-6"`},
			notWant: []string{"comment-replying"},
		},
		{
			name:    "deeper than the cap stays at the cap and says whom it replies to",
			data:    card(commentOf(9, 8, nil), nil),
			want:    []string{`class="post comment depth-6"`, `<p class="comment-replying">replying to @alice</p>`},
			notWant: []string{"depth-7", "depth-8"},
		},
		{
			name: "deeper than the cap under a deleted comment",
			data: card(commentOf(9, 7, func(c *app.Comment) { c.ParentAuthor = app.UserRef{} }), nil),
			want: []string{`class="post comment depth-6"`, `<p class="comment-replying">replying to a deleted comment</p>`},
		},
		{
			name: "the placeholder of a deleted comment",
			data: card(deleted, nil),
			want: []string{`class="post comment depth-2 post-deleted"`, "This comment was deleted."},
			notWant: []string{
				"post-author", "post-meta", "post-body", "tags", "post-edit", "/delete", "post-reply", "reply-slot", "comment-replying", "post-comments", "This post was deleted",
			},
		},
		{
			name:    "the placeholder of a deleted comment past the cap says nothing of its parent",
			data:    card(commentOf(11, 9, func(c *app.Comment) { c.Deleted, c.HTML = true, "" }), nil),
			want:    []string{"depth-6", "This comment was deleted."},
			notWant: []string{"comment-replying", "@alice"},
		},
		{
			name: "the comment in focus is highlighted and scrolled into view",
			data: card(commentOf(9, 2, nil), func(d *views.PostCardData) { d.Focus = true }),
			want: []string{`class="post comment depth-2 post-focus"`, `data-init="el.scrollIntoView({block: &#39;center&#39;})"`},
		},
		{
			name:    "someone who has not joined has no Reply",
			data:    card(commentOf(9, 2, nil), func(d *views.PostCardData) { d.CanReply = false }),
			want:    []string{`class="post-edit"`},
			notWant: []string{"post-reply", "reply-slot"},
		},
		{
			name: "the reply editor is open in the slot",
			data: card(commentOf(9, 2, nil), func(d *views.PostCardData) {
				editor := views.ReplyEditorData(9)
				d.Reply = &editor
			}),
			want: []string{`<div id="reply-slot-9" class="reply-slot"><form id="post-editor-reply-9"`, `action="/posts/9/replies"`},
		},
		{
			name: "a message above the comment",
			data: card(commentOf(9, 2, nil), func(d *views.PostCardData) { d.Message = "You are not allowed to do that" }),
			want: []string{`<p class="form-error" role="alert">You are not allowed to do that</p>`},
		},
		{
			name:    "someone else's comment has neither Edit nor Delete",
			data:    card(commentOf(9, 2, func(c *app.Comment) { c.CanEdit, c.CanDelete = false, false }), nil),
			want:    []string{"post-reply"},
			notWant: []string{"post-edit", "/delete"},
		},
		{
			name: "an edited comment says so",
			data: card(commentOf(9, 2, func(c *app.Comment) {
				at := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
				c.EditedAt = &at
			}), nil),
			want: []string{`class="post-edited"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireContains(t, render(t, views.PostCard(tt.data)), tt.want, tt.notWant)
		})
	}
}

func TestCommentEditView(t *testing.T) {
	c := commentOf(9, 8, nil)
	editor := views.EditPostEditorData(c.Post, views.FromPost)
	tests := []struct {
		name string
		data views.PostEditData
		want []string
	}{
		{"a comment being edited keeps its indentation", views.PostEditData{Post: c.Post, Editor: editor, Depth: c.Depth}, []string{`class="post post-editing comment depth-6"`, `id="post-editor-edit-9"`}},
		{"a post being edited", views.PostEditData{Post: app.Post{ID: 5}, Editor: editor}, []string{`class="post post-editing"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			html := render(t, views.PostEdit(tt.data))
			requireContains(t, html, tt.want, nil)
		})
	}
}

func TestThreadView(t *testing.T) {
	user := app.User{ID: 2, DisplayName: "Alice", Username: "alice", Role: app.RoleMember}
	sidebar := views.SidebarData{User: user, Rooms: []app.Room{{ID: 2, Name: "general"}}, CurrentRoomID: 2}
	root := app.Post{
		ID: 5, RoomID: 2, RoomName: "general", Author: app.UserRef{ID: 2, Username: "alice", DisplayName: "Alice"},
		Format: app.FormatText, Body: "root", HTML: "<p>root body</p>", CommentCount: 2, CreatedAt: time.Date(2026, 10, 26, 9, 30, 0, 0, time.UTC),
	}
	comments := []app.Comment{
		commentOf(6, 1, func(c *app.Comment) { c.HTML = "<p>first</p>" }),
		commentOf(7, 2, func(c *app.Comment) { c.HTML = "<p>second</p>"; c.ParentID = 6 }),
	}
	data := views.PostPageData{Sidebar: sidebar, Post: root, Comments: comments, CanReply: true}
	tests := []struct {
		name    string
		data    views.PostPageData
		want    []string
		notWant []string
	}{
		{
			name:    "the root, then the comments in order",
			data:    data,
			want:    []string{`id="thread"`, "root body", "<p>first</p>", "<p>second</p>", `class="comment-list"`, "2 comments"},
			notWant: []string{"No comments yet", "Join this room to reply", "post-focus"},
		},
		{
			name: "the focus is one comment",
			data: func() views.PostPageData { d := data; d.Focus = 7; return d }(),
			want: []string{`class="post comment depth-2 post-focus"`},
		},
		{
			name:    "a visitor is told to join and has no Reply",
			data:    func() views.PostPageData { d := data; d.CanReply = false; return d }(),
			want:    []string{"Join this room to reply", "<p>first</p>"},
			notWant: []string{"post-reply", "reply-slot"},
		},
		{
			name: "the reply editor opens under the active comment only",
			data: func() views.PostPageData {
				d := data
				d.ActiveID, d.Replying, d.Editor = 6, true, views.ReplyEditorData(6)
				return d
			}(),
			want: []string{`<div id="reply-slot-6" class="reply-slot"><form id="post-editor-reply-6"`, `<div id="reply-slot-7" class="reply-slot"></div>`, `<div id="reply-slot-5" class="reply-slot"></div>`},
		},
		{
			name: "the active comment is replaced by its editor",
			data: func() views.PostPageData {
				d := data
				d.ActiveID, d.Editing, d.Editor = 7, true, views.EditPostEditorData(comments[1].Post, views.FromPost)
				return d
			}(),
			want:    []string{`class="post post-editing comment depth-2"`, `id="post-editor-edit-7"`, "<p>first</p>"},
			notWant: []string{"<p>second</p>"},
		},
		{
			name: "a message above the active comment",
			data: func() views.PostPageData {
				d := data
				d.ActiveID, d.Message = 7, "You are not allowed to do that"
				return d
			}(),
			want: []string{"You are not allowed to do that"},
		},
		{
			name: "a message without an active item is above the post",
			data: func() views.PostPageData { d := data; d.Message = "Try again"; return d }(),
			want: []string{"Try again"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			html := render(t, views.Thread(tt.data))
			requireContains(t, html, tt.want, tt.notWant)
		})
	}
	t.Run("the message of the root stays above the root", func(t *testing.T) {
		d := data
		d.Message = "Try again"
		html := render(t, views.Thread(d))
		if strings.Index(html, "Try again") > strings.Index(html, `id="comments"`) {
			t.Error("the message is below the comments")
		}
	})
}

func TestThreadPaths(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"comments of a post", views.PostCommentsPath(12), "/posts/12#comments"},
		{"a comment in its thread", views.CommentPath(12, 30), "/posts/12#post-30"},
		{"the reply editor", views.ReplyEditorData(30).Action, "/posts/30/replies"},
		{"the reply editor id", views.ReplyEditorData(30).ID, "reply-30"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}

func TestAttachmentsOfAPost(t *testing.T) {
	image := app.AttachmentView{ID: 5, FileName: "mock up.png", ContentType: "image/png", Size: 2048, IsImage: true, Width: 320, Height: 200}
	noSize := app.AttachmentView{ID: 6, FileName: "odd.gif", ContentType: "image/gif", Size: 10, IsImage: true}
	text := app.AttachmentView{ID: 7, FileName: "notes <1>.txt", ContentType: "text/plain", Size: 12 * 1024}
	pdf := app.AttachmentView{ID: 8, FileName: "ünï.pdf", ContentType: "application/pdf", Size: 3_565_158}
	tests := []struct {
		name    string
		atts    []app.AttachmentView
		want    []string
		notWant []string
	}{
		{
			name: "an image is inline with its size, lazy, and links to the file",
			atts: []app.AttachmentView{image},
			want: []string{
				`<ul class="attachment-images">`, `<a href="/files/5/mock%20up.png" target="_blank" rel="noopener">`,
				`<img src="/files/5/mock%20up.png" alt="mock up.png" loading="lazy" width="320" height="200">`,
			},
			notWant: []string{"attachment-files"},
		},
		{
			name:    "an image without a size has no width and height",
			atts:    []app.AttachmentView{noSize},
			want:    []string{`<img src="/files/6/odd.gif" alt="odd.gif" loading="lazy">`},
			notWant: []string{"width=", "height="},
		},
		{
			name: "other files are links with a human size",
			atts: []app.AttachmentView{text, pdf},
			want: []string{
				`<ul class="attachment-files" aria-label="Files">`,
				`<a href="/files/7/notes%20%3C1%3E.txt">notes &lt;1&gt;.txt</a>`, `<span class="hint">12 KB</span>`,
				`<a href="/files/8/%C3%BCn%C3%AF.pdf">ünï.pdf</a>`, `<span class="hint">3.4 MB</span>`,
			},
			notWant: []string{"<img", "attachment-images", "notes <1>"},
		},
		{
			name: "images come first, then the list of files, each in order",
			atts: []app.AttachmentView{text, image, pdf},
			want: []string{`<img src="/files/5/`, `<ul class="attachment-files"`},
		},
		{
			name:    "no files, no markup",
			atts:    nil,
			notWant: []string{"<ul", "<li"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := render(t, views.Attachments(tt.atts))
			requireContains(t, got, tt.want, tt.notWant)
			if tt.name == "images come first, then the list of files, each in order" {
				if strings.Index(got, "<img") > strings.Index(got, "attachment-files") || strings.Index(got, "notes") > strings.Index(got, "ünï") {
					t.Errorf("wrong order: %s", got)
				}
			}
		})
	}
}

func TestPostCardShowsItsAttachments(t *testing.T) {
	post := app.Post{
		ID: 3, Author: app.UserRef{Username: "alice", DisplayName: "Alice"}, Format: app.FormatText, Body: "hi", HTML: "<p>hi</p>",
		Attachments: []app.AttachmentView{
			{ID: 5, FileName: "a.png", ContentType: "image/png", Size: 100, IsImage: true, Width: 3, Height: 2},
			{ID: 6, FileName: "b.txt", ContentType: "text/plain", Size: 5},
		},
	}
	got := render(t, views.PostCard(views.PostCardData{Post: post}))
	requireContains(t, got, []string{`<img src="/files/5/a.png"`, `<a href="/files/6/b.txt">b.txt</a>`, "5 B"}, nil)
	if strings.Index(got, "<p>hi</p>") > strings.Index(got, "<img") {
		t.Errorf("the files come before the body: %s", got)
	}
	// A comment shows its files too.
	post.IsComment = true
	requireContains(t, render(t, views.PostCard(views.PostCardData{Post: post, Depth: 1})), []string{`<img src="/files/5/a.png"`}, nil)
	// A deleted post shows none.
	post.Deleted = true
	requireContains(t, render(t, views.PostCard(views.PostCardData{Post: post})), nil, []string{"/files/"})
}

func TestSizeText(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{-5, "0 B"}, {0, "0 B"}, {1, "1 B"}, {1023, "1023 B"},
		{1024, "1 KB"}, {1536, "1.5 KB"}, {12 * 1024, "12 KB"}, {10*1024 - 1, "10 KB"}, {10 * 1024, "10 KB"}, {10240 + 600, "11 KB"},
		{1024*1024 - 1, "1024 KB"}, {1024 * 1024, "1 MB"}, {3_565_158, "3.4 MB"}, {25 << 20, "25 MB"},
		{1 << 30, "1 GB"}, {5 << 30, "5 GB"}, {2048 << 30, "2048 GB"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := views.SizeText(tt.n); got != tt.want {
				t.Errorf("SizeText(%d) = %q, want %q", tt.n, got, tt.want)
			}
		})
	}
}

func TestFilePath(t *testing.T) {
	tests := []struct {
		name string
		a    app.AttachmentView
		want string
	}{
		{"plain", app.AttachmentView{ID: 3, FileName: "a.txt"}, "/files/3/a.txt"},
		{"spaces and slashes", app.AttachmentView{ID: 3, FileName: "a b/c?d#e.txt"}, "/files/3/a%20b%2Fc%3Fd%23e.txt"},
		{"non-ASCII", app.AttachmentView{ID: 12, FileName: "é.txt"}, "/files/12/%C3%A9.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := views.FilePath(tt.a); got != tt.want {
				t.Errorf("FilePath = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStreamPaths(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"a room", views.RoomStreamPath(7, 31, 0, 0), "/rooms/7/stream?seen=31"},
		{"a room without posts", views.RoomStreamPath(7, 0, 0, 0), "/rooms/7/stream?seen=0"},
		{"a page of older posts of a room", views.RoomStreamPath(7, 31, 20, 0), "/rooms/7/stream?seen=31&before=20"},
		{"a room with the line of new posts", views.RoomStreamPath(7, 31, 0, 25), "/rooms/7/stream?seen=31&line=25"},
		{"a post", views.PostStreamPath(12, 40), "/posts/12/stream?seen=40"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}

func TestNewestID(t *testing.T) {
	comments := []app.Comment{{Post: app.Post{ID: 9}}, {Post: app.Post{ID: 14}}, {Post: app.Post{ID: 11}}}
	tests := []struct {
		name string
		got  uint64
		want uint64
	}{
		{"a feed has its newest post first", views.FeedData{Page: app.FeedPage{Posts: []app.Post{{ID: 30}, {ID: 20}}}}.NewestID(), 30},
		{"an empty feed", views.FeedData{}.NewestID(), 0},
		{"a thread without comments", views.PostPageData{Post: app.Post{ID: 5}}.NewestID(), 5},
		{"a thread with comments", views.PostPageData{Post: app.Post{ID: 5}, Comments: comments}.NewestID(), 14},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %d, want %d", tt.got, tt.want)
			}
		})
	}
}

func TestPagesOpenTheirStream(t *testing.T) {
	user := app.User{ID: 2, DisplayName: "Alice", Username: "alice", Role: app.RoleMember}
	room := app.Room{ID: 7, Name: "general", IsMember: true}
	tests := []struct {
		name string
		c    templ.Component
		want []string
	}{
		{
			name: "the room page",
			c: views.RoomPage(views.RoomData{
				Sidebar: views.SidebarData{User: user}, Viewer: user, Room: room, Editor: views.NewPostEditorData(7),
				Feed: views.FeedData{RoomID: 7, Page: app.FeedPage{Posts: []app.Post{{ID: 31}}}},
			}),
			want: []string{
				`<div id="live-stream" hidden data-init="@get(&#39;/rooms/7/stream?seen=31&#39;, {headers: {&#39;X-Page-Title&#39;: encodeURIComponent(document.title)}, retry: &#39;always&#39;, retryMaxCount: 1000, openWhenHidden: true})"></div>`,
				`id="post-composer"`, `id="room-members-count"`, `<aside id="sidebar" popover>`,
			},
		},
		{
			name: "the post page",
			c: views.PostPage(views.PostPageData{
				Sidebar: views.SidebarData{User: user}, Post: app.Post{ID: 12, RoomID: 7, RoomName: "general"},
				Comments: []app.Comment{{Post: app.Post{ID: 15}}},
			}),
			want: []string{`data-init="@get(&#39;/posts/12/stream?seen=15&#39;, {headers:`, `id="comments"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireContains(t, render(t, tt.c), tt.want, nil)
		})
	}
}

func TestPostContentLeavesTheReplySlotAlone(t *testing.T) {
	post := app.Post{ID: 5, RoomID: 2, Author: app.UserRef{ID: 1, Username: "alice", DisplayName: "Alice"}, HTML: template.HTML("<p>hi</p>")}
	editor := views.ReplyEditorData(5)
	card := views.PostCardData{Post: post, CanReply: true, Reply: &editor}
	content := render(t, views.PostContent(card))
	requireContains(t, content, []string{`<div id="post-content-5" class="post-content">`, "<p>hi</p>", "post-footer"},
		[]string{"<article", "reply-slot", "<textarea"})
	// The card is the article with the content, then the slot with the editor.
	requireContains(t, render(t, views.PostCard(card)), []string{`<article id="post-5"`, `id="post-content-5"`, `id="reply-slot-5"`, "<textarea"}, nil)
}
