package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
)

// The delegation explorer projects the shared execution resolution
// (execution_resolver.go) onto one workspace: one row per execution with a
// stream in the workspace, plus its family as navigation-only context. The
// explorer decides no identity, parent, label or sibling order of its own.

// delegationFamilyLimit bounds the context rows a workspace's families add,
// within the snapshot's 10,000-row analysis limit. Context is chosen nearest
// first (familyContext), so a reader always holds its relatives; a larger
// family reports incomplete coverage, and each row with omitted children
// counts them and opens a reader centred on itself.
var delegationFamilyLimit = 10000

type delegationProjection struct {
	res *executionResolution
}

// nodeID is the explorer row ID for an execution: its origin transcript's
// main agent_sessions row, "conversation:<id>" for a transcript without one,
// or the launch/embedded session. Earlier row IDs resolve through bindings.
func (p delegationProjection) nodeID(id executionID) string {
	if cid, ok := strings.CutPrefix(string(id), "conversation:"); ok {
		if s := p.res.Stream(sourceRef{sourceConversation, cid}); s != nil && s.MainSessionID != "" {
			return s.MainSessionID
		}
		return string(id)
	}
	return strings.TrimPrefix(string(id), "agent:")
}

// execution returns the execution a source row ID names, if it resolves to one.
func (p delegationProjection) execution(row string) *execution {
	b := p.res.ResolveAlias(row)
	if b.State != bindingResolved {
		return nil
	}
	return p.res.Executions[b.ExecutionID]
}

// alias resolves a saved row ID. ok is false for an ID the evidence does not
// know; canonical is empty for an attempt, a copy whose original is unknown,
// or an ID that no longer identifies one execution.
func (p delegationProjection) alias(id string) (canonical string, ok bool) {
	b := p.res.ResolveAlias(id)
	switch b.State {
	case bindingUnknown:
		return "", false
	case bindingResolved, bindingInherited:
		if x := p.res.Executions[b.ExecutionID]; x != nil && !x.Conflicted {
			return p.nodeID(x.ID), true
		}
	}
	return "", true
}

// ancestry projects the execution's own parent state, the resolved chain of
// row IDs nearest first, and a depth only when the chain reaches a known root.
// A child of an unresolved execution keeps its known parent without a depth.
func (p delegationProjection) ancestry(id executionID) (delegationAncestry, []string) {
	a, out := p.res.Ancestry(id), delegationAncestry{State: string(parentMissing)}
	if x := p.res.Executions[id]; x != nil {
		out.State = string(x.Parent.State)
	}
	if a.Known {
		depth := a.Depth
		out.Depth = &depth
		out.Certainty, out.DepthQuality = a.CertaintyName(), a.DepthQuality()
	}
	out.RootQuality, out.Conflicted = a.RootQuality(), a.Conflicted
	chain := make([]string, len(a.Chain))
	for i, ancestor := range a.Chain {
		chain[i] = p.nodeID(ancestor)
	}
	return out, chain
}

