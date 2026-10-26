package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// mapEnv returns an env function backed by a map.
func mapEnv(m map[string]string) func(string) string {
	return func(key string) string { return m[key] }
}

func TestRunDispatch(t *testing.T) {
	// A file, so that serve fails at the data directory and never listens.
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		args       []string
		wantTag    string
		wantRoute  string
		wantMeta   map[string]any
		wantUsage  bool
		wantStderr string
	}{
		{
			name:      "no command",
			args:      nil,
			wantTag:   tagUnknownCommand,
			wantRoute: "run.UnknownCommand",
			wantMeta:  map[string]any{"command": ""},
			wantUsage: true,
		},
		{
			name:      "unknown command",
			args:      []string{"frobnicate"},
			wantTag:   tagUnknownCommand,
			wantRoute: "run.UnknownCommand",
			wantMeta:  map[string]any{"command": "frobnicate"},
			wantUsage: true,
		},
		{
			name:      "serve with a bad data directory",
			args:      []string{"serve", "-data", filepath.Join(notADir, "sub")},
			wantTag:   tagDataDirCreateFailed,
			wantRoute: "run->runServe.DataDirCreateFailed",
			wantMeta:  map[string]any{"dir": filepath.Join(notADir, "sub")},
		},
		{
			name:      "serve with a bad flag",
			args:      []string{"serve", "-nope"},
			wantTag:   tagFlagsInvalid,
			wantRoute: "run->runServe->parseServeConfig.FlagsInvalid",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(context.Background(), tt.args, mapEnv(nil), &stdout, &stderr, auth.FastHasher())
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T: %v", err, err)
			}
			if appErr.Tag != tt.wantTag {
				t.Errorf("tag = %q, want %q", appErr.Tag, tt.wantTag)
			}
			if !appErr.HasRoute(tt.wantRoute) {
				t.Errorf("unexpected failure path: %s, want %s", appErr.Route(), tt.wantRoute)
			}
			for k, want := range tt.wantMeta {
				if got := appErr.Metadata[k]; got != want {
					t.Errorf("metadata %q = %v, want %v", k, got, want)
				}
			}
			if gotUsage := strings.Contains(stderr.String(), "usage: clowder"); gotUsage != tt.wantUsage {
				t.Errorf("usage printed = %v, want %v; stderr:\n%s", gotUsage, tt.wantUsage, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("unexpected stdout: %q", stdout.String())
			}
		})
	}
}

