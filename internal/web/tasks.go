package web

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
	"github.com/rm4n0s/errors"
	"github.com/starfederation/datastar-go/datastar"
)

// taskFieldPattern matches the name of a field of a task row of the editor:
// tasks[<index>][id], [title], [assignees] or [due].
var taskFieldPattern = regexp.MustCompile(`^tasks\[(\d{1,6})\]\[(id|title|assignees|due)\]$`)

// maxTaskRowIndex is the largest index the server gives a new task row.
const maxTaskRowIndex = 999999

// dueLayout is how the date field of a task row writes a due date.
const dueLayout = "2006-01-02"

// taskRowForm is a task row as the editor sent it, before it is read.
type taskRowForm struct {
	index     int
	id        string
	title     string
	assignees []string
	due       string
}

// blank reports whether the row holds nothing: no title, no assignee, no due
// date and no task ID. The editor sends such a row for a new row that was left
// empty, and the server ignores it. A row of a task that exists (it has an ID)
// is never blank, so that clearing its title is refused instead of deleting
// the task without a word.
func (t taskRowForm) blank() bool {
	return strings.TrimSpace(t.title) == "" && len(t.assignees) == 0 && strings.TrimSpace(t.due) == "" && !t.hasID()
}

// hasID reports whether the row names a task that exists.
func (t taskRowForm) hasID() bool {
	id := strings.TrimSpace(t.id)
	return id != "" && id != "0"
}

// taskRowsFrom reads the task rows of a form that was read into r.PostForm
// (urlencoded or multipart), in the order of their indexes, without the blank
// rows.
func taskRowsFrom(r *http.Request) []taskRowForm {
	byIndex := map[int]*taskRowForm{}
	for name, values := range r.PostForm {
		m := taskFieldPattern.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		index, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		row := byIndex[index]
		if row == nil {
			row = &taskRowForm{index: index}
			byIndex[index] = row
		}
		switch m[2] {
		case "id":
			row.id = values[0]
		case "title":
			row.title = values[0]
		case "assignees":
			row.assignees = values
		case "due":
			row.due = values[0]
		}
	}
	rows := make([]taskRowForm, 0, len(byIndex))
	for _, row := range byIndex {
		if !row.blank() {
			rows = append(rows, *row)
		}
	}
	slices.SortFunc(rows, func(a, b taskRowForm) int { return a.index - b.index })
	return rows
}

// taskInputsFrom reads the tasks of the editor into the input of the service.
// A row whose task ID or due date cannot be read makes a TagInvalidInput
// error with the message "Task N: ..." for the field "tasks", N counting the
// rows that are not blank, as the service does; the editor shows it above the
// rows.
func taskInputsFrom(r *http.Request) ([]app.TaskInput, error) {
	rows := taskRowsFrom(r)
	if len(rows) == 0 {
		return nil, nil
	}
	tasks := make([]app.TaskInput, len(rows))
	for i, row := range rows {
		label := "Task " + strconv.Itoa(i+1) + ": "
		in := app.TaskInput{Title: row.title}
		if row.hasID() {
			id, err := strconv.ParseUint(strings.TrimSpace(row.id), 10, 64)
			if err != nil || id == 0 {
				return nil, errors.New(app.TagInvalidInput, "a task row has a wrong ID",
					"fields", map[string]string{app.FieldTasks: label + "this task is not part of the post."}, "row", i+1)
			}
			in.ID = id
		}
		for _, v := range row.assignees {
			in.Assignees = append(in.Assignees, idOfText(v))
		}
		if due := strings.TrimSpace(row.due); due != "" {
			day, err := time.Parse(dueLayout, due)
			if err != nil {
				return nil, errors.New(app.TagInvalidInput, "a task row has a wrong due date",
					"fields", map[string]string{app.FieldTasks: label + "write the due date as YYYY-MM-DD."}, "row", i+1)
			}
			in.DueDate = &day
		}
		tasks[i] = in
	}
	return tasks, nil
}

// typedTasks makes the rows of the editor from what the person typed, to
// show them again after a submit that failed. The rows are numbered again from
// 0, so that the row "Task 2: ..." of the message is the second one.
func typedTasks(r *http.Request) []views.TaskRowData {
	rows := taskRowsFrom(r)
	if len(rows) == 0 {
		return nil
	}
	out := make([]views.TaskRowData, len(rows))
	for i, row := range rows {
		d := views.TaskRowData{ID: idOfText(row.id), Title: row.title, Due: strings.TrimSpace(row.due)}
		for _, v := range row.assignees {
			d.Assignees = append(d.Assignees, idOfText(v))
		}
		out[i] = d
	}
	return out
}

// editorFor puts the members of the room in the pickers of the editor. A room
// whose members cannot be read leaves the pickers empty (the person can still
// write the tasks, only not assign them): the editor is shown in answers that
// have another problem to report, and that problem comes first.
func (s *Server) editorFor(ctx context.Context, actor app.Actor, roomID uint64, d views.PostEditorData) views.PostEditorData {
	if roomID == 0 {
		return d
	}
	members, err := s.svc.Rooms.Members(ctx, actor, roomID)
	if err != nil {
		s.logger.Debug("the members of the room are not read for the editor", "room_id", roomID, "err", err)
		return d
	}
	return d.WithMembers(roomID, members)
}

// taskRowForm answers GET /editor/task-row?editor=<id>&index=<n>&room=<id>: an
// empty task row with that index, appended to the rows of that editor, with the
// members of the room in its assignee picker. It is what the Add task button
// asks for.
func (s *Server) taskRowForm(w http.ResponseWriter, r *http.Request) {
	actor, _ := actorFrom(r.Context())
	editor := editorIDOf(r)
	index := int(min(uintQuery(r, "index"), maxTaskRowIndex))
	members, err := s.svc.Rooms.Members(r.Context(), actor, uintQuery(r, "room"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.patchAll(w, r, patchOp{
		component: views.TaskRow(editor, index, views.TaskRowData{}, views.AssigneeOptions(members)),
		options:   []datastar.PatchElementOption{datastar.WithSelector(views.TaskRowsSelector(editor)), datastar.WithModeAppend()},
	})
}
