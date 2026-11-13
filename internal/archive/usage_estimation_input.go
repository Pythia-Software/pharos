package archive

import (
	"fmt"
	"sort"
	"time"
)

// Processed input is what the model re-read, not what was newly written. A
// response with tool calls spans several model requests, and each request
// reads the whole retained conversation again. The estimate therefore sums
// one context per inferred request and is published beside generated output,
// never folded into it.
type estimationInputPolicy struct {
	ContextRate     estimationRange `json:"context_tokens_per_character"`
	RequestOverhead estimationRange `json:"request_overhead_tokens"`
	ContextWindow   float64         `json:"lower_bound_context_window_tokens"`
	CacheTTLSeconds float64         `json:"cache_ttl_seconds"`
	Basis           string          `json:"basis"`
	DonorRequests   int             `json:"donor_requests"`
	DonorFamilies   int             `json:"donor_families"`
	Notes           string          `json:"notes"`
}

func (d estimationManifest) validateInput() error {
	p := d.Input
	if !p.ContextRate.valid() || p.ContextRate.Lower <= 0 || !p.RequestOverhead.valid() || p.ContextWindow <= 0 || p.CacheTTLSeconds <= 0 ||
		p.Basis != "coding-study" || p.DonorRequests <= 0 || p.DonorFamilies <= 0 || p.Notes == "" {
		return fmt.Errorf("invalid input estimation policy")
	}
	return nil
}

// The cache division is a pricing scenario under standard prompt caching. It
// has less support than the total and is labelled apart from it.
type usageInputEstimate struct {
	Requests        int             `json:"requests"`
	Tokens          estimationRange `json:"input_tokens"`
	CacheRead       float64         `json:"cache_read_input_tokens"`
	CacheCreation   float64         `json:"cache_creation_input_tokens"`
	Context         estimationRange `json:"context_tokens"`
	Basis           string          `json:"basis"`
	CacheBasis      string          `json:"cache_basis"`
	NontextExcluded bool            `json:"nontext_excluded,omitempty"`
}

func estimationResponseEnd(r *usageEstimationResponse) (time.Time, bool) {
	var latest time.Time
	for _, value := range append([]string{r.nativeCreated, r.usageAt}, estimationBlockClocks(r.blocks)...) {
		if at, ok := parseTime(value); ok && at.After(latest) {
			latest = at
		}
	}
	return latest, !latest.IsZero()
}

func estimationBlockClocks(blocks []usageEstimationBlock) []string {
	clocks := make([]string, 0, 2*len(blocks))
	for _, block := range blocks {
		clocks = append(clocks, block.start, block.stop)
	}
	return clocks
}

// estimateInputs walks the selected branch in source order. A request starts
// at the first assistant block of a response and after each run of tool
// results; consecutive calls without a result between them share one request.
// Reported responses still grow the context that later responses read.
// thinking holds each response's estimated thinking tokens, by native ID.
// Recorded coding input keeps earlier turns' thinking, so the expected context
// does too; the lower bound keeps none of it.
func (b *usageEstimationBuilder) estimateInputs(manifest estimationManifest, thinking map[string]estimationRange) map[string]*usageInputEstimate {
	policy, rate := manifest.Input, manifest.Input.ContextRate
	history := make([]*usageEstimationResponse, 0, len(b.responses))
	for _, r := range b.responses {
		history = append(history, r)
	}
	sort.Slice(history, func(i, j int) bool {
		if history[i].order != history[j].order {
			return history[i].order < history[j].order
		}
		return history[i].id < history[j].id
	})
	inputs := map[string]*usageInputEstimate{}
	context := estimationRange{}
	nontext := false
	previousPrompt, previousEnd, cached, dated := 0.0, time.Time{}, false, false
	for _, r := range history {
		blocks := append([]usageEstimationBlock(nil), r.blocks...)
		sort.SliceStable(blocks, func(i, j int) bool {
			if blocks[i].order != blocks[j].order {
				return blocks[i].order < blocks[j].order
			}
			return blocks[i].message < blocks[j].message
		})
		nontext = nontext || r.nontext
		context = context.plus(rate.times(float64(r.contextCharacters)))
		if !r.assistant {
			for _, block := range blocks {
				if block.kind == "text" {
					context = context.plus(rate.times(float64(block.characters)))
				}
			}
			continue
		}
		_, profile, _ := manifest.resolve(r.model, r.effort, r.speed)
		started, startKnown := parseTime(defaultString(nilIfEmpty(r.nativeCreated), r.usageAt))
		fresh := cached && dated && startKnown && started.Sub(previousEnd).Seconds() <= policy.CacheTTLSeconds
		estimate := &usageInputEstimate{Basis: "published_" + policy.Basis + ":visible_history", CacheBasis: "assumed_prompt_cache", NontextExcluded: nontext}
		// The response's thinking estimate is spread over its thinking blocks, or
		// placed at its start when no trace was retained.
		thought, traces := thinking[r.id], 0
		thought.Lower = 0
		for _, block := range blocks {
			if block.kind == "thinking" {
				traces++
			}
		}
		open, seen := false, map[string]bool{}
		for _, block := range blocks {
			switch block.kind {
			case "text", "thinking", "tool_use":
				if !open {
					open = true
					prompt := context.plus(policy.RequestOverhead)
					prompt.Lower = min(prompt.Lower, policy.ContextWindow)
					estimate.Requests++
					estimate.Tokens = estimate.Tokens.plus(prompt)
					read := 0.0
					if fresh {
						read = min(previousPrompt, prompt.Expected)
					}
					estimate.CacheRead += read
					estimate.CacheCreation += prompt.Expected - read
					previousPrompt, cached, fresh = prompt.Expected, true, true
					if traces == 0 && estimate.Requests == 1 {
						context = context.plus(thought)
					}
				}
				switch block.kind {
				case "text":
					context = context.plus(rate.times(float64(block.characters)))
				case "thinking":
					context = context.plus(thought.times(1 / float64(traces)))
				case "tool_use":
					if block.call != "" && seen[block.call] {
						continue
					}
					seen[block.call] = true
					context = context.plus(rate.times(float64(block.characters))).plus(profile.ToolOutputTokens)
				}
			case "tool_result":
				open = false
				context = context.plus(rate.times(float64(block.characters)))
			}
		}
		estimate.Context = context
		previousEnd, dated = estimationResponseEnd(r)
		if estimate.Requests > 0 {
			inputs[r.id] = estimate
		}
	}
	return inputs
}
