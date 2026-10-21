package postgres

import (
	"strings"
	"testing"
)

func columns() map[string][]string {
	return map[string][]string{
		"fleetmodule.fleets":  {"fleetid"},
		"fleetmodule.vehicle": {"vehicleid", "fleetid"},
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
		"fleetmodule.fleets": {Column: "fleetid", IDs: map[string]struct{}{"f1": {}}},
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
		"fleetmodule.fleets": {Column: "fleetid", IDs: map[string]struct{}{}},
	}, columns()); err == nil {
		t.Fatal("expected empty scope denial")
	}
}
