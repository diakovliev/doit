# doit

`doit` is a command-line workspace assistant for AI-assisted development, code review, and testing. It combines a model backend with bounded repository context and controlled local tools.

The project is designed for OpenAI Responses-compatible backends, including Microsoft Foundry deployments, Ollama-compatible local servers, gateways, and other hosted services.

## Installation

Install the `doit` binary with:

```bash
go install github.com/diakovliev/doit@latest
```

Then run:

```bash
doit --help
doit --version
doit version
```

## Current Status

The current vertical slice supports:

- One-shot `run` and `agent` requests
- OpenAI Responses-compatible HTTP model backends
- Human-readable progress output and JSON result output
- Bounded filesystem, Git, code, and process tools
- Project-local or ephemeral sessions
- Input, output, and total token accounting
- Provider rate-limit handling with bounded retries and backoff
- Deterministic tests without live model credentials

The specialized `develop`, `review`, and `test` workflows are reserved in the CLI but are not yet separate workflow implementations. Responses streaming is supported when enabled by a backend profile. Human-mode progress reports public model activity such as thinking, response composition, and selected tool actions; it never prints hidden reasoning traces.

See the project documents for the full direction and implementation status:

- [Purpose](docs/purpose.md)
- [Definitions](docs/definition.md)
- [Design](docs/design.md)
- [Implementation plan](docs/implementation-plan.md)

## Requirements

- A supported build environment for the `doit` binary
- An OpenAI Responses-compatible model endpoint
- A configured model identifier
- A credential supplied through an environment variable when the backend requires authentication

The root package is intentionally thin. The application behavior lives under `internal/` packages. Repository validation and formatting are configured per workspace rather than tied to a particular programming language.

## Quick Start

Show the CLI help and version:

```powershell
doit --help
doit --version
doit version
```

Global options must appear before the command:

```powershell
doit --profile ollama --timeout 5m run "Explain this repository"
```

Run a request without a positional prompt by piping text through stdin:

```powershell
"Explain the current implementation and list the main packages." | doit --profile ollama run
```

Use an ephemeral session when you do not want `.doit/sessions/` data written:

```powershell
doit --profile ollama --ephemeral --timeout 5m run "Inspect the repository without modifying files."
```

Use JSON output for scripts:

```powershell
doit --profile ollama --format json --ephemeral run "Summarize the repository."
```

For an installed executable, use `doit`:

```powershell
doit --profile ollama --ephemeral run "Summarize the repository."
```

## Command Shape

```text
doit [global options] <command> [arguments]
```

Implemented command paths:

| Command | Purpose |
| --- | --- |
| `doit init` | Create project-local `.doit` configuration, instruction, and skill templates without contacting a model. Existing files are preserved. |
| `doit` | Start the default agent request flow. |
| `doit agent` | Run an agent request, reading a prompt from stdin when no prompt argument is supplied. |
| `doit run <request>` | Run one development-oriented request and exit. |
| `doit develop <request>` | Run a trusted workspace development request with local automation enabled. |
| `doit review <request>` | Run a read-oriented review request without enabling workspace mutations. |
| `doit test [task]` | Run a configured validation task directly, defaulting to `test`. |
| `doit status` | Report the effective workspace, selected profiles, and Git state. |
| `doit model test` | Send a minimal request to the selected backend and report normalized response metadata. |
| `doit config list` | Print the effective non-secret configuration. |
| `doit session list;inspect;export;resume;prune` | Inspect, resume, export, or prune project-local sessions. |
| `doit doctor` | Run local workspace, configuration, and profile diagnostics. |
| `doit --help` | Print CLI usage. Put `--help` before the command. |
| `doit --version` | Print the CLI version. |
| `doit version` | Print the CLI version. |

Use `run` for a general-purpose request, `develop` for trusted workspace automation, and `review` for read-oriented analysis. The local diagnostic commands do not create model sessions unless explicitly documented above.

Initialize a workspace before adding project-specific guidance:

```powershell
doit init
doit -C .\sample init
```

The command creates `.doit/config.json`, `.doit/instructions.md`, instruction and skill README files, and an example skill template. It never overwrites an existing scaffold file, so it can be run again after the project has been customized.

## Global Options

