package agent

import (
	"context"
	"os"
	"testing"

	contextdata "github.com/diakovliev/doit/internal/contextbuilder"
	"github.com/diakovliev/doit/internal/model"
	"github.com/diakovliev/doit/internal/policy"
	"github.com/diakovliev/doit/internal/session"
	"github.com/diakovliev/doit/internal/tools"
	"github.com/diakovliev/doit/internal/usage"
	"github.com/diakovliev/doit/internal/workspacefs"
)

type sequenceClient struct {
	responses []model.Response
	index     int
}

func (client *sequenceClient) Create(context.Context, model.Request) (model.Response, error) {
	response := client.responses[client.index]
	if client.index < len(client.responses)-1 {
		client.index++
	}
	return response, nil
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

func newAgentTestRunner(t *testing.T, root string) (Runner, *session.FileStore) {
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
	store, err := session.NewFileStore(session.Options{InvocationPath: root, Ephemeral: true})
	if err != nil {
		t.Fatalf("new session store: %v", err)
	}
	client := &sequenceClient{responses: []model.Response{{ID: "call", Status: "in_progress", ToolCalls: []model.ToolCall{{CallID: "1", Name: "fs.read", Arguments: `{"path":"README.md"}`}}}, {ID: "done", Status: "completed", Text: "finished", Usage: usage.FromProvider(pointer(4), pointer(2), pointer(6))}}}
	return Runner{Client: client, Context: contextdata.New(filesystem, usage.ByteEstimator{}), Tools: registry, Policy: policy.DefaultPolicy{}, Sessions: store}, store
}

func pointer(value int64) *int64 {
	return &value
}
