package archive

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type usageToolEstimate struct {
	MessageID        string           `json:"message_id"`
	CallID           string           `json:"call_id"`
	Tool             string           `json:"tool"`
	Status           string           `json:"status"`
	Basis            string           `json:"basis"`
	Seconds          *estimationRange `json:"seconds,omitempty"`
	ArgumentSeconds  *estimationRange `json:"argument_generation_seconds,omitempty"`
	ArgumentStatus   string           `json:"argument_generation_status"`
	ExecutionSeconds *estimationRange `json:"execution_seconds,omitempty"`
	ExecutionStatus  string           `json:"execution_status"`
}
type usageResponseEstimate struct {
	MessageID                 string               `json:"message_id"`
	NativeID                  string               `json:"native_id"`
	UsageAt                   string               `json:"usage_at,omitempty"`
	PromptID                  string               `json:"prompt_native_id,omitempty"`
	Model                     string               `json:"model,omitempty"`
	ModelSource               string               `json:"model_source,omitempty"`
	ModelObservedAt           string               `json:"model_observed_at,omitempty"`
	Effort                    string               `json:"effort,omitempty"`
	Speed                     string               `json:"speed"`
	SpeedSource               string               `json:"speed_source"`
	EvidenceScope             string               `json:"evidence_scope"`
	ResponseModel             string               `json:"response_model,omitempty"`
	ModelUnit                 string               `json:"response_model_unit,omitempty"`
	ModelFamilies             int                  `json:"response_model_families,omitempty"`
	ModelResponses            int                  `json:"response_model_responses,omitempty"`
	Window                    *usageWindowEvidence `json:"response_window,omitempty"`
	SecondsAvailable          bool                 `json:"thinking_seconds_available"`
	Profile                   string               `json:"profile"`
	Fallback                  string               `json:"fallback"`
	Basis                     string               `json:"basis"`
	Output                    estimationRange      `json:"output_tokens"`
	Input                     *usageInputEstimate  `json:"input,omitempty"`
	Thinking                  estimationRange      `json:"thinking_tokens"`
	ThinkingSeconds           estimationRange      `json:"thinking_seconds"`
	ThinkingDurationModel     string               `json:"thinking_duration_model,omitempty"`
	ClockedBlocks             int                  `json:"clocked_thinking_blocks"`
	MissingBlocks             int                  `json:"unclocked_thinking_blocks"`
	LocalFamilies             int                  `json:"local_families"`
	LocalRequests             int                  `json:"local_requests"`
	LocalWeight               float64              `json:"local_weight"`
	Tools                     []usageToolEstimate  `json:"tools"`
	VisibleThinkingCharacters int                  `json:"visible_thinking_characters,omitempty"`
	VisibleThinkingAdjustment bool                 `json:"visible_thinking_adjusted,omitempty"`
}
type conversationUsageEstimates struct {
	Version              string                  `json:"version"`
	ManifestRevision     string                  `json:"manifest_revision"`
	ManifestDigest       string                  `json:"manifest_digest"`
	CalibrationBuiltAt   string                  `json:"calibration_built_at,omitempty"`
	Scope                string                  `json:"scope"`
	RangeKind            string                  `json:"range_kind"`
	Output               *estimationRange        `json:"output_tokens,omitempty"`
	Input                *estimationRange        `json:"input_tokens,omitempty"`
	InputRequests        int                     `json:"input_requests,omitempty"`
	Responses            []usageResponseEstimate `json:"responses"`
	ReportedResponses    int                     `json:"reported_responses"`
	UnsupportedResponses int                     `json:"unsupported_responses"`
}

