package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
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

// fileHistoryFiles bounds the files one path names.
var fileHistoryFiles = 50

// fileHistoryKeys are what a query names, in the project when one is given.
type fileHistoryKeys struct {
	Keys []string
	// Truncated is set when more files matched than fileHistoryFiles.
	Truncated bool
	// Elsewhere is set when the project hid every file the path names.
	Elsewhere bool
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
	found, err := c.findFileHistoryKeys(ctx, query, options.Project)
	if err != nil {
		return nil, err
	}
	files := []map[string]any{}
	for _, key := range found.Keys {
		file, err := c.fileTimeline(ctx, key, limit)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return map[string]any{"path": query, "files": files, "files_truncated": found.Truncated, "note": fileHistoryNote(files, found, options.Project)}, nil
}

// fileHistoryNote says what the timeline covers, for a person or an agent
// reading an empty, cut or Git-less answer.
func fileHistoryNote(files []map[string]any, found fileHistoryKeys, project string) string {
	if found.Elsewhere {
		return fmt.Sprintf("No workspace in %s edited a file at this path, but workspaces in other projects did. Clear the project to see them.", project)
	}
	if len(files) == 0 {
		return "No workspace in this library edited a file at this path. File history lists files that agents edited through their tool calls; try a repository-relative path, a folder path, or Library file search."
	}
	note := "Workspaces are those whose agents edited the file through their tool calls, newest first. Commits are listed where local Git checked the repository, from git.commits_since: Git is read from the repository's first recorded edit, so earlier commits are not listed."
	if found.Truncated {
		note = fmt.Sprintf("More than %d files match this path; the first %d by path are shown. Give a longer path or a project to narrow it. ", fileHistoryFiles, fileHistoryFiles) + note
	}
	return note
}

// fileFilterSQL matches workspace w when it changed a file whose path
// contains the pattern's text: a change set's file (change_files), or a file
// a session edited through its tool calls (session_files), which every
// harness with file tools records. A session's file matches by its path in
// its repository, or a folder file by its ~ path or absolute path; never by
// the rest of its key, whose repository: and path: prefixes and repository
// ID every file would share. An absolute path also matches a repository file
// at that path in a checkout Pharos knows (fileFilterArgs resolves it). Use
// fileFilterArgs for its arguments.
func fileFilterSQL(w string) string {
	return `(EXISTS(SELECT 1 FROM change_sets cs JOIN change_files cf ON cf.change_set_id=cs.id WHERE cs.workspace_id=` + w + `.id AND cf.path LIKE ?)
		OR EXISTS(SELECT 1 FROM session_files sf WHERE sf.workspace_id=` + w + `.id AND (sf.path LIKE ?
			OR substr(sf.file_key,1,5)='path:' AND substr(sf.file_key,6) LIKE ?
			OR sf.repository_id IS NOT NULL AND EXISTS(SELECT 1 FROM json_each(?) j
				WHERE json_extract(j.value,'$.repository')=sf.repository_id AND sf.path LIKE json_extract(j.value,'$.path')))))`
}

// fileFilterRoots are the checkouts fileFilterArgs resolves a file filter
// against: every repository's, for an absolute (or ~) path, else none.
func fileFilterRoots(ctx context.Context, q queryer, file string) ([]projectRoot, error) {
	if folderKey(file) == "" || strings.Contains(file, "%") {
		return nil, nil
	}
	return projectRoots(ctx, q)
}

// fileFilterArgs are fileFilterSQL's arguments for a file filter: text a
// path contains, or a LIKE pattern when it has %. An absolute path inside a
// checkout in roots (fileFilterRoots) is also the repository's files at or
// below its path in the repository, as session_files records them.
func fileFilterArgs(file string, roots []projectRoot) []any {
	pattern := file
	if !strings.Contains(pattern, "%") {
		pattern = "%" + pattern + "%"
	}
	checkouts := []map[string]string{}
	if key := folderKey(file); key != "" && !strings.Contains(file, "%") {
		for _, root := range roots {
			if key == root.folder {
				checkouts = append(checkouts, map[string]string{"repository": root.repository, "path": "%"})
			} else if rest, ok := strings.CutPrefix(key, strings.TrimSuffix(root.folder, "/")+"/"); ok {
				checkouts = append(checkouts, map[string]string{"repository": root.repository, "path": rest + "%"})
			}
		}
	}
	return []any{pattern, pattern, pattern, jsonText(checkouts)}
}

// findFileHistoryKeys finds the session_files keys a query names. An exact
// match wins: the path itself, a folder path (~/… or absolute), or an
// absolute path in a checkout this Mac recorded, which maps to its
// repository file. Only when nothing matches exactly does a path ending
// match, so src/index.ts names that file before every packages/*/src/index.ts.
// The project narrows the match before it is bounded.
func (c *Catalog) findFileHistoryKeys(ctx context.Context, query, project string) (fileHistoryKeys, error) {
	clean := filepath.ToSlash(query)
	absolute := ""
	if expanded := expandHome(query); filepath.IsAbs(expanded) {
		absolute = filepath.ToSlash(filepath.Clean(expanded))
	}
	keys := []any{"path:" + query}
	paths := []any{clean}
	if absolute != "" {
		keys = append(keys, "path:"+absolute)
		repositoryKeys, err := c.checkoutFileKeys(ctx, absolute)
		if err != nil {
			return fileHistoryKeys{}, err
		}
		keys = append(keys, repositoryKeys...)
	}
	if folder := folderKey(clean); folder != "" {
		paths = append(paths, folder)
	}
	exact := `(sf.file_key IN (SELECT value FROM json_each(?)) OR sf.path IN (SELECT value FROM json_each(?)))`
	ending := `substr(sf.path,-length(?)-1)='/'||?`
	match := func(where string, project string, args ...any) (fileHistoryKeys, error) {
		statement := `SELECT DISTINCT sf.file_key FROM session_files sf`
		if project != "" {
			statement += ` JOIN workspaces w ON w.id=sf.workspace_id`
			where += ` AND ` + projectPredicate("w")
			args = append(args, projectArgs(project)...)
		}
		rows, err := queryMapsContext(ctx, c.reader(), statement+` WHERE `+where+` AND sf.workspace_id NOT IN (SELECT workspace_id FROM tool_mirror_workspaces)
			ORDER BY sf.file_key LIMIT ?`, append(args, fileHistoryFiles+1)...)
		if err != nil {
			return fileHistoryKeys{}, err
		}
		found := fileHistoryKeys{Keys: []string{}, Truncated: len(rows) > fileHistoryFiles}
		for _, row := range rows[:min(len(rows), fileHistoryFiles)] {
			found.Keys = append(found.Keys, firstString(row["file_key"]))
		}
		return found, nil
	}
	find := func(project string) (fileHistoryKeys, error) {
		found, err := match(exact, project, jsonText(keys), jsonText(paths))
		if err != nil || len(found.Keys) > 0 || absolute != "" || strings.HasPrefix(clean, "~") {
			return found, err
		}
		return match(ending, project, clean, clean)
	}
	found, err := find(project)
	if err != nil || len(found.Keys) > 0 || project == "" {
		return found, err
	}
	// Nothing in the project: say whether the path is edited elsewhere, so
	// an empty answer is not read as a file nobody edited.
	anywhere, err := find("")
	found.Elsewhere = len(anywhere.Keys) > 0
	return found, err
}

// checkoutFileKeys maps an absolute path to the repository file it is in a
// checkout this Mac recorded: a repository's known location, or the location
// of a workspace in it, as tool call paths are resolved (resolveAbsolutePath).
// The checkout need not be one a session edited in.
func (c *Catalog) checkoutFileKeys(ctx context.Context, absolute string) ([]any, error) {
	roots, err := toolRepositoryLocations(c.reader())
	if err != nil {
		return nil, err
	}
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT DISTINCT l.location,r.display_name,r.id FROM (
			SELECT id workspace_id,location FROM workspaces WHERE COALESCE(location,'')<>''
			UNION SELECT workspace_id,location FROM workspace_sightings WHERE host_id=? AND COALESCE(location,'')<>'') l
		JOIN workspaces w ON w.id=l.workspace_id JOIN repositories r ON r.id=w.repository_id`, currentHost().ID)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		roots = append(roots, repoRoot{firstString(row["location"]), firstString(row["display_name"]), firstString(row["id"])})
	}
	rel, repository, scope := resolveAbsolutePath(absolute, roots)
	if scope != "repo" || repository.ID == "" || rel == "" || rel == "." {
		return nil, nil
	}
	return []any{"repository:" + repository.ID + ":" + rel}, nil
}

// firstEditCallSQL selects fields of the call where a session first edited
// the session_files row sf: its first successful edit call that recorded the
// file as its own or, for a patch's other files, which only session_files
// records, its edit call at the file's first edit. session_files counts only
// successful edits, so a failed call is never the first edit.
func firstEditCallSQL(sf, fields string) string {
	return `COALESCE((SELECT ` + fields + ` FROM tool_calls t
			WHERE t.workspace_id=` + sf + `.workspace_id AND t.tool_category='edit' AND t.status='ok'
			AND (` + sf + `.repository_id IS NOT NULL AND t.path_repository_id=` + sf + `.repository_id AND t.repo_path=` + sf + `.path
				OR ` + sf + `.repository_id IS NULL AND 'path:'||t.path_absolute=` + sf + `.file_key)
			ORDER BY COALESCE(t.started_at,''),t.sequence LIMIT 1),
		(SELECT ` + fields + ` FROM tool_calls t
			WHERE t.workspace_id=` + sf + `.workspace_id AND t.tool_category='edit' AND t.status='ok' AND t.started_at=` + sf + `.first_edit_at
			ORDER BY t.sequence LIMIT 1))`
}

// fileTimeline is one file's sessions and commits.
func (c *Catalog) fileTimeline(ctx context.Context, key string, limit int) (map[string]any, error) {
	sessions, err := queryMapsContext(ctx, c.reader(), `SELECT sf.workspace_id,sf.path,sf.repository_id,sf.first_edit_at,sf.last_edit_at,sf.edits,sf.lines_added,sf.lines_removed,sf.rewrote,
		w.title,w.source_kind,`+projectNameSQL("w")+` project,`+projectKindSQL("w")+` project_kind,
		`+outcomeLabelSQL("w")+` outcome_label,`+outcomeWhySQL("w")+` outcome_why,
		`+firstEditCallSQL("sf", "t.conversation_id||char(31)||COALESCE(t.call_message_id,'')||char(31)||COALESCE(t.call_id,'')")+` edit
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
	scans, err := queryMapsContext(ctx, c.reader(), `SELECT status,reason,scanned_at,history_since FROM git_repository_scans WHERE repository_id=? AND host_id=?`, repository, currentHost().ID)
	if err != nil {
		return nil, nil, err
	}
	if len(scans) == 0 {
		return []map[string]any{}, map[string]any{"status": "not_checked", "reason": "Local Git has not checked this repository on this Mac yet."}, nil
	}
	// The scan reads history from its sessions' first edit, so commits
	// cover only the time since (commits_since), not the file's whole life.
	git := map[string]any{"status": scans[0]["status"], "reason": scans[0]["reason"], "checked_at": scans[0]["scanned_at"], "commits_since": scans[0]["history_since"]}
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