// familyContext chooses up to limit context executions for the locals:
// ancestor chains first (the layout needs them), nearest ancestors of every
// local before farther ones and never past capacity, then relatives nearest
// first, breadth first over parent and child edges in the resolution's
// order. Rows the reader expanded (delegation_relatives) then add their own
// omitted children within capacity, the analysis limit: expanding along any
// path reaches every member of the family in one reader.
func (p delegationProjection) familyContext(locals, expand []executionID, limit, capacity int) (context []executionID, complete bool) {
	local, added := map[executionID]bool{}, map[executionID]bool{}
	for _, id := range locals {
		local[id] = true
	}
	add := func(id executionID) {
		if !local[id] && !added[id] && p.res.Executions[id] != nil {
			added[id] = true
			context = append(context, id)
		}
	}
	// Ancestors by distance across all locals, so a bound keeps each local's
	// nearest parents; past capacity the rest are left out and the family
	// reads incomplete (each left-out ancestor is still named by its rows'
	// ancestor IDs and reachable from its own work).
	truncated := false
	chains := make([][]executionID, len(locals))
	longest := 0
	for i, id := range locals {
		chains[i] = p.res.Ancestry(id).Chain
		longest = max(longest, len(chains[i]))
	}
	for distance := 0; distance < longest; distance++ {
		for _, chain := range chains {
			if distance >= len(chain) || local[chain[distance]] || added[chain[distance]] {
				continue
			}
			if len(context) >= capacity {
				truncated = true
				continue
			}
			add(chain[distance])
		}
	}
	seen := map[executionID]bool{}
	frontier := append([]executionID{}, locals...)
	frontier = append(frontier, context...)
	for _, id := range frontier {
		seen[id] = true
	}
	for len(frontier) > 0 {
		next := []executionID{}
		neighbours := func(id executionID) []executionID {
			out := append([]executionID{}, p.res.Children(id)...)
			sort.Slice(out, func(i, j int) bool { return p.res.Less(out[i], out[j]) })
			// A root's members whose own parent is unresolved (Family).
			out = append(out, p.res.detached[id]...)
			if x := p.res.Executions[id]; x != nil && x.Parent.State == parentResolved {
				out = append([]executionID{x.Parent.ParentID}, out...)
			}
			// Membership without a resolved parent still names the family root.
			if a := p.res.Ancestry(id); a.Root != "" && a.Root != id {
				out = append(out, a.Root)
			}
			return out
		}
		for _, id := range frontier {
			for _, n := range neighbours(id) {
				if seen[n] {
					continue
				}
				seen[n] = true
				if len(context) >= limit && !local[n] {
					continue // left out: its parent's row counts it (ChildrenOmitted)
				}
				add(n)
				next = append(next, n)
			}
		}
		frontier = next
	}
	// Expanded rows in the resolution's order, so a nested expansion follows
	// its parent's.
	for len(expand) > 0 {
		progress := false
		rest := []executionID{}
		for _, id := range expand {
			if !local[id] && !added[id] {
				rest = append(rest, id) // not shown yet: perhaps after its parent expands
				continue
			}
			progress = true
			children := append([]executionID{}, p.res.Children(id)...)
			sort.Slice(children, func(i, j int) bool { return p.res.Less(children[i], children[j]) })
			for _, child := range append(children, p.res.detached[id]...) {
				if local[child] || added[child] {
					continue
				}
				if len(context) >= capacity {
					break
				}
				add(child)
			}
		}
		if !progress {
			break
		}
		expand = rest
	}
	if truncated {
		return context, false
	}
	// Complete when every member of the locals' families is shown, whether
	// the bound or an expansion placed it (the resolution holds them in
	// memory). Each family is walked once, keyed by its root, not once per
	// local row.
	checked := map[executionID]bool{}
	for _, id := range locals {
		// The walk Family makes starts at the chain's top (or the root
		// membership names), so locals sharing it share one family.
		a, key := p.res.Ancestry(id), id
		if len(a.Chain) > 0 {
			key = a.Chain[len(a.Chain)-1]
		}
		if a.Root != "" && !a.Known {
			key = a.Root
		}
		if checked[key] {
			continue
		}
		checked[key] = true
		family, _ := p.res.Family(id, 0)
		for _, member := range family {
			if !local[member] && !added[member] {
				return context, false
			}
		}
	}
	return context, true
}

// parentEvidence names the winning parent claim for the explorer's evidence
// column, in the vocabulary it already published.
func (p delegationProjection) parentEvidence(x *execution) (string, sourceRef) {
	if x.Parent.State != parentResolved || len(x.Parent.Evidence) == 0 {
		return "", sourceRef{}
	}
	claim := x.Parent.Evidence[0]
	switch claim.Kind {
	case claimLaunchOwner:
		return "exact-delegation-handle", claim.Subject
	case claimSessionParent:
		return "session-parent", sourceRef{}
	case claimTranscriptParent:
		return "conversation-parent", sourceRef{}
	case claimCommandLaunch:
		return "inferred-command-launch", claim.Locator
	}
	return string(claim.Kind), sourceRef{}
}

// less is the resolution's sibling order, so every view, page and reader
// agrees. Rows without an execution sort after resolved ones by ID.
func (p delegationProjection) less(a, b *delegationNode) bool {
	switch {
	case a.execution != "" && b.execution != "":
		return p.res.Less(a.execution, b.execution)
	case a.execution != b.execution:
		return a.execution != ""
	}
	return a.ID < b.ID
}

