package model

import (
	"bytes"
	"encoding/json"
	"strings"
)

// StripToolArgumentEchoes removes JSON objects from public text only when they
// exactly duplicate structured tool arguments in the same model response.
func StripToolArgumentEchoes(text string, calls []ToolCall) string {
	if text == "" || len(calls) == 0 {
		return text
	}
	expected := toolArgumentObjects(calls)
	if len(expected) == 0 {
		return text
	}
	cleaned, changed := removeToolArgumentObjects(text, expected)
	if !changed {
		return text
	}
	cleaned = strings.NewReplacer(" .", ".", " ,", ",", " ;", ";", " :", ":").Replace(cleaned)
	return strings.TrimSpace(cleaned)
}

func toolArgumentObjects(calls []ToolCall) map[string]struct{} {
	expected := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		if compact, ok := compactJSONObject(call.Arguments); ok {
			expected[compact] = struct{}{}
		}
	}
	return expected
}

func removeToolArgumentObjects(text string, expected map[string]struct{}) (string, bool) {
	var cleaned strings.Builder
	last := 0
	for index := 0; index < len(text); index++ {
		if text[index] != '{' {
			continue
		}
		end := jsonObjectEnd(text, index)
		if end < 0 {
			continue
		}
		compact, ok := compactJSONObject(text[index:end])
		if !ok {
			continue
		}
		if _, duplicate := expected[compact]; !duplicate {
			index = end - 1
			continue
		}
		cleaned.WriteString(text[last:index])
		last = end
		index = end - 1
	}
	if last == 0 {
		return text, false
	}
	cleaned.WriteString(text[last:])
	return cleaned.String(), true
}

func compactJSONObject(value string) (string, bool) {
	var compact bytes.Buffer
	if json.Compact(&compact, []byte(value)) != nil {
		return "", false
	}
	if len(compact.Bytes()) == 0 || compact.Bytes()[0] != '{' {
		return "", false
	}
	return compact.String(), true
}

func jsonObjectEnd(value string, start int) int {
	depth := 0
	quoted := false
	escaped := false
	for index := start; index < len(value); index++ {
		current := value[index]
		if quoted {
			if escaped {
				escaped = false
				continue
			}
			switch current {
			case '\\':
				escaped = true
			case '"':
				quoted = false
			}
			continue
		}
		switch current {
		case '"':
			quoted = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return index + 1
			}
		}
	}
	return -1
}
