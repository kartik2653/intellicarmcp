package authz

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

const (
	PermAlerts   = "fleet.alerts.view"
	PermTrips    = "fleet.trip_insights.view"
	PermGeofence = "fleet.geofence.view"
	PermCharging = "fleet.charge_insights.view"

	DomainAlerts   = "alerts"
	DomainTrips    = "trips"
	DomainGeofence = "geofence"
	DomainCharging = "charging"
)

// Scope is the per-user authorization result. Empty slices mean no access.
type Scope struct {
	AlertVehicleIDs    []string
	TripFleetIDs       []string
	GeofenceVehicleIDs []string
	ChargingFleetIDs   []string
	FleetsByPerm       map[string][]string
	VehiclesByPerm     map[string][]string
}

type fleetPayload struct {
	FleetID    string   `json:"fleetid"`
	PermIDs    []string `json:"permids"`
	VehicleIDs []string `json:"vehicleids"`
}

type apiPayload struct {
	Err  json.RawMessage `json:"err"`
	Data struct {
		Fleets []fleetPayload `json:"fleets"`
	} `json:"data"`
}

// ParseScope builds domain scopes from the permissions API body.
// It does not walk a fleet hierarchy. The endpoint has already done that.
func ParseScope(body []byte) (Scope, error) {
	var payload apiPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return Scope{}, fmt.Errorf("authorization response was not valid JSON")
	}
	if !nullOrEmpty(payload.Err) {
		return Scope{}, fmt.Errorf("authorization response reported an error")
	}
	if payload.Data.Fleets == nil {
		return Scope{}, fmt.Errorf("authorization response is missing fleets")
	}
	fleetsByPerm := map[string]map[string]struct{}{}
	vehiclesByPerm := map[string]map[string]struct{}{}
	for _, fleet := range payload.Data.Fleets {
		fleetID, err := validID(fleet.FleetID)
		if err != nil {
			return Scope{}, fmt.Errorf("fleetid: %w", err)
		}
		var vehicles []string
		for _, vehicle := range fleet.VehicleIDs {
			id, err := validID(vehicle)
			if err != nil {
				return Scope{}, fmt.Errorf("vehicleid: %w", err)
			}
			vehicles = append(vehicles, id)
		}
		for _, perm := range fleet.PermIDs {
			perm = strings.TrimSpace(perm)
			if perm == "" {
				continue
			}
			if fleetsByPerm[perm] == nil {
				fleetsByPerm[perm] = map[string]struct{}{}
			}
			fleetsByPerm[perm][fleetID] = struct{}{}
			if vehiclesByPerm[perm] == nil {
				vehiclesByPerm[perm] = map[string]struct{}{}
			}
			for _, id := range vehicles {
				vehiclesByPerm[perm][id] = struct{}{}
			}
		}
	}
	scope := Scope{
		FleetsByPerm:   mapKeys(fleetsByPerm),
		VehiclesByPerm: mapKeys(vehiclesByPerm),
	}
	scope.AlertVehicleIDs = scope.VehiclesForPerms(PermAlerts)
	scope.GeofenceVehicleIDs = scope.VehiclesForPerms(PermGeofence)
	scope.TripFleetIDs = scope.FleetsForPerms(PermTrips)
	scope.ChargingFleetIDs = scope.FleetsForPerms(PermCharging)
	return scope, nil
}

func (s Scope) IDs(domain string) []string {
	switch domain {
	case DomainAlerts:
		return s.AlertVehicleIDs
	case DomainTrips:
		return s.TripFleetIDs
	case DomainGeofence:
		return s.GeofenceVehicleIDs
	case DomainCharging:
		return s.ChargingFleetIDs
	default:
		return nil
	}
}

// FleetsForPerms unions fleet IDs for any exact or prefix permission match.
func (s Scope) FleetsForPerms(perms ...string) []string {
	return unionIDs(s.FleetsByPerm, perms...)
}

// VehiclesForPerms unions vehicle IDs for any exact or prefix permission match.
func (s Scope) VehiclesForPerms(perms ...string) []string {
	return unionIDs(s.VehiclesByPerm, perms...)
}

func unionIDs(byPerm map[string][]string, perms ...string) []string {
	if len(byPerm) == 0 {
		return nil
	}
	out := map[string]struct{}{}
	for key, ids := range byPerm {
		if !permMatches(key, perms...) {
			continue
		}
		for _, id := range ids {
			out[id] = struct{}{}
		}
	}
	return keys(out)
}

func permMatches(have string, wanted ...string) bool {
	for _, want := range wanted {
		want = strings.TrimSpace(want)
		if want == "" {
			continue
		}
		if strings.HasSuffix(want, ".*") {
			prefix := strings.TrimSuffix(want, ".*")
			if have == prefix || strings.HasPrefix(have, prefix+".") {
				return true
			}
			continue
		}
		if have == want {
			return true
		}
	}
	return false
}

func mapKeys(sets map[string]map[string]struct{}) map[string][]string {
	out := make(map[string][]string, len(sets))
	for key, set := range sets {
		out[key] = keys(set)
	}
	return out
}

func validID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return "", fmt.Errorf("invalid identifier")
	}
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			continue
		}
		return "", fmt.Errorf("invalid identifier")
	}
	return value, nil
}

func nullOrEmpty(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed == "" || trimmed == "null"
}

func keys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	return out
}
