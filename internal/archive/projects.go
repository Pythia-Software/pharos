package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// A project is what every workspace is grouped by: its repository, else the
// repository whose checkout holds its folder, else its working folder, else
// none (a chat export). See docs/projects-and-outcomes.md.

// folderKey is a working directory as a folder project names it: cleaned, with
// a home directory (any /Users/<name> or /home/<name>) as ~. It reads nothing
// of this host, so every Mac sharing a library derives the same key and none
// rewrites another's.
func folderKey(location string) string {
	location = strings.TrimSpace(location)
	if location == "" {
		return ""
	}
	if location == "~" || strings.HasPrefix(location, "~/") {
		return filepath.Clean(location)
	}
	if !filepath.IsAbs(location) {
		return ""
	}
	location = filepath.Clean(location)
	parts := strings.SplitN(strings.TrimPrefix(location, "/"), "/", 3)
	if len(parts) >= 2 && (parts[0] == "Users" || parts[0] == "home") && parts[1] != "" && parts[1] != "Shared" {
		if len(parts) == 2 {
			return "~"
		}
		return "~/" + parts[2]
	}
	return location
}

// projectFolder is the folder project of a working directory. Temporary
// directories (temporaryFolders) are one project, so each scratch directory
// is not one. Tests, whose own directories are temporary, clear the list.
func projectFolder(location string) string {
	key := folderKey(location)
	for _, temp := range temporaryFolders {
		if key == temp || strings.HasPrefix(key, temp+"/") {
			return "/tmp"
		}
	}
	return key
}

// projectRoot is a checkout location of a repository, as a folder key.
type projectRoot struct{ folder, repository string }

func projectRoots(ctx context.Context, q queryer) ([]projectRoot, error) {
	rows, err := queryMapsContext(ctx, q, `SELECT id,local_locations_json FROM repositories`)
	if err != nil {
		return nil, err
	}
	roots := []projectRoot{}
	for _, row := range rows {
		var locations []string
		_ = json.Unmarshal([]byte(firstString(row["local_locations_json"])), &locations)
		for _, location := range locations {
			if key := folderKey(location); key != "" {
				roots = append(roots, projectRoot{key, firstString(row["id"])})
			}
		}
	}
	return roots, nil
}

// folderRepository is the one repository with a checkout at the folder or
// above it, the nearest one winning. A checkout at the home directory or at /
// claims only itself, or a home directory under Git would swallow every
// folder. Two repositories at the nearest checkout leave the folder alone.
func folderRepository(folder string, roots []projectRoot) string {
	best, found := "", ""
	for _, root := range roots {
		inside := folder == root.folder || root.folder != "~" && root.folder != "/" && strings.HasPrefix(folder, strings.TrimSuffix(root.folder, "/")+"/")
		if !inside || len(root.folder) < len(best) {
			continue
		}
		if len(root.folder) > len(best) {
			best, found = root.folder, root.repository
		} else if found != root.repository {
			found = ""
		}
	}
	return found
}

// projectChange is a workspace whose project columns differ from what its
// location and repositories derive.
type projectChange struct{ id, folder, repository string }

// projectChanges derives the project columns of the workspaces predicate (over
// w) selects, and returns those that differ from the stored ones. It only reads.
func projectChanges(ctx context.Context, q queryer, predicate string, args ...any) ([]projectChange, error) {
	rows, err := queryMapsContext(ctx, q, `SELECT w.id,w.repository_id,w.location,w.project_folder,w.folder_repository_id FROM workspaces w WHERE `+predicate, args...)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	var roots []projectRoot
	changes := []projectChange{}
	for _, row := range rows {
		location := firstString(row["location"])
		folder, repository := projectFolder(location), ""
		if firstString(row["repository_id"]) == "" && folder != "" {
			if roots == nil {
				if roots, err = projectRoots(ctx, q); err != nil {
					return nil, err
				}
			}
			repository = folderRepository(folderKey(location), roots)
		}
		if folder != firstString(row["project_folder"]) || repository != firstString(row["folder_repository_id"]) {
			changes = append(changes, projectChange{firstString(row["id"]), folder, repository})
		}
	}
	return changes, nil
}

