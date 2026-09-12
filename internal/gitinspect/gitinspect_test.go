package gitinspect

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
	runGit(t, root, "add", "README.md")
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
