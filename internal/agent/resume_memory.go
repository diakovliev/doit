package agent

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/diakovliev/doit/internal/model"
)

const (
	recentResumeHistoryItems = 12
	resumeSummaryGroupSize   = 6
	maxResumeSummaryBytes    = 1800
	maxResumeFactsBytes      = 1800
	maxResumeFactCount       = 12
)

var exactMemoryKeys = map[string]struct{}{
	"after_hashes":  {},
	"before_hashes": {},
	"changed_paths": {},
	"command":       {},
	"diagnostics":   {},
	"exit_code":     {},
	"hash":          {},
	"path":          {},
	"passed":        {},
	"paths":         {},
	"task":          {},
}

func compactResumeHistory(history []model.InputItem) []model.InputItem {
	for index := range history {
		if strings.HasPrefix(history[index].Content, "Compressed prior session memory.") {
			history[index].ContextPriority = model.ContextPriorityMemory
		} else {
			history[index].ContextPriority = model.ContextPriorityRecent
		}
	}
	start := len(history) - recentResumeHistoryItems
	if start <= 0 {
		return history
	}
	if history[start].Type == "function_call_output" {
		start--
	}
	memory := buildResumeMemory(history[:start])
	result := make([]model.InputItem, 0, len(history)-start+1)
	if memory != "" {
		result = append(result, model.InputItem{Type: "message", Role: "user", Content: memory, ContextPriority: model.ContextPriorityMemory})
	}
	return append(result, history[start:]...)
}

func buildResumeMemory(older []model.InputItem) string {
	segments := summarizeHistorySegments(older)
	facts := collectExactMemoryFacts(older)
	if len(segments) == 0 && len(facts) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("Compressed prior session memory. Recent turns below are exact; these older summaries are lossy. Use session.history with a focused query to retrieve original events.\n")
	if len(segments) > 0 {
		builder.WriteString("Earlier context, chronological segments:\n")
		appendBoundedLines(&builder, segments, maxResumeSummaryBytes)
	}
	if len(facts) > 0 {
		builder.WriteString("Exact facts from earlier tool activity:\n")
		appendBoundedLines(&builder, facts, maxResumeFactsBytes)
	}
	return strings.TrimSpace(builder.String())
}

func summarizeHistorySegments(history []model.InputItem) []string {
	segments := make([]string, 0, (len(history)+resumeSummaryGroupSize-1)/resumeSummaryGroupSize)
	for start := 0; start < len(history); start += resumeSummaryGroupSize {
		end := min(start+resumeSummaryGroupSize, len(history))
		parts := make([]string, 0, end-start)
		for _, item := range history[start:end] {
			if part := resumeItemSummary(item); part != "" {
				parts = append(parts, part)
			}
		}
		if len(parts) > 0 {
			segments = append(segments, fmt.Sprintf("segment %d: %s", len(segments)+1, compactText(strings.Join(parts, "; "), 360)))
		}
	}
	return rollupHistorySegments(segments)
}

func rollupHistorySegments(segments []string) []string {
	level := segments
	for len(level) > 4 {
		rolled := make([]string, 0, (len(level)+3)/4)
		for start := 0; start < len(level); start += 4 {
			end := min(start+4, len(level))
			rolled = append(rolled, fmt.Sprintf("period %d: %s", len(rolled)+1, compactText(strings.Join(level[start:end], "; "), 420)))
		}
		level = rolled
	}
	return level
}

func resumeItemSummary(item model.InputItem) string {
	switch item.Type {
	case "message":
		if strings.HasPrefix(item.Content, "Compressed prior session memory.") {
			return "previous memory: " + compactText(item.Content, 1000)
		}
		content := compactText(item.Content, 180)
		if content == "" {
			return ""
		}
		return item.Role + ": " + content
	case "function_call":
		return "tool call: " + item.Name
	case "function_call_output":
		return "tool result: " + compactText(item.Output, 140)
	default:
		return ""
	}
}

func collectExactMemoryFacts(history []model.InputItem) []string {
	facts := make([]string, 0, maxResumeFactCount)
	seen := make(map[string]struct{})
	usedBytes := 0
	for _, item := range history {
		collectExactFactsFromItem(item, &facts, seen, &usedBytes)
		if len(facts) >= maxResumeFactCount {
			break
		}
	}
	return facts
}

func collectExactFactsFromItem(item model.InputItem, facts *[]string, seen map[string]struct{}, usedBytes *int) {
	switch item.Type {
	case "message":
		for _, line := range strings.Split(item.Content, "\n") {
			if strings.HasPrefix(line, "- ") && exactFactLine(line[2:]) {
				appendExactFactText(facts, seen, usedBytes, line[2:])
			}
		}
	case "function_call":
		if item.Arguments != "" {
			appendExactFact(facts, seen, usedBytes, "tool "+item.Name+" arguments", json.RawMessage(item.Arguments))
		}
	case "function_call_output":
		var value any
		if json.Unmarshal([]byte(item.Output), &value) == nil {
			collectExactValues(value, "", facts, seen, usedBytes)
		}
	}
}

func exactFactLine(line string) bool {
	separator := strings.Index(line, " = ")
	if separator <= 0 {
		return false
	}
	label := strings.TrimPrefix(line[:separator], "tool ")
	if strings.HasSuffix(line[:separator], " arguments") {
		return true
	}
	_, ok := exactMemoryKeys[label]
	return ok
}

func appendExactFactText(facts *[]string, seen map[string]struct{}, usedBytes *int, line string) {
	if len(*facts) >= maxResumeFactCount || *usedBytes+len(line) > maxResumeFactsBytes {
		return
	}
	if _, exists := seen[line]; exists {
		return
	}
	seen[line] = struct{}{}
	*facts = append(*facts, line)
	*usedBytes += len(line)
}

func collectExactValues(value any, parent string, facts *[]string, seen map[string]struct{}, usedBytes *int) {
	if len(*facts) >= maxResumeFactCount {
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			child := typed[key]
			if _, preserve := exactMemoryKeys[key]; preserve {
				appendExactFact(facts, seen, usedBytes, key, child)
			}
			collectExactValues(child, key, facts, seen, usedBytes)
		}
	case []any:
		for _, child := range typed {
			collectExactValues(child, parent, facts, seen, usedBytes)
		}
	}
}

func appendExactFact(facts *[]string, seen map[string]struct{}, usedBytes *int, label string, value any) {
	if len(*facts) >= maxResumeFactCount {
		return
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return
	}
	line := label + " = " + string(encoded)
	if len(line) > 320 || *usedBytes+len(line) > maxResumeFactsBytes {
		return
	}
	if _, exists := seen[line]; exists {
		return
	}
	seen[line] = struct{}{}
	*facts = append(*facts, line)
	*usedBytes += len(line)
}

func appendBoundedLines(builder *strings.Builder, lines []string, limit int) {
	used := 0
	for _, line := range lines {
		line = "- " + line + "\n"
		if used+len(line) > limit {
			break
		}
		builder.WriteString(line)
		used += len(line)
	}
}

func compactText(value string, maxLength int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= maxLength {
		return value
	}
	return value[:maxLength] + "..."
}
