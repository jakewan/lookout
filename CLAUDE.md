# lookout — Agent Guide

This file orients an AI agent (or a new contributor) working in this repository. It is self-contained: everything needed to work here is described below or in the linked in-repo files.

## What lookout is

A command-line tool that gives one vantage point over everything you have in flight. It gathers recent activity — git branches and worktrees, GitHub pull requests and issues, AI coding sessions, and files supplied by other tools such as an issue-tracker exporter — groups it into workstreams you define, and reports how recently each one moved and whether every input it relies on is healthy.

It is the data layer only. It emits a readout for a renderer to consume and does not draw one itself.

## Status and layout

Early. `lookout read --json` is the only command, and it reads supplied files only. The git, GitHub, and session adapters, the commands that edit the configuration or write state, and the baseline snapshot are not built yet.

```
cmd/lookout/              # binary entry point and its behavior specs
schema/                   # the published contracts, embedded so lookout enforces the same files
internal/config/          # locating and loading one setting's configuration file
internal/supplied/        # reading a supplied file and judging its input's health
internal/health/          # the health record every input reports
internal/readout/         # assembling the readout
internal/devtools/sweep/  # disclosure sweep run before every push; not part of the binary
```

A change to a file under `schema/` changes a published contract: suppliers and renderers outside this repository read it.

Packages arrive under `internal/` when a change needs them — do not create them speculatively.

## Build, test, lint

Tool versions are managed by [mise](https://mise.jdx.dev/) (`mise.toml`, with checksums in the committed `mise.lock` — regenerate it with `mise lock` whenever a pin changes); tasks run through [just](https://github.com/casey/just) (`justfile`). One-time setup:

```sh
mise install     # install pinned Go, golangci-lint, just, lefthook
just hooks       # install git hooks (lefthook)
```

Everyday commands:

```sh
just build              # build the binary to bin/
just test               # go test ./...
just test-race          # go test -race ./... — what CI and the pre-push hook run
just lint               # golangci-lint run ./...
just fmt                # golangci-lint fmt ./... — the formatter set CI enforces
just tidy-check         # fail if go.mod/go.sum are not tidy (CI runs the same check)
just vuln               # govulncheck over dependencies and the standard library
just toolchain-outdated # report mise-managed pins behind upstream (what the weekly report runs)
just sweep              # disclosure sweep over the tree and commits no remote-tracking ref reaches (the pre-push hook sweeps the refs being pushed)
just sweep-file <path>… # disclosure sweep over drafted text bound for GitHub
just install            # build and install to ~/.local/bin
```

Formatting is enforced by golangci-lint's configured formatters (`gofmt`, `goimports`) — there is no separate format-check step. The `lefthook` hooks run formatting on commit and the disclosure sweep, lint, and tests on push.

## Development approach

This project uses [BDD][bdd]-style/outside-in [TDD][tdd] for non-trivial code: write a failing behavior test from the caller's perspective first, let it drive the API, then implement the minimum to pass and refactor under the test's safety net. Specs start from the readout a renderer would consume and drive configuration, state, and adapters inward. Tests use the standard `testing` package (no external frameworks), favor table-driven cases, and isolate filesystem state with `t.TempDir()`. Skip the ceremony for trivial work (typos, single-line fixes, documentation, these instruction files).

Go authoring conventions are in `.claude/rules/go-practices.md` (loaded when Claude reads a Go file).

## Key design decisions

- **lookout never talks to an issue tracker.** A tracker's data reaches it as a file an outside supplier writes, already reduced, in a versioned contract. Suppliers know nothing about workstreams, so either side can change without the other.
- **Input health is computed, never self-reported, and never softened.** A missing expected input is an error, a stale or schema-violating one is degraded, and a supplier's own degraded or error status passes through. A workstream fed by a failing input says so rather than quietly reporting less.
- **One configuration file per setting, never merged.** A setting (personal, work, …) is selected explicitly whenever more than one is configured; with several configured and none selected, lookout refuses rather than combining them.
- **Every forge-backed adapter is fenced by configured owners.** Only repositories, pull requests, and sessions belonging to the configured owners are admitted, so one account's credentials cannot pull another context's work into a readout.
- **Configuration is user intent; state is tool history.** Workstreams and their order live in the hand-editable configuration file, and commands that change them preserve its comments and layout. Dismissals, the gesture log, and the baseline live under the state directory and are never hand-edited.

## Conventions in this repo

- `.claude/rules/comment-conventions.md` — repo-wide comment conventions (why-not-what, comment durability); always loaded.
- `.claude/rules/go-practices.md` — Go authoring conventions (loaded when Claude reads a Go file).
- `.claude/rules/github-actions-pinning.md` — pin every action to a commit SHA (loaded when Claude reads a workflow).
- `.claude/rules/pr-conventions.md` — PR descriptions, commit format, changelog policy, branch freshness, fix-vs-defer.
- `.claude/rules/pr-waste-patterns.md` — what counts as reviewer-distracting waste in a diff.
- `.claude/rules/no-personal-details.md` — keep personal, employer, and identifying details out of this public repo.
- `.claude/rules/toolchain-ci-parity.md` — keeping the pinned local toolchain and CI in lockstep (loaded when Claude reads a pinned-toolchain file).
- `.claude/rules/design-fork-adjudication.md` — how value-laden design forks are weighed here.
- `README.md` — what the tool is.
- `docs/vision.md` — the dashboard lookout is the data layer for: what it shows, the settled decisions, and the strategic questions still open.
- `SECURITY.md` — reporting channel, plus the supply-chain claims. It asserts what CI scans, pins, and verifies, so a change to the workflows, `mise.lock`, or the Dependabot config should be checked against it.

[bdd]: https://en.wikipedia.org/wiki/Behavior-driven_development
[tdd]: https://en.wikipedia.org/wiki/Test-driven_development
