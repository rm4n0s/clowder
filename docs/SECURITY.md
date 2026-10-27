# Security

This file records the security reviews of Clowder: of 1.0 (step 13.2, 2026-10-27) in the first part, and of what 1.1 added (step 17.1, 2026-10-27) in the part "Clowder 1.1" at the end: what was reviewed, what was found and fixed, what is accepted, what is out of scope, and how to report a vulnerability. The decisions the first review led to are D36 and D37 in `docs/SPEC.md`; those of the second are D47 and D48.

## How to report a vulnerability

Open an issue in the GitHub repository of the project (`github.com/rm4n0s/clowder`) with the label "security", and say what you found and how to repeat it. Do not put a working exploit against a live installation in a public issue: say in the issue that you have details, and the repository owner will tell you where to send them. You can also contact the repository owner through their GitHub profile. There is no security mailing list.

## What was reviewed

Every route of `internal/web/server.go` (73 patterns) and every service method it reaches, in this order:

- **Access.** Every route that needs a session has one (routes are protected unless marked `optionalSession` or `noSession`). Every service method takes the acting user and checks the permission itself. A private room, a post in it, its board, its files, a private conversation and its messages are the 404 page to anyone who is not a member, admins included (SPEC D29, §7.11). The admin pages are the 404 page to members. Edit and delete are for the author, or for an admin where SPEC §7.3 and §7.2 say so. The IDs of a path, a query and a form were checked to belong to the parent they come with: a card, a column or a member of another room, a file of another post, a message to reply to from another conversation, a message of a conversation the user is not in.
- **Streams.** Each stream checks access when it subscribes and again for each event it renders (it reads through the services as its user); a user who loses a room or a conversation is sent away and gets nothing more; `Last-Event-ID`, `seen` and `before` are only IDs used as cursors inside the room or conversation that was already checked.
- **Sessions and cookies.** `HttpOnly`, `SameSite=Lax`, `Secure` unless the base URL is `http://`. Session and invitation tokens are 32 random bytes; the database keeps only their SHA-256. Login makes a new token every time (no fixation); logout deletes the session on the server; disabling a user deletes their sessions.
- **Cross-origin protection.** `http.CrossOriginProtection` wraps every route: a state-changing request that the browser marks cross-site, or whose `Origin` is not the host, is a 403, for forms, Datastar requests and multipart uploads alike.
- **Headers** of every answer, including errors and streams.
- **Uploads.** Limits are enforced while the file is read; names are cleaned; storage keys are random 64-character hex strings and nothing else can name a file; only PNG, JPEG, GIF and WebP are served inline, everything else is an attachment of type `application/octet-stream`; temporary files are removed on every failure and when the server starts.
- **Output.** Every `templ.Raw` and `template.HTML` (listed below); markdown in goldmark's safe mode; search snippets; mention links; message bodies; every `data-*` expression that holds a value.
- **Authentication.** The login limiter, argon2id, invitations, sign-up, password lengths.
- **Input limits, the server's timeouts, shutdown, the log, and the dependencies.**

### Raw HTML

Every place that marks text as HTML, and why it is safe:

| Where | What | Why it is safe |
|---|---|---|
| `internal/markdown` (`template.HTML` in `convert` and `renderText`) | the body of a post, comment or message | the only package that makes HTML from user text: goldmark without `html.WithUnsafe` (raw HTML becomes a comment), only `http`, `https` and `mailto` links, only `http` and `https` images, every attribute escaped; text bodies are escaped as a whole. Tests: `TestRenderMarkdownInjection`, `TestRenderTextInjection`, `TestRenderMarkdownLinkAttributes` |
| `views/posts.templ` (`d.HTML`, `d.Post.HTML`) | the rendered body of a post and of the preview | the output of `internal/markdown` |
| `views/messages.templ` (`d.HTML`) | the body of a message | the output of `internal/markdown`, as text |
| `views/search.templ` (`r.Snippet`) | the match in a search result | `snippetHTML` escapes the whole snippet and then turns the two private-use marker characters of the index (which the index drops from user text) into `<mark>` and `</mark>` |

### Reviewed, with no finding

