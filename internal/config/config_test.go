package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadUsesConfiguredPrecedence(t *testing.T) {
	workspace := t.TempDir()
	projectFile := filepath.Join(workspace, "project.json")
	userFile := filepath.Join(workspace, "user.json")
	writeConfigFile(t, projectFile, `{
  "default_profile": "project",
	"tool_profile": "edit",
	  "format": "human",
	  "mcp_servers": {"fixture": {"transport": "stdio", "command": "fixture-mcp"}},
  "profiles": {"project": {"api_root": "https://project.example/v1", "model": "project-model"}},
	"tasks": {"probe": {"executable": "git", "arguments": ["--version"]}},
  "token": {"max_input_tokens": 1000}
}`)
	writeConfigFile(t, userFile, `{
  "default_profile": "user",
  "profiles": {"user": {"api_root": "https://user.example/v1", "model": "user-model"}},
  "token": {"max_output_tokens": 2000}
}`)
	ephemeral := true
	configuration, err := Load(LoadOptions{
		Workspace:   workspace,
		ProjectFile: projectFile,
		UserFile:    userFile,
		Environment: map[string]string{"DOIT_PROFILE": "user", "DOIT_MODEL": "env-model"},
		Overrides:   Overrides{Profile: "project", Format: "json", Ephemeral: &ephemeral},
	})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	assertPrecedenceConfig(t, configuration)
	profile, err := configuration.SelectedProfile()
	if err != nil {
		t.Fatalf("select profile: %v", err)
	}
	if profile.Model != "env-model" {
		t.Fatalf("expected environment model override, got %q", profile.Model)
	}
}

func assertPrecedenceConfig(t *testing.T, configuration Config) {
	t.Helper()
	if configuration.Profile != "project" || configuration.Format != "json" || !configuration.Ephemeral {
		t.Fatalf("unexpected effective config: %+v", configuration)
	}
	if configuration.ToolProfile != "edit" {
		t.Fatalf("expected configured tool profile, got %q", configuration.ToolProfile)
	}
	if configuration.MCPServers["fixture"].Command != "fixture-mcp" {
		t.Fatalf("expected configured MCP server: %+v", configuration.MCPServers)
	}
	if configuration.Token.MaxInputTokens != 1000 || configuration.Token.MaxOutputTokens != 2000 {
		t.Fatalf("unexpected token budgets: %+v", configuration.Token)
	}
	if configuration.Tasks["probe"].Executable != "git" {
		t.Fatalf("expected configured project task: %+v", configuration.Tasks)
	}
	assertExecutionDefaults(t, configuration.Execution)
}

func assertExecutionDefaults(t *testing.T, execution ExecutionConfig) {
	t.Helper()
	if execution.MaxRounds != 128 || execution.RequestTimeoutMs != 600000 {
		t.Fatalf("expected relaxed execution defaults: %+v", execution)
	}
}

func TestLoadMergesExecutionLimits(t *testing.T) {
	workspace := t.TempDir()
	projectFile := filepath.Join(workspace, "project.json")
	writeConfigFile(t, projectFile, `{"execution":{"request_timeout_ms":900000,"max_rounds":256,"process_default_timeout_ms":180000,"process_max_timeout_ms":3600000}}`)
	configuration, err := Load(LoadOptions{Workspace: workspace, ProjectFile: projectFile, UserFile: filepath.Join(workspace, "missing-user.json")})
	if err != nil {
		t.Fatalf("load execution limits: %v", err)
	}
	if configuration.Execution.RequestTimeoutMs != 900000 || configuration.Execution.MaxRounds != 256 || configuration.Execution.ProcessMaxTimeoutMs != 3600000 {
		t.Fatalf("execution limits were not merged: %+v", configuration.Execution)
	}
}

func TestLoadRejectsInvalidJSON(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "config.json")
	writeConfigFile(t, filePath, `{invalid`)

	_, err := Load(LoadOptions{Workspace: t.TempDir(), ProjectFile: filePath, UserFile: filepath.Join(t.TempDir(), "missing.json")})
	if err == nil {
		t.Fatal("expected invalid JSON to fail")
	}
}

func TestLoadUsesExplicitConfigFileOverride(t *testing.T) {
	workspace := t.TempDir()
	explicitFile := filepath.Join(workspace, "explicit.json")
	writeConfigFile(t, explicitFile, `{
  "default_profile": "explicit",
  "profiles": {"explicit": {"api_root": "https://explicit.example/v1", "model": "explicit-model", "api_key_env": "DOIT_TEST_KEY"}}
}`)

	configuration, err := Load(LoadOptions{
		Workspace:   workspace,
		ProjectFile: filepath.Join(workspace, "missing-project.json"),
		UserFile:    filepath.Join(workspace, "missing-user.json"),
		Environment: map[string]string{"DOIT_CONFIG_FILE": explicitFile},
	})
	if err != nil {
		t.Fatalf("load explicit config: %v", err)
	}
	profile, err := configuration.SelectedProfile()
	if err != nil {
		t.Fatalf("select explicit profile: %v", err)
	}
	if profile.Model != "explicit-model" || profile.APIKeyEnv != "DOIT_TEST_KEY" {
		t.Fatalf("unexpected explicit profile: %+v", profile)
	}
}

func TestBackendProfileValidation(t *testing.T) {
	profile := BackendProfile{APIRoot: "https://example.test/v1", Model: "model"}
	if err := profile.Validate(); err != nil {
		t.Fatalf("expected valid profile, got %v", err)
	}
	profile.Model = ""
	if err := profile.Validate(); err == nil {
		t.Fatal("expected missing model to fail")
	}
}

func TestBackendProfileAcceptsReasoningRequestParameters(t *testing.T) {
	profile := BackendProfile{
		APIRoot: "https://example.test/v1",
		Model:   "model",
		RequestParameters: map[string]json.RawMessage{
			"reasoning": json.RawMessage(`{"effort":"high"}`),
		},
	}
	if err := profile.Validate(); err != nil {
		t.Fatalf("expected reasoning request parameters to validate: %v", err)
	}
	profile.RequestParameters["model"] = json.RawMessage(`"unsafe-override"`)
	if err := profile.Validate(); err == nil {
		t.Fatal("expected core request override to be rejected")
	}
}

func TestMCPServerValidation(t *testing.T) {
	tests := []struct {
		name   string
		server MCPServerConfig
		valid  bool
	}{
		{name: "stdio", server: MCPServerConfig{Transport: "stdio", Command: "fixture"}, valid: true},
		{name: "http", server: MCPServerConfig{Transport: "streamable-http", URL: "https://example.test/mcp", AllowNetwork: true}, valid: true},
		{name: "missing command", server: MCPServerConfig{Transport: "stdio"}},
		{name: "network opt in", server: MCPServerConfig{Transport: "streamable-http", URL: "https://example.test/mcp"}},
		{name: "invalid URL", server: MCPServerConfig{Transport: "streamable-http", URL: "file:///tmp/mcp", AllowNetwork: true}},
		{name: "timeout bound", server: MCPServerConfig{Transport: "stdio", Command: "fixture", TimeoutMs: 600001}},
		{name: "header bound", server: MCPServerConfig{Transport: "stdio", Command: "fixture", Headers: map[string]string{"X-Test": "line\nbreak"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.server.Validate()
			if test.valid && err != nil {
				t.Fatalf("expected valid MCP server, got %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("expected invalid MCP server")
			}
		})
	}
}

func writeConfigFile(t *testing.T, filePath, contents string) {
	t.Helper()
	if err := os.WriteFile(filePath, []byte(contents), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}
