package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strings"
	"unicode/utf8"
)

// The files page (docs/projects-and-outcomes.md, "Files, sessions and
// commits, linked both ways"): a project's files as a tree, a directory's
// sessions and commits, and a commit's files and sessions. A repository local
// Git checked has its whole tree at the mainline tip (git_tree_files) and
// every commit read (git_commit_files); any other project lists the files its
// sessions edited. Requests read only stored facts; Git is never run for them.

// fileTreeActivityLimit bounds a directory's listed sessions and commits.
var fileTreeActivityLimit = 20

// fileProject is a project as the files page addresses it. Ref is the value
// a URL's file_project holds: the name, unless another project shares it.
type fileProject struct {
	Ref, Name, Kind string
	RepositoryID    string
	Folder          string
}

func (project fileProject) value() map[string]any {
	value := map[string]any{"ref": project.Ref, "name": project.Name, "kind": project.Kind}
	if project.RepositoryID != "" {
		value["repository_id"] = project.RepositoryID
	} else {
		value["folder"] = project.Folder
	}
	return value
}

// filePageURL is a files page address: a project's file, directory or
// commit, or the project's root when all three are empty.
func filePageURL(project, file, dir, commit string) string {
	params := url.Values{"file_project": {project}}
	switch {
	case file != "":
		params.Set("file", file)
	case dir != "":
		params.Set("file_dir", dir)
	case commit != "":
		params.Set("file_commit", commit)
	}
	return "/files?" + params.Encode()
}

// placeURL opens a call in its session's transcript.
func placeURL(workspace string, place transcriptPlace) string {
	address := "/work/" + url.PathEscape(workspace)
	if place.Conversation == "" {
		return address
	}
	params := url.Values{"conversation": {place.Conversation}}
	if place.Message != "" {
		params.Set("message", place.Message)
		if place.Call != "" {
			params.Set("call", place.Call)
		}
	}
	return address + "?" + params.Encode()
}

// evidencePlace reads the place a link's evidence names.
func evidencePlace(evidence map[string]any) transcriptPlace {
	return transcriptPlace{Conversation: firstString(evidence["conversation"]), Message: firstString(evidence["message"]), Call: firstString(evidence["call"])}
}

// fileProjects resolves project refs and names projects for URLs, once per
// request: a repository is addressed by its name unless another repository
// or folder shares it, then as repository:<id>.
type fileProjects struct {
	catalog *Catalog
	shared  map[string]bool
	names   map[string]string
}

