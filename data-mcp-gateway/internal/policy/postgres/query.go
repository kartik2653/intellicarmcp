package postgres

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

var (
	multiStatement = regexp.MustCompile(`;(?:\s|--)`)
	fromJoinTable  = regexp.MustCompile(`(?is)\b(?:from|join)\s+((?:"[^"]+"|[a-zA-Z_][\w$]*)\s*\.\s*(?:"[^"]+"|[a-zA-Z_][\w$]*))`)
	starSelect     = regexp.MustCompile(`(?is)\bselect\s+(?:distinct\s+)?(?:all\s+)?\*`)
	tableStar      = regexp.MustCompile(`(?is)\b[a-zA-Z_][\w$]*\s*\.\s*\*`)
	identToken     = regexp.MustCompile(`(?i)[a-z_][\w$]*`)
)

// bannedKeywords are statement-level verbs rejected after comment stripping.
var bannedKeywords = map[string]struct{}{
	"insert": {}, "update": {}, "delete": {}, "truncate": {}, "drop": {}, "alter": {}, "create": {},
	"grant": {}, "revoke": {}, "copy": {}, "call": {}, "do": {}, "set": {}, "reset": {}, "vacuum": {},
	"refresh": {}, "comment": {}, "lock": {}, "reindex": {}, "execute": {}, "prepare": {},
	"deallocate": {}, "discard": {}, "listen": {}, "notify": {}, "unlisten": {}, "security": {},
}

// TableBinding is the row scope for one physical table.
type TableBinding struct {
	Column string
	IDs    map[string]struct{}
	Perms  []string
}

// Options configure PostgreSQL query validation.
type Options struct {
	AllowedTables map[string][]string // schema.table -> columns
}

// QueryPolicy validates and scopes PostgreSQL SQL.
type QueryPolicy struct {
	tables map[string][]string
}

// New returns a PostgreSQL query policy.
func New(opts Options) *QueryPolicy {
	tables := map[string][]string{}
	for name, cols := range opts.AllowedTables {
		tables[strings.ToLower(name)] = append([]string(nil), cols...)
	}
	return &QueryPolicy{tables: tables}
}

// Validate rejects non-read-only or disallowed PostgreSQL SQL.
func (p *QueryPolicy) Validate(sql string) error {
	sql = strings.TrimSpace(sql)
	if sql == "" {
		return fmt.Errorf("query is empty")
	}
	stripped, err := stripSQLComments(sql)
	if err != nil {
		return err
	}
	stripped = strings.TrimSpace(stripped)
	if stripped == "" {
		return fmt.Errorf("query is empty")
	}
	if strings.Contains(stripped, ";") {
		trimmed := strings.TrimRightFunc(stripped, unicode.IsSpace)
		if strings.HasSuffix(trimmed, ";") {
			trimmed = strings.TrimSuffix(trimmed, ";")
		}
		if multiStatement.MatchString(trimmed+";") || strings.Contains(trimmed, ";") {
			return fmt.Errorf("multiple SQL statements are not allowed")
		}
		stripped = strings.TrimSpace(trimmed)
	}
	lower := strings.ToLower(stripped)
	if !strings.HasPrefix(lower, "select") && !strings.HasPrefix(lower, "with") {
		return fmt.Errorf("only read-only SELECT statements are allowed")
	}
	if err := rejectBannedTokens(stripped); err != nil {
		return err
	}
	if starSelect.MatchString(stripped) || tableStar.MatchString(stripped) {
		return fmt.Errorf("SELECT * is not allowed")
	}
	tables := physicalTables(stripped)
	if len(tables) == 0 {
		return fmt.Errorf("query must reference an approved table")
	}
	for _, table := range tables {
		if _, ok := p.tables[table]; !ok {
			return fmt.Errorf("table %s is not approved", table)
		}
	}
	return nil
}

// ApplyScope wraps every bound physical table so rows outside the scope cannot be read.
func ApplyScope(sql string, bindings map[string]TableBinding, columns map[string][]string) (string, error) {
	sql = strings.TrimSpace(sql)
	if err := (&QueryPolicy{tables: columns}).Validate(sql); err != nil {
		return "", err
	}
	stripped, err := stripSQLComments(sql)
	if err != nil {
		return "", err
	}
	tables := physicalTables(stripped)
	for _, table := range tables {
		binding, ok := bindings[table]
		if !ok {
			return "", fmt.Errorf("table %s is not approved", table)
		}
		if len(binding.IDs) == 0 {
			return "", fmt.Errorf("not authorized for table %s", table)
		}
		if err := rejectOutsideScope(sql, binding); err != nil {
			return "", err
		}
	}
	rewritten := sql
	for _, table := range tables {
		binding := bindings[table]
		if _, ok := columns[table]; !ok {
			return "", fmt.Errorf("table %s is not approved", table)
		}
		ids := make([]string, 0, len(binding.IDs))
		for id := range binding.IDs {
			ids = append(ids, "'"+strings.ReplaceAll(id, "'", "''")+"'")
		}
		// Project all columns inside the scope wrap; outer SELECT * remains rejected by Validate.
		inner := "SELECT * FROM " + qualify(table) +
			" WHERE " + quoteIdent(binding.Column) + " IN (" + strings.Join(ids, ", ") + ")"
		alias := strings.Split(table, ".")[1]
		wrapped := "(" + inner + ") AS " + quoteIdent(alias)
		rewritten = replaceTableRef(rewritten, table, wrapped)
	}
	return strings.TrimSpace(rewritten), nil
}

