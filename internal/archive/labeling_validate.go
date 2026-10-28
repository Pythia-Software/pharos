package archive

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Item outcomes of the publication contract. Only usable items (with or
// without evidence issues) are training rows; uncertain labels are never
// accepted labels.
const (
	itemUsable         = "usable"
	itemEvidenceIssues = "usable_with_evidence_issues"
	itemInvalid        = "invalid"
	itemMissing        = "missing"
	itemRefused        = "refused"
	// itemHeld: answered by a model other than the requested one; kept
	// for an operator decision, never published by default.
	itemHeld = "held"
)

// labelingTargetPiece is a target piece shown in the unit's visible text.
type labelingTargetPiece struct {
	start, end               int // original message bytes
	visibleStart, visibleEnd int
	transform                string
}

// labelingItemSource is what validation needs to know about one item.
type labelingItemSource struct {
	id, unitID, text, message, context string
	visibleStart                       int
	markers                            [][2]int // unit visible text
	targets                            []labelingTargetPiece
}

// labelingEvidence is one evidence entry and where its quote occurs.
type labelingEvidence struct {
	Label  string `json:"label"`
	Quote  string `json:"quote"`
	Status string `json:"status"`
	// Occurrences are the valid places of the quote: item-relative visible
	// byte ranges and the original message bytes they came from.
	Occurrences []labelingOccurrence `json:"occurrences,omitempty"`
	Ambiguous   bool                 `json:"ambiguous,omitempty"`
}

type labelingOccurrence struct {
	ItemStart int              `json:"item_start"`
	ItemEnd   int              `json:"item_end"`
	Source    []map[string]any `json:"source"`
}

// labelingItemResult is one item's validated annotation.
type labelingItemResult struct {
	ItemID      string             `json:"id"`
	Outcome     string             `json:"outcome"`
	Labels      []string           `json:"labels"`
	Uncertain   []string           `json:"uncertain_labels"`
	NeedContext *bool              `json:"need_context"`
	Evidence    []labelingEvidence `json:"evidence"`
	Issues      []string           `json:"issues"`
	Annotation  json.RawMessage    `json:"annotation,omitempty"`
}

// validateLabelingOutput checks a structured response against the request's
// items: exact id set, fields and types, label enums, accepted/uncertain
// overlap, and extractive evidence mapped to original bytes. It returns one
// result per expected item, in order, and the request-level problems.
func validateLabelingOutput(output json.RawMessage, items []labelingItemSource, labels map[string]bool) ([]labelingItemResult, []string) {
	results := make([]labelingItemResult, len(items))
	index := map[string]int{}
	for i, item := range items {
		index[item.id] = i
		results[i] = labelingItemResult{ItemID: item.id, Outcome: itemMissing, Labels: []string{}, Uncertain: []string{}, Evidence: []labelingEvidence{}, Issues: []string{}}
	}
	invalidAll := func(problem string) ([]labelingItemResult, []string) {
		for i := range results {
			results[i].Outcome, results[i].Issues = itemInvalid, []string{problem}
		}
		return results, []string{problem}
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(output, &top); err != nil || top == nil {
		return invalidAll("output_not_an_object")
	}
	problems := []string{}
	for key := range top {
		if key != "annotations" {
			problems = append(problems, "unexpected_top_level_field:"+key)
		}
	}
	var annotations []json.RawMessage
	if err := json.Unmarshal(top["annotations"], &annotations); err != nil || top["annotations"] == nil {
		return invalidAll("annotations_not_an_array")
	}
	seen := map[string]int{}
	for position, raw := range annotations {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			problems = append(problems, fmt.Sprintf("annotation_%d_not_an_object", position))
			continue
		}
		var id string
		if json.Unmarshal(fields["id"], &id) != nil || fields["id"] == nil {
			problems = append(problems, fmt.Sprintf("annotation_%d_without_id", position))
			continue
		}
		i, expected := index[id]
		if !expected {
			problems = append(problems, "extra_id:"+id)
			continue
		}
		seen[id]++
		if seen[id] > 1 {
			problems = append(problems, "duplicate_id:"+id)
			results[i].Outcome = itemInvalid
			results[i].Issues = appendUnique(results[i].Issues, "duplicate_id")
			continue
		}
		results[i] = validateLabelingItem(items[i], fields, raw, labels)
	}
	// An id set that is not exactly the request's (an id missing, extra,
	// repeated or absent) suggests annotations may be misassigned: no item
	// of the request is trusted. Missing items stay missing.
	mismatch := len(problems) > 0
	for _, result := range results {
		mismatch = mismatch || result.Outcome == itemMissing
	}
	if mismatch {
		for i := range results {
			if results[i].Outcome != itemMissing {
				results[i].Outcome = itemInvalid
			}
			results[i].Issues = appendUnique(results[i].Issues, "request_id_set_mismatch")
		}
	}
	return results, problems
}

func appendUnique(values []string, value string) []string {
	if containsString(values, value) {
		return values
	}
	return append(values, value)
}

func validateLabelingItem(item labelingItemSource, fields map[string]json.RawMessage, raw json.RawMessage, labels map[string]bool) labelingItemResult {
	result := labelingItemResult{ItemID: item.id, Labels: []string{}, Uncertain: []string{}, Evidence: []labelingEvidence{}, Issues: []string{},
		Annotation: append(json.RawMessage(nil), raw...)}
	invalid := false
	fail := func(issue string) {
		invalid = true
		result.Issues = appendUnique(result.Issues, issue)
	}
	for _, key := range []string{"id", "labels", "uncertain_labels", "evidence", "need_context"} {
		if _, ok := fields[key]; !ok {
			fail("missing_field:" + key)
		}
	}
	for key := range fields {
		switch key {
		case "id", "labels", "uncertain_labels", "evidence", "need_context":
		default:
			fail("unexpected_field:" + key)
		}
	}
	readLabels := func(key string) []string {
		if fields[key] == nil {
			return []string{}
		}
		var values []string
		if json.Unmarshal(fields[key], &values) != nil || values == nil {
			fail("field_type:" + key)
			return []string{}
		}
		for _, label := range values {
			if !labels[label] {
				fail("unknown_label:" + label)
			}
		}
		return values
	}
	result.Labels, result.Uncertain = readLabels("labels"), readLabels("uncertain_labels")
	if fields["need_context"] != nil {
		var need bool
		if json.Unmarshal(fields["need_context"], &need) != nil || bytes.Equal(bytes.TrimSpace(fields["need_context"]), []byte("null")) {
			fail("field_type:need_context")
		} else {
			result.NeedContext = &need
		}
	}
	accepted := map[string]bool{}
	for _, label := range result.Labels {
		if accepted[label] {
			result.Issues = appendUnique(result.Issues, "repeated_label:"+label)
		}
		accepted[label] = true
	}
	for _, label := range result.Uncertain {
		if accepted[label] {
			fail("accepted_and_uncertain:" + label)
		}
	}
	evidenceIssue := false
	if fields["evidence"] != nil {
		var entries []map[string]json.RawMessage
		if json.Unmarshal(fields["evidence"], &entries) != nil || entries == nil {
			fail("field_type:evidence")
		}
		for _, entry := range entries {
			var label, quote string
			if json.Unmarshal(entry["label"], &label) != nil || json.Unmarshal(entry["quote"], &quote) != nil || len(entry) != 2 {
				fail("evidence_shape")
				continue
			}
			if !labels[label] {
				fail("unknown_label:" + label)
				continue
			}
			evidence := locateLabelingQuote(item, label, quote)
			if !accepted[label] && !containsString(result.Uncertain, label) {
				evidence.Status = "label_not_returned"
			}
			if evidence.Status != "valid" {
				evidenceIssue = true
				result.Issues = appendUnique(result.Issues, "evidence_"+evidence.Status+":"+label)
			}
			result.Evidence = append(result.Evidence, evidence)
		}
	}
	for _, label := range result.Labels {
		supported := false
		for _, evidence := range result.Evidence {
			supported = supported || evidence.Label == label && evidence.Status == "valid"
		}
		if !supported {
			evidenceIssue = true
			result.Issues = appendUnique(result.Issues, "label_without_valid_evidence:"+label)
		}
	}
	sort.Strings(result.Issues)
	switch {
	case invalid:
		result.Outcome = itemInvalid
	case evidenceIssue || len(result.Issues) > 0:
		result.Outcome = itemEvidenceIssues
	default:
		result.Outcome = itemUsable
	}
	return result
}

// locateLabelingQuote finds every occurrence of quote in the item's
// model-visible text. An occurrence is valid evidence only inside target
// pieces, outside every marker, and when it maps to the same bytes, on
// character boundaries, in the original message. A transformed piece
// (json-string-v1) maps to its whole encoded range.
func locateLabelingQuote(item labelingItemSource, label, quote string) labelingEvidence {
	evidence := labelingEvidence{Label: label, Quote: quote, Occurrences: []labelingOccurrence{}}
	if strings.TrimSpace(quote) == "" {
		evidence.Status = "empty_quote"
		return evidence
	}
	reasons := map[string]bool{}
	for offset := 0; offset+len(quote) <= len(item.text); {
		at := strings.Index(item.text[offset:], quote)
		if at < 0 {
			break
		}
		start := offset + at
		end := start + len(quote)
		if source, reason := mapLabelingRange(item, start, end); reason == "" {
			evidence.Occurrences = append(evidence.Occurrences, labelingOccurrence{ItemStart: start, ItemEnd: end, Source: source})
		} else {
			reasons[reason] = true
		}
		_, size := utf8.DecodeRuneInString(item.text[start:])
		offset = start + max(size, 1)
	}
	switch {
	case len(evidence.Occurrences) > 0:
		evidence.Status = "valid"
		evidence.Ambiguous = len(evidence.Occurrences) > 1
	case reasons["in_marker"]:
		evidence.Status = "in_marker"
	case len(reasons) > 0:
		evidence.Status = firstKey(reasons)
	case strings.Contains(item.context, quote):
		evidence.Status = "in_context"
	default:
		evidence.Status = "not_found"
	}
	return evidence
}

func firstKey(set map[string]bool) string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys[0]
}

