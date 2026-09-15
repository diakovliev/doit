package codetools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/diakovliev/doit/internal/tools"
)

func TestStructuredEditsApplyExactChangesWithChangeSets(t *testing.T) {
	root, service := newStructuredEditService(t, "package main\n\nfunc main() {}\n")
	hash := hashContent(t, readStructuredEditFile(t, root))

	preview := requireStructuredPreview(t, service, hash)

	requireStructuredReplacement(t, service, hash, preview)
	if !strings.Contains(readStructuredEditFile(t, root), "println(1)") {
		t.Fatalf("replacement was not applied: %q", readStructuredEditFile(t, root))
	}

	requireStructuredInsertion(t, service, root)

	requireStructuredDeletion(t, service, root)
}

func requireStructuredPreview(t *testing.T, service *Service, hash string) StructuredEditResponse {
	t.Helper()
	preview, err := service.ReplaceExact(context.Background(), ExactReplaceRequest{Path: "main.go", OldText: "func main() {}", NewText: "func main() { println(1) }", ExpectedHash: hash, DryRun: true})
	if err != nil || preview.Applied || !preview.Changed || preview.ChangeSet == nil || preview.ChangeSet.State != "preview" {
		t.Fatalf("unexpected exact replacement preview: %+v, error=%v", preview, err)
	}
	return preview
}

func requireStructuredReplacement(t *testing.T, service *Service, hash string, preview StructuredEditResponse) StructuredEditResponse {
	t.Helper()
	result, err := service.ReplaceExact(context.Background(), ExactReplaceRequest{Path: "main.go", OldText: "func main() {}", NewText: "func main() { println(1) }", ExpectedHash: hash})
	if err != nil || !result.Applied || result.ChangeSet == nil || result.ChangeSet.State != "applied" || result.ChangeSet.ID != preview.ChangeSet.ID {
		t.Fatalf("unexpected exact replacement result: %+v, error=%v", result, err)
	}
	return result
}

func requireStructuredInsertion(t *testing.T, service *Service, root string) {
	t.Helper()
	insert, err := service.InsertAtAnchor(context.Background(), AnchorInsertRequest{Path: "main.go", Anchor: "package main\n", Position: "after", Content: "\n// generated\n"})
	if err != nil || !insert.Applied || !strings.Contains(readStructuredEditFile(t, root), "// generated") {
		t.Fatalf("anchor insertion failed: %+v, error=%v", insert, err)
	}
}

func requireStructuredDeletion(t *testing.T, service *Service, root string) {
	t.Helper()
	deleted, err := service.DeleteExact(context.Background(), ExactDeleteRequest{Path: "main.go", Text: "// generated\n"})
	if err != nil || !deleted.Applied || strings.Contains(readStructuredEditFile(t, root), "// generated") {
		t.Fatalf("exact deletion failed: %+v, error=%v", deleted, err)
	}
}

func TestStructuredEditsRejectAmbiguousAndStaleChanges(t *testing.T) {
	root, service := newStructuredEditService(t, "same\nsame\n")
	if _, err := service.ReplaceExact(context.Background(), ExactReplaceRequest{Path: "main.go", OldText: "same", NewText: "changed"}); err == nil {
		t.Fatal("expected ambiguous replacement to fail")
	}
	if contents := readStructuredEditFile(t, root); contents != "same\nsame\n" {
		t.Fatalf("ambiguous edit changed file: %q", contents)
	}

	hash := hashContent(t, readStructuredEditFile(t, root))
	writeStructuredEditFile(t, root, "changed\n")
	if _, err := service.DeleteExact(context.Background(), ExactDeleteRequest{Path: "main.go", Text: "changed", ExpectedHash: hash}); err == nil {
		t.Fatal("expected stale hash to fail")
	}
}

func TestStructuredEditToolsAreRegistered(t *testing.T) {
	_, service := newStructuredEditService(t, "content\n")
	registry := tools.NewRegistry()
	if err := RegisterTools(registry, service); err != nil {
		t.Fatalf("register code tools: %v", err)
	}
	for _, name := range []string{"code.replace_exact", "code.insert_at_anchor", "code.delete_exact"} {
		tool, exists := registry.Lookup(name)
		if !exists || len(tool.Definition().Parameters) == 0 {
			t.Fatalf("structured edit tool missing schema: %s", name)
		}
	}
}

func newStructuredEditService(t *testing.T, content string) (string, *Service) {
	t.Helper()
	root := t.TempDir()
	writeStructuredEditFile(t, root, content)
	service, err := New(root, nil)
	if err != nil {
		t.Fatalf("new code service: %v", err)
	}
	return root, service
}

func readStructuredEditFile(t *testing.T, root string) string {
	t.Helper()
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		t.Fatalf("open fixture root: %v", err)
	}
	contents, err := rootHandle.ReadFile("main.go")
	_ = rootHandle.Close()
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(contents)
}

func writeStructuredEditFile(t *testing.T, root, content string) {
	t.Helper()
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		t.Fatalf("open fixture root: %v", err)
	}
	file, err := rootHandle.OpenFile("main.go", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err == nil {
		_, err = file.WriteString(content)
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
	}
	_ = rootHandle.Close()
	if err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func hashContent(t *testing.T, content string) string {
	t.Helper()
	contents := []byte(content)
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}
