package archive

import (
	"context"
	"database/sql"
	"strings"
)

const agentSessionModelVersion = "2"
const multipleSessionModels = "Multiple models"

// Summary labels never canonicalize aliases: two reported identities (including
// context variants) remain two identities. The original strings stay on the
// messages, requests and usage rows, where filtering and pricing can use them.
type sessionModels map[string]bool

func (m sessionModels) add(model string) {
	if strings.TrimSpace(model) != "" && !placeholderModel(strings.TrimSpace(model)) {
		m[model] = true
	}
}

func (m sessionModels) summary() string {
	if len(m) > 1 {
		return multipleSessionModels
	}
	for model := range m {
		return model
	}
	return ""
}

func executionModelSummary(requests, usage sessionModels) string {
	// Retained requests can cover only part of an execution. Positive usage
	// recording several real identities must not become a single-model label.
	if len(requests) > 1 || len(usage) > 1 {
		return multipleSessionModels
	}
	if model := requests.summary(); model != "" {
		return model
	}
	return usage.summary()
}

// Only protocol model fields prove an executing model. MessageRecord.Model can
// be inherited parser state, and the launch envelope belongs to the parent.
// Tool output and user prose are never inspected for model claims.
func ownRequestModel(message MessageRecord) string {
	if message.Kind == "delegation_result" || message.Kind == "tool_result" || message.Role == "user" {
		return ""
	}
	// Native adapters put the assistant's answer in Text. JSON in an answer
	// is still prose, even when it resembles a response envelope. Metadata
	// records may retain their protocol envelope directly in Text.
	if strings.TrimSpace(message.RawText) == "" && message.Kind != "metadata" {
		return ""
	}
	raw, value := tokenObject(message)
	typ := firstString(value["type"], raw["type"])
	if !matchesFold(typ, "assistant", "message_start", "message", "response", "response.completed", "response.created", "token_usage_record") && firstString(value["object"]) != "response" {
		return ""
	}
	return firstString(mapValueDefault(value["message"])["model"], mapValueDefault(value["response"])["model"], value["model"])
}

func launchConfiguredModel(message MessageRecord) string {
	return firstString(mapValueDefault(mapValueDefault(parseJSONValue(message.Text))["input"])["model"])
}

func summarySessionModel(summary agentSessionSummary, conversation ConversationRecord) string {
	// Empty ledger identities still use the summary as their pricing fallback.
	// Do not turn a metadata change into newly priced usage in that legacy case.
	for bucket, counts := range summary.usage {
		if counts["total_tokens"] > 0 && strings.TrimSpace(bucket.model) == "" && strings.TrimSpace(conversation.Model) == "" {
			return conversation.Model
		}
	}
	requests, usage, reported := sessionModels{}, sessionModels{}, sessionModels{}
	for _, index := range summary.messageIndexes {
		message := conversation.Messages[index]
		if usageCopyOf(message) == "" {
			requests.add(ownRequestModel(message))
		}
	}
	for bucket, counts := range summary.usage {
		if counts["total_tokens"] > 0 {
			reported.add(bucket.model)
		}
		// Task result fallbacks can borrow the parent's launch model. They
		// are accounting policy, not observed execution metadata.
		if counts["total_tokens"] > 0 && (summary.nativeID == "main" || bucket.model != conversation.Model) {
			usage.add(bucket.model)
		}
	}
	if len(reported) > 1 {
		usage = reported
	}
	for _, message := range conversation.Messages {
		if message.Kind == "delegation_result" && message.CallID == summary.nativeID {
			usage.add(firstString(mapValueDefault(mapValueDefault(parseJSONValue(message.Text))["details"])["model"]))
		}
	}
	if model := executionModelSummary(requests, usage); model != "" {
		return model
	}
	if index := summary.delegationMessageIndex; index >= 0 {
		configured := sessionModels{}
		configured.add(launchConfiguredModel(conversation.Messages[index]))
		if model := configured.summary(); model != "" {
			return model
		}
	}
	if summary.nativeID == "main" {
		return conversation.Model // This transcript's own configuration.
	}
	return "Unknown model"
}

// SessionModelRepair describes stored rows, including mirrors; it does not
// count deduplicated executions. A repeat scan reports Updated=0.
type SessionModelRepair struct {
	Sessions        int `json:"sessions"`
	Updated         int `json:"updated"`
	Multiple        int `json:"multiple"`
	Unknown         int `json:"unknown"`
	PricingFallback int `json:"pricing_fallback"`
}

type sessionModelChange struct{ id, before, after string }

