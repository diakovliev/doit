// Package gitinspect exposes bounded, structured Git inspection.
package gitinspect

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/diakovliev/doit/internal/apperr"
	"github.com/diakovliev/doit/internal/tools"
)

// Service executes a fixed set of read-only Git commands within one workspace.
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

// Root discovers the containing Git repository and HEAD state.
func (service *Service) Root(ctx context.Context, _ RootRequest) (RootResponse, error) {
	repository, err := service.run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return RootResponse{}, err
	}
	detached := service.isDetached(ctx)
	return RootResponse{Workspace: service.workspace, Repository: strings.TrimSpace(repository), DetachedHead: detached, IsRepository: true}, nil
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

// DiffRequest selects a bounded diff source.
type DiffRequest struct {
	Source   string   `json:"source"`
	Revision string   `json:"revision"`
	Paths    []string `json:"paths"`
	MaxBytes int      `json:"max_bytes"`
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
	truncated := len(diff) > maxBytes
	if truncated {
		diff = diff[:maxBytes]
	}
	return DiffResponse{Source: request.Source, Revision: request.Revision, Diff: diff, Truncated: truncated}, nil
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
func (service *Service) CheckIgnore(_ context.Context, request CheckIgnoreRequest) (CheckIgnoreResponse, error) {
	path, err := service.scopedPath(request.Path)
	if err != nil {
		return CheckIgnoreResponse{}, err
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return CheckIgnoreResponse{}, apperr.Wrap(apperr.KindTool, "gitinspect.check_ignore", err)
	}
	command := &exec.Cmd{Path: gitPath, Args: []string{gitPath, "check-ignore", "--quiet", "--", path}, Dir: service.workspace}
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

// RegisterTools exposes Git inspection through the normalized tool registry.
func RegisterTools(registry *tools.Registry, service *Service) error {
	adapters := []toolAdapter{
		{name: "git.root", risk: tools.RiskReadOnly, execute: func(ctx context.Context, _ tools.Call) (any, error) {
			var request RootRequest
			return service.Root(ctx, request)
		}},
		{name: "git.status", risk: tools.RiskReadOnly, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request StatusRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Status(ctx, request)
		}},
		{name: "git.diff", risk: tools.RiskReadOnly, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request DiffRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Diff(ctx, request)
		}},
		{name: "git.log", risk: tools.RiskReadOnly, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request LogRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Log(ctx, request)
		}},
		{name: "git.show", risk: tools.RiskReadOnly, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request ShowRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Show(ctx, request)
		}},
		{name: "git.blame", risk: tools.RiskReadOnly, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request BlameRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Blame(ctx, request)
		}},
		{name: "git.check_ignore", risk: tools.RiskReadOnly, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request CheckIgnoreRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.CheckIgnore(ctx, request)
		}},
	}
	for _, adapter := range adapters {
		if err := registry.Register(adapter); err != nil {
			return err
		}
	}
	return nil
}

type toolAdapter struct {
	name    string
	risk    tools.Risk
	execute func(context.Context, tools.Call) (any, error)
}

func (adapter toolAdapter) Definition() tools.Definition {
	return tools.Definition{Name: adapter.name, Risk: adapter.risk, Timeout: 30_000_000_000, MaxOutputBytes: 64 * 1024, MaxArguments: 16}
}

func (adapter toolAdapter) Execute(ctx context.Context, call tools.Call) tools.Result {
	data, err := adapter.execute(ctx, call)
	if err != nil {
		return tools.Result{Status: tools.StatusFailed, Diagnostics: []tools.Diagnostic{{Level: "error", Message: err.Error()}}}
	}
	return tools.Result{Status: tools.StatusSucceeded, Data: data}
}

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

func (service *Service) run(_ context.Context, arguments ...string) (string, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", apperr.Wrap(apperr.KindTool, "gitinspect.git", err)
	}
	command := &exec.Cmd{Path: gitPath, Args: append([]string{gitPath}, arguments...), Dir: service.workspace}
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
