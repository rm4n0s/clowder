package store

import (
	"reflect"
	"testing"
)

// statsWorld is a small healthy database: two rooms with three columns each,
// a post with a comment and a task in the first room, all indexed.
type statsWorld struct {
	f            *fix
	alice, bob   *User
	room, other  *Room
	post, answer *Post
	columns      []BoardColumn
	otherColumns []BoardColumn
	task         *Task
}

func newStatsWorld(t *testing.T) *statsWorld {
	t.Helper()
	f := newFix(t)
	w := &statsWorld{f: f, alice: f.user(), bob: f.user()}
	w.room, w.other = f.room(w.alice), f.room(w.alice)
	for _, r := range []*Room{w.room, w.other} {
		f.join(r, w.alice)
	}
	f.join(w.room, w.bob)
	cols := func(r *Room) []BoardColumn {
		rows := []BoardColumn{{RoomID: r.ID, Name: "To do", Position: 0}, {RoomID: r.ID, Name: "Done", Position: 1}}
		if err := f.s.CreateBoardColumns(f.ctx, rows); err != nil {
			t.Fatalf("CreateBoardColumns: %+v", err)
		}
		return rows
	}
	w.columns, w.otherColumns = cols(w.room), cols(w.other)
	w.post = f.post(w.room, w.alice)
	w.answer = f.comment(w.post, w.bob)
	w.task = &Task{RoomID: w.room.ID, PostID: w.post.ID, ColumnID: w.columns[0].ID, Position: 0, Title: "ship"}
	f.create(w.task)
	f.reindex(w.post, w.answer)
	return w
}

func TestStats(t *testing.T) {
	t.Run("an empty database", func(t *testing.T) {
		f := newFix(t)
		got, err := f.s.Stats(f.ctx)
		if err != nil || !reflect.DeepEqual(got, Stats{}) {
			t.Errorf("Stats = %+v, %v; want zeros", got, err)
		}
	})

	t.Run("a healthy database", func(t *testing.T) {
		w := newStatsWorld(t)
		got, err := w.f.s.Stats(w.f.ctx)
		if err != nil {
			t.Fatalf("Stats: %+v", err)
		}
		want := Stats{
			Users: 2, Rooms: 2, Members: 3, Posts: 1, Comments: 1, Columns: 4, Tasks: 1, Indexed: 2,
		}
		if got != want {
			t.Errorf("Stats = %+v, want %+v", got, want)
		}
	})

	tests := []struct {
		name   string
		break_ func(w *statsWorld)
		check  func(s Stats) int64
	}{
		{"a post without an index row", func(w *statsWorld) {
			table, idColumn := w.f.indexTable()
			w.f.exec("DELETE FROM "+table+" WHERE "+idColumn+" = ?", w.post.ID)
		}, func(s Stats) int64 { return s.Unindexed }},
		{"an index row without a post", func(w *statsWorld) {
			table, idColumn := w.f.indexTable()
			w.f.exec("INSERT INTO " + table + " (" + idColumn + ", text) VALUES (999, 'stray')")
		}, func(s Stats) int64 { return s.StrayIndexRows }},
		{"a deleted post that is still indexed", func(w *statsWorld) {
			w.f.exec("UPDATE posts SET deleted_at = ? WHERE id = ?", fakeStart, w.answer.ID)
		}, func(s Stats) int64 { return s.StrayIndexRows }},
		{"a comment in another room than its root", func(w *statsWorld) {
			w.f.exec("UPDATE posts SET room_id = ? WHERE id = ?", w.other.ID, w.answer.ID)
		}, func(s Stats) int64 { return s.BadComments }},
		{"a comment whose root is a comment", func(w *statsWorld) {
			w.f.exec("UPDATE posts SET root_id = ? WHERE id = ?", w.answer.ID, w.answer.ID)
		}, func(s Stats) int64 { return s.BadComments }},
		{"an author who is not in the room", func(w *statsWorld) {
			w.f.exec("DELETE FROM room_members WHERE room_id = ? AND user_id = ?", w.room.ID, w.bob.ID)
		}, func(s Stats) int64 { return s.NonMemberAuthors }},
		{"a room without columns", func(w *statsWorld) {
			w.f.exec("DELETE FROM board_columns WHERE room_id = ?", w.other.ID)
		}, func(s Stats) int64 { return s.BadColumns }},
		{"columns with a gap", func(w *statsWorld) {
			w.f.exec("UPDATE board_columns SET position = 5 WHERE id = ?", w.columns[1].ID)
		}, func(s Stats) int64 { return s.BadColumns }},
		{"a task in the column of another room", func(w *statsWorld) {
			w.f.exec("UPDATE tasks SET column_id = ? WHERE id = ?", w.otherColumns[0].ID, w.task.ID)
		}, func(s Stats) int64 { return s.TasksInForeignColumn }},
		{"a task whose position starts at 1", func(w *statsWorld) {
			w.f.exec("UPDATE tasks SET position = 1 WHERE id = ?", w.task.ID)
		}, func(s Stats) int64 { return s.BadTaskPositions }},
		{"a membership without a mark", func(w *statsWorld) {
			w.f.exec("DELETE FROM room_reads WHERE room_id = ? AND user_id = ?", w.room.ID, w.bob.ID)
		}, func(s Stats) int64 { return s.MembersWithoutMark }},
		{"a mark without a membership", func(w *statsWorld) {
			w.f.exec("DELETE FROM room_members WHERE room_id = ? AND user_id = ?", w.other.ID, w.alice.ID)
		}, func(s Stats) int64 { return s.StrayMarks }},
		{"two tasks at one position", func(w *statsWorld) {
			w.f.create(&Task{RoomID: w.room.ID, PostID: w.post.ID, ColumnID: w.columns[0].ID, Position: 0, Title: "twin"})
		}, func(s Stats) int64 { return s.BadTaskPositions }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newStatsWorld(t)
			tt.break_(w)
			got, err := w.f.s.Stats(w.f.ctx)
			if err != nil {
				t.Fatalf("Stats: %+v", err)
			}
			if n := tt.check(got); n != 1 {
				t.Errorf("the check found %d rows, want 1; stats %+v", n, got)
			}
		})
	}

	t.Run("a closed store", func(t *testing.T) {
		f := newFix(t)
		if err := f.s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		_, err := f.s.Stats(f.ctx)
		_ = requireRoute(t, err, "Stats.QueryFailed")
	})
}

func TestRoomPostCounts(t *testing.T) {
	w := newStatsWorld(t)
	busy := w.f.room(w.alice)
	w.f.create(&RoomMember{RoomID: busy.ID, UserID: w.alice.ID})
	for range 3 {
		w.f.post(busy, w.alice)
	}
	got, err := w.f.s.RoomPostCounts(w.f.ctx)
	if err != nil {
		t.Fatalf("RoomPostCounts: %+v", err)
	}
	want := []RoomPostCount{{RoomID: busy.ID, Posts: 3}, {RoomID: w.room.ID, Posts: 2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RoomPostCounts = %+v, want %+v (the room without posts is left out)", got, want)
	}
	if err := w.f.s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err = w.f.s.RoomPostCounts(w.f.ctx)
	_ = requireRoute(t, err, "RoomPostCounts.QueryFailed")
}
