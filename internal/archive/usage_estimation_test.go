package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
	"github.com/gbdubs/pharos/estimation"
)

func TestUsageEstimationManifestAndFallbacks(t *testing.T) {
	m, err := parseEstimationManifest(estimation.Assumptions)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ model, effort, speed, fallback, basis string }{
		{"claude-opus-5-5", "high", "standard", "model_effort", "coding-study"},
		{"claude-opus-5-5", "high", "fast", "model_effort", "assumption"},
		{"claude-opus-4-6", "low", "standard", "model_effort", "assumption"},
		{"claude-opus-5-5", "", "standard", "model", "assumption"},
		{"new-unmapped-model", "high", "standard", "provider", "assumption"},
		{"", "", "standard", "provider", "assumption"},
	} {
		row, profile, fallback := m.resolve(tc.model, tc.effort, tc.speed)
		if fallback != tc.fallback || row.Basis != tc.basis || profile.ThinkingRate.Expected <= 0 {
			t.Fatalf("%+v resolved to %+v %s", tc, row, fallback)
		}
	}
	for _, change := range []func(*estimationManifest){
		func(d *estimationManifest) { d.Version++ },
		func(d *estimationManifest) { d.Models = append(d.Models, d.Models[0]) },
		func(d *estimationManifest) { d.Models[0].Profile = "missing" },
		func(d *estimationManifest) { d.Models[0].Notes = "" },
		func(d *estimationManifest) {
			p := d.Profiles["claude-default"]
			p.TextRate.Lower = -1
			d.Profiles["claude-default"] = p
		},
		func(d *estimationManifest) { d.DefaultToolSeconds.Upper = 0 },
		func(d *estimationManifest) { d.Local.MinimumFamilies = 0 },
		func(d *estimationManifest) { d.ResponseModels[0].Coefficients["unknown_feature"] = 1 },
		func(d *estimationManifest) { d.ResponseModels[0].Coefficients["tools"] = -1 },
		func(d *estimationManifest) { d.ResponseModels[0].Unit = "unreported" },
		func(d *estimationManifest) { d.ResponseModels[0].RequiresSettings = false },
		func(d *estimationManifest) { d.ResponseModels[0].Unit = "seconds" },
		func(d *estimationManifest) { d.ResponseModels[0].DonorFamilies = 0 },
		func(d *estimationManifest) { d.ResponseModels[0].MaximumTools = 0 },
		func(d *estimationManifest) { d.ResponseModels = append(d.ResponseModels, d.ResponseModels[0]) },
		func(d *estimationManifest) { d.ToolCategories["tool"] = "unrecognized" },
		func(d *estimationManifest) { d.Window.MaximumClockSkewSeconds = 30 },
		func(d *estimationManifest) { d.Window.GapOverheadSeconds = -1 },
		func(d *estimationManifest) { d.ToolTiming.ArgumentSecondsPerCharacter.Expected = -1 },
		func(d *estimationManifest) { d.ToolTiming.SupportedTools = nil },
		func(d *estimationManifest) { d.ToolTiming.DonorFamilies = 0 },
		func(d *estimationManifest) { d.ResponseModels[len(d.ResponseModels)-1].Evidence = "observed_window" },
		func(d *estimationManifest) { d.VisibleThinkingModels[0].TokensPerCharacter = -1 },
		func(d *estimationManifest) { d.VisibleThinkingModels[0].MaximumBlocks = 0 },
		func(d *estimationManifest) {
			d.VisibleThinkingModels = append(d.VisibleThinkingModels, d.VisibleThinkingModels[0])
		},
		func(d *estimationManifest) { d.VisibleThinkingDuration.TextToThinkingDurationRatio = -1 },
	} {
		d, _ := parseEstimationManifest(estimation.Assumptions)
		change(&d)
		if _, err := parseEstimationManifest([]byte(jsonText(d))); err == nil {
			t.Fatal("invalid assumptions accepted")
		}
	}
	for _, data := range [][]byte{append(append([]byte{}, estimation.Assumptions...), []byte(` {}`)...), []byte(`{"version":1,"unknown":2}`)} {
		if _, err := parseEstimationManifest(data); err == nil {
			t.Fatal("invalid manifest shape accepted")
		}
	}
	c, config := testCatalog(t)
	s := testServer(t, config, c)
	w := apiCall(t, s, config.APIToken, http.MethodGet, "/api/estimation/assumptions", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"manifest"`) || !strings.Contains(w.Body.String(), m.Revision) {
		t.Fatalf("published assumptions not exposed: %d %s", w.Code, w.Body.String())
	}
}

func TestUsageEstimationResponseModelsAndWindowGuards(t *testing.T) {
	manifest, err := parseEstimationManifest(estimation.Assumptions)
	if err != nil {
		t.Fatal(err)
	}
	// Small synthetic coefficients keep this test about evidence selection and
	// interval accounting, independent of fitted numerical coefficients.
	model := func(id, evidence, unit string, coefficients map[string]float64) estimationResponseModel {
		return estimationResponseModel{ID: id, Evidence: evidence, Unit: unit, Model: "*", Effort: "*", Speed: "*", Coefficients: coefficients, MinimumTools: 1, MaximumTools: 10, MaximumVisibleCharacters: 10000, MinimumWindowSeconds: .1, MaximumWindowSeconds: 1800, UpperMultiplier: 4, DonorResponses: 100, DonorFamilies: 20, Basis: "chat-duration-study"}
	}
	observed := model("observed", "observed_window", "seconds", map[string]float64{"residual_minutes": 60})
	window := model("window", "window", "seconds", map[string]float64{"elapsed_minutes": 10, "first_call_minutes": 30})
	body := model("body", "body", "tokens", map[string]float64{"tools": 100})
	body.Model, body.Effort, body.Speed, body.RequiresSettings = "claude-opus-5-5", "high", "standard", true
	body.MinimumWindowSeconds, body.MaximumWindowSeconds, body.Basis = 0, 0, "coding-study"
	manifest.ResponseModels = []estimationResponseModel{body, window, observed}
	blocks := []map[string]any{
		{"type": "tool_use", "id": "a", "name": "web_search", "start_timestamp": "2026-11-11T10:00:02Z", "input": map[string]any{"q": "test"}},
		{"type": "tool_use", "id": "b", "name": "web_search", "start_timestamp": "2026-11-11T10:00:03Z", "input": map[string]any{"q": "test"}},
		{"type": "tool_result", "tool_use_id": "a", "start_timestamp": "2026-11-11T10:00:06Z"},
		{"type": "tool_result", "tool_use_id": "b", "start_timestamp": "2026-11-11T10:00:08Z"},
		{"type": "text", "text": "Done", "start_timestamp": "2026-11-11T10:00:10Z", "stop_timestamp": "2026-11-11T10:00:12Z"},
	}
	_, profile, _ := manifest.resolve("claude-opus-5-5", "high", "standard")
	for _, tc := range []struct {
		name, want string
		change     func([]map[string]any)
	}{
		{"overlapping native tool spans", "observed", nil},
		{"missing result clock", "window", func(b []map[string]any) { delete(b[3], "start_timestamp") }},
		{"missing call clock", "body", func(b []map[string]any) { delete(b[0], "start_timestamp"); delete(b[1], "start_timestamp") }},
		{"no native end", "body", func(b []map[string]any) { delete(b[4], "stop_timestamp") }},
		{"out of clock domain", "body", func(b []map[string]any) { b[4]["stop_timestamp"] = "2026-11-11T11:00:00Z" }},
		{"native result after final response", "body", func(b []map[string]any) { b[3]["start_timestamp"] = "2026-11-11T10:00:20Z" }},
		{"argument stop after result", "body", func(b []map[string]any) { b[0]["stop_timestamp"] = "2026-11-11T10:00:20Z" }},
		{"async acknowledgment", "body", func(b []map[string]any) { b[0]["name"] = "Agent" }},
		{"unpaired result", "", func(b []map[string]any) { b[3]["tool_use_id"] = "other" }},
		{"duplicate call identity", "", func(b []map[string]any) { b[1]["id"] = "a" }},
		{"nontext tool context", "", func(b []map[string]any) {
			b[2]["content"] = []any{map[string]any{"type": "image", "source": map[string]any{"type": "base64", "data": "unmeasured"}}}
		}},
		{"summary evidence takes precedence", "", func(b []map[string]any) {
			b[2]["type"] = "thinking"
			b[2]["summaries"] = []any{map[string]any{"summary": "Summary"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var copied []map[string]any
			if err := json.Unmarshal([]byte(jsonText(blocks)), &copied); err != nil {
				t.Fatal(err)
			}
			if tc.change != nil {
				tc.change(copied)
			}
			b := newUsageEstimationBuilder("claude-ai-export")
			rows := estimationFixtureRows(t, copied, nil)
			// Reader transforms happen before the reader sorts SQL row references.
			for i := len(rows) - 1; i >= 0; i-- {
				b.observe(rows[i])
			}
			b.observe(map[string]any{"id": "user", "native_id": "prompt", "kind": "message", "role": "user", "raw_text": jsonText(map[string]any{"text": "Go", "claude_ai_message": map[string]any{"uuid": "prompt", "sender": "human", "created_at": "2026-11-11T10:00:00Z"}}), "created_at": "2026-11-12T10:00:00Z", "source_order": -1})
			report := b.estimate(manifest, estimationCalibration{}, "digest")
			if len(report.Responses) != 1 || !report.Output.valid() {
				t.Fatal("missing response estimate", report)
			}
			r := report.Responses[0]
			if r.ResponseModel != tc.want {
				t.Fatalf("wrong evidence route: want %s, got %+v", tc.want, r)
			}
			if tc.want == "observed" {
				if r.Window == nil || r.Window.Seconds != 12 || r.Window.ToolCoveredSeconds == nil || *r.Window.ToolCoveredSeconds != 6 || *r.Window.ResidualSeconds != 6 || r.ThinkingSeconds.Expected != 6 || !r.SecondsAvailable {
					t.Fatal("tool overlap or native clock provenance lost", r)
				}
				if math.Abs(r.Thinking.Expected-6*profile.ThinkingRate.Expected) > 1e-8 {
					t.Fatal("duration model did not use model/effort thinking rate", r)
				}
			}
			if tc.want == "window" && (r.Window.ResidualSeconds != nil || r.Window.ToolCoveredSeconds != nil || r.Window.ObservedToolSpans != 1 || r.ThinkingSeconds.Expected != 3) {
				t.Fatal("missing completions became measured intervals", r)
			}
			if tc.want == "body" && (r.Thinking.Expected != 200 || r.SecondsAvailable || r.ThinkingSeconds.Expected != 0) {
				t.Fatal("token model invented a duration or counted a turn twice", r)
			}
			if strings.Contains(tc.name, "summary") && (r.MissingBlocks != 1 || r.ThinkingSeconds.Expected != 7) {
				t.Fatal("summary evidence was replaced by a whole-response model", r)
			}
		})
	}

	for _, tc := range []struct{ model, effort, speed, source string }{{"", "", "standard", "export"}, {"different", "high", "standard", "export"}, {"claude-opus-5-5", "", "standard", "export"}, {"claude-opus-5-5", "high", "fast", "export"}, {"claude-opus-5-5", "high", "standard", "assumed_standard"}} {
		r := &usageEstimationResponse{model: tc.model, effort: tc.effort, speed: tc.speed, speedSource: tc.source, blocks: []usageEstimationBlock{{kind: "tool_use", call: "a", name: "web_search", orderKnown: true}, {kind: "tool_result", call: "a", orderKnown: true}, {kind: "text", characters: 4, orderKnown: true}}}
		if _, ok := newUsageEstimationBuilder("claude-ai-export").estimateMissingResponse(r, manifest, []usageToolEstimate{{CallID: "a", Status: "estimated"}}); ok {
			t.Fatal("unsupported settings borrowed an exact token-model fit", tc)
		}
	}
	for _, blocks := range [][]usageEstimationBlock{
		{{kind: "tool_use", call: "a", orderKnown: true}, {kind: "tool_result", call: "a", orderKnown: true}, {kind: "tool_result", call: "a", orderKnown: true}, {kind: "text", characters: 4, orderKnown: true}},
		{{kind: "tool_result", call: "a", orderKnown: true}, {kind: "tool_use", call: "a", orderKnown: true}, {kind: "text", characters: 4, orderKnown: true}},
		{{kind: "tool_use", call: "a", orderKnown: true}, {kind: "tool_result", call: "a", orderKnown: true}, {kind: "tool_result", call: "stray", orderKnown: true}, {kind: "text", characters: 4, orderKnown: true}},
	} {
		if _, complete := estimationBodyFeatures(&usageEstimationResponse{blocks: blocks}, manifest); complete {
			t.Fatal("ambiguous result identity was accepted as complete response evidence", blocks)
		}
	}
	// A complete call/result body can have missing arguments. Models that need
	// their size must decline it, while models using only observed clocks remain
	// eligible. A known empty object still has a measured serialized size.
	r := &usageEstimationResponse{blocks: []usageEstimationBlock{
		{kind: "tool_use", call: "a", orderKnown: true},
		{kind: "tool_result", call: "a", orderKnown: true},
		{kind: "text", characters: 4, orderKnown: true},
	}}
	features, complete := estimationBodyFeatures(r, manifest)
	if _, known := features["argument_kbytes"]; !complete || known {
		t.Fatal("missing argument payload became measured zero bytes", features)
	}
	needsArguments := body
	needsArguments.Coefficients = map[string]float64{"argument_kbytes": 100}
	if _, ok := predictEstimationResponseModel(needsArguments, features); ok {
		t.Fatal("model accepted an unavailable argument-size predictor")
	}
	r.blocks[0].inputKnown, r.blocks[0].inputBytes = true, 2
	features, complete = estimationBodyFeatures(r, manifest)
	if !complete || features["argument_kbytes"] != .002 {
		t.Fatal("known empty arguments lost measured byte size", features)
	}
}

func TestUsageEstimationConnectedThinkingCalibration(t *testing.T) {
	policy := estimationLocalPolicy{MinimumRequests: 1, MinimumFamilies: 2, PriorFamilies: 2}
	manifest := estimationManifest{Local: policy}
	prior := estimationRange{60, 10, 150}
	book := estimationCalibration{Groups: map[string]estimationRateGroup{}}
	book.Groups[estimationGroupKey("model", "high", "standard", 2)] = estimationRateGroup{Rate: estimationRange{40, 20, 80}, Requests: 100, Families: 2}
	book.Groups[estimationGroupKey("model", "high", "standard", 7)] = estimationRateGroup{Rate: estimationRange{100, 50, 150}, Requests: 100, Families: 2}
	r := &usageEstimationResponse{model: "model", effort: "high", speed: "standard", blocks: []usageEstimationBlock{
		{kind: "thinking", start: "2026-11-11T10:00:00Z", stop: "2026-11-11T10:00:02Z"},
		{kind: "thinking", start: "2026-11-11T10:00:01Z", stop: "2026-11-11T10:00:02Z"},
		{kind: "thinking", start: "2026-11-11T10:00:10Z", stop: "2026-11-11T10:00:12Z"},
	}}
	p := estimationProfile{ThinkingRate: prior, SummarySeconds: estimationRange{7, 1, 25}, BlockSeconds: estimationRange{8, 1, 45}}
	seconds, _, _ := estimationThinkingSeconds(r.blocks, p)
	tokens, support, weight := estimateThinkingContributions(r, seconds, manifest, book, p)
	if seconds.Expected != 4 || tokens.Expected != 200 || support.Families != 2 || weight != .5 {
		t.Fatalf("native intervals were duplicated or assigned a response-total duration band: %+v %+v %v", tokens, support, weight)
	}
	r.blocks = append(r.blocks, usageEstimationBlock{kind: "thinking", summaries: 1})
	seconds, _, _ = estimationThinkingSeconds(r.blocks, p)
	tokens, _, _ = estimateThinkingContributions(r, seconds, manifest, book, p)
	if tokens.Expected != 760 {
		t.Fatal("partial-clock fallback double counted native thinking or lost the summary", tokens)
	}
	r.blocks = append(r.blocks, usageEstimationBlock{kind: "thinking", summaries: 1})
	seconds, _, _ = estimationThinkingSeconds(r.blocks, p)
	tokens, _, _ = estimateThinkingContributions(r, seconds, manifest, book, p)
	if tokens.Expected != 1320 {
		t.Fatal("unclocked blocks borrowed a combined duration band", tokens)
	}
	for _, duration := range []float64{0, .01, 601, math.NaN(), math.Inf(1)} {
		rate, _, weight := estimationThinkingRate(manifest, book, "model", "high", "standard", duration, prior)
		if rate != prior || weight != 0 {
			t.Fatal("unsupported duration borrowed a local native-clock band", duration, rate)
		}
	}
	r.blocks = []usageEstimationBlock{{kind: "thinking", start: "2026-11-11T10:00:00.001Z", stop: "2026-11-11T10:00:00.101Z"}}
	seconds, clocked, missing := estimationThinkingSeconds(r.blocks, p)
	if seconds.Expected != .1 || clocked != 1 || missing != 0 {
		t.Fatal("epoch float precision changed a native 100-ms timer", seconds, clocked, missing)
	}
}

func TestUsageEstimationObservedGapEvidence(t *testing.T) {
	start := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	end := start.Add(12 * time.Second)
	text := usageEstimationBlock{kind: "text", characters: 4, start: "2026-11-11T10:00:10Z", stop: "2026-11-11T10:00:12Z"}
	spans := [][2]float64{{2, 6}, {3, 8}}
	for _, withThinking := range []bool{false, true} {
		blocks := []usageEstimationBlock{text}
		if withThinking {
			blocks = append(blocks, usageEstimationBlock{kind: "thinking", start: "2026-11-11T10:00:01Z", stop: "2026-11-11T10:00:08Z"})
		}
		f := map[string]float64{}
		window := &usageWindowEvidence{}
		estimationGapFeatures(blocks, start, end, spans, 1, f, window)
		if window.UnexplainedSeconds == nil || *window.UnexplainedSeconds != 4 || *window.NonthinkingCoveredSeconds != 8 || *window.GapAdjustedSeconds != 2 || window.GapCount != 2 || f["gap_excess_minutes"] != 2./60 {
			t.Fatal("gap union, overhead or thinking-clock masking was lost", window, f)
		}
	}
	text.start = ""
	f := map[string]float64{}
	window := &usageWindowEvidence{}
	estimationGapFeatures([]usageEstimationBlock{text}, start, end, spans, 1, f, window)
	if _, present := f["gap_excess_minutes"]; present || window.UnexplainedSeconds != nil {
		t.Fatal("missing text clock became measured zero coverage", window, f)
	}
	manifest, _ := bundledEstimation()
	for _, model := range manifest.ResponseModels {
		if model.Evidence == "observed_gaps" {
			f = map[string]float64{"tools": 1, "visible_kchars": 1, "elapsed_minutes": 1}
			if _, ok := predictEstimationResponseModel(model, f); ok {
				t.Fatal("missing gap measurement became zero")
			}
			f["gap_excess_minutes"] = 0
			if value, ok := predictEstimationResponseModel(model, f); ok && value.Expected == 0 {
				t.Fatal("zero unexplained time became proof of zero thinking")
			}
		}
	}
}

func TestUsageEstimationPublishedModelParity(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "usage-estimation-model-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		ModelID  string             `json:"model_id"`
		Features map[string]float64 `json:"features"`
		Expected float64            `json:"expected"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	manifest, err := bundledEstimation()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		found := false
		for _, model := range manifest.ResponseModels {
			if model.ID != tc.ModelID {
				continue
			}
			found = true
			value, valid := predictEstimationResponseModel(model, tc.Features)
			if !valid || math.Abs(value.Expected-tc.Expected) > max(1e-8, tc.Expected*1e-12) {
				t.Fatalf("Python-exported inference differs in Go for %s: %+v want %v", tc.ModelID, value, tc.Expected)
			}
		}
		if !found {
			t.Fatal("missing published model", tc.ModelID)
		}
	}
}

