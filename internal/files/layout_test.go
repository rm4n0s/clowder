package files

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLayoutNamesAreTheDirectoriesTheStoreMakes checks that the exported names
// of the layout are the ones New and PutNote use, and that IsKey accepts the
// keys that Save makes: a backup relies on all three.
func TestLayoutNamesAreTheDirectoriesTheStoreMakes(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %+v", err)
	}
	saved, err := s.Save(t.Context(), strings.NewReader("content"), 1<<10)
	if err != nil {
		t.Fatalf("Save: %+v", err)
	}
	if err := s.PutNote(saved.Key, []byte("{}")); err != nil {
		t.Fatalf("PutNote: %+v", err)
	}
	for _, name := range []string{TmpDir, NotesDir} {
		if info, err := os.Stat(filepath.Join(dir, name)); err != nil || !info.IsDir() {
			t.Errorf("%s: %v, %v; want a directory", name, info, err)
		}
	}
	tests := []struct {
		name string
		want bool
	}{
		{saved.Key, true},
		{strings.Repeat("0", 64), true},
		{strings.ToUpper(saved.Key), false},
		{saved.Key[:63], false},
		{saved.Key + "0", false},
		{"..", false},
		{"", false},
		{TmpDir, false},
	}
	for _, tt := range tests {
		if got := IsKey(tt.name); got != tt.want {
			t.Errorf("IsKey(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}
