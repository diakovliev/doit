// Package gitinspect exposes bounded, structured Git inspection.
package gitinspect

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/diakovliev/doit/internal/apperr"
	"github.com/diakovliev/doit/internal/tools"
)

// Service executes fixed, workspace-scoped Git commands within one workspace.
type Service struct {
	workspace string
	maxBytes  int
}

// New creates a Git inspection service.
func New(workspace string) (*Service, error) {
	absolutePath, err := filepath.Abs(workspace)
	if err != nil {
		return nil, apperr.Wrap(apperr.KindConfig, "gitinspect.workspace", err)
	}
	return &Service{workspace: absolutePath, maxBytes: 64 * 1024}, nil
}

// RootRequest controls repository-root discovery.
type RootRequest struct{}

// RootResponse describes the repository containing the workspace.
type RootResponse struct {
	Workspace    string `json:"workspace"`
	Repository   string `json:"repository"`
	DetachedHead bool   `json:"detached_head"`
	IsRepository bool   `json:"is_repository"`
}

// BranchRequest selects current branch metadata.
type BranchRequest struct{}

// BranchResponse describes the current branch and optional upstream state.
type BranchResponse struct {
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	Upstream   string `json:"upstream,omitempty"`
	Detached   bool   `json:"detached"`
	Ahead      int    `json:"ahead"`
	Behind     int    `json:"behind"`
}

// WorktreeRequest selects local worktree metadata.
type WorktreeRequest struct{}

// WorktreeEntry describes one local Git worktree.
type WorktreeEntry struct {
	Path   string `json:"path"`
	Head   string `json:"head"`
	Branch string `json:"branch,omitempty"`
	Bare   bool   `json:"bare"`
}

// WorktreeResponse contains the local worktree inventory.
type WorktreeResponse struct {
	Worktrees []WorktreeEntry `json:"worktrees"`
}

// Root discovers the containing Git repository and HEAD state.
func (service *Service) Root(ctx context.Context, _ RootRequest) (RootResponse, error) {
	repository, err := service.run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return RootResponse{}, err
	}
	detached := service.isDetached(ctx)
	return RootResponse{Workspace: service.workspace, Repository: strings.TrimSpace(repository), DetachedHead: detached, IsRepository: true}, nil
}

