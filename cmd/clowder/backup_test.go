package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/clowder/internal/markdown"
	"github.com/rm4n0s/clowder/internal/sshcode"
	"github.com/rm4n0s/clowder/internal/store"
	rmerrors "github.com/rm4n0s/errors"
	"golang.org/x/crypto/ssh"
)

// installation is a demo data directory with the things a backup must treat
// in their own way.
type installation struct {
	// dir is the data directory.
	dir string
	// fingerprint is the fingerprint of the SSH host key.
	fingerprint string
	// stagedKey is the key of an upload that was staged and never posted.
	stagedKey string
	// usedKey is the key of an attachment that also has a note.
	usedKey string
	// attachmentKeys are the keys of the files in files/ that are attachments.
	attachmentKeys []string
}

// newInstallation makes a demo installation in a new directory, with an SSH
// host key, a staged upload with its note, a note on a file that is in use,
// a file being written, and the files that the database leaves around.
func newInstallation(t *testing.T) installation {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"demo", "-data", dir}, mapEnv(nil), &stdout, &stderr, auth.FastHasher()); err != nil {
		t.Fatalf("demo: %+v", err)
	}
	in := installation{dir: dir}
	signer, err := sshcode.LoadHostKey(filepath.Join(dir, config.DefaultSSHHostKeyFile))
	if err != nil {
		t.Fatalf("host key: %+v", err)
	}
	in.fingerprint = ssh.FingerprintSHA256(signer.PublicKey())

	entries, err := os.ReadDir(filepath.Join(dir, filesDir))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if files.IsKey(e.Name()) {
			in.attachmentKeys = append(in.attachmentKeys, e.Name())
		}
	}
	if len(in.attachmentKeys) < 2 {
		t.Fatal("the demo has fewer than two attachments")
	}
	fs := openFiles(t, dir)
	saved, err := fs.Save(t.Context(), strings.NewReader("a staged upload nobody posted"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	in.stagedKey = saved.Key
	if err := fs.PutNote(in.stagedKey, []byte(`{"user":2}`)); err != nil {
		t.Fatal(err)
	}
	in.usedKey = in.attachmentKeys[0]
	if err := fs.PutNote(in.usedKey, []byte(`{"user":2}`)); err != nil {
		t.Fatal(err)
	}
	junk := map[string]string{
		"files/tmp/upload-123":      "a file being written",
		"files/avatars/tmp/part-1":  "an avatar being written",
		"clowder.db-wal":            "wal",
		"clowder.db-shm":            "shm",
		"backup-leftover.db":        "an old copy",
		"notes.txt":                 "something of the operator",
		".restore-old/clowder.db":   "x",
		"files/avatars/not-a-key":   "kept: any regular file of files/",
		"files/notes/not-a-key.tmp": "left out with the notes",
	}
	for name, content := range junk {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return in
}

// archiveEntry is an entry read back from an archive.
type archiveEntry struct {
	header tar.Header
	data   []byte
}

// readArchive reads a tar.gz, in order.
func readArchive(t *testing.T, r io.Reader) []archiveEntry {
	t.Helper()
	gz, err := gzip.NewReader(r)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	tr := tar.NewReader(gz)
	var out []archiveEntry
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("tar entry %s: %v", h.Name, err)
		}
		out = append(out, archiveEntry{header: *h, data: data})
	}
}

// readArchiveFile reads the archive at path.
func readArchiveFile(t *testing.T, path string) []archiveEntry {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	return readArchive(t, f)
}

// manifestOf decodes the manifest, which must be the first entry.
func manifestOf(t *testing.T, entries []archiveEntry) backupManifest {
	t.Helper()
	if len(entries) == 0 || entries[0].header.Name != "clowder-backup/manifest.json" {
		t.Fatalf("the first entry is not the manifest: %v", entries)
	}
	var m backupManifest
	if err := json.Unmarshal(entries[0].data, &m); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	return m
}

