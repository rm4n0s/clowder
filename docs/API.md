# The Clowder API

This is the guide for people who write a client for Clowder, such as a desktop application. It explains how to log in, what an answer and an error look like, how the lists are paged, how files go up and down, and how to keep a client live with the WebSocket. The exact paths, parameters and fields of every route are in the OpenAPI document, which this guide does not repeat: [`internal/api/openapi.json`](../internal/api/openapi.json), served by every installation at `/api/v1/openapi.json`. The rules behind the API are in [`SPEC.md`](SPEC.md) §7.12 and §7.13 (decisions D42 to D45).

- [What the API is for](#what-the-api-is-for)
- [Base URL, versions and conventions](#base-url-versions-and-conventions)
- [Logging in](#logging-in)
- [Errors](#errors)
- [Paging](#paging)
- [Changing things: POST, PATCH, PUT and DELETE](#changing-things-post-patch-put-and-delete)
- [Files: uploads and downloads](#files-uploads-and-downloads)
- [Reactions](#reactions)
- [The WebSocket](#the-websocket)
- [An example session](#an-example-session)
- [The OpenAPI document and generated clients](#the-openapi-document-and-generated-clients)
- [Advice for client writers](#advice-for-client-writers)

## What the API is for

The API does everything a person does in the pages of Clowder, and an admin does in the admin pages: read and write posts, comments, reactions, tasks and the board, search, notifications, private messages, rooms, members, and the administration of users, invitations and the audit log. It is for **clients that act as one logged-in user**. It calls the same code as the pages, so every rule is the same: a private room or a private conversation is `404` to anyone who is not a member (admins included), a member cannot edit someone else's post, a file is downloaded only by people who can read its room, and so on. A client that is allowed to see a thing sees what the pages show; it never sees more.

It is not a way to run code on the server, and it has no "service account" or "all users" mode.

## Base URL, versions and conventions

- **Base URL.** `<the address of the installation>/api/v1`, for example `https://chat.example.org/api/v1`. Every path in this guide is relative to it. The version is in the path: a change that would break a client gets `/api/v2`; `/api/v1` only grows.
- **JSON.** Bodies of requests and answers are JSON in UTF-8. A request with a body must send `Content-Type: application/json` (`charset=utf-8` is allowed), or the answer is `415`. Names are in `snake_case`. Times are RFC 3339 in UTC (`2026-10-27T09:30:00Z`); a due date is a day, `YYYY-MM-DD`. IDs are positive whole numbers that start at 1 and count up, so a 64-bit integer holds them (and a JavaScript number, which is exact up to 2^53, holds any ID a real installation reaches).
- **Limits.** A JSON body is at most 64 KiB (`413 too_large` above it). A field that the route does not have is refused with `422` and its name under `fields`, so a typo is never silently ignored. A number that is too large to be an ID (above 2^63-1) is `404` in a path and `422` in a body.
- **Answers.** `200` for a read or a change that returns the resource, `201` for a create (with the resource, and a `Location` header for a room, a post, a comment, a column and a conversation), `204` with no body for a delete, a join, a leave, a move and similar. `HEAD` works wherever `GET` does.
- **No CORS.** The API sets no cross-origin headers and a browser page on another site cannot use it. The pages of the installation never accept a bearer token, and the API never reads the session cookie of the pages.
- **No `Origin` header.** The server refuses a state-changing request (`POST`, `PUT`, `PATCH`, `DELETE`) that the browser marks cross-site or whose `Origin` is not the host of the installation, with `403`. An HTTP library of a desktop application sends neither header and is not affected. An application that is a web view (Electron, Tauri) and calls the API with `fetch` from its own page sends an `Origin` and is refused: make the requests from the native side of the application instead.
- **Caching.** Answers carry `Cache-Control: no-store`, except file downloads (`private, max-age=86400`).
- **`html`.** A post, a comment and a message have the `body` as written and the `html` as the pages show it, so a client needs no markdown renderer. The server makes it safe (raw HTML in the text is not passed through; links are `http`, `https` or `mailto`; images are `http` or `https`). A client that puts it in a web view should still apply its own sanitizer or content security policy, and may want to block remote images, which tell their server who opens the post.

## Logging in

A client logs in with the username and password of a user and keeps the **token** that comes back. There is no OAuth and no API key: the token is a session, like the browser's, and the user can see and end it (below).

```
POST /auth/login
{"username": "alice", "password": "...", "client_name": "Clowder Desktop 1.0 on Alice's laptop"}

200
{"token": "mT3...", "expires_at": "2026-11-26T09:30:00Z", "user": {"id": 2, "username": "alice", "display_name": "Alice", "role": "member", "created_at": "..."}}
```

- `client_name` is required: 1 to 80 characters, no control characters. It is what the user sees in the list of their sessions, so make it say which application and which machine.
- Send the token with every other request as `Authorization: Bearer <token>`. The scheme is case-insensitive. A token is 43 characters of base64url; treat it as opaque.
- **Lifetime.** A token lasts 30 days from its last use by default (the operator can change it with `-session-lifetime`). Using it moves the end forward, at most once a minute, so a client that is used often stays logged in. `expires_at` is when the token ends if it is not used again; `GET /me/sessions` shows the current value.
- **Sign up.** `POST /auth/signup` with `{"invitation_token", "username", "display_name", "password", "client_name"}` does what the sign-up page does and logs the new user in: `201` with the same answer as a login. `GET /auth/invitations/{token}` tells whether the token of an invitation link can still be used (`{"valid": true, "role": "member", "expires_at": "..."}`, or `404` for a link that is unknown, used or expired; the answer does not say which). The invitation link is `<address>/invite/<token>`; the client takes the token from its end.
- **Who am I.** `GET /me` returns the user and the number of unread notifications and conversations.
- **Group mentions, keywords, following and presence (1.2, SPEC §7.15).** `GET /me/settings` and `PATCH /me/settings` also carry `keywords` (up to 10 strings of 1 to 32 characters, unique ignoring case; `[]` clears; a broken list is `422` with the field `keywords`) and `group_mentions` (whether `@room`, `@all` and `@here` reach the user). `GET /rooms/{id}/group-mention-count?body=&format=` and `GET /conversations/{id}/group-mention-count?body=` give `{"count": n}`, the number of people the group mentions of a body would notify; a client asks its own user "This will notify 14 people. Send anyway?" above 5, the API itself never asks. `PUT` and `DELETE /posts/{id}/follow` follow and unfollow a post (the root post of a comment) and answer `204` also when nothing changes; `GET /posts/{id}` has `following`. Notifications have the kinds `group_mention` and `keyword` besides the four of 1.0, and `others` (the people folded into a reply notification). `GET /me/presence?user_ids=1,2,3` (at most 100) gives `{"online": {"1": true}}` for the enabled users who have a page or a client connected in any process.
- **Profile, avatar, settings and password (1.2).** `PATCH /me` sets the display name. `GET /me/settings` and `PATCH /me/settings` read and change the theme (`system`, `light`, `dark`), the sound and the browser notifications; a setting that is missing or `null` in the body stays as it is. `PUT /me/avatar` takes a `multipart/form-data` body with one picture in the field `file` (PNG, JPEG or GIF, at most 2 MiB and 4096 by 4096 pixels; the server cuts the centre square and stores it as a PNG of 256 by 256) and returns the user; `DELETE /me/avatar` removes it. Every user object has `avatar_url`: `/api/v1/avatars/{id}?v=<key>` or `null`, a path to fetch with the bearer token (the picture is cached for a day, and a new picture has a new address). `POST /me/password` takes `current_password` and `new_password` (at least 12 characters, not the current one) and answers `204`: it ends every other session of the user, browsers and clients, and keeps the one that asked. A wrong `current_password` is `422` and counts as a failed login in the limiter. The WebSocket sends `user.updated` and `settings.changed` on `user:<id>` after these changes.
- **Sessions.** `GET /me/sessions` lists the live sessions of the user, browsers and clients, with the one of the request marked `current`. `DELETE /me/sessions/{id}` ends one of them (`404` for another user's). The page `/me/sessions` in the browser does the same. `POST /auth/logout` ends the session of the token (`204`). A client logs out when the user asks to, and forgets the token.
- **When the token stops working.** Any route answers `401` with `error.code` `unauthenticated` and `WWW-Authenticate: Bearer realm="clowder"` when the token is missing, unknown, expired, ended, of a browser session, or of a user who was disabled. The client cannot tell these apart and does not need to: it forgets the token and shows the login. A `401` from `/auth/login` itself means the username or the password is wrong (the answer is the same for an unknown user and for a wrong password).
- **Too many tries.** After 5 failed logins for a username, or 5 from one address, within 15 minutes, `/auth/login` answers `429 rate_limited` with `Retry-After: 900` (the whole window; the server does not know how much of it is left), even for the right password. Wait, and tell the user. Behind a reverse proxy the address is the client's only if the operator turned on `-trust-proxy`.
- **What ends a token early.** Disabling the user, logging out, and ending the session from the list. A change of the user's role keeps the token and changes what it may do from the next request; an open WebSocket is closed (below).

Store the token where the operating system keeps secrets (Keychain, the Windows credential store, the Secret Service), never in a plain configuration file, and never log it.

## Errors

Every error answer has this body (`Content-Type: application/json`):

```json
{"error": {"code": "invalid_input", "message": "Some of what you sent is not valid.", "fields": {"name": "Use 1 to 80 characters."}}}
```

`code` is for the program, `message` is fixed English for a human who reads a log, and `fields` (only for `invalid_input`) maps the name of a JSON field, or `body` for the request as a whole, to what is wrong with it. The messages inside `fields` are meant to be shown to the user next to the field. The names in `fields` are the API's own: `display_name`, `upload_ids`, `assignee_ids`, and so on.

| Status | `code` | When |
|---|---|---|
| 401 | `unauthenticated` | No usable token (see above), or a wrong username or password at login. Has `WWW-Authenticate`. |
| 403 | `forbidden` | You may see the thing but not do this to it: edit someone else's post, post in a room you have not joined, or any `/admin` route as a member. |
| 404 | `not_found` | The thing does not exist **or you may not know that it exists**: a private room, a private conversation, their posts, messages and files, for anyone who is not a member. Also a path the API does not have, and an ID that is not a number. |
| 405 | `method_not_allowed` | The path exists in other methods; `Allow` lists them. |
| 409 | `conflict` | The change clashes with something that exists or changed just now: a name that is taken, the last column of a board, the last admin. |
| 413 | `too_large` | A body over 64 KiB, or a file over the limit of the installation. |
| 415 | `unsupported_media_type` | The body is not `application/json` (or, for `/uploads`, not `multipart/form-data`). |
| 422 | `invalid_input` | The request is well formed but breaks a rule; `fields` says which. Unknown fields, wrong types, empty required fields, a task title that is too long, an upload that is not yours. |
| 429 | `rate_limited` | Too many failed logins. Has `Retry-After`. |
| 500 | `internal` | The server failed. The answer says nothing about the cause; the operator has it in the log. Try again later. |

An answer is never HTML: not for a path that does not exist, not for a wrong method, not for a crash.

## Paging

A list that can grow is paged. A cursor is always the ID of an item, so a page is not shifted by things that arrive while the client reads.

| Route | Parameters | Answer | Notes |
|---|---|---|---|
| `GET /rooms/{id}/posts` | `before` (a post ID), `limit` (1 to 100, default 30) | `{"posts": [...], "next_before": 123}` | The top-level posts of the room, newest first. For the next page send `before=<next_before>`. `next_before` is `null` on the last page. |
| `GET /notifications` | `before`, `limit` (default 30) | `{"notifications": [...], "next_before": ...}` | Newest first, as the feed. |
| `GET /conversations/{id}/messages` | `before` (a message ID), `limit` (default 50) | `{"messages": [...], "next_before": ...}` | The page is **oldest first**, and the newest page comes when `before` is left out. `next_before` is the cursor of the next older page, `null` when there is none. |
| `GET /search` | `q`, `page` (from 1) | `{"results": [...], "page": 1, "has_more": true}` | 20 results a page. The query language is the one of the search box (`#tag`, `room:`, `from:`, `after:`, `before:`, and quoted phrases). A page after the 1000th is empty. |
| `GET /admin/audit` | `page`, and the filters `room_id`, `actor_id`, `entity_type`, `action`, `from`, `to` | `{"events": [...], "page": 1, "has_more": true}` | 50 events a page. `from` and `to` are days, `YYYY-MM-DD`, both included. A filter that is not valid is `422`. |
| `GET /users?q=` | `q` | `{"users": [...]}` | The enabled users whose username or display name starts with `q`, 20 at most. What a client needs to pick members or to mention people. |
| `GET /tags?prefix=` | `prefix` | `{"tags": [...]}` | Up to 10 suggestions. |

A bad `before`, `limit` or `page` is `422` with the parameter under `fields`. These lists have no paging: `GET /rooms`, `GET /rooms/{id}/members`, `GET /rooms/{id}/candidates`, `GET /conversations`, `GET /me/sessions`, `GET /rooms/{id}/board` and the `/admin` lists other than the audit log.

A thread is not paged: `GET /posts/{id}/thread` returns the post and **all** its comments in reading order, each with its `depth` (`parent_id` and `depth` rebuild the tree). Give it the ID of a comment and it returns the thread of its post with `focus_id` set to the comment.

## Changing things: POST, PATCH, PUT and DELETE

- **Create** with `POST`: a room, a post (`POST /rooms/{id}/posts`), a comment (`POST /posts/{id}/comments`, to a post or to a comment), a column, a message, a conversation.
- **Delete** with `DELETE`: `204`. A post that has comments under it is not removed from its thread: it comes back with `deleted: true` and no `body`, `html`, `author`, or files (the comments below stay readable). A deleted message is the same.
- **Edit** with `PATCH`. A `PATCH` changes only what it names:
  - A field that is **absent** keeps its value. For a post or a comment, `null` is the same as absent.
  - A field that is **present** replaces the value. A list replaces the whole list: `"tags": ["a"]` leaves exactly the tag `a`; `"tags": []` removes all tags.
  - The lists of a post are `tags`, `tasks` and `attachment_ids`. For `tasks`, the entries that carry an `id` of a task of the post are edited, those without an `id` are new, and the tasks of the post that are not in the list are deleted. For `attachment_ids`, the files of the post that are not in the list are removed. `upload_ids` adds new uploaded files (below). `attachment_ids` is for an edit; on a new post it is `422`.
  - `PATCH /tasks/{id}` (a card of the board): `assignee_ids` absent keeps the assignees and `[]` removes all of them; `due_date` absent keeps the day, `null` clears it and `"2026-11-03"` sets it. The title of a task is changed by editing its post.
  - `PATCH /rooms/{id}` takes `name` and `description`, either or both. `PATCH /columns/{id}` and `PATCH /conversations/{id}` take `name` (required). `PATCH /messages/{id}` takes `body` (required). `PATCH /admin/users/{id}` takes `role`, `disabled` or both (at least one).
- **Reset link** with `POST /admin/users/{id}/reset-link` (admins only, no body): `201` with `{"url", "expires_at"}`, the link `<address>/reset/<token>` of a page where the user sets a new password. It is shown once, valid for 24 hours, used once, and a newer link replaces the unused older ones. `404` for an unknown user and `409` for a user who is disabled. Using the link ends every session of the user, API tokens included; there is no API route to use it.
- **Move** with `POST`: `POST /tasks/{id}/move` with `{"column_id", "position"}` and `POST /columns/{id}/move` with `{"position"}`. A position counts from 0 and one past the end is the end. They answer `204`; read the board again (or listen for `board.changed`).
- **Mark read** with `POST /notifications/read` and `POST /conversations/{id}/read`, both with `{"up_to_id": <id>}`: everything up to that ID is read.
- Put and take off a reaction with `PUT` and `DELETE` (below). Both are idempotent.

## Files: uploads and downloads

A file goes up on its own, before the post or the message that carries it, so that the client can show the progress and retry a file without writing the post again.

1. `POST /uploads`, `Content-Type: multipart/form-data`, **one** part named `file` (nothing else in the form). Answer `201` `{"id": "...", "file_name", "content_type", "size", "width", "height"}`. The `id` is a long random string, the one thing to keep. The limit is the size the operator set (25 MiB by default): over it the answer is `413`. The server looks at the content to decide the type; `content_type` of the answer is the server's.
2. Send the `id` in `upload_ids` of the post, comment, edit or message that attaches it: `{"body": "the report", "upload_ids": ["…"]}`. An upload belongs to the user who made it and is used **once**: a second use, an upload of another user, an unknown id, and one that was deleted all fail the whole request with `422`, and the client uploads the file again. A request that is refused after its uploads were found (for example because the body is empty, or the user may not post in the room) also uses them up, so the client uploads the files again before it tries again.
3. An upload that no post takes is deleted by the server about 24 hours after it was made (the server looks once an hour).

The most files one post, comment or message takes is set by the operator (10 by default).

A post's `attachments` have an `id`, `file_name`, `content_type`, `size`, `width` and `height` (for images) and a `url`. Download with `GET /attachments/{id}` (the `url` is that path, from the base of the server, `/api/v1/attachments/12`), and for a file of a private message `GET /messages/attachments/{id}`. Both need the bearer token and the right to read the room or the conversation (`404` otherwise). They support `Range`, `If-Modified-Since` and `HEAD`. Only the four image types (PNG, JPEG, GIF and WebP) are sent as themselves, with `Content-Disposition: inline`; every other file is `application/octet-stream` with `Content-Disposition: attachment`, whatever its name says, and always `X-Content-Type-Options: nosniff`. Save a download under the name in `Content-Disposition` (or the `file_name` of the attachment), cleaned of path separators, and never open it automatically.

## Reactions

`PUT /posts/{id}/reactions/{emoji}` puts the emoji on a post or a comment as the user, and `DELETE` takes it off. The emoji is written in the path, URL-escaped (`%F0%9F%91%8D` for the thumbs up). Putting one that is there, or taking off one that is not, is not an error. The answer is `200` `{"reactions": [...]}`: all the reactions of the post as this user sees them (`emoji`, `count`, `users`, and `mine`). Messages use `/messages/{id}/reactions/{emoji}` in the same way. Only members of the room (of the conversation) react.

## The WebSocket

The WebSocket carries the live events that the pages receive over server-sent events, so that a client can keep what it shows up to date without polling. It does not replace the REST routes: the client loads with REST and listens with the WebSocket.

- **Connect** with `GET /api/v1/ws` and the same `Authorization: Bearer <token>` header as any other call, in a WebSocket handshake. A missing or wrong token is the plain `401` JSON answer before any upgrade, and a request that is no handshake is refused by the library (`426` or `400`). The token is never put in the URL. A browser on another site is refused (`403`), because the server checks `Origin`; a desktop client sends none.
- **Messages** are JSON in text frames, one JSON value each. Binary frames and text that is not exactly one JSON value close the connection with `1003`. A message over 16 KiB closes it with `1009`. The server sends nothing when the connection opens.
- **Subscribe.** `{"type": "subscribe", "id": 1, "topics": ["room:3", "post:17"]}`. `id` is the client's own number for the request (a non-negative integer, required), which the answer repeats. The topics:
  - `room:<id>`: the feed of a room: new and changed posts, deleted posts, membership, the room itself;
  - `post:<id>`: the thread of a **top-level** post: its comments, reactions on them (a comment's ID is `invalid_input`: subscribe to the post of its thread);
  - `board:<id>`: the board of the room with that ID;
  - `conversation:<id>`: a private conversation: new, edited and deleted messages, reactions, who has read;
  - `user:<id>`: your own. You are subscribed to it from the start (asking again is fine, removing it is an error, someone else's is `invalid_input`): it carries your notifications, unread counts, and the rooms and conversations you join and leave.
  Ids are written in decimal, with no sign and no leading zero. At most 200 topics besides your own. A subscribe is checked like the REST route that reads the same thing: a room, thread, board or conversation you may not read is `not_found`, admins included. It is all or nothing: if one topic fails, none of the request is subscribed. Subscribing to a topic you hold is not an error.
- **Unsubscribe** `{"type": "unsubscribe", "id": 2, "topics": [...]}` and **ping** `{"type": "ping", "id": 3}` (answered with `{"type": "pong", "id": 3}`).
- **Answers.** `{"type": "ack", "id": 1, "topics": [...]}` when the request was done, or `{"type": "error", "id": 1, "topic": "room:9", "error": {...}}` with the error object of the REST API when it was not; `topic` names the first topic that failed. A message that is valid JSON but not a valid message is answered with an `error` of `invalid_input` and the connection stays open.
- **Events.** `{"type": "event", "topic": "room:3", "kind": "post.created", "room_id": 3, "post_id": 42, "data": {...}}`. The ID fields that the event has are set (`room_id`, `post_id`, `root_id`, `user_id`, `conversation_id`, `message_id`) and the others are absent. `data` is the resource **as the REST API returns it to this user at that moment**, so a client can apply it without another request; it is absent when the event is a deletion. The kinds and their `data`:

  | Kind | Topic | `data` |
  |---|---|---|
  | `post.created`, `post.updated` | `room:<id>` | the post |
  | `post.deleted` | `room:<id>` | none |
  | `comment.created`, `comment.updated` | `post:<root id>` | the comment (a change of a comment also arrives as `post.updated` of its root on the room, so that the comment count refreshes) |
  | `comment.deleted` | `post:<root id>` | none |
  | `reaction.changed` | `room:<id>` and `post:<root id>` | `{"reactions": [...]}` |
  | `board.changed` | `board:<id>` | the board |
  | `room.updated` | `room:<id>` | the room |
  | `room.deleted` | `room:<id>`, `user:<id>` | none; the topics of the room are dropped after it |
  | `member.joined` | `room:<id>`, `user:<id>` | the member |
  | `member.left` | `room:<id>`, `user:<id>` | none |
  | `notification.created` | `user:<id>` | the notification |
  | `notification.changed` | `user:<id>` | the notification, after a reply was folded into it (it names a newer actor and counts more `others`); it never rings |
  | `notification.read`, `conversation.read` | `user:<id>` | `{"unread_notifications": n, "unread_conversations": n}` |
  | `conversation.read` | `conversation:<id>` | `{"conversation_id", "user_id", "up_to_id"}`: who has read up to which message |
  | `message.created`, `message.updated` | `conversation:<id>` | the message |
  | `message.deleted` | `conversation:<id>` | none |
  | `message.reaction` | `conversation:<id>` | `{"reactions": [...]}` |
  | `conversation.updated` | `conversation:<id>` | the conversation (none if you are no longer a member) |
  | `conversation.changed` | `user:<id>` | the conversation (none if you are no longer a member): you were added, or it changed |

- **Access follows you.** Every event is checked for you before it is sent, with the check the REST route would make; an event you may not see is not sent. If you lose a room or a conversation you are subscribed to (you were removed from a private room, it was deleted, you left the conversation), one `error` of `not_found` without an `id` and with the `topic` is sent for each topic that concerned it, and the topics are dropped.
- **Pings and closing.** The server sends WebSocket ping frames every 30 seconds and closes a connection that does not answer within 15 seconds; every common WebSocket library answers pings by itself. A write to a client has 10 seconds. The server closes the connection with a code:

  | Code | Meaning | What the client does |
  |---|---|---|
  | 1001 | The server is stopping (going away). | Reconnect with backoff. |
  | 1003 | A binary frame, or text that is not one JSON value. | A bug of the client. |
  | 1009 | A message over 16 KiB. | A bug of the client. |
  | 1011 | Internal error. | Reconnect with backoff. |
  | 1013 | Try again later. Sent after a `resync` message. | Reconnect, then reload what it shows. |
  | 4001 | The session ended: logged out, expired, or the user was disabled. | Do **not** reconnect; ask the user to log in. |
  | 4002 | The user's role changed. | Reconnect at once (the token still works), then reload. |

- **Resync.** If a client reads too slowly (its queue of 64 events is full) or the server lost its link to the other processes of the installation, the server sends `{"type": "resync", "reason": "overflow"}` (or `"reconnect"`) and closes with `1013`. Events were missed: **reconnect, subscribe again, and load again everything the client shows**. The same applies to any connection that was lost: events are not stored, so after a reconnect the client subscribes first and then loads with REST, and nothing falls in between.
- **Reconnecting.** Wait 1 second after the first failure and double the wait up to about 30 seconds, with some random jitter, so that clients do not return all at the same moment after a restart of the server. Reset the wait when a connection has been up for a while. The server may be restarted for an upgrade at any time.

## An example session

The commands are `curl` against a local installation made with `clowder demo` (the demo users all have the password `demo-password-1234`). `jq` picks out the token.

```sh
BASE=http://localhost:8080/api/v1

# Log in and keep the token.
TOKEN=$(curl -s $BASE/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username": "alice", "password": "demo-password-1234", "client_name": "curl on my laptop"}' | jq -r .token)
AUTH="Authorization: Bearer $TOKEN"

# Who am I, and which rooms can I see?
curl -s -H "$AUTH" $BASE/me
curl -s -H "$AUTH" $BASE/rooms | jq '.rooms[] | {id, name, is_member}'

# The newest posts of room 1, then the page before them.
curl -s -H "$AUTH" "$BASE/rooms/1/posts?limit=2" | jq '{ids: [.posts[].id], next_before}'
curl -s -H "$AUTH" "$BASE/rooms/1/posts?limit=2&before=4" | jq '[.posts[].id]'

# Upload a file, and write a post that carries it, with a tag and a task.
UPLOAD=$(curl -s -H "$AUTH" -F file=@notes.txt $BASE/uploads | jq -r .id)
curl -s -H "$AUTH" -H 'Content-Type: application/json' -X POST $BASE/rooms/1/posts \
  -d "{\"format\": \"markdown\", \"body\": \"Meeting **notes**\", \"tags\": [\"meeting\"], \"tasks\": [{\"title\": \"Send the notes\", \"assignee_ids\": [3]}], \"upload_ids\": [\"$UPLOAD\"]}" | jq '{id, html, attachments}'

# Comment, react, and edit (a PATCH changes only what it names).
curl -s -H "$AUTH" -H 'Content-Type: application/json' -X POST $BASE/posts/12/comments -d '{"body": "Thanks!"}'
curl -s -H "$AUTH" -X PUT "$BASE/posts/12/reactions/%F0%9F%91%8D"
curl -s -H "$AUTH" -H 'Content-Type: application/json' -X PATCH $BASE/posts/12 -d '{"body": "Meeting notes (edited)"}'

# An error: the body is `{"error": {"code": ..., "message": ..., "fields": ...}}`.
curl -s -i -H "$AUTH" -H 'Content-Type: application/json' -X POST $BASE/rooms/1/posts -d '{"body": "x", "colour": "red"}'

# Log out.
curl -s -i -H "$AUTH" -X POST $BASE/auth/logout
```

`cmd/clowder-apicheck` is a small client that runs a longer session of this kind against a running installation and prints every request and answer (`go run ./cmd/clowder-apicheck -h`); with `-watch` it opens the WebSocket, subscribes, makes changes through REST and prints each event that comes back. Its source is a worked example of the calls of this guide.

A WebSocket client in Go, with `github.com/coder/websocket`: it subscribes first, then reads the events, and reconnects after a `resync` or a lost connection (the loading of the page with REST is left out).

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func listen(ctx context.Context, base, token string) error {
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := session(ctx, base+"/api/v1/ws", token)
		if time.Since(started) > time.Minute {
			backoff = time.Second // it was up for a while
		}
		if websocket.CloseStatus(err) == 4001 {
			return fmt.Errorf("the token is no longer valid: log in again")
		}
		sleep := backoff + time.Duration(rand.Int64N(int64(backoff)/2))
		select {
		case <-time.After(sleep):
		case <-ctx.Done():
		}
		backoff = min(backoff*2, 30*time.Second)
	}
	return ctx.Err()
}

func session(ctx context.Context, url, token string) error {
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20) // events can be larger than the 32 KiB default
	subscribe := map[string]any{"type": "subscribe", "id": 1, "topics": []string{"room:1", "board:1"}}
	if err := wsjson.Write(ctx, conn, subscribe); err != nil {
		return err
	}
	// Here the client loads what it shows with REST: it is subscribed already.
	for {
		var msg map[string]any
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			return err // includes the close codes of the table above
		}
		switch msg["type"] {
		case "event":
			raw, _ := json.Marshal(msg["data"])
			fmt.Println(msg["topic"], msg["kind"], string(raw))
		case "error":
			fmt.Println("refused:", msg)
		case "resync":
			return fmt.Errorf("resync: %v", msg["reason"]) // reconnect and reload
		}
	}
}
```

## The OpenAPI document and generated clients

`GET /api/v1/openapi.json` returns the OpenAPI 3.1 document of the installation you talk to, with no login; the same file is in the repository as `internal/api/openapi.json`. A test keeps it equal to the routes (every route has an operation and every operation has a route), and every answer the tests of the API receive is validated against its schema, so the document is the exact contract. It describes every route, its parameters, its request body, its answers and its errors, and the messages of the WebSocket as the schemas `WsClientMessage` and `WsServerMessage` (and `WsEvent` with the list of the kinds).

To generate a client, point a generator at the document, for example:

```sh
curl -s http://localhost:8080/api/v1/openapi.json -o clowder-openapi.json
openapi-generator-cli generate -i clowder-openapi.json -g go -o ./clowder-client      # or typescript-fetch, java, ...
npx openapi-typescript clowder-openapi.json -o clowder.d.ts                           # TypeScript types
```

The document is OpenAPI **3.1** (it uses JSON Schema types such as `"type": ["string", "null"]`). Generators differ in how much of 3.1 they know; if yours only reads 3.0, convert the document first, or use one that supports 3.1. A generator does not cover the WebSocket (OpenAPI does not describe it) or the multipart upload as well as hand-written code does; use the schemas of the WebSocket messages for your types.

## Advice for client writers

- **Treat the server as the source of truth.** Load with REST, listen with the WebSocket, and when in doubt (a `resync`, a reconnect, a `409`), load again.
- **Use the `can_edit`, `can_delete` and `can_manage` flags** of the resources to decide which buttons to show; the server still checks every request.
- **Do not guess at IDs.** A private thing and a missing thing look the same on purpose (`404`), and so do a wrong token and an expired one (`401`).
- **Be gentle on login.** Do not retry a failed login in a loop: five failures lock the username for 15 minutes for every client, the user's browser included.
- **Use TLS.** The token is a bearer token: whoever has it is the user. Use an `https://` installation; the server itself speaks plain HTTP, so the operator puts a proxy with TLS in front (see the README).
- **Do not render `html` blindly and do not open downloads.** See above.
- **Mind the clock.** Times are in UTC; show them in the user's zone. A due date is a day with no zone.
