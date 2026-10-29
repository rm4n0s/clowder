package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	rmerrors "github.com/rm4n0s/errors"
)

// tarBytes returns the tar stream (not compressed) of the archive spec.
func tarBytes(t *testing.T, spec archiveSpec) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "a.tar.gz")
	spec.write(t, path)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// gzipped compresses a tar stream.
func gzipped(t *testing.T, tarStream []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(tarStream); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// patchHeader finds the header block of the entry name in the tar stream, lets
// fix change it, and writes the checksum of the block again.
func patchHeader(t *testing.T, tarStream []byte, name string, fix func(block []byte)) {
	t.Helper()
	for off := 0; off+512 <= len(tarStream); off += 512 {
		block := tarStream[off : off+512]
		if !bytes.HasPrefix(block, []byte(name+"\x00")) {
			continue
		}
		fix(block)
		for i := 148; i < 156; i++ {
			block[i] = ' '
		}
		var sum int
		for _, b := range block {
			sum += int(b)
		}
		copy(block[148:156], fmt.Sprintf("%06o\x00 ", sum))
		return
	}
	t.Fatalf("no header for %q", name)
}

// TestRestoreMoreHostileArchives tries the shapes that the table of
// TestRestoreHostileArchives does not: headers a tar writer refuses to make
// (a negative size), names that no file system takes (a NUL, a very long
// path), and a manifest of a size no honest backup has. Each is refused with
// a tag, and leaves the target and its parent as they were (step 24.1).
func TestRestoreMoreHostileArchives(t *testing.T) {
	long := strings.Repeat("a", 300)
	tests := []struct {
		name string
		// make returns the whole archive file.
		make  func(t *testing.T) []byte
		tag   string
		route string
	}{
		{
			name: "a header with a negative size (base-256)",
			make: func(t *testing.T) []byte {
				spec := goodSpec()
				stream := tarBytes(t, spec)
				patchHeader(t, stream, "clowder-backup/clowder.db", func(b []byte) {
					for i := 124; i < 136; i++ {
						b[i] = 0xff
					}
				})
				return gzipped(t, stream)
			},
			tag: "RestoreArchiveInvalid", route: "unpackArchive.RestoreArchiveInvalid",
		},
		{
			name: "a header with a size that is not a number",
			make: func(t *testing.T) []byte {
				stream := tarBytes(t, goodSpec())
				patchHeader(t, stream, "clowder-backup/clowder.db", func(b []byte) { copy(b[124:136], "zzzzzzzzzzz\x00") })
				return gzipped(t, stream)
			},
			tag: "RestoreArchiveInvalid", route: "unpackArchive.RestoreArchiveInvalid",
		},
		{
			name: "a name with a NUL inside it",
			make: func(t *testing.T) []byte {
				stream := tarBytes(t, goodSpec())
				patchHeader(t, stream, "clowder-backup/clowder.db", func(b []byte) {
					// A reader cuts a name at the NUL: put the NUL in the prefix
					// field instead, which is joined to the name.
					copy(b[345:], "clowder-backup/files\x00x")
				})
				return gzipped(t, stream)
			},
			tag: "RestoreEntryUnlisted", route: "unpackArchive.RestoreEntryUnlisted",
		},
		{
			name: "a manifest path with a NUL",
			make: func(t *testing.T) []byte {
				spec := goodSpec()
				spec.manifest.Entries = append(spec.manifest.Entries, backupEntry{Path: "files/a\x00b", Type: entryDir})
				return writeSpec(t, spec)
			},
			tag: "RestoreManifestInvalid", route: "checkManifest.RestoreManifestInvalid",
		},
		{
			name: "a path with an element of 300 bytes",
			make: func(t *testing.T) []byte {
				spec := goodSpec()
				spec.manifest.Entries = append(spec.manifest.Entries, backupEntry{Path: "files/" + long, Type: entryDir})
				spec.items = append(spec.items, tarItem{name: "clowder-backup/files/" + long + "/", typ: tar.TypeDir})
				return writeSpec(t, spec)
			},
			tag: "RestoreWriteFailed", route: "unpackArchive.RestoreWriteFailed",
		},
		{
			name: "a path of 6000 bytes in a PAX header",
			make: func(t *testing.T) []byte {
				spec := goodSpec()
				var p strings.Builder
				p.WriteString("files")
				for range 24 {
					p.WriteString("/" + strings.Repeat("d", 250))
				}
				// Every directory on the way is listed, so that only the
				// length of the path is wrong.
				parts := strings.Split(p.String(), "/")
				for i := 1; i <= len(parts); i++ {
					dir := strings.Join(parts[:i], "/")
					if dir == "files" {
						continue
					}
					spec.manifest.Entries = append(spec.manifest.Entries, backupEntry{Path: dir, Type: entryDir})
					spec.items = append(spec.items, tarItem{name: "clowder-backup/" + dir + "/", typ: tar.TypeDir})
				}
				return writeSpec(t, spec)
			},
			tag: "RestoreWriteFailed", route: "unpackArchive.RestoreWriteFailed",
		},
	}
	for _, tt := range tests {
		for _, existing := range []bool{false, true} {
			name := tt.name + ", target missing"
			if existing {
				name = tt.name + ", target empty"
			}
			t.Run(name, func(t *testing.T) {
				archive := filepath.Join(t.TempDir(), "a.tar.gz")
				if err := os.WriteFile(archive, tt.make(t), 0o600); err != nil {
					t.Fatal(err)
				}
				parent := t.TempDir()
				target := filepath.Join(parent, "data")
				if existing {
					if err := os.Mkdir(target, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				_, err := restoreWith(t, archive, target)
				appErr, ok := rmerrors.FromError(err)
				if !ok {
					t.Fatalf("expected *errors.Error, got %T: %v", err, err)
				}
				if appErr.Tag != tt.tag || !appErr.HasRoute(tt.route) {
					t.Errorf("tag %s, route %s; want %s via %s (%v)", appErr.Tag, appErr.Route(), tt.tag, tt.route, appErr.Message)
				}
				if existing {
					if names := dirNames(t, target); len(names) != 0 {
						t.Errorf("the target has %v", names)
					}
				} else if _, err := os.Stat(target); err == nil {
					t.Error("the target was made and left behind")
				}
				if names := dirNames(t, parent); !slices.Equal(names, wantParent(existing)) {
					t.Errorf("the parent directory has %v", names)
				}
			})
		}
	}
}

// writeSpec writes the spec as an archive file and returns its bytes.
func writeSpec(t *testing.T, spec archiveSpec) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spec.tar.gz")
	spec.write(t, path)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestCheckManifestOfManyEntries shows what a manifest of the largest size
// costs: the manifest is read into memory whole and capped at 64 MiB, so the
// entries it can list are bounded by its size (about 1.5 million directories,
// far fewer than maxEntryCount), and checking them takes seconds and a few
// hundred megabytes at the worst. It is the cost of reading a hostile archive
// the operator chose to restore.
func TestCheckManifestOfManyEntries(t *testing.T) {
	const n = 300_000
	m := backupManifest{Format: backupFormat, Database: databaseExternal}
	m.Entries = append(m.Entries, backupEntry{Path: filesDir, Type: entryDir})
	for i := range n {
		m.Entries = append(m.Entries, backupEntry{Path: fmt.Sprintf("%s/d%d", filesDir, i), Type: entryDir})
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	listed, _, err := checkManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	if len(listed) != n+1 {
		t.Fatalf("listed %d, want %d", len(listed), n+1)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("checking %d entries took %v", n, took)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 200<<20 {
		t.Errorf("checking %d entries allocated %d MiB", n, grew>>20)
	}
}
