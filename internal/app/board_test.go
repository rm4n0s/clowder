package app_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/live"
)

// boardEnv is the liveEnv (lobby and den, a Recorder behind every service)
// with the board service. Every room has the columns To do, In progress and
// Done.
type boardEnv struct {
	*liveEnv
	board *app.Board
}

func newBoardEnv(t *testing.T) *boardEnv {
	t.Helper()
	e := &boardEnv{liveEnv: newLiveEnv(t)}
	e.board = app.NewBoard(e.st, e.clk, e.rec)
	return e
}

// view returns the board of the room as the user sees it.
func (e *boardEnv) view(user string, roomID uint64) app.BoardView {
	e.t.Helper()
	v, err := e.board.Board(e.t.Context(), e.as(user), roomID)
	if err != nil {
		e.t.Fatalf("Board: %+v", err)
	}
	return v
}

// titles returns the titles of the cards of each column of the view.
func titles(v app.BoardView) [][]string {
	out := make([][]string, len(v.Columns))
	for i, c := range v.Columns {
		out[i] = []string{}
		for _, card := range c.Tasks {
			out[i] = append(out[i], card.Title)
		}
	}
	return out
}

// requireDense fails the test unless every column of the view has the
// positions 0..n-1 in order.
func requireDense(t *testing.T, v app.BoardView) {
	t.Helper()
	for i, c := range v.Columns {
		if c.Position != i {
			t.Errorf("column %q has position %d, want %d", c.Name, c.Position, i)
		}
		for j, card := range c.Tasks {
			if card.Position != j {
				t.Errorf("card %q of %q has position %d, want %d", card.Title, c.Name, card.Position, j)
			}
		}
	}
}

// card returns the card with the title from the view.
func card(t *testing.T, v app.BoardView, title string) app.TaskCard {
	t.Helper()
	for _, c := range v.Columns {
		for _, card := range c.Tasks {
			if card.Title == title {
				return card
			}
		}
	}
	t.Fatalf("no card %q on the board %v", title, titles(v))
	return app.TaskCard{}
}

// cardByID returns the card with the ID from the view.
func cardByID(t *testing.T, v app.BoardView, id uint64) app.TaskCard {
	t.Helper()
	for _, c := range v.Columns {
		for _, card := range c.Tasks {
			if card.ID == id {
				return card
			}
		}
	}
	t.Fatalf("no card %d on the board %v", id, titles(v))
	return app.TaskCard{}
}

// withTasks has the user write a post with these tasks in the room and fails
// the test on an error.
func (e *boardEnv) withTasks(user string, roomID uint64, tasks ...app.TaskInput) app.Post {
	e.t.Helper()
	p, err := e.posts.Create(e.t.Context(), e.as(user), roomID,
		app.PostInput{Format: app.FormatText, Body: "a post", Tasks: tasks})
	if err != nil {
		e.t.Fatalf("Create: %+v", err)
	}
	return p
}

// newRoom makes a visible room of the admin, with bob as a member, and
// returns it. Tests that move cards use a room of their own, so that one
// fixture serves many cases.
func (e *boardEnv) newRoom(name string) app.Room {
	e.t.Helper()
	room, err := e.rooms.Create(e.t.Context(), e.as("admin"), app.RoomInput{Name: name, Visibility: app.VisibilityVisible})
	if err != nil {
		e.t.Fatalf("Create room %s: %+v", name, err)
	}
	if err := e.rooms.Join(e.t.Context(), e.as("bob"), room.ID); err != nil {
		e.t.Fatalf("Join %s: %+v", name, err)
	}
	return room
}

// seed fills the board of the room: layout holds the titles of the cards of
// each column, top to bottom. Each card is the task of a post of the admin.
// It forgets the events it made and returns the board.
func (e *boardEnv) seed(room app.Room, layout [][]string) app.BoardView {
	e.t.Helper()
	ctx := e.t.Context()
	cols := e.view("admin", room.ID).Columns
	for i, names := range layout {
		for _, title := range names {
			p := e.withTasks("admin", room.ID, app.TaskInput{Title: title})
			if i == 0 {
				continue
			}
			id := card(e.t, e.view("admin", room.ID), p.Tasks[0].Title).ID
			if err := e.board.MoveTask(ctx, e.as("admin"), id, cols[i].ID, 1000); err != nil {
				e.t.Fatalf("MoveTask: %+v", err)
			}
		}
	}
	e.rec.Reset()
	return e.view("admin", room.ID)
}

// boardEvts are the live events of a change that a post's task made on the
// board: the board, then the room and the thread of the post.
func boardEvts(room app.Room, p app.Post) []live.Event {
	return []live.Event{
		{Topic: live.BoardTopic(room.ID), Kind: live.KindBoardChanged, RoomID: room.ID, PostID: p.ID},
		{Topic: live.RoomTopic(room.ID), Kind: live.KindPostUpdated, RoomID: room.ID, PostID: p.ID},
		{Topic: live.PostTopic(p.ID), Kind: live.KindPostUpdated, RoomID: room.ID, PostID: p.ID},
	}
}

// columnEvts is the live event of a change to the columns of a room.
func columnEvts(room app.Room) []live.Event {
	return []live.Event{{Topic: live.BoardTopic(room.ID), Kind: live.KindBoardChanged, RoomID: room.ID}}
}

