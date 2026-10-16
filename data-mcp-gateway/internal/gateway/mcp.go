package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newMCPServer(instructions string, logger *slog.Logger) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "data-mcp-gateway",
		Version: "0.1.0",
	}, &mcp.ServerOptions{Instructions: instructions})
	server.AddReceivingMiddleware(handshakeMiddleware(logger))
	return server
}

func handshakeMiddleware(logger *slog.Logger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, req)
			if err != nil || logger == nil {
				return result, err
			}
			sessionID := ""
			if req != nil && req.GetSession() != nil {
				sessionID = req.GetSession().ID()
			}
			switch method {
			case "initialize":
				protocol := ""
				clientName := ""
				if params, ok := req.GetParams().(*mcp.InitializeParams); ok && params != nil {
					protocol = params.ProtocolVersion
					if params.ClientInfo != nil {
						clientName = params.ClientInfo.Name
					}
				}
				if init, ok := result.(*mcp.InitializeResult); ok && init != nil && init.ProtocolVersion != "" {
					protocol = init.ProtocolVersion
				}
				logger.Info("mcp_handshake", "stage", "initialize", "protocol", protocol, "client", clientName, "session", sessionID)
			case "tools/list":
				listed, ok := result.(*mcp.ListToolsResult)
				if !ok || listed == nil {
					return result, err
				}
				tools := make([]map[string]string, 0, len(listed.Tools))
				for _, tool := range listed.Tools {
					if tool == nil {
						continue
					}
					tools = append(tools, map[string]string{
						"name":        tool.Name,
						"description": tool.Description,
					})
				}
				logger.Info("mcp_handshake", "stage", "tools_listed", "session", sessionID, "tools", tools)
			}
			return result, err
		}
	}
}

func newStreamHandler(server *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{
		SessionTimeout: 30 * time.Minute,
	})
}
