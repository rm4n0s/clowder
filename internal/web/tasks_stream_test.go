package web

import (
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
)

// columnID returns the ID of the column of the board of general at the place.
func (e *roomsEnv) columnID(room string, place int) uint64 {
	e.t.Helper()
	board, err := e.board.Board(e.t.Context(), e.actor(e.alice), e.room(room).ID)
	if err != nil {
		e.t.Fatalf("Board: %+v", err)
	}
	return board.Columns[place].ID
}

// contentOf is the id of the element that holds the content of a post.
func contentOf(p app.Post) string { return `id="post-content-` + idText(p.ID) + `"` }

func TestRoomStreamRefreshesTheTasksOfPostsOnAColumnChange(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	past := mustDay(t, pastDue)
	withTasks := e.writeTasks("bob", "general", "Has tasks", app.TaskInput{Title: "Late", DueDate: &past}, app.TaskInput{Title: "Other"})
	other := e.writeTasks("alice", "general", "Also has a task", app.TaskInput{Title: "Second post task"})
	plain := e.writePost("bob", "general", "No tasks at all")
	onComment := e.commentOn(plain, "alice", "a comment with a task")
	if _, err := e.posts.Edit(t.Context(), e.actor(e.alice), onComment.ID,
		app.PostInput{Format: app.FormatText, Body: "a comment with a task", Tasks: []app.TaskInput{{Title: "Comment task"}}}); err != nil {
		t.Fatalf("Edit: %+v", err)
	}
	seen := "seen=" + idText(newestOf(e, "general"))
	c := e.serveStreams().open("alice", e.streamOfRoom("general", seen), asDatastar())
	if c.status != 200 {
		t.Fatalf("status = %d, want 200", c.status)
	}

	// A column renamed: the badges of the posts that have tasks are patched,
	// and nothing else (a comment is not in the feed, a post without tasks has
	// nothing to refresh).
	if err := e.board.RenameColumn(t.Context(), e.actor(e.alice), e.columnID("general", 0), "Backlog"); err != nil {
		t.Fatalf("RenameColumn: %+v", err)
	}
	got := c.patches(2)
	for _, p := range got {
		requirePatch(t, p, "", "outer", []string{`class="post-tasks"`, `<span class="task-column">Backlog</span>`}, []string{"<article", "To do"})
	}
	requireContains(t, got[0].html+got[1].html, []string{contentOf(withTasks), contentOf(other)}, []string{contentOf(plain)})
	c.requireQuiet()

	// A card moved to the last column: its post is patched with the badge of
	// the done column and the date no longer red.
	if err := e.board.MoveTask(t.Context(), e.actor(e.alice), withTasks.Tasks[0].ID, e.columnID("general", 2), 0); err != nil {
		t.Fatalf("MoveTask: %+v", err)
	}
	moved := c.drain()
	if len(moved) == 0 {
		t.Fatal("no patch after a card moved")
	}
	for _, p := range moved {
		requirePatch(t, p, "", "outer", []string{contentOf(withTasks), `<span class="task-column task-column-done">Done</span>`}, []string{"overdue", "<article"})
	}
}

func TestRoomStreamIgnoresTheBoardOfAnotherRoom(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	e.writeTasks("alice", "general", "Has tasks", app.TaskInput{Title: "A"})
	c := e.serveStreams().open("alice", e.streamOfRoom("general", "seen="+idText(newestOf(e, "general"))), asDatastar())
	if err := e.board.RenameColumn(t.Context(), e.actor(e.alice), e.columnID("secret", 0), "Elsewhere"); err != nil {
		t.Fatalf("RenameColumn: %+v", err)
	}
	c.requireQuiet()
}

func TestPostStreamRefreshesTheTasksOfTheThreadOnAColumnChange(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	root := e.writeTasks("alice", "general", "Root", app.TaskInput{Title: "Root task"})
	plain := e.commentOn(root, "bob", "no tasks here")
	with, err := e.posts.Reply(t.Context(), e.actor(e.bob), root.ID, app.PostInput{
		Format: app.FormatText, Body: "with a task", Tasks: []app.TaskInput{{Title: "Comment task"}},
	})
	if err != nil {
		t.Fatalf("Reply: %+v", err)
	}
	c := e.serveStreams().open("alice", e.streamOfPost(root, "seen="+idText(with.ID)), asDatastar())
	if err := e.board.RenameColumn(t.Context(), e.actor(e.alice), e.columnID("general", 0), "Backlog"); err != nil {
		t.Fatalf("RenameColumn: %+v", err)
	}
	got := c.patches(2)
	requirePatch(t, got[0], "", "outer", []string{contentOf(root), `<span class="task-column">Backlog</span>`}, []string{"<article", "To do"})
	requirePatch(t, got[1], "", "outer", []string{contentOf(with), `<span class="task-column">Backlog</span>`}, []string{"<article", "To do"})
	c.requireQuiet()

	// A task moved: the post or comment of the task is patched.
	if err := e.board.MoveTask(t.Context(), e.actor(e.alice), with.Tasks[0].ID, e.columnID("general", 1), 0); err != nil {
		t.Fatalf("MoveTask: %+v", err)
	}
	moved := c.drain()
	if len(moved) == 0 {
		t.Fatal("no patch after a card moved")
	}
	for _, p := range moved {
		requirePatch(t, p, "", "outer", []string{contentOf(with), `<span class="task-column">In progress</span>`}, []string{"<article"})
	}
	_ = plain
}
