package gateway

import (
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Registry stores the public tools exposed by the gateway.
type Registry struct {
	order    []string
	handlers map[string]mcp.ToolHandler
	tools    map[string]*mcp.Tool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		handlers: map[string]mcp.ToolHandler{},
		tools:    map[string]*mcp.Tool{},
	}
}

// Register adds a tool. Duplicate public names are rejected.
func (r *Registry) Register(reg Registration) error {
	if reg.Tool == nil || reg.Tool.Name == "" {
		return fmt.Errorf("tool registration is missing a name")
	}
	if reg.Handler == nil {
		return fmt.Errorf("tool %q is missing a handler", reg.Tool.Name)
	}
	if _, exists := r.handlers[reg.Tool.Name]; exists {
		return fmt.Errorf("duplicate public tool %q", reg.Tool.Name)
	}
	if reg.Tool.InputSchema == nil {
		return fmt.Errorf("tool %q is missing an input schema", reg.Tool.Name)
	}
	r.order = append(r.order, reg.Tool.Name)
	r.handlers[reg.Tool.Name] = reg.Handler
	r.tools[reg.Tool.Name] = reg.Tool
	return nil
}

// Names returns public tool names in registration order.
func (r *Registry) Names() []string {
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Mount adds every registered tool to the MCP server without changing schemas.
func (r *Registry) Mount(server *mcp.Server) {
	for _, name := range r.order {
		server.AddTool(r.tools[name], r.handlers[name])
	}
}
