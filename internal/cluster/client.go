package cluster

import "context"

// EventsChannel is the Redis channel that carries the live events of every
// process (SPEC D41).
const EventsChannel = "clowder:events"

// client is the part of a Redis client that the bridge uses. redisClient is the
// implementation over go-redis; tests use an in-memory fake. Every error a
// client returns is already tagged.
type client interface {
	// publish sends the payload to the channel.
	publish(ctx context.Context, channel string, payload []byte) error
	// subscribe listens to the channel. It returns once Redis confirmed the
	// subscription, so no message published afterwards is missed.
	subscribe(ctx context.Context, channel string) (subscription, error)
	// close closes the client. Subscriptions are closed first.
	close() error
}

// subscription is the stream of one subscribed channel. receive is called
// from one goroutine; close may be called from another and makes a receive
// that is waiting return an error.
type subscription interface {
	// receive blocks until the next message arrives, or until the connection
	// breaks (an error: the next call tries again, and the implementation
	// reconnects by itself) or the subscription is closed.
	receive(ctx context.Context) (incoming, error)
	// close ends the subscription.
	close() error
}

// incoming is what a subscription yields: a message, or the signal that the
// subscription was made again after a broken connection.
type incoming struct {
	// payload is the message; nil when resubscribed is true.
	payload []byte
	// resubscribed says the connection broke and came back, and the channel
	// was subscribed again. Messages sent in between were missed.
	resubscribed bool
}
