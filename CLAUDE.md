# CLAUDE.md

Guidance for Claude Code when working in this repository (Go, hosted on GitHub).

## Project
Clowder is a Go project (module `github.com/rm4n0s/clowder`, Go 1.27, BSD-3-Clause). _What it is, who uses it and what it targets is not written yet: see SPEC §1–§3 and the open questions in SPEC §16._

**`docs/SPEC.md` is the source of truth.** Read the sections that cover a task before starting it. If the code and the spec disagree, or a task needs a decision the spec doesn't make, ask instead of guessing. When a decision changes, update the spec on the same branch.

## Commands
- Build: `go build ./...`
- Test all: `go test -race ./...`
- Test one package: `go test ./<pkg> -run TestName -v`
- Vet: `go vet ./...`
- Lint: `golangci-lint run`
- Format: `gofmt -w .` (or `goimports -w .`)
- Generate: `go generate ./...`
- After changing dependencies: `go mod tidy`

## Definition of done
Before reporting a task as finished, run `gofmt`, `go vet ./...`, `golangci-lint run` and `go test -race ./...`, and fix anything they report. Say so explicitly if any step was skipped or failed. Also:
- Every new exported identifier has a doc comment
- Every new error tag has a route test (see "Error handling")

## Layout
_The package layout is decided in SPEC §6 and copied here once it is._
- `cmd/<name>/` holds the commands; `internal/` holds what is not public API; `examples/<name>/` holds runnable examples (public API only); `scripts/` holds helper scripts
- `docs/SPEC.md` is the specification; `docs/IMPLEMENTATION.md` is the step-by-step plan; `docs/IMPLEMENTED.md` logs finished steps; `docs/implemented-proof-images/` holds each step's proof images
- Tests live next to the code (`foo_test.go`); test data lives in `testdata/` of its package

## Go conventions
- Never ignore a returned error; handle it or return it
- Pass `context.Context` as the first parameter of anything that does I/O or may block. Name it `ctx`
- Tests are table-driven
- No global mutable state. Package-level read-only values are fine and are never reassigned. Dependencies are passed through constructors
- Prefer the standard library. Allowed dependencies: `github.com/rm4n0s/errors`. Ask before adding any other
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
- **Exception:** if `docs/IMPLEMENTATION.md` exists, follow its Rules section for the steps it lists. Each step is done on its own branch, one at a time, and a finished step (all Definition-of-done checks passing) is published to `main` by a fast-forward merge and push, without a PR, after its entry is added to `docs/IMPLEMENTED.md` on that branch. Never commit work for a step directly on `main`. Everything not covered by that file (other changes, fixes outside the listed steps) still follows the rule above
- **Tags:** after a finished step is pushed to `main`, tag its commit with an annotated tag `step-<id>` (e.g. `step-2.3`) and push the tag; the step counts as finished when the tag is on `origin`. Never move or delete a tag. When a version is finished (its release step, or a fix step that closes a patch version), publish it to `main` and make and push its annotated release tag (`v1.0.0` and the like) on that commit at once, without asking
- Commit messages: imperative mood, e.g. "Add retry to client"
- PRs: short description, link the issue (`Closes #123`), keep them small
- No `.github/` directory and no GitHub Actions workflows: never add them. The Definition-of-done checks are run locally

## Working through the steps
- Never stop to ask for confirmation, and never end a turn with a question such as "Should I start the next step?", until every step in `docs/IMPLEMENTATION.md` is finished (through the last one). After a step is published, report it briefly and start the next step in the same turn. The cases where a step must stop and ask are in `docs/IMPLEMENTATION.md`, rule 7
- A request such as "publish to main" that is already satisfied gets a one-line answer, then work continues with the next step
- **Manager and workers:** the main session (Opus 5.5, high effort) manages and Sonnet 5.5 agents at high effort write the code (Agent tool with `model: "sonnet"`, `effort: "high"`). The manager reads the step and the SPEC, writes the brief, launches and steers the workers, reviews their diffs, runs the checks, and does the git work; it writes only small fixes itself. The details are in `docs/IMPLEMENTATION.md`, Rules, "Who does the work"

## Don't
- Don't edit generated files (`*_gen.go`); regenerate them with `go generate ./...`
- Don't commit secrets or `.env` files
- Never look for, list, open or read files outside this project folder (no home-directory databases, configs, logs or other projects). If something outside it is needed, ask the user to provide it. Exception: dependency source in the Go module cache

## Testing notes
- Tests run without network access or external services unless a build tag says otherwise; a fake replaces each external dependency
- Every error Tag has a route test
