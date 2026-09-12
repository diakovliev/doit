// Package processrunner executes configured development tasks without shell composition.
package processrunner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/diakovliev/doit/internal/apperr"
	"github.com/diakovliev/doit/internal/process"
)

// Definition describes one executable allowlisted task.
type Definition struct {
	Name        string
	Executable  string
	Arguments   []string
	Environment []string
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
	result := process.Result{Duration: time.Since(started), Stdout: stdout.String(), Stderr: stderr.String(), Truncated: len(stdout.Bytes()) >= runner.maxOutput || len(stderr.Bytes()) >= runner.maxOutput}
	return runner.finish(processContext, runErr, result)
}

func (runner *Runner) prepare(task process.Task) (Definition, string, time.Duration, error) {
	definition, exists := runner.tasks[task.Name]
	if !exists {
		return Definition{}, "", 0, apperr.New(apperr.KindPolicy, "processrunner.run", "task is not allowlisted: "+task.Name)
	}
	workingDirectory, err := runner.workingDirectory(task.WorkingDirectory)
	if err != nil {
		return Definition{}, "", 0, err
	}
	timeout := task.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return definition, workingDirectory, timeout, nil
}

func (runner *Runner) workingDirectory(requested string) (string, error) {
	if requested == "" {
		return runner.workspace, nil
	}
	candidate, err := filepath.Abs(filepath.Join(runner.workspace, requested))
	if err != nil || !withinRoot(runner.workspace, candidate) {
		return "", apperr.New(apperr.KindPolicy, "processrunner.run", "working directory escapes the workspace")
	}
	return candidate, nil
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