// Branch returns current branch and upstream divergence without changing Git state.
func (service *Service) Branch(ctx context.Context, _ BranchRequest) (BranchResponse, error) {
	root, err := service.Root(ctx, RootRequest{})
	if err != nil {
		return BranchResponse{}, err
	}
	branch, branchErr := service.run(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	response := BranchResponse{Repository: root.Repository, Branch: strings.TrimSpace(branch), Detached: branchErr != nil}
	if response.Detached {
		response.Branch = ""
		return response, nil
	}
	response.Upstream = service.upstream(ctx)
	if response.Upstream != "" {
		response.Ahead, response.Behind = service.divergence(ctx)
	}
	return response, nil
}

func (service *Service) upstream(ctx context.Context) string {
	upstream, err := service.run(ctx, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(upstream)
}

func (service *Service) divergence(ctx context.Context) (ahead, behind int) {
	counts, err := service.run(ctx, "rev-list", "--left-right", "--count", "HEAD...@{upstream}")
	if err != nil {
		return 0, 0
	}
	fields := strings.Fields(counts)
	if len(fields) != 2 {
		return 0, 0
	}
	return parseInt(fields[0]), parseInt(fields[1])
}

// Worktrees returns the local worktree inventory without changing Git state.
func (service *Service) Worktrees(ctx context.Context, _ WorktreeRequest) (WorktreeResponse, error) {
	output, err := service.run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return WorktreeResponse{}, err
	}
	return WorktreeResponse{Worktrees: parseWorktrees(output)}, nil
}

func (service *Service) isDetached(ctx context.Context) bool {
	detachedOutput, err := service.run(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	return err != nil || strings.TrimSpace(detachedOutput) == ""
}

// StatusRequest controls structured status output.
type StatusRequest struct {
	Path           string `json:"path"`
	IncludeIgnored bool   `json:"include_ignored"`
}

// StatusEntry describes one Git path state.
type StatusEntry struct {
	Index    string `json:"index"`
	Worktree string `json:"worktree"`
	Path     string `json:"path"`
	Original string `json:"original,omitempty"`
}

// StatusResponse contains structured porcelain status.
type StatusResponse struct {
	Repository string        `json:"repository"`
	Entries    []StatusEntry `json:"entries"`
}

// Status returns porcelain-v1 status for paths within the workspace.
func (service *Service) Status(ctx context.Context, request StatusRequest) (StatusResponse, error) {
	arguments := []string{"status", "--porcelain=v1", "-z"}
	if request.IncludeIgnored {
		arguments = append(arguments, "--ignored")
	}
	if request.Path != "" {
		path, err := service.scopedPath(request.Path)
		if err != nil {
			return StatusResponse{}, err
		}
		arguments = append(arguments, "--", path)
	}
	output, err := service.run(ctx, arguments...)
	if err != nil {
		return StatusResponse{}, err
	}
	root, err := service.Root(ctx, RootRequest{})
	if err != nil {
		return StatusResponse{}, err
	}
	return StatusResponse{Repository: root.Repository, Entries: parsePorcelainStatus(output)}, nil
}

// StatusSummary returns a compact fresh status suitable for model context.
func (service *Service) StatusSummary(ctx context.Context) (string, error) {
	status, err := service.Status(ctx, StatusRequest{})
	if err != nil {
		return "", err
	}
	if len(status.Entries) == 0 {
		return "clean", nil
	}
	lines := make([]string, 0, len(status.Entries))
	for _, entry := range status.Entries {
		lines = append(lines, entry.Index+entry.Worktree+" "+entry.Path)
	}
	return strings.Join(lines, "\n"), nil
}

// DiffRequest selects a bounded diff source.
type DiffRequest struct {
	Source           string   `json:"source"`
	Revision         string   `json:"revision"`
	Paths            []string `json:"paths"`
	MaxBytes         int      `json:"max_bytes"`
	IncludeUntracked bool     `json:"include_untracked"`
}

// DiffResponse contains a bounded textual diff.
type DiffResponse struct {
	Source    string `json:"source"`
	Revision  string `json:"revision,omitempty"`
	Diff      string `json:"diff"`
	Truncated bool   `json:"truncated"`
}

// Diff returns a working-tree, index, or revision-range diff.
func (service *Service) Diff(ctx context.Context, request DiffRequest) (DiffResponse, error) {
	arguments, err := service.diffArguments(request)
	if err != nil {
		return DiffResponse{}, err
	}
	diff, err := service.run(ctx, arguments...)
	if err != nil {
		return DiffResponse{}, err
	}
	maxBytes := request.MaxBytes
	if maxBytes <= 0 || maxBytes > service.maxBytes {
		maxBytes = service.maxBytes
	}
	if request.IncludeUntracked {
		if request.Source != "" && request.Source != "worktree" {
			return DiffResponse{}, apperr.New(apperr.KindUsage, "gitinspect.diff", "untracked files can only be included in a worktree diff")
		}
		untracked, untrackedErr := service.untrackedDiff(ctx, request.Paths, maxBytes-len(diff))
		if untrackedErr != nil {
			return DiffResponse{}, untrackedErr
		}
		diff += untracked
	}
	truncated := len(diff) > maxBytes
	if truncated {
		diff = diff[:maxBytes]
	}
	return DiffResponse{Source: request.Source, Revision: request.Revision, Diff: diff, Truncated: truncated}, nil
}

func (service *Service) untrackedDiff(ctx context.Context, requestedPaths []string, remaining int) (string, error) {
	if remaining <= 0 {
		return "", nil
	}
	paths, err := service.scopedPaths(requestedPaths)
	if err != nil {
		return "", err
	}
	arguments := []string{"ls-files", "--others", "--exclude-standard", "-z"}
	if len(paths) > 0 {
		arguments = append(arguments, "--")
		arguments = append(arguments, paths...)
	}
	output, err := service.run(ctx, arguments...)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(service.workspace)
	if err != nil {
		return "", apperr.Wrap(apperr.KindTool, "gitinspect.diff", err)
	}
	defer func() { _ = root.Close() }()
	var builder strings.Builder
	for _, path := range strings.Split(strings.TrimSuffix(output, "\x00"), "\x00") {
		if path == "" || builder.Len() >= remaining {
			break
		}
		patch, readErr := untrackedFilePatch(ctx, root, path, remaining-builder.Len())
		if readErr != nil {
			return "", readErr
		}
		builder.WriteString(patch)
	}
	return builder.String(), nil
}

func untrackedFilePatch(ctx context.Context, root *os.Root, path string, limit int) (string, error) {
	info, err := root.Stat(path)
	if err != nil {
		return "", apperr.Wrap(apperr.KindTool, "gitinspect.diff", err)
	}
	if info.IsDir() {
		return "", nil
	}
	file, err := root.Open(path)
	if err != nil {
		return "", apperr.Wrap(apperr.KindTool, "gitinspect.diff", err)
	}
	contents, readErr := io.ReadAll(io.LimitReader(file, int64(maxInt(0, limit))+1))
	closeErr := file.Close()
	if readErr != nil {
		return "", apperr.Wrap(apperr.KindTool, "gitinspect.diff", readErr)
	}
	if closeErr != nil {
		return "", apperr.Wrap(apperr.KindTool, "gitinspect.diff", closeErr)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	slashPath := filepath.ToSlash(path)
	var builder strings.Builder
	fmt.Fprintf(&builder, "diff --git a/%s b/%s\nnew file mode %o\n--- /dev/null\n+++ b/%s\n", slashPath, slashPath, info.Mode().Perm(), slashPath)
	if bytes.IndexByte(contents, 0) >= 0 {
		builder.WriteString("Binary files /dev/null and b/")
		builder.WriteString(slashPath)
		builder.WriteString(" differ\n")
		return builder.String(), nil
	}
	text := strings.TrimSuffix(string(contents), "\n")
	lines := []string{}
	if text != "" {
		lines = strings.Split(text, "\n")
	}
	fmt.Fprintf(&builder, "@@ -0,0 +1,%d @@\n", len(lines))
	for _, line := range lines {
		builder.WriteByte('+')
		builder.WriteString(line)
		builder.WriteByte('\n')
	}
	return builder.String(), nil
}

func (service *Service) diffArguments(request DiffRequest) ([]string, error) {
	arguments := []string{"diff", "--no-ext-diff", "--binary"}
	switch request.Source {
	case "worktree", "":
	case "index":
		arguments = append(arguments, "--cached")
	case "range":
		if request.Revision == "" {
			return nil, apperr.New(apperr.KindUsage, "gitinspect.diff", "revision is required for range diff")
		}
		arguments = append(arguments, request.Revision)
	default:
		return nil, apperr.New(apperr.KindUsage, "gitinspect.diff", "source must be worktree, index, or range")
	}
	paths, err := service.scopedPaths(request.Paths)
	if err != nil {
		return nil, err
	}
	if len(paths) > 0 {
		arguments = append(arguments, "--")
		arguments = append(arguments, paths...)
	}
	return arguments, nil
}

// LogRequest selects bounded commit metadata.
type LogRequest struct {
	Revision string   `json:"revision"`
	Paths    []string `json:"paths"`
	Limit    int      `json:"limit"`
}

// Commit describes one commit.
type Commit struct {
	Hash    string `json:"hash"`
	Author  string `json:"author"`
	Subject string `json:"subject"`
}

// LogResponse contains commit metadata.
type LogResponse struct {
	Commits []Commit `json:"commits"`
}

// Log returns bounded commit metadata.
func (service *Service) Log(ctx context.Context, request LogRequest) (LogResponse, error) {
	limit := request.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	arguments := []string{"log", "-n", strconv.Itoa(limit), "--format=%H%x00%an%x00%s%x00"}
	if request.Revision != "" {
		arguments = append(arguments, request.Revision)
	}
	paths, err := service.scopedPaths(request.Paths)
	if err != nil {
		return LogResponse{}, err
	}
	if len(paths) > 0 {
		arguments = append(arguments, "--")
		arguments = append(arguments, paths...)
	}
	output, err := service.run(ctx, arguments...)
	if err != nil {
		return LogResponse{}, err
	}
	parts := strings.Split(output, "\x00")
	response := LogResponse{Commits: make([]Commit, 0, limit)}
	for index := 0; index+2 < len(parts); index += 4 {
		if parts[index] == "" {
			continue
		}
		response.Commits = append(response.Commits, Commit{Hash: parts[index], Author: parts[index+1], Subject: parts[index+2]})
	}
	return response, nil
}

// ShowRequest selects one Git object.
type ShowRequest struct {
	Revision string `json:"revision"`
	MaxBytes int    `json:"max_bytes"`
}

// ShowResponse contains bounded object text.
type ShowResponse struct {
	Revision  string `json:"revision"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

// Show returns bounded output for an explicit revision or object.
func (service *Service) Show(ctx context.Context, request ShowRequest) (ShowResponse, error) {
	if request.Revision == "" {
		return ShowResponse{}, apperr.New(apperr.KindUsage, "gitinspect.show", "revision is required")
	}
	content, err := service.run(ctx, "show", "--no-ext-diff", request.Revision)
	if err != nil {
		return ShowResponse{}, err
	}
	maxBytes := request.MaxBytes
	if maxBytes <= 0 || maxBytes > service.maxBytes {
		maxBytes = service.maxBytes
	}
	truncated := len(content) > maxBytes
	if truncated {
		content = content[:maxBytes]
	}
	return ShowResponse{Revision: request.Revision, Content: content, Truncated: truncated}, nil
}

// BlameRequest selects a bounded file range.
type BlameRequest struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

// BlameLine contains line ownership metadata.
type BlameLine struct {
	Commit  string `json:"commit"`
	Author  string `json:"author"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

// BlameResponse contains bounded line ownership.
type BlameResponse struct {
	Lines []BlameLine `json:"lines"`
}

// Blame returns line ownership for a workspace file range.
func (service *Service) Blame(ctx context.Context, request BlameRequest) (BlameResponse, error) {
	path, err := service.scopedPath(request.Path)
	if err != nil {
		return BlameResponse{}, err
	}
	arguments := []string{"blame", "--line-porcelain"}
	if request.StartLine > 0 && request.EndLine >= request.StartLine {
		arguments = append(arguments, "-L", fmt.Sprintf("%d,%d", request.StartLine, request.EndLine))
	}
	arguments = append(arguments, "--", path)
	output, err := service.run(ctx, arguments...)
	if err != nil {
		return BlameResponse{}, err
	}
	return parseBlame(output), nil
}

// CheckIgnoreRequest checks one workspace path.
type CheckIgnoreRequest struct {
	Path string `json:"path"`
}

// CheckIgnoreResponse explains ignore state.
type CheckIgnoreResponse struct {
	Path    string `json:"path"`
	Ignored bool   `json:"ignored"`
}

// CheckIgnore returns whether Git ignores a workspace path.
func (service *Service) CheckIgnore(ctx context.Context, request CheckIgnoreRequest) (CheckIgnoreResponse, error) {
	path, err := service.scopedPath(request.Path)
	if err != nil {
		return CheckIgnoreResponse{}, err
	}
	// #nosec G204 -- git is fixed and path is workspace-scoped before execution.
	command := exec.CommandContext(ctx, "git", "check-ignore", "--quiet", "--", path)
	command.Dir = service.workspace
	err = command.Run()
	if err == nil {
		return CheckIgnoreResponse{Path: path, Ignored: true}, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return CheckIgnoreResponse{Path: path, Ignored: false}, nil
	}
	return CheckIgnoreResponse{}, apperr.Wrap(apperr.KindTool, "gitinspect.check_ignore", err)
}

// StageRequest selects workspace paths to add to the index.
type StageRequest struct {
	Paths []string `json:"paths"`
}

// StageResponse describes paths added to the index.
type StageResponse struct {
	Paths  []string `json:"paths"`
	Staged []string `json:"staged"`
}

// Stage adds explicitly selected workspace paths to the index.
func (service *Service) Stage(ctx context.Context, request StageRequest) (StageResponse, error) {
	paths, err := service.mutationPaths(request.Paths, "gitinspect.stage")
	if err != nil {
		return StageResponse{}, err
	}
	if err := service.stagePaths(ctx, paths); err != nil {
		return StageResponse{}, err
	}
	staged, err := service.stagedPaths(ctx, paths)
	if err != nil {
		return StageResponse{}, err
	}
	return StageResponse{Paths: paths, Staged: staged}, nil
}

// UnstageRequest selects workspace paths to remove from the index.
type UnstageRequest struct {
	Paths []string `json:"paths"`
}

// UnstageResponse describes paths removed from the index.
type UnstageResponse struct {
	Paths []string `json:"paths"`
}

// Unstage removes explicitly selected workspace paths from the index.
func (service *Service) Unstage(ctx context.Context, request UnstageRequest) (UnstageResponse, error) {
	paths, err := service.mutationPaths(request.Paths, "gitinspect.unstage")
	if err != nil {
		return UnstageResponse{}, err
	}
	arguments := append([]string{"reset", "--"}, paths...)
	if _, err := service.run(ctx, arguments...); err != nil {
		return UnstageResponse{}, err
	}
	return UnstageResponse{Paths: paths}, nil
}

// CommitRequest creates a commit from explicitly selected staged paths.
type CommitRequest struct {
	Message string   `json:"message"`
	Paths   []string `json:"paths"`
}

// CommitResponse describes the created commit.
type CommitResponse struct {
	Hash    string   `json:"hash"`
	Message string   `json:"message"`
	Paths   []string `json:"paths"`
}

// Commit stages and commits only the explicitly selected workspace paths.
func (service *Service) Commit(ctx context.Context, request CommitRequest) (CommitResponse, error) {
	message := strings.TrimSpace(request.Message)
	if message == "" {
		return CommitResponse{}, apperr.New(apperr.KindUsage, "gitinspect.commit", "commit message is required")
	}
	if len(message) > 2000 {
		return CommitResponse{}, apperr.New(apperr.KindUsage, "gitinspect.commit", "commit message exceeds 2000 bytes")
	}
	paths, err := service.mutationPaths(request.Paths, "gitinspect.commit")
	if err != nil {
		return CommitResponse{}, err
	}
	if err := service.stagePaths(ctx, paths); err != nil {
		return CommitResponse{}, err
	}
	staged, err := service.stagedPaths(ctx, paths)
	if err != nil {
		return CommitResponse{}, err
	}
	if len(staged) == 0 {
		return CommitResponse{}, service.noSelectedChangesError(ctx, paths)
	}
	arguments := append([]string{"commit", "-m", message, "--"}, paths...)
	if _, err := service.run(ctx, arguments...); err != nil {
		return CommitResponse{}, err
	}
	hash, err := service.run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return CommitResponse{}, err
	}
	return CommitResponse{Hash: strings.TrimSpace(hash), Message: message, Paths: paths}, nil
}

// RestoreRequest selects paths and the local state to restore.
type RestoreRequest struct {
	Mode  string   `json:"mode"`
	Paths []string `json:"paths"`
}

// RestoreResponse describes paths restored from the index or HEAD.
type RestoreResponse struct {
	Mode  string   `json:"mode"`
	Paths []string `json:"paths"`
}

// Restore restores selected worktree or index paths using fixed local modes.
func (service *Service) Restore(ctx context.Context, request RestoreRequest) (RestoreResponse, error) {
	paths, err := service.mutationPaths(request.Paths, "gitinspect.restore")
	if err != nil {
		return RestoreResponse{}, err
	}
	var arguments []string
	switch request.Mode {
	case "worktree":
		arguments = []string{"restore", "--worktree", "--"}
	case "staged":
		arguments = []string{"restore", "--staged", "--"}
	case "head":
		arguments = []string{"restore", "--source=HEAD", "--staged", "--worktree", "--"}
	default:
		return RestoreResponse{}, apperr.New(apperr.KindUsage, "gitinspect.restore", "mode must be worktree, staged, or head")
	}
	arguments = append(arguments, paths...)
	if _, err := service.run(ctx, arguments...); err != nil {
		return RestoreResponse{}, err
	}
	return RestoreResponse{Mode: request.Mode, Paths: paths}, nil
}

func (service *Service) stagedPaths(ctx context.Context, paths []string) ([]string, error) {
	arguments := append([]string{"diff", "--cached", "--name-only", "-z", "--"}, paths...)
	output, err := service.run(ctx, arguments...)
	if err != nil {
		return nil, err
	}
	staged := make([]string, 0)
	for _, path := range strings.Split(output, "\x00") {
		if path != "" {
			staged = append(staged, path)
		}
	}
	return staged, nil
}

func (service *Service) stagePaths(ctx context.Context, paths []string) error {
	arguments := append([]string{"add", "--"}, paths...)
	_, err := service.run(ctx, arguments...)
	return err
}

func (service *Service) noSelectedChangesError(ctx context.Context, paths []string) error {
	message := "no staged changes exist for the requested paths: " + strings.Join(paths, ", ")
	status, err := service.Status(ctx, StatusRequest{})
	if err == nil && len(status.Entries) > 0 {
		changed := make([]string, 0, len(status.Entries))
		for _, entry := range status.Entries {
			changed = append(changed, entry.Path)
		}
		message += "; current changed paths: " + strings.Join(changed, ", ")
	}
	return apperr.New(apperr.KindPolicy, "gitinspect.commit", message)
}

func (service *Service) mutationPaths(paths []string, operation string) ([]string, error) {
	if len(paths) == 0 {
		return nil, apperr.New(apperr.KindUsage, operation, "at least one workspace path is required")
	}
	scoped, err := service.scopedPaths(paths)
	if err != nil {
		return nil, err
	}
	for _, path := range scoped {
		if path == ".git" || strings.HasPrefix(path, ".git/") {
			return nil, apperr.New(apperr.KindPolicy, operation, "Git metadata paths are not writable")
		}
	}
	return scoped, nil
}

// RegisterTools exposes Git inspection through the normalized tool registry.
func RegisterTools(registry *tools.Registry, service *Service) error {
	if err := registerAdapters(registry, gitReadAdapters(service)); err != nil {
		return err
	}
	return registerAdapters(registry, gitMutationAdapters(service))
}

func registerAdapters(registry *tools.Registry, adapters []toolAdapter) error {
	for _, adapter := range adapters {
		if err := registry.Register(adapter); err != nil {
			return err
		}
	}
	return nil
}

func gitReadAdapters(service *Service) []toolAdapter {
	return []toolAdapter{
		{name: "git.root", description: "Find the containing repository and HEAD state.", parameters: `{"type":"object","properties":{}}`, risk: tools.RiskReadOnly, execute: func(ctx context.Context, _ tools.Call) (any, error) {
			var request RootRequest
			return service.Root(ctx, request)
		}},
		{name: "git.branch", description: "Inspect the current branch, upstream, and divergence.", parameters: `{"type":"object","properties":{}}`, risk: tools.RiskReadOnly, execute: func(ctx context.Context, _ tools.Call) (any, error) {
			var request BranchRequest
			return service.Branch(ctx, request)
		}},
		{name: "git.worktree", description: "List local Git worktrees and their branches.", parameters: `{"type":"object","properties":{}}`, risk: tools.RiskReadOnly, execute: func(ctx context.Context, _ tools.Call) (any, error) {
			var request WorktreeRequest
			return service.Worktrees(ctx, request)
		}},
		{name: "git.status", description: "Inspect bounded staged and worktree status.", parameters: `{"type":"object","properties":{"path":{"type":"string"},"include_ignored":{"type":"boolean"}}}`, risk: tools.RiskReadOnly, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request StatusRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Status(ctx, request)
		}},
		{name: "git.diff", description: "Read a bounded worktree, index, or revision diff, optionally including untracked files.", parameters: `{"type":"object","properties":{"source":{"type":"string","enum":["worktree","index","range"]},"revision":{"type":"string"},"paths":{"type":"array","items":{"type":"string"}},"max_bytes":{"type":"integer","minimum":1},"include_untracked":{"type":"boolean"}},"required":["source"]}`, risk: tools.RiskReadOnly, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request DiffRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Diff(ctx, request)
		}},
		{name: "git.log", description: "Read bounded commit metadata.", parameters: `{"type":"object","properties":{"revision":{"type":"string"},"paths":{"type":"array","items":{"type":"string"}},"limit":{"type":"integer"}}}`, risk: tools.RiskReadOnly, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request LogRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Log(ctx, request)
		}},
		{name: "git.show", description: "Read bounded content from an explicit Git revision or object.", parameters: `{"type":"object","properties":{"revision":{"type":"string"},"max_bytes":{"type":"integer"}},"required":["revision"]}`, risk: tools.RiskReadOnly, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request ShowRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Show(ctx, request)
		}},
		{name: "git.blame", description: "Inspect bounded line ownership for a workspace file.", parameters: `{"type":"object","properties":{"path":{"type":"string"},"start_line":{"type":"integer"},"end_line":{"type":"integer"}},"required":["path"]}`, risk: tools.RiskReadOnly, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request BlameRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Blame(ctx, request)
		}},
		{name: "git.check_ignore", description: "Check whether one workspace path is ignored.", parameters: `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`, risk: tools.RiskReadOnly, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request CheckIgnoreRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.CheckIgnore(ctx, request)
		}},
	}
}

