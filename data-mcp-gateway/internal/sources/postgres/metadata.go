package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ColumnMeta is one column from information_schema.
type ColumnMeta struct {
	Name     string `json:"name"`
	DataType string `json:"data_type"`
	Nullable bool   `json:"nullable"`
	Ordinal  int    `json:"ordinal_position"`
}

// TableMeta is approved-table metadata hydrated from PostgreSQL.
type TableMeta struct {
	Name        string       `json:"name"`
	TableType   string       `json:"table_type,omitempty"`
	Description string       `json:"description,omitempty"`
	Columns     []ColumnMeta `json:"columns,omitempty"`
}

// MetadataCache holds in-memory schema metadata for approved tables only.
type MetadataCache struct {
	mu        sync.RWMutex
	tables    map[string]TableMeta // schema.table -> meta
	fetchedAt time.Time
}

func newMetadataCache() *MetadataCache {
	return &MetadataCache{tables: map[string]TableMeta{}}
}

// Snapshot returns a copy of cached table metadata keyed by schema.table.
func (c *MetadataCache) Snapshot() map[string]TableMeta {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]TableMeta, len(c.tables))
	for k, v := range c.tables {
		cols := append([]ColumnMeta(nil), v.Columns...)
		v.Columns = cols
		out[k] = v
	}
	return out
}

// FetchedAt returns when the cache was last hydrated.
func (c *MetadataCache) FetchedAt() time.Time {
	if c == nil {
		return time.Time{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.fetchedAt
}

// Replace swaps the cache contents.
func (c *MetadataCache) Replace(tables map[string]TableMeta) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tables = tables
	c.fetchedAt = time.Now().UTC()
}

func hydrateMetadata(ctx context.Context, client *Client, approved []string) (map[string]TableMeta, error) {
	if client == nil {
		return nil, fmt.Errorf("postgres client is nil")
	}
	if len(approved) == 0 {
		return map[string]TableMeta{}, nil
	}
	filter, err := approvedINClause(approved)
	if err != nil {
		return nil, err
	}
	tableSQL := `
SELECT
    t.table_schema,
    t.table_name,
    t.table_type,
    obj_description(
        to_regclass(format('%I.%I', t.table_schema, t.table_name)),
        'pg_class'
    ) AS table_description
FROM information_schema.tables t
WHERE format('%s.%s', t.table_schema, t.table_name) IN (` + filter + `)
ORDER BY t.table_schema, t.table_name`
	colSQL := `
SELECT
    table_schema,
    table_name,
    ordinal_position,
    column_name,
    data_type,
    is_nullable
FROM information_schema.columns
WHERE format('%s.%s', table_schema, table_name) IN (` + filter + `)
ORDER BY table_schema, table_name, ordinal_position`

	tableRows, err := queryJSONRows(ctx, client, tableSQL)
	if err != nil {
		return nil, fmt.Errorf("table metadata: %w", err)
	}
	colRows, err := queryJSONRows(ctx, client, colSQL)
	if err != nil {
		return nil, fmt.Errorf("column metadata: %w", err)
	}
	return mergeMetadata(approved, tableRows, colRows), nil
}

func approvedINClause(approved []string) (string, error) {
	parts := make([]string, 0, len(approved))
	for _, name := range approved {
		name = strings.ToLower(strings.TrimSpace(name))
		schema, table, ok := strings.Cut(name, ".")
		if !ok || schema == "" || table == "" || strings.ContainsAny(schema+table, `'"\`) {
			return "", fmt.Errorf("invalid approved table %q", name)
		}
		parts = append(parts, fmt.Sprintf("'%s.%s'", schema, table))
	}
	return strings.Join(parts, ", "), nil
}

func queryJSONRows(ctx context.Context, client *Client, sql string) ([]map[string]any, error) {
	result, err := client.Call(ctx, upstreamQueryTool, client.QueryArgs(strings.TrimSpace(sql)))
	if err != nil {
		return nil, err
	}
	text := toolResultText(result)
	if result.IsError {
		return nil, fmt.Errorf("upstream error: %s", text)
	}
	return parseJSONRows(text)
}

func parseJSONRows(text string) ([]map[string]any, error) {
	text = strings.TrimSpace(text)
	if text == "" || text == "null" {
		return nil, nil
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(text), &rows); err == nil {
		return rows, nil
	}
	// Some MCP responses wrap rows: {"rows":[...]} or {"data":[...]}
	var wrapped map[string]any
	if err := json.Unmarshal([]byte(text), &wrapped); err != nil {
		return nil, fmt.Errorf("metadata result is not JSON rows: %w", err)
	}
	for _, key := range []string{"rows", "data", "result"} {
		raw, ok := wrapped[key]
		if !ok {
			continue
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		if err := json.Unmarshal(encoded, &rows); err == nil {
			return rows, nil
		}
	}
	return nil, fmt.Errorf("metadata result JSON shape is unsupported")
}

func mergeMetadata(approved []string, tableRows, colRows []map[string]any) map[string]TableMeta {
	out := make(map[string]TableMeta, len(approved))
	for _, name := range approved {
		out[strings.ToLower(name)] = TableMeta{Name: strings.ToLower(name)}
	}
	for _, row := range tableRows {
		schema := stringField(row, "table_schema")
		table := stringField(row, "table_name")
		key := strings.ToLower(schema + "." + table)
		meta, ok := out[key]
		if !ok {
			continue
		}
		meta.TableType = stringField(row, "table_type")
		meta.Description = stringField(row, "table_description")
		out[key] = meta
	}
	for _, row := range colRows {
		schema := stringField(row, "table_schema")
		table := stringField(row, "table_name")
		key := strings.ToLower(schema + "." + table)
		meta, ok := out[key]
		if !ok {
			continue
		}
		meta.Columns = append(meta.Columns, ColumnMeta{
			Name:     stringField(row, "column_name"),
			DataType: stringField(row, "data_type"),
			Nullable: strings.EqualFold(stringField(row, "is_nullable"), "YES"),
			Ordinal:  intField(row, "ordinal_position"),
		})
		out[key] = meta
	}
	return out
}

func stringField(row map[string]any, key string) string {
	for _, candidate := range []string{key, strings.ToUpper(key), strings.ToLower(key)} {
		if v, ok := row[candidate]; ok && v != nil {
			switch t := v.(type) {
			case string:
				return t
			case fmt.Stringer:
				return t.String()
			default:
				return fmt.Sprint(t)
			}
		}
	}
	return ""
}

func intField(row map[string]any, key string) int {
	raw := stringField(row, key)
	if raw == "" {
		return 0
	}
	var n int
	_, _ = fmt.Sscanf(raw, "%d", &n)
	if n != 0 {
		return n
	}
	for _, candidate := range []string{key, strings.ToUpper(key), strings.ToLower(key)} {
		switch v := row[candidate].(type) {
		case float64:
			return int(v)
		case int:
			return v
		case json.Number:
			i, _ := v.Int64()
			return int(i)
		}
	}
	return 0
}
