package codetools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/diakovliev/doit/internal/apperr"
	"github.com/diakovliev/doit/internal/tools"
)

const maxStructuredEditBytes = 64 * 1024

// ExactReplaceRequest replaces one exact text occurrence in an existing file.
type ExactReplaceRequest struct {
	Path         string `json:"path"`
	OldText      string `json:"old_text"`
	NewText      string `json:"new_text"`
	ExpectedHash string `json:"expected_hash,omitempty"`
	DryRun       bool   `json:"dry_run"`
}

// AnchorInsertRequest inserts content immediately before or after one exact anchor.
type AnchorInsertRequest struct {
	Path         string `json:"path"`
	Anchor       string `json:"anchor"`
	Position     string `json:"position"`
	Content      string `json:"content"`
	ExpectedHash string `json:"expected_hash,omitempty"`
	DryRun       bool   `json:"dry_run"`
}

// ExactDeleteRequest deletes one exact text occurrence in an existing file.
type ExactDeleteRequest struct {
	Path         string `json:"path"`
	Text         string `json:"text"`
	ExpectedHash string `json:"expected_hash,omitempty"`
	DryRun       bool   `json:"dry_run"`
}

// StructuredEditResponse describes a deterministic edit preview or application.
type StructuredEditResponse struct {
	Operation  string           `json:"operation"`
	Path       string           `json:"path"`
	Position   string           `json:"position,omitempty"`
	MatchCount int              `json:"match_count"`
	Applied    bool             `json:"applied"`
	Changed    bool             `json:"changed"`
	Message    string           `json:"message"`
	ChangeSet  *tools.ChangeSet `json:"change_set,omitempty"`
}

type structuredEdit struct {
	operation string
	path      string
	position  string
	anchor    string
	oldText   string
	newText   string
	content   string
}

type structuredEditPlan struct {
	edit         structuredEdit
	before       []byte
	after        []byte
	beforeExists bool
	afterExists  bool
	matchCount   int
}

func (service *Service) ReplaceExact(ctx context.Context, request ExactReplaceRequest) (StructuredEditResponse, error) {
	return service.executeStructuredEdit(ctx, structuredEdit{
		operation: "code.replace_exact",
		path:      request.Path,
		oldText:   request.OldText,
		newText:   request.NewText,
	}, request.ExpectedHash, request.DryRun)
}

func (service *Service) InsertAtAnchor(ctx context.Context, request AnchorInsertRequest) (StructuredEditResponse, error) {
	return service.executeStructuredEdit(ctx, structuredEdit{
		operation: "code.insert_at_anchor",
		path:      request.Path,
		position:  request.Position,
		anchor:    request.Anchor,
		content:   request.Content,
	}, request.ExpectedHash, request.DryRun)
}

func (service *Service) DeleteExact(ctx context.Context, request ExactDeleteRequest) (StructuredEditResponse, error) {
	return service.executeStructuredEdit(ctx, structuredEdit{
		operation: "code.delete_exact",
		path:      request.Path,
		oldText:   request.Text,
	}, request.ExpectedHash, request.DryRun)
}

func (service *Service) executeStructuredEdit(ctx context.Context, edit structuredEdit, expectedHash string, dryRun bool) (StructuredEditResponse, error) {
	plan, err := service.prepareStructuredEdit(ctx, edit, expectedHash)
	if err != nil {
		return StructuredEditResponse{}, err
	}
	response, err := structuredEditResponse(plan, dryRun)
	if err != nil || dryRun {
		return response, err
	}
	return service.applyStructuredEditPlan(plan)
}

