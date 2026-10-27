package cluster

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/errors"
)

// connectTimeout bounds the connecting: the PING and the confirmation of the
// subscription.
const connectTimeout = 5 * time.Second

// redisClient is the client over go-redis.
type redisClient struct {
	rc *redis.Client
}

// redisSubscription is the subscription over go-redis. Its PubSub makes the
// connection again by itself when it breaks and subscribes again; the next
// receive then yields a confirmation, which is the signal of a reconnect.
type redisSubscription struct {
	ps *redis.PubSub
}

// openRedis connects to the server at rawURL and checks it with a PING. The
// error holds the URL without its password.
func openRedis(ctx context.Context, rawURL string) (client, error) {
	opt, err := redis.ParseURL(rawURL)
	if err != nil {
		// The error of ParseURL may quote the URL, password included.
		return nil, errors.New(TagRedisURLUnparseable, "the Redis URL cannot be parsed",
			"redisURL", config.RedactURL(rawURL))
	}
	rc := redis.NewClient(opt)
	pctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err := rc.Ping(pctx).Err(); err != nil {
		appErr := errors.NewErr(TagRedisPingFailed, err).SetMetadata("redisURL", config.RedactURL(rawURL))
		if closeErr := rc.Close(); closeErr != nil {
			appErr = appErr.SetMetadata("closeError", closeErr.Error())
		}
		return nil, appErr
	}
	return &redisClient{rc: rc}, nil
}

// publish sends the payload to the channel.
func (c *redisClient) publish(ctx context.Context, channel string, payload []byte) error {
	if err := c.rc.Publish(ctx, channel, payload).Err(); err != nil {
		return errors.NewErr(TagRedisPublishFailed, err).SetMetadata("channel", channel)
	}
	return nil
}

// subscribe listens to the channel and waits for the confirmation of Redis.
// That first confirmation is consumed here: any later one is a reconnect.
func (c *redisClient) subscribe(ctx context.Context, channel string) (subscription, error) {
	ps := c.rc.Subscribe(ctx, channel)
	sctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	for {
		msg, err := ps.ReceiveTimeout(sctx, connectTimeout)
		if err != nil {
			appErr := errors.NewErr(TagRedisSubscribeFailed, err).SetMetadata("channel", channel)
			if closeErr := ps.Close(); closeErr != nil {
				appErr = appErr.SetMetadata("closeError", closeErr.Error())
			}
			return nil, appErr
		}
		if sub, ok := msg.(*redis.Subscription); ok && sub.Kind == "subscribe" {
			return &redisSubscription{ps: ps}, nil
		}
	}
}

// close closes the connection pool.
func (c *redisClient) close() error {
	if err := c.rc.Close(); err != nil {
		return errors.NewErr(TagRedisCloseFailed, err)
	}
	return nil
}

// receive returns the next message, or the signal that the subscription was
// made again. Pings and other replies are skipped.
func (s *redisSubscription) receive(ctx context.Context) (incoming, error) {
	for {
		msg, err := s.ps.Receive(ctx)
		if err != nil {
			return incoming{}, errors.NewErr(TagRedisReceiveFailed, err)
		}
		switch m := msg.(type) {
		case *redis.Message:
			return incoming{payload: []byte(m.Payload)}, nil
		case *redis.Subscription:
			if m.Kind == "subscribe" {
				return incoming{resubscribed: true}, nil
			}
		}
	}
}

// close ends the subscription; a receive that waits returns an error.
func (s *redisSubscription) close() error {
	if err := s.ps.Close(); err != nil {
		return errors.NewErr(TagRedisCloseFailed, err)
	}
	return nil
}

const (
	// TagRedisURLUnparseable is the tag of a Redis URL that go-redis cannot parse.
	TagRedisURLUnparseable = "RedisURLUnparseable"
	// TagRedisPingFailed is the tag of a Redis server that does not answer the first PING.
	TagRedisPingFailed = "RedisPingFailed"
	// TagRedisSubscribeFailed is the tag of the failure to subscribe to the events channel.
	TagRedisSubscribeFailed = "RedisSubscribeFailed"
	// TagRedisPublishFailed is the tag of the failure to publish an event to Redis.
	TagRedisPublishFailed = "RedisPublishFailed"
	// TagRedisReceiveFailed is the tag of the failure to receive from the subscription, for example when the connection broke.
	TagRedisReceiveFailed = "RedisReceiveFailed"
	// TagRedisCloseFailed is the tag of the failure to close the Redis client or its subscription.
	TagRedisCloseFailed = "RedisCloseFailed"
)
