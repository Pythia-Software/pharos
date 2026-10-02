package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// repoRoot is a known checkout of a repository. ID is empty when only the
// name is known.
type repoRoot struct{ Location, Repository, ID string }

func repoRelativePath(path, cwd string, roots []repoRoot) (rel, repo, scope string) {
	rel, root, scope := resolveRepoPath(path, cwd, roots)
	return rel, root.Repository, scope
}

// resolveRepoPath returns a path relative to its repository checkout and the
// repository it belongs to. A worktree's repository comes from a known
// checkout at its root or at the clone it hangs off, so it follows repository
// merges and renames; the directory name is the fallback.
func resolveRepoPath(path, cwd string, roots []repoRoot) (rel string, repo repoRoot, scope string) {
	if path == "" {
		return "", repoRoot{}, ""
	}
	absolute := absoluteToolPath(path, cwd)
	if absolute == "" {
		return "", repoRoot{}, "external"
	}
	return resolveAbsolutePath(absolute, roots)
}

// absoluteToolPath is the path a tool call named, resolved against its working
// directory; it is empty for a relative path with no working directory.
func absoluteToolPath(path, cwd string) string {
	if path == "" {
		return ""
	}
	path, cwd = expandHome(path), expandHome(cwd)
	if !filepath.IsAbs(path) {
		if cwd == "" {
			return ""
		}
		path = filepath.Join(cwd, path)
	}
	return filepath.Clean(path)
}

func expandHome(path string) string {
	home, _ := os.UserHomeDir()
	return expandHomeIn(home, path)
}

func expandHomeIn(home, path string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}

// resolveAbsolutePath resolves a path absoluteToolPath returned.
func resolveAbsolutePath(path string, roots []repoRoot) (rel string, repo repoRoot, scope string) {
	home, _ := os.UserHomeDir()
	expand := func(path string) string { return expandHomeIn(home, path) }
	parts := strings.Split(strings.Trim(path, "/"), "/")
	best := -1
	known := func(location string) (repoRoot, bool) {
		for _, root := range roots {
			if filepath.Clean(expand(root.Location)) == location {
				return root, true
			}
		}
		return repoRoot{}, false
	}
	// knownUnder finds the single repository with checkouts in a Conductor
	// repository directory; several mean the directory name was reused. Only
	// a checkout counts, not a clone kept inside one (a test fixture under
	// .context, say).
	knownUnder := func(prefix string) (repoRoot, bool) {
		var found repoRoot
		for _, root := range roots {
			checkout, ok := strings.CutPrefix(filepath.Clean(expand(root.Location)), prefix)
			if root.ID == "" || !ok || checkout == "" || strings.Contains(checkout, "/") {
				continue
			}
			if found.ID != "" && found.ID != root.ID {
				return repoRoot{}, false
			}
			found = root
		}
		return found, found.ID != ""
	}
	for i := range parts {
		skip := 0
		switch {
		case parts[i] == ".conductor" && i+1 < len(parts):
			skip = 2
		case parts[i] == ".claude" && i+2 < len(parts) && parts[i+1] == "worktrees":
			skip = 3
		case parts[i] == ".task-worktrees" && i+1 < len(parts):
			skip = 2
		case parts[i] == ".codex" && i+3 < len(parts) && parts[i+1] == "worktrees":
			skip = 4
		case parts[i] == "conductor" && i+3 < len(parts) && parts[i+1] == "workspaces":
			skip = 4
		}
		if skip > 0 && i+skip <= len(parts) && i > best {
			best = i
			// The outermost worktree names the repository; nested agent
			// worktrees inside it keep that name.
			if repo.Repository == "" {
				if root, ok := known("/" + strings.Join(parts[:i+skip], "/")); ok {
					repo = root
				} else if root, ok := known("/" + strings.Join(parts[:i], "/")); ok && parts[i] != "conductor" {
					repo = root
				} else if root, ok := conductorRepository(parts, i, knownUnder); ok {
					repo = root
				} else if parts[i] == "conductor" {
					repo = repoRoot{Repository: parts[i+2]}
				} else if i > 0 {
					repo = repoRoot{Repository: parts[i-1]}
				}
			}
			rel = strings.Join(parts[i+skip:], "/")
		}
	}
	if best >= 0 {
		if rel == "" {
			rel = "."
		}
		return rel, repo, "repo"
	}
	longest := -1
	for _, root := range roots {
		base := filepath.Clean(expand(root.Location))
		if base == "." || base == "" {
			continue
		}
		if path == base || strings.HasPrefix(path, base+string(filepath.Separator)) {
			if len(base) > longest {
				longest = len(base)
				rel, _ = filepath.Rel(base, path)
				repo = root
			}
		}
	}
	if longest >= 0 {
		return filepath.ToSlash(rel), repo, "repo"
	}
	if strings.HasPrefix(path, "/tmp/") || strings.HasPrefix(path, "/private/tmp/") || strings.HasPrefix(path, "/var/folders/") {
		return "", repoRoot{}, "temp"
	}
	if strings.HasPrefix(path, filepath.Join(home, ".claude")+"/") || strings.HasPrefix(path, filepath.Join(home, ".codex")+"/") {
		return "", repoRoot{}, "agent_home"
	}
	return "", repoRoot{}, "external"
}

