# Clowder: Specification

| | |
|---|---|
| Status | 1.0 released as `v1.0.0` (2026-10-27); 1.1 (PostgreSQL, several processes, the API for desktop clients) in progress |
| Date | 2026-10-27 |
| Owner | Manos Ragiadakos |
| Module | `github.com/rm4n0s/clowder` |
| License | BSD-3-Clause |

Clowder is a self-hosted web application where a team talks in rooms. A room is not a stream of chat lines: it is a feed of posts, and the conversation about a post happens in the comments under it, which can be answered in turn. A post (and a comment) can be plain text or markdown, carry attachments, and hold tasks; the tasks of a room are the cards of the room's Kanban board. Posts have tags, given in their own field, and everything a user can read can be searched. Besides the rooms, users write private messages to each other, one to one or in small groups. People join only by invitation. Clowder is one Go binary with an SQLite database and a directory of attachments. The pages are rendered on the server with templ and kept live with Datastar over server-sent events, so what one person posts, comments, reacts or moves on a board appears at once for everyone looking at it.

From 1.1 the database can also be PostgreSQL, run with Docker containers, and several Clowder processes can serve one PostgreSQL database, with Redis carrying the live events between them (§8.1). A REST API with a WebSocket for live events (§7.12, §7.13) lets desktop clients, written separately, do everything the pages do; it is described by an OpenAPI document.

---

## 1. Goals

| # | Goal |
|---|---|
| G1 | **Posts first.** A room is a feed of posts with threaded comments, so a discussion stays attached to what it is about |
| G2 | **Work lives next to the talk.** Tasks are written in posts and comments and shown on the room's board, which links each card back to where it was written |
| G3 | **Closed by default.** Nobody gets in without an invitation; private rooms are invisible to non-members |
| G4 | **Live.** Every change is pushed to the pages that show it, without a reload (§8) |
| G5 | **Easy to run.** One binary, one data directory, no external service; `CGO_ENABLED=0 go build` works (§11) |
| G6 | **Findable.** Full-text and tag search over everything the user can read (§7.8) |
| G7 | **Testable.** The domain and the HTTP layer are tested without a browser or a network (§12) |

## 2. Non-goals for 1.0

