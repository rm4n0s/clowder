package app

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// The rules of SPEC §7.6 and D25 for the names of the board columns.
const (
	columnNameMinLength = 1
	columnNameMaxLength = 40
)

// Messages for people about a board that cannot change as asked. They are the
// messages of the TagConflict errors.
const (
	messageColumnHasCards = "Move its cards first."
	messageLastColumn     = "A board needs at least one column."
)

// TaskCard is a task as the board and the posts show it.
type TaskCard struct {
	// ID is the ID of the task.
	ID uint64
	// Title is the title of the task.
	Title string
	// PostID is the ID of the post or comment the task was written in.
	PostID uint64
	// RootID is the ID of the top-level post of the thread of PostID, which a
	// link back to the task needs; it is PostID itself for a top-level post.
	RootID uint64
	// ColumnID is the ID of the column the task is in.
	ColumnID uint64
	// ColumnName is the name of that column.
	ColumnName string
	// Done is true when the task is in the last column of the board, which
	// counts as done (SPEC §7.6).
	Done bool
	// Assignees are the people the task is assigned to, in the order they were
	// assigned.
	Assignees []UserRef
	// DueDate is the day the task is due, as midnight UTC; nil when it has no
	// due date.
	DueDate *time.Time
	// Overdue is true when the due date is before today (UTC) and the task is
	// not in the last column of the board, which counts as done (SPEC §7.6).
	Overdue bool
	// Position is the place of the task in its column, from 0 at the top.
	Position int
}

// ColumnView is a column of a board with its cards.
type ColumnView struct {
	// ID is the ID of the column.
	ID uint64
	// Name is the name of the column, unique in the room without regard to
	// case.
	Name string
	// Position is the place of the column from the left, from 0.
	Position int
	// Last is true for the last column, which counts as done.
	Last bool
	// Tasks are the cards of the column from top to bottom.
	Tasks []TaskCard
}

// BoardView is the board of a room as one actor sees it.
type BoardView struct {
	// Room is the room of the board.
	Room Room
	// Columns are the columns from left to right.
	Columns []ColumnView
	// CanEdit is true when the actor is a member of the room, who may move
	// cards, change them and change the columns.
	CanEdit bool
}

// TaskPatch is a change to a task made on the board. A nil field stays as it
// is.
type TaskPatch struct {
	// Title is the new title: 1 to 200 characters, trimmed.
	Title *string
	// Assignees is the new list of assignees, members of the room, at most 10,
	// duplicates dropped; an empty list unassigns everybody.
	Assignees *[]uint64
	// DueDate is the new due date; only the date counts, as in TaskInput.
	DueDate *time.Time
	// ClearDueDate removes the due date. Together with DueDate it is invalid.
	ClearDueDate bool
}

// Board is the service of the Kanban boards of the rooms (SPEC §7.6). Make it
// with NewBoard. It is safe for concurrent use.
type Board struct {
	st  *store.Store
	clk clock.Clock
	pub Publisher
}

// NewBoard makes the service. The clock decides what is overdue; pub receives
// the live events of the changes, after they are committed; nil publishes
// nothing.
func NewBoard(st *store.Store, clk clock.Clock, pub Publisher) *Board {
	return &Board{st: st, clk: clk, pub: orNop(pub)}
}

// Board returns the board of a room that the actor may read: the columns in
// order with their cards in order. For a room that the actor may not read it
// fails with TagNotFound.
func (b *Board) Board(ctx context.Context, actor Actor, roomID uint64) (BoardView, error) {
	room, err := loadReadableRoom(ctx, b.st, actor, roomID)
	if err != nil {
		return BoardView{}, err
	}
	rooms, err := roomViews(ctx, b.st, actor, []store.Room{room})
	if err != nil {
		return BoardView{}, err
	}
	columns, err := b.st.ListBoardColumns(ctx, room.ID)
	if err != nil {
		return BoardView{}, err
	}
	rows, err := b.st.TaskRowsOfRoom(ctx, room.ID)
	if err != nil {
		return BoardView{}, err
	}
	byColumn := map[uint64][]store.TaskRow{}
	for _, r := range rows {
		byColumn[r.ColumnID] = append(byColumn[r.ColumnID], r)
	}
	today := dayOf(b.clk.Now())
	view := BoardView{Room: rooms[0], Columns: make([]ColumnView, len(columns)), CanEdit: rooms[0].IsMember}
	for i, c := range columns {
		view.Columns[i] = ColumnView{
			ID: c.ID, Name: c.Name, Position: c.Position, Last: i == len(columns)-1,
			Tasks: taskCards(byColumn[c.ID], today),
		}
	}
	return view, nil
}

