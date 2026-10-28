package cluster

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/metrics"
	"github.com/rm4n0s/errors"
)

const (
	// queueSize is the number of events that wait to be sent to Redis. When it
	// is full the event is not sent to Redis (the local hub still gets it).
	queueSize = 1024
	// publishTimeout is how long one publish to Redis may take.
	publishTimeout = 5 * time.Second
	// flushTimeout is how long Close lets the events still queued, or being
	// sent, take to reach Redis.
	flushTimeout = time.Second
	// logEvery is the shortest time between two log lines of the same kind
	// about a failing Redis, so that an outage does not flood the log.
	logEvery = time.Second
	// minBackoff and maxBackoff bound the pause of the receiver between two
	// failed attempts to receive.
	minBackoff = 100 * time.Millisecond
	maxBackoff = 2 * time.Second
	// originBytes is the number of random bytes of the process id.
	originBytes = 16
)

// Bridge carries the events of the hub of this process to the hubs of the
// other processes that share its Redis, and theirs to this one (SPEC D41). It
// is the Publisher of every service when Clowder runs on several processes.
// Make it with NewBridge and stop it with Close. It is safe for concurrent
// use.
//
// Publish hands the event to the local hub at once and queues it for Redis. A
// sender goroutine publishes the queue to EventsChannel; a receiver goroutine
// reads the channel and publishes the events of other processes to the local
// hub. When the connection to Redis broke and came back, the receiver closes
// every subscription of the local hub as dropped (Hub.DropAll), because
// events may have been missed.
type Bridge struct {
	origin string
	hub    *live.Hub
	logger *slog.Logger
	client client
	sub    subscription
	// marshal encodes a message; json.Marshal, except in a test.
	marshal func(any) ([]byte, error)

	queue chan live.Event
	// cancel stops the sender and the receiver. cancelSend cancels the
	// publishes in progress: Close does it flushTimeout after cancel, so that
	// the events queued at Close still get that long to reach Redis.
	cancel     context.CancelFunc
	cancelSend context.CancelFunc
	wg         sync.WaitGroup

	closeOnce sync.Once
	closed    atomic.Bool
	lost      atomic.Uint64
	metrics   BridgeMetrics

	// One gate per kind of log line, so that an outage of one kind does not
	// hide the others.
	lostLog, sendLog, receiveLog, malformedLog *logGate
}

// bridgeOptions are the settings of newBridge that tests change.
type bridgeOptions struct {
	// queueSize is the size of the queue to Redis.
	queueSize int
	// clock times the rate limit of the log lines.
	clock clock.Clock
	// marshal encodes the messages; nil means json.Marshal.
	marshal func(any) ([]byte, error)
	// random fills the bytes with random ones; nil means rand.Read.
	random func([]byte) (int, error)
	// metrics are the counters of the bridge; the zero value counts nothing.
	metrics BridgeMetrics
}

// BridgeMetrics are the counters of a Bridge (SPEC D60). A nil counter counts
// nothing.
type BridgeMetrics struct {
	// Reconnects counts the times the connection to Redis came back after a
	// break.
	Reconnects *metrics.Counter
	// Published counts the events the bridge sent to Redis.
	Published *metrics.Counter
	// Received counts the events of other processes the bridge delivered to
	// the local hub.
	Received *metrics.Counter
}

// BridgeOption changes how NewBridge makes the bridge.
type BridgeOption func(*bridgeOptions)

// WithMetrics makes the bridge count its work in m.
func WithMetrics(m BridgeMetrics) BridgeOption {
	return func(o *bridgeOptions) { o.metrics = m }
}

// NewBridge connects to Redis at redisURL (redis:// or rediss://), checks the
// connection with a PING, subscribes to EventsChannel and starts the sender
// and the receiver. ctx bounds the connecting only; the goroutines run until
// Close. The error never holds the password of the URL.
func NewBridge(ctx context.Context, redisURL string, hub *live.Hub, logger *slog.Logger, opts ...BridgeOption) (*Bridge, error) {
	c, err := openRedis(ctx, redisURL)
	if err != nil {
		return nil, err
	}
	var o bridgeOptions
	for _, opt := range opts {
		opt(&o)
	}
	return newBridge(ctx, c, hub, logger, o)
}

