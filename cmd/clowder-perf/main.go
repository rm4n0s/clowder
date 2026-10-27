// Command clowder-perf measures a running Clowder server against two targets
// of SPEC §10: how long a new post takes to reach many open streams of its
// room, and how much memory the server uses with those streams open and idle.
// scripts/perf.sh builds the large demo, starts the server and runs it; see
// docs/PERFORMANCE.md.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rm4n0s/errors"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "clowder-perf:", err)
		os.Exit(1)
	}
}

// Defaults of the flags.
const (
	defaultStreams      = 200
	defaultRounds       = 5
	defaultSettle       = 2 * time.Second
	defaultReadyTimeout = 60 * time.Second
	defaultRoundTimeout = 30 * time.Second
	defaultRoomID       = 1
	defaultUser         = "alice"
	defaultDemoPassword = "demo-password-1234"
	readyPollInterval   = 100 * time.Millisecond
	betweenRoundsPause  = 500 * time.Millisecond
	targetFanOut        = 200 * time.Millisecond
	targetIdleMemoryMiB = 150
	kibPerMiB           = 1024
)

// freePortAddr is where -free-port looks for a free port: the loopback
// address, since the server of the measurement listens there.
const freePortAddr = "127.0.0.1:0"

// options are the flags of the command.
type options struct {
	base     string
	user     string
	password string
	roomID   uint64
	streams  int
	rounds   int
	pid      int
	settle   time.Duration
	ready    time.Duration
	timeout  time.Duration
	freePort bool
}

// run parses the flags and either prints a free TCP port (-free-port) or
// measures the server at -base.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	opts, err := parseOptions(args, stderr)
	if err != nil {
		return err
	}
	if opts.freePort {
		port, err := freePort(freePortAddr)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(stdout, port)
		return nil
	}
	return measure(ctx, opts, stdout)
}

// parseOptions reads the flags. It fails with PerfFlagsInvalid.
func parseOptions(args []string, stderr io.Writer) (options, error) {
	var o options
	fs := newFlagSet(&o, stderr)
	if err := fs.Parse(args); err != nil {
		return options{}, errors.NewErr(tagPerfFlagsInvalid, err)
	}
	if fs.NArg() > 0 {
		return options{}, errors.New(tagPerfFlagsInvalid, "unexpected arguments", "args", fs.Args())
	}
	if !o.freePort && (o.base == "" || o.streams < 1 || o.rounds < 1 || o.roomID == 0) {
		return options{}, errors.New(tagPerfFlagsInvalid, "-base is required and -streams, -rounds and -room must be positive",
			"base", o.base, "streams", o.streams, "rounds", o.rounds, "room", o.roomID)
	}
	return o, nil
}

// freePort asks the system for a TCP port on addr (host:0) that nothing
// listens on. The port is free when it returns; the caller uses it at once.
func freePort(addr string) (int, error) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return 0, errors.NewErr(tagPerfPortFailed, err).SetMetadata("addr", addr)
	}
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		_ = l.Close()
		return 0, errors.NewErr(tagPerfPortFailed, err).SetMetadata("addr", l.Addr().String())
	}
	if err := l.Close(); err != nil {
		return 0, errors.NewErr(tagPerfPortFailed, err).SetMetadata("addr", addr)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return 0, errors.NewErr(tagPerfPortFailed, err).SetMetadata("port", port)
	}
	return n, nil
}

// hit is the arrival of a probe post on one stream.
type hit struct {
	round int
	at    time.Time
}

