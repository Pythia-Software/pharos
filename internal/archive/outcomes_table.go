package archive

import (
	"context"
	"time"
)

// The outcomes dataset is the Analyze → Outcomes dashboard's records: one row
// per session the Library lists (mirrors folded the same way), with its
// outcome label and the evidence tier that decided it, its project and
// branch, the model that took most of its cost, its priced usage, its Tier 1
// facts (files, lines, rework) and its Tier 2 commits and the lines they
// added. See docs/projects-and-outcomes.md.

// outcomeEvidenceTier is the highest evidence tier a session reached: local
// Git checked it, its transcript recorded tool calls, or neither.
func outcomeEvidenceTierSQL(w string) string {
	return `CASE WHEN EXISTS(SELECT 1 FROM session_git sg WHERE sg.workspace_id=` + w + `.id AND sg.status='checked') THEN 'local_git'
		WHEN EXISTS(SELECT 1 FROM session_outcomes so WHERE so.workspace_id=` + w + `.id AND so.tool_calls_recorded>0) THEN 'transcript' ELSE 'none' END`
}

// outcomeKeptLabels are the labels of a session whose changes were kept:
// cost per kept change divides by them.
const outcomeKeptLabels = `('landed','committed','changed_kept')`

func (c *Catalog) outcomeRows(ctx context.Context) ([]map[string]any, error) {
	return cachedValue(ctx, c, "outcome-rows:"+c.clock().Format("2006-01-02"), func(ctx context.Context) ([]map[string]any, error) {
		rows, err := queryMapsContext(ctx, c.reader(), `SELECT w.id,w.id workspace_id,w.title,w.source_kind,w.activity_at,
			`+projectNameSQL("w")+` project_name,`+projectKindSQL("w")+` project_kind,
			CASE WHEN `+projectKindSQL("w")+`='repository' THEN NULLIF(TRIM(COALESCE(w.branch,'')),'') END branch,
			label outcome_label,`+outcomeEvidenceTierSQL("w")+` evidence_tier,`+gitCheckSQL("w")+` git_check,`+gitCheckReasonSQL("w")+` git_check_reason,
			CASE WHEN label IN `+outcomeKeptLabels+` THEN 1 ELSE 0 END kept_change,
			so.files_changed,so.lines_added,so.lines_removed,so.kept_lines,so.rework_removed_lines,so.first_edit_at,
			(SELECT sg.linked_commits FROM session_git sg WHERE sg.workspace_id=w.id AND sg.status='checked') linked_commits,
			(SELECT sg.landed_commits FROM session_git sg WHERE sg.workspace_id=w.id AND sg.status='checked') landed_commits,
			(SELECT sg.lines_added FROM session_git sg WHERE sg.workspace_id=w.id AND sg.status='checked') committed_lines,
			(SELECT sg.lines_landed FROM session_git sg WHERE sg.workspace_id=w.id AND sg.status='checked') landed_lines
			FROM (SELECT w.*,`+outcomeLabelSQL("w")+` label FROM workspaces w) w`+projectJoin("w")+`
			LEFT JOIN session_outcomes so ON so.workspace_id=w.id`)
		if err != nil {
			return nil, err
		}
		rows = c.suppressMirrors(rows)
		usage, err := c.sessionModelUsage(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			row["started_at"] = defaultString(firstString(row["first_edit_at"]), firstString(row["activity_at"]))
			row["cost_usd"], row["tokens"], row["provider"], row["model_family"] = nil, int64(0), nil, nil
			if session := usage[firstString(row["id"])]; session != nil {
				row["cost_usd"], row["tokens"], row["price_status"] = session.cost, session.tokens, session.status
				row["provider"], row["model_family"] = nilIfEmpty(session.provider), nilIfEmpty(modelFamily(session.model))
				if session.first != "" {
					row["started_at"] = session.first
				}
			}
			delete(row, "first_edit_at")
		}
		return rows, nil
	})
}

// sessionUsage is one session's priced usage and the model that took most of
// its cost (or, unpriced, most of its tokens).
type sessionUsage struct {
	workspaceUsageTotal
	provider, model, first string
}

// sessionModelUsage prices the usage ledger per session as workspaceUsage
// does, and finds each session's main model and first usage hour.
func (c *Catalog) sessionModelUsage(ctx context.Context) (map[string]*sessionUsage, error) {
	ledger, err := c.usageLedger(nil)
	if err != nil {
		return nil, err
	}
	book, err := c.loadPriceBook()
	if err != nil {
		return nil, err
	}
	type share struct {
		provider string
		cost     float64
		tokens   int64
	}
	sessions := map[string]*sessionUsage{}
	shares := map[string]map[string]*share{}
	for _, entry := range ledger {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if integer(entry["current_group_member"]) == 0 {
			continue
		}
		day, hour := "", firstString(entry["usage_hour"])
		if at, ok := parseTime(hour); ok {
			day = localMidnight(at, time.Local).Format("2006-01-02")
			hour = at.UTC().Format(time.RFC3339)
		}
		model := ledgerModel(entry)
		tokens := ledgerTokens(entry)
		priced := book.cost(model, day, tokens)
		id := firstString(entry["workspace_id"])
		session := sessions[id]
		if session == nil {
			session = &sessionUsage{}
			sessions[id], shares[id] = session, map[string]*share{}
		}
		session.tokens += tokens["total_tokens"]
		session.cost = addCost(session.cost, priced.cost)
		session.today = addCost(session.today, priced.costToday)
		session.status = worsePriceStatus(session.status, priced.status)
		if hour != "" && (session.first == "" || hour < session.first) {
			session.first = hour
		}
		part := shares[id][model]
		if part == nil {
			part = &share{provider: firstString(entry["provider"])}
			shares[id][model] = part
		}
		if priced.cost != nil {
			part.cost += *priced.cost
		}
		part.tokens += tokens["total_tokens"]
	}
	for id, session := range sessions {
		best := ""
		for model, part := range shares[id] {
			if current := shares[id][best]; best == "" || part.cost > current.cost || part.cost == current.cost && (part.tokens > current.tokens || part.tokens == current.tokens && model < best) {
				best = model
			}
		}
		session.model, session.provider = best, shares[id][best].provider
	}
	return sessions, nil
}
