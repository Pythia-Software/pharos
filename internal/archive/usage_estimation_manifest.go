package archive

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/gbdubs/pharos/estimation"
)

const usageEstimatorVersion = "claude-output-v4.2"

// Changes to either inference code policy or published coefficients invalidate
// the saved calibration and schedule the existing Tools maintenance stage on
// upgrade, including libraries with no new inputs to index.
func usageEstimationIdentity() string {
	return usageEstimatorVersion + ":" + hashBytes(estimation.Assumptions)
}

// Ranges describe sensitivity, not a calibrated confidence interval for chat.
// Expected is an arithmetic expectation; it is never a back-transformed median.
type estimationRange struct {
	Expected float64 `json:"expected"`
	Lower    float64 `json:"lower"`
	Upper    float64 `json:"upper"`
}

func (v estimationRange) valid() bool {
	return !math.IsNaN(v.Expected+v.Lower+v.Upper) && !math.IsInf(v.Expected+v.Lower+v.Upper, 0) && v.Lower >= 0 && v.Lower <= v.Expected && v.Expected <= v.Upper
}
func (v estimationRange) times(n float64) estimationRange {
	return estimationRange{v.Expected * n, v.Lower * n, v.Upper * n}
}
func (v estimationRange) plus(w estimationRange) estimationRange {
	return estimationRange{v.Expected + w.Expected, v.Lower + w.Lower, v.Upper + w.Upper}
}
func (v estimationRange) multiply(w estimationRange) estimationRange {
	return estimationRange{v.Expected * w.Expected, v.Lower * w.Lower, v.Upper * w.Upper}
}

type estimationProfile struct {
	ThinkingRate        estimationRange `json:"thinking_tokens_per_second"`
	TextRate            estimationRange `json:"text_tokens_per_character"`
	ArgumentRate        estimationRange `json:"argument_tokens_per_byte"`
	ToolOutputTokens    estimationRange `json:"tool_call_output_tokens"`
	SummarySeconds      estimationRange `json:"seconds_per_summary"`
	BlockSeconds        estimationRange `json:"seconds_per_thinking_block"`
	WithoutTraceTokens  estimationRange `json:"thinking_tokens_without_trace"`
	VisibleThinkingRate estimationRange `json:"visible_thinking_tokens_per_character"`
}
type estimationModel struct {
	Model         string `json:"model"`
	Effort        string `json:"effort"`
	Speed         string `json:"speed"`
	Profile       string `json:"profile"`
	Basis         string `json:"basis"`
	Notes         string `json:"notes"`
	DonorRequests int    `json:"donor_requests,omitempty"`
	DonorFamilies int    `json:"donor_families,omitempty"`
}
type estimationToolPrior struct {
	Runtime    string          `json:"runtime"`
	Tool       string          `json:"tool"`
	Seconds    estimationRange `json:"seconds"`
	Basis      string          `json:"basis"`
	DonorCalls int             `json:"donor_calls"`
	Notes      string          `json:"notes"`
}
type estimationLocalPolicy struct {
	MinimumRequests int `json:"minimum_requests"`
	MinimumFamilies int `json:"minimum_families"`
	PriorFamilies   int `json:"prior_families"`
	MaximumRequests int `json:"maximum_requests"`
	MaximumAgeDays  int `json:"maximum_age_days"`
}
type estimationToolTimingPolicy struct {
	ArgumentSecondsPerCharacter estimationRange       `json:"argument_seconds_per_character"`
	ExecutionSeconds            estimationRange       `json:"default_execution_seconds"`
	Tools                       []estimationToolPrior `json:"execution_tools"`
	MaximumArgumentSeconds      float64               `json:"maximum_argument_seconds"`
	MaximumArgumentCharacters   int                   `json:"maximum_argument_characters"`
	DonorCalls                  int                   `json:"donor_calls"`
	DonorFamilies               int                   `json:"donor_families"`
	SupportedTools              []string              `json:"supported_tools"`
}
type estimationManifest struct {
	Version                 int                              `json:"version"`
	Revision                string                           `json:"revision"`
	PublishedAt             string                           `json:"published_at"`
	Provider                string                           `json:"provider"`
	Methodology             string                           `json:"methodology"`
	Notes                   string                           `json:"notes"`
	Profiles                map[string]estimationProfile     `json:"profiles"`
	Models                  []estimationModel                `json:"models"`
	DefaultToolSeconds      estimationRange                  `json:"default_tool_seconds"`
	Tools                   []estimationToolPrior            `json:"tools"`
	UnobservableTools       []string                         `json:"completion_unobservable_tools"`
	Local                   estimationLocalPolicy            `json:"local_refinement"`
	ResponseModels          []estimationResponseModel        `json:"response_models"`
	ToolCategories          map[string]string                `json:"tool_categories"`
	Window                  estimationWindowPolicy           `json:"response_window"`
	ToolTiming              estimationToolTimingPolicy       `json:"tool_timing"`
	VisibleThinkingModels   []estimationVisibleThinkingModel `json:"visible_thinking_models"`
	VisibleThinkingDuration estimationVisibleDurationPolicy  `json:"visible_thinking_duration"`
}