func gitMutationAdapters(service *Service) []toolAdapter {
	return []toolAdapter{
		{name: "git.stage", description: "Stage explicit workspace paths for a later commit.", parameters: `{"type":"object","properties":{"paths":{"type":"array","items":{"type":"string"}}},"required":["paths"]}`, risk: tools.RiskWrite, changedPaths: gitPathsChangedPaths, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request StageRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Stage(ctx, request)
		}},
		{name: "git.unstage", description: "Remove explicit workspace paths from the Git index without changing files.", parameters: `{"type":"object","properties":{"paths":{"type":"array","items":{"type":"string"}}},"required":["paths"]}`, risk: tools.RiskWrite, changedPaths: gitPathsChangedPaths, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request UnstageRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Unstage(ctx, request)
		}},
		{name: "git.commit", description: "Stage and commit only explicit workspace paths with a required message. Validate the selected diff before calling this tool.", parameters: `{"type":"object","properties":{"message":{"type":"string","maxLength":2000},"paths":{"type":"array","items":{"type":"string"}}},"required":["message","paths"]}`, risk: tools.RiskDestructive, changedPaths: gitPathsChangedPaths, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request CommitRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Commit(ctx, request)
		}},
		{name: "git.restore", description: "Restore explicit paths from the index, worktree, or HEAD. This can discard local changes.", parameters: `{"type":"object","properties":{"mode":{"type":"string","enum":["worktree","staged","head"]},"paths":{"type":"array","items":{"type":"string"}}},"required":["mode","paths"]}`, risk: tools.RiskDestructive, changedPaths: gitPathsChangedPaths, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request RestoreRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Restore(ctx, request)
		}},
	}
}

