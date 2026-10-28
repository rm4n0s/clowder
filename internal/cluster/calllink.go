package cluster

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rm4n0s/clowder/internal/calls"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/errors"
)

const (
	// CallsChannelPrefix starts the Redis channel that carries the signalling
	// messages addressed to one process: clowder:calls:<process id> (SPEC D65).
	CallsChannelPrefix = "clowder:calls:"
	// ProcessKeyPrefix starts the key with which a process announces that it
	// runs: clowder:process:<process id>, whose value is the media address of
	// the process (SPEC D65).
	ProcessKeyPrefix = "clowder:process:"
	// ProcessKeyTTL is how long the key of a process lives in Redis without a
	// refresh. A process that dies disappears from Redis after it, and the
	// calls it held are ended (SPEC D65).
	ProcessKeyTTL = 15 * time.Second
	// ProcessKeyRefresh is how often a process refreshes its key.
	ProcessKeyRefresh = 5 * time.Second
	// callMessagesQueue is how many envelopes wait for the media server of the
	// process; the receiver waits when it is full.
	callMessagesQueue = 256
	// callLinkTimeout bounds the removal of the key of the process at Close.
	callLinkTimeout = 2 * time.Second
)

// CallsChannel returns the Redis channel of the call messages addressed to the
// process.
func CallsChannel(process string) string {
	return CallsChannelPrefix + process
}

// ProcessKey returns the Redis key with which the process announces itself.
func ProcessKey(process string) string {
	return ProcessKeyPrefix + process
}

// linkBackend is the part of the Redis client that CallLink uses.
// redisClient is the implementation over go-redis; tests use an in-memory
// fake. Every error a backend returns is already tagged.
type linkBackend interface {
	// publishCount sends the payload to the channel and returns how many
	// subscribers got it.
	publishCount(ctx context.Context, channel string, payload []byte) (int64, error)
	// subscribe listens to the channel and returns once Redis confirmed it.
	subscribe(ctx context.Context, channel string) (subscription, error)
	// setKey writes the key with a time to live.
	setKey(ctx context.Context, key, value string, ttl time.Duration) error
	// keyExists reports whether the key is there.
	keyExists(ctx context.Context, key string) (bool, error)
	// deleteKey removes the key; a key that is not there is not an error.
	deleteKey(ctx context.Context, key string) error
	// close closes the backend.
	close() error
}

// CallLink is what the calls of a process share with the other processes
// through Redis (SPEC D65). It implements calls.Relay, which carries the
// signalling of a participant whose socket reached a process other than the
// home of the call, and the service of the calls asks it whether the home of a
// call is alive (Alive). A process announces itself with Announce, which keeps
// the key clowder:process:<id> alive from a goroutine until Close. Make it with
// NewCallLink and stop it with Close. It is safe for concurrent use.
//
// No media goes through Redis: only the JSON of the signalling messages does,
// on the channel clowder:calls:<process id> of the process they are for.
type CallLink struct {
	id      string
	logger  *slog.Logger
	backend linkBackend
	sub     subscription
	ttl     time.Duration
	refresh time.Duration
	msgs    chan calls.Envelope

	// run is the context of the goroutines; Close cancels it.
	run       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
	closeErr  error

	mu        sync.Mutex
	announced bool

	receiveLog, malformedLog, refreshLog *logGate
}

// CallLinkConfig holds the settings of NewCallLink.
type CallLinkConfig struct {
	// RedisURL is the redis:// or rediss:// URL of the server.
	RedisURL string
	// ProcessID is the ID of this process (16 hexadecimal characters), the
	// home that the calls it starts record.
	ProcessID string
	// KeyTTL and KeyRefresh set the life of the key of the process and how often
	// it is refreshed; zero takes ProcessKeyTTL and ProcessKeyRefresh. Tests
	// make them short.
	KeyTTL     time.Duration
	KeyRefresh time.Duration
	// Logger receives the failures; nil logs nothing.
	Logger *slog.Logger
}

// NewCallLink connects to Redis, checks the connection with a PING and
// subscribes to the channel of the process. ctx bounds the connecting only;
// the goroutines run until Close. The error never holds the password of the
// URL. It fails with TagRedisURLUnparseable, TagRedisPingFailed and
// TagRedisSubscribeFailed.
func NewCallLink(ctx context.Context, cfg CallLinkConfig) (*CallLink, error) {
	c, err := openRedis(ctx, cfg.RedisURL)
	if err != nil {
		return nil, err
	}
	return newCallLink(ctx, c, cfg)
}

