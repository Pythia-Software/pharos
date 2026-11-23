package archive

import "context"

// TL1's stream and Antigravity's native transcript are separate retained
// evidence. The explicit native alias joins them; filenames, titles and times
// never establish identity. Keep TL1's workflow envelope while reading native
// content when it exists. No messages or provenance are moved or overwritten.
const tl1NativeEvidenceSQL = `SELECT t.id tl1_id,t.workspace_id tl1_workspace_id,n.id native_id,n.workspace_id native_workspace_id
 FROM conversations t JOIN workspaces tw ON tw.id=t.workspace_id
 JOIN declared_native_aliases a ON a.conversation_id=t.id
 JOIN conversations n ON n.account=a.account AND n.native_id=a.native_id AND n.provider='antigravity'
 JOIN workspaces nw ON nw.id=n.workspace_id
 WHERE tw.source_kind='tl1' AND t.provider='antigravity' AND nw.source_kind='antigravity'
 AND EXISTS(SELECT 1 FROM messages m WHERE m.conversation_id=n.id AND m.kind IN ('message','tool_call','tool_result','reasoning'))`

const tl1NativeUsageSQL = tl1NativeEvidenceSQL + ` AND EXISTS(
 SELECT 1 FROM agent_sessions s JOIN agent_session_usage u ON u.agent_session_id=s.id
 WHERE s.conversation_id=n.id AND u.is_estimate=0 AND u.total_tokens>0)`

// A resumed/reused native ID can span several TL1 runs. The native store's
// aggregate cannot be assigned in full to each attempt. Keep each stream's
// scoped counters and content in that case; archive-wide usage still chooses
// the native conversation once.
const tl1SharedNativeSQL = `EXISTS(SELECT 1 FROM declared_native_aliases other
 JOIN conversations ot ON ot.id=other.conversation_id JOIN workspaces ow ON ow.id=ot.workspace_id
 WHERE other.account=a.account AND other.native_id=a.native_id AND other.conversation_id<>t.id AND ow.source_kind='tl1')`

const tl1UnambiguousNativeSQL = tl1NativeEvidenceSQL + ` AND NOT ` + tl1SharedNativeSQL

func (c *Catalog) tl1NativeEvidence(ctx context.Context, requireUsage bool) (map[string]string, error) {
	query, key := tl1NativeEvidenceSQL, "tl1-native-evidence"
	if requireUsage {
		query, key = tl1NativeUsageSQL, "tl1-native-usage"
	}
	return cachedValue(ctx, c, key, func(ctx context.Context) (map[string]string, error) {
		rows, err := queryMapsContext(ctx, c.reader(), query)
		if err != nil {
			return nil, err
		}
		result := map[string]string{}
		for _, row := range rows {
			result[firstString(row["tl1_id"])] = firstString(row["native_id"])
		}
		return result, nil
	})
}

func (c *Catalog) tl1ContentConversation(ctx context.Context, id string) (string, error) {
	// A targeted lookup also works before a cache generation has been built.
	rows, err := queryMapsContext(ctx, c.reader(), tl1UnambiguousNativeSQL+" AND t.id=?", id)
	if err != nil || len(rows) == 0 {
		return id, err
	}
	return firstString(rows[0]["native_id"]), nil
}

func (c *Catalog) preferTL1NativeContent(ctx context.Context, conversations []map[string]any) error {
	for index, conversation := range conversations {
		if firstString(conversation["provider"]) != "antigravity" {
			continue
		}
		id := firstString(conversation["id"])
		native, err := c.tl1ContentConversation(ctx, id)
		if err != nil {
			return err
		}
		if native == id {
			continue
		}
		rows, err := queryMapsContext(ctx, c.reader(), "SELECT c.*,c.id conversation_id FROM conversations c WHERE c.id=?", native)
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			rows[0]["workflow_conversation_id"] = id
			rows[0]["workflow_workspace_id"] = conversation["workspace_id"]
			conversations[index] = rows[0]
		}
	}
	return nil
}

