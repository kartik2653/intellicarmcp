//go:build integration

package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Intellicars/intellicarmcp/data-mcp-gateway/internal/config"
)

// TestLivePostgresUpstream dials Microsoft postgres-mcp when credentials and proxy are available.
func TestLivePostgresUpstream(t *testing.T) {
	if os.Getenv("POSTGRES_USER") == "" || os.Getenv("POSTGRES_PASSWORD") == "" {
		t.Skip("set POSTGRES_USER and POSTGRES_PASSWORD with Auth Proxy on POSTGRES_HOST")
	}
	cfg, err := config.Load()
	if err != nil || !cfg.Postgres.Enabled {
		t.Skip("postgres MCP is not enabled in config")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client, err := Dial(ctx, cfg.Postgres, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if _, ok := client.Tool(upstreamQueryTool); !ok {
		t.Fatal("missing postgres_mcp_query")
	}
	if _, ok := client.Tool(upstreamModifyTool); ok {
		// Present upstream is fine; the gateway must not expose it publicly.
	}
}
