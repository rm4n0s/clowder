package cluster

import (
	"context"
	"strconv"
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
// error holds the URL without its password. The client is both the client of
// the bridge and the backend of the login counter.
func openRedis(ctx context.Context, rawURL string) (*redisClient, error) {
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
	// TagRedisCountFailed is the tag of the failure to count the failed logins of a key in Redis.
	TagRedisCountFailed = "RedisCountFailed"
	// TagRedisAddFailed is the tag of the failure to record a failed login in Redis.
	TagRedisAddFailed = "RedisAddFailed"
	// TagRedisClearFailed is the tag of the failure to forget the failed logins of a key in Redis.
	TagRedisClearFailed = "RedisClearFailed"
)

// count implements counterBackend with one MULTI/EXEC transaction: it removes
// the failures that left the window and counts the rest.
func (c *redisClient) count(ctx context.Context, key string, cutoff int64) (int, error) {
	pipe := c.rc.TxPipeline()
	pipe.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(cutoff, 10))
	card := pipe.ZCard(ctx, key)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, errors.NewErr(TagRedisCountFailed, err)
	}
	return int(card.Val()), nil
}

// add implements counterBackend with one MULTI/EXEC transaction.
func (c *redisClient) add(ctx context.Context, key, member string, now, cutoff int64, keep int, ttl time.Duration) error {
	pipe := c.rc.TxPipeline()
	pipe.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(cutoff, 10))
	pipe.ZAdd(ctx, key, redis.Z{Score: float64(now), Member: member})
	// Rank 0 is the oldest: this drops all but the latest keep members.
	pipe.ZRemRangeByRank(ctx, key, 0, int64(-keep-1))
	pipe.PExpire(ctx, key, ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return errors.NewErr(TagRedisAddFailed, err)
	}
	return nil
}

// clear implements counterBackend.
func (c *redisClient) clear(ctx context.Context, key string) error {
	if err := c.rc.Del(ctx, key).Err(); err != nil {
		return errors.NewErr(TagRedisClearFailed, err)
	}
	return nil
}
