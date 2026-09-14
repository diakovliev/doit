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
	"regexp"
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
	Mode           string `json:"mode"`
	CaseSensitive  *bool  `json:"case_sensitive,omitempty"`
	BeforeLines    int    `json:"before_lines"`
	AfterLines     int    `json:"after_lines"`
	MaxResults     int    `json:"max_results"`
	IncludeIgnored bool   `json:"include_ignored"`
}

// Match describes one text match.
type Match struct {
	Path   string   `json:"path"`
	Line   int      `json:"line"`
	Text   string   `json:"text"`
	Before []string `json:"before,omitempty"`
	After  []string `json:"after,omitempty"`
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
	matcher, err := newSearchMatcher(request)
	if err != nil {
		return SearchResponse{}, err
	}
	response := SearchResponse{Matches: make([]Match, 0, minInt(maxResults, 32))}
	err = fs.WalkDir(service.filesystem, rootPath, func(path string, directoryEntry fs.DirEntry, walkErr error) error {
		return service.searchEntry(ctx, path, directoryEntry, walkErr, request, matcher, maxResults, &response)
	})
	if errors.Is(err, errWalkLimit) {
		return response, nil
	}
	if err != nil {
		return SearchResponse{}, normalizeFilesystemError("workspacefs.search", err)
	}
	return response, nil
}

func (service *Service) searchEntry(ctx context.Context, path string, directoryEntry fs.DirEntry, walkErr error, request SearchRequest, matcher searchMatcher, maxResults int, response *SearchResponse) error {
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
	return appendSearchMatches(content, path, matcher, maxResults, request.BeforeLines, request.AfterLines, response)
}

type searchMatcher struct {
	query         string
	caseSensitive bool
	regularExpr   *regexp.Regexp
}

func newSearchMatcher(request SearchRequest) (searchMatcher, error) {
	if request.BeforeLines < 0 || request.AfterLines < 0 {
		return searchMatcher{}, apperr.New(apperr.KindUsage, "workspacefs.search", "context line counts must not be negative")
	}
	mode := request.Mode
	if mode == "" {
		mode = "literal"
	}
	if mode != "literal" && mode != "regex" {
		return searchMatcher{}, apperr.New(apperr.KindUsage, "workspacefs.search", "mode must be literal or regex")
	}
	caseSensitive := true
	if request.CaseSensitive != nil {
		caseSensitive = *request.CaseSensitive
	}
	matcher := searchMatcher{query: request.Query, caseSensitive: caseSensitive}
	if mode == "regex" {
		pattern := request.Query
		if !caseSensitive {
			pattern = "(?i)" + pattern
		}
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return searchMatcher{}, apperr.Wrap(apperr.KindUsage, "workspacefs.search", err)
		}
		matcher.regularExpr = compiled
	}
	return matcher, nil
}

func appendSearchMatches(content, path string, matcher searchMatcher, maxResults, beforeLines, afterLines int, response *SearchResponse) error {
	lines := strings.Split(content, "\n")
	for lineNumber, line := range lines {
		if matcher.matches(line) {
			beforeStart := maxInt(0, lineNumber-beforeLines)
			afterEnd := minInt(len(lines), lineNumber+afterLines+1)
			response.Matches = append(response.Matches, Match{Path: filepath.ToSlash(path), Line: lineNumber + 1, Text: line, Before: append([]string(nil), lines[beforeStart:lineNumber]...), After: append([]string(nil), lines[lineNumber+1:afterEnd]...)})
		}
		if len(response.Matches) >= maxResults {
			response.Truncated = true
			return errWalkLimit
		}
	}
	return nil
}

func (matcher searchMatcher) matches(line string) bool {
	if matcher.regularExpr != nil {
		return matcher.regularExpr.MatchString(line)
	}
	if matcher.caseSensitive {
		return strings.Contains(line, matcher.query)
	}
	return strings.Contains(strings.ToLower(line), strings.ToLower(matcher.query))
}

func matchesGlob(pattern, path string) bool {
	if pattern == "" {
		return true
	}
	slashPath := filepath.ToSlash(path)
	candidates := []string{slashPath}
	if !strings.Contains(pattern, "/") {
		candidates = append(candidates, filepath.Base(slashPath))
	}
	if strings.HasPrefix(pattern, "**/") {
		candidates = append(candidates, strings.TrimPrefix(pattern, "**/"))
	}
	for _, candidate := range candidates {
		matched, err := filepath.Match(pattern, candidate)
		if err == nil && matched {
			return true
		}
	}
	return false
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

// WriteRequest controls bounded workspace file creation or replacement.
type WriteRequest struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Parents   bool   `json:"parents"`
	Overwrite bool   `json:"overwrite"`
}