// MoveTask puts the task in the column at the position (from 0, the top). The
// position counts in the target column without the task, and is clamped to
// 0..the number of cards there. The positions of the cards stay 0..n-1 in
// both columns. It writes task.moved with the column and the position from
// and to, and publishes board.changed and post.updated (comment.updated for a
// comment). A move to where the card is already writes and publishes nothing.
// Only members may: a task of a room that the actor may not read, a missing
// task and a column that is missing or of another room fail with TagNotFound,
// a visible room that the actor has not joined with TagForbidden.
func (b *Board) MoveTask(ctx context.Context, actor Actor, taskID, columnID uint64, position int) error {
	var events []live.Event
	err := b.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		task, room, member, err := loadTaskRoom(ctx, tx, actor, taskID)
		if err != nil {
			return err
		}
		if !member {
			return errors.New(TagForbidden, "only members move cards",
				"userID", actor.UserID, "roomID", room.ID)
		}
		target, err := tx.BoardColumnByID(ctx, columnID)
		if err != nil {
			return err
		}
		if target.RoomID != room.ID {
			return errors.New(TagNotFound, "no such column", "reason", "otherRoom", "columnID", columnID)
		}
		cards, err := tx.ListTasksOfColumn(ctx, task.ColumnID)
		if err != nil {
			return err
		}
		from := slices.IndexFunc(cards, func(t store.Task) bool { return t.ID == task.ID })
		rest := slices.DeleteFunc(slices.Clone(cards), func(t store.Task) bool { return t.ID == task.ID })
		same := columnID == task.ColumnID
		dest := rest
		source := target
		if !same {
			if dest, err = tx.ListTasksOfColumn(ctx, columnID); err != nil {
				return err
			}
			if source, err = tx.BoardColumnByID(ctx, task.ColumnID); err != nil {
				return err
			}
		}
		pos := min(max(position, 0), len(dest))
		if same && pos == from {
			return nil
		}
		ordered := slices.Insert(slices.Clone(dest), pos, task)
		for i, t := range ordered {
			if t.ID == task.ID || t.Position != i {
				if err := tx.SetTaskPlacement(ctx, t.ID, columnID, i); err != nil {
					return err
				}
			}
		}
		if !same {
			for i, t := range rest {
				if t.Position != i {
					if err := tx.SetTaskPlacement(ctx, t.ID, task.ColumnID, i); err != nil {
						return err
					}
				}
			}
		}
		who, err := displayNameOf(ctx, tx, actor.UserID)
		if err != nil {
			return err
		}
		summary := who + " moved \"" + task.Title + "\" within " + target.Name + " in #" + room.Name
		if !same {
			summary = who + " moved \"" + task.Title + "\" to " + target.Name + " in #" + room.Name
		}
		if events, err = taskChangeEvents(ctx, tx, task); err != nil {
			return err
		}
		return recordAudit(ctx, tx, auditEntry{
			ActorID:    actor.UserID,
			Action:     ActionTaskMoved,
			RoomID:     room.ID,
			EntityType: EntityTask,
			EntityID:   task.ID,
			Summary:    summary,
			Details: map[string]any{
				"column":   changeOf(source.Name, target.Name),
				"position": changeOf(from, pos),
			},
		})
	})
	if err != nil {
		return err
	}
	publishAll(b.pub, events)
	return nil
}

