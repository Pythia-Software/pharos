package archive

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Bound discovery before reading any optional enrichment. These are catalog
// records and extractive documents, never message scans or live derivations.
var recentConversationCandidates = 200

// Bound descendant metadata too, including exceptionally large imported
// families. Never display a truncated traversal as an exact helper depth.
var recentConversationFamilyLimit = 10000

const recentConversationCount = 10

type recentConversation struct {
	ID            string `json:"id"`
	WorkspaceID   string `json:"workspace_id"`
	Title         string `json:"title"`
	Repository    string `json:"repository,omitempty"`
	RepositoryID  string `json:"-"`
	Provider      string `json:"provider"`
	Model         string `json:"model,omitempty"`
	ActivityAt    string `json:"activity_at,omitempty"`
	FirstInput    string `json:"first_input,omitempty"`
	LastResponse  string `json:"last_response,omitempty"`
	SubagentDepth int    `json:"subagent_depth"`
	LabelsReady   bool   `json:"labels_ready"`
	EventsReady   bool   `json:"events_ready"`
}

func (c *Catalog) recentConversations(ctx context.Context, labels, events bool) ([]recentConversation, error) {
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	// Optional ledgers do not exist until the user enables their passes. Do
	// not create them, run inference, or treat a zero-event result as pending.
	tables, err := queryMapsContext(ctx, c.reader(), `SELECT name FROM sqlite_master WHERE type='table'
		AND name IN ('message_labels_conversations','message_labels_dirty','agent_events_conversations','agent_events_dirty')`)
	if err != nil {
		return nil, err
	}
	has := map[string]bool{}
	for _, table := range tables {
		has[firstString(table["name"])] = true
	}
	labelsSQL, eventsSQL := "0", "0"
	if labels && has["message_labels_conversations"] && has["message_labels_dirty"] {
		labelsSQL = `EXISTS(SELECT 1 FROM message_labels_conversations l WHERE l.conversation_id=c.id AND l.units>0)
			AND NOT EXISTS(SELECT 1 FROM message_labels_dirty ld WHERE ld.conversation_id=c.id)`
	}
	if events && has["agent_events_conversations"] && has["agent_events_dirty"] {
		eventsSQL = `EXISTS(SELECT 1 FROM agent_events_conversations e WHERE e.conversation_id=c.id)
			AND NOT EXISTS(SELECT 1 FROM agent_events_dirty ed WHERE ed.conversation_id=c.id)`
	}
	// The launch step is written family-first (CROSS JOIN): left to choose, the
	// planner starts it from every conversation without a parent, on each
	// step of the recursion.
	// Match usageConversationRoots' distinction between delegated runs and
	// Codex filename continuations. Session depths include agent_depth at
	// ingestion, so subtract that base before adding the family's depth.
	rows, err := queryMapsContext(ctx, c.reader(), `WITH RECURSIVE recent AS MATERIALIZED (
		SELECT c.id,c.workspace_id,c.provider,c.model,COALESCE(c.ended_at,c.started_at) activity_at
		FROM conversations c WHERE c.parent_id IS NULL AND c.agent_depth=0 AND `+currentGroupConversation("c")+`
		AND NOT EXISTS(SELECT 1 FROM conversation_launches l WHERE l.child_id=c.id)
		ORDER BY COALESCE(c.ended_at,c.started_at) DESC,c.id LIMIT ?
	), family(root_id,id,depth) AS (
		SELECT id,id,0 FROM recent
		UNION ALL
		SELECT f.root_id,child.id,f.depth+CASE WHEN child.agent_depth>0 OR child.provider<>'codex'
			THEN MAX(1,child.agent_depth-parent.agent_depth) ELSE 0 END
		FROM family f JOIN conversations parent ON parent.id=f.id
		JOIN conversations child ON child.parent_id=f.id WHERE `+currentGroupConversation("child")+`
		UNION ALL
		SELECT f.root_id,child.id,f.depth+1+MAX(0,COALESCE(owner.depth,0)-parent.agent_depth)
		FROM family f CROSS JOIN conversations parent ON parent.id=f.id
		CROSS JOIN conversation_launches l ON l.parent_id=f.id
		CROSS JOIN conversations child ON child.id=l.child_id
		LEFT JOIN tool_calls t ON t.id=l.tool_call_id
		LEFT JOIN agent_sessions owner ON owner.id=t.agent_session_id
		WHERE child.parent_id IS NULL AND `+currentGroupConversation("child")+` LIMIT ?
	), depths AS MATERIALIZED (
		SELECT f.root_id,MAX(f.depth+MAX(0,COALESCE((SELECT MAX(a.depth) FROM agent_sessions a
			WHERE a.conversation_id=f.id),0)-c.agent_depth)) subagent_depth
		FROM family f JOIN conversations c ON c.id=f.id GROUP BY f.root_id
	) SELECT c.*,w.title,w.repository_id,r.display_name repository,d.initiation,d.outcome,
		depths.subagent_depth,(SELECT COUNT(*) FROM family)>? family_limited,
		(`+labelsSQL+`) labels_ready,(`+eventsSQL+`) events_ready
		FROM recent c JOIN workspaces w ON w.id=c.workspace_id
		JOIN depths ON depths.root_id=c.id
		JOIN conversation_documents d ON d.conversation_id=c.id
		LEFT JOIN repositories r ON r.id=w.repository_id
		WHERE COALESCE(d.initiation,'')<>'' OR COALESCE(d.outcome,'')<>''
		ORDER BY c.activity_at DESC,c.id`, recentConversationCandidates, recentConversationFamilyLimit+1, recentConversationFamilyLimit)
	if err != nil {
		return nil, err
	}
	candidates := make([]recentConversation, 0, len(rows))
	for _, row := range rows {
		if integer(row["family_limited"]) != 0 {
			return nil, fmt.Errorf("recent conversation families exceed the %d-conversation discovery limit", recentConversationFamilyLimit)
		}
		candidates = append(candidates, recentConversation{
			ID: firstString(row["id"]), WorkspaceID: firstString(row["workspace_id"]), Title: firstString(row["title"]),
			RepositoryID: firstString(row["repository_id"]), Repository: firstString(row["repository"]),
			Provider: firstString(row["provider"]), Model: firstString(row["model"]), ActivityAt: firstString(row["activity_at"]),
			FirstInput: firstString(row["initiation"]), LastResponse: firstString(row["outcome"]), SubagentDepth: int(integer(row["subagent_depth"])),
			LabelsReady: integer(row["labels_ready"]) != 0, EventsReady: integer(row["events_ready"]) != 0,
		})
	}
	return selectRecentConversations(candidates), nil
}

