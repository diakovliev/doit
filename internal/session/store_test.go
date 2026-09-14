package session

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileStorePersistsOrderedRedactedSession(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root, false)
	defer func() { _ = store.Close() }()
	id := startTestSession(t, store, root)
	eventData := appendTestEvent(t, store, id)
	completeTestSession(t, store, id)
	reloaded := loadTestSession(t, store, id)
	assertPersistedSession(t, root, id, eventData, reloaded)
}

func TestFileStoreListsSessionMetadataNewestFirst(t *testing.T) {
	store := newStore(t, t.TempDir(), true)
	defer func() { _ = store.Close() }()
	completeNamedSession(t, store, "first")
	completeNamedSession(t, store, "second")
	metadata, err := store.List(context.Background())
	assertSessionList(t, metadata, err)
}

func completeNamedSession(t *testing.T, store *FileStore, command string) ID {
	t.Helper()
	id, err := store.Start(context.Background(), Metadata{Command: command})
	if err != nil {
		t.Fatalf("start %s session: %v", command, err)
	}
	if err := store.Complete(context.Background(), id, Result{Summary: command}); err != nil {
		t.Fatalf("complete %s session: %v", command, err)
	}
	return id
}

func assertSessionList(t *testing.T, metadata []Metadata, err error) {
	t.Helper()
	if err != nil || len(metadata) != 2 || metadata[0].UpdatedAt.Before(metadata[1].UpdatedAt) {
		t.Fatalf("unexpected session list: %+v, error=%v", metadata, err)
	}
	commands := map[string]bool{metadata[0].Command: true, metadata[1].Command: true}
	if !commands["first"] || !commands["second"] {
		t.Fatalf("session list omitted a session: %+v", metadata)
	}
}

func TestFileStorePrunesOldSessions(t *testing.T) {
	store, err := NewFileStore(Options{InvocationPath: t.TempDir()})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer func() { _ = store.Close() }()
	first, err := store.Start(context.Background(), Metadata{Command: "first"})
	if err != nil {
		t.Fatalf("start first session: %v", err)
	}
	if err := store.Complete(context.Background(), first, Result{Summary: "first"}); err != nil {
		t.Fatalf("complete first session: %v", err)
	}
	second, err := store.Start(context.Background(), Metadata{Command: "second"})
	if err != nil {
		t.Fatalf("start second session: %v", err)
	}
	if err := store.Complete(context.Background(), second, Result{Summary: "second"}); err != nil {
		t.Fatalf("complete second session: %v", err)
	}
	removed, err := store.Prune(context.Background(), 1)
	if err != nil || removed != 1 {
		t.Fatalf("unexpected prune result: removed=%d error=%v", removed, err)
	}
	metadata, err := store.List(context.Background())
	if err != nil || len(metadata) != 1 {
		t.Fatalf("unexpected sessions after prune: %+v, error=%v", metadata, err)
	}
}

func newStore(t *testing.T, root string, ephemeral bool) *FileStore {
	t.Helper()
	store, err := NewFileStore(Options{InvocationPath: root, Ephemeral: ephemeral, MaxEventBytes: 512})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func startTestSession(t *testing.T, store *FileStore, root string) ID {
	t.Helper()
	id, err := store.Start(context.Background(), Metadata{Command: "run", InvocationPath: root})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	return id
}

func appendTestEvent(t *testing.T, store *FileStore, id ID) []byte {
	t.Helper()
	eventData, err := json.Marshal(map[string]string{"authorization": "Bearer secret-token"})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if err := store.Append(context.Background(), id, Event{Type: "request", Data: eventData}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	if err := store.WriteContinuation(context.Background(), id, json.RawMessage(`{"response_id":"resp-1"}`)); err != nil {
		t.Fatalf("write continuation: %v", err)
	}
	return eventData
}

func completeTestSession(t *testing.T, store *FileStore, id ID) {
	t.Helper()
	if err := store.Complete(context.Background(), id, Result{Summary: "done token=secret-result"}); err != nil {
		t.Fatalf("complete session: %v", err)
	}
}

func loadTestSession(t *testing.T, store *FileStore, id ID) Record {
	t.Helper()
	reloaded, err := store.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	return reloaded
}

func assertPersistedSession(t *testing.T, root string, id ID, eventData []byte, reloaded Record) {
	t.Helper()
	if len(reloaded.Events) != 1 || reloaded.Events[0].Sequence != 1 || reloaded.Result == nil || len(reloaded.Continuation) == 0 {
		t.Fatalf("unexpected reloaded session: %+v", reloaded)
	}
	if string(reloaded.Events[0].Data) == string(eventData) || containsSecret(reloaded.Events[0].Data) {
		t.Fatalf("event was not redacted: %s", reloaded.Events[0].Data)
	}
	if containsSecret([]byte(reloaded.Result.Summary)) {
		t.Fatalf("result was not redacted: %+v", reloaded.Result)
	}
	if _, err := os.Stat(filepath.Join(root, ".doit", "sessions", string(id), "manifest.json")); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
}

func TestEphemeralStoreDoesNotWriteFiles(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(Options{InvocationPath: root, Ephemeral: true})
	if err != nil {
		t.Fatalf("new ephemeral store: %v", err)
	}
	defer func() { _ = store.Close() }()
	id, err := store.Start(context.Background(), Metadata{Command: "run"})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	if err := store.Append(context.Background(), id, Event{Type: "lifecycle"}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".doit")); !os.IsNotExist(err) {
		t.Fatalf("ephemeral store wrote project files: %v", err)
	}
}

func TestStaleLockCanBeRecovered(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(Options{InvocationPath: root})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer func() { _ = store.Close() }()
	oldID := ID("stale-session")
	directory := filepath.Join(root, ".doit", "sessions", string(oldID))
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatalf("make stale session: %v", err)
	}
	lockPath := filepath.Join(directory, "lock")
	if err := os.WriteFile(lockPath, []byte("stale"), 0600); err != nil {
		t.Fatalf("write stale lock: %v", err)
	}
	oldTime := time.Now().Add(-2 * lockStaleAfter)
	if err := os.Chtimes(lockPath, oldTime, oldTime); err != nil {
		t.Fatalf("age stale lock: %v", err)
	}
	if _, err := store.Start(context.Background(), Metadata{ID: oldID}); err != nil {
		t.Fatalf("recover stale lock: %v", err)
	}
}