func TestBoardView(t *testing.T) {
	t.Parallel()
	e := newBoardEnv(t)
	// The clock is 2026-10-26 09:30 UTC. Due dates are given with a time and
	// a zone: only the day counts.
	yesterday := time.Date(2026, 10, 25, 23, 0, 0, 0, time.UTC)
	today := time.Date(2026, 10, 26, 0, 5, 0, 0, time.UTC)
	tomorrow := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	p1 := e.withTasks("bob", e.lobby.ID,
		app.TaskInput{Title: "Late", Assignees: []uint64{e.uid("bob"), e.uid("admin")}, DueDate: &yesterday},
		app.TaskInput{Title: "Today", DueDate: &today},
		app.TaskInput{Title: "Soon", DueDate: &tomorrow},
		app.TaskInput{Title: "Undated"},
	)
	cols := e.view("bob", e.lobby.ID).Columns
	move := func(title string, col int) {
		t.Helper()
		id := card(t, e.view("bob", e.lobby.ID), title).ID
		if err := e.board.MoveTask(t.Context(), e.as("bob"), id, cols[col].ID, 0); err != nil {
			t.Fatalf("MoveTask: %+v", err)
		}
	}
	// Late and Soon go to Done, where nothing is overdue.
	move("Late", 2)
	p2 := e.withTasks("admin", e.lobby.ID, app.TaskInput{Title: "Also late", DueDate: &yesterday})

	v := e.view("bob", e.lobby.ID)
	if v.Room.ID != e.lobby.ID || v.Room.Name != "lobby" || !v.CanEdit {
		t.Errorf("Room = %+v, CanEdit = %v", v.Room, v.CanEdit)
	}
	wantTitles := [][]string{{"Today", "Soon", "Undated", "Also late"}, {}, {"Late"}}
	if got := titles(v); !reflect.DeepEqual(got, wantTitles) {
		t.Fatalf("titles = %v, want %v", got, wantTitles)
	}
	requireDense(t, v)
	wantNames := []string{"To do", "In progress", "Done"}
	for i, c := range v.Columns {
		if c.Name != wantNames[i] || c.Last != (i == 2) {
			t.Errorf("column %d = %q, Last %v", i, c.Name, c.Last)
		}
	}
	overdue := map[string]bool{"Today": false, "Soon": false, "Undated": false, "Also late": true, "Late": false}
	for title, want := range overdue {
		if got := card(t, v, title).Overdue; got != want {
			t.Errorf("%q: Overdue = %v, want %v", title, got, want)
		}
	}
	late := card(t, v, "Late")
	if late.PostID != p1.ID || late.RootID != p1.ID || late.ColumnName != "Done" || late.ColumnID != cols[2].ID {
		t.Errorf("Late = %+v", late)
	}
	if late.DueDate == nil || !late.DueDate.Equal(time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("Late.DueDate = %v, want the day at midnight UTC", late.DueDate)
	}
	wantWho := []app.UserRef{{ID: e.uid("bob"), Username: "bob", DisplayName: "Bob"}, {ID: e.uid("admin"), Username: "admin", DisplayName: "Admin"}}
	if !reflect.DeepEqual(late.Assignees, wantWho) {
		t.Errorf("Late.Assignees = %+v, want %+v", late.Assignees, wantWho)
	}
	if got := card(t, v, "Undated"); got.DueDate != nil || len(got.Assignees) != 0 {
		t.Errorf("Undated = %+v", got)
	}
	if card(t, v, "Also late").PostID != p2.ID {
		t.Errorf("Also late is not the task of the second post")
	}

	// A day later, Today and Soon become overdue; the Done column stays calm.
	e.clk.Advance(48 * time.Hour)
	v = e.view("bob", e.lobby.ID)
	for title, want := range map[string]bool{"Today": true, "Soon": true, "Undated": false, "Late": false} {
		if got := card(t, v, title).Overdue; got != want {
			t.Errorf("two days later, %q: Overdue = %v, want %v", title, got, want)
		}
	}
	// Moving the last column to the front makes another column the last one:
	// Late is overdue now, and a card of the new last column is not.
	if err := e.board.MoveColumn(t.Context(), e.as("bob"), cols[2].ID, 0); err != nil {
		t.Fatalf("MoveColumn: %+v", err)
	}
	if err := e.board.MoveTask(t.Context(), e.as("bob"), card(t, v, "Today").ID, cols[1].ID, 0); err != nil {
		t.Fatalf("MoveTask: %+v", err)
	}
	v = e.view("bob", e.lobby.ID)
	if !v.Columns[2].Last || v.Columns[0].Last || v.Columns[2].Name != "In progress" || !card(t, v, "Late").Overdue || card(t, v, "Today").Overdue {
		t.Errorf("after moving Done to the front: Late overdue %v, Today overdue %v", card(t, v, "Late").Overdue, card(t, v, "Today").Overdue)
	}
}

func TestBoardViewForEveryReader(t *testing.T) {
	t.Parallel()
	e := newBoardEnv(t)
	e.withTasks("bob", e.lobby.ID, app.TaskInput{Title: "One"})
	for _, tc := range []struct {
		user    string
		canEdit bool
	}{{"bob", true}, {"admin", true}, {"root", false}, {"alice", false}, {"carol", false}} {
		v := e.view(tc.user, e.lobby.ID)
		if v.CanEdit != tc.canEdit || v.Room.IsMember != tc.canEdit {
			t.Errorf("%s: CanEdit = %v, IsMember = %v, want %v", tc.user, v.CanEdit, v.Room.IsMember, tc.canEdit)
		}
		if got := titles(v); !reflect.DeepEqual(got, [][]string{{"One"}, {}, {}}) {
			t.Errorf("%s sees %v", tc.user, got)
		}
	}
}

func TestBoardPermissions(t *testing.T) {
	t.Parallel()
	routes := map[string]string{app.TagNotFound: notFoundPrivate}
	cases := []permCase{
		{"admin", "lobby", ""}, {"bob", "lobby", ""}, {"root", "lobby", ""}, {"alice", "lobby", ""}, {"carol", "lobby", ""},
		{"alice", "den", ""}, {"bob", "den", ""},
		{"admin", "den", app.TagNotFound}, {"root", "den", app.TagNotFound}, {"carol", "den", app.TagNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.actor+" on "+tc.room, func(t *testing.T) {
			e := newBoardEnv(t)
			v, err := e.board.Board(t.Context(), e.as(tc.actor), e.room(tc.room).ID)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("got %+v, want success", err)
				}
				if len(v.Columns) != 3 {
					t.Errorf("%d columns", len(v.Columns))
				}
				return
			}
			_ = requireTag(t, err, tc.want, routes[tc.want])
		})
	}
	e := newBoardEnv(t)
	_, err := e.board.Board(t.Context(), e.as("admin"), 9999)
	_ = requireTag(t, err, app.TagNotFound, notFoundMissing)
}

// boardCase is one row of a permission table of a method of the board: who
// calls it on a room, and the tag it must fail with. A fixture of the case has
// a task (written by bob) in the room.
type boardCase = permCase

