package codetools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPatchPreviewAndApply(t *testing.T) {
	root, service := newCodeService(t)
	patch := "*** Update File: file.txt\n+new\n"
	preview := requirePatchPreview(t, service, patch)
	result := requirePatchApply(t, service, patch)
	assertPatchChangeSet(t, preview, result)
	contents := readCodeFixture(t, root)
	if string(contents) != "new\n" {
		t.Fatalf("unexpected file content: %q", contents)
	}
}

func requirePatchPreview(t *testing.T, service *Service, patch string) PatchResponse {
	t.Helper()
	preview, err := service.ApplyPatch(context.Background(), PatchRequest{Patch: patch, DryRun: true})
	if err != nil || preview.Applied || !preview.Changed || preview.ChangeSet == nil || preview.ChangeSet.State != "preview" {
		t.Fatalf("unexpected preview: %+v, error=%v", preview, err)
	}
	return preview
}

func requirePatchApply(t *testing.T, service *Service, patch string) PatchResponse {
	t.Helper()
	result, err := service.ApplyPatch(context.Background(), PatchRequest{Patch: patch})
	if err != nil || !result.Applied || result.ChangeSet == nil || result.ChangeSet.State != "applied" {
		t.Fatalf("unexpected apply result: %+v, error=%v", result, err)
	}
	return result
}

func assertPatchChangeSet(t *testing.T, preview, result PatchResponse) {
	t.Helper()
	if result.ChangeSet.ID != preview.ChangeSet.ID {
		t.Fatalf("preview/apply change-set IDs differ: %q != %q", preview.ChangeSet.ID, result.ChangeSet.ID)
	}
	if result.ChangeSet.BeforeHashes["file.txt"] == "" || result.ChangeSet.AfterHashes["file.txt"] == "" || result.ChangeSet.BeforeHashes["file.txt"] == result.ChangeSet.AfterHashes["file.txt"] {
		t.Fatalf("unexpected change-set hashes: %+v", result.ChangeSet)
	}
}

func newCodeService(t *testing.T) (string, *Service) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("old\n"), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	service, err := New(root, nil)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return root, service
}

func readCodeFixture(t *testing.T, root string) []byte {
	t.Helper()
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		t.Fatalf("open fixture root: %v", err)
	}
	contents, err := rootHandle.ReadFile("file.txt")
	_ = rootHandle.Close()
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return contents
}

func TestPatchRejectsHashConflictAndPathEscape(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "file.txt")
	if err := os.WriteFile(filePath, []byte("old\n"), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	service, err := New(root, nil)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if _, err := service.ApplyPatch(context.Background(), PatchRequest{Patch: "*** Update File: file.txt\n+new\n", ExpectedHashes: map[string]string{"file.txt": strings.Repeat("0", 64)}}); err == nil {
		t.Fatal("expected hash conflict")
	}
	if _, err := service.Rename(context.Background(), RenameRequest{From: "../file.txt", To: "out.txt"}); err == nil {
		t.Fatal("expected rename path escape")
	}
}

func TestRenameRejectsSymlinkEscape(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, "linked")); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "source.txt"), []byte("content"), 0600); err != nil {
		t.Fatalf("write source fixture: %v", err)
	}
	service, err := New(workspace, nil)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if _, err := service.Rename(context.Background(), RenameRequest{From: "source.txt", To: "linked/moved.txt"}); err == nil {
		t.Fatal("expected rename through an escaping symlink to fail")
	}
	if _, err := os.Stat(filepath.Join(outside, "moved.txt")); !os.IsNotExist(err) {
		t.Fatalf("rename escaped workspace: %v", err)
	}
}

func TestRenameReturnsChangeSetEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "before.txt"), []byte("content\n"), 0600); err != nil {
		t.Fatalf("write source fixture: %v", err)
	}
	service, err := New(root, nil)
	if err != nil {
		t.Fatalf("new code service: %v", err)
	}
	result, err := service.Rename(context.Background(), RenameRequest{From: "before.txt", To: "after.txt"})
	if err != nil || result.ChangeSet == nil || result.ChangeSet.Operation != "code.rename" || result.ChangeSet.State != "applied" {
		t.Fatalf("rename change set missing: %+v, error=%v", result, err)
	}
	if result.ChangeSet.BeforeHashes["before.txt"] == "" || result.ChangeSet.AfterHashes["after.txt"] == "" {
		t.Fatalf("rename hashes missing: %+v", result.ChangeSet)
	}
}

func TestPatchUpdatePreservesUnchangedContent(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "file.txt")
	if err := os.WriteFile(filePath, []byte("old\nsecond\n"), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	service, err := New(root, nil)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	patch := "*** Begin Patch\n*** Update File: file.txt\n@@\n-old\n+new\n*** End Patch\n"
	if _, err := service.ApplyPatch(context.Background(), PatchRequest{Patch: patch}); err != nil {
		t.Fatalf("apply hunk patch: %v", err)
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		t.Fatalf("open fixture root: %v", err)
	}
	contents, err := rootHandle.ReadFile("file.txt")
	_ = rootHandle.Close()
	if err != nil || string(contents) != "new\nsecond\n" {
		t.Fatalf("unexpected updated content: %q, error=%v", contents, err)
	}
}

func TestPatchReportsAndSkipsNoOp(t *testing.T) {
	root, service := newCodeService(t)
	patch := "*** Update File: file.txt\n+old\n"
	preview, err := service.CheckPatch(context.Background(), PatchRequest{Patch: patch})
	if err != nil {
		t.Fatalf("check no-op patch: %v", err)
	}
	if preview.Changed {
		t.Fatal("expected no-op patch to report no change")
	}
	result, err := service.ApplyPatch(context.Background(), PatchRequest{Patch: patch})
	if err != nil {
		t.Fatalf("apply no-op patch: %v", err)
	}
	if result.Changed {
		t.Fatal("expected applied no-op patch to report no change")
	}
	if contents := readCodeFixture(t, root); string(contents) != "old\n" {
		t.Fatalf("unexpected no-op content: %q", contents)
	}
}

func TestPatchRollsBackEarlierOperationsWhenLaterOperationFails(t *testing.T) {
	root := t.TempDir()
	service, err := New(root, nil)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		t.Fatalf("open fixture root: %v", err)
	}
	err = service.applyOperations(rootHandle, []patchOperation{
		{path: "created.txt", content: "created\n"},
		{path: "missing.txt", delete: true},
	})
	_ = rootHandle.Close()
	if err == nil {
		t.Fatal("expected second operation to fail")
	}
	if _, err := os.Stat(filepath.Join(root, "created.txt")); !os.IsNotExist(err) {
		t.Fatalf("earlier operation was not rolled back: %v", err)
	}
}
