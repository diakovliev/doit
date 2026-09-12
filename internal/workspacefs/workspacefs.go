// Package workspacefs provides bounded, workspace-confined filesystem tools.
package workspacefs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/diakovliev/doit/internal/apperr"
	"github.com/diakovliev/doit/internal/tools"
)

const (
	defaultMaxBytes   = 64 * 1024
	defaultMaxEntries = 1000
	defaultMaxResults = 100
	maxHashBytes      = 128 * 1024 * 1024
)

var errWalkLimit = errors.New("workspace walk limit reached")

// Service owns read-only operations rooted at one effective workspace path.
type Service struct {
	root       string
	realRoot   string
	filesystem fs.FS
	patterns   []string
}

// New creates a filesystem service for an existing directory.
func New(root string) (*Service, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, apperr.Wrap(apperr.KindConfig, "workspacefs.root", err)
	}
	info, err := os.Stat(absoluteRoot)
	if err != nil {
		return nil, apperr.Wrap(apperr.KindConfig, "workspacefs.root", err)
	}
	if !info.IsDir() {
		return nil, apperr.New(apperr.KindConfig, "workspacefs.root", "workspace is not a directory")
	}
	realRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return nil, apperr.Wrap(apperr.KindConfig, "workspacefs.root", err)
	}
	service := &Service{
		root:       absoluteRoot,
		realRoot:   realRoot,
		filesystem: os.DirFS(absoluteRoot),
	}
	service.patterns = service.loadIgnorePatterns()
	return service, nil
}

// Root returns the absolute configured workspace path.
func (service *Service) Root() string {
	return service.root
}

// ListRequest controls bounded directory listing.
type ListRequest struct {
	Path           string `json:"path"`
	Recursive      bool   `json:"recursive"`
	MaxEntries     int    `json:"max_entries"`
	MaxDepth       int    `json:"max_depth"`
	IncludeIgnored bool   `json:"include_ignored"`
}

// Entry describes one workspace entry.
type Entry struct {
	Path string      `json:"path"`
	Type string      `json:"type"`
	Size int64       `json:"size"`
	Mode fs.FileMode `json:"mode"`
}

// ListResponse is a bounded listing result.
type ListResponse struct {
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated"`
}

// List returns entries below a workspace-relative path.
func (service *Service) List(ctx context.Context, request ListRequest) (ListResponse, error) {
	relativePath, _, err := service.resolveExisting(request.Path)
	if err != nil {
		return ListResponse{}, err
	}
	maxEntries := request.MaxEntries
	if maxEntries <= 0 {
		maxEntries = defaultMaxEntries
	}
	response := ListResponse{Entries: make([]Entry, 0, minInt(maxEntries, 32))}
	err = fs.WalkDir(service.filesystem, relativePath, func(path string, directoryEntry fs.DirEntry, walkErr error) error {
		return service.listEntry(ctx, path, relativePath, directoryEntry, walkErr, request, maxEntries, &response)
	})
	if errors.Is(err, errWalkLimit) {
		return response, nil
	}
	if err != nil {
		return ListResponse{}, normalizeFilesystemError("workspacefs.list", err)
	}
	return response, nil
}

