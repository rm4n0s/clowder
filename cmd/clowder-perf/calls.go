package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rm4n0s/clowder/internal/calls/callstest"
	"github.com/rm4n0s/errors"
)

// Defaults of the measurement of calls.
const (
	defaultCallDuration = 60 * time.Second
	defaultVideoBitrate = 1_000_000
	// callsConnectTimeout is how long every client has to connect.
	callsConnectTimeout = 60 * time.Second
	// rssSampleEvery is how often the memory of the server is read.
	rssSampleEvery = time.Second
	// clockTicks is the unit of the CPU times of /proc/<pid>/stat (USER_HZ is
	// 100 on Linux).
	clockTicks = 100
)

// perfTB is the callstest.TB of a program that is not a test: it logs to
// stderr and a Fatal ends the measurement through a panic that measureCalls
// turns into an error.
type perfTB struct {
	ctx      context.Context
	stderr   io.Writer
	cleanups []func()
}

// perfFatal is what perfTB panics with.
type perfFatal struct{ msg string }

func (*perfTB) Helper() {}

func (t *perfTB) Logf(format string, args ...any) {
	_, _ = fmt.Fprintf(t.stderr, format+"\n", args...)
}

func (t *perfTB) Errorf(format string, args ...any) {
	_, _ = fmt.Fprintf(t.stderr, "error: "+format+"\n", args...)
}

func (t *perfTB) Fatal(args ...any) { panic(perfFatal{fmt.Sprint(args...)}) }

func (t *perfTB) Fatalf(format string, args ...any) { panic(perfFatal{fmt.Sprintf(format, args...)}) }

func (t *perfTB) Cleanup(f func()) { t.cleanups = append(t.cleanups, f) }

func (t *perfTB) Context() context.Context { return t.ctx }

// close runs the cleanups, last first.
func (t *perfTB) close() {
	for i := len(t.cleanups) - 1; i >= 0; i-- {
		t.cleanups[i]()
	}
	t.cleanups = nil
}

// apiUser is a user of the large demo with a bearer token.
type apiUser struct {
	id       uint64
	username string
	token    string
}

// api sends a JSON request of the API as the user and decodes the answer into
// out (nil: the answer is dropped). It fails with PerfRequestFailed when the
// status is not one of ok.
func api(ctx context.Context, client *http.Client, base, token, method, path string, body, out any, ok ...int) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return errors.NewErr(tagPerfRequestFailed, err).SetMetadata("path", path)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+"/api/v1"+path, rd)
	if err != nil {
		return errors.NewErr(tagPerfRequestFailed, err).SetMetadata("path", path)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.NewErr(tagPerfRequestFailed, err).SetMetadata("path", path)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return errors.NewErr(tagPerfRequestFailed, err).SetMetadata("path", path)
	}
	for _, code := range ok {
		if resp.StatusCode == code {
			if out != nil {
				if err := json.Unmarshal(data, out); err != nil {
					return errors.NewErr(tagPerfRequestFailed, err).SetMetadata("path", path)
				}
			}
			return nil
		}
	}
	return errors.New(tagPerfRequestFailed, "the API answered with another status",
		"path", path, "status", resp.StatusCode, "body", string(data[:min(len(data), 200)]))
}

// apiLogin logs the user in through the API and returns the token.
func apiLogin(ctx context.Context, client *http.Client, o options, username string) (apiUser, error) {
	var out struct {
		Token string `json:"token"`
		User  struct {
			ID uint64 `json:"id"`
		} `json:"user"`
	}
	err := api(ctx, client, o.base, "", http.MethodPost, "/auth/login", map[string]any{
		"username": username, "password": o.password, "client_name": "clowder-perf",
	}, &out, http.StatusOK)
	return apiUser{id: out.User.ID, username: username, token: out.Token}, err
}

