package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/png" // the decoder for TestDemoFile
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/markdown"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// openFiles opens the files directory of the data directory dir.
func openFiles(t *testing.T, dir string) *files.Store {
	t.Helper()
	fs, err := files.New(filepath.Join(dir, "files"))
	if err != nil {
		t.Fatalf("files.New: %+v", err)
	}
	return fs
}

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
				"CLOWDER_TRUST_PROXY":         "true",
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
				TrustProxyHeaders:     true,
			},
		},
		{
			name: "flags only",
			args: []string{
				"-addr", "127.0.0.1:9001", "-data", "d", "-base-url", "http://x.test",
				"-max-attachment-size", "10", "-max-attachments", "2",
				"-session-lifetime", "1h", "-invitation-lifetime", "30m", "-log-level", "warn",
				"-trust-proxy",
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
				TrustProxyHeaders:     true,
			},
		},
		{
			name: "trust proxy: flag beats environment",
			args: []string{"-trust-proxy=false"},
			env:  map[string]string{"CLOWDER_TRUST_PROXY": "1"},
			want: def,
		},
		{
			name: "trust proxy: environment 1",
			env:  map[string]string{"CLOWDER_TRUST_PROXY": "1"},
			want: func() config.Config {
				c := def
				c.TrustProxyHeaders = true
				return c
			}(),
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
		{
			name: "database URL from the environment",
			env:  map[string]string{"CLOWDER_DATABASE_URL": "postgres://u:p@db:5432/clowder"},
			want: func() config.Config {
				c := def
				c.DatabaseURL = "postgres://u:p@db:5432/clowder"
				return c
			}(),
		},
		{
			name: "database URL: flag beats environment",
			args: []string{"-database-url", "postgresql://flag/clowder"},
			env:  map[string]string{"CLOWDER_DATABASE_URL": "postgres://env/clowder"},
			want: func() config.Config {
				c := def
				c.DatabaseURL = "postgresql://flag/clowder"
				return c
			}(),
		},
		{
			name: "Redis URL from the environment",
			env: map[string]string{
				"CLOWDER_DATABASE_URL": "postgres://u:p@db:5432/clowder",
				"CLOWDER_REDIS_URL":    "redis://:p@redis:6379/0",
			},
			want: func() config.Config {
				c := def
				c.DatabaseURL = "postgres://u:p@db:5432/clowder"
				c.RedisURL = "redis://:p@redis:6379/0"
				return c
			}(),
		},
		{
			name: "Redis URL: flag beats environment",
			args: []string{"-database-url", "postgres://db/clowder", "-redis-url", "rediss://flag:6380"},
			env:  map[string]string{"CLOWDER_REDIS_URL": "redis://env:6379"},
			want: func() config.Config {
				c := def
				c.DatabaseURL = "postgres://db/clowder"
				c.RedisURL = "rediss://flag:6380"
				return c
			}(),
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
			name:      "malformed trust proxy in the environment",
			env:       map[string]string{"CLOWDER_TRUST_PROXY": "maybe"},
			wantTag:   tagEnvValueInvalid,
			wantRoute: "parseServeConfig->envBool.EnvValueInvalid",
			wantMeta:  map[string]any{"name": "CLOWDER_TRUST_PROXY", "value": "maybe"},
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
			name:      "invalid config: database URL flag",
			args:      []string{"-database-url", "mysql://u:secret@db/clowder"},
			wantTag:   config.TagDatabaseURLInvalid,
			wantRoute: "parseServeConfig->Config.Validate.DatabaseURLInvalid",
			wantMeta:  map[string]any{"databaseURL": "mysql://u:xxxxx@db/clowder"},
		},
		{
			name:      "invalid config: database URL from the environment",
			env:       map[string]string{"CLOWDER_DATABASE_URL": "clowder.db"},
			wantTag:   config.TagDatabaseURLInvalid,
			wantRoute: "parseServeConfig->Config.Validate.DatabaseURLInvalid",
		},
		{
			name:      "invalid config: Redis URL flag",
			args:      []string{"-database-url", "postgres://db/clowder", "-redis-url", "http://:secret@redis"},
			wantTag:   config.TagRedisURLInvalid,
			wantRoute: "parseServeConfig->Config.Validate.RedisURLInvalid",
			wantMeta:  map[string]any{"redisURL": "http://:xxxxx@redis"},
		},
		{
			name:      "invalid config: Redis URL without PostgreSQL",
			env:       map[string]string{"CLOWDER_REDIS_URL": "redis://:secret@redis:6379/0"},
			wantTag:   config.TagRedisNeedsPostgres,
			wantRoute: "parseServeConfig->Config.Validate.RedisNeedsPostgres",
			wantMeta:  map[string]any{"redisURL": "redis://:xxxxx@redis:6379/0"},
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

func TestRunDemoMakesTheDemoRooms(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"demo", "-data", dir}, mapEnv(nil), &stdout, &stderr, auth.FastHasher()); err != nil {
		t.Fatalf("demo: %+v", err)
	}
	for _, line := range []string{"demo rooms:", "general  visible Everything", "random   visible Off topic", "secret   private Just for a few of us"} {
		if !strings.Contains(stdout.String(), line) {
			t.Errorf("stdout lacks %q:\n%s", line, stdout.String())
		}
	}

	_, st := openAccounts(t, dir)
	rooms := app.NewRooms(st, openFiles(t, dir), nil, nil)
	actors := map[string]app.Actor{}
	for _, d := range demoUsers {
		row, err := st.UserByUsername(t.Context(), d.username)
		if err != nil {
			t.Fatal(err)
		}
		actors[d.username] = app.Actor{UserID: row.ID, Username: row.Username, Role: row.Role}
	}

	type row struct {
		name       string
		visibility app.Visibility
		count      int
	}
	rowsOf := func(list []app.Room, err error) []row {
		t.Helper()
		if err != nil {
			t.Fatalf("%+v", err)
		}
		out := []row{}
		for _, r := range list {
			out = append(out, row{r.Name, r.Visibility, r.MemberCount})
		}
		return out
	}
	general := row{"general", app.VisibilityVisible, 4}
	random := row{"random", app.VisibilityVisible, 3}
	secret := row{"secret", app.VisibilityPrivate, 2}
	tests := []struct {
		name string
		got  []row
		want []row
	}{
		{"admin sees the visible rooms", rowsOf(rooms.Visible(t.Context(), actors["admin"])), []row{general, random}},
		{"admin is in general and random", rowsOf(rooms.Mine(t.Context(), actors["admin"])), []row{general, random}},
		{"alice is in all three", rowsOf(rooms.Mine(t.Context(), actors["alice"])), []row{general, random, secret}},
		{"bob is in all three", rowsOf(rooms.Mine(t.Context(), actors["bob"])), []row{general, random, secret}},
		{"carol is in general only", rowsOf(rooms.Mine(t.Context(), actors["carol"])), []row{general}},
		{"admin lists every room", rowsOf(rooms.AllRooms(t.Context(), actors["admin"])), []row{general, random, secret}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.got, tt.want) {
				t.Errorf("rooms = %v, want %v", tt.got, tt.want)
			}
		})
	}

	// secret was made by alice, who added bob; the three columns exist.
	all, err := rooms.AllRooms(t.Context(), actors["admin"])
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range all {
		cols, err := st.ListBoardColumns(t.Context(), r.ID)
		if err != nil || len(cols) != 3 {
			t.Errorf("%s: columns = %v, %v, want 3", r.Name, cols, err)
		}
		if r.Name == "secret" && r.CreatedBy != actors["alice"].UserID {
			t.Errorf("secret was made by user %d, want alice", r.CreatedBy)
		}
		if r.Name != "secret" && r.CreatedBy != actors["admin"].UserID {
			t.Errorf("%s was made by user %d, want admin", r.Name, r.CreatedBy)
		}
	}
	members, err := rooms.Members(t.Context(), actors["bob"], all[2].ID)
	if err != nil || len(members) != 2 || members[1].AddedByDisplayName != "Alice" {
		t.Errorf("secret members = %+v, %v, want alice and bob (added by Alice)", members, err)
	}
	if _, err := rooms.Get(t.Context(), actors["carol"], all[2].ID); err == nil {
		t.Error("carol reads the private room")
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
	if !appErr.HasRoute("run->runDemo->fillDemo->openStore->openStoreWith->Open->open") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}
}

