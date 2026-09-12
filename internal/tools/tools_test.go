package tools

import (
	"context"
	"testing"
)

type testTool struct {
	definition Definition
}

func (tool testTool) Definition() Definition {
	return tool.definition
}

func (tool testTool) Execute(context.Context, Call) Result {
	return Result{Status: StatusSucceeded}
}

func TestRegistrySortsDefinitionsAndRejectsDuplicates(t *testing.T) {
	registry := NewRegistry()
	first := testTool{definition: Definition{Name: "z.read", Risk: RiskReadOnly}}
	second := testTool{definition: Definition{Name: "a.read", Risk: RiskReadOnly}}

	if err := registry.Register(first); err != nil {
		t.Fatalf("register first tool: %v", err)
	}
	if err := registry.Register(second); err != nil {
		t.Fatalf("register second tool: %v", err)
	}
	if err := registry.Register(first); err == nil {
		t.Fatal("expected duplicate registration to fail")
	}

	definitions := registry.Definitions()
	if len(definitions) != 2 || definitions[0].Name != "a.read" || definitions[1].Name != "z.read" {
		t.Fatalf("unexpected definition order: %+v", definitions)
	}
}
