package mcpclient

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestConvertPreservesObjectSchema(t *testing.T) {
	t.Parallel()
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string"},
		},
		"required": []any{"query"},
	}
	tool, raw, err := Convert(&mcp.Tool{Name: "run_query", Description: "Run SQL", InputSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	if tool.Name != "run_query" || tool.Description != "Run SQL" {
		t.Fatalf("tool = %+v", tool)
	}
	if err := Validate(raw, json.RawMessage(`{"query":"SELECT 1"}`)); err != nil {
		t.Fatal(err)
	}
	if err := Validate(raw, json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected missing required field")
	}
	if err := Validate(raw, json.RawMessage(`["nope"]`)); err == nil {
		t.Fatal("expected object validation error")
	}
	if err := Validate(raw, json.RawMessage(`{`)); err == nil {
		t.Fatal("expected invalid JSON error")
	}
}

func TestConvertWrapsNonObjectSchema(t *testing.T) {
	t.Parallel()
	tool, _, err := Convert(&mcp.Tool{Name: "list_databases", InputSchema: map[string]any{"type": "string"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tool.InputSchema), `"type":"object"`) && !strings.Contains(string(tool.InputSchema), `"type": "object"`) {
		t.Fatalf("parameters = %s", tool.InputSchema)
	}
}
