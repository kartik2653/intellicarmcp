package gateway

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeSource struct {
	connected   bool
	closed      bool
	failConnect error
}

func (f *fakeSource) Name() string { return "fake" }

func (f *fakeSource) Connect(context.Context) error {
	if f.failConnect != nil {
		return f.failConnect
	}
	f.connected = true
	return nil
}

func (f *fakeSource) Registrations(context.Context) ([]Registration, error) {
	return []Registration{{
		Tool: &mcp.Tool{Name: "list_databases", Description: "databases", InputSchema: map[string]any{"type": "object"}},
		Handler: func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `["analytics"]`}}}, nil
		},
	}}, nil
}

func (f *fakeSource) Close() error {
	f.closed = true
	return nil
}

func TestPublicToolRegistrationAndUnknownTool(t *testing.T) {
	t.Parallel()
	source := &fakeSource{}
	server := New(nil, []Source{source}, time.Second)
	if err := server.Bind(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL + publicPath}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 1 || listed.Tools[0].Name != "list_databases" {
		t.Fatalf("tools = %#v", listed.Tools)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_databases"})
	if err != nil || result.IsError {
		t.Fatalf("call = %+v err=%v", result, err)
	}
	_, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "missing_tool"})
	if err == nil {
		t.Fatal("expected unknown tool error")
	}
}

func TestGracefulShutdownClosesSources(t *testing.T) {
	source := &fakeSource{}
	server := New(nil, []Source{source}, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Run(ctx, "127.0.0.1:0")
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !source.connected && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !source.connected {
		t.Fatal("source was not connected")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timed out")
	}
	if !source.closed {
		t.Fatal("source was not closed")
	}
}

func TestBindConnectionFailure(t *testing.T) {
	t.Parallel()
	source := &fakeSource{failConnect: context.DeadlineExceeded}
	server := New(nil, []Source{source}, time.Second)
	if err := server.Bind(context.Background()); err == nil {
		t.Fatal("expected connect error")
	}
	if !source.closed {
		t.Fatal("failed connect should still close sources")
	}
}
