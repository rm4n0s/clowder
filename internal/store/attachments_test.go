package store

import (
	"slices"
	"testing"
)

func TestAttachmentQueries(t *testing.T) {
	f := newFix(t)
	alice := f.user()
	room, other := f.room(alice), f.room(alice)
	p1, p2, p3 := f.post(room, alice), f.post(room, alice), f.post(other, alice)
	c1 := f.comment(p1, alice)

	t.Run("CreateAttachments sets IDs and reads back", func(t *testing.T) {
		rows := []Attachment{
			{PostID: p1.ID, UploaderID: alice.ID, FileName: "a.png", ContentType: "image/png", Size: 10, StorageKey: "k1", Width: ptr(3), Height: ptr(4)},
			{PostID: p1.ID, UploaderID: alice.ID, FileName: "b.txt", ContentType: "text/plain", Size: 0, StorageKey: "k2"},
			{PostID: p2.ID, UploaderID: alice.ID, FileName: "c.txt", ContentType: "text/plain", Size: 5, StorageKey: "k3"},
			{PostID: c1.ID, UploaderID: alice.ID, FileName: "d.txt", ContentType: "text/plain", Size: 5, StorageKey: "k4"},
			{PostID: p3.ID, UploaderID: alice.ID, FileName: "e.txt", ContentType: "text/plain", Size: 5, StorageKey: "k5"},
		}
		if err := f.s.CreateAttachments(f.ctx, rows); err != nil {
			t.Fatalf("CreateAttachments: %+v", err)
		}
		for _, r := range rows {
			if r.ID == 0 {
				t.Fatalf("no ID on %+v", r)
			}
		}
		got, err := f.s.AttachmentByID(f.ctx, rows[0].ID)
		if err != nil || got.FileName != "a.png" || got.Width == nil || *got.Width != 3 || *got.Height != 4 || got.StorageKey != "k1" {
			t.Fatalf("AttachmentByID = %+v, %v", got, err)
		}
		got, err = f.s.AttachmentByID(f.ctx, rows[1].ID)
		if err != nil || got.Width != nil || got.Height != nil {
			t.Fatalf("AttachmentByID = %+v, %v", got, err)
		}
		if err := f.s.CreateAttachments(f.ctx, nil); err != nil {
			t.Errorf("CreateAttachments(nil) = %v", err)
		}
	})

	t.Run("AttachmentByID of a missing row", func(t *testing.T) {
		_, err := f.s.AttachmentByID(f.ctx, 9999)
		appErr := requireRoute(t, err, "Store.AttachmentByID.NotFound")
		if appErr.Tag != TagNotFound {
			t.Errorf("tag = %s", appErr.Tag)
		}
	})

	t.Run("a storage key is used once", func(t *testing.T) {
		err := f.s.CreateAttachments(f.ctx, []Attachment{
			{PostID: p2.ID, UploaderID: alice.ID, FileName: "x", ContentType: "text/plain", StorageKey: "k1"},
		})
		appErr := requireRoute(t, err, "Store.CreateAttachments.Conflict")
		if appErr.Tag != TagConflict {
			t.Errorf("tag = %s", appErr.Tag)
		}
	})

	t.Run("a missing post fails", func(t *testing.T) {
		err := f.s.CreateAttachments(f.ctx, []Attachment{
			{PostID: 9999, UploaderID: alice.ID, FileName: "x", ContentType: "text/plain", StorageKey: "k9"},
		})
		_ = requireRoute(t, err, "Store.CreateAttachments.QueryFailed")
	})

	t.Run("AttachmentsOfPosts groups and orders", func(t *testing.T) {
		got, err := f.s.AttachmentsOfPosts(f.ctx, []uint64{p1.ID, p2.ID, p3.ID, 9999})
		if err != nil {
			t.Fatalf("AttachmentsOfPosts: %+v", err)
		}
		names := func(id uint64) []string {
			var out []string
			for _, a := range got[id] {
				out = append(out, a.FileName)
			}
			return out
		}
		if !slices.Equal(names(p1.ID), []string{"a.png", "b.txt"}) || !slices.Equal(names(p2.ID), []string{"c.txt"}) ||
			!slices.Equal(names(p3.ID), []string{"e.txt"}) || len(got[9999]) != 0 {
			t.Errorf("got %v", got)
		}
		if m, err := f.s.AttachmentsOfPosts(f.ctx, nil); err != nil || len(m) != 0 {
			t.Errorf("AttachmentsOfPosts(nil) = %v, %v", m, err)
		}
	})

	t.Run("StorageKeysInUse", func(t *testing.T) {
		got, err := f.s.StorageKeysInUse(f.ctx, []string{"k1", "k3", "nope"})
		if err != nil {
			t.Fatalf("StorageKeysInUse: %+v", err)
		}
		slices.Sort(got)
		if !slices.Equal(got, []string{"k1", "k3"}) {
			t.Errorf("got %v", got)
		}
		if got, err := f.s.StorageKeysInUse(f.ctx, nil); err != nil || len(got) != 0 {
			t.Errorf("StorageKeysInUse(nil) = %v, %v", got, err)
		}
	})

	t.Run("AttachmentKeysOfRoom includes comments and skips other rooms", func(t *testing.T) {
		got, err := f.s.AttachmentKeysOfRoom(f.ctx, room.ID)
		if err != nil {
			t.Fatalf("AttachmentKeysOfRoom: %+v", err)
		}
		slices.Sort(got)
		if !slices.Equal(got, []string{"k1", "k2", "k3", "k4"}) {
			t.Errorf("got %v", got)
		}
	})

	t.Run("DeleteAttachments", func(t *testing.T) {
		m, err := f.s.AttachmentsOfPosts(f.ctx, []uint64{p1.ID})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.s.DeleteAttachments(f.ctx, []uint64{m[p1.ID][0].ID, 9999}); err != nil {
			t.Fatalf("DeleteAttachments: %+v", err)
		}
		m, err = f.s.AttachmentsOfPosts(f.ctx, []uint64{p1.ID})
		if err != nil || len(m[p1.ID]) != 1 || m[p1.ID][0].FileName != "b.txt" {
			t.Errorf("after: %v, %v", m, err)
		}
		if err := f.s.DeleteAttachments(f.ctx, nil); err != nil {
			t.Errorf("DeleteAttachments(nil) = %v", err)
		}
	})
}
