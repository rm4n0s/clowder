# Security

This file records the security review of Clowder 1.0 (step 13.2, 2026-10-27): what was reviewed, what was found and fixed, what is accepted, what is out of scope, and how to report a vulnerability. The decisions it led to are D36 and D37 in `docs/SPEC.md`.

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
