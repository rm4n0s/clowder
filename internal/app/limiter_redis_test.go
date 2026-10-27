package app_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/cluster"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/clowder/internal/store/storetest"
	"github.com/rm4n0s/errors"
)

// redisTestEnv names the environment variable with the redis:// URL of a
// server for the tests that need one (SPEC D39); scripts/test-docker.sh sets
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

// openRedisCounter opens the counter of internal/cluster on the Redis of the
// tests, with every key under a prefix that only this test uses (the server is
// shared with the other tests and packages, and the keys outlive the test for
// the window).
func openRedisCounter(t *testing.T, url, prefix string) app.FailureCounter {
	t.Helper()
	c, err := cluster.NewLoginCounter(t.Context(), url)
	if err != nil {
		t.Fatalf("NewLoginCounter: %+v", err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("close the counter: %v", err)
		}
	})
	return prefixCounter{FailureCounter: c, prefix: prefix}
}

// prefixCounter puts a prefix before every key of the counter it wraps.
type prefixCounter struct {
	app.FailureCounter
	prefix string
}

func (p prefixCounter) Count(ctx context.Context, ns, key string, now time.Time, window time.Duration) (int, error) {
	return p.FailureCounter.Count(ctx, ns, p.prefix+key, now, window)
}

func (p prefixCounter) Add(ctx context.Context, ns, key string, now time.Time, window time.Duration, keep int) error {
	return p.FailureCounter.Add(ctx, ns, p.prefix+key, now, window, keep)
}

func (p prefixCounter) Clear(ctx context.Context, ns, key string) error {
	return p.FailureCounter.Clear(ctx, ns, p.prefix+key)
}

// uniquePrefix returns a prefix no other test run uses.
func uniquePrefix() string {
	return rand.Text() + ":"
}

// TestLoginLimiterRedis runs the table of the behaviour of the limiter on the
// counter in Redis, with the fake clock.
func TestLoginLimiterRedis(t *testing.T) {
	t.Parallel()
	url := testRedisURL(t)
	for _, tt := range limiterCases() {
		if tt.memoryOnly {
			continue
		}
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewFake(start)
			counter := openRedisCounter(t, url, uniquePrefix())
			tt.run(t, app.NewLoginLimiterOn(clk, counter, nil), clk)
		})
	}
}

// TestLoginLimiterSharesRedis has two limiters, as two processes have, on
// the same Redis: their failures add up, and a reset by one clears the other.
func TestLoginLimiterSharesRedis(t *testing.T) {
	t.Parallel()
	url := testRedisURL(t)
	prefix := uniquePrefix()
	clk := clock.NewFake(start)
	a := app.NewLoginLimiterOn(clk, openRedisCounter(t, url, prefix), nil)
	b := app.NewLoginLimiterOn(clk, openRedisCounter(t, url, prefix), nil)
	ctx := t.Context()

	for range 3 {
		a.Fail(ctx, "ana", "10.0.0.1")
	}
	for range 2 {
		b.Fail(ctx, "ana", "10.0.0.1")
	}
	if a.Allow(ctx, "ana", "10.9.9.9") || b.Allow(ctx, "ana", "10.9.9.9") {
		t.Error("5 failures spread over two limiters do not block the username on both")
	}
	if a.Allow(ctx, "zed", "10.0.0.1") || b.Allow(ctx, "zed", "10.0.0.1") {
		t.Error("5 failures of one address spread over two limiters do not block the address on both")
	}
	b.Reset(ctx, "ana")
	if !a.Allow(ctx, "ana", "10.9.9.9") {
		t.Error("the reset on limiter b did not clear the username on limiter a")
	}
	// Two failures in the same instant on two limiters both count.
	other := app.NewLoginLimiterOn(clk, openRedisCounter(t, url, prefix), nil)
	for range 3 {
		a.Fail(ctx, "bob", "")
		other.Fail(ctx, "bob", "")
	}
	if a.Allow(ctx, "bob", "") {
		t.Error("6 failures of the same instant on two limiters did not block")
	}
}

// failingCounter is a FailureCounter whose every call fails.
type failingCounter struct{}

func (failingCounter) Count(context.Context, string, string, time.Time, time.Duration) (int, error) {
	return 0, errors.New("CounterDown", "the counter is down")
}

func (failingCounter) Add(context.Context, string, string, time.Time, time.Duration, int) error {
	return errors.New("CounterDown", "the counter is down")
}

func (failingCounter) Clear(context.Context, string, string) error {
	return errors.New("CounterDown", "the counter is down")
}

// TestLoginLimiterFailsOpen has a counter that fails: the limiter allows the
// attempt and logs a warning with the tagged error (SPEC §8.1), and a login
// goes on to check the password.
func TestLoginLimiterFailsOpen(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	clk := clock.NewFake(start)
	limiter := app.NewLoginLimiterOn(clk, failingCounter{}, logger)
	ctx := t.Context()

	for range 10 {
		limiter.Fail(ctx, "ana", "10.0.0.1")
	}
	if !limiter.Allow(ctx, "ana", "10.0.0.1") {
		t.Error("Allow = false: a failing counter must fail open")
	}
	limiter.Reset(ctx, "ana")
	for _, want := range []string{"counting the failed logins failed", "recording a failed login failed", "clearing the failed logins failed", `"Tag":"CounterDown"`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the log does not contain %q:\n%s", want, logs.String())
		}
	}

	st := storetest.Open(t, clk)
	svc := app.NewAccounts(st, clk, auth.FastHasher(), app.AccountsConfig{
		InvitationLifetime: lifetime, SessionLifetime: sessionLifetime, Limiter: limiter,
	})
	inv, err := svc.CreateSystemInvitation(ctx, store.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SignUp(ctx, inv.Token, "alice", "Alice", goodPassword); err != nil {
		t.Fatal(err)
	}
	for range 7 {
		_, _, err := svc.Login(ctx, "alice", "a wrong password, long enough", loginIP)
		_ = requireTag(t, err, app.TagInvalidInput, "Accounts.Login.InvalidInput")
	}
	if _, _, err := svc.Login(ctx, "alice", goodPassword, loginIP); err != nil {
		t.Errorf("the right password after 7 failures was refused although the counter is down: %v", err)
	}
}