func (c *Catalog) fileProjects(ctx context.Context) (*fileProjects, error) {
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT lower(display_name) name FROM repositories GROUP BY lower(display_name) HAVING COUNT(*)>1`)
	if err != nil {
		return nil, err
	}
	projects := &fileProjects{catalog: c, shared: map[string]bool{}, names: map[string]string{}}
	for _, row := range rows {
		projects.shared[firstString(row["name"])] = true
	}
	return projects, nil
}

// repository is a repository's project, named for its URLs.
func (projects *fileProjects) repository(ctx context.Context, id string) (fileProject, error) {
	name, ok := projects.names[id]
	if !ok {
		err := projects.catalog.reader().QueryRowContext(ctx, `SELECT display_name FROM repositories WHERE id=?`, id).Scan(&name)
		if err != nil && err != sql.ErrNoRows {
			return fileProject{}, err
		}
		projects.names[id] = name
	}
	ref := name
	if name == "" || projects.shared[strings.ToLower(name)] {
		ref = "repository:" + id
	}
	return fileProject{Ref: ref, Name: defaultString(name, id), Kind: "repository", RepositoryID: id}, nil
}

func folderProject(folder string) fileProject {
	return fileProject{Ref: folder, Name: folder, Kind: "folder", Folder: folder}
}

// resolve finds the projects a file_project names: repository:<id>, a
// repository's name or remote, or a folder project's key or path.
func (projects *fileProjects) resolve(ctx context.Context, value string) ([]fileProject, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		// An empty name would match every repository without a remote.
		return nil, fmt.Errorf("a project is required; list_projects names them")
	}
	if id, ok := strings.CutPrefix(value, "repository:"); ok {
		var found int
		if err := projects.catalog.reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM repositories WHERE id=?`, id).Scan(&found); err != nil || found == 0 {
			return nil, err
		}
		project, err := projects.repository(ctx, id)
		return []fileProject{project}, err
	}
	rows, err := queryMapsContext(ctx, projects.catalog.reader(), `SELECT id FROM repositories WHERE lower(display_name)=lower(?) OR lower(COALESCE(canonical_remote,''))=lower(?)
		ORDER BY id`, value, value)
	if err != nil {
		return nil, err
	}
	found := []fileProject{}
	for _, row := range rows {
		project, err := projects.repository(ctx, firstString(row["id"]))
		if err != nil {
			return nil, err
		}
		found = append(found, project)
	}
	folder := strings.TrimPrefix(value, "folder:")
	if key := projectFolder(folder); key != "" {
		folder = key
	}
	var folders int
	if err := projects.catalog.reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM workspaces WHERE project_folder=? AND repository_id IS NULL AND folder_repository_id IS NULL`, folder).Scan(&folders); err != nil {
		return nil, err
	}
	if folders > 0 {
		found = append(found, folderProject(folder))
	}
	return found, nil
}

// choose resolves one project, or says which were meant.
func (projects *fileProjects) choose(ctx context.Context, value string) (fileProject, []map[string]any, error) {
	found, err := projects.resolve(ctx, value)
	if err != nil {
		return fileProject{}, nil, err
	}
	if len(found) == 0 {
		return fileProject{}, nil, fmt.Errorf("no project %q in this library; list_projects names them", value)
	}
	if len(found) > 1 {
		choices := make([]map[string]any, len(found))
		for index, project := range found {
			choices[index] = project.value()
		}
		return fileProject{}, choices, nil
	}
	return found[0], nil, nil
}

// fileGitState is local Git's state for a repository on this Mac: the scan's
// status, how far back it read and the tree it kept.
func (c *Catalog) fileGitState(ctx context.Context, repository string) (map[string]any, error) {
	scans, err := queryMapsContext(ctx, c.reader(), `SELECT status,reason,scanned_at,history_since,tree_ref,tree_commit FROM git_repository_scans WHERE repository_id=? AND host_id=?`,
		repository, currentHost().ID)
	if err != nil || len(scans) == 0 {
		return map[string]any{"status": "not_checked", "reason": "Local Git has not checked this repository on this Mac yet."}, err
	}
	scan := scans[0]
	git := map[string]any{"status": scan["status"], "reason": scan["reason"], "checked_at": scan["scanned_at"], "commits_since": scan["history_since"],
		"tree_ref": scan["tree_ref"], "tree_commit": scan["tree_commit"]}
	if scan["status"] != "checked" {
		git["reason"] = defaultString(scan["reason"], "Local Git could not check this repository.")
	}
	return git, nil
}

type FileTreeOptions struct {
	// Project is a file_project: a name, remote, folder or repository:<id>.
	Project string
	// Dir is a directory in the project, "" for its root.
	Dir string
	// Activity adds the directory's latest sessions and commits.
	Activity bool
}

// fileTreeEntry is one child of a directory, with what touched it.
type fileTreeEntry struct {
	Name, Path, Kind string
	Sessions         int64
	Commits          int64
	LastAt           string
	InTree           bool
	// LastSession is the session that edited it last, LastSessionAt when;
	// LastCommit is the newest commit that changed it.
	LastSession, LastSessionAt, LastCommit, LastCommitAt string
}

// FileTree lists one directory of a project: its files and directories, each
// with the sessions that edited it and the commits that changed it, and, with
// Activity, the directory's latest sessions and commits.
func (c *Catalog) FileTree(ctx context.Context, options FileTreeOptions) (map[string]any, error) {
	projects, err := c.fileProjects(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(options.Project) == "" {
		return c.fileTreeProjects(ctx, projects)
	}
	project, choices, err := projects.choose(ctx, options.Project)
	if err != nil || choices != nil {
		return map[string]any{"choices": choices}, err
	}
	dir := treeDir(project, options.Dir)
	result := map[string]any{"project": project.value(), "dir": dir}
	var entries map[string]*fileTreeEntry
	if project.Kind == "repository" {
		git, err := c.fileGitState(ctx, project.RepositoryID)
		if err != nil {
			return nil, err
		}
		result["git"] = git
		entries, err = c.repositoryTreeEntries(ctx, project.RepositoryID, dir, git["tree_commit"] != nil)
		if err != nil {
			return nil, err
		}
	} else {
		result["git"] = map[string]any{"status": "not_applicable", "reason": "A folder, not a Git repository: its files are those agents edited in it."}
		entries, err = c.folderTreeEntries(ctx, project.Folder, dir)
		if err != nil {
			return nil, err
		}
	}
	list := make([]map[string]any, 0, len(entries))
	sessions := []string{}
	for _, entry := range slices.SortedFunc(func(yield func(*fileTreeEntry) bool) {
		for _, entry := range entries {
			if !yield(entry) {
				return
			}
		}
	}, func(a, b *fileTreeEntry) int {
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		return strings.Compare(a.Name, b.Name)
	}) {
		value := map[string]any{"name": entry.Name, "path": entry.Path, "kind": entry.Kind, "sessions": entry.Sessions, "commits": entry.Commits,
			"last_at": nilIfEmpty(entry.LastAt), "in_tree": entry.InTree, "last_session": nil, "last_commit": nil}
		if entry.LastSession != "" {
			value["last_session"] = map[string]any{"workspace_id": entry.LastSession, "at": entry.LastSessionAt, "url": "/work/" + url.PathEscape(entry.LastSession)}
			sessions = append(sessions, entry.LastSession)
		}
		if entry.LastCommit != "" {
			value["last_commit"] = map[string]any{"hash": entry.LastCommit, "author_at": entry.LastCommitAt, "url": filePageURL(project.Ref, "", "", entry.LastCommit)}
		}
		list = append(list, value)
	}
	if err := c.addSessionTitles(ctx, list, sessions); err != nil {
		return nil, err
	}
	result["entries"] = list
	if options.Activity {
		sessions, commits, err := c.directoryActivity(ctx, projects, project, dir)
		if err != nil {
			return nil, err
		}
		result["sessions"], result["commits"] = sessions, commits
	}
	return result, nil
}

// treeDir is a directory as a project's tree names it: repository-relative,
// "" for the root. A folder project's directory outside the folder keeps its
// own folder path (/… or ~/…), as folderTreeEntries lists it.
func treeDir(project fileProject, dir string) string {
	dir = strings.TrimSpace(dir)
	if project.Kind != "repository" && strings.HasPrefix(dir, "/") {
		if clean := path.Clean(dir); clean != "/" {
			return clean
		}
		return ""
	}
	return strings.Trim(path.Clean("/"+dir), "/")
}

// fileTreeProjectLimit bounds the projects listed without one chosen.
var fileTreeProjectLimit = 100

// fileTreeProjects lists the projects whose sessions edited files, most
// recently edited first, for choosing one.
func (c *Catalog) fileTreeProjects(ctx context.Context, projects *fileProjects) (map[string]any, error) {
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT COALESCE(w.repository_id,w.folder_repository_id) repository_id,w.project_folder,
		COUNT(DISTINCT sf.workspace_id) sessions,COUNT(DISTINCT sf.file_key) files,MAX(sf.last_edit_at) last_at
		FROM session_files sf JOIN workspaces w ON w.id=sf.workspace_id
		WHERE sf.workspace_id NOT IN (SELECT workspace_id FROM tool_mirror_workspaces) AND (w.repository_id IS NOT NULL OR w.folder_repository_id IS NOT NULL OR w.project_folder IS NOT NULL)
		GROUP BY COALESCE('repository:'||COALESCE(w.repository_id,w.folder_repository_id),'folder:'||w.project_folder) ORDER BY last_at DESC LIMIT ?`, fileTreeProjectLimit)
	if err != nil {
		return nil, err
	}
	list := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		project := folderProject(firstString(row["project_folder"]))
		if repository := firstString(row["repository_id"]); repository != "" {
			if project, err = projects.repository(ctx, repository); err != nil {
				return nil, err
			}
		}
		value := project.value()
		value["sessions"], value["files"], value["last_at"] = row["sessions"], row["files"], row["last_at"]
		list = append(list, value)
	}
	return map[string]any{"projects": list}, nil
}

