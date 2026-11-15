package archive

import (
	"context"
	"encoding/json"
	"net/url"
)

// A session's files and commits (docs/projects-and-outcomes.md, "Files,
// sessions and commits, linked both ways"): from a session down to each
// file it edited, every edit's place in its transcript, the file's page, the
// sessions it reworked or that reworked it, and the commits linked to it.
// Requests read only stored facts; Git is never run for them.

// workFilesLimit bounds the files listed for a session, and workFilePlaces
// each file's edits.
var workFilesLimit = 300
var workFilePlaces = 20

// WorkFiles is a session's outcome, the files it edited and its commits.
func (c *Catalog) WorkFiles(ctx context.Context, workspace string) (map[string]any, error) {
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT w.id,w.title,w.project_folder,w.repository_id IS NULL AND w.folder_repository_id IS NULL in_folder,
		`+outcomeLabelSQL("w")+` outcome_label,`+outcomeWhySQL("w")+` outcome_why,`+gitCheckSQL("w")+` git_status,`+gitCheckReasonSQL("w")+` git_reason
		FROM workspaces w`+projectJoin("w")+` WHERE w.id=?`, workspace)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	work := rows[0]
	projects, err := c.fileProjects(ctx)
	if err != nil {
		return nil, err
	}
	var folder *fileProject
	if integer(work["in_folder"]) != 0 && firstString(work["project_folder"]) != "" {
		found := folderProject(firstString(work["project_folder"]))
		folder = &found
	}
	result := map[string]any{"workspace_id": workspace, "outcome_label": work["outcome_label"], "outcome_why": work["outcome_why"],
		"git": map[string]any{"status": work["git_status"], "reason": work["git_reason"]}}
	files, err := queryMapsContext(ctx, c.reader(), `SELECT sf.file_key,sf.path,sf.repository_id,sf.first_edit_at,sf.last_edit_at,sf.edits,sf.lines_added,sf.lines_removed,sf.rewrote,
		(SELECT COUNT(DISTINCT o.workspace_id) FROM session_files o WHERE o.file_key=sf.file_key AND o.workspace_id<>sf.workspace_id
			AND o.workspace_id NOT IN (SELECT workspace_id FROM tool_mirror_workspaces)) other_sessions
		FROM session_files sf WHERE sf.workspace_id=? ORDER BY sf.first_edit_at,sf.path LIMIT ?`, workspace, workFilesLimit+1)
	if err != nil {
		return nil, err
	}
	result["files_truncated"] = len(files) > workFilesLimit
	if len(files) > workFilesLimit {
		files = files[:workFilesLimit]
	}
	items := make([]map[string]any, 0, len(files))
	for _, row := range files {
		key := firstString(row["file_key"])
		item := map[string]any{"file_key": key, "path": row["path"], "first_edit_at": row["first_edit_at"], "last_edit_at": row["last_edit_at"], "edits": row["edits"],
			"lines_added": row["lines_added"], "lines_removed": row["lines_removed"], "rewrote": integer(row["rewrote"]) != 0, "other_sessions": row["other_sessions"]}
		link, err := projects.fileLink(ctx, key, firstString(row["path"]), folder)
		if err != nil {
			return nil, err
		}
		for name, value := range link {
			item[name] = value
		}
		if item["places"], item["places_truncated"], err = c.sessionFileEdits(ctx, workspace, key, workFilePlaces); err != nil {
			return nil, err
		}
		if item["reworked_by"], item["reworked"], err = c.sessionFileRework(ctx, workspace, key); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	result["files"] = items
	commits, err := queryMapsContext(ctx, c.reader(), `SELECT l.repository_id,l.hash,l.method,l.confidence,l.evidence_json,
		g.author_at,g.files,g.lines_added,g.lines_removed,g.on_mainline,g.reverted_by
		FROM session_commit_links l LEFT JOIN git_commits g ON g.repository_id=l.repository_id AND g.hash=l.hash
		WHERE l.workspace_id=? ORDER BY l.confidence<? ,g.author_at,l.hash`, workspace, outcomeGitCounted)
	if err != nil {
		return nil, err
	}
	linked := make([]map[string]any, 0, len(commits))
	for _, row := range commits {
		evidence := map[string]any{}
		_ = json.Unmarshal([]byte(firstString(row["evidence_json"])), &evidence)
		repository, hash := firstString(row["repository_id"]), firstString(row["hash"])
		project, err := projects.repository(ctx, repository)
		if err != nil {
			return nil, err
		}
		commit := map[string]any{"hash": hash, "repository_id": repository, "project": project.Name, "project_ref": project.Ref,
			"method": row["method"], "confidence": row["confidence"], "counted": floatOr(row["confidence"]) >= outcomeGitCounted, "evidence": evidence,
			"author_at": row["author_at"], "files": row["files"], "lines_added": row["lines_added"], "lines_removed": row["lines_removed"],
			"on_mainline": nullableBool(row["on_mainline"]), "reverted_by": row["reverted_by"], "url": filePageURL(project.Ref, "", "", hash)}
		if place := evidencePlace(evidence); place.Conversation != "" {
			commit["place_url"] = placeURL(workspace, place)
		}
		linked = append(linked, commit)
	}
	result["commits"] = linked
	return result, nil
}

// fileLink names a file's project and page: a repository file in its
// repository's tree, a folder file under the folder project of the session
// that edited it, and any other file by its own path.
func (projects *fileProjects) fileLink(ctx context.Context, key, path string, folder *fileProject) (map[string]any, error) {
	if repository, relative, ok := repositoryFileKey(key); ok {
		project, err := projects.repository(ctx, repository)
		if err != nil {
			return nil, err
		}
		return map[string]any{"project": project.Name, "project_ref": project.Ref, "relative_path": relative, "url": filePageURL(project.Ref, relative, "", "")}, nil
	}
	if folder != nil {
		relative := folderRelative(folder.Folder, path)
		return map[string]any{"project": folder.Name, "project_ref": folder.Ref, "relative_path": relative, "url": filePageURL(folder.Ref, relative, "", "")}, nil
	}
	return map[string]any{"url": "/files?" + url.Values{"file": {path}}.Encode()}, nil
}

// conversationFileLinks are the files a conversation's calls edited or read,
// each with the page of its history, for the reader's file chips: an edit's
// files (session_file_edits, a patch's every file) and a read's repository
// file (tool_calls).
func (c *Catalog) conversationFileLinks(ctx context.Context, workspace, conversation string) ([]map[string]any, error) {
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT message_id,call_id,path,file_key,'edit' kind FROM session_file_edits
		WHERE conversation_id=? AND workspace_id=? AND message_id IS NOT NULL AND message_id<>''
		UNION ALL SELECT call_message_id,call_id,COALESCE(path_absolute,repo_path),'repository:'||path_repository_id||':'||repo_path,'read' FROM tool_calls
		WHERE conversation_id=? AND tool_category='read' AND path_repository_id IS NOT NULL AND repo_path IS NOT NULL AND repo_path<>'' AND call_message_id IS NOT NULL`,
		conversation, workspace, conversation)
	if err != nil || len(rows) == 0 {
		return []map[string]any{}, err
	}
	projects, err := c.fileProjects(ctx)
	if err != nil {
		return nil, err
	}
	var folder *fileProject
	folders, err := queryMapsContext(ctx, c.reader(), `SELECT project_folder FROM workspaces WHERE id=? AND repository_id IS NULL AND folder_repository_id IS NULL AND project_folder IS NOT NULL`, workspace)
	if err != nil {
		return nil, err
	}
	if len(folders) > 0 {
		found := folderProject(firstString(folders[0]["project_folder"]))
		folder = &found
	}
	links := make([]map[string]any, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		key := firstString(row["file_key"])
		id := firstString(row["message_id"]) + "\x1f" + firstString(row["call_id"]) + "\x1f" + key
		if seen[id] {
			continue
		}
		seen[id] = true
		path := firstString(row["path"])
		recorded := path
		if _, _, ok := repositoryFileKey(key); !ok {
			recorded = defaultString(folderKey(path), path)
		}
		link, err := projects.fileLink(ctx, key, recorded, folder)
		if err != nil {
			return nil, err
		}
		links = append(links, map[string]any{"message_id": row["message_id"], "call_id": nilIfEmpty(firstString(row["call_id"])), "path": path,
			"kind": row["kind"], "file": defaultString(link["relative_path"], recorded), "project": link["project"], "url": link["url"]})
	}
	return links, nil
}
