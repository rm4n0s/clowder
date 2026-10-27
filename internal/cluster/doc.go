// Package cluster is what several Clowder processes share through Redis
// (SPEC D41, §8.1). Bridge carries the events of the hub of one process to the
// hubs of the others. It is the only package that imports go-redis; the rest of
// Clowder sees the Redis client through the small interfaces of this package.
package cluster