func sum(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

func TestBackupArchive(t *testing.T) {
	in := newInstallation(t)
	out := filepath.Join(t.TempDir(), "backups", "b.tar.gz")
	if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"backup", "-data", in.dir, "-o", out}, mapEnv(nil), &stdout, &stderr, auth.FastHasher()); err != nil {
		t.Fatalf("backup: %+v\n%s", err, stderr.String())
	}

	info, err := os.Stat(out)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("archive = %v, %v; want a file of mode 0600", info, err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(out), ".clowder-backup-*")); len(left) > 0 {
		t.Errorf("temporary archives left: %v", left)
	}
	if left, _ := filepath.Glob(filepath.Join(in.dir, "backup-*.db")); !slices.Equal(left, []string{filepath.Join(in.dir, "backup-leftover.db")}) {
		t.Errorf("database copies in the data directory = %v, want only the old one that the test made", left)
	}

	entries := readArchiveFile(t, out)
	m := manifestOf(t, entries)
	if m.Format != 1 || m.Dialect != "sqlite" || m.Database != "clowder.db" || m.Schema != store.LatestMigration() {
		t.Errorf("manifest = %+v", m)
	}
	if time.Since(m.CreatedAt) > time.Minute || m.CreatedAt.Location() != time.UTC || m.Version == "" {
		t.Errorf("manifest time or version: %v, %q", m.CreatedAt, m.Version)
	}

	want := []string{
		"clowder-backup/manifest.json",
		"clowder-backup/clowder.db",
		"clowder-backup/ssh_host_ed25519_key",
		"clowder-backup/files/",
		"clowder-backup/files/avatars/",
		"clowder-backup/files/avatars/not-a-key",
	}
	for _, k := range in.attachmentKeys {
		want = append(want, "clowder-backup/files/"+k)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.header.Name)
		if e.header.Typeflag != tar.TypeReg && e.header.Typeflag != tar.TypeDir {
			t.Errorf("%s: type %q", e.header.Name, e.header.Typeflag)
		}
	}
	// The avatars are files too; the test lists the rest by prefix.
	var avatarFiles int
	for _, name := range got {
		if strings.HasPrefix(name, "clowder-backup/files/avatars/") && len(name) > len("clowder-backup/files/avatars/")+10 {
			avatarFiles++
		}
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("the archive lacks %s; has %v", w, got)
		}
	}
	for _, g := range got {
		if strings.Contains(g, in.stagedKey) || strings.Contains(g, "tmp") || strings.Contains(g, "notes") ||
			strings.Contains(g, "wal") || strings.Contains(g, "shm") || strings.Contains(g, ".restore") ||
			strings.Contains(g, "backup-leftover") {
			t.Errorf("the archive has %s, which a backup leaves out", g)
		}
	}
	if !slices.Contains(got, "clowder-backup/files/"+in.usedKey) {
		t.Errorf("a file in use that has a note must be in the archive")
	}
	if avatarFiles < 3 {
		t.Errorf("the avatars are missing: %v", got)
	}

	// The manifest lists every other entry, in order, with what is there.
	if len(m.Entries) != len(entries)-1 {
		t.Fatalf("manifest lists %d entries, the archive has %d besides it", len(m.Entries), len(entries)-1)
	}
	var fileCount int64
	for i, e := range entries[1:] {
		listed := m.Entries[i]
		if "clowder-backup/"+listed.Path != strings.TrimSuffix(e.header.Name, "/") {
			t.Errorf("entry %d is %s, the manifest says %s", i, e.header.Name, listed.Path)
		}
		if e.header.Typeflag == tar.TypeDir {
			if listed.Type != "dir" || e.header.Mode != 0o700 {
				t.Errorf("%s: %+v, mode %o", e.header.Name, listed, e.header.Mode)
			}
			continue
		}
		if listed.Type != "file" || listed.Size != int64(len(e.data)) || listed.SHA256 != sum(e.data) || e.header.Mode != 0o600 {
			t.Errorf("%s: manifest %+v, mode %o, size %d", e.header.Name, listed, e.header.Mode, len(e.data))
		}
		if strings.HasPrefix(listed.Path, "files/") {
			fileCount++
		}
	}
	if m.Counts.Files != fileCount {
		t.Errorf("counts.files = %d, want %d", m.Counts.Files, fileCount)
	}
	if m.Counts.Users != 7 || m.Counts.Rooms != 3 || m.Counts.Posts == 0 || m.Counts.Messages == 0 {
		t.Errorf("counts = %+v", m.Counts)
	}
	// The host key is the one of the installation.
	for _, e := range entries {
		if e.header.Name == "clowder-backup/ssh_host_ed25519_key" {
			signer, err := ssh.ParsePrivateKey(e.data)
			if err != nil || ssh.FingerprintSHA256(signer.PublicKey()) != in.fingerprint {
				t.Errorf("the host key in the archive: %v", err)
			}
		}
	}

	for _, want := range []string{out, "database: SQLite", "7 users, 3 rooms"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("the summary lacks %q:\n%s", want, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "pg_dump") {
		t.Errorf("a SQLite backup must not print a pg_dump command:\n%s", stdout.String())
	}
}

