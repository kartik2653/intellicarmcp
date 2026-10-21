package postgres

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed allowlist.json
var allowlistJSON []byte

type allowlistEntry struct {
	SchemaTable     string `json:"schema_table"`
	Scope           string `json:"scope"`
	TargetKey       string `json:"target_key"`
	ExcludeReason   *string `json:"exclude_reason"`
	PermissionsHint *string `json:"permissions_hint"`
	DeriveFrom      []struct {
		MappingTable string `json:"mapping_table"`
		PickColumn   string `json:"pick_column"`
		ScopeColumn  string `json:"scope_column"`
	} `json:"derive_from"`
	StatusOrEnabledColumns []string `json:"status_or_enabled_columns"`
}

var loadedAllowlist []tableSpec

func init() {
	specs, err := loadAllowlist(allowlistJSON)
	if err != nil {
		panic("postgres allowlist: " + err.Error())
	}
	loadedAllowlist = specs
}

func loadAllowlist(raw []byte) ([]tableSpec, error) {
	var entries []allowlistEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("parse allowlist.json: %w", err)
	}
	out := make([]tableSpec, 0, len(entries))
	seen := map[string]struct{}{}
	for _, entry := range entries {
		if entry.ExcludeReason != nil && strings.TrimSpace(*entry.ExcludeReason) != "" {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(entry.SchemaTable))
		if name == "" {
			return nil, fmt.Errorf("allowlist entry missing schema_table")
		}
		if _, ok := seen[name]; ok {
			return nil, fmt.Errorf("duplicate allowlist table %q", name)
		}
		spec, err := entryToSpec(entry)
		if err != nil {
			return nil, err
		}
		seen[name] = struct{}{}
		out = append(out, spec)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("allowlist.json produced no exposable tables")
	}
	return out, nil
}

func entryToSpec(entry allowlistEntry) (tableSpec, error) {
	name := strings.ToLower(strings.TrimSpace(entry.SchemaTable))
	perms := permissionsFor(entry)
	useWhen := useWhenFor(entry)
	switch strings.ToLower(strings.TrimSpace(entry.Scope)) {
	case "fleetid", "vehicleid":
		col := strings.ToLower(strings.TrimSpace(entry.Scope))
		return tableSpec{name: name, column: col, perms: perms, useWhen: useWhen}, nil
	case "derived":
		target := strings.ToLower(strings.TrimSpace(entry.TargetKey))
		if target == "" {
			return tableSpec{}, fmt.Errorf("%s: derived scope missing target_key", name)
		}
		if len(entry.DeriveFrom) == 0 {
			return tableSpec{}, fmt.Errorf("%s: derived scope missing derive_from", name)
		}
		derive := make([]deriveSpec, 0, len(entry.DeriveFrom))
		for _, d := range entry.DeriveFrom {
			mt := strings.ToLower(strings.TrimSpace(d.MappingTable))
			pick := strings.ToLower(strings.TrimSpace(d.PickColumn))
			scope := strings.ToLower(strings.TrimSpace(d.ScopeColumn))
			if mt == "" || pick == "" || (scope != "fleetid" && scope != "vehicleid") {
				return tableSpec{}, fmt.Errorf("%s: invalid derive_from entry", name)
			}
			derive = append(derive, deriveSpec{table: mt, pick: pick, scope: scope})
		}
		return tableSpec{name: name, column: target, perms: perms, useWhen: useWhen, derive: derive}, nil
	case "unscoped", "":
		return tableSpec{}, fmt.Errorf("%s: unscoped entries must not be loaded", name)
	default:
		return tableSpec{}, fmt.Errorf("%s: unknown scope %q", name, entry.Scope)
	}
}

func permissionsFor(entry allowlistEntry) []string {
	if entry.PermissionsHint != nil && strings.TrimSpace(*entry.PermissionsHint) != "" {
		hint := strings.TrimSpace(*entry.PermissionsHint)
		if hint == "fleet.vehicle_share.share" {
			return []string{"fleet.vehicle_share.share", "account.vehicle_share.*"}
		}
		switch strings.ToLower(entry.SchemaTable) {
		case "fleetmodule.vehicle", "fleetmodule.auditvehicles", "fleetmodule.vehicle_device", "fleetmodule.vehicle_device_history":
			return []string{"account.vehicle.view", "fleet.fleet_management.view"}
		}
		return []string{hint}
	}
	name := strings.ToLower(entry.SchemaTable)
	switch {
	case strings.HasPrefix(name, "alertmodule."):
		return []string{"fleet.alerts.view"}
	case strings.HasPrefix(name, "geofencemodule."):
		return []string{"fleet.geofence.view"}
	case strings.HasPrefix(name, "commandmodule."):
		return []string{"fleet.command_center.view"}
	case strings.HasPrefix(name, "devicemodule."), strings.HasPrefix(name, "modelmodule."):
		return []string{"account.vehicle.view", "fleet.fleet_management.view"}
	case strings.HasPrefix(name, "mobileusermodule."):
		return []string{"account.mobileuser.view"}
	case strings.HasPrefix(name, "fmsws.livetrack"):
		return []string{"fleet.live_tracking.view"}
	case strings.HasPrefix(name, "fmsws."):
		return []string{"fleet.fleet_management.view"}
	default:
		return []string{"fleet.fleet_management.view"}
	}
}

func useWhenFor(entry allowlistEntry) []string {
	name := strings.ToLower(entry.SchemaTable)
	switch name {
	case "fleetmodule.fleets":
		return []string{"Fleet list"}
	case "fleetmodule.vehicle":
		return []string{"Vehicle records"}
	case "fleetmodule.fleet_vehicle":
		return []string{"Fleet vehicle membership"}
	case "geofencemodule.geofencerule":
		return []string{"Geofence rule definitions including enabled/disabled state"}
	case "geofencemodule.geofence":
		return []string{"Geofence polygon/region definitions including enabled/disabled state"}
	case "alertmodule.alert":
		return []string{"Alert definitions including enabled/disabled state"}
	case "commandmodule.sentcommands":
		return []string{"Commands sent to vehicles"}
	default:
		if len(entry.StatusOrEnabledColumns) > 0 {
			return []string{"Includes status/enabled columns: " + strings.Join(entry.StatusOrEnabledColumns, ", ")}
		}
		return nil
	}
}
