#!/usr/bin/env bash
# Measures the targets of SPEC §10 on PostgreSQL (step 17.1, docs/PERFORMANCE.md).
#
# It starts throw-away postgres:18 and redis:8 containers on free local ports,
# makes the large demo on PostgreSQL (`clowder demo -size large -database-url`),
# and then runs, one after the other:
#
#   1. the benchmarks of the feed page and of the search (the same as on SQLite)
#      on that database, through CLOWDER_BENCH_DATABASE_URL;
#   2. scripts/perf.sh with one process: the fan-out of a new post to 200 open
#      streams, and the memory of the server with them idle;
#   3. scripts/perf.sh with two processes and Redis: 100 streams on each, the
#      probe posts written on the first.
#
# Everything is removed at the end, whatever the result. The output is what
# docs/implemented-proof-images/17.1-perf-postgres.txt holds.
#
# usage: scripts/perf-postgres.sh
# environment: CONTAINER_TOOL (docker or podman; default: docker, else podman),
#   BENCHTIME (iterations of a benchmark, default 200x), BENCHCOUNT (default 3),
#   STREAMS, ROUNDS and SETTLE as for scripts/perf.sh, SKIP_CLUSTER=1 leaves out
#   step 3 and ONLY_CLUSTER=1 runs only step 3; LONG_SETTLE (default 20s) is the rest before the memory is read in
#   the second reading of steps 2 and 3, which lets the Go runtime give back
#   what the logins (Argon2id, 64 MiB each) used
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

tool=${CONTAINER_TOOL:-}
if [[ -z $tool ]]; then
	if command -v docker >/dev/null 2>&1; then
		tool=docker
	elif command -v podman >/dev/null 2>&1; then
		tool=podman
	fi
fi
if [[ -z $tool ]] || ! command -v "$tool" >/dev/null 2>&1; then
	echo "perf-postgres.sh: neither docker nor podman is installed" >&2
	exit 2
fi

pg_name="clowder-perf-pg-$$"
redis_name="clowder-perf-redis-$$"
work=$(mktemp -d "${TMPDIR:-/tmp}/clowder-perf-pg.XXXXXX")

