package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/errors"
)

// Due dates of the tests: the fake clock starts on 2026-10-26.
const (
	pastDue   = "2026-10-20"
	futureDue = "2026-11-05"
)

// taskRowOf is a task row of the editor form.
type taskRowOf struct {
	id        string
	title     string
	assignees []string
	due       string
}

// editorTasksForm is the form of the editor with the task rows at the indexes
// given in order (the first row gets index 0).
func editorTasksForm(body string, rows ...taskRowOf) url.Values {
	form := editorForm("text", body, "")
	for i, r := range rows {
		prefix := "tasks[" + idText(uint64(i)) + "]"
		form.Set(prefix+"[id]", r.id)
		form.Set(prefix+"[title]", r.title)
		form.Set(prefix+"[due]", r.due)
		for _, a := range r.assignees {
			form.Add(prefix+"[assignees]", a)
		}
	}
	return form
}

// encodings runs fn for a urlencoded form and for a multipart one, each by
// Datastar and plain.
func encodings(t *testing.T, fn func(t *testing.T, e *roomsEnv, send func(user, path string, form url.Values) *httptest.ResponseRecorder, datastar bool)) {
	t.Helper()
	for _, multipart := range []bool{false, true} {
		name := "urlencoded"
		if multipart {
			name = "multipart"
		}
		t.Run(name, func(t *testing.T) {
			modes(t, func(t *testing.T, datastar bool) {
				e := newRoomsEnv(t)
				send := func(user, path string, form url.Values) *httptest.ResponseRecorder {
					if multipart {
						return e.postFiles(user, path, form, nil, datastar)
					}
					return e.post(user, path, form, datastar)
				}
				fn(t, e, send, datastar)
			})
		})
	}
}

// titlesOf returns the titles of the tasks of the post.
func titlesOf(p app.Post) []string {
	out := []string{}
	for _, t := range p.Tasks {
		out = append(out, t.Title)
	}
	return out
}

// assigneeNamesOf returns the usernames of the assignees of a task.
func assigneeNamesOf(t app.TaskCard) []string {
	out := []string{}
	for _, a := range t.Assignees {
		out = append(out, a.Username)
	}
	return out
}

func TestTaskCreate(t *testing.T) {
	t.Parallel()
	encodings(t, func(t *testing.T, e *roomsEnv, send func(string, string, url.Values) *httptest.ResponseRecorder, datastar bool) {
		form := editorTasksForm("Plan the week",
			taskRowOf{id: "0", title: "  Book the room ", assignees: []string{idText(e.bob.ID)}, due: futureDue},
			taskRowOf{id: "0", title: "Send the agenda"},
		)
		// A row left empty, between and after the others, is ignored.
		form.Set("tasks[2][id]", "0")
		form.Set("tasks[2][title]", "  ")
		form.Set("tasks[2][due]", "")
		rec := send("alice", "/rooms/"+idText(e.general.ID)+"/posts", form)
		posts := e.feed("alice", "general")
		if len(posts) != 1 {
			t.Fatalf("%d posts after the create, want 1; answer:\n%s", len(posts), rec.Body)
		}
		p := posts[0]
		if got := titlesOf(p); !slices.Equal(got, []string{"Book the room", "Send the agenda"}) {
			t.Fatalf("tasks = %q", got)
		}
		first := p.Tasks[0]
		if got := assigneeNamesOf(first); !slices.Equal(got, []string{"bob"}) {
			t.Errorf("assignees of the first task = %q, want [bob]", got)
		}
		if first.DueDate == nil || first.DueDate.Format("2006-01-02") != futureDue {
			t.Errorf("due date of the first task = %v, want %s", first.DueDate, futureDue)
		}
		if p.Tasks[1].DueDate != nil || len(p.Tasks[1].Assignees) != 0 {
			t.Errorf("second task = %+v, want no assignee and no due date", p.Tasks[1])
		}
		if !datastar {
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("answer = %d, want 303", rec.Code)
			}
			return
		}
		requireSSE(t, rec)
		requireBody(t, rec, []string{
			"data: selector #feed-posts", `class="post-tasks"`, "Book the room", "Send the agenda",
			`class="task-column"`, ">To do<", "Bob", futureDue, `href="/rooms/` + idText(e.general.ID) + `/board#task-` + idText(first.ID) + `"`,
			// The fresh editor has no rows and offers the members.
			"data: selector #post-editor-new", `class="task-rows"`, `data-next="0"`,
		}, "overdue")
	})
}