func TestParseServeConfig(t *testing.T) {
	def := config.Default()
	tests := []struct {
		name string
		args []string
		env  map[string]string
		want config.Config
	}{
		{
			name: "defaults",
			want: def,
		},
		{
			name: "environment only",
			env: map[string]string{
				"CLOWDER_ADDR":                "127.0.0.1:9000",
				"CLOWDER_DATA":                "/var/lib/clowder",
				"CLOWDER_BASE_URL":            "https://chat.example.com",
				"CLOWDER_MAX_ATTACHMENT_SIZE": "1048576",
				"CLOWDER_MAX_ATTACHMENTS":     "3",
				"CLOWDER_SESSION_LIFETIME":    "48h",
				"CLOWDER_INVITATION_LIFETIME": "24h",
				"CLOWDER_LOG_LEVEL":           "debug",
			},
			want: config.Config{
				ListenAddr:            "127.0.0.1:9000",
				DataDir:               "/var/lib/clowder",
				BaseURL:               "https://chat.example.com",
				MaxAttachmentSize:     1048576,
				MaxAttachmentsPerPost: 3,
				SessionLifetime:       48 * time.Hour,
				InvitationLifetime:    24 * time.Hour,
				LogLevel:              "debug",
			},
		},
		{
			name: "flags only",
			args: []string{
				"-addr", "127.0.0.1:9001", "-data", "d", "-base-url", "http://x.test",
				"-max-attachment-size", "10", "-max-attachments", "2",
				"-session-lifetime", "1h", "-invitation-lifetime", "30m", "-log-level", "warn",
			},
			want: config.Config{
				ListenAddr:            "127.0.0.1:9001",
				DataDir:               "d",
				BaseURL:               "http://x.test",
				MaxAttachmentSize:     10,
				MaxAttachmentsPerPost: 2,
				SessionLifetime:       time.Hour,
				InvitationLifetime:    30 * time.Minute,
				LogLevel:              "warn",
			},
		},
		{
			name: "flag beats environment, environment beats default",
			args: []string{"-addr", ":1111", "-max-attachments", "7"},
			env: map[string]string{
				"CLOWDER_ADDR":            ":2222",
				"CLOWDER_MAX_ATTACHMENTS": "3",
				"CLOWDER_LOG_LEVEL":       "error",
			},
			want: func() config.Config {
				c := def
				c.ListenAddr = ":1111"
				c.MaxAttachmentsPerPost = 7
				c.LogLevel = "error"
				return c
			}(),
		},
		{
			name: "empty environment value is unset",
			env:  map[string]string{"CLOWDER_ADDR": "", "CLOWDER_SESSION_LIFETIME": ""},
			want: def,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			got, err := parseServeConfig(tt.args, mapEnv(tt.env), &stderr)
			if err != nil {
				t.Fatalf("parseServeConfig: %v", err)
			}
			if got != tt.want {
				t.Errorf("config = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseServeConfigErrors(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		env       map[string]string
		wantTag   string
		wantRoute string
		wantMeta  map[string]any
	}{
		{
			name:      "malformed size in the environment",
			env:       map[string]string{"CLOWDER_MAX_ATTACHMENT_SIZE": "big"},
			wantTag:   tagEnvValueInvalid,
			wantRoute: "parseServeConfig->envInt64.EnvValueInvalid",
			wantMeta:  map[string]any{"name": "CLOWDER_MAX_ATTACHMENT_SIZE", "value": "big"},
		},
		{
			name:      "malformed count in the environment",
			env:       map[string]string{"CLOWDER_MAX_ATTACHMENTS": "1.5"},
			wantTag:   tagEnvValueInvalid,
			wantRoute: "parseServeConfig->envInt.EnvValueInvalid",
			wantMeta:  map[string]any{"name": "CLOWDER_MAX_ATTACHMENTS", "value": "1.5"},
		},
		{
			name:      "malformed session lifetime in the environment",
			env:       map[string]string{"CLOWDER_SESSION_LIFETIME": "30 days"},
			wantTag:   tagEnvValueInvalid,
			wantRoute: "parseServeConfig->envDuration.EnvValueInvalid",
			wantMeta:  map[string]any{"name": "CLOWDER_SESSION_LIFETIME", "value": "30 days"},
		},
		{
			name:      "malformed invitation lifetime in the environment",
			env:       map[string]string{"CLOWDER_INVITATION_LIFETIME": "soon"},
			wantTag:   tagEnvValueInvalid,
			wantRoute: "parseServeConfig->envDuration.EnvValueInvalid",
			wantMeta:  map[string]any{"name": "CLOWDER_INVITATION_LIFETIME", "value": "soon"},
		},
		{
			name:      "a flag does not hide a malformed environment value",
			args:      []string{"-max-attachments", "2"},
			env:       map[string]string{"CLOWDER_MAX_ATTACHMENTS": "x"},
			wantTag:   tagEnvValueInvalid,
			wantRoute: "parseServeConfig->envInt.EnvValueInvalid",
		},
		{
			name:      "unknown flag",
			args:      []string{"-nope"},
			wantTag:   tagFlagsInvalid,
			wantRoute: "parseServeConfig.FlagsInvalid",
		},
		{
			name:      "malformed flag value",
			args:      []string{"-session-lifetime", "forever"},
			wantTag:   tagFlagsInvalid,
			wantRoute: "parseServeConfig.FlagsInvalid",
		},
		{
			name:      "unexpected argument",
			args:      []string{"extra"},
			wantTag:   tagFlagsInvalid,
			wantRoute: "parseServeConfig.FlagsInvalid",
			wantMeta:  map[string]any{"args": []string{"extra"}},
		},
		{
			name:      "invalid config: log level",
			args:      []string{"-log-level", "loud"},
			wantTag:   config.TagLogLevelUnknown,
			wantRoute: "parseServeConfig->Config.Validate.LogLevelUnknown",
		},
		{
			name:      "invalid config: base URL from the environment",
			env:       map[string]string{"CLOWDER_BASE_URL": "chat.example.com"},
			wantTag:   config.TagBaseURLInvalid,
			wantRoute: "parseServeConfig->Config.Validate.BaseURLInvalid",
		},
		{
			name:      "invalid config: session lifetime",
			args:      []string{"-session-lifetime", "0s"},
			wantTag:   config.TagSessionLifetimeInvalid,
			wantRoute: "parseServeConfig->Config.Validate.SessionLifetimeInvalid",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			_, err := parseServeConfig(tt.args, mapEnv(tt.env), &stderr)
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T: %v", err, err)
			}
			if appErr.Tag != tt.wantTag {
				t.Errorf("tag = %q, want %q", appErr.Tag, tt.wantTag)
			}
			if !appErr.HasRoute(tt.wantRoute) {
				t.Errorf("unexpected failure path: %s, want %s", appErr.Route(), tt.wantRoute)
			}
			for k, want := range tt.wantMeta {
				got := appErr.Metadata[k]
				if g, ok := got.([]string); ok {
					got = strings.Join(g, ",")
					want = strings.Join(want.([]string), ",")
				}
				if got != want {
					t.Errorf("metadata %q = %v, want %v", k, got, want)
				}
			}
		})
	}
}

func TestNewLogger(t *testing.T) {
	tests := []struct {
		level     string
		wantDebug bool
		wantInfo  bool
		wantWarn  bool
		wantError bool
	}{
		{level: "debug", wantDebug: true, wantInfo: true, wantWarn: true, wantError: true},
		{level: "info", wantInfo: true, wantWarn: true, wantError: true},
		{level: "warn", wantWarn: true, wantError: true},
		{level: "error", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			var buf bytes.Buffer
			logger := newLogger(tt.level, &buf)
			logger.Debug("d-msg")
			logger.Info("i-msg")
			logger.Warn("w-msg")
			logger.Error("e-msg")
			out := buf.String()
			for _, c := range []struct {
				msg  string
				want bool
			}{{"d-msg", tt.wantDebug}, {"i-msg", tt.wantInfo}, {"w-msg", tt.wantWarn}, {"e-msg", tt.wantError}} {
				if got := strings.Contains(out, c.msg); got != c.want {
					t.Errorf("%s logged = %v, want %v", c.msg, got, c.want)
				}
			}
			if !strings.HasPrefix(out, "{") {
				t.Errorf("log is not JSON: %q", out)
			}
		})
	}
}

