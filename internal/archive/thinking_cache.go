package archive

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// One immutable timing population is shared across views. The existing catalog
// generation monitor invalidates it after ledger, identity or attribution edits,
// including commits from another process. Concurrent cold readers share a build.
func (c *Catalog) thinkingSnapshot(ctx context.Context) ([]thinkingRecord, error) {
	return cachedValue(ctx, c, "thinking:requests", func(ctx context.Context) ([]thinkingRecord, error) {
		bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		return c.thinkingRecords(bounded, thinkingOptions{})
	})
}

// Native WHERE remains compiled/executed by the existing SQL dataset. Predicates
// on the request ledger do not need to reconstruct timing to select eligible IDs.
// In particular Card Library date/model filters take this inexpensive path.
func (c *Catalog) thinkingNativeIDs(ctx context.Context, o thinkingOptions) (map[string]bool, error) {
	if o.FilterSQL == "" {
		return nil, nil
	}
	encoded, err := json.Marshal(struct {
		SQL  string
		Args []any
	}{o.FilterSQL, o.FilterArgs})
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(encoded)
	return cachedValue(ctx, c, "thinking:selection:"+hex.EncodeToString(h[:]), func(ctx context.Context) (map[string]bool, error) {
		bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		return c.loadThinkingNativeIDs(bounded, o)
	})
}

func (c *Catalog) loadThinkingNativeIDs(ctx context.Context, o thinkingOptions) (map[string]bool, error) {
	from := "(" + thinkingSource + ") d"
	simple := true
	for _, term := range o.NativeWhere {
		for _, branch := range term.Branches() {
			for _, clause := range branch {
				switch clause.Field {
				case "id", "conversation_id", "agent_session_id", "native_id", "sequence", "requested_at", "model", "effort", "input_tokens", "cache_read_input_tokens", "output_tokens", "reasoning_output_tokens", "compacted_before", "model_effort", "day", "week", "month":
				default:
					simple = false
				}
			}
		}
	}
	if simple {
		from = "(SELECT mr.*,COALESCE(NULLIF(model,''),'Unreported') thinking_model,COALESCE(NULLIF(effort,''),'Unreported') thinking_effort FROM model_requests mr) d"
	}
	where := o.FilterSQL
	if simple {
		where = strings.ReplaceAll(where, "d.model", "d.thinking_model")
		where = strings.ReplaceAll(where, "d.effort", "d.thinking_effort")
	}
	tx, release, err := beginQueryWithTempStore(ctx, c.reader(), 1)
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := tx.QueryContext(ctx, "SELECT d.id FROM "+from+" WHERE "+strings.TrimPrefix(where, " WHERE "), o.FilterArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, rows.Err()
}

func selectThinkingRecords(ctx context.Context, all []thinkingRecord, o thinkingOptions, ids map[string]bool) ([]thinkingRecord, error) {
	if ids == nil && o.From.IsZero() && o.To.IsZero() && (o.Model == "" || o.Model == "all") && (o.Effort == "" || o.Effort == "all") {
		return all, nil
	}
	capacity := len(all)
	if ids != nil || !o.From.IsZero() || !o.To.IsZero() || o.Model != "" && o.Model != "all" || o.Effort != "" && o.Effort != "all" {
		capacity = min(capacity, 16384)
	}
	out := make([]thinkingRecord, 0, capacity)
	for i, r := range all {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if ids != nil && !ids[r.ID] {
			continue
		}
		if (!o.From.IsZero() && (!r.ObservedMillis.Valid || r.ObservedMillis.Int64 < o.From.UnixMilli())) || (!o.To.IsZero() && (!r.ObservedMillis.Valid || r.ObservedMillis.Int64 >= o.To.UnixMilli())) {
			continue
		}
		if o.Model != "" && o.Model != "all" && r.Model != o.Model || o.Effort != "" && o.Effort != "all" && r.Effort != o.Effort {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

type thinkingOptionsList struct{ Models, Efforts []string }

func (c *Catalog) thinkingOptionsList(ctx context.Context) (thinkingOptionsList, error) {
	return cachedValue(ctx, c, "thinking:options", func(ctx context.Context) (thinkingOptionsList, error) {
		rows, err := queryMapsContext(ctx, c.reader(), "SELECT DISTINCT COALESCE(NULLIF(model,''),'Unreported') model,COALESCE(NULLIF(effort,''),'Unreported') effort FROM model_requests")
		if err != nil {
			return thinkingOptionsList{}, err
		}
		models, efforts := map[string]bool{}, map[string]bool{}
		for _, r := range rows {
			models[asString(r["model"])] = true
			efforts[asString(r["effort"])] = true
		}
		out := thinkingOptionsList{}
		for m := range models {
			out.Models = append(out.Models, m)
		}
		for e := range efforts {
			out.Efforts = append(out.Efforts, e)
		}
		sortThinkingOptions(&out)
		return out, nil
	})
}

type thinkingFit struct {
	Factors []thinkingFactor
	Ratios  []float64
}

func (c *Catalog) thinkingFit(ctx context.Context, rs []thinkingRecord, normalization, factors bool) (thinkingFit, error) {
	if !normalization && !factors {
		return thinkingFit{}, nil
	}
	key := fmt.Sprintf("thinking:fit:%t:%t:%s", normalization, factors, thinkingCohortKey(rs))
	return cachedValue(ctx, c, key, func(ctx context.Context) (thinkingFit, error) {
		bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		c.derived.mu.Lock()
		if c.thinkingFitSlots == nil {
			c.thinkingFitSlots = make(chan struct{}, 1)
		}
		slots := c.thinkingFitSlots
		c.derived.mu.Unlock()
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		case <-bounded.Done():
			return thinkingFit{}, bounded.Err()
		}

		input := rs
		if normalization {
			input = append([]thinkingRecord(nil), rs...)
		}
		value := thinkingFit{Factors: thinkingModels(bounded, input, normalization, factors)}
		if err := bounded.Err(); err != nil {
			return thinkingFit{}, err
		}
		if normalization {
			value.Ratios = make([]float64, len(input))
			for i, r := range input {
				if r.Ratio != nil {
					value.Ratios[i] = *r.Ratio
				}
			}
		}
		return value, nil
	})
}

func thinkingCohortKey(rs []thinkingRecord) string {
	h := sha256.New()
	// Include fitted inputs as well as identity: a source commit can land between
	// acquiring the immutable population and acquiring this cache's generation.
	// An older in-flight cohort must never seed a fit for updated observations.
	var number [8]byte
	for _, r := range rs {
		for _, value := range []string{r.ID, r.Model, r.Effort, r.Conversation, r.Stream, r.Native} {
			binary.LittleEndian.PutUint64(number[:], uint64(len(value)))
			_, _ = h.Write(number[:])
			_, _ = h.Write([]byte(value))
		}
		gap := math.NaN()
		if r.Gap != nil {
			gap = *r.Gap
		}
		for _, value := range []uint64{math.Float64bits(gap), math.Float64bits(r.Input), math.Float64bits(r.Output), math.Float64bits(r.Reasoning), math.Float64bits(r.Cache), uint64(r.Sequence), uint64(r.At.UnixNano())} {
			binary.LittleEndian.PutUint64(number[:], value)
			_, _ = h.Write(number[:])
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}
