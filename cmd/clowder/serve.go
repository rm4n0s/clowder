package main

import (
	"context"
	"flag"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/clowder/internal/web"
	"github.com/rm4n0s/errors"
)

const (
	// dataDirMode is the permission of the data directory when serve creates it.
	dataDirMode = 0o750
	// databaseFile is the name of the database inside the data directory.
	databaseFile = "clowder.db"
)

// runServe runs the server until ctx is done.
func runServe(ctx context.Context, args []string, env func(string) string, stderr io.Writer) error {
	cfg, err := parseServeConfig(args, env, stderr)
	if err != nil {
		return err
	}
	logger := newLogger(cfg.LogLevel, stderr)
	if err := os.MkdirAll(cfg.DataDir, dataDirMode); err != nil {
		return errors.NewErr(tagDataDirCreateFailed, err).SetMetadata("dir", cfg.DataDir)
	}
	st, err := store.Open(ctx, filepath.Join(cfg.DataDir, databaseFile), clock.Real(), logger)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := st.Close(); closeErr != nil {
			logger.Error("closing the store failed", "err", closeErr)
		}
	}()
	accounts := app.NewAccounts(st, clock.Real(), auth.DefaultHasher(), app.AccountsConfig{
		InvitationLifetime: cfg.InvitationLifetime,
		SessionLifetime:    cfg.SessionLifetime,
	})
	srv, err := web.New(cfg, logger, web.Services{Accounts: accounts, Rooms: app.NewRooms(st)})
	if err != nil {
		return err
	}
	return srv.Run(ctx)
}

// parseServeConfig builds the configuration of serve. Each setting is the flag
// if given, else its CLOWDER_* environment variable if set, else the default.
// Usage and flag errors are written to stderr. The result is validated.
func parseServeConfig(args []string, env func(string) string, stderr io.Writer) (config.Config, error) {
	def := config.Default()
	maxAttachmentSize, err := envInt64(env, "CLOWDER_MAX_ATTACHMENT_SIZE", def.MaxAttachmentSize)
	if err != nil {
		return config.Config{}, err
	}
	maxAttachments, err := envInt(env, "CLOWDER_MAX_ATTACHMENTS", def.MaxAttachmentsPerPost)
	if err != nil {
		return config.Config{}, err
	}
	sessionLifetime, err := envDuration(env, "CLOWDER_SESSION_LIFETIME", def.SessionLifetime)
	if err != nil {
		return config.Config{}, err
	}
	invitationLifetime, err := envDuration(env, "CLOWDER_INVITATION_LIFETIME", def.InvitationLifetime)
	if err != nil {
		return config.Config{}, err
	}

	trustProxy, err := envBool(env, "CLOWDER_TRUST_PROXY", def.TrustProxyHeaders)
	if err != nil {
		return config.Config{}, err
	}

	var cfg config.Config
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.ListenAddr, "addr", envString(env, "CLOWDER_ADDR", def.ListenAddr),
		"address to listen on, as host:port (CLOWDER_ADDR)")
	fs.StringVar(&cfg.DataDir, "data", envString(env, "CLOWDER_DATA", def.DataDir),
		"data directory (CLOWDER_DATA)")
	fs.StringVar(&cfg.BaseURL, "base-url", envString(env, "CLOWDER_BASE_URL", def.BaseURL),
		"base URL of invitation links (CLOWDER_BASE_URL)")
	fs.Int64Var(&cfg.MaxAttachmentSize, "max-attachment-size", maxAttachmentSize,
		"largest attachment in bytes (CLOWDER_MAX_ATTACHMENT_SIZE)")
	fs.IntVar(&cfg.MaxAttachmentsPerPost, "max-attachments", maxAttachments,
		"most attachments per post (CLOWDER_MAX_ATTACHMENTS)")
	fs.DurationVar(&cfg.SessionLifetime, "session-lifetime", sessionLifetime,
		"how long a login lasts (CLOWDER_SESSION_LIFETIME)")
	fs.DurationVar(&cfg.InvitationLifetime, "invitation-lifetime", invitationLifetime,
		"how long an invitation is valid (CLOWDER_INVITATION_LIFETIME)")
	fs.StringVar(&cfg.LogLevel, "log-level", envString(env, "CLOWDER_LOG_LEVEL", def.LogLevel),
		"log level: debug, info, warn or error (CLOWDER_LOG_LEVEL)")
	fs.BoolVar(&cfg.TrustProxyHeaders, "trust-proxy", trustProxy,
		"take the client address from the last X-Forwarded-For entry; only behind our own reverse proxy (CLOWDER_TRUST_PROXY)")
	if err := fs.Parse(args); err != nil {
		return config.Config{}, errors.NewErr(tagFlagsInvalid, err)
	}
	if fs.NArg() > 0 {
		return config.Config{}, errors.New(tagFlagsInvalid, "unexpected arguments", "args", fs.Args())
	}
	if err := cfg.Validate(); err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

// newLogger makes the JSON logger on w at the given level. The level was
// validated by config.Config.Validate: an unknown one is info.
func newLogger(level string, w io.Writer) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl}))
}

// envString returns the variable key, or def when it is unset or empty.
func envString(env func(string) string, key, def string) string {
	if v := env(key); v != "" {
		return v
	}
	return def
}

// envInt64 returns the variable key as an integer, or def when it is unset.
func envInt64(env func(string) string, key string, def int64) (int64, error) {
	v := env(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, errors.NewErr(tagEnvValueInvalid, err).SetMetadata("name", key, "value", v)
	}
	return n, nil
}

// envInt returns the variable key as an integer, or def when it is unset.
func envInt(env func(string) string, key string, def int) (int, error) {
	v := env(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, errors.NewErr(tagEnvValueInvalid, err).SetMetadata("name", key, "value", v)
	}
	return n, nil
}

// envBool returns the variable key as a boolean ("true", "false", "1", "0"),
// or def when it is unset.
func envBool(env func(string) string, key string, def bool) (bool, error) {
	v := env(key)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, errors.NewErr(tagEnvValueInvalid, err).SetMetadata("name", key, "value", v)
	}
	return b, nil
}

// envDuration returns the variable key as a Go duration ("720h"), or def when
// it is unset.
func envDuration(env func(string) string, key string, def time.Duration) (time.Duration, error) {
	v := env(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, errors.NewErr(tagEnvValueInvalid, err).SetMetadata("name", key, "value", v)
	}
	return d, nil
}

const (
	tagEnvValueInvalid     = "EnvValueInvalid"
	tagFlagsInvalid        = "FlagsInvalid"
	tagDataDirCreateFailed = "DataDirCreateFailed"
)
