// Package mcpclient maps configured MCP tool servers into the normalized tool registry.
package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/diakovliev/doit/internal/apperr"
	"github.com/diakovliev/doit/internal/config"
	"github.com/diakovliev/doit/internal/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const defaultToolTimeout = 30 * time.Second

// Client owns the connected MCP sessions for one runtime.
type Client struct {
	sessions []*mcp.ClientSession
}

// Close closes every connected MCP session.
func (client *Client) Close() error {
	var closeErr error
	for _, session := range client.sessions {
		closeErr = errors.Join(closeErr, session.Close())
	}
	return closeErr
}

// RegisterTools connects to configured MCP servers and exposes their tools.
func RegisterTools(ctx context.Context, registry *tools.Registry, workspace string, servers map[string]config.MCPServerConfig) (*Client, error) {
	client := &Client{}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		configuration := servers[name]
		serverContext, cancel := serverContext(ctx, configuration)
		transport, err := buildTransport(workspace, configuration)
		if err != nil {
			cancel()
			_ = client.Close()
			return nil, apperr.Wrap(apperr.KindConfig, "mcp.transport", err)
		}
		mcpClient := mcp.NewClient(&mcp.Implementation{Name: "doit", Version: "dev"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}})
		session, connectErr := mcpClient.Connect(serverContext, transport, nil)
		cancel()
		if connectErr != nil {
			_ = client.Close()
			return nil, apperr.Wrap(apperr.KindTool, "mcp.connect", connectErr)
		}
		client.sessions = append(client.sessions, session)
		if err := registerSessionTools(ctx, registry, name, configuration, session); err != nil {
			_ = client.Close()
			return nil, err
		}
	}
	return client, nil
}

func registerSessionTools(ctx context.Context, registry *tools.Registry, serverName string, configuration config.MCPServerConfig, session *mcp.ClientSession) error {
	result, err := session.ListTools(ctx, nil)
	if err != nil {
		return apperr.Wrap(apperr.KindTool, "mcp.tools.list", err)
	}
	for _, definition := range result.Tools {
		if definition == nil || definition.Name == "" {
			continue
		}
		parameters, err := schemaBytes(definition.InputSchema)
		if err != nil {
			return apperr.Wrap(apperr.KindTool, "mcp.tools.schema", err)
		}
		localName := "mcp." + serverName + "." + definition.Name
		adapter := &toolAdapter{server: serverName, remoteName: definition.Name, session: session}
		adapter.definition = tools.Definition{
			Name:           localName,
			Description:    definition.Description,
			Parameters:     parameters,
			Risk:           toolRisk(configuration, definition),
			UsesNetwork:    usesNetwork(configuration),
			Timeout:        defaultToolTimeout,
			MaxOutputBytes: 64 * 1024,
			MaxArguments:   32,
		}
		if configuration.TimeoutMs > 0 {
			adapter.definition.Timeout = time.Duration(configuration.TimeoutMs) * time.Millisecond
		}
		if err := registry.Register(adapter); err != nil {
			return err
		}
	}
	return nil
}

type toolAdapter struct {
	server     string
	remoteName string
	session    *mcp.ClientSession
	definition tools.Definition
}

func (adapter *toolAdapter) Definition() tools.Definition {
	return adapter.definition
}

func (adapter *toolAdapter) Execute(ctx context.Context, call tools.Call) tools.Result {
	arguments, err := toolArguments(call.Arguments)
	if err != nil {
		return tools.Result{Status: tools.StatusFailed, Diagnostics: []tools.Diagnostic{{Level: "error", Message: err.Error()}}}
	}
	result, err := adapter.session.CallTool(ctx, &mcp.CallToolParams{Name: adapter.remoteName, Arguments: arguments})
	if err != nil {
		return mcpCallError(ctx, err)
	}
	return normalizeToolResult(adapter, result)
}

func mcpCallError(ctx context.Context, err error) tools.Result {
	status := tools.StatusFailed
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		status = tools.StatusCancelled
	}
	return tools.Result{Status: status, Diagnostics: []tools.Diagnostic{{Level: "error", Message: err.Error()}}}
}

func toolArguments(raw json.RawMessage) (map[string]any, error) {
	arguments := make(map[string]any)
	if len(raw) == 0 {
		return arguments, nil
	}
	if err := json.Unmarshal(raw, &arguments); err != nil {
		return nil, err
	}
	return arguments, nil
}

func normalizeToolResult(adapter *toolAdapter, result *mcp.CallToolResult) tools.Result {
	data := map[string]any{
		"server":  adapter.server,
		"tool":    adapter.remoteName,
		"content": contentText(result.Content),
		"error":   result.IsError,
	}
	if result.StructuredContent != nil {
		data["structured_content"] = result.StructuredContent
	}
	if result.IsError {
		return tools.Result{Status: tools.StatusFailed, Data: data, Diagnostics: []tools.Diagnostic{{Level: "error", Message: contentText(result.Content)}}}
	}
	normalized := tools.Result{Status: tools.StatusSucceeded, Data: data}
	applyStructuredResult(&normalized, result.StructuredContent)
	return normalized
}

