# Implementation Plan

**Status:** Not started

This plan turns [design.md](design.md) into trackable work. It covers the Safe Local MVP first and leaves provider extensions, remote Git operations, and cloud features out of the critical path.

## Tracking Rules

Use one status for every task:

- `TODO`: Not started.
- `IN PROGRESS`: Actively being implemented.
- `BLOCKED`: Waiting on a decision, dependency, or external requirement.
- `DONE`: Acceptance criteria and validation are complete.
- `DEFERRED`: Intentionally moved out of the current scope.

When a task changes status, update this document with the implementation commit or pull request, test evidence, and any new follow-up task. Do not mark a task `DONE` because the code compiles alone; its stated acceptance criteria and focused tests must pass.

## Definition of Done

The Phase 1 MVP is complete when:

- `doit run "explain this repository"` completes against the deterministic fake backend without network access or real credentials.
- The CLI loads a named backend profile and calls the OpenAI-compatible Responses endpoint.
- The agent can inspect bounded workspace files, request an allowlisted validation task, and return structured results.
- A model function call can request a tool, pass through approval policy, receive a normalized result, and continue the task.
- Code changes are previewed, validated, approved, and applied as reviewable patches.
- Input, output, and total token counters are emitted for every model request and aggregated into the session result.
- Durable session data is redacted, bounded, recoverable, and stored under `.doit/sessions/` below the effective invocation path.
- Read-only Git status and diff inspection works without arbitrary Git command composition.
- Required tests, `go vet`, `golangci-lint`, and `gosec` pass.

## Phase 0: Decision Freeze

