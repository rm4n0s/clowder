#!/usr/bin/env bash
# Smoke test of the Docker composition (SPEC D40, D41).
#
# It builds the image, starts compose.yaml (Clowder and PostgreSQL), checks that
# both containers are healthy, makes the first admin with
# `docker compose run --rm clowder invite -admin`, signs up through the form
# with curl, opens the home page logged in, asks /healthz, and takes everything
# down with its volumes, whatever the result. It exits non-zero if a step fails.
#
# With -cluster it uses compose.cluster.yaml as well (Redis, a second Clowder
# process and Caddy in front of both) and also checks that every container is
# healthy, that the login made through Caddy works on each Clowder process
# directly, that Caddy gives requests to both, and that a post written on one
# process arrives live in the stream of a page served by the other.
#
# It also checks that the SSH server of the sign-in codes answers on its port
# with an SSH banner (SSH-2.0-); with -cluster, that only the first Clowder
# process runs it, and that the media server of the calls listens on its port
# (TCP, with the UDP port published); with -cluster, that both processes do, each
# on a port of its own (7882 and 7883), and that each has announced itself in
# Redis with the key clowder:process:<id> (SPEC D65).
#
# It never touches the compose project of a developer: it uses its own project
# name, its own image tag, a free port and a temporary env file with a random
# password.
#
# usage: scripts/compose-smoke.sh [-cluster]
# environment:
#   SMOKE_BUILD_TAIL  lines of the build output to show (default 15; 0: all)
#   SMOKE_WAIT        seconds to wait for the containers to be healthy (default 180)
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

cluster=false
case "${1:-}" in
	"") ;;
	-cluster | --cluster) cluster=true ;;
	*)
		echo "usage: scripts/compose-smoke.sh [-cluster]" >&2
		exit 2
		;;
