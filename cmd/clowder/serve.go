package main

import (
	"context"
	"crypto/rand"
	"flag"
	"io"
	"log/slog"
	"net"
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
	"github.com/rm4n0s/clowder/internal/metrics"
	"github.com/rm4n0s/clowder/internal/sshcode"
	"github.com/rm4n0s/clowder/internal/web"
	"github.com/rm4n0s/errors"
	"golang.org/x/crypto/ssh"
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
	// The metrics exist only with -metrics-addr (SPEC D60): without it nothing
	// is counted and nothing listens. The listener opens first, so that a port
	// in use stops serve before anything else is started, and it closes last.
	metricsSet, err := newMetricsSet(cfg)
	if err != nil {
		return err
	}
	m := reportTo(metricsSet)
	if metricsSet != nil {
		stopMetrics, _, err := startMetrics(ctx, cfg, metricsSet.Registry, logger)
		if err != nil {
			return err
		}
		defer stopMetrics()
	}
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
	if metricsSet != nil {
		if err := metricsSet.RegisterPool(st.PoolStats); err != nil {
			return err
		}
	}
	fileStore, err := files.New(filepath.Join(cfg.DataDir, filesDir))
	if err != nil {
		return err
	}
	avatarStore, err := files.New(filepath.Join(cfg.DataDir, filesDir, avatarsDir))
	if err != nil {
		return err
	}
	hub := live.New(live.WithDropCounter(m.HubDropped))
	defer hub.Close()
	pub, closeBridge, err := newPublisher(ctx, cfg.RedisURL, hub, logger, func(ctx context.Context, redisURL string, hub *live.Hub, logger *slog.Logger) (publisherCloser, error) {
		return openBridge(ctx, redisURL, hub, logger, cluster.WithMetrics(bridgeMetrics(m)))
	})
	if err != nil {
		return err
	}
	defer closeBridge()
	livePresence, closePresence, err := newPresence(ctx, cfg.RedisURL, logger, openPresence)
	if err != nil {
		return err
	}
	defer closePresence()
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
		// A login asks for the SSH code only when this process runs the SSH
		// server; every process of a cluster has the same -ssh-addr
		// (SPEC §7.16).
		SSHServer: cfg.SSHAddr != "",
		Logins:    m.Logins,
		// Sign-ups the limiter refused (SPEC D62).
		SignUpsRefused: m.LimitsRefused.With(metrics.LimitSignUp),
	})
	postsConfig := app.PostsConfig{
		MaxAttachmentSize: cfg.MaxAttachmentSize,
		MaxAttachments:    cfg.MaxAttachmentsPerPost,
		Logger:            logger,
		Presence:          livePresence,
		// Uploads refused for the staged bytes (SPEC D62).
		StagedRefused: m.LimitsRefused.With(metrics.LimitStaged),
	}
	notifications := app.NewNotifications(st, pub)
	prefs := app.NewPrefs(st, pub)
	messages := app.NewMessages(st, clock.Real(), fileStore, postsConfig, pub)
	rooms := app.NewRooms(st, fileStore, logger, pub)
	md := markdown.New()
	posts := app.NewPosts(st, clock.Real(), md, fileStore, postsConfig, pub)
	// Uploads of the API that no post takes are removed after a day. The
	// sweeper stops before the store is closed (the deferred calls run in
	// reverse order).
	stopSweeper := startStagedSweeper(ctx, posts, stagedSweepInterval, logger)
	defer stopSweeper()
	processID, err := newProcessID(rand.Reader)
	if err != nil {
		return err
	}
	// With Redis the process announces itself and relays the sockets of the
	// calls that other processes hold (SPEC D65). Its key is removed after the
	// calls of the process were ended (the deferred calls run in reverse).
	link, closeLink, err := newCallLink(ctx, cfg.RedisURL, processID, logger, openCallLink)
	if err != nil {
		return err
	}
	defer closeLink()
	callsConfig := app.CallsConfig{
		ProcessID: processID,
		Enabled:   cfg.MediaAddr != "",
		MaxCalls:  cfg.MaxCalls,
		// The service takes the default for a zero value, which only a
		// hand-made configuration has.
		RingTimeout: cfg.CallRingTimeout,
		Logger:      logger,
	}
	if link != nil {
		callsConfig.Processes = link
	}
	callSvc := app.NewCalls(st, clock.Real(), callsConfig, pub)
	// A run without Redis starts with no call alive: whatever the database says
	// is open belongs to an earlier run.
	if err := endCallsAtStart(ctx, callSvc, cfg, logger); err != nil {
		return err
	}
	// The deferred calls run in reverse order: the expirer and the media server
	// stop (and close every socket) before the calls of the process are ended,
	// and those before the key of the process is removed, the hub and the
	// store close.
	defer endCallsAtStop(ctx, callSvc, processID, logger)
	media, stopMedia, err := startMedia(ctx, cfg, callSvc, hub, processID, link, logger, withMediaMetrics(m))
	if err != nil {
		return err
	}
	defer stopMedia()
	if metricsSet != nil {
		if err := metricsSet.RegisterCalls(func() int {
			if media == nil {
				return 0
			}
			return media.Calls()
		}); err != nil {
			return err
		}
	}
	stopExpirer := startCallExpirer(ctx, callSvc, callExpireInterval, logger)
	defer stopExpirer()
	board := app.NewBoard(st, clock.Real(), pub)
	search := app.NewSearch(st)
	audit := app.NewAudit(st)
	presence := app.NewPresence(st, livePresence)
	stopSSH, sshInfo, err := startSSH(ctx, cfg, app.NewSignIn(st, clock.Real(), logger, app.WithCodesCounter(m.SSHCodes)), limiter, logger)
	if err != nil {
		return err
	}
	defer stopSSH()
	sshKeys := app.NewSSHKeys(st, clock.Real(), accounts, sshInfo)
	// The live connections of each user in this process, streams and sockets
	// together (SPEC D62): one counter for the pages and the API.
	connections := app.NewConnections(app.MaxConnectionsPerUser, m.LimitsRefused.With(metrics.LimitConnections))
	apiServer, err := api.New(api.Options{
		Services: api.Services{
			Accounts: accounts, Notifications: notifications, Messages: messages, Rooms: rooms, Posts: posts,
			Board: board, Search: search, Audit: audit, Prefs: prefs, Presence: presence, SSHKeys: sshKeys, Calls: callSvc, Media: media, Hub: hub,
			Connections: connections,
		},
		Logger:        logger,
		NoteUser:      web.NoteUser,
		ClientIP:      web.ClientIPFunc(cfg.TrustProxyHeaders),
		MaxUploadSize: cfg.MaxAttachmentSize,
		Markdown:      md,
		BaseURL:       cfg.BaseURL,
		WebSockets:    m.WebSockets.With(metrics.WebSocketKindAPI),
	})
	if err != nil {
		return err
	}
	srv, err := web.New(cfg, logger, web.Services{
		Connections:   connections,
		Metrics:       metricsSet,
		API:           apiServer,
		Accounts:      accounts,
		Rooms:         rooms,
		Audit:         audit,
		Board:         board,
		Search:        search,
		Health:        app.NewHealth(st),
		Hub:           hub,
		Notifications: notifications,
		Prefs:         prefs,
		Presence:      presence,
		Posts:         posts,
		Messages:      messages,
		SSHKeys:       sshKeys,
		Calls:         callSvc,
		Media:         media,
	})
	if err != nil {
		return err
	}
	return srv.Run(ctx)
}

