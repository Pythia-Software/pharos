package archive

import (
	"context"
	"fmt"
	"net/url"
	pathpkg "path"
	"path/filepath"
	"strings"
)

// File history ("why is this file like this?", docs/projects-and-outcomes.md):
// every session that edited a path, from its own tool calls (session_files),
// with what it did, its outcome and a link into the reader at its first edit
// of the file. A file in a repository local Git checked also lists the
// commits that changed it (git_commit_files), each with the sessions linked
// to it. Requests read only stored facts; Git is never run for them.

// fileHistoryLimit bounds the sessions and commits listed per file, and
// fileHistoryPlaces the edits listed per session.
var fileHistoryLimit = 200
var fileHistoryPlaces = 50

type FileHistoryOptions struct {
	// Path is a repository-relative path, a folder path (~/… or absolute),
	// or an absolute path inside a checkout this Mac recorded.
	Path string
	// Project narrows to one project: a file_project ref (a name, remote,
	// folder or repository:<id>). Naming exactly one project, Path is
	// that project's file, found though no session touched it.
	Project string
	Limit   int
	// Places bounds the edits listed per session; 0 is fileHistoryPlaces.
	Places int
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
	places := options.Places
	if places <= 0 || places > fileHistoryPlaces {
		places = fileHistoryPlaces
	}
	projects, err := c.fileProjects(ctx)
	if err != nil {
		return nil, err
	}
	// With one project, a repository-relative path is that repository's
	// file even when no session touched it (projectFileKey); else the path
	// is matched (findFileHistoryKeys).
	var found fileHistoryKeys
	if options.Project != "" {
		resolved, err := projects.resolve(ctx, options.Project)
		if err != nil {
			return nil, err
		}
		if len(resolved) == 1 {
			key, err := c.projectFileKey(ctx, resolved[0], query)
			if err != nil {
				return nil, err
			}
			if key != "" {
				found.Keys = []string{key}
			}
		}
	}
	if found.Keys == nil {
		if found, err = c.findFileHistoryKeys(ctx, query, options.Project); err != nil {
			return nil, err
		}
	}
	files := []map[string]any{}
	for _, key := range found.Keys {
		file, err := c.fileTimeline(ctx, projects, key, limit, places)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return map[string]any{"path": query, "files": files, "files_truncated": found.Truncated, "note": fileHistoryNote(files, found, options.Project)}, nil
}

// projectFileKey is the key of a project's file at path, "" when nothing
// in the library knows it: a repository file in the tree local Git read, or
// one a session edited or a commit changed; a folder file a session edited.
func (c *Catalog) projectFileKey(ctx context.Context, project fileProject, path string) (string, error) {
	path = filepath.ToSlash(path)
	if project.Kind == "repository" {
		relative := strings.Trim(pathpkg.Clean("/"+path), "/")
		if filepath.IsAbs(path) || relative == "" {
			return "", nil
		}
		key := "repository:" + project.RepositoryID + ":" + relative
		var found int
		err := c.reader().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM session_files WHERE file_key=?)
			OR EXISTS(SELECT 1 FROM git_tree_files WHERE repository_id=? AND path=?)
			OR EXISTS(SELECT 1 FROM git_commit_files WHERE repository_id=? AND path=?)`, key, project.RepositoryID, relative, project.RepositoryID, relative).Scan(&found)
		if err != nil || found == 0 {
			return "", err
		}
		return key, nil
	}
	recorded := folderPath(project.Folder, path)
	if key := folderKey(recorded); key != "" {
		recorded = key
	}
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT file_key FROM session_files WHERE path=? AND repository_id IS NULL ORDER BY last_edit_at DESC LIMIT 1`, recorded)
	if err != nil || len(rows) == 0 {
		return "", err
	}
	return firstString(rows[0]["file_key"]), nil
}

