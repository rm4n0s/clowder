package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/store"
)

// startServe runs serve with args until the returned function is called, and
// returns the address of its web server and its log.
func startServe(t *testing.T, args []string) (addr string, log *syncBuffer, stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	log = &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, args, mapEnv(nil), log) }()
	deadline := time.Now().Add(20 * time.Second)
	for addr == "" {
		for line := range strings.SplitSeq(log.String(), "\n") {
			var entry struct {
				Msg  string `json:"msg"`
				Addr string `json:"addr"`
			}
			if json.Unmarshal([]byte(line), &entry) == nil && entry.Msg == "server listening" {
				addr = entry.Addr
			}
		}
		select {
		case err := <-done:
			t.Fatalf("serve ended early: %+v\n%s", err, log.String())
		default:
		}
		if addr == "" && time.Now().After(deadline) {
			cancel()
			t.Fatalf("serve did not listen; log:\n%s", log.String())
		}
		if addr == "" {
			time.Sleep(10 * time.Millisecond)
		}
	}
	var once sync.Once
	return addr, log, func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("serve returned %+v", err)
				}
			case <-time.After(15 * time.Second):
				t.Error("serve did not stop")
			}
		})
	}
}

// apiGet calls a route of the API with the token and returns the status and
// the body.
func apiGet(t *testing.T, base, token, path string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, body
}