func estimationFixtureRows(t *testing.T, blocks []map[string]any, usage map[string]any) []map[string]any {
	t.Helper()
	envelope := map[string]any{"uuid": "reply", "sender": "assistant", "parent_message_uuid": "prompt", "model": "claude-opus-5-5", "effort": "high", "speed": "standard"}
	if usage != nil {
		envelope["usage"] = usage
	}
	rows := []map[string]any{}
	for i, block := range blocks {
		raw := map[string]any{}
		for k, v := range block {
			raw[k] = v
		}
		if i == 0 {
			raw["claude_ai_message"] = envelope
		}
		kind, role := "reasoning", "assistant"
		if block["type"] == "text" {
			kind = "message"
		} else if block["type"] == "tool_use" {
			kind = "tool_call"
		} else if block["type"] == "tool_result" {
			kind, role = "tool_result", "tool"
		}
		rows = append(rows, map[string]any{"id": jsonText(i), "native_id": "reply:block:" + jsonText(i), "role": role, "kind": kind, "raw_text": jsonText(raw), "selected": 1, "source_order": i,
			"created_at": "2026-11-11T11:59:59Z"}) // this late envelope clock must never supply missing block clocks
	}
	return rows
}

func TestUsageEstimationUsesNativeClocksAndKeepsReportedUsageSeparate(t *testing.T) {
	manifest, err := bundledEstimation()
	if err != nil {
		t.Fatal(err)
	}
	blocks := []map[string]any{
		{"type": "thinking", "start_timestamp": "2026-11-11T10:00:00Z", "stop_timestamp": "2026-11-11T10:00:10Z", "summaries": []any{map[string]any{"summary": "Not a token count"}}},
		{"type": "thinking", "start_timestamp": "2026-11-11T10:00:05Z", "stop_timestamp": "2026-11-11T10:00:15Z"},
		{"type": "tool_use", "id": "web", "name": "web_search", "start_timestamp": "2026-11-11T10:00:20Z", "stop_timestamp": "2026-11-11T10:00:20.1Z", "input": map[string]any{"query": "test"}},
		{"type": "tool_result", "tool_use_id": "web", "start_timestamp": "2026-11-11T10:00:22Z"},
		{"type": "tool_use", "id": "unknown-clock", "name": "web_search", "input": map[string]any{"query": "test"}},
		{"type": "tool_result", "tool_use_id": "unknown-clock", "content": []any{map[string]any{"type": "text", "text": `{"usage":{"output_tokens":999999}}`}}},
		{"type": "tool_use", "id": "agent", "name": "Agent", "start_timestamp": "2026-11-11T10:00:25Z"},
		{"type": "tool_result", "tool_use_id": "agent", "start_timestamp": "2026-11-11T10:00:25.01Z"},
		{"type": "tool_use", "id": "unpaired", "name": "Bash"},
		{"type": "text", "text": strings.Repeat("A", 400)},
	}
	rows := estimationFixtureRows(t, blocks, nil)
	b := newUsageEstimationBuilder("claude-ai-export")
	for _, m := range rows {
		b.observe(m)
	}
	report := b.estimate(manifest, estimationCalibration{}, "digest")
	if len(report.Responses) != 1 || report.Output == nil || report.ReportedResponses != 0 {
		t.Fatalf("missing estimates: %+v", report)
	}
	r := report.Responses[0]
	if r.ThinkingSeconds.Expected != 15 || r.ClockedBlocks != 2 || r.MissingBlocks != 0 {
		t.Fatalf("overlapping intervals must be unioned, summaries not added again: %+v", r)
	}
	if r.Output.Expected <= r.Thinking.Expected || !r.Output.valid() {
		t.Fatalf("output includes visible text and arguments: %+v", r)
	}
	if r.Tools[0].Status != "observed" || r.Tools[0].Seconds.Expected != 2 {
		t.Fatalf("native result clock, not argument stop, establishes span: %+v", r.Tools)
	}
	if r.Tools[1].Status != "estimated" || r.Tools[1].Basis != "published_argument_stream_and_execution" || r.Tools[1].Seconds.Expected <= 0 || r.Tools[1].ArgumentStatus != "estimated" || r.Tools[1].ExecutionStatus != "estimated" {
		t.Fatalf("missing result clocks use explicit priors: %+v", r.Tools)
	}
	for _, tool := range r.Tools[2:] {
		if tool.Status != "unavailable" || tool.Seconds != nil {
			t.Fatalf("acknowledgments and missing results cannot become completion estimates: %+v", tool)
		}
	}
	// Separate rendering can shorten raw bodies without changing the retained features.
	for _, m := range rows {
		m["raw_text"] = "clipped"
	}
	if b.estimate(manifest, estimationCalibration{}, "digest").Output.Expected != report.Output.Expected {
		t.Fatal("clipping changed already extracted features")
	}
	known := newUsageEstimationBuilder("claude-ai-export")
	for _, m := range estimationFixtureRows(t, blocks, map[string]any{"input_tokens": 10, "output_tokens": 0}) {
		known.observe(m)
	}
	knownReport := known.estimate(manifest, estimationCalibration{}, "digest")
	if knownReport.Output != nil || knownReport.ReportedResponses != 1 {
		t.Fatalf("reported zero output must not receive estimated replacement usage: %+v", knownReport)
	}
}

