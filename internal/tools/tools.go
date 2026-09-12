// Package tools defines structured capabilities exposed to the model.
package tools

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/diakovliev/doit/internal/apperr"
)

// Risk classifies the side effects of a tool.
type Risk string

const (
	RiskReadOnly    Risk = "read-only"
	RiskProcess     Risk = "process"
	RiskWrite       Risk = "write"
	RiskDestructive Risk = "destructive"
	RiskRejected    Risk = "rejected"
)

// Status is the outcome of one tool invocation.
type Status string

const (
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusDenied    Status = "denied"
	StatusCancelled Status = "cancelled"
)

// Diagnostic is bounded, user-visible tool information.
type Diagnostic struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

// Definition describes a registered tool and its policy metadata.
type Definition struct {
	Name           string          `json:"name"`
	Description    string          `json:"description,omitempty"`
	Parameters     json.RawMessage `json:"parameters,omitempty"`
	Risk           Risk            `json:"risk"`
	Timeout        time.Duration   `json:"timeout"`
	MaxOutputBytes int             `json:"max_output_bytes"`
	MaxArguments   int             `json:"max_arguments"`
}

// Call contains validated tool arguments.
type Call struct {
	Name      string
	Arguments json.RawMessage
}

// Result is the normalized outcome returned by every tool.
type Result struct {
	Status       Status       `json:"status"`
	Data         any          `json:"data,omitempty"`
	Diagnostics  []Diagnostic `json:"diagnostics,omitempty"`
	ChangedPaths []string     `json:"changed_paths,omitempty"`
	Truncated    bool         `json:"truncated"`
}

// Tool is a cancellation-aware structured capability.
type Tool interface {
	Definition() Definition
	Execute(context.Context, Call) Result
}

// Registry stores tools by their stable names.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds a tool and rejects duplicate or incomplete definitions.
func (registry *Registry) Register(tool Tool) error {
	if tool == nil {
		return apperr.New(apperr.KindTool, "tools.register", "tool is required")
	}
	definition := tool.Definition()
	if definition.Name == "" || definition.Risk == "" {
		return apperr.New(apperr.KindTool, "tools.register", "tool name and risk are required")
	}
	if _, exists := registry.tools[definition.Name]; exists {
		return apperr.New(apperr.KindTool, "tools.register", "tool is already registered: "+definition.Name)
	}
	registry.tools[definition.Name] = tool
	return nil
}

// Lookup finds a registered tool by name.
func (registry *Registry) Lookup(name string) (Tool, bool) {
	tool, exists := registry.tools[name]
	return tool, exists
}

// Definitions returns stable, name-sorted tool definitions.
func (registry *Registry) Definitions() []Definition {
	definitions := make([]Definition, 0, len(registry.tools))
	for _, tool := range registry.tools {
		definitions = append(definitions, tool.Definition())
	}
	slices.SortFunc(definitions, func(first, second Definition) int {
		return cmp.Compare(first.Name, second.Name)
	})
	return definitions
}