esac
if (($# > 1)); then
	echo "usage: scripts/compose-smoke.sh [-cluster]" >&2
	exit 2
fi

project="clowder-smoke-$$"
image="clowder-smoke:$$"
tmp=$(mktemp -d)
build_tail=${SMOKE_BUILD_TAIL:-15}
wait_seconds=${SMOKE_WAIT:-180}

# A free port: the first random one in 20000-39999 that nothing listens on.
free_port() {
	local candidate
	for _ in $(seq 1 50); do
		candidate=$((20000 + RANDOM % 20000))
		if ! (exec 3<>"/dev/tcp/127.0.0.1/$candidate") 2>/dev/null; then
			echo "$candidate"
			return 0
		fi
	done
	return 1
}
port=$(free_port) || {
	echo "compose-smoke.sh: no free port found" >&2
	exit 2
}
ssh_port=$(free_port) || {
	echo "compose-smoke.sh: no free port found" >&2
	exit 2
}
if [[ $ssh_port == "$port" ]]; then
	ssh_port=$((port + 1))
fi
media_port=$(free_port) || {
	echo "compose-smoke.sh: no free port found" >&2
	exit 2
}
if [[ $media_port == "$port" || $media_port == "$ssh_port" ]]; then
	media_port=$((port + 2))
fi
# The second Clowder process of the cluster has a media port of its own.
media_port2=$(free_port) || {
	echo "compose-smoke.sh: no free port found" >&2
	exit 2
}
if [[ $media_port2 == "$port" || $media_port2 == "$ssh_port" || $media_port2 == "$media_port" ]]; then
	media_port2=$((port + 3))
fi
base_url="http://localhost:$port"
password=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')

cat >"$tmp/env" <<ENV
CLOWDER_DB_PASSWORD=$password
CLOWDER_PORT=$port
CLOWDER_SSH_PORT=$ssh_port
CLOWDER_MEDIA_PORT=$media_port
CLOWDER_MEDIA_PORT2=$media_port2
CLOWDER_BASE_URL=$base_url
ENV
# The compose project builds its own tag, so that clowder:local of a developer
# is left alone.
cat >"$tmp/override.yaml" <<OVERRIDE
services:
  clowder:
    image: $image
OVERRIDE
compose_files=(-f "$root/compose.yaml")
expected_services="clowder postgres "
if $cluster; then
	cat >>"$tmp/override.yaml" <<OVERRIDE
  clowder2:
    image: $image
OVERRIDE
	compose_files+=(-f "$root/compose.cluster.yaml")
	expected_services="caddy clowder clowder2 postgres redis "
fi
compose_files+=(-f "$tmp/override.yaml")
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

echo "project $project, image $image, base URL $base_url, cluster: $cluster"

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
[[ $(dc ps --format '{{.Service}}' | sort | tr '\n' ' ') == "$expected_services" ]] || fail "expected the services: $expected_services"

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
grep -o "<title[^>]*>[^<]*</title>" "$tmp/home.html" | head -n1
grep -o "<p [^>]*id=\"sidebar-user\"[^>]*>[^<]*</p>" "$tmp/home.html" | head -n1
grep -q ">$display_name<" "$tmp/home.html" || fail "the home page does not show \"$display_name\""

step "GET /healthz"
health=$(curl -sS -i "$local_url/healthz")
echo "$health" | tr -d '\r' | sed -n '1p;/^[Cc]ontent-[Tt]ype/p;/^[Cc]ache-[Cc]ontrol/p;/^ok$/p'
grep -q '^HTTP/[0-9.]* 200' <<<"$health" || fail "/healthz did not answer 200"
tr -d '\r' <<<"$health" | grep -qx 'ok' || fail "/healthz did not say ok"

step "the SSH server answers on port $ssh_port with an SSH banner"
banner=
for _ in $(seq 1 20); do
	if exec 3<>"/dev/tcp/127.0.0.1/$ssh_port" 2>/dev/null; then
		IFS= read -r -t 5 banner <&3 || true
		exec 3<&- 3>&-
		[[ $banner == SSH-2.0-* ]] && break
	fi
	sleep 0.5
done
echo "banner: ${banner%$'\r'}"
[[ $banner == SSH-2.0-* ]] || fail "the SSH port did not answer with an SSH banner"

# media_check SERVICE CONTAINER_PORT HOST_PORT checks that the media server of the
# service listens: the TCP port accepts, the log says so, and the UDP port is
# published under the host port.
media_check() {
	local svc=$1 container_port=$2 host_port=$3 ok=
	for _ in $(seq 1 20); do
		if (exec 3<>"/dev/tcp/127.0.0.1/$host_port") 2>/dev/null; then
			ok=1
			break
		fi
		sleep 0.5
	done
	[[ -n $ok ]] || fail "$svc: the media port $host_port did not accept a TCP connection"
	dc logs --no-color "$svc" >"$tmp/$svc.log" 2>&1 || fail "could not read the log of $svc"
	grep -q 'media server listening' "$tmp/$svc.log" || fail "$svc logged no media server"
	dc port --protocol udp "$svc" "$container_port" >"$tmp/$svc-udp-port.txt" || fail "$svc: the UDP media port is not published"
	grep -q ":$host_port" "$tmp/$svc-udp-port.txt" || fail "$svc: the UDP media port is not published on $host_port: $(cat "$tmp/$svc-udp-port.txt")"
	echo "$svc: TCP $host_port accepts; UDP $host_port is published"
}

if $cluster; then
	step "the media servers of both processes listen, each on a port of its own"
	[[ $media_port != "$media_port2" ]] || fail "the two media ports are the same: $media_port"
	media_check clowder 7882 "$media_port"
	media_check clowder2 7883 "$media_port2"

	step "both processes announced themselves in Redis (clowder:process:<id>, with the media address)"
	keys=
	for _ in $(seq 1 20); do
		keys=$(dc exec -T redis redis-cli --raw keys 'clowder:process:*' | tr -d '\r' | sort || true)
		[[ $(grep -c . <<<"$keys") == 2 ]] && break
		sleep 0.5
	done
	[[ $(grep -c . <<<"$keys") == 2 ]] || fail "expected two process keys in Redis, found: $keys"
	for key in $keys; do
		value=$(dc exec -T redis redis-cli --raw get "$key" | tr -d '\r')
		ttl=$(dc exec -T redis redis-cli --raw ttl "$key" | tr -d '\r')
		echo "$key = $value (expires in ${ttl}s)"
		[[ $value == *:7882 || $value == *:7883 ]] || fail "$key does not hold a media address: $value"
		((ttl > 0 && ttl <= 15)) || fail "$key has the time to live $ttl, want 1-15"
	done
	grep -q ':7882' <<<"$(for k in $keys; do dc exec -T redis redis-cli --raw get "$k"; done)" || fail "no process announced port 7882"
	grep -q ':7883' <<<"$(for k in $keys; do dc exec -T redis redis-cli --raw get "$k"; done)" || fail "no process announced port 7883"
else
	step "the media server listens on port $media_port (TCP, and UDP in the published ports)"
	media_check clowder 7882 "$media_port"
fi

if $cluster; then
	step "both Clowder processes run the SSH server, and share its host key"
	dc logs --no-color clowder 2>&1 | grep -q 'the SSH server is listening' || fail "clowder logged no SSH server"
	dc logs --no-color clowder2 2>&1 | grep -q 'the SSH server is listening' || fail "clowder2 logged no SSH server"
	host_keys=$(dc logs --no-color clowder clowder2 2>&1 | grep -o '"hostKey":"[^"]*"' | sort -u)
	[[ $(wc -l <<<"$host_keys") == 1 ]] || fail "the two processes show different host keys: $host_keys"
	echo "clowder and clowder2 run the SSH server with one host key: ${host_keys#*:}"
	token=$(awk '$6 == "clowder_session" { print $7 }' "$jar")
	[[ -n $token ]] || fail "no session token in the cookie jar"

	step "GET /healthz and GET / on each Clowder process, with the session made through Caddy"
	for svc in clowder clowder2; do
		out=$(dc exec -T caddy wget -qO- "http://$svc:8080/healthz") || fail "$svc did not answer /healthz"
		echo "$svc /healthz: $out"
		[[ $out == ok ]] || fail "$svc: /healthz said: $out"
		dc exec -T caddy wget -qO- --header "Cookie: clowder_session=$token" "http://$svc:8080/" >"$tmp/$svc.html" ||
			fail "$svc did not answer / for the session"
		grep -q ">$display_name<" "$tmp/$svc.html" || fail "$svc does not show \"$display_name\" for the session of the other process"
		echo "$svc /: logged in as $(grep -o "<p [^>]*id=\"sidebar-user\"[^>]*>[^<]*</p>" "$tmp/$svc.html" | head -n1)"
	done

	step "Caddy gives the requests to both processes (12 requests of GET /login)"
	for _ in $(seq 1 12); do
		curl -sS -o /dev/null "$local_url/login"
	done
	for svc in clowder clowder2; do
		n=$(dc logs --no-color "$svc" 2>&1 | grep -c '"pattern":"GET /login"' || true)
		echo "$svc logged $n requests of GET /login"
		((n >= 1)) || fail "$svc received no request through Caddy"
	done

	step "a post written on clowder arrives live in a stream served by clowder2"
	room_url=$(curl -sS -b "$jar" -o /dev/null -w '%{redirect_url}' \
		--data-urlencode "name=smoke" --data-urlencode "description=cluster smoke" --data-urlencode "visibility=visible" \
		"$local_url/rooms")
	room_path=$(sed -E 's#^https?://[^/]+##' <<<"$room_url")
	echo "room: $room_path"
	[[ $room_path =~ ^/rooms/[0-9]+$ ]] || fail "the room was not made: $room_url"
	text="Written on process A"
	: >"$tmp/stream.txt"
	timeout 12 docker compose -p "$project" --project-directory "$root" --env-file "$tmp/env" "${compose_files[@]}" \
		exec -T caddy wget -qO- --header "Cookie: clowder_session=$token" --header "Datastar-Request: true" \
		"http://clowder2:8080$room_path/stream" >"$tmp/stream.txt" 2>/dev/null &
	stream_pid=$!
	# Wait until the stream is open on clowder2 (its first patch has arrived).
	for _ in $(seq 1 40); do
		[[ -s $tmp/stream.txt ]] && break
		sleep 0.25
	done
	[[ -s $tmp/stream.txt ]] || fail "the stream of clowder2 sent nothing"
	grep -q "$text" "$tmp/stream.txt" && fail "the stream shows the post before it was written"
	# Straight to clowder, not through Caddy. wget then follows the redirect of
	# the plain form post to the public address, which the container cannot
	# reach, so its exit status says nothing; the stream below is the check.
	dc exec -T caddy wget -qO- --header "Cookie: clowder_session=$token" \
		--post-data "format=text&body=$(sed 's/ /+/g' <<<"$text")" "http://clowder:8080$room_path/posts" >/dev/null 2>&1 || true
	echo "posted \"$text\" on clowder"
	for _ in $(seq 1 40); do
		grep -q "$text" "$tmp/stream.txt" && break
		sleep 0.25
	done
	kill "$stream_pid" 2>/dev/null || true
	wait "$stream_pid" 2>/dev/null || true
	grep -q "$text" "$tmp/stream.txt" || fail "the post written on clowder did not arrive in the stream of clowder2"
	echo "the stream of clowder2 received the post:"
	grep -m1 "$text" "$tmp/stream.txt" | cut -c1-160
fi

step "docker compose ps (after the requests)"
dc ps

echo
echo "compose-smoke: OK"