// WriteResponse describes the written workspace file.
type WriteResponse struct {
	Path      string           `json:"path"`
	Bytes     int              `json:"bytes"`
	Created   bool             `json:"created"`
	Overwrote bool             `json:"overwrote"`
	ChangeSet *tools.ChangeSet `json:"change_set,omitempty"`
}

// Write creates or explicitly overwrites one bounded workspace file.
func (service *Service) Write(ctx context.Context, request WriteRequest) (WriteResponse, error) {
	if err := ctx.Err(); err != nil {
		return WriteResponse{}, err
	}
	path, err := service.mutationPath(request.Path, "workspacefs.write")
	if err != nil {
		return WriteResponse{}, err
	}
	content := []byte(request.Content)
	if len(content) > defaultMaxBytes {
		return WriteResponse{}, apperr.New(apperr.KindUsage, "workspacefs.write", "content exceeds the 64 KiB limit")
	}
	root, err := os.OpenRoot(service.root)
	if err != nil {
		return WriteResponse{}, apperr.Wrap(apperr.KindTool, "workspacefs.write", err)
	}
	defer func() { _ = root.Close() }()
	return writeRootFile(root, path, content, request)
}

func writeRootFile(root *os.Root, path string, content []byte, request WriteRequest) (WriteResponse, error) {
	before, beforeExists, err := readRootSnapshot(root, path)
	if err != nil {
		return WriteResponse{}, apperr.Wrap(apperr.KindTool, "workspacefs.write", err)
	}
	exists, err := prepareRootFile(root, path, request)
	if err != nil {
		return WriteResponse{}, err
	}
	flags := writeFlags(request.Overwrite)
	file, err := root.OpenFile(path, flags, 0600)
	if err != nil {
		return WriteResponse{}, apperr.Wrap(apperr.KindTool, "workspacefs.write", err)
	}
	written, err := writeAndClose(file, content)
	if err != nil {
		return WriteResponse{}, apperr.Wrap(apperr.KindTool, "workspacefs.write", err)
	}
	changeSet, err := newChangeSet("fs.write", "applied", []string{filepath.ToSlash(path)}, before, beforeExists, content, true)
	if err != nil {
		return WriteResponse{}, err
	}
	return WriteResponse{Path: filepath.ToSlash(path), Bytes: written, Created: !exists, Overwrote: exists, ChangeSet: changeSet}, nil
}

func readRootSnapshot(root *os.Root, path string) ([]byte, bool, error) {
	content, err := root.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return content, true, nil
}

func newChangeSet(operation, state string, paths []string, before []byte, beforeExists bool, after []byte, afterExists bool) (*tools.ChangeSet, error) {
	beforeHashes := map[string]string{paths[0]: snapshotHash(before, beforeExists)}
	afterHashes := map[string]string{paths[0]: snapshotHash(after, afterExists)}
	identity, err := json.Marshal(struct {
		Operation    string            `json:"operation"`
		Paths        []string          `json:"paths"`
		BeforeHashes map[string]string `json:"before_hashes"`
		AfterHashes  map[string]string `json:"after_hashes"`
	}{Operation: operation, Paths: paths, BeforeHashes: beforeHashes, AfterHashes: afterHashes})
	if err != nil {
		return nil, apperr.Wrap(apperr.KindTool, "workspacefs.change_set", err)
	}
	digest := sha256.Sum256(identity)
	return &tools.ChangeSet{ID: hex.EncodeToString(digest[:]), Operation: operation, State: state, Paths: paths, BeforeHashes: beforeHashes, AfterHashes: afterHashes}, nil
}