| Option | Meaning | Example |
| --- | --- | --- |
| `-C, --directory <path>` | Use another workspace directory. | `doit -C .\sample --profile ollama run "Inspect this project"` |
| `-p, --profile <name>` | Select a named backend profile. | `doit --profile foundry_deepseek run "Review the code"` |
| `-m, --model <id>` | Override the profile's model identifier. | `doit --profile ollama --model <model-id> run "Explain the project entrypoint"` |
| `--format human` | Print progress and a human result. | `doit --format human run "Summarize"` |
| `--format json` | Print one machine-readable result object. | `doit --format json run "Summarize"` |
| `--ephemeral` | Keep the session in memory and do not persist it. | `doit --ephemeral run "Inspect only"` |
| `--new-session` / `--no-resume` | Start a fresh durable session instead of reusing the latest one. | `doit --new-session run "Start a separate review"` |
| `--timeout <duration>` | Set the request context deadline. | `doit --timeout 10m run "Review the repository"` |
| `--no-color` | Disable terminal styling. | `doit --no-color run "Summarize"` |
| `--quiet` | Reserved output-control flag (currently non-functional). | `doit --quiet run "Summarize"` |
| `--verbose` | Show bounded public model diagnostics and text explicitly returned by the model, including response IDs, statuses, usage, and stream event types. Hidden reasoning is never printed. | `doit --verbose run "Summarize"` |

Use `--` when you need to terminate global option parsing before arguments:

```powershell
doit --profile ollama run -- "Explain the files under internal."
```

## Backend Configuration

Project configuration is stored in `.doit/config.json`. Keep credentials out of this file.

A local Ollama profile can look like this:

```json
{
  "default_profile": "ollama",
  "profiles": {
    "ollama": {
      "api_root": "http://127.0.0.1:11434/v1",
      "model": "<tool-capable-ollama-model>"
    }
  }
}
```

Execution limits can be relaxed per project without making the agent unbounded:

```json
{
  "execution": {
    "request_timeout_ms": 600000,
    "max_rounds": 128,
    "process_default_timeout_ms": 120000,
    "process_max_timeout_ms": 1800000
  }
}
```

These settings control the model HTTP request timeout, maximum model/tool rounds, default configured-process timeout, and maximum configured-process timeout. The caller's `--timeout` remains the hard deadline for the complete invocation.

Provider-specific request parameters can be configured under a backend profile. For Responses reasoning models, for example:

```json
{
  "profiles": {
    "reasoning-model": {
      "api_root": "https://example.test/v1",
      "model": "<model-id>",
      "request_parameters": {
        "reasoning": {"effort": "high"}
      }
    }
  }
}
```

These values are merged into every provider request. Core fields such as `model`, `input`, `tools`, `stream`, and `max_output_tokens` cannot be overridden. Unsupported parameters remain the backend's responsibility and may be rejected by the provider.

An authenticated Microsoft Foundry profile can look like this:

```json
{
  "default_profile": "foundry_deepseek",
  "profiles": {
    "foundry_deepseek": {
      "api_root": "https://daemondeveloperfoundry.services.ai.azure.com/openai/v1",
      "model": "DeepSeek-V4-Flash",
      "api_key_env": "DOIT_FOUNDRY_API_KEY",
      "rate_limit": {
        "tokens_per_minute": 20000,
        "requests_per_minute": 20,
        "max_retries": 3,
        "initial_backoff_ms": 500,
        "max_backoff_ms": 30000
      }
    }
  }
}
```

Set `streaming: true` on a backend profile to opt into Responses SSE events when the backend supports them. The adapter falls back to the normalized non-streaming request path when streaming is unavailable.

Configured MCP tools can be added under `mcp_servers`:

```json
{
  "mcp_servers": {
    "local-tools": {
      "transport": "stdio",
      "command": "<mcp-server>",
      "arguments": ["<server-argument>"]
    }
  }
}
```

MCP servers are explicit capabilities. Streamable HTTP requires `allow_network: true`; discovered tools are filtered by `tool_profile` and use the same timeout, output, redaction, and workspace policy as built-in tools.

Set the credential only in the current shell. Never commit it, put it in a README, or pass it as a CLI argument:

```powershell
$env:DOIT_FOUNDRY_API_KEY = "<rotated-key>"
doit --profile foundry_deepseek --ephemeral --timeout 10m run "Inspect this repository. Do not modify files."
```

Environment overrides include:

```text
DOIT_CONFIG_FILE
DOIT_PROFILE
DOIT_MODEL
DOIT_API_ROOT
DOIT_API_KEY_ENV
DOIT_EPHEMERAL
```

The project configuration may set `tool_profile` to control which capabilities are exposed to the model. Available profiles are `full` (default), `inspect`, `edit`, `validate`, `git-read`, `git-write`, and `destructive`. This controls model-visible tools; workspace confinement and the trusted automation policy still apply.

