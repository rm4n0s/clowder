package app_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/errors"
)

// stage makes the user upload a file for a later request and fails the test
// on an error.
func (e *postsEnv) stage(user, name, content string) app.StagedUpload {
	e.t.Helper()
	s, err := e.posts.UploadStaged(e.t.Context(), e.as(user), name, strings.NewReader(content))
	if err != nil {
		e.t.Fatalf("UploadStaged %q: %+v", name, err)
	}
	return s
}

func TestPostsUploadStaged(t *testing.T) {
	t.Parallel()
	e := newPostsEnv(t)
	for _, user := range []string{"admin", "root", "alice", "bob", "carol", "dave"} {
		t.Run(user, func(t *testing.T) {
			s, err := e.posts.UploadStaged(t.Context(), e.as(user), "../dir/a.txt", strings.NewReader("hello"))
			if err != nil {
				t.Fatalf("got %+v, want success", err)
			}
			if s.ID != s.Upload.Key || len(s.ID) != 64 {
				t.Errorf("ID = %q, upload key %q, want the same 64 characters", s.ID, s.Upload.Key)
			}
			if s.Upload.FileName != "a.txt" || s.Upload.Size != 5 || s.Upload.ContentType == "" {
				t.Errorf("upload = %+v", s.Upload)
			}
			if _, err := e.files.Note(s.ID); err != nil {
				t.Errorf("no note kept: %+v", err)
			}
		})
	}
	t.Run("nobody", func(t *testing.T) {
		before := len(e.onDisk())
		_, err := e.posts.UploadStaged(t.Context(), app.Actor{}, "a.txt", strings.NewReader("x"))
		_ = requireTag(t, err, app.TagUnauthenticated, "Posts.UploadStaged->Posts.Upload.Unauthenticated")
		if got := len(e.onDisk()); got != before {
			t.Errorf("a refused upload left %d files", got-before)
		}
	})
	t.Run("over the limit", func(t *testing.T) {
		_, err := e.posts.UploadStaged(t.Context(), e.as("bob"), "big.bin", strings.NewReader(strings.Repeat("x", testMaxUpload+1)))
		_ = requireTag(t, err, app.TagTooLarge, "Posts.UploadStaged->Posts.Upload")
	})
	t.Run("the note cannot be kept", func(t *testing.T) {
		e := newPostsEnv(t)
		if err := os.WriteFile(filepath.Join(e.filesDir, "notes"), []byte("a file"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := e.posts.UploadStaged(t.Context(), e.as("bob"), "a.txt", strings.NewReader("x"))
		_ = requireTag(t, err, "FileNoteDirFailed", "Posts.UploadStaged->Store.PutNote.FileNoteDirFailed")
		if got := e.onDisk(); len(got) != 0 {
			t.Errorf("the file stayed on disk: %v", got)
		}
	})
}

func TestPostsStagedUploads(t *testing.T) {
	t.Parallel()
	e := newPostsEnv(t)
	mine := e.stage("bob", "mine.txt", "mine")
	other := e.stage("alice", "other.txt", "other")
	damaged := e.stage("bob", "damaged.txt", "damaged")
	if err := e.files.PutNote(damaged.ID, []byte("not a note")); err != nil {
		t.Fatal(err)
	}
	wrongScope := e.stage("bob", "scope.txt", "scope")
	if err := e.files.PutNote(wrongScope.ID, []byte(strings.Join([]string{"clowder-upload-1", "messages", "1", "1700000000", "5", "0", "0", "text/plain", "scope.txt"}, "\n"))); err != nil {
		t.Fatal(err)
	}
	noSuchFile := strings.Repeat("0", 64)

	tests := []struct {
		name string
		user string
		ids  []string
		// ok means success with these keys; otherwise the call fails with
		// "Upload the files again."
		ok   bool
		keys []string
	}{
		{"the uploader", "bob", []string{mine.ID}, true, []string{mine.ID}},
		{"nothing", "bob", nil, true, nil},
		{"the uploader, twice listed", "bob", []string{mine.ID, mine.ID}, true, []string{mine.ID, mine.ID}},
		{"another member's upload", "alice", []string{mine.ID}, false, nil},
		{"an admin gets nobody else's upload", "admin", []string{mine.ID}, false, nil},
		{"a member of nothing", "carol", []string{mine.ID}, false, nil},
		{"one of the two is another's", "bob", []string{mine.ID, other.ID}, false, nil},
		{"an ID that is not a key", "bob", []string{"../x"}, false, nil},
		{"an empty ID", "bob", []string{""}, false, nil},
		{"a key with no file", "bob", []string{noSuchFile}, false, nil},
		{"a damaged note", "bob", []string{damaged.ID}, false, nil},
		{"a note of the messages", "bob", []string{wrongScope.ID}, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := e.posts.StagedUploads(t.Context(), e.as(tt.user), tt.ids)
			if tt.ok {
				if err != nil {
					t.Fatalf("got %+v, want success", err)
				}
				var keys []string
				for _, u := range got {
					keys = append(keys, u.Key)
				}
				if !slices.Equal(keys, tt.keys) {
					t.Errorf("keys = %v, want %v", keys, tt.keys)
				}
				return
			}
			appErr := requireTag(t, err, app.TagInvalidInput, "Posts.StagedUploads->stagedUploads.InvalidInput")
			if got := fieldsOf(t, appErr)[app.FieldAttachments]; got != "Upload the files again." {
				t.Errorf("fields = %v", appErr.Metadata["fields"])
			}
		})
	}
	t.Run("what the note says comes back", func(t *testing.T) {
		got, err := e.posts.StagedUploads(t.Context(), e.as("bob"), []string{mine.ID})
		if err != nil || len(got) != 1 || got[0] != mine.Upload {
			t.Errorf("got %+v, %v, want %+v", got, err, mine.Upload)
		}
	})
	t.Run("an image keeps its size in pixels", func(t *testing.T) {
		s, err := e.posts.UploadStaged(t.Context(), e.as("bob"), "dot.png", strings.NewReader(string(pngData(t, 3, 2))))
		if err != nil {
			t.Fatal(err)
		}
		got, err := e.posts.StagedUploads(t.Context(), e.as("bob"), []string{s.ID})
		if err != nil || len(got) != 1 || got[0].Width != 3 || got[0].Height != 2 || got[0].ContentType != "image/png" {
			t.Errorf("got %+v, %v", got, err)
		}
	})
	t.Run("nobody", func(t *testing.T) {
		_, err := e.posts.StagedUploads(t.Context(), app.Actor{}, []string{mine.ID})
		_ = requireTag(t, err, app.TagUnauthenticated, "Posts.StagedUploads->stagedUploads.Unauthenticated")
	})
	t.Run("the disk fails", func(t *testing.T) {
		e := newPostsEnv(t)
		s := e.stage("bob", "a.txt", "a")
		notes := filepath.Join(e.filesDir, "notes")
		if err := os.Remove(filepath.Join(notes, s.ID)); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(notes, s.ID), 0o750); err != nil {
			t.Fatal(err)
		}
		_, err := e.posts.StagedUploads(t.Context(), e.as("bob"), []string{s.ID})
		_ = requireTag(t, err, "FileNoteReadFailed", "Posts.StagedUploads->stagedUploads->Store.Note.FileNoteReadFailed")
	})
}

