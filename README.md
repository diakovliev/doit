# doit

`doit` is a Go command-line workspace assistant for AI-assisted development, code review, and testing. It combines a model backend with bounded repository context and controlled local tools.

The project is designed for OpenAI Responses-compatible backends, including Microsoft Foundry deployments, Ollama-compatible local servers, gateways, and other hosted services.

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

The specialized `develop`, `review`, and `test` workflows are reserved in the CLI but are not yet separate workflow implementations. Streaming Responses support is deferred. Write and process tools require approval, and the current CLI composition uses conservative non-interactive behavior.

See the project documents for the full direction and implementation status:

- [Purpose](docs/purpose.md)
- [Definitions](docs/definition.md)
- [Design](docs/design.md)
- [Implementation plan](docs/implementation-plan.md)

## Requirements

- Go `1.27.1` or a compatible newer Go toolchain
- An OpenAI Responses-compatible model endpoint
- A configured model identifier
- A credential supplied through an environment variable when the backend requires authentication

The root package is intentionally thin. The application behavior lives under `internal/` packages.

## Quick Start

Show the CLI help and version:

```powershell
go run . --help
go run . --version
go run . version
```

Global options must appear before the command:

```powershell
go run . --profile ollama --timeout 5m run "Explain this repository"
```

Run a request without a positional prompt by piping text through stdin:

```powershell
"Explain the current implementation and list the main packages." | go run . --profile ollama run
```

Use an ephemeral session when you do not want `.doit/sessions/` data written:

```powershell
go run . --profile ollama --ephemeral --timeout 5m run "Inspect the repository without modifying files."
```

Use JSON output for scripts:

```powershell
go run . --profile ollama --format json --ephemeral run "Summarize the repository."
```

For a built binary, replace `go run .` with `doit`:

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
| `doit` | Start the default agent request flow. |
| `doit agent` | Run an agent request, reading a prompt from stdin when no prompt argument is supplied. |
| `doit run <request>` | Run one development-oriented request and exit. |
| `doit --help` | Print CLI usage. Put `--help` before the command. |
| `doit --version` | Print the CLI version. |
| `doit version` | Print the CLI version. |

The following names are recognized for future workflow implementations: `develop`, `review`, `test`, `status`, `model`, `config`, `session`, and `doctor`. Use `run` for the current general-purpose workflow.

## Global Options

| Option | Meaning | Example |
| --- | --- | --- |
| `-C, --directory <path>` | Use another workspace directory. | `doit -C .\sample --profile ollama run "Inspect this project"` |
| `-p, --profile <name>` | Select a named backend profile. | `doit --profile foundry_deepseek run "Review the code"` |
| `-m, --model <id>` | Override the profile's model identifier. | `doit --profile ollama --model phi4-mini:latest run "Explain main.go"` |
| `--format human` | Print progress and a human result. | `doit --format human run "Summarize"` |
| `--format json` | Print one machine-readable result object. | `doit --format json run "Summarize"` |
| `--ephemeral` | Keep the session in memory and do not persist it. | `doit --ephemeral run "Inspect only"` |
| `--new-session` / `--no-resume` | Start a fresh durable session instead of reusing the latest one. | `doit --new-session run "Start a separate review"` |
| `--timeout <duration>` | Set the request context deadline. | `doit --timeout 10m run "Review the repository"` |
| `--no-color` | Disable terminal styling. | `doit --no-color run "Summarize"` |
| `--quiet` | Reserved output-control flag. | `doit --quiet run "Summarize"` |
| `--verbose` | Reserved diagnostic-output flag. | `doit --verbose run "Summarize"` |

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

Set the credential only in the current shell. Never commit it, put it in a README, or pass it as a CLI argument:

```powershell
$env:DOIT_FOUNDRY_API_KEY = "<rotated-key>"
go run . --profile foundry_deepseek --ephemeral --timeout 10m run "Inspect this repository. Do not modify files."
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
go run . --profile ollama --ephemeral --timeout 10m run "Inspect main.go and summarize it. Do not modify files."
```

A model may answer normal text requests while still failing tool workflows. Coding tasks that require filesystem or process tools need a model/backend that emits Responses `function_call` items rather than prose that merely describes a hypothetical tool call.

## Microsoft Foundry

The adapter uses the OpenAI-compatible Foundry endpoint directly:

```powershell
$env:DOIT_FOUNDRY_API_KEY = "<rotated-key>"
go run . --profile foundry_deepseek --ephemeral --timeout 10m run "Inspect this repository and summarize the current implementation. Do not modify files."
```

For a higher-throughput profile, use the model deployment configured in your project:

```powershell
go run . --profile foundry_gpt --ephemeral --timeout 10m run "Review the Go package structure. Do not modify files."
```

The example Foundry profile is limited to `20,000` tokens per minute and `20 requests per minute`. The rate limiter uses the profile's `tokens_per_minute` and `requests_per_minute` values. On HTTP 429 responses it honors `Retry-After`, then uses bounded exponential backoff with jitter. An exhausted throttle returns a distinct rate-limit error.

If the endpoint rejects a request, inspect the provider response and verify:

- The API root ends at `/openai/v1`, without an extra `/responses` in configuration.
- The model value is the deployment/model identifier accepted by the endpoint.
- The API key environment variable is present in the same shell that launches `doit`.
- The model supports function calling when the task requires tools.

## Console Output

Human mode emits safe progress events before the final result:

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

Progress messages do not include credentials, prompts, or raw tool arguments. `--format json` is intended for automation and writes the final result object without human progress lines.

## Tools

The agent exposes structured tools rather than unrestricted shell access.

Filesystem inspection:

- `fs.list`
- `fs.stat`
- `fs.read`
- `fs.search`
- `fs.hash`

Git inspection:

- `git.root`
- `git.status`
- `git.diff`
- `git.log`
- `git.show`
- `git.blame`
- `git.check_ignore`

Code and validation:

- `code.check_patch`
- `code.apply_patch`
- `code.rename`
- `code.format`
- `process.run`

The built-in `code.format` task is named `format` and runs `gofmt -w`. Pass workspace-relative Go files in `arguments`, such as `["main.go"]`. Use `format-check` with the same file arguments to list files that need formatting without changing them.

`process.run` accepts a configured task name, not an executable or shell command. The model-facing schema advertises the tasks available in the current environment, typically `test`, `vet`, `format`, `format-check`, `lint`, and `security`; pass extra arguments through `args`, for example `{"task":"test","args":[]}`. Providers that reject dotted tool names see this tool as `process_run`, which is mapped back to `process.run` before execution.

Read-only inspection is automatic within the workspace scope. In `doit agent`, writes, deletes, formatter execution, and process tasks show an approval prompt. Answer `y` or `yes` to allow one action. `doit run` is the automation path: it allows local workspace changes and configured process tasks without prompting, while tool-level path confinement, network rejection, and command allowlists remain active. JSON mode stays non-interactive and does not emit prompts.

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

Format and validate the repository with:

```powershell
gofmt -w main.go internal
go test ./...
go vet ./...
golangci-lint run
gosec ./...
```

The implementation sequence and acceptance criteria are tracked in [docs/implementation-plan.md](docs/implementation-plan.md).

## Security Notes

- Rotate any credential that has been pasted into chat, source files, or command history.
- Use environment variables or an operating-system credential store for API keys.
- Do not commit `.doit/sessions/` or raw model transcripts.
- Prefer `--ephemeral` while testing credentials or new backend configurations.
- Review proposed code changes before enabling write-capable workflows.