- Email of any kind (invitations by email, notification digests, password reset by email). Invitations are links that an admin hands over (§7.1).
- Single sign-on (OIDC, SAML), passkeys and two-factor authentication.
- Several separate communities in one installation. One installation is one community.
- PostgreSQL. The storage is written so it can be added later (D6). *(Added in 1.1, D38.)*
- Voice, video, screen sharing.
- Native mobile or desktop apps. The web pages work on a phone's browser. *(1.1 adds the API that desktop clients use, §7.12; the clients themselves are separate projects and not part of this repository.)*
- End-to-end encryption.
- A public API for other programs, bots and webhooks. *(1.1 adds an API for clients that act as a logged-in user, §7.12. Bots, webhooks and keys that are not a user's login are still not in scope.)*
- Edit history. An edited post shows that it was edited and when, not what it said before.
- Running more than one Clowder process on the same database (§8). *(Added in 1.1 for PostgreSQL with Redis, D41.)*

### Non-goals for 1.1

- Copying an existing SQLite installation into PostgreSQL. A 1.1 installation chooses its database when it starts empty (the owner's answer, 2026-10-27).
- Live events through PostgreSQL (`LISTEN`/`NOTIFY`): several processes use Redis (the owner's answer, 2026-10-27).
- Browsers calling the API from other sites (CORS), API keys that are not a user's login, bots, webhooks.
- Attachments in object storage (S3 and the like): they stay files in the data directory, which several processes share as one volume.

## 3. Scope of 1.0

1. **Invitations and accounts.** An admin makes one-time invitation links; the first admin's link is made by `clowder invite -admin`. Opening a link lets a person choose a username, a display name and a password. Users log in and out; an admin can disable an account.
2. **Rooms.** Admins create visible rooms; any user creates private rooms. Every user sees the list of visible rooms and can read and join them. A private room is seen only by its members, and any member can add another user to it without asking that user. Members can leave a room.
3. **Posts.** A member writes a post in a room. A post has a body in plain text or markdown, optional attachments, optional tasks, and optional tags given in a separate field. The author edits or deletes it; admins delete any post.
4. **Comments.** A comment is written under a post or under another comment, to any depth, and can have everything a post has (body, attachments, tasks, tags). The author edits or deletes it; admins delete any comment.
5. **Reactions.** Users react to posts and comments with emoji; each user adds or removes each emoji once per post.
6. **Attachments.** Files are uploaded with a post or a comment, stored on local disk, limited in size, and downloaded only by users who can read the room. Images are shown inline.
7. **Tasks and the board.** Each room has a Kanban board. A task has a title, a column, a position, assignees (members of the room) and an optional due date. Tasks are written in a post or comment and appear on the board as cards linked back to it. The board starts with the columns To do, In progress and Done; members of the room add, rename, reorder and remove columns. Cards are moved between and within columns by drag and drop.
8. **Search.** Full-text search over post and comment bodies, attachment names and task titles, filtered by tags, room, author and date, over every room the user can read.
9. **Mentions and notifications.** `@username` in a body mentions a user. A user is notified, inside Clowder, of mentions, replies to their posts and comments, tasks assigned to them, and being added to a private room. Unread notifications show as a count that updates live.
10. **Live updates** of feeds, threads, reactions, boards and notification counts (§8).
11. **Audit log.** Every creation, edit and deletion of a post or comment, a task, a board column and a room membership is recorded with who did it and when; admins read and filter the log in its own page (§7.10).
12. **Private messages.** Any user starts a conversation with one or more other users. A conversation is a stream of short messages, oldest at the top and newest at the bottom, with emoji reactions, attachments, replies to a message, editing and deleting, read state and an unread count (§7.11).

## 3a. Scope of 1.1

The owner's request, 2026-10-27: "make it possible to work with postgres sql using docker containers" and "add rest api and websocket for desktop clients that I will create in the future (write also openapi spec)". Everything of 1.0 stays as it is; an installation that does not set the new options runs exactly as 1.0 did.

1. **PostgreSQL.** The database is SQLite (the default) or PostgreSQL, chosen by a database URL (D38). Every feature of §7 works the same on both, search included; the whole test suite also runs on PostgreSQL (D39).
2. **Docker.** A `Dockerfile` makes a small image of the binary, and `compose.yaml` runs Clowder with a PostgreSQL container; `compose.cluster.yaml` runs two Clowder processes behind a load balancer with Redis (D40).
3. **Several processes.** With PostgreSQL and Redis, any number of Clowder processes serve one database and one shared attachments directory; live events and the login limiter go through Redis (D41, §8.1).
4. **REST API.** Under `/api/v1`, JSON over HTTP, every feature of §7 that a user or an admin has in the pages, for desktop clients (§7.12, D42, D43).
5. **WebSocket.** `/api/v1/ws` pushes the live events of §8 to a client, with the changed resource in the same JSON as the REST API (§7.13, D44).
6. **OpenAPI.** `internal/api/openapi.json` (OpenAPI 3.1) describes every route, and a test keeps it equal to the routes; `docs/API.md` is the guide for client writers (D45).

## 4. Decisions log

| # | Decision | Notes |
|---|---|---|
| D1 | **Go 1.27**, the standard toolchain, no cgo | §11 |
| D2 | **`github.com/rm4n0s/errors` v0.2.0** for every error, used the way its `AI_GUIDE.md` says | The project owner's requirement. §9 |
| D3 | Dependencies: the standard library first. Every other dependency is a decision recorded here | CLAUDE.md "Go conventions". 1.1 adds `gorm.io/driver/postgres` (D38), `github.com/redis/go-redis/v9` (D41) and `github.com/coder/websocket` (D44) |
| D4 | The work is done in numbered steps (`docs/IMPLEMENTATION.md`), each published to `main` with a `step-<id>` tag, by a managing session and worker agents | CLAUDE.md "Working through the steps" |
| D5 | No CI on GitHub: the checks run locally | CLAUDE.md "Git and GitHub" |
| D6 | **GORM** (`gorm.io/gorm`) for storage, on **SQLite** through the pure-Go driver `github.com/glebarez/sqlite` (built on `modernc.org/sqlite`) | Keeps D1. Only `internal/store` imports GORM; nothing SQLite-specific leaves it, so PostgreSQL can be added behind the same API later. Full-text search uses SQLite's FTS5 (§7.8). 1.1 adds PostgreSQL (D38) |
| D7 | **Datastar** for the browser side: `github.com/starfederation/datastar-go` on the server and the matching `datastar.js` client, embedded in the binary | The versions of the SDK and the client are pinned together; changing one changes both. SDK `v1.2.2`, client `v1.0.2`. §8 |
| D8 | **templ** (`github.com/a-h/templ`) for HTML, run as `go tool templ` (a `tool` line in `go.mod`); the generated `*_templ.go` files are committed | `go generate ./...` regenerates them; a check makes sure they are up to date (IMPLEMENTATION.md rule 8) |
| D9 | **goldmark** (`github.com/yuin/goldmark`) renders markdown, in its safe mode: raw HTML in a body is escaped and dangerous link schemes are dropped. No other sanitizer | §7.3 |
| D10 | Passwords are hashed with **argon2id** from `golang.org/x/crypto` | §7.1 |
| D11 | Sessions are random tokens in an `HttpOnly`, `SameSite=Lax` cookie; the database keeps only their SHA-256. Requests that change state are checked with the standard library's `http.CrossOriginProtection` | §7.1 |
| D12 | The HTTP router is the standard library's `http.ServeMux` with method and path patterns | No router dependency |
| D13 | Styles are hand-written CSS, embedded in the binary. No Node, no CSS or JavaScript build step | |
| D14 | One installation is one community. Roles are **admin** and **member** | §7.1 |
| D15 | Visible rooms are created by admins; private rooms by anyone. A member of a private room can add any user to it without that user's consent | The owner's rule, 2026-10-26 |
| D16 | A comment is a post with a parent: one table, one editor, one renderer. Comments nest to any depth | §6.3 |
| D17 | Tasks are entered in the editor in their own fields, like tags, not parsed out of the markdown | Gives each task its assignees and due date as data. §7.6 |
| D18 | Live updates go through an in-process hub. Clowder runs as one process per database | §8. From 1.1, several processes with PostgreSQL and Redis, each with its own hub (D41, §8.1) |
| D19 | Attachments are stored on local disk under the data directory, with a configurable size limit | §7.5 |
| D20 | Notifications are only shown inside Clowder | §7.9 |
| D21 | Posts and comments are deleted by their author or an admin. One that has replies stays as "deleted" so its thread keeps its shape; one without replies disappears. Its tasks, attachments, reactions, tags and mentions are deleted with it | The owner's answer, 2026-10-26. §7.3 |
| D22 | A step proves a page with a screenshot taken in headless Chromium by `cmd/clowder-proof`, which runs `clowder serve` on a temporary data directory filled by `clowder demo` and logs in through the real login form. Steps with no page prove themselves with test output | The owner's answer, 2026-10-26. No development-only login route exists, so there is nothing to leave open in production. Chromium 156 is installed on the development machine (`/usr/bin/chromium-browser`) |
| D23 | Browser tests use **`github.com/chromedp/chromedp`** behind the build tag `clowder_browser`, not run by the default `go test`; they need Chromium | The owner's answer, 2026-10-26. §12 |
| D24 | **Audit log.** Every create, update and delete of posts and comments, tasks, board columns and room memberships is written as an audit event in the same transaction as the change, and shown to admins in the UI. Events are never edited or deleted | The owner's rule, 2026-10-26. §7.10 |
| D25 | Every member of a room may change the room's board columns (each change is audited, D24) | The owner's answer, 2026-10-26. §7.6 |
| D26 | A visible room is readable by every user before joining; joining is for posting, reacting, changing the board and being notified | The owner's answer, 2026-10-26. §7.2 |
| D27 | Rooms are audited too: `room.created`, `room.renamed` (and description changes), `room.deleted` | The owner's answer, 2026-10-26. §7.10 |
| D28 | **Private messages** are conversations between users, separate from rooms: a stream of messages, not posts (no comments, tasks, tags or board). A conversation with one other user is unique for the pair; a group conversation has up to 50 members | The owner's request, 2026-10-26. §7.11 |
| D29 | Admins see every room, private ones included, in the admin rooms list (`/admin/rooms`: name, visibility, members, creation date), where they rename and delete rooms and remove members. Reading a private room's posts, board and members page still needs membership, admins included | Decided by the manager, 2026-10-27, from §7.2 (admins manage any room; an empty private room is kept for admins to delete); the owner may revise. §7.2 |
| D30 | Notifications, decided by the manager: a mention links to `/search?q=from:<username>` (there are no profile pages); one action gives a user at most one notification (assigned wins over reply, reply over mention; several tasks give one); a self-mention is linked but not notified; disabled users are not notified; a mention of someone who cannot read the room is no mention; notifications of a deleted post or comment are deleted with it, and those of a room the user can no longer read are hidden from the list and the count; the notifications page shows 30 at a time and marks read everything up to the newest it shows | Decided by the manager, 2026-10-27. §7.9 |
| D31 | A page keeps one live stream (§8). The streams of the room, thread and board pages also keep the sidebar's counts (notifications, later messages) live through `user:<id>`. Every other logged-in page opens `GET /me/stream`, which only follows `user:<id>`. The notifications page shows the notifications as they were when it opened, marks read the ones it shows, and puts new ones at the top live | Decided by the manager, 2026-10-27. §7.9, §8 |
| D32 | Private messages, decided by the manager: a direct conversation needs another enabled user (not oneself) and nobody leaves it; a group is started with at least two other enabled users (50 members at most, the starter included) and its name is optional (up to 80 characters); a user added to a group, or added back after leaving, sees its whole history; a message needs a body or an attachment; only the author edits or deletes a message, and a deleted one keeps its place as "message deleted" without its body, attachments or reactions; in a direct conversation whose other user is disabled, the messages are read but no new ones are sent; reading marks the conversation read up to its newest message, and sending marks it read up to the sent one; the unread count is the number of conversations with a message from someone else after the member's last read one | Decided by the manager, 2026-10-27. §7.11 |
| D33 | Messages pages, decided by the manager: a new conversation is started on the Messages page from usernames (one makes or opens the direct conversation, several make a group with an optional name) and from a "Message" button beside each member of a room's member list; an open conversation counts as read: its stream marks it read up to each message it shows; message bodies are rendered as the text format of posts (escaped, line breaks kept, links clickable) | Decided by the manager, 2026-10-27. §7.11 |
| D34 | The connection pool of a file database is bounded: two connections per CPU, at least 8 and at most 32, all kept open (`database/sql` keeps two idle ones and opens a new one whenever a query finds none free, and every new SQLite connection parses the schema). Measured in step 13.1: with 200 open streams the fan-out of a new post fell from 369 ms to 30 ms. Queries beyond the limit wait for a connection. No code may hold a connection and ask for another one | Decided by the manager, 2026-10-27; the owner may revise. §10, `docs/PERFORMANCE.md` |
| D35 | `clowder demo -size large` makes the data set of §10 on top of the small demo (which stays exactly as it was): rooms, memberships and private conversations go through the services; users, posts, comments, tags, reactions, mentions, attachments, tasks and their audit events are written in bulk in one transaction by `store.BulkCreate`, and the full-text index is rebuilt from them. No notifications are made for the bulk data. The generator is seeded: a scale gives the same data every time | Decided by the manager, 2026-10-27; the owner may revise. §10, §14 |
| D36 | Security headers, decided by the manager in the security review of step 13.2: every answer, error pages, redirects and streams included, carries `Content-Security-Policy` (scripts from this site only, plus `'unsafe-eval'` because Datastar builds a function from each `data-*` expression; no inline script and no inline style; images from this site and from `https:` and `http:` because a markdown body may embed one; frames, plug-ins, `<base>` and forms to other sites refused), `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin`, `X-Frame-Options: DENY`, `Cross-Origin-Opener-Policy` and `Cross-Origin-Resource-Policy: same-origin` and `Cache-Control: no-store` (the static files revalidate; a download keeps its own sandbox policy and `private, max-age=86400`); `Strict-Transport-Security` (one year, this host only) when the base URL is `https`. Because inline scripts are refused, a redirect or a change of the address that the server sends over an event stream is a hidden element with a `data-init` expression, not the SDK's `<script>` | Decided by the manager, 2026-10-27; the owner may revise. §7.5, §8, §17, `docs/SECURITY.md` |
| D37 | Account changes and limits, decided by the manager in the security review of step 13.2: disabling a user, changing a role and a logout publish `user.changed` on the user's topic, and every stream reads its session again at once: a session that is gone sends the page to the login page and ends the stream, a changed role ends the stream so that the page opens it again; at most twice the number of CPUs (2 to 8) passwords are hashed or checked at a time, the other logins wait (64 MiB each); the database file is created with the mode 0600 (and an older one is changed to it), and so are the files SQLite adds to it; a search page after the 1000th is empty; the server closes connections idle for 2 minutes, a form that takes longer than a minute and an upload longer than 15 minutes to arrive, and a stream write longer than 30 seconds (there is no `ReadTimeout` or `WriteTimeout`, which would cut the streams); a request that no route matched is logged with its path, except that the token of `/invite/<token>` is never logged | Decided by the manager, 2026-10-27; the owner may revise. §7.1, §8, `docs/SECURITY.md` |
| D38 | **PostgreSQL** through **`gorm.io/driver/postgres`** (which uses `github.com/jackc/pgx/v5`, pure Go). The database is chosen by `-database-url` / `CLOWDER_DATABASE_URL`: empty means SQLite in `<data>/clowder.db` (as in 1.0), a `postgres://` or `postgresql://` URL means PostgreSQL; any other scheme is refused. PostgreSQL 18 is the tested version. Only `internal/store` knows which one it runs on: the models, the queries and the services are shared; what differs is kept in the store behind one `dialect` value: the raw migrations (each one exists for both), the full-text search (PostgreSQL: a `posts_search` table with a `tsvector` column and a GIN index, the `simple` configuration with `unaccent`, prefix matching with `:*`, snippets with `ts_headline`; the same results as §7.8), the mapping of a unique violation to `TagConflict` (SQLSTATE `23505`), the pool settings (the D34 bounds on both) and `BulkCreate`. A database URL never appears in a log (its password is removed). The attachments stay in `<data>/files/`. Found in step 14.1: the search uses its own text-search configuration `clowder_search` (`simple` with `unaccent` in front, so a generated `tsvector` column works although `unaccent()` is not immutable), and ASCII punctuation is turned into spaces before parsing so that `report.pdf` splits as in FTS5; snippets are rebuilt in Go from the text so they match the SQLite ones; the columns and orderings declared `COLLATE NOCASE` get an ICU collation `nocase` (`und-u-ks-level2`, not deterministic: case is ignored, accents are not); `json_valid(text)` is defined with `pg_input_is_valid`; `timestamptz` values are read in UTC. Found in step 14.2: PostgreSQL cannot read the feed of a room (`parent_id IS NULL ORDER BY id DESC`) in order from `idx_posts_feed`, because it treats `IS NULL` as a filter and not as an equality, and sorts all the root posts of the room; migration `0002_posts_feed_roots` adds a partial index `idx_posts_feed_roots (room_id, id DESC) WHERE parent_id IS NULL` on PostgreSQL (SQLite does nothing in it). PostgreSQL keeps times to the microsecond, SQLite to the nanosecond; nothing in Clowder depends on the finer precision So the server needs PostgreSQL 16 or later with ICU and the `unaccent` extension (the official image has both), and the user must be allowed to create the extension, the collation and functions in the database | The owner's request, 2026-10-27. The details decided by the manager, 2026-10-27; the owner may revise |
| D39 | **Tests on PostgreSQL and Redis.** The default `go test ./...` stays on SQLite and needs no network (G7). With `CLOWDER_TEST_POSTGRES` set to the URL of a PostgreSQL server where the user may create databases, `storetest.Open` gives each test a new database made with `CREATE DATABASE … TEMPLATE` from one migrated once per test binary, and drops it at the end; every store, service and handler test then runs on PostgreSQL. `CLOWDER_TEST_REDIS` (a `redis://` URL) does the same for the Redis tests, which otherwise skip with a message. `scripts/test-docker.sh` starts `postgres:18` and `redis:8` containers on free ports with `docker` (or `podman`), runs `go test -race ./...` with both variables, and removes the containers, whatever the result. From step 14.2 it is one of the standard checks | The owner's answer, 2026-10-27 ("Tests on Postgres"). The details decided by the manager, 2026-10-27 |
| D40 | **Docker.** `Dockerfile`: a build stage on the official `golang:1.27` image (`CGO_ENABLED=0`, `-trimpath`), and a run stage on a distroless static image, user `nonroot`, the binary as `/clowder`, `/data` a volume, port 8080, `HEALTHCHECK` with `clowder healthcheck` (which asks `GET /healthz`; the image has no shell and no curl). `GET /healthz` needs no login and answers `200 ok` when the database answers a ping within 2 s, `503` otherwise, and says nothing else. `compose.yaml`: `postgres:18` (a named volume, its own healthcheck) and `clowder` (built from the `Dockerfile`, waiting for a healthy database, its data directory a named volume), configured by a `.env` file of which `.env.example` is committed. `compose.cluster.yaml`, used together with it: `redis:8`, a second Clowder process with the same data volume, and Caddy in front balancing between them. `docker compose run --rm clowder invite -admin` makes the first admin | The owner's answer, 2026-10-27 ("Dockerfile + compose"). The details decided by the manager, 2026-10-27 |
| D41 | **Several processes** on one database need PostgreSQL and **Redis**, through **`github.com/redis/go-redis/v9`** (pure Go; Redis 8 is tested, Valkey works too). `-redis-url` / `CLOWDER_REDIS_URL` turns it on; with SQLite it is refused at start. Each process keeps its own hub (D18 stays true per process): a service's event goes to the local hub at once and to one Redis channel, `clowder:events`, as JSON with the id of the process; every process delivers the events of the others from that channel to its hub and skips its own. When the connection to Redis breaks, local delivery goes on, the bridge reconnects, and every subscription of the process is closed as if it were slow (§8), so pages and clients reload what they missed. The login limiter (§7.1) counts in Redis when it is set, so a limit holds across processes; the password-hashing slots (D37) stay per process. Every process gets the same data directory (attachments) and the same configuration | The owner's answer, 2026-10-27 ("Several processes", with Redis rather than PostgreSQL for the live events). The details decided by the manager, 2026-10-27 |
| D42 | **REST API.** `internal/api` holds the API: its handlers, its JSON types, the WebSocket and the OpenAPI document. Like `internal/web` it calls only `internal/app` and `internal/live` and never imports `internal/store`; `internal/web`'s server mounts it on the same mux and port under `/api/v1/`. JSON in and out (`Content-Type: application/json`, UTF-8), field names in `snake_case`, times in RFC 3339 UTC, ids as JSON numbers. Errors are `{"error": {"code": "...", "message": "...", "fields": {...}}}` with the statuses of §9.1 (`not_found`, `forbidden`, `invalid_input` with the field errors, `conflict`, `rate_limited`, `too_large`; `unauthenticated` is 401 with `WWW-Authenticate: Bearer`; anything else is 500 `internal` with no detail). A post, comment or message has its `body` as written and its `html` as the pages render it (D9), so a client needs no markdown renderer. Lists are paged the way the pages page them. Every rule of §7 holds as it does in the pages, because the API calls the same service methods: a private room is 404 to a non-member, and so on. The API covers everything a user and an admin do in the pages, except the pages' own helpers (the tag suggestions are an endpoint; the HTML fragments are not). Found in step 16.1: a body that is not `application/json` is 415 `unsupported_media_type`, and a wrong method on a known path is a JSON 405 `method_not_allowed` with `Allow`; the keys of `fields` are the API's snake_case field names (`display_name`), a problem with the body as a whole is under `body`, an unknown field under its own name; a login refused by the limiter has `Retry-After: 900` (the whole window, because the limiter does not know the remaining time) | The owner's request and answer ("Everything"), 2026-10-27. The details decided by the manager, 2026-10-27; the owner may revise |
| D43 | **API login.** `POST /api/v1/auth/login` (username, password, `client_name`) returns `{token, expires_at, user}`; `POST /api/v1/auth/signup` (invitation token, username, display name, password, `client_name`) does what the sign-up page does and returns the same. The token is a session of §7.1: stored as its SHA-256, the same 30-day lifetime from its last use, the same login limiter, deleted when the user is disabled; the `Session` row gets `Kind` (`browser`, `api`) and `ClientName` (up to 80 characters; the pages store the browser's user agent, cut to 80). A request sends it as `Authorization: Bearer <token>`. `/api/` routes never read the session cookie and the pages never read a bearer token, so no page's cookie can be used across sites through the API and the API needs no cross-origin check; there are no CORS headers. `POST /api/v1/auth/logout` deletes the token's session. `GET /api/v1/me/sessions` and `DELETE /api/v1/me/sessions/{id}` list and end the user's sessions, browsers and clients alike, and the new page `/me/sessions` does the same in the browser (ending one with a form `POST /me/sessions/{id}/end`; ending the current one logs out) | The owner's answer, 2026-10-27 ("Login → bearer token"). The details decided by the manager, 2026-10-27 |
| D44 | **WebSocket** through **`github.com/coder/websocket`** (pure Go, no dependencies). `GET /api/v1/ws` with the bearer header; the library's origin check stays on (a browser on another site is refused; a desktop client sends no `Origin`). Text messages of JSON, described in §7.13: the client subscribes to topics (the topics of §8, each checked like the stream of its page, 404 when the user may not read it), the user's own topic is always subscribed, and each event is sent with the resource it concerns as the REST API returns it to that user. A connection has the bounded queue of §8 and is closed with a `resync` message when it overflows. The server pings every 30 s and closes a connection that does not answer within 15 s. A changed session (logout, disabled user, changed role, D37) closes it | The owner's answer, 2026-10-27 ("coder/websocket"). The details decided by the manager, 2026-10-27 |
| D45 | **OpenAPI.** `internal/api/openapi.json`, OpenAPI 3.1 in JSON (so the standard library reads it in the tests; no YAML dependency), embedded and served at `GET /api/v1/openapi.json` without login. A test checks that every route registered under `/api/v1/` has an operation in the document with the same method and path, and the other way round, that every `$ref` resolves, that every operation has a summary, its responses and its error responses, and that every response a handler test receives validates against the schema of its operation (a small checker of the JSON Schema subset the document uses, written in the test). The messages of the WebSocket are schemas of the document's components. `docs/API.md` is the guide for the writers of desktop clients: login, errors, paging, uploads, the WebSocket and an example session | The owner's request, 2026-10-27. Decided by the manager, 2026-10-27 |
| D46 | 1.1 is released as `v1.1.0`. A 1.0 SQLite installation upgrades by running the new binary: the migrations add the new columns | Decided by the manager, 2026-10-27 |

## 5. Size estimate

| Part | Go lines (approx., without tests and generated code) |
|---|---|
| Storage: models, queries, migrations, search | 2,500 |
| Accounts, invitations, sessions | 1,200 |
| Rooms, posts, comments, reactions, attachments | 2,500 |
| Tasks and the board | 1,500 |
| Mentions and notifications | 800 |
| Audit log | 600 |
| Private messages | 1,500 |
| Live hub and SSE streams | 800 |
| HTTP handlers and middleware | 3,000 |
| templ views | 3,000 (templ) |
| CSS and the little JavaScript Datastar does not cover (drag and drop) | 1,500 |

## 6. Architecture

### 6.1 Layers

```
browser ── HTML pages, Datastar requests, SSE streams        desktop client ── REST (JSON), WebSocket
   │                                                            │
internal/web      handlers, middleware, the templ views     internal/api   JSON handlers, the WebSocket,
   │              (mounts internal/api under /api/v1/)        │            the OpenAPI document (§7.12)
   └──────────────────────────────┬───────────────────────────┘
internal/app      the services: accounts, rooms, posts, tasks, search, notifications; all rules of §7
   │         └──► internal/live   the hub: services publish events, streams and sockets subscribe (§8);
   │                              with Redis, internal/cluster carries the events between processes (§8.1)
internal/store    GORM models and queries; the only package that knows the database
   │
SQLite file or PostgreSQL + attachment files in the data directory
```

- A service method takes `ctx` and the acting user first, checks the permission, does the work in one transaction, and publishes its events after the commit.
- `internal/web` and `internal/api` never touch `internal/store`; `internal/app` never imports `net/http` or templ; `internal/api` imports neither templ nor `internal/web` (the server of `internal/web` mounts it).
- Every dependency is passed in a constructor; `cmd/clowder` builds them all in `main` (CLAUDE.md "Go conventions").

### 6.2 Package layout

| Package | Holds |
|---|---|
| `cmd/clowder` | the binary: the subcommands `serve`, `invite`, `demo` (§11), flag and environment parsing, wiring |
| `cmd/clowder-proof` | the proof tool (D22): starts a server on demo data, logs in with chromedp, saves a screenshot of a page; with `-cluster` it starts two servers on one PostgreSQL database and one Redis (D41) and takes the screenshot on the second after the `-other` user acted on the first; built only with the `clowder_browser` tag |
| `internal/browsertest` | the harness of the browser tests (D23): a server on a temporary data directory, Chromium sessions logged in as given users; `clowder_browser` tag only |
| `internal/config` | the configuration (§11) and its validation |
| `internal/store` | GORM models (§6.3), queries, migrations, the FTS5 index |
| `internal/app` | the services of §7, their permission checks and the audit events they write (§7.10) |
| `internal/auth` | argon2id hashing, session and invitation tokens |
| `internal/markdown` | the goldmark renderer (D9) and the extraction of mentions |
| `internal/files` | attachment storage on disk |
| `internal/live` | the event hub (§8) |
| `internal/web` | the HTTP server, routes, middleware, handlers, SSE streams |
| `internal/web/views` | templ components and pages |
| `internal/web/static` | `datastar.js`, CSS, icons, the drag-and-drop script; embedded with `embed` |
| `internal/api` | the REST API and the WebSocket for desktop clients (§7.12, §7.13), their JSON types, the embedded `openapi.json` (D42–D45) |
| `internal/cluster` | what several processes share through Redis (D41): the bridge that carries the hub's events between processes and the counters of the login limiter; the only package that imports go-redis |
| `cmd/clowder-apicheck` | a small API client that exercises the REST API and the WebSocket against a running server and prints what it did: the proof of the API steps and an example for client writers |
| `internal/clock` | the clock interface and its fake for tests |
| `docs/` | this spec, the plan, the log of finished steps, proofs |
| `scripts/` | helper scripts (`test-docker.sh`, D39) |
| `Dockerfile`, `compose.yaml`, `compose.cluster.yaml`, `.env.example`, `deploy/Caddyfile.cluster` | the container image, the compositions and the Caddy configuration of the cluster composition (D40) |

### 6.3 Data model

Every table has an integer primary key and `CreatedAt`; times are stored in UTC.

| Model | Fields (besides ID, CreatedAt) | Notes |
|---|---|---|
| `User` | `Username` (unique, lower case), `DisplayName`, `PasswordHash`, `Role` (admin, member), `DisabledAt` | |
| `Invitation` | `TokenHash` (unique), `Role`, `CreatedBy`, `ExpiresAt`, `UsedBy`, `UsedAt` | one use only |
| `Session` | `TokenHash` (unique), `UserID`, `ExpiresAt`, `LastSeenAt`, `Kind` (browser, api; 1.1), `ClientName` (1.1) | D43 |
| `Room` | `Name` (unique), `Description`, `Visibility` (visible, private), `CreatedBy` | |
| `RoomMember` | `RoomID`, `UserID`, `AddedBy` | unique (room, user) |
| `Post` | `RoomID`, `AuthorID`, `ParentID` (null for a post), `RootID` (the post a comment belongs to), `Format` (text, markdown), `Body`, `EditedAt`, `DeletedAt` | comments are posts with a parent (D16); `DeletedAt` marks a deleted post kept for its replies (D21), not GORM's soft delete |
| `Tag` | `Name` (unique, normalized: lower case, no leading `#`) | |
| `PostTag` | `PostID`, `TagID` | |
| `Attachment` | `PostID`, `UploaderID`, `FileName`, `ContentType`, `Size`, `StorageKey`, `Width`, `Height` (images) | |
| `Reaction` | `PostID`, `UserID`, `Emoji` | unique (post, user, emoji) |
| `BoardColumn` | `RoomID`, `Name`, `Position` | three made with every room |
| `Task` | `RoomID`, `PostID`, `ColumnID`, `Position`, `Title`, `DueDate` (date only, nullable) | |
| `TaskAssignee` | `TaskID`, `UserID` | |
| `Mention` | `PostID`, `UserID` | rebuilt when a post is edited |
| `Notification` | `UserID`, `Kind` (mention, reply, assigned, added_to_room), `PostID`, `TaskID`, `RoomID`, `ActorID`, `ReadAt` | |
| `Conversation` | `Kind` (direct, group), `Name` (groups, optional), `CreatedBy`, `DirectKey` (direct only: the two user IDs in order, unique) | |
| `ConversationMember` | `ConversationID`, `UserID`, `AddedBy`, `LastReadMessageID`, `LeftAt` | unique (conversation, user) |
| `Message` | `ConversationID`, `AuthorID`, `Body`, `ReplyToID`, `EditedAt`, `DeletedAt` | |
| `MessageAttachment` | as `Attachment`, with `MessageID` instead of `PostID` | |
| `MessageReaction` | `MessageID`, `UserID`, `Emoji` | unique (message, user, emoji) |
| `AuditEvent` | `ActorID`, `Action` (§7.10), `RoomID`, `EntityType` (room, post, comment, task, column, member), `EntityID`, `Summary`, `Details` (JSON) | no foreign keys to the entity: the event outlives it. Indexed by time, room, actor and action |

## 7. Behavior

### 7.1 Accounts and invitations

- `clowder invite -admin` prints a one-time link that makes an admin. Admins make invitation links (member or admin) in the admin page. A link expires after 7 days (configurable) and is used once.
- Opening a valid link shows a form for username (3–32 characters of `a-z`, `0-9`, `_`, `-`), display name and password (at least 12 characters). Submitting it creates the user, uses up the invitation and logs the user in.
- Login is username and password. A failed login does not say whether the username exists. After 5 failed attempts for one username or one IP address in 15 minutes, further attempts wait (a limiter in memory, or in Redis when several processes share a database, §8.1).
- A session lasts 30 days from its last use (configurable). Logout deletes it. Disabling a user deletes their sessions.
- Admins see the users, disable and enable them, and change their role. The last admin cannot lose the role.

### 7.2 Rooms

- Room names are unique (1–64 characters). A new room gets the columns To do, In progress, Done.
- Visible rooms: every user sees them in the room list, can read them, and joins with one click. Only members post, comment, react and change the board.
- Private rooms: only members see them, anywhere (list, search, notifications, links: a non-member gets 404). The creator is the first member. Any member adds any user; the added user is notified. A private room with no members left is kept for admins to delete.
- Any member leaves a room. Admins remove a member from any room. Admins rename, change the description of, and delete any room; the creator of a room can too.

### 7.3 Posts and comments

- The editor has: the body with a **Text / Markdown** switch, attachments, tasks (§7.6) and tags (§7.7), the last two in their own fields below the body.
- Text is shown as written (escaped, line breaks kept, links made clickable). Markdown is rendered by goldmark in safe mode with GitHub-flavored extensions (tables, strikethrough, autolinks, task-list items shown as read-only checkboxes) (D9).
- A post body is at most 20,000 characters; a post or comment needs a body, an attachment or a task.
- The author edits the body, format, attachments, tasks and tags; the post then shows "edited" with the time. Editing rebuilds its mentions, notifying newly mentioned users only.
- Comments are written under a post or a comment, to any depth; the page indents them to a maximum visual depth (6) and keeps the rest at that depth with a "replying to" line.
- The feed shows the newest posts first, 30 at a time, with more loaded on scroll. Each post shows its comment count, reactions and tags; its page shows the whole thread.
- The author or an admin deletes a post or comment (D21). A deleted one with replies is shown as "deleted" with no author, body, tags or reactions; its tasks leave the board.

### 7.4 Reactions

- Any member reacts to a post or comment with an emoji, chosen from a picker of common emoji or typed. Each (user, post, emoji) is there once; clicking it again removes it. Each emoji shows its count and, on hover, who reacted.
- A reaction is exactly one emoji. A typed one is trimmed, at most 32 bytes, and is: one emoji code point (the emoji blocks U+1F000–1FAFF, U+2600–27BF, U+2300–23FF, U+2B00–2BFF, U+2190–21FF, U+2900–297F, and ©, ®, ™, U+3030, U+303D, U+3297, U+3299), optionally followed by a variation selector and then one skin tone modifier, and optionally joined by the zero width joiner to further such units (at most 4 in all); or exactly two regional indicators (a flag); or a keycap (`0-9`, `#` or `*`, an optional U+FE0F, U+20E3); or a tag sequence (the black flag, tag characters, the cancel tag). Anything else, such as several emoji in a row, a lone regional indicator, letters, digits, spaces or punctuation, is refused with `TagInvalidInput` (decided by the manager, 2026-10-27; the owner may revise).
- Reactions are not audited (§7.10). Changes are published as `reaction.changed` to the room and the thread; each open page re-reads the post for its user and replaces only the chips (not the picker, so an open picker and what is typed in it stay), so every viewer sees their own chips highlighted. People who have not joined a visible room see the chips but cannot react; a deleted post shows no reactions.

### 7.5 Attachments

- Uploaded with the editor, up to 10 files of at most 25 MB each (configurable). Stored under `<data>/files/` by a random key; the original name is kept in the database.
- Downloaded through Clowder, which checks that the user can read the room. Images (PNG, JPEG, GIF, WebP) are shown inline, scaled down; other files are links with name and size. Files are served with `Content-Disposition` and `X-Content-Type-Options: nosniff`; only the image types above are served inline.

### 7.6 Tasks and the board

- In the editor, a task is a row: title (1–200 characters), assignees (members of the room), due date. A post or comment has any number of tasks.
- A new task goes to the end of the board's first column. Editing the post edits its tasks; a task removed in the editor is deleted.
- The board shows one column per `BoardColumn`, with the cards in order. A card shows title, assignees, due date (overdue in red) and a link to its post. Any member drags cards between and within columns, assigns and unassigns, and changes the due date on the card.
- Members add, rename, reorder and remove columns. A column with cards cannot be removed until its cards are moved. A room always has at least one column.
- The board's last column counts as done: due dates there are not shown as overdue.

### 7.10 Audit log

- These actions write an `AuditEvent` in the same transaction as the change (D24); a change that fails writes nothing:

  | Entity | Actions |
  |---|---|
  | post, comment | `post.created`, `post.edited`, `post.deleted` (with `EntityType` post or comment) |
  | task | `task.created`, `task.edited` (title, assignees, due date), `task.moved` (column or position), `task.deleted` |
  | column | `column.created`, `column.renamed`, `column.moved`, `column.deleted` |
  | member | `member.joined`, `member.added` (by another member), `member.left`, `member.removed` |
  | room | `room.created`, `room.renamed` (name or description), `room.deleted` (D27) |

- An event records the actor, the time, the room, the entity and a `Summary` readable on its own after the entity is gone (the room's and the users' names at that time, the task's title, the column's names from and to). `Details` holds the changed fields with their old and new values, except the bodies of posts and comments: an edit records that the body changed and its length, not the text (§2, "Edit history").
- A task deleted together with its post writes its own `task.deleted`; a post deleted with its room writes nothing more than the room's deletion (D27).
- Admins see the log in the admin area: newest first, 50 per page, filtered by room, actor, entity type, action and date range, with a link to the entity while it exists. The log covers private rooms too. Members do not see it.
- Nothing in Clowder edits or deletes audit events, and no route does.

### 7.7 Tags

- Tags are typed in their own field under the body, separated by spaces or commas, with suggestions from existing tags. Each is 1–50 characters of letters, digits, `-` and `_`, stored normalized. A post or comment has at most 10.
- A tag on a post is a link to the search for that tag.

### 7.8 Search

- One search box. Words search the bodies of posts and comments, attachment names and task titles (FTS5, prefix matching). Filters: `#tag` (repeatable, all must match), room, author, date range, and "posts only".
- Results come only from rooms the user can read: visible rooms and the private rooms the user is a member of. Each result shows its room, author, time and the matching text, and links to the post or comment.
- The FTS5 index is updated in the same transaction as the post.

### 7.9 Mentions and notifications

- `@username` in a body (outside code) of a user who can read the room is a mention and is rendered as a link to that user.
- Notifications: mention, reply (a comment under your post or comment), assigned (a task assigned to you), added to a room by someone else. Nobody is notified of their own action.
- A bell shows the unread count, updated live; its page lists the notifications, newest first, and marks them read.

### 7.11 Private messages

- **Starting.** From a user's name anywhere, or from the Messages page, a user starts a conversation with one other user (opening the existing one if there is one) or with several (a group, up to 50 members, with an optional name). Disabled users cannot be added.
- **Members.** Any member of a group adds other users and renames the group; any member leaves it. A direct conversation has exactly its two users and nobody can be added; to include a third person, a new group is started.
- **Messages.** A message is plain text (escaped, line breaks kept, links clickable, emoji as typed), up to 4,000 characters, with optional attachments (the limits and the download checks of §7.5, readers being the conversation's members). A message can reply to an earlier message of the same conversation, shown as a quote above it. The author edits a message (it shows "edited") and deletes it (it shows "message deleted" to everyone; its attachments are removed).
- **Reactions.** Members react with emoji as in §7.4.
- **Reading.** The Messages page lists the user's conversations, the most recent first, with the last message and an unread mark. A conversation shows its messages oldest first, newest at the bottom, 50 at a time with older ones loaded on scrolling up. Opening a conversation marks it read; a direct conversation shows "Seen" under the last message the other user has read.
- **Unread count.** The sidebar shows the number of conversations with unread messages, updated live. Messages are not notifications (§7.9) and do not ring the bell.
- **Privacy.** Only the members read a conversation; a non-member gets 404, admins included. A member who left no longer reads it. Messages are not searched by §7.8 and not written to the audit log (see §16).

### 7.12 REST API (1.1)

The API is for desktop clients that act as one logged-in user (D42, D43). It is versioned in the path: `/api/v1/`. Everything below is a call of a service method of `internal/app`, so the rules, limits, notifications, audit events and live events of §7.1–§7.11 apply exactly as they do to the pages.

- **Auth.** `POST /auth/login`, `POST /auth/signup`, `GET /auth/invitations/{token}` (whether an invitation link is valid, and its role), `POST /auth/logout`. Every other route needs `Authorization: Bearer <token>`; a missing, unknown or expired token is 401 `unauthenticated`. The login limiter of §7.1 applies (429 `rate_limited` with `Retry-After`).
- **Me.** `GET /me` (the user, unread notification and conversation counts), `GET /me/sessions`, `DELETE /me/sessions/{id}`.
- **Users.** `GET /users?q=` (enabled users by username or display name prefix, 20 at most: what a client needs to pick members and mention people).
- **Rooms.** `GET /rooms` (the visible rooms and the user's private ones, with whether the user is a member), `POST /rooms`, `GET /rooms/{id}`, `PATCH /rooms/{id}` (name, description), `DELETE /rooms/{id}`, `POST /rooms/{id}/join`, `POST /rooms/{id}/leave`, `GET /rooms/{id}/members`, `POST /rooms/{id}/members` (add), `DELETE /rooms/{id}/members/{user_id}`, `GET /rooms/{id}/candidates?q=` (users who can be added).
- **Posts and comments.** `GET /rooms/{id}/posts?before=<post id>&limit=` (the feed, newest first, 30 by default and at most 100), `POST /rooms/{id}/posts`, `GET /posts/{id}` (a post or a comment), `GET /posts/{id}/thread` (the post and all its comments as a tree), `POST /posts/{id}/comments` (a reply to a post or a comment), `PATCH /posts/{id}`, `DELETE /posts/{id}`, `PUT /posts/{id}/reactions/{emoji}` and `DELETE /posts/{id}/reactions/{emoji}` (the emoji URL-escaped; adding one that is there, or removing one that is not, is not an error), `GET /tags?prefix=` (suggestions). A post input is `{format, body, tags[], tasks[{id?, title, assignee_ids[], due_date?}], upload_ids[], attachment_ids[]}`: `upload_ids` are new files, `attachment_ids` the existing ones an edit keeps.
- **Uploads and attachments.** `POST /uploads` (multipart, one file in the field `file`; the limits of §7.5) returns an upload id that one later create or edit takes, as the editor does; `GET /attachments/{id}` downloads with the checks and headers of §7.5; the same for messages under `/messages/attachments/{id}`.
- **Board.** `GET /rooms/{id}/board` (the columns with their cards), `POST /rooms/{id}/columns`, `PATCH /columns/{id}` (name), `POST /columns/{id}/move` (position), `DELETE /columns/{id}`, `PATCH /tasks/{id}` (assignees, due date), `POST /tasks/{id}/move` (column, position).
- **Search.** `GET /search?q=&page=` with the query language of §7.8 (`#tag`, `room:`, `from:`, dates, posts only), 20 results a page.
- **Notifications.** `GET /notifications?before=&limit=`, `POST /notifications/read` (up to an id, as the page does).
- **Messages.** `GET /conversations`, `POST /conversations` (`{user_ids[]}` or `{usernames[]}`, and `name` for a group: one other user opens the direct conversation), `GET /conversations/{id}`, `PATCH /conversations/{id}` (name), `POST /conversations/{id}/members`, `POST /conversations/{id}/leave`, `GET /conversations/{id}/messages?before=&limit=` (oldest first in the page, 50 by default), `POST /conversations/{id}/messages` (`{body, reply_to_id?, upload_ids[]}`), `PATCH /messages/{id}`, `DELETE /messages/{id}`, `PUT`/`DELETE /messages/{id}/reactions/{emoji}`, `POST /conversations/{id}/read` (up to a message id).
- **Admin** (admins only, 403 `forbidden` for others): `GET /admin/users`, `PATCH /admin/users/{id}` (role, disabled), `POST /admin/invitations` (role; returns the link and when it expires), `GET /admin/rooms`, `GET /admin/rooms/{id}/members`, `GET /admin/audit?page=&room_id=&actor_id=&entity_type=&action=&from=&to=`.
- **Other.** `GET /openapi.json` (D45) needs no login. `GET /healthz` (D40) is outside `/api/`.
- A request body is at most 64 KiB, except uploads (§7.5). A body that is not JSON, or has unknown fields, is 422 `invalid_input`.
- The exact paths, parameters and schemas are those of `internal/api/openapi.json`, which is kept equal to the routes by a test (D45). A change of a route changes the document in the same commit.

### 7.13 WebSocket (1.1)

`GET /api/v1/ws`, with the bearer token as for every API route (D44). All messages are JSON text messages with a `type`.

- **Client to server.** `{"type": "subscribe", "id": 1, "topics": ["room:3", "post:17"]}`, `{"type": "unsubscribe", "id": 2, "topics": [...]}`, `{"type": "ping", "id": 3}`. `id` is the client's own number, echoed in the answer. The topics are those of §8: `room:<id>`, `post:<id>` (a thread), `board:<id>` (a room's board), `conversation:<id>`. `user:<id>` of the user is subscribed from the start and cannot be removed; another user's is refused. At most 200 topics per connection.
- **Server to client.** `{"type": "ack", "id": 1, "topics": [...]}` when all topics were subscribed; `{"type": "error", "id": 1, "error": {...}}` (the error object of §7.12) when one was not (nothing of that request is subscribed: a room the user may not read is `not_found`); `{"type": "pong", "id": 3}`; `{"type": "event", "topic": "room:3", "kind": "post.created", "room_id": 3, "post_id": 42, ..., "data": {...}}` for each live event the user may see; `{"type": "resync", "reason": "overflow" | "reconnect"}` just before the server closes the connection when its queue overflowed or the Redis bridge reconnected (§8.1): the client reconnects and reloads what it shows.
- **Events.** The kinds are those of `internal/live` (§8): `post.created`, `post.updated`, `post.deleted`, `comment.*`, `reaction.changed`, `board.changed`, `room.updated`, `room.deleted`, `member.joined`, `member.left`, `notification.created`, `notification.read`, `message.*`, `conversation.updated`, `unread.changed` and the others the hub has. `data` is the resource as the REST API returns it to this user at the time of sending (the post, the comment, the board, the message, the counts), absent for a deletion. An event the user may no longer see (left the room, removed from a conversation) is not sent, and a subscription to a room the user can no longer read is dropped with an `error` message of `not_found` for that topic.
- **Lifecycle.** The server pings every 30 s and closes a connection that does not answer in 15 s. Logging out that session, disabling the user or changing their role closes the connection (code 4001 "session ended" or 4002 "session changed"; a client reconnects after 4002). A message that is not valid JSON or is over 16 KiB closes it with 1003 or 1009. The server's shutdown (§8) closes every connection with 1001.

## 8. Live updates and lifecycle

- `internal/live` has one hub per process. Services publish an event after their transaction commits: a topic (`room:<id>`, `post:<id>`, `board:<id>`, `conversation:<id>`, `user:<id>`) and what changed.
- Each page opens one SSE stream (Datastar `@get` on load) for the topics it shows. The handler checks the permission when it subscribes, and again when it renders each event for its user, then sends Datastar patches: a new post at the top of the feed, a comment in its thread, new reaction counts, a moved card, an unread count.
- A user removed from a private room or disabled loses its streams on that room.
- A slow stream does not block the hub: each subscriber has a bounded queue, and a subscriber whose queue is full is closed; the page reconnects and reloads its part.
- The server stops on SIGINT or SIGTERM: it stops accepting requests, closes the streams, waits for running requests (10 s at most), and closes the database. Every goroutine is owned by a `context.Context` (CLAUDE.md "Go conventions").
- Clowder runs as one process per database (D18), unless it runs on PostgreSQL with Redis (§8.1).
- The WebSocket of the API (§7.13) subscribes to the hub like a page's stream and follows the same rules.

### 8.1 Several processes (1.1)

- With PostgreSQL and `-redis-url` (D41), several Clowder processes serve one database. They share the data directory (one volume) for the attachments, and they are configured the same way. A load balancer in front of them needs no sticky sessions: every request carries its session, and every process delivers every event.
- `internal/cluster` subscribes to the Redis channel `clowder:events` when the process starts and publishes to it after each service's event, which the local hub also gets at once. An event that comes from another process is published to the local hub as it is. The goroutine that reads the channel is owned by the server's context and stopped before the hub is closed.
- When the connection to Redis breaks, the processes keep serving; their pages may miss the events of the others until it comes back. When it is back, every subscription of the process is closed as if it were slow, so every page and client reloads (§7.13 `resync`).
- The login limiter counts failures in Redis with keys that expire after the window of §7.1, so 5 failures in all the processes block the username or address.
- The cleanups that each process runs (expired sessions, unused uploads) are safe to run in several processes at once.
- **When Redis fails during a login, the limiter fails open** (decided by the manager, 2026-10-27; the owner may revise): the attempt goes on, the failure is counted nowhere, and a warning with the tagged error is logged; the password is still checked. A Redis outage must not lock everybody out, and the limit is a defense against guessing, not the only one (passwords are hashed with Argon2id, D37). Each call to Redis for the limiter is bounded by 2 seconds. The keys are `clowder:login:user:<hash>` and `clowder:login:ip:<hash>`, sorted sets of the failure times (from the limiter's clock) of which only the latest five are kept, updated atomically (MULTI/EXEC); the hash (SHA-256) keeps a username or an address from injecting a key pattern.
- **Migrations.** On PostgreSQL a process takes the advisory lock `pg_advisory_lock` (the key is the bytes of "clowder") on a connection of its own while it migrates, so that processes that start together on an empty database migrate one after the other and the later ones find the schema current.
- **The state of a process, one by one** (the review of step 15.2):
  - sessions, invitations, notifications, unread counts, audit events: in the database, shared;
  - the live hub and its subscriptions: per process by design (D18, D41); the events cross through Redis;
  - the login limiter: in Redis when it is set (above), else in memory;
  - the password-hashing slots (D37): per process by design, because they bound the memory of that process;
  - the cache of the embedded static files: immutable and the same in every process (same binary);
  - the cleanups of expired sessions, of unused uploads and of the files of a deleted post: idempotent statements and removals that ignore a missing file, safe in any number of processes (the one run by a sign-in deletes expired rows of that user only);
  - the staging directory of uploads (`files/tmp`): a process that starts removes only the leftovers that were not written to for an hour, not the upload another process is writing now (this was wrong with two processes; fixed in 15.2); an upload is written and renamed to its key within one request, so what one process stored the other reads at once from the shared directory;
  - "the last admin cannot lose the role" and "cannot be disabled": the check and the change run under a PostgreSQL advisory lock (`Store.LockAdmins`), because two admins who changed each other at the same moment could both see the other as the remaining admin (this was wrong on PostgreSQL even with one process; SQLite serializes write transactions); fixed in 15.2;
  - the configuration, including the base URL, is read at start and must be the same in every process.

## 9. Errors and diagnostics

### 9.1 Public error tags

The services return tagged errors that the handlers turn into HTTP answers. The tags that decide the answer are exported from `internal/app` (`TagNotFound` → 404, `TagForbidden` → 403, `TagInvalidInput` → 422 with the field errors in the metadata, `TagConflict` → 409, `TagRateLimited` → 429 (a login refused by the limiter of §7.1), `TagTooLarge` → 413 (a file over the attachment limit, §7.5), `TagUnauthenticated` → redirect to the login). A user who may not see a private room gets `TagNotFound`, not `TagForbidden`. Any other error is a 500, logged with its route; the page shows a short message, never the error. These statuses are what a full page or a plain form post gets. A Datastar request is answered with 200 and an SSE stream that patches the error into the page, because `datastar.js` drops the body of any other status. The API (§7.12) answers with the same statuses and a JSON error object, except that `TagUnauthenticated` is 401 there (D42).

### 9.2 Logging

Every package logs through a `*slog.Logger` it is given. An error is logged as the attribute `"err"`. Each request is logged once (method, path pattern, status, duration, user ID). Passwords, tokens and post bodies are never logged.

### 9.3 How errors are made (`rm4n0s/errors` v0.2.0)

Every error of the module is made, returned, logged and tested the way `AI_GUIDE.md` of `github.com/rm4n0s/errors` says (D2). The guide is in the module cache (`$(go env GOMODCACHE)/github.com/rm4n0s/errors@v0.2.0/AI_GUIDE.md`) and is read in full before error code is written. The rules, with the guide's numbers:

| Rule | What the code does | Why |
|---|---|---|
| R1 | `errors.New(tag, message, metadata...)` always has a tag and a message | the tag names the one thing a function cannot do; tests and callers match on it |
| R2 | the metadata is one list of `"key", value` pairs with string keys: given to `errors.New`, or in exactly one `SetMetadata` after `NewErr` and `NewErrf`; a later `SetMetadata` only adds data that becomes known in another function | a key that is not a string is stored as `arg<index>` and a missing value as `nil`; both are bugs |
| R3 | an error is tagged once, where it first appears. An error of this module goes up as it is (`return err`). An error from outside it (standard library, GORM, the SQLite driver, templ, goldmark) is tagged where it first appears with `NewErr`. A failure that the code finds itself is `errors.New` | a second tag or a `fmt.Errorf` hides the first tag and cuts the route; `errors.FromError` is a plain type assertion |
| R4 | the constructor is called in the function that fails, not in a helper that builds the error | the route is the call stack at the constructor: a helper adds a frame that is not the failing function |
| R5 | a tag is a constant next to the code that produces it; code compares `Tag` with that constant or uses `errors.Is` with a sentinel (`&errors.Error{Tag: TagX}`), never a string literal or the message; no package-level `errors.New` | messages are for people and change; a package-level `New` has the route of `init` |
| R6 | only `github.com/rm4n0s/errors` is imported (it re-exports `Is`, `As` and `Unwrap`); the standard library's package, when `Join` is needed, is imported as `stdErrors` | one import, one behavior |
| Logging | an error is logged with `slog` as `"err", err`; `*errors.Error` is a `slog.LogValuer` (keys `Tag`, `Message`, `Route`, `OriginalErr`, `Metadata`, `StackFrames`), and the entry unmarshals into `errors.ErrorJson` and replays with `errors.FromJson`. `ToJson` and `json.Marshal` are not used to log | the route in the entry is cut at the function that logs |
| Tests | each tag has a test with `HasRoute("Outer->Inner.Tag")` (from the entry function that the test calls, with the tag), and the metadata where it matters | the test shows which path made the error |

`gorm.ErrRecordNotFound` is turned into `TagNotFound` in `internal/store`, where it first appears.

## 10. Performance targets

Measured on the development machine with a database of 100 rooms, 200 users, 100,000 posts and comments and 20,000 tasks (made by `clowder demo`).

| What | Target |
|---|---|
| A room's feed page (30 posts), server time | under 50 ms |
| A search with words and two tags | under 100 ms |
| A new post reaching 200 open streams of its room | under 200 ms |
| Memory of an idle server with 200 open streams | under 150 MB |

From 1.1 the same targets are measured on PostgreSQL too (step 17.1), and the fan-out also across two processes with Redis; a miss is recorded in `docs/PERFORMANCE.md`.

## 11. Build and runtime requirements

- Go 1.27 or later, the standard toolchain; `CGO_ENABLED=0 go build ./cmd/clowder` makes the whole program. Linux on amd64 and arm64 is what is tested; the code has no OS-specific parts.
- The binary embeds the templates, CSS and `datastar.js`. At run time it needs only its data directory.
- Subcommands:
  - `clowder serve`: runs the server.
  - `clowder invite -admin`: prints a one-time link for a new admin (used for the first one).
  - `clowder demo`: fills an empty database with demo data, for development, proofs and §10.
  - `clowder healthcheck`: asks `GET /healthz` of the server at `-addr` and exits 0 when it answers 200, 1 otherwise (D40, for the image's `HEALTHCHECK`).
- Configuration, as flags or `CLOWDER_*` environment variables: the listen address (`:8080`), the data directory (`./data`, holding `clowder.db` and `files/`), the base URL (for invitation links), secure cookies (on unless the base URL is `http://`), the attachment limits, the session lifetime, the invitation lifetime, the log level, and whether to trust the proxy headers (`-trust-proxy`, `CLOWDER_TRUST_PROXY`, off by default: the client address, which the login limiter counts, is then the last address of `X-Forwarded-For` instead of the address of the connection). From 1.1 also: the database URL (`-database-url`, `CLOWDER_DATABASE_URL`, D38) and the Redis URL (`-redis-url`, `CLOWDER_REDIS_URL`, D41).
- The container image and the compositions are those of D40; the README says how to run them.
- Clowder speaks plain HTTP; TLS is done by a reverse proxy in front of it.

## 12. Testing strategy

- Unit tests are table-driven and live next to the code.
- `internal/store` and `internal/app` tests run on a fresh SQLite database per test (a copy of a database migrated once, D34 step 13.1), with the fake clock and a recording hub.
- `internal/web` tests run the real handlers with `httptest`: they log in, send the requests a page sends (including Datastar's), and check the status, the HTML (with `golang.org/x/net/html` only if the standard library is not enough: ask first) and the SSE patches.
- Permission tests: for every service method, a table of who may do it (admin, member, member of the room, non-member of a visible room, non-member of a private room, disabled user).
- Every error tag has a route test (§9.3).
- Every audited action has a test that checks the event it writes (action, actor, room, entity, summary, details) and that a failed change writes none (§7.10).
- The default tests need no network and no browser.
- From 1.1 every store, service and handler test also runs on PostgreSQL, and the Redis tests run, through `scripts/test-docker.sh` (D39). A test that depends on the database writes the same assertion for both; where the results may differ (the order of rows with equal keys, the snippet's exact cut), the test orders or compares what §7 promises, not what one engine happens to return.
- API tests (`internal/api`) run the real handlers with `httptest` and a bearer token, check the status, the JSON and its schema in the OpenAPI document (D45), and, for the WebSocket, connect with `coder/websocket`'s client to an `httptest` server. Every API route has a test of who may call it, as the service methods do.
- Browser tests (D23) carry the build tag `clowder_browser` and use `internal/browsertest`: drag and drop on the board, live updates seen in two sessions at once, the editor's tag and task fields, and, with `CLOWDER_TEST_POSTGRES` and `CLOWDER_TEST_REDIS`, two servers on one database where a post written on one appears live in a page served by the other (D41). Each test starts its own server and Chromium on temporary directories and stops them in `t.Cleanup`. Run with `go test -tags clowder_browser ./...`.

## 13. Milestones

Each milestone is broken into steps in `docs/IMPLEMENTATION.md` (written after this spec is approved) and follows the workflow in CLAUDE.md.

| # | Milestone | Done when |
|---|---|---|
| M0 | Skeleton: `go.mod` with the dependencies of D6–D10 and D23, package layout, `go tool templ` wired to `go generate`, embedded static files with `datastar.js`, `clowder serve` showing a page, the proof tool and the browser-test harness | the checks pass and the proof tool saves a screenshot of the page |
| M1 | Storage: the models of §6.3, migrations, the fake clock, store tests | every model is saved and read back in tests |
| M2 | Accounts: `clowder invite`, invitations, sign-up, login, logout, sessions, cross-origin protection, login limiter, the admin's user page | a person goes from an invitation link to a logged-in page |
| M3 | Rooms: create, list, join, leave, add members, private rooms, room page; the audit log (§7.10) with its admin page, starting with the member events | the permission table of §7.2 passes and an admin sees who joined and left |
| M4 | Posts: the editor (text and markdown), tags, the feed, the post page, editing, deleting, their audit events | a post with markdown and tags appears in its room, and its edit shows in the audit log |
| M5 | Comments: threads to any depth, editing, deleting, their audit events | a reply to a reply shows in its thread |
| M6 | Attachments: upload, storage, limits, download checks, inline images | an image shows in a post; a non-member can't download it |
| M7 | Live updates: the hub, the SSE streams of feeds and threads, reconnects | a post written in one session appears in another without reload |
| M8 | Reactions | emoji are added and removed and their counts update live |
| M9 | Tasks and the board: tasks in the editor, the board, columns, drag and drop, assignees, due dates, live board, the audit events of tasks and columns | a task written in a post is moved on the board in two sessions at once |
| M10 | Search: FTS5, tags and the other filters, permission-aware results | the search tests of §7.8 pass |
| M11 | Mentions and notifications | a mention and an assignment raise the bell's count live |
| M12 | Private messages: conversations, messages, replies, reactions, attachments, read state, the unread count, live | a message from alice appears in bob's open conversation and raises his unread count when it is closed |
| M13 | 1.0: `clowder demo`, the numbers of §10, a README with how to run it behind a proxy, `v1.0.0` | every check passes and the targets of §10 are met or recorded |
| M14 | PostgreSQL: the second dialect of the store, the whole test suite on PostgreSQL, the Docker image and `compose.yaml` (D38–D40) | `scripts/test-docker.sh` passes and `docker compose up` serves Clowder on PostgreSQL |
| M15 | Several processes: the Redis bridge of the live events, the login limiter in Redis, `compose.cluster.yaml` (D41, §8.1) | a post written on one process appears live in a page served by another |
| M16 | REST API and WebSocket: login with bearer tokens and the sessions page, every route of §7.12, the WebSocket of §7.13, `openapi.json` kept equal to the routes (D42–D45) | `clowder-apicheck` logs in, posts, comments, moves a card and sends a message through the API, and sees each one come back over the WebSocket |
| M17 | 1.1: the security review of the new surface, `docs/API.md`, the README, the numbers of §10 on PostgreSQL, `v1.1.0` | every check passes, the targets of §10 are met or recorded on both databases, `v1.1.0` is on `origin` |

## 14. Demo data

There is no `examples/` directory: Clowder is an application, not a library. `clowder demo` fills a database with users, rooms, posts, threads, reactions, attachments and tasks, and prints the login of a demo admin. Proofs and manual checks use it. `clowder demo -size large` adds the data set of §10 (200 users, 100 rooms, 100,000 posts and comments, 20,000 tasks, a handful of private conversations) to it and prints the counts; it takes seconds, not minutes (D35). The default, `-size small`, is the demo the proofs and tests rely on.

## 15. Licensing and third-party code

Clowder is BSD-3-Clause (`LICENSE`). `datastar.js` is embedded as released by its authors; its license (MIT) is kept next to it in `internal/web/static/` and listed in a `NOTICE` file with the other embedded third-party files. Go dependencies are not vendored.

## 16. Open decisions

New ones are added here with a recommendation and moved to §4 once decided.

| Decision | Recommendation |
|---|---|
| Can admins read private messages, and are messages in the audit log? | No to both (§7.11 as written): they are private between their members |
| Can private messages be searched? | Yes, but only inside the Messages page and only in the user's own conversations; not in 1.0 unless asked |

## 17. Risks

| Risk | Mitigation |
|---|---|
| SQLite allows one writer at a time; a busy room could queue writes | WAL mode, a busy timeout, short transactions. The targets of §10 check it; PostgreSQL can come later (D6) |
| Two databases behave differently (ordering, case, time precision, search) | One test suite runs on both (D39); the differences live in `internal/store` behind `dialect` (D38) |
| Several processes miss each other's events while Redis is down | Local delivery goes on; a reconnect makes every page and client reload (§8.1) |
| An API token leaks from a desktop client | Tokens are sessions: listed and ended from `/me/sessions` and the API, ended when the user is disabled, stored only as hashes (D43) |
| The OpenAPI document drifts from the routes | A test compares them both ways and validates every response the handler tests get (D45) |
| The pure-Go SQLite driver is slower than the cgo one | Measured against §10 in M12; the store's API keeps a driver change local |
| Datastar's client and Go SDK change between versions | Both pinned together (D7); an upgrade is its own step |
| Many open SSE streams hold memory and file descriptors | Bounded queues, one stream per page, measured in §10 |
| A markdown body or an uploaded file used for script injection | goldmark's safe mode (D9), `nosniff` and `Content-Disposition` on downloads, only listed image types served inline (§7.5), templ's escaping everywhere else |
