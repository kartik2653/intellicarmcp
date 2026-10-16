package config

import (
	"testing"
	"time"
)

func TestLoadDefaultsAndValidation(t *testing.T) {
	t.Parallel()
	cfg, err := LoadFrom(func(key string) string {
		if key == "CLICKHOUSE_MCP_COMMAND" {
			return "mcp-clickhouse"
		}
		if key == "PERMISSIONS_API_BASE_URL" {
			return "https://permissions.example"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.HTTPAddr != "127.0.0.1:8080" {
		t.Fatalf("addr = %s", cfg.HTTPAddr)
	}
	if cfg.ClickHouse.Transport != TransportStdio {
		t.Fatalf("transport = %s", cfg.ClickHouse.Transport)
	}
	if cfg.Policy.QueryTimeout != 30*time.Second || cfg.Policy.MaxResultRows != 200 || cfg.Policy.MaxResponseBytes != 1<<20 {
		t.Fatalf("unexpected defaults: %+v", cfg.Policy)
	}
	if cfg.Policy.AggregationOnly || cfg.LogSQL {
		t.Fatalf("flags should default off")
	}
	if cfg.Policy.ReferDatabase != "lafdatav1" || len(cfg.Policy.AllowedDatabases) != 1 || cfg.Policy.AllowedDatabases[0] != "lafdatav1" {
		t.Fatalf("refer database = %+v", cfg.Policy)
	}
	if cfg.ClickHouse.Env["CLICKHOUSE_DATABASE"] != "lafdatav1" {
		t.Fatalf("upstream database = %#v", cfg.ClickHouse.Env)
	}
}

func TestLoadHTTPAndJSONFields(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"CLICKHOUSE_MCP_TRANSPORT":    "http",
		"CLICKHOUSE_MCP_URL":          "http://127.0.0.1:8000/mcp",
		"CLICKHOUSE_MCP_ARGS":         `["run","mcp-clickhouse"]`,
		"CLICKHOUSE_MCP_ENV":          `{"CLICKHOUSE_HOST":"localhost","CLICKHOUSE_PASSWORD":"secret"}`,
		"QUERY_TIMEOUT_SECONDS":       "15",
		"MAX_RESULT_ROWS":             "10",
		"MAX_RESPONSE_BYTES":          "4096",
		"AGGREGATION_ONLY":            "true",
		"REFER_DATABASE":              "lafdatav1",
		"LOG_SQL":                     "on",
		"CLICKHOUSE_MCP_AUTH_TOKEN":   "token-value",
		"PERMISSIONS_API_BASE_URL":    "https://permissions.example",
		"PERMISSIONS_TIMEOUT_SECONDS": "3",
	}
	cfg, err := LoadFrom(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.ClickHouse.Transport != TransportHTTP || cfg.ClickHouse.URL != env["CLICKHOUSE_MCP_URL"] {
		t.Fatalf("http config = %+v", cfg.ClickHouse)
	}
	if len(cfg.ClickHouse.Args) != 2 || cfg.ClickHouse.Env["CLICKHOUSE_PASSWORD"] != "secret" {
		t.Fatalf("json fields = %+v", cfg.ClickHouse)
	}
	if !cfg.Policy.AggregationOnly || !cfg.LogSQL || cfg.Policy.MaxResultRows != 10 {
		t.Fatalf("policy = %+v log=%v", cfg.Policy, cfg.LogSQL)
	}
	if cfg.Policy.ReferDatabase != "lafdatav1" || cfg.ClickHouse.Env["CLICKHOUSE_DATABASE"] != "lafdatav1" {
		t.Fatalf("refer database = %+v env=%#v", cfg.Policy, cfg.ClickHouse.Env)
	}
	if cfg.ClickHouse.AuthToken != "token-value" {
		t.Fatal("auth token was not preserved")
	}
	if cfg.Permissions.BaseURL != "https://permissions.example" || cfg.Permissions.Timeout != 3*time.Second {
		t.Fatalf("permissions = %+v", cfg.Permissions)
	}
}

func TestLoadRejectsInvalidConfig(t *testing.T) {
	t.Parallel()
	cases := []map[string]string{
		{},
		{"CLICKHOUSE_MCP_TRANSPORT": "http"},
		{"CLICKHOUSE_MCP_TRANSPORT": "http", "CLICKHOUSE_MCP_URL": "http://user:pass@127.0.0.1:8000/mcp"},
		{"CLICKHOUSE_MCP_TRANSPORT": "sse", "CLICKHOUSE_MCP_COMMAND": "mcp-clickhouse"},
		{"CLICKHOUSE_MCP_COMMAND": "mcp-clickhouse", "CLICKHOUSE_MCP_ARGS": "run mcp-clickhouse"},
		{"CLICKHOUSE_MCP_COMMAND": "mcp-clickhouse", "CLICKHOUSE_MCP_ENV": `["nope"]`},
		{"CLICKHOUSE_MCP_COMMAND": "mcp-clickhouse", "QUERY_TIMEOUT_SECONDS": "0"},
		{"CLICKHOUSE_MCP_COMMAND": "mcp-clickhouse", "MAX_RESULT_ROWS": "-1"},
		{"CLICKHOUSE_MCP_COMMAND": "mcp-clickhouse", "MAX_RESPONSE_BYTES": "abc"},
		{"CLICKHOUSE_MCP_COMMAND": "mcp-clickhouse", "REFER_DATABASE": "other db"},
		{"CLICKHOUSE_MCP_TRANSPORT": "http", "CLICKHOUSE_MCP_URL": "ftp://127.0.0.1/mcp"},
	}
	for _, env := range cases {
		_, err := LoadFrom(func(key string) string { return env[key] })
		if err == nil {
			t.Fatalf("expected error for %#v", env)
		}
	}
}
