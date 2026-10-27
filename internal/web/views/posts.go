package views

import (
	"net/url"
	"slices"
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

// MaxVisualDepth is the deepest level a comment is indented to. A comment
// deeper than that stays at this level and says whom it replies to (SPEC §7.3).
const MaxVisualDepth = 6

// PostEditorData is what the post editor shows. The room page uses it for the
// new post, the post page for the edit in place and for the replies; the
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
	// CancelClose makes the Cancel link close the reply slot the editor is in,
	// in the browser, without asking the server (CancelHref is its target
	// without JavaScript).
	CancelClose bool
	// From says where the post is shown (FromPost), for an edit in place.
	From string
	// Attachments are the files the post has now, listed with a Remove box
	// each; nil for an editor that writes a new post.
	Attachments []app.AttachmentView
	// Removing holds the IDs of the attachments whose Remove box is ticked,
	// kept from a submit that failed.
	Removing map[uint64]bool
	// RoomID is the ID of the room the post is written in; the Add task button
	// asks the server for a row with the members of that room.
	RoomID uint64
	// Tasks are the task rows of the editor: the tasks of the post when it is
	// edited, or the rows typed in a submit that failed. The errors of the
	// rows are the message of the field "tasks".
	Tasks []TaskRowData
	// Assignable are the people the assignee pickers offer: the members of the
	// room and, when a post is edited, the former members who are assigned to
	// one of its tasks (see WithMembers).
	Assignable []AssigneeOption
	// assigned are the people assigned to the tasks the post has now, with
	// their names, so that WithMembers can keep those who left the room.
	assigned []app.UserRef
}

// TaskRowData is one task row of the editor.
type TaskRowData struct {
	// ID is the ID of the task for a row that edits one; 0 for a new task.
	ID uint64
	// Title is the text in the title field.
	Title string
	// Assignees are the IDs of the people selected in the picker.
	Assignees []uint64
	// Due is the due date as the date field holds it ("2006-01-02"), or empty.
	Due string
}

// AssigneeOption is a person the assignee picker of a task row offers.
type AssigneeOption struct {
	// ID is the ID of the user, the value of the option.
	ID uint64
	// Label is the text of the option.
	Label string
}

// dueLayout is how a due date is written in a date field and in a task list.
const dueLayout = "2006-01-02"

// AssigneeOptions makes the options of the assignee picker from the members of
// a room, in their order.
func AssigneeOptions(members []app.Member) []AssigneeOption {
	out := make([]AssigneeOption, len(members))
	for i, m := range members {
		out[i] = AssigneeOption{ID: m.UserID, Label: m.DisplayName + " (@" + m.Username + ")"}
	}
	return out
}

// WithMembers returns the editor for the room, with the members of the room
// offered in the assignee pickers. People assigned to the tasks of the post
// who are no longer members stay in the pickers, marked as having left, so
// that an edit can keep them (SPEC §7.6).
func (d PostEditorData) WithMembers(roomID uint64, members []app.Member) PostEditorData {
	d.RoomID = roomID
	d.Assignable = AssigneeOptions(members)
	for _, u := range d.assigned {
		if !slices.ContainsFunc(d.Assignable, func(o AssigneeOption) bool { return o.ID == u.ID }) {
			d.Assignable = append(d.Assignable, AssigneeOption{ID: u.ID, Label: u.DisplayName + " (@" + u.Username + ", left the room)"})
		}
	}
	return d
}

// taskRowsOf makes the rows of the editor from the tasks a post has.
func taskRowsOf(tasks []app.TaskCard) (rows []TaskRowData, assigned []app.UserRef) {
	for _, t := range tasks {
		row := TaskRowData{ID: t.ID, Title: t.Title}
		for _, a := range t.Assignees {
			row.Assignees = append(row.Assignees, a.ID)
			if !slices.ContainsFunc(assigned, func(u app.UserRef) bool { return u.ID == a.ID }) {
				assigned = append(assigned, a)
			}
		}
		if t.DueDate != nil {
			row.Due = t.DueDate.UTC().Format(dueLayout)
		}
		rows = append(rows, row)
	}
	return rows, assigned
}

// taskRowsID is the id of the list of task rows of an editor, which the Add
// task button appends to.
func taskRowsID(editorID string) string {
	return "post-task-rows-" + editorID
}