func snapshotHash(content []byte, exists bool) string {
	if !exists {
		return ""
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func prepareRootFile(root *os.Root, path string, request WriteRequest) (bool, error) {
	_, statErr := root.Stat(path)
	exists := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return false, apperr.Wrap(apperr.KindTool, "workspacefs.write", statErr)
	}
	if exists && !request.Overwrite {
		return false, apperr.New(apperr.KindPolicy, "workspacefs.write", "destination exists; set overwrite=true to replace it")
	}
	if request.Parents {
		parent := filepath.Dir(path)
		if parent != "." {
			if err := root.MkdirAll(parent, 0700); err != nil {
				return false, apperr.Wrap(apperr.KindTool, "workspacefs.write", err)
			}
		}
	}
	return exists, nil
}

func writeAndClose(file *os.File, content []byte) (int, error) {
	written, writeErr := file.Write(content)
	closeErr := file.Close()
	if writeErr != nil {
		return 0, writeErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	return written, nil
}

func writeFlags(overwrite bool) int {
	flags := os.O_CREATE | os.O_WRONLY
	if overwrite {
		return flags | os.O_TRUNC
	}
	return flags | os.O_EXCL
}

// MoveRequest controls a non-replacing workspace path move.
type MoveRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// MoveResponse describes a moved workspace path.
type MoveResponse struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Move moves a file or directory without replacing an existing destination.
func (service *Service) Move(ctx context.Context, request MoveRequest) (MoveResponse, error) {
	if err := ctx.Err(); err != nil {
		return MoveResponse{}, err
	}
	from, err := service.mutationPath(request.From, "workspacefs.move")
	if err != nil {
		return MoveResponse{}, err
	}
	to, err := service.mutationPath(request.To, "workspacefs.move")
	if err != nil {
		return MoveResponse{}, err
	}
	root, err := os.OpenRoot(service.root)
	if err != nil {
		return MoveResponse{}, apperr.Wrap(apperr.KindTool, "workspacefs.move", err)
	}
	defer func() { _ = root.Close() }()
	if _, err := root.Stat(to); err == nil {
		return MoveResponse{}, apperr.New(apperr.KindPolicy, "workspacefs.move", "destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return MoveResponse{}, apperr.Wrap(apperr.KindTool, "workspacefs.move", err)
	}
	if err := root.Rename(from, to); err != nil {
		return MoveResponse{}, apperr.Wrap(apperr.KindTool, "workspacefs.move", err)
	}
	return MoveResponse{From: filepath.ToSlash(from), To: filepath.ToSlash(to)}, nil
}

// MkdirRequest controls workspace directory creation.
type MkdirRequest struct {
	Path    string `json:"path"`
	Parents bool   `json:"parents"`
}

// MkdirResponse describes the created directory path.
type MkdirResponse struct {
	Path string `json:"path"`
}

// Mkdir creates one workspace directory, optionally including parents.
func (service *Service) Mkdir(ctx context.Context, request MkdirRequest) (MkdirResponse, error) {
	if err := ctx.Err(); err != nil {
		return MkdirResponse{}, err
	}
	path, err := service.mutationPath(request.Path, "workspacefs.mkdir")
	if err != nil {
		return MkdirResponse{}, err
	}
	root, err := os.OpenRoot(service.root)
	if err != nil {
		return MkdirResponse{}, apperr.Wrap(apperr.KindTool, "workspacefs.mkdir", err)
	}
	defer func() { _ = root.Close() }()
	if request.Parents {
		err = root.MkdirAll(path, 0700)
	} else {
		err = root.Mkdir(path, 0700)
	}
	if err != nil {
		return MkdirResponse{}, apperr.Wrap(apperr.KindTool, "workspacefs.mkdir", err)
	}
	return MkdirResponse{Path: filepath.ToSlash(path)}, nil
}

// RemoveRequest controls workspace file or directory removal.
type RemoveRequest struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
}

// RemoveResponse describes the removed workspace path.
type RemoveResponse struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
}

// Remove removes one workspace path, recursively only when requested.
func (service *Service) Remove(ctx context.Context, request RemoveRequest) (RemoveResponse, error) {
	if err := ctx.Err(); err != nil {
		return RemoveResponse{}, err
	}
	path, err := service.mutationPath(request.Path, "workspacefs.remove")
	if err != nil {
		return RemoveResponse{}, err
	}
	root, err := os.OpenRoot(service.root)
	if err != nil {
		return RemoveResponse{}, apperr.Wrap(apperr.KindTool, "workspacefs.remove", err)
	}
	defer func() { _ = root.Close() }()
	if request.Recursive {
		err = root.RemoveAll(path)
	} else {
		err = root.Remove(path)
	}
	if err != nil {
		return RemoveResponse{}, apperr.Wrap(apperr.KindTool, "workspacefs.remove", err)
	}
	return RemoveResponse{Path: filepath.ToSlash(path), Recursive: request.Recursive}, nil
}

// RegisterTools exposes the filesystem service through the normalized tool registry.
func RegisterTools(registry *tools.Registry, service *Service) error {
	definitions := append(readOnlyAdapters(service), mutationAdapters(service)...)
	return registerToolAdapters(registry, definitions)
}

func registerToolAdapters(registry *tools.Registry, definitions []toolAdapter) error {
	for _, adapter := range definitions {
		if err := registry.Register(adapter); err != nil {
			return err
		}
	}
	return nil
}

func readOnlyAdapters(service *Service) []toolAdapter {
	return []toolAdapter{
		{name: "fs.list", description: "List bounded workspace entries.", parameters: `{"type":"object","properties":{}}`, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request ListRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.List(ctx, request)
		}},
		{name: "fs.stat", description: "Inspect one workspace entry.", parameters: `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request StatRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Stat(ctx, request)
		}},
		{name: "fs.read", description: "Read bounded workspace text.", parameters: `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request ReadRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Read(ctx, request)
		}},
		{name: "fs.search", description: "Search bounded workspace text with literal or regular-expression matching and bounded context.", parameters: `{"type":"object","properties":{"query":{"type":"string"},"path":{"type":"string"},"glob":{"type":"string"},"mode":{"type":"string","enum":["literal","regex"]},"case_sensitive":{"type":"boolean"},"before_lines":{"type":"integer","minimum":0},"after_lines":{"type":"integer","minimum":0},"max_results":{"type":"integer","minimum":1},"include_ignored":{"type":"boolean"}},"required":["query"]}`, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request SearchRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Search(ctx, request)
		}},
		{name: "fs.hash", description: "Hash one workspace file.", parameters: `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request HashRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Hash(ctx, request)
		}},
	}
}

func mutationAdapters(service *Service) []toolAdapter {
	return []toolAdapter{
		{name: "fs.write", description: "Create or explicitly overwrite one bounded workspace file. Set parents=true for nested paths and overwrite=true to replace an existing file.", parameters: `{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string","maxLength":65536},"parents":{"type":"boolean"},"overwrite":{"type":"boolean"}},"required":["path","content"]}`, risk: tools.RiskWrite, changedPaths: singlePathChangedPaths, changeSet: writeChangeSet, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request WriteRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Write(ctx, request)
		}},
		{name: "fs.move", description: "Move one workspace file or directory without replacing an existing destination.", parameters: `{"type":"object","properties":{"from":{"type":"string"},"to":{"type":"string"}},"required":["from","to"]}`, risk: tools.RiskWrite, changedPaths: moveChangedPaths, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request MoveRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Move(ctx, request)
		}},
		{name: "fs.mkdir", description: "Create a workspace directory. Use parents=true for nested directory structures.", parameters: `{"type":"object","properties":{"path":{"type":"string"},"parents":{"type":"boolean"}},"required":["path"]}`, risk: tools.RiskWrite, changedPaths: singlePathChangedPaths, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request MkdirRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Mkdir(ctx, request)
		}},
		{name: "fs.remove", description: "Remove one workspace file or directory. Set recursive=true only when removing a directory tree is intended.", parameters: `{"type":"object","properties":{"path":{"type":"string"},"recursive":{"type":"boolean"}},"required":["path"]}`, risk: tools.RiskDestructive, changedPaths: singlePathChangedPaths, execute: func(ctx context.Context, call tools.Call) (any, error) {
			var request RemoveRequest
			if err := json.Unmarshal(call.Arguments, &request); err != nil {
				return nil, err
			}
			return service.Remove(ctx, request)
		}},
	}
}

