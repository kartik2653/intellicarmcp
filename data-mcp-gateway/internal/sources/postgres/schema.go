package postgres

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Intellicars/intellicarmcp/data-mcp-gateway/internal/authz"
	pgpolicy "github.com/Intellicars/intellicarmcp/data-mcp-gateway/internal/policy/postgres"
)

const toolSchemaCatalog = "get_postgres_schema_catalog"

// Catalog is the model-facing description of queryable PostgreSQL tables.
type Catalog struct {
	Database   string   `json:"database"`
	Tables     []Table  `json:"tables"`
	Rules      []string `json:"query_rules"`
	FetchedAt  string   `json:"metadata_fetched_at,omitempty"`
	MetadataOK bool     `json:"metadata_hydrated"`
}

// Table is one approved PostgreSQL relation.
type Table struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	ScopeColumn string       `json:"scope_column"`
	Permissions []string     `json:"permissions"`
	Columns     []ColumnMeta `json:"columns,omitempty"`
	UseWhen     []string     `json:"use_when,omitempty"`
}

type tableSpec struct {
	name    string
	column  string // must be fleetid or vehicleid
	perms   []string
	useWhen []string
}

func phase1Tables() []tableSpec {
	// Only fleetid/vehicleid-scoped tables. Tables without those columns (e.g. geofencemodule.geofencerule)
	// are intentionally omitted until a safe ID derivation exists.
	return []tableSpec{
		{"fleetmodule.fleets", "fleetid", []string{"fleet.fleet_management.view"}, []string{"Fleet list"}},
		{"fleetmodule.auditfleets", "fleetid", []string{"fleet.fleet_management.view"}, nil},
		{"fleetmodule.userfleet", "fleetid", []string{"fleet.fleet_permissions.view"}, nil},
		{"fleetmodule.vehicle", "vehicleid", []string{"account.vehicle.view", "fleet.fleet_management.view"}, []string{"Vehicle records"}},
		{"fleetmodule.auditvehicles", "vehicleid", []string{"account.vehicle.view"}, nil},
		{"fleetmodule.fleet_vehicle", "fleetid", []string{"fleet.fleet_management.view"}, []string{"Fleet vehicle membership"}},
		{"fleetmodule.fleet_vehicle_history", "fleetid", []string{"fleet.fleet_management.view"}, nil},
		{"fleetmodule.vehicle_device", "vehicleid", []string{"account.vehicle.view", "fleet.fleet_management.view"}, nil},
		{"fleetmodule.userparams", "fleetid", []string{"fleet.fleet.params.view"}, nil},
		{"fleetmodule.vehicle_share", "vehicleid", []string{"fleet.vehicle_share.share", "account.vehicle_share.*"}, nil},
		{"fleetmodule.vehicle_share_inbound", "vehicleid", []string{"account.vehicle_share.*"}, nil},
		{"fleetmodule.auditvehicleshare", "vehicleid", []string{"account.vehicle_share.*"}, nil},
		{"fleetmodule.vehicle_share_history", "vehicleid", []string{"account.vehicle_share.*"}, nil},
		{"alertmodule.alertownerfleet", "fleetid", []string{"fleet.alerts.view"}, nil},
		{"alertmodule.auditalertownerfleet", "fleetid", []string{"fleet.alerts.view"}, nil},
		{"alertmodule.fleet_alerts", "fleetid", []string{"fleet.alerts.view"}, nil},
		{"alertmodule.auditfleetalerts", "fleetid", []string{"fleet.alerts.view"}, nil},
		{"alertmodule.vehicle_alerts", "vehicleid", []string{"fleet.alerts.view"}, nil},
		{"alertmodule.auditvehiclealerts", "vehicleid", []string{"fleet.alerts.view"}, nil},
		{"geofencemodule.geofenceownerfleet", "fleetid", []string{"fleet.geofence.view"}, nil},
		{"geofencemodule.auditgeofenceownerfleet", "fleetid", []string{"fleet.geofence.view"}, nil},
		{"geofencemodule.geofenceruleownerfleet", "fleetid", []string{"fleet.geofence.view"}, nil},
		{"geofencemodule.auditruleownerfleet", "fleetid", []string{"fleet.geofence.view"}, nil},
		{"geofencemodule.geofencerulevehicle", "vehicleid", []string{"fleet.geofence.view"}, nil},
		{"geofencemodule.auditrulevehicle", "vehicleid", []string{"fleet.geofence.view"}, nil},
		{"geofencemodule.geofencerulefleet", "fleetid", []string{"fleet.geofence.view"}, nil},
		{"geofencemodule.geofencerule", "fleetid", []string{"fleet.geofence.view", "fleet.geofence.view"}, nil},
		{"geofencemodule.auditrulefleet", "fleetid", []string{"fleet.geofence.view"}, nil},
		{"commandmodule.sentcommands", "vehicleid", []string{"fleet.command_center.view"}, []string{"Commands sent to vehicles"}},
		{"fmsws.livetracklinks", "vehicleid", []string{"fleet.live_tracking.view"}, nil},
		{"mobileusermodule.usertovehiclemap", "vehicleid", []string{"account.mobileuser.view"}, nil},
		{"mobileusermodule.auditusertovehiclemap", "vehicleid", []string{"account.mobileuser.view"}, nil},
	}
}

