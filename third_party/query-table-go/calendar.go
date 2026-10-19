package querytable

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type MetricDateGrouping struct {
	Field        string `json:"field"`
	Interval     string `json:"interval"`
	TimeZone     string `json:"timeZone,omitempty"`
	IncludeEmpty bool   `json:"includeEmpty"`
	Range        string `json:"range"`
	Start        string `json:"start,omitempty"`
	End          string `json:"end,omitempty"`
}

var calendarDatePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(?:T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2}))?$`)

func calendarLocation(config MetricDateGrouping) (*time.Location, error) {
	zone := config.TimeZone
	if zone == "" {
		zone = "UTC"
	}
	if len(zone) > 80 || strings.HasPrefix(zone, "+") || strings.HasPrefix(zone, "-") {
		return nil, diagnostic("date_grouping", "use an IANA grouping timezone")
	}
	if !containsString([]string{"hour", "day", "week", "month", "year"}, config.Interval) || !containsString([]string{"query", "observed", "custom"}, config.Range) || config.Field == "" || len(config.Field) > 256 {
		return nil, diagnostic("date_grouping", "invalid date grouping definition")
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return nil, diagnostic("date_grouping", "use an IANA grouping timezone")
	}
	return location, nil
}

func calendarLocalWall(instant time.Time, loc *time.Location) time.Time {
	local := instant.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(), local.Second(), local.Nanosecond(), time.UTC)
}

func calendarOffsetChanges(at time.Time, loc *time.Location) bool {
	_, before := at.Add(-time.Nanosecond).In(loc).Zone()
	_, after := at.In(loc).Zone()
	return before != after
}

// Resolve wall midnight using zone intervals rather than time.Date's unspecified
// choice at overlaps. Gaps use the first valid instant, matching startOfDay.
func calendarWall(wall time.Time, loc *time.Location, later bool) (time.Time, error) {
	from, to := wall.Add(-72*time.Hour), wall.Add(72*time.Hour)
	matches := []time.Time{}
	seen := map[int]bool{}
	var gap time.Time
	for cursor := from; cursor.Before(to); {
		local := cursor.In(loc)
		_, offset := local.Zone()
		start, end := local.ZoneBounds()
		if !seen[offset] {
			seen[offset] = true
			candidate := wall.Add(-time.Duration(offset) * time.Second)
			// ZoneBounds includes synthetic UTC year boundaries for POSIX
			// extensions. Validate against the actual offset at the candidate.
			if calendarLocalWall(candidate, loc).Equal(wall) {
				matches = append(matches, candidate)
			}
		}
		if !start.IsZero() && calendarOffsetChanges(start, loc) {
			before := calendarLocalWall(start.Add(-time.Nanosecond), loc).Add(time.Nanosecond)
			after := calendarLocalWall(start, loc)
			if !wall.Before(before) && wall.Before(after) && (gap.IsZero() || start.Before(gap)) {
				gap = start
			}
		}
		if !end.IsZero() && !end.After(cursor) {
			// Go's POSIX extension uses a 365-day year end, which can be
			// behind cursor on a leap year's final day. Continue at UTC Jan 1.
			end = time.Date(cursor.UTC().Year()+1, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		if end.IsZero() || !end.Before(to) {
			break
		}
		cursor = end
	}
	if len(matches) > 0 {
		sort.Slice(matches, func(i, j int) bool { return matches[i].Before(matches[j]) })
		if later {
			return matches[len(matches)-1], nil
		}
		return matches[0], nil
	}
	if !gap.IsZero() {
		return gap, nil
	}
	return time.Time{}, diagnostic("date_grouping", "unresolvable calendar boundary")
}
func calendarFloor(instant time.Time, interval string, loc *time.Location) (time.Time, error) {
	local := instant.In(loc)
	_, offset := local.Zone()
	if interval == "hour" {
		wall := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), 0, 0, 0, time.UTC)
		floor := wall.Add(-time.Duration(offset) * time.Second)
		start, _ := local.ZoneBounds()
		if !start.IsZero() && start.After(floor) && calendarOffsetChanges(start, loc) {
			floor = start
		}
		return floor.UTC(), nil
	}
	y, m, day := local.Date()
	wall := time.Date(y, m, day, 0, 0, 0, 0, time.UTC)
	if interval == "week" {
		wall = wall.AddDate(0, 0, -((int(local.Weekday()) + 6) % 7))
	}
	if interval == "month" {
		wall = time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
	}
	if interval == "year" {
		wall = time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return calendarWall(wall, loc, false)
}
func calendarNext(instant time.Time, interval string, loc *time.Location) (time.Time, error) {
	local := instant.In(loc)
	y, m, day := local.Date()
	wall := time.Date(y, m, day, 0, 0, 0, 0, time.UTC)
	if interval == "hour" {
		wall = time.Date(y, m, day, local.Hour()+1, 0, 0, 0, time.UTC)
		next, err := calendarWall(wall, loc, false)
		if err != nil {
			return next, err
		}
		if !next.After(instant) {
			next, err = calendarWall(wall, loc, true)
			if err != nil {
				return next, err
			}
		}
		_, transition := local.ZoneBounds()
		if !transition.IsZero() && transition.After(instant) && transition.Before(next) && calendarOffsetChanges(transition, loc) {
			next = transition
		}
		return next.UTC(), nil
	}
	switch interval {
	case "day":
		wall = wall.AddDate(0, 0, 1)
	case "week":
		wall = wall.AddDate(0, 0, 7)
	case "month":
		wall = time.Date(y, m+1, 1, 0, 0, 0, 0, time.UTC)
	case "year":
		wall = time.Date(y+1, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return calendarWall(wall, loc, false)
}
func calendarInput(value string, loc *time.Location) (time.Time, error) {
	if !calendarDatePattern.MatchString(value) {
		return time.Time{}, diagnostic("date_grouping", "use ISO dates or timestamps with an explicit offset")
	}
	if len(value) == 10 {
		date, err := time.Parse("2006-01-02", value)
		if err != nil {
			return date, diagnostic("date_grouping", "invalid calendar date")
		}
		return calendarWall(date, loc, false)
	}
	instant, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return instant, diagnostic("date_grouping", "invalid calendar instant")
	}
	return instant, nil
}
func calendarRange(config MetricDateGrouping, q MetricQuery, observed []time.Time, now time.Time, loc *time.Location) (time.Time, time.Time, bool, error) {
	var min, max time.Time
	for _, t := range observed {
		if min.IsZero() || t.Before(min) {
			min = t
		}
		if max.IsZero() || t.After(max) {
			max = t
		}
	}
	if config.Range == "observed" {
		return min, max, len(observed) > 0, nil
	}
	var start, end time.Time
	if config.Range == "custom" {
		var err error
		start, err = calendarInput(config.Start, loc)
		if err != nil {
			return start, end, false, err
		}
		end, err = calendarInput(config.End, loc)
		if err != nil {
			return start, end, false, err
		}
		if len(config.End) == 10 {
			end, err = calendarNext(end, "day", loc)
			if err != nil {
				return start, end, false, err
			}
			end = end.Add(-time.Millisecond)
		}
		if end.Before(start) {
			return start, end, false, diagnostic("date_grouping", "end date must be on or after start date")
		}
		start, err = calendarFloor(start, config.Interval, loc)
		if err != nil {
			return start, end, false, err
		}
		end, err = calendarFloor(end, config.Interval, loc)
		if err != nil {
			return start, end, false, err
		}
		if len(observed) > 0 && (start.After(min) || end.Before(max)) {
			return start, end, false, diagnostic("date_grouping", "custom date bounds must include all observed buckets")
		}
		return start, end, true, nil
	}
	for _, term := range q.Where {
		if term.Value == "" || term.IsGroup() || term.Negated || term.Field != config.Field || !containsString([]string{"=", ">", ">=", "<", "<="}, term.Op) {
			continue
		}
		resolved, err := ResolveRelativeDatetime(term.Value, now)
		if err != nil {
			return start, end, false, err
		}
		value, err := calendarInput(resolved, time.UTC)
		if err != nil {
			return start, end, false, err
		}
		if term.Op == ">" {
			value = value.Add(time.Millisecond)
		}
		if term.Op == "<" {
			value = value.Add(-time.Millisecond)
		}
		if term.Op == "=" || term.Op == ">" || term.Op == ">=" {
			if start.IsZero() || value.After(start) {
				start = value
			}
		}
		if term.Op == "=" || term.Op == "<" || term.Op == "<=" {
			if end.IsZero() || value.Before(end) {
				end = value
			}
		}
	}
	if start.IsZero() || end.IsZero() {
		return start, end, false, diagnostic("date_grouping", "include empty dates needs a bounded date filter, observed dates, or a custom range")
	}
	if end.Before(start) {
		return start, end, false, nil
	}
	start, err := calendarFloor(start, config.Interval, loc)
	if err != nil {
		return start, end, false, err
	}
	end, err = calendarFloor(end, config.Interval, loc)
	return start, end, true, err
}

func (d QueryDatasetV3) executeMetric(ctx context.Context, tx *sql.Tx, spec AggSpec, q MetricQuery, s Schema, o PlanOptions, total int64, maxGroups int, tables *[]string) (SQLiteMetricV2, error) {
	if spec.DateGrouping == nil {
		if err := d.checkMetricSamples(ctx, tx, spec, q, s, o, maxGroups); err != nil {
			return SQLiteMetricV2{}, err
		}
		p, err := compileMetricDialect(ctx, q, spec, s, o, d.Dialect == "sqlite")
		if err != nil {
			return SQLiteMetricV2{}, err
		}
		return (SQLiteV2Dataset{}).readV2Metric(ctx, tx, MetricSQLPlan{spec.ID, p}, spec, total, q, maxGroups)
	}
	config := *spec.DateGrouping
	loc, err := calendarLocation(config)
	if err != nil {
		return SQLiteMetricV2{}, err
	}
	field, ok := s.Fields[config.Field]
	if !ok || field.Kind != FieldDatetime || !fieldGroupable(field) || len(spec.GroupBy) == 0 || spec.GroupBy[0] != config.Field || spec.Distribution != nil {
		return SQLiteMetricV2{}, diagnostic("date_grouping", "calendar grouping requires the first datetime key and scalar metrics")
	}
	if _, err = metricOrder(spec, len(spec.GroupBy), spec.ExpressionY != "", false); err != nil {
		return SQLiteMetricV2{}, err
	}
	rowQuery := WireQuery{Where: q.Where, OrderBy: OrderBys(q.OrderBy)}
	if spec.Scope == "shownRows" {
		rowQuery.Limit = q.Limit
		rowQuery.Offset = q.Offset
	}
	// Select only this metric's expression, key, filter and window dependencies.
	// The source/result projection can include many unrelated, large values.
	plain := spec
	plain.DateGrouping = nil
	dependencyPlan, err := compileMetricDialect(ctx, q, plain, s, o, d.Dialect == "sqlite")
	if err != nil {
		return SQLiteMetricV2{}, err
	}
	names := append([]string{s.IDField, config.Field}, dependencyPlan.Dependencies...)
	sort.Strings(names)
	rowQuery.Select = uniqueStrings(names)
	_, _, maxPopulation, maxBytes, err := d.limits()
	if err != nil {
		return SQLiteMetricV2{}, err
	}
	var p SQLPlan
	if spec.Scope == "shownRows" {
		p, err = compileComputedRowsDialect(ctx, rowQuery, s, o, d.Dialect == "sqlite")
	} else {
		// An all-matching reduction has no row ordering semantics.
		rowQuery.OrderBy = nil
		rowQuery.Limit = maxPopulation + 1
		p, err = compileQuerySource(ctx, rowQuery, s, o, d.Dialect == "sqlite")
	}
	if err != nil {
		return SQLiteMetricV2{}, err
	}
	dateAlias := ""
	for _, column := range p.Columns {
		if column.Field == config.Field {
			dateAlias = column.ValueAlias
		}
	}
	// Include the ordinal and 24-byte ISO key in this metric's scratch budget.
	sizeSQL := d.stagingSizeSQL(p.Columns, "8", "CASE WHEN "+queryIdent(dateAlias)+" IS NULL THEN 0 ELSE 24 END")
	if err = d.checkCalendarStaging(ctx, tx, "("+p.SQL+") AS qt_calendar_budget", sizeSQL, p.Args, maxPopulation, maxBytes); err != nil {
		return SQLiteMetricV2{}, err
	}
	table := queryTableName()
	*tables = append(*tables, table)
	if _, err = tx.ExecContext(ctx, "CREATE TEMP TABLE "+queryIdent(table)+" AS SELECT *,ROW_NUMBER() OVER () AS qt_calendar_row FROM ("+p.SQL+") AS qt_calendar_source", p.Args...); err != nil {
		return SQLiteMetricV2{}, err
	}
	// A calendar scratch relation lives for one metric, rather than accumulating
	// with every calendar clause in the request. ExecuteIn also retains a fallback
	// cleanup entry for failures/cancelled transactions.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_, _ = tx.ExecContext(cleanup, "DROP TABLE IF EXISTS "+queryIdent(table))
	}()
	if err = d.checkCalendarStaging(ctx, tx, queryIdent(table), sizeSQL, nil, maxPopulation, maxBytes); err != nil {
		return SQLiteMetricV2{}, err
	}
	if _, err = tx.ExecContext(ctx, "CREATE UNIQUE INDEX "+queryIdent(table+"_index")+" ON "+queryIdent(table)+" (qt_calendar_row)"); err != nil {
		return SQLiteMetricV2{}, err
	}
	if _, err = tx.ExecContext(ctx, "ALTER TABLE "+queryIdent(table)+" ADD COLUMN qt_calendar_key TEXT"); err != nil {
		return SQLiteMetricV2{}, err
	}
	calendarSchema := Schema{Name: s.Name, IDField: s.IDField, Fields: map[string]FieldSpec{}}
	for _, column := range p.Columns {
		original := s.Fields[column.Field]
		original.Expr = "r." + queryIdent(column.ValueAlias)
		original.ValueErrorExpr = "r." + queryIdent(column.ErrorAlias)
		original.SortExpr = ""
		if original.Kind == FieldDatetime {
			original.SQLiteDatetimeFormat = "rfc3339"
		}
		calendarSchema.Fields[column.Field] = original
		if column.Field == config.Field {
			dateAlias = column.ValueAlias
		}
	}
	if err = d.keyCalendarRows(ctx, tx, table, dateAlias, config.Interval, loc); err != nil {
		return SQLiteMetricV2{}, err
	}
	bucketName := "__qt_calendar_bucket__"
	if _, exists := calendarSchema.Fields[bucketName]; exists {
		return SQLiteMetricV2{}, diagnostic("schema", "reserved calendar field collision")
	}
	// Text ISO keys avoid PostgreSQL interpreting a staged datetime string under
	// the connection timezone; original datetime inputs remain available to formulas.
	calendarSchema.Fields[bucketName] = resultField(bucketName, "text", "r.qt_calendar_key", true)
	calendarKey := calendarSchema.Fields[bucketName]
	calendarKey.ValueErrorExpr = calendarSchema.Fields[config.Field].ValueErrorExpr
	calendarSchema.Fields[bucketName] = calendarKey
	modified := spec
	modified.DateGrouping = nil
	modified.Scope = "allMatching"
	modified.Sort = nil
	modified.GroupLimit = 0
	// MinCount is applied below, after empty buckets are filled; the metric
	// filter runs over the staged rows (the page for shown-row scope).
	modified.MinCount = 0
	modified.GroupBy = append([]string{}, spec.GroupBy...)
	modified.GroupBy[0] = bucketName
	options := o
	options.SourceSQL = "SELECT * FROM " + queryIdent(table)
	options.SourceArgs = nil
	if err = d.checkMetricSamples(ctx, tx, modified, MetricQuery{Version: 2}, calendarSchema, options, maxGroups); err != nil {
		return SQLiteMetricV2{}, err
	}
	metricPlan, err := compileMetricDialect(ctx, MetricQuery{Version: 2}, modified, calendarSchema, options, d.Dialect == "sqlite")
	if err != nil {
		return SQLiteMetricV2{}, err
	}
	result, err := (SQLiteV2Dataset{}).readV2Metric(ctx, tx, MetricSQLPlan{spec.ID, metricPlan}, modified, total, q, maxGroups)
	if err != nil {
		return result, err
	}
	result.Scope = spec.Scope
	if result.Scope == "" {
		result.Scope = "allMatching"
	}
	if result.Scope == "shownRows" {
		result.ProcessedRows = total - int64(q.Offset)
		if result.ProcessedRows < 0 {
			result.ProcessedRows = 0
		}
		if result.ProcessedRows > int64(q.Limit) {
			result.ProcessedRows = int64(q.Limit)
		}
	}
	if config.IncludeEmpty {
		if err = fillCalendarBuckets(ctx, &result, config, spec, q, o.Now, loc, maxGroups); err != nil {
			return result, err
		}
	}
	if spec.MinCount > 0 {
		// Filled buckets (count 0) are never hidden. A hidden observed bucket
		// leaves a gap: it is not replaced by an empty bucket.
		kept := result.Buckets[:0]
		hidden := int64(0)
		for _, bucket := range result.Buckets {
			if bucket.Count > 0 && bucket.Count < int64(spec.MinCount) {
				hidden++
			} else {
				kept = append(kept, bucket)
			}
		}
		result.Buckets = kept
		result.HiddenGroups = &hidden
	}
	sortCalendarBuckets(result.Buckets, spec)
	result.GroupCount = int64(len(result.Buckets))
	if spec.GroupLimit > 0 && len(result.Buckets) > spec.GroupLimit {
		result.Buckets = result.Buckets[:spec.GroupLimit]
	}
	return result, nil
}
func fillCalendarBuckets(ctx context.Context, result *SQLiteMetricV2, config MetricDateGrouping, spec AggSpec, q MetricQuery, now time.Time, loc *time.Location, maxGroups int) error {
	observed := []time.Time{}
	series := map[string][]any{}
	present := map[string]bool{}
	for _, bucket := range result.Buckets {
		key, _ := jsonValues(bucket.Keys)
		present[key] = true
		if len(bucket.Keys) > 0 && bucket.Keys[0] != nil {
			date, err := calendarInput(fmt.Sprint(bucket.Keys[0]), time.UTC)
			if err != nil {
				return err
			}
			observed = append(observed, date)
			tuple := bucket.Keys[1:]
			key, _ = jsonValues(tuple)
			series[key] = tuple
		}
	}
	if len(spec.GroupBy) == 1 && len(series) == 0 {
		series["[]"] = []any{}
	}
	start, end, has, err := calendarRange(config, q, observed, now, loc)
	if err != nil {
		return err
	}
	if !has {
		return nil
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	// With multiple keys, only series observed at nonnull dates are filled.
	// Without any such series there is no work, regardless of the date range.
	if len(series) == 0 {
		return nil
	}
	empty := func(expression string) any {
		node, err := parseExpression(expression)
		if err == nil && containsString([]string{"COUNT", "COUNT_DISTINCT", "SUM"}, strings.ToUpper(node.op)) {
			return float64(0)
		}
		return nil
	}
	for at := start; !at.After(end); {
		if err = ctx.Err(); err != nil {
			return err
		}
		for _, tuple := range series {
			keys := append([]any{at.UTC().Format("2006-01-02T15:04:05.000Z")}, tuple...)
			key, _ := jsonValues(keys)
			if !present[key] {
				if len(result.Buckets) >= maxGroups {
					return diagnostic("resource_limit", "calendar group population exceeds host budget")
				}
				bucket := SQLiteMetricV2Bucket{Keys: keys, Count: 0, Value: empty(MetricExpression(spec))}
				if spec.ExpressionY != "" {
					value := empty(spec.ExpressionY)
					bucket.Y = &value
				}
				result.Buckets = append(result.Buckets, bucket)
				present[key] = true
			}
		}
		next, e := calendarNext(at, config.Interval, loc)
		if e != nil {
			return e
		}
		if !next.After(at) {
			return diagnostic("date_grouping", "calendar boundaries must advance")
		}
		at = next
	}
	return nil
}
func (d QueryDatasetV3) checkMetricSamples(ctx context.Context, tx *sql.Tx, spec AggSpec, q MetricQuery, s Schema, o PlanOptions, maxGroups int) error {
	if spec.Distribution == nil && !sampledExpression(MetricExpression(spec)) && !sampledExpression(spec.ExpressionY) {
		return nil
	}
	probe := AggSpec{ID: "__qt_samples__", Op: "count", GroupBy: spec.GroupBy, Scope: spec.Scope, Where: spec.Where}
	p, err := compileMetricDialect(ctx, q, probe, s, o, d.Dialect == "sqlite")
	if err != nil {
		return err
	}
	metric, err := (SQLiteV2Dataset{}).readV2Metric(ctx, tx, MetricSQLPlan{probe.ID, p}, probe, 0, q, maxGroups)
	if err != nil {
		return err
	}
	limit, err := d.sampleLimit()
	if err != nil {
		return err
	}
	for _, bucket := range metric.Buckets {
		if bucket.Count > int64(limit) {
			return diagnostic("resource_limit", sampleBudgetMessage(spec, limit))
		}
	}
	return nil
}
func jsonValues(values []any) (string, error) {
	pairs := [][]any{}
	for i, value := range values {
		pairs = append(pairs, []any{strconv.Itoa(i), value})
	}
	return jsonTuple(pairs), nil
}
func compareQueryValues(a, b any) int {
	if a == nil {
		if b == nil {
			return 0
		}
		return 1
	}
	if b == nil {
		return -1
	}
	if left, ok := a.(float64); ok {
		right, _ := b.(float64)
		if left < right {
			return -1
		}
		if left > right {
			return 1
		}
		return 0
	}
	if left, ok := a.(int64); ok {
		right, _ := b.(int64)
		if left < right {
			return -1
		}
		if left > right {
			return 1
		}
		return 0
	}
	if left, ok := a.(bool); ok {
		right, _ := b.(bool)
		if left == right {
			return 0
		}
		if !left {
			return -1
		}
		return 1
	}
	left, right := []rune(fmt.Sprint(a)), []rune(fmt.Sprint(b))
	for i := 0; i < len(left) && i < len(right); i++ {
		if left[i] < right[i] {
			return -1
		}
		if left[i] > right[i] {
			return 1
		}
	}
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return 0
}
func sortCalendarBuckets(buckets []SQLiteMetricV2Bucket, spec AggSpec) {
	ordering := spec.Sort
	if len(ordering) == 0 {
		ordering = []MetricSort{{Key: "group0", Dir: "asc", Nulls: "last"}}
	}
	target := func(bucket SQLiteMetricV2Bucket, sort MetricSort) any {
		switch sort.Key {
		case "value":
			return bucket.Value
		case "count":
			return bucket.Count
		case "y":
			if bucket.Y != nil {
				return *bucket.Y
			}
			return nil
		}
		if strings.HasPrefix(sort.Key, "group") {
			index, _ := strconv.Atoi(sort.Key[5:])
			if index < len(bucket.Keys) {
				return bucket.Keys[index]
			}
		}
		return nil
	}
	sort.SliceStable(buckets, func(i, j int) bool {
		a, b := buckets[i], buckets[j]
		badA, badB := a.Error != "" || a.YError != "", b.Error != "" || b.YError != ""
		if badA != badB {
			return !badA
		}
		for _, s := range ordering {
			left, right := target(a, s), target(b, s)
			cmp := compareQueryValues(left, right)
			if left == nil || right == nil {
				if s.Nulls == "first" {
					cmp = -cmp
				}
			} else if s.Dir == "desc" {
				cmp = -cmp
			}
			if cmp != 0 {
				return cmp < 0
			}
		}
		for k := range a.Keys {
			if cmp := compareQueryValues(a.Keys[k], b.Keys[k]); cmp != 0 {
				return cmp < 0
			}
		}
		return false
	})
}

// Read and write bounded batches with the cursor closed between them. Neither
// Go memory nor SQLite/PostgreSQL statement count grows by one per source row.
func (d QueryDatasetV3) keyCalendarRows(ctx context.Context, tx *sql.Tx, table, dateAlias, interval string, loc *time.Location) error {
	const batchSize = 400 // Two parameters per key fit SQLite's legacy 999 limit.
	var after int64
	for {
		rows, err := tx.QueryContext(ctx, "SELECT qt_calendar_row,"+queryIdent(dateAlias)+" FROM "+queryIdent(table)+" WHERE qt_calendar_row>"+d.param(1)+" ORDER BY qt_calendar_row LIMIT "+strconv.Itoa(batchSize), after)
		if err != nil {
			return err
		}
		values := []string{}
		args := []any{}
		scanned := 0
		for rows.Next() {
			var id int64
			var raw any
			if err = rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return err
			}
			scanned++
			after = id
			if raw == nil {
				continue
			}
			instant, e := calendarInput(fmt.Sprint(typedQueryValue(raw, "datetime")), time.UTC)
			if e != nil {
				rows.Close()
				return e
			}
			floor, e := calendarFloor(instant, interval, loc)
			if e != nil {
				rows.Close()
				return e
			}
			values = append(values, "(CAST("+d.param(len(args)+1)+" AS bigint),CAST("+d.param(len(args)+2)+" AS text))")
			args = append(args, id, floor.Format("2006-01-02T15:04:05.000Z"))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(values) > 0 {
			update := "WITH qt_calendar_keys(ordinal,bucket) AS (VALUES " + strings.Join(values, ",") + ") UPDATE " + queryIdent(table) + " AS target SET qt_calendar_key=keys.bucket FROM qt_calendar_keys AS keys WHERE target.qt_calendar_row=keys.ordinal"
			if _, err = tx.ExecContext(ctx, update, args...); err != nil {
				return err
			}
		}
		if scanned < batchSize {
			return nil
		}
	}
}
