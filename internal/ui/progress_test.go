package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/diakovliev/doit/internal/agent"
)

func TestProgressLineReplacesStatusBelowMetrics(t *testing.T) {
	var output bytes.Buffer
	line := &ProgressLine{writer: &output, enabled: true, replace: true}
	line.Update(agent.ProgressEvent{Phase: "session", Message: "session started"})
	line.Update(agent.ProgressEvent{Phase: "metrics", Message: "step=1 context=1200 generation_tps=unknown request_tps=2.50"})
	line.Update(agent.ProgressEvent{Phase: "model", Message: "first action"})
	line.Update(agent.ProgressEvent{Phase: "tool", Message: "second action"})
	line.Clear()
	for _, expected := range []string{"[doit] metrics: step=1 context=1200", "[doit] model: first action", "[doit] tool: second action", "\x1b[1A\r\x1b[2K[doit] metrics:"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("replaceable UI output missing %q: %q", expected, output.String())
		}
	}
}

func TestProgressLineWritesCapturedOutputLineOriented(t *testing.T) {
	var output bytes.Buffer
	line := NewProgressLine(&output, true)
	line.Update(agent.ProgressEvent{Phase: "model", Message: "captured action"})
	if output.String() != "[doit] model: captured action\n" {
		t.Fatalf("unexpected captured output: %q", output.String())
	}
}

func TestProgressLineTruncatesToCurrentTerminalWidth(t *testing.T) {
	line := &ProgressLine{width: 12}
	if got := line.limit("[doit] metrics: context=12000"); got != "[doit] me..." {
		t.Fatalf("progress text was not bounded to terminal width: %q", got)
	}
}
