package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diakovliev/doit/internal/agent"
	"github.com/diakovliev/doit/internal/cli"
	"github.com/diakovliev/doit/internal/config"
	"github.com/diakovliev/doit/internal/policy"
	"github.com/diakovliev/doit/internal/process"
	"github.com/diakovliev/doit/internal/processrunner"
	"github.com/diakovliev/doit/internal/session"
	"github.com/diakovliev/doit/internal/tools"
	"github.com/diakovliev/doit/internal/usage"
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
	assertMetricCLIOutput(t, stdout.String())
}

func assertMetricCLIOutput(t *testing.T, output string) {
	t.Helper()
	for _, expected := range []string{"repository explained", "input_tokens=4", "context_tokens_total=", "[doit] metrics: step=1 context=", "generation_tps=unknown", "request_tps=", "average_metrics steps=1"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("CLI output missing %q: %s", expected, output)
		}
	}
}

func TestProgressLineReplacesTerminalStatus(t *testing.T) {
	var output bytes.Buffer
	line := &progressLine{writer: &output, enabled: true, replace: true}
	line.Update(agent.ProgressEvent{Phase: "session", Message: "session started"})
	line.Update(agent.ProgressEvent{Phase: "metrics", Message: "step=1 context=1200 generation_tps=unknown request_tps=2.50"})
	line.Update(agent.ProgressEvent{Phase: "model", Message: "first action"})
	line.Update(agent.ProgressEvent{Phase: "tool", Message: "second action"})
	line.Clear()

	for _, expected := range []string{"[doit] metrics: step=1 context=1200", "[doit] model: first action", "[doit] tool: second action", "\x1b[1A\r\x1b[2K[doit] metrics:"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("expected metrics-above-status progress display containing %q: %q", expected, output.String())
		}
	}
}

func TestWriteOutcomeShowsOnlyAverageMetrics(t *testing.T) {
	rate := 20.0
	outcome := agent.Outcome{SessionID: "session-1", Text: "finished", Steps: []usage.StepMetrics{{Round: 1, ContextTokens: 100, OutputTokens: 20, RequestWallClockTokensPerSecond: 5, OutputTokensPerSecond: &rate}}, StepSummary: usage.SummarizeSteps([]usage.StepMetrics{{ContextTokens: 100, OutputTokens: 20, RequestWallClockTokensPerSecond: 5, OutputTokensPerSecond: &rate}})}
	var output bytes.Buffer
	if err := writeOutcome(&output, "human", outcome); err != nil {
		t.Fatalf("write human outcome: %v", err)
	}
	if !strings.Contains(output.String(), "average_metrics steps=1 context_tokens_per_step=100 generation_tokens_per_second=20.00") || strings.Contains(output.String(), "step=1 context_tokens=") {
		t.Fatalf("expected only average metrics in final output: %s", output.String())
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

func TestWriteHumanSession(t *testing.T) {
	created := time.Date(2026, time.September, 30, 10, 0, 0, 0, time.UTC)
	result := session.Record{
		Metadata: session.Metadata{ID: "session-1", Status: session.StatusCompleted, Command: "run", Profile: "local", Model: "test-model", InvocationPath: "/workspace", CreatedAt: created, UpdatedAt: created, Resumable: true},
		Events: []session.Event{
			{Sequence: 1, Timestamp: created, Type: "request", Data: json.RawMessage(`{"input":[{"type":"message","role":"user","content":"inspect the repository"}]}`)},
			{Sequence: 2, Timestamp: created, Type: "model_message", Data: json.RawMessage(`{"status":"completed","text":"done","tool_calls":[]}`)},
		},
		Result: &session.Result{Summary: "done", ChangedPaths: []string{"README.md"}},
	}
	var output bytes.Buffer
	if err := writeHumanSession(&output, result); err != nil {
		t.Fatalf("write human session: %v", err)
	}
	for _, expected := range []string{"Session session-1", "status=completed", "#1", "prompt=inspect the repository", "text=done", "result=done", "changed_paths=README.md"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("human session output missing %q: %s", expected, output.String())
		}
	}
}

func TestWriteMarkdownSession(t *testing.T) {
	record := session.Record{Metadata: session.Metadata{ID: "session-1", Status: session.StatusCompleted, Command: "run", Model: "test-model"}, Events: []session.Event{{Sequence: 1, Type: "model_message", Data: json.RawMessage(`{"status":"completed","text":"done"}`)}}, Result: &session.Result{Summary: "done\n\n- detail", ChangedPaths: []string{"README.md"}, ContextTokensTotal: 1200, Steps: []usage.StepMetrics{{Round: 1, ContextTokens: 1200, ContextSource: usage.SourceEstimate, ProviderInputTokens: pointerApp(1100), OutputTokens: 200, OutputSource: usage.SourceProvider, RequestDurationMs: 2000, RequestWallClockTokensPerSecond: 100, GenerationDurationMs: 1000, OutputTokensPerSecond: pointerAppRate(200)}}, StepSummary: usage.SummarizeSteps([]usage.StepMetrics{{ContextTokens: 1200, RequestWallClockTokensPerSecond: 100, OutputTokensPerSecond: pointerAppRate(200)}})}}
	var output bytes.Buffer
	if err := writeMarkdownSession(&output, record); err != nil {
		t.Fatalf("write markdown session: %v", err)
	}
	for _, expected := range []string{"# Session `session-1`", "| Status | `completed` |", "## Events", "| # | Time | Type | Status | Tool calls | Text |", "| 1 |", "completed", "| 0 | done |", "## Full Text", "<summary>Event #1 (model_message)</summary>", "<pre>done</pre>", "## Result", "### Summary", "- detail", "Context tokens across model rounds:** 1200", "### Model Steps", "| Step | Context | Provider input | Output | Output source | Generation tokens/sec | Generation time | Request tokens/sec | Request time |", "| 1 | 1200 `local-estimate` | 1100 | 200 | `provider` | 200.00 | 1000 ms | 100.00 | 2000 ms |", "### Average Metrics", "Context tokens per step: 1200", "Generation tokens/sec: 200.00 across 1 streamed step(s)", "Request-wall tokens/sec: 100.00", "### Changed Paths", "- `README.md`"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("markdown session output missing %q: %s", expected, output.String())
		}
	}
}

