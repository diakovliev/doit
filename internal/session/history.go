package session

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/diakovliev/doit/internal/apperr"
	"github.com/diakovliev/doit/internal/tools"
)

const (
	defaultHistoryEvents = 20
	maxHistoryEvents     = 100
	defaultHistoryBytes  = 32 * 1024
	maxHistoryBytes      = 64 * 1024
	maxHistoryQueryBytes = 2048
)

// History returns a bounded, redacted view of one session's public events.
func (store *FileStore) History(ctx context.Context, id ID, query HistoryQuery) (HistoryResult, error) {
	if err := ctx.Err(); err != nil {
		return HistoryResult{}, err
	}
	record, err := store.Load(ctx, id)
	if err != nil {
		return HistoryResult{}, err
	}
	return queryHistory(record.Events, query)
}

func queryHistory(events []Event, query HistoryQuery) (HistoryResult, error) {
	options, err := normalizeHistoryQuery(query)
	if err != nil {
		return HistoryResult{}, err
	}
	result := HistoryResult{Events: make([]HistoryEvent, 0, options.maxEvents)}
	usedBytes := 0
	for _, event := range events {
		if !matchesHistoryEvent(event, query, options.eventTypes, options.needle) {
			continue
		}
		if len(result.Events) >= options.maxEvents {
			markHistoryTruncated(&result)
			break
		}
		candidate, encodedBytes, eventTruncated, eventErr := boundedHistoryEvent(event, usedBytes, options.maxBytes)
		if eventErr != nil {
			return HistoryResult{}, eventErr
		}
		if candidate == nil {
			markHistoryTruncated(&result)
			break
		}
		result.Events = append(result.Events, *candidate)
		usedBytes += encodedBytes
		if eventTruncated {
			result.Truncated = true
		}
		result.NextAfterSequence = candidate.Sequence
	}
	if !result.Truncated {
		result.NextAfterSequence = 0
	}
	return result, nil
}

type historyQueryOptions struct {
	maxEvents  int
	maxBytes   int
	eventTypes map[string]struct{}
	needle     string
}

func normalizeHistoryQuery(query HistoryQuery) (historyQueryOptions, error) {
	if len(query.Query) > maxHistoryQueryBytes {
		return historyQueryOptions{}, apperr.New(apperr.KindUsage, "session.history", "query exceeds the history search limit")
	}
	maxEvents, err := historyLimit(query.MaxEvents, defaultHistoryEvents, maxHistoryEvents, "events")
	if err != nil {
		return historyQueryOptions{}, err
	}
	maxBytes, err := historyLimit(query.MaxBytes, defaultHistoryBytes, maxHistoryBytes, "bytes")
	if err != nil {
		return historyQueryOptions{}, err
	}
	eventTypes := make(map[string]struct{}, len(query.EventTypes))
	for _, eventType := range query.EventTypes {
		if eventType == "" || len(eventType) > 64 {
			return historyQueryOptions{}, apperr.New(apperr.KindUsage, "session.history", "event type is empty or too long")
		}
		eventTypes[eventType] = struct{}{}
	}
	return historyQueryOptions{maxEvents: maxEvents, maxBytes: maxBytes, eventTypes: eventTypes, needle: strings.ToLower(query.Query)}, nil
}

func matchesHistoryEvent(event Event, query HistoryQuery, eventTypes map[string]struct{}, needle string) bool {
	// Note: history search is intentionally limited to public, redacted events.
	if event.Sequence <= query.AfterSequence || (query.BeforeSequence > 0 && event.Sequence >= query.BeforeSequence) {
		return false
	}
	if len(eventTypes) > 0 {
		if _, ok := eventTypes[event.Type]; !ok {
			return false
		}
	}
	return needle == "" || strings.Contains(strings.ToLower(event.Type+" "+string(event.Data)), needle)
}

func boundedHistoryEvent(event Event, usedBytes, maxBytes int) (*HistoryEvent, int, bool, error) {
	candidate := &HistoryEvent{Sequence: event.Sequence, Timestamp: event.Timestamp, Type: event.Type, Data: append(json.RawMessage(nil), event.Data...)}
	encoded, err := json.Marshal(candidate)
	if err != nil {
		return nil, 0, false, apperr.Wrap(apperr.KindTool, "session.history", err)
	}
	if usedBytes+len(encoded) <= maxBytes {
		return candidate, len(encoded), false, nil
	}
	candidate.Data = json.RawMessage(`{"truncated":true}`)
	encoded, err = json.Marshal(candidate)
	if err != nil {
		return nil, 0, false, apperr.Wrap(apperr.KindTool, "session.history", err)
	}
	if usedBytes+len(encoded) > maxBytes {
		return nil, 0, true, nil
	}
	return candidate, len(encoded), true, nil
}

func markHistoryTruncated(result *HistoryResult) {
	result.Truncated = true
	if len(result.Events) > 0 {
		result.NextAfterSequence = result.Events[len(result.Events)-1].Sequence
	}
}

func historyLimit(value, defaultValue, maximum int, name string) (int, error) {
	if value == 0 {
		return defaultValue, nil
	}
	if value < 0 || value > maximum {
		return 0, apperr.New(apperr.KindUsage, "session.history", name+" limit is outside the allowed range")
	}
	return value, nil
}

type historyTool struct {
	store Store
}

// NewHistoryTool creates the model-visible bounded history capability.
func NewHistoryTool(store Store) tools.Tool {
	return &historyTool{store: store}
}

func (tool *historyTool) Definition() tools.Definition {
	return tools.Definition{
		Name:           "session.history",
		Description:    "Search bounded public events from the active session. Older context is available here when it was trimmed from the current model request.",
		Parameters:     json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","maxLength":2048},"event_types":{"type":"array","items":{"type":"string"},"maxItems":8},"after_sequence":{"type":"integer","minimum":0},"before_sequence":{"type":"integer","minimum":0},"max_events":{"type":"integer","minimum":1,"maximum":100},"max_bytes":{"type":"integer","minimum":1,"maximum":65536}}}`),
		Risk:           tools.RiskReadOnly,
		Timeout:        5 * 1e9,
		MaxOutputBytes: maxHistoryBytes,
		MaxArguments:   6,
	}
}

func (tool *historyTool) Execute(ctx context.Context, call tools.Call) tools.Result {
	id, ok := ActiveID(ctx)
	if !ok {
		return tools.Result{Status: tools.StatusFailed, Diagnostics: []tools.Diagnostic{{Level: "error", Message: "active session is not bound"}}}
	}
	var query HistoryQuery
	if len(call.Arguments) > 0 {
		if err := json.Unmarshal(call.Arguments, &query); err != nil {
			return tools.Result{Status: tools.StatusFailed, Diagnostics: []tools.Diagnostic{{Level: "error", Message: err.Error()}}}
		}
	}
	result, err := tool.store.History(ctx, id, query)
	if err != nil {
		status := tools.StatusFailed
		if ctx.Err() != nil {
			status = tools.StatusCancelled
		}
		return tools.Result{Status: status, Diagnostics: []tools.Diagnostic{{Level: "error", Message: err.Error()}}}
	}
	return tools.Result{Status: tools.StatusSucceeded, Data: result}
}

var _ tools.Tool = (*historyTool)(nil)
