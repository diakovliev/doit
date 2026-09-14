package mcpclient

import (
	"context"
	"testing"

	"github.com/diakovliev/doit/internal/config"
	"github.com/diakovliev/doit/internal/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoInput struct {
	Value string `json:"value"`
}

func TestRegisterSessionToolsMapsAndCallsMCPTool(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture-server", Version: "test"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "Echo one value."}, func(_ context.Context, _ *mcp.CallToolRequest, input echoInput) (*mcp.CallToolResult, map[string]any, error) {
		return nil, map[string]any{
			"echo":          input.Value,
			"changed_paths": []string{"generated.txt"},
			"change_set":    tools.ChangeSet{ID: "mcp-change-1", Operation: "mcp.echo", State: "applied"},
		}, nil
	})
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, session := connectFixture(ctx, t, server, serverTransport, clientTransport)

	registry := tools.NewRegistry()
	registerFixtureTools(ctx, t, registry, session)
	tool := lookupFixtureTool(t, registry)
	result := tool.Execute(ctx, tools.Call{Arguments: []byte(`{"value":"hello"}`)})
	assertEchoResult(t, result)
}

func connectFixture(ctx context.Context, t *testing.T, server *mcp.Server, serverTransport, clientTransport mcp.Transport) (*mcp.ServerSession, *mcp.ClientSession) {
	t.Helper()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect fixture server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "doit-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect fixture client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return serverSession, session
}

func registerFixtureTools(ctx context.Context, t *testing.T, registry *tools.Registry, session *mcp.ClientSession) {
	t.Helper()
	if err := registerSessionTools(ctx, registry, "fixture", config.MCPServerConfig{Transport: "stdio"}, session); err != nil {
		t.Fatalf("register MCP tools: %v", err)
	}
}

func lookupFixtureTool(t *testing.T, registry *tools.Registry) tools.Tool {
	t.Helper()
	tool, exists := registry.Lookup("mcp.fixture.echo")
	if !exists {
		t.Fatal("MCP tool was not registered")
	}
	return tool
}

func assertEchoResult(t *testing.T, result tools.Result) {
	t.Helper()
	if result.Status != tools.StatusSucceeded {
		t.Fatalf("MCP tool failed: %+v", result)
	}
	data := echoResultData(t, result)
	if data["server"] != "fixture" || data["tool"] != "echo" {
		t.Fatalf("unexpected normalized MCP result: %+v", result.Data)
	}
	structured, ok := data["structured_content"].(map[string]any)
	if !ok || structured["echo"] != "hello" {
		t.Fatalf("structured MCP result missing: %+v", data)
	}
	if len(result.ChangedPaths) != 1 || result.ChangedPaths[0] != "generated.txt" || result.ChangeSet == nil || result.ChangeSet.ID != "mcp-change-1" {
		t.Fatalf("structured MCP change metadata missing: %+v", result)
	}
}

func echoResultData(t *testing.T, result tools.Result) map[string]any {
	t.Helper()
	data, ok := result.Data.(map[string]any)
	if !ok {
		t.Fatalf("unexpected normalized MCP result: %+v", result.Data)
	}
	return data
}

func TestMCPReadOnlyHintDoesNotGrantReadOnlyRisk(t *testing.T) {
	definition := &mcp.Tool{Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}
	if risk := toolRisk(config.MCPServerConfig{Transport: "stdio"}, definition); risk != tools.RiskProcess {
		t.Fatalf("expected advisory read-only hint to remain process risk, got %q", risk)
	}
}

func TestMCPCallCancellationIsNormalized(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture-server", Version: "test"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ echoInput) (*mcp.CallToolResult, map[string]string, error) {
		<-ctx.Done()
		return nil, nil, ctx.Err()
	})
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, session := connectFixture(ctx, t, server, serverTransport, clientTransport)
	registry := tools.NewRegistry()
	registerFixtureTools(ctx, t, registry, session)
	tool, exists := registry.Lookup("mcp.fixture.echo")
	if !exists {
		t.Fatal("MCP cancellation tool was not registered")
	}
	callContext, callCancel := context.WithCancel(context.Background())
	callCancel()
	result := tool.Execute(callContext, tools.Call{Arguments: []byte(`{"value":"cancel"}`)})
	if result.Status != tools.StatusCancelled {
		t.Fatalf("expected cancelled MCP result, got %+v", result)
	}
}
