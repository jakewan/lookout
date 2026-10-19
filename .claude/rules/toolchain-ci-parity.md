---
paths:
  - "mise.toml"
  - "mise.lock"
  - "go.mod"
  - "lefthook.yml"
  - ".golangci.yml"
  - ".github/workflows/ci.yml"
  - ".github/workflows/vuln.yml"
  - ".github/workflows/toolchain.yml"
  - "justfile"
---

# Toolchain / CI Parity

Local development and CI must run the same tool versions and the same formatters. When they drift, a change passes locally and fails in CI (or the reverse), or CI silently tests a different Go than developers run. These invariants are coupled across the files this rule conditions on — editing one file without its partner reintroduces the drift.

## golangci-lint is pinned twice, as one atomic value

golangci-lint is pinned in `mise.toml` (the local binary) and as the `golangci-lint-action` `version:` input in `.github/workflows/ci.yml` (the CI binary). The two must be identical, and a bump moves both in the same commit. A config written for one minor can warn or error on another, so a mismatch means lint passes locally and fails in CI, or vice versa.

The two spellings differ and that is not drift: mise pins a bare patch, and the workflow input carries the same patch with a leading `v`, because `golangci-lint-action` validates its input against a `v`-anchored pattern and throws on a bare patch (read from the action's source at the SHA pinned in `ci.yml`; if it changed, the symptom would be a loud failure at install rather than a wrong binary).

**Nothing detects a mismatch between the `mise.toml` pin and the workflow input.** Dependabot bumps an action's `uses:` SHA, never its inputs, and the weekly `toolchain-report` job in `.github/workflows/toolchain.yml` reads `mise.toml` alone — so bumping the mise pin and forgetting the workflow leaves that report green the following week while the drift stands. Review is the only guard, which is why this is written down rather than left to a gate.

## go.mod `go` directive tracks the mise Go pin

CI installs Go via `actions/setup-go` with `go-version-file: go.mod`, so the `go` directive in `go.mod` — not `mise.toml` — chooses the CI toolchain. Pin it to the same patch version as the `go` pin in `mise.toml`:

- A minor-only floor (`go 1.26`) lets CI float to the latest 1.26.x, drifting ahead of the pinned dev toolchain.
- A `.0` floor (`go 1.26.0`) pins CI to the oldest 1.26 patch, drifting behind it.
- The exact patch makes CI install the same Go developers run. This is also what `go mod init`/`go mod tidy` write by default.

Bump `go.mod` and `mise.toml` together on a Go upgrade. `govulncheck` reports standard-library advisories against the Go on `PATH`, so whichever pin a given run resolves decides what the vulnerability scan considers vulnerable: CI's scan job uses `setup-go` from `go.mod`, a developer's `just vuln` uses mise's, and they agree only while the two pins do.

A Go upgrade can also retire code forms, since `go fix`'s analyzer set grows with the toolchain. `.claude/rules/go-practices.md` § Modernization records that this module tracks those forms and how to report them. It is repeated here because a pin bump touches no Go file, so that rule does not load at the moment this one does.

**The Go pin also has an upper bound, which the parity above does not express.** golangci-lint analyses with the `go/types` of the Go release it was itself built against — `golangci-lint --version` reports that release — and each golangci-lint release announces which Go minor it adds support for. A Go minor is published before the golangci-lint release supporting it, and the weekly toolchain report names the two tools independently, so taking the Go line on its own is the report's most natural misreading. Move the pair together, and take the supporting release from golangci-lint's own release notes rather than assuming the newest patch covers it.

The failure is loud rather than silent, which makes `just lint` the gate that catches it and the one to run when moving the pair: golangci-lint refuses to start and names both versions — `the Go language version (goX.Y) used to build golangci-lint is lower than the targeted Go version (X.Z)`. Every other gate passes, so a bump verified with anything less looks clean.

## mise.lock moves with every mise.toml pin

`mise.toml` sets `lockfile = true`, and `mise.lock` records each tool's resolved URL and checksum per platform. It is committed, and the two files must move together: run `mise lock` and commit the result in the same commit as any `mise.toml` version change.

**Nothing in CI enforces this.** No workflow job installs through mise — the only `jdx/mise-action` call, in `toolchain.yml`'s toolchain-report job, sets `install: false`, and CI takes Go from `go.mod` and golangci-lint from an action input. So a stale `mise.lock` is caught by review or not at all.

**The two commands are not interchangeable, and only one of them is safe after a pin change.** `mise lock` refreshes every platform already recorded in the lockfile. An install rewrites the changed tool's entry to the new version and **drops all of its platform blocks**, leaving an entry nothing can be verified against — whether or not it actually installs anything. So a pin bump followed by an install alone silently strips that tool's verification data. Run `mise lock` after any pin change, and read the resulting diff. The symptom of this going wrong is a lockfile entry carrying a version with no platform blocks under it.

## The govulncheck tool dependency reaches the built binary

`govulncheck` is a `go.mod` tool dependency, so its requirements participate in the main module's version resolution. That is not confined to tooling: a module it requires can be one the binary also links, and an update of `x/vuln` or its dependencies can then raise that module's version in the shipped build.

The consequence for review: a bump that looks like tooling can change the binary's build list. Check `go list -deps ./cmd/lookout` when a govulncheck update moves a shared dependency, and do not describe such a change as CI-only without checking.

## pre-commit formats with CI's formatter set; pre-push runs CI's test form

CI's golangci-lint enforces the formatters configured in `.golangci.yml` (`gofmt` + `goimports`). The pre-commit hook in `lefthook.yml` runs `golangci-lint fmt`, which applies that same configured set and re-stages the result — so an import-ordering issue can't pass the commit hook only to fail the lint job later. If the formatters enabled in `.golangci.yml` change, the hook inherits them automatically.

The pre-push test hook runs `just test-race`, matching CI's `go test -race`. Keep them the same form, so a push the hook passes cannot fail CI's test job for a reason the hook never checked.
