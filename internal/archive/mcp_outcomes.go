package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// The outcome and file-history MCP tools (docs/projects-and-outcomes.md).
// Both read stored facts only, and answer as fully for a session in a plain
// folder as for one in a repository: the label comes from the transcript,
// and Git adds commits where local Git checked them.

var outcomeMCPTools = []map[string]any{
	{"name": "list_outcomes", "description": "List sessions with their outcome label (landed, committed, changed_kept, changed_recent, reworked, no_changes, unknown), the evidence that decided it (outcome_why) and its tier (transcript or local Git), newest first, with counts per label for the filters. Works with or without Git: labels come from each session's own tool calls, and local Git adds commit evidence and the lines its commits added (committed_lines, landed_lines). Pass workspace_ids for full evidence, including each linked commit's method and confidence.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"project":           map[string]any{"type": "string", "description": "A project name from list_projects: a repository, or a folder as ~/path."},
			"label":             map[string]any{"type": "string", "enum": outcomeLabels},
			"workspace_ids":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 100},
			"from":              map[string]any{"type": "string", "description": "Sessions last active at or after this time: an RFC 3339 timestamp or a UTC date."},
			"to":                map[string]any{"type": "string", "description": "Sessions last active at or before this time; a date includes its whole day (UTC)."},
			"limit":             map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
			"offset":            map[string]any{"type": "integer", "minimum": 0},
			"max_output_tokens": map[string]any{"type": "integer", "minimum": 200, "maximum": 4000}}}},
	{"name": "get_file_history", "description": "Why is this file like this? Every session that edited a file, newest first: its edits, each a reader link to the call that made it (places), lines added and removed, outcome label with its evidence, and the sessions whose lines it removed (reworked) or that removed its lines (reworked_by). In a repository local Git checked, also the commits that changed the file, each with the sessions linked to it and a reader link to where each printed or made it. Works for files in plain folders from transcripts alone. path is repository-relative (src/main.go), a folder path (~/notes/plan.md) or an absolute path; with project, a repository-relative path is that repository's file even when no session touched it. Each file has url, its page on the Files page with its project's tree.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"path"}, "properties": map[string]any{
			"path":              map[string]any{"type": "string"},
			"project":           map[string]any{"type": "string", "description": "Only the file in this project, when several projects hold the path: a name or folder as list_projects gives it, or repository:<id>."},
			"limit":             map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "Sessions and commits per file; default 20."},
			"max_output_tokens": map[string]any{"type": "integer", "minimum": 200, "maximum": 4000}}}},
	{"name": "get_branch_spend", "description": "What agents spent on each branch of your repositories: cost (API-price equivalent, as the Library prices it), tokens and sessions per branch, most spent first, and the coverage split (on a branch per turn or per session, unknown branch, no branch data) that adds up to the repository sessions' whole spend. A session that switched branches is split turn by turn. Sessions in folders have no branches and are not counted; for them use query_statistics. Same numbers as Analyze → Spend by Branch.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"project":           map[string]any{"type": "string", "description": "A repository from list_projects."},
			"branch":            map[string]any{"type": "string", "description": "Only branches whose name contains this text."},
			"from":              map[string]any{"type": "string", "description": "Usage in UTC hours starting at or after this time: an RFC 3339 timestamp or a UTC date. Spend is placed in the hour it was used."},
			"to":                map[string]any{"type": "string", "description": "Usage in UTC hours starting before this time; a date includes its whole day."},
			"limit":             map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "Branches listed; default 20."},
			"max_output_tokens": map[string]any{"type": "integer", "minimum": 200, "maximum": 4000}}}},
}

func init() { mcpTools = append(mcpTools, outcomeMCPTools...) }

