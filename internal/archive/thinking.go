package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	qt "github.com/Pythia-Software/query-table/backends/go"
	"github.com/gbdubs/pharos/internal/querytable"
	"hash/fnv"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

var thinkingEdges = []float64{.1, .2, .3, .5, 1, 2, 3, 5, 10, 20, 30, 50, 100, 200, 300, 500, 600}

type thinkingOptions struct {
	FilterSQL                                                                  string
	FilterArgs                                                                 []any
	NativeWhere                                                                []querytable.WhereTerm
	View, GroupBy, Model, Effort, Period, Rows, Metric, Sort, Direction, Range string
	From, To                                                                   time.Time
	Zone                                                                       *time.Location
	Minimum, Hours, Days, HourA, HourB                                         int
	Overview, Evidence                                                         bool
}

func thinkingChoice(q url.Values, key, fallback string, values ...string) (string, error) {
	s := q.Get(key)
	if s == "" {
		return fallback, nil
	}
	for _, v := range values {
		if s == v {
			return s, nil
		}
	}
	return "", fmt.Errorf("invalid %s", key)
}
func readThinkingOptions(q url.Values, now time.Time) (thinkingOptions, error) {
	o := thinkingOptions{Model: q.Get("model"), Effort: q.Get("effort"), Overview: q.Get("overview") == "1", Evidence: q.Get("evidence") == "1"}
	var err error
	for _, v := range []struct {
		key, def string
		target   *string
		values   []string
	}{
		{"view", "time", &o.View, []string{"time", "heatmap", "histogram", "reasoning", "wait-box", "calendar"}},
		{"groupBy", "none", &o.GroupBy, []string{"none", "model", "model_effort", "provider", "effort", "harness", "repository", "machine", "preceding_tool", "agent_depth"}},
		{"period", "day", &o.Period, []string{"hour", "hours", "day", "days", "week", "month"}},
		{"rows", "day", &o.Rows, []string{"day", "week", "month"}}, {"metric", "raw", &o.Metric, []string{"raw", "ratio"}},
		{"sort", "median", &o.Sort, []string{"median", "p95", "volume"}}, {"direction", "desc", &o.Direction, []string{"asc", "desc"}},
		{"range", "all", &o.Range, []string{"all", "today", "1d", "7d", "30d", "90d", "6m", "1y", "custom"}},
	} {
		*v.target, err = thinkingChoice(q, v.key, v.def, v.values...)
		if err != nil {
			return o, err
		}
	}
	if token := q.Get("filters"); token != "" {
		query, e := qt.DecodeQueryLink(token)
		if e != nil || query.Grouping != nil {
			return o, fmt.Errorf("invalid Thinking population filters")
		}
		document, e := querySchemaDocument("model_requests")
		if e != nil {
			return o, e
		}
		schema, e := querytable.LoadSchema(document)
		if e != nil {
			return o, e
		}
		o.NativeWhere = query.Where
		wire := querytable.Query{Where: query.Where, Limit: 1}
		if e = wire.Validate(); e != nil {
			return o, e
		}
		o.FilterSQL, o.FilterArgs, e = thinkingRequestDataset.where(query.Where, schema)
		if e != nil {
			return o, e
		}
	}
	o.Zone = time.Local
	if zone := q.Get("timezone"); zone != "" {
		o.Zone, err = time.LoadLocation(zone)
		if err != nil {
			return o, fmt.Errorf("invalid timezone")
		}
	}
	for _, v := range []struct {
		key           string
		target        *int
		def, min, max int
	}{{"minimum", &o.Minimum, 0, 0, 1000000}, {"hours", &o.Hours, 6, 1, 10000}, {"days", &o.Days, 3, 1, 10000}, {"hourA", &o.HourA, 18, 0, 21}, {"hourB", &o.HourB, 9, 0, 21}} {
		*v.target = v.def
		if raw := q.Get(v.key); raw != "" {
			*v.target, err = strconv.Atoi(raw)
			if err != nil || *v.target < v.min || *v.target > v.max {
				return o, fmt.Errorf("invalid %s", v.key)
			}
		}
	}
	if o.HourA%3 != 0 || o.HourB%3 != 0 {
		return o, fmt.Errorf("hour windows must start at multiples of three")
	}
	if len(o.Model) > 300 || len(o.Effort) > 100 {
		return o, fmt.Errorf("filter value too long")
	}
	if o.Range == "custom" {
		for _, v := range []struct {
			key string
			t   *time.Time
		}{{"from", &o.From}, {"to", &o.To}} {
			if s := q.Get(v.key); s != "" {
				*v.t, err = time.Parse(time.RFC3339Nano, s)
				if err != nil {
					return o, fmt.Errorf("invalid %s timestamp", v.key)
				}
			}
		}
		if !o.From.IsZero() && !o.To.IsZero() && !o.From.Before(o.To) {
			return o, fmt.Errorf("start must be before end")
		}
	} else if o.Range != "all" {
		o.To = now
		days := map[string]int{"1d": 1, "7d": 7, "30d": 30, "90d": 90, "6m": 180, "1y": 365}[o.Range]
		o.From = now.Add(-time.Duration(days) * 24 * time.Hour)
		if o.Range == "today" {
			d := now.In(o.Zone)
			o.From = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, o.Zone)
		}
	}
	return o, nil
}