func toolRepositoryLocations(tx queryer) ([]repoRoot, error) {
	rows, err := queryMapsContext(context.Background(), tx, `SELECT id,display_name,local_locations_json FROM repositories`)
	if err != nil {
		return nil, err
	}
	roots := []repoRoot{}
	for _, row := range rows {
		var locations []string
		if json.Unmarshal([]byte(firstString(row["local_locations_json"])), &locations) == nil {
			for _, location := range locations {
				roots = append(roots, repoRoot{location, firstString(row["display_name"]), firstString(row["id"])})
			}
		}
	}
	return roots, nil
}

func toolRepoRoots(tx queryer, workspaceID string, locations []repoRoot) ([]repoRoot, error) {
	rows, err := queryMapsContext(context.Background(), tx, `SELECT w.location,r.display_name,r.id FROM workspaces w LEFT JOIN repositories r ON r.id=w.repository_id WHERE w.id=?`, workspaceID)
	if err != nil {
		return nil, err
	}
	roots := make([]repoRoot, 0, len(locations)+1)
	if len(rows) > 0 {
		roots = append(roots, repoRoot{firstString(rows[0]["location"]), firstString(rows[0]["display_name"]), firstString(rows[0]["id"])})
	}
	return append(roots, locations...), nil
}

// conductorRepository is the one repository with checkouts in the Conductor
// repository directory that parts[i] ("conductor") starts, if any.
func conductorRepository(parts []string, i int, knownUnder func(string) (repoRoot, bool)) (repoRoot, bool) {
	if parts[i] != "conductor" || i+3 > len(parts) {
		return repoRoot{}, false
	}
	return knownUnder("/" + strings.Join(parts[:i+3], "/") + "/")
}

// conductorCheckout names the Conductor repository directory a location is
// in (".../conductor/workspaces/<repo>") and whether the location is one of
// its checkouts, not a directory inside one.
func conductorCheckout(location string) (directory string, checkout bool) {
	parts := strings.Split(strings.Trim(filepath.Clean(expandHome(location)), "/"), "/")
	for i := range parts {
		if parts[i] == "conductor" && i+3 < len(parts) && parts[i+1] == "workspaces" {
			return "/" + strings.Join(parts[:i+3], "/"), len(parts) == i+4
		}
	}
	return "", false
}

// staleToolPaths names the directories whose tool call paths may resolve to
// another repository once repository id has the added locations. A new
// checkout is known by its own path; when id had no checkout in that
// Conductor repository directory before, which single repository has
// checkouts there may change too, so the whole directory is named.
func staleToolPaths(added []string, id string, before []repositoryIdentity) []string {
	stale := []string{}
	for _, location := range added {
		location = filepath.Clean(expandHome(location))
		directory, checkout := conductorCheckout(location)
		if !checkout || slices.ContainsFunc(before, func(item repositoryIdentity) bool {
			return item.ID == id && slices.ContainsFunc(item.Locations, func(known string) bool {
				other, isCheckout := conductorCheckout(known)
				return isCheckout && other == directory
			})
		}) {
			stale = append(stale, location)
		} else {
			stale = append(stale, directory)
		}
	}
	return stale
}

// reresolveToolPaths resolves the repository of the tool calls under the
// given directories again, after the known checkouts changed, so the ledger
// holds what building it now would. Rows built before path_absolute was
// recorded are left to BackfillToolLedger.
func reresolveToolPaths(tx *sql.Tx, directories []string) error {
	slices.Sort(directories)
	outermost := []string{}
	for _, directory := range directories {
		if count := len(outermost); count > 0 && (directory == outermost[count-1] || strings.HasPrefix(directory, outermost[count-1]+"/")) {
			continue
		}
		outermost = append(outermost, directory)
	}
	if len(outermost) == 0 {
		return nil
	}
	locations, err := toolRepositoryLocations(tx)
	if err != nil {
		return err
	}
	roots := map[string][]repoRoot{}
	changed := false
	for _, directory := range outermost {
		// "0" sorts just after "/", so the range holds the directory and
		// everything in it (and a few siblings, which resolve unchanged).
		rows, err := queryMaps(tx, `SELECT id,workspace_id,path_absolute,COALESCE(repo_path,'') repo_path,COALESCE(path_repository,'') path_repository,
			COALESCE(path_repository_id,'') path_repository_id,COALESCE(path_scope,'') path_scope FROM tool_calls WHERE path_absolute>=? AND path_absolute<?`, directory, directory+"0")
		if err != nil {
			return err
		}
		for _, row := range rows {
			workspaceID := firstString(row["workspace_id"])
			if _, ok := roots[workspaceID]; !ok {
				if roots[workspaceID], err = toolRepoRoots(tx, workspaceID, locations); err != nil {
					return err
				}
			}
			rel, repo, scope := resolveAbsolutePath(firstString(row["path_absolute"]), roots[workspaceID])
			if rel == firstString(row["repo_path"]) && repo.Repository == firstString(row["path_repository"]) && repo.ID == firstString(row["path_repository_id"]) && scope == firstString(row["path_scope"]) {
				continue
			}
			if _, err := tx.Exec(`UPDATE tool_calls SET repo_path=?,path_repository=?,path_repository_id=?,path_scope=? WHERE id=?`,
				nilIfEmpty(rel), nilIfEmpty(repo.Repository), nilIfEmpty(repo.ID), nilIfEmpty(scope), row["id"]); err != nil {
				return err
			}
			changed = true
		}
	}
	if changed {
		return bumpToolLedgerGeneration(tx)
	}
	return nil
}
