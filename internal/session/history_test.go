package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/diakovliev/doit/internal/tools"
)

func TestHistoryFiltersPublicEventsAndUsesCursorBounds(t *testing.T) {
	store, id := newHistoryTestStore(t)
	appendHistoryEvent(t, store, id, "model_message", `{"text":"started"}`)
	appendHistoryEvent(t, store, id, "validation", `{"message":"tests failed","exit_code":1}`)
	appendHistoryEvent(t, store, id, "tool_result", `{"content":"unrelated"}`)

	result, err := store.History(context.Background(), id, HistoryQuery{Query: "tests failed", EventTypes: []string{"validation"}, MaxEvents: 10, MaxBytes: 4096})
	if err != nil || len(result.Events) != 1 {
		t.Fatalf("unexpected filtered history: %+v, error=%v", result, err)
	}
	if result.Events[0].Type != "validation" || !strings.Contains(string(result.Events[0].Data), "tests failed") {
		t.Fatalf("unexpected history event: %+v", result.Events[0])
	}

	result, err = store.History(context.Background(), id, HistoryQuery{AfterSequence: result.Events[0].Sequence, MaxEvents: 10})
	if err != nil || len(result.Events) != 1 || result.Events[0].Type != "tool_result" {
		t.Fatalf("unexpected cursor history: %+v, error=%v", result, err)
	}
}

func TestHistoryBoundsResultsAndRequiresActiveSession(t *testing.T) {
	store, id := newHistoryTestStore(t)
	appendHistoryEvent(t, store, id, "model_message", `{"text":"one"}`)
	appendHistoryEvent(t, store, id, "model_message", `{"text":"two"}`)
	otherID, err := store.Start(context.Background(), Metadata{Command: "other", Model: "fixture"})
	if err != nil {
		t.Fatalf("start other session: %v", err)
	}
	appendHistoryEvent(t, store, otherID, "validation", `{"message":"other session"}`)

	result, err := store.History(context.Background(), id, HistoryQuery{MaxEvents: 1, MaxBytes: 4096})
	assertHistoryBounds(t, result, err)

	tool := NewHistoryTool(store)
	withoutSession := tool.Execute(context.Background(), tools.Call{Arguments: []byte(`{}`)})
	assertHistoryRequiresScope(t, withoutSession)
	withSession := tool.Execute(WithActiveID(context.Background(), id), tools.Call{Arguments: []byte(`{"max_events":1}`)})
	assertHistoryToolScope(t, withSession)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	assertHistoryCancellation(cancelled, t, store, id)
}

func assertHistoryBounds(t *testing.T, result HistoryResult, err error) {
	t.Helper()
	if err != nil || len(result.Events) != 1 || !result.Truncated || result.NextAfterSequence == 0 {
		t.Fatalf("history bounds were not reported: %+v, error=%v", result, err)
	}
}

func assertHistoryRequiresScope(t *testing.T, result tools.Result) {
	t.Helper()
	if result.Status != tools.StatusFailed {
		t.Fatalf("history tool escaped active-session scope: %+v", result)
	}
}

func assertHistoryToolScope(t *testing.T, result tools.Result) {
	t.Helper()
	if result.Status != tools.StatusSucceeded {
		t.Fatalf("history tool failed for active session: %+v", result)
	}
	data, ok := result.Data.(HistoryResult)
	if !ok || len(data.Events) != 1 || strings.Contains(string(data.Events[0].Data), "other session") {
		t.Fatalf("history tool crossed session boundary: %+v", result.Data)
	}
}

func assertHistoryCancellation(ctx context.Context, t *testing.T, store *FileStore, id ID) {
	t.Helper()
	if _, err := store.History(ctx, id, HistoryQuery{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("history did not preserve cancellation: %v", err)
	}
}

func newHistoryTestStore(t *testing.T) (*FileStore, ID) {
	t.Helper()
	store, err := NewFileStore(Options{InvocationPath: t.TempDir(), Ephemeral: true})
	if err != nil {
		t.Fatalf("new session store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	id, err := store.Start(context.Background(), Metadata{Command: "test", Model: "fixture"})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	return store, id
}

func appendHistoryEvent(t *testing.T, store *FileStore, id ID, eventType, data string) {
	t.Helper()
	if !json.Valid([]byte(data)) {
		t.Fatalf("invalid fixture JSON: %s", data)
	}
	if err := store.Append(context.Background(), id, Event{Type: eventType, Data: json.RawMessage(data)}); err != nil {
		t.Fatalf("append history event: %v", err)
	}
}
