# CLAUDE.md

Guidance for Claude Code when working in this repository (Go, hosted on GitHub).

## Project
Clowder (module `github.com/rm4n0s/clowder`, Go 1.27, BSD-3-Clause) is a self-hosted web application where a team talks in rooms. A room is a feed of posts with threaded comments; posts and comments carry text or markdown, attachments, tags and tasks, and each room's tasks make its Kanban board. Users also write private messages to each other, one to one or in groups. People join by invitation only. It is one cgo-free binary: GORM on SQLite (pure-Go driver) or PostgreSQL (pgx) for storage, pages rendered on the server with templ, and kept live with Datastar over server-sent events. From 1.1, several processes can share one PostgreSQL database with Redis carrying the live events, and a REST API with a WebSocket (`/api/v1`, described by `internal/api/openapi.json`) serves desktop clients. 1.2 adds a settings page, an SSH server that hands one-time sign-in codes to the owners of registered keys (a second factor), video calls in private messages (a pion SFU), code highlighting, unread counts, and a new look for phones and desktops.

**`docs/SPEC.md` is the source of truth.** Read the sections that cover a task before starting it. If the code and the spec disagree, or a task needs a decision the spec doesn't make, ask instead of guessing. When a decision changes, update the spec on the same branch.

## Commands
- Build: `go build ./...`. Clowder is cgo-free; `CGO_ENABLED=0 go build ./...` must always work
- Test all: `go test -race ./...`
- Test one package: `go test ./<pkg> -run TestName -v`
- Vet: `go vet ./...`
- Lint: `golangci-lint run`
- Format: `gofmt -w .` (or `goimports -w .`); templ files: `go tool templ fmt .`
- Generate (the templ views): `go generate ./...`; the generated `*_templ.go` files are committed
- Run: `go run ./cmd/clowder serve`; first admin: `go run ./cmd/clowder invite -admin`; demo data: `go run ./cmd/clowder demo`
- Browser tests (need Chromium): `go test -tags clowder_browser ./...`
- Proof screenshot of a page: `go run -tags clowder_browser ./cmd/clowder-proof -as <user> -page <path> -o docs/implemented-proof-images/<id>-<name>.png`; `-page` takes `{room:<name>}`, `{board:<name>}` (the board of the room), `{post:<text>}` and `{conversation:<user>,<user>}` (the conversation with exactly those members, found in the list of the `-as` user), `-fill sel=value`/`-click sel` act on the page, and `-other <user>` with `-other-page`/`-other-fill`/`-other-click` acts as a second user in another browser before the screenshot (live updates); `-api-login <client name>` also logs the `-as` user in through `POST /api/v1/auth/login` as a client of that name before the page opens and leaves that session in place; `-cluster` (with `-postgres-url`/`-redis-url` or `CLOWDER_TEST_POSTGRES`/`CLOWDER_TEST_REDIS`) starts two servers on one new PostgreSQL database and one Redis, and the `-other` user acts on the first while the screenshot is taken on the second; `-calls` (with `-ring-timeout`) starts the media server of the calls and gives the browsers fake devices, so that `-other-click '[data-call-start="video"]'` and `-click '[data-call-answer="video"]'` make a call
- API client check (needs a running server): `go run ./cmd/clowder-apicheck -base-url http://localhost:8080 -username alice -password …`; it logs in, calls the API and prints each request and answer (tokens as `<token>`)
- After changing dependencies: `go mod tidy`
- Tests on PostgreSQL and Redis (needs docker or podman): `scripts/test-docker.sh` (passes its arguments to `go test`); one package: `scripts/test-docker.sh ./internal/store -run TestName -v`
- Docker: `docker compose up -d` (Clowder + PostgreSQL); with two processes, Redis and Caddy: `docker compose -f compose.yaml -f compose.cluster.yaml up -d`; smoke test: `scripts/compose-smoke.sh` (`-cluster`)
- API smoke client: `go run ./cmd/clowder-apicheck -base-url http://localhost:8080 -username <u> -password <p>`