func TestBackupToStandardOutput(t *testing.T) {
	in := newInstallation(t)
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"backup", "-data", in.dir, "-o", "-"}, mapEnv(nil), &stdout, &stderr, auth.FastHasher()); err != nil {
		t.Fatalf("backup: %+v", err)
	}
	m := manifestOf(t, readArchive(t, bytes.NewReader(stdout.Bytes())))
	if m.Database != "clowder.db" {
		t.Errorf("manifest = %+v", m)
	}
	if !strings.Contains(stderr.String(), "backup written: the standard output") {
		t.Errorf("the summary must go to stderr:\n%s", stderr.String())
	}
	if left, _ := filepath.Glob(filepath.Join(in.dir, ".clowder-backup-*")); len(left) > 0 {
		t.Errorf("temporary archives left: %v", left)
	}
}

// TestBackupWhileWriting runs backup while a goroutine keeps writing posts and
// messages: the database in the archive opens, passes the integrity check and
// has every row that was committed before the backup began; every attachment
// it names is in the archive.
func TestBackupWhileWriting(t *testing.T) {
	in := newInstallation(t)
	ctx := t.Context()
	st, err := store.Open(ctx, filepath.Join(in.dir, databaseFile), clock.Real(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	alice, err := st.UserByUsername(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := st.UserByUsername(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	actor := app.Actor{UserID: alice.ID, Username: alice.Username, Role: alice.Role}
	fs := openFiles(t, in.dir)
	cfg := app.PostsConfig{MaxAttachmentSize: config.DefaultMaxAttachmentSize, MaxAttachments: config.DefaultMaxAttachmentsPerPost}
	rooms := app.NewRooms(st, fs, nil, nil)
	posts := app.NewPosts(st, clock.Real(), markdown.New(), fs, cfg, nil)
	messages := app.NewMessages(st, clock.Real(), fs, cfg, nil)
	visible, err := rooms.Visible(ctx, actor)
	if err != nil || len(visible) == 0 {
		t.Fatalf("rooms = %v, %v", visible, err)
	}
	conv, err := messages.StartDirect(ctx, actor, bob.ID)
	if err != nil {
		t.Fatal(err)
	}

	var wrotePosts, wroteMessages atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			// A failed write counts for nothing: the counters grow only after
			// the commit.
			if _, err := posts.Create(ctx, actor, visible[0].ID, app.PostInput{Format: app.FormatText, Body: "post while backing up"}); err == nil {
				wrotePosts.Add(1)
			}
			if _, err := messages.Send(ctx, actor, conv, app.MessageInput{Body: "message while backing up"}); err == nil {
				wroteMessages.Add(1)
			}
		}
	}()
	defer func() {
		close(stop)
		wg.Wait()
	}()
	for wrotePosts.Load() < 10 || wroteMessages.Load() < 10 {
		time.Sleep(time.Millisecond)
	}

	out := filepath.Join(t.TempDir(), "b.tar.gz")
	postsBefore, messagesBefore := wrotePosts.Load(), wroteMessages.Load()
	counts, err := st.BackupCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if err := run(ctx, []string{"backup", "-data", in.dir, "-o", out}, mapEnv(nil), io.Discard, &stderr, auth.FastHasher()); err != nil {
		t.Fatalf("backup: %+v\n%s", err, stderr.String())
	}
	if wrotePosts.Load() == postsBefore && wroteMessages.Load() == messagesBefore {
		t.Log("the writer did not get ahead of the backup; the test still checks the copy")
	}

	entries := readArchiveFile(t, out)
	var dbBytes []byte
	have := map[string]bool{}
	for _, e := range entries {
		have[strings.TrimPrefix(e.header.Name, "clowder-backup/")] = true
		if e.header.Name == "clowder-backup/clowder.db" {
			dbBytes = e.data
		}
	}
	copyPath := filepath.Join(t.TempDir(), "copy.db")
	if err := os.WriteFile(copyPath, dbBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", copyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity_check = %q, %v", integrity, err)
	}
	var gotPosts, gotMessages int64
	if err := db.QueryRow("SELECT COUNT(*) FROM posts").Scan(&gotPosts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM messages").Scan(&gotMessages); err != nil {
		t.Fatal(err)
	}
	// The demo has the rest of the rows; the counts read before the backup are
	// a floor of what the copy has.
	if gotPosts < counts.Posts || gotMessages < counts.Messages {
		t.Errorf("the copy has %d posts and %d messages, %d and %d were committed before the backup", gotPosts, gotMessages, counts.Posts, counts.Messages)
	}
	if gotPosts < postsBefore || gotMessages < messagesBefore {
		t.Errorf("the copy lost rows: %d posts for %d written, %d messages for %d written", gotPosts, postsBefore, gotMessages, messagesBefore)
	}
	rows, err := db.Query("SELECT storage_key FROM attachments UNION SELECT storage_key FROM message_attachments")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	named := 0
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		named++
		if !have["files/"+key] {
			t.Errorf("the copy names the attachment %s, which is not in the archive", key)
		}
	}
	if err := rows.Err(); err != nil || named == 0 {
		t.Errorf("attachments named: %d, %v", named, err)
	}
}

