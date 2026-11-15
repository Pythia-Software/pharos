package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	qt "github.com/Pythia-Software/query-table/backends/go"
	"github.com/gbdubs/pharos/internal/querytable"
	"hash/fnv"
	"maps"
	"math"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
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
		{"groupBy", "none", &o.GroupBy, []string{"none", "model", "model_effort", "speed", "model_speed", "model_effort_speed", "provider", "effort", "harness", "project", "repository", "machine", "preceding_tool", "agent_depth"}},
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
	o.Zone = time.Local
	if zone := q.Get("timezone"); zone != "" {
		o.Zone, err = time.LoadLocation(zone)
		if err != nil {
			return o, fmt.Errorf("invalid timezone")
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
		// Filters on the calendar fields read them in the viewer's zone, as
		// query-v3 does for the same request, so the fit and the page's
		// metrics count the same requests.
		table := thinkingRequestDataset
		if zone := q.Get("timezone"); zone != "" && zone != "Local" {
			if table, e = thinkingZonedDataset(table, zone); e != nil {
				return o, e
			}
		}
		o.FilterSQL, o.FilterArgs, e = table.where(query.Where, schema)
		if e != nil {
			return o, e
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
	ID, Conversation, Stream, Workspace, Model, Effort, Speed, Provider, Harness, Repository, Project, Machine, Preceding, Quality, Previous, Native string
	ObservedMillis                                                                                                                                   sql.NullInt64
	At                                                                                                                                               time.Time
	Sequence, Depth                                                                                                                                  int
	Input, Cache, Output, Reasoning                                                                                                                  float64
	Gap, Ratio                                                                                                                                       *float64
	Compaction                                                                                                                                       bool
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

// thinkingCalendarCell is one row period and local hour of a group's time of
// day: the median wait of its timed requests and the median workload-adjusted
// ratio of its estimates, each only when supported (20 values from 3
// conversations). It is read here rather than through query-v3 because a
// metric takes one calendar key and the zoned day and hour are computed,
// so Query Table reads them once per clause rather than in its shared scan.
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

// thinkingGroup holds what only the workload fit gives one group: its
// supported estimates by time of day, their three-hour bands and the paired
// comparison. With the ratio metric, stats and Conversations describe the
// estimates; otherwise model_requests describes the group.
type thinkingGroup struct {
	Key           string                 `json:"key"`
	Adjusted      int                    `json:"Adjusted"`
	Stats         *thinkingStats         `json:"stats,omitempty"`
	Conversations *int                   `json:"Conversations,omitempty"`
	Calendar      []thinkingCalendarCell `json:"calendar"`
	Bands         []thinkingBand         `json:"bands"`
	Comparison    thinkingComparison     `json:"comparison"`
}

// thinkingResponse is what the Thinking dashboards read beside query-v3:
// the fitted estimates, factors, compaction pairs, traces and validation,
// which need whole conversations in order rather than grouped rows.
type thinkingResponse struct {
	Adjusted    int                  `json:"Adjusted"`
	Groups      []thinkingGroup      `json:"groups"`
	Timezone    string               `json:"timezone"`
	Factors     []thinkingFactor     `json:"factors,omitempty"`
	Compactions []thinkingCompaction `json:"compactions,omitempty"`
	Traces      []thinkingTrace      `json:"traces,omitempty"`
	Validation  *thinkingValidation  `json:"validation,omitempty"`
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
	// Fixed-size chunks are joined once at the end, which costs less than
	// counting the ledger first or regrowing one slice.
	rows, err := tx.QueryContext(ctx, `SELECT id,conversation_id,COALESCE(agent_session_id,''),workspace_id,model,effort,speed,
 COALESCE(provider,''),COALESCE(harness,''),COALESCE(repository_name,''),COALESCE(project_name,''),COALESCE(machine,''),preceding_tool,timing_quality,
 COALESCE(previous_request_id,''),COALESCE(native_id,''),COALESCE(requested_at,''),observed_millis,sequence,COALESCE(agent_depth,0),input_tokens,
 cache_read_input_tokens,output_tokens,reasoning_output_tokens,gap_seconds,compacted_before FROM (`+thinkingSource+`) d WHERE is_estimate=0 AND (`+where+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	const chunk = 1 << 16
	chunks := [][]thinkingRecord{}
	out := make([]thinkingRecord, 0, chunk)
	for rows.Next() {
		var r thinkingRecord
		var at string
		var gap sql.NullFloat64
		var compacted int
		if err := rows.Scan(&r.ID, &r.Conversation, &r.Stream, &r.Workspace, &r.Model, &r.Effort, &r.Speed, &r.Provider, &r.Harness, &r.Repository, &r.Project, &r.Machine, &r.Preceding, &r.Quality, &r.Previous, &r.Native, &at, &r.ObservedMillis, &r.Sequence, &r.Depth, &r.Input, &r.Cache, &r.Output, &r.Reasoning, &gap, &compacted); err != nil {
			return nil, err
		}
		r.At, _ = time.Parse(time.RFC3339Nano, at)
		r.Compaction = compacted != 0
		thinkingRelabel(&r)
		if gap.Valid {
			r.Gap = &gap.Float64
		}
		if len(out) == chunk {
			chunks = append(chunks, out)
			out = make([]thinkingRecord, 0, chunk)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return slices.Concat(append(chunks, out)...), nil
}

// thinkingRelabel names a record's missing labels as the dashboards show them.
// Model and effort are stored with missing ones already named; the other
// breakdowns keep missing apart from a value that reads as its name
// (thinkingNamed).
func thinkingRelabel(r *thinkingRecord) {
	r.Model = thinkingLabel(r.Model)
	r.Effort = thinkingLabel(r.Effort)
	r.Provider = thinkingNamed(r.Provider, "Unreported")
	r.Harness = thinkingNamed(r.Harness, "Unreported")
	r.Repository = thinkingLabel(r.Repository)
	r.Project = thinkingNamed(r.Project, "No project")
	r.Machine = thinkingNamed(r.Machine, "Unreported")
}

// thinkingNamed is a breakdown value as a group key: missing is the missing
// label, and a value spelled like that label (or like an escaped one) is
// escaped with a leading NUL, so query-v3's separate buckets stay separate
// groups (thinkingGroupIdentity in thinking-query.ts).
func thinkingNamed(s, missing string) string {
	if strings.TrimSpace(s) == "" {
		return missing
	}
	if s == missing || strings.HasPrefix(s, "\x00") {
		return "\x00" + s
	}
	return s
}

// thinkingNamedLabel shows an escaped value in quotes, apart from the
// missing group it is spelled like.
func thinkingNamedLabel(key string) string {
	if value, escaped := strings.CutPrefix(key, "\x00"); escaped {
		return "“" + value + "”"
	}
	return key
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

// thinkingCalendarRow is the local day, Monday week or month holding t, as
// model_requests' day, week and month fields name it in the same zone.
func thinkingCalendarRow(t time.Time, rows string, zone *time.Location) string {
	t = t.In(zone)
	switch rows {
	case "week":
		return t.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7)).Format("2006-01-02")
	case "month":
		return t.Format("2006-01")
	}
	return t.Format("2006-01-02")
}

// thinkingGroupKey is a record's group under a breakdown, keyed and labelled
// as thinking-query.ts keys model_requests' groups, which the page merges.
// thinkingTupleKey is a JSON array of a group's labels, as JSON.stringify
// writes it: no HTML escaping.
func thinkingTupleKey(parts ...string) string {
	var b strings.Builder
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(parts)
	return strings.TrimSuffix(b.String(), "\n")
}

func thinkingGroupKey(r thinkingRecord, by string) (string, string) {
	s := "All requests"
	switch by {
	case "model":
		s = r.Model
	case "effort":
		s = r.Effort
	case "speed":
		return strings.ToLower(speedLabel(r.Speed)), speedLabel(r.Speed)
	case "model_speed", "model_effort_speed":
		parts := []string{r.Model}
		if by == "model_effort_speed" {
			parts = append(parts, r.Effort)
		}
		parts = append(parts, speedLabel(r.Speed))
		return thinkingTupleKey(parts...), strings.Join(parts, " · ")
	case "model_effort":
		return thinkingTupleKey(r.Model, r.Effort), r.Model + " · " + r.Effort
	case "provider":
		return r.Provider, thinkingNamedLabel(r.Provider)
	case "harness":
		return r.Harness, thinkingNamedLabel(r.Harness)
	// A link from before projects grouped by repository; it reads as project.
	case "project", "repository":
		return r.Project, thinkingNamedLabel(r.Project)
	case "machine":
		return r.Machine, thinkingNamedLabel(r.Machine)
	case "preceding_tool":
		s = r.Preceding
	case "agent_depth":
		s = strconv.Itoa(r.Depth)
	}
	return s, s
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

// aggregateThinking gives each group its calendar estimates, bands and
// comparison, for the time-of-day view; other views need none. Records carry
// their fitted Ratio.
func aggregateThinking(ctx context.Context, records []thinkingRecord, o thinkingOptions) (thinkingResponse, error) {
	out := thinkingResponse{Groups: []thinkingGroup{}, Timezone: o.Zone.String()}
	if err := ctx.Err(); err != nil || o.View != "calendar" {
		return out, err
	}
	groups := map[string][]int{}
	for i, r := range records {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return out, err
			}
		}
		key, _ := thinkingGroupKey(r, o.GroupBy)
		groups[key] = append(groups[key], i)
		if r.Ratio != nil {
			out.Adjusted++
		}
	}
	type sample struct {
		ratios        []float64
		conversations map[string]bool
	}
	newSample := func() *sample { return &sample{conversations: map[string]bool{}} }
	add := func(m map[string]*sample, key string, r thinkingRecord) *sample {
		if m[key] == nil {
			m[key] = newSample()
		}
		m[key].ratios = append(m[key].ratios, *r.Ratio)
		m[key].conversations[r.Conversation] = true
		return m[key]
	}
	supported := func(s *sample) bool { return len(s.ratios) >= 20 && len(s.conversations) >= 3 }
	type cellSample struct{ raw, adjusted *sample }
	for key, rs := range groups {
		g := thinkingGroup{Key: key, Calendar: []thinkingCalendarCell{}, Bands: []thinkingBand{}}
		all := newSample()
		cells, daily := map[string]*cellSample{}, map[string]*sample{}
		for _, index := range rs {
			r := records[index]
			if r.Ratio != nil {
				all.ratios = append(all.ratios, *r.Ratio)
				all.conversations[r.Conversation] = true
			}
			if r.At.IsZero() {
				continue
			}
			hour := r.At.In(o.Zone).Hour()
			k := thinkingCalendarRow(r.At, o.Rows, o.Zone) + "|" + strconv.Itoa(hour)
			c := cells[k]
			if c == nil {
				c = &cellSample{raw: newSample(), adjusted: newSample()}
				cells[k] = c
			}
			if r.Gap != nil {
				c.raw.ratios = append(c.raw.ratios, *r.Gap)
				c.raw.conversations[r.Conversation] = true
			}
			if r.Ratio != nil {
				c.adjusted.ratios = append(c.adjusted.ratios, *r.Ratio)
				c.adjusted.conversations[r.Conversation] = true
				add(daily, thinkingCalendarRow(r.At, "day", o.Zone)+"|"+strconv.Itoa(hour/3*3), r)
			}
		}
		g.Adjusted = len(all.ratios)
		if o.Metric == "ratio" {
			stats, n := thinkingSummarize(all.ratios), len(all.conversations)
			g.Stats, g.Conversations = &stats, &n
		}
		for k, c := range cells {
			day, hour, _ := strings.Cut(k, "|")
			cell := thinkingCalendarCell{Day: day, N: len(c.raw.ratios), Conversations: len(c.raw.conversations),
				Adjusted: len(c.adjusted.ratios), AdjustedConversations: len(c.adjusted.conversations)}
			cell.Hour, _ = strconv.Atoi(hour)
			if supported(c.raw) {
				v := thinkingMedian(c.raw.ratios)
				cell.Raw = &v
			}
			if supported(c.adjusted) {
				v := thinkingMedian(c.adjusted.ratios)
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
		keys := slices.Sorted(maps.Keys(daily))
		for hour := 0; hour < 24; hour += 3 {
			band := thinkingBand{Hour: hour, Daily: map[string]float64{}}
			values := []float64{}
			for _, k := range keys {
				c := daily[k]
				if strings.HasSuffix(k, "|"+strconv.Itoa(hour)) && supported(c) {
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
		for _, k := range slices.Sorted(maps.Keys(a.Daily)) {
			if v, ok := b.Daily[k]; ok {
				paired = append(paired, a.Daily[k]/v)
			}
		}
		g.Comparison.Days = len(paired)
		g.Comparison.Ratio, g.Comparison.Low, g.Comparison.High = thinkingBootstrap(paired, 5123)
		out.Groups = append(out.Groups, g)
	}
	sort.Slice(out.Groups, func(i, j int) bool { return out.Groups[i].Key < out.Groups[j].Key })
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
	// now (milliseconds) is the clock a rolling or Today Range ends at, so
	// the page can end it where its query-v3 read ends (loadThinkingData).
	now := time.Now()
	if value := r.URL.Query().Get("now"); value != "" {
		ms, err := strconv.ParseInt(value, 10, 64)
		if err != nil || ms <= 0 {
			writeError(w, fmt.Errorf("invalid now"), 400)
			return
		}
		now = time.UnixMilli(ms)
	}
	o, err := readThinkingOptions(r.URL.Query(), now)
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
	if err := s.Catalog.currentRequestTimings(r.Context()); err != nil {
		writeError(w, fmt.Errorf("read request timing: %w", err), 500)
		return
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
	// The overview's sections read the same records independently, so they
	// are computed concurrently.
	var sections sync.WaitGroup
	var compactions []thinkingCompaction
	var traces []thinkingTrace
	if o.Overview {
		sections.Go(func() { compactions = thinkingCompactions(pairRecords) })
		sections.Go(func() { traces = thinkingTraces(records) })
	}
	value, err := aggregateThinking(r.Context(), records, o)
	sections.Wait()
	if err != nil {
		writeError(w, err, 400)
		return
	}
	if o.Overview {
		value.Factors = fit.Factors
		value.Compactions = []thinkingCompaction{}
		for _, event := range compactions {
			if o.Model != "" && o.Model != "all" && event.Model != o.Model {
				continue
			}
			if o.Effort != "" && o.Effort != "all" && event.Effort != o.Effort {
				continue
			}
			value.Compactions = append(value.Compactions, event)
		}
		value.Traces = traces
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
	w.Header().Set("Server-Timing", strings.Join(timings, ", "))
	writeJSON(w, value, 200)
}