Configuration precedence is built-in defaults, project configuration, user configuration, `DOIT_*` environment overrides, and command-line flags.

## Ollama in Docker

If an Ollama container is already running on port `11434`, use its OpenAI-compatible endpoint directly. Otherwise a basic local setup is:

```powershell
docker run -d --name doit-ollama -p 11434:11434 ollama/ollama

docker exec -it doit-ollama ollama list
docker exec -it doit-ollama ollama pull <tool-capable-model>
```

Configure the endpoint as `http://127.0.0.1:11434/v1`, then run:

```powershell
doit --profile ollama --ephemeral --timeout 10m run "Inspect the project entrypoint and summarize it. Do not modify files."
```

A model may answer normal text requests while still failing tool workflows. Coding tasks that require filesystem or process tools need a model/backend that emits Responses `function_call` items rather than prose that merely describes a hypothetical tool call.

## Repository Guidance

Before each model request, `doit` loads bounded repository guidance when the files exist:

- `.github/copilot-instructions.md`
- `AGENTS.md`
- `.github/instructions/*.instructions.md`
- `.github/skills/*/SKILL.md`
- `.agents/skills/*/SKILL.md`
- `.doit/instructions.md`
- `.doit/instructions/*.md`
- `.doit/skills/*/SKILL.md`

Instruction files are supplied as repository instructions, and skill files are supplied as repository skills with their source paths. Directory entries are loaded in deterministic order. Each file and the combined guidance have bounded sizes. Other `.doit` state, including sessions and configuration, is not automatically supplied as guidance.

## Microsoft Foundry

The adapter uses the OpenAI-compatible Foundry endpoint directly:

```powershell
$env:DOIT_FOUNDRY_API_KEY = "<rotated-key>"
doit --profile foundry_deepseek --ephemeral --timeout 10m run "Inspect this repository and summarize the current implementation. Do not modify files."
```

For a higher-throughput profile, use the model deployment configured in your project:

```powershell
doit --profile foundry_gpt --ephemeral --timeout 10m run "Review the project structure. Do not modify files."
```

The example Foundry profile is limited to `20,000` tokens per minute and `20 requests per minute`. The rate limiter uses the profile's `tokens_per_minute` and `requests_per_minute` values. On HTTP 429 responses it honors `Retry-After`, then uses bounded exponential backoff with jitter. An exhausted throttle returns a distinct rate-limit error.

If the endpoint rejects a request, inspect the provider response and verify:

- The API root ends at `/openai/v1`, without an extra `/responses` in configuration.
- The model value is the deployment/model identifier accepted by the endpoint.
- The API key environment variable is present in the same shell that launches `doit`.
- The model supports function calling when the task requires tools.

## Console Output

Human mode emits one replaceable `[doit] <current action>` status line while attached to an interactive terminal. Captured output and CI streams keep newline-delimited progress events for diagnostics:

```text
[doit] session: session started
[doit] context: bounded context prepared
[doit] model: requesting model response (round 1)
[doit] model: model response received (1 tool call(s))
[doit] tool: tool requested: fs.read
[doit] tool: tool succeeded: fs.read
[doit] session: task completed
Finished repository inspection.
session=... input_tokens=... output_tokens=... total_tokens=... source=provider exact=true
```

Progress messages do not include credentials, prompts, or raw tool arguments. JSON output (`--format json`) is intended for automation and writes the final result object without progress lines.

## Tools

The agent exposes structured tools rather than unrestricted shell access.

Filesystem inspection:

- `fs.list`
- `fs.stat`
- `fs.read`
- `fs.search`
- `fs.hash`
- `fs.write`
- `fs.move`
- `fs.mkdir`
- `fs.remove`

Use `fs.list` to inspect the workspace tree; pass a relative `path` such as `docs` and `recursive: true` to enumerate a subtree. Use `fs.search` with the same relative `path` and an optional glob such as `*.md` to search within that subtree. Use `fs.write` to create or explicitly overwrite bounded files, `fs.move` to move files or directories without replacement, `fs.mkdir` with `parents: true` to create nested directories, and `fs.remove` with `recursive: true` only when removing a directory tree is intended. All mutation tools are workspace-confined; the workspace root and `.git` metadata are protected. Use `code.apply_patch` for larger reviewable file changes. Agent mode asks for approval, while `doit run` can automate local workspace changes.

Git inspection and local operations:

- `git.root`
- `git.status`
- `git.diff`
- `git.log`
- `git.show`
- `git.blame`
- `git.check_ignore`
- `git.stage`
- `git.unstage`
- `git.commit`
- `git.restore`
- `git.branch`
- `git.worktree`

