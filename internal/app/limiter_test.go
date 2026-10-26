package app_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/clock"
)

func TestLoginLimiter(t *testing.T) {
	const window = 15 * time.Minute
	fail := func(l *app.LoginLimiter, n int, user, ip string) {
		for range n {
			l.Fail(user, ip)
		}
	}
	tests := []struct {
		name string
		run  func(t *testing.T, l *app.LoginLimiter, clk *clock.Fake)
	}{
		{"nothing failed yet", func(t *testing.T, l *app.LoginLimiter, _ *clock.Fake) {
			if !l.Allow("ana", "10.0.0.1") {
				t.Error("Allow = false before any failure")
			}
		}},
		{"four failures still pass", func(t *testing.T, l *app.LoginLimiter, _ *clock.Fake) {
			fail(l, 4, "ana", "10.0.0.1")
			if !l.Allow("ana", "10.0.0.1") {
				t.Error("Allow = false after 4 failures")
			}
		}},
		{"five failures of one username block it from any address", func(t *testing.T, l *app.LoginLimiter, _ *clock.Fake) {
			for i := range 5 {
				l.Fail("ana", fmt.Sprintf("10.0.0.%d", i))
			}
			if l.Allow("ana", "10.9.9.9") {
				t.Error("Allow = true for the username after 5 failures")
			}
			if !l.Allow("bob", "10.9.9.9") {
				t.Error("Allow = false for another username and a fresh address")
			}
		}},
		{"five failures of one address block it for any username", func(t *testing.T, l *app.LoginLimiter, _ *clock.Fake) {
			for i := range 5 {
				l.Fail(fmt.Sprintf("user%d", i), "10.0.0.1")
			}
			if l.Allow("fresh", "10.0.0.1") {
				t.Error("Allow = true for the address after 5 failures")
			}
			if !l.Allow("fresh", "10.0.0.2") {
				t.Error("Allow = false for another address")
			}
		}},
		{"the window passes", func(t *testing.T, l *app.LoginLimiter, clk *clock.Fake) {
			fail(l, 5, "ana", "10.0.0.1")
			clk.Advance(window - time.Second)
			if l.Allow("ana", "10.0.0.1") {
				t.Error("Allow = true one second before the window ends")
			}
			clk.Advance(time.Second)
			if !l.Allow("ana", "10.0.0.1") {
				t.Error("Allow = false when the window has passed")
			}
		}},
		{"the window slides", func(t *testing.T, l *app.LoginLimiter, clk *clock.Fake) {
			fail(l, 3, "ana", "")
			clk.Advance(10 * time.Minute)
			fail(l, 2, "ana", "")
			if l.Allow("ana", "") {
				t.Fatal("Allow = true with 5 failures in the window")
			}
			clk.Advance(5 * time.Minute) // the first three leave the window
			if !l.Allow("ana", "") {
				t.Error("Allow = false after the oldest failures left the window")
			}
			fail(l, 3, "ana", "")
			if l.Allow("ana", "") {
				t.Error("Allow = true with 5 failures in the window again")
			}
		}},
		{"a success resets the username only", func(t *testing.T, l *app.LoginLimiter, _ *clock.Fake) {
			fail(l, 4, "ana", "10.0.0.1")
			l.Reset("ana")
			fail(l, 1, "ana", "10.0.0.2")
			if !l.Allow("ana", "10.0.0.3") {
				t.Error("Allow = false: the reset should have cleared the 4 failures")
			}
			// The address kept its 4 failures; one more blocks it.
			l.Fail("zed", "10.0.0.1")
			if l.Allow("other", "10.0.0.1") {
				t.Error("Allow = true: the reset must not clear the address")
			}
		}},
		{"an empty address is not counted", func(t *testing.T, l *app.LoginLimiter, _ *clock.Fake) {
			for i := range 8 {
				l.Fail(fmt.Sprintf("user%d", i), "")
			}
			if !l.Allow("fresh", "") {
				t.Error("Allow = false: failures without an address must not block the empty address")
			}
		}},
		{"a very long username is cut to one key", func(t *testing.T, l *app.LoginLimiter, _ *clock.Fake) {
			long := strings.Repeat("a", 5000)
			for i := range 5 {
				l.Fail(long+fmt.Sprint(i), "")
			}
			if l.Allow(long, "") {
				t.Error("Allow = true: the long usernames share their first 128 bytes")
			}
			if n := l.Len(); n != 1 {
				t.Errorf("the limiter holds %d keys, want 1", n)
			}
		}},
		{"memory is bounded", func(t *testing.T, l *app.LoginLimiter, clk *clock.Fake) {
			for i := range 1000 {
				l.Fail(fmt.Sprintf("user%d", i), fmt.Sprintf("10.1.%d.%d", i/250, i%250))
			}
			if n := l.Len(); n != 2000 {
				t.Fatalf("the limiter holds %d keys, want 2000", n)
			}
			clk.Advance(window + time.Minute)
			if !l.Allow("anyone", "10.0.0.1") {
				t.Error("Allow = false long after the failures")
			}
			if n := l.Len(); n != 0 {
				t.Errorf("the limiter holds %d keys after the window, want 0", n)
			}
		}},
		{"keys with failures in the window survive a sweep", func(t *testing.T, l *app.LoginLimiter, clk *clock.Fake) {
			l.Fail("old", "10.0.0.1")
			clk.Advance(14 * time.Minute)
			l.Fail("new", "10.0.0.2")
			clk.Advance(2 * time.Minute) // "old" is out, "new" is in
			l.Allow("x", "10.0.0.3")
			if n := l.Len(); n != 2 {
				t.Errorf("the limiter holds %d keys, want 2 (the new username and address)", n)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clk := clock.NewFake(start)
			tt.run(t, app.NewLoginLimiter(clk), clk)
		})
	}
}

func TestLoginLimiterNilClockIsReal(t *testing.T) {
	l := app.NewLoginLimiter(nil)
	for range 5 {
		l.Fail("ana", "10.0.0.1")
	}
	if l.Allow("ana", "10.0.0.1") {
		t.Error("Allow = true after 5 failures")
	}
}

func TestLoginLimiterConcurrent(t *testing.T) {
	l := app.NewLoginLimiter(clock.NewFake(start))
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 200 {
				user := fmt.Sprintf("user%d", (g+i)%10)
				l.Allow(user, "10.0.0.1")
				l.Fail(user, "10.0.0.1")
				if i%50 == 0 {
					l.Reset(user)
				}
			}
		}()
	}
	wg.Wait()
	if l.Allow("user1", "10.0.0.1") {
		t.Error("the address failed hundreds of times and is still allowed")
	}
}