// runBoardPermissions runs call as the actor of each case on a fresh fixture
// where bob has written a post with the task "Fix login" in lobby and in den,
// and checks the result. A failed call writes no audit event and publishes
// nothing.
func runBoardPermissions(t *testing.T, routes map[string]string, cases []boardCase, call func(e *boardEnv, a app.Actor, room app.Room, task app.TaskCard) error) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.actor+" on "+tc.room, func(t *testing.T) {
			e := newBoardEnv(t)
			room := e.room(tc.room)
			e.withTasks("bob", room.ID, app.TaskInput{Title: "Fix login"})
			task := card(t, e.view("bob", room.ID), "Fix login")
			before := len(e.events())
			e.rec.Reset()
			err := call(e, e.as(tc.actor), room, task)
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
			if got := e.rec.Events(); len(got) != 0 {
				t.Errorf("a failed call published %+v", got)
			}
		})
	}
}

// mutatingCases is the table of the methods that only members call: admin and
// bob are members of lobby, alice and bob of den.
var mutatingCases = []boardCase{
	{"admin", "lobby", ""}, {"bob", "lobby", ""},
	{"root", "lobby", app.TagForbidden}, {"alice", "lobby", app.TagForbidden}, {"carol", "lobby", app.TagForbidden},
	{"alice", "den", ""}, {"bob", "den", ""},
	{"admin", "den", app.TagNotFound}, {"root", "den", app.TagNotFound}, {"carol", "den", app.TagNotFound},
}

func TestBoardMoveTaskPermissions(t *testing.T) {
	t.Parallel()
	routes := map[string]string{
		app.TagNotFound:  "Board.MoveTask.func1->loadTaskRoom->loadReadableRoom.NotFound",
		app.TagForbidden: "Board.MoveTask.func1.Forbidden",
	}
	runBoardPermissions(t, routes, mutatingCases, func(e *boardEnv, a app.Actor, room app.Room, task app.TaskCard) error {
		cols := e.view("bob", room.ID).Columns
		return e.board.MoveTask(t.Context(), a, task.ID, cols[1].ID, 0)
	})
}

func TestBoardUpdateTaskPermissions(t *testing.T) {
	t.Parallel()
	routes := map[string]string{
		app.TagNotFound:  "Board.UpdateTask.func1->loadTaskRoom->loadReadableRoom.NotFound",
		app.TagForbidden: "Board.UpdateTask.func1.Forbidden",
	}
	title := "Fix the login"
	runBoardPermissions(t, routes, mutatingCases, func(e *boardEnv, a app.Actor, room app.Room, task app.TaskCard) error {
		return e.board.UpdateTask(t.Context(), a, task.ID, app.TaskPatch{Title: &title})
	})
}

func TestBoardAddColumnPermissions(t *testing.T) {
	t.Parallel()
	routes := map[string]string{
		app.TagNotFound:  notFoundPrivate,
		app.TagForbidden: "Board.AddColumn.func1.Forbidden",
	}
	runBoardPermissions(t, routes, mutatingCases, func(e *boardEnv, a app.Actor, room app.Room, _ app.TaskCard) error {
		_, err := e.board.AddColumn(t.Context(), a, room.ID, "Review")
		return err
	})
}

func TestBoardRenameColumnPermissions(t *testing.T) {
	t.Parallel()
	routes := map[string]string{
		app.TagNotFound:  "Board.RenameColumn.func1->loadColumnRoom->loadReadableRoom.NotFound",
		app.TagForbidden: "Board.RenameColumn.func1.Forbidden",
	}
	runBoardPermissions(t, routes, mutatingCases, func(e *boardEnv, a app.Actor, room app.Room, _ app.TaskCard) error {
		return e.board.RenameColumn(t.Context(), a, e.view("bob", room.ID).Columns[0].ID, "Backlog")
	})
}

func TestBoardMoveColumnPermissions(t *testing.T) {
	t.Parallel()
	routes := map[string]string{
		app.TagNotFound:  "Board.MoveColumn.func1->loadColumnRoom->loadReadableRoom.NotFound",
		app.TagForbidden: "Board.MoveColumn.func1.Forbidden",
	}
	runBoardPermissions(t, routes, mutatingCases, func(e *boardEnv, a app.Actor, room app.Room, _ app.TaskCard) error {
		return e.board.MoveColumn(t.Context(), a, e.view("bob", room.ID).Columns[2].ID, 0)
	})
}

func TestBoardDeleteColumnPermissions(t *testing.T) {
	t.Parallel()
	routes := map[string]string{
		app.TagNotFound:  "Board.DeleteColumn.func1->loadColumnRoom->loadReadableRoom.NotFound",
		app.TagForbidden: "Board.DeleteColumn.func1.Forbidden",
	}
	runBoardPermissions(t, routes, mutatingCases, func(e *boardEnv, a app.Actor, room app.Room, _ app.TaskCard) error {
		// The last column is empty, so a member may delete it.
		return e.board.DeleteColumn(t.Context(), a, e.view("bob", room.ID).Columns[2].ID)
	})
}

