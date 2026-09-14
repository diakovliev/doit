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

func TestSearchSupportsRegexCaseAndContext(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0700); err != nil {
		t.Fatalf("make nested fixture: %v", err)
	}
	writeWorkspaceFile(t, filepath.Join(root, "nested", "source.go"), "before\nTODO: FixThing\nafter\n")
	service, err := New(root)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	caseInsensitive := false
	response, err := service.Search(context.Background(), SearchRequest{Query: `todo:\s+fixthing`, Mode: "regex", CaseSensitive: &caseInsensitive, Glob: "*.go", BeforeLines: 1, AfterLines: 1})
	if err != nil || len(response.Matches) != 1 {
		t.Fatalf("unexpected regex search response: %+v, error=%v", response, err)
	}
	match := response.Matches[0]
	if match.Line != 2 || len(match.Before) != 1 || match.Before[0] != "before" || len(match.After) != 1 || match.After[0] != "after" {
		t.Fatalf("unexpected search context: %+v", match)
	}
}

func TestListAndSearchNestedDocsPath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0700); err != nil {
		t.Fatalf("make docs directory: %v", err)
	}
	writeWorkspaceFile(t, filepath.Join(root, "docs", "design.md"), "Phase 5 design\n")
	writeWorkspaceFile(t, filepath.Join(root, "docs", "definition.md"), "Product definition\n")
	service, err := New(root)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	listing, err := service.List(context.Background(), ListRequest{Path: "docs", Recursive: true})
	assertDocsListing(t, listing, err)

	search, err := service.Search(context.Background(), SearchRequest{Path: "docs", Query: "definition", Glob: "*.md"})
	assertDocsSearch(t, search, err, "docs/definition.md")

	rootSearch, err := service.Search(context.Background(), SearchRequest{Query: "Phase", Glob: "docs/*.md"})
	assertDocsSearch(t, rootSearch, err, "docs/design.md")
}

func assertDocsListing(t *testing.T, listing ListResponse, err error) {
	t.Helper()
	if err != nil || len(listing.Entries) != 2 {
		t.Fatalf("unexpected docs listing: %+v, error=%v", listing, err)
	}
	byPath := make(map[string]bool, len(listing.Entries))
	for _, entry := range listing.Entries {
		byPath[entry.Path] = true
	}
	if !byPath["docs/design.md"] || !byPath["docs/definition.md"] {
		t.Fatalf("docs listing omitted expected files: %+v", listing.Entries)
	}
}

func assertDocsSearch(t *testing.T, search SearchResponse, err error, expectedPath string) {
	t.Helper()
	if err != nil || len(search.Matches) != 1 || search.Matches[0].Path != expectedPath {
		t.Fatalf("unexpected docs search: %+v, error=%v", search, err)
	}
}

func TestSearchRejectsUnknownMode(t *testing.T) {
	service, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if _, err := service.Search(context.Background(), SearchRequest{Query: "value", Mode: "glob"}); err == nil {
		t.Fatal("expected unknown search mode to fail")
	}
	negative := -1
	if _, err := service.Search(context.Background(), SearchRequest{Query: "value", BeforeLines: negative}); err == nil {
		t.Fatal("expected negative context lines to fail")
	}
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
	service := newWorkspaceService(t, root)
	createNestedDirectory(t, service, root)
	assertNonRecursiveRemoveFails(t, service)
	removed := removeNestedDirectory(t, service)
	assertRemovedDirectory(t, removed, root)
}

func newWorkspaceService(t *testing.T, root string) *Service {
	t.Helper()
	service, err := New(root)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return service
}

func createNestedDirectory(t *testing.T, service *Service, root string) {
	t.Helper()
	created, err := service.Mkdir(context.Background(), MkdirRequest{Path: "nested/dir", Parents: true})
	if err != nil || created.Path != "nested/dir" || created.ChangeSet == nil {
		t.Fatalf("unexpected mkdir response: %+v, error=%v", created, err)
	}
	writeWorkspaceFile(t, filepath.Join(root, "nested", "dir", "file.txt"), "content")
}

func assertNonRecursiveRemoveFails(t *testing.T, service *Service) {
	t.Helper()
	if _, err := service.Remove(context.Background(), RemoveRequest{Path: "nested/dir"}); err == nil {
		t.Fatal("expected non-recursive directory removal to fail")
	}
}

func removeNestedDirectory(t *testing.T, service *Service) RemoveResponse {
	t.Helper()
	removed, err := service.Remove(context.Background(), RemoveRequest{Path: "nested", Recursive: true})
	if err != nil || removed.Path != "nested" || !removed.Recursive || removed.ChangeSet == nil {
		t.Fatalf("unexpected remove response: %+v, error=%v", removed, err)
	}
	return removed
}

