package app_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/live"
)

// ids returns the IDs of the named users.
func (e *boardEnv) ids(names ...string) []uint64 {
	out := []uint64{}
	for _, n := range names {
		out = append(out, e.uid(n))
	}
	return out
}

func TestTaskInputRules(t *testing.T) {
	t.Parallel()
	e := newBoardEnv(t)
	var eleven, twentyOne = []uint64{}, []app.TaskInput{}
	for i := range 11 {
		eleven = append(eleven, uint64(1000+i))
	}
	for range 21 {
		twentyOne = append(twentyOne, app.TaskInput{Title: "t"})
	}
	bob, admin := e.uid("bob"), e.uid("admin")
	tests := []struct {
		name  string
		tasks []app.TaskInput
		// want is the message for the field "tasks"; empty when valid.
		want string
	}{
		{"title only", []app.TaskInput{{Title: "Fix login"}}, ""},
		{"title of 200 characters", []app.TaskInput{{Title: strings.Repeat("é", 200)}}, ""},
		{"title of 201 characters", []app.TaskInput{{Title: strings.Repeat("é", 201)}}, "Task 1: write a title of 1 to 200 characters."},
		{"empty title", []app.TaskInput{{Title: ""}}, "Task 1: write a title of 1 to 200 characters."},
		{"blank title", []app.TaskInput{{Title: " \t\n "}}, "Task 1: write a title of 1 to 200 characters."},
		{"title that is not text", []app.TaskInput{{Title: "a\xffb"}}, "Task 1: write a title of 1 to 200 characters."},
		{"the second row is wrong", []app.TaskInput{{Title: "ok"}, {Title: ""}, {Title: ""}}, "Task 2: write a title of 1 to 200 characters."},
		{"assignees who are members", []app.TaskInput{{Title: "t", Assignees: []uint64{bob, admin}}}, ""},
		{"duplicate assignees", []app.TaskInput{{Title: "t", Assignees: []uint64{bob, bob, admin, bob}}}, ""},
		{"assignee who is not a member", []app.TaskInput{{Title: "t", Assignees: []uint64{bob, e.uid("carol")}}}, "Task 1: assign only members of the room."},
		{"assignee who is a member of another room", []app.TaskInput{{Title: "ok"}, {Title: "t", Assignees: []uint64{e.uid("alice")}}}, "Task 2: assign only members of the room."},
		{"assignee who does not exist", []app.TaskInput{{Title: "t", Assignees: []uint64{9999}}}, "Task 1: assign only members of the room."},
		{"eleven assignees", []app.TaskInput{{Title: "t", Assignees: eleven}}, "Task 1: assign at most 10 people."},
		{"a task that is not new", []app.TaskInput{{ID: 1, Title: "t"}}, "Task 1: this task is not part of the post."},
		{"twenty tasks", twentyOne[:20], ""},
		{"twenty-one tasks", twentyOne, "Write at most 20 tasks."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A fresh room per case keeps the cards of one case off the board
			// of the next.
			room := e.newRoom("rules-" + strings.ReplaceAll(tt.name, " ", "-"))
			before := len(e.events())
			e.rec.Reset()
			p, err := e.posts.Create(t.Context(), e.as("bob"), room.ID, app.PostInput{Format: app.FormatText, Body: "x", Tasks: tt.tasks})
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Create: %+v", err)
				}
				if len(p.Tasks) != len(tt.tasks) {
					t.Errorf("%d tasks, want %d", len(p.Tasks), len(tt.tasks))
				}
				return
			}
			appErr := requireTag(t, err, app.TagInvalidInput, "Posts.Create.func1.InvalidInput")
			if got := fieldsOf(t, appErr); !reflect.DeepEqual(got, map[string]string{app.FieldTasks: tt.want}) {
				t.Errorf("fields = %v, want tasks: %q", got, tt.want)
			}
			if n := len(e.events()) - before; n != 0 || len(e.rec.Events()) != 0 {
				t.Errorf("wrote %d events, published %+v", n, e.rec.Events())
			}
			if got := titles(e.view("bob", room.ID)); !reflect.DeepEqual(got, [][]string{{}, {}, {}}) {
				t.Errorf("a refused post left cards: %v", got)
			}
			feed, err := e.posts.Feed(t.Context(), e.as("bob"), room.ID, 0, 10)
			if err != nil || len(feed.Posts) != 0 {
				t.Errorf("a refused post was written: %v, %v", feed.Posts, err)
			}
		})
	}
}

func TestTaskCleaning(t *testing.T) {
	t.Parallel()
	e := newBoardEnv(t)
	// 2026-11-03 23:30 in UTC-5 is 2026-11-04 04:30 UTC: the day is converted
	// to UTC before the time is dropped.
	west := time.Date(2026, 11, 3, 23, 30, 0, 0, time.FixedZone("west", -5*3600))
	east := time.Date(2026, 11, 3, 1, 0, 0, 0, time.FixedZone("east", 5*3600))
	zero := time.Time{}
	p, err := e.posts.Create(t.Context(), e.as("bob"), e.lobby.ID, app.PostInput{Format: app.FormatText, Body: "x", Tasks: []app.TaskInput{
		{Title: "  padded  ", Assignees: e.ids("bob", "admin", "bob"), DueDate: &west},
		{Title: "east", DueDate: &east},
		{Title: "zero", DueDate: &zero},
	}})
	if err != nil {
		t.Fatalf("Create: %+v", err)
	}
	if got := p.Tasks[0]; got.Title != "padded" || len(got.Assignees) != 2 || got.Assignees[0].Username != "bob" || got.Assignees[1].Username != "admin" {
		t.Errorf("task 1 = %+v", got)
	}
	days := []*time.Time{
		ptr(time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)),
		ptr(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)),
		nil,
	}
	for i, want := range days {
		got := p.Tasks[i].DueDate
		if (got == nil) != (want == nil) || (got != nil && !got.Equal(*want)) {
			t.Errorf("task %d: DueDate = %v, want %v", i+1, got, want)
		}
	}
}

