// Package fake provides deterministic model and process clients for tests.
package fake

import (
	"context"
	"sync"

	"github.com/diakovliev/doit/internal/model"
	"github.com/diakovliev/doit/internal/process"
)

// ModelClient records requests and returns a configured response or error.
type ModelClient struct {
	mu       sync.Mutex
	Response model.Response
	Err      error
	Requests []model.Request
}

// NewModelClient creates a deterministic fake model client.
func NewModelClient(response model.Response, clientErr error) *ModelClient {
	return &ModelClient{Response: response, Err: clientErr}
}

// Create records a request and returns the configured result.
func (client *ModelClient) Create(ctx context.Context, request model.Request) (model.Response, error) {
	if err := ctx.Err(); err != nil {
		return model.Response{}, err
	}
	if err := request.Validate(); err != nil {
		return model.Response{}, err
	}
	client.mu.Lock()
	client.Requests = append(client.Requests, request)
	client.mu.Unlock()
	if client.Err != nil {
		return model.Response{}, client.Err
	}
	return client.Response, nil
}

// RecordedRequests returns a snapshot of received requests.
func (client *ModelClient) RecordedRequests() []model.Request {
	client.mu.Lock()
	defer client.mu.Unlock()
	requests := make([]model.Request, len(client.Requests))
	copy(requests, client.Requests)
	return requests
}

// ProcessRunner records tasks and returns a configured result or error.
type ProcessRunner struct {
	mu     sync.Mutex
	Result process.Result
	Err    error
	Tasks  []process.Task
}

// NewProcessRunner creates a deterministic fake process runner.
func NewProcessRunner(result process.Result, runnerErr error) *ProcessRunner {
	return &ProcessRunner{Result: result, Err: runnerErr}
}

// Run records a task and returns the configured result.
func (runner *ProcessRunner) Run(ctx context.Context, task process.Task) (process.Result, error) {
	if err := ctx.Err(); err != nil {
		return process.Result{}, err
	}
	runner.mu.Lock()
	runner.Tasks = append(runner.Tasks, task)
	runner.mu.Unlock()
	if runner.Err != nil {
		return process.Result{}, runner.Err
	}
	return runner.Result, nil
}

// RecordedTasks returns a snapshot of received process tasks.
func (runner *ProcessRunner) RecordedTasks() []process.Task {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	tasks := make([]process.Task, len(runner.Tasks))
	copy(tasks, runner.Tasks)
	return tasks
}
