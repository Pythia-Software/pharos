package archive

// This is the native counterpart of compact-fingerprint.ts's production input
// projection. It intentionally does not implement arbitrary SVG or JS layouts.
import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

type fingerprintInput struct {
	Conversation struct {
		ID          string               `json:"id"`
		MainSession *string              `json:"main_session"`
		Messages    []fingerprintMessage `json:"messages"`
		Sessions    []fingerprintSession `json:"sessions"`
		Activity    []fingerprintTool    `json:"activity"`
	} `json:"conversation"`
	Events   []fingerprintEvent `json:"events"`
	Taxonomy []struct {
		ID string `json:"id"`
	} `json:"taxonomy"`
}
type fingerprintMessage struct {
	ID          string               `json:"id"`
	At          string               `json:"at"`
	Session     *string              `json:"session_id"`
	Chunks      float64              `json:"chunks"`
	Labels      [][2]json.RawMessage `json:"labels"`
	states      []string
	parallelism int
}
type fingerprintSession struct {
	ID    string `json:"id"`
	Depth int    `json:"depth"`
}
type fingerprintTool struct {
	ID       string   `json:"id"`
	Session  string   `json:"session_id"`
	Message  string   `json:"message_id"`
	Category string   `json:"category"`
	Status   string   `json:"status"`
	Start    string   `json:"start"`
	End      string   `json:"end"`
	Duration *float64 `json:"duration_ms"`
}
type fingerprintTarget struct {
	Message string `json:"message_id"`
	Method  string `json:"method"`
	Strong  bool   `json:"strong"`
}
type fingerprintEvent struct {
	ID          string             `json:"id"`
	Message     string             `json:"message_id"`
	Kind        string             `json:"kind"`
	Group       string             `json:"group"`
	Probability float64            `json:"probability"`
	Span        []float64          `json:"span"`
	Target      *fingerprintTarget `json:"target"`
	Members     []fingerprintEvent `json:"members"`
}
type fingerprintCandidate struct {
	event           *fingerprintEvent
	x, targetX      float64
	target, session string
	strength, phase int
}
type fingerprintToolCandidate struct {
	tool               *fingerprintTool
	x, endX, duration  float64 // NaN denotes absent timing.
	phase              int
	failure            bool
	drawX, drawEndX, y float64
}
type fingerprintSlice struct {
	color string
	share float64
}
type fingerprintBand struct {
	x, width    float64
	slices      []fingerprintSlice
	parallelism int
}
type fingerprintPrepared struct {
	bands                                 []fingerprintBand
	candidates                            []fingerprintCandidate
	tools                                 []fingerprintToolCandidate
	messages, events, toolCount, branches int
	compare                               *collate.Collator // Per render: collators have mutable scratch.
}
type fingerprintObservation struct {
	message *fingerprintMessage
	tool    *fingerprintTool
	at, x   float64
	order   int
}

