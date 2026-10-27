package files

import (
	"io"
	"os"
)

// TempFile gives the tests the interface of the file Save writes.
type TempFile = tempFile

// UseRandom makes the store take its keys from r.
func (s *Store) UseRandom(r io.Reader) { s.random = r }

// UseCreate makes the store make its temporary files with f.
func (s *Store) UseCreate(f func(dir, pattern string) (TempFile, error)) { s.create = f }

// CreateTemp makes a temporary file the way the store does by default.
func CreateTemp(dir, pattern string) (TempFile, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	return f, nil
}
