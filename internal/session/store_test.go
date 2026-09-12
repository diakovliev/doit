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

func containsSecret(data []byte) bool {
	return bytes.Contains(data, []byte("secret-token"))
}