func (service *Service) listEntry(ctx context.Context, path, rootPath string, directoryEntry fs.DirEntry, walkErr error, request ListRequest, maxEntries int, response *ListResponse) error {
	if walkErr != nil {
		return walkErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if path == rootPath {
		return nil
	}
	if service.skipListEntry(path, rootPath, directoryEntry, request) {
		if directoryEntry.IsDir() {
			return fs.SkipDir
		}
		return nil
	}
	if _, _, scopeErr := service.resolveExisting(path); scopeErr != nil {
		return scopeErr
	}
	entryInfo, infoErr := directoryEntry.Info()
	if infoErr != nil {
		return infoErr
	}
	response.Entries = append(response.Entries, Entry{Path: filepath.ToSlash(path), Type: entryType(directoryEntry), Size: entryInfo.Size(), Mode: entryInfo.Mode()})
	if len(response.Entries) >= maxEntries {
		response.Truncated = true
		return errWalkLimit
	}
	return nil
}

func (service *Service) skipListEntry(path, rootPath string, _ fs.DirEntry, request ListRequest) bool {
	if path == rootPath || service.isIgnored(path, request.IncludeIgnored) {
		return true
	}
	depth := pathDepth(path, rootPath)
	return (!request.Recursive && depth > 1) || (request.MaxDepth > 0 && depth > request.MaxDepth)
}

func entryType(directoryEntry fs.DirEntry) string {
	if directoryEntry.IsDir() {
		return "directory"
	}
	if directoryEntry.Type()&os.ModeSymlink != 0 {
		return "symlink"
	}
	return "file"
}

// StatRequest selects one workspace entry.
type StatRequest struct {
	Path string `json:"path"`
}

// StatResponse describes one workspace entry.
type StatResponse struct {
	Entry Entry `json:"entry"`
}

// Stat returns metadata for one workspace-relative entry.
func (service *Service) Stat(ctx context.Context, request StatRequest) (StatResponse, error) {
	if err := ctx.Err(); err != nil {
		return StatResponse{}, err
	}
	relativePath, _, err := service.resolveExisting(request.Path)
	if err != nil {
		return StatResponse{}, err
	}
	info, err := fs.Stat(service.filesystem, relativePath)
	if err != nil {
		return StatResponse{}, normalizeFilesystemError("workspacefs.stat", err)
	}
	entryType := "file"
	if info.IsDir() {
		entryType = "directory"
	}
	return StatResponse{Entry: Entry{Path: filepath.ToSlash(relativePath), Type: entryType, Size: info.Size(), Mode: info.Mode()}}, nil
}

// ReadRequest controls a bounded text read.
type ReadRequest struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	MaxBytes  int    `json:"max_bytes"`
}

// ReadResponse contains bounded file content.
type ReadResponse struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Truncated bool   `json:"truncated"`
}

// Read returns bounded text content from one workspace file.
func (service *Service) Read(ctx context.Context, request ReadRequest) (ReadResponse, error) {
	relativePath, _, err := service.resolveExisting(request.Path)
	if err != nil {
		return ReadResponse{}, err
	}
	info, err := fs.Stat(service.filesystem, relativePath)
	if err != nil {
		return ReadResponse{}, normalizeFilesystemError("workspacefs.read", err)
	}
	if info.IsDir() {
		return ReadResponse{}, apperr.New(apperr.KindTool, "workspacefs.read", "path is a directory")
	}
	maxBytes := request.MaxBytes
	if maxBytes <= 0 || maxBytes > defaultMaxBytes {
		maxBytes = defaultMaxBytes
	}
	content, truncated, readErr := service.readBounded(ctx, relativePath, maxBytes)
	if readErr != nil {
		return ReadResponse{}, readErr
	}
	return selectLines(filepath.ToSlash(relativePath), content, request, truncated), nil
}

func selectLines(path, content string, request ReadRequest, truncated bool) ReadResponse {
	lines := strings.Split(content, "\n")
	startLine := request.StartLine
	if startLine <= 0 {
		startLine = 1
	}
	endLine := request.EndLine
	if endLine <= 0 || endLine > len(lines) {
		endLine = len(lines)
	}
	response := ReadResponse{Path: path, StartLine: startLine, EndLine: endLine, Truncated: truncated}
	if startLine <= endLine && startLine <= len(lines) {
		response.Content = strings.Join(lines[startLine-1:endLine], "\n")
	}
	return response
}

// SearchRequest controls bounded text search.
type SearchRequest struct {
	Query          string `json:"query"`
	Path           string `json:"path"`
	Glob           string `json:"glob"`
	MaxResults     int    `json:"max_results"`
	IncludeIgnored bool   `json:"include_ignored"`
}

// Match describes one text match.
type Match struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// SearchResponse is a bounded search result.
type SearchResponse struct {
	Matches   []Match `json:"matches"`
	Truncated bool    `json:"truncated"`
}

// Search finds a literal query in bounded workspace files.
func (service *Service) Search(ctx context.Context, request SearchRequest) (SearchResponse, error) {
	if request.Query == "" {
		return SearchResponse{}, apperr.New(apperr.KindUsage, "workspacefs.search", "query is required")
	}
	rootPath, _, err := service.resolveExisting(request.Path)
	if err != nil {
		return SearchResponse{}, err
	}
	maxResults := request.MaxResults
	if maxResults <= 0 {
		maxResults = defaultMaxResults
	}
	response := SearchResponse{Matches: make([]Match, 0, minInt(maxResults, 32))}
	err = fs.WalkDir(service.filesystem, rootPath, func(path string, directoryEntry fs.DirEntry, walkErr error) error {
		return service.searchEntry(ctx, path, directoryEntry, walkErr, request, maxResults, &response)
	})
	if errors.Is(err, errWalkLimit) {
		return response, nil
	}
	if err != nil {
		return SearchResponse{}, normalizeFilesystemError("workspacefs.search", err)
	}
	return response, nil
}

