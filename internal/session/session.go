// Package session defines durable and ephemeral session contracts.
package session

import (
	"context"
	"encoding/json"
	"time"

	"github.com/diakovliev/doit/internal/usage"
)

// ID identifies one related task or interactive interaction.
type ID string

// Status is the lifecycle state of a session.
type Status string

const (
	StatusActive    Status = "active"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Metadata identifies a session and its execution context.
type Metadata struct {
	ID             ID        `json:"id"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	InvocationPath string    `json:"invocation_path"`
	Command        string    `json:"command"`
	Profile        string    `json:"profile"`
	Model          string    `json:"model"`
	Status         Status    `json:"status"`
	ExitCode       int       `json:"exit_code"`
	Resumable      bool      `json:"resumable"`
}

// Event is an ordered, redacted session record.
type Event struct {
	Sequence  uint64          `json:"sequence"`
	Timestamp time.Time       `json:"timestamp"`
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// Validation records one configured validation task.
type Validation struct {
	Task        string        `json:"task"`
	ExitCode    int           `json:"exit_code"`
	Duration    time.Duration `json:"duration"`
	Diagnostics string        `json:"diagnostics,omitempty"`
}

// Result is the final redacted session summary.
type Result struct {
	Summary      string       `json:"summary"`
	ChangedPaths []string     `json:"changed_paths,omitempty"`
	Validations  []Validation `json:"validations,omitempty"`
	Unresolved   []string     `json:"unresolved,omitempty"`
	Usage        usage.Counts `json:"usage"`
}

// Record contains the metadata, events, and optional completion result.
type Record struct {
	Metadata     Metadata        `json:"metadata"`
	Events       []Event         `json:"events"`
	Result       *Result         `json:"result,omitempty"`
	Continuation json.RawMessage `json:"continuation,omitempty"`
}

// Store persists or serves session records.
type Store interface {
	Start(context.Context, Metadata) (ID, error)
	Append(context.Context, ID, Event) error
	Complete(context.Context, ID, Result) error
	WriteContinuation(context.Context, ID, json.RawMessage) error
	Load(context.Context, ID) (Record, error)
}
