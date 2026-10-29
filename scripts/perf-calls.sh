#!/usr/bin/env bash
# Measures the CPU and the memory of the server during N simultaneous one-to-one
# calls (step 24.1, docs/PERFORMANCE.md): it builds clowder and clowder-perf,
# makes the large demo (200 users) once, and for each N starts a fresh
# `clowder serve` with the media port on the loopback address and its metrics,
# runs `clowder-perf -calls N` (2N pion clients, each sending VP8 at about
# 1 Mbit/s and Opus) for the duration, and stops the server. The clients run in
# the clowder-perf process, so the CPU and the memory read from /proc are the
# server's alone.
#
# usage: scripts/perf-calls.sh
# environment: CALLS (the numbers of calls to run one after the other, default
#   "1 10"), DURATION (default 60s), BITRATE (video bits per second of each
#   client, default 1000000), LARGE_DEMO (the data directory of an earlier
#   `clowder demo -size large`, left unchanged; default: made here)
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/clowder-perf-calls.XXXXXX")
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

clowder_env=(env -u CLOWDER_DATABASE_URL -u CLOWDER_REDIS_URL)
if [[ -n ${LARGE_DEMO:-} ]]; then
	[[ -f $LARGE_DEMO/clowder.db ]] || { echo "perf-calls.sh: $LARGE_DEMO has no clowder.db" >&2; exit 2; }
	large="$LARGE_DEMO"
else
	echo "making the large demo"
	large="$work/large"
	"${clowder_env[@]}" "$work/clowder" demo -data "$large" -size large >"$work/demo.log" 2>"$work/demo.err"
	grep '^large demo' "$work/demo.log" | head -3
fi

echo "machine: $(uname -sr), $(nproc) logical CPUs, $(awk '/MemTotal/ {printf "%.0f GiB", $2 / 1048576}' /proc/meminfo)"
for n in ${CALLS:-1 10}; do
	echo
	echo "== $n simultaneous calls, ${DURATION:-60s}; load average before: $(cut -d' ' -f1-3 /proc/loadavg)"
	data="$work/data-$n"
	cp -r "$large" "$data"
	port=$("$work/clowder-perf" -free-port)
	media=$("$work/clowder-perf" -free-port)
	metrics=$("$work/clowder-perf" -free-port)
	"${clowder_env[@]}" "$work/clowder" serve -addr "127.0.0.1:$port" -data "$data" -log-level warn \
		-media-addr "127.0.0.1:$media" -media-public-ip 127.0.0.1 -max-calls 50 \
		-metrics-addr "127.0.0.1:$metrics" >"$work/serve-$n.log" 2>&1 &
	server_pid=$!
	"$work/clowder-perf" -calls "$n" -base "http://127.0.0.1:$port" -pid "$server_pid" \
		-metrics-url "http://127.0.0.1:$metrics/metrics" -call-duration "${DURATION:-60s}" \
		-video-bitrate "${BITRATE:-1000000}"
	echo "load average after: $(cut -d' ' -f1-3 /proc/loadavg)"
	kill -TERM "$server_pid" 2>/dev/null || true
	wait "$server_pid" 2>/dev/null || true
	server_pid=""
	rm -rf "$data"
done
