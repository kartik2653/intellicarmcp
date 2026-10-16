package clickhouse

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Intellicars/intellicarmcp/data-mcp-gateway/internal/authz"
	"github.com/Intellicars/intellicarmcp/data-mcp-gateway/internal/policy"
)

const toolSchemaCatalog = "get_schema_catalog"

// Catalog is the static model-facing description of queryable ClickHouse tables.
type Catalog struct {
	Time       TimeRules       `json:"time"`
	Domains    []Domain        `json:"domains"`
	Rules      []SelectionRule `json:"selection_rules"`
	Relations  []string        `json:"relationships"`
	Caveats    []string        `json:"caveats"`
	QueryRules []string        `json:"query_rules"`
}

// TimeRules documents epoch bucket definitions.
type TimeRules struct {
	DayBucket      string `json:"daybucket"`
	WeekBucket     string `json:"weekbucket"`
	MonthBucket    string `json:"monthbucket"`
	MonthMeaning   string `json:"monthbucket_meaning"`
	RollupUTCTime  string `json:"rollup_utctime"`
	Timezone       string `json:"timezone"`
	CalendarMonths string `json:"calendar_months"`
}

// Domain groups related fact and rollup tables.
type Domain struct {
	Name   string  `json:"name"`
	Tables []Table `json:"tables"`
}

// Table is one normal query-facing table.
type Table struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Description string   `json:"description"`
	Grain       string   `json:"grain,omitempty"`
	Identity    string   `json:"identity,omitempty"`
	OrderingKey string   `json:"ordering_key,omitempty"`
	UseWhen     []string `json:"use_when,omitempty"`
	Columns     []Column `json:"columns"`
}

// Column describes one known column. SQLType is set only when confirmed.
type Column struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Unit        string `json:"unit,omitempty"`
	SQLType     string `json:"sql_type,omitempty"`
	TypeStatus  string `json:"type_status"`
}

// SelectionRule maps a documented question to the preferred table.
type SelectionRule struct {
	Question string `json:"question"`
	Table    string `json:"table"`
	Note     string `json:"note,omitempty"`
}

const (
	kindFact   = "fact"
	kindRollup = "rollup"
	typeKnown  = "confirmed"
	typeOpen   = "unconfirmed"
)

func staticCatalog() Catalog {
	return Catalog{
		Time: TimeRules{
			DayBucket:      "daybucket = starttime_ms / 86400000",
			WeekBucket:     "weekbucket = starttime_ms / 604800000",
			MonthBucket:    "monthbucket = starttime_ms / 2592000000",
			MonthMeaning:   "monthbucket is a fixed 30-day epoch interval, not a calendar month",
			RollupUTCTime:  "In rollup tables, utctime is Unix milliseconds at noon UTC for that bucket",
			Timezone:       "Use UTC unless the user explicitly requests another timezone",
			CalendarMonths: "For calendar months, filter and group by the actual timestamp. Do not treat monthbucket as a calendar month",
		},
		Domains: []Domain{
			chargeDomain(),
			tripDomain(),
			geofenceDomain(),
			alertDomain(),
		},
		Rules:     selectionRules(),
		Relations: relationshipNotes(),
		Caveats:   caveatNotes(),
		QueryRules: []string{
			"Use only the table names in this catalog.",
			"Use fact tables for detailed records and exact timestamp boundaries.",
			"Use rollups for aggregate analytics when their grain fits the question.",
			"Combine rollup measures with sum(). Do not count rollup rows to get session or trip counts.",
			"For averages, divide the summed numerator by the summed denominator. Do not average rollup rows directly.",
			"Prefer one table. If two reports must be combined, aggregate each source first, then join on explicit keys and a defined time relationship.",
			"Do not invent JSON paths. Inspect a sample value before extracting fields from geofencedata, alertdata, or devdata.",
			"Do not use FINAL indiscriminately. Consider query cost and table semantics.",
		},
	}
}

