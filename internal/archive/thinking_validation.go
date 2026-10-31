package archive

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"time"
)

type thinkingValidation struct {
	Pairs          int          `json:"pairs"`
	Correlation    *float64     `json:"correlation"`
	MedianRatio    *float64     `json:"medianRatio"`
	Points         [][2]float64 `json:"points"`
	Counters       [][2]float64 `json:"counters"`
	CounterCount   int          `json:"counterCount"`
	CounterOverlap int          `json:"counterOverlap"`
}

func thinkingRanks(values []float64) []float64 {
	order := make([]int, len(values))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool { return values[order[i]] < values[order[j]] })
	r := make([]float64, len(values))
	for i := 0; i < len(order); {
		j := i + 1
		for j < len(order) && values[order[j]] == values[order[i]] {
			j++
		}
		for k := i; k < j; k++ {
			r[order[k]] = float64(i+j-1) / 2
		}
		i = j
	}
	return r
}
func thinkingCorrelation(a, b []float64) *float64 {
	if len(a) < 3 {
		return nil
	}
	x, y := thinkingRanks(a), thinkingRanks(b)
	mx, my := 0.0, 0.0
	for i := range x {
		mx += x[i] / float64(len(x))
		my += y[i] / float64(len(y))
	}
	xx, yy, xy := 0.0, 0.0, 0.0
	for i := range x {
		xx += (x[i] - mx) * (x[i] - mx)
		yy += (y[i] - my) * (y[i] - my)
		xy += (x[i] - mx) * (y[i] - my)
	}
	if xx == 0 || yy == 0 {
		return nil
	}
	v := xy / math.Sqrt(xx*yy)
	return &v
}
func (c *Catalog) thinkingValidation(ctx context.Context, o thinkingOptions, records []thinkingRecord) (*thinkingValidation, error) {
	return cachedValue(ctx, c, "thinking:validation:"+thinkingCohortKey(records), func(ctx context.Context) (*thinkingValidation, error) {
		bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		return c.loadThinkingValidation(bounded, records)
	})
}
func (c *Catalog) loadThinkingValidation(ctx context.Context, records []thinkingRecord) (*thinkingValidation, error) {
	result := &thinkingValidation{Points: [][2]float64{}, Counters: [][2]float64{}}
	// Select metadata from the already qualified population, without rebuilding the
	// request/tool projection for each validation query. Only numbers leave SQL.
	conversations := map[string]bool{}
	natives := map[string]string{}
	gaps := map[string]float64{}
	for _, r := range records {
		conversations[r.Conversation] = true
		natives[r.Conversation+"|"+r.Native] = r.ID
		if r.Gap != nil {
			gaps[r.ID] = *r.Gap
		}
	}
	selected := []string{}
	for id := range conversations {
		selected = append(selected, id)
	}
	sort.Strings(selected)
	selection, err := json.Marshal(selected)
	if err != nil {
		return nil, err
	}
	args := []any{string(selection)}
	source := `WITH selected AS (SELECT value conversation_id FROM json_each(?)), envelopes AS (
 SELECT m.conversation_id,m.source_order,CASE WHEN json_valid(COALESCE(NULLIF(m.raw_text,''),m.text))
 THEN COALESCE(NULLIF(m.raw_text,''),m.text) ELSE '{}' END body
 FROM selected s CROSS JOIN messages m ON m.conversation_id=s.conversation_id WHERE m.kind='metadata') `
	// Read each metadata body once. Materialize only scalar evidence, so the
	// timer grouping and latest-counter window share a narrow numeric relation.
	source += `, measured AS MATERIALIZED (
 SELECT conversation_id,source_order,CASE WHEN json_type(body,'$.message.id')='text' THEN json_extract(body,'$.message.id') END native_id,
 CASE WHEN json_type(body,'$.thinkingDurationMs') IN ('integer','real') THEN json_extract(body,'$.thinkingDurationMs') END thinking_ms,json_extract(body,'$.type') kind,
 CASE WHEN json_type(body,'$.totalDuration') IN ('integer','real') THEN json_extract(body,'$.totalDuration') END total,CASE WHEN json_type(body,'$.totalAPIDuration') IN ('integer','real') THEN json_extract(body,'$.totalAPIDuration') END api,
 CASE WHEN json_type(body,'$.totalToolDuration') IN ('integer','real') THEN json_extract(body,'$.totalToolDuration') END tool FROM envelopes), states AS (
 SELECT *,ROW_NUMBER() OVER(PARTITION BY conversation_id ORDER BY source_order DESC) latest
 FROM measured WHERE kind='cost-state' AND typeof(total) IN ('integer','real')
 AND typeof(api) IN ('integer','real') AND typeof(tool) IN ('integer','real'))
 SELECT 'timer' evidence,conversation_id,native_id,MAX(thinking_ms) value,NULL components FROM measured
 WHERE typeof(thinking_ms) IN ('integer','real') AND typeof(native_id)='text' GROUP BY conversation_id,native_id
 UNION ALL SELECT 'counter',conversation_id,'',total,api+tool FROM states WHERE latest=1`
	rows, err := queryMapsContext(ctx, c.reader(), source, args...)
	if err != nil {
		return nil, err
	}
	x, y, ratios := []float64{}, []float64{}, []float64{}
	for _, r := range rows {
		if asString(r["evidence"]) == "counter" {
			total, components := thinkingNumber(r["value"])/1000, thinkingNumber(r["components"])/1000
			result.CounterCount++
			if components > total {
				result.CounterOverlap++
			}
			if len(result.Counters) < 1600 {
				result.Counters = append(result.Counters, [2]float64{total, components})
			}
			continue
		}
		gap, ok := gaps[natives[asString(r["conversation_id"])+"|"+asString(r["native_id"])]]
		timer := thinkingNumber(r["value"]) / 1000
		if !ok || timer <= 0 {
			continue
		}
		x = append(x, gap)
		y = append(y, timer)
		ratios = append(ratios, timer/gap)
	}
	result.Pairs = len(x)
	result.Correlation = thinkingCorrelation(x, y)
	if len(ratios) > 0 {
		v := thinkingMedian(ratios)
		result.MedianRatio = &v
	}
	for i := 0; i < min(len(x), 1600); i++ {
		at := i * len(x) / min(len(x), 1600)
		result.Points = append(result.Points, [2]float64{x[at], y[at]})
	}
	return result, nil
}
