// Package app composes the CLI's runtime dependencies.
package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/diakovliev/doit/internal/agent"
	"github.com/diakovliev/doit/internal/apperr"
	"github.com/diakovliev/doit/internal/cli"
	"github.com/diakovliev/doit/internal/codetools"
	"github.com/diakovliev/doit/internal/config"
	contextdata "github.com/diakovliev/doit/internal/contextbuilder"
	"github.com/diakovliev/doit/internal/gitinspect"
	"github.com/diakovliev/doit/internal/mcpclient"
	"github.com/diakovliev/doit/internal/model"
	"github.com/diakovliev/doit/internal/modelhttp"
	"github.com/diakovliev/doit/internal/policy"
	"github.com/diakovliev/doit/internal/process"
	"github.com/diakovliev/doit/internal/processrunner"
	"github.com/diakovliev/doit/internal/projectinit"
	"github.com/diakovliev/doit/internal/session"
	"github.com/diakovliev/doit/internal/tools"
	"github.com/diakovliev/doit/internal/usage"
	"github.com/diakovliev/doit/internal/workspacefs"
)

// Handler composes the production CLI runtime.
type Handler struct{}

// Run executes a one-shot request.
func (Handler) Run(ctx context.Context, invocation cli.Invocation, stdin io.Reader, stdout io.Writer) error {
	return Handler{}.execute(ctx, invocation, stdin, stdout)
}

// Agent executes one request in agent mode. Interactive continuation is added
// after the one-shot orchestration path is stable.
func (Handler) Agent(ctx context.Context, invocation cli.Invocation, stdin io.Reader, stdout io.Writer) error {
	return Handler{}.execute(ctx, invocation, stdin, stdout)
}

func (Handler) execute(ctx context.Context, invocation cli.Invocation, stdin io.Reader, stdout io.Writer) error {
	if invocation.Command == "init" {
		return initializeProject(ctx, invocation, stdout)
	}
	if handled, localErr := runLocalCommand(ctx, invocation, stdin, stdout); handled {
		return localErr
	}
	return Handler{}.runModelCommand(ctx, invocation, stdin, stdout)
}

func (Handler) runModelCommand(ctx context.Context, invocation cli.Invocation, stdin io.Reader, stdout io.Writer) error {
	input := bufio.NewReader(stdin)
	request, err := requestText(invocation, input, stdout)
	if err != nil {
		return err
	}
	configuration, err := config.Load(config.LoadOptions{Workspace: invocation.Directory, Overrides: config.Overrides{Profile: invocation.Profile, Model: invocation.Model, ThinkingEffort: invocation.ThinkingEffort, Format: invocation.Format, Verbose: invocation.Verbose, Ephemeral: ephemeralOverride(invocation)}})
	if err != nil {
		return err
	}
	progressLine := newProgressLine(stdout, configuration.Format != "json")
	defer progressLine.Close()
	progress := progressLine.Update
	dependencies, err := buildRuntime(configuration, progress)
	if err != nil {
		return err
	}
	defer dependencies.close()
	budget := configuration.EffectiveTokenBudget(dependencies.profile)
	contextPolicy := configuration.EffectiveContextPolicy(dependencies.profile)
	if invocation.Command == "agent" && configuration.Format != "json" {
		dependencies.runner.Approve = func(approvalContext context.Context, action policy.Action, call tools.Call) (bool, error) {
			progressLine.Clear()
			return promptApproval(approvalContext, input, stdout, action, call)
		}
	}
	outcome, err := dependencies.runner.Run(ctx, agent.Task{Command: invocation.Command, Request: request, Workspace: configuration.Workspace, Profile: configuration.Profile, Model: dependencies.profile.Model, ThinkingEffort: dependencies.profile.ThinkingEffort, ContextPolicy: contextPolicyForAgent(contextPolicy), MaxInputTokens: budget.MaxInputTokens, MaxOutputTokens: budget.MaxOutputTokens, MaxSessionTokens: budget.MaxSessionTokens, NonInteractive: invocation.Command != "agent", WorkspaceAutomation: invocation.Command == "run" || invocation.Command == "develop", NewSession: invocation.NewSession})
	if err != nil {
		return err
	}
	progressLine.Clear()
	return writeOutcome(stdout, configuration.Format, outcome)
}

type progressLine struct {
	writer  io.Writer
	enabled bool
	replace bool
	width   int
}

func newProgressLine(writer io.Writer, enabled bool) *progressLine {
	return &progressLine{writer: writer, enabled: enabled, replace: enabled && isTerminalWriter(writer)}
}