func TestPostsStagedUploadsBecomeAttachments(t *testing.T) {
	t.Parallel()
	e := newPostsEnv(t)
	first := e.stage("bob", "first.txt", "first")
	second := e.stage("bob", "second.txt", "second")
	uploads, err := e.posts.StagedUploads(t.Context(), e.as("bob"), []string{first.ID, second.ID})
	if err != nil {
		t.Fatal(err)
	}
	post, err := e.posts.Create(t.Context(), e.as("bob"), e.room("lobby").ID,
		app.PostInput{Format: app.FormatText, Body: "files", Uploads: uploads})
	if err != nil {
		t.Fatalf("Create: %+v", err)
	}
	e.posts.ForgetStaged(t.Context(), e.as("bob"), uploads)
	if got := attachmentNames(post); !slices.Equal(got, []string{"first.txt", "second.txt"}) {
		t.Errorf("attachments = %v", got)
	}
	// The notes are gone: the same IDs are no uploads any more.
	for _, s := range []app.StagedUpload{first, second} {
		if _, err := e.files.Note(s.ID); err == nil {
			t.Errorf("the note of %s is still there", s.Upload.FileName)
		}
	}
	_, err = e.posts.StagedUploads(t.Context(), e.as("bob"), []string{first.ID})
	_ = requireTag(t, err, app.TagInvalidInput, "Posts.StagedUploads->stagedUploads.InvalidInput")
	e.requireOnDisk(first.Upload, second.Upload)
}

