package contextbuilder_test

import (
	stdcontext "context"
	"os"
	"path/filepath"
	"testing"

	contextdata "github.com/diakovliev/doit/internal/contextbuilder"
	"github.com/diakovliev/doit/internal/model"
	"github.com/diakovliev/doit/internal/usage"
	"github.com/diakovliev/doit/internal/workspacefs"
)

func TestBuilderIncludesInstructionsAndSelectedFileWithinBudget(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".github"), 0700); err != nil {
		t.Fatalf("make instructions directory: %v", err)
	}
	writeContextFile(t, filepath.Join(root, ".github", "copilot-instructions.md"), "Follow repository rules.")
	writeContextFile(t, filepath.Join(root, "README.md"), "repository contents")
	filesystem, err := workspacefs.New(root)
	if err != nil {
		t.Fatalf("new filesystem: %v", err)
	}
	builder := contextdata.New(filesystem, usage.ByteEstimator{})
	request, counts, err := builder.Build(stdcontext.Background(), contextdata.Request{Model: "test-model", UserInput: "explain the repository", Paths: []string{"README.md"}, MaxInputTokens: 1000, Tools: []model.ToolDefinition{{Type: "function", Name: "fs.read"}}})
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if request.Model != "test-model" || request.ToolChoice != "auto" || counts.InputTokens == nil || *counts.InputTokens > 1000 {
		t.Fatalf("unexpected context result: request=%+v usage=%+v", request, counts)
	}
	if request.Instructions == "" || len(request.Input) != 2 {
		t.Fatalf("expected instructions and selected file input: %+v", request)
	}
}

func writeContextFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write context file: %v", err)
	}
}