func chargeDomain() Domain {
	session := []Column{
		col("daybucket", "Day bucket derived from the start timestamp in milliseconds divided by 86400000", "epoch days"),
		col("fleetid", "Fleet identifier copied at insertion time", ""),
		col("vehicleid", "Vehicle identifier", ""),
		col("chassisno", "Chassis number copied at insertion time", ""),
		col("regno", "Vehicle registration number copied at insertion time", ""),
		col("starttime", "Session start time", "Unix milliseconds"),
		col("endtime", "Session end time", "Unix milliseconds"),
		col("socgain", "State-of-charge gain", "percentage points"),
		col("duration", "Charging duration", "milliseconds"),
		col("completesession", "Whether the session was completed", ""),
		col("overchargesession", "Whether the session was classified as an overcharge", ""),
		col("createdat", "Creation or version timestamp where present", ""),
	}
	return Domain{
		Name: "charge",
		Tables: []Table{
			{
				Name: "chargereport", Kind: kindFact,
				Description: "Each row is a charging session.",
				Identity:    "(daybucket, fleetid, starttime, vehicleid). ReplacingMergeTree versioning may leave multiple versions visible until merges complete.",
				UseWhen:     []string{"Individual charging-session details", "Exact timestamp analysis of charging sessions"},
				Columns:     session,
			},
			{
				Name: "invalid_chargereport", Kind: kindFact,
				Description: "Invalid charge reports, including sessions with socgain <= 0. Use only when the user asks about invalid charging sessions. Do not mix these rows into normal charging statistics unless requested.",
				Columns:     session,
			},
			chargeRollup("chargereport_dailybucket", "daybucket"),
			chargeRollup("chargereport_weeklybucket", "weekbucket"),
			chargeRollup("chargereport_monthlybucket", "monthbucket"),
		},
	}
}

func chargeRollup(name, grain string) Table {
	return Table{
		Name: name, Kind: kindRollup, Grain: grain,
		Description: "Aggregate charging measures. sum() additive metrics. Do not count rows to obtain the number of sessions. The primary grain column is " + grain + ". Other bucket columns are known for this rollup family; confirm them from DDL before filtering on a non-primary bucket.",
		UseWhen:     []string{"Charging session count and total SOC gain", "Fleet or vehicle charging trends"},
		Columns: []Column{
			col("daybucket", "Day bucket", "epoch days"),
			col("weekbucket", "Week bucket", "epoch weeks"),
			col("monthbucket", "Fixed 30-day epoch interval, not a calendar month", "30-day epoch intervals"),
			col("utctime", "Noon UTC for the relevant bucket", "Unix milliseconds"),
			col("vehicleid", "Vehicle identifier", ""),
			col("fleetid", "Fleet identifier copied into the source report", ""),
			col("totalcharges", "Accumulated charge-session count", "sessions"),
			col("totalsocgain", "Accumulated SOC gain", "percentage points"),
			col("totalcompletesessions", "Accumulated completed-session count", "sessions"),
			col("totaloverchargesessions", "Accumulated overcharge-session count", "sessions"),
			col("totalchargeduration", "Accumulated charging duration", "milliseconds"),
		},
	}
}

func tripDomain() Domain {
	trip := []Column{
		col("daybucket", "Day bucket derived from the start timestamp in milliseconds divided by 86400000", "epoch days"),
		col("fleetid", "Fleet identifier copied at insertion time", ""),
		col("vehicleid", "Vehicle identifier", ""),
		col("chassisno", "Chassis number copied at insertion time", ""),
		col("regno", "Vehicle registration number copied at insertion time", ""),
		col("starttime", "Trip start time", "Unix milliseconds"),
		col("endtime", "Trip end time", "Unix milliseconds"),
		col("startodo", "Starting odometer reading", ""),
		col("endodo", "Ending odometer reading", ""),
		col("startsoc", "Starting state of charge", "percentage points"),
		col("endsoc", "Ending state of charge", "percentage points"),
		col("socconsumed", "SOC consumption", "percentage points"),
		col("distance", "Trip distance", "kilometres"),
		col("duration", "Trip duration", "milliseconds"),
		col("rangevalue", "Range metric", "kilometres per 100% SOC"),
		col("createdat", "Creation or version timestamp where present. Used for versioning where configured.", ""),
	}
	return Domain{
		Name: "trip",
		Tables: []Table{
			{
				Name: "tripreport", Kind: kindFact,
				Description: "Each row is a trip.",
				Identity:    "(daybucket, fleetid, starttime, vehicleid), with createdat used for versioning where configured.",
				UseWhen:     []string{"Detailed trips", "Exact timestamp analysis"},
				Columns:     trip,
			},
			{
				Name: "invalidtripreport", Kind: kindFact,
				Description: "Invalid trip records. Use only when the user asks about invalid trips or explicitly includes invalid records.",
				Columns:     trip,
			},
			tripRollup("tripreport_dailybucket", "daybucket"),
			tripRollup("tripreport_weeklybucket", "weekbucket"),
			tripRollup("tripreport_monthlybucket", "monthbucket"),
		},
	}
}