// prefixRange bounds the paths under a directory, for a range scan.
func prefixRange(prefix string) (string, string) {
	return prefix, prefix + "\xff"
}

// childSQL is the child of the directory a path under it falls in: its
// first segment from the start'th character (sqlStart), and whether that is
// a directory.
const childSQL = `CASE WHEN instr(substr(%[1]s,?),'/')>0 THEN substr(%[1]s,?,instr(substr(%[1]s,?),'/')-1) ELSE substr(%[1]s,?) END child,
	instr(substr(%[1]s,?),'/')>0 is_dir`

func childArgs(start int) []any { return []any{start, start, start, start, start} }

// sqlStart is where SQLite's substr finds what follows prefix: it counts
// characters, not bytes.
func sqlStart(prefix string) int { return utf8.RuneCountInString(prefix) + 1 }

// repositoryTreeEntries are a repository directory's children: the tree's
// paths, the files sessions edited and the files commits changed, which may
// be gone from the tree or never committed.
func (c *Catalog) repositoryTreeEntries(ctx context.Context, repository, dir string, tree bool) (map[string]*fileTreeEntry, error) {
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	start := sqlStart(prefix)
	low, high := prefixRange(prefix)
	entries := map[string]*fileTreeEntry{}
	entry := func(row map[string]any) *fileTreeEntry {
		name := firstString(row["child"])
		if name == "" {
			return nil
		}
		kind := "file"
		if integer(row["is_dir"]) != 0 {
			kind = "dir"
		}
		if entries[name] == nil {
			entries[name] = &fileTreeEntry{Name: name, Path: prefix + name, Kind: kind}
		}
		return entries[name]
	}
	if tree {
		rows, err := queryMapsContext(ctx, c.reader(), `SELECT `+fmt.Sprintf(childSQL, "path")+` FROM git_tree_files
			WHERE repository_id=? AND path>=? AND path<? GROUP BY child`, append(childArgs(start), repository, low, high)...)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if found := entry(row); found != nil {
				found.InTree = true
			}
		}
	}
	keyPrefix := "repository:" + repository + ":"
	keyLow, keyHigh := prefixRange(keyPrefix + prefix)
	// With one MAX, SQLite takes a bare column from the row holding it.
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT `+fmt.Sprintf(childSQL, "p")+`,COUNT(DISTINCT workspace_id) sessions,MAX(COALESCE(last_edit_at,'')) last_at,workspace_id
		FROM (SELECT substr(file_key,?) p,workspace_id,last_edit_at FROM session_files WHERE file_key>=? AND file_key<?
			AND workspace_id NOT IN (SELECT workspace_id FROM tool_mirror_workspaces)) GROUP BY child`,
		append(childArgs(start), sqlStart(keyPrefix), keyLow, keyHigh)...)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if found := entry(row); found != nil {
			found.Sessions, found.LastAt = integer(row["sessions"]), firstString(row["last_at"])
			found.LastSession, found.LastSessionAt = firstString(row["workspace_id"]), found.LastAt
		}
	}
	rows, err = queryMapsContext(ctx, c.reader(), `SELECT `+fmt.Sprintf(childSQL, "f.path")+`,COUNT(DISTINCT f.hash) commits,MAX(COALESCE(g.author_at,'')) last_at,f.hash
		FROM git_commit_files f JOIN git_commits g ON g.repository_id=f.repository_id AND g.hash=f.hash
		WHERE f.repository_id=? AND f.path>=? AND f.path<? GROUP BY child`, append(childArgs(start), repository, low, high)...)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if found := entry(row); found != nil {
			found.Commits = integer(row["commits"])
			found.LastCommit, found.LastCommitAt = firstString(row["hash"]), firstString(row["last_at"])
			if last := firstString(row["last_at"]); laterThan(last, found.LastAt) {
				found.LastAt = last
			}
		}
	}
	return entries, nil
}

// folderPath is a folder project's file or directory as session_files
// records it: under the folder when relative, else its own folder path
// (~/… or absolute), for files its sessions edited elsewhere.
func folderPath(folder, relative string) string {
	switch {
	case relative == "":
		return folder
	case strings.HasPrefix(relative, "~") || strings.HasPrefix(relative, "/"):
		return relative
	}
	return folder + "/" + relative
}

// folderRelative is a session_files path as its folder project's tree names
// it: relative under the folder, else unchanged.
func folderRelative(folder, path string) string {
	if inside, ok := strings.CutPrefix(path, folder+"/"); ok && folder != "" {
		return inside
	}
	return path
}

// folderTreeEntries are a folder project directory's children: the files its
// sessions edited, under the folder as relative paths and elsewhere by their
// own folder paths (~/… or absolute).
func (c *Catalog) folderTreeEntries(ctx context.Context, folder, dir string) (map[string]*fileTreeEntry, error) {
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT sf.path,COUNT(DISTINCT sf.workspace_id) sessions,MAX(COALESCE(sf.last_edit_at,'')) last_at,sf.workspace_id
		FROM session_files sf JOIN workspaces w ON w.id=sf.workspace_id
		WHERE w.project_folder=? AND w.repository_id IS NULL AND w.folder_repository_id IS NULL AND sf.repository_id IS NULL
		AND sf.workspace_id NOT IN (SELECT workspace_id FROM tool_mirror_workspaces) GROUP BY sf.path`, folder)
	if err != nil {
		return nil, err
	}
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	entries := map[string]*fileTreeEntry{}
	for _, row := range rows {
		relative, ok := strings.CutPrefix(folderRelative(folder, firstString(row["path"])), prefix)
		if !ok || relative == "" {
			continue
		}
		// An absolute path outside the folder keeps its leading slash on
		// its first segment, so /opt is a child of the root.
		lead := ""
		if prefix == "" && strings.HasPrefix(relative, "/") {
			lead, relative = "/", relative[1:]
		}
		name, _, nested := strings.Cut(relative, "/")
		name = lead + name
		kind := "file"
		if nested {
			kind = "dir"
		}
		entry := entries[name]
		if entry == nil {
			entry = &fileTreeEntry{Name: name, Path: prefix + name, Kind: kind}
			entries[name] = entry
		}
		entry.Sessions += integer(row["sessions"])
		if last := firstString(row["last_at"]); laterThan(last, entry.LastAt) || entry.LastSession == "" {
			entry.LastAt, entry.LastSession, entry.LastSessionAt = last, firstString(row["workspace_id"]), last
		}
	}
	return entries, nil
}