// ptr returns a pointer to v.
func ptr[T any](v T) *T { return &v }

func TestTasksEmptyBody(t *testing.T) {
	t.Parallel()
	e := newBoardEnv(t)
	ctx := t.Context()
	p, err := e.posts.Create(ctx, e.as("bob"), e.lobby.ID, app.PostInput{Format: app.FormatText, Tasks: []app.TaskInput{{Title: "Only a task"}}})
	if err != nil || p.Body != "" || len(p.Tasks) != 1 {
		t.Fatalf("Create = %+v, %v", p, err)
	}
	_, err = e.posts.Create(ctx, e.as("bob"), e.lobby.ID, app.PostInput{Format: app.FormatText})
	appErr := requireTag(t, err, app.TagInvalidInput, "Posts.Create.func1.InvalidInput")
	if got := fieldsOf(t, appErr); got[app.FieldBody] == "" {
		t.Errorf("fields = %v", got)
	}
	c, err := e.posts.Reply(ctx, e.as("admin"), p.ID, app.PostInput{Format: app.FormatText, Tasks: []app.TaskInput{{Title: "A comment's task"}}})
	if err != nil || c.Body != "" || len(c.Tasks) != 1 {
		t.Fatalf("Reply = %+v, %v", c, err)
	}
	// An edit may empty the body when the post keeps a task, and not when it
	// removes the last one.
	e2, err := e.posts.Edit(ctx, e.as("bob"), p.ID, app.PostInput{Format: app.FormatText, Tasks: []app.TaskInput{{ID: p.Tasks[0].ID, Title: "Only a task"}}})
	if err != nil || e2.Body != "" {
		t.Fatalf("Edit = %+v, %v", e2, err)
	}
	_, err = e.posts.Edit(ctx, e.as("bob"), p.ID, app.PostInput{Format: app.FormatText})
	appErr = requireTag(t, err, app.TagInvalidInput, "Posts.Edit.func1.InvalidInput")
	if got := fieldsOf(t, appErr); got[app.FieldBody] == "" {
		t.Errorf("fields = %v", got)
	}
}