type toolAdapter struct {
	name         string
	description  string
	parameters   string
	risk         tools.Risk
	changedPaths func(any) []string
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
	return result
}

func gitPathsChangedPaths(data any) []string {
	response, ok := data.(interface{ changedPaths() []string })
	if !ok {
		return nil
	}
	return append([]string(nil), response.changedPaths()...)
}

func (response StageResponse) changedPaths() []string   { return response.Paths }
func (response UnstageResponse) changedPaths() []string { return response.Paths }
func (response CommitResponse) changedPaths() []string  { return response.Paths }
func (response RestoreResponse) changedPaths() []string { return response.Paths }

func (service *Service) scopedPath(path string) (string, error) {
	if path == "" {
		return ".", nil
	}
	if filepath.IsAbs(path) {
		return "", apperr.New(apperr.KindTool, "gitinspect.path", "absolute paths are not allowed")
	}
	cleanPath := filepath.Clean(filepath.FromSlash(path))
	if cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
		return "", apperr.New(apperr.KindTool, "gitinspect.path", "path escapes the workspace")
	}
	return filepath.ToSlash(cleanPath), nil
}

func (service *Service) scopedPaths(paths []string) ([]string, error) {
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		scoped, err := service.scopedPath(path)
		if err != nil {
			return nil, err
		}
		result = append(result, scoped)
	}
	return result, nil
}