func TestUsageEstimationToolComponentsAndVisibleThinking(t *testing.T) {
	manifest, _ := bundledEstimation()
	for _, tc := range []struct {
		name, total, argument, execution string
		stop, result, tool               string
		input                            bool
	}{
		{"native components", "observed", "observed", "observed", "02", "03", "web_search", true},
		{"native argument and prior execution", "estimated", "observed", "estimated", "02", "", "web_search", true},
		{"unclocked components", "estimated", "estimated", "estimated", "", "", "web_search", true},
		{"total without argument clock", "observed", "unavailable", "unavailable", "", "03", "web_search", true},
		{"inverted component clock", "observed", "unavailable", "unavailable", "04", "03", "web_search", true},
		{"unknown input", "estimated", "unavailable", "unavailable", "", "", "web_search", false},
		{"unknown operation", "estimated", "unavailable", "unavailable", "", "", "mcp__unknown__operation", true},
		{"async acknowledgment", "unavailable", "observed", "unavailable", "02", "03", "Agent", true},
		{"recorded zero execution", "observed", "observed", "observed", "02", "02", "web_search", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := map[string]any{"type": "tool_use", "id": "call", "name": tc.tool, "start_timestamp": "2026-11-11T10:00:00Z"}
			if tc.input {
				call["input"] = map[string]any{"query": "界🙂"}
			}
			if tc.stop != "" {
				call["stop_timestamp"] = "2026-11-11T10:00:" + tc.stop + "Z"
			}
			result := map[string]any{"type": "tool_result", "tool_use_id": "call", "content": "Done"}
			if tc.result != "" {
				result["start_timestamp"] = "2026-11-11T10:00:" + tc.result + "Z"
			}
			b := newUsageEstimationBuilder("claude-ai-export")
			for _, row := range estimationFixtureRows(t, []map[string]any{call, result, {"type": "text", "text": "Done"}}, nil) {
				b.observe(row)
			}
			tool := b.estimate(manifest, estimationCalibration{}, "digest").Responses[0].Tools[0]
			if tool.Status != tc.total || tool.ArgumentStatus != tc.argument || tool.ExecutionStatus != tc.execution {
				t.Fatalf("wrong component evidence: %+v", tool)
			}
			if tool.ExecutionSeconds != nil && tool.ArgumentSeconds != nil && math.Abs(tool.Seconds.Expected-tool.ExecutionSeconds.Expected-tool.ArgumentSeconds.Expected) > 1e-9 {
				t.Fatal("components do not reconcile", tool)
			}
			if tc.name == "recorded zero execution" && (tool.ExecutionSeconds == nil || tool.ExecutionSeconds.Expected != 0) {
				t.Fatal("recorded zero was lost", tool)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		hidden any
		clock  bool
		text   bool
		want   bool
	}{
		{"explicit visible native", false, true, true, true},
		{"explicit visible unclocked", false, false, true, true},
		{"hidden text is not visible", true, true, true, false},
		{"missing flag is not visible", nil, true, true, false},
		{"summaries are not thinking text", false, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			thought := map[string]any{"type": "thinking", "summaries": []any{map[string]any{"summary": strings.Repeat("Summary", 3000)}}}
			if tc.hidden != nil {
				thought["thinking_hidden"] = tc.hidden
			}
			if tc.text {
				thought["thinking"] = strings.Repeat("界🙂", 5000)
			}
			if tc.clock {
				thought["start_timestamp"] = "2026-11-11T10:00:00Z"
				thought["stop_timestamp"] = "2026-11-11T10:00:01Z"
			}
			b := newUsageEstimationBuilder("claude-ai-export")
			for _, row := range estimationFixtureRows(t, []map[string]any{thought, {"type": "text", "text": "Done"}}, nil) {
				b.observe(row)
			}
			r := b.estimate(manifest, estimationCalibration{}, "digest").Responses[0]
			if r.VisibleThinkingAdjustment != tc.want || !r.Output.valid() {
				t.Fatal("wrong visible-thinking contribution", r)
			}
			if tc.want && r.VisibleThinkingCharacters != 10000 {
				t.Fatal("visible text not counted once by Unicode character", r)
			}
			if tc.want && tc.clock && r.Thinking.Expected != manifest.Profiles[r.Profile].VisibleThinkingRate.Expected*10000 {
				t.Fatal("native clock text floor changed", r)
			}
			if tc.want && !tc.clock && (r.ResponseModel != "complete-visible-unclocked-v4" || !r.SecondsAvailable || r.ThinkingDurationModel == "" || r.ModelFamilies == 0) {
				t.Fatal("complete visible unclocked text did not use its validated token model", r)
			}
			if tc.clock && r.ThinkingSeconds.Expected != 1 {
				t.Fatal("text contribution overwrote native duration", r)
			}
			if !tc.want && r.VisibleThinkingCharacters != 0 {
				t.Fatal("hidden or summary text became visible evidence", r)
			}
		})
	}
}

func TestUsageEstimationCompleteVisibleModelGuards(t *testing.T) {
	manifest, _ := bundledEstimation()
	for _, tc := range []struct {
		name                        string
		characters, blocks, clocked int
		partial                     bool
		want                        bool
	}{
		{"complete unclocked", 1000, 1, 0, false, true},
		{"two blocks", 1000, 2, 0, false, true},
		{"native time takes precedence", 1000, 1, 1, false, false},
		{"partial trace", 1000, 2, 0, true, false},
		{"unsupported block count", 1000, 3, 0, false, false},
		{"unsupported character domain", 1000000, 1, 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &usageEstimationResponse{speed: "standard", speedSource: "assumed_standard"}
			for i := 0; i < tc.blocks; i++ {
				chars := tc.characters
				if tc.partial && i == 0 {
					chars = 0
				}
				r.blocks = append(r.blocks, usageEstimationBlock{kind: "thinking", visibleThinkingCharacters: chars})
			}
			value, _, ok := manifest.estimateCompleteVisibleThinking(r, tc.clocked, tc.blocks)
			if ok != tc.want || ok && (!value.valid() || value.Expected <= 0) {
				t.Fatal("wrong complete-visible evidence gate", value, ok)
			}
		})
	}
}

