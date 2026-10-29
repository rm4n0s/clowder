package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/metrics"
	"github.com/rm4n0s/errors"
)

// scrapeURL fetches the URL and returns the status and the body.
func scrapeURL(t *testing.T, method, rawURL string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, string(body)
}

func TestStartMetrics(t *testing.T) {
	set, err := metrics.NewSet(metrics.SetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.DiscardHandler)

	t.Run("off", func(t *testing.T) {
		stop, addr, err := startMetrics(t.Context(), config.Config{}, set.Registry, logger)
		if err != nil || addr != "" {
			t.Fatalf("startMetrics = %q, %v; want no listener", addr, err)
		}
		stop()
		stop()
	})

	t.Run("serves GET /metrics only and stops", func(t *testing.T) {
		stop, addr, err := startMetrics(t.Context(), config.Config{MetricsAddr: "127.0.0.1:0"}, set.Registry, logger)
		if err != nil {
			t.Fatal(err)
		}
		base := "http://" + addr
		status, header, body := scrapeURL(t, http.MethodGet, base+"/metrics")
		if status != http.StatusOK || !strings.Contains(body, "# TYPE clowder_build_info gauge") {
			t.Errorf("GET /metrics = %d %q", status, body)
		}
		if got := header.Get("Content-Type"); got != "text/plain; version=0.0.4; charset=utf-8" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q", got)
		}
		for _, tt := range []struct {
			method, path string
			want         int
		}{
			{http.MethodGet, "/", http.StatusNotFound},
			{http.MethodGet, "/login", http.StatusNotFound},
			{http.MethodGet, "/metrics/more", http.StatusNotFound},
			{http.MethodGet, "/debug/pprof/", http.StatusNotFound},
			{http.MethodPost, "/metrics", http.StatusMethodNotAllowed},
			{http.MethodHead, "/metrics", http.StatusMethodNotAllowed},
			{http.MethodPut, "/metrics", http.StatusMethodNotAllowed},
		} {
			if status, _, _ := scrapeURL(t, tt.method, base+tt.path); status != tt.want {
				t.Errorf("%s %s = %d, want %d", tt.method, tt.path, status, tt.want)
			}
		}
		stop()
		stop() // twice is fine
		if conn, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			_ = conn.Close()
			t.Error("the metrics port still answers after stop")
		}
	})

	t.Run("a port in use", func(t *testing.T) {
		busy, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = busy.Close() }()
		_, _, err = startMetrics(t.Context(), config.Config{MetricsAddr: busy.Addr().String()}, set.Registry, logger)
		appErr, ok := errors.FromError(err)
		if !ok || !appErr.HasRoute("startMetrics.MetricsListenFailed") {
			t.Fatalf("err = %v, want the route startMetrics.MetricsListenFailed", err)
		}
		if appErr.Metadata["addr"] != busy.Addr().String() {
			t.Errorf("metadata = %v", appErr.Metadata)
		}
	})
}