func (line *progressLine) Update(event agent.ProgressEvent) {
	if !line.enabled {
		return
	}
	if !line.replace {
		_, _ = fmt.Fprintf(line.writer, "[doit] %s: %s\n", event.Phase, event.Message)
		return
	}
	line.clearTerminalText()
	text := fmt.Sprintf("[doit] %s: %s", event.Phase, event.Message)
	_, _ = fmt.Fprint(line.writer, text)
	line.width = len(text)
}

func (line *progressLine) Clear() {
	if line.replace {
		line.clearTerminalText()
	}
}

func (line *progressLine) Close() {
	line.Clear()
}

func (line *progressLine) clearTerminalText() {
	if line.width == 0 {
		return
	}
	_, _ = fmt.Fprintf(line.writer, "\r%s\r", strings.Repeat(" ", line.width))
	line.width = 0
}

func isTerminalWriter(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func runLocalCommand(ctx context.Context, invocation cli.Invocation, stdin io.Reader, stdout io.Writer) (bool, error) {
	switch invocation.Command {
	case "test":
		return true, runConfiguredTest(ctx, invocation, stdout)
	case "status":
		return true, writeStatus(ctx, invocation, stdout)
	case "model":
		return true, testModel(ctx, invocation, stdout)
	case "config":
		return true, listConfig(invocation, stdout)
	case "session":
		return true, handleSessionCommand(ctx, invocation, stdin, stdout)
	case "doctor":
		return true, runDoctor(ctx, invocation, stdout)
	default:
		return false, nil
	}
}

func runConfiguredTest(ctx context.Context, invocation cli.Invocation, stdout io.Writer) error {
	configuration, err := config.Load(config.LoadOptions{Workspace: invocation.Directory})
	if err != nil {
		return err
	}
	runner, err := processrunner.NewWithLimits(configuration.Workspace, 64*1024, executionDuration(configuration.Execution.ProcessDefaultTimeoutMs), executionDuration(configuration.Execution.ProcessMaxTimeoutMs))
	if err != nil {
		return err
	}
	if err := registerConfiguredTasks(runner, configuration.Tasks); err != nil {
		return err
	}
	taskName, arguments := configuredTaskArgs(invocation.Arguments)
	result, err := runner.Run(ctx, process.Task{Name: taskName, Arguments: arguments, WorkingDirectory: "."})
	if err := writeProcessResult(invocation.Format, stdout, result); err != nil {
		return err
	}
	if err != nil {
		return err
	}
	return requirePassedTask(result)
}

func configuredTaskArgs(arguments []string) (string, []string) {
	if len(arguments) == 0 {
		return "test", nil
	}
	return arguments[0], arguments[1:]
}

func writeProcessResult(format string, stdout io.Writer, result process.Result) error {
	if format == "json" {
		return json.NewEncoder(stdout).Encode(result)
	}
	_, _ = fmt.Fprintf(stdout, "task=%s passed=%t exit_code=%d duration=%s\n", result.Task, result.Passed, result.ExitCode, result.Duration.Round(time.Millisecond))
	if result.Stdout != "" {
		_, _ = fmt.Fprintln(stdout, result.Stdout)
	}
	if result.Stderr != "" {
		_, _ = fmt.Fprintln(stdout, result.Stderr)
	}
	return nil
}

func requirePassedTask(result process.Result) error {
	if result.Passed {
		return nil
	}
	return apperr.New(apperr.KindTool, "app.test", "configured validation task failed: "+result.Task)
}

type statusReport struct {
	Workspace   string `json:"workspace"`
	Profile     string `json:"profile"`
	ToolProfile string `json:"tool_profile"`
	Repository  string `json:"repository,omitempty"`
	GitStatus   string `json:"git_status,omitempty"`
	GitError    string `json:"git_error,omitempty"`
}

func writeStatus(ctx context.Context, invocation cli.Invocation, stdout io.Writer) error {
	configuration, err := config.Load(config.LoadOptions{Workspace: invocation.Directory})
	if err != nil {
		return err
	}
	report := statusReport{Workspace: configuration.Workspace, Profile: configuration.Profile, ToolProfile: configuration.ToolProfile}
	gitService, gitErr := gitinspect.New(configuration.Workspace)
	if gitErr == nil {
		root, rootErr := gitService.Root(ctx, gitinspect.RootRequest{})
		if rootErr == nil {
			report.Repository = root.Repository
			report.GitStatus, rootErr = gitService.StatusSummary(ctx)
		}
		if rootErr != nil {
			report.GitError = rootErr.Error()
		}
	} else {
		report.GitError = gitErr.Error()
	}
	if invocation.Format == "json" {
		return json.NewEncoder(stdout).Encode(report)
	}
	_, err = fmt.Fprintf(stdout, "workspace=%s profile=%s tool_profile=%s\n", report.Workspace, report.Profile, report.ToolProfile)
	if report.Repository != "" {
		_, _ = fmt.Fprintln(stdout, "repository="+report.Repository)
	}
	if report.GitStatus != "" {
		_, _ = fmt.Fprintln(stdout, "git_status="+report.GitStatus)
	}
	if report.GitError != "" {
		_, _ = fmt.Fprintln(stdout, "git_error="+report.GitError)
	}
	return err
}

type modelTestReport struct {
	Model             string       `json:"model"`
	Status            string       `json:"status"`
	ResponseID        string       `json:"response_id,omitempty"`
	RequestID         string       `json:"request_id,omitempty"`
	ProviderRequestID string       `json:"provider_request_id,omitempty"`
	Text              string       `json:"text,omitempty"`
	Usage             usage.Counts `json:"usage"`
}

func testModel(ctx context.Context, invocation cli.Invocation, stdout io.Writer) error {
	if len(invocation.Arguments) > 0 && invocation.Arguments[0] != "test" {
		return apperr.New(apperr.KindUsage, "app.model", "supported model command is: model test")
	}
	configuration, err := config.Load(config.LoadOptions{Workspace: invocation.Directory, Overrides: config.Overrides{Profile: invocation.Profile, Model: invocation.Model}})
	if err != nil {
		return err
	}
	profile, err := configuration.SelectedProfile()
	if err != nil {
		return err
	}
	client, err := modelhttp.New(profile, modelhttp.Options{TokenCounter: usage.ByteEstimator{}})
	if err != nil {
		return err
	}
	response, err := client.Create(ctx, model.Request{Model: profile.Model, Input: []model.InputItem{{Type: "message", Role: "user", Content: "Reply with exactly OK"}}, MaxOutputTokens: 32})
	if err != nil {
		return err
	}
	report := modelTestReport{Model: profile.Model, Status: response.Status, ResponseID: response.ID, RequestID: response.RequestID, ProviderRequestID: response.ProviderRequestID, Text: response.Text, Usage: response.Usage}
	if invocation.Format == "json" {
		return json.NewEncoder(stdout).Encode(report)
	}
	_, err = fmt.Fprintf(stdout, "model=%s status=%s response=%s request_id=%s provider_request_id=%s\n%s\n", report.Model, report.Status, report.ResponseID, report.RequestID, report.ProviderRequestID, report.Text)
	return err
}

func listConfig(invocation cli.Invocation, stdout io.Writer) error {
	if len(invocation.Arguments) > 0 && invocation.Arguments[0] != "list" {
		return apperr.New(apperr.KindUsage, "app.config", "supported config command is: config list")
	}
	configuration, err := config.Load(config.LoadOptions{Workspace: invocation.Directory, Overrides: config.Overrides{Profile: invocation.Profile, Model: invocation.Model}})
	if err != nil {
		return err
	}
	if invocation.Format == "json" {
		return json.NewEncoder(stdout).Encode(configuration)
	}
	_, err = fmt.Fprintf(stdout, "workspace=%s profile=%s tool_profile=%s format=%s\n", configuration.Workspace, configuration.Profile, configuration.ToolProfile, configuration.Format)
	if len(configuration.Profiles) > 0 {
		_, _ = fmt.Fprintln(stdout, "profiles="+strings.Join(sortedKeys(configuration.Profiles), ","))
	}
	_, _ = fmt.Fprintf(stdout, "tasks=%d\n", len(configuration.Tasks))
	return err
}

func handleSessionCommand(ctx context.Context, invocation cli.Invocation, stdin io.Reader, stdout io.Writer) error {
	command := "list"
	if len(invocation.Arguments) > 0 {
		command = invocation.Arguments[0]
	}
	if command == "resume" {
		return resumeSession(ctx, invocation, stdin, stdout)
	}
	configuration, err := config.Load(config.LoadOptions{Workspace: invocation.Directory})
	if err != nil {
		return err
	}
	store, err := session.NewFileStore(session.Options{InvocationPath: configuration.Workspace, Ephemeral: false})
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	switch command {
	case "list":
		return listSessions(ctx, invocation, stdout, store)
	case "inspect", "export":
		return inspectSession(ctx, invocation, stdout, store, command)
	case "prune":
		return pruneSessions(ctx, invocation, stdout, store)
	default:
		return apperr.New(apperr.KindUsage, "app.session", "supported session commands are: list, inspect, export, resume, prune")
	}
}

func listSessions(ctx context.Context, invocation cli.Invocation, stdout io.Writer, store *session.FileStore) error {
	metadata, err := store.List(ctx)
	if err != nil {
		return err
	}
	if invocation.Format == "json" {
		return json.NewEncoder(stdout).Encode(metadata)
	}
	for _, item := range metadata {
		_, _ = fmt.Fprintf(stdout, "%s %s %s %s\n", item.ID, item.Status, item.Command, item.UpdatedAt.Format(time.RFC3339))
	}
	return nil
}

func inspectSession(ctx context.Context, invocation cli.Invocation, stdout io.Writer, store *session.FileStore, command string) error {
	sessionID, err := inspectionSessionID(ctx, invocation, store, command)
	if err != nil {
		return err
	}
	record, err := store.Load(ctx, sessionID)
	if err != nil {
		return err
	}
	if invocation.Format == "json" || command == "export" {
		return json.NewEncoder(stdout).Encode(record)
	}
	if invocation.Format == "markdown" || invocation.Format == "md" {
		return writeMarkdownSession(stdout, record)
	}
	return writeHumanSession(stdout, record)
}

func inspectionSessionID(ctx context.Context, invocation cli.Invocation, store *session.FileStore, command string) (session.ID, error) {
	if len(invocation.Arguments) >= 2 {
		return session.ID(invocation.Arguments[1]), nil
	}
	if command != "inspect" {
		return "", apperr.New(apperr.KindUsage, "app.session", command+" requires a session id")
	}
	latestID, found, err := store.Latest(ctx)
	if err != nil {
		return "", err
	}
	if !found {
		return "", apperr.New(apperr.KindUsage, "app.session", "no completed session is available to inspect")
	}
	return latestID, nil
}

func writeMarkdownSession(writer io.Writer, record session.Record) error {
	metadata := record.Metadata
	if _, err := fmt.Fprintf(writer, "# Session `%s`\n\n", markdownEscape(string(metadata.ID))); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "| Field | Value |\n| --- | --- |\n| Status | `%s` |\n| Command | `%s` |\n| Profile | `%s` |\n| Model | `%s` |\n| Workspace | `%s` |\n| Created | `%s` |\n| Updated | `%s` |\n| Resumable | `%t` |\n\n", markdownEscape(string(metadata.Status)), markdownEscape(metadata.Command), markdownEscape(metadata.Profile), markdownEscape(metadata.Model), markdownEscape(metadata.InvocationPath), metadata.CreatedAt.Format(time.RFC3339), metadata.UpdatedAt.Format(time.RFC3339), metadata.Resumable); err != nil {
		return err
	}
	if err := writeMarkdownEvents(writer, record.Events); err != nil {
		return err
	}
	if err := writeMarkdownEventText(writer, record.Events); err != nil {
		return err
	}
	return writeMarkdownResult(writer, record.Result)
}