func (service *Service) run(ctx context.Context, arguments ...string) (string, error) {
	// #nosec G204 -- git is fixed and arguments come from typed, validated Git operations.
	command := exec.CommandContext(ctx, "git", arguments...)
	command.Dir = service.workspace
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return "", apperr.Wrap(apperr.KindTool, "gitinspect."+arguments[0], errors.New(message))
	}
	return stdout.String(), nil
}

func parsePorcelainStatus(output string) []StatusEntry {
	entries := make([]StatusEntry, 0)
	for _, record := range strings.Split(output, "\x00") {
		if len(record) < 3 {
			continue
		}
		entry := StatusEntry{Index: record[:1], Worktree: record[1:2], Path: record[3:]}
		if entry.Index == "R" || entry.Worktree == "R" {
			parts := strings.SplitN(entry.Path, "\x00", 2)
			entry.Path = parts[0]
			if len(parts) == 2 {
				entry.Original = parts[1]
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

func parseWorktrees(output string) []WorktreeEntry {
	worktrees := make([]WorktreeEntry, 0)
	current := WorktreeEntry{}
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			worktrees, current = appendWorktree(worktrees, current)
			continue
		}
		if strings.HasPrefix(line, "worktree ") {
			worktrees, current = appendWorktree(worktrees, current)
			current.Path = strings.TrimPrefix(line, "worktree ")
			continue
		}
		parseWorktreeField(&current, line)
	}
	worktrees, _ = appendWorktree(worktrees, current)
	return worktrees
}

func appendWorktree(worktrees []WorktreeEntry, current WorktreeEntry) ([]WorktreeEntry, WorktreeEntry) {
	if current.Path != "" || current.Bare {
		worktrees = append(worktrees, current)
	}
	return worktrees, WorktreeEntry{}
}

func parseWorktreeField(entry *WorktreeEntry, line string) {
	switch {
	case strings.HasPrefix(line, "HEAD "):
		entry.Head = strings.TrimPrefix(line, "HEAD ")
	case strings.HasPrefix(line, "branch "):
		entry.Branch = strings.TrimPrefix(line, "branch ")
	case line == "bare":
		entry.Bare = true
	}
}

func parseInt(value string) int {
	number := 0
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0
		}
		number = number*10 + int(character-'0')
	}
	return number
}

func parseBlame(output string) BlameResponse {
	response := BlameResponse{Lines: make([]BlameLine, 0)}
	scanner := bufio.NewScanner(strings.NewReader(output))
	current := BlameLine{}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "author ") {
			current.Author = strings.TrimPrefix(line, "author ")
		} else if strings.HasPrefix(line, "\t") {
			current.Content = strings.TrimPrefix(line, "\t")
			current.Line = len(response.Lines) + 1
			response.Lines = append(response.Lines, current)
			current = BlameLine{}
		} else if fields := strings.Fields(line); len(fields) > 0 && len(fields[0]) >= 7 {
			current.Commit = fields[0]
		}
	}
	return response
}

func maxInt(first, second int) int {
	if first > second {
		return first
	}
	return second
}
