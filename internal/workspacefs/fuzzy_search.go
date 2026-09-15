package workspacefs

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/diakovliev/doit/internal/apperr"
	"github.com/diakovliev/doit/internal/tools"
	"github.com/sahilm/fuzzy"
)

const (
	defaultFuzzyMaxResults    = 20
	maxFuzzyResults           = 100
	defaultFuzzyMaxCandidates = 5000
	maxFuzzyCandidates        = 20000
	defaultFuzzyScanBytes     = 8 * 1024 * 1024
	maxFuzzyScanBytes         = 32 * 1024 * 1024
	maxFuzzyQueryBytes        = 256
	maxFuzzyContextLines      = 8
)

// FuzzySearchRequest controls bounded approximate search over paths and file lines.
type FuzzySearchRequest struct {
	Query          string `json:"query"`
	Path           string `json:"path"`
	Glob           string `json:"glob"`
	Target         string `json:"target"`
	CaseSensitive  bool   `json:"case_sensitive"`
	BeforeLines    int    `json:"before_lines"`
	AfterLines     int    `json:"after_lines"`
	MaxResults     int    `json:"max_results"`
	MaxCandidates  int    `json:"max_candidates"`
	MaxScanBytes   int    `json:"max_scan_bytes"`
	IncludeIgnored bool   `json:"include_ignored"`
}

// FuzzyMatch describes one ranked path or content match.
type FuzzyMatch struct {
	Path           string   `json:"path"`
	Line           int      `json:"line,omitempty"`
	Text           string   `json:"text,omitempty"`
	Score          int      `json:"score"`
	MatchKind      string   `json:"match_kind"`
	MatchedIndexes []int    `json:"matched_indexes,omitempty"`
	Before         []string `json:"before,omitempty"`
	After          []string `json:"after,omitempty"`
}

// FuzzySearchResponse is a bounded, ranked fuzzy-search result.
type FuzzySearchResponse struct {
	Matches           []FuzzyMatch `json:"matches"`
	Truncated         bool         `json:"truncated"`
	ScannedCandidates int          `json:"scanned_candidates"`
	ScannedBytes      int          `json:"scanned_bytes"`
}

type fuzzySearchLimits struct {
	maxResults    int
	maxCandidates int
	maxScanBytes  int
}

type fuzzyCandidate struct {
	value     string
	path      string
	line      int
	text      string
	matchKind string
	before    []string
	after     []string
}

type fuzzyCandidates []fuzzyCandidate

func (candidates fuzzyCandidates) String(index int) string {
	return candidates[index].value
}

func (candidates fuzzyCandidates) Len() int {
	return len(candidates)
}

type fuzzySearchState struct {
	request      FuzzySearchRequest
	limits       fuzzySearchLimits
	candidates   fuzzyCandidates
	scannedBytes int
	truncated    bool
}

// FuzzySearch ranks approximate matches within the bounded workspace scope.
func (service *Service) FuzzySearch(ctx context.Context, request FuzzySearchRequest) (FuzzySearchResponse, error) {
	request, limits, err := normalizeFuzzySearchRequest(request)
	if err != nil {
		return FuzzySearchResponse{}, err
	}
	rootPath, _, err := service.resolveExisting(request.Path)
	if err != nil {
		return FuzzySearchResponse{}, err
	}
	state := fuzzySearchState{request: request, limits: limits, candidates: make(fuzzyCandidates, 0, minInt(limits.maxCandidates, 128))}
	err = fsWalkDir(ctx, service, rootPath, &state)
	if err != nil && !errors.Is(err, errWalkLimit) {
		return FuzzySearchResponse{}, normalizeFilesystemError("workspacefs.fuzzy_search", err)
	}
	if errors.Is(err, errWalkLimit) {
		state.truncated = true
	}
	matches := rankFuzzyMatches(request.Query, state.candidates, request.CaseSensitive)
	response := FuzzySearchResponse{Matches: matches, Truncated: state.truncated, ScannedCandidates: len(state.candidates), ScannedBytes: state.scannedBytes}
	if len(response.Matches) > limits.maxResults {
		response.Matches = response.Matches[:limits.maxResults]
		response.Truncated = true
	}
	return response, nil
}