func TestBoardMissingThings(t *testing.T) {
	t.Parallel()
	e := newBoardEnv(t)
	ctx := t.Context()
	cols := e.view("bob", e.lobby.ID).Columns
	e.withTasks("bob", e.lobby.ID, app.TaskInput{Title: "Fix login"})
	task := card(t, e.view("bob", e.lobby.ID), "Fix login")
	// A column and a task of the private room den, for a member of lobby.
	e.withTasks("bob", e.den.ID, app.TaskInput{Title: "Den task"})
	denView := e.view("bob", e.den.ID)
	denTask, denCol := card(t, denView, "Den task"), denView.Columns[1]
	title := "x"
	e.rec.Reset()
	calls := map[string]struct {
		err   error
		route string
	}{
		"MoveTask missing task":         {e.board.MoveTask(ctx, e.as("bob"), 9999, cols[0].ID, 0), "Board.MoveTask.func1->loadTaskRoom->Store.TaskByID.NotFound"},
		"MoveTask missing column":       {e.board.MoveTask(ctx, e.as("bob"), task.ID, 9999, 0), "Board.MoveTask.func1->Store.BoardColumnByID.NotFound"},
		"MoveTask column of a room":     {e.board.MoveTask(ctx, e.as("bob"), task.ID, denCol.ID, 0), "Board.MoveTask.func1.NotFound"},
		"MoveTask task of a room":       {e.board.MoveTask(ctx, e.as("carol"), denTask.ID, cols[0].ID, 0), "Board.MoveTask.func1->loadTaskRoom->loadReadableRoom.NotFound"},
		"UpdateTask missing task":       {e.board.UpdateTask(ctx, e.as("bob"), 9999, app.TaskPatch{Title: &title}), "Board.UpdateTask.func1->loadTaskRoom->Store.TaskByID.NotFound"},
		"RenameColumn missing column":   {e.board.RenameColumn(ctx, e.as("bob"), 9999, "x"), "Board.RenameColumn.func1->loadColumnRoom->Store.BoardColumnByID.NotFound"},
		"MoveColumn missing column":     {e.board.MoveColumn(ctx, e.as("bob"), 9999, 0), "Board.MoveColumn.func1->loadColumnRoom->Store.BoardColumnByID.NotFound"},
		"DeleteColumn missing column":   {e.board.DeleteColumn(ctx, e.as("bob"), 9999), "Board.DeleteColumn.func1->loadColumnRoom->Store.BoardColumnByID.NotFound"},
		"RenameColumn of a private one": {e.board.RenameColumn(ctx, e.as("carol"), denCol.ID, "x"), "loadReadableRoom.NotFound"},
	}
	for name, c := range calls {
		t.Run(name, func(t *testing.T) {
			_ = requireTag(t, c.err, app.TagNotFound, c.route)
		})
	}
	_, err := e.board.AddColumn(ctx, e.as("bob"), 9999, "x")
	_ = requireTag(t, err, app.TagNotFound, "Board.AddColumn.func1->loadReadableRoom->Store.RoomByID.NotFound")
	if got := e.rec.Events(); len(got) != 0 {
		t.Errorf("failed calls published %+v", got)
	}
}

func TestBoardMoveTaskPositions(t *testing.T) {
	t.Parallel()
	e := newBoardEnv(t)
	start := [][]string{{"a", "b", "c"}, {"x", "y"}, {}}
	tests := []struct {
		name   string
		task   string
		column int
		pos    int
		want   [][]string
	}{
		{"within, down", "a", 0, 2, [][]string{{"b", "c", "a"}, {"x", "y"}, {}}},
		{"within, up", "c", 0, 0, [][]string{{"c", "a", "b"}, {"x", "y"}, {}}},
		{"within, to the middle", "a", 0, 1, [][]string{{"b", "a", "c"}, {"x", "y"}, {}}},
		{"within, to the same place", "b", 0, 1, start},
		{"within, past the end", "a", 0, 99, [][]string{{"b", "c", "a"}, {"x", "y"}, {}}},
		{"within, before the start", "c", 0, -3, [][]string{{"c", "a", "b"}, {"x", "y"}, {}}},
		{"within, swap the last two", "y", 1, 0, [][]string{{"a", "b", "c"}, {"y", "x"}, {}}},
		{"to another column, at the start", "b", 1, 0, [][]string{{"a", "c"}, {"b", "x", "y"}, {}}},
		{"to another column, in the middle", "b", 1, 1, [][]string{{"a", "c"}, {"x", "b", "y"}, {}}},
		{"to another column, at the end", "b", 1, 2, [][]string{{"a", "c"}, {"x", "y", "b"}, {}}},
		{"to another column, past the end", "b", 1, 99, [][]string{{"a", "c"}, {"x", "y", "b"}, {}}},
		{"to another column, before the start", "b", 1, -1, [][]string{{"a", "c"}, {"b", "x", "y"}, {}}},
		{"to an empty column", "a", 2, 5, [][]string{{"b", "c"}, {"x", "y"}, {"a"}}},
		{"the only card of a column leaves", "x", 0, 0, [][]string{{"x", "a", "b", "c"}, {"y"}, {}}},
		{"back to the first column, in the middle", "y", 0, 2, [][]string{{"a", "b", "y", "c"}, {"x"}, {}}},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			room := e.newRoom("moves-" + string(rune('a'+i)))
			v := e.seed(room, start)
			before := len(e.events())
			if err := e.board.MoveTask(t.Context(), e.as("bob"), card(t, v, tt.task).ID, v.Columns[tt.column].ID, tt.pos); err != nil {
				t.Fatalf("MoveTask: %+v", err)
			}
			got := e.view("bob", room.ID)
			if !reflect.DeepEqual(titles(got), tt.want) {
				t.Errorf("board = %v, want %v", titles(got), tt.want)
			}
			requireDense(t, got)
			changed := !reflect.DeepEqual(tt.want, start)
			if n := len(e.events()) - before; (n == 1) != changed || n > 1 {
				t.Errorf("%d audit events, want %v", n, map[bool]int{true: 1, false: 0}[changed])
			}
			wantLive := 0
			if changed {
				wantLive = 3
			}
			if n := len(e.rec.Events()); n != wantLive {
				t.Errorf("%d live events, want %d: %+v", n, wantLive, e.rec.Events())
			}
		})
	}
}

