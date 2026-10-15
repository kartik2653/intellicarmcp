package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/Intellicars/intellicarmcp/data-mcp-gateway/internal/config"
	"github.com/Intellicars/intellicarmcp/data-mcp-gateway/internal/gateway"
	"github.com/Intellicars/intellicarmcp/data-mcp-gateway/internal/observability"
	"github.com/Intellicars/intellicarmcp/data-mcp-gateway/internal/sources/clickhouse"
)

func main() {
	logger := observability.New(os.Stderr)
	cfg, err := config.Load()
	if err != nil {
		logger.Error("config_invalid", "error", err.Error())
		os.Exit(1)
	}
	logger.Info("gateway_starting",
		"addr", cfg.HTTPAddr,
		"transport", cfg.ClickHouse.Transport,
		"upstream", observability.RedactURL(cfg.ClickHouse.URL),
		"database", cfg.Policy.ReferDatabase,
		"aggregation_only", cfg.Policy.AggregationOnly,
		"log_sql", cfg.LogSQL,
	)
	if cfg.ClickHouse.Transport == config.TransportHTTP && len(cfg.ClickHouse.Env) > 0 {
		logger.Warn("clickhouse_mcp_env_ignored", "reason", "CLICKHOUSE_MCP_ENV is applied only to the stdio subprocess")
	}

	source := clickhouse.New(cfg.ClickHouse, cfg.Policy, cfg.LogSQL, logger)
	server := gateway.New(logger, []gateway.Source{source}, cfg.Policy.QueryTimeout)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := server.Run(ctx, cfg.HTTPAddr); err != nil {
		logger.Error("gateway_failed", "error", err.Error())
		os.Exit(1)
	}
}