// UpdateTask changes the title, the assignees or the due date of a task on
// the board, and writes task.edited with only what changed (the title, the
// usernames of the assignees, the due date, old and new). It publishes
// board.changed and post.updated (comment.updated for a comment). A patch that
// changes nothing writes and publishes nothing. Only members may: a task of a
// room that the actor may not read and a missing task fail with TagNotFound,
// a visible room that the actor has not joined with TagForbidden. It fails
// with TagInvalidInput, with the problems in the metadata "fields", for a
// title, assignees or due date that break a rule. The users newly assigned are
// notified (assigned), not the actor and not those who stay on the task.
func (b *Board) UpdateTask(ctx context.Context, actor Actor, taskID uint64, patch TaskPatch) error {
	var events []live.Event
	err := b.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		task, room, member, err := loadTaskRoom(ctx, tx, actor, taskID)
		if err != nil {
			return err
		}
		if !member {
			return errors.New(TagForbidden, "only members change cards",
				"userID", actor.UserID, "roomID", room.ID)
		}
		rows, err := tx.TaskRowsOfPosts(ctx, []uint64{task.PostID})
		if err != nil {
			return err
		}
		i := slices.IndexFunc(rows[task.PostID], func(r store.TaskRow) bool { return r.ID == task.ID })
		if i < 0 {
			return errors.New(TagNotFound, "no such task", "reason", "missing", "taskID", taskID)
		}
		current := rows[task.PostID][i]
		want := cleanTask{id: task.ID, title: current.Title, dueDate: current.DueDate}
		for _, a := range current.Assignees {
			want.assignees = append(want.assignees, a.UserID)
		}
		fields := map[string]string{}
		names := map[uint64]string{}
		if patch.Title != nil {
			title, ok := cleanTitle(*patch.Title)
			if !ok {
				fields[FieldTitle] = "Write a title of 1 to 200 characters."
			}
			want.title = title
		}
		if patch.Assignees != nil {
			ids := dedupe(*patch.Assignees)
			switch {
			case len(ids) > taskMaxAssignees:
				fields[FieldAssignees] = "Assign at most 10 people."
			default:
				if names, err = tx.MemberUsernames(ctx, room.ID, ids); err != nil {
					return err
				}
				for _, id := range ids {
					if _, ok := names[id]; ok {
						continue
					}
					// Somebody already on the task may stay after leaving the room.
					a := slices.IndexFunc(current.Assignees, func(x store.TaskAssigneeRow) bool { return x.UserID == id })
					if a < 0 {
						fields[FieldAssignees] = "Assign only members of the room."
						break
					}
					names[id] = current.Assignees[a].Username
				}
			}
			want.assignees = ids
		}
		switch {
		case patch.ClearDueDate && patch.DueDate != nil:
			fields[FieldDueDate] = "Choose a due date or clear it, not both."
		case patch.ClearDueDate:
			want.dueDate = nil
		case patch.DueDate != nil:
			want.dueDate = dateOf(patch.DueDate)
		}
		if len(fields) > 0 {
			return errors.New(TagInvalidInput, "the task has mistakes", "fields", fields)
		}
		plan := planTasks([]store.TaskRow{current}, []cleanTask{want}, names)
		if plan.empty() {
			return nil
		}
		who, err := displayNameOf(ctx, tx, actor.UserID)
		if err != nil {
			return err
		}
		var assigned []assignment
		audits, err := applyTasks(ctx, tx, actor, who, room, store.Post{ID: task.PostID}, plan, names, &assigned)
		if err != nil {
			return err
		}
		if events, err = taskChangeEvents(ctx, tx, task); err != nil {
			return err
		}
		if err := recordAudits(ctx, tx, audits); err != nil {
			return err
		}
		notified, err := notify(ctx, tx, actor, room, task.PostID, assignedNotices(assigned))
		events = append(events, notified...)
		return err
	})
	if err != nil {
		return err
	}
	publishAll(b.pub, events)
	return nil
}

