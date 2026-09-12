// Package agent orchestrates model requests, tool calls, approvals, and sessions.
package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/diakovliev/doit/internal/apperr"
	contextdata "github.com/diakovliev/doit/internal/contextbuilder"
	"github.com/diakovliev/doit/internal/model"
	"github.com/diakovliev/doit/internal/policy"
	"github.com/diakovliev/doit/internal/session"
	"github.com/diakovliev/doit/internal/tools"
	"github.com/diakovliev/doit/internal/usage"
)

// defaultMaxRounds allows repository inspections to gather context across
// several bounded tool calls without permitting an unbounded agent loop.
const defaultMaxRounds = 32

// ApprovalFunc handles confirmation-required tool actions.
type ApprovalFunc func(context.Context, policy.Action, tools.Call) (bool, error)

// ProgressEvent describes safe, human-readable runtime activity.
type ProgressEvent struct {
	Phase   string
	Message string
	Round   int
	Tool    string
}

// ProgressFunc receives runtime activity without prompt or credential content.
type ProgressFunc func(ProgressEvent)

// Task describes one model-assisted request.
type Task struct {
	Command         string
	Request         string
	Instructions    string
	Paths           []string
	Workspace       string
	Profile         string
	Model           string
	MaxInputTokens  int
	MaxOutputTokens int
	NonInteractive  bool
}

// Outcome is the final normalized task result.
type Outcome struct {
	SessionID    session.ID
	Text         string
	Usage        usage.Counts
	ChangedPaths []string
}

// Runner coordinates one task.
type Runner struct {
	Client    model.ModelClient
	Context   *contextdata.Builder
	Tools     *tools.Registry
	Policy    policy.ApprovalPolicy
	Sessions  session.Store
	Approve   ApprovalFunc
	MaxRounds int
	Progress  ProgressFunc
}

// Run executes a model/tool loop and persists its lifecycle events.
func (runner *Runner) Run(ctx context.Context, task Task) (Outcome, error) {
	if err := runner.validate(); err != nil {
		return Outcome{}, err
	}
	metadata := session.Metadata{InvocationPath: task.Workspace, Command: task.Command, Profile: task.Profile, Model: task.Model}
	sessionID, err := runner.Sessions.Start(ctx, metadata)
	if err != nil {
		return Outcome{}, err
	}
	runner.emit(ProgressEvent{Phase: "session", Message: "session started"})
	request, initialUsage, err := runner.buildRequest(ctx, task)
	if err != nil {
		return Outcome{SessionID: sessionID}, runner.failSession(ctx, sessionID, err)
	}
	if err := runner.appendEvent(ctx, sessionID, "request", request); err != nil {
		return Outcome{SessionID: sessionID}, err
	}
	runner.emit(ProgressEvent{Phase: "context", Message: "bounded context prepared"})
	return runner.loop(ctx, sessionID, request, initialUsage, task.NonInteractive)
}

func (runner *Runner) loop(ctx context.Context, sessionID session.ID, request model.Request, initialUsage usage.Counts, nonInteractive bool) (Outcome, error) {
	totalUsage := initialUsage
	maxRounds := runner.MaxRounds
	if maxRounds <= 0 {
		maxRounds = defaultMaxRounds
	}
	for round := range maxRounds {
		runner.emit(ProgressEvent{Phase: "model", Message: fmt.Sprintf("requesting model response (round %d)", round+1), Round: round + 1})
		response, createErr := runner.createResponse(ctx, request)
		if createErr != nil {
			return Outcome{SessionID: sessionID, Usage: totalUsage}, runner.failSession(ctx, sessionID, createErr)
		}
		outcome, done, err := runner.processResponse(ctx, sessionID, response, &totalUsage, round == 0, initialUsage)
		if err != nil {
			return Outcome{SessionID: sessionID, Usage: totalUsage}, err
		}
		if done {
			runner.emit(ProgressEvent{Phase: "session", Message: "task completed"})
			return outcome, nil
		}
		if err := runner.handleToolCalls(ctx, sessionID, &request, response.ToolCalls, nonInteractive); err != nil {
			return Outcome{SessionID: sessionID, Usage: totalUsage}, runner.failSession(ctx, sessionID, err)
		}
	}
	err := apperr.New(apperr.KindTool, "agent.run", "maximum model/tool rounds exceeded")
	return Outcome{SessionID: sessionID, Usage: totalUsage}, runner.failSession(ctx, sessionID, err)
}

func (runner *Runner) processResponse(ctx context.Context, sessionID session.ID, response model.Response, totalUsage *usage.Counts, first bool, initialUsage usage.Counts) (Outcome, bool, error) {
	*totalUsage = addResponseUsage(*totalUsage, response.Usage, first, initialUsage)
	runner.emit(ProgressEvent{Phase: "model", Message: fmt.Sprintf("model response received (%d tool call(s))", len(response.ToolCalls))})
	if err := runner.appendEvent(ctx, sessionID, "model_message", response); err != nil {
		return Outcome{SessionID: sessionID, Usage: *totalUsage}, false, err
	}
	if response.Status == "failed" || response.Status == "incomplete" || response.Status == "cancelled" {
		err := apperr.New(apperr.KindBackend, "agent.response", "model response status: "+response.Status)
		return Outcome{SessionID: sessionID, Usage: *totalUsage}, false, runner.failSession(ctx, sessionID, err)
	}
	if len(response.ToolCalls) > 0 {
		return Outcome{SessionID: sessionID, Usage: *totalUsage}, false, nil
	}
	outcome := Outcome{SessionID: sessionID, Text: response.Text, Usage: *totalUsage}
	if err := runner.completeSession(ctx, sessionID, outcome); err != nil {
		return outcome, false, err
	}
	return outcome, true, nil
}

