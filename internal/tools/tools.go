// Package tools defines structured capabilities exposed to the model.
package tools

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
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
	UsesNetwork    bool            `json:"uses_network,omitempty"`
	Timeout        time.Duration   `json:"timeout"`
	MaxOutputBytes int             `json:"max_output_bytes"`
	MaxArguments   int             `json:"max_arguments"`
}

// Call contains validated tool arguments.
type Call struct {
	Name      string
	Arguments json.RawMessage
}

// ChangeSet identifies a bounded workspace mutation and its content hashes.
type ChangeSet struct {
	ID           string            `json:"id"`
	Operation    string            `json:"operation"`
	State        string            `json:"state"`
	Approval     string            `json:"approval,omitempty"`
	Paths        []string          `json:"paths"`
	BeforeHashes map[string]string `json:"before_hashes"`
	AfterHashes  map[string]string `json:"after_hashes"`
}

// Result is the normalized outcome returned by every tool.
type Result struct {
	Status       Status        `json:"status"`
	Data         any           `json:"data,omitempty"`
	Diagnostics  []Diagnostic  `json:"diagnostics,omitempty"`
	ChangedPaths []string      `json:"changed_paths,omitempty"`
	Truncated    bool          `json:"truncated"`
	Duration     time.Duration `json:"duration,omitempty"`
	ChangeSet    *ChangeSet    `json:"change_set,omitempty"`
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
	registry.tools[definition.Name] = contractTool{tool: tool, definition: definition}
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

// Select returns a model-visible registry for one capability profile.
// The full profile preserves the complete registered toolset.
func (registry *Registry) Select(profile string) (*Registry, error) {
	if profile == "" {
		profile = "full"
	}
	if !validProfile(profile) {
		return nil, apperr.New(apperr.KindConfig, "tools.profile", "unknown tool profile: "+profile)
	}
	selected := NewRegistry()
	for name, tool := range registry.tools {
		if profileAllows(profile, tool.Definition()) {
			selected.tools[name] = tool
		}
	}
	return selected, nil
}

func validProfile(profile string) bool {
	switch profile {
	case "full", "inspect", "edit", "validate", "git-read", "git-write", "destructive":
		return true
	default:
		return false
	}
}

func profileAllows(profile string, definition Definition) bool {
	if profile == "full" {
		return true
	}
	if profile == "destructive" {
		return definition.Risk == RiskDestructive
	}
	if profile == "git-read" {
		return strings.HasPrefix(definition.Name, "git.") && definition.Risk == RiskReadOnly
	}
	if profile == "git-write" {
		return strings.HasPrefix(definition.Name, "git.")
	}
	if profile == "inspect" {
		return definition.Risk == RiskReadOnly
	}
	if profile == "edit" {
		return definition.Risk == RiskReadOnly || definition.Risk == RiskWrite
	}
	return definition.Risk == RiskReadOnly || definition.Risk == RiskProcess
}

type contractTool struct {
	tool       Tool
	definition Definition
}

func (tool contractTool) Definition() Definition {
	return tool.definition
}

func (tool contractTool) Execute(ctx context.Context, call Call) Result {
	if err := validateCall(tool.definition, call); err != nil {
		return Result{Status: StatusFailed, Diagnostics: []Diagnostic{{Level: "error", Message: err.Error()}}}
	}
	executionContext := ctx
	if tool.definition.Timeout > 0 {
		var cancel context.CancelFunc
		executionContext, cancel = context.WithTimeout(ctx, tool.definition.Timeout)
		defer cancel()
	}
	started := time.Now()
	result := tool.tool.Execute(executionContext, call)
	result.Duration = time.Since(started)
	if result.Duration <= 0 {
		result.Duration = time.Nanosecond
	}
	if executionContext.Err() != nil && result.Status == StatusSucceeded {
		return Result{Status: StatusCancelled, Duration: result.Duration, Diagnostics: []Diagnostic{{Level: "warning", Message: executionContext.Err().Error()}}}
	}
	if tool.definition.MaxOutputBytes > 0 {
		encoded, err := json.Marshal(result.Data)
		if err != nil {
			return Result{Status: StatusFailed, Diagnostics: []Diagnostic{{Level: "error", Message: "tool output is not serializable"}}}
		}
		if len(encoded) > tool.definition.MaxOutputBytes {
			return Result{Status: StatusFailed, Truncated: true, Duration: result.Duration, Diagnostics: []Diagnostic{{Level: "error", Message: fmt.Sprintf("tool output exceeds %d bytes", tool.definition.MaxOutputBytes)}}}
		}
	}
	return result
}

func validateCall(definition Definition, call Call) error {
	var arguments map[string]json.RawMessage
	if len(call.Arguments) == 0 {
		arguments = make(map[string]json.RawMessage)
	} else if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
		return fmt.Errorf("tool arguments must be a JSON object: %w", err)
	}
	if arguments == nil {
		return errors.New("tool arguments must be a JSON object")
	}
	if definition.MaxArguments > 0 && len(arguments) > definition.MaxArguments {
		return fmt.Errorf("tool accepts at most %d arguments", definition.MaxArguments)
	}
	var schema struct {
		Required []string `json:"required"`
	}
	if len(definition.Parameters) > 0 && json.Unmarshal(definition.Parameters, &schema) == nil {
		for _, required := range schema.Required {
			if _, exists := arguments[required]; !exists {
				return fmt.Errorf("required tool argument is missing: %s", required)
			}
		}
	}
	return nil
}
