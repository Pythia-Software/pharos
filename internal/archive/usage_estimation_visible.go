package archive

import (
	"fmt"
	"sort"
	"strings"
)

type estimationVisibleDurationPolicy struct {
	SecondsPerCharacter         float64 `json:"seconds_per_character"`
	TextToThinkingDurationRatio float64 `json:"text_to_thinking_duration_ratio"`
	MinimumTextCharacters       int     `json:"minimum_text_characters"`
	MinimumTextSeconds          float64 `json:"minimum_text_seconds"`
	MaximumTextSeconds          float64 `json:"maximum_text_seconds"`
	MinimumTextRate             float64 `json:"minimum_text_characters_per_second"`
	MaximumTextRate             float64 `json:"maximum_text_characters_per_second"`
	MaximumThinkingCharacters   int     `json:"maximum_thinking_characters"`
	MaximumThinkingSeconds      float64 `json:"maximum_thinking_seconds"`
	DonorBlocks                 int     `json:"donor_blocks"`
	DonorFamilies               int     `json:"donor_families"`
	LowerMultiplier             float64 `json:"lower_multiplier"`
	UpperMultiplier             float64 `json:"upper_multiplier"`
	Notes                       string  `json:"notes"`
}

// This model describes a complete visible trace with no native thinking clock.
// Its per-block term composes without inventing a hidden model-request count.
type estimationVisibleThinkingModel struct {
	Model              string  `json:"model"`
	Effort             string  `json:"effort"`
	Speed              string  `json:"speed"`
	TokensPerCharacter float64 `json:"tokens_per_character"`
	TokensPerBlock     float64 `json:"tokens_per_block"`
	DonorRequests      int     `json:"donor_requests"`
	DonorFamilies      int     `json:"donor_families"`
	MaximumCharacters  int     `json:"maximum_characters"`
	MaximumBlocks      int     `json:"maximum_blocks"`
	LowerMultiplier    float64 `json:"lower_multiplier"`
	UpperMultiplier    float64 `json:"upper_multiplier"`
	Notes              string  `json:"notes"`
}

func (d estimationManifest) validateVisibleThinkingModels() error {
	if len(d.VisibleThinkingModels) > 0 || d.VisibleThinkingDuration != (estimationVisibleDurationPolicy{}) {
		p := d.VisibleThinkingDuration
		if !validEstimationNumber(p.SecondsPerCharacter) || p.SecondsPerCharacter <= 0 || !validEstimationNumber(p.TextToThinkingDurationRatio) || p.TextToThinkingDurationRatio <= 0 || p.MinimumTextCharacters < 1 || !validEstimationNumber(p.MinimumTextSeconds) || p.MinimumTextSeconds < .1 || !validEstimationNumber(p.MaximumTextSeconds) || p.MaximumTextSeconds < p.MinimumTextSeconds || p.MaximumTextSeconds > 600 || !validEstimationNumber(p.MinimumTextRate) || p.MinimumTextRate <= 0 || !validEstimationNumber(p.MaximumTextRate) || p.MaximumTextRate < p.MinimumTextRate || p.MaximumThinkingCharacters < 1 || p.MaximumThinkingCharacters > 1e6 || !validEstimationNumber(p.MaximumThinkingSeconds) || p.MaximumThinkingSeconds < .1 || p.MaximumThinkingSeconds > 600 || p.DonorBlocks < 20 || p.DonorFamilies < 5 || p.DonorFamilies > p.DonorBlocks || !validEstimationNumber(p.LowerMultiplier) || p.LowerMultiplier > 1 || !validEstimationNumber(p.UpperMultiplier) || p.UpperMultiplier < 1 || p.UpperMultiplier > 100 || p.Notes == "" {
			return fmt.Errorf("invalid visible thinking duration policy")
		}
	}
	seen := map[string]bool{}
	for _, m := range d.VisibleThinkingModels {
		key := jsonText([]string{m.Model, m.Effort, m.Speed})
		if m.Model == "" || m.Effort == "" || normalizeEffort(m.Effort) != m.Effort || (m.Speed != "*" && m.Speed != "standard" && m.Speed != "fast") || seen[key] || !validEstimationNumber(m.TokensPerCharacter) || m.TokensPerCharacter <= 0 || m.TokensPerCharacter > 10 || !validEstimationNumber(m.TokensPerBlock) || m.TokensPerBlock > 1e6 || m.DonorRequests < 20 || m.DonorFamilies < 5 || m.DonorFamilies > m.DonorRequests || m.MaximumCharacters < 1 || m.MaximumCharacters > 1e6 || m.MaximumBlocks < 1 || m.MaximumBlocks > 1000 || !validEstimationNumber(m.LowerMultiplier) || m.LowerMultiplier > 1 || !validEstimationNumber(m.UpperMultiplier) || m.UpperMultiplier < 1 || m.UpperMultiplier > 100 || m.Notes == "" {
			return fmt.Errorf("invalid visible thinking model %s", key)
		}
		seen[key] = true
	}
	return nil
}