// TaskRowsSelector is the selector of the list of task rows of the editor.
func TaskRowsSelector(editorID string) string {
	return "#" + taskRowsID(editorID)
}

// taskFieldName is the name of a field of the task row at the index:
// tasks[<index>][<field>].
func taskFieldName(index int, field string) string {
	return "tasks[" + strconv.Itoa(index) + "][" + field + "]"
}

// taskInputID is the id of a field of the task row at the index of an editor.
func taskInputID(editorID string, index int, field string) string {
	return "post-task-" + editorID + "-" + strconv.Itoa(index) + "-" + field
}

// hasAssignee reports whether the row has the user selected.
func (r TaskRowData) hasAssignee(id uint64) bool {
	return slices.Contains(r.Assignees, id)
}

// addTaskAction is the Datastar expression of the Add task button: it takes
// the next index of the rows from the list (so that a removed row never makes
// two rows share one) and asks the server for an empty row with that index,
// which is appended to the list.
func addTaskAction(d PostEditorData) string {
	return "const c = document.getElementById('" + taskRowsID(d.ID) + "'); const i = c.dataset.next; c.dataset.next = Number(i) + 1; " +
		"@get('/editor/task-row?editor=" + escapeQuote(d.ID) + "&room=" + strconv.FormatUint(d.RoomID, 10) + "&index=' + i)"
}

// removeTaskAction is the Datastar expression of the Remove button of a task
// row: the row leaves the form, so that a saved edit deletes its task.
const removeTaskAction = "el.closest('.task-row').remove()"

// TaskBoardPath returns the path of a task on the board of its room, which
// the board page opens at its card: /rooms/<room>/board#task-<id>.
func TaskBoardPath(roomID, taskID uint64) string {
	return RoomPath(roomID) + "/board#task-" + strconv.FormatUint(taskID, 10)
}

// taskAssigneesText writes the names of the assignees of a task, separated by
// commas.
func taskAssigneesText(assignees []app.UserRef) string {
	names := make([]string, len(assignees))
	for i, a := range assignees {
		names[i] = a.DisplayName
	}
	return strings.Join(names, ", ")
}

// taskDueText is the due date of a task as shown.
func taskDueText(t app.TaskCard) string {
	if t.DueDate == nil {
		return ""
	}
	return t.DueDate.UTC().Format(dueLayout)
}

// taskClasses are the classes of an item of the task list of a post.
func taskClasses(t app.TaskCard) string {
	if t.Done {
		return "post-task post-task-done"
	}
	return "post-task"
}

// taskColumnClasses are the classes of the badge with the column of a task:
// the last column is styled as done.
func taskColumnClasses(t app.TaskCard) string {
	if t.Done {
		return "task-column task-column-done"
	}
	return "task-column"
}

// taskDueTitle is the hover text of the due date of a task.
func taskDueTitle(t app.TaskCard) string {
	if t.Overdue {
		return "Overdue"
	}
	return "Due date"
}

// taskDueClasses are the classes of the due date of a task: red when overdue.
func taskDueClasses(t app.TaskCard) string {
	if t.Overdue {
		return "task-due overdue"
	}
	return "task-due"
}

// NewPostEditorData is the empty editor that writes a post in the room.
func NewPostEditorData(roomID uint64) PostEditorData {
	return PostEditorData{
		ID:          "new",
		RoomID:      roomID,
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
		Attachments: p.Attachments,
		SubmitLabel: "Save",
		CancelPath:  postActionPath(p.ID, "view", from),
		CancelHref:  PostPath(p.ID),
		From:        from,
		RoomID:      p.RoomID,
	}
	d.Tasks, d.assigned = taskRowsOf(p.Tasks)
	return d
}