func TestTasksGoToTheEndOfTheFirstColumn(t *testing.T) {
	t.Parallel()
	e := newBoardEnv(t)
	ctx := t.Context()
	due := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	before := len(e.events())
	e.rec.Reset()
	p1, err := e.posts.Create(ctx, e.as("bob"), e.lobby.ID, app.PostInput{Format: app.FormatText, Body: "release", Tasks: []app.TaskInput{
		{Title: "one", Assignees: e.ids("bob"), DueDate: &due}, {Title: "two"},
	}})
	if err != nil {
		t.Fatalf("Create: %+v", err)
	}
	wantLive := []live.Event{
		{Topic: live.RoomTopic(e.lobby.ID), Kind: live.KindPostCreated, RoomID: e.lobby.ID, PostID: p1.ID},
		{Topic: live.PostTopic(p1.ID), Kind: live.KindPostCreated, RoomID: e.lobby.ID, PostID: p1.ID},
		{Topic: live.BoardTopic(e.lobby.ID), Kind: live.KindBoardChanged, RoomID: e.lobby.ID, PostID: p1.ID},
		unreadEvt(e.uid("admin"), e.lobby.ID),
	}
	if got := norm(e.rec.Events()); !reflect.DeepEqual(got, wantLive) {
		t.Errorf("published %+v, want %+v", got, wantLive)
	}
	e.requireNewEvents(before,
		wantEvent{
			actor: "bob", action: app.ActionPostCreated, room: e.lobby.ID, entityType: app.EntityPost, entityID: p1.ID,
			summary: "Bob posted in #lobby",
			details: map[string]any{"format": "text", "length": float64(len("release")), "tags": []any{}, "attachments": float64(0), "tasks": float64(2)},
		},
		wantEvent{
			actor: "bob", action: app.ActionTaskCreated, room: e.lobby.ID, entityType: app.EntityTask, entityID: p1.Tasks[0].ID,
			summary: `Bob added the task "one" in #lobby`,
			details: map[string]any{
				"postID":    float64(p1.ID),
				"title":     map[string]any{"old": nil, "new": "one"},
				"column":    map[string]any{"old": nil, "new": "To do"},
				"assignees": map[string]any{"old": nil, "new": []any{"bob"}},
				"dueDate":   map[string]any{"old": nil, "new": "2026-10-30"},
			},
		},
		wantEvent{
			actor: "bob", action: app.ActionTaskCreated, room: e.lobby.ID, entityType: app.EntityTask, entityID: p1.Tasks[1].ID,
			summary: `Bob added the task "two" in #lobby`,
			details: map[string]any{
				"postID":    float64(p1.ID),
				"title":     map[string]any{"old": nil, "new": "two"},
				"column":    map[string]any{"old": nil, "new": "To do"},
				"assignees": map[string]any{"old": nil, "new": []any{}},
				"dueDate":   map[string]any{"old": nil, "new": nil},
			},
		})

	// A card in another column does not change where the new ones go.
	cols := e.view("bob", e.lobby.ID).Columns
	if err := e.board.MoveTask(ctx, e.as("bob"), p1.Tasks[0].ID, cols[1].ID, 0); err != nil {
		t.Fatalf("MoveTask: %+v", err)
	}
	c1, err := e.posts.Reply(ctx, e.as("admin"), p1.ID, app.PostInput{Format: app.FormatText, Body: "c", Tasks: []app.TaskInput{{Title: "three"}, {Title: "four"}}})
	if err != nil {
		t.Fatalf("Reply: %+v", err)
	}
	e.write("admin", "lobby", "no tasks")
	e.withTasks("admin", e.lobby.ID, app.TaskInput{Title: "five"})
	v := e.view("bob", e.lobby.ID)
	if got, want := titles(v), [][]string{{"two", "three", "four", "five"}, {"one"}, {}}; !reflect.DeepEqual(got, want) {
		t.Errorf("board = %v, want %v", got, want)
	}
	requireDense(t, v)
	if got := card(t, v, "three"); got.PostID != c1.ID || got.RootID != p1.ID {
		t.Errorf("three = %+v", got)
	}

	// The views of the post and the comment carry their tasks.
	got, err := e.posts.Get(ctx, e.as("alice"), p1.ID)
	if err != nil {
		t.Fatalf("Get: %+v", err)
	}
	if len(got.Tasks) != 2 || got.Tasks[0].Title != "one" || got.Tasks[0].ColumnName != "In progress" ||
		got.Tasks[1].Title != "two" || got.Tasks[1].ColumnName != "To do" || got.Tasks[0].PostID != p1.ID || got.Tasks[0].RootID != p1.ID {
		t.Errorf("Get.Tasks = %+v", got.Tasks)
	}
	if got.Tasks[0].DueDate == nil || !got.Tasks[0].DueDate.Equal(due) || got.Tasks[0].Overdue {
		t.Errorf("one: DueDate %v, Overdue %v (the clock is on 2026-10-26: a due date of 10-30 is not overdue)", got.Tasks[0].DueDate, got.Tasks[0].Overdue)
	}
	th, err := e.posts.Thread(ctx, e.as("bob"), c1.ID)
	if err != nil || len(th.Root.Tasks) != 2 || len(th.Comments) != 1 || len(th.Comments[0].Tasks) != 2 || th.Comments[0].Tasks[0].Title != "three" {
		t.Fatalf("Thread = %+v, %v", th, err)
	}
	feed, err := e.posts.Feed(ctx, e.as("bob"), e.lobby.ID, 0, 10)
	if err != nil || len(feed.Posts) != 3 {
		t.Fatalf("Feed = %+v, %v", feed, err)
	}
	counts := map[uint64]int{}
	for _, p := range feed.Posts {
		counts[p.ID] = len(p.Tasks)
	}
	if counts[p1.ID] != 2 || len(counts) != 3 {
		t.Errorf("tasks per post in the feed = %v", counts)
	}
}

func TestTasksOverdueInPostViews(t *testing.T) {
	t.Parallel()
	e := newBoardEnv(t)
	yesterday := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	p := e.withTasks("bob", e.lobby.ID, app.TaskInput{Title: "late", DueDate: &yesterday}, app.TaskInput{Title: "done", DueDate: &yesterday})
	cols := e.view("bob", e.lobby.ID).Columns
	if err := e.board.MoveTask(t.Context(), e.as("bob"), p.Tasks[1].ID, cols[2].ID, 0); err != nil {
		t.Fatalf("MoveTask: %+v", err)
	}
	got, err := e.posts.Get(t.Context(), e.as("bob"), p.ID)
	if err != nil || len(got.Tasks) != 2 || !got.Tasks[0].Overdue || got.Tasks[1].Overdue {
		t.Errorf("Get.Tasks = %+v, %v", got.Tasks, err)
	}
}

