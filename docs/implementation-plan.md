# Implementation Plan

**Status:** Phase 6 session context and retrieval complete; cloud synchronization is out of scope

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
- Repository-configured tests, formatting, lint, and security checks pass.

## Phase 0: Decision Freeze

These decisions must be resolved before the corresponding implementation task starts. The recommended defaults are recorded in [design.md](design.md#123-recommended-mvp-defaults).

| ID | Decision | Acceptance evidence | Depends on | Status |
| --- | --- | --- | --- | --- |
| `DEC-001` | Freeze the core OpenAI Responses compatibility level: non-streaming text plus function calling; decide whether streaming is a Phase 1 requirement. | A short compatibility checklist names required and optional fields and fallback behavior. | None | `DONE` |
| `DEC-002` | Freeze configuration format, locations, precedence, profile names, and environment variable names. | Example configuration and precedence table are committed to the docs. | None | `DONE` |
| `DEC-003` | Freeze core contracts for `ModelClient`, `Tool`, `ToolResult`, `ApprovalPolicy`, `SessionStore`, and `TokenCounter`. | Interfaces include cancellation, errors, normalized results, and usage fields. | `DEC-001` | `DONE` |
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
- Streaming is not required for the Phase 1 MVP. `MODEL-002` is scheduled for Phase 5 alongside MCP support; non-streaming fallback remains mandatory.

#### `DEC-002` Configuration

- Configuration uses JSON to keep the initial implementation dependency-light and directly supported by the standard library. Project configuration may define named process tasks with executables, argument arrays, and optional environments; no language-specific tasks are built in.
- Project configuration is a non-secret `.doit/config.json` below the effective invocation path. User configuration lives in the operating-system user configuration directory under `doit/config.json`.
- Precedence is built-in defaults, project configuration, user configuration, `DOIT_*` environment overrides, then command-line flags.
- `DOIT_CONFIG_FILE` may select an explicit configuration file. `DOIT_PROFILE`, `DOIT_MODEL`, and `DOIT_API_ROOT` are supported direct overrides.
- Credentials are referenced by environment-variable name or operating-system credential reference; raw tokens are not stored in configuration files.

#### `DEC-003` Core Contracts

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
- `doit agent` requires confirmation for process execution, code writes, renames, deletes, and all Git index or history changes.
- `doit run` is trusted workspace automation: configured operations contained by the effective workspace may execute without an interactive prompt, including destructive file changes, local Git mutations, and configured process tasks. Human review happens through the resulting change request, diff, validation results, and session evidence.
- The effective workspace is the security boundary. Path escapes, symlink or working-directory escapes, and capabilities that are not explicitly configured remain denied; risk labels control reporting and review requirements but do not force confirmation inside trusted workspace automation.
- JSON output remains non-interactive and does not emit approval prompts; confirmation-required actions are denied unless workspace automation is explicitly active.

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

- Phase 1 is validated on Windows 10/11 with PowerShell 5.1 or later and `pwsh` 7 or later. The implementation avoids shell-specific quoting so later POSIX support remains possible.
- Paths use platform-aware path handling. Process tasks use configured argument arrays rather than shell command strings.
- Process cancellation is context-driven with bounded output, bounded duration, and an allowlisted environment.

#### `DEC-009` Fake Backend Scenarios

- The test backend is an in-process `httptest.Server` with deterministic responses for text, function calls, provider usage, missing usage, malformed JSON, authentication failure, rate limiting, server failure, timeout, and cancellation.
- Streaming is represented as an unsupported capability in the MVP fixture set; SSE scenarios begin with `MODEL-002` if streaming is enabled later.
- The default test suite never requires real credentials or network access.

## Phase 1: Foundation

| ID | Work item | Depends on | Done when | Status |
| --- | --- | --- | --- | --- |
| `FOUND-001` | Establish the package layout and core error types. | `DEC-003` | Packages have clear ownership, errors preserve cause and category, and the empty application still builds. | `DONE` |
| `FOUND-002` | Implement configuration loading and backend profiles. | `DEC-002` | Defaults, file configuration, environment overrides, flags, credential references, and validation are tested. | `DONE` |
| `FOUND-003` | Implement the CLI root, global options, help, version, command dispatch, and stable exit codes. | `DEC-002`, `DEC-008` | `doit`, `doit run`, `doit agent`, `--format`, `--directory`, `--profile`, `--ephemeral`, and invalid-input paths behave as documented. | `DONE` |
| `FOUND-004` | Implement the normalized tool contract and registry. | `DEC-003`, `DEC-004` | Tools register schemas, risk classes, limits, cancellation behavior, and normalized results. | `DONE` |
| `FOUND-005` | Implement deterministic fake model and process clients. | `DEC-003`, `DEC-009` | Unit and integration tests can run model/tool loops without credentials or network access. | `DONE` |
| `FOUND-006` | Implement token accounting primitives. | `DEC-006` | Every request creates usage counters, provider usage reconciles estimates, and cumulative totals are testable; streaming-specific accounting is deferred to `MODEL-002`. | `DONE` |
| `FOUND-007` | Make process tasks repository-configured and language-neutral. | `FOUND-002`, `TOOL-003` | Project configuration defines allowlisted executables, arguments, and environments; runtime registration assumes no compiler, formatter, linter, or security scanner. | `DONE` |

## Phase 2: Local Tools and Persistence

| ID | Work item | Depends on | Done when | Status |
| --- | --- | --- | --- | --- |
| `TOOL-001` | Implement bounded filesystem inspection. | `FOUND-004`, `DEC-004`, `DEC-007` | `fs.list`, `fs.stat`, `fs.read`, `fs.search`, and `fs.hash` enforce workspace, ignore, symlink, size, and cancellation rules. | `DONE` |
| `TOOL-002` | Implement structured Git inspection. | `TOOL-001`, `DEC-007` | `git.root`, `git.status`, `git.diff`, `git.log`, `git.show`, `git.blame`, and `git.check_ignore` return structured fixture results and refuse unsupported scope. | `DONE` |
| `TOOL-003` | Implement the allowlisted process runner. | `FOUND-004`, `DEC-005`, `DEC-008` | Repository-configured check, format, lint, analysis, security, and build tasks enforce arguments, environment, working directory, timeout, cancellation, and output limits without assuming a programming language. | `DONE` |
| `TOOL-004` | Implement patch validation, preview, application, rename, and formatter integration. | `TOOL-001`, `TOOL-003`, `DEC-004`, `DEC-005` | `code.check_patch`, `code.apply_patch`, `code.rename`, and `code.format` detect conflicts, produce diffs, use atomic writes, and never bypass approval. | `DONE` |
| `TOOL-005` | Add structured workspace-scoped local Git mutations. | `TOOL-002`, `POL-001`, `DEC-005`, `DEC-007` | `git.stage`, `git.unstage`, `git.commit`, and `git.restore` enforce path scope, approval, staged-path selection, bounded messages, and no remote or arbitrary command execution. | `DONE` |
| `TOOL-006` | Add structured workspace directory operations. | `TOOL-001`, `POL-001`, `DEC-005`, `DEC-007` | `fs.mkdir` and `fs.remove` create or remove explicit workspace paths with rooted confinement, root and `.git` protection, recursive controls, approval metadata, and focused tests. | `DONE` |
| `TOOL-007` | Add explicit workspace file write and path move operations. | `TOOL-006`, `POL-001`, `DEC-005`, `DEC-007` | `fs.write` creates or explicitly overwrites bounded files, and `fs.move` moves files or directories without replacement, all through rooted workspace operations and focused tests. | `DONE` |
| `INIT-001` | Add project initialization scaffolding. | `FOUND-002`, `FOUND-003`, `CONTEXT-002` | `doit init` creates a non-secret `.doit/config.json`, instruction templates, and skill templates below the selected workspace without contacting a model or overwriting existing files. | `DONE` |
| `POL-001` | Implement approval and safety policy evaluation. | `FOUND-004`, `TOOL-001`, `TOOL-002`, `TOOL-003`, `TOOL-004`, `DEC-005` | Automatic, confirmation, denied, cancelled, and non-interactive decisions are visible and tested. | `DONE` |
| `SESSION-001` | Implement project-local session persistence. | `FOUND-006`, `DEC-006`, `DEC-007` | Manifests, ordered events, results, continuation data, locks, redaction, bounded output, atomic writes, recovery, and `--ephemeral` are tested below the effective invocation path. | `DONE` |
| `SESSION-002` | Preserve valid JSON during session redaction. | `SESSION-001` | Redaction handles escaped strings structurally and persisted events remain valid JSON under arbitrary tool/file content. | `DONE` |

## Phase 3: Model and Orchestration

| ID | Work item | Depends on | Done when | Status |
| --- | --- | --- | --- | --- |
| `MODEL-001` | Implement the OpenAI-compatible HTTP Responses adapter. | `FOUND-002`, `FOUND-005`, `FOUND-006`, `DEC-001`, `DEC-009` | API-root joining, bearer credentials, request IDs, text output, function calls, function results, errors, timeouts, and usage reconciliation pass fake-backend tests. | `DONE` |
| `MODEL-002` | Implement streaming support if accepted in `DEC-001`. | `MODEL-001`, `DEC-009` | SSE deltas, terminal events, cancellation, partial output, and usage reconciliation produce the same normalized events as non-streaming requests. | `DEFERRED` |
| `CONTEXT-001` | Implement the bounded context builder. | `TOOL-001`, `FOUND-006`, `SESSION-001` | Instructions, project guidance, user input, selected files, Git state, and tool results are selected within token and privacy budgets. | `DONE` |
| `CONTEXT-002` | Load repository instructions and skills into model context. | `CONTEXT-001`, `DEC-007` | Existing `.github` guidance and bounded `AGENTS.md`, `.github/instructions`, `.github/skills`, `.agents/skills`, and `.doit` instructions/skills are discovered deterministically without exposing other `.doit` state. | `DONE` |
| `AGENT-001` | Implement the model/tool orchestration loop. | `MODEL-001`, `FOUND-004`, `POL-001`, `SESSION-001`, `CONTEXT-001` | The loop handles text, function calls, approval, normalized results, retries, cancellation, incomplete responses, session events, and final summaries. | `DONE` |
| `AGENT-002` | Preserve valid function-call history across budget trimming and session resume. | `AGENT-001`, `CONTEXT-001` | Resumed and budget-trimmed Requests never contain orphaned `function_call_output` items or incomplete function-call pairs. | `DONE` |
| `CLI-001` | Connect `doit run` and `doit agent` to the orchestration loop. | `FOUND-003`, `AGENT-001` | Human and JSON output, stdin requests, interactive prompts, errors, token usage, changed paths, and exit codes match the CLI design. | `DONE` |
| `MVP-001` | Prove the end-to-end MVP acceptance scenario. | `CLI-001`, `MODEL-001`, `TOOL-001`, `TOOL-003`, `TOOL-004`, `SESSION-001` | The documented `doit run "explain this repository"` fake-backend scenario passes without credentials or network access. | `DONE` |

## Phase 4: Hardening and Workflows

| ID | Work item | Depends on | Done when | Status |
| --- | --- | --- | --- | --- |
| `HARD-001` | Add security and boundary tests. | `MVP-001` | Workspace escapes, symlink escapes, command injection, secret leakage, oversized output, interrupted processes, and unsafe Git operations are covered. | `DONE` |
| `HARD-002` | Add reviewable operational diagnostics. | `MVP-001` | Request IDs, provider errors, rate limits, tool timings, validation status, and redacted session evidence are available without credentials. | `DONE` |
| `HARD-003` | Unify the effective-workspace boundary and side-effect policy. | `MVP-001`, `HARD-001`, `POL-001` | Trusted workspace automation can autonomously perform configured operations contained by the effective workspace, including destructive file, Git, and process actions; shared checks reject path, symlink, and working-directory escapes, and change-request evidence exposes effects for human review. | `DONE` |
| `HARD-004` | Enforce the normalized tool contract and result accounting. | `FOUND-004`, `AGENT-001`, `HARD-003` | Tool schemas and arguments are validated centrally; declared timeouts, output limits, and argument limits are enforced; bounded diagnostics and changed paths reach the final outcome and session result. | `DONE` |
| `TOOL-008` | Implement reviewable change sets and transactional edits. | `TOOL-004`, `TOOL-006`, `TOOL-007`, `HARD-003`, `HARD-004` | Writes produce bounded, reviewable change-set evidence with before/after hashes, approval identity, conflict detection, and atomic-or-rollback behavior across multi-file operations; patch tools support pre-apply preview and direct trusted mutations report applied evidence for human change-request review. | `DONE` |
| `VALID-001` | Add structured validation task contracts and results. | `FOUND-007`, `HARD-004` | Configured tasks declare purpose and side effects, preserve the approved environment policy, and return normalized pass/fail, timeout, and file/line diagnostic data instead of only raw output. | `DONE` |
| `TOOL-009` | Improve code search and repository change review. | `TOOL-001`, `TOOL-002`, `TOOL-008` | Search supports regex, case, context, file filters, and reliable globs; review can include untracked files and emit a complete bounded change patch. | `DONE` |
| `AGENT-003` | Add capability-scoped tool profiles. | `FOUND-004`, `MODEL-001`, `HARD-004` | Inspection, editing, validation, Git-write, and destructive capabilities can be exposed separately, reducing prompt/schema overhead and supporting smaller local models without changing tool contracts. | `DONE` |
| `GIT-001` | Add local branch and worktree awareness. | `TOOL-002`, `HARD-003` | Read-only branch/upstream/worktree state is structured, and explicitly authorized local branch operations remain workspace-scoped; remote operations stay excluded. | `DONE` |
| `WORK-001` | Add dedicated `develop`, `review`, and `test` workflows. | `MVP-001`, `HARD-001`, `HARD-003`, `HARD-004`, `TOOL-008`, `TOOL-009`, `VALID-001` | Each workflow has focused context selection, output, validation, review, and exit-status tests. | `DONE` |
| `WORK-002` | Add session resume, export, pruning, and recovery commands. | `SESSION-001`, `MVP-001` | Interrupted and completed sessions can be safely inspected, resumed, exported, and pruned under the documented policy. | `DONE` |
| `WORK-002A` | Reuse the latest durable session automatically. | `SESSION-001`, `MVP-001` | Subsequent runs reuse the newest non-active resumable workspace session, replay bounded public turns, and support explicit fresh-session opt-outs. | `DONE` |
| `WORK-003` | Add CI for the repository's required checks. | `HARD-001` | CI runs configured tests, formatting, lint, security, deterministic integration tests, and platform-specific checks without live model credentials. | `DONE` |

Recommended implementation order for the new work is `HARD-003` -> `HARD-004` -> `TOOL-008`/`VALID-001` -> `TOOL-009` -> `AGENT-003` -> `GIT-001` -> `WORK-001`. This order fixes the execution boundary and observability contract before adding more automation surface.

## Deferred Work

These items are intentionally outside the MVP critical path:

- `DEFER-001`: Native adapters for APIs that do not satisfy the OpenAI Responses compatibility contract.
- `DEFER-002`: Multimodal input and provider-hosted tools.
- `DEFER-004`: Git push, merge, force-push, history rewriting, and remote management.
- `DEFER-006`: General plugin or external extension protocols remain undecided; do not implement or design around them until the MCP boundary has been evaluated.
- `DEFER-007`: Additional interactive UI frontends and automatic model discovery.

Deferred work must not change the approval, observability, token accounting, session redaction, or deterministic validation boundaries established by the MVP.

## Phase 5: Protocol Extensions

| ID | Work item | Depends on | Done when | Status |
| --- | --- | --- | --- | --- |
| `MODEL-002` | Add optional OpenAI Responses SSE streaming. | `MODEL-001`, `DEC-009`, `HARD-002` | SSE deltas, terminal completion/failure events, cancellation, partial output, request/provider IDs, and usage reconciliation produce the same normalized outcome contract as non-streaming requests, with mandatory fallback. | `DONE` |
| `MCP-001` | Support explicitly configured MCP tool servers. | `HARD-003`, `HARD-004`, `AGENT-003` | MCP tool definitions and results map into the normalized contract; server transport, network effects, tool capabilities, timeouts, output bounds, redaction, change-set evidence, and trusted workspace automation policy are enforced and tested. | `DONE` |

Cloud synchronization, hosted analytics, and cloud persistence are explicitly out of scope and must not be added as implementation tasks. General plugin architecture remains undecided; MCP is the preferred extension boundary for external tools.

## Phase 6: Session Context and Retrieval

| ID | Work item | Depends on | Done when | Status |
| --- | --- | --- | --- | --- |
| `SESSION-003` | Bound cumulative session usage and add model-visible bounded session history retrieval. | `SESSION-001`, `SESSION-002`, `CONTEXT-001`, `AGENT-002`, `HARD-004` | `max_input_tokens` remains a per-request context bound; `max_session_tokens` is propagated and reported as a cumulative usage threshold without prematurely stopping valid tool loops; recent resume history remains pair-safe; `session.history` searches only the active redacted session with event, cursor, event-count, and byte limits; continuation state, secrets, raw session files, and other sessions remain inaccessible; the active-session context hook is ready for future remote workspace-lease binding; focused tests cover threshold continuation, history filtering, truncation, and cancellation. | `DONE` |

## Progress Log

| Date | Task ID | Change | Evidence |
| --- | --- | --- | --- |
| 2026-09-12 | `PLAN-001` | Created the initial trackable implementation plan from the project design. | This document |
| 2026-09-12 | `DEC-001`-`DEC-009` | Frozen the Safe Local MVP compatibility, configuration, contracts, tool schemas, approvals, token accounting, Git scope, platform, and fake-backend decisions. | [Phase 0 Decision Record](#phase-0-decision-record); [design open decisions](design.md#11-open-decisions) |
| 2026-09-12 | `FOUND-001`-`FOUND-006` | Implemented the Phase 1 Foundation packages, CLI shell, configuration loader, tool and policy contracts, fake clients, session contracts, and token accounting. | Repository validation matrix |
| 2026-09-12 | `TOOL-001`-`TOOL-004`, `POL-001`, `SESSION-001` | Implemented bounded filesystem and Git inspection, allowlisted process execution, reviewable code operations, approval coverage, and persistent/ephemeral sessions. | Repository validation matrix |
| 2026-09-12 | `MODEL-001`, `CONTEXT-001`, `AGENT-001`, `CLI-001`, `MVP-001` | Implemented the Responses HTTP adapter, bounded context builder, model/tool orchestration, stdin-aware CLI composition, process validation tool, and deterministic end-to-end CLI vertical slice. `MODEL-002` remains deferred by `DEC-001`. | Repository validation matrix |
| 2026-09-12 | `RUN-001` | Validated a local Ollama-compatible backend; simple requests completed successfully, while some installed models rejected tool calls or produced simulated tool prose. | Ollama Responses probe; local backend trials |
| 2026-09-12 | `RUN-003` | Added bounded Foundry rate-limit retries with `Retry-After` support, jittered backoff, minimum request pacing, and distinct exhausted-throttle errors. | Repository validation matrix |
| 2026-09-12 | `CLI-001` | Wired explicit interactive approval prompts for `doit agent`; `doit run` and JSON mode remain non-interactive and deny confirmation-required tools. | Repository validation matrix |
| 2026-09-12 | `AUTO-001` | Made `doit run` explicit workspace automation: local write/process/destructive tools can execute without prompts, while path escapes, network access, remote operations, and rejected-risk tools remain denied. | Repository validation matrix |
| 2026-09-12 | `TOOL-004` | Hardened code-operation outcomes so patch previews and applications report actual content changes, identical writes are skipped, and configured format tasks are exposed. | Repository validation matrix |
| 2026-09-12 | `CLI-001` | Clarified root entrypoint integration by extracting process arguments once and wiring a named application handler. | Repository validation matrix |
| 2026-09-12 | `WORK-002A` | Implemented automatic durable-session reuse: subsequent runs select the newest non-active resumable session in the effective workspace by default, replay bounded public turns, and support `--new-session`, `--no-resume`, and `--ephemeral` opt-outs. | Repository validation matrix |
| 2026-09-12 | `TOOL-003`, `CLI-001` | Published the configured process-task enum and structured schema to model calls, improved unknown-task diagnostics, and replaced the placeholder entrypoint test with root CLI argument/exit-code coverage. | Repository validation matrix |
| 2026-09-12 | `TOOL-003` | Made process deadlines model-selectable with human-readable duration strings, a 10-minute per-process cap, and caller-controlled outer cancellation. | Repository validation matrix |
| 2026-09-12 | `TOOL-005` | Implemented structured local Git automation for staging, unstaging, committing selected staged paths, and restoring selected paths; remote and arbitrary Git operations remain excluded. | Repository validation matrix |
| 2026-09-12 | `TOOL-003`, `TOOL-005` | Made registered validation task names explicit in the process schema and added production-registry coverage for Git inspection, Git mutation, and process automation tools. | Repository validation matrix |
| 2026-09-12 | `TOOL-005` | Made `git.commit` atomically stage its explicit path group before verifying and committing it, eliminating cross-tool staging-state failures in multi-commit automation. | Repository validation matrix |
| 2026-09-12 | `TOOL-005` | Added fresh changed-path diagnostics when a commit selection is clean and staged-path reporting for `git.stage`, covering stale model path selections. | Repository validation matrix |
| 2026-09-12 | `CONTEXT-001`, `TOOL-005` | Added fresh Git status to every new model context so automatic session reuse cannot silently drive commit grouping from stale conversation paths. | Repository validation matrix |
| 2026-09-12 | `CONTEXT-002` | Started bounded repository guidance discovery for existing `.github` conventions and opt-in `.doit` instructions/skills. | Focused context tests pending |
| 2026-09-12 | `CONTEXT-002` | Added deterministic bounded loading for repository instructions and skills, including `.doit/instructions*` and `.doit/skills/*/SKILL.md`, while excluding other `.doit` state. | Repository validation matrix |
| 2026-09-12 | `AGENT-002` | Started repairing resumed Responses history after provider rejection of orphaned function-call outputs; budget trimming now needs atomic call/output handling and legacy history sanitization. | Focused agent/context tests pending |
| 2026-09-12 | `AGENT-002` | Preserved function-call/output pairs during budget trimming and sanitized orphaned or incomplete tool items from resumed sessions. | Repository validation matrix |
| 2026-09-12 | `SESSION-002` | Reworked event redaction to decode and redact JSON values structurally, preventing quoted or backslash-containing tool output from corrupting session event JSON. | Repository validation matrix |
| 2026-09-12 | `TOOL-006` | Started structured workspace directory operations for explicit mkdir and remove actions; rooted APIs keep paths inside the workspace. | Focused workspacefs tests pending |
| 2026-09-12 | `TOOL-006` | Added model-facing `fs.mkdir` and `fs.remove` with rooted confinement, recursive controls, protected workspace/Git paths, and fixture coverage. | Repository validation matrix |
| 2026-09-12 | `TOOL-007` | Started explicit arbitrary workspace file creation/overwrite and file-or-directory move operations. | Focused workspacefs tests pending |
| 2026-09-12 | `TOOL-007` | Added bounded model-facing `fs.write` and non-replacing `fs.move` for arbitrary files and directories, with overwrite protection and rooted fixture coverage. | Repository validation matrix |
| 2026-09-12 | `FOUND-007` | Started moving process task registration from built-in language-specific tools to repository-configured task definitions. | Focused configuration/app tests pending |
| 2026-09-12 | `FOUND-007` | Replaced built-in language-specific process tasks with repository-configured task definitions and removed language-specific product guidance/examples. | Repository validation matrix |
| 2026-09-14 | `TOOL-001` | Declared the required `properties` for `fs.stat`, `fs.read`, `fs.search`, and `fs.hash` schemas so OpenAI-compatible validators can accept the filesystem tool definitions. | Focused `workspacefs` tests: 7 passed; changed files: `internal/workspacefs/workspacefs.go`, `internal/workspacefs/workspacefs_test.go` |
| 2026-09-14 | `TOOL-001`, `TOOL-002` | Declared empty `properties` objects for `fs.list` and `git.root`, completing LM Studio-compatible JSON Schemas for zero-argument tools; added regression coverage for the empty filesystem schema. | Focused `workspacefs` and `gitinspect` tests: 13 passed; live `lmstudio` probe returned `OK` with exit code 0 |
| 2026-09-14 | `INIT-001` | Started project initialization scaffolding for `.doit` configuration, repository instructions, and skill templates with rooted, non-overwriting file creation. | Focused initialization tests pending |
| 2026-09-14 | `INIT-001` | Added `doit init` parsing, rooted scaffold creation, human/JSON output, repeatable non-overwriting behavior, focused tests, and user documentation. | `go test ./internal/cli ./internal/app ./internal/projectinit`; disposable CLI probe created and re-ran the five-file scaffold successfully |
| 2026-09-14 | `INIT-001` | Cleared the final test-lint complexity finding and completed repository validation. | `go test ./...`, `go vet ./...`, `gofmt -l`, `golangci-lint run`, and `gosec ./...` all passed |
| 2026-09-14 | `PLAN-002` | Converted the generic code-automation toolset review into `HARD-003`, `HARD-004`, `TOOL-008`, `VALID-001`, `TOOL-009`, `AGENT-003`, and `GIT-001`; tightened `WORK-001` dependencies around those prerequisites. | Read-only toolset and policy review; implementation pending |
| 2026-09-14 | `PLAN-003` | Corrected the automation trust model: workspace automation is authorized to perform configured destructive local operations autonomously; the effective workspace boundary and review artifacts, not interactive confirmation, provide the safety contract. | User decision; follow-up policy and change-set implementation pending |
| 2026-09-14 | `HARD-001`, `HARD-003` | Started Phase 4 boundary hardening: rooted code rename/patch writes and real-path validation for process working directories reject symlink escapes while preserving trusted workspace automation. | Focused `codetools` and `processrunner` tests passed |
| 2026-09-14 | `HARD-004` | Propagated unique mutation paths from filesystem, code, and Git adapters into `agent.Outcome` and durable session results. | Focused agent, code, filesystem, and Git tests passed; changed-path regression added |
| 2026-09-14 | `VALID-001` | Added configured task kinds, pass/fail metadata, working-directory identity, and bounded file/line diagnostics to process results. | Focused process, processrunner, config, and app tests passed |
| 2026-09-14 | `TOOL-009` | Extended filesystem search with literal/regex modes, case control, bounded context lines, and basename-friendly globs. | Focused workspacefs tests passed |
| 2026-09-14 | `AGENT-003` | Added configuration-backed `full`, `inspect`, `edit`, `validate`, `git-read`, `git-write`, and `destructive` tool profiles that filter the model-visible registry; `full` remains the default. | Focused tools, config, and app tests passed |
| 2026-09-14 | `GIT-001` | Added read-only `git.branch` and `git.worktree` inspection for branch/upstream divergence and local worktree inventory. | Focused gitinspect tests passed |
| 2026-09-14 | `HARD-001` | Added symlink-escape tests for code rename and process working directories, shell-composition rejection coverage, and retained existing session-redaction, truncation, timeout, and Git-scope tests. | Full repository validation passed |
| 2026-09-14 | `HARD-002` | Added centralized tool execution durations to normalized results; request/provider diagnostics and complete change-request evidence remain follow-up work. | Focused tools tests passed |
| 2026-09-14 | `HARD-004` | Completed centralized required-argument, argument-count, timeout, serialized-output, execution-duration, and changed-path enforcement across the tool registry and agent outcome. | Full validation matrix passed |
| 2026-09-14 | `HARD-001`, `HARD-003`, `VALID-001`, `TOOL-009` | Completed the first Phase 4 hardening slice: rooted/symlink-safe code and process boundaries, shell-composition rejection, structured task results and diagnostics, regex/context search, and bounded untracked-file Git diffs. | Full validation matrix passed; 22 files scanned by `gosec`, 0 issues |
| 2026-09-14 | `HARD-003`, `VALID-001` | Fixed configured process tasks that explicitly select the effective workspace root (`working_directory: "."`); root paths are now accepted while symlink escapes remain denied. | Direct root-task regression passed; `go test ./...`, `go vet ./...`, `gofmt -l`, `golangci-lint run`, and `gosec ./...` all passed |
| 2026-09-14 | `TOOL-008` | Added stable before/after SHA-256 change sets for `code.check_patch`, `code.apply_patch`, and `fs.write`; preview/apply share an ID, state transitions are explicit, and multi-file patch rollback remains atomic. | Focused codetools and workspacefs tests passed |
| 2026-09-14 | `VALID-001` | Promoted structured `process.run` results into agent outcomes and persisted session validation records with task kind, pass/fail, timeout, truncation, duration, working directory, and diagnostics. | Focused agent/session/process tests passed |
| 2026-09-14 | `TOOL-008`, `VALID-001` | Extended change-set and validation evidence through normalized tool results, agent outcomes, and durable session results; completed lint/test refactors and repository validation. | `go test ./...`, `go vet ./...`, `gofmt -l`, `golangci-lint run`, `gosec ./...`, and `git diff --check` all passed |
| 2026-09-14 | `TOOL-008` | Added policy authorization identity to change sets (`workspace-automation`, `interactive-approval`, or `policy-allow`) without changing trusted workspace autonomy. | Focused agent/tools tests and final Phase 4 validation matrix passed |
| 2026-09-14 | `TOOL-008` | Extended normalized change-set evidence to filesystem move/mkdir/remove and Git stage/unstage/commit/restore mutations using bounded path or Git-state fingerprints. | Focused workspacefs and gitinspect tests passed |
| 2026-09-14 | `TOOL-008` | Completed applied change-set evidence for all local filesystem and Git mutation tools; direct-mutation preview orchestration remains the next refinement while code patches retain preview/apply state. | Focused workspacefs/gitinspect tests and lint passed |
| 2026-09-14 | `WORK-001` | Implemented local `develop`, `review`, and configured-task `test` workflows plus `status`, `model test`, `config list`, `doctor`, and session commands. | Focused CLI/app tests passed |
| 2026-09-14 | `WORK-002` | Added session metadata listing, explicit resume by session ID, JSON inspect/export, and bounded prune support. | Focused agent/session tests passed |
| 2026-09-14 | `WORK-003` | Added GitHub Actions validation for tests, vet, formatting, golangci-lint, and gosec. | Workflow file added; local validation matrix passed |
| 2026-09-14 | `HARD-002`, `HARD-003` | Closed diagnostics and effective-workspace boundary hardening after request correlation, validation/session evidence, symlink confinement, trusted automation policy, and redaction coverage were in place. | Full local validation matrix passed |
| 2026-09-14 | `TOOL-008` | Closed the change-set contract: patch operations retain preview/apply state, while trusted direct filesystem and Git mutations emit applied before/after evidence for remote human change-request review. | Full local validation matrix passed |
| 2026-09-14 | `HARD-002` | Exposed generated client request IDs and provider `X-Request-Id`/`Request-Id` values in normalized model responses so session model events retain operational correlation data. | Focused model HTTP tests passed |
| 2026-09-14 | `TOOL-008`, `HARD-002` | Completed the current Phase 4 slice for persisted change-set/validation evidence and request correlation. | `go test ./...`, `go vet ./...`, `gofmt -l`, `golangci-lint run`, `gosec ./...`, and `git diff --check` all passed |
| 2026-09-14 | `WORK-001`, `WORK-002`, `WORK-003` | Completed the Phase 4 command and delivery surface: develop/review/test workflows, status/config/model/doctor diagnostics, session list/inspect/export/resume/prune, and GitHub Actions CI. | Focused CLI/app/agent/session tests passed; full local validation matrix passed |
| 2026-09-14 | `PLAN-004` | Product decision: cloud synchronization and hosted analytics are permanently out of scope; MCP is the planned external-tool interoperability boundary; general plugins remain undecided. | User decision; `MCP-001` added as the next protocol-extension task |
| 2026-09-14 | `PLAN-005` | Scheduled optional Responses SSE streaming in Phase 5 alongside MCP; non-streaming remains the required fallback. | User decision; `MODEL-002` added to Phase 5 |
| 2026-09-14 | `MODEL-002` | Implemented optional Responses SSE streaming with decoded terminal events, partial-output preservation, cancellation propagation, request/provider correlation, usage reconciliation, and orchestration fallback to non-streaming requests. | Focused model HTTP and agent tests; full repository validation matrix passed |
| 2026-09-14 | `MCP-001` | Added official MCP SDK integration with configured stdio and opt-in streamable HTTP transports, namespaced tool discovery, bounded normalized results, structured change-set propagation, conservative annotation handling, network policy metadata, cancellation, and runtime lifecycle cleanup. | In-memory MCP integration/config tests; full repository validation matrix passed |
| 2026-09-14 | `TOOL-001`, `TOOL-009` | Fixed the model-facing `fs.list` schema so nested workspace paths expose `path`, recursion, depth, entry, and ignore controls; clarified scoped `fs.search` behavior and added nested `docs/` listing/search regressions. | Focused workspacefs regression and full repository validation matrix passed |
| 2026-09-14 | `TOOL-001`, `TOOL-009` | Aligned the normalized `fs.search` argument limit with its nine-field schema so complete scoped searches are not rejected by the central tool contract. | Focused full-argument `fs.search` regression and full repository validation matrix passed |
| 2026-09-14 | `SESSION-003` | Defined the three session bounds: per-request input context, cumulative model usage, and bounded transcript retrieval. Specified the active-session-only `session.history` model tool with redaction, cursors, event filters, output limits, and remote workspace-lease scoping. | Design recorded in [design.md](design.md); implementation pending |
| 2026-09-14 | `SESSION-003` | Implemented cumulative session-token accounting and threshold diagnostics without prematurely stopping valid tool loops, per-request compaction, active-session-scoped `session.history`, bounded store queries, cursor/event filtering, cancellation handling, and explicit model guidance for omitted history. | Focused session/agent/contextbuilder tests and full repository validation matrix passed |
| 2026-09-14 | `CLI-001`, `HARD-002` | Replaced newline-heavy interactive progress output with one replaceable terminal status line; preserved newline-delimited progress for captured/CI output and suppressed progress in JSON mode. | Focused app progress tests and full repository validation matrix passed |
| 2026-09-15 | `CLI-001`, `MODEL-002`, `TOOL-003` | Added safe public model-progress labels for thinking, response composition, and tool selection; added relaxed configurable request timeout, 128-round default, 2-minute process default, and 30-minute process maximum while retaining caller deadlines and bounded execution. | Focused agent/model/process/config/app tests and full repository validation matrix passed |
| 2026-09-15 | `CLI-001`, `MODEL-002` | Activated `--verbose` as a bounded public diagnostic trace for response IDs, statuses, usage correlation, stream event types, and explicitly returned public model text; hidden chain-of-thought remains unavailable by design. | Focused agent/app tests and full repository validation matrix passed |
