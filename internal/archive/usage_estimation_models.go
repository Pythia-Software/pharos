package archive

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// These models predict a complete, tool-bearing response, never one inferred
// model request. A duration model is converted with the separately resolved
// coding model/effort rate; a token model uses either its exact setting or an
// explicitly published provider-wide prior.
type estimationResponseModel struct {
	ID                       string             `json:"id"`
	Evidence                 string             `json:"evidence"`
	Model                    string             `json:"model"`
	Effort                   string             `json:"effort"`
	Speed                    string             `json:"speed"`
	Unit                     string             `json:"unit"`
	Intercept                float64            `json:"intercept"`
	Coefficients             map[string]float64 `json:"coefficients"`
	DonorResponses           int                `json:"donor_responses"`
	DonorFamilies            int                `json:"donor_families"`
	MinimumTools             int                `json:"minimum_tools"`
	MaximumTools             int                `json:"maximum_tools"`
	MaximumVisibleCharacters int                `json:"maximum_visible_characters"`
	MinimumWindowSeconds     float64            `json:"minimum_window_seconds"`
	MaximumWindowSeconds     float64            `json:"maximum_window_seconds"`
	LowerMultiplier          float64            `json:"lower_multiplier"`
	UpperMultiplier          float64            `json:"upper_multiplier"`
	RequiresSettings         bool               `json:"requires_reported_settings"`
	Basis                    string             `json:"basis"`
	Notes                    string             `json:"notes"`
}

type estimationWindowPolicy struct {
	MaximumClockSkewSeconds float64 `json:"maximum_clock_skew_seconds"`
	MaximumSeconds          float64 `json:"maximum_seconds"`
	GapOverheadSeconds      float64 `json:"gap_overhead_seconds"`
}

func isBodyEstimationEvidence(evidence string) bool {
	return evidence == "body" || evidence == "body_prior"
}

var estimationResponseFeatures = map[string]bool{
	"visible_kchars": true, "argument_kbytes": true, "tools": true,
	"tool_command": true, "tool_read": true, "tool_edit": true,
	"tool_search": true, "tool_web": true, "tool_agent": true,
	"tool_plan": true, "tool_user": true, "tool_meta": true,
	"tool_mcp": true, "tool_other": true,
	"elapsed_minutes": true, "residual_minutes": true,
	"first_call_minutes": true, "last_call_minutes": true,
	"first_call_fraction": true, "last_call_fraction": true,
	"gap_excess_minutes": true,
}

func validEstimationNumber(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0
}

type estimationClockSpan struct{ start, end time.Time }

// Keep absolute clocks as time.Time. Converting epoch timestamps to floating
// seconds first loses enough precision to misclassify a native 100-ms timer.
// Native observation clocks remain evidence outside the calibration domain.
// Rate fitting retains its 100 ms–600 s eligibility gate independently.
func estimationThinkingClock(block usageEstimationBlock) bool {
	start, a := parseTime(block.start)
	end, z := parseTime(block.stop)
	return a && z && end.After(start)
}

func connectedEstimationThinkingIntervals(blocks []usageEstimationBlock) []estimationClockSpan {
	intervals := []estimationClockSpan{}
	for _, block := range blocks {
		if block.kind != "thinking" {
			continue
		}
		start, a := parseTime(block.start)
		end, z := parseTime(block.stop)
		if a && z && end.After(start) {
			intervals = append(intervals, estimationClockSpan{start, end})
		}
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].start.Before(intervals[j].start) })
	merged := []estimationClockSpan{}
	for _, interval := range intervals {
		if len(merged) == 0 || interval.start.After(merged[len(merged)-1].end) {
			merged = append(merged, interval)
		} else if interval.end.After(merged[len(merged)-1].end) {
			merged[len(merged)-1].end = interval.end
		}
	}
	return merged
}

