// Package agent orchestrates model requests, tool calls, approvals, and sessions.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/diakovliev/doit/internal/apperr"
	contextdata "github.com/diakovliev/doit/internal/contextbuilder"
	"github.com/diakovliev/doit/internal/model"
	"github.com/diakovliev/doit/internal/policy"
	"github.com/diakovliev/doit/internal/process"
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
	Command             string
	Request             string
	Instructions        string
	Paths               []string
	Workspace           string
	Profile             string
	Model               string
	MaxInputTokens      int
	MaxOutputTokens     int
	NonInteractive      bool
	WorkspaceAutomation bool
	NewSession          bool
	SessionID           string
}

// Outcome is the final normalized task result.
type Outcome struct {
	SessionID    session.ID
	Text         string
	Usage        usage.Counts
	ChangedPaths []string
	ChangeSets   []tools.ChangeSet
	Validations  []session.Validation
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
	metadata := session.Metadata{ID: session.ID(task.SessionID), InvocationPath: task.Workspace, Command: task.Command, Profile: task.Profile, Model: task.Model}
	sessionID, resumed, err := runner.openSession(ctx, metadata, task.NewSession)
	if err != nil {
		return Outcome{}, err
	}
	message := "session started"
	var previous *session.Record
	if resumed {
		message = "session resumed"
		record, loadErr := runner.Sessions.Load(ctx, sessionID)
		if loadErr != nil {
			return Outcome{SessionID: sessionID}, runner.failSession(ctx, sessionID, loadErr)
		}
		previous = &record
	}
	runner.emit(ProgressEvent{Phase: "session", Message: message})
	request, initialUsage, err := runner.buildRequest(ctx, task, previous)
	if err != nil {
		return Outcome{SessionID: sessionID}, runner.failSession(ctx, sessionID, err)
	}
	if err := runner.appendEvent(ctx, sessionID, "request", request); err != nil {
		return Outcome{SessionID: sessionID}, err
	}
	runner.emit(ProgressEvent{Phase: "context", Message: "bounded context prepared"})
	return runner.loop(ctx, sessionID, request, initialUsage, task.NonInteractive, task.WorkspaceAutomation)
}

func (runner *Runner) openSession(ctx context.Context, metadata session.Metadata, newSession bool) (session.ID, bool, error) {
	if metadata.ID != "" {
		resumedID, err := runner.Sessions.Resume(ctx, metadata.ID, metadata)
		return resumedID, true, err
	}
	if !newSession {
		latestID, found, err := runner.Sessions.Latest(ctx)
		if err != nil {
			return "", false, err
		}
		if found {
			resumedID, resumeErr := runner.Sessions.Resume(ctx, latestID, metadata)
			return resumedID, true, resumeErr
		}
	}
	startedID, err := runner.Sessions.Start(ctx, metadata)
	return startedID, false, err
}

func (runner *Runner) loop(ctx context.Context, sessionID session.ID, request model.Request, initialUsage usage.Counts, nonInteractive bool, workspaceAutomation bool) (Outcome, error) {
	totalUsage := initialUsage
	changedPaths := make([]string, 0)
	changeSets := make([]tools.ChangeSet, 0)
	validations := make([]session.Validation, 0)
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
		outcome, done, err := runner.processResponse(ctx, sessionID, response, &totalUsage, changedPaths, changeSets, validations, round == 0, initialUsage)
		if err != nil {
			return Outcome{SessionID: sessionID, Usage: totalUsage}, err
		}
		if done {
			runner.emit(ProgressEvent{Phase: "session", Message: "task completed"})
			return outcome, nil
		}
		if err := runner.handleToolCalls(ctx, sessionID, &request, response.ToolCalls, &changedPaths, &changeSets, &validations, nonInteractive, workspaceAutomation); err != nil {
			return Outcome{SessionID: sessionID, Usage: totalUsage}, runner.failSession(ctx, sessionID, err)
		}
	}
	err := apperr.New(apperr.KindTool, "agent.run", "maximum model/tool rounds exceeded")
	return Outcome{SessionID: sessionID, Usage: totalUsage}, runner.failSession(ctx, sessionID, err)
}

