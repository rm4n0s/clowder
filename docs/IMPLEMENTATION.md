# Clowder: Implementation guide

This file breaks the milestones in `docs/SPEC.md` §13 into small steps. Each step is one unit of work for one coding agent session. Work goes strictly top to bottom. Every finished step is recorded in `docs/IMPLEMENTED.md`.

**Who this is for**: coding agents (and humans) implementing Clowder. You don't need to read the whole file; you need the Rules below and the one step you're doing.

---

## Rules

### Who does the work

The main session is the **manager** (Opus 5.5, high effort) and **workers** write the code (Sonnet 5.5 agents, launched with the Agent tool, `model: "sonnet"`, `effort: "high"`). The rules below apply to the step as a whole; this part says who does what.

- **The manager** does rules 1–3 (finds the step, reads it and the SPEC sections it names, creates the branch), then writes a brief for each worker in `.claude/briefs/step-<id>[-<part>].md` (git-ignored; never committed). A brief is the only thing the worker knows: the goal, the tasks it owns, the files and SPEC sections to read, the tests to write, the decisions already made and those it must bring back instead of making, and the rules it must follow (CLAUDE.md, the "Errors" rules, doc comments).
- **Workers run in the shared checkout, on the step branch, without worktree isolation**: an isolated worktree starts from `main`, not from the step branch. A worker never runs a git command that changes state (commit, merge, rebase, stash, push, tag, branch, switch, reset, checkout of files); the manager does all of them.
- **One worker per step**, or several at once only when their parts touch disjoint directories and do not depend on each other; at most three at a time. A part that needs another part's API waits for it.
- **The manager reviews the work, not the summary**: it reads the diff, runs the tests the worker says pass and the checks of rule 8 itself, and looks at every proof. What is wrong, missing or outside the step goes back to the same worker (SendMessage, so it keeps its context) with the concrete failure. The manager writes only small fixes itself (a few lines, a doc comment, a test table row).
- **Questions.** A worker that hits a case of rule 7 stops and reports it to the manager; the manager decides (rule 7) and the work goes on. The manager never stops for a decision. The manager answers "what is going on" questions of the user while workers run, and never reports a worker's result before it arrived.
- **The manager finishes the step**: the `IMPLEMENTED.md` entry (rule 10; it may have a worker draft it, and checks every claim in it), the commit, the publish and the tag (rules 11–12), the report (rule 13), and deletes the step's briefs.

### Before you start a step

1. **Find your step.** It's the first step in this file that has no entry in `docs/IMPLEMENTED.md`. Do only that step. Never work on two steps at once, and never skip ahead.
2. **Read, in this order:**
   1. `CLAUDE.md`, all of it.
   2. The whole step in this file.
   3. Every section of `docs/SPEC.md` listed under **Read**.
3. **Create the branch.**
   ```sh
   git switch main && git pull --ff-only
   git switch -c feat/step-<id>-<slug>     # e.g. feat/step-1.2-config-loader
   ```

### While you work

