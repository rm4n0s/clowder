package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
)

// boardWriteStatuses is the table of who changes a board: the members of the
// room (D25). A visible room that the user has not joined is 403, a private
// room the user is not in is 404, admins included.
var boardWriteStatuses = []struct {
	user, room string
	want       int // 0 for success
}{
	{"bob", "lobby", 0}, {"admin", "lobby", 0},
	{"alice", "lobby", http.StatusForbidden}, {"carol", "lobby", http.StatusForbidden}, {"root", "lobby", http.StatusForbidden},
	{"alice", "den", 0}, {"bob", "den", 0},
	{"admin", "den", http.StatusNotFound}, {"root", "den", http.StatusNotFound}, {"carol", "den", http.StatusNotFound},
}

// taskOnBoard has the user write a post with a task in the room and returns
// the card.
func (f *forum) taskOnBoard(user, room, title string) app.TaskCard {
	f.t.Helper()
	p, err := f.posts.Create(f.t.Context(), actorOf(f.testEnv, user), f.room(room).ID,
		app.PostInput{Format: app.FormatText, Body: "a post", Tasks: []app.TaskInput{{Title: title}}})
	if err != nil {
		f.t.Fatalf("Create: %+v", err)
	}
	return p.Tasks[0]
}

// httptestGet makes a GET request to the target.
func httptestGet(target string) *http.Request {
	return httptest.NewRequest(http.MethodGet, target, nil)
}

// boardOf reads the board of the room as the user through the API.
func (f *forum) boardOf(user, room string) map[string]any {
	f.t.Helper()
	return f.ok(user, http.MethodGet, path("/rooms/%d/board", f.room(room).ID), nil, http.StatusOK)
}

// columnsOf returns the columns of a board answer.
func columnsOf(t *testing.T, board map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, c := range list(t, board["columns"]) {
		out = append(out, obj(t, c))
	}
	return out
}

// layout returns the titles of the cards of each column of a board answer.
func layout(t *testing.T, board map[string]any) [][]string {
	t.Helper()
	var out [][]string
	for _, c := range columnsOf(t, board) {
		titles := []string{}
		for _, card := range list(t, c["tasks"]) {
			titles = append(titles, obj(t, card)["title"].(string))
		}
		out = append(out, titles)
	}
	return out
}

// columnIDs returns the IDs of the columns of a board answer, left to right.
func columnIDs(t *testing.T, board map[string]any) []uint64 {
	t.Helper()
	var out []uint64
	for _, c := range columnsOf(t, board) {
		out = append(out, idOf(t, c))
	}
	return out
}

func TestBoard(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	// The cards: two in To do, one in Done, written by bob, with an assignee and a due date.
	f.taskOnBoard("bob", "lobby", "first")
	second := f.taskOnBoard("bob", "lobby", "second")
	third := f.taskOnBoard("bob", "lobby", "third")
	cols := columnIDs(t, f.boardOf("bob", "lobby"))
	f.ok("bob", http.MethodPost, path("/tasks/%d/move", third.ID), map[string]any{"column_id": cols[2], "position": 0}, http.StatusNoContent)
	f.ok("bob", http.MethodPatch, path("/tasks/%d", second.ID),
		map[string]any{"assignee_ids": []uint64{f.userID("admin")}, "due_date": "2026-11-05"}, http.StatusOK)

	board := f.boardOf("carol", "lobby")
	if board["room_id"] != float64(f.lobby.ID) {
		t.Errorf("room_id = %v", board["room_id"])
	}
	columns := columnsOf(t, board)
	names := []string{}
	for i, c := range columns {
		names = append(names, c["name"].(string))
		if c["position"] != float64(i) || c["done"] != (i == len(columns)-1) {
			t.Errorf("column %v: position %v, done %v", c["name"], c["position"], c["done"])
		}
	}
	if !reflect.DeepEqual(names, []string{"To do", "In progress", "Done"}) {
		t.Errorf("columns = %v", names)
	}
	if got := layout(t, board); !reflect.DeepEqual(got, [][]string{{"first", "second"}, {}, {"third"}}) {
		t.Errorf("cards = %v", got)
	}
	card := obj(t, list(t, columns[0]["tasks"])[1])
	if card["id"] != float64(second.ID) || card["post_id"] != float64(second.PostID) || card["position"] != float64(1) ||
		card["column_id"] != columns[0]["id"] || card["due_date"] != "2026-11-05" {
		t.Errorf("card = %v", card)
	}
	if as := list(t, card["assignees"]); len(as) != 1 || obj(t, as[0])["username"] != "admin" {
		t.Errorf("assignees = %v", as)
	}
	if first := obj(t, list(t, columns[0]["tasks"])[0]); first["position"] != float64(0) || first["due_date"] != nil {
		t.Errorf("first card = %v", first)
	}
	// The post of a card opens as a thread.
	f.ok("carol", http.MethodGet, path("/posts/%d/thread", second.PostID), nil, http.StatusOK)
}

