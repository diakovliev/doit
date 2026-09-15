// Package codetools provides reviewable, workspace-confined code operations.
package codetools

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/diakovliev/doit/internal/apperr"
	"github.com/diakovliev/doit/internal/process"
	"github.com/diakovliev/doit/internal/processrunner"
	"github.com/diakovliev/doit/internal/tools"
)

// Service owns code changes below one workspace path.
type Service struct {
	workspace string
	runner    *processrunner.Runner
}

// New creates a code-operation service.
func New(workspace string, runner *processrunner.Runner) (*Service, error) {
	absoluteWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return nil, apperr.Wrap(apperr.KindConfig, "codetools.workspace", err)
	}
	return &Service{workspace: absoluteWorkspace, runner: runner}, nil
}

// PatchRequest describes a unified patch operation.
type PatchRequest struct {
	Patch          string            `json:"patch"`
	ExpectedHashes map[string]string `json:"expected_hashes,omitempty"`
	DryRun         bool              `json:"dry_run"`
}

// PatchResponse describes affected paths and preview state.
type PatchResponse struct {
	Patch     string           `json:"patch"`
	Paths     []string         `json:"paths"`
	Applied   bool             `json:"applied"`
	Changed   bool             `json:"changed"`
	ChangeSet *tools.ChangeSet `json:"change_set,omitempty"`
}

// CheckPatch validates a patch and returns affected paths without writing.
func (service *Service) CheckPatch(ctx context.Context, request PatchRequest) (PatchResponse, error) {
	if err := validatePatch(ctx, request.Patch); err != nil {
		return PatchResponse{}, err
	}
	operations, err := parsePatchOperations(request.Patch)
	if err != nil {
		return PatchResponse{}, err
	}
	paths := operationPaths(operations)
	if err := service.checkExpectedHashes(paths, request.ExpectedHashes); err != nil {
		return PatchResponse{}, err
	}
	changed, err := service.patchChanged(operations)
	if err != nil {
		return PatchResponse{}, err
	}
	changeSet, err := service.patchChangeSet(operations, "preview")
	if err != nil {
		return PatchResponse{}, err
	}
	return PatchResponse{Patch: request.Patch, Paths: paths, Applied: false, Changed: changed, ChangeSet: changeSet}, nil
}

// ApplyPatch validates and atomically applies a patch after the caller's policy check.
func (service *Service) ApplyPatch(ctx context.Context, request PatchRequest) (PatchResponse, error) {
	preview, err := service.CheckPatch(ctx, request)
	if err != nil {
		return PatchResponse{}, err
	}
	if request.DryRun {
		return preview, nil
	}
	operations, err := parsePatchOperations(request.Patch)
	if err != nil {
		return PatchResponse{}, err
	}
	root, err := os.OpenRoot(service.workspace)
	if err != nil {
		return PatchResponse{}, apperr.Wrap(apperr.KindTool, "codetools.apply_patch", err)
	}
	defer func() { _ = root.Close() }()
	if err := service.applyOperations(root, operations); err != nil {
		return PatchResponse{}, err
	}
	changeSet := *preview.ChangeSet
	changeSet.State = "applied"
	return PatchResponse{Patch: request.Patch, Paths: preview.Paths, Applied: true, Changed: preview.Changed, ChangeSet: &changeSet}, nil
}