func TestPgDumpCommand(t *testing.T) {
	tests := []struct {
		name         string
		url          string
		want         string
		wantPassword bool
	}{
		{"with a password", "postgres://clowder:s3cr3t@db.example.com:5432/clowder?sslmode=disable",
			"pg_dump --format=custom --file=clowder.pgdump 'postgres://clowder@db.example.com:5432/clowder?sslmode=disable'", true},
		{"with a password in the query", "postgresql://db.example.com/clowder?user=c&password=s3cr3t&sslpassword=x",
			"pg_dump --format=custom --file=clowder.pgdump 'postgresql://db.example.com/clowder?user=c'", true},
		{"without a password", "postgres://clowder@db/clowder",
			"pg_dump --format=custom --file=clowder.pgdump 'postgres://clowder@db/clowder'", false},
		{"a quote in the name", "postgres://u'x:pw@db/c",
			`pg_dump --format=custom --file=clowder.pgdump 'postgres://u%27x@db/c'`, true},
		{"not a URL", "postgres://u:s3cr3t@[bad/c", "pg_dump --format=custom --file=clowder.pgdump '<database URL>'", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, left := pgDumpCommand(tt.url)
			if got != tt.want || left != tt.wantPassword {
				t.Errorf("pgDumpCommand = %q, %v; want %q, %v", got, left, tt.want, tt.wantPassword)
			}
			if strings.Contains(got, "s3cr3t") || strings.Contains(got, "pw@") {
				t.Errorf("the command has the password: %s", got)
			}
		})
	}
}

// failureWriter fails after n bytes.
type failureWriter struct{ n int }

func (w *failureWriter) Write(p []byte) (int, error) {
	if len(p) > w.n {
		return 0, errors.New("disk full")
	}
	w.n -= len(p)
	return len(p), nil
}

func TestBackupErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Log("running as root: the tests of unreadable files are skipped")
	}
	readOnly := func(t *testing.T, path string, mode os.FileMode) {
		t.Helper()
		if os.Geteuid() == 0 {
			t.Skip("root ignores file modes")
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
	}
	tests := []struct {
		name  string
		setup func(t *testing.T, in installation) (args []string)
		route string
		meta  map[string]any
	}{
		{
			name:  "no -o",
			setup: func(t *testing.T, in installation) []string { return []string{"-data", in.dir} },
			route: "run->runBackup.BackupOutputInvalid",
		},
		{
			name: "-o inside files/",
			setup: func(t *testing.T, in installation) []string {
				return []string{"-data", in.dir, "-o", filepath.Join(in.dir, "files", "avatars", "x.tar.gz")}
			},
			route: "run->runBackup->resolveBackupOutput.BackupOutputInvalid",
		},
		{
			name: "-o is files/ itself",
			setup: func(t *testing.T, in installation) []string {
				return []string{"-data", in.dir, "-o", filepath.Join(in.dir, "files")}
			},
			route: "run->runBackup->resolveBackupOutput.BackupOutputInvalid",
		},
		{
			name: "-o in a directory that does not exist",
			setup: func(t *testing.T, in installation) []string {
				return []string{"-data", in.dir, "-o", filepath.Join(t.TempDir(), "nope", "b.tar.gz")}
			},
			route: "run->runBackup->resolveBackupOutput.BackupOutputInvalid",
		},
		{
			name: "-o is a directory",
			setup: func(t *testing.T, in installation) []string {
				return []string{"-data", in.dir, "-o", t.TempDir()}
			},
			route: "run->runBackup->resolveBackupOutput.BackupOutputInvalid",
		},
		{
			name: "-o is the database",
			setup: func(t *testing.T, in installation) []string {
				return []string{"-data", in.dir, "-o", filepath.Join(in.dir, databaseFile)}
			},
			route: "run->runBackup->resolveBackupOutput.BackupOutputInvalid",
		},
		{
			name: "-o is the host key",
			setup: func(t *testing.T, in installation) []string {
				return []string{"-data", in.dir, "-o", filepath.Join(in.dir, "ssh_host_ed25519_key")}
			},
			route: "run->runBackup->resolveBackupOutput.BackupOutputInvalid",
		},
		{
			name: "the data directory does not exist",
			setup: func(t *testing.T, in installation) []string {
				return []string{"-data", filepath.Join(t.TempDir(), "nope"), "-o", filepath.Join(t.TempDir(), "b")}
			},
			route: "run->runBackup.BackupDataDirInvalid",
		},
		{
			name: "the data directory is a file",
			setup: func(t *testing.T, in installation) []string {
				return []string{"-data", filepath.Join(in.dir, databaseFile), "-o", filepath.Join(t.TempDir(), "b")}
			},
			route: "run->runBackup.BackupDataDirInvalid",
		},
		{
			name: "the data directory has no database",
			setup: func(t *testing.T, in installation) []string {
				return []string{"-data", t.TempDir(), "-o", filepath.Join(t.TempDir(), "b")}
			},
			route: "run->runBackup.BackupDataDirInvalid",
		},
		{
			name: "the database is a directory",
			setup: func(t *testing.T, in installation) []string {
				dir := t.TempDir()
				if err := os.Mkdir(filepath.Join(dir, databaseFile), 0o700); err != nil {
					t.Fatal(err)
				}
				return []string{"-data", dir, "-o", filepath.Join(t.TempDir(), "b")}
			},
			route: "run->runBackup.BackupDataDirInvalid",
		},
		{
			name: "an unreadable attachment",
			setup: func(t *testing.T, in installation) []string {
				readOnly(t, filepath.Join(in.dir, "files", in.attachmentKeys[0]), 0)
				return []string{"-data", in.dir, "-o", filepath.Join(t.TempDir(), "b")}
			},
			route: "run->runBackup->makeBackup->hashItem.BackupFilesFailed",
		},
		{
			name: "an unreadable notes directory",
			setup: func(t *testing.T, in installation) []string {
				readOnly(t, filepath.Join(in.dir, "files", "notes"), 0)
				return []string{"-data", in.dir, "-o", filepath.Join(t.TempDir(), "b")}
			},
			route: "run->runBackup->makeBackup->stagedUploads.BackupFilesFailed",
		},
		{
			name: "an unreadable files directory",
			setup: func(t *testing.T, in installation) []string {
				readOnly(t, filepath.Join(in.dir, "files", "avatars"), 0)
				return []string{"-data", in.dir, "-o", filepath.Join(t.TempDir(), "b")}
			},
			route: "run->runBackup->makeBackup->collectFiles.BackupFilesFailed",
		},
		{
			name: "an output directory that cannot take the archive",
			setup: func(t *testing.T, in installation) []string {
				out := t.TempDir()
				readOnly(t, out, 0o500)
				return []string{"-data", in.dir, "-o", filepath.Join(out, "b")}
			},
			route: "run->runBackup->makeBackup.BackupTempFailed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := newInstallation(t)
			args := tt.setup(t, in)
			var out string
			for i, a := range args {
				if a == "-o" {
					out = args[i+1]
				}
			}
			before := dirNames(t, in.dir)
			_, statErr := os.Stat(out)
			existed := statErr == nil
			err := run(t.Context(), append([]string{"backup"}, args...), mapEnv(nil), io.Discard, io.Discard, auth.FastHasher())
			appErr, ok := rmerrors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T: %v", err, err)
			}
			if !appErr.HasRoute(tt.route) {
				t.Errorf("unexpected failure path: %s\nwant it to contain %s", appErr.Route(), tt.route)
			}
			// A failed backup leaves no archive under -o, no temporary archive
			// and no copy of the database.
			if out != "" && out != "-" {
				if _, err := os.Stat(out); err == nil && !existed {
					t.Errorf("the archive %s exists after a failed backup", out)
				}
				if left, _ := filepath.Glob(filepath.Join(filepath.Dir(out), ".clowder-backup-*")); len(left) > 0 {
					t.Errorf("temporary archives left: %v", left)
				}
			}
			if strings.HasPrefix(tt.name, "the data directory") || strings.Contains(tt.name, "no -o") {
				return
			}
			if after := dirNames(t, in.dir); !slices.Equal(before, after) {
				t.Errorf("the data directory changed: %v -> %v", before, after)
			}
		})
	}
}

