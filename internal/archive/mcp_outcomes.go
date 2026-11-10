package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// The outcome and file-history MCP tools (docs/projects-and-outcomes.md).
// Both read stored facts only, and answer as fully for a session in a plain
// folder as for one in a repository: the label comes from the transcript,
// and Git adds commits where local Git checked them.

var outcomeMCPTools = []map[string]any{
	{"name": "list_outcomes", "description": "List sessions with their outcome label (landed, committed, changed_kept, changed_recent, reworked, no_changes, unknown), the evidence that decided it (outcome_why) and its tier (transcript or local Git), newest first, with counts per label for the filters. Works with or without Git: labels come from each session's own tool calls, and local Git adds commit evidence. Pass workspace_ids for full evidence, including each linked commit's method and confidence.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"project":           map[string]any{"type": "string", "description": "A project name from list_projects: a repository, or a folder as ~/path."},
			"label":             map[string]any{"type": "string", "enum": outcomeLabels},
			"workspace_ids":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 100},
			"from":              map[string]any{"type": "string", "description": "Sessions last active at or after this time: an RFC 3339 timestamp or a UTC date."},
			"to":                map[string]any{"type": "string", "description": "Sessions last active at or before this time; a date includes its whole day (UTC)."},
			"limit":             map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
			"offset":            map[string]any{"type": "integer", "minimum": 0},
			"max_output_tokens": map[string]any{"type": "integer", "minimum": 200, "maximum": 4000}}}},
	{"name": "get_file_history", "description": "Why is this file like this? Every session that edited a file, newest first: its edits, lines added and removed, outcome label with its evidence, and a reader link at its first edit of the file. In a repository local Git checked, also the commits that changed the file, each with the sessions linked to it. Works for files in plain folders from transcripts alone. path is repository-relative (src/main.go), a folder path (~/notes/plan.md) or an absolute path; a path that matches exactly wins, and only otherwise does a path ending match. files_truncated means more files matched than were returned: give a longer path or a project.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"path"}, "properties": map[string]any{
			"path":              map[string]any{"type": "string"},
			"project":           map[string]any{"type": "string", "description": "Only the file in this project, when several projects hold the path."},
			"limit":             map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "Sessions and commits per file; default 20."},
			"max_output_tokens": map[string]any{"type": "integer", "minimum": 200, "maximum": 4000}}}},
}

func init() { mcpTools = append(mcpTools, outcomeMCPTools...) }

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
		` + gitFactSQL("w", "sg.linked_commits") + ` linked_commits,` + gitFactSQL("w", "sg.landed_commits") + ` landed_commits
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
	result, err := c.FileHistory(ctx, FileHistoryOptions{Path: firstString(args["path"]), Project: firstString(args["project"]), Limit: limit})
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
