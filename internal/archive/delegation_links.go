package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Launch acknowledgements identify transcripts, not additional executions.
// Codex task paths are only unique within their recorded parent conversation.
type delegationHandle struct {
	agent, path string
	failed      bool
}

func readDelegationHandle(text string) delegationHandle {
	var payload map[string]any
	if json.Unmarshal([]byte(text), &payload) != nil {
		return delegationHandle{}
	}
	// Error fields in a decoded subagent report describe the task outcome.
	// Treat error fields as launch failures only on the result envelope.
	h := delegationHandle{failed: truthy(payload["is_error"]) || truthy(payload["error"])}
	var read func(any, int)
	read = func(value any, depth int) {
		if depth > 4 {
			return
		}
		switch v := value.(type) {
		case map[string]any:
			h.agent = firstString(h.agent, v["agentId"], v["agent_id"], v["thread_id"])
			if path := firstString(v["task_name"]); strings.HasPrefix(path, "/") {
				h.path = path
			}
			for _, key := range []string{"details", "content", "output", "text"} {
				read(v[key], depth+1)
			}
		case []any:
			for _, part := range v {
				read(part, depth+1)
			}
		case string:
			var decoded any
			if json.Unmarshal([]byte(v), &decoded) == nil {
				read(decoded, depth+1)
				return
			}
			// These are harness failure acknowledgements, not arbitrary agent prose.
			h.failed = h.failed || strings.HasPrefix(v, "collab spawn failed:") || (strings.HasPrefix(v, "agent path `") && strings.HasSuffix(v, "` already exists"))
			if _, after, ok := strings.Cut(v, "agentId:"); ok {
				if fields := strings.Fields(after); len(fields) > 0 {
					h.agent = firstString(h.agent, fields[0])
				}
			}
		}
	}
	read(payload, 0)
	// A returned handle establishes that a child was created, even if its task
	// failed. Keep the exact launch relationship in that case.
	h.failed = h.failed && h.agent == "" && h.path == ""
	return h
}

type delegationLinks struct {
	aliases  map[string]string
	external map[string]*delegationReference
	failed   map[string]bool
	extra    []map[string]any
}

func loadDelegationLinks(ctx context.Context, tx *sql.Tx, workspace string, conversations, sessions []map[string]any, roots map[string]string) (*delegationLinks, error) {
	family, err := loadDelegationFamily(ctx, tx, workspace)
	if err != nil {
		return nil, err
	}

	outside := []map[string]any{}
	for _, cv := range family {
		if firstString(cv["workspace_id"]) != workspace {
			outside = append(outside, cv)
		}
	}
	return resolveDelegationLinks(ctx, tx, workspace, conversations, sessions, roots, outside)
}

var errDelegationFamilyLimit = errors.New("workspace exceeds the 10,000-conversation family analysis limit")

func loadDelegationFamily(ctx context.Context, tx *sql.Tx, workspace string) ([]map[string]any, error) {
	// Child transcripts can live in other workspaces. Follow both recorded
	// parents and inferred command launches, including mixed-provider chains.
	// Recorded parents take precedence over inferred launches.
	family, err := queryMapsContext(ctx, tx, `WITH RECURSIVE family(id) AS (
 SELECT id FROM conversations WHERE workspace_id=?
 UNION SELECT c.id FROM family p JOIN conversations c ON c.parent_id=p.id WHERE c.agent_depth>0
 UNION SELECT c.id FROM family p JOIN conversation_launches l ON l.parent_id=p.id
 JOIN conversations c ON c.id=l.child_id WHERE c.parent_id IS NULL LIMIT 10001)
 SELECT c.id,c.workspace_id,c.native_id,c.parent_id,c.provider,c.account,c.agent_path,c.agent_nickname,c.model,c.effort,c.coverage,
 l.parent_id launch_parent_id,t.call_message_id launch_message_id
 FROM family f JOIN conversations c ON c.id=f.id
 LEFT JOIN conversation_launches l ON l.child_id=c.id
 LEFT JOIN tool_calls t ON t.id=l.tool_call_id ORDER BY c.started_at,c.id`, workspace)
	if err != nil {
		return nil, err
	}
	if len(family) > 10000 {
		return nil, errDelegationFamilyLimit
	}
	return family, nil
}

