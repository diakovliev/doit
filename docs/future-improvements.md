# Future Improvements Plan

This document outlines the planned evolution of the `doit` CLI, moving from a functional MVP to a robust, high-performance developer tool.

## Overview

The goal is to improve the maintainability of the codebase, enhance the user experience in complex terminal environments, and provide deeper observability into AI agent performance.

---

## Phase 1: Foundation - UI Package Extraction
**Goal**: Decouple terminal rendering logic from application orchestration.

### Tasks
- [x] **Task 1.1: Create `internal/ui` package**
    - Move `progressLine` and ANSI sequence constants to `internal/ui`.
    - Define a clean interface for progress updates.
- [x] **Task 1.2: Refactor `internal/app/app.go`**
    - Remove terminal-specific logic from `Handler`.
    - Inject the `ui.Renderer` dependency into the application layer.
- [x] **Task 1.3: Update existing tests**
    - Ensure `internal/app/app_test.go` still validates the output correctly using the new package.

---

## Phase 2: Observability - Debug & Trace Mode
**Goal**: Provide visibility into the "black box" of LLM interactions.

### Tasks
- [x] **Task 2.1: Implement `--debug` flag**
    - Add a configuration option to toggle raw output.
- [x] **Task 2.2: Raw Stream Interceptor**
    - Create a middleware-like component in the execution pipeline that can intercept and log raw LLM/Tool responses to `stderr` or a log file.
- [x] **Task 2.3: Structured Logging**
    - (Optional) Integrate a structured logger (e.g., `slog`) to allow for machine-readable debug logs.

---

## Phase 3: Robustness - Defensive Terminal Management
**Goal**: Ensure a glitch-free experience even when the environment changes.

### Tasks
- [x] **Task 3.1: Terminal Resize Handling (SIGWINCH)**
    - Implement a listener for terminal resize signals.
    - Re-calculate `progressLine` width/positioning on resize.
- [x] **Task 3.2: Terminal State Recovery**
    - Ensure that if the CLI crashes, the terminal state (cursor visibility, colors) is restored.

---

## Phase 4: Advanced Performance & Metrics
**Goal**: Turn `doit` into a profiling tool for agentic workflows.

### Tasks
- [x] **Task 4.1: Granular Metric Collection**
    - Expand `agent.Outcome` to include per-step timing (e.g., `tool_latency`, `llm_latency`).
- [x] **Task 4.2: Event-Driven Context Building**
    - Refactor `internal/contextbuilder` to use an event-based stream rather than accumulating large strings.
    - This allows for more efficient processing of extremely long-running sessions.

---

## Maintenance & Quality Gates
*All tasks must pass the existing `go test ./...` and `golangci-lint run ./...` gates before being marked as `DONE`.*
