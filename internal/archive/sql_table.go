package archive

import (
	"context"
	"database/sql/driver"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gbdubs/pharos/internal/querytable"
	"modernc.org/sqlite"
)

// sqlDataset runs query-table requests in SQLite for datasets too large to
// load into memory (the tool ledger holds a row per tool call). Every field is
// bound to an allowlisted SQL expression; request values are always bound as
// parameters. Filter semantics follow querytable.Apply.
type sqlDataset struct {
	from    string            // FROM and JOIN clauses
	base    string            // always-applied condition, or ""
	columns map[string]string // field name -> SQL expression
	// countFrom, when set, is enough to count rows whose filters read only
	// the table aliased countAlias, sparing a join per row.
	countFrom, countAlias string
	// buckets maps day, week, and month fields to the timestamp they are
	// taken from, so a filter on one can walk an index on the timestamp.
	buckets map[string]string
	// timestamps admits raw ISO timestamp columns for conservative indexed
	// candidates; the typed executor still applies the exact datetime predicate.
	timestamps map[string]string
	// lookups are fields with an index that finds their few rows directly,
	// such as IDs; filters on other fields are marked likely() (see where).
	lookups           map[string]bool
	toolSearchIndexed bool            // the command trigram index is complete
	queryText         *queryTextIndex // complete, normalized exact substring membership
	scalarFacts       bool            // complete compact per-call time/activity projection
	calendarFacts     bool            // calendar matches the current local timezone
	// cube, when set, answers what it can in place of the rows (see sqlCube).
	cube *sqlCube
	// Joins to unique primary keys can be omitted when no consumed expression
	// references them. Explicit pruning lets SQLite choose a covering count
	// index before its later unused-LEFT-JOIN elimination pass.
	optionalJoins      map[string]string
	pageFrom           string
	pageIndexes        map[string]string
	coveringIndex      string
	rangeCoveringIndex string
	coveredFields      map[string]bool
	timeCoveringFields map[string]bool
}

func (d sqlDataset) preferCoveringScan(terms []querytable.WhereTerm) (sqlDataset, bool) {
	if d.pageFrom == "FROM tool_query_facts t" {
		return d, true
	}
	if d.coveringIndex == "" {
		return d, false
	}
	for _, term := range terms {
		for _, clause := range term.Predicates() {
			if d.lookups[clause.Field] && clause.Op == "=" && clause.Value != "" {
				return d, false
			}
		}
	}
	for field := range d.columns {
		if !d.coveredFields[field] {
			return d, false
		}
	}
	index := d.coveringIndex
	compact := true
	for field := range d.columns {
		compact = compact && d.timeCoveringFields[field]
	}
	if compact {
		index = d.pageIndexes["started_at"]
	}
	for _, term := range terms {
		if !term.IsGroup() && !term.Negated && slices.Contains([]string{"started_at", "day", "week", "month"}, term.Field) {
			index = d.rangeCoveringIndex
			if compact {
				index = d.pageIndexes["started_at"]
			}
		}
	}
	d.from = strings.Replace(d.from, d.pageFrom, d.pageFrom+" INDEXED BY "+sqlIdentifier(index), 1)
	return d, true
}

