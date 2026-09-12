package processrunner

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/diakovliev/doit/internal/process"
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
