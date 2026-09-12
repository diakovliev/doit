# Project Guidelines

## Project Scope

- This repository is a Go CLI for AI-assisted code development, review, and testing.
- Keep project goals and terminology aligned with [docs/purpose.md](../docs/purpose.md) and [docs/definition.md](../docs/definition.md).

## Go Development

- Use the Go version declared in [go.mod](../go.mod).
- Keep code idiomatic and focused; prefer small, testable packages as the CLI grows.
- Keep the root `main` package as a thin integration and composition layer for CLI wiring, dependency construction, and process exit handling; it is not a place for domain, orchestration, model, tool, session, or configuration logic.
- Concentrate behavior in small, focused subpackages with one clear responsibility, explicit dependency direction, and tests at their boundaries. Move reusable or meaningful logic out of the root package rather than growing a large entrypoint.
- Format every changed Go file with `gofmt` before validation.
- Preserve existing behavior unless the task explicitly requires a behavior change.

## Implementation Plan

- Treat [docs/implementation-plan.md](../docs/implementation-plan.md) as the source of truth for implementation sequencing and MVP scope.
- Before changing code, identify the smallest unblocked plan task that owns the requested behavior and include its task ID in the work summary.
- Respect task dependencies. If a prerequisite decision or task is unresolved, mark the task `BLOCKED` and update the plan instead of silently inventing a new contract.
- Keep task status current: use `IN PROGRESS` while working, `DONE` only after its acceptance criteria and focused validation pass, and `DEFERRED` only for explicitly postponed scope.
- Update the plan's progress log with changed files and validation evidence when a task starts, completes, or becomes blocked.
- If implementation reveals a design change, update the relevant design or definition document and add or revise a tracked plan task before continuing.

## Validation

- After Go code changes, run `go test ./...`, `go vet ./...`, `golangci-lint run`, and `gosec ./...`.
- Fix lint and security findings in the implementation; do not silence `revive` or other findings with disable directives unless explicitly requested.
- Add or update focused tests when behavior changes.

## Documentation

- Update the relevant documentation in `docs/` when a user-facing behavior, workflow, or project concept changes.
- Keep documentation concise and consistent with the current implementation.