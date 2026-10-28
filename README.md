# Clowder

Clowder is a self-hosted web application where a team talks in rooms. A room is a feed of posts, and the conversation about a post happens in the comments under it, which can be answered in turn. Posts and comments carry text or markdown, attachments, tags and tasks, and the tasks of a room are the cards of its Kanban board. People also write private messages to each other, one to one or in small groups. Nobody joins without an invitation. Clowder is one program with an SQLite database (or PostgreSQL) and a directory of attachments; the pages are rendered on the server and kept live over server-sent events, so what one person posts or moves appears for everyone looking at it. A REST API and a WebSocket let a desktop client do the same.

## What it does

- **Invitations and accounts.** An admin makes one-time invitation links. Opening a link lets a person choose a username, a display name and a password. Admins disable accounts and change roles.
- **Rooms.** Admins create visible rooms, which everyone can read and join. Anyone creates private rooms, which only their members see.
- **Posts and comments.** A post has a body in plain text or markdown, attachments, tasks and tags. Comments nest to any depth and have everything a post has. Authors edit and delete their own; admins delete any.
- **Reactions** with emoji on posts and comments.
- **Attachments** up to a configurable size, stored on local disk, downloaded only by people who can read the room. Images are shown inline.
- **Tasks and the board.** Each room has a Kanban board whose cards are the tasks written in its posts and comments, with assignees and due dates. Cards move between columns by drag and drop; members add, rename, reorder and remove columns.
- **Search** over post and comment bodies, attachment names and task titles, with filters for tags, room, author and date, in every room the person can read.
- **Mentions and notifications** for `@username`, replies, assigned tasks and being added to a room, with a count that updates live.
- **Private messages** between users and in groups, with replies, reactions, attachments, editing, deleting and read state.
- **Live updates** of feeds, threads, reactions, boards and counts.
- **Audit log** of changes to posts, tasks, columns, rooms and memberships, which admins read and filter.
- **A REST API and a WebSocket** for desktop clients, with an OpenAPI document (see "API for desktop clients").
- **PostgreSQL, Docker and several processes** for installations that want them (see "Running with Docker").

## Screenshots

| | |
|---|---|
| ![The feed of a room](docs/implemented-proof-images/4.3-feed.png) | ![A thread of comments](docs/implemented-proof-images/5.2-thread.png) |
| The feed of a room | A thread of comments |
| ![The board of a room](docs/implemented-proof-images/9.3-board.png) | ![Search results](docs/implemented-proof-images/10.2-search.png) |
| The board of a room | Search |
| ![The notifications page](docs/implemented-proof-images/11.2-notifications.png) | ![A private conversation](docs/implemented-proof-images/12.2-messages.png) |
| Notifications | Private messages |

## API for desktop clients

Everything a person does in the pages can also be done through a JSON API under `/api/v1`, and the live changes of the pages arrive over a WebSocket at `/api/v1/ws`. A client logs in with a username and password, gets a token (a session that the user can see and end on the `/me/sessions` page) and sends it as `Authorization: Bearer <token>`. The API calls the same code as the pages, so every rule is the same: a private room is `404` to anyone who is not in it, files are downloaded only by people who can read them, and so on.

- [`docs/API.md`](docs/API.md) is the guide for the people who write a client: logging in, errors, paging, uploads, the PATCH rules, the WebSocket (subscribe, events, closing, reconnecting) and an example session with `curl` and Go.
- `/api/v1/openapi.json` is the OpenAPI 3.1 document of the installation you talk to (no login needed); it is also in the repository as [`internal/api/openapi.json`](internal/api/openapi.json). A test keeps it equal to the routes. Use it to generate a client.
- `go run ./cmd/clowder-apicheck -h` is a small client that runs a whole session against a server and prints every request and answer; with `-watch` it also prints the live events.

The API sets no cross-origin headers and a web page of another site cannot call it. Behind a reverse proxy it needs the same things as the pages (the `Host` header, `-trust-proxy` for the login limit), and a proxy must pass WebSocket upgrades (see "Running behind a TLS reverse proxy").

## Building

Go 1.27 or later is needed. Clowder has no cgo, so the build needs no C compiler:

```sh
CGO_ENABLED=0 go build -o clowder ./cmd/clowder
```

The binary has the templates, the stylesheet and the Datastar client inside it. At run time it needs only its data directory. Linux on amd64 and arm64 is what is tested.

## The first admin

Nobody can sign up without an invitation, and the first admin has no one to invite them, so the command line makes the first link:

```sh
./clowder invite -admin -base-url https://chat.example.org
```

