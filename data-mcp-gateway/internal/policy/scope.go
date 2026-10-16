package policy

import (
	"fmt"
	"strings"

	chparser "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// TableBinding is the row scope for one physical table.
type TableBinding struct {
	Column string
	IDs    map[string]struct{}
}

// ApplyScope wraps every bound physical table so rows outside the scope cannot be read.
func ApplyScope(sql string, bindings map[string]TableBinding, columns map[string][]string) (string, error) {
	sql = strings.TrimSpace(sql)
	stmts, err := chparser.NewParser(sql).ParseStmts()
	if err != nil || len(stmts) != 1 {
		return "", fmt.Errorf("query could not be parsed")
	}
	var explain *chparser.ExplainStmt
	stmt, ok := stmts[0].(*chparser.SelectQuery)
	if !ok {
		explain, ok = stmts[0].(*chparser.ExplainStmt)
		if !ok {
			return sql, nil
		}
		stmt, ok = explain.Statement.(*chparser.SelectQuery)
		if !ok {
			return "", fmt.Errorf("only EXPLAIN of a SELECT statement is allowed")
		}
	}
	aliases := map[string]struct{}{}
	var tables []*chparser.TableExpr
	chparser.Walk(stmt, func(node chparser.Expr) bool {
		switch n := node.(type) {
		case *chparser.CTEStmt:
			for _, expr := range []chparser.Expr{n.Expr, n.Alias} {
				if name := strings.ToLower(identName(expr)); name != "" {
					aliases[name] = struct{}{}
				}
			}
		case *chparser.TableExpr:
			tables = append(tables, n)
			if n.Alias != nil {
				if name := strings.ToLower(identName(n.Alias.Alias)); name != "" {
					aliases[name] = struct{}{}
				}
			}
		}
		return true
	})
	used := map[string]TableBinding{}
	for _, table := range tables {
		ident, ok := table.Expr.(*chparser.TableIdentifier)
		if !ok || ident.Table == nil {
			continue
		}
		name := strings.ToLower(ident.Table.Name)
		binding, protected := bindings[name]
		if _, shadowed := aliases[name]; shadowed && protected {
			return "", fmt.Errorf("alias %q shadows a protected table", name)
		}
		if !protected {
			continue
		}
		if len(binding.IDs) == 0 {
			return "", fmt.Errorf("not authorized for table %s", name)
		}
		used[binding.Column] = intersectBinding(used[binding.Column], binding)
	}
	if err := rejectOutsideScope(stmt, used); err != nil {
		return "", err
	}
	for _, table := range tables {
		ident, ok := table.Expr.(*chparser.TableIdentifier)
		if !ok || ident.Table == nil {
			continue
		}
		name := strings.ToLower(ident.Table.Name)
		binding, protected := bindings[name]
		if !protected {
			continue
		}
		wrapped, err := scopedSelect(name, columns[name], binding)
		if err != nil {
			return "", err
		}
		table.Expr = &chparser.SubQuery{HasParen: true, Select: wrapped}
		if table.Alias == nil {
			table.Alias = &chparser.AliasExpr{Alias: &chparser.Ident{Name: ident.Table.Name}}
		}
	}
	if explain != nil {
		explain.Statement = stmt
		return strings.TrimSpace(chparser.Format(explain)), nil
	}
	return strings.TrimSpace(chparser.Format(stmt)), nil
}

func intersectBinding(current, next TableBinding) TableBinding {
	if len(current.IDs) == 0 {
		return next
	}
	kept := map[string]struct{}{}
	for id := range current.IDs {
		if _, ok := next.IDs[id]; ok {
			kept[id] = struct{}{}
		}
	}
	return TableBinding{Column: next.Column, IDs: kept}
}

func rejectOutsideScope(root chparser.Expr, used map[string]TableBinding) error {
	var rejected error
	chparser.Walk(root, func(node chparser.Expr) bool {
		bin, ok := node.(*chparser.BinaryOperation)
		if !ok {
			return true
		}
		column := scopeColumn(bin.LeftExpr)
		binding, ok := used[column]
		if !ok {
			return true
		}
		op := strings.ToUpper(string(bin.Operation))
		if op != "=" && op != "IN" {
			rejected = fmt.Errorf("unsupported filter on %s", column)
			return false
		}
		for _, id := range literalIDs(bin.RightExpr) {
			if _, allowed := binding.IDs[id]; !allowed {
				rejected = fmt.Errorf("%s %q is not authorized", column, id)
				return false
			}
		}
		return true
	})
	return rejected
}

func scopeColumn(expr chparser.Expr) string {
	switch n := expr.(type) {
	case *chparser.Ident:
		return strings.ToLower(n.Name)
	case *chparser.NestedIdentifier:
		if n.DotIdent != nil {
			return strings.ToLower(n.DotIdent.Name)
		}
	}
	return ""
}

func literalIDs(expr chparser.Expr) []string {
	switch n := expr.(type) {
	case *chparser.StringLiteral:
		return []string{trimLiteral(n.Literal)}
	case *chparser.NumberLiteral:
		return []string{strings.TrimSpace(n.Literal)}
	case *chparser.ColumnExprList:
		var out []string
		for _, item := range n.Items {
			out = append(out, literalIDs(item)...)
		}
		return out
	default:
		return nil
	}
}

func trimLiteral(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		value = value[1 : len(value)-1]
		value = strings.ReplaceAll(value, "''", "'")
	}
	return value
}

func scopedSelect(table string, columns []string, binding TableBinding) (*chparser.SelectQuery, error) {
	if len(columns) == 0 {
		return nil, fmt.Errorf("table %s has no catalog columns", table)
	}
	ids := make([]string, 0, len(binding.IDs))
	for id := range binding.IDs {
		ids = append(ids, "'"+strings.ReplaceAll(id, "'", "''")+"'")
	}
	sql := "SELECT " + strings.Join(columns, ", ") + " FROM " + table + " WHERE " + binding.Column + " IN (" + strings.Join(ids, ", ") + ")"
	stmts, err := chparser.NewParser(sql).ParseStmts()
	if err != nil || len(stmts) != 1 {
		return nil, fmt.Errorf("scoped query for %s could not be built", table)
	}
	sel, ok := stmts[0].(*chparser.SelectQuery)
	if !ok {
		return nil, fmt.Errorf("scoped query for %s could not be built", table)
	}
	return sel, nil
}
