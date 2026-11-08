package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
)

// workPeer is related top-level work shown beside a workspace: the chip strip
// on its page, and its family in the Hierarchy and Timeline when certain.
type workPeer struct {
	WorkspaceID string           `json:"workspace_id"`
	Title       string           `json:"title"`
	Provider    string           `json:"provider"`
	StartedAt   string           `json:"started_at,omitempty"`
	EndedAt     string           `json:"ended_at,omitempty"`
	Role        string           `json:"role"`
	Confidence  string           `json:"confidence"`
	Via         string           `json:"via,omitempty"`
	Evidence    []workPeerReason `json:"evidence"`
}

// workPeerReason is one stored link that relates a peer.
type workPeerReason struct {
	Kind       string         `json:"kind"`
	Confidence string         `json:"confidence"`
	From       string         `json:"from_workspace_id"`
	To         string         `json:"to_workspace_id"`
	FromMsg    string         `json:"from_message_id,omitempty"`
	ToMsg      string         `json:"to_message_id,omitempty"`
	At         string         `json:"at,omitempty"`
	Details    map[string]any `json:"details"`
}

const (
	peerRoleReviewer    = "reviewer"
	peerRoleImplementer = "implementer"
	peerRoleRelated     = "related"
)

// peerRole is the part a link's endpoint played: whoever read another's
// fresh lines in a diff reviewed them; the other side implemented. A quoted
// reply alone names no role: on a full library copies carried plans and
// debugging output between tasks as often as review findings.
func peerRole(kind string, left bool) string {
	if kind == "read_written_by" {
		if left {
			return peerRoleImplementer
		}
		return peerRoleReviewer
	}
	return peerRoleRelated
}

// peerKindRank orders the evidence a role is taken from.
var peerKindRank = map[string]int{"read_written_by": 0, "copied_into": 1, "tl1_candidate": 2}

func (c *Catalog) peerRepresentatives(ctx context.Context) (map[string]string, error) {
	return cachedValue(ctx, c, "peer-representatives", c.authorshipRepresentatives)
}

// peerRootWorkspace is the workspace links are stored under for a page: the
// mirror representative of the workspace holding the top-level conversation
// of the page's first family.
func (c *Catalog) peerRootWorkspace(ctx context.Context, workspace string) (string, error) {
	var conversation string
	err := c.reader().QueryRowContext(ctx, `SELECT id FROM conversations WHERE workspace_id=?
		ORDER BY parent_id IS NOT NULL,started_at IS NULL,started_at,id LIMIT 1`, workspace).Scan(&conversation)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	root := workspace
	seen := map[string]bool{conversation: true}
	for range 64 {
		var parent, launcher sql.NullString
		if err := c.reader().QueryRowContext(ctx, `SELECT p.id,p.workspace_id FROM conversations c JOIN conversations p ON p.id=c.parent_id WHERE c.id=?`, conversation).Scan(&parent, &launcher); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		if !parent.Valid {
			if err := c.reader().QueryRowContext(ctx, `SELECT p.id,p.workspace_id FROM conversation_launches l JOIN conversations p ON p.id=l.parent_id WHERE l.child_id=?`, conversation).Scan(&parent, &launcher); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return "", err
			}
		}
		if !parent.Valid || seen[parent.String] {
			break
		}
		seen[parent.String] = true
		conversation, root = parent.String, launcher.String
	}
	representatives, err := c.peerRepresentatives(ctx)
	if err != nil {
		return "", err
	}
	if rep := representatives[root]; rep != "" {
		root = rep
	}
	return root, nil
}

