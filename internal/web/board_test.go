package web

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
)

// boardSeed is the board of "general" that the board tests start from:
//
//	To do        Late (bob, overdue), Soon (due later), Plain, Comment task
//	In progress  (empty)
//	Done         Finished (bob, due in the past: not overdue in the last column)
type boardSeed struct {
	// root is alice's post with Late, Soon and Plain; other is bob's post with
	// Finished; comment is the comment with the task "Comment task".
	root, other, comment app.Post
	late, soon, plain    app.TaskCard
	finished, onComment  app.TaskCard
	todo, doing, done    uint64
}

// seedBoard writes the tasks of boardSeed in general.
func (e *roomsEnv) seedBoard() boardSeed {
	e.t.Helper()
	past, future := mustDay(e.t, pastDue), mustDay(e.t, futureDue)
	var s boardSeed
	s.root = e.writeTasks("alice", "general", "Planning",
		app.TaskInput{Title: "Late", Assignees: []uint64{e.bob.ID}, DueDate: &past},
		app.TaskInput{Title: "Soon", DueDate: &future},
		app.TaskInput{Title: "Plain"},
	)
	s.other = e.writeTasks("bob", "general", "Shipping", app.TaskInput{Title: "Finished", DueDate: &past})
	reply, err := e.posts.Reply(e.t.Context(), e.actor(e.bob), s.other.ID, app.PostInput{
		Format: app.FormatText, Body: "also this", Tasks: []app.TaskInput{{Title: "Comment task"}},
	})
	if err != nil {
		e.t.Fatalf("Reply: %+v", err)
	}
	s.comment = reply
	view := e.boardOf("alice", "general")
	s.todo, s.doing, s.done = view.Columns[0].ID, view.Columns[1].ID, view.Columns[2].ID
	if err := e.board.MoveTask(e.t.Context(), e.actor(e.alice), e.cardTitled("general", "Finished").ID, s.done, 0); err != nil {
		e.t.Fatalf("MoveTask: %+v", err)
	}
	s.late, s.soon, s.plain = e.cardTitled("general", "Late"), e.cardTitled("general", "Soon"), e.cardTitled("general", "Plain")
	s.finished, s.onComment = e.cardTitled("general", "Finished"), e.cardTitled("general", "Comment task")
	return s
}

// boardOf returns the board of the room as the user sees it.
func (e *roomsEnv) boardOf(user, room string) app.BoardView {
	e.t.Helper()
	view, err := e.board.Board(e.t.Context(), e.actor(e.users(user)), e.room(room).ID)
	if err != nil {
		e.t.Fatalf("Board: %+v", err)
	}
	return view
}

// cardTitled returns the card with the title on the board of the room.
func (e *roomsEnv) cardTitled(room, title string) app.TaskCard {
	e.t.Helper()
	for _, c := range e.boardOf("alice", room).Columns {
		for _, t := range c.Tasks {
			if t.Title == title {
				return t
			}
		}
	}
	e.t.Fatalf("no card %q in %s", title, room)
	return app.TaskCard{}
}

// layout returns the titles of the cards of each column of the board of the
// room, left to right.
func (e *roomsEnv) layout(room string) [][]string {
	e.t.Helper()
	var out [][]string
	for _, c := range e.boardOf("alice", room).Columns {
		titles := []string{}
		for _, t := range c.Tasks {
			titles = append(titles, t.Title)
		}
		out = append(out, titles)
	}
	return out
}

// columnNames returns the names of the columns of the board of the room, left
// to right.
func (e *roomsEnv) columnNames(room string) []string {
	e.t.Helper()
	var out []string
	for _, c := range e.boardOf("alice", room).Columns {
		out = append(out, c.Name)
	}
	return out
}

// boardURL is the path of the board of the room, with the suffix.
func (e *roomsEnv) boardURL(room, suffix string) string {
	return "/rooms/" + idText(e.room(room).ID) + "/board" + suffix
}