It prints a link such as `https://chat.example.org/invite/<token>` and the time it expires. Open it in a browser, choose a username (3 to 32 characters of `a-z`, `0-9`, `_` and `-`), a display name and a password of at least 12 characters, and you are logged in as an admin. The link works once and expires after 7 days. Give the same `-data` directory as the server (or set `CLOWDER_DATA`), and the public URL as `-base-url`, or the link names the default `http://localhost:8080`.

From then on admins make invitation links for members and other admins in the Admin page. `./clowder invite -member` does the same on the command line. A user who forgot the password gets a reset link from an admin in the same page (Reset password), or on the command line with `./clowder reset-password -user alice`, which prints a link that works once for 24 hours.

## Running it

```sh
./clowder serve -data /var/lib/clowder -addr 127.0.0.1:8080 -base-url https://chat.example.org
```

The data directory holds `clowder.db` (the database) and `files/` (the attachments). The program makes the directory when it is missing, with access for its owner and group only, and keeps the database file readable by its owner only. Keep the directory private: it holds password hashes and every attachment. Stop the server with SIGINT or SIGTERM: it stops accepting requests, closes the open streams, waits up to 10 seconds for running requests and closes the database.

Clowder runs as one process per database, unless it runs on PostgreSQL with Redis: then several processes can serve one database (see "Several processes" below). It speaks plain HTTP, so put a reverse proxy with TLS in front of it (see below).

### Upgrading

Back up the data directory first (see "Backups"), stop the old process, start the new binary on the same data directory (or the same `-database-url`), and Clowder migrates the database when it starts: it only adds what the new version needs. **A 1.0 installation on SQLite upgrades to 1.1 this way, with nothing else to do**: the sessions of the people who are logged in stay valid (they become browser sessions), and the API is on at once. Moving an existing SQLite installation to PostgreSQL is not supported (there is no tool for it): PostgreSQL is for a new installation, or you start the new one empty and invite people again. Downgrading is not supported either: restore the backup of the old version.

## Running with Docker

`compose.yaml` runs Clowder and PostgreSQL 18. The image is built from the `Dockerfile` (a static binary on a distroless base, running as a user that is not root); PostgreSQL is the official image. You need Docker with the Compose plugin.

```sh
cp .env.example .env
```

Edit `.env`: **change `CLOWDER_DB_PASSWORD`** (the example value is a placeholder; compose refuses to start without a password, and anyone who knows the placeholder knows your database password), and set `CLOWDER_BASE_URL` to the address people will type in their browser (`https://chat.example.org` behind a proxy). `.env.example` explains every variable. Then:

```sh
docker compose up -d --build
docker compose run --rm clowder invite -admin
```

The second command prints the link of the first admin (see "The first admin"); its address is made from `CLOWDER_BASE_URL`. Clowder is published on `CLOWDER_PORT` (8080). `docker compose ps` shows both services `healthy` once they are ready: the image asks its own `GET /healthz`, which answers `200 ok` when the database answers.

- **Logs:** `docker compose logs -f clowder` (JSON lines).
- **Stop and start:** `docker compose stop` and `docker compose start`. `docker compose down` removes the containers and keeps the data; `docker compose down -v` also deletes the volumes, which is the database and the attachments.
- **Where the data is:** the volume `pgdata` holds the database and the volume `clowderdata` is the data directory of Clowder (`/data`), of which only `files/`, the attachments, matters with PostgreSQL.
- **Upgrades:** get the new source (`git pull`) or image, then `docker compose build` (or `docker compose pull`) and `docker compose up -d`. Clowder migrates the database when it starts.
- **Backups:** the database with `pg_dump`, then `clowder backup` for the attachments and the host key, **after** it, for the reason given under "Backups". The image has no shell, so the archive travels through the standard streams:

  ```sh
  docker compose exec -T postgres sh -c 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' > clowder.dump
  docker compose run --rm -T clowder backup -o - > clowder-backup.tar.gz
  ```

  To restore into a new, empty installation (the volume `clowderdata` is empty): `docker compose up -d postgres`, then

  ```sh
  docker compose exec -T postgres sh -c 'pg_restore -U "$POSTGRES_USER" -d "$POSTGRES_DB" --clean --if-exists' < clowder.dump
  docker compose run --rm -T clowder restore -i - -data /data < clowder-backup.tar.gz
  docker compose up -d
  ```

  `clowder backup` reports the `pg_dump` command for the URL inside the container (`postgres://clowder@postgres:5432/clowder`), which is the one that `docker compose exec` above runs for you. Both files hold private data (password hashes, messages, attachments): keep them private.