// startSSH starts the SSH server of the sign-in codes (SPEC §7.16, D52) when
// cfg.SSHAddr is set: it loads or makes the host key, listens and serves in a
// goroutine that the returned function stops and waits for. It returns what
// the pages and the API show of the server (the zero value when it is off).
// The server stops when ctx is done too. An error here (a port in use, a host
// key that cannot be read) stops serve before anything else runs.
func startSSH(ctx context.Context, cfg config.Config, signIn sshcode.SignIn, limiter sshcode.Limiter, logger *slog.Logger) (func(), app.SSHServerInfo, error) {
	if cfg.SSHAddr == "" {
		return func() {}, app.SSHServerInfo{}, nil
	}
	hostKey, err := sshcode.LoadHostKey(cfg.SSHHostKeyPath())
	if err != nil {
		return nil, app.SSHServerInfo{}, err
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.SSHAddr)
	if err != nil {
		return nil, app.SSHServerInfo{}, errors.NewErr(tagSSHListenFailed, err).SetMetadata("addr", cfg.SSHAddr)
	}
	srv := sshcode.New(sshcode.Config{HostKey: hostKey}, signIn, limiter, logger)
	sshCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Serve(sshCtx, ln); err != nil {
			logger.Error("the SSH server stopped", "err", err)
		}
	}()
	info := app.SSHServerInfo{
		// The listener's address, so that ":0" shows the port it got.
		Addr:            ln.Addr().String(),
		PublicHost:      cfg.SSHPublicHostOrDefault(),
		HostFingerprint: ssh.FingerprintSHA256(hostKey.PublicKey()),
	}
	return func() {
		cancel()
		<-done
	}, info, nil
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

