package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"maps"
	"slices"
	"sort"
	"strings"
)

// Claude Code copies a reply into another session's transcript when a session
// is resumed or forked: the copy keeps the reply's uuid, time, message ID and
// request ID, and takes the new session's ID, so nothing in it says which is
// the original. Library totals count the reply once. Every Claude Code
// conversation's replies are listed in claude_usage_copies by account and
// message ID (claudeCrossKey), with what its records of each say now (copy_of);
// of the conversations holding one, the one that started first (then by
// native ID) counts it, unless another holds a fuller record of it, and the
// others keep their records as copies of it
// (wrapUsageCopy), which count nothing. The owner does not depend on which
// conversation was indexed first, so a library rebuilt in another order, or
// on another Mac, counts the same. A gateway can reuse one message ID for
// every reply, so its replies are matched by request ID too, and not at all
// without one. Copies within one session are settled by its parse
// (dedupeClaudeGroupUsage).
//
// Only Claude Code's own records take part. Conductor and TL1 hold Claude
// conversations that mirror a native one; Library counts one of a mirror
// group (suppressMirrorRows), which must keep its replies.

type claudeUsageHolder struct {
	conversation, workspace, native, started, copyOf string
	tokens                                           int64
}

// before says whether h counts a reply rather than other: the fuller record
// of it (a reply's usage grows as it streams, so a copy can hold more than an
// original read before it finished), then the earlier start.
func (h claudeUsageHolder) before(other claudeUsageHolder) bool {
	if h.tokens != other.tokens {
		return h.tokens > other.tokens
	}
	if h.started != other.started {
		return h.started < other.started
	}
	return h.native < other.native
}

func claudeUsageOwner(holders []claudeUsageHolder) claudeUsageHolder {
	owner := holders[0]
	for _, holder := range holders[1:] {
		if holder.before(owner) {
			owner = holder
		}
	}
	return owner
}

// claudeCrossKey is the key of a usage record's reply across sessions, or ""
// for a record that is not a reply's usage, with the tokens the record
// reports. An Anthropic message ID names one reply in every session, whatever
// request ID a sidechain gave its copy; another message ID needs its request
// ID, and is not matched without one.
func claudeCrossKey(account string, m MessageRecord) (string, int64) {
	keys, tokens := claudeUsageKeys(m)
	if len(keys) == 0 {
		return "", 0
	}
	return account + "\x00" + keys[len(keys)-1], tokens
}

// usageCopyOf names the conversation a copy's record counts in, or "" when m
// is not a copy.
func usageCopyOf(m MessageRecord) string {
	if _, copied := unwrapUsageCopy(m.RawText); !copied {
		return ""
	}
	value := map[string]any{}
	json.Unmarshal([]byte(m.RawText), &value)
	return firstString(value["copy_of"])
}

// claudeKeyState is what a conversation's records of one reply say: the
// conversation they name as counting it (copy_of, "" for their own) and the
// most tokens one reports.
type claudeKeyState struct {
	copyOf string
	tokens int64
}

// claudeConversationKeys lists a conversation's cross-session keys, leaving
// out the copies its session's parse made of another of natives.
func claudeConversationKeys(conversation ConversationRecord, natives map[string]bool) map[string]claudeKeyState {
	keys := map[string]claudeKeyState{}
	if conversation.Provider != "claude" {
		return keys
	}
	for _, message := range conversation.Messages {
		key, tokens := claudeCrossKey(conversation.Account, message)
		if key == "" {
			continue
		}
		copyOf := usageCopyOf(message)
		if natives[copyOf] {
			continue
		}
		state, seen := keys[key]
		if !seen {
			state.copyOf = copyOf
		} else if state.copyOf != copyOf {
			// A reply's records that disagree agree with no owner, so the
			// settlement rewrites them.
			state.copyOf = claudeMixedCopies
		}
		state.tokens = max(state.tokens, tokens)
		keys[key] = state
	}
	return keys
}

// claudeMixedCopies is the copy_of of a conversation whose records of one
// reply say different things; it names no conversation.
const claudeMixedCopies = "\x00mixed"

func recordNatives(record WorkspaceRecord) map[string]bool {
	natives := map[string]bool{}
	for _, conversation := range record.Conversations {
		natives[conversation.NativeID] = true
	}
	return natives
}