type usageEstimationBlock struct {
	message, kind, call, name string
	start, stop               string
	characters, summaries     int
	inputBytes                int
	inputKnown                bool
	visibleThinkingCharacters int
	order                     int64
	orderKnown                bool
	delegation                bool
}
type usageEstimationResponse struct {
	id, anchor, prompt, model, effort, speed, speedSource string
	order                                                 int64
	anchorOrder                                           int64
	anchorOrderKnown                                      bool
	assistant, reported, envelope                         bool
	modelAssumed                                          bool
	sender, nativeCreated, usageAt                        string
	nontext                                               bool
	contextCharacters                                     int // injected prompts and extracted attachment text
	blocks                                                []usageEstimationBlock
}
type usageEstimationBuilder struct {
	responses  map[string]*usageEstimationResponse
	selections []map[string]any
}

// A saved selection is an estimation assumption, not evidence of which model ran.
// Prefer the latest selection before the response, or the first after it when
// none exists. Conflicting organizations, timestamps, and unknown selections
// do not support an assumption. Truncated history cannot safely be ordered.
func assumedClaudeModel(rows []map[string]any, responseAt string) (string, string) {
	if len(rows) == 0 || len(rows) > 500 {
		return "", ""
	}
	var organization, model, observed string
	var chosen time.Time
	responseTime, dated := parseTime(responseAt)
	before := false
	conflict := false
	for _, row := range rows {
		org := firstString(row["organization_uuid"])
		if org == "" || organization != "" && org != organization {
			return "", ""
		}
		organization = org
		at, ok := parseTime(firstString(row["observed_at"]))
		if !ok {
			return "", ""
		}
		prior := dated && !at.After(responseTime)
		better := chosen.IsZero() || prior && !before || prior == before && (prior && at.After(chosen) || !prior && dated && at.Before(chosen) || !dated && at.After(chosen))
		value := strings.TrimSpace(firstString(row["model"]))
		if better {
			chosen, model, observed, before, conflict = at, value, firstString(row["observed_at"]), prior, false
		} else if at.Equal(chosen) && value != model {
			conflict = true
		}
	}
	if conflict || model == "" {
		return "", ""
	}
	return model, observed
}

func newUsageEstimationBuilder(harness string) *usageEstimationBuilder {
	if harness != "claude-ai-export" {
		return nil
	}
	return &usageEstimationBuilder{responses: map[string]*usageEstimationResponse{}}
}

func estimationExportMessageID(native string) string {
	for _, suffix := range []string{":block:", ":attachment:"} {
		if at := strings.LastIndex(native, suffix); at >= 0 {
			return native[:at]
		}
	}
	return strings.TrimSuffix(strings.TrimSuffix(native, ":usage"), ":files")
}