func stripSQLComments(sql string) (string, error) {
	var b strings.Builder
	b.Grow(len(sql))
	inSingle := false
	inDouble := false
	inLine := false
	inBlock := false
	for i := 0; i < len(sql); i++ {
		ch := sql[i]
		next := byte(0)
		if i+1 < len(sql) {
			next = sql[i+1]
		}
		if inLine {
			if ch == '\n' {
				inLine = false
				b.WriteByte(ch)
			}
			continue
		}
		if inBlock {
			if ch == '*' && next == '/' {
				inBlock = false
				i++
			}
			continue
		}
		if !inSingle && !inDouble {
			if ch == '-' && next == '-' {
				inLine = true
				i++
				continue
			}
			if ch == '/' && next == '*' {
				inBlock = true
				i++
				continue
			}
		}
		if ch == '\'' && !inDouble {
			if inSingle && next == '\'' {
				b.WriteByte(ch)
				b.WriteByte(next)
				i++
				continue
			}
			inSingle = !inSingle
			b.WriteByte(ch)
			continue
		}
		if ch == '"' && !inSingle {
			if inDouble && next == '"' {
				b.WriteByte(ch)
				b.WriteByte(next)
				i++
				continue
			}
			inDouble = !inDouble
			b.WriteByte(ch)
			continue
		}
		b.WriteByte(ch)
	}
	if inBlock {
		return "", fmt.Errorf("unclosed block comment")
	}
	return b.String(), nil
}

func rejectBannedTokens(sql string) error {
	for _, match := range identToken.FindAllString(sql, -1) {
		word := strings.ToLower(match)
		if _, ok := bannedKeywords[word]; ok {
			return fmt.Errorf("write or DDL statements are not allowed")
		}
	}
	return nil
}

func physicalTables(sql string) []string {
	matches := fromJoinTable.FindAllStringSubmatch(sql, -1)
	seen := map[string]struct{}{}
	var out []string
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		name := normalizeTable(match[1])
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func normalizeTable(raw string) string {
	raw = strings.TrimSpace(raw)
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return ""
	}
	schema := unquoteIdent(strings.TrimSpace(parts[0]))
	table := unquoteIdent(strings.TrimSpace(parts[1]))
	if schema == "" || table == "" {
		return ""
	}
	return strings.ToLower(schema + "." + table)
}

func unquoteIdent(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		return strings.ReplaceAll(value[1:len(value)-1], `""`, `"`)
	}
	return value
}

func quoteIdent(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func qualify(table string) string {
	parts := strings.Split(table, ".")
	if len(parts) != 2 {
		return quoteIdent(table)
	}
	return quoteIdent(parts[0]) + "." + quoteIdent(parts[1])
}

func replaceTableRef(sql, table, wrapped string) string {
	parts := strings.Split(table, ".")
	patterns := []string{
		qualify(table),
		parts[0] + "." + parts[1],
		quoteIdent(parts[0]) + "." + parts[1],
		parts[0] + "." + quoteIdent(parts[1]),
	}
	out := sql
	for _, pattern := range patterns {
		re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(pattern) + `\b`)
		out = re.ReplaceAllString(out, wrapped)
	}
	return out
}

func rejectOutsideScope(sql string, binding TableBinding) error {
	col := regexp.QuoteMeta(binding.Column)
	eq := regexp.MustCompile(`(?i)\b` + col + `\s*=\s*'([^']*)'`)
	for _, match := range eq.FindAllStringSubmatch(sql, -1) {
		if len(match) < 2 {
			continue
		}
		if _, ok := binding.IDs[match[1]]; !ok {
			return fmt.Errorf("%s %q is not authorized", binding.Column, match[1])
		}
	}
	in := regexp.MustCompile(`(?is)\b` + col + `\s+in\s*\(([^)]*)\)`)
	for _, match := range in.FindAllStringSubmatch(sql, -1) {
		if len(match) < 2 {
			continue
		}
		for _, lit := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(match[1], -1) {
			if len(lit) < 2 {
				continue
			}
			if _, ok := binding.IDs[lit[1]]; !ok {
				return fmt.Errorf("%s %q is not authorized", binding.Column, lit[1])
			}
		}
	}
	return nil
}