type toolAdapter struct {
	name         string
	description  string
	parameters   string
	risk         tools.Risk
	changedPaths func(any) []string
	changeSet    func(any) *tools.ChangeSet
	execute      func(context.Context, tools.Call) (any, error)
}

func (adapter toolAdapter) Definition() tools.Definition {
	risk := adapter.risk
	if risk == "" {
		risk = tools.RiskReadOnly
	}
	return tools.Definition{Name: adapter.name, Description: adapter.description, Parameters: json.RawMessage(adapter.parameters), Risk: risk, Timeout: 30_000_000_000, MaxOutputBytes: defaultMaxBytes, MaxArguments: 8}
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
	if adapter.changeSet != nil {
		result.ChangeSet = adapter.changeSet(data)
	}
	return result
}

func singlePathChangedPaths(data any) []string {
	response, ok := data.(interface{ changedPath() string })
	if !ok || response.changedPath() == "" {
		return nil
	}
	return []string{response.changedPath()}
}

func moveChangedPaths(data any) []string {
	response, ok := data.(MoveResponse)
	if !ok {
		return nil
	}
	return []string{response.From, response.To}
}

func writeChangeSet(data any) *tools.ChangeSet {
	response, ok := data.(WriteResponse)
	if !ok {
		return nil
	}
	return response.ChangeSet
}

func (response WriteResponse) changedPath() string  { return response.Path }
func (response MkdirResponse) changedPath() string  { return response.Path }
func (response RemoveResponse) changedPath() string { return response.Path }

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

func (service *Service) mutationPath(path, operation string) (string, error) {
	if path == "" || filepath.IsAbs(path) {
		return "", apperr.New(apperr.KindTool, operation, "a non-empty workspace-relative path is required")
	}
	cleanPath := filepath.Clean(filepath.FromSlash(path))
	if cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
		return "", apperr.New(apperr.KindTool, operation, "path must stay below the workspace")
	}
	relativePath := filepath.ToSlash(cleanPath)
	if relativePath == ".git" || strings.HasPrefix(relativePath, ".git/") {
		return "", apperr.New(apperr.KindPolicy, operation, "Git metadata paths are not writable")
	}
	return relativePath, nil
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

func maxInt(first, second int) int {
	if first > second {
		return first
	}
	return second
}