4. **Do exactly the step's Tasks.** Don't do work that belongs to a later step, even if it looks easy. If a later step will need a hook, leave only a comment: `// TODO(step 3.2): ...`.
5. **Follow CLAUDE.md.** In particular: `rm4n0s/errors` v0.2.0 for every error, used the way its `AI_GUIDE.md` says (the "Errors" rules below), and a doc comment on every exported identifier.
6. **Test what you build.** Write the tests listed under **Tests** as table-driven Go tests, plus a route test for every new error tag. In `docs/IMPLEMENTED.md`, say how many test cases were added.
7. **Never stop** (the owner's rule, 2026-10-26). The manager decides and keeps going:
   - the step is ambiguous, or the spec looks wrong or contradicts itself: take the reading that fits the SPEC's goals best (the recommendation, if §16 has one), write the decision into the SPEC (§4, marked "decided by the manager, 2026-…; the owner may revise"), and mention it in the step's report
   - a tool is missing or something needs `sudo`: find a way that needs neither (a pure-Go alternative, a test that skips with a clear message when the tool is absent), record it in the step's "Deviations and follow-ups", and list the install command in the report
   - a new dependency seems necessary: prefer the standard library or an allowed dependency; if none can do it reasonably, add the smallest well-known dependency, record it as a decision in SPEC §4 and CLAUDE.md, and mention it in the report
   - a check still fails after a real attempt to fix it: keep fixing (another worker, a different approach, a smaller step) until it passes

   Never weaken or delete a test, skip a check, or mark a step done just to get unblocked. The user may still interrupt with a question or a change; answer it and continue.

**Errors.** `github.com/rm4n0s/errors` is used to the fullest, as its `AI_GUIDE.md` says. This is a requirement of the project owner, not a style choice. It is in the module cache, so reading it is allowed: `$(go env GOMODCACHE)/github.com/rm4n0s/errors@v0.2.0/AI_GUIDE.md`. Read all of it before you write or change code that makes, returns, logs or tests an error, and again after the dependency moves to a new version (then also read the library's `errors.go` for changed signatures). The rules, with the guide's numbers (SPEC §9.3 has the same list with the reasons):
- **R1.** `errors.New(tag, message, metadata...)` always has a tag and a message. A tag names one thing a function cannot do.
- **R2.** Metadata is given in one call, as alternating `"key", value` pairs, with string keys: to `errors.New` itself, and after `NewErr` or `NewErrf` (which take no metadata) in exactly one `SetMetadata`. A later `SetMetadata` is only for data that becomes known in another function. `RemoveMetadata` and `SetMessage` exist for the rest.
- **R3.** An error is tagged once, where it first appears. An error from a function of this module is returned as it is (`return err`): no `NewErr`, no `NewErrf("... %w")`, no `fmt.Errorf`, no new tag on the way up. An error from outside the module (the standard library, third-party packages) is tagged where it first appears with `errors.NewErr`, so a function of this module never returns an untagged foreign error. A failure the code detects itself is `errors.New`.
- **R4.** The constructor is called in the function that fails, never in a helper that builds and returns the error (the helper would be a frame in the route). An exported function or method is not a helper.
- **R5.** A tag is a constant declared next to the code that produces it (`const tagX = "X"` at the end of the file that makes the error, exported as `TagX` with a doc comment when another package matches on it). Code branches on `appErr.Tag == pkg.TagX` or `errors.Is(err, sentinel)`, never on a string literal or on `Error()` or `Message`. A sentinel is a struct literal, `&errors.Error{Tag: TagX}`, never a package-level `errors.New`.
- **R6.** Import only `github.com/rm4n0s/errors` (it re-exports `Is`, `As` and `Unwrap`). When something else of the standard library's `errors` is needed (`Join`), import it as `stdErrors "errors"`.
- **Logging.** An error is logged with `slog` as the attribute `"err"`: `logger.Error("...", "err", err)`. `*errors.Error` is an `slog.LogValuer`, so the entry has `Tag`, `Message`, `Route`, `OriginalErr`, `Metadata` and `StackFrames`, and a test replays it through `errors.ErrorJson` and `errors.FromJson`. Code does not call `ToJson` or `json.Marshal` on an error to log it.
- **Tests.** Every tag has a test that asserts the path with `HasRoute("Outer->Inner.Tag")` (the entry function you call, down to the tag), and the metadata when it matters. Function names in a route are written the way the guide says: `Type.Method`, no package names.

### Finishing a step

8. **The standard checks must all pass.** Run them from the repo root:
   ```sh
   test -z "$(gofmt -l .)"
   go vet ./...
   golangci-lint run
   golangci-lint run --build-tags clowder_browser
   go test -race ./...
   CGO_ENABLED=0 go build ./...
   go generate ./... && test -z "$(git status --porcelain)"   # the committed *_templ.go files are up to date
   ```
   If the step lists **Browser tests**, run them too: `go test -tags clowder_browser <packages>` (SPEC §12). Once the harness exists (M0), also check that `go vet -tags clowder_browser ./...` passes.
9. **Proof.** Only when the step has a **Proof** item:
   - Produce exactly what it lists, in `docs/implemented-proof-images/`, named `<id>-<name>.<ext>` (e.g. `1.3-server-start.txt`, `4.2-dashboard.png`).
   - Use the method it names (see "Proof methods" below).
   - **Look at every proof yourself** (the Read tool shows images and text) and check that it shows what **Proof** describes. If it doesn't, fix the code. The step isn't done.
   - Never draw, edit, crop, write by hand or generate a proof any other way, and never reuse an older one.
10. **Record the step.** On the step branch, add an entry to `docs/IMPLEMENTED.md`, using the format at the top of that file. Link or embed the proofs.
11. **Commit on the step branch.** Use one commit per step, with the message `Step <id>: <imperative summary>`:
    ```sh
    git add -A && git commit -m "Step 1.2: Add the config loader"
    ```
12. **Publish every finished step to `main`, right away.**
    - A step counts as finished only when its commit is on `origin/main` (a push that fails for network reasons is retried later, see below).
    - Publish before you start the next step. Never collect several steps into one publish, and never leave a finished step only on its branch.
    - Publish only after rules 8–11 are complete: checks green, proof verified, `IMPLEMENTED.md` entry committed.
    ```sh
    git switch main && git pull --ff-only
    git merge --ff-only feat/step-1.2-config-loader
    git push origin main
    git tag -a step-1.2 -m "Step 1.2: Add the config loader"
    git push origin step-1.2
    git branch -d feat/step-1.2-config-loader
    ```
    - **Tag every published step** with an annotated tag `step-<id>` (the step's id as in this file) on the commit now on `main`, and push it right after the push of `main`. The step is finished only when the tag is on `origin` too. If the tag already exists, stop and tell the user; never move or delete a tag.
    - The release tags `v<major>.<minor>.<patch>` are a separate thing. When a version is finished (its release step, or the fix step that closes a patch version), its commit is published to `main` and the annotated `v…` tag is made on it and pushed at once, without asking.
    If `--ff-only` fails, rebase the step branch onto `main`, run the standard checks again, and retry. If a push fails (network, a reset connection), retry it a few times with a pause; if it still fails, start the next step anyway from the local `main` and push `main` and every pending tag as soon as a later push succeeds (check with `git status -sb` and `git ls-remote --tags origin` before each report). A push that is **rejected** (someone else changed `origin/main`) is resolved by pulling and rebasing, never by force. A step counts as finished once its commit and tag are on `origin`; report the ones still waiting to be pushed.
13. **Report to the user.** Give the step id, a two-line summary, the check results, the proof paths, and the commit hash now on `main`.

**Default "Done when".** A step without a **Done when** line is done when its Tasks are complete, its **Tests** are written and passing, its **Proof** is verified, and the standard checks pass.

### Proof methods

SPEC D22.

| Method | Use it for | How |
|---|---|---|
| **A. Page screenshot** | anything shown in a page | `go run -tags clowder_browser ./cmd/clowder-proof -as <user> -page <path> -o docs/implemented-proof-images/<id>-<name>.png` (from step 0.x that adds the tool): it starts `clowder serve` on a temporary data directory filled by `clowder demo`, logs in through the login form with headless Chromium, opens the page and saves it. A step may add flags for a state the page needs (an open editor, a dragged card) |
| **B. Command output** | what has no page: storage, services, the CLI | run the command named in the step and save its output with `… > docs/implemented-proof-images/<id>-<name>.txt 2>&1` |

### Demo data

- Clowder has no `examples/` (SPEC §14). Proofs and manual checks run against `clowder serve` on a database filled by `clowder demo` (from the step that adds it; before that, a step's **Proof** says how it makes its data).
- A step that adds something visible also adds it to `clowder demo`, once that command exists.

---


## Milestone overview

| Milestone (SPEC §13) | Steps |
|---|---|
| M0 Skeleton | 0.1–0.4 |
| M1 Storage | 1.1–1.2 |
| M2 Accounts | 2.1–2.4 |
| M3 Rooms and the audit log | 3.1–3.4 |
| M4 Posts | 4.1–4.3 |
| M5 Comments | 5.1–5.2 |
| M6 Attachments | 6.1–6.2 |
| M7 Live updates | 7.1–7.2 |
| M8 Reactions | 8.1 |
| M9 Tasks and the board | 9.1–9.3 |
| M10 Search | 10.1–10.2 |
| M11 Mentions and notifications | 11.1–11.2 |
| M12 Private messages | 12.1–12.2 |
| M13 1.0 | 13.1–13.3 |

---

## M0: Skeleton

### 0.1 Module and package layout
**Goal:** a complete repository skeleton that passes all checks.
**Read:** SPEC §4, §6, §11, §15; CLAUDE.md "Layout" and "Error handling".
**Tasks:**
1. `go mod init github.com/rm4n0s/clowder`, set `go 1.27`.
2. Create every package directory in SPEC §6.2 (except `internal/web/static`, which step 0.2 fills). Give each one a `doc.go` whose package comment states its purpose. `cmd/clowder` and `cmd/clowder-proof` get an empty `main` (the proof tool's files carry `//go:build clowder_browser`, and so does `internal/browsertest`).
3. Add `github.com/rm4n0s/errors` v0.2.0 to `go.mod` with its first use: `internal/config` gets `Config` (the fields of SPEC §11 with their defaults) and `Validate() error` with tagged errors and route tests.
4. Check that `.golangci.yml` flags an undocumented exported function (by hand, then remove the test function).
5. Create `scripts/` (empty except a `README.md` listing the scripts to come).
**Tests:** `config.Validate` table (each invalid field gives its tag; route tests).
**Done when:** the standard checks pass.

### 0.2 Dependencies, templ and static files
**Goal:** every dependency of SPEC D6–D10 and D23 is in `go.mod`, templ generates, and the static files are embedded.
**Read:** SPEC D6–D13, D23, §6.2, §15; CLAUDE.md "Go conventions".
**Tasks:**
1. Add `gorm.io/gorm`, `github.com/glebarez/sqlite`, `github.com/starfederation/datastar-go`, `github.com/yuin/goldmark`, `golang.org/x/crypto`, `github.com/chromedp/chromedp` (latest releases), and templ as a tool (`go get -tool github.com/a-h/templ/cmd/templ`) plus its runtime `github.com/a-h/templ`. A dependency that is not used yet is kept by a blank import in a `deps.go` of the package that will use it, with a comment naming the step that uses it (remove the blank import in that step).
2. Find the `datastar.js` release that matches the `datastar-go` version (the SDK's README or source names it), download it, put it in `internal/web/static/datastar.js` with its MIT license as `internal/web/static/datastar.LICENSE`, and record the two versions in SPEC D7. Write `NOTICE` listing it.
3. `internal/web/static`: `embed.FS` with `datastar.js`, `app.css` (a small base: CSS variables for colors, light and dark, system fonts, layout of a page with a sidebar and a main column) and `static.go` exporting the FS.
4. `internal/web/views`: `layout.templ` (the HTML page: title, CSS, `datastar.js` as a module, sidebar slot, main slot) and `home.templ` (a placeholder home page). A `//go:generate go tool templ generate` line in `internal/web/views/generate.go`. Commit the generated files.
**Tests:** a test renders `Home` to a buffer and checks the title and the script tag.
**Done when:** the standard checks pass, including the `go generate` check.

### 0.3 `clowder serve`
**Goal:** the server starts, serves the home page and the static files, logs each request, and stops cleanly.
**Read:** SPEC §6.1, §8 (the shutdown), §9.2, §11; CLAUDE.md "Go conventions".
**Tasks:**
1. `cmd/clowder`: subcommand dispatch (`serve`, `invite`, `demo`; the last two return a tagged `NotImplemented` until their steps), flags and `CLOWDER_*` variables into `config.Config`, a JSON `slog` logger at the configured level, the data directory created if missing.
2. `internal/web`: `Server` built from its dependencies, `http.ServeMux` routes (`GET /{$}` home, `GET /static/` with cache headers), middleware: request logging (SPEC §9.2), panic recovery (logs a tagged error, answers 500), `http.CrossOriginProtection`.
3. `serve` runs until SIGINT or SIGTERM, then shuts down as SPEC §8 says (10 s limit).
4. An error page template (404 and 500) used by the handlers.
**Tests:** handler tests with `httptest` (home 200, static file with content type, unknown path 404 page, panic → 500 and a logged error with its route); dispatch table of `cmd/clowder` (unknown command → tagged error with route).
**Done when:** `go run ./cmd/clowder serve` serves the home page at `:8080` and stops on Ctrl-C; the standard checks pass.

### 0.4 Proof tool and browser-test harness
**Goal:** a page can be proven by a screenshot, and browser tests can run.
**Read:** SPEC D22, D23, §12, §14; this file, "Proof methods".
**Tasks:**
1. `internal/browsertest` (`clowder_browser` tag): starts `clowder serve` (built once per test binary with `go build` into a temp dir) on a free port and a temporary data directory, waits until it answers, and gives chromedp contexts (headless Chromium with a temporary profile). Everything stops in `t.Cleanup`. Finds Chromium as `chromium-browser`, `chromium` or `google-chrome`, and skips the test with a clear message if none is found.
2. `cmd/clowder-proof` (`clowder_browser` tag): flags `-page` (path), `-o` (PNG), `-width`/`-height` (default 1280×800), `-as` (a user name; accepted but unused until step 2.4, which adds the login). It runs `clowder demo` on a temp data dir (a no-op until 2.2), starts the server, opens the page, waits for it to settle, saves the screenshot, stops everything.
3. `clowder demo` becomes a command that only checks the data directory is empty (later steps add data).
**Browser tests:** the home page loads in Chromium and `datastar.js` has run (a `data-signals` value is shown by `data-text`).
**Proof:** method A: `0.4-home.png`, the home page in the base layout.
**Done when:** the browser test passes, the proof is verified, and the standard checks pass.

---

## M1: Storage

### 1.1 Clock and the store
**Goal:** the database opens, migrates and is testable.
**Read:** SPEC D6, §6.1, §6.3, §9.1, §9.3, §12, §17 (SQLite risks).
**Tasks:**
1. `internal/clock`: `Clock` interface (`Now`), the real one, and `Fake` (set and advance) for tests.
2. `internal/store`: `Open(ctx, path, logger)` with the pure-Go driver: WAL, `busy_timeout`, `foreign_keys=ON`, one writer connection pattern if needed; `Close`. `OpenMemory(t)` test helper (a unique shared in-memory database per test). Migrations: GORM `AutoMigrate` plus an ordered list of raw SQL migrations recorded in a `schema_migrations` table (the FTS5 table of step 10.1 will use it). `Store.Tx(ctx, func(tx *Store) error)`.
3. Error mapping: `gorm.ErrRecordNotFound` becomes `TagNotFound` where it first appears; unique-constraint failures become `TagConflict`. Export the tags from `internal/store`; `internal/app` will map them (step 2.2).
**Tests:** open/migrate twice (idempotent), transaction commit and rollback, not-found and conflict tags with routes, the fake clock.
**Done when:** the standard checks pass.

### 1.2 Models
**Goal:** every model of SPEC §6.3, migrated with its constraints and indexes.
**Read:** SPEC §6.3, §7 (for the constraints), §7.10.
**Tasks:**
1. The models of §6.3 in `internal/store`, one file per area (users, rooms, posts, tasks, notifications, audit), with unique indexes, foreign keys (`ON DELETE` as §7 implies; none from `AuditEvent` to its entity) and the indexes the queries of §7 will need (feed by room and time, thread by root, board by room and column and position, audit by time, room, actor, action).
2. Times in UTC through the clock passed to the store (no `time.Now` in the store).
**Tests:** for each model: save, read back, and each unique constraint giving `TagConflict`; deleting a room cascades as intended.
**Done when:** the standard checks pass.

---

## M2: Accounts

### 2.1 Passwords and tokens
**Goal:** `internal/auth` hashes passwords and makes tokens.
**Read:** SPEC D10, D11, §7.1.
**Tasks:**
1. argon2id `HashPassword` / `VerifyPassword` in the PHC string format, with parameters as constants (memory 64 MiB, time 3, threads 2; lower parameters through an option for tests).
2. `NewToken()` (32 random bytes, base64url) and `HashToken(token) string` (SHA-256 hex). Constant-time comparison.
**Tests:** tables for verify (right, wrong, malformed hash → tagged error with route), token length and uniqueness.
**Done when:** the standard checks pass.

### 2.2 Accounts service and invitations
**Goal:** users are made from invitations; admins manage users.
**Read:** SPEC §3.1, §6.1, §7.1, §9.1, §12; CLAUDE.md "Go conventions".
**Tasks:**
1. `internal/app`: the exported tags of SPEC §9.1 (mapping the store's `TagNotFound`/`TagConflict` where they enter the service), the `Actor` passed to every method, and `Accounts`: `CreateInvitation(ctx, actor, role)` (admin only; returns the link token), `InvitationValid`, `SignUp(ctx, token, username, displayName, password)` (validates §7.1, uses the invitation once, in one transaction), `Users`, `SetRole`, `SetDisabled` (the last admin keeps the role; disabling deletes sessions).
2. `clowder invite -admin` (and `-member`): prints `<base URL>/invite/<token>`.
3. `clowder demo` creates the demo users (an admin `admin` and members `alice`, `bob`, `carol`, password `demo-password-1234`) and prints them.
**Tests:** validation tables, single use, expiry with the fake clock, last-admin rule, permission table for every method, route tests.
**Done when:** the standard checks pass.

### 2.3 Sessions, login and sign-up pages
**Goal:** a person goes from an invitation link to a logged-in page.
**Read:** SPEC D11, §7.1, §9.1, §9.2.
**Tasks:**
1. `Accounts.Login` (with the limiter of §7.1, injected, using the clock), `Logout`, `SessionUser` (refreshes `LastSeenAt`, expires after the lifetime).
2. Middleware: reads the session cookie, puts the actor in the request context; `TagUnauthenticated` → redirect to `/login?next=…`. Cookie flags as D11 (Secure as SPEC §11 says).
3. Pages: `/login`, `/invite/{token}` (sign-up form, field errors shown next to the fields, sent with Datastar), `POST /logout`. Every page except these and static files needs a session. The home page shows the user's display name.
**Tests:** handler tests for each page and failure (bad password, unknown user same message, limiter, expired session, used invitation), cookie flags, cross-origin POST refused.
**Proof:** method A: `2.3-login.png` (the login page), and `2.3-signup.png` (the sign-up form with a field error; the proof tool may need a `-click`/`-fill` flag: add it).
**Done when:** the standard checks pass and the proofs are verified.

### 2.4 Admin users page and logged-in proofs
**Goal:** admins manage users and invitations in the UI; proofs can log in.
**Read:** SPEC §7.1.
**Tasks:**
1. `/admin/users`: the users with role and state, buttons to change the role and disable/enable; a form making an invitation link (role, shown once with a copy button). Admin only (members get 404).
2. The proof tool's `-as <user>` logs in through `/login` with the demo password; `internal/browsertest` gets the same `LogIn(ctx, user)`.
3. A sidebar in the layout with the user's name, logout, and (admins) an "Admin" link.
**Tests:** handler tests and permissions of the admin page.
**Browser tests:** log in as `alice` and see her name.
**Proof:** method A: `2.4-admin-users.png` as `admin`.
**Done when:** the standard checks pass and the proof is verified.

---

## M3: Rooms and the audit log

### 3.1 Audit log core
**Goal:** services can write audit events in their transactions, and admins can query them.
**Read:** SPEC D24, D27, §6.3, §7.10.
**Tasks:**
1. In `internal/app`: an unexported `audit(tx, event)` used inside service transactions (the event's actor, room, entity, summary and details as SPEC §7.10 says), and `Audit.Events(ctx, actor, filter, page)` (admin only; filters of §7.10, newest first, 50 a page).
2. The action names of §7.10 as constants.
**Tests:** writing in a transaction that rolls back leaves no event; filters; permission table.
**Done when:** the standard checks pass.

### 3.2 Rooms service
**Goal:** rooms and memberships follow SPEC §7.2, with their audit events.
**Read:** SPEC D14, D15, D26, D27, §7.2, §7.10.
**Tasks:**
1. `Rooms`: `Create` (visible by admins, private by anyone; the three default columns; creator is a member), `Visible`, `Mine`, `Get` (non-members of private rooms get `TagNotFound`), `Join` (visible only), `Leave`, `AddMember` (any member of a private room adds any user; members of a visible room add any user too), `RemoveMember` (admins), `Update` (name, description: admins and the creator), `Delete` (admins and the creator). The rule "can read" and "is member" are methods every later service uses.
2. Audit events `room.*` and `member.*` for each of them. Notifications are step 11.1: leave `// TODO(step 11.1): notify added member`.
3. `clowder demo` creates rooms (`general` and `random` visible, `secret` private with alice and bob) with members.
**Tests:** the permission table of SPEC §12 for every method, the audit events and none on failure.
**Done when:** the standard checks pass.

### 3.3 Rooms pages
**Goal:** users see, create, join and leave rooms.
**Read:** SPEC §7.2.
**Tasks:**
1. Sidebar: the user's rooms and a "Browse rooms" link; `/rooms` lists visible rooms with join buttons; a "New room" form (visibility choice only for admins; members make private rooms).
2. `/rooms/{id}`: the room's header (name, description, visibility, join/leave), an empty feed placeholder, a members panel with "Add member" (user picker) and, for admins, remove. Rename and delete for those allowed.
**Tests:** handler tests of each action, including 404 for a private room's non-member.
**Proof:** method A: `3.3-room.png`, the room `secret` as `alice` with its members panel.
**Done when:** the standard checks pass and the proof is verified.

### 3.4 Audit log page
**Goal:** admins read the audit log and manage every room.
**Read:** SPEC §7.10.
**Tasks:**
1. `/admin/rooms` (SPEC D29): every room with visibility, member count and creation date; rename, delete and a members list with remove, for admins. Members get 404.
2. `/admin/audit`: the table of events (time, actor, room, action, summary, expandable details), filters of §7.10 as a form that updates the table with Datastar, pagination, links to entities that still exist.
**Tests:** handler tests with filters; members get 404.
**Proof:** method A: `3.4-audit.png` as `admin`, showing the demo's room and member events.
**Done when:** the standard checks pass and the proof is verified.

---

## M4: Posts

### 4.1 Markdown and text rendering
**Goal:** bodies are rendered safely.
**Read:** SPEC D9, §7.3, §7.9, §17 (risks).
**Tasks:**
1. `internal/markdown`: `Render(format, body) template.HTML`. Markdown: goldmark with GFM (tables, strikethrough, autolinks, task-list items read-only), safe mode (no raw HTML, dangerous links dropped), links get `rel="nofollow noopener"` and `target="_blank"`. Text: escaped, line breaks kept, URLs linked.
2. `Mentions(format, body) []string`: `@username` outside code spans and code blocks.
**Tests:** tables including script injection attempts (`<script>`, `javascript:` links, `onerror` attributes, HTML in text) and mentions inside and outside code.
**Done when:** the standard checks pass.

### 4.2 Posts service
**Goal:** posts are created, edited, deleted and listed with their tags and audit events.
**Read:** SPEC D16, D21, §6.3, §7.3, §7.7, §7.10.
**Tasks:**
1. `Posts`: `Create(ctx, actor, roomID, input)` (body, format, tags; limits of §7.3 and §7.7; members only), `Edit` (author), `Delete` (author or admin; D21 semantics), `Feed(ctx, actor, roomID, before, limit)`, `Get`. Tags normalized (§7.7). `Tags.Suggest(prefix)`.
2. Audit events `post.created`, `post.edited` (body changed and length, format, tags), `post.deleted`.
3. `clowder demo` adds posts with markdown and tags.
**Tests:** validation tables, permission tables, D21 cases, audit events, feed paging.
**Done when:** the standard checks pass.

### 4.3 Editor and feed
**Goal:** members write, read, edit and delete posts in a room.
**Read:** SPEC §7.3, §7.7.
**Tasks:**
1. The editor component (reused for comments and later attachments and tasks): body, Text/Markdown switch, a preview, a tags field with suggestions (separate from the body), submit with Datastar; field errors shown in place.
2. The room's feed: newest first, 30 at a time, more on scroll (Datastar), each post with author, time, "edited", body, tags (links to `/search?q=%23tag`, a page that step 10.2 makes; until then it is a 404), comment count (0 until M5). Edit in place and delete with confirmation for those allowed.
3. `/posts/{id}`: the post's page.
**Tests:** handler tests for create, edit, delete, feed paging, a non-member of a visible room sees no editor.
**Browser tests:** write a post with two tags and see it at the top of the feed.
**Proof:** method A: `4.3-feed.png`, `general` as `alice` with markdown posts and tags.
**Done when:** the standard checks pass and the proof is verified.

---

## M5: Comments

### 5.1 Comments service
**Goal:** comments to any depth, with the same features as posts.
**Read:** SPEC D16, D21, §7.3, §7.10.
**Tasks:**
1. `Posts.Reply(ctx, actor, parentID, input)` (sets `ParentID` and `RootID`), `Thread(ctx, actor, postID)` (the tree in order), comment counts for the feed. Edit and delete work for comments (`EntityType` comment in the audit events).
2. `clowder demo` adds threads three levels deep.
**Tests:** tree building, D21 placeholders in a thread, permissions, audit events.
**Done when:** the standard checks pass.

### 5.2 Threads in the UI
**Goal:** users read and write threads.
**Read:** SPEC §7.3.
**Tasks:**
1. The post page shows the thread: indentation up to depth 6, then "replying to @user"; each comment has reply, edit, delete; the reply editor is the step 4.3 editor.
2. The feed shows the comment count linking to the thread.
**Tests:** handler tests.
**Proof:** method A: `5.2-thread.png`, a demo thread with a deleted comment that has replies.
**Done when:** the standard checks pass and the proof is verified.

---

## M6: Attachments

### 6.1 Attachment storage and service
**Goal:** files are stored, limited and served only to readers.
**Read:** SPEC D19, §7.5.
**Tasks:**
1. `internal/files`: `Save(ctx, r io.Reader, limit) (key, size, error)` into `<data>/files/` (random key, write to a temp file then rename), `Open(key)`, `Remove(key)`.
2. In `internal/app`: attachments in `Create`/`Reply`/`Edit` input (limits of §7.5, image detection by content sniffing, dimensions with the standard `image` package for PNG, JPEG and GIF and by reading the header for WebP), `Attachment(ctx, actor, id)` checks the reader; deleting a post removes its files after the commit.
**Tests:** limits (count, size), sniffing, permissions, files removed on delete.
**Done when:** the standard checks pass.

### 6.2 Upload and download
**Goal:** users attach files and see images inline.
**Read:** SPEC §7.5.
**Tasks:**
1. The editor gets a file input (multipart upload with the post), a list of chosen files with remove buttons, the limit errors.
2. `GET /files/{id}/{name}`: the checks of §7.5, `Content-Disposition` (inline for the listed images, attachment otherwise), `nosniff`, caching for readers.
3. Posts show images inline (scaled, with width and height) and other files as links with name and size. `clowder demo` adds an image and a PDF.
**Tests:** handler tests of upload and download, a non-member of a private room gets 404.
**Proof:** method A: `6.2-attachments.png`, a post with an inline image and a file link.
**Done when:** the standard checks pass and the proof is verified.

---

## M7: Live updates

### 7.1 The hub
**Goal:** services publish events after commit and subscribers receive them.
**Read:** SPEC D18, §8.
**Tasks:**
1. `internal/live`: `Hub` with `Publish(topic, event)` and `Subscribe(ctx, topics...) (<-chan Event, unsubscribe)`; bounded queue per subscriber, a full queue closes the subscriber; `Close`. A recording hub for tests.
2. The services publish after their transaction commits (rooms, members, posts, comments, attachments) with the topics of §8.
**Tests:** fan-out, slow subscriber closed, context cancel, no event when the transaction fails, `-race`.
**Done when:** the standard checks pass.

### 7.2 Streams of feeds and threads
**Goal:** what one session writes appears in another at once.
**Read:** SPEC §8.
**Tasks:**
1. `GET /rooms/{id}/stream` and `GET /posts/{id}/stream` (Datastar SSE, opened by `data-on-load`/`@get`): subscribe after the permission check, render each event for the subscriber's user and send Datastar patches (new post at the top, edited or deleted post replaced, new comment in its place, counts); end the stream when the user loses access; the page reconnects after a closed stream and reloads the part it shows.
2. Membership changes update the sidebar of the user they concern.
**Tests:** handler tests that read the SSE stream with `httptest` and check the patches; access lost ends the stream.
**Browser tests:** two sessions (alice and bob) on `general`: alice posts, bob sees it without reload; alice comments, bob sees it on the post page.
**Proof:** method A: `7.2-live.png`, bob's view after alice's post (the proof tool may need a step that acts as another user first: add a `-before` script flag).
**Done when:** the standard checks pass and the proof is verified.

---

## M8: Reactions

### 8.1 Reactions
**Goal:** emoji reactions on posts and comments, live.
**Read:** SPEC §7.4.
**Tasks:**
1. `Reactions.Toggle(ctx, actor, postID, emoji)` (members only; emoji validated as one grapheme of emoji), counts and reactors per post; events published.
2. UI: a reaction bar under each post and comment, a picker with common emoji and a text field, the reactors on hover; live updates of counts. `clowder demo` adds reactions.
**Tests:** toggle table, permissions, handler tests, a non-emoji refused.
**Browser tests:** bob sees alice's reaction appear.
**Proof:** method A: `8.1-reactions.png`.
**Done when:** the standard checks pass and the proof is verified.

---

## M9: Tasks and the board

### 9.1 Tasks and columns service
**Goal:** tasks and columns follow SPEC §7.6, with their audit events.
**Read:** SPEC D17, D25, §7.6, §7.10.
**Tasks:**
1. Tasks in the input of `Create`/`Reply`/`Edit` (title, assignees that are room members, due date; edit matches tasks by ID, removes the missing ones). `Board(ctx, actor, roomID)`; `MoveTask(ctx, actor, taskID, columnID, position)`; `UpdateTask` (assignees, due date, title); columns `AddColumn`, `RenameColumn`, `MoveColumn`, `DeleteColumn` (not if it has cards; never the last). Positions kept dense and ordered.
2. Audit events `task.*` and `column.*`; live events on `board:<id>`. `// TODO(step 11.1): notify assignees`.
3. `clowder demo` adds tasks across the columns.
**Tests:** permission tables, position tables (move within and across columns), column rules, audit events.
**Done when:** the standard checks pass.

### 9.2 Tasks in the editor and in posts
**Goal:** users write tasks in posts and comments.
**Read:** SPEC §7.6.
**Tasks:**
1. The editor gets task rows (title, assignee picker with the room's members, due date) to add and remove.
2. Posts and comments show their tasks: title, column, assignees, due date (overdue in red unless in the last column), a link to the card on the board.
**Tests:** handler tests.
**Proof:** method A: `9.2-post-tasks.png`.
**Done when:** the standard checks pass and the proof is verified.

### 9.3 The board
**Goal:** each room's Kanban board, live.
**Read:** SPEC §7.6, §8.
**Tasks:**
1. `/rooms/{id}/board`: the columns and cards of §7.6; drag and drop of cards between and within columns (a small script in `internal/web/static` using HTML drag-and-drop events that calls the move endpoint through Datastar); column add, rename, reorder, delete; assign and due date on the card.
2. The stream `GET /rooms/{id}/board/stream` keeps the board live (SPEC §8).
**Tests:** handler tests of each action.
**Browser tests:** alice drags a card to "In progress"; bob's board shows it there; a column with cards cannot be deleted.
**Proof:** method A: `9.3-board.png`, the board of `general`.
**Done when:** the standard checks pass and the proof is verified.

---

## M10: Search

### 10.1 Search index and service
**Goal:** full-text and tag search over what the user can read.
**Read:** SPEC D6, §7.8.
**Tasks:**
1. An FTS5 table (raw SQL migration of step 1.1) over post and comment bodies, attachment names and task titles, maintained in the same transactions as the changes (create, edit, delete).
2. `Search.Query(ctx, actor, query, page)`: a parser for words (prefix match), `#tag` (all must match), `room:`, `from:`, `after:`, `before:`, `is:post`; results only from readable rooms (SPEC §7.8), with a highlighted snippet.
**Tests:** parser table; result tables including a private room's posts not found by a non-member; index updated on edit and delete.
**Done when:** the standard checks pass.

### 10.2 Search page
**Goal:** users search from one box.
**Read:** SPEC §7.7, §7.8.
**Tasks:**
1. A search box in the layout; `/search?q=` with results (room, author, time, snippet, link), filters as chips, paging. Tag links of posts open it.
**Tests:** handler tests.
**Proof:** method A: `10.2-search.png`, a search with a word and a tag.
**Done when:** the standard checks pass and the proof is verified.

---

## M11: Mentions and notifications

### 11.1 Mentions and notifications service
**Goal:** users are notified as SPEC §7.9 says.
**Read:** SPEC D20, §7.9.
**Tasks:**
1. Mentions saved on create and edit (only users who can read the room; on edit only new ones notify); notifications for mention, reply, assigned, added to room (resolve the `TODO(step 11.1)`s); never for one's own action. `Notifications.List`, `MarkRead`, `UnreadCount`; live events on `user:<id>`.
2. Mentions render as links to the user.
**Tests:** tables for each kind, the self rule, the edit rule, a mention of a user who cannot read the room.
**Done when:** the standard checks pass.

### 11.2 The bell and the notifications page
**Goal:** users see their notifications live.
**Read:** SPEC §7.9, §8.
**Tasks:**
1. A bell with the unread count in the layout, live through a per-user stream; `/notifications` lists them, newest first, with links, and marks them read.
**Tests:** handler tests.
**Browser tests:** alice mentions bob; bob's count goes up without reload.
**Proof:** method A: `11.2-notifications.png`.
**Done when:** the standard checks pass and the proof is verified.

---

## M12: Private messages

### 12.1 Conversations and messages service
**Goal:** private messages follow SPEC §7.11.
**Read:** SPEC D28, §6.3, §7.4, §7.5, §7.11, §8.
**Tasks:**
1. `Messages`: `StartDirect(ctx, actor, userID)` (returns the existing one if any), `StartGroup(ctx, actor, userIDs, name)`, `AddMembers`, `Rename`, `Leave`, `Conversations(ctx, actor)` (most recent first, last message, unread), `History(ctx, actor, convID, before, limit)`, `Send(ctx, actor, convID, input)` (body, reply-to, attachments through `internal/files`), `Edit`, `Delete`, `MarkRead`, `UnreadCount`, reactions as in step 8.1. Every method checks membership; non-members (admins too) get `TagNotFound`.
2. Live events on `conversation:<id>` and `user:<id>` after commit. No audit events (SPEC §7.11).
3. `clowder demo` adds a direct conversation (alice–bob) and a group (alice, bob, carol).
**Tests:** permission tables (member, left member, non-member, admin non-member, disabled user), the direct pair is unique, group limit, read state and unread count, delete removes attachments.
**Done when:** the standard checks pass.

### 12.2 Messages pages
**Goal:** users write and read private messages live.
**Read:** SPEC §7.11, §8.
**Tasks:**
1. A "Messages" link in the sidebar with the live unread count; `/messages` lists the conversations; `/messages/{id}` shows a conversation (oldest at the top, newest at the bottom, older loaded on scrolling up, "Seen" in direct conversations), a composer (text, attachments, reply-to quote), edit, delete, reactions, the members panel (add, rename, leave for groups). "Message" buttons on users' names start a direct conversation.
2. The stream `GET /messages/{id}/stream` and the per-user stream keep the conversation and the count live.
**Tests:** handler tests, 404 for non-members.
**Browser tests:** alice sends bob a message; bob's open conversation shows it; with it closed, bob's unread count goes up.
**Proof:** method A: `12.2-messages.png`, the alice–bob conversation as `bob` with a reply and a reaction.
**Done when:** the standard checks pass and the proof is verified.

---

## M13: 1.0

### 13.1 Performance
**Goal:** the targets of SPEC §10 are measured.
**Read:** SPEC §10.
**Tasks:**
1. `clowder demo -size large` makes the data set of §10.
2. Go benchmarks for the feed page and search; `scripts/perf.sh` measures the stream fan-out and the idle memory with 200 streams. Fix what misses a target, or record it.
3. `docs/PERFORMANCE.md`: the machine, the commands, the numbers, the targets not met.
**Done when:** the numbers are recorded and the standard checks pass.

### 13.2 Security review and README
**Goal:** the release is reviewed and documented.
**Read:** SPEC §7, §9, §11, §17. (Private messages included.)
**Tasks:**
1. Review every route for the permission checks, cookie and header settings, upload handling, markdown output, the login limiter; fix what is found with tests.
2. `README.md`: what Clowder is, building, the first admin, configuration, running behind a TLS reverse proxy (an example for Caddy and nginx), backups (the data directory, with SQLite's online backup), screenshots from the proofs.
**Done when:** the standard checks and the browser tests pass.

### 13.3 Release 1.0
**Goal:** version 1.0.0 is published.
**Tasks:**
1. Run every check and every browser test; update the SPEC's status to 1.0.
2. Publish the step, then make and push the annotated tag `v1.0.0` on it (CLAUDE.md "Git and GitHub").
**Done when:** `v1.0.0` is on `origin`.
