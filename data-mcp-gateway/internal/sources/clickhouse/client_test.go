package clickhouse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Intellicars/intellicarmcp/data-mcp-gateway/internal/config"
	"github.com/Intellicars/intellicarmcp/data-mcp-gateway/internal/gateway"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMain(m *testing.M) {
	if os.Getenv("GATEWAY_TEST_STDIO") == "1" {
		if err := runStdioFixture(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

func runStdioFixture() error {
	server := mcp.NewServer(&mcp.Implementation{Name: "stdio-fixture", Version: "0.0.1"}, nil)
	server.AddTool(toolSchema(queryToolName, "query"), func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return textResult(`[{"ok":true}]`), nil
	})
	return server.Run(context.Background(), &mcp.StdioTransport{})
}

func TestDialDiscoversAndCallsTool(t *testing.T) {
	t.Parallel()
	upstream := fakeUpstream(t, func(server *mcp.Server) {
		server.AddTool(toolSchema(queryToolName, "Execute SQL"), echoQuery)
		server.AddTool(toolSchema("list_databases", "List databases"), func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return textResult(`["analytics"]`), nil
		})
		server.AddTool(toolSchema("list_tables", "List tables"), func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return textResult(`{"tables":[{"name":"events"}]}`), nil
		})
	})
	client := dialHTTP(t, upstream.URL+"/mcp", time.Second)
	defer client.Close()

	for _, name := range []string{queryToolName, "list_databases", "list_tables"} {
		tool, ok := client.Tool(name)
		if !ok || tool.Description == "" || tool.InputSchema == nil {
			t.Fatalf("tool %s = %+v ok=%v", name, tool, ok)
		}
	}
	result, err := client.Call(context.Background(), queryToolName, map[string]any{"query": "SELECT 1"})
	if err != nil || result.IsError {
		t.Fatalf("call = %+v err=%v", result, err)
	}
	if textOf(result) != "SELECT 1" {
		t.Fatalf("parsed response = %s", textOf(result))
	}
}

func TestMissingQueryTool(t *testing.T) {
	t.Parallel()
	upstream := fakeUpstream(t, func(server *mcp.Server) {
		server.AddTool(toolSchema("list_databases", "List databases"), func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return textResult("[]"), nil
		})
	})
	_, err := Dial(context.Background(), config.ClickHouse{Transport: config.TransportHTTP, URL: upstream.URL + "/mcp"}, time.Second, nil)
	if !errors.Is(err, ErrQueryToolMissing) {
		t.Fatalf("err = %v", err)
	}
}

func TestInvalidArgumentsAndUpstreamFailure(t *testing.T) {
	t.Parallel()
	upstream := fakeUpstream(t, func(server *mcp.Server) {
		server.AddTool(toolSchema(queryToolName, "Execute SQL"), func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var args map[string]any
			_ = json.Unmarshal(req.Params.Arguments, &args)
			if args["query"] == "SELECT 1" {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "upstream failed"}}}, nil
			}
			return textResult("[]"), nil
		})
	})
	svc := New(config.ClickHouse{Transport: config.TransportHTTP, URL: upstream.URL + "/mcp"}, config.Policy{
		QueryTimeout: time.Second, MaxResultRows: 10, MaxResponseBytes: 1 << 20,
	}, false, nil)
	if err := svc.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	regs, err := svc.Registrations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerNamed(t, regs, queryToolName)
	result, err := handler(context.Background(), callRaw(`{"params":[]}`))
	if err != nil || !result.IsError || textOf(result) != "query is required and must be a string" {
		t.Fatalf("invalid args = %+v err=%v", result, err)
	}
	result, err = handler(context.Background(), callRaw(`{"query":"INSERT INTO analytics.t VALUES (1)"}`))
	if err != nil || !result.IsError {
		t.Fatalf("policy result = %+v err=%v", result, err)
	}
	result, err = handler(context.Background(), callRaw(`{"query":"SELECT 1"}`))
	if err != nil || !result.IsError || textOf(result) != "upstream failed" {
		t.Fatalf("upstream failure = %+v %s err=%v", result, textOf(result), err)
	}
}