// measure logs in, opens the streams, reads the memory of the server, posts
// the probe posts and prints the summary.
func measure(ctx context.Context, o options, stdout io.Writer) error {
	client := newClient()
	defer client.CloseIdleConnections()
	if err := waitReady(ctx, client, o.base, o.ready); err != nil {
		return err
	}
	var before int64
	if o.pid > 0 {
		var err error
		if before, err = statusKiB(o.pid, "VmRSS"); err != nil {
			return err
		}
	}
	cookie, err := login(ctx, client, o)
	if err != nil {
		return err
	}
	seen, err := newestSeen(ctx, client, o, cookie)
	if err != nil {
		return err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	runID := strconv.FormatInt(time.Now().UnixNano(), 36)
	hits := make(chan hit, o.streams*o.rounds)
	s := &streams{client: client, o: o, cookie: cookie, seen: seen, prefix: "perf-probe-" + runID + "-", hits: hits, done: make(chan struct{}, o.streams)}
	defer func() {
		cancel()
		s.wait()
	}()
	if err := s.open(streamCtx); err != nil {
		return err
	}
	select {
	case <-time.After(o.settle):
	case <-ctx.Done():
		return errors.NewErr(tagPerfCanceled, ctx.Err())
	}
	var idle int64
	if o.pid > 0 {
		if idle, err = statusKiB(o.pid, "VmRSS"); err != nil {
			return err
		}
	}
	var all []time.Duration
	_, _ = fmt.Fprintf(stdout, "streams open: %d of room %d\n", o.streams, o.roomID)
	for round := 1; round <= o.rounds; round++ {
		latencies, postTook, err := s.probe(ctx, round)
		if err != nil {
			return err
		}
		sum := summarize(latencies)
		_, _ = fmt.Fprintf(stdout, "round %d: post answered in %s, reached %d streams: p50 %s p95 %s max %s\n",
			round, postTook.Round(time.Millisecond), len(latencies), ms(sum.p50), ms(sum.p95), ms(sum.max))
		all = append(all, latencies...)
		select {
		case <-time.After(betweenRoundsPause):
		case <-ctx.Done():
			return errors.NewErr(tagPerfCanceled, ctx.Err())
		}
	}
	sum := summarize(all)
	_, _ = fmt.Fprintf(stdout, "fan-out to %d streams over %d posts: p50 %s p95 %s max %s (target: max under %s) %s\n",
		o.streams, o.rounds, ms(sum.p50), ms(sum.p95), ms(sum.max), targetFanOut, verdict(sum.max < targetFanOut))
	if o.pid > 0 {
		_, _ = fmt.Fprintf(stdout, "server memory: %.1f MiB before the streams, %.1f MiB with %d idle streams (target: under %d MiB) %s\n",
			mib(before), mib(idle), o.streams, targetIdleMemoryMiB, verdict(mib(idle) < targetIdleMemoryMiB))
		peak, err := statusKiB(o.pid, "VmHWM")
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "server memory peak over the run: %.1f MiB\n", mib(peak))
	}
	return nil
}

// ms formats a duration in milliseconds with one decimal.
func ms(d time.Duration) string {
	return strconv.FormatFloat(float64(d)/float64(time.Millisecond), 'f', 1, 64) + " ms"
}

// mib converts kibibytes to mebibytes.
func mib(kib int64) float64 {
	return float64(kib) / kibPerMiB
}

// verdict says whether a target is met.
func verdict(ok bool) string {
	if ok {
		return "OK"
	}
	return "MISSED"
}

