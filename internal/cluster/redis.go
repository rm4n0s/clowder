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
	// TagRedisPresenceFailed is the tag of the failure of a command that records or reads who is online in Redis; the metadata "op" names the operation (add, remove, refresh, drop or online).
	TagRedisPresenceFailed = "RedisPresenceFailed"
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

// presenceKey returns the Redis key of the set of online users of a process.
func presenceKey(process string) string {
	return presenceKeyPrefix + process
}

// presenceMembers returns the user ids as the strings stored in the sets.
func presenceMembers(userIDs []uint64) []any {
	members := make([]any, len(userIDs))
	for i, id := range userIDs {
		members[i] = strconv.FormatUint(id, 10)
	}
	return members
}

// addUser implements presenceBackend: it reads the clock of Redis, then adds the
// user, sets the time to live and writes the heartbeat in one MULTI/EXEC.
func (c *redisClient) addUser(ctx context.Context, process string, userID uint64, ttl time.Duration) error {
	now, err := c.rc.Time(ctx).Result()
	if err != nil {
		return errors.NewErr(TagRedisPresenceFailed, err).SetMetadata("op", "add")
	}
	key := presenceKey(process)
	pipe := c.rc.TxPipeline()
	pipe.SAdd(ctx, key, strconv.FormatUint(userID, 10))
	pipe.PExpire(ctx, key, ttl)
	pipe.ZAdd(ctx, presenceProcessesKey, redis.Z{Score: float64(now.UnixMilli()), Member: process})
	if _, err := pipe.Exec(ctx); err != nil {
		return errors.NewErr(TagRedisPresenceFailed, err).SetMetadata("op", "add")
	}
	return nil
}

// removeUser implements presenceBackend.
func (c *redisClient) removeUser(ctx context.Context, process string, userID uint64) error {
	if err := c.rc.SRem(ctx, presenceKey(process), strconv.FormatUint(userID, 10)).Err(); err != nil {
		return errors.NewErr(TagRedisPresenceFailed, err).SetMetadata("op", "remove")
	}
	return nil
}

// refreshProcess implements presenceBackend: it reads the clock of Redis, then
// replaces the set, writes the heartbeat and forgets the old processes in one
// MULTI/EXEC.
func (c *redisClient) refreshProcess(ctx context.Context, process string, users []uint64, ttl time.Duration) error {
	now, err := c.rc.Time(ctx).Result()
	if err != nil {
		return errors.NewErr(TagRedisPresenceFailed, err).SetMetadata("op", "refresh")
	}
	ms := now.UnixMilli()
	key := presenceKey(process)
	pipe := c.rc.TxPipeline()
	pipe.Del(ctx, key)
	if len(users) > 0 {
		pipe.SAdd(ctx, key, presenceMembers(users)...)
		pipe.PExpire(ctx, key, ttl)
	}
	pipe.ZAdd(ctx, presenceProcessesKey, redis.Z{Score: float64(ms), Member: process})
	pipe.ZRemRangeByScore(ctx, presenceProcessesKey, "-inf", strconv.FormatInt(ms-2*ttl.Milliseconds(), 10))
	if _, err := pipe.Exec(ctx); err != nil {
		return errors.NewErr(TagRedisPresenceFailed, err).SetMetadata("op", "refresh")
	}
	return nil
}

// dropProcess implements presenceBackend with one MULTI/EXEC transaction.
func (c *redisClient) dropProcess(ctx context.Context, process string) error {
	pipe := c.rc.TxPipeline()
	pipe.Del(ctx, presenceKey(process))
	pipe.ZRem(ctx, presenceProcessesKey, process)
	if _, err := pipe.Exec(ctx); err != nil {
		return errors.NewErr(TagRedisPresenceFailed, err).SetMetadata("op", "drop")
	}
	return nil
}

// onlineUsers implements presenceBackend: it reads the clock of Redis, lists the
// processes whose heartbeat is newer than ttl and asks each of their sets
// which of the users it holds, all sets in one pipeline.
func (c *redisClient) onlineUsers(ctx context.Context, userIDs []uint64, ttl time.Duration) (map[uint64]bool, error) {
	out := make(map[uint64]bool, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	now, err := c.rc.Time(ctx).Result()
	if err != nil {
		return nil, errors.NewErr(TagRedisPresenceFailed, err).SetMetadata("op", "online")
	}
	// The minimum is exclusive: a heartbeat of exactly ttl ago is dead.
	processes, err := c.rc.ZRangeArgs(ctx, redis.ZRangeArgs{
		Key:     presenceProcessesKey,
		ByScore: true,
		Start:   "(" + strconv.FormatInt(now.UnixMilli()-ttl.Milliseconds(), 10),
		Stop:    "+inf",
	}).Result()
	if err != nil {
		return nil, errors.NewErr(TagRedisPresenceFailed, err).SetMetadata("op", "online")
	}
	if len(processes) == 0 {
		return out, nil
	}
	members := presenceMembers(userIDs)
	pipe := c.rc.Pipeline()
	cmds := make([]*redis.BoolSliceCmd, len(processes))
	for i, process := range processes {
		cmds[i] = pipe.SMIsMember(ctx, presenceKey(process), members...)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, errors.NewErr(TagRedisPresenceFailed, err).SetMetadata("op", "online")
	}
	for _, cmd := range cmds {
		for i, ok := range cmd.Val() {
			if ok {
				out[userIDs[i]] = true
			}
		}
	}
	return out, nil
}