func (d estimationManifest) validateResponseModels() error {
	p := d.Window
	if !validEstimationNumber(p.MaximumClockSkewSeconds) || p.MaximumClockSkewSeconds > 1 || p.MaximumSeconds < 1 || p.MaximumSeconds > 3600 || !validEstimationNumber(p.GapOverheadSeconds) || p.GapOverheadSeconds > 10 {
		return fmt.Errorf("invalid estimation window policy")
	}
	seen, keys := map[string]bool{}, map[string]bool{}
	for _, m := range d.ResponseModels {
		key := jsonText([]string{m.Evidence, m.Model, m.Effort, m.Speed})
		if m.ID == "" || seen[m.ID] || keys[key] || m.Model == "" || m.Effort == "" || (m.Speed != "*" && m.Speed != "standard" && m.Speed != "fast") || m.Notes == "" || m.DonorResponses < 20 || m.DonorFamilies < 5 || m.DonorFamilies > m.DonorResponses {
			return fmt.Errorf("invalid or duplicate estimation response model %s", m.ID)
		}
		if m.Effort != "*" && normalizeEffort(m.Effort) != m.Effort {
			return fmt.Errorf("invalid effort in estimation response model %s", m.ID)
		}
		if m.Unit != "tokens" && m.Unit != "seconds" || m.Basis != "coding-study" && m.Basis != "chat-duration-study" || !validEstimationNumber(m.Intercept) || m.Intercept > 1e9 {
			return fmt.Errorf("invalid units or fit in estimation response model %s", m.ID)
		}
		if isBodyEstimationEvidence(m.Evidence) && (m.Unit != "tokens" || m.Basis != "coding-study" || m.Evidence == "body" && !m.RequiresSettings) || !isBodyEstimationEvidence(m.Evidence) && (m.Unit != "seconds" || m.Basis != "chat-duration-study") {
			return fmt.Errorf("incompatible evidence and target in estimation response model %s", m.ID)
		}
		if !isBodyEstimationEvidence(m.Evidence) && m.Evidence != "window" && m.Evidence != "observed_window" && m.Evidence != "observed_gaps" || m.MinimumTools < 1 || m.MaximumTools < m.MinimumTools || m.MaximumTools > 1000 || m.MaximumVisibleCharacters < 1 || m.MaximumVisibleCharacters > 10e6 {
			return fmt.Errorf("invalid domain in estimation response model %s", m.ID)
		}
		if m.RequiresSettings && (m.Model == "*" || m.Effort == "*" || m.Speed == "*") {
			return fmt.Errorf("exact settings required in estimation response model %s", m.ID)
		}
		if !validEstimationNumber(m.MinimumWindowSeconds) || !validEstimationNumber(m.MaximumWindowSeconds) || isBodyEstimationEvidence(m.Evidence) && (m.MinimumWindowSeconds != 0 || m.MaximumWindowSeconds != 0) || !isBodyEstimationEvidence(m.Evidence) && (m.MinimumWindowSeconds < .1 || m.MaximumWindowSeconds < m.MinimumWindowSeconds || m.MaximumWindowSeconds > p.MaximumSeconds) {
			return fmt.Errorf("invalid clock domain in estimation response model %s", m.ID)
		}
		if !validEstimationNumber(m.LowerMultiplier) || !validEstimationNumber(m.UpperMultiplier) || m.LowerMultiplier > 1 || m.UpperMultiplier < 1 || m.UpperMultiplier > 100 {
			return fmt.Errorf("invalid sensitivity in estimation response model %s", m.ID)
		}
		if len(m.Coefficients) == 0 || len(m.Coefficients) > len(estimationResponseFeatures) {
			return fmt.Errorf("missing coefficients in estimation response model %s", m.ID)
		}
		if m.Evidence == "observed_gaps" && m.Coefficients["gap_excess_minutes"] <= 0 {
			return fmt.Errorf("observed gaps model %s requires gap evidence", m.ID)
		}
		for name, coefficient := range m.Coefficients {
			if !estimationResponseFeatures[name] || !validEstimationNumber(coefficient) || coefficient > 1e9 || isBodyEstimationEvidence(m.Evidence) && (strings.Contains(name, "minutes") || strings.Contains(name, "fraction")) || m.Evidence != "observed_window" && m.Evidence != "observed_gaps" && name == "residual_minutes" || m.Evidence != "observed_gaps" && name == "gap_excess_minutes" {
				return fmt.Errorf("invalid feature %s in estimation response model %s", name, m.ID)
			}
		}
		seen[m.ID], keys[key] = true, true
	}
	for name, category := range d.ToolCategories {
		if name == "" || !estimationResponseFeatures["tool_"+category] {
			return fmt.Errorf("invalid estimation tool category %s", name)
		}
	}
	return nil
}

type usageWindowEvidence struct {
	Source                    string   `json:"source"`
	Seconds                   float64  `json:"seconds"`
	ToolStarts                int      `json:"tool_starts"`
	ObservedToolSpans         int      `json:"observed_tool_spans"`
	ToolCoveredSeconds        *float64 `json:"tool_covered_seconds,omitempty"`
	ResidualSeconds           *float64 `json:"residual_seconds,omitempty"`
	NonthinkingCoveredSeconds *float64 `json:"nonthinking_covered_seconds,omitempty"`
	UnexplainedSeconds        *float64 `json:"unexplained_seconds,omitempty"`
	GapAdjustedSeconds        *float64 `json:"gap_adjusted_seconds,omitempty"`
	GapCount                  int      `json:"gap_count,omitempty"`
}