// newClient makes the HTTP client of the measurement: no redirects (a login
// answers with one), no compression, as many connections as streams.
func newClient() *http.Client {
	return &http.Client{
		Transport:     &http.Transport{DisableCompression: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// waitReady polls the login page until the server answers 200.
func waitReady(ctx context.Context, client *http.Client, base string, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/login", nil)
		if err != nil {
			return errors.NewErr(tagPerfRequestFailed, err).SetMetadata("url", base+"/login")
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return errors.New(tagPerfNotReady, "the server did not answer in time", "base", base, "limit", limit.String())
		}
		select {
		case <-time.After(readyPollInterval):
		case <-ctx.Done():
			return errors.NewErr(tagPerfCanceled, ctx.Err())
		}
	}
}

// sessionCookieName is the cookie that holds the session of Clowder.
const sessionCookieName = "clowder_session"

// login posts the login form and returns the session cookie. It fails with
// PerfLoginFailed when the server does not set one.
func login(ctx context.Context, client *http.Client, o options) (*http.Cookie, error) {
	form := url.Values{"username": {o.user}, "password": {o.password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.base+"/login", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, errors.NewErr(tagPerfRequestFailed, err).SetMetadata("url", o.base+"/login")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.NewErr(tagPerfRequestFailed, err).SetMetadata("url", o.base+"/login")
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			return c, nil
		}
	}
	return nil, errors.New(tagPerfLoginFailed, "the server set no session cookie",
		"user", o.user, "status", resp.StatusCode)
}

// seenPattern finds the newest post in the stream URL that the room page gives
// to its stream.
var seenPattern = regexp.MustCompile(`/stream\?seen=(\d+)`)

// newestSeen opens the room page, as a browser does before it opens the
// stream, and returns the "seen" value of its stream URL: the ID of the newest
// post it shows. It fails with PerfPageFailed when the page does not answer
// 200 or has no stream URL.
func newestSeen(ctx context.Context, client *http.Client, o options, cookie *http.Cookie) (string, error) {
	target := o.base + "/rooms/" + strconv.FormatUint(o.roomID, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", errors.NewErr(tagPerfRequestFailed, err).SetMetadata("url", target)
	}
	req.AddCookie(cookie)
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.NewErr(tagPerfRequestFailed, err).SetMetadata("url", target)
	}
	defer func() { _ = resp.Body.Close() }()
	page, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", errors.NewErr(tagPerfRequestFailed, err).SetMetadata("url", target)
	}
	m := seenPattern.FindSubmatch(page)
	if resp.StatusCode != http.StatusOK || m == nil {
		return "", errors.New(tagPerfPageFailed, "the room page did not answer with a stream URL",
			"url", target, "status", resp.StatusCode)
	}
	return string(m[1]), nil
}

// streams are the open streams of the measurement.
type streams struct {
	client *http.Client
	o      options
	cookie *http.Cookie
	// seen is the ID of the newest post of the page that opened the streams,
	// which the page tells its stream so that it does not send the feed again.
	seen string
	// prefix starts the text of every probe post of this run.
	prefix string
	hits   chan hit
	done   chan struct{}
	// readers counts the goroutines that read a stream.
	readers int
}

// open starts the streams, one goroutine each, and returns when all have
// received their first event, which the server writes after it subscribed. It
// fails with PerfStreamFailed when a stream cannot be opened.
func (s *streams) open(ctx context.Context) error {
	opened := make(chan error, s.o.streams)
	for range s.o.streams {
		s.readers++
		go func() {
			s.read(ctx, opened)
			s.done <- struct{}{}
		}()
	}
	for range s.o.streams {
		select {
		case err := <-opened:
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return errors.NewErr(tagPerfCanceled, ctx.Err())
		}
	}
	return nil
}

// wait waits for the reading goroutines to end; the context of open must be
// canceled first.
func (s *streams) wait() {
	for range s.readers {
		<-s.done
	}
}

// read opens one stream, reports on opened when its first line arrived (or
// why it failed) and then reads it until the context ends, recording the
// arrival of every probe post.
func (s *streams) read(ctx context.Context, opened chan<- error) {
	target := s.o.base + "/rooms/" + strconv.FormatUint(s.o.roomID, 10) + "/stream?seen=" + s.seen
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		opened <- errors.NewErr(tagPerfRequestFailed, err).SetMetadata("url", target)
		return
	}
	req.AddCookie(s.cookie)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := s.client.Do(req)
	if err != nil {
		opened <- errors.NewErr(tagPerfRequestFailed, err).SetMetadata("url", target)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		opened <- errors.New(tagPerfStreamFailed, "the stream did not open", "url", target, "status", resp.StatusCode)
		return
	}
	r := bufio.NewReader(resp.Body)
	first := true
	seen := map[int]bool{}
	for {
		line, err := r.ReadString('\n')
		if first {
			first = false
			if err != nil {
				opened <- errors.NewErr(tagPerfStreamFailed, err).SetMetadata("url", target)
				return
			}
			opened <- nil
		}
		// A post is in several lines of its patch; the first one counts.
		if round, ok := s.roundOf(line); ok && !seen[round] {
			seen[round] = true
			s.hits <- hit{round: round, at: time.Now()}
		}
		if err != nil {
			return
		}
	}
}

