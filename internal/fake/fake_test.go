package fake

import (
	"context"
	"testing"

	"github.com/diakovliev/doit/internal/model"
	"github.com/diakovliev/doit/internal/process"
)

func TestModelClientRecordsValidRequests(t *testing.T) {
	client := NewModelClient(model.Response{ID: "response-1", Status: "completed"}, nil)
	request := model.Request{Model: "fake-model"}
	response, err := client.Create(context.Background(), request)
	if err != nil {
		t.Fatalf("create response: %v", err)
	}
	if response.ID != "response-1" {
		t.Fatalf("unexpected response: %+v", response)
	}
	if len(client.RecordedRequests()) != 1 {
		t.Fatal("expected one recorded request")
	}
}

func TestProcessRunnerRecordsTasks(t *testing.T) {
	runner := NewProcessRunner(process.Result{ExitCode: 0, Stdout: "ok"}, nil)
	result, err := runner.Run(context.Background(), process.Task{Name: "test"})
	if err != nil {
		t.Fatalf("run task: %v", err)
	}
	if result.ExitCode != 0 || len(runner.RecordedTasks()) != 1 {
		t.Fatalf("unexpected process result or task count: %+v", result)
	}
}
