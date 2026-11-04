package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
)

// File history ("why is this file like this?", docs/projects-and-outcomes.md):
// every session that edited a path, from its own tool calls (session_files),
// with what it did, its outcome and a link into the reader at its first edit
// of the file. A file in a repository local Git checked also lists the
// commits that changed it (git_commit_files), each with the sessions linked
// to it. Requests read only stored facts; Git is never run for them.

// fileHistoryLimit bounds the sessions and commits listed per file.
var fileHistoryLimit = 200

type FileHistoryOptions struct {
	// Path is a repository-relative path, a folder path (~/… or absolute),
	// or an absolute path inside a checkout this Mac recorded.
	Path string
	// Project narrows to one project by name, when several hold the path.
	Project string
	Limit   int
}

// FileHistory finds the files Path names and returns each one's timeline,
// newest first.
func (c *Catalog) FileHistory(ctx context.Context, options FileHistoryOptions) (map[string]any, error) {
	query := strings.TrimSpace(options.Path)
	if query == "" {
		return nil, fmt.Errorf("path is required")
	}
	limit := options.Limit
	if limit <= 0 || limit > fileHistoryLimit {
		limit = fileHistoryLimit
	}
	keys, err := c.fileHistoryKeys(ctx, query)
	if err != nil {
		return nil, err
	}
	files := []map[string]any{}
	for _, key := range keys {
		file, err := c.fileTimeline(ctx, key, limit)
		if err != nil {
			return nil, err
		}
		if options.Project != "" && !strings.EqualFold(firstString(file["project"]), options.Project) {
			continue
		}
		files = append(files, file)
	}
	return map[string]any{"path": query, "files": files, "note": fileHistoryNote(files)}, nil
}

// fileHistoryNote says what the timeline covers, for a person or an agent
// reading an empty or Git-less answer.
func fileHistoryNote(files []map[string]any) string {
	if len(files) == 0 {
		return "No session in this library edited a file at this path. File history lists files sessions edited through their tool calls; try a repository-relative path, a folder path, or Library file search."
	}
	return "Sessions are those that edited the file through their tool calls, newest first. Commits are listed where local Git checked the repository."
}