// RenameRequest describes an in-workspace rename.
type RenameRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// RenameResponse describes the renamed paths.
type RenameResponse struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Rename moves a path without replacing an existing destination.
func (service *Service) Rename(ctx context.Context, request RenameRequest) (RenameResponse, error) {
	if err := ctx.Err(); err != nil {
		return RenameResponse{}, err
	}
	from, err := service.scopedPath(request.From)
	if err != nil {
		return RenameResponse{}, err
	}
	to, err := service.scopedPath(request.To)
	if err != nil {
		return RenameResponse{}, err
	}
	root, err := os.OpenRoot(service.workspace)
	if err != nil {
		return RenameResponse{}, apperr.Wrap(apperr.KindTool, "codetools.rename", err)
	}
	defer func() { _ = root.Close() }()
	if _, err := root.Stat(to); err == nil {
		return RenameResponse{}, apperr.New(apperr.KindPolicy, "codetools.rename", "destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return RenameResponse{}, apperr.Wrap(apperr.KindTool, "codetools.rename", err)
	}
	if err := root.Rename(from, to); err != nil {
		return RenameResponse{}, apperr.Wrap(apperr.KindTool, "codetools.rename", err)
	}
	return RenameResponse{From: filepath.ToSlash(request.From), To: filepath.ToSlash(request.To)}, nil
}

// FormatRequest runs a configured formatter and returns its result.
type FormatRequest struct {
	Task             string   `json:"task"`
	Arguments        []string `json:"arguments,omitempty"`
	WorkingDirectory string   `json:"working_directory,omitempty"`
}

// FormatResponse contains formatter output and status.
type FormatResponse struct {
	Result process.Result `json:"result"`
}

// Format runs a named formatter through the allowlisted process runner.
func (service *Service) Format(ctx context.Context, request FormatRequest) (FormatResponse, error) {
	if service.runner == nil {
		return FormatResponse{}, apperr.New(apperr.KindConfig, "codetools.format", "process runner is required")
	}
	result, err := service.runner.Run(ctx, process.Task{Name: request.Task, Arguments: request.Arguments, WorkingDirectory: request.WorkingDirectory})
	if err != nil {
		return FormatResponse{}, err
	}
	return FormatResponse{Result: result}, nil
}

// RegisterTools exposes code operations through the normalized registry.
func RegisterTools(registry *tools.Registry, service *Service) error {
	adapters := []toolAdapter{
		{name: "code.check_patch", description: "Preview a patch without changing files. Inspect with fs.read/fs.hash or git.diff first; returns affected paths, conflicts, hashes, and a preview change set.", parameters: `{"type":"object","properties":{"patch":{"type":"string","minLength":1},"expected_hashes":{"type":"object","additionalProperties":{"type":"string"}},"dry_run":{"type":"boolean","default":true}},"required":["patch"]}`, risk: tools.RiskReadOnly, changeSet: patchChangeSetFromData, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request PatchRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.CheckPatch(ctx, request)
		}},
		{name: "code.apply_patch", description: "Apply a patch only after inspecting or previewing it. Use exact *** Add/Update/Delete File blocks; do not use markdown fences. expected_hashes prevents overwriting a file changed after inspection. dry_run=true previews only; conflicts fail without partial writes.", parameters: `{"type":"object","properties":{"patch":{"type":"string","minLength":1},"expected_hashes":{"type":"object","additionalProperties":{"type":"string"}},"dry_run":{"type":"boolean","default":false}},"required":["patch"]}`, risk: tools.RiskWrite, changedPaths: patchChangedPaths, changeSet: patchChangeSetFromData, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request PatchRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.ApplyPatch(ctx, request)
		}},
		{name: "code.rename", description: "Rename one workspace path without replacing an existing destination. Inspect both paths first; fails on collisions.", parameters: `{"type":"object","properties":{"from":{"type":"string"},"to":{"type":"string"}},"required":["from","to"]}`, risk: tools.RiskWrite, changedPaths: renameChangedPaths, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request RenameRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Rename(ctx, request)
		}},
		{name: "code.format", description: "Run a configured formatter task inside the workspace. Use a task name and workspace-relative formatter arguments from the repository configuration.", parameters: `{"type":"object","properties":{"task":{"type":"string"},"arguments":{"type":"array","items":{"type":"string"}},"working_directory":{"type":"string"}},"required":["task"]}`, risk: tools.RiskProcess, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request FormatRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Format(ctx, request)
		}},
	}
	adapters = append(adapters, structuredEditAdapters(service)...)
	for _, adapter := range adapters {
		if err := registry.Register(adapter); err != nil {
			return err
		}
	}
	return nil
}

