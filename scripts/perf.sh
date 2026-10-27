#!/usr/bin/env bash
# Measures two targets of SPEC §10 on the large demo and prints a summary:
#
#   - a new post reaching 200 open streams of its room (p50, p95, max)
#   - the memory of the idle server with those 200 streams open
#
# It builds clowder and clowder-perf, makes the large demo in a temporary
# directory (or copies the one given as the first argument: the data directory
# of an earlier `clowder demo -size large`, left unchanged), starts
# `clowder serve` on a free port, runs clowder-perf against it and stops
# everything it started, also when it fails or is interrupted.
#
# On PostgreSQL (step 17.1): DATABASE_URL is the postgres:// URL of an empty
# database, which the large demo then fills; with a data directory as the
# argument the database is taken as already filled by an earlier
# `clowder demo -size large -database-url ...` (the directory then only has the
# attachments, and is copied). With REDIS_URL also set and PROCESSES=2, a
# second `clowder serve` runs on the same database and data directory, 100 of
# the 200 streams are opened on each process (STREAMS/2) and the probe posts
# are written on the first: the fan-out across two processes through Redis.
# The URLs go to the programs in their environment, not as flags.
#
# usage: scripts/perf.sh [large-demo-data-dir]
# environment: STREAMS (default 200), ROUNDS (probe posts, default 5),
#   SETTLE (how long the streams rest before the memory is read, default 2s),
#   DATABASE_URL (PostgreSQL; default: SQLite), REDIS_URL, PROCESSES (1 or 2,
#   default 1; 2 needs DATABASE_URL and REDIS_URL)
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
streams=${STREAMS:-200}
rounds=${ROUNDS:-5}
database_url=${DATABASE_URL:-}
redis_url=${REDIS_URL:-}
processes=${PROCESSES:-1}
if [[ $processes != 1 && $processes != 2 ]]; then
	echo "perf.sh: PROCESSES must be 1 or 2" >&2
	exit 2
fi
if [[ $processes == 2 && ( -z $database_url || -z $redis_url ) ]]; then
	echo "perf.sh: PROCESSES=2 needs DATABASE_URL and REDIS_URL (several processes need PostgreSQL and Redis)" >&2
	exit 2
fi
work=$(mktemp -d "${TMPDIR:-/tmp}/clowder-perf.XXXXXX")
server_pid=""
server2_pid=""

# clowder_env runs a command with the database and Redis settings of this run
# in its environment (and none of the caller's). The servers are started with
# it directly, not through a function, so that $! is the process ID of the
# server itself, whose memory is measured.
clowder_env=(env -u CLOWDER_DATABASE_URL -u CLOWDER_REDIS_URL)
if [[ -n $database_url ]]; then
	clowder_env+=("CLOWDER_DATABASE_URL=$database_url")
fi
if [[ -n $redis_url ]]; then
	clowder_env+=("CLOWDER_REDIS_URL=$redis_url")
fi

cleanup() {
	local status=$?
	trap - EXIT INT TERM
	for pid in "$server_pid" "$server2_pid"; do
		[[ -n $pid ]] || continue
		if kill -0 "$pid" 2>/dev/null; then
			kill -TERM "$pid" 2>/dev/null || true
			for _ in $(seq 1 50); do
				kill -0 "$pid" 2>/dev/null || break
				sleep 0.2
			done
			kill -KILL "$pid" 2>/dev/null || true
			wait "$pid" 2>/dev/null || true
		fi
	done
	rm -rf "$work"
	exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

cd "$root"
echo "building clowder and clowder-perf"
go build -o "$work/clowder" ./cmd/clowder
go build -o "$work/clowder-perf" ./cmd/clowder-perf

data="$work/data"
if [[ $# -ge 1 ]]; then
	# On PostgreSQL the directory only holds the attachments (files/); the
	# database is the one DATABASE_URL names, filled by an earlier demo.
	if [[ -n $database_url ]]; then
		[[ -d $1 ]] || { echo "perf.sh: $1 is not a directory" >&2; exit 2; }
	else
		[[ -f $1/clowder.db ]] || { echo "perf.sh: $1 has no clowder.db" >&2; exit 2; }
	fi
	echo "copying the large demo in $1"
	cp -r "$1" "$data"
else
	echo "making the large demo"
	started=$(date +%s.%N)
	"${clowder_env[@]}" "$work/clowder" demo -data "$data" -size large >"$work/demo.log" 2>"$work/demo.err"
	finished=$(date +%s.%N)
	grep '^large demo' "$work/demo.log"
	printf 'demo built in %.1f s\n' "$(echo "$finished - $started" | bc)"
fi

perf_args=(-streams "$streams" -rounds "$rounds" -settle "${SETTLE:-2s}")
port=$("$work/clowder-perf" -free-port)
echo "starting the server on 127.0.0.1:$port"
"${clowder_env[@]}" "$work/clowder" serve -addr "127.0.0.1:$port" -data "$data" -log-level warn >"$work/serve.log" 2>&1 &
server_pid=$!
perf_args+=(-base "http://127.0.0.1:$port" -pid "$server_pid")
if [[ $processes == 2 ]]; then
	port2=$("$work/clowder-perf" -free-port)
	echo "starting the second server on 127.0.0.1:$port2 (same database, same data directory)"
	"${clowder_env[@]}" "$work/clowder" serve -addr "127.0.0.1:$port2" -data "$data" -log-level warn >"$work/serve2.log" 2>&1 &
	server2_pid=$!
	perf_args+=(-base2 "http://127.0.0.1:$port2" -pid2 "$server2_pid")
fi

echo "measuring with $streams streams and $rounds probe posts"
"$work/clowder-perf" "${perf_args[@]}"
