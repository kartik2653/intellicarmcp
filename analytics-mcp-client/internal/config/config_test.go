package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := LoadFrom(func(key string) string {
		if key == "ANTHROPIC_API_KEY" {
			return "test-key"
		}
		if key == "ANALYTICS_ACCESS_TOKEN" {
			return "user-token"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "https://api.anthropic.com" || cfg.Model != "claude-sonnet-5-5" || cfg.Effort != "medium" || cfg.MaxTokens != 32000 || cfg.MCPServerURL != "http://127.0.0.1:8080/mcp" {
		t.Fatalf("defaults = %+v", cfg)
	}
	if cfg.MaxToolCalls != 10 || cfg.MaxResultBytes != 16*1024 || cfg.RequestTimeout != 60*time.Second {
		t.Fatalf("limits = %+v", cfg)
	}
	if cfg.ReferDatabase != "lafdatav1" {
		t.Fatalf("database = %s", cfg.ReferDatabase)
	}
}

func TestLoadRejectsInvalidConfig(t *testing.T) {
	t.Parallel()
	cases := []map[string]string{
		{},
		{"ANTHROPIC_API_KEY": "k", "ANTHROPIC_BASE_URL": "ftp://api.anthropic.com"},
		{"ANTHROPIC_API_KEY": "k", "ANTHROPIC_BASE_URL": "https://user:secret@api.anthropic.com"},
		{"ANTHROPIC_API_KEY": "k", "MCP_SERVER_URL": "http://user:pw@127.0.0.1:8080/mcp"},
		{"ANTHROPIC_API_KEY": "k", "MAX_TOOL_CALLS": "0"},
		{"ANTHROPIC_API_KEY": "k", "MCP_MAX_RESULT_BYTES": "0"},
		{"ANTHROPIC_API_KEY": "k", "MCP_MAX_RESULT_BYTES": "nope"},
		{"ANTHROPIC_API_KEY": "k", "REQUEST_TIMEOUT_SECONDS": "nope"},
		{"ANTHROPIC_API_KEY": "k", "ANTHROPIC_EFFORT": "turbo"},
		{"ANTHROPIC_API_KEY": "k", "ANTHROPIC_MODEL": "claude-custom", "ANALYTICS_ACCESS_TOKEN": "user-token"},
	}
	for i, env := range cases {
		_, err := LoadFrom(func(key string) string { return env[key] })
		if i == len(cases)-1 {
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("expected error for %#v", env)
		}
	}
}

func TestLoadFileDoesNotExportAPIKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "client.env")
	if err := os.WriteFile(path, []byte("ANTHROPIC_API_KEY=file-secret\nANTHROPIC_MODEL=claude-sonnet-5-5\nANALYTICS_ACCESS_TOKEN=user-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLIENT_ENV_FILE", path)
	t.Setenv("ANTHROPIC_API_KEY", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "file-secret" {
		t.Fatalf("key = %s", cfg.APIKey)
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		t.Fatal("API key was exported to the process environment")
	}
}