Local Git mutations are explicit and workspace-scoped. Use `git.stage` when you want a separate preview step, or use `git.commit` to stage and commit an explicit path group atomically after validating its diff. `git.restore` supports `worktree`, `staged`, and `head` modes and can discard local changes. `git.branch` reports branch/upstream divergence, and `git.worktree` lists local worktrees. Agent mode asks for approval; `doit run` can automate configured local operations. Remote operations and arbitrary Git command composition are not exposed.

Code and validation:

- `code.check_patch`
- `code.apply_patch`
- `code.replace_exact`
- `code.insert_at_anchor`
- `code.delete_exact`
- `code.rename`
- `code.format`
- `process.run`

For small, localized edits, prefer `code.replace_exact`, `code.insert_at_anchor`, and `code.delete_exact`. They require exactly one match, reject ambiguous anchors, support expected hashes and dry-run previews, and return change-set evidence. Use `code.apply_patch` for larger multi-file changes. `code.format` runs a formatter task configured by the workspace. Pass the configured task name and workspace-relative arguments; `doit` does not assume a language, formatter, or file extension.

`process.run` accepts a configured task name, not an executable or shell command. Define repository tasks in `.doit/config.json` or another selected configuration file:

```json
{
  "tasks": {
    "check": {"executable": "<test-runner>", "arguments": ["<project-arguments>"]},
    "format": {"executable": "<formatter>", "arguments": ["<project-arguments>"]}
  }
}
```

The model-facing schema advertises the tasks available in the current workspace. Providers that reject dotted tool names see this tool as `process_run`, which is mapped back to `process.run` before execution.

MCP is the planned extension boundary for external tools. Configured MCP servers will be mapped into the same normalized tool, policy, workspace, timeout, output, redaction, and change-set contracts as built-in tools. Cloud session synchronization and hosted telemetry are intentionally not part of `doit`; general plugins remain undecided.

The model may choose a process deadline with a human-readable `timeout`, such as `"5m"`. The default configured-process timeout is 2 minutes and the default maximum is 30 minutes; a caller-supplied global `--timeout` remains a hard upper bound for the entire request. Omit the global option when the model should choose per-process deadlines without a caller-imposed request deadline.

Read-only inspection is automatic within the workspace scope. In `doit agent`, writes, deletes, formatter execution, and process tasks show an approval prompt. Answer `y` or `yes` to allow one action. `doit run` is trusted workspace automation: configured operations inside the effective workspace, including destructive local changes, run without prompting. The resulting diff, change request, validation output, and session evidence are the human review surface; workspace boundaries, symlink checks, and configured capability allowlists remain active. JSON mode stays non-interactive and does not emit prompts.

Example approval flow:

```text
doit> Add a small README improvement
[doit] approval required: code.apply_patch (write)
[doit] arguments: {"patch":"..."}
[doit] allow this action? [y/N] y
```

## Sessions

Durable sessions are stored below the effective invocation path:

```text
<workspace>/.doit/sessions/<session-id>/
```

A session can contain:

- `manifest.json`
- `events.jsonl`
- `result.json`
- `continuation.json`, when provider continuation state is needed

Durable runs automatically reuse the newest completed or failed resumable session below the current workspace. The previous public conversation turns are included in the next model request, while the existing session history and lock remain protected. Use `--new-session` or `--no-resume` to force a new durable session. `--ephemeral` disables persistence and automatic reuse for that invocation.

Use `--ephemeral` for experiments or sensitive requests that should not persist a session. Session data is redacted and bounded before writing. The repository ignores `.doit/sessions/` through `.gitignore`.

## Exit Codes

| Code | Meaning |
| --- | --- |
| `0` | Task completed successfully. |
| `1` | Task, tool, or validation failure. |
| `2` | Usage, configuration, or credential setup error. |
| `3` | Backend, connectivity, model compatibility, or exhausted rate-limit error. |
| `4` | Cancellation or denied approval-required action. |

## Development

Configure repository-specific validation tasks under `.doit/config.json`, then run them through the structured `process.run` tool. The implementation sequence and acceptance criteria are tracked in [docs/implementation-plan.md](docs/implementation-plan.md).

## Security Notes

- Rotate any credential that has been pasted into chat, source files, or command history.
- Use environment variables or an operating-system credential store for API keys.
- Do not commit `.doit/sessions/` or raw model transcripts.
- Prefer `--ephemeral` while testing credentials or new backend configurations.
- Review proposed code changes before enabling write-capable workflows.