func TestLatestAndResumePreserveSessionHistory(t *testing.T) {
	root, store := newPersistentStore(t)
	defer func() { _ = store.Close() }()
	firstID := startTestSession(t, store, root)
	completeTestSession(t, store, firstID)
	time.Sleep(time.Millisecond)
	secondID := startTestSession(t, store, root)
	appendSessionEvent(t, store, secondID)
	completeTestSession(t, store, secondID)

	latestID := latestSessionID(t, store, secondID)
	if _, err := store.Resume(context.Background(), latestID, Metadata{Command: "follow-up", Model: "new-model"}); err != nil {
		t.Fatalf("resume latest session: %v", err)
	}
	record, err := store.Load(context.Background(), latestID)
	if err != nil {
		t.Fatalf("load resumed session: %v", err)
	}
	assertResumedSession(t, record)
}

func TestAppendPreservesEscapedJSONWhileRedacting(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root, true)
	defer func() { _ = store.Close() }()
	id := startTestSession(t, store, root)
	data, err := json.Marshal(map[string]string{"content": "token=secret\"quoted\\path"})
	if err != nil {
		t.Fatalf("marshal escaped event: %v", err)
	}
	if err := store.Append(context.Background(), id, Event{Type: "tool_result", Data: data}); err != nil {
		t.Fatalf("append escaped event: %v", err)
	}
	record, err := store.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("load escaped event: %v", err)
	}
	if len(record.Events) != 1 || !json.Valid(record.Events[0].Data) || bytes.Contains(record.Events[0].Data, []byte("secret")) {
		t.Fatalf("escaped event was not safely redacted: %s", record.Events[0].Data)
	}
}

func newPersistentStore(t *testing.T) (string, *FileStore) {
	t.Helper()
	root := t.TempDir()
	return root, newStore(t, root, false)
}

func appendSessionEvent(t *testing.T, store *FileStore, id ID) {
	t.Helper()
	if err := store.Append(context.Background(), id, Event{Type: "request", Data: json.RawMessage(`{"request":"second"}`)}); err != nil {
		t.Fatalf("append second session event: %v", err)
	}
}

func latestSessionID(t *testing.T, store *FileStore, expected ID) ID {
	t.Helper()
	latestID, found, err := store.Latest(context.Background())
	if err != nil || !found || latestID != expected {
		t.Fatalf("unexpected latest session: id=%q found=%t error=%v", latestID, found, err)
	}
	return latestID
}

func assertResumedSession(t *testing.T, record Record) {
	t.Helper()
	if record.Metadata.Status != StatusActive {
		t.Fatalf("unexpected resumed status: %s", record.Metadata.Status)
	}
	if record.Metadata.Command != "follow-up" || record.Metadata.Model != "new-model" {
		t.Fatalf("resume metadata was not updated: %+v", record.Metadata)
	}
	if record.Result != nil || len(record.Events) != 1 {
		t.Fatalf("resume did not preserve session history correctly: %+v", record)
	}
}

func containsSecret(data []byte) bool {
	return bytes.Contains(data, []byte("secret-token"))
}
