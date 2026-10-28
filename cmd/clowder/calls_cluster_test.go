package main

import (
	"context"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/cluster"
	"github.com/rm4n0s/clowder/internal/store"
)

// TestServeAnnouncesTheProcessInRedis runs serve on PostgreSQL and Redis (the
// tests of scripts/test-docker.sh; skipped without them) with calls on: the
// process makes its key at start and removes it when serve returns, and the
// goroutine that refreshes it has ended by then (SPEC D65).
func TestServeAnnouncesTheProcessInRedis(t *testing.T) {
	pgURL, redisURL := os.Getenv(store.PostgresTestEnv), os.Getenv("CLOWDER_TEST_REDIS")
	if pgURL == "" || redisURL == "" {
		t.Skip("CLOWDER_TEST_POSTGRES and CLOWDER_TEST_REDIS are not both set (scripts/test-docker.sh sets them)")
	}
	dbURL, drop, err := store.NewPostgresDatabase(t.Context(), pgURL)
	if err != nil {
		t.Fatalf("NewPostgresDatabase: %+v", err)
	}
	t.Cleanup(func() {
		if err := drop(); err != nil {
			t.Errorf("drop: %+v", err)
		}
	})
	// A second link, with an ID of its own, asks Redis about the process.
	probe, err := cluster.NewCallLink(t.Context(), cluster.CallLinkConfig{RedisURL: redisURL, ProcessID: "probe-" + t.Name()})
	if err != nil {
		t.Fatalf("NewCallLink: %+v", err)
	}
	t.Cleanup(func() {
		if err := probe.Close(); err != nil {
			t.Errorf("probe.Close: %+v", err)
		}
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var stderr syncBuffer
	done := make(chan error, 1)
	go func() {
		done <- runServe(ctx, []string{
			"-data", t.TempDir(), "-addr", "127.0.0.1:0", "-database-url", dbURL, "-redis-url", redisURL,
			"-media-addr", "127.0.0.1:0", "-media-public-ip", "127.0.0.1",
		}, mapEnv(nil), &stderr)
	}()
	idPattern := regexp.MustCompile(`"process":"([0-9a-f]{16})"`)
	var id string
	deadline := time.Now().Add(20 * time.Second)
	for id == "" {
		select {
		case err := <-done:
			t.Fatalf("serve ended early: %+v\n%s", err, stderr.String())
		default:
		}
		if m := idPattern.FindStringSubmatch(stderr.String()); m != nil {
			id = m[1]
		}
		if time.Now().After(deadline) {
			t.Fatalf("serve did not log its process; stderr:\n%s", stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	for {
		alive, err := probe.Alive(t.Context(), id)
		if err != nil {
			t.Fatalf("Alive: %+v", err)
		}
		if alive {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("serve ended early: %+v\n%s", err, stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the process %s never announced itself; stderr:\n%s", id, stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned %+v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("serve did not stop")
	}
	if alive, err := probe.Alive(t.Context(), id); err != nil || alive {
		t.Errorf("after serve returned: Alive = %v, %v; want the key removed", alive, err)
	}
}
