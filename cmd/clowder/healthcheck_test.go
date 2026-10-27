package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/errors"
)

func TestHealthURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		addr    string
		want    string
		wantTag string
	}{
		{addr: ":8080", want: "http://127.0.0.1:8080/healthz"},
		{addr: "0.0.0.0:9000", want: "http://127.0.0.1:9000/healthz"},
		{addr: "[::]:8080", want: "http://127.0.0.1:8080/healthz"},
		{addr: "127.0.0.1:8081", want: "http://127.0.0.1:8081/healthz"},
		{addr: "localhost:8080", want: "http://localhost:8080/healthz"},
		{addr: "[::1]:8080", want: "http://[::1]:8080/healthz"},
		{addr: "clowder.internal:80", want: "http://clowder.internal:80/healthz"},
		{addr: "", wantTag: tagHealthcheckAddrInvalid},
		{addr: "8080", wantTag: tagHealthcheckAddrInvalid},
		{addr: "localhost", wantTag: tagHealthcheckAddrInvalid},
		{addr: "localhost:", wantTag: tagHealthcheckAddrInvalid},
		{addr: "localhost:http", wantTag: tagHealthcheckAddrInvalid},
		{addr: ":0", wantTag: tagHealthcheckAddrInvalid},
		{addr: ":70000", wantTag: tagHealthcheckAddrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			t.Parallel()
			got, err := healthURL(tt.addr)
			if tt.wantTag == "" {
				if err != nil {
					t.Fatalf("healthURL: %+v", err)
				}
				if got != tt.want {
					t.Errorf("healthURL = %q, want %q", got, tt.want)
				}
				return
			}
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T: %v", err, err)
			}
			if appErr.Tag != tt.wantTag || !appErr.HasRoute("healthURL."+tagHealthcheckAddrInvalid) {
				t.Errorf("tag = %s, route = %s", appErr.Tag, appErr.Route())
			}
			if appErr.Metadata["addr"] != tt.addr {
				t.Errorf("addr = %v", appErr.Metadata["addr"])
			}
		})
	}
}

// runCheck runs the healthcheck command against the address.
func runCheck(t *testing.T, args ...string) error {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := run(t.Context(), append([]string{"healthcheck"}, args...), mapEnv(nil), &stdout, &stderr, auth.FastHasher())
	if stdout.Len() != 0 {
		t.Errorf("unexpected stdout: %q", stdout.String())
	}
	return err
}

func TestHealthcheckCommand(t *testing.T) {
	t.Parallel()
	status := func(code int, body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/healthz" || r.Method != http.MethodGet {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(code)
			_, _ = w.Write([]byte(body))
		}))
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	}))
	t.Cleanup(redirect.Close)
	up, down := status(200, "ok\n"), status(503, "unavailable\n")
	t.Cleanup(up.Close)
	t.Cleanup(down.Close)

	// An address that nothing listens on: listen, note the port, close.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		args      []string
		wantTag   string
		wantRoute string
	}{
		{name: "healthy", args: []string{"-addr", strings.TrimPrefix(up.URL, "http://")}},
		{
			name: "unavailable", args: []string{"-addr", strings.TrimPrefix(down.URL, "http://")},
			wantTag: tagHealthcheckUnhealthy, wantRoute: "run->runHealthcheck->checkHealth.HealthcheckUnhealthy",
		},
		{
			name: "a redirect is not healthy", args: []string{"-addr", strings.TrimPrefix(redirect.URL, "http://")},
			wantTag: tagHealthcheckUnhealthy, wantRoute: "run->runHealthcheck->checkHealth.HealthcheckUnhealthy",
		},
		{
			name: "nothing listening", args: []string{"-addr", closedAddr},
			wantTag: tagHealthcheckRequestFailed, wantRoute: "run->runHealthcheck->checkHealth.HealthcheckRequestFailed",
		},
		{
			name: "bad address", args: []string{"-addr", "nonsense"},
			wantTag: tagHealthcheckAddrInvalid, wantRoute: "run->runHealthcheck->healthURL.HealthcheckAddrInvalid",
		},
		{
			name: "bad flag", args: []string{"-nope"},
			wantTag: tagFlagsInvalid, wantRoute: "run->runHealthcheck.FlagsInvalid",
		},
		{
			name: "extra argument", args: []string{"-addr", closedAddr, "extra"},
			wantTag: tagFlagsInvalid, wantRoute: "run->runHealthcheck.FlagsInvalid",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := runCheck(t, tt.args...)
			if tt.wantTag == "" {
				if err != nil {
					t.Fatalf("healthcheck: %+v", err)
				}
				return
			}
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T: %v", err, err)
			}
			if appErr.Tag != tt.wantTag || !appErr.HasRoute(tt.wantRoute) {
				t.Errorf("tag = %s, route = %s, want %s", appErr.Tag, appErr.Route(), tt.wantRoute)
			}
			if tt.wantTag == tagHealthcheckUnhealthy || tt.wantTag == tagHealthcheckRequestFailed {
				if !strings.HasPrefix(appErr.Error(), "healthcheck: ") {
					t.Errorf("message = %q, want the prefix \"healthcheck: \"", appErr.Error())
				}
			}
		})
	}
}

// TestHealthcheckReadsTheAddressFromTheEnvironment: CLOWDER_ADDR is the
// default of -addr, the flag wins.
func TestHealthcheckReadsTheAddressFromTheEnvironment(t *testing.T) {
	t.Parallel()
	up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(up.Close)
	addr := strings.TrimPrefix(up.URL, "http://")
	var stderr bytes.Buffer
	if err := runHealthcheck(t.Context(), nil, mapEnv(map[string]string{"CLOWDER_ADDR": addr}), &stderr); err != nil {
		t.Fatalf("with the variable: %+v", err)
	}
	err := runHealthcheck(t.Context(), []string{"-addr", "nonsense"}, mapEnv(map[string]string{"CLOWDER_ADDR": addr}), &stderr)
	if err == nil {
		t.Fatal("the flag did not win over the variable")
	}
}

func TestCheckHealthTimeoutAndBadURL(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(release) })

	start := time.Now()
	err := checkHealth(context.Background(), slow.URL+"/healthz", 50*time.Millisecond)
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("took %s", took)
	}
	appErr, ok := errors.FromError(err)
	if !ok || appErr.Tag != tagHealthcheckRequestFailed || !appErr.HasRoute("checkHealth.HealthcheckRequestFailed") {
		t.Fatalf("err = %v", err)
	}
	if hits.Load() != 1 {
		t.Errorf("hits = %d", hits.Load())
	}

	err = checkHealth(context.Background(), "http://127.0.0.1:1/\x7f", time.Second)
	appErr, ok = errors.FromError(err)
	if !ok || appErr.Tag != tagHealthcheckRequestInvalid || !appErr.HasRoute("checkHealth.HealthcheckRequestInvalid") {
		t.Fatalf("err = %v", err)
	}
}

// TestUsageListsHealthcheck: the list of commands names every command that run
// dispatches.
func TestUsageListsHealthcheck(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	usage(&buf)
	for _, name := range []string{"serve", "invite", "demo", "healthcheck"} {
		if !strings.Contains(buf.String(), "  "+name+" ") {
			t.Errorf("usage does not list %q:\n%s", name, buf.String())
		}
	}
}