// fileHistoryNote says what the timeline covers, for a person or an agent
// reading an empty, cut or Git-less answer.
func fileHistoryNote(files []map[string]any, found fileHistoryKeys, project string) string {
	if found.Elsewhere {
		return fmt.Sprintf("No workspace in %s edited a file at this path, but workspaces in other projects did. Clear the project to see them.", project)
	}
	if len(files) == 0 {
		return "No workspace in this library edited or committed a file at this path. File history lists files that agents edited through their tool calls or changed in their linked commits; try a repository-relative path, a folder path, or Library file search."
	}
	note := "Workspaces are those whose agents edited the file through their tool calls, newest first. Commits are listed where local Git checked the repository, each with the workspaces linked to it, which may have changed the file another way. They start at git.commits_since: Git is read from the repository's first recorded edit, so earlier commits are not listed."
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

// findFileHistoryKeys finds the file keys a query names, of the files
// sessions edited (session_files) and of those a session's counted commit
// changed, though no edit call named them. An exact
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
		// The files sessions edited, and those a session's counted commit
		// changed though no edit call named them.
		statement := `SELECT DISTINCT sf.file_key FROM (SELECT file_key,path,workspace_id FROM session_files
			UNION ALL SELECT 'repository:'||f.repository_id||':'||f.path,f.path,l.workspace_id FROM git_commit_files f
				JOIN session_commit_links l ON l.repository_id=f.repository_id AND l.hash=f.hash AND l.confidence>=?) sf`
		args = append([]any{outcomeGitCounted}, args...)
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

// repositoryFileKey reads a repository file's key (outcomeFileKey).
func repositoryFileKey(key string) (repository, path string, ok bool) {
	if rest, found := strings.CutPrefix(key, "repository:"); found {
		return strings.Cut(rest, ":")
	}
	return "", "", false
}

// fileTimeline is one file's sessions and commits. Each session lists its
// edits of the file, each a place in its transcript, and the rework between
// it and other sessions: whose lines it removed, and who removed its own.
func (c *Catalog) fileTimeline(ctx context.Context, projects *fileProjects, key string, limit, places int) (map[string]any, error) {
	sessions, err := queryMapsContext(ctx, c.reader(), `SELECT sf.workspace_id,sf.first_edit_at,sf.last_edit_at,sf.edits,sf.lines_added,sf.lines_removed,sf.rewrote,
		w.title,w.source_kind,w.project_folder,w.repository_id IS NULL AND w.folder_repository_id IS NULL in_folder,
		`+outcomeLabelSQL("w")+` outcome_label,`+outcomeWhySQL("w")+` outcome_why
		FROM session_files sf JOIN workspaces w ON w.id=sf.workspace_id
		WHERE sf.file_key=? AND sf.workspace_id NOT IN (SELECT workspace_id FROM tool_mirror_workspaces)
		ORDER BY COALESCE(sf.last_edit_at,'') DESC,sf.workspace_id LIMIT ?`, key, limit+1)
	if err != nil {
		return nil, err
	}
	file := map[string]any{"file_key": key}
	var project *fileProject
	if repository, path, ok := repositoryFileKey(key); ok {
		file["repository_id"], file["path"] = repository, path
		found, err := projects.repository(ctx, repository)
		if err != nil {
			return nil, err
		}
		project = &found
		var inTree bool
		if err := c.reader().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM git_tree_files WHERE repository_id=? AND path=?)`, repository, path).Scan(&inTree); err != nil {
			return nil, err
		}
		file["in_tree"] = inTree
	} else {
		file["path"] = folderKey(strings.TrimPrefix(key, "path:"))
		// A folder file belongs to the folder project of its latest session,
		// when that session is in a folder rather than a repository.
		if len(sessions) > 0 && integer(sessions[0]["in_folder"]) != 0 && firstString(sessions[0]["project_folder"]) != "" {
			found := folderProject(firstString(sessions[0]["project_folder"]))
			project = &found
		}
	}
	if project != nil {
		relative := firstString(file["path"])
		if project.Kind == "folder" {
			relative = folderRelative(project.Folder, relative)
		}
		file["project"], file["project_kind"], file["project_ref"], file["relative_path"] = project.Name, project.Kind, project.Ref, relative
		file["url"] = filePageURL(project.Ref, relative, "", "")
	} else {
		file["url"] = "/files?" + url.Values{"file": {firstString(file["path"])}}.Encode()
	}
	file["sessions_truncated"] = len(sessions) > limit
	if len(sessions) > limit {
		sessions = sessions[:limit]
	}
	items := make([]map[string]any, 0, len(sessions))
	for _, row := range sessions {
		workspace := firstString(row["workspace_id"])
		item := map[string]any{
			"workspace_id": workspace, "title": row["title"], "source": row["source_kind"],
			"first_edit_at": row["first_edit_at"], "last_edit_at": row["last_edit_at"], "edits": row["edits"],
			"lines_added": row["lines_added"], "lines_removed": row["lines_removed"], "rewrote": integer(row["rewrote"]) != 0,
			"outcome_label": row["outcome_label"], "outcome_why": row["outcome_why"], "url": placeURL(workspace, transcriptPlace{}),
		}
		edits, truncated, err := c.sessionFileEdits(ctx, workspace, key, places)
		if err != nil {
			return nil, err
		}
		item["places"], item["places_truncated"] = edits, truncated
		if len(edits) > 0 {
			item["url"] = edits[0]["url"]
		}
		if item["reworked_by"], item["reworked"], err = c.sessionFileRework(ctx, workspace, key); err != nil {
			return nil, err
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

// sessionFileEdits are a session's edits of a file, in order, each a place
// in its transcript.
func (c *Catalog) sessionFileEdits(ctx context.Context, workspace, key string, limit int) ([]map[string]any, bool, error) {
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT conversation_id,message_id,call_id,edited_at,lines_added,lines_removed,path FROM session_file_edits
		WHERE workspace_id=? AND file_key=? ORDER BY seq LIMIT ?`, workspace, key, limit+1)
	if err != nil {
		return nil, false, err
	}
	truncated := len(rows) > limit
	if truncated {
		rows = rows[:limit]
	}
	edits := make([]map[string]any, len(rows))
	for index, row := range rows {
		place := transcriptPlace{Conversation: firstString(row["conversation_id"]), Message: firstString(row["message_id"]), Call: firstString(row["call_id"])}
		edits[index] = map[string]any{"at": row["edited_at"], "path": row["path"], "lines_added": row["lines_added"], "lines_removed": row["lines_removed"],
			"conversation_id": nilIfEmpty(place.Conversation), "message_id": nilIfEmpty(place.Message), "call_id": nilIfEmpty(place.Call), "url": placeURL(workspace, place)}
	}
	return edits, truncated, nil
}