// addSessionTitles names each entry's last session.
func (c *Catalog) addSessionTitles(ctx context.Context, entries []map[string]any, sessions []string) error {
	if len(sessions) == 0 {
		return nil
	}
	slices.Sort(sessions)
	sessions = slices.Compact(sessions)
	titles := map[string]any{}
	for start := 0; start < len(sessions); start += 500 {
		batch := sessions[start:min(start+500, len(sessions))]
		rows, err := queryMapsContext(ctx, c.reader(), `SELECT id,title FROM workspaces WHERE id IN (`+placeholders(len(batch))+`)`, anySlice(batch)...)
		if err != nil {
			return err
		}
		for _, row := range rows {
			titles[firstString(row["id"])] = row["title"]
		}
	}
	for _, entry := range entries {
		if session, ok := entry["last_session"].(map[string]any); ok {
			session["title"] = titles[firstString(session["workspace_id"])]
		}
	}
	return nil
}

func anySlice[T any](values []T) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

// fileFindLimit bounds the paths Go to file lists.
var fileFindLimit = 50

// FileFind is a project's files whose paths hold every word of a query,
// those whose names hold them first, then shorter paths: the tree's files
// and those sessions edited or commits changed for a repository, those
// sessions edited for a folder.
func (c *Catalog) FileFind(ctx context.Context, projectValue, query string) (map[string]any, error) {
	projects, err := c.fileProjects(ctx)
	if err != nil {
		return nil, err
	}
	project, choices, err := projects.choose(ctx, projectValue)
	if err != nil || choices != nil {
		return map[string]any{"choices": choices}, err
	}
	words := strings.Fields(strings.ToLower(query))
	result := map[string]any{"project": project.value(), "query": query, "files": []map[string]any{}}
	if len(words) == 0 {
		return result, nil
	}
	like, args := "", []any{}
	for _, word := range words {
		like += ` AND lower(p) LIKE ? ESCAPE '\'`
		args = append(args, likePattern(word, "%", "%"))
	}
	var statement string
	if project.Kind == "repository" {
		keyPrefix := "repository:" + project.RepositoryID + ":"
		statement = `SELECT p,MAX(t) in_tree FROM (SELECT path p,1 t FROM git_tree_files WHERE repository_id=?
			UNION ALL SELECT substr(file_key,?) p,0 t FROM session_files WHERE file_key>=? AND file_key<?
			UNION ALL SELECT path p,0 t FROM git_commit_files WHERE repository_id=?) WHERE 1` + like + ` GROUP BY p`
		low, high := prefixRange(keyPrefix)
		args = append([]any{project.RepositoryID, sqlStart(keyPrefix), low, high, project.RepositoryID}, args...)
	} else {
		statement = `SELECT p,0 in_tree FROM (SELECT DISTINCT sf.path p FROM session_files sf JOIN workspaces w ON w.id=sf.workspace_id
			WHERE w.project_folder=? AND w.repository_id IS NULL AND w.folder_repository_id IS NULL AND sf.repository_id IS NULL) WHERE 1` + like
		args = append([]any{project.Folder}, args...)
	}
	rows, err := queryMapsContext(ctx, c.reader(), statement+` LIMIT 5000`, args...)
	if err != nil {
		return nil, err
	}
	type found struct {
		path   string
		inTree bool
		named  bool
	}
	matches := make([]found, 0, len(rows))
	for _, row := range rows {
		relative := firstString(row["p"])
		if project.Kind != "repository" {
			relative = folderRelative(project.Folder, relative)
		}
		name := strings.ToLower(path.Base(relative))
		named := true
		for _, word := range words {
			named = named && strings.Contains(name, word)
		}
		matches = append(matches, found{path: relative, inTree: integer(row["in_tree"]) != 0, named: named})
	}
	slices.SortFunc(matches, func(a, b found) int {
		if a.named != b.named {
			return map[bool]int{true: -1, false: 1}[a.named]
		}
		if len(a.path) != len(b.path) {
			return len(a.path) - len(b.path)
		}
		return strings.Compare(a.path, b.path)
	})
	files := make([]map[string]any, 0, min(len(matches), fileFindLimit))
	for _, match := range matches[:min(len(matches), fileFindLimit)] {
		files = append(files, map[string]any{"path": match.path, "in_tree": match.inTree, "url": filePageURL(project.Ref, match.path, "", "")})
	}
	result["files"], result["more"] = files, len(matches) > fileFindLimit
	return result, nil
}