func (runner *Runner) createResponse(ctx context.Context, request model.Request) (model.Response, error) {
	for attempt := range 3 {
		response, err := runner.Client.Create(ctx, request)
		if err == nil {
			return response, nil
		}
		if apperr.KindOf(err) != apperr.KindBackend || attempt == 2 {
			return model.Response{}, err
		}
	}
	return model.Response{}, apperr.New(apperr.KindBackend, "agent.response", "model request retries exhausted")
}

func (runner *Runner) validate() error {
	if runner.Client == nil || runner.Context == nil || runner.Tools == nil || runner.Policy == nil || runner.Sessions == nil {
		return apperr.New(apperr.KindInternal, "agent.validate", "agent dependencies are incomplete")
	}
	return nil
}

func (runner *Runner) buildRequest(ctx context.Context, task Task) (model.Request, usage.Counts, error) {
	definitions := runner.Tools.Definitions()
	modelTools := make([]model.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		modelTools = append(modelTools, model.ToolDefinition{Type: "function", Name: definition.Name, Description: definition.Description, Parameters: definition.Parameters})
	}
	return runner.Context.Build(ctx, contextdata.Request{Model: task.Model, UserInput: task.Request, Instructions: task.Instructions, Paths: task.Paths, Tools: modelTools, MaxInputTokens: task.MaxInputTokens, MaxOutputTokens: task.MaxOutputTokens})
}

func (runner *Runner) handleToolCalls(ctx context.Context, sessionID session.ID, request *model.Request, calls []model.ToolCall, nonInteractive bool) error {
	for _, call := range calls {
		tool, exists := runner.Tools.Lookup(call.Name)
		if !exists {
			return apperr.New(apperr.KindTool, "agent.tool", "model requested unknown tool: "+call.Name)
		}
		runner.emit(ProgressEvent{Phase: "tool", Message: "tool requested: " + call.Name, Tool: call.Name})
		definition := tool.Definition()
		action := policy.Action{Name: definition.Name, Risk: definition.Risk, NonInteractive: nonInteractive}
		decision := runner.Policy.Decide(ctx, action)
		if decision == policy.DecisionConfirm {
			if runner.Approve == nil {
				decision = policy.DecisionDeny
			} else {
				approved, err := runner.Approve(ctx, action, tools.Call{Name: call.Name, Arguments: json.RawMessage(call.Arguments)})
				if err != nil {
					return err
				}
				if approved {
					decision = policy.DecisionAllow
				} else {
					decision = policy.DecisionDeny
				}
			}
		}
		toolResult := tools.Result{Status: tools.StatusDenied, Diagnostics: []tools.Diagnostic{{Level: "warning", Message: "tool call denied by policy"}}}
		if decision == policy.DecisionAllow {
			toolResult = tool.Execute(ctx, tools.Call{Name: call.Name, Arguments: json.RawMessage(call.Arguments)})
		}
		if err := runner.appendEvent(ctx, sessionID, "tool_result", toolResult); err != nil {
			return err
		}
		runner.emit(ProgressEvent{Phase: "tool", Message: "tool completed: " + call.Name, Tool: call.Name})
		encodedResult, err := json.Marshal(toolResult)
		if err != nil {
			return err
		}
		request.Input = append(request.Input,
			model.InputItem{Type: "function_call", CallID: call.CallID, Name: call.Name, Arguments: call.Arguments},
			model.InputItem{Type: "function_call_output", CallID: call.CallID, Output: string(encodedResult)},
		)
	}
	return nil
}

func (runner *Runner) emit(event ProgressEvent) {
	if runner.Progress != nil {
		runner.Progress(event)
	}
}

func (runner *Runner) appendEvent(ctx context.Context, id session.ID, eventType string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return apperr.Wrap(apperr.KindTool, "agent.event", err)
	}
	return runner.Sessions.Append(ctx, id, session.Event{Type: eventType, Data: data})
}

func (runner *Runner) completeSession(ctx context.Context, id session.ID, outcome Outcome) error {
	return runner.Sessions.Complete(ctx, id, session.Result{Summary: outcome.Text, ChangedPaths: outcome.ChangedPaths, Usage: outcome.Usage})
}

func (runner *Runner) failSession(ctx context.Context, id session.ID, err error) error {
	completeErr := runner.Sessions.Complete(ctx, id, session.Result{Summary: err.Error(), Unresolved: []string{err.Error()}})
	if completeErr != nil {
		return fmt.Errorf("%w; completing session: %w", err, completeErr)
	}
	return err
}

func addResponseUsage(total, response usage.Counts, first bool, initial usage.Counts) usage.Counts {
	if first {
		if response.InputTokens == nil {
			return initial
		}
		return response
	}
	return usage.Add(total, response)
}
