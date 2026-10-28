package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Intellicars/intellicarmcp/analytics-mcp-client/internal/agent"
	"github.com/Intellicars/intellicarmcp/analytics-mcp-client/internal/cli"
	"github.com/Intellicars/intellicarmcp/analytics-mcp-client/internal/config"
	"github.com/Intellicars/intellicarmcp/analytics-mcp-client/internal/llm"
	"github.com/Intellicars/intellicarmcp/analytics-mcp-client/internal/mcpclient"
)

func main() {
	verbose := flag.Bool("v", false, "print tool names and execution durations")
	flag.Parse()
	if flag.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "Type questions at the You: prompt. Do not pass the question as an argument.")
		os.Exit(2)
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %s\n", err.Error())
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	gateway, err := mcpclient.Dial(ctx, cfg.MCPServerURL, cfg.AccessToken, cfg.MaxResultBytes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "MCP connection error: %s\n", err.Error())
		os.Exit(1)
	}
	defer gateway.Close()

	model := &llm.Client{
		BaseURL:    cfg.BaseURL,
		APIKey:     cfg.APIKey,
		Model:      cfg.Model,
		Effort:     cfg.Effort,
		MaxTokens:  cfg.MaxTokens,
		HTTPClient: llm.TimeoutClient(cfg.RequestTimeout),
	}
	app := &cli.App{
		In:        os.Stdin,
		Out:       os.Stdout,
		Model:     cfg.Model,
		ToolNames: gateway.Names(),
		Handshake: handshakeView(gateway.Snapshot()),
		Verbose:   *verbose,
		Reload: func(ctx context.Context) (cli.Handshake, []string, error) {
			if err := gateway.Reload(ctx); err != nil {
				return cli.Handshake{}, nil, err
			}
			return handshakeView(gateway.Snapshot()), gateway.Names(), nil
		},
	}
	loop := &agent.Loop{
		LLM:           model,
		Tools:         gateway,
		MaxToolCalls:  cfg.MaxToolCalls,
		Timeout:       cfg.RequestTimeout,
		ReferDatabase: cfg.ReferDatabase,
		Instructions:  gateway.Instructions(),
		Hooks: agent.Hooks{
			Assistant: func(text string) {
				fmt.Fprintf(app.Out, "Assistant: %s\n", text)
			},
			ToolStart: app.PrintToolStart,
			ToolDone:  app.PrintToolDone,
		},
	}
	app.Asker = loop
	if err := app.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Client error: %s\n", err.Error())
		os.Exit(1)
	}
}

func handshakeView(snapshot mcpclient.Handshake) cli.Handshake {
	view := cli.Handshake{
		Endpoint:      snapshot.Endpoint,
		Protocol:      snapshot.Protocol,
		ServerName:    snapshot.ServerName,
		ServerVersion: snapshot.ServerVersion,
		Instructions:  snapshot.Instructions,
	}
	for _, tool := range snapshot.Tools {
		view.Tools = append(view.Tools, cli.ToolInfo{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: string(tool.InputSchema),
		})
	}
	return view
}
