# Definition

This document defines the vocabulary used by `doit`. Detailed behavior, interfaces, and delivery stages are documented in [design.md](design.md); these definitions keep the purpose, implementation, and user-facing documentation aligned.

## Product Concepts

### `doit`

The command-line workspace assistant described by this project. It coordinates a model backend, repository context, controlled tools, approval policy, session state, and validation results.

### Workspace

The directory whose files and development processes are available to the current invocation. Filesystem writes and local process execution are confined to this scope by default.

### Effective Invocation Path

The absolute path used as the anchor for the current invocation: the current working directory, or the directory selected by `-C` or `--directory`. Project-local session data is stored below this path and is not silently moved to a parent repository.

### Command Mode

A non-interactive invocation that performs one focused task and exits with a stable status code. Examples include `doit run`, `doit develop`, `doit review`, and `doit test`.

### Agent Mode

An interactive session in which the user and model work through multiple related steps. Running `doit` in a terminal or using `doit agent` starts this mode.

### MVP

The smallest safe local release: CLI startup, configuration, an interactive loop, an OpenAI-compatible model adapter, focused workspace tools, token accounting, local sessions, explicit approvals, and deterministic tests.

## Model Concepts

### Backend

The HTTP(S) service that hosts or routes a model request. A backend may be a public provider, private service, local model server, gateway, or enterprise proxy.

### Backend Profile

A named configuration containing an API root, opaque model identifier, credential reference, optional headers, capability settings, and transport limits. A user can switch profiles without changing the task or repository workflow.

### OpenAI-Compatible Backend

A backend that implements the required subset of the OpenAI Responses API: `POST {api_root}/responses`, text input and output, and client-defined function calling. Streaming, structured output, multimodal input, managed state, and provider-hosted tools are optional capabilities.

### Model Client Adapter

The provider-neutral component that serializes internal requests, authenticates with a backend, parses Responses API output, normalizes errors, handles streaming, and exposes model events to the orchestrator.

### Capability Profile

The set of features known to work for a backend and model, such as streaming, structured output, multimodal input, managed state, or provider-hosted tools. Optional request fields are sent only when enabled by this profile.

### Token Accounting

The per-request and cumulative measurement of `input_tokens`, `output_tokens`, and `total_tokens`. Each value records whether it came from provider usage, a local estimate, mixed sources, or an unknown source. Estimates must never be presented as exact billing data.

## Tool Concepts

### Tool

A narrow, structured capability the model can request through function calling. A tool has a stable name, validated arguments, a risk class, limits, cancellation behavior, and a normalized result.

### Filesystem Tool

A read-oriented tool for listing, inspecting, reading, searching, or hashing workspace content. Filesystem tools do not provide unrestricted file writes or path traversal.

### Code Tool

A tool that validates or applies a reviewable code operation, such as a unified patch, rename, or configured formatter result. Code tools produce affected paths and a diff or change summary.

### Git Tool

A structured tool for repository inspection or explicitly approved Git state changes. Core Git tools are read-only and include repository root, status, diff, log, show, blame, and ignore explanations. Remote changes and history rewrites are outside the core agent.

### Process Runner

The allowlisted execution component for named development tasks such as tests, formatting checks, linting, security scans, and builds. The model selects a configured task; it does not provide arbitrary shell pipelines or unrestricted environment values.

### Normalized Result

A provider- and tool-independent result containing a status, structured data, bounded diagnostics, changed paths when applicable, and truncation information. `succeeded`, `failed`, `denied`, and `cancelled` are distinct outcomes.

### Approval Policy

The rules that classify a requested action as automatic, confirmation-based, or rejected. Read-only inspection may be automatic; writes, deletes, process execution, and Git state changes require the policy level defined by the user and the current workflow.

## Session Concepts

### Session

A sequence of user requests, model responses, tool calls, approvals, validation results, token usage events, and lifecycle state for one related task or interactive interaction.

### Durable Session

A session persisted below:

```text
<effective invocation path>/.doit/sessions/<session-id>/
```

It contains redacted metadata, ordered events, a completion result, and optional provider continuation data required for resume. Durable sessions must not contain raw credentials, unredacted secrets, unbounded file contents, or hidden model reasoning.

### Ephemeral Session

A session held in memory for one invocation. It uses the same orchestration and approval behavior but writes no durable session directory and cannot be resumed after exit. It is enabled with `--ephemeral`.

### Session Event

An ordered record describing a request, public model message, tool call, approval, tool result, validation result, token usage, or lifecycle transition. Events are bounded and redacted before persistence.

### Session Export

An explicit operation that creates a shareable, redacted session artifact. Export is not an automatic upload or synchronization mechanism.

## Workflow Concepts

### Context Builder

The component that selects relevant user input, instructions, repository metadata, file content, and previous results within token and privacy limits.

### Validation

A configured check that determines whether a task is complete and trustworthy, such as a test, formatter, linter, security scan, or build. A failed required validation produces a failed task outcome even when the model supplies a useful explanation.

### Effective Scope

The set of paths, Git revisions, tools, and processes permitted for one task. The scope is derived from the workspace, command arguments, pathspecs, approval policy, and configured limits.