func TestTaskCreateWithTasksOnly(t *testing.T) {
	t.Parallel()
	encodings(t, func(t *testing.T, e *roomsEnv, send func(string, string, url.Values) *httptest.ResponseRecorder, datastar bool) {
		send("alice", "/rooms/"+idText(e.general.ID)+"/posts", editorTasksForm("", taskRowOf{id: "0", title: "Only a task"}))
		posts := e.feed("alice", "general")
		if len(posts) != 1 || posts[0].Body != "" || !slices.Equal(titlesOf(posts[0]), []string{"Only a task"}) {
			t.Fatalf("posts = %+v, want one with no body and the task", posts)
		}
	})
}

func TestTaskCreateMistakes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		rows func(e *roomsEnv) []taskRowOf
		want []string // the message and the input kept
	}{
		{
			name: "the second task has no title",
			rows: func(e *roomsEnv) []taskRowOf {
				return []taskRowOf{
					{id: "0", title: "Fine one", due: futureDue},
					{id: "0", title: " ", assignees: []string{idText(e.bob.ID)}, due: pastDue},
				}
			},
			want: []string{
				"Task 2: write a title of 1 to 200 characters.", `id="post-tasks-new-error"`,
				// Both rows are kept as typed: titles, due dates and the choice.
				`value="Fine one"`, `value="` + futureDue + `"`, `value="` + pastDue + `"`, `name="tasks[1][title]"`,
			},
		},
		{
			name: "an assignee who is not a member",
			rows: func(e *roomsEnv) []taskRowOf {
				return []taskRowOf{{id: "0", title: "Give it to carol", assignees: []string{idText(e.carol.ID)}}}
			},
			want: []string{"Task 1: assign only members of the room.", `value="Give it to carol"`},
		},
		{
			name: "a due date that is not a date",
			rows: func(*roomsEnv) []taskRowOf {
				return []taskRowOf{{id: "0", title: "Some time", due: "next week"}}
			},
			want: []string{"Task 1: write the due date as YYYY-MM-DD.", `value="Some time"`, `value="next week"`},
		},
		{
			name: "a task that is not part of the post",
			rows: func(*roomsEnv) []taskRowOf {
				return []taskRowOf{{id: "999", title: "Made up"}}
			},
			want: []string{"Task 1: this task is not part of the post.", `value="Made up"`},
		},
		{
			name: "an ID that is not a number",
			rows: func(*roomsEnv) []taskRowOf {
				return []taskRowOf{{id: "x", title: "Strange"}}
			},
			want: []string{"Task 1: this task is not part of the post.", `value="Strange"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encodings(t, func(t *testing.T, e *roomsEnv, send func(string, string, url.Values) *httptest.ResponseRecorder, datastar bool) {
				form := editorTasksForm("Kept body", tt.rows(e)...)
				rec := send("alice", "/rooms/"+idText(e.general.ID)+"/posts", form)
				want := append([]string{`id="post-editor-new"`, ">Kept body</textarea>", `class="field-error"`}, tt.want...)
				if datastar {
					requireSSE(t, rec)
					requireBody(t, rec, want, "data: mode prepend")
				} else {
					if rec.Code != http.StatusUnprocessableEntity {
						t.Fatalf("status = %d, want 422; body:\n%s", rec.Code, rec.Body)
					}
					requireBody(t, rec, append(want, `id="room-header"`))
				}
				// The error comes above the rows.
				body := rec.Body.String()
				if at, rows := strings.Index(body, `id="post-tasks-new-error"`), strings.Index(body, `class="task-rows"`); at < 0 || rows < at {
					t.Errorf("the tasks error (%d) is not above the rows (%d)", at, rows)
				}
				if n := len(e.feed("alice", "general")); n != 0 {
					t.Errorf("%d posts after a refused create", n)
				}
			})
		})
	}
}

func TestTaskInputsFromRoutes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		form  url.Values
		route string
		field string
	}{
		{
			name: "a due date that is not a date", form: editorTasksForm("x", taskRowOf{id: "0", title: "t", due: "31/12/2026"}),
			route: "taskInputsFrom.InvalidInput", field: "Task 1: write the due date as YYYY-MM-DD.",
		},
		{
			name: "an ID that is not a number", form: editorTasksForm("x", taskRowOf{id: "abc", title: "t"}),
			route: "taskInputsFrom.InvalidInput", field: "Task 1: this task is not part of the post.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := postForm("/rooms/1/posts", tt.form)
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			_, err := taskInputsFrom(r)
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T", err)
			}
			if !appErr.HasRoute(tt.route) {
				t.Errorf("unexpected failure path: %s", appErr.Route())
			}
			if got := formErrorsOf(err).Fields[app.FieldTasks]; got != tt.field {
				t.Errorf("message = %q, want %q", got, tt.field)
			}
		})
	}
}

func TestTaskInputsSkipBlankRowsAndOrderByIndex(t *testing.T) {
	t.Parallel()
	form := url.Values{
		"tasks[10][title]": {"last"}, "tasks[10][id]": {"0"},
		"tasks[2][title]": {"first"}, "tasks[2][assignees]": {"3", "4"}, "tasks[2][due]": {futureDue},
		"tasks[5][title]": {""}, "tasks[5][id]": {"0"}, "tasks[5][due]": {""},
		"tasks[7][title]": {""}, "tasks[7][id]": {"12"},
		"tasksfoo": {"x"}, "tasks[1][nope]": {"x"},
	}
	r := postForm("/rooms/1/posts", form)
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	got, err := taskInputsFrom(r)
	if err != nil {
		t.Fatalf("taskInputsFrom: %+v", err)
	}
	if len(got) != 3 || got[0].Title != "first" || got[1].ID != 12 || got[2].Title != "last" {
		t.Fatalf("tasks = %+v, want first, the row of task 12 (never blank), last", got)
	}
	if !slices.Equal(got[0].Assignees, []uint64{3, 4}) || got[0].DueDate == nil {
		t.Errorf("first = %+v, want assignees 3 and 4 and a due date", got[0])
	}
}

// writeTasks makes a post of the user in the room with the tasks.
func (e *roomsEnv) writeTasks(user, room, body string, tasks ...app.TaskInput) app.Post {
	e.t.Helper()
	p, err := e.posts.Create(e.t.Context(), e.actor(e.users(user)), e.room(room).ID,
		app.PostInput{Format: app.FormatText, Body: body, Tasks: tasks})
	if err != nil {
		e.t.Fatalf("Create %q: %+v", body, err)
	}
	return p
}

func TestTaskEditForm(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	due := mustDay(t, futureDue)
	p := e.writeTasks("alice", "general", "With tasks",
		app.TaskInput{Title: "First", Assignees: []uint64{e.bob.ID}, DueDate: &due},
		app.TaskInput{Title: "Second"},
	)
	for _, datastar := range []bool{true, false} {
		rec := e.getAs("alice", postPathOf(p, "edit"), datastar)
		if datastar {
			requireSSE(t, rec)
		} else if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		requireBody(t, rec, []string{
			`id="post-task-rows-edit-` + idText(p.ID) + `"`, `data-next="2"`,
			`name="tasks[0][id]" value="` + idText(p.Tasks[0].ID) + `"`, `name="tasks[1][id]" value="` + idText(p.Tasks[1].ID) + `"`,
			`name="tasks[0][title]" type="text" value="First"`, `name="tasks[1][title]" type="text" value="Second"`,
			`name="tasks[0][due]" type="date" value="` + futureDue + `"`,
			// The members of the room are the options; bob is selected on the first task only.
			`<option value="` + idText(e.alice.ID) + `">Alice (@alice)</option>`,
			`<option value="` + idText(e.bob.ID) + `" selected>Bob (@bob)</option>`,
			"Add task", "/editor/task-row?editor=edit-" + idText(p.ID) + "&amp;room=" + idText(e.general.ID),
			`<option value="` + idText(e.admin.ID) + `">Admin (@admin)</option>`,
		}, "Carol")
	}
}

// mustDay reads a due date written YYYY-MM-DD.
func mustDay(t *testing.T, s string) time.Time {
	t.Helper()
	day, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatal(err)
	}
	return day
}

func TestTaskEdit(t *testing.T) {
	t.Parallel()
	encodings(t, func(t *testing.T, e *roomsEnv, send func(string, string, url.Values) *httptest.ResponseRecorder, datastar bool) {
		due := mustDay(t, futureDue)
		p := e.writeTasks("alice", "general", "With tasks",
			app.TaskInput{Title: "Keep me", Assignees: []uint64{e.bob.ID}, DueDate: &due},
			app.TaskInput{Title: "Rename me"},
			app.TaskInput{Title: "Remove me"},
		)
		keep, rename, remove := p.Tasks[0], p.Tasks[1], p.Tasks[2]
		form := editorTasksForm("With tasks",
			taskRowOf{id: idText(keep.ID), title: "Keep me", assignees: []string{idText(e.bob.ID)}, due: futureDue},
			taskRowOf{id: idText(rename.ID), title: "Renamed", assignees: []string{idText(e.alice.ID)}, due: pastDue},
			taskRowOf{id: "0", title: "Brand new"},
		)
		rec := send("alice", postPathOf(p, "edit"), form)
		if !datastar {
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("answer = %d, want 303; body:\n%s", rec.Code, rec.Body)
			}
		} else {
			requireSSE(t, rec)
		}
		got := e.getPost("alice", p.ID)
		if titles := titlesOf(got); !slices.Equal(titles, []string{"Keep me", "Renamed", "Brand new"}) {
			t.Fatalf("tasks after the edit = %q", titles)
		}
		if got.Tasks[0].ID != keep.ID || got.Tasks[1].ID != rename.ID {
			t.Errorf("task IDs = %d, %d; want the same %d, %d", got.Tasks[0].ID, got.Tasks[1].ID, keep.ID, rename.ID)
		}
		if names := assigneeNamesOf(got.Tasks[1]); !slices.Equal(names, []string{"alice"}) || got.Tasks[1].DueDate == nil || got.Tasks[1].DueDate.Format("2006-01-02") != pastDue {
			t.Errorf("renamed task = %+v, want alice and %s", got.Tasks[1], pastDue)
		}
		if got.Tasks[2].ID == remove.ID || got.Tasks[2].ID == 0 {
			t.Errorf("the new task has ID %d", got.Tasks[2].ID)
		}
		if datastar {
			requireBody(t, rec, []string{`id="post-content-` + idText(p.ID) + `"`, "Renamed", "Brand new"}, "Remove me", "post-editor")
		}
	})
}

func TestTaskEditRemovesWhatTheFormLeavesOut(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	p := e.writeTasks("alice", "general", "With tasks", app.TaskInput{Title: "One"}, app.TaskInput{Title: "Two"})
	rec := e.post("alice", postPathOf(p, "edit"), editorTasksForm("With tasks"), true)
	requireSSE(t, rec)
	if got := e.getPost("alice", p.ID).Tasks; len(got) != 0 {
		t.Errorf("tasks = %+v, want none after an edit with no rows", got)
	}
}

func TestTaskEditMistakesKeepTheRows(t *testing.T) {
	t.Parallel()
	for _, datastar := range []bool{true, false} {
		e := newRoomsEnv(t)
		p := e.writeTasks("alice", "general", "With tasks", app.TaskInput{Title: "One"}, app.TaskInput{Title: "Two"})
		form := editorTasksForm("With tasks",
			taskRowOf{id: idText(p.Tasks[0].ID), title: "One edited"},
			taskRowOf{id: idText(p.Tasks[1].ID), title: ""},
			taskRowOf{id: "0", title: "Added", due: futureDue},
		)
		rec := e.post("alice", postPathOf(p, "edit"), form, datastar)
		want := []string{
			"Task 2: write a title of 1 to 200 characters.", `id="post-editor-edit-` + idText(p.ID) + `"`,
			`name="tasks[0][id]" value="` + idText(p.Tasks[0].ID) + `"`, `value="One edited"`,
			`name="tasks[1][id]" value="` + idText(p.Tasks[1].ID) + `"`, `name="tasks[2][id]" value="0"`, `value="Added"`, `value="` + futureDue + `"`,
		}
		if datastar {
			requireSSE(t, rec)
		} else if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", rec.Code)
		}
		requireBody(t, rec, want)
		if got := titlesOf(e.getPost("alice", p.ID)); !slices.Equal(got, []string{"One", "Two"}) {
			t.Errorf("tasks after a refused edit = %q, want them unchanged", got)
		}
	}
}

func TestTaskFormerMembersStayAssignable(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	p := e.writeTasks("alice", "general", "Bob's work", app.TaskInput{Title: "Bob's task", Assignees: []uint64{e.bob.ID}})
	if err := e.rooms.Leave(t.Context(), e.actor(e.bob), e.general.ID); err != nil {
		t.Fatalf("Leave: %+v", err)
	}
	bobOption := `<option value="` + idText(e.bob.ID) + `" selected>Bob (@bob, left the room)</option>`

	// The edit form still offers bob, selected, marked as having left.
	requireBody(t, e.getAs("alice", postPathOf(p, "edit"), true), []string{bobOption})

	// Saving keeps him on the task...
	form := editorTasksForm("Bob's work", taskRowOf{id: idText(p.Tasks[0].ID), title: "Bob's task, renamed", assignees: []string{idText(e.bob.ID)}})
	rec := e.post("alice", postPathOf(p, "edit"), form, true)
	requireSSE(t, rec)
	got := e.getPost("alice", p.ID)
	if len(got.Tasks) != 1 || got.Tasks[0].Title != "Bob's task, renamed" || !slices.Equal(assigneeNamesOf(got.Tasks[0]), []string{"bob"}) {
		t.Fatalf("tasks = %+v, want the renamed task still assigned to bob", got.Tasks)
	}

	// ...but a new task cannot be given to him, and the form keeps him selected.
	form = editorTasksForm("Bob's work",
		taskRowOf{id: idText(p.Tasks[0].ID), title: "Bob's task, renamed", assignees: []string{idText(e.bob.ID)}},
		taskRowOf{id: "0", title: "New for bob", assignees: []string{idText(e.bob.ID)}},
	)
	rec = e.post("alice", postPathOf(p, "edit"), form, true)
	requireSSE(t, rec)
	requireBody(t, rec, []string{"Task 2: assign only members of the room.", bobOption})
}

func TestTaskRowsInEditorsOfTheRoomAndTheReply(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	root := e.writePost("alice", "general", "A post")
	room := e.getAs("alice", "/rooms/"+idText(e.general.ID), false)
	reply := e.getAs("bob", "/posts/"+idText(root.ID)+"/reply", true)
	for name, rec := range map[string]*httptest.ResponseRecorder{"room": room, "reply": reply} {
		t.Run(name, func(t *testing.T) {
			requireBody(t, rec, []string{
				`<legend>Tasks</legend>`, `class="task-rows"`, `data-next="0"`, ">Add task</button>",
				"/editor/task-row?editor=", "&amp;room=" + idText(e.general.ID) + "&amp;index=",
			}, `class="task-row"`)
		})
	}
}

func TestTaskRowRoute(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	path := func(room uint64, editor string, index string) string {
		return "/editor/task-row?editor=" + editor + "&index=" + index + "&room=" + idText(room)
	}
	t.Run("a row for the member", func(t *testing.T) {
		rec := e.getAs("alice", path(e.general.ID, "edit-7", "3"), true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{
			"data: selector #post-task-rows-edit-7", "data: mode append", `<li class="task-row">`,
			`name="tasks[3][id]" value="0"`, `name="tasks[3][title]"`, `name="tasks[3][assignees]"`, `name="tasks[3][due]" type="date"`,
			`id="post-task-edit-7-3-title"`, `<option value="` + idText(e.bob.ID) + `">Bob (@bob)</option>`, "Remove task",
		}, "selected", "Carol")
	})
	t.Run("an editor id that is not one is the new post's", func(t *testing.T) {
		rec := e.getAs("alice", path(e.general.ID, "x%20y", "0"), true)
		requireBody(t, rec, []string{"data: selector #post-task-rows-new"})
	})
	t.Run("a wrong index is the first", func(t *testing.T) {
		rec := e.getAs("alice", path(e.general.ID, "new", "abc"), true)
		requireBody(t, rec, []string{`name="tasks[0][title]"`})
	})
	t.Run("a visible room that the person has not joined", func(t *testing.T) {
		rec := e.getAs("carol", path(e.general.ID, "new", "0"), true)
		requireSSE(t, rec)
		requireBody(t, rec, []string{`name="tasks[0][title]"`})
	})
	t.Run("a private room of others is not found", func(t *testing.T) {
		rec := e.getAs("carol", path(e.secret.ID, "new", "0"), true)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
		requireBody(t, rec, nil, "Bob", "Alice")
	})
	t.Run("no login", func(t *testing.T) {
		rec := e.send(get(path(e.general.ID, "new", "0"), asDatastar()))
		requireBody(t, rec, []string{redirectScript("/login")}, "task-row")
	})
}

func TestPostShowsItsTasks(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	past, future := mustDay(t, pastDue), mustDay(t, futureDue)
	p := e.writeTasks("alice", "general", "Look at these",
		app.TaskInput{Title: "Late one", Assignees: []uint64{e.alice.ID, e.bob.ID}, DueDate: &past},
		app.TaskInput{Title: "Later one", DueDate: &future},
		app.TaskInput{Title: "Finished late", DueDate: &past},
		app.TaskInput{Title: "No date"},
	)
	// The last column counts as done: a card there is never overdue.
	board, err := e.board.Board(t.Context(), e.actor(e.alice), e.general.ID)
	if err != nil {
		t.Fatalf("Board: %+v", err)
	}
	last := board.Columns[len(board.Columns)-1]
	if err := e.board.MoveTask(t.Context(), e.actor(e.alice), p.Tasks[2].ID, last.ID, 0); err != nil {
		t.Fatalf("MoveTask: %+v", err)
	}
	if err := e.board.MoveTask(t.Context(), e.actor(e.alice), p.Tasks[1].ID, board.Columns[1].ID, 0); err != nil {
		t.Fatalf("MoveTask: %+v", err)
	}
	c := e.commentOn(p, "bob", "a comment")
	_ = c

	feed := e.getAs("bob", "/rooms/"+idText(e.general.ID), false)
	page := e.getAs("bob", "/posts/"+idText(p.ID), false)
	for name, rec := range map[string]*httptest.ResponseRecorder{"feed": feed, "post page": page} {
		t.Run(name, func(t *testing.T) {
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			content := article(t, rec.Body.String(), p)
			requireOrder(t, content, `class="post-body"`, `class="post-tasks"`, "Late one", "Later one", "Finished late", "No date")
			for _, want := range []string{
				// Under the body, in the content of the post.
				`<span class="task-title">Late one</span>`, `<span class="task-column">To do</span>`,
				`<span class="task-assignees" title="Assigned to">Alice, Bob</span>`,
				// Overdue in red; a date in the future and one in the last column are not.
				`<time class="task-due overdue" datetime="` + pastDue + `" title="Overdue">` + pastDue + `</time>`,
				`<time class="task-due" datetime="` + futureDue + `" title="Due date">` + futureDue + `</time>`,
				`<span class="task-column">In progress</span>`,
				`<span class="task-column task-column-done">Done</span>`,
				`<li class="post-task post-task-done"><svg class="icon"`, `<span class="task-title">Finished late</span>`,
				`<time class="task-due" datetime="` + pastDue + `" title="Due date">`,
				`href="/rooms/` + idText(e.general.ID) + `/board#task-` + idText(p.Tasks[0].ID) + `"`,
			} {
				if !strings.Contains(content, want) {
					t.Errorf("the post does not contain %q:\n%s", want, content)
				}
			}
			if strings.Count(content, `class="task-due overdue"`) != 1 {
				t.Errorf("want exactly one overdue task:\n%s", content)
			}
			// A task without assignees and one without a date show neither.
			if strings.Count(content, `class="task-assignees"`) != 1 {
				t.Errorf("want one task with assignees:\n%s", content)
			}
		})
	}
	// Time passes: the task due in the future is overdue now.
	e.clk.Advance(11 * 24 * time.Hour)
	rec := e.getAs("bob", "/posts/"+idText(p.ID), false)
	if got := strings.Count(rec.Body.String(), `class="task-due overdue"`); got != 2 {
		t.Errorf("%d overdue tasks a month later, want 2 (the one in the last column stays done)", got)
	}
}

func TestCommentShowsItsTasks(t *testing.T) {
	t.Parallel()
	encodings(t, func(t *testing.T, e *roomsEnv, send func(string, string, url.Values) *httptest.ResponseRecorder, datastar bool) {
		root := e.writePost("alice", "general", "Root")
		form := editorTasksForm("", taskRowOf{id: "0", title: "Comment task", assignees: []string{idText(e.bob.ID)}, due: pastDue})
		rec := send("bob", "/posts/"+idText(root.ID)+"/replies", form)
		if !datastar && rec.Code != http.StatusSeeOther {
			t.Fatalf("answer = %d, want 303; body:\n%s", rec.Code, rec.Body)
		}
		page := e.getAs("alice", "/posts/"+idText(root.ID), false)
		requireBody(t, page, []string{"Comment task", `class="task-due overdue"`, "Bob", ">To do<"})
		if datastar {
			requireSSE(t, rec)
			requireBody(t, rec, []string{"Comment task", `class="post-tasks"`})
		}
	})
}

func TestReplyMistakesKeepTheTaskRows(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	root := e.writePost("alice", "general", "Root")
	form := editorTasksForm("", taskRowOf{id: "0", title: "Mine", assignees: []string{idText(e.carol.ID)}})
	for _, datastar := range []bool{true, false} {
		rec := e.post("bob", "/posts/"+idText(root.ID)+"/replies", form, datastar)
		requireBody(t, rec, []string{
			"Task 1: assign only members of the room.", `value="Mine"`, `id="post-editor-reply-` + idText(root.ID) + `"`,
			`<option value="` + idText(e.bob.ID) + `">Bob (@bob)</option>`,
		})
	}
}