func TestBoardMoveTask(t *testing.T) {
	t.Parallel()
	e := newBoardEnv(t)
	e.clk.Advance(time.Hour)
	p := e.withTasks("bob", e.lobby.ID,
		app.TaskInput{Title: "Fix login"}, app.TaskInput{Title: "Ship it"})
	v := e.view("admin", e.lobby.ID)
	fix := card(t, v, "Fix login")
	before := len(e.events())
	e.rec.Reset()
	e.clk.Advance(time.Minute)
	if err := e.board.MoveTask(t.Context(), e.as("admin"), fix.ID, v.Columns[1].ID, 0); err != nil {
		t.Fatalf("MoveTask: %+v", err)
	}
	e.requireNewEvents(before, wantEvent{
		actor: "admin", action: app.ActionTaskMoved, room: e.lobby.ID, entityType: app.EntityTask, entityID: fix.ID,
		summary: `Admin moved "Fix login" to In progress in #lobby`,
		details: map[string]any{
			"column":   map[string]any{"old": "To do", "new": "In progress"},
			"position": map[string]any{"old": float64(0), "new": float64(0)},
		},
	})
	if got := e.rec.Events(); !reflect.DeepEqual(got, boardEvts(e.lobby, p)) {
		t.Errorf("published %+v, want %+v", got, boardEvts(e.lobby, p))
	}
	// Within the column: another summary and details.
	before = len(e.events())
	other := card(t, e.view("admin", e.lobby.ID), "Ship it")
	if err := e.board.MoveTask(t.Context(), e.as("bob"), other.ID, v.Columns[0].ID, 5); err != nil {
		t.Fatalf("MoveTask: %+v", err)
	}
	if n := len(e.events()) - before; n != 0 {
		t.Fatalf("a move to the same place wrote %d events", n)
	}
	if err := e.board.MoveTask(t.Context(), e.as("bob"), other.ID, v.Columns[1].ID, 1); err != nil {
		t.Fatalf("MoveTask: %+v", err)
	}
	before = len(e.events())
	if err := e.board.MoveTask(t.Context(), e.as("bob"), other.ID, v.Columns[1].ID, 0); err != nil {
		t.Fatalf("MoveTask: %+v", err)
	}
	e.requireNewEvents(before, wantEvent{
		actor: "bob", action: app.ActionTaskMoved, room: e.lobby.ID, entityType: app.EntityTask, entityID: other.ID,
		summary: `Bob moved "Ship it" within In progress in #lobby`,
		details: map[string]any{
			"column":   map[string]any{"old": "In progress", "new": "In progress"},
			"position": map[string]any{"old": float64(1), "new": float64(0)},
		},
	})
}

func TestBoardMoveTaskOfAComment(t *testing.T) {
	t.Parallel()
	e := newBoardEnv(t)
	p := e.write("bob", "lobby", "a post")
	c, err := e.posts.Reply(t.Context(), e.as("admin"), p.ID, app.PostInput{
		Format: app.FormatText, Body: "a comment", Tasks: []app.TaskInput{{Title: "From a comment"}},
	})
	if err != nil {
		t.Fatalf("Reply: %+v", err)
	}
	v := e.view("bob", e.lobby.ID)
	task := card(t, v, "From a comment")
	if task.PostID != c.ID || task.RootID != p.ID {
		t.Errorf("card = %+v, want post %d and root %d", task, c.ID, p.ID)
	}
	e.rec.Reset()
	if err := e.board.MoveTask(t.Context(), e.as("bob"), task.ID, v.Columns[2].ID, 0); err != nil {
		t.Fatalf("MoveTask: %+v", err)
	}
	want := []live.Event{
		{Topic: live.BoardTopic(e.lobby.ID), Kind: live.KindBoardChanged, RoomID: e.lobby.ID, PostID: c.ID},
		{Topic: live.PostTopic(p.ID), Kind: live.KindCommentUpdated, RoomID: e.lobby.ID, PostID: c.ID, RootID: p.ID},
		{Topic: live.RoomTopic(e.lobby.ID), Kind: live.KindPostUpdated, RoomID: e.lobby.ID, PostID: p.ID, RootID: p.ID},
	}
	if got := e.rec.Events(); !reflect.DeepEqual(got, want) {
		t.Errorf("published %+v, want %+v", got, want)
	}
	// The comment shows its task in its new column.
	got, err := e.posts.Get(t.Context(), e.as("bob"), c.ID)
	if err != nil || len(got.Tasks) != 1 || got.Tasks[0].ColumnName != "Done" {
		t.Errorf("Get = %+v, %v", got.Tasks, err)
	}
}

func TestBoardUpdateTask(t *testing.T) {
	t.Parallel()
	e := newBoardEnv(t)
	due := time.Date(2026, 11, 3, 18, 0, 0, 0, time.FixedZone("x", 5*3600))
	p := e.withTasks("bob", e.lobby.ID, app.TaskInput{Title: "Fix login", Assignees: []uint64{e.uid("bob")}, DueDate: &due})
	task := card(t, e.view("bob", e.lobby.ID), "Fix login")
	if task.DueDate == nil || !task.DueDate.Equal(time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("DueDate = %v", task.DueDate)
	}
	str := func(s string) *string { return &s }
	ids := func(n ...string) *[]uint64 {
		out := []uint64{}
		for _, name := range n {
			out = append(out, e.uid(name))
		}
		return &out
	}
	newDue := time.Date(2026, 12, 1, 23, 59, 0, 0, time.UTC)
	tests := []struct {
		name  string
		patch app.TaskPatch
		// want is the audit event it writes, or nil for a no-op.
		want *wantEvent
		// check looks at the card afterwards.
		check func(c app.TaskCard) bool
	}{
		{"empty patch", app.TaskPatch{}, nil, nil},
		{"same title", app.TaskPatch{Title: str(" Fix login ")}, nil, nil},
		{"same assignees", app.TaskPatch{Assignees: ids("bob")}, nil, nil},
		{"same due date", app.TaskPatch{DueDate: &due}, nil, nil},
		{"title", app.TaskPatch{Title: str("  Fix the login  ")}, &wantEvent{
			summary: `Admin edited the task "Fix the login" in #lobby`,
			details: map[string]any{"title": map[string]any{"old": "Fix login", "new": "Fix the login"}},
		}, func(c app.TaskCard) bool { return c.Title == "Fix the login" }},
		{"assignees", app.TaskPatch{Assignees: ids("admin", "bob", "admin")}, &wantEvent{
			summary: `Admin edited the task "Fix the login" in #lobby`,
			details: map[string]any{"assignees": map[string]any{"old": []any{"bob"}, "new": []any{"admin", "bob"}}},
		}, func(c app.TaskCard) bool { return len(c.Assignees) == 2 && c.Assignees[0].Username == "admin" }},
		{"unassign everybody", app.TaskPatch{Assignees: ids()}, &wantEvent{
			summary: `Admin edited the task "Fix the login" in #lobby`,
			details: map[string]any{"assignees": map[string]any{"old": []any{"admin", "bob"}, "new": []any{}}},
		}, func(c app.TaskCard) bool { return len(c.Assignees) == 0 }},
		{"due date", app.TaskPatch{DueDate: &newDue}, &wantEvent{
			summary: `Admin edited the task "Fix the login" in #lobby`,
			details: map[string]any{"dueDate": map[string]any{"old": "2026-11-03", "new": "2026-12-01"}},
		}, func(c app.TaskCard) bool {
			return c.DueDate != nil && c.DueDate.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))
		}},
		{"all three", app.TaskPatch{Title: str("Fix it"), Assignees: ids("bob"), ClearDueDate: true}, &wantEvent{
			summary: `Admin edited the task "Fix it" in #lobby`,
			details: map[string]any{
				"title":     map[string]any{"old": "Fix the login", "new": "Fix it"},
				"assignees": map[string]any{"old": []any{}, "new": []any{"bob"}},
				"dueDate":   map[string]any{"old": "2026-12-01", "new": nil},
			},
		}, func(c app.TaskCard) bool { return c.DueDate == nil && c.Title == "Fix it" && len(c.Assignees) == 1 }},
		{"clear a missing due date", app.TaskPatch{ClearDueDate: true}, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := len(e.events())
			e.rec.Reset()
			if err := e.board.UpdateTask(t.Context(), e.as("admin"), task.ID, tt.patch); err != nil {
				t.Fatalf("UpdateTask: %+v", err)
			}
			if tt.want == nil {
				if n := len(e.events()) - before; n != 0 {
					t.Errorf("wrote %d events", n)
				}
				if got := e.rec.Events(); len(got) != 0 {
					t.Errorf("published %+v", got)
				}
				return
			}
			w := *tt.want
			w.actor, w.action, w.room, w.entityType, w.entityID = "admin", app.ActionTaskEdited, e.lobby.ID, app.EntityTask, task.ID
			e.requireNewEvents(before, w)
			wantLive := boardEvts(e.lobby, p)
			if tt.name == "all three" {
				// bob is newly assigned; the actor is admin.
				wantLive = append(wantLive, notifEvt(e.uid("bob"), e.lobby.ID, p.ID))
			}
			if got := e.rec.Events(); !reflect.DeepEqual(got, wantLive) {
				t.Errorf("published %+v, want %+v", got, wantLive)
			}
			if c := cardByID(t, e.view("admin", e.lobby.ID), task.ID); !tt.check(c) {
				t.Errorf("card = %+v", c)
			}
		})
	}
}

