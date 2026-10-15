package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	chparser "github.com/AfterShip/clickhouse-sql-parser/parser"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Query is a SQL request presented to the policy layer.
// Analytics is unused by the basic validator and exists so a later analytics
// module can supply dataset, metric, dimension, and time-range checks.
type Query struct {
	SQL       string
	Params    map[string]any
	Analytics *AnalyticsQuery
}

// AnalyticsQuery is the future analytics-module contract.
type AnalyticsQuery struct {
	Dataset    string
	Metrics    []string
	Dimensions []string
	TimeRange  *TimeRange
}

// TimeRange bounds an analytical query.
type TimeRange struct {
	Start time.Time
	End   time.Time
}

// QueryPolicy validates a query before it is sent upstream.
type QueryPolicy interface {
	Validate(ctx context.Context, q Query) error
}

// Options configures the basic validator.
type Options struct {
	AggregationOnly  bool
	AllowedDatabases []string
	AllowedTables    []string
}

// New returns a fail-closed ClickHouse SQL validator.
func New(opts Options) QueryPolicy {
	allowed := make(map[string]struct{}, len(opts.AllowedDatabases))
	for _, name := range opts.AllowedDatabases {
		name = strings.TrimSpace(name)
		if name != "" {
			allowed[name] = struct{}{}
		}
	}
	tables := make(map[string]struct{}, len(opts.AllowedTables))
	for _, name := range opts.AllowedTables {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" {
			tables[name] = struct{}{}
		}
	}
	return &validator{aggregationOnly: opts.AggregationOnly, allowed: allowed, tables: tables}
}

type validator struct {
	aggregationOnly bool
	allowed         map[string]struct{}
	tables          map[string]struct{}
}

func (v *validator) Validate(_ context.Context, q Query) error {
	sql := strings.TrimSpace(q.SQL)
	if sql == "" {
		return fmt.Errorf("query is empty")
	}
	if countStatements(sql) > 1 {
		return fmt.Errorf("multiple SQL statements are not allowed")
	}
	stmts, err := chparser.NewParser(sql).ParseStmts()
	if err != nil {
		return fmt.Errorf("query could not be parsed")
	}
	if len(stmts) != 1 {
		return fmt.Errorf("multiple SQL statements are not allowed")
	}
	switch stmt := stmts[0].(type) {
	case *chparser.SelectQuery:
		return v.checkSelect(stmt)
	case *chparser.ExplainStmt:
		return v.checkExplain(stmt)
	case *chparser.ShowStmt:
		return v.checkShow(stmt)
	case *chparser.DescribeStmt:
		return v.checkTable(stmt.Target, nil)
	default:
		return fmt.Errorf("only read-only SELECT, SHOW, DESCRIBE, and EXPLAIN statements are allowed")
	}
}

func (v *validator) checkExplain(stmt *chparser.ExplainStmt) error {
	if stmt.Statement == nil {
		return fmt.Errorf("EXPLAIN is missing a statement")
	}
	switch inner := stmt.Statement.(type) {
	case *chparser.SelectQuery:
		return v.checkSelect(inner)
	default:
		return fmt.Errorf("only EXPLAIN of a SELECT statement is allowed")
	}
}

func (v *validator) checkSelect(q *chparser.SelectQuery) error {
	if q == nil {
		return fmt.Errorf("missing SELECT statement")
	}
	if err := visitSelects(q, func(node *chparser.SelectQuery) error {
		return rejectStars(node.SelectItems)
	}); err != nil {
		return err
	}
	if v.aggregationOnly && !hasAggregate(q.SelectItems) {
		return fmt.Errorf("aggregation-only policy requires an aggregate function in the select list")
	}
	aliases := map[string]struct{}{}
	chparser.Walk(q, func(node chparser.Expr) bool {
		switch n := node.(type) {
		case *chparser.CTEStmt:
			// The parser stores `name AS (SELECT ...)` with the name in Expr.
			for _, expr := range []chparser.Expr{n.Expr, n.Alias} {
				if name := identName(expr); name != "" {
					aliases[strings.ToLower(name)] = struct{}{}
				}
			}
		case *chparser.TableExpr:
			if n.Alias != nil {
				if name := identName(n.Alias.Alias); name != "" {
					aliases[strings.ToLower(name)] = struct{}{}
				}
			}
		}
		return true
	})
	var rejected error
	chparser.Walk(q, func(node chparser.Expr) bool {
		switch n := node.(type) {
		case *chparser.TableFunctionExpr:
			if len(v.allowed) > 0 {
				rejected = fmt.Errorf("table functions are not allowed when ALLOWED_DATABASES is set")
				return false
			}
		case *chparser.TableIdentifier:
			if err := v.checkTable(n, aliases); err != nil {
				rejected = err
				return false
			}
		}
		return true
	})
	return rejected
}