// AddColumn appends a column to the right of the board of the room and writes
// column.created. The name is trimmed, 1 to 40 characters, and unique in the
// room without regard to case; otherwise it fails with TagInvalidInput and the
// message under FieldName. Any member may (D25): a room that the actor may not
// read fails with TagNotFound, a visible room that the actor has not joined
// with TagForbidden. It publishes board.changed.
func (b *Board) AddColumn(ctx context.Context, actor Actor, roomID uint64, name string) (ColumnView, error) {
	var created store.BoardColumn
	var events []live.Event
	err := b.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		room, err := loadReadableRoom(ctx, tx, actor, roomID)
		if err != nil {
			return err
		}
		member, err := isMember(ctx, tx, actor.UserID, room.ID)
		if err != nil {
			return err
		}
		if !member {
			return errors.New(TagForbidden, "only members change the columns",
				"userID", actor.UserID, "roomID", room.ID)
		}
		columns, err := tx.ListBoardColumns(ctx, room.ID)
		if err != nil {
			return err
		}
		clean, problem := checkColumnName(name, columns, 0)
		if problem != "" {
			return errors.New(TagInvalidInput, "the column has mistakes",
				"fields", map[string]string{FieldName: problem})
		}
		who, err := displayNameOf(ctx, tx, actor.UserID)
		if err != nil {
			return err
		}
		made := []store.BoardColumn{{RoomID: room.ID, Name: clean, Position: len(columns)}}
		if err := tx.CreateBoardColumns(ctx, made); err != nil {
			return err
		}
		created = made[0]
		events = []live.Event{boardEvent(room.ID, 0)}
		return recordAudit(ctx, tx, auditEntry{
			ActorID:    actor.UserID,
			Action:     ActionColumnCreated,
			RoomID:     room.ID,
			EntityType: EntityColumn,
			EntityID:   created.ID,
			Summary:    who + " added the column \"" + clean + "\" to #" + room.Name,
			Details: map[string]any{
				"name":     changeOf(nil, clean),
				"position": changeOf(nil, created.Position),
			},
		})
	})
	if err != nil {
		return ColumnView{}, err
	}
	publishAll(b.pub, events)
	return ColumnView{ID: created.ID, Name: created.Name, Position: created.Position, Last: true, Tasks: []TaskCard{}}, nil
}

// RenameColumn changes the name of a column and writes column.renamed with
// the old and the new name. The name rules are those of AddColumn; renaming a
// column to its own name, or to another case of it, is allowed, and to the
// exact name it has already does nothing. Any member may (D25): a column of a
// room that the actor may not read and a missing column fail with
// TagNotFound, a visible room that the actor has not joined with
// TagForbidden. It publishes board.changed.
func (b *Board) RenameColumn(ctx context.Context, actor Actor, columnID uint64, name string) error {
	var events []live.Event
	err := b.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		col, room, member, err := loadColumnRoom(ctx, tx, actor, columnID)
		if err != nil {
			return err
		}
		if !member {
			return errors.New(TagForbidden, "only members change the columns",
				"userID", actor.UserID, "roomID", room.ID)
		}
		columns, err := tx.ListBoardColumns(ctx, room.ID)
		if err != nil {
			return err
		}
		clean, problem := checkColumnName(name, columns, col.ID)
		if problem != "" {
			return errors.New(TagInvalidInput, "the column has mistakes",
				"fields", map[string]string{FieldName: problem})
		}
		if clean == col.Name {
			return nil
		}
		who, err := displayNameOf(ctx, tx, actor.UserID)
		if err != nil {
			return err
		}
		if err := tx.UpdateBoardColumnName(ctx, col.ID, clean); err != nil {
			return err
		}
		events = []live.Event{boardEvent(room.ID, 0)}
		return recordAudit(ctx, tx, auditEntry{
			ActorID:    actor.UserID,
			Action:     ActionColumnRenamed,
			RoomID:     room.ID,
			EntityType: EntityColumn,
			EntityID:   col.ID,
			Summary:    who + " renamed the column \"" + col.Name + "\" to \"" + clean + "\" in #" + room.Name,
			Details:    map[string]any{"name": changeOf(col.Name, clean)},
		})
	})
	if err != nil {
		return err
	}
	publishAll(b.pub, events)
	return nil
}