func TestUsageEstimationVisibleDurationAndTokensStayIndependent(t *testing.T) {
	manifest, _ := parseEstimationManifest(estimation.Assumptions)
	r := &usageEstimationResponse{blocks: []usageEstimationBlock{{kind: "thinking", visibleThinkingCharacters: 200}, {kind: "text", characters: 200, start: "2026-11-11T10:00:00Z", stop: "2026-11-11T10:00:02Z"}}}
	tokens, _, ok := manifest.estimateCompleteVisibleThinking(r, 0, 1)
	if !ok {
		t.Fatal("missing visible token model")
	}
	duration, model, ok := manifest.estimateVisibleThinkingDuration(r, 200)
	if !ok || model != "chat-visible-stream-v4" || !duration.valid() {
		t.Fatal("native text rate was not used", duration, model)
	}
	manifest.VisibleThinkingDuration.TextToThinkingDurationRatio *= 2
	changed, _, _ := manifest.estimateVisibleThinkingDuration(r, 200)
	unchanged, _, _ := manifest.estimateCompleteVisibleThinking(r, 0, 1)
	if changed.Expected != duration.Expected*2 || unchanged != tokens {
		t.Fatal("independent timing changed token prediction", tokens, unchanged, duration, changed)
	}
	r.blocks[1].start = ""
	if _, model, ok := manifest.estimateVisibleThinkingDuration(r, 200); !ok || model != "chat-visible-characters-v4" {
		t.Fatal("missing text clock did not retain explicit character fallback", model)
	}
	b := newUsageEstimationBuilder("claude-ai-export")
	b.responses["prompt"] = &usageEstimationResponse{sender: "human", nativeCreated: "2026-11-11T10:00:00Z"}
	r.prompt = "prompt"
	if b.estimationWindow(r, manifest, nil, map[string]float64{"tools": 0}) == nil {
		t.Fatal("native final text clock did not establish a duration bound")
	}
	for _, kind := range []string{"thinking", "tool_result"} {
		r.blocks[1].kind = kind
		if b.estimationWindow(r, manifest, nil, map[string]float64{"tools": 0}) != nil {
			t.Fatal("intermediate block stop masqueraded as final text stop", kind)
		}
	}
}