func TestTasksEdit(t *testing.T) {
	t.Parallel()
	e := newBoardEnv(t)
	ctx := t.Context()
	due := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	newDue := time.Date(2026, 11, 9, 14, 0, 0, 0, time.UTC)
	other := e.withTasks("admin", e.lobby.ID, app.TaskInput{Title: "other"})
	p := e.withTasks("bob", e.lobby.ID,
		app.TaskInput{Title: "keep", Assignees: e.ids("bob")},
		app.TaskInput{Title: "change", Assignees: e.ids("bob"), DueDate: &due},
		app.TaskInput{Title: "drop"},
	)
	tail := e.withTasks("admin", e.lobby.ID, app.TaskInput{Title: "tail"})
	_ = tail
	keep, change, drop := p.Tasks[0], p.Tasks[1], p.Tasks[2]
	if got := titles(e.view("bob", e.lobby.ID)); !reflect.DeepEqual(got, [][]string{{"other", "keep", "change", "drop", "tail"}, {}, {}}) {
		t.Fatalf("board = %v", got)
	}
	// Move "change" to Done: an edit does not move a card.
	cols := e.view("bob", e.lobby.ID).Columns
	if err := e.board.MoveTask(ctx, e.as("bob"), change.ID, cols[2].ID, 0); err != nil {
		t.Fatalf("MoveTask: %+v", err)
	}
	e.clk.Advance(time.Hour)
	before := len(e.events())
	e.rec.Reset()
	got, err := e.posts.Edit(ctx, e.as("bob"), p.ID, app.PostInput{Format: app.FormatText, Body: "a post", Tasks: []app.TaskInput{
		{ID: change.ID, Title: " changed ", Assignees: e.ids("admin", "bob"), DueDate: &newDue},
		{ID: keep.ID, Title: "keep", Assignees: e.ids("bob")},
		{Title: "new one", DueDate: &due},
		{Title: "new two"},
	}})
	if err != nil {
		t.Fatalf("Edit: %+v", err)
	}
	// The order of the post's tasks is the order they were made in.
	var gotTitles []string
	for _, c := range got.Tasks {
		gotTitles = append(gotTitles, c.Title)
	}
	if want := []string{"keep", "changed", "new one", "new two"}; !reflect.DeepEqual(gotTitles, want) {
		t.Errorf("post tasks = %v, want %v", gotTitles, want)
	}
	if got.EditedAt == nil || !got.EditedAt.Equal(e.clk.Now()) {
		t.Errorf("EditedAt = %v", got.EditedAt)
	}
	v := e.view("bob", e.lobby.ID)
	if want := [][]string{{"other", "keep", "tail", "new one", "new two"}, {}, {"changed"}}; !reflect.DeepEqual(titles(v), want) {
		t.Errorf("board = %v, want %v", titles(v), want)
	}
	requireDense(t, v)
	if c := card(t, v, "changed"); c.ID != change.ID || len(c.Assignees) != 2 || c.Assignees[0].Username != "admin" ||
		c.DueDate == nil || !c.DueDate.Equal(time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("changed = %+v", c)
	}
	if c := card(t, v, "keep"); c.ID != keep.ID || len(c.Assignees) != 1 {
		t.Errorf("keep = %+v", c)
	}
	if card(t, v, "other").ID != other.Tasks[0].ID {
		t.Errorf("the task of another post changed")
	}
	if _, err := e.st.TaskByID(ctx, drop.ID); err == nil {
		t.Errorf("the dropped task is still there")
	}
	nTasks := func(title string) uint64 { return card(t, v, title).ID }
	e.requireNewEvents(before,
		wantEvent{
			actor: "bob", action: app.ActionPostEdited, room: e.lobby.ID, entityType: app.EntityPost, entityID: p.ID,
			summary: "Bob edited a post in #lobby",
			details: map[string]any{"tasks": map[string]any{
				"added": []any{"new one", "new two"}, "edited": []any{"changed"}, "removed": []any{"drop"},
			}},
		},
		wantEvent{
			actor: "bob", action: app.ActionTaskDeleted, room: e.lobby.ID, entityType: app.EntityTask, entityID: drop.ID,
			summary: `Bob deleted the task "drop" in #lobby`,
			details: map[string]any{
				"postID": float64(p.ID), "title": map[string]any{"old": "drop", "new": nil},
				"column": map[string]any{"old": "To do", "new": nil}, "assignees": map[string]any{"old": []any{}, "new": nil},
				"dueDate": map[string]any{"old": nil, "new": nil},
			},
		},
		wantEvent{
			actor: "bob", action: app.ActionTaskEdited, room: e.lobby.ID, entityType: app.EntityTask, entityID: change.ID,
			summary: `Bob edited the task "changed" in #lobby`,
			details: map[string]any{
				"title":     map[string]any{"old": "change", "new": "changed"},
				"assignees": map[string]any{"old": []any{"bob"}, "new": []any{"admin", "bob"}},
				"dueDate":   map[string]any{"old": "2026-11-03", "new": "2026-11-09"},
			},
		},
		wantEvent{
			actor: "bob", action: app.ActionTaskCreated, room: e.lobby.ID, entityType: app.EntityTask, entityID: nTasks("new one"),
			summary: `Bob added the task "new one" in #lobby`,
			details: map[string]any{
				"postID": float64(p.ID), "title": map[string]any{"old": nil, "new": "new one"},
				"column": map[string]any{"old": nil, "new": "To do"}, "assignees": map[string]any{"old": nil, "new": []any{}},
				"dueDate": map[string]any{"old": nil, "new": "2026-11-03"},
			},
		},
		wantEvent{
			actor: "bob", action: app.ActionTaskCreated, room: e.lobby.ID, entityType: app.EntityTask, entityID: nTasks("new two"),
			summary: `Bob added the task "new two" in #lobby`,
			details: map[string]any{
				"postID": float64(p.ID), "title": map[string]any{"old": nil, "new": "new two"},
				"column": map[string]any{"old": nil, "new": "To do"}, "assignees": map[string]any{"old": nil, "new": []any{}},
				"dueDate": map[string]any{"old": nil, "new": nil},
			},
		})
	wantLive := []live.Event{
		{Topic: live.RoomTopic(e.lobby.ID), Kind: live.KindPostUpdated, RoomID: e.lobby.ID, PostID: p.ID},
		{Topic: live.PostTopic(p.ID), Kind: live.KindPostUpdated, RoomID: e.lobby.ID, PostID: p.ID},
		{Topic: live.BoardTopic(e.lobby.ID), Kind: live.KindBoardChanged, RoomID: e.lobby.ID, PostID: p.ID},
		// admin is newly assigned to "changed": bob, the actor, is not told.
		notifEvt(e.users["admin"].ID, e.lobby.ID, p.ID),
	}
	if got := norm(e.rec.Events()); !reflect.DeepEqual(got, wantLive) {
		t.Errorf("published %+v, want %+v", got, wantLive)
	}

	// The same edit again changes nothing: no event, no live event, no new
	// edit time.
	same := app.PostInput{Format: app.FormatText, Body: "a post", Tasks: []app.TaskInput{
		{ID: keep.ID, Title: "keep", Assignees: e.ids("bob")},
		{ID: change.ID, Title: "changed", Assignees: e.ids("bob", "admin"), DueDate: &newDue},
	}}
	// The first edit removes the two new tasks; the second changes nothing.
	if _, err := e.posts.Edit(ctx, e.as("bob"), p.ID, same); err != nil {
		t.Fatalf("Edit: %+v", err)
	}
	e.clk.Advance(time.Hour)
	before = len(e.events())
	e.rec.Reset()
	again, err := e.posts.Edit(ctx, e.as("bob"), p.ID, same)
	if err != nil {
		t.Fatalf("Edit: %+v", err)
	}
	if n := len(e.events()) - before; n != 0 || len(e.rec.Events()) != 0 {
		t.Errorf("a no-op edit wrote %d events and published %+v", n, e.rec.Events())
	}
	if again.EditedAt == nil || again.EditedAt.Equal(e.clk.Now()) {
		t.Errorf("a no-op edit changed EditedAt to %v", again.EditedAt)
	}
	if got := titles(e.view("bob", e.lobby.ID)); !reflect.DeepEqual(got, [][]string{{"other", "keep", "tail"}, {}, {"changed"}}) {
		t.Errorf("board = %v", got)
	}

	// An edit that lists no tasks deletes them all and keeps the body.
	before = len(e.events())
	if _, err := e.posts.Edit(ctx, e.as("bob"), p.ID, app.PostInput{Format: app.FormatText, Body: "a post"}); err != nil {
		t.Fatalf("Edit: %+v", err)
	}
	var kinds []app.AuditAction
	for _, ev := range e.events()[before:] {
		kinds = append(kinds, ev.Action)
	}
	if want := []app.AuditAction{app.ActionPostEdited, app.ActionTaskDeleted, app.ActionTaskDeleted}; !reflect.DeepEqual(kinds, want) {
		t.Errorf("actions = %v, want %v", kinds, want)
	}
	v = e.view("bob", e.lobby.ID)
	if want := [][]string{{"other", "tail"}, {}, {}}; !reflect.DeepEqual(titles(v), want) {
		t.Errorf("board = %v, want %v", titles(v), want)
	}
	requireDense(t, v)
}

