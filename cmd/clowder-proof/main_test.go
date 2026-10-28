//go:build clowder_browser

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/browsertest"
	"github.com/rm4n0s/errors"
)

func TestParseFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    options
		wantTag string
	}{
		{
			name: "defaults",
			args: []string{"-o", "x.png"},
			want: options{page: "/", out: "x.png", width: 1280, height: 800, wait: 500 * time.Millisecond},
		},
		{
			name: "all flags",
			args: []string{"-o", "x.png", "-page", "/rooms/1", "-as", "ana", "-width", "400", "-height", "300", "-wait", "1s"},
			want: options{page: "/rooms/1", out: "x.png", as: "ana", width: 400, height: 300, wait: time.Second},
		},
		{
			name: "ssh flag",
			args: []string{"-o", "x.png", "-as", "alice", "-ssh", "-page", "/me/settings"},
			want: options{page: "/me/settings", out: "x.png", as: "alice", width: 1280, height: 800, wait: 500 * time.Millisecond, ssh: true},
		},
		{
			name: "ssh code flag",
			args: []string{"-o", "x.png", "-ssh-code", "alice", "-page", "/login"},
			want: options{page: "/login", out: "x.png", width: 1280, height: 800, wait: 500 * time.Millisecond, sshCode: "alice"},
		},
		{
			name: "sign-up flags",
			args: []string{
				"-o", "x.png", "-invite", "-page", "/invite/{invite}",
				"-fill", "#username=ab", "-fill", "input[name=password]=short=1", "-click", "button[type=submit]",
			},
			want: options{
				page: "/invite/{invite}", out: "x.png", width: 1280, height: 800, wait: 500 * time.Millisecond,
				invite: true, clicks: clicks{"button[type=submit]"},
				fills: fills{{"#username", "ab"}, {"input[name=password]", "short=1"}},
			},
		},
		{
			name: "reset link",
			args: []string{"-o", "x.png", "-reset", "bob", "-page", "/reset/{reset}"},
			want: options{page: "/reset/{reset}", out: "x.png", width: 1280, height: 800, wait: 500 * time.Millisecond, reset: "bob"},
		},
		{
			name: "room placeholder",
			args: []string{"-o", "x.png", "-as", "alice", "-page", "{room:secret}"},
			want: options{page: "{room:secret}", out: "x.png", as: "alice", width: 1280, height: 800, wait: 500 * time.Millisecond},
		},
		{
			name: "board placeholder",
			args: []string{"-o", "x.png", "-as", "alice", "-page", "{board:general}"},
			want: options{page: "{board:general}", out: "x.png", as: "alice", width: 1280, height: 800, wait: 500 * time.Millisecond},
		},
		{
			name: "post placeholder",
			args: []string{"-o", "x.png", "-as", "alice", "-page", "{post:Quick question}"},
			want: options{page: "{post:Quick question}", out: "x.png", as: "alice", width: 1280, height: 800, wait: 500 * time.Millisecond},
		},
		{
			name: "conversation placeholder",
			args: []string{"-o", "x.png", "-as", "bob", "-page", "{conversation:alice,bob}"},
			want: options{page: "{conversation:alice,bob}", out: "x.png", as: "bob", width: 1280, height: 800, wait: 500 * time.Millisecond},
		},
		{
			name: "a second user",
			args: []string{
				"-o", "x.png", "-as", "bob", "-page", "{room:general}", "-other", "alice", "-other-page", "{room:general}",
				"-other-fill", "#post-body-new=Live from **alice**", "-other-fill", "#post-tags-new=a=b", "-other-click", "button[type=submit]",
			},
			want: options{
				page: "{room:general}", out: "x.png", as: "bob", width: 1280, height: 800, wait: 500 * time.Millisecond,
				other: script{
					user: "alice", page: "{room:general}", clicks: clicks{"button[type=submit]"},
					fills: fills{{"#post-body-new", "Live from **alice**"}, {"#post-tags-new", "a=b"}},
				},
			},
		},
		{
			name: "the second user stays online",
			args: []string{"-o", "x.png", "-as", "bob", "-other", "alice", "-other-hold"},
			want: options{
				page: "/", out: "x.png", as: "bob", width: 1280, height: 800, wait: 500 * time.Millisecond,
				other: script{user: "alice", page: "/"}, otherHold: true,
			},
		},
		{
			name: "clicks are made in order",
			args: []string{
				"-o", "x.png", "-as", "bob", "-click", "summary", "-click", "button.pick",
				"-other", "alice", "-other-click", "summary", "-other-click", "button.pick",
			},
			want: options{
				page: "/", out: "x.png", as: "bob", width: 1280, height: 800, wait: 500 * time.Millisecond,
				clicks: clicks{"summary", "button.pick"},
				other:  script{user: "alice", page: "/", clicks: clicks{"summary", "button.pick"}},
			},
		},
		{
			name: "a second user opens the home page by default",
			args: []string{"-o", "x.png", "-as", "bob", "-other", "alice"},
			want: options{page: "/", out: "x.png", as: "bob", width: 1280, height: 800, wait: 500 * time.Millisecond, other: script{user: "alice", page: "/"}},
		},
		{
			name: "a cluster of two servers",
			args: []string{
				"-o", "x.png", "-as", "bob", "-page", "{room:general}", "-cluster", "-postgres-url", "postgres://u:p@db:5432/postgres",
				"-redis-url", "redis://redis:6379/0", "-other", "alice", "-other-page", "{room:general}",
			},
			want: options{
				page: "{room:general}", out: "x.png", as: "bob", width: 1280, height: 800, wait: 500 * time.Millisecond,
				cluster: true, postgresURL: "postgres://u:p@db:5432/postgres", redisURL: "redis://redis:6379/0",
				other: script{user: "alice", page: "{room:general}"},
			},
		},
		{
			name: "api login",
			args: []string{"-o", "x.png", "-as", "alice", "-page", "/me/sessions", "-api-login", "clowder-apicheck"},
			want: options{page: "/me/sessions", out: "x.png", as: "alice", width: 1280, height: 800, wait: 500 * time.Millisecond, apiLogin: "clowder-apicheck"},
		},
		{
			name: "color scheme",
			args: []string{"-o", "x.png", "-as", "alice", "-color-scheme", "dark"},
			want: options{page: "/", out: "x.png", as: "alice", width: 1280, height: 800, wait: 500 * time.Millisecond, colorScheme: "dark"},
		},
		{name: "color scheme that does not exist", args: []string{"-o", "x.png", "-color-scheme", "sepia"}, wantTag: tagFlagsInvalid},
		{name: "api login without a user", args: []string{"-o", "x.png", "-api-login", "clowder-apicheck"}, wantTag: tagFlagsInvalid},
		{name: "cluster without other", args: []string{"-o", "x.png", "-as", "bob", "-cluster"}, wantTag: tagFlagsInvalid},
		{name: "cluster with ssh", args: []string{"-o", "x.png", "-as", "bob", "-other", "alice", "-cluster", "-ssh"}, wantTag: tagFlagsInvalid},
		{name: "cluster with ssh code", args: []string{"-o", "x.png", "-as", "bob", "-other", "alice", "-cluster", "-ssh-code", "alice"}, wantTag: tagFlagsInvalid},
		{name: "ssh code user is the as user", args: []string{"-o", "x.png", "-as", "alice", "-ssh-code", "alice"}, wantTag: tagFlagsInvalid},
		{name: "cluster with invite", args: []string{"-o", "x.png", "-as", "bob", "-other", "alice", "-cluster", "-invite", "-page", "/invite/{invite}"}, wantTag: tagFlagsInvalid},
		{name: "cluster with reset", args: []string{"-o", "x.png", "-as", "bob", "-other", "alice", "-cluster", "-reset", "bob", "-page", "/reset/{reset}"}, wantTag: tagFlagsInvalid},
		{name: "reset without the placeholder", args: []string{"-o", "x.png", "-reset", "bob", "-page", "/reset/x"}, wantTag: tagFlagsInvalid},
		{name: "reset placeholder without reset", args: []string{"-o", "x.png", "-page", "/reset/{reset}"}, wantTag: tagFlagsInvalid},
		{name: "postgres-url without cluster", args: []string{"-o", "x.png", "-postgres-url", "postgres://db"}, wantTag: tagFlagsInvalid},
		{name: "redis-url without cluster", args: []string{"-o", "x.png", "-redis-url", "redis://redis"}, wantTag: tagFlagsInvalid},
		{name: "other-page without other", args: []string{"-o", "x.png", "-other-page", "/rooms"}, wantTag: tagFlagsInvalid},
		{name: "other-fill without other", args: []string{"-o", "x.png", "-other-fill", "#a=b"}, wantTag: tagFlagsInvalid},
		{name: "other-click without other", args: []string{"-o", "x.png", "-other-click", "button"}, wantTag: tagFlagsInvalid},
		{name: "click without a selector", args: []string{"-o", "x.png", "-click", ""}, wantTag: tagFlagsInvalid},
		{name: "other-hold without other", args: []string{"-o", "x.png", "-other-hold"}, wantTag: tagFlagsInvalid},
		{name: "other without as", args: []string{"-o", "x.png", "-other", "alice"}, wantTag: tagFlagsInvalid},
		{name: "other-page without slash", args: []string{"-o", "x.png", "-as", "bob", "-other", "alice", "-other-page", "rooms"}, wantTag: tagFlagsInvalid},
		{name: "post placeholder without a user", args: []string{"-o", "x.png", "-page", "{post:Quick question}"}, wantTag: tagFlagsInvalid},
		{name: "board placeholder without a user", args: []string{"-o", "x.png", "-page", "{board:general}"}, wantTag: tagFlagsInvalid},
		{name: "room placeholder without a user", args: []string{"-o", "x.png", "-page", "{room:secret}"}, wantTag: tagFlagsInvalid},
		{name: "conversation placeholder without a user", args: []string{"-o", "x.png", "-page", "{conversation:alice,bob}"}, wantTag: tagFlagsInvalid},
		{name: "invite without the placeholder", args: []string{"-o", "x.png", "-invite", "-page", "/invite/x"}, wantTag: tagFlagsInvalid},
		{name: "placeholder without invite", args: []string{"-o", "x.png", "-page", "/invite/{invite}"}, wantTag: tagFlagsInvalid},
		{name: "fill without a value", args: []string{"-o", "x.png", "-fill", "#username"}, wantTag: tagFlagsInvalid},
		{name: "fill without a selector", args: []string{"-o", "x.png", "-fill", "=value"}, wantTag: tagFlagsInvalid},
		{name: "output missing", args: nil, wantTag: tagFlagsInvalid},
		{name: "page without slash", args: []string{"-o", "x.png", "-page", "rooms"}, wantTag: tagFlagsInvalid},
		{name: "zero width", args: []string{"-o", "x.png", "-width", "0"}, wantTag: tagFlagsInvalid},
		{name: "negative wait", args: []string{"-o", "x.png", "-wait", "-1s"}, wantTag: tagFlagsInvalid},
		{name: "unknown flag", args: []string{"-nope"}, wantTag: tagFlagsInvalid},
		{name: "extra argument", args: []string{"-o", "x.png", "more"}, wantTag: tagFlagsInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			got, err := parseFlags(tt.args, &stderr)
			if tt.wantTag == "" {
				if err != nil {
					t.Fatalf("parseFlags: %v", err)
				}
				if !reflect.DeepEqual(got, tt.want) {
					t.Errorf("options = %+v, want %+v", got, tt.want)
				}
				return
			}
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T: %v", err, err)
			}
			if appErr.Tag != tt.wantTag || !appErr.HasRoute("parseFlags.FlagsInvalid") {
				t.Errorf("tag = %q, route = %s", appErr.Tag, appErr.Route())
			}
		})
	}
}

