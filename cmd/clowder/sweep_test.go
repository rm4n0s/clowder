package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/errors"
)

// fakeSweeper records its calls and answers from the function it is given.
type fakeSweeper struct {
	mu     sync.Mutex
	calls  chan time.Duration
	answer func(n int) (int, error)
	n      int
}

func newFakeSweeper(answer func(n int) (int, error)) *fakeSweeper {
	return &fakeSweeper{calls: make(chan time.Duration, 100), answer: answer}
}

func (f *fakeSweeper) SweepStaged(ctx context.Context, maxAge time.Duration) (int, error) {
	f.mu.Lock()
	f.n++
	n := f.n
	f.mu.Unlock()
	f.calls <- maxAge
	return f.answer(n)
}

func (f *fakeSweeper) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

// syncLog is a buffer for a logger used by the goroutine of the sweeper.
type syncLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *syncLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *syncLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func waitFor(t *testing.T, ch <-chan time.Duration) time.Duration {
	t.Helper()
	select {
	case d := <-ch:
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("the sweeper did not sweep")
		return 0
	}
}

func TestStagedLifetimeIsADay(t *testing.T) {
	t.Parallel()
	if app.StagedLifetime != 24*time.Hour {
		t.Errorf("StagedLifetime = %v, want 24h (SPEC §7.12)", app.StagedLifetime)
	}
}

func TestStagedSweeperSweepsAtStartAndEveryInterval(t *testing.T) {
	t.Parallel()
	sw := newFakeSweeper(func(int) (int, error) { return 0, nil })
	stop := startStagedSweeper(t.Context(), sw, 20*time.Millisecond, slog.New(slog.DiscardHandler))
	defer stop()
	for range 3 {
		if got := waitFor(t, sw.calls); got != 24*time.Hour {
			t.Errorf("maxAge = %v, want 24h", got)
		}
	}
}

func TestStagedSweeperStopsAndIsWaitedFor(t *testing.T) {
	t.Parallel()
	sw := newFakeSweeper(func(int) (int, error) { return 0, nil })
	stop := startStagedSweeper(t.Context(), sw, 5*time.Millisecond, slog.New(slog.DiscardHandler))
	waitFor(t, sw.calls)
	stop()
	after := sw.count()
	time.Sleep(60 * time.Millisecond)
	if got := sw.count(); got != after {
		t.Errorf("the sweeper swept %d times after it was stopped", got-after)
	}
	stop() // twice is fine
}

func TestStagedSweeperEndsWithItsContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	// A sweep that is running when the context ends returns its error at once.
	sw := &blockingSweeper{started: make(chan struct{})}
	stop := startStagedSweeper(ctx, sw, time.Hour, slog.New(slog.DiscardHandler))
	<-sw.started
	cancel()
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sweeper did not end with its context")
	}
}

// blockingSweeper sweeps until its context ends.
type blockingSweeper struct{ started chan struct{} }

func (b *blockingSweeper) SweepStaged(ctx context.Context, _ time.Duration) (int, error) {
	close(b.started)
	<-ctx.Done()
	return 0, errors.NewErr(app.TagSweepCanceled, ctx.Err())
}

func TestStagedSweeperLogsAndGoesOn(t *testing.T) {
	t.Parallel()
	sw := newFakeSweeper(func(n int) (int, error) {
		switch n {
		case 1:
			return 0, errors.New("SweepBroke", "the disk failed")
		case 2:
			return 3, nil
		}
		return 0, nil
	})
	out := &syncLog{}
	stop := startStagedSweeper(t.Context(), sw, 5*time.Millisecond, slog.New(slog.NewJSONHandler(out, nil)))
	for range 3 {
		waitFor(t, sw.calls)
	}
	stop()
	for _, want := range []string{"sweeping the staged uploads failed", "SweepBroke", "removed staged uploads that no post took", `"count":3`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the log does not hold %q:\n%s", want, out.String())
		}
	}
}
