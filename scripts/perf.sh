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
# usage: scripts/perf.sh [large-demo-data-dir]
# environment: STREAMS (default 200), ROUNDS (probe posts, default 5)
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
streams=${STREAMS:-200}
rounds=${ROUNDS:-5}
work=$(mktemp -d "${TMPDIR:-/tmp}/clowder-perf.XXXXXX")
server_pid=""

cleanup() {
	local status=$?
	trap - EXIT INT TERM
	if [[ -n $server_pid ]] && kill -0 "$server_pid" 2>/dev/null; then
		kill -TERM "$server_pid" 2>/dev/null || true
		for _ in $(seq 1 50); do
			kill -0 "$server_pid" 2>/dev/null || break
			sleep 0.2
		done
		kill -KILL "$server_pid" 2>/dev/null || true
		wait "$server_pid" 2>/dev/null || true
	fi
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
	[[ -f $1/clowder.db ]] || { echo "perf.sh: $1 has no clowder.db" >&2; exit 2; }
	echo "copying the large demo in $1"
	cp -r "$1" "$data"
else
	echo "making the large demo"
	started=$(date +%s.%N)
	"$work/clowder" demo -data "$data" -size large >"$work/demo.log" 2>"$work/demo.err"
	finished=$(date +%s.%N)
	grep '^large demo' "$work/demo.log"
	printf 'demo built in %.1f s\n' "$(echo "$finished - $started" | bc)"
fi

port=$("$work/clowder-perf" -free-port)
echo "starting the server on 127.0.0.1:$port"
"$work/clowder" serve -addr "127.0.0.1:$port" -data "$data" -log-level warn >"$work/serve.log" 2>&1 &
server_pid=$!

echo "measuring with $streams streams and $rounds probe posts"
"$work/clowder-perf" -base "http://127.0.0.1:$port" -pid "$server_pid" -streams "$streams" -rounds "$rounds"
