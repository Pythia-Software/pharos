package archive

import (
	"bytes"
	"encoding/json"

	"github.com/gbdubs/pharos/internal/teams"
)

// teamsNativeProof is only evidence of a local parser observation. It is not
// an account attestation, consent, ownership assignment or content authority.
type teamsNativeProof struct {
	Provider        string
	NativeID        string
	Status          string
	TrailingPartial bool
	TotalTokens     *int64
	TotalDigest     string
}
type teamsJSONLQuality struct{ gap, trailingPartial bool }

func (q *teamsJSONLQuality) observe(line []byte, terminated bool) {
	b := bytes.TrimSpace(line)
	if len(b) == 0 {
		return
	}
	var object map[string]json.RawMessage
	valid := teams.ValidUniqueJSON(b) && json.Unmarshal(b, &object) == nil && object != nil
	if valid {
		return
	}
	if terminated {
		q.gap = true
	} else {
		q.trailingPartial = true
	}
}

// teamsCodexNativeProof admits only a header-backed independent thread. Forks
// with inherited counters need a separately audited base proof; filename and
// synthetic rewrites can never establish identity. A torn final JSON line is
// outside the indexed prefix, whereas a malformed completed line is a gap.
func teamsCodexNativeProof(events []map[string]any, indexedID string, quality teamsJSONLQuality) *teamsNativeProof {
	proof := &teamsNativeProof{Provider: "codex", NativeID: indexedID, Status: "header_missing", TrailingPartial: quality.trailingPartial}
	if quality.gap {
		proof.Status = "capture_gap"
		return proof
	}
	if len(events) == 0 || firstString(events[0]["type"]) != "session_meta" {
		return proof
	}
	headerID, lineage := "", ""
	for _, event := range events {
		if firstString(event["type"]) != "session_meta" {
			continue
		}
		meta := mapValue(event["payload"])
		if meta == nil {
			proof.Status = "header_invalid"
			return proof
		}
		id := firstString(meta["id"])
		if id == "" || len(id) > 512 {
			proof.Status = "header_invalid"
			return proof
		}
		spawn := mapValue(mapValue(mapValue(meta["source"])["subagent"])["thread_spawn"])
		parent := firstString(meta["parent_thread_id"], spawn["parent_thread_id"])
		current := jsonText([]any{parent, meta["forked_from_id"], meta["forked_from_ordinal_exclusive"], meta["subagent_history_start_ordinal"], meta["history_base"]})
		if headerID != "" && (headerID != id || lineage != current) {
			proof.Status = "header_conflict"
			return proof
		}
		headerID, lineage = id, current
		if continuesHistory(meta) || teamsCodexUnprovedLineage(meta) {
			proof.Status = "inherited_unproved"
			return proof
		}
	}
	if headerID == "" {
		return proof
	}
	if headerID != indexedID {
		proof.Status = "native_id_rewritten"
		return proof
	}
	proof.Status = "header_independent_unvalidated"
	return proof
}

// An unexpected representation is not evidence that inherited history is
// absent. The ordinary local parser remains tolerant; federated accounting
// must instead withhold this observation until its lineage is understood.
func teamsCodexUnprovedLineage(meta map[string]any) bool {
	if value, ok := meta["forked_from_id"]; ok && value != nil {
		id, valid := value.(string)
		if !valid || id != "" {
			return true
		}
	}
	if value, ok := meta["history_base"]; ok && value != nil {
		return true
	}
	for _, key := range []string{"forked_from_ordinal_exclusive", "subagent_history_start_ordinal"} {
		if value, ok := meta[key]; ok && value != nil {
			n, valid := teamsSafeCounter(value)
			if !valid || n != 0 {
				return true
			}
		}
	}
	return false
}
