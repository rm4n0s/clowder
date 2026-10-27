// Package cluster is what several Clowder processes share through Redis
// (SPEC D41, §8.1). Bridge carries the events of the hub of one process to the
// hubs of the others, and LoginCounter keeps the failed logins of the login
// limiter, so that its limit holds across the processes. It is the only
// package that imports go-redis; the rest of Clowder sees the Redis client
// through the small interfaces of this package and of the packages that use it.
package cluster
