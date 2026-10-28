package views_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
)

// roomMembers are the members of the room of the view tests.
var roomMembers = []app.Member{
	{UserID: 1, Username: "alice", DisplayName: "Alice <A>"},
	{UserID: 2, Username: "bob", DisplayName: "Bob"},
}

func TestTaskRowsOfTheEditor(t *testing.T) {
	due := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	edited := app.Post{
		ID: 12, RoomID: 3, Format: app.FormatText, Body: "b",
		Tasks: []app.TaskCard{
			{ID: 31, Title: "Write <it>", Assignees: []app.UserRef{{ID: 2, Username: "bob", DisplayName: "Bob"}, {ID: 9, Username: "gone", DisplayName: "Gus"}}, DueDate: &due},
			{ID: 32, Title: "No extras"},
		},
	}
	tests := []struct {
		name    string
		data    views.PostEditorData
		want    []string
		notWant []string
	}{
		{
			name: "a new editor has the section and no rows",
			data: views.NewPostEditorData(7).WithMembers(7, roomMembers),
			want: []string{
				`<fieldset class="tasks-field"><legend>Tasks</legend>`,
				`<ul id="post-task-rows-new" class="task-rows" data-next="0"></ul>`,
				`data-on:click="const c = document.getElementById(&#39;post-task-rows-new&#39;); const i = c.dataset.next; c.dataset.next = Number(i) + 1; @get(&#39;/editor/task-row?editor=new&amp;room=7&amp;index=&#39; + i)">Add task</button>`,
			},
			notWant: []string{`class="task-row"`, `id="post-tasks-new-error"`},
		},
		{
			name: "a reply editor learns its room from WithMembers",
			data: views.ReplyEditorData(4).WithMembers(8, roomMembers),
			want: []string{"/editor/task-row?editor=reply-4&amp;room=8&amp;index="},
		},
		{
			name: "an edit fills the rows with IDs, titles, assignees and due dates",
			data: views.EditPostEditorData(edited, "").WithMembers(3, roomMembers),
			want: []string{
				`data-next="2"`,
				`<li class="task-row"><input type="hidden" name="tasks[0][id]" value="31">`,
				`id="post-task-edit-12-0-title" name="tasks[0][title]" type="text" value="Write &lt;it&gt;" maxlength="200"`,
				`<input type="hidden" name="tasks[1][id]" value="32">`, `name="tasks[1][title]" type="text" value="No extras"`,
				`name="tasks[0][due]" type="date" value="2026-11-05"`, `name="tasks[1][due]" type="date" value=""`,
				`<select id="post-task-edit-12-0-assignees" name="tasks[0][assignees]" multiple size="3">`,
				`<option value="1">Alice &lt;A&gt; (@alice)</option>`,
				// Bob is selected on the first task only.
				`<option value="2" selected>Bob (@bob)</option><option value="9" selected>`,
				`<option value="2">Bob (@bob)</option><option value="9">`,
				`<label class="task-label" for="post-task-edit-12-1-title">Task</label>`,
				`data-on:click="el.closest(&#39;.task-row&#39;).remove()">Remove task</button>`,
			},
		},
		{
			name: "a former member stays in the picker, selected and marked",
			data: views.EditPostEditorData(edited, "").WithMembers(3, roomMembers),
			want: []string{`<option value="9" selected>Gus (@gone, left the room)</option>`},
		},
		{
			name:    "a former member is not offered by an editor that has no task of theirs",
			data:    views.NewPostEditorData(3).WithMembers(3, roomMembers),
			notWant: []string{"left the room"},
		},
		{
			name: "the rows typed in a failed submit are kept, with the message above them",
			data: func() views.PostEditorData {
				d := views.NewPostEditorData(7).WithMembers(7, roomMembers)
				d.Tasks = []views.TaskRowData{{Title: "kept", Assignees: []uint64{1, 2}, Due: "next week"}, {ID: 4, Title: ""}}
				d.Errors = views.FormErrors{Fields: map[string]string{"tasks": "Task 2: write a title of 1 to 200 characters."}}
				return d
			}(),
			want: []string{
				`<p class="field-error" id="post-tasks-new-error" role="alert">Task 2: write a title of 1 to 200 characters.</p>`,
				`value="kept"`, `<option value="1" selected>`, `<option value="2" selected>`, `value="next week"`,
				`name="tasks[1][id]" value="4"`, `data-next="2"`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			html := render(t, views.PostEditor(tt.data))
			requireContains(t, html, tt.want, tt.notWant)
			if tt.name == "the rows typed in a failed submit are kept, with the message above them" {
				if at, rows := strings.Index(html, `id="post-tasks-new-error"`), strings.Index(html, `class="task-rows"`); at < 0 || at > rows {
					t.Errorf("the message (%d) is not above the rows (%d)", at, rows)
				}
			}
		})
	}
}

