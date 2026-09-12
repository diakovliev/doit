// Package process defines controlled development-task execution contracts.
package process

import (
	"context"
	"time"
)

// Task identifies a configured process task and its bounded arguments.
type Task struct {
	Name             string
	Arguments        []string
	WorkingDirectory string
	Timeout          time.Duration
}

// Result is the normalized outcome of a process task.
type Result struct {
	ExitCode  int
	Stdout    string
	Stderr    string
	Duration  time.Duration
	TimedOut  bool
	Truncated bool
}

// Runner executes only configured tasks.
type Runner interface {
	Run(context.Context, Task) (Result, error)
}
