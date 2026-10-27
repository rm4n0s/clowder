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

	"github.com/rm4n0s/clowder/internal/api"
	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/cluster"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/markdown"
	"github.com/rm4n0s/clowder/internal/web"
	"github.com/rm4n0s/errors"
)

const (
	// dataDirMode is the permission of the data directory when serve creates it.
	dataDirMode = 0o750
	// databaseFile is the name of the database inside the data directory.
	databaseFile = "clowder.db"
	// filesDir is the directory of the attachments inside the data directory.
	filesDir = "files"
	// avatarsDir is the directory of the avatars inside the files directory.
	avatarsDir = "avatars"
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
	st, err := openStore(ctx, cfg.DataDir, cfg.DatabaseURL, clock.Real(), logger)
	if err != nil {
		return err
	}
	// The deferred closes run in the reverse order of their declaration, which
	// is the order of SPEC §8 and D41: the HTTP server stopped (and ended the
	// streams) before serve returns, then the bridge to Redis, then the hub,
	// then the database. The services publish while requests run, and the
	// bridge publishes to the hub, so each is closed after what uses it.
	defer func() {
		if closeErr := st.Close(); closeErr != nil {
			logger.Error("closing the store failed", "err", closeErr)
		}
	}()
	fileStore, err := files.New(filepath.Join(cfg.DataDir, filesDir))
	if err != nil {
		return err
	}
	avatarStore, err := files.New(filepath.Join(cfg.DataDir, filesDir, avatarsDir))
	if err != nil {
		return err
	}
	hub := live.New()
	defer hub.Close()
	pub, closeBridge, err := newPublisher(ctx, cfg.RedisURL, hub, logger, openBridge)
	if err != nil {
		return err
	}
	defer closeBridge()
	limiter, closeLimiter, err := newLimiter(ctx, cfg.RedisURL, clock.Real(), logger, openCounter)
	if err != nil {
		return err
	}
	defer closeLimiter()
	accounts := app.NewAccounts(st, clock.Real(), auth.DefaultHasher(), app.AccountsConfig{
		InvitationLifetime: cfg.InvitationLifetime,
		SessionLifetime:    cfg.SessionLifetime,
		Limiter:            limiter,
		Publisher:          pub,
		Avatars:            avatarStore,
		Logger:             logger,
	})
	postsConfig := app.PostsConfig{
		MaxAttachmentSize: cfg.MaxAttachmentSize,
		MaxAttachments:    cfg.MaxAttachmentsPerPost,
		Logger:            logger,
	}
	notifications := app.NewNotifications(st, pub)
	messages := app.NewMessages(st, clock.Real(), fileStore, postsConfig, pub)
	rooms := app.NewRooms(st, fileStore, logger, pub)
	md := markdown.New()
	posts := app.NewPosts(st, clock.Real(), md, fileStore, postsConfig, pub)
	// Uploads of the API that no post takes are removed after a day. The
	// sweeper stops before the store is closed (the deferred calls run in
	// reverse order).
	stopSweeper := startStagedSweeper(ctx, posts, stagedSweepInterval, logger)
	defer stopSweeper()
	board := app.NewBoard(st, clock.Real(), pub)
	search := app.NewSearch(st)
	audit := app.NewAudit(st)
	apiServer, err := api.New(api.Options{
		Services: api.Services{
			Accounts: accounts, Notifications: notifications, Messages: messages, Rooms: rooms, Posts: posts,
			Board: board, Search: search, Audit: audit, Hub: hub,
		},
		Logger:        logger,
		NoteUser:      web.NoteUser,
		ClientIP:      web.ClientIPFunc(cfg.TrustProxyHeaders),
		MaxUploadSize: cfg.MaxAttachmentSize,
		Markdown:      md,
		BaseURL:       cfg.BaseURL,
	})
	if err != nil {
		return err
	}
	srv, err := web.New(cfg, logger, web.Services{
		API:           apiServer,
		Accounts:      accounts,
		Rooms:         rooms,
		Audit:         audit,
		Board:         board,
		Search:        search,
		Health:        app.NewHealth(st),
		Hub:           hub,
		Notifications: notifications,
		Posts:         posts,
		Messages:      messages,
	})
	if err != nil {
		return err
	}
	return srv.Run(ctx)
}

