package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/errors"
)

// redisTestEnv names the environment variable that holds the redis:// URL of
// a server for the tests that need one (SPEC D39). scripts/test-docker.sh sets
// it.
const redisTestEnv = "CLOWDER_TEST_REDIS"

// testRedisURL returns the URL of the Redis server of the tests, or skips the
// test.
func testRedisURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv(redisTestEnv)
	if u == "" {
		t.Skipf("%s is not set: no Redis server for this test (scripts/test-docker.sh starts one)", redisTestEnv)
	}
	return u
}

// deadRedisURL returns the URL of a port where nothing listens, with a
// password in it.
func deadRedisURL(t *testing.T, password string) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("redis://:%s@%s/0", password, addr)
}

// openTestRedis opens the real client; the test closes it.
func openTestRedis(t *testing.T, url string) client {
	t.Helper()
	c, err := openRedis(t.Context(), url)
	if err != nil {
		t.Fatalf("openRedis: %v", err)
	}
	t.Cleanup(func() {
		if err := c.close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return c
}

// realNode is a process of a test on a real Redis.
type realNode struct {
	hub    *live.Hub
	bridge *Bridge
	logs   *syncBuffer
}

// newRealNode starts a process with NewBridge. The test closes it.
func newRealNode(t *testing.T, url string) *realNode {
	t.Helper()
	n := &realNode{hub: live.New(), logs: &syncBuffer{}}
	logger := slog.New(slog.NewJSONHandler(n.logs, nil))
	b, err := NewBridge(t.Context(), url, n.hub, logger)
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	n.bridge = b
	t.Cleanup(func() {
		if err := b.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
		n.hub.Close()
	})
	return n
}

func TestRedisBridgesDeliverToEachOther(t *testing.T) {
	t.Parallel()
	url := testRedisURL(t)
	topic := "room:" + t.Name()
	a, b := newRealNode(t, url), newRealNode(t, url)
	subA := a.hub.Subscribe(t.Context(), topic)
	subB := b.hub.Subscribe(t.Context(), topic)

	events := []live.Event{eventN(topic, 1), eventN(topic, 2), eventN(topic, 3)}
	for _, ev := range events {
		a.bridge.Publish(ev)
	}
	fence := live.Event{Topic: topic, Kind: kindFence}
	a.bridge.Publish(fence)
	want := append(append([]live.Event(nil), events...), fence)
	if got := collect(t, subB); !equalEvents(got, want) {
		t.Errorf("the other process got %+v, want %+v", got, want)
	}

	fence2 := live.Event{Topic: topic, Kind: kindFence, PostID: 99}
	b.bridge.Publish(fence2)
	want = append(want, fence2)
	var gotA []live.Event
	for len(gotA) == 0 || gotA[len(gotA)-1] != fence2 {
		gotA = append(gotA, nextEvent(t, subA))
	}
	if !equalEvents(gotA, want) {
		t.Errorf("the publishing process got %+v, want exactly %+v (no echo)", gotA, want)
	}
	if a.bridge.Lost() != 0 || b.bridge.Lost() != 0 {
		t.Errorf("Lost() = %d and %d, want 0", a.bridge.Lost(), b.bridge.Lost())
	}
	if lines := a.logs.entries(t, ""); len(lines) != 0 {
		t.Errorf("unexpected log lines: %s", a.logs.String())
	}
}

func TestRedisFirstConfirmationIsNotAReconnect(t *testing.T) {
	t.Parallel()
	url := testRedisURL(t)
	c := openTestRedis(t, url)
	sub, err := c.subscribe(t.Context(), EventsChannel)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	// The subscription is confirmed, so a message sent now is not missed, and
	// the first thing received is that message.
	payload := []byte(`{"origin":"x","event":{"topic":"` + t.Name() + `","kind":"k"}}`)
	if err := c.publish(t.Context(), EventsChannel, payload); err != nil {
		t.Fatalf("publish: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), wait)
	defer cancel()
	for {
		in, err := sub.receive(ctx)
		if err != nil {
			t.Fatalf("receive: %v", err)
		}
		if in.resubscribed {
			t.Fatal("the first confirmation is reported as a reconnect")
		}
		if string(in.payload) == string(payload) {
			break
		}
	}
	if err := sub.close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

// TestRedisReconnectDropsTheSubscriptions is not parallel: it kills the
// subscriber connections of the whole server, which would reset the bridges of
// the tests that run at the same time.
func TestRedisReconnectDropsTheSubscriptions(t *testing.T) {
	url := testRedisURL(t)
	topic := "room:" + t.Name()
	a, b := newRealNode(t, url), newRealNode(t, url)
	subB := b.hub.Subscribe(t.Context(), topic)

	a.bridge.Publish(live.Event{Topic: topic, Kind: kindFence})
	collect(t, subB)
	if subB.Dropped() {
		t.Fatal("the subscription is dropped before the connection broke")
	}

	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	admin := redis.NewClient(opt)
	defer func() {
		if err := admin.Close(); err != nil {
			t.Errorf("closing the admin client: %v", err)
		}
	}()
	if err := admin.Do(t.Context(), "CLIENT", "KILL", "TYPE", "pubsub").Err(); err != nil {
		t.Fatalf("CLIENT KILL: %v", err)
	}

	requireClosed(t, subB)
	if !subB.Dropped() {
		t.Error("the subscription closed, but not as dropped")
	}

	// The bridge delivers again once the connection is back.
	fresh := b.hub.Subscribe(t.Context(), topic)
	deadline := time.Now().Add(10 * time.Second)
	for {
		a.bridge.Publish(live.Event{Topic: topic, Kind: kindFence, PostID: 7})
		select {
		case ev := <-fresh.C():
			if ev.PostID != 7 {
				t.Fatalf("got %+v", ev)
			}
			return
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("no event after the reconnect; log:\n%s", b.logs.String())
		}
	}
}

func TestOpenRedisFailures(t *testing.T) {
	t.Parallel()
	const password = "hunter2-secret"
	tests := []struct {
		name      string
		url       func(t *testing.T) string
		wantRoute string
	}{
		{"nothing listens", func(t *testing.T) string { return deadRedisURL(t, password) }, "NewBridge->openRedis.RedisPingFailed"},
		{"the URL does not parse", func(*testing.T) string { return "redis://:" + password + "@localhost:notaport/0" }, "NewBridge->openRedis.RedisURLUnparseable"},
		{"wrong scheme", func(*testing.T) string { return "http://:" + password + "@localhost:6379" }, "NewBridge->openRedis.RedisURLUnparseable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hub := live.New()
			defer hub.Close()
			b, err := NewBridge(t.Context(), tt.url(t), hub, slog.New(slog.DiscardHandler))
			if b != nil {
				t.Fatal("a bridge came back with the error")
			}
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T: %v", err, err)
			}
			if !appErr.HasRoute(tt.wantRoute) {
				t.Errorf("unexpected failure path: %s, want %s", appErr.Route(), tt.wantRoute)
			}
			if strings.Contains(appErr.Error(), password) || strings.Contains(appErr.Message, password) ||
				strings.Contains(fmt.Sprint(appErr.Metadata), password) || strings.Contains(fmt.Sprintf("%+v", appErr), password) {
				t.Errorf("the error holds the password: %v %v", appErr.Message, appErr.Metadata)
			}
			if u, _ := appErr.Metadata["redisURL"].(string); !strings.Contains(u, "xxxxx") && u != config.RedactURL("%zz") {
				t.Errorf("redisURL metadata = %q, want the redacted URL", u)
			}
		})
	}
}

// deadClient is a client over go-redis to a port where nothing listens, made
// without the PING of openRedis.
func deadClient(t *testing.T) *redisClient {
	t.Helper()
	opt, err := redis.ParseURL(deadRedisURL(t, "x"))
	if err != nil {
		t.Fatal(err)
	}
	opt.MaxRetries = -1
	return &redisClient{rc: redis.NewClient(opt)}
}

func TestRedisClientFailureRoutes(t *testing.T) {
	t.Parallel()
	t.Run("publish", func(t *testing.T) {
		t.Parallel()
		c := deadClient(t)
		defer func() { _ = c.close() }()
		requireRoute(t, c.publish(t.Context(), EventsChannel, []byte("x")), "redisClient.publish.RedisPublishFailed")
	})
	t.Run("subscribe", func(t *testing.T) {
		t.Parallel()
		c := deadClient(t)
		defer func() { _ = c.close() }()
		_, err := c.subscribe(t.Context(), EventsChannel)
		requireRoute(t, err, "redisClient.subscribe.RedisSubscribeFailed")
	})
	t.Run("close twice", func(t *testing.T) {
		t.Parallel()
		c := deadClient(t)
		if err := c.close(); err != nil {
			t.Fatalf("first close: %v", err)
		}
		requireRoute(t, c.close(), "redisClient.close.RedisCloseFailed")
	})
	t.Run("receive and close of a closed subscription", func(t *testing.T) {
		t.Parallel()
		c := deadClient(t)
		defer func() { _ = c.close() }()
		sub := &redisSubscription{ps: c.rc.Subscribe(t.Context(), EventsChannel)}
		if err := sub.close(); err != nil {
			t.Fatalf("first close: %v", err)
		}
		_, err := sub.receive(t.Context())
		requireRoute(t, err, "redisSubscription.receive.RedisReceiveFailed")
		requireRoute(t, sub.close(), "redisSubscription.close.RedisCloseFailed")
	})
}

// requireRoute fails the test unless err is a tagged error whose route holds
// want.
func requireRoute(t *testing.T, err error, want string) {
	t.Helper()
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T: %v", err, err)
	}
	if !appErr.HasRoute(want) {
		t.Errorf("unexpected failure path: %s, want %s", appErr.Route(), want)
	}
}
