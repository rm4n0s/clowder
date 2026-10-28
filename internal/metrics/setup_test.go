package metrics

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestNewSetWritesEveryMetric(t *testing.T) {
	s, err := NewSet(SetOptions{Version: "v9.9.9", GoVersion: "go1.test", Start: time.Unix(1700000000, 0), Redis: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterPool(func() sql.DBStats {
		return sql.DBStats{OpenConnections: 3, InUse: 1, Idle: 2, WaitCount: 7, WaitDuration: 1500 * time.Millisecond}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterCalls(func() int { return 2 }); err != nil {
		t.Fatal(err)
	}
	out := scrape(t, s.Registry)
	for _, want := range []string{
		"# TYPE clowder_http_requests_total counter",
		"# TYPE clowder_http_request_duration_seconds histogram",
		"# TYPE clowder_sse_streams_open gauge",
		"clowder_sse_streams_open 0",
		`clowder_websockets_open{kind="api"} 0`,
		`clowder_websockets_open{kind="call"} 0`,
		`clowder_hub_dropped_total{reason="overflow"} 0`,
		`clowder_hub_dropped_total{reason="reconnect"} 0`,
		`clowder_logins_total{result="ok"} 0`,
		`clowder_logins_total{result="ssh_code_wrong"} 0`,
		"clowder_ssh_codes_issued_total 0",
		"clowder_calls_active 2",
		`clowder_calls_total{outcome="hangup"} 0`,
		`clowder_db_connections{state="idle"} 2`,
		`clowder_db_connections{state="in_use"} 1`,
		`clowder_db_connections{state="open"} 3`,
		"clowder_db_wait_total 7",
		"clowder_db_wait_seconds_total 1.5",
		"clowder_redis_reconnects_total 0",
		"clowder_redis_bridge_published_total 0",
		"clowder_redis_bridge_received_total 0",
		`clowder_build_info{version="v9.9.9",go_version="go1.test"} 1`,
		"go_goroutines ",
		"go_memstats_heap_alloc_bytes ",
		"go_memstats_heap_sys_bytes ",
		"# TYPE go_gc_cycles_total counter",
		"process_start_time_seconds 1700000000",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "go_goroutines ") && line == "go_goroutines 0" {
			t.Error("go_goroutines is 0")
		}
		if strings.HasPrefix(line, "go_memstats_heap_alloc_bytes ") && line == "go_memstats_heap_alloc_bytes 0" {
			t.Error("the heap is 0 bytes")
		}
	}
}

func TestNewSetWithoutRedisHasNoBridgeMetrics(t *testing.T) {
	s, err := NewSet(SetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out := scrape(t, s.Registry)
	if strings.Contains(out, "redis") {
		t.Errorf("Redis metrics without Redis:\n%s", out)
	}
	if !strings.Contains(out, `clowder_build_info{version="unknown",go_version="go`) {
		t.Errorf("build info:\n%s", out)
	}
	s.RedisPublished.Inc() // nil: does nothing
}

func TestSetFoldsOpenValuesOfClosedLabels(t *testing.T) {
	s, err := NewSet(SetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s.HTTPRequests.With("GET /x", "BREW", "9xx").Inc()
	s.Logins.With("bob-guessed-it").Inc()
	out := scrape(t, s.Registry)
	for _, want := range []string{
		`clowder_http_requests_total{pattern="GET /x",method="other",code="other"} 1`,
		`clowder_logins_total{result="other"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestRegisterTwiceFails(t *testing.T) {
	s, err := NewSet(SetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterCalls(func() int { return 0 }); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterCalls(func() int { return 0 }); err == nil {
		t.Error("a second RegisterCalls succeeded")
	}
	if err := s.RegisterPool(func() sql.DBStats { return sql.DBStats{} }); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterPool(func() sql.DBStats { return sql.DBStats{} }); err == nil {
		t.Error("a second RegisterPool succeeded")
	}
}
