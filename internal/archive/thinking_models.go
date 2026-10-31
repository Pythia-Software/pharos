package archive

import (
	"context"
	"math"
	"sort"
	"time"
)

// Fits are recomputed on the selected library cohort. There are no embedded
// study coefficients, model-name shortlists or silently substituted estimates.
type thinkingFactor struct {
	Model            string `json:"model"`
	Factor           string `json:"factor"`
	Ratio, Low, High float64
	N, Conversations int
}

func thinkingNumber(v any) float64 { n, _ := number(v); return n }
func thinkingFeatures(r thinkingRecord) []float64 {
	return []float64{math.Log(math.Max(1, r.Input)), math.Log1p(math.Min(r.Reasoning, r.Output)), math.Log1p(math.Max(0, r.Output-r.Reasoning)), r.Cache / math.Max(1, r.Input), math.Log1p(float64(r.Sequence))}
}
func thinkingDot(a, b []float64) float64 {
	v := 0.0
	for i := range a {
		v += a[i] * b[i]
	}
	return v
}
func thinkingInverse(a [][]float64) [][]float64 {
	n := len(a)
	v := make([][]float64, n)
	for i := range v {
		v[i] = make([]float64, n*2)
		copy(v[i], a[i])
		v[i][n+i] = 1
	}
	for i := 0; i < n; i++ {
		pivot := i
		for j := i + 1; j < n; j++ {
			if math.Abs(v[j][i]) > math.Abs(v[pivot][i]) {
				pivot = j
			}
		}
		if math.Abs(v[pivot][i]) < 1e-9 {
			return nil
		}
		v[i], v[pivot] = v[pivot], v[i]
		d := v[i][i]
		for j := 0; j < 2*n; j++ {
			v[i][j] /= d
		}
		for k := 0; k < n; k++ {
			if k == i {
				continue
			}
			scale := v[k][i]
			for j := 0; j < 2*n; j++ {
				v[k][j] -= scale * v[i][j]
			}
		}
	}
	out := make([][]float64, n)
	for i := range out {
		out[i] = append([]float64{}, v[i][n:]...)
	}
	return out
}
func thinkingNormal(x [][]float64, y []float64, ridge float64) ([]float64, [][]float64) {
	if len(x) == 0 {
		return nil, nil
	}
	n := len(x[0])
	gram := make([][]float64, n)
	rhs := make([]float64, n)
	for i := range gram {
		gram[i] = make([]float64, n)
		gram[i][i] = ridge
	}
	for k, row := range x {
		for i := range row {
			rhs[i] += row[i] * y[k]
			for j := range row {
				gram[i][j] += row[i] * row[j]
			}
		}
	}
	inverse := thinkingInverse(gram)
	if inverse == nil {
		return nil, nil
	}
	b := make([]float64, n)
	for i := range b {
		b[i] = thinkingDot(inverse[i], rhs)
	}
	return b, inverse
}
func thinkingModels(ctx context.Context, records []thinkingRecord, includeNormalization, includeFactors bool) []thinkingFactor {
	factors := []thinkingFactor{}
	if !includeNormalization && !includeFactors {
		return factors
	}
	groups := map[string][]int{}
	for i, r := range records {
		if r.Gap != nil && r.Input > 0 && r.Output > 0 {
			groups[r.Model] = append(groups[r.Model], i)
		}
	}
	names := []string{}
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if ctx.Err() != nil {
			return nil
		}
		ids := groups[name]
		conversations := map[string]bool{}
		efforts := map[string]bool{}
		for _, i := range ids {
			conversations[records[i].Conversation] = true
			efforts[records[i].Effort] = true
		}
		if len(ids) < 250 || len(conversations) < 25 {
			continue
		}
		levels := []string{}
		for level := range efforts {
			levels = append(levels, level)
		}
		sort.Strings(levels)
		features := func(r thinkingRecord) []float64 {
			v := append([]float64{1}, thinkingFeatures(r)...)
			for _, level := range levels[1:] {
				if level == r.Effort {
					v = append(v, 1)
				} else {
					v = append(v, 0)
				}
			}
			return v
		}
		// Five conversation-held-out folds. Efforts absent from training and numeric
		// feature extrapolations have no estimate, even when the ridge fit exists.
		for fold := 0; includeNormalization && fold < 5; fold++ {
			if ctx.Err() != nil {
				return nil
			}
			x := [][]float64{}
			y := []float64{}
			counts := map[string]bool{}
			seen := map[string]bool{}
			lo, hi := []float64{}, []float64{}
			for _, i := range ids {
				r := records[i]
				if thinkingFold(r.Conversation) == fold {
					continue
				}
				v := features(r)
				x = append(x, v)
				y = append(y, math.Log(*r.Gap))
				counts[r.Conversation] = true
				seen[r.Effort] = true
				if len(lo) == 0 {
					lo = append(lo, v...)
					hi = append(hi, v...)
				} else {
					for j := range v {
						lo[j] = math.Min(lo[j], v[j])
						hi[j] = math.Max(hi[j], v[j])
					}
				}
			}
			if len(x) < 200 || len(counts) < 20 {
				continue
			}
			b, _ := thinkingNormal(x, y, 10)
			if b == nil {
				continue
			}
			for _, i := range ids {
				r := &records[i]
				if thinkingFold(r.Conversation) != fold || !seen[r.Effort] {
					continue
				}
				v := features(*r)
				supported := true
				for j := 1; j <= 5; j++ {
					if v[j] < lo[j] || v[j] > hi[j] {
						supported = false
					}
				}
				if !supported {
					continue
				}
				ratio := *r.Gap / math.Exp(thinkingDot(v, b))
				if !math.IsNaN(ratio) && !math.IsInf(ratio, 0) && ratio > 0 {
					r.Ratio = &ratio
				}
			}
		}
		if !includeFactors {
			continue
		}
		// Absorb stream, UTC date and effort effects without allocating a
		// request-by-calendar-day matrix for an all-time library. Alternating
		// projections converge to the same least-squares residualization.
		streams := map[string][]int{}
		for _, i := range ids {
			r := records[i]
			key := r.Conversation + "|" + r.Stream
			streams[key] = append(streams[key], i)
		}
		x, y := [][]float64{}, []float64{}
		clusters := []string{}
		used := []int{}
		streamCount := 0
		keys := []string{}
		for k := range streams {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		categories := []map[string][]int{{}, {}, {}}
		for _, key := range keys {
			members := streams[key]
			if len(members) < 3 {
				continue
			}
			streamCount++
			for _, i := range members {
				r := records[i]
				at := len(x)
				x = append(x, thinkingFeatures(r))
				y = append(y, math.Log(*r.Gap))
				clusters = append(clusters, r.Conversation)
				used = append(used, i)
				for j, label := range []string{key, r.At.UTC().Format("2006-01-02"), r.Effort} {
					categories[j][label] = append(categories[j][label], at)
				}
			}
		}
		if len(x) < 250 {
			continue
		}
		partitions := [][][]int{}
		for _, category := range categories {
			names := []string{}
			for key := range category {
				names = append(names, key)
			}
			sort.Strings(names)
			partition := [][]int{}
			for _, key := range names {
				partition = append(partition, category[key])
			}
			partitions = append(partitions, partition)
		}
		converged := false
		for iteration := 0; iteration < 200; iteration++ {
			if ctx.Err() != nil {
				return nil
			}
			shift := 0.0
			for _, partition := range partitions {
				for _, members := range partition {
					means := make([]float64, len(x[0]))
					meanY := 0.0
					for _, i := range members {
						meanY += y[i] / float64(len(members))
						for j, v := range x[i] {
							means[j] += v / float64(len(members))
						}
					}
					shift = math.Max(shift, math.Abs(meanY))
					for _, v := range means {
						shift = math.Max(shift, math.Abs(v))
					}
					for _, i := range members {
						y[i] -= meanY
						for j := range x[i] {
							x[i][j] -= means[j]
						}
					}
				}
			}
			if shift < 1e-8 {
				converged = true
				break
			}
		}
		if !converged {
			continue
		} // Never publish an unconverged factor table.
		// Modified Gram–Schmidt selects an independent control basis. Factor columns
		// are visited first; omitted columns never receive fabricated confidence CIs.
		keep := []int{}
		basis := [][]float64{}
		for col := range x[0] {
			v := make([]float64, len(x))
			norm := 0.0
			for i := range x {
				v[i] = x[i][col]
				norm += v[i] * v[i]
			}
			if norm < 1e-10 {
				continue
			}
			for repeat := 0; repeat < 2; repeat++ {
				for _, q := range basis {
					dot := thinkingDot(v, q)
					for i := range v {
						v[i] -= dot * q[i]
					}
				}
			}
			rest := thinkingDot(v, v)
			if rest < norm*1e-10 {
				continue
			}
			for i := range v {
				v[i] /= math.Sqrt(rest)
			}
			basis = append(basis, v)
			keep = append(keep, col)
		}
		for i, v := range x {
			next := []float64{}
			for _, col := range keep {
				next = append(next, v[col])
			}
			x[i] = next
		}
		if len(keep) == 0 {
			continue
		}
		b, bread := thinkingNormal(x, y, 0)
		if b == nil {
			continue
		}
		scores := map[string][]float64{}
		for i, row := range x {
			score := scores[clusters[i]]
			if score == nil {
				score = make([]float64, len(b))
			}
			resid := y[i] - thinkingDot(row, b)
			for j, v := range row {
				score[j] += v * resid
			}
			scores[clusters[i]] = score
		}
		if len(scores) < 25 {
			continue
		}
		adjust := float64(len(scores)) / float64(len(scores)-1) * float64(len(x)-1) / float64(max(1, len(x)-streamCount-len(categories[1])-len(categories[2])+2-len(b)))
		for j, col := range keep {
			if col > 2 {
				continue
			}
			values := []float64{}
			for _, i := range used {
				r := records[i]
				v := r.Input
				if col == 1 {
					v = math.Min(r.Reasoning, r.Output)
				}
				if col == 2 {
					v = math.Max(0, r.Output-r.Reasoning)
				}
				if v > 0 {
					values = append(values, v)
				}
			}
			if len(values) < 50 {
				continue
			}
			delta := math.Log(2.0)
			if col > 0 {
				ref := thinkingMedian(values)
				delta = math.Log1p(2*ref) - math.Log1p(ref)
			}
			variance := 0.0
			for _, score := range scores {
				v := thinkingDot(bread[j], score)
				variance += v * v
			}
			se := math.Sqrt(variance * adjust)
			ratio, low, high := math.Exp(b[j]*delta), math.Exp((b[j]-1.96*se)*delta), math.Exp((b[j]+1.96*se)*delta)
			if math.IsInf(high, 0) || math.IsNaN(high) {
				continue
			}
			factors = append(factors, thinkingFactor{name, []string{"context", "reasoning", "other"}[col], ratio, low, high, len(x), len(scores)})
		}
	}
	return factors
}

