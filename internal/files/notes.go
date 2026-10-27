package files

import (
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/rm4n0s/errors"
)

// notesDirName is the directory of the notes, inside the files directory. It
// is made when the first note is stored, so a store that never keeps a note
// has no such directory.
const notesDirName = "notes"

// notesFileMode is the permission of a note: only the server reads it.
const notesFileMode = 0o600

// notesSuffix is added to the name of a note that is being written. A note is
// renamed to its key when it is complete.
const notesSuffix = ".part"

// PutNote keeps a small piece of data about the file stored under key, beside
// the file, replacing the note that was there. A note is how an upload that
// has not become an attachment yet is remembered between two requests, by this
// process and by any other that shares the directory (SPEC §8.1). A note is
// written whole and renamed to its place. A key that is not 64 lowercase hex
// characters fails with TagBadKey before the disk is touched.
func (s *Store) PutNote(key string, data []byte) error {
	if !validKey(key) {
		return errors.New(TagBadKey, "not a file key", "length", len(key))
	}
	dir := filepath.Join(s.dir, notesDirName)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return errors.NewErr(tagNoteDirFailed, err).SetMetadata("dir", dir)
	}
	path := filepath.Join(dir, key)
	part := path + notesSuffix
	if err := os.WriteFile(part, data, notesFileMode); err != nil {
		return errors.NewErr(tagNoteWriteFailed, err).SetMetadata("key", key)
	}
	if err := os.Rename(part, path); err != nil {
		// Best effort: the error that is returned is the one that matters.
		_ = os.Remove(part)
		return errors.NewErr(tagNoteRenameFailed, err).SetMetadata("key", key)
	}
	return nil
}

// Note returns the note kept for the key. A key that is not 64 lowercase hex
// characters fails with TagBadKey, a key with no note with TagNotFound.
func (s *Store) Note(key string) ([]byte, error) {
	if !validKey(key) {
		return nil, errors.New(TagBadKey, "not a file key", "length", len(key))
	}
	data, err := os.ReadFile(filepath.Join(s.dir, notesDirName, key))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, errors.NewErr(TagNotFound, err).SetMetadata("key", key)
		}
		return nil, errors.NewErr(tagNoteReadFailed, err).SetMetadata("key", key)
	}
	return data, nil
}

// RemoveNote deletes the note kept for the key. A key with no note is not an
// error. A key that is not 64 lowercase hex characters fails with TagBadKey.
func (s *Store) RemoveNote(key string) error {
	if !validKey(key) {
		return errors.New(TagBadKey, "not a file key", "length", len(key))
	}
	if err := os.Remove(filepath.Join(s.dir, notesDirName, key)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errors.NewErr(tagNoteRemoveFailed, err).SetMetadata("key", key)
	}
	return nil
}

// NoteInfo is a note that exists, as Notes lists it.
type NoteInfo struct {
	// Key is the key of the file the note is for.
	Key string
	// ModTime is when the note was last written.
	ModTime time.Time
}

// Notes lists the notes that exist, in no particular order. Only entries named
// like a key count: a note being written and anything else in the directory
// are left out. A directory that does not exist yet has no notes. A note
// removed by another process while the list is made is left out. It fails with
// FileNoteListFailed when the directory cannot be read.
func (s *Store) Notes() ([]NoteInfo, error) {
	dir := filepath.Join(s.dir, notesDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, errors.NewErr(tagNoteListFailed, err).SetMetadata("dir", dir)
	}
	var notes []NoteInfo
	for _, e := range entries {
		if !validKey(e.Name()) || !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // removed in the meantime
		}
		notes = append(notes, NoteInfo{Key: e.Name(), ModTime: info.ModTime()})
	}
	return notes, nil
}

const (
	tagNoteListFailed   = "FileNoteListFailed"
	tagNoteDirFailed    = "FileNoteDirFailed"
	tagNoteWriteFailed  = "FileNoteWriteFailed"
	tagNoteRenameFailed = "FileNoteRenameFailed"
	tagNoteReadFailed   = "FileNoteReadFailed"
	tagNoteRemoveFailed = "FileNoteRemoveFailed"
)