func TestLegacyWallClockTPSIsNotShownAsGenerationTPS(t *testing.T) {
	legacy := usage.StepMetrics{Round: 1, OutputTokens: 104, OutputTokensPerSecond: pointerAppRate(2.53), LegacyDurationKind: "request_wall_clock", LegacyDurationMs: 41117}
	record := session.Record{Metadata: session.Metadata{ID: "legacy-session"}, Result: &session.Result{Steps: []usage.StepMetrics{legacy}}}
	var output bytes.Buffer
	if err := writeMarkdownSession(&output, record); err != nil {
		t.Fatalf("write legacy session markdown: %v", err)
	}
	if !strings.Contains(output.String(), "| unknown | unknown | 2.53 | 41117 ms |") {
		t.Fatalf("legacy wall-clock TPS was mislabeled as generation speed: %s", output.String())
	}
}

func TestInspectSessionDefaultsToLatest(t *testing.T) {
	store, err := session.NewFileStore(session.Options{InvocationPath: t.TempDir()})
	if err != nil {
		t.Fatalf("new session store: %v", err)
	}
	defer func() { _ = store.Close() }()
	first, err := store.Start(context.Background(), session.Metadata{Command: "run", Model: "first"})
	if err != nil {
		t.Fatalf("start first session: %v", err)
	}
	if err := store.Complete(context.Background(), first, session.Result{Summary: "first result"}); err != nil {
		t.Fatalf("complete first session: %v", err)
	}
	second, err := store.Start(context.Background(), session.Metadata{Command: "run", Model: "second"})
	if err != nil {
		t.Fatalf("start second session: %v", err)
	}
	if err := store.Complete(context.Background(), second, session.Result{Summary: "latest result"}); err != nil {
		t.Fatalf("complete second session: %v", err)
	}
	var output bytes.Buffer
	if err := inspectSession(context.Background(), cli.Invocation{Arguments: []string{"inspect"}}, &output, store, "inspect"); err != nil {
		t.Fatalf("inspect latest session: %v", err)
	}
	if !strings.Contains(output.String(), "latest result") || strings.Contains(output.String(), "first result") {
		t.Fatalf("inspection did not select latest session: %s", output.String())
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

func pointerApp(value int64) *int64 {
	return &value
}

func pointerAppRate(value float64) *float64 {
	return &value
}