// newCallLink makes a link over a backend, which it owns from then on: when it
// fails, it has closed the backend.
func newCallLink(ctx context.Context, b linkBackend, cfg CallLinkConfig) (*CallLink, error) {
	if cfg.KeyTTL <= 0 {
		cfg.KeyTTL = ProcessKeyTTL
	}
	if cfg.KeyRefresh <= 0 {
		cfg.KeyRefresh = ProcessKeyRefresh
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	sub, err := b.subscribe(ctx, CallsChannel(cfg.ProcessID))
	if err != nil {
		if closeErr := b.close(); closeErr != nil {
			cfg.Logger.Warn("closing the Redis client failed", "err", closeErr)
		}
		return nil, err
	}
	// The goroutines outlive ctx, which only bounded the connecting.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	l := &CallLink{
		id: cfg.ProcessID, logger: cfg.Logger, backend: b, sub: sub, ttl: cfg.KeyTTL, refresh: cfg.KeyRefresh,
		msgs: make(chan calls.Envelope, callMessagesQueue), run: runCtx, cancel: cancel,
		receiveLog:   newLogGate(clock.Real(), logEvery),
		malformedLog: newLogGate(clock.Real(), logEvery),
		refreshLog:   newLogGate(clock.Real(), logEvery),
	}
	l.wg.Add(1)
	go l.runReceiver()
	return l, nil
}

// ID returns the ID of the process.
func (l *CallLink) ID() string { return l.id }

// Messages implements calls.Relay: the envelopes addressed to this process. The
// channel is closed by Close.
func (l *CallLink) Messages() <-chan calls.Envelope { return l.msgs }

// Send implements calls.Relay: it publishes the envelope on the channel of the
// process to. It fails with TagCallEnvelopeEncodeFailed, with
// TagRedisPublishFailed, and with TagCallNobodyListening when no process is
// subscribed there, which is how the sender learns that the other process is
// gone.
func (l *CallLink) Send(ctx context.Context, to string, env calls.Envelope) error {
	payload, err := json.Marshal(env)
	if err != nil {
		return errors.NewErr(TagCallEnvelopeEncodeFailed, err).SetMetadata("kind", env.Kind)
	}
	n, err := l.backend.publishCount(ctx, CallsChannel(to), payload)
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New(TagCallNobodyListening, "no process listens for the call messages", "to", to, "kind", env.Kind)
	}
	return nil
}

// Announce writes the key of the process, with the media address as its value,
// and starts the goroutine that refreshes it until Close (SPEC D65). The other
// processes then see the process as alive. A second call does nothing. It fails
// with TagRedisProcessFailed.
func (l *CallLink) Announce(ctx context.Context, addr string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.announced {
		return nil
	}
	if err := l.backend.setKey(ctx, ProcessKey(l.id), addr, l.ttl); err != nil {
		return err
	}
	l.announced = true
	l.wg.Add(1)
	go l.runRefresher(addr)
	return nil
}

// Alive reports whether the process announced itself and its key has not
// expired. It fails with TagRedisProcessFailed when Redis cannot be read.
func (l *CallLink) Alive(ctx context.Context, process string) (bool, error) {
	return l.backend.keyExists(ctx, ProcessKey(process))
}

// Close stops the receiver and the refresher, removes the key of the process
// (so the others see it gone at once) and closes the connection to Redis. It is
// safe to call more than once; calls after the first return its result.
func (l *CallLink) Close() error {
	return l.shutdown(true)
}

// Abandon stops the link as a process that crashed leaves it: the goroutines
// and the connection end, and the key is left to expire after its time to
// live. Only a test of that expiry calls it.
func (l *CallLink) Abandon() error {
	return l.shutdown(false)
}

// shutdown stops the link, and removes the key when deleteKey is set.
func (l *CallLink) shutdown(deleteKey bool) error {
	l.closeOnce.Do(func() {
		l.cancel()
		// Closing the subscription wakes the receiver, which may be waiting
		// on the connection.
		if err := l.sub.close(); err != nil {
			l.logger.Warn("closing the Redis subscription of the calls failed", "err", err)
		}
		l.wg.Wait()
		l.mu.Lock()
		announced := l.announced
		l.mu.Unlock()
		if deleteKey && announced {
			ctx, cancel := context.WithTimeout(context.Background(), callLinkTimeout)
			if err := l.backend.deleteKey(ctx, ProcessKey(l.id)); err != nil {
				l.logger.Warn("removing the key of this process from Redis failed", "err", err)
			}
			cancel()
		}
		l.closeErr = l.backend.close()
	})
	return l.closeErr
}

// runRefresher writes the key of the process again every refresh until the link
// is closed. A failure is logged and tried again at the next tick.
func (l *CallLink) runRefresher(addr string) {
	defer l.wg.Done()
	t := time.NewTicker(l.refresh)
	defer t.Stop()
	for {
		select {
		case <-l.run.Done():
			return
		case <-t.C:
		}
		wctx, cancel := context.WithTimeout(l.run, l.refresh)
		err := l.backend.setKey(wctx, ProcessKey(l.id), addr, l.ttl)
		cancel()
		if err != nil && l.run.Err() == nil {
			if ok, skipped := l.refreshLog.allow(); ok {
				l.logger.Warn("refreshing the key of this process in Redis failed", "err", err, "skipped_logs", skipped)
			}
		}
	}
}