// Validate portable host view state using the same option domains as execution.
func validateThinkingView(value map[string]any) error {
	if value == nil {
		return fmt.Errorf("needs Thinking dashboard state")
	}
	names := map[string]string{"view": "view", "groupBy": "groupBy", "model": "model", "effort": "effort", "minimum": "minimum", "period": "period", "hours": "hours", "days": "days", "range": "range", "from": "from", "to": "to", "calendarRows": "rows", "calendarMetric": "metric", "hourA": "hourA", "hourB": "hourB", "sortBy": "sort", "sortDirection": "direction", "timezone": "timezone"}
	q := url.Values{}
	for key, item := range value {
		switch key {
		case "hiddenPercentiles":
			items, ok := item.([]any)
			if !ok {
				return fmt.Errorf("invalid hiddenPercentiles")
			}
			for _, v := range items {
				if v != "median" && v != "p75" && v != "p90" && v != "p95" {
					return fmt.Errorf("invalid hiddenPercentiles")
				}
			}
		case "normalization":
			if item != "share" && item != "count" {
				return fmt.Errorf("invalid normalization")
			}
		case "traceScale":
			if item != "linear" && item != "log" {
				return fmt.Errorf("invalid traceScale")
			}
		case "session":
			if text, ok := item.(string); !ok || jsTextLength(text) > 500 {
				return fmt.Errorf("invalid session")
			}
		case "minimum", "hours", "days", "hourA", "hourB":
			n, ok := item.(float64)
			if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) {
				return fmt.Errorf("invalid %s", key)
			}
			q.Set(names[key], strconv.FormatFloat(n, 'f', 0, 64))
		default:
			name, ok := names[key]
			if !ok {
				return fmt.Errorf("unknown Thinking control %s", key)
			}
			text, ok := item.(string)
			if !ok {
				return fmt.Errorf("invalid %s", key)
			}
			if key == "from" || key == "to" {
				if _, err := time.Parse(time.RFC3339Nano, text); err != nil {
					return fmt.Errorf("invalid %s", key)
				}
			}
			if text == "" && key != "model" && key != "effort" && key != "timezone" {
				return fmt.Errorf("invalid %s", key)
			}
			q.Set(name, text)
		}
	}
	if q.Get("from") != "" && q.Get("to") != "" {
		a, _ := time.Parse(time.RFC3339Nano, q.Get("from"))
		b, _ := time.Parse(time.RFC3339Nano, q.Get("to"))
		if !a.Before(b) {
			return fmt.Errorf("start must be before end")
		}
	}
	_, err := readThinkingOptions(q, time.Now())
	return err
}

type thinkingRecord struct {
	ID, Conversation, Stream, Workspace, Model, Effort, Provider, Harness, Repository, Machine, Preceding, Quality, Previous, Native string
	ObservedMillis                                                                                                                   sql.NullInt64
	At                                                                                                                               time.Time
	Sequence, Depth                                                                                                                  int
	Input, Cache, Output, Reasoning                                                                                                  float64
	Gap, Ratio                                                                                                                       *float64
	Compaction                                                                                                                       bool
}
type thinkingStats struct {
	N      int      `json:"n"`
	Median *float64 `json:"median"`
	P75    *float64 `json:"p75"`
	P90    *float64 `json:"p90"`
	P95    *float64 `json:"p95"`
	Q1     *float64 `json:"q1"`
	Q3     *float64 `json:"q3"`
	Low    *float64 `json:"low"`
	High   *float64 `json:"high"`
}

func thinkingQuantile(a []float64, p float64) *float64 {
	if len(a) == 0 {
		return nil
	}
	at := p * float64(len(a)-1)
	lo := int(at)
	v := a[lo] + (a[int(math.Ceil(at))]-a[lo])*(at-float64(lo))
	return &v
}
func thinkingSummarize(a []float64) thinkingStats {
	sort.Float64s(a)
	return thinkingStats{len(a), thinkingQuantile(a, .5), thinkingQuantile(a, .75), thinkingQuantile(a, .9), thinkingQuantile(a, .95), thinkingQuantile(a, .25), thinkingQuantile(a, .75), thinkingQuantile(a, .1), thinkingQuantile(a, .9)}
}
func thinkingMedian(a []float64) float64 {
	sort.Float64s(a)
	if v := thinkingQuantile(a, .5); v != nil {
		return *v
	}
	return 0
}

