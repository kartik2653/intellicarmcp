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
