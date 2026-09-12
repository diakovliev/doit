# Purpose

`doit` is a command-line workspace assistant for software development. It combines an AI model with repository context and controlled local tools so a developer can ask for work, inspect proposed actions, approve changes, and review the result from a terminal.

This document describes why the project exists. The technical architecture and implementation boundaries are defined in [design.md](design.md).

## Mission

The project aims to make development, code review, and testing workflows more effective without removing the developer from the loop. `doit` should fit into existing repositories and development tools rather than requiring a new hosted workflow or replacing an IDE, compiler, test runner, or Git.

## Objectives

- Provide a Git-like CLI with one-shot commands for development, review, and testing, plus an interactive agent shell.
- Support models hosted by OpenAI or by any local, hosted, gateway, or enterprise backend that implements the project's OpenAI Responses compatibility contract.
- Ground model requests in focused workspace context instead of sending an entire repository by default.
- Expose filesystem inspection, code manipulation, Git inspection, and validation as controlled, reviewable tools.
- Keep file writes, process execution, and Git state changes visible and subject to approval policy.
- Store resumable session data below `<effective invocation path>/.doit/sessions/<session-id>/`, with redaction, bounded outputs, and an explicit ephemeral mode.
- Report input, output, and total token usage from the first model request, clearly distinguishing provider-authoritative values from estimates.
- Integrate with existing formatters, test runners, linters, security scanners, and Git workflows.
- Remain secure, reliable, interruptible, and useful when a model, tool, or validation command fails.
- Provide documentation and contributor guidance that support feedback, testing, and responsible extension of the project.

## Product Boundaries

`doit` is local and developer-controlled by default. It must not modify a workspace, execute arbitrary commands, expose credentials, push to remotes, or rewrite Git history without an explicit policy and user authorization. The initial product focuses on text-capable model requests, client-defined function tools, local repository context, and deterministic validation.

The project does not initially aim to support every model API, replace a full IDE or CI system, provide unrestricted autonomous operation, or synchronize session data to a remote service.

## Success Criteria

The project is successful when a developer can:

- Start an interactive session with `doit` or run a focused request with `doit run`.
- Select a model backend through configuration without changing the task workflow.
- Ask the agent to inspect a repository, propose a small change, and see a reviewable diff before approval.
- Run configured tests and validation commands and receive truthful status and exit codes.
- Resume or inspect a local session without exposing secrets or unbounded repository content.
- Understand the model usage, files changed, tools executed, and checks performed for each task.

## Conclusion

`doit` exists to make AI-assisted software work practical inside ordinary developer workflows. Its value comes from combining provider flexibility with local context, controlled actions, transparent token and session accounting, and human review at the points where changes or execution carry risk.
