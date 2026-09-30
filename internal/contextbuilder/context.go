// Package contextbuilder builds bounded, token-aware model context.
package contextbuilder

import (
	stdcontext "context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/diakovliev/doit/internal/model"
	"github.com/diakovliev/doit/internal/usage"
	"github.com/diakovliev/doit/internal/workspacefs"
)

const defaultInputTokenBudget = 16000

const sessionHistoryGuidance = "Session context contains a recent exact window and a lossy summary of older public turns; selected paths and exact tool facts may be preserved separately. If an older detail matters, use session.history with a focused query or sequence cursor. Its results include concise summaries and bounded original event data. Do not treat summaries as exact evidence."

const toolWorkflowGuidance = "Tool workflow: inspect before changing files; use fs.read/fs.hash or git.status/git.diff first. Prefer exact structured edits for small changes and dry_run previews before applying. If a tool reports conflict or ambiguous matches, inspect again instead of retrying the same arguments. For commits, review git.status and git.diff, then pass exact changed paths to git.commit, including deleted paths."

// Builder selects local repository context for model requests.
type Builder struct {
	filesystem         *workspacefs.Service
	counter            usage.TokenCounter
	gitStatus          GitStatusProvider
	globalGuidanceRoot string
}

// GitStatusProvider returns a fresh, bounded repository status summary.
type GitStatusProvider func(stdcontext.Context) (string, error)

// New creates a context builder.
func New(filesystem *workspacefs.Service, counter usage.TokenCounter) *Builder {
	if counter == nil {
		counter = usage.ByteEstimator{}
	}
	globalRoot := ""
	if home, err := os.UserHomeDir(); err == nil {
		globalRoot = filepath.Join(home, ".doit")
	}
	return &Builder{filesystem: filesystem, counter: counter, globalGuidanceRoot: globalRoot}
}

// WithGlobalGuidanceRoot overrides the home .doit guidance root, primarily for embedding callers and tests.
func (builder *Builder) WithGlobalGuidanceRoot(root string) *Builder {
	if builder != nil {
		builder.globalGuidanceRoot = root
	}
	return builder
}

// WithGitStatus adds fresh repository status to each newly built context.
func (builder *Builder) WithGitStatus(provider GitStatusProvider) *Builder {
	if builder != nil {
		builder.gitStatus = provider
	}
	return builder
}

// Request describes context to include in a model request.
type Request struct {
	Model           string
	ThinkingEffort  string
	Policy          *Policy
	UserInput       string
	Instructions    string
	History         []model.InputItem
	Paths           []string
	Tools           []model.ToolDefinition
	MaxInputTokens  int
	MaxOutputTokens int
}

// Policy controls which repository material is included automatically.
type Policy struct {
	IncludeGuidance         bool
	IncludeGitStatus        bool
	IncludeWorkspaceListing bool
}