type thinkingCompaction struct {
	ID, Model, Effort, Conversation     string
	Gap, Context, Output, Before, After float64
}

func thinkingCompactions(records []thinkingRecord) []thinkingCompaction {
	streams := map[string][]int{}
	for i, r := range records {
		key := r.Conversation + "|" + r.Stream
		streams[key] = append(streams[key], i)
	}
	out := []thinkingCompaction{}
	for _, rs := range streams {
		sort.Slice(rs, func(i, j int) bool {
			if records[rs[i]].Sequence == records[rs[j]].Sequence {
				return records[rs[i]].ID < records[rs[j]].ID
			}
			return records[rs[i]].Sequence < records[rs[j]].Sequence
		})
		for i, index := range rs {
			r := records[index]
			if !r.Compaction || i < 3 || i+3 > len(rs) {
				continue
			}
			good := true
			var inputsBefore, inputsAfter, outputsBefore, outputsAfter, gapsBefore, gapsAfter []float64
			for j, index := range rs[i-3 : i+3] {
				p := records[index]
				if p.Model != r.Model || p.Effort != r.Effort || p.At.IsZero() || p.At.Sub(records[rs[i-3]].At) > time.Hour || (p.Compaction && j != 3) || (j > 0 && p.Previous != "" && p.Previous != records[rs[i-3+j-1]].ID) {
					good = false
				}
				if j < 3 {
					inputsBefore = append(inputsBefore, p.Input)
					outputsBefore = append(outputsBefore, p.Output)
					if p.Gap != nil {
						gapsBefore = append(gapsBefore, *p.Gap)
					}
				} else {
					inputsAfter = append(inputsAfter, p.Input)
					outputsAfter = append(outputsAfter, p.Output)
					// The first response after compaction can span the compaction
					// itself. Keep its token measurements, but use only later gaps.
					if j > 3 && p.Gap != nil {
						gapsAfter = append(gapsAfter, *p.Gap)
					}
				}
			}
			if !good || len(gapsBefore) < 2 || len(gapsAfter) < 2 || thinkingMedian(inputsBefore) <= 0 {
				continue
			}
			b, a := thinkingMedian(gapsBefore), thinkingMedian(gapsAfter)
			out = append(out, thinkingCompaction{r.ID, r.Model, r.Effort, r.Conversation, a / b, thinkingMedian(inputsAfter) / thinkingMedian(inputsBefore), (thinkingMedian(outputsAfter) + 1) / (thinkingMedian(outputsBefore) + 1), b, a})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

type thinkingTracePoint struct {
	ID                     string
	At                     string
	Rank                   int
	Context, Cache, Output float64
	Gap, Smooth            *float64
	Compaction             bool
}
type thinkingTrace struct {
	ID, Model, Conversation, Workspace string
	Count                              int
	Points                             []thinkingTracePoint
}

func thinkingTraces(records []thinkingRecord) []thinkingTrace {
	streams := map[string][]int{}
	for i, r := range records {
		key := r.Conversation + "|" + r.Stream
		streams[key] = append(streams[key], i)
	}
	keys := []string{}
	for k, rs := range streams {
		if len(rs) >= 6 {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(streams[keys[i]]) == len(streams[keys[j]]) {
			return keys[i] < keys[j]
		}
		return len(streams[keys[i]]) > len(streams[keys[j]])
	})
	out := []thinkingTrace{}
	for _, k := range keys[:min(12, len(keys))] {
		rs := streams[k]
		sort.Slice(rs, func(i, j int) bool {
			if records[rs[i]].Sequence == records[rs[j]].Sequence {
				return records[rs[i]].ID < records[rs[j]].ID
			}
			return records[rs[i]].Sequence < records[rs[j]].Sequence
		})
		t := thinkingTrace{ID: k, Model: records[rs[0]].Model, Conversation: records[rs[0]].Conversation, Workspace: records[rs[0]].Workspace, Count: len(rs), Points: []thinkingTracePoint{}}
		start := 0
		for i, index := range rs[:min(len(rs), 1000)] {
			r := records[index]
			if r.Compaction {
				start = i
			}
			recent := []float64{}
			for j := max(start, i-10); j <= i; j++ {
				if records[rs[j]].Gap != nil {
					recent = append(recent, *records[rs[j]].Gap)
				}
			}
			var smooth *float64
			if len(recent) >= 6 && r.Gap != nil {
				v := 0.0
				for _, n := range recent {
					v += n
				}
				v /= float64(len(recent))
				smooth = &v
			}
			t.Points = append(t.Points, thinkingTracePoint{r.ID, thinkingStamp(r.At), i + 1, r.Input, r.Cache, r.Output, r.Gap, smooth, r.Compaction})
		}
		out = append(out, t)
	}
	return out
}