// workPeers lists the work related to a workspace: every direct link, and
// work linked to a certain peer by another certain link (a second review of
// the same implementation). A peer is certain when one of its links is, or
// when two kinds of evidence agree.
func (c *Catalog) workPeers(ctx context.Context, workspace string) ([]workPeer, error) {
	root, err := c.peerRootWorkspace(ctx, workspace)
	if err != nil || root == "" {
		return []workPeer{}, err
	}
	edges := func(id string) ([]workPeerReason, error) {
		rows, err := c.reader().QueryContext(ctx, `SELECT left_workspace_id,right_workspace_id,kind,confidence,left_message_id,right_message_id,COALESCE(at,''),evidence_json
			FROM work_peer_links WHERE left_workspace_id=? UNION ALL
			SELECT left_workspace_id,right_workspace_id,kind,confidence,left_message_id,right_message_id,COALESCE(at,''),evidence_json
			FROM work_peer_links WHERE right_workspace_id=? AND left_workspace_id<>?`, id, id, id)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []workPeerReason{}
		for rows.Next() {
			var r workPeerReason
			var evidence string
			if err := rows.Scan(&r.From, &r.To, &r.Kind, &r.Confidence, &r.FromMsg, &r.ToMsg, &r.At, &evidence); err != nil {
				return nil, err
			}
			_ = json.Unmarshal([]byte(evidence), &r.Details)
			out = append(out, r)
		}
		return out, rows.Err()
	}
	other := func(r workPeerReason, id string) string {
		if r.From == id {
			return r.To
		}
		return r.From
	}
	peers := map[string]*workPeer{}
	order := []string{}
	add := func(id, via string, r workPeerReason) {
		if id == root || id == workspace {
			return
		}
		p := peers[id]
		if p == nil {
			if len(order) >= peerMaxPeers {
				return
			}
			p = &workPeer{WorkspaceID: id, Via: via}
			peers[id] = p
			order = append(order, id)
		}
		if p.Via != "" && via == "" {
			p.Via = ""
			p.Evidence = nil
		}
		if p.Via == via {
			p.Evidence = append(p.Evidence, r)
		}
	}
	direct, err := edges(root)
	if err != nil {
		return nil, err
	}
	sortPeerReasons(direct)
	for _, r := range direct {
		add(other(r, root), "", r)
	}
	for _, r := range direct {
		if r.Confidence != peerHigh {
			continue
		}
		hop := other(r, root)
		second, err := edges(hop)
		if err != nil {
			return nil, err
		}
		sortPeerReasons(second)
		for _, s := range second {
			if s.Confidence == peerHigh {
				add(other(s, hop), hop, s)
			}
		}
	}
	out := []workPeer{}
	for _, id := range order {
		p := peers[id]
		kinds := map[string]bool{}
		role, rank := peerRoleRelated, len(peerKindRank)
		for _, r := range p.Evidence {
			kinds[r.Kind] = true
			if r.Confidence == peerHigh {
				p.Confidence = peerHigh
			}
			if k, ok := peerKindRank[r.Kind]; ok && k < rank {
				role, rank = peerRole(r.Kind, r.From == id), k
			}
		}
		if len(kinds) >= 2 {
			p.Confidence = peerHigh
		}
		if p.Confidence == "" {
			p.Confidence = peerLow
		}
		p.Role = role
		var title, provider, started, ended sql.NullString
		err := c.reader().QueryRowContext(ctx, `SELECT COALESCE(w.title,'Untitled work'),
			(SELECT provider FROM conversations WHERE workspace_id=w.id ORDER BY parent_id IS NOT NULL,started_at LIMIT 1),
			(SELECT MIN(started_at) FROM conversations WHERE workspace_id=w.id),
			(SELECT MAX(COALESCE(ended_at,started_at)) FROM conversations WHERE workspace_id=w.id)
			FROM workspaces w WHERE w.id=?`, id).Scan(&title, &provider, &started, &ended)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		p.Title, p.Provider, p.StartedAt, p.EndedAt = title.String, provider.String, started.String, ended.String
		out = append(out, *p)
	}
	// Certain work first, then in the order it started.
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Confidence == peerHigh) != (out[j].Confidence == peerHigh) {
			return out[i].Confidence == peerHigh
		}
		return out[i].StartedAt < out[j].StartedAt
	})
	return out, nil
}