func TestRunDemoDatabaseURL(t *testing.T) {
	// A URL that is not valid is refused before anything is made.
	dir := filepath.Join(t.TempDir(), "demo")
	var stdout, stderr bytes.Buffer
	err := run(t.Context(), []string{"demo", "-data", dir, "-database-url", "mysql://u:secret@db/clowder"}, mapEnv(nil), &stdout, &stderr, auth.FastHasher())
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T: %v", err, err)
	}
	if appErr.Tag != config.TagDatabaseURLInvalid || !appErr.HasRoute("run->runDemo->Config.Validate.DatabaseURLInvalid") {
		t.Errorf("tag %s, path %s", appErr.Tag, appErr.Route())
	}
	if _, statErr := os.Stat(dir); statErr == nil {
		t.Error("the data directory was made")
	}
	// The same from the environment.
	err = run(t.Context(), []string{"demo", "-data", dir}, mapEnv(map[string]string{"CLOWDER_DATABASE_URL": "x"}), &stdout, &stderr, auth.FastHasher())
	if appErr, ok := errors.FromError(err); !ok || appErr.Tag != config.TagDatabaseURLInvalid {
		t.Errorf("environment: %v", err)
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
	if info, err := os.Stat(filepath.Join(dataDir, filesDir, "tmp")); err != nil || !info.IsDir() {
		t.Errorf("files/tmp not created while serving: %v, %v", info, err)
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

func TestServeFilesDirectoryFailure(t *testing.T) {
	dataDir := t.TempDir()
	// A file where the files directory should be.
	if err := os.WriteFile(filepath.Join(dataDir, filesDir), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr syncBuffer
	err := runServe(t.Context(), []string{"-data", dataDir, "-addr", "127.0.0.1:0"}, mapEnv(nil), &stderr)
	appErr, ok := errors.FromError(err)
	if !ok || !appErr.HasRoute("runServe->New.FilesDirCreateFailed") {
		t.Errorf("err = %v", err)
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
	if !appErr.HasRoute("runServe->openStore->openStoreWith->Open->open.StoreOpenFailed") {
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

func TestRunDemoMakesTheDemoAttachments(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"demo", "-data", dir}, mapEnv(nil), &stdout, &stderr, auth.FastHasher()); err != nil {
		t.Fatalf("demo: %+v", err)
	}
	if !strings.Contains(stdout.String(), "demo attachments: 2") {
		t.Errorf("stdout lacks the number of attachments:\n%s", stdout.String())
	}

	_, st := openAccounts(t, dir)
	fileStore := openFiles(t, dir)
	posts := app.NewPosts(st, clock.Real(), markdown.New(), fileStore, app.PostsConfig{}, nil)
	reader := app.Actor{UserID: 1, Username: "admin", Role: store.RoleAdmin}
	rooms, err := st.ListRooms(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var general uint64
	for _, r := range rooms {
		if r.Name == "general" {
			general = r.ID
		}
	}
	page, err := posts.Feed(t.Context(), reader, general, 0, 30)
	if err != nil {
		t.Fatalf("Feed: %+v", err)
	}
	var attached []app.AttachmentView
	for _, p := range page.Posts {
		attached = append(attached, p.Attachments...)
	}
	if len(attached) != 2 {
		t.Fatalf("general has %d attachments, want 2: %+v", len(attached), attached)
	}
	byName := map[string]app.AttachmentView{}
	for _, a := range attached {
		byName[a.FileName] = a
	}
	img, txt := byName["mockup.png"], byName["notes.txt"]
	if !img.IsImage || img.ContentType != "image/png" || img.Width != 320 || img.Height != 200 {
		t.Errorf("mockup.png = %+v", img)
	}
	if txt.IsImage || !strings.HasPrefix(txt.ContentType, "text/plain") || txt.Size == 0 {
		t.Errorf("notes.txt = %+v", txt)
	}
	for _, a := range attached {
		view, f, err := posts.Attachment(t.Context(), reader, a.ID)
		if err != nil {
			t.Fatalf("Attachment %s: %+v", a.FileName, err)
		}
		b, err := io.ReadAll(f)
		if cerr := f.Close(); err != nil || cerr != nil {
			t.Fatalf("read %s: %v, %v", a.FileName, err, cerr)
		}
		if int64(len(b)) != view.Size {
			t.Errorf("%s: %d bytes on disk, size %d", a.FileName, len(b), view.Size)
		}
	}
}

func TestDemoFile(t *testing.T) {
	t.Run("an unknown file", func(t *testing.T) {
		_, err := demoFile("nothing.bin")
		appErr, ok := errors.FromError(err)
		if !ok || appErr.Tag != tagDemoFileUnknown || !appErr.HasRoute("demoFile.DemoFileUnknown") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("the image is a PNG of 320 by 200", func(t *testing.T) {
		b, err := demoFile("mockup.png")
		if err != nil {
			t.Fatal(err)
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
		if err != nil || format != "png" || cfg.Width != 320 || cfg.Height != 200 {
			t.Errorf("DecodeConfig = %+v, %s, %v", cfg, format, err)
		}
	})
	t.Run("an image that cannot be written", func(t *testing.T) {
		err := writeDemoImage(failingWriter{})
		appErr, ok := errors.FromError(err)
		if !ok || appErr.Tag != tagDemoImageFailed || !appErr.HasRoute("writeDemoImage.DemoImageFailed") {
			t.Errorf("err = %v", err)
		}
	})
}

// failingWriter fails every write.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

func TestRunDemoMakesTheDemoPosts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"demo", "-data", dir}, mapEnv(nil), &stdout, &stderr, auth.FastHasher()); err != nil {
		t.Fatalf("demo: %+v", err)
	}
	if !strings.Contains(stdout.String(), "demo posts: 8") {
		t.Errorf("stdout lacks the number of posts:\n%s", stdout.String())
	}

	_, st := openAccounts(t, dir)
	fileStore := openFiles(t, dir)
	rooms := app.NewRooms(st, fileStore, nil, nil)
	posts := app.NewPosts(st, clock.Real(), markdown.New(), fileStore, app.PostsConfig{}, nil)
	actors := map[string]app.Actor{}
	for _, d := range demoUsers {
		row, err := st.UserByUsername(t.Context(), d.username)
		if err != nil {
			t.Fatal(err)
		}
		actors[d.username] = app.Actor{UserID: row.ID, Username: row.Username, Role: row.Role}
	}
	all, err := rooms.AllRooms(t.Context(), actors["admin"])
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]app.Room{}
	for _, r := range all {
		byName[r.Name] = r
	}

	feed := func(viewer, room string) []app.Post {
		t.Helper()
		page, err := posts.Feed(t.Context(), actors[viewer], byName[room].ID, 0, 30)
		if err != nil {
			t.Fatalf("Feed %s: %+v", room, err)
		}
		return page.Posts
	}
	general := feed("carol", "general")
	if len(general) != 5 {
		t.Fatalf("general has %d posts, want 5", len(general))
	}
	authors := map[string]bool{}
	tags := map[string]bool{}
	html := ""
	formats := map[app.Format]int{}
	for _, p := range general {
		authors[p.Author.Username] = true
		formats[p.Format]++
		html += string(p.HTML)
		for _, tag := range p.Tags {
			tags[tag] = true
		}
	}
	for _, name := range []string{"alice", "bob", "carol"} {
		if !authors[name] {
			t.Errorf("%s wrote no post in general", name)
		}
	}
	for _, tag := range []string{"design", "release", "help"} {
		if !tags[tag] {
			t.Errorf("no post in general is tagged %s", tag)
		}
	}
	if formats[app.FormatText] != 1 || formats[app.FormatMarkdown] != 4 {
		t.Errorf("formats = %v, want one text and four markdown", formats)
	}
	for _, want := range []string{"<h3>", "<h4>", "<ul>", "<li>", "<pre>", "<code", "<a href=", "<table>"} {
		if !strings.Contains(html, want) {
			t.Errorf("the rendered posts of general lack %s", want)
		}
	}
	if n := len(feed("alice", "random")); n != 2 {
		t.Errorf("random has %d posts, want 2", n)
	}
	secret := feed("alice", "secret")
	if len(secret) != 1 || secret[0].Author.Username != "alice" {
		t.Errorf("secret = %+v, want one post by alice", secret)
	}
	if _, err := posts.Feed(t.Context(), actors["carol"], byName["secret"].ID, 0, 30); err == nil {
		t.Error("carol reads the feed of the private room")
	}
}

func TestRunDemoMakesTheDemoThreads(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"demo", "-data", dir}, mapEnv(nil), &stdout, &stderr, auth.FastHasher()); err != nil {
		t.Fatalf("demo: %+v", err)
	}
	if !strings.Contains(stdout.String(), "demo comments: 9 (1 deleted)") {
		t.Errorf("stdout lacks the number of comments:\n%s", stdout.String())
	}

	_, st := openAccounts(t, dir)
	fileStore := openFiles(t, dir)
	rooms := app.NewRooms(st, fileStore, nil, nil)
	posts := app.NewPosts(st, clock.Real(), markdown.New(), fileStore, app.PostsConfig{}, nil)
	row, err := st.UserByUsername(t.Context(), "carol")
	if err != nil {
		t.Fatal(err)
	}
	carol := app.Actor{UserID: row.ID, Username: row.Username, Role: row.Role}
	adminRow, err := st.UserByUsername(t.Context(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	all, err := rooms.AllRooms(t.Context(), app.Actor{UserID: adminRow.ID, Username: adminRow.Username, Role: adminRow.Role})
	if err != nil {
		t.Fatal(err)
	}
	var general app.Room
	for _, r := range all {
		if r.Name == "general" {
			general = r
		}
	}
	page, err := posts.Feed(t.Context(), carol, general.ID, 0, 30)
	if err != nil {
		t.Fatalf("Feed: %+v", err)
	}
	// The feed is newest first: demoPosts[1] is the fourth of five, [2] the third.
	if len(page.Posts) != 5 {
		t.Fatalf("general has %d posts, want 5", len(page.Posts))
	}
	question, checklist := page.Posts[3], page.Posts[2]

	type shape struct {
		author  string
		depth   int
		deleted bool
	}
	tests := []struct {
		name     string
		post     app.Post
		comments int // not deleted
		want     []shape
	}{
		{
			name: "the question", post: question, comments: 4,
			want: []shape{{"alice", 1, false}, {"bob", 2, false}, {"carol", 3, false}, {"", 1, true}, {"alice", 2, false}},
		},
		{
			name: "the checklist", post: checklist, comments: 4,
			want: []shape{{"bob", 1, false}, {"carol", 2, false}, {"alice", 3, false}, {"bob", 4, false}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.post.CommentCount != tt.comments {
				t.Errorf("CommentCount = %d, want %d", tt.post.CommentCount, tt.comments)
			}
			th, err := posts.Thread(t.Context(), carol, tt.post.ID)
			if err != nil {
				t.Fatalf("Thread: %+v", err)
			}
			var got []shape
			for _, c := range th.Comments {
				got = append(got, shape{c.Author.Username, c.Depth, c.Deleted})
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("thread = %v, want %v", got, tt.want)
			}
		})
	}

	th, err := posts.Thread(t.Context(), carol, checklist.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first := th.Comments[0]; !reflect.DeepEqual(first.Tags, []string{"release", "changelog"}) {
		t.Errorf("the first comment's tags = %q, want release and changelog", first.Tags)
	}
	th, err = posts.Thread(t.Context(), carol, question.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gone := th.Comments[3]; gone.Body != "" || gone.HTML != "" || len(gone.Tags) != 0 || gone.Author != (app.UserRef{}) {
		t.Errorf("the deleted comment = %+v, want a bare placeholder", gone)
	} else if reply := th.Comments[4]; reply.ParentID != gone.ID || reply.ParentAuthor != (app.UserRef{}) {
		t.Errorf("the reply to the deleted comment = %+v", reply)
	}
}

func TestRunDemoMakesTheDemoReactions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"demo", "-data", dir}, mapEnv(nil), &stdout, &stderr, auth.FastHasher()); err != nil {
		t.Fatalf("demo: %+v", err)
	}
	if !strings.Contains(stdout.String(), "demo reactions: 13\n") {
		t.Errorf("stdout lacks the number of reactions:\n%s", stdout.String())
	}

	_, st := openAccounts(t, dir)
	fileStore := openFiles(t, dir)
	rooms := app.NewRooms(st, fileStore, nil, nil)
	posts := app.NewPosts(st, clock.Real(), markdown.New(), fileStore, app.PostsConfig{}, nil)
	actorOf := func(username string) app.Actor {
		row, err := st.UserByUsername(t.Context(), username)
		if err != nil {
			t.Fatal(err)
		}
		return app.Actor{UserID: row.ID, Username: row.Username, Role: row.Role}
	}
	carol := actorOf("carol")
	all, err := rooms.AllRooms(t.Context(), actorOf("admin"))
	if err != nil {
		t.Fatal(err)
	}
	var general app.Room
	for _, r := range all {
		if r.Name == "general" {
			general = r
		}
	}
	page, err := posts.Feed(t.Context(), carol, general.ID, 0, 30)
	if err != nil {
		t.Fatalf("Feed: %+v", err)
	}
	if len(page.Posts) != 5 {
		t.Fatalf("general has %d posts, want 5", len(page.Posts))
	}
	// Newest first: page.Posts[0] is the last of demoPosts. The chips are in the
	// order of first use, and carol's own are marked.
	tests := []struct {
		name string
		post app.Post
		want []app.ReactionCount
	}{
		{"welcome", page.Posts[4], []app.ReactionCount{
			{Emoji: "\U0001F44D", Count: 3, Mine: true, Names: []string{"Bob", "Carol", "Admin"}},
			{Emoji: "❤️", Count: 1, Names: []string{"Bob"}},
		}},
		{"question", page.Posts[3], []app.ReactionCount{{Emoji: "\U0001F914", Count: 1, Names: []string{"Alice"}}}},
		{"checklist", page.Posts[2], []app.ReactionCount{
			{Emoji: "\U0001F680", Count: 2, Names: []string{"Alice", "Bob"}},
			{Emoji: "\U0001F440", Count: 1, Names: []string{"Admin"}},
		}},
		{"design review", page.Posts[1], []app.ReactionCount{
			{Emoji: "✅", Count: 1, Names: []string{"Bob"}},
			{Emoji: "\U0001F44D", Count: 1, Mine: true, Names: []string{"Carol"}},
		}},
		// The newest post has no party popper: the proof puts it there live.
		{"thanks", page.Posts[0], []app.ReactionCount{{Emoji: "\U0001F64F", Count: 2, Mine: true, Names: []string{"Alice", "Carol"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.post.Reactions, tt.want) {
				t.Errorf("reactions = %+v, want %+v", tt.post.Reactions, tt.want)
			}
		})
	}
	// One comment has a reaction too; the others none.
	th, err := posts.Thread(t.Context(), carol, page.Posts[3].ID)
	if err != nil {
		t.Fatal(err)
	}
	withReactions := 0
	for _, c := range th.Comments {
		if len(c.Reactions) > 0 {
			withReactions++
			want := []app.ReactionCount{{Emoji: "\U0001F44D", Count: 1, Names: []string{"Alice"}}}
			if c.Author.Username != "bob" || !reflect.DeepEqual(c.Reactions, want) {
				t.Errorf("reacted comment = %s %+v", c.Author.Username, c.Reactions)
			}
		}
	}
	if withReactions != 1 {
		t.Errorf("%d comments have reactions, want 1", withReactions)
	}
	// Other rooms have none.
	for _, name := range []string{"random", "secret"} {
		for _, r := range all {
			if r.Name != name {
				continue
			}
			feed, err := posts.Feed(t.Context(), actorOf("alice"), r.ID, 0, 30)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range feed.Posts {
				if len(p.Reactions) != 0 {
					t.Errorf("a post in %s has reactions: %+v", name, p.Reactions)
				}
			}
		}
	}
}

func TestRunDemoMakesTheDemoTasks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"demo", "-data", dir}, mapEnv(nil), &stdout, &stderr, auth.FastHasher()); err != nil {
		t.Fatalf("demo: %+v", err)
	}
	if !strings.Contains(stdout.String(), "demo tasks: 9\n") {
		t.Errorf("stdout lacks the number of tasks:\n%s", stdout.String())
	}

	_, st := openAccounts(t, dir)
	rooms := app.NewRooms(st, openFiles(t, dir), nil, nil)
	board := app.NewBoard(st, clock.Real(), nil)
	actorOf := func(username string) app.Actor {
		row, err := st.UserByUsername(t.Context(), username)
		if err != nil {
			t.Fatal(err)
		}
		return app.Actor{UserID: row.ID, Username: row.Username, Role: row.Role}
	}
	all, err := rooms.AllRooms(t.Context(), actorOf("admin"))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]app.Room{}
	for _, r := range all {
		byName[r.Name] = r
	}
	titles := func(v app.BoardView) [][]string {
		out := make([][]string, len(v.Columns))
		for i, c := range v.Columns {
			out[i] = []string{}
			for _, card := range c.Tasks {
				out[i] = append(out[i], card.Title)
			}
		}
		return out
	}
	general, err := board.Board(t.Context(), actorOf("carol"), byName["general"].ID)
	if err != nil {
		t.Fatalf("Board: %+v", err)
	}
	want := [][]string{
		{"Invite the rest of the team", "Tag the release", "Write the release notes", "Write the changelog script"},
		{"Run the tests", "Pick the colors"},
		{"Write the welcome guide"},
	}
	if got := titles(general); !reflect.DeepEqual(got, want) {
		t.Errorf("general board = %v, want %v", got, want)
	}
	overdue, assigned, dated, comments := 0, 0, 0, 0
	for _, c := range general.Columns {
		for _, card := range c.Tasks {
			if card.Overdue {
				overdue++
				if card.Title != "Write the release notes" {
					t.Errorf("%q is overdue", card.Title)
				}
			}
			if len(card.Assignees) > 0 {
				assigned++
			}
			if card.DueDate != nil {
				dated++
			}
			if card.PostID != card.RootID {
				comments++
				if card.Title != "Write the changelog script" {
					t.Errorf("%q is the task of a comment", card.Title)
				}
			}
		}
	}
	if overdue != 1 || assigned != 7 || dated != 6 || comments != 1 {
		t.Errorf("overdue %d, assigned %d, dated %d, comments %d; want 1, 7, 6, 1", overdue, assigned, dated, comments)
	}
	secret, err := board.Board(t.Context(), actorOf("alice"), byName["secret"].ID)
	if err != nil {
		t.Fatalf("Board: %+v", err)
	}
	if got, want := titles(secret), [][]string{{"Book the party room"}, {"Order the cake"}, {}}; !reflect.DeepEqual(got, want) {
		t.Errorf("secret board = %v, want %v", got, want)
	}
	random, err := board.Board(t.Context(), actorOf("alice"), byName["random"].ID)
	if err != nil || !reflect.DeepEqual(titles(random), [][]string{{}, {}, {}}) {
		t.Errorf("random board = %v, %v", titles(random), err)
	}

	// A move of a card that the room does not have is an error of the demo.
	err = makeDemoBoard(t.Context(), board, map[string]app.Room{"general": byName["random"], "secret": byName["random"]},
		map[string]app.Actor{"alice": actorOf("alice"), "bob": actorOf("bob")}, io.Discard)
	appErr, ok := errors.FromError(err)
	if !ok || appErr.Tag != tagDemoTaskUnknown || !appErr.HasRoute("makeDemoBoard.DemoTaskUnknown") {
		t.Errorf("makeDemoBoard = %v", err)
	}
}

