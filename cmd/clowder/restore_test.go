package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/store"
	rmerrors "github.com/rm4n0s/errors"
)

// tarItem is one entry the tests put in a hand-made archive.
type tarItem struct {
	// name is the whole name in the tar header.
	name string
	// typ is the type flag; 0 means a regular file.
	typ byte
	// data is the content of a file.
	data string
	// size overrides the size in the header when not zero.
	size int64
	// link is the target of a link.
	link string
}

// archiveSpec describes an archive: the manifest that is written, the entries
// after it, and what to break.
type archiveSpec struct {
	manifest backupManifest
	items    []tarItem
	// manifestAt is the index among the entries at which the manifest is
	// written; 0 is first.
	manifestAt int
	// trailer is written inside the gzip stream after the end of the tar.
	trailer int
	// raw, when not nil, is the whole file instead of the archive.
	raw []byte
}

// goodSpec is a valid archive: a database, a host key and two files.
func goodSpec() archiveSpec {
	key := strings.Repeat("ab", 32)
	avatar := strings.Repeat("cd", 32)
	data := map[string]string{
		"clowder.db":              "database content",
		"ssh_host_ed25519_key":    "key content",
		"files/" + key:            "attachment content",
		"files/avatars/" + avatar: "avatar content",
	}
	m := backupManifest{
		Format: backupFormat, Version: "test", CreatedAt: time.Unix(1_700_000_000, 0).UTC(),
		Dialect: "sqlite", Database: "clowder.db", Schema: store.LatestMigration(),
	}
	var items []tarItem
	add := func(path string) {
		content, isFile := data[path]
		if !isFile {
			m.Entries = append(m.Entries, backupEntry{Path: path, Type: entryDir})
			items = append(items, tarItem{name: backupTopDir + "/" + path + "/", typ: tar.TypeDir})
			return
		}
		m.Entries = append(m.Entries, backupEntry{Path: path, Type: entryFile, Size: int64(len(content)), SHA256: sum([]byte(content))})
		items = append(items, tarItem{name: backupTopDir + "/" + path, data: content})
	}
	for _, p := range []string{"clowder.db", "ssh_host_ed25519_key", "files", "files/" + key, "files/avatars", "files/avatars/" + avatar} {
		add(p)
	}
	return archiveSpec{manifest: m, items: items}
}

