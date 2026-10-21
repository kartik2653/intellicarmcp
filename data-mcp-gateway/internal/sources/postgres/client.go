package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Intellicars/intellicarmcp/data-mcp-gateway/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	upstreamQueryTool   = "postgres_mcp_query"
	upstreamConnectTool = "postgres_mcp_connect"
	upstreamModifyTool  = "postgres_mcp_modify"
)

// ErrQueryToolMissing is returned when the upstream server has no postgres_mcp_query tool.
var ErrQueryToolMissing = errors.New("upstream PostgreSQL MCP server does not expose postgres_mcp_query")

// Client is an MCP client for Microsoft's PostgreSQL MCP server.
type Client struct {
	session      *mcp.ClientSession
	tools        map[string]*mcp.Tool
	timeout      time.Duration
	profile      string
	connectionID string
	logger       *slog.Logger
}

// Dial starts the stdio upstream server, discovers tools, and connects the env profile.
func Dial(ctx context.Context, cfg config.Postgres, logger *slog.Logger) (*Client, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	timeout := cfg.QueryTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = mergeEnv(cfg.Env)
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "data-mcp-gateway", Version: "0.1.0"}, nil)
	session, err := mcpClient.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		logger.Error("postgres_upstream_connect_failed", "error", err.Error())
		return nil, fmt.Errorf("connect upstream PostgreSQL MCP server: %w", err)
	}
	client := &Client{
		session: session,
		tools:   map[string]*mcp.Tool{},
		timeout: timeout,
		profile: cfg.ProfileName,
		logger:  logger,
	}
	if err := client.discover(ctx); err != nil {
		_ = session.Close()
		return nil, err
	}
	if err := client.connectProfile(ctx); err != nil {
		_ = session.Close()
		return nil, err
	}
	logger.Info("postgres_upstream_connected", "tools", client.ToolNames(), "profile", cfg.ProfileName, "connection_id", client.connectionID)
	return client, nil
}

func (c *Client) discover(ctx context.Context) error {
	var cursor string
	for page := 0; page < 20; page++ {
		var params *mcp.ListToolsParams
		if cursor != "" {
			params = &mcp.ListToolsParams{Cursor: cursor}
		}
		result, err := c.session.ListTools(ctx, params)
		if err != nil {
			return fmt.Errorf("list upstream PostgreSQL tools: %w", err)
		}
		for _, tool := range result.Tools {
			if tool == nil || tool.Name == "" {
				continue
			}
			c.tools[tool.Name] = tool
		}
		if result.NextCursor == "" || result.NextCursor == cursor {
			break
		}
		cursor = result.NextCursor
	}
	if _, ok := c.tools[upstreamQueryTool]; !ok {
		return ErrQueryToolMissing
	}
	return nil
}

func (c *Client) connectProfile(ctx context.Context) error {
	if _, ok := c.tools[upstreamConnectTool]; !ok {
		return nil
	}
	result, err := c.Call(ctx, upstreamConnectTool, map[string]any{"profileId": c.profile})
	if err != nil {
		return fmt.Errorf("postgres_mcp_connect failed: %w", err)
	}
	if result != nil && result.IsError {
		detail := toolResultText(result)
		if detail == "" {
			detail = "unknown error"
		}
		return fmt.Errorf("postgres_mcp_connect returned an error: %s", detail)
	}
	id, err := parseConnectionID(toolResultText(result))
	if err != nil {
		return fmt.Errorf("postgres_mcp_connect: %w", err)
	}
	c.connectionID = id
	return nil
}

// QueryArgs builds arguments for postgres_mcp_query, including the active connectionId.
func (c *Client) QueryArgs(sql string) map[string]any {
	args := map[string]any{"sql": sql, "query": sql}
	if c != nil && c.connectionID != "" {
		args["connectionId"] = c.connectionID
	}
	return args
}

func parseConnectionID(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("empty connect result")
	}
	var payload struct {
		ConnectionID string `json:"connectionId"`
	}
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return "", fmt.Errorf("connect result is not JSON: %w", err)
	}
	id := strings.TrimSpace(payload.ConnectionID)
	if id == "" {
		return "", fmt.Errorf("connect result missing connectionId")
	}
	return id, nil
}

func toolResultText(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	var parts []string
	for _, content := range result.Content {
		text, ok := content.(*mcp.TextContent)
		if !ok || text == nil || text.Text == "" {
			continue
		}
		parts = append(parts, text.Text)
	}
	return strings.Join(parts, "\n")
}

// ToolNames returns discovered upstream tool names.
func (c *Client) ToolNames() []string {
	names := make([]string, 0, len(c.tools))
	for name := range c.tools {
		names = append(names, name)
	}
	return names
}

// Tool returns a discovered upstream tool.
func (c *Client) Tool(name string) (*mcp.Tool, bool) {
	tool, ok := c.tools[name]
	return tool, ok
}

// Call invokes an upstream tool.
func (c *Client) Call(ctx context.Context, name string, arguments map[string]any) (*mcp.CallToolResult, error) {
	if c.session == nil {
		return nil, fmt.Errorf("upstream PostgreSQL MCP session is closed")
	}
	if _, ok := c.tools[name]; !ok {
		return nil, fmt.Errorf("upstream tool %q is not available", name)
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	result, err := c.session.CallTool(callCtx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			c.logger.Warn("postgres_upstream_timeout", "tool", name)
			return nil, fmt.Errorf("upstream tool %s timed out: %w", name, context.DeadlineExceeded)
		}
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			c.logger.Warn("postgres_upstream_cancelled", "tool", name)
		} else {
			c.logger.Error("postgres_upstream_tool_error", "tool", name, "error", err.Error())
		}
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("upstream tool %s returned an empty result", name)
	}
	return result, nil
}

// Close closes the MCP session and subprocess.
func (c *Client) Close() error {
	if c == nil || c.session == nil {
		return nil
	}
	err := c.session.Close()
	c.session = nil
	c.logger.Info("postgres_upstream_closed")
	return err
}

func mergeEnv(overrides map[string]string) []string {
	base := os.Environ()
	if len(overrides) == 0 {
		return base
	}
	index := make(map[string]int, len(base))
	for i, item := range base {
		for j := 0; j < len(item); j++ {
			if item[j] == '=' {
				index[item[:j]] = i
				break
			}
		}
	}
	for key, value := range overrides {
		item := key + "=" + value
		if pos, ok := index[key]; ok {
			base[pos] = item
			continue
		}
		index[key] = len(base)
		base = append(base, item)
	}
	return base
}