func (service *Service) searchEntry(ctx context.Context, path string, directoryEntry fs.DirEntry, walkErr error, request SearchRequest, maxResults int, response *SearchResponse) error {
	if walkErr != nil {
		return walkErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if service.isIgnored(path, request.IncludeIgnored) {
		if directoryEntry.IsDir() {
			return fs.SkipDir
		}
		return nil
	}
	if directoryEntry.IsDir() || directoryEntry.Type()&os.ModeSymlink != 0 || !matchesGlob(request.Glob, path) {
		return nil
	}
	content, _, err := service.readBounded(ctx, path, defaultMaxBytes)
	if err != nil {
		return err
	}
	return appendSearchMatches(content, path, request.Query, maxResults, response)
}

func appendSearchMatches(content, path, query string, maxResults int, response *SearchResponse) error {
	for lineNumber, line := range strings.Split(content, "\n") {
		if strings.Contains(line, query) {
			response.Matches = append(response.Matches, Match{Path: filepath.ToSlash(path), Line: lineNumber + 1, Text: line})
		}
		if len(response.Matches) >= maxResults {
			response.Truncated = true
			return errWalkLimit
		}
	}
	return nil
}

func matchesGlob(pattern, path string) bool {
	if pattern == "" {
		return true
	}
	matched, err := filepath.Match(pattern, filepath.ToSlash(path))
	return err == nil && matched
}

// HashRequest selects one workspace file.
type HashRequest struct {
	Path string `json:"path"`
}

// HashResponse contains a SHA-256 content hash.
type HashResponse struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// Hash calculates a bounded-size SHA-256 hash for one file.
func (service *Service) Hash(ctx context.Context, request HashRequest) (HashResponse, error) {
	relativePath, _, err := service.resolveExisting(request.Path)
	if err != nil {
		return HashResponse{}, err
	}
	info, err := fs.Stat(service.filesystem, relativePath)
	if err != nil {
		return HashResponse{}, normalizeFilesystemError("workspacefs.hash", err)
	}
	if info.IsDir() {
		return HashResponse{}, apperr.New(apperr.KindTool, "workspacefs.hash", "path is a directory")
	}
	if info.Size() > maxHashBytes {
		return HashResponse{}, apperr.New(apperr.KindTool, "workspacefs.hash", "file exceeds hash size limit")
	}
	file, err := service.filesystem.Open(relativePath)
	if err != nil {
		return HashResponse{}, normalizeFilesystemError("workspacefs.hash", err)
	}
	defer func() { _ = file.Close() }()
	hasher := sha256.New()
	bytesRead, err := io.Copy(hasher, file)
	if err != nil {
		return HashResponse{}, apperr.Wrap(apperr.KindTool, "workspacefs.hash", err)
	}
	if err := ctx.Err(); err != nil {
		return HashResponse{}, err
	}
	return HashResponse{Path: filepath.ToSlash(relativePath), SHA256: hex.EncodeToString(hasher.Sum(nil)), Bytes: bytesRead}, nil
}

// RegisterTools exposes the filesystem service through the normalized tool registry.
func RegisterTools(registry *tools.Registry, service *Service) error {
	definitions := []toolAdapter{
		{name: "fs.list", description: "List bounded workspace entries.", parameters: `{"type":"object"}`, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request ListRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.List(ctx, request)
		}},
		{name: "fs.stat", description: "Inspect one workspace entry.", parameters: `{"type":"object","required":["path"]}`, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request StatRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Stat(ctx, request)
		}},
		{name: "fs.read", description: "Read bounded workspace text.", parameters: `{"type":"object","required":["path"]}`, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request ReadRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Read(ctx, request)
		}},
		{name: "fs.search", description: "Search bounded workspace text.", parameters: `{"type":"object","required":["query"]}`, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request SearchRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Search(ctx, request)
		}},
		{name: "fs.hash", description: "Hash one workspace file.", parameters: `{"type":"object","required":["path"]}`, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request HashRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Hash(ctx, request)
		}},
	}
	for _, adapter := range definitions {
		if err := registry.Register(adapter); err != nil {
			return err
		}
	}
	return nil
}