func TestBoardUpdateTaskInvalid(t *testing.T) {
	t.Parallel()
	e := newBoardEnv(t)
	e.withTasks("bob", e.lobby.ID, app.TaskInput{Title: "Fix login"})
	task := card(t, e.view("bob", e.lobby.ID), "Fix login")
	str := func(s string) *string { return &s }
	ids := func(n ...uint64) *[]uint64 { return &n }
	var eleven []uint64
	for i := range 11 {
		eleven = append(eleven, uint64(1000+i))
	}
	due := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		patch app.TaskPatch
		field string
	}{
		{"empty title", app.TaskPatch{Title: str("")}, app.FieldTitle},
		{"blank title", app.TaskPatch{Title: str(" \t ")}, app.FieldTitle},
		{"title of 201 characters", app.TaskPatch{Title: str(strings.Repeat("é", 201))}, app.FieldTitle},
		{"title that is not text", app.TaskPatch{Title: str("a\xffb")}, app.FieldTitle},
		{"assignee who is not a member", app.TaskPatch{Assignees: ids(e.uid("carol"))}, app.FieldAssignees},
		{"assignee who is a member of another room only", app.TaskPatch{Assignees: ids(e.uid("alice"))}, app.FieldAssignees},
		{"assignee who does not exist", app.TaskPatch{Assignees: ids(9999)}, app.FieldAssignees},
		{"one good and one wrong assignee", app.TaskPatch{Assignees: ids(e.uid("bob"), e.uid("dave"))}, app.FieldAssignees},
		{"eleven assignees", app.TaskPatch{Assignees: &eleven}, app.FieldAssignees},
		{"a due date and clear", app.TaskPatch{DueDate: &due, ClearDueDate: true}, app.FieldDueDate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := len(e.events())
			e.rec.Reset()
			err := e.board.UpdateTask(t.Context(), e.as("bob"), task.ID, tt.patch)
			appErr := requireTag(t, err, app.TagInvalidInput, "Board.UpdateTask.func1.InvalidInput")
			if got := fieldsOf(t, appErr); len(got) != 1 || got[tt.field] == "" {
				t.Errorf("fields = %v, want only %s", got, tt.field)
			}
			if n := len(e.events()) - before; n != 0 {
				t.Errorf("wrote %d events", n)
			}
			if got := e.rec.Events(); len(got) != 0 {
				t.Errorf("published %+v", got)
			}
		})
	}
	if c := card(t, e.view("bob", e.lobby.ID), "Fix login"); len(c.Assignees) != 0 || c.DueDate != nil {
		t.Errorf("a refused patch changed the card: %+v", c)
	}
}

func TestBoardUpdateTaskKeepsOtherFields(t *testing.T) {
	t.Parallel()
	e := newBoardEnv(t)
	due := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	e.withTasks("bob", e.lobby.ID, app.TaskInput{Title: "Fix login", Assignees: []uint64{e.uid("bob")}, DueDate: &due})
	task := card(t, e.view("bob", e.lobby.ID), "Fix login")
	title := "Renamed"
	if err := e.board.UpdateTask(t.Context(), e.as("bob"), task.ID, app.TaskPatch{Title: &title}); err != nil {
		t.Fatalf("UpdateTask: %+v", err)
	}
	got := card(t, e.view("bob", e.lobby.ID), "Renamed")
	if len(got.Assignees) != 1 || got.DueDate == nil || !got.DueDate.Equal(due) {
		t.Errorf("card = %+v, want the assignee and the due date kept", got)
	}
}

