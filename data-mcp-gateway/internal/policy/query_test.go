package policy

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestQueryPolicy(t *testing.T) {
	t.Parallel()
	basic := New(Options{})
	agg := New(Options{AggregationOnly: true, AllowedDatabases: []string{"analytics"}})
	ctx := context.Background()

	allowed := []string{
		"SELECT 1",
		"SELECT count() FROM analytics.events",
		"WITH daily AS (SELECT count() AS c FROM analytics.events) SELECT sum(c) FROM daily",
		"SHOW DATABASES",
		"DESCRIBE analytics.events",
		"EXPLAIN ESTIMATE SELECT count() FROM analytics.events",
		"SELECT count() FROM analytics.events WHERE name = 'a; drop'",
	}
	for _, sql := range allowed {
		if err := basic.Validate(ctx, Query{SQL: sql}); err != nil {
			t.Fatalf("basic rejected %q: %v", sql, err)
		}
	}
	for _, sql := range []string{
		"SELECT count() FROM analytics.events",
		"SELECT count() FROM events",
		"SELECT sum(amount) FROM analytics.events GROUP BY user",
	} {
		if err := agg.Validate(ctx, Query{SQL: sql}); err != nil {
			t.Fatalf("aggregation rejected %q: %v", sql, err)
		}
	}

	rejected := []struct {
		policy QueryPolicy
		sql    string
		want   string
	}{
		{basic, "", "empty"},
		{basic, "INSERT INTO analytics.events VALUES (1)", "read-only"},
		{basic, "DROP TABLE analytics.events", "read-only"},
		{basic, "ALTER TABLE analytics.events DELETE WHERE 1", "read-only"},
		{basic, "SELECT 1; SELECT 2", "multiple"},
		{basic, "SELECT 1; DROP TABLE analytics.events", "multiple"},
		{basic, "NOT SQL AT ALL !!!", "parsed"},
		{agg, "SELECT * FROM analytics.events", "SELECT *"},
		{agg, "SELECT user FROM analytics.events", "aggregate"},
		{agg, "SHOW DATABASES", "only database"},
		{agg, "SELECT count() FROM other.events", "not in ALLOWED_DATABASES"},
		{agg, "SELECT count() FROM remote('localhost', analytics.events)", "table functions"},
		{basic, "SELECT count() FROM chargereport_local", "_local"},
		{basic, "SELECT count() FROM lafdatav1.tripreport_mv", "_mv"},
		{basic, "WITH daily AS (SELECT count() FROM chargereport_local) SELECT count() FROM daily", "_local"},
		{basic, "SELECT count() FROM (SELECT vehicleid FROM tripreport_mv)", "_mv"},
		{basic, "SELECT * FROM tripreport", "SELECT *"},
		{New(Options{AllowedTables: []string{"tripreport"}}), "SELECT count() FROM events", "schema catalog"},
		{New(Options{AllowedTables: []string{"tripreport"}}), "WITH daily AS (SELECT count() AS c FROM events) SELECT sum(c) FROM daily", "schema catalog"},
	}
	allowedCatalog := New(Options{AllowedDatabases: []string{"lafdatav1"}, AllowedTables: []string{"tripreport"}})
	for _, sql := range []string{
		"SELECT count() FROM tripreport",
		"SELECT count() FROM lafdatav1.tripreport",
		"WITH daily AS (SELECT count() AS c FROM tripreport) SELECT sum(c) FROM daily",
		"SELECT vehicleid FROM tripreport",
	} {
		if err := allowedCatalog.Validate(ctx, Query{SQL: sql}); err != nil {
			t.Fatalf("catalog rejected %q: %v", sql, err)
		}
	}
	for _, tc := range rejected {
		err := tc.policy.Validate(ctx, Query{SQL: tc.sql})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("sql %q error = %v, want substring %q", tc.sql, err, tc.want)
		}
	}
}

func TestEnforceResultLimits(t *testing.T) {
	t.Parallel()
	result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `[{"a":1},{"a":2},{"a":3}]`}}}
	limited, err := EnforceResult(result, Limits{MaxResultRows: 2, MaxResponseBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if !limited.IsError || !strings.Contains(textOf(limited), "MAX_RESULT_ROWS") || !strings.Contains(textOf(limited), "not a partial report") {
		t.Fatalf("row limit result = %+v", limited)
	}

	huge := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", 500)}}}
	limited, err = EnforceResult(huge, Limits{MaxResultRows: 200, MaxResponseBytes: 80})
	if err != nil {
		t.Fatal(err)
	}
	if !limited.IsError || strings.Contains(textOf(limited), strings.Repeat("x", 50)) {
		t.Fatal("byte limit must not return a truncated payload")
	}
	if !strings.Contains(textOf(limited), "MAX_RESPONSE_BYTES") {
		t.Fatalf("byte limit text = %s", textOf(limited))
	}

	ok, err := EnforceResult(result, Limits{MaxResultRows: 3, MaxResponseBytes: 1 << 20})
	if err != nil || ok.IsError {
		t.Fatalf("expected original result, err=%v isError=%v", err, ok != nil && ok.IsError)
	}
}

func TestEnforceResultNestedRows(t *testing.T) {
	t.Parallel()
	result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"rows":[1,2]}`}}}
	limited, err := EnforceResult(result, Limits{MaxResultRows: 1, MaxResponseBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if !limited.IsError {
		t.Fatal("expected row rejection for rows field")
	}
}

func textOf(result *mcp.CallToolResult) string {
	if result == nil || len(result.Content) == 0 {
		return ""
	}
	text, _ := result.Content[0].(*mcp.TextContent)
	if text == nil {
		return ""
	}
	return text.Text
}