// newBridge makes a bridge over c and owns c from then on: when it fails, it
// has closed c.
func newBridge(ctx context.Context, c client, hub *live.Hub, logger *slog.Logger, opts bridgeOptions) (*Bridge, error) {
	if opts.queueSize <= 0 {
		opts.queueSize = queueSize
	}
	if opts.clock == nil {
		opts.clock = clock.Real()
	}
	if opts.marshal == nil {
		opts.marshal = json.Marshal
	}
	if opts.random == nil {
		opts.random = rand.Read
	}
	raw := make([]byte, originBytes)
	if _, err := opts.random(raw); err != nil {
		closeQuietly(c, logger)
		return nil, errors.NewErr(TagOriginFailed, err)
	}
	sub, err := c.subscribe(ctx, EventsChannel)
	if err != nil {
		closeQuietly(c, logger)
		return nil, err
	}
	// The goroutines outlive ctx, which only bounded the connecting.
	base := context.WithoutCancel(ctx)
	runCtx, cancel := context.WithCancel(base)
	sendCtx, cancelSend := context.WithCancel(base)
	b := &Bridge{
		origin:       hex.EncodeToString(raw),
		hub:          hub,
		logger:       logger,
		client:       c,
		sub:          sub,
		marshal:      opts.marshal,
		queue:        make(chan live.Event, opts.queueSize),
		cancel:       cancel,
		cancelSend:   cancelSend,
		metrics:      opts.metrics,
		lostLog:      newLogGate(opts.clock, logEvery),
		sendLog:      newLogGate(opts.clock, logEvery),
		receiveLog:   newLogGate(opts.clock, logEvery),
		malformedLog: newLogGate(opts.clock, logEvery),
	}
	b.wg.Add(2)
	go b.runSender(runCtx, sendCtx)
	go b.runReceiver(runCtx)
	return b, nil
}

// closeQuietly closes c after a failure that is being returned; a failure to
// close is logged.
func closeQuietly(c client, logger *slog.Logger) {
	if err := c.close(); err != nil {
		logger.Warn("closing the Redis client failed", "err", err)
	}
}

// Publish delivers the event to the local hub at once and queues it for the
// other processes. It never blocks: when the queue to Redis is full the event
// is not sent to Redis, it is counted (Lost) and logged, and the local hub
// still has it. After Close it only publishes to the local hub.
func (b *Bridge) Publish(ev live.Event) {
	b.hub.Publish(ev)
	if b.closed.Load() {
		return
	}
	select {
	case b.queue <- ev:
	default:
		b.lost.Add(1)
		if ok, skipped := b.lostLog.allow(); ok {
			b.logger.Warn("the queue of live events to Redis is full; the event only reached this process",
				"queue", cap(b.queue), "lost", b.lost.Load(), "skipped_logs", skipped)
		}
	}
}

// Lost returns the number of events that did not reach Redis since the
// bridge started: the queue was full or the publish failed. Other processes
// missed them.
func (b *Bridge) Lost() uint64 {
	return b.lost.Load()
}

// Close stops the sender (after it tried to send what is queued, for a
// second at most) and the receiver, waits for both to end and closes the
// connection to Redis. It is safe to call more than once and from several
// goroutines; calls after the first return nil. Call it before closing the
// hub.
func (b *Bridge) Close() error {
	var err error
	b.closeOnce.Do(func() {
		b.closed.Store(true)
		b.cancel()
		// Whatever is still in flight or queued gets flushTimeout to reach
		// Redis; then the publishes are cancelled.
		cut := time.AfterFunc(flushTimeout, b.cancelSend)
		defer cut.Stop()
		defer b.cancelSend()
		// Closing the subscription wakes the receiver, which may be waiting
		// on the connection.
		if subErr := b.sub.close(); subErr != nil {
			b.logger.Warn("closing the Redis subscription failed", "err", subErr)
		}
		b.wg.Wait()
		err = b.client.close()
	})
	return err
}