func TestBoardPermissions(t *testing.T) {
	t.Parallel()
	for _, tt := range roomReadStatuses {
		t.Run(tt.user+" on "+tt.room, func(t *testing.T) {
			t.Parallel()
			f := newForum(t)
			rec := f.call(tt.user, http.MethodGet, path("/rooms/%d/board", f.room(tt.room).ID), nil)
			requireStatus(t, rec, tt.want)
		})
	}
	f := newForum(t)
	for _, id := range []string{"999999", "0", "abc", "-1", "18446744073709551616"} {
		requireStatus(t, f.call("alice", http.MethodGet, "/api/v1/rooms/"+id+"/board", nil), http.StatusNotFound)
	}
}

// TestBoardChangesPermissions runs every route that changes a board as each
// user: the members succeed, the others get the status of the table, and a
// refused call changes nothing and writes no audit event.
func TestBoardChangesPermissions(t *testing.T) {
	t.Parallel()
	type route struct {
		name    string
		success int
		call    func(f *forum, user, room string, card app.TaskCard, extra uint64) (method, target string, body any)
		audit   string
	}
	routes := []route{
		{"add a column", http.StatusCreated, func(f *forum, user, room string, card app.TaskCard, extra uint64) (string, string, any) {
			return http.MethodPost, path("/rooms/%d/columns", f.room(room).ID), map[string]any{"name": "Review"}
		}, "column.created column"},
		{"rename a column", http.StatusOK, func(f *forum, user, room string, card app.TaskCard, extra uint64) (string, string, any) {
			return http.MethodPatch, path("/columns/%d", card.ColumnID), map[string]any{"name": "Backlog"}
		}, "column.renamed column"},
		{"move a column", http.StatusNoContent, func(f *forum, user, room string, card app.TaskCard, extra uint64) (string, string, any) {
			return http.MethodPost, path("/columns/%d/move", card.ColumnID), map[string]any{"position": 1}
		}, "column.moved column"},
		{"delete a column", http.StatusNoContent, func(f *forum, user, room string, card app.TaskCard, extra uint64) (string, string, any) {
			return http.MethodDelete, path("/columns/%d", extra), nil
		}, "column.deleted column"},
		{"change a card", http.StatusOK, func(f *forum, user, room string, card app.TaskCard, extra uint64) (string, string, any) {
			return http.MethodPatch, path("/tasks/%d", card.ID), map[string]any{"due_date": "2026-12-01"}
		}, "task.edited task"},
		{"move a card", http.StatusNoContent, func(f *forum, user, room string, card app.TaskCard, extra uint64) (string, string, any) {
			return http.MethodPost, path("/tasks/%d/move", card.ID), map[string]any{"column_id": extra, "position": 0}
		}, "task.moved task"},
	}
	for _, rt := range routes {
		for _, tt := range boardWriteStatuses {
			t.Run(rt.name+": "+tt.user+" on "+tt.room, func(t *testing.T) {
				t.Parallel()
				f := newForum(t)
				card := f.taskOnBoard("bob", tt.room, "Fix login")
				// A column to delete and to move cards into: the one that is last of three is not
				// used, a new fourth column is empty.
				extra, err := f.board.AddColumn(t.Context(), actorOf(f.testEnv, "bob"), f.room(tt.room).ID, "Extra")
				if err != nil {
					t.Fatal(err)
				}
				method, target, body := rt.call(f, tt.user, tt.room, card, extra.ID)
				before := len(f.audit())
				rec := f.call(tt.user, method, target, body)
				if tt.want == 0 {
					requireStatus(t, rec, rt.success)
					f.requireAudit(before, rt.audit)
					return
				}
				requireStatus(t, rec, tt.want)
				f.requireAudit(before)
			})
		}
	}
}

