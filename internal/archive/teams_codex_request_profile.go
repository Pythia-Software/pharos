package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// teamsCodexRequestFact is private local indexed evidence. Native request IDs
// are committed by a domain-separated digest and are never sent as values.
type teamsOptionalTokens struct {
	Known bool  `json:"known"`
	Value int64 `json:"value"`
}

type teamsCodexRequestFact struct {
	RequestSHA256 string              `json:"request_sha256"`
	Timestamp     string              `json:"timestamp"`
	Model         string              `json:"model"`
	Effort        string              `json:"effort"`
	Input         int64               `json:"input_tokens"`
	Cached        teamsOptionalTokens `json:"cached_input_tokens"`
	Output        int64               `json:"output_tokens"`
	Reasoning     teamsOptionalTokens `json:"reasoning_output_tokens"`
	Total         int64               `json:"total_tokens"`
}
type teamsCodexRequestOccurrence struct {
	NativeID, RawSHA256, Timestamp, Model, Effort string
}
type teamsCodexRequestProof struct {
	Facts       []teamsCodexRequestFact
	Occurrences []teamsCodexRequestOccurrence
	Digest      string
}

const teamsCodexMaxRequestFacts = 10000
const teamsMaxExactInteger = int64(1<<53 - 1)

// teamsCodexRequestProfile certifies only independent complete native streams.
// Counter-only, inherited, interrupted, ambiguous and unsupported records do
// not become request-level facts. It never derives cost or opens a file.
func teamsCodexRequestProfile(header *teamsNativeProof, events []map[string]any, initialEffort string) *teamsCodexRequestProof {
	if header == nil || header.Status != "header_independent_unvalidated" || header.TrailingPartial || header.InvalidUTF8 {
		return nil
	}
	p := &teamsCodexRequestProof{Facts: []teamsCodexRequestFact{}}
	seen := map[string]teamsCodexRequestFact{}
	occurrences := map[string]bool{}
	model, effort := "", initialEffort
	var sum int64
	cumulative := teamsCodexRequestFact{Cached: teamsOptionalTokens{Known: true}, Reasoning: teamsOptionalTokens{Known: true}}
	var last *teamsCodexRequestFact
	for _, event := range events {
		typ := firstString(event["type"])
		payload := mapValue(event["payload"])
		if payload == nil {
			payload = event
		}
		if typ == "session_meta" {
			id, ok := payload["id"].(string)
			if !ok || id == "" || id != header.NativeID || strings.TrimSpace(id) != id {
				return nil
			}
			if v, exists := payload["model"]; exists {
				model = teamsExactModel(v)
			}
			effort = updatedEffort(effort, payload)
			if !teamsCodexAttributionMatches(payload, model, effort) {
				return nil
			}
			continue
		}
		if typ == "turn_context" {
			model = teamsExactModel(payload["model"])
			effort = recordedEffort(payload)
			if !teamsCodexAttributionMatches(payload, model, effort) {
				return nil
			}
			continue
		}
		record := typ == "token_usage_record" || (typ == "event_msg" && firstString(payload["type"]) == "token_usage_record")
		counter := typ == "event_msg" && firstString(payload["type"]) == "token_count"
		if counter {
			info := mapValue(payload["info"])
			if info == nil && payload["info"] == nil {
				continue
			}
			if info == nil || last == nil {
				return nil
			}
			if !teamsCodexCountsMatch(mapValue(info["total_token_usage"]), cumulative) {
				return nil
			}
			if !teamsCodexCountsMatch(mapValue(info["last_token_usage"]), *last) {
				return nil
			}
			if raw, exists := info["latest_token_usage_record"]; exists && raw != nil {
				v := mapValue(raw)
				if v == nil {
					return nil
				}
				id, valid := v["response_id"].(string)
				if !valid || id == "" || teamsCodexRequestID(id) != last.RequestSHA256 || !teamsCodexCountsMatch(mapValue(v["usage"]), *last) || !teamsCodexCountsMatch(mapValue(v["thread_token_usage"]), cumulative) {
					return nil
				}
			}
			continue
		}
		if !record {
			if hasUsageEvidence(event) {
				return nil
			}
			continue
		}
		if !teamsCodexAttributionMatches(event, model, effort) || !teamsCodexAttributionMatches(payload, model, effort) {
			return nil
		}
		id, isString := payload["response_id"].(string)
		if !isString || id == "" || len(id) > 512 || strings.TrimSpace(id) != id || model == "" || len(model) > 256 || len(effort) > 128 {
			return nil
		}
		timestamp, validTimestamp := event["timestamp"].(string)
		if !validTimestamp || strings.TrimSpace(timestamp) != timestamp {
			return nil
		}
		stamp, err := time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return nil
		}
		stamp = stamp.UTC()
		if last != nil {
			prev, _ := time.Parse(time.RFC3339Nano, last.Timestamp)
			if stamp.Before(prev) {
				return nil
			}
		}
		u := mapValue(payload["usage"])
		if u == nil {
			return nil
		}
		input, a := teamsSafeJSONInt(u["input_tokens"])
		cached, b := teamsOptionalJSONInt(u, "cached_input_tokens")
		output, c := teamsSafeJSONInt(u["output_tokens"])
		reasoning, d := teamsOptionalJSONInt(u, "reasoning_output_tokens")
		total, e := teamsSafeJSONInt(u["total_tokens"])
		if !a || !b || !c || !d || !e || total == 0 || input > teamsMaxExactInteger-output || input+output != total || (cached.Known && cached.Value > input) || (reasoning.Known && reasoning.Value > output) {
			return nil
		}
		for k := range u {
			switch k {
			case "input_tokens", "cached_input_tokens", "output_tokens", "reasoning_output_tokens", "total_tokens":
			default:
				return nil
			}
		}
		fact := teamsCodexRequestFact{teamsCodexRequestID(id), stamp.Format(time.RFC3339Nano), model, effort, input, cached, output, reasoning, total}
		prev, duplicate := seen[id]
		if duplicate && (prev != fact || last == nil || *last != fact) {
			return nil
		}
		if !duplicate {
			if len(p.Facts) >= teamsCodexMaxRequestFacts || sum > teamsMaxExactInteger-total {
				return nil
			}
			sum += total
			for _, pair := range []struct {
				dst   *int64
				value int64
			}{{&cumulative.Input, input}, {&cumulative.Output, output}} {
				if *pair.dst > teamsMaxExactInteger-pair.value {
					return nil
				}
				*pair.dst += pair.value
			}
			for _, pair := range []struct {
				dst   *teamsOptionalTokens
				value teamsOptionalTokens
			}{{&cumulative.Cached, cached}, {&cumulative.Reasoning, reasoning}} {
				if !pair.value.Known {
					pair.dst.Known = false
					continue
				}
				if pair.dst.Value > teamsMaxExactInteger-pair.value.Value {
					return nil
				}
				pair.dst.Value += pair.value.Value
			}
			cumulative.Total = sum
			p.Facts = append(p.Facts, fact)
			seen[id] = fact
			last = &p.Facts[len(p.Facts)-1]
		}
		if !teamsCodexCountsMatch(mapValue(payload["thread_token_usage"]), cumulative) {
			return nil
		}
		if len(p.Occurrences) >= teamsCodexMaxRequestFacts {
			return nil
		}
		native := firstString(payload["id"], event["id"])
		if native == "" {
			native = fmt.Sprintf("line-%d", integer(event["_line"]))
		}
		if occurrences[native] {
			return nil
		}
		occurrences[native] = true
		raw := accountingRaw(event)
		if raw == "" {
			return nil
		}
		hash := sha256.Sum256([]byte(raw))
		p.Occurrences = append(p.Occurrences, teamsCodexRequestOccurrence{native, hex.EncodeToString(hash[:]), fact.Timestamp, model, effort})
	}
	if len(p.Facts) == 0 {
		return nil
	}
	raw, err := json.Marshal(p.Facts)
	if err != nil {
		return nil
	}
	sumHash := sha256.Sum256(append([]byte("pharos-codex-request-usage-v1\x00"), raw...))
	p.Digest = hex.EncodeToString(sumHash[:])
	return p
}
func teamsCodexRequestID(id string) string {
	h := sha256.Sum256(append([]byte("pharos-codex-request-id-v1\x00"), []byte(id)...))
	return hex.EncodeToString(h[:])
}
func teamsSafeJSONInt(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, e := n.Int64()
	return i, e == nil && i >= 0 && i <= teamsMaxExactInteger
}
func teamsOptionalJSONInt(m map[string]any, key string) (teamsOptionalTokens, bool) {
	value, exists := m[key]
	if !exists || value == nil {
		return teamsOptionalTokens{}, true
	}
	n, ok := teamsSafeJSONInt(value)
	return teamsOptionalTokens{Known: ok, Value: n}, ok
}
func teamsCodexCountsMatch(m map[string]any, f teamsCodexRequestFact) bool {
	if m == nil {
		return false
	}
	expected := map[string]teamsOptionalTokens{
		"input_tokens": {Known: true, Value: f.Input}, "cached_input_tokens": f.Cached,
		"output_tokens": {Known: true, Value: f.Output}, "reasoning_output_tokens": f.Reasoning,
		"total_tokens": {Known: true, Value: f.Total},
	}
	total, ok := teamsSafeJSONInt(m["total_tokens"])
	if !ok || total != f.Total {
		return false
	}
	for k, v := range m {
		want, known := expected[k]
		if !known || !want.Known {
			return false
		}
		got, ok := teamsSafeJSONInt(v)
		if !ok || got != want.Value {
			return false
		}
	}
	return true
}