func tripRollup(name, grain string) Table {
	return Table{
		Name: name, Kind: kindRollup, Grain: grain,
		Description: "Aggregate trip measures. sum() additive metrics. The primary grain column is " + grain + ". Other bucket columns are known for this rollup family; confirm them from DDL before filtering on a non-primary bucket.",
		UseWhen:     []string{"Total distance over a long date range", "Trip count and distance by fleet", "Vehicle or fleet trends"},
		Columns: []Column{
			col("daybucket", "Day bucket", "epoch days"),
			col("weekbucket", "Week bucket", "epoch weeks"),
			col("monthbucket", "Fixed 30-day epoch interval, not a calendar month", "30-day epoch intervals"),
			col("utctime", "Noon UTC for the relevant bucket", "Unix milliseconds"),
			col("vehicleid", "Vehicle identifier", ""),
			col("fleetid", "Fleet identifier copied into the source report", ""),
			col("totaltrips", "Accumulated trip count", "trips"),
			{Name: "totaldistance", Description: "Accumulated distance", Unit: "kilometres", SQLType: "Decimal(10,3)", TypeStatus: typeKnown},
			col("totalduration", "Accumulated trip duration", "milliseconds"),
			col("totalsocconsumed", "Accumulated SOC consumption", "percentage points"),
			col("totalrangevalue", "Accumulated range metric", "kilometres per 100% SOC"),
		},
	}
}

func geofenceDomain() Domain {
	return Domain{
		Name: "geofence",
		Tables: []Table{{
			Name: "geofenceevent", Kind: kindFact,
			Description: "Each row is a geofence event.",
			OrderingKey: "(vehicleid, utctime, ruleid). This key does not uniquely identify every logical event. Multiple events may share it even when geofenceid differs.",
			UseWhen:     []string{"Entry and exit counts", "Event counts by vehicle, geofence, event type, or time interval", "Geofence activity analysis"},
			Columns: []Column{
				col("vehicleid", "Vehicle identifier", ""),
				col("utctime", "Event timestamp", "Unix milliseconds"),
				col("ruleid", "Rule identifier", ""),
				col("geofenceid", "Geofence identifier", ""),
				col("geofenceevent", "Event type, typically ENTRY or EXIT", ""),
				col("geofencedata", "JSON payload with additional geofence details. Do not invent JSON paths.", "JSON"),
			},
		}},
	}
}

func alertDomain() Domain {
	return Domain{
		Name: "alert",
		Tables: []Table{{
			Name: "alertevent", Kind: kindFact,
			Description: "Each row is an alert firing.",
			OrderingKey: "(vehicleid, utctime, alertid). No version column is confirmed, so this key must not be treated as a unique logical event id.",
			UseWhen:     []string{"Alert counts over time", "Alert counts by vehicle or alert identifier", "Alert breakdown by alert name extracted from alertdata", "Vehicles with frequent alerts"},
			Columns: []Column{
				col("vehicleid", "Vehicle identifier", ""),
				col("utctime", "Alert timestamp", "Unix milliseconds"),
				col("alertid", "Alert identifier", ""),
				col("alertdata", "JSON payload containing alert details, including alertname where available. Extract it with ClickHouse JSON functions only after seeing the actual JSON structure. Do not invent a path.", "JSON"),
				col("devdata", "JSON payload with additional device or event information. Do not invent JSON paths.", "JSON"),
			},
		}},
	}
}