// Share acknowledgement matching with incoming ancestry so ambiguity and
// provider/account/path checks agree in both directions.
func resolveDelegationLinks(ctx context.Context, tx *sql.Tx, workspace string, conversations, sessions []map[string]any, roots map[string]string, outside []map[string]any) (*delegationLinks, error) {
	links := &delegationLinks{aliases: map[string]string{}, external: map[string]*delegationReference{}, failed: map[string]bool{}}
	byConversation := map[string]map[string]any{}
	for _, cv := range conversations {
		byConversation[firstString(cv["id"])] = cv
	}
	candidates := append(append([]map[string]any{}, conversations...), outside...)
	outsideByID := map[string]map[string]any{}
	for _, cv := range outside {
		outsideByID[firstString(cv["id"])] = cv
	}
	claimed := map[string]bool{}
	representatives := map[string]string{}
	for cid, id := range roots {
		representatives[cid] = id
	}
	bind := func(row, cv map[string]any) {
		id, child := firstString(row["id"]), firstString(cv["id"])
		claimed[child] = true
		if root := roots[child]; root != "" {
			links.aliases[id] = root
			return
		}
		if prior := representatives[child]; prior != "" && prior != id {
			links.aliases[id] = prior
			return
		}
		representatives[child] = id
		row["parent_evidence"] = "exact-delegation-handle"
		links.external[id] = &delegationReference{ConversationID: child, WorkspaceID: firstString(cv["workspace_id"]), Provider: firstString(cv["provider"]), Label: firstString(cv["agent_nickname"], cv["agent_path"], cv["native_id"])}
		row["child_model"], row["child_effort"], row["child_coverage"] = cv["model"], cv["effort"], cv["coverage"]
	}
	for _, row := range sessions {
		if child := firstString(row["child_conversation_id"]); child != "" {
			claimed[child] = true
			if cv := byConversation[child]; cv != nil {
				bind(row, cv)
			} else if cv := outsideByID[child]; cv != nil {
				bind(row, cv)
			}
		}
	}
	evidence, err := queryMapsContext(ctx, tx, delegationResultEvidenceSQL, workspace)
	if err != nil {
		return nil, err
	}
	// Reconcile all results before binding: conflicting acknowledgements must not
	// make the hierarchy depend on SQLite row order.
	handles := map[string]delegationHandle{}
	ambiguous := map[string]bool{}
	for _, e := range evidence {
		id := firstString(e["id"])
		h := readDelegationHandle(firstString(e["text"]))
		if h.agent == "" && h.path == "" && !h.failed {
			continue
		}
		if prior, ok := handles[id]; ok && prior != h {
			ambiguous[id] = true
		}
		handles[id] = h
	}
	for _, row := range sessions {
		id := firstString(row["id"])
		h, ok := handles[id]
		if !ok || ambiguous[id] {
			continue
		}
		if h.failed {
			// A failed launch with independently recorded execution evidence is kept.
			if integer(row["total_tokens"]) == 0 {
				var observed bool
				err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_session_messages WHERE agent_session_id=?) OR EXISTS(SELECT 1 FROM model_requests WHERE agent_session_id=?) OR EXISTS(SELECT 1 FROM tool_calls WHERE agent_session_id=?)`, id, id, id).Scan(&observed)
				if err != nil {
					return nil, err
				}
				links.failed[id] = !observed
			}
			continue
		}
		parent := firstString(row["conversation_id"])
		source := byConversation[parent]
		var matched map[string]any
		count := 0
		for _, cv := range candidates {
			exact := false
			if h.path != "" {
				exact = firstString(cv["agent_path"]) == h.path && firstString(cv["parent_id"]) == parent && cv["provider"] == source["provider"] && cv["account"] == source["account"]
			} else if h.agent != "" {
				native := firstString(cv["native_id"])
				exact = strings.HasSuffix(native, "agent-"+h.agent) || (native == h.agent && cv["provider"] == source["provider"] && cv["account"] == source["account"])
			}
			if exact {
				matched = cv
				count++
			}
		}
		if count == 1 {
			bind(row, matched)
		}
	}
	for _, cv := range outside {
		child := firstString(cv["id"])
		if claimed[child] {
			continue
		}
		id := "external-conversation:" + child
		parent, proof, message := firstString(cv["parent_id"]), "conversation-parent", ""
		if parent == "" {
			parent, proof, message = firstString(cv["launch_parent_id"]), "inferred-command-launch", firstString(cv["launch_message_id"])
		}
		row := map[string]any{"id": id, "native_id": id, "conversation_id": parent, "provider": cv["provider"], "usage_status": "unavailable", "assignment": firstString(cv["agent_nickname"], cv["agent_path"], cv["native_id"]), "delegation_message_id": message}
		bind(row, cv)
		row["parent_evidence"] = proof
		links.extra = append(links.extra, row)
	}
	for _, row := range links.extra {
		row["parent_id"] = representatives[firstString(row["conversation_id"])]
	}
	return links, nil
}

// A separately archived subagent is a workspace root for accounting, but is
// still nested in its execution family. Resolve each incoming edge using the
// retained launch evidence before falling back to its transcript parent.
func loadDelegationAncestors(ctx context.Context, tx *sql.Tx, s *delegationSnapshot, roots map[string]string) error {
	representatives := map[string]string{}
	cids := []string{}
	for cid, id := range roots {
		representatives[cid] = id
		cids = append(cids, cid)
	}
	// Find omitted transcript parents with the child-identity index. Ordinary
	// top-level conversations do not need per-conversation ancestor lookups.
	encoded, err := json.Marshal(cids)
	if err != nil {
		return err
	}
	incoming, err := queryMapsContext(ctx, tx, `SELECT DISTINCT child_conversation_id FROM agent_sessions WHERE child_conversation_id IN (SELECT value FROM json_each(?))`, string(encoded))
	if err != nil {
		return err
	}
	declared := map[string]bool{}
	for _, row := range incoming {
		declared[firstString(row["child_conversation_id"])] = true
	}
	pending := []*delegationNode{}
	for _, n := range s.Nodes {
		if n.ExternalChild != nil && representatives[n.ExternalChild.ConversationID] == "" {
			representatives[n.ExternalChild.ConversationID] = n.ID
		}
		if n.ID == roots[n.ConversationID] && s.ByID[n.ParentID] == nil && (n.ExternalParent != nil || n.ParentID != "" || declared[n.ConversationID]) {
			pending = append(pending, n)
		}
	}
	resolver := delegationAncestorResolver{ctx: ctx, tx: tx, workspaces: map[string]*delegationAncestorWorkspace{}, conversations: map[string]map[string]any{}}
	for len(pending) > 0 {
		n := pending[0]
		pending = pending[1:]
		cv, err := resolver.conversation(n.ConversationID)
		if err != nil {
			return err
		}
		if cv == nil {
			continue
		}
		cid, proof, message, err := resolver.parent(cv)
		if err != nil {
			return err
		}
		if cid == "" {
			continue
		}
		parent, err := resolver.conversation(cid)
		if err != nil {
			return err
		}
		if parent == nil {
			continue
		}
		id := representatives[cid]
		if s.ByID[id] == nil {
			if len(s.Nodes) >= 10000 {
				return fmt.Errorf("workspace exceeds the 10,000-session analysis limit")
			}
			id = "ancestor-conversation:" + cid
			p := &delegationNode{ID: id, ConversationID: cid, WorkspaceID: firstString(parent["workspace_id"]),
				Provider: firstString(parent["provider"]), Model: firstString(parent["model"]), Effort: firstString(parent["effort"]),
				Label: firstString(parent["label"]), Coverage: firstString(parent["coverage"]), Usage: "unavailable",
				ContextOnly: true, Self: map[string]delegationMetric{}, All: map[string]delegationMetric{}}
			p.Navigation = &delegationReference{ConversationID: cid, WorkspaceID: p.WorkspaceID, Provider: p.Provider, Label: p.Label}
			for _, spec := range delegationMetrics {
				p.Self[spec.ID] = delegationUnknown()
			}
			representatives[cid] = id
			s.Nodes = append(s.Nodes, p)
			s.ByID[id] = p
			pending = append(pending, p)
		}
		n.ParentID, n.ParentEvidence = id, proof
		if message != "" {
			n.MessageID = message
		}
		if !n.ContextOnly && s.ByID[id].WorkspaceID != n.WorkspaceID {
			p := s.ByID[id]
			n.ExternalParent = &delegationReference{ConversationID: cid, WorkspaceID: p.WorkspaceID, Provider: p.Provider, Label: p.Label, MessageID: message}
		}
	}
	return nil
}

type delegationAncestorWorkspace struct {
	conversations, sessions []map[string]any
	outside                 []map[string]any
	roots                   map[string]string
	bySession               map[string]map[string]any
}

type delegationAncestorResolver struct {
	ctx           context.Context
	tx            *sql.Tx
	workspaces    map[string]*delegationAncestorWorkspace
	conversations map[string]map[string]any
}

func (r *delegationAncestorResolver) conversation(cid string) (map[string]any, error) {
	if cv, ok := r.conversations[cid]; ok {
		return cv, nil
	}
	rows, err := queryMapsContext(r.ctx, r.tx, `SELECT c.id,c.workspace_id,c.native_id,c.parent_id,c.provider,c.account,c.agent_path,c.model,c.effort,c.coverage,
 substr(COALESCE(NULLIF(c.agent_nickname,''),NULLIF(w.title,''),NULLIF(d.initiation,''),c.native_id),1,180) label
 FROM conversations c JOIN workspaces w ON w.id=c.workspace_id
 LEFT JOIN conversation_documents d ON d.conversation_id=c.id WHERE c.id=?`, cid)
	if err != nil {
		return nil, err
	}
	var cv map[string]any
	if len(rows) > 0 {
		cv = rows[0]
	}
	r.conversations[cid] = cv
	return cv, nil
}

func (r *delegationAncestorResolver) workspace(id string) (*delegationAncestorWorkspace, error) {
	if w, ok := r.workspaces[id]; ok {
		return w, nil
	}
	conversations, err := queryMapsContext(r.ctx, r.tx, `SELECT id,workspace_id,native_id,parent_id,provider,account,agent_path,agent_nickname,model,effort,coverage FROM conversations WHERE workspace_id=? ORDER BY started_at,id LIMIT 10001`, id)
	if err != nil {
		return nil, err
	}
	// Read only identity and reconciliation fields, never ancestor usage ledgers
	// or transcript bodies. The token total determines whether a launch aliases.
	sessions, err := queryMapsContext(r.ctx, r.tx, `SELECT id,conversation_id,parent_id,child_conversation_id,delegation_message_id,native_id,usage_status,total_tokens FROM agent_sessions WHERE workspace_id=? ORDER BY started_at,id LIMIT 10001`, id)
	if err != nil {
		return nil, err
	}
	if len(conversations) > 10000 || len(sessions) > 10000 {
		// Reconciliation is optional context. Unrelated records in the parent's
		// workspace must not prevent loading the selected child's ancestry.
		r.workspaces[id] = nil
		return nil, nil
	}
	w := &delegationAncestorWorkspace{conversations: conversations, sessions: sessions, roots: map[string]string{}, bySession: map[string]map[string]any{}}
	for _, row := range sessions {
		w.bySession[firstString(row["id"])] = row
		if row["native_id"] == "main" {
			w.roots[firstString(row["conversation_id"])] = firstString(row["id"])
		}
	}
	for _, cv := range conversations {
		cid := firstString(cv["id"])
		if w.roots[cid] == "" {
			w.roots[cid] = "conversation:" + cid
		}
	}
	family, err := loadDelegationFamily(r.ctx, r.tx, id)
	if errors.Is(err, errDelegationFamilyLimit) {
		r.workspaces[id] = nil
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, cv := range family {
		if firstString(cv["workspace_id"]) != id {
			w.outside = append(w.outside, cv)
		}
	}
	r.workspaces[id] = w
	return w, nil
}

func (r *delegationAncestorResolver) parent(cv map[string]any) (string, string, string, error) {
	cid := firstString(cv["id"])
	// Explicit child identities can recover an incoming owner even when the
	// child omitted its transcript parent entirely.
	exact, err := queryMapsContext(r.ctx, r.tx, `SELECT a.parent_id,a.conversation_id,a.delegation_message_id
 FROM agent_sessions a
 WHERE a.child_conversation_id=? AND a.conversation_id<>? ORDER BY a.started_at,a.id LIMIT 10001`, cid, cid)
	if err != nil {
		return "", "", "", err
	}
	if len(exact) > 10000 {
		return "", "", "", fmt.Errorf("workspace exceeds the 10,000-session analysis limit")
	}
	if len(exact) > 0 {
		row := exact[len(exact)-1]
		owner, err := r.sessionConversation(firstString(row["parent_id"]), firstString(row["conversation_id"]))
		if err != nil {
			return "", "", "", err
		}
		if owner != "" {
			return owner, "exact-delegation-handle", firstString(row["delegation_message_id"]), nil
		}
	}
	parent := firstString(cv["parent_id"])
	if parent != "" {
		hint, err := r.conversation(parent)
		if err != nil {
			return "", "", "", err
		}
		if hint != nil {
			w, err := r.workspace(firstString(hint["workspace_id"]))
			if err != nil {
				return "", "", "", err
			}
			if w != nil {
				roots := map[string]string{}
				for key, id := range w.roots {
					roots[key] = id
				}
				outside := append([]map[string]any{}, w.outside...)
				if roots[cid] == "" {
					roots[cid] = "ancestor-conversation:" + cid
					found := false
					for _, candidate := range outside {
						found = found || candidate["id"] == cid
					}
					if !found {
						outside = append(outside, cv)
					}
				}
				links, err := resolveDelegationLinks(r.ctx, r.tx, firstString(hint["workspace_id"]), w.conversations, w.sessions, roots, outside)
				if err != nil {
					return "", "", "", err
				}
				owner, message := "", ""
				for _, row := range w.sessions {
					if links.aliases[firstString(row["id"])] != roots[cid] {
						continue
					}
					candidate, err := r.sessionConversation(firstString(row["parent_id"]), firstString(row["conversation_id"]))
					if err != nil {
						return "", "", "", err
					}
					if candidate == "" {
						continue
					}
					owner, message = candidate, firstString(row["delegation_message_id"])
				}
				if owner != "" {
					return owner, "exact-delegation-handle", message, nil
				}
			}
		}
	}
	// A recorded main-session parent precedes the transcript fallback, just as
	// it does in the local execution hierarchy.
	sessionParents, err := queryMapsContext(r.ctx, r.tx, `SELECT a.parent_id FROM agent_sessions a JOIN agent_sessions p ON p.id=a.parent_id WHERE a.conversation_id=? AND a.native_id='main' AND p.conversation_id<>?`, cid, cid)
	if err != nil {
		return "", "", "", err
	}
	if len(sessionParents) > 0 {
		owner, err := r.sessionConversation(firstString(sessionParents[0]["parent_id"]), "")
		if err != nil {
			return "", "", "", err
		}
		if owner != "" {
			return owner, "session-parent", "", nil
		}
	}
	if parent != "" {
		return parent, "conversation-parent", "", nil
	}
	launches, err := queryMapsContext(r.ctx, r.tx, `SELECT l.parent_id,t.call_message_id FROM conversation_launches l LEFT JOIN tool_calls t ON t.id=l.tool_call_id WHERE l.child_id=?`, cid)
	if err != nil {
		return "", "", "", err
	}
	if len(launches) > 0 {
		return firstString(launches[0]["parent_id"]), "inferred-command-launch", firstString(launches[0]["call_message_id"]), nil
	}
	return "", "", "", nil
}

// A parent handle names its reconciled execution, which may have a different
// conversation from the transcript retaining the launch. Use the same alias
// eligibility and acknowledgement matching as the outgoing hierarchy.
func (r *delegationAncestorResolver) sessionConversation(id, fallback string) (string, error) {
	if id == "" {
		return fallback, nil
	}
	rows, err := queryMapsContext(r.ctx, r.tx, `SELECT id,workspace_id,conversation_id,child_conversation_id,delegation_message_id,usage_status,total_tokens FROM agent_sessions WHERE id=?`, id)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return fallback, nil
	}
	row := rows[0]
	aliases := func(row map[string]any) bool {
		return firstString(row["usage_status"]) == "in-child-conversation" || integer(row["total_tokens"]) == 0
	}
	if !aliases(row) {
		return firstString(row["conversation_id"]), nil
	}
	if child := firstString(row["child_conversation_id"]); child != "" {
		return child, nil
	}
	if firstString(row["delegation_message_id"]) == "" {
		return firstString(row["conversation_id"]), nil
	}
	w, err := r.workspace(firstString(row["workspace_id"]))
	if err != nil {
		return "", err
	}
	if w == nil {
		// Keep the recorded ancestry when reconciliation is bounded out.
		return "", nil
	}
	links, err := resolveDelegationLinks(r.ctx, r.tx, firstString(row["workspace_id"]), w.conversations, w.sessions, w.roots, w.outside)
	if err != nil {
		return "", err
	}
	seen := map[string]bool{}
	for !seen[id] {
		seen[id] = true
		if parent := w.bySession[id]; parent != nil && !aliases(parent) {
			return firstString(parent["conversation_id"]), nil
		}
		if alias := links.aliases[id]; alias != "" {
			id = alias
			continue
		}
		if child := links.external[id]; child != nil {
			return child.ConversationID, nil
		}
		for cid, root := range w.roots {
			if id == root {
				return cid, nil
			}
		}
		return firstString(w.bySession[id]["conversation_id"], row["conversation_id"]), nil
	}
	return "", nil
}