func structuredEditAdapters(service *Service) []toolAdapter {
	return []toolAdapter{
		{name: "code.replace_exact", description: "Small-model edit: inspect/hash first, then replace exactly one old_text occurrence. Zero or multiple matches are conflicts, never fuzzy matches. Use dry_run=true first; expected_hash prevents stale edits.", parameters: `{"type":"object","properties":{"path":{"type":"string"},"old_text":{"type":"string","minLength":1,"maxLength":65536},"new_text":{"type":"string","maxLength":65536},"expected_hash":{"type":"string"},"dry_run":{"type":"boolean","default":false}},"required":["path","old_text","new_text"]}`, risk: tools.RiskWrite, changedPaths: structuredEditChangedPaths, changeSet: structuredEditChangeSetFromData, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request ExactReplaceRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.ReplaceExact(ctx, request)
		}},
		{name: "code.insert_at_anchor", description: "Small-model edit: inspect/hash first, then insert before or after exactly one anchor. Zero or multiple anchors are conflicts. Use dry_run=true first; expected_hash prevents stale edits.", parameters: `{"type":"object","properties":{"path":{"type":"string"},"anchor":{"type":"string","minLength":1,"maxLength":65536},"position":{"type":"string","enum":["before","after"]},"content":{"type":"string","minLength":1,"maxLength":65536},"expected_hash":{"type":"string"},"dry_run":{"type":"boolean","default":false}},"required":["path","anchor","position","content"]}`, risk: tools.RiskWrite, changedPaths: structuredEditChangedPaths, changeSet: structuredEditChangeSetFromData, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request AnchorInsertRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.InsertAtAnchor(ctx, request)
		}},
		{name: "code.delete_exact", description: "Small-model edit: inspect/hash first, then delete exactly one text occurrence. Zero or multiple matches are conflicts; use dry_run=true first.", parameters: `{"type":"object","properties":{"path":{"type":"string"},"text":{"type":"string","minLength":1,"maxLength":65536},"expected_hash":{"type":"string"},"dry_run":{"type":"boolean","default":false}},"required":["path","text"]}`, risk: tools.RiskWrite, changedPaths: structuredEditChangedPaths, changeSet: structuredEditChangeSetFromData, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request ExactDeleteRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.DeleteExact(ctx, request)
		}},
	}
}

type toolAdapter struct {
	name         string
	description  string
	parameters   string
	risk         tools.Risk
	changedPaths func(any) []string
	changeSet    func(any) *tools.ChangeSet
	execute      func(context.Context, tools.Call) (any, error)
}

func (adapter toolAdapter) Definition() tools.Definition {
	return tools.Definition{Name: adapter.name, Description: adapter.description, Parameters: json.RawMessage(adapter.parameters), Risk: adapter.risk, Timeout: 30_000_000_000, MaxOutputBytes: 64 * 1024, MaxArguments: 16}
}

func (adapter toolAdapter) Execute(ctx context.Context, call tools.Call) tools.Result {
	data, err := adapter.execute(ctx, call)
	if err != nil {
		return tools.Result{Status: tools.StatusFailed, Diagnostics: []tools.Diagnostic{{Level: "error", Message: err.Error()}}}
	}
	result := tools.Result{Status: tools.StatusSucceeded, Data: data}
	if adapter.changedPaths != nil {
		result.ChangedPaths = adapter.changedPaths(data)
	}
	if adapter.changeSet != nil {
		result.ChangeSet = adapter.changeSet(data)
	}
	return result
}

func patchChangedPaths(data any) []string {
	response, ok := data.(PatchResponse)
	if !ok || !response.Changed {
		return nil
	}
	return append([]string(nil), response.Paths...)
}

func renameChangedPaths(data any) []string {
	response, ok := data.(RenameResponse)
	if !ok {
		return nil
	}
	return []string{response.From, response.To}
}

func patchChangeSetFromData(data any) *tools.ChangeSet {
	response, ok := data.(PatchResponse)
	if !ok {
		return nil
	}
	return response.ChangeSet
}

type patchOperation struct {
	path        string
	content     string
	updateLines []string
	delete      bool
}

type fileSnapshot struct {
	path    string
	content []byte
	exists  bool
}

func (service *Service) applyOperations(root *os.Root, operations []patchOperation) error {
	snapshots, err := service.captureSnapshots(root, operations)
	if err != nil {
		return err
	}
	for _, operation := range operations {
		if err := service.applyOperation(root, operation); err != nil {
			if rollbackErr := rollbackSnapshots(root, snapshots); rollbackErr != nil {
				return apperr.Wrap(apperr.KindTool, "codetools.apply_patch", errors.Join(err, rollbackErr))
			}
			return err
		}
	}
	return nil
}