// runSender publishes the queue to Redis until ctx is done, then sends what is
// still queued. sendCtx bounds the publishes: it ends flushTimeout after ctx.
func (b *Bridge) runSender(ctx, sendCtx context.Context) {
	defer b.wg.Done()
	for {
		select {
		case <-ctx.Done():
			b.flush(sendCtx)
			return
		case ev := <-b.queue:
			b.send(sendCtx, ev)
		}
	}
}

// flush sends the queued events without waiting for new ones.
func (b *Bridge) flush(ctx context.Context) {
	for {
		select {
		case ev := <-b.queue:
			b.send(ctx, ev)
		default:
			return
		}
	}
}

// send publishes one event to Redis. A failure is counted and logged, and the
// event is dropped for the other processes.
func (b *Bridge) send(ctx context.Context, ev live.Event) {
	payload, err := b.marshal(wireMessage{Origin: b.origin, Event: wireOf(ev)})
	if err != nil {
		b.lost.Add(1)
		b.logger.Error("encoding a live event failed", "err", errors.NewErr(TagEventEncodeFailed, err).SetMetadata("kind", ev.Kind))
		return
	}
	pctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	if err := b.client.publish(pctx, EventsChannel, payload); err != nil {
		b.lost.Add(1)
		if ok, skipped := b.sendLog.allow(); ok {
			b.logger.Warn("sending a live event to Redis failed", "err", err, "lost", b.lost.Load(), "skipped_logs", skipped)
		}
		return
	}
	b.metrics.Published.Inc()
}

// runReceiver delivers the events of other processes to the local hub until
// ctx is done.
func (b *Bridge) runReceiver(ctx context.Context) {
	defer b.wg.Done()
	backoff := minBackoff
	for ctx.Err() == nil {
		in, err := b.sub.receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if ok, skipped := b.receiveLog.allow(); ok {
				b.logger.Warn("receiving live events from Redis failed; trying again", "err", err, "skipped_logs", skipped)
			}
			if !sleep(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		backoff = minBackoff
		if in.resubscribed {
			b.logger.Info("the connection to Redis came back; live subscriptions of this process are reset")
			b.hub.DropAll()
			b.metrics.Reconnects.Inc()
			continue
		}
		b.deliver(in.payload)
	}
}

// deliver publishes the event of a message to the local hub, unless this
// process sent it or it is malformed.
func (b *Bridge) deliver(payload []byte) {
	origin, ev, err := decodeMessage(payload)
	if err != nil {
		if ok, skipped := b.malformedLog.allow(); ok {
			b.logger.Warn("ignoring a malformed live event from Redis", "err", err, "skipped_logs", skipped)
		}
		return
	}
	if origin == b.origin {
		return
	}
	b.hub.Publish(ev)
	b.metrics.Received.Inc()
}

// sleep waits for d or for ctx to end; it reports whether d passed.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// logGate lets at most one log line through per interval.
type logGate struct {
	clock    clock.Clock
	interval time.Duration

	mu      sync.Mutex
	next    time.Time
	skipped uint64
}

// newLogGate makes a gate that is open at once.
func newLogGate(clk clock.Clock, interval time.Duration) *logGate {
	return &logGate{clock: clk, interval: interval}
}

// allow reports whether a line may be logged now, and how many were held back
// since the last one that was.
func (g *logGate) allow() (bool, uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.clock.Now()
	if now.Before(g.next) {
		g.skipped++
		return false, 0
	}
	skipped := g.skipped
	g.skipped = 0
	g.next = now.Add(g.interval)
	return true, skipped
}

const (
	// TagOriginFailed is the tag of the failure to read random bytes for the id of the process.
	TagOriginFailed = "OriginFailed"
	// TagEventEncodeFailed is the tag of an event that could not be encoded for Redis.
	TagEventEncodeFailed = "EventEncodeFailed"
)
