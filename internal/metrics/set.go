package metrics

import (
	"database/sql"
	"runtime"
	rtmetrics "runtime/metrics"
	"time"
)

// The names of the metrics of SPEC D60 and of step 23.2. They are the contract
// with the people who scrape and alert on them.
const (
	NameHTTPRequests     = "clowder_http_requests_total"
	NameHTTPDuration     = "clowder_http_request_duration_seconds"
	NameSSEStreams       = "clowder_sse_streams_open"
	NameWebSockets       = "clowder_websockets_open"
	NameHubDropped       = "clowder_hub_dropped_total"
	NameLogins           = "clowder_logins_total"
	NameSSHCodes         = "clowder_ssh_codes_issued_total"
	NameCallsActive      = "clowder_calls_active"
	NameCallsTotal       = "clowder_calls_total"
	NameDBConnections    = "clowder_db_connections"
	NameDBWait           = "clowder_db_wait_total"
	NameDBWaitSeconds    = "clowder_db_wait_seconds_total"
	NameRedisReconnects  = "clowder_redis_reconnects_total"
	NameRedisPublished   = "clowder_redis_bridge_published_total"
	NameRedisReceived    = "clowder_redis_bridge_received_total"
	NameBuildInfo        = "clowder_build_info"
	NameGoroutines       = "go_goroutines"
	NameHeapAlloc        = "go_memstats_heap_alloc_bytes"
	NameHeapSys          = "go_memstats_heap_sys_bytes"
	NameGCCycles         = "go_gc_cycles_total"
	NameProcessStartTime = "process_start_time_seconds"
)

// The values of the closed labels that the reporting packages use.
const (
	// WebSocketKindAPI is the kind of the user's WebSocket of the API and of
	// the call socket of the API.
	WebSocketKindAPI = "api"
	// WebSocketKindCall is the kind of the signalling socket of a call.
	WebSocketKindCall = "call"
	// LoginOK is a login that started a session.
	LoginOK = "ok"
	// LoginWrong is a wrong username or password.
	LoginWrong = "wrong"
	// LoginLimited is a login the limiter refused.
	LoginLimited = "limited"
	// LoginSSHCodeRequired is a right password of a user who must now give an
	// SSH sign-in code.
	LoginSSHCodeRequired = "ssh_code_required"
	// LoginSSHCodeWrong is a wrong SSH sign-in code.
	LoginSSHCodeWrong = "ssh_code_wrong"
	// DropOverflow is a subscription closed because its buffer was full.
	DropOverflow = "overflow"
	// DropReconnect is a subscription closed because the bridge to Redis came
	// back after a break.
	DropReconnect = "reconnect"
	// PatternNone is the route pattern of a request no route matched.
	PatternNone = "none"
)

// DurationBuckets are the upper bounds, in seconds, of the histogram of the
// duration of requests.
var DurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Set is every metric Clowder reports, made in one Registry. serve makes it
// and passes it, or the metric a package reports into, to that package. A nil
// *Set is not valid, but every field is nil-safe: a package that was given no
// Set reports into nothing.
type Set struct {
	// Registry holds all the metrics and writes them.
	Registry *Registry
	// HTTPRequests counts the requests that ended, by route pattern, method
	// and status class. Event streams and WebSockets are counted when they
	// end.
	HTTPRequests *CounterVec
	// HTTPDuration is the duration of the requests that are not streams or
	// WebSockets, by route pattern and method.
	HTTPDuration *HistogramVec
	// SSEStreams is the number of open event streams of the pages.
	SSEStreams *Gauge
	// WebSockets is the number of open WebSockets by kind: api (the user's
	// socket and the call socket of the API) is WebSocketKindAPI and the
	// signalling socket of a call is WebSocketKindCall.
	WebSockets *GaugeVec
	// HubDropped counts the subscriptions of the hub that it closed, by
	// reason: DropOverflow or DropReconnect.
	HubDropped *CounterVec
	// Logins counts the logins by result: LoginOK and the others.
	Logins *CounterVec
	// SSHCodes counts the sign-in codes the SSH server issued.
	SSHCodes *Counter
	// CallsTotal counts the calls whose media this process held, by how they
	// ended.
	CallsTotal *CounterVec
	// RedisReconnects, RedisPublished and RedisReceived are the counters of
	// the bridge to Redis. They are nil when Redis is not configured.
	RedisReconnects *Counter
	RedisPublished  *Counter
	RedisReceived   *Counter
}