// observe runs before reader clipping. It keeps only scalar features, never a
// second copy of tool output or transcript text. Native block clocks are the
// only clocks used; old catalog timestamps cannot fill missing native clocks.
func (b *usageEstimationBuilder) observe(m map[string]any) {
	if b == nil || (m["selected"] != nil && integer(m["selected"]) == 0) {
		return
	}
	id := estimationExportMessageID(firstString(m["native_id"]))
	attachment := firstString(m["kind"]) == "attachment"
	var raw map[string]any
	if json.Unmarshal([]byte(firstString(m["raw_text"])), &raw) != nil {
		// Older retained exports can lack the message envelope but still have
		// an explicit adapter-owned usage record. Only that protocol row may
		// supply accounting; quoted tool output or user text never does.
		if attachment {
			raw = map[string]any{} // file-list attachments can be JSON arrays
		} else if firstString(m["kind"]) != "metadata" || firstString(m["role"]) != "system" || !strings.HasSuffix(firstString(m["native_id"]), ":usage") || json.Unmarshal([]byte(firstString(m["text"])), &raw) != nil || firstString(raw["type"]) != "token_usage" || firstString(raw["usage_message_id"]) != id || normalizeTokenCounts(mapValue(raw["usage"])) == nil {
			return
		}
	}
	if raw["claude_ai_conversation"] != nil {
		return
	}
	r := b.responses[id]
	if r == nil {
		r = &usageEstimationResponse{id: id, order: integer(m["source_order"]), speed: "standard", speedSource: "assumed_standard"}
		b.responses[id] = r
	}
	r.order = min(r.order, integer(m["source_order"]))
	r.nontext = r.nontext || attachment
	if attachment {
		r.contextCharacters += utf8.RuneCountInString(firstString(raw["extracted_content"]))
	}
	if firstString(raw["type"]) == "token_usage" && firstString(m["kind"]) == "metadata" && firstString(m["role"]) == "system" && strings.HasSuffix(firstString(m["native_id"]), ":usage") && normalizeTokenCounts(mapValue(raw["usage"])) != nil {
		r.reported = true
	}
	if env := mapValue(raw["claude_ai_message"]); env != nil {
		r.envelope = true
		r.assistant = firstString(env["sender"]) == "assistant"
		r.prompt = firstString(env["parent_message_uuid"])
		r.sender, r.nativeCreated = firstString(env["sender"]), firstString(env["created_at"])
		r.nontext = r.nontext || len(reasoningList(env["attachments"])) > 0 || len(reasoningList(env["files"])) > 0
		metadata := mapValue(env["metadata"])
		r.model = firstString(env["model"], metadata["model"])
		r.effort = updatedEffort("", metadata, env)
		if normalizeTokenCounts(mapValue(firstNonNil(env["usage"], metadata["usage"]))) != nil {
			r.reported = true
		}
		if speed := firstString(env["speed"], metadata["speed"]); speed == "fast" || speed == "standard" {
			r.speed, r.speedSource = speed, "export"
		}
	}
	if firstString(m["role"]) == "assistant" {
		r.assistant = true
		// Accounting placement is independent of the stable message anchor.
		// An unclocked tool can be first while a later reply block is dated.
		// These retained dates never supply missing block-generation clocks.
		if at, valid := parseTime(firstString(m["created_at"])); valid {
			previous, dated := parseTime(r.usageAt)
			if !dated || at.Before(previous) {
				r.usageAt = firstString(m["created_at"])
			}
		}
		order, known, message := integer(m["source_order"]), m["source_order"] != nil, firstString(m["id"])
		if r.anchor == "" || known && !r.anchorOrderKnown || known == r.anchorOrderKnown && (order < r.anchorOrder || order == r.anchorOrder && message < r.anchor) {
			r.anchor, r.anchorOrder, r.anchorOrderKnown = message, order, known
		}
	}
	if r.model == "" {
		r.model = firstString(m["model"])
	}
	if r.effort == "" {
		r.effort = firstString(m["effort"])
	}
	v := usageEstimationBlock{message: firstString(m["id"]), kind: firstString(raw["type"]), start: firstString(raw["start_timestamp"]), stop: firstString(raw["stop_timestamp"]), order: integer(m["source_order"]), orderKnown: m["source_order"] != nil, delegation: firstString(m["kind"]) == "delegation"}
	switch v.kind {
	case "":
		// The adapter preserves flattened replies from text-only exports as
		// {text, claude_ai_message?}, without inventing a content-block type.
		// Only a stored assistant message can use this representation.
		// A person's flattened message is context later requests read.
		if _, typed := raw["type"]; typed || firstString(m["kind"]) != "message" || (firstString(m["role"]) != "assistant" && firstString(m["role"]) != "user") {
			return
		}
		if _, text := raw["text"].(string); !text {
			return
		}
		v.kind, v.characters = "text", utf8.RuneCountInString(asString(m["text"]))
	case "text", "voice_note":
		v.kind, v.characters = "text", utf8.RuneCountInString(firstString(raw["text"]))
	case "injected_prompt_block":
		r.contextCharacters += utf8.RuneCountInString(firstString(raw["prompt"]))
		return
	case "thinking":
		v.summaries = len(reasoningList(raw["summaries"]))
		if hidden, explicit := raw["thinking_hidden"].(bool); explicit && !hidden {
			v.visibleThinkingCharacters = utf8.RuneCountInString(firstString(raw["thinking"]))
		}
	case "tool_use":
		v.call, v.name = firstString(raw["id"]), firstString(raw["name"])
		if input := raw["input"]; input != nil {
			v.inputKnown = true
			v.characters = utf8.RuneCountInString(jsonText(input))
			v.inputBytes = len(jsonText(input))
		}
	case "tool_result":
		v.call = firstString(raw["tool_use_id"])
		// Only the size is kept: result text is input to the requests after it.
		text, images := toolResultText(raw["content"])
		v.characters = utf8.RuneCountInString(text)
		r.nontext = r.nontext || images > 0
	default:
		r.nontext = r.nontext || v.kind == "image" || v.kind == "document" || v.kind == "file"
		return
	}
	r.blocks = append(r.blocks, v)
}

