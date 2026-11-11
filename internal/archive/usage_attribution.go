package archive

import (
	"context"
	"database/sql"
	"slices"
	"strings"
)

const usageAttributionVersion = "9"

func claudeChildren(conversations []ConversationRecord, root ConversationRecord) []ConversationRecord {
	if root.Provider != "claude" || root.AgentDepth != 0 {
		return nil
	}
	children := []ConversationRecord{}
	for _, child := range conversations {
		if child.AgentDepth > 0 && (child.ParentNativeID == root.NativeID || strings.HasPrefix(child.NativeID, root.NativeID+":subagent:")) {
			children = append(children, child)
		}
	}
	return children
}

func claudeGroupRemainder(conversations []ConversationRecord, root ConversationRecord) map[string]tokenCounts {
	if root.Provider != "claude" || root.AgentDepth != 0 {
		return nil
	}
	return resolvedClaudeGroupRemainder(conversationsWithChildUsage(conversations), root, nil)
}

type usageConversationKey struct{ provider, account, nativeID string }

func usageConversationIdentity(c ConversationRecord) usageConversationKey {
	return usageConversationKey{c.Provider, c.Account, c.NativeID}
}

func resolvedClaudeGroupRemainder(conversations []ConversationRecord, root ConversationRecord, preserved map[usageConversationKey]map[string]tokenCounts) map[string]tokenCounts {
	for _, c := range conversations {
		if c.NativeID == root.NativeID && c.Account == root.Account {
			root = c
			break
		}
	}
	// Claude Code names a model's claim by its context variant
	// ("claude-opus-5-5[1m]") while requests carry the base ID, so claims and
	// requests are matched by model family. Each cost-state replaces the
	// previous one for the models it names.
	latest := map[string]tokenCounts{}
	for _, message := range root.Messages {
		raw, value := tokenObject(message)
		if firstString(value["type"], raw["type"]) != "cost-state" {
			continue
		}
		for model, usage := range mapValueDefault(value["modelUsage"]) {
			if counts := normalizeTokenCounts(mapValue(usage)); counts != nil {
				latest[model] = counts
			}
		}
	}
	if len(latest) == 0 {
		return nil
	}
	claims, names := map[string]tokenCounts{}, map[string]string{}
	for model, counts := range latest {
		family := modelFamily(model)
		if claims[family] == nil {
			claims[family], names[family] = tokenCounts{}, model
		} else if model < names[family] {
			names[family] = model
		}
		addTokenCounts(claims[family], counts)
	}
	requests := map[string]tokenCounts{}
	hasRequests := false
	for _, conversation := range append([]ConversationRecord{root}, claudeChildren(conversations, root)...) {
		// A claim counts whatever the process sent, so a reply counted in
		// another conversation is not left over beyond it.
		modelRequests, _ := buildToolLedger(withUsageCopies(conversation.Messages), conversation.Model)
		for _, request := range modelRequests {
			if request.Counts["total_tokens"] <= 0 {
				continue
			}
			hasRequests = true
			family := tokenModelFamily(request.Model, claims)
			if requests[family] == nil {
				requests[family] = tokenCounts{}
			}
			addTokenCounts(requests[family], request.Counts)
		}
		// Categorized zero-tool results are requests recovered without their
		// envelopes. They are not model_requests rows, but the process claim
		// covers them just as it covers ordinary retained requests.
		for _, report := range conversationTokenReportsWithCostState(withUsageCopies(conversation.Messages), true) {
			if !report.requestOnly || report.counts["total_tokens"] <= 0 {
				continue
			}
			hasRequests = true
			family := tokenModelFamily(report.model, claims)
			if requests[family] == nil {
				requests[family] = tokenCounts{}
			}
			addTokenCounts(requests[family], report.counts)
		}
		// Legacy facts preserved without their original envelopes are already
		// accounted usage too. They overlap the process claim exactly as retained
		// requests do; observations of an untyped minimum never enter this pool.
		for model, counts := range preserved[usageConversationIdentity(conversation)] {
			hasRequests = true
			family := tokenModelFamily(model, claims)
			if requests[family] == nil {
				requests[family] = tokenCounts{}
			}
			addTokenCounts(requests[family], counts)
		}
	}
	if !hasRequests {
		return nil
	}
	remainders := map[string]tokenCounts{}
	for family, claim := range claims {
		if remainder := tokenRemainder(claim, requests[family]); remainder["total_tokens"] > 0 {
			remainders[names[family]] = remainder
		}
	}
	// A recovered request can have an unknown or ambiguous model alias. Its
	// categories still overlap the whole-process claim. Reconcile against the
	// remaining pool without assigning those categories to an arbitrary model.
	unmatched := tokenCounts{}
	for family, counts := range requests {
		if claims[family] == nil {
			addTokenCounts(unmatched, counts)
		}
	}
	if unmatched["total_tokens"] > 0 {
		pool := tokenCounts{}
		for _, counts := range remainders {
			addTokenCounts(pool, counts)
		}
		remainders = map[string]tokenCounts{}
		if remaining := tokenRemainder(pool, unmatched); remaining["total_tokens"] > 0 {
			remainders["Unknown model"] = remaining
		}
	}
	return remainders
}

