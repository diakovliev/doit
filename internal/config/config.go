// Package config loads validated CLI and backend configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/diakovliev/doit/internal/apperr"
)

// BackendProfile selects one OpenAI-compatible model backend.
type BackendProfile struct {
	APIRoot   string            `json:"api_root"`
	Model     string            `json:"model"`
	APIKeyEnv string            `json:"api_key_env,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	RateLimit RateLimitConfig   `json:"rate_limit,omitempty"`
}

// RateLimitConfig controls bounded client-side retries after provider throttling.
type RateLimitConfig struct {
	MaxRetries        int `json:"max_retries,omitempty"`
	InitialBackoffMs  int `json:"initial_backoff_ms,omitempty"`
	MaxBackoffMs      int `json:"max_backoff_ms,omitempty"`
	MinIntervalMs     int `json:"min_interval_ms,omitempty"`
	TokensPerMinute   int `json:"tokens_per_minute,omitempty"`
	RequestsPerMinute int `json:"requests_per_minute,omitempty"`
}

// TokenBudget contains request and session token limits.
type TokenBudget struct {
	MaxInputTokens   int `json:"max_input_tokens"`
	MaxOutputTokens  int `json:"max_output_tokens"`
	MaxSessionTokens int `json:"max_session_tokens"`
}

// TaskConfig describes one project-defined allowlisted process task.
type TaskConfig struct {
	Executable  string   `json:"executable"`
	Arguments   []string `json:"arguments,omitempty"`
	Environment []string `json:"environment,omitempty"`
	Kind        string   `json:"kind,omitempty"`
}

// Config is the effective configuration after all sources are merged.
type Config struct {
	Workspace   string                    `json:"workspace"`
	Profile     string                    `json:"profile"`
	ToolProfile string                    `json:"tool_profile"`
	Format      string                    `json:"format"`
	Ephemeral   bool                      `json:"ephemeral"`
	Profiles    map[string]BackendProfile `json:"profiles"`
	Tasks       map[string]TaskConfig     `json:"tasks"`
	Token       TokenBudget               `json:"token"`
}

// Overrides are values supplied by environment variables or CLI flags.
type Overrides struct {
	Profile   string
	Model     string
	APIRoot   string
	APIKeyEnv string
	Format    string
	Ephemeral *bool
}

// LoadOptions controls configuration source locations and overrides.
type LoadOptions struct {
	Workspace   string
	ProjectFile string
	UserFile    string
	Environment map[string]string
	Overrides   Overrides
}

type fileConfig struct {
	DefaultProfile string                    `json:"default_profile"`
	ToolProfile    string                    `json:"tool_profile"`
	Profiles       map[string]BackendProfile `json:"profiles"`
	Format         string                    `json:"format"`
	Ephemeral      *bool                     `json:"ephemeral"`
	Tasks          map[string]TaskConfig     `json:"tasks"`
	Token          TokenBudget               `json:"token"`
}

// DefaultTokenBudget returns the frozen Phase 0 defaults.
func DefaultTokenBudget() TokenBudget {
	return TokenBudget{
		MaxInputTokens:   16000,
		MaxOutputTokens:  4000,
		MaxSessionTokens: 64000,
	}
}

// Load merges built-ins, project JSON, user JSON, environment, and overrides.
func Load(options LoadOptions) (Config, error) {
	absoluteWorkspace, err := absoluteWorkspace(options.Workspace)
	if err != nil {
		return Config{}, err
	}

	result := defaultConfig(absoluteWorkspace)
	if err := applyConfigFiles(&result, options, absoluteWorkspace); err != nil {
		return Config{}, err
	}

	environment := options.Environment
	if environment == nil {
		environment = environmentMap()
	}
	if err := applyExplicitConfig(&result, environment); err != nil {
		return Config{}, err
	}
	if err := applyEnvironment(&result, environment); err != nil {
		return Config{}, err
	}
	applyOverrides(&result, options.Overrides)
	applyEnvironmentProfileValues(&result, environment, options.Overrides)
	return result, nil
}

func absoluteWorkspace(workspace string) (string, error) {
	if workspace == "" {
		currentDirectory, err := os.Getwd()
		if err != nil {
			return "", apperr.Wrap(apperr.KindConfig, "config.workspace", err)
		}
		workspace = currentDirectory
	}
	absolutePath, err := filepath.Abs(workspace)
	if err != nil {
		return "", apperr.Wrap(apperr.KindConfig, "config.workspace", err)
	}
	return absolutePath, nil
}

func defaultConfig(workspace string) Config {
	return Config{
		Workspace:   workspace,
		Profile:     "default",
		ToolProfile: "full",
		Format:      "human",
		Profiles:    make(map[string]BackendProfile),
		Tasks:       make(map[string]TaskConfig),
		Token:       DefaultTokenBudget(),
	}
}

func applyConfigFiles(result *Config, options LoadOptions, workspace string) error {
	projectFile := options.ProjectFile
	if projectFile == "" {
		projectFile = filepath.Join(workspace, ".doit", "config.json")
	}
	userFile := options.UserFile
	if userFile == "" {
		userFile = defaultUserConfigFile()
	}
	for _, filePath := range []string{projectFile, userFile} {
		loaded, err := readFileConfig(filePath)
		if err != nil {
			return err
		}
		applyFileConfig(result, loaded)
	}
	return nil
}

func applyExplicitConfig(result *Config, environment map[string]string) error {
	explicitFile := environment["DOIT_CONFIG_FILE"]
	if explicitFile == "" {
		return nil
	}
	loaded, err := readFileConfig(explicitFile)
	if err != nil {
		return err
	}
	applyFileConfig(result, loaded)
	return nil
}

// SelectedProfile returns and validates the configured backend profile.
func (configuration Config) SelectedProfile() (BackendProfile, error) {
	profile, exists := configuration.Profiles[configuration.Profile]
	if !exists {
		return BackendProfile{}, apperr.New(apperr.KindConfig, "config.profile", "profile is not configured: "+configuration.Profile)
	}
	if err := profile.Validate(); err != nil {
		return BackendProfile{}, err
	}
	return profile, nil
}

// Validate checks the fields required by an OpenAI-compatible profile.
func (profile BackendProfile) Validate() error {
	if profile.APIRoot == "" {
		return apperr.New(apperr.KindConfig, "config.profile", "api_root is required")
	}
	parsedURL, err := url.Parse(profile.APIRoot)
	if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return apperr.New(apperr.KindConfig, "config.profile", "api_root must be an HTTP or HTTPS URL")
	}
	if profile.Model == "" {
		return apperr.New(apperr.KindConfig, "config.profile", "model is required")
	}
	return nil
}

func readFileConfig(filePath string) (fileConfig, error) {
	if filePath == "" {
		return fileConfig{}, nil
	}
	absolutePath, err := filepath.Abs(filePath)
	if err != nil {
		return fileConfig{}, apperr.Wrap(apperr.KindConfig, "config.path", err)
	}
	root := filepath.Dir(absolutePath)
	name := filepath.Base(absolutePath)
	contents, err := fs.ReadFile(os.DirFS(root), name)
	if errors.Is(err, os.ErrNotExist) {
		return fileConfig{}, nil
	}
	if err != nil {
		return fileConfig{}, apperr.Wrap(apperr.KindConfig, "config.read", err)
	}
	var loaded fileConfig
	if err := json.Unmarshal(contents, &loaded); err != nil {
		return fileConfig{}, apperr.Wrap(apperr.KindConfig, "config.parse", fmt.Errorf("%s: %w", filePath, err))
	}
	return loaded, nil
}

func applyFileConfig(result *Config, loaded fileConfig) {
	if loaded.DefaultProfile != "" {
		result.Profile = loaded.DefaultProfile
	}
	if loaded.ToolProfile != "" {
		result.ToolProfile = loaded.ToolProfile
	}
	for name, profile := range loaded.Profiles {
		profile.APIRoot = strings.TrimRight(profile.APIRoot, "/")
		result.Profiles[name] = profile
	}
	for name, task := range loaded.Tasks {
		result.Tasks[name] = task
	}
	if loaded.Format != "" {
		result.Format = loaded.Format
	}
	if loaded.Ephemeral != nil {
		result.Ephemeral = *loaded.Ephemeral
	}
	if loaded.Token.MaxInputTokens > 0 {
		result.Token.MaxInputTokens = loaded.Token.MaxInputTokens
	}
	if loaded.Token.MaxOutputTokens > 0 {
		result.Token.MaxOutputTokens = loaded.Token.MaxOutputTokens
	}
	if loaded.Token.MaxSessionTokens > 0 {
		result.Token.MaxSessionTokens = loaded.Token.MaxSessionTokens
	}
}

func applyEnvironment(result *Config, environment map[string]string) error {
	if value := environment["DOIT_PROFILE"]; value != "" {
		result.Profile = value
	}
	if value := environment["DOIT_FORMAT"]; value != "" {
		result.Format = value
	}
	if value := environment["DOIT_MODEL"]; value != "" {
		profile := result.Profiles[result.Profile]
		profile.Model = value
		result.Profiles[result.Profile] = profile
	}
	if value := environment["DOIT_API_ROOT"]; value != "" {
		profile := result.Profiles[result.Profile]
		profile.APIRoot = strings.TrimRight(value, "/")
		result.Profiles[result.Profile] = profile
	}
	if value := environment["DOIT_API_KEY_ENV"]; value != "" {
		profile := result.Profiles[result.Profile]
		profile.APIKeyEnv = value
		result.Profiles[result.Profile] = profile
	}
	if value := environment["DOIT_EPHEMERAL"]; value != "" {
		ephemeral, err := strconv.ParseBool(value)
		if err != nil {
			return apperr.Wrap(apperr.KindConfig, "config.environment", err)
		}
		result.Ephemeral = ephemeral
	}
	return nil
}

func applyOverrides(result *Config, overrides Overrides) {
	if overrides.Profile != "" {
		result.Profile = overrides.Profile
	}
	profile := result.Profiles[result.Profile]
	if overrides.Model != "" {
		profile.Model = overrides.Model
	}
	if overrides.APIRoot != "" {
		profile.APIRoot = strings.TrimRight(overrides.APIRoot, "/")
	}
	if overrides.APIKeyEnv != "" {
		profile.APIKeyEnv = overrides.APIKeyEnv
	}
	if overrides.Model != "" || overrides.APIRoot != "" || overrides.APIKeyEnv != "" {
		result.Profiles[result.Profile] = profile
	}
	if overrides.Format != "" {
		result.Format = overrides.Format
	}
	if overrides.Ephemeral != nil {
		result.Ephemeral = *overrides.Ephemeral
	}
}

func applyEnvironmentProfileValues(result *Config, environment map[string]string, overrides Overrides) {
	profile := result.Profiles[result.Profile]
	changed := false
	if overrides.Model == "" && environment["DOIT_MODEL"] != "" {
		profile.Model = environment["DOIT_MODEL"]
		changed = true
	}
	if overrides.APIRoot == "" && environment["DOIT_API_ROOT"] != "" {
		profile.APIRoot = strings.TrimRight(environment["DOIT_API_ROOT"], "/")
		changed = true
	}
	if overrides.APIKeyEnv == "" && environment["DOIT_API_KEY_ENV"] != "" {
		profile.APIKeyEnv = environment["DOIT_API_KEY_ENV"]
		changed = true
	}
	if changed {
		result.Profiles[result.Profile] = profile
	}
}

func defaultUserConfigFile() string {
	configDirectory, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(configDirectory, "doit", "config.json")
}

func environmentMap() map[string]string {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 {
			values[parts[0]] = parts[1]
		}
	}
	return values
}