- **Who can reach it:** compose publishes `CLOWDER_PORT` on every interface of the machine. Behind a reverse proxy on the same machine, publish it on the loopback address only, with a `compose.override.yaml` next to `compose.yaml`: `services: {clowder: {ports: !override ["127.0.0.1:8080:8080"]}}`. PostgreSQL (and, in the cluster, Redis and the Clowder processes) are never published; they are reachable only inside the compose network. The database password is in the environment of the Clowder container, so anyone who may use Docker on the machine can read it: that is already root-equivalent.
- **TLS and a reverse proxy:** the container speaks plain HTTP, so put a reverse proxy with TLS in front of it, as "Running behind a TLS reverse proxy" says: proxy to the published port, set `CLOWDER_BASE_URL` to the public `https://` address and, only when every request comes through the proxy, `CLOWDER_TRUST_PROXY=true`.

`scripts/compose-smoke.sh` checks the whole composition on a throw-away project: it builds the image, starts both services, makes an admin invitation, signs up through the form, opens a page and removes everything.

### Several processes

One Clowder process is enough for a team. When you want more than one, for example to restart one process without a pause for the users, or to use more than one machine's CPUs, `compose.cluster.yaml` runs two Clowder processes behind a load balancer, and they behave as one Clowder:

```sh
docker compose -f compose.yaml -f compose.cluster.yaml up -d --build
docker compose -f compose.yaml -f compose.cluster.yaml run --rm clowder invite -admin
```

It adds, to the services of `compose.yaml`:

- **`redis`** (`redis:8`), which carries the live events between the processes (a post written on one appears at once in the pages served by the other) and counts the failed logins, so that "5 failures in 15 minutes" holds across the processes. It keeps nothing on disk and has no volume: restart it whenever you like. While it is down the processes keep serving, their pages may miss each other's live updates, and the login limit is not enforced (a login still checks the password); when it is back the open pages reload what they missed.
- **`clowder2`**, a second process of the same image, with the same database and the same `clowderdata` volume (the attachments). Both processes need the same configuration, which they get from the same `.env`.
- **`caddy`** (`caddy:2`, configured by `deploy/Caddyfile.cluster`), which publishes `CLOWDER_PORT` and gives the requests to the two processes in turn. A process that fails `/healthz` is left out until it answers again. The processes are no longer published themselves.

Notes:

- **Requirements.** Several processes need PostgreSQL and Redis; with SQLite `-redis-url` is refused. All the processes must use the same data directory (one volume, or a network file system) for the attachments, and the same database.
- **No sticky sessions.** A session lives in the database and every process delivers every live event, so the load balancer may send any request to any process. Any proxy with round-robin works; the Caddyfile is an example. It sends each write of a live stream at once (`flush_interval -1`) and passes the client's address in `X-Forwarded-For`, which is why the composition sets `CLOWDER_TRUST_PROXY=true`.
- **Starting together.** The processes may start at the same moment on an empty database: the first one migrates it while the others wait.
- **Connections.** Each process keeps up to 32 connections to PostgreSQL (two per CPU, at least 8), so `max_connections` of the server must be above that times the number of processes.
- **Without Docker.** Give every process the same `-database-url`, the same `-redis-url` (`redis://host:6379/0`, or `rediss://` for TLS) and the same `-data`.
- **Upgrades.** As with one process: `docker compose -f compose.yaml -f compose.cluster.yaml up -d --build` recreates both; the first to start migrates the database and the other waits for it.
- **Calls.** Both processes run the media server of the calls, each on a port of its own: `clowder` on 7882 (`CLOWDER_MEDIA_PORT`) and `clowder2` on 7883 (`CLOWDER_MEDIA_PORT2`), both published for UDP and TCP and announced with `CLOWDER_MEDIA_PUBLIC_IP`; open all four in your firewall. The media of a call lives in the process that started it (its *home*): the browsers of both people connect straight to that process' port, whichever process Caddy gave their page to. A person whose page is on the other process has the signalling of the call relayed through Redis. Each process keeps the key `clowder:process:<id>` in Redis (15 seconds, refreshed every 5) while it runs; when a process stops, it ends its calls, and when one is gone without a trace (a crash, a killed container) the other ends its calls about 35 seconds later: the key expires after 15 seconds and a 20-second grace follows, so that a restart of Redis, which loses the keys until the processes write them again, ends no live call ("call ended: server restarting"), and the people can start a new one. A Redis that is down stops the calls that cross processes (a call whose people are both on its home goes on).

`scripts/compose-smoke.sh -cluster` checks it on a throw-away project: every container healthy, the login made through Caddy working on each process directly, both processes receiving requests, both media ports open and announced in Redis, and a post written on one process arriving live in a stream of the other.

### PostgreSQL without Docker

`clowder serve` uses PostgreSQL instead of SQLite when it is given a database URL, with `-database-url` or `CLOWDER_DATABASE_URL`, for example `postgres://clowder:secret@db.example.org:5432/clowder?sslmode=verify-full`. `clowder invite`, `clowder reset-password` and `clowder demo` read the same setting. The attachments stay in `files/` of the data directory.

