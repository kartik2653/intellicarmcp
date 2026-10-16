package authz

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseScopeUnionsVehiclesAndKeepsDomainsSeparate(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"err": null,
		"data": {"fleets": [
			{"fleetid":"parent","permids":["fleet.trip_insights.view","fleet.alerts.view"],"vehicleids":["v1"]},
			{"fleetid":"child","permids":["fleet.geofence.view","fleet.charge_insights.view"],"vehicleids":["v1","v2"]},
			{"fleetid":"empty","permids":["fleet.alerts.view"],"vehicleids":[]}
		]},
		"msg": ""
	}`)
	scope, err := ParseScope(body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(scope.TripFleetIDs, ",") != "parent" {
		t.Fatalf("trips = %#v", scope.TripFleetIDs)
	}
	if strings.Join(scope.ChargingFleetIDs, ",") != "child" {
		t.Fatalf("charging = %#v", scope.ChargingFleetIDs)
	}
	if len(scope.AlertVehicleIDs) != 1 || scope.AlertVehicleIDs[0] != "v1" {
		t.Fatalf("alerts = %#v", scope.AlertVehicleIDs)
	}
	if len(scope.GeofenceVehicleIDs) != 2 {
		t.Fatalf("geofence = %#v", scope.GeofenceVehicleIDs)
	}
}

func TestParseScopeFailClosed(t *testing.T) {
	t.Parallel()
	cases := []string{
		`{"err":"nope","data":{"fleets":[]}}`,
		`{"err":null}`,
		`{"err":null,"data":{"fleets":[{"fleetid":"bad id","permids":[],"vehicleids":[]}]}}`,
		`not-json`,
	}
	for _, body := range cases {
		if _, err := ParseScope([]byte(body)); err == nil {
			t.Fatalf("expected error for %s", body)
		}
	}
}

func TestFetchForwardsBearerAndFailClosed(t *testing.T) {
	t.Parallel()
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		if r.URL.Path != "/api/v1/permissions/userfleetvehicles" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(server.Close)
	client := &Client{BaseURL: server.URL, HTTPClient: server.Client()}
	if _, err := client.Fetch(context.Background(), "user-token"); err == nil {
		t.Fatal("expected HTTP failure")
	}
	if got != "Bearer user-token" {
		t.Fatalf("authorization = %q", got)
	}
}

func TestBearerTokenRequiresExactlyOne(t *testing.T) {
	t.Parallel()
	header := http.Header{}
	if _, err := BearerToken(header); err == nil {
		t.Fatal("expected missing token")
	}
	header.Add("Authorization", "Bearer one")
	header.Add("Authorization", "Bearer two")
	if _, err := BearerToken(header); err == nil {
		t.Fatal("expected duplicate token rejection")
	}
}
