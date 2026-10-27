package config

import (
	"net/url"
	"strings"
	"time"

	"github.com/rm4n0s/errors"
)

// Defaults of the settings (SPEC §11).
const (
	// DefaultListenAddr is the address the server listens on.
	DefaultListenAddr = ":8080"
	// DefaultDataDir is the directory that holds clowder.db and files/.
	DefaultDataDir = "./data"
	// DefaultBaseURL is the URL used to build invitation links.
	DefaultBaseURL = "http://localhost:8080"
	// DefaultMaxAttachmentSize is the largest attachment accepted, in bytes (25 MB).
	DefaultMaxAttachmentSize int64 = 25 << 20
	// DefaultMaxAttachmentsPerPost is the most attachments one post may carry.
	DefaultMaxAttachmentsPerPost = 10
	// DefaultSessionLifetime is how long a login session lasts (30 days).
	DefaultSessionLifetime = 30 * 24 * time.Hour
	// DefaultInvitationLifetime is how long an invitation stays valid (7 days).
	DefaultInvitationLifetime = 7 * 24 * time.Hour
	// DefaultLogLevel is the log level.
	DefaultLogLevel = "info"
)

// Config holds the settings of a Clowder process (SPEC §11).
type Config struct {
	// ListenAddr is the address the server listens on, as host:port.
	ListenAddr string
	// DataDir is the directory that holds clowder.db and files/.
	DataDir string
	// BaseURL is the absolute http or https URL used to build invitation links.
	BaseURL string
	// MaxAttachmentSize is the largest attachment accepted, in bytes.
	MaxAttachmentSize int64
	// MaxAttachmentsPerPost is the most attachments one post may carry.
	MaxAttachmentsPerPost int
	// SessionLifetime is how long a login session lasts.
	SessionLifetime time.Duration
	// InvitationLifetime is how long an invitation stays valid.
	InvitationLifetime time.Duration
	// LogLevel is one of debug, info, warn and error.
	LogLevel string
	// TrustProxyHeaders says that Clowder runs behind a reverse proxy of our
	// own: the client address is then the last address of X-Forwarded-For (the
	// one the proxy appended) and not the address of the connection. Leave it
	// off when clients can reach Clowder directly, or they can forge the header.
	TrustProxyHeaders bool
	// DatabaseURL is the URL of the PostgreSQL database (postgres:// or
	// postgresql://, SPEC D38). Empty means SQLite in DataDir/clowder.db. It
	// holds a password: print it only through RedactURL.
	DatabaseURL string
	// RedisURL is the URL of the Redis server (redis:// or rediss://, SPEC
	// D41) that carries the live events between several Clowder processes.
	// Empty means one process with in-process events. It needs DatabaseURL
	// (PostgreSQL) and may hold a password: print it only through RedactURL.
	RedisURL string
}

// Default returns the configuration with every setting at its default.
func Default() Config {
	return Config{
		ListenAddr:            DefaultListenAddr,
		DataDir:               DefaultDataDir,
		BaseURL:               DefaultBaseURL,
		MaxAttachmentSize:     DefaultMaxAttachmentSize,
		MaxAttachmentsPerPost: DefaultMaxAttachmentsPerPost,
		SessionLifetime:       DefaultSessionLifetime,
		InvitationLifetime:    DefaultInvitationLifetime,
		LogLevel:              DefaultLogLevel,
	}
}

// SecureCookies reports whether cookies get the Secure attribute: always,
// except when the base URL is plain http://.
func (c Config) SecureCookies() bool {
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return true
	}
	return u.Scheme != "http"
}