func normalizeFuzzySearchRequest(request FuzzySearchRequest) (FuzzySearchRequest, fuzzySearchLimits, error) {
	if err := validateFuzzySearchRequest(request); err != nil {
		return FuzzySearchRequest{}, fuzzySearchLimits{}, err
	}
	target, err := normalizeFuzzyTarget(request.Target)
	if err != nil {
		return FuzzySearchRequest{}, fuzzySearchLimits{}, err
	}
	request.Target = target
	request.BeforeLines = minInt(request.BeforeLines, maxFuzzyContextLines)
	request.AfterLines = minInt(request.AfterLines, maxFuzzyContextLines)
	limits := fuzzySearchLimits{
		maxResults:    boundedFuzzyLimit(request.MaxResults, defaultFuzzyMaxResults, maxFuzzyResults),
		maxCandidates: boundedFuzzyLimit(request.MaxCandidates, defaultFuzzyMaxCandidates, maxFuzzyCandidates),
		maxScanBytes:  boundedFuzzyLimit(request.MaxScanBytes, defaultFuzzyScanBytes, maxFuzzyScanBytes),
	}
	return request, limits, nil
}

func validateFuzzySearchRequest(request FuzzySearchRequest) error {
	switch {
	case request.Query == "":
		return apperr.New(apperr.KindUsage, "workspacefs.fuzzy_search", "query is required")
	case len(request.Query) > maxFuzzyQueryBytes:
		return apperr.New(apperr.KindUsage, "workspacefs.fuzzy_search", "query exceeds the 256-byte limit")
	case request.BeforeLines < 0 || request.AfterLines < 0:
		return apperr.New(apperr.KindUsage, "workspacefs.fuzzy_search", "context line counts must not be negative")
	default:
		return nil
	}
}

func normalizeFuzzyTarget(target string) (string, error) {
	if target == "" {
		return "path_and_content", nil
	}
	switch target {
	case "path", "content", "path_and_content":
		return target, nil
	default:
		return "", apperr.New(apperr.KindUsage, "workspacefs.fuzzy_search", "target must be path, content, or path_and_content")
	}
}

func boundedFuzzyLimit(value, defaultValue, maximum int) int {
	if value <= 0 {
		return defaultValue
	}
	if value > maximum {
		return maximum
	}
	return value
}

func fsWalkDir(ctx context.Context, service *Service, rootPath string, state *fuzzySearchState) error {
	return fs.WalkDir(service.filesystem, rootPath, func(path string, directoryEntry fs.DirEntry, walkErr error) error {
		return service.fuzzySearchEntry(ctx, path, directoryEntry, walkErr, state)
	})
}

func (service *Service) fuzzySearchEntry(ctx context.Context, path string, directoryEntry fs.DirEntry, walkErr error, state *fuzzySearchState) error {
	action, err := service.fuzzyEntryAction(ctx, path, directoryEntry, walkErr, state.request)
	if err != nil {
		return err
	}
	if action == fuzzySkipDir {
		return fs.SkipDir
	}
	if action == fuzzySkip {
		return nil
	}
	return service.collectFuzzyCandidates(ctx, path, state)
}

type fuzzyEntryAction uint8

const (
	fuzzyProcess fuzzyEntryAction = iota
	fuzzySkip
	fuzzySkipDir
)

func (service *Service) fuzzyEntryAction(ctx context.Context, path string, directoryEntry fs.DirEntry, walkErr error, request FuzzySearchRequest) (fuzzyEntryAction, error) {
	if walkErr != nil {
		return fuzzySkip, walkErr
	}
	if err := ctx.Err(); err != nil {
		return fuzzySkip, err
	}
	if service.isIgnored(path, request.IncludeIgnored) {
		if directoryEntry.IsDir() {
			return fuzzySkipDir, nil
		}
		return fuzzySkip, nil
	}
	if directoryEntry.IsDir() || directoryEntry.Type()&os.ModeSymlink != 0 || !matchesGlob(request.Glob, path) {
		return fuzzySkip, nil
	}
	return fuzzyProcess, nil
}

func (service *Service) collectFuzzyCandidates(ctx context.Context, path string, state *fuzzySearchState) error {
	slashPath := filepath.ToSlash(path)
	if state.request.Target == "path" || state.request.Target == "path_and_content" {
		if !state.add(fuzzyCandidate{value: slashPath, path: slashPath, matchKind: "path"}) {
			return errWalkLimit
		}
	}
	if state.request.Target == "path" {
		return nil
	}
	remaining := state.limits.maxScanBytes - state.scannedBytes
	if remaining <= 0 {
		state.truncated = true
		return errWalkLimit
	}
	readLimit := minInt(defaultMaxBytes, remaining)
	content, readTruncated, err := service.readBounded(ctx, path, readLimit)
	if err != nil {
		return err
	}
	state.scannedBytes += len(content)
	if readTruncated {
		state.truncated = true
	}
	if !state.addContentCandidates(content, slashPath, state.request.BeforeLines, state.request.AfterLines) {
		return errWalkLimit
	}
	if state.scannedBytes >= state.limits.maxScanBytes {
		state.truncated = true
		return errWalkLimit
	}
	return nil
}

