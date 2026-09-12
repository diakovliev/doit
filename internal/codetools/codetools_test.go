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
	preview, err := service.ApplyPatch(context.Background(), PatchRequest{Patch: patch, DryRun: true})
	if err != nil || preview.Applied || !preview.Changed {
		t.Fatalf("unexpected preview: %+v, error=%v", preview, err)
	}
	result, err := service.ApplyPatch(context.Background(), PatchRequest{Patch: patch})
	if err != nil || !result.Applied {
		t.Fatalf("unexpected apply result: %+v, error=%v", result, err)
	}
	contents := readCodeFixture(t, root)
	if err != nil || string(contents) != "new\n" {
		t.Fatalf("unexpected file content: %q, error=%v", contents, err)
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
