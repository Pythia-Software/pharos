package archive

import "sort"

type usageConversationAttribution struct {
	roots          map[string]map[string]any
	delegatedRuns  map[string]string
	delegatedDepth map[string]int64
	sessionRoles   map[string]string
}

// usageConversationRoots assigns every run to one recorded top-level
// conversation. Embedded agents already share a conversation ID; transcript
// parents, exact delegation handles, and command launches connect other runs,
// even across workspaces. Filename forks are continuations of the same work.
// Missing parents and cycles remain unresolved rather than inventing a root.
func (c *Catalog) usageConversationRoots(ledger []map[string]any) (*usageConversationAttribution, error) {
	conversations, err := queryMaps(c.DB, `SELECT c.id,c.workspace_id,c.parent_id,c.agent_depth,c.provider,
		COALESCE(NULLIF(c.model,''),a.model) model,COALESCE(NULLIF(c.effort,''),a.effort) effort
		FROM conversations c LEFT JOIN agent_sessions a ON a.conversation_id=c.id AND a.native_id='main' ORDER BY c.id`)
	if err != nil {
		return nil, err
	}
	sessions, err := queryMaps(c.DB, `SELECT id,native_id,kind,conversation_id,parent_id,child_conversation_id FROM agent_sessions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	links, err := queryMaps(c.DB, `SELECT left_id,right_id FROM conversation_identity_links ORDER BY left_id,right_id`)
	if err != nil {
		return nil, err
	}
	launches, err := queryMaps(c.DB, `SELECT child_id,parent_id FROM conversation_launches ORDER BY child_id,parent_id`)
	if err != nil {
		return nil, err
	}
	kept := map[string]bool{}
	for _, row := range ledger {
		kept[firstString(row["workspace_id"])] = true
	}
	byID, aliases := map[string]map[string]any{}, map[string]string{}
	for _, row := range conversations {
		id := firstString(row["id"])
		byID[id], aliases[id] = row, id
	}
	var canonical func(string) string
	canonical = func(id string) string {
		if aliases[id] != "" && aliases[id] != id {
			aliases[id] = canonical(aliases[id])
			return aliases[id]
		}
		return id
	}
	// Use the same retained workspace as the usage ledger for mirror identity.
	for _, link := range links {
		left, right := canonical(firstString(link["left_id"])), canonical(firstString(link["right_id"]))
		if byID[left] == nil || byID[right] == nil || left == right {
			continue
		}
		l, r := kept[firstString(byID[left]["workspace_id"])], kept[firstString(byID[right]["workspace_id"])]
		if r && !l || l == r && right < left {
			left, right = right, left
		}
		aliases[right] = left
	}
	parents := map[string]string{}
	delegations := map[string]bool{}
	// Prefer the retained transcript's parent if both mirrors recorded one.
	isAlias := func(row map[string]any) bool { return canonical(firstString(row["id"])) != firstString(row["id"]) }
	sort.SliceStable(conversations, func(i, j int) bool {
		return isAlias(conversations[i]) && !isAlias(conversations[j])
	})
	for _, row := range conversations {
		id, parent := canonical(firstString(row["id"])), canonical(firstString(row["parent_id"]))
		if parent != "" && (parent != id || row["parent_id"] == row["id"]) {
			parents[id] = parent
		}
		if integer(row["agent_depth"]) > 0 || parent != "" && row["provider"] != "codex" {
			delegations[id] = true
		}
	}
	bySession := map[string]map[string]any{}
	for _, row := range sessions {
		bySession[firstString(row["id"])] = row
	}
	for _, row := range sessions {
		id := canonical(firstString(row["conversation_id"]))
		if parent := bySession[firstString(row["parent_id"])]; parent != nil {
			pid := canonical(firstString(parent["conversation_id"]))
			if pid != "" && pid != id {
				parents[id] = pid
				delegations[id] = true
			}
		}
		if child := canonical(firstString(row["child_conversation_id"])); child != "" && child != id {
			parents[child] = id
			delegations[child] = true
		}
	}
	for _, link := range launches {
		id, parent := canonical(firstString(link["child_id"])), canonical(firstString(link["parent_id"]))
		if id != parent && parents[id] == "" {
			parents[id] = parent
			delegations[id] = true
		}
	}
	resolved, done := map[string]map[string]any{}, map[string]bool{}
	for _, row := range conversations {
		id := canonical(firstString(row["id"]))
		path, seen := []string{}, map[string]bool{}
		var root map[string]any
		for at := id; at != "" && byID[at] != nil && !seen[at]; at = parents[at] {
			if done[at] {
				root = resolved[at]
				break
			}
			seen[at], path = true, append(path, at)
			if parents[at] == "" {
				// A recorded child without its parent is not a known root.
				if integer(byID[at]["agent_depth"]) == 0 {
					root = byID[at]
				}
				break
			}
		}
		for _, at := range path {
			resolved[at], done[at] = root, true
		}
	}
	result := &usageConversationAttribution{roots: map[string]map[string]any{}, delegatedRuns: map[string]string{}, delegatedDepth: map[string]int64{}, sessionRoles: map[string]string{}}
	for id := range byID {
		result.roots[id] = resolved[canonical(id)]
	}
	// A continuation is the same execution, not another delegation. Exact
	// handles and their child transcripts name that same execution as well.
	runIDs, knownRun := map[string]string{}, map[string]bool{}
	conversationRun := func(id string) string {
		root := resolved[id]
		if root == nil {
			return ""
		}
		var path []string
		identity := ""
		for id != firstString(root["id"]) {
			if knownRun[id] {
				identity = runIDs[id]
				break
			}
			path = append(path, id)
			if delegations[id] {
				identity = "conversation:" + id
				break
			}
			id = parents[id]
		}
		for _, at := range path {
			runIDs[at], knownRun[at] = identity, true
		}
		return identity
	}
	for _, session := range sessions {
		id, cv := firstString(session["id"]), canonical(firstString(session["conversation_id"]))
		if resolved[cv] == nil {
			continue
		}
		if session["native_id"] == "main" {
			result.delegatedRuns[id] = conversationRun(cv)
		} else if child := canonical(firstString(session["child_conversation_id"])); child != "" && resolved[child] != nil && resolved[child]["id"] == resolved[cv]["id"] {
			result.delegatedRuns[id] = conversationRun(child)
		} else {
			result.delegatedRuns[id] = "agent:" + id
		}
	}
	// Build execution parents after alias resolution. Exact launch handles keep
	// nested delegation attached to its actual parent execution, not a flattened
	// transcript parent. Continuations add no level.
	runParents := map[string]string{}
	sessionParent := func(session map[string]any) (string, bool) {
		if parentID := firstString(session["parent_id"]); parentID != "" {
			parent, known := result.delegatedRuns[parentID]
			return parent, known
		}
		cv := canonical(firstString(session["conversation_id"]))
		return conversationRun(cv), resolved[cv] != nil
	}
	for _, session := range sessions {
		id, cv := firstString(session["id"]), canonical(firstString(session["conversation_id"]))
		run := result.delegatedRuns[id]
		if run == "" {
			continue
		}
		if session["native_id"] == "main" {
			parent := conversationRun(parents[cv])
			if run != parent {
				runParents[run] = parent
			}
		} else {
			if parent, known := sessionParent(session); known {
				runParents[run] = parent
			}
		}
	}
	// Exact handles are stronger than transcript parents, independent of ID order.
	for _, session := range sessions {
		if firstString(session["child_conversation_id"]) == "" {
			continue
		}
		run := result.delegatedRuns[firstString(session["id"])]
		if run != "" {
			if parent, known := sessionParent(session); known {
				runParents[run] = parent
			} else {
				delete(runParents, run)
			}
		}
	}
	depths := map[string]int64{"": 0}
	for _, run := range result.delegatedRuns {
		if run == "" {
			continue
		}
		path, seen := []string{}, map[string]bool{}
		at := run
		for {
			if _, known := depths[at]; known {
				break
			}
			parent, known := runParents[at]
			if seen[at] || !known {
				at = ""
				path = nil
				break
			}
			seen[at], path = true, append(path, at)
			at = parent
		}
		depth := depths[at]
		for i := len(path) - 1; i >= 0; i-- {
			depth++
			depths[path[i]] = depth
		}
	}
	for session, run := range result.delegatedRuns {
		if depth, known := depths[run]; known {
			result.delegatedDepth[session] = depth
		}
	}
	// Split recorded roots by structural delegation, across the full recorded
	// graph. Tokenless children still establish a parent; continuations alone do
	// not. A root and its continuations share their execution's parent status.
	hasChildren := map[string]bool{}
	for _, session := range sessions {
		id := firstString(session["id"])
		root := result.roots[firstString(session["conversation_id"])]
		run := result.delegatedRuns[id]
		parent, known := runParents[run]
		if _, resolvedDepth := result.delegatedDepth[id]; root != nil && run != "" && known && resolvedDepth && run != parent {
			hasChildren[firstString(root["id"])+"|"+parent] = true
		}
	}
	for _, session := range sessions {
		id := firstString(session["id"])
		if session["kind"] == "subagent" {
			// A subagent remains a subagent even when it delegates further.
			result.sessionRoles[id] = "subagent"
		} else if session["kind"] == "root" {
			root := result.roots[firstString(session["conversation_id"])]
			if _, known := result.delegatedDepth[id]; root == nil || !known {
				continue
			}
			result.sessionRoles[id] = "singleton"
			if hasChildren[firstString(root["id"])+"|"+result.delegatedRuns[id]] {
				result.sessionRoles[id] = "parent"
			}
		}
	}
	return result, nil
}