## Definition of done
Before reporting a task as finished, run `gofmt`, `go vet ./...`, `golangci-lint run`, `go test -race ./...` and (from step 14.2) `scripts/test-docker.sh`, and fix anything they report. Say so explicitly if any step was skipped or failed. Also:
- `CGO_ENABLED=0 go build ./...` passes
- `go generate ./...` leaves no change in the tree (the committed `*_templ.go` files are up to date)
- Every new exported identifier has a doc comment
- Every new error tag has a route test (see "Error handling")
- Every new service method has a permission test (SPEC §12)
- Every new or changed API route is in `internal/api/openapi.json` (the OpenAPI test fails otherwise, SPEC D45)

## Layout
The packages are listed in SPEC §6.2; the layers in SPEC §6.1.
- `cmd/clowder/` is the binary (`serve`, `invite`, `demo`) and wires everything together
- `internal/store` is the only package that imports GORM or knows the database (SQLite or PostgreSQL: what differs stays behind its `dialect`); `internal/app` holds the services and every permission check, and never imports `net/http` or templ; `internal/web` holds the handlers, middleware and SSE streams, and never imports `internal/store`
- `internal/api` holds the REST API and the WebSocket for desktop clients, their JSON types and the embedded `openapi.json`; like `internal/web` it never imports `internal/store`, and it never imports templ
- `internal/cluster` is what several processes share through Redis (the event bridge, the login counters); the only package that imports go-redis
- `internal/sshcode` (from 1.2) is the SSH server that hands out sign-in codes; the only package that runs an SSH server, and it never imports `internal/store`. `internal/metrics` (from 1.2) holds the Prometheus counters; `internal/calls` (from 1.2) is the WebRTC server of the calls, the only package that imports pion, and it never imports `internal/store`
- `internal/web/views` holds the templ components; `internal/web/static` holds the embedded `datastar.js`, CSS and scripts
- `internal/live` is the event hub; `internal/markdown`, `internal/files`, `internal/auth`, `internal/config` and `internal/clock` are what their names say
- `docs/SPEC.md` is the specification; `docs/IMPLEMENTATION.md` is the step-by-step plan; `docs/IMPLEMENTED.md` logs finished steps; `docs/implemented-proof-images/` holds each step's proofs
- `data/` (git-ignored) is the default data directory of a local run
- Tests live next to the code (`foo_test.go`); test data lives in `testdata/` of its package

## Go conventions
- Never ignore a returned error; handle it or return it
- Pass `context.Context` as the first parameter of anything that does I/O or may block. Name it `ctx`
- Tests are table-driven
- No global mutable state. Package-level read-only values are fine and are never reassigned. Dependencies are passed through constructors
- Prefer the standard library. Allowed dependencies: `github.com/rm4n0s/errors`, `gorm.io/gorm`, `github.com/glebarez/sqlite`, `github.com/starfederation/datastar-go`, `github.com/a-h/templ`, `github.com/yuin/goldmark`, `golang.org/x/crypto`, `gorm.io/driver/postgres` (with its `pgx`), `github.com/redis/go-redis/v9`, `github.com/coder/websocket`, `github.com/alecthomas/chroma/v2` (code highlighting, from 1.2), `github.com/pion/webrtc/v4` with the pion modules it requires (calls, from step 22.1, only in `internal/calls`), `golang.org/x/vuln` (only as the `govulncheck` tool, from step 23.3) and, for browser tests and proofs only, `github.com/chromedp/chromedp` (SPEC §4). Ask before adding any other
- **No cgo**: never write `import "C"` or add a dependency that needs it (the SQLite driver is the pure-Go one)
- The SDK `datastar-go` and the embedded `datastar.js` are the same release; never upgrade one without the other
- Routes use the standard `http.ServeMux` patterns (`"POST /rooms/{id}/posts"`)
- A service method takes `ctx` and the acting user first, checks the permission itself, does its writes in one transaction, and publishes its live events only after the commit (SPEC §6.1, §8)
- A user who may not see a private room gets "not found", never "forbidden" (SPEC §9.1)
- Every create, update and delete of posts, comments, tasks, board columns and room memberships writes its audit event in the same transaction (SPEC §7.10). Nothing edits or deletes an audit event
- Never log passwords, tokens or post bodies
- Log with `log/slog`; a logger is passed in, never taken from a package-level variable
- Every goroutine has an owner that stops it (through a `context.Context` or a `Close`) and waits for it to end