| Area | Result | Tests |
|---|---|---|
| Routes without a session | all redirect to the login page, by Datastar too | `TestProtectedRoutesNeedASession`, `TestPostRoutesNeedASession`, `TestAdminPagesNeedASession` |
| Private rooms and conversations | 404 for non-members and admins on every route that takes the ID of something in them (45 requests, 42 for an admin), nothing changed | `TestPrivateDataIsNotFoundToNonMembers` (new), `TestPrivateRoomPostsAreNotFound`, `TestStreamsRefuseWhoCannotRead` |
| Admin pages | 404 for members, nothing changed | `TestAdminRoutesRefuseMembers` (new), `TestAdminPagesAreInvisibleToMembers` |
| Objects of another parent (IDOR) | refused: a card or column of another room under this room's board, a file of another post in an edit, a reply to a message of another conversation | `TestObjectsOfAnotherParentAreRefused`, `TestEditCannotKeepTheFileOfAnotherPost`, `TestMessageCannotReplyToAnotherConversation` (new) |
| Cross-origin | every one of the POST routes answers 403 to a cross-site or other-origin request (urlencoded and multipart) | `TestEveryPostRouteRefusesCrossOriginRequests` (new), `TestCrossOriginPostsAreRefused` |
| Body sizes | every POST route reads at most 64 KiB (512 KiB for the editors of posts and messages) of a body of 12 MiB; uploads are capped at the files and a megabyte, and a file is cut while it is read | `TestEveryPostRouteCapsItsBody` (new), `TestUploadRequestIsCapped`, `TestUploadTooLarge`, `TestSaveLimit` |
| Uploads | names cleaned, keys random, temporary files removed, only the four image types inline with `nosniff`, a sandboxing policy and `attachment` for the rest | `TestDownloadHeaders`, `TestDownloadFileNames`, `TestUploadRefusedCallsLeaveNoFiles`, `TestBadKeys`, `TestSaveFailures` |
| Markdown | `javascript:`, `vbscript:`, `data:` and `file:` links and images, raw HTML, event attributes and every mix of case and white space are neutralised | `TestRenderMarkdownInjection` |
| Text of people on the pages | room, user, tag, file, column, task and conversation names, bodies in both formats and messages holding `'`, `"`, `\`, `</script>` and Datastar syntax are escaped on 19 pages (as full pages and as Datastar patches); none reaches a `data-*` expression, an inline handler or a `style` attribute | `TestUserTextIsEscapedEverywhere` (new) |
| Passwords | argon2id, 64 MiB, 3 iterations, 2 threads, a random 16-byte salt, 32-byte keys; stored hashes are checked against limits before they are verified; a password is 12 to 1024 bytes; an unknown username costs the same time as a wrong password | `TestDefaultHasherParameters`, `TestVerifyMalformed`, `TestSignUpValidation`, `TestLoginFailureMessagesAreTheSame` |
| Invitations | 32 random bytes, stored hashed, used once (the update is conditional, so two sign-ups with one token cannot both win), expire; a username is lower-cased before it is checked, so `ALICE` cannot take `alice` | `TestInvitationValid`, `TestSignUpWithABadInvitation`, `TestSignUpValidation` |
| Login limiter | 5 failures per username and 5 per address in 15 minutes, in memory, the address is the connection's unless `-trust-proxy` (off by default) says to read the last `X-Forwarded-For` entry; keys are cut at 128 bytes and swept | `TestLoginLimiter`, `TestLoginRateLimit`, `TestClientIP` |
| Cookies | `HttpOnly`, `SameSite=Lax`, `Secure` for an https base URL, cleared when stale; a new token on every login; logout deletes the session | `TestLoginSuccess`, `TestLogout`, `TestProtectedRoutesNeedASession` |
| Pagination | feed and message cursors are IDs inside one room or conversation; message pages are at most 100; the audit page is capped at 1,000,000; the search page (see S7) | `TestFeedPages`, `TestAuditEventsPageNumbers` |
| Search | no FTS5 syntax is passed on (words and phrases are quoted), at most 500 characters and 32 terms | `TestParseQuery` |
| Open redirect | `next` is a path of this site | `TestLoginNextValidation` |
| Logs | passwords, tokens, bodies and query strings are never logged; GORM's slow-query log has no arguments | `TestRequestLog`, the store's logger tests |
| Shutdown | the server stops accepting, ends the streams, waits 10 s and closes | `TestServerShutdownEndsOpenStreams` |

## Findings

| # | Severity | What | Fix | Test |
|---|---|---|---|---|
| S1 | High | **Markup injection in the redirect after login.** The `next` of the login is a path of this site, but `safeNext` let `<`, `>`, quotes and spaces through, and the Datastar SDK writes a redirect into an inline `<script>`. A link `/login?next=/</script><img src=x onerror=…>` ends the script and puts the rest in the page as HTML when the person logs in through it: a script that runs in the logged-in session | `safeNext` refuses markup characters, quotes, backtick, space and `{}\|^`; a redirect and a change of the address bar are now a hidden element with a `data-init` expression holding one JavaScript string literal (`jsString` writes `<` and `@` as `\u` escapes, and the attribute is HTML-escaped), never an inline script | `TestRedirectCannotInjectMarkup`, `TestRedirectJSIsOneStringLiteral`, `TestSafeNextRefusesMarkupCharacters`, `TestLoginNextValidation` |
| S2 | Medium | **No security headers.** Only downloads had `nosniff` and a policy. Pages could be framed (clickjacking), had no Content-Security-Policy as a second line against S1-like bugs, sent the `Referer` (an invitation link) to other sites and were cacheable | every answer, error pages, redirects and streams included, has `Content-Security-Policy` (D36), `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin`, `X-Frame-Options: DENY`, `Cross-Origin-Opener-Policy` and `Cross-Origin-Resource-Policy: same-origin` and `Cache-Control: no-store`; `Strict-Transport-Security` for an https base URL. The SDK's `no-cache` on event streams becomes `no-store`. The browser tests run unchanged under the policy | `TestSecurityHeaders`, `TestSecurityHeadersOnStreams`, `TestSecurityHeadersKeepTheOwnOnesOfStaticFilesAndDownloads`, `TestHSTSOnlyForHTTPS`, and every browser test |
| S3 | Medium | **An account that is disabled, demoted or logged out kept its streams for up to 25 seconds**, and during that time received the patches of every change in its rooms, as the actor it had when the stream opened (a demoted admin kept the admin role) | disabling, a change of role and a logout publish `user.changed` on the user's topic; every stream reads the session again at once: a session that is gone gets the redirect to the login page and the stream ends, a changed role ends the stream so that the page opens it again with the new one (D37) | `TestStreamsEndAtOnceWhenTheAccountChanges` (15 cases: room, thread, board, conversation and `/me/stream`, each disabled, logged out and made admin), `TestAccountEventLeavesAValidSessionAlone`, `TestAccountChangesPublishUserChanged` |
| S4 | Medium | **Unbounded concurrent password hashing.** Each login or sign-up hashes with 64 MiB, and the limiter counts a failure only after the hash, so many parallel requests, from one address or many, all got through the check and could use gigabytes of memory | at most twice the number of CPUs (2 to 8) passwords are hashed or checked at once, the others wait for their turn or for the end of their request | `TestHashingWaitsForItsTurn`, `TestHashingWaitEndsWithTheRequest` (route and tag `HashWaitCanceled`) |
| S5 | Low | **Server timeouts.** Only `ReadHeaderTimeout` was set: an idle connection stayed for ever, a client could send the headers of a form and stop (holding a goroutine), and a client that stopped reading an event stream held its handler in a blocked write | `IdleTimeout` 2 minutes, `MaxHeaderBytes` 64 KiB; a deadline for the body (1 minute for a form, 15 for an upload) that is lifted when the body ends; a write deadline of 30 seconds before each write to a stream. There is no `ReadTimeout` or `WriteTimeout`, which would cut the streams. A form that times out is answered with the 400 page | `TestHTTPServerTimeouts`, `TestSlowBodyIsCut`, `TestBodyDeadlineIsLiftedWhenTheBodyIsRead`, `TestStreamWritesHaveADeadline` |
| S6 | Low | **The invitation token reached the log.** A request that no route matched is logged with its path, and `PUT /invite/<token>` or `GET /invite/<token>/x` is such a request. A path of any length was logged | the token of `/invite/…` is logged as `/invite/{token}`, and a logged path is cut at 200 bytes | `TestRequestLogHidesInvitationTokens`, `TestRequestLog` |
| S7 | Low | **A search page without a bound.** `?page=9223372036854775807` asked the index to skip that many rows, and the offset overflowed | a page after the 1000th is empty (D37) | `TestSearchPageIsBounded` |
| S8 | Low | **The database file was readable by everyone** (mode 0644; only the data directory's 0750 kept others out, and a directory made by someone else, such as a mounted volume, may be open). It holds the password hashes | the file is made with 0600 and an older one is changed to it, which also sets the mode of the `-wal` and `-shm` files | `TestOpenRestrictsTheDatabaseFile` |

S1 needed a victim who logs in through a link the attacker made; S3 let a user whom an admin had just disabled go on reading for up to one ping interval. S2 and S4 to S8 are hardening against the bugs that come after them (S2 is the second line against S1).

## Accepted risks and things to know

- **`'unsafe-eval'` in the policy.** Datastar compiles the `data-*` expressions of the page with `Function`. The policy still refuses every inline script and every script from another site, which is what an injected element needs.
- **Images from other sites.** A markdown body may embed an image from any `http` or `https` address (SPEC §7.3), so the policy allows such images, and the author of a post can see the address of whoever opens it (no `Referer` is sent). Turn it off by changing `img-src` in `internal/web/security.go`.
- **A user can lock a username out.** Five failed logins for a username block that username for 15 minutes from every address (SPEC §7.1). The address limit stops one client from trying many usernames. The limiter is in memory: a restart forgets it.
- **`-trust-proxy`.** Behind a proxy, the address limit works only with `-trust-proxy` and only when every request comes through the proxy. Without it every client has the proxy's address.
- **Opening a conversation or the notifications page marks them read** (SPEC D33, §7.9). A cross-site link to one marks it read for a logged-in member. It changes nothing else, and no other `GET` route changes state.
- **Downloads are cached by the browser of the reader for a day** (`private, max-age=86400`). A member who was removed from a room can still open a file they had opened in the same browser, until it expires.
- **Admins** cannot read private rooms or conversations, but can rename and delete any room and remove any member (D29), and see names and summaries in the audit log.
- **Names are public inside the installation.** Every user can list every enabled user (to add them to a room or a conversation), so usernames are not secret.
- **Attachments are not scanned.** A file is stored as it came. It is served as a download with `nosniff` and a sandboxing policy, and inline only when its content is a valid PNG, JPEG, GIF or WebP.
- **One process per database**, and a full disk or a copy of the data directory is outside what the program can protect.

## Out of scope

- **TLS.** Clowder speaks plain HTTP. The reverse proxy terminates TLS and must pass the `Host` header (the README has examples for Caddy and nginx). Without TLS a password and a session cookie cross the network in clear text.
- **Email.** There is none: invitation links are handed over by an admin, and there is no password reset. A person who forgets their password needs an admin to disable the account and invite them again.
- **Two-factor authentication, single sign-on and passkeys** (SPEC §2).
- **End-to-end encryption.** The server can read every message, and so can whoever has the data directory.
- **Denial of service of the machine.** Limits on request bodies, headers, forms, passwords, hashes and streams are set, but a flood of requests is for the proxy or the firewall.
- **The host.** File permissions of the data directory beyond the program's own, backups and who has a shell on the machine.
- **Hostile administrators.** An admin is trusted with the installation.

## Dependencies

`govulncheck` is not installed and was not run. These are the versions of the modules that go into the binary (`go list -deps ./cmd/clowder`), with the parts of `go.mod` that matter. Look them up in the Go vulnerability database before a release.

| Module | Version |
|---|---|
| `github.com/a-h/templ` | v0.3.1100 |
| `github.com/glebarez/sqlite` | v1.11.0 |
| `github.com/glebarez/go-sqlite` | v1.21.2 |
| `modernc.org/sqlite` | v1.23.1 (SQLite 3.41; the version of the pure-Go driver, the oldest part of the chain) |
| `modernc.org/libc`, `modernc.org/mathutil`, `modernc.org/memory` | v1.22.5, v1.5.0, v1.5.0 |
| `gorm.io/gorm` | v1.31.2 |
| `github.com/starfederation/datastar-go` | v1.2.2 (the embedded `datastar.js` is v1.0.2, pinned with it, D7) |
| `github.com/yuin/goldmark` | v1.8.6 |
| `golang.org/x/crypto` | v0.58.0 |
| `golang.org/x/sys`, `golang.org/x/text` | v0.49.0, v0.43.0 |
| `github.com/rm4n0s/errors` | v0.2.0 |
| `github.com/CAFxX/httpcompression`, `github.com/andybalholm/brotli`, `github.com/klauspost/compress` | v0.0.9, v1.2.7, v1.18.0 (through `datastar-go`) |
| `github.com/dustin/go-humanize`, `github.com/google/uuid`, `github.com/jinzhu/inflection`, `github.com/jinzhu/now`, `github.com/mattn/go-isatty`, `github.com/remyoudompheng/bigfft`, `github.com/valyala/bytebufferpool` | v1.0.1, v1.3.0, v1.0.0, v1.1.5, v0.0.24, 2023-01-29, v1.0.0 |

Go itself is 1.27.3 on the machine that built this: the standard library provides the HTTP server, `CrossOriginProtection` and `crypto/rand`. The embedded SQLite is old; updating `github.com/glebarez/sqlite` when a newer release is out is the most useful single update.

`go list -m all` also names modules of the build graph that are not in the binary (`github.com/mattn/go-sqlite3`, which needs cgo, among them); `CGO_ENABLED=0 go build ./...` builds without them.

---

# Clowder 1.1

The review of step 17.1 (2026-10-27) covers everything 1.1 added: the REST API (`internal/api`, 63 routes) and its WebSocket, the bearer tokens, PostgreSQL, Redis and the several-processes mode (`internal/store`, `internal/cluster`), the upload staging, the Docker image, `compose.yaml`, `compose.cluster.yaml` and the Caddyfile. It was done the way the first one was: every route and every service method it reaches was read, the claims below were turned into tests wherever a test can state them, and the tests were run on PostgreSQL and Redis too (`scripts/test-docker.sh`).

## What was reviewed

- **Every API route and the WebSocket.** Each handler takes the acting user from the bearer token (`authenticated`, which wraps every route but `login`, `signup`, `invitations/{token}`, `openapi.json` and the catch-all), calls one service method that checks the permission itself, and has no other way to the data. `TestEverySecuredRouteIsListed` fails when a route that needs a login is missing from the table, and `TestWhoMayCall` sends every one of the routes with no header, an empty bearer, another scheme, an unknown token, an expired one, a browser session's token, the session cookie alone, a disabled user, a member and an admin. A private room, a post, board, file or member list in it, and a private conversation, its messages and files are `404` to non-members, admins included; the admin routes are `403` to members. IDs in paths, queries and bodies that name something of another parent were checked (the upload of another user, an attachment of another post kept on an edit, a task of another post in an edit, a reply to a message of another conversation, a column or card of another room): each is refused by the service, with the same tests as the pages (`TestUploadsOfAnotherUserAreRefused`, `TestPatchPostMistakes`, `TestMessageRoutesOfTheErrors`, `TestRoutesOfTheErrorsOfTheBoard`).
- **Mass assignment.** Every body is decoded with unknown fields refused (`422`, the field named), and no body struct has a field for something the user must not set (no role at sign-up, no owner, no visibility on a room patch). `TestRequestSchemasHaveTheFieldsOfTheStructs` ties the structs to the document, which says `additionalProperties: false`.
- **Limits and numbers.** A JSON body is at most 64 KiB (`http.MaxBytesReader`), the feed, notification and message limits are 1 to 100, positions are clamped by the service, a search page after the 1000th is empty, an audit page is capped, topics are at most 200 per WebSocket and a WebSocket message at most 16 KiB. Deep nesting, numbers out of range, NUL and invalid UTF-8 are `422`, never a panic or a 500 (`TestHostileJSONBodies`). An ID above the range of the databases was a finding (S9).
- **Tokens.** 32 random bytes (43 characters of base64url) from `crypto/rand`; the database keeps only the SHA-256, and the lookup is by that hash (a lookup in a unique index, so no secret is compared byte by byte); a login makes a new token every time; the kind of the session is checked on both sides (the cookie of a page is not a bearer token, a bearer token is not read from a cookie, `TestWhoMayCall`, `TestAPIIgnoresThePageCookie`); the lifetime is the configured one from the last use; a disabled user, a logout, ending a session from the list and an expiry end it (`TestAnExpiredTokenIsDeleted`, `TestEndSession`, `TestLogout`, `TestAdminPatchUser`); a role change reads the role again with the next request and closes the sockets (`TestWSSessionChanges`). A token is never logged: the request log has the route pattern, the status and the user ID, never the header, the path of an unmatched request is cut and the invitation token is replaced; error metadata holds no token; the WebSocket never takes one in the URL. `TestNoSecretsInTheLog`, `TestNoTokenInAnyAnswerOrLog` and `TestWSLogs` search the log and the answers for the token, the password and the header after the routes were used, also the failing ones.
- **The login limiter.** `/auth/login` counts failures per username and per address with the limiter of the pages (5 in 15 minutes; in Redis when a Redis is set; `TestLoginLimiter`, `TestAPILoginUsesTheClientAddress`, `TestLoginCounterOnRedis`); the answer is `429` with `Retry-After: 900`. `/auth/signup` and `GET /auth/invitations/{token}` are not limited: see the accepted risks.
- **JSON answers and headers.** Every answer of the API goes through the same `secureHeaders` as the pages (D36): `Content-Security-Policy`, `nosniff`, `Referrer-Policy`, `X-Frame-Options`, `Cross-Origin-*`, `Cache-Control: no-store` (and HSTS for an https base URL), the JSON content type has a charset, there are no CORS headers at all (an `OPTIONS` request is a `405` without `Access-Control-*`), and the cross-origin protection of the pages covers the API's unsafe methods, so a web page of another site cannot send a state-changing request even with a token (`TestAPIThroughTheServer`, `TestAPIPostsAreProtectedAgainstOtherSites`). Errors never repeat the input; a `500` says nothing about the cause and the cause is logged with its route (`TestInternalErrorIsLoggedAndHidden`, `TestPanicIsAJSON500`).
- **Uploads.** The size limit is enforced while the file is read (`TestUploadBodyIsCutEarly`), the form must hold the one file and nothing else, a file stored before the rest of the form was found wrong is removed again, names are cleaned and storage keys are 64 random hex characters (a key from a client is checked with `validKey` before the disk is touched: `TestBadKeys`, `TestNotesRefuseBadKeys`, so `../` and absolute paths never reach a path), the note that binds a staged upload to its owner is written whole and renamed, an upload is attached once and only by its owner (`TestUploadsOfAnotherUserAreRefused`, `TestUploadIDsThatAreNotUploads`), the sweep removes what no post took after a day and a download has the headers of the pages' download (`nosniff`, a sandboxing policy, only four image types inline: `TestDownloadHeaders`).
- **The WebSocket.** The token is checked before the upgrade; the library's origin check is on (`TestWSHandshake` sends another site and gets `403`); compression is not negotiated (`TestWSDoesNotNegotiateCompression`), so a message cannot be a bomb inside the 16 KiB read limit; a message over it, a binary frame and text that is not one JSON value close the connection (`TestWSMessagesThatCloseTheConnection`); topics are checked on subscribe and again for every event, for the user (`TestWSEventsReachOnlyTheSubscribers`, `TestWSLosingAccess`), the sockets end at once on logout, disable and role change and at the next ping when the session expired (`TestWSSessionChanges`, `TestWSExpiredSessionEndsAtThePing`); a slow client gets `resync` and is closed instead of blocking the hub (`TestWSOverflowSendsResync`); a client that does not answer pings is closed (`TestWSClientThatDoesNotAnswerPingsIsClosed`); the server closes every socket with 1001 on shutdown and waits for them (`TestWSServerShutdown`).
- **PostgreSQL and Redis.** Every statement with a value uses a parameter (`grep` of `Raw(`, `Exec(` and the builders of the search: the only text concatenated into SQL are table and column names that are constants of the code and `quoteIdent` of the model's own names; the search query is passed as a parameter to `to_tsquery` after it was made safe, and `TestSearchNeverFailsOnOddQueries` throws the syntax of FTS5, `tsquery`, `LIKE` and SQL at it on both databases). The URL of the database and of Redis is shown only with its password replaced by `xxxxx` (`config.RedactURL`, used by every log and every error metadata), an unparseable URL is replaced altogether, and the error of the driver for a URL that cannot be parsed is dropped because it quotes the URL (`TestOpenPostgresFailures` in `internal/store/postgres_test.go`, `TestOpenStoreLogsTheURLWithoutItsPassword`, the Redis tests of `internal/cluster`). A usage text that printed it was S10. The events on Redis hold a topic and IDs only, never text (`wireEvent`), the keys of the login limiter hold a hash of the username or address, never the text (`TestCounterKeyIsSafe`), a message on the channel that is not an event is dropped and logged without its content (`TestMalformedMessagesAreSkippedAndLogged`).
- **The database user's rights** were tried with real roles (a superuser, the owner of the database, a role with `CREATE` on the database and the schema, and a role with nothing): see the README, "PostgreSQL without Docker". The owner and the role with the two grants migrate and run; the role with nothing fails to migrate with "permission denied for schema public". The Redis user's commands were tried the same way (README, "Redis").
- **Docker.** See "The Docker files" below.

### Reviewed, with no finding

| Area | Result | Tests |
|---|---|---|
| Authentication of every route | one wrapper, `authenticated`, for every route but five; the table of who may call is complete | `TestEverySecuredRouteIsListed`, `TestWhoMayCall` |
| Sessions of the two kinds | a cookie is not a token and a token is not a cookie, on the API, the pages and the WebSocket | `TestWhoMayCall`, `TestAPIIgnoresThePageCookie`, `TestWSHandshake` |
| Private rooms and conversations over the API | `404` for non-members and admins; the admin routes `403` for members | `TestFeedPermissions`, `TestPrivateBoardIsNotFound`, `TestConversationsArePrivate`, `TestDownloadPermissions`, `TestAdminRoutesAreForAdmins`, `TestWSEventsReachOnlyTheSubscribers` |
| Objects of another parent | refused | `TestUploadsOfAnotherUserAreRefused`, `TestPatchPostMistakes`, `TestRoutesOfTheErrorsOfTheBoard`, `TestMessageRoutesOfTheErrors` |
| Unknown fields, wrong types, deep nesting, huge numbers | `422`, nothing logged as a failure | `TestHostileJSONBodies`, `TestPostMistakes` |
| Body and message sizes | 64 KiB for JSON (`413`), the file size for an upload, 16 KiB for a WebSocket message | `TestHostileJSONBodies`, `TestUploadMistakes`, `TestWSMessagesThatCloseTheConnection` |
| Cross-site use of the API | the cross-origin protection answers `403`; there are no CORS headers | `TestAPIPostsAreProtectedAgainstOtherSites`, `TestAPIThroughTheServer` |
| Slow bodies of the API | the deadline of a form (1 minute) for JSON and of an upload (15 minutes) for a multipart body | `TestAPIUploadHasTheUploadDeadline` |
| Tokens in logs | none, in the request log, the error metadata, the WebSocket log and the output of `clowder-apicheck` | `TestNoSecretsInTheLog`, `TestNoTokenInAnyAnswerOrLog`, `TestWSLogs`, `TestTheTokenIsNeverPrinted` |
| Passwords of the database and of Redis in logs and errors | none | `TestOpenStoreLogsTheURLWithoutItsPassword`, `TestOpenPostgresFailures`, `TestRedactURL`, `TestNewLoginCounterFailures`, `TestNewBridgeFailures` |
| SQL | parameters only | `TestSearchNeverFailsOnOddQueries`, `TestParseQuery`, `TestUsersTreatWildcardsAsPlainCharacters` |
| Upload staging | owner-bound, used once, swept after a day, keys checked before any path is made | `TestUploadsOfAnotherUserAreRefused`, `TestStagedSweeperSweepsAtStartAndEveryInterval`, `TestBadKeys` |
| The WebSocket | origin, no compression, read limit, topic limits, access on every event, session changes, slow clients, shutdown | `TestWS...` (all) |
| The upgrade of a 1.0 database | the sessions that were live stay and become browser sessions; the API is on | `TestOpenUpgradesTheSessionsOfA10Database`, and by hand with the binary of `v1.0.0` (below) |

## Findings

| # | Severity | What | Fix | Test |
|---|---|---|---|---|
| S9 | Medium | **An ID above 2^63-1 was a 500 and an error in the log.** Both databases keep an ID in a signed 64-bit column, and the driver refuses a larger number as a query argument. The pages and the API parsed an ID with `ParseUint(…, 10, 64)`, which accepts up to 2^64-1, so `GET /posts/9223372036854775808`, `?before=…`, a form field, or a number in a JSON body reached the driver: 43 request shapes of the pages and 26 of the 28 the API test sends (a path, a query or a body) answered a 500 and logged the error with its route. No data was exposed and nothing was written, but anyone who can log in could fill the log with failures and make the 500 rate of a monitor meaningless | every ID is parsed with 63 bits (`ParseUint(…, 10, 63)`: `pathID`, `before`, the audit filters, the WebSocket topics, and every ID of the pages), so such a number is "not found" in a path and "not a valid number" in a query; the JSON body is checked after it is decoded (`oversizedID`), and a number above the range in any ID field is `422` with the field named | `TestIDsAboveTheDatabaseRangeNeverFail` (28 requests × 3 numbers on the API), `TestOversizedIDInABodyNamesTheField`, `TestTheLargestIDOfTheDatabaseIsAnOrdinaryMissingID`, `TestIDsAboveTheDatabaseRangeAreNotFoundOnThePages` (every route of the pages), on SQLite and PostgreSQL |
| S10 | Medium | **`clowder serve -h`, a mistyped flag, and the same for `invite` and `demo`, printed the database URL and the Redis URL with their passwords.** The flag package prints the default of each flag in the usage text, and the default of `-database-url` and `-redis-url` was the value of `CLOWDER_DATABASE_URL` and `CLOWDER_REDIS_URL`. In a container the usage goes to the log of the service: a typo in a flag put the password of the database in the logs of the host | the environment is no longer the default of these flags: the flag has an empty default, and the setting is the flag if it was given (an empty one turns the environment off), else the variable, else the default (`urlFlag`); the precedence is the same as before | `TestUsageDoesNotPrintTheURLsOfTheEnvironment` (serve, serve with a mistake, invite, demo), `TestURLFlagsKeepTheirPrecedence`, `TestInviteAndDemoReadTheDatabaseURLFromTheEnvironment` |
| S11 | Low | **The build context of the Docker image was not limited enough.** `.dockerignore` left out `.git`, `.env`, `data/` and the proofs, but not `.env.*` (a `.env.production` or `.env.local`), database files (`*.db`, `*-wal`, `*-shm`) next to the source, or `.tmp/`, where the tests of a developer leave their data. The final image copies nothing but the binary and an empty directory from the build stage, so none of these reached the image or its layers, but they were sent to the Docker daemon and sat in the build stage's layers on the machine that builds | all of them are ignored (`.env.*` with `!.env.example`) | `TestDockerignoreKeepsSecretsAndDataOutOfTheBuild` |

S9 was reachable by every logged-in user and by nobody else; S10 needs a person who can start the program or read the logs of the service, and gave them a password they are likely to have anyway; S11 is a leak to the build machine only.

## The Docker files

| Item | Result |
|---|---|
| Who the container runs as | the distroless `static-debian13:nonroot` image, `USER nonroot:nonroot` (65532), no shell, no package manager; the data directory is made in the build stage and copied with that owner. `TestDockerfileKeepsTheImageSmallAndUnprivileged` |
| Secrets in the image | the `Dockerfile` has no `ARG` or `ENV` for a password, a token or a URL (only `CLOWDER_DATA` and `CLOWDER_ADDR`), no `ADD`, and the final stage copies only `--from=build`. `docker history --no-trunc` of the built image shows the layers of the distroless base, `COPY` of the binary and of the empty `/data`, `ENV CLOWDER_DATA=/data CLOWDER_ADDR=:8080`, `VOLUME`, `EXPOSE`, `USER nonroot:nonroot`, `HEALTHCHECK` and `ENTRYPOINT`: no secret, no path of the build machine (the binary is built with `-trimpath`: it holds no `/home/…`); `docker image inspect` shows the user `nonroot:nonroot` and those two variables only |
| The build context | S11. The binary is built with `-trimpath -ldflags="-s -w"`, so no path of the build machine is in it |
| The health check | `clowder healthcheck` asks `GET /healthz` of the container itself (`127.0.0.1`); the answer is `ok` or `unavailable` and nothing else, not even the error (D40); it needs no login and is logged at debug level when it succeeds |
| What compose publishes | `compose.yaml`: only Clowder, on `CLOWDER_PORT` on every interface (the README shows how to bind it to the loopback address). `compose.cluster.yaml`: only Caddy. PostgreSQL, Redis and the Clowder processes are reachable inside the compose network only. `TestComposePublishesOnlyWhatItShould` |
| The database password | required (`${CLOWDER_DB_PASSWORD:?…}`), never written in a compose file; `.env` is ignored by git and by Docker. It is in the environment of the Clowder container (visible to anyone who can use Docker on the machine, which is root-equivalent) and the example in `.env.example` is a placeholder the README tells the operator to change |
| Redis | no password and no published port: reachable only by the containers of the composition, which is the whole trust boundary. The README tells how to give it a password, ACL and TLS for any other setup |
| Caddy | `admin off` (no admin API on the network), `auto_https off` (plain HTTP behind the operator's TLS), round robin with health checks. Caddy replaces any `X-Forwarded-For` of the client with the address it saw, and Clowder reads the **last** entry of the header, so a forged value in front of it does not count. `CLOWDER_TRUST_PROXY=true` is set only in `compose.cluster.yaml`, where Caddy is the only way in, and not in `compose.yaml`. `TestCaddyfileOfTheClusterIsClosed` |
| Image tags | `golang:1.27`, `postgres:18`, `redis:8`, `caddy:2` and the distroless image are tags, not digests, so a rebuild may take a newer minor version: accepted (below) |

## Accepted risks and things to know (1.1)

- **Sign-up and the invitation check are not rate limited.** `POST /auth/signup` and `GET /auth/invitations/{token}` can be called with a wrong invitation token as often as the network allows. An invitation token is 32 random bytes (256 bits) looked up by its SHA-256, so guessing one is hopeless however many tries there are, and a wrong token costs one indexed query and no password hashing (the invitation is checked before the hash), so it cannot exhaust the hashing slots either. The same is true of the sign-up page since 1.0. A flood is for the reverse proxy.
- **No limit on the number of WebSockets or of open streams per user.** A logged-in user can open as many as the process allows file descriptors for; each costs a few goroutines and a queue of 64 events. The same is true of the pages' streams since 1.0 ("Denial of service of the machine" is out of scope). A cap per user (for example 20) would be a visible change of behaviour; it is recorded as a recommendation for a later release, not made here.
- **No limit on what a user has staged.** A member can upload files of the size limit without a post taking them; the sweep removes them after a day. Within that day the disk can be filled by a member as fast as the network allows (a post can carry the same files for good, so the cap would not be new). Recommendation: a cap on the staged bytes per user.
- **A client made of a web view cannot call the API.** The cross-origin protection of the pages also covers the API (D43 said it was not needed): a state-changing request with an `Origin` other than the host is `403`. This is a safety property; a desktop client must send the request from its native HTTP stack. `docs/API.md` says so.
- **A download is cached for a day by the client** (`private, max-age=86400`), as on the pages: a member removed from a room can open a file they already have until it expires.
- **API sessions are not counted.** Every login makes a session row; a user who logs in in a loop makes many. They expire with the rest, the user's own list shows them, and the hashing of every login is bounded (S4), so the rate is low.
- **The `html` of the answers is the server's output, to be trusted as the pages trust it.** A client that renders it must apply its own policy as well (`docs/API.md`).
- **Redis without a password inside the compose network.** Anyone who gets a connection to that Redis could publish events (forcing page reloads, closing sockets with `user.changed`) and clear the login counters. The events carry no text. The README says to give any other Redis a password, ACL and TLS.
- **Mutable image tags** (above) and no scan of the image: the operator pins digests if they need to.
- **`sslmode`.** The driver's default is `prefer`; the README says to use `verify-full` or `require` beyond a private network, and compose uses `disable` inside its own network.
- **`/healthz` and `/api/v1/openapi.json` need no login.** The first says `ok` or `unavailable`; the second describes the API, which is open source.

## Out of scope (1.1)

- **TLS to PostgreSQL and Redis**, their passwords and their network access: the operator's (the README says how).
- **Backups and restores** of PostgreSQL, Redis and the attachments directory.
- **The security of a desktop client**: where it keeps the token, what it does with `html` and downloads (`docs/API.md` has advice).
- **A shared file system** for the attachments of several processes: its permissions and availability.

## Dependencies (1.1)

New modules in the binary since 1.0 (`go list -deps ./cmd/clowder`), to be looked up in the Go vulnerability database before a release (`govulncheck` is not installed and was not run):

| Module | Version |
|---|---|
| `github.com/coder/websocket` | v1.8.15 |
| `gorm.io/driver/postgres` | v1.6.3 |
| `github.com/jackc/pgx/v5` and its `pgpassfile`, `pgservicefile`, `puddle/v2` | v5.10.0, v1.0.0, 2024-06-06, v2.2.2 |
| `github.com/redis/go-redis/v9` and `github.com/cespare/xxhash/v2`, `go.uber.org/atomic` | v9.23.0, v2.3.0, v1.12.0 |
| `golang.org/x/sync` | v0.24.0 |

The versions of the first table are the same except `golang.org/x/text` (v0.43.0) and `github.com/google/uuid` (v1.3.0).