func TestAddColumn(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	before := len(f.audit())
	rec := f.call("bob", http.MethodPost, path("/rooms/%d/columns", f.lobby.ID), map[string]any{"name": "  Review  "})
	requireStatus(t, rec, http.StatusCreated)
	col := decode(t, rec)
	if col["name"] != "Review" || col["position"] != float64(3) || col["done"] != true || len(list(t, col["tasks"])) != 0 {
		t.Errorf("column = %v", col)
	}
	if loc := rec.Header().Get("Location"); loc != fmt.Sprintf("/api/v1/rooms/%d/board", f.lobby.ID) {
		t.Errorf("Location = %q", loc)
	}
	f.requireAudit(before, "column.created column")
	board := f.boardOf("bob", "lobby")
	ids := columnIDs(t, board)
	if len(ids) != 4 || ids[3] != uint64(col["id"].(float64)) {
		t.Errorf("columns = %v", ids)
	}
	// The one before it is not done any more.
	if columnsOf(t, board)[2]["done"] != false {
		t.Errorf("the old last column is still done: %v", columnsOf(t, board)[2])
	}

	for _, tt := range []struct {
		name string
		body any
	}{
		{"no name", map[string]any{}},
		{"a blank name", map[string]any{"name": "   "}},
		{"a name of 41 characters", map[string]any{"name": strings.Repeat("c", 41)}},
		{"a name that is taken", map[string]any{"name": "In Progress"}},
		{"the name of the column just made", map[string]any{"name": "review"}},
		{"a name that is a number", `{"name": 5}`},
		{"an unknown field", map[string]any{"name": "x", "position": 1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := len(f.audit())
			rec := f.call("bob", http.MethodPost, path("/rooms/%d/columns", f.lobby.ID), tt.body)
			requireStatus(t, rec, http.StatusUnprocessableEntity)
			if len(errorFields(t, rec)) != 1 {
				t.Errorf("fields = %v", errorFields(t, rec))
			}
			f.requireAudit(before)
		})
	}
	t.Run("a name of 40 characters", func(t *testing.T) {
		requireStatus(t, f.call("bob", http.MethodPost, path("/rooms/%d/columns", f.lobby.ID), map[string]any{"name": strings.Repeat("c", 40)}), http.StatusCreated)
	})
}

func TestRenameColumn(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	card := f.taskOnBoard("bob", "lobby", "card")
	before := len(f.audit())
	col := f.ok("bob", http.MethodPatch, path("/columns/%d", card.ColumnID), map[string]any{"name": "Backlog"}, http.StatusOK)
	if col["name"] != "Backlog" || col["position"] != float64(0) || col["done"] != false {
		t.Errorf("column = %v", col)
	}
	if tasks := list(t, col["tasks"]); len(tasks) != 1 || obj(t, tasks[0])["title"] != "card" {
		t.Errorf("the renamed column has its cards: %v", col["tasks"])
	}
	f.requireAudit(before, "column.renamed column")
	// Another case of its own name is allowed, and the same name changes nothing.
	f.ok("bob", http.MethodPatch, path("/columns/%d", card.ColumnID), map[string]any{"name": "BACKLOG"}, http.StatusOK)
	before = len(f.audit())
	f.ok("bob", http.MethodPatch, path("/columns/%d", card.ColumnID), map[string]any{"name": "BACKLOG"}, http.StatusOK)
	f.requireAudit(before)
	for _, name := range []string{"", "  ", "done", strings.Repeat("c", 41)} {
		rec := f.call("bob", http.MethodPatch, path("/columns/%d", card.ColumnID), map[string]any{"name": name})
		requireStatus(t, rec, http.StatusUnprocessableEntity)
		if errorFields(t, rec)["name"] == nil {
			t.Errorf("name %q: fields = %v", name, errorFields(t, rec))
		}
	}
}

