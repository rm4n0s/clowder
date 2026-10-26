package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/config"
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
			name:      "invite is not implemented",
			args:      []string{"invite", "-admin"},
			wantTag:   tagNotImplemented,
			wantRoute: "run.NotImplemented",
			wantMeta:  map[string]any{"command": "invite"},
		},
		{
			name:      "demo is not implemented",
			args:      []string{"demo"},
			wantTag:   tagNotImplemented,
			wantRoute: "run.NotImplemented",
			wantMeta:  map[string]any{"command": "demo"},
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
			err := run(context.Background(), tt.args, mapEnv(nil), &stdout, &stderr)
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
