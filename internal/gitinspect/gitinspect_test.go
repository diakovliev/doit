package gitinspect

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diakovliev/doit/internal/tools"
)

func TestGitInspectionReadsFixtureRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := newGitFixture(t)
	service, err := New(root)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	requireGitStatus(t, service)
	requireGitDiff(t, service)
	requireGitLog(t, service)
	requireGitShow(t, service)
}

func newGitFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test User")
	filePath := filepath.Join(root, "README.md")
	if err := os.WriteFile(filePath, []byte("hello\n"), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	entryPath := filepath.Join(root, "entry.txt")
	if err := os.WriteFile(entryPath, []byte("entrypoint\n"), 0600); err != nil {
		t.Fatalf("write entry fixture: %v", err)
	}
	runGit(t, root, "add", "README.md", "entry.txt")
	runGit(t, root, "commit", "-m", "initial")
	if err := os.WriteFile(filePath, []byte("hello\nchanged\n"), 0600); err != nil {
		t.Fatalf("update fixture: %v", err)
	}
	return root
}

func requireGitStatus(t *testing.T, service *Service) {
	t.Helper()
	status, err := service.Status(context.Background(), StatusRequest{})
	if err != nil || len(status.Entries) != 1 || status.Entries[0].Path != "README.md" {
		t.Fatalf("unexpected status: %+v, error=%v", status, err)
	}
}

func requireGitDiff(t *testing.T, service *Service) {
	t.Helper()
	diff, err := service.Diff(context.Background(), DiffRequest{Source: "worktree"})
	if err != nil || diff.Diff == "" {
		t.Fatalf("unexpected diff: %+v, error=%v", diff, err)
	}
}

func requireGitLog(t *testing.T, service *Service) {
	t.Helper()
	logResponse, err := service.Log(context.Background(), LogRequest{Limit: 1})
	if err != nil || len(logResponse.Commits) != 1 {
		t.Fatalf("unexpected log: %+v, error=%v", logResponse, err)
	}
}

func requireGitShow(t *testing.T, service *Service) {
	t.Helper()
	show, err := service.Show(context.Background(), ShowRequest{Revision: "HEAD", MaxBytes: 1000})
	if err != nil || show.Content == "" {
		t.Fatalf("unexpected show: %+v, error=%v", show, err)
	}
}

func TestGitInspectionRejectsPathEscape(t *testing.T) {
	service, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if _, err := service.Diff(context.Background(), DiffRequest{Paths: []string{"../outside"}}); err == nil {
		t.Fatal("expected path escape to fail")
	}
}

func TestGitLocalMutationsAreWorkspaceScoped(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := newGitFixture(t)
	service, err := New(root)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	commit := commitGitFixtureChange(t, service)
	if commit.Hash == "" {
		t.Fatalf("commit did not return a hash: %+v", commit)
	}
	restoreGitFixtureChange(t, service, root)
	contents := readGitFile(t, root, "README.md")
	if string(contents) != "hello\nchanged\n" {
		t.Fatalf("unexpected restored content: %q", contents)
	}
}

func TestGitCommitReportsCurrentPathsWhenSelectionIsClean(t *testing.T) {
	root := newGitFixture(t)
	service, err := New(root)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	_, err = service.Commit(context.Background(), CommitRequest{Message: "wrong selection", Paths: []string{"entry.txt"}})
	if err == nil || !strings.Contains(err.Error(), "README.md") {
		t.Fatalf("expected current changed path diagnostic, error=%v", err)
	}
}

func TestGitToolsRegisterLocalMutationCapabilities(t *testing.T) {
	service, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	registry := tools.NewRegistry()
	if err := RegisterTools(registry, service); err != nil {
		t.Fatalf("register Git tools: %v", err)
	}
	for _, name := range []string{"git.stage", "git.unstage", "git.commit", "git.restore"} {
		tool, exists := registry.Lookup(name)
		if !exists {
			t.Fatalf("Git tool is not registered: %s", name)
		}
		if len(tool.Definition().Parameters) == 0 {
			t.Fatalf("Git tool has no model schema: %s", name)
		}
	}
}

func commitGitFixtureChange(t *testing.T, service *Service) CommitResponse {
	t.Helper()
	commit, err := service.Commit(context.Background(), CommitRequest{Message: "update README", Paths: []string{"README.md"}})
	if err != nil {
		t.Fatalf("commit file: %v", err)
	}
	return commit
}

func restoreGitFixtureChange(t *testing.T, service *Service, root string) {
	t.Helper()
	filePath := filepath.Join(root, "README.md")
	if err := os.WriteFile(filePath, []byte("second change\n"), 0600); err != nil {
		t.Fatalf("write second change: %v", err)
	}
	if _, err := service.Stage(context.Background(), StageRequest{Paths: []string{"README.md"}}); err != nil {
		t.Fatalf("stage second change: %v", err)
	}
	if _, err := service.Unstage(context.Background(), UnstageRequest{Paths: []string{"README.md"}}); err != nil {
		t.Fatalf("unstage second change: %v", err)
	}
	if _, err := service.Stage(context.Background(), StageRequest{Paths: []string{"README.md"}}); err != nil {
		t.Fatalf("restage second change: %v", err)
	}
	if _, err := service.Restore(context.Background(), RestoreRequest{Mode: "head", Paths: []string{"README.md"}}); err != nil {
		t.Fatalf("restore HEAD: %v", err)
	}
}

func readGitFile(t *testing.T, root, path string) []byte {
	t.Helper()
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		t.Fatalf("open fixture root: %v", err)
	}
	contents, err := rootHandle.ReadFile(path)
	_ = rootHandle.Close()
	if err != nil {
		t.Fatalf("read fixture file: %v", err)
	}
	return contents
}

func runGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal("find git:", err)
	}
	command := &exec.Cmd{Path: gitPath, Args: append([]string{gitPath}, arguments...), Dir: root}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}