func TestTasksEditOfAComment(t *testing.T) {
	t.Parallel()
	e := newBoardEnv(t)
	p := e.write("bob", "lobby", "root")
	c, err := e.posts.Reply(t.Context(), e.as("admin"), p.ID, app.PostInput{Format: app.FormatText, Body: "c", Tasks: []app.TaskInput{{Title: "t"}}})
	if err != nil {
		t.Fatalf("Reply: %+v", err)
	}
	e.rec.Reset()
	before := len(e.events())
	// Only the tasks change.
	if _, err := e.posts.Edit(t.Context(), e.as("admin"), c.ID, app.PostInput{Format: app.FormatText, Body: "c", Tasks: []app.TaskInput{{ID: c.Tasks[0].ID, Title: "t2"}}}); err != nil {
		t.Fatalf("Edit: %+v", err)
	}
	e.requireNewEvents(before,
		wantEvent{
			actor: "admin", action: app.ActionPostEdited, room: e.lobby.ID, entityType: app.EntityComment, entityID: c.ID,
			summary: "Admin edited a comment in #lobby",
			details: map[string]any{"tasks": map[string]any{"added": []any{}, "edited": []any{"t2"}, "removed": []any{}}},
		},
		wantEvent{
			actor: "admin", action: app.ActionTaskEdited, room: e.lobby.ID, entityType: app.EntityTask, entityID: c.Tasks[0].ID,
			summary: `Admin edited the task "t2" in #lobby`,
			details: map[string]any{"title": map[string]any{"old": "t", "new": "t2"}},
		})
	wantLive := []live.Event{
		{Topic: live.PostTopic(p.ID), Kind: live.KindCommentUpdated, RoomID: e.lobby.ID, PostID: c.ID, RootID: p.ID},
		{Topic: live.RoomTopic(e.lobby.ID), Kind: live.KindPostUpdated, RoomID: e.lobby.ID, PostID: p.ID, RootID: p.ID},
		{Topic: live.BoardTopic(e.lobby.ID), Kind: live.KindBoardChanged, RoomID: e.lobby.ID, PostID: c.ID},
	}
	if got := norm(e.rec.Events()); !reflect.DeepEqual(got, wantLive) {
		t.Errorf("published %+v, want %+v", got, wantLive)
	}
}

