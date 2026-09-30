package contextbuilder_test

import (
	stdcontext "context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contextdata "github.com/diakovliev/doit/internal/contextbuilder"
	"github.com/diakovliev/doit/internal/model"
	"github.com/diakovliev/doit/internal/usage"
	"github.com/diakovliev/doit/internal/workspacefs"
)

func TestBuilderIncludesInstructionsAndSelectedFileWithinBudget(t *testing.T) {
	root := t.TempDir()
	writeGuidanceFixture(t, root)
	filesystem, err := workspacefs.New(root)
	if err != nil {
		t.Fatalf("new filesystem: %v", err)
	}
	builder := contextdata.New(filesystem, usage.ByteEstimator{}).WithGitStatus(func(stdcontext.Context) (string, error) {
		return " M README.md", nil
	})
	request, counts, err := builder.Build(stdcontext.Background(), contextdata.Request{Model: "test-model", UserInput: "explain the repository", Paths: []string{"README.md"}, MaxInputTokens: 1000, Tools: []model.ToolDefinition{{Type: "function", Name: "fs.read"}}})
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if request.Model != "test-model" || request.ToolChoice != "auto" || counts.InputTokens == nil || *counts.InputTokens > 1000 {
		t.Fatalf("unexpected context result: request=%+v usage=%+v", request, counts)
	}
	if request.Instructions == "" || len(request.Input) != 3 {
		t.Fatalf("expected instructions and selected file input: %+v", request)
	}
	assertFreshGitStatus(t, request.Input)
	assertGuidance(t, request.Instructions)
}

func TestBuilderLoadsGlobalGuidanceAndProjectOverridesAfterIt(t *testing.T) {
	root := t.TempDir()
	global := filepath.Join(t.TempDir(), ".doit")
	writeGlobalGuidanceFixture(t, global)
	writeProjectGuidanceFixture(t, root)
	filesystem, err := workspacefs.New(root)
	if err != nil {
		t.Fatalf("new filesystem: %v", err)
	}
	builder := contextdata.New(filesystem, usage.ByteEstimator{}).WithGlobalGuidanceRoot(global)
	request, _, err := builder.Build(stdcontext.Background(), contextdata.Request{Model: "test-model", UserInput: "continue", MaxInputTokens: 1000})
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	assertGuidanceOrder(t, request.Instructions)
}

func TestBuilderContextPolicyLimitsAutomaticMaterial(t *testing.T) {
	root := t.TempDir()
	writeGuidanceFixture(t, root)
	filesystem, err := workspacefs.New(root)
	if err != nil {
		t.Fatalf("new filesystem: %v", err)
	}
	builder := contextdata.New(filesystem, usage.ByteEstimator{}).WithGitStatus(func(stdcontext.Context) (string, error) {
		return " M README.md", nil
	})
	request, _, err := builder.Build(stdcontext.Background(), contextdata.Request{
		Model:          "test-model",
		UserInput:      "inspect README",
		Paths:          []string{"README.md"},
		Policy:         &contextdata.Policy{IncludeGuidance: false, IncludeGitStatus: false, IncludeWorkspaceListing: false},
		MaxInputTokens: 1000,
	})
	if err != nil {
		t.Fatalf("build restricted context: %v", err)
	}
	if len(request.Input) != 2 || strings.Contains(request.Instructions, "Follow repository rules.") || strings.Contains(request.Input[1].Content, "Current Git status") {
		t.Fatalf("automatic context policy was not applied: %+v instructions=%s", request.Input, request.Instructions)
	}
	if !strings.Contains(request.Input[1].Content, "repository contents") {
		t.Fatalf("explicit file context was unexpectedly removed: %+v", request.Input)
	}
}

func writeGlobalGuidanceFixture(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "skills", "shared"), 0700); err != nil {
		t.Fatalf("make global guidance directory: %v", err)
	}
	writeContextFile(t, filepath.Join(root, "instructions.md"), "Global instructions.")
	writeContextFile(t, filepath.Join(root, "skills", "shared", "SKILL.md"), "Global skill.")
}

func writeProjectGuidanceFixture(t *testing.T, root string) {
	t.Helper()
	projectRoot := filepath.Join(root, ".doit")
	if err := os.MkdirAll(filepath.Join(projectRoot, "skills", "shared"), 0700); err != nil {
		t.Fatalf("make project guidance directory: %v", err)
	}
	writeContextFile(t, filepath.Join(projectRoot, "instructions.md"), "Project instructions.")
	writeContextFile(t, filepath.Join(projectRoot, "skills", "shared", "SKILL.md"), "Project skill.")
}

func assertGuidanceOrder(t *testing.T, instructions string) {
	t.Helper()
	for _, expected := range []string{"Global instructions.", "Project instructions.", "Global skill.", "Project skill."} {
		if !strings.Contains(instructions, expected) {
			t.Fatalf("global/project guidance missing %q: %s", expected, instructions)
		}
	}
	if strings.Index(instructions, "Global instructions.") > strings.Index(instructions, "Project instructions.") || strings.Index(instructions, "Global skill.") > strings.Index(instructions, "Project skill.") {
		t.Fatalf("project guidance did not follow global guidance: %s", instructions)
	}
}

