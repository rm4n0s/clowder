package main

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/errors"
)

// secretURLs are the database and Redis URLs of the environment in the tests
// below. Their passwords must not reach the output of a command.
const (
	secretDatabaseURL = "postgres://clowder:s3cr3t-db-pass@db.example.com:5432/clowder"
	secretRedisURL    = "redis://:s3cr3t-redis-pass@cache.example.com:6379/0"
)

// TestUsageDoesNotPrintTheURLsOfTheEnvironment checks that the usage text of
// a command (the output of -h and of a mistake in the flags) shows no password:
// the flag package prints the default of each flag, and the default of
// -database-url and -redis-url would be the URL of the environment.
func TestUsageDoesNotPrintTheURLsOfTheEnvironment(t *testing.T) {
	env := mapEnv(map[string]string{"CLOWDER_DATABASE_URL": secretDatabaseURL, "CLOWDER_REDIS_URL": secretRedisURL})
	tests := []struct {
		name string
		run  func(stderr *bytes.Buffer) error
	}{
		{"serve -h", func(stderr *bytes.Buffer) error {
			_, err := parseServeConfig([]string{"-h"}, env, stderr)
			return err
		}},
		{"serve with a mistake", func(stderr *bytes.Buffer) error {
			_, err := parseServeConfig([]string{"-nosuchflag"}, env, stderr)
			return err
		}},
		{"invite -h", func(stderr *bytes.Buffer) error {
			return runInvite(context.Background(), []string{"-h"}, env, &bytes.Buffer{}, stderr, auth.FastHasher())
		}},
		{"demo -h", func(stderr *bytes.Buffer) error {
			return runDemo(context.Background(), []string{"-h"}, env, &bytes.Buffer{}, stderr, auth.FastHasher())
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			if err := tt.run(&stderr); err == nil {
				t.Fatal("no error")
			}
			out := stderr.String()
			if !strings.Contains(out, "-database-url") {
				t.Errorf("the usage does not list -database-url:\n%s", out)
			}
			for _, secret := range []string{"s3cr3t", "db.example.com", "cache.example.com"} {
				if strings.Contains(out, secret) {
					t.Errorf("the usage holds %q:\n%s", secret, out)
				}
			}
		})
	}
}

// TestURLFlagsKeepTheirPrecedence checks that moving the environment out of
// the flag defaults changed nothing: a flag wins over the environment, an
// empty flag turns the environment off, and the environment is used when the
// flag is not given.
func TestURLFlagsKeepTheirPrecedence(t *testing.T) {
	env := mapEnv(map[string]string{"CLOWDER_DATABASE_URL": secretDatabaseURL, "CLOWDER_REDIS_URL": secretRedisURL})
	other := "postgres://other@db2.example.com/clowder"
	tests := []struct {
		name         string
		args         []string
		wantDatabase string
		wantRedis    string
	}{
		{"the environment", nil, secretDatabaseURL, secretRedisURL},
		{"the flags win", []string{"-database-url", other, "-redis-url", "redis://r2:6379/1"}, other, "redis://r2:6379/1"},
		{"empty flags turn the environment off", []string{"-database-url", "", "-redis-url", ""}, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := parseServeConfig(tt.args, env, &bytes.Buffer{})
			if err != nil {
				t.Fatalf("parseServeConfig: %+v", err)
			}
			if cfg.DatabaseURL != tt.wantDatabase || cfg.RedisURL != tt.wantRedis {
				t.Errorf("database %q redis %q, want %q and %q", cfg.DatabaseURL, cfg.RedisURL, tt.wantDatabase, tt.wantRedis)
			}
		})
	}
}

// TestInviteAndDemoReadTheDatabaseURLFromTheEnvironment checks the same for
// the commands that open the database: the URL of the environment is used
// when the flag is not given (a SQLite directory is used when both are empty).
func TestInviteAndDemoReadTheDatabaseURLFromTheEnvironment(t *testing.T) {
	// The URL points at nothing: the command must fail while opening the
	// database, and the error must not hold the password.
	bad := mapEnv(map[string]string{"CLOWDER_DATABASE_URL": "postgres://clowder:s3cr3t-db-pass@127.0.0.1:1/clowder?connect_timeout=1"})
	for name, run := range map[string]func(dir string, stderr *bytes.Buffer) error{
		"invite": func(dir string, stderr *bytes.Buffer) error {
			return runInvite(context.Background(), []string{"-member", "-data", dir}, bad, &bytes.Buffer{}, stderr, auth.FastHasher())
		},
		"demo": func(dir string, stderr *bytes.Buffer) error {
			return runDemo(context.Background(), []string{"-data", filepath.Join(dir, "demo")}, bad, &bytes.Buffer{}, stderr, auth.FastHasher())
		},
	} {
		t.Run(name, func(t *testing.T) {
			var stderr bytes.Buffer
			err := run(t.TempDir(), &stderr)
			if err == nil {
				t.Fatal("no error: the environment URL was not used")
			}
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T", err)
			}
			text := appErr.Error() + appErr.Message + stderr.String()
			for k, v := range appErr.Metadata {
				text += k + "=" + fmt.Sprint(v)
			}
			if strings.Contains(text, "s3cr3t") {
				t.Errorf("the failure holds the password: %s", text)
			}
		})
	}
}
