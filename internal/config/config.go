package config

import (
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
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
	// DefaultMaxCalls is the most calls that one process holds at once (SPEC
	// D64).
	DefaultMaxCalls = 20
	// DefaultCallRingTimeout is how long a call rings before it is missed
	// (SPEC §7.18).
	DefaultCallRingTimeout = 45 * time.Second
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
	// SSHAddr is the address of the SSH server of the sign-in codes (SPEC
	// §7.16, D52), as host:port, such as ":2222". Empty means the server is
	// off.
	SSHAddr string
	// SSHHostKey is the path of the host key of the SSH server. Empty means
	// ssh_host_ed25519_key in DataDir; see SSHHostKeyPath. The key is made on
	// the first start when the file does not exist.
	SSHHostKey string
	// SSHPublicHost is the host shown in the ssh command of the settings
	// page: a host name or an IP address, without a scheme or a path. Empty
	// means the host of BaseURL; see SSHPublicHostOrDefault.
	SSHPublicHost string
	// MediaAddr is the address of the media server of the calls (SPEC §7.18,
	// D63, D64), as host:port, such as ":7882": one UDP port and one TCP port
	// with that number. Empty means calls are off.
	MediaAddr string
	// MediaPublicIP is the IP address, IPv4 or IPv6, that the media server
	// announces as its own to the browsers when it runs behind NAT (D64).
	// Empty announces the addresses of the machine.
	MediaPublicIP string
	// MaxCalls is the most calls (ringing or in progress) that one process
	// holds at once; at least 1.
	MaxCalls int
	// CallRingTimeout is how long a call rings before nobody answered it and
	// it becomes a missed call (SPEC §7.18); at least one second. The default
	// of 45 seconds is what people expect; a shorter time is for the tests of
	// the pages.
	CallRingTimeout time.Duration
}

// DefaultSSHHostKeyFile is the name of the host key inside the data directory
// when SSHHostKey is empty.
const DefaultSSHHostKeyFile = "ssh_host_ed25519_key"

// SSHHostKeyPath returns the path of the host key: SSHHostKey, or
// ssh_host_ed25519_key in the data directory.
func (c Config) SSHHostKeyPath() string {
	if c.SSHHostKey != "" {
		return c.SSHHostKey
	}
	return filepath.Join(c.DataDir, DefaultSSHHostKeyFile)
}

// SSHPublicHostOrDefault returns the host of the ssh command: SSHPublicHost,
// or the host name of the base URL.
func (c Config) SSHPublicHostOrDefault() string {
	if c.SSHPublicHost != "" {
		return c.SSHPublicHost
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
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
		MaxCalls:              DefaultMaxCalls,
		CallRingTimeout:       DefaultCallRingTimeout,
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
	if c.SSHAddr != "" {
		_, port, err := net.SplitHostPort(c.SSHAddr)
		if n, convErr := strconv.Atoi(port); err != nil || convErr != nil || n < 0 || n > 65535 {
			return errors.New(TagSSHAddrInvalid, "the SSH address must be host:port, such as :2222",
				"sshAddr", c.SSHAddr)
		}
	}
	if c.SSHHostKey != "" {
		// Only a key the person named is checked: the default is in the data
		// directory, which serve makes.
		if info, err := os.Stat(filepath.Dir(c.SSHHostKey)); err != nil || !info.IsDir() {
			return errors.New(TagSSHHostKeyDirMissing, "the directory of the SSH host key does not exist",
				"sshHostKey", c.SSHHostKey)
		}
	}
	if c.SSHPublicHost != "" && !validPublicHost(c.SSHPublicHost) {
		return errors.New(TagSSHPublicHostInvalid,
			"the public SSH host must be a host name or an IP address, without a scheme, a port or a path",
			"sshPublicHost", c.SSHPublicHost)
	}
	if c.MediaAddr != "" {
		_, port, err := net.SplitHostPort(c.MediaAddr)
		if n, convErr := strconv.Atoi(port); err != nil || convErr != nil || n < 0 || n > 65535 {
			return errors.New(TagMediaAddrInvalid, "the media address must be host:port, such as :7882",
				"mediaAddr", c.MediaAddr)
		}
	}
	if c.MediaPublicIP != "" && net.ParseIP(c.MediaPublicIP) == nil {
		return errors.New(TagMediaPublicIPInvalid, "the public media IP must be an IPv4 or IPv6 address",
			"mediaPublicIP", c.MediaPublicIP)
	}
	if c.MaxCalls < 1 {
		return errors.New(TagMaxCallsInvalid, "the maximum number of calls must be at least 1",
			"value", c.MaxCalls)
	}
	if c.CallRingTimeout < time.Second {
		return errors.New(TagCallRingTimeoutInvalid, "a call must ring for at least one second",
			"value", c.CallRingTimeout.String())
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return errors.New(TagLogLevelUnknown, "log level must be debug, info, warn or error",
			"value", c.LogLevel)
	}
	return nil
}

// validPublicHost reports whether host is an IP address or a host name made of
// letters, digits, dots and hyphens: what an ssh command can use.
func validPublicHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
		default:
			return false
		}
	}
	return !strings.HasPrefix(host, ".") && !strings.HasPrefix(host, "-")
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
	// TagMediaAddrInvalid is the tag of a media address that is not host:port.
	TagMediaAddrInvalid = "MediaAddrInvalid"
	// TagMediaPublicIPInvalid is the tag of a public media IP that is not an IP address.
	TagMediaPublicIPInvalid = "MediaPublicIPInvalid"
	// TagMaxCallsInvalid is the tag of a maximum number of calls below 1.
	TagMaxCallsInvalid = "MaxCallsInvalid"
	// TagCallRingTimeoutInvalid is the tag of a ring timeout below one second.
	TagCallRingTimeoutInvalid = "CallRingTimeoutInvalid"
	// TagSSHAddrInvalid is the tag of an SSH address that is not host:port.
	TagSSHAddrInvalid = "SSHAddrInvalid"
	// TagSSHHostKeyDirMissing is the tag of an SSH host key path in a directory that does not exist.
	TagSSHHostKeyDirMissing = "SSHHostKeyDirMissing"
	// TagSSHPublicHostInvalid is the tag of a public SSH host that is not a host name or an IP address.
	TagSSHPublicHostInvalid = "SSHPublicHostInvalid"
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