func TestTasksEditRefusals(t *testing.T) {
	t.Parallel()
	e := newBoardEnv(t)
	mine := e.withTasks("bob", e.lobby.ID, app.TaskInput{Title: "mine"}, app.TaskInput{Title: "mine 2"})
	theirs := e.withTasks("admin", e.lobby.ID, app.TaskInput{Title: "theirs"})
	den := e.withTasks("bob", e.den.ID, app.TaskInput{Title: "in the den"})
	notAMember := "assign only members of the room."
	tests := []struct {
		name  string
		tasks []app.TaskInput
		want  string
	}{
		{"a task of another post", []app.TaskInput{{ID: theirs.Tasks[0].ID, Title: "x"}}, "Task 1: this task is not part of the post."},
		{"a task of another room", []app.TaskInput{{ID: den.Tasks[0].ID, Title: "x"}}, "Task 1: this task is not part of the post."},
		{"a task that does not exist", []app.TaskInput{{ID: 9999, Title: "x"}}, "Task 1: this task is not part of the post."},
		{"the same task twice", []app.TaskInput{{ID: mine.Tasks[0].ID, Title: "x"}, {ID: mine.Tasks[0].ID, Title: "y"}}, "Task 2: this task is not part of the post."},
		{"a wrong title in the second row", []app.TaskInput{{ID: mine.Tasks[0].ID, Title: "x"}, {ID: mine.Tasks[1].ID}}, "Task 2: write a title of 1 to 200 characters."},
		{"an assignee who is not a member", []app.TaskInput{{ID: mine.Tasks[0].ID, Title: "x", Assignees: e.ids("carol")}}, "Task 1: " + notAMember},
		{"twenty-one tasks", func() []app.TaskInput {
			out := make([]app.TaskInput, 21)
			for i := range out {
				out[i] = app.TaskInput{Title: "t"}
			}
			return out
		}(), "Write at most 20 tasks."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := len(e.events())
			e.rec.Reset()
			_, err := e.posts.Edit(t.Context(), e.as("bob"), mine.ID, app.PostInput{Format: app.FormatText, Body: "mine", Tasks: tt.tasks})
			appErr := requireTag(t, err, app.TagInvalidInput, "Posts.Edit.func1.InvalidInput")
			if got := fieldsOf(t, appErr); !reflect.DeepEqual(got, map[string]string{app.FieldTasks: tt.want}) {
				t.Errorf("fields = %v, want %q", got, tt.want)
			}
			if n := len(e.events()) - before; n != 0 || len(e.rec.Events()) != 0 {
				t.Errorf("wrote %d events, published %+v", n, e.rec.Events())
			}
			if got := titles(e.view("bob", e.lobby.ID)); !reflect.DeepEqual(got, [][]string{{"mine", "mine 2", "theirs"}, {}, {}}) {
				t.Errorf("a refused edit changed the board: %v", got)
			}
		})
	}
}

func TestTasksDeleteWithThePost(t *testing.T) {
	t.Parallel()
	e := newBoardEnv(t)
	ctx := t.Context()
	e.withTasks("admin", e.lobby.ID, app.TaskInput{Title: "before"})
	p := e.withTasks("bob", e.lobby.ID, app.TaskInput{Title: "a", Assignees: e.ids("bob")}, app.TaskInput{Title: "b"})
	e.withTasks("admin", e.lobby.ID, app.TaskInput{Title: "after"})
	cols := e.view("bob", e.lobby.ID).Columns
	if err := e.board.MoveTask(ctx, e.as("bob"), p.Tasks[1].ID, cols[1].ID, 0); err != nil {
		t.Fatalf("MoveTask: %+v", err)
	}
	before := len(e.events())
	e.rec.Reset()
	if err := e.posts.Delete(ctx, e.as("admin"), p.ID); err != nil {
		t.Fatalf("Delete: %+v", err)
	}
	v := e.view("bob", e.lobby.ID)
	if want := [][]string{{"before", "after"}, {}, {}}; !reflect.DeepEqual(titles(v), want) {
		t.Errorf("board = %v, want %v", titles(v), want)
	}
	requireDense(t, v)
	e.requireNewEvents(before,
		wantEvent{
			actor: "admin", action: app.ActionPostDeleted, room: e.lobby.ID, entityType: app.EntityPost, entityID: p.ID,
			summary: "Admin deleted a post in #lobby", details: map[string]any{"kept": false},
		},
		wantEvent{
			actor: "admin", action: app.ActionTaskDeleted, room: e.lobby.ID, entityType: app.EntityTask, entityID: p.Tasks[0].ID,
			summary: `Admin deleted the task "a" in #lobby`,
			details: map[string]any{
				"postID": float64(p.ID), "title": map[string]any{"old": "a", "new": nil},
				"column": map[string]any{"old": "To do", "new": nil}, "assignees": map[string]any{"old": []any{"bob"}, "new": nil},
				"dueDate": map[string]any{"old": nil, "new": nil},
			},
		},
		wantEvent{
			actor: "admin", action: app.ActionTaskDeleted, room: e.lobby.ID, entityType: app.EntityTask, entityID: p.Tasks[1].ID,
			summary: `Admin deleted the task "b" in #lobby`,
			details: map[string]any{
				"postID": float64(p.ID), "title": map[string]any{"old": "b", "new": nil},
				"column": map[string]any{"old": "In progress", "new": nil}, "assignees": map[string]any{"old": []any{}, "new": nil},
				"dueDate": map[string]any{"old": nil, "new": nil},
			},
		})
	wantLive := []live.Event{
		{Topic: live.RoomTopic(e.lobby.ID), Kind: live.KindPostDeleted, RoomID: e.lobby.ID, PostID: p.ID},
		{Topic: live.PostTopic(p.ID), Kind: live.KindPostDeleted, RoomID: e.lobby.ID, PostID: p.ID},
		unreadEvt(e.uid("admin"), e.lobby.ID),
		{Topic: live.BoardTopic(e.lobby.ID), Kind: live.KindBoardChanged, RoomID: e.lobby.ID, PostID: p.ID},
	}
	if got := norm(e.rec.Events()); !reflect.DeepEqual(got, wantLive) {
		t.Errorf("published %+v, want %+v", got, wantLive)
	}

	// A post without tasks publishes no board event.
	plain := e.write("bob", "lobby", "plain")
	e.rec.Reset()
	if err := e.posts.Delete(ctx, e.as("bob"), plain.ID); err != nil {
		t.Fatalf("Delete: %+v", err)
	}
	for _, ev := range e.rec.Events() {
		if ev.Kind == live.KindBoardChanged {
			t.Errorf("a post without tasks published %+v", ev)
		}
	}
}

