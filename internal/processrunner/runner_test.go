package processrunner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/diakovliev/doit/internal/process"
	"github.com/diakovliev/doit/internal/tools"
)

func TestRunnerExecutesAllowlistedTask(t *testing.T) {
	runner := newTestRunner(t)
	result, err := runner.Run(context.Background(), process.Task{Name: "echo", Arguments: []string{"hello"}, WorkingDirectory: "."})
	if err != nil {
		t.Fatalf("run task: %v", err)
	}
	if result.ExitCode != 0 || !result.Passed || result.Task != "echo" || result.Kind != "test" || !strings.Contains(result.Stdout, "hello") {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestParseDiagnostics(t *testing.T) {
	diagnostics := parseDiagnostics("main.go:12:4: error: undefined: value\nwarning.go:7: warning: check this")
	if len(diagnostics) != 2 {
		t.Fatalf("unexpected diagnostics: %+v", diagnostics)
	}
	if diagnostics[0].Path != "main.go" || diagnostics[0].Line != 12 || diagnostics[0].Column != 4 || diagnostics[0].Severity != "error" {
		t.Fatalf("unexpected first diagnostic: %+v", diagnostics[0])
	}
	if diagnostics[1].Path != "warning.go" || diagnostics[1].Line != 7 || diagnostics[1].Severity != "warning" {
		t.Fatalf("unexpected second diagnostic: %+v", diagnostics[1])
	}
}

func TestRunnerRejectsUnknownTask(t *testing.T) {
	runner := newTestRunner(t)
	if _, err := runner.Run(context.Background(), process.Task{Name: "unknown"}); err == nil {
		t.Fatal("expected unknown task to be rejected")
	}
}

func TestRunnerRejectsShellComposition(t *testing.T) {
	runner, err := New(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	if err := runner.Register(Definition{Name: "unsafe", Executable: "echo && whoami"}); err == nil {
		t.Fatal("expected shell composition to be rejected")
	}
}

func TestRunnerTimesOut(t *testing.T) {
	runner := newTestRunner(t)
	result, err := runner.Run(context.Background(), process.Task{Name: "sleep", Timeout: 10 * time.Millisecond})
	if err == nil || !result.TimedOut {
		t.Fatalf("expected timeout, result=%+v error=%v", result, err)
	}
}

func TestWorkingDirectoryRejectsSymlinkEscape(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	linkPath := filepath.Join(workspace, "linked")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	runner, err := New(workspace, 1024)
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	if _, err := runner.workingDirectory("linked"); err == nil {
		t.Fatal("expected symlinked working directory to be rejected")
	}
}

func TestWorkingDirectoryAcceptsWorkspaceRoot(t *testing.T) {
	workspace := t.TempDir()
	runner, err := New(workspace, 1024)
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	workingDirectory, err := runner.workingDirectory(".")
	if err != nil {
		t.Fatalf("workspace root was rejected: %v", err)
	}
	if workingDirectory != workspace {
		t.Fatalf("unexpected working directory: %q", workingDirectory)
	}
}

func TestProcessToolAdvertisesAndExecutesAllowlistedTask(t *testing.T) {
	runner := newTestRunner(t)
	registry := tools.NewRegistry()
	if err := RegisterTool(registry, runner); err != nil {
		t.Fatalf("register process tool: %v", err)
	}
	tool, exists := registry.Lookup("process.run")
	if !exists {
		t.Fatal("process tool was not registered")
	}
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tool.Definition().Parameters, &schema); err != nil {
		t.Fatalf("decode process schema: %v", err)
	}
	if !contains(schema.Properties["task"].Enum, "echo") {
		t.Fatalf("process schema omitted echo task: %+v", schema.Properties["task"].Enum)
	}
	result := tool.Execute(context.Background(), tools.Call{Name: "process.run", Arguments: []byte(`{"task":"echo","args":["hello"],"timeout":"1s"}`)})
	if result.Status != tools.StatusSucceeded {
		t.Fatalf("process tool failed: %+v", result)
	}
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func newTestRunner(t *testing.T) *Runner {
	t.Helper()
	runner, err := New(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	if runtime.GOOS == "windows" {
		if err := runner.Register(Definition{Name: "echo", Executable: "cmd.exe", Arguments: []string{"/c", "echo"}, Kind: "test"}); err != nil {
			t.Fatalf("register echo: %v", err)
		}
		if err := runner.Register(Definition{Name: "sleep", Executable: "powershell.exe", Arguments: []string{"-NoProfile", "-Command", "Start-Sleep -Seconds 2"}, Kind: "test"}); err != nil {
			t.Fatalf("register sleep: %v", err)
		}
		return runner
	}
	if err := runner.Register(Definition{Name: "echo", Executable: "printf", Arguments: []string{"%s"}, Kind: "test"}); err != nil {
		t.Fatalf("register echo: %v", err)
	}
	if err := runner.Register(Definition{Name: "sleep", Executable: "sleep", Arguments: []string{"2"}, Kind: "test"}); err != nil {
		t.Fatalf("register sleep: %v", err)
	}
	return runner
}