func TestMoveColumn(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	ids := columnIDs(t, f.boardOf("bob", "lobby")) // To do, In progress, Done
	order := func() []uint64 { return columnIDs(t, f.boardOf("bob", "lobby")) }
	move := func(id uint64, position any) {
		t.Helper()
		f.ok("bob", http.MethodPost, path("/columns/%d/move", id), map[string]any{"position": position}, http.StatusNoContent)
	}
	move(ids[2], 0)
	if got := order(); !reflect.DeepEqual(got, []uint64{ids[2], ids[0], ids[1]}) {
		t.Errorf("order = %v", got)
	}
	move(ids[2], 99) // past the end is the end
	if got := order(); !reflect.DeepEqual(got, ids) {
		t.Errorf("order = %v, want %v", got, ids)
	}
	move(ids[0], -5) // below 0 is 0
	if got := order(); !reflect.DeepEqual(got, ids) {
		t.Errorf("order = %v, want %v", got, ids)
	}
	board := f.boardOf("bob", "lobby")
	for i, c := range columnsOf(t, board) {
		if c["position"] != float64(i) {
			t.Errorf("column %d has position %v", i, c["position"])
		}
	}
	before := len(f.audit())
	move(ids[0], 0) // where it is
	f.requireAudit(before)
	for _, tt := range []struct {
		name string
		body any
	}{
		{"no position", map[string]any{}},
		{"a null position", `{"position": null}`},
		{"a position that is text", `{"position": "1"}`},
		{"a position that is a fraction", `{"position": 1.5}`},
		{"an unknown field", map[string]any{"position": 1, "name": "x"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.call("bob", http.MethodPost, path("/columns/%d/move", ids[0]), tt.body)
			requireStatus(t, rec, http.StatusUnprocessableEntity)
			if len(errorFields(t, rec)) != 1 {
				t.Errorf("fields = %v", errorFields(t, rec))
			}
		})
	}
}

func TestDeleteColumn(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	card := f.taskOnBoard("bob", "lobby", "card")
	ids := columnIDs(t, f.boardOf("bob", "lobby"))
	before := len(f.audit())
	rec := f.call("bob", http.MethodDelete, path("/columns/%d", card.ColumnID), nil)
	requireStatus(t, rec, http.StatusConflict)
	if errorCode(t, rec) != "conflict" {
		t.Errorf("code = %q", errorCode(t, rec))
	}
	f.requireAudit(before)
	f.ok("bob", http.MethodDelete, path("/columns/%d", ids[1]), nil, http.StatusNoContent)
	f.requireAudit(before, "column.deleted column")
	if got := columnIDs(t, f.boardOf("bob", "lobby")); !reflect.DeepEqual(got, []uint64{ids[0], ids[2]}) {
		t.Errorf("columns = %v", got)
	}
	// Move the card away: the column can go.
	f.ok("bob", http.MethodPost, path("/tasks/%d/move", card.ID), map[string]any{"column_id": ids[2], "position": 0}, http.StatusNoContent)
	f.ok("bob", http.MethodDelete, path("/columns/%d", ids[0]), nil, http.StatusNoContent)
	// A room keeps one column.
	before = len(f.audit())
	rec = f.call("bob", http.MethodDelete, path("/columns/%d", ids[2]), nil)
	requireStatus(t, rec, http.StatusConflict)
	f.requireAudit(before)
	// It is gone: 404 for a second delete.
	requireStatus(t, f.call("bob", http.MethodDelete, path("/columns/%d", ids[0]), nil), http.StatusNotFound)
}

