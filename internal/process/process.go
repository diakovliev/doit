// Package process defines controlled development-task execution contracts.
package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// DefaultTaskTimeout applies when the model omits a process deadline.
	DefaultTaskTimeout = 30 * time.Second
	// MaximumTaskTimeout bounds a model-selected process deadline.
	MaximumTaskTimeout = 10 * time.Minute
)

// Task identifies a configured process task and its bounded arguments.
type Task struct {
	Name             string        `json:"task"`
	Arguments        []string      `json:"args,omitempty"`
	WorkingDirectory string        `json:"working_directory,omitempty"`
	Timeout          time.Duration `json:"timeout,omitempty"`
}

// UnmarshalJSON accepts human-readable durations and legacy nanosecond values.
func (task *Task) UnmarshalJSON(data []byte) error {
	type wireTask struct {
		Name             string          `json:"task"`
		Arguments        []string        `json:"args,omitempty"`
		WorkingDirectory string          `json:"working_directory,omitempty"`
		Timeout          json.RawMessage `json:"timeout,omitempty"`
	}
	var wire wireTask
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	timeout, err := decodeTimeout(wire.Timeout)
	if err != nil {
		return err
	}
	*task = Task{Name: wire.Name, Arguments: wire.Arguments, WorkingDirectory: wire.WorkingDirectory, Timeout: timeout}
	return nil
}

func decodeTimeout(data json.RawMessage) (time.Duration, error) {
	value := strings.TrimSpace(string(data))
	if value == "" || value == "null" {
		return 0, nil
	}
	if strings.HasPrefix(value, `"`) {
		var duration string
		if err := json.Unmarshal(data, &duration); err != nil {
			return 0, err
		}
		parsed, err := time.ParseDuration(duration)
		if err != nil {
			return 0, fmt.Errorf("invalid process timeout %q: %w", duration, err)
		}
		if parsed < 0 {
			return 0, errors.New("process timeout must not be negative")
		}
		return parsed, nil
	}
	var nanoseconds int64
	if err := json.Unmarshal(data, &nanoseconds); err != nil {
		return 0, fmt.Errorf("process timeout must be a duration string or nanoseconds: %w", err)
	}
	if nanoseconds < 0 {
		return 0, errors.New("process timeout must not be negative")
	}
	return time.Duration(nanoseconds), nil
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
