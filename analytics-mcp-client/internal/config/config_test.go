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
		if key == "XAI_API_KEY" {
			return "test-key"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "https://api.x.ai/v1" || cfg.Model != "grok-4.6" || cfg.MCPServerURL != "http://127.0.0.1:8080/mcp" {
		t.Fatalf("defaults = %+v", cfg)
	}
	if cfg.MaxToolCalls != 10 || cfg.RequestTimeout != 60*time.Second {
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
		{"XAI_API_KEY": "k", "XAI_BASE_URL": "ftp://api.x.ai/v1"},
		{"XAI_API_KEY": "k", "XAI_BASE_URL": "https://user:secret@api.x.ai/v1"},
		{"XAI_API_KEY": "k", "MCP_SERVER_URL": "http://user:pw@127.0.0.1:8080/mcp"},
		{"XAI_API_KEY": "k", "MAX_TOOL_CALLS": "0"},
		{"XAI_API_KEY": "k", "REQUEST_TIMEOUT_SECONDS": "nope"},
		{"XAI_API_KEY": "k", "XAI_MODEL": "grok-custom"},
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
	if err := os.WriteFile(path, []byte("XAI_API_KEY=file-secret\nXAI_MODEL=grok-4.6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLIENT_ENV_FILE", path)
	t.Setenv("XAI_API_KEY", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "file-secret" {
		t.Fatalf("key = %s", cfg.APIKey)
	}
	if os.Getenv("XAI_API_KEY") != "" {
		t.Fatal("API key was exported to the process environment")
	}
}
