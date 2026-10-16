package mcpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDialDiscoversAndCalls(t *testing.T) {
	t.Parallel()
	upstream := fakeMCP(t, func(server *mcp.Server) {
		server.AddTool(&mcp.Tool{
			Name:        "run_query",
			Description: "Execute SQL",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"query": map[string]any{"type": "string"}},
				"required":   []any{"query"},
			},
		}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var args map[string]any
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return nil, err
			}
			query, _ := args["query"].(string)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: query}}}, nil
		})
	})
	client, err := Dial(context.Background(), upstream.URL+"/mcp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if len(client.Names()) != 1 || client.Definitions()[0].Name != "run_query" {
		t.Fatalf("tools = %#v", client.Names())
	}
	text, err := client.Execute(context.Background(), "run_query", json.RawMessage(`{"query":"SELECT 1"}`))
	if err != nil || text != "SELECT 1" {
		t.Fatalf("text=%s err=%v", text, err)
	}
	if _, err := client.Execute(context.Background(), "drop_database", json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected unknown tool")
	}
	if _, err := client.Execute(context.Background(), "run_query", json.RawMessage(`{"query":1}`)); err == nil {
		t.Fatal("expected invalid arguments")
	}
}

func TestExecuteKeepsToolErrorAndTruncates(t *testing.T) {
	t.Parallel()
	upstream := fakeMCP(t, func(server *mcp.Server) {
		server.AddTool(&mcp.Tool{Name: "run_query", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", maxResultBytes+10)}}}, nil
		})
	})
	client, err := Dial(context.Background(), upstream.URL+"/mcp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	text, err := client.Execute(context.Background(), "run_query", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(text, "[truncated:") {
		t.Fatalf("text suffix=%q err=%v", text[len(text)-40:], err)
	}
}

func TestDialConnectionFailure(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := Dial(ctx, "http://127.0.0.1:1/mcp")
	if err == nil {
		t.Fatal("expected connection error")
	}
}

func TestCloseSession(t *testing.T) {
	t.Parallel()
	upstream := fakeMCP(t, func(server *mcp.Server) {
		server.AddTool(&mcp.Tool{Name: "list_databases", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "[]"}}}, nil
		})
	})
	client, err := Dial(context.Background(), upstream.URL+"/mcp")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = client.Execute(context.Background(), "list_databases", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("err = %v", err)
	}
}

func fakeMCP(t *testing.T, register func(*mcp.Server)) *httptest.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "gateway", Version: "0.0.1"}, nil)
	register(server)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	return httpServer
}
