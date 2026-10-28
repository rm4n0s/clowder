package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/errors"
)

func TestDefaultValidates(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Default().Validate() = %v", err)
	}
	want := Config{
		ListenAddr:            ":8080",
		DataDir:               "./data",
		BaseURL:               "http://localhost:8080",
		MaxAttachmentSize:     25 * 1024 * 1024,
		MaxAttachmentsPerPost: 10,
		SessionLifetime:       30 * 24 * time.Hour,
		InvitationLifetime:    7 * 24 * time.Hour,
		LogLevel:              "info",
		TrustProxyHeaders:     false,
		MaxCalls:              20,
		CallRingTimeout:       45 * time.Second,
	}
	if cfg != want {
		t.Errorf("Default() = %+v, want %+v", cfg, want)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantTag string // empty: valid
	}{
		{"default", func(*Config) {}, ""},
		{"https base URL", func(c *Config) { c.BaseURL = "https://clowder.example.com" }, ""},
		{"trust proxy headers", func(c *Config) { c.TrustProxyHeaders = true }, ""},
		{"log level debug", func(c *Config) { c.LogLevel = "debug" }, ""},
		{"log level warn", func(c *Config) { c.LogLevel = "warn" }, ""},
		{"log level error", func(c *Config) { c.LogLevel = "error" }, ""},
		{"empty listen address", func(c *Config) { c.ListenAddr = "" }, TagListenAddrEmpty},
		{"empty data directory", func(c *Config) { c.DataDir = "" }, TagDataDirEmpty},
		{"empty base URL", func(c *Config) { c.BaseURL = "" }, TagBaseURLInvalid},
		{"relative base URL", func(c *Config) { c.BaseURL = "/chat" }, TagBaseURLInvalid},
		{"base URL without host", func(c *Config) { c.BaseURL = "http://" }, TagBaseURLInvalid},
		{"base URL with other scheme", func(c *Config) { c.BaseURL = "ftp://example.com" }, TagBaseURLInvalid},
		{"base URL that does not parse", func(c *Config) { c.BaseURL = "http://exa mple.com/%zz" }, TagBaseURLInvalid},
		{"zero attachment size", func(c *Config) { c.MaxAttachmentSize = 0 }, TagMaxAttachmentSizeInvalid},
		{"negative attachment size", func(c *Config) { c.MaxAttachmentSize = -1 }, TagMaxAttachmentSizeInvalid},
		{"zero attachments per post", func(c *Config) { c.MaxAttachmentsPerPost = 0 }, TagMaxAttachmentsPerPostInvalid},
		{"negative attachments per post", func(c *Config) { c.MaxAttachmentsPerPost = -3 }, TagMaxAttachmentsPerPostInvalid},
		{"zero session lifetime", func(c *Config) { c.SessionLifetime = 0 }, TagSessionLifetimeInvalid},
		{"negative session lifetime", func(c *Config) { c.SessionLifetime = -time.Hour }, TagSessionLifetimeInvalid},
		{"zero invitation lifetime", func(c *Config) { c.InvitationLifetime = 0 }, TagInvitationLifetimeInvalid},
		{"negative invitation lifetime", func(c *Config) { c.InvitationLifetime = -time.Hour }, TagInvitationLifetimeInvalid},
		{"database URL postgres", func(c *Config) { c.DatabaseURL = "postgres://u:p@localhost:5432/clowder" }, ""},
		{"database URL postgresql", func(c *Config) { c.DatabaseURL = "postgresql://localhost/clowder?sslmode=disable" }, ""},
		{"database URL sqlite scheme", func(c *Config) { c.DatabaseURL = "sqlite://clowder.db" }, TagDatabaseURLInvalid},
		{"database URL http scheme", func(c *Config) { c.DatabaseURL = "http://localhost/clowder" }, TagDatabaseURLInvalid},
		{"database URL garbage", func(c *Config) { c.DatabaseURL = "not a url" }, TagDatabaseURLInvalid},
		{"database URL without host", func(c *Config) { c.DatabaseURL = "postgres:///clowder" }, TagDatabaseURLInvalid},
		{"database URL that does not parse", func(c *Config) { c.DatabaseURL = "postgres://u:secret@exa mple.com/%zz" }, TagDatabaseURLInvalid},
		{"redis URL", func(c *Config) {
			c.DatabaseURL = "postgres://localhost/clowder"
			c.RedisURL = "redis://localhost:6379/0"
		}, ""},
		{"redis URL with password", func(c *Config) {
			c.DatabaseURL = "postgres://localhost/clowder"
			c.RedisURL = "redis://:secret@redis:6379/0"
		}, ""},
		{"rediss URL with user and password", func(c *Config) {
			c.DatabaseURL = "postgres://localhost/clowder"
			c.RedisURL = "rediss://default:secret@redis.example.com:6380"
		}, ""},
		{"redis URL with an upper case scheme", func(c *Config) {
			c.DatabaseURL = "postgres://localhost/clowder"
			c.RedisURL = "REDIS://localhost"
		}, ""},
		{"redis URL http scheme", func(c *Config) { c.DatabaseURL = "postgres://localhost/clowder"; c.RedisURL = "http://localhost:6379" }, TagRedisURLInvalid},
		{"redis URL postgres scheme", func(c *Config) {
			c.DatabaseURL = "postgres://localhost/clowder"
			c.RedisURL = "postgres://localhost/clowder"
		}, TagRedisURLInvalid},
		{"redis URL garbage", func(c *Config) { c.DatabaseURL = "postgres://localhost/clowder"; c.RedisURL = "not a url" }, TagRedisURLInvalid},
		{"redis URL without host", func(c *Config) { c.DatabaseURL = "postgres://localhost/clowder"; c.RedisURL = "redis:///0" }, TagRedisURLInvalid},
		{"redis URL that does not parse", func(c *Config) {
			c.DatabaseURL = "postgres://localhost/clowder"
			c.RedisURL = "redis://u:secret@exa mple.com/%zz"
		}, TagRedisURLInvalid},
		{"redis URL with SQLite", func(c *Config) { c.RedisURL = "redis://localhost:6379/0" }, TagRedisNeedsPostgres},
		{"invalid redis URL is reported before the missing PostgreSQL", func(c *Config) { c.RedisURL = "http://localhost" }, TagRedisURLInvalid},
		{"ssh address with a port", func(c *Config) { c.SSHAddr = ":2222" }, ""},
		{"ssh address with a host", func(c *Config) { c.SSHAddr = "127.0.0.1:2222" }, ""},
		{"ssh address of an IPv6 host", func(c *Config) { c.SSHAddr = "[::1]:2222" }, ""},
		{"ssh address without a port", func(c *Config) { c.SSHAddr = "localhost" }, TagSSHAddrInvalid},
		{"ssh address with only a number", func(c *Config) { c.SSHAddr = "2222" }, TagSSHAddrInvalid},
		{"ssh address with a word for a port", func(c *Config) { c.SSHAddr = ":ssh" }, TagSSHAddrInvalid},
		{"ssh address with a port too large", func(c *Config) { c.SSHAddr = ":65536" }, TagSSHAddrInvalid},
		{"ssh address that is a URL", func(c *Config) { c.SSHAddr = "ssh://host:22" }, TagSSHAddrInvalid},
		{"ssh host key in the data directory", func(c *Config) { c.SSHHostKey = filepath.Join(t.TempDir(), "key") }, ""},
		{"ssh host key in a missing directory", func(c *Config) { c.SSHHostKey = filepath.Join(t.TempDir(), "missing", "key") }, TagSSHHostKeyDirMissing},
		{"ssh host key in a file, not a directory", func(c *Config) {
			f := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(f, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			c.SSHHostKey = filepath.Join(f, "key")
		}, TagSSHHostKeyDirMissing},
		{"ssh public host", func(c *Config) { c.SSHPublicHost = "chat.example.org" }, ""},
		{"ssh public host that is an IP", func(c *Config) { c.SSHPublicHost = "192.0.2.7" }, ""},
		{"ssh public host that is an IPv6 address", func(c *Config) { c.SSHPublicHost = "2001:db8::1" }, ""},
		{"ssh public host with a scheme", func(c *Config) { c.SSHPublicHost = "ssh://chat.example.org" }, TagSSHPublicHostInvalid},
		{"ssh public host with a path", func(c *Config) { c.SSHPublicHost = "chat.example.org/ssh" }, TagSSHPublicHostInvalid},
		{"ssh public host with a port", func(c *Config) { c.SSHPublicHost = "chat.example.org:2222" }, TagSSHPublicHostInvalid},
		{"ssh public host with a user", func(c *Config) { c.SSHPublicHost = "alice@chat.example.org" }, TagSSHPublicHostInvalid},
		{"ssh public host with a space", func(c *Config) { c.SSHPublicHost = "chat example" }, TagSSHPublicHostInvalid},
		{"no metrics address", func(c *Config) { c.MetricsAddr = "" }, ""},
		{"metrics address with a host", func(c *Config) { c.MetricsAddr = "127.0.0.1:9090" }, ""},
		{"metrics address with only a port", func(c *Config) { c.MetricsAddr = ":9090" }, ""},
		{"metrics address with an ephemeral port", func(c *Config) { c.MetricsAddr = "127.0.0.1:0" }, ""},
		{"metrics address without a port", func(c *Config) { c.MetricsAddr = "localhost" }, TagMetricsAddrInvalid},
		{"metrics address with only a number", func(c *Config) { c.MetricsAddr = "9090" }, TagMetricsAddrInvalid},
		{"metrics address with a port too large", func(c *Config) { c.MetricsAddr = ":65536" }, TagMetricsAddrInvalid},
		{"metrics address with a negative port", func(c *Config) { c.MetricsAddr = ":-1" }, TagMetricsAddrInvalid},
		{"metrics address that is a URL", func(c *Config) { c.MetricsAddr = "http://host:9090" }, TagMetricsAddrInvalid},
		{"media address with a port", func(c *Config) { c.MediaAddr = ":7882" }, ""},
		{"media address with a host", func(c *Config) { c.MediaAddr = "127.0.0.1:7882" }, ""},
		{"media address with an ephemeral port", func(c *Config) { c.MediaAddr = "127.0.0.1:0" }, ""},
		{"media address without a port", func(c *Config) { c.MediaAddr = "localhost" }, TagMediaAddrInvalid},
		{"media address with only a number", func(c *Config) { c.MediaAddr = "7882" }, TagMediaAddrInvalid},
		{"media address with a port too large", func(c *Config) { c.MediaAddr = ":65536" }, TagMediaAddrInvalid},
		{"media address that is a URL", func(c *Config) { c.MediaAddr = "udp://host:7882" }, TagMediaAddrInvalid},
		{"public media IPv4", func(c *Config) { c.MediaPublicIP = "203.0.113.7" }, ""},
		{"public media IPv6", func(c *Config) { c.MediaPublicIP = "2001:db8::1" }, ""},
		{"public media IP that is a host name", func(c *Config) { c.MediaPublicIP = "chat.example.org" }, TagMediaPublicIPInvalid},
		{"public media IP with a port", func(c *Config) { c.MediaPublicIP = "203.0.113.7:7882" }, TagMediaPublicIPInvalid},
		{"one call at most", func(c *Config) { c.MaxCalls = 1 }, ""},
		{"no calls at most", func(c *Config) { c.MaxCalls = 0 }, TagMaxCallsInvalid},
		{"negative calls at most", func(c *Config) { c.MaxCalls = -2 }, TagMaxCallsInvalid},
		{"a call rings for a second", func(c *Config) { c.CallRingTimeout = time.Second }, ""},
		{"a call rings for half a second", func(c *Config) { c.CallRingTimeout = time.Second / 2 }, TagCallRingTimeoutInvalid},
		{"a call rings for no time", func(c *Config) { c.CallRingTimeout = 0 }, TagCallRingTimeoutInvalid},
		{"a call rings for a negative time", func(c *Config) { c.CallRingTimeout = -time.Second }, TagCallRingTimeoutInvalid},
		{"unknown log level", func(c *Config) { c.LogLevel = "verbose" }, TagLogLevelUnknown},
		{"empty log level", func(c *Config) { c.LogLevel = "" }, TagLogLevelUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(&cfg)
			err := cfg.Validate()
			if tt.wantTag == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want tag %s", tt.wantTag)
			}
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T: %v", err, err)
			}
			if appErr.Tag != tt.wantTag {
				t.Errorf("tag = %s, want %s", appErr.Tag, tt.wantTag)
			}
			if !appErr.HasRoute("Config.Validate." + tt.wantTag) {
				t.Errorf("unexpected failure path: %s", appErr.Route())
			}
		})
	}
}