func TestChangeACard(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	card := f.taskOnBoard("bob", "lobby", "card")
	patch := func(body any) map[string]any {
		t.Helper()
		return f.ok("bob", http.MethodPatch, path("/tasks/%d", card.ID), body, http.StatusOK)
	}
	admin, bob := f.userID("admin"), f.userID("bob")
	names := func(task map[string]any) []string {
		out := []string{}
		for _, a := range list(t, task["assignees"]) {
			out = append(out, obj(t, a)["username"].(string))
		}
		return out
	}

	got := patch(map[string]any{"assignee_ids": []uint64{admin, bob}, "due_date": "2026-11-05"})
	if !reflect.DeepEqual(names(got), []string{"admin", "bob"}) || got["due_date"] != "2026-11-05" || got["title"] != "card" ||
		got["id"] != float64(card.ID) || got["post_id"] != float64(card.PostID) || got["position"] != float64(0) || got["column_id"] != float64(card.ColumnID) {
		t.Errorf("task = %v", got)
	}

	t.Run("a body with nothing keeps both", func(t *testing.T) {
		before := len(f.audit())
		got := patch(map[string]any{})
		if !reflect.DeepEqual(names(got), []string{"admin", "bob"}) || got["due_date"] != "2026-11-05" {
			t.Errorf("task = %v", got)
		}
		f.requireAudit(before)
	})
	t.Run("only the assignees leave the due date", func(t *testing.T) {
		got := patch(map[string]any{"assignee_ids": []uint64{bob}})
		if !reflect.DeepEqual(names(got), []string{"bob"}) || got["due_date"] != "2026-11-05" {
			t.Errorf("task = %v", got)
		}
	})
	t.Run("only the due date leaves the assignees", func(t *testing.T) {
		got := patch(map[string]any{"due_date": "2026-11-06"})
		if !reflect.DeepEqual(names(got), []string{"bob"}) || got["due_date"] != "2026-11-06" {
			t.Errorf("task = %v", got)
		}
	})
	t.Run("null assignees are like absent", func(t *testing.T) {
		got := patch(`{"assignee_ids": null}`)
		if !reflect.DeepEqual(names(got), []string{"bob"}) {
			t.Errorf("task = %v", got)
		}
	})
	t.Run("a null due date clears it and keeps the assignees", func(t *testing.T) {
		before := len(f.audit())
		got := patch(`{"due_date": null}`)
		if !reflect.DeepEqual(names(got), []string{"bob"}) || got["due_date"] != nil {
			t.Errorf("task = %v", got)
		}
		f.requireAudit(before, "task.edited task")
	})
	t.Run("an empty list unassigns everybody", func(t *testing.T) {
		got := patch(map[string]any{"assignee_ids": []uint64{}})
		if len(names(got)) != 0 {
			t.Errorf("task = %v", got)
		}
	})
	t.Run("both at once", func(t *testing.T) {
		got := patch(map[string]any{"assignee_ids": []uint64{admin}, "due_date": "2027-01-31"})
		if !reflect.DeepEqual(names(got), []string{"admin"}) || got["due_date"] != "2027-01-31" {
			t.Errorf("task = %v", got)
		}
	})
	t.Run("the change shows on the board and on the post", func(t *testing.T) {
		board := f.boardOf("carol", "lobby")
		onBoard := obj(t, list(t, columnsOf(t, board)[0]["tasks"])[0])
		if onBoard["due_date"] != "2027-01-31" || !reflect.DeepEqual(names(onBoard), []string{"admin"}) {
			t.Errorf("card = %v", onBoard)
		}
		post := f.ok("carol", http.MethodGet, path("/posts/%d", card.PostID), nil, http.StatusOK)
		if task := obj(t, list(t, post["tasks"])[0]); task["due_date"] != "2027-01-31" {
			t.Errorf("task = %v", task)
		}
	})
	t.Run("the assignee is notified once", func(t *testing.T) {
		// admin was assigned just now by bob: one notification of the kind.
		page := f.ok("admin", http.MethodGet, "/api/v1/notifications", nil, http.StatusOK)
		assigned := 0
		for _, n := range list(t, page["notifications"]) {
			if obj(t, n)["kind"] == "assigned" {
				assigned++
			}
		}
		if assigned == 0 {
			t.Errorf("notifications = %v", page["notifications"])
		}
	})

	for _, tt := range []struct {
		name  string
		body  any
		field string
	}{
		{"an assignee who is not a member", map[string]any{"assignee_ids": []uint64{f.userID("carol")}}, "assignee_ids"},
		{"eleven assignees", map[string]any{"assignee_ids": []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}}, "assignee_ids"},
		{"a due date that is no day", map[string]any{"due_date": "27/10/2026"}, "due_date"},
		{"a due date with a time", map[string]any{"due_date": "2026-10-27T10:00:00Z"}, "due_date"},
		{"an empty due date", map[string]any{"due_date": ""}, "due_date"},
		{"a due date that is a number", `{"due_date": 5}`, "due_date"},
		{"a title: it is edited with the post", map[string]any{"title": "new"}, "title"},
		{"an unknown field", map[string]any{"column_id": 1}, "column_id"},
		{"assignees that are text", `{"assignee_ids": "bob"}`, "assignee_ids"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := len(f.audit())
			rec := f.call("bob", http.MethodPatch, path("/tasks/%d", card.ID), tt.body)
			requireStatus(t, rec, http.StatusUnprocessableEntity)
			if errorFields(t, rec)[tt.field] == nil {
				t.Errorf("fields = %v, want %q", errorFields(t, rec), tt.field)
			}
			f.requireAudit(before)
		})
	}
}