func writeMarkdownEvents(writer io.Writer, events []session.Event) error {
	if _, err := fmt.Fprintf(writer, "## Events (%d)\n\n| # | Time | Type | Status | Tool calls | Text |\n| ---: | --- | --- | --- | ---: | --- |\n", len(events)); err != nil {
		return err
	}
	for _, event := range events {
		status, toolCalls, text := markdownEventFields(event)
		if _, err := fmt.Fprintf(writer, "| %d | %s | `%s` | %s | %s | %s |\n", event.Sequence, event.Timestamp.Format("15:04:05"), markdownEscape(event.Type), markdownTableEscape(status), markdownTableEscape(toolCalls), markdownTableEscape(text)); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(writer)
	return err
}

func markdownEventFields(event session.Event) (status, toolCalls, text string) {
	switch event.Type {
	case "request":
		var request model.Request
		if json.Unmarshal(event.Data, &request) == nil {
			for index := len(request.Input) - 1; index >= 0; index-- {
				if request.Input[index].Type == "message" && request.Input[index].Role == "user" {
					return "-", "-", publicSessionPreview(request.Input[index].Content)
				}
			}
		}
	case "model_message":
		var response model.Response
		if json.Unmarshal(event.Data, &response) == nil {
			return response.Status, strconv.Itoa(len(response.ToolCalls)), publicSessionPreview(response.Text)
		}
	case "tool_result":
		var result tools.Result
		if json.Unmarshal(event.Data, &result) == nil {
			return string(result.Status), "-", "-"
		}
	}
	return "-", "-", "-"
}

func writeMarkdownEventText(writer io.Writer, events []session.Event) error {
	wroteHeading := false
	for _, event := range events {
		text := fullMarkdownEventText(event)
		if text == "" {
			continue
		}
		if !wroteHeading {
			if _, err := fmt.Fprint(writer, "## Full Text\n\n"); err != nil {
				return err
			}
			wroteHeading = true
		}
		if _, err := fmt.Fprintf(writer, "<details>\n<summary>Event #%d (%s)</summary>\n\n<pre>%s</pre>\n\n</details>\n\n", event.Sequence, markdownEscape(event.Type), html.EscapeString(text)); err != nil {
			return err
		}
	}
	return nil
}

func fullMarkdownEventText(event session.Event) string {
	switch event.Type {
	case "request":
		var request model.Request
		if json.Unmarshal(event.Data, &request) == nil {
			for index := len(request.Input) - 1; index >= 0; index-- {
				if request.Input[index].Type == "message" && request.Input[index].Role == "user" {
					return request.Input[index].Content
				}
			}
		}
	case "model_message":
		var response model.Response
		if json.Unmarshal(event.Data, &response) == nil {
			return response.Text
		}
	}
	return ""
}

func writeMarkdownResult(writer io.Writer, result *session.Result) error {
	if _, err := fmt.Fprint(writer, "## Result\n\n"); err != nil {
		return err
	}
	if result == nil {
		_, err := fmt.Fprintln(writer, "No result recorded.")
		return err
	}
	if _, err := fmt.Fprintf(writer, "### Summary\n\n%s\n\n### Usage\n\n`%s`\n\n", strings.TrimSpace(result.Summary), markdownEscape(formatUsage(result.Usage))); err != nil {
		return err
	}
	if err := writeMarkdownChangedPaths(writer, result.ChangedPaths); err != nil {
		return err
	}
	return writeMarkdownValidations(writer, result.Validations)
}

func writeMarkdownChangedPaths(writer io.Writer, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	if _, err := fmt.Fprint(writer, "### Changed Paths\n\n"); err != nil {
		return err
	}
	for _, path := range paths {
		if _, err := fmt.Fprintf(writer, "- `%s`\n", markdownEscape(path)); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(writer)
	return err
}

func writeMarkdownValidations(writer io.Writer, validations []session.Validation) error {
	if len(validations) == 0 {
		return nil
	}
	if _, err := fmt.Fprint(writer, "### Validations\n\n| Validation | Passed | Exit code | Duration |\n| --- | ---: | ---: | --- |\n"); err != nil {
		return err
	}
	for _, validation := range validations {
		if _, err := fmt.Fprintf(writer, "| `%s` | %t | %d | %s |\n", markdownEscape(validation.Task), validation.Passed, validation.ExitCode, validation.Duration.Round(time.Millisecond)); err != nil {
			return err
		}
	}
	return nil
}

func markdownEscape(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

func markdownTableEscape(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "`", "\\`")
	value = strings.ReplaceAll(value, "*", "\\*")
	return value
}

func writeHumanSession(writer io.Writer, record session.Record) error {
	metadata := record.Metadata
	if err := writeSessionHeader(writer, metadata, len(record.Events)); err != nil {
		return err
	}
	if err := writeSessionEvents(writer, record.Events); err != nil {
		return err
	}
	return writeSessionResult(writer, record.Result)
}

func writeSessionHeader(writer io.Writer, metadata session.Metadata, eventCount int) error {
	_, err := fmt.Fprintf(writer, "Session %s\nstatus=%s command=%s profile=%s model=%s\nworkspace=%s\ncreated=%s updated=%s resumable=%t\nevents=%d\n", metadata.ID, metadata.Status, metadata.Command, metadata.Profile, metadata.Model, metadata.InvocationPath, metadata.CreatedAt.Format(time.RFC3339), metadata.UpdatedAt.Format(time.RFC3339), metadata.Resumable, eventCount)
	return err
}

func writeSessionEvents(writer io.Writer, events []session.Event) error {
	for _, event := range events {
		if _, err := fmt.Fprintf(writer, "  #%d %s %s", event.Sequence, event.Timestamp.Format(time.RFC3339), event.Type); err != nil {
			return err
		}
		if summary := humanEventSummary(event); summary != "" {
			if _, err := fmt.Fprint(writer, ": "+summary); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(writer); err != nil {
			return err
		}
	}
	return nil
}

func writeSessionResult(writer io.Writer, result *session.Result) error {
	if result == nil {
		_, err := fmt.Fprintln(writer, "result=none")
		return err
	}
	if _, err := fmt.Fprintf(writer, "result=%s\nusage=%s\n", result.Summary, formatUsage(result.Usage)); err != nil {
		return err
	}
	if len(result.ChangedPaths) > 0 {
		if _, err := fmt.Fprintln(writer, "changed_paths="+strings.Join(result.ChangedPaths, ",")); err != nil {
			return err
		}
	}
	for _, validation := range result.Validations {
		if _, err := fmt.Fprintf(writer, "validation=%s passed=%t exit_code=%d duration=%s\n", validation.Task, validation.Passed, validation.ExitCode, validation.Duration.Round(time.Millisecond)); err != nil {
			return err
		}
	}
	return nil
}

func humanEventSummary(event session.Event) string {
	switch event.Type {
	case "request":
		var request model.Request
		if json.Unmarshal(event.Data, &request) == nil {
			for index := len(request.Input) - 1; index >= 0; index-- {
				if request.Input[index].Type == "message" && request.Input[index].Role == "user" {
					return "prompt=" + publicSessionPreview(request.Input[index].Content)
				}
			}
		}
	case "model_message":
		var response model.Response
		if json.Unmarshal(event.Data, &response) == nil {
			return fmt.Sprintf("status=%s tool_calls=%d text=%s", response.Status, len(response.ToolCalls), publicSessionPreview(response.Text))
		}
	case "tool_result":
		var result tools.Result
		if json.Unmarshal(event.Data, &result) == nil {
			return fmt.Sprintf("status=%s", result.Status)
		}
	}
	return ""
}

func publicSessionPreview(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "-"
	}
	if len(value) > 160 {
		return value[:160] + "..."
	}
	return value
}

func formatUsage(counts usage.Counts) string {
	return fmt.Sprintf("input=%d output=%d total=%d", usageCount(counts.InputTokens), usageCount(counts.OutputTokens), usageCount(counts.TotalTokens))
}

func usageCount(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func pruneSessions(ctx context.Context, invocation cli.Invocation, stdout io.Writer, store *session.FileStore) error {
	keep := 10
	if len(invocation.Arguments) > 1 {
		parsed, err := strconv.Atoi(invocation.Arguments[1])
		if err != nil || parsed < 0 {
			return apperr.New(apperr.KindUsage, "app.session", "prune keep must be a non-negative integer")
		}
		keep = parsed
	}
	removed, err := store.Prune(ctx, keep)
	if err != nil {
		return err
	}
	if invocation.Format == "json" {
		return json.NewEncoder(stdout).Encode(map[string]any{"removed": removed, "kept": keep})
	}
	_, err = fmt.Fprintf(stdout, "removed=%d kept=%d\n", removed, keep)
	return err
}

func resumeSession(ctx context.Context, invocation cli.Invocation, stdin io.Reader, stdout io.Writer) error {
	if len(invocation.Arguments) < 2 {
		return apperr.New(apperr.KindUsage, "app.session", "resume requires a session id and request")
	}
	requestInvocation := invocation
	requestInvocation.Command = "run"
	requestInvocation.Request = strings.Join(invocation.Arguments[1:], " ")
	request, err := requestText(requestInvocation, stdin, stdout)
	if err != nil {
		return err
	}
	configuration, err := config.Load(config.LoadOptions{Workspace: invocation.Directory, Overrides: config.Overrides{Profile: invocation.Profile, Model: invocation.Model, Format: invocation.Format, Verbose: invocation.Verbose, Ephemeral: ephemeralOverride(invocation)}})
	if err != nil {
		return err
	}
	progress := func(event agent.ProgressEvent) {
		if configuration.Format != "json" {
			_, _ = fmt.Fprintf(stdout, "[doit] %s: %s\n", event.Phase, event.Message)
		}
	}
	dependencies, err := buildRuntime(configuration, progress)
	if err != nil {
		return err
	}
	defer dependencies.close()
	budget := configuration.EffectiveTokenBudget(dependencies.profile)
	contextPolicy := configuration.EffectiveContextPolicy(dependencies.profile)
	outcome, err := dependencies.runner.Run(ctx, agent.Task{Command: "run", Request: request, Workspace: configuration.Workspace, Profile: configuration.Profile, Model: dependencies.profile.Model, ThinkingEffort: dependencies.profile.ThinkingEffort, ContextPolicy: contextPolicyForAgent(contextPolicy), MaxInputTokens: budget.MaxInputTokens, MaxOutputTokens: budget.MaxOutputTokens, MaxSessionTokens: budget.MaxSessionTokens, NonInteractive: true, WorkspaceAutomation: true, SessionID: invocation.Arguments[0]})
	if err != nil {
		return err
	}
	return writeOutcome(stdout, configuration.Format, outcome)
}

func runDoctor(_ context.Context, invocation cli.Invocation, stdout io.Writer) error {
	configuration, err := config.Load(config.LoadOptions{Workspace: invocation.Directory})
	if err != nil {
		return err
	}
	report := map[string]any{"workspace": configuration.Workspace, "tasks": len(configuration.Tasks), "tool_profile": configuration.ToolProfile}
	if _, workspaceErr := workspacefs.New(configuration.Workspace); workspaceErr != nil {
		report["workspace_error"] = workspaceErr.Error()
	} else {
		report["workspace_ok"] = true
	}
	if _, profileErr := configuration.SelectedProfile(); profileErr != nil {
		report["profile_error"] = profileErr.Error()
	} else {
		report["profile_ok"] = true
	}
	if invocation.Format == "json" {
		return json.NewEncoder(stdout).Encode(report)
	}
	return json.NewEncoder(stdout).Encode(report)
}

func sortedKeys(values map[string]config.BackendProfile) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func initializeProject(ctx context.Context, invocation cli.Invocation, stdout io.Writer) error {
	var result projectinit.Result
	var err error
	if invocation.GlobalInit {
		result, err = projectinit.InitializeGlobal(ctx, "")
	} else {
		result, err = projectinit.Initialize(ctx, invocation.Directory)
	}
	if err != nil {
		return err
	}
	if invocation.Format == "json" {
		return json.NewEncoder(stdout).Encode(result)
	}
	if _, err := fmt.Fprintf(stdout, "Initialized doit in %s\n", result.Workspace); err != nil {
		return err
	}
	for _, path := range result.Created {
		if _, err := fmt.Fprintln(stdout, "created "+path); err != nil {
			return err
		}
	}
	for _, path := range result.Existing {
		if _, err := fmt.Fprintln(stdout, "already exists "+path); err != nil {
			return err
		}
	}
	return nil
}

type runtimeDependencies struct {
	runner  agent.Runner
	profile config.BackendProfile
	close   func()
}

type runtimeTools struct {
	registry *tools.Registry
	close    func()
}

func buildRuntime(configuration config.Config, progress agent.ProgressFunc) (runtimeDependencies, error) {
	profile, err := configuration.SelectedProfile()
	if err != nil {
		return runtimeDependencies{}, err
	}
	filesystem, err := workspacefs.New(configuration.Workspace)
	if err != nil {
		return runtimeDependencies{}, err
	}
	processService, err := processrunner.NewWithLimits(configuration.Workspace, 64*1024, executionDuration(configuration.Execution.ProcessDefaultTimeoutMs), executionDuration(configuration.Execution.ProcessMaxTimeoutMs))
	if err != nil {
		return runtimeDependencies{}, err
	}
	if err := registerConfiguredTasks(processService, configuration.Tasks); err != nil {
		return runtimeDependencies{}, err
	}
	gitService, err := gitinspect.New(configuration.Workspace)
	if err != nil {
		return runtimeDependencies{}, err
	}
	sessionStore, err := session.NewFileStore(session.Options{InvocationPath: configuration.Workspace, Ephemeral: configuration.Ephemeral})
	if err != nil {
		return runtimeDependencies{}, err
	}
	toolset, err := buildRuntimeTools(configuration, profile, filesystem, processService, gitService, sessionStore)
	if err != nil {
		_ = sessionStore.Close()
		return runtimeDependencies{}, err
	}
	modelClient, err := modelhttp.New(profile, modelhttp.Options{Timeout: executionDuration(configuration.Execution.RequestTimeoutMs), TokenCounter: usage.ByteEstimator{}, OnRetry: func(attempt int, delay time.Duration) {
		if progress != nil {
			progress(agent.ProgressEvent{Phase: "rate-limit", Message: fmt.Sprintf("throttled; retry %d in %s", attempt, delay.Round(time.Millisecond))})
		}
	}})
	if err != nil {
		toolset.close()
		_ = sessionStore.Close()
		return runtimeDependencies{}, err
	}
	contextBuilder := contextdata.New(filesystem, usage.ByteEstimator{}).WithGitStatus(gitService.StatusSummary)
	runner := agent.Runner{Client: modelClient, Context: contextBuilder, Tools: toolset.registry, Policy: policy.DefaultPolicy{}, Sessions: sessionStore, Progress: progress, Streaming: profile.Streaming, Verbose: configuration.Verbose, MaxRounds: configuration.Execution.MaxRounds}
	return runtimeDependencies{runner: runner, profile: profile, close: func() { _ = sessionStore.Close(); toolset.close() }}, nil
}

func executionDuration(milliseconds int) time.Duration {
	if milliseconds <= 0 {
		return 0
	}
	return time.Duration(milliseconds) * time.Millisecond
}

func contextPolicyForAgent(contextPolicy config.EffectiveContextPolicy) *contextdata.Policy {
	return &contextdata.Policy{IncludeGuidance: contextPolicy.IncludeGuidance, IncludeGitStatus: contextPolicy.IncludeGitStatus, IncludeWorkspaceListing: contextPolicy.IncludeWorkspaceListing}
}

func buildRuntimeTools(configuration config.Config, profile config.BackendProfile, filesystem *workspacefs.Service, processService *processrunner.Runner, gitService *gitinspect.Service, sessionStore session.Store) (runtimeTools, error) {
	registry, err := buildRegistryWithGit(configuration.Workspace, filesystem, processService, gitService, "full")
	if err != nil {
		return runtimeTools{}, err
	}
	mcpRuntime, err := mcpclient.RegisterTools(context.Background(), registry, configuration.Workspace, configuration.MCPServers)
	if err != nil {
		return runtimeTools{}, err
	}
	if err := registry.Register(session.NewHistoryTool(sessionStore)); err != nil {
		_ = mcpRuntime.Close()
		return runtimeTools{}, err
	}
	registry, err = registry.Select(configuration.EffectiveToolProfile(profile))
	if err != nil {
		_ = mcpRuntime.Close()
		return runtimeTools{}, err
	}
	return runtimeTools{registry: registry, close: func() { _ = mcpRuntime.Close() }}, nil
}

func buildRegistry(workspace string, filesystem *workspacefs.Service, processService *processrunner.Runner) (*tools.Registry, error) {
	gitService, err := gitinspect.New(workspace)
	if err != nil {
		return nil, err
	}
	return buildRegistryWithGit(workspace, filesystem, processService, gitService, "full")
}

func buildRegistryWithGit(workspace string, filesystem *workspacefs.Service, processService *processrunner.Runner, gitService *gitinspect.Service, toolProfile string) (*tools.Registry, error) {
	registry := tools.NewRegistry()
	if err := workspacefs.RegisterTools(registry, filesystem); err != nil {
		return nil, err
	}
	if err := gitinspect.RegisterTools(registry, gitService); err != nil {
		return nil, err
	}
	if err := processrunner.RegisterTool(registry, processService); err != nil {
		return nil, err
	}
	codeService, err := codetools.New(workspace, processService)
	if err != nil {
		return nil, err
	}
	if err := codetools.RegisterTools(registry, codeService); err != nil {
		return nil, err
	}
	return registry.Select(toolProfile)
}

func requestText(invocation cli.Invocation, stdin io.Reader, stdout io.Writer) (string, error) {
	if strings.TrimSpace(invocation.Request) != "" {
		return strings.TrimSpace(invocation.Request), nil
	}
	if invocation.Command == "agent" {
		_, _ = fmt.Fprint(stdout, "doit> ")
		reader, ok := stdin.(*bufio.Reader)
		if !ok {
			reader = bufio.NewReader(stdin)
		}
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", apperr.Wrap(apperr.KindUsage, "app.input", err)
		}
		if strings.TrimSpace(line) != "" {
			return strings.TrimSpace(line), nil
		}
	}
	contents, err := io.ReadAll(stdin)
	if err != nil {
		return "", apperr.Wrap(apperr.KindUsage, "app.input", err)
	}
	request := strings.TrimSpace(string(contents))
	if request == "" {
		return "", apperr.New(apperr.KindUsage, "app.input", "run requires a request argument or stdin input")
	}
	return request, nil
}

func promptApproval(ctx context.Context, reader *bufio.Reader, writer io.Writer, action policy.Action, call tools.Call) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	_, _ = fmt.Fprintf(writer, "[doit] approval required: %s (%s)\n", action.Name, action.Risk)
	arguments := strings.TrimSpace(string(call.Arguments))
	if len(arguments) > 2000 {
		arguments = arguments[:2000] + "..."
	}
	if arguments != "" {
		_, _ = fmt.Fprintf(writer, "[doit] arguments: %s\n", arguments)
	}
	_, _ = fmt.Fprint(writer, "[doit] allow this action? [y/N] ")
	answer, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

func ephemeralOverride(invocation cli.Invocation) *bool {
	if !invocation.Ephemeral {
		return nil
	}
	value := true
	return &value
}

func registerConfiguredTasks(runner *processrunner.Runner, tasks map[string]config.TaskConfig) error {
	for name, task := range tasks {
		if err := runner.Register(processrunner.Definition{Name: name, Executable: task.Executable, Arguments: task.Arguments, Environment: task.Environment, Kind: task.Kind}); err != nil {
			return err
		}
	}
	return nil
}

func writeOutcome(writer io.Writer, format string, outcome agent.Outcome) error {
	if format == "json" {
		return json.NewEncoder(writer).Encode(outcome)
	}
	if _, err := fmt.Fprintln(writer, outcome.Text); err != nil {
		return err
	}
	_, err := fmt.Fprintf(writer, "session=%s input_tokens=%s output_tokens=%s total_tokens=%s source=%s exact=%t\n", outcome.SessionID, usageValue(outcome.Usage.InputTokens), usageValue(outcome.Usage.OutputTokens), usageValue(outcome.Usage.TotalTokens), outcome.Usage.Source, outcome.Usage.Exact)
	return err
}

func usageValue(value *int64) string {
	if value == nil {
		return "unknown"
	}
	return strconv.FormatInt(*value, 10)
}

var _ cli.Handler = Handler{}