func TestBoardAddColumn(t *testing.T) {
	t.Parallel()
	e := newBoardEnv(t)
	before := len(e.events())
	e.rec.Reset()
	col, err := e.board.AddColumn(t.Context(), e.as("bob"), e.lobby.ID, "  Review  ")
	if err != nil {
		t.Fatalf("AddColumn: %+v", err)
	}
	if col.ID == 0 || col.Name != "Review" || col.Position != 3 || !col.Last {
		t.Errorf("column = %+v", col)
	}
	e.requireNewEvents(before, wantEvent{
		actor: "bob", action: app.ActionColumnCreated, room: e.lobby.ID, entityType: app.EntityColumn, entityID: col.ID,
		summary: `Bob added the column "Review" to #lobby`,
		details: map[string]any{
			"name":     map[string]any{"old": nil, "new": "Review"},
			"position": map[string]any{"old": nil, "new": float64(3)},
		},
	})
	if got := e.rec.Events(); !reflect.DeepEqual(got, columnEvts(e.lobby)) {
		t.Errorf("published %+v", got)
	}
	v := e.view("admin", e.lobby.ID)
	if len(v.Columns) != 4 || v.Columns[3].ID != col.ID || !v.Columns[3].Last || v.Columns[2].Last {
		t.Errorf("columns = %+v", v.Columns)
	}
	requireDense(t, v)
	// The same name in another room is fine.
	if _, err := e.board.AddColumn(t.Context(), e.as("alice"), e.den.ID, "Review"); err != nil {
		t.Errorf("AddColumn in another room: %+v", err)
	}

	names := []struct {
		name string
		in   string
	}{
		{"empty", ""}, {"blank", "   "}, {"41 characters", strings.Repeat("x", 41)}, {"not text", "a\xffb"},
		{"taken", "Review"}, {"taken in another case", "to DO"}, {"taken after trimming", "  done "},
	}
	for _, tt := range names {
		t.Run(tt.name, func(t *testing.T) {
			before := len(e.events())
			e.rec.Reset()
			_, err := e.board.AddColumn(t.Context(), e.as("bob"), e.lobby.ID, tt.in)
			appErr := requireTag(t, err, app.TagInvalidInput, "Board.AddColumn.func1.InvalidInput")
			if got := fieldsOf(t, appErr); len(got) != 1 || got[app.FieldName] == "" {
				t.Errorf("fields = %v", got)
			}
			if n := len(e.events()) - before; n != 0 || len(e.rec.Events()) != 0 {
				t.Errorf("wrote %d events, published %+v", n, e.rec.Events())
			}
		})
	}
	// 40 characters are fine.
	if _, err := e.board.AddColumn(t.Context(), e.as("bob"), e.lobby.ID, strings.Repeat("é", 40)); err != nil {
		t.Errorf("AddColumn of 40 characters: %+v", err)
	}
}

func TestBoardRenameColumn(t *testing.T) {
	t.Parallel()
	e := newBoardEnv(t)
	cols := e.view("bob", e.lobby.ID).Columns
	rename := func(i int, name string) error {
		return e.board.RenameColumn(t.Context(), e.as("bob"), cols[i].ID, name)
	}
	before := len(e.events())
	e.rec.Reset()
	if err := rename(0, " Backlog "); err != nil {
		t.Fatalf("RenameColumn: %+v", err)
	}
	e.requireNewEvents(before, wantEvent{
		actor: "bob", action: app.ActionColumnRenamed, room: e.lobby.ID, entityType: app.EntityColumn, entityID: cols[0].ID,
		summary: `Bob renamed the column "To do" to "Backlog" in #lobby`,
		details: map[string]any{"name": map[string]any{"old": "To do", "new": "Backlog"}},
	})
	if got := e.rec.Events(); !reflect.DeepEqual(got, columnEvts(e.lobby)) {
		t.Errorf("published %+v", got)
	}
	if got := e.view("bob", e.lobby.ID).Columns[0].Name; got != "Backlog" {
		t.Errorf("name = %q", got)
	}

	// Only the case changes: allowed. The same name: nothing happens.
	before = len(e.events())
	if err := rename(0, "BACKLOG"); err != nil {
		t.Fatalf("RenameColumn to another case of its own name: %+v", err)
	}
	if n := len(e.events()) - before; n != 1 {
		t.Errorf("a change of case wrote %d events, want 1", n)
	}
	before = len(e.events())
	e.rec.Reset()
	if err := rename(0, " BACKLOG "); err != nil {
		t.Fatalf("RenameColumn to the same name: %+v", err)
	}
	if n := len(e.events()) - before; n != 0 || len(e.rec.Events()) != 0 {
		t.Errorf("the same name wrote %d events and published %+v", n, e.rec.Events())
	}

	invalid := []struct {
		name string
		col  int
		in   string
	}{
		{"empty", 0, ""}, {"too long", 0, strings.Repeat("x", 41)},
		{"taken", 0, "In progress"}, {"taken in another case", 1, "done"}, {"taken after trimming", 2, " BACKLOG "},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			before := len(e.events())
			e.rec.Reset()
			appErr := requireTag(t, rename(tt.col, tt.in), app.TagInvalidInput, "Board.RenameColumn.func1.InvalidInput")
			if got := fieldsOf(t, appErr); len(got) != 1 || got[app.FieldName] == "" {
				t.Errorf("fields = %v", got)
			}
			if n := len(e.events()) - before; n != 0 || len(e.rec.Events()) != 0 {
				t.Errorf("wrote %d events, published %+v", n, e.rec.Events())
			}
		})
	}
	// The name "To do" is free again after the rename.
	if err := rename(1, "To do"); err != nil {
		t.Errorf("RenameColumn to a name that was freed: %+v", err)
	}
}