func (service *Service) captureSnapshots(root *os.Root, operations []patchOperation) ([]fileSnapshot, error) {
	snapshots := make([]fileSnapshot, 0, len(operations))
	for _, operation := range operations {
		path, err := service.scopedPath(operation.path)
		if err != nil {
			return nil, err
		}
		_, statErr := root.Stat(path)
		if errors.Is(statErr, os.ErrNotExist) {
			snapshots = append(snapshots, fileSnapshot{path: path})
			continue
		}
		if statErr != nil {
			return nil, apperr.Wrap(apperr.KindTool, "codetools.apply_patch", statErr)
		}
		content, readErr := root.ReadFile(path)
		if readErr != nil {
			return nil, apperr.Wrap(apperr.KindTool, "codetools.apply_patch", readErr)
		}
		snapshots = append(snapshots, fileSnapshot{path: path, content: content, exists: true})
	}
	return snapshots, nil
}

func rollbackSnapshots(root *os.Root, snapshots []fileSnapshot) error {
	var rollbackErr error
	for index := len(snapshots) - 1; index >= 0; index-- {
		snapshot := snapshots[index]
		if snapshot.exists {
			if err := writeOperation(root, snapshot.path, string(snapshot.content)); err != nil {
				rollbackErr = errors.Join(rollbackErr, err)
			}
			continue
		}
		if err := root.Remove(snapshot.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollbackErr = errors.Join(rollbackErr, err)
		}
	}
	return rollbackErr
}

func (service *Service) applyOperation(root *os.Root, operation patchOperation) error {
	path, err := service.scopedPath(operation.path)
	if err != nil {
		return err
	}
	if operation.delete {
		if err := root.Remove(path); err != nil {
			return apperr.Wrap(apperr.KindTool, "codetools.apply_patch", err)
		}
		return nil
	}
	content, err := operationContent(root, path, operation)
	if err != nil {
		return err
	}
	current, readErr := root.ReadFile(path)
	if readErr == nil && string(current) == content {
		return nil
	}
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return apperr.Wrap(apperr.KindTool, "codetools.apply_patch", readErr)
	}
	return writeOperation(root, path, content)
}

func operationContent(root *os.Root, path string, operation patchOperation) (string, error) {
	if len(operation.updateLines) == 0 {
		return operation.content, nil
	}
	current, err := root.ReadFile(path)
	if err != nil {
		return "", apperr.Wrap(apperr.KindTool, "codetools.apply_patch", err)
	}
	return applyUpdateLines(string(current), operation.updateLines)
}

func writeOperation(root *os.Root, path, content string) error {
	parent := filepath.Dir(path)
	if parent != "." {
		if err := root.MkdirAll(parent, 0700); err != nil {
			return apperr.Wrap(apperr.KindTool, "codetools.apply_patch", err)
		}
	}
	temporaryPath, err := temporaryPath(parent)
	if err != nil {
		return apperr.Wrap(apperr.KindTool, "codetools.apply_patch", err)
	}
	temporary, err := root.OpenFile(temporaryPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return apperr.Wrap(apperr.KindTool, "codetools.apply_patch", err)
	}
	defer func() { _ = root.Remove(temporaryPath) }()
	if _, err := temporary.WriteString(content); err != nil {
		_ = temporary.Close()
		return apperr.Wrap(apperr.KindTool, "codetools.apply_patch", err)
	}
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return apperr.Wrap(apperr.KindTool, "codetools.apply_patch", err)
	}
	if err := temporary.Close(); err != nil {
		return apperr.Wrap(apperr.KindTool, "codetools.apply_patch", err)
	}
	if err := root.Rename(temporaryPath, path); err != nil {
		return apperr.Wrap(apperr.KindTool, "codetools.apply_patch", err)
	}
	return nil
}

func temporaryPath(parent string) (string, error) {
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	return filepath.Join(parent, ".doit-patch-"+hex.EncodeToString(suffix)), nil
}

func (service *Service) patchChanged(operations []patchOperation) (bool, error) {
	root, err := os.OpenRoot(service.workspace)
	if err != nil {
		return false, apperr.Wrap(apperr.KindTool, "codetools.check_patch", err)
	}
	defer func() { _ = root.Close() }()
	for _, operation := range operations {
		changed, err := service.operationChanged(root, operation)
		if err != nil {
			return false, err
		}
		if changed {
			return true, nil
		}
	}
	return false, nil
}