// Thinking intervals may overlap emitted text or other thinking. Their union
// avoids treating overlapping exported observation clocks as serial work.
func estimationThinkingSeconds(blocks []usageEstimationBlock, p estimationProfile) (estimationRange, int, int) {
	missing := estimationRange{}
	clocked, unclocked := 0, 0
	for _, b := range blocks {
		if b.kind != "thinking" {
			continue
		}
		start, a := parseTime(b.start)
		end, z := parseTime(b.stop)
		if a && z && end.After(start) {
			clocked++
		} else {
			unclocked++
			if b.summaries > 0 {
				missing = missing.plus(p.SummarySeconds.times(float64(b.summaries)))
			} else {
				missing = missing.plus(p.BlockSeconds)
			}
		}
	}
	seconds := 0.0
	for _, interval := range connectedEstimationThinkingIntervals(blocks) {
		seconds += interval.end.Sub(interval.start).Seconds()
	}
	return missing.plus(estimationRange{seconds, seconds, seconds}), clocked, unclocked
}

func estimateUsageTools(blocks []usageEstimationBlock, manifest estimationManifest) []usageToolEstimate {
	results := map[string]usageEstimationBlock{}
	for _, b := range blocks {
		if b.kind == "tool_result" && b.call != "" {
			if _, exists := results[b.call]; !exists {
				results[b.call] = b
			}
		}
	}
	tools, seen := []usageToolEstimate{}, map[string]bool{}
	for _, b := range blocks {
		if b.kind != "tool_use" || b.call == "" || seen[b.call] {
			continue
		}
		seen[b.call] = true
		t := usageToolEstimate{MessageID: b.message, CallID: b.call, Tool: b.name, Status: "unavailable", Basis: "missing_result"}
		result, paired := results[b.call]
		unobservable := b.delegation
		for _, name := range manifest.UnobservableTools {
			unobservable = unobservable || name == b.name
		}
		if unobservable {
			t.Basis = "completion_unobservable"
		} else if paired {
			start, a := parseTime(b.start)
			end, z := parseTime(defaultString(nilIfEmpty(result.start), result.stop))
			if a && z && end.After(start) {
				seconds := end.Sub(start).Seconds()
				v := estimationRange{seconds, seconds, seconds}
				t.Status, t.Basis, t.Seconds = "observed", "native_call_to_result", &v
			} else {
				v := manifest.DefaultToolSeconds
				t.Status, t.Basis = "estimated", "published_tool_fallback"
				for _, prior := range manifest.Tools {
					if prior.Runtime == "claude-ai-export" && prior.Tool == b.name {
						v, t.Basis = prior.Seconds, "published_"+prior.Basis
						break
					}
				}
				t.Seconds = &v
			}
		}
		estimateToolTiming(&t, b, manifest, paired, unobservable)
		tools = append(tools, t)
	}
	return tools
}

