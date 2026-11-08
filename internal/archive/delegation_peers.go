package archive

import (
	"context"
	"slices"
)

// delegationPeer identifies related work whose agent sessions a workspace's
// Hierarchy and Timeline show beside its own, so a review lines up with the
// implementation it read. Only certain peers are merged; uncertain ones stay
// chips on the page.
type delegationPeer struct {
	WorkspaceID string `json:"workspace_id"`
	Title       string `json:"title"`
	Role        string `json:"role"`
}

// workGroupSnapshot is the workspace's delegation snapshot with the families
// of its certain peers added as further top-level trees. Totals stay the
// workspace's own; GroupTotals counts everything shown.
func (c *Catalog) workGroupSnapshot(ctx context.Context, workspace string) (*delegationSnapshot, error) {
	own, err := c.delegationSnapshot(ctx, workspace)
	if err != nil || own == nil {
		return own, err
	}
	peers, err := c.workPeers(ctx, workspace)
	if err != nil {
		return nil, err
	}
	certain := []workPeer{}
	for _, p := range peers {
		if p.Confidence == peerHigh {
			certain = append(certain, p)
		}
	}
	if len(certain) == 0 {
		return own, nil
	}
	// The key keeps the delegation prefix so the cache's snapshot limit and
	// idle pruning apply to it as well.
	return cachedValue(ctx, c, "delegation:group:"+workspace, func(ctx context.Context) (*delegationSnapshot, error) {
		parts := []*delegationSnapshot{own}
		marks := []*delegationPeer{nil}
		for _, p := range certain {
			snapshot, err := c.delegationSnapshot(ctx, p.WorkspaceID)
			if err != nil {
				return nil, err
			}
			if snapshot != nil {
				parts = append(parts, snapshot)
				marks = append(marks, &delegationPeer{WorkspaceID: p.WorkspaceID, Title: p.Title, Role: p.Role})
			}
		}
		return mergeDelegationSnapshots(parts, marks), nil
	})
}

// mergeDelegationSnapshots copies the nodes of several snapshots into one.
// Cached snapshots are shared, so every node is copied before it is
// renumbered; a node already present (a launched child seen from both sides)
// keeps its first copy.
func mergeDelegationSnapshots(parts []*delegationSnapshot, marks []*delegationPeer) *delegationSnapshot {
	s := &delegationSnapshot{ByID: map[string]*delegationNode{}, Aliases: map[string]string{}, Warnings: []string{}}
	for i, part := range parts {
		for _, n := range part.Nodes {
			if s.ByID[n.ID] != nil {
				continue
			}
			copy := *n
			copy.children = nil
			copy.Self = make(map[string]delegationMetric, len(n.Self))
			for k, v := range n.Self {
				copy.Self[k] = v
			}
			copy.Peer = marks[i]
			s.Nodes = append(s.Nodes, &copy)
			s.ByID[n.ID] = &copy
		}
		for alias, id := range part.Aliases {
			if s.Aliases[alias] == "" {
				s.Aliases[alias] = id
			}
		}
		for _, warning := range part.Warnings {
			if !slices.Contains(s.Warnings, warning) {
				s.Warnings = append(s.Warnings, warning)
			}
		}
		if part.Start != nil && (s.Start == nil || *part.Start < *s.Start) {
			v := *part.Start
			s.Start = &v
		}
		if part.End != nil && (s.End == nil || *part.End > *s.End) {
			v := *part.End
			s.End = &v
		}
	}
	for _, n := range s.Nodes {
		if p := s.ByID[n.ParentID]; p != nil {
			p.children = append(p.children, n)
		} else {
			s.Roots = append(s.Roots, n)
		}
	}
	s.finish()
	s.Totals = parts[0].Totals
	s.GroupTotals = delegationWorkspaceTotals(s.Nodes)
	return s
}
