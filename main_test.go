package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunWiresCLIArgumentsAndExitCodes(t *testing.T) {
	var output bytes.Buffer
	var errors bytes.Buffer
	if status := run([]string{"--help"}, strings.NewReader(""), &output, &errors); status != 0 {
		t.Fatalf("help returned status %d: %q", status, errors.String())
	}
	if !strings.Contains(output.String(), "Usage: doit") {
		t.Fatalf("help output was not forwarded: %q", output.String())
	}

	output.Reset()
	errors.Reset()
	if status := run([]string{"--unknown-option"}, strings.NewReader(""), &output, &errors); status != 2 {
		t.Fatalf("invalid option returned status %d: stdout=%q stderr=%q", status, output.String(), errors.String())
	}
	if !strings.Contains(errors.String(), "unknown option") {
		t.Fatalf("invalid option error was not forwarded: %q", errors.String())
	}
}