func (service *Service) applyStructuredEditPlan(plan structuredEditPlan) (StructuredEditResponse, error) {
	edit := plan.edit
	root, err := os.OpenRoot(service.workspace)
	if err != nil {
		return StructuredEditResponse{}, apperr.Wrap(apperr.KindTool, edit.operation, err)
	}
	defer func() { _ = root.Close() }()
	current, err := root.ReadFile(edit.path)
	if err != nil {
		return StructuredEditResponse{}, apperr.Wrap(apperr.KindTool, edit.operation, err)
	}
	if snapshotHash(current, true) != snapshotHash(plan.before, plan.beforeExists) {
		return StructuredEditResponse{}, apperr.New(apperr.KindPolicy, edit.operation, "file changed after the edit was prepared")
	}
	after, matchCount, err := applyStructuredEdit(string(current), edit)
	if err != nil {
		return StructuredEditResponse{}, err
	}
	if matchCount != plan.matchCount {
		return StructuredEditResponse{}, apperr.New(apperr.KindPolicy, edit.operation, "file changed after the edit was prepared")
	}
	if string(current) != after {
		if err := writeOperation(root, edit.path, after); err != nil {
			return StructuredEditResponse{}, err
		}
	}
	plan.after = []byte(after)
	plan.afterExists = true
	plan.matchCount = matchCount
	return structuredEditResponse(plan, false)
}

func (service *Service) prepareStructuredEdit(ctx context.Context, edit structuredEdit, expectedHash string) (structuredEditPlan, error) {
	if err := ctx.Err(); err != nil {
		return structuredEditPlan{}, err
	}
	path, err := service.scopedPath(edit.path)
	if err != nil {
		return structuredEditPlan{}, err
	}
	if err := validateStructuredEditContent(edit); err != nil {
		return structuredEditPlan{}, err
	}
	before, err := service.readStructuredEditBefore(edit, path, expectedHash)
	if err != nil {
		return structuredEditPlan{}, err
	}
	after, matchCount, err := applyStructuredEdit(string(before), edit)
	if err != nil {
		return structuredEditPlan{}, err
	}
	return structuredEditPlan{edit: edit, before: before, after: []byte(after), beforeExists: true, afterExists: true, matchCount: matchCount}, nil
}

func validateStructuredEditContent(edit structuredEdit) error {
	if len(edit.oldText) > maxStructuredEditBytes || len(edit.newText) > maxStructuredEditBytes || len(edit.anchor) > maxStructuredEditBytes || len(edit.content) > maxStructuredEditBytes {
		return apperr.New(apperr.KindUsage, edit.operation, "structured edit content exceeds the 64 KiB limit")
	}
	return nil
}

func (service *Service) readStructuredEditBefore(edit structuredEdit, path, expectedHash string) ([]byte, error) {
	root, err := os.OpenRoot(service.workspace)
	if err != nil {
		return nil, apperr.Wrap(apperr.KindTool, edit.operation, err)
	}
	defer func() { _ = root.Close() }()
	before, err := root.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, apperr.New(apperr.KindTool, edit.operation, "target file does not exist: "+edit.path)
		}
		return nil, apperr.Wrap(apperr.KindTool, edit.operation, err)
	}
	if expectedHash != "" && snapshotHash(before, true) != expectedHash {
		return nil, apperr.New(apperr.KindPolicy, edit.operation, "file changed since it was inspected: "+edit.path)
	}
	return before, nil
}

func applyStructuredEdit(content string, edit structuredEdit) (string, int, error) {
	switch edit.operation {
	case "code.replace_exact":
		return replaceExact(content, edit.oldText, edit.newText, edit.operation)
	case "code.delete_exact":
		return replaceExact(content, edit.oldText, "", edit.operation)
	case "code.insert_at_anchor":
		return insertAtAnchor(content, edit.anchor, edit.position, edit.content, edit.operation)
	default:
		return "", 0, apperr.New(apperr.KindInternal, "codetools.structured_edit", "unsupported structured edit")
	}
}

func replaceExact(content, oldText, newText, operation string) (string, int, error) {
	if oldText == "" {
		return "", 0, apperr.New(apperr.KindUsage, operation, "old_text or text is required")
	}
	matches := strings.Count(content, oldText)
	if matches != 1 {
		return "", matches, apperr.New(apperr.KindPolicy, operation, fmt.Sprintf("expected exactly one match, found %d", matches))
	}
	return strings.Replace(content, oldText, newText, 1), matches, nil
}