func writeGuidanceFixture(t *testing.T, root string) {
	t.Helper()
	for _, directory := range []string{".github", ".github/instructions", ".github/skills/review", ".doit/instructions", ".doit/skills/testing"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0700); err != nil {
			t.Fatalf("make guidance directory: %v", err)
		}
	}
	files := map[string]string{
		".github/copilot-instructions.md":              "Follow repository rules.",
		".github/instructions/project.instructions.md": "Use project conventions.",
		".github/skills/review/SKILL.md":               "Review changed code.",
		".doit/instructions.md":                        "Use local project rules.",
		".doit/instructions/testing.md":                "Run focused tests first.",
		".doit/skills/testing/SKILL.md":                "Prefer deterministic tests.",
		"README.md":                                    "repository contents",
	}
	for path, content := range files {
		writeContextFile(t, filepath.Join(root, path), content)
	}
}

func assertFreshGitStatus(t *testing.T, input []model.InputItem) {
	t.Helper()
	for _, item := range input {
		if strings.Contains(item.Content, "Current Git status") {
			return
		}
	}
	t.Fatalf("expected fresh Git status in context: %+v", input)
}

func TestBuilderTrimsFunctionCallsWithTheirOutputs(t *testing.T) {
	root := t.TempDir()
	filesystem, err := workspacefs.New(root)
	if err != nil {
		t.Fatalf("new filesystem: %v", err)
	}
	builder := contextdata.New(filesystem, itemCountCounter{})
	history := []model.InputItem{
		{Type: "message", Role: "assistant", Content: "old"},
		{Type: "function_call", CallID: "call-1", Name: "fs.read", Arguments: "{}"},
		{Type: "function_call_output", CallID: "call-1", Output: "{}"},
	}
	request, _, err := builder.Build(stdcontext.Background(), contextdata.Request{Model: "test-model", UserInput: "continue", History: history, MaxInputTokens: 2})
	if err != nil {
		t.Fatalf("build trimmed context: %v", err)
	}
	if hasOrphanedToolOutput(request.Input) {
		t.Fatalf("trimmed history contains orphaned tool output: %+v", request.Input)
	}
}

func TestBuilderDropsCompactMemoryBeforeRecentHistoryWhenBudgetIsTight(t *testing.T) {
	filesystem, err := workspacefs.New(t.TempDir())
	if err != nil {
		t.Fatalf("new filesystem: %v", err)
	}
	builder := contextdata.New(filesystem, itemCountCounter{})
	request, _, err := builder.Build(stdcontext.Background(), contextdata.Request{
		Model:     "test-model",
		UserInput: "continue",
		History: []model.InputItem{
			{Type: "message", Role: "user", Content: "Compressed prior session memory. older details"},
			{Type: "message", Role: "assistant", Content: "recent turn"},
		},
		MaxInputTokens: 2,
	})
	if err != nil {
		t.Fatalf("build budgeted context: %v", err)
	}
	if len(request.Input) != 2 || request.Input[0].Content != "recent turn" || request.Input[1].Content != "continue" {
		t.Fatalf("tight budget did not prefer recent exact context: %+v", request.Input)
	}
}

func TestFitRequestDropsAutomaticContextAndMemoryBeforeRecentTurns(t *testing.T) {
	builder := contextdata.New(nil, itemCountCounter{})
	request := model.Request{Input: []model.InputItem{
		{Type: "message", Role: "user", Content: "workspace listing", ContextPriority: model.ContextPriorityAutomatic},
		{Type: "message", Role: "user", Content: "compressed memory", ContextPriority: model.ContextPriorityMemory},
		{Type: "message", Role: "assistant", Content: "recent turn", ContextPriority: model.ContextPriorityRecent},
		{Type: "message", Role: "user", Content: "current request", ContextPriority: model.ContextPriorityCurrent},
	}}
	if _, err := builder.FitRequest(stdcontext.Background(), &request, 2); err != nil {
		t.Fatalf("fit ongoing request: %v", err)
	}
	if len(request.Input) != 2 || request.Input[0].Content != "recent turn" || request.Input[1].Content != "current request" {
		t.Fatalf("ongoing fit did not retain newer exact turns: %+v", request.Input)
	}
}

