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
	"sync"
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

// Native WHERE remains compiled/executed by the existing SQL dataset, over the
// stored timing projection, which is narrower than the request ledger.
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
	tx, release, err := beginQueryWithTempStore(ctx, c.reader(), 1)
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := tx.QueryContext(ctx, "SELECT d.id FROM ("+thinkingSource+") d WHERE "+strings.TrimPrefix(o.FilterSQL, " WHERE "), o.FilterArgs...)
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
	// Include fitted inputs as well as identity: a source commit can land between
	// acquiring the immutable population and acquiring this cache's generation.
	// An older in-flight cohort must never seed a fit for updated observations.
	// Chunks hash in parallel, since an all-time cohort has a million records.
	const chunk = 1 << 16
	sums := make([][]byte, (len(rs)+chunk-1)/chunk)
	var wg sync.WaitGroup
	for n := range sums {
		wg.Go(func() { sums[n] = thinkingRecordsHash(rs[n*chunk : min(len(rs), (n+1)*chunk)]) })
	}
	wg.Wait()
	h := sha256.New()
	for _, sum := range sums {
		_, _ = h.Write(sum)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func thinkingRecordsHash(rs []thinkingRecord) []byte {
	h := sha256.New()
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
	return h.Sum(nil)
}