// ReplyEditorData is the empty editor that writes a comment under the post or
// comment parentID. It opens in the reply slot under that item.
func ReplyEditorData(parentID uint64) PostEditorData {
	return PostEditorData{
		ID:          "reply-" + strconv.FormatUint(parentID, 10),
		Action:      PostPath(parentID) + "/replies",
		Format:      string(app.FormatMarkdown),
		SubmitLabel: "Reply",
		CancelHref:  PostPath(parentID),
		CancelClose: true,
	}
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

// PostCardData is what one post or comment shows.
type PostCardData struct {
	// Post is the post or comment.
	Post app.Post
	// From says where the post is shown (FromPost or empty): the delete form
	// takes the person to the room when the post page itself is gone.
	From string
	// Message is the problem of the last action on the post (edit, delete),
	// shown above it; empty shows none.
	Message string
	// Depth is how deep a comment is in its thread (1 for a reply to the post);
	// 0 for a post.
	Depth int
	// ParentAuthor is the author of the item a comment replies to; the zero
	// value when that is deleted. It is shown for a comment deeper than
	// MaxVisualDepth.
	ParentAuthor app.UserRef
	// Focus marks the comment that the page was opened for: highlighted and
	// scrolled into view.
	Focus bool
	// CanReply shows the Reply button (and holds the reply slot): the post is
	// shown on its own page to a member of its room.
	CanReply bool
	// Reply is the editor open in the reply slot under the item; nil for an
	// empty slot.
	Reply *PostEditorData
}

// reactions makes the data of the reaction bar of the card.
func (d PostCardData) reactions() ReactionsData {
	return ReactionsData{Post: d.Post, From: d.From}
}

// ReactionsData is what the reaction bar of a post or comment shows.
type ReactionsData struct {
	// Post is the post or comment: its reactions, and whether the person may
	// react.
	Post app.Post
	// From says where the post is shown (FromPost or empty), so that a plain
	// form post is sent back to the right page.
	From string
	// Message is the problem of the last reaction, shown above the bar (the
	// person may not react); empty shows none.
	Message string
	// Problem is the message of the emoji field of the last reaction that was
	// not an emoji; the picker is open and shows it (see ReactionPicker).
	Problem string
	// Typed is what the person typed in the emoji field, kept with Problem.
	Typed string
}

// pickerEmoji are the emoji of the picker of the reaction bar, in the order
// they are shown (SPEC §7.4).
var pickerEmoji = []string{
	"\U0001F44D", "\U0001F44E", "\U0001F600", "\U0001F602", "\U0001F389", "\u2764\uFE0F",
	"\U0001F64F", "\U0001F440", "\U0001F525", "\U0001F680", "\u2705", "\u274C",
	"\U0001F914", "\U0001F62E", "\U0001F622", "\U0001F621", "\U0001F4AF", "\U0001F44F",
	"\U0001F64C", "\U0001F4A1", "\u2B50", "\U0001F355", "\u2615", "\U0001F41B",
}

// reactionNamesShown is how many names the title of a reaction lists; the rest
// are counted.
const reactionNamesShown = 20

// reactionsID is the id of the reaction bar of a post or comment.
func reactionsID(id uint64) string {
	return "reactions-" + roomIDText(id)
}

// reactionPickerID is the id of the picker of the reaction bar of a post or
// comment, which the live changes of the reactions do not patch.
func reactionPickerID(id uint64) string {
	return "reaction-picker-" + roomIDText(id)
}

// reactionsPath is the path that adds or removes a reaction of a post or
// comment.
func reactionsPath(id uint64) string {
	return PostPath(id) + "/reactions"
}

// reactionClasses are the classes of the chip of a reaction.
func reactionClasses(r app.ReactionCount) string {
	if r.Mine {
		return "reaction-chip mine"
	}
	return "reaction-chip"
}

// reactionTitle is the hover text of a reaction: who reacted, the first 20 by
// name and the rest counted.
func reactionTitle(r app.ReactionCount) string {
	names := r.Names
	more := 0
	if len(names) > reactionNamesShown {
		names, more = names[:reactionNamesShown], len(names)-reactionNamesShown
	}
	title := strings.Join(names, ", ")
	if more > 0 {
		title += " and " + strconv.Itoa(more) + " more"
	}
	return title
}

// reactionLabel is the accessible name of the chip of a reaction.
func reactionLabel(r app.ReactionCount) string {
	label := r.Emoji + " " + strconv.Itoa(r.Count)
	if r.Mine {
		label += ", you reacted"
	}
	return label
}

// NewestID is the ID of the newest post of the page, 0 when it has none. The
// stream of the page is told it, to find out what was written while the page
// was loading.
func (d FeedData) NewestID() uint64 {
	if len(d.Page.Posts) == 0 {
		return 0
	}
	return d.Page.Posts[0].ID
}

// PostEditData is the post replaced by its editor.
type PostEditData struct {
	// Post is the post being edited.
	Post app.Post
	// Editor is the editor, filled for this post.
	Editor PostEditorData
	// Depth is how deep the post is, when it is a comment (see PostCardData).
	Depth int
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
	// Post is the top-level post of the thread.
	Post app.Post
	// Comments are the comments of the thread, depth first.
	Comments []app.Comment
	// Focus is the ID of the comment the page was opened for; 0 for the post.
	Focus uint64
	// CanReply is true for a member of the room: the items have Reply buttons.
	// Anyone else is told to join the room.
	CanReply bool
	// ActiveID is the ID of the post or comment that Message, Editing and
	// Replying are about; 0 is the top-level post.
	ActiveID uint64
	// Message is the problem of the last action on the active item, shown above
	// it.
	Message string
	// Editing is true when the active item is replaced by its editor.
	Editing bool
	// Replying is true when the active item has the editor in Editor open in
	// its reply slot.
	Replying bool
	// Editor is the editor, used when Editing or Replying is true.
	Editor PostEditorData
}

// activeID is the ID of the item that Message, Editing and Replying are about.
func (d PostPageData) activeID() uint64 {
	if d.ActiveID == 0 {
		return d.Post.ID
	}
	return d.ActiveID
}

// card makes the data of the card of an item of the thread.
func (d PostPageData) card(c PostCardData) PostCardData {
	c.From = FromPost
	c.CanReply = d.CanReply
	c.Focus = d.Focus != 0 && d.Focus == c.Post.ID
	if c.Post.ID == d.activeID() {
		c.Message = d.Message
		if d.Replying {
			editor := d.Editor
			c.Reply = &editor
		}
	}
	return c
}

// RootCard is the card of the top-level post of the page.
func (d PostPageData) RootCard() PostCardData {
	return d.card(PostCardData{Post: d.Post})
}

// CommentCard is the card of a comment of the thread.
func (d PostPageData) CommentCard(c app.Comment) PostCardData {
	return d.card(PostCardData{Post: c.Post, Depth: c.Depth, ParentAuthor: c.ParentAuthor})
}

// NewestID is the largest ID among the top-level post and the comments of the
// thread: the newest item the page shows. The stream of the page is told it,
// to find out what was written while the page was loading.
func (d PostPageData) NewestID() uint64 {
	newest := d.Post.ID
	for _, c := range d.Comments {
		newest = max(newest, c.ID)
	}
	return newest
}

// PostStreamPath returns the path of the live stream of the thread of the
// top-level post rootID. newest is the ID of the newest item the page showed
// (PostPageData.NewestID).
func PostStreamPath(rootID, newest uint64) string {
	return PostPath(rootID) + "/stream?seen=" + strconv.FormatUint(newest, 10)
}

// RoomStreamPath returns the path of the live stream of the room. newest is
// the ID of the newest post the page showed, 0 for none; before is the beforeID
// of the page of the feed (0 for the newest posts), which a stream does not
// keep up to date.
func RoomStreamPath(roomID, newest, before uint64) string {
	path := RoomPath(roomID) + "/stream?seen=" + strconv.FormatUint(newest, 10)
	if before != 0 {
		path += "&before=" + strconv.FormatUint(before, 10)
	}
	return path
}

// postContentID is the id of the element that holds the content of the card of
// a post or comment (see PostContent).
func postContentID(id uint64) string {
	return "post-content-" + roomIDText(id)
}

// streamAction is the Datastar expression that opens the live stream at path.
// A stream that ends is opened again after a second, however it ended (the
// default is to retry only after an error), and on every attempt the client
// sends the id of the last event, which tells the server that this is not the
// first connection. A hidden page keeps its stream.
func streamAction(path string) string {
	return "@get('" + escapeQuote(path) + "', {retry: 'always', retryMaxCount: 1000, openWhenHidden: true})"
}

// PostPath returns the path of the page of a post, /posts/<id>. Its actions
// are PostPath + "/edit", "/view" and "/delete".
func PostPath(id uint64) string {
	return "/posts/" + strconv.FormatUint(id, 10)
}

// PostCommentsPath returns the path of the comments of a post, the page of the
// post at its thread: /posts/<id>#comments.
func PostCommentsPath(id uint64) string {
	return PostPath(id) + "#comments"
}

// CommentPath returns the path that shows a comment in its thread, on the page
// of the top-level post: /posts/<root>#post-<id>.
func CommentPath(rootID, id uint64) string {
	return PostPath(rootID) + "#" + postElementID(id)
}

// timePath is where the time of a post links to: its page, or for a comment
// its place in the thread.
func timePath(p app.Post) string {
	if p.IsComment && p.RootID != 0 {
		return CommentPath(p.RootID, p.ID)
	}
	return PostPath(p.ID)
}

// replyPath is the path that opens the reply editor under a post or comment.
func replyPath(id uint64) string {
	return PostPath(id) + "/reply"
}

// visualDepth is the level a comment is indented to: its depth, at most
// MaxVisualDepth.
func visualDepth(depth int) int {
	return min(depth, MaxVisualDepth)
}

// cardClasses are the classes of the article of a post or comment.
func cardClasses(d PostCardData) string {
	classes := "post"
	if d.Post.IsComment {
		classes += " comment " + depthClass(d.Depth)
	}
	if d.Post.Deleted {
		classes += " post-deleted"
	}
	if d.Focus {
		classes += " post-focus"
	}
	return classes
}

// depthClass is the class of the indentation of a comment of the depth.
func depthClass(depth int) string {
	return "depth-" + strconv.Itoa(max(visualDepth(depth), 1))
}

// editClasses are the classes of the article of a post or comment replaced by
// its editor.
func editClasses(d PostEditData) string {
	classes := "post post-editing"
	if d.Post.IsComment {
		classes += " comment " + depthClass(d.Depth)
	}
	return classes
}

// replyingToCapped reports whether the card says whom the comment replies to:
// it is deeper than MaxVisualDepth, so the indentation does not show it.
func replyingToCapped(d PostCardData) bool {
	return d.Post.IsComment && !d.Post.Deleted && d.Depth > MaxVisualDepth
}

// deletedNote is the text of the placeholder of a deleted post or comment.
func deletedNote(p app.Post) string {
	if p.IsComment {
		return "This comment was deleted."
	}
	return "This post was deleted."
}

// deleteConfirm is the question asked before a post or comment is deleted.
func deleteConfirm(p app.Post) string {
	if p.IsComment {
		return "Delete this comment? This cannot be undone."
	}
	return "Delete this post? This cannot be undone."
}

// cancelAction is the Datastar expression of the Cancel link of an editor: it
// asks the server for what the editor replaced, or closes the reply slot.
func cancelAction(d PostEditorData) string {
	if d.CancelClose {
		return closeReplyAction
	}
	return "@get('" + escapeQuote(d.CancelPath) + "')"
}

// scrollIntoViewAction is the Datastar expression that scrolls the focused
// comment into view when the page opens.
const scrollIntoViewAction = "el.scrollIntoView({block: 'center'})"

// closeReplyAction is the Datastar expression that closes the reply editor it
// is in.
const closeReplyAction = "el.closest('.reply-slot').replaceChildren()"

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

// FilePath returns the path that downloads an attachment, /files/<id>/<name>.
// The name only gives the download a good name; the ID decides.
func FilePath(a app.AttachmentView) string {
	return "/files/" + strconv.FormatUint(a.ID, 10) + "/" + url.PathEscape(a.FileName)
}

// SizeText writes a size in bytes for people: "512 B", "12 KB", "3.4 MB". The
// units are powers of 1024; a value of 10 or more has no decimals, a smaller
// one has one unless it is whole.
func SizeText(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(max(n, 0), 10) + " B"
	}
	value := float64(n)
	unit := ""
	for _, u := range []string{"KB", "MB", "GB"} {
		value /= 1024
		unit = u
		if value < 1024 {
			break
		}
	}
	text := strconv.FormatFloat(value, 'f', 1, 64)
	if value >= 9.95 || strings.HasSuffix(text, ".0") {
		text = strconv.FormatFloat(value, 'f', 0, 64)
	}
	return text + " " + unit
}

// splitAttachments divides the attachments into the images that are shown
// inline and the other files, each in its order.
func splitAttachments(atts []app.AttachmentView) (images, others []app.AttachmentView) {
	for _, a := range atts {
		if a.IsImage {
			images = append(images, a)
		} else {
			others = append(others, a)
		}
	}
	return images, others
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
// The file input is switched off while the request is made, so that the files
// are not sent with every preview (a disabled input is not part of the form).
const previewAction = "const f = el.closest('form').elements.files; f.disabled = true; " +
	"@post('/preview', {contentType: 'form'}); f.disabled = false"

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
