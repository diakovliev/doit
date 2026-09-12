package session

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/diakovliev/doit/internal/apperr"
)

const (
	defaultMaxEventBytes = 64 * 1024
	lockStaleAfter       = time.Hour
)

var (
	redactionPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(authorization\s*:\s*bearer\s+)[^\s"']+`),
		regexp.MustCompile(`(?i)(bearer\s+)[^\s"']+`),
		regexp.MustCompile(`(?i)((?:api[_-]?key|token|password|secret)\s*[:=]\s*)[^\s,}"']+`),
	}
)

// Options controls project-local or ephemeral session storage.
type Options struct {
	InvocationPath string
	Ephemeral      bool
	MaxEventBytes  int
}

// FileStore persists sessions below .doit/sessions or keeps them in memory.
type FileStore struct {
	root          string
	sessionsRoot  string
	rootFS        *os.Root
	ephemeral     bool
	maxEventBytes int
	mu            sync.Mutex
	memory        map[ID]Record
}

// NewFileStore creates a session store anchored to the effective invocation path.
func NewFileStore(options Options) (*FileStore, error) {
	if options.InvocationPath == "" {
		return nil, apperr.New(apperr.KindConfig, "session.store", "invocation path is required")
	}
	root, err := filepath.Abs(options.InvocationPath)
	if err != nil {
		return nil, apperr.Wrap(apperr.KindConfig, "session.store", err)
	}
	maxEventBytes := options.MaxEventBytes
	if maxEventBytes <= 0 {
		maxEventBytes = defaultMaxEventBytes
	}
	store := &FileStore{
		root:          root,
		sessionsRoot:  filepath.Join(root, ".doit", "sessions"),
		ephemeral:     options.Ephemeral,
		maxEventBytes: maxEventBytes,
		memory:        make(map[ID]Record),
	}
	if !options.Ephemeral {
		if err := os.MkdirAll(store.sessionsRoot, 0700); err != nil {
			return nil, apperr.Wrap(apperr.KindTool, "session.store", err)
		}
		rootFS, err := os.OpenRoot(store.sessionsRoot)
		if err != nil {
			return nil, apperr.Wrap(apperr.KindTool, "session.store", err)
		}
		store.rootFS = rootFS
	}
	return store, nil
}

// Close releases the rooted session directory handle.
func (store *FileStore) Close() error {
	if store.rootFS == nil {
		return nil
	}
	return store.rootFS.Close()
}

// Start creates an active session and its manifest/event files.
func (store *FileStore) Start(ctx context.Context, metadata Metadata) (ID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if metadata.ID == "" {
		metadata.ID = newID()
	}
	if !validID(metadata.ID) {
		return "", apperr.New(apperr.KindUsage, "session.start", "invalid session id")
	}
	now := time.Now().UTC()
	if metadata.CreatedAt.IsZero() {
		metadata.CreatedAt = now
	}
	metadata.UpdatedAt = now
	metadata.Status = StatusActive
	metadata.Resumable = !store.ephemeral
	if store.ephemeral {
		store.memory[metadata.ID] = Record{Metadata: metadata, Events: make([]Event, 0)}
		return metadata.ID, nil
	}
	return store.startPersistent(metadata)
}

