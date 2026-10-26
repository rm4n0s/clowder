package config

import (
	"net/url"
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
		return errors.New(tagListenAddrEmpty, "listen address is empty")
	}
	if c.DataDir == "" {
		return errors.New(tagDataDirEmpty, "data directory is empty")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return errors.NewErr(tagBaseURLInvalid, err).SetMetadata("baseURL", c.BaseURL)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New(tagBaseURLInvalid, "base URL is not an absolute http or https URL",
			"baseURL", c.BaseURL)
	}
	if c.MaxAttachmentSize <= 0 {
		return errors.New(tagMaxAttachmentSizeInvalid, "maximum attachment size must be positive",
			"value", c.MaxAttachmentSize)
	}
	if c.MaxAttachmentsPerPost <= 0 {
		return errors.New(tagMaxAttachmentsPerPostInvalid, "maximum attachments per post must be positive",
			"value", c.MaxAttachmentsPerPost)
	}
	if c.SessionLifetime <= 0 {
		return errors.New(tagSessionLifetimeInvalid, "session lifetime must be positive",
			"value", c.SessionLifetime.String())
	}
	if c.InvitationLifetime <= 0 {
		return errors.New(tagInvitationLifetimeInvalid, "invitation lifetime must be positive",
			"value", c.InvitationLifetime.String())
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return errors.New(tagLogLevelUnknown, "log level must be debug, info, warn or error",
			"value", c.LogLevel)
	}
	return nil
}

const (
	tagListenAddrEmpty              = "ListenAddrEmpty"
	tagDataDirEmpty                 = "DataDirEmpty"
	tagBaseURLInvalid               = "BaseURLInvalid"
	tagMaxAttachmentSizeInvalid     = "MaxAttachmentSizeInvalid"
	tagMaxAttachmentsPerPostInvalid = "MaxAttachmentsPerPostInvalid"
	tagSessionLifetimeInvalid       = "SessionLifetimeInvalid"
	tagInvitationLifetimeInvalid    = "InvitationLifetimeInvalid"
	tagLogLevelUnknown              = "LogLevelUnknown"
)