type toolAdapter struct {
	name        string
	description string
	parameters  string
	execute     func(context.Context, tools.Call) (any, error)
}

func (adapter toolAdapter) Definition() tools.Definition {
	return tools.Definition{Name: adapter.name, Description: adapter.description, Parameters: json.RawMessage(adapter.parameters), Risk: tools.RiskReadOnly, Timeout: 30_000_000_000, MaxOutputBytes: defaultMaxBytes, MaxArguments: 8}
}

func (adapter toolAdapter) Execute(ctx context.Context, call tools.Call) tools.Result {
	data, err := adapter.execute(ctx, call)
	if err != nil {
		return tools.Result{Status: tools.StatusFailed, Diagnostics: []tools.Diagnostic{{Level: "error", Message: err.Error()}}}
	}
	return tools.Result{Status: tools.StatusSucceeded, Data: data}
}

func (service *Service) resolveExisting(path string) (relativePath string, absolutePath string, err error) {
	relativePath, absolutePath, err = service.resolve(path)
	if err != nil {
		return "", "", err
	}
	if _, err := fs.Stat(service.filesystem, relativePath); err != nil {
		return "", "", normalizeFilesystemError("workspacefs.path", err)
	}
	return relativePath, absolutePath, nil
}

func (service *Service) resolve(path string) (relativePath string, absolutePath string, err error) {
	if path == "" {
		path = "."
	}
	if filepath.IsAbs(path) {
		return "", "", apperr.New(apperr.KindTool, "workspacefs.path", "absolute paths are not allowed")
	}
	cleanPath := filepath.Clean(filepath.FromSlash(path))
	if cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
		return "", "", apperr.New(apperr.KindTool, "workspacefs.path", "path escapes the workspace")
	}
	relativePath = filepath.ToSlash(cleanPath)
	if relativePath == "." {
		relativePath = "."
	}
	absolutePath = filepath.Join(service.root, cleanPath)
	realPath, err := filepath.EvalSymlinks(absolutePath)
	if err != nil {
		return "", "", normalizeFilesystemError("workspacefs.path", err)
	}
	relativeRealPath, err := filepath.Rel(service.realRoot, realPath)
	if err != nil || relativeRealPath == ".." || strings.HasPrefix(relativeRealPath, ".."+string(filepath.Separator)) {
		return "", "", apperr.New(apperr.KindTool, "workspacefs.path", "symlink resolves outside the workspace")
	}
	return relativePath, absolutePath, nil
}

func (service *Service) readBounded(ctx context.Context, path string, maxBytes int) (string, bool, error) {
	file, err := service.filesystem.Open(path)
	if err != nil {
		return "", false, normalizeFilesystemError("workspacefs.read", err)
	}
	defer func() { _ = file.Close() }()
	contents, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return "", false, apperr.Wrap(apperr.KindTool, "workspacefs.read", err)
	}
	truncated := len(contents) > maxBytes
	if truncated {
		contents = contents[:maxBytes]
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	return string(contents), truncated, nil
}

func (service *Service) loadIgnorePatterns() []string {
	contents, err := fs.ReadFile(service.filesystem, ".gitignore")
	if err != nil {
		return nil
	}
	var patterns []string
	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		patterns = append(patterns, strings.TrimPrefix(line, "/"))
	}
	return patterns
}

func (service *Service) isIgnored(path string, includeIgnored bool) bool {
	if includeIgnored {
		return false
	}
	slashPath := filepath.ToSlash(path)
	for _, segment := range strings.Split(slashPath, "/") {
		if segment == ".git" || segment == ".doit" {
			return true
		}
	}
	for _, pattern := range service.patterns {
		matched, err := filepath.Match(pattern, slashPath)
		if err == nil && matched {
			return true
		}
	}
	return false
}

func pathDepth(path, root string) int {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." {
		return 0
	}
	return len(strings.Split(filepath.ToSlash(relative), "/"))
}

func normalizeFilesystemError(operation string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return apperr.Wrap(apperr.KindTool, operation, err)
}

func minInt(first, second int) int {
	if first < second {
		return first
	}
	return second
}
