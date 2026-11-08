package archive

import (
	"context"
	"database/sql"
	"sort"
)

type usageConversationAttribution struct {
	roots            map[string]map[string]any
	delegatedRuns    map[string]string
	delegatedDepth   map[string]int64
	delegatedParents map[string]string
	sessionRoles     map[string]string
	unresolved       map[string]string
	// rootQuality (by conversation) and depthQuality (by session) say how a
	// projected root or depth was established (executionAncestry): a cohort
	// or depth reached through an inferred or unverified link is qualified,
	// never presented as observed. uncertain marks conversations and sessions
	// whose execution, or an ancestor, is in an unresolved identity conflict:
	// their run and cohort counts are not established.
	rootQuality, depthQuality map[string]string
	uncertain                 map[string]bool
	// canonical names the conversation each mirror alias stands for, and runs
	// its execution identity: "" for a root's own (or continued) execution.
	canonical, runs map[string]string
	// resolution is the shared execution result these fields project.
	resolution *executionResolution
}

// usageConversationRoots projects the shared execution resolution onto
// usage scope: each conversation's recorded top-level root, and each
// session's execution, parent and depth. Identity, parentage, continuations,
// launch outcomes and copied launches are decided once by the resolver; the
// only scope decision here is which retained copy of a mirrored transcript
// represents it (ledger lists the retained workspaces). Unresolved ancestry
// stays unresolved rather than inventing a root.
func (c *Catalog) usageConversationRoots(ctx context.Context, ledger []map[string]any) (*usageConversationAttribution, error) {
	tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	r, err := c.executionResolutionAt(ctx, tx)
	if err != nil {
		return nil, err
	}
	return usageConversationRootsOf(ctx, tx, r, ledger)
}

