#!/usr/bin/env bash
# Smoke test of the Docker composition (SPEC D40).
#
# It builds the image, starts compose.yaml (Clowder and PostgreSQL), checks that
# both containers are healthy, makes the first admin with
# `docker compose run --rm clowder invite -admin`, signs up through the form
# with curl, opens the home page logged in, asks /healthz, and takes everything
# down with its volumes, whatever the result. It exits non-zero if a step fails.
#
# It never touches the compose project of a developer: it uses its own project
# name, its own image tag, a free port and a temporary env file with a random
# password.
#
# usage: scripts/compose-smoke.sh
# environment:
#   SMOKE_BUILD_TAIL  lines of the build output to show (default 15; 0: all)
#   SMOKE_WAIT        seconds to wait for the containers to be healthy (default 180)
#
# TODO(step 15.2): accept -cluster, which adds compose.cluster.yaml to
# `compose_files` and checks that both Clowder processes answer.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

if ! command -v docker >/dev/null 2>&1; then
	echo "compose-smoke.sh: docker is not installed (https://docs.docker.com/engine/install/)" >&2
	exit 2
fi
if ! command -v curl >/dev/null 2>&1; then
	echo "compose-smoke.sh: curl is not installed" >&2
	exit 2
fi

project="clowder-smoke-$$"
image="clowder-smoke:$$"
tmp=$(mktemp -d)
build_tail=${SMOKE_BUILD_TAIL:-15}
wait_seconds=${SMOKE_WAIT:-180}

# A free port: the first random one in 20000-39999 that nothing listens on.
port=
for _ in $(seq 1 50); do
	candidate=$((20000 + RANDOM % 20000))
	if ! (exec 3<>"/dev/tcp/127.0.0.1/$candidate") 2>/dev/null; then
		port=$candidate
		break
	fi
done
if [[ -z $port ]]; then
	echo "compose-smoke.sh: no free port found" >&2
	exit 2
fi
base_url="http://localhost:$port"
password=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')

cat >"$tmp/env" <<ENV
CLOWDER_DB_PASSWORD=$password
CLOWDER_PORT=$port
CLOWDER_BASE_URL=$base_url
ENV
# The compose project builds its own tag, so that clowder:local of a developer
# is left alone.
cat >"$tmp/override.yaml" <<OVERRIDE
services:
  clowder:
    image: $image
OVERRIDE

compose_files=(-f "$root/compose.yaml" -f "$tmp/override.yaml")
dc() {
	docker compose -p "$project" --project-directory "$root" --env-file "$tmp/env" "${compose_files[@]}" "$@"
}

cleanup() {
	local status=$?
	trap - EXIT INT TERM
	set +e
	if ((status != 0)); then
		echo
		echo "==> FAILED (status $status); the last log lines of the containers:"
		dc logs --no-color --tail 40 2>&1
	fi
	echo
	echo "==> docker compose down -v --remove-orphans"
	dc down -v --remove-orphans 2>&1
	docker image rm "$image" >/dev/null 2>&1
	rm -rf "$tmp"
	exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

step() {
	echo
	echo "==> $*"
}

fail() {
	echo "compose-smoke.sh: $*" >&2
	exit 1
}

echo "project $project, image $image, base URL $base_url"

step "docker compose build (the last $build_tail lines)"
if ! dc build --progress=plain >"$tmp/build.log" 2>&1; then
	cat "$tmp/build.log"
	fail "the build failed"
fi
if ((build_tail > 0)); then
	tail -n "$build_tail" "$tmp/build.log"
else
	cat "$tmp/build.log"
fi

step "docker compose up -d --wait"
dc up -d --wait --wait-timeout "$wait_seconds" 2>&1

step "docker compose ps"
dc ps
unhealthy=$(dc ps --format '{{.Service}} {{.Health}}' | grep -v ' healthy$' || true)
[[ -z $unhealthy ]] || fail "not healthy: $unhealthy"
[[ $(dc ps --format '{{.Service}}' | sort | tr '\n' ' ') == "clowder postgres " ]] || fail "expected the services clowder and postgres"

step "docker compose run --rm clowder invite -admin"
invite_out=$(dc run --rm -T clowder invite -admin 2>"$tmp/invite.err") || {
	cat "$tmp/invite.err" >&2
	fail "invite failed"
}
echo "$invite_out"
link=$(grep -m1 "^$base_url/invite/" <<<"$invite_out" || true)
[[ -n $link ]] || fail "the invitation link does not start with $base_url/invite/"
invite_path=${link#"$base_url"}

local_url="http://127.0.0.1:$port"
jar="$tmp/cookies"
display_name="Smoke Admin"

step "GET $invite_path (the sign-up form)"
status=$(curl -sS -o "$tmp/form.html" -w '%{http_code}' "$local_url$invite_path")
echo "status $status"
[[ $status == 200 ]] || fail "the sign-up form answered $status"
grep -q 'name="username"' "$tmp/form.html" || fail "the page has no sign-up form"

step "POST $invite_path (sign up as smokeadmin, \"$display_name\")"
status=$(curl -sS -c "$jar" -o /dev/null -w '%{http_code} -> %{redirect_url}' \
	--data-urlencode "username=smokeadmin" \
	--data-urlencode "displayName=$display_name" \
	--data-urlencode "password=correct horse battery staple" \
	"$local_url$invite_path")
echo "status $status"
[[ $status == "303 -> "* ]] || fail "the sign-up answered: $status"
grep -q 'clowder_session' "$jar" || fail "no session cookie was set"

step "GET / (logged in)"
status=$(curl -sS -b "$jar" -o "$tmp/home.html" -w '%{http_code}' "$local_url/")
echo "status $status"
[[ $status == 200 ]] || fail "the home page answered $status"
grep -o "<title>[^<]*</title>" "$tmp/home.html" | head -n1
grep -o "<p class=\"sidebar-user\"[^>]*>[^<]*</p>" "$tmp/home.html" | head -n1
grep -q ">$display_name<" "$tmp/home.html" || fail "the home page does not show \"$display_name\""

step "GET /healthz"
health=$(curl -sS -i "$local_url/healthz")
echo "$health" | tr -d '\r' | sed -n '1p;/^[Cc]ontent-[Tt]ype/p;/^[Cc]ache-[Cc]ontrol/p;/^ok$/p'
grep -q '^HTTP/[0-9.]* 200' <<<"$health" || fail "/healthz did not answer 200"
tr -d '\r' <<<"$health" | grep -qx 'ok' || fail "/healthz did not say ok"

step "docker compose ps (after the requests)"
dc ps

echo
echo "compose-smoke: OK"
