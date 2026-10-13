package querytable

import (
	"fmt"
	"strconv"
	"strings"
)

// Rewrite only executable PostgreSQL parameter/identifier tokens. Host SQL may
// contain quoted identifiers, dollar-quoted strings, escape strings, and nested
// comments; none of those may be interpreted as compiler aliases or bindings.
func rewritePostgresSQL(sql string, parameters []int, relations map[string]string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(sql); {
		start := i
		c := sql[i]
		switch {
		case c == '\'' || c == '"':
			quote := c
			escape := quote == '\'' && i > 0 && (sql[i-1] == 'E' || sql[i-1] == 'e') && (i < 2 || !postgresIdentifierByte(sql[i-2]))
			i++
			for i < len(sql) {
				if escape && sql[i] == '\\' {
					i += 2
					continue
				}
				if sql[i] == quote {
					i++
					if i < len(sql) && sql[i] == quote {
						i++
						continue
					}
					break
				}
				i++
			}
			if i > len(sql) {
				i = len(sql)
			}
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
			i += 2
			depth := 1
			for i < len(sql) && depth > 0 {
				if i+1 < len(sql) && sql[i:i+2] == "/*" {
					depth++
					i += 2
				} else if i+1 < len(sql) && sql[i:i+2] == "*/" {
					depth--
					i += 2
				} else {
					i++
				}
			}
		case c == '$':
			i++
			if i < len(sql) && sql[i] >= '0' && sql[i] <= '9' {
				for i < len(sql) && sql[i] >= '0' && sql[i] <= '9' {
					i++
				}
				n, err := strconv.Atoi(sql[start+1 : i])
				if err != nil || n < 1 || n > len(parameters) {
					return "", fmt.Errorf("invalid PostgreSQL plan parameter")
				}
				out.WriteByte('$')
				out.WriteString(strconv.Itoa(parameters[n-1]))
				continue
			}
			// A dollar quote tag begins with a letter/underscore, or is empty.
			if i < len(sql) && sql[i] != '$' && !postgresIdentifierStart(sql[i]) {
				out.WriteByte(c)
				continue
			}
			for i < len(sql) && (postgresIdentifierStart(sql[i]) || sql[i] >= '0' && sql[i] <= '9') {
				i++
			}
			if i < len(sql) && sql[i] == '$' {
				tag := sql[start : i+1]
				i++
				if end := strings.Index(sql[i:], tag); end >= 0 {
					i += end + len(tag)
				} else {
					return "", fmt.Errorf("unterminated PostgreSQL dollar quote")
				}
			}
		case postgresIdentifierStart(c):
			i++
			for i < len(sql) && postgresIdentifierByte(sql[i]) {
				i++
			}
			if name, ok := relations[sql[start:i]]; ok {
				out.WriteString(name)
				continue
			}
		default:
			i++
		}
		out.WriteString(sql[start:i])
	}
	return out.String(), nil
}
func postgresIdentifierStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 128
}
func postgresIdentifierByte(c byte) bool {
	return postgresIdentifierStart(c) || c >= '0' && c <= '9' || c == '$'
}