cleanup() {
	local status=$?
	trap - EXIT INT TERM
	"$tool" rm -f -v "$pg_name" "$redis_name" >/dev/null 2>&1 || true
	rm -rf "$work"
	exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# The official image with its default configuration (shared_buffers 128 MB),
# which is what docker compose runs; only the shared memory is raised.
"$tool" run -d --name "$pg_name" --shm-size=256m \
	-e POSTGRES_USER=clowder -e POSTGRES_PASSWORD=clowder -e POSTGRES_DB=clowder \
	-p 127.0.0.1::5432 docker.io/library/postgres:18 >/dev/null
"$tool" run -d --name "$redis_name" -p 127.0.0.1::6379 docker.io/library/redis:8 >/dev/null

hostport() {
	"$tool" port "$1" "$2" | head -n 1 | sed 's/.*://'
}
pg_port=$(hostport "$pg_name" 5432/tcp)
redis_port=$(hostport "$redis_name" 6379/tcp)
deadline=$((SECONDS + 60))
until "$tool" exec "$pg_name" pg_isready -q -h 127.0.0.1 -U clowder -d clowder >/dev/null 2>&1; do
	if ((SECONDS >= deadline)); then
		echo "perf-postgres.sh: PostgreSQL did not accept connections within 60 s" >&2
		exit 1
	fi
	sleep 0.5
done
until [[ $("$tool" exec "$redis_name" redis-cli ping 2>/dev/null || true) == PONG ]]; do
	if ((SECONDS >= deadline)); then
		echo "perf-postgres.sh: Redis did not answer within 60 s" >&2
		exit 1
	fi
	sleep 0.5
done

database_url="postgres://clowder:clowder@127.0.0.1:${pg_port}/clowder?sslmode=disable"
redis_url="redis://127.0.0.1:${redis_port}/0"

echo "== machine"
echo "kernel:  $(uname -srvm)"
echo "cpu:     $(grep -m1 'model name' /proc/cpuinfo | sed 's/.*: //') ($(nproc) logical CPUs)"
echo "memory:  $(awk '/MemTotal/ {printf "%.0f GiB", $2 / 1048576}' /proc/meminfo)"
echo "go:      $(go version)"
echo "postgres: $("$tool" exec "$pg_name" postgres --version)"
echo "redis:   $("$tool" exec "$redis_name" redis-server --version | sed 's/ malloc.*//')"
for setting in shared_buffers work_mem effective_cache_size max_connections fsync synchronous_commit; do
	printf 'pg %s: %s\n' "$setting" "$("$tool" exec "$pg_name" psql -U clowder -d clowder -Atc "SHOW $setting")"
done

cd "$root"
echo
echo "== the large demo on PostgreSQL"
go build -o "$work/clowder" ./cmd/clowder
started=$(date +%s.%N)
env -u CLOWDER_DATABASE_URL -u CLOWDER_REDIS_URL CLOWDER_DATABASE_URL="$database_url" \
	"$work/clowder" demo -data "$work/data" -size large 2>"$work/demo.err" | grep '^large demo' || { cat "$work/demo.err" >&2; exit 1; }
finished=$(date +%s.%N)
printf 'whole clowder demo -size large -database-url ... process: %.1f s\n' "$(echo "$finished - $started" | bc)"
"$tool" exec "$pg_name" psql -U clowder -d clowder -Atc \
	"SELECT 'database size: ' || pg_size_pretty(pg_database_size('clowder'))"

if [[ -z ${ONLY_CLUSTER:-} ]]; then
	echo
	echo "== benchmarks: the feed, a post, the Messages list, the sidebar and the search (handler through httptest, ${BENCHCOUNT:-3} runs of ${BENCHTIME:-200x})"
	CLOWDER_BENCH_DATABASE_URL="$database_url" go test ./internal/web ./internal/app -run '^$' -bench Large \
		-benchtime "${BENCHTIME:-200x}" -count "${BENCHCOUNT:-3}"

	# Step 24.1: the unread counts of 1.2. The large demo starts with nothing
	# unread; take alice's marks away, so that every post of others in her rooms
	# is unread, and measure the pages whose sidebar carries the counts again.
	echo
	echo "== benchmarks with every post unread for alice (her room_reads rows deleted)"
	"$tool" exec "$pg_name" psql -U clowder -d clowder -Atc \
		"DELETE FROM room_reads WHERE user_id = (SELECT id FROM users WHERE username = 'alice')"
	CLOWDER_BENCH_DATABASE_URL="$database_url" go test ./internal/web ./internal/app -run '^$' \
		-bench 'Large(Sidebar|Messages|RoomFeed|PostPage)' -benchtime "${BENCHTIME:-200x}" -count "${BENCHCOUNT:-3}"

	echo
	echo "== fan-out and memory, one process on PostgreSQL (streams rest ${SETTLE:-2s} before the memory is read)"
	DATABASE_URL="$database_url" scripts/perf.sh "$work/data"

	echo
	echo "== the same, the streams resting ${LONG_SETTLE:-20s}"
	DATABASE_URL="$database_url" SETTLE="${LONG_SETTLE:-20s}" scripts/perf.sh "$work/data"
fi

if [[ -z ${SKIP_CLUSTER:-} ]]; then
	echo
	echo "== fan-out and memory, two processes on PostgreSQL and Redis (streams rest ${SETTLE:-2s})"
	DATABASE_URL="$database_url" REDIS_URL="$redis_url" PROCESSES=2 scripts/perf.sh "$work/data"

	echo
	echo "== the same, the streams resting ${LONG_SETTLE:-20s}"
	DATABASE_URL="$database_url" REDIS_URL="$redis_url" PROCESSES=2 SETTLE="${LONG_SETTLE:-20s}" scripts/perf.sh "$work/data"
fi