func visitSelects(root *chparser.SelectQuery, fn func(*chparser.SelectQuery) error) error {
	seen := map[*chparser.SelectQuery]struct{}{}
	var walk func(*chparser.SelectQuery) error
	walk = func(node *chparser.SelectQuery) error {
		if node == nil {
			return nil
		}
		if _, ok := seen[node]; ok {
			return nil
		}
		seen[node] = struct{}{}
		if err := walk(node.InnerQuery); err != nil {
			return err
		}
		if err := fn(node); err != nil {
			return err
		}
		for _, next := range []*chparser.SelectQuery{node.UnionAll, node.UnionDistinct, node.Except, node.Intersect} {
			if err := walk(next); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root); err != nil {
		return err
	}
	var nested error
	chparser.Walk(root, func(node chparser.Expr) bool {
		sel, ok := node.(*chparser.SelectQuery)
		if !ok {
			return true
		}
		if err := walk(sel); err != nil {
			nested = err
			return false
		}
		return true
	})
	return nested
}

func countStatements(sql string) int {
	count := 0
	started := false
	inSingle, inDouble := false, false
	inLine, inBlock := false, false
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		if inLine {
			if c == '\n' {
				inLine = false
			}
			continue
		}
		if inBlock {
			if c == '*' && i+1 < len(sql) && sql[i+1] == '/' {
				i++
				inBlock = false
			}
			continue
		}
		if inSingle {
			if c == '\\' && i+1 < len(sql) {
				i++
				continue
			}
			if c == '\'' {
				if i+1 < len(sql) && sql[i+1] == '\'' {
					i++
					continue
				}
				inSingle = false
			}
			continue
		}
		if inDouble {
			if c == '"' {
				inDouble = false
			}
			continue
		}
		if c == '-' && i+1 < len(sql) && sql[i+1] == '-' {
			inLine = true
			continue
		}
		if c == '/' && i+1 < len(sql) && sql[i+1] == '*' {
			inBlock = true
			i++
			continue
		}
		if c == '\'' {
			inSingle = true
			started = true
			continue
		}
		if c == '"' {
			inDouble = true
			started = true
			continue
		}
		if c == ';' {
			if started {
				count++
				started = false
			}
			continue
		}
		if c != ' ' && c != '\n' && c != '\t' && c != '\r' {
			started = true
		}
	}
	if started {
		count++
	}
	return count
}

func (v *validator) checkShow(stmt *chparser.ShowStmt) error {
	if stmt != nil && len(v.allowed) > 0 && strings.Contains(strings.ToUpper(stmt.ShowType), "DATABASE") {
		return fmt.Errorf("only database %s may be referenced", v.onlyDatabase())
	}
	if stmt == nil {
		return nil
	}
	return v.checkTable(stmt.Target, nil)
}

func (v *validator) onlyDatabase() string {
	for name := range v.allowed {
		return name
	}
	return ""
}

func (v *validator) checkTable(table *chparser.TableIdentifier, aliases map[string]struct{}) error {
	if table == nil || table.Table == nil {
		return nil
	}
	name := strings.ToLower(strings.TrimSpace(table.Table.Name))
	if strings.HasSuffix(name, "_local") || strings.HasSuffix(name, "_mv") {
		return fmt.Errorf("tables ending in _local or _mv are not allowed")
	}
	if len(v.allowed) > 0 && table.Database != nil && strings.TrimSpace(table.Database.Name) != "" {
		if _, ok := v.allowed[table.Database.Name]; !ok {
			return fmt.Errorf("database %q is not in ALLOWED_DATABASES", table.Database.Name)
		}
	}
	if len(v.tables) == 0 {
		return nil
	}
	if _, ok := aliases[name]; ok {
		return nil
	}
	if _, ok := v.tables[name]; !ok {
		return fmt.Errorf("table %q is not in the schema catalog", name)
	}
	return nil
}

