package contextbuilder

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/diakovliev/doit/internal/model"
)

const (
	rollingMemoryPrefix     = "Compressed prior session memory."
	rollingMemorySummaryCap = 1800
	rollingMemoryFactsCap   = 1800
	rollingMemoryFactCount  = 12
)

var rollingFactKeys = map[string]struct{}{
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

func updateRollingMemory(input, removed []model.InputItem) []model.InputItem {
	summaries := make([]string, 0)
	facts := make([]string, 0, rollingMemoryFactCount)
	seenFacts := make(map[string]struct{})
	memoryIndex := -1
	for index, item := range input {
		if item.Type != "message" || !strings.HasPrefix(item.Content, rollingMemoryPrefix) {
			continue
		}
		memoryIndex = index
		readRollingMemory(item.Content, &summaries, &facts, seenFacts)
		break
	}
	for _, item := range removed {
		if summary := summarizeTrimmedItem(item); summary != "" {
			summaries = append(summaries, summary)
		}
		collectTrimmedFacts(item, &facts, seenFacts)
	}
	content := renderRollingMemory(summaries, facts)
	item := model.InputItem{Type: "message", Role: "user", Content: content, ContextPriority: model.ContextPriorityRollingMemory}
	if memoryIndex >= 0 {
		input[memoryIndex] = item
		return input
	}
	return append([]model.InputItem{item}, input...)
}

func readRollingMemory(content string, summaries, facts *[]string, seen map[string]struct{}) {
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		value := line[2:]
		if isRollingFact(value) {
			appendRollingFact(facts, seen, value)
		} else {
			*summaries = append(*summaries, value)
		}
	}
}

func isRollingFact(line string) bool {
	separator := strings.Index(line, " = ")
	if separator < 0 {
		return false
	}
	label := strings.TrimPrefix(line[:separator], "tool ")
	if strings.HasSuffix(line[:separator], " arguments") {
		return true
	}
	_, exists := rollingFactKeys[label]
	return exists
}

func summarizeTrimmedItem(item model.InputItem) string {
	switch item.Type {
	case "message":
		text := compactMemoryText(item.Content, 180)
		if text == "" || strings.HasPrefix(item.Content, rollingMemoryPrefix) {
			return ""
		}
		return item.Role + ": " + text
	case "function_call":
		return "tool call: " + item.Name
	case "function_call_output":
		return "tool result: " + compactMemoryText(item.Output, 140)
	default:
		return ""
	}
}

func collectTrimmedFacts(item model.InputItem, facts *[]string, seen map[string]struct{}) {
	if item.Type == "function_call" && len(item.Arguments) <= 320 && item.Arguments != "" {
		appendRollingFact(facts, seen, "tool "+item.Name+" arguments = "+item.Arguments)
	}
	if item.Type != "function_call_output" {
		return
	}
	var value any
	if json.Unmarshal([]byte(item.Output), &value) == nil {
		collectRollingValues(value, facts, seen)
	}
}

func collectRollingValues(value any, facts *[]string, seen map[string]struct{}) {
	if len(*facts) >= rollingMemoryFactCount {
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
			if _, preserve := rollingFactKeys[key]; preserve {
				encoded, err := json.Marshal(child)
				if err == nil && len(encoded) <= 320 {
					appendRollingFact(facts, seen, key+" = "+string(encoded))
				}
			}
			collectRollingValues(child, facts, seen)
		}
	case []any:
		for _, child := range typed {
			collectRollingValues(child, facts, seen)
		}
	}
}

func appendRollingFact(facts *[]string, seen map[string]struct{}, fact string) {
	if len(*facts) >= rollingMemoryFactCount || len(fact) > 320 {
		return
	}
	if _, exists := seen[fact]; exists {
		return
	}
	seen[fact] = struct{}{}
	*facts = append(*facts, fact)
}

func renderRollingMemory(summaries, facts []string) string {
	summaries = boundRollingLines(summaries, rollingMemorySummaryCap)
	facts = boundRollingLines(facts, rollingMemoryFactsCap)
	var builder strings.Builder
	builder.WriteString(rollingMemoryPrefix + " Recent turns below are exact; older summaries are lossy. Use session.history to retrieve original events.\n")
	if len(summaries) > 0 {
		builder.WriteString("Chronological rolling summary:\n")
		writeRollingLines(&builder, summaries)
	}
	if len(facts) > 0 {
		builder.WriteString("Exact facts from earlier tool activity:\n")
		writeRollingLines(&builder, facts)
	}
	return strings.TrimSpace(builder.String())
}

func boundRollingLines(lines []string, maximum int) []string {
	result := make([]string, 0, len(lines))
	used := 0
	for index := len(lines) - 1; index >= 0; index-- {
		line := lines[index]
		if used+len(line)+1 > maximum {
			continue
		}
		result = append(result, line)
		used += len(line) + 1
	}
	slices.Reverse(result)
	return result
}

func writeRollingLines(builder *strings.Builder, lines []string) {
	for _, line := range lines {
		builder.WriteString("- ")
		builder.WriteString(line)
		builder.WriteString("\n")
	}
}

func compactMemoryText(value string, maximum int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > maximum {
		return value[:maximum] + "..."
	}
	return value
}