func TestTaskRowAlone(t *testing.T) {
	html := render(t, views.TaskRow("edit-5", 3, views.TaskRowData{}, views.AssigneeOptions(roomMembers)))
	requireContains(t, html, []string{
		`<li class="task-row">`, `<input type="hidden" name="tasks[3][id]" value="0">`,
		`name="tasks[3][title]"`, `name="tasks[3][assignees]" multiple`, `name="tasks[3][due]" type="date"`,
		`<option value="2">Bob (@bob)</option>`, "Remove task",
	}, []string{"selected"})
	if got := views.TaskRowsSelector("edit-5"); got != "#post-task-rows-edit-5" {
		t.Errorf("TaskRowsSelector = %q", got)
	}
}

func TestPostTasksList(t *testing.T) {
	past := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	future := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	tasks := []app.TaskCard{
		{ID: 1, Title: "Late <one>", ColumnName: "To do", Assignees: []app.UserRef{{DisplayName: "Alice"}, {DisplayName: "Bob <B>"}}, DueDate: &past, Overdue: true},
		{ID: 2, Title: "Fine", ColumnName: "In progress", DueDate: &future},
		{ID: 3, Title: "Finished", ColumnName: "Done", Done: true, DueDate: &past},
		{ID: 4, Title: "Bare", ColumnName: "To do"},
	}
	html := render(t, views.PostTasks(6, tasks))
	requireContains(t, html, []string{
		`<ul class="post-tasks" aria-label="Tasks">`,
		`<li class="post-task"><svg class="icon"`, `<span class="task-title">Late &lt;one&gt;</span> <span class="task-column">To do</span> <span class="task-assignees" title="Assigned to">Alice, Bob &lt;B&gt;</span> <time class="task-due overdue" datetime="2026-10-20" title="Overdue">2026-10-20</time> <a class="task-link" href="/rooms/6/board#task-1">On the board</a></li>`,
		`<time class="task-due" datetime="2026-11-05" title="Due date">2026-11-05</time>`,
		`<span class="task-column">In progress</span>`,
		`<li class="post-task post-task-done"><svg class="icon"`, `<span class="task-title">Finished</span> <span class="task-column task-column-done">Done</span>`,
		`href="/rooms/6/board#task-4"`,
	}, []string{"Late <one>"})
	// Only the overdue one is red; the others show no assignee or date.
	if n := strings.Count(html, "overdue"); n != 1 {
		t.Errorf("%d overdue marks, want 1:\n%s", n, html)
	}
	if n := strings.Count(html, "task-assignees"); n != 1 {
		t.Errorf("%d assignee lists, want 1", n)
	}
	if got := render(t, views.PostTasks(6, nil)); strings.Contains(got, "post-tasks") {
		t.Errorf("a post with no tasks shows a list: %s", got)
	}
	if got := views.TaskBoardPath(6, 31); got != "/rooms/6/board#task-31" {
		t.Errorf("TaskBoardPath = %q", got)
	}
}

func TestPostContentShowsTheTasksUnderTheBody(t *testing.T) {
	post := app.Post{
		ID: 5, RoomID: 2, Author: app.UserRef{ID: 1, Username: "alice", DisplayName: "Alice"},
		HTML: "<p>Hello</p>", Tags: []string{"design"}, CreatedAt: time.Date(2026, 10, 26, 9, 5, 0, 0, time.UTC),
		Tasks: []app.TaskCard{{ID: 7, Title: "Do it", ColumnName: "To do"}},
	}
	html := render(t, views.PostContent(views.PostCardData{Post: post}))
	at := func(s string) int { return strings.Index(html, s) }
	if at(`class="post-body"`) >= at(`class="post-tasks"`) || at(`class="post-tasks"`) >= at(`class="tags"`) {
		t.Errorf("the tasks are not between the body and the tags:\n%s", html)
	}
	deleted := app.Post{ID: 6, RoomID: 2, Deleted: true}
	if got := render(t, views.PostContent(views.PostCardData{Post: deleted})); strings.Contains(got, "post-tasks") {
		t.Errorf("a deleted post shows tasks: %s", got)
	}
}
