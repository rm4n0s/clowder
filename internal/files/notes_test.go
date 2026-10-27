package files_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/files"
)

func TestNotes(t *testing.T) {
	t.Parallel()
	s, dir := newStore(t)
	key := strings.Repeat("ab", 32)
	if _, err := os.Stat(filepath.Join(dir, "notes")); !os.IsNotExist(err) {
		t.Fatalf("the notes directory exists before a note is kept: %v", err)
	}
	if err := s.PutNote(key, []byte(`{"a":1}`)); err != nil {
		t.Fatalf("PutNote: %+v", err)
	}
	got, err := s.Note(key)
	if err != nil || string(got) != `{"a":1}` {
		t.Fatalf("Note = %q, %v", got, err)
	}
	// A note is replaced whole.
	if err := s.PutNote(key, []byte("second")); err != nil {
		t.Fatalf("PutNote again: %+v", err)
	}
	if got, err := s.Note(key); err != nil || string(got) != "second" {
		t.Fatalf("Note after a replace = %q, %v", got, err)
	}
	if got := names(t, filepath.Join(dir, "notes")); len(got) != 1 || got[0] != key {
		t.Errorf("the notes directory holds %v, want only the note", got)
	}
	info, err := os.Stat(filepath.Join(dir, "notes", key))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("note mode = %v, %v, want 0600", info.Mode(), err)
	}
	if err := s.RemoveNote(key); err != nil {
		t.Fatalf("RemoveNote: %+v", err)
	}
	_, err = s.Note(key)
	appErr := requireRoute(t, err, "Store.Note.FileNotFound")
	if appErr.Tag != files.TagNotFound {
		t.Errorf("tag = %s", appErr.Tag)
	}
	if err := s.RemoveNote(key); err != nil {
		t.Errorf("removing a note that is gone: %+v", err)
	}
}

func TestNotesRefuseBadKeys(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	for _, key := range []string{"", "../x", "abc", strings.Repeat("A", 64)} {
		err := s.PutNote(key, []byte("x"))
		if appErr := requireRoute(t, err, "Store.PutNote.FileKeyInvalid"); appErr.Tag != files.TagBadKey {
			t.Errorf("PutNote(%q) tag = %s", key, appErr.Tag)
		}
		_, err = s.Note(key)
		if appErr := requireRoute(t, err, "Store.Note.FileKeyInvalid"); appErr.Tag != files.TagBadKey {
			t.Errorf("Note(%q) tag = %s", key, appErr.Tag)
		}
		err = s.RemoveNote(key)
		if appErr := requireRoute(t, err, "Store.RemoveNote.FileKeyInvalid"); appErr.Tag != files.TagBadKey {
			t.Errorf("RemoveNote(%q) tag = %s", key, appErr.Tag)
		}
	}
}

func TestNoteFailures(t *testing.T) {
	key := strings.Repeat("cd", 32)
	t.Run("the directory cannot be made", func(t *testing.T) {
		t.Parallel()
		s, dir := newStore(t)
		if err := os.WriteFile(filepath.Join(dir, "notes"), []byte("a file"), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = requireRoute(t, s.PutNote(key, []byte("x")), "Store.PutNote.FileNoteDirFailed")
	})
	t.Run("the note cannot be written", func(t *testing.T) {
		skipIfRoot(t)
		s, dir := newStore(t)
		notes := filepath.Join(dir, "notes")
		if err := os.MkdirAll(notes, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(notes, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(notes, 0o750) })
		_ = requireRoute(t, s.PutNote(key, []byte("x")), "Store.PutNote.FileNoteWriteFailed")
	})
	t.Run("the note cannot be put in its place", func(t *testing.T) {
		t.Parallel()
		s, dir := newStore(t)
		if err := os.MkdirAll(filepath.Join(dir, "notes", key, "inner"), 0o750); err != nil {
			t.Fatal(err)
		}
		_ = requireRoute(t, s.PutNote(key, []byte("x")), "Store.PutNote.FileNoteRenameFailed")
		if got := names(t, filepath.Join(dir, "notes")); len(got) != 1 {
			t.Errorf("the notes directory holds %v, want the part file removed", got)
		}
	})
	t.Run("the note cannot be read", func(t *testing.T) {
		t.Parallel()
		s, dir := newStore(t)
		if err := os.MkdirAll(filepath.Join(dir, "notes", key), 0o750); err != nil {
			t.Fatal(err)
		}
		_, err := s.Note(key)
		_ = requireRoute(t, err, "Store.Note.FileNoteReadFailed")
	})
	t.Run("the note cannot be removed", func(t *testing.T) {
		t.Parallel()
		s, dir := newStore(t)
		if err := os.MkdirAll(filepath.Join(dir, "notes", key, "inner"), 0o750); err != nil {
			t.Fatal(err)
		}
		_ = requireRoute(t, s.RemoveNote(key), "Store.RemoveNote.FileNoteRemoveFailed")
	})
}

func TestListNotes(t *testing.T) {
	t.Parallel()
	s, dir := newStore(t)
	if notes, err := s.Notes(); err != nil || len(notes) != 0 {
		t.Fatalf("Notes before any note = %v, %v", notes, err)
	}
	first, second := strings.Repeat("ab", 32), strings.Repeat("cd", 32)
	for _, key := range []string{first, second} {
		if err := s.PutNote(key, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	// What is not a note: a part being written, a directory, a name that is no key.
	notes := filepath.Join(dir, "notes")
	if err := os.WriteFile(filepath.Join(notes, first+".part"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notes, "readme"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(notes, strings.Repeat("ef", 32)), 0o750); err != nil {
		t.Fatal(err)
	}
	got, err := s.Notes()
	if err != nil || len(got) != 2 {
		t.Fatalf("Notes = %v, %v, want the two notes", got, err)
	}
	keys := []string{got[0].Key, got[1].Key}
	slices.Sort(keys)
	if want := []string{first, second}; !slices.Equal(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
	if got[0].ModTime.IsZero() {
		t.Error("no modification time")
	}
}

func TestListNotesFailure(t *testing.T) {
	t.Parallel()
	s, dir := newStore(t)
	if err := os.WriteFile(filepath.Join(dir, "notes"), []byte("a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := s.Notes()
	_ = requireRoute(t, err, "Store.Notes.FileNoteListFailed")
}