// directoryActivity is a directory's latest sessions, by their last edit
// under it, and latest commits, by author time.
func (c *Catalog) directoryActivity(ctx context.Context, projects *fileProjects, project fileProject, dir string) ([]map[string]any, []map[string]any, error) {
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	var filter string
	var args []any
	if project.Kind == "repository" {
		low, high := prefixRange("repository:" + project.RepositoryID + ":" + prefix)
		filter, args = `sf.file_key>=? AND sf.file_key<?`, []any{low, high}
	} else {
		filter, args = `sf.repository_id IS NULL AND sf.workspace_id IN (SELECT id FROM workspaces WHERE project_folder=? AND repository_id IS NULL AND folder_repository_id IS NULL)`, []any{project.Folder}
		if dir != "" {
			low, high := prefixRange(folderPath(project.Folder, dir) + "/")
			filter, args = filter+` AND sf.path>=? AND sf.path<?`, append(args, low, high)
		}
	}
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT sf.workspace_id,COUNT(*) files,SUM(sf.edits) edits,MAX(sf.last_edit_at) last_edit_at,w.title,
		`+outcomeLabelSQL("w")+` outcome_label,`+outcomeWhySQL("w")+` outcome_why
		FROM session_files sf JOIN workspaces w ON w.id=sf.workspace_id
		WHERE `+filter+` AND sf.workspace_id NOT IN (SELECT workspace_id FROM tool_mirror_workspaces)
		GROUP BY sf.workspace_id ORDER BY MAX(COALESCE(sf.last_edit_at,'')) DESC,sf.workspace_id LIMIT ?`, append(args, fileTreeActivityLimit)...)
	if err != nil {
		return nil, nil, err
	}
	sessions := make([]map[string]any, len(rows))
	for index, row := range rows {
		sessions[index] = map[string]any{"workspace_id": row["workspace_id"], "title": row["title"], "files": row["files"], "edits": row["edits"],
			"last_edit_at": row["last_edit_at"], "outcome_label": row["outcome_label"], "outcome_why": row["outcome_why"],
			"url": "/work/" + url.PathEscape(firstString(row["workspace_id"]))}
	}
	commits := []map[string]any{}
	if project.Kind == "repository" {
		low, high := prefixRange(prefix)
		rows, err := queryMapsContext(ctx, c.reader(), `SELECT g.hash,g.author_at,g.on_mainline,g.reverted_by,f.files,f.added,f.removed
			FROM (SELECT hash,COUNT(*) files,SUM(lines_added) added,SUM(lines_removed) removed FROM git_commit_files
				WHERE repository_id=? AND path>=? AND path<? GROUP BY hash) f
			JOIN git_commits g ON g.repository_id=? AND g.hash=f.hash ORDER BY g.author_at DESC,g.hash LIMIT ?`,
			project.RepositoryID, low, high, project.RepositoryID, fileTreeActivityLimit)
		if err != nil {
			return nil, nil, err
		}
		for _, row := range rows {
			commit := map[string]any{"hash": row["hash"], "author_at": row["author_at"], "files": row["files"], "lines_added": row["added"], "lines_removed": row["removed"],
				"on_mainline": nullableBool(row["on_mainline"]), "reverted_by": row["reverted_by"]}
			links, err := c.commitSessions(ctx, project.RepositoryID, firstString(row["hash"]))
			if err != nil {
				return nil, nil, err
			}
			commit["sessions"] = links
			commits = append(commits, commit)
		}
	}
	return sessions, commits, nil
}

func nullableBool(value any) any {
	if value == nil {
		return nil
	}
	return integer(value) != 0
}

// commitSessions are the sessions linked to a commit, each with how, how
// sure, and the place in its transcript that printed or made the commit. A
// squash on mainline is also linked to the sessions whose commits it took in
// (their mainline evidence names it), as "landed", each once at its surest.
func (c *Catalog) commitSessions(ctx context.Context, repository, hash string) ([]map[string]any, error) {
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT * FROM (SELECT l.workspace_id,l.method,l.confidence,l.evidence_json,w.title,'' via FROM session_commit_links l
		LEFT JOIN workspaces w ON w.id=l.workspace_id WHERE l.repository_id=? AND l.hash=?
		UNION ALL SELECT l.workspace_id,'landed',l.confidence,l.evidence_json,w.title,g.hash via FROM git_commits g
		JOIN session_commit_links l ON l.repository_id=g.repository_id AND l.hash=g.hash LEFT JOIN workspaces w ON w.id=l.workspace_id
		WHERE g.repository_id=? AND length(?)>=12 AND g.mainline_evidence LIKE '% '||substr(?,1,12)||' on %')
		ORDER BY confidence DESC,via<>'',workspace_id`, repository, hash, repository, hash, hash)
	if err != nil {
		return nil, err
	}
	links := make([]map[string]any, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		workspace := firstString(row["workspace_id"])
		if seen[workspace] {
			continue
		}
		seen[workspace] = true
		evidence := map[string]any{}
		_ = json.Unmarshal([]byte(firstString(row["evidence_json"])), &evidence)
		if via := firstString(row["via"]); via != "" {
			evidence = map[string]any{"of": via, "by": firstString(row["method"]), "place": evidence}
		}
		place := evidencePlace(evidence)
		if nested, ok := evidence["place"].(map[string]any); ok {
			place = evidencePlace(nested)
		}
		links = append(links, map[string]any{"workspace_id": workspace, "title": row["title"], "method": row["method"], "confidence": row["confidence"],
			"counted": floatOr(row["confidence"]) >= outcomeGitCounted, "evidence": evidence, "url": placeURL(workspace, place)})
	}
	return links, nil
}

