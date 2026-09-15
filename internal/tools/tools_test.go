package tools

import (
	"context"
	"strings"
	"testing"
	"time"
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

type contractTestTool struct {
	definition Definition
	result     Result
	delay      time.Duration
}

func (tool contractTestTool) Definition() Definition {
	return tool.definition
}

func (tool contractTestTool) Execute(ctx context.Context, _ Call) Result {
	if tool.delay > 0 {
		timer := time.NewTimer(tool.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return Result{Status: StatusSucceeded}
		}
	}
	return tool.result
}

func TestRegistryEnforcesToolContract(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(contractTestTool{definition: Definition{Name: "bounded", Risk: RiskReadOnly, Parameters: []byte(`{"type":"object","required":["first"]}`), MaxArguments: 1, MaxOutputBytes: 8}, result: Result{Status: StatusSucceeded, Data: strings.Repeat("x", 9)}}); err != nil {
		t.Fatalf("register bounded tool: %v", err)
	}
	tool, exists := registry.Lookup("bounded")
	if !exists {
		t.Fatal("bounded tool was not registered")
	}
	tooMany := tool.Execute(context.Background(), Call{Arguments: []byte(`{"first":1,"second":2}`)})
	if tooMany.Status != StatusFailed {
		t.Fatalf("expected argument-limit failure: %+v", tooMany)
	}
	missing := tool.Execute(context.Background(), Call{})
	if missing.Status != StatusFailed {
		t.Fatalf("expected required-argument failure: %+v", missing)
	}
	tooLarge := tool.Execute(context.Background(), Call{Arguments: []byte(`{"first":1}`)})
	if tooLarge.Status != StatusFailed || !tooLarge.Truncated || tooLarge.Duration <= 0 {
		t.Fatalf("expected output-limit failure: %+v", tooLarge)
	}
}

func TestRegistryEnforcesToolTimeout(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(contractTestTool{definition: Definition{Name: "slow", Risk: RiskReadOnly, Timeout: time.Millisecond}, delay: 20 * time.Millisecond}); err != nil {
		t.Fatalf("register slow tool: %v", err)
	}
	tool, _ := registry.Lookup("slow")
	result := tool.Execute(context.Background(), Call{})
	if result.Status != StatusCancelled {
		t.Fatalf("expected timeout cancellation: %+v", result)
	}
}

func TestRegistrySelectsCapabilityProfiles(t *testing.T) {
	registry := NewRegistry()
	for _, definition := range []Definition{
		{Name: "fs.read", Risk: RiskReadOnly},
		{Name: "code.apply_patch", Risk: RiskWrite},
		{Name: "process.run", Risk: RiskProcess},
		{Name: "git.commit", Risk: RiskDestructive},
	} {
		if err := registry.Register(testTool{definition: definition}); err != nil {
			t.Fatalf("register %s: %v", definition.Name, err)
		}
	}
	selected, err := registry.Select("edit")
	if err != nil {
		t.Fatalf("select edit profile: %v", err)
	}
	if _, exists := selected.Lookup("fs.read"); !exists {
		t.Fatal("edit profile omitted inspection tool")
	}
	if _, exists := selected.Lookup("code.apply_patch"); !exists {
		t.Fatal("edit profile omitted write tool")
	}
	if _, exists := selected.Lookup("process.run"); exists {
		t.Fatal("edit profile exposed validation tool")
	}
	if _, err := registry.Select("unknown"); err == nil {
		t.Fatal("expected unknown profile to fail")
	}
}

func TestRegistrySelectsSmallEditProfile(t *testing.T) {
	registry := NewRegistry()
	for _, definition := range []Definition{
		{Name: "fs.read", Risk: RiskReadOnly},
		{Name: "code.replace_exact", Risk: RiskWrite},
		{Name: "code.apply_patch", Risk: RiskWrite},
		{Name: "process.run", Risk: RiskProcess},
	} {
		if err := registry.Register(testTool{definition: definition}); err != nil {
			t.Fatalf("register %s: %v", definition.Name, err)
		}
	}
	selected, err := registry.Select("small-edit")
	if err != nil {
		t.Fatalf("select small-edit profile: %v", err)
	}
	if _, exists := selected.Lookup("fs.read"); !exists {
		t.Fatal("small-edit profile omitted inspection tool")
	}
	if _, exists := selected.Lookup("code.replace_exact"); !exists {
		t.Fatal("small-edit profile omitted exact edit tool")
	}
	if _, exists := selected.Lookup("code.apply_patch"); exists {
		t.Fatal("small-edit profile exposed broad patch tool")
	}
	if _, exists := selected.Lookup("process.run"); exists {
		t.Fatal("small-edit profile exposed process tool")
	}
}

func TestRegistryAddsDiagnosticRecoveryMetadata(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(contractTestTool{definition: Definition{Name: "edit", Risk: RiskWrite}, result: Result{Status: StatusFailed, Diagnostics: []Diagnostic{{Level: "error", Message: "expected exactly one match, found 2"}}}}); err != nil {
		t.Fatalf("register edit tool: %v", err)
	}
	tool, _ := registry.Lookup("edit")
	result := tool.Execute(context.Background(), Call{})
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != "edit_conflict" || result.Diagnostics[0].NextAction == "" || result.Diagnostics[0].Retryable {
		t.Fatalf("missing edit recovery metadata: %+v", result.Diagnostics)
	}
}
