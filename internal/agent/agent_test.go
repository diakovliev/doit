package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	contextdata "github.com/diakovliev/doit/internal/contextbuilder"
	"github.com/diakovliev/doit/internal/model"
	"github.com/diakovliev/doit/internal/policy"
	"github.com/diakovliev/doit/internal/process"
	"github.com/diakovliev/doit/internal/session"
	"github.com/diakovliev/doit/internal/tools"
	"github.com/diakovliev/doit/internal/usage"
	"github.com/diakovliev/doit/internal/workspacefs"
)

type sequenceClient struct {
	responses []model.Response
	index     int
	requests  []model.Request
}

type streamingTestClient struct {
	streamed    bool
	streamError bool
	fallback    bool
}

func (client *streamingTestClient) Create(context.Context, model.Request) (model.Response, error) {
	client.fallback = true
	return model.Response{Status: "completed", Text: "fallback"}, nil
}

func (client *streamingTestClient) CreateStream(_ context.Context, _ model.Request, onEvent func(model.StreamEvent) error) (model.Response, error) {
	client.streamed = true
	if client.streamError {
		return model.Response{}, errors.New("streaming transport unavailable")
	}
	if err := onEvent(model.StreamEvent{Type: "response.output_text.delta", Text: "streamed"}); err != nil {
		return model.Response{}, err
	}
	if err := onEvent(model.StreamEvent{Type: "response.completed"}); err != nil {
		return model.Response{}, err
	}
	return model.Response{Status: "completed", Text: "streamed"}, nil
}

func TestRunnerUsesConfiguredStreamingClient(t *testing.T) {
	root := t.TempDir()
	runner, store := newAgentTestRunnerWithEphemeral(t, root, true)
	defer func() { _ = store.Close() }()
	client := &streamingTestClient{}
	runner.Client = client
	runner.Streaming = true
	outcome, err := runner.Run(context.Background(), Task{Command: "run", Request: "stream", Workspace: root, Model: "test-model"})
	if err != nil || outcome.Text != "streamed" || !client.streamed {
		t.Fatalf("streaming client was not used: outcome=%+v streamed=%t error=%v", outcome, client.streamed, err)
	}
}

func TestRunnerEmitsPublicModelProgress(t *testing.T) {
	root := t.TempDir()
	runner, store := newAgentTestRunnerWithEphemeral(t, root, true)
	defer func() { _ = store.Close() }()
	var events []ProgressEvent
	runner.Progress = func(event ProgressEvent) { events = append(events, event) }
	runner.Verbose = true
	if _, err := runner.Run(context.Background(), Task{Command: "run", Request: "progress", Workspace: root, Model: "test-model"}); err != nil {
		t.Fatalf("run progress task: %v", err)
	}
	assertProgressMessage(t, events, "thinking about the next action")
	assertProgressMessage(t, events, "selected 1 tool action(s)")
	assertProgressMessage(t, events, "finished composing public response")
	assertProgressMessage(t, events, "public response status=in_progress")
	assertProgressMessage(t, events, "public model text: finished")
}

func assertProgressMessage(t *testing.T, events []ProgressEvent, expected string) {
	t.Helper()
	for _, event := range events {
		if strings.Contains(event.Message, expected) {
			return
		}
	}
	t.Fatalf("progress message %q was not emitted: %+v", expected, events)
}

func TestRunnerFallsBackWhenStreamingIsUnavailable(t *testing.T) {
	root := t.TempDir()
	runner, store := newAgentTestRunnerWithEphemeral(t, root, true)
	defer func() { _ = store.Close() }()
	runner.Streaming = true
	outcome, err := runner.Run(context.Background(), Task{Command: "run", Request: "fallback", Workspace: root, Model: "test-model"})
	if err != nil || outcome.Text != "finished" {
		t.Fatalf("streaming fallback failed: outcome=%+v error=%v", outcome, err)
	}
}

