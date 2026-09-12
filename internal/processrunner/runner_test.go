package processrunner

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/diakovliev/doit/internal/process"
	"github.com/diakovliev/doit/internal/tools"
)

func TestRunnerExecutesAllowlistedTask(t *testing.T) {
	runner := newTestRunner(t)
	result, err := runner.Run(context.Background(), process.Task{Name: "echo", Arguments: []string{"hello"}})
	if err != nil {
		t.Fatalf("run task: %v", err)
	}
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, "hello") {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestRunnerRejectsUnknownTask(t *testing.T) {
	runner := newTestRunner(t)
	if _, err := runner.Run(context.Background(), process.Task{Name: "unknown"}); err == nil {
		t.Fatal("expected unknown task to be rejected")
	}
}

func TestRunnerTimesOut(t *testing.T) {
	runner := newTestRunner(t)
	result, err := runner.Run(context.Background(), process.Task{Name: "sleep", Timeout: 10 * time.Millisecond})
	if err == nil || !result.TimedOut {
		t.Fatalf("expected timeout, result=%+v error=%v", result, err)
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
		if err := runner.Register(Definition{Name: "echo", Executable: "cmd.exe", Arguments: []string{"/c", "echo"}}); err != nil {
			t.Fatalf("register echo: %v", err)
		}
		if err := runner.Register(Definition{Name: "sleep", Executable: "powershell.exe", Arguments: []string{"-NoProfile", "-Command", "Start-Sleep -Seconds 2"}}); err != nil {
			t.Fatalf("register sleep: %v", err)
		}
		return runner
	}
	if err := runner.Register(Definition{Name: "echo", Executable: "printf", Arguments: []string{"%s"}}); err != nil {
		t.Fatalf("register echo: %v", err)
	}
	if err := runner.Register(Definition{Name: "sleep", Executable: "sleep", Arguments: []string{"2"}}); err != nil {
		t.Fatalf("register sleep: %v", err)
	}
	return runner
}