// dirNames lists the names in dir, except the write-ahead log and the shared
// memory file that opening the database removes.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.Name() == "clowder.db-wal" || e.Name() == "clowder.db-shm" {
			continue
		}
		names = append(names, e.Name())
	}
	return names
}

func TestBackupCanceled(t *testing.T) {
	in := newInstallation(t)
	out := filepath.Join(t.TempDir(), "b.tar.gz")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := run(ctx, []string{"backup", "-data", in.dir, "-o", out}, mapEnv(nil), io.Discard, io.Discard, auth.FastHasher())
	if err == nil {
		t.Fatal("a canceled backup must fail")
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("the archive exists after a canceled backup")
	}
}

func TestBackupFlags(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		route string
	}{
		{"an unknown flag", []string{"-nosuchflag"}, "run->runBackup.FlagsInvalid"},
		{"an argument", []string{"-o", "x", "extra"}, "run->runBackup.FlagsInvalid"},
		{"a database URL that is not PostgreSQL", []string{"-o", "x", "-database-url", "mysql://u:p@h/d"}, "run->runBackup->Config.Validate.DatabaseURLInvalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			err := run(t.Context(), append([]string{"backup"}, tt.args...), mapEnv(nil), io.Discard, &stderr, auth.FastHasher())
			appErr, ok := rmerrors.FromError(err)
			if !ok || !appErr.HasRoute(tt.route) {
				t.Fatalf("err = %v, want the route %s", err, tt.route)
			}
			if strings.Contains(stderr.String(), "p@h") {
				t.Errorf("the password is in the output:\n%s", stderr.String())
			}
		})
	}
}