func TestRunnerFallsBackWhenStreamingFails(t *testing.T) {
	root := t.TempDir()
	runner, store := newAgentTestRunnerWithEphemeral(t, root, true)
	defer func() { _ = store.Close() }()
	client := &streamingTestClient{streamError: true}
	runner.Client = client
	runner.Streaming = true
	outcome, err := runner.Run(context.Background(), Task{Command: "run", Request: "stream error", Workspace: root, Model: "test-model"})
	if err != nil || outcome.Text != "fallback" || !client.streamed || !client.fallback {
		t.Fatalf("streaming error fallback failed: outcome=%+v client=%+v error=%v", outcome, client, err)
	}
}

func TestRunnerContinuesAfterSessionUsageThreshold(t *testing.T) {
	root := t.TempDir()
	runner, store := newAgentTestRunnerWithEphemeral(t, root, true)
	defer func() { _ = store.Close() }()
	client := runner.Client.(*sequenceClient)
	outcome, err := runner.Run(context.Background(), Task{Command: "run", Request: "budget", Workspace: root, Model: "test-model", MaxSessionTokens: 1, NewSession: true})
	if err != nil || outcome.Text != "finished" {
		t.Fatalf("expected bounded session to complete after threshold: outcome=%+v error=%v", outcome, err)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected model/tool loop to continue after threshold: %d request(s)", len(client.requests))
	}
	record, loadErr := store.Load(context.Background(), outcome.SessionID)
	if loadErr != nil || record.Result == nil || record.Result.Summary != "finished" {
		t.Fatalf("completed result was not persisted: record=%+v error=%v", record, loadErr)
	}
}

func (client *sequenceClient) Create(_ context.Context, request model.Request) (model.Response, error) {
	client.requests = append(client.requests, request)
	response := client.responses[client.index]
	if client.index < len(client.responses)-1 {
		client.index++
	}
	return response, nil
}

func TestRunnerChangesThinkingEffortBetweenRounds(t *testing.T) {
	root := t.TempDir()
	runner, store := newAgentTestRunnerWithEphemeral(t, root, true)
	defer func() { _ = store.Close() }()
	client := &sequenceClient{responses: []model.Response{
		{ID: "tool", Status: "in_progress", ToolCalls: []model.ToolCall{{CallID: "read", Name: "fs.read", Arguments: `{"path":"README.md"}`}}},
		{ID: "done", Status: "completed", Text: "finished"},
	}}
	runner.Client = client
	_, err := runner.Run(context.Background(), Task{Command: "run", Request: "inspect", Workspace: root, Model: "test-model", ThinkingEffort: "low", ThinkingEffortForRound: func(round int) string {
		if round > 0 {
			return "high"
		}
		return "low"
	}})
	if err != nil {
		t.Fatalf("run dynamic effort task: %v", err)
	}
	if len(client.requests) != 2 || client.requests[0].ThinkingEffort != "low" || client.requests[1].ThinkingEffort != "high" {
		t.Fatalf("unexpected per-round thinking effort: %+v", client.requests)
	}
}

func TestRunnerAppliesModelRequestedThinkingEffortForNextRound(t *testing.T) {
	root := t.TempDir()
	runner, store := newAgentTestRunnerWithEphemeral(t, root, true)
	defer func() { _ = store.Close() }()
	client := &sequenceClient{responses: []model.Response{
		{ID: "effort", Status: "in_progress", ToolCalls: []model.ToolCall{{CallID: "effort-call", Name: thinkingEffortToolName, Arguments: `{"effort":"high","reason":"synthesize the next step"}`}}},
		{ID: "done", Status: "completed", Text: "finished"},
	}}
	runner.Client = client
	if _, err := runner.Run(context.Background(), Task{Command: "run", Request: "inspect", Workspace: root, Model: "test-model", ThinkingEffort: "low"}); err != nil {
		t.Fatalf("run adaptive effort task: %v", err)
	}
	if len(client.requests) != 2 || client.requests[0].ThinkingEffort != "low" || client.requests[1].ThinkingEffort != "high" {
		t.Fatalf("unexpected adaptive thinking effort: %+v", client.requests)
	}
}