func TestRunDemo(t *testing.T) {
	// A file, so that demo cannot read it as a directory.
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		setup     func(t *testing.T) (dir string) // returns the data directory
		args      func(dir string) []string
		env       func(dir string) map[string]string
		wantTag   string
		wantRoute string
	}{
		{
			name:  "missing directory is created",
			setup: func(t *testing.T) string { return filepath.Join(t.TempDir(), "a", "b") },
		},
		{
			name:  "empty directory is fine",
			setup: func(t *testing.T) string { return t.TempDir() },
		},
		{
			name:  "directory from the environment",
			setup: func(t *testing.T) string { return filepath.Join(t.TempDir(), "env") },
			args:  func(string) []string { return nil },
			env:   func(dir string) map[string]string { return map[string]string{"CLOWDER_DATA": dir} },
		},
		{
			name: "non-empty directory",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "clowder.db"), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			wantTag:   tagDataDirNotEmpty,
			wantRoute: "run->runDemo.DataDirNotEmpty",
		},
		{
			name:      "data path is a file",
			setup:     func(*testing.T) string { return notADir },
			wantTag:   tagDataDirCreateFailed,
			wantRoute: "run->runDemo.DataDirCreateFailed",
		},
		{
			name: "data directory cannot be read",
			setup: func(t *testing.T) string {
				dir := filepath.Join(t.TempDir(), "locked")
				if err := os.Mkdir(dir, 0o000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
				if _, err := os.ReadDir(dir); err == nil {
					t.Skip("permissions are not enforced (running as root?)")
				}
				return dir
			},
			wantTag:   tagDataDirReadFailed,
			wantRoute: "run->runDemo.DataDirReadFailed",
		},
		{
			name:      "data directory cannot be created",
			setup:     func(*testing.T) string { return filepath.Join(notADir, "sub") },
			wantTag:   tagDataDirCreateFailed,
			wantRoute: "run->runDemo.DataDirCreateFailed",
		},
		{
			name:      "bad flag",
			setup:     func(t *testing.T) string { return t.TempDir() },
			args:      func(string) []string { return []string{"-nope"} },
			wantTag:   tagFlagsInvalid,
			wantRoute: "run->runDemo.FlagsInvalid",
		},
		{
			name:      "unexpected argument",
			setup:     func(t *testing.T) string { return t.TempDir() },
			args:      func(dir string) []string { return []string{"-data", dir, "extra"} },
			wantTag:   tagFlagsInvalid,
			wantRoute: "run->runDemo.FlagsInvalid",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := tt.setup(t)
			args := []string{"demo", "-data", dir}
			if tt.args != nil {
				args = append([]string{"demo"}, tt.args(dir)...)
			}
			var env map[string]string
			if tt.env != nil {
				env = tt.env(dir)
			}
			var stdout, stderr bytes.Buffer
			err := run(context.Background(), args, mapEnv(env), &stdout, &stderr, auth.FastHasher())
			if tt.wantTag == "" {
				if err != nil {
					t.Fatalf("run demo: %v", err)
				}
				if st, statErr := os.Stat(dir); statErr != nil || !st.IsDir() {
					t.Errorf("data directory %s was not made: %v", dir, statErr)
				}
				out := stdout.String()
				if !strings.HasPrefix(out, "demo data directory is ready") ||
					!strings.Contains(out, demoPassword) {
					t.Errorf("stdout = %q, want the directory and the password", out)
				}
				return
			}
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T: %v", err, err)
			}
			if appErr.Tag != tt.wantTag {
				t.Errorf("tag = %q, want %q", appErr.Tag, tt.wantTag)
			}
			if !appErr.HasRoute(tt.wantRoute) {
				t.Errorf("unexpected failure path: %s, want %s", appErr.Route(), tt.wantRoute)
			}
			if stdout.Len() != 0 {
				t.Errorf("unexpected stdout: %q", stdout.String())
			}
		})
	}
}