func TestTasksDeleteOfAKeptPlaceholder(t *testing.T) {
	t.Parallel()
	e := newBoardEnv(t)
	ctx := t.Context()
	p := e.withTasks("bob", e.lobby.ID, app.TaskInput{Title: "a"})
	c, err := e.posts.Reply(ctx, e.as("admin"), p.ID, app.PostInput{Format: app.FormatText, Body: "reply", Tasks: []app.TaskInput{{Title: "of the reply"}}})
	if err != nil {
		t.Fatalf("Reply: %+v", err)
	}
	before := len(e.events())
	e.rec.Reset()
	if err := e.posts.Delete(ctx, e.as("bob"), p.ID); err != nil {
		t.Fatalf("Delete: %+v", err)
	}
	// The post stays, deleted; its task is gone; the reply's task stays.
	got, err := e.posts.Get(ctx, e.as("bob"), p.ID)
	if err != nil || !got.Deleted || len(got.Tasks) != 0 {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if want := [][]string{{"of the reply"}, {}, {}}; !reflect.DeepEqual(titles(e.view("bob", e.lobby.ID)), want) {
		t.Errorf("board = %v, want %v", titles(e.view("bob", e.lobby.ID)), want)
	}
	requireDense(t, e.view("bob", e.lobby.ID))
	e.requireNewEvents(before,
		wantEvent{
			actor: "bob", action: app.ActionPostDeleted, room: e.lobby.ID, entityType: app.EntityPost, entityID: p.ID,
			summary: "Bob deleted a post in #lobby", details: map[string]any{"kept": true},
		},
		wantEvent{
			actor: "bob", action: app.ActionTaskDeleted, room: e.lobby.ID, entityType: app.EntityTask, entityID: p.Tasks[0].ID,
			summary: `Bob deleted the task "a" in #lobby`,
			details: map[string]any{
				"postID": float64(p.ID), "title": map[string]any{"old": "a", "new": nil},
				"column": map[string]any{"old": "To do", "new": nil}, "assignees": map[string]any{"old": []any{}, "new": nil},
				"dueDate": map[string]any{"old": nil, "new": nil},
			},
		})
	// Deleting the reply removes its task and, with the placeholder, the post.
	e.rec.Reset()
	before = len(e.events())
	if err := e.posts.Delete(ctx, e.as("admin"), c.ID); err != nil {
		t.Fatalf("Delete: %+v", err)
	}
	if want := [][]string{{}, {}, {}}; !reflect.DeepEqual(titles(e.view("bob", e.lobby.ID)), want) {
		t.Errorf("board = %v, want %v", titles(e.view("bob", e.lobby.ID)), want)
	}
	if n := len(e.events()) - before; n != 2 {
		t.Errorf("%d events, want comment deleted and task deleted", n)
	}
	var board []live.Event
	for _, ev := range e.rec.Events() {
		if ev.Kind == live.KindBoardChanged {
			board = append(board, ev)
		}
	}
	if want := []live.Event{{Topic: live.BoardTopic(e.lobby.ID), Kind: live.KindBoardChanged, RoomID: e.lobby.ID, PostID: c.ID}}; !reflect.DeepEqual(board, want) {
		t.Errorf("board events = %+v, want %+v", board, want)
	}
}

func TestTasksOfARoomThatIsDeleted(t *testing.T) {
	t.Parallel()
	e := newBoardEnv(t)
	e.withTasks("bob", e.lobby.ID, app.TaskInput{Title: "a", Assignees: e.ids("bob")})
	before := len(e.events())
	if err := e.rooms.Delete(t.Context(), e.as("admin"), e.lobby.ID); err != nil {
		t.Fatalf("Delete: %+v", err)
	}
	// Only the room's deletion is audited (D27).
	if n := len(e.events()) - before; n != 1 {
		t.Errorf("%d events, want only room.deleted", n)
	}
}

// formerMemberEnv has the admin write a post in lobby with the task "t",
// assigned to bob, and the task "u", assigned to nobody, and then removes bob
// from lobby.
func formerMemberEnv(t *testing.T) (*boardEnv, app.Post) {
	t.Helper()
	e := newBoardEnv(t)
	p := e.withTasks("admin", e.lobby.ID, app.TaskInput{Title: "t", Assignees: e.ids("bob")}, app.TaskInput{Title: "u"})
	if err := e.rooms.RemoveMember(t.Context(), e.as("admin"), e.lobby.ID, e.uid("bob")); err != nil {
		t.Fatalf("RemoveMember: %+v", err)
	}
	return e, p
}

func TestTasksEditWithAFormerMember(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// tasks builds the edit from the IDs of "t" and "u".
		tasks func(e *boardEnv, t, u uint64) []app.TaskInput
		// want is the message for the field "tasks"; empty when the edit works.
		want string
		// assignees is the usernames of "t" and "u" afterwards.
		assignees [2][]string
		// audit is the details of the task.edited of "t", nil for none.
		audit map[string]any
	}{
		{"keep a former member", func(e *boardEnv, t, u uint64) []app.TaskInput {
			return []app.TaskInput{{ID: t, Title: "t", Assignees: e.ids("bob")}, {ID: u, Title: "u"}}
		}, "", [2][]string{{"bob"}, {}}, nil},
		{"keep a former member and add a member", func(e *boardEnv, t, u uint64) []app.TaskInput {
			return []app.TaskInput{{ID: t, Title: "t", Assignees: e.ids("bob", "admin")}, {ID: u, Title: "u"}}
		}, "", [2][]string{{"bob", "admin"}, {}}, map[string]any{
			"assignees": map[string]any{"old": []any{"bob"}, "new": []any{"bob", "admin"}},
		}},
		{"remove a former member", func(e *boardEnv, t, u uint64) []app.TaskInput {
			return []app.TaskInput{{ID: t, Title: "t"}, {ID: u, Title: "u"}}
		}, "", [2][]string{{}, {}}, map[string]any{
			"assignees": map[string]any{"old": []any{"bob"}, "new": []any{}},
		}},
		{"add a former member to another task", func(e *boardEnv, t, u uint64) []app.TaskInput {
			return []app.TaskInput{{ID: t, Title: "t", Assignees: e.ids("bob")}, {ID: u, Title: "u", Assignees: e.ids("bob")}}
		}, "Task 2: assign only members of the room.", [2][]string{}, nil},
		{"add a former member to a new task", func(e *boardEnv, t, u uint64) []app.TaskInput {
			return []app.TaskInput{{ID: t, Title: "t", Assignees: e.ids("bob")}, {ID: u, Title: "u"}, {Title: "new", Assignees: e.ids("bob")}}
		}, "Task 3: assign only members of the room.", [2][]string{}, nil},
		{"a former member of another task is not a member either", func(e *boardEnv, t, u uint64) []app.TaskInput {
			return []app.TaskInput{{ID: t, Title: "t"}, {ID: u, Title: "u", Assignees: e.ids("bob")}}
		}, "Task 2: assign only members of the room.", [2][]string{}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, p := formerMemberEnv(t)
			before := len(e.events())
			_, err := e.posts.Edit(t.Context(), e.as("admin"), p.ID,
				app.PostInput{Format: app.FormatText, Body: "a post", Tasks: tt.tasks(e, p.Tasks[0].ID, p.Tasks[1].ID)})
			if tt.want != "" {
				appErr := requireTag(t, err, app.TagInvalidInput, "Posts.Edit.func1.InvalidInput")
				if got := fieldsOf(t, appErr); !reflect.DeepEqual(got, map[string]string{app.FieldTasks: tt.want}) {
					t.Errorf("fields = %v, want %q", got, tt.want)
				}
				if n := len(e.events()) - before; n != 0 {
					t.Errorf("wrote %d events", n)
				}
				return
			}
			if err != nil {
				t.Fatalf("Edit: %+v", err)
			}
			got, err := e.posts.Get(t.Context(), e.as("admin"), p.ID)
			if err != nil {
				t.Fatalf("Get: %+v", err)
			}
			for i, want := range tt.assignees {
				names := []string{}
				for _, a := range got.Tasks[i].Assignees {
					names = append(names, a.Username)
				}
				if !reflect.DeepEqual(names, want) {
					t.Errorf("task %d assignees = %v, want %v", i+1, names, want)
				}
			}
			var edited []map[string]any
			for _, ev := range e.events()[before:] {
				if ev.Action == app.ActionTaskEdited {
					details := map[string]any{}
					if err := json.Unmarshal([]byte(ev.Details), &details); err != nil {
						t.Fatal(err)
					}
					edited = append(edited, details)
				}
			}
			switch {
			case tt.audit == nil && len(edited) != 0:
				t.Errorf("task.edited events = %v, want none", edited)
			case tt.audit != nil && (len(edited) != 1 || !reflect.DeepEqual(edited[0], tt.audit)):
				t.Errorf("task.edited events = %v, want %v", edited, tt.audit)
			}
		})
	}
}

