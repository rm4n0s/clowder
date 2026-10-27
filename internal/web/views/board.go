package views

import (
	"hash/fnv"
	"slices"
	"strconv"
	"strings"

	"github.com/rm4n0s/clowder/internal/app"
)

// BoardData is what the page of the board of a room shows.
type BoardData struct {
	// Sidebar is the sidebar of the page.
	Sidebar SidebarData
	// Board is the board as the viewer sees it: its room, the columns with the
	// cards, and whether the viewer may change it.
	Board app.BoardView
	// Message is the problem of the last action on the board, shown above the
	// columns; empty shows none.
	Message string
	// Editor is the form of the card being edited; nil when none is open.
	Editor *BoardEditorData
}

// BoardEditorData is the form that edits one card. It lives in its own panel
// outside the columns, so that a live update of the columns never closes it
// or clears what is typed in it.
type BoardEditorData struct {
	// Task is the card being edited, as the board shows it.
	Task app.TaskCard
	// Title is the text in the title field.
	Title string
	// Assignees are the IDs of the people selected in the picker.
	Assignees []uint64
	// Due is the due date as the date field holds it ("2006-01-02"), or empty.
	Due string
	// ClearDue is true when the box that removes the due date is ticked.
	ClearDue bool
	// Options are the people the picker offers: the members of the room and
	// the former members who are assigned to the card.
	Options []AssigneeOption
	// Errors are the problems of the last submit; the fields are named title,
	// assignees and dueDate.
	Errors FormErrors
}

// NewBoardEditorData makes the editor of a card, filled with what the card
// has now. members are the members of the room; people assigned to the card
// who are no longer members stay in the picker, marked as having left, so
// that a save keeps them (SPEC §7.6).
func NewBoardEditorData(task app.TaskCard, members []app.Member) BoardEditorData {
	d := BoardEditorData{Task: task, Title: task.Title, Options: assigneeOptionsFor(members, task.Assignees)}
	for _, a := range task.Assignees {
		d.Assignees = append(d.Assignees, a.ID)
	}
	if task.DueDate != nil {
		d.Due = task.DueDate.UTC().Format(dueLayout)
	}
	return d
}

// assigneeOptionsFor makes the options of an assignee picker: the members of
// the room, then the people in assigned who are not among them, marked as
// having left the room.
func assigneeOptionsFor(members []app.Member, assigned []app.UserRef) []AssigneeOption {
	options := AssigneeOptions(members)
	for _, u := range assigned {
		if !slices.ContainsFunc(options, func(o AssigneeOption) bool { return o.ID == u.ID }) {
			options = append(options, AssigneeOption{ID: u.ID, Label: u.DisplayName + " (@" + u.Username + ", left the room)"})
		}
	}
	return options
}

// hasAssignee reports whether the editor has the user selected.
func (d BoardEditorData) hasAssignee(id uint64) bool {
	return slices.Contains(d.Assignees, id)
}

// BoardPath returns the path of the board of a room, /rooms/<id>/board. Its
// actions are BoardPath + "/columns" and "/tasks/<id>"; its stream is
// BoardPath + "/stream".
func BoardPath(roomID uint64) string {
	return RoomPath(roomID) + "/board"
}

// boardTaskPath returns the path of an action on a card of the board of a
// room: "" for the save of the edit, "move", or "edit" for the form.
func boardTaskPath(roomID, taskID uint64, action string) string {
	path := BoardPath(roomID) + "/tasks/" + strconv.FormatUint(taskID, 10)
	if action != "" {
		path += "/" + action
	}
	return path
}

// boardColumnPath returns the path of an action on a column of the board of a
// room: "rename", "move" or "delete".
func boardColumnPath(roomID, columnID uint64, action string) string {
	return BoardPath(roomID) + "/columns/" + strconv.FormatUint(columnID, 10) + "/" + action
}