- **Version and extensions.** PostgreSQL 16 or later (18 is the tested version) with ICU and the `unaccent` extension, which the official images and the usual packages (`postgresql-contrib`) have.
- **The rights of the database user.** Clowder migrates the database when it starts, so the user must be allowed to create, in its database: tables and indexes, the extension `unaccent` (a trusted extension, so the owner of the database may create it), a text search configuration (`clowder_search`), an ICU collation (`nocase`) and functions (`json_valid`), and to use advisory locks. The simplest way is to make the user the **owner of the database**: `CREATE ROLE clowder LOGIN PASSWORD '...'; CREATE DATABASE clowder OWNER clowder;`. It needs no other right, and no superuser. From PostgreSQL 15 only the owner may create objects in the `public` schema, so a user that is not the owner needs `GRANT CREATE ON SCHEMA public` and `GRANT CREATE ON DATABASE clowder`.
- **TLS.** The URL's `sslmode` decides. The driver's default is `prefer`, which uses TLS when the server offers it and goes on without it when not, so an attacker on the network can make it go without. For a database that is not on the same machine or the same private network use `sslmode=verify-full` (with `sslrootcert=/path/to/ca.pem` when the certificate is not from a public authority) or at least `require`. `sslmode=disable` is for a database in the same compose network.
- **The password in the URL.** A password with characters that have a meaning in a URL (`@`, `:`, `/`, `?`, `#`, `%`) must be percent-encoded, or use letters and digits only. The URL is never written to a log or an error with its password (it shows `xxxxx`), and `clowder serve -h` does not print the URL of the environment. Keep it in the environment (`CLOWDER_DATABASE_URL`) or in a file your service manager reads, not on a command line that others can list with `ps`.
- **Connections.** Each process keeps up to 32 connections (two per CPU, at least 8): `max_connections` of the server must be above that times the number of processes.
- **Backups.** `pg_dump -Fc`, then `clowder backup` for the attachments and the host key after it (see "Backups" and "Running with Docker").
- **Sizes and speed.** The numbers on PostgreSQL are in [`docs/PERFORMANCE.md`](docs/PERFORMANCE.md).

### Redis

Several processes need Redis (`-redis-url`, version 8 is tested, Valkey works too). Redis carries only the live events (the topic and the IDs involved, never a text) and the counters of failed logins, so it keeps nothing worth saving and needs no persistence. **Keep it private**: whoever can write to the channel `clowder:events` can make every page reload or log a user's streams out, and whoever can write to the keys `clowder:login:*` can lock people out or clear the limit. Put it on a private network or bind it to the machine, give it a password (`redis://:password@host:6379/0`; percent-encode special characters) and, if it crosses a network, use TLS with a `rediss://` URL. The URL is redacted in logs and errors like the database URL. With Redis 6 or later ACLs, the user of Clowder needs the commands `ping`, `publish`, `subscribe`, `multi`, `exec`, `zadd`, `zcard`, `zremrangebyscore`, `zremrangebyrank`, `pexpire` and `del` (and `hello` and `client|setinfo`, which the client sends when it connects), the keys `clowder:login:*` and the channel `clowder:events`.

## Configuration

Each setting is a flag of `clowder serve` or a `CLOWDER_*` environment variable. A flag wins over the variable, and the variable wins over the default. `clowder serve -h` lists the flags.

