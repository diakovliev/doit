package model

import "testing"

func TestRequestValidationRequiresModel(t *testing.T) {
	if err := (Request{}).Validate(); err == nil {
		t.Fatal("expected missing model to fail validation")
	}
}

func TestRequestValidationAcceptsTextRequest(t *testing.T) {
	request := Request{Model: "fake-model", Input: []InputItem{{Type: "message", Role: "user", Content: "hello"}}}
	if err := request.Validate(); err != nil {
		t.Fatalf("expected valid request, got %v", err)
	}
}

func TestStripToolArgumentEchoes(t *testing.T) {
	arguments := `{"path":"docs"}`
	text := `I inspected the request. { "path" : "docs" }`
	cleaned := StripToolArgumentEchoes(text, []ToolCall{{Name: "fs.list", Arguments: arguments}})
	if cleaned != "I inspected the request." {
		t.Fatalf("duplicate tool arguments were not stripped: %q", cleaned)
	}
	plainJSON := `The response contains {"path":"docs"} as an example.`
	if got := StripToolArgumentEchoes(plainJSON, []ToolCall{{Name: "fs.list", Arguments: `{"path":"other"}`}}); got != plainJSON {
		t.Fatalf("unrelated JSON prose was changed: %q", got)
	}
	if got := StripToolArgumentEchoes(plainJSON, nil); got != plainJSON {
		t.Fatalf("text without function calls was changed: %q", got)
	}
}
