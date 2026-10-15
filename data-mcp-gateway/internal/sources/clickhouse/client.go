package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/Intellicars/intellicarmcp/data-mcp-gateway/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const queryToolName = "run_query"

// ErrQueryToolMissing is returned when the upstream server has no run_query tool.
var ErrQueryToolMissing = errors.New("upstream ClickHouse MCP server does not expose run_query")

// Client is an MCP client for the official ClickHouse MCP server.
type Client struct {
	session *mcp.ClientSession
	tools   map[string]*mcp.Tool
	timeout time.Duration
	logger  *slog.Logger
}

// Dial connects to the upstream MCP server and discovers its tools.
func Dial(ctx context.Context, cfg config.ClickHouse, timeout time.Duration, logger *slog.Logger) (*Client, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	transport, err := transportFor(cfg, timeout)
	if err != nil {
		return nil, err
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "data-mcp-gateway", Version: "0.1.0"}, nil)
	session, err := mcpClient.Connect(ctx, transport, nil)
	if err != nil {
		logger.Error("upstream_connect_failed", "transport", cfg.Transport, "error", err.Error())
		return nil, fmt.Errorf("connect upstream MCP server: %w", err)
	}
	client := &Client{session: session, tools: map[string]*mcp.Tool{}, timeout: timeout, logger: logger}
	if err := client.discover(ctx); err != nil {
		_ = session.Close()
		return nil, err
	}
	logger.Info("upstream_connected", "transport", cfg.Transport, "tools", client.ToolNames())
	return client, nil
}

func transportFor(cfg config.ClickHouse, timeout time.Duration) (mcp.Transport, error) {
	switch cfg.Transport {
	case config.TransportHTTP:
		httpClient := &http.Client{Timeout: timeout + 5*time.Second}
		if cfg.AuthToken != "" {
			httpClient.Transport = &bearerRoundTripper{base: http.DefaultTransport, token: cfg.AuthToken}
		}
		return &mcp.StreamableClientTransport{Endpoint: cfg.URL, HTTPClient: httpClient}, nil
	case config.TransportStdio:
		cmd := exec.Command(cfg.Command, cfg.Args...)
		cmd.Env = mergeEnv(cfg.Env)
		return &mcp.CommandTransport{Command: cmd}, nil
	default:
		return nil, fmt.Errorf("unsupported upstream transport %q", cfg.Transport)
	}
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

type bearerRoundTripper struct {
	base  http.RoundTripper
	token string
}

func (t *bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.Header.Set("Authorization", "Bearer "+t.token)
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(cloned)
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
			return fmt.Errorf("list upstream tools: %w", err)
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
	if _, ok := c.tools[queryToolName]; !ok {
		return ErrQueryToolMissing
	}
	return nil
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

// Call invokes an upstream tool. The caller context and the client timeout both apply.
func (c *Client) Call(ctx context.Context, name string, arguments map[string]any) (*mcp.CallToolResult, error) {
	if c.session == nil {
		return nil, fmt.Errorf("upstream MCP session is closed")
	}
	if _, ok := c.tools[name]; !ok {
		return nil, fmt.Errorf("upstream tool %q is not available", name)
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	result, err := c.session.CallTool(callCtx, &mcp.CallToolParams{
		Name:      name,
		Arguments: arguments,
	})
	if err != nil {
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			c.logger.Warn("upstream_timeout", "tool", name)
			return nil, fmt.Errorf("upstream tool %s timed out: %w", name, context.DeadlineExceeded)
		}
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			c.logger.Warn("upstream_cancelled", "tool", name)
		} else {
			c.logger.Error("upstream_tool_error", "tool", name, "error", err.Error())
		}
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("upstream tool %s returned an empty result", name)
	}
	if result.IsError {
		c.logger.Warn("upstream_tool_failed", "tool", name)
	}
	return result, nil
}

// Close closes the MCP session and any subprocess owned by the command transport.
func (c *Client) Close() error {
	if c == nil || c.session == nil {
		return nil
	}
	err := c.session.Close()
	c.session = nil
	c.logger.Info("upstream_closed")
	return err
}
