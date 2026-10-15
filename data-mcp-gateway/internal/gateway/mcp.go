package gateway

import (
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newMCPServer(instructions string) *mcp.Server {
	return mcp.NewServer(&mcp.Implementation{
		Name:    "data-mcp-gateway",
		Version: "0.1.0",
	}, &mcp.ServerOptions{Instructions: instructions})
}

func newStreamHandler(server *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{
		SessionTimeout: 30 * time.Minute,
	})
}