// A native session with no saved cwd still belongs to the project TL1
// explicitly recorded for it. Conflicting project claims remain unresolved.
func tl1EvidenceProject(ctx context.Context, q queryer, workspace string) (string, error) {
	rows, err := queryMapsContext(ctx, q, `SELECT DISTINCT tw.repository_id FROM (`+tl1NativeEvidenceSQL+`) e
		JOIN workspaces tw ON tw.id=e.tl1_workspace_id
		WHERE e.native_workspace_id=? AND tw.repository_id IS NOT NULL`, workspace)
	if err != nil || len(rows) != 1 {
		return "", err
	}
	return firstString(rows[0]["repository_id"]), nil
}

type tl1TranscriptEvidence struct {
	ConversationID, NativeConversationID, SessionID string
	Shared, Readable                                bool
}

func (c *Catalog) tl1TranscriptEvidence(ctx context.Context, installation string) (map[string]*tl1TranscriptEvidence, error) {
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT t.native_id,t.id conversation_id,a.native_id session_id,n.id native_conversation_id,`+tl1SharedNativeSQL+` shared_native_id,
		EXISTS(SELECT 1 FROM messages m WHERE m.conversation_id=n.id AND m.kind IN ('message','tool_call','tool_result','reasoning')) readable
		FROM conversations t JOIN workspaces w ON w.id=t.workspace_id
		LEFT JOIN declared_native_aliases a ON a.conversation_id=t.id
		LEFT JOIN conversations n ON n.account=a.account AND n.native_id=a.native_id AND n.provider='antigravity'
			AND EXISTS(SELECT 1 FROM workspaces nw WHERE nw.id=n.workspace_id AND nw.source_kind='antigravity')
		WHERE w.source_kind='tl1' AND t.provider='antigravity' AND t.native_id>=? AND t.native_id<?`, installation+":", installation+":￿")
	if err != nil {
		return nil, err
	}
	result := map[string]*tl1TranscriptEvidence{}
	for _, row := range rows {
		key := firstString(row["native_id"])
		ref := result[key]
		if ref == nil {
			ref = &tl1TranscriptEvidence{ConversationID: firstString(row["conversation_id"])}
			result[key] = ref
		}
		// Antigravity session IDs are lowercase UUIDs, not the basename
		// used by a stream that ended before init.
		session := firstString(row["session_id"])
		if len(session) == 36 {
			antigravityLogIDs([]byte(session), func(start, end int) {
				if start == 0 && end == len(session) {
					ref.SessionID = session
					ref.NativeConversationID = firstString(row["native_conversation_id"])
					ref.Shared = integer(row["shared_native_id"]) != 0
					ref.Readable = integer(row["readable"]) != 0
				}
			})
		}
	}
	return result, nil
}

func tl1NativeCoverage(data *tl1Data) map[string]any {
	matched, referenced := 0, 0
	missing, noKey := []map[string]any{}, []map[string]any{}
	shared := []map[string]any{}
	unique := map[string]bool{}
	for _, attempt := range data.Attempts {
		if !matchesFold(attempt.Executor, "agy", "antigravity") && !tl1AGYTranscript(attempt.Conversation) {
			continue
		}
		row := map[string]any{"installation_id": attempt.Task.Installation["installation_id"], "task_id": attempt.TaskID, "attempt_id": attempt.ID}
		ref := attempt.Evidence
		if ref == nil || ref.SessionID == "" {
			noKey = append(noKey, row)
			continue
		}
		referenced++
		unique[ref.SessionID] = true
		row["native_session_id"] = ref.SessionID
		if ref.Shared {
			shared = append(shared, row)
		}
		if ref.NativeConversationID == "" {
			missing = append(missing, row)
		} else {
			matched++
		}
	}
	return map[string]any{"scope": "indexed TL1 attempts in the selected window", "referenced": referenced, "unique_native_sessions": len(unique), "matched": matched, "missing": missing, "without_join_key": noKey, "shared_native_sessions": shared}
}