func TestRunDemoMakesTheDemoNotifications(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"demo", "-data", dir}, mapEnv(nil), &stdout, &stderr, auth.FastHasher()); err != nil {
		t.Fatalf("demo: %+v", err)
	}
	if !strings.Contains(stdout.String(), "demo notifications: ") {
		t.Errorf("stdout lacks the number of notifications:\n%s", stdout.String())
	}

	_, st := openAccounts(t, dir)
	notes := app.NewNotifications(st, nil)
	kindsOf := func(username string) (kinds map[app.NotificationKind]int, unread int) {
		t.Helper()
		row, err := st.UserByUsername(t.Context(), username)
		if err != nil {
			t.Fatal(err)
		}
		a := app.Actor{UserID: row.ID, Username: row.Username, Role: row.Role}
		page, err := notes.List(t.Context(), a, 0, 50)
		if err != nil {
			t.Fatalf("List %s: %+v", username, err)
		}
		kinds = map[app.NotificationKind]int{}
		for _, n := range page.Notifications {
			kinds[n.Kind]++
			if n.Actor.ID == a.UserID {
				t.Errorf("%s is notified of their own action: %+v", username, n)
			}
		}
		unread, err = notes.UnreadCount(t.Context(), a)
		if err != nil {
			t.Fatalf("UnreadCount %s: %+v", username, err)
		}
		return kinds, unread
	}
	for _, tt := range []struct {
		user string
		// want are the kinds the user must have at least once.
		want []app.NotificationKind
	}{
		{"admin", []app.NotificationKind{app.NotificationAssigned}},
		{"alice", []app.NotificationKind{app.NotificationReply, app.NotificationMention, app.NotificationAssigned}},
		{"bob", []app.NotificationKind{app.NotificationMention, app.NotificationReply, app.NotificationAssigned, app.NotificationAddedToRoom}},
		{"carol", []app.NotificationKind{app.NotificationMention, app.NotificationReply}},
	} {
		t.Run(tt.user, func(t *testing.T) {
			kinds, unread := kindsOf(tt.user)
			for _, k := range tt.want {
				if kinds[k] == 0 {
					t.Errorf("%s has no %s notification: %v", tt.user, k, kinds)
				}
			}
			total := 0
			for _, n := range kinds {
				total += n
			}
			if unread != total || total == 0 {
				t.Errorf("%s has %d notifications, %d unread", tt.user, total, unread)
			}
		})
	}
}

