package postgres

import (
	"strings"
	"testing"
)

func columns() map[string][]string {
	return map[string][]string{
		"fleetmodule.fleets":           {"fleetid"},
		"fleetmodule.vehicle":          {"vehicleid", "fleetid"},
		"geofencemodule.geofencerule":  nil,
		"geofencemodule.geofencerulefleet": nil,
		"alertmodule.alert":            nil,
	}
}

func TestValidateRejectsWritesAndStars(t *testing.T) {
	t.Parallel()
	p := New(Options{AllowedTables: columns()})
	for _, sql := range []string{
		"INSERT INTO fleetmodule.fleets VALUES (1)",
		"SELECT * FROM fleetmodule.fleets",
		"SELECT a.* FROM fleetmodule.fleets a",
		"SELECT fleetid FROM fleetmodule.fleets; DROP TABLE fleetmodule.fleets",
		"SELECT fleetid FROM other.fleets",
		"SELECT fleetid FROM fleetmodule.fleets /* comment */ ; DELETE FROM fleetmodule.fleets",
		"SELECT fleetid FROM fleetmodule.fleets --\nDELETE FROM fleetmodule.fleets",
	} {
		if err := p.Validate(sql); err == nil {
			t.Fatalf("expected rejection for %s", sql)
		}
	}
	if err := p.Validate("SELECT fleetid FROM fleetmodule.fleets WHERE fleetid = 'f1'"); err != nil {
		t.Fatal(err)
	}
	if err := p.Validate("SELECT /* hint */ fleetid FROM fleetmodule.fleets"); err != nil {
		t.Fatal(err)
	}
}

func TestApplyScopeWrapsAndDenies(t *testing.T) {
	t.Parallel()
	bindings := map[string]TableBinding{
		"fleetmodule.fleets": {Column: "fleetid", FleetIDs: map[string]struct{}{"f1": {}}},
	}
	sql, err := ApplyScope("SELECT fleetid FROM fleetmodule.fleets WHERE fleetid = 'f1'", bindings, columns())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, `SELECT * FROM "fleetmodule"."fleets"`) || !strings.Contains(sql, `"fleetid" IN`) || !strings.Contains(sql, "'f1'") {
		t.Fatalf("sql = %s", sql)
	}
	if _, err := ApplyScope("SELECT fleetid FROM fleetmodule.fleets WHERE fleetid = 'other'", bindings, columns()); err == nil {
		t.Fatal("expected unauthorized fleet")
	}
	if _, err := ApplyScope("SELECT fleetid FROM fleetmodule.fleets", map[string]TableBinding{
		"fleetmodule.fleets": {Column: "fleetid", FleetIDs: map[string]struct{}{}},
	}, columns()); err == nil {
		t.Fatal("expected empty scope denial")
	}
}

func TestApplyScopeDerivedMixedFleetAndVehicle(t *testing.T) {
	t.Parallel()
	bindings := map[string]TableBinding{
		"geofencemodule.geofencerule": {
			Column:     "ruleid",
			FleetIDs:   map[string]struct{}{"f1": {}},
			VehicleIDs: map[string]struct{}{"v9": {}},
			Derive: []DeriveSource{
				{Table: "geofencemodule.geofenceruleownerfleet", PickColumn: "ruleid", ScopeColumn: "fleetid"},
				{Table: "geofencemodule.geofencerulefleet", PickColumn: "ruleid", ScopeColumn: "fleetid"},
				{Table: "geofencemodule.geofencerulevehicle", PickColumn: "ruleid", ScopeColumn: "vehicleid"},
			},
		},
	}
	sql, err := ApplyScope("SELECT ruleid FROM geofencemodule.geofencerule", bindings, columns())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, `"ruleid" IN`) ||
		!strings.Contains(sql, `"geofenceruleownerfleet" WHERE "fleetid" IN ('f1')`) ||
		!strings.Contains(sql, `"geofencerulevehicle" WHERE "vehicleid" IN ('v9')`) ||
		!strings.Contains(sql, `UNION`) {
		t.Fatalf("sql = %s", sql)
	}
	if strings.Contains(sql, `FROM "geofencemodule"."geofencerule" WHERE "fleetid"`) {
		t.Fatalf("must not scope geofencerule by fleetid column: %s", sql)
	}

	fleetOnly := bindings["geofencemodule.geofencerule"]
	fleetOnly.VehicleIDs = nil
	bindings["geofencemodule.geofencerule"] = fleetOnly
	sql, err = ApplyScope("SELECT ruleid FROM geofencemodule.geofencerule", bindings, columns())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sql, "geofencerulevehicle") || !strings.Contains(sql, "geofenceruleownerfleet") {
		t.Fatalf("empty vehicle set should drop vehicle arms: %s", sql)
	}

	empty := map[string]TableBinding{
		"geofencemodule.geofencerule": {
			Column: "ruleid",
			Derive: []DeriveSource{
				{Table: "geofencemodule.geofenceruleownerfleet", PickColumn: "ruleid", ScopeColumn: "fleetid"},
				{Table: "geofencemodule.geofencerulevehicle", PickColumn: "ruleid", ScopeColumn: "vehicleid"},
			},
		},
	}
	if _, err := ApplyScope("SELECT ruleid FROM geofencemodule.geofencerule", empty, columns()); err == nil {
		t.Fatal("expected denial when both ID sets empty")
	}
}

func TestApplyScopeAlertDerived(t *testing.T) {
	t.Parallel()
	bindings := map[string]TableBinding{
		"alertmodule.alert": {
			Column:   "alertid",
			FleetIDs: map[string]struct{}{"f1": {}},
			Derive: []DeriveSource{
				{Table: "alertmodule.alertownerfleet", PickColumn: "alertid", ScopeColumn: "fleetid"},
				{Table: "alertmodule.vehicle_alerts", PickColumn: "alertid", ScopeColumn: "vehicleid"},
			},
		},
	}
	sql, err := ApplyScope("SELECT alertid FROM alertmodule.alert", bindings, columns())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "alertownerfleet") || strings.Contains(sql, "vehicle_alerts") {
		t.Fatalf("expected only fleet arms when vehicles empty: %s", sql)
	}
}