// Latest returns the newest completed or failed durable session in this workspace.
func (store *FileStore) Latest(ctx context.Context) (ID, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.ephemeral {
		return "", false, nil
	}
	directory, err := store.rootFS.Open(".")
	if err != nil {
		return "", false, apperr.Wrap(apperr.KindTool, "session.latest", err)
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil {
		return "", false, apperr.Wrap(apperr.KindTool, "session.latest", readErr)
	}
	if closeErr != nil {
		return "", false, apperr.Wrap(apperr.KindTool, "session.latest", closeErr)
	}
	return newestSession(store.rootFS, entries)
}

// Resume reopens a completed or failed session while preserving its history.
func (store *FileStore) Resume(ctx context.Context, id ID, metadata Metadata) (ID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.ephemeral {
		return "", apperr.New(apperr.KindPolicy, "session.resume", "ephemeral sessions cannot be resumed")
	}
	return store.resumePersistent(id, metadata)
}

func (store *FileStore) resumePersistent(id ID, metadata Metadata) (ID, error) {
	directory, err := store.existingSessionDirectory(id)
	if err != nil {
		return "", err
	}
	existing, err := readMetadata(store.rootFS, filepath.Join(directory, "manifest.json"))
	if err != nil {
		return "", err
	}
	if !existing.Resumable {
		return "", apperr.New(apperr.KindPolicy, "session.resume", "session is not resumable")
	}
	if err := acquireLock(store.rootFS, directory); err != nil {
		return "", err
	}
	if err := store.writeResumedMetadata(directory, mergeResumeMetadata(existing, metadata)); err != nil {
		_ = store.rootFS.Remove(filepath.Join(directory, "lock"))
		return "", err
	}
	return id, nil
}

func mergeResumeMetadata(existing, requested Metadata) Metadata {
	resumed := existing
	if requested.InvocationPath != "" {
		resumed.InvocationPath = requested.InvocationPath
	}
	if requested.Command != "" {
		resumed.Command = requested.Command
	}
	if requested.Profile != "" {
		resumed.Profile = requested.Profile
	}
	if requested.Model != "" {
		resumed.Model = requested.Model
	}
	resumed.UpdatedAt = time.Now().UTC()
	resumed.Status = StatusActive
	return resumed
}

func (store *FileStore) writeResumedMetadata(directory string, metadata Metadata) error {
	if err := atomicWriteJSON(store.rootFS, filepath.Join(directory, "manifest.json"), metadata); err != nil {
		return err
	}
	if err := store.rootFS.Remove(filepath.Join(directory, "result.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return apperr.Wrap(apperr.KindTool, "session.resume", err)
	}
	return nil
}

func (store *FileStore) startPersistent(metadata Metadata) (ID, error) {
	directory := string(metadata.ID)
	if err := store.rootFS.MkdirAll(directory, 0700); err != nil {
		return "", apperr.Wrap(apperr.KindTool, "session.start", err)
	}
	if err := acquireLock(store.rootFS, directory); err != nil {
		return "", err
	}
	if err := atomicWriteJSON(store.rootFS, filepath.Join(directory, "manifest.json"), metadata); err != nil {
		_ = store.rootFS.Remove(filepath.Join(directory, "lock"))
		return "", err
	}
	eventsFile, err := store.rootFS.OpenFile(filepath.Join(directory, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		_ = store.rootFS.Remove(filepath.Join(directory, "lock"))
		return "", apperr.Wrap(apperr.KindTool, "session.start", err)
	}
	if err := eventsFile.Close(); err != nil {
		_ = store.rootFS.Remove(filepath.Join(directory, "lock"))
		return "", apperr.Wrap(apperr.KindTool, "session.start", err)
	}
	return metadata.ID, nil
}

func newestSession(root *os.Root, entries []os.DirEntry) (ID, bool, error) {
	var newest ID
	var newestTime time.Time
	for _, entry := range entries {
		if !entry.IsDir() || !validID(ID(entry.Name())) {
			continue
		}
		metadata, err := readMetadata(root, filepath.Join(entry.Name(), "manifest.json"))
		if err != nil || !metadata.Resumable || metadata.Status == StatusActive {
			continue
		}
		updatedAt := metadata.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = metadata.CreatedAt
		}
		if newest == "" || updatedAt.After(newestTime) {
			newest = ID(entry.Name())
			newestTime = updatedAt
		}
	}
	return newest, newest != "", nil
}

// Append writes one redacted, bounded event in sequence order.
func (store *FileStore) Append(ctx context.Context, id ID, event Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.ephemeral {
		return store.appendMemory(id, event)
	}
	return store.appendPersistent(id, event)
}

func (store *FileStore) appendMemory(id ID, event Event) error {
	record, exists := store.memory[id]
	if !exists {
		return apperr.New(apperr.KindTool, "session.append", "session not found")
	}
	event = normalizeEvent(record.Events, event, store.maxEventBytes)
	record.Events = append(record.Events, event)
	record.Metadata.UpdatedAt = time.Now().UTC()
	store.memory[id] = record
	return nil
}

func (store *FileStore) appendPersistent(id ID, event Event) error {
	directory, err := store.existingSessionDirectory(id)
	if err != nil {
		return err
	}
	eventsPath := filepath.Join(directory, "events.jsonl")
	existing, err := readEvents(store.rootFS, eventsPath)
	if err != nil {
		return err
	}
	event = normalizeEvent(existing, event, store.maxEventBytes)
	line, err := json.Marshal(event)
	if err != nil {
		return apperr.Wrap(apperr.KindTool, "session.append", err)
	}
	file, err := store.rootFS.OpenFile(eventsPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return apperr.Wrap(apperr.KindTool, "session.append", err)
	}
	if _, writeErr := fmt.Fprintf(file, "%s\n", line); writeErr != nil {
		_ = file.Close()
		return apperr.Wrap(apperr.KindTool, "session.append", writeErr)
	}
	if closeErr := file.Close(); closeErr != nil {
		return apperr.Wrap(apperr.KindTool, "session.append", closeErr)
	}
	metadata, err := readMetadata(store.rootFS, filepath.Join(directory, "manifest.json"))
	if err != nil {
		return err
	}
	metadata.UpdatedAt = time.Now().UTC()
	return atomicWriteJSON(store.rootFS, filepath.Join(directory, "manifest.json"), metadata)
}

// Complete writes a redacted result and closes the active lock.
func (store *FileStore) Complete(ctx context.Context, id ID, result Result) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.ephemeral {
		record, exists := store.memory[id]
		if !exists {
			return apperr.New(apperr.KindTool, "session.complete", "session not found")
		}
		result = redactResult(result, store.maxEventBytes)
		record.Result = &result
		record.Metadata.Status = StatusCompleted
		record.Metadata.UpdatedAt = time.Now().UTC()
		store.memory[id] = record
		return nil
	}
	directory, err := store.existingSessionDirectory(id)
	if err != nil {
		return err
	}
	result = redactResult(result, store.maxEventBytes)
	if err := atomicWriteJSON(store.rootFS, filepath.Join(directory, "result.json"), result); err != nil {
		return err
	}
	metadata, err := readMetadata(store.rootFS, filepath.Join(directory, "manifest.json"))
	if err != nil {
		return err
	}
	metadata.Status = StatusCompleted
	metadata.UpdatedAt = time.Now().UTC()
	if err := atomicWriteJSON(store.rootFS, filepath.Join(directory, "manifest.json"), metadata); err != nil {
		return err
	}
	if err := store.rootFS.Remove(filepath.Join(directory, "lock")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return apperr.Wrap(apperr.KindTool, "session.complete", err)
	}
	return nil
}

// WriteContinuation stores opaque provider state needed to resume a session.
func (store *FileStore) WriteContinuation(ctx context.Context, id ID, data json.RawMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(data) == 0 || !json.Valid(data) {
		return apperr.New(apperr.KindUsage, "session.continuation", "continuation must be valid JSON")
	}
	if len(data) > store.maxEventBytes {
		return apperr.New(apperr.KindTool, "session.continuation", "continuation exceeds size limit")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.ephemeral {
		record, exists := store.memory[id]
		if !exists {
			return apperr.New(apperr.KindTool, "session.continuation", "session not found")
		}
		record.Continuation = append(json.RawMessage(nil), data...)
		store.memory[id] = record
		return nil
	}
	directory, err := store.existingSessionDirectory(id)
	if err != nil {
		return err
	}
	return atomicWriteJSON(store.rootFS, filepath.Join(directory, "continuation.json"), data)
}

// Load reads a complete or interrupted session.
func (store *FileStore) Load(ctx context.Context, id ID) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.ephemeral {
		return store.loadMemory(id)
	}
	return store.loadPersistent(id)
}

func (store *FileStore) loadMemory(id ID) (Record, error) {
	record, exists := store.memory[id]
	if !exists {
		return Record{}, apperr.New(apperr.KindTool, "session.load", "session not found")
	}
	return record, nil
}

func (store *FileStore) loadPersistent(id ID) (Record, error) {
	directory, err := store.existingSessionDirectory(id)
	if err != nil {
		return Record{}, err
	}
	metadata, err := readMetadata(store.rootFS, filepath.Join(directory, "manifest.json"))
	if err != nil {
		return Record{}, err
	}
	events, err := readEvents(store.rootFS, filepath.Join(directory, "events.jsonl"))
	if err != nil {
		return Record{}, err
	}
	record := Record{Metadata: metadata, Events: events}
	result, resultErr := readResult(store.rootFS, filepath.Join(directory, "result.json"))
	if resultErr == nil {
		record.Result = &result
	} else if !errors.Is(resultErr, os.ErrNotExist) {
		return Record{}, resultErr
	}
	continuation, continuationErr := readRootFile(store.rootFS, filepath.Join(directory, "continuation.json"))
	if continuationErr == nil {
		record.Continuation = continuation
	} else if !errors.Is(continuationErr, os.ErrNotExist) {
		return Record{}, apperr.Wrap(apperr.KindTool, "session.read", continuationErr)
	}
	return record, nil
}

func (store *FileStore) existingSessionDirectory(id ID) (string, error) {
	if !validID(id) {
		return "", apperr.New(apperr.KindUsage, "session.path", "invalid session id")
	}
	directory := string(id)
	if _, err := store.rootFS.Stat(directory); err != nil {
		return "", apperr.Wrap(apperr.KindTool, "session.path", err)
	}
	return directory, nil
}

func acquireLock(root *os.Root, directory string) error {
	lockPath := filepath.Join(directory, "lock")
	file, err := root.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if !errors.Is(err, os.ErrExist) {
			return apperr.Wrap(apperr.KindTool, "session.lock", err)
		}
		info, statErr := root.Stat(lockPath)
		if statErr == nil && time.Since(info.ModTime()) > lockStaleAfter {
			if removeErr := root.Remove(lockPath); removeErr == nil {
				return acquireLock(root, directory)
			}
		}
		return apperr.New(apperr.KindPolicy, "session.lock", "session is already active")
	}
	defer func() { _ = file.Close() }()
	_, err = fmt.Fprintf(file, "pid=%d\nstarted=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return apperr.Wrap(apperr.KindTool, "session.lock", err)
	}
	return nil
}

