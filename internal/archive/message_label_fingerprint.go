package archive

import (
	"net/http"
	"strings"
)

var messageLabelFingerprintLimit = 2400

// Bounded ribbon evidence, fetched by conversation identity only after the
// query page knows which conversations are visible. No message bodies are sent.
func (s *Server) getMessageLabelFingerprint(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/labels/conversations/")
	rows, err := queryMapsContext(r.Context(), s.Catalog.reader(), `SELECT c.id,c.workspace_id work_id,COALESCE(NULLIF(c.agent_nickname,''),w.title,c.id) title,c.provider,repo.display_name repository,c.started_at FROM conversations c LEFT JOIN workspaces w ON w.id=c.workspace_id LEFT JOIN repositories repo ON repo.id=w.repository_id WHERE c.id=?`, id)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	if len(rows) == 0 {
		writeJSON(w, map[string]any{"error": "conversation not found"}, 404)
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
 WHERE m.conversation_id=? AND m.selected=1 AND m.kind IN ('message','thinking','reasoning','delegation','delegation_result')
 ORDER BY m.created_at,m.source_order,m.id LIMIT ?`, id, messageLabelFingerprintLimit+1)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	truncated := len(messages) > messageLabelFingerprintLimit
	if truncated {
		messages = messages[:messageLabelFingerprintLimit]
	}
	messageIDs := make([]string, 0, len(messages))
	for _, message := range messages {
		messageIDs = append(messageIDs, firstString(message["id"]))
	}
	attach, summary, err := s.Catalog.messageLabelsAttacherContext(r.Context(), id, messageIDs)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	meta, err := summary()
	if err != nil {
		writeError(w, err, 500)
		return
	}
	pending := meta != nil && meta["pending"] == true
	sessions, err := queryMapsContext(r.Context(), s.Catalog.reader(), `SELECT id,parent_id,conversation_id,workspace_id work_id,kind,depth,provider,model,native_id name,native_id FROM agent_sessions WHERE conversation_id=? ORDER BY depth,id`, id)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	main := id
	for _, session := range sessions {
		session["link_evidence"] = "Recorded session"
		if session["native_id"] == "main" {
			main = firstString(session["id"])
		}
		delete(session, "native_id")
	}
	if len(sessions) == 0 {
		sessions = []map[string]any{{"id": id, "parent_id": nil, "conversation_id": id, "work_id": conversation["work_id"], "kind": "root", "depth": 0, "name": "Main agent", "link_evidence": "Recorded conversation"}}
	}
	for _, message := range messages {
		if firstString(message["session_id"]) == "" {
			message["session_id"] = main
		}
		message["labels"] = []any{}
		message["segment"] = ""
		message["text"] = ""
		message["text_source"] = "Ribbon projection"
		message["truncated"] = false
		message["work_id"] = conversation["work_id"]
		if pending {
			message["chunks"] = 0
			continue
		}
		attach(message)
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
	conversation["main_session"] = main
	conversation["messages"] = messages
	conversation["sessions"] = sessions
	taxonomy := []map[string]any{}
	if meta != nil {
		if value, ok := meta["taxonomy"].([]map[string]any); ok {
			taxonomy = value
		}
	}
	// Agent events with their targets (agent_event_links.go): message ids,
	// kinds, short sentences and target excerpts; still no message bodies.
	events, err := s.Catalog.agentEventLinks(r.Context(), id)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	if events == nil {
		events = []map[string]any{}
	}
	// Avoid a stale partial ribbon being cached as if it were the full transcript.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"conversation": conversation, "taxonomy": taxonomy, "truncated": truncated, "pending": pending, "agent_events": events}, 200)
}