// Plan from retained membership and usage without re-running accounting. Even
// sessions whose original transcripts have disappeared can use their own
// positive usage buckets. Legacy blank-model buckets still price via the old
// session label, so those rows require a separate accounting migration.
func planSessionModels(ctx context.Context, q queryer, workspace string) ([]sessionModelChange, SessionModelRepair, error) {
	rows, err := queryMapsContext(ctx, q, `SELECT s.*,c.model conversation_model FROM agent_sessions s
		JOIN conversations c ON c.id=s.conversation_id WHERE (s.workspace_id=? AND s.kind='subagent') OR (s.native_id='main' AND s.conversation_id IN
		(SELECT child_conversation_id FROM agent_sessions WHERE workspace_id=? AND child_conversation_id IS NOT NULL))`, workspace, workspace)
	if err != nil {
		return nil, SessionModelRepair{}, err
	}
	return planSessionModelRows(ctx, q, rows, func(row map[string]any) bool {
		return row["workspace_id"] == workspace && row["kind"] == "subagent"
	})
}

func planSessionModelRows(ctx context.Context, q queryer, rows []map[string]any, target func(map[string]any) bool) ([]sessionModelChange, SessionModelRepair, error) {
	byID, mains := map[string]map[string]any{}, map[string]string{}
	requests, usage, configured := map[string]sessionModels{}, map[string]sessionModels{}, map[string]sessionModels{}
	reported := map[string]sessionModels{}
	blankPricing := map[string]bool{}
	ids := []string{}
	for _, row := range rows {
		id := firstString(row["id"])
		ids = append(ids, id)
		byID[id] = row
		requests[id], usage[id], configured[id] = sessionModels{}, sessionModels{}, sessionModels{}
		reported[id] = sessionModels{}
		if row["native_id"] == "main" {
			mains[firstString(row["conversation_id"])] = id
		}
	}
	encoded := jsonText(ids)
	models, err := queryMapsContext(ctx, q, `SELECT agent_session_id,model,attribution FROM agent_session_usage
		WHERE total_tokens>0 AND agent_session_id IN (SELECT value FROM json_each(?))`, encoded)
	if err != nil {
		return nil, SessionModelRepair{}, err
	}
	for _, row := range models {
		id, model := firstString(row["agent_session_id"]), firstString(row["model"])
		reported[id].add(model)
		// Seeded legacy rows and task fallbacks may simply echo the parent
		// label. They cannot prove which model the child executed.
		if byID[id]["native_id"] == "main" || model != firstString(byID[id]["conversation_model"]) {
			usage[id].add(model)
		}
		if strings.TrimSpace(model) == "" {
			blankPricing[id] = true
		}
	}
	for id, models := range reported {
		if len(models) > 1 {
			usage[id] = models
		}
	}
	// Membership owns the envelope, including sessions identified by stream
	// aliases. Do not join by conversation alone: that imports parent replies.
	messages, err := queryMapsContext(ctx, q, `SELECT am.agent_session_id,m.role,m.kind,m.text,m.raw_text FROM agent_session_messages am
		JOIN messages m ON m.id=am.message_id WHERE am.agent_session_id IN (SELECT value FROM json_each(?))
		AND (m.raw_text IS NOT NULL OR m.kind='metadata')`, encoded)
	if err != nil {
		return nil, SessionModelRepair{}, err
	}
	for _, row := range messages {
		m := storedMessageRecords([]map[string]any{row})[0]
		if usageCopyOf(m) == "" {
			requests[firstString(row["agent_session_id"])].add(ownRequestModel(m))
		}
	}
	// Request ownership can survive missing membership. Read the owning
	// envelope; model_requests.model itself can be a conversation fallback.
	modelRequests, err := queryMapsContext(ctx, q, `WITH owned AS (
		SELECT r.* FROM model_requests r WHERE r.conversation_id IN
		(SELECT conversation_id FROM agent_sessions WHERE id IN (SELECT value FROM json_each(?)))
		AND r.agent_session_id IN (SELECT value FROM json_each(?)))
		SELECT r.agent_session_id,m.role,m.kind,m.text,m.raw_text FROM owned r JOIN messages m ON m.id=r.source_message_id
		WHERE m.conversation_id=r.conversation_id
		UNION ALL
		SELECT r.agent_session_id,m.role,m.kind,m.text,m.raw_text FROM owned r JOIN messages m ON m.id=r.final_message_id
		WHERE m.conversation_id=r.conversation_id`, encoded, encoded)
	if err != nil {
		return nil, SessionModelRepair{}, err
	}
	for _, row := range modelRequests {
		m := storedMessageRecords([]map[string]any{row})[0]
		if usageCopyOf(m) == "" {
			requests[firstString(row["agent_session_id"])].add(ownRequestModel(m))
		}
	}
	results, err := queryMapsContext(ctx, q, `SELECT s.id,m.text FROM agent_sessions s JOIN messages m
		ON m.conversation_id=s.conversation_id AND m.call_id=s.native_id AND m.kind='delegation_result'
		WHERE s.id IN (SELECT value FROM json_each(?))`, encoded)
	if err != nil {
		return nil, SessionModelRepair{}, err
	}
	for _, row := range results {
		usage[firstString(row["id"])].add(firstString(mapValueDefault(mapValueDefault(parseJSONValue(firstString(row["text"])))["details"])["model"]))
	}
	launches, err := queryMapsContext(ctx, q, `SELECT s.id,m.text FROM agent_sessions s JOIN messages m ON m.id=s.delegation_message_id
		WHERE s.id IN (SELECT value FROM json_each(?))`, encoded)
	if err != nil {
		return nil, SessionModelRepair{}, err
	}
	for _, row := range launches {
		configured[firstString(row["id"])].add(launchConfiguredModel(MessageRecord{Text: firstString(row["text"])}))
	}
	var resolve func(string, map[string]bool) string
	resolve = func(id string, seen map[string]bool) string {
		if seen[id] || byID[id] == nil {
			return ""
		}
		seen[id] = true
		row := byID[id]
		if model := executionModelSummary(requests[id], usage[id]); model != "" {
			return model
		}
		if child := mains[firstString(row["child_conversation_id"])]; child != "" {
			if model := resolve(child, seen); model != "" && model != "Unknown model" {
				return model
			}
		}
		if model := configured[id].summary(); model != "" {
			return model
		}
		if row["native_id"] == "main" {
			models := sessionModels{}
			models.add(firstString(row["conversation_model"]))
			if model := models.summary(); model != "" {
				return model
			}
		}
		return "Unknown model"
	}
	changes, report := []sessionModelChange{}, SessionModelRepair{}
	for _, row := range rows {
		if !target(row) {
			continue
		}
		report.Sessions++
		id := firstString(row["id"])
		if blankPricing[id] {
			report.PricingFallback++
			continue
		}
		model := resolve(id, map[string]bool{})
		if model == multipleSessionModels {
			report.Multiple++
		}
		if model == "Unknown model" {
			report.Unknown++
		}
		if before := firstString(row["model"]); before != model {
			changes = append(changes, sessionModelChange{id, before, model})
			report.Updated++
		}
	}
	return changes, report, nil
}

