package app_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/clock"
)

// limiterCase is one behaviour of the LoginLimiter that every counter shows.
type limiterCase struct {
	name string
	// memoryOnly is true for a case that looks into the memory of the
	// in-memory counter (Len).
	memoryOnly bool
	run        func(t *testing.T, l *app.LoginLimiter, clk *clock.Fake)
}

// limiterCases is the table of the behaviour of LoginLimiter (SPEC §7.1). It
// runs on the in-memory counter and on the counter in Redis.
func limiterCases() []limiterCase {
	const window = 15 * time.Minute
	fail := func(l *app.LoginLimiter, n int, user, ip string) {
		for range n {
			l.Fail(context.Background(), user, ip)
		}
	}
	return []limiterCase{
		{name: "nothing failed yet", run: func(t *testing.T, l *app.LoginLimiter, _ *clock.Fake) {
			ctx := t.Context()
			if !l.Allow(ctx, "ana", "10.0.0.1") {
				t.Error("Allow = false before any failure")
			}
		}},
		{name: "four failures still pass", run: func(t *testing.T, l *app.LoginLimiter, _ *clock.Fake) {
			ctx := t.Context()
			fail(l, 4, "ana", "10.0.0.1")
			if !l.Allow(ctx, "ana", "10.0.0.1") {
				t.Error("Allow = false after 4 failures")
			}
		}},
		{name: "five failures of one username block it from any address", run: func(t *testing.T, l *app.LoginLimiter, _ *clock.Fake) {
			ctx := t.Context()
			for i := range 5 {
				l.Fail(ctx, "ana", fmt.Sprintf("10.0.0.%d", i))
			}
			if l.Allow(ctx, "ana", "10.9.9.9") {
				t.Error("Allow = true for the username after 5 failures")
			}
			if !l.Allow(ctx, "bob", "10.9.9.9") {
				t.Error("Allow = false for another username and a fresh address")
			}
		}},
		{name: "five failures of one address block it for any username", run: func(t *testing.T, l *app.LoginLimiter, _ *clock.Fake) {
			ctx := t.Context()
			for i := range 5 {
				l.Fail(ctx, fmt.Sprintf("user%d", i), "10.0.0.1")
			}
			if l.Allow(ctx, "fresh", "10.0.0.1") {
				t.Error("Allow = true for the address after 5 failures")
			}
			if !l.Allow(ctx, "fresh", "10.0.0.2") {
				t.Error("Allow = false for another address")
			}
		}},
		{name: "the window passes", run: func(t *testing.T, l *app.LoginLimiter, clk *clock.Fake) {
			ctx := t.Context()
			fail(l, 5, "ana", "10.0.0.1")
			clk.Advance(window - time.Second)
			if l.Allow(ctx, "ana", "10.0.0.1") {
				t.Error("Allow = true one second before the window ends")
			}
			clk.Advance(time.Second)
			if !l.Allow(ctx, "ana", "10.0.0.1") {
				t.Error("Allow = false when the window has passed")
			}
		}},
		{name: "the window slides", run: func(t *testing.T, l *app.LoginLimiter, clk *clock.Fake) {
			ctx := t.Context()
			fail(l, 3, "ana", "")
			clk.Advance(10 * time.Minute)
			fail(l, 2, "ana", "")
			if l.Allow(ctx, "ana", "") {
				t.Fatal("Allow = true with 5 failures in the window")
			}
			clk.Advance(5 * time.Minute) // the first three leave the window
			if !l.Allow(ctx, "ana", "") {
				t.Error("Allow = false after the oldest failures left the window")
			}
			fail(l, 3, "ana", "")
			if l.Allow(ctx, "ana", "") {
				t.Error("Allow = true with 5 failures in the window again")
			}
		}},
		{name: "a success resets the username only", run: func(t *testing.T, l *app.LoginLimiter, _ *clock.Fake) {
			ctx := t.Context()
			fail(l, 4, "ana", "10.0.0.1")
			l.Reset(ctx, "ana")
			fail(l, 1, "ana", "10.0.0.2")
			if !l.Allow(ctx, "ana", "10.0.0.3") {
				t.Error("Allow = false: the reset should have cleared the 4 failures")
			}
			// The address kept its 4 failures; one more blocks it.
			l.Fail(ctx, "zed", "10.0.0.1")
			if l.Allow(ctx, "other", "10.0.0.1") {
				t.Error("Allow = true: the reset must not clear the address")
			}
		}},
		{name: "an empty address is not counted", run: func(t *testing.T, l *app.LoginLimiter, _ *clock.Fake) {
			ctx := t.Context()
			for i := range 8 {
				l.Fail(ctx, fmt.Sprintf("user%d", i), "")
			}
			if !l.Allow(ctx, "fresh", "") {
				t.Error("Allow = false: failures without an address must not block the empty address")
			}
		}},
		{name: "a very long username is cut to one key", run: func(t *testing.T, l *app.LoginLimiter, _ *clock.Fake) {
			ctx := t.Context()
			long := strings.Repeat("a", 5000)
			for i := range 5 {
				l.Fail(ctx, long+fmt.Sprint(i), "")
			}
			if l.Allow(ctx, long, "") {
				t.Error("Allow = true: the long usernames share their first 128 bytes")
			}
		}},
		{name: "a very long username is one key in memory", memoryOnly: true, run: func(t *testing.T, l *app.LoginLimiter, _ *clock.Fake) {
			ctx := t.Context()
			long := strings.Repeat("a", 5000)
			for i := range 5 {
				l.Fail(ctx, long+fmt.Sprint(i), "")
			}
			if n := l.Len(); n != 1 {
				t.Errorf("the limiter holds %d keys, want 1", n)
			}
		}},
		{name: "memory is bounded", memoryOnly: true, run: func(t *testing.T, l *app.LoginLimiter, clk *clock.Fake) {
			ctx := t.Context()
			for i := range 1000 {
				l.Fail(ctx, fmt.Sprintf("user%d", i), fmt.Sprintf("10.1.%d.%d", i/250, i%250))
			}
			if n := l.Len(); n != 2000 {
				t.Fatalf("the limiter holds %d keys, want 2000", n)
			}
			clk.Advance(window + time.Minute)
			if !l.Allow(ctx, "anyone", "10.0.0.1") {
				t.Error("Allow = false long after the failures")
			}
			if n := l.Len(); n != 0 {
				t.Errorf("the limiter holds %d keys after the window, want 0", n)
			}
		}},
		{name: "keys with failures in the window survive a sweep", memoryOnly: true, run: func(t *testing.T, l *app.LoginLimiter, clk *clock.Fake) {
			ctx := t.Context()
			l.Fail(ctx, "old", "10.0.0.1")
			clk.Advance(14 * time.Minute)
			l.Fail(ctx, "new", "10.0.0.2")
			clk.Advance(2 * time.Minute) // "old" is out, "new" is in
			l.Allow(ctx, "x", "10.0.0.3")
			if n := l.Len(); n != 2 {
				t.Errorf("the limiter holds %d keys, want 2 (the new username and address)", n)
			}
		}},
	}
}

func TestLoginLimiter(t *testing.T) {
	t.Parallel()
	for _, tt := range limiterCases() {
		t.Run(tt.name, func(t *testing.T) {
			clk := clock.NewFake(start)
			tt.run(t, app.NewLoginLimiter(clk), clk)
		})
	}
}

func TestLoginLimiterNilClockIsReal(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	l := app.NewLoginLimiter(nil)
	for range 5 {
		l.Fail(ctx, "ana", "10.0.0.1")
	}
	if l.Allow(ctx, "ana", "10.0.0.1") {
		t.Error("Allow = true after 5 failures")
	}
}

func TestLoginLimiterConcurrent(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	l := app.NewLoginLimiter(clock.NewFake(start))
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 200 {
				user := fmt.Sprintf("user%d", (g+i)%10)
				l.Allow(ctx, user, "10.0.0.1")
				l.Fail(ctx, user, "10.0.0.1")
				if i%50 == 0 {
					l.Reset(ctx, user)
				}
			}
		}()
	}
	wg.Wait()
	if l.Allow(ctx, "user1", "10.0.0.1") {
		t.Error("the address failed hundreds of times and is still allowed")
	}
}