func TestParseFlagsRenderIcon(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    options
		wantTag string
	}{
		{
			name: "defaults to 32 px",
			args: []string{"-o", "x.png", "-render-icon", "favicon.svg"},
			want: options{page: "/", out: "x.png", width: 1280, height: 800, wait: 500 * time.Millisecond, renderIcon: "favicon.svg", iconSize: 32},
		},
		{
			name: "all flags",
			args: []string{"-o", "x.png", "-render-icon", "favicon.svg", "-icon-size", "180", "-icon-bg", "#fbf6ee", "-icon-pad", "20"},
			want: options{page: "/", out: "x.png", width: 1280, height: 800, wait: 500 * time.Millisecond, renderIcon: "favicon.svg", iconSize: 180, iconBackground: "#fbf6ee", iconPadding: 20},
		},
		{name: "too small", args: []string{"-o", "x.png", "-render-icon", "a.svg", "-icon-size", "8"}, wantTag: tagFlagsInvalid},
		{name: "too large", args: []string{"-o", "x.png", "-render-icon", "a.svg", "-icon-size", "2048"}, wantTag: tagFlagsInvalid},
		{name: "padding too large", args: []string{"-o", "x.png", "-render-icon", "a.svg", "-icon-pad", "60"}, wantTag: tagFlagsInvalid},
		{name: "negative padding", args: []string{"-o", "x.png", "-render-icon", "a.svg", "-icon-pad", "-1"}, wantTag: tagFlagsInvalid},
		{name: "ground that is not a hex color", args: []string{"-o", "x.png", "-render-icon", "a.svg", "-icon-bg", "cream"}, wantTag: tagFlagsInvalid},
		{name: "ground with three digits", args: []string{"-o", "x.png", "-render-icon", "a.svg", "-icon-bg", "#fff"}, wantTag: tagFlagsInvalid},
		{name: "with a user", args: []string{"-o", "x.png", "-render-icon", "a.svg", "-as", "alice"}, wantTag: tagFlagsInvalid},
		{name: "with the cluster", args: []string{"-o", "x.png", "-render-icon", "a.svg", "-cluster"}, wantTag: tagFlagsInvalid},
		{name: "with ssh", args: []string{"-o", "x.png", "-render-icon", "a.svg", "-ssh"}, wantTag: tagFlagsInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			got, err := parseFlags(tt.args, &stderr)
			if tt.wantTag == "" {
				if err != nil {
					t.Fatalf("parseFlags: %v", err)
				}
				if !reflect.DeepEqual(got, tt.want) {
					t.Errorf("options = %+v, want %+v", got, tt.want)
				}
				return
			}
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T: %v", err, err)
			}
			if appErr.Tag != tt.wantTag || !appErr.HasRoute("parseFlags->options.checkRenderIcon.FlagsInvalid") {
				t.Errorf("tag = %q, route = %s", appErr.Tag, appErr.Route())
			}
		})
	}
}