func TestPostsStagedUploadOfAFailedCall(t *testing.T) {
	t.Parallel()
	e := newPostsEnv(t)
	s := e.stage("bob", "a.txt", "a")
	uploads, err := e.posts.StagedUploads(t.Context(), e.as("bob"), []string{s.ID})
	if err != nil {
		t.Fatal(err)
	}
	// A post of a room bob is not a member of fails; the call removes the file.
	_, err = e.posts.Create(t.Context(), e.as("bob"), e.room("den").ID+1000,
		app.PostInput{Format: app.FormatText, Body: "x", Uploads: uploads})
	_ = requireTag(t, err, app.TagNotFound, "Posts.Create")
	e.posts.ForgetStaged(t.Context(), e.as("bob"), uploads)
	e.requireOnDisk()
	if _, err := e.files.Note(s.ID); err == nil {
		t.Error("the note is still there")
	}
}

func TestPostsForgetStaged(t *testing.T) {
	t.Parallel()
	e := newPostsEnv(t)
	s := e.stage("bob", "a.txt", "a")
	e.posts.ForgetStaged(t.Context(), app.Actor{}, []app.Upload{s.Upload})
	if _, err := e.files.Note(s.ID); err != nil {
		t.Errorf("nobody removed the note: %+v", err)
	}
	e.posts.ForgetStaged(t.Context(), e.as("bob"), []app.Upload{s.Upload})
	_, err := e.files.Note(s.ID)
	appErr := requireTag(t, err, files.TagNotFound, "Store.Note.FileNotFound")
	_ = appErr
	// Forgetting again, and a key that is no note, are no errors.
	e.posts.ForgetStaged(t.Context(), e.as("bob"), []app.Upload{s.Upload, {Key: "bad"}})
}