// SetOptions are the settings of NewSet.
type SetOptions struct {
	// Version is the version shown by clowder_build_info; empty is "unknown".
	Version string
	// GoVersion is the Go version shown by clowder_build_info; empty is
	// runtime.Version().
	GoVersion string
	// Start is the start time of the process for process_start_time_seconds;
	// the zero time is now.
	Start time.Time
	// Redis makes the metrics of the bridge to Redis.
	Redis bool
}

// setBuilder makes the metrics of a Set and keeps the first error.
type setBuilder struct {
	reg *Registry
	err error
}

func (b *setBuilder) counterVec(name, help string, labels ...Label) *CounterVec {
	if b.err != nil {
		return nil
	}
	cv, err := b.reg.NewCounterVec(name, help, labels...)
	b.err = err
	return cv
}

func (b *setBuilder) counter(name, help string) *Counter {
	if b.err != nil {
		return nil
	}
	c, err := b.reg.NewCounter(name, help)
	b.err = err
	return c
}

func (b *setBuilder) gauge(name, help string) *Gauge {
	if b.err != nil {
		return nil
	}
	g, err := b.reg.NewGauge(name, help)
	b.err = err
	return g
}

func (b *setBuilder) gaugeVec(name, help string, labels ...Label) *GaugeVec {
	if b.err != nil {
		return nil
	}
	gv, err := b.reg.NewGaugeVec(name, help, labels...)
	b.err = err
	return gv
}

func (b *setBuilder) histogramVec(name, help string, buckets []float64, labels ...Label) *HistogramVec {
	if b.err != nil {
		return nil
	}
	hv, err := b.reg.NewHistogramVec(name, help, buckets, labels...)
	b.err = err
	return hv
}

func (b *setBuilder) gaugeFunc(name, help string, fn func() float64) {
	if b.err == nil {
		b.err = b.reg.NewGaugeFunc(name, help, fn)
	}
}

func (b *setBuilder) counterFunc(name, help string, fn func() float64) {
	if b.err == nil {
		b.err = b.reg.NewCounterFunc(name, help, fn)
	}
}