## Error handling
Use `github.com/rm4n0s/errors` (v0.2.0) for all errors, the way its `AI_GUIDE.md` says. This is a requirement of the project owner: read the whole guide (it is in the module cache, `$(go env GOMODCACHE)/github.com/rm4n0s/errors@v0.2.0/AI_GUIDE.md`) before you write or change code that makes, returns, logs or tests an error, and again when the dependency version changes. SPEC §9.3 lists the rules with the reasons. Never use `fmt.Errorf`, and never import the standard library `errors` package under the name `errors`: this package re-exports `errors.Is`, `errors.As` and `errors.Unwrap`, so a single import covers everything (if `Join` is needed, import the standard one as `stdErrors "errors"`).

**Creating errors**
- New failure with a fixed message: `errors.New("Tag", "message", "key", value, ...)`. It always has a tag and a message
  - Tag: short PascalCase name of the thing the function cannot do, e.g. `ConfigLoadFailed`. Declare it as a constant next to the code that produces it (`const tagConfigLoadFailed = "ConfigLoadFailed"` at the end of the file; exported as `TagX`, with a doc comment, when another package matches on it)
  - Trailing arguments are key/value metadata pairs with string keys, e.g. `"path", name`. Give all of them in this one call; never chain `SetMetadata` after `errors.New`
- New failure with a formatted message: `errors.NewErrf("Tag", "format %s", arg)`
- Wrapping an error that comes from outside this module (stdlib, any third-party package): `errors.NewErr("Tag", err)`, once, where it first appears. `NewErr` and `NewErrf` take no metadata: add it with one `.SetMetadata("key", value, "key2", value2)` call. `SetMetadata` merges, so a later call in another function adds keys; `RemoveMetadata` and `SetMessage` change an error that is already made. `NewErr(tag, nil)` panics, so call it only inside `if err != nil`
- Call the constructor in the function that fails, never in a helper that builds and returns the error: the route is the call stack at the constructor, and a helper adds a frame that is not the failing function. An exported function or method is not a helper
- Every `New`, `NewErr` and `NewErrf` call captures the stack at that point
- A sentinel for `errors.Is` is a struct literal, `&errors.Error{Tag: TagX}`. Never write `var ErrX = errors.New(...)` at package level: its route is `init`

**Propagating errors**
- Every error is tagged exactly once. If the error came from a function of this module, return it untouched: `return err`. Re-wrapping it (`NewErr`, `NewErrf` with `%w`, `fmt.Errorf`) would replace the Tag and the stack and break route tests; the route already shows who called whom, so no context is added by wrapping
- Functions return the plain `error` interface, never `*errors.Error` (avoids the non-nil-interface-holding-nil-pointer bug)
- `errors.FromError(err)` is a plain type assertion. It fails if something else wrapped the error, so don't wrap these errors with anything

**Inspecting errors**
- `appErr, ok := errors.FromError(err)`, then use `appErr.Tag`, `appErr.Metadata`, `appErr.Message`
- Branch on the tag constant (`appErr.Tag == pkg.TagX`) or on `errors.Is(err, sentinel)` (it matches by Tag), never on a string literal, `Message` or `Error()`
- Use `errors.Is` / `errors.As` / `errors.Unwrap` for the underlying cause stored in `OriginalErr`

**Logging errors**
- Log with `slog` and pass the error as the attribute `"err"`: `logger.Error("what failed", "err", err)`. `*errors.Error` is an `slog.LogValuer`: the entry has `Tag`, `Message`, `Route`, `OriginalErr`, `Metadata` and `StackFrames`, and a test replays it with `json.Unmarshal` into `errors.ErrorJson` and `errors.FromJson`. Do not call `ToJson` or `json.Marshal` on an error to log it
- Metadata values must be JSON-serializable (strings, numbers, bools, simple structs); after a round trip numbers are `float64`

**Testing errors**
- Every new failure case gets its own unique Tag and a test that asserts the route, not just that an error occurred:
  ```go
  appErr, ok := errors.FromError(err)
  if !ok {
      t.Fatalf("expected *errors.Error, got %T", err)
  }
  if !appErr.HasRoute("Server.Start->Config.Load.ConfigLoadFailed") {
      t.Errorf("unexpected failure path: %s", appErr.Route())
  }
  ```