func fingerprintTime(value string) float64 {
	if value == "" {
		return math.NaN()
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999", "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return float64(parsed.UnixMilli())
		}
	}
	return math.NaN()
}
func fingerprintToolTiming(t *fingerprintTool) (duration, end float64) {
	start, finish := fingerprintTime(t.Start), fingerprintTime(t.End)
	duration = math.NaN()
	if !math.IsNaN(start) && !math.IsNaN(finish) && finish >= start {
		duration = finish - start
	} else if t.Duration != nil && *t.Duration >= 0 {
		duration = *t.Duration
	}
	end = math.NaN()
	if !math.IsNaN(start) && !math.IsNaN(duration) {
		end = start + duration
	}
	return
}
func fingerprintPhase(x float64) int     { return int(math.Min(7, math.Max(0, math.Floor(x*8)))) }
func fingerprintClamp(x float64) float64 { return math.Max(0, math.Min(1, x)) }
func fingerprintGroup(group string) int {
	switch group {
	case "Friction":
		return 0
	case "Reactions and learning":
		return 1
	case "Worth a second look":
		return 2
	case "Choices you may not see":
		return 3
	}
	return 4
}
func fingerprintFamily(id string) int {
	if id == "P" || id == "C" {
		return -1
	}
	parts := strings.Split(id, ".")
	if len(parts) > 2 {
		parts = parts[:2]
	}
	switch strings.Join(parts, ".") {
	case "P.request":
		return 0
	case "P.frame":
		return 1
	case "P.control":
		return 2
	case "C.input":
		return 3
	case "C.feedback":
		return 4
	case "C.deliberation":
		return 5
	case "C.report":
		return 6
	case "C.difficulty":
		return 7
	}
	return 8
}
func (p *fingerprintPrepared) compareStates(a, b string) int {
	ar, br := 1, 1
	if a == "P" || strings.HasPrefix(a, "P.") {
		ar = 0
	}
	if b == "P" || strings.HasPrefix(b, "P.") {
		br = 0
	}
	if ar != br {
		return ar - br
	}
	if d := fingerprintFamily(a) - fingerprintFamily(b); d != 0 {
		return d
	}
	return p.compare.CompareString(a, b)
}
func (p *fingerprintPrepared) states(m *fingerprintMessage) []string {
	if m.Chunks == 0 {
		return nil
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, label := range m.Labels {
		var id string
		if json.Unmarshal(label[0], &id) == nil && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	states := []string{}
	for _, id := range ids {
		if id != "P" && id != "C" && !strings.HasPrefix(id, "P.") && !strings.HasPrefix(id, "C.") {
			continue
		}
		specific := true
		for _, other := range ids {
			if strings.HasPrefix(other, id+".") {
				specific = false
				break
			}
		}
		if specific {
			states = append(states, id)
		}
	}
	sort.SliceStable(states, func(i, j int) bool { return p.compareStates(states[i], states[j]) < 0 })
	return states
}
func (p *fingerprintPrepared) ribbonSlices(messages []*fingerprintMessage, taxonomy []string) []fingerprintSlice {
	definitions := append([]string(nil), taxonomy...)
	known := map[string]bool{}
	for _, id := range definitions {
		known[id] = true
	}
	for _, m := range messages {
		for _, id := range m.states {
			if !known[id] {
				known[id] = true
				definitions = append(definitions, id)
			}
		}
	}
	weights := map[string]float64{}
	children := map[string][]string{}
	terminals := map[string]bool{}
	add := func(path []string, weight float64) {
		for i, id := range path {
			weights[id] += weight
			if i > 0 {
				parent := path[i-1]
				found := false
				for _, c := range children[parent] {
					if c == id {
						found = true
						break
					}
				}
				if !found {
					children[parent] = append(children[parent], id)
				}
			}
		}
		terminals[path[len(path)-1]] = true
	}
	for _, m := range messages {
		if len(m.states) == 0 {
			id := "unlabeled"
			if m.Chunks != 0 {
				id = "no-match"
			}
			add([]string{"all", "uncategorized", id}, 1)
			continue
		}
		roots := []string{}
		for _, root := range []string{"P", "C"} {
			for _, id := range m.states {
				if id == root || strings.HasPrefix(id, root+".") {
					roots = append(roots, root)
					break
				}
			}
		}
		for _, root := range roots {
			ids := []string{}
			for _, id := range m.states {
				if id == root || strings.HasPrefix(id, root+".") {
					ids = append(ids, id)
				}
			}
			for _, id := range ids {
				parts := strings.Split(id, ".")
				path := []string{"all"}
				for i := range parts {
					path = append(path, strings.Join(parts[:i+1], "."))
				}
				other := id == root
				for _, d := range definitions {
					if strings.HasPrefix(d, id+".") {
						other = true
						break
					}
				}
				if other {
					path = append(path, id+"~other")
				}
				add(path, 1/float64(len(roots))/float64(len(ids)))
			}
		}
	}
	slices := []fingerprintSlice{}
	var visit func(string)
	visit = func(id string) {
		if terminals[id] {
			color := id
			if strings.HasSuffix(id, "~other") {
				color = strings.TrimSuffix(id, "~other")
			} else if id == "all" || id == "uncategorized" || id == "unlabeled" {
				color = "?"
			}
			slices = append(slices, fingerprintSlice{color, weights[id] / float64(len(messages))})
		}
		ids := children[id]
		sort.SliceStable(ids, func(i, j int) bool {
			a, b := ids[i], ids[j]
			for _, key := range []string{"~other", "uncategorized", "unlabeled"} {
				av, bv := false, false
				if key == "~other" {
					av, bv = strings.HasSuffix(a, key), strings.HasSuffix(b, key)
				} else {
					av, bv = a == key, b == key
				}
				if av != bv {
					if key == "unlabeled" {
						return av
					}
					return bv
				}
			}
			return p.compareStates(a, b) < 0
		})
		for _, child := range ids {
			visit(child)
		}
	}
	if len(messages) > 0 {
		visit("all")
	}
	return slices
}
func (p *fingerprintPrepared) mergeEvents(ctx context.Context, events []fingerprintEvent) ([]fingerprintEvent, error) {
	counts := map[string]int{}
	messages := map[string][]int{}
	messageOrder := []string{}
	for i, e := range events {
		if e.Members == nil {
			counts[e.Kind]++
		} else {
			for _, m := range e.Members {
				counts[m.Kind]++
			}
		}
		if _, ok := messages[e.Message]; !ok {
			messageOrder = append(messageOrder, e.Message)
		}
		messages[e.Message] = append(messages[e.Message], i)
	}
	replacement := map[int]fingerprintEvent{}
	skip := map[int]bool{}
	for _, message := range messageOrder {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		list := messages[message]
		parent := make([]int, len(list))
		for i := range parent {
			parent[i] = i
		}
		var find func(int) int
		find = func(i int) int {
			if parent[i] != i {
				parent[i] = find(parent[i])
			}
			return parent[i]
		}
		for i, ei := range list {
			for j := i + 1; j < len(list); j++ {
				if j&63 == 0 {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
				}
				a, b := events[ei].Span, events[list[j]].Span
				if len(a) != 2 || len(b) != 2 {
					continue
				}
				lo, hi := math.Max(a[0], b[0]), math.Min(a[1], b[1])
				if hi <= lo {
					continue
				}
				if a[0] <= b[0] && a[1] >= b[1] || b[0] <= a[0] && b[1] >= a[1] || (hi-lo)/(math.Max(a[1], b[1])-math.Min(a[0], b[0])) >= .8 {
					a, b := find(i), find(j)
					parent[max(a, b)] = min(a, b)
				}
			}
		}
		groups := map[int][]int{}
		order := []int{}
		for i := range list {
			root := find(i)
			if groups[root] == nil {
				order = append(order, root)
			}
			groups[root] = append(groups[root], list[i])
		}
		for _, root := range order {
			indices := groups[root]
			if len(indices) < 2 {
				continue
			}
			members := append([]int(nil), indices...)
			sort.SliceStable(members, func(i, j int) bool {
				a, b := events[members[i]], events[members[j]]
				if d := counts[a.Kind] - counts[b.Kind]; d != 0 {
					return d < 0
				}
				if d := fingerprintGroup(a.Group) - fingerprintGroup(b.Group); d != 0 {
					return d < 0
				}
				if d := p.compare.CompareString(a.Kind, b.Kind); d != 0 {
					return d < 0
				}
				if a.Probability != b.Probability {
					return a.Probability > b.Probability
				}
				return p.compare.CompareString(a.ID, b.ID) < 0
			})
			mark := events[members[0]]
			if mark.Target == nil || mark.Target.Message == "" {
				for _, i := range members {
					if t := events[i].Target; t != nil && t.Message != "" {
						mark.Target = t
						break
					}
				}
			}
			replacement[indices[0]] = mark
			for _, i := range indices[1:] {
				skip[i] = true
			}
		}
	}
	out := make([]fingerprintEvent, 0, len(events))
	for i, e := range events {
		if skip[i] {
			continue
		}
		if r, ok := replacement[i]; ok {
			e = r
		}
		out = append(out, e)
	}
	return out, nil
}
func prepareFingerprint(ctx context.Context, input *fingerprintInput) (*fingerprintPrepared, error) {
	p := &fingerprintPrepared{compare: collate.New(language.Und)}
	c := &input.Conversation
	main := c.ID
	if c.MainSession != nil {
		main = *c.MainSession
	}
	sessionOf := func(m *fingerprintMessage) string {
		if m.Session != nil {
			return *m.Session
		}
		return main
	}
	messages := map[string]*fingerprintMessage{}
	branches := map[string]bool{}
	stream := make([]fingerprintObservation, 0, max(len(c.Messages), len(c.Activity)))
	hasClock := false
	for i := range c.Messages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		m := &c.Messages[i]
		m.states = p.states(m)
		messages[m.ID] = m
		branches[sessionOf(m)] = true
		at := fingerprintTime(m.At)
		hasClock = hasClock || !math.IsNaN(at)
		stream = append(stream, fingerprintObservation{message: m, at: at, order: i})
	}
	positioned := []*fingerprintTool{}
	for i := range c.Activity {
		t := &c.Activity[i]
		branches[t.Session] = true
		at := fingerprintTime(t.Start)
		if t.Message != "" && messages[t.Message] != nil || len(c.Messages) == 0 || hasClock && !math.IsNaN(at) {
			positioned = append(positioned, t)
			if len(c.Messages) == 0 {
				stream = append(stream, fingerprintObservation{tool: t, at: at, order: len(positioned) - 1})
			}
		}
	}
	sort.SliceStable(stream, func(i, j int) bool {
		a, b := stream[i], stream[j]
		if math.IsNaN(a.at) {
			if !math.IsNaN(b.at) {
				return false
			}
		} else if math.IsNaN(b.at) {
			return true
		} else if a.at != b.at {
			return a.at < b.at
		}
		return a.order < b.order
	})
	type anchor struct{ at, x float64 }
	anchors := []anchor{}
	positions, toolPositions := map[string]float64{}, map[string]float64{}
	for i := range stream {
		o := &stream[i]
		o.x = (float64(i) + .5) / float64(len(stream))
		if o.message != nil {
			positions[o.message.ID] = o.x
		} else {
			toolPositions[o.tool.ID] = o.x
		}
		if !math.IsNaN(o.at) {
			anchors = append(anchors, anchor{o.at, o.x})
		}
	}
	if len(anchors) > 0 {
		first, last := anchors[0].at, anchors[len(anchors)-1].at
		for _, t := range positioned {
			at := fingerprintTime(t.Start)
			_, end := fingerprintToolTiming(t)
			if !math.IsNaN(at) {
				first = math.Min(first, at)
				last = math.Max(last, at)
				if !math.IsNaN(end) {
					last = math.Max(last, end)
				}
			}
		}
		if first < anchors[0].at {
			anchors = append([]anchor{{first, math.Max(0, anchors[0].x-.5/float64(len(stream)))}}, anchors...)
		}
		if last > anchors[len(anchors)-1].at {
			anchors = append(anchors, anchor{last, math.Min(1, anchors[len(anchors)-1].x+.5/float64(len(stream)))})
		}
	}
	project := func(at float64) float64 {
		if len(anchors) == 0 {
			return math.NaN()
		}
		lo := sort.Search(len(anchors), func(i int) bool { return anchors[i].at >= at })
		right, left := anchors[min(lo, len(anchors)-1)], anchors[max(0, lo-1)]
		share := 0.
		if right.at != left.at {
			share = fingerprintClamp((at - left.at) / (right.at - left.at))
		}
		return left.x + float64((right.x-left.x)*share)
	}
	for _, t := range positioned {
		if x, ok := positions[t.Message]; t.Message != "" && ok {
			toolPositions[t.ID] = x
			continue
		}
		if len(c.Messages) == 0 {
			continue
		}
		at := fingerprintTime(t.Start)
		if !math.IsNaN(at) {
			toolPositions[t.ID] = project(at)
		}
	}
	// conversationRibbon compresses >40 label/depth/parallelism runs before its
	// per-message overlaps are sampled onto the equal-spacing compact columns.
	sessions := c.Sessions
	if sessions == nil {
		sessions = []fingerprintSession{{ID: c.ID}}
	}
	intervals := [][2]float64{}
	for _, s := range sessions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lo, hi := math.Inf(1), math.Inf(-1)
		for i := range c.Messages {
			m := &c.Messages[i]
			at := fingerprintTime(m.At)
			if sessionOf(m) == s.ID && !math.IsNaN(at) {
				lo, hi = math.Min(lo, at), math.Max(hi, at)
			}
		}
		for i := range c.Activity {
			t := &c.Activity[i]
			if t.Session != s.ID {
				continue
			}
			for _, v := range []string{t.Start, t.End} {
				at := fingerprintTime(v)
				if !math.IsNaN(at) {
					lo, hi = math.Min(lo, at), math.Max(hi, at)
				}
			}
		}
		if !math.IsInf(lo, 1) {
			intervals = append(intervals, [2]float64{lo, hi})
		}
	}
	type run struct {
		ids             string
		depth, parallel int
		messages        []*fingerprintMessage
	}
	runs := []run{}
	depths := map[string]int{}
	for _, s := range sessions {
		depths[s.ID] = s.Depth
	}
	for _, o := range stream {
		if o.message == nil {
			continue
		}
		m := o.message
		parallel := 0
		for _, interval := range intervals {
			if interval[0] <= o.at && interval[1] >= o.at {
				parallel++
			}
		}
		parallel = max(0, parallel-1)
		ids := strings.Join(m.states, "|")
		if len(m.states) == 0 {
			ids = "?"
			if m.Chunks != 0 {
				ids = "no-match"
			}
		}
		depth := depths[sessionOf(m)]
		if len(runs) > 0 && runs[len(runs)-1].ids == ids && runs[len(runs)-1].parallel == parallel && runs[len(runs)-1].depth == depth {
			r := &runs[len(runs)-1]
			r.messages = append(r.messages, m)
		} else {
			runs = append(runs, run{ids, depth, parallel, []*fingerprintMessage{m}})
		}
	}
	if len(runs) <= 40 {
		for _, r := range runs {
			for _, m := range r.messages {
				m.parallelism = r.parallel
			}
		}
	} else {
		for i := 0; i < 40; i++ {
			chunk := runs[i*len(runs)/40 : (i+1)*len(runs)/40]
			peak := 0
			for _, r := range chunk {
				peak = max(peak, r.parallel)
			}
			for _, r := range chunk {
				for _, m := range r.messages {
					m.parallelism = peak
				}
			}
		}
	}
	taxonomy := []string{}
	for _, t := range input.Taxonomy {
		taxonomy = append(taxonomy, t.ID)
	}
	count := min(40, len(stream))
	for i := 0; i < count; i++ {
		band := fingerprintBand{x: float64(i) / float64(count), width: 1 / float64(count)}
		bandMessages := []*fingerprintMessage{}
		for _, o := range stream[i*len(stream)/count : (i+1)*len(stream)/count] {
			if o.message != nil {
				bandMessages = append(bandMessages, o.message)
				band.parallelism = max(band.parallelism, o.message.parallelism)
			}
		}
		band.slices = p.ribbonSlices(bandMessages, taxonomy)
		p.bands = append(p.bands, band)
	}
	marks, err := p.mergeEvents(ctx, input.Events)
	if err != nil {
		return nil, err
	}
	for i := range marks {
		e := &marks[i]
		x, ok := positions[e.Message]
		if !ok {
			continue
		}
		candidate := fingerprintCandidate{event: e, x: x, targetX: math.NaN(), phase: fingerprintPhase(x), session: sessionOf(messages[e.Message])}
		if target := e.Target; target != nil && target.Message != "" {
			if tx, ok := positions[target.Message]; ok && tx < x {
				candidate.target = target.Message
				candidate.targetX = tx
				candidate.strength = 2
				if target.Method == "overlap" || target.Method == "quote" && !target.Strong {
					candidate.strength = 1
				}
			}
		}
		p.candidates = append(p.candidates, candidate)
	}
	for _, t := range positioned {
		duration, end := fingerprintToolTiming(t)
		x := toolPositions[t.ID]
		endX := math.NaN()
		if !math.IsNaN(end) {
			endX = project(end)
			if !math.IsNaN(endX) {
				endX = math.Max(x, endX)
			}
		}
		status := strings.ToLower(t.Status)
		failure := false
		for _, word := range []string{"error", "fail", "block", "interrupt"} {
			failure = failure || strings.Contains(status, word)
		}
		p.tools = append(p.tools, fingerprintToolCandidate{tool: t, x: x, endX: endX, duration: duration, phase: fingerprintPhase(x), failure: failure})
	}
	p.messages, p.events, p.toolCount, p.branches = len(c.Messages), len(input.Events), len(c.Activity), len(branches)
	return p, nil
}