// NewSet makes a Registry with the metrics of SPEC D60: the ones services
// report into, and the ones read when scraped (the Go runtime, the process
// start time and the build information). The pool of the database and the
// calls of the media server are added with RegisterPool and RegisterCalls,
// once they exist.
func NewSet(opts SetOptions) (*Set, error) {
	if opts.Version == "" {
		opts.Version = "unknown"
	}
	if opts.GoVersion == "" {
		opts.GoVersion = runtime.Version()
	}
	if opts.Start.IsZero() {
		opts.Start = time.Now()
	}
	b := &setBuilder{reg: NewRegistry()}
	s := &Set{Registry: b.reg}
	methods := L("method", "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS")
	s.HTTPRequests = b.counterVec(NameHTTPRequests,
		"Requests that ended, by route pattern, method and status class. Event streams and WebSockets are counted when they end.",
		L("pattern"), methods, L("code", "1xx", "2xx", "3xx", "4xx", "5xx"))
	s.HTTPDuration = b.histogramVec(NameHTTPDuration,
		"Seconds a request took, by route pattern and method. Event streams and WebSockets are not observed.",
		DurationBuckets, L("pattern"), methods)
	s.SSEStreams = b.gauge(NameSSEStreams, "Open event streams of the pages.")
	s.WebSockets = b.gaugeVec(NameWebSockets, "Open WebSockets, by kind.", L("kind", WebSocketKindAPI, WebSocketKindCall))
	s.HubDropped = b.counterVec(NameHubDropped,
		"Subscriptions of the live hub that it closed, by reason.", L("reason", DropOverflow, DropReconnect))
	s.Logins = b.counterVec(NameLogins, "Logins, by result.",
		L("result", LoginOK, LoginWrong, LoginLimited, LoginSSHCodeRequired, LoginSSHCodeWrong))
	s.SSHCodes = b.counter(NameSSHCodes, "SSH sign-in codes issued.")
	s.CallsTotal = b.counterVec(NameCallsTotal, "Calls whose media this process held, by how they ended.",
		L("outcome", "ended", "hangup", "cancelled", "declined", "missed", "busy", "failed", "disabled",
			"session_ended", "server_stopped"))
	if opts.Redis {
		s.RedisReconnects = b.counter(NameRedisReconnects, "Times the connection to Redis came back after a break.")
		s.RedisPublished = b.counter(NameRedisPublished, "Live events this process sent to Redis.")
		s.RedisReceived = b.counter(NameRedisReceived, "Live events this process received from other processes through Redis.")
	}
	if b.err != nil {
		return nil, b.err
	}
	s.WebSockets.Preload()
	s.HubDropped.Preload()
	s.Logins.Preload()
	s.CallsTotal.Preload()

	b.gaugeFunc(NameGoroutines, "Number of goroutines.", func() float64 { return float64(runtime.NumGoroutine()) })
	b.gaugeFunc(NameHeapAlloc, "Bytes of allocated heap objects.", func() float64 {
		return readRuntime("/memory/classes/heap/objects:bytes")
	})
	b.gaugeFunc(NameHeapSys, "Bytes of heap memory obtained from the system.", func() float64 {
		return readRuntime("/memory/classes/heap/objects:bytes", "/memory/classes/heap/unused:bytes",
			"/memory/classes/heap/free:bytes", "/memory/classes/heap/released:bytes")
	})
	b.counterFunc(NameGCCycles, "Completed garbage collection cycles.", func() float64 {
		return readRuntime("/gc/cycles/total:gc-cycles")
	})
	start := float64(opts.Start.UnixNano()) / 1e9
	b.gaugeFunc(NameProcessStartTime, "Start time of the process, in seconds since the Unix epoch.", func() float64 { return start })
	if b.err == nil {
		b.err = b.reg.NewFunc(KindGauge, NameBuildInfo, "Build information; the value is always 1.",
			[]string{"version", "go_version"}, func() []Sample {
				return []Sample{{Labels: []string{opts.Version, opts.GoVersion}, Value: 1}}
			})
	}
	if b.err != nil {
		return nil, b.err
	}
	return s, nil
}

// readRuntime returns the sum of the runtime metrics with the names, read from
// runtime/metrics; a name the runtime does not know counts as 0.
func readRuntime(names ...string) float64 {
	samples := make([]rtmetrics.Sample, len(names))
	for i, n := range names {
		samples[i].Name = n
	}
	rtmetrics.Read(samples)
	var total float64
	for _, s := range samples {
		switch s.Value.Kind() {
		case rtmetrics.KindUint64:
			total += float64(s.Value.Uint64())
		case rtmetrics.KindFloat64:
			total += s.Value.Float64()
		case rtmetrics.KindBad, rtmetrics.KindFloat64Histogram:
		}
	}
	return total
}

// RegisterPool adds the metrics of the database pool, read from sql.DBStats
// when scraped: the connections by state (open, in_use, idle) and the number of
// waits for a connection and the seconds spent waiting.
func (s *Set) RegisterPool(stats func() sql.DBStats) error {
	err := s.Registry.NewFunc(KindGauge, NameDBConnections, "Connections of the database pool, by state.",
		[]string{"state"}, func() []Sample {
			st := stats()
			return []Sample{
				{Labels: []string{"open"}, Value: float64(st.OpenConnections)},
				{Labels: []string{"in_use"}, Value: float64(st.InUse)},
				{Labels: []string{"idle"}, Value: float64(st.Idle)},
			}
		})
	if err != nil {
		return err
	}
	if err := s.Registry.NewCounterFunc(NameDBWait, "Times a request waited for a database connection.",
		func() float64 { return float64(stats().WaitCount) }); err != nil {
		return err
	}
	return s.Registry.NewCounterFunc(NameDBWaitSeconds, "Seconds spent waiting for a database connection.",
		func() float64 { return stats().WaitDuration.Seconds() })
}

// RegisterCalls adds clowder_calls_active, the calls whose home is this
// process, read from active when scraped.
func (s *Set) RegisterCalls(active func() int) error {
	return s.Registry.NewGaugeFunc(NameCallsActive, "Calls whose media this process holds.",
		func() float64 { return float64(active()) })
}
