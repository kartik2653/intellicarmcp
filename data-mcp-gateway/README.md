# Data MCP Gateway

A Go MCP gateway with one public Streamable HTTP endpoint. It connects to the official [ClickHouse MCP server](https://github.com/ClickHouse/mcp-clickhouse) and, when enabled, Microsoft's [PostgreSQL MCP server](https://github.com/microsoft/postgres-mcp) as MCP clients. It does not open database drivers itself.

## Architecture

```text
AI application
    |  MCP Streamable HTTP  /mcp
    v
Go gateway
    |-- MCP server (downstream tools)
    |-- tool registry
    |-- query policy + fleet authorization
    |-- ClickHouse source module (MCP client)
    |-- PostgreSQL source module (MCP client, optional)
    v
Official mcp-clickhouse          @microsoft/postgres-mcp (stdio/npx)
    v                                   v
ClickHouse                         AlloyDB via Auth Proxy
```

Public tools:

- `get_schema_catalog` / `run_query` — ClickHouse telemetry catalog and queries.
- `get_postgres_schema_catalog` / `postgres_query` — PostgreSQL relational catalog and queries (when `POSTGRES_MCP_ENABLED` is on).

`list_databases`, `list_tables`, `postgres_mcp_modify`, and other upstream discovery/write tools are not exposed. Cross-database joins are not supported.

`run_query` is required for ClickHouse. If discovery does not find it, the process exits during startup.

## Layout

```text
data-mcp-gateway/
├── cmd/server/main.go
├── internal/config/
├── internal/gateway/
├── internal/policy/
├── internal/observability/
└── internal/sources/
    ├── clickhouse/
    └── postgres/
```

## Prerequisites

- Go 1.25 or newer
- [uv](https://docs.astral.sh/uv/) or another way to run `mcp-clickhouse`
- A ClickHouse HTTP endpoint for the upstream server (port 8123, or 8443 for TLS)
- For PostgreSQL: Node.js 22+ with `npx`, and AlloyDB Auth Proxy on `127.0.0.1:9471` (project `intellicar-in`, region `asia-south1`, cluster `pgsql-in`, instance `primary`)

The Go module depends on `github.com/modelcontextprotocol/go-sdk/mcp` and `github.com/AfterShip/clickhouse-sql-parser`.

## Upstream ClickHouse MCP server

The gateway starts the official server. Database passwords and the MCP bearer token belong in `.env`, not in the shell. The gateway reads that file and does not call `os.Setenv`. For stdio, only `CLICKHOUSE_MCP_ENV` is added to the child process environment.

### stdio

`.env`:

```text
CLICKHOUSE_MCP_TRANSPORT=stdio
CLICKHOUSE_MCP_COMMAND=uv
CLICKHOUSE_MCP_ARGS=["run","--with","mcp-clickhouse","--python","3.12","mcp-clickhouse"]
CLICKHOUSE_MCP_ENV={"CLICKHOUSE_HOST":"localhost","CLICKHOUSE_PORT":"8123","CLICKHOUSE_USER":"readonly","CLICKHOUSE_PASSWORD":"replace-me","CLICKHOUSE_SECURE":"false"}
```

The default upstream transport is stdio. Leave `CLICKHOUSE_ALLOW_WRITE_ACCESS` unset so the upstream server stays read-only. Also set a dedicated ClickHouse user and `CLICKHOUSE_MCP_QUERY_TIMEOUT` inside `CLICKHOUSE_MCP_ENV`. The gateway timeout does not stop work the database has already started.

### HTTP

Current mcp-clickhouse builds support Streamable HTTP. SSE is deprecated there and is not a gateway transport. Run the official server with its own settings, then point the gateway `.env` at it:

```text
CLICKHOUSE_MCP_TRANSPORT=http
CLICKHOUSE_MCP_URL=http://127.0.0.1:8000/mcp
CLICKHOUSE_MCP_AUTH_TOKEN=replace-me
```

The upstream process still needs its ClickHouse settings and one of `CLICKHOUSE_MCP_AUTH_TOKEN`, `FASTMCP_SERVER_AUTH`, or `CLICKHOUSE_MCP_AUTH_DISABLED=true` (local only). Put those in the upstream program's own env file. Do not export them from the gateway shell.

The MCP endpoint is `http://127.0.0.1:8000/mcp`. Health is `http://127.0.0.1:8000/health`.

## Gateway configuration

Copy `.env.example` to `.env` in the working directory. A missing `.env` is ignored. `GATEWAY_ENV_FILE` selects a different path and is required to exist when set. Keys in the file override the process environment. Keys absent from the file still fall back to the process environment.

`CLICKHOUSE_MCP_ARGS` is a JSON array of strings. `CLICKHOUSE_MCP_ENV` is a JSON object merged into the stdio subprocess environment. The subprocess is started with `exec.Command(executable, args...)`, never through a shell.

The bearer token is sent as `Authorization: Bearer` and is not written to logs. URLs with embedded userinfo are rejected.

| Variable | Default | Purpose |
| --- | --- | --- |
| `HTTP_ADDR` | `127.0.0.1:8080` | Public bind address |
| `CLICKHOUSE_MCP_TRANSPORT` | `stdio` | `stdio` or `http` |
| `CLICKHOUSE_MCP_COMMAND` |  | Required for stdio |
| `CLICKHOUSE_MCP_ARGS` | empty | JSON array of arguments |
| `CLICKHOUSE_MCP_ENV` | empty | JSON object for the subprocess |
| `CLICKHOUSE_MCP_URL` |  | Required for http |
| `CLICKHOUSE_MCP_AUTH_TOKEN` | empty | Bearer token for http |
| `QUERY_TIMEOUT_SECONDS` | `30` | Upstream tool call timeout |
| `MAX_RESULT_ROWS` | `200` | Reject larger JSON row arrays |
| `MAX_RESPONSE_BYTES` | `1048576` | Reject larger encoded tool results |
| `AGGREGATION_ONLY` | `false` | Stricter SELECT rules |
| `REFER_DATABASE` | `lafdatav1` | The only ClickHouse database the gateway will reference |
| `LOG_SQL` | `false` | When true, query traces include the original SQL. When false, quoted string literals are redacted |
| `PERMISSIONS_API_BASE_URL` | required | Base URL for `GET /api/v1/permissions/userfleetvehicles` |
| `PERMISSIONS_TIMEOUT_SECONDS` | `5` | Timeout for that permissions request |

## Run

From `data-mcp-gateway`:

```bash
cp .env.example .env
go run ./cmd/server
```

Edit `.env` before starting. Do not export passwords, tokens, or `CLICKHOUSE_MCP_ENV`.

The public endpoint is `http://127.0.0.1:8080/mcp`. A downstream MCP client uses Streamable HTTP against that URL. SIGINT and SIGTERM stop the HTTP server and close the upstream session, including a stdio subprocess.

Example questions, the table to use, and a query shape:

| Question | Table | SQL |
| --- | --- | --- |
| How much distance did each fleet travel last month? | `tripreport_dailybucket` for a long range, or `tripreport` when the month must be a calendar month | `SELECT fleetid, sum(totaldistance) FROM tripreport_dailybucket WHERE utctime >= ... AND utctime < ... GROUP BY fleetid` |
| Show individual charging sessions for one vehicle | `chargereport` | `SELECT vehicleid, starttime, endtime, socgain, duration FROM chargereport WHERE vehicleid = ...` |
| How many invalid charging sessions were there? | `invalid_chargereport` | `SELECT count() FROM invalid_chargereport` |
| How many geofence entries happened today? | `geofenceevent` | `SELECT count() FROM geofenceevent WHERE geofenceevent = 'ENTRY' AND utctime >= ...` |
| Which vehicles raised the most alerts? | `alertevent` | `SELECT vehicleid, count() FROM alertevent GROUP BY vehicleid ORDER BY count() DESC` |

`monthbucket` is a fixed 30-day epoch interval, not a calendar month. For a calendar month, filter `starttime` or `utctime` and group in UTC.

The query tool schema is the schema returned by the live upstream server. As of the current mcp-clickhouse README, `run_query` takes `query` and optional `params`. Treat discovery as the source of truth. Do not assume that name if the upstream server advertises a different query tool; this gateway requires the discovered `run_query` tool.

## Tests

Unit tests use an in-process MCP server and do not need ClickHouse:

```bash
go test ./...
```

The live test is built with the `integration` tag. It uses `.env` when that file is present, and otherwise skips:

```bash
go test -tags integration ./...
```

## Security limits

The policy parses one ClickHouse statement with `github.com/AfterShip/clickhouse-sql-parser` and rejects anything it cannot parse. It allows a single `SELECT`, `SHOW`, `DESCRIBE`, or `EXPLAIN` of a `SELECT` (`EXPLAIN ESTIMATE`, `EXPLAIN SYNTAX`, `EXPLAIN PIPELINE`, or `EXPLAIN AST`). Bare `EXPLAIN SELECT` is rejected because the parser requires one of those explain types. A semicolon scan also rejects a second statement outside quotes and comments. This is not a complete ClickHouse grammar, and a parse failure fails closed.

`SELECT *` and `table.*` are rejected. When `AGGREGATION_ONLY=true`, the outer select list must also contain a recognized aggregate call (`count`, `sum`, `avg`, and the other names in `internal/policy/query.go`). Unrecognized aggregate functions are rejected in that mode. The `Query` type carries an optional analytics payload so a future module can check datasets, metrics, dimensions, and time ranges before SQL is built. This phase does not plan natural-language questions.

`run_query` requires exactly one `Authorization: Bearer` token on the MCP HTTP request. The gateway sends that token to `GET {PERMISSIONS_API_BASE_URL}/api/v1/permissions/userfleetvehicles` and builds four scopes: alert vehicles (`fleet.alerts.view`), trip fleets (`fleet.trip_insights.view`), geofence vehicles (`fleet.geofence.view`), and charging fleets (`fleet.charge_insights.view`). Each physical catalog table is rewritten as a derived table that lists catalog columns and adds `vehicleid IN (...)` or `fleetid IN (...)`. A missing token, a permissions failure, an empty scope, or a filter outside that scope rejects the query before ClickHouse. `get_schema_catalog` stays local and does not call the permissions API. The gateway does not log the token.

`REFER_DATABASE` defaults to `lafdatav1`. Queries may use that database explicitly or leave the table unqualified, because the gateway sets the upstream `CLICKHOUSE_DATABASE` to the same name. Any other database is rejected, including `SHOW DATABASES`. Physical tables must be in the static catalog. Names ending in `_local` or `_mv` are rejected, including inside CTEs and subqueries. Table functions such as `remote()` are rejected.

## Column types still to confirm

Only `tripreport_*bucket.totaldistance` is recorded as `Decimal(10,3)`. These columns are described with units, and their ClickHouse types still need DDL confirmation:

- Charge facts: `daybucket`, `fleetid`, `vehicleid`, `chassisno`, `regno`, `starttime`, `endtime`, `socgain`, `duration`, `completesession`, `overchargesession`, `createdat`.
- Charge rollups: `daybucket`, `weekbucket`, `monthbucket`, `utctime`, `vehicleid`, `fleetid`, `totalcharges`, `totalsocgain`, `totalcompletesessions`, `totaloverchargesessions`, `totalchargeduration`.
- Trip facts: `daybucket`, `fleetid`, `vehicleid`, `chassisno`, `regno`, `starttime`, `endtime`, `startodo`, `endodo`, `startsoc`, `endsoc`, `socconsumed`, `distance`, `duration`, `rangevalue`, `createdat`.
- Trip rollups except `totaldistance`: `daybucket`, `weekbucket`, `monthbucket`, `utctime`, `vehicleid`, `fleetid`, `totaltrips`, `totalduration`, `totalsocconsumed`, `totalrangevalue`.
- `geofenceevent`: `vehicleid`, `utctime`, `ruleid`, `geofenceid`, `geofenceevent`, `geofencedata`.
- `alertevent`: `vehicleid`, `utctime`, `alertid`, `alertdata`, `devdata`. The JSON path for `alertname` inside `alertdata` is not confirmed.

A row limit counts a JSON array, or a `rows` / `data` / `result` array, in upstream text content. Other shapes are bounded only by `MAX_RESPONSE_BYTES`. Oversized results are rejected whole. The gateway does not trim JSON. Neither limit reduces the rows ClickHouse scans. Read-only database grants and upstream `CLICKHOUSE_ALLOW_WRITE_ACCESS=false` are still required. An MCP client cannot prove that an upstream query is read-only.

Every ClickHouse query is logged as a `clickhouse_query` trace on stderr: `received`, `accepted` or `rejected`, `upstream`, then `completed`, `upstream_error`, or `result_rejected`. Lines for one query share a `trace` id and include the database and SQL. Quoted string literals are replaced with `?` unless `LOG_SQL=true`. Result rows are not logged. Passwords and tokens are omitted.

## PostgreSQL MCP (optional)

Upstream: [`@microsoft/postgres-mcp`](https://github.com/microsoft/postgres-mcp) over **stdio** (`npx -y @microsoft/postgres-mcp run`). The gateway keeps one long-lived MCP session, calls `postgres_mcp_connect`, then `postgres_mcp_query` with the returned `connectionId`. Streamable HTTP for this upstream is not used.

Enable with `POSTGRES_MCP_ENABLED=true` and set `POSTGRES_USER` / `POSTGRES_PASSWORD`. The gateway builds `POSTGRES_MCP_CONNECTION_STRING` for the subprocess and never logs it. Defaults target AlloyDB Auth Proxy (`POSTGRES_HOST=127.0.0.1`, `POSTGRES_PORT=9471`, `POSTGRES_DATABASE=region_india`). For Neon or other TLS hosts set `POSTGRES_SSLMODE=require`.

```bash
npx -y @microsoft/postgres-mcp run   # what the gateway launches via POSTGRES_MCP_COMMAND/ARGS
```

Use a dedicated read-only PostgreSQL role: `CONNECT` on the database, `USAGE` on approved schemas, `SELECT` on approved tables only. Do not grant write privileges. The gateway never exposes `postgres_mcp_modify`.

### Schema metadata and joins

The exposable table set is defined in [`internal/sources/postgres/allowlist.json`](internal/sources/postgres/allowlist.json) (schema-derived). Only entries with `scope` of `fleetid`, `vehicleid`, or `derived` and a null `exclude_reason` are loaded. Secrets, permission catalogs, staging tables, and other `unscoped` relations stay out.

At connect (and again from `get_postgres_schema_catalog` if the cache is empty), the gateway loads metadata for **those approved tables only**:

- `COMMENT ON TABLE` via `obj_description` / `information_schema.tables`
- Column name, `data_type`, nullability, ordinal via `information_schema.columns`

Results stay in an in-memory cache. There is **no** persisted relationship catalog and no foreign-key assumption. The analytics client instructs the LLM to infer joins on demand from descriptions, column names/types, and business meaning, with explicit confidence levels and aggregation/grain checks.

`get_postgres_schema_catalog` returns the allowlist plus hydrated descriptions/columns when available (`metadata_hydrated`). Missing comments are allowed. Status/enabled column names come from hydrated metadata (and optional `use_when` hints), not from a separate status catalog.

### Authorization and limits

`postgres_query` requires the same Bearer token as ClickHouse. The gateway resolves fleet/vehicle IDs from `userfleetvehicles` and rewrites approved `schema.table` references (inner wrap uses `SELECT *`; outer `SELECT *` remains rejected):

- Direct scope: `"fleetid" IN (...)` or `"vehicleid" IN (...)`
- Derived scope: `"ruleid"|"alertid"|… IN (SELECT … FROM mapping WHERE fleetid IN (…) UNION SELECT … WHERE vehicleid IN (…))` — each mapping arm uses the matching ID set; empty arms are omitted

Examples: `geofencemodule.geofencerule` (`isenabled` / `isdeleted`), `alertmodule.alert`, `geofencemodule.geofence`.

Unmapped, secret, staging, and userid-only tables are rejected. Cross-database joins with ClickHouse are out of scope.

Query policy strips SQL comments, rejects multi-statement and write/DDL tokens, allowlists tables, then enforces row/byte limits (`POSTGRES_MAX_RESULT_ROWS`, `POSTGRES_MAX_RESPONSE_BYTES`, `POSTGRES_QUERY_TIMEOUT_SECONDS`). Prefer a DB role that cannot write; gateway checks are defense in depth, not a full PostgreSQL AST parser.

Example:

```sql
SELECT fleetid FROM fleetmodule.fleets
```

PostgreSQL queries are traced as `postgres_query` on stderr with the same stage names as ClickHouse. Passwords and connection strings are omitted.

## Adding a source

Implement `gateway.Source` (`Name`, `Connect`, `Registrations`, `Close`) under `internal/sources/<name>/`. Return public tools with their own handlers. Register the value in the `[]gateway.Source` slice in `cmd/server/main.go`. Do not import the new package from the ClickHouse module or from the registry.
