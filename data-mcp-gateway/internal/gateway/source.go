package gateway

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Registration is one public tool contributed by a source module.
type Registration struct {
	Tool    *mcp.Tool
	Handler mcp.ToolHandler
}

// Instructor optionally supplies concise MCP server instructions.
type Instructor interface {
	Instructions() string
}

// Source is the lifecycle and tool-registration contract for a data source.
// Sources do not share query semantics.
type Source interface {
	Name() string
	Connect(ctx context.Context) error
	Registrations(ctx context.Context) ([]Registration, error)
	Close() error
}
