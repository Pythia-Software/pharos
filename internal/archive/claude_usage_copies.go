package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"sort"
	"strings"
)

// Claude Code copies a reply into another session's transcript when a session
// is resumed or forked: the copy keeps the reply's uuid, time, message ID and
// request ID, and takes the new session's ID, so nothing in it says which is
// the original. Library totals count the reply once. Every Claude
// conversation's replies are listed in claude_usage_copies by account,
// message ID and request ID; of the conversations holding one, the one that
// started first (then by native ID) counts it, and the others keep their
// records as copies of it (wrapUsageCopy), which count nothing. The owner does
// not depend on which conversation was indexed first, so a library rebuilt in
// another order, or on another Mac, counts the same. A reply without a
// request ID is not matched across sessions: a gateway can reuse one message
// ID for every reply. Copies within one session are settled by its parse
// (dedupeClaudeGroupUsage).

type claudeUsageHolder struct {
	conversation, workspace, native, started string
}

func (h claudeUsageHolder) before(other claudeUsageHolder) bool {
	if h.started != other.started {
		return h.started < other.started
	}
	return h.native < other.native
}

// claudeCrossKey is the key of a usage record's reply across sessions, or ""
// for a record that is not a reply's usage or has no request ID.
func claudeCrossKey(account string, m MessageRecord) string {
	if !claudeUsageRecord(m) {
		return ""
	}
	raw, _ := unwrapUsageCopy(m.RawText)
	event := map[string]any{}
	if json.Unmarshal([]byte(raw), &event) != nil {
		return ""
	}
	id, request := firstString(mapValue(event["message"])["id"]), firstString(event["requestId"])
	if id == "" || request == "" {
		return ""
	}
	return account + "\x00" + id + "\x00" + request
}

// claudeGroupCopy says whether m is a copy its own record's parse made of
// another of its conversations' records.
func claudeGroupCopy(m MessageRecord, natives map[string]bool) bool {
	if _, copied := unwrapUsageCopy(m.RawText); !copied {
		return false
	}
	value := map[string]any{}
	json.Unmarshal([]byte(m.RawText), &value)
	return natives[firstString(value["copy_of"])]
}