- A route lists the call chain from outermost to innermost (`Type.Method` or `Func`, joined by `->`, no package names) and ends with the Tag. `Route` and `HasRoute` stop at the function that called them, so in a test the route starts just below the test function. `HasRoute` matches by substring: give the path from the entry function you call down to `.Tag`, so the tag is checked too. Print `appErr.Route()` when an assertion fails

## Git and GitHub
- Never commit or push directly to `main`; work on a branch named `feat/...` or `fix/...`
- **Exception:** if `docs/IMPLEMENTATION.md` exists, follow its Rules section for the steps it lists. Each step is done on its own branch, one at a time, and a finished step (all Definition-of-done checks passing) is merged into the local `main` by a fast-forward merge, without a PR, after its entry is added to `docs/IMPLEMENTED.md` on that branch. **Only finished milestones are pushed to GitHub** (the owner's rule, 2026-10-27): when the last step of a milestone is merged, `main` and the tags are pushed; steps in between stay local. Never commit work for a step directly on `main`. Everything not covered by that file (other changes, fixes outside the listed steps) still follows the rule above
- **Tags:** after a finished step is merged into the local `main`, tag its commit with an annotated tag `step-<id>` (e.g. `step-2.3`). When the milestone's last step is merged, also tag it `milestone-M<n>` (e.g. `milestone-M4`) and push `main` with all tags; the milestone counts as finished when that tag is on `origin`. Never move or delete a tag. When a version is finished (its release step, or a fix step that closes a patch version), publish it to `main` and make and push its annotated release tag (`v1.0.0` and the like) on that commit at once, without asking
- Commit messages: imperative mood, e.g. "Add retry to client"
- PRs: short description, link the issue (`Closes #123`), keep them small
- No `.github/` directory and no GitHub Actions workflows: never add them. The Definition-of-done checks are run locally

## Working through the steps
- **Never stop** until every step in `docs/IMPLEMENTATION.md` is finished (through the last one): the owner's rule, 2026-10-26. Never stop to ask for confirmation, never end a turn with a question such as "Should I start the next step?", and never stop because something failed. After a step is merged and tagged, report it briefly and start the next step in the same turn; after a milestone is pushed, report the milestone. When a step needs a decision, a tool, a dependency or a fix, follow `docs/IMPLEMENTATION.md` rule 7: decide, record, and keep going. A failed milestone push is retried and never blocks the next step (rule 12)
- A request such as "publish to main" that is already satisfied gets a one-line answer, then work continues with the next step
- **Manager and workers:** the main session (Opus 5.5, high effort) manages and Sonnet 5.5 agents at high effort write the code (Agent tool with `model: "sonnet"`, `effort: "high"`). The manager reads the step and the SPEC, writes the brief, launches and steers the workers, reviews their diffs, runs the checks, and does the git work; it writes only small fixes itself. The details are in `docs/IMPLEMENTATION.md`, Rules, "Who does the work"

## Don't
- Don't edit generated files (`*_templ.go`, `*_gen.go`); change the `.templ` source and regenerate with `go generate ./...`
- Don't render user content as raw HTML except the output of `internal/markdown` (goldmark in safe mode, SPEC D9)
- Don't commit `data/` or any database file
- Don't commit secrets or `.env` files
- Never look for, list, open or read files outside this project folder (no home-directory databases, configs, logs or other projects). If something outside it is needed, ask the user to provide it. Exception: dependency source in the Go module cache

## Testing notes
- The default tests run without network access, a browser or external services; a fake replaces each external dependency. `CLOWDER_TEST_POSTGRES` and `CLOWDER_TEST_REDIS` (set by `scripts/test-docker.sh`) run the same tests on PostgreSQL and the Redis tests against real Redis
- Browser tests carry the `clowder_browser` build tag and use `internal/browsertest`; `chromedp` is imported only in files with that tag
- Store and service tests use a fresh SQLite database per test (`storetest.Open`: a file copy of a database migrated once), the fake clock (`internal/clock`) and a recording hub; independent tests call `t.Parallel()`
- Handler tests run the real handlers with `httptest`, logged in, and check the status, the HTML and the Datastar SSE patches
- Every service method has a table of who may call it (admin, member, room member, non-member of a visible room, non-member of a private room, disabled user)
- Every error Tag has a route test
- Every audited action has a test of the event it writes, and of no event when the change fails