func (d estimationManifest) estimateVisibleThinkingDuration(r *usageEstimationResponse, characters int) (estimationRange, string, bool) {
	p := d.VisibleThinkingDuration
	if characters < 1 || characters > p.MaximumThinkingCharacters {
		return estimationRange{}, "", false
	}
	rates := []float64{}
	for _, b := range r.blocks {
		if b.kind != "text" || b.characters < p.MinimumTextCharacters {
			continue
		}
		start, a := parseTime(b.start)
		end, z := parseTime(b.stop)
		if !a || !z {
			continue
		}
		seconds := end.Sub(start).Seconds()
		if seconds < p.MinimumTextSeconds || seconds > p.MaximumTextSeconds {
			continue
		}
		rate := float64(b.characters) / seconds
		if rate >= p.MinimumTextRate && rate <= p.MaximumTextRate {
			rates = append(rates, rate)
		}
	}
	seconds, model := float64(characters)*p.SecondsPerCharacter, "chat-visible-characters-v4"
	if len(rates) > 0 {
		sort.Float64s(rates)
		rate := rates[len(rates)/2]
		if len(rates)%2 == 0 {
			rate = (rate + rates[len(rates)/2-1]) / 2
		}
		seconds, model = float64(characters)/rate*p.TextToThinkingDurationRatio, "chat-visible-stream-v4"
	}
	value := estimationRange{seconds, seconds * p.LowerMultiplier, seconds * p.UpperMultiplier}
	value.Upper = min(value.Upper, p.MaximumThinkingSeconds)
	return value, model, value.valid() && seconds > 0 && seconds <= p.MaximumThinkingSeconds
}

func completeVisibleThinkingCharacters(r *usageEstimationResponse) (int, int, bool) {
	characters, blocks := 0, 0
	for _, block := range r.blocks {
		if block.kind != "thinking" {
			continue
		}
		if block.visibleThinkingCharacters == 0 {
			return 0, 0, false
		}
		characters += block.visibleThinkingCharacters
		blocks++
	}
	return characters, blocks, blocks > 0
}

func (d estimationManifest) estimateCompleteVisibleThinking(r *usageEstimationResponse, clocked, missing int) (estimationRange, estimationVisibleThinkingModel, bool) {
	if clocked > 0 || missing == 0 {
		return estimationRange{}, estimationVisibleThinkingModel{}, false
	}
	characters, blocks, complete := completeVisibleThinkingCharacters(r)
	if !complete {
		return estimationRange{}, estimationVisibleThinkingModel{}, false
	}
	model, effort := strings.ToLower(strings.TrimSpace(r.model)), normalizeEffort(r.effort)
	for _, pair := range [][2]string{{model, effort}, {model, "*"}, {"*", effort}, {"*", "*"}} {
		for _, speed := range []string{r.speed, "*"} {
			for _, m := range d.VisibleThinkingModels {
				if m.Model != pair[0] || m.Effort != pair[1] || m.Speed != speed || (speed != "*" && r.speedSource != "export") || characters > m.MaximumCharacters || blocks > m.MaximumBlocks {
					continue
				}
				expected := float64(characters)*m.TokensPerCharacter + float64(blocks)*m.TokensPerBlock
				value := estimationRange{expected, expected * m.LowerMultiplier, expected * m.UpperMultiplier}
				if value.valid() && expected > 0 {
					return value, m, true
				}
			}
		}
	}
	return estimationRange{}, estimationVisibleThinkingModel{}, false
}
