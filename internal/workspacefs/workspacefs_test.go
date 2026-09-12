package workspacefs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diakovliev/doit/internal/tools"
)

func TestReadSearchHashAndListStayBounded(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFile(t, filepath.Join(root, "main.go"), "package main\nfunc main() {}\n")
	writeWorkspaceFile(t, filepath.Join(root, "notes.txt"), "find-me\nsecond line\n")
	writeWorkspaceFile(t, filepath.Join(root, ".gitignore"), "ignored.txt\n")
	writeWorkspaceFile(t, filepath.Join(root, "ignored.txt"), "find-me\n")

	service, err := New(root)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	ctx := context.Background()
	requireRead(ctx, t, service)
	requireSearch(ctx, t, service)
	requireList(ctx, t, service)
	requireHash(ctx, t, service)
}

func requireRead(ctx context.Context, t *testing.T, service *Service) {
	t.Helper()
	response, err := service.Read(ctx, ReadRequest{Path: "main.go", MaxBytes: 12})
	if err != nil || !response.Truncated || !strings.Contains(response.Content, "package") {
		t.Fatalf("unexpected read response: %+v, error=%v", response, err)
	}
}

func requireSearch(ctx context.Context, t *testing.T, service *Service) {
	t.Helper()
	response, err := service.Search(ctx, SearchRequest{Query: "find-me", MaxResults: 10})
	if err != nil || len(response.Matches) != 1 || response.Matches[0].Path != "notes.txt" {
		t.Fatalf("unexpected search response: %+v, error=%v", response, err)
	}
}

func requireList(ctx context.Context, t *testing.T, service *Service) {
	t.Helper()
	response, err := service.List(ctx, ListRequest{MaxEntries: 1})
	if err != nil || !response.Truncated || len(response.Entries) != 1 {
		t.Fatalf("unexpected list response: %+v, error=%v", response, err)
	}
}

func requireHash(ctx context.Context, t *testing.T, service *Service) {
	t.Helper()
	response, err := service.Hash(ctx, HashRequest{Path: "notes.txt"})
	if err != nil || response.SHA256 == "" || response.Bytes == 0 {
		t.Fatalf("unexpected hash response: %+v, error=%v", response, err)
	}
}

func TestRejectsWorkspaceEscape(t *testing.T) {
	service, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if _, err := service.Read(context.Background(), ReadRequest{Path: "../outside.txt"}); err == nil {
		t.Fatal("expected workspace escape to fail")
	}
}

func TestRegisterTools(t *testing.T) {
	service, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	registry := tools.NewRegistry()
	if err := RegisterTools(registry, service); err != nil {
		t.Fatalf("register filesystem tools: %v", err)
	}
	if len(registry.Definitions()) != 5 {
		t.Fatalf("expected five filesystem tools, got %d", len(registry.Definitions()))
	}
}

func writeWorkspaceFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}
}