| Flag | Variable | Default | What it sets |
|---|---|---|---|
| `-addr` | `CLOWDER_ADDR` | `:8080` | Address to listen on, as `host:port`. |
| `-data` | `CLOWDER_DATA` | `./data` | Data directory, with `clowder.db` and `files/`. |
| `-database-url` | `CLOWDER_DATABASE_URL` | empty | A `postgres://` URL. Empty: SQLite in `clowder.db` of the data directory. |
| `-redis-url` | `CLOWDER_REDIS_URL` | empty | A `redis://` or `rediss://` URL. Set it to run several processes on one database: it carries the live events between them and counts the failed logins. Needs `-database-url`. Empty: one process, with the events and the limiter in memory. |
| `-base-url` | `CLOWDER_BASE_URL` | `http://localhost:8080` | The public URL, an absolute `http` or `https` URL. Invitation links are made from it. Cookies get the `Secure` attribute unless it starts with `http://`. |
| `-max-attachment-size` | `CLOWDER_MAX_ATTACHMENT_SIZE` | `26214400` | Largest attachment in bytes (25 MiB). |
| `-max-attachments` | `CLOWDER_MAX_ATTACHMENTS` | `10` | Most attachments on one post, comment or message. |
| `-session-lifetime` | `CLOWDER_SESSION_LIFETIME` | `720h` | How long a login lasts from its last use (30 days). |
| `-invitation-lifetime` | `CLOWDER_INVITATION_LIFETIME` | `168h` | How long an invitation link is valid (7 days). |
| `-log-level` | `CLOWDER_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. Logs are JSON lines on stderr. |
| `-ssh-addr` | `CLOWDER_SSH_ADDR` | empty | Address of the SSH server of the sign-in codes, as `host:port`, such as `:2222`. Empty: the server is off. |
| `-ssh-host-key` | `CLOWDER_SSH_HOST_KEY` | `ssh_host_ed25519_key` in the data directory | The PEM file of the host key of the SSH server. It is made on the first start (Ed25519, mode 0600) when it does not exist; several processes on one data directory share it. |
| `-ssh-public-host` | `CLOWDER_SSH_PUBLIC_HOST` | the host of `-base-url` | The host the settings page shows in the `ssh` command: a host name or an IP address, without a scheme, a port or a path. |
| `-media-addr` | `CLOWDER_MEDIA_ADDR` | empty | Address of the media server of the video and audio calls in private messages, as `host:port`, such as `:7882`. It uses that number for one UDP and one TCP port; open both in your firewall. Empty: calls are off. The compose file turns it on and publishes `CLOWDER_MEDIA_PORT` (7882); in the cluster composition the second process listens on 7883 (`CLOWDER_MEDIA_PORT2`), because each process needs a media port of its own. |
| `-media-public-ip` | `CLOWDER_MEDIA_PUBLIC_IP` | empty | The IP address that the people who call reach this machine at, when it runs behind NAT or in a container. Empty announces the addresses of the machine. |
| `-max-calls` | `CLOWDER_MAX_CALLS` | `20` | Most calls (ringing or in progress) that one process holds at once; zero or less is no limit. |
| `-call-ring-timeout` | `CLOWDER_CALL_RING_TIMEOUT` | `45s` | How long a call rings before nobody answered it and it becomes a missed call; at least one second. The default is what people expect: change it only for tests. |
| `-metrics-addr` | `CLOWDER_METRICS_ADDR` | empty | Address of the listener that serves the metrics at `GET /metrics`, as `host:port`, such as `127.0.0.1:9090` (see "Metrics"). It has no login: bind it to `127.0.0.1` or a private address. Empty: no metrics and no listener. |
| `-trust-proxy` | `CLOWDER_TRUST_PROXY` | `false` | Take the client address, which the login limiter counts, from the last entry of `X-Forwarded-For`. Turn it on only behind your own reverse proxy. |

Durations are written the way Go reads them: `720h`, `90m`. `clowder invite` reads `-data`, `-database-url`, `-base-url` and `-invitation-lifetime` (and their variables); `clowder demo` reads `-data`, `-database-url` and `-size`; `clowder backup` reads `-o`, `-data` and `-database-url`, and `clowder restore` reads `-i` and `-data` (see "Backups"). `clowder healthcheck -addr host:port` asks `GET /healthz` of a running server and exits 0 when it answers 200 and 1 otherwise; the Docker image runs it as its health check.

The log has one entry per request (method, route pattern, status, duration, user ID). It never has passwords, tokens, post bodies or query strings.

## Metrics

With `-metrics-addr 127.0.0.1:9090`, Clowder serves its metrics at `GET /metrics` on that address, in the Prometheus text format. It is a listener of its own and never the public one: `/metrics` on `-addr` is a 404 page. Without `-metrics-addr` nothing is counted and nothing listens.

**The listener has no login.** It is for a private network: bind it to `127.0.0.1` (and scrape from the same machine, or through an SSH tunnel or a reverse proxy of your own that asks for a login) or to an address of a private network. Never bind it to a public address. With Docker, leave the port unpublished and let Prometheus scrape it over the compose network:

```yaml
# in compose.yaml, under clowder: environment:
CLOWDER_METRICS_ADDR: ":9090"      # reachable as clowder:9090 from other services of the compose network
```

A scrape configuration for Prometheus:

```yaml
scrape_configs:
  - job_name: clowder
    scrape_interval: 15s
    static_configs:
      - targets: ["127.0.0.1:9090"]     # or ["clowder:9090"] in the compose network
