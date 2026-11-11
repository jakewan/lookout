# lookout — Agent Guide

This file orients an AI agent (or a new contributor) working in this repository. It is self-contained: everything needed to work here is described below or in the linked in-repo files.

## What lookout is

A command-line tool that gives one vantage point over everything you have in flight. It gathers recent activity — git branches and worktrees, GitHub pull requests and issues, AI coding sessions, and files supplied by other tools such as an issue-tracker exporter — groups it into workstreams you define, and reports how recently each one moved and whether every input it relies on is healthy.

It is the data layer: it emits a readout for a renderer to consume. `lookout view` writes a self-contained graphical page and `lookout show` prints a plain-text view over that same readout. Both are drawn only from what the readout publishes, so they stay replaceable. The page is the dashboard's chosen renderer; `docs/vision.md` records why, and that the health, staleness and ranking rules stay in Go: the page's script derives only layout and what is shown, never a rule.

## Status and layout

Early. `lookout read --json`, `lookout show`, `lookout view` and `lookout unplaced` report on supplied files, local git repositories, GitHub and Claude Code sessions; `read --json` also publishes the replies owed that `awaiting.v1` files supply, whose inputs' health `show` and `view` report though they do not draw the entries yet; `adopt`, `move`, `remove`, `rename`, `order`, `complete` and `delete` edit the configuration file, and `dismiss` writes state, each recording a gesture; `snapshot` records a daily baseline in state.

```
cmd/lookout/              # binary entry point and its behavior specs
schema/                   # the published contracts, embedded so lookout enforces the same files
internal/config/          # locating, loading, and editing one setting's configuration file
internal/git/             # reading local branches of owner-fenced git repositories
internal/github/          # reading owner-fenced pull requests and issues on github.com
internal/claudecode/      # reading owner-fenced Claude Code sessions, one item per repository and branch
internal/execpath/        # finding an executable on the invocation's own PATH
internal/fsx/             # the one rule filesystem errors are sorted by
internal/supplied/        # reading a supplied file and judging its input's health, for every contract sharing its envelope
internal/awaiting/        # reading a file of replies owed in the awaiting.v1 contract
internal/health/          # the health record every input reports
internal/readout/         # assembling the readout, and deciding each workstream's active and behind verdicts
internal/textview/        # the plain-text view of a readout, read only through its published JSON
internal/htmlview/        # the self-contained browser page of a readout, read only through its published JSON
internal/htmlview/browser/ # the page's browser specs (Playwright, plain JavaScript); not part of the binary
internal/viewrules/       # how every view marks age, staleness and health, the same way
internal/state/           # one setting's tool history: the gesture log, the dismissals replayed from it, and the baseline
internal/dirlock/         # the advisory lock state and the page's directory are written under
internal/baseline/        # deriving each day's baseline record from a readout, the configuration and the gesture log
internal/devtools/sweep/  # disclosure sweep run before every push; not part of the binary
internal/devtools/npmregistry/ # fails a package-lock.json that resolves anywhere but the public npm registry
```

A change to a file under `schema/` changes a published contract: suppliers and renderers outside this repository read it.

Packages arrive under `internal/` when a change needs them — do not create them speculatively.

## Build, test, lint