func (service *Service) patchChangeSet(operations []patchOperation, state string) (*tools.ChangeSet, error) {
	root, err := os.OpenRoot(service.workspace)
	if err != nil {
		return nil, apperr.Wrap(apperr.KindTool, "codetools.change_set", err)
	}
	defer func() { _ = root.Close() }()
	paths := make([]string, 0, len(operations))
	beforeHashes := make(map[string]string, len(operations))
	afterHashes := make(map[string]string, len(operations))
	for _, operation := range operations {
		path, pathErr := service.scopedPath(operation.path)
		if pathErr != nil {
			return nil, pathErr
		}
		before, beforeExists, readErr := readSnapshot(root, path)
		if readErr != nil {
			return nil, apperr.Wrap(apperr.KindTool, "codetools.change_set", readErr)
		}
		beforeHashes[path] = snapshotHash(before, beforeExists)
		var after []byte
		afterExists := !operation.delete
		if afterExists {
			content, contentErr := operationContent(root, path, operation)
			if contentErr != nil {
				return nil, contentErr
			}
			after = []byte(content)
		}
		afterHashes[path] = snapshotHash(after, afterExists)
		paths = append(paths, path)
	}
	changeSet := &tools.ChangeSet{Operation: "code.apply_patch", State: state, Paths: paths, BeforeHashes: beforeHashes, AfterHashes: afterHashes}
	identity, err := json.Marshal(struct {
		Operation    string            `json:"operation"`
		Paths        []string          `json:"paths"`
		BeforeHashes map[string]string `json:"before_hashes"`
		AfterHashes  map[string]string `json:"after_hashes"`
	}{Operation: changeSet.Operation, Paths: changeSet.Paths, BeforeHashes: changeSet.BeforeHashes, AfterHashes: changeSet.AfterHashes})
	if err != nil {
		return nil, apperr.Wrap(apperr.KindTool, "codetools.change_set", err)
	}
	digest := sha256.Sum256(identity)
	changeSet.ID = hex.EncodeToString(digest[:])
	return changeSet, nil
}

func readSnapshot(root *os.Root, path string) ([]byte, bool, error) {
	content, err := root.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return content, true, nil
}

func snapshotHash(content []byte, exists bool) string {
	if !exists {
		return ""
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func (service *Service) operationChanged(root *os.Root, operation patchOperation) (bool, error) {
	path, err := service.scopedPath(operation.path)
	if err != nil {
		return false, err
	}
	if operation.delete {
		return operationExists(root, path)
	}
	content, err := operationContent(root, path, operation)
	if err != nil {
		return false, err
	}
	current, err := root.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, apperr.Wrap(apperr.KindTool, "codetools.check_patch", err)
	}
	return string(current) != content, nil
}

func operationExists(root *os.Root, path string) (bool, error) {
	if _, err := root.Stat(path); err != nil {
		return false, apperr.Wrap(apperr.KindTool, "codetools.check_patch", err)
	}
	return true, nil
}

func (service *Service) checkExpectedHashes(paths []string, expected map[string]string) error {
	root, err := os.OpenRoot(service.workspace)
	if err != nil {
		return apperr.Wrap(apperr.KindTool, "codetools.check_patch", err)
	}
	defer func() { _ = root.Close() }()
	for _, relativePath := range paths {
		expectedHash, exists := expected[relativePath]
		if !exists {
			continue
		}
		path, err := service.scopedPath(relativePath)
		if err != nil {
			return err
		}
		contents, err := root.ReadFile(path)
		if err != nil {
			return apperr.Wrap(apperr.KindTool, "codetools.check_patch", err)
		}
		actual := sha256.Sum256(contents)
		if hex.EncodeToString(actual[:]) != expectedHash {
			return apperr.New(apperr.KindPolicy, "codetools.check_patch", "file changed since the patch was prepared: "+relativePath)
		}
	}
	return nil
}

func validatePatch(ctx context.Context, patch string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(patch) == "" || (!strings.Contains(patch, "*** Add File:") && !strings.Contains(patch, "*** Update File:") && !strings.Contains(patch, "*** Delete File:")) {
		return apperr.New(apperr.KindUsage, "codetools.check_patch", "patch must contain a supported file operation")
	}
	return nil
}

func operationPaths(operations []patchOperation) []string {
	paths := make([]string, 0, len(operations))
	for _, operation := range operations {
		paths = append(paths, operation.path)
	}
	return paths
}

func parsePatchOperations(patch string) ([]patchOperation, error) {
	lines := strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n")
	operations := make([]patchOperation, 0)
	for index := 0; index < len(lines); index++ {
		line := lines[index]
		switch {
		case strings.HasPrefix(line, "*** Add File: "):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Add File: "))
			patchLines, next := collectPatchLines(lines, index+1)
			content := addedContent(patchLines)
			operations = append(operations, patchOperation{path: path, content: content})
			index = next
		case strings.HasPrefix(line, "*** Update File: "):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Update File: "))
			patchLines, next := collectPatchLines(lines, index+1)
			operation := patchOperation{path: path, content: addedContent(patchLines)}
			if containsUpdateHunk(patchLines) {
				operation.updateLines = patchLines
			}
			operations = append(operations, operation)
			index = next
		case strings.HasPrefix(line, "*** Delete File: "):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File: "))
			operations = append(operations, patchOperation{path: path, delete: true})
		}
	}
	if len(operations) == 0 {
		return nil, apperr.New(apperr.KindUsage, "codetools.check_patch", "no file operations found")
	}
	return operations, nil
}

func collectPatchLines(lines []string, start int) ([]string, int) {
	patchLines := make([]string, 0)
	index := start
	for ; index < len(lines); index++ {
		if strings.HasPrefix(lines[index], "*** ") {
			break
		}
		patchLines = append(patchLines, lines[index])
	}
	return patchLines, index - 1
}

func addedContent(lines []string) string {
	content := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			content = append(content, strings.TrimPrefix(line, "+"))
		}
	}
	if len(content) == 0 {
		return ""
	}
	return strings.Join(content, "\n") + "\n"
}