// Build returns a token-bounded normalized model request and its input usage.
func (builder *Builder) Build(ctx stdcontext.Context, request Request) (model.Request, usage.Counts, error) {
	if err := builder.validateRequest(request); err != nil {
		return model.Request{}, usage.Counts{Source: usage.SourceUnknown}, err
	}
	input, policy := builder.buildInput(ctx, request)
	instructions := builder.buildInstructions(ctx, request.Instructions, request.Tools, policy.IncludeGuidance)
	normalized := model.Request{Model: request.Model, ThinkingEffort: request.ThinkingEffort, Instructions: strings.TrimSpace(instructions), Input: input, Tools: request.Tools, ToolChoice: toolChoice(request.Tools), MaxOutputTokens: request.MaxOutputTokens}
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

func (builder *Builder) validateRequest(request Request) error {
	if builder == nil || builder.filesystem == nil {
		return errors.New("context builder filesystem is required")
	}
	if request.UserInput == "" {
		return errors.New("context request is empty")
	}
	return nil
}

func (builder *Builder) buildInput(ctx stdcontext.Context, request Request) ([]model.InputItem, *Policy) {
	input := append([]model.InputItem(nil), request.History...)
	for index := range input {
		if strings.HasPrefix(input[index].Content, rollingMemoryPrefix) {
			input[index].ContextPriority = model.ContextPriorityMemory
		} else if input[index].ContextPriority != model.ContextPriorityMemory && input[index].ContextPriority != model.ContextPriorityRollingMemory {
			input[index].ContextPriority = model.ContextPriorityRecent
		}
	}
	policy := request.Policy
	if policy == nil {
		policy = &Policy{IncludeGuidance: true, IncludeGitStatus: true, IncludeWorkspaceListing: true}
	}
	input = append(input, model.InputItem{Type: "message", Role: "user", Content: request.UserInput, ContextPriority: model.ContextPriorityCurrent})
	input = builder.appendExplicitPaths(ctx, input, request.Paths)
	if policy.IncludeGitStatus {
		input = builder.appendGitStatus(ctx, input)
	}
	if policy.IncludeWorkspaceListing {
		input = builder.appendWorkspaceListing(ctx, input, len(request.Paths) == 0)
	}
	return input, policy
}

func (builder *Builder) buildInstructions(ctx stdcontext.Context, instructions string, definitions []model.ToolDefinition, includeGuidance bool) string {
	if includeGuidance {
		instructions += "\n" + builder.projectInstructions(ctx)
	}
	if hasToolWorkflow(definitions) {
		instructions += "\n\n" + toolWorkflowGuidance
	}
	if hasTool(definitions, "session.history") {
		instructions += "\n\n" + sessionHistoryGuidance
	}
	return strings.TrimSpace(instructions)
}

func (builder *Builder) appendExplicitPaths(ctx stdcontext.Context, input []model.InputItem, paths []string) []model.InputItem {
	for _, path := range paths {
		readResponse, err := builder.filesystem.Read(ctx, workspacefs.ReadRequest{Path: path, MaxBytes: 16 * 1024})
		if err == nil {
			input = append(input, model.InputItem{Type: "message", Role: "user", Content: "File: " + readResponse.Path + "\n" + readResponse.Content, ContextPriority: model.ContextPrioritySelected})
		}
	}
	return input
}

func (builder *Builder) appendWorkspaceListing(ctx stdcontext.Context, input []model.InputItem, include bool) []model.InputItem {
	if include {
		if listing, err := builder.filesystem.List(ctx, workspacefs.ListRequest{MaxEntries: 100}); err == nil {
			input = append(input, model.InputItem{Type: "message", Role: "user", Content: "Workspace entries:\n" + formatEntries(listing)})
		}
	}
	return input
}

func (builder *Builder) appendGitStatus(ctx stdcontext.Context, input []model.InputItem) []model.InputItem {
	if builder.gitStatus == nil {
		return input
	}
	status, err := builder.gitStatus(ctx)
	if err != nil || status == "" {
		return input
	}
	return append(input, model.InputItem{Type: "message", Role: "user", Content: "Current Git status:\n" + status})
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
		trimmed, removed, ok := dropOldestContextItem(request.Input)
		if !ok {
			return usage.Counts{Source: usage.SourceUnknown}, errors.New("context exceeds input token budget")
		}
		request.Input = trimmed
		if hasRecentContext(removed) {
			request.Input = updateRollingMemory(request.Input, removed)
		}
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

// FitRequest trims an already-running model request to the per-request input budget.
// It preserves the newest user message and removes tool call/output pairs atomically.
func (builder *Builder) FitRequest(ctx stdcontext.Context, request *model.Request, budget int) (usage.Counts, error) {
	if request == nil {
		return usage.Counts{Source: usage.SourceUnknown}, errors.New("model request is required")
	}
	if budget <= 0 {
		budget = defaultInputTokenBudget
	}
	for {
		counts, err := builder.countRequest(ctx, *request)
		if err != nil {
			return usage.Counts{Source: usage.SourceUnknown}, err
		}
		if counts.InputTokens != nil && *counts.InputTokens <= int64(budget) {
			return counts, nil
		}
		trimmed, removed, ok := dropOldestContextItem(request.Input)
		if !ok {
			return usage.Counts{Source: usage.SourceUnknown}, errors.New("model request exceeds input token budget")
		}
		request.Input = trimmed
		if hasRecentContext(removed) {
			request.Input = updateRollingMemory(request.Input, removed)
		}
	}
}

func dropOldestContextItem(input []model.InputItem) (trimmed []model.InputItem, removed []model.InputItem, ok bool) {
	protectedUser := lastUserMessage(input)
	dropIndex := -1
	for index, item := range input {
		if index == protectedUser {
			continue
		}
		if dropIndex < 0 || item.ContextPriority < input[dropIndex].ContextPriority {
			dropIndex = index
		}
	}
	if dropIndex < 0 {
		return input, nil, false
	}
	item := input[dropIndex]
	if item.Type == "function_call" || item.Type == "function_call_output" {
		if trimmed, ok := dropFunctionPair(input, dropIndex, item.CallID); ok {
			matching := matchingFunctionItem(input, dropIndex, item.CallID)
			return trimmed, []model.InputItem{input[min(dropIndex, matching)], input[max(dropIndex, matching)]}, true
		}
	}
	trimmed = append(append([]model.InputItem(nil), input[:dropIndex]...), input[dropIndex+1:]...)
	return trimmed, []model.InputItem{item}, true
}

func hasRecentContext(items []model.InputItem) bool {
	for _, item := range items {
		if item.ContextPriority == model.ContextPriorityRecent {
			return true
		}
	}
	return false
}

func lastUserMessage(input []model.InputItem) int {
	selected := -1
	priority := -1
	for index := len(input) - 1; index >= 0; index-- {
		item := input[index]
		if item.Type == "message" && item.Role == "user" && item.ContextPriority >= priority {
			selected = index
			priority = item.ContextPriority
		}
	}
	return selected
}

func dropFunctionPair(input []model.InputItem, index int, callID string) ([]model.InputItem, bool) {
	matching := matchingFunctionItem(input, index, callID)
	if matching < 0 {
		return nil, false
	}
	first, second := index, matching
	if first > second {
		first, second = second, first
	}
	trimmed := make([]model.InputItem, 0, len(input)-2)
	trimmed = append(trimmed, input[:first]...)
	trimmed = append(trimmed, input[first+1:second]...)
	trimmed = append(trimmed, input[second+1:]...)
	return trimmed, true
}

func matchingFunctionItem(input []model.InputItem, index int, callID string) int {
	if input[index].Type == "function_call" {
		for offset := range input[index+1:] {
			candidate := index + 1 + offset
			if input[candidate].Type == "function_call_output" && input[candidate].CallID == callID {
				return candidate
			}
		}
		return -1
	}
	for candidate := range input[:index] {
		if input[candidate].Type == "function_call" && input[candidate].CallID == callID {
			return candidate
		}
	}
	return -1
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
	if err := ctx.Err(); err != nil {
		return ""
	}
	return loadRepositoryGuidance(ctx, builder.filesystem.Root(), builder.globalGuidanceRoot)
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

func hasTool(definitions []model.ToolDefinition, name string) bool {
	for _, definition := range definitions {
		if definition.Name == name {
			return true
		}
	}
	return false
}

func hasToolWorkflow(definitions []model.ToolDefinition) bool {
	for _, definition := range definitions {
		switch definition.Name {
		case "code.apply_patch", "code.replace_exact", "code.insert_at_anchor", "code.delete_exact", "fs.write", "fs.move", "fs.remove", "git.stage", "git.commit", "git.restore":
			return true
		}
	}
	return false
}
