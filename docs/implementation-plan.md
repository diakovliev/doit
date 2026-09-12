# Implementation Plan

**Status:** Phase 3 core complete; Phase 4 not started

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
| `DEC-001` | Freeze the core OpenAI Responses compatibility level: non-streaming text plus function calling; decide whether streaming is a Phase 1 requirement. | A short compatibility checklist names required and optional fields and fallback behavior. | None | `DONE` |
| `DEC-002` | Freeze configuration format, locations, precedence, profile names, and environment variable names. | Example configuration and precedence table are committed to the docs. | None | `DONE` |
| `DEC-003` | Freeze Go contracts for `ModelClient`, `Tool`, `ToolResult`, `ApprovalPolicy`, `SessionStore`, and `TokenCounter`. | Interfaces include cancellation, errors, normalized results, and usage fields. | `DEC-001` | `DONE` |
| `DEC-004` | Freeze the initial argument and result schemas for `fs.read`, `fs.search`, `code.apply_patch`, `git.status`, `git.diff`, and `process.run`. | Each tool has a schema test or fixture request and a documented risk class. | `DEC-003` | `DONE` |
| `DEC-005` | Freeze automatic, confirmation-based, and rejected actions. | Approval matrix covers filesystem writes, process tasks, Git writes, remote access, and non-interactive mode. | `DEC-004` | `DONE` |
| `DEC-006` | Freeze token counting and budget behavior, including provider usage, local estimates, unknown values, and retries. | Usage examples show per-request and cumulative session accounting. | `DEC-001`, `DEC-003` | `DONE` |
| `DEC-007` | Freeze Git scope for subdirectories, worktrees, submodules, detached HEAD, and repository roots outside the workspace. | Git scope rules and refusal cases have fixture scenarios. | `DEC-004`, `DEC-005` | `DONE` |
| `DEC-008` | Freeze the minimum operating-system and shell matrix, path rules, process environment, and cancellation behavior. | Supported-platform table is documented and test environments are identified. | None | `DONE` |
| `DEC-009` | Define the deterministic fake Responses backend scenarios. | Fixtures cover text, function calls, streaming decision, usage, missing usage, rate limits, malformed responses, timeouts, and cancellation. | `DEC-001`, `DEC-006` | `DONE` |

### Phase 0 Decision Record

These decisions are frozen for the Safe Local MVP on 2026-09-12. Changes require a new plan entry and an explicit design update.

#### `DEC-001` API Compatibility

- Core compatibility requires non-streaming `POST {api_root}/responses` with text input, text output, client-defined function tools, `function_call` output items, and `function_call_output` follow-up items.
- The client manages conversation state locally. Provider-managed state, hosted tools, multimodal input, and structured output are optional capabilities.
- Streaming is not required for the Phase 1 MVP. `MODEL-002` may add SSE support after the non-streaming path is stable; non-streaming fallback remains mandatory.

#### `DEC-002` Configuration

- Configuration uses JSON to keep the initial implementation dependency-light and directly supported by the Go standard library.
- Project configuration is a non-secret `.doit/config.json` below the effective invocation path. User configuration lives in the operating-system user configuration directory under `doit/config.json`.
- Precedence is built-in defaults, project configuration, user configuration, `DOIT_*` environment overrides, then command-line flags.
- `DOIT_CONFIG_FILE` may select an explicit configuration file. `DOIT_PROFILE`, `DOIT_MODEL`, and `DOIT_API_ROOT` are supported direct overrides.
- Credentials are referenced by environment-variable name or operating-system credential reference; raw tokens are not stored in configuration files.

#### `DEC-003` Go Contracts

- `ModelClient` accepts `context.Context` and normalized model requests, returning normalized responses or categorized errors.
- `Tool` exposes a stable name, JSON schema, risk class, limits, and a cancellation-aware execution method.
- `ToolResult` distinguishes success, failure, denial, and cancellation and carries bounded structured data and diagnostics.
- `ApprovalPolicy` evaluates a typed action and returns allow, confirm, deny, or cancel without executing the action.
- `SessionStore` starts or resumes a session, appends ordered events, writes completion results, and recovers interrupted sessions.
- `TokenCounter` produces labeled exact, estimated, mixed, or unknown usage values and never treats unknown as zero.
- Contracts live in focused subpackages under `internal/`; the root `main` package remains integration-only.

#### `DEC-004` Tool Schemas

- `fs.read`: `{path, start_line?, end_line?, max_bytes?}`.
- `fs.search`: `{query, path?, glob?, max_results?, include_ignored?}`.
- `code.apply_patch`: `{patch, expected_hashes?, dry_run?}`.
- `git.status`: `{path?, include_ignored?}`.
- `git.diff`: `{source, revision?, paths?, max_bytes?}` where source is `worktree`, `index`, or `range`.
- `process.run`: `{task, args?, working_directory?, timeout?}` where task is a configured name, not an executable string.
- Every tool returns the normalized result shape and declares its risk class, path scope, timeout, output limit, and cancellation behavior.

