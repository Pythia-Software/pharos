package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// A stored projection (the Search view's workspace_library, the workspace
// metrics) is kept current by triggers on the tables it reads. A trigger that
// marks on every UPDATE recomputes a workspace whenever a writer stores a row
// again unchanged or advances a column the projection never reads, which on a
// large library was most of the projections' work. So an UPDATE trigger marks
// only when a column the projection reads holds a different value, and the
// columns are found in the projection's own statements rather than listed by
// hand, where a list would go stale without anything failing.
//
// The reading is deliberately loose, and errs towards marking: a column
// counts as read by a table's trigger when a statement names it through an
// alias of that table, through an alias this reading cannot place, or without
// a qualifier in a statement that reads the table at all. A statement with a
// wildcard reads every column. TestProjectionTriggersWatchEveryColumnRead
// checks the result against SQLite's own name resolution.

var (
	sqlLiteralPattern   = regexp.MustCompile(`'(?:[^']|'')*'`)
	sqlBindingPattern   = regexp.MustCompile(`(?i)\b(?:FROM|JOIN)\s+([a-z_][a-z0-9_]*)(?:\s+(?:AS\s+)?([a-z_][a-z0-9_]*))?`)
	sqlQualifiedPattern = regexp.MustCompile(`(?i)\b([a-z_][a-z0-9_]*)\s*\.\s*([a-z_][a-z0-9_]*|\*)`)
	sqlNamePattern      = regexp.MustCompile(`(?i)[a-z_][a-z0-9_]*`)
	sqlWildcardPattern  = regexp.MustCompile(`(?i)(?:\bSELECT|\bDISTINCT|,)\s*\*`)
	// Words that follow a table name without naming it.
	sqlNotAnAlias = map[string]bool{"on": true, "where": true, "join": true, "left": true, "inner": true, "cross": true, "outer": true,
		"natural": true, "indexed": true, "not": true, "group": true, "order": true, "limit": true, "union": true, "using": true, "as": true,
		"having": true, "except": true, "intersect": true, "window": true, "full": true, "right": true, "set": true, "values": true, "select": true}
)

// projectionColumnReads is the columns of table, among columns, that the
// statements can read. NEW and OLD name the table's own row, as in the trigger
// expression that finds a changed row's workspace.
func projectionColumnReads(table string, columns []string, statements []string) []string {
	named := map[string]bool{}
	for _, column := range columns {
		named[strings.ToLower(column)] = false
	}
	read := func(name string) {
		if _, column := named[strings.ToLower(name)]; column {
			named[strings.ToLower(name)] = true
		}
	}
	for _, statement := range statements {
		text := sqlLiteralPattern.ReplaceAllString(statement, "''")
		if sqlWildcardPattern.MatchString(text) {
			return columns
		}
		aliases := map[string]map[string]bool{"new": {table: true}, "old": {table: true}}
		reads := false
		for _, match := range sqlBindingPattern.FindAllStringSubmatch(text, -1) {
			bound := strings.ToLower(match[1])
			reads = reads || bound == table
			for _, alias := range []string{bound, strings.ToLower(match[2])} {
				if alias == "" || sqlNotAnAlias[alias] {
					continue
				}
				if aliases[alias] == nil {
					aliases[alias] = map[string]bool{}
				}
				aliases[alias][bound] = true
			}
		}
		for _, match := range sqlQualifiedPattern.FindAllStringSubmatch(text, -1) {
			owners, placed := aliases[strings.ToLower(match[1])]
			if placed && !owners[table] || !placed && !reads {
				continue
			}
			if match[2] == "*" {
				return columns
			}
			read(match[2])
		}
		if reads {
			for _, name := range sqlNamePattern.FindAllString(sqlQualifiedPattern.ReplaceAllString(text, " "), -1) {
				read(name)
			}
		}
	}
	out := []string{}
	for _, column := range columns {
		if named[strings.ToLower(column)] {
			out = append(out, column)
		}
	}
	return out
}

// projectionChangeCondition is the WHEN clause of table's UPDATE trigger:
// true when the update changed a column the statements read.
func projectionChangeCondition(q queryer, table string, statements []string) (string, error) {
	rows, err := queryMaps(q, "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return "", err
	}
	columns := make([]string, 0, len(rows))
	for _, row := range rows {
		columns = append(columns, firstString(row["name"]))
	}
	reads := projectionColumnReads(table, columns, statements)
	if len(reads) == 0 {
		return "", fmt.Errorf("projection triggers: no column of %s is read", table)
	}
	changed := make([]string, 0, len(reads))
	for _, column := range reads {
		changed = append(changed, fmt.Sprintf(`OLD."%s" IS NOT NEW."%s"`, column, column))
	}
	return strings.Join(changed, " OR "), nil
}

// triggerDigest identifies a set of trigger statements. A projection stores
// the digest of the triggers it installed and installs them again when it
// differs, without marking anything: the earlier triggers marked at least as
// often.
func triggerDigest(statements []string) string {
	digest := sha256.Sum256([]byte(strings.Join(statements, "\n")))
	return hex.EncodeToString(digest[:])
}