// write writes the archive to path.
func (a archiveSpec) write(t *testing.T, path string) {
	t.Helper()
	if a.raw != nil {
		if err := os.WriteFile(path, a.raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	js, err := json.Marshal(a.manifest)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	put := func(it tarItem) {
		h := &tar.Header{Name: it.name, Typeflag: it.typ, Mode: 0o644, Size: int64(len(it.data)), Linkname: it.link, ModTime: time.Unix(1, 0)}
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Typeflag != tar.TypeReg {
			h.Size = 0
		}
		if it.size != 0 {
			h.Size = it.size
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			data := []byte(it.data)
			if int64(len(data)) < h.Size {
				data = append(data, make([]byte, h.Size-int64(len(data)))...)
			}
			if _, err := tw.Write(data[:h.Size]); err != nil {
				t.Fatal(err)
			}
		}
	}
	manifest := tarItem{name: backupTopDir + "/" + backupManifestName, data: string(js)}
	for i, it := range a.items {
		if i == a.manifestAt {
			put(manifest)
		}
		put(it)
	}
	if a.manifestAt >= len(a.items) {
		put(manifest)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if a.trailer > 0 {
		zeros := make([]byte, 1<<20)
		for left := a.trailer; left > 0; left -= len(zeros) {
			if _, err := gz.Write(zeros[:min(left, len(zeros))]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// restoreWith runs "clowder restore" on the archive into target.
func restoreWith(t *testing.T, archive, target string) (stdout string, err error) {
	t.Helper()
	var out bytes.Buffer
	err = run(t.Context(), []string{"restore", "-i", archive, "-data", target}, mapEnv(nil), &out, io.Discard, auth.FastHasher())
	return out.String(), err
}

func TestRestoreGoodArchive(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "a.tar.gz")
	goodSpec().write(t, archive)
	target := filepath.Join(t.TempDir(), "new", "data")
	out, err := restoreWith(t, archive, target)
	if err != nil {
		t.Fatalf("restore: %+v", err)
	}
	if !strings.Contains(out, "clowder serve -data "+target) {
		t.Errorf("the summary lacks the next step:\n%s", out)
	}
	var got []string
	err = filepath.WalkDir(target, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(target, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir() && info.Mode().Perm() != 0o700:
			t.Errorf("%s: mode %o, want 0700", rel, info.Mode().Perm())
		case !d.IsDir() && info.Mode().Perm() != 0o600:
			t.Errorf("%s: mode %o, want 0600", rel, info.Mode().Perm())
		}
		got = append(got, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	key, avatar := strings.Repeat("ab", 32), strings.Repeat("cd", 32)
	want := []string{".", "clowder.db", "files", "files/" + key, "files/avatars", "files/avatars/" + avatar, "ssh_host_ed25519_key"}
	if !slices.Equal(got, want) {
		t.Errorf("restored = %v, want %v", got, want)
	}
	if b, err := os.ReadFile(filepath.Join(target, "ssh_host_ed25519_key")); err != nil || string(b) != "key content" {
		t.Errorf("host key = %q, %v", b, err)
	}
	// The parent directories that the restore made are private too.
	if info, err := os.Stat(filepath.Dir(target)); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("parent = %v, %v; want 0700", info, err)
	}
}

func TestRestoreIntoEmptyExistingDirectory(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "a.tar.gz")
	goodSpec().write(t, archive)
	target := t.TempDir()
	if _, err := restoreWith(t, archive, target); err != nil {
		t.Fatalf("restore: %+v", err)
	}
	names := dirNames(t, target)
	if !slices.Equal(names, []string{"clowder.db", "files", "ssh_host_ed25519_key"}) {
		t.Errorf("target = %v (the staging directory must be gone)", names)
	}
}

func TestRestoreFromStandardInput(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "a.tar.gz")
	goodSpec().write(t, archive)
	raw, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "data")
	var out bytes.Buffer
	if err := runRestore(t.Context(), []string{"-i", "-", "-data", target}, mapEnv(nil), &out, io.Discard, bytes.NewReader(raw)); err != nil {
		t.Fatalf("restore: %+v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "clowder.db")); err != nil {
		t.Error(err)
	}
	// The data directory may also come from the environment.
	other := filepath.Join(t.TempDir(), "env")
	if err := runRestore(t.Context(), []string{"-i", "-"}, mapEnv(map[string]string{"CLOWDER_DATA": other}), &out, io.Discard, bytes.NewReader(raw)); err != nil {
		t.Fatalf("restore: %+v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "clowder.db")); err != nil {
		t.Error(err)
	}
}

func TestRestoreExternalDatabase(t *testing.T) {
	spec := goodSpec()
	spec.manifest.Dialect, spec.manifest.Database = "postgres", databaseExternal
	spec.manifest.Entries = spec.manifest.Entries[1:]
	spec.items = spec.items[1:]
	archive := filepath.Join(t.TempDir(), "a.tar.gz")
	spec.write(t, archive)
	target := filepath.Join(t.TempDir(), "data")
	out, err := restoreWith(t, archive, target)
	if err != nil {
		t.Fatalf("restore: %+v", err)
	}
	if !strings.Contains(out, "pg_restore") || strings.Contains(out, "next: clowder serve") {
		t.Errorf("a PostgreSQL archive must say the database comes first:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(target, "clowder.db")); err == nil {
		t.Error("a database file was made")
	}
}

// TestRestoreOlderSchema checks that an archive of an older schema restores:
// serve migrates it.
func TestRestoreOlderSchema(t *testing.T) {
	spec := goodSpec()
	spec.manifest.Schema = "0001_posts_fts"
	archive := filepath.Join(t.TempDir(), "a.tar.gz")
	spec.write(t, archive)
	if _, err := restoreWith(t, archive, filepath.Join(t.TempDir(), "data")); err != nil {
		t.Fatalf("restore: %+v", err)
	}
}

// TestRestoreHostileArchives checks that every kind of bad archive is refused
// with its own tag, and that nothing is written: a target that did not exist
// does not exist afterwards, one that was empty is empty, and nothing appears
// beside it.
func TestRestoreHostileArchives(t *testing.T) {
	key := strings.Repeat("ab", 32)
	tests := []struct {
		name   string
		mutate func(a *archiveSpec)
		tag    string
		route  string
	}{
		{
			name: "../ in a path",
			mutate: func(a *archiveSpec) {
				a.items = append(a.items, tarItem{name: "clowder-backup/files/../../evil", data: "x"})
			},
			tag: "RestoreEntryUnsafe", route: "unpackArchive.RestoreEntryUnsafe",
		},
		{
			name: "../ at the start of a path",
			mutate: func(a *archiveSpec) {
				a.items = append(a.items, tarItem{name: "../evil", data: "x"})
			},
			tag: "RestoreEntryUnsafe", route: "unpackArchive.RestoreEntryUnsafe",
		},
		{
			name: "an absolute path",
			mutate: func(a *archiveSpec) {
				a.items = append(a.items, tarItem{name: "/tmp/clowder-evil", data: "x"})
			},
			tag: "RestoreEntryUnsafe", route: "unpackArchive.RestoreEntryUnsafe",
		},
		{
			name: "an absolute path under the top directory",
			mutate: func(a *archiveSpec) {
				a.items = append(a.items, tarItem{name: "clowder-backup//tmp/evil", data: "x"})
			},
			tag: "RestoreEntryUnsafe", route: "unpackArchive.RestoreEntryUnsafe",
		},
		{
			name: "a . element",
			mutate: func(a *archiveSpec) {
				a.items = append(a.items, tarItem{name: "clowder-backup/files/./x", data: "x"})
			},
			tag: "RestoreEntryUnsafe", route: "unpackArchive.RestoreEntryUnsafe",
		},
		{
			name: "a path with a backslash",
			mutate: func(a *archiveSpec) {
				a.items = append(a.items, tarItem{name: `clowder-backup/files\..\evil`, data: "x"})
			},
			tag: "RestoreEntryUnsafe", route: "unpackArchive.RestoreEntryUnsafe",
		},
		{
			name: "a path outside the top directory",
			mutate: func(a *archiveSpec) {
				a.items = append(a.items, tarItem{name: "other/files/x", data: "x"})
			},
			tag: "RestoreEntryUnsafe", route: "unpackArchive.RestoreEntryUnsafe",
		},
		{
			name: "a symbolic link",
			mutate: func(a *archiveSpec) {
				a.items = append(a.items, tarItem{name: "clowder-backup/files/link", typ: tar.TypeSymlink, link: "/etc"})
			},
			tag: "RestoreEntryUnsafe", route: "unpackArchive.RestoreEntryUnsafe",
		},
		{
			name: "a hard link",
			mutate: func(a *archiveSpec) {
				a.items = append(a.items, tarItem{name: "clowder-backup/files/link", typ: tar.TypeLink, link: "clowder-backup/clowder.db"})
			},
			tag: "RestoreEntryUnsafe", route: "unpackArchive.RestoreEntryUnsafe",
		},
		{
			name: "a character device",
			mutate: func(a *archiveSpec) {
				a.items = append(a.items, tarItem{name: "clowder-backup/files/dev", typ: tar.TypeChar})
			},
			tag: "RestoreEntryUnsafe", route: "unpackArchive.RestoreEntryUnsafe",
		},
		{
			name: "a block device",
			mutate: func(a *archiveSpec) {
				a.items = append(a.items, tarItem{name: "clowder-backup/files/dev", typ: tar.TypeBlock})
			},
			tag: "RestoreEntryUnsafe", route: "unpackArchive.RestoreEntryUnsafe",
		},
		{
			name: "a FIFO",
			mutate: func(a *archiveSpec) {
				a.items = append(a.items, tarItem{name: "clowder-backup/files/fifo", typ: tar.TypeFifo})
			},
			tag: "RestoreEntryUnsafe", route: "unpackArchive.RestoreEntryUnsafe",
		},
		{
			name: "an entry that the manifest does not list",
			mutate: func(a *archiveSpec) {
				a.items = append(a.items, tarItem{name: "clowder-backup/files/extra", data: "x"})
			},
			tag: "RestoreEntryUnlisted", route: "unpackArchive.RestoreEntryUnlisted",
		},
		{
			name: "a directory where the manifest lists a file",
			mutate: func(a *archiveSpec) {
				a.items[3] = tarItem{name: "clowder-backup/files/" + key + "/", typ: tar.TypeDir}
			},
			tag: "RestoreEntryUnlisted", route: "unpackArchive.RestoreEntryUnlisted",
		},
		{
			name: "an entry that the archive does not have",
			mutate: func(a *archiveSpec) {
				a.manifest.Entries = append(a.manifest.Entries, backupEntry{Path: "files/gone", Type: entryFile, Size: 1, SHA256: sum([]byte("x"))})
			},
			tag: "RestoreEntryMissing", route: "unpackArchive.RestoreEntryMissing",
		},
		{
			name: "an entry twice",
			mutate: func(a *archiveSpec) {
				a.items = append(a.items, a.items[3])
			},
			tag: "RestoreEntryDuplicate", route: "unpackArchive.RestoreEntryDuplicate",
		},
		{
			name: "more content than the manifest declares",
			mutate: func(a *archiveSpec) {
				a.items[3].data += " and more"
			},
			tag: "RestoreSizeMismatch", route: "unpackArchive.RestoreSizeMismatch",
		},
		{
			name: "a header that declares a huge size, which would inflate far beyond the manifest",
			mutate: func(a *archiveSpec) {
				a.items[3].size = 64 << 20
			},
			tag: "RestoreSizeMismatch", route: "unpackArchive.RestoreSizeMismatch",
		},
		{
			name: "a wrong checksum",
			mutate: func(a *archiveSpec) {
				a.items[3].data = "attachment CONTENT"
			},
			tag: "RestoreChecksumMismatch", route: "extractFile.RestoreChecksumMismatch",
		},
		{
			name: "a manifest that is not first",
			mutate: func(a *archiveSpec) {
				a.manifestAt = 2
			},
			tag: "RestoreManifestNotFirst", route: "readManifest.RestoreManifestNotFirst",
		},
		{
			name: "a manifest at the end",
			mutate: func(a *archiveSpec) {
				a.manifestAt = len(a.items)
			},
			tag: "RestoreManifestNotFirst", route: "readManifest.RestoreManifestNotFirst",
		},
		{
			name: "an archive with nothing in it",
			mutate: func(a *archiveSpec) {
				a.items = nil
				a.manifestAt = 1
				a.raw = emptyArchive(t)
			},
			tag: "RestoreManifestNotFirst", route: "readManifest.RestoreManifestNotFirst",
		},
		{
			name: "a newer schema",
			mutate: func(a *archiveSpec) {
				a.manifest.Schema = "9999_from_the_future"
			},
			tag: "RestoreSchemaTooNew", route: "checkManifest.RestoreSchemaTooNew",
		},
		{
			name: "an unknown format",
			mutate: func(a *archiveSpec) {
				a.manifest.Format = 2
			},
			tag: "RestoreFormatUnknown", route: "checkManifest.RestoreFormatUnknown",
		},
		{
			name: "a gzip bomb after the end of the tar",
			mutate: func(a *archiveSpec) {
				a.trailer = 16 << 20
			},
			tag: "RestoreArchiveTooLarge", route: "unpackArchive.RestoreArchiveTooLarge",
		},
		{
			name: "a manifest that lists a path with ..",
			mutate: func(a *archiveSpec) {
				a.manifest.Entries = append(a.manifest.Entries, backupEntry{Path: "files/../../evil", Type: entryFile, Size: 1, SHA256: sum([]byte("x"))})
			},
			tag: "RestoreManifestInvalid", route: "checkManifest.RestoreManifestInvalid",
		},
		{
			name: "a manifest that lists an absolute path",
			mutate: func(a *archiveSpec) {
				a.manifest.Entries = append(a.manifest.Entries, backupEntry{Path: "/etc/passwd", Type: entryFile, Size: 1, SHA256: sum([]byte("x"))})
			},
			tag: "RestoreManifestInvalid", route: "checkManifest.RestoreManifestInvalid",
		},
		{
			name: "a manifest that lists a place a data directory does not have",
			mutate: func(a *archiveSpec) {
				a.manifest.Entries = append(a.manifest.Entries, backupEntry{Path: "clowder.db-wal", Type: entryFile, Size: 1, SHA256: sum([]byte("x"))})
				a.items = append(a.items, tarItem{name: "clowder-backup/clowder.db-wal", data: "x"})
			},
			tag: "RestoreManifestInvalid", route: "checkManifest.RestoreManifestInvalid",
		},
		{
			name: "a manifest that lists a path twice",
			mutate: func(a *archiveSpec) {
				a.manifest.Entries = append(a.manifest.Entries, a.manifest.Entries[3])
			},
			tag: "RestoreManifestInvalid", route: "checkManifest.RestoreManifestInvalid",
		},
		{
			name: "a manifest whose entry has no directory",
			mutate: func(a *archiveSpec) {
				a.manifest.Entries = append(a.manifest.Entries, backupEntry{Path: "files/deeper/x", Type: entryFile, Size: 1, SHA256: sum([]byte("x"))})
				a.items = append(a.items, tarItem{name: "clowder-backup/files/deeper/x", data: "x"})
			},
			tag: "RestoreManifestInvalid", route: "checkManifest.RestoreManifestInvalid",
		},
		{
			name: "a manifest with an entry of another type",
			mutate: func(a *archiveSpec) {
				a.manifest.Entries[3].Type = "symlink"
			},
			tag: "RestoreManifestInvalid", route: "checkManifest.RestoreManifestInvalid",
		},
		{
			name: "a manifest with a size that cannot be",
			mutate: func(a *archiveSpec) {
				a.manifest.Entries[3].Size = -1
			},
			tag: "RestoreManifestInvalid", route: "checkManifest.RestoreManifestInvalid",
		},
		{
			name: "a manifest with a checksum that is not one",
			mutate: func(a *archiveSpec) {
				a.manifest.Entries[3].SHA256 = "zz"
			},
			tag: "RestoreManifestInvalid", route: "checkManifest.RestoreManifestInvalid",
		},
		{
			name: "a manifest that names a database it does not list",
			mutate: func(a *archiveSpec) {
				a.manifest.Entries = a.manifest.Entries[1:]
				a.items = a.items[1:]
			},
			tag: "RestoreManifestInvalid", route: "checkManifest.RestoreManifestInvalid",
		},
		{
			name: "a manifest with a database of no known kind",
			mutate: func(a *archiveSpec) {
				a.manifest.Database = "mysql"
			},
			tag: "RestoreManifestInvalid", route: "checkManifest.RestoreManifestInvalid",
		},
		{
			name: "a directory with a checksum",
			mutate: func(a *archiveSpec) {
				a.manifest.Entries[2].SHA256 = sum([]byte("x"))
			},
			tag: "RestoreManifestInvalid", route: "checkManifest.RestoreManifestInvalid",
		},
		{
			name: "a manifest that is not JSON",
			mutate: func(a *archiveSpec) {
				a.raw = rawArchive(t, backupTopDir+"/"+backupManifestName, "{not json")
			},
			tag: "RestoreManifestInvalid", route: "readManifest.RestoreManifestInvalid",
		},
		{
			name: "a file that is not gzip",
			mutate: func(a *archiveSpec) {
				a.raw = []byte("this is not an archive")
			},
			tag: "RestoreArchiveInvalid", route: "unpackArchive.RestoreArchiveInvalid",
		},
		{
			name: "a gzip stream that stops in the middle",
			mutate: func(a *archiveSpec) {
				a.raw = truncatedArchive(t)
			},
			tag: "RestoreArchiveInvalid", route: "RestoreArchiveInvalid",
		},
	}
	for _, tt := range tests {
		for _, existing := range []bool{false, true} {
			name := tt.name + ", target missing"
			if existing {
				name = tt.name + ", target empty"
			}
			t.Run(name, func(t *testing.T) {
				spec := goodSpec()
				tt.mutate(&spec)
				archive := filepath.Join(t.TempDir(), "a.tar.gz")
				spec.write(t, archive)

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
					t.Errorf("tag %s, route %s; want %s via %s", appErr.Tag, appErr.Route(), tt.tag, tt.route)
				}
				// The entry that was refused is named in the metadata.
				switch appErr.Tag {
				case "RestoreEntryUnsafe", "RestoreEntryUnlisted", "RestoreEntryDuplicate", "RestoreEntryMissing",
					"RestoreSizeMismatch", "RestoreChecksumMismatch":
					if p, _ := appErr.Metadata["path"].(string); p == "" {
						t.Errorf("no path in the metadata: %v", appErr.Metadata)
					}
				}
				// Nothing is written: not in the target, and not beside it.
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

func wantParent(existing bool) []string {
	if existing {
		return []string{"data"}
	}
	return nil
}

// emptyArchive is a valid tar.gz with no entries.
func emptyArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if err := tar.NewWriter(gz).Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// rawArchive is a tar.gz whose only entry is name with the content.
func rawArchive(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// truncatedArchive is a good archive cut in the middle of its gzip stream.
func truncatedArchive(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "a.tar.gz")
	spec := goodSpec()
	// Content that does not compress, so that the cut is inside it.
	noise := make([]byte, 50_000)
	for i := range noise {
		noise[i] = byte(i * 7919 % 251)
	}
	spec.items[3].data = string(noise)
	spec.manifest.Entries[3].Size = int64(len(noise))
	spec.manifest.Entries[3].SHA256 = sum(noise)
	spec.write(t, path)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw[:len(raw)/2]
}

func TestRestoreRefusesANonEmptyTarget(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "a.tar.gz")
	goodSpec().write(t, archive)
	tests := []struct {
		name  string
		setup func(t *testing.T) string
		tag   string
		route string
	}{
		{
			name: "a file in it",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "clowder.db"), []byte("precious"), 0o600); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			tag: "RestoreTargetNotEmpty", route: "run->runRestore->prepareRestoreTarget.RestoreTargetNotEmpty",
		},
		{
			name: "a directory in it",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				if err := os.Mkdir(filepath.Join(dir, "files"), 0o700); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			tag: "RestoreTargetNotEmpty", route: "run->runRestore->prepareRestoreTarget.RestoreTargetNotEmpty",
		},
		{
			name: "a hidden file in it",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, ".keep"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			tag: "RestoreTargetNotEmpty", route: "run->runRestore->prepareRestoreTarget.RestoreTargetNotEmpty",
		},
		{
			name: "a file instead of a directory",
			setup: func(t *testing.T) string {
				p := filepath.Join(t.TempDir(), "file")
				if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				return p
			},
			tag: "RestoreTargetInvalid", route: "run->runRestore->prepareRestoreTarget.RestoreTargetInvalid",
		},
		{
			name: "a directory that cannot be made",
			setup: func(t *testing.T) string {
				p := filepath.Join(t.TempDir(), "file")
				if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(p, "data")
			},
			tag: "RestoreTargetInvalid", route: "run->runRestore->prepareRestoreTarget.RestoreTargetInvalid",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := tt.setup(t)
			before := dirNames(t, target)
			_, err := restoreWith(t, archive, target)
			appErr, ok := rmerrors.FromError(err)
			if !ok || appErr.Tag != tt.tag || !appErr.HasRoute(tt.route) {
				t.Fatalf("err = %v (route %v); want %s via %s", err, appErr, tt.tag, tt.route)
			}
			if info, statErr := os.Stat(target); statErr == nil && info.IsDir() {
				if after := dirNames(t, target); !slices.Equal(before, after) {
					t.Errorf("the target changed: %v -> %v", before, after)
				}
			}
		})
	}
}

func TestRestoreInputAndFlags(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		route string
	}{
		{"no -i", []string{"-data", "x"}, "run->runRestore.RestoreInputInvalid"},
		{"an archive that does not exist", []string{"-i", filepath.Join(t.TempDir(), "nope"), "-data", filepath.Join(t.TempDir(), "d")}, "run->runRestore.RestoreInputInvalid"},
		{"an empty -data", []string{"-i", "x", "-data", ""}, "run->runRestore.RestoreTargetInvalid"},
		{"an unknown flag", []string{"-nosuchflag"}, "run->runRestore.FlagsInvalid"},
		{"an argument", []string{"-i", "x", "extra"}, "run->runRestore.FlagsInvalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := run(t.Context(), append([]string{"restore"}, tt.args...), mapEnv(nil), io.Discard, io.Discard, auth.FastHasher())
			appErr, ok := rmerrors.FromError(err)
			if !ok || !appErr.HasRoute(tt.route) {
				t.Fatalf("err = %v, want the route %s", err, tt.route)
			}
		})
	}
}

// TestRestoreWriteFailure checks the tag of a target that cannot take the
// staging directory.
func TestRestoreWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	archive := filepath.Join(t.TempDir(), "a.tar.gz")
	goodSpec().write(t, archive)
	target := t.TempDir()
	if err := os.Chmod(target, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(target, 0o700) })
	_, err := restoreWith(t, archive, target)
	appErr, ok := rmerrors.FromError(err)
	if !ok || !appErr.HasRoute("run->runRestore->restoreInto.RestoreWriteFailed") {
		t.Fatalf("err = %v, want RestoreWriteFailed from restoreInto", err)
	}
}

// TestRestoreCanceled checks that a canceled restore leaves the target as it was.
func TestRestoreCanceled(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "a.tar.gz")
	goodSpec().write(t, archive)
	target := filepath.Join(t.TempDir(), "data")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := run(ctx, []string{"restore", "-i", archive, "-data", target}, mapEnv(nil), io.Discard, io.Discard, auth.FastHasher())
	appErr, ok := rmerrors.FromError(err)
	if !ok || appErr.Tag != "RestoreWriteFailed" {
		t.Fatalf("err = %v, want RestoreWriteFailed", err)
	}
	if _, err := os.Stat(target); err == nil {
		t.Error("the target was left behind")
	}
}