func recentEnrichment(item recentConversation) int {
	count := 0
	if item.LabelsReady {
		count++
	}
	if item.EventsReady {
		count++
	}
	return count
}

// Candidates arrive newest first. Prefer the last month of the library's
// activity (also useful for an older imported archive), retaining enough older
// entries to fill a small library. All goals are preferences inside this pool.
func selectRecentConversations(candidates []recentConversation) []recentConversation {
	if len(candidates) > recentConversationCount {
		if newest, ok := parseTime(candidates[0].ActivityAt); ok {
			cutoff := newest.AddDate(0, 0, -30)
			end := recentConversationCount
			for end < len(candidates) {
				at, ok := parseTime(candidates[end].ActivityAt)
				if !ok || at.Before(cutoff) {
					break
				}
				end++
			}
			candidates = candidates[:end]
		}
	}
	count := min(recentConversationCount, len(candidates))
	withAgents := 0
	for _, item := range candidates {
		if item.SubagentDepth > 0 {
			withAgents++
		}
	}
	agentTarget := min((count+1)/2, withAgents)
	selected := make([]recentConversation, 0, count)
	used := map[string]bool{}
	repos, providers, models, depths := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[int]bool{}
	agents := 0
	for len(selected) < count {
		// Reserve alternating places for conversations with helpers when
		// available; the other places can favor any shape of conversation.
		needAgents := agents < agentTarget && (len(selected)%2 == 0 || count-len(selected) <= agentTarget-agents)
		best, bestScore := -1, -100000
		for index, item := range candidates {
			if used[item.ID] || needAgents && item.SubagentDepth == 0 {
				continue
			}
			score := recentEnrichment(item)*100 - index/8
			if item.RepositoryID != "" && !repos[item.RepositoryID] {
				score += 30
			}
			if item.Provider != "" && !providers[item.Provider] {
				score += 24
			}
			if item.Model != "" && !models[item.Model] {
				score += 12
			}
			if !depths[item.SubagentDepth] {
				score += 18
			}
			if score > bestScore {
				best, bestScore = index, score
			}
		}
		if best < 0 {
			break
		}
		item := candidates[best]
		selected = append(selected, item)
		used[item.ID], repos[item.RepositoryID], providers[item.Provider], models[item.Model], depths[item.SubagentDepth] = true, true, true, true, true
		if item.SubagentDepth > 0 {
			agents++
		}
	}
	// Surface enriched entries first; preserve discovery order within a tier.
	sort.SliceStable(selected, func(i, j int) bool { return recentEnrichment(selected[i]) > recentEnrichment(selected[j]) })
	return selected
}
