# Clowder

Clowder is a self-hosted web application where a team talks in rooms. A room is a feed of posts, and the conversation about a post happens in the comments under it, which can be answered in turn. Posts and comments carry text or markdown, attachments, tags and tasks, and the tasks of a room are the cards of its Kanban board. People also write private messages to each other, one to one or in small groups. Nobody joins without an invitation. Clowder is one program with an SQLite database and a directory of attachments; the pages are rendered on the server and kept live over server-sent events, so what one person posts or moves appears for everyone looking at it.

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

## Screenshots

| | |
|---|---|
| ![The feed of a room](docs/implemented-proof-images/4.3-feed.png) | ![A thread of comments](docs/implemented-proof-images/5.2-thread.png) |
| The feed of a room | A thread of comments |
| ![The board of a room](docs/implemented-proof-images/9.3-board.png) | ![Search results](docs/implemented-proof-images/10.2-search.png) |
| The board of a room | Search |
| ![The notifications page](docs/implemented-proof-images/11.2-notifications.png) | ![A private conversation](docs/implemented-proof-images/12.2-messages.png) |
| Notifications | Private messages |

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

From then on admins make invitation links for members and other admins in the Admin page. `./clowder invite -member` does the same on the command line.

## Running it

```sh
./clowder serve -data /var/lib/clowder -addr 127.0.0.1:8080 -base-url https://chat.example.org
```

The data directory holds `clowder.db` (the database) and `files/` (the attachments). The program makes the directory when it is missing, with access for its owner and group only, and keeps the database file readable by its owner only. Keep the directory private: it holds password hashes and every attachment. Stop the server with SIGINT or SIGTERM: it stops accepting requests, closes the open streams, waits up to 10 seconds for running requests and closes the database.

Clowder runs as one process per database. It speaks plain HTTP, so put a reverse proxy with TLS in front of it (see below).

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
- **TLS and a reverse proxy:** the container speaks plain HTTP, so put a reverse proxy with TLS in front of it, as "Running behind a TLS reverse proxy" says: proxy to the published port, set `CLOWDER_BASE_URL` to the public `https://` address and, only when every request comes through the proxy, `CLOWDER_TRUST_PROXY=true`.

`scripts/compose-smoke.sh` checks the whole composition on a throw-away project: it builds the image, starts both services, makes an admin invitation, signs up through the form, opens a page and removes everything.

### PostgreSQL without Docker

`clowder serve` uses PostgreSQL instead of SQLite when it is given a database URL, with `-database-url` or `CLOWDER_DATABASE_URL`, for example `postgres://clowder:secret@db.example.org:5432/clowder?sslmode=require`. `clowder invite` and `clowder demo` read the same setting. The server needs PostgreSQL 16 or later (18 is the tested version) with ICU and the `unaccent` extension, which the official images and the usual packages (`postgresql-contrib`) have. The database user must be allowed to create the extension, a collation and functions in the database, because Clowder migrates the database when it starts. The URL is never written to a log with its password. The attachments stay in `files/` of the data directory.

## Configuration

Each setting is a flag of `clowder serve` or a `CLOWDER_*` environment variable. A flag wins over the variable, and the variable wins over the default. `clowder serve -h` lists the flags.

| Flag | Variable | Default | What it sets |
|---|---|---|---|
| `-addr` | `CLOWDER_ADDR` | `:8080` | Address to listen on, as `host:port`. |
| `-data` | `CLOWDER_DATA` | `./data` | Data directory, with `clowder.db` and `files/`. |
| `-database-url` | `CLOWDER_DATABASE_URL` | empty | A `postgres://` URL. Empty: SQLite in `clowder.db` of the data directory. |
| `-base-url` | `CLOWDER_BASE_URL` | `http://localhost:8080` | The public URL, an absolute `http` or `https` URL. Invitation links are made from it. Cookies get the `Secure` attribute unless it starts with `http://`. |
| `-max-attachment-size` | `CLOWDER_MAX_ATTACHMENT_SIZE` | `26214400` | Largest attachment in bytes (25 MiB). |
| `-max-attachments` | `CLOWDER_MAX_ATTACHMENTS` | `10` | Most attachments on one post, comment or message. |
| `-session-lifetime` | `CLOWDER_SESSION_LIFETIME` | `720h` | How long a login lasts from its last use (30 days). |
| `-invitation-lifetime` | `CLOWDER_INVITATION_LIFETIME` | `168h` | How long an invitation link is valid (7 days). |
| `-log-level` | `CLOWDER_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. Logs are JSON lines on stderr. |
| `-trust-proxy` | `CLOWDER_TRUST_PROXY` | `false` | Take the client address, which the login limiter counts, from the last entry of `X-Forwarded-For`. Turn it on only behind your own reverse proxy. |

Durations are written the way Go reads them: `720h`, `90m`. `clowder invite` reads `-data`, `-database-url`, `-base-url` and `-invitation-lifetime` (and their variables); `clowder demo` reads `-data`, `-database-url` and `-size`. `clowder healthcheck -addr host:port` asks `GET /healthz` of a running server and exits 0 when it answers 200 and 1 otherwise; the Docker image runs it as its health check.

The log has one entry per request (method, route pattern, status, duration, user ID). It never has passwords, tokens, post bodies or query strings.

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
		proxy_set_header Connection "";
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

Start Clowder with `-addr 127.0.0.1:8080 -base-url https://chat.example.org -trust-proxy`. Do not turn on `gzip` for `text/event-stream` in nginx.

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