func selectionRules() []SelectionRule {
	return []SelectionRule{
		{Question: "Total distance over a long date range", Table: "tripreport_dailybucket", Note: "Use a coarser tripreport rollup when the reporting period is weekly or monthly and the grain is suitable."},
		{Question: "Detailed trips or exact timestamp analysis", Table: "tripreport"},
		{Question: "Total trip count and distance by fleet", Table: "tripreport_dailybucket", Note: "Choose the rollup grain that matches the requested period."},
		{Question: "Charging session count and total SOC gain", Table: "chargereport_dailybucket", Note: "Choose the rollup grain that matches the requested period."},
		{Question: "Individual charging-session details", Table: "chargereport"},
		{Question: "Invalid charging sessions", Table: "invalid_chargereport"},
		{Question: "Invalid trips", Table: "invalidtripreport"},
		{Question: "Geofence entry/exit counts", Table: "geofenceevent"},
		{Question: "Alert counts by time, vehicle, or alert name", Table: "alertevent"},
		{Question: "Vehicle-level or fleet-level trends", Table: "tripreport_dailybucket", Note: "Use the trip or charge rollup that matches the metric."},
		{Question: "Calendar-month statistics", Table: "tripreport", Note: "Group by the actual timestamp. monthbucket is not a calendar month. Use the fact table for the metric being asked."},
	}
}

func relationshipNotes() []string {
	return []string{
		"vehicleid is the common identifier across charge, trip, geofence, and alert reports.",
		"fleetid is present in charge and trip reports and their rollups. It is not a known column in geofenceevent or alertevent.",
		"Charge and trip records copy fleetid, chassisno, and regno at insertion time. These values may not reflect current vehicle membership.",
		"There is no known transaction ID connecting a charging session to a trip.",
		"Charge and trip intervals can overlap. Joining raw reports can multiply rows and inflate totals.",
		"Avoid cross-report joins when a question can be answered from one table.",
		"For cross-report analysis, aggregate each source at a compatible grain before joining, and explicitly define the join keys and time relationship.",
		"Do not assume historical fleet membership is available in ClickHouse.",
	}
}

func caveatNotes() []string {
	return []string{
		"ReplacingMergeTree fact tables may contain multiple versions until merges complete.",
		"Rollups may retain contributions from older or subsequently replaced fact versions.",
		"Rollup results must not automatically be assumed equivalent to deduplicated fact-table results.",
		"Do not claim that facts or rollups are always more accurate.",
		"When results disagree, explain the possible data-quality issue and recommend validation against the corresponding fact table.",
		"Do not use FINAL indiscriminately; consider query cost and table semantics.",
	}
}

func col(name, description, unit string) Column {
	return Column{Name: name, Description: description, Unit: unit, TypeStatus: typeOpen}
}

// CatalogJSON returns the full static catalog. It does not query ClickHouse.
func CatalogJSON() string {
	encoded, err := json.Marshal(staticCatalog())
	if err != nil {
		return `{"error":"schema catalog could not be encoded"}`
	}
	return string(encoded)
}

// TableColumns returns the catalog column names for one queryable table.
func TableColumns(name string) []string {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, domain := range staticCatalog().Domains {
		for _, table := range domain.Tables {
			if table.Name != name {
				continue
			}
			out := make([]string, 0, len(table.Columns))
			for _, column := range table.Columns {
				out = append(out, column.Name)
			}
			return out
		}
	}
	return nil
}

// TableBinding describes how a catalog table is authorized.
func TableBinding(name string) (domain, column string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "alertevent":
		return "alerts", "vehicleid", true
	case "geofenceevent":
		return "geofence", "vehicleid", true
	case "tripreport", "invalidtripreport", "tripreport_dailybucket", "tripreport_weeklybucket", "tripreport_monthlybucket":
		return "trips", "fleetid", true
	case "chargereport", "invalid_chargereport", "chargereport_dailybucket", "chargereport_weeklybucket", "chargereport_monthlybucket":
		return "charging", "fleetid", true
	default:
		return "", "", false
	}
}

func bindingsFor(scope authz.Scope) map[string]policy.TableBinding {
	out := map[string]policy.TableBinding{}
	for _, name := range QueryTables() {
		domain, column, ok := TableBinding(name)
		if !ok {
			continue
		}
		ids := map[string]struct{}{}
		for _, id := range scope.IDs(domain) {
			ids[id] = struct{}{}
		}
		out[name] = policy.TableBinding{Column: column, IDs: ids}
	}
	return out
}

func columnsFor() map[string][]string {
	out := map[string][]string{}
	for _, name := range QueryTables() {
		out[name] = TableColumns(name)
	}
	return out
}

