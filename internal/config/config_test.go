package config

import (
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
