// Package contextbuilder builds bounded, token-aware model context.
package contextbuilder

import (
	stdcontext "context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/diakovliev/doit/internal/model"
	"github.com/diakovliev/doit/internal/usage"
	"github.com/diakovliev/doit/internal/workspacefs"
)

const defaultInputTokenBudget = 16000

// Builder selects local repository context for model requests.
type Builder struct {
	filesystem *workspacefs.Service
	counter    usage.TokenCounter
}

// New creates a context builder.
func New(filesystem *workspacefs.Service, counter usage.TokenCounter) *Builder {
	if counter == nil {
		counter = usage.ByteEstimator{}
	}
	return &Builder{filesystem: filesystem, counter: counter}
}

// Request describes context to include in a model request.
type Request struct {
	Model           string
	UserInput       string
	Instructions    string
	Paths           []string
	Tools           []model.ToolDefinition
	MaxInputTokens  int
	MaxOutputTokens int
}

// Build returns a token-bounded normalized model request and its input usage.
func (builder *Builder) Build(ctx stdcontext.Context, request Request) (model.Request, usage.Counts, error) {
	if builder == nil || builder.filesystem == nil {
		return model.Request{}, usage.Counts{Source: usage.SourceUnknown}, errors.New("context builder filesystem is required")
	}
	if request.UserInput == "" {
		return model.Request{}, usage.Counts{Source: usage.SourceUnknown}, errors.New("context request is empty")
	}
	input := []model.InputItem{{Type: "message", Role: "user", Content: request.UserInput}}
	instructions := request.Instructions + "\n" + builder.projectInstructions(ctx)
	for _, path := range request.Paths {
		readResponse, err := builder.filesystem.Read(ctx, workspacefs.ReadRequest{Path: path, MaxBytes: 16 * 1024})
		if err != nil {
			continue
		}
		input = append(input, model.InputItem{Type: "message", Role: "user", Content: "File: " + readResponse.Path + "\n" + readResponse.Content})
	}
	if len(request.Paths) == 0 {
		if listing, err := builder.filesystem.List(ctx, workspacefs.ListRequest{MaxEntries: 100}); err == nil {
			input = append(input, model.InputItem{Type: "message", Role: "user", Content: "Workspace entries:\n" + formatEntries(listing)})
		}
	}
	normalized := model.Request{Model: request.Model, Instructions: strings.TrimSpace(instructions), Input: input, Tools: request.Tools, ToolChoice: toolChoice(request.Tools), MaxOutputTokens: request.MaxOutputTokens}
	budget := request.MaxInputTokens
	if budget <= 0 {
		budget = defaultInputTokenBudget
	}
	counts, err := builder.fitBudget(ctx, &normalized, budget)
	if err != nil {
		return model.Request{}, usage.Counts{Source: usage.SourceUnknown}, err
	}
	return normalized, counts, nil
}

func (builder *Builder) fitBudget(ctx stdcontext.Context, request *model.Request, budget int) (usage.Counts, error) {
	for len(request.Input) > 1 {
		counts, err := builder.countRequest(ctx, *request)
		if err != nil {
			return usage.Counts{Source: usage.SourceUnknown}, err
		}
		if counts.InputTokens != nil && *counts.InputTokens <= int64(budget) {
			return counts, nil
		}
		request.Input = request.Input[:len(request.Input)-1]
	}
	counts, err := builder.countRequest(ctx, *request)
	if err != nil {
		return usage.Counts{Source: usage.SourceUnknown}, err
	}
	if counts.InputTokens != nil && *counts.InputTokens > int64(budget) {
		return usage.Counts{Source: usage.SourceUnknown}, errors.New("context exceeds input token budget")
	}
	return counts, nil
}

func (builder *Builder) countRequest(ctx stdcontext.Context, request model.Request) (usage.Counts, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return usage.Counts{}, err
	}
	inputTokens, err := builder.counter.Count(ctx, encoded)
	if err != nil {
		return usage.Counts{}, err
	}
	zero := int64(0)
	return usage.Counts{InputTokens: &inputTokens, OutputTokens: &zero, TotalTokens: &inputTokens, Source: usage.SourceEstimate, Exact: false}, nil
}

func (builder *Builder) projectInstructions(ctx stdcontext.Context) string {
	readResponse, err := builder.filesystem.Read(ctx, workspacefs.ReadRequest{Path: ".github/copilot-instructions.md", MaxBytes: 16 * 1024})
	if err != nil {
		return ""
	}
	return readResponse.Content
}

func formatEntries(listing workspacefs.ListResponse) string {
	paths := make([]string, 0, len(listing.Entries))
	for _, entry := range listing.Entries {
		paths = append(paths, entry.Path)
	}
	return strings.Join(paths, "\n")
}

func toolChoice(definitions []model.ToolDefinition) string {
	if len(definitions) == 0 {
		return ""
	}
	return "auto"
}
