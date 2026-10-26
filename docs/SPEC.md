# Clowder: Specification

| | |
|---|---|
| Status | Draft 0.0 (skeleton: the project is not described yet) |
| Date | 2026-10-26 |
| Owner | Manos Ragiadakos |
| Module | `github.com/rm4n0s/clowder` |
| License | BSD-3-Clause |

_One paragraph on what Clowder is, who it is for, and what each version adds. To be written once the questions in §16 are answered._

---

## 1. Goals

| # | Goal |
|---|---|
| G1 | _TBD_ |

## 2. Non-goals for 1.0

- _TBD_

## 3. Scope of 1.0

_TBD: what 1.0 does, as a list a user can check._

## 4. Decisions log

| # | Decision | Notes |
|---|---|---|
| D1 | **Go 1.27**, the standard toolchain, no cgo unless a later decision allows it | §11 |
| D2 | **`github.com/rm4n0s/errors` v0.2.0** for every error, used the way its `AI_GUIDE.md` says | The project owner's requirement. §9 |
| D3 | Dependencies: the standard library first. Every other dependency is a decision recorded here | CLAUDE.md "Go conventions" |
| D4 | The work is done in numbered steps (`docs/IMPLEMENTATION.md`), each published to `main` with a `step-<id>` tag, by a managing session and worker agents | CLAUDE.md "Working through the steps" |
| D5 | No CI on GitHub: the checks run locally | CLAUDE.md "Git and GitHub" |

## 5. Size estimate

_TBD_

## 6. Architecture

### 6.1 Components

_TBD_

### 6.2 Package layout

_TBD: every package, with one line on what it holds. CLAUDE.md "Layout" copies this once it is decided._

## 7. Public API

_TBD: the types and functions a user calls, and the conventions they follow (constructors, options, naming)._

## 8. Concurrency and lifecycle

_TBD: which goroutines exist, who owns them, how they stop (CLAUDE.md "Go conventions")._

## 9. Errors and diagnostics

### 9.1 Public error tags

_TBD: the tags a user of the module matches on, exported as `TagX` constants._

### 9.2 Logging

Every package logs through a `*slog.Logger` it is given. An error is logged as the attribute `"err"`.

### 9.3 How errors are made (`rm4n0s/errors` v0.2.0)

Every error of the module is made, returned, logged and tested the way `AI_GUIDE.md` of `github.com/rm4n0s/errors` says (D2). The guide is in the module cache (`$(go env GOMODCACHE)/github.com/rm4n0s/errors@v0.2.0/AI_GUIDE.md`) and is read in full before error code is written. The rules, with the guide's numbers:

| Rule | What the code does | Why |
|---|---|---|
| R1 | `errors.New(tag, message, metadata...)` always has a tag and a message | the tag names the one thing a function cannot do; tests and callers match on it |
| R2 | the metadata is one list of `"key", value` pairs with string keys: given to `errors.New`, or in exactly one `SetMetadata` after `NewErr` and `NewErrf`; a later `SetMetadata` only adds data that becomes known in another function | a key that is not a string is stored as `arg<index>` and a missing value as `nil`; both are bugs |
| R3 | an error is tagged once, where it first appears. An error of this module goes up as it is (`return err`). An error from outside it (standard library, third-party packages) is tagged where it first appears with `NewErr`. A failure that the code finds itself is `errors.New` | a second tag or a `fmt.Errorf` hides the first tag and cuts the route; `errors.FromError` is a plain type assertion |
| R4 | the constructor is called in the function that fails, not in a helper that builds the error | the route is the call stack at the constructor: a helper adds a frame that is not the failing function |
| R5 | a tag is a constant next to the code that produces it; code compares `Tag` with that constant or uses `errors.Is` with a sentinel (`&errors.Error{Tag: TagX}`), never a string literal or the message; no package-level `errors.New` | messages are for people and change; a package-level `New` has the route of `init` |
| R6 | only `github.com/rm4n0s/errors` is imported (it re-exports `Is`, `As` and `Unwrap`); the standard library's package, when `Join` is needed, is imported as `stdErrors` | one import, one behavior |
| Logging | an error is logged with `slog` as `"err", err`; `*errors.Error` is a `slog.LogValuer` (keys `Tag`, `Message`, `Route`, `OriginalErr`, `Metadata`, `StackFrames`), and the entry unmarshals into `errors.ErrorJson` and replays with `errors.FromJson`. `ToJson` and `json.Marshal` are not used to log | the route in the entry is cut at the function that logs |
| Tests | each tag has a test with `HasRoute("Outer->Inner.Tag")` (from the entry function that the test calls, with the tag), and the metadata where it matters | the test shows which path made the error |

## 10. Performance targets

_TBD, or "none" if the project has none._

## 11. Build and runtime requirements

- Go 1.27 or later, the standard toolchain
- _TBD: target OSes and architectures, external programs or services needed at run time_

## 12. Testing strategy

- Unit tests are table-driven and live next to the code
- Tests need no network and no external service; each external dependency has a fake. Tests that need the real thing carry a build tag (named here when the first one is added) and are skipped by the default run
- Every error tag has a route test (§9.3)
- The checks of `docs/IMPLEMENTATION.md` rule 8 run before every step is published

## 13. Milestones

Each milestone is broken into steps in `docs/IMPLEMENTATION.md` (written after this spec is approved) and follows the workflow in CLAUDE.md.

| # | Milestone | Done when |
|---|---|---|
| M0 | Skeleton: `go.mod`, package layout, `.golangci.yml`, `rm4n0s/errors` | all the checks pass |
| M1… | _TBD_ | |

## 14. Examples (`examples/`)

_TBD_

## 15. Licensing and third-party code

Clowder is BSD-3-Clause (`LICENSE`). _TBD: any third-party code that is ported or vendored, with its license, and the NOTICE file if one is needed._

## 16. Open decisions

New ones are added here with a recommendation and moved to §4 once decided.

| Decision | Recommendation |
|---|---|
| What is Clowder? What problem does it solve, and for whom? | _Owner to answer_ |
| What kind of project is it: a library, a command-line tool, a service, or a mix? | _Owner to answer_ |
| Which OSes and architectures does it target? | Linux first, unless it needs to be cross-platform from the start |
| Which dependencies besides `rm4n0s/errors` are allowed? | Only the standard library until a step needs more |
| Is cgo allowed? | No (D1), unless a dependency forces it |
| What does 1.0 contain, and what comes after it? | _Owner to answer_ |
| How does a step prove it works (proof images, command output, a recorded run)? | Output of a command or an example, saved in `docs/implemented-proof-images/` when it is an image |
| Is there an upstream project whose behavior Clowder follows (as glutter follows Flutter)? If so, how is it pinned and where are its sources read from? | _Owner to answer_ |

## 17. Risks

| Risk | Mitigation |
|---|---|
| _TBD_ | |