func TestValidateMetadata(t *testing.T) {
	cfg := Default()
	cfg.BaseURL = "ftp://example.com"
	appErr, ok := errors.FromError(cfg.Validate())
	if !ok {
		t.Fatal("expected *errors.Error")
	}
	if appErr.Metadata["baseURL"] != "ftp://example.com" {
		t.Errorf("metadata = %v", appErr.Metadata)
	}
}

func TestSecureCookies(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    bool
	}{
		{"http", "http://localhost:8080", false},
		{"http upper case scheme", "HTTP://localhost:8080", false},
		{"https", "https://clowder.example.com", true},
		{"invalid URL stays secure", "http://exa mple.com/%zz", true},
		{"empty stays secure", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.BaseURL = tt.baseURL
			if got := cfg.SecureCookies(); got != tt.want {
				t.Errorf("SecureCookies() with %q = %v, want %v", tt.baseURL, got, tt.want)
			}
		})
	}
}

func TestValidateDatabaseURLNeverLeaksThePassword(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"mysql://u:hunter2@localhost/db",
		"postgres://u:hunter2@exa mple.com/%zz",
		"http://u:hunter2@localhost/db",
	} {
		cfg := Default()
		cfg.DatabaseURL = raw
		err := cfg.Validate()
		appErr, ok := errors.FromError(err)
		if !ok {
			t.Fatalf("%q: expected *errors.Error, got %T", raw, err)
		}
		if strings.Contains(appErr.Message, "hunter2") || strings.Contains(fmt.Sprint(appErr.Metadata), "hunter2") ||
			strings.Contains(appErr.Error(), "hunter2") {
			t.Errorf("%q: the error leaks the password: %v %v", raw, appErr.Message, appErr.Metadata)
		}
	}
}