#### `DEC-005` Approval Policy

- Read-only filesystem and Git inspection is automatic within the effective scope.
- Process execution, code writes, renames, deletes, and all Git index or history changes require confirmation in the MVP.
- Arbitrary shell pipelines, path escapes, writes outside the workspace, remote access, pushes, force operations, and history rewrites are rejected.
- Non-interactive invocations deny confirmation-required actions unless an explicit configured policy allows them.

#### `DEC-006` Token Accounting

- Provider usage is authoritative when present. Otherwise the first implementation uses a deterministic fallback estimate of at least one token for non-empty input and approximately one token per four UTF-8 bytes, always labeled as an estimate.
- Input usage includes instructions, input items, tool definitions, and previous tool results. Output usage includes provider-reported reasoning tokens when included by the backend.
- Initial defaults are `max_input_tokens: 16000`, `max_output_tokens: 4000`, and `max_session_tokens: 64000`; backend or user limits may lower them.
- Transport retries are limited to two retries for explicitly retryable failures. Every attempt has its own usage record; tool execution is never replayed automatically.

#### `DEC-007` Git Scope

- The effective invocation path is the write boundary. Git may report a containing repository root for inspection, but files outside the effective path are not exposed through path-scoped tools by default.
- Worktrees are treated as independent roots. Submodules are not traversed unless explicitly selected.
- Detached HEAD is readable but does not enable branch or history changes. A repository root outside the effective path permits read-only, path-scoped inspection only.
- The MVP performs no remote Git operations and does not commit, restore, branch, merge, reset, or rewrite history.

#### `DEC-008` Platform Contract

- Phase 1 is validated on Windows 10/11 with PowerShell 5.1 or later and `pwsh` 7 or later. The Go APIs avoid shell-specific quoting so later POSIX support remains possible.
- Paths use Go's platform-aware path handling. Process tasks use configured argument arrays rather than shell command strings.
- Process cancellation is context-driven with bounded output, bounded duration, and an allowlisted environment.

#### `DEC-009` Fake Backend Scenarios

- The test backend is an in-process `httptest.Server` with deterministic responses for text, function calls, provider usage, missing usage, malformed JSON, authentication failure, rate limiting, server failure, timeout, and cancellation.
- Streaming is represented as an unsupported capability in the MVP fixture set; SSE scenarios begin with `MODEL-002` if streaming is enabled later.
- The default test suite never requires real credentials or network access.

## Phase 1: Foundation

| ID | Work item | Depends on | Done when | Status |
| --- | --- | --- | --- | --- |
| `FOUND-001` | Establish the Go package layout and core error types. | `DEC-003` | Packages have clear ownership, errors preserve cause and category, and the empty application still builds. | `DONE` |
| `FOUND-002` | Implement configuration loading and backend profiles. | `DEC-002` | Defaults, file configuration, environment overrides, flags, credential references, and validation are tested. | `DONE` |
| `FOUND-003` | Implement the CLI root, global options, help, version, command dispatch, and stable exit codes. | `DEC-002`, `DEC-008` | `doit`, `doit run`, `doit agent`, `--format`, `--directory`, `--profile`, `--ephemeral`, and invalid-input paths behave as documented. | `DONE` |
| `FOUND-004` | Implement the normalized tool contract and registry. | `DEC-003`, `DEC-004` | Tools register schemas, risk classes, limits, cancellation behavior, and normalized results. | `DONE` |
| `FOUND-005` | Implement deterministic fake model and process clients. | `DEC-003`, `DEC-009` | Unit and integration tests can run model/tool loops without credentials or network access. | `DONE` |
| `FOUND-006` | Implement token accounting primitives. | `DEC-006` | Every request creates usage counters, provider usage reconciles estimates, and cumulative totals are testable; streaming-specific accounting is deferred to `MODEL-002`. | `DONE` |

## Phase 2: Local Tools and Persistence

| ID | Work item | Depends on | Done when | Status |
| --- | --- | --- | --- | --- |
| `TOOL-001` | Implement bounded filesystem inspection. | `FOUND-004`, `DEC-004`, `DEC-007` | `fs.list`, `fs.stat`, `fs.read`, `fs.search`, and `fs.hash` enforce workspace, ignore, symlink, size, and cancellation rules. | `DONE` |
| `TOOL-002` | Implement structured Git inspection. | `TOOL-001`, `DEC-007` | `git.root`, `git.status`, `git.diff`, `git.log`, `git.show`, `git.blame`, and `git.check_ignore` return structured fixture results and refuse unsupported scope. | `DONE` |
| `TOOL-003` | Implement the allowlisted process runner. | `FOUND-004`, `DEC-005`, `DEC-008` | Named test, format, lint, security, vet, and build tasks enforce arguments, environment, working directory, timeout, cancellation, and output limits. | `DONE` |
| `TOOL-004` | Implement patch validation, preview, application, rename, and formatter integration. | `TOOL-001`, `TOOL-003`, `DEC-004`, `DEC-005` | `code.check_patch`, `code.apply_patch`, `code.rename`, and `code.format` detect conflicts, produce diffs, use atomic writes, and never bypass approval. | `DONE` |
| `POL-001` | Implement approval and safety policy evaluation. | `FOUND-004`, `TOOL-001`, `TOOL-002`, `TOOL-003`, `TOOL-004`, `DEC-005` | Automatic, confirmation, denied, cancelled, and non-interactive decisions are visible and tested. | `DONE` |
| `SESSION-001` | Implement project-local session persistence. | `FOUND-006`, `DEC-006`, `DEC-007` | Manifests, ordered events, results, continuation data, locks, redaction, bounded output, atomic writes, recovery, and `--ephemeral` are tested below the effective invocation path. | `DONE` |