// sameLayout reports whether two layouts are equal.
func sameLayout(a, b [][]string) bool {
	return slices.EqualFunc(a, b, slices.Equal[[]string])
}

func TestBoardPage(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	s := e.seedBoard()
	rec := e.getAs("alice", e.boardURL("general", ""), false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	id := idText(e.general.ID)
	requireBody(t, rec, []string{
		"<title id=\"page-title\">Board of general · Clowder</title>",
		// The sidebar, with the room highlighted.
		`<aside id="sidebar">`, `href="/rooms/` + id + `" class="current" data-room="general" aria-current="page"`,
		// The header, with the way back to the posts.
		`id="board-header"`, "<h1>general</h1>", `<a href="/rooms/` + id + `">Posts</a>`, `<a href="/rooms/` + id + `/board" aria-current="page">Board</a>`,
		// The columns in order with their counts and cards in order.
		`aria-label="To do"`, `aria-label="In progress"`, `aria-label="Done"`,
		`<span class="column-count" title="4 cards">4</span>`, `<span class="column-count" title="0 cards">0</span>`, `<span class="column-count" title="1 card">1</span>`,
		// A card: id, title, assignees, due date, link to its post and to the comment.
		`id="task-` + idText(s.late.ID) + `"`, `<p class="card-title">Late</p>`, `<p class="card-assignees" title="Assigned to">`, `<span class="card-assignee" title="Bob">`, `<span class="visually-hidden">Bob</span>`,
		`<time class="task-due overdue" datetime="` + pastDue + `" title="Overdue">` + pastDue + `</time>`,
		`<time class="task-due" datetime="` + futureDue + `" title="Due date">` + futureDue + `</time>`,
		`href="/posts/` + idText(s.root.ID) + `#post-` + idText(s.root.ID) + `"`,
		`href="/posts/` + idText(s.other.ID) + `#post-` + idText(s.comment.ID) + `"`,
		// The cards of a member can be dragged, moved by choice and opened.
		`draggable="true"`, `class="card-move"`, `class="card-details"`, `id="board-add-form"`, `class="column-rename"`,
		// The panel of the card form is outside the columns.
		`id="board-editor"`,
		// The live stream and the script.
		`id="live-stream"`, `/rooms/` + id + `/board/stream?v=`, `<script src="/static/board.js" defer>`,
		// The hidden form and link the script uses.
		`id="board-drop"`, `id="board-refresh"`,
	}, "Join this room to change the board")
	body := rec.Body.String()
	requireOrder(t, body, `aria-label="To do"`, `<p class="card-title">Late</p>`, `<p class="card-title">Soon</p>`, `<p class="card-title">Plain</p>`,
		`aria-label="In progress"`, `aria-label="Done"`, `<p class="card-title">Finished</p>`)
	// Only the overdue card is red: the card in the last column is not.
	if n := strings.Count(body, "task-due overdue"); n != 1 {
		t.Errorf("%d overdue dates, want 1", n)
	}
	finished := body[strings.Index(body, `id="task-`+idText(s.finished.ID)+`"`):]
	finished = finished[:strings.Index(finished, "</li>")]
	requireContains(t, finished, []string{"board-card board-card-done", `datetime="` + pastDue + `" title="Due date"`}, []string{"overdue"})
	// The card is not offered a move to the column it is in.
	todoCard := body[strings.Index(body, `id="task-`+idText(s.late.ID)+`"`):]
	todoCard = todoCard[:strings.Index(todoCard, "</li>")]
	requireContains(t, todoCard, []string{`<option value="">Move to…</option>`, `<option value="` + idText(s.doing) + `">In progress</option>`}, []string{`<option value="` + idText(s.todo) + `">`})
	// The board is also the target of the links of the tasks of a post.
	requireBody(t, e.getAs("alice", "/posts/"+idText(s.root.ID), false), []string{`href="/rooms/` + id + `/board#task-` + idText(s.late.ID) + `"`})
}

func TestBoardPageIsReadOnlyForWhoHasNotJoined(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	s := e.seedBoard()
	rec := e.getAs("carol", e.boardURL("general", ""), false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	requireBody(t, rec, []string{
		`<p class="hint join-note">Join this room to change the board</p>`,
		`<h2 class="column-name">To do</h2>`, `<p class="card-title">Late</p>`, `id="task-` + idText(s.soon.ID) + `"`,
		`href="/posts/` + idText(s.root.ID) + `#post-` + idText(s.root.ID) + `"`,
	}, "draggable", `class="card-move"`, `class="card-details"`, `class="column-rename"`, `id="board-add-form"`, `class="column-actions"`)
	// A member does not see the note.
	requireBody(t, e.getAs("bob", e.boardURL("general", ""), false), []string{`draggable="true"`}, "Join this room to change the board")
}

func TestBoardOfAPrivateRoom(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	// Bob is a member of secret and sees its board; the others do not know it.
	rec := e.getAs("bob", e.boardURL("secret", ""), false)
	requireBody(t, rec, []string{"<h1>secret</h1>", "badge-private", `aria-label="To do"`, `data-room="secret" aria-current="page"`})
	for _, user := range []string{"carol", "admin"} {
		modes(t, func(t *testing.T, datastar bool) {
			rec := e.getAs(user, e.boardURL("secret", ""), datastar)
			requireNotFound(t, rec, datastar, "/rooms/"+idText(e.secret.ID))
			requireBody(t, rec, nil, `aria-label="To do"`)
		})
	}
}

// TestBoardRoutesOfAPrivateRoomOfOthersAre404 is the rule of SPEC §9.1 for
// every route of the board: a person who is not a member of a private room does
// not know it, admins included (D29).
func TestBoardRoutesOfAPrivateRoomOfOthersAre404(t *testing.T) {
	t.Parallel()
	for _, user := range []string{"carol", "admin"} {
		modes(t, func(t *testing.T, datastar bool) {
			e := newRoomsEnv(t)
			view := e.boardOf("alice", "secret")
			col := view.Columns[0].ID
			wrote := e.writeTasks("alice", "secret", "Secret plan", app.TaskInput{Title: "Hidden task"})
			card := wrote.Tasks[0].ID
			base := e.boardURL("secret", "")
			roomPath := "/rooms/" + idText(e.secret.ID)
			posts := []struct {
				name string
				path string
				form url.Values
			}{
				{"move a card", base + "/tasks/" + idText(card) + "/move", url.Values{"column": {idText(view.Columns[1].ID)}}},
				{"update a card", base + "/tasks/" + idText(card), url.Values{"title": {"Stolen"}}},
				{"add a column", base + "/columns", url.Values{"name": {"Mine"}}},
				{"rename a column", base + "/columns/" + idText(col) + "/rename", url.Values{"name": {"Mine"}}},
				{"move a column", base + "/columns/" + idText(col) + "/move", url.Values{"position": {"1"}}},
				{"delete a column", base + "/columns/" + idText(col) + "/delete", nil},
			}
			for _, p := range posts {
				t.Run(user+" "+p.name, func(t *testing.T) {
					requireNotFound(t, e.post(user, p.path, p.form, datastar), datastar, roomPath)
				})
			}
			gets := []struct{ name, path string }{
				{"the page", base},
				{"the columns", base + "/columns"},
				{"the form of a card", base + "/tasks/" + idText(card) + "/edit"},
			}
			for _, g := range gets {
				t.Run(user+" "+g.name, func(t *testing.T) {
					requireNotFound(t, e.getAs(user, g.path, datastar), datastar, roomPath)
				})
			}
			// Nothing changed.
			if got := e.columnNames("secret"); !slices.Equal(got, []string{"To do", "In progress", "Done"}) {
				t.Errorf("columns = %v, want the three of a new board", got)
			}
			if got := e.layout("secret"); !sameLayout(got, [][]string{{"Hidden task"}, {}, {}}) {
				t.Errorf("layout = %v", got)
			}
		})
	}
}

func TestBoardRoutesOfRoomsThatDoNotExist(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	for _, id := range []string{"9999", "abc", "0"} {
		paths := []string{"", "/columns", "/stream", "/tasks/1/edit"}
		for _, p := range paths {
			rec := e.getAs("alice", "/rooms/"+id+"/board"+p, false)
			if rec.Code != http.StatusNotFound {
				t.Errorf("GET /rooms/%s/board%s: status = %d, want 404", id, p, rec.Code)
			}
		}
		for _, p := range []string{"/columns", "/columns/1/rename", "/columns/1/move", "/columns/1/delete", "/tasks/1", "/tasks/1/move"} {
			rec := e.post("alice", "/rooms/"+id+"/board"+p, url.Values{"name": {"x"}}, false)
			if rec.Code != http.StatusNotFound {
				t.Errorf("POST /rooms/%s/board%s: status = %d, want 404", id, p, rec.Code)
			}
		}
	}
}

func TestBoardRoutesNeedASession(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	base := e.boardURL("general", "")
	tests := []struct {
		name    string
		request *http.Request
	}{
		{"page", get(base)},
		{"columns", get(base + "/columns")},
		{"stream", get(base + "/stream")},
		{"form of a card", get(base + "/tasks/1/edit")},
		{"move a card", postForm(base+"/tasks/1/move", url.Values{"column": {"1"}})},
		{"update a card", postForm(base+"/tasks/1", url.Values{"title": {"x"}})},
		{"add a column", postForm(base+"/columns", url.Values{"name": {"x"}})},
		{"rename a column", postForm(base+"/columns/1/rename", url.Values{"name": {"x"}})},
		{"move a column", postForm(base+"/columns/1/move", url.Values{"position": {"1"}})},
		{"delete a column", postForm(base+"/columns/1/delete", nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.send(tt.request)
			if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
				t.Errorf("status = %d, Location = %q; want a redirect to the login page", rec.Code, rec.Header().Get("Location"))
			}
		})
	}
	if got := e.columnNames("general"); !slices.Equal(got, []string{"To do", "In progress", "Done"}) {
		t.Errorf("columns = %v after actions without a session", got)
	}
}

func TestBoardTaskMove(t *testing.T) {
	t.Parallel()
	// The seed's layout is To do: Late, Soon, Plain, Comment task; In
	// progress: -; Done: Finished.
	tests := []struct {
		name   string
		card   string
		column int // index of the column
		pos    string
		want   [][]string
	}{
		{"to another column", "Soon", 1, "0", [][]string{{"Late", "Plain", "Comment task"}, {"Soon"}, {"Finished"}}},
		{"to the middle of a column", "Plain", 2, "1", [][]string{{"Late", "Soon", "Comment task"}, {}, {"Finished", "Plain"}}},
		{"to the top of a column", "Plain", 2, "0", [][]string{{"Late", "Soon", "Comment task"}, {}, {"Plain", "Finished"}}},
		{"within a column, down", "Late", 0, "2", [][]string{{"Soon", "Plain", "Late", "Comment task"}, {}, {"Finished"}}},
		{"within a column, up", "Comment task", 0, "0", [][]string{{"Comment task", "Late", "Soon", "Plain"}, {}, {"Finished"}}},
		{"no position is the end of the column", "Late", 2, "", [][]string{{"Soon", "Plain", "Comment task"}, {}, {"Finished", "Late"}}},
		{"a position past the end is the end", "Late", 2, "99", [][]string{{"Soon", "Plain", "Comment task"}, {}, {"Finished", "Late"}}},
		{"a negative position is the top", "Late", 2, "-4", [][]string{{"Soon", "Plain", "Comment task"}, {}, {"Late", "Finished"}}},
		{"to where it is", "Late", 0, "0", [][]string{{"Late", "Soon", "Plain", "Comment task"}, {}, {"Finished"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modes(t, func(t *testing.T, datastar bool) {
				e := newRoomsEnv(t)
				s := e.seedBoard()
				cols := []uint64{s.todo, s.doing, s.done}
				card := e.cardTitled("general", tt.card)
				form := url.Values{"column": {idText(cols[tt.column])}}
				if tt.pos != "" {
					form.Set("position", tt.pos)
				}
				rec := e.post("bob", e.boardURL("general", "/tasks/"+idText(card.ID)+"/move"), form, datastar)
				if got := e.layout("general"); !sameLayout(got, tt.want) {
					t.Errorf("layout = %v, want %v", got, tt.want)
				}
				if !datastar {
					if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != e.boardURL("general", "#task-"+idText(card.ID)) {
						t.Errorf("answer = %d to %q, want 303 to the card on the board", rec.Code, rec.Header().Get("Location"))
					}
					return
				}
				// The answer holds the columns as the server has them now.
				requireSSE(t, rec)
				body := rec.Body.String()
				requireBody(t, rec, []string{`id="board-columns"`, `id="board-message"`}, `id="board-editor"`, `class="form-error"`)
				var order []string
				for _, col := range tt.want {
					for _, title := range col {
						order = append(order, `<p class="card-title">`+title+`</p>`)
					}
				}
				requireOrder(t, body, order...)
				// The count of the columns is right too.
				requireContains(t, body, []string{`title="` + cardCountText(len(tt.want[0])) + `"`}, nil)
			})
		})
	}
}

// cardCountText is the title of the count of a column.
func cardCountText(n int) string {
	if n == 1 {
		return "1 card"
	}
	return idText(uint64(n)) + " cards"
}

func TestBoardTaskMoveByTheSelect(t *testing.T) {
	t.Parallel()
	// The "Move to…" choice posts only the column: the card goes to its end.
	modes(t, func(t *testing.T, datastar bool) {
		e := newRoomsEnv(t)
		s := e.seedBoard()
		rec := e.post("alice", e.boardURL("general", "/tasks/"+idText(s.late.ID)+"/move"), url.Values{"column": {idText(s.done)}}, datastar)
		want := [][]string{{"Soon", "Plain", "Comment task"}, {}, {"Finished", "Late"}}
		if got := e.layout("general"); !sameLayout(got, want) {
			t.Errorf("layout = %v, want %v", got, want)
		}
		if datastar {
			requireSSE(t, rec)
			// In the last column the card is no longer overdue.
			requireBody(t, rec, []string{"board-card board-card-done"})
		}
	})
}

func TestBoardTaskMoveFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		user       string
		path       func(e *roomsEnv, s boardSeed) string
		form       func(e *roomsEnv, s boardSeed) url.Values
		wantStatus int // of a plain post
		wantText   string
	}{
		{
			name: "no column", user: "alice",
			path: func(e *roomsEnv, s boardSeed) string {
				return e.boardURL("general", "/tasks/"+idText(s.late.ID)+"/move")
			},
			form:       func(*roomsEnv, boardSeed) url.Values { return url.Values{"position": {"0"}} },
			wantStatus: http.StatusUnprocessableEntity, wantText: messageChooseColumn,
		},
		{
			name: "a position that is not a number", user: "alice",
			path: func(e *roomsEnv, s boardSeed) string {
				return e.boardURL("general", "/tasks/"+idText(s.late.ID)+"/move")
			},
			form: func(e *roomsEnv, s boardSeed) url.Values {
				return url.Values{"column": {idText(s.doing)}, "position": {"top"}}
			},
			wantStatus: http.StatusUnprocessableEntity, wantText: messageInvalid,
		},
		{
			name: "a person who has not joined", user: "carol",
			path: func(e *roomsEnv, s boardSeed) string {
				return e.boardURL("general", "/tasks/"+idText(s.late.ID)+"/move")
			},
			form: func(e *roomsEnv, s boardSeed) url.Values {
				return url.Values{"column": {idText(s.doing)}, "position": {"0"}}
			},
			wantStatus: http.StatusForbidden, wantText: messageForbidden,
		},
		{
			name: "a column of another room", user: "alice",
			path: func(e *roomsEnv, s boardSeed) string {
				return e.boardURL("general", "/tasks/"+idText(s.late.ID)+"/move")
			},
			form: func(e *roomsEnv, s boardSeed) url.Values {
				return url.Values{"column": {idText(e.boardOf("alice", "secret").Columns[0].ID)}}
			},
			wantStatus: http.StatusNotFound, wantText: messageColumnGone,
		},
		{
			name: "a column that does not exist", user: "alice",
			path: func(e *roomsEnv, s boardSeed) string {
				return e.boardURL("general", "/tasks/"+idText(s.late.ID)+"/move")
			},
			form:       func(*roomsEnv, boardSeed) url.Values { return url.Values{"column": {"9999"}} },
			wantStatus: http.StatusNotFound, wantText: messageColumnGone,
		},
		{
			name: "a card of another room", user: "alice",
			path: func(e *roomsEnv, s boardSeed) string {
				hidden := e.writeTasks("alice", "secret", "x", app.TaskInput{Title: "Elsewhere"})
				return e.boardURL("general", "/tasks/"+idText(hidden.Tasks[0].ID)+"/move")
			},
			form:       func(e *roomsEnv, s boardSeed) url.Values { return url.Values{"column": {idText(s.doing)}} },
			wantStatus: http.StatusNotFound, wantText: messageCardGone,
		},
		{
			name: "a card that does not exist", user: "alice",
			path:       func(e *roomsEnv, s boardSeed) string { return e.boardURL("general", "/tasks/9999/move") },
			form:       func(e *roomsEnv, s boardSeed) url.Values { return url.Values{"column": {idText(s.doing)}} },
			wantStatus: http.StatusNotFound, wantText: messageCardGone,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modes(t, func(t *testing.T, datastar bool) {
				e := newRoomsEnv(t)
				s := e.seedBoard()
				before := e.layout("general")
				rec := e.post(tt.user, tt.path(e, s), tt.form(e, s), datastar)
				if datastar {
					// 200, the message at the top of the board, nothing moved.
					requireSSE(t, rec)
					requireBody(t, rec, []string{`id="board-message"`, tt.wantText})
				} else {
					if rec.Code != tt.wantStatus {
						t.Fatalf("status = %d, want %d; body:\n%s", rec.Code, tt.wantStatus, rec.Body)
					}
					if tt.wantStatus == http.StatusNotFound {
						requireBody(t, rec, []string{"Page not found"})
					} else {
						requireBody(t, rec, []string{`id="board-message"`, tt.wantText, `aria-label="To do"`})
					}
				}
				if got := e.layout("general"); !sameLayout(got, before) {
					t.Errorf("layout changed to %v from %v", got, before)
				}
			})
		})
	}
}

func TestBoardColumnsRefresh(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	e.seedBoard()
	rec := e.getAs("alice", e.boardURL("general", "/columns"), true)
	requireSSE(t, rec)
	requireBody(t, rec, []string{`id="board-columns"`, `<p class="card-title">Late</p>`}, `id="board-note"`)
	// A plain request is sent to the page.
	rec = e.getAs("alice", e.boardURL("general", "/columns"), false)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != e.boardURL("general", "") {
		t.Errorf("answer = %d to %q, want 303 to the board", rec.Code, rec.Header().Get("Location"))
	}
}

// appTask is a task row with only a title.
func appTask(title string) app.TaskInput { return app.TaskInput{Title: title} }

// appRoomInput is a room input with only a name.
func appRoomInput(name string) app.RoomInput {
	return app.RoomInput{Name: name, Visibility: app.VisibilityVisible}
}