// unqueried reports whether the execution has streams in another workspace,
// whose activity and usage a workspace snapshot does not read. A mirror (the
// same transcript retained again) adds nothing to read.
func (p delegationProjection) unqueried(x *execution, workspace string) bool {
	for _, ref := range x.Streams {
		st := p.res.Stream(ref)
		if st == nil || st.WorkspaceID == workspace {
			continue
		}
		// A mirror claim is recorded on one end; look at every member's.
		mirror := false
		for _, member := range x.Streams {
			for _, claim := range p.res.Bind(member).Evidence {
				mirror = mirror || claim.Kind == claimMirror && (claim.Subject == ref || claim.Object == ref)
			}
		}
		if !mirror {
			return true
		}
	}
	return false
}

// hasTranscriptIn reports whether one of the execution's transcript segments
// is retained in the workspace.
func (p delegationProjection) hasTranscriptIn(x *execution, workspace string) bool {
	for _, ref := range x.Streams {
		if st := p.res.Stream(ref); st != nil && ref.Kind == sourceConversation && st.WorkspaceID == workspace {
			return true
		}
	}
	return false
}

// segments lists the execution's transcripts in segment order with the
// workspace that retains each, so every segment stays navigable.
func (p delegationProjection) segments(x *execution) []delegationSegment {
	out := make([]delegationSegment, 0, len(x.Segments))
	for _, cid := range x.Segments {
		segment := delegationSegment{ConversationID: cid}
		if st := p.res.Stream(sourceRef{sourceConversation, cid}); st != nil {
			segment.WorkspaceID = st.WorkspaceID
		}
		out = append(out, segment)
	}
	return out
}

// delegationConversationMeta is the compact metadata of conversations outside
// the workspace that context rows and external links show.
type delegationConversationMeta struct {
	ID, WorkspaceID, Provider, Model, Effort, Coverage string
	// Title is its workspace's title; Initiation its opening request.
	Title, Initiation string
}

func loadDelegationConversationMeta(ctx context.Context, tx *sql.Tx, ids []string) (map[string]delegationConversationMeta, error) {
	out := map[string]delegationConversationMeta{}
	if len(ids) == 0 {
		return out, nil
	}
	encoded, _ := json.Marshal(ids)
	rows, err := queryMapsContext(ctx, tx, `SELECT c.id,c.workspace_id,c.provider,c.model,c.effort,c.coverage,
 COALESCE(w.title,'') title,substr(COALESCE(d.initiation,''),1,180) initiation
 FROM conversations c JOIN workspaces w ON w.id=c.workspace_id
 LEFT JOIN conversation_documents d ON d.conversation_id=c.id
 WHERE c.id IN (SELECT value FROM json_each(?))`, string(encoded))
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		m := delegationConversationMeta{ID: firstString(row["id"]), WorkspaceID: firstString(row["workspace_id"]), Provider: firstString(row["provider"]),
			Model: firstString(row["model"]), Effort: firstString(row["effort"]), Coverage: firstString(row["coverage"]),
			Title: firstString(row["title"]), Initiation: firstString(row["initiation"])}
		out[m.ID] = m
	}
	return out, nil
}

// delegationLaunchMessages returns the retained launch message of each launch
// session, for parent links that point back at the call that created a child.
func delegationLaunchMessages(ctx context.Context, tx *sql.Tx, sessions []string) (map[string]string, error) {
	out := map[string]string{}
	if len(sessions) == 0 {
		return out, nil
	}
	encoded, _ := json.Marshal(sessions)
	rows, err := queryMapsContext(ctx, tx, `SELECT id,delegation_message_id FROM agent_sessions WHERE id IN (SELECT value FROM json_each(?)) AND delegation_message_id IS NOT NULL`, string(encoded))
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[firstString(row["id"])] = firstString(row["delegation_message_id"])
	}
	return out, nil
}

// transcripts are the conversations whose own stream is among the rows, so
// their messages without session membership belong to that execution.
func (p delegationProjection) transcripts(rows []string) []string {
	out := []string{}
	for _, row := range rows {
		if cid, ok := strings.CutPrefix(row, "conversation:"); ok {
			out = append(out, cid)
		} else if st := p.res.Stream(sourceRef{sourceSession, row}); st != nil && st.Ref.Kind == sourceConversation {
			out = append(out, st.ConversationID)
		}
	}
	return out
}