// fileHistoryKeys are the session_files keys a query names: an exact path,
// a path ending in it, a folder path, or an absolute path sessions edited in
// a repository checkout.
func (c *Catalog) fileHistoryKeys(ctx context.Context, query string) ([]string, error) {
	clean := filepath.ToSlash(query)
	candidates := []any{clean}
	if folder := folderKey(clean); folder != "" {
		candidates = append(candidates, folder)
	}
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT DISTINCT file_key FROM session_files
		WHERE file_key=? OR path IN (SELECT value FROM json_each(?)) OR substr(path,-length(?)-1)='/'||?
		ORDER BY file_key LIMIT 50`, "path:"+query, jsonText(candidates), clean, clean)
	if err != nil {
		return nil, err
	}
	keys := []string{}
	for _, row := range rows {
		keys = append(keys, firstString(row["file_key"]))
	}
	if filepath.IsAbs(query) {
		// An absolute path is the repository file the edits at it changed.
		rows, err := queryMapsContext(ctx, c.reader(), `SELECT DISTINCT sf.file_key FROM tool_calls t
			JOIN session_files sf ON sf.workspace_id=t.workspace_id AND sf.repository_id=t.path_repository_id AND sf.path=t.repo_path
			WHERE t.path_absolute=? AND t.tool_category='edit' LIMIT 50`, query)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if key := firstString(row["file_key"]); !slices.Contains(keys, key) {
				keys = append(keys, key)
			}
		}
	}
	return keys, nil
}

// fileTimeline is one file's sessions and commits.
func (c *Catalog) fileTimeline(ctx context.Context, key string, limit int) (map[string]any, error) {
	sessions, err := queryMapsContext(ctx, c.reader(), `SELECT sf.workspace_id,sf.path,sf.repository_id,sf.first_edit_at,sf.last_edit_at,sf.edits,sf.lines_added,sf.lines_removed,sf.rewrote,
		w.title,w.source_kind,`+projectNameSQL("w")+` project,`+projectKindSQL("w")+` project_kind,
		`+outcomeLabelSQL("w")+` outcome_label,`+outcomeWhySQL("w")+` outcome_why,
		(SELECT t.conversation_id||char(31)||COALESCE(t.call_message_id,'')||char(31)||COALESCE(t.call_id,'') FROM tool_calls t
			WHERE t.workspace_id=sf.workspace_id AND t.tool_category='edit'
			AND (sf.repository_id IS NOT NULL AND t.path_repository_id=sf.repository_id AND t.repo_path=sf.path
				OR sf.repository_id IS NULL AND 'path:'||t.path_absolute=sf.file_key)
			ORDER BY COALESCE(t.started_at,''),t.sequence LIMIT 1) edit
		FROM session_files sf JOIN workspaces w ON w.id=sf.workspace_id`+projectJoin("w")+`
		WHERE sf.file_key=? AND sf.workspace_id NOT IN (SELECT workspace_id FROM tool_mirror_workspaces)
		ORDER BY COALESCE(sf.last_edit_at,'') DESC,sf.workspace_id LIMIT ?`, key, limit+1)
	if err != nil {
		return nil, err
	}
	file := map[string]any{"file_key": key}
	if repository, path, ok := strings.Cut(strings.TrimPrefix(key, "repository:"), ":"); ok && strings.HasPrefix(key, "repository:") {
		file["repository_id"], file["path"] = repository, path
	} else {
		file["path"] = folderKey(strings.TrimPrefix(key, "path:"))
	}
	file["sessions_truncated"] = len(sessions) > limit
	if len(sessions) > limit {
		sessions = sessions[:limit]
	}
	items := make([]map[string]any, 0, len(sessions))
	for index, row := range sessions {
		if index == 0 {
			file["project"], file["project_kind"] = row["project"], row["project_kind"]
		}
		item := map[string]any{
			"workspace_id": row["workspace_id"], "title": row["title"], "source": row["source_kind"],
			"first_edit_at": row["first_edit_at"], "last_edit_at": row["last_edit_at"], "edits": row["edits"],
			"lines_added": row["lines_added"], "lines_removed": row["lines_removed"], "rewrote": integer(row["rewrote"]) != 0,
			"outcome_label": row["outcome_label"], "outcome_why": row["outcome_why"],
			"url": "/work/" + url.PathEscape(firstString(row["workspace_id"])),
		}
		if parts := strings.Split(firstString(row["edit"]), "\x1f"); len(parts) == 3 && parts[0] != "" {
			item["conversation_id"] = parts[0]
			params := url.Values{"conversation": {parts[0]}}
			if parts[1] != "" {
				item["message_id"] = parts[1]
				params.Set("message", parts[1])
				if parts[2] != "" {
					params.Set("call", parts[2])
				}
			}
			item["url"] = firstString(item["url"]) + "?" + params.Encode()
		}
		items = append(items, item)
	}
	file["sessions"] = items
	commits, git, err := c.fileCommits(ctx, file, limit)
	if err != nil {
		return nil, err
	}
	file["git"], file["commits"] = git, commits
	return file, nil
}

// fileCommits are the commits that changed a repository file, each with the
// sessions linked to it, and the state of local Git for it: checked, not
// checked with the reason, or not applicable outside a repository.
func (c *Catalog) fileCommits(ctx context.Context, file map[string]any, limit int) ([]map[string]any, map[string]any, error) {
	repository := firstString(file["repository_id"])
	if repository == "" {
		return []map[string]any{}, map[string]any{"status": "not_applicable", "reason": "This file is in a folder, not a Git repository; its history comes from transcripts."}, nil
	}
	scans, err := queryMapsContext(ctx, c.reader(), `SELECT status,reason,scanned_at FROM git_repository_scans WHERE repository_id=? AND host_id=?`, repository, currentHost().ID)
	if err != nil {
		return nil, nil, err
	}
	if len(scans) == 0 {
		return []map[string]any{}, map[string]any{"status": "not_checked", "reason": "Local Git has not checked this repository on this Mac yet."}, nil
	}
	git := map[string]any{"status": scans[0]["status"], "reason": scans[0]["reason"], "checked_at": scans[0]["scanned_at"]}
	if scans[0]["status"] != "checked" {
		git["reason"] = defaultString(scans[0]["reason"], "Local Git could not check this repository.")
	}
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT f.hash,f.lines_added,f.lines_removed,g.author_at,g.on_mainline,g.reverted_by,
		(SELECT json_group_array(json_object('workspace_id',l.workspace_id,'method',l.method,'confidence',l.confidence))
			FROM session_commit_links l WHERE l.repository_id=f.repository_id AND l.hash=f.hash) links
		FROM git_commit_files f JOIN git_commits g ON g.repository_id=f.repository_id AND g.hash=f.hash
		WHERE f.repository_id=? AND f.path=? ORDER BY g.author_at DESC,f.hash LIMIT ?`, repository, file["path"], limit+1)
	if err != nil {
		return nil, nil, err
	}
	git["commits_truncated"] = len(rows) > limit
	if len(rows) > limit {
		rows = rows[:limit]
	}
	commits := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		links := []map[string]any{}
		_ = json.Unmarshal([]byte(firstString(row["links"])), &links)
		for _, link := range links {
			// Below outcomeGitCounted a link is a candidate, not counted.
			link["counted"] = floatOr(link["confidence"]) >= outcomeGitCounted
		}
		var mainline any
		if row["on_mainline"] != nil {
			mainline = integer(row["on_mainline"]) != 0
		}
		commits = append(commits, map[string]any{"hash": row["hash"], "author_at": row["author_at"], "lines_added": row["lines_added"], "lines_removed": row["lines_removed"],
			"on_mainline": mainline, "reverted_by": row["reverted_by"], "sessions": links})
	}
	return commits, git, nil
}
