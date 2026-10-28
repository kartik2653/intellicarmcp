package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Intellicars/intellicarmcp/analytics-mcp-client/internal/llm"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Client is an MCP session against the analytics gateway.
type Client struct {
	endpoint       string
	session        *mcp.ClientSession
	order          []string
	tools          map[string]storedTool
	maxResultBytes int
}

// ToolInfo is one discovered tool, safe to print. It never includes credentials.
type ToolInfo struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// Handshake is the initialize result and the tool list returned to this client.
type Handshake struct {
	Endpoint      string
	Protocol      string
	ServerName    string
	ServerVersion string
	Instructions  string
	Tools         []ToolInfo
}

type storedTool struct {
	definition llm.Tool
	schema     json.RawMessage
}

// Dial connects to a Streamable HTTP MCP server and discovers its tools.
// token is sent as Authorization: Bearer on every gateway request.
// maxResultBytes caps tool result text passed to the model; it must be positive.
func Dial(ctx context.Context, endpoint, token string, maxResultBytes int) (*Client, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("ANALYTICS_ACCESS_TOKEN is required")
	}
	if maxResultBytes <= 0 {
		return nil, fmt.Errorf("MCP_MAX_RESULT_BYTES must be positive")
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "analytics-mcp-client", Version: "0.1.0"}, nil)
	session, err := mcpClient.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: &http.Client{Transport: bearerRoundTripper{token: token, base: http.DefaultTransport}},
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect MCP gateway: %w", err)
	}
	client := &Client{endpoint: endpoint, session: session, tools: map[string]storedTool{}, maxResultBytes: maxResultBytes}
	if err := client.Reload(ctx); err != nil {
		_ = session.Close()
		return nil, err
	}
	return client, nil
}

// Reload replaces the discovered tool list.
func (c *Client) Reload(ctx context.Context) error {
	if c.session == nil {
		return fmt.Errorf("MCP session is closed")
	}
	next := map[string]storedTool{}
	var order []string
	var cursor string
	for page := 0; page < 20; page++ {
		var params *mcp.ListToolsParams
		if cursor != "" {
			params = &mcp.ListToolsParams{Cursor: cursor}
		}
		result, err := c.session.ListTools(ctx, params)
		if err != nil {
			return fmt.Errorf("list MCP tools: %w", err)
		}
		for _, tool := range result.Tools {
			definition, schema, err := Convert(tool)
			if err != nil {
				return err
			}
			if _, exists := next[tool.Name]; exists {
				continue
			}
			next[tool.Name] = storedTool{definition: definition, schema: schema}
			order = append(order, tool.Name)
		}
		if result.NextCursor == "" || result.NextCursor == cursor {
			break
		}
		cursor = result.NextCursor
	}
	c.tools = next
	c.order = order
	return nil
}

// Definitions returns Anthropic tool definitions in discovery order.
func (c *Client) Definitions() []llm.Tool {
	out := make([]llm.Tool, 0, len(c.order))
	for _, name := range c.order {
		out = append(out, c.tools[name].definition)
	}
	return out
}

// Instructions returns the gateway's concise schema guidance.
func (c *Client) Instructions() string {
	if c == nil || c.session == nil || c.session.InitializeResult() == nil {
		return ""
	}
	return c.session.InitializeResult().Instructions
}

// Snapshot returns the handshake and tool descriptions for terminal tracing.
func (c *Client) Snapshot() Handshake {
	if c == nil {
		return Handshake{}
	}
	out := Handshake{Endpoint: c.endpoint, Instructions: c.Instructions()}
	if c.session != nil && c.session.InitializeResult() != nil {
		init := c.session.InitializeResult()
		out.Protocol = init.ProtocolVersion
		out.Instructions = init.Instructions
		if init.ServerInfo != nil {
			out.ServerName = init.ServerInfo.Name
			out.ServerVersion = init.ServerInfo.Version
		}
	}
	for _, name := range c.order {
		tool := c.tools[name]
		out.Tools = append(out.Tools, ToolInfo{
			Name:        name,
			Description: tool.definition.Description,
			InputSchema: append(json.RawMessage(nil), tool.schema...),
		})
	}
	return out
}

// Names returns discovered tool names.
func (c *Client) Names() []string {
	out := make([]string, len(c.order))
	copy(out, c.order)
	return out
}

// Execute validates a tool call and invokes it through the MCP session.
func (c *Client) Execute(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
	tool, ok := c.tools[name]
	if !ok {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	if err := Validate(tool.schema, arguments); err != nil {
		return "", err
	}
	if c.session == nil {
		return "", fmt.Errorf("MCP session is closed")
	}
	var args map[string]any
	if len(bytesTrim(arguments)) == 0 {
		args = map[string]any{}
	} else if err := json.Unmarshal(arguments, &args); err != nil {
		return "", fmt.Errorf("tool arguments must be valid JSON")
	}
	result, err := c.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return "", fmt.Errorf("MCP tool %s failed: %w", name, err)
	}
	if result == nil {
		return "", fmt.Errorf("MCP tool %s returned an empty result", name)
	}
	text := c.renderResult(result)
	if result.IsError {
		return text, fmt.Errorf("MCP tool %s returned an error", name)
	}
	return text, nil
}

// Close closes the MCP session.
func (c *Client) Close() error {
	if c == nil || c.session == nil {
		return nil
	}
	err := c.session.Close()
	c.session = nil
	return err
}

func (c *Client) renderResult(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		text, ok := content.(*mcp.TextContent)
		if !ok || text.Text == "" {
			continue
		}
		parts = append(parts, text.Text)
	}
	if result.StructuredContent != nil {
		encoded, err := json.Marshal(result.StructuredContent)
		if err == nil {
			parts = append(parts, string(encoded))
		}
	}
	joined := strings.Join(parts, "\n")
	if joined == "" {
		joined = "MCP tool returned no text content"
	}
	limit := c.maxResultBytes
	if len(joined) > limit {
		return joined[:limit] + fmt.Sprintf("\n[truncated: MCP response exceeded %d bytes]", limit)
	}
	return joined
}

type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (b bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+b.token)
	base := b.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}