func TestRunDemoMakesTheDemoUsers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	hasher := auth.FastHasher()
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"demo", "-data", dir}, mapEnv(nil), &stdout, &stderr, hasher); err != nil {
		t.Fatalf("demo: %+v", err)
	}
	for _, line := range []string{"admin  admin  Admin", "alice  member Alice", "bob    member Bob", "carol  member Carol"} {
		if !strings.Contains(stdout.String(), line) {
			t.Errorf("stdout lacks %q:\n%s", line, stdout.String())
		}
	}

	accounts, st := openAccounts(t, dir)
	admin, err := accounts.Users(t.Context(), app.Actor{Role: store.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, u := range admin {
		got = append(got, u.Username)
	}
	if want := "admin,alice,bob,carol"; strings.Join(got, ",") != want {
		t.Fatalf("users = %v, want %s", got, want)
	}
	for _, d := range demoUsers {
		row, err := st.UserByUsername(t.Context(), d.username)
		if err != nil {
			t.Fatal(err)
		}
		if row.Role != d.role || row.DisplayName != d.displayName || row.DisabledAt != nil {
			t.Errorf("%s = %+v", d.username, row)
		}
		if ok, err := hasher.Verify("demo-password-1234", row.PasswordHash); err != nil || !ok {
			t.Errorf("%s: the password does not verify: %v, %v", d.username, ok, err)
		}
	}

	// The database is there now, so the directory is no longer empty.
	err = run(t.Context(), []string{"demo", "-data", dir}, mapEnv(nil), &stdout, &stderr, hasher)
	appErr, ok := errors.FromError(err)
	if !ok || appErr.Tag != tagDataDirNotEmpty || !appErr.HasRoute("run->runDemo.DataDirNotEmpty") {
		t.Errorf("second demo: %v", err)
	}
}

func TestRunDemoStoreOpenFailure(t *testing.T) {
	// A closed context fails the open of the database, after the directory is ready.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var stdout, stderr bytes.Buffer
	err := run(ctx, []string{"demo", "-data", t.TempDir()}, mapEnv(nil), &stdout, &stderr, auth.FastHasher())
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T: %v", err, err)
	}
	if !appErr.HasRoute("run->runDemo->Open->open") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}
}

func TestServeOpensAndClosesTheStore(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, databaseFile)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var stderr syncBuffer
	done := make(chan error, 1)
	go func() {
		done <- runServe(ctx, []string{"-data", dataDir, "-addr", "127.0.0.1:0"}, mapEnv(nil), &stderr)
	}()
	// The server logs that it listens after the store is open.
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(stderr.String(), "server listening") {
		select {
		case err := <-done:
			t.Fatalf("serve ended early: %+v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("serve did not start; stderr:\n%s", stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("database file not created while serving: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned %+v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop")
	}
	// Closing the last connection of a WAL database removes its -wal file.
	if _, err := os.Stat(dbPath + "-wal"); !os.IsNotExist(err) {
		t.Errorf("the store was not closed: stat of the -wal file = %v", err)
	}
	if strings.Contains(stderr.String(), "closing the store failed") {
		t.Errorf("close error logged:\n%s", stderr.String())
	}
}

func TestServeStoreOpenFailure(t *testing.T) {
	dataDir := t.TempDir()
	// A directory where the database file should be.
	if err := os.Mkdir(filepath.Join(dataDir, databaseFile), 0o750); err != nil {
		t.Fatal(err)
	}
	var stderr syncBuffer
	err := runServe(t.Context(), []string{"-data", dataDir, "-addr", "127.0.0.1:0"}, mapEnv(nil), &stderr)
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T: %v", err, err)
	}
	if !appErr.HasRoute("runServe->Open->open.StoreOpenFailed") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}
}

// syncBuffer is a bytes.Buffer that the logger of a running server and the
// test can use at the same time.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