// BoardColumnsPath returns the path that adds a column to the board of a room
// and, with GET, shows the columns alone (what the board asks for when a drag
// ends without a drop).
func BoardColumnsPath(roomID uint64) string {
	return BoardPath(roomID) + "/columns"
}

// BoardStreamPath returns the path of the live stream of the board. version
// is the BoardVersion of the board the page showed; the stream sends the
// columns at once when the board changed since.
func BoardStreamPath(roomID uint64, version string) string {
	return BoardPath(roomID) + "/stream?v=" + version
}

// BoardVersion is a short text that changes when anything the board shows
// changes: the columns and the cards with their places, titles, assignees and
// due dates, and whether the viewer may change the board. The page tells it to
// its stream, which finds out what happened while the page was loading.
func BoardVersion(b app.BoardView) string {
	h := fnv.New64a()
	var sb strings.Builder
	sb.WriteString(strconv.FormatBool(b.CanEdit))
	for _, c := range b.Columns {
		sb.WriteString("|c" + strconv.FormatUint(c.ID, 10) + ":" + c.Name + ":" + strconv.Itoa(c.Position))
		for _, t := range c.Tasks {
			sb.WriteString("|t" + strconv.FormatUint(t.ID, 10) + ":" + t.Title + ":" + strconv.Itoa(t.Position))
			if t.DueDate != nil {
				sb.WriteString(":" + t.DueDate.UTC().Format(dueLayout))
			}
			for _, a := range t.Assignees {
				sb.WriteString(":" + strconv.FormatUint(a.ID, 10))
			}
			sb.WriteString(":" + strconv.FormatBool(t.Overdue))
		}
	}
	// The hash writer never fails to write.
	_, _ = h.Write([]byte(sb.String()))
	return strconv.FormatUint(h.Sum64(), 36)
}

// boardCardClasses are the classes of a card of the board: the cards of the
// last column are styled as done.
func boardCardClasses(t app.TaskCard) string {
	if t.Done {
		return "board-card board-card-done"
	}
	return "board-card"
}

// boardColumnClasses are the classes of a column of the board.
func boardColumnClasses(c app.ColumnView) string {
	if c.Last {
		return "board-column board-column-done"
	}
	return "board-column"
}

// cardCountText is "1 card" or "3 cards".
func cardCountText(n int) string {
	if n == 1 {
		return "1 card"
	}
	return strconv.Itoa(n) + " cards"
}

// columnIDText is the decimal text of a column ID, for ids of elements and
// values of options.
func columnIDText(id uint64) string {
	return strconv.FormatUint(id, 10)
}

// moveColumnTo is the position a button moves a column to, as text.
func moveColumnTo(position int) string {
	return strconv.Itoa(position)
}

// boardDropAction is the Datastar expression of the hidden form that the
// script of the board fills and submits when a card is dropped: it posts the
// form to the path the script put in its action attribute.
const boardDropAction = "@post(el.getAttribute('action'), {contentType: 'form'})"

// boardRefreshAction is the Datastar expression of the hidden link that the
// script clicks when a drag ends without a drop: it fetches the columns, which
// the board did not take while the card was held.
func boardRefreshAction(roomID uint64) string {
	return "@get('" + escapeQuote(BoardColumnsPath(roomID)) + "')"
}

// openBoardEditorAction is the Datastar expression that runs when the card form
// appears: it scrolls to the form and puts the cursor in the title.
const openBoardEditorAction = "el.scrollIntoView({block: 'center'}); el.querySelector('input[name=title]').focus()"

// closeBoardEditorAction is the Datastar expression of the Cancel link of the
// card form: the panel is emptied in the browser.
const closeBoardEditorAction = "document.getElementById('board-editor').replaceChildren()"

// RoomNavCurrent says which page of a room the navigation marks as current.
type RoomNavCurrent string

// The pages of a room that the navigation links.
const (
	// NavPosts is the page of the feed of the room.
	NavPosts RoomNavCurrent = "posts"
	// NavBoard is the page of the board of the room.
	NavBoard RoomNavCurrent = "board"
)