func (state *fuzzySearchState) add(candidate fuzzyCandidate) bool {
	if len(state.candidates) >= state.limits.maxCandidates {
		state.truncated = true
		return false
	}
	state.candidates = append(state.candidates, candidate)
	return true
}

func (state *fuzzySearchState) addContentCandidates(content, path string, beforeLines, afterLines int) bool {
	lines := strings.Split(content, "\n")
	for lineNumber, line := range lines {
		if line == "" {
			continue
		}
		beforeStart := maxInt(0, lineNumber-beforeLines)
		afterEnd := minInt(len(lines), lineNumber+afterLines+1)
		if !state.add(fuzzyCandidate{
			value:     line,
			path:      path,
			line:      lineNumber + 1,
			text:      line,
			matchKind: "content",
			before:    append([]string(nil), lines[beforeStart:lineNumber]...),
			after:     append([]string(nil), lines[lineNumber+1:afterEnd]...),
		}) {
			return false
		}
	}
	return true
}

func rankFuzzyMatches(query string, candidates fuzzyCandidates, caseSensitive bool) []FuzzyMatch {
	ranked := fuzzy.FindFrom(query, candidates)
	matches := make([]FuzzyMatch, 0, len(ranked))
	for _, match := range ranked {
		if match.Index < 0 || match.Index >= len(candidates) {
			continue
		}
		candidate := candidates[match.Index]
		if caseSensitive && !hasExactCase(match.Str, query, match.MatchedIndexes) {
			continue
		}
		matches = append(matches, FuzzyMatch{Path: candidate.path, Line: candidate.line, Text: candidate.text, Score: match.Score, MatchKind: candidate.matchKind, MatchedIndexes: byteIndexesToRuneIndexes(match.Str, match.MatchedIndexes), Before: candidate.before, After: candidate.after})
	}
	slices.SortStableFunc(matches, func(first, second FuzzyMatch) int {
		if fuzzyMatchLess(first, second) {
			return -1
		}
		if fuzzyMatchLess(second, first) {
			return 1
		}
		return 0
	})
	return matches
}

func fuzzyMatchLess(first, second FuzzyMatch) bool {
	if first.Score != second.Score {
		return first.Score > second.Score
	}
	if first.Path != second.Path {
		return first.Path < second.Path
	}
	if first.Line != second.Line {
		return first.Line < second.Line
	}
	if first.MatchKind != second.MatchKind {
		return first.MatchKind < second.MatchKind
	}
	return first.Text < second.Text
}

func hasExactCase(candidate, query string, indexes []int) bool {
	queryRunes := []rune(query)
	if len(queryRunes) != len(indexes) {
		return false
	}
	for index, byteOffset := range indexes {
		if byteOffset < 0 || byteOffset >= len(candidate) {
			return false
		}
		candidateRune, _ := utf8.DecodeRuneInString(candidate[byteOffset:])
		if candidateRune != queryRunes[index] {
			return false
		}
	}
	return true
}

func byteIndexesToRuneIndexes(value string, indexes []int) []int {
	result := make([]int, 0, len(indexes))
	for _, index := range indexes {
		if index < 0 || index > len(value) {
			continue
		}
		result = append(result, utf8.RuneCountInString(value[:index]))
	}
	return result
}

func fuzzySearchAdapter(service *Service) toolAdapter {
	return toolAdapter{name: "fs.fuzzy_search", description: "Rank approximate matches in workspace paths and file lines. Use target=path for file discovery, target=content for line discovery, or path_and_content for both. Results are read-only, bounded, sorted by score (higher is better), and may be truncated. Fuzzy matches are discovery hints only; read and hash a file before editing it.", parameters: `{"type":"object","properties":{"query":{"type":"string","minLength":1,"maxLength":256},"path":{"type":"string","description":"Optional workspace-relative directory or file"},"glob":{"type":"string","description":"Optional file pattern such as *.go"},"target":{"type":"string","enum":["path","content","path_and_content"],"default":"path_and_content"},"case_sensitive":{"type":"boolean","default":false},"before_lines":{"type":"integer","minimum":0,"maximum":8,"default":0},"after_lines":{"type":"integer","minimum":0,"maximum":8,"default":0},"max_results":{"type":"integer","minimum":1,"maximum":100,"default":20},"max_candidates":{"type":"integer","minimum":1,"maximum":20000,"default":5000},"max_scan_bytes":{"type":"integer","minimum":1,"maximum":33554432,"default":8388608},"include_ignored":{"type":"boolean","default":false}},"required":["query"]}`, maxArguments: 11, execute: func(ctx context.Context, call tools.Call) (any, error) {
		var request FuzzySearchRequest
		if err := json.Unmarshal(call.Arguments, &request); err != nil {
			return nil, err
		}
		return service.FuzzySearch(ctx, request)
	}}
}