func TestUsageEstimationFallbackSummariesMissingSettingsAndBranches(t *testing.T) {
	manifest, _ := bundledEstimation()
	rows := estimationFixtureRows(t, []map[string]any{{"type": "thinking", "summaries": []any{map[string]any{"summary": "One"}, map[string]any{"summary": "Two"}}}, {"type": "text", "text": "Hello"}}, nil)
	var raw map[string]any
	_ = json.Unmarshal([]byte(firstString(rows[0]["raw_text"])), &raw)
	envelope := mapValue(raw["claude_ai_message"])
	delete(envelope, "model")
	delete(envelope, "effort")
	rows[0]["raw_text"] = jsonText(raw)
	b := newUsageEstimationBuilder("claude-ai-export")
	for _, m := range rows {
		b.observe(m)
	}
	report := b.estimate(manifest, estimationCalibration{}, "digest")
	r := report.Responses[0]
	if r.Fallback != "provider" || r.Model != "" || r.Effort != "" || r.ThinkingSeconds.Expected != 14 || r.MissingBlocks != 1 {
		t.Fatalf("missing settings/clocks must use published assumptions: %+v", r)
	}
	if r.ThinkingSeconds.Lower >= r.ThinkingSeconds.Expected || r.ThinkingSeconds.Upper <= r.ThinkingSeconds.Expected {
		t.Fatal("summary timing must retain sensitivity")
	}
	inactive := newUsageEstimationBuilder("claude-ai-export")
	for _, row := range rows {
		row["selected"] = 0
		inactive.observe(row)
	}
	if inactive.estimate(manifest, estimationCalibration{}, "digest").Output != nil {
		t.Fatal("inactive branches must not add to selected-branch estimates")
	}
	if newUsageEstimationBuilder("claude-code") != nil {
		t.Fatal("native reported accounting must not be replaced by export guesses")
	}
	_, profile, _ := manifest.resolve("", "", "standard")
	for _, stop := range []string{"2026-11-11T10:00:00Z", "2026-11-10T10:00:00Z"} {
		seconds, clocked, missing := estimationThinkingSeconds([]usageEstimationBlock{{kind: "thinking", start: "2026-11-11T10:00:00Z", stop: stop, summaries: 1}}, profile)
		if clocked != 0 || missing != 1 || seconds.Expected != profile.SummarySeconds.Expected {
			t.Fatal("zero or reversed clocks cannot establish a thinking interval", seconds)
		}
	}
	// Legacy indexed blocks can be useful even without a retained envelope.
	delete(raw, "claude_ai_message")
	rows[0]["raw_text"] = jsonText(raw)
	legacy := newUsageEstimationBuilder("claude-ai-export")
	for _, row := range rows {
		row["selected"] = 1
		legacy.observe(row)
	}
	old := legacy.estimate(manifest, estimationCalibration{}, "digest")
	if old.Output == nil || old.Responses[0].EvidenceScope != "retained_content_blocks" {
		t.Fatal("retained native blocks unnecessarily require a reindex", old)
	}
	legacy.observe(map[string]any{"id": "usage", "native_id": "reply:usage", "role": "system", "kind": "metadata", "text": `{"type":"token_usage","usage_message_id":"reply","usage":{"input_tokens":0,"output_tokens":0}}`})
	if old := legacy.estimate(manifest, estimationCalibration{}, "digest"); old.Output != nil || old.ReportedResponses != 1 {
		t.Fatal("legacy explicit zero usage must still override guesses", old)
	}
	for _, tc := range []struct {
		role, kind string
		typed      bool
		want       bool
	}{
		{"assistant", "message", false, true},
		{"user", "message", false, false},
		{"tool", "tool_result", false, false},
		{"assistant", "metadata", false, false},
		{"assistant", "message", true, false},
	} {
		raw := map[string]any{"text": "Hi 世界🙂"}
		if tc.typed {
			raw["type"] = ""
		}
		b := newUsageEstimationBuilder("claude-ai-export")
		b.observe(map[string]any{"id": "text", "native_id": "text", "kind": tc.kind, "role": tc.role, "text": raw["text"], "raw_text": jsonText(raw)})
		result := b.estimate(manifest, estimationCalibration{}, "digest")
		if (result.Output != nil) != tc.want {
			t.Fatalf("text-only fallback must respect stored message ownership: %+v %+v", tc, result)
		}
		if tc.want && result.Output.Expected != profile.WithoutTraceTokens.Expected+6*profile.TextRate.Expected {
			t.Fatal("text-only fallback must count Unicode characters and include the missing-trace assumption", result.Output)
		}
	}
}

func TestUsageEstimationLocalRatesBalanceFamiliesAndRequireSupport(t *testing.T) {
	policy := estimationLocalPolicy{MinimumRequests: 6, MinimumFamilies: 2, PriorFamilies: 2}
	donors := []estimationDonor{}
	for i := range 5 {
		donors = append(donors, estimationDonor{Native: jsonText(i), Conversation: "child", Model: "model", Effort: "high", Speed: "standard", Seconds: 10, Tokens: 1000})
	}
	donors = append(donors, estimationDonor{Native: "other", Conversation: "other", Model: "model", Effort: "high", Speed: "standard", Seconds: 10, Tokens: 100})
	donors = append(donors, donors[0]) // source mirror
	groups, retained := fitEstimationRates(donors, map[string]string{"child": "root"}, policy)
	key := estimationGroupKey("model", "high", "standard", 10)
	g := groups[key]
	if retained != 6 || g.Requests != 6 || g.Families != 2 || g.Rate.Expected != 55 {
		t.Fatalf("family balance or mirror deduplication lost: %+v retained=%d", g, retained)
	}
	manifest := estimationManifest{Local: policy}
	prior := estimationRange{40, 5, 150}
	rate, support, weight := estimationThinkingRate(manifest, estimationCalibration{Groups: groups}, "model", "high", "standard", 10, prior)
	if rate.Expected != 47.5 || weight != .5 || support.Families != 2 || rate.Lower > prior.Lower || rate.Upper < prior.Upper {
		t.Fatalf("prior must retain transfer sensitivity: %+v %+v %v", rate, support, weight)
	}
	for _, setting := range [][2]string{{"high", "fast"}, {"low", "standard"}, {"", "standard"}} {
		rate, _, weight := estimationThinkingRate(manifest, estimationCalibration{Groups: groups}, "model", setting[0], setting[1], 10, prior)
		if rate != prior || weight != 0 {
			t.Fatal("unsupported effort or speed borrowed unrelated local rates")
		}
	}
	policy.MinimumFamilies = 3
	if groups, _ := fitEstimationRates(donors, nil, policy); len(groups) != 0 {
		t.Fatal("insufficient independent support must keep the published prior")
	}
}

func TestUsageEstimationCalibrationRefreshAndInvalidation(t *testing.T) {
	c, _ := testCatalog(t)
	c.now = func() time.Time { return time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC) }
	// No coding history is a successful calibration pass, not a missing-data error.
	if err := c.refreshUsageEstimationCalibration(t.Context()); err != nil {
		t.Fatal(err)
	}
	book, err := c.loadEstimationCalibration(t.Context())
	if err != nil || book.Version == "" || len(book.Groups) != 0 {
		t.Fatalf("empty library must keep bundled assumptions available: %+v %v", book, err)
	}
	book.Groups = map[string]estimationRateGroup{"sentinel": {Rate: estimationRange{40, 10, 100}, Requests: 100, Families: 20}}
	if _, err := c.DB.Exec("UPDATE meta SET value=? WHERE key=?", jsonText(book), estimationCalibrationKey); err != nil {
		t.Fatal(err)
	}
	if err := c.refreshUsageEstimationCalibration(t.Context()); err != nil {
		t.Fatal(err)
	}
	current, _ := c.loadEstimationCalibration(t.Context())
	if current.Groups["sentinel"].Families != 20 {
		t.Fatal("unchanged same-day data rebuilt needlessly")
	}
	if _, err := c.DB.Exec("INSERT INTO meta(key,value) VALUES('tool_ledger_generation','changed') ON CONFLICT(key) DO UPDATE SET value=excluded.value"); err != nil {
		t.Fatal(err)
	}
	stale, err := c.loadEstimationCalibration(t.Context())
	if usageEstimationSnapshotKey(book) != usageEstimationSnapshotKey(stale) {
		t.Fatal("coding ingest changed persisted estimate identity")
	}
	if err != nil || stale.Groups["sentinel"].Families != 20 {
		t.Fatal("unrelated coding ingest replaced last good calibration", stale, err)
	}
	if err := c.refreshUsageEstimationCalibration(t.Context()); err != nil {
		t.Fatal(err)
	}
	sameDay, _ := c.loadEstimationCalibration(t.Context())
	if sameDay.Groups["sentinel"].Families != 20 {
		t.Fatal("ordinary ingestion caused same-day recalibration")
	}
	c.now = func() time.Time { return time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC) }
	if err := c.refreshUsageEstimationCalibration(t.Context()); err != nil {
		t.Fatal(err)
	}
	current, _ = c.loadEstimationCalibration(t.Context())
	if !strings.HasPrefix(current.Generation, "changed/") || len(current.Groups) != 0 {
		t.Fatalf("removed donors left stale coefficients: %+v", current)
	}
	current.Manifest = "old-manifest"
	if _, err := c.DB.Exec("UPDATE meta SET value=? WHERE key=?", jsonText(current), estimationCalibrationKey); err != nil {
		t.Fatal(err)
	}
	if old, _ := c.loadEstimationCalibration(t.Context()); old.Version != "" {
		t.Fatal("old manifest must not reuse incompatible local calibration")
	}
}

