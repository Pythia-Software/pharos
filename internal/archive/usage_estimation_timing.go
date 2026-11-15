package archive

import (
	"fmt"
	"sort"
	"time"
)

func (d estimationManifest) validateToolTiming() error {
	p := d.ToolTiming
	if !p.ArgumentSecondsPerCharacter.valid() || p.ArgumentSecondsPerCharacter.Expected <= 0 || !p.ExecutionSeconds.valid() || p.ExecutionSeconds.Expected <= 0 || !validEstimationNumber(p.MaximumArgumentSeconds) || p.MaximumArgumentSeconds < 1 || p.MaximumArgumentSeconds > 1800 || p.MaximumArgumentCharacters < 1 || p.MaximumArgumentCharacters > 10e6 || p.DonorCalls < 20 || p.DonorFamilies < 5 || p.DonorFamilies > p.DonorCalls {
		return fmt.Errorf("invalid estimation tool timing policy")
	}
	seen := map[string]bool{}
	for _, tool := range p.SupportedTools {
		if tool == "" || seen[tool] {
			return fmt.Errorf("invalid tool timing support list")
		}
		seen[tool] = true
	}
	if len(seen) == 0 {
		return fmt.Errorf("missing tool timing support list")
	}
	supported := seen
	seen = map[string]bool{}
	for _, prior := range p.Tools {
		if prior.Runtime != "claude-ai-export" || !supported[prior.Tool] || seen[prior.Tool] || !prior.Seconds.valid() || prior.Basis == "" || prior.Notes == "" || prior.DonorCalls < 5 {
			return fmt.Errorf("invalid estimation execution prior %s", prior.Tool)
		}
		seen[prior.Tool] = true
	}
	return nil
}

// Native tool-use stop marks the end of argument writing, not completion of
// execution. Total call-to-result semantics stay intact; components are only
// observed when their own native endpoints exist and agree.
func estimateToolTiming(t *usageToolEstimate, call usageEstimationBlock, manifest estimationManifest, paired, unobservable bool) {
	t.ArgumentStatus, t.ExecutionStatus = "unavailable", "unavailable"
	policy := manifest.ToolTiming
	start, a := parseTime(call.start)
	stop, z := parseTime(call.stop)
	argumentClock := a && z && !stop.Before(start) && stop.Sub(start).Seconds() <= policy.MaximumArgumentSeconds
	if argumentClock {
		seconds := stop.Sub(start).Seconds()
		t.ArgumentSeconds, t.ArgumentStatus = &estimationRange{seconds, seconds, seconds}, "observed"
	}
	if !paired || unobservable {
		return
	}
	if t.Status == "observed" {
		if argumentClock && t.Seconds.Expected >= t.ArgumentSeconds.Expected {
			seconds := t.Seconds.Expected - t.ArgumentSeconds.Expected
			t.ExecutionSeconds, t.ExecutionStatus = &estimationRange{seconds, seconds, seconds}, "observed"
		} else if argumentClock {
			// Conflicting native endpoints cannot establish a decomposition.
			t.ArgumentSeconds, t.ArgumentStatus = nil, "unavailable"
		}
		return
	}
	supported := false
	for _, tool := range policy.SupportedTools {
		supported = supported || tool == call.name
	}
	if !supported {
		return
	}
	if !argumentClock {
		if !call.inputKnown || call.characters > policy.MaximumArgumentCharacters {
			return // legacy total-span prior remains explicit
		}
		seconds := policy.ArgumentSecondsPerCharacter.times(float64(call.characters))
		if seconds.Expected > policy.MaximumArgumentSeconds {
			return
		}
		t.ArgumentSeconds, t.ArgumentStatus = &seconds, "estimated"
	}
	execution := policy.ExecutionSeconds
	for _, prior := range policy.Tools {
		if prior.Tool == call.name {
			execution = prior.Seconds
			break
		}
	}
	t.ExecutionSeconds, t.ExecutionStatus = &execution, "estimated"
	total := t.ArgumentSeconds.plus(execution)
	t.Seconds, t.Status, t.Basis = &total, "estimated", "published_argument_stream_and_execution"
	if argumentClock {
		t.Basis = "native_argument_clock_with_published_execution"
	}
}

// Thinking intervals never enter these predictors. Only complete, observed
// text-generation and tool spans can uncover a gap. An inferred execution
// prior cannot turn an unobserved span into an observed one.
func estimationGapFeatures(blocks []usageEstimationBlock, start, end time.Time, toolSpans [][2]float64, overhead float64, features map[string]float64, evidence *usageWindowEvidence) {
	intervals := append([][2]float64{}, toolSpans...)
	for _, block := range blocks {
		if block.kind != "text" {
			continue
		}
		a, validStart := parseTime(block.start)
		z, validStop := parseTime(block.stop)
		if !validStart || !validStop || a.Before(start) || z.After(end) || z.Before(a) || block.characters > 0 && z.Equal(a) {
			return
		}
		intervals = append(intervals, [2]float64{a.Sub(start).Seconds(), z.Sub(start).Seconds()})
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i][0] < intervals[j][0] })
	gap, adjusted, cursor, count := 0.0, 0.0, 0.0, 0
	contribute := func(seconds float64) {
		if seconds > 0 {
			gap += seconds
			adjusted += max(0, seconds-overhead)
			count++
		}
	}
	for _, span := range intervals {
		contribute(span[0] - cursor)
		cursor = max(cursor, span[1])
	}
	contribute(end.Sub(start).Seconds() - cursor)
	covered := end.Sub(start).Seconds() - gap
	evidence.NonthinkingCoveredSeconds, evidence.UnexplainedSeconds, evidence.GapAdjustedSeconds, evidence.GapCount = &covered, &gap, &adjusted, count
	features["gap_excess_minutes"] = adjusted / 60
}
