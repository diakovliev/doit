package cli

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

type testHandler struct {
	runInvocation   Invocation
	agentInvocation Invocation
}

func (handler *testHandler) Run(_ context.Context, invocation Invocation, _ io.Reader, _ io.Writer) error {
	handler.runInvocation = invocation
	return nil
}

func (handler *testHandler) Agent(_ context.Context, invocation Invocation, _ io.Reader, _ io.Writer) error {
	handler.agentInvocation = invocation
	return nil
}

func TestParseRunOptions(t *testing.T) {
	invocation, err := Parse([]string{"-C", "workspace", "--format", "json", "-p", "local", "-m", "test-model", "--ephemeral", "--timeout", "2s", "run", "inspect", "files"})
	if err != nil {
		t.Fatalf("parse invocation: %v", err)
	}
	if invocation.Command != "run" || invocation.Request != "inspect files" || invocation.Directory != "workspace" || invocation.Format != "json" || invocation.Profile != "local" || invocation.Model != "test-model" || !invocation.Ephemeral || invocation.Timeout != 2*time.Second {
		t.Fatalf("unexpected invocation: %+v", invocation)
	}
}

func TestParseNoCommandUsesAgent(t *testing.T) {
	invocation, err := Parse(nil)
	if err != nil {
		t.Fatalf("parse empty invocation: %v", err)
	}
	if invocation.Command != "agent" {
		t.Fatalf("expected agent command, got %q", invocation.Command)
	}
}

func TestApplicationHandlesHelpAndVersion(t *testing.T) {
	application := Application{Version: "test-version", Handler: &testHandler{}}
	var helpOutput strings.Builder
	if status := application.Execute([]string{"--help"}, strings.NewReader(""), &helpOutput, io.Discard); status != 0 {
		t.Fatalf("expected help success, got %d", status)
	}
	if !strings.Contains(helpOutput.String(), "Usage: doit") {
		t.Fatalf("expected help output, got %q", helpOutput.String())
	}

	var versionOutput strings.Builder
	if status := application.Execute([]string{"--version"}, strings.NewReader(""), &versionOutput, io.Discard); status != 0 {
		t.Fatalf("expected version success, got %d", status)
	}
	if versionOutput.String() != "test-version\n" {
		t.Fatalf("unexpected version output: %q", versionOutput.String())
	}
}

func TestApplicationDispatchesRun(t *testing.T) {
	handler := &testHandler{}
	application := Application{Version: "test", Handler: handler}
	status := application.Execute([]string{"run", "hello"}, strings.NewReader(""), io.Discard, io.Discard)
	if status != 0 || handler.runInvocation.Request != "hello" {
		t.Fatalf("unexpected status or invocation: status=%d invocation=%+v", status, handler.runInvocation)
	}
}

func TestParseRejectsInvalidFormat(t *testing.T) {
	if _, err := Parse([]string{"--format", "yaml", "run", "hello"}); err == nil {
		t.Fatal("expected invalid format to fail")
	}
}
