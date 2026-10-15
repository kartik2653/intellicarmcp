//go:build integration

package clickhouse

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Intellicars/intellicarmcp/data-mcp-gateway/internal/config"
)

// TestLiveUpstream calls a real official ClickHouse MCP server.
// It is skipped unless .env or the process environment configures an upstream.
func TestLiveUpstream(t *testing.T) {
	if os.Getenv("CLICKHOUSE_MCP_URL") == "" && os.Getenv("CLICKHOUSE_MCP_COMMAND") == "" && os.Getenv("GATEWAY_ENV_FILE") == "" {
		if _, err := os.Stat(".env"); err != nil {
			t.Skip("add a .env file or set CLICKHOUSE_MCP_URL or CLICKHOUSE_MCP_COMMAND")
		}
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	client, err := Dial(context.Background(), cfg.ClickHouse, cfg.Policy.QueryTimeout, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, ok := client.Tool(queryToolName); !ok {
		t.Fatal("live server does not expose run_query")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := client.Call(ctx, queryToolName, map[string]any{"query": "SELECT 1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("run_query failed: %s", textOf(result))
	}
	if textOf(result) == "" && result.StructuredContent == nil {
		t.Fatal("run_query returned no content")
	}
	svc := New(cfg.ClickHouse, cfg.Policy, false, nil)
	if err := svc.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	regs, err := svc.Registrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, reg := range regs {
		names[reg.Tool.Name] = true
	}
	if !names[queryToolName] || !names[toolSchemaCatalog] || names["list_databases"] || names["list_tables"] {
		t.Fatalf("public tools = %#v", names)
	}
}
