// Package processrunner executes configured development tasks without shell composition.
package processrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/diakovliev/doit/internal/apperr"
	"github.com/diakovliev/doit/internal/process"
	"github.com/diakovliev/doit/internal/tools"
)

// Definition describes one executable allowlisted task.
type Definition struct {
	Name        string
	Executable  string
	Arguments   []string
	Environment []string
	Kind        string
}

// Runner executes definitions below one workspace root.
type Runner struct {
	workspace string
	tasks     map[string]Definition
	maxOutput int
}

// New creates an empty runner rooted at workspace.
func New(workspace string, maxOutputBytes int) (*Runner, error) {
	absoluteWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return nil, apperr.Wrap(apperr.KindConfig, "processrunner.workspace", err)
	}
	if maxOutputBytes <= 0 {
		maxOutputBytes = 64 * 1024
	}
	return &Runner{workspace: absoluteWorkspace, tasks: make(map[string]Definition), maxOutput: maxOutputBytes}, nil
}

// Register adds an executable task to the allowlist.
func (runner *Runner) Register(definition Definition) error {
	if definition.Name == "" || definition.Executable == "" {
		return apperr.New(apperr.KindConfig, "processrunner.register", "task name and executable are required")
	}
	if strings.ContainsAny(definition.Executable, "&|<>;$\n\r") {
		return apperr.New(apperr.KindConfig, "processrunner.register", "shell composition is not allowed")
	}
	resolvedExecutable, err := exec.LookPath(definition.Executable)
	if err != nil {
		return apperr.Wrap(apperr.KindConfig, "processrunner.register", err)
	}
	definition.Executable = resolvedExecutable
	if _, exists := runner.tasks[definition.Name]; exists {
		return apperr.New(apperr.KindConfig, "processrunner.register", "task is already registered: "+definition.Name)
	}
	runner.tasks[definition.Name] = definition
	return nil
}

// Run executes a registered task with bounded output and duration.
func (runner *Runner) Run(ctx context.Context, task process.Task) (process.Result, error) {
	definition, workingDirectory, timeout, err := runner.prepare(task)
	if err != nil {
		return process.Result{}, err
	}
	processContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	arguments := append([]string(nil), definition.Arguments...)
	arguments = append(arguments, task.Arguments...)
	command := &exec.Cmd{
		Path: definition.Executable,
		Args: append([]string{definition.Executable}, arguments...),
		Dir:  workingDirectory,
		Env:  append([]string(nil), definition.Environment...),
	}
	started := time.Now()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &limitedWriter{writer: &stdout, limit: runner.maxOutput}
	command.Stderr = &limitedWriter{writer: &stderr, limit: runner.maxOutput}
	runErr := command.Run()
	result := process.Result{Task: task.Name, Kind: definition.Kind, WorkingDirectory: workingDirectory, Duration: time.Since(started), Stdout: stdout.String(), Stderr: stderr.String(), Diagnostics: parseDiagnostics(stdout.String(), stderr.String()), Truncated: len(stdout.Bytes()) >= runner.maxOutput || len(stderr.Bytes()) >= runner.maxOutput}
	result, runErr = runner.finish(processContext, runErr, result)
	result.Passed = runErr == nil && result.ExitCode == 0 && !result.TimedOut
	return result, runErr
}

// RegisterTool exposes the allowlisted process runner as process.run.
func RegisterTool(registry *tools.Registry, runner *Runner) error {
	return registry.Register(processTool{runner: runner})
}

type processTool struct {
	runner *Runner
}

func (tool processTool) Definition() tools.Definition {
	taskNames := tool.runner.taskNames()
	return tools.Definition{Name: "process.run", Description: "Run one configured allowlisted task. Choose the task name and, when needed, a human-readable timeout such as 5m; the caller's outer deadline still applies.", Parameters: processParameters(taskNames), Risk: tools.RiskProcess, Timeout: 30 * time.Second, MaxOutputBytes: 64 * 1024, MaxArguments: 16}
}

func (tool processTool) Execute(ctx context.Context, call tools.Call) tools.Result {
	var task process.Task
	if err := json.Unmarshal(call.Arguments, &task); err != nil {
		return tools.Result{Status: tools.StatusFailed, Diagnostics: []tools.Diagnostic{{Level: "error", Message: err.Error()}}}
	}
	result, err := tool.runner.Run(ctx, task)
	if err != nil {
		return tools.Result{Status: tools.StatusFailed, Data: result, Diagnostics: []tools.Diagnostic{{Level: "error", Message: err.Error()}}}
	}
	return tools.Result{Status: tools.StatusSucceeded, Data: result}
}

func (runner *Runner) prepare(task process.Task) (Definition, string, time.Duration, error) {
	definition, exists := runner.tasks[task.Name]
	if !exists {
		return Definition{}, "", 0, apperr.New(apperr.KindPolicy, "processrunner.run", "task is not allowlisted: "+task.Name+" (available: "+strings.Join(runner.taskNames(), ", ")+")")
	}
	workingDirectory, err := runner.workingDirectory(task.WorkingDirectory)
	if err != nil {
		return Definition{}, "", 0, err
	}
	timeout := task.Timeout
	if timeout <= 0 {
		timeout = process.DefaultTaskTimeout
	}
	if timeout > process.MaximumTaskTimeout {
		return Definition{}, "", 0, apperr.New(apperr.KindPolicy, "processrunner.run", "task timeout exceeds maximum of "+process.MaximumTaskTimeout.String())
	}
	return definition, workingDirectory, timeout, nil
}