// openBridge opens a cluster.Bridge with the options.
func openBridge(ctx context.Context, redisURL string, hub *live.Hub, logger *slog.Logger, opts ...cluster.BridgeOption) (publisherCloser, error) {
	b, err := cluster.NewBridge(ctx, redisURL, hub, logger, opts...)
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

// presenceCloser is the live.Presence of the process when it runs with Redis:
// it also has to be closed. *cluster.Presence is one.
type presenceCloser interface {
	live.Presence
	Close() error
}

// presenceOpener opens the presence in Redis; openPresence is the real one and
// tests pass a fake.
type presenceOpener func(ctx context.Context, redisURL string, logger *slog.Logger) (presenceCloser, error)

// openPresence opens a cluster.Presence.
func openPresence(ctx context.Context, redisURL string, logger *slog.Logger) (presenceCloser, error) {
	p, err := cluster.NewPresence(ctx, redisURL, logger)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// newPresence makes the presence of the process: counted in memory when
// redisURL is empty, else kept in Redis, so that "who is online" covers every
// process (SPEC §7.15, §8.1). The returned function stops what must be stopped
// (the refresher of the Redis one) and does nothing for the in-memory one.
func newPresence(ctx context.Context, redisURL string, logger *slog.Logger, open presenceOpener) (live.Presence, func(), error) {
	if redisURL == "" {
		return live.NewMemoryPresence(), func() {}, nil
	}
	p, err := open(ctx, redisURL, logger)
	if err != nil {
		return nil, nil, err
	}
	logger.Info("presence is kept in Redis", "redis", config.RedactURL(redisURL))
	return p, func() {
		if closeErr := p.Close(); closeErr != nil {
			logger.Error("closing the presence in Redis failed", "err", closeErr)
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
	maxCalls, err := envInt(env, "CLOWDER_MAX_CALLS", def.MaxCalls)
	if err != nil {
		return config.Config{}, err
	}
	callRingTimeout, err := envDuration(env, "CLOWDER_CALL_RING_TIMEOUT", def.CallRingTimeout)
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
	fs.StringVar(&cfg.SSHAddr, "ssh-addr", envString(env, "CLOWDER_SSH_ADDR", def.SSHAddr),
		"address of the SSH server of the sign-in codes, such as :2222; empty: off (CLOWDER_SSH_ADDR)")
	fs.StringVar(&cfg.SSHHostKey, "ssh-host-key", envString(env, "CLOWDER_SSH_HOST_KEY", def.SSHHostKey),
		"path of the SSH host key; made on the first start; default: ssh_host_ed25519_key in the data directory (CLOWDER_SSH_HOST_KEY)")
	fs.StringVar(&cfg.SSHPublicHost, "ssh-public-host", envString(env, "CLOWDER_SSH_PUBLIC_HOST", def.SSHPublicHost),
		"host shown in the ssh command of the settings page; default: the host of the base URL (CLOWDER_SSH_PUBLIC_HOST)")
	fs.StringVar(&cfg.MediaAddr, "media-addr", envString(env, "CLOWDER_MEDIA_ADDR", def.MediaAddr),
		"address of the media server of the calls, as host:port, such as :7882: one UDP and one TCP port; empty: calls are off (CLOWDER_MEDIA_ADDR)")
	fs.StringVar(&cfg.MediaPublicIP, "media-public-ip", envString(env, "CLOWDER_MEDIA_PUBLIC_IP", def.MediaPublicIP),
		"IP address that browsers reach the media server at, when it runs behind NAT; default: the addresses of the machine (CLOWDER_MEDIA_PUBLIC_IP)")
	fs.IntVar(&cfg.MaxCalls, "max-calls", maxCalls,
		"most calls (ringing or in progress) this process holds at once (CLOWDER_MAX_CALLS)")
	fs.DurationVar(&cfg.CallRingTimeout, "call-ring-timeout", callRingTimeout,
		"how long a call rings before it is missed (CLOWDER_CALL_RING_TIMEOUT)")
	fs.StringVar(&cfg.MetricsAddr, "metrics-addr", envString(env, "CLOWDER_METRICS_ADDR", def.MetricsAddr),
		"address of the listener that serves the metrics at GET /metrics, such as 127.0.0.1:9090; no login: bind it to a private address; empty: no metrics (CLOWDER_METRICS_ADDR)")
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
	tagSSHListenFailed     = "SSHListenFailed"
)