```

| Metric | Labels | What it counts |
|---|---|---|
| `clowder_http_requests_total` | `pattern`, `method`, `code` | Requests that ended. `pattern` is the route that answered, such as `GET /rooms/{id}` (never the path: no ID is in it), or `none` when no route matched; `code` is the status class, `1xx` to `5xx`. The REST API is counted too. A stream or a WebSocket is counted when it ends. |
| `clowder_http_request_duration_seconds` | `pattern`, `method` | A histogram of how long requests took (buckets from 5 ms to 10 s). Event streams and WebSockets are left out: they are long by design. |
| `clowder_sse_streams_open` | | Open event streams of the pages. |
| `clowder_websockets_open` | `kind` | Open WebSockets: `api` (the user's socket of the API) and `call` (the signalling socket of a call, from the pages or the API). |
| `clowder_hub_dropped_total` | `reason` | Live subscriptions the server closed: `overflow` (the client was too slow) and `reconnect` (Redis came back after a break). The pages and clients reload when it happens. |
| `clowder_logins_total` | `result` | Logins: `ok`, `wrong` (wrong username or password), `limited` (too many failures), `ssh_code_required` (the password was right, the SSH code is next) and `ssh_code_wrong`. |
| `clowder_ssh_codes_issued_total` | | SSH sign-in codes issued. |
| `clowder_calls_active` | | Calls whose media this process holds. |
| `clowder_calls_total` | `outcome` | Calls whose media this process held, counted when they end: `hangup`, `cancelled`, `declined`, `missed`, `busy`, `failed`, `disabled`, `session_ended`, `server_stopped` or `ended`. A call that ended while it rang, before anyone opened its media, is not counted. |
| `clowder_db_connections` | `state` | Connections of the database pool: `open`, `in_use`, `idle`. |
| `clowder_db_wait_total`, `clowder_db_wait_seconds_total` | | How often, and for how many seconds in all, a query waited for a connection. |
| `clowder_redis_reconnects_total`, `clowder_redis_bridge_published_total`, `clowder_redis_bridge_received_total` | | With `-redis-url` only: reconnections, events sent to Redis, and events received from other processes. |
| `go_goroutines`, `go_memstats_heap_alloc_bytes`, `go_memstats_heap_sys_bytes`, `go_gc_cycles_total`, `process_start_time_seconds` | | The Go runtime and the start time of the process. |
| `clowder_build_info` | `version`, `go_version` | Always 1. |

No label carries a user, an ID, a room name, an address or a token. Each process has its own numbers: scrape every process of a cluster.

## SSH sign-in codes

With `-ssh-addr :2222` Clowder runs a small SSH server that gives one-time sign-in codes. A user adds a public key in Settings › Sign-in (it needs the password) and then runs `ssh -p 2222 alice@chat.example.org`, which prints a code that works for 60 seconds, once (`ssh -p 2222 alice@chat.example.org json` prints it as JSON). The server accepts public keys only; it has no shell, no forwarding and no other command. Check the host key against the fingerprint that the Sign-in section shows (the first connection asks you to trust it). A user who has fetched a code with one of their keys can turn on "Ask for an SSH code when I log in" in Settings › Sign-in (it needs the password): after the password, the login asks for a code in a second form (the API takes it as `ssh_code`, and answers `401` `ssh_code_required` without it); the code alone never logs anyone in, sign-up and a password reset link never ask for it, and an admin can turn it off for a user who lost their keys (Admin › Users). The code is asked for only while this process runs the SSH server: with `-ssh-addr` empty, the password alone logs in. The compose files publish `CLOWDER_SSH_PORT` (2222); in the cluster composition both Clowder processes run the server (every process of a cluster must have the same `-ssh-addr` setting, or a user could skip the code by landing on a process without one), they share one host key in the data volume, and only the first publishes the port. A client that fails to authenticate counts as a failed login of its address in the same limiter as the web login, so five failed connections block the address for 15 minutes.

## Running behind a TLS reverse proxy

Terminate TLS in a reverse proxy, and let it pass the request to Clowder on a local address. Four things matter:

1. **The public URL.** Start Clowder with `-base-url https://chat.example.org`. Cookies are then `Secure`, and the server sends `Strict-Transport-Security`. With an `http://` base URL cookies are not `Secure` (fine on a laptop, not on the internet).
2. **The `Host` header must reach Clowder as the browser sent it.** Clowder refuses a state-changing request whose `Origin` is not its own host, so a proxy that rewrites `Host` makes every form and button fail with 403.
3. **No buffering, long timeouts.** Every page keeps one server-sent event stream open. The server writes a comment line every 25 seconds, so a proxy must not buffer the response and must not close a connection that is quiet for less than a minute.
4. **`-trust-proxy`.** With it on, the client address is the last entry of `X-Forwarded-For`, the one the proxy added. Turn it on only when every request comes through the proxy (bind Clowder to `127.0.0.1`), or a client can forge the header and escape the per-address limit of failed logins. With it off, every client looks like the proxy's address, and five failed logins from anyone block the address for 15 minutes; the per-username limit of 5 failures applies in both cases.

Caddy:

```
chat.example.org {
	encode zstd gzip
	reverse_proxy 127.0.0.1:8080 {
		# send each event of a stream at once
		flush_interval -1
	}
}
```

Caddy gets the certificate itself, passes `Host` unchanged and sets `X-Forwarded-For` to the address it saw. Start Clowder with `-addr 127.0.0.1:8080 -base-url https://chat.example.org -trust-proxy`.

nginx:

```
map $http_upgrade $connection_upgrade {
	default upgrade;
	""      "";
}

server {
	listen 443 ssl;
	http2 on;
	server_name chat.example.org;
	ssl_certificate     /etc/ssl/chat.example.org/fullchain.pem;
	ssl_certificate_key /etc/ssl/chat.example.org/privkey.pem;

	# at most 10 files of 25 MiB and a megabyte of fields, the defaults;
	# raise it with -max-attachment-size and -max-attachments
	client_max_body_size 260m;

	location / {
		proxy_pass http://127.0.0.1:8080;
		proxy_http_version 1.1;
		# an empty Connection keeps the connection to Clowder alive; the
		# WebSocket of the API (/api/v1/ws) needs the upgrade headers, which
		# $connection_upgrade (the map above the server block) provides
		proxy_set_header Upgrade $http_upgrade;
		proxy_set_header Connection $connection_upgrade;
		proxy_set_header Host $http_host;
		proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
		proxy_set_header X-Forwarded-Proto $scheme;

		# server-sent events
		proxy_buffering off;
		proxy_cache off;
		proxy_read_timeout 1h;
		proxy_send_timeout 1h;
	}
}
```

Start Clowder with `-addr 127.0.0.1:8080 -base-url https://chat.example.org -trust-proxy`. Do not turn on `gzip` for `text/event-stream` in nginx. Caddy passes the WebSocket of the API without any setting. The server pings every open WebSocket every 30 seconds, so a proxy timeout of a minute or more (`proxy_read_timeout 1h` above) keeps it open.

## Backups

`clowder backup` writes one `.tar.gz` archive of the installation, and `clowder restore` turns it back into a data directory that `clowder serve` runs. Backup works while the server runs, on SQLite and on PostgreSQL.

```sh
clowder backup -data /var/lib/clowder -o /backups/clowder-2026-10-28.tar.gz
```

It prints the archive's path and size, the counts it recorded (users, rooms, posts and comments, messages, files) and, on PostgreSQL, the `pg_dump` command for the database. Take it from a timer (`cron`, a systemd timer) and copy the archives off the machine.

**What is in an archive.** Everything a restored installation needs, under one directory `clowder-backup/`:

| Entry | What it is |
|---|---|
| `manifest.json` | First entry. The format version, the Clowder version, the time (UTC), the database (`clowder.db`, or `external` on PostgreSQL), the schema version (the last migration applied), the counts, and every other entry with its path, size and SHA-256. |
| `clowder.db` | SQLite only: a consistent copy of the database, made with `VACUUM INTO` (it is a snapshot; the server keeps writing meanwhile and the copy does not stop it). |
| `files/` | The attachments and, in `files/avatars/`, the avatars. They are added **after** the copy of the database, so every attachment the copy names is in the archive (a file that is only in the directory is harmless; a row without its file is a broken download). |
| `ssh_host_ed25519_key` | The host key of the SSH server (`-ssh-addr`), when the data directory has one, so that its fingerprint does not change and your users' `known_hosts` keep working. A host key kept elsewhere with `-ssh-host-key` is not in the archive: keep a copy of it yourself. |

Left out, because it is temporary or rebuilt: uploads that were staged and never posted (they are removed after a day anyway), the files being written (`files/tmp/`), the SQLite `-wal` and `-shm` files (the copy of the database has no need of them), and anything else in the data directory. Redis keeps nothing worth saving.

**An archive holds every message, post, password hash, session and file of the installation, and the host key.** Keep it as safely as the server itself: the file is made readable by its owner only, store it encrypted if it leaves the machine, and delete old ones.

`-o -` writes the archive to the standard output (the report goes to stderr), and `clowder restore -i -` reads it from the standard input; Docker uses them below. The archive is complete before the first byte reaches the standard output, in a temporary file in the data directory. With a file name, the archive is written to a temporary file next to it and renamed at the end, so a failed backup never leaves a half archive under that name. `-o` may not be inside `files/`. An existing file of that name is replaced.

### Restoring

```sh
mv /var/lib/clowder /var/lib/clowder.old     # restore only goes into an empty directory
clowder restore -i /backups/clowder-2026-10-28.tar.gz -data /var/lib/clowder
clowder serve -data /var/lib/clowder         # with your usual flags
```