func insertAtAnchor(content, anchor, position, inserted, operation string) (string, int, error) {
	if anchor == "" || inserted == "" {
		return "", 0, apperr.New(apperr.KindUsage, operation, "anchor and content are required")
	}
	if position != "before" && position != "after" {
		return "", 0, apperr.New(apperr.KindUsage, operation, "position must be before or after")
	}
	matches := strings.Count(content, anchor)
	if matches != 1 {
		return "", matches, apperr.New(apperr.KindPolicy, operation, fmt.Sprintf("expected exactly one anchor match, found %d", matches))
	}
	index := strings.Index(content, anchor)
	if position == "after" {
		index += len(anchor)
	}
	return content[:index] + inserted + content[index:], matches, nil
}

func structuredEditResponse(plan structuredEditPlan, dryRun bool) (StructuredEditResponse, error) {
	changeSet, err := structuredEditChangeSet(plan, map[bool]string{true: "preview", false: "applied"}[dryRun])
	if err != nil {
		return StructuredEditResponse{}, err
	}
	changed := string(plan.before) != string(plan.after)
	message := "no content change"
	if changed {
		message = "exact structured edit is ready"
		if !dryRun {
			message = "exact structured edit applied"
		}
	}
	return StructuredEditResponse{Operation: plan.edit.operation, Path: plan.edit.path, Position: plan.edit.position, MatchCount: plan.matchCount, Applied: !dryRun, Changed: changed, Message: message, ChangeSet: changeSet}, nil
}

func structuredEditChangeSet(plan structuredEditPlan, state string) (*tools.ChangeSet, error) {
	path := plan.edit.path
	beforeHashes := map[string]string{path: snapshotHash(plan.before, plan.beforeExists)}
	afterHashes := map[string]string{path: snapshotHash(plan.after, plan.afterExists)}
	identity, err := json.Marshal(struct {
		Operation    string            `json:"operation"`
		Paths        []string          `json:"paths"`
		BeforeHashes map[string]string `json:"before_hashes"`
		AfterHashes  map[string]string `json:"after_hashes"`
	}{Operation: plan.edit.operation, Paths: []string{path}, BeforeHashes: beforeHashes, AfterHashes: afterHashes})
	if err != nil {
		return nil, apperr.Wrap(apperr.KindTool, "codetools.change_set", err)
	}
	digest := sha256.Sum256(identity)
	return &tools.ChangeSet{ID: hex.EncodeToString(digest[:]), Operation: plan.edit.operation, State: state, Paths: []string{path}, BeforeHashes: beforeHashes, AfterHashes: afterHashes}, nil
}

func renameChangeSet(paths []string, beforeFrom []byte, beforeFromExists bool, beforeTo []byte, beforeToExists bool, afterFrom []byte, afterFromExists bool, afterTo []byte, afterToExists bool) (*tools.ChangeSet, error) {
	if len(paths) != 2 {
		return nil, apperr.New(apperr.KindTool, "codetools.rename", "internal error: rename requires two paths")
	}
	beforeHashes := map[string]string{paths[0]: snapshotHash(beforeFrom, beforeFromExists), paths[1]: snapshotHash(beforeTo, beforeToExists)}
	afterHashes := map[string]string{paths[0]: snapshotHash(afterFrom, afterFromExists), paths[1]: snapshotHash(afterTo, afterToExists)}
	identity, err := json.Marshal(struct {
		Operation    string            `json:"operation"`
		Paths        []string          `json:"paths"`
		BeforeHashes map[string]string `json:"before_hashes"`
		AfterHashes  map[string]string `json:"after_hashes"`
	}{Operation: "code.rename", Paths: paths, BeforeHashes: beforeHashes, AfterHashes: afterHashes})
	if err != nil {
		return nil, apperr.Wrap(apperr.KindTool, "codetools.rename", err)
	}
	digest := sha256.Sum256(identity)
	return &tools.ChangeSet{ID: hex.EncodeToString(digest[:]), Operation: "code.rename", State: "applied", Paths: paths, BeforeHashes: beforeHashes, AfterHashes: afterHashes}, nil
}

func structuredEditChangedPaths(data any) []string {
	response, ok := data.(StructuredEditResponse)
	if !ok || !response.Changed {
		return nil
	}
	return []string{response.Path}
}

func structuredEditChangeSetFromData(data any) *tools.ChangeSet {
	response, ok := data.(StructuredEditResponse)
	if !ok {
		return nil
	}
	return response.ChangeSet
}