// demoNames returns the usernames of want users of the large demo besides
// admin and the user of the options, found through the search of the API with
// the token (the start of a username, letter by letter).
func demoNames(ctx context.Context, client *http.Client, o options, token string, want int) ([]string, error) {
	seen := map[string]bool{"admin": true, o.user: true}
	var names []string
	for _, letter := range "abcdefghijklmnopqrstuvwxyz" {
		var list struct {
			Users []struct {
				Username string `json:"username"`
			} `json:"users"`
		}
		if err := api(ctx, client, o.base, token, http.MethodGet, "/users?q="+string(letter), nil, &list, http.StatusOK); err != nil {
			return nil, err
		}
		for _, u := range list.Users {
			if !seen[u.Username] && len(names) < want {
				seen[u.Username] = true
				names = append(names, u.Username)
			}
		}
		if len(names) == want {
			return names, nil
		}
	}
	return nil, errors.New(tagPerfFlagsInvalid, "the server has too few users", "wanted", want, "found", len(names))
}

// demoUsers logs in 2*pairs users of the large demo through the API and
// returns them in pairs.
func demoUsers(ctx context.Context, client *http.Client, o options, pairs int) ([]apiUser, error) {
	first, err := apiLogin(ctx, client, o, o.user)
	if err != nil {
		return nil, err
	}
	names, err := demoNames(ctx, client, o, first.token, 2*pairs)
	if err != nil {
		return nil, err
	}
	users := make([]apiUser, 0, len(names))
	for _, name := range names {
		u, err := apiLogin(ctx, client, o, name)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, nil
}

// cpuTicks returns the CPU time the process has used, user and system, in
// clock ticks. It fails with PerfRSSFailed.
func cpuTicks(pid int) (int64, error) {
	path := "/proc/" + strconv.Itoa(pid) + "/stat"
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, errors.NewErr(tagPerfRSSFailed, err).SetMetadata("path", path)
	}
	// The name of the process is in parentheses and may hold spaces: the
	// fields count from the last closing one. utime and stime are the 14th and
	// 15th fields, the 12th and 13th after the state.
	text := string(raw)
	i := strings.LastIndexByte(text, ')')
	if i < 0 {
		return 0, errors.New(tagPerfRSSFailed, "the stat file has no process name", "path", path)
	}
	fields := strings.Fields(text[i+1:])
	if len(fields) < 13 {
		return 0, errors.New(tagPerfRSSFailed, "the stat file is too short", "path", path)
	}
	user, err1 := strconv.ParseInt(fields[11], 10, 64)
	system, err2 := strconv.ParseInt(fields[12], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, errors.New(tagPerfRSSFailed, "the stat file has no CPU times", "path", path)
	}
	return user + system, nil
}

// goMetrics are the numbers of the metrics of the server that the measurement
// reports.
type goMetrics struct {
	heapAlloc, heapSys, goroutines, callsActive float64
}

// readMetrics reads the metrics page. A failure is returned; the caller
// reports without them.
func readMetrics(ctx context.Context, client *http.Client, url string) (goMetrics, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return goMetrics{}, errors.NewErr(tagPerfRequestFailed, err).SetMetadata("url", url)
	}
	resp, err := client.Do(req)
	if err != nil {
		return goMetrics{}, errors.NewErr(tagPerfRequestFailed, err).SetMetadata("url", url)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return goMetrics{}, errors.NewErr(tagPerfRequestFailed, err).SetMetadata("url", url)
	}
	var m goMetrics
	for line := range strings.SplitSeq(string(data), "\n") {
		name, value, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		v, err := strconv.ParseFloat(value, 64)
		if err != nil {
			continue
		}
		switch name {
		case "go_memstats_heap_alloc_bytes":
			m.heapAlloc = v
		case "go_memstats_heap_sys_bytes":
			m.heapSys = v
		case "go_goroutines":
			m.goroutines = v
		case "clowder_calls_active":
			m.callsActive = v
		}
	}
	return m, nil
}

// packets is what the clients received, summed.
type packets struct{ audio, video int64 }

