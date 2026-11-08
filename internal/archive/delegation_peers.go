package archive

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
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

// workGroupPart reads one workspace's snapshot for a group (a package
// variable so tests can interleave commits between the parts).
var workGroupPart = (*Catalog).delegationSnapshotWith

// workGroupAttempts bounds how often a group is composed again when its
// parts were resolved at different execution revisions.
var workGroupAttempts = 3

// errWorkGroupRevisions: a group's parts were resolved at different
// revisions, so their rows cannot form one graph. It is never cached.
var errWorkGroupRevisions = errors.New("related work was resolved at different execution revisions")

// workGroupSnapshot is the workspace's delegation snapshot with the families
// of its certain peers added as further top-level trees. Totals stay the
// workspace's own; GroupTotals counts everything shown. relatives are the
// explorer's delegation_relatives; each part expands the ones it shows.
//
// Every part must share one execution revision: the Timeline reads lanes
// against the snapshot's revision, projection and members. A commit during
// composition retries it a bounded number of times, then shows the
// workspace's own snapshot alone rather than a mixed graph.
func (c *Catalog) workGroupSnapshot(ctx context.Context, workspace string, relatives []string) (*delegationSnapshot, error) {
	relatives = slices.Compact(slices.Sorted(slices.Values(relatives)))
	own, err := workGroupPart(c, ctx, workspace, relatives)
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
	for attempt := 0; attempt < workGroupAttempts; attempt++ {
		// The key keeps the delegation prefix so the cache's snapshot limit and
		// idle pruning apply to it as well, and names the execution source
		// generation and relatives as delegationSnapshotWith's key does.
		key := "delegation:group:" + workspace
		if generation, tracked, err := executionSourceGeneration(ctx, c.reader()); err != nil {
			return nil, err
		} else if tracked {
			key += "@" + strconv.FormatInt(generation, 10)
		}
		if len(relatives) > 0 {
			key += "+" + strings.Join(relatives, ",")
		}
		group, err := cachedValue(ctx, c, key, func(ctx context.Context) (*delegationSnapshot, error) {
			own, err := workGroupPart(c, ctx, workspace, relatives)
			if err != nil || own == nil {
				return own, err
			}
			parts, workspaces := []*delegationSnapshot{own}, []string{workspace}
			marks := []*delegationPeer{nil}
			for _, p := range certain {
				snapshot, err := workGroupPart(c, ctx, p.WorkspaceID, relatives)
				if err != nil {
					return nil, err
				}
				if snapshot != nil {
					parts, workspaces = append(parts, snapshot), append(workspaces, p.WorkspaceID)
					marks = append(marks, &delegationPeer{WorkspaceID: p.WorkspaceID, Title: p.Title, Role: p.Role})
				}
			}
			return mergeDelegationSnapshots(parts, workspaces, marks)
		})
		if !errors.Is(err, errWorkGroupRevisions) {
			return group, err
		}
	}
	// Still changing: the own snapshot is coherent by itself. It is shared,
	// so the warning goes on a copy.
	own, err = workGroupPart(c, ctx, workspace, relatives)
	if err != nil || own == nil {
		return own, err
	}
	alone := *own
	alone.Warnings = append(slices.Clip(own.Warnings), "Related work is changing; its agent sessions are left out until it settles.")
	return &alone, nil
}

// mergeDelegationSnapshots copies the nodes of several snapshots, each read
// for the workspace at the same index, into one.
// The parts must share one execution revision; the merged snapshot keeps it,
// with their projection, so aliases, members and Timeline lanes resolve as in
// each part. Cached snapshots are shared, so every node is copied before it
// is renumbered. A node present in several parts (a launched child seen from
// both sides) keeps the first copy that is not context only, so a peer's own
// row replaces the workspace's context copy of it, with its members.
func mergeDelegationSnapshots(parts []*delegationSnapshot, workspaces []string, marks []*delegationPeer) (*delegationSnapshot, error) {
	for _, part := range parts[1:] {
		if part.Revision != parts[0].Revision {
			return nil, errWorkGroupRevisions
		}
	}
	s := &delegationSnapshot{ByID: map[string]*delegationNode{}, Aliases: map[string]string{}, Warnings: []string{}, members: map[string][]string{},
		Revision: parts[0].Revision, projection: parts[0].projection, FamilyComplete: true}
	// The workspace each node was read for, which decides what it left unqueried.
	readFor := map[string]string{}
	for i, part := range parts {
		for _, n := range part.Nodes {
			existing := s.ByID[n.ID]
			if existing != nil && (n.ContextOnly || !existing.ContextOnly) {
				continue
			}
			copy := *n
			copy.children = nil
			copy.Self = make(map[string]delegationMetric, len(n.Self))
			for k, v := range n.Self {
				copy.Self[k] = v
			}
			copy.Peer = marks[i]
			if existing != nil {
				*existing = copy
			} else {
				existing = &copy
				s.Nodes = append(s.Nodes, existing)
				s.ByID[n.ID] = existing
			}
			s.members[n.ID] = part.members[n.ID]
			readFor[n.ID] = workspaces[i]
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
		for _, missing := range part.Coverage.Missing {
			if !slices.Contains(s.Coverage.Missing, missing) {
				s.Coverage.Missing = append(s.Coverage.Missing, missing)
			}
		}
		s.FamilyComplete = s.FamilyComplete && part.FamilyComplete
		if part.Start != nil && (s.Start == nil || *part.Start < *s.Start) {
			v := *part.Start
			s.Start = &v
		}
		if part.End != nil && (s.End == nil || *part.End > *s.End) {
			v := *part.End
			s.End = &v
		}
	}
	if s.Coverage.Missing == nil {
		s.Coverage.Missing = []string{}
	}
	for _, n := range s.Nodes {
		if p := s.ByID[n.ParentID]; p != nil {
			p.children = append(p.children, n)
		} else {
			s.Roots = append(s.Roots, n)
		}
	}
	s.finish()
	// Coverage describes the rows shown, each as its own part read it.
	for _, n := range s.Nodes {
		switch {
		case n.Ancestry.State == string(parentConflict):
			s.Coverage.Conflicts++
			s.Coverage.UnresolvedParents++
		case n.Ancestry.State != string(parentRoot) && n.Ancestry.State != string(parentResolved):
			s.Coverage.UnresolvedParents++
		case n.ParentCertainty == evidenceLegacy.String():
			s.Coverage.UnverifiedParents++
		}
		if s.projection.res != nil {
			if x := s.projection.res.Executions[n.execution]; x != nil && s.projection.unqueried(x, readFor[n.ID]) {
				s.Coverage.Unqueried++
			}
		}
	}
	s.Totals = parts[0].Totals
	s.GroupTotals = delegationWorkspaceTotals(s.Nodes)
	return s, nil
}