func parseEstimationManifest(data []byte) (estimationManifest, error) {
	var doc estimationManifest
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&doc); err != nil {
		return doc, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return doc, fmt.Errorf("estimation manifest must contain one JSON document")
	}
	_, dateErr := time.Parse("2006-01-02", doc.PublishedAt)
	if doc.Version != 4 || strings.TrimSpace(doc.Revision) == "" || doc.Provider != "claude" || dateErr != nil || doc.Methodology == "" || doc.Notes == "" {
		return doc, fmt.Errorf("invalid estimation manifest header")
	}
	for name, p := range doc.Profiles {
		for _, v := range []estimationRange{p.ThinkingRate, p.TextRate, p.ArgumentRate, p.SummarySeconds, p.BlockSeconds, p.WithoutTraceTokens, p.VisibleThinkingRate} {
			if !v.valid() || v.Expected <= 0 {
				return doc, fmt.Errorf("invalid estimation range in profile %s", name)
			}
		}
		if !p.ToolOutputTokens.valid() {
			return doc, fmt.Errorf("invalid tool output range in profile %s", name)
		}
	}
	seen, fallback := map[string]bool{}, false
	efforts := map[string]bool{"*": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}
	for _, m := range doc.Models {
		key := m.Model + "/" + m.Effort + "/" + m.Speed
		if _, ok := doc.Profiles[m.Profile]; !ok || seen[key] || m.Model == "" || m.Notes == "" || (m.Speed != "*" && m.Speed != "standard" && m.Speed != "fast") || !efforts[m.Effort] || (m.Basis != "assumption" && m.Basis != "coding-study") {
			return doc, fmt.Errorf("invalid or duplicate estimation model %s", key)
		}
		if m.Basis == "coding-study" && (m.DonorRequests <= 0 || m.DonorFamilies <= 0) {
			return doc, fmt.Errorf("coding study %s requires support", key)
		}
		seen[key] = true
		fallback = fallback || key == "*/*/*"
	}
	if !fallback || !doc.DefaultToolSeconds.valid() || doc.DefaultToolSeconds.Expected <= 0 {
		return doc, fmt.Errorf("estimation fallback assumptions required")
	}
	seen = map[string]bool{}
	for _, p := range doc.Tools {
		key := p.Runtime + "/" + p.Tool
		if p.Runtime == "" || p.Tool == "" || p.Basis == "" || p.Notes == "" || p.DonorCalls < 0 || seen[key] || !p.Seconds.valid() {
			return doc, fmt.Errorf("invalid tool estimation prior %s", key)
		}
		seen[key] = true
	}
	if err := doc.validateToolTiming(); err != nil {
		return doc, err
	}
	if err := doc.validateVisibleThinkingModels(); err != nil {
		return doc, err
	}
	p := doc.Local
	if p.MinimumRequests < 1 || p.MinimumFamilies < 2 || p.PriorFamilies < 1 || p.MaximumRequests < p.MinimumRequests || p.MaximumRequests > 100000 || p.MaximumAgeDays < 1 || p.MaximumAgeDays > 365 {
		return doc, fmt.Errorf("invalid local estimation policy")
	}
	if err := doc.validateResponseModels(); err != nil {
		return doc, err
	}
	return doc, nil
}

var bundledEstimation = sync.OnceValues(func() (estimationManifest, error) {
	return parseEstimationManifest(estimation.Assumptions)
})

// Exact identity and effort precede explicit model/provider fallbacks. No
// heuristic prefix match silently assigns a new model another model's rate.
func (d estimationManifest) resolve(model, effort, speed string) (estimationModel, estimationProfile, string) {
	model = strings.ToLower(strings.TrimSpace(model))
	effort = normalizeEffort(effort)
	if speed == "" {
		speed = "standard"
	}
	for _, pair := range [][2]string{{model, effort}, {model, "*"}, {"*", effort}, {"*", "*"}} {
		if pair[0] == "" || pair[1] == "" {
			continue
		}
		for _, s := range []string{speed, "*"} {
			for _, row := range d.Models {
				if row.Model == pair[0] && row.Effort == pair[1] && row.Speed == s {
					level := "model_effort"
					if row.Model == "*" {
						level = "provider"
					} else if row.Effort == "*" {
						level = "model"
					}
					return row, d.Profiles[row.Profile], level
				}
			}
		}
	}
	return estimationModel{}, estimationProfile{}, "unavailable" // validated manifests have a fallback
}

func estimationManifestResponse() (map[string]any, error) {
	doc, err := bundledEstimation()
	return map[string]any{"manifest": doc, "digest": hashBytes(estimation.Assumptions), "estimator_version": usageEstimatorVersion}, err
}