// MoveColumn puts a column at the position (from 0, the left), clamped to
// 0..the number of columns minus one, and writes column.moved with the
// position from and to. The positions of the columns stay 0..n-1. A move to
// where the column is already writes and publishes nothing. The permissions
// and errors are those of RenameColumn. It publishes board.changed.
func (b *Board) MoveColumn(ctx context.Context, actor Actor, columnID uint64, position int) error {
	var events []live.Event
	err := b.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		col, room, member, err := loadColumnRoom(ctx, tx, actor, columnID)
		if err != nil {
			return err
		}
		if !member {
			return errors.New(TagForbidden, "only members change the columns",
				"userID", actor.UserID, "roomID", room.ID)
		}
		columns, err := tx.ListBoardColumns(ctx, room.ID)
		if err != nil {
			return err
		}
		from := slices.IndexFunc(columns, func(c store.BoardColumn) bool { return c.ID == col.ID })
		rest := slices.DeleteFunc(slices.Clone(columns), func(c store.BoardColumn) bool { return c.ID == col.ID })
		pos := min(max(position, 0), len(rest))
		if pos == from {
			return nil
		}
		ordered := slices.Insert(rest, pos, col)
		for i, c := range ordered {
			if c.Position != i {
				if err := tx.SetBoardColumnPosition(ctx, c.ID, i); err != nil {
					return err
				}
			}
		}
		who, err := displayNameOf(ctx, tx, actor.UserID)
		if err != nil {
			return err
		}
		place := "to the start of the board"
		if pos > 0 {
			place = "after \"" + ordered[pos-1].Name + "\""
		}
		events = []live.Event{boardEvent(room.ID, 0)}
		return recordAudit(ctx, tx, auditEntry{
			ActorID:    actor.UserID,
			Action:     ActionColumnMoved,
			RoomID:     room.ID,
			EntityType: EntityColumn,
			EntityID:   col.ID,
			Summary:    who + " moved the column \"" + col.Name + "\" " + place + " in #" + room.Name,
			Details: map[string]any{
				"name":     col.Name,
				"position": changeOf(from, pos),
			},
		})
	})
	if err != nil {
		return err
	}
	publishAll(b.pub, events)
	return nil
}

// DeleteColumn deletes a column and writes column.deleted. A column that still
// has cards (message "Move its cards first.") and the last column of a board
// (message "A board needs at least one column.") are refused with TagConflict;
// the last column is checked first. The positions of the columns stay 0..n-1.
// The permissions and errors are those of RenameColumn. It publishes
// board.changed.
func (b *Board) DeleteColumn(ctx context.Context, actor Actor, columnID uint64) error {
	var events []live.Event
	err := b.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		col, room, member, err := loadColumnRoom(ctx, tx, actor, columnID)
		if err != nil {
			return err
		}
		if !member {
			return errors.New(TagForbidden, "only members change the columns",
				"userID", actor.UserID, "roomID", room.ID)
		}
		columns, err := tx.ListBoardColumns(ctx, room.ID)
		if err != nil {
			return err
		}
		if len(columns) <= 1 {
			return errors.New(TagConflict, messageLastColumn,
				"reason", "last", "columnID", col.ID, "roomID", room.ID)
		}
		cards, err := tx.ListTasksOfColumn(ctx, col.ID)
		if err != nil {
			return err
		}
		if len(cards) > 0 {
			return errors.New(TagConflict, messageColumnHasCards,
				"reason", "cards", "columnID", col.ID, "cards", len(cards))
		}
		who, err := displayNameOf(ctx, tx, actor.UserID)
		if err != nil {
			return err
		}
		if err := tx.DeleteBoardColumn(ctx, col.ID); err != nil {
			return err
		}
		i := 0
		for _, c := range columns {
			if c.ID == col.ID {
				continue
			}
			if c.Position != i {
				if err := tx.SetBoardColumnPosition(ctx, c.ID, i); err != nil {
					return err
				}
			}
			i++
		}
		events = []live.Event{boardEvent(room.ID, 0)}
		return recordAudit(ctx, tx, auditEntry{
			ActorID:    actor.UserID,
			Action:     ActionColumnDeleted,
			RoomID:     room.ID,
			EntityType: EntityColumn,
			EntityID:   col.ID,
			Summary:    who + " deleted the column \"" + col.Name + "\" from #" + room.Name,
			Details: map[string]any{
				"name":     changeOf(col.Name, nil),
				"position": changeOf(col.Position, nil),
			},
		})
	})
	if err != nil {
		return err
	}
	publishAll(b.pub, events)
	return nil
}

