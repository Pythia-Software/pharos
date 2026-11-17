package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
)

// teamsCodexTotalPrefix proves one deliberately narrow, all-history total for
// an independent header-backed thread. It does not prove per-date, per-model,
// child attribution or content eligibility, and is not wired to reporting.
// A gap, inherited base, non-integral counter or unrecognized checkpoint makes
// the entire unit unavailable instead of inventing an aggregate remainder.
func teamsCodexTotalPrefix(proof *teamsNativeProof, events []map[string]any) (int64, string, bool) {
	if proof == nil || proof.Status != "header_independent_unvalidated" {
		return 0, "", false
	}
	var previous, previousLast int64
	seen := false
	for _, event := range events {
		if firstString(event["type"]) != "event_msg" {
			continue
		}
		payload := mapValue(event["payload"])
		if firstString(payload["type"]) != "token_count" {
			continue
		}
		info := mapValue(payload["info"])
		if info == nil {
			if value, exists := payload["info"]; exists && value == nil {
				continue
			}
			return 0, "", false
		}
		total, okTotal := teamsSafeCounter(mapValue(info["total_token_usage"])["total_tokens"])
		last, okLast := teamsSafeCounter(mapValue(info["last_token_usage"])["total_tokens"])
		if !okTotal || !okLast || total < previous || last > total {
			return 0, "", false
		}
		if !seen && total != last {
			return 0, "", false
		}
		// Repeated checkpoints can replay the same last request. Otherwise
		// the next cumulative checkpoint must advance by precisely that request.
		if seen && ((total != previous && total-previous != last) || (total == previous && last != previousLast && last != 0)) {
			return 0, "", false
		}
		previous, previousLast, seen = total, last, true
	}
	if !seen {
		return 0, "", false
	}
	// This digest describes only this all-history measure. Other projections
	// need their own intrinsic profiles, never this digest as a universal proof.
	sum := sha256.Sum256([]byte(fmt.Sprintf("pharos-codex-independent-total-v1\x00%d", previous)))
	return previous, hex.EncodeToString(sum[:]), true
}

func teamsSafeCounter(value any) (int64, bool) {
	if number, ok := value.(json.Number); ok {
		n, err := number.Int64()
		return n, err == nil && n >= 0 && n <= 1<<53-1
	}
	n, ok := value.(float64)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 1<<53-1 || math.Trunc(n) != n {
		return 0, false
	}
	return int64(n), true
}