func TestBoardMoveColumn(t *testing.T) {
	t.Parallel()
	e := newBoardEnv(t)
	room := e.newRoom("columns")
	names := func() []string {
		var out []string
		v := e.view("admin", room.ID)
		requireDense(t, v)
		for _, c := range v.Columns {
			out = append(out, c.Name)
		}
		return out
	}
	tests := []struct {
		name   string
		column string
		pos    int
		want   []string
		// summary is the audit summary, empty for a no-op.
		summary string
		from    int
		to      int
	}{
		{"to the start", "Done", 0, []string{"Done", "To do", "In progress"}, `Admin moved the column "Done" to the start of the board in #columns`, 2, 0},
		{"to the right", "To do", 2, []string{"In progress", "Done", "To do"}, `Admin moved the column "To do" after "Done" in #columns`, 0, 2},
		{"to the middle", "To do", 1, []string{"In progress", "To do", "Done"}, `Admin moved the column "To do" after "In progress" in #columns`, 0, 1},
		{"to the same place", "To do", 0, nil, "", 0, 0},
		{"the last one, past the end", "Done", 99, nil, "", 0, 0},
		{"past the end", "In progress", 99, []string{"To do", "Done", "In progress"}, `Admin moved the column "In progress" after "Done" in #columns`, 1, 2},
		{"before the start", "In progress", -5, []string{"In progress", "To do", "Done"}, `Admin moved the column "In progress" to the start of the board in #columns`, 1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset the order to To do, In progress, Done.
			cur := e.view("admin", room.ID).Columns
			byName := map[string]uint64{}
			for _, c := range cur {
				byName[c.Name] = c.ID
			}
			for i, n := range []string{"To do", "In progress", "Done"} {
				if err := e.board.MoveColumn(t.Context(), e.as("admin"), byName[n], i); err != nil {
					t.Fatalf("reset: %+v", err)
				}
			}
			before := len(e.events())
			e.rec.Reset()
			if err := e.board.MoveColumn(t.Context(), e.as("bob"), byName[tt.column], tt.pos); err != nil {
				t.Fatalf("MoveColumn: %+v", err)
			}
			if tt.summary == "" {
				if n := len(e.events()) - before; n != 0 || len(e.rec.Events()) != 0 {
					t.Errorf("wrote %d events, published %+v", n, e.rec.Events())
				}
				return
			}
			if got := names(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("columns = %v, want %v", got, tt.want)
			}
			e.requireNewEvents(before, wantEvent{
				actor: "bob", action: app.ActionColumnMoved, room: room.ID, entityType: app.EntityColumn, entityID: byName[tt.column],
				summary: strings.Replace(tt.summary, "Admin", "Bob", 1),
				details: map[string]any{
					"name":     tt.column,
					"position": map[string]any{"old": float64(tt.from), "new": float64(tt.to)},
				},
			})
			if got := e.rec.Events(); !reflect.DeepEqual(got, columnEvts(room)) {
				t.Errorf("published %+v", got)
			}
		})
	}
}

func TestBoardDeleteColumn(t *testing.T) {
	t.Parallel()
	e := newBoardEnv(t)
	room := e.newRoom("deleting")
	e.withTasks("admin", room.ID, app.TaskInput{Title: "Fix login"})
	v := e.view("admin", room.ID)

	// A column with cards is refused.
	before := len(e.events())
	e.rec.Reset()
	appErr := requireTag(t, e.board.DeleteColumn(t.Context(), e.as("bob"), v.Columns[0].ID), app.TagConflict, "Board.DeleteColumn.func1.Conflict")
	if appErr.Message != "Move its cards first." || appErr.Metadata["reason"] != "cards" {
		t.Errorf("message %q, metadata %v", appErr.Message, appErr.Metadata)
	}
	if n := len(e.events()) - before; n != 0 || len(e.rec.Events()) != 0 {
		t.Errorf("a refused delete wrote %d events and published %+v", n, e.rec.Events())
	}

	// An empty one in the middle goes, and the positions close up.
	if err := e.board.DeleteColumn(t.Context(), e.as("bob"), v.Columns[1].ID); err != nil {
		t.Fatalf("DeleteColumn: %+v", err)
	}
	e.requireNewEvents(before, wantEvent{
		actor: "bob", action: app.ActionColumnDeleted, room: room.ID, entityType: app.EntityColumn, entityID: v.Columns[1].ID,
		summary: `Bob deleted the column "In progress" from #deleting`,
		details: map[string]any{
			"name":     map[string]any{"old": "In progress", "new": nil},
			"position": map[string]any{"old": float64(1), "new": nil},
		},
	})
	if got := e.rec.Events(); !reflect.DeepEqual(got, columnEvts(room)) {
		t.Errorf("published %+v", got)
	}
	v = e.view("admin", room.ID)
	if got := titles(v); !reflect.DeepEqual(got, [][]string{{"Fix login"}, {}}) || v.Columns[1].Name != "Done" || !v.Columns[1].Last {
		t.Errorf("board = %v / %+v", got, v.Columns)
	}
	requireDense(t, v)

	// The cards move to Done; the first column is empty now and goes; the
	// last one left is refused, with cards or without.
	if err := e.board.MoveTask(t.Context(), e.as("bob"), v.Columns[0].Tasks[0].ID, v.Columns[1].ID, 0); err != nil {
		t.Fatalf("MoveTask: %+v", err)
	}
	if err := e.board.DeleteColumn(t.Context(), e.as("bob"), v.Columns[0].ID); err != nil {
		t.Fatalf("DeleteColumn of an emptied column: %+v", err)
	}
	v = e.view("admin", room.ID)
	requireDense(t, v)
	before = len(e.events())
	e.rec.Reset()
	appErr = requireTag(t, e.board.DeleteColumn(t.Context(), e.as("bob"), v.Columns[0].ID), app.TagConflict, "Board.DeleteColumn.func1.Conflict")
	if appErr.Message != "A board needs at least one column." || appErr.Metadata["reason"] != "last" {
		t.Errorf("message %q, metadata %v", appErr.Message, appErr.Metadata)
	}
	if n := len(e.events()) - before; n != 0 || len(e.rec.Events()) != 0 {
		t.Errorf("a refused delete wrote %d events and published %+v", n, e.rec.Events())
	}
	if got := titles(e.view("admin", room.ID)); !reflect.DeepEqual(got, [][]string{{"Fix login"}}) {
		t.Errorf("board = %v", got)
	}
	// With the card removed (its post deleted) the column is still the last.
	p, err := e.posts.Feed(t.Context(), e.as("admin"), room.ID, 0, 10)
	if err != nil || len(p.Posts) != 1 {
		t.Fatalf("Feed: %+v, %v", p, err)
	}
	if err := e.posts.Delete(t.Context(), e.as("admin"), p.Posts[0].ID); err != nil {
		t.Fatalf("Delete: %+v", err)
	}
	_ = requireTag(t, e.board.DeleteColumn(t.Context(), e.as("bob"), v.Columns[0].ID), app.TagConflict, "Board.DeleteColumn.func1.Conflict")
	// New tasks go to the first (and only) column.
	e.withTasks("bob", room.ID, app.TaskInput{Title: "Again"})
	if got := titles(e.view("admin", room.ID)); !reflect.DeepEqual(got, [][]string{{"Again"}}) {
		t.Errorf("board = %v", got)
	}
}