func assertRemovedDirectory(t *testing.T, removed RemoveResponse, root string) {
	t.Helper()
	if removed.ChangeSet.BeforeHashes["nested"] == "" || removed.ChangeSet.AfterHashes["nested"] != "" {
		t.Fatalf("unexpected remove change set: %+v", removed.ChangeSet)
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

func TestWriteProvidesChangeSet(t *testing.T) {
	root := t.TempDir()
	service, err := New(root)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	response, err := service.Write(context.Background(), WriteRequest{Path: "created.txt", Content: "content"})
	if err != nil || response.ChangeSet == nil {
		t.Fatalf("write did not return a change set: %+v, error=%v", response, err)
	}
	if response.ChangeSet.Operation != "fs.write" || response.ChangeSet.State != "applied" || response.ChangeSet.BeforeHashes["created.txt"] != "" || response.ChangeSet.AfterHashes["created.txt"] == "" {
		t.Fatalf("unexpected write change set: %+v", response.ChangeSet)
	}
}

func writeAndMoveFile(t *testing.T, service *Service) {
	t.Helper()
	writeInitialFile(t, service)
	assertOverwriteProtection(t, service)
	writeReplacementFile(t, service)
	assertMoveFile(t, service)
}

func writeInitialFile(t *testing.T, service *Service) {
	t.Helper()
	written, err := service.Write(context.Background(), WriteRequest{Path: "nested/file.txt", Content: "first", Parents: true})
	if err != nil || !written.Created || written.Bytes != 5 {
		t.Fatalf("unexpected write response: %+v, error=%v", written, err)
	}
}

func assertOverwriteProtection(t *testing.T, service *Service) {
	t.Helper()
	if _, err := service.Write(context.Background(), WriteRequest{Path: "nested/file.txt", Content: "second"}); err == nil {
		t.Fatal("expected overwrite protection")
	}
}

func writeReplacementFile(t *testing.T, service *Service) {
	t.Helper()
	written, err := service.Write(context.Background(), WriteRequest{Path: "nested/file.txt", Content: "second", Overwrite: true})
	if err != nil || !written.Overwrote {
		t.Fatalf("unexpected overwrite response: %+v, error=%v", written, err)
	}
}

func assertMoveFile(t *testing.T, service *Service) {
	t.Helper()
	moved, err := service.Move(context.Background(), MoveRequest{From: "nested/file.txt", To: "nested/moved.txt"})
	if err != nil || moved.ChangeSet == nil {
		t.Fatalf("move file: %v", err)
	}
	if moved.ChangeSet.BeforeHashes["nested/file.txt"] == "" || moved.ChangeSet.AfterHashes["nested/file.txt"] != "" || moved.ChangeSet.AfterHashes["nested/moved.txt"] == "" {
		t.Fatalf("unexpected move change set: %+v", moved.ChangeSet)
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
	mkdirTool, _ := registry.Lookup("fs.mkdir")
	result := mkdirTool.Execute(context.Background(), tools.Call{Arguments: []byte(`{"path":"adapter-dir"}`)})
	if result.Status != tools.StatusSucceeded || result.ChangeSet == nil || result.ChangeSet.Operation != "fs.mkdir" {
		t.Fatalf("filesystem adapter did not expose change set: %+v", result)
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

func TestListSchemaDeclaresSupportedProperties(t *testing.T) {
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
	for _, property := range []string{"path", "recursive", "max_entries", "max_depth", "include_ignored"} {
		if _, ok := schema.Properties[property]; !ok {
			t.Fatalf("fs.list schema does not declare %q: %s", property, tool.Definition().Parameters)
		}
	}
}

func TestSearchAcceptsAllDeclaredArguments(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0700); err != nil {
		t.Fatalf("make docs directory: %v", err)
	}
	writeWorkspaceFile(t, filepath.Join(root, "docs", "plan.md"), "needle\n")
	service, err := New(root)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	registry := tools.NewRegistry()
	if err := RegisterTools(registry, service); err != nil {
		t.Fatalf("register filesystem tools: %v", err)
	}
	tool, exists := registry.Lookup("fs.search")
	if !exists {
		t.Fatal("search tool is not registered")
	}
	result := tool.Execute(context.Background(), tools.Call{Arguments: []byte(`{"query":"needle","path":"docs","glob":"*.md","mode":"literal","case_sensitive":true,"before_lines":0,"after_lines":0,"max_results":10,"include_ignored":false}`)})
	if result.Status != tools.StatusSucceeded {
		t.Fatalf("search rejected its declared arguments: %+v", result)
	}
}

func writeWorkspaceFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}
}
