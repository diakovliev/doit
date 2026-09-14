// Package app composes the CLI's runtime dependencies.
package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	configuration, err := config.Load(config.LoadOptions{Workspace: invocation.Directory, Overrides: config.Overrides{Profile: invocation.Profile, Model: invocation.Model, Format: invocation.Format, Ephemeral: ephemeralOverride(invocation)}})
	if err != nil {
		return err
	}
	progress := func(event agent.ProgressEvent) {
		if configuration.Format == "json" {
			return
		}
		_, _ = fmt.Fprintf(stdout, "[doit] %s: %s\n", event.Phase, event.Message)
	}
	dependencies, err := buildRuntime(configuration, progress)
	if err != nil {
		return err
	}
	defer dependencies.close()
	if invocation.Command == "agent" && configuration.Format != "json" {
		dependencies.runner.Approve = func(approvalContext context.Context, action policy.Action, call tools.Call) (bool, error) {
			return promptApproval(approvalContext, input, stdout, action, call)
		}
	}
	outcome, err := dependencies.runner.Run(ctx, agent.Task{Command: invocation.Command, Request: request, Workspace: configuration.Workspace, Profile: configuration.Profile, Model: dependencies.profile.Model, MaxInputTokens: configuration.Token.MaxInputTokens, MaxOutputTokens: configuration.Token.MaxOutputTokens, NonInteractive: invocation.Command != "agent", WorkspaceAutomation: invocation.Command == "run" || invocation.Command == "develop", NewSession: invocation.NewSession})
	if err != nil {
		return err
	}
	return writeOutcome(stdout, configuration.Format, outcome)
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
	runner, err := processrunner.New(configuration.Workspace, 64*1024)
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
	if len(invocation.Arguments) < 2 {
		return apperr.New(apperr.KindUsage, "app.session", command+" requires a session id")
	}
	record, err := store.Load(ctx, session.ID(invocation.Arguments[1]))
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(record)
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
	configuration, err := config.Load(config.LoadOptions{Workspace: invocation.Directory, Overrides: config.Overrides{Profile: invocation.Profile, Model: invocation.Model, Format: invocation.Format, Ephemeral: ephemeralOverride(invocation)}})
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
	outcome, err := dependencies.runner.Run(ctx, agent.Task{Command: "run", Request: request, Workspace: configuration.Workspace, Profile: configuration.Profile, Model: dependencies.profile.Model, MaxInputTokens: configuration.Token.MaxInputTokens, MaxOutputTokens: configuration.Token.MaxOutputTokens, NonInteractive: true, WorkspaceAutomation: true, SessionID: invocation.Arguments[0]})
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
	result, err := projectinit.Initialize(ctx, invocation.Directory)
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

func buildRuntime(configuration config.Config, progress agent.ProgressFunc) (runtimeDependencies, error) {
	profile, err := configuration.SelectedProfile()
	if err != nil {
		return runtimeDependencies{}, err
	}
	filesystem, err := workspacefs.New(configuration.Workspace)
	if err != nil {
		return runtimeDependencies{}, err
	}
	processService, err := processrunner.New(configuration.Workspace, 64*1024)
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
	registry, err := buildRegistryWithGit(configuration.Workspace, filesystem, processService, gitService, configuration.ToolProfile)
	if err != nil {
		return runtimeDependencies{}, err
	}
	modelClient, err := modelhttp.New(profile, modelhttp.Options{TokenCounter: usage.ByteEstimator{}, OnRetry: func(attempt int, delay time.Duration) {
		if progress != nil {
			progress(agent.ProgressEvent{Phase: "rate-limit", Message: fmt.Sprintf("throttled; retry %d in %s", attempt, delay.Round(time.Millisecond))})
		}
	}})
	if err != nil {
		return runtimeDependencies{}, err
	}
	sessionStore, err := session.NewFileStore(session.Options{InvocationPath: configuration.Workspace, Ephemeral: configuration.Ephemeral})
	if err != nil {
		return runtimeDependencies{}, err
	}
	contextBuilder := contextdata.New(filesystem, usage.ByteEstimator{}).WithGitStatus(gitService.StatusSummary)
	runner := agent.Runner{Client: modelClient, Context: contextBuilder, Tools: registry, Policy: policy.DefaultPolicy{}, Sessions: sessionStore, Progress: progress}
	return runtimeDependencies{runner: runner, profile: profile, close: func() { _ = sessionStore.Close() }}, nil
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
