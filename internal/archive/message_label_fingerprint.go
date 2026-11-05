package archive

import (
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
	prefix, scope, entity := "/api/labels/conversations/", "c.id=?", "conversation"
	metadataSQL := `SELECT c.id,c.workspace_id work_id,COALESCE(NULLIF(c.agent_nickname,''),w.title,c.id) title,c.provider,repo.display_name repository,c.started_at FROM conversations c LEFT JOIN workspaces w ON w.id=c.workspace_id LEFT JOIN repositories repo ON repo.id=w.repository_id WHERE c.id=?`
	if workspace {
		prefix, scope, entity = "/api/labels/workspaces/", "c.workspace_id=?", "workspace"
		metadataSQL = `SELECT w.id,w.id work_id,w.title,repo.display_name repository FROM workspaces w LEFT JOIN repositories repo ON repo.id=w.repository_id WHERE w.id=?`
	}
	id := strings.TrimPrefix(r.URL.Path, prefix)
	rows, err := queryMapsContext(r.Context(), s.Catalog.reader(), metadataSQL, id)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	if len(rows) == 0 {
		writeJSON(w, map[string]any{"error": entity + " not found"}, 404)
		return
	}
	conversation := rows[0]
	labelJoin := "LEFT JOIN message_labels l ON l.message_id=m.id"
	var hasLabels int
	if err := s.Catalog.reader().QueryRowContext(r.Context(), "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='message_labels'").Scan(&hasLabels); err != nil {
		writeError(w, err, 500)
		return
	}
	if hasLabels == 0 {
		labelJoin = "LEFT JOIN (SELECT NULL message_id,0 chunks WHERE 0) l ON 0"
	}
	messages, err := queryMapsContext(r.Context(), s.Catalog.reader(), `SELECT m.id,m.conversation_id,m.role,m.kind,m.created_at at,l.chunks,
 (SELECT agent_session_id FROM agent_session_messages am WHERE am.message_id=m.id ORDER BY agent_session_id LIMIT 1) session_id
 FROM messages m `+labelJoin+`
 WHERE m.conversation_id IN (SELECT c.id FROM conversations c WHERE `+scope+`) AND m.selected=1 AND m.kind IN ('message','thinking','reasoning','delegation','delegation_result')
 ORDER BY m.created_at,m.source_order,m.id LIMIT ?`, id, messageLabelFingerprintLimit+1)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	truncated := len(messages) > messageLabelFingerprintLimit
	if truncated {
		messages = messages[:messageLabelFingerprintLimit]
	}
	// Each conversation owns its label freshness. A dirty sibling must not
	// hide accepted labels from the rest of the workspace.
	byConversation := map[string][]string{}
	for _, message := range messages {
		key := firstString(message["conversation_id"])
		byConversation[key] = append(byConversation[key], firstString(message["id"]))
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
		attach, summary, err := s.Catalog.messageLabelsAttacherContext(r.Context(), key, messageIDs)
		if err != nil {
			writeError(w, err, 500)
			return
		}
		meta, err := summary()
		if err != nil {
			writeError(w, err, 500)
			return
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
	sessions, err := queryMapsContext(r.Context(), s.Catalog.reader(), `WITH RECURSIVE represented(id) AS (
 SELECT id FROM agent_sessions WHERE conversation_id IN (SELECT value FROM json_each(?)) AND (native_id='main' OR id IN (SELECT value FROM json_each(?)))
 UNION SELECT a.parent_id FROM agent_sessions a JOIN represented r ON a.id=r.id WHERE a.parent_id IS NOT NULL
 ) SELECT id,parent_id,conversation_id,workspace_id work_id,kind,depth,provider,model,native_id name,native_id FROM agent_sessions WHERE id IN (SELECT id FROM represented) ORDER BY depth,id`, jsonText(keys), jsonText(sessionIDs))
	if err != nil {
		writeError(w, err, 500)
		return
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
	conversation["main_session"] = mains[id]
	conversation["messages"] = messages
	conversation["sessions"] = sessions
	// Avoid a stale partial ribbon being cached as if it were the full transcript.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"conversation": conversation, "taxonomy": taxonomy, "truncated": truncated, "pending": pending}, 200)
}