type usageResponseModelResult struct {
	Model  estimationResponseModel
	Value  estimationRange
	Window *usageWindowEvidence
}

func (d estimationManifest) estimationToolCategory(name string) string {
	if category := d.ToolCategories[name]; category != "" {
		return category
	}
	category, _ := toolCategory(name, "tool_call")
	if !estimationResponseFeatures["tool_"+category] {
		return "other"
	}
	return category
}

// The caller has already sorted blocks by retained source order. Only scalar
// values survive parsing. In particular, tool payloads never enter a model.
func estimationBodyFeatures(r *usageEstimationResponse, manifest estimationManifest) (map[string]float64, bool) {
	f := map[string]float64{"visible_kchars": 0, "argument_kbytes": 0, "tools": 0}
	for name := range estimationResponseFeatures {
		if strings.HasPrefix(name, "tool_") {
			f[name] = 0
		}
	}
	if r.nontext || len(r.blocks) == 0 || r.blocks[len(r.blocks)-1].kind != "text" || r.blocks[len(r.blocks)-1].characters == 0 {
		return f, false
	}
	argumentsKnown := true
	seen := map[string]bool{}
	results := map[string]bool{}
	for _, block := range r.blocks {
		if !block.orderKnown {
			return f, false
		}
		switch block.kind {
		case "text":
			f["visible_kchars"] += float64(block.characters) / 1000
		case "tool_use":
			if block.call == "" || seen[block.call] {
				return f, false // ambiguous identity cannot establish a complete tool body
			}
			seen[block.call] = true
			argumentsKnown = argumentsKnown && block.inputKnown
			f["argument_kbytes"] += float64(block.inputBytes) / 1000
			f["tools"]++
			f["tool_"+manifest.estimationToolCategory(block.name)]++
		case "tool_result":
			if !seen[block.call] || results[block.call] {
				return f, false // stray, reordered or duplicate results are incomplete evidence
			}
			results[block.call] = true
		}
	}
	if !argumentsKnown {
		delete(f, "argument_kbytes") // missing payloads cannot become measured zero bytes
	}
	return f, len(results) == len(seen)
}

func unionEstimationIntervals(intervals [][2]float64) float64 {
	sort.Slice(intervals, func(i, j int) bool { return intervals[i][0] < intervals[j][0] })
	seconds, end := 0.0, 0.0
	for i, v := range intervals {
		if i == 0 || v[0] >= end {
			seconds += v[1] - v[0]
		} else if v[1] > end {
			seconds += v[1] - end
		}
		end = max(end, v[1])
	}
	return seconds
}

func (b *usageEstimationBuilder) estimationWindow(r *usageEstimationResponse, manifest estimationManifest, tools []usageToolEstimate, features map[string]float64) *usageWindowEvidence {
	if len(r.blocks) == 0 || r.blocks[len(r.blocks)-1].kind != "text" || r.blocks[len(r.blocks)-1].characters == 0 {
		return nil // a thinking/tool stop cannot establish the final-answer endpoint
	}
	parent := b.responses[r.prompt]
	if parent == nil || parent.sender != "human" && parent.sender != "user" {
		return nil
	}
	start, ok := parseTime(parent.nativeCreated)
	end, complete := parseTime(r.blocks[len(r.blocks)-1].stop)
	if !ok || !complete {
		return nil
	}
	senderStart := start
	// Small observed differences between native clock domains are explicit;
	// larger inversions cannot be repaired with an envelope/updated timestamp.
	for _, block := range r.blocks {
		if at, valid := parseTime(block.start); valid && at.Before(start) {
			if senderStart.Sub(at).Seconds() > manifest.Window.MaximumClockSkewSeconds {
				return nil
			}
			start = at
		}
	}
	seconds := end.Sub(start).Seconds()
	if seconds < .1 || seconds > manifest.Window.MaximumSeconds {
		return nil
	}
	statuses := map[string]usageToolEstimate{}
	for _, t := range tools {
		if t.Status == "unavailable" {
			return nil // a launch/interaction acknowledgment is not a blocking span
		}
		statuses[t.CallID] = t
	}
	intervals := [][2]float64{}
	first, last, starts := seconds, 0.0, 0
	observed := 0
	for _, block := range r.blocks {
		if block.kind != "tool_use" {
			continue
		}
		at, valid := parseTime(block.start)
		if !valid || at.Before(start) || at.After(end) {
			continue
		}
		starts++
		offset := at.Sub(start).Seconds()
		first, last = min(first, offset), max(last, offset)
		t := statuses[block.call]
		if t.Status == "observed" && t.Seconds != nil {
			if argumentStop, valid := parseTime(block.stop); valid && (argumentStop.Before(at) || argumentStop.Sub(at).Seconds() > t.Seconds.Expected+.001) {
				return nil // contradictory native argument/result clock domains
			}
			stop := offset + t.Seconds.Expected
			if stop > seconds+.001 {
				return nil
			}
			intervals = append(intervals, [2]float64{offset, min(stop, seconds)})
			observed++
		}
	}
	features["elapsed_minutes"] = seconds / 60
	window := &usageWindowEvidence{Source: "native_sender_to_final_text_stop", Seconds: seconds, ToolStarts: starts, ObservedToolSpans: observed}
	if starts > 0 && starts == int(features["tools"]) {
		features["first_call_minutes"], features["last_call_minutes"] = first/60, last/60
		features["first_call_fraction"], features["last_call_fraction"] = first/seconds, last/seconds
		if observed == starts {
			covered := unionEstimationIntervals(intervals)
			residual := max(0, seconds-covered)
			window.ToolCoveredSeconds, window.ResidualSeconds = &covered, &residual
			features["residual_minutes"] = residual / 60
			estimationGapFeatures(r.blocks, start, end, intervals, manifest.Window.GapOverheadSeconds, features, window)
		}
	}
	return window
}