func applyProjectChanges(ctx context.Context, tx *sql.Tx, changes []projectChange) error {
	for _, change := range changes {
		if _, err := tx.ExecContext(ctx, `UPDATE workspaces SET project_folder=?,folder_repository_id=? WHERE id=?`, nilIfEmpty(change.folder), nilIfEmpty(change.repository), change.id); err != nil {
			return err
		}
	}
	return nil
}

// resolveProjects sets the project columns of the workspaces predicate (over
// w) selects. Only ingest and maintenance call it: requests never write.
func resolveProjects(ctx context.Context, tx *sql.Tx, predicate string, args ...any) error {
	changes, err := projectChanges(ctx, tx, predicate, args...)
	if err != nil {
		return err
	}
	return applyProjectChanges(ctx, tx, changes)
}

// resolveFolderProjects resolves again the workspaces without a repository
// whose folders are at or under the given checkout paths, since a folder may
// now be in one of those checkouts. A folder elsewhere keeps its nearest
// checkout, so only these can change. It runs inside ingest's writer, so it
// reads only those workspaces (by workspaces_project_folder_idx).
func resolveFolderProjects(ctx context.Context, tx *sql.Tx, paths []string) error {
	keys := []string{}
	for _, path := range paths {
		if key := folderKey(path); key != "" {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	temporary := false
	for index, key := range keys {
		if index > 0 && key == keys[index-1] {
			continue
		}
		// A checkout at the home directory or at / claims only itself.
		predicate, args := `w.project_folder=?`, []any{key}
		if key != "~" && key != "/" {
			predicate, args = `(w.project_folder=? OR w.project_folder>=? AND w.project_folder<?)`, []any{key, key + "/", key + "0"}
		}
		if err := resolveProjects(ctx, tx, `w.repository_id IS NULL AND w.location IS NOT NULL AND `+predicate, args...); err != nil {
			return err
		}
		// Every temporary directory is the one /tmp project, which these
		// ranges cannot see into.
		for _, folder := range temporaryFolders {
			temporary = temporary || folder == key || strings.HasPrefix(folder, strings.TrimSuffix(key, "/")+"/") || strings.HasPrefix(key, folder+"/")
		}
	}
	if !temporary {
		return nil
	}
	return resolveProjects(ctx, tx, `w.repository_id IS NULL AND w.location IS NOT NULL AND w.project_folder='/tmp'`)
}

// changedRoots is the folders of the checkouts in one list of roots and not
// the other, to tell which folders a write that bypasses
// upsertRepositoryChanges may have moved into or out of a repository.
func changedRoots(before, after []projectRoot) []string {
	changed := []string{}
	for _, pair := range [][2][]projectRoot{{before, after}, {after, before}} {
		for _, root := range pair[0] {
			if !slices.Contains(pair[1], root) {
				changed = append(changed, root.folder)
			}
		}
	}
	return changed
}

// migrateProjects adds the project columns and derives them for every
// workspace. It runs whenever a build first opens the catalog, so it also
// repairs what an older build wrote without them (repairProjects does so
// while this build stays open); it writes only the rows that differ.
func (c *Catalog) migrateProjects() error {
	for _, column := range []string{"project_folder", "folder_repository_id"} {
		has, err := c.hasColumn("workspaces", column)
		if err != nil {
			return err
		}
		if !has {
			if _, err := c.DB.Exec("ALTER TABLE workspaces ADD COLUMN " + column + " TEXT"); err != nil {
				return err
			}
		}
	}
	// resolveFolderProjects reads folders of one checkout by this index.
	if _, err := c.DB.Exec(`CREATE INDEX IF NOT EXISTS workspaces_project_folder_idx ON workspaces(project_folder) WHERE repository_id IS NULL`); err != nil {
		return err
	}
	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := resolveProjects(context.Background(), tx, "1"); err != nil {
		return err
	}
	return tx.Commit()
}

// repairProjects derives every workspace's project columns and writes those
// that differ: a build from before projects inserts workspaces without them
// and moves locations without updating them, on this Mac or another sharing
// the library. The derivation only reads, so a library with nothing to repair
// takes no write lock.
func (c *Catalog) repairProjects(ctx context.Context) error {
	changes, err := projectChanges(ctx, c.DB, "1")
	if err != nil || len(changes) == 0 {
		return err
	}
	return c.writeTransaction(ctx, "repair projects", func(tx *sql.Tx) error {
		// Derive again under the write lock, in case a writer changed them.
		return resolveProjects(ctx, tx, "w.id IN (SELECT value FROM json_each(?))", jsonText(projectChangeIDs(changes)))
	})
}

func projectChangeIDs(changes []projectChange) []string {
	ids := make([]string, len(changes))
	for index, change := range changes {
		ids[index] = change.id
	}
	return ids
}

// projectJoin joins the repository of the project of workspace alias w as pj.
func projectJoin(w string) string {
	return " LEFT JOIN repositories pj ON pj.id=COALESCE(" + w + ".repository_id," + w + ".folder_repository_id)"
}

// projectNameSQL is the project's name: its repository's, else its folder.
// It is NULL only for a workspace with no project.
func projectNameSQL(w string) string {
	return "COALESCE(pj.display_name," + w + ".project_folder)"
}

// projectKindSQL is repository, folder, or none: three states, never NULL.
func projectKindSQL(w string) string {
	return "CASE WHEN pj.id IS NOT NULL THEN 'repository' WHEN " + w + ".project_folder IS NOT NULL THEN 'folder' ELSE 'none' END"
}

// projectIDSQL is a stable project key, NULL for no project.
func projectIDSQL(w string) string {
	return "CASE WHEN pj.id IS NOT NULL THEN 'repository:'||pj.id WHEN " + w + ".project_folder IS NOT NULL THEN 'folder:'||" + w + ".project_folder END"
}

// projectPredicate selects the workspaces (alias w) of the projects a project
// filter names. A value that is exactly a project's name, a repository's
// remote, or a folder project's key (~/notes, ~, or its absolute path) names
// only that project. Otherwise it matches as the repository filter does, by
// any part of a repository's name or remote, including the folders checked
// out inside it, or any part of a folder project's key. A workspace in a
// repository is that repository's project, even inside a folder project's
// directory, so a folder matches only workspaces outside any repository, as
// projectKindSQL tells them. Its arguments are projectArgs.
func projectPredicate(w string) string {
	repository := "COALESCE(" + w + ".repository_id," + w + ".folder_repository_id)"
	outside := "NOT EXISTS(SELECT 1 FROM repositories pf WHERE pf.id=" + repository + ")"
	exactRepository := "(lower(%[1]s.display_name)=lower(?) OR lower(COALESCE(%[1]s.canonical_remote,''))=lower(?))"
	// Uncorrelated, so SQLite evaluates it once per statement.
	exact := "(EXISTS(SELECT 1 FROM repositories px WHERE " + fmt.Sprintf(exactRepository, "px") + ")" +
		" OR EXISTS(SELECT 1 FROM workspaces wx WHERE wx.repository_id IS NULL AND wx.project_folder=? AND NOT EXISTS(SELECT 1 FROM repositories pf WHERE pf.id=wx.folder_repository_id)))"
	return "(CASE WHEN " + exact + " THEN " +
		repository + " IN (SELECT pr.id FROM repositories pr WHERE " + fmt.Sprintf(exactRepository, "pr") + ") OR " + w + ".project_folder=? AND " + outside +
		" ELSE " + repository + " IN (SELECT pr.id FROM repositories pr WHERE " + repositoryNamePredicate("pr") + ")" +
		" OR instr(lower(" + w + ".project_folder),lower(?))>0 AND " + outside + " END)"
}

func projectArgs(value string) []any {
	folder := value
	if key := projectFolder(value); key != "" {
		folder = key
	}
	return []any{value, value, folder, value, value, folder, value, value, folder}
}

// checkProjectFilter reports an error when a project filter matches no
// project, which would otherwise filter everything out silently.
func (c *Catalog) checkProjectFilter(value string) error {
	matches, err := queryMaps(c.DB, `SELECT 1 FROM workspaces w WHERE `+projectPredicate("w")+` LIMIT 1`, projectArgs(value)...)
	if err != nil || len(matches) > 0 {
		return err
	}
	return fmt.Errorf("no project named %q. list_projects lists the projects Pharos knows: repositories by name, and folders as ~/path", value)
}

// listProjects is the list_projects tool: every project an agent can pass as
// the project filter, repositories and folders alike, most recently active
// first. A library without Git lists its folders here as completely as one
// with Git lists its repositories.
func (c *Catalog) listProjects(args map[string]any) (map[string]any, error) {
	query := strings.ToLower(strings.TrimSpace(firstString(args["query"])))
	kind := firstString(args["kind"])
	limit := clamp(int(integer(valueOr(args["limit"], 20))), 1, 100)
	budget := mcpBudget(args, 1500)
	// A path (the agent's working directory) selects the project it is in,
	// as the project rule places a session there: the nearest repository
	// checked out at or above it, else the nearest folder project above it.
	where, values, nearest := "", []any{}, false
	if path := strings.TrimSpace(firstString(args["path"])); path != "" {
		key := folderKey(path)
		if key == "" {
			return nil, fmt.Errorf("path must be absolute")
		}
		roots, err := projectRoots(context.Background(), c.DB)
		if err != nil {
			return nil, err
		}
		if repository := folderRepository(key, roots); repository != "" {
			where, values = " AND pj.id=?", []any{repository}
		} else {
			where, values, nearest = " AND pj.id IS NULL AND (w.project_folder=? OR substr(?,1,length(w.project_folder)+1)=w.project_folder||'/')", []any{key, key}, true
		}
	}
	rows, err := queryMaps(c.DB, `SELECT `+projectNameSQL("w")+` project,`+projectKindSQL("w")+` kind,MAX(pj.canonical_remote) remote,
		COUNT(c.id) conversations,MAX(COALESCE(c.ended_at,c.started_at,w.activity_at)) last_message_at,MAX(w.indexed_at) last_indexed_at
		FROM workspaces w`+projectJoin("w")+` JOIN conversations c ON c.workspace_id=w.id
		WHERE `+projectNameSQL("w")+` IS NOT NULL`+where+`
		GROUP BY `+projectIDSQL("w")+` ORDER BY last_message_at DESC,project`, values...)
	if err != nil {
		return nil, err
	}
	if nearest && len(rows) > 1 {
		// Folders nest: the deepest one holding the path is its project.
		deepest := slices.MaxFunc(rows, func(a, b map[string]any) int { return len(firstString(a["project"])) - len(firstString(b["project"])) })
		rows = []map[string]any{deepest}
	}
	items := []map[string]any{}
	for _, row := range rows {
		if kind != "" && row["kind"] != kind {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(firstString(row["project"])+"\n"+firstString(row["remote"])), query) {
			continue
		}
		if row["remote"] == nil {
			delete(row, "remote")
		}
		items = append(items, row)
	}
	result := map[string]any{"total": len(items)}
	if len(items) > limit {
		items = items[:limit]
		result["truncated"] = true
	}
	result["items"] = items
	// Chat exports without a folder belong to no project and match no filter.
	none, err := queryMaps(c.DB, `SELECT COUNT(*) n FROM conversations c JOIN workspaces w ON w.id=c.workspace_id
		WHERE w.repository_id IS NULL AND w.folder_repository_id IS NULL AND w.project_folder IS NULL`)
	if err != nil {
		return nil, err
	}
	result["conversations_without_project"] = none[0]["n"]
	fitConversationItems(result, budget)
	return result, nil
}