func atomicWriteJSON(root *os.Root, path string, value any) error {
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return apperr.Wrap(apperr.KindTool, "session.write", err)
	}
	temporaryPath := filepath.Join(filepath.Dir(path), ".doit-session-"+string(newID()))
	temporary, err := root.OpenFile(temporaryPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return apperr.Wrap(apperr.KindTool, "session.write", err)
	}
	defer func() { _ = root.Remove(temporaryPath) }()
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return apperr.Wrap(apperr.KindTool, "session.write", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return apperr.Wrap(apperr.KindTool, "session.write", err)
	}
	if err := temporary.Close(); err != nil {
		return apperr.Wrap(apperr.KindTool, "session.write", err)
	}
	if err := root.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return apperr.Wrap(apperr.KindTool, "session.write", err)
	}
	if err := root.Rename(temporaryPath, path); err != nil {
		return apperr.Wrap(apperr.KindTool, "session.write", err)
	}
	return nil
}

func normalizeEvent(existing []Event, event Event, maxBytes int) Event {
	if event.Sequence == 0 {
		event.Sequence = uint64(len(existing) + 1)
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	event.Data = redactAndBound(event.Data, maxBytes)
	return event
}

func redactAndBound(data json.RawMessage, maxBytes int) json.RawMessage {
	if len(data) == 0 {
		return nil
	}
	value := redactString(string(data))
	if len(value) > maxBytes {
		value = `{"truncated":true}`
	}
	return json.RawMessage(value)
}

func redactResult(result Result, maxBytes int) Result {
	encoded, err := json.Marshal(result)
	if err == nil {
		redacted := redactString(string(encoded))
		if len(redacted) <= maxBytes {
			var normalized Result
			if json.Unmarshal([]byte(redacted), &normalized) == nil {
				return normalized
			}
		}
	}
	result.Summary = truncate(redactString(result.Summary), maxBytes/4)
	for index := range result.Unresolved {
		result.Unresolved[index] = truncate(redactString(result.Unresolved[index]), maxBytes/8)
	}
	for index := range result.Validations {
		result.Validations[index].Diagnostics = truncate(redactString(result.Validations[index].Diagnostics), maxBytes/8)
	}
	return result
}

func truncate(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	return value[:maxBytes]
}

func redactString(value string) string {
	for _, pattern := range redactionPatterns {
		value = pattern.ReplaceAllString(value, "$1[REDACTED]")
	}
	return value
}

func readMetadata(root *os.Root, path string) (Metadata, error) {
	contents, err := readRootFile(root, path)
	if err != nil {
		return Metadata{}, apperr.Wrap(apperr.KindTool, "session.read", err)
	}
	var metadata Metadata
	if err := json.Unmarshal(contents, &metadata); err != nil {
		return Metadata{}, apperr.Wrap(apperr.KindTool, "session.read", err)
	}
	return metadata, nil
}

func readResult(root *os.Root, path string) (Result, error) {
	contents, err := readRootFile(root, path)
	if err != nil {
		return Result{}, err
	}
	var result Result
	if err := json.Unmarshal(contents, &result); err != nil {
		return Result{}, apperr.Wrap(apperr.KindTool, "session.read", err)
	}
	return result, nil
}

func readEvents(root *os.Root, path string) ([]Event, error) {
	file, err := root.Open(path)
	if err != nil {
		return nil, apperr.Wrap(apperr.KindTool, "session.read", err)
	}
	defer func() { _ = file.Close() }()
	events := make([]Event, 0)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, apperr.Wrap(apperr.KindTool, "session.read", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, apperr.Wrap(apperr.KindTool, "session.read", err)
	}
	return events, nil
}

func readRootFile(root *os.Root, path string) ([]byte, error) {
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	contents, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return contents, nil
}

func newID() ID {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return ID(fmt.Sprintf("session-%d", time.Now().UnixNano()))
	}
	return ID(hex.EncodeToString(bytes))
}

func validID(id ID) bool {
	return id != "" && filepath.Base(string(id)) == string(id) && !strings.ContainsAny(string(id), `/\`)
}