func (runner *Runner) processResponse(ctx context.Context, sessionID session.ID, response model.Response, totalUsage *usage.Counts, changedPaths []string, changeSets []tools.ChangeSet, validations []session.Validation, first bool, initialUsage usage.Counts) (Outcome, bool, error) {
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
	outcome := Outcome{SessionID: sessionID, Text: response.Text, Usage: *totalUsage, ChangedPaths: append([]string(nil), changedPaths...), ChangeSets: append([]tools.ChangeSet(nil), changeSets...), Validations: append([]session.Validation(nil), validations...)}
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

func (runner *Runner) buildRequest(ctx context.Context, task Task, previous *session.Record) (model.Request, usage.Counts, error) {
	definitions := runner.Tools.Definitions()
	modelTools := make([]model.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		modelTools = append(modelTools, model.ToolDefinition{Type: "function", Name: definition.Name, Description: definition.Description, Parameters: definition.Parameters})
	}
	return runner.Context.Build(ctx, contextdata.Request{Model: task.Model, UserInput: task.Request, Instructions: task.Instructions, History: resumeHistory(previous), Paths: task.Paths, Tools: modelTools, MaxInputTokens: task.MaxInputTokens, MaxOutputTokens: task.MaxOutputTokens})
}

func resumeHistory(record *session.Record) []model.InputItem {
	if record == nil {
		return nil
	}
	history, requestIndex := recordedRequest(record.Events)
	if requestIndex < 0 {
		return previousSummary(record)
	}
	pending := make([]pendingCall, 0)
	for _, event := range record.Events[requestIndex+1:] {
		history, pending = replayEvent(history, pending, event)
	}
	return sanitizeHistory(removePendingCalls(history, pending))
}

func previousSummary(record *session.Record) []model.InputItem {
	if record.Result == nil || strings.TrimSpace(record.Result.Summary) == "" {
		return nil
	}
	return []model.InputItem{{Type: "message", Role: "user", Content: "Previous session summary:\n" + record.Result.Summary}}
}

func replayEvent(history []model.InputItem, pending []pendingCall, event session.Event) ([]model.InputItem, []pendingCall) {
	if event.Type == "model_message" {
		return replayModelMessage(history, pending, event.Data)
	}
	if event.Type == "tool_result" {
		return replayToolResult(history, pending, event.Data)
	}
	return history, pending
}

func replayModelMessage(history []model.InputItem, pending []pendingCall, data json.RawMessage) ([]model.InputItem, []pendingCall) {
	var response model.Response
	if json.Unmarshal(data, &response) != nil {
		return history, pending
	}
	if response.Text != "" {
		history = append(history, model.InputItem{Type: "message", Role: "assistant", Content: response.Text})
	}
	for _, call := range response.ToolCalls {
		pending = append(pending, pendingCall{index: len(history), callID: call.CallID})
		history = append(history, model.InputItem{Type: "function_call", CallID: call.CallID, Name: call.Name, Arguments: call.Arguments})
	}
	return history, pending
}

func replayToolResult(history []model.InputItem, pending []pendingCall, data json.RawMessage) ([]model.InputItem, []pendingCall) {
	if len(pending) == 0 {
		return history, pending
	}
	call := pending[0]
	return append(history, model.InputItem{Type: "function_call_output", CallID: call.callID, Output: string(data)}), pending[1:]
}

type pendingCall struct {
	index  int
	callID string
}

func recordedRequest(events []session.Event) ([]model.InputItem, int) {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Type != "request" {
			continue
		}
		var request model.Request
		if json.Unmarshal(events[index].Data, &request) == nil && len(request.Input) > 0 {
			return append([]model.InputItem(nil), request.Input...), index
		}
	}
	return nil, -1
}

