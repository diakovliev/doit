package app

import (
	"bufio"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diakovliev/doit/internal/cli"
	"github.com/diakovliev/doit/internal/config"
	"github.com/diakovliev/doit/internal/policy"
	"github.com/diakovliev/doit/internal/process"
	"github.com/diakovliev/doit/internal/processrunner"
	"github.com/diakovliev/doit/internal/tools"
	"github.com/diakovliev/doit/internal/workspacefs"
)

func TestRunCompletesAgainstDeterministicResponsesBackend(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/responses" {
			http.Error(writer, "unexpected path", http.StatusNotFound)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"resp-test","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"repository explained"}]}],"usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}`))
	}))
	defer server.Close()

	workspace := t.TempDir()
	configPath := filepath.Join(workspace, "config.json")
	configContents := `{"default_profile":"local","profiles":{"local":{"api_root":"` + server.URL + `/v1","model":"test-model"}}}`
	if err := os.WriteFile(configPath, []byte(configContents), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("DOIT_CONFIG_FILE", configPath)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	status := cli.RunWithHandler([]string{"-C", workspace, "run", "explain", "this", "repository"}, strings.NewReader(""), &stdout, &stderr, Handler{})
	if status != 0 {
		t.Fatalf("expected successful CLI run, status=%d stderr=%q", status, stderr.String())
	}
	if !strings.Contains(stdout.String(), "repository explained") || !strings.Contains(stdout.String(), "input_tokens=4") {
		t.Fatalf("unexpected CLI output: %q", stdout.String())
	}
}

func TestPromptApprovalAcceptsExplicitYes(t *testing.T) {
	var output bytes.Buffer
	approved, err := promptApproval(context.Background(), bufio.NewReader(strings.NewReader("yes\n")), &output, policy.Action{Name: "code.apply_patch", Risk: tools.RiskWrite}, tools.Call{Arguments: []byte(`{"patch":"..."}`)})
	if err != nil || !approved {
		t.Fatalf("expected approval, approved=%t error=%v", approved, err)
	}
	if !strings.Contains(output.String(), "approval required") || !strings.Contains(output.String(), "allow this action") {
		t.Fatalf("unexpected approval prompt: %q", output.String())
	}
}

func TestConfiguredTaskRuns(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	workspace := t.TempDir()
	runner, err := processrunner.New(workspace, 1024)
	if err != nil {
		t.Fatalf("new process runner: %v", err)
	}
	if err := registerConfiguredTasks(runner, map[string]config.TaskConfig{"probe": {Executable: "git", Arguments: []string{"--version"}}}); err != nil {
		t.Fatalf("register configured tasks: %v", err)
	}
	result, err := runner.Run(context.Background(), process.Task{Name: "probe"})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("configured task failed: result=%+v error=%v", result, err)
	}
}

func TestBuildRegistryExposesGitAndProcessAutomationTools(t *testing.T) {
	workspace := t.TempDir()
	filesystem, err := workspacefs.New(workspace)
	if err != nil {
		t.Fatalf("new filesystem: %v", err)
	}
	processService, err := processrunner.New(workspace, 1024)
	if err != nil {
		t.Fatalf("new process runner: %v", err)
	}
	if err := registerConfiguredTasks(processService, map[string]config.TaskConfig{"probe": {Executable: "git", Arguments: []string{"--version"}}}); err != nil {
		t.Fatalf("register configured tasks: %v", err)
	}
	registry, err := buildRegistry(workspace, filesystem, processService)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	for _, name := range []string{"git.status", "git.diff", "git.stage", "git.commit", "git.restore", "process.run"} {
		tool, exists := registry.Lookup(name)
		if !exists {
			t.Fatalf("automation tool is not registered: %s", name)
		}
		if len(tool.Definition().Parameters) == 0 {
			t.Fatalf("automation tool has no model schema: %s", name)
		}
	}
}
