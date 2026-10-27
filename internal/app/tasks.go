package app

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
)

// The rules of SPEC §7.6 for the tasks that a person types in the editor.
const (
	taskTitleMinLength = 1
	taskTitleMaxLength = 200
	// taskMaxAssignees is the most people assigned to one task.
	taskMaxAssignees = 10
	// postMaxTasks is the most tasks of one post or comment.
	postMaxTasks = 20
	// dateLayout is how a due date is written in the details of an audit event.
	dateLayout = "2006-01-02"
)

// The names of the fields of a task in the metadata "fields" of a
// TagInvalidInput error. The tasks of a post are one field, FieldTasks, whose
// message names the row of the first wrong task ("Task 2: ..."); the fields
// of TaskPatch are the other three.
const (
	// FieldTasks names the tasks of a post.
	FieldTasks = "tasks"
	// FieldTitle names the title of a task.
	FieldTitle = "title"
	// FieldAssignees names the assignees of a task.
	FieldAssignees = "assignees"
	// FieldDueDate names the due date of a task.
	FieldDueDate = "dueDate"
)

// TaskInput is a task as a person types it in the editor of a post or comment
// (D17): one row of the tasks field.
type TaskInput struct {
	// ID is the ID of the task when Edit is given a task of the post that
	// stays; 0 for a new task. Create and Reply accept only 0.
	ID uint64
	// Title is the title: 1 to 200 characters, trimmed.
	Title string
	// Assignees are the IDs of the members of the room that the task is
	// assigned to: at most 10, duplicates are dropped.
	Assignees []uint64
	// DueDate is the day the task is due, or nil. Only the date counts: the
	// service converts it to UTC and drops the time of day. The zero time is
	// the same as nil.
	DueDate *time.Time
}

// cleanTask is a TaskInput that passed the rules.
type cleanTask struct {
	id        uint64
	title     string
	assignees []uint64
	dueDate   *time.Time
}

// dateOf returns the day of t as midnight UTC, or nil for a nil or zero t.
func dateOf(t *time.Time) *time.Time {
	if t == nil || t.IsZero() {
		return nil
	}
	d := dayOf(*t)
	return &d
}