// claudeRecordKeys lists each Claude conversation's cross-session keys, by
// index, leaving out the copies its parse made within the record.
func claudeRecordKeys(record WorkspaceRecord) map[int][]string {
	natives := map[string]bool{}
	for _, conversation := range record.Conversations {
		natives[conversation.NativeID] = true
	}
	keys := map[int][]string{}
	for index, conversation := range record.Conversations {
		if conversation.Provider != "claude" {
			continue
		}
		seen := map[string]bool{}
		for _, message := range conversation.Messages {
			key := claudeCrossKey(conversation.Account, message)
			if key == "" || seen[key] || claudeGroupCopy(message, natives) {
				continue
			}
			seen[key] = true
			keys[index] = append(keys[index], key)
		}
	}
	return keys
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
		rows, err := tx.Query(`SELECT k.usage_key,k.conversation_id,k.workspace_id,c.native_id,COALESCE(c.started_at,'') FROM claude_usage_copies k
			JOIN conversations c ON c.id=k.conversation_id WHERE k.usage_key IN (`+placeholders(len(chunk))+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var key string
			var holder claudeUsageHolder
			if err := rows.Scan(&key, &holder.conversation, &holder.workspace, &holder.native, &holder.started); err != nil {
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

// claimClaudeUsage makes the usage records of record's Claude conversations
// copies where a conversation in another workspace owns their reply. It
// returns the keys workspaceID held before, for settleClaudeUsage.
func claimClaudeUsage(tx *sql.Tx, record *WorkspaceRecord, workspaceID string) ([]string, error) {
	held, err := claudeHeldKeys(tx, workspaceID)
	if err != nil {
		return nil, err
	}
	byIndex := claudeRecordKeys(*record)
	if len(byIndex) == 0 {
		return held, nil
	}
	natives := map[string]bool{}
	all := []string{}
	candidates := map[string][]claudeUsageHolder{}
	for index, keys := range byIndex {
		conversation := record.Conversations[index]
		natives[conversation.NativeID] = true
		for _, key := range keys {
			if candidates[key] == nil {
				all = append(all, key)
			}
			candidates[key] = append(candidates[key], claudeUsageHolder{native: conversation.NativeID, started: conversation.StartedAt})
		}
	}
	holders, err := claudeUsageHolders(tx, all)
	if err != nil {
		return nil, err
	}
	owners := map[string]claudeUsageHolder{}
	for key, list := range candidates {
		for _, holder := range holders[key] {
			// This record's own conversations are judged from what it says now,
			// wherever they were stored.
			if holder.workspace != workspaceID && !natives[holder.native] {
				list = append(list, holder)
			}
		}
		owners[key] = claudeUsageOwner(list)
	}
	for index := range byIndex {
		conversation := &record.Conversations[index]
		for m := range conversation.Messages {
			message := &conversation.Messages[m]
			owner, ok := owners[claudeCrossKey(conversation.Account, *message)]
			if !ok || claudeGroupCopy(*message, natives) {
				continue
			}
			raw, _ := unwrapUsageCopy(message.RawText)
			if owner.native == conversation.NativeID {
				message.RawText = raw
			} else {
				message.RawText = wrapUsageCopy(raw, owner.native)
			}
		}
	}
	return held, nil
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

func claudeHeldKeys(tx *sql.Tx, workspaceID string) ([]string, error) {
	held := []string{}
	if workspaceID == "" {
		return held, nil
	}
	rows, err := tx.Query("SELECT DISTINCT usage_key FROM claude_usage_copies WHERE workspace_id=?", workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		held = append(held, key)
	}
	return held, rows.Err()
}

// settleClaudeUsage lists what record's Claude conversations hold now, then
// has every holder of a key workspaceID holds or held agree with that key's
// owner, rebuilding the usage of each workspace whose stored records changed.
// claimClaudeUsage already made this record's agree, unless it was merged into
// conversations it did not write.
func settleClaudeUsage(ctx context.Context, tx *sql.Tx, record WorkspaceRecord, workspaceID string, conversationIDs []string, held []string) error {
	touched := map[string]bool{}
	for _, key := range held {
		touched[key] = true
	}
	// A conversation can have moved here from another workspace.
	for _, id := range conversationIDs {
		if id == "" {
			continue
		}
		rows, err := tx.Query("SELECT usage_key FROM claude_usage_copies WHERE conversation_id=? AND workspace_id<>?", id, workspaceID)
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
	}
	if _, err := tx.Exec("DELETE FROM claude_usage_copies WHERE workspace_id=?", workspaceID); err != nil {
		return err
	}
	for index, keys := range claudeRecordKeys(record) {
		if index >= len(conversationIDs) || conversationIDs[index] == "" {
			continue
		}
		for _, key := range keys {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO claude_usage_copies(usage_key,conversation_id,workspace_id)
				SELECT ?,id,workspace_id FROM conversations WHERE id=?`, key, conversationIDs[index]); err != nil {
				return err
			}
			touched[key] = true
		}
	}
	if len(touched) == 0 {
		return nil
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
	// Each holder's records are read once, for every key it shares.
	decisions, workspaceOf := map[string]map[string]string{}, map[string]string{}
	for _, key := range keys {
		list := holders[key]
		if len(list) == 0 || len(list) == 1 && list[0].workspace == workspaceID {
			continue
		}
		owner := claudeUsageOwner(list)
		for _, holder := range list {
			copyOf := ""
			if holder.conversation != owner.conversation {
				copyOf = owner.native
			}
			if decisions[holder.conversation] == nil {
				decisions[holder.conversation] = map[string]string{}
			}
			decisions[holder.conversation][key], workspaceOf[holder.conversation] = copyOf, holder.workspace
		}
	}
	conversations := make([]string, 0, len(decisions))
	for conversation := range decisions {
		conversations = append(conversations, conversation)
	}
	sort.Strings(conversations)
	changed := map[string]bool{}
	for _, conversation := range conversations {
		updated, err := settleStoredUsage(tx, conversation, decisions[conversation])
		if err != nil {
			return err
		}
		if updated {
			changed[workspaceOf[conversation]] = true
		}
	}
	workspaces := make([]string, 0, len(changed))
	for workspace := range changed {
		workspaces = append(workspaces, workspace)
	}
	sort.Strings(workspaces)
	for _, workspace := range workspaces {
		if err := rebuildWorkspaceUsage(ctx, tx, workspace); err != nil {
			return err
		}
	}
	return nil
}

// settleStoredUsage makes a stored conversation's usage records of each key
// in copyOf a copy of the conversation it names there, or, where that is "",
// its own. It reports whether any changed.
func settleStoredUsage(tx *sql.Tx, conversationID string, copyOf map[string]string) (bool, error) {
	rows, err := tx.Query(`SELECT m.id,m.native_id,COALESCE(m.raw_text,''),c.account FROM messages m JOIN conversations c ON c.id=m.conversation_id
		WHERE m.conversation_id=? AND m.kind='metadata' AND (m.native_id LIKE '%:usage' OR m.native_id LIKE '%:usage:advisor:%')`, conversationID)
	if err != nil {
		return false, err
	}
	type update struct{ id, raw string }
	updates := []update{}
	for rows.Next() {
		var id, native, raw, account string
		if err := rows.Scan(&id, &native, &raw, &account); err != nil {
			rows.Close()
			return false, err
		}
		owner, ok := copyOf[claudeCrossKey(account, MessageRecord{NativeID: native, Kind: "metadata", RawText: raw})]
		if !ok {
			continue
		}
		want, _ := unwrapUsageCopy(raw)
		if owner != "" {
			want = wrapUsageCopy(want, owner)
		}
		if want != raw {
			updates = append(updates, update{id, want})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	for _, u := range updates {
		if _, err := tx.Exec("UPDATE messages SET raw_text=? WHERE id=?", u.raw, u.id); err != nil {
			return false, err
		}
	}
	return len(updates) > 0, nil
}

// repairClaudeUsageCopies settles the copies of a workspace's stored Claude
// conversations, as their parse would now: within each session, then across
// sessions. Claude Code removes old transcripts, so a conversation's stored
// messages may be all that is left of it.
func repairClaudeUsageCopies(ctx context.Context, tx *sql.Tx, workspaceID string) error {
	conversations, ids, err := storedWorkspaceConversations(ctx, tx, workspaceID)
	if err != nil {
		return err
	}
	stored := map[string]string{}
	for index, conversation := range conversations {
		stored[conversation.NativeID] = ids[index]
	}
	for _, root := range conversations {
		if root.Provider != "claude" || root.AgentDepth != 0 {
			continue
		}
		group := WorkspaceRecord{Conversations: append([]ConversationRecord{root}, claudeChildren(conversations, root)...)}
		before := make([][]string, len(group.Conversations))
		for c, conversation := range group.Conversations {
			// The group's records share their messages with conversations.
			group.Conversations[c].Messages = slices.Clone(conversation.Messages)
			for _, message := range conversation.Messages {
				before[c] = append(before[c], message.RawText)
			}
		}
		dedupeClaudeGroupUsage(&group)
		for c, conversation := range group.Conversations {
			for m, message := range conversation.Messages {
				if message.RawText != before[c][m] {
					if _, err := tx.Exec("UPDATE messages SET raw_text=? WHERE conversation_id=? AND native_id=?", message.RawText, stored[conversation.NativeID], message.NativeID); err != nil {
						return err
					}
				}
			}
		}
	}
	held, err := claudeHeldKeys(tx, workspaceID)
	if err != nil {
		return err
	}
	conversations, ids, err = storedWorkspaceConversations(ctx, tx, workspaceID)
	if err != nil {
		return err
	}
	return settleClaudeUsage(ctx, tx, WorkspaceRecord{Conversations: conversations}, workspaceID, ids, held)
}

// rebuildWorkspaceUsage derives a workspace's sessions, hourly usage, model
// requests and token metrics again from its stored messages, whatever they
// held before: a reply it counted may now be another's.
func rebuildWorkspaceUsage(ctx context.Context, tx *sql.Tx, workspaceID string) error {
	conversations, ids, err := storedWorkspaceConversations(ctx, tx, workspaceID)
	if err != nil {
		return err
	}
	for j, conversation := range conversations {
		if err := replaceAgentSessions(tx, workspaceID, ids[j], conversation); err != nil {
			return err
		}
		if err := replaceToolLedger(tx, workspaceID, ids[j], conversation); err != nil {
			return err
		}
	}
	if err := applyClaudeGroupUsage(tx, workspaceID, conversations, ids, nil); err != nil {
		return err
	}
	if metrics := reconciledTokenMetrics(WorkspaceRecord{Conversations: conversations}); len(metrics) > 0 {
		if _, err := tx.Exec("DELETE FROM metrics WHERE workspace_id=? AND unit='tokens'", workspaceID); err != nil {
			return err
		}
		return replaceMetrics(tx, workspaceID, metrics)
	}
	return nil
}

// storedWorkspaceConversations reads a workspace's conversations back from
// the catalog, as the usage derivations need them.
func storedWorkspaceConversations(ctx context.Context, q queryer, workspaceID string) ([]ConversationRecord, []string, error) {
	items, err := queryMapsContext(ctx, q, `SELECT id,native_id,parent_id,provider,account,model,effort,agent_depth,started_at FROM conversations
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
			Model: firstString(item["model"]), Effort: firstString(item["effort"]), AgentDepth: int(integer(item["agent_depth"])), StartedAt: firstString(item["started_at"]), Messages: messages})
	}
	return conversations, ids, nil
}