func TestRowAndByteLimits(t *testing.T) {
	t.Parallel()
	upstream := fakeUpstream(t, func(server *mcp.Server) {
		server.AddTool(toolSchema(queryToolName, "Execute SQL"), func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var args map[string]any
			_ = json.Unmarshal(req.Params.Arguments, &args)
			if args["query"] == "SELECT count() FROM tripreport" {
				return textResult(`[1,2,3,4]`), nil
			}
			return textResult(`"` + "0123456789abcdef" + `"`), nil
		})
	})
	svc := New(config.ClickHouse{Transport: config.TransportHTTP, URL: upstream.URL + "/mcp"}, config.Policy{
		QueryTimeout: time.Second, MaxResultRows: 2, MaxResponseBytes: 40,
	}, false, nil)
	if err := svc.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	regs, err := svc.Registrations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerNamed(t, regs, queryToolName)
	rows, err := handler(context.Background(), callRaw(`{"query":"SELECT count() FROM tripreport"}`))
	if err != nil || !rows.IsError || textOf(rows) == "" {
		t.Fatalf("rows = %+v err=%v", rows, err)
	}
	wide, err := handler(context.Background(), callRaw(`{"query":"SELECT 1"}`))
	if err != nil || !wide.IsError {
		t.Fatalf("bytes = %+v err=%v", wide, err)
	}
}

func TestTimeoutAndCancellation(t *testing.T) {
	t.Parallel()
	upstream := fakeUpstream(t, func(server *mcp.Server) {
		server.AddTool(toolSchema(queryToolName, "Execute SQL"), func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(5 * time.Second):
				return textResult("[]"), nil
			}
		})
	})
	client := dialHTTP(t, upstream.URL+"/mcp", 50*time.Millisecond)
	defer client.Close()
	_, err := client.Call(context.Background(), queryToolName, map[string]any{"query": "SELECT 1"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout err = %v", err)
	}

	cancelClient := dialHTTP(t, upstream.URL+"/mcp", time.Second)
	defer cancelClient.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = cancelClient.Call(ctx, queryToolName, map[string]any{"query": "SELECT 1"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err = %v", err)
	}
}

func TestUpstreamConnectionFailure(t *testing.T) {
	t.Parallel()
	_, err := Dial(context.Background(), config.ClickHouse{
		Transport: config.TransportHTTP,
		URL:       "http://127.0.0.1:1/mcp",
	}, 200*time.Millisecond, nil)
	if err == nil {
		t.Fatal("expected connection failure")
	}
}

func TestStdioTransportDiscoversQueryTool(t *testing.T) {
	client, err := Dial(context.Background(), config.ClickHouse{
		Transport: config.TransportStdio,
		Command:   os.Args[0],
		Env:       map[string]string{"GATEWAY_TEST_STDIO": "1"},
	}, 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, ok := client.Tool(queryToolName); !ok {
		t.Fatal("stdio fixture did not expose run_query")
	}
	result, err := client.Call(context.Background(), queryToolName, map[string]any{"query": "SELECT 1"})
	if err != nil || textOf(result) != `[{"ok":true}]` {
		t.Fatalf("stdio call = %+v err=%v", result, err)
	}
}

func TestSchemaCatalogHidesDiscoveryTools(t *testing.T) {
	t.Parallel()
	upstream := fakeUpstream(t, func(server *mcp.Server) {
		server.AddTool(toolSchema(queryToolName, "Execute SQL"), echoQuery)
		server.AddTool(toolSchema("list_databases", "List databases"), func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return textResult(`["other","lafdatav1"]`), nil
		})
		server.AddTool(toolSchema("list_tables", "List tables"), func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return textResult(`["chargereport_local"]`), nil
		})
	})
	svc := New(config.ClickHouse{Transport: config.TransportHTTP, URL: upstream.URL + "/mcp"}, config.Policy{
		QueryTimeout: time.Second, MaxResultRows: 20, MaxResponseBytes: 1 << 20, ReferDatabase: "lafdatav1", AllowedDatabases: []string{"lafdatav1"},
	}, false, nil)
	if err := svc.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	regs, err := svc.Registrations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, reg := range regs {
		names[reg.Tool.Name] = true
	}
	if !names[queryToolName] || !names[toolSchemaCatalog] || names["list_databases"] || names["list_tables"] {
		t.Fatalf("public tools = %#v", names)
	}
	catalog, err := handlerNamed(t, regs, toolSchemaCatalog)(context.Background(), callRaw(`{}`))
	if err != nil || catalog.IsError || !strings.Contains(textOf(catalog), "chargereport") || strings.Contains(textOf(catalog), "_local") || strings.Contains(textOf(catalog), "_mv") {
		t.Fatalf("catalog = %s err=%v", textOf(catalog), err)
	}
	query, err := handlerNamed(t, regs, queryToolName)(context.Background(), callRaw(`{"query":"SELECT count() FROM other.tripreport"}`))
	if err != nil || !query.IsError {
		t.Fatalf("query = %+v err=%v", query, err)
	}
	hidden, err := handlerNamed(t, regs, queryToolName)(context.Background(), callRaw(`{"query":"SELECT count() FROM chargereport_local"}`))
	if err != nil || !hidden.IsError || !strings.Contains(textOf(hidden), "_local") {
		t.Fatalf("hidden = %s err=%v", textOf(hidden), err)
	}
}

