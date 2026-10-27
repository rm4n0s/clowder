package store

import (
	"reflect"
	"testing"
)

func TestBulkCreate(t *testing.T) {
	t.Run("writes rows with their IDs in batches", func(t *testing.T) {
		f := newFix(t)
		author := f.user()
		room := f.room(author)
		const n = bulkBatchSize*2 + 7
		first, err := NextID[Post](f.ctx, f.s)
		if err != nil || first != 1 {
			t.Fatalf("NextID of an empty table = %d, %v; want 1", first, err)
		}
		posts := make([]Post, n)
		for i := range posts {
			posts[i] = Post{ID: first + uint64(i), RoomID: room.ID, AuthorID: author.ID, Format: FormatText, Body: "bulk"}
		}
		if err := BulkCreate(f.ctx, f.s, posts); err != nil {
			t.Fatalf("BulkCreate: %+v", err)
		}
		if got := f.count("posts"); got != n {
			t.Errorf("posts = %d, want %d", got, n)
		}
		next, err := NextID[Post](f.ctx, f.s)
		if err != nil || next != n+1 {
			t.Errorf("NextID = %d, %v; want %d", next, err, n+1)
		}
		var last Post
		if err := f.s.db(f.ctx).First(&last, n).Error; err != nil {
			t.Fatalf("the last row is missing: %v", err)
		}
		requireSameTime(t, "CreatedAt", fakeStart, last.CreatedAt)
	})

	t.Run("an empty list does nothing", func(t *testing.T) {
		f := newFix(t)
		if err := BulkCreate(f.ctx, f.s, []Post(nil)); err != nil {
			t.Errorf("BulkCreate(nil) = %+v", err)
		}
	})

	tests := []struct {
		name  string
		rows  func(f *fix) []User
		route string
	}{
		{
			name: "a taken primary key is a conflict",
			rows: func(f *fix) []User {
				u := f.user()
				return []User{{ID: u.ID, Username: "other", DisplayName: "Other", PasswordHash: "h", Role: RoleMember}}
			},
			route: "BulkCreate[...].Conflict",
		},
		{
			name: "a row the table refuses is a failed query",
			rows: func(*fix) []User {
				return []User{{Username: "UPPER", DisplayName: "Upper", PasswordHash: "h", Role: RoleMember}}
			},
			route: "BulkCreate[...].QueryFailed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFix(t)
			err := BulkCreate(f.ctx, f.s, tt.rows(f))
			_ = requireRoute(t, err, tt.route)
		})
	}
}

func TestNextIDFailsOnAClosedStore(t *testing.T) {
	f := newFix(t)
	if err := f.s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err := NextID[Post](f.ctx, f.s)
	_ = requireRoute(t, err, "NextID[...].QueryFailed")
}

func TestListTags(t *testing.T) {
	f := newFix(t)
	if err := BulkCreate(f.ctx, f.s, []Tag{{ID: 5, Name: "b"}, {ID: 2, Name: "a"}}); err != nil {
		t.Fatalf("BulkCreate: %+v", err)
	}
	tags, err := f.s.ListTags(f.ctx)
	if err != nil {
		t.Fatalf("ListTags: %+v", err)
	}
	var names []string
	for _, tag := range tags {
		names = append(names, tag.Name)
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(names, want) {
		t.Errorf("ListTags = %v, want %v (by ID)", names, want)
	}
	if err := f.s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err = f.s.ListTags(f.ctx)
	_ = requireRoute(t, err, "ListTags.QueryFailed")
}

func TestRebuildSearchIndex(t *testing.T) {
	f := newFix(t)
	author := f.user()
	room := f.room(author)
	kept := f.post(room, author)
	deleted := f.post(room, author)
	f.exec("UPDATE posts SET deleted_at = ? WHERE id = ?", fakeStart, deleted.ID)
	withFile := f.post(room, author)
	f.create(&Attachment{PostID: withFile.ID, UploaderID: author.ID, FileName: "plan.txt", ContentType: "text/plain", Size: 1, StorageKey: "k1"})
	f.reindex(kept, withFile)
	want := f.indexRows()

	// Lose the index, then rebuild it: it holds what ReindexPost made.
	table, idColumn := f.indexTable()
	f.exec("DELETE FROM " + table)
	f.exec("INSERT INTO "+table+" ("+idColumn+", text) VALUES (?, 'stale')", deleted.ID)
	if err := f.s.RebuildSearchIndex(f.ctx); err != nil {
		t.Fatalf("RebuildSearchIndex: %+v", err)
	}
	if got := f.indexRows(); !reflect.DeepEqual(got, want) {
		t.Errorf("index = %v, want %v", got, want)
	}

	if err := f.s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_ = requireRoute(t, f.s.RebuildSearchIndex(f.ctx), "RebuildSearchIndex.QueryFailed")
}
