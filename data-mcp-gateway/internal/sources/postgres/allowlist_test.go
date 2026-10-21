package postgres

import (
	"strings"
	"testing"
	"time"
)

func TestAllowlistLoadsExposableTables(t *testing.T) {
	t.Parallel()
	tables := phase1Tables()
	if len(tables) < 40 {
		t.Fatalf("expected expanded allowlist, got %d", len(tables))
	}
	byName := map[string]tableSpec{}
	for _, table := range tables {
		byName[table.name] = table
	}
	for _, forbidden := range []string{
		"fmsaccount.usersecret",
		"alertmodule.permissions",
		"geofencemodule.uniquekey",
		"taskmodule.bulk_vehicle_staging",
		"chargemodule.chargecheckpoints",
	} {
		if _, ok := byName[forbidden]; ok {
			t.Fatalf("excluded table present: %s", forbidden)
		}
	}
	rule, ok := byName["geofencemodule.geofencerule"]
	if !ok || rule.column != "ruleid" || len(rule.derive) != 3 {
		t.Fatalf("geofencerule = %#v", rule)
	}
	alert, ok := byName["alertmodule.alert"]
	if !ok || alert.column != "alertid" || len(alert.derive) != 3 {
		t.Fatalf("alert = %#v", alert)
	}
	fv, ok := byName["fleetmodule.fleet_vehicle"]
	if !ok || fv.column != "vehicleid" {
		t.Fatalf("fleet_vehicle should scope vehicleid: %#v", fv)
	}
}

func TestLoadAllowlistRejectsUnscopedOnly(t *testing.T) {
	t.Parallel()
	raw := []byte(`[
	  {"schema_table":"fmsaccount.usersecret","scope":"unscoped","exclude_reason":"secrets","permissions_hint":null},
	  {"schema_table":"fleetmodule.fleets","scope":"fleetid","exclude_reason":null,"permissions_hint":"fleet.fleet_management.view"}
	]`)
	specs, err := loadAllowlist(raw)
	if err != nil || len(specs) != 1 || specs[0].name != "fleetmodule.fleets" {
		t.Fatalf("specs=%#v err=%v", specs, err)
	}
}

func TestCatalogIncludesDerivedScopeVia(t *testing.T) {
	t.Parallel()
	raw := CatalogJSON("region_india", nil, time.Time{})
	if !strings.Contains(raw, `"name":"alertmodule.alert"`) || !strings.Contains(raw, "alertownerfleet") {
		t.Fatalf("catalog missing alert derive: %s", raw)
	}
	if strings.Contains(raw, "usersecret") {
		t.Fatal("catalog must not include secrets")
	}
}
