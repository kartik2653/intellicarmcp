package policy

import (
	"strings"
	"testing"
)

func tripBinding(ids ...string) map[string]TableBinding {
	set := map[string]struct{}{}
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return map[string]TableBinding{
		"tripreport": {Column: "fleetid", IDs: set},
		"alertevent": {Column: "vehicleid", IDs: set},
	}
}

func columns() map[string][]string {
	return map[string][]string{
		"tripreport": {"fleetid", "totaldistance"},
		"alertevent": {"vehicleid", "alertid"},
	}
}

func TestApplyScopeWrapsPhysicalTable(t *testing.T) {
	t.Parallel()
	sql, err := ApplyScope("SELECT count() FROM tripreport WHERE fleetid = 'f1'", tripBinding("f1"), columns())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "fleetid IN") || !strings.Contains(sql, "'f1'") || !strings.Contains(sql, "totaldistance") {
		t.Fatalf("sql = %s", sql)
	}
}

func TestApplyScopeKeepsDateFiltersAndDoesNotRewriteStrategy(t *testing.T) {
	t.Parallel()
	bindings := map[string]TableBinding{
		"tripreport":             {Column: "fleetid", IDs: map[string]struct{}{"f1": {}}},
		"tripreport_dailybucket": {Column: "fleetid", IDs: map[string]struct{}{"f1": {}}},
	}
	cols := map[string][]string{
		"tripreport":             {"fleetid", "vehicleid", "starttime"},
		"tripreport_dailybucket": {"fleetid", "utctime", "totaldistance"},
	}
	long, err := ApplyScope("SELECT sum(totaldistance) FROM tripreport_dailybucket WHERE utctime >= '2020-01-01' AND utctime < '2021-01-01'", bindings, cols)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(long, "sum(totaldistance)") || !strings.Contains(long, "2020-01-01") || !strings.Contains(long, "fleetid IN") {
		t.Fatalf("long = %s", long)
	}
	short, err := ApplyScope("SELECT vehicleid, starttime FROM tripreport WHERE starttime >= '2024-01-01' AND starttime < '2024-01-02'", bindings, cols)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(short, "vehicleid") || !strings.Contains(short, "2024-01-01") || strings.Contains(short, "sum(totaldistance)") {
		t.Fatalf("short = %s", short)
	}
}

func TestApplyScopeWrapsExplainEstimate(t *testing.T) {
	t.Parallel()
	sql, err := ApplyScope("EXPLAIN ESTIMATE SELECT count() FROM tripreport WHERE fleetid = 'f1'", tripBinding("f1"), columns())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "EXPLAIN ESTIMATE") || !strings.Contains(sql, "fleetid IN") || !strings.Contains(sql, "'f1'") {
		t.Fatalf("sql = %s", sql)
	}
	if _, err := ApplyScope("EXPLAIN ESTIMATE SELECT count() FROM tripreport WHERE fleetid = 'other'", tripBinding("f1"), columns()); err == nil {
		t.Fatal("expected unauthorized fleet on estimate")
	}
}

func TestApplyScopeRejectsUnauthorizedFilterAndAlias(t *testing.T) {
	t.Parallel()
	if _, err := ApplyScope("SELECT count() FROM tripreport WHERE fleetid = 'other'", tripBinding("f1"), columns()); err == nil {
		t.Fatal("expected unauthorized fleet")
	}
	if _, err := ApplyScope("WITH tripreport AS (SELECT 1 AS fleetid) SELECT fleetid FROM tripreport", tripBinding("f1"), columns()); err == nil {
		t.Fatal("expected shadow rejection")
	}
	if _, err := ApplyScope("SELECT count() FROM alertevent", map[string]TableBinding{
		"alertevent": {Column: "vehicleid", IDs: map[string]struct{}{}},
	}, columns()); err == nil {
		t.Fatal("expected empty scope denial")
	}
}