func TestRunDemoMakesTheDemoMessages(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"demo", "-data", dir}, mapEnv(nil), &stdout, &stderr, auth.FastHasher()); err != nil {
		t.Fatalf("demo: %+v", err)
	}
	if want := "demo messages: 6 in 2 conversations (2 reactions, 1 edited; bob has 1 unread)"; !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout lacks %q:\n%s", want, stdout.String())
	}

	_, st := openAccounts(t, dir)
	msgs := app.NewMessages(st, clock.Real(), openFiles(t, dir), app.MessagesConfig{}, nil)
	actors := map[string]app.Actor{}
	for _, d := range demoUsers {
		row, err := st.UserByUsername(t.Context(), d.username)
		if err != nil {
			t.Fatal(err)
		}
		actors[d.username] = app.Actor{UserID: row.ID, Username: row.Username, Role: row.Role}
	}

	// Who sees which conversations, and what is unread.
	tests := []struct {
		user   string
		titles []string
		unread []bool
		count  int
	}{
		{"alice", []string{"Launch crew", "Bob"}, []bool{false, false}, 0},
		{"bob", []string{"Launch crew", "Alice"}, []bool{false, true}, 1},
		{"carol", []string{"Launch crew"}, []bool{false}, 0},
		{"admin", nil, nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.user, func(t *testing.T) {
			list, err := msgs.Conversations(t.Context(), actors[tt.user])
			if err != nil {
				t.Fatalf("Conversations: %+v", err)
			}
			var titles []string
			var unread []bool
			for _, c := range list {
				titles = append(titles, c.DisplayName)
				unread = append(unread, c.Unread)
			}
			if !reflect.DeepEqual(titles, tt.titles) || !reflect.DeepEqual(unread, tt.unread) {
				t.Errorf("conversations = %v unread %v, want %v unread %v", titles, unread, tt.titles, tt.unread)
			}
			n, err := msgs.UnreadCount(t.Context(), actors[tt.user])
			if err != nil || n != tt.count {
				t.Errorf("unread count = %d, %v; want %d", n, err, tt.count)
			}
		})
	}

	list, err := msgs.Conversations(t.Context(), actors["bob"])
	if err != nil {
		t.Fatal(err)
	}
	var direct, group app.ConversationSummary
	for _, c := range list {
		if c.Kind == app.ConversationDirect {
			direct = c
		} else {
			group = c
		}
	}
	if len(group.Members) != 3 || group.LastMessage == nil || group.LastMessage.Author.Username != "bob" {
		t.Errorf("the group = %+v", group)
	}
	page, err := msgs.History(t.Context(), actors["bob"], direct.ID, 0, 0)
	if err != nil || len(page.Messages) != 3 {
		t.Fatalf("direct history = %+v, %v", page, err)
	}
	first, reply, lunch := page.Messages[0], page.Messages[1], page.Messages[2]
	if first.ReplyTo != nil || first.EditedAt != nil || len(first.Reactions) != 0 {
		t.Errorf("first message = %+v", first)
	}
	if reply.ReplyTo == nil || reply.ReplyTo.ID != first.ID || reply.ReplyTo.Author.Username != "alice" ||
		len(reply.Reactions) != 1 || reply.Reactions[0].Emoji != "\u2764\uFE0F" {
		t.Errorf("reply = %+v", reply)
	}
	if lunch.EditedAt == nil || lunch.Body != "Great! Lunch at noon? My treat." ||
		len(lunch.Reactions) != 1 || lunch.Reactions[0].Emoji != "\U0001F44D" || !lunch.Reactions[0].Mine {
		t.Errorf("lunch = %+v", lunch)
	}
	gp, err := msgs.History(t.Context(), actors["carol"], group.ID, 0, 0)
	if err != nil || len(gp.Messages) != 3 || gp.Messages[2].ReplyTo == nil || gp.Messages[2].ReplyTo.Author.Username != "carol" {
		t.Errorf("group history = %+v, %v", gp, err)
	}
}

