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
- **Backups:** the database with `pg_dump`, then the attachments, **after** it, for the reason given under "Backups":

  ```sh
  docker compose exec postgres sh -c 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' > clowder.dump
  docker run --rm -v clowder_clowderdata:/data:ro -v "$PWD":/backup busybox \
      tar czf /backup/files.tgz -C /data files
  ```

  (The volume is named `<project>_clowderdata`, here for a project called `clowder`, which is the name of the directory unless you set `-p` or `COMPOSE_PROJECT_NAME`; `docker volume ls` shows it.) To restore into a new, empty installation: `docker compose up -d postgres`, then `docker compose exec -T postgres sh -c 'pg_restore -U "$POSTGRES_USER" -d "$POSTGRES_DB" --clean --if-exists' < clowder.dump`, unpack `files.tgz` into the volume the same way, and `docker compose up -d`. Both files hold private data (password hashes, attachments): keep them private.
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

`scripts/compose-smoke.sh -cluster` checks it on a throw-away project: every container healthy, the login made through Caddy working on each process directly, both processes receiving requests, and a post written on one process arriving live in a stream of the other.

### PostgreSQL without Docker

`clowder serve` uses PostgreSQL instead of SQLite when it is given a database URL, with `-database-url` or `CLOWDER_DATABASE_URL`, for example `postgres://clowder:secret@db.example.org:5432/clowder?sslmode=verify-full`. `clowder invite`, `clowder reset-password` and `clowder demo` read the same setting. The attachments stay in `files/` of the data directory.

- **Version and extensions.** PostgreSQL 16 or later (18 is the tested version) with ICU and the `unaccent` extension, which the official images and the usual packages (`postgresql-contrib`) have.
- **The rights of the database user.** Clowder migrates the database when it starts, so the user must be allowed to create, in its database: tables and indexes, the extension `unaccent` (a trusted extension, so the owner of the database may create it), a text search configuration (`clowder_search`), an ICU collation (`nocase`) and functions (`json_valid`), and to use advisory locks. The simplest way is to make the user the **owner of the database**: `CREATE ROLE clowder LOGIN PASSWORD '...'; CREATE DATABASE clowder OWNER clowder;`. It needs no other right, and no superuser. From PostgreSQL 15 only the owner may create objects in the `public` schema, so a user that is not the owner needs `GRANT CREATE ON SCHEMA public` and `GRANT CREATE ON DATABASE clowder`.
- **TLS.** The URL's `sslmode` decides. The driver's default is `prefer`, which uses TLS when the server offers it and goes on without it when not, so an attacker on the network can make it go without. For a database that is not on the same machine or the same private network use `sslmode=verify-full` (with `sslrootcert=/path/to/ca.pem` when the certificate is not from a public authority) or at least `require`. `sslmode=disable` is for a database in the same compose network.
- **The password in the URL.** A password with characters that have a meaning in a URL (`@`, `:`, `/`, `?`, `#`, `%`) must be percent-encoded, or use letters and digits only. The URL is never written to a log or an error with its password (it shows `xxxxx`), and `clowder serve -h` does not print the URL of the environment. Keep it in the environment (`CLOWDER_DATABASE_URL`) or in a file your service manager reads, not on a command line that others can list with `ps`.
- **Connections.** Each process keeps up to 32 connections (two per CPU, at least 8): `max_connections` of the server must be above that times the number of processes.
- **Backups.** `pg_dump -Fc`, then the attachments after it (see "Running with Docker" and "Backups").
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
| `-trust-proxy` | `CLOWDER_TRUST_PROXY` | `false` | Take the client address, which the login limiter counts, from the last entry of `X-Forwarded-For`. Turn it on only behind your own reverse proxy. |

Durations are written the way Go reads them: `720h`, `90m`. `clowder invite` reads `-data`, `-database-url`, `-base-url` and `-invitation-lifetime` (and their variables); `clowder demo` reads `-data`, `-database-url` and `-size`. `clowder healthcheck -addr host:port` asks `GET /healthz` of a running server and exits 0 when it answers 200 and 1 otherwise; the Docker image runs it as its health check.

The log has one entry per request (method, route pattern, status, duration, user ID). It never has passwords, tokens, post bodies or query strings.

## SSH sign-in codes

With `-ssh-addr :2222` Clowder runs a small SSH server that gives one-time sign-in codes. A user adds a public key in Settings › Sign-in (it needs the password) and then runs `ssh -p 2222 alice@chat.example.org`, which prints a code that works for 60 seconds, once (`ssh -p 2222 alice@chat.example.org json` prints it as JSON). The server accepts public keys only; it has no shell, no forwarding and no other command. Check the host key against the fingerprint that the Sign-in section shows (the first connection asks you to trust it). The code is not asked for at login yet. The compose files publish `CLOWDER_SSH_PORT` (2222); in the cluster composition only the first Clowder process runs the server, and any process takes a code that it gave. A client that fails to authenticate counts as a failed login of its address in the same limiter as the web login, so five failed connections block the address for 15 minutes.

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

Everything Clowder keeps is in the data directory: `clowder.db` and `files/`. The database runs in WAL mode, so do not copy the file with `cp` while the server runs. Take an online backup with SQLite's own tools, which give a consistent copy while the server keeps working:

```sh
sqlite3 /var/lib/clowder/clowder.db ".backup /backups/clowder-2026-10-27.db"
# or
sqlite3 /var/lib/clowder/clowder.db "VACUUM INTO '/backups/clowder-2026-10-27.db'"
```

Then copy the attachments, **after** the database, so that every attachment the copy of the database mentions is in the copy of the directory (a file that is only in the directory is harmless; a row without its file is a broken download):

```sh
rsync -a /var/lib/clowder/files/ /backups/clowder-2026-10-27-files/
```

Keep both together, and keep them private: the database has password hashes.

To restore, on a stopped server:

1. Stop Clowder.
2. Move the data directory away, and make a new one with mode 700: `mkdir -m 700 /var/lib/clowder`.
3. Put the copy of the database there as `clowder.db`, and the copy of the attachments as `files/`. There must be no `clowder.db-wal` or `clowder.db-shm` file next to the restored database.
4. Start Clowder. It checks the schema and brings it up to date.

Sessions are in the database, so everyone stays logged in after a restore to a recent backup. Test a restore before you need it.

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