func TestBoardUpdateTaskWithAFormerMember(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		assignees func(e *boardEnv) []uint64
		invalid   bool
		want      []string
	}{
		{"keep a former member", func(e *boardEnv) []uint64 { return e.ids("bob") }, false, []string{"bob"}},
		{"keep a former member and add a member", func(e *boardEnv) []uint64 { return e.ids("admin", "bob") }, false, []string{"admin", "bob"}},
		{"remove a former member", func(e *boardEnv) []uint64 { return nil }, false, []string{}},
		{"add a former member", func(e *boardEnv) []uint64 { return e.ids("bob", "alice") }, true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, p := formerMemberEnv(t)
			before := len(e.events())
			ids := tt.assignees(e)
			if ids == nil {
				ids = []uint64{}
			}
			err := e.board.UpdateTask(t.Context(), e.as("admin"), p.Tasks[0].ID, app.TaskPatch{Assignees: &ids})
			if tt.invalid {
				appErr := requireTag(t, err, app.TagInvalidInput, "Board.UpdateTask.func1.InvalidInput")
				if got := fieldsOf(t, appErr); got[app.FieldAssignees] == "" {
					t.Errorf("fields = %v", got)
				}
				if n := len(e.events()) - before; n != 0 {
					t.Errorf("wrote %d events", n)
				}
				return
			}
			if err != nil {
				t.Fatalf("UpdateTask: %+v", err)
			}
			names := []string{}
			for _, a := range cardByID(t, e.view("admin", e.lobby.ID), p.Tasks[0].ID).Assignees {
				names = append(names, a.Username)
			}
			if !reflect.DeepEqual(names, tt.want) {
				t.Errorf("assignees = %v, want %v", names, tt.want)
			}
		})
	}
}
