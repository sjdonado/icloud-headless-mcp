package health

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/sjdonado/icloud-headless-mcp/internal/config"
)

// maxSQLRows bounds one health_sql answer. The store is local and small,
// but an unbounded SELECT * is still a context flood, not an answer.
const maxSQLRows = 200

// selectOnly rejects everything that is not exactly one read-only SELECT.
// Lexical, deliberately narrow: leading comments and whitespace go, one
// trailing semicolon is tolerated, WITH/CTEs and stacked statements are
// refused (revisit with a recorded decision if a rollup ever needs one).
// The read-only handle plus query-only pragma stays underneath regardless.
func selectOnly(q string) (string, error) {
	s := strings.TrimSpace(stripLeadingComments(q))
	if s == "" {
		return "", fmt.Errorf("health_sql takes a read-only SELECT")
	}
	// Exactly one trailing semicolon is tolerated; anything left means
	// stacked statements. This is lexical on purpose: a semicolon inside
	// a string literal is refused too, in the safe direction.
	s = strings.TrimSuffix(s, ";")
	if strings.Contains(s, ";") {
		return "", fmt.Errorf("health_sql takes one statement, not several")
	}
	first, _ := splitFirstWord(s)
	if !strings.EqualFold(first, "SELECT") {
		return "", fmt.Errorf("health_sql takes a read-only SELECT, not %q", first)
	}
	return s, nil
}

func splitFirstWord(s string) (string, string) {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	i := strings.IndexFunc(s, unicode.IsSpace)
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i:]
}

// stripLeadingComments removes -- line comments and /* */ blocks ahead of
// the statement, so `/* x */ SELECT ...` gates on SELECT, while a comment
// smuggling a second statement still hits the semicolon rule.
func stripLeadingComments(s string) string {
	for {
		t := strings.TrimLeftFunc(s, unicode.IsSpace)
		if strings.HasPrefix(t, "--") {
			if i := strings.Index(t, "\n"); i >= 0 {
				s = t[i+1:]
				continue
			}
			return ""
		}
		if strings.HasPrefix(t, "/*") {
			if i := strings.Index(t[2:], "*/"); i >= 0 {
				s = t[2+i+2:]
				continue
			}
			return ""
		}
		return t
	}
}

// SQL runs one gated read-only SELECT and returns columns plus rows.
func SQL(cfg *config.Config, query string) (map[string]any, error) {
	stmt, err := selectOnly(query)
	if err != nil {
		return nil, err
	}
	db, out, ok := openQueryDB(cfg)
	if !ok {
		if e, isErr := out["error"]; isErr {
			return nil, fmt.Errorf("%v", e)
		}
		return out, nil
	}
	defer db.Close()
	rows, err := db.Query(stmt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var records []map[string]any
	truncated := false
	for rows.Next() {
		if len(records) >= maxSQLRows {
			truncated = true
			break
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for i, c := range cols {
			row[c] = jsonable(vals[i])
		}
		records = append(records, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if records == nil {
		records = []map[string]any{}
	}
	return map[string]any{"columns": cols, "rows": records, "truncated": truncated}, nil
}

func jsonable(v any) any {
	switch t := v.(type) {
	case []byte:
		return string(t)
	case int64, float64, string, bool, nil:
		return t
	default:
		return fmt.Sprintf("%v", t)
	}
}