type FileCommitOptions struct {
	Project, Commit string
}

// FileCommit is one commit of a project's repository: when it was made,
// where it stands, the files it changed and the sessions linked to it.
func (c *Catalog) FileCommit(ctx context.Context, options FileCommitOptions) (map[string]any, error) {
	projects, err := c.fileProjects(ctx)
	if err != nil {
		return nil, err
	}
	project, choices, err := projects.choose(ctx, options.Project)
	if err != nil || choices != nil {
		return map[string]any{"choices": choices}, err
	}
	if project.Kind != "repository" {
		return nil, fmt.Errorf("%s is a folder, not a Git repository: it has no commits", project.Name)
	}
	hash := strings.ToLower(strings.TrimSpace(options.Commit))
	if len(hash) < 4 || strings.Trim(hash, "0123456789abcdef") != "" {
		return nil, fmt.Errorf("commit must be a hash of at least 4 hex digits")
	}
	low, high := prefixRange(hash)
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT hash,author_at,committed_at,parents,files,lines_added,lines_removed,on_mainline,mainline_evidence,reverted_by,revert_evidence
		FROM git_commits WHERE repository_id=? AND hash>=? AND hash<? ORDER BY hash LIMIT 2`, project.RepositoryID, low, high)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"project": project.value()}
	git, err := c.fileGitState(ctx, project.RepositoryID)
	if err != nil {
		return nil, err
	}
	result["git"] = git
	if len(rows) != 1 {
		result["note"] = "Local Git did not keep this commit: it is older than the history read (git.commits_since), on no branch read, or not in this repository."
		if len(rows) > 1 {
			result["note"] = "Several commits start with " + hash + "; give more of the hash."
		}
		return result, nil
	}
	row := rows[0]
	full := firstString(row["hash"])
	commit := map[string]any{"hash": full, "author_at": row["author_at"], "committed_at": row["committed_at"], "parents": row["parents"],
		"lines_added": row["lines_added"], "lines_removed": row["lines_removed"], "on_mainline": nullableBool(row["on_mainline"]),
		"mainline_evidence": row["mainline_evidence"], "reverted_by": row["reverted_by"], "revert_evidence": row["revert_evidence"]}
	files, err := queryMapsContext(ctx, c.reader(), `SELECT f.path,f.lines_added,f.lines_removed,EXISTS(SELECT 1 FROM git_tree_files t WHERE t.repository_id=f.repository_id AND t.path=f.path) in_tree,
		(SELECT COUNT(DISTINCT sf.workspace_id) FROM session_files sf WHERE sf.file_key='repository:'||f.repository_id||':'||f.path) sessions
		FROM git_commit_files f WHERE f.repository_id=? AND f.hash=? ORDER BY f.path`, project.RepositoryID, full)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		file["in_tree"] = integer(file["in_tree"]) != 0
	}
	commit["files"] = files
	if commit["sessions"], err = c.commitSessions(ctx, project.RepositoryID, full); err != nil {
		return nil, err
	}
	result["commit"] = commit
	return result, nil
}