func TestValidateRedisURLNeverLeaksThePassword(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, database, redis string }{
		{"wrong scheme", "postgres://localhost/db", "http://:hunter2@localhost/0"},
		{"does not parse", "postgres://localhost/db", "redis://:hunter2@exa mple.com/%zz"},
		{"needs PostgreSQL", "", "redis://default:hunter2@localhost/0"},
		{"no host", "postgres://localhost/db", "redis://:hunter2@/0"},
	} {
		cfg := Default()
		cfg.DatabaseURL = tt.database
		cfg.RedisURL = tt.redis
		err := cfg.Validate()
		appErr, ok := errors.FromError(err)
		if !ok {
			t.Fatalf("%s: expected *errors.Error, got %T", tt.name, err)
		}
		if strings.Contains(appErr.Message, "hunter2") || strings.Contains(fmt.Sprint(appErr.Metadata), "hunter2") ||
			strings.Contains(appErr.Error(), "hunter2") {
			t.Errorf("%s: the error leaks the password: %v %v", tt.name, appErr.Message, appErr.Metadata)
		}
	}
}

func TestRedactURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"with password", "postgres://clowder:hunter2@db:5432/clowder", "postgres://clowder:xxxxx@db:5432/clowder"},
		{"without password", "postgres://clowder@db:5432/clowder", "postgres://clowder@db:5432/clowder"},
		{"without user", "postgres://db/clowder", "postgres://db/clowder"},
		{"query parameters kept", "postgres://u:pw@db/clowder?sslmode=disable&application_name=x",
			"postgres://u:xxxxx@db/clowder?sslmode=disable&application_name=x"},
		{"password in the query", "postgres://u@db/clowder?password=hunter2&sslmode=disable",
			"postgres://u@db/clowder?password=xxxxx&sslmode=disable"},
		{"special characters in the password", "postgres://u:p%40ss%2Fword@db/clowder", "postgres://u:xxxxx@db/clowder"},
		{"redis with password and no user", "redis://:hunter2@redis:6379/0", "redis://:xxxxx@redis:6379/0"},
		{"redis with user and password", "redis://default:hunter2@redis:6379/0", "redis://default:xxxxx@redis:6379/0"},
		{"rediss with password", "rediss://:hunter2@redis.example.com:6380", "rediss://:xxxxx@redis.example.com:6380"},
		{"redis without password", "redis://redis:6379/0", "redis://redis:6379/0"},
		{"unparseable", "postgres://u:hunter2@exa mple.com/%zz", unparseableURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := RedactURL(tt.in)
			if got != tt.want {
				t.Errorf("RedactURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if strings.Contains(got, "hunter2") {
				t.Errorf("RedactURL(%q) = %q leaks the password", tt.in, got)
			}
		})
	}
}

