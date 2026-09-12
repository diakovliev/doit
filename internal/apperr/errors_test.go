package apperr

import (
	"errors"
	"testing"
)

func TestWrapPreservesCauseAndKind(t *testing.T) {
	testCause := errors.New("request failed")
	applicationError := Wrap(KindBackend, "model.create", testCause)

	if !errors.Is(applicationError, testCause) {
		t.Fatal("expected wrapped cause to be discoverable")
	}
	if got := KindOf(applicationError); got != KindBackend {
		t.Fatalf("expected kind %q, got %q", KindBackend, got)
	}
	if got := applicationError.Error(); got != "model.create: request failed" {
		t.Fatalf("unexpected error text: %q", got)
	}
}

func TestNewErrorUsesMessage(t *testing.T) {
	applicationError := New(KindUsage, "cli.parse", "missing request")

	if got := applicationError.Error(); got != "cli.parse: missing request" {
		t.Fatalf("unexpected error text: %q", got)
	}
	if got := KindOf(applicationError); got != KindUsage {
		t.Fatalf("expected kind %q, got %q", KindUsage, got)
	}
}

func TestKindOfUnknownError(t *testing.T) {
	if got := KindOf(errors.New("unknown")); got != KindUnknown {
		t.Fatalf("expected kind %q, got %q", KindUnknown, got)
	}
}