// claudeUsageHolders lists the stored holders of keys, by key.
func claudeUsageHolders(tx *sql.Tx, keys []string) (map[string][]claudeUsageHolder, error) {
	holders := map[string][]claudeUsageHolder{}
	for start := 0; start < len(keys); start += 400 {
		chunk := keys[start:min(start+400, len(keys))]
		args := make([]any, len(chunk))
		for i, key := range chunk {
			args[i] = key
		}
		rows, err := tx.Query(`SELECT k.usage_key,k.conversation_id,c.workspace_id,c.native_id,COALESCE(c.started_at,''),k.copy_of,k.tokens FROM claude_usage_copies k
			JOIN conversations c ON c.id=k.conversation_id WHERE k.usage_key IN (`+placeholders(len(chunk))+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var key string
			var holder claudeUsageHolder
			if err := rows.Scan(&key, &holder.conversation, &holder.workspace, &holder.native, &holder.started, &holder.copyOf, &holder.tokens); err != nil {
				rows.Close()
				return nil, err
			}
			holders[key] = append(holders[key], holder)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return holders, nil
}

// claimClaudeUsage makes the usage records of a Claude Code record's
// conversations copies where another conversation owns their reply, and its
// own where none does, before they are written. It leaves the conversations
// and messages record had as they were: they are what the source gave.
func claimClaudeUsage(tx *sql.Tx, record *WorkspaceRecord) error {
	if record.SourceKind != "claude" {
		return nil
	}
	natives := recordNatives(*record)
	byIndex := map[int]map[string]claudeKeyState{}
	candidates := map[string][]claudeUsageHolder{}
	all := []string{}
	for index, conversation := range record.Conversations {
		keys := claudeConversationKeys(conversation, natives)
		if len(keys) == 0 {
			continue
		}
		byIndex[index] = keys
		for key, state := range keys {
			if candidates[key] == nil {
				all = append(all, key)
			}
			candidates[key] = append(candidates[key], claudeUsageHolder{native: conversation.NativeID, started: conversation.StartedAt, tokens: state.tokens})
		}
	}
	if len(all) == 0 {
		return nil
	}
	sort.Strings(all)
	holders, err := claudeUsageHolders(tx, all)
	if err != nil {
		return err
	}
	owners := map[string]string{}
	for key, list := range candidates {
		for _, holder := range holders[key] {
			// This record's own conversations are judged from what it says now.
			if !natives[holder.native] {
				list = append(list, holder)
			}
		}
		owners[key] = claudeUsageOwner(list).native
	}
	record.Conversations = slices.Clone(record.Conversations)
	for index, keys := range byIndex {
		conversation := &record.Conversations[index]
		conversation.Messages = slices.Clone(conversation.Messages)
		for m := range conversation.Messages {
			message := &conversation.Messages[m]
			key, _ := claudeCrossKey(conversation.Account, *message)
			// A copy the session's parse made stays one.
			if _, ok := keys[key]; !ok || natives[usageCopyOf(*message)] {
				continue
			}
			owner := owners[key]
			if owner == conversation.NativeID {
				owner = ""
			}
			message.RawText = usageRecordAs(*message, owner)
		}
	}
	return nil
}

// settleClaudeUsage lists what a Claude Code record's conversations hold now,
// then has every holder of a key they hold or held agree with that key's
// owner, rebuilding the usage of each workspace whose stored records changed.
// A conversation the record merged into rather than wrote (fromStore) holds
// what is stored, which can be more than the record has.
func settleClaudeUsage(ctx context.Context, tx *sql.Tx, record WorkspaceRecord, conversationIDs []string, fromStore []bool, own string, ownLedger map[string]bool) error {
	if record.SourceKind != "claude" {
		return nil
	}
	natives := recordNatives(record)
	touched := map[string]bool{}
	for index, conversation := range record.Conversations {
		if index >= len(conversationIDs) || conversationIDs[index] == "" || conversation.Provider != "claude" {
			continue
		}
		id := conversationIDs[index]
		rows, err := tx.Query("SELECT usage_key FROM claude_usage_copies WHERE conversation_id=?", id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				rows.Close()
				return err
			}
			touched[key] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM claude_usage_copies WHERE conversation_id=?", id); err != nil {
			return err
		}
		if index < len(fromStore) && fromStore[index] {
			if conversation.Messages, err = storedMessages(ctx, tx, id); err != nil {
				return err
			}
		}
		for key, state := range claudeConversationKeys(conversation, natives) {
			if _, err := tx.Exec("INSERT INTO claude_usage_copies(usage_key,conversation_id,copy_of,tokens) VALUES(?,?,?,?)", key, id, state.copyOf, state.tokens); err != nil {
				return err
			}
			touched[key] = true
		}
	}
	keys := make([]string, 0, len(touched))
	for key := range touched {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	holders, err := claudeUsageHolders(tx, keys)
	if err != nil {
		return err
	}
	// Only a holder whose records say otherwise than its key's owner is read.
	decisions, workspaceOf := map[string]map[string]string{}, map[string]string{}
	for _, key := range keys {
		list := holders[key]
		if len(list) == 0 {
			continue
		}
		owner := claudeUsageOwner(list)
		for _, holder := range list {
			want := ""
			if holder.conversation != owner.conversation {
				want = owner.native
			}
			if want == holder.copyOf {
				continue
			}
			if decisions[holder.conversation] == nil {
				decisions[holder.conversation] = map[string]string{}
			}
			decisions[holder.conversation][key], workspaceOf[holder.conversation] = want, holder.workspace
		}
	}
	conversations := make([]string, 0, len(decisions))
	for conversation := range decisions {
		conversations = append(conversations, conversation)
	}
	sort.Strings(conversations)
	changed := map[string]map[string]bool{}
	for _, conversation := range conversations {
		updated, err := settleStoredUsage(tx, conversation, decisions[conversation])
		if err != nil {
			return err
		}
		if updated {
			if changed[workspaceOf[conversation]] == nil {
				changed[workspaceOf[conversation]] = map[string]bool{}
			}
			changed[workspaceOf[conversation]][conversation] = true
		}
		for key, copyOf := range decisions[conversation] {
			if _, err := tx.Exec("UPDATE claude_usage_copies SET copy_of=? WHERE usage_key=? AND conversation_id=?", copyOf, key, conversation); err != nil {
				return err
			}
		}
	}
	if own != "" {
		if changed[own] == nil {
			changed[own] = map[string]bool{}
		}
		maps.Copy(changed[own], ownLedger)
	}
	workspaces := make([]string, 0, len(changed))
	for workspace := range changed {
		workspaces = append(workspaces, workspace)
	}
	sort.Strings(workspaces)
	for _, workspace := range workspaces {
		conversations, ids, err := storedWorkspaceConversations(ctx, tx, workspace)
		if err != nil {
			return err
		}
		if err := rebuildWorkspaceUsage(tx, workspace, conversations, ids, changed[workspace]); err != nil {
			return err
		}
	}
	return nil
}

// settleStoredUsage makes a stored conversation's usage records of each key
// in copyOf a copy of the conversation it names there, or, where that is "",
// its own. It reports whether any changed.
func settleStoredUsage(tx *sql.Tx, conversationID string, copyOf map[string]string) (bool, error) {
	rows, err := tx.Query(`SELECT m.id,m.native_id,COALESCE(m.raw_text,''),COALESCE(m.text,''),c.account FROM messages m JOIN conversations c ON c.id=m.conversation_id
		WHERE m.conversation_id=? AND m.kind='metadata' AND (m.native_id LIKE '%:usage' OR m.native_id LIKE '%:usage:advisor:%')`, conversationID)
	if err != nil {
		return false, err
	}
	type update struct{ id, raw string }
	updates := []update{}
	for rows.Next() {
		var id, native, raw, text, account string
		if err := rows.Scan(&id, &native, &raw, &text, &account); err != nil {
			rows.Close()
			return false, err
		}
		record := MessageRecord{NativeID: native, Kind: "metadata", RawText: raw, Text: text}
		key, _ := claudeCrossKey(account, record)
		owner, ok := copyOf[key]
		if !ok {
			continue
		}
		if want := usageRecordAs(record, owner); want != raw {
			updates = append(updates, update{id, want})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	for _, u := range updates {
		if _, err := tx.Exec("UPDATE messages SET raw_text=? WHERE id=?", nilIfEmpty(u.raw), u.id); err != nil {
			return false, err
		}
	}
	return len(updates) > 0, nil
}

// settleStoredClaude settles a Claude Code workspace whole, from what it
// stores, when record does not hold all of it: record merged into a stored
// conversation (merged), whose own copies within its session were decided
// from what was stored, or the workspace holds conversations record did not
// have (a subagent whose transcript Claude Code removed while its session's
// stayed is still stored).
func settleStoredClaude(ctx context.Context, tx *sql.Tx, record WorkspaceRecord, workspaceID string, conversationIDs []string, merged bool) error {
	if record.SourceKind != "claude" || workspaceID == "" {
		return nil
	}
	if !merged {
		written := []any{workspaceID}
		for _, id := range conversationIDs {
			if id != "" {
				written = append(written, id)
			}
		}
		query := "SELECT EXISTS(SELECT 1 FROM conversations WHERE workspace_id=? AND provider='claude'"
		if len(written) > 1 {
			query += " AND id NOT IN (" + placeholders(len(written)-1) + ")"
		}
		var omitted bool
		if err := tx.QueryRow(query+")", written...).Scan(&omitted); err != nil || !omitted {
			return err
		}
	}
	conversations, ids, err := storedWorkspaceConversations(ctx, tx, workspaceID)
	if err != nil {
		return err
	}
	return repairClaudeUsageCopies(ctx, tx, workspaceID, conversations, ids)
}

// claudeRecordHasUsage says whether record's token metrics are derived from
// Claude usage records, so that a record whose records are all copies counts
// none.
func claudeRecordHasUsage(record WorkspaceRecord) bool {
	for _, conversation := range record.Conversations {
		if conversation.Provider == "claude" && slices.ContainsFunc(conversation.Messages, claudeUsageRecord) {
			return true
		}
	}
	return false
}

// repairClaudeUsageCopies settles the copies of a workspace's stored Claude
// Code conversations, as their parse would now: within each session, then
// across sessions. Claude Code removes old transcripts, so a conversation's
// stored messages may be all that is left of it. It rebuilds the
// workspace's usage, and that of any other whose records changed.
func repairClaudeUsageCopies(ctx context.Context, tx *sql.Tx, workspaceID string, conversations []ConversationRecord, ids []string) error {
	changed, err := backfillClaudeAdvisorUsage(tx, conversations, ids)
	if err != nil {
		return err
	}
	for rootIndex, root := range conversations {
		if root.Provider != "claude" || root.AgentDepth != 0 {
			continue
		}
		// The session's root and its subagents, as claudeChildren finds them.
		group := []int{rootIndex}
		for index, conversation := range conversations {
			if conversation.AgentDepth > 0 && (conversation.ParentNativeID == root.NativeID || strings.HasPrefix(conversation.NativeID, root.NativeID+":subagent:")) {
				group = append(group, index)
			}
		}
		record := WorkspaceRecord{}
		for _, index := range group {
			conversation := conversations[index]
			conversation.Messages = slices.Clone(conversation.Messages)
			record.Conversations = append(record.Conversations, conversation)
		}
		// Copies are decided by depth as a parse finds it from the session's
		// launches, which a library indexed earlier may not have stored.
		lineage := WorkspaceRecord{Conversations: slices.Clone(record.Conversations)}
		reconcileClaudeLineage(&lineage)
		depths := map[string]int{}
		for _, conversation := range lineage.Conversations {
			depths[conversation.NativeID] = conversation.AgentDepth
		}
		for g := range record.Conversations {
			record.Conversations[g].AgentDepth = depths[record.Conversations[g].NativeID]
		}
		dedupeClaudeGroupUsage(&record)
		for g, index := range group {
			for m, message := range record.Conversations[g].Messages {
				if message.RawText == conversations[index].Messages[m].RawText {
					continue
				}
				if _, err := tx.Exec("UPDATE messages SET raw_text=? WHERE conversation_id=? AND native_id=?", nilIfEmpty(message.RawText), ids[index], message.NativeID); err != nil {
					return err
				}
				changed[ids[index]] = true
			}
			conversations[index].Messages = record.Conversations[g].Messages
		}
	}
	return settleClaudeUsage(ctx, tx, WorkspaceRecord{SourceKind: "claude", Conversations: conversations}, ids, nil, workspaceID, changed)
}

// backfillClaudeAdvisorUsage adds the advisor records a workspace's stored
// Claude replies hold (repairClaudeAdvisorUsage), and says which
// conversations it added them to.
func backfillClaudeAdvisorUsage(tx *sql.Tx, conversations []ConversationRecord, ids []string) (map[string]bool, error) {
	changed := map[string]bool{}
	for index := range conversations {
		added, err := repairClaudeAdvisorUsage(tx, ids[index], &conversations[index])
		if err != nil {
			return nil, err
		}
		if added {
			changed[ids[index]] = true
		}
	}
	return changed, nil
}

// repairClaudeAdvisorUsage adds the advisor usage records (claudeAdvisorUsage)
// a stored conversation's replies hold in their usage but were stored without;
// the settlement after it makes them copies where their reply's are. It
// reports whether it added any.
func repairClaudeAdvisorUsage(tx *sql.Tx, conversationID string, conversation *ConversationRecord) (bool, error) {
	if conversation.Provider != "claude" {
		return false, nil
	}
	stored := map[string]bool{}
	for _, message := range conversation.Messages {
		stored[message.NativeID] = true
	}
	messages := make([]MessageRecord, 0, len(conversation.Messages))
	var writer *messageWriter
	for _, message := range conversation.Messages {
		messages = append(messages, message)
		if message.Kind != "metadata" || !strings.HasSuffix(message.NativeID, ":usage") || !strings.Contains(message.RawText, "advisor_message") {
			continue
		}
		raw, _ := unwrapUsageCopy(message.RawText)
		event := map[string]any{}
		if json.Unmarshal([]byte(raw), &event) != nil {
			continue
		}
		for _, advisor := range claudeAdvisorUsage(message, event, mapValue(mapValue(event["message"])["usage"])) {
			if stored[advisor.NativeID] {
				continue
			}
			if writer == nil {
				var err error
				if writer, err = newMessageWriter(tx, conversationID); err != nil {
					return false, err
				}
				defer writer.statement.Close()
			}
			id, _, err := writer.write(advisor)
			if err != nil {
				return false, err
			}
			if err := replaceMessageFTS(tx, id); err != nil {
				return false, err
			}
			messages = append(messages, advisor)
		}
	}
	conversation.Messages = messages
	return writer != nil, nil
}

// rederiveRecoveredClaudeUsage derives a recovered workspace's Claude usage
// again where it was saved without the advisor records its replies hold
// (claudeAdvisorUsage), or, for Claude Code's, where its stored usage records
// differ from those it was saved with (saved): writing them settled their
// replies' copies against the conversations this catalog holds. Otherwise
// the usage it was saved with stands.
func rederiveRecoveredClaudeUsage(ctx context.Context, tx *sql.Tx, saved WorkspaceRecord, workspaceID string) error {
	records := map[string]string{}
	for _, conversation := range saved.Conversations {
		for _, message := range conversation.Messages {
			if claudeUsageRecord(message) {
				records[conversation.NativeID+"\x00"+message.NativeID] = message.RawText
			}
		}
	}
	if len(records) == 0 {
		return nil
	}
	conversations, ids, err := storedWorkspaceConversations(ctx, tx, workspaceID)
	if err != nil {
		return err
	}
	changed := map[string]bool{}
	for index := range conversations {
		conversation := &conversations[index]
		for _, message := range conversation.Messages {
			if saved.SourceKind == "claude" && claudeUsageRecord(message) && message.RawText != records[conversation.NativeID+"\x00"+message.NativeID] {
				changed[ids[index]] = true
				break
			}
		}
		if added, err := repairClaudeAdvisorUsage(tx, ids[index], conversation); err != nil {
			return err
		} else if added {
			changed[ids[index]] = true
		}
	}
	if len(changed) == 0 {
		return nil
	}
	// The restored model requests and tool calls carry the fingerprints of
	// those the writer derived before they were restored over.
	for id := range changed {
		if _, err := tx.Exec("DELETE FROM tool_ledger_inputs WHERE conversation_id=?", id); err != nil {
			return err
		}
	}
	if saved.SourceKind != "claude" {
		return rebuildWorkspaceUsage(tx, workspaceID, conversations, ids, changed)
	}
	return settleClaudeUsage(ctx, tx, WorkspaceRecord{SourceKind: "claude", Conversations: conversations}, ids, nil, workspaceID, changed)
}

// rebuildWorkspaceUsage derives a workspace's sessions, hourly usage and token
// metrics again from retained evidence, preserving positive legacy facts when
// their envelopes were not retained. A reply it counted may now be another's.
// The model requests of the
// conversations in ledger are derived again too.
func rebuildWorkspaceUsage(tx *sql.Tx, workspaceID string, conversations []ConversationRecord, ids []string, ledger map[string]bool) error {
	conversations, err := conversationsWithStoredChildUsage(tx, workspaceID, conversations, ids)
	if err != nil {
		return err
	}
	for j, conversation := range conversations {
		if err := writeResolvedAgentSessions(tx, workspaceID, ids[j], conversation, false, false, true); err != nil {
			return err
		}
		if ledger[ids[j]] {
			if err := replaceToolLedger(tx, workspaceID, ids[j], conversation); err != nil {
				return err
			}
		}
	}
	if err := applyClaudeGroupUsage(tx, workspaceID, conversations, ids, nil); err != nil {
		return err
	}
	record := WorkspaceRecord{Conversations: conversations}
	if metrics := reconciledTokenMetrics(record); len(metrics) > 0 || claudeRecordHasUsage(record) {
		if _, err := tx.Exec("DELETE FROM metrics WHERE workspace_id=? AND unit='tokens'", workspaceID); err != nil {
			return err
		}
		// The persisted sessions also include facts preserved from legacy
		// imports. Replacing metrics from messages alone would omit them in
		// a workspace that mixes legacy and reconstructable conversations.
		fields := make([]string, 0, len(usageTokenFields))
		for _, field := range usageTokenFields {
			fields = append(fields, "COALESCE(SUM("+field+"),0) AS "+field)
		}
		rows, err := queryMaps(tx, "SELECT "+strings.Join(fields, ",")+" FROM agent_sessions WHERE workspace_id=?", workspaceID)
		if err != nil {
			return err
		}
		known := map[string]bool{"total_tokens": true}
		for _, metric := range metrics {
			known[firstString(metric["name"])] = true
		}
		totals := tokenCounts{}
		for _, field := range usageTokenFields {
			value := float64(integer(rows[0][field]))
			if value != 0 || known[field] {
				totals[field] = value
			}
		}
		return replaceMetrics(tx, workspaceID, tokenMetricRows(totals))
	}
	return nil
}

// storedWorkspaceConversations reads a workspace's conversations back from
// the catalog, as the usage derivations need them.
var storedWorkspaceConversations = loadStoredWorkspaceConversations

func loadStoredWorkspaceConversations(ctx context.Context, q queryer, workspaceID string) ([]ConversationRecord, []string, error) {
	items, err := queryMapsContext(ctx, q, `SELECT id,native_id,parent_id,provider,account,model,effort,agent_depth,started_at,ended_at,coverage,session_bounds_json FROM conversations
		WHERE workspace_id=? ORDER BY agent_depth,native_id`, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	conversations, ids := make([]ConversationRecord, 0, len(items)), make([]string, 0, len(items))
	nativeIDs := map[string]string{}
	for _, item := range items {
		nativeIDs[firstString(item["id"])] = firstString(item["native_id"])
	}
	for _, item := range items {
		messages, err := storedMessages(ctx, q, item["id"])
		if err != nil {
			return nil, nil, err
		}
		ids = append(ids, firstString(item["id"]))
		nativeID := firstString(item["native_id"])
		parentNativeID := nativeIDs[firstString(item["parent_id"])]
		if before, _, ok := strings.Cut(nativeID, ":subagent:"); ok && parentNativeID == "" {
			parentNativeID = before
		}
		conversations = append(conversations, ConversationRecord{NativeID: nativeID, ParentNativeID: parentNativeID, Provider: firstString(item["provider"]), Account: firstString(item["account"]),
			Model: firstString(item["model"]), Effort: firstString(item["effort"]), AgentDepth: int(integer(item["agent_depth"])), StartedAt: firstString(item["started_at"]), EndedAt: firstString(item["ended_at"]), Coverage: firstString(item["coverage"]), SessionBounds: decodeSessionBounds(item["session_bounds_json"]), Messages: messages})
	}
	return conversations, ids, nil
}
