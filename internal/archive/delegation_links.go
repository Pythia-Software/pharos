package archive

import (
	"context"
	"database/sql"
	"encoding/json"
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
	links := &delegationLinks{aliases: map[string]string{}, external: map[string]*delegationReference{}, failed: map[string]bool{}}
	byConversation := map[string]map[string]any{}
	for _, cv := range conversations {
		byConversation[firstString(cv["id"])] = cv
	}
	// Codex archives one workspace per rollout, including subagents. Follow the
	// reverse parent edges even though their transcripts live outside this scope.
	family, err := queryMapsContext(ctx, tx, `WITH RECURSIVE family(id) AS (
 SELECT id FROM conversations WHERE workspace_id=?
 UNION SELECT c.id FROM family p JOIN conversations c ON c.parent_id=p.id WHERE c.agent_depth>0 LIMIT 10001)
 SELECT c.id,c.workspace_id,c.native_id,c.parent_id,c.provider,c.account,c.agent_path,c.agent_nickname,c.model,c.effort,c.coverage
 FROM family f JOIN conversations c ON c.id=f.id ORDER BY c.started_at,c.id`, workspace)
	if err != nil {
		return nil, err
	}
	if len(family) > 10000 {
		return nil, fmt.Errorf("workspace exceeds the 10,000-conversation family analysis limit")
	}
	outside := []map[string]any{}
	for _, cv := range family {
		if firstString(cv["workspace_id"]) != workspace {
			outside = append(outside, cv)
		}
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
	evidence, err := queryMapsContext(ctx, tx, `SELECT a.id,substr(m.text,1,8192) text FROM agent_sessions a JOIN messages d ON d.id=a.delegation_message_id JOIN messages m ON m.conversation_id=d.conversation_id AND m.call_id=d.call_id AND +m.kind IN ('delegation_result','tool_result') WHERE a.workspace_id=? AND a.child_conversation_id IS NULL AND a.native_id<>'main'`, workspace)
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
		row := map[string]any{"id": id, "native_id": id, "conversation_id": cv["parent_id"], "parent_id": roots[firstString(cv["parent_id"])], "provider": cv["provider"], "usage_status": "unavailable", "assignment": firstString(cv["agent_nickname"], cv["agent_path"], cv["native_id"])}
		bind(row, cv)
		links.extra = append(links.extra, row)
	}
	for _, row := range links.extra {
		row["parent_id"] = representatives[firstString(row["conversation_id"])]
		row["parent_evidence"] = "conversation-parent"
	}
	return links, nil
}