// fileHashes returns the SHA-256 of every regular file under dir, by name
// relative to dir.
func fileHashes(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestBackupRestoreRoundTrip makes a demo installation, backs it up, restores
// the archive into an empty directory and runs the server on it: a user logs
// in, reads a room, downloads an attachment and an avatar, and the host key of
// the SSH server has the fingerprint it had.
func TestBackupRestoreRoundTrip(t *testing.T) {
	in := newInstallation(t)
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := run(t.Context(), []string{"backup", "-data", in.dir, "-o", archive}, mapEnv(nil), io.Discard, io.Discard, auth.FastHasher()); err != nil {
		t.Fatalf("backup: %+v", err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	var stdout bytes.Buffer
	if err := run(t.Context(), []string{"restore", "-i", archive, "-data", restored}, mapEnv(nil), &stdout, io.Discard, auth.FastHasher()); err != nil {
		t.Fatalf("restore: %+v", err)
	}

	// The files are the files of the installation, without what a backup leaves out.
	src, dst := fileHashes(t, filepath.Join(in.dir, filesDir)), fileHashes(t, filepath.Join(restored, filesDir))
	for name, h := range dst {
		if src[name] != h {
			t.Errorf("files/%s differs from the original", name)
		}
	}
	for _, key := range in.attachmentKeys {
		if dst[key] == "" {
			t.Errorf("the attachment %s was not restored", key)
		}
	}
	if dst[in.stagedKey] != "" {
		t.Errorf("the staged upload was restored")
	}

	addr, log, stop := startServe(t, []string{"-data", restored, "-addr", "127.0.0.1:0", "-ssh-addr", "127.0.0.1:0"})
	defer stop()
	if !strings.Contains(log.String(), in.fingerprint) {
		t.Errorf("the host key of the restored installation has another fingerprint than %s:\n%s", in.fingerprint, log.String())
	}
	base := "http://" + addr
	body, err := json.Marshal(map[string]string{"username": "alice", "password": demoPassword, "client_name": "test"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.Post(base+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var login struct {
		Token string `json:"token"`
		User  struct {
			ID        uint64  `json:"id"`
			AvatarURL *string `json:"avatar_url"`
		} `json:"user"`
	}
	err = json.NewDecoder(res.Body).Decode(&login)
	_ = res.Body.Close()
	if err != nil || res.StatusCode != http.StatusOK || login.Token == "" {
		t.Fatalf("login = %d, %v, %+v", res.StatusCode, err, login)
	}

	status, raw := apiGet(t, base, login.Token, "/api/v1/rooms")
	var rooms struct {
		Rooms []struct {
			ID   uint64 `json:"id"`
			Name string `json:"name"`
		} `json:"rooms"`
	}
	if err := json.Unmarshal(raw, &rooms); err != nil || status != http.StatusOK {
		t.Fatalf("rooms = %d, %v, %s", status, err, raw)
	}
	var general uint64
	for _, r := range rooms.Rooms {
		if r.Name == "general" {
			general = r.ID
		}
	}
	if general == 0 {
		t.Fatalf("no room general in %s", raw)
	}
	status, raw = apiGet(t, base, login.Token, "/api/v1/rooms/"+strconv.FormatUint(general, 10)+"/posts")
	var feed struct {
		Posts []struct {
			Body        string `json:"body"`
			Attachments []struct {
				Size int64  `json:"size"`
				URL  string `json:"url"`
			} `json:"attachments"`
		} `json:"posts"`
	}
	if err := json.Unmarshal(raw, &feed); err != nil || status != http.StatusOK || len(feed.Posts) == 0 {
		t.Fatalf("feed = %d, %v, %s", status, err, raw)
	}
	downloaded := 0
	for _, p := range feed.Posts {
		for _, a := range p.Attachments {
			status, data := apiGet(t, base, login.Token, a.URL)
			sum := sha256.Sum256(data)
			if status != http.StatusOK || int64(len(data)) != a.Size {
				t.Errorf("attachment %s = %d, %d bytes (size %d)", a.URL, status, len(data), a.Size)
			}
			found := false
			for _, key := range in.attachmentKeys {
				found = found || src[key] == hex.EncodeToString(sum[:])
			}
			if !found {
				t.Errorf("attachment %s is not one of the files of the original", a.URL)
			}
			downloaded++
		}
	}
	if downloaded == 0 {
		t.Error("the room has no attachment to download")
	}
	if login.User.AvatarURL == nil {
		t.Fatal("alice has no avatar in the demo")
	}
	status, data := apiGet(t, base, login.Token, *login.User.AvatarURL)
	sum := sha256.Sum256(data)
	if status != http.StatusOK || !bytes.HasPrefix(data, []byte("\x89PNG")) || src["avatars/"+avatarKeyOf(t, *login.User.AvatarURL)] != hex.EncodeToString(sum[:]) {
		t.Errorf("avatar = %d, %d bytes", status, len(data))
	}
	stop()

	// The restored database has the rows of the original.
	counts := func(dir string) store.BackupCounts {
		st, err := store.Open(t.Context(), filepath.Join(dir, databaseFile), clock.Real(), slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = st.Close() }()
		c, err := st.BackupCounts(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if a, b := counts(in.dir), counts(restored); a != b {
		t.Errorf("counts differ: original %+v, restored %+v", a, b)
	}
}

// avatarKeyOf returns the key in an avatar URL, /api/v1/avatars/<id>?v=<key>.
func avatarKeyOf(t *testing.T, avatarURL string) string {
	t.Helper()
	u, err := url.Parse(avatarURL)
	if err != nil || u.Query().Get("v") == "" {
		t.Fatalf("avatar URL %q: %v", avatarURL, err)
	}
	return u.Query().Get("v")
}

// TestBackupPostgres checks a backup on PostgreSQL (scripts/test-docker.sh):
// the archive has the files and the host key and no database, the manifest
// says the database is external, and the pg_dump line has no password.
func TestBackupPostgres(t *testing.T) {
	pgURL := os.Getenv(store.PostgresTestEnv)
	if pgURL == "" {
		t.Skip("CLOWDER_TEST_POSTGRES is not set (scripts/test-docker.sh sets it)")
	}
	dbURL, drop, err := store.NewPostgresDatabase(t.Context(), pgURL)
	if err != nil {
		t.Fatalf("NewPostgresDatabase: %+v", err)
	}
	t.Cleanup(func() {
		if err := drop(); err != nil {
			t.Errorf("drop: %+v", err)
		}
	})
	in := newInstallation(t)
	// The SQLite database of the demo must not be backed up: the URL is used.
	archive := filepath.Join(t.TempDir(), "b.tar.gz")
	var stdout, stderr bytes.Buffer
	env := mapEnv(map[string]string{"CLOWDER_DATABASE_URL": dbURL})
	if err := run(t.Context(), []string{"backup", "-data", in.dir, "-o", archive}, env, &stdout, &stderr, auth.FastHasher()); err != nil {
		t.Fatalf("backup: %+v\n%s", err, stderr.String())
	}
	entries := readArchiveFile(t, archive)
	m := manifestOf(t, entries)
	if m.Dialect != "postgres" || m.Database != "external" || m.Schema != store.LatestMigration() {
		t.Errorf("manifest = %+v", m)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.header.Name)
		if strings.HasSuffix(e.header.Name, "clowder.db") {
			t.Errorf("the archive has a database: %s", e.header.Name)
		}
	}
	// The first attachment has a note and the database has no row for it: it is
	// a staged upload as far as this database knows, and a backup leaves it out.
	if !strings.Contains(strings.Join(names, " "), "ssh_host_ed25519_key") || !strings.Contains(strings.Join(names, " "), "files/"+in.attachmentKeys[1]) {
		t.Errorf("the archive lacks the host key or the files: %v", names)
	}
	out := stdout.String()
	if !strings.Contains(out, "pg_dump --format=custom") || !strings.Contains(out, "PostgreSQL, not in the archive") {
		t.Errorf("the summary lacks the pg_dump line:\n%s", out)
	}
	if u, err := url.Parse(dbURL); err == nil && u.User != nil {
		// The password may be a word the output has for other reasons, so look
		// for it where a URL has it.
		if pw, ok := u.User.Password(); ok && pw != "" && (strings.Contains(out, ":"+pw+"@") || strings.Contains(stderr.String(), ":"+pw+"@")) {
			t.Errorf("the password of the database URL is in the output:\n%s\n%s", out, stderr.String())
		}
	}
	// And the restore says that the database comes first.
	target := filepath.Join(t.TempDir(), "data")
	var restoreOut bytes.Buffer
	if err := run(t.Context(), []string{"restore", "-i", archive, "-data", target}, mapEnv(nil), &restoreOut, io.Discard, auth.FastHasher()); err != nil {
		t.Fatalf("restore: %+v", err)
	}
	if !strings.Contains(restoreOut.String(), "pg_restore") {
		t.Errorf("restore output:\n%s", restoreOut.String())
	}
}
