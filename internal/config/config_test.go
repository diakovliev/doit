package config

import (
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
  "format": "human",
  "profiles": {"project": {"api_root": "https://project.example/v1", "model": "project-model"}},
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
	if configuration.Profile != "project" || configuration.Format != "json" || !configuration.Ephemeral {
		t.Fatalf("unexpected effective config: %+v", configuration)
	}
	if configuration.Token.MaxInputTokens != 1000 || configuration.Token.MaxOutputTokens != 2000 {
		t.Fatalf("unexpected token budgets: %+v", configuration.Token)
	}
	profile, err := configuration.SelectedProfile()
	if err != nil {
		t.Fatalf("select profile: %v", err)
	}
	if profile.Model != "env-model" {
		t.Fatalf("expected environment model override, got %q", profile.Model)
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

func writeConfigFile(t *testing.T, filePath, contents string) {
	t.Helper()
	if err := os.WriteFile(filePath, []byte(contents), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}
