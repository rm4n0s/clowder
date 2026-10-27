#!/usr/bin/env bash
# Runs the test suite against throw-away PostgreSQL and Redis servers (SPEC D39).
#
# It starts postgres:18 and redis:8 in containers on free local ports, waits
# until both answer, sets CLOWDER_TEST_POSTGRES and CLOWDER_TEST_REDIS, runs
# `go test -race "$@"` (./... when there are no arguments) and removes both
# containers, whatever the result. It exits with the status of `go test`.
#
# usage: scripts/test-docker.sh [go test arguments]
#   scripts/test-docker.sh                                  # the whole suite
#   scripts/test-docker.sh ./internal/store/... -run Search -v
# environment: CONTAINER_TOOL (docker or podman; default: docker, else podman)
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
	echo "test-docker.sh: neither docker nor podman is installed (install docker: https://docs.docker.com/engine/install/)" >&2
	exit 2
fi

pg_name="clowder-test-pg-$$"
redis_name="clowder-test-redis-$$"

cleanup() {
	local status=$?
	trap - EXIT INT TERM
	"$tool" rm -f -v "$pg_name" "$redis_name" >/dev/null 2>&1 || true
	exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# A throw-away server: durability is switched off for speed, the shared memory
# is large enough for parallel migrations (the default of 64 MB is not) and the
# connection limit is high because every test of a package has its own pool.
"$tool" run -d --name "$pg_name" \
	--shm-size=1g \
	-e POSTGRES_USER=clowder -e POSTGRES_PASSWORD=clowder -e POSTGRES_DB=clowder \
	-p 127.0.0.1::5432 \
	docker.io/library/postgres:18 \
	-c max_connections=1000 -c fsync=off -c synchronous_commit=off -c full_page_writes=off >/dev/null
"$tool" run -d --name "$redis_name" -p 127.0.0.1::6379 docker.io/library/redis:8 >/dev/null

# hostport prints the port that the container published for $2 on 127.0.0.1.
hostport() {
	"$tool" port "$1" "$2" | head -n 1 | sed 's/.*://'
}
pg_port=$(hostport "$pg_name" 5432/tcp)
redis_port=$(hostport "$redis_name" 6379/tcp)

echo "test-docker.sh: waiting for PostgreSQL on port $pg_port and Redis on port $redis_port" >&2
deadline=$((SECONDS + 60))
until "$tool" exec "$pg_name" pg_isready -q -h 127.0.0.1 -U clowder -d clowder >/dev/null 2>&1; do
	if ((SECONDS >= deadline)); then
		echo "test-docker.sh: PostgreSQL did not accept connections within 60 s" >&2
		"$tool" logs "$pg_name" >&2 || true
		exit 1
	fi
	sleep 0.5
done
until [[ $("$tool" exec "$redis_name" redis-cli ping 2>/dev/null || true) == PONG ]]; do
	if ((SECONDS >= deadline)); then
		echo "test-docker.sh: Redis did not answer PING within 60 s" >&2
		"$tool" logs "$redis_name" >&2 || true
		exit 1
	fi
	sleep 0.5
done

export CLOWDER_TEST_POSTGRES="postgres://clowder:clowder@127.0.0.1:${pg_port}/postgres?sslmode=disable"
export CLOWDER_TEST_REDIS="redis://127.0.0.1:${redis_port}/0"

if (($# == 0)); then
	set -- ./...
fi
cd "$root"
# Not `exec`: the trap removes the containers after go test ends.
status=0
go test -race "$@" || status=$?
exit "$status"