func predictEstimationResponseModel(model estimationResponseModel, features map[string]float64) (estimationRange, bool) {
	if model.Evidence == "observed_gaps" {
		if gap, present := features["gap_excess_minutes"]; !present || !validEstimationNumber(gap) {
			return estimationRange{}, false
		}
	}
	if features["tools"] < float64(model.MinimumTools) || features["tools"] > float64(model.MaximumTools) || features["visible_kchars"]*1000 > float64(model.MaximumVisibleCharacters) {
		return estimationRange{}, false
	}
	if !isBodyEstimationEvidence(model.Evidence) {
		seconds, present := features["elapsed_minutes"]
		if !present || seconds*60 < model.MinimumWindowSeconds || seconds*60 > model.MaximumWindowSeconds {
			return estimationRange{}, false
		}
	}
	value := model.Intercept
	for name, coefficient := range model.Coefficients {
		v, present := features[name]
		if !present || !validEstimationNumber(v) {
			return estimationRange{}, false
		}
		value += v * coefficient
	}
	if !validEstimationNumber(value) || value <= 0 || model.Unit == "seconds" && value < .1 {
		return estimationRange{}, false
	}
	result := estimationRange{value, value * model.LowerMultiplier, value * model.UpperMultiplier}
	if model.Unit == "seconds" {
		window := features["elapsed_minutes"] * 60
		if value > window {
			return estimationRange{}, false
		}
		result.Upper = min(result.Upper, window)
	}
	return result, result.valid()
}

func (b *usageEstimationBuilder) estimateMissingResponse(r *usageEstimationResponse, manifest estimationManifest, tools []usageToolEstimate) (usageResponseModelResult, bool) {
	features, complete := estimationBodyFeatures(r, manifest)
	if parent := b.responses[r.prompt]; parent != nil && parent.nontext {
		complete = false
	}
	if !complete {
		return usageResponseModelResult{}, false
	}
	for _, tool := range tools {
		if tool.Basis == "missing_result" {
			return usageResponseModelResult{}, false
		}
	}
	window := b.estimationWindow(r, manifest, tools, features)
	model, effort := strings.ToLower(strings.TrimSpace(r.model)), normalizeEffort(r.effort)
	for _, evidence := range []string{"observed_gaps", "observed_window", "window", "body", "body_prior"} {
		if !isBodyEstimationEvidence(evidence) && window == nil {
			continue
		}
		for _, pair := range [][2]string{{model, effort}, {model, "*"}, {"*", effort}, {"*", "*"}} {
			for _, speed := range []string{r.speed, "*"} {
				for _, m := range manifest.ResponseModels {
					if m.Evidence != evidence || m.Model != pair[0] || m.Effort != pair[1] || m.Speed != speed || m.RequiresSettings && (model == "" || effort == "" || r.speedSource != "export" || m.Model != model || m.Effort != effort || m.Speed != r.speed) {
						continue
					}
					if value, valid := predictEstimationResponseModel(m, features); valid {
						return usageResponseModelResult{Model: m, Value: value, Window: window}, true
					}
				}
			}
		}
	}
	return usageResponseModelResult{}, false
}