// Unlike the tolerant display parser, proof identifiers and model names must
// be genuine unmodified strings, never numeric/string coercions.
func teamsExactModel(value any) string {
	s, ok := value.(string)
	if !ok || s == "" || strings.TrimSpace(s) != s {
		return ""
	}
	return s
}

// Reject embedded attribution that disagrees with the explicit preceding
// native context; the tolerant indexer's fallback is not independent proof.
func teamsCodexAttributionMatches(v map[string]any, model, effort string) bool {
	if raw, exists := v["model"]; exists && teamsExactModel(raw) != model {
		return false
	}
	keys := []string{"effort", "reasoning_effort", "reasoningEffort", "effortLevel", "effort_level", "model_reasoning_effort"}
	for _, key := range keys {
		if raw, exists := v[key]; exists {
			if raw == nil {
				if effort != "" {
					return false
				}
				continue
			}
			text, ok := raw.(string)
			if !ok || strings.TrimSpace(text) != text || normalizeEffort(text) != effort {
				return false
			}
		}
	}
	for _, pair := range []struct{ object, key string }{{"reasoning", "effort"}, {"output_config", "effort"}, {"outputConfig", "effort"}, {"settings", "effortLevel"}} {
		nested := mapValue(v[pair.object])
		if nested == nil {
			continue
		}
		raw, exists := nested[pair.key]
		if !exists {
			continue
		}
		if raw == nil {
			if effort != "" {
				return false
			}
			continue
		}
		text, ok := raw.(string)
		if !ok || strings.TrimSpace(text) != text || normalizeEffort(text) != effort {
			return false
		}
	}
	return true
}
