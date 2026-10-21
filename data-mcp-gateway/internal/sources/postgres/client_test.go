package postgres

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Intellicars/intellicarmcp/data-mcp-gateway/internal/authz"
	"github.com/Intellicars/intellicarmcp/data-mcp-gateway/internal/config"
	"github.com/Intellicars/intellicarmcp/data-mcp-gateway/internal/gateway"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPublicToolsHideModifyAndScopeQuery(t *testing.T) {
	t.Parallel()
	var connected bool
	var upstreamSQL string
	var upstreamConnectionID string
	const wantConnectionID = "pgsql/default/region_india"
	upstream := fakeUpstream(t, func(server *mcp.Server) {
		server.AddTool(&mcp.Tool{Name: upstreamConnectTool, InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			connected = true
			return textResult(`{"connectionId":"` + wantConnectionID + `","success":true}`), nil
		})
		server.AddTool(&mcp.Tool{Name: upstreamQueryTool, InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var args map[string]any
			_ = json.Unmarshal(req.Params.Arguments, &args)
			upstreamSQL, _ = args["sql"].(string)
			if upstreamSQL == "" {
				upstreamSQL, _ = args["query"].(string)
			}
			upstreamConnectionID, _ = args["connectionId"].(string)
			return textResult(`[{"fleetid":"f1"}]`), nil
		})
		server.AddTool(&mcp.Tool{Name: upstreamModifyTool, InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return textResult("should not be public"), nil
		})
	})
	svc := New(config.Postgres{
		Enabled: true, Database: "region_india", ProfileName: "default",
		QueryTimeout: time.Second, MaxResultRows: 20, MaxResponseBytes: 1 << 20,
		Command: "true", // unused; we inject client via Connect override pattern
	}, false, nil)
	// Dial real HTTP transport by swapping: use test helper dialHTTP
	client := dialHTTP(t, upstream.URL+"/mcp", "default")
	svc.client = client
	svc.SetPermissions(staticFetcher{scope: authz.Scope{
		FleetsByPerm: map[string][]string{"fleet.fleet_management.view": {"f1"}},
	}})
	regs, err := svc.Registrations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, reg := range regs {
		names[reg.Tool.Name] = true
	}
	if !names[publicQueryTool] || !names[toolSchemaCatalog] || names[upstreamModifyTool] || names[upstreamQueryTool] {
		t.Fatalf("public tools = %#v", names)
	}
	if !connected || client.connectionID != wantConnectionID {
		t.Fatalf("connected=%v connectionID=%q", connected, client.connectionID)
	}
	result, err := handlerNamed(t, regs, publicQueryTool)(context.Background(), callRaw(`{"query":"SELECT fleetid FROM fleetmodule.fleets"}`))
	if err != nil || result.IsError {
		t.Fatalf("query = %s err=%v", textOf(result), err)
	}
	if upstreamConnectionID != wantConnectionID {
		t.Fatalf("upstream connectionId = %q", upstreamConnectionID)
	}
	if !strings.Contains(upstreamSQL, `"fleetid" IN`) || !strings.Contains(upstreamSQL, "'f1'") {
		t.Fatalf("scoped sql = %s", upstreamSQL)
	}
	denied, err := handlerNamed(t, regs, publicQueryTool)(context.Background(), callRaw(`{"query":"SELECT userid FROM fmsaccount.usersecret"}`))
	if err != nil || !denied.IsError {
		t.Fatalf("unmapped = %s err=%v", textOf(denied), err)
	}
	write, err := handlerNamed(t, regs, publicQueryTool)(context.Background(), callRaw(`{"query":"DELETE FROM fleetmodule.fleets"}`))
	if err != nil || !write.IsError {
		t.Fatalf("write = %s err=%v", textOf(write), err)
	}
}

func TestCatalogExcludesSecrets(t *testing.T) {
	t.Parallel()
	raw := CatalogJSON("region_india", map[string]TableMeta{
		"fleetmodule.fleets": {
			Name:        "fleetmodule.fleets",
			Description: "Fleet master records",
			Columns:     []ColumnMeta{{Name: "fleetid", DataType: "text", Nullable: false, Ordinal: 1}},
		},
	}, time.Unix(1, 0).UTC())
	if strings.Contains(raw, "usersecret") || strings.Contains(raw, "vapidkeys") || !strings.Contains(raw, "fleetmodule.fleets") {
		t.Fatalf("catalog = %s", raw)
	}
	if !strings.Contains(raw, "Fleet master records") || !strings.Contains(raw, `"metadata_hydrated":true`) {
		t.Fatalf("expected hydrated description: %s", raw)
	}
	if strings.Contains(raw, `"name":"geofencemodule.geofencerule"`) {
		t.Fatal("geofencerule without fleet/vehicle scope must not be in catalog")
	}
	if !strings.Contains(BriefInstructions("region_india"), "ClickHouse") {
		t.Fatal("instructions should distinguish ClickHouse")
	}
}

func fakeUpstream(t *testing.T, register func(*mcp.Server)) *httptest.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "pg-upstream", Version: "0.0.1"}, nil)
	register(server)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	return httpServer
}

func dialHTTP(t *testing.T, endpoint, profile string) *Client {
	t.Helper()
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "gateway-test", Version: "0.0.1"}, nil)
	session, err := mcpClient.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{session: session, tools: map[string]*mcp.Tool{}, timeout: time.Second, profile: profile, logger: nil}
	if client.logger == nil {
		client.logger = discardLogger()
	}
	if err := client.discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.connectProfile(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestParseConnectionID(t *testing.T) {
	t.Parallel()
	id, err := parseConnectionID(`{"connectionId":"pgsql/default/region_india","success":true}`)
	if err != nil || id != "pgsql/default/region_india" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if _, err := parseConnectionID(`{"success":true}`); err == nil {
		t.Fatal("expected missing connectionId error")
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func textOf(result *mcp.CallToolResult) string {
	if result == nil || len(result.Content) == 0 {
		return ""
	}
	text, _ := result.Content[0].(*mcp.TextContent)
	if text == nil {
		return ""
	}
	return text.Text
}

func callRaw(payload string) *mcp.CallToolRequest {
	return &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Arguments: json.RawMessage(payload)},
		Extra:  &mcp.RequestExtra{Header: http.Header{"Authorization": []string{"Bearer user-token"}}},
	}
}

type staticFetcher struct{ scope authz.Scope }

func (s staticFetcher) Fetch(context.Context, string) (authz.Scope, error) { return s.scope, nil }

func handlerNamed(t *testing.T, regs []gateway.Registration, name string) mcp.ToolHandler {
	t.Helper()
	for _, reg := range regs {
		if reg.Tool.Name == name {
			return reg.Handler
		}
	}
	t.Fatalf("missing handler %s", name)
	return nil
}