func TestPostsSweepStaged(t *testing.T) {
	t.Parallel()
	const day = 24 * time.Hour
	t.Run("old uploads go and fresh ones stay", func(t *testing.T) {
		e := newPostsEnv(t)
		old := e.stage("bob", "old.txt", "old")
		e.clk.Advance(day + time.Minute)
		fresh := e.stage("alice", "fresh.txt", "fresh")
		e.clk.Advance(time.Hour)
		n, err := e.posts.SweepStaged(t.Context(), day)
		if err != nil || n != 1 {
			t.Fatalf("SweepStaged = %d, %v, want 1", n, err)
		}
		e.requireOnDisk(fresh.Upload)
		if _, err := e.files.Note(old.ID); err == nil {
			t.Error("the note of the old upload is still there")
		}
		if _, err := e.posts.StagedUploads(t.Context(), e.as("alice"), []string{fresh.ID}); err != nil {
			t.Errorf("the fresh upload is gone: %+v", err)
		}
		_, err = e.posts.StagedUploads(t.Context(), e.as("bob"), []string{old.ID})
		_ = requireTag(t, err, app.TagInvalidInput, "Posts.StagedUploads->stagedUploads.InvalidInput")
		// Nothing is left to do, and a second sweep says so.
		if n, err := e.posts.SweepStaged(t.Context(), day); err != nil || n != 0 {
			t.Errorf("second sweep = %d, %v", n, err)
		}
	})
	t.Run("the age is the one in the note", func(t *testing.T) {
		e := newPostsEnv(t)
		e.stage("bob", "a.txt", "a")
		e.clk.Advance(day - time.Minute)
		if n, err := e.posts.SweepStaged(t.Context(), day); err != nil || n != 0 {
			t.Errorf("just under a day: %d, %v", n, err)
		}
		e.clk.Advance(2 * time.Minute)
		if n, err := e.posts.SweepStaged(t.Context(), day); err != nil || n != 1 {
			t.Errorf("just over a day: %d, %v", n, err)
		}
	})
	t.Run("an upload that a post took is never swept", func(t *testing.T) {
		e := newPostsEnv(t)
		s := e.stage("bob", "taken.txt", "taken")
		uploads, err := e.posts.StagedUploads(t.Context(), e.as("bob"), []string{s.ID})
		if err != nil {
			t.Fatal(err)
		}
		post := e.writeWith("bob", "lobby", "with a file", uploads...)
		e.posts.ForgetStaged(t.Context(), e.as("bob"), uploads)
		e.clk.Advance(10 * day)
		if n, err := e.posts.SweepStaged(t.Context(), day); err != nil || n != 0 {
			t.Fatalf("SweepStaged = %d, %v", n, err)
		}
		e.requireOnDisk(s.Upload)
		if _, _, err := e.posts.Attachment(t.Context(), e.as("bob"), post.Attachments[0].ID); err != nil {
			t.Errorf("the attachment is gone: %+v", err)
		}
	})
	t.Run("a note left behind by a taken upload is ended, the file stays", func(t *testing.T) {
		e := newPostsEnv(t)
		s := e.stage("bob", "taken.txt", "taken")
		uploads, err := e.posts.StagedUploads(t.Context(), e.as("bob"), []string{s.ID})
		if err != nil {
			t.Fatal(err)
		}
		e.writeWith("bob", "lobby", "with a file", uploads...) // ForgetStaged never ran
		e.clk.Advance(2 * day)
		if n, err := e.posts.SweepStaged(t.Context(), day); err != nil || n != 0 {
			t.Fatalf("SweepStaged = %d, %v", n, err)
		}
		e.requireOnDisk(s.Upload)
		if _, err := e.files.Note(s.ID); err == nil {
			t.Error("the note is still there")
		}
	})
	t.Run("a file that is gone already is no error", func(t *testing.T) {
		e := newPostsEnv(t)
		s := e.stage("bob", "gone.txt", "gone")
		if err := e.files.Remove(s.ID); err != nil {
			t.Fatal(err)
		}
		e.clk.Advance(2 * day)
		if n, err := e.posts.SweepStaged(t.Context(), day); err != nil || n != 1 {
			t.Errorf("SweepStaged = %d, %v", n, err)
		}
		if _, err := e.files.Note(s.ID); err == nil {
			t.Error("the note is still there")
		}
	})
	t.Run("the notes of the messages are swept too", func(t *testing.T) {
		e := newPostsEnv(t)
		up := e.uploadText("bob", "message.txt")
		note := strings.Join([]string{"clowder-upload-1", "messages", "1", strconv.FormatInt(e.clk.Now().Unix(), 10), "7", "0", "0", "text/plain", "message.txt"}, "\n")
		if err := e.files.PutNote(up.Key, []byte(note)); err != nil {
			t.Fatal(err)
		}
		e.clk.Advance(2 * day)
		if n, err := e.posts.SweepStaged(t.Context(), day); err != nil || n != 1 {
			t.Fatalf("SweepStaged = %d, %v", n, err)
		}
		e.requireOnDisk()
	})
	t.Run("a damaged note counts from when it was written", func(t *testing.T) {
		e := newPostsEnv(t)
		oldUp, freshUp := e.uploadText("bob", "old.txt"), e.uploadText("bob", "fresh.txt")
		for _, u := range []app.Upload{oldUp, freshUp} {
			if err := e.files.PutNote(u.Key, []byte("junk")); err != nil {
				t.Fatal(err)
			}
		}
		stale := e.clk.Now().Add(-2 * day)
		if err := os.Chtimes(filepath.Join(e.filesDir, "notes", oldUp.Key), stale, stale); err != nil {
			t.Fatal(err)
		}
		e.clk.Set(time.Now()) // the files' times are the real ones
		if n, err := e.posts.SweepStaged(t.Context(), day); err != nil || n != 1 {
			t.Fatalf("SweepStaged = %d, %v", n, err)
		}
		e.requireOnDisk(freshUp)
	})
	t.Run("nothing staged", func(t *testing.T) {
		e := newPostsEnv(t)
		if n, err := e.posts.SweepStaged(t.Context(), day); err != nil || n != 0 {
			t.Errorf("SweepStaged = %d, %v", n, err)
		}
	})
	t.Run("sweeps at the same time", func(t *testing.T) {
		e := newPostsEnv(t)
		for i := range 20 {
			e.stage("bob", fmt.Sprintf("f%d.txt", i), "x")
		}
		e.clk.Advance(2 * day)
		var wg sync.WaitGroup
		errs := make([]error, 6)
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = e.posts.SweepStaged(t.Context(), day)
			}()
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("sweep %d failed: %+v", i, err)
			}
		}
		e.requireOnDisk()
		if notes, err := e.files.Notes(); err != nil || len(notes) != 0 {
			t.Errorf("notes left: %v, %v", notes, err)
		}
	})
	t.Run("the context ends the sweep", func(t *testing.T) {
		e := newPostsEnv(t)
		e.stage("bob", "a.txt", "a")
		e.clk.Advance(2 * day)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := e.posts.SweepStaged(ctx, day)
		appErr := requireTag(t, err, app.TagSweepCanceled, "Posts.SweepStaged.StagedSweepCanceled")
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the cause is lost: %v", appErr)
		}
		if len(e.onDisk()) != 1 {
			t.Errorf("a canceled sweep removed files: %v", e.onDisk())
		}
	})
	t.Run("the notes cannot be listed", func(t *testing.T) {
		e := newPostsEnv(t)
		if err := os.WriteFile(filepath.Join(e.filesDir, "notes"), []byte("a file"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := e.posts.SweepStaged(t.Context(), day)
		_ = requireTag(t, err, "FileNoteListFailed", "Posts.SweepStaged->Store.Notes.FileNoteListFailed")
	})
}