func TestMoveACard(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	for _, title := range []string{"a", "b", "c"} {
		f.taskOnBoard("bob", "lobby", title)
	}
	board := f.boardOf("bob", "lobby")
	ids := columnIDs(t, board)
	cards := map[string]uint64{}
	for _, c := range list(t, columnsOf(t, board)[0]["tasks"]) {
		cards[obj(t, c)["title"].(string)] = idOf(t, c)
	}
	move := func(title string, column uint64, position int) {
		t.Helper()
		f.ok("bob", http.MethodPost, path("/tasks/%d/move", cards[title]), map[string]any{"column_id": column, "position": position}, http.StatusNoContent)
	}
	check := func(want [][]string) {
		t.Helper()
		board := f.boardOf("bob", "lobby")
		if got := layout(t, board); !reflect.DeepEqual(got, want) {
			t.Errorf("cards = %v, want %v", got, want)
		}
		for _, c := range columnsOf(t, board) {
			for i, card := range list(t, c["tasks"]) {
				if obj(t, card)["position"] != float64(i) || obj(t, card)["column_id"] != c["id"] {
					t.Errorf("card %v in %v: position and column are wrong", card, c["name"])
				}
			}
		}
	}
	check([][]string{{"a", "b", "c"}, {}, {}})
	move("c", ids[1], 0)
	check([][]string{{"a", "b"}, {"c"}, {}})
	move("a", ids[1], 0)
	check([][]string{{"b"}, {"a", "c"}, {}})
	move("b", ids[1], 1)
	check([][]string{{}, {"a", "b", "c"}, {}})
	move("c", ids[1], 0) // within the column
	check([][]string{{}, {"c", "a", "b"}, {}})
	move("c", ids[2], 99) // past the end is the end
	check([][]string{{}, {"a", "b"}, {"c"}})
	move("a", ids[0], -3) // below 0 is 0
	check([][]string{{"a"}, {"b"}, {"c"}})

	before := len(f.audit())
	move("a", ids[0], 0) // where it is
	f.requireAudit(before)

	t.Run("the move shows on the post", func(t *testing.T) {
		post := f.ok("bob", http.MethodGet, path("/posts/%d", f.mustCard(cards["c"]).PostID), nil, http.StatusOK)
		if task := obj(t, list(t, post["tasks"])[0]); task["column_id"] != float64(ids[2]) {
			t.Errorf("task = %v", task)
		}
	})
	for _, tt := range []struct {
		name   string
		body   any
		fields []string
	}{
		{"nothing", map[string]any{}, []string{"column_id", "position"}},
		{"no position", map[string]any{"column_id": ids[0]}, []string{"position"}},
		{"no column", map[string]any{"position": 0}, []string{"column_id"}},
		{"nulls", `{"column_id": null, "position": null}`, []string{"column_id", "position"}},
		{"a column that is text", `{"column_id": "1", "position": 0}`, []string{"column_id"}},
		{"an unknown field", map[string]any{"column_id": ids[0], "position": 0, "title": "x"}, []string{"title"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.call("bob", http.MethodPost, path("/tasks/%d/move", cards["a"]), tt.body)
			requireStatus(t, rec, http.StatusUnprocessableEntity)
			for _, field := range tt.fields {
				if errorFields(t, rec)[field] == nil {
					t.Errorf("fields = %v, want %q", errorFields(t, rec), field)
				}
			}
		})
	}
	t.Run("a column that is not there", func(t *testing.T) {
		requireStatus(t, f.call("bob", http.MethodPost, path("/tasks/%d/move", cards["a"]), map[string]any{"column_id": 999999, "position": 0}), http.StatusNotFound)
	})
	t.Run("a column of another room", func(t *testing.T) {
		den := f.den
		other, err := f.board.Board(t.Context(), actorOf(f.testEnv, "bob"), den.ID)
		if err != nil {
			t.Fatal(err)
		}
		rec := f.call("bob", http.MethodPost, path("/tasks/%d/move", cards["a"]), map[string]any{"column_id": other.Columns[0].ID, "position": 0})
		requireStatus(t, rec, http.StatusNotFound)
	})
	t.Run("a card that is not there", func(t *testing.T) {
		requireStatus(t, f.call("bob", http.MethodPost, path("/tasks/%d/move", 999999), map[string]any{"column_id": ids[0], "position": 0}), http.StatusNotFound)
	})
}