func TestRunnerAutomaticallyResumesLatestWorkspaceSession(t *testing.T) {
	root := t.TempDir()
	runner, store := newAgentTestRunnerWithEphemeral(t, root, false)
	defer func() { _ = store.Close() }()
	client := runner.Client.(*sequenceClient)
	first, err := runner.Run(context.Background(), Task{Command: "run", Request: "first request", Workspace: root, Model: "test-model"})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, err := runner.Run(context.Background(), Task{Command: "run", Request: "second request", Workspace: root, Model: "test-model"})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if first.SessionID != second.SessionID {
		t.Fatalf("expected session reuse, first=%q second=%q", first.SessionID, second.SessionID)
	}
	if len(client.requests) != 3 {
		t.Fatalf("unexpected model request count: %d", len(client.requests))
	}
	if !requestContains(client.requests[2], "first request") || !requestContains(client.requests[2], "finished") || !requestContains(client.requests[2], "second request") {
		t.Fatalf("resumed request did not contain prior public turns: %+v", client.requests[2].Input)
	}
}

func TestRunnerResumesExplicitSessionID(t *testing.T) {
	root := t.TempDir()
	runner, store := newAgentTestRunnerWithEphemeral(t, root, false)
	defer func() { _ = store.Close() }()
	first, err := runner.Run(context.Background(), Task{Command: "run", Request: "first request", Workspace: root, Model: "test-model", NewSession: true})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, err := runner.Run(context.Background(), Task{Command: "run", Request: "resume request", Workspace: root, Model: "test-model", SessionID: string(first.SessionID)})
	if err != nil || second.SessionID != first.SessionID {
		t.Fatalf("explicit resume failed: first=%q second=%q error=%v", first.SessionID, second.SessionID, err)
	}
}

func TestResumeHistoryDropsOrphanedAndIncompleteToolItems(t *testing.T) {
	request := model.Request{Input: []model.InputItem{
		{Type: "function_call_output", CallID: "orphan", Output: "{}"},
		{Type: "function_call", CallID: "complete", Name: "fs.read", Arguments: "{}"},
		{Type: "function_call_output", CallID: "complete", Output: "{}"},
		{Type: "function_call", CallID: "pending", Name: "fs.read", Arguments: "{}"},
	}}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal resumed request: %v", err)
	}
	history := resumeHistory(&session.Record{Events: []session.Event{{Type: "request", Data: data}}})
	if len(history) != 2 || history[0].CallID != "complete" || history[1].CallID != "complete" {
		t.Fatalf("unexpected sanitized history: %+v", history)
	}
}

func TestCompactResumeHistoryKeepsRecentWindowAndExactFacts(t *testing.T) {
	compacted := compactResumeHistory(resumeMemoryFixture())
	assertResumeMemoryFacts(t, compacted[0].Content)
	assertRecentResumeWindow(t, compacted)
	assertRecentResumeToolPair(t, compacted)
}

func resumeMemoryFixture() []model.InputItem {
	history := make([]model.InputItem, 0, 35)
	for index := range 20 {
		history = append(history, model.InputItem{Type: "message", Role: "assistant", Content: fmt.Sprintf("older assistant turn %d", index)})
	}
	history = append(history, model.InputItem{Type: "function_call", CallID: "call-1", Name: "fs.read", Arguments: `{"path":"internal/app/app.go"}`}, model.InputItem{Type: "function_call_output", CallID: "call-1", Output: `{"status":"succeeded","data":{"changed_paths":["README.md"],"passed":true,"exit_code":0}}`})
	for index := range 10 {
		history = append(history, model.InputItem{Type: "message", Role: "assistant", Content: fmt.Sprintf("recent assistant turn %d", index)})
	}
	return append(history, model.InputItem{Type: "function_call", CallID: "recent-call", Name: "fs.read", Arguments: `{"path":"README.md"}`}, model.InputItem{Type: "function_call_output", CallID: "recent-call", Output: `{"status":"succeeded"}`}, model.InputItem{Type: "message", Role: "assistant", Content: "most recent assistant response"})
}

func assertResumeMemoryFacts(t *testing.T, memory string) {
	t.Helper()
	if !strings.Contains(memory, `"path":"internal/app/app.go"`) || !strings.Contains(memory, `changed_paths = ["README.md"]`) || !strings.Contains(memory, "passed = true") {
		t.Fatalf("exact tool facts were not preserved: %s", memory)
	}
}