func TestSSHDefaults(t *testing.T) {
	tests := []struct {
		name         string
		mutate       func(*Config)
		wantKey      string
		wantHostName string
	}{
		{"defaults", func(*Config) {}, filepath.Join("./data", "ssh_host_ed25519_key"), "localhost"},
		{"another data directory", func(c *Config) { c.DataDir = "/var/lib/clowder" }, "/var/lib/clowder/ssh_host_ed25519_key", "localhost"},
		{"a named key", func(c *Config) { c.SSHHostKey = "/etc/clowder/host_key" }, "/etc/clowder/host_key", "localhost"},
		{"the host of the base URL", func(c *Config) { c.BaseURL = "https://chat.example.org:8443/x" }, filepath.Join("./data", "ssh_host_ed25519_key"), "chat.example.org"},
		{"a named public host", func(c *Config) { c.SSHPublicHost = "ssh.example.org" }, filepath.Join("./data", "ssh_host_ed25519_key"), "ssh.example.org"},
		{"a base URL that does not parse", func(c *Config) { c.BaseURL = "http://exa mple.com/%zz" }, filepath.Join("./data", "ssh_host_ed25519_key"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(&cfg)
			if got := cfg.SSHHostKeyPath(); got != tt.wantKey {
				t.Errorf("SSHHostKeyPath() = %q, want %q", got, tt.wantKey)
			}
			if got := cfg.SSHPublicHostOrDefault(); got != tt.wantHostName {
				t.Errorf("SSHPublicHostOrDefault() = %q, want %q", got, tt.wantHostName)
			}
		})
	}
}
