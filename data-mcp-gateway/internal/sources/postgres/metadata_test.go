package postgres

import (
	"testing"
)

func TestParseJSONRowsAndMerge(t *testing.T) {
	t.Parallel()
	rows, err := parseJSONRows(`[{"table_schema":"fleetmodule","table_name":"fleets","table_type":"BASE TABLE","table_description":"Fleet list"}]`)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	cols, err := parseJSONRows(`{"rows":[{"table_schema":"fleetmodule","table_name":"fleets","ordinal_position":1,"column_name":"fleetid","data_type":"text","is_nullable":"NO"}]}`)
	if err != nil || len(cols) != 1 {
		t.Fatalf("cols=%v err=%v", cols, err)
	}
	meta := mergeMetadata([]string{"fleetmodule.fleets", "fleetmodule.vehicle"}, rows, cols)
	fleets := meta["fleetmodule.fleets"]
	if fleets.Description != "Fleet list" || len(fleets.Columns) != 1 || fleets.Columns[0].Name != "fleetid" || fleets.Columns[0].Nullable {
		t.Fatalf("fleets = %#v", fleets)
	}
	if _, ok := meta["fleetmodule.vehicle"]; !ok {
		t.Fatal("missing approved table with empty metadata")
	}
}

func TestApprovedINClauseRejectsInjection(t *testing.T) {
	t.Parallel()
	if _, err := approvedINClause([]string{`fleetmodule.fleets'; drop table x`}); err == nil {
		t.Fatal("expected rejection")
	}
	clause, err := approvedINClause([]string{"fleetmodule.fleets", "FleetModule.Vehicle"})
	if err != nil || clause != `'fleetmodule.fleets', 'fleetmodule.vehicle'` {
		t.Fatalf("clause=%q err=%v", clause, err)
	}
}

func TestParseJSONRowsEmpty(t *testing.T) {
	t.Parallel()
	rows, err := parseJSONRows("")
	if err != nil || rows != nil {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
}