// Reconcile embedded labels only after the group's sessions and links exist.
// This also handles retained-lineage restoration and withdrawn child links.
// It shares the backfill's evidence/pricing rules without rescanning unrelated
// executions in the workspace or advancing the one-time upgrade marker.
func refreshLinkedSessionModels(tx *sql.Tx, conversations []string) error {
	rows, err := queryMaps(tx, `WITH handles AS (
		SELECT * FROM agent_sessions WHERE conversation_id IN (SELECT value FROM json_each(?)) AND kind='subagent')
		SELECT s.*,c.model conversation_model FROM agent_sessions s JOIN conversations c ON c.id=s.conversation_id
		WHERE s.id IN (SELECT id FROM handles) OR (s.native_id='main' AND s.conversation_id IN (SELECT child_conversation_id FROM handles))`, jsonText(conversations))
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	changes, _, err := planSessionModelRows(context.Background(), tx, rows, func(row map[string]any) bool {
		return row["kind"] == "subagent"
	})
	if err != nil {
		return err
	}
	return applySessionModelChanges(context.Background(), tx, changes)
}

func applySessionModelChanges(ctx context.Context, tx *sql.Tx, changes []sessionModelChange) error {
	for _, change := range changes {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_sessions SET model=? WHERE id=?`, change.after, change.id); err != nil {
			return err
		}
	}
	return nil
}

// RepairAgentSessionModels is a metadata-only, restartable pass. Each workspace
// commits separately through the normal writer; repeating it is harmless. Its
// own version marker intentionally does not advance usageAttributionVersion.
func (c *Catalog) RepairAgentSessionModels(ctx context.Context, progress func(int, int)) (SessionModelRepair, error) {
	rows, err := queryMapsContext(ctx, c.DB, `SELECT DISTINCT workspace_id FROM agent_sessions WHERE kind='subagent' ORDER BY workspace_id`)
	if err != nil {
		return SessionModelRepair{}, err
	}
	total := SessionModelRepair{}
	for i, row := range rows {
		var report SessionModelRepair
		err = c.writeTransaction(ctx, "session-model-repair", func(tx *sql.Tx) error {
			changes, r, err := planSessionModels(ctx, tx, firstString(row["workspace_id"]))
			report = r
			if err != nil {
				return err
			}
			return applySessionModelChanges(ctx, tx, changes)
		})
		if err != nil {
			return total, err
		}
		total.Sessions += report.Sessions
		total.Updated += report.Updated
		total.Multiple += report.Multiple
		total.Unknown += report.Unknown
		total.PricingFallback += report.PricingFallback
		c.boundWAL(ctx, walSizeLimit)
		if progress != nil {
			progress(i+1, len(rows))
		}
	}
	err = c.writeTransaction(ctx, "session-model-version", func(tx *sql.Tx) error { return setMeta(tx, "agent_session_model_version", agentSessionModelVersion) })
	return total, err
}