func (b *usageEstimationBuilder) estimate(manifest estimationManifest, calibration estimationCalibration, digest string) *conversationUsageEstimates {
	if b == nil {
		return nil
	}
	report := &conversationUsageEstimates{Version: usageEstimatorVersion, ManifestRevision: manifest.Revision, ManifestDigest: digest,
		CalibrationBuiltAt: calibration.BuiltAt, Scope: "selected_branch_unreported_usage", RangeKind: "sensitivity_not_confidence", Responses: []usageResponseEstimate{}}
	ordered := []*usageEstimationResponse{}
	for _, r := range b.responses {
		if r.assistant {
			ordered = append(ordered, r)
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].order != ordered[j].order {
			return ordered[i].order < ordered[j].order
		}
		return ordered[i].id < ordered[j].id
	})
	for _, r := range ordered {
		sort.SliceStable(r.blocks, func(i, j int) bool {
			if r.blocks[i].order != r.blocks[j].order {
				return r.blocks[i].order < r.blocks[j].order
			}
			return r.blocks[i].message < r.blocks[j].message
		})
		if len(r.blocks) == 0 || r.anchor == "" {
			report.UnsupportedResponses++
			continue
		}
		if r.reported {
			report.ReportedResponses++
			continue
		}
		usageAt := r.usageAt
		if _, valid := parseTime(r.nativeCreated); valid {
			usageAt = r.nativeCreated // the response's own date outranks its block dates
		}
		modelSource, modelObservedAt := "", ""
		if r.model == "" {
			if model, observed := assumedClaudeModel(b.selections, usageAt); model != "" {
				assumed := *r
				assumed.model, assumed.modelAssumed = model, true
				r, modelSource, modelObservedAt = &assumed, "observed_conversation_selection", observed
			}
		}
		row, p, fallback := manifest.resolve(r.model, r.effort, r.speed)
		seconds, clocked, missing := estimationThinkingSeconds(r.blocks, p)
		thought, local, weight := estimateThinkingContributions(r, seconds, manifest, calibration, p)
		basis := "published_" + row.Basis
		if weight > 0 {
			basis = "local_refinement_with_published_prior"
		}
		if clocked+missing == 0 {
			thought = p.WithoutTraceTokens
			basis = "published_" + row.Basis + ":missing_thinking_trace"
			local, weight = estimationRateGroup{}, 0
		}
		out := usageResponseEstimate{MessageID: r.anchor, NativeID: r.id, UsageAt: usageAt, PromptID: r.prompt, Model: r.model, ModelSource: modelSource, ModelObservedAt: modelObservedAt, Effort: r.effort,
			Speed: r.speed, SpeedSource: r.speedSource, Profile: row.Profile, Fallback: fallback, Basis: basis,
			Thinking: thought, ThinkingSeconds: seconds,
			ClockedBlocks: clocked, MissingBlocks: missing, LocalFamilies: local.Families, LocalRequests: local.Requests, LocalWeight: weight,
			Tools: estimateUsageTools(r.blocks, manifest), SecondsAvailable: clocked+missing > 0}
		if clocked+missing == 0 {
			if prediction, ok := b.estimateMissingResponse(r, manifest, out.Tools); ok {
				thought = prediction.Value
				if prediction.Model.Unit == "seconds" {
					out.ThinkingSeconds, out.SecondsAvailable = prediction.Value, true
					var rate estimationRange
					rate, local, weight = estimationThinkingRate(manifest, calibration, r.model, r.effort, r.speed, prediction.Value.Expected, p.ThinkingRate)
					thought = prediction.Value.multiply(rate)
				}
				thought.Upper = max(thought.Upper, p.WithoutTraceTokens.Upper)
				out.Thinking, out.Basis = thought, "published_"+prediction.Model.Basis+":missing_thinking_trace"
				if weight > 0 {
					out.Basis = "published_" + prediction.Model.Basis + "_with_local_coding_rate:missing_thinking_trace"
				}
				out.ResponseModel, out.ModelUnit = prediction.Model.ID, prediction.Model.Unit
				out.ModelFamilies, out.ModelResponses, out.Window = prediction.Model.DonorFamilies, prediction.Model.DonorResponses, prediction.Window
				out.LocalFamilies, out.LocalRequests, out.LocalWeight = local.Families, local.Requests, weight
			}
		}
		for _, block := range r.blocks {
			out.VisibleThinkingCharacters += block.visibleThinkingCharacters
		}
		if value, model, ok := manifest.estimateCompleteVisibleThinking(r, clocked, missing); ok {
			out.VisibleThinkingAdjustment = value.Expected != out.Thinking.Expected
			out.Thinking = value
			out.Thinking.Upper = max(value.Upper, p.WithoutTraceTokens.Upper)
			out.ResponseModel, out.ModelUnit = "complete-visible-unclocked-v4", "tokens"
			out.ModelResponses, out.ModelFamilies = model.DonorRequests, model.DonorFamilies
			out.Basis = "published_coding_study:complete_visible_unclocked_thinking"
			// A token model does not establish an elapsed duration or a local
			// duration-rate contribution, even if a block-duration prior existed.
			out.ThinkingSeconds, out.SecondsAvailable = estimationRange{}, false
			out.LocalFamilies, out.LocalRequests, out.LocalWeight = 0, 0, 0
		}
		if characters, _, complete := completeVisibleThinkingCharacters(r); complete && clocked == 0 && missing > 0 {
			if duration, durationModel, available := manifest.estimateVisibleThinkingDuration(r, characters); available {
				// Timing and token models have independent support domains. A
				// token-only model cannot manufacture duration, and a timing
				// estimate never silently changes the separately chosen tokens.
				window := b.estimationWindow(r, manifest, out.Tools, map[string]float64{"tools": float64(len(out.Tools))})
				if window == nil || duration.Expected <= window.Seconds {
					if window != nil {
						duration.Upper = min(duration.Upper, window.Seconds)
						out.Window = window
					}
					out.ThinkingSeconds, out.ThinkingDurationModel, out.SecondsAvailable = duration, durationModel, true
				} else {
					out.Window = window
					out.ThinkingSeconds, out.ThinkingDurationModel, out.SecondsAvailable = estimationRange{}, "", false
				}
			}
		}
		if out.VisibleThinkingCharacters > 0 {
			text := p.VisibleThinkingRate.times(float64(out.VisibleThinkingCharacters))
			// This is an estimated contribution floor, not a tokenizer bound.
			// Keep native time/provenance and avoid adding the same thinking twice.
			out.VisibleThinkingAdjustment = out.VisibleThinkingAdjustment || text.Expected > out.Thinking.Expected
			out.Thinking.Expected = max(out.Thinking.Expected, text.Expected)
			out.Thinking.Lower = max(out.Thinking.Lower, text.Lower)
			out.Thinking.Upper = max(out.Thinking.Upper, text.Upper)
			out.Basis += ":visible_thinking_text"
		}
		visible := estimationRange{}
		seenTools := map[string]bool{}
		for _, block := range r.blocks {
			if block.kind == "text" {
				visible = visible.plus(p.TextRate.times(float64(block.characters)))
			} else if block.kind == "tool_use" {
				if block.call != "" && seenTools[block.call] {
					continue
				}
				if block.call != "" {
					seenTools[block.call] = true
				}
				visible = visible.plus(p.ArgumentRate.times(float64(block.inputBytes))).plus(p.ToolOutputTokens)
			}
		}
		out.Output = out.Thinking.plus(visible)
		out.EvidenceScope = "retained_content_blocks"
		if r.envelope {
			out.EvidenceScope = "native_message_envelope"
		}
		report.Responses = append(report.Responses, out)
		if report.Output == nil {
			report.Output = &estimationRange{}
		}
		*report.Output = report.Output.plus(out.Output)
	}
	// Input follows output: the thinking a response is estimated to have
	// generated is context its later requests re-read.
	thinking := map[string]estimationRange{}
	for _, response := range report.Responses {
		thinking[response.NativeID] = response.Thinking
	}
	inputs := b.estimateInputs(manifest, thinking)
	for index := range report.Responses {
		if input := inputs[report.Responses[index].NativeID]; input != nil {
			report.Responses[index].Input = input
			if report.Input == nil {
				report.Input = &estimationRange{}
			}
			*report.Input = report.Input.plus(input.Tokens)
			report.InputRequests += input.Requests
		}
	}
	return report
}
