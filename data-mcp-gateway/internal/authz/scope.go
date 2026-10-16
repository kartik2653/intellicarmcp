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
	alerts := map[string]struct{}{}
	trips := map[string]struct{}{}
	geofence := map[string]struct{}{}
	charging := map[string]struct{}{}
	for _, fleet := range payload.Data.Fleets {
		fleetID, err := validID(fleet.FleetID)
		if err != nil {
			return Scope{}, fmt.Errorf("fleetid: %w", err)
		}
		perms := map[string]struct{}{}
		for _, perm := range fleet.PermIDs {
			perms[strings.TrimSpace(perm)] = struct{}{}
		}
		var vehicles []string
		for _, vehicle := range fleet.VehicleIDs {
			id, err := validID(vehicle)
			if err != nil {
				return Scope{}, fmt.Errorf("vehicleid: %w", err)
			}
			vehicles = append(vehicles, id)
		}
		if _, ok := perms[PermAlerts]; ok {
			for _, id := range vehicles {
				alerts[id] = struct{}{}
			}
		}
		if _, ok := perms[PermGeofence]; ok {
			for _, id := range vehicles {
				geofence[id] = struct{}{}
			}
		}
		if _, ok := perms[PermTrips]; ok {
			trips[fleetID] = struct{}{}
		}
		if _, ok := perms[PermCharging]; ok {
			charging[fleetID] = struct{}{}
		}
	}
	return Scope{
		AlertVehicleIDs:    keys(alerts),
		TripFleetIDs:       keys(trips),
		GeofenceVehicleIDs: keys(geofence),
		ChargingFleetIDs:   keys(charging),
	}, nil
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