// mustCard reads a card through the service.
func (f *forum) mustCard(id uint64) app.TaskCard {
	f.t.Helper()
	card, err := f.board.Task(f.t.Context(), actorOf(f.testEnv, "bob"), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return card
}

// TestPrivateBoardIsNotFound checks that every board route answers 404 for the
// card and the column of a private room to somebody outside it, and says no
// more than it does for one that is not there.
func TestPrivateBoardIsNotFound(t *testing.T) {
	t.Parallel()
	f := newForum(t)
	card := f.taskOnBoard("bob", "den", "secret")
	for _, user := range []string{"carol", "admin", "root"} {
		for _, tt := range []struct {
			name, method, target string
			body                 any
		}{
			{"patch", http.MethodPatch, path("/tasks/%d", card.ID), map[string]any{"due_date": "2026-11-05"}},
			{"move", http.MethodPost, path("/tasks/%d/move", card.ID), map[string]any{"column_id": card.ColumnID, "position": 0}},
			{"rename", http.MethodPatch, path("/columns/%d", card.ColumnID), map[string]any{"name": "x"}},
			{"move column", http.MethodPost, path("/columns/%d/move", card.ColumnID), map[string]any{"position": 0}},
			{"delete column", http.MethodDelete, path("/columns/%d", card.ColumnID), nil},
			{"add column", http.MethodPost, path("/rooms/%d/columns", f.den.ID), map[string]any{"name": "x"}},
			{"board", http.MethodGet, path("/rooms/%d/board", f.den.ID), nil},
		} {
			t.Run(user+": "+tt.name, func(t *testing.T) {
				rec := f.call(user, tt.method, tt.target, tt.body)
				requireStatus(t, rec, http.StatusNotFound)
				if strings.Contains(rec.Body.String(), "secret") {
					t.Error("the answer names the card")
				}
			})
		}
	}
}

// TestRoutesOfTheErrorsOfTheBoard checks the route of every tag that the board,
// search and notification routes make in this package.
func TestRoutesOfTheErrorsOfTheBoard(t *testing.T) {
	t.Parallel()
	position, column := 0, uint64(3)
	notADay := "soon"
	zero := uint64(0)
	tests := []struct {
		name  string
		tag   string
		route string
		run   func() error
		field string
	}{
		{"a column move with no position", tagFieldMissing, "columnPosition.FieldMissing", func() error { _, err := columnPosition(moveColumnRequest{}); return err }, "position"},
		{"a card move with no column", tagFieldMissing, "cardTarget.FieldMissing", func() error {
			_, _, err := cardTarget(moveTaskRequest{Position: &position})
			return err
		}, "column_id"},
		{"a card move with no position", tagFieldMissing, "cardTarget.FieldMissing", func() error {
			_, _, err := cardTarget(moveTaskRequest{ColumnID: &column})
			return err
		}, "position"},
		{"no notification to read up to", tagFieldMissing, "upToID.FieldMissing", func() error { _, err := upToID(notificationsReadRequest{}); return err }, "up_to_id"},
		{"notification 0", tagFieldMissing, "upToID.FieldMissing", func() error { _, err := upToID(notificationsReadRequest{UpToID: &zero}); return err }, "up_to_id"},
		{"a due date that is no day", tagTaskDueInvalid, "taskPatchOf.TaskDueInvalid", func() error {
			_, err := taskPatchOf(taskPatchRequest{DueDate: optionalDate{Set: true, Value: &notADay}})
			return err
		}, "due_date"},
		{"a page of 0", tagPageInvalid, "searchPage.PageInvalid", func() error {
			_, err := searchPage(httptestGet("/x?page=0"))
			return err
		}, "page"},
		{"a page that is text", tagPageInvalid, "searchPage.PageInvalid", func() error {
			_, err := searchPage(httptestGet("/x?page=x"))
			return err
		}, "page"},
		{"an empty page", tagPageInvalid, "searchPage.PageInvalid", func() error {
			_, err := searchPage(httptestGet("/x?page="))
			return err
		}, "page"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.run()
			appErr := requireRoute(t, err, tt.tag, tt.route)
			fields, _ := appErr.Metadata["fields"].(map[string]string)
			if fields[tt.field] == "" {
				t.Errorf("fields = %v, want one for %q", fields, tt.field)
			}
			p, internal := problemOf(err)
			if internal || p.status != http.StatusUnprocessableEntity || p.object.Code != codeInvalidInput || p.object.Fields[tt.field] == "" {
				t.Errorf("problem = %+v, internal %v", p, internal)
			}
		})
	}
	t.Run("the pages", func(t *testing.T) {
		t.Parallel()
		for query, want := range map[string]int{"": 1, "?page=1": 1, "?page=7": 7} {
			if got, err := searchPage(httptestGet("/x" + query)); err != nil || got != want {
				t.Errorf("searchPage(%q) = %d, %v, want %d", query, got, err, want)
			}
		}
	})
	t.Run("the patch of a body", func(t *testing.T) {
		t.Parallel()
		day := "2026-11-05"
		ids := []uint64{1}
		got, err := taskPatchOf(taskPatchRequest{AssigneeIDs: &ids, DueDate: optionalDate{Set: true, Value: &day}})
		if err != nil || got.DueDate == nil || got.DueDate.Format(dateLayout) != day || got.ClearDueDate || got.Assignees == nil {
			t.Errorf("patch = %+v, %v", got, err)
		}
		got, err = taskPatchOf(taskPatchRequest{DueDate: optionalDate{Set: true}})
		if err != nil || got.DueDate != nil || !got.ClearDueDate {
			t.Errorf("patch = %+v, %v, want the due date cleared", got, err)
		}
		got, err = taskPatchOf(taskPatchRequest{})
		if err != nil || got.DueDate != nil || got.ClearDueDate || got.Assignees != nil {
			t.Errorf("patch = %+v, %v, want nothing", got, err)
		}
	})
}