// dayOf returns the day of t as midnight UTC.
func dayOf(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// dateText is a due date as the audit details write it: "2026-10-30", or nil.
func dateText(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(dateLayout)
}

// sameDate reports whether a and b are the same due date (both nil, or the
// same day).
func sameDate(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// dedupe returns ids without the repeated ones, in order.
func dedupe(ids []uint64) []uint64 {
	out := make([]uint64, 0, len(ids))
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// sameIDs reports whether a and b hold the same IDs, in any order.
func sameIDs(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

// cleanTitle trims a task title and reports whether it is 1 to 200
// characters of valid text.
func cleanTitle(title string) (string, bool) {
	title = strings.TrimSpace(title)
	n := utf8.RuneCountInString(title)
	return title, utf8.ValidString(title) && n >= taskTitleMinLength && n <= taskTitleMaxLength
}

// checkTasks applies the rules of SPEC §7.6 to the tasks of a post of the room
// and returns them as they are stored, and the usernames of their assignees
// by user ID. own holds the tasks the post has now by ID (empty for a new
// post): a task with an ID must be one of them, and only once. Assignees must
// be members of the room, except those already assigned to that task, who may
// stay although they have left the room. The first
// problem is put in fields under FieldTasks, with the number of the row.
// Only a failing query returns an error.
func checkTasks(ctx context.Context, tx *store.Store, roomID uint64, in []TaskInput, own map[uint64]store.TaskRow, fields map[string]string) ([]cleanTask, map[uint64]string, error) {
	if len(in) == 0 {
		return nil, nil, nil
	}
	if len(in) > postMaxTasks {
		fields[FieldTasks] = "Write at most " + strconv.Itoa(postMaxTasks) + " tasks."
		return nil, nil, nil
	}
	// One query for the membership of everybody: the lists that are too long
	// are refused below and need no lookup.
	assignees := make([][]uint64, len(in))
	var all []uint64
	for i, t := range in {
		assignees[i] = dedupe(t.Assignees)
		if len(assignees[i]) <= taskMaxAssignees {
			all = append(all, assignees[i]...)
		}
	}
	names, err := tx.MemberUsernames(ctx, roomID, dedupe(all))
	if err != nil {
		return nil, nil, err
	}
	out := make([]cleanTask, len(in))
	seen := map[uint64]bool{}
	former := map[uint64]string{}
	for i, t := range in {
		row := "Task " + strconv.Itoa(i+1) + ": "
		current, owned := own[t.ID]
		if t.ID != 0 && (!owned || seen[t.ID]) {
			fields[FieldTasks] = row + "this task is not part of the post."
			return nil, nil, nil
		}
		seen[t.ID] = true
		title, ok := cleanTitle(t.Title)
		if !ok {
			fields[FieldTasks] = row + "write a title of 1 to " + strconv.Itoa(taskTitleMaxLength) + " characters."
			return nil, nil, nil
		}
		if len(assignees[i]) > taskMaxAssignees {
			fields[FieldTasks] = row + "assign at most " + strconv.Itoa(taskMaxAssignees) + " people."
			return nil, nil, nil
		}
		for _, id := range assignees[i] {
			if _, member := names[id]; member {
				continue
			}
			a := slices.IndexFunc(current.Assignees, func(x store.TaskAssigneeRow) bool { return x.UserID == id })
			if t.ID == 0 || a < 0 {
				fields[FieldTasks] = row + "assign only members of the room."
				return nil, nil, nil
			}
			// A former member who stays on the task: its username comes from
			// the task, for the audit event. It goes in names after the loop,
			// so that it is not taken for a member of another task.
			former[id] = current.Assignees[a].Username
		}
		out[i] = cleanTask{id: t.ID, title: title, assignees: assignees[i], dueDate: dateOf(t.DueDate)}
	}
	maps.Copy(names, former)
	return out, names, nil
}

// taskEdit is a task of the post that changes in an edit.
type taskEdit struct {
	row  store.TaskRow
	want cleanTask
	// details holds the changed fields of the audit event task.edited.
	details map[string]any
	// assignees is true when the assignees changed.
	assignees bool
}

// taskPlan is what an edit of the tasks of a post does.
type taskPlan struct {
	// remove are the tasks that go.
	remove []store.TaskRow
	// edits are the tasks that stay and change.
	edits []taskEdit
	// add are the new tasks, in the order to append them.
	add []cleanTask
}

// empty reports whether the plan changes nothing.
func (p taskPlan) empty() bool {
	return len(p.remove) == 0 && len(p.edits) == 0 && len(p.add) == 0
}

// usernamesOf returns the usernames of the assignees of a row, never nil.
func usernamesOf(row store.TaskRow) []string {
	out := make([]string, len(row.Assignees))
	for i, a := range row.Assignees {
		out[i] = a.Username
	}
	return out
}

// planTasks compares the tasks the post has (current) with the tasks of the
// edit (want, from checkTasks) and says what to remove, change and add. names
// holds the usernames of the wanted assignees.
func planTasks(current []store.TaskRow, want []cleanTask, names map[uint64]string) taskPlan {
	var plan taskPlan
	byID := make(map[uint64]store.TaskRow, len(current))
	for _, r := range current {
		byID[r.ID] = r
	}
	kept := map[uint64]bool{}
	for _, w := range want {
		if w.id == 0 {
			plan.add = append(plan.add, w)
			continue
		}
		kept[w.id] = true
		row := byID[w.id]
		details := map[string]any{}
		if row.Title != w.title {
			details["title"] = changeOf(row.Title, w.title)
		}
		oldIDs := make([]uint64, len(row.Assignees))
		for i, a := range row.Assignees {
			oldIDs[i] = a.UserID
		}
		assignees := !sameIDs(oldIDs, w.assignees)
		if assignees {
			newNames := make([]string, len(w.assignees))
			for i, id := range w.assignees {
				newNames[i] = names[id]
			}
			details["assignees"] = changeOf(usernamesOf(row), newNames)
		}
		if !sameDate(row.DueDate, w.dueDate) {
			details["dueDate"] = changeOf(dateText(row.DueDate), dateText(w.dueDate))
		}
		if len(details) > 0 {
			plan.edits = append(plan.edits, taskEdit{row: row, want: w, details: details, assignees: assignees})
		}
	}
	for _, r := range current {
		if !kept[r.ID] {
			plan.remove = append(plan.remove, r)
		}
	}
	return plan
}

// planDetails is the "tasks" entry of the details of post.edited: the titles
// of the tasks added, edited and removed.
func (p taskPlan) planDetails() map[string]any {
	added := make([]string, len(p.add))
	for i, w := range p.add {
		added[i] = w.title
	}
	edited := make([]string, len(p.edits))
	for i, e := range p.edits {
		edited[i] = e.want.title
	}
	removed := make([]string, len(p.remove))
	for i, r := range p.remove {
		removed[i] = r.Title
	}
	return map[string]any{"added": added, "edited": edited, "removed": removed}
}

// applyTasks does a plan on the tasks of the post, as who (the display name
// of the actor), and returns the audit events of the changes, in the order of
// the changes, for the caller to record after the event of the post: removed
// tasks go first and the gaps they leave in the columns are closed, then the
// changed ones are updated, then the new ones are appended to the end of the
// first column of the board, in the order given.
func applyTasks(ctx context.Context, tx *store.Store, actor Actor, who string, room store.Room, post store.Post, plan taskPlan, names map[uint64]string) ([]auditEntry, error) {
	audits, err := changeTasks(ctx, tx, actor, who, room, post, plan, names)
	if err != nil {
		return nil, err
	}
	if plan.empty() {
		return audits, nil
	}
	// The titles of the tasks are part of the search index of the post.
	if err := tx.ReindexPost(ctx, post.ID); err != nil {
		return nil, err
	}
	return audits, nil
}

// changeTasks is the part of applyTasks that writes the tasks and their audit
// events.
func changeTasks(ctx context.Context, tx *store.Store, actor Actor, who string, room store.Room, post store.Post, plan taskPlan, names map[uint64]string) ([]auditEntry, error) {
	var audits []auditEntry
	entry := func(action AuditAction, taskID uint64, summary string, details map[string]any) auditEntry {
		return auditEntry{
			ActorID: actor.UserID, Action: action, RoomID: room.ID,
			EntityType: EntityTask, EntityID: taskID, Summary: summary, Details: details,
		}
	}
	if len(plan.remove) > 0 {
		ids := make([]uint64, len(plan.remove))
		var columns []uint64
		for i, r := range plan.remove {
			ids[i] = r.ID
			if !slices.Contains(columns, r.ColumnID) {
				columns = append(columns, r.ColumnID)
			}
			audits = append(audits, entry(ActionTaskDeleted, r.ID,
				who+" deleted the task \""+r.Title+"\" in #"+room.Name, taskDeletedDetails(r)))
		}
		if err := tx.DeleteTasks(ctx, ids); err != nil {
			return nil, err
		}
		for _, c := range columns {
			if err := compactColumn(ctx, tx, c); err != nil {
				return nil, err
			}
		}
	}
	for _, e := range plan.edits {
		if err := tx.UpdateTaskFields(ctx, e.row.ID, e.want.title, e.want.dueDate); err != nil {
			return nil, err
		}
		if e.assignees {
			if err := tx.ReplaceTaskAssignees(ctx, e.row.ID, e.want.assignees); err != nil {
				return nil, err
			}
			// TODO(step 11.1): notify new assignees.
		}
		audits = append(audits, entry(ActionTaskEdited, e.row.ID,
			who+" edited the task \""+e.want.title+"\" in #"+room.Name, e.details))
	}
	if len(plan.add) == 0 {
		return audits, nil
	}
	columns, err := tx.ListBoardColumns(ctx, room.ID)
	if err != nil {
		return nil, err
	}
	first := columns[0]
	existing, err := tx.ListTasksOfColumn(ctx, first.ID)
	if err != nil {
		return nil, err
	}
	for i, w := range plan.add {
		t := store.Task{
			RoomID: room.ID, PostID: post.ID, ColumnID: first.ID, Position: len(existing) + i,
			Title: w.title, DueDate: w.dueDate,
		}
		if err := tx.CreateTask(ctx, &t); err != nil {
			return nil, err
		}
		if err := tx.ReplaceTaskAssignees(ctx, t.ID, w.assignees); err != nil {
			return nil, err
		}
		// TODO(step 11.1): notify new assignees.
		audits = append(audits, entry(ActionTaskCreated, t.ID,
			who+" added the task \""+w.title+"\" in #"+room.Name, taskCreatedDetails(post.ID, first.Name, w, names)))
	}
	return audits, nil
}

// taskCreatedDetails is the Details of task.created.
func taskCreatedDetails(postID uint64, column string, w cleanTask, names map[uint64]string) map[string]any {
	assignees := make([]string, len(w.assignees))
	for i, id := range w.assignees {
		assignees[i] = names[id]
	}
	return map[string]any{
		"postID":    postID,
		"title":     changeOf(nil, w.title),
		"column":    changeOf(nil, column),
		"assignees": changeOf(nil, assignees),
		"dueDate":   changeOf(nil, dateText(w.dueDate)),
	}
}

// taskDeletedDetails is the Details of task.deleted.
func taskDeletedDetails(r store.TaskRow) map[string]any {
	return map[string]any{
		"postID":    r.PostID,
		"title":     changeOf(r.Title, nil),
		"column":    changeOf(r.ColumnName, nil),
		"assignees": changeOf(usernamesOf(r), nil),
		"dueDate":   changeOf(dateText(r.DueDate), nil),
	}
}

// compactColumn makes the positions of the tasks of the column 0..n-1 again,
// in their order, after tasks left it.
func compactColumn(ctx context.Context, tx *store.Store, columnID uint64) error {
	tasks, err := tx.ListTasksOfColumn(ctx, columnID)
	if err != nil {
		return err
	}
	for i, t := range tasks {
		if t.Position != i {
			if err := tx.SetTaskPlacement(ctx, t.ID, columnID, i); err != nil {
				return err
			}
		}
	}
	return nil
}

// recordAudits writes the audit events in order.
func recordAudits(ctx context.Context, tx *store.Store, entries []auditEntry) error {
	for _, e := range entries {
		if err := recordAudit(ctx, tx, e); err != nil {
			return err
		}
	}
	return nil
}

// boardEvent is the live event of a change to the board of a room. postID is
// the post or comment of the task that changed, or 0 when none is concerned.
func boardEvent(roomID, postID uint64) live.Event {
	return live.Event{Topic: live.BoardTopic(roomID), Kind: live.KindBoardChanged, RoomID: roomID, PostID: postID}
}

// tasksByID returns the rows by task ID.
func tasksByID(rows []store.TaskRow) map[uint64]store.TaskRow {
	byID := make(map[uint64]store.TaskRow, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	return byID
}

// addNewTasks appends the tasks of a new post or comment to the first column
// of the board, writes their audit events after the event of the post, and
// adds the live event of the board to events. It does nothing for no tasks.
func addNewTasks(ctx context.Context, tx *store.Store, actor Actor, who string, room store.Room, post store.Post, tasks []cleanTask, names map[uint64]string, events *[]live.Event) error {
	if len(tasks) == 0 {
		return nil
	}
	audits, err := applyTasks(ctx, tx, actor, who, room, post, taskPlan{add: tasks}, names)
	if err != nil {
		return err
	}
	*events = append(*events, boardEvent(room.ID, post.ID))
	return recordAudits(ctx, tx, audits)
}