func removePendingCalls(history []model.InputItem, pending []pendingCall) []model.InputItem {
	if len(pending) == 0 {
		return history
	}
	indices := make(map[int]struct{}, len(pending))
	for _, call := range pending {
		indices[call.index] = struct{}{}
	}
	filtered := make([]model.InputItem, 0, len(history)-len(pending))
	for index, item := range history {
		if _, exists := indices[index]; !exists {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func sanitizeHistory(history []model.InputItem) []model.InputItem {
	activeCalls := make(map[string]int)
	matchedCalls := make(map[string]bool)
	filtered := make([]model.InputItem, 0, len(history))
	for _, item := range history {
		switch item.Type {
		case "function_call":
			if item.CallID == "" {
				continue
			}
			activeCalls[item.CallID] = len(filtered)
			filtered = append(filtered, item)
		case "function_call_output":
			if _, exists := activeCalls[item.CallID]; !exists {
				continue
			}
			matchedCalls[item.CallID] = true
			delete(activeCalls, item.CallID)
			filtered = append(filtered, item)
		default:
			filtered = append(filtered, item)
		}
	}
	if len(activeCalls) == 0 {
		return filtered
	}
	result := make([]model.InputItem, 0, len(filtered)-len(activeCalls))
	for _, item := range filtered {
		if item.Type == "function_call" && !matchedCalls[item.CallID] {
			continue
		}
		result = append(result, item)
	}
	return result
}

func (runner *Runner) handleToolCalls(ctx context.Context, sessionID session.ID, request *model.Request, calls []model.ToolCall, changedPaths *[]string, changeSets *[]tools.ChangeSet, validations *[]session.Validation, nonInteractive bool, workspaceAutomation bool) error {
	for _, call := range calls {
		if err := runner.handleToolCall(ctx, sessionID, request, call, changedPaths, changeSets, validations, nonInteractive, workspaceAutomation); err != nil {
			return err
		}
	}
	return nil
}

func (runner *Runner) handleToolCall(ctx context.Context, sessionID session.ID, request *model.Request, call model.ToolCall, changedPaths *[]string, changeSets *[]tools.ChangeSet, validations *[]session.Validation, nonInteractive bool, workspaceAutomation bool) error {
	tool, exists := runner.Tools.Lookup(call.Name)
	if !exists {
		return apperr.New(apperr.KindTool, "agent.tool", "model requested unknown tool: "+call.Name)
	}
	runner.emit(ProgressEvent{Phase: "tool", Message: "tool requested: " + call.Name, Tool: call.Name})
	decision, err := runner.toolDecision(ctx, tool.Definition(), call, nonInteractive, workspaceAutomation)
	if err != nil {
		return err
	}
	toolResult := tools.Result{Status: tools.StatusDenied, Diagnostics: []tools.Diagnostic{{Level: "warning", Message: "tool call denied by policy"}}}
	if decision == policy.DecisionAllow {
		toolResult = tool.Execute(ctx, tools.Call{Name: call.Name, Arguments: json.RawMessage(call.Arguments)})
		if toolResult.ChangeSet != nil {
			toolResult.ChangeSet.Approval = approvalIdentity(workspaceAutomation, nonInteractive)
		}
		*changedPaths = appendUniquePaths(*changedPaths, toolResult.ChangedPaths)
		if toolResult.ChangeSet != nil {
			*changeSets = appendUniqueChangeSets(*changeSets, *toolResult.ChangeSet)
		}
		if call.Name == "process.run" {
			if validation, ok := validationFromResult(toolResult.Data); ok {
				*validations = append(*validations, validation)
			}
		}
	}
	if err := runner.appendEvent(ctx, sessionID, "tool_result", toolResult); err != nil {
		return err
	}
	runner.emit(ProgressEvent{Phase: "tool", Message: "tool " + string(toolResult.Status) + ": " + call.Name, Tool: call.Name})
	encodedResult, err := json.Marshal(toolResult)
	if err != nil {
		return err
	}
	request.Input = append(request.Input,
		model.InputItem{Type: "function_call", CallID: call.CallID, Name: call.Name, Arguments: call.Arguments},
		model.InputItem{Type: "function_call_output", CallID: call.CallID, Output: string(encodedResult)},
	)
	return nil
}

func approvalIdentity(workspaceAutomation, nonInteractive bool) string {
	if workspaceAutomation {
		return "workspace-automation"
	}
	if !nonInteractive {
		return "interactive-approval"
	}
	return "policy-allow"
}

func (runner *Runner) toolDecision(ctx context.Context, definition tools.Definition, call model.ToolCall, nonInteractive bool, workspaceAutomation bool) (policy.Decision, error) {
	action := policy.Action{Name: definition.Name, Risk: definition.Risk, NonInteractive: nonInteractive, WorkspaceAutomation: workspaceAutomation}
	decision := runner.Policy.Decide(ctx, action)
	if decision != policy.DecisionConfirm || runner.Approve == nil {
		if decision == policy.DecisionConfirm {
			return policy.DecisionDeny, nil
		}
		return decision, nil
	}
	approved, err := runner.Approve(ctx, action, tools.Call{Name: call.Name, Arguments: json.RawMessage(call.Arguments)})
	if err != nil {
		return policy.DecisionDeny, err
	}
	if approved {
		return policy.DecisionAllow, nil
	}
	return policy.DecisionDeny, nil
}

func appendUniquePaths(existing, additions []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(additions))
	for _, path := range existing {
		seen[path] = struct{}{}
	}
	for _, path := range additions {
		if path == "" {
			continue
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		existing = append(existing, path)
	}
	return existing
}

func appendUniqueChangeSets(existing []tools.ChangeSet, additions ...tools.ChangeSet) []tools.ChangeSet {
	seen := make(map[string]struct{}, len(existing)+len(additions))
	for _, changeSet := range existing {
		seen[changeSet.ID] = struct{}{}
	}
	for _, changeSet := range additions {
		if changeSet.ID == "" {
			continue
		}
		if _, exists := seen[changeSet.ID]; exists {
			continue
		}
		seen[changeSet.ID] = struct{}{}
		existing = append(existing, changeSet)
	}
	return existing
}

func validationFromResult(data any) (session.Validation, bool) {
	result, ok := data.(process.Result)
	if !ok {
		return session.Validation{}, false
	}
	diagnostics, err := json.Marshal(result.Diagnostics)
	if err != nil {
		diagnostics = nil
	}
	return session.Validation{Task: result.Task, Kind: result.Kind, WorkingDirectory: result.WorkingDirectory, Passed: result.Passed, ExitCode: result.ExitCode, Duration: result.Duration, TimedOut: result.TimedOut, Truncated: result.Truncated, Diagnostics: string(diagnostics)}, true
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
	return runner.Sessions.Complete(ctx, id, session.Result{Summary: outcome.Text, ChangedPaths: outcome.ChangedPaths, ChangeSets: outcome.ChangeSets, Validations: outcome.Validations, Usage: outcome.Usage})
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
