package main

import (
	"flag"
	"io"
)

// newFlagSet declares the flags of the command into o.
func newFlagSet(o *options, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("clowder-perf", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.base, "base", "", "base URL of the running server, such as http://127.0.0.1:8080")
	fs.StringVar(&o.user, "user", defaultUser, "user to log in as")
	fs.StringVar(&o.password, "password", defaultDemoPassword, "password of the user")
	fs.Uint64Var(&o.roomID, "room", defaultRoomID, "ID of the room whose streams are opened (general is room 1 in every demo)")
	fs.IntVar(&o.streams, "streams", defaultStreams, "number of streams to open")
	fs.IntVar(&o.rounds, "rounds", defaultRounds, "number of probe posts; each gives one sample per stream")
	fs.IntVar(&o.pid, "pid", 0, "process ID of the server, to read its memory from /proc/<pid>/status (0: skip)")
	fs.StringVar(&o.base2, "base2", "", "base URL of a second process of the same installation (several processes with Redis, SPEC §8.1): every other stream is opened on it, and the probe posts are still written on -base")
	fs.IntVar(&o.pid2, "pid2", 0, "process ID of the second server, to read its memory (0: skip); needs -base2")
	fs.DurationVar(&o.settle, "settle", defaultSettle, "how long the streams rest before the memory is read")
	fs.DurationVar(&o.ready, "ready-timeout", defaultReadyTimeout, "how long to wait for the server to answer")
	fs.DurationVar(&o.timeout, "timeout", defaultRoundTimeout, "how long a probe post has to reach every stream")
	fs.BoolVar(&o.freePort, "free-port", false, "print a free TCP port and exit")
	return fs
}