func (runner *Runner) taskNames() []string {
	names := make([]string, 0, len(runner.tasks))
	for name := range runner.tasks {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func processParameters(taskNames []string) json.RawMessage {
	availableTasks := strings.Join(taskNames, ", ")
	parameters := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task":              map[string]any{"type": "string", "enum": taskNames, "description": "Configured task name. Available tasks: " + availableTasks},
			"args":              map[string]any{"type": "array", "items": map[string]string{"type": "string"}},
			"working_directory": map[string]string{"type": "string"},
			"timeout":           map[string]string{"type": "string", "description": "Optional model-selected duration such as 30s or 5m; maximum 10m. The outer CLI deadline still applies."},
		},
		"required": []string{"task"},
	}
	encoded, err := json.Marshal(parameters)
	if err != nil {
		return json.RawMessage(`{"type":"object","required":["task"]}`)
	}
	return encoded
}

func (runner *Runner) workingDirectory(requested string) (string, error) {
	if requested == "" {
		return runner.workspace, nil
	}
	candidate, err := filepath.Abs(filepath.Join(runner.workspace, requested))
	if err != nil || !withinRoot(runner.workspace, candidate) {
		return "", apperr.New(apperr.KindPolicy, "processrunner.run", "working directory escapes the workspace")
	}
	return runner.validateWorkingDirectory(candidate)
}

func (runner *Runner) validateWorkingDirectory(candidate string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(runner.workspace)
	if err != nil {
		return "", apperr.Wrap(apperr.KindTool, "processrunner.run", err)
	}
	info, statErr := os.Stat(candidate)
	if statErr == nil {
		resolvedCandidate, resolveErr := filepath.EvalSymlinks(candidate)
		if resolveErr != nil || !info.IsDir() || !withinRoot(realRoot, resolvedCandidate) {
			return "", symlinkEscapeError()
		}
		return candidate, nil
	}
	if !errors.Is(statErr, os.ErrNotExist) {
		return "", apperr.Wrap(apperr.KindTool, "processrunner.run", statErr)
	}
	resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(candidate))
	if err != nil {
		return "", apperr.Wrap(apperr.KindPolicy, "processrunner.run", err)
	}
	if !withinRoot(realRoot, resolvedParent) {
		return "", symlinkEscapeError()
	}
	return "", apperr.Wrap(apperr.KindTool, "processrunner.run", statErr)
}

func symlinkEscapeError() error {
	return apperr.New(apperr.KindPolicy, "processrunner.run", "working directory escapes the workspace through a symlink")
}

func (runner *Runner) finish(processContext context.Context, runErr error, result process.Result) (process.Result, error) {
	if errors.Is(processContext.Err(), context.DeadlineExceeded) {
		result.TimedOut = true
		return result, apperr.Wrap(apperr.KindTool, "processrunner.run", processContext.Err())
	}
	if errors.Is(processContext.Err(), context.Canceled) {
		return result, processContext.Err()
	}
	if runErr == nil {
		result.ExitCode = 0
		return result, nil
	}
	var exitError *exec.ExitError
	if errors.As(runErr, &exitError) {
		result.ExitCode = exitError.ExitCode()
		return result, nil
	}
	return result, apperr.Wrap(apperr.KindTool, "processrunner.run", runErr)
}

type limitedWriter struct {
	writer  io.Writer
	limit   int
	written int
}

func (writer *limitedWriter) Write(data []byte) (int, error) {
	originalLength := len(data)
	remaining := writer.limit - writer.written
	if remaining <= 0 {
		return originalLength, nil
	}
	if len(data) > remaining {
		data = data[:remaining]
	}
	count, err := writer.writer.Write(data)
	writer.written += count
	return originalLength, err
}

func withinRoot(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

var diagnosticPattern = regexp.MustCompile(`^(.*?):([0-9]+)(?::([0-9]+))?:\s*(?:(error|warning|fatal):\s*)?(.*)$`)

func parseDiagnostics(outputs ...string) []process.Diagnostic {
	diagnostics := make([]process.Diagnostic, 0)
	for _, output := range outputs {
		for _, line := range strings.Split(output, "\n") {
			match := diagnosticPattern.FindStringSubmatch(strings.TrimSpace(line))
			if len(match) == 0 || strings.TrimSpace(match[5]) == "" {
				continue
			}
			lineNumber := parseDiagnosticNumber(match[2])
			column := parseDiagnosticNumber(match[3])
			severity := match[4]
			if severity == "" {
				severity = "error"
			}
			diagnostics = append(diagnostics, process.Diagnostic{Path: match[1], Line: lineNumber, Column: column, Severity: severity, Message: strings.TrimSpace(match[5])})
		}
	}
	return diagnostics
}

func parseDiagnosticNumber(value string) int {
	if value == "" {
		return 0
	}
	var number int
	for _, character := range value {
		number = number*10 + int(character-'0')
	}
	return number
}
