package metrics

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	clerrors "github.com/rm4n0s/errors"
)

// buildGolden makes the registry of the golden test: a counter with labels
// that need escaping, a gauge, a histogram and a metric read when scraped.
func buildGolden(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	req, err := r.NewCounterVec("test_requests_total", "Requests with a \\ and a\nnewline in the help.",
		L("route"), L("code", "2xx", "5xx"))
	if err != nil {
		t.Fatal(err)
	}
	req.With(`GET /a"b\c`+"\n"+`d`, "2xx").Add(3)
	req.With("GET /z", "5xx").Inc()
	req.With("GET /a", "2xx").Inc()
	req.With("GET /a", "418").Inc() // outside the closed set: other
	open, err := r.NewGauge("test_open", "Open things.")
	if err != nil {
		t.Fatal(err)
	}
	open.Add(5)
	open.Dec()
	hist, err := r.NewHistogramVec("test_duration_seconds", "Durations.", []float64{0.1, 1, 2.5}, L("route"))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []float64{0.05, 0.1, 0.7, 2.5, 30} {
		hist.With("GET /a").Observe(v)
	}
	hist.With("GET /empty")
	if err := r.NewFunc(KindGauge, "test_pool", "A pool.", []string{"state"}, func() []Sample {
		return []Sample{{Labels: []string{"open"}, Value: 4}, {Labels: []string{"idle"}, Value: 1.5}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.NewCounterFunc("test_waits_total", "Waits.", func() float64 { return 1.7e9 + 0.25 }); err != nil {
		t.Fatal(err)
	}
	return r
}

func scrape(t *testing.T, r *Registry) string {
	t.Helper()
	var b bytes.Buffer
	if _, err := r.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestTextFormatGolden(t *testing.T) {
	r := buildGolden(t)
	first := scrape(t, r)
	if second := scrape(t, r); first != second {
		t.Errorf("two scrapes differ:\n%s\n---\n%s", first, second)
	}
	want, err := os.ReadFile("testdata/golden.txt")
	if err != nil {
		t.Fatal(err)
	}
	if first != string(want) {
		t.Errorf("output differs from testdata/golden.txt:\n%s", first)
	}
}

func TestHandler(t *testing.T) {
	r := buildGolden(t)
	h := r.Handler(nil)
	tests := []struct {
		name, method, path string
		wantStatus         int
		wantAllow          string
	}{
		{"scrape", http.MethodGet, "/metrics", http.StatusOK, ""},
		{"other path", http.MethodGet, "/", http.StatusNotFound, ""},
		{"a path under it", http.MethodGet, "/metrics/x", http.StatusNotFound, ""},
		{"the profiler", http.MethodGet, "/debug/pprof/", http.StatusNotFound, ""},
		{"post", http.MethodPost, "/metrics", http.StatusMethodNotAllowed, "GET"},
		{"head", http.MethodHead, "/metrics", http.StatusMethodNotAllowed, "GET"},
		{"delete", http.MethodDelete, "/metrics", http.StatusMethodNotAllowed, "GET"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Header().Get("Allow"); got != tt.wantAllow {
				t.Errorf("Allow = %q, want %q", got, tt.wantAllow)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
			if tt.wantStatus == http.StatusOK {
				if got := rec.Header().Get("Content-Type"); got != "text/plain; version=0.0.4; charset=utf-8" {
					t.Errorf("Content-Type = %q", got)
				}
				if rec.Body.String() != scrape(t, r) {
					t.Error("the body is not the text format of the registry")
				}
			}
		})
	}
}

func TestRegisterFailures(t *testing.T) {
	tests := []struct {
		name      string
		do        func(r *Registry) error
		wantRoute string
	}{
		{"a bad name", func(r *Registry) error { _, err := r.NewCounter("1bad", "x"); return err },
			"Registry.NewCounter->Registry.NewCounterVec->Registry.register.MetricNameInvalid"},
		{"a name with a dash", func(r *Registry) error { _, err := r.NewGauge("a-b", "x"); return err },
			"Registry.NewGauge->Registry.NewGaugeVec->Registry.register.MetricNameInvalid"},
		{"a bad label", func(r *Registry) error { _, err := r.NewCounterVec("a_total", "x", L("a b")); return err },
			"Registry.NewCounterVec->Registry.register.MetricLabelInvalid"},
		{"a reserved label", func(r *Registry) error { _, err := r.NewCounterVec("a_total", "x", L("__name")); return err },
			"Registry.NewCounterVec->Registry.register.MetricLabelInvalid"},
		{"a label twice", func(r *Registry) error { _, err := r.NewCounterVec("a_total", "x", L("a"), L("a")); return err },
			"Registry.NewCounterVec->Registry.register.MetricLabelInvalid"},
		{"too many labels", func(r *Registry) error {
			_, err := r.NewGaugeVec("a", "x", L("a"), L("b"), L("c"), L("d"), L("e"))
			return err
		}, "Registry.NewGaugeVec->Registry.register.MetricLabelInvalid"},
		{"le on a histogram", func(r *Registry) error {
			_, err := r.NewHistogramVec("a_seconds", "x", []float64{1}, L("le"))
			return err
		}, "Registry.NewHistogramVec->Registry.register.MetricLabelInvalid"},
		{"a duplicate", func(r *Registry) error {
			if _, err := r.NewCounter("a_total", "x"); err != nil {
				return err
			}
			_, err := r.NewGauge("a_total", "y")
			return err
		}, "Registry.NewGauge->Registry.NewGaugeVec->Registry.register.MetricDuplicate"},
		{"buckets not increasing", func(r *Registry) error {
			_, err := r.NewHistogramVec("a_seconds", "x", []float64{1, 1})
			return err
		}, "Registry.NewHistogramVec.MetricBucketsInvalid"},
		{"an infinite bucket", func(r *Registry) error {
			_, err := r.NewHistogramVec("a_seconds", "x", []float64{1, 1 / zero()})
			return err
		}, "Registry.NewHistogramVec.MetricBucketsInvalid"},
		{"a function histogram", func(r *Registry) error {
			return r.NewFunc(KindHistogram, "a", "x", nil, nil)
		}, "Registry.NewFunc.MetricKindInvalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.do(NewRegistry())
			appErr, ok := clerrors.FromError(err)
			if !ok {
				t.Fatalf("err = %v, want a tagged error", err)
			}
			if !appErr.HasRoute(tt.wantRoute) {
				t.Errorf("route = %s, want it to contain %s", appErr.Route(), tt.wantRoute)
			}
		})
	}
}

// zero returns 0, so that 1/zero() is +Inf without a constant division.
func zero() float64 { return 0 }

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestWriteToFailure(t *testing.T) {
	r := NewRegistry()
	_, err := r.WriteTo(failingWriter{})
	appErr, ok := clerrors.FromError(err)
	if !ok || !appErr.HasRoute("Registry.WriteTo.MetricsWriteFailed") {
		t.Fatalf("err = %v, want the route Registry.WriteTo.MetricsWriteFailed", err)
	}
	if !clerrors.Is(err, io.ErrClosedPipe) {
		t.Error("the error does not wrap the cause")
	}
	rec := httptest.NewRecorder()
	r.Handler(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("an empty registry answers %d, want 200", rec.Code)
	}
}

func TestNilMetricsDoNothing(t *testing.T) {
	var (
		c  *Counter
		cv *CounterVec
		g  *Gauge
		gv *GaugeVec
		h  *Histogram
		hv *HistogramVec
	)
	c.Inc()
	c.Add(2)
	cv.With("a").Inc()
	cv.Preload()
	g.Inc()
	g.Dec()
	g.Add(3)
	g.Set(4)
	gv.With("a").Inc()
	gv.Preload()
	h.Observe(1)
	h.ObserveDuration(time.Second)
	hv.With("a").Observe(1)
	if c.Value() != 0 || g.Value() != 0 {
		t.Error("a nil metric holds a value")
	}
}

func TestLabelValues(t *testing.T) {
	r := NewRegistry()
	cv, err := r.NewCounterVec("t_total", "x", L("closed", "a", "b"), L("open"))
	if err != nil {
		t.Fatal(err)
	}
	cv.With("a", "x").Inc()
	cv.With("zzz", "x").Inc()    // folded into other
	cv.With("other", "x").Inc()  // the same series
	cv.With("a").Inc()           // too few values: nothing
	cv.With("a", "x", "y").Inc() // too many values: nothing
	cv.With("b", "bad\xffutf").Inc()
	cv.With("b", strings.Repeat("x", 500)).Inc()
	out := scrape(t, r)
	for _, want := range []string{
		`t_total{closed="a",open="x"} 1`,
		`t_total{closed="other",open="x"} 2`,
		"t_total{closed=\"b\",open=\"bad\uFFFDutf\"} 1",
		`t_total{closed="b",open="` + strings.Repeat("x", maxLabelValue) + `"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "t_total{") != 4 {
		t.Errorf("want 4 series:\n%s", out)
	}
}

func TestSeriesLimit(t *testing.T) {
	r := NewRegistry()
	cv, err := r.NewCounterVec("t_total", "x", L("open"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range maxSeries + 10 {
		cv.With(strings.Repeat("a", i%50+1) + string(rune('a'+i%26)) + strings.Repeat("b", i/50)).Inc()
	}
	if n := strings.Count(scrape(t, r), "t_total{"); n > maxSeries {
		t.Errorf("series = %d, want at most %d", n, maxSeries)
	}
}

func TestPreload(t *testing.T) {
	r := NewRegistry()
	closed, err := r.NewCounterVec("c_total", "x", L("a", "x", "y"), L("b", "p", "q"))
	if err != nil {
		t.Fatal(err)
	}
	closed.Preload()
	open, err := r.NewCounterVec("o_total", "x", L("a"))
	if err != nil {
		t.Fatal(err)
	}
	open.Preload()
	out := scrape(t, r)
	if n := strings.Count(out, "c_total{"); n != 4 {
		t.Errorf("preloaded series = %d, want 4:\n%s", n, out)
	}
	if strings.Contains(out, "o_total{") {
		t.Errorf("an open label was preloaded:\n%s", out)
	}
}

func TestHistogramBuckets(t *testing.T) {
	r := NewRegistry()
	hv, err := r.NewHistogramVec("h_seconds", "x", []float64{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	h := hv.With()
	for _, v := range []float64{0.5, 1, 1.5, 2, 3} {
		h.Observe(v)
	}
	h.Observe(nan())
	h.ObserveDuration(500 * time.Millisecond)
	want := "h_seconds_bucket{le=\"1\"} 3\nh_seconds_bucket{le=\"2\"} 5\nh_seconds_bucket{le=\"+Inf\"} 6\nh_seconds_sum 8.5\nh_seconds_count 6\n"
	if out := scrape(t, r); !strings.Contains(out, want) {
		t.Errorf("output lacks\n%s\nin\n%s", want, out)
	}
}

func nan() float64 {
	z := zero()
	return z / z
}

// TestConcurrentUseAndScrape increments and observes from many goroutines
// while another scrapes; the race detector watches, and the totals must add
// up.
func TestConcurrentUseAndScrape(t *testing.T) {
	r := NewRegistry()
	cv, err := r.NewCounterVec("c_total", "x", L("route"))
	if err != nil {
		t.Fatal(err)
	}
	gv, err := r.NewGauge("g", "x")
	if err != nil {
		t.Fatal(err)
	}
	hv, err := r.NewHistogramVec("h_seconds", "x", DurationBuckets, L("route"))
	if err != nil {
		t.Fatal(err)
	}
	const workers, each = 16, 2000
	var wg sync.WaitGroup
	stop := make(chan struct{})
	scraped := make(chan struct{})
	go func() {
		defer close(scraped)
		for {
			select {
			case <-stop:
				return
			default:
				var b bytes.Buffer
				if _, err := r.WriteTo(&b); err != nil {
					t.Error(err)
					return
				}
			}
		}
	}()
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			route := []string{"GET /a", "GET /b", "GET /c"}[w%3]
			for range each {
				cv.With(route).Inc()
				gv.Inc()
				hv.With(route).Observe(0.02)
				gv.Dec()
			}
		}()
	}
	wg.Wait()
	close(stop)
	<-scraped
	var total uint64
	for _, route := range []string{"GET /a", "GET /b", "GET /c"} {
		total += cv.With(route).Value()
	}
	if total != workers*each {
		t.Errorf("counted %d, want %d", total, workers*each)
	}
	if gv.Value() != 0 {
		t.Errorf("gauge = %d, want 0", gv.Value())
	}
	if out := scrape(t, r); !strings.Contains(out, `h_seconds_count{route="GET /a"} `) {
		t.Errorf("the histogram is missing:\n%s", out)
	}
}

func BenchmarkCounterInc(b *testing.B) {
	r := NewRegistry()
	c, err := r.NewCounter("c_total", "x")
	if err != nil {
		b.Fatal(err)
	}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Inc()
		}
	})
}

// BenchmarkRequestPath measures what a request costs: one lookup and increment
// of the request counter and one observation of the histogram.
func BenchmarkRequestPath(b *testing.B) {
	s, err := NewSet(SetOptions{})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			s.HTTPRequests.With("GET /rooms/{id}", "GET", "2xx").Inc()
			s.HTTPDuration.With("GET /rooms/{id}", "GET").ObserveDuration(3 * time.Millisecond)
		}
	})
}