## Phase 3: Model and Orchestration

| ID | Work item | Depends on | Done when | Status |
| --- | --- | --- | --- | --- |
| `MODEL-001` | Implement the OpenAI-compatible HTTP Responses adapter. | `FOUND-002`, `FOUND-005`, `FOUND-006`, `DEC-001`, `DEC-009` | API-root joining, bearer credentials, request IDs, text output, function calls, function results, errors, timeouts, and usage reconciliation pass fake-backend tests. | `DONE` |
| `MODEL-002` | Implement streaming support if accepted in `DEC-001`. | `MODEL-001`, `DEC-009` | SSE deltas, terminal events, cancellation, partial output, and usage reconciliation produce the same normalized events as non-streaming requests. | `DEFERRED` |
| `CONTEXT-001` | Implement the bounded context builder. | `TOOL-001`, `FOUND-006`, `SESSION-001` | Instructions, project guidance, user input, selected files, Git state, and tool results are selected within token and privacy budgets. | `DONE` |
| `AGENT-001` | Implement the model/tool orchestration loop. | `MODEL-001`, `FOUND-004`, `POL-001`, `SESSION-001`, `CONTEXT-001` | The loop handles text, function calls, approval, normalized results, retries, cancellation, incomplete responses, session events, and final summaries. | `DONE` |
| `CLI-001` | Connect `doit run` and `doit agent` to the orchestration loop. | `FOUND-003`, `AGENT-001` | Human and JSON output, stdin requests, interactive prompts, errors, token usage, changed paths, and exit codes match the CLI design. | `DONE` |
| `MVP-001` | Prove the end-to-end MVP acceptance scenario. | `CLI-001`, `MODEL-001`, `TOOL-001`, `TOOL-003`, `TOOL-004`, `SESSION-001` | The documented `doit run "explain this repository"` fake-backend scenario passes without credentials or network access. | `DONE` |

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
| 2026-09-12 | `DEC-001`-`DEC-009` | Frozen the Safe Local MVP compatibility, configuration, contracts, tool schemas, approvals, token accounting, Git scope, platform, and fake-backend decisions. | [Phase 0 Decision Record](#phase-0-decision-record); [design open decisions](design.md#11-open-decisions) |
| 2026-09-12 | `FOUND-001`-`FOUND-006` | Implemented the Phase 1 Foundation packages, CLI shell, configuration loader, tool and policy contracts, fake clients, session contracts, and token accounting. | `go test ./...`; `go vet ./...`; `golangci-lint run`; `gosec ./...` |
| 2026-09-12 | `TOOL-001`-`TOOL-004`, `POL-001`, `SESSION-001` | Implemented bounded filesystem and Git inspection, allowlisted process execution, reviewable code operations, approval coverage, and persistent/ephemeral sessions. | `go test ./...`; `go vet ./...`; `golangci-lint run`; `gosec ./...` |
| 2026-09-12 | `MODEL-001`, `CONTEXT-001`, `AGENT-001`, `CLI-001`, `MVP-001` | Implemented the Responses HTTP adapter, bounded context builder, model/tool orchestration, stdin-aware CLI composition, process validation tool, and deterministic end-to-end CLI vertical slice. `MODEL-002` remains deferred by `DEC-001`. | `git diff --check`; `go test ./...`; `go vet ./...`; `golangci-lint run`; `gosec ./...` |
| 2026-09-12 | `RUN-001` | Validated the local Docker Ollama backend at `http://127.0.0.1:11434/v1` with `phi4-mini:latest`; simple `doit run` completed successfully, while installed models rejected tool calls or produced simulated tool prose. | Ollama Responses probe; `go run . --ephemeral --timeout 5m run "Reply with exactly: hello"`; tool-call trials with `phi4-mini`, `phi4-mini-reasoning`, and `deepseek-coder-v2` |
| 2026-09-12 | `RUN-003` | Added bounded Foundry rate-limit retries with `Retry-After` support, jittered backoff, minimum request pacing, and distinct exhausted-throttle errors. | `go test ./internal/modelhttp`; full repository validation |