Tool versions are managed by [mise](https://mise.jdx.dev/) (`mise.toml`, with checksums in the committed `mise.lock` — regenerate it with `mise lock` whenever a pin changes); tasks run through [just](https://github.com/casey/just) (`justfile`). One-time setup:

```sh
mise install     # install pinned Go, golangci-lint, just, lefthook, Node
just hooks       # install git hooks (lefthook)
just page-setup  # install the page's npm tools from the lockfile, and the browsers its specs drive
```

Building, installing, and `go test ./...` need no Node; only the page's specs and checks do.

Everyday commands:

```sh
just build              # build the binary to bin/
just test               # go test ./...
just test-race          # go test -race ./... — what CI and the pre-push hook run
just test-page          # the page's browser specs in Chromium and Firefox
just page-check         # tsc, Biome, and the lockfile registry check over the page's files
just lint               # golangci-lint run ./...
just fmt                # golangci-lint fmt ./... — the formatter set CI enforces
just tidy-check         # fail if go.mod/go.sum are not tidy (CI runs the same check)
just vuln               # govulncheck over dependencies and the standard library
just toolchain-outdated # report mise-managed pins behind upstream (what the weekly report runs)
just sweep              # disclosure sweep over the tree and commits no remote-tracking ref reaches (the pre-push hook sweeps the refs being pushed)
just sweep-file <path>… # disclosure sweep over drafted text bound for GitHub
just install            # build and install to ~/.local/bin
```

Go formatting is enforced by golangci-lint's configured formatters (`gofmt`, `goimports`) — there is no separate format-check step; the page's JavaScript and JSON are formatted by Biome. The `lefthook` hooks run formatting on commit and the disclosure sweep, lint, and tests on push, the page's commands only when a pushed change touches one of the page's non-Go files; CI's page job also runs for the Go the page is rendered through.

## Development approach

This project uses [BDD][bdd]-style/outside-in [TDD][tdd] for non-trivial code: write a failing behavior test from the caller's perspective first, let it drive the API, then implement the minimum to pass and refactor under the test's safety net. Specs start from the readout a renderer would consume and drive configuration, state, and adapters inward. Go tests use the standard `testing` package (no external frameworks), favor table-driven cases, and isolate filesystem state with `t.TempDir()`. The page's behavior in a browser is specified in Playwright specs under `internal/htmlview/browser/`, which drive the page `lookout view` writes from fixture readouts. Skip the ceremony for trivial work (typos, single-line fixes, documentation, these instruction files).

Go authoring conventions are in `.claude/rules/go-practices.md` (loaded when Claude reads a Go file); the page's are in `.claude/rules/page-practices.md`.

## Key design decisions

- **lookout never talks to an issue tracker.** A tracker's data reaches it as a file an outside supplier writes, already reduced, in a versioned contract. Suppliers know nothing about workstreams, so either side can change without the other.
- **Input health is computed, never self-reported, and never softened.** A missing expected input is an error, a stale or schema-violating one is degraded, and a supplier's own degraded or error status passes through. A workstream fed by a failing input says so rather than quietly reporting less.
- **One configuration file per setting, never merged.** A setting (personal, work, …) is selected explicitly whenever more than one is configured; with several configured and none selected, lookout refuses rather than combining them.
- **Every forge-backed adapter is fenced by configured owners.** Only repositories, pull requests, issues, and sessions belonging to the configured owners are admitted, so one account's credentials cannot pull another context's work into a readout.
- **Configuration is user intent; state is tool history.** Workstreams and their order live in the hand-editable configuration file, and commands that change them preserve its comments and layout. Dismissals, the gesture log, and the baseline live under the state directory and are never hand-edited.

## Conventions in this repo

- `.claude/rules/comment-conventions.md` — repo-wide comment conventions (why-not-what, comment durability); always loaded.
- `.claude/rules/go-practices.md` — Go authoring conventions (loaded when Claude reads a Go file).
- `.claude/rules/page-practices.md` — what the page may compute, its files and policy, its browser specs, and what its stack was checked to do (loaded when Claude reads a page file).
- `.claude/rules/github-actions-pinning.md` — pin every action to a commit SHA (loaded when Claude reads a workflow).
- `.claude/rules/pr-conventions.md` — PR descriptions, commit format, changelog policy, branch freshness, fix-vs-defer.
- `.claude/rules/pr-waste-patterns.md` — what counts as reviewer-distracting waste in a diff.
- `.claude/rules/no-personal-details.md` — keep personal, employer, and identifying details out of this public repo.
- `.claude/rules/toolchain-ci-parity.md` — keeping the pinned local toolchain and CI in lockstep (loaded when Claude reads a pinned-toolchain file).
- `.claude/rules/design-fork-adjudication.md` — how value-laden design forks are weighed here.
- `README.md` — what the tool is.
- `docs/vision.md` — the dashboard lookout is the data layer for: what it shows, the settled decisions, and the strategic questions still open.
- `SECURITY.md` — reporting channel, plus the supply-chain claims. It asserts what CI scans, pins, and verifies, so a change to the workflows, `mise.lock`, `package-lock.json`, `.npmrc`, or the Dependabot config should be checked against it.

[bdd]: https://en.wikipedia.org/wiki/Behavior-driven_development
[tdd]: https://en.wikipedia.org/wiki/Test-driven_development