func TestUsageEstimationCalibrationReadsOnlyEligibleNativeTimers(t *testing.T) {
	c, _ := testCatalog(t)
	c.now = func() time.Time { return time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC) }
	seedReaderWorkspace(t, c)
	tx, err := c.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE conversations SET harness=CASE id WHEN 'main' THEN 'claude-code/sdk-ts' ELSE 'conductor' END"); err != nil {
		t.Fatal(err)
	}
	for i, row := range []struct {
		id, conversation, speedSource string
		milliseconds                  int
	}{
		{"native", "main", "speed", 10000},
		{"tiny-timer", "main", "speed", 1},
		{"unknown-speed", "main", "assumed_standard", 10000},
		{"mirror", "agent", "speed", 10000},
	} {
		if _, err := tx.Exec(`INSERT INTO model_requests(id,conversation_id,native_id,sequence,requested_at,model,effort,speed,speed_source,output_tokens,reasoning_output_tokens)
 VALUES(?,?,?,?,?,'claude-opus-5-5','high','standard',?,600,500)`, row.id, row.conversation, "api-"+row.id, i, "2026-11-11T10:00:00Z", row.speedSource); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec("INSERT INTO model_request_evidence(message_id,conversation_id,native_id,thinking_ms) VALUES(?,?,?,?)", row.id, row.conversation, "api-"+row.id, row.milliseconds); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := c.refreshUsageEstimationCalibration(t.Context()); err != nil {
		t.Fatal(err)
	}
	book, err := c.loadEstimationCalibration(t.Context())
	if err != nil || book.Requests != 1 || len(book.Groups) != 0 {
		t.Fatalf("donor selection or support gate failed: %+v %v", book, err)
	}
	// A timer-only correction leaves usage unchanged but invalidates the fit.
	if _, err := c.DB.Exec("UPDATE model_request_evidence SET thinking_ms=5000 WHERE message_id='native'"); err != nil {
		t.Fatal(err)
	}
	if old, err := c.loadEstimationCalibration(t.Context()); err != nil || old.Generation != book.Generation {
		t.Fatal("timer correction discarded the last compatible calibration", err)
	}
	if err := c.refreshUsageEstimationCalibration(t.Context()); err != nil {
		t.Fatal(err)
	}
	refreshed, err := c.loadEstimationCalibration(t.Context())
	if err != nil || refreshed.Generation == book.Generation {
		t.Fatal("corrected timer was not recalibrated", refreshed, err)
	}
	if _, err := c.DB.Exec("UPDATE conversations SET parent_id=NULL WHERE id='agent'"); err != nil {
		t.Fatal(err)
	}
	if old, err := c.loadEstimationCalibration(t.Context()); err != nil || old.Generation != refreshed.Generation {
		t.Fatal("ancestry correction discarded last good snapshot", old, err)
	}
	if err := c.refreshUsageEstimationCalibration(t.Context()); err != nil {
		t.Fatal(err)
	}
	ancestry, _ := c.loadEstimationCalibration(t.Context())
	if ancestry.Generation == refreshed.Generation {
		t.Fatal("ancestry correction did not refresh the fit")
	}

}