// publisherCloser is the Publisher of the services when events go through
// Redis: it also has to be closed. *cluster.Bridge is one.
type publisherCloser interface {
	app.Publisher
	Close() error
}

// bridgeOpener opens the bridge to Redis; openBridge is the real one and tests
// pass a fake.
type bridgeOpener func(ctx context.Context, redisURL string, hub *live.Hub, logger *slog.Logger) (publisherCloser, error)

// openBridge opens a cluster.Bridge.
func openBridge(ctx context.Context, redisURL string, hub *live.Hub, logger *slog.Logger) (publisherCloser, error) {
	b, err := cluster.NewBridge(ctx, redisURL, hub, logger)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// newPublisher chooses the Publisher of the services: the hub itself when
// redisURL is empty, else a bridge that also carries the events to the other
// processes (SPEC D41). The web handlers subscribe to the hub in both cases.
// The returned function stops the bridge, and does nothing for the hub; the
// caller closes the hub after it.
func newPublisher(ctx context.Context, redisURL string, hub *live.Hub, logger *slog.Logger, open bridgeOpener) (app.Publisher, func(), error) {
	if redisURL == "" {
		return hub, func() {}, nil
	}
	bridge, err := open(ctx, redisURL, hub, logger)
	if err != nil {
		return nil, nil, err
	}
	logger.Info("live events go through Redis", "redis", config.RedactURL(redisURL))
	return bridge, func() {
		if closeErr := bridge.Close(); closeErr != nil {
			logger.Error("closing the Redis bridge failed", "err", closeErr)
		}
	}, nil
}

// counterCloser is the FailureCounter of the login limiter when it counts in
// Redis: it also has to be closed. *cluster.LoginCounter is one.
type counterCloser interface {
	app.FailureCounter
	Close() error
}

// counterOpener opens the counter of the login limiter in Redis; openCounter
// is the real one and tests pass a fake.
type counterOpener func(ctx context.Context, redisURL string) (counterCloser, error)

// openCounter opens a cluster.LoginCounter.
func openCounter(ctx context.Context, redisURL string) (counterCloser, error) {
	c, err := cluster.NewLoginCounter(ctx, redisURL)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// newLimiter makes the login limiter of the services: it counts in memory when
// redisURL is empty, else in Redis, so that the limit of SPEC §7.1 holds across
// the processes (SPEC D41, §8.1). The returned function closes the counter, and
// does nothing for the in-memory one.
func newLimiter(ctx context.Context, redisURL string, clk clock.Clock, logger *slog.Logger, open counterOpener) (*app.LoginLimiter, func(), error) {
	if redisURL == "" {
		return app.NewLoginLimiter(clk), func() {}, nil
	}
	counter, err := open(ctx, redisURL)
	if err != nil {
		return nil, nil, err
	}
	logger.Info("the login limiter counts in Redis", "redis", config.RedactURL(redisURL))
	return app.NewLoginLimiterOn(clk, counter, logger), func() {
		if closeErr := counter.Close(); closeErr != nil {
			logger.Error("closing the login counter failed", "err", closeErr)
		}
	}, nil
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
	fromEnv := []func(){
		urlFlag(fs, &cfg.DatabaseURL, "database-url", env, "CLOWDER_DATABASE_URL", def.DatabaseURL,
			"PostgreSQL URL, postgres://...; empty: SQLite in the data directory (CLOWDER_DATABASE_URL)"),
		urlFlag(fs, &cfg.RedisURL, "redis-url", env, "CLOWDER_REDIS_URL", def.RedisURL,
			"Redis URL, redis://...; carries the live events between several processes; needs -database-url (CLOWDER_REDIS_URL)"),
	}
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
	for _, apply := range fromEnv {
		apply()
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

// urlFlag defines the string flag name for a URL that holds a password (the
// database and the Redis URL) and returns the function that applies its
// setting once the flags are parsed: the flag if it was given (an empty one
// too), else the variable key, else def. The environment is not the default
// of the flag, because the flag package prints the default in the usage
// text, and the usage text must never show a password.
func urlFlag(fs *flag.FlagSet, p *string, name string, env func(string) string, key, def, usage string) func() {
	fs.StringVar(p, name, "", usage)
	return func() {
		given := false
		fs.Visit(func(f *flag.Flag) { given = given || f.Name == name })
		if !given {
			*p = envString(env, key, def)
		}
	}
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