func TestSplitFill(t *testing.T) {
	tests := []struct {
		in           string
		wantSelector string
		wantValue    string
		wantOK       bool
	}{
		{"#username=ab", "#username", "ab", true},
		{"#username=", "#username", "", true},
		{"input[name=username]=ab", "input[name=username]", "ab", true},
		{`input[name="a=b"]=x`, `input[name="a=b"]`, "x", true},
		{`input[name='a]=b']=x`, `input[name='a]=b']`, "x", true},
		{"#p=a=b=c", "#p", "a=b=c", true},
		{"#p=with spaces", "#p", "with spaces", true},
		{"#p", "", "", false},
		{"=x", "", "", false},
		{"", "", "", false},
		{"input[name=x]", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			selector, value, ok := splitFill(tt.in)
			if selector != tt.wantSelector || value != tt.wantValue || ok != tt.wantOK {
				t.Errorf("splitFill(%q) = %q, %q, %v; want %q, %q, %v",
					tt.in, selector, value, ok, tt.wantSelector, tt.wantValue, tt.wantOK)
			}
		})
	}
}

func TestFillsString(t *testing.T) {
	f := fills{{"#a", "1"}, {"#b", "2"}}
	if got := f.String(); got != "#a=1 #b=2" {
		t.Errorf("String = %q", got)
	}
}

