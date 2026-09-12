// Package app composes the CLI's runtime dependencies.
package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
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
	"github.com/diakovliev/doit/internal/modelhttp"
	"github.com/diakovliev/doit/internal/policy"
	"github.com/diakovliev/doit/internal/processrunner"
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
	outcome, err := dependencies.runner.Run(ctx, agent.Task{Command: invocation.Command, Request: request, Workspace: configuration.Workspace, Profile: configuration.Profile, Model: dependencies.profile.Model, MaxInputTokens: configuration.Token.MaxInputTokens, MaxOutputTokens: configuration.Token.MaxOutputTokens, NonInteractive: invocation.Command == "run", WorkspaceAutomation: invocation.Command == "run", NewSession: invocation.NewSession})
	if err != nil {
		return err
	}
	return writeOutcome(stdout, configuration.Format, outcome)
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
	if err := registerDevelopmentTasks(processService); err != nil {
		return runtimeDependencies{}, err
	}
	registry, err := buildRegistry(configuration.Workspace, filesystem, processService)
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
	runner := agent.Runner{Client: modelClient, Context: contextdata.New(filesystem, usage.ByteEstimator{}), Tools: registry, Policy: policy.DefaultPolicy{}, Sessions: sessionStore, Progress: progress}
	return runtimeDependencies{runner: runner, profile: profile, close: func() { _ = sessionStore.Close() }}, nil
}

func buildRegistry(workspace string, filesystem *workspacefs.Service, processService *processrunner.Runner) (*tools.Registry, error) {
	registry := tools.NewRegistry()
	if err := workspacefs.RegisterTools(registry, filesystem); err != nil {
		return nil, err
	}
	gitService, err := gitinspect.New(workspace)
	if err != nil {
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
	return registry, nil
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

func registerDevelopmentTasks(runner *processrunner.Runner) error {
	tasks := []processrunner.Definition{
		{Name: "test", Executable: "go", Arguments: []string{"test", "./..."}},
		{Name: "vet", Executable: "go", Arguments: []string{"vet", "./..."}},
		{Name: "format", Executable: "gofmt", Arguments: []string{"-w"}},
		{Name: "format-check", Executable: "gofmt", Arguments: []string{"-l"}},
		{Name: "lint", Executable: "golangci-lint", Arguments: []string{"run"}},
		{Name: "security", Executable: "gosec", Arguments: []string{"./..."}},
	}
	for _, task := range tasks {
		if _, err := exec.LookPath(task.Executable); err != nil {
			continue
		}
		if err := runner.Register(task); err != nil {
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