// roundOf returns the round of the probe post that a line of the stream
// carries, if it carries one.
func (s *streams) roundOf(line string) (int, bool) {
	i := strings.Index(line, s.prefix)
	if i < 0 {
		return 0, false
	}
	rest := line[i+len(s.prefix):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0, false
	}
	return n, true
}

// probe posts the probe post of the round and returns, for each stream, how
// long after the post was sent it received the post, and how long the server
// took to answer the post. It fails with PerfPostFailed when the server
// refuses the post and with PerfTimeout when a stream does not receive it.
func (s *streams) probe(ctx context.Context, round int) ([]time.Duration, time.Duration, error) {
	form := url.Values{"format": {"text"}, "body": {s.prefix + strconv.Itoa(round) + " measuring the fan-out of a new post to the open streams"}}
	target := s.o.base + "/rooms/" + strconv.FormatUint(s.o.roomID, 10) + "/posts"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, errors.NewErr(tagPerfRequestFailed, err).SetMetadata("url", target)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(s.cookie)
	start := time.Now()
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, 0, errors.NewErr(tagPerfRequestFailed, err).SetMetadata("url", target)
	}
	took := time.Since(start)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		return nil, 0, errors.New(tagPerfPostFailed, "the server did not accept the post", "status", resp.StatusCode, "round", round)
	}
	latencies := make([]time.Duration, 0, s.o.streams)
	timeout := time.After(s.o.timeout)
	for len(latencies) < s.o.streams {
		select {
		case h := <-s.hits:
			if h.round == round {
				latencies = append(latencies, h.at.Sub(start))
			}
		case <-timeout:
			return nil, 0, errors.New(tagPerfTimeout, "not every stream received the post in time",
				"round", round, "received", len(latencies), "streams", s.o.streams)
		case <-ctx.Done():
			return nil, 0, errors.NewErr(tagPerfCanceled, ctx.Err())
		}
	}
	return latencies, took, nil
}

// summary holds the percentiles of a list of durations.
type summary struct {
	p50, p95, max time.Duration
}

// summarize returns the median, the 95th percentile and the maximum of d. It
// sorts d.
func summarize(d []time.Duration) summary {
	if len(d) == 0 {
		return summary{}
	}
	slices.Sort(d)
	at := func(p float64) time.Duration {
		i := int(p*float64(len(d))+0.999999) - 1
		return d[max(0, min(i, len(d)-1))]
	}
	return summary{p50: at(0.50), p95: at(0.95), max: d[len(d)-1]}
}

// statusKiB returns the value of the field (VmRSS, VmHWM) of
// /proc/<pid>/status, which is in kibibytes. It fails with PerfRSSFailed.
func statusKiB(pid int, field string) (int64, error) {
	path := "/proc/" + strconv.Itoa(pid) + "/status"
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, errors.NewErr(tagPerfRSSFailed, err).SetMetadata("path", path)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		rest, ok := strings.CutPrefix(line, field+":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 1 {
			break
		}
		n, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0, errors.NewErr(tagPerfRSSFailed, err).SetMetadata("path", path, "line", line)
		}
		return n, nil
	}
	return 0, errors.New(tagPerfRSSFailed, "the status file lacks the field", "path", path, "field", field)
}

const (
	tagPerfFlagsInvalid  = "PerfFlagsInvalid"
	tagPerfPortFailed    = "PerfPortFailed"
	tagPerfRequestFailed = "PerfRequestFailed"
	tagPerfNotReady      = "PerfNotReady"
	tagPerfPageFailed    = "PerfPageFailed"
	tagPerfLoginFailed   = "PerfLoginFailed"
	tagPerfStreamFailed  = "PerfStreamFailed"
	tagPerfPostFailed    = "PerfPostFailed"
	tagPerfTimeout       = "PerfTimeout"
	tagPerfRSSFailed     = "PerfRSSFailed"
	tagPerfCanceled      = "PerfCanceled"
)