func applyClaudeGroupUsage(tx *sql.Tx, workspaceID string, conversations []ConversationRecord, ids []string, authority []conversationAuthority) error {
	if !slices.ContainsFunc(conversations, func(c ConversationRecord) bool { return c.Provider == "claude" }) {
		return nil
	}
	// Child sessions may have just been replaced by an authoritative ingest.
	// Resolve against their committed-to-this-transaction state, so ownership
	// can move back to the parent when a child's accounting disappears.
	conversations, err := conversationsWithStoredChildUsage(tx, workspaceID, conversations, ids)
	if err != nil {
		return err
	}
	states, err := queryMaps(tx, `SELECT i.conversation_id,i.child_usage FROM conversation_derivation_inputs i
		JOIN conversations c ON c.id=i.conversation_id WHERE c.workspace_id=?`, workspaceID)
	if err != nil {
		return err
	}
	prior := map[string]string{}
	for _, state := range states {
		prior[firstString(state["conversation_id"])] = firstString(state["child_usage"])
	}
	for index, conversation := range conversations {
		if index >= len(ids) || ids[index] == "" || authority != nil && (index >= len(authority) || !authority[index].writer) {
			continue
		}
		want := childUsageFingerprint(conversation.Messages)
		before, recorded := prior[ids[index]]
		changed := recorded && before != want
		if !changed && !hasChildUsageResult(conversation.Messages) {
			continue
		}
		calls := []string{}
		for _, m := range conversation.Messages {
			if m.usageInChild {
				calls = append(calls, m.CallID)
			}
		}
		var duplicate bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM agent_sessions WHERE conversation_id=? AND total_tokens>0 AND native_id IN (SELECT value FROM json_each(?)))`, ids[index], jsonText(calls)).Scan(&duplicate); err != nil {
			return err
		}
		if changed || duplicate {
			if err := writeResolvedAgentSessions(tx, workspaceID, ids[index], conversation, false, false, true); err != nil {
				return err
			}
			if changed {
				if _, err := tx.Exec(`UPDATE conversation_derivation_inputs SET child_usage=? WHERE conversation_id=?`, want, ids[index]); err != nil {
					return err
				}
			}
		}
	}
	preserved, err := preservedClaudeGroupUsage(tx, workspaceID, conversations, ids)
	if err != nil {
		return err
	}
	hasRoot := false
	for index, root := range conversations {
		if root.Provider != "claude" || root.AgentDepth != 0 || index >= len(ids) || ids[index] == "" ||
			(authority != nil && (index >= len(authority) || !authority[index].writer)) {
			continue
		}
		hasRoot = true
		remainders := resolvedClaudeGroupRemainder(conversations, root, preserved)
		rootID := ids[index]
		current, err := groupRemaindersCurrent(tx, rootID, remainders)
		if err != nil {
			return err
		}
		if remainders != nil && !current {
			if err := writeResolvedAgentSessions(tx, workspaceID, rootID, root, true, false, true); err != nil {
				return err
			}
			var sessionID string
			if err := tx.QueryRow("SELECT id FROM agent_sessions WHERE conversation_id=? AND native_id='main'", rootID).Scan(&sessionID); err != nil {
				return err
			}
			for model, counts := range remainders {
				if err := insertSessionRemainder(tx, sessionID, usageHour(root.StartedAt), model, counts); err != nil {
					return err
				}
			}
		}
	}
	if hasRoot {
		return linkClaudeGroupDelegations(tx, conversations, ids)
	}
	return nil
}

// Find only session facts the retained transcript could not reconstruct.
// Matching ordinary requests/recovered results are counted above, so reading
// every stored ledger row here would double-count them instead.
func preservedClaudeGroupUsage(q queryer, workspace string, conversations []ConversationRecord, ids []string) (map[usageConversationKey]map[string]tokenCounts, error) {
	byID := map[string]ConversationRecord{}
	reconstructable := map[string]map[string]bool{}
	for i, c := range conversations {
		if i >= len(ids) || ids[i] == "" {
			continue
		}
		byID[ids[i]] = c
		reconstructable[ids[i]] = map[string]bool{}
		for _, summary := range agentSessionSummaries(c.Messages) {
			known := len(summary.counts) > 0
			for _, index := range summary.messageIndexes {
				known = known || usageCopyOf(c.Messages[index]) != ""
			}
			reconstructable[ids[i]][summary.nativeID] = known
		}
	}
	sessions, err := queryMaps(q, `SELECT id,conversation_id,native_id FROM agent_sessions
		WHERE workspace_id=? AND total_tokens>0 AND usage_status<>'in-child-conversation'`, workspace)
	if err != nil {
		return nil, err
	}
	kept := []string{}
	for _, s := range sessions {
		cid, native := firstString(s["conversation_id"]), firstString(s["native_id"])
		c, included := byID[cid]
		if !included || c.AgentDepth == 0 && native == "main" || reconstructable[cid][native] {
			continue
		}
		kept = append(kept, firstString(s["id"]))
	}
	if len(kept) == 0 {
		return nil, nil
	}
	fields := []string{"a.conversation_id", "COALESCE(NULLIF(u.model,''),NULLIF(a.model,''),'Unknown model') model"}
	for _, field := range usageTokenFields {
		fields = append(fields, "COALESCE(u."+field+",a."+field+",0) AS "+field)
	}
	rows, err := queryMaps(q, "SELECT "+strings.Join(fields, ",")+` FROM agent_sessions a
		LEFT JOIN agent_session_usage u ON u.agent_session_id=a.id WHERE a.id IN (SELECT value FROM json_each(?))`, jsonText(kept))
	if err != nil {
		return nil, err
	}
	result := map[usageConversationKey]map[string]tokenCounts{}
	for _, row := range rows {
		key := usageConversationIdentity(byID[firstString(row["conversation_id"])])
		if result[key] == nil {
			result[key] = map[string]tokenCounts{}
		}
		model := firstString(row["model"])
		if result[key][model] == nil {
			result[key][model] = tokenCounts{}
		}
		for _, field := range usageTokenFields {
			result[key][model][field] += float64(integer(row[field]))
		}
	}
	return result, nil
}

// Avoid rewriting an unchanged root merely because its group has a claim.
func groupRemaindersCurrent(q queryer, conversationID string, want map[string]tokenCounts) (bool, error) {
	if want == nil {
		return true, nil
	}
	rows, err := queryMaps(q, `SELECT u.* FROM agent_session_usage u JOIN agent_sessions a ON a.id=u.agent_session_id
		WHERE a.conversation_id=? AND a.native_id='main' AND u.attribution LIKE '%-remainder'`, conversationID)
	if err != nil || len(rows) != len(want) {
		return false, err
	}
	for _, row := range rows {
		counts, ok := want[firstString(row["model"])]
		if !ok || firstString(row["attribution"]) != "session-remainder" {
			return false, nil
		}
		for _, field := range usageTokenFields {
			value := counts[field]
			switch field {
			case "uncached_input_tokens":
				value = max(counts["input_tokens"]-counts["cache_read_input_tokens"]-counts["cache_creation_input_tokens"], 0)
			case "unclassified_tokens":
				value = max(counts["total_tokens"]-counts["input_tokens"]-counts["output_tokens"], 0)
			}
			if integer(row[field]) != integer(value) {
				return false, nil
			}
		}
	}
	return true, nil
}

func insertSessionRemainder(tx *sql.Tx, sessionID, hour, model string, counts tokenCounts) error {
	if counts["total_tokens"] <= 0 {
		return nil
	}
	_, err := tx.Exec(`INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,attribution,input_tokens,uncached_input_tokens,
		cache_read_input_tokens,cache_creation_input_tokens,cache_creation_5m_input_tokens,cache_creation_1h_input_tokens,
		output_tokens,reasoning_output_tokens,unclassified_tokens,total_tokens) VALUES(?,?,?,'session-remainder',?,?,?,?,?,?,?,?,?,?)`,
		sessionID, hour, model, integer(counts["input_tokens"]), integer(max(counts["input_tokens"]-counts["cache_read_input_tokens"]-counts["cache_creation_input_tokens"], 0)),
		integer(counts["cache_read_input_tokens"]), integer(counts["cache_creation_input_tokens"]), integer(counts["cache_creation_5m_input_tokens"]),
		integer(counts["cache_creation_1h_input_tokens"]), integer(counts["output_tokens"]), integer(counts["reasoning_output_tokens"]),
		integer(max(counts["total_tokens"]-counts["input_tokens"]-counts["output_tokens"], 0)), integer(counts["total_tokens"]))
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE agent_sessions SET input_tokens=input_tokens+?,uncached_input_tokens=uncached_input_tokens+?,
		cache_read_input_tokens=cache_read_input_tokens+?,cache_creation_input_tokens=cache_creation_input_tokens+?,
		cache_creation_5m_input_tokens=cache_creation_5m_input_tokens+?,cache_creation_1h_input_tokens=cache_creation_1h_input_tokens+?,
		output_tokens=output_tokens+?,reasoning_output_tokens=reasoning_output_tokens+?,unclassified_tokens=unclassified_tokens+?,
		total_tokens=total_tokens+?,usage_status=CASE WHEN unclassified_tokens+?>0 THEN 'reported-partial' ELSE 'reported-reconciled' END WHERE id=?`,
		integer(counts["input_tokens"]), integer(max(counts["input_tokens"]-counts["cache_read_input_tokens"]-counts["cache_creation_input_tokens"], 0)),
		integer(counts["cache_read_input_tokens"]), integer(counts["cache_creation_input_tokens"]), integer(counts["cache_creation_5m_input_tokens"]),
		integer(counts["cache_creation_1h_input_tokens"]), integer(counts["output_tokens"]), integer(counts["reasoning_output_tokens"]),
		integer(max(counts["total_tokens"]-counts["input_tokens"]-counts["output_tokens"], 0)), integer(counts["total_tokens"]),
		integer(max(counts["total_tokens"]-counts["input_tokens"]-counts["output_tokens"], 0)), sessionID)
	return err
}

