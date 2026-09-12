// Package process defines controlled development-task execution contracts.
package process

import (
	"context"
	"time"
)

// Task identifies a configured process task and its bounded arguments.
type Task struct {
	Name             string        `json:"task"`
	Arguments        []string      `json:"args,omitempty"`
	WorkingDirectory string        `json:"working_directory,omitempty"`
	Timeout          time.Duration `json:"timeout,omitempty"`
}

// Result is the normalized outcome of a process task.
type Result struct {
	ExitCode  int           `json:"exit_code"`
	Stdout    string        `json:"stdout"`
	Stderr    string        `json:"stderr"`
	Duration  time.Duration `json:"duration"`
	TimedOut  bool          `json:"timed_out"`
	Truncated bool          `json:"truncated"`
}

// Runner executes only configured tasks.
type Runner interface {
	Run(context.Context, Task) (Result, error)
}
