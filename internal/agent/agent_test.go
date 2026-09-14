package agent

import (
	"context"
	"encoding/json"
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

func (client *sequenceClient) Create(_ context.Context, request model.Request) (model.Response, error) {
	client.requests = append(client.requests, request)
	response := client.responses[client.index]
	if client.index < len(client.responses)-1 {
		client.index++
	}
	return response, nil
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
	record, err := store.Load(context.Background(), outcome.SessionID)
	if err != nil || len(record.Events) < 3 || record.Result == nil {
		t.Fatalf("unexpected session: %+v, error=%v", record, err)
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