// fakeBridge is a publisherCloser that records what it gets.
type fakeBridge struct {
	events   []live.Event
	closed   int
	closeErr error
}

func (f *fakeBridge) Publish(ev live.Event) { f.events = append(f.events, ev) }

func (f *fakeBridge) Close() error {
	f.closed++
	return f.closeErr
}

func TestNewPublisher(t *testing.T) {
	t.Run("without a Redis URL the hub is the publisher", func(t *testing.T) {
		hub := live.New()
		defer hub.Close()
		var logs syncBuffer
		opened := false
		pub, closeBridge, err := newPublisher(t.Context(), "", hub, newLogger("info", &logs),
			func(context.Context, string, *live.Hub, *slog.Logger) (publisherCloser, error) {
				opened = true
				return nil, nil
			})
		if err != nil {
			t.Fatal(err)
		}
		if pub != app.Publisher(hub) {
			t.Errorf("publisher = %T, want the hub", pub)
		}
		if opened {
			t.Error("the bridge was opened without a Redis URL")
		}
		closeBridge() // does nothing
		if strings.Contains(logs.String(), "Redis") {
			t.Errorf("logged about Redis:\n%s", logs.String())
		}
	})
	t.Run("with a Redis URL the bridge is the publisher", func(t *testing.T) {
		hub := live.New()
		defer hub.Close()
		bridge := &fakeBridge{}
		var logs syncBuffer
		var gotURL string
		var gotHub *live.Hub
		pub, closeBridge, err := newPublisher(t.Context(), "redis://:hunter2@redis:6379/0", hub, newLogger("info", &logs),
			func(_ context.Context, url string, h *live.Hub, _ *slog.Logger) (publisherCloser, error) {
				gotURL, gotHub = url, h
				return bridge, nil
			})
		if err != nil {
			t.Fatal(err)
		}
		if pub != app.Publisher(bridge) {
			t.Errorf("publisher = %T, want the bridge", pub)
		}
		if gotURL != "redis://:hunter2@redis:6379/0" || gotHub != hub {
			t.Errorf("the bridge was opened with %q and %p, want the URL and the hub", gotURL, gotHub)
		}
		out := logs.String()
		if !strings.Contains(out, "live events go through Redis") || !strings.Contains(out, "redis://:xxxxx@redis:6379/0") {
			t.Errorf("no info line with the redacted URL:\n%s", out)
		}
		if strings.Contains(out, "hunter2") {
			t.Errorf("the log holds the password:\n%s", out)
		}
		if bridge.closed != 0 {
			t.Error("the bridge is closed before the function is called")
		}
		closeBridge()
		if bridge.closed != 1 {
			t.Errorf("Close was called %d times, want 1", bridge.closed)
		}
	})
	t.Run("a failure to close the bridge is logged", func(t *testing.T) {
		hub := live.New()
		defer hub.Close()
		var logs syncBuffer
		bridge := &fakeBridge{closeErr: errors.New("RedisCloseFailed", "boom")}
		_, closeBridge, err := newPublisher(t.Context(), "redis://redis", hub, newLogger("info", &logs),
			func(context.Context, string, *live.Hub, *slog.Logger) (publisherCloser, error) { return bridge, nil })
		if err != nil {
			t.Fatal(err)
		}
		closeBridge()
		if !strings.Contains(logs.String(), "closing the Redis bridge failed") {
			t.Errorf("not logged:\n%s", logs.String())
		}
	})
	t.Run("the error of the opener is returned as it is", func(t *testing.T) {
		hub := live.New()
		defer hub.Close()
		_, _, err := newPublisher(t.Context(), "redis://redis", hub, newLogger("info", &syncBuffer{}),
			func(context.Context, string, *live.Hub, *slog.Logger) (publisherCloser, error) {
				return nil, errors.New("RedisPingFailed", "no server")
			})
		appErr, ok := errors.FromError(err)
		if !ok || appErr.Tag != "RedisPingFailed" || !appErr.HasRoute("newPublisher") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestOpenBridgeWithoutAServer(t *testing.T) {
	hub := live.New()
	defer hub.Close()
	// Nothing listens on port 1.
	b, err := openBridge(t.Context(), "redis://:hunter2@127.0.0.1:1/0", hub, newLogger("info", &syncBuffer{}))
	if b != nil {
		t.Errorf("bridge = %v, want nil", b)
	}
	appErr, ok := errors.FromError(err)
	if !ok || !appErr.HasRoute("openBridge->NewBridge->openRedis.RedisPingFailed") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(appErr.Error(), "hunter2") || strings.Contains(fmt.Sprint(appErr.Metadata), "hunter2") {
		t.Errorf("the error holds the password: %v", appErr)
	}
}

func TestServeWithRedisURLNeedsPostgres(t *testing.T) {
	var stderr syncBuffer
	err := runServe(t.Context(), []string{"-data", t.TempDir(), "-addr", "127.0.0.1:0", "-redis-url", "redis://localhost:6379"},
		mapEnv(nil), &stderr)
	appErr, ok := errors.FromError(err)
	if !ok || !appErr.HasRoute("runServe->parseServeConfig->Config.Validate.RedisNeedsPostgres") {
		t.Errorf("err = %v", err)
	}
}
