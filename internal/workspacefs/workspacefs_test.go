package workspacefs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diakovliev/doit/internal/tools"
)

func TestReadSearchHashAndListStayBounded(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFile(t, filepath.Join(root, "source.txt"), "source\nentrypoint\n")
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
	response, err := service.Read(ctx, ReadRequest{Path: "source.txt", MaxBytes: 12})
	if err != nil || !response.Truncated || !strings.Contains(response.Content, "source") {
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

func TestMkdirAndRemoveDirectoryTree(t *testing.T) {
	root := t.TempDir()
	service, err := New(root)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	created, err := service.Mkdir(context.Background(), MkdirRequest{Path: "nested/dir", Parents: true})
	if err != nil || created.Path != "nested/dir" {
		t.Fatalf("unexpected mkdir response: %+v, error=%v", created, err)
	}
	writeWorkspaceFile(t, filepath.Join(root, "nested", "dir", "file.txt"), "content")
	if _, err := service.Remove(context.Background(), RemoveRequest{Path: "nested/dir"}); err == nil {
		t.Fatal("expected non-recursive directory removal to fail")
	}
	removed, err := service.Remove(context.Background(), RemoveRequest{Path: "nested", Recursive: true})
	if err != nil || removed.Path != "nested" || !removed.Recursive {
		t.Fatalf("unexpected remove response: %+v, error=%v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(root, "nested")); !os.IsNotExist(err) {
		t.Fatalf("directory tree still exists: %v", err)
	}
}

func TestWriteAndMoveFilesAndDirectories(t *testing.T) {
	root := t.TempDir()
	service, err := New(root)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	writeAndMoveFile(t, service)
	writeAndMoveDirectory(t, service, root)
}

func writeAndMoveFile(t *testing.T, service *Service) {
	t.Helper()
	written, err := service.Write(context.Background(), WriteRequest{Path: "nested/file.txt", Content: "first", Parents: true})
	if err != nil || !written.Created || written.Bytes != 5 {
		t.Fatalf("unexpected write response: %+v, error=%v", written, err)
	}
	if _, err := service.Write(context.Background(), WriteRequest{Path: "nested/file.txt", Content: "second"}); err == nil {
		t.Fatal("expected overwrite protection")
	}
	written, err = service.Write(context.Background(), WriteRequest{Path: "nested/file.txt", Content: "second", Overwrite: true})
	if err != nil || !written.Overwrote {
		t.Fatalf("unexpected overwrite response: %+v, error=%v", written, err)
	}
	if _, err := service.Move(context.Background(), MoveRequest{From: "nested/file.txt", To: "nested/moved.txt"}); err != nil {
		t.Fatalf("move file: %v", err)
	}
}

func writeAndMoveDirectory(t *testing.T, service *Service, root string) {
	t.Helper()
	if _, err := service.Mkdir(context.Background(), MkdirRequest{Path: "tree"}); err != nil {
		t.Fatalf("make directory: %v", err)
	}
	if _, err := service.Write(context.Background(), WriteRequest{Path: "tree/file.txt", Content: "tree file"}); err != nil {
		t.Fatalf("write directory file: %v", err)
	}
	if _, err := service.Move(context.Background(), MoveRequest{From: "tree", To: "moved-tree"}); err != nil {
		t.Fatalf("move directory: %v", err)
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		t.Fatalf("open moved workspace: %v", err)
	}
	contents, err := rootHandle.ReadFile("moved-tree/file.txt")
	_ = rootHandle.Close()
	if err != nil || string(contents) != "tree file" {
		t.Fatalf("unexpected moved directory content: %q, error=%v", contents, err)
	}
}

func TestMkdirAndRemoveRejectWorkspaceRootAndGitMetadata(t *testing.T) {
	service, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if _, err := service.Mkdir(context.Background(), MkdirRequest{Path: "."}); err == nil {
		t.Fatal("expected workspace root mkdir to fail")
	}
	if _, err := service.Remove(context.Background(), RemoveRequest{Path: ".git", Recursive: true}); err == nil {
		t.Fatal("expected Git metadata removal to fail")
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
	if len(registry.Definitions()) != 9 {
		t.Fatalf("expected nine filesystem tools, got %d", len(registry.Definitions()))
	}
	for _, name := range []string{"fs.write", "fs.move", "fs.mkdir", "fs.remove"} {
		tool, exists := registry.Lookup(name)
		if !exists || len(tool.Definition().Parameters) == 0 {
			t.Fatalf("directory tool is not registered with a schema: %s", name)
		}
	}
}

func TestInspectionToolSchemasDeclareRequiredProperties(t *testing.T) {
	service, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	registry := tools.NewRegistry()
	if err := RegisterTools(registry, service); err != nil {
		t.Fatalf("register filesystem tools: %v", err)
	}

	expected := map[string]string{
		"fs.hash":   "path",
		"fs.read":   "path",
		"fs.search": "query",
		"fs.stat":   "path",
	}
	for name, property := range expected {
		tool, exists := registry.Lookup(name)
		if !exists {
			t.Fatalf("inspection tool is not registered: %s", name)
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		if err := json.Unmarshal(tool.Definition().Parameters, &schema); err != nil {
			t.Fatalf("decode %s schema: %v", name, err)
		}
		if _, ok := schema.Properties[property]; !ok {
			t.Fatalf("%s schema does not declare required property %q: %s", name, property, tool.Definition().Parameters)
		}
		for _, required := range schema.Required {
			if _, ok := schema.Properties[required]; !ok {
				t.Fatalf("%s schema requires undeclared property %q", name, required)
			}
		}
	}
}

func TestListSchemaDeclaresEmptyProperties(t *testing.T) {
	service, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	registry := tools.NewRegistry()
	if err := RegisterTools(registry, service); err != nil {
		t.Fatalf("register filesystem tools: %v", err)
	}

	tool, exists := registry.Lookup("fs.list")
	if !exists {
		t.Fatal("list tool is not registered")
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(tool.Definition().Parameters, &schema); err != nil {
		t.Fatalf("decode fs.list schema: %v", err)
	}
	if schema.Properties == nil {
		t.Fatalf("fs.list schema does not declare an empty properties object: %s", tool.Definition().Parameters)
	}
}

func writeWorkspaceFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}
}