// getBranchSpend is the get_branch_spend tool: the branch_usage dataset's
// rows, summed per branch and per coverage.
func (c *Catalog) getBranchSpend(ctx context.Context, args map[string]any) (map[string]any, error) {
	limit := clamp(int(integer(valueOr(args["limit"], 20))), 1, 100)
	budget := mcpBudget(args, 1500)
	project, branchText := firstString(args["project"]), strings.ToLower(firstString(args["branch"]))
	// Rows are hourly: the bounds select the hours starting in [from, to),
	// compared as instants, so a bound's offset counts.
	var from, to time.Time
	for key, bound := range map[string]*time.Time{"from": &from, "to": &to} {
		if value := strings.TrimSpace(firstString(args[key])); value != "" {
			parsed, ok := parseTime(value)
			if !ok {
				return nil, fmt.Errorf("%s must be a valid timestamp or date", key)
			}
			if key == "to" && len(value) == len("2006-01-02") {
				parsed = parsed.Add(24 * time.Hour)
			}
			*bound = parsed
		}
	}
	var inProject map[string]bool
	if project != "" {
		rows, err := queryMapsContext(ctx, c.reader(), `SELECT w.id FROM workspaces w`+projectJoin("w")+` WHERE `+projectPredicate("w"), projectArgs(project)...)
		if err != nil {
			return nil, err
		}
		inProject = map[string]bool{}
		for _, row := range rows {
			inProject[firstString(row["id"])] = true
		}
	}
	rows, err := c.branchUsageRows(ctx)
	if err != nil {
		return nil, err
	}
	type total struct {
		cost     any
		tokens   int64
		sessions map[string]bool
		recorded map[string]bool
		status   string
		projects map[string]bool
	}
	add := func(t *total, row map[string]any) {
		if value, ok := row["cost_usd"].(float64); ok {
			t.cost = addCost(t.cost, &value)
		}
		t.tokens += integer(row["tokens"])
		t.sessions[firstString(row["workspace_id"])] = true
		if source := firstString(row["branch_source"]); source != "" {
			t.recorded[source] = true
		}
		t.status = worsePriceStatus(t.status, firstString(row["price_status"]))
		if name := firstString(row["project_name"]); name != "" {
			t.projects[name] = true
		}
	}
	newTotal := func() *total {
		return &total{sessions: map[string]bool{}, recorded: map[string]bool{}, projects: map[string]bool{}}
	}
	branches, coverage := map[string]*total{}, map[string]*total{}
	for _, row := range rows {
		at, _ := parseTime(firstString(row["usage_at"]))
		if inProject != nil && !inProject[firstString(row["workspace_id"])] || !from.IsZero() && at.Before(from) || !to.IsZero() && !at.Before(to) {
			continue
		}
		branch := firstString(row["branch"])
		if branchText != "" && !strings.Contains(strings.ToLower(branch), branchText) {
			continue
		}
		key := firstString(row["coverage"])
		if coverage[key] == nil {
			coverage[key] = newTotal()
		}
		add(coverage[key], row)
		if branch != "" {
			if branches[branch] == nil {
				branches[branch] = newTotal()
			}
			add(branches[branch], row)
		}
	}
	keys := func(set map[string]bool) []string { return slices.Sorted(maps.Keys(set)) }
	describe := func(t *total) map[string]any {
		item := map[string]any{"cost_usd": t.cost, "tokens": t.tokens, "sessions": len(t.sessions)}
		if t.status != "" && t.status != "priced" {
			item["price_status"] = t.status
		}
		return item
	}
	items := []map[string]any{}
	for name, t := range branches {
		item := describe(t)
		item["branch"], item["recorded"], item["projects"] = name, keys(t.recorded), keys(t.projects)
		items = append(items, item)
	}
	slices.SortFunc(items, func(a, b map[string]any) int {
		if left, right := floatOr(a["cost_usd"]), floatOr(b["cost_usd"]); left != right {
			if left > right {
				return -1
			}
			return 1
		}
		return strings.Compare(firstString(a["branch"]), firstString(b["branch"]))
	})
	split := map[string]any{}
	for key, t := range coverage {
		split[key] = describe(t)
	}
	result := map[string]any{"total": len(items), "coverage": split,
		"note": "coverage adds up to the spend of every repository session in the filters: on_branch (per turn when Claude Code recorded the branch on each turn, per session when the source records one branch per session), unknown (before a session's first branch, on a detached HEAD, or not placed on a turn) and no_branch_data. recorded says how each branch was known. Cost is an API-price equivalent; price_status flags a partly priced total."}
	if len(coverage) == 0 {
		result["note"] = "No repository session has spend in these filters. Spend by branch covers Git repositories only; for spend in folders, or per project, use query_statistics with group_by project and a cost_usd sum."
	}
	if len(items) > limit {
		items = items[:limit]
		result["truncated"] = true
	}
	result["items"] = items
	fitConversationItems(result, budget)
	return result, nil
}