// usageConversationRootsAndOccurrences is usageConversationRoots with the
// catalog-wide occurrence index of the same generation, read in one
// transaction, for consumers that aggregate the whole catalog.
func (c *Catalog) usageConversationRootsAndOccurrences(ctx context.Context, ledger []map[string]any) (*usageConversationAttribution, *occurrenceIndex, error) {
	tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	s, err := c.executionSnapshotAt(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	lineage, err := usageConversationRootsOf(ctx, tx, s.resolution, ledger)
	if err != nil {
		return nil, nil, err
	}
	occurrences, err := s.catalogOccurrences(ctx, tx)
	return lineage, occurrences, err
}

// usageConversationRootsContext is usageConversationRoots, under the name
// master's cancellable callers use.
func (c *Catalog) usageConversationRootsContext(ctx context.Context, ledger []map[string]any) (*usageConversationAttribution, error) {
	return c.usageConversationRoots(ctx, ledger)
}

// usageConversationRootsFrom is usageConversationRoots read through q.
func usageConversationRootsFrom(q queryer, ledger []map[string]any) (*usageConversationAttribution, error) {
	return usageConversationRootsWith(q, ledger, nil)
}

// usageConversationRootsWith resolves roots with the given launch links
// (child_id, parent_id) in place of the stored ledger, when not nil. It
// resolves q's own snapshot, as offline labeling requires.
func usageConversationRootsWith(q queryer, ledger, derived []map[string]any) (*usageConversationAttribution, error) {
	ctx := context.Background()
	e, err := loadExecutionEvidenceWith(ctx, q, derived)
	if err != nil {
		return nil, err
	}
	return usageConversationRootsOf(ctx, q, resolveExecutions(e), ledger)
}

func usageConversationRootsOf(ctx context.Context, q queryer, r *executionResolution, ledger []map[string]any) (*usageConversationAttribution, error) {
	conversations, err := queryMapsContext(ctx, q, `SELECT c.id,c.workspace_id,c.provider,
		COALESCE(NULLIF(c.model,''),a.model) model,COALESCE(NULLIF(c.effort,''),a.effort) effort,
		w.title root_title,r.display_name root_repository_name
		FROM conversations c LEFT JOIN agent_sessions a ON a.conversation_id=c.id AND a.native_id='main'
		LEFT JOIN workspaces w ON w.id=c.workspace_id LEFT JOIN repositories r ON r.id=w.repository_id ORDER BY c.id`)
	if err != nil {
		return nil, err
	}
	sessions, err := queryMapsContext(ctx, q, `SELECT id,native_id,kind,conversation_id FROM agent_sessions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return usageAttributionFrom(r, conversations, sessions, ledger), nil
}

func usageAttributionFrom(r *executionResolution, conversations, sessions, ledger []map[string]any) *usageConversationAttribution {
	kept := map[string]bool{}
	for _, row := range ledger {
		kept[firstString(row["workspace_id"])] = true
	}
	byID := map[string]map[string]any{}
	for _, row := range conversations {
		byID[firstString(row["id"])] = row
	}
	result := &usageConversationAttribution{roots: map[string]map[string]any{}, canonical: map[string]string{}, runs: map[string]string{},
		delegatedRuns: map[string]string{}, delegatedDepth: map[string]int64{}, delegatedParents: map[string]string{}, sessionRoles: map[string]string{},
		unresolved: map[string]string{}, rootQuality: map[string]string{}, depthQuality: map[string]string{}, uncertain: map[string]bool{}, resolution: r}
	// Mirrors the resolver joined into one execution: the copy in a retained
	// workspace represents them in usage scope, then the smallest ID.
	group := map[string]string{}
	var find func(string) string
	find = func(id string) string {
		if p := group[id]; p != "" && p != id {
			group[id] = find(p)
			return group[id]
		}
		return id
	}
	better := func(a, b string) bool {
		ka, kb := kept[firstString(byID[a]["workspace_id"])], kept[firstString(byID[b]["workspace_id"])]
		if ka != kb {
			return ka
		}
		return a < b
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		b := r.BindConversation(id)
		for _, claim := range b.Evidence {
			if claim.Kind != claimMirror || claim.Subject.ID != id || byID[claim.Object.ID] == nil {
				continue
			}
			if r.BindConversation(claim.Object.ID).ExecutionID != b.ExecutionID || b.ExecutionID == "" {
				continue
			}
			x, y := find(id), find(claim.Object.ID)
			if x == y {
				continue
			}
			if better(y, x) {
				x, y = y, x
			}
			group[y] = x
		}
	}
	for _, id := range ids {
		result.canonical[id] = find(id)
	}
	// runID names an execution in usage scope: a transcript-backed execution
	// by its representative conversation, any other by its execution ID.
	runID := func(id executionID) string {
		x := r.Executions[id]
		if x == nil {
			return ""
		}
		if x.Conversation != "" {
			return "conversation:" + result.canonical[x.Conversation]
		}
		return string(id)
	}
	rootRow := func(id executionID) map[string]any {
		if x := r.Executions[id]; x != nil && x.Conversation != "" {
			return byID[result.canonical[x.Conversation]]
		}
		return nil
	}
	unresolvedReason := func(state parentState) string {
		switch state {
		case parentCycle:
			return "cycle"
		case parentAmbiguous, parentConflict:
			return "conflict"
		}
		return "missing_parent"
	}
	for _, id := range ids {
		b := r.BindConversation(id)
		if b.State != bindingResolved {
			result.unresolved[id] = "missing_parent"
			continue
		}
		a := r.Ancestry(b.ExecutionID)
		root := rootRow(a.Root)
		result.roots[id] = root
		if a.Conflicted {
			result.uncertain[id] = true
		}
		if root == nil {
			result.unresolved[id] = unresolvedReason(a.State)
			continue
		}
		result.rootQuality[id] = a.RootQuality()
		if r.Executions[b.ExecutionID].Parent.State != parentRoot {
			result.runs[id] = runID(b.ExecutionID)
		}
	}
	for _, session := range sessions {
		id, cv := firstString(session["id"]), firstString(session["conversation_id"])
		if result.roots[cv] == nil {
			continue
		}
		b := r.BindSession(id)
		if b.ExecutionID == "" || b.State != bindingResolved && b.State != bindingInherited {
			continue // an attempt, or identity the evidence cannot decide
		}
		x := r.Executions[b.ExecutionID]
		a := r.Ancestry(x.ID)
		run := ""
		if x.Parent.State != parentRoot {
			run = runID(x.ID)
		}
		result.delegatedRuns[id] = run
		if a.Conflicted {
			result.uncertain[id] = true
		}
		if a.Known {
			result.delegatedDepth[id] = int64(a.Depth)
			result.depthQuality[id] = a.DepthQuality()
			if a.Depth > 0 {
				result.delegatedParents[id] = runID(x.Parent.ParentID)
			}
		}
	}
	for _, session := range sessions {
		id := firstString(session["id"])
		switch session["kind"] {
		case "subagent":
			// A subagent remains a subagent even when it delegates further.
			result.sessionRoles[id] = "subagent"
		case "root":
			if _, known := result.delegatedDepth[id]; !known || result.roots[firstString(session["conversation_id"])] == nil {
				continue
			}
			result.sessionRoles[id] = "singleton"
			if len(r.Children(r.BindSession(id).ExecutionID)) > 0 {
				result.sessionRoles[id] = "parent"
			}
		}
	}
	return result
}