func assertRecentResumeWindow(t *testing.T, compacted []model.InputItem) {
	t.Helper()
	if len(compacted) != recentResumeHistoryItems+1 || !strings.Contains(compacted[0].Content, "Earlier context, chronological segments") {
		t.Fatalf("expected one memory item and a recent exact window, got %d items: %+v", len(compacted), compacted)
	}
	if compacted[1].Type != "message" || compacted[len(compacted)-1].Content != "most recent assistant response" {
		t.Fatalf("recent history was not preserved exactly: %+v", compacted)
	}
}

func assertRecentResumeToolPair(t *testing.T, compacted []model.InputItem) {
	t.Helper()
	callIndex := len(compacted) - 3
	if compacted[callIndex].Type != "function_call" || compacted[callIndex+1].Type != "function_call_output" || compacted[callIndex].CallID != compacted[callIndex+1].CallID {
		t.Fatalf("recent function pair was broken: %+v", compacted[1:])
	}
}

func TestResumeMemoryCarriesExactFactsAcrossCompaction(t *testing.T) {
	initial := []model.InputItem{
		{Type: "function_call", CallID: "call-1", Name: "process.run", Arguments: `{"task":"test"}`},
		{Type: "function_call_output", CallID: "call-1", Output: `{"status":"succeeded","data":{"task":"test","passed":true,"exit_code":0}}`},
	}
	firstMemory := buildResumeMemory(initial)
	secondMemory := buildResumeMemory([]model.InputItem{{Type: "message", Role: "user", Content: firstMemory}})
	if !strings.Contains(secondMemory, `tool process.run arguments = {"task":"test"}`) || !strings.Contains(secondMemory, `passed = true`) {
		t.Fatalf("exact facts were lost during rolling compaction: %s", secondMemory)
	}
}

func TestResumeMemoryHierarchicallyBoundsLongHistory(t *testing.T) {
	history := make([]model.InputItem, 0, 60)
	for index := range 60 {
		history = append(history, model.InputItem{Type: "message", Role: "assistant", Content: fmt.Sprintf("turn %d %s", index, strings.Repeat("detail ", 80))})
	}
	memory := buildResumeMemory(history)
	if !strings.Contains(memory, "period 1:") || len(memory) > 4200 {
		t.Fatalf("long history was not hierarchically bounded: bytes=%d memory=%s", len(memory), memory)
	}
}

type testTool struct{}

func (testTool) Definition() tools.Definition {
	return tools.Definition{Name: "fs.read", Risk: tools.RiskReadOnly}
}

func (testTool) Execute(context.Context, tools.Call) tools.Result {
	return tools.Result{Status: tools.StatusSucceeded, Data: map[string]string{"content": "hello"}}
}

func TestRunnerCompletesFunctionCallLoopAndPersistsSession(t *testing.T) {
	root := t.TempDir()
	runner, store := newAgentTestRunner(t, root)
	defer func() { _ = store.Close() }()
	outcome, err := runner.Run(context.Background(), Task{Command: "run", Request: "read README", Workspace: root, Model: "test-model"})
	if err != nil || outcome.Text != "finished" || outcome.SessionID == "" {
		t.Fatalf("unexpected outcome: %+v, error=%v", outcome, err)
	}
	assertStepMetrics(t, outcome.Steps)
	if outcome.ContextTokensTotal == 0 {
		t.Fatalf("expected accumulated context tokens: %+v", outcome)
	}
	record, err := store.Load(context.Background(), outcome.SessionID)
	if err != nil || len(record.Events) < 3 || record.Result == nil {
		t.Fatalf("unexpected session: %+v, error=%v", record, err)
	}
	if len(record.Result.Steps) != len(outcome.Steps) || record.Result.ContextTokensTotal != outcome.ContextTokensTotal {
		t.Fatalf("step metrics were not persisted: %+v", record.Result)
	}
	assertStepMetricEvents(t, record.Events, len(outcome.Steps))
}

func assertStepMetricEvents(t *testing.T, events []session.Event, expected int) {
	t.Helper()
	count := 0
	for _, event := range events {
		if event.Type == "step_metrics" {
			count++
		}
	}
	if count != expected {
		t.Fatalf("expected %d persisted step-metrics events, got %d", expected, count)
	}
}