func (d sqlDataset) project(fields []string) sqlDataset {
	columns := make(map[string]string, len(fields))
	for _, field := range fields {
		if expression, ok := d.columns[field]; ok {
			columns[field] = expression
		}
	}
	d.columns = columns
	if d.scalarFacts && d.pageFrom == "FROM tool_calls t" {
		compact := true
		for field := range columns {
			compact = compact && slices.Contains([]string{"id", "workspace_id", "started_at", "day", "week", "month", "duration_ms", "provider", "activity", "tool_category", "call_count", "repository_name", "source_kind", "title"}, field)
		}
		if compact {
			d.from = strings.Replace(d.from, "FROM tool_calls t", "FROM tool_query_facts t", 1)
			d.pageFrom = "FROM tool_query_facts t"
			if _, ok := columns["activity"]; ok {
				columns["activity"] = "t.activity"
			}
			if d.calendarFacts {
				for field, expr := range map[string]string{"day": "t.day", "week": "date(t.day,'-6 days','weekday 1')", "month": "substr(t.day,1,7)"} {
					if _, ok := columns[field]; ok {
						columns[field] = expr
					}
				}
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for alias, join := range d.optionalJoins {
			remaining := strings.Replace(d.from, join, "", 1)
			if remaining == d.from {
				continue
			}
			uses := func(sql string) bool {
				return slices.ContainsFunc(sqlQualifier.FindAllStringSubmatch(sql, -1), func(match []string) bool { return match[1] == alias })
			}
			used := uses(remaining + " " + d.base)
			for _, expression := range columns {
				used = used || uses(expression)
			}
			if !used {
				d.from = remaining
				changed = true
			}
		}
	}
	return d
}

// sqlCube is a table of a dataset's rows grouped by fields with few values,
// holding each group's row count and the sums and ranges of its numbers. A
// row count, filter-value, or column-stats request that reads only fields the
// cube keeps gets the same answer from it in a fraction of the time.
type sqlCube struct {
	from  string // FROM clause of the grouped table
	count string // column holding each group's row count
	// dimensions are the grouped fields and fields that follow from them, as
	// expressions over the grouped table equal to the dataset's own.
	dimensions map[string]string
	// present maps fields kept only as present or absent to a column that is
	// true when the field is non-empty. They serve is_null and is_not_null.
	present map[string]string
	// measures maps number and time fields to their per-group columns.
	measures map[string]cubeMeasure
}

// cubeMeasure names a field's per-group columns: its sum (numbers only), its
// smallest and largest non-empty values, and how many rows had one.
type cubeMeasure struct{ sum, min, max, count string }

// table is the cube as a dataset of its dimensions, with each present-only
// field null when absent.
func (cube *sqlCube) table() sqlDataset {
	columns := maps.Clone(cube.dimensions)
	for field, column := range cube.present {
		columns[field] = "CASE WHEN " + column + " THEN 'x' END"
	}
	return sqlDataset{from: cube.from, columns: columns}
}

// dimensionFor returns field's expression over the cube, if it is grouped by
// it. A nil cube has none.
func (cube *sqlCube) dimensionFor(field string) (string, bool) {
	if cube == nil {
		return "", false
	}
	expression, ok := cube.dimensions[field]
	return expression, ok
}

// filters reports whether the cube can apply every term.
func (cube *sqlCube) filters(terms []querytable.WhereTerm) bool {
	for _, term := range terms {
		for _, clause := range term.Predicates() {
			if _, ok := cube.dimensions[clause.Field]; ok {
				continue
			}
			if _, ok := cube.present[clause.Field]; !ok || clause.Op != "is_null" && clause.Op != "is_not_null" {
				return false
			}
		}
	}
	return true
}

func (d sqlDataset) cubeFilters(terms []querytable.WhereTerm) bool {
	if d.cube == nil || !d.cube.filters(terms) {
		return false
	}
	if d.toolSearchIndexed {
		for _, term := range terms {
			for _, clause := range term.Predicates() {
				if !clause.Negated && toolSearchTerm(clause.Field, clause.Op, clause.Value) != "" {
					return false
				}
			}
		}
	}
	return true
}

var sqlQualifier = regexp.MustCompile(`([A-Za-z_]\w*)\.[A-Za-z_]`)

// countable reports whether countFrom can count rows matching terms.
func (d sqlDataset) countable(terms []querytable.WhereTerm) bool {
	if d.countFrom == "" {
		return false
	}
	for _, term := range terms {
		for _, clause := range term.Predicates() {
			for _, match := range sqlQualifier.FindAllStringSubmatch(d.columns[clause.Field], -1) {
				if match[1] != d.countAlias {
					return false
				}
			}
		}
	}
	return true
}

// bucketRange bounds the timestamps a day, week, or month bucket can hold,
// with a day's margin for the local time offset.
func bucketRange(field, value string) (from, to string, ok bool) {
	var start, end time.Time
	var err error
	switch field {
	case "day", "week":
		start, err = time.ParseInLocation("2006-01-02", value, time.Local)
		end = start.AddDate(0, 0, map[string]int{"day": 1, "week": 7}[field])
	case "month":
		start, err = time.ParseInLocation("2006-01", value, time.Local)
		end = start.AddDate(0, 1, 0)
	}
	if err != nil || start.IsZero() {
		return "", "", false
	}
	return formatTime(start.Add(-24 * time.Hour)), formatTime(end.Add(24 * time.Hour)), true
}

var registerRegexp sync.Once

// registerSQLiteRegexp provides REGEXP for matches_regex filters, and
// regexp_extract(pattern, value, numeric) for sorts on an extracted value.
func registerSQLiteRegexp() {
	registerRegexp.Do(func() {
		var cache sync.Map
		compiled := func(pattern any) *regexp.Regexp {
			text, _ := pattern.(string)
			if cached, ok := cache.Load(text); ok {
				expression, _ := cached.(*regexp.Regexp)
				return expression
			}
			expression, err := regexp.Compile(text)
			if err != nil {
				expression = nil
			}
			cache.Store(text, expression)
			return expression
		}
		_ = sqlite.RegisterDeterministicScalarFunction("regexp", 2, func(ctx *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			expression := compiled(args[0])
			if args[1] == nil || expression == nil {
				return int64(0), nil
			}
			if expression.MatchString(sqliteText(args[1])) {
				return int64(1), nil
			}
			return int64(0), nil
		})
		// As in querytable.Apply, a sort key is the first capture group, or the
		// whole match without one; a null value, a non-match, or an invalid
		// pattern sorts as NULL. A numeric field's key that reads as a number
		// is one, so it orders numerically.
		_ = sqlite.RegisterDeterministicScalarFunction("regexp_extract", 3, func(ctx *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			expression := compiled(args[0])
			if args[1] == nil || expression == nil {
				return nil, nil
			}
			match := expression.FindStringSubmatch(sqliteText(args[1]))
			if len(match) == 0 {
				return nil, nil
			}
			extracted := match[0]
			if len(match) > 1 {
				extracted = match[1]
			}
			if numeric, _ := args[2].(int64); numeric != 0 {
				if number, err := strconv.ParseFloat(extracted, 64); err == nil {
					return number, nil
				}
			}
			return extracted, nil
		})
	})
}

func sqliteText(value driver.Value) string {
	switch value := value.(type) {
	case string:
		return value
	case []byte:
		return string(value)
	}
	return fmt.Sprint(value)
}

func (d sqlDataset) expr(field string) (string, error) {
	expression, ok := d.columns[field]
	if !ok {
		return "", fmt.Errorf("field %q has no SQL binding", field)
	}
	return expression, nil
}

func likePattern(value, prefix, suffix string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(value))
	return prefix + escaped + suffix
}

func (d sqlDataset) clause(clause querytable.WhereClause, field querytable.Field) (string, []any, error) {
	expression, err := d.expr(clause.Field)
	if err != nil {
		return "", nil, err
	}
	// Match querytable's pending-filter behavior for blank typed comparisons.
	if clause.Value == "" && clause.Op != "is_null" && clause.Op != "is_not_null" &&
		(clause.Op != "=" && clause.Op != "!=" || field.Kind == querytable.Number || field.Kind == querytable.Datetime || field.Kind == querytable.Bool) {
		return "1", nil, nil
	}
	isNull := fmt.Sprintf("(%s IS NULL OR %s = '')", expression, expression)
	if field.Kind == querytable.TextArray {
		// Text arrays are stored as JSON arrays; an empty one is null.
		isNull = fmt.Sprintf("(%s IS NULL OR %s IN ('','[]'))", expression, expression)
	}
	var base string
	args := []any{}
	switch clause.Op {
	case "includes":
		// Membership in a JSON text array; a null, empty, or malformed array
		// contains nothing. Case-insensitive unless the field opts out.
		elements := fmt.Sprintf("json_each(CASE WHEN json_valid(%s) THEN %s END)", expression, expression)
		if field.ArrayCaseSensitive {
			base = fmt.Sprintf("EXISTS (SELECT 1 FROM %s WHERE CAST(value AS TEXT) = ?)", elements)
			args = append(args, clause.Value)
		} else {
			base = fmt.Sprintf("EXISTS (SELECT 1 FROM %s WHERE lower(CAST(value AS TEXT)) = ?)", elements)
			args = append(args, strings.ToLower(clause.Value))
		}
	case "is_null":
		base = isNull
	case "is_not_null":
		base = "NOT " + isNull
	case "matches_regex", "not_matches_regex":
		registerSQLiteRegexp()
		base = fmt.Sprintf("(%s IS NOT NULL AND %s REGEXP ?)", expression, expression)
		if clause.Op == "not_matches_regex" {
			base = fmt.Sprintf("(%s IS NOT NULL AND NOT (%s REGEXP ?))", expression, expression)
		}
		args = append(args, clause.Value)
	case "contains":
		base = fmt.Sprintf("lower(COALESCE(%s,'')) LIKE ? ESCAPE '\\'", expression)
		args = append(args, likePattern(clause.Value, "%", "%"))
	case "starts_with":
		base = fmt.Sprintf("lower(COALESCE(%s,'')) LIKE ? ESCAPE '\\'", expression)
		args = append(args, likePattern(clause.Value, "", "%"))
	case "ends_with":
		base = fmt.Sprintf("lower(COALESCE(%s,'')) LIKE ? ESCAPE '\\'", expression)
		args = append(args, likePattern(clause.Value, "%", ""))
	case "length_gt", "length_lt", "length_eq":
		limit, err := querytable.ParseLength(clause.Value)
		if err != nil {
			return "", nil, fmt.Errorf("field %q: %w", clause.Field, err)
		}
		// SQLite's length counts a text value's characters; NULL and non-text
		// values have no length, as in querytable.Apply.
		comparison := map[string]string{"length_gt": ">", "length_lt": "<", "length_eq": "="}[clause.Op]
		base = fmt.Sprintf("(typeof(%s) = 'text' AND length(%s) %s ?)", expression, expression, comparison)
		args = append(args, limit)
	case "=", "!=", ">", ">=", "<", "<=":
		var value any = clause.Value
		switch field.Kind {
		case querytable.Number:
			var number float64
			if _, err := fmt.Sscan(clause.Value, &number); err != nil {
				return "", nil, fmt.Errorf("field %q: not a number: %q", clause.Field, clause.Value)
			}
			value = number
		case querytable.Bool:
			switch strings.ToLower(clause.Value) {
			case "true", "1", "t":
				value = 1
			case "false", "0", "f":
				value = 0
			default:
				return "", nil, fmt.Errorf("field %q: not a bool: %q", clause.Field, clause.Value)
			}
		case querytable.Datetime:
			parsed, ok := parseTime(clause.Value)
			if !ok {
				return "", nil, fmt.Errorf("field %q: not a datetime: %q", clause.Field, clause.Value)
			}
			value = formatTime(parsed)
		}
		if clause.Op == "!=" {
			base = fmt.Sprintf("(%s IS NULL OR %s <> ?)", expression, expression)
		} else {
			base = fmt.Sprintf("%s %s ?", expression, clause.Op)
		}
		args = append(args, value)
	default:
		return "", nil, fmt.Errorf("unknown op %q", clause.Op)
	}
	if clause.Negated {
		return fmt.Sprintf("(NOT %s AND NOT (%s))", isNull, base), args, nil
	}
	return base, args, nil
}

func (d sqlDataset) where(terms []querytable.WhereTerm, schema querytable.Schema) (string, []any, error) {
	var err error
	terms, err = querytable.ResolveRelativeWhere(terms, schema, time.Now())
	if err != nil {
		return "", nil, err
	}
	parts := []string{}
	args := []any{}
	if d.base != "" {
		parts = append(parts, d.base)
	}
	for _, term := range terms {
		lookup := false
		alternatives := []string{}
		for _, clause := range term.Predicates() {
			sqlText, clauseArgs, err := d.clause(clause, schema.Fields[clause.Field])
			if err != nil {
				return "", nil, err
			}
			if d.toolSearchIndexed && !clause.Negated {
				if term := toolSearchTerm(clause.Field, clause.Op, clause.Value); term != "" {
					sqlText = "(" + toolSearchPredicate(term) + " AND " + sqlText + ")"
					clauseArgs = append([]any{term}, clauseArgs...)
					lookup = true
				}
			}
			if column, ok := d.buckets[clause.Field]; ok && clause.Op == "=" && !clause.Negated {
				if from, to, ok := bucketRange(clause.Field, clause.Value); ok {
					sqlText = "(" + sqlText + " AND " + column + ">=? AND " + column + "<?)"
					clauseArgs = append(clauseArgs, from, to)
				}
			}
			alternatives = append(alternatives, sqlText)
			args = append(args, clauseArgs...)
			lookup = lookup || d.lookups[clause.Field]
		}
		// Without statistics SQLite takes a filter on a column it has no index
		// for to keep few rows, and reads and sorts every match to order a page.
		// likely() says it keeps most, so a page walks the index its order uses
		// and stops once full. A lookup field's index finds its rows directly.
		condition := strings.Join(alternatives, " OR ")
		if !lookup {
			condition = "likely(" + condition + ")"
		}
		parts = append(parts, "("+condition+")")
	}
	if len(parts) == 0 {
		return "", args, nil
	}
	return " WHERE " + strings.Join(parts, " AND "), args, nil
}

func (d sqlDataset) selectList() string {
	names := make([]string, 0, len(d.columns))
	for name := range d.columns {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for index, name := range names {
		parts[index] = d.columns[name] + ` AS "` + name + `"`
	}
	return strings.Join(parts, ",")
}

// scopedRows returns, in ID order, every row of the dataset that also meets
// condition: an SQL condition over the dataset's aliases, and its arguments.
func (d sqlDataset) scopedRows(ctx context.Context, q queryer, condition string, args []any, idField string) ([]map[string]any, error) {
	where := condition
	if d.base != "" {
		where = d.base + " AND (" + condition + ")"
	}
	statement := "SELECT " + d.selectList() + " " + d.from + " WHERE " + where
	if id, ok := d.columns[idField]; ok {
		statement += " ORDER BY " + id
	}
	return queryMapsContext(ctx, q, statement, args...)
}

func (d sqlDataset) Rows(ctx context.Context, q queryer, query querytable.Query, schema querytable.Schema) (querytable.Result, error) {
	if err := schema.ValidateQuery(query); err != nil {
		return querytable.Result{}, err
	}
	where, args, err := d.where(query.Where, schema)
	if err != nil {
		return querytable.Result{}, err
	}
	count, countArgs := "SELECT COUNT(*) n "+d.from+where, args
	if d.cubeFilters(query.Where) {
		cubeWhere, cubeArgs, err := d.cube.table().where(query.Where, schema)
		if err != nil {
			return querytable.Result{}, err
		}
		count, countArgs = "SELECT COALESCE(SUM("+d.cube.count+"),0) n "+d.cube.from+cubeWhere, cubeArgs
	} else if d.countable(query.Where) {
		count = "SELECT COUNT(*) n " + d.countFrom + where
	}
	var total int
	countRows, err := queryMapsContext(ctx, q, count, countArgs...)
	if err != nil {
		return querytable.Result{}, err
	}
	if len(countRows) == 1 {
		total = int(integer(countRows[0]["n"]))
	}
	orders, orderArgs := []string{}, []any{}
	for _, order := range query.OrderBy {
		expression, err := d.expr(order.Field)
		if err != nil {
			return querytable.Result{}, err
		}
		kind := schema.Fields[order.Field].Kind
		if order.Extract != nil {
			registerSQLiteRegexp()
			numeric := 0
			if kind == querytable.Number {
				numeric = 1
			}
			expression = fmt.Sprintf("NULLIF(regexp_extract(?,%s,%d),'')", expression, numeric)
			orderArgs = append(orderArgs, order.Extract.Regex)
		}
		nulls := "LAST"
		if order.Nulls == "first" {
			nulls = "FIRST"
		}
		direction := "ASC"
		if order.Dir == "desc" {
			direction = "DESC"
		}
		// Numbers and times are never '', so they sort on the bare column,
		// which lets SQLite walk an index for the default newest-first page.
		if order.Extract == nil && kind != querytable.Number && kind != querytable.Datetime && kind != querytable.Bool {
			expression = "NULLIF(" + expression + ",'')"
		}
		orders = append(orders, fmt.Sprintf("%s %s NULLS %s", expression, direction, nulls))
	}
	if id, ok := d.columns[schema.IDField]; ok {
		orders = append(orders, id+" ASC")
	}
	statement := "SELECT " + d.selectList() + " " + d.from + where + " ORDER BY " + strings.Join(orders, ",") + " LIMIT ? OFFSET ?"
	rows, err := queryMapsContext(ctx, q, statement, append(append(args, orderArgs...), query.Limit, query.Offset)...)
	if err != nil {
		return querytable.Result{}, err
	}
	return querytable.Result{Rows: rows, Total: total}, nil
}

func (d sqlDataset) Distinct(ctx context.Context, q queryer, fieldName, search string, limit int, schema querytable.Schema) (querytable.DistinctResult, error) {
	if _, ok := d.cube.dimensionFor(fieldName); ok {
		// Daily summaries preserve this set of dimension values, including
		// absent values, with far fewer and narrower rows than the raw cube.
		if d.cube == toolCallCube && slices.Contains([]string{"day", "week", "month", "repository_name", "source_kind", "provider", "model", "effort", "model_family", "model_effort", "session_kind", "tool_name", "tool_category", "mcp_server", "mcp_method", "program", "subcommand", "command_name", "command_category", "activity", "operation_key", "operation_name"}, fieldName) {
			return toolRollupDataset.Distinct(ctx, q, fieldName, search, limit, schema)
		}
		return d.cube.table().Distinct(ctx, q, fieldName, search, limit, schema)
	}
	d = d.project([]string{fieldName})
	field, ok := schema.Fields[fieldName]
	if !ok || !field.Filterable {
		return querytable.DistinctResult{}, fmt.Errorf("unknown filter field %q", fieldName)
	}
	expression, err := d.expr(fieldName)
	if err != nil {
		return querytable.DistinctResult{}, err
	}
	if limit < 1 {
		limit = 50
	}
	limit = min(limit, 200)
	where, args, err := d.where(nil, schema)
	if err != nil {
		return querytable.DistinctResult{}, err
	}
	condition := " WHERE "
	if where != "" {
		condition = where + " AND "
	}
	filter := ""
	filterArgs := append([]any{}, args...)
	if search != "" {
		filter = fmt.Sprintf(" AND lower(CAST(%s AS TEXT)) LIKE ? ESCAPE '\\'", expression)
		filterArgs = append(filterArgs, likePattern(search, "%", "%"))
	}
	values, err := queryMapsContext(ctx, q, fmt.Sprintf("SELECT DISTINCT CAST(%s AS TEXT) value %s%s%s IS NOT NULL AND %s <> ''%s ORDER BY 1 LIMIT ?",
		expression, d.from, condition, expression, expression, filter), append(filterArgs, limit+1)...)
	if err != nil {
		return querytable.DistinctResult{}, err
	}
	result := querytable.DistinctResult{Values: []string{}}
	for _, row := range values {
		result.Values = append(result.Values, firstString(row["value"]))
	}
	if len(result.Values) > limit {
		result.Values, result.HasMore = result.Values[:limit], true
	}
	nulls, err := queryMapsContext(ctx, q, fmt.Sprintf("SELECT EXISTS(SELECT 1 %s%s(%s IS NULL OR %s = '')) has_null", d.from, condition, expression, expression), args...)
	if err == nil && len(nulls) == 1 {
		result.HasNull = integer(nulls[0]["has_null"]) != 0
	}
	return result, nil
}

// FieldStats reports each field's distinct non-null values, and the range of
// number and datetime fields, over the whole dataset in one scan.
func (d sqlDataset) FieldStats(ctx context.Context, q queryer, names []string, schema querytable.Schema) (map[string]querytable.FieldStat, error) {
	requested, err := querytable.StatFields(names, schema)
	if err != nil {
		return nil, err
	}
	if d.cube != nil {
		return d.cube.fieldStats(ctx, q, requested, schema)
	}
	fields, measures := []string{}, []string{}
	for _, name := range requested {
		expression, ok := d.columns[name]
		if !ok {
			continue // priced or derived after paging; no SQL values
		}
		index := len(fields)
		fields = append(fields, name)
		measures = append(measures, fmt.Sprintf("COUNT(DISTINCT NULLIF(%s,'')) AS d%d", expression, index))
		if kind := schema.Fields[name].Kind; kind == querytable.Number || kind == querytable.Datetime {
			measures = append(measures, fmt.Sprintf("MIN(NULLIF(%s,'')) AS lo%d,MAX(NULLIF(%s,'')) AS hi%d", expression, index, expression, index))
		}
	}
	result := make(map[string]querytable.FieldStat, len(fields))
	if len(fields) == 0 {
		return result, nil
	}
	where, args, err := d.where(nil, schema)
	if err != nil {
		return nil, err
	}
	rows, err := queryMapsContext(ctx, q, "SELECT "+strings.Join(measures, ",")+" "+d.from+where, args...)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return result, nil
	}
	for index, name := range fields {
		row := rows[0]
		result[name] = querytable.FieldStat{Distinct: querytable.Count(int(integer(row[fmt.Sprintf("d%d", index)]))),
			Min: row[fmt.Sprintf("lo%d", index)], Max: row[fmt.Sprintf("hi%d", index)]}
	}
	return result, nil
}

// fieldStats reports what the cube keeps of each field: the distinct values
// and range of a dimension, and the range of a measure. A measure's distinct
// values, and fields the cube does not keep, would take a scan of every row,
// so they are left out.
func (cube *sqlCube) fieldStats(ctx context.Context, q queryer, names []string, schema querytable.Schema) (map[string]querytable.FieldStat, error) {
	fields, measures := []string{}, []string{}
	for _, name := range names {
		kind := schema.Fields[name].Kind
		ranged := kind == querytable.Number || kind == querytable.Datetime
		index := len(fields)
		if expression, ok := cube.dimensions[name]; ok {
			measures = append(measures, fmt.Sprintf("COUNT(DISTINCT NULLIF(%s,'')) AS d%d", expression, index))
			if ranged {
				measures = append(measures, fmt.Sprintf("MIN(NULLIF(%s,'')) AS lo%d,MAX(NULLIF(%s,'')) AS hi%d", expression, index, expression, index))
			}
		} else if columns, ok := cube.measures[name]; ok && ranged {
			measures = append(measures, fmt.Sprintf("MIN(%s) AS lo%d,MAX(%s) AS hi%d", columns.min, index, columns.max, index))
		} else {
			continue
		}
		fields = append(fields, name)
	}
	result := make(map[string]querytable.FieldStat, len(fields))
	if len(fields) == 0 {
		return result, nil
	}
	rows, err := queryMapsContext(ctx, q, "SELECT "+strings.Join(measures, ",")+" "+cube.from)
	if err != nil || len(rows) != 1 {
		return result, err
	}
	for index, name := range fields {
		row, stat := rows[0], querytable.FieldStat{}
		if distinct, ok := row[fmt.Sprintf("d%d", index)]; ok {
			stat.Distinct = querytable.Count(int(integer(distinct)))
		}
		stat.Min, stat.Max = row[fmt.Sprintf("lo%d", index)], row[fmt.Sprintf("hi%d", index)]
		result[name] = stat
	}
	return result, nil
}
