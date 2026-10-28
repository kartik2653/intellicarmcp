package clickhouse

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCatalogSerializationAndSelection(t *testing.T) {
	t.Parallel()
	raw := CatalogJSON()
	if strings.Contains(raw, "_local") || strings.Contains(raw, "_mv") {
		t.Fatal("catalog exposes internal table names")
	}
	if strings.Contains(BriefInstructions(), "_local") || strings.Contains(BriefInstructions(), "_mv") {
		t.Fatal("instructions expose internal table names")
	}
	guidance := BriefInstructions()
	for _, phrase := range []string{"user's intent", "EXPLAIN ESTIMATE", "aggregation", "bucket", "not a partial", "SQL LIMIT"} {
		if !strings.Contains(guidance, phrase) {
			t.Fatalf("instructions missing %q", phrase)
		}
	}
	for _, phrase := range []string{"six months", "6 months", "always summarize"} {
		if strings.Contains(strings.ToLower(guidance), phrase) {
			t.Fatalf("instructions contain a date rule %q", phrase)
		}
	}
	limits := LimitInstructions(1000, 1048576, 30*time.Second)
	if !strings.Contains(limits, "MAX_RESULT_ROWS=1000") || strings.Contains(limits, "month") {
		t.Fatalf("limits = %s", limits)
	}
	var catalog Catalog
	if err := json.Unmarshal([]byte(raw), &catalog); err != nil {
		t.Fatal(err)
	}
	names := map[string]Table{}
	for _, domain := range catalog.Domains {
		for _, table := range domain.Tables {
			names[table.Name] = table
		}
	}
	for _, name := range []string{
		"chargereport", "invalid_chargereport",
		"chargereport_dailybucket", "chargereport_weeklybucket", "chargereport_monthlybucket",
		"tripreport", "invalidtripreport",
		"tripreport_dailybucket", "tripreport_weeklybucket", "tripreport_monthlybucket",
		"geofenceevent", "alertevent",
	} {
		table, ok := names[name]
		if !ok {
			t.Fatalf("missing table %s", name)
		}
		if len(table.Columns) == 0 {
			t.Fatalf("table %s has no columns", name)
		}
	}
	if names["tripreport_dailybucket"].ColumnsNamed("totaldistance").SQLType != "Decimal(10,3)" {
		t.Fatal("totaldistance type was not preserved")
	}
	if names["alertevent"].ColumnsNamed("alertdata").SQLType != "" {
		t.Fatal("alertdata type must stay unconfirmed")
	}
	if got := QueryTables(); len(got) != len(names) {
		t.Fatalf("query tables = %d, catalog tables = %d", len(got), len(names))
	}
	questions := map[string]string{
		"Total distance over a long date range":        "tripreport_dailybucket",
		"Detailed trips or exact timestamp analysis":   "tripreport",
		"Total trip count and distance by fleet":       "tripreport_dailybucket",
		"Charging session count and total SOC gain":    "chargereport_dailybucket",
		"Individual charging-session details":          "chargereport",
		"Invalid charging sessions":                    "invalid_chargereport",
		"Invalid trips":                                "invalidtripreport",
		"Geofence entry/exit counts":                   "geofenceevent",
		"Alert counts by time, vehicle, or alert name": "alertevent",
		"Vehicle-level or fleet-level trends":          "tripreport_dailybucket",
		"Calendar-month statistics":                    "tripreport",
	}
	for question, want := range questions {
		got, ok := SelectTable(question)
		if !ok || got != want {
			t.Fatalf("SelectTable(%q) = %q ok=%v", question, got, ok)
		}
		if _, exists := names[got]; !exists {
			t.Fatalf("selected table %s is not in the catalog", got)
		}
	}
	if _, ok := SelectTable("unknown question"); ok {
		t.Fatal("unknown question selected a table")
	}
	if !strings.Contains(catalog.Time.MonthMeaning, "not a calendar month") {
		t.Fatal("month bucket meaning missing")
	}
}

func (t Table) ColumnsNamed(name string) Column {
	for _, column := range t.Columns {
		if column.Name == name {
			return column
		}
	}
	return Column{}
}
