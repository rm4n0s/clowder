package views

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
)

// The values of the form field "from" of the actions on a post: where the post
// is shown, so that the answer fits the page.
const (
	// FromPost says the post is shown on its own page.
	FromPost = "post"
)

// PostEditorData is what the post editor shows. The room page uses it for the
// new post, the feed for the edit in place; the comments of step 5.2 and the
// attachments and tasks of M6 and M9 add their fields here.
type PostEditorData struct {
	// ID tells the editors of a page apart: "new" for the new post, "edit-12"
	// for the edit of the post 12. The ids of its elements end with it.
	ID string
	// Action is the path the form posts to.
	Action string
	// Format is the value of the Text / Markdown switch: "markdown" or "text".
	Format string
	// Body is the text in the body field.
	Body string
	// Tags is the text in the tags field: tags separated by spaces or commas.
	Tags string
	// Errors are the problems of the last submit; the fields are named body,
	// format and tags.
	Errors FormErrors
	// SubmitLabel is the text of the submit button.
	SubmitLabel string
	// CancelPath is the path that restores what the editor replaced, which
	// Datastar fetches; empty for an editor that replaces nothing (it has no
	// Cancel link).
	CancelPath string
	// CancelHref is where the Cancel link goes without JavaScript.
	CancelHref string
	// From says where the post is shown (FromPost), for an edit in place.
	From string
}

// NewPostEditorData is the empty editor that writes a post in the room.
func NewPostEditorData(roomID uint64) PostEditorData {
	return PostEditorData{
		ID:          "new",
		Action:      RoomPath(roomID) + "/posts",
		Format:      string(app.FormatMarkdown),
		SubmitLabel: "Post",
	}
}

// EditPostEditorData is the editor that edits the post, filled with its body,
// format and tags. from says where the post is shown (FromPost or empty).
func EditPostEditorData(p app.Post, from string) PostEditorData {
	d := PostEditorData{
		ID:          "edit-" + strconv.FormatUint(p.ID, 10),
		Action:      PostPath(p.ID) + "/edit",
		Format:      string(p.Format),
		Body:        p.Body,
		Tags:        strings.Join(p.Tags, " "),
		SubmitLabel: "Save",
		CancelPath:  postActionPath(p.ID, "view", from),
		CancelHref:  PostPath(p.ID),
		From:        from,
	}
	return d
}

// postActionPath returns the path of an action on a post, with the query that
// says where the post is shown when it is on its own page.
func postActionPath(id uint64, action, from string) string {
	p := PostPath(id) + "/" + action
	if from != "" {
		p += "?from=" + url.QueryEscape(from)
	}
	return p
}

// PostCardData is what one post shows.
type PostCardData struct {
	// Post is the post.
	Post app.Post
	// From says where the post is shown (FromPost or empty): the delete form
	// takes the person to the room when the post page itself is gone.
	From string
	// Message is the problem of the last action on the post (edit, delete),
	// shown above it; empty shows none.
	Message string
}

// PostEditData is the post replaced by its editor.
type PostEditData struct {
	// Post is the post being edited.
	Post app.Post
	// Editor is the editor, filled for this post.
	Editor PostEditorData
}

// FeedData is a page of the feed of a room.
type FeedData struct {
	// RoomID is the ID of the room.
	RoomID uint64
	// Page holds the posts, newest first, and where the next page starts.
	Page app.FeedPage
	// Before is the beforeID that asked for this page; 0 for the first page.
	Before uint64
}

// PostPageData is what the page of a post shows.
type PostPageData struct {
	// Sidebar is the sidebar of the page; the room of the post is highlighted.
	Sidebar SidebarData
	// Post is the post.
	Post app.Post
	// Message is the problem of the last action on the post, shown above it.
	Message string
	// Editing is true when the post is replaced by its editor.
	Editing bool
	// Editor is the editor, used when Editing is true.
	Editor PostEditorData
}

// PostPath returns the path of the page of a post, /posts/<id>. Its actions
// are PostPath + "/edit", "/view" and "/delete".
func PostPath(id uint64) string {
	return "/posts/" + strconv.FormatUint(id, 10)
}

// FeedPath returns the path that loads the posts older than before, in the
// feed of the room, as a Datastar patch.
func FeedPath(roomID, before uint64) string {
	return RoomPath(roomID) + "/feed?before=" + strconv.FormatUint(before, 10)
}

// OlderPostsPath returns the path of the page of the room that shows the posts
// older than before, for a browser without JavaScript.
func OlderPostsPath(roomID, before uint64) string {
	return RoomPath(roomID) + "?before=" + strconv.FormatUint(before, 10)
}

// TagSearchPath returns the path of the search for a tag, /search?q=%23tag.
func TagSearchPath(tag string) string {
	return "/search?q=" + url.QueryEscape("#"+tag)
}

// postTimeLayout is how the time of a post is shown, in UTC.
const postTimeLayout = "2006-01-02 15:04"

// postTimeText is the time of a post as shown, in UTC.
func postTimeText(t time.Time) string {
	return t.UTC().Format(postTimeLayout)
}

// postTimeStamp is the time as the datetime attribute of a time element.
func postTimeStamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// commentCountText is "0 comments", "1 comment" or "5 comments".
func commentCountText(n int) string {
	if n == 1 {
		return "1 comment"
	}
	return strconv.Itoa(n) + " comments"
}

// previewAction is the Datastar action that sends the form it is in to the
// preview of the body.
const previewAction = "@post('/preview', {contentType: 'form'})"

// suggestAction returns the Datastar action that asks for the tags that start
// like the tag being typed, which is the last piece of the field, for the
// editor with the id. The id is made of letters, digits and "-", so it needs
// no escaping.
func suggestAction(editorID string) string {
	return "@get('/tags/suggest?editor=" + escapeQuote(editorID) +
		"&q=' + encodeURIComponent(el.value.split(',').join(' ').split(' ').pop()))"
}

// pickTagAction is the Datastar expression of a suggestion: it puts the tag in
// the data-tag attribute of the button in place of the piece being typed in the
// tags field of the form, and clears the list.
const pickTagAction = "const f = el.closest('form').elements.tags; const v = f.value; " +
	"const i = Math.max(v.lastIndexOf(' '), v.lastIndexOf(',')) + 1; " +
	"f.value = v.slice(0, i) + el.dataset.tag + ' '; f.focus(); el.closest('ul').replaceChildren()"

// loadMoreAction returns the Datastar action that loads the posts older than
// before into the feed of the room.
func loadMoreAction(roomID, before uint64) string {
	return "@get('" + escapeQuote(FeedPath(roomID, before)) + "')"
}

// closePreviewAction is the Datastar expression that hides the preview box it
// is in.
const closePreviewAction = "el.closest('.post-preview').hidden = true"
