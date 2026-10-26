# Implemented steps

This is the log of finished steps from `docs/IMPLEMENTATION.md`, newest at the bottom. An agent adds an entry on the step's branch, as part of the step's commit (`docs/IMPLEMENTATION.md`, rules 10–12). The next step to do is the first step in `docs/IMPLEMENTATION.md` that has no entry here.

## Entry format

Copy this block, fill it in, and keep every heading. Write "none" where something doesn't apply.

```markdown
## <id> <step title>

- **Date:** YYYY-MM-DD
- **Branch:** feat/step-<id>-<slug>
- **Summary:** 2–4 sentences on what now exists and works.
- **Main files:** the packages or files created or changed.
- **Tests:** what was added, and the number of test cases.
- **Checks:** gofmt ✅ · go vet ✅ · golangci-lint ✅ · go test -race ✅ · step's own checks ✅/none
- **Proof:**
  [<id> <name>](implemented-proof-images/<id>-<name>.<ext>) (or `![…](…)` for an image)
  One sentence describing what the proof shows, as you verified it by looking at it.
- **Manual checks:** the user's answers to the step's manual checks, or "none".
- **Deviations and follow-ups:** differences from SPEC (also recorded in SPEC), and TODOs left for later steps.
```

---

<!-- Entries start below this line. -->

## 0.1 Module and package layout

- **Date:** 2026-10-26
- **Branch:** feat/step-0.1-module-and-package-layout
- **Summary:** The module `github.com/rm4n0s/clowder` (go 1.27) exists with every package of SPEC §6.2, each with a `doc.go`; `cmd/clowder-proof` and `internal/browsertest` carry the `clowder_browser` tag. `internal/config` has the settings of SPEC §11 with their defaults, the secure-cookie rule and `Validate` with tagged errors from `rm4n0s/errors` v0.2.0.
- **Main files:** `go.mod`, `internal/config/config.go`, `*/doc.go`, `cmd/clowder/main.go`, `cmd/clowder-proof/main.go`, `scripts/README.md`.
- **Tests:** `internal/config`: 4 tests, 31 cases with subtests (every tag with its route, the metadata, the secure-cookie rule). Checked by hand that revive flags an undocumented exported function.
- **Checks:** gofmt ✅ · go vet ✅ · golangci-lint ✅ · go test -race ✅ · CGO_ENABLED=0 build ✅ · go generate clean ✅ · go vet -tags clowder_browser ✅
- **Proof:** none
- **Manual checks:** none
- **Deviations and follow-ups:** `BaseURLInvalid` covers both a URL that does not parse (`NewErr`) and one that is not absolute http(s) (`New`). Flags and environment variables come in step 0.3.

## 0.2 Dependencies, templ and static files

- **Date:** 2026-10-26
- **Branch:** feat/step-0.2-dependencies-templ-static
- **Summary:** Every dependency of SPEC D6–D10 and D23 is in `go.mod` (gorm v1.31.2, glebarez/sqlite v1.11.0, datastar-go v1.2.2, goldmark v1.8.6, x/crypto v0.58.0, chromedp v0.21.0, templ v0.3.1100 as runtime and `tool`); the unused ones are held by blank imports in `deps.go` files naming the step that will use them, chromedp only under the `clowder_browser` tag. `datastar.js` v1.0.2 with its MIT license, `app.css` and the `Layout`/`Home` templ views are embedded and generated.
- **Main files:** `go.mod`, `NOTICE`, `internal/web/static/` (`datastar.js`, `datastar.LICENSE`, `app.css`, `static.go`), `internal/web/views/` (`layout.templ`, `home.templ`, `generate.go`, generated `*_templ.go`), `internal/*/deps.go`.
- **Tests:** `views.TestHome` (9 cases), `static.TestFS` (2 cases).
- **Checks:** gofmt ✅ · go vet ✅ · golangci-lint ✅ (also with `--build-tags clowder_browser`) · go test -race ✅ · CGO_ENABLED=0 build ✅ (chromedp not in the default build) · go generate clean ✅
- **Proof:** none
- **Manual checks:** none
- **Deviations and follow-ups:** The SDK does not name its client version. Client v1.0.2 was matched to SDK v1.2.2 by release dates and by `viewTransitionSelector`, which both have and v1.0.1 lacks (recorded in SPEC D7). The SQLite driver brings `modernc.org/sqlite` v1.23.1, the version glebarez/sqlite pins.