// QueryTables returns the normal table names the model may query.
func QueryTables() []string {
	catalog := staticCatalog()
	names := make([]string, 0, 16)
	for _, domain := range catalog.Domains {
		for _, table := range domain.Tables {
			names = append(names, table.Name)
		}
	}
	return names
}

// SelectTable returns the preferred table for a documented question.
func SelectTable(question string) (string, bool) {
	question = strings.TrimSpace(question)
	for _, rule := range selectionRules() {
		if rule.Question == question {
			return rule.Table, rule.Table != ""
		}
	}
	return "", false
}

// BriefInstructions is the concise guidance sent to the model.
func BriefInstructions() string {
	return strings.TrimSpace(`
You answer analytics questions about the ClickHouse database already selected for this gateway.
Choose a table from this catalog. Do not call list_databases, list_tables, or any other schema-discovery tool.
Call get_schema_catalog only when you need column or metric detail that is not in this summary.

Charge facts: chargereport (one charging session), invalid_chargereport (invalid sessions, including socgain <= 0; use only when asked).
Charge rollups: chargereport_dailybucket, chargereport_weeklybucket, chargereport_monthlybucket.
Metrics: totalcharges, totalsocgain, totalcompletesessions, totaloverchargesessions, totalchargeduration. Combine them with sum(). Do not count rollup rows as sessions.

Trip facts: tripreport (one trip), invalidtripreport (invalid trips; use only when asked).
Trip rollups: tripreport_dailybucket, tripreport_weeklybucket, tripreport_monthlybucket.
Metrics: totaltrips, totaldistance (kilometres, Decimal(10,3)), totalduration, totalsocconsumed, totalrangevalue. Use sum() for totals. For an average, divide summed values; do not average rollup rows.

Events: geofenceevent (ENTRY or EXIT and related counts) and alertevent (alert counts). fleetid is not a known column on those two tables.
vehicleid is the shared identifier. fleetid on charge and trip rows was copied at insert time and may be stale. Do not join raw charge and trip rows; intervals overlap and totals inflate. Aggregate each source before any cross-report join.

Time: daybucket = starttime_ms/86400000, weekbucket = starttime_ms/604800000, monthbucket = starttime_ms/2592000000. monthbucket is a fixed 30-day epoch interval, not a calendar month. Rollup utctime is noon UTC for the bucket. Use UTC unless asked otherwise. For calendar months, group by the real timestamp on the fact table.
Use rollups for long-range counts and totals. Use fact tables for individual records and exact timestamp filters.
Query only the table names listed here. Fact tables may show multiple versions until merges finish, and rollups may not match a deduplicated fact query. Say so when results disagree instead of claiming one side is always correct.

Choose the result shape from the user's intent, this catalog, the authorized fleet or vehicle scope, and any cardinality estimate that a query actually returned. Use detailed rows when the user asks for individual records and the result can fit. Use aggregation for totals, averages, trends, or grouped statistics. Use a summary with breakdowns when a broad request would be unhelpful as raw rows. Use SQL LIMIT when the user asks for raw records that cannot fit in one response. A long date range is not automatically a summary, and a short date range is not automatically raw rows. Do not silently replace an explicit request for every raw record with a summary; say that it cannot fit and offer an aggregate or a narrower filter.

Compute totals, averages, and trends in ClickHouse. Use a daily, weekly, or monthly bucket table only when its grain matches the question. Do not mix fact rows and rollup rows in one total.

EXPLAIN ESTIMATE may be run on the same SELECT before fetching rows. If it fails or returns no cardinality, do not invent a row count.

MAX_RESULT_ROWS, MAX_RESPONSE_BYTES, and the query timeout are fixed server limits. A rejection means no result was returned and is not a partial or complete report. SQL LIMIT is the only supported bound. Do not claim an export or a next page.
`)
}

// LimitInstructions states the configured safety boundaries. It is not a date rule.
func LimitInstructions(maxRows, maxBytes int, timeout time.Duration) string {
	seconds := int(timeout.Seconds())
	if timeout > 0 && seconds == 0 {
		seconds = 1
	}
	return fmt.Sprintf("Configured limits: MAX_RESULT_ROWS=%d, MAX_RESPONSE_BYTES=%d, QUERY_TIMEOUT_SECONDS=%d. These limits are fixed. A result that exceeds them is not returned and is not a partial report.", maxRows, maxBytes, seconds)
}
