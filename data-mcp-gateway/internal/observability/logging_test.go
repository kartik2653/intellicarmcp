package observability

import "testing"

func TestRedactURLAndSQL(t *testing.T) {
	t.Parallel()
	if got := RedactURL("http://user:secret@127.0.0.1:8000/mcp?token=abc"); got != "http://127.0.0.1:8000/mcp" {
		t.Fatalf("url = %s", got)
	}
	if got := RedactURL("not a url"); got != "" {
		t.Fatalf("invalid url = %q", got)
	}
	if !SecretEnvKey("CLICKHOUSE_PASSWORD") || !SecretEnvKey("CLICKHOUSE_MCP_AUTH_TOKEN") || SecretEnvKey("CLICKHOUSE_HOST") {
		t.Fatal("secret key detection mismatch")
	}
	got := RedactSQL("SELECT count() FROM t WHERE name = 'alice' AND note = 'a''b'")
	if got != "SELECT count() FROM t WHERE name = ? AND note = ?" {
		t.Fatalf("sql = %s", got)
	}
}