func sortPeerReasons(reasons []workPeerReason) {
	sort.SliceStable(reasons, func(i, j int) bool {
		if (reasons[i].Confidence == peerHigh) != (reasons[j].Confidence == peerHigh) {
			return reasons[i].Confidence == peerHigh
		}
		return reasons[i].At < reasons[j].At
	})
}

// messagePeerAttacher marks the messages a link is anchored on: the reply a
// peer quoted (handoffs_out) and the prompt that quoted it (handoffs_in); the
// edit a reviewer later saw, and the diff it saw it in.
func (c *Catalog) messagePeerAttacher(conversationID string) (func(map[string]any), error) {
	rows, err := queryMaps(c.reader(), `SELECT l.left_message_id here,l.right_message_id there,'out' direction,l.kind,l.confidence,l.right_workspace_id workspace_id
		FROM work_peer_links l WHERE l.left_message_id<>'' AND l.left_message_id IN (SELECT id FROM messages WHERE conversation_id=?)
		UNION ALL
		SELECT l.right_message_id,l.left_message_id,'in',l.kind,l.confidence,l.left_workspace_id
		FROM work_peer_links l WHERE l.right_message_id<>'' AND l.right_message_id IN (SELECT id FROM messages WHERE conversation_id=?)`, conversationID, conversationID)
	if err != nil || len(rows) == 0 {
		return func(map[string]any) {}, err
	}
	targets := map[string]bool{}
	for _, row := range rows {
		if id := firstString(row["there"]); id != "" {
			targets[id] = true
		}
	}
	where := map[string]map[string]any{}
	if len(targets) > 0 {
		ids := make([]any, 0, len(targets))
		for id := range targets {
			ids = append(ids, id)
		}
		found, err := queryMaps(c.reader(), `SELECT m.id,m.conversation_id,c.workspace_id,COALESCE(w.title,'Untitled work') title FROM messages m
			JOIN conversations c ON c.id=m.conversation_id JOIN workspaces w ON w.id=c.workspace_id WHERE m.id IN (`+placeholders(len(ids))+`)`, ids...)
		if err != nil {
			return nil, err
		}
		for _, row := range found {
			where[firstString(row["id"])] = row
		}
	}
	byMessage := map[string]map[string][]map[string]any{}
	for _, row := range rows {
		target := where[firstString(row["there"])]
		if target == nil {
			continue
		}
		here, direction := firstString(row["here"]), firstString(row["direction"])
		if byMessage[here] == nil {
			byMessage[here] = map[string][]map[string]any{}
		}
		kind := firstString(row["kind"])
		byMessage[here][direction] = append(byMessage[here][direction], map[string]any{
			"kind": kind, "confidence": row["confidence"], "role": peerRole(kind, direction == "in"),
			"workspace_id": target["workspace_id"], "conversation_id": target["conversation_id"], "message_id": target["id"], "title": target["title"],
		})
	}
	return func(message map[string]any) {
		for direction, list := range byMessage[firstString(message["id"])] {
			message["handoffs_"+direction] = list
		}
	}, nil
}

// peerConversations lists the conversations of certain related work, so the
// work page's reader can open them in place from its hierarchy and timeline.
func (c *Catalog) peerConversations(ctx context.Context, peers []workPeer) ([]map[string]any, error) {
	out := []map[string]any{}
	for _, p := range peers {
		if p.Confidence != peerHigh {
			continue
		}
		rows, err := queryMapsContext(ctx, c.reader(), `SELECT c.id,c.workspace_id,c.provider,c.model,c.native_id,c.parent_id,c.started_at,c.ended_at,
			(SELECT substr(d.initiation,1,400) FROM conversation_documents d WHERE d.conversation_id=c.id) first_prompt
			FROM conversations c WHERE c.workspace_id=? ORDER BY c.started_at,c.id LIMIT 1000`, p.WorkspaceID)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			row["title"], row["peer_role"], row["peer"] = p.Title, p.Role, true
		}
		out = append(out, rows...)
	}
	return out, nil
}
