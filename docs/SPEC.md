# Clowder: Specification

| | |
|---|---|
| Status | 1.0 released as `v1.0.0` (2026-10-27); 1.1 (PostgreSQL, several processes, the API for desktop clients) released as `v1.1.0` (2026-10-27); 1.2 (settings, SSH sign-in codes, code highlighting, a new look, phones, operations) in progress |
| Date | 2026-10-28 |
| Owner | Manos Ragiadakos |
| Module | `github.com/rm4n0s/clowder` |
| License | BSD-3-Clause |

Clowder is a self-hosted web application where a team talks in rooms. A room is not a stream of chat lines: it is a feed of posts, and the conversation about a post happens in the comments under it, which can be answered in turn. A post (and a comment) can be plain text or markdown, carry attachments, and hold tasks; the tasks of a room are the cards of the room's Kanban board. Posts have tags, given in their own field, and everything a user can read can be searched. Besides the rooms, users write private messages to each other, one to one or in small groups. People join only by invitation. Clowder is one Go binary with an SQLite database and a directory of attachments. The pages are rendered on the server with templ and kept live with Datastar over server-sent events, so what one person posts, comments, reacts or moves on a board appears at once for everyone looking at it.

From 1.1 the database can also be PostgreSQL, run with Docker containers, and several Clowder processes can serve one PostgreSQL database, with Redis carrying the live events between them (§8.1). A REST API with a WebSocket for live events (§7.12, §7.13) lets desktop clients, written separately, do everything the pages do; it is described by an OpenAPI document.