// listOutcomes is the list_outcomes tool.
func (c *Catalog) listOutcomes(ctx context.Context, args map[string]any) (map[string]any, error) {
	limit := clamp(int(integer(valueOr(args["limit"], 20))), 1, 100)
	offset := max(int(integer(args["offset"])), 0)
	budget := mcpBudget(args, 2000)
	where, values := []string{"w.id NOT IN (SELECT workspace_id FROM tool_mirror_workspaces)"}, []any{}
	if project := firstString(args["project"]); project != "" {
		where = append(where, projectPredicate("w"))
		values = append(values, projectArgs(project)...)
	}
	ids := stringSlice(args["workspace_ids"])
	if len(ids) > 100 {
		return nil, fmt.Errorf("workspace_ids takes at most 100 IDs")
	}
	if len(ids) > 0 {
		where = append(where, "w.id IN (SELECT value FROM json_each(?))")
		values = append(values, jsonText(ids))
	}
	// Times compare as instants, so a bound with an offset means what it says.
	for _, key := range []string{"from", "to"} {
		if value := firstString(args[key]); value != "" {
			if _, valid := parseTime(strings.TrimSpace(value)); !valid {
				return nil, fmt.Errorf("%s must be a valid timestamp or date", key)
			}
			operator := ">="
			if key == "to" {
				operator = "<="
			}
			where = append(where, "julianday(w.activity_at)"+operator+"julianday(?)")
			values = append(values, timeBound(value, key == "to"))
		}
	}
	label := firstString(args["label"])
	if label != "" && !slices.Contains(outcomeLabels, label) {
		return nil, fmt.Errorf("label must be one of %s", strings.Join(outcomeLabels, ", "))
	}
	sessions := `SELECT w.id workspace_id,w.title,w.source_kind source,w.activity_at,
		` + projectNameSQL("w") + ` project,` + projectKindSQL("w") + ` project_kind,` + outcomeLabelSQL("w") + ` outcome_label,` + outcomeWhySQL("w") + ` outcome_why,
		` + outcomeEvidenceTierSQL("w") + ` evidence_tier,` + gitCheckSQL("w") + ` git_check,` + gitCheckReasonSQL("w") + ` git_check_reason,
		so.files_changed,so.lines_added,so.lines_removed,so.kept_lines,so.rework_removed_lines,so.verification,
		` + gitFactSQL("w", "sg.linked_commits") + ` linked_commits,` + gitFactSQL("w", "sg.landed_commits") + ` landed_commits,
		` + gitFactSQL("w", "sg.lines_added") + ` committed_lines,` + gitFactSQL("w", "sg.lines_landed") + ` landed_lines
		FROM workspaces w` + projectJoin("w") + ` LEFT JOIN session_outcomes so ON so.workspace_id=w.id
		WHERE ` + strings.Join(where, " AND ")
	counted, err := queryMapsContext(ctx, c.reader(), `SELECT outcome_label,COUNT(*) n FROM (`+sessions+`) GROUP BY outcome_label`, values...)
	if err != nil {
		return nil, err
	}
	counts, total := map[string]int{}, 0
	for _, row := range counted {
		counts[firstString(row["outcome_label"])] = int(integer(row["n"]))
		if label == "" || row["outcome_label"] == label {
			total += int(integer(row["n"]))
		}
	}
	filter := ""
	if label != "" {
		filter = " WHERE outcome_label=?"
		values = append(values, label)
	}
	items, err := queryMapsContext(ctx, c.reader(), `SELECT * FROM (`+sessions+`)`+filter+` ORDER BY COALESCE(activity_at,'') DESC,workspace_id LIMIT ? OFFSET ?`,
		append(values, limit, offset)...)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"total": total, "labels": counts}
	// Asked about by ID, a session's linked commits are its evidence.
	if len(ids) > 0 {
		for _, item := range items {
			links, err := queryMapsContext(ctx, c.reader(), `SELECT l.hash,l.method,l.confidence,l.evidence_json,g.on_mainline,g.reverted_by,g.author_at
				FROM session_commit_links l LEFT JOIN git_commits g ON g.repository_id=l.repository_id AND g.hash=l.hash
				WHERE l.workspace_id=? ORDER BY g.author_at,l.hash`, item["workspace_id"])
			if err != nil {
				return nil, err
			}
			for _, link := range links {
				var evidence any
				_ = json.Unmarshal([]byte(firstString(link["evidence_json"])), &evidence)
				link["evidence"] = evidence
				delete(link, "evidence_json")
				link["counted"] = floatOr(link["confidence"]) >= outcomeGitCounted
			}
			item["commits"] = links
		}
	}
	result["items"] = items
	result["note"] = "outcome_why names the evidence and its tier. Landed and committed need local Git; every other label comes from the session's own tool calls, so sessions in plain folders are labelled too. Commit links below confidence 0.7 are candidates and are not counted."
	// The next page starts after the last item that fit the budget; one
	// that fits none is an error, not a page that never advances.
	if err := fitAuditPage(result, budget, offset, total); err != nil {
		return nil, err
	}
	return result, nil
}

// getFileHistory is the get_file_history tool.
func (c *Catalog) getFileHistory(ctx context.Context, args map[string]any) (map[string]any, error) {
	limit := clamp(int(integer(valueOr(args["limit"], 20))), 1, 100)
	// Three edits per session lead to the rest, which the file's page lists.
	result, err := c.FileHistory(ctx, FileHistoryOptions{Path: firstString(args["path"]), Project: firstString(args["project"]), Limit: limit, Places: 3})
	if err != nil {
		return nil, err
	}
	// Fit the budget by shedding the oldest sessions and commits first, then
	// files past the first, which a narrower path or project names.
	budget := mcpBudget(args, 2500)
	files, _ := result["files"].([]map[string]any)
	for len(jsonText(result)) > budget*3 {
		shed := false
		for _, file := range files {
			for _, key := range []string{"commits", "sessions"} {
				if list, _ := file[key].([]map[string]any); len(list) > 1 {
					file[key] = list[:len(list)-1]
					file["truncated"], shed = true, true
				}
			}
		}
		if !shed && len(files) > 1 {
			files = files[:len(files)-1]
			result["files"], result["files_truncated"], shed = files, true, true
		}
		if !shed {
			return nil, fmt.Errorf("max_output_tokens is too small for one file's history; increase the output budget")
		}
	}
	return result, nil
}