func assertStepMetrics(t *testing.T, steps []usage.StepMetrics) {
	t.Helper()
	if len(steps) != 2 {
		t.Fatalf("expected one metric per model round: %+v", steps)
	}
	for index, step := range steps {
		assertRequestStepMetrics(t, step, index+1)
	}
	assertNonStreamingProviderMetrics(t, steps[1])
}

func assertRequestStepMetrics(t *testing.T, step usage.StepMetrics, round int) {
	t.Helper()
	if step.Round != round || step.ContextTokens <= 0 || step.RequestDurationMs < 0 || step.RequestWallClockTokensPerSecond <= 0 {
		t.Fatalf("invalid step metrics: %+v", step)
	}
}

func assertNonStreamingProviderMetrics(t *testing.T, step usage.StepMetrics) {
	t.Helper()
	if step.ProviderInputTokens == nil || step.OutputSource != usage.SourceProvider || step.OutputTokens != 2 || step.OutputTokensPerSecond != nil {
		t.Fatalf("provider usage provenance was not retained: %+v", step)
	}
}

func TestStreamingRoundMeasuresGenerationRateSeparately(t *testing.T) {
	root := t.TempDir()
	runner, store := newAgentTestRunnerWithEphemeral(t, root, true)
	defer func() { _ = store.Close() }()
	runner.Streaming = true
	runner.Client = &streamingTestClient{}
	outcome, err := runner.Run(context.Background(), Task{Command: "run", Request: "stream", Workspace: root, Model: "test-model"})
	if err != nil {
		t.Fatalf("run streaming metrics task: %v", err)
	}
	if len(outcome.Steps) != 1 || outcome.Steps[0].OutputTokensPerSecond == nil || outcome.Steps[0].GenerationDurationMs < 0 || outcome.Steps[0].RequestWallClockTokensPerSecond <= 0 {
		t.Fatalf("stream generation and request rates were not separated: %+v", outcome.Steps)
	}
}

type changedPathTool struct{}

func (changedPathTool) Definition() tools.Definition {
	return tools.Definition{Name: "fs.write", Risk: tools.RiskWrite}
}

func (changedPathTool) Execute(context.Context, tools.Call) tools.Result {
	return tools.Result{Status: tools.StatusSucceeded, ChangedPaths: []string{"generated.txt"}, ChangeSet: &tools.ChangeSet{ID: "change-1", Operation: "fs.write", State: "applied", Paths: []string{"generated.txt"}}}
}

func TestRunnerAggregatesChangedPathsIntoOutcome(t *testing.T) {
	root := t.TempDir()
	runner, store := newAgentRunnerWithTool(t, root, changedPathTool{}, []model.Response{
		{ID: "call", Status: "in_progress", ToolCalls: []model.ToolCall{{CallID: "1", Name: "fs.write", Arguments: `{}`}}},
		{ID: "done", Status: "completed", Text: "finished"},
	})
	defer func() { _ = store.Close() }()
	outcome, err := runner.Run(context.Background(), Task{Command: "run", Request: "make a file", Workspace: root, Model: "test-model", WorkspaceAutomation: true, NonInteractive: true})
	if err != nil {
		t.Fatalf("run changed-path task: %v", err)
	}
	assertChangedPathOutcome(t, outcome)
	record, err := store.Load(context.Background(), outcome.SessionID)
	assertPersistedChangeSet(t, record, err)
}

type validationTool struct{}

func (validationTool) Definition() tools.Definition {
	return tools.Definition{Name: "process.run", Risk: tools.RiskProcess}
}

func (validationTool) Execute(context.Context, tools.Call) tools.Result {
	return tools.Result{Status: tools.StatusSucceeded, Data: process.Result{Task: "test", Kind: "test", WorkingDirectory: ".", Passed: true, ExitCode: 0}}
}