func TestReportToNilIsEmpty(t *testing.T) {
	m := reportTo(nil)
	if m == nil {
		t.Fatal("reportTo(nil) = nil")
	}
	// Every metric of it is nil and accepts every call.
	m.Logins.With("ok").Inc()
	m.WebSockets.With("api").Inc()
	m.SSHCodes.Inc()
	m.HubDropped.With("overflow").Inc()
	m.LimitsRefused.With("connections").Inc()
	set, err := metrics.NewSet(metrics.SetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if reportTo(set) != set {
		t.Error("reportTo(set) is not the set")
	}
}

func TestNewMetricsSet(t *testing.T) {
	set, err := newMetricsSet(config.Config{})
	if set != nil || err != nil {
		t.Errorf("newMetricsSet with no address = %v, %v; want nil, nil", set, err)
	}
	set, err = newMetricsSet(config.Config{MetricsAddr: ":0"})
	if err != nil || set == nil {
		t.Fatalf("newMetricsSet = %v, %v", set, err)
	}
	if set.RedisPublished != nil {
		t.Error("the Redis metrics exist without Redis")
	}
	set, err = newMetricsSet(config.Config{MetricsAddr: ":0", RedisURL: "redis://localhost"})
	if err != nil || set.RedisPublished == nil {
		t.Errorf("newMetricsSet with Redis = %v, %v; want the Redis metrics", set, err)
	}
}

// serveFor starts serve with the arguments and returns the address of the web
// server and the lines of its log, and a function that stops it.
func serveFor(t *testing.T, args ...string) (webAddr, metricsAddr string, stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	var stderr syncBuffer
	done := make(chan error, 1)
	all := append([]string{"-data", t.TempDir(), "-addr", "127.0.0.1:0"}, args...)
	go func() { done <- runServe(ctx, all, mapEnv(nil), &stderr) }()
	wantMetrics := strings.Contains(strings.Join(args, " "), "-metrics-addr")
	deadline := time.Now().Add(10 * time.Second)
	for webAddr == "" || wantMetrics && metricsAddr == "" {
		for line := range strings.SplitSeq(stderr.String(), "\n") {
			var entry struct {
				Msg  string `json:"msg"`
				Addr string `json:"addr"`
			}
			if json.Unmarshal([]byte(line), &entry) != nil {
				continue
			}
			switch entry.Msg {
			case "server listening":
				webAddr = entry.Addr
			case "metrics listening":
				metricsAddr = entry.Addr
			}
		}
		select {
		case err := <-done:
			t.Fatalf("serve ended early: %+v\n%s", err, stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("serve did not start; stderr:\n%s", stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	return webAddr, metricsAddr, func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("serve returned %+v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("serve did not stop")
		}
		if !wantMetrics && strings.Contains(stderr.String(), "metrics listening") {
			t.Errorf("serve opened a metrics listener without -metrics-addr:\n%s", stderr.String())
		}
	}
}

// TestServeWithoutMetricsAddrHasNoMetrics checks that the listener is off by
// default: the log says nothing of it, and /metrics on the public address is
// the 404 page.
func TestServeWithoutMetricsAddrHasNoMetrics(t *testing.T) {
	webAddr, _, stop := serveFor(t)
	defer stop()
	status, _, body := scrapeURL(t, http.MethodGet, "http://"+webAddr+"/metrics")
	if status != http.StatusNotFound || strings.Contains(body, "clowder_") {
		t.Errorf("GET /metrics on the public address = %d %q, want the 404 page", status, body)
	}
}

// TestServeWithMetricsAddr runs serve with the listener on: it answers the
// metrics of D60 after some traffic, never on the public address, and it shuts
// down with serve.
func TestServeWithMetricsAddr(t *testing.T) {
	webAddr, metricsAddr, stop := serveFor(t, "-metrics-addr", "127.0.0.1:0")
	stopped := false
	defer func() {
		if !stopped {
			stop()
		}
	}()
	web := "http://" + webAddr
	if metricsAddr == webAddr || metricsAddr == "" {
		t.Fatalf("metrics address = %q, web address = %q", metricsAddr, webAddr)
	}

	// Some traffic: a page, a wrong login through the API, a 404 with an ID.
	scrapeURL(t, http.MethodGet, web+"/login")
	scrapeURL(t, http.MethodGet, web+"/rooms/123456789")
	scrapeURL(t, http.MethodGet, web+"/healthz")
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, web+"/api/v1/auth/login",
		strings.NewReader(url.Values{"username": {"mallory-the-guesser"}}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	// The public address does not serve them.
	if status, _, _ := scrapeURL(t, http.MethodGet, web+"/metrics"); status != http.StatusNotFound {
		t.Errorf("GET /metrics on the public address = %d, want 404", status)
	}

	status, _, body := scrapeURL(t, http.MethodGet, "http://"+metricsAddr+"/metrics")
	if status != http.StatusOK {
		t.Fatalf("GET /metrics = %d", status)
	}
	for _, want := range []string{
		// The login page twice: asked for, and where the room redirected to.
		`clowder_http_requests_total{pattern="GET /login",method="GET",code="2xx"} 2`,
		`clowder_http_requests_total{pattern="GET /rooms/{id}",method="GET",code="3xx"} 1`,
		`clowder_http_requests_total{pattern="GET /healthz",method="GET",code="2xx"} 1`,
		`clowder_http_requests_total{pattern="none",method="GET",code="4xx"} 1`,
		`clowder_http_request_duration_seconds_count{pattern="GET /login",method="GET"} 2`,
		"clowder_sse_streams_open 0",
		`clowder_websockets_open{kind="api"} 0`,
		`clowder_hub_dropped_total{reason="overflow"} 0`,
		`clowder_logins_total{result="ok"} 0`,
		"clowder_ssh_codes_issued_total 0",
		`clowder_limits_refused_total{limit="connections"} 0`,
		"clowder_calls_active 0",
		`clowder_db_connections{state="open"} `,
		"clowder_db_wait_total ",
		"clowder_db_wait_seconds_total ",
		`clowder_build_info{version="`,
		"go_goroutines ",
		"go_memstats_heap_alloc_bytes ",
		"go_memstats_heap_sys_bytes ",
		"go_gc_cycles_total ",
		"process_start_time_seconds ",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the metrics lack %q", want)
		}
	}
	if strings.Contains(body, "redis") {
		t.Error("Redis metrics without Redis")
	}
	if matched, _ := regexp.MatchString(`mallory|123456789`, body); matched {
		t.Errorf("a name or an ID is in the metrics:\n%s", body)
	}

	stop()
	stopped = true
	if conn, err := net.DialTimeout("tcp", metricsAddr, time.Second); err == nil {
		_ = conn.Close()
		t.Error("the metrics listener still answers after serve returned")
	}
}

// TestServeWithMetricsAddrInUse checks that a port in use stops serve at once,
// before it opens the database.
func TestServeWithMetricsAddrInUse(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()
	dataDir := t.TempDir()
	err = runServe(t.Context(), []string{"-data", dataDir, "-addr", "127.0.0.1:0", "-metrics-addr", busy.Addr().String()}, mapEnv(nil), &syncBuffer{})
	appErr, ok := errors.FromError(err)
	if !ok || !appErr.HasRoute("runServe->startMetrics.MetricsListenFailed") {
		t.Fatalf("err = %v, want the route runServe->startMetrics.MetricsListenFailed", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, databaseFile)); !os.IsNotExist(err) {
		t.Errorf("the database was opened before the listener failed: %v", err)
	}
}
