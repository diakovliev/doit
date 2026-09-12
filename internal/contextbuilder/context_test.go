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
	if !strings.Contains(input[1].Content, "Current Git status") {
		t.Fatalf("expected fresh Git status in context: %+v", input)
	}
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