// runReceiver reads the channel of the process and queues the envelopes for the
// media server until the link is closed. The queue is closed when it ends.
func (l *CallLink) runReceiver() {
	ctx := l.run
	defer l.wg.Done()
	defer close(l.msgs)
	backoff := minBackoff
	for ctx.Err() == nil {
		in, err := l.sub.receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if ok, skipped := l.receiveLog.allow(); ok {
				l.logger.Warn("receiving call messages from Redis failed; trying again", "err", err, "skipped_logs", skipped)
			}
			if !sleep(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		backoff = minBackoff
		if in.resubscribed {
			l.logger.Warn("the connection to Redis came back; call messages may have been missed")
			continue
		}
		env, err := decodeEnvelope(in.payload)
		if err != nil {
			if ok, skipped := l.malformedLog.allow(); ok {
				l.logger.Warn("ignoring a malformed call message from Redis", "err", err, "skipped_logs", skipped)
			}
			continue
		}
		select {
		case l.msgs <- env:
		case <-ctx.Done():
			return
		}
	}
}

// decodeEnvelope reads a message of a calls channel. The error never holds the
// payload.
func decodeEnvelope(payload []byte) (calls.Envelope, error) {
	var env calls.Envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return calls.Envelope{}, errors.NewErr(TagCallEnvelopeMalformed, err).SetMetadata("bytes", len(payload))
	}
	switch env.Kind {
	case calls.EnvJoin:
		if env.Join == nil {
			return calls.Envelope{}, errors.New(TagCallEnvelopeMalformed, "a join has no call", "bytes", len(payload))
		}
	case calls.EnvClient, calls.EnvServer:
		if len(env.Message) == 0 {
			return calls.Envelope{}, errors.New(TagCallEnvelopeMalformed, "a message has no content", "kind", env.Kind, "bytes", len(payload))
		}
	case calls.EnvLeave, calls.EnvClose:
	default:
		return calls.Envelope{}, errors.New(TagCallEnvelopeMalformed, "the kind is not one of the relay", "bytes", len(payload))
	}
	if env.From == "" || env.CallID == 0 || env.Socket == "" {
		return calls.Envelope{}, errors.New(TagCallEnvelopeMalformed, "the message has no sender, call or socket",
			"kind", env.Kind, "bytes", len(payload))
	}
	return env, nil
}

// publishCount implements linkBackend.
func (c *redisClient) publishCount(ctx context.Context, channel string, payload []byte) (int64, error) {
	n, err := c.rc.Publish(ctx, channel, payload).Result()
	if err != nil {
		return 0, errors.NewErr(TagRedisPublishFailed, err).SetMetadata("channel", channel)
	}
	return n, nil
}

// setKey implements linkBackend.
func (c *redisClient) setKey(ctx context.Context, key, value string, ttl time.Duration) error {
	if err := c.rc.Set(ctx, key, value, ttl).Err(); err != nil {
		return errors.NewErr(TagRedisProcessFailed, err).SetMetadata("op", "set")
	}
	return nil
}

// keyExists implements linkBackend.
func (c *redisClient) keyExists(ctx context.Context, key string) (bool, error) {
	n, err := c.rc.Exists(ctx, key).Result()
	if err != nil {
		return false, errors.NewErr(TagRedisProcessFailed, err).SetMetadata("op", "exists")
	}
	return n > 0, nil
}

// deleteKey implements linkBackend.
func (c *redisClient) deleteKey(ctx context.Context, key string) error {
	if err := c.rc.Del(ctx, key).Err(); err != nil && err != redis.Nil {
		return errors.NewErr(TagRedisProcessFailed, err).SetMetadata("op", "delete")
	}
	return nil
}

const (
	// TagCallEnvelopeEncodeFailed is the tag of a call message that could not
	// be encoded for Redis.
	TagCallEnvelopeEncodeFailed = "CallEnvelopeEncodeFailed"
	// TagCallEnvelopeMalformed is the tag of a message of a calls channel that
	// is not a valid envelope.
	TagCallEnvelopeMalformed = "CallEnvelopeMalformed"
	// TagCallNobodyListening is the tag of a call message sent to a process
	// whose channel has no subscriber: the process is not running.
	TagCallNobodyListening = "CallNobodyListening"
	// TagRedisProcessFailed is the tag of a command that writes, reads or
	// removes the key of a process in Redis; the metadata "op" names the
	// operation (set, exists or delete).
	TagRedisProcessFailed = "RedisProcessFailed"
)