func TestRunnerPersistsValidationResults(t *testing.T) {
	root := t.TempDir()
	runner, store := newAgentRunnerWithTool(t, root, validationTool{}, []model.Response{
		{ID: "validation", Status: "in_progress", ToolCalls: []model.ToolCall{{CallID: "1", Name: "process.run", Arguments: `{}`}}},
		{ID: "done", Status: "completed", Text: "validated"},
	})
	defer func() { _ = store.Close() }()
	outcome, err := runner.Run(context.Background(), Task{Command: "run", Request: "validate", Workspace: root, Model: "test-model", WorkspaceAutomation: true, NonInteractive: true})
	if err != nil {
		t.Fatalf("run validation task: %v", err)
	}
	if len(outcome.Validations) != 1 || !outcome.Validations[0].Passed {
		t.Fatalf("unexpected validation outcome: %+v, error=%v", outcome, err)
	}
	record, err := store.Load(context.Background(), outcome.SessionID)
	if err != nil {
		t.Fatalf("load validation session: %v", err)
	}
	if record.Result == nil || len(record.Result.Validations) != 1 || record.Result.Validations[0].Task != "test" {
		t.Fatalf("validation was not persisted: %+v", record.Result)
	}
}

func newAgentRunnerWithTool(t *testing.T, root string, tool tools.Tool, responses []model.Response) (Runner, *session.FileStore) {
	t.Helper()
	filesystem, err := workspacefs.New(root)
	if err != nil {
		t.Fatalf("new filesystem: %v", err)
	}
	registry := tools.NewRegistry()
	if err := registry.Register(tool); err != nil {
		t.Fatalf("register test tool: %v", err)
	}
	store, err := session.NewFileStore(session.Options{InvocationPath: root, Ephemeral: true})
	if err != nil {
		t.Fatalf("new session store: %v", err)
	}
	client := &sequenceClient{responses: responses}
	return Runner{Client: client, Context: contextdata.New(filesystem, usage.ByteEstimator{}), Tools: registry, Policy: policy.DefaultPolicy{}, Sessions: store}, store
}

func assertChangedPathOutcome(t *testing.T, outcome Outcome) {
	t.Helper()
	if len(outcome.ChangedPaths) != 1 || outcome.ChangedPaths[0] != "generated.txt" {
		t.Fatalf("unexpected changed paths: %+v", outcome.ChangedPaths)
	}
	if len(outcome.ChangeSets) != 1 || outcome.ChangeSets[0].ID != "change-1" || outcome.ChangeSets[0].Approval != "workspace-automation" {
		t.Fatalf("unexpected change sets: %+v", outcome.ChangeSets)
	}
}

func assertPersistedChangeSet(t *testing.T, record session.Record, err error) {
	t.Helper()
	if err != nil || record.Result == nil || len(record.Result.ChangeSets) != 1 || record.Result.ChangeSets[0].ID != "change-1" {
		t.Fatalf("change set was not persisted: %+v, error=%v", record.Result, err)
	}
}

func newAgentTestRunner(t *testing.T, root string) (Runner, *session.FileStore) {
	return newAgentTestRunnerWithEphemeral(t, root, true)
}

func newAgentTestRunnerWithEphemeral(t *testing.T, root string, ephemeral bool) (Runner, *session.FileStore) {
	t.Helper()
	if err := os.WriteFile(root+"/README.md", []byte("hello"), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	filesystem, err := workspacefs.New(root)
	if err != nil {
		t.Fatalf("new filesystem: %v", err)
	}
	registry := tools.NewRegistry()
	if err := registry.Register(testTool{}); err != nil {
		t.Fatalf("register tool: %v", err)
	}
	store, err := session.NewFileStore(session.Options{InvocationPath: root, Ephemeral: ephemeral})
	if err != nil {
		t.Fatalf("new session store: %v", err)
	}
	client := &sequenceClient{responses: []model.Response{{ID: "call", Status: "in_progress", ToolCalls: []model.ToolCall{{CallID: "1", Name: "fs.read", Arguments: `{"path":"README.md"}`}}}, {ID: "done", Status: "completed", Text: "finished", Usage: usage.FromProvider(pointer(4), pointer(2), pointer(6))}}}
	return Runner{Client: client, Context: contextdata.New(filesystem, usage.ByteEstimator{}), Tools: registry, Policy: policy.DefaultPolicy{}, Sessions: store}, store
}

func requestContains(request model.Request, values ...string) bool {
	for _, value := range values {
		found := false
		for _, input := range request.Input {
			if strings.Contains(input.Content, value) || strings.Contains(input.Output, value) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func pointer(value int64) *int64 {
	return &value
}
