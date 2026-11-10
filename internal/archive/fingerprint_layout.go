package archive

import (
	"context"
	"math"
	"sort"
	"unicode/utf16"
)

const fingerprintWidth = 320.
const fingerprintGap = 1.5

var fingerprintRadius = float64(320) / 140
var fingerprintToolRadius = fingerprintRadius * .82 * 1.1

const fingerprintToolTailRadius = .6

type fingerprintArc struct {
	key                string
	x0, x1, lift, rise float64
	level, side        int
	returning, weak    bool
}
type fingerprintMark struct {
	id, arc     string
	x, x1, y, r float64
	overlap     bool
}
type fingerprintGeometry struct {
	arcs  []fingerprintArc
	marks []fingerprintMark
	axis  float64
}
type fingerprintLayout struct {
	prepared      *fingerprintPrepared
	profile       string
	height        float64
	geometry      fingerprintGeometry
	selected      []fingerprintCandidate
	tools         []fingerprintToolCandidate
	kinds, phases int
}

// Match the engine's normalized Kahan hypot. The choice of a lane can depend
// on the last bit near a collision boundary, even though SVG rounds to 0.01.
func fingerprintHypot(a, b float64) float64 {
	a, b = math.Abs(a), math.Abs(b)
	m := math.Max(a, b)
	if m == 0 {
		return 0
	}
	a /= m
	b /= m
	sum := float64(a * a)
	next := sum + float64(b*b)
	return math.Sqrt(next) * m
}
func fingerprintArcY(a fingerprintArc, axis, x float64) float64 {
	distance := x - a.x0
	if a.returning {
		distance = a.x1 - x
	}
	u := fingerprintClamp(distance / a.rise)
	// Explicit conversions forbid fused multiply-add: JS rounds each operator.
	eased := 1 - float64((1-u)*(1-u))
	displacement := float64(float64(a.side)*a.lift) * eased
	return axis - float64(displacement)
}
func fingerprintExtent(radius float64) float64 { return float64(radius*1.18) + .5 }
func fingerprintMarkGap(a, b fingerprintMark) float64 {
	dx := math.Max(0, math.Max(a.x-b.x1, b.x-a.x1))
	return fingerprintHypot(dx, a.y-b.y) - a.r - b.r
}
func fingerprintLanes(height, radius float64) []float64 {
	pitch, axis := float64(2*radius)+fingerprintGap, height/2
	lanes := []float64{axis}
	for k := 1.; axis-float64(k*pitch) >= radius-.5 && axis+float64(k*pitch) <= height-radius+.5; k++ {
		lanes = append(lanes, axis-float64(k*pitch), axis+float64(k*pitch))
	}
	return lanes
}
func fingerprintEventGeometry(p *fingerprintPrepared, candidates []fingerprintCandidate, links map[string]bool, height float64) fingerprintGeometry {
	type group struct {
		key    string
		x0     float64
		events []fingerprintCandidate
	}
	groups := map[string]*group{}
	list := []*group{}
	for _, c := range candidates {
		if !links[c.event.ID] {
			continue
		}
		g := groups[c.target]
		if g == nil {
			g = &group{key: c.target, x0: float64(c.targetX * fingerprintWidth)}
			groups[c.target] = g
			list = append(list, g)
		}
		g.events = append(g.events, c)
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].x0 != list[j].x0 {
			return list[i].x0 < list[j].x0
		}
		return p.compare.CompareString(list[i].key, list[j].key) < 0
	})
	geometry := fingerprintGeometry{axis: height / 2}
	marker := fingerprintRadius * 1.1
	maxLift := height/2 - float64(fingerprintRadius*1.1) - 2
	minLift := math.Min(maxLift, float64(fingerprintRadius*2.2)+1)
	for _, g := range list {
		end := g.x0 + float64(marker*3)
		weak := true
		for _, c := range g.events {
			end = math.Max(end, c.x*fingerprintWidth)
			weak = weak && c.strength == 1
		}
		geometry.arcs = append(geometry.arcs, fingerprintArc{key: g.key, x0: g.x0, x1: math.Min(fingerprintWidth, end), weak: weak, level: 1})
	}
	order := make([]int, len(list))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := geometry.arcs[order[i]], geometry.arcs[order[j]]
		if a.x0 != b.x0 {
			return a.x0 > b.x0
		}
		return order[i] > order[j]
	})
	depth := 0
	for k, i := range order {
		inner := 0
		for _, j := range order[:k] {
			if geometry.arcs[j].x0 >= geometry.arcs[i].x0 && geometry.arcs[j].x0 <= geometry.arcs[i].x1 {
				inner = max(inner, geometry.arcs[j].level)
			}
		}
		geometry.arcs[i].level = inner + 1
		depth = max(depth, inner+1)
	}
	split := depth > 2
	sideDepth := depth
	if split {
		sideDepth = (depth + 1) / 2
	}
	byKey := map[string]fingerprintArc{}
	for i := range geometry.arcs {
		a := &geometry.arcs[i]
		rank := a.level
		if split {
			rank = (rank + 1) / 2
		}
		a.lift = (minLift + maxLift) / 2
		if sideDepth > 1 {
			a.lift = minLift + float64((maxLift-minLift)*float64(rank-1)/float64(sideDepth-1))
		}
		a.rise = a.lift * math.Max(3, math.Min(7, fingerprintWidth/90))
		hash := uint32(2166136261)
		for _, code := range utf16.Encode([]rune(a.key)) {
			hash = (hash ^ uint32(code)) * 16777619
		}
		a.side = 1
		if hash&1 != 0 {
			a.side = -1
		}
		a.returning = hash&2 != 0
		byKey[a.key] = *a
	}
	ordered := append([]fingerprintCandidate(nil), candidates...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].x != ordered[j].x {
			return ordered[i].x < ordered[j].x
		}
		return fingerprintIDLess(ordered[i].event.ID, ordered[j].event.ID)
	})
	// The grid traversal order is preserved because overlap costs are summed in
	// cell order; rounding differences in a crowded lane must not change ties.
	grid := map[int][]int{}
	reach := 0.
	radii := map[string]float64{}
	for _, c := range ordered {
		radius := fingerprintRadius
		if c.target != "" {
			radius *= 1.1
		}
		radii[c.event.ID] = radius
		r := fingerprintExtent(radius)
		x := float64(c.x * fingerprintWidth)
		ys := fingerprintLanes(height, r)
		if arc, ok := byKey[c.target]; c.target != "" && links[c.event.ID] && ok {
			ys = []float64{fingerprintArcY(arc, geometry.axis, x)}
		}
		bestCost := math.Inf(1)
		var best fingerprintMark
		for _, y := range ys {
			mark := fingerprintMark{id: c.event.ID, x: x, x1: x, y: y, r: r}
			total := 0.
			seen := map[int]bool{}
			for cell := int(math.Floor((x - r - reach - fingerprintGap) / 24)); cell <= int(math.Floor((x+r+reach+fingerprintGap)/24)); cell++ {
				for _, i := range grid[cell] {
					if seen[i] {
						continue
					}
					seen[i] = true
					d := fingerprintMarkGap(mark, geometry.marks[i])
					if d < fingerprintGap-1e-9 {
						total += fingerprintGap - d
					}
				}
			}
			if total < bestCost-1e-9 {
				bestCost = total
				best = mark
				best.overlap = total > 0
				if total == 0 {
					break
				}
			}
		}
		index := len(geometry.marks)
		geometry.marks = append(geometry.marks, best)
		reach = math.Max(reach, r)
		grid[int(math.Floor(x/24))] = append(grid[int(math.Floor(x/24))], index)
		geometry.marks[index].arc = ""
		if links[c.event.ID] {
			geometry.marks[index].arc = c.target
		}
	}
	// placeMarks used extents during placement; public marks retain glyph radii.
	for i := range geometry.marks {
		geometry.marks[i].r = radii[geometry.marks[i].id]
	}
	return geometry
}
func fingerprintCoverage(marks []fingerprintMark, height float64, columns int) bool {
	cell := fingerprintWidth / float64(max(1, columns))
	covered := make([]float64, columns)
	limit := cell * height * .28
	for _, m := range marks {
		lo := max(0, int(math.Floor((m.x-m.r)/cell)))
		hi := min(columns-1, int(math.Floor((m.x1+m.r)/cell)))
		for i := lo; i <= hi; i++ {
			covered[i] += float64(float64(math.Max(0, math.Min(float64(float64(i+1)*cell), m.x1+m.r)-math.Max(float64(float64(i)*cell), m.x-m.r))*m.r) * 2)
			if covered[i] > limit {
				return false
			}
		}
	}
	const stacks = 10
	if len(marks) <= stacks {
		return true
	}
	starts, ends := make([]float64, len(marks)), make([]float64, len(marks))
	for i, m := range marks {
		starts[i], ends[i] = m.x-m.r, m.x1+m.r
	}
	sort.Float64s(starts)
	sort.Float64s(ends)
	start, end, stacked := 0, 0, 0
	for start < len(starts) {
		if ends[end] <= starts[start] {
			end++
			stacked--
		} else {
			start++
			stacked++
			if stacked > stacks {
				return false
			}
		}
	}
	return true
}
func fingerprintExtentMarks(geometry fingerprintGeometry) []fingerprintMark {
	marks := append([]fingerprintMark(nil), geometry.marks...)
	for i := range marks {
		marks[i].r = fingerprintExtent(marks[i].r)
	}
	return marks
}
func fingerprintClearGeometry(geometry fingerprintGeometry, height float64, columns int) bool {
	for _, a := range geometry.arcs {
		if a.level > 2 {
			return false
		}
	}
	marks := fingerprintExtentMarks(geometry)
	for _, m := range marks {
		if m.overlap || m.x-m.r < 0 || m.x+m.r > fingerprintWidth || m.y-m.r < 0 || m.y+m.r > height {
			return false
		}
	}
	if !fingerprintCoverage(marks, height, columns) {
		return false
	}
	reach := 0.
	for _, m := range marks {
		reach = math.Max(reach, m.r)
	}
	for i, a := range marks {
		for _, b := range marks[i+1:] {
			if b.x-a.x >= a.r+reach+1 {
				break
			}
			if fingerprintMarkGap(a, b) < 1 {
				return false
			}
		}
	}
	for i, a := range geometry.arcs {
		for _, b := range geometry.arcs[i+1:] {
			if math.Abs(a.x0-b.x0) < 2.5 {
				return false
			}
			for x := math.Max(a.x0, b.x0); x <= math.Min(a.x1, b.x1); x += .5 {
				if math.Abs(fingerprintArcY(a, geometry.axis, x)-fingerprintArcY(b, geometry.axis, x)) < fingerprintGap {
					return false
				}
			}
		}
	}
	for _, a := range geometry.arcs {
		for _, m := range marks {
			if m.arc == a.key {
				continue
			}
			for x := math.Max(a.x0, m.x-m.r-1); x <= math.Min(a.x1, m.x+m.r+1); x += .4 {
				if fingerprintHypot(x-m.x, fingerprintArcY(a, geometry.axis, x)-m.y) < m.r+1 {
					return false
				}
			}
			if math.Abs(m.x-a.x0) < m.r+1 && math.Abs(m.y-fingerprintArcY(a, geometry.axis, a.x0)) < m.r+3 {
				return false
			}
		}
	}
	return true
}
func fingerprintClearTool(mark fingerprintMark, occupied []fingerprintMark, geometry fingerprintGeometry, height float64, columns int, extra ...fingerprintMark) bool {
	if mark.x-mark.r < 0 || mark.x1+mark.r > fingerprintWidth || mark.y-mark.r < 0 || mark.y+mark.r > height {
		return false
	}
	for _, m := range occupied {
		if fingerprintMarkGap(mark, m) < fingerprintGap {
			return false
		}
	}
	coverage := make([]fingerprintMark, 0, len(occupied)+len(extra)+1)
	coverage = append(coverage, occupied...)
	coverage = append(coverage, extra...)
	coverage = append(coverage, mark)
	if !fingerprintCoverage(coverage, height, columns) {
		return false
	}
	for _, arc := range geometry.arcs {
		if arc.x0 > mark.x-mark.r-1 && arc.x0 < mark.x1+mark.r+1 && math.Abs(fingerprintArcY(arc, geometry.axis, arc.x0)-mark.y) < mark.r+3 {
			return false
		}
		for x := math.Max(arc.x0, mark.x-mark.r-1); x <= math.Min(arc.x1, mark.x1+mark.r+1); x += .4 {
			dx := math.Max(0, math.Max(mark.x-x, x-mark.x1))
			if fingerprintHypot(dx, fingerprintArcY(arc, geometry.axis, x)-mark.y) < mark.r+1 {
				return false
			}
		}
	}
	return true
}
func layoutFingerprint(ctx context.Context, p *fingerprintPrepared, profile string, events bool) (fingerprintLayout, error) {
	height := 40.
	arcLimit := 4
	if profile == "card" {
		height = 80
		arcLimit = 6
	}
	result := fingerprintLayout{prepared: p, profile: profile, height: height}
	links := map[string]bool{}
	geometry := fingerprintEventGeometry(p, nil, links, height)
	rejected := map[string]bool{}
	kinds, groups, targets, sessions := map[string]int{}, map[string]bool{}, map[string]int{}, map[string]bool{}
	phases := map[int]bool{}
	type scored struct {
		candidate fingerprintCandidate
		score     float64
	}
	ranked := []scored{}
	rank := 0
	rerank := true
	if events {
		for len(result.selected) < 500 {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			if rerank {
				ranked = ranked[:0]
				for _, c := range p.candidates {
					if rejected[c.event.ID] {
						continue
					}
					score := float64(c.strength * 100)
					if kinds[c.event.Kind] == 0 {
						score += 28
					}
					if !groups[c.event.Group] {
						score += 18
					}
					if c.target != "" && targets[c.target] == 0 {
						score += 22
					}
					if !phases[c.phase] {
						score += 24
					}
					if !sessions[c.session] {
						score += 12
					}
					score -= float64(kinds[c.event.Kind] * 5)
					if c.target != "" {
						score -= float64(targets[c.target] * 3)
					}
					score += fingerprintClamp(c.event.Probability)
					ranked = append(ranked, scored{c, score})
				}
				sort.SliceStable(ranked, func(i, j int) bool {
					if ranked[i].score != ranked[j].score {
						return ranked[i].score > ranked[j].score
					}
					return fingerprintIDLess(ranked[i].candidate.event.ID, ranked[j].candidate.event.ID)
				})
				rank = 0
				rerank = false
			}
			if rank == len(ranked) {
				break
			}
			best := ranked[rank].candidate
			rank++
			rejected[best.event.ID] = true
			x := float64(best.x * fingerprintWidth)
			r := fingerprintRadius
			if best.target != "" {
				r *= 1.1
			}
			r = fingerprintExtent(r)
			if x-r < 0 || x+r > fingerprintWidth {
				continue
			}
			coverage := fingerprintExtentMarks(geometry)
			coverage = append(coverage, fingerprintMark{x: x, x1: x, r: r})
			if !fingerprintCoverage(coverage, height, len(p.bands)) {
				continue
			}
			nextLinks := map[string]bool{}
			for id := range links {
				nextLinks[id] = true
			}
			hasArc := false
			for _, a := range geometry.arcs {
				if a.key == best.target {
					hasArc = true
					break
				}
			}
			if best.target != "" && (hasArc || len(geometry.arcs) < arcLimit) {
				nextLinks[best.event.ID] = true
			}
			selected := append(result.selected, best)
			next := fingerprintEventGeometry(p, selected, nextLinks, height)
			if !fingerprintClearGeometry(next, height, len(p.bands)) {
				if !nextLinks[best.event.ID] {
					continue
				}
				delete(nextLinks, best.event.ID)
				next = fingerprintEventGeometry(p, selected, nextLinks, height)
				if !fingerprintClearGeometry(next, height, len(p.bands)) {
					continue
				}
			}
			result.selected = selected
			geometry = next
			links = nextLinks
			rerank = true
			kinds[best.event.Kind]++
			groups[best.event.Group] = true
			phases[best.phase] = true
			sessions[best.session] = true
			if best.target != "" {
				targets[best.target]++
			}
		}
	}
	result.geometry = geometry
	result.kinds, result.phases = len(kinds), len(phases)
	toolLimit := 8
	if len(result.selected) >= 4 {
		toolLimit = 4
	}
	eventMarks := fingerprintExtentMarks(geometry)
	longest := 1.
	for _, t := range p.tools {
		if !math.IsNaN(t.duration) {
			longest = math.Max(longest, t.duration)
		}
	}
	dot := func(t fingerprintToolCandidate) fingerprintMark {
		return fingerprintMark{x: t.drawX, x1: t.drawX, y: t.y, r: fingerprintToolRadius + .5}
	}
	tailEnd := func(t fingerprintToolCandidate) float64 {
		if math.IsNaN(t.endX) {
			return t.x * fingerprintWidth
		}
		return math.Min(fingerprintWidth-fingerprintToolTailRadius, t.endX*fingerprintWidth)
	}
	skipped, categories, toolPhases := map[string]bool{}, map[string]bool{}, map[int]bool{}
	type scoredTool struct {
		candidate fingerprintToolCandidate
		score     float64
	}
	rankedTools := []scoredTool{}
	rank = 0
	rerank = true
	for len(result.tools) < toolLimit {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if rerank {
			rankedTools = rankedTools[:0]
			for _, t := range p.tools {
				if skipped[t.tool.ID] {
					continue
				}
				duration := t.duration
				if math.IsNaN(duration) {
					duration = 0
				}
				score := 90 * math.Log1p(duration) / math.Log1p(longest)
				if t.failure {
					score += 1000
				}
				if !categories[t.tool.Category] {
					score += 30
				}
				if !toolPhases[t.phase] {
					score += 22
				}
				rankedTools = append(rankedTools, scoredTool{t, score})
			}
			sort.SliceStable(rankedTools, func(i, j int) bool {
				if rankedTools[i].score != rankedTools[j].score {
					return rankedTools[i].score > rankedTools[j].score
				}
				return fingerprintIDLess(rankedTools[i].candidate.tool.ID, rankedTools[j].candidate.tool.ID)
			})
			rank = 0
			rerank = false
		}
		if rank == len(rankedTools) {
			break
		}
		best := rankedTools[rank].candidate
		rank++
		skipped[best.tool.ID] = true
		best.drawX = best.x * fingerprintWidth
		occupied := append([]fingerprintMark(nil), eventMarks...)
		for _, t := range result.tools {
			occupied = append(occupied, dot(t))
		}
		lanes := []float64{}
		for _, y := range fingerprintLanes(height, fingerprintToolRadius+.5) {
			best.y = y
			if fingerprintClearTool(dot(best), occupied, geometry, height, len(p.bands)) {
				lanes = append(lanes, y)
			}
		}
		if len(lanes) == 0 {
			continue
		}
		best.y = lanes[0]
		end := tailEnd(best)
		for _, y := range lanes {
			candidate := best
			candidate.y = y
			if end-best.drawX > fingerprintToolRadius+2 && fingerprintClearTool(fingerprintMark{x: best.drawX, x1: end, y: y, r: fingerprintToolTailRadius}, occupied, geometry, height, len(p.bands), dot(candidate)) {
				best.y = y
				break
			}
		}
		best.drawEndX = best.drawX
		result.tools = append(result.tools, best)
		categories[best.tool.Category] = true
		toolPhases[best.phase] = true
		rerank = true
	}
	tails := []fingerprintMark{}
	for i := range result.tools {
		tool := &result.tools[i]
		end := tailEnd(*tool)
		if end-tool.drawX <= fingerprintToolRadius+2 {
			continue
		}
		others := append([]fingerprintMark(nil), eventMarks...)
		for j, t := range result.tools {
			if i != j {
				others = append(others, dot(t))
			}
		}
		others = append(others, tails...)
		ys := append([]float64{tool.y}, fingerprintLanes(height, fingerprintToolRadius+.5)...)
		seen := map[float64]bool{}
		for _, y := range ys {
			if seen[y] {
				continue
			}
			seen[y] = true
			candidate := *tool
			candidate.y = y
			tail := fingerprintMark{x: tool.drawX, x1: end, y: y, r: fingerprintToolTailRadius}
			if fingerprintClearTool(dot(candidate), others, geometry, height, len(p.bands)) && fingerprintClearTool(tail, others, geometry, height, len(p.bands), dot(candidate)) {
				tool.y = y
				tool.drawEndX = end
				tails = append(tails, tail)
				break
			}
		}
	}
	return result, nil
}

// Stable selection ties use JS's UTF-16 code-unit order, independently of the
// locale collation used for labels and merged event kinds. ASCII IDs take the
// byte path; supplementary characters sort before BMP U+E000..U+FFFF.
func fingerprintIDLess(a, b string) bool {
	for i := 0; i < min(len(a), len(b)); i++ {
		if a[i] == b[i] {
			continue
		}
		if a[i] >= 0xf0 && b[i] >= 0xee && b[i] <= 0xef {
			return true
		}
		if b[i] >= 0xf0 && a[i] >= 0xee && a[i] <= 0xef {
			return false
		}
		return a[i] < b[i]
	}
	return len(a) < len(b)
}
