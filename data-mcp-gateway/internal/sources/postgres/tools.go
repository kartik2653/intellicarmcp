package postgres

import (
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const publicQueryTool = "postgres_query"

func publicPostgresQueryTool(database string) *mcp.Tool {
	return &mcp.Tool{
		Name: publicQueryTool,
		Description: strings.TrimSpace(`Run a read-only SQL query against the approved PostgreSQL catalog in database ` + database + `. ` +
			`Use schema-qualified table names from get_postgres_schema_catalog. Do not use this for ClickHouse telemetry.`),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "A single read-only SELECT statement"},
			},
			"required": []any{"query"},
		},
	}
}

func schemaCatalogTool(database string) *mcp.Tool {
	return &mcp.Tool{
		Name:        toolSchemaCatalog,
		Description: BriefInstructions(database),
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}
