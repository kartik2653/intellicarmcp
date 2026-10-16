//go:build integration

package agent

import (
	"context"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/Intellicars/intellicarmcp/analytics-mcp-client/internal/config"
	"github.com/Intellicars/intellicarmcp/analytics-mcp-client/internal/llm"
	"github.com/Intellicars/intellicarmcp/analytics-mcp-client/internal/mcpclient"
)

// TestLive asks Claude one question through a running MCP gateway.
// It is skipped unless ANTHROPIC_API_KEY is configured and the gateway accepts TCP connections.
func TestLive(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Skip(err)
	}
	parsed, err := url.Parse(cfg.MCPServerURL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", parsed.Host, time.Second)
	if err != nil {
		t.Skip("MCP gateway is not reachable")
	}
	_ = conn.Close()

	gateway, err := mcpclient.Dial(context.Background(), cfg.MCPServerURL, cfg.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gateway.Close() })
	if len(gateway.Names()) == 0 {
		t.Fatal("gateway exposed no tools")
	}
	loop := &Loop{
		LLM: &llm.Client{
			BaseURL:    cfg.BaseURL,
			APIKey:     cfg.APIKey,
			Model:      cfg.Model,
			Effort:     cfg.Effort,
			MaxTokens:  cfg.MaxTokens,
			HTTPClient: llm.TimeoutClient(cfg.RequestTimeout),
		},
		Tools:        gateway,
		MaxToolCalls: cfg.MaxToolCalls,
		Timeout:      cfg.RequestTimeout,
	}
	answer, err := loop.Ask(context.Background(), "Reply with the single word ok and do not call any tools.")
	if err != nil {
		t.Fatal(err)
	}
	if answer == "" {
		t.Fatal("empty live answer")
	}
}
