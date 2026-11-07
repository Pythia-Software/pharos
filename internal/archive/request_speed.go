package archive

import (
	"slices"
	"strings"
)

const (
	inferenceFast        = "fast"
	inferenceStandard    = "standard"
	speedFromField       = "speed"
	speedFromTier        = "service_tier"
	speedAssumedStandard = "assumed_standard"
)

// inferenceSpeed is the ingestion contract, shared by source adapters, retained
// messages, request reconstruction and queries. The default is a policy, so its
// provenance remains distinct from an explicitly reported standard speed.
type inferenceSpeed struct {
	Speed, ServiceTier, Source string
	tierRecorded               bool
	speedRecorded              bool // includes null/unknown resets, which have no speed source
}

// fastTiers are the service tiers that buy faster inference: OpenAI's
// priority processing (requested as "fast") and its Ultrafast Processing.
// Every other tier (default, auto, flex, scale, standard) is standard.
var fastTiers = []string{"priority", "fast", "ultrafast"}

// fastTier says whether tier, trimmed and lowercased, is a fast one.
func fastTier(tier string) bool {
	return slices.Contains(fastTiers, tier)
}

// ensureFastTiers marks for a ledger rebuild the conversations whose stored
// requests took their speed from a tier that fastTiers now judges otherwise,
// once per change to the list. The rebuild re-derives their messages and
// requests (BackfillToolLedger), and the timings follow from the requests.
// Only those conversations are rebuilt, not the whole ledger.
func (c *Catalog) ensureFastTiers() error {
	policy := strings.Join(fastTiers, ",")
	tiers := "'" + strings.Join(fastTiers, "','") + "'"
	_, err := c.DB.Exec(`UPDATE tool_ledger_state SET version='stale-fast-tiers'
 WHERE COALESCE((SELECT value FROM meta WHERE key='fast_service_tiers'),'')<>?1
 AND conversation_id IN (SELECT conversation_id FROM model_requests
  WHERE speed_source='`+speedFromTier+`' AND (speed='`+inferenceFast+`')<>(COALESCE(service_tier,'') IN (`+tiers+`)));
 INSERT INTO meta(key,value) VALUES('fast_service_tiers',?1) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, policy)
	return err
}

func standardInference() inferenceSpeed {
	return inferenceSpeed{Speed: inferenceStandard, Source: speedAssumedStandard}
}

// inferenceSetting inspects protocol/configuration objects only, never prose,
// tool inputs or outputs. Explicit speed (Claude) wins over the service tier.
// OpenAI priority is fast; every other or absent tier defaults to standard.
func inferenceSetting(value map[string]any) (inferenceSpeed, bool) {
	objects := []map[string]any{}
	for _, key := range []string{"message", "response"} {
		if nested := mapValue(value[key]); nested != nil {
			objects = append(objects, mapValue(nested["usage"]), mapValue(nested["metadata"]), nested)
		}
	}
	objects = append(objects, mapValue(value["usage"]), mapValue(value["metadata"]), value)
	result := standardInference()
	tierFound := false
	for _, object := range objects {
		for _, key := range []string{"service_tier", "serviceTier"} {
			if raw, exists := object[key]; exists {
				result.tierRecorded = true
				result.ServiceTier = strings.ToLower(strings.TrimSpace(firstString(raw)))
				if result.ServiceTier != "" {
					result.Source = speedFromTier
				}
				if fastTier(result.ServiceTier) {
					result.Speed = inferenceFast
				}
				tierFound = true
				break
			}
		}
		if tierFound {
			break
		}
	}
	for _, object := range objects {
		if raw, exists := object["speed"]; exists {
			result.speedRecorded = true
			switch strings.ToLower(strings.TrimSpace(firstString(raw))) {
			case inferenceFast:
				result.Speed, result.Source = inferenceFast, speedFromField
			case inferenceStandard:
				result.Speed, result.Source = inferenceStandard, speedFromField
			}
			return result, true
		}
	}
	return result, tierFound
}

// mergeInference updates independent pieces of evidence. Tier-only accounting
// cannot replace an explicit speed; a later speed setting or reset can. Keep
// field presence as well as provenance so resets survive streamed merges.
func mergeInference(previous, update inferenceSpeed) inferenceSpeed {
	if update.tierRecorded {
		previous.ServiceTier, previous.tierRecorded = update.ServiceTier, true
	}
	if update.speedRecorded {
		previous.Speed, previous.Source = update.Speed, update.Source
		previous.speedRecorded = true
	}
	if previous.Source != speedFromField {
		previous.Speed, previous.Source = inferenceStandard, speedAssumedStandard
		if previous.ServiceTier != "" {
			previous.Source = speedFromTier
		}
		if fastTier(previous.ServiceTier) {
			previous.Speed = inferenceFast
		}
	}
	return previous
}

func messageInference(message MessageRecord) (inferenceSpeed, bool) {
	// Typed adapter fields retain turn configuration even when the response's
	// accounting envelope omits it, and survive catalog recovery and replay.
	result, recorded := standardInference(), false
	if message.SpeedSource != "" || message.Speed != "" || message.ServiceTier != "" {
		result = inferenceSpeed{Speed: inferenceStandard, ServiceTier: strings.ToLower(strings.TrimSpace(message.ServiceTier)), Source: message.SpeedSource, tierRecorded: message.ServiceTier != ""}
		if strings.EqualFold(message.Speed, "fast") || (message.Speed == "" && fastTier(result.ServiceTier)) {
			result.Speed = inferenceFast
		}
		if message.SpeedSource == speedFromTier && result.ServiceTier != "" {
			// A speed taken from the tier follows the current policy, so a
			// replay of stored messages applies a change to fastTiers.
			result.Speed = inferenceStandard
			if fastTier(result.ServiceTier) {
				result.Speed = inferenceFast
			}
		}
		switch result.Source {
		case speedFromField, speedFromTier, speedAssumedStandard:
		default:
			if message.Speed != "" {
				result.Source = speedFromField
			} else {
				result.Source = speedFromTier
			}
		}
		result.speedRecorded = result.Source == speedFromField
		recorded = result.Source != speedAssumedStandard
	}
	var raw map[string]any
	if message.RawText != "" || message.Kind == "metadata" {
		_, raw = tokenObject(message)
	}
	if update, found := inferenceSetting(raw); found {
		return mergeInference(result, update), true
	}
	return result, recorded
}

func withInference(message MessageRecord, config inferenceSpeed) MessageRecord {
	message.Speed, message.ServiceTier, message.SpeedSource = config.Speed, config.ServiceTier, config.Source
	return message
}

func normalizeMessageInference(message MessageRecord) MessageRecord {
	config, _ := messageInference(message)
	return withInference(message, config)
}

func normalizeRecordInference(record WorkspaceRecord) WorkspaceRecord {
	for ci := range record.Conversations {
		for mi := range record.Conversations[ci].Messages {
			record.Conversations[ci].Messages[mi] = normalizeMessageInference(record.Conversations[ci].Messages[mi])
		}
	}
	return record
}

func speedLabel(speed string) string {
	if strings.EqualFold(speed, inferenceFast) {
		return "Fast"
	}
	return "Standard"
}