These decisions must be resolved before the corresponding implementation task starts. The recommended defaults are recorded in [design.md](design.md#123-recommended-mvp-defaults).

| ID | Decision | Acceptance evidence | Depends on | Status |
| --- | --- | --- | --- | --- |
| `DEC-001` | Freeze the core OpenAI Responses compatibility level: non-streaming text plus function calling; decide whether streaming is a Phase 1 requirement. | A short compatibility checklist names required and optional fields and fallback behavior. | None | `TODO` |
| `DEC-002` | Freeze configuration format, locations, precedence, profile names, and environment variable names. | Example configuration and precedence table are committed to the docs. | None | `TODO` |
| `DEC-003` | Freeze Go contracts for `ModelClient`, `Tool`, `ToolResult`, `ApprovalPolicy`, `SessionStore`, and `TokenCounter`. | Interfaces include cancellation, errors, normalized results, and usage fields. | `DEC-001` | `TODO` |
| `DEC-004` | Freeze the initial argument and result schemas for `fs.read`, `fs.search`, `code.apply_patch`, `git.status`, `git.diff`, and `process.run`. | Each tool has a schema test or fixture request and a documented risk class. | `DEC-003` | `TODO` |
| `DEC-005` | Freeze automatic, confirmation-based, and rejected actions. | Approval matrix covers filesystem writes, process tasks, Git writes, remote access, and non-interactive mode. | `DEC-004` | `TODO` |
| `DEC-006` | Freeze token counting and budget behavior, including provider usage, local estimates, unknown values, and retries. | Usage examples show per-request and cumulative session accounting. | `DEC-001`, `DEC-003` | `TODO` |
| `DEC-007` | Freeze Git scope for subdirectories, worktrees, submodules, detached HEAD, and repository roots outside the workspace. | Git scope rules and refusal cases have fixture scenarios. | `DEC-004`, `DEC-005` | `TODO` |
| `DEC-008` | Freeze the minimum operating-system and shell matrix, path rules, process environment, and cancellation behavior. | Supported-platform table is documented and test environments are identified. | None | `TODO` |
| `DEC-009` | Define the deterministic fake Responses backend scenarios. | Fixtures cover text, function calls, streaming decision, usage, missing usage, rate limits, malformed responses, timeouts, and cancellation. | `DEC-001`, `DEC-006` | `TODO` |

## Phase 1: Foundation

| ID | Work item | Depends on | Done when | Status |
| --- | --- | --- | --- | --- |
| `FOUND-001` | Establish the Go package layout and core error types. | `DEC-003` | Packages have clear ownership, errors preserve cause and category, and the empty application still builds. | `TODO` |
| `FOUND-002` | Implement configuration loading and backend profiles. | `DEC-002` | Defaults, file configuration, environment overrides, flags, credential references, and validation are tested. | `TODO` |
| `FOUND-003` | Implement the CLI root, global options, help, version, command dispatch, and stable exit codes. | `DEC-002`, `DEC-008` | `doit`, `doit run`, `doit agent`, `--format`, `--directory`, `--profile`, `--ephemeral`, and invalid-input paths behave as documented. | `TODO` |
| `FOUND-004` | Implement the normalized tool contract and registry. | `DEC-003`, `DEC-004` | Tools register schemas, risk classes, limits, cancellation behavior, and normalized results. | `TODO` |
| `FOUND-005` | Implement deterministic fake model and process clients. | `DEC-003`, `DEC-009` | Unit and integration tests can run model/tool loops without credentials or network access. | `TODO` |
| `FOUND-006` | Implement token accounting primitives. | `DEC-006` | Every request creates usage counters, provider usage reconciles estimates, streaming does not double-count, and cumulative totals are testable. | `TODO` |

## Phase 2: Local Tools and Persistence

| ID | Work item | Depends on | Done when | Status |
| --- | --- | --- | --- | --- |
| `TOOL-001` | Implement bounded filesystem inspection. | `FOUND-004`, `DEC-004`, `DEC-007` | `fs.list`, `fs.stat`, `fs.read`, `fs.search`, and `fs.hash` enforce workspace, ignore, symlink, size, and cancellation rules. | `TODO` |
| `TOOL-002` | Implement structured Git inspection. | `TOOL-001`, `DEC-007` | `git.root`, `git.status`, `git.diff`, `git.log`, `git.show`, `git.blame`, and `git.check_ignore` return structured fixture results and refuse unsupported scope. | `TODO` |
| `TOOL-003` | Implement the allowlisted process runner. | `FOUND-004`, `DEC-005`, `DEC-008` | Named test, format, lint, security, vet, and build tasks enforce arguments, environment, working directory, timeout, cancellation, and output limits. | `TODO` |
| `TOOL-004` | Implement patch validation, preview, application, rename, and formatter integration. | `TOOL-001`, `TOOL-003`, `DEC-004`, `DEC-005` | `code.check_patch`, `code.apply_patch`, `code.rename`, and `code.format` detect conflicts, produce diffs, use atomic writes, and never bypass approval. | `TODO` |
| `POL-001` | Implement approval and safety policy evaluation. | `FOUND-004`, `TOOL-001`, `TOOL-002`, `TOOL-003`, `TOOL-004`, `DEC-005` | Automatic, confirmation, denied, cancelled, and non-interactive decisions are visible and tested. | `TODO` |
| `SESSION-001` | Implement project-local session persistence. | `FOUND-006`, `DEC-006`, `DEC-007` | Manifests, ordered events, results, continuation data, locks, redaction, bounded output, atomic writes, recovery, and `--ephemeral` are tested below the effective invocation path. | `TODO` |

## Phase 3: Model and Orchestration

| ID | Work item | Depends on | Done when | Status |
| --- | --- | --- | --- | --- |
| `MODEL-001` | Implement the OpenAI-compatible HTTP Responses adapter. | `FOUND-002`, `FOUND-005`, `FOUND-006`, `DEC-001`, `DEC-009` | API-root joining, bearer credentials, request IDs, text output, function calls, function results, errors, timeouts, and usage reconciliation pass fake-backend tests. | `TODO` |
| `MODEL-002` | Implement streaming support if accepted in `DEC-001`. | `MODEL-001`, `DEC-009` | SSE deltas, terminal events, cancellation, partial output, and usage reconciliation produce the same normalized events as non-streaming requests. | `TODO` |
| `CONTEXT-001` | Implement the bounded context builder. | `TOOL-001`, `FOUND-006`, `SESSION-001` | Instructions, project guidance, user input, selected files, Git state, and tool results are selected within token and privacy budgets. | `TODO` |
| `AGENT-001` | Implement the model/tool orchestration loop. | `MODEL-001`, `FOUND-004`, `POL-001`, `SESSION-001`, `CONTEXT-001` | The loop handles text, function calls, approval, normalized results, retries, cancellation, incomplete responses, session events, and final summaries. | `TODO` |
| `CLI-001` | Connect `doit run` and `doit agent` to the orchestration loop. | `FOUND-003`, `AGENT-001` | Human and JSON output, stdin requests, interactive prompts, errors, token usage, changed paths, and exit codes match the CLI design. | `TODO` |
| `MVP-001` | Prove the end-to-end MVP acceptance scenario. | `CLI-001`, `MODEL-001`, `TOOL-001`, `TOOL-003`, `TOOL-004`, `SESSION-001` | The documented `doit run "explain this repository"` fake-backend scenario passes without credentials or network access. | `TODO` |

## Phase 4: Hardening and Workflows

| ID | Work item | Depends on | Done when | Status |
| --- | --- | --- | --- | --- |
| `HARD-001` | Add security and boundary tests. | `MVP-001` | Workspace escapes, symlink escapes, command injection, secret leakage, oversized output, interrupted processes, and unsafe Git operations are covered. | `TODO` |
| `HARD-002` | Add reviewable operational diagnostics. | `MVP-001` | Request IDs, provider errors, rate limits, tool timings, validation status, and redacted session evidence are available without credentials. | `TODO` |
| `WORK-001` | Add dedicated `develop`, `review`, and `test` workflows. | `MVP-001`, `HARD-001` | Each workflow has focused context selection, output, validation, and exit-status tests. | `TODO` |
| `WORK-002` | Add session resume, export, pruning, and recovery commands. | `SESSION-001`, `MVP-001` | Interrupted and completed sessions can be safely inspected, resumed, exported, and pruned under the documented policy. | `TODO` |
| `WORK-003` | Add CI for the repository's required checks. | `HARD-001` | CI runs tests, vet, lint, gosec, deterministic integration tests, and platform-specific checks without live model credentials. | `TODO` |

## Deferred Work

These items are intentionally outside the MVP critical path:

- `DEFER-001`: Native adapters for APIs that do not satisfy the OpenAI Responses compatibility contract.
- `DEFER-002`: Multimodal input and provider-hosted tools.
- `DEFER-003`: Remote MCP integrations.
- `DEFER-004`: Git push, merge, force-push, history rewriting, and remote management.
- `DEFER-005`: Cloud session synchronization and hosted analytics.
- `DEFER-006`: Plugin or external tool extension protocols.
- `DEFER-007`: Additional interactive UI frontends and automatic model discovery.

Deferred work must not change the approval, observability, token accounting, session redaction, or deterministic validation boundaries established by the MVP.

## Progress Log

| Date | Task ID | Change | Evidence |
| --- | --- | --- | --- |
| 2026-09-12 | `PLAN-001` | Created the initial trackable implementation plan from the project design. | This document |