// Validate checks every setting and returns a tagged error for the first one
// that is not usable, or nil.
func (c Config) Validate() error {
	if c.ListenAddr == "" {
		return errors.New(TagListenAddrEmpty, "listen address is empty")
	}
	if c.DataDir == "" {
		return errors.New(TagDataDirEmpty, "data directory is empty")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return errors.NewErr(TagBaseURLInvalid, err).SetMetadata("baseURL", c.BaseURL)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New(TagBaseURLInvalid, "base URL is not an absolute http or https URL",
			"baseURL", c.BaseURL)
	}
	if c.MaxAttachmentSize <= 0 {
		return errors.New(TagMaxAttachmentSizeInvalid, "maximum attachment size must be positive",
			"value", c.MaxAttachmentSize)
	}
	if c.MaxAttachmentsPerPost <= 0 {
		return errors.New(TagMaxAttachmentsPerPostInvalid, "maximum attachments per post must be positive",
			"value", c.MaxAttachmentsPerPost)
	}
	if c.SessionLifetime <= 0 {
		return errors.New(TagSessionLifetimeInvalid, "session lifetime must be positive",
			"value", c.SessionLifetime.String())
	}
	if c.InvitationLifetime <= 0 {
		return errors.New(TagInvitationLifetimeInvalid, "invitation lifetime must be positive",
			"value", c.InvitationLifetime.String())
	}
	if c.DatabaseURL != "" {
		// The error holds the redacted URL only, and not the error of
		// url.Parse, which quotes the URL.
		du, err := url.Parse(c.DatabaseURL)
		if err != nil {
			return errors.New(TagDatabaseURLInvalid, "database URL cannot be parsed",
				"databaseURL", RedactURL(c.DatabaseURL))
		}
		scheme := strings.ToLower(du.Scheme)
		if (scheme != "postgres" && scheme != "postgresql") || du.Host == "" {
			return errors.New(TagDatabaseURLInvalid,
				"database URL must be empty or a postgres:// or postgresql:// URL with a host",
				"databaseURL", RedactURL(c.DatabaseURL))
		}
	}
	if c.RedisURL != "" {
		// The error holds the redacted URL only, and not the error of
		// url.Parse, which quotes the URL.
		ru, err := url.Parse(c.RedisURL)
		if err != nil {
			return errors.New(TagRedisURLInvalid, "Redis URL cannot be parsed",
				"redisURL", RedactURL(c.RedisURL))
		}
		scheme := strings.ToLower(ru.Scheme)
		if (scheme != "redis" && scheme != "rediss") || ru.Host == "" {
			return errors.New(TagRedisURLInvalid,
				"Redis URL must be empty or a redis:// or rediss:// URL with a host",
				"redisURL", RedactURL(c.RedisURL))
		}
		if c.DatabaseURL == "" {
			return errors.New(TagRedisNeedsPostgres,
				"a Redis URL needs a PostgreSQL database URL: several processes cannot share a SQLite file",
				"redisURL", RedactURL(c.RedisURL))
		}
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return errors.New(TagLogLevelUnknown, "log level must be debug, info, warn or error",
			"value", c.LogLevel)
	}
	return nil
}

// RedactedPassword replaces a password in the output of RedactURL.
const RedactedPassword = "xxxxx"

// unparseableURL is what RedactURL returns for a string that is not a URL: its
// text could hold a password, so none of it is shown.
const unparseableURL = "<unparseable url>"

// RedactURL returns raw with its password replaced by "xxxxx", for logs and
// error metadata. The password of the user info and the query parameters
// password and sslpassword are replaced. A string that does not parse as a URL
// is replaced altogether. An empty string stays empty.
func RedactURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return unparseableURL
	}
	if u.User != nil {
		if _, ok := u.User.Password(); ok {
			u.User = url.UserPassword(u.User.Username(), RedactedPassword)
		}
	}
	if u.RawQuery != "" {
		q := u.Query()
		changed := false
		for _, key := range []string{"password", "sslpassword"} {
			if q.Has(key) {
				q.Set(key, RedactedPassword)
				changed = true
			}
		}
		if changed {
			u.RawQuery = q.Encode()
		}
	}
	return u.String()
}

const (
	// TagRedisURLInvalid is the tag of a Redis URL that is not empty and not a redis:// or rediss:// URL with a host.
	TagRedisURLInvalid = "RedisURLInvalid"
	// TagRedisNeedsPostgres is the tag of a Redis URL given without a PostgreSQL database URL.
	TagRedisNeedsPostgres = "RedisNeedsPostgres"
	// TagDatabaseURLInvalid is the tag of a database URL that is not empty and not a postgres:// or postgresql:// URL with a host.
	TagDatabaseURLInvalid = "DatabaseURLInvalid"
	// TagListenAddrEmpty is the tag of a listen address that is empty.
	TagListenAddrEmpty = "ListenAddrEmpty"
	// TagDataDirEmpty is the tag of a data directory that is empty.
	TagDataDirEmpty = "DataDirEmpty"
	// TagBaseURLInvalid is the tag of a base URL that is not an absolute http or https URL.
	TagBaseURLInvalid = "BaseURLInvalid"
	// TagMaxAttachmentSizeInvalid is the tag of a maximum attachment size that is not positive.
	TagMaxAttachmentSizeInvalid = "MaxAttachmentSizeInvalid"
	// TagMaxAttachmentsPerPostInvalid is the tag of a maximum number of attachments per post that is not positive.
	TagMaxAttachmentsPerPostInvalid = "MaxAttachmentsPerPostInvalid"
	// TagSessionLifetimeInvalid is the tag of a session lifetime that is not positive.
	TagSessionLifetimeInvalid = "SessionLifetimeInvalid"
	// TagInvitationLifetimeInvalid is the tag of an invitation lifetime that is not positive.
	TagInvitationLifetimeInvalid = "InvitationLifetimeInvalid"
	// TagLogLevelUnknown is the tag of a log level that is not debug, info, warn or error.
	TagLogLevelUnknown = "LogLevelUnknown"
)
