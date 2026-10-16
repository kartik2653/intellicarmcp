package gateway

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRegistryRejectsDuplicatesAndMountsTools(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	tool := &mcp.Tool{Name: "list_databases", Description: "list", InputSchema: map[string]any{"type": "object"}}
	handler := func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	}
	if err := registry.Register(Registration{Tool: tool, Handler: handler}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(Registration{Tool: tool, Handler: handler}); err == nil {
		t.Fatal("expected duplicate tool error")
	}
	if err := registry.Register(Registration{Tool: &mcp.Tool{Name: "missing"}, Handler: handler}); err == nil {
		t.Fatal("expected missing schema error")
	}
	server := newMCPServer("", nil)
	registry.Mount(server)
	if got := registry.Names(); len(got) != 1 || got[0] != "list_databases" {
		t.Fatalf("names = %#v", got)
	}
}