// taskChangeEvents are the live events of a change to a task made on the
// board: the board, and the post or comment of the task, so that its list of
// tasks refreshes.
func taskChangeEvents(ctx context.Context, tx *store.Store, task store.Task) ([]live.Event, error) {
	post, err := tx.PostByID(ctx, task.PostID)
	if err != nil {
		return nil, err
	}
	events := []live.Event{boardEvent(task.RoomID, task.PostID)}
	return append(events, postEvents(refOf(post), live.KindPostUpdated, live.KindCommentUpdated)...), nil
}

// loadTaskRoom returns the task, the room of it if the actor may read it, and
// whether the actor is a member. A task that does not exist and a task in a
// room the actor may not read fail with TagNotFound.
func loadTaskRoom(ctx context.Context, tx *store.Store, actor Actor, taskID uint64) (store.Task, store.Room, bool, error) {
	task, err := tx.TaskByID(ctx, taskID)
	if err != nil {
		return store.Task{}, store.Room{}, false, err
	}
	room, err := loadReadableRoom(ctx, tx, actor, task.RoomID)
	if err != nil {
		return store.Task{}, store.Room{}, false, err
	}
	member, err := isMember(ctx, tx, actor.UserID, room.ID)
	if err != nil {
		return store.Task{}, store.Room{}, false, err
	}
	return task, room, member, nil
}

// loadColumnRoom returns the column, the room of it if the actor may read it,
// and whether the actor is a member. A column that does not exist and a column
// in a room the actor may not read fail with TagNotFound.
func loadColumnRoom(ctx context.Context, tx *store.Store, actor Actor, columnID uint64) (store.BoardColumn, store.Room, bool, error) {
	col, err := tx.BoardColumnByID(ctx, columnID)
	if err != nil {
		return store.BoardColumn{}, store.Room{}, false, err
	}
	room, err := loadReadableRoom(ctx, tx, actor, col.RoomID)
	if err != nil {
		return store.BoardColumn{}, store.Room{}, false, err
	}
	member, err := isMember(ctx, tx, actor.UserID, room.ID)
	if err != nil {
		return store.BoardColumn{}, store.Room{}, false, err
	}
	return col, room, member, nil
}

// checkColumnName trims a column name and checks it against the rules: 1 to 40
// characters of valid text, and not the name of another column of the room
// (except is the ID of the column being renamed, 0 for a new one), without
// regard to case. The second result is the message for the problem, or empty.
func checkColumnName(raw string, columns []store.BoardColumn, except uint64) (string, string) {
	name := strings.TrimSpace(raw)
	n := utf8.RuneCountInString(name)
	if !utf8.ValidString(name) || n < columnNameMinLength || n > columnNameMaxLength {
		return name, "Use 1 to 40 characters."
	}
	for _, c := range columns {
		if c.ID != except && strings.EqualFold(c.Name, name) {
			return name, "A column with this name exists."
		}
	}
	return name, ""
}

// taskCards makes the cards of the task rows, in their order. today is
// midnight UTC of the current day.
func taskCards(rows []store.TaskRow, today time.Time) []TaskCard {
	cards := make([]TaskCard, len(rows))
	for i, r := range rows {
		c := TaskCard{
			ID: r.ID, Title: r.Title, PostID: r.PostID, RootID: r.RootID,
			ColumnID: r.ColumnID, ColumnName: r.ColumnName, Done: r.ColumnLast,
			Assignees: make([]UserRef, len(r.Assignees)), Position: r.Position,
		}
		for j, a := range r.Assignees {
			c.Assignees[j] = UserRef{ID: a.UserID, Username: a.Username, DisplayName: a.DisplayName}
		}
		if r.DueDate != nil {
			d := r.DueDate.UTC()
			c.DueDate = &d
			c.Overdue = d.Before(today) && !r.ColumnLast
		}
		cards[i] = c
	}
	return cards
}