// RepairUsageAttribution replays stored messages one workspace at a time.
// The version row commits with the corrected sessions and metrics.
func (c *Catalog) RepairUsageAttribution(ctx context.Context, progress func(int, int)) (int, error) {
	return c.repairUsageAttribution(ctx, progress, walSizeLimit)
}

func (c *Catalog) repairUsageAttribution(ctx context.Context, progress func(int, int), walLimit int64) (int, error) {
	rows, err := queryMapsContext(ctx, c.DB, `SELECT w.id,w.source_kind FROM workspaces w WHERE EXISTS
		(SELECT 1 FROM conversations c WHERE c.workspace_id=w.id)
		AND NOT EXISTS (SELECT 1 FROM usage_attribution_state s WHERE s.workspace_id=w.id AND s.version=?)`, usageAttributionVersion)
	if err != nil {
		return 0, err
	}
	for index, row := range rows {
		if err := ctx.Err(); err != nil {
			return index, err
		}
		workspaceID := firstString(row["id"])
		// Read under the writer: what an ingest adds meanwhile must not be
		// lost to an earlier snapshot.
		err := c.writeTransaction(ctx, "usage-repair workspace="+workspaceID, func(tx *sql.Tx) error {
			conversations, ids, err := storedWorkspaceConversations(ctx, tx, workspaceID)
			if err != nil {
				return err
			}
			if firstString(row["source_kind"]) == "claude" {
				err = repairClaudeUsageCopies(ctx, tx, workspaceID, conversations, ids)
			} else {
				// TL1 runs Claude Code too; its stored replies get their
				// advisor records, but its copies are not settled.
				var changed map[string]bool
				if changed, err = backfillClaudeAdvisorUsage(tx, conversations, ids); err == nil {
					err = rebuildWorkspaceUsage(tx, workspaceID, conversations, ids, changed)
				}
			}
			if err != nil {
				return err
			}
			if err := repairModelSummaries(tx, workspaceID, conversations, ids); err != nil {
				return err
			}
			_, err = tx.Exec(`INSERT INTO usage_attribution_state(workspace_id,version) VALUES(?,?)
				ON CONFLICT(workspace_id) DO UPDATE SET version=excluded.version`, workspaceID, usageAttributionVersion)
			return err
		})
		if err != nil {
			return index, err
		}
		c.boundWAL(ctx, walLimit)
		if progress != nil {
			progress(index+1, len(rows))
		}
	}
	return len(rows), nil
}

