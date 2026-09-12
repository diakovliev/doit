package app

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diakovliev/doit/internal/cli"
)

func TestRunCompletesAgainstDeterministicResponsesBackend(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/responses" {
			http.Error(writer, "unexpected path", http.StatusNotFound)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"resp-test","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"repository explained"}]}],"usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}`))
	}))
	defer server.Close()

	workspace := t.TempDir()
	configPath := filepath.Join(workspace, "config.json")
	configContents := `{"default_profile":"local","profiles":{"local":{"api_root":"` + server.URL + `/v1","model":"test-model"}}}`
	if err := os.WriteFile(configPath, []byte(configContents), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("DOIT_CONFIG_FILE", configPath)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	status := cli.RunWithHandler([]string{"-C", workspace, "run", "explain", "this", "repository"}, strings.NewReader(""), &stdout, &stderr, Handler{})
	if status != 0 {
		t.Fatalf("expected successful CLI run, status=%d stderr=%q", status, stderr.String())
	}
	if !strings.Contains(stdout.String(), "repository explained") || !strings.Contains(stdout.String(), "input_tokens=4") {
		t.Fatalf("unexpected CLI output: %q", stdout.String())
	}
}
