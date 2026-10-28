package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/clowder/internal/store/storetest"
	"github.com/rm4n0s/errors"
)

func TestNewProcessID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		reader io.Reader
		want   string
		route  string
	}{
		{name: "random bytes", reader: bytes.NewReader([]byte{0, 1, 2, 3, 0xaa, 0xbb, 0xcc, 0xff}), want: "00010203aabbccff"},
		{name: "a reader that has too little", reader: bytes.NewReader([]byte{1, 2}), route: "newProcessID.ProcessIDFailed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := newProcessID(tt.reader)
			if tt.route == "" {
				if err != nil || got != tt.want {
					t.Fatalf("newProcessID = %q, %v; want %q", got, err, tt.want)
				}
				return
			}
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T", err)
			}
			if appErr.Tag != tagProcessIDFailed || !appErr.HasRoute(tt.route) {
				t.Errorf("tag %q, route %s", appErr.Tag, appErr.Route())
			}
		})
	}
}

// fakeExpirer counts the calls of Expire.
type fakeExpirer struct {
	mu    sync.Mutex
	calls int
	// results are returned in order; the last one repeats.
	results []expireResult
}

type expireResult struct {
	n   int
	err error
}

func (f *fakeExpirer) Expire(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := min(f.calls, len(f.results)-1)
	f.calls++
	return f.results[i].n, f.results[i].err
}

func (f *fakeExpirer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestStartCallExpirer(t *testing.T) {
	t.Parallel()
	buf := &syncBuffer{}
	logger := slog.New(slog.NewJSONHandler(buf, nil))
	ex := &fakeExpirer{results: []expireResult{
		{err: errors.New("TestFailure", "the store is gone")},
		{n: 2},
		{n: 0},
	}}
	stop := startCallExpirer(t.Context(), ex, 5*time.Millisecond, logger)
	deadline := time.Now().Add(10 * time.Second)
	for ex.count() < 4 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	stop() // stopping twice is fine
	after := ex.count()
	time.Sleep(30 * time.Millisecond)
	if ex.count() != after {
		t.Errorf("Expire ran after the stop: %d then %d", after, ex.count())
	}
	if after < 4 {
		t.Fatalf("Expire ran %d times", after)
	}
	log := buf.String()
	for _, want := range []string{"ending the calls that rang too long failed", "calls were not answered"} {
		if !strings.Contains(log, want) {
			t.Errorf("the log has no %q:\n%s", want, log)
		}
	}
}

func TestStartCallExpirerStopsWithItsContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	ex := &fakeExpirer{results: []expireResult{{}}}
	stop := startCallExpirer(ctx, ex, time.Millisecond, slog.New(slog.DiscardHandler))
	cancel()
	stop() // returns: the goroutine ended with the context
}

// fakeStopper records the home that EndOpen was asked for.
type fakeStopper struct {
	homes []string
	n     int
	err   error
}

func (f *fakeStopper) EndOpen(_ context.Context, home string) (int, error) {
	f.homes = append(f.homes, home)
	return f.n, f.err
}

