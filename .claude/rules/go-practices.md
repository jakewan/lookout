---
paths:
  - "**/*.go"
---

# Go Practices

Conventions for Go code in this repository.

## Errors

- Wrap errors with context as they propagate: `fmt.Errorf("reading supplied file: %w", err)`. Use `%w` so callers can `errors.Is`/`errors.As` the cause.
- Before relying on `errors.Is` to match a dependency's sentinel, confirm the cause is in the chain — `errors.Is` only traverses causes wrapped with `%w`, so one formatted with `%v` silently fails to match. When unsure, match a stable error code or type instead.
- On Go 1.26+, prefer `errors.AsType[T](err)` over the pre-declared-var-plus-`errors.As` form when recovering a typed error: `if e, ok := errors.AsType[T](err); ok { … }`. It matches on the concrete dynamic type, so a pointer representation in the chain will not satisfy a value target.
- Return errors; don't `log.Fatal` outside `main`.
- Make validation errors specific and actionable — name what was wrong and what to do instead, so the message stands on its own to someone reading it in a terminal.
- `errcheck` runs with `check-blank: true`, so discarding an error to `_` is itself a lint failure — `_ = f()` does not silence an unwanted error. Capture and inspect it, or fold a secondary cleanup error into the primary one with `errors.Join(...)`.
- `govet`'s `shadow` check is enabled. Give inner error variables distinct names (`readErr`, `writeErr`, `cerr`) rather than re-declaring `err`.

## Context

- Functions that do I/O or are cancellable take `context.Context` as the **first** parameter.
- Don't store a `context.Context` in a struct; pass it through the call chain.

## Command output

- **stdout carries command output and nothing else.** Callers parse it, so diagnostics, warnings, and usage go to stderr. `cmd/lookout` takes its writers as parameters, so tests assert on both streams.
- Build slices that reach JSON output with `make([]T, 0, n)`. A nil slice marshals to `null`, which breaks an iterating consumer.

## Tests

- Use the standard `testing` package — no external assertion or mocking frameworks.
- Prefer table-driven tests for behavior variations (valid input, invalid input, edge cases, error paths) — not just the happy path.
- Isolate filesystem state with `t.TempDir()`; register cleanup with `t.Cleanup`.
- Tests describe **what** the code does from the caller's perspective, not **how**. An interface should exist because a test needs to substitute an implementation, not as speculative abstraction.
- A guard that has never been observed failing is not yet a guard. When adding one, break the thing it protects, watch it go red, and restore.

## Documentation

- Exported types, functions, and packages carry godoc comments beginning with the symbol's name (`// New builds …`). The `revive` `exported` lint rule enforces this — a missing or malformed comment fails CI.
- Comments explain **why** — rationale, constraints — not **what** the code already says.

## Modernization

This module tracks the forms `go fix` endorses. `go fix -diff ./...` reports them as a patch without applying anything, and `go fix ./...` applies them; `go tool fix help` lists the analyzers and what each one rewrites. The `errors.AsType[T]` preference under Errors is an instance of the same posture — its analyzer is `errorsastype`. Not every form preference in this file is: `make([]T, 0, n)` under Command output is a wire-shape requirement no analyzer emits, and reading it as optional tidiness would be a defect.

**No gate runs it, deliberately.** `go fix -diff` exits non-zero on a non-empty diff and would make a one-line CI or hook check, but that would let the pinned Go version decide what the module is allowed to contain: a fixer added upstream would turn the build red with no code change, and the pin bump and the code rewrite would then have to land together. That is the coupling class `.claude/rules/toolchain-ci-parity.md` governs, and it is a heavier trade than the tidiness buys. Run it when a Go pin moves, or when you want to know.

The tool's reach is narrower than a form's applicability, so a clean `go fix -diff` means the analyzers matched nothing rather than that no site remains — `waitgroupgo`, for one, skips a closure that takes a parameter. Prefer the modern form at a site the analyzer declines rather than reading its silence as approval.

A rewrite is not automatically behavior-preserving just because a tool emitted it. `waitgroupgo` is the standing counter-example: `sync.WaitGroup.Go` carries the precondition `The function f must not panic` in its own doc comment, because it skips `Done` and re-panics where a hand-written `defer wg.Done()` runs. Read what the analyzer substitutes before accepting it.