func TestWriteBackupArchiveErrors(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	if err := os.WriteFile(good, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry := func(src, content string) backupItem {
		return backupItem{
			entry:   backupEntry{Path: "files/x", Type: entryFile, Size: int64(len(content)), SHA256: sum([]byte(content))},
			src:     src,
			modTime: time.Unix(1, 0),
		}
	}
	m := backupManifest{Format: 1, CreatedAt: time.Unix(1, 0).UTC()}
	tests := []struct {
		name  string
		w     io.Writer
		items []backupItem
		route string
	}{
		{"the file changed", io.Discard, []backupItem{entry(good, "different")}, "writeBackupArchive->copyBackupFile.BackupFileChanged"},
		{"the file became shorter", io.Discard, []backupItem{entry(good, "content and more")}, "writeBackupArchive->copyBackupFile.BackupFileChanged"},
		{"the file is gone", io.Discard, []backupItem{entry(filepath.Join(dir, "gone"), "content")}, "writeBackupArchive->copyBackupFile.BackupFileChanged"},
		{"the file is a directory", io.Discard, []backupItem{entry(dir, "content")}, "writeBackupArchive->copyBackupFile.BackupWriteFailed"},
		{"the writer fails at the start", &failureWriter{n: 0}, []backupItem{entry(good, "content")}, "writeBackupArchive.BackupWriteFailed"},
		{"the writer fails at the end", &failureWriter{n: 40}, nil, "writeBackupArchive.BackupWriteFailed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := writeBackupArchive(t.Context(), tt.w, m, tt.items)
			appErr, ok := rmerrors.FromError(err)
			if !ok || !appErr.HasRoute(tt.route) {
				t.Fatalf("err = %v, want the route %s", err, tt.route)
			}
		})
	}
}

// TestBackupHostKeyIsOptional checks that an installation that never ran the
// SSH server has no host key in its archive.
func TestBackupHostKeyIsOptional(t *testing.T) {
	in := newInstallation(t)
	if err := os.Remove(filepath.Join(in.dir, "ssh_host_ed25519_key")); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := run(t.Context(), []string{"backup", "-data", in.dir, "-o", out}, mapEnv(nil), io.Discard, io.Discard, auth.FastHasher()); err != nil {
		t.Fatalf("backup: %+v", err)
	}
	for _, e := range readArchiveFile(t, out) {
		if strings.Contains(e.header.Name, "ssh_host") {
			t.Errorf("the archive has %s", e.header.Name)
		}
	}
}

// TestBackupReplacesAnOlderArchive checks that -o names a file that is
// replaced whole.
func TestBackupReplacesAnOlderArchive(t *testing.T) {
	in := newInstallation(t)
	out := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := os.WriteFile(out, []byte("an older backup"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"backup", "-data", in.dir, "-o", out}, mapEnv(nil), io.Discard, io.Discard, auth.FastHasher()); err != nil {
		t.Fatalf("backup: %+v", err)
	}
	manifestOf(t, readArchiveFile(t, out))
	if info, err := os.Stat(out); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("archive = %v, %v; want mode 0600", info, err)
	}
}

// TestBackupSkipsWhatIsNotAFileOrADirectory checks that a link or a FIFO in
// files/ is left out of the archive, and that the archive only has regular
// files and directories.
func TestBackupSkipsWhatIsNotAFileOrADirectory(t *testing.T) {
	in := newInstallation(t)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside-file-content-7f3a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(in.dir, "files", "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(in.dir, "files", "dirlink")); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	out := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := run(t.Context(), []string{"backup", "-data", in.dir, "-o", out}, mapEnv(nil), io.Discard, &stderr, auth.FastHasher()); err != nil {
		t.Fatalf("backup: %+v", err)
	}
	for _, e := range readArchiveFile(t, out) {
		if strings.Contains(e.header.Name, "link") || bytes.Contains(e.data, []byte("outside-file-content-7f3a")) {
			t.Errorf("the archive has %s", e.header.Name)
		}
		if e.header.Typeflag != tar.TypeReg && e.header.Typeflag != tar.TypeDir {
			t.Errorf("%s has the type %q", e.header.Name, e.header.Typeflag)
		}
	}
}

// TestBackupWithAFilesDirectoryThatIsALink checks that a files directory that
// is a symbolic link (a volume mounted elsewhere) is followed.
func TestBackupWithAFilesDirectoryThatIsALink(t *testing.T) {
	in := newInstallation(t)
	moved := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Rename(filepath.Join(in.dir, "files"), moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, filepath.Join(in.dir, "files")); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := run(t.Context(), []string{"backup", "-data", in.dir, "-o", out}, mapEnv(nil), io.Discard, io.Discard, auth.FastHasher()); err != nil {
		t.Fatalf("backup: %+v", err)
	}
	var found bool
	for _, e := range readArchiveFile(t, out) {
		found = found || e.header.Name == "clowder-backup/files/"+in.attachmentKeys[0]
	}
	if !found {
		t.Error("the files behind the link are not in the archive")
	}
}