func containsUpdateHunk(lines []string) bool {
	for _, line := range lines {
		if strings.HasPrefix(line, "@@") || strings.HasPrefix(line, "-") || strings.HasPrefix(line, " ") {
			return true
		}
	}
	return false
}

func applyUpdateLines(existing string, patchLines []string) (string, error) {
	hadTrailingNewline := strings.HasSuffix(existing, "\n")
	existing = strings.TrimSuffix(existing, "\n")
	oldLines := []string{}
	if existing != "" {
		oldLines = strings.Split(existing, "\n")
	}
	updated := make([]string, 0, len(oldLines))
	cursor := 0
	for _, line := range patchLines {
		if isHunkHeader(line) {
			continue
		}
		prefix, payload := patchLineParts(line)
		if prefix == '+' {
			updated = append(updated, payload)
			continue
		}
		match := findPatchLine(oldLines, cursor, payload)
		if match < 0 {
			return "", apperr.New(apperr.KindTool, "codetools.apply_patch", "patch context does not match the current file")
		}
		updated = append(updated, oldLines[cursor:match]...)
		if prefix == ' ' {
			updated = append(updated, payload)
		}
		cursor = match + 1
	}
	updated = append(updated, oldLines[cursor:]...)
	result := strings.Join(updated, "\n")
	if hadTrailingNewline || len(updated) > 0 {
		result += "\n"
	}
	return result, nil
}

func isHunkHeader(line string) bool {
	return line == "" || strings.HasPrefix(line, "@@")
}

func patchLineParts(line string) (byte, string) {
	if len(line) == 0 {
		return ' ', ""
	}
	prefix := line[0]
	if prefix == '+' || prefix == '-' || prefix == ' ' {
		return prefix, line[1:]
	}
	return ' ', line
}

func findPatchLine(lines []string, start int, value string) int {
	for index := start; index < len(lines); index++ {
		if lines[index] == value {
			return index
		}
	}
	return -1
}

func (service *Service) scopedPath(path string) (string, error) {
	if path == "" || filepath.IsAbs(path) {
		return "", apperr.New(apperr.KindTool, "codetools.path", "a workspace-relative path is required")
	}
	cleanPath := filepath.Clean(filepath.FromSlash(path))
	if cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
		return "", apperr.New(apperr.KindTool, "codetools.path", "path escapes the workspace")
	}
	return filepath.ToSlash(cleanPath), nil
}