// TestStartClusterNeedsServers has no PostgreSQL or Redis URL: the run fails
// with FlagsInvalid before it starts anything.
func TestStartClusterNeedsServers(t *testing.T) {
	t.Setenv(postgresURLEnv, "")
	t.Setenv(redisURLEnv, "")
	var stdout bytes.Buffer
	_, _, err := startCluster(t.Context(), options{cluster: true, postgresURL: "postgres://u:secret@db/postgres"}, &stdout)
	appErr, ok := errors.FromError(err)
	if !ok || appErr.Tag != tagFlagsInvalid || !appErr.HasRoute("startCluster.FlagsInvalid") {
		t.Fatalf("err = %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
}

// TestRunCluster takes a screenshot with -cluster: two servers, the other user
// posts on the first, and the PNG is made on the second. It needs both servers
// of the tests and Chromium.
func TestRunCluster(t *testing.T) {
	if os.Getenv(postgresURLEnv) == "" || os.Getenv(redisURLEnv) == "" {
		t.Skipf("set %s and %s (scripts/test-docker.sh does) to run the proof tool with two servers", postgresURLEnv, redisURLEnv)
	}
	if _, err := browsertest.FindChromium(); err != nil {
		t.Skipf("browser test skipped: %v", err)
	}
	out := filepath.Join(t.TempDir(), "cluster.png")
	var stdout, stderr bytes.Buffer
	err := run(t.Context(), []string{
		"-o", out, "-as", "bob", "-page", "{room:general}", "-cluster", "-other", "alice", "-other-page", "{room:general}",
		"-other-fill", "#post-body-new=Written on process A", "-other-click", `#post-editor-new button[type="submit"]`,
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run: %+v\nstderr: %s", err, stderr.String())
	}
	info, statErr := os.Stat(out)
	if statErr != nil || info.Size() == 0 {
		t.Fatalf("no PNG written: %v", statErr)
	}
	lines := strings.Split(stdout.String(), "\n")
	if len(lines) < 4 || !strings.HasPrefix(lines[0], "server A, where -other acts: http://127.0.0.1:") ||
		!strings.HasPrefix(lines[1], "server B, where the screenshot is taken: http://127.0.0.1:") ||
		!strings.HasPrefix(lines[3], "saved ") {
		t.Errorf("stdout = %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "clowder:clowder@") {
		t.Errorf("stdout holds a password: %q", stdout.String())
	}
}

func TestAPILogin(t *testing.T) {
	var got struct {
		Username   string `json:"username"`
		Password   string `json:"password"`
		ClientName string `json:"client_name"`
	}
	tests := []struct {
		name    string
		status  int
		wantTag string
	}{
		{name: "accepted", status: http.StatusOK},
		{name: "refused", status: http.StatusUnauthorized, wantTag: tagAPILoginFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/auth/login" || r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("request = %s %s %q", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
				}
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
				}
				w.WriteHeader(tt.status)
			}))
			defer ts.Close()
			err := apiLogin(t.Context(), ts.URL, "alice", "clowder-apicheck")
			if got.Username != "alice" || got.ClientName != "clowder-apicheck" || got.Password != browsertest.DemoPassword {
				t.Errorf("body = %+v", got)
			}
			if tt.wantTag == "" {
				if err != nil {
					t.Fatalf("apiLogin: %v", err)
				}
				return
			}
			appErr, ok := errors.FromError(err)
			if !ok || appErr.Tag != tt.wantTag || !appErr.HasRoute("apiLogin.APILoginFailed") {
				t.Errorf("error = %v", err)
			}
		})
	}
	// A server that is not there.
	err := apiLogin(t.Context(), "http://127.0.0.1:1", "alice", "x")
	if appErr, ok := errors.FromError(err); !ok || !appErr.HasRoute("apiLogin.APILoginFailed") {
		t.Errorf("error = %v, want APILoginFailed", err)
	}
}