func countPackets(clients []*callstest.Client) packets {
	var p packets
	for _, c := range clients {
		p.audio += c.AudioIn()
		p.video += c.VideoIn()
	}
	return p
}

// measureCalls runs o.calls calls at once against the server at o.base and
// reports the CPU and the memory of the server (the process o.pid) over
// o.callDuration, and the packets that reached the clients. The server is
// the one of scripts/perf-calls.sh: a large demo, calls on, the media port on
// the loopback address.
func measureCalls(ctx context.Context, o options, stdout io.Writer) (err error) {
	if o.pid == 0 {
		return errors.New(tagPerfFlagsInvalid, "-calls needs -pid, the process ID of the server")
	}
	tb := &perfTB{ctx: ctx, stderr: os.Stderr}
	defer tb.close()
	defer func() {
		if r := recover(); r != nil {
			fatal, ok := r.(perfFatal)
			if !ok {
				panic(r)
			}
			err = errors.New(tagPerfCallFailed, "a client failed", "msg", fatal.msg)
		}
	}()

	client := newClient()
	if err := waitReady(ctx, client, o.base, o.ready); err != nil {
		return err
	}
	users, err := demoUsers(ctx, client, o, o.calls)
	if err != nil {
		return err
	}
	type pair struct {
		conv           uint64
		caller, callee apiUser
	}
	pairs := make([]pair, o.calls)
	for i := range pairs {
		p := pair{caller: users[2*i], callee: users[2*i+1]}
		var conv struct {
			ID uint64 `json:"id"`
		}
		if err := api(ctx, client, o.base, p.caller.token, http.MethodPost, "/conversations",
			map[string]any{"usernames": []string{p.callee.username}}, &conv, http.StatusOK, http.StatusCreated); err != nil {
			return err
		}
		p.conv = conv.ID
		pairs[i] = p
	}
	wsBase := "ws" + strings.TrimPrefix(o.base, "http")
	var clients []*callstest.Client
	for i, p := range pairs {
		path := "/conversations/" + strconv.FormatUint(p.conv, 10) + "/call"
		if err := api(ctx, client, o.base, p.caller.token, http.MethodPost, path, map[string]any{"video": true}, nil, http.StatusCreated); err != nil {
			return err
		}
		if err := api(ctx, client, o.base, p.callee.token, http.MethodPost, path+"/answer", map[string]any{"video": true}, nil, http.StatusOK); err != nil {
			return err
		}
		for _, who := range []struct {
			name string
			user apiUser
		}{{fmt.Sprintf("caller%d", i), p.caller}, {fmt.Sprintf("callee%d", i), p.callee}} {
			c, _, err := callstest.Dial(tb, who.name, wsBase+"/api/v1"+path+"/ws", callstest.Options{
				Header:       http.Header{"Authorization": {"Bearer " + who.user.token}},
				VideoBitrate: o.bitrate,
				Log:          func(string, ...any) {},
			})
			if err != nil {
				return errors.NewErr(tagPerfCallFailed, err).SetMetadata("client", who.name)
			}
			clients = append(clients, c)
		}
	}
	defer func() {
		for _, p := range pairs {
			path := "/conversations/" + strconv.FormatUint(p.conv, 10) + "/call/hangup"
			_ = api(context.WithoutCancel(ctx), client, o.base, p.caller.token, http.MethodPost, path, nil, nil, http.StatusOK, http.StatusNotFound, http.StatusConflict)
		}
	}()

	connectDeadline := time.After(callsConnectTimeout)
	for _, c := range clients {
		select {
		case <-c.Connected():
		case <-connectDeadline:
			return errors.New(tagPerfTimeout, "a client did not connect", "client", c.Name())
		case <-ctx.Done():
			return errors.New(tagPerfCanceled, "canceled while the clients connected")
		}
	}
	// A moment for the media to flow before the window starts.
	select {
	case <-time.After(3 * time.Second):
	case <-ctx.Done():
		return errors.New(tagPerfCanceled, "canceled while the calls settled")
	}

	cpu0, err := cpuTicks(o.pid)
	if err != nil {
		return err
	}
	rss0, err := statusKiB(o.pid, "VmRSS")
	if err != nil {
		return err
	}
	p0 := countPackets(clients)
	var m0 goMetrics
	if o.metrics != "" {
		if m0, err = readMetrics(ctx, client, o.metrics); err != nil {
			return err
		}
	}
	start := time.Now()
	var rssMax, rssSum, rssN int64
	var metricsMax goMetrics
	ticker := time.NewTicker(rssSampleEvery)
	defer ticker.Stop()
	for time.Since(start) < o.callDuration {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return errors.New(tagPerfCanceled, "canceled during the measurement")
		}
		rss, err := statusKiB(o.pid, "VmRSS")
		if err != nil {
			return err
		}
		rssMax, rssSum, rssN = max(rssMax, rss), rssSum+rss, rssN+1
		if o.metrics != "" && rssN%5 == 0 {
			if m, err := readMetrics(ctx, client, o.metrics); err == nil {
				metricsMax.heapAlloc = max(metricsMax.heapAlloc, m.heapAlloc)
				metricsMax.heapSys = max(metricsMax.heapSys, m.heapSys)
				metricsMax.goroutines = max(metricsMax.goroutines, m.goroutines)
				metricsMax.callsActive = max(metricsMax.callsActive, m.callsActive)
			}
		}
	}
	wall := time.Since(start)
	cpu1, err := cpuTicks(o.pid)
	if err != nil {
		return err
	}
	p1 := countPackets(clients)
	var m1 goMetrics
	if o.metrics != "" {
		if m1, err = readMetrics(ctx, client, o.metrics); err != nil {
			return err
		}
	}
	hwm, err := statusKiB(o.pid, "VmHWM")
	if err != nil {
		return err
	}

	cpuSeconds := float64(cpu1-cpu0) / clockTicks
	audio, video := p1.audio-p0.audio, p1.video-p0.video
	secs := wall.Seconds()
	_, _ = fmt.Fprintf(stdout, "calls: %d at once (%d clients), %.0f s, each client sends VP8 at about %.1f Mbit/s and Opus\n",
		o.calls, len(clients), secs, float64(o.bitrate)/1e6)
	_, _ = fmt.Fprintf(stdout, "server CPU: %.2f s of CPU in %.1f s = %.1f%% of one core (%.1f%% for each call)\n",
		cpuSeconds, secs, 100*cpuSeconds/secs, 100*cpuSeconds/secs/float64(o.calls))
	_, _ = fmt.Fprintf(stdout, "server memory (VmRSS): %.1f MiB when the window began, mean %.1f MiB, max %.1f MiB, peak of the process %.1f MiB\n",
		mib(rss0), mib(rssSum/max(rssN, 1)), mib(rssMax), mib(hwm))
	if o.metrics != "" {
		_, _ = fmt.Fprintf(stdout, "server heap: in use %.1f MiB at the start, %.1f MiB at the end, up to %.1f MiB; from the system %.1f MiB at the end; %.0f goroutines at the end (up to %.0f); %.0f calls active\n",
			m0.heapAlloc/(1<<20), m1.heapAlloc/(1<<20), metricsMax.heapAlloc/(1<<20), m1.heapSys/(1<<20), m1.goroutines, metricsMax.goroutines, m1.callsActive)
	}
	_, _ = fmt.Fprintf(stdout, "packets forwarded to the clients in the window: %d audio (%.0f/s), %d video (%.0f/s); per call %.0f audio/s and %.0f video/s\n",
		audio, float64(audio)/secs, video, float64(video)/secs,
		float64(audio)/secs/float64(o.calls), float64(video)/secs/float64(o.calls))
	return nil
}

const tagPerfCallFailed = "PerfCallFailed"