1.2 gives each user a settings page (profile, password, theme, notification preferences like Mattermost's) and a second factor in the manner of badgermole: Clowder runs an SSH server that hands a one-time code to the owner of a registered public key (§7.14–§7.16). The two users of a direct conversation can video-call each other through a WebRTC server made with pion. Code in markdown is highlighted, rooms show what is unread, private messages can be searched, and the pages get a new look for cat lovers, made for phones too (§3b).

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
- Voice, video, screen sharing. *(1.2 adds calls in private messages, §7.18.)*
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

### Non-goals for 1.2

- Email of any kind still (a password reset is a link that an admin hands over, D51).
- TOTP and passkeys: the SSH sign-in code is the second factor (the owner's answer, 2026-10-28).
- Anything else over SSH: no shell, no commands besides the code, no port forwarding.
- Logging in with the SSH code alone, without the password (badgermole's way). It is a small change on top of D53 if the owner wants it later.
- Push notifications while no Clowder page is open (Web Push needs a push service). Notifications reach an open page, in any tab.
- Offline use of the installed app: it needs the server.
- Calls in group conversations and rooms (calls are one-to-one, the owner's answer, 2026-10-28), recording a call, dialling in by phone, end-to-end encryption of calls (the server forwards the media, D63), simulcast, and a TURN server.

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

## 3b. Scope of 1.2

The owner's request, 2026-10-28: "UI is ugly. no modern at all and doesn't represents the aesthetics of "clowder"", "the markdown doesn't support programming languages" and "the user doesn't have settings to update password, notifications (like disabling from a group or having sound by default and disabling it)". To the manager's list of further improvements: "all are good". On a second factor: "rather using TOTP I want you to create something similar to github.com/rm4n0s/badgermole". On the look: "design mockups, make it for cat lovers". Everything of 1.1 keeps working; the API only grows (`/api/v1`).

1. **Settings.** A settings page for each user: display name, avatar, password, theme, notification preferences, SSH keys and the sign-in code, sessions (§7.14, D49).
2. **Notification preferences, like Mattermost's.** Global defaults and per-room and per-group overrides (all, mentions, nothing; sound; mute; mark unread), `@room` and `@here` mentions with presence, keywords, and following posts. The sound is on by default and can be turned off, and browser notifications are available (§7.15, D50).
3. **Password reset.** An admin makes a one-time reset link for a user; `clowder reset-password` does it on the command line (§7.1, D51).
4. **SSH sign-in codes**, in the manner of badgermole: Clowder runs an SSH server; a user who registered a public key runs `ssh` and gets a one-time code, which a login asks for after the password (§7.16, D52, D53).
5. **Code in markdown** is highlighted by language (§7.3, D54).
6. **Unread state** per room, a "new since your last visit" line in the feed, and the count in the tab's title (§7.17, D55).
7. **Search in private messages**, inside the Messages page and only in the user's own conversations (§7.11, D56).
8. **A new look** for cat lovers, chosen by the owner from mockups, with light and dark themes (D57), and pages made for phones, installable as an app (D58).
9. **One-to-one video and audio calls in direct conversations**, through a WebRTC server made with pion (§7.18, D63–D65).
10. **Operations and limits.** `clowder backup` and `clowder restore` (D59), metrics for Prometheus (D60), `govulncheck` among the checks (D61), and the per-user limits recommended by the 1.1 review (D62).

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
| D42 | **REST API.** `internal/api` holds the API: its handlers, its JSON types, the WebSocket and the OpenAPI document. Like `internal/web` it calls only `internal/app` and `internal/live` and never imports `internal/store`; `internal/web`'s server mounts it on the same mux and port under `/api/v1/`. JSON in and out (`Content-Type: application/json`, UTF-8), field names in `snake_case`, times in RFC 3339 UTC, ids as JSON numbers. Errors are `{"error": {"code": "...", "message": "...", "fields": {...}}}` with the statuses of §9.1 (`not_found`, `forbidden`, `invalid_input` with the field errors, `conflict`, `rate_limited`, `too_large`; `unauthenticated` is 401 with `WWW-Authenticate: Bearer`; anything else is 500 `internal` with no detail). A post, comment or message has its `body` as written and its `html` as the pages render it (D9), so a client needs no markdown renderer. Lists are paged the way the pages page them. Every rule of §7 holds as it does in the pages, because the API calls the same service methods: a private room is 404 to a non-member, and so on. The API covers everything a user and an admin do in the pages, except the pages' own helpers (the tag suggestions are an endpoint; the HTML fragments are not). Found in step 16.1: a body that is not `application/json` is 415 `unsupported_media_type`, and a wrong method on a known path is a JSON 405 `method_not_allowed` with `Allow`; the keys of `fields` are the API's snake_case field names (`display_name`), a problem with the body as a whole is under `body`, an unknown field under its own name; a login refused by the limiter has `Retry-After: 900` (the whole window, because the limiter does not know the remaining time). Found in step 16.2: a create is 201 with the resource and a `Location`, an edit is 200 with the resource, a delete, join, leave and member add or remove are 204, and `PUT`/`DELETE /posts/{id}/reactions/{emoji}` are 200 with `{reactions}` (the post's reactions as the caller sees them; both are idempotent, unlike the pages' toggle). A thread is `{post, comments, focus_id}` where `comments` is the flat reading-order list of `{depth, comment}` (every comment before its replies, siblings oldest first, any depth; `parent_id` and `depth` rebuild the tree), and `focus_id` names the comment when the thread was asked for by a comment. An edit (`PATCH`) keeps what the body leaves out or sets to `null`, and replaces a list that is there (`tags`, `tasks`, `attachment_ids`); a new post or comment starts from empty, as text. `root_id` of a top-level post is its own ID. A feed page is 30 posts by default and at most 100 (422 above). A room is renamed, deleted and its members removed by admins in any room, private ones included, as in the pages (D29); every read of a private room is 404 for a non-member, admins included. The field names of the services (`user`, `attachments`) become `user_id` and `upload_ids` (`attachment_ids` on an edit); the tasks of a post are one field `tasks` whose message names the row. Found in step 16.3: `POST /uploads` takes one file in the field `file` of a `multipart/form-data` body (anything else in the form is 422 on `file`, another Content-Type 415) and answers 201 `{id, file_name, content_type, size, width, height}`; the `id` is the 64-character key of the file. The server keeps a small note beside the file (`<data>/files/notes/<key>`, shared by the processes of a cluster) that says who uploaded it and what the upload was, so `upload_ids` of a create or an edit give a file back only to the person who uploaded it (anything else is 422 on `upload_ids`), and an id is used once: the post that takes it, or refuses it, ends the note, and a refused post removes the file as in the pages, so the client uploads it again. A file that no post takes is removed with its note after 24 hours (the pages never leave one, because their upload and their post are one request): the server sweeps at start and every hour, by the time written in the note. The upload route is exempt from the 64 KiB rule; its body is capped at the attachment limit plus a megabyte, and the deadline for the body of an upload (15 minutes) applies to it as to the pages' upload because it goes by the Content-Type. `GET /attachments/{id}` sends the file with the headers of the pages' download, which `internal/files` sets for both (`ServeDownload`). The board: `GET /rooms/{id}/board` is `{room_id, columns[{id, name, position, done, tasks[{...task, post_id, position}]}]}`; a column is added with 201 and a `Location` that is the board, renamed with 200 (the column), and `POST /columns/{id}/move` and `POST /tasks/{id}/move` are 204 (the client reads the board again; a position outside the list is clamped); `PATCH /tasks/{id}` takes `assignee_ids` and `due_date` (absent keeps, `null` clears the due date, `[]` unassigns; the title is edited with the post) and answers the card; a column with cards, and the last column, are 409. Search is `{results[{post_id, root_id, room, author, created_at, kind, snippet[{text, match}]}], page, has_more}`, the snippet as plain-text segments so that a client needs no HTML. Notifications are `{notifications[{id, kind, actor, room, post_id, task_id, created_at, read}], next_before}` paged by id like the feed, 30 by default and 100 at most (the service's maximum was 50 and is 100 now; the page still shows 30); `POST /notifications/read` is 204 | The owner's request and answer ("Everything"), 2026-10-27. The details decided by the manager, 2026-10-27; the owner may revise |
| D43 | **API login.** `POST /api/v1/auth/login` (username, password, `client_name`) returns `{token, expires_at, user}`; `POST /api/v1/auth/signup` (invitation token, username, display name, password, `client_name`) does what the sign-up page does and returns the same. The token is a session of §7.1: stored as its SHA-256, the same 30-day lifetime from its last use, the same login limiter, deleted when the user is disabled; the `Session` row gets `Kind` (`browser`, `api`) and `ClientName` (up to 80 characters; the pages store the browser's user agent, cut to 80). A request sends it as `Authorization: Bearer <token>`. `/api/` routes never read the session cookie and the pages never read a bearer token, so no page's cookie can be used across sites through the API and the API needs no cross-origin check; there are no CORS headers. `POST /api/v1/auth/logout` deletes the token's session. `GET /api/v1/me/sessions` and `DELETE /api/v1/me/sessions/{id}` list and end the user's sessions, browsers and clients alike, and the new page `/me/sessions` does the same in the browser (ending one with a form `POST /me/sessions/{id}/end`; ending the current one logs out) | The owner's answer, 2026-10-27 ("Login → bearer token"). The details decided by the manager, 2026-10-27 |
| D44 | **WebSocket** through **`github.com/coder/websocket`** (pure Go, no dependencies). `GET /api/v1/ws` with the bearer header; the library's origin check stays on (a browser on another site is refused; a desktop client sends no `Origin`). Text messages of JSON, described in §7.13: the client subscribes to topics (the topics of §8, each checked like the stream of its page, 404 when the user may not read it), the user's own topic is always subscribed, and each event is sent with the resource it concerns as the REST API returns it to that user. A connection has the bounded queue of §8 and is closed with a `resync` message when it overflows. The server pings every 30 s and closes a connection that does not answer within 15 s. A changed session (logout, disabled user, changed role, D37) closes it. Found in step 16.5: a message that is JSON but not a valid message is an `error` of `invalid_input` and the connection stays (invalid JSON, a binary message and more than one value are 1003, over 16 KiB is 1009); a `resync` is followed by the close code 1013 "try again later" for both reasons; the hub tells why a subscription was dropped (`DropReason`: overflow or reconnect) and a connection keeps one hub subscription whose topics are added and removed in place (`AddTopics`, `RemoveTopics`), so that a subscribe leaves no gap; `api.Server.Drain` lets the server wait for the sockets, which `http.Server.Shutdown` does not; `clowder-apicheck -watch` is the client that proves it | The owner's answer, 2026-10-27 ("coder/websocket"). The details decided by the manager, 2026-10-27 |
| D45 | **OpenAPI.** `internal/api/openapi.json`, OpenAPI 3.1 in JSON (so the standard library reads it in the tests; no YAML dependency), embedded and served at `GET /api/v1/openapi.json` without login. A test checks that every route registered under `/api/v1/` has an operation in the document with the same method and path, and the other way round, that every `$ref` resolves, that every operation has a summary, its responses and its error responses, and that every response a handler test receives validates against the schema of its operation (a small checker of the JSON Schema subset the document uses, written in the test). The messages of the WebSocket are schemas of the document's components. `docs/API.md` is the guide for the writers of desktop clients: login, errors, paging, uploads, the WebSocket and an example session | The owner's request, 2026-10-27. Decided by the manager, 2026-10-27 |
| D46 | 1.1 is released as `v1.1.0`. A 1.0 SQLite installation upgrades by running the new binary: the migrations add the new columns | Decided by the manager, 2026-10-27 |
| D47 | The security review of 1.1 (step 17.1, `docs/SECURITY.md`): an ID has 63 bits at most wherever it is read from a path, a query, a form or a JSON body (the databases keep it in a signed column), so a larger number is "not found" (`404`) or "not valid" (`422` with the field) and never a 500; the flags `-database-url` and `-redis-url` have an empty default and take the variable `CLOWDER_DATABASE_URL` or `CLOWDER_REDIS_URL` only when the flag is not given (an empty flag turns the variable off), so that the usage text never prints a password; the cross-origin protection of the pages also covers the unsafe methods of the API (D43 said the API needed none; it is harmless to a client that sends no `Origin`, and a client made of a web view must call the API from its native side). Recorded as accepted, not changed: no limit on sign-up tries (an invitation token is 256 random bits), none on the number of sockets or streams of a user and none on what a user has staged; a cap per user is a recommendation for a later release | Decided by the manager, 2026-10-27; the owner may revise. `docs/SECURITY.md`, `docs/API.md` |
| D48 | PostgreSQL performance (step 17.1, `docs/PERFORMANCE.md`): after the bulk load of `clowder demo -size large` the demo runs `ANALYZE` on PostgreSQL (`Store.Analyze`), because a table that was just loaded has no statistics until autovacuum reaches it, and the planner then reads the feed of a room with a bitmap scan and a sort of all its posts (6 ms instead of 0.1 ms); a database that grows through the services needs nothing. Found in the same step: the Go runtime keeps the 64 MiB of an Argon2id hash in the process after the hash (a server that did one login stayed at 95 MiB instead of 30, and a process with 100 open streams at 155 MiB, over the 150 MB target), so the password-hashing gate (D37) calls `debug.FreeOSMemory` when the last hash in flight ends (only for hashers of 16 MiB or more; `Hasher.MemoryBytes`). The benchmarks take `CLOWDER_BENCH_DATABASE_URL` (a PostgreSQL URL of a large demo) besides `CLOWDER_BENCH_DB`, `scripts/perf.sh` takes `DATABASE_URL`, `REDIS_URL` and `PROCESSES=2` (the fan-out across two processes through Redis, 100 streams on each), `clowder-perf` takes `-base2`, `-pid2`, and `scripts/perf-postgres.sh` runs the whole measurement on throw-away containers | Decided by the manager, 2026-10-27; the owner may revise. §10 |
| D49 | **Settings** (§7.14). One page, `/me/settings`, with the sections Profile, Password, Appearance, Notifications, Sign-in and Sessions; `/me/sessions` becomes its Sessions section (the old address redirects). Everything on it is also in the API under `/api/v1/me`. Preferences are stored per user in the database (a `user_settings` row), so they follow the user to every browser and client. The avatar is decoded and re-encoded by Clowder with the standard library only (a box filter written in the code, no image dependency) | The owner's request, 2026-10-28. Decided by the manager, 2026-10-28 |
| D50 | **Notification preferences like Mattermost's** (§7.15):<br>- global defaults and per-place overrides for rooms and group conversations (notify about all, mentions or nothing; sound; ignore group mentions; mark unread; mute);<br>- the group mentions `@room`/`@all` and `@here`, with presence shared through Redis when several processes run, and mention keywords;<br>- following posts, so that every reply in a thread notifies its followers.<br>The sound is on by default, chosen from four sounds made with the Web Audio API (no audio files). Browser notifications are off by default and say who did what where, never the text. A mute silences everything, and a mention still reaches the bell, as in Mattermost | The owner's requests, 2026-10-28 ("disabling from a group or having sound by default and disabling it", "bob in general did what?", "group notifications should be like mattermost"). The details decided by the manager, 2026-10-28; the owner approved the notification texts |
| D51 | **Password reset by an admin** (§7.1). There is no email, so an admin makes a one-time reset link, the way invitations work: 32 random bytes, stored as SHA-256, 24 hours, used once, a new link replacing the old one. Using it sets a new password and ends every session of the user. `clowder reset-password -user <name>` prints a link, so that the last admin can get back in. The reset does not change SSH keys or the sign-in code; an admin turns off a user's sign-in code separately (for a lost key). Each step writes an audit event of the account (§7.10) | Decided by the manager, 2026-10-28 |
| D52 | **The SSH server** (§7.16), like `github.com/rm4n0s/badgermole`. It is written with `golang.org/x/crypto/ssh` (already allowed, D6), in the package `internal/sshcode`, which talks to `internal/app` only. It is off unless `-ssh-addr` is set. Its host key is an Ed25519 key in the data directory, made on the first start, which several processes share through the data volume. A connection authenticates with a public key of the username it gives; the server answers with a one-time code and closes. It offers nothing else | The owner's request, 2026-10-28. The details decided by the manager, 2026-10-28 |
| D53 | **The sign-in code at login** (§7.16). The SSH code is a second factor after the password, which a user turns on in the settings once one of their keys has fetched a code. Pages: after the right password, a second form asks for the code. The pending login lives in the database for 5 minutes, behind a cookie of its own. API: `POST /auth/login` takes `ssh_code`; without it, a user who needs one gets `401` with the new code `ssh_code_required`. A wrong code counts as a failed login in the limiter. The code alone never logs anyone in | The owner's answer, 2026-10-28 ("rather using TOTP …"). Decided by the manager, 2026-10-28; the owner may revise (a login with the code alone is a non-goal of 1.2) |
| D54 | **Highlighting** with `github.com/alecthomas/chroma/v2` (the owner's approval, 2026-10-28), inside `internal/markdown` as the renderer of fenced code blocks. Its output uses CSS classes, not inline styles, so the theme's light and dark palettes style it. HTML is rendered when it is read, so old posts are highlighted too | The owner's request and approval, 2026-10-28. Decided by the manager, 2026-10-28 |
| D55 | **Unread posts per room** (§7.17). A `room_reads` row per member and room keeps the last post the member has seen. Opening the feed moves it to the newest post. A member who joins starts with nothing unread, and the migration starts existing members with nothing unread | Decided by the manager, 2026-10-28 |
| D56 | **Search in private messages** (§7.11), which resolves the open decision of §16 the way its recommendation said: only inside the Messages page and the API, only in the user's own conversations, words only. It uses an FTS5 table on SQLite and a generated `tsvector` with a GIN index on PostgreSQL, like posts (D38). Admins get nothing more | The owner's answer, 2026-10-28 ("all are good"). Decided by the manager, 2026-10-28 |
| D57 | **The new look.** The owner picked **"Sunbeam"** (2026-10-28: "I like Sunbeam design, but I want it also to work on dark mode") from the three directions of the design canvas (https://claude.ai/artifact/5Eu6uWNtFdhxn7NsPbMDZS, artboards "A · Sunbeam" and "A · Sunbeam, dark"): warm cream ground, ginger accent `#C2410C`, sage second accent, rounded cards, Bricolage Grotesque for headings, Nunito for text, JetBrains Mono for code, a cat-head logo and paw-print marks. The dark theme is a full palette of its own (warm charcoal ground, the same ginger made lighter for contrast), as complete as the light one, not an inversion. For both:<br>- the theme is CSS custom properties in `app.css`, with light and dark palettes (the system's choice, unless the user's setting overrides it with `data-theme` on `<html>`);<br>- the fonts are self-hosted in `internal/web/static/fonts` (woff2, SIL OFL, with their licenses; no request goes to another site);<br>- there is an SVG logo and favicon, and the icons are inline SVG (no icon font; emoji only where users chose them, in reactions);<br>- a test checks that every pair of text and ground colours in the tokens has a contrast of at least 4.5:1, and focus rings are visible;<br>- motion respects `prefers-reduced-motion`.<br>Every page is redone, and the Datastar behaviour stays as it is. | The owner's request, 2026-10-28 ("make it for cat lovers"). The direction is the owner's choice; the rest decided by the manager, 2026-10-28 |
| D58 | **Phones and the installed app.**<br>- Every page works from 360 px wide with no horizontal scroll: the rooms become a menu, the board scrolls inside its own box, and touch targets are at least 44 px.<br>- `manifest.webmanifest` has the name, PNG icons of 192 and 512 px (and maskable ones), the theme colours and `display: standalone`.<br>- A small service worker caches only `/static/`: never pages, API answers or files, which are private | Decided by the manager, 2026-10-28 |
| D59 | **Backups.** `clowder backup -o <file.tar.gz>`, which works while the server runs:<br>- on SQLite it writes a consistent copy of the database (`VACUUM INTO`) and the `files/` directory (avatars included) into one archive, with a `manifest.json` (Clowder version, time, dialect, counts);<br>- on PostgreSQL it writes the files only and prints the `pg_dump` command for the database.<br>`clowder restore -i <file> -data <dir>` unpacks only into an empty directory and refuses archive entries that are absolute, contain `..` or are links | Decided by the manager, 2026-10-28 |
| D60 | **Metrics.** `-metrics-addr` (`CLOWDER_METRICS_ADDR`) is empty, and so off, by default. When set, it serves `GET /metrics` in the Prometheus text format on a listener of its own, never on the public one. The metrics are written by hand, with no client library:<br>- requests by route pattern, method and status class, and a histogram of their durations;<br>- open SSE streams and WebSockets, and hub drops by reason;<br>- logins by result and SSH codes issued;<br>- the database pool, Redis reconnects, and the Go runtime.<br>No label carries a user, an ID or a path with an ID | Decided by the manager, 2026-10-28 |
| D61 | **`govulncheck`** (`golang.org/x/vuln`) is a `tool` of `go.mod` (`go tool govulncheck ./...`) and a check of the Definition of done from step 23.3. It needs the network for its database; when that is missing, the step records it | The owner's answer, 2026-10-28 ("all are good"). Decided by the manager, 2026-10-28 |
| D62 | **Per-user limits** (the recommendations of D47):<br>- at most 30 live connections per user per process, SSE streams and WebSockets together. Beyond that, a stream answers `429` and a WebSocket handshake answers `429`;<br>- staged uploads (§7.12) of at most 10 times the attachment limit per user (`413 too_large`);<br>- a sign-up with an unknown invitation token counts as a failed login of the address in the limiter | Decided by the manager, 2026-10-28 |
| D63 | **Calls with pion** (§7.18). The calls use `github.com/pion/webrtc/v4` and the pion modules it requires (pure Go, cgo-free), with the owner's request of 2026-10-28 as the approval of the dependency. It is imported only in `internal/calls`, which talks to `internal/app` for every permission and never imports `internal/store`. The server forwards the media of a one-to-one call:<br>- one `PeerConnection` per side, VP8 or H.264 for video and Opus for audio, with pion's default interceptors (NACK, RTCP reports, TWCC);<br>- a forwarded track for each track a side sends, and renegotiation started by the server;<br>- no simulcast, transcoding or recording | The owner's request, 2026-10-28. Decided by the manager, 2026-10-28 |
| D64 | **The media ports.**<br>- Calls are on when `-media-addr` (`CLOWDER_MEDIA_ADDR`, for example `:7882`) is set, and off otherwise: the call buttons are then hidden and the API answers `409`.<br>- All ICE goes through that one port: UDP (a pion UDP mux) and TCP (ICE-TCP on the same number, for networks that block UDP).<br>- `-media-public-ip` (`CLOWDER_MEDIA_PUBLIC_IP`) is announced as the host candidate when the server is behind NAT (pion's `NAT1To1IPs`).<br>- No STUN or TURN server is needed, because the server is the peer.<br>- The compose files publish the port in both protocols.<br>- Limits: 2 participants per call, `-max-calls` (default 20) calls per process, 2.5 Mbit/s of video per sender (signalled with REMB and the SDP bandwidth line), a 10-second deadline for ICE to connect, and closing a participant whose transport fails for 15 seconds | Decided by the manager, 2026-10-28 |
| D65 | **Calls across processes.** A call's media lives in the process that started it (the call's *home*), recorded with the call in the database. Each process announces its own media address:<br>- in the cluster composition, `clowder` and `clowder2` publish different ports;<br>- the call's state and the signalling of a participant whose WebSocket reached another process are relayed through Redis (`clowder:calls:<process>`) to the home process;<br>- the ICE candidates the home announces point at its own port, so the media goes straight to the home.<br>When the home process stops, its calls end ("call ended: server restarting") and the members can start a new one | Decided by the manager, 2026-10-28 |

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
| `internal/files` | attachment storage on disk, the notes of uploads that wait for a post (D42), and the headers of a download |
| `internal/live` | the event hub (§8) |
| `internal/web` | the HTTP server, routes, middleware, handlers, SSE streams |
| `internal/web/views` | templ components and pages |
| `internal/web/static` | `datastar.js`, CSS, icons, the drag-and-drop script; embedded with `embed` |
| `internal/api` | the REST API and the WebSocket for desktop clients (§7.12, §7.13), their JSON types, the embedded `openapi.json` (D42–D45) |
| `internal/cluster` | what several processes share through Redis (D41): the bridge that carries the hub's events between processes and the counters of the login limiter; the only package that imports go-redis |
| `internal/sshcode` *(1.2)* | the SSH server of the sign-in codes (D52): the host key, the public-key check and the code, through the services of `internal/app`; the only package that runs an SSH server, and it never imports `internal/store` |
| `internal/metrics` *(1.2)* | the counters and histograms of D60 and their Prometheus text, written by hand; the middleware and the services report to it through a small interface |
| `cmd/clowder-apicheck` | a small API client that exercises the REST API and the WebSocket against a running server and prints what it did: the proof of the API steps and an example for client writers |
| `internal/clock` | the clock interface and its fake for tests |
| `docs/` | this spec, the plan, the log of finished steps, proofs |
| `scripts/` | helper scripts (`test-docker.sh`, D39) |
| `Dockerfile`, `compose.yaml`, `compose.cluster.yaml`, `.env.example`, `deploy/Caddyfile.cluster` | the container image, the compositions and the Caddy configuration of the cluster composition (D40) |

### 6.3 Data model

Every table has an integer primary key and `CreatedAt`; times are stored in UTC.

| Model | Fields (besides ID, CreatedAt) | Notes |
|---|---|---|
| `User` | `Username` (unique, lower case), `DisplayName`, `PasswordHash`, `Role` (admin, member), `DisabledAt`, `AvatarKey` (1.2) | `AvatarKey` is the key of the avatar's file in `files/avatars/`, empty for none (§7.14) |
| `UserSettings` | `UserID` (unique), `Theme` (system, light, dark), `Sound`, `BrowserNotifications`, `Notify` (all, mentions, nothing), `SoundName` (purr, chirp, bell, meow), `Replies` (following, direct) | 1.2; a user has no row until they save a setting, and the defaults are system, on, off, mentions, purr, following (§7.14, §7.15, D49) |
| `PlacePref` | `UserID`, `RoomID` or `ConversationID` (exactly one), `Notify` (default, all, mentions, nothing), `Sound` (default, on, off), `IgnoreGroupMentions`, `MarkUnread` (all, mentions), `Muted` | 1.2; unique per (user, room) and (user, conversation); a row exists only while some choice differs from the default; deleted with the room or conversation, and when the user leaves (§7.15, D50) |
| `Invitation` | `TokenHash` (unique), `Role`, `CreatedBy`, `ExpiresAt`, `UsedBy`, `UsedAt` | one use only |
| `PasswordReset` | `UserID`, `TokenHash` (unique), `CreatedBy` (null for the command line), `ExpiresAt`, `UsedAt` | 1.2; a password reset link (§7.1, D51); a user has one unused link at most, a newer one replacing the older |
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
| `AuditEvent` | `ActorID`, `Action` (§7.10), `RoomID` (0 for an event that belongs to no room), `EntityType` (room, post, comment, task, column, member, user), `EntityID`, `Summary`, `Details` (JSON) | no foreign keys to the entity: the event outlives it. Indexed by time, room, actor and action |

## 7. Behavior

### 7.1 Accounts and invitations

- `clowder invite -admin` prints a one-time link that makes an admin. Admins make invitation links (member or admin) in the admin page. A link expires after 7 days (configurable) and is used once.
- Opening a valid link shows a form for username (3–32 characters of `a-z`, `0-9`, `_`, `-`), display name and password (at least 12 characters). Submitting it creates the user, uses up the invitation and logs the user in.
- Login is username and password. A failed login does not say whether the username exists. After 5 failed attempts for one username or one IP address in 15 minutes, further attempts wait (a limiter in memory, or in Redis when several processes share a database, §8.1).
- A session lasts 30 days from its last use (configurable). Logout deletes it. Disabling a user deletes their sessions.
- Admins see the users, disable and enable them, and change their role. The last admin cannot lose the role.
- *(1.2)* An admin makes a one-time **password reset link** for a user (Admin › Users › Reset password, D51): `<base URL>/reset/<token>`, valid for 24 hours, used once, a newer link replacing an older one, refused for a disabled user. Opening it shows a form for the new password, twice; saving ends every session of the user and logs them in. `clowder reset-password -user <name>` prints a link on the command line (it reads `-data`, `-database-url`, `-base-url`). Writes `user.reset_link_created` and `user.password_reset`.

### 7.2 Rooms

- Room names are unique (1–64 characters). A new room gets the columns To do, In progress, Done.
- Visible rooms: every user sees them in the room list, can read them, and joins with one click. Only members post, comment, react and change the board.
- Private rooms: only members see them, anywhere (list, search, notifications, links: a non-member gets 404). The creator is the first member. Any member adds any user; the added user is notified. A private room with no members left is kept for admins to delete.
- Any member leaves a room. Admins remove a member from any room. Admins rename, change the description of, and delete any room; the creator of a room can too.

### 7.3 Posts and comments

- The editor has: the body with a **Text / Markdown** switch, attachments, tasks (§7.6) and tags (§7.7), the last two in their own fields below the body.
- Text is shown as written (escaped, line breaks kept, links made clickable). Markdown is rendered by goldmark in safe mode with GitHub-flavored extensions (tables, strikethrough, autolinks, task-list items shown as read-only checkboxes) (D9).
- *(1.2)* A fenced code block names its language after the fence (` ```go `, ` ```python `, any name or alias chroma knows) and is highlighted (D54); with no name or an unknown one it is plain. A block over 32 KiB or 1,000 lines is not highlighted. The block shows its language and a Copy button. Highlighting adds only `<span class="…">` around escaped text; the safe-mode rules of D9 are unchanged.
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
  | user *(1.2)* | `user.password_changed`, `user.reset_link_created`, `user.password_reset`, `user.ssh_key_added`, `user.ssh_key_removed`, `user.ssh_code_enabled`, `user.ssh_code_disabled`, `user.ssh_code_removed` (by an admin); they belong to no room, and the admin's filter of the log gets "Accounts" |

- *(1.2)* A reset link made with `clowder reset-password` has no admin to name, and the table of events has a foreign key from the actor to the users, so its `user.reset_link_created` names the user of the link as its actor, says in its `Summary` that it was "made on the command line" and has `"via": "command line"` in its `Details` (decided by the manager, 2026-10-28: a reserved system user or a nullable actor would reach into the users list and every audit query for one rare event; the owner may revise). The event `user.password_reset` has the user as its actor in every case. Neither holds the token or a password.
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
- *(1.2)* Group mentions (`@room`, `@all`, `@here`), mention keywords, and replies in followed posts notify too; §7.15 has the rules.

### 7.11 Private messages

- **Starting.** From a user's name anywhere, or from the Messages page, a user starts a conversation with one other user (opening the existing one if there is one) or with several (a group, up to 50 members, with an optional name). Disabled users cannot be added.
- **Members.** Any member of a group adds other users and renames the group; any member leaves it. A direct conversation has exactly its two users and nobody can be added; to include a third person, a new group is started.
- **Messages.** A message is plain text (escaped, line breaks kept, links clickable, emoji as typed), up to 4,000 characters, with optional attachments (the limits and the download checks of §7.5, readers being the conversation's members). A message can reply to an earlier message of the same conversation, shown as a quote above it. The author edits a message (it shows "edited") and deletes it (it shows "message deleted" to everyone; its attachments are removed).
- **Reactions.** Members react with emoji as in §7.4.
- **Reading.** The Messages page lists the user's conversations, the most recent first, with the last message and an unread mark. A conversation shows its messages oldest first, newest at the bottom, 50 at a time with older ones loaded on scrolling up. Opening a conversation marks it read; a direct conversation shows "Seen" under the last message the other user has read.
- **Unread count.** The sidebar shows the number of conversations with unread messages, updated live. Messages are not notifications (§7.9) and do not ring the bell.
- **Search** *(1.2, D56)*. The Messages page has a search box for the messages of the user's own conversations, the ones they can read now: words and phrases as in §7.8, no tags, newest first, 30 at a time, each result linking to the message in its conversation. `GET /api/v1/messages/search?q=`.
- **Calls** *(1.2)*. The two users of a direct conversation call each other, with video or audio only (§7.18). Group conversations have no calls.
- **Privacy.** Only the members read a conversation; a non-member gets 404, admins included. A member who left no longer reads it. Messages are not searched by §7.8 (only by the search of the Messages page) and not written to the audit log.

### 7.12 REST API (1.1)

The API is for desktop clients that act as one logged-in user (D42, D43). It is versioned in the path: `/api/v1/`. Everything below is a call of a service method of `internal/app`, so the rules, limits, notifications, audit events and live events of §7.1–§7.11 apply exactly as they do to the pages.

- **Auth.** `POST /auth/login`, `POST /auth/signup`, `GET /auth/invitations/{token}` (whether an invitation link is valid, and its role), `POST /auth/logout`. Every other route needs `Authorization: Bearer <token>`; a missing, unknown or expired token is 401 `unauthenticated`. The login limiter of §7.1 applies (429 `rate_limited` with `Retry-After`).
- **Me.** `GET /me` (the user, unread notification and conversation counts), `GET /me/sessions`, `DELETE /me/sessions/{id}`.
- **Users.** `GET /users?q=` (enabled users by username or display name prefix, 20 at most: what a client needs to pick members and mention people).
- **Rooms.** `GET /rooms` (the visible rooms and the user's private ones, with whether the user is a member), `POST /rooms`, `GET /rooms/{id}`, `PATCH /rooms/{id}` (name, description), `DELETE /rooms/{id}`, `POST /rooms/{id}/join`, `POST /rooms/{id}/leave`, `GET /rooms/{id}/members`, `POST /rooms/{id}/members` (add), `DELETE /rooms/{id}/members/{user_id}`, `GET /rooms/{id}/candidates?q=` (users who can be added).
- **Posts and comments.** `GET /rooms/{id}/posts?before=<post id>&limit=` (the feed, newest first, 30 by default and at most 100), `POST /rooms/{id}/posts`, `GET /posts/{id}` (a post or a comment), `GET /posts/{id}/thread` (the post and all its comments, in reading order with the depth of each, which is a tree), `POST /posts/{id}/comments` (a reply to a post or a comment), `PATCH /posts/{id}`, `DELETE /posts/{id}`, `PUT /posts/{id}/reactions/{emoji}` and `DELETE /posts/{id}/reactions/{emoji}` (the emoji URL-escaped; adding one that is there, or removing one that is not, is not an error), `GET /tags?prefix=` (suggestions). A post input is `{format, body, tags[], tasks[{id?, title, assignee_ids[], due_date?}], upload_ids[], attachment_ids[]}`: `upload_ids` are new files, `attachment_ids` the existing ones an edit keeps.
- **Uploads and attachments.** `POST /uploads` (multipart, one file in the field `file`; the limits of §7.5, 413 `too_large` over the limit) returns an upload id that one later create or edit takes in `upload_ids`, as the editor does; only the user who uploaded a file can attach it; `GET /attachments/{id}` downloads with the checks and headers of §7.5; the same for messages under `/messages/attachments/{id}`.
- **Board.** `GET /rooms/{id}/board` (the columns with their cards), `POST /rooms/{id}/columns`, `PATCH /columns/{id}` (name), `POST /columns/{id}/move` (position), `DELETE /columns/{id}`, `PATCH /tasks/{id}` (assignees, due date), `POST /tasks/{id}/move` (column, position).
- **Search.** `GET /search?q=&page=` with the query language of §7.8 (`#tag`, `room:`, `from:`, dates, posts only), 20 results a page.
- **Notifications.** `GET /notifications?before=&limit=` (30 by default, 100 at most), `POST /notifications/read` (`{up_to_id}`, as the page does).
- **Messages.** `GET /conversations`, `POST /conversations` (`{user_ids[]}` or `{usernames[]}`, exactly one of the two lists, and `name` for a group: one other user opens the direct conversation, 201 when it is new and 200 when it existed; the caller's own name in the list is not counted), `GET /conversations/{id}`, `PATCH /conversations/{id}` (name), `POST /conversations/{id}/members`, `POST /conversations/{id}/leave`, `GET /conversations/{id}/messages?before=&limit=` (oldest first in the page, 50 by default), `POST /conversations/{id}/messages` (`{body, reply_to_id?, upload_ids[]}`), `PATCH /messages/{id}`, `DELETE /messages/{id}`, `PUT`/`DELETE /messages/{id}/reactions/{emoji}`, `POST /conversations/{id}/read` (`{up_to_id}`, as the page does). A conversation has `members` (the current ones), `last_message`, `unread`, `can_send` (false in a direct conversation with a disabled user) and, in a direct conversation, `seen_up_to_id`, the last message the other user has read, which drives "Seen"; a message has the text as written (`body`) and as the pages show it (`html`, D33), and a deleted one keeps its author and time and has no `body`, `html`, files or reactions. A conversation, a message and a file of one are 404 for anyone who is not a member, admins included.
- **Admin** (admins only, 403 `forbidden` for others): `GET /admin/users`, `PATCH /admin/users/{id}` (role, disabled), `POST /admin/invitations` (role; returns the link and when it expires), `POST /admin/users/{id}/reset-link` (no body; 201 with `{url, expires_at}`, the link `<base URL>/reset/<token>`, shown once; 404 for an unknown user and 409 for a disabled one; the link is used on a page, not through the API), `GET /admin/rooms`, `GET /admin/rooms/{id}/members`, `GET /admin/audit?page=&room_id=&actor_id=&entity_type=&action=&from=&to=` (`from` and `to` are UTC days, `YYYY-MM-DD`, both included; a filter that is not valid is 422, where the page ignores it). `POST /admin/invitations` returns the link built from the base URL of the configuration (`<base URL>/invite/<token>`). Disabling a user through `PATCH /admin/users/{id}` ends their sessions, API tokens included, and publishes `user.changed` (D37).
- **Other.** `GET /openapi.json` (D45) needs no login. `GET /healthz` (D40) is outside `/api/`.
- An upload that no post takes is removed, file and note, 24 hours after it was made (decided by the manager, 2026-10-27; the owner may revise), so that a user cannot fill the disk with files that are never posted. `clowder serve` sweeps at start and then every hour; the sweep is one for the posts and the messages: `POST /uploads` serves both, and a staged upload is attached once, by a post, a comment or a message of the user who uploaded it (step 16.4).
- A request body is at most 64 KiB, except uploads (§7.5). A body that is not JSON, or has unknown fields, is 422 `invalid_input`.
- The exact paths, parameters and schemas are those of `internal/api/openapi.json`, which is kept equal to the routes by a test (D45). A change of a route changes the document in the same commit.

### 7.13 WebSocket (1.1)

`GET /api/v1/ws`, with the bearer token as for every API route (D44). The token is checked before the upgrade: a missing or wrong one is the plain 401 JSON answer, and a request that is no handshake is refused by the library (426, 400; a browser from another site: 403). All messages are JSON text messages with a `type`; the messages are the schemas `WsClientMessage` and `WsServerMessage` of `openapi.json`.

- **Client to server.** `{"type": "subscribe", "id": 1, "topics": ["room:3", "post:17"]}`, `{"type": "unsubscribe", "id": 2, "topics": [...]}`, `{"type": "ping", "id": 3}`. `id` is the client's own number (a non-negative integer, required), echoed in the answer. The topics are those of §8: `room:<id>`, `post:<id>` (the thread of a top-level post; a comment is `invalid_input`), `board:<id>` (a room's board), `conversation:<id>`, each written in decimal with no sign and no leading zero. `user:<id>` of the user is subscribed from the start; asking for it again is fine, removing it is `invalid_input`, and another user's is `invalid_input`. At most 200 other topics per connection. Subscribing to a topic already held, and unsubscribing from one not held, is not an error.
- **Server to client.** `{"type": "ack", "id": 1, "topics": [...]}` (the topics of the request, without repeats) when the request was done; `{"type": "error", "id": 1, "topic": "room:9", "error": {...}}` (the error object of §7.12) when it was not: a subscribe is all or nothing, so nothing of that request is subscribed, and `topic` names the first topic that failed. The names are all checked before any permission, so a name that is no topic is found first; then the limit; then the permissions, in the order of the request (a room, thread, board or conversation the user may not read is `not_found`, admins included). A message that is valid JSON but not a message (not an object, an unknown field, a type that is not one of the three, a wrong type of a field, no `id`, no topics) is answered with an `error` of `invalid_input` with the `fields` and, when its `id` could be read, that `id`; the connection stays. `{"type": "pong", "id": 3}`; `{"type": "event", "topic": "room:3", "kind": "post.created", "room_id": 3, "post_id": 42, ..., "data": {...}}` for each live event the user may see; `{"type": "resync", "reason": "overflow" | "reconnect"}` just before the server closes the connection with 1013 when its queue overflowed or the Redis bridge reconnected (§8.1): the client reconnects and reloads what it shows. The server sends nothing when a connection opens, so a client subscribes first and then loads what it shows with the REST routes: nothing falls in between.
- **Events.** The kinds are those of `internal/live` (§8) except `user.changed`, which is handled by the server (below). The envelope has `topic`, `kind` and the IDs the event has (`room_id`, `post_id`, `root_id`, `user_id`, `conversation_id`, `message_id`; absent when 0). `data` is the resource as the REST API returns it to this user at the time of sending, built by the functions of the handlers, and absent for a deletion:
  - `post.created`, `post.updated`, `comment.created`, `comment.updated`: the `Post` (a comment's change is announced on `post:<root id>` as `comment.*` and on `room:<id>` as `post.updated` of its root, so that the comment count refreshes);
  - `reaction.changed` and `message.reaction`: `{reactions}`, the reactions of the post, comment or message as this user sees them (`mine` is theirs), as the routes that put a reaction answer;
  - `board.changed`: the `Board`; `room.updated`: the `Room`; `member.joined`: the `Member` (no event when the member is gone again); `notification.created`: the `Notification` (the event does not say which one it is, so the server sends one event for each notification newer than the last it sent on that connection); `notification.read`, and `conversation.read` on `user:<id>`: `{unread_notifications, unread_conversations}`; `message.created`, `message.updated`: the `Message`; `conversation.updated`, `conversation.changed`: the `Conversation` (none when the user is no longer a member); `conversation.read` on `conversation:<id>`: `{conversation_id, user_id, up_to_id}` (who has read up to which message);
  - no `data`: `post.deleted`, `comment.deleted`, `message.deleted`, `member.left`, `room.deleted`.
  - Memberships of rooms and room deletions are also sent on `user:<id>` (`member.joined`, `member.left`, `room.deleted`), as the pages' sidebar hears them.
  - **Access.** Every event is checked for the user as the pages' streams do (the service call that builds `data`, or the call that subscribe used when there is none). An event the user may no longer see is not sent. When the user has lost a room or a conversation they subscribed to (they left or were removed from a private room, the room or the conversation was deleted, they left the conversation), one `error` of `not_found` without an `id` and with the `topic` is sent for each topic of that room or for that conversation, and the topics are dropped. `room.deleted` is sent on the room's topic first, then the topics of the room are dropped the same way. A visible room that the user leaves stays readable, so its topics stay.
- **Lifecycle.** The server pings (WebSocket ping frames) every 30 s and closes a connection that does not answer in 15 s; each answered ping also reads the session again. Logging out that session, disabling the user, an expired session (4001 "session ended"), or changing their role (4002 "session changed"; a client reconnects after 4002) closes the connection with that code; the server learns of the first three at once from `user.changed` (D37) and the rest at the next ping. A text that is not one JSON value (also an empty text and two values) or a binary message closes it with 1003, a message over 16 KiB with 1009. A write to a client has 10 s; a client that does not read is closed, with `resync`, when its queue of 64 events overflows. The server's shutdown (§8) closes every connection with 1001 "going away": it closes the hub and waits for the connections to end before the process does.

### 7.14 Settings (1.2)

- **The page.** `/me/settings` is reached from the user's avatar in the header. It has six sections; each one saves on its own and says "Saved".
- **Profile.**
  - The display name follows the rules of sign-up.
  - The avatar is a PNG, JPEG or GIF (its first frame) of up to 2 MiB and 4,096×4,096 px. Clowder checks the size from the image's header before decoding it.
  - Clowder cuts the centre square, scales it to 256×256 and stores it as a PNG in `files/avatars/`. Removing it brings back the initials on a colour picked from the user's ID.
  - Any logged-in user sees avatars at `GET /avatars/{userID}`, as `image/png`, cached for a day. A new avatar gets a new address.
- **Password.**
  - The form asks for the current password and the new one twice. The new one has at least 12 characters and differs from the current one.
  - Saving it ends every other session of the user, browsers and clients, and keeps the current one.
  - It writes `user.password_changed`. A wrong current password counts in the login limiter.
- **Appearance.** The theme is System (the default), Light or Dark (D57).
- **Notifications** (§7.15): the global preferences (notify about, sound and its choice, browser notifications, replies, mention keywords), and the list of rooms and conversations that have their own preferences, each with a link to its dialog.
- **Sign-in** (§7.16):
  - the SSH keys, each with its name, type, SHA-256 fingerprint, when it was added and when it was last used, and Remove;
  - Add a key;
  - the switch "Ask for an SSH code when I log in";
  - the server's host-key fingerprint and the `ssh` command to run.
- **Sessions.** The list of 1.1 (D43), with End.
- **API.** `GET`/`PATCH /me/settings` (theme, sound, browser notifications); `PATCH /me` (display name); `PUT`/`DELETE /me/avatar` (multipart); `POST /me/password`; `GET`/`POST /me/ssh-keys` and `DELETE /me/ssh-keys/{id}`; `PUT`/`DELETE /me/ssh-code` (the switch); the notification preferences of §7.15. A password is needed to add a key and to change the switch: `current_password` in the body. The `avatar_url` of every user object is `/api/v1/avatars/{id}?v=<key>`, or null: the address of the pages (`/avatars/{id}`) reads the cookie, which a client does not have, so the API serves the same PNG to a bearer token (decided by the worker of step 18.1, 2026-10-28). The events `user.updated` (the user object) and `settings.changed` (the settings) go to the user's own topic only, so the pages of other people show a new name or avatar the next time they are opened.

### 7.15 Notification preferences (1.2)

These work the way Mattermost's do (the owner's request, 2026-10-28: "group notifications should be like mattermost"). There are global defaults in the settings, and each room and each group conversation can override them in its own "Notification preferences" menu. A "group" here is a room or a group conversation; a direct conversation has the same menu without the group-mention option.

- **What counts as a mention of the user:**
  - `@username`, always.
  - **Group mentions.** `@room` (alias `@all`) mentions every member of the room or group. `@here` mentions the members who are online now: they have a Clowder page or a client connected, through any process; presence is shared through Redis when several processes run.
    - Group mentions in a room work only in posts and comments, and in a group conversation only in messages.
    - Before sending one to more than 5 people, the editor asks "This will notify 14 people. Send anyway?".
    - Each user can ignore them in a place (below).
  - **Keywords** of the user's own: up to 10 words or phrases, case-insensitive, matched as whole words outside code (for example their first name, "deploy", "feeder"). A keyword match is a mention.
  - `@username` in a group conversation mentions that member, as in a room.
- **Global settings** (Settings › Notifications), each with its default:
  - **"Notify me about"**:
    - *All new activity*: every new post in my rooms and every message in my conversations;
    - *Mentions, direct messages and group messages* (default): mentions, replies in threads I follow, assignments, being added, and every message in my direct and group conversations;
    - *Nothing*.

    It decides what rings the sound and shows a browser notification. The bell (§7.9) is not affected: it always gets the notifications of §7.9 and mentions.
  - **Sound**: on (default) or off, and which one: *Purr* (default), *Chirp*, *Bell* or *Meow*. These are short sounds made with the Web Audio API (no audio files), each with a Play button to hear it.
  - **Browser notifications**: off by default; turning them on asks the browser's permission.
  - **Replies**:
    - *Threads I follow* (default): a new comment anywhere under a post I follow notifies me;
    - *Only direct replies*: as in 1.0, a comment under my post or comment.
  - **Mention keywords**: the list above; `@room`, `@all` and `@here` are on by default.
- **Following a post** (a thread, as in Mattermost):
  - A user follows a post automatically when they write it, comment under it, or are mentioned in it.
  - The post page and the post's menu have Follow and Unfollow.
  - With *Threads I follow*, each new comment by someone else under a followed post creates a `reply` notification in the bell. A burst of comments within 1 minute counts as one notification, "Bob and 2 others replied in …".
- **Per place** (the menu of a room or a conversation › Notification preferences):
  - **Notify me about**: *Default (what the global setting says)*, *All new activity*, *Mentions* or *Nothing*. A group conversation's default is *All*, like Mattermost's group messages; a room's default is the global setting.
  - **Sound** in this place: Default, On or Off.
  - **Ignore `@room`, `@all` and `@here`** in this place: off by default.
  - **Mark as unread** (the sidebar count, §7.17):
    - *All new posts*: the count of D55; the default for rooms;
    - *Only mentions*: the room shows only the number of its unread mentions, and is otherwise not bold.

    A conversation always counts all its messages.
  - **Mute**:
    - The place is dimmed and moves to the end of its list.
    - It has no unread count, only a mention badge.
    - Nothing rings and nothing pops up, not even mentions.
    - A mention still reaches the bell and shows its badge, as in Mattermost.
    - Unmute brings back the place's other choices as they were.
  - Leaving a room or a conversation removes its preferences.
- **When a notification rings.** The sound plays, and the browser notification shows when no Clowder page of the user has the focus, if the place's "Notify me about" covers the event, the place is not muted, and the event is not in the conversation or room open in the focused tab.
  - At most one sound plays every 2 seconds.
  - A browser that has not been interacted with yet plays nothing (the browser forbids it), and nothing is queued.
  - Each event is shown by one tab only, chosen through `BroadcastChannel`.
- **The text of a browser notification** says who did what and where, never the body:
  - "Bob mentioned you in #general" (for `@room` and `@here` too);
  - "Bob replied in «Feeder schedule» in #general" (the start of the post's first line, up to 40 characters, as the thread's name);
  - "Bob assigned you «Review the retry» in #general";
  - "Bob added you to #design";
  - "Bob posted in #general" (with *All new activity*);
  - "Bob sent you a message";
  - "Bob wrote in Feeder team".

  Clicking it opens the place. *(The owner's answer, 2026-10-28: "bob in general did what?")*
- **How it reaches the page.** The user's stream, which every page already has, carries the events. The page decides with the user's preferences, which come with the page and change live (`settings.changed`, `place_prefs.changed`). The WebSocket carries the same events, so a desktop client can do the same.
- **API:**
  - `GET`/`PATCH /me/settings` gains `notify`, `sound`, `sound_name`, `browser_notifications`, `replies`, `keywords` and `group_mentions`;
  - `GET`/`PUT /rooms/{id}/notification-prefs` and `GET`/`PUT /conversations/{id}/notification-prefs` (`notify`, `sound`, `ignore_group_mentions`, `mark_unread`, `muted`);
  - `PUT`/`DELETE /posts/{id}/follow`;
  - `GET /me/presence?user_ids=` (who is online, for the members' list).

- **Decisions of step 18.3** (the worker's, 2026-10-28; the owner may revise):
  - One function decides whether an event rings (`ringFor`): a mute rings nothing; a place's own *Notify me about* overrides the global one; a conversation whose place choice is *Default* counts as *All* unless the global setting is *Nothing*; *Nothing* covers no event, *Mentions* covers notifications and messages that mention the user, *All* also covers posts and messages. The sound is the place's *On*/*Off*, else the global *Sound*, and its name is the global one (Purr when none).
  - The server decides per recipient when it fans an event out and puts `ring`, `sound` and `browser` on the event (never the body or its text, which the page reads from the notice it appends to `#notify-events`); `notify.js` obeys the decision and adds only what the browser knows (focus, the open place, the first touch, the 2-second gap, the election between tabs).
  - `ignore_group_mentions` is stored and returned now; it takes effect with `@room`, `@all` and `@here` in step 18.4. The global `keywords`, `group_mentions` and the follow endpoints come in 18.4 too.
  - `PUT .../notification-prefs` is partial: a field that is left out keeps its value, and an empty body is 422. There is no endpoint for the global choices other than `PATCH /me/settings`.
  - A muted room's mention badge in the sidebar waits for step 20.2 (the sidebar rework); until then a muted place has no count and the bell alone shows its mentions. A muted conversation does not count in the unread number.

### 7.16 SSH sign-in codes (1.2)

- **The server** (D52):
  - It runs when `-ssh-addr` (`CLOWDER_SSH_ADDR`) is set, for example `:2222`. The compose files publish `CLOWDER_SSH_PORT`.
  - The host key is `-ssh-host-key` (`CLOWDER_SSH_HOST_KEY`), by default `ssh_host_ed25519_key` in the data directory. It is made on the first start with mode 0600, created exclusively, so that two processes starting together end up with the same key.
  - The log of the start and the Sign-in section show its SHA-256 fingerprint.
  - `-ssh-public-host` (`CLOWDER_SSH_PUBLIC_HOST`, by default the host of the base URL) is the host shown in the `ssh` command.
- **Keys.**
  - A user adds a public key with a name (1–40 characters) and the line of a `.pub` file. Clowder accepts these types:
    - `ssh-ed25519`;
    - `ecdsa-sha2-nistp256`, `-nistp384` and `-nistp521`;
    - `sk-ssh-ed25519@openssh.com` and `sk-ecdsa-sha2-nistp256@openssh.com`;
    - `ssh-rsa` of at least 3,072 bits.
  - A key belongs to one user only, and a user has at most 10 keys. Adding and removing a key need the current password and write `user.ssh_key_added` and `user.ssh_key_removed`.
- **Fetching a code.**
  - The user runs `ssh -p 2222 alice@chat.example.org`. The server accepts only public-key authentication, with a key of the username given, whose user is not disabled. It writes:

    ```
    Your Clowder sign-in code: K7QF-9MZP (valid for 60 seconds)
    ```

    and closes the connection. The command `json` writes `{"code":"K7QF9MZP","expires_at":"…"}` instead.
  - The server refuses everything else: shells, PTYs, other commands, subsystems, forwarding and environment requests.
  - A code is 8 characters of Crockford base32 (40 bits; the dash is for reading and is ignored on entry, and so is case). It is stored as its SHA-256 with the user and the key, lasts 60 seconds and is used once. A new code replaces the user's unused ones.
  - The key's "last used" time is updated. The log records the user, the key's fingerprint and the address, never the code.
- **Limits.**
  - A connection has 10 seconds for its handshake and at most 3 authentication attempts.
  - A failed public-key authentication counts as a failed login of the address in the limiter.
  - A user gets at most 10 codes in 15 minutes.
  - Each process accepts at most 64 SSH connections at a time. Shutdown closes them.
- **At login** (D53):
  - A user turns on "Ask for an SSH code when I log in" once one of their keys has fetched a code (that proves the setup works). This needs the current password and writes `user.ssh_code_enabled`. Turning it off needs the password too and writes `user.ssh_code_disabled`.
  - Pages: when the password is right and the user has the code turned on, the login page shows the second form, "Run `ssh -p 2222 alice@chat.example.org` and type the code". The pending login is a row with a random token (stored as SHA-256), kept in the cookie `clowder_pending` (HttpOnly, SameSite=Strict, 5 minutes). A right code makes the session and deletes the row.
  - A wrong code counts as a failed login. After 5 wrong codes the pending login is deleted.
  - API: `POST /auth/login` takes `ssh_code`. When it is missing, the answer is `401` `ssh_code_required` (the password was right); when it is wrong, `401` `unauthenticated`.
  - Sign-up and a reset link do not ask for a code.
  - An admin turns off the code of a user who lost their keys (Admin › Users › Turn off SSH code), which writes `user.ssh_code_removed`.
  - When the server has no SSH address, the switch cannot be turned on. Users who have it on are not asked for a code while the server is off, and the admin's users page warns that the server is off.

### 7.17 Unread state (1.2)

- **Rooms.**
  - The sidebar shows, for each room the user is a member of, the number of top-level posts by others newer than the last one they saw (D55). The count reads "99+" above 99, a room set to *Only mentions* shows its unread mentions instead, and a muted room shows only a mention badge (§7.15).
  - Opening the room's feed marks the newest post as seen, and the count updates live on every page.
  - The feed shows the line "New since your last visit" above the oldest unseen post, worked out before the mark moves.
- **The tab.** The title starts with "(n) ", where n is the user's unread notifications plus their unread conversations (not muted), and the favicon gets a dot when n > 0. Both update live.
- **API.** Each room of `GET /rooms` gets `unread_posts`, and `POST /rooms/{id}/read` (with `post_id`) marks what a client has shown. The WebSocket sends `room.unread` to the user.

### 7.18 One-to-one calls in private messages (1.2)

The owner's request, 2026-10-28: "I want in private messages to be able do video calls with webrtc using pion", and then: "video calls in private messages that are one-to-one". A call is between the two users of a **direct conversation**; group conversations and rooms have no calls. The browser talks WebRTC with the Clowder server, which runs a small forwarding server made with pion (D63): each side sends its audio and video to the server, which forwards it to the other. Media never goes from one user to the other directly, so a call works through any NAT the server can be reached from, with no STUN or TURN server.

- **Starting.**
  - In a direct conversation, either user presses **Video call** or **Audio call** in its header.
  - The other user gets a ringing invitation: a dialog in every Clowder page they have open, with **Answer with video**, **Answer with audio** and **Decline**, and their client gets `call.ringing` on the WebSocket.
  - The ringtone is a call sound of its own, *Purr ring*, repeated for up to 45 seconds and played even when the user's notification sound is off. When the conversation is muted, or its preferences say *Nothing*, the invitation shows without the ringtone (§7.15).
  - A browser notification ("Bob is calling you") shows when no Clowder page has the focus and browser notifications are on.
  - The caller sees "Calling Bob…" with Cancel.
  - A user is in at most one call at a time. Calling someone who is in another call tells the caller "Bob is in another call" (the callee still gets a missed call).
- **In the call.**
  - Each side sees the other large and themselves small.
  - Each can mute and unmute the microphone, turn the camera off and on, share a screen or a window (shown instead of the camera), choose the devices, and hang up.
  - The page shows the call's duration and a mark when the other side's microphone is muted or their camera is off. The conversation stays usable beside the call on a wide screen.
  - Leaving the page leaves the call, after a confirmation.
- **Who.**
  - Only the two members of the direct conversation can start, see or answer its call; anyone else gets 404, admins included.
  - Disabling a user, or a session ending, ends the call at once.
  - A call is refused when the other user is disabled.
- **The end.**
  - A call ends when either side hangs up, when the caller cancels, when the callee declines, or when nobody answers within 45 seconds.
  - The conversation then gets a call entry in its history: "Video call · 12 min", "Declined video call", or, for the callee, "Missed video call". A missed call raises the conversation's unread count like a message.
  - Nothing of the media is recorded or stored.
- **API** (D45) for desktop clients:
  - `GET /conversations/{id}/call` (the call in progress), `POST /conversations/{id}/call` (start, `{"video": true}`; `409 conflict` for a group conversation or a busy user), `POST /conversations/{id}/call/answer` and `POST /conversations/{id}/call/decline`;
  - the signalling WebSocket `GET /api/v1/conversations/{id}/call/ws` (bearer header), whose messages are in the OpenAPI document;
  - the user's WebSocket carries `call.ringing`, `call.started`, `call.updated` and `call.ended`;
  - `docs/API.md` explains how a desktop client takes part with any WebRTC stack.
- **Signalling** is a WebSocket of its own per participant: `GET /calls/{conversationID}/ws` for pages (the session cookie and the cross-origin check), and the API route above. It carries the SDP offers and answers, the ICE candidates, the microphone and camera state, and the renegotiations when a screen share replaces the camera. The pages need no other script library: the browser's own `RTCPeerConnection`, and a small `call.js`.

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
  - the notes of the uploads of the API that wait for a post (`files/notes/<key>`): written whole and renamed into place within the upload request and read by the later request on any process through the shared directory; ended by the request that takes them, or by the sweep of the uploads older than 24 hours that every process runs at start and every hour (removing what is already gone is not an error, so processes may sweep at the same time; the age is the time written in the note, a damaged note counts from its last write);
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
  - `clowder reset-password -user <name>` *(1.2)*: prints a one-time link (24 hours) that lets the user set a new password; it reads `-data`, `-database-url` and `-base-url` and refuses an unknown or disabled user.
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
| M18 | Settings: the settings page (profile, avatar, password, theme, sessions), password reset links and `clowder reset-password`, notification preferences like Mattermost's (per place, sound, browser notifications, `@room`/`@here`, keywords, following posts) (§7.14, §7.15, D49–D51) | a user changes their password, sets a group to *Mentions*, and is notified by an `@here`, and an admin's reset link lets a locked-out user in |
| M19 | SSH sign-in codes: the SSH server, the keys, the code at login in the pages and the API (§7.16, D52, D53) | `ssh` hands alice a code, and her login asks for it and accepts it once |
| M20 | Reading: code highlighting, unread posts per room, search in private messages (§7.3, §7.17, §7.11, D54–D56) | a Go block is highlighted, a room shows its unread count live, and a message is found by a word |
| M21 | The new look: the theme, fonts, logo and icons, every page redone in light and dark, phones and the installed app (D57, D58) | every page has its proof in light, dark and at phone width, and the browser tests pass |
| M22 | One-to-one calls: the pion media server and its signalling, calls in the pages, calls across processes (§7.18, D63–D65) | alice video-calls bob from their direct conversation, each sees the other, also when their pages are on different processes |
| M23 | Operations and limits: backup and restore, metrics, the per-user limits, `govulncheck` (D59–D62) | a backup restores into a server that serves the same data, and `/metrics` answers on its own address |
| M24 | 1.2: the security review of the new surface (the SSH server and the calls first), the docs, `v1.2.0` | every check passes and `v1.2.0` is on `origin` |

## 14. Demo data

There is no `examples/` directory: Clowder is an application, not a library. `clowder demo` fills a database with users, rooms, posts, threads, reactions, attachments and tasks, and prints the login of a demo admin. Proofs and manual checks use it. `clowder demo -size large` adds the data set of §10 (200 users, 100 rooms, 100,000 posts and comments, 20,000 tasks, a handful of private conversations) to it and prints the counts; it takes seconds, not minutes (D35). The default, `-size small`, is the demo the proofs and tests rely on.

## 15. Licensing and third-party code

Clowder is BSD-3-Clause (`LICENSE`). `datastar.js` is embedded as released by its authors; its license (MIT) is kept next to it in `internal/web/static/` and listed in a `NOTICE` file with the other embedded third-party files. Go dependencies are not vendored.

## 16. Open decisions

New ones are added here with a recommendation and moved to §4 once decided.

| Decision | Recommendation |
|---|---|
| Can admins read private messages, and are messages in the audit log? | No to both (§7.11 as written): they are private between their members |

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