// mapLabelingRange maps item-relative visible bytes [start,end) to original
// message ranges, or names why it cannot be evidence.
func mapLabelingRange(item labelingItemSource, start, end int) ([]map[string]any, string) {
	start, end = start+item.visibleStart, end+item.visibleStart
	for _, marker := range item.markers {
		if start < marker[1] && marker[0] < end {
			return nil, "in_marker"
		}
	}
	ranges := []map[string]any{}
	at := start
	for _, piece := range item.targets {
		if piece.visibleEnd <= at || piece.visibleStart >= end {
			continue
		}
		if piece.visibleStart > at {
			return nil, "outside_target"
		}
		segmentEnd := min(end, piece.visibleEnd)
		if piece.transform != "" {
			ranges = append(ranges, map[string]any{"start": piece.start, "end": piece.end, "transform": piece.transform})
		} else {
			from, to := piece.start+at-piece.visibleStart, piece.start+segmentEnd-piece.visibleStart
			if from < 0 || to > len(item.message) || item.message[from:to] != item.text[at-item.visibleStart:segmentEnd-item.visibleStart] {
				return nil, "source_mismatch"
			}
			if !utf8.ValidString(item.message[from:to]) || !utf8.RuneStart(item.message[from]) || to < len(item.message) && !utf8.RuneStart(item.message[to]) {
				return nil, "not_on_character_boundary"
			}
			ranges = append(ranges, map[string]any{"start": from, "end": to})
		}
		at = segmentEnd
		if at >= end {
			return ranges, ""
		}
	}
	return nil, "outside_target"
}