func TestPublicGatewayForwardsMetadataAndQuery(t *testing.T) {
	t.Parallel()
	upstream := fakeUpstream(t, func(server *mcp.Server) {
		server.AddTool(toolSchema(queryToolName, "Execute SQL"), echoQuery)
		server.AddTool(toolSchema("list_databases", "List databases"), func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return textResult(`["analytics"]`), nil
		})
	})
	svc := New(config.ClickHouse{Transport: config.TransportHTTP, URL: upstream.URL + "/mcp"}, config.Policy{
		QueryTimeout: time.Second, MaxResultRows: 20, MaxResponseBytes: 1 << 20,
	}, false, nil)
	gw := gateway.New(nil, []gateway.Source{svc}, time.Second)
	if err := gw.Bind(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gw.Close)
	httpServer := httptest.NewServer(gw.Handler())
	t.Cleanup(httpServer.Close)

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "downstream", Version: "0.0.1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range listed.Tools {
		names[tool.Name] = true
		if tool.Name == queryToolName && tool.InputSchema == nil {
			t.Fatal("query schema was not preserved")
		}
	}
	if !names[queryToolName] || !names[toolSchemaCatalog] || names["list_databases"] || names["list_tables"] || names["run_chdb_select_query"] {
		t.Fatalf("public tools = %#v", names)
	}
	if session.InitializeResult() == nil || !strings.Contains(session.InitializeResult().Instructions, "chargereport") || strings.Contains(session.InitializeResult().Instructions, "_local") {
		t.Fatalf("instructions = %#v", session.InitializeResult())
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: queryToolName, Arguments: map[string]any{"query": "SELECT count() FROM tripreport"}})
	if err != nil || result.IsError {
		t.Fatalf("query call = %+v err=%v", result, err)
	}
}

func TestQueryTraceLogsApproach(t *testing.T) {
	t.Parallel()
	upstream := fakeUpstream(t, func(server *mcp.Server) {
		server.AddTool(toolSchema(queryToolName, "Execute SQL"), echoQuery)
	})
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	svc := New(config.ClickHouse{Transport: config.TransportHTTP, URL: upstream.URL + "/mcp"}, config.Policy{
		QueryTimeout: time.Second, MaxResultRows: 20, MaxResponseBytes: 1 << 20, ReferDatabase: "lafdatav1", AllowedDatabases: []string{"lafdatav1"},
	}, false, logger)
	if err := svc.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	regs, err := svc.Registrations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result, err := handlerNamed(t, regs, queryToolName)(context.Background(), callRaw(`{"query":"SELECT count() FROM tripreport WHERE name = 'secret'"}`))
	if err != nil || result.IsError {
		t.Fatalf("query = %+v err=%v", result, err)
	}
	text := logs.String()
	for _, stage := range []string{"received", "accepted", "upstream", "completed"} {
		if !strings.Contains(text, `"stage":"`+stage+`"`) {
			t.Fatalf("missing stage %s in %s", stage, text)
		}
	}
	if !strings.Contains(text, "SELECT count() FROM tripreport WHERE name = ?") || strings.Contains(text, "secret") {
		t.Fatalf("sql trace = %s", text)
	}
	if !strings.Contains(text, `"trace":"q1"`) || !strings.Contains(text, `"database":"lafdatav1"`) {
		t.Fatalf("trace fields = %s", text)
	}
}

func fakeUpstream(t *testing.T, register func(*mcp.Server)) *httptest.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "upstream", Version: "0.0.1"}, nil)
	register(server)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	return httpServer
}

func dialHTTP(t *testing.T, endpoint string, timeout time.Duration) *Client {
	t.Helper()
	client, err := Dial(context.Background(), config.ClickHouse{Transport: config.TransportHTTP, URL: endpoint}, timeout, nil)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func toolSchema(name, description string) *mcp.Tool {
	return &mcp.Tool{
		Name:        name,
		Description: description,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":    map[string]any{"type": "string"},
				"database": map[string]any{"type": "string"},
				"params":   map[string]any{"type": "object"},
			},
		},
	}
}

func echoQuery(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args map[string]any
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return nil, err
	}
	query, _ := args["query"].(string)
	return textResult(query), nil
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
	return &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: json.RawMessage(payload)}}
}

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
