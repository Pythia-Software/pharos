package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"time"

	"github.com/gbdubs/pharos/internal/querytable"
)

// People label observations preserve each dashboard's message population and
// attribution. Each message contributes once, under its highest-confidence
// most-specific accepted label, so label groups add up to the message total.
func (c *Catalog) peopleMessageLabelRows(ctx context.Context, fields libraryFields, terms ...[]querytable.WhereTerm) ([]map[string]any, error) {
	c.ensureAuthorship()
	if err := c.ensureOrigins(ctx); err != nil {
		return nil, err
	}
	scopes := []string{"human", "activity", "habits"}
	for _, where := range terms {
		for _, term := range where {
			if !term.IsGroup() && !term.Negated && term.Field == "people_scope" && term.Op == "=" {
				scopes = []string{term.Value}
			}
		}
	}
	// Read only consumed authorship fields, plus identities needed to join
	// labels and the dashboard's existing conversation/hour attribution.
	needed := []string{"id", "conversation_id", "workspace_id", "sent_at", "typed_words"}
	for field := range fields {
		if _, ok := writingMessageDataset.columns[field]; ok {
			needed = append(needed, field)
		}
	}
	if fields == nil {
		for field := range writingMessageDataset.columns {
			needed = append(needed, field)
		}
	}
	needed = slices.Compact(slices.Sorted(slices.Values(needed)))
	table := writingMessageDataset.project(needed)
	var where []querytable.WhereTerm
	for _, terms := range terms {
		where = append(where, terms...)
	}
	// Restrict timestamps before loading message facts. Other predicates stay
	// with Query Table, after the selected scope supplies its own metadata.
	times := []querytable.WhereTerm{}
	for _, term := range where {
		if term.IsGroup() {
			continue
		}
		if slices.Contains([]string{"sent_at", "event_at", "started_at"}, term.Field) {
			// These are candidate day bounds, with an extra day on each
			// side. That also covers Habits' hour-start placement; the
			// final predicate still compares its original timestamp.
			term.Field = "sent_at"
			times = append(times, term)
		}
	}
	source, args := table.windowedQuerySource(PharosWritingMessagesSchema(), times, c.clock())
	messages, err := queryMapsContext(ctx, c.reader(), source, args...)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, firstString(message["id"]))
	}
	if len(ids) == 0 {
		return []map[string]any{}, nil
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	labels, _ := c.sqlDatasetFor("message_labels")
	labelSQL := messageLabelSelect(labels, []string{"message_id", "label", "family", "label_status", "label_model", "labeled_at"})
	observations, err := queryMapsContext(ctx, c.reader(), "SELECT * FROM ("+labelSQL+") WHERE message_id IN (SELECT value FROM json_each(?))", string(encoded))
	if err != nil {
		return nil, err
	}
	probabilities := map[string]map[string]float32{}
	if c.hasLabelsLedger() {
		stored, err := queryMapsContext(ctx, c.reader(), `SELECT l.message_id,l.model,l.probs,lm.labels_json
 FROM message_labels l JOIN message_labels_models lm ON lm.model=l.model
 WHERE l.message_id IN (SELECT value FROM json_each(?))`, string(encoded))
		if err != nil {
			return nil, err
		}
		orders := map[string][]string{}
		for _, row := range stored {
			model := firstString(row["model"])
			order, ok := orders[model]
			if !ok {
				_ = json.Unmarshal([]byte(firstString(row["labels_json"])), &order)
				orders[model] = order
			}
			// queryMaps reads BLOBs as strings; retain the stored float32
			// scores rather than the rounded reader probabilities.
			raw, _ := row["probs"].([]byte)
			if value, ok := row["probs"].(string); ok {
				raw = []byte(value)
			}
			scores := map[string]float32{}
			for index, score := range decodeLabelProbs(raw) {
				if index < len(order) {
					scores[order[index]] = score
				}
			}
			probabilities[firstString(row["message_id"])] = scores
		}
	}
	byMessage := map[string]map[string]any{}
	for _, observation := range observations {
		id, label := firstString(observation["message_id"]), firstString(observation["label"])
		previous := byMessage[id]
		prior := firstString(previous["label"])
		score, priorScore := probabilities[id][label], probabilities[id][prior]
		// The accepted-label projection already removes ancestors. Equal
		// or unavailable scores use label ID order for a stable choice.
		if previous == nil || score > priorScore || score == priorScore && label < prior {
			byMessage[id] = observation
		}
	}
	contexts := map[string]map[string]map[string]any{"human": {}, "activity": {}, "habits": {}}
	if slices.Contains(scopes, "activity") {
		activity, err := c.conversationActivityRows(ctx)
		if err != nil {
			return nil, err
		}
		for _, event := range activity {
			if event["event_kind"] == "human" {
				contexts["activity"][firstString(event["message_id"])] = event
			}
		}
	}
	if slices.Contains(scopes, "habits") {
		rhythm, err := c.rhythm(ctx, c.habitsSettings(ctx).EngagedGap)
		if err != nil {
			return nil, err
		}
		hours := map[string]map[string]any{}
		for _, row := range rhythm.maps(nil, math.MinInt64, math.MaxInt64) {
			hours[firstString(row["id"])] = row
		}
		for _, source := range rhythm.messageSources {
			unit := rhythm.units[source.unit]
			clock := newLocalClock(time.UnixMilli(source.message.at).Truncate(time.Hour).UnixMilli())
			key := fmt.Sprintf("%s|%s|%s|%s|%s", clock.started, unit.actor, unit.initiator, unit.automation, unit.id)
			if row := hours[key]; row != nil {
				contexts["habits"][source.message.id] = row
			}
		}
	}
	rows := []map[string]any{}
	for _, message := range messages {
		id := firstString(message["id"])
		for _, scope := range scopes {
			context := contexts[scope][id]
			if scope != "human" && context == nil {
				continue
			}
			base := maps.Clone(message)
			maps.Copy(base, context)
			// Writing and activity use the actual message timestamp. Habits
			// keeps its existing hour placement and hour-level filters.
			for _, field := range []string{"sent_at", "event_at", "occurred_at"} {
				base[field] = message["sent_at"]
			}
			if scope != "habits" {
				base["started_at"] = message["sent_at"]
			}
			base["source_conversation_id"], base["source_workspace_id"] = message["conversation_id"], message["workspace_id"]
			base["message_id"], base["people_scope"] = id, scope
			base["typed_message"] = integer(message["typed_words"]) > 0
			observation := byMessage[id]
			if observation == nil {
				observation = map[string]any{"label": "?", "family": "?", "label_status": "unlabeled"}
			}
			maps.Copy(base, observation)
			base["id"] = scope + ":" + id + ":" + firstString(observation["label"])
			rows = append(rows, base)
		}
	}
	return rows, nil
}