func TestFitRequestRollsTrimmedRecentTurnsIntoMemory(t *testing.T) {
	builder := contextdata.New(nil, itemCountCounter{})
	request := model.Request{Input: []model.InputItem{
		{Type: "message", Role: "assistant", Content: "older response one", ContextPriority: model.ContextPriorityRecent},
		{Type: "message", Role: "assistant", Content: "older response two", ContextPriority: model.ContextPriorityRecent},
		{Type: "message", Role: "user", Content: "current request", ContextPriority: model.ContextPriorityCurrent},
	}}
	if _, err := builder.FitRequest(stdcontext.Background(), &request, 2); err != nil {
		t.Fatalf("fit ongoing request: %v", err)
	}
	if len(request.Input) != 2 || !strings.HasPrefix(request.Input[0].Content, "Compressed prior session memory.") || !strings.Contains(request.Input[0].Content, "assistant: older response one") || !strings.Contains(request.Input[0].Content, "assistant: older response two") || request.Input[1].Content != "current request" {
		t.Fatalf("trimmed turns were not rolled into retained memory: %+v", request.Input)
	}
}

func TestFitRequestCarriesExactFactsFromTrimmedToolPair(t *testing.T) {
	builder := contextdata.New(nil, itemCountCounter{})
	request := model.Request{Input: []model.InputItem{
		{Type: "function_call", CallID: "read-1", Name: "fs.read", Arguments: `{"path":"internal/app/app.go"}`, ContextPriority: model.ContextPriorityRecent},
		{Type: "function_call_output", CallID: "read-1", Output: `{"status":"succeeded","data":{"changed_paths":["README.md"],"passed":true}}`, ContextPriority: model.ContextPriorityRecent},
		{Type: "message", Role: "user", Content: "current request", ContextPriority: model.ContextPriorityCurrent},
	}}
	if _, err := builder.FitRequest(stdcontext.Background(), &request, 2); err != nil {
		t.Fatalf("fit tool-result request: %v", err)
	}
	if len(request.Input) != 2 || !strings.Contains(request.Input[0].Content, `tool fs.read arguments = {"path":"internal/app/app.go"}`) || !strings.Contains(request.Input[0].Content, `changed_paths = ["README.md"]`) || !strings.Contains(request.Input[0].Content, "passed = true") {
		t.Fatalf("trimmed tool facts were not preserved in memory: %+v", request.Input)
	}
}

func TestBuilderExplainsBoundedSessionHistory(t *testing.T) {
	root := t.TempDir()
	filesystem, err := workspacefs.New(root)
	if err != nil {
		t.Fatalf("new filesystem: %v", err)
	}
	builder := contextdata.New(filesystem, usage.ByteEstimator{})
	request, _, err := builder.Build(stdcontext.Background(), contextdata.Request{
		Model:          "test-model",
		UserInput:      "continue the task",
		Tools:          []model.ToolDefinition{{Type: "function", Name: "session.history"}},
		MaxInputTokens: 1000,
	})
	if err != nil {
		t.Fatalf("build session history context: %v", err)
	}
	if !strings.Contains(request.Instructions, "session.history") || !strings.Contains(request.Instructions, "Do not treat summaries as exact evidence") {
		t.Fatalf("bounded history guidance was not included: %s", request.Instructions)
	}
}

func TestBuilderExplainsMutationWorkflow(t *testing.T) {
	root := t.TempDir()
	filesystem, err := workspacefs.New(root)
	if err != nil {
		t.Fatalf("new filesystem: %v", err)
	}
	builder := contextdata.New(filesystem, usage.ByteEstimator{})
	request, _, err := builder.Build(stdcontext.Background(), contextdata.Request{
		Model:          "test-model",
		UserInput:      "update the file",
		Tools:          []model.ToolDefinition{{Type: "function", Name: "code.replace_exact"}, {Type: "function", Name: "git.commit"}},
		MaxInputTokens: 1000,
	})
	if err != nil {
		t.Fatalf("build mutation workflow context: %v", err)
	}
	for _, expected := range []string{"inspect before changing files", "dry_run previews", "ambiguous", "including deleted paths"} {
		if !strings.Contains(request.Instructions, expected) {
			t.Fatalf("mutation workflow guidance missing %q: %s", expected, request.Instructions)
		}
	}
}

type itemCountCounter struct{}

func (itemCountCounter) Count(_ stdcontext.Context, content []byte) (int64, error) {
	var request model.Request
	if err := json.Unmarshal(content, &request); err != nil {
		return 0, err
	}
	return int64(len(request.Input)), nil
}

func hasOrphanedToolOutput(input []model.InputItem) bool {
	calls := make(map[string]bool)
	for _, item := range input {
		if item.Type == "function_call" {
			calls[item.CallID] = true
		}
		if item.Type == "function_call_output" && !calls[item.CallID] {
			return true
		}
	}
	return false
}

func assertGuidance(t *testing.T, instructions string) {
	t.Helper()
	for _, expected := range []string{"Follow repository rules.", "Use project conventions.", "Review changed code.", "Use local project rules.", "Run focused tests first.", "Prefer deterministic tests."} {
		if !strings.Contains(instructions, expected) {
			t.Fatalf("expected guidance %q in instructions: %s", expected, instructions)
		}
	}
}

func writeContextFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write context file: %v", err)
	}
}
