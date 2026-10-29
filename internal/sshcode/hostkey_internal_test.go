package sshcode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rm4n0s/errors"
)

// TestLoadHostKeyTightensALooseKey: a host key that was copied or restored
// with a mode that others can read is made 0600 when the server starts (step
// 24.1), and a key that is 0600 or tighter is left as it is.
func TestLoadHostKeyTightensALooseKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		mode os.FileMode
		want os.FileMode
	}{
		{"world readable", 0o644, 0o600},
		{"group readable", 0o640, 0o600},
		{"everything", 0o777, 0o600},
		{"read only for the owner", 0o400, 0o400},
		{"the owner's", 0o600, 0o600},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "key")
			first, err := LoadHostKey(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tt.mode); err != nil {
				t.Fatal(err)
			}
			second, err := LoadHostKey(path)
			if err != nil {
				t.Fatalf("LoadHostKey: %+v", err)
			}
			if string(first.PublicKey().Marshal()) != string(second.PublicKey().Marshal()) {
				t.Error("the key changed")
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != tt.want {
				t.Errorf("mode = %v, want %v", info.Mode().Perm(), tt.want)
			}
		})
	}
}

func TestTightenHostKeyFailures(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "key")
	if _, err := LoadHostKey(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		path  string
		chmod func(string, os.FileMode) error
	}{
		{"the file is gone", filepath.Join(t.TempDir(), "gone"), os.Chmod},
		{"the mode cannot be changed", path, func(string, os.FileMode) error { return os.ErrPermission }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tightenHostKey(tt.path, tt.chmod)
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T", err)
			}
			if !appErr.HasRoute("tightenHostKey.HostKeyModeFailed") {
				t.Errorf("unexpected failure path: %s", appErr.Route())
			}
		})
	}
}