// AllowedTableColumns returns approved schema.table names for policy (column lists unused; scope wraps use SELECT *).
func AllowedTableColumns() map[string][]string {
	out := map[string][]string{}
	for _, table := range phase1Tables() {
		out[table.name] = nil
	}
	return out
}

// QueryTables returns approved schema.table names.
func QueryTables() []string {
	tables := phase1Tables()
	out := make([]string, 0, len(tables))
	for _, table := range tables {
		out = append(out, table.name)
	}
	return out
}

// CatalogJSON builds the catalog from the allowlist merged with optional live metadata.
func CatalogJSON(database string, meta map[string]TableMeta, fetchedAt time.Time) string {
	catalog := Catalog{
		Database:   database,
		MetadataOK: len(meta) > 0,
		Rules: []string{
			"Query only schema-qualified approved tables listed here.",
			"Table description comes from COMMENT ON TABLE when metadata is hydrated; it may be empty.",
			"Columns list name, data_type, and nullability from information_schema when hydrated. There are no column comments and generally no foreign keys.",
			"Infer joins on demand from descriptions, column names/types, and business meaning. Never claim a verified foreign-key relationship when none exists.",
			"All columns on approved tables are available in queries; name them explicitly (SELECT * is rejected).",
			"PostgreSQL holds relational and configuration data. Trip, charge, geofence event, and alert event facts live in ClickHouse.",
			"Do not join PostgreSQL and ClickHouse in one query. Say so if a question needs both.",
			"Prefer aggregates or a SQL LIMIT when results may be large.",
			"Only tables with a fleetid or vehicleid scope column are queryable. Rule definition tables without those columns (for example geofencemodule.geofencerule) are not exposed.",
			"Unmapped, secret, staging, userid-only, and permission-catalog tables are not queryable.",
		},
	}
	if !fetchedAt.IsZero() {
		catalog.FetchedAt = fetchedAt.UTC().Format(time.RFC3339)
	}
	for _, table := range phase1Tables() {
		entry := Table{
			Name:        table.name,
			ScopeColumn: table.column,
			Permissions: append([]string(nil), table.perms...),
			UseWhen:     append([]string(nil), table.useWhen...),
		}
		if live, ok := meta[table.name]; ok {
			entry.Description = live.Description
			entry.Columns = append([]ColumnMeta(nil), live.Columns...)
		}
		catalog.Tables = append(catalog.Tables, entry)
	}
	raw, err := json.Marshal(catalog)
	if err != nil {
		return `{"database":"","tables":[],"query_rules":[],"metadata_hydrated":false}`
	}
	return string(raw)
}

// BriefInstructions guides tool selection for PostgreSQL.
func BriefInstructions(database string) string {
	return strings.TrimSpace(fmt.Sprintf(`
PostgreSQL database %s holds relational and configuration data (fleets, vehicles, alert/geofence assignments, command history). Telemetry events and trip/charge report facts are in ClickHouse — use run_query for those. Do not invent a cross-database join; say the limitation instead.
Call get_postgres_schema_catalog for approved PostgreSQL tables (includes COMMENT ON TABLE descriptions and column types when hydrated). Query only schema-qualified names from that catalog with postgres_query.
Infer joins from table descriptions and column metadata; foreign keys are generally absent. Server-side authorization rewrites fleetid or vehicleid scope. Tables without those scope columns are not available.
`, database))
}

// LimitInstructions states configured safety boundaries.
func LimitInstructions(maxRows, maxBytes int, timeout time.Duration) string {
	seconds := int(timeout.Seconds())
	if timeout > 0 && seconds == 0 {
		seconds = 1
	}
	return fmt.Sprintf("PostgreSQL limits: MAX_RESULT_ROWS=%d, MAX_RESPONSE_BYTES=%d, QUERY_TIMEOUT_SECONDS=%d. A result that exceeds them is not returned and is not a partial report.", maxRows, maxBytes, seconds)
}

func bindingsFor(scope authz.Scope) map[string]pgpolicy.TableBinding {
	out := map[string]pgpolicy.TableBinding{}
	for _, table := range phase1Tables() {
		ids := map[string]struct{}{}
		var list []string
		switch table.column {
		case "fleetid":
			list = scope.FleetsForPerms(table.perms...)
		case "vehicleid":
			list = scope.VehiclesForPerms(table.perms...)
		default:
			// Fail closed: unknown scope columns never authorize rows.
			out[table.name] = pgpolicy.TableBinding{Column: table.column, IDs: ids, Perms: append([]string(nil), table.perms...)}
			continue
		}
		for _, id := range list {
			ids[id] = struct{}{}
		}
		out[table.name] = pgpolicy.TableBinding{Column: table.column, IDs: ids, Perms: append([]string(nil), table.perms...)}
	}
	return out
}