// Kept separate from the source fingerprint used by delivery checkpoints.
func childUsageFingerprint(messages []MessageRecord) string {
	calls := []string{}
	for _, m := range messages {
		if m.usageInChild {
			calls = append(calls, m.CallID)
		}
	}
	if len(calls) == 0 {
		return ""
	}
	slices.Sort(calls)
	return hashBytes([]byte(jsonText(slices.Compact(calls))))
}

func hasChildUsageResult(messages []MessageRecord) bool {
	for _, m := range messages {
		if m.usageInChild {
			return true
		}
	}
	return false
}

func storedChildUsageCalls(q queryer, conversationID string) (map[string]bool, error) {
	rows, err := queryMaps(q, `SELECT native_id FROM agent_sessions WHERE conversation_id=? AND child_conversation_id IS NOT NULL AND usage_status='in-child-conversation'`, conversationID)
	if err != nil {
		return nil, err
	}
	calls := map[string]bool{}
	for _, row := range rows {
		calls[firstString(row["native_id"])] = true
	}
	return calls, nil
}

func messagesWithStoredChildUsage(q queryer, conversationID string, messages []MessageRecord) ([]MessageRecord, error) {
	candidate := false
	for _, m := range messages {
		if m.Kind == "delegation_result" && strings.Contains(m.Text, "totalToolUseCount") && singleRequestAgentResult(mapValueDefault(mapValueDefault(parseJSONValue(m.Text))["details"])) != nil {
			candidate = true
			break
		}
	}
	if !candidate {
		return messages, nil
	}
	rows, err := queryMaps(q, `SELECT workspace_id FROM conversations WHERE id=?`, conversationID)
	if err != nil || len(rows) == 0 {
		return messages, err
	}
	workspace := firstString(rows[0]["workspace_id"])
	conversations, ids, err := storedWorkspaceConversations(context.Background(), q, workspace)
	if err != nil {
		return nil, err
	}
	normalized, err := conversationsWithStoredChildUsage(q, workspace, conversations, ids)
	if err != nil {
		return nil, err
	}
	calls := map[string]bool{}
	for i, c := range normalized {
		if ids[i] == conversationID {
			for _, m := range c.Messages {
				if m.usageInChild {
					calls[m.CallID] = true
				}
			}
		}
	}
	if len(calls) == 0 {
		return messages, nil
	}
	result := slices.Clone(messages)
	for i, m := range result {
		if m.Kind == "delegation_result" && calls[m.CallID] {
			result[i].usageInChild = true
		}
	}
	return result, nil
}

