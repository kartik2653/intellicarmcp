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
	ScopeVia    []string     `json:"scope_via,omitempty"`
	Permissions []string     `json:"permissions"`
	Columns     []ColumnMeta `json:"columns,omitempty"`
	UseWhen     []string     `json:"use_when,omitempty"`
}

type deriveSpec struct {
	table string // mapping schema.table
	pick  string // column selected from mapping table
	scope string // fleetid or vehicleid on mapping table
}

type tableSpec struct {
	name    string
	column  string // column on this table used in the scoped WHERE
	perms   []string
	useWhen []string
	derive  []deriveSpec
}

func phase1Tables() []tableSpec {
	return loadedAllowlist
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
			"Query only schema-qualified approved tables listed here (from schema allowlist.json).",
			"Table description comes from COMMENT ON TABLE when metadata is hydrated; it may be empty.",
			"Columns list name, data_type, and nullability from information_schema when hydrated. There are no column comments and generally no foreign keys.",
			"Infer joins on demand from descriptions, column names/types, and business meaning. Never claim a verified foreign-key relationship when none exists.",
			"All columns on approved tables are available in queries; name them explicitly (SELECT * is rejected).",
			"PostgreSQL holds relational and configuration data. Trip, charge, geofence event, and alert event facts live in ClickHouse.",
			"Do not join PostgreSQL and ClickHouse in one query. Say so if a question needs both.",
			"Prefer aggregates or a SQL LIMIT when results may be large.",
			"Tables are scoped by fleetid, vehicleid, or a derived key (see scope_column and scope_via). Derived keys use UNION of mapping-table selects over authorized fleet and/or vehicle IDs.",
			"Unscoped, secret, staging, userid-only, and permission-catalog tables are not in this allowlist.",
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
		for _, d := range table.derive {
			entry.ScopeVia = append(entry.ScopeVia, d.table)
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
Infer joins from table descriptions and column metadata; foreign keys are generally absent. Server-side authorization rewrites fleetid, vehicleid, or derived keys (for example ruleid, alertid, geofenceid) via mapping tables listed in scope_via.
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
		fleetIDs := idSet(scope.FleetsForPerms(table.perms...))
		vehicleIDs := idSet(scope.VehiclesForPerms(table.perms...))
		binding := pgpolicy.TableBinding{
			Column:     table.column,
			FleetIDs:   fleetIDs,
			VehicleIDs: vehicleIDs,
			Perms:      append([]string(nil), table.perms...),
		}
		for _, d := range table.derive {
			binding.Derive = append(binding.Derive, pgpolicy.DeriveSource{
				Table:       d.table,
				PickColumn:  d.pick,
				ScopeColumn: d.scope,
			})
		}
		out[table.name] = binding
	}
	return out
}

func idSet(ids []string) map[string]struct{} {
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out
}
