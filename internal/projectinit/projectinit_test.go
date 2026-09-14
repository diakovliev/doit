package projectinit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/diakovliev/doit/internal/apperr"
)

func TestInitializeCreatesScaffold(t *testing.T) {
	workspace := t.TempDir()

	result, err := Initialize(context.Background(), workspace)
	if err != nil {
		t.Fatalf("initialize project: %v", err)
	}
	if result.Workspace != workspace {
		t.Fatalf("unexpected workspace: %q", result.Workspace)
	}
	expectedCreated := scaffoldPaths()
	assertInitialResult(t, result, expectedCreated)
	assertGeneratedConfig(t, workspace)
	assertGeneratedFiles(t, workspace, expectedCreated[1:])
}

func TestInitializePreservesExistingFiles(t *testing.T) {
	workspace := t.TempDir()
	configPath := filepath.Join(workspace, ".doit", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		t.Fatalf("make config directory: %v", err)
	}
	original := []byte(`{"default_profile":"custom","profiles":{}}`)
	if err := os.WriteFile(configPath, original, 0600); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	result := initializeOrFail(t, workspace)
	assertExistingConfigResult(t, result, original, workspace)
	second := initializeOrFail(t, workspace)
	if len(second.Created) != 0 || len(second.Existing) != len(templates) {
		t.Fatalf("unexpected repeat result: %+v", second)
	}
}

func TestInitializeRejectsTemplateDirectory(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".doit", "config.json"), 0700); err != nil {
		t.Fatalf("make conflicting directory: %v", err)
	}

	_, err := Initialize(context.Background(), workspace)
	if err == nil || apperr.KindOf(err) != apperr.KindConfig {
		t.Fatalf("expected config error, got %v", err)
	}
}

func TestInitializeHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Initialize(ctx, t.TempDir())
	if err == nil || apperr.KindOf(err) != apperr.KindCancelled || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation error, got %v", err)
	}
}

func scaffoldPaths() []string {
	return []string{
		".doit/config.json",
		".doit/instructions.md",
		".doit/instructions/README.md",
		".doit/skills/README.md",
		".doit/skills/example/SKILL.md.template",
	}
}

func assertInitialResult(t *testing.T, result Result, expectedCreated []string) {
	t.Helper()
	if !slices.Equal(result.Created, expectedCreated) || len(result.Existing) != 0 {
		t.Fatalf("unexpected initial result: %+v", result)
	}
}

func assertGeneratedConfig(t *testing.T, workspace string) {
	t.Helper()
	configContents := readWorkspaceFile(t, workspace, ".doit/config.json")
	var configuration struct {
		DefaultProfile string `json:"default_profile"`
		Profiles       map[string]struct {
			APIRoot string `json:"api_root"`
			Model   string `json:"model"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(configContents, &configuration); err != nil {
		t.Fatalf("parse generated config: %v", err)
	}
	if configuration.DefaultProfile != "lmstudio" || configuration.Profiles["lmstudio"].APIRoot == "" {
		t.Fatalf("unexpected generated config: %s", configContents)
	}
}

func assertGeneratedFiles(t *testing.T, workspace string, paths []string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Stat(filepath.Join(workspace, path)); err != nil {
			t.Fatalf("stat generated %s: %v", path, err)
		}
	}
}

func initializeOrFail(t *testing.T, workspace string) Result {
	t.Helper()
	result, err := Initialize(context.Background(), workspace)
	if err != nil {
		t.Fatalf("initialize project: %v", err)
	}
	return result
}

func assertExistingConfigResult(t *testing.T, result Result, original []byte, workspace string) {
	t.Helper()
	if len(result.Created) != len(templates)-1 || len(result.Existing) != 1 || result.Existing[0] != ".doit/config.json" {
		t.Fatalf("unexpected initialization result: %+v", result)
	}
	contents := readWorkspaceFile(t, workspace, ".doit/config.json")
	if string(contents) != string(original) {
		t.Fatalf("existing config changed: %q", contents)
	}
}

func readWorkspaceFile(t *testing.T, workspace, path string) []byte {
	t.Helper()
	root, err := os.OpenRoot(workspace)
	if err != nil {
		t.Fatalf("open workspace root: %v", err)
	}
	defer func() { _ = root.Close() }()
	contents, err := root.ReadFile(path)
	if err != nil {
		t.Fatalf("read workspace file %s: %v", path, err)
	}
	return contents
}
