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

	"github.com/diakovliev/doit/internal/agent"
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

func TestProgressLineReplacesTerminalStatus(t *testing.T) {
	var output bytes.Buffer
	line := &progressLine{writer: &output, enabled: true, replace: true}
	line.Update(agent.ProgressEvent{Phase: "model", Message: "first action"})
	line.Update(agent.ProgressEvent{Phase: "tool", Message: "second action"})
	line.Clear()

	if strings.Contains(output.String(), "\n") || !strings.Contains(output.String(), "\r[doit] tool: second action") {
		t.Fatalf("expected one replaceable terminal status line: %q", output.String())
	}
}

func TestProgressLineKeepsCapturedOutputLineOriented(t *testing.T) {
	var output bytes.Buffer
	line := newProgressLine(&output, true)
	line.Update(agent.ProgressEvent{Phase: "model", Message: "captured action"})

	if output.String() != "[doit] model: captured action\n" {
		t.Fatalf("unexpected captured progress output: %q", output.String())
	}
}

func TestInitCreatesProjectScaffoldWithoutModel(t *testing.T) {
	workspace := t.TempDir()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	status := cli.RunWithHandler([]string{"-C", workspace, "init"}, strings.NewReader(""), &stdout, &stderr, Handler{})
	if status != 0 {
		t.Fatalf("expected init success, status=%d stderr=%q", status, stderr.String())
	}
	for _, path := range []string{
		".doit/config.json",
		".doit/instructions.md",
		".doit/instructions/README.md",
		".doit/skills/README.md",
		".doit/skills/example/SKILL.md.template",
	} {
		if _, err := os.Stat(filepath.Join(workspace, path)); err != nil {
			t.Fatalf("expected init file %s: %v", path, err)
		}
	}
	if !strings.Contains(stdout.String(), "Initialized doit") || !strings.Contains(stdout.String(), "created .doit/config.json") {
		t.Fatalf("unexpected init output: %q", stdout.String())
	}
}

func TestTestCommandRunsConfiguredTaskWithoutModel(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	workspace := t.TempDir()
	doitDirectory := filepath.Join(workspace, ".doit")
	if err := os.MkdirAll(doitDirectory, 0700); err != nil {
		t.Fatalf("make doit directory: %v", err)
	}
	configuration := `{"tasks":{"probe":{"executable":"git","arguments":["--version"],"kind":"test"}}}`
	if err := os.WriteFile(filepath.Join(doitDirectory, "config.json"), []byte(configuration), 0600); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	status := cli.RunWithHandler([]string{"-C", workspace, "test", "probe"}, strings.NewReader(""), &stdout, &stderr, Handler{})
	if status != 0 || !strings.Contains(stdout.String(), "passed=true") {
		t.Fatalf("configured test command failed: status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
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
