package store

import (
	"reflect"
	"testing"
	"time"

	"github.com/rm4n0s/errors"
)

func TestTaskQueries(t *testing.T) {
	f := newFix(t)
	alice, bob, carol := f.user(), f.user(), f.user()
	room, other := f.room(alice), f.room(alice)
	for _, u := range []*User{alice, bob} {
		f.create(&RoomMember{RoomID: room.ID, UserID: u.ID})
	}
	todo, doing, done := f.column(room), f.column(room), f.column(room)
	otherCol := f.column(other)
	post := f.post(room, alice)
	comment := f.comment(post, bob)
	otherPost := f.post(other, alice)

	mk := func(p *Post, col *BoardColumn, title string, pos int, due *time.Time) *Task {
		t.Helper()
		task := &Task{RoomID: p.RoomID, PostID: p.ID, ColumnID: col.ID, Position: pos, Title: title, DueDate: due}
		if err := f.s.CreateTask(f.ctx, task); err != nil || task.ID == 0 {
			t.Fatalf("CreateTask %s: %v", title, err)
		}
		return task
	}
	due := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	a := mk(post, todo, "a", 1, &due)
	b := mk(comment, todo, "b", 0, nil)
	c := mk(post, doing, "c", 0, nil)
	d := mk(post, done, "d", 0, nil)
	mk(otherPost, otherCol, "elsewhere", 0, nil)
	if err := f.s.ReplaceTaskAssignees(f.ctx, a.ID, []uint64{bob.ID, alice.ID}); err != nil {
		t.Fatalf("ReplaceTaskAssignees: %v", err)
	}

	t.Run("MemberUsernames", func(t *testing.T) {
		got, err := f.s.MemberUsernames(f.ctx, room.ID, []uint64{alice.ID, bob.ID, carol.ID, 9999})
		want := map[uint64]string{alice.ID: alice.Username, bob.ID: bob.Username}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("MemberUsernames = %v, %v, want %v", got, err, want)
		}
		if got, err := f.s.MemberUsernames(f.ctx, room.ID, nil); err != nil || len(got) != 0 {
			t.Errorf("MemberUsernames(nil) = %v, %v", got, err)
		}
	})

	t.Run("TaskByID", func(t *testing.T) {
		got, err := f.s.TaskByID(f.ctx, a.ID)
		if err != nil || got.Title != "a" || got.DueDate == nil || !got.DueDate.Equal(due) {
			t.Errorf("TaskByID = %+v, %v", got, err)
		}
		_, err = f.s.TaskByID(f.ctx, 9999)
		if appErr, ok := errors.FromError(err); !ok || appErr.Tag != TagNotFound {
			t.Errorf("TaskByID of a missing task = %v", err)
		}
	})

	t.Run("TaskRowsOfRoom", func(t *testing.T) {
		rows, err := f.s.TaskRowsOfRoom(f.ctx, room.ID)
		if err != nil {
			t.Fatalf("TaskRowsOfRoom: %v", err)
		}
		var got []string
		for _, r := range rows {
			got = append(got, r.Title)
		}
		// By column, then by position: b (0) before a (1) in the first one.
		if want := []string{"b", "a", "c", "d"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("titles = %v, want %v", got, want)
		}
		wantRoot := map[string]uint64{"a": post.ID, "b": post.ID, "c": post.ID, "d": post.ID}
		wantLast := map[string]bool{"a": false, "b": false, "c": false, "d": true}
		wantColumn := map[string]string{"a": todo.Name, "b": todo.Name, "c": doing.Name, "d": done.Name}
		for _, r := range rows {
			if r.RootID != wantRoot[r.Title] || r.ColumnLast != wantLast[r.Title] || r.ColumnName != wantColumn[r.Title] || r.RoomID != room.ID {
				t.Errorf("row %s = %+v", r.Title, r)
			}
		}
		if rows[1].DueDate == nil || !rows[1].DueDate.Equal(due) || rows[0].DueDate != nil {
			t.Errorf("due dates = %v, %v", rows[1].DueDate, rows[0].DueDate)
		}
		if len(rows[1].Assignees) != 2 || rows[1].Assignees[0].UserID != bob.ID || rows[1].Assignees[1].Username != alice.Username || len(rows[0].Assignees) != 0 {
			t.Errorf("assignees = %+v / %+v", rows[1].Assignees, rows[0].Assignees)
		}
		// The task of a comment has the post of its thread as root.
		if rows[0].PostID != comment.ID {
			t.Errorf("b is the task of post %d, want the comment %d", rows[0].PostID, comment.ID)
		}
		empty, err := f.s.TaskRowsOfRoom(f.ctx, 9999)
		if err != nil || len(empty) != 0 {
			t.Errorf("TaskRowsOfRoom of nothing = %v, %v", empty, err)
		}
	})

	t.Run("TaskRowsOfPosts", func(t *testing.T) {
		got, err := f.s.TaskRowsOfPosts(f.ctx, []uint64{post.ID, comment.ID, 9999})
		if err != nil || len(got) != 2 {
			t.Fatalf("TaskRowsOfPosts = %v, %v", got, err)
		}
		if len(got[post.ID]) != 3 || got[post.ID][0].ID != a.ID || got[post.ID][1].ID != c.ID || got[post.ID][2].ID != d.ID {
			t.Errorf("tasks of the post = %+v", got[post.ID])
		}
		if len(got[comment.ID]) != 1 || got[comment.ID][0].RootID != post.ID {
			t.Errorf("tasks of the comment = %+v", got[comment.ID])
		}
		if got, err := f.s.TaskRowsOfPosts(f.ctx, nil); err != nil || len(got) != 0 {
			t.Errorf("TaskRowsOfPosts(nil) = %v, %v", got, err)
		}
	})

	t.Run("placement, fields and assignees", func(t *testing.T) {
		if err := f.s.SetTaskPlacement(f.ctx, c.ID, todo.ID, 5); err != nil {
			t.Fatalf("SetTaskPlacement: %v", err)
		}
		got, err := f.s.ListTasksOfColumn(f.ctx, todo.ID)
		if err != nil || len(got) != 3 || got[2].ID != c.ID || got[2].Position != 5 {
			t.Errorf("ListTasksOfColumn = %+v, %v", got, err)
		}
		if err := f.s.UpdateTaskFields(f.ctx, a.ID, "a2", nil); err != nil {
			t.Fatalf("UpdateTaskFields: %v", err)
		}
		again, err := f.s.TaskByID(f.ctx, a.ID)
		if err != nil || again.Title != "a2" || again.DueDate != nil {
			t.Errorf("task = %+v, %v", again, err)
		}
		if err := f.s.ReplaceTaskAssignees(f.ctx, a.ID, nil); err != nil {
			t.Fatalf("ReplaceTaskAssignees: %v", err)
		}
		if n := f.count("task_assignees"); n != 0 {
			t.Errorf("%d assignees left", n)
		}
		for name, call := range map[string]func() error{
			"SetTaskPlacement":  func() error { return f.s.SetTaskPlacement(f.ctx, 9999, todo.ID, 0) },
			"UpdateTaskFields":  func() error { return f.s.UpdateTaskFields(f.ctx, 9999, "x", nil) },
			"UpdateColumnName":  func() error { return f.s.UpdateBoardColumnName(f.ctx, 9999, "x") },
			"SetColumnPosition": func() error { return f.s.SetBoardColumnPosition(f.ctx, 9999, 0) },
			"DeleteColumn":      func() error { return f.s.DeleteBoardColumn(f.ctx, 9999) },
		} {
			if appErr, ok := errors.FromError(call()); !ok || appErr.Tag != TagNotFound {
				t.Errorf("%s of a missing row did not fail with NotFound", name)
			}
		}
	})

	t.Run("columns", func(t *testing.T) {
		if err := f.s.UpdateBoardColumnName(f.ctx, doing.ID, "Review"); err != nil {
			t.Fatalf("UpdateBoardColumnName: %v", err)
		}
		if err := f.s.SetBoardColumnPosition(f.ctx, doing.ID, 99); err != nil {
			t.Fatalf("SetBoardColumnPosition: %v", err)
		}
		got, err := f.s.BoardColumnByID(f.ctx, doing.ID)
		if err != nil || got.Name != "Review" || got.Position != 99 {
			t.Errorf("column = %+v, %v", got, err)
		}
		if _, err := f.s.BoardColumnByID(f.ctx, 9999); err == nil {
			t.Error("BoardColumnByID of a missing column succeeded")
		}
		empty := f.column(room)
		if err := f.s.DeleteBoardColumn(f.ctx, empty.ID); err != nil {
			t.Errorf("DeleteBoardColumn: %v", err)
		}
	})

	t.Run("DeleteTasks", func(t *testing.T) {
		if err := f.s.DeleteTasks(f.ctx, nil); err != nil {
			t.Errorf("DeleteTasks(nil): %v", err)
		}
		f.exec("INSERT INTO task_assignees (task_id, user_id, created_at) VALUES (?, ?, ?)", b.ID, bob.ID, fakeStart)
		if err := f.s.DeleteTasks(f.ctx, []uint64{b.ID, d.ID, 9999}); err != nil {
			t.Fatalf("DeleteTasks: %v", err)
		}
		if _, err := f.s.TaskByID(f.ctx, b.ID); err == nil {
			t.Error("the task is still there")
		}
		if n := f.count("task_assignees"); n != 0 {
			t.Errorf("%d assignees left", n)
		}
	})
}
