package web

import (
	"bytes"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/metrics"
)

// scrape writes the registry of the set as text.
func scrape(t *testing.T, set *metrics.Set) string {
	t.Helper()
	var b bytes.Buffer
	if _, err := set.Registry.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// sample returns the value of the line of the metric that starts with prefix
// (name and labels as scraped), or "" when there is none.
func sample(out, prefix string) string {
	for line := range strings.SplitSeq(out, "\n") {
		if rest, ok := strings.CutPrefix(line, prefix+" "); ok {
			return rest
		}
	}
	return ""
}

func newMetricsSet(t *testing.T) *metrics.Set {
	t.Helper()
	set, err := metrics.NewSet(metrics.SetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// labelValue matches one label of a sample line: name="value".
var labelValue = regexp.MustCompile(`([a-z_]+)="((?:[^"\\]|\\.)*)"`)

// TestMetricsLabelsNeverCarryIDsOrNames makes requests whose paths hold IDs,
// a name and a token, and checks the whole output of the registry (SPEC D60):
// every pattern is a pattern of the mux or "none", no label value holds a long
// number, and no username, ID or token appears anywhere.
func TestMetricsLabelsNeverCarryIDsOrNames(t *testing.T) {
	t.Parallel()
	set := newMetricsSet(t)
	e := newRoomsEnvOn(t, set, nil)
	ss := e.serveStreams()
	token := "tok_9f8e7d6c5b4a39281706"
	apiToken := e.apiToken("alice", "metrics test")
	requests := []struct {
		name, method, path string
		header             http.Header
		body               string
		wantPattern        string
		wantCode           string
	}{
		{"a room", "GET", "/rooms/" + idText(e.general.ID), nil, "", "GET /rooms/{id}", "2xx"},
		{"a room that is not there", "GET", "/rooms/123456789", nil, "", "GET /rooms/{id}", "4xx"},
		{"a post that is not there", "GET", "/posts/456456456", nil, "", "GET /posts/{id}", "4xx"},
		{"a file", "GET", "/files/1234567/secret-name.png", nil, "", "GET /files/{id}/{name}", "4xx"},
		{"a conversation call, no token", "GET", "/api/v1/conversations/789789789/call", nil, "", "GET /api/v1/conversations/{id}/call", "4xx"},
		{"a conversation call, a token", "GET", "/api/v1/conversations/789789789/call", http.Header{"Authorization": {"Bearer " + apiToken}}, "", "GET /api/v1/conversations/{id}/call", "4xx"},
		{"no route, an ID-like segment", "GET", "/nothing/here/20201231235959", nil, "", "none", "4xx"},
		{"no route, a bot", "GET", "/wp-login.php", nil, "", "none", "4xx"},
		{"the metrics path on the public listener", "GET", metrics.Path, nil, "", "none", "4xx"},
		{"the wrong method", "DELETE", "/rooms/" + idText(e.general.ID), nil, "", "none", "4xx"},
		{"a login with a username", "POST", "/login", http.Header{"Content-Type": {"application/x-www-form-urlencoded"}},
			url.Values{"username": {"mallory-the-guesser"}, "password": {"nope"}}.Encode(), "POST /login", "4xx"},
		{"an invitation token", "GET", "/invite/" + token, nil, "", "GET /invite/{token}", "4xx"},
		{"a reset token", "GET", "/reset/" + token, nil, "", "GET /reset/{token}", "4xx"},
	}
	for _, tt := range requests {
		req, err := http.NewRequest(tt.method, ss.URL+tt.path, strings.NewReader(tt.body))
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range tt.header {
			req.Header[k] = v
		}
		e.cookies["alice"](req)
		resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		_ = resp.Body.Close()
	}
	out := scrape(t, set)

	// The public listener answers 404 for /metrics, and says nothing of the
	// metrics.
	rec := e.send(get(metrics.Path))
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "clowder_") {
		t.Errorf("GET /metrics on the public listener = %d %q, want the 404 page", rec.Code, rec.Body.String())
	}

	patterns := map[string]bool{metrics.PatternNone: true}
	for _, p := range e.srv.patterns {
		patterns[p] = true
	}
	for _, rt := range e.srv.svc.API.Routes() {
		patterns[rt.Pattern] = true
	}
	long := regexp.MustCompile(`[0-9]{4,}`)
	found := map[string]bool{}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		for _, m := range labelValue.FindAllStringSubmatch(line, -1) {
			name, value := m[1], m[2]
			if name == "pattern" {
				found[value] = true
				if !patterns[value] {
					t.Errorf("pattern %q is not a pattern of the mux:\n%s", value, line)
				}
			}
			if name != "le" && long.MatchString(value) {
				t.Errorf("label %s=%q holds a long number:\n%s", name, value, line)
			}
		}
	}
	for _, secret := range []string{"alice", "bob", "carol", "admin", "mallory", "tok_9f8e", "secret-name", "metrics test", "20201231", "123456789", "456456456", "789789789", "1234567", "wp-login", "general"} {
		if strings.Contains(out, secret) {
			t.Errorf("the metrics hold %q:\n%s", secret, out)
		}
	}
	for _, tt := range requests {
		if !found[tt.wantPattern] {
			t.Errorf("%s: no series for the pattern %q", tt.name, tt.wantPattern)
		}
		method := tt.method
		prefix := `clowder_http_requests_total{pattern="` + tt.wantPattern + `",method="` + method + `",code="` + tt.wantCode + `"}`
		if sample(out, prefix) == "" {
			t.Errorf("%s: no counter %s in\n%s", tt.name, prefix, out)
		}
	}
}

// TestMetricsRequestCounterAndHistogramMove checks that a request moves the
// counter by one and the histogram by one observation, in the right bucket.
func TestMetricsRequestCounterAndHistogramMove(t *testing.T) {
	t.Parallel()
	set := newMetricsSet(t)
	e := newRoomsEnvOn(t, set, nil)
	counter := `clowder_http_requests_total{pattern="GET /rooms/{id}",method="GET",code="2xx"}`
	for range 3 {
		req := get("/rooms/" + idText(e.general.ID))
		e.cookies["alice"](req)
		if rec := e.send(req); rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
	}
	out := scrape(t, set)
	if got := sample(out, counter); got != "3" {
		t.Errorf("%s = %q, want 3", counter, got)
	}
	count := `clowder_http_request_duration_seconds_count{pattern="GET /rooms/{id}",method="GET"}`
	if got := sample(out, count); got != "3" {
		t.Errorf("%s = %q, want 3", count, got)
	}
	inf := `clowder_http_request_duration_seconds_bucket{pattern="GET /rooms/{id}",method="GET",le="+Inf"}`
	if got := sample(out, inf); got != "3" {
		t.Errorf("%s = %q, want 3", inf, got)
	}
	sum := sample(out, `clowder_http_request_duration_seconds_sum{pattern="GET /rooms/{id}",method="GET"}`)
	if sum == "" || sum == "0" {
		t.Errorf("the sum of the durations = %q, want a positive number", sum)
	}
	// The status classes are told apart: a 3xx redirect to the login page.
	if rec := e.send(get("/rooms/" + idText(e.general.ID))); rec.Code != http.StatusSeeOther && rec.Code != http.StatusFound {
		t.Fatalf("a request with no login = %d, want a redirect", rec.Code)
	}
	if got := sample(scrape(t, set), `clowder_http_requests_total{pattern="GET /rooms/{id}",method="GET",code="3xx"}`); got != "1" {
		t.Errorf("3xx counter = %q, want 1", got)
	}
}

// TestMetricsStreamGaugeAndNoDuration opens an event stream, sees the gauge
// of open streams go up, closes it and sees the gauge go down and the stream
// counted, without an observation in the histogram.
func TestMetricsStreamGaugeAndNoDuration(t *testing.T) {
	t.Parallel()
	set := newMetricsSet(t)
	e := newRoomsEnvOn(t, set, nil)
	ss := e.serveStreams()
	c := ss.open("alice", e.streamOfRoom("general", "seen=0"), asDatastar())
	if c.status != http.StatusOK {
		t.Fatalf("stream status = %d", c.status)
	}
	if got := set.SSEStreams.Value(); got != 1 {
		t.Errorf("open streams = %d, want 1", got)
	}
	c2 := ss.open("bob", e.streamOfRoom("general", "seen=0"), asDatastar())
	if got := set.SSEStreams.Value(); got != 2 {
		t.Errorf("open streams = %d, want 2", got)
	}
	c.cancel()
	c2.cancel()
	deadline := time.Now().Add(streamTimeout)
	for set.SSEStreams.Value() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("open streams = %d after both ended, want 0", set.SSEStreams.Value())
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The counter is written after the gauge goes down: wait for it.
	counter := `clowder_http_requests_total{pattern="GET /rooms/{id}/stream",method="GET",code="2xx"}`
	for sample(scrape(t, set), counter) != "2" {
		if time.Now().After(deadline) {
			t.Fatalf("stream counter = %q, want 2:\n%s", sample(scrape(t, set), counter), scrape(t, set))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if out := scrape(t, set); strings.Contains(out, `clowder_http_request_duration_seconds_count{pattern="GET /rooms/{id}/stream"`) {
		t.Errorf("a stream was observed in the histogram:\n%s", out)
	}
}

// TestMetricsHubDropsLoginsAndWebSocketsAreWired checks that the pieces the
// server is made of report into the set it was given: the hub, the logins and
// the API's sockets.
func TestMetricsHubDropsLoginsAndWebSocketsAreWired(t *testing.T) {
	t.Parallel()
	set := newMetricsSet(t)
	e := newRoomsEnvOn(t, set, nil)
	// A login through the page, wrong and right.
	for _, tt := range []struct {
		password, result string
	}{{"wrong password", metrics.LoginWrong}, {testPassword, metrics.LoginOK}} {
		before := set.Logins.With(tt.result).Value()
		e.send(postForm("/login", loginForm("alice", tt.password), fromAddr("192.0.2.77:1234")))
		if got := set.Logins.With(tt.result).Value(); got != before+1 {
			t.Errorf("logins{%s} = %d, want %d", tt.result, got, before+1)
		}
	}
	// The hub drops a subscription that does not read.
	slow := e.hub.Subscribe(t.Context(), "metrics-test-topic")
	for range 100 {
		e.hub.Publish(live.Event{Topic: "metrics-test-topic", Kind: live.KindPostCreated})
	}
	if !slow.Dropped() {
		t.Fatal("the slow subscription was not dropped")
	}
	if got := set.HubDropped.With(metrics.DropOverflow).Value(); got != 1 {
		t.Errorf("hub drops = %d, want 1", got)
	}
}