func TestUsageEstimationBackfillResumesAndWithholdsSupersededRows(t *testing.T) {
	c, config := testCatalog(t)
	path := filepath.Join(t.TempDir(), "conversations.json")
	writeSourceFile(t, path, claudeAIExport)
	tx, err := c.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, record := range discoverClaudeAI(t, path) {
		if _, _, err := ingestWorkspace(tx, record, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	execTx(t, c.DB, "DELETE FROM usage_estimation_inputs", "DELETE FROM meta WHERE key='usage_estimation_store_version'")
	if err := c.ensureUsageEstimationStore(); err != nil {
		t.Fatal(err)
	}
	if countRows(t, c, "SELECT COUNT(*) FROM usage_estimation_inputs") != 2 {
		t.Fatal("old imports not enrolled")
	}
	override(t, &usageEstimationBatch, 1)
	if more, err := c.backfillUsageEstimationBatch(t.Context()); err != nil || !more {
		t.Fatal(more, err)
	}
	status, err := c.UsageEstimationStatus(t.Context())
	if err != nil || integer(status["pending_conversations"]) != 1 {
		t.Fatal(status, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.backfillUsageEstimation(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := c.backfillUsageEstimation(t.Context()); err != nil {
		t.Fatal(err)
	}
	predicted := func() int { return countRows(t, c, "SELECT COUNT(*) FROM model_requests WHERE is_estimate=1") }
	if predicted() != 3 {
		t.Fatal("missing canonical output estimates", predicted())
	}
	var total int64
	for _, row := range queryRows(t, c, "SELECT * FROM model_requests WHERE is_estimate=1") {
		total += integer(row["output_tokens"])
		if integer(row["input_tokens_available"]) != 0 || integer(row["total_tokens"]) != integer(row["output_tokens"]) {
			t.Fatal("invented input usage", row)
		}
	}
	if countRows(t, c, "SELECT SUM(total_tokens) FROM agent_session_usage WHERE is_estimate=1") != int(total) || countRows(t, c, "SELECT SUM(total_tokens) FROM agent_sessions WHERE is_estimate=1") != int(total) {
		t.Fatal("canonical totals disagree")
	}
	rows, err := c.usageRows(time.UTC)
	if err != nil || len(rows) == 0 {
		t.Fatal(rows, err)
	}
	for _, row := range rows {
		if !estimateFlag(row["is_estimate"]) || row["input_tokens"] != nil || row["cost_usd"] != nil {
			t.Fatal("unknown usage not preserved", row)
		}
	}
	if err := c.ensureToolRollup(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Run("optional calibration failure", func(t *testing.T) {
		override(t, &refreshUsageCalibration, func(*Catalog, context.Context) error { return context.DeadlineExceeded })
		if err := c.ensureToolRollup(t.Context()); err != nil {
			t.Fatal("optional refinement failed Tools", err)
		}
		status, err := c.UsageEstimationStatus(t.Context())
		if err != nil || status["calibration_error"] == nil || predicted() != 3 {
			t.Fatal(status, err)
		}
		canceled, cancel := context.WithCancel(t.Context())
		cancel()
		if err := c.ensureToolRollup(canceled); !errors.Is(err, context.Canceled) {
			t.Fatal("parent cancellation swallowed", err)
		}
	})
	s := testServer(t, config, c)
	q := groupRequest("provider", qt.SummaryColumn{ID: "tokens", Op: "sum", Field: "total_tokens"})
	got, _, err := s.executeQueryV3(t.Context(), "usage", nil, PharosUsageSchema(), q)
	if err != nil || len(got.Rows) != 1 || integer(got.Rows[0]["tokens"]) != total || !estimateFlag(got.Rows[0]["is_estimate"]) {
		t.Fatal(got, err)
	}

	for _, table := range []struct {
		name   string
		schema qt.Schema
		field  string
		flag   string
	}{
		{"library", PharosLibrarySchema(), "token_count", "is_estimate"},
		{"tool_calls", PharosToolCallsSchema(), "duration_ms", "duration_is_estimate"},
	} {
		request := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Rows: true, Result: qt.WireQuery{Select: []string{"id", table.field}, Limit: 10}}
		result, _, err := s.executeQueryV3(t.Context(), table.name, nil, table.schema, request)
		if err != nil || len(result.Rows) == 0 {
			t.Fatal(table.name, result, err)
		}
		for _, row := range result.Rows {
			if row[table.field] == nil || !estimateFlag(row[table.flag]) {
				t.Fatal(table.name, row)
			}
		}
	}
	// A complete response is not a measured model request.
	counts := groupRequest("provider", qt.SummaryColumn{ID: "responses", Op: "count"})
	counted, _, err := s.executeQueryV3(t.Context(), "usage", nil, PharosUsageSchema(), counts)
	if err != nil || len(counted.Rows) == 0 {
		t.Fatal("count-only groups", counted, err)
	}
	for _, row := range counted.Rows {
		if _, present := row["is_estimate"]; present {
			t.Fatal("count-only groups read estimate provenance", row)
		}
	}
	requests, _, err := s.executeQueryV3(t.Context(), "model_requests", nil, PharosModelRequestsSchema(), qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Rows: true, Result: qt.WireQuery{Select: []string{"id", "output_tokens"}, Limit: 50}})
	if err != nil || requests.Total != 0 || len(requests.Rows) != 0 {
		t.Fatal("estimated responses became request counts", requests, err)
	}
	// An unrelated derived-table update must leave predictions current.
	execTx(t, c.DB, "UPDATE meta SET value='unrelated-outcomes-update' WHERE key='tool_derivations_version'")
	if err := c.scheduleToolDerivations(); err != nil {
		t.Fatal(err)
	}
	status, err = c.UsageEstimationStatus(t.Context())
	if err != nil || integer(status["pending_conversations"]) != 0 || predicted() != 3 {
		t.Fatal("unrelated derivation erased estimates", status, err)
	}
	if err := c.ensureToolRollup(t.Context()); err != nil || predicted() != 3 {
		t.Fatal("Tools lost existing estimates", err)
	}
	t.Run("estimator update schedules every cleared conversation", func(t *testing.T) {
		override(t, &estimation.Assumptions, append(append([]byte{}, estimation.Assumptions...), '\n'))
		if err := c.scheduleToolDerivations(); err != nil {
			t.Fatal(err)
		}
		status, err := c.UsageEstimationStatus(t.Context())
		if err != nil || predicted() != 0 || integer(status["pending_conversations"]) != 2 {
			t.Fatal("cleared estimates were not pending", status, err)
		}
		if err := c.backfillUsageEstimation(t.Context()); err != nil || predicted() != 3 {
			t.Fatal("model-update backfill did not restore estimates", err)
		}
	})
	if err := c.scheduleToolDerivations(); err != nil {
		t.Fatal(err)
	}
	if err := c.ensureToolRollup(t.Context()); err != nil || predicted() != 3 {
		t.Fatal(err)
	}
	// Companion metric metadata uses the same filter and public formula API.
	metrics := metricRequest(qt.AggSpec{ID: "tokens", Op: "sum", Field: "total_tokens"}, qt.AggSpec{ID: "estimated", Expression: "MAX(IF([is_estimate],1,0))"})
	metricResult, _, err := s.executeQueryV3(t.Context(), "usage", nil, PharosUsageSchema(), qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Result: qt.WireQuery{Limit: 50}, Metrics: metrics.Metrics})
	if err != nil || len(metricResult.Metrics) != 2 || len(metricResult.Metrics[1].Buckets) != 1 || integer(metricResult.Metrics[1].Buckets[0].Value) != 1 {
		t.Fatal(metricResult, err)
	}
	execTx(t, c.DB, "UPDATE messages SET selected=selected,raw_text=raw_text")
	if status, err := c.UsageEstimationStatus(t.Context()); err != nil || integer(status["pending_conversations"]) != 0 {
		t.Fatal(status, err)
	}
	if err := c.backfillUsageEstimation(t.Context()); err != nil || predicted() != 3 {
		t.Fatal("not idempotent", err)
	}
	first := firstString(queryRows(t, c, "SELECT id FROM conversations WHERE native_id='conv-1'")[0]["id"])
	var revision int64
	if err := c.DB.QueryRow("SELECT revision FROM usage_estimation_inputs WHERE conversation_id=?", first).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	key, _ := c.usageEstimationKey(t.Context())
	if err := c.storeUsageEstimationConversation(t.Context(), first, revision, key+":old", &conversationUsageEstimates{}); err != nil || predicted() != 3 {
		t.Fatal("stale publication", err)
	}

	// A known model uses ordinary historical pricing for its output. No missing
	// input is converted into a measured zero or silently included in full cost.
	execTx(t, c.DB, "UPDATE messages SET model='claude-sonnet-4-20250514'")
	if err := c.backfillUsageEstimation(t.Context()); err != nil {
		t.Fatal(err)
	}
	pricedRows, err := c.usageRows(time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range pricedRows {
		if row["cost_usd"] == nil || row["price_status"] != "partial" || row["input_tokens"] != nil {
			t.Fatal("estimated output pricing", row)
		}
	}
	// Shared totals expose provenance and partial output pricing by window.
	summary, err := c.UsageSummary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, window := range summary["windows"].([]*usageSummaryWindow) {
		if window.Key == "all" && (!window.IsEstimate || !window.CostIsEstimate || window.InputTokensAvailable || window.PriceStatus != "partial") {
			t.Fatal("unflagged Home totals", window)
		}
		if window.Key == "1h" && window.IsEstimate {
			t.Fatal("outside-window estimates leaked into headline flags")
		}
	}
	ledger, err := queryMapsContext(t.Context(), c.reader(), `SELECT u.*,a.model session_model FROM agent_session_usage u JOIN agent_sessions a ON a.id=u.agent_session_id`)
	if err != nil {
		t.Fatal(err)
	}
	book, err := c.loadPriceBookFrom(t.Context(), c.reader())
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]statMetricValue{}
	usageMetrics(values, ledger, book)
	if !values["total_tokens"].IsEstimate || values["total_tokens"].Status != "partial" || values["input_tokens"].Value != nil || !values["cost_usd"].IsEstimate || values["cost_usd"].PriceStatus != "partial" {
		t.Fatal("MCP presented estimates as measurements", values)
	}
	evidence := statisticsMetricEvidence(statisticsRow{ID: "estimate", Metrics: values}, statisticsRequest{Metrics: []string{"total_tokens", "cost_usd"}, Quality: "available"})
	if !estimateFlag(evidence["total_tokens"].(map[string]any)["is_estimate"]) {
		t.Fatal(evidence)
	}
	recent, err := c.recentConversations(t.Context(), false, false)
	if err != nil || len(recent) == 0 {
		t.Fatal(recent, err)
	}
	for _, item := range recent {
		if item.Tokens != nil && (!item.IsEstimate || item.PriceStatus != "partial") {
			t.Fatal("unflagged recent conversation", item)
		}
	}
	// The reader consumes the stored prediction through its ordinary reports.
	conversation := queryRows(t, c, "SELECT id,workspace_id FROM conversations WHERE native_id='conv-1'")[0]
	read, err := c.WorkConversation(firstString(conversation["workspace_id"]), firstString(conversation["id"]))
	if err != nil || !estimateFlag(read["token_usage_is_estimate"]) {
		t.Fatal(read, err)
	}
	found := false
	for _, message := range read["messages"].([]map[string]any) {
		found = found || estimateFlag(message["usage_is_estimate"])
	}
	if !found {
		t.Fatal("reader omitted canonical usage")
	}
	// Replaying a native ledger must retain current predictions when it still
	// has no measured usage or tool completion clock.
	if err := c.writeTransaction(t.Context(), "test native replay", func(tx *sql.Tx) error { return rebuildStoredToolLedger(t.Context(), tx, first) }); err != nil {
		t.Fatal(err)
	}
	if predicted() != 3 || countRows(t, c, "SELECT COUNT(*) FROM tool_calls WHERE duration_is_estimate=1") != 1 {
		t.Fatal("native replay lost prediction provenance")
	}
	if err := c.refreshUsageEstimationCalibration(t.Context()); err != nil {
		t.Fatal(err)
	}
	calibration, err := c.loadEstimationCalibration(t.Context())
	if err != nil || calibration.Candidates != 0 {
		t.Fatal("estimates became training labels", calibration, err)
	}
	// Any authoritative report, including zero, removes predictions for the
	// entire conversation in the same write transaction.
	execTx(t, c.DB, "INSERT INTO model_requests(id,conversation_id,native_id,sequence,input_tokens,output_tokens,total_tokens) VALUES('actual',"+sqlQuote(first)+",'real-zero',99,0,0,0)")
	if predicted() != 1 {
		t.Fatal("real zero did not replace all conversation estimates", predicted())
	}
	if err := c.backfillUsageEstimation(t.Context()); err != nil || predicted() != 1 {
		t.Fatal("backfill replaced real zero", err)
	}
	if countRows(t, c, "SELECT COUNT(*) FROM model_requests WHERE id='actual' AND is_estimate=0 AND total_tokens=0") != 1 {
		t.Fatal("actual zero was mutated")
	}
	// Mixed aggregates carry the flag; a measured-only filter clears it.
	session := firstString(queryRows(t, c, "SELECT id FROM agent_sessions WHERE conversation_id="+sqlQuote(first)+" AND native_id='main'")[0]["id"])
	execTx(t, c.DB, "INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,effort,attribution,output_tokens,total_tokens) VALUES("+sqlQuote(session)+",'2026-10-01T10:00:00Z','claude-sonnet-4-20250514','','request',10,10)")
	mixed, err := c.usageRows(time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	measured, estimatedCount := 0, 0
	for _, row := range mixed {
		if estimateFlag(row["is_estimate"]) {
			estimatedCount++
		} else {
			measured++
		}
	}
	if measured != 1 || estimatedCount != 1 {
		t.Fatal("mixed usage lost provenance", mixed)
	}
	q.Source.Where = []qt.WhereTerm{{Field: "is_estimate", Op: "=", Value: "false"}}
	onlyMeasured, _, err := s.executeQueryV3(t.Context(), "usage", nil, PharosUsageSchema(), q)
	if err != nil || len(onlyMeasured.Rows) != 1 || estimateFlag(onlyMeasured.Rows[0]["is_estimate"]) || integer(onlyMeasured.Rows[0]["tokens"]) != 10 {
		t.Fatal("measured-only aggregate marked estimated", onlyMeasured, err)
	}
	execTx(t, c.DB, `UPDATE messages SET raw_text=json_set(raw_text,'$.claude_ai_message.usage',json('{"output_tokens":0}')) WHERE native_id='n2'`)
	if predicted() != 0 {
		t.Fatal("source correction retained old prediction")
	}
	if err := c.backfillUsageEstimation(t.Context()); err != nil || predicted() != 0 {
		t.Fatal("raw reported zero was ignored", err)
	}
	if w := apiCall(t, s, config.APIToken, http.MethodGet, "/api/estimation/status", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"pending_conversations":0`) {
		t.Fatal(w.Code, w.Body.String())
	}
	t.Run("response dates survive unclocked anchors", func(t *testing.T) {
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprint("legacy=", legacy), func(t *testing.T) {
				catalog, cfg := testCatalog(t)
				var exports []map[string]any
				if err := json.Unmarshal([]byte(claudeAIExport), &exports); err != nil {
					t.Fatal(err)
				}
				// The fixture's first reply begins with an unclocked tool on a
				// later day than the conversation. Its own date must survive.
				message := mapValue(reasoningList(exports[0]["chat_messages"])[1])
				message["created_at"] = "2026-10-03T14:01:00Z"
				message["content"] = reasoningList(message["content"])[1:]
				path := filepath.Join(t.TempDir(), "conversations.json")
				writeSourceFile(t, path, jsonText(exports))
				ingestRecordTx(t, catalog, discoverClaudeAI(t, path)[0])
				if legacy {
					execTx(t, catalog.DB, `UPDATE messages SET raw_text=json_remove(raw_text,'$.claude_ai_message')`)
				}
				if err := catalog.backfillUsageEstimation(t.Context()); err != nil {
					t.Fatal(err)
				}
				// Existing installations must repair dates without reimporting.
				execTx(t, catalog.DB,
					`UPDATE model_requests SET requested_at=NULL WHERE native_id='m2' AND is_estimate=1`,
					`UPDATE meta SET value='chat-estimates-v3-provenance' WHERE key='usage_estimation_store_version'`)
				if err := catalog.ensureUsageEstimationStore(); err != nil {
					t.Fatal(err)
				}
				if status, err := catalog.UsageEstimationStatus(t.Context()); err != nil || integer(status["pending_conversations"]) == 0 {
					t.Fatal("old estimate dates were not queued for repair", status, err)
				}
				if err := catalog.backfillUsageEstimation(t.Context()); err != nil {
					t.Fatal(err)
				}
				request := queryRows(t, catalog, `SELECT r.requested_at,r.output_tokens,m.created_at anchor_at FROM model_requests r JOIN messages m ON m.id=r.source_message_id WHERE r.native_id='m2' AND r.is_estimate=1`)[0]
				at, valid := parseTime(firstString(request["requested_at"]))
				want, _ := parseTime("2026-10-03T14:01:00Z")
				if !valid || !at.Equal(want) || request["anchor_at"] != nil {
					t.Fatal("response date depends on its unclocked tool anchor", request)
				}
				q := groupRequest("provider", qt.SummaryColumn{ID: "tokens", Op: "sum", Field: "total_tokens"})
				q.Source.Where = []qt.WhereTerm{{Field: "first_usage_at", Op: ">=", Value: "2026-10-03T00:00:00Z"}, {Field: "first_usage_at", Op: "<", Value: "2026-10-04T00:00:00Z"}}
				result, _, err := testServer(t, cfg, catalog).executeQueryV3(t.Context(), "usage", nil, PharosUsageSchema(), q)
				if err != nil || len(result.Rows) != 1 || integer(result.Rows[0]["tokens"]) != integer(request["output_tokens"]) {
					t.Fatal("dated totals lost response output", result, err)
				}
				tool := queryRows(t, catalog, `SELECT started_at,ended_at FROM tool_calls WHERE call_id='tu1'`)[0]
				if tool["started_at"] != nil || tool["ended_at"] != nil {
					t.Fatal("accounting date fabricated a tool clock", tool)
				}
			})
		}
	})
	execTx(t, c.DB, "DELETE FROM conversations")
	if countRows(t, c, "SELECT COUNT(*) FROM usage_estimation_inputs") != 0 {
		t.Fatal("dangling backfill state")
	}
}

func TestUsageEstimationNativeClocksFloorsAndStableAnchors(t *testing.T) {
	manifest, err := bundledEstimation()
	if err != nil {
		t.Fatal(err)
	}
	_, profile, _ := manifest.resolve("", "", "standard")
	for _, duration := range []float64{.05, 901} {
		start := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
		builder := newUsageEstimationBuilder("claude-ai-export")
		builder.observe(map[string]any{"id": "clock", "native_id": "response:block:0", "role": "assistant", "kind": "reasoning", "source_order": 0, "raw_text": jsonText(map[string]any{"type": "thinking", "thinking_hidden": true, "start_timestamp": start.Format(time.RFC3339Nano), "stop_timestamp": start.Add(time.Duration(duration * float64(time.Second))).Format(time.RFC3339Nano)})})
		response := builder.estimate(manifest, estimationCalibration{}, "").Responses[0]
		if response.ClockedBlocks != 1 || response.MissingBlocks != 0 || math.Abs(response.ThinkingSeconds.Expected-duration) > 1e-6 || math.Abs(response.Thinking.Expected-duration*profile.ThinkingRate.Expected) > 1e-6 {
			t.Fatal("native clock replaced by missing-block prior", duration, response)
		}
	}
	builder := newUsageEstimationBuilder("claude-ai-export")
	thinking := strings.Repeat("thought ", 500)
	builder.observe(map[string]any{"id": "visible", "native_id": "visible:block:0", "role": "assistant", "kind": "reasoning", "source_order": 0, "raw_text": jsonText(map[string]any{"type": "thinking", "thinking_hidden": false, "thinking": thinking, "start_timestamp": "2026-11-11T10:00:00Z", "stop_timestamp": "2026-11-11T10:00:00.05Z"})})
	response := builder.estimate(manifest, estimationCalibration{}, "").Responses[0]
	if response.Thinking.Lower < float64(response.VisibleThinkingCharacters)*profile.VisibleThinkingRate.Lower || !response.Thinking.valid() {
		t.Fatal("visible text failed to set a floor", response)
	}
	rows := []map[string]any{
		{"id": "a-late", "native_id": "reply:block:2", "role": "assistant", "kind": "tool_call", "source_order": 2, "raw_text": `{"type":"tool_use","id":"same","name":"bash_tool","input":{"cmd":"echo hello"}}`},
		{"id": "z-first", "native_id": "reply:block:0", "role": "assistant", "kind": "message", "source_order": 0, "raw_text": `{"type":"text","text":"Hello"}`},
	}
	run := func(rows []map[string]any) usageResponseEstimate {
		b := newUsageEstimationBuilder("claude-ai-export")
		for _, row := range rows {
			b.observe(row)
		}
		return b.estimate(manifest, estimationCalibration{}, "").Responses[0]
	}
	before := run(rows)
	duplicate := maps.Clone(rows[0])
	duplicate["id"], duplicate["native_id"], duplicate["source_order"] = "duplicate", "reply:block:3", 3
	after := run([]map[string]any{rows[1], duplicate, rows[0]})
	if before.MessageID != "z-first" || after.MessageID != before.MessageID || before.Output != after.Output || len(after.Tools) != 1 {
		t.Fatal("ordering or repeated tool IDs changed response accounting", before, after)
	}
	for _, tc := range []struct{ name, envelope, block, want string }{
		{"native response date", "2026-11-11T10:00:00Z", "2026-11-12T11:00:00Z", "2026-11-11T10:00:00Z"},
		{"retained block date", "", "2026-11-12T11:00:00Z", "2026-11-12T11:00:00Z"},
		{"invalid envelope date", "unknown", "2026-11-12T11:00:00Z", "2026-11-12T11:00:00Z"},
		{"no response date", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := map[string]any{"id": "call", "native_id": "reply:block:0", "role": "assistant", "kind": "tool_call", "source_order": 0,
				"raw_text": jsonText(map[string]any{"type": "tool_use", "id": "tool", "name": "bash_tool", "claude_ai_message": map[string]any{"sender": "assistant", "created_at": tc.envelope}})}
			text := map[string]any{"id": "text", "native_id": "reply:block:1", "role": "assistant", "kind": "message", "source_order": 1, "created_at": tc.block, "raw_text": `{"type":"text","text":"Done"}`}
			for _, rows := range [][]map[string]any{{call, text}, {text, call}} {
				got := run(rows)
				if got.MessageID != "call" || got.UsageAt != tc.want || got.Window != nil || got.SecondsAvailable {
					t.Fatal("accounting date changed identity or invented generation clocks", got)
				}
			}
		})
	}
}