func applyStructuredResult(result *tools.Result, content any) {
	structured, ok := content.(map[string]any)
	if !ok {
		return
	}
	result.ChangedPaths = structuredChangedPaths(structured["changed_paths"])
	result.ChangeSet = structuredChangeSet(structured["change_set"])
}

func structuredChangedPaths(value any) []string {
	paths, ok := value.([]any)
	if !ok {
		return nil
	}
	changedPaths := make([]string, 0, len(paths))
	for _, path := range paths {
		if value, ok := path.(string); ok {
			changedPaths = append(changedPaths, value)
		}
	}
	return changedPaths
}

func structuredChangeSet(value any) *tools.ChangeSet {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var changeSet tools.ChangeSet
	if json.Unmarshal(encoded, &changeSet) != nil || changeSet.ID == "" {
		return nil
	}
	return &changeSet
}

func contentText(contents []mcp.Content) string {
	parts := make([]string, 0, len(contents))
	for _, content := range contents {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
			continue
		}
		encoded, err := json.Marshal(content)
		if err == nil {
			parts = append(parts, string(encoded))
		}
	}
	return strings.Join(parts, "\n")
}

func schemaBytes(schema any) (json.RawMessage, error) {
	if schema == nil {
		return json.RawMessage(`{"type":"object","properties":{}}`), nil
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	if !json.Valid(encoded) {
		return nil, errors.New("MCP tool schema is invalid JSON")
	}
	return json.RawMessage(encoded), nil
}

func toolRisk(configuration config.MCPServerConfig, definition *mcp.Tool) tools.Risk {
	transport := configuration.Transport
	if transport == "" {
		transport = "stdio"
	}
	if transport != "stdio" && !configuration.AllowNetwork {
		return tools.RiskRejected
	}
	if definition.Annotations != nil {
		if definition.Annotations.DestructiveHint != nil && *definition.Annotations.DestructiveHint {
			return tools.RiskDestructive
		}
		if definition.Annotations.ReadOnlyHint {
			return tools.RiskProcess
		}
	}
	return tools.RiskWrite
}

func usesNetwork(configuration config.MCPServerConfig) bool {
	transport := configuration.Transport
	return transport == "streamable-http"
}

func buildTransport(workspace string, configuration config.MCPServerConfig) (mcp.Transport, error) {
	transport := configuration.Transport
	if transport == "" {
		transport = "stdio"
	}
	switch transport {
	case "stdio":
		if configuration.Command == "" {
			return nil, errors.New("stdio MCP server requires command")
		}
		// #nosec G204 -- the executable and arguments are an explicit, user-configured MCP capability, not model input.
		command := exec.Command(configuration.Command, configuration.Arguments...)
		if configuration.WorkingDirectory != "" {
			workingDirectory, err := scopedDirectory(workspace, configuration.WorkingDirectory)
			if err != nil {
				return nil, err
			}
			command.Dir = workingDirectory
		}
		if configuration.Environment != nil {
			command.Env = append([]string(nil), configuration.Environment...)
		}
		return &mcp.CommandTransport{Command: command}, nil
	case "streamable-http":
		if !configuration.AllowNetwork {
			return nil, errors.New("streamable-http MCP server requires allow_network=true")
		}
		if configuration.URL == "" {
			return nil, errors.New("streamable-http MCP server requires url")
		}
		return &mcp.StreamableClientTransport{Endpoint: configuration.URL, HTTPClient: &http.Client{Transport: headerRoundTripper{headers: configuration.Headers}}}, nil
	default:
		return nil, fmt.Errorf("unsupported MCP transport %q", transport)
	}
}

func scopedDirectory(workspace, requested string) (string, error) {
	candidate, err := filepath.Abs(filepath.Join(workspace, requested))
	if err != nil || !withinRoot(workspace, candidate) {
		return "", errors.New("MCP working directory escapes the workspace")
	}
	info, err := os.Stat(candidate)
	if err != nil || !info.IsDir() {
		return "", errors.New("MCP working directory is not an existing directory")
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil || !withinRoot(root, resolved) {
		return "", errors.New("MCP working directory escapes the workspace through a symlink")
	}
	return candidate, nil
}

func withinRoot(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func serverContext(ctx context.Context, configuration config.MCPServerConfig) (context.Context, context.CancelFunc) {
	if configuration.TimeoutMs <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, time.Duration(configuration.TimeoutMs)*time.Millisecond)
}

type headerRoundTripper struct {
	headers map[string]string
}

func (roundTripper headerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	for name, value := range roundTripper.headers {
		request.Header.Set(name, value)
	}
	return http.DefaultTransport.RoundTrip(request)
}

var _ tools.Tool = (*toolAdapter)(nil)
