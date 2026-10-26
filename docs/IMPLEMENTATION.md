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
- **Questions.** A worker that hits a case of rule 7 stops and reports it; the manager decides what the plan and the SPEC already decide, and asks the user otherwise (rule 7). The manager answers "what is going on" questions of the user while workers run, and never reports a worker's result before it arrived.
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
7. **Stop and ask the user** when any of these happens:
   - the step is ambiguous
   - the spec looks wrong or contradicts itself
   - a tool is missing (give the exact install command, e.g. `sudo dnf install <package>`)
   - a new dependency seems necessary
   - something needs `sudo`
   - a check still fails after a real attempt to fix it

   Never weaken or delete a test, skip a check, or mark a step done just to get unblocked.

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
   go test -race ./...
   ```
   If the step lists more checks (tests behind a build tag, a cross-compile), run them too.
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
    - A step counts as finished only when its commit is on `origin/main`.
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
    If `--ff-only` fails, rebase the step branch onto `main`, run the standard checks again, and retry. If the push is rejected or fails (authentication, network), stop and tell the user. Don't start the next step until the push succeeded.
13. **Report to the user.** Give the step id, a two-line summary, the check results, the proof paths, and the commit hash now on `main`.

**Default "Done when".** A step without a **Done when** line is done when its Tasks are complete, its **Tests** are written and passing, its **Proof** is verified, and the standard checks pass.

### Proof methods

_To be filled in once SPEC §16 decides how a step proves it works. A likely start:_

| Method | Use it for | How |
|---|---|---|
| **A. Command output** | anything a command or an example prints | run the command named in the step and save its output with `… > docs/implemented-proof-images/<id>-<name>.txt 2>&1` |

### Examples

- Public examples live in `examples/<name>/main.go` and use **only public packages**, the way a user would.
- Every example starts with a comment saying what it shows and how to run it (`go run ./examples/<name>`).
- An example step is done only when the example runs, its proof is verified, and the standard checks pass.

---

## Milestone overview

| Milestone (SPEC §13) | Steps | Examples |
|---|---|---|
| M0 Skeleton | 0.1 | |
| M1… | _written after SPEC is approved_ | |

---

## M0: Skeleton

### 0.1 Module and package layout
**Goal:** an empty but complete repository skeleton that passes all checks.
**Read:** SPEC §6.2, §9.3, §15; CLAUDE.md "Layout" and "Error handling".
**Tasks:**
1. `go mod init github.com/rm4n0s/clowder`, set `go 1.27`.
2. Create every package directory in SPEC §6.2. Give each one a `doc.go` whose package comment states its purpose.
3. Add `github.com/rm4n0s/errors` v0.2.0 to `go.mod` with its first use.
4. Check that `.golangci.yml` flags an undocumented exported function (by hand, then remove the test function).
5. Create `scripts/` (empty except a `README.md` listing the scripts to come), if SPEC §6.2 lists any.
**Done when:** the standard checks pass.