func identName(expr chparser.Expr) string {
	switch n := expr.(type) {
	case *chparser.Ident:
		return n.Name
	default:
		return ""
	}
}

func rejectStars(items []*chparser.SelectItem) error {
	for _, item := range items {
		if item == nil || item.Expr == nil {
			continue
		}
		formatted := strings.TrimSpace(chparser.Format(item.Expr))
		if formatted == "*" || strings.HasSuffix(formatted, ".*") {
			return fmt.Errorf("SELECT * is not allowed")
		}
	}
	return nil
}

func hasAggregate(items []*chparser.SelectItem) bool {
	found := false
	for _, item := range items {
		if item == nil {
			continue
		}
		chparser.Walk(item, func(node chparser.Expr) bool {
			name := ""
			switch n := node.(type) {
			case *chparser.FunctionExpr:
				if n.Name != nil {
					name = n.Name.Name
				}
			case *chparser.WindowFunctionExpr:
				if n.Function != nil && n.Function.Name != nil {
					name = n.Function.Name.Name
				}
			}
			if aggregateName(name) {
				found = true
				return false
			}
			return true
		})
	}
	return found
}

func aggregateName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	switch name {
	case "count", "sum", "avg", "min", "max", "any", "anylast", "uniq", "uniqexact",
		"grouparray", "groupuniqarray", "quantile", "median", "argmin", "argmax",
		"topk", "stddevpop", "stddevsamp", "varpop", "varsamp":
		return true
	}
	for _, prefix := range []string{"count", "sum", "avg", "uniq", "quantile", "grouparray", "median"} {
		if strings.HasPrefix(name, prefix) && len(name) > len(prefix) {
			return true
		}
	}
	return false
}

// Limits are applied to an upstream tool result after it returns.
type Limits struct {
	MaxResultRows    int
	MaxResponseBytes int
}

// EnforceResult rejects a tool result that exceeds the configured row or byte limit.
// It never returns a truncated JSON payload.
func EnforceResult(result *mcp.CallToolResult, limits Limits) (*mcp.CallToolResult, error) {
	if result == nil {
		return nil, fmt.Errorf("upstream tool returned an empty result")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("upstream tool result could not be encoded")
	}
	if limits.MaxResponseBytes > 0 && len(encoded) > limits.MaxResponseBytes {
		return toolError(fmt.Sprintf("upstream response exceeds MAX_RESPONSE_BYTES (%d)", limits.MaxResponseBytes)), nil
	}
	if limits.MaxResultRows > 0 {
		rows, known := countResultRows(result)
		if known && rows > limits.MaxResultRows {
			return toolError(fmt.Sprintf("upstream response exceeds MAX_RESULT_ROWS (%d)", limits.MaxResultRows)), nil
		}
	}
	return result, nil
}

func toolError(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: message}},
	}
}

func countResultRows(result *mcp.CallToolResult) (int, bool) {
	total := 0
	known := false
	for _, content := range result.Content {
		text, ok := content.(*mcp.TextContent)
		if !ok {
			continue
		}
		rows, ok := countJSONRows([]byte(text.Text))
		if !ok {
			continue
		}
		known = true
		total += rows
	}
	if structuredRows, ok := countJSONValue(result.StructuredContent); ok {
		known = true
		total += structuredRows
	}
	return total, known
}

func countJSONRows(payload []byte) (int, bool) {
	payload = []byte(strings.TrimSpace(string(payload)))
	if len(payload) == 0 {
		return 0, false
	}
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return 0, false
	}
	if text, ok := value.(string); ok {
		return countJSONRows([]byte(text))
	}
	return countJSONValue(value)
}

func countJSONValue(value any) (int, bool) {
	switch typed := value.(type) {
	case []any:
		return len(typed), true
	case map[string]any:
		for _, key := range []string{"rows", "data", "result"} {
			raw, ok := typed[key]
			if !ok {
				continue
			}
			if rows, known := countJSONValue(raw); known {
				if _, isArray := raw.([]any); isArray {
					return rows, true
				}
			}
		}
	}
	return 0, false
}
