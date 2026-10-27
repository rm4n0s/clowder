package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rm4n0s/errors"
)

// fakeServer is a stand-in for Clowder: it logs in, holds streams open and
// writes the body of every post it receives to all of them.
type fakeServer struct {
	mu      sync.Mutex
	streams []chan string
	// failLogin makes the login answer 401 without a cookie; failStream makes
	// the stream answer 500; failPost makes the post answer 500; silent makes
	// the server keep the posts to itself; hangup makes the login close the
	// connection; failPage makes the room page answer 500.
	failLogin, failStream, failPost, failPage, silent, hangup bool
}

func (f *fakeServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "login") })
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		if f.hangup {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		if f.failLogin {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "token"})
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
	mux.HandleFunc("GET /rooms/{id}", func(w http.ResponseWriter, _ *http.Request) {
		if f.failPage {
			http.Error(w, "no", http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, `<div data-init="@get('/rooms/1/stream?seen=31')"></div>`)
	})
	mux.HandleFunc("GET /rooms/{id}/stream", func(w http.ResponseWriter, r *http.Request) {
		if f.failStream {
			http.Error(w, "no", http.StatusInternalServerError)
			return
		}
		if c, err := r.Cookie(sessionCookieName); err != nil || c.Value != "token" {
			http.Error(w, "no session", http.StatusUnauthorized)
			return
		}
		ch := make(chan string, 16)
		f.mu.Lock()
		f.streams = append(f.streams, ch)
		f.mu.Unlock()
		_, _ = io.WriteString(w, "id: 1\n\n")
		w.(http.Flusher).Flush()
		for {
			select {
			case text := <-ch:
				_, _ = fmt.Fprintf(w, "event: datastar-patch-elements\ndata: elements <p>%s</p>\n\n", text)
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	mux.HandleFunc("POST /rooms/{id}/posts", func(w http.ResponseWriter, r *http.Request) {
		if f.failPost {
			http.Error(w, "no", http.StatusInternalServerError)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		if !f.silent {
			f.mu.Lock()
			for _, ch := range f.streams {
				ch <- r.PostFormValue("body")
			}
			f.mu.Unlock()
		}
		http.Redirect(w, r, "/rooms/1", http.StatusSeeOther)
	})
	return mux
}

// testOptions are quick options for the fake server.
func testOptions(base string) options {
	return options{
		base: base, user: "alice", password: "pw", roomID: 1, streams: 5, rounds: 2,
		settle: 10 * time.Millisecond, ready: time.Second, timeout: 2 * time.Second,
	}
}

func TestMeasure(t *testing.T) {
	f := &fakeServer{}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	o := testOptions(srv.URL)
	o.pid = os.Getpid()
	var out bytes.Buffer
	if err := measure(t.Context(), o, &out); err != nil {
		t.Fatalf("measure: %+v", err)
	}
	for _, want := range []string{
		"streams open: 5 of room 1", "round 1: post answered in", "round 2:", "reached 5 streams",
		"fan-out to 5 streams over 2 posts: p50", "(target: max under 200ms) OK",
		"server memory:", "MiB with 5 idle streams",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestMeasureFailures(t *testing.T) {
	tests := []struct {
		name  string
		setup func(f *fakeServer, o *options)
		route string
	}{
		{"login without a cookie", func(f *fakeServer, _ *options) { f.failLogin = true }, "measure->login.PerfLoginFailed"},
		{"login whose connection is closed", func(f *fakeServer, _ *options) { f.hangup = true }, "measure->login.PerfRequestFailed"},
		{"a room page without a stream", func(f *fakeServer, _ *options) { f.failPage = true }, "measure->newestSeen.PerfPageFailed"},
		{"a stream that does not open", func(f *fakeServer, _ *options) { f.failStream = true }, "streams.read.PerfStreamFailed"},
		{"a post that is refused", func(f *fakeServer, _ *options) { f.failPost = true }, "measure->streams.probe.PerfPostFailed"},
		{"a post that no stream receives", func(f *fakeServer, o *options) {
			f.silent = true
			o.timeout = 150 * time.Millisecond
		}, "measure->streams.probe.PerfTimeout"},
		{"a server whose memory cannot be read", func(_ *fakeServer, o *options) { o.pid = 1 << 30 }, "measure->statusKiB.PerfRSSFailed"},
		{"a server that does not answer", func(_ *fakeServer, o *options) {
			o.base = "http://127.0.0.1:1"
			o.ready = 150 * time.Millisecond
		}, "measure->waitReady.PerfNotReady"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeServer{}
			srv := httptest.NewServer(f.handler())
			t.Cleanup(srv.Close)
			o := testOptions(srv.URL)
			tt.setup(f, &o)
			err := measure(t.Context(), o, io.Discard)
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T: %v", err, err)
			}
			if !appErr.HasRoute(tt.route) {
				t.Errorf("route = %s, want it to contain %s", appErr.Route(), tt.route)
			}
		})
	}
}

func TestMeasureCanceled(t *testing.T) {
	srv := httptest.NewServer((&fakeServer{}).handler())
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := measure(ctx, testOptions(srv.URL), io.Discard)
	appErr, ok := errors.FromError(err)
	if !ok || !appErr.HasRoute("measure->waitReady.PerfCanceled") {
		t.Errorf("err = %v, want PerfCanceled from waitReady", err)
	}
}

func TestRun(t *testing.T) {
	t.Run("a free port", func(t *testing.T) {
		var out bytes.Buffer
		if err := run(t.Context(), []string{"-free-port"}, &out, io.Discard); err != nil {
			t.Fatalf("run: %+v", err)
		}
		if n, err := strconv.Atoi(strings.TrimSpace(out.String())); err != nil || n < 1 || n > 65535 {
			t.Errorf("port = %q, %v", out.String(), err)
		}
	})
	tests := []struct {
		name  string
		args  []string
		route string
	}{
		{"an unknown flag", []string{"-nope"}, "run->parseOptions.PerfFlagsInvalid"},
		{"an argument", []string{"-base", "http://x", "extra"}, "run->parseOptions.PerfFlagsInvalid"},
		{"no base", nil, "run->parseOptions.PerfFlagsInvalid"},
		{"zero streams", []string{"-base", "http://x", "-streams", "0"}, "run->parseOptions.PerfFlagsInvalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := run(t.Context(), tt.args, io.Discard, io.Discard)
			appErr, ok := errors.FromError(err)
			if !ok || !appErr.HasRoute(tt.route) {
				t.Errorf("err = %v, want route %s", err, tt.route)
			}
		})
	}
	t.Run("a measurement", func(t *testing.T) {
		srv := httptest.NewServer((&fakeServer{}).handler())
		t.Cleanup(srv.Close)
		var out bytes.Buffer
		args := []string{"-base", srv.URL, "-streams", "3", "-rounds", "1", "-settle", "1ms"}
		if err := run(t.Context(), args, &out, io.Discard); err != nil {
			t.Fatalf("run: %+v", err)
		}
		if !strings.Contains(out.String(), "fan-out to 3 streams over 1 posts") {
			t.Errorf("output:\n%s", out.String())
		}
	})
}

func TestFreePortFailure(t *testing.T) {
	_, err := freePort("127.0.0.1:99999")
	appErr, ok := errors.FromError(err)
	if !ok || !appErr.HasRoute("freePort.PerfPortFailed") {
		t.Errorf("err = %v, want freePort.PerfPortFailed", err)
	}
}

func TestSummarize(t *testing.T) {
	ms := time.Millisecond
	tests := []struct {
		name string
		in   []time.Duration
		want summary
	}{
		{"nothing", nil, summary{}},
		{"one", []time.Duration{5 * ms}, summary{5 * ms, 5 * ms, 5 * ms}},
		{"ten, unsorted", []time.Duration{9 * ms, 1 * ms, 8 * ms, 2 * ms, 7 * ms, 3 * ms, 6 * ms, 4 * ms, 5 * ms, 10 * ms}, summary{5 * ms, 10 * ms, 10 * ms}},
		{"twenty", seq(20), summary{10 * ms, 19 * ms, 20 * ms}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summarize(tt.in); got != tt.want {
				t.Errorf("summarize = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// seq returns 1 ms, 2 ms, ... n ms.
func seq(n int) []time.Duration {
	out := make([]time.Duration, n)
	for i := range out {
		out[i] = time.Duration(i+1) * time.Millisecond
	}
	return out
}

func TestRoundOf(t *testing.T) {
	s := &streams{prefix: "perf-probe-x-"}
	tests := []struct {
		line   string
		want   int
		wantOK bool
	}{
		{"data: elements <p>perf-probe-x-12 measuring</p>\n", 12, true},
		{"perf-probe-x-3", 3, true},
		{"perf-probe-y-3", 0, false},
		{"perf-probe-x-", 0, false},
		{"id: 1\n", 0, false},
	}
	for _, tt := range tests {
		got, ok := s.roundOf(tt.line)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("roundOf(%q) = %d, %v; want %d, %v", tt.line, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestStatusKiB(t *testing.T) {
	for _, field := range []string{"VmRSS", "VmHWM"} {
		if n, err := statusKiB(os.Getpid(), field); err != nil || n <= 0 {
			t.Errorf("statusKiB(%s) = %d, %v", field, n, err)
		}
	}
	_, err := statusKiB(os.Getpid(), "NoSuchField")
	appErr, ok := errors.FromError(err)
	if !ok || !appErr.HasRoute("statusKiB.PerfRSSFailed") {
		t.Errorf("err = %v, want statusKiB.PerfRSSFailed", err)
	}
}

func TestParseOptionsOfTwoProcesses(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    options
		wantErr string
	}{
		{"the second process", []string{"-base", "http://a", "-base2", "http://b", "-pid", "7", "-pid2", "8", "-streams", "10"},
			options{base: "http://a", base2: "http://b", pid: 7, pid2: 8, streams: 10}, ""},
		{"one stream for two processes", []string{"-base", "http://a", "-base2", "http://b", "-streams", "1"}, options{},
			"parseOptions.PerfFlagsInvalid"},
		{"a second pid without a second process", []string{"-base", "http://a", "-pid2", "8"}, options{}, "parseOptions.PerfFlagsInvalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseOptions(tt.args, io.Discard)
			if tt.wantErr != "" {
				appErr, ok := errors.FromError(err)
				if !ok || !appErr.HasRoute(tt.wantErr) {
					t.Fatalf("err = %v, want route %s", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseOptions: %+v", err)
			}
			if got.base != tt.want.base || got.base2 != tt.want.base2 || got.pid != tt.want.pid || got.pid2 != tt.want.pid2 || got.streams != tt.want.streams {
				t.Errorf("options = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestSplitStreams(t *testing.T) {
	tests := []struct {
		name            string
		o               options
		wantLocal, want int
	}{
		{"one process", options{streams: 200}, 200, 0},
		{"two processes", options{streams: 200, base2: "x"}, 100, 100},
		{"an odd number", options{streams: 5, base2: "x"}, 3, 2},
	}
	for _, tt := range tests {
		if local, peer := splitStreams(tt.o); local != tt.wantLocal || peer != tt.want {
			t.Errorf("%s: splitStreams = %d, %d; want %d, %d", tt.name, local, peer, tt.wantLocal, tt.want)
		}
	}
}

func TestDurations(t *testing.T) {
	ms := time.Millisecond
	samples := []sample{{1 * ms, false}, {2 * ms, true}, {3 * ms, false}, {4 * ms, true}}
	tests := []struct {
		name         string
		local, other bool
		want         []time.Duration
	}{
		{"all", true, true, []time.Duration{1 * ms, 2 * ms, 3 * ms, 4 * ms}},
		{"the first process", true, false, []time.Duration{1 * ms, 3 * ms}},
		{"the second process", false, true, []time.Duration{2 * ms, 4 * ms}},
		{"none", false, false, []time.Duration{}},
	}
	for _, tt := range tests {
		got := durations(samples, tt.local, tt.other)
		if len(got) != len(tt.want) {
			t.Errorf("%s: durations = %v, want %v", tt.name, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("%s: durations = %v, want %v", tt.name, got, tt.want)
			}
		}
	}
}

// TestMeasureAcrossTwoProcesses runs the measurement with a second server.
// Both fake servers share their streams, as two processes share their events
// through Redis: a post written on the first reaches the streams opened on
// the second.
func TestMeasureAcrossTwoProcesses(t *testing.T) {
	f := &fakeServer{}
	first := httptest.NewServer(f.handler())
	second := httptest.NewServer(f.handler())
	t.Cleanup(first.Close)
	t.Cleanup(second.Close)
	o := testOptions(first.URL)
	o.base2 = second.URL
	o.streams = 6
	o.pid, o.pid2 = os.Getpid(), os.Getpid()
	var out bytes.Buffer
	if err := measure(t.Context(), o, &out); err != nil {
		t.Fatalf("measure: %+v", err)
	}
	for _, want := range []string{
		"streams open: 6 of room 1 (3 on " + first.URL + ", where the posts are written, 3 on " + second.URL + ")",
		"reached 6 streams", "fan-out to 6 streams over 2 posts",
		"streams of the process that took the post: p50", "streams of the other process, through Redis: p50",
		"server memory:", "second server memory:", "MiB with 3 idle streams",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.streams) != 6 {
		t.Errorf("%d streams were opened, want 6", len(f.streams))
	}
}

func TestMeasureSecondProcessNotReady(t *testing.T) {
	srv := httptest.NewServer((&fakeServer{}).handler())
	t.Cleanup(srv.Close)
	o := testOptions(srv.URL)
	o.base2 = "http://127.0.0.1:1"
	o.ready = 150 * time.Millisecond
	err := measure(t.Context(), o, io.Discard)
	appErr, ok := errors.FromError(err)
	if !ok || !appErr.HasRoute("measure->waitReady.PerfNotReady") {
		t.Errorf("err = %v, want measure->waitReady.PerfNotReady", err)
	}
}
