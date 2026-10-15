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

	gateway, err := mcpclient.Dial(ctx, cfg.MCPServerURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "MCP connection error: %s\n", err.Error())
		os.Exit(1)
	}
	defer gateway.Close()

	model := &llm.Client{
		BaseURL:    cfg.BaseURL,
		APIKey:     cfg.APIKey,
		Model:      cfg.Model,
		HTTPClient: llm.TimeoutClient(cfg.RequestTimeout),
	}
	app := &cli.App{
		In:        os.Stdin,
		Out:       os.Stdout,
		Model:     cfg.Model,
		ToolNames: gateway.Names(),
		Verbose:   *verbose,
		Reload: func(ctx context.Context) ([]string, error) {
			if err := gateway.Reload(ctx); err != nil {
				return nil, err
			}
			return gateway.Names(), nil
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
