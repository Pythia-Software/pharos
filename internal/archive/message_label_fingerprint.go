package archive

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strings"
)

var messageLabelFingerprintLimit = 2400

// Bounded ribbon evidence, fetched by entity identity only when its column is
// visible. A workspace combines its conversations in recorded time order.
// No message bodies are sent and reading never starts labeling.
func (s *Server) getMessageLabelFingerprint(w http.ResponseWriter, r *http.Request) {
	workspace := strings.HasPrefix(r.URL.Path, "/api/labels/workspaces/")
	prefix, entity := "/api/labels/conversations/", "conversation"
	if workspace {
		prefix, entity = "/api/labels/workspaces/", "workspace"
	}
	value, err := s.Catalog.messageLabelFingerprintInput(r.Context(), strings.TrimPrefix(r.URL.Path, prefix), workspace, messageLabelFingerprintLimit, false)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, map[string]any{"error": entity + " not found"}, 404)
		return
	}
	if err != nil {
		writeError(w, err, 500)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, value, 200)
}

// limit=0 reads the full retained evidence for background image generation.
// The public evidence endpoint keeps its existing bounded, read-only contract.
func (c *Catalog) messageLabelFingerprintInput(ctx context.Context, id string, workspace bool, limit int, includeTools bool) (map[string]any, error) {
	scope := "c.id=?"
	metadataSQL := `SELECT c.id,c.workspace_id work_id,COALESCE(NULLIF(c.agent_nickname,''),w.title,c.id) title,c.provider,repo.display_name repository,c.started_at FROM conversations c LEFT JOIN workspaces w ON w.id=c.workspace_id LEFT JOIN repositories repo ON repo.id=w.repository_id WHERE c.id=?`
	if workspace {
		scope = "c.workspace_id=?"
		metadataSQL = `SELECT w.id,w.id work_id,w.title,repo.display_name repository FROM workspaces w LEFT JOIN repositories repo ON repo.id=w.repository_id WHERE w.id=?`
	}
	rows, err := queryMapsContext(ctx, c.reader(), metadataSQL, id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, sql.ErrNoRows
	}
	conversation := rows[0]
	labelJoin := "LEFT JOIN message_labels l ON l.message_id=m.id"
	var hasLabels int
	if err := c.reader().QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='message_labels'").Scan(&hasLabels); err != nil {
		return nil, err
	}
	if hasLabels == 0 {
		labelJoin = "LEFT JOIN (SELECT NULL message_id,0 chunks WHERE 0) l ON 0"
	}
	query := `SELECT m.id,m.conversation_id,m.role,m.kind,m.created_at at,l.chunks,
 (SELECT agent_session_id FROM agent_session_messages am WHERE am.message_id=m.id ORDER BY agent_session_id LIMIT 1) session_id
 FROM messages m ` + labelJoin + `
 WHERE m.conversation_id IN (SELECT c.id FROM conversations c WHERE ` + scope + `) AND m.selected=1 AND m.kind IN ('message','thinking','reasoning','delegation','delegation_result')
 ORDER BY m.created_at,m.source_order,m.id`
	args := []any{id}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit+1)
	}
	messages, err := queryMapsContext(ctx, c.reader(), query, args...)
	if err != nil {
		return nil, err
	}
	truncated := limit > 0 && len(messages) > limit
	if truncated {
		messages = messages[:limit]
	}
	// Each conversation owns its label freshness. A dirty sibling must not
	// hide accepted labels from the rest of the workspace.
	byConversation := map[string][]string{}
	for _, message := range messages {
		key := firstString(message["conversation_id"])
		byConversation[key] = append(byConversation[key], firstString(message["id"]))
	}
	if includeTools {
		tools, err := queryMapsContext(ctx, c.reader(), `SELECT id,conversation_id,agent_session_id session_id,call_message_id message_id,tool_name name,tool_category category,command_category,status,started_at start,ended_at end,duration_ms FROM tool_calls WHERE conversation_id IN (SELECT c.id FROM conversations c WHERE `+scope+`) ORDER BY sequence,id`, id)
		if err != nil {
			return nil, err
		}
		conversation["activity"] = tools
		for _, tool := range tools {
			tool["category"] = delegationCategory(firstString(tool["name"]), firstString(tool["category"]), firstString(tool["command_category"]))
			key := firstString(tool["conversation_id"])
			if _, ok := byConversation[key]; !ok {
				byConversation[key] = []string{}
			}
		}
	}
	keys := make([]string, 0, len(byConversation))
	for key := range byConversation {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	attachers := map[string]func(map[string]any){}
	pendingConversations := map[string]bool{}
	taxonomy, knownTaxonomy := []map[string]any{}, map[string]bool{}
	pending := false
	for _, key := range keys {
		messageIDs := byConversation[key]
		attach, summary, err := c.messageLabelsAttacherContext(ctx, key, messageIDs)
		if err != nil {
			return nil, err
		}
		meta, err := summary()
		if err != nil {
			return nil, err
		}
		attachers[key] = attach
		if meta != nil {
			pendingConversations[key] = meta["pending"] == true
			pending = pending || pendingConversations[key]
			if definitions, ok := meta["taxonomy"].([]map[string]any); ok {
				for _, definition := range definitions {
					key := firstString(definition["id"])
					if !knownTaxonomy[key] {
						knownTaxonomy[key] = true
						taxonomy = append(taxonomy, definition)
					}
				}
			}
		}
	}
	// Only branches represented in the bounded sample (and their recorded
	// ancestors) are needed. Workspaces can retain arbitrarily many sessions.
	sessionIDs := []string{}
	for _, message := range messages {
		if session := firstString(message["session_id"]); session != "" {
			sessionIDs = append(sessionIDs, session)
		}
	}
	if includeTools {
		for _, tool := range conversation["activity"].([]map[string]any) {
			if session := firstString(tool["session_id"]); session != "" {
				sessionIDs = append(sessionIDs, session)
			}
		}
	}
	sessions, err := queryMapsContext(ctx, c.reader(), `WITH RECURSIVE represented(id) AS (
 SELECT id FROM agent_sessions WHERE conversation_id IN (SELECT value FROM json_each(?)) AND (native_id='main' OR id IN (SELECT value FROM json_each(?)))
 UNION SELECT a.parent_id FROM agent_sessions a JOIN represented r ON a.id=r.id WHERE a.parent_id IS NOT NULL
 ) SELECT id,parent_id,conversation_id,workspace_id work_id,kind,depth,provider,model,native_id name,native_id FROM agent_sessions WHERE id IN (SELECT id FROM represented) ORDER BY depth,id`, jsonText(keys), jsonText(sessionIDs))
	if err != nil {
		return nil, err
	}
	mains := map[string]string{}
	for key := range byConversation {
		mains[key] = key
	}
	for _, session := range sessions {
		session["link_evidence"] = "Recorded session"
		if session["native_id"] == "main" {
			mains[firstString(session["conversation_id"])] = firstString(session["id"])
		}
		delete(session, "native_id")
	}
	for _, key := range keys {
		main := mains[key]
		found := false
		for _, session := range sessions {
			found = found || session["id"] == main
		}
		if !found {
			sessions = append(sessions, map[string]any{"id": main, "parent_id": nil, "conversation_id": key, "work_id": conversation["work_id"], "kind": "root", "depth": 0, "name": "Main agent", "link_evidence": "Recorded conversation"})
		}
	}
	for _, message := range messages {
		if firstString(message["session_id"]) == "" {
			message["session_id"] = mains[firstString(message["conversation_id"])]
		}
		message["labels"] = []any{}
		message["segment"] = ""
		message["text"] = ""
		message["text_source"] = "Ribbon projection"
		message["truncated"] = false
		message["work_id"] = conversation["work_id"]
		if pendingConversations[firstString(message["conversation_id"])] {
			message["chunks"] = 0
			continue
		}
		attachers[firstString(message["conversation_id"])](message)
		if value, ok := message["labels"].(map[string]any); ok {
			labels := []any{}
			probs, _ := value["probs"].(map[string]float64)
			ids, _ := value["ids"].([]string)
			for _, label := range ids {
				labels = append(labels, []any{label, probs[label]})
			}
			message["segment"] = value["segment"]
			message["labels"] = labels
		}
	}
	if includeTools {
		for _, tool := range conversation["activity"].([]map[string]any) {
			if firstString(tool["session_id"]) == "" {
				tool["session_id"] = mains[firstString(tool["conversation_id"])]
			}
		}
	}
	conversation["main_session"] = mains[id]
	conversation["messages"] = messages
	conversation["sessions"] = sessions
	// Every represented conversation contributes its sampled agent events,
	// resolved against its own earlier text, including in workspace ribbons.
	events := []map[string]any{}
	for _, key := range keys {
		found, err := c.agentEventLinksForMessages(ctx, key, byConversation[key])
		if err != nil {
			return nil, err
		}
		events = append(events, found...)
	}
	return map[string]any{"conversation": conversation, "taxonomy": taxonomy, "truncated": truncated, "pending": pending, "agent_events": events}, nil
}
