package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseEnvFile(t *testing.T) {
	t.Parallel()
	values, err := parseEnvFile([]byte(`
# comment
HTTP_ADDR=127.0.0.1:9090
CLICKHOUSE_MCP_ENV={"CLICKHOUSE_PASSWORD":"secret"}
CLICKHOUSE_MCP_AUTH_TOKEN="quoted-token"
export CLICKHOUSE_MCP_COMMAND=uv
EMPTY=
NOTE=hello # trailing
`))
	if err != nil {
		t.Fatal(err)
	}
	if values["CLICKHOUSE_MCP_ENV"] != `{"CLICKHOUSE_PASSWORD":"secret"}` {
		t.Fatalf("env json = %s", values["CLICKHOUSE_MCP_ENV"])
	}
	if values["CLICKHOUSE_MCP_AUTH_TOKEN"] != "quoted-token" || values["CLICKHOUSE_MCP_COMMAND"] != "uv" {
		t.Fatalf("values = %#v", values)
	}
	if values["NOTE"] != "hello" || values["EMPTY"] != "" {
		t.Fatalf("values = %#v", values)
	}
}

func TestLoadFileDoesNotExportSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway.env")
	contents := "CLICKHOUSE_MCP_COMMAND=uv\nCLICKHOUSE_MCP_ENV={\"CLICKHOUSE_PASSWORD\":\"secret\"}\nCLICKHOUSE_MCP_AUTH_TOKEN=token-value\nPERMISSIONS_API_BASE_URL=https://permissions.example\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATEWAY_ENV_FILE", path)
	t.Setenv("CLICKHOUSE_MCP_COMMAND", "from-process")
	t.Setenv("CLICKHOUSE_MCP_ENV", "")
	t.Setenv("CLICKHOUSE_MCP_AUTH_TOKEN", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClickHouse.Command != "uv" {
		t.Fatalf("command = %s", cfg.ClickHouse.Command)
	}
	if cfg.ClickHouse.Env["CLICKHOUSE_PASSWORD"] != "secret" {
		t.Fatal("password was not read from the file")
	}
	if cfg.ClickHouse.AuthToken != "token-value" {
		t.Fatal("token was not read from the file")
	}
	if os.Getenv("CLICKHOUSE_MCP_ENV") != "" || os.Getenv("CLICKHOUSE_MCP_AUTH_TOKEN") != "" {
		t.Fatal("Load exported secret values into the process environment")
	}
}

func TestLoadMissingDefaultFileUsesProcessEnv(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("GATEWAY_ENV_FILE", "")
	t.Setenv("CLICKHOUSE_MCP_COMMAND", "from-process")
	t.Setenv("PERMISSIONS_API_BASE_URL", "https://permissions.example")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClickHouse.Command != "from-process" {
		t.Fatalf("command = %s", cfg.ClickHouse.Command)
	}
}

func TestParseEnvFileRejectsInvalidLines(t *testing.T) {
	t.Parallel()
	if _, err := parseEnvFile([]byte("NOTAKEY\n")); err == nil {
		t.Fatal("expected invalid line error")
	}
	if _, err := parseEnvFile([]byte("TOKEN=\"unterminated\n")); err == nil {
		t.Fatal("expected quote error")
	}
}
