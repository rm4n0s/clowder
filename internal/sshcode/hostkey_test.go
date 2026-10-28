package sshcode_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/rm4n0s/clowder/internal/sshcode"
	"golang.org/x/crypto/ssh"
)

func TestLoadHostKeyMakesAndLoadsTheKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "ssh_host_ed25519_key")
	first, err := sshcode.LoadHostKey(path)
	if err != nil {
		t.Fatalf("LoadHostKey: %+v", err)
	}
	if first.PublicKey().Type() != ssh.KeyAlgoED25519 {
		t.Errorf("type = %s, want ed25519", first.PublicKey().Type())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	// Only the key is left in the directory: the temporary file is gone.
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Errorf("directory = %v, %v; want only the key", entries, err)
	}
	// The second start loads the same key.
	second, err := sshcode.LoadHostKey(path)
	if err != nil {
		t.Fatalf("LoadHostKey again: %+v", err)
	}
	if ssh.FingerprintSHA256(first.PublicKey()) != ssh.FingerprintSHA256(second.PublicKey()) {
		t.Error("the second start made another key")
	}
}

func TestLoadHostKeyTwoStartsTogether(t *testing.T) {
	t.Parallel()
	for range 5 {
		path := filepath.Join(t.TempDir(), "key")
		const starters = 8
		var ready, done sync.WaitGroup
		barrier := make(chan struct{})
		fingerprints := make([]string, starters)
		errs := make([]error, starters)
		ready.Add(starters)
		done.Add(starters)
		for i := range starters {
			go func() {
				defer done.Done()
				ready.Done()
				<-barrier
				signer, err := sshcode.LoadHostKey(path)
				if err != nil {
					errs[i] = err
					return
				}
				fingerprints[i] = ssh.FingerprintSHA256(signer.PublicKey())
			}()
		}
		ready.Wait()
		close(barrier)
		done.Wait()
		for i := range starters {
			if errs[i] != nil {
				t.Fatalf("starter %d: %+v", i, errs[i])
			}
			if fingerprints[i] != fingerprints[0] {
				t.Fatalf("starter %d ended with %s, starter 0 with %s", i, fingerprints[i], fingerprints[0])
			}
		}
		entries, err := os.ReadDir(filepath.Dir(path))
		if err != nil || len(entries) != 1 {
			t.Errorf("directory = %v, %v; want only the key", entries, err)
		}
	}
}

func TestLoadHostKeyFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		path  func(t *testing.T) string
		route string
	}{
		{"a directory in place of the file", func(t *testing.T) string {
			return t.TempDir()
		}, "LoadHostKey->readHostKey.HostKeyReadFailed"},
		{"a file that is not a key", func(t *testing.T) string {
			p := filepath.Join(t.TempDir(), "key")
			if err := os.WriteFile(p, []byte("not a key"), 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}, "LoadHostKey->readHostKey.HostKeyInvalid"},
		{"a missing directory", func(t *testing.T) string {
			return filepath.Join(t.TempDir(), "missing", "key")
		}, "LoadHostKey.HostKeyTempFailed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := tt.path(t)
			_, err := sshcode.LoadHostKey(path)
			appErr, ok := asTaggedError(err)
			if !ok {
				t.Fatalf("err = %T %v, want a tagged error", err, err)
			}
			if !appErr.HasRoute(tt.route) {
				t.Errorf("route = %s, want %s", appErr.Route(), tt.route)
			}
			if appErr.Metadata["path"] != path {
				t.Errorf("metadata = %v, want the path", appErr.Metadata)
			}
		})
	}
}