// sessionFileRework is the rework judged between a session and others on a
// file (session_file_rework): the later sessions that removed lines it
// added, and the earlier sessions whose added lines it removed. Each opens
// at the other session's first edit of the file.
func (c *Catalog) sessionFileRework(ctx context.Context, workspace, key string) ([]map[string]any, []map[string]any, error) {
	read := func(statement string) ([]map[string]any, error) {
		rows, err := queryMapsContext(ctx, c.reader(), `SELECT r.other workspace_id,r.removed_lines,w.title,
			(SELECT e.conversation_id||char(31)||COALESCE(e.message_id,'')||char(31)||COALESCE(e.call_id,'') FROM session_file_edits e
				WHERE e.workspace_id=r.other AND e.file_key=r.file_key ORDER BY e.seq LIMIT 1) place
			FROM (`+statement+`) r LEFT JOIN workspaces w ON w.id=r.other ORDER BY r.removed_lines DESC,r.other`, workspace, key)
		if err != nil {
			return nil, err
		}
		found := make([]map[string]any, len(rows))
		for index, row := range rows {
			other := firstString(row["workspace_id"])
			place := transcriptPlace{}
			if parts := strings.Split(firstString(row["place"]), "\x1f"); len(parts) == 3 {
				place = transcriptPlace{Conversation: parts[0], Message: parts[1], Call: parts[2]}
			}
			found[index] = map[string]any{"workspace_id": other, "title": row["title"], "removed_lines": row["removed_lines"], "url": placeURL(other, place)}
		}
		return found, nil
	}
	by, err := read(`SELECT later_workspace_id other,file_key,removed_lines FROM session_file_rework WHERE workspace_id=? AND file_key=?`)
	if err != nil {
		return nil, nil, err
	}
	of, err := read(`SELECT workspace_id other,file_key,removed_lines FROM session_file_rework WHERE later_workspace_id=? AND file_key=?`)
	return by, of, err
}

// fileCommits are the commits that changed a repository file, each with the
// sessions linked to it and where in their transcripts, and the state of
// local Git for it: checked, not checked with the reason, or not applicable
// outside a repository.
func (c *Catalog) fileCommits(ctx context.Context, file map[string]any, limit int) ([]map[string]any, map[string]any, error) {
	repository := firstString(file["repository_id"])
	if repository == "" {
		return []map[string]any{}, map[string]any{"status": "not_applicable", "reason": "This file is in a folder, not a Git repository; its history comes from transcripts."}, nil
	}
	// The scan reads history from its sessions' first edit, so commits
	// cover only the time since (commits_since), not the file's whole life.
	git, err := c.fileGitState(ctx, repository)
	if err != nil || git["status"] == "not_checked" && git["checked_at"] == nil {
		return []map[string]any{}, git, err
	}
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT f.hash,f.lines_added,f.lines_removed,g.author_at,g.on_mainline,g.reverted_by
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
		hash := firstString(row["hash"])
		links, err := c.commitSessions(ctx, repository, hash)
		if err != nil {
			return nil, nil, err
		}
		commit := map[string]any{"hash": hash, "author_at": row["author_at"], "lines_added": row["lines_added"], "lines_removed": row["lines_removed"],
			"on_mainline": nullableBool(row["on_mainline"]), "reverted_by": row["reverted_by"], "sessions": links}
		if ref := firstString(file["project_ref"]); ref != "" {
			commit["url"] = filePageURL(ref, "", "", hash)
		}
		commits = append(commits, commit)
	}
	return commits, git, nil
}