type thinkingCell struct {
	Stats         thinkingStats `json:"stats"`
	Retained      int           `json:"retained"`
	Conversations int           `json:"conversations"`
	Counts        []int         `json:"counts"`
}
type thinkingCalendarCell struct {
	Day                                               string `json:"day"`
	Hour                                              int    `json:"hour"`
	Raw, Ratio                                        *float64
	N, Adjusted, Conversations, AdjustedConversations int
}
type thinkingBand struct {
	Hour             int `json:"hour"`
	Days             int `json:"days"`
	N                int `json:"n"`
	Ratio, Low, High *float64
	Daily            map[string]float64 `json:"-"`
}
type thinkingComparison struct {
	Days             int `json:"days"`
	Ratio, Low, High *float64
}
type thinkingGroup struct {
	Key                                                             string `json:"key"`
	Label                                                           string `json:"label"`
	Model                                                           string `json:"model"`
	Effort                                                          string `json:"effort"`
	Retained, Timed, Adjusted, Conversations, RetainedConversations int
	Stats                                                           thinkingStats           `json:"stats"`
	Counts                                                          []int                   `json:"counts"`
	Periods                                                         map[string]thinkingCell `json:"periods"`
	Calendar                                                        []thinkingCalendarCell  `json:"calendar"`
	Bands                                                           []thinkingBand          `json:"bands"`
	Comparison                                                      thinkingComparison      `json:"comparison"`
	CDF                                                             [][2]float64            `json:"cdf,omitempty"`
}
type thinkingBounds struct{ From, To string }
type thinkingResponse struct {
	PeriodBounds                                     map[string]thinkingBounds `json:"periodBounds"`
	CalendarBounds                                   map[string]thinkingBounds `json:"calendarBounds"`
	Retained, Timed, Adjusted, Conversations, Hidden int
	Edges                                            []float64       `json:"edges"`
	Groups                                           []thinkingGroup `json:"groups"`
	Models, Efforts                                  []string
	Summary                                          thinkingStats `json:"summary"`
	Periods                                          []string      `json:"periods"`
	CalendarPeriods                                  []string      `json:"calendarPeriods"`
	From, To                                         string
	Timezone                                         string               `json:"timezone"`
	AgentCoverage                                    []thinkingGroup      `json:"agentCoverage,omitempty"`
	Coverage                                         []thinkingGroup      `json:"coverage,omitempty"`
	Factors                                          []thinkingFactor     `json:"factors,omitempty"`
	Compactions                                      []thinkingCompaction `json:"compactions,omitempty"`
	Traces                                           []thinkingTrace      `json:"traces,omitempty"`
	Validation                                       *thinkingValidation  `json:"validation,omitempty"`
}

func thinkingStamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
func thinkingWhere(o thinkingOptions) (string, []any) {
	terms := []string{"1=1"}
	args := []any{}
	for _, v := range []struct {
		field, op string
		t         time.Time
	}{{"requested_at", ">=", o.From}, {"requested_at", "<", o.To}} {
		if !v.t.IsZero() {
			terms = append(terms, "unixepoch(d."+v.field+",'subsec')"+v.op+"?")
			args = append(args, float64(v.t.UnixMilli())/1000)
		}
	}
	for _, v := range []struct{ field, value string }{{"model", o.Model}, {"effort", o.Effort}} {
		if v.value != "" && v.value != "all" {
			terms = append(terms, "COALESCE(NULLIF(d."+v.field+",''),'Unreported')=?")
			args = append(args, v.value)
		}
	}
	if o.FilterSQL != "" {
		terms = append(terms, "("+strings.TrimPrefix(o.FilterSQL, " WHERE ")+")")
		args = append(args, o.FilterArgs...)
	}
	return strings.Join(terms, " AND "), args
}
func (c *Catalog) thinkingRecords(ctx context.Context, o thinkingOptions) ([]thinkingRecord, error) {
	where, args := thinkingWhere(o)
	// Window/identity/tool evidence uses temporary B-trees. Lease the shared
	// bounded scratch cache, with disk spill, rather than SQLite's zero-page
	// default. Settings are restored before returning this reader connection.
	tx, release, err := beginQueryWithTempStore(ctx, c.reader(), 1)
	if err != nil {
		return nil, err
	}
	defer release()
	// Stream directly into the quantitative records. A million intermediate maps
	// retain several gigabytes and make a full-library read spend its budget in GC.
	capacity := 4096
	if o.FilterSQL == "" && o.From.IsZero() && o.To.IsZero() && (o.Model == "" || o.Model == "all") && (o.Effort == "" || o.Effort == "all") {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM model_requests").Scan(&count); err != nil {
			return nil, err
		}
		capacity = min(count, 2_000_000)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,conversation_id,COALESCE(agent_session_id,''),workspace_id,model,effort,
 COALESCE(provider,''),COALESCE(harness,''),COALESCE(repository_name,''),COALESCE(machine,''),preceding_tool,timing_quality,
 COALESCE(previous_request_id,''),COALESCE(native_id,''),COALESCE(requested_at,''),observed_millis,sequence,COALESCE(agent_depth,0),input_tokens,
 cache_read_input_tokens,output_tokens,reasoning_output_tokens,gap_seconds,compacted_before FROM (`+thinkingSource+`) d WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]thinkingRecord, 0, capacity)
	for rows.Next() {
		var r thinkingRecord
		var at string
		var gap sql.NullFloat64
		var compacted int
		if err := rows.Scan(&r.ID, &r.Conversation, &r.Stream, &r.Workspace, &r.Model, &r.Effort, &r.Provider, &r.Harness, &r.Repository, &r.Machine, &r.Preceding, &r.Quality, &r.Previous, &r.Native, &at, &r.ObservedMillis, &r.Sequence, &r.Depth, &r.Input, &r.Cache, &r.Output, &r.Reasoning, &gap, &compacted); err != nil {
			return nil, err
		}
		r.At, _ = time.Parse(time.RFC3339Nano, at)
		r.Compaction = compacted != 0
		r.Model = thinkingLabel(r.Model)
		r.Effort = thinkingLabel(r.Effort)
		r.Provider = thinkingLabel(r.Provider)
		r.Harness = thinkingLabel(r.Harness)
		r.Repository = thinkingLabel(r.Repository)
		r.Machine = thinkingLabel(r.Machine)
		if gap.Valid {
			r.Gap = &gap.Float64
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func thinkingLabel(s string) string {
	if strings.TrimSpace(s) == "" {
		return "Unreported"
	}
	return s
}
func thinkingBin(v float64) int {
	i := sort.SearchFloat64s(thinkingEdges, v)
	if i == len(thinkingEdges) {
		return -1
	}
	if thinkingEdges[i] == v {
		return min(i, len(thinkingEdges)-2)
	}
	return i - 1
}

// Resolve a civil grid boundary to a real instant. A skipped boundary starts
// at the clock transition; a repeated boundary starts at its first occurrence.
func thinkingCivilHour(y int, m time.Month, d, hour int, zone *time.Location) time.Time {
	wall := time.Date(y, m, d, hour, 0, 0, 0, time.UTC)
	at := time.Date(y, m, d, hour, 0, 0, 0, zone)
	civil := func(t time.Time) time.Time {
		t = t.In(zone)
		y, m, d := t.Date()
		return time.Date(y, m, d, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
	}
	start, end := at.ZoneBounds()
	if !civil(at).Equal(wall) {
		for _, transition := range []time.Time{start, end} {
			if !transition.IsZero() && civil(transition.Add(-time.Nanosecond)).Before(wall) && !civil(transition).Before(wall) {
				return transition.In(zone)
			}
		}
	}
	if !start.IsZero() {
		_, previous := start.Add(-time.Nanosecond).Zone()
		_, current := at.Zone()
		if previous > current {
			earlier := at.Add(-time.Duration(previous-current) * time.Second)
			if civil(earlier).Equal(wall) {
				return earlier
			}
		}
	}
	return at
}

func thinkingPeriod(t time.Time, period string, hours, days int, zone *time.Location) time.Time {
	t = t.In(zone)
	y, m, d := t.Date()
	switch period {
	case "hour":
		// Subtract within the instant to preserve both occurrences of a DST hour.
		start := t.Add(-time.Duration(t.Minute())*time.Minute - time.Duration(t.Second())*time.Second - time.Duration(t.Nanosecond()))
		if transition, _ := t.ZoneBounds(); transition.After(start) {
			return transition
		}
		return start
	case "hours":
		if hours == 1 {
			return thinkingPeriod(t, "hour", hours, days, zone)
		}
		if 24%hours == 0 {
			return thinkingCivilHour(y, m, d, t.Hour()/hours*hours, zone)
		}
		sec := int64(hours) * 3600
		return time.Unix(t.Unix()/sec*sec, 0).In(zone)
	case "days":
		date := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
		base := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
		count := int(date.Sub(base).Hours() / 24)
		v := base.AddDate(0, 0, int(math.Floor(float64(count)/float64(days)))*days)
		return time.Date(v.Year(), v.Month(), v.Day(), 0, 0, 0, 0, zone)
	case "week":
		return time.Date(y, m, d-(int(t.Weekday())+6)%7, 0, 0, 0, 0, zone)
	case "month":
		return time.Date(y, m, 1, 0, 0, 0, 0, zone)
	}
	return time.Date(y, m, d, 0, 0, 0, 0, zone)
}
func thinkingNext(t time.Time, period string, hours, days int) time.Time {
	switch period {
	case "hour":
		next := t.Add(time.Hour - time.Duration(t.Minute())*time.Minute - time.Duration(t.Second())*time.Second - time.Duration(t.Nanosecond()))
		if _, transition := t.ZoneBounds(); transition.After(t) && transition.Before(next) {
			return transition
		}
		return next
	case "hours":
		if hours > 1 && 24%hours == 0 {
			y, m, d := t.Date()
			return thinkingCivilHour(y, m, d, (t.Hour()/hours+1)*hours, t.Location())
		}
		if hours == 1 {
			return thinkingNext(t, "hour", hours, days)
		}
		return t.Add(time.Duration(hours) * time.Hour)
	case "days":
		return t.AddDate(0, 0, days)
	case "week":
		return t.AddDate(0, 0, 7)
	case "month":
		return t.AddDate(0, 1, 0)
	}
	return t.AddDate(0, 0, 1)
}
func thinkingPeriodKey(t time.Time, period string) string {
	if period == "hour" || period == "hours" {
		return t.Format(time.RFC3339)
	}
	if period == "month" {
		return t.Format("2006-01")
	}
	return t.Format("2006-01-02")
}
func thinkingGroupKey(r thinkingRecord, by string) (string, string) {
	s := "All requests"
	switch by {
	case "model":
		s = r.Model
	case "effort":
		s = r.Effort
	case "model_effort":
		b, _ := json.Marshal([]string{r.Model, r.Effort})
		return string(b), r.Model + " · " + r.Effort
	case "provider":
		s = r.Provider
	case "harness":
		s = r.Harness
	case "repository":
		s = r.Repository
	case "machine":
		s = r.Machine
	case "preceding_tool":
		s = r.Preceding
	case "agent_depth":
		s = strconv.Itoa(r.Depth)
	}
	return s, s
}

type thinkingAccumulator struct {
	values, raw, ratios                                         []float64
	conversations, retainedConversations, adjustedConversations map[string]bool
	counts                                                      []int
	retained, timed, adjusted                                   int
}

func newThinkingAccumulator() *thinkingAccumulator {
	return &thinkingAccumulator{conversations: map[string]bool{}, retainedConversations: map[string]bool{}, adjustedConversations: map[string]bool{}, counts: make([]int, len(thinkingEdges)-1)}
}
func (a *thinkingAccumulator) add(r thinkingRecord, reasoning, ratio bool) {
	a.retained++
	a.retainedConversations[r.Conversation] = true
	if r.Gap != nil {
		a.timed++
		a.raw = append(a.raw, *r.Gap)
	}
	if r.Ratio != nil {
		a.adjusted++
		a.ratios = append(a.ratios, *r.Ratio)
		a.adjustedConversations[r.Conversation] = true
	}
	var v *float64
	if reasoning {
		if r.Reasoning > 0 {
			v = &r.Reasoning
		}
	} else if ratio {
		v = r.Ratio
	} else {
		v = r.Gap
	}
	if v != nil {
		a.values = append(a.values, *v)
		a.conversations[r.Conversation] = true
		if r.Gap != nil {
			a.counts[thinkingBin(*r.Gap)]++
		}
	}
}
func (a *thinkingAccumulator) cell() thinkingCell {
	return thinkingCell{thinkingSummarize(a.values), a.retained, len(a.conversations), a.counts}
}
func thinkingBootstrap(values []float64, seed uint32) (ratio, low, high *float64) {
	if len(values) < 4 {
		return nil, nil, nil
	}
	logs := make([]float64, len(values))
	var sum float64
	for i, v := range values {
		logs[i] = math.Log(v)
		sum += logs[i]
	}
	mid := math.Exp(sum / float64(len(logs)))
	samples := make([]float64, 600)
	for i := range samples {
		sum = 0
		for range logs {
			seed = 1664525*seed + 1013904223
			sum += logs[int(float64(seed)/4294967296*float64(len(logs)))]
		}
		samples[i] = math.Exp(sum / float64(len(logs)))
	}
	sort.Float64s(samples)
	return &mid, thinkingQuantile(samples, .025), thinkingQuantile(samples, .975)
}
func thinkingFold(s string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return int(h.Sum32() % 5)
}
func thinkingEffortOrder(s string) int {
	if s == "Unreported" {
		return 100
	}
	for i, v := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "ultra", "max"} {
		if strings.EqualFold(s, v) {
			return i
		}
	}
	return 99
}
func aggregateThinking(ctx context.Context, records []thinkingRecord, o thinkingOptions) (thinkingResponse, error) {
	if err := ctx.Err(); err != nil {
		return thinkingResponse{}, err
	}
	out := thinkingResponse{PeriodBounds: map[string]thinkingBounds{}, CalendarBounds: map[string]thinkingBounds{}, Edges: thinkingEdges, Groups: []thinkingGroup{}, Models: []string{}, Efforts: []string{}, From: thinkingStamp(o.From), To: thinkingStamp(o.To), Timezone: o.Zone.String(), Periods: []string{}, CalendarPeriods: []string{}}
	models, efforts, conversations := map[string]bool{}, map[string]bool{}, map[string]bool{}
	all := newThinkingAccumulator()
	groups := map[string][]int{}
	labels := map[string]string{}
	var first, last time.Time
	for i, r := range records {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return out, err
			}
		}
		all.add(r, false, false)
		models[r.Model] = true
		efforts[r.Effort] = true
		conversations[r.Conversation] = true
		key, label := thinkingGroupKey(r, o.GroupBy)
		groups[key] = append(groups[key], i)
		labels[key] = label
		if !r.At.IsZero() {
			if first.IsZero() || r.At.Before(first) {
				first = r.At
			}
			if last.IsZero() || r.At.After(last) {
				last = r.At
			}
		}
	}
	out.Retained = all.retained
	out.Timed = all.timed
	out.Adjusted = all.adjusted
	out.Conversations = len(conversations)
	out.Summary = thinkingSummarize(all.raw)
	for k := range models {
		out.Models = append(out.Models, k)
	}
	sort.Strings(out.Models)
	for k := range efforts {
		out.Efforts = append(out.Efforts, k)
	}
	sort.Slice(out.Efforts, func(i, j int) bool { return thinkingEffortOrder(out.Efforts[i]) < thinkingEffortOrder(out.Efforts[j]) })
	if !first.IsZero() {
		from, to := first, last
		if !o.From.IsZero() {
			from = o.From
		}
		if !o.To.IsZero() {
			to = o.To.Add(-time.Nanosecond)
		}
		for _, v := range []struct {
			period string
			target *[]string
		}{{o.Period, &out.Periods}, {o.Rows, &out.CalendarPeriods}} {
			if v.target == &out.Periods && o.View != "time" || v.target == &out.CalendarPeriods && o.View != "calendar" {
				continue
			}
			t := thinkingPeriod(from, v.period, o.Hours, o.Days, o.Zone)
			for !t.After(to) {
				if len(*v.target) >= 5000 {
					return out, fmt.Errorf("more than 5,000 time buckets; choose a coarser Time Bucket or shorter Range")
				}
				key := thinkingPeriodKey(t, v.period)
				*v.target = append(*v.target, key)
				lo, hi := t, thinkingNext(t, v.period, o.Hours, o.Days)
				if !o.From.IsZero() && lo.Before(o.From) {
					lo = o.From
				}
				if !o.To.IsZero() && hi.After(o.To) {
					hi = o.To
				}
				bounds := thinkingBounds{thinkingStamp(lo), thinkingStamp(hi)}
				if v.target == &out.Periods {
					out.PeriodBounds[key] = bounds
				} else {
					out.CalendarBounds[key] = bounds
				}
				t = thinkingNext(t, v.period, o.Hours, o.Days)
			}
		}
	}
	for key, rs := range groups {
		g := thinkingGroup{Key: key, Label: labels[key], Counts: make([]int, len(thinkingEdges)-1), Periods: map[string]thinkingCell{}, Calendar: []thinkingCalendarCell{}, Bands: []thinkingBand{}}
		if o.GroupBy == "model_effort" {
			g.Model = records[rs[0]].Model
			g.Effort = records[rs[0]].Effort
		}
		if o.GroupBy == "effort" {
			g.Effort = records[rs[0]].Effort
		}
		if o.GroupBy == "model" {
			g.Model = records[rs[0]].Model
		}
		a := newThinkingAccumulator()
		periods := map[string]*thinkingAccumulator{}
		cells := map[string]*thinkingAccumulator{}
		daily := map[string]*thinkingAccumulator{}
		cellDays := map[string]string{}
		cellHours := map[string]int{}
		for _, index := range rs {
			r := records[index]
			reasoning := o.View == "reasoning"
			relative := o.View == "calendar" && o.Metric == "ratio"
			a.add(r, reasoning, relative)
			if r.At.IsZero() {
				continue
			}
			if o.View == "time" {
				p := thinkingPeriodKey(thinkingPeriod(r.At, o.Period, o.Hours, o.Days, o.Zone), o.Period)
				if periods[p] == nil {
					periods[p] = newThinkingAccumulator()
				}
				periods[p].add(r, false, false)
			}
			if o.View == "calendar" {
				day := thinkingPeriodKey(thinkingPeriod(r.At, o.Rows, 1, 1, o.Zone), o.Rows)
				hour := r.At.In(o.Zone).Hour()
				cellKey := day + "|" + strconv.Itoa(hour)
				if cells[cellKey] == nil {
					cells[cellKey] = newThinkingAccumulator()
					cellDays[cellKey] = day
					cellHours[cellKey] = hour
				}
				cells[cellKey].add(r, false, false)
				dk := r.At.In(o.Zone).Format("2006-01-02") + "|" + strconv.Itoa(hour/3*3)
				if daily[dk] == nil {
					daily[dk] = newThinkingAccumulator()
				}
				daily[dk].add(r, false, true)
			}
		}
		g.Retained = a.retained
		g.Timed = a.timed
		g.Adjusted = a.adjusted
		g.Conversations = len(a.conversations)
		g.RetainedConversations = len(a.retainedConversations)
		g.Counts = a.counts
		g.Stats = thinkingSummarize(a.values)
		if g.Stats.N < o.Minimum {
			out.Hidden++
			continue
		}
		for p, c := range periods {
			g.Periods[p] = c.cell()
		}
		if o.Overview && len(a.raw) > 0 {
			sort.Float64s(a.raw)
			for i := 0; i <= 100; i++ {
				g.CDF = append(g.CDF, [2]float64{*thinkingQuantile(a.raw, float64(i)/100), float64(i) / 100})
			}
		}
		for k, c := range cells {
			cell := thinkingCalendarCell{Day: cellDays[k], Hour: cellHours[k], N: len(c.raw), Adjusted: len(c.ratios), Conversations: len(c.conversations), AdjustedConversations: len(c.adjustedConversations)}
			if cell.N >= 20 && cell.Conversations >= 3 {
				v := thinkingMedian(c.raw)
				cell.Raw = &v
			}
			if cell.Adjusted >= 20 && cell.AdjustedConversations >= 3 {
				v := thinkingMedian(c.ratios)
				cell.Ratio = &v
			}
			g.Calendar = append(g.Calendar, cell)
		}
		sort.Slice(g.Calendar, func(i, j int) bool {
			if g.Calendar[i].Day != g.Calendar[j].Day {
				return g.Calendar[i].Day < g.Calendar[j].Day
			}
			return g.Calendar[i].Hour < g.Calendar[j].Hour
		})
		if o.View == "calendar" {
			for hour := 0; hour < 24; hour += 3 {
				band := thinkingBand{Hour: hour, Daily: map[string]float64{}}
				values := []float64{}
				keys := []string{}
				for k := range daily {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					c := daily[k]
					if strings.HasSuffix(k, "|"+strconv.Itoa(hour)) && len(c.ratios) >= 20 && len(c.adjustedConversations) >= 3 {
						v := thinkingMedian(c.ratios)
						band.Daily[strings.Split(k, "|")[0]] = v
						band.N += len(c.ratios)
						values = append(values, v)
					}
				}
				band.Days = len(values)
				band.Ratio, band.Low, band.High = thinkingBootstrap(values, uint32(800+hour))
				g.Bands = append(g.Bands, band)
			}
			a, b := g.Bands[o.HourA/3], g.Bands[o.HourB/3]
			paired := []float64{}
			keys := []string{}
			for k := range a.Daily {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if v, ok := b.Daily[k]; ok {
					paired = append(paired, a.Daily[k]/v)
				}
			}
			g.Comparison.Days = len(paired)
			g.Comparison.Ratio, g.Comparison.Low, g.Comparison.High = thinkingBootstrap(paired, 5123)
		}
		out.Groups = append(out.Groups, g)
	}
	sort.Slice(out.Groups, func(i, j int) bool {
		a, b := out.Groups[i], out.Groups[j]
		if o.GroupBy == "effort" || o.GroupBy == "model_effort" {
			if a.Model != b.Model {
				return a.Model < b.Model
			}
			if thinkingEffortOrder(a.Effort) != thinkingEffortOrder(b.Effort) {
				return thinkingEffortOrder(a.Effort) < thinkingEffortOrder(b.Effort)
			}
			return a.Label < b.Label
		}
		var av, bv *float64
		switch o.Sort {
		case "volume":
			x, y := float64(a.Stats.N), float64(b.Stats.N)
			av, bv = &x, &y
		case "p95":
			av, bv = a.Stats.P95, b.Stats.P95
		default:
			av, bv = a.Stats.Median, b.Stats.Median
		}
		if av == nil {
			return bv == nil && a.Label < b.Label
		}
		if bv == nil {
			return true
		}
		if *av == *bv {
			return a.Label < b.Label
		}
		if o.Direction == "asc" {
			return *av < *bv
		}
		return *av > *bv
	})
	return out, nil
}
func (s *Server) handleThinking(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, fmt.Errorf("GET required"), http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	r = r.WithContext(ctx)
	o, err := readThinkingOptions(r.URL.Query(), time.Now())
	if err != nil {
		writeError(w, err, 400)
		return
	}
	phase := time.Now()
	timings := []string{}
	mark := func(name string) {
		timings = append(timings, fmt.Sprintf("%s;dur=%.1f", name, float64(time.Since(phase).Microseconds())/1000))
		phase = time.Now()
	}
	all, err := s.Catalog.thinkingSnapshot(r.Context())
	if err != nil {
		writeError(w, fmt.Errorf("read request timing: %w", err), 500)
		return
	}
	mark("records")
	ids, err := s.Catalog.thinkingNativeIDs(r.Context(), o)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	pairOptions := o
	if o.Overview {
		pairOptions.Model = ""
		pairOptions.Effort = ""
	}
	pairRecords, err := selectThinkingRecords(r.Context(), all, pairOptions, ids)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	records := pairRecords
	if o.Overview && (o.Model != "" && o.Model != "all" || o.Effort != "" && o.Effort != "all") {
		records, err = selectThinkingRecords(r.Context(), pairRecords, thinkingOptions{Model: o.Model, Effort: o.Effort}, nil)
		if err != nil {
			writeError(w, err, 500)
			return
		}
	}
	mark("filter")
	fit, err := s.Catalog.thinkingFit(r.Context(), records, o.View == "calendar", o.Overview)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	if len(fit.Ratios) > 0 {
		records = append([]thinkingRecord(nil), records...)
	}
	for i, ratio := range fit.Ratios {
		if ratio > 0 {
			records[i].Ratio = &fit.Ratios[i]
		}
	}
	mark("fit")
	value, err := aggregateThinking(r.Context(), records, o)
	if err != nil {
		writeError(w, err, 400)
		return
	}
	if o.Overview {
		value.Factors = fit.Factors
		value.Compactions = []thinkingCompaction{}
		for _, event := range thinkingCompactions(pairRecords) {
			if o.Model != "" && o.Model != "all" && event.Model != o.Model {
				continue
			}
			if o.Effort != "" && o.Effort != "all" && event.Effort != o.Effort {
				continue
			}
			value.Compactions = append(value.Compactions, event)
		}
		value.Traces = thinkingTraces(records)
		coverage := o
		coverage.GroupBy = "model"
		coverage.Minimum = 0
		coverage.View = "histogram"
		other, _ := aggregateThinking(r.Context(), records, coverage)
		value.Coverage = other.Groups
		coverage.GroupBy = "harness"
		agents, _ := aggregateThinking(r.Context(), records, coverage)
		value.AgentCoverage = agents.Groups
	}
	mark("aggregate")
	if o.Evidence {
		value.Validation, err = s.Catalog.thinkingValidation(r.Context(), o, records)
		if err != nil {
			writeError(w, err, 500)
			return
		}
	}
	if o.Evidence {
		mark("validation")
	}
	metadata, err := s.Catalog.thinkingOptionsList(r.Context())
	if err != nil {
		writeError(w, err, 500)
		return
	}
	value.Models = metadata.Models
	value.Efforts = metadata.Efforts
	mark("options")
	w.Header().Set("Server-Timing", strings.Join(timings, ", "))
	writeJSON(w, value, 200)
}

func sortThinkingOptions(v *thinkingOptionsList) {
	sort.Strings(v.Models)
	sort.Slice(v.Efforts, func(i, j int) bool {
		a, b := thinkingEffortOrder(v.Efforts[i]), thinkingEffortOrder(v.Efforts[j])
		if a != b {
			return a < b
		}
		return v.Efforts[i] < v.Efforts[j]
	})
}
