package clickhouse

import (
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func publicTool(upstream *mcp.Tool) *mcp.Tool {
	schema := upstream.InputSchema
	if schema == nil {
		schema = map[string]any{"type": "object"}
	}
	return &mcp.Tool{
		Name:         upstream.Name,
		Title:        upstream.Title,
		Description:  upstream.Description,
		Annotations:  upstream.Annotations,
		InputSchema:  schema,
		OutputSchema: upstream.OutputSchema,
	}
}

func publicQueryTool(upstream *mcp.Tool) *mcp.Tool {
	tool := publicTool(upstream)
	note := " Execute SQL only against tables in the static schema catalog. Call get_schema_catalog for column details. Do not request schema discovery."
	tool.Description = strings.TrimSpace(tool.Description + note)
	return tool
}

func schemaCatalogTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        toolSchemaCatalog,
		Description: BriefInstructions(),
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}