The directory must not exist or must be empty (anything in it is refused before a byte is written); a missing one is made with mode 0700. Restore checks the archive before it trusts it: the first entry must be the manifest, with a format and a schema this Clowder knows (an archive from a **newer** Clowder is refused; one from an older Clowder is fine, because `serve` migrates the database when it starts); every entry must be a regular file or a directory (no links, devices or FIFOs) with a relative path under `clowder-backup/` and no `..`, listed in the manifest, once, with the size and the SHA-256 the manifest gives; nothing may be missing, and the archive may not inflate beyond what the manifest declares. It writes into a temporary directory inside the target and moves the result into place at the end, so a refused or failed restore leaves the target as it was. Files are written with mode 0600 and directories with mode 0700 (the host key must be 0600).

Sessions are in the database, so everyone stays logged in after a restore of a recent backup. **Test a restore before you need it**: restore into a throw-away directory and start `clowder serve` on it with another `-addr`.

### With Docker

The image has no shell, so give Clowder its commands directly and move the archive through the standard streams. The volume `clowderdata` is the data directory (`/data`):

```sh
docker compose run --rm -T clowder backup -o - > clowder-backup.tar.gz
```

(`-T` keeps Docker from adding a terminal to the stream. The report and the `pg_dump` line go to the terminal's stderr.) To restore into a new installation, with the volume empty:

```sh
docker compose run --rm -T clowder restore -i - -data /data < clowder-backup.tar.gz
docker compose up -d
```

With the PostgreSQL of `compose.yaml` the archive has the files and the host key, and the database is saved with `pg_dump`, **before** the archive (see below). The full sequence is under "Running with Docker".

### PostgreSQL

On PostgreSQL the archive holds the files and the host key, and the manifest says `"database": "external"`. The database is saved with `pg_dump`, which backup prints for your URL with the password left out (give it to `pg_dump` with `PGPASSWORD` or `~/.pgpass`):

```sh
pg_dump --format=custom --file=clowder.pgdump 'postgres://clowder@db.example.com:5432/clowder'
clowder backup -o clowder-backup.tar.gz
```

**Run `pg_dump` first and `clowder backup` after it**: a dump taken before the archive names only attachments that are in the archive, and one taken after it may name attachments that were uploaded in between. To restore, make an empty database, load the dump with `pg_restore --dbname=<url> clowder.pgdump` (or `psql` for a plain SQL dump), then `clowder restore -i clowder-backup.tar.gz -data <dir>` and `clowder serve -data <dir> -database-url <url>`. Restore prints this reminder when the archive says the database is external. The dump and the archive hold private data: keep both private.

## The demo

`clowder demo` fills an empty data directory with demo data: four users (`admin`, `alice`, `bob` and `carol`, all with the password `demo-password-1234`), rooms, posts, threads, reactions, attachments, tasks, notifications and private messages. It refuses a directory that already has files in it.

```sh
./clowder demo -data /tmp/clowder-demo
./clowder serve -data /tmp/clowder-demo
```

`clowder demo -size large` adds on top of that the data set that the performance targets are measured on: 200 users, 100 rooms, 100,000 posts and comments and 20,000 tasks. It takes seconds. The numbers are in [`docs/PERFORMANCE.md`](docs/PERFORMANCE.md). Do not run the demo on a real installation, and do not leave the demo passwords on a server that others can reach.

## Development

- [`CLAUDE.md`](CLAUDE.md) says how the code is written and checked.
- [`docs/API.md`](docs/API.md) is the guide for the writers of clients of the API.
- [`docs/SPEC.md`](docs/SPEC.md) is the specification, [`docs/IMPLEMENTATION.md`](docs/IMPLEMENTATION.md) the plan, [`docs/IMPLEMENTED.md`](docs/IMPLEMENTED.md) the log of finished steps, [`docs/PERFORMANCE.md`](docs/PERFORMANCE.md) the measurements and [`docs/SECURITY.md`](docs/SECURITY.md) the security review.

```sh
go build ./...                      # CGO_ENABLED=0 go build ./... must work
go vet ./...
golangci-lint run
go test -race ./...                 # no network and no browser needed
go generate ./...                   # the templ views; the generated files are committed
go run ./cmd/clowder serve          # run it
```

The browser tests drive Chromium with chromedp. They are behind a build tag and need Chromium installed:

```sh
go test -tags clowder_browser ./...
go run -tags clowder_browser ./cmd/clowder-proof -as alice -page '{room:general}' -o /tmp/room.png   # a screenshot of a page of the demo
```

## License

Clowder is released under the BSD 3-Clause license, see [`LICENSE`](LICENSE). It embeds `datastar.js`, which is released under the MIT license; its license text is next to it in `internal/web/static/`, and [`NOTICE`](NOTICE) lists it.