// Positive legacy child facts remain evidence when request envelopes were
// not retained. Explicit zero reports, however, supersede those old facts.
func conversationsWithStoredChildUsage(q queryer, workspace string, conversations []ConversationRecord, ids []string) ([]ConversationRecord, error) {
	candidate := false
	for _, conversation := range conversations {
		for _, m := range conversation.Messages {
			if m.Kind == "delegation_result" && strings.Contains(m.Text, "totalToolUseCount") && singleRequestAgentResult(mapValueDefault(mapValueDefault(parseJSONValue(m.Text))["details"])) != nil {
				candidate = true
				break
			}
		}
	}
	if !candidate {
		return conversations, nil
	}
	sessions, err := queryMaps(q, `SELECT conversation_id FROM agent_sessions WHERE workspace_id=? AND native_id='main' AND total_tokens>0`, workspace)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, s := range sessions {
		known[firstString(s["conversation_id"])] = true
	}
	recorded := map[int]bool{}
	for i, id := range ids {
		if !known[id] {
			continue
		}
		reconstructable := false
		for _, summary := range agentSessionSummaries(withUsageCopies(conversations[i].Messages)) {
			if summary.nativeID == "main" && len(summary.counts) > 0 {
				reconstructable = true
			}
		}
		recorded[i] = !reconstructable
	}
	return conversationsWithChildUsageState(conversations, recorded), nil
}