func TestEndCallsAtStartAndStop(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		redisURL  string
		stopper   *fakeStopper
		wantHomes []string
		wantErr   bool
		wantLog   string
	}{
		{name: "without Redis every open call is ended", stopper: &fakeStopper{n: 3}, wantHomes: []string{""}, wantLog: "calls of an earlier run were ended"},
		{name: "without Redis and no call", stopper: &fakeStopper{}, wantHomes: []string{""}},
		{name: "with Redis none is touched", redisURL: "redis://localhost:6379/0", stopper: &fakeStopper{n: 3}},
		{name: "a failure is returned", stopper: &fakeStopper{err: errors.New("TestFailure", "no")}, wantHomes: []string{""}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			buf := &syncBuffer{}
			logger := slog.New(slog.NewTextHandler(buf, nil))
			err := endCallsAtStart(t.Context(), tt.stopper, config.Config{RedisURL: tt.redisURL}, logger)
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, want error %v", err, tt.wantErr)
			}
			if strings.Join(tt.stopper.homes, ",") != strings.Join(tt.wantHomes, ",") {
				t.Errorf("homes = %q, want %q", tt.stopper.homes, tt.wantHomes)
			}
			if tt.wantLog != "" && !strings.Contains(buf.String(), tt.wantLog) {
				t.Errorf("log %q has no %q", buf.String(), tt.wantLog)
			}
		})
	}

	t.Run("at stop the calls of this process end, with a context that outlives the one of serve", func(t *testing.T) {
		t.Parallel()
		done, cancel := context.WithCancel(t.Context())
		cancel()
		stopper := &fakeStopper{n: 2}
		buf := &syncBuffer{}
		endCallsAtStop(done, stopper, "0123456789abcdef", slog.New(slog.NewTextHandler(buf, nil)))
		if len(stopper.homes) != 1 || stopper.homes[0] != "0123456789abcdef" {
			t.Errorf("homes = %q", stopper.homes)
		}
		if !strings.Contains(buf.String(), "calls were ended because the server stops") {
			t.Errorf("log = %q", buf.String())
		}
		failing := &fakeStopper{err: errors.New("TestFailure", "no")}
		buf = &syncBuffer{}
		endCallsAtStop(done, failing, "x", slog.New(slog.NewTextHandler(buf, nil)))
		if !strings.Contains(buf.String(), "ending the calls of this process failed") {
			t.Errorf("log = %q", buf.String())
		}
	})
}

func TestStartMedia(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.DiscardHandler)

	t.Run("without an address calls are off", func(t *testing.T) {
		t.Parallel()
		media, stop, err := startMedia(t.Context(), config.Config{}, nil, nil, logger)
		if err != nil || media != nil || stop == nil {
			t.Fatalf("startMedia = %v, %v, %v", media, stop != nil, err)
		}
		stop()
	})

	t.Run("with an address it listens until stopped", func(t *testing.T) {
		t.Parallel()
		hub := live.New()
		t.Cleanup(hub.Close)
		svc := app.NewCalls(storetest.Open(t, clock.Real()), clock.Real(), app.CallsConfig{ProcessID: "0123456789abcdef", Enabled: true}, hub)
		media, stop, err := startMedia(t.Context(), config.Config{MediaAddr: "127.0.0.1:0"}, svc, hub, logger)
		if err != nil || media == nil {
			t.Fatalf("startMedia = %v, %v", media, err)
		}
		_, tcp := media.Addr()
		conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(t.Context(), "tcp", tcp.String())
		if err != nil {
			t.Fatalf("the media port does not accept: %v", err)
		}
		_ = conn.Close()
		stop()
	})

	t.Run("a port in use stops the start", func(t *testing.T) {
		t.Parallel()
		ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		hub := live.New()
		t.Cleanup(hub.Close)
		_, stop, err := startMedia(t.Context(), config.Config{MediaAddr: ln.Addr().String()}, nil, hub, logger)
		if err == nil {
			stop()
			t.Fatal("startMedia succeeded on a port in use")
		}
		appErr, ok := errors.FromError(err)
		if !ok || !appErr.HasRoute("startMedia") {
			t.Errorf("error = %v", err)
		}
	})
}

func TestCallsControllerAsksTheService(t *testing.T) {
	t.Parallel()
	st := storetest.Open(t, clock.Real())
	ctl := callsController{svc: app.NewCalls(st, clock.Real(), app.CallsConfig{ProcessID: "0123456789abcdef", Enabled: true}, nil)}
	// Nothing is stored: a call that is not there is not open, and the
	// calls of the controller fail as the service does.
	if got, err := ctl.Status(t.Context(), 1); err != nil || got.Open || got.Active || got.Reason != "" {
		t.Errorf("Status = %+v, %v", got, err)
	}
	tests := []struct {
		name string
		run  func() error
		tag  string
	}{
		{"Fail", func() error { return ctl.Fail(t.Context(), 1) }, store.TagNotFound},
		{"SetMedia", func() error { return ctl.SetMedia(t.Context(), 1, 1, true, true) }, app.TagUnauthenticated},
		{"Leave", func() error { return ctl.Leave(t.Context(), 1, 1) }, app.TagUnauthenticated},
	}
	for _, tt := range tests {
		err := tt.run()
		appErr, ok := errors.FromError(err)
		if !ok || appErr.Tag != tt.tag {
			t.Errorf("%s = %v, want the tag %s", tt.name, err, tt.tag)
		}
	}
}
