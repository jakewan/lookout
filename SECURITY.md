# Security Policy

## Reporting a vulnerability

Please report security issues privately through GitHub's [private vulnerability reporting](https://docs.github.com/en/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability) on this repository's **Security** tab, rather than opening a public issue.

If that route is unavailable to you for any reason, open a regular issue saying only that you have a security report and how to reach you — no details — and you'll get a private channel back. Never put the details in a public issue.

Include what you did, what happened, and what you expected — a reproduction is the most useful thing you can send. Please allow time for a fix before disclosing publicly.

## Supported versions

lookout is in early development and cuts no tagged releases yet, so the supported surface is the current `main` branch. Fixes land there; there is no backport channel.

## Build and CI supply chain

- **Dependencies and the standard library are scanned for known vulnerabilities.** [`govulncheck`](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) runs on every pull request against `main`, on pushes to `main`, and weekly on a schedule. It reports advisories whose vulnerable symbols are reachable from this code. The scan is **advisory, not blocking** — it is not a required status check, so it informs review rather than gating merges.
- **Build-time dependencies are checksum-verified.** Go modules are verified against the checksum database and `go.sum`, re-hashed in CI by `go mod verify`, and held tidy by `go mod tidy -diff` so entries no longer required cannot linger. `govulncheck` itself is a `go.mod` tool dependency, so the scanner is covered by the same verification as everything else.
- **CI pins its own toolchain; `mise.lock` covers local development.** Tool versions live in `mise.toml` with their resolved download URLs and checksums in the committed `mise.lock`, and `mise install` verifies against it — but no CI job installs through mise, so the lockfile governs a contributor's machine rather than the build. CI pins independently: Go comes from `go.mod` via `actions/setup-go`, and golangci-lint from its action's explicit `version:` input.
- **Actions are pinned by commit digest.** Every `uses:` in every workflow names a full commit SHA with the version as a trailing comment — a tag can be repointed, a commit SHA cannot.
- **Update automation covers Go modules and GitHub Actions.** Dependabot watches both weekly, and bumps the action SHAs along with their version comments.
- **The mise toolchain is reviewed manually.** No update bot covers `mise.toml`, so the **Toolchain currency** workflow checks the pins against upstream weekly and **fails when any is behind**. The deliberate failure is what surfaces the report at all, since `mise outdated` exits 0 whether or not a pin has moved. A red run therefore means one of three things, and its annotation says which: a pin is behind and is named, or mise could not report, or its output could not be processed. Read the last two as the reporter having failed rather than as a verdict about the pins — and note that the report is not able to detect a change in mise's own output schema, which would make it fall quiet rather than loud, so a run that stops failing across a mise upgrade is worth distrusting. GitHub notifies you about workflow runs *you* triggered, and for a scheduled run the person notified is the workflow's creator — or whoever last changed its cron or re-enabled it. It blocks nothing (neither scheduled workflow is a required check). `just toolchain-outdated` runs the same query locally, against whatever mise is on your `PATH` rather than the pinned one, and it reports without failing. The pinned version of mise itself lives in that workflow rather than in `mise.toml`, so nothing reports it; it moves only when a maintainer moves it.
- **Scheduled scanning stops firing in a quiet repository.** In a public repository GitHub disables scheduled workflows after 60 days without repository activity. That is one repository-level clock rather than one per workflow, so **Vulnerability scan** and **Toolchain currency** stop together — read a gap in either one's weekly runs as a symptom, not as a clean result.

This describes a detector paired with a human response, not a project that is continuously free of known advisories. Go patch releases routinely fix reachable standard-library symbols, so the scan going red is expected periodically and is resolved by a maintainer bumping the pinned toolchain.
