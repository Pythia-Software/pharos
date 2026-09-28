package archive

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
	home, _ := os.UserHomeDir()
	expand := func(p string) string {
		if p == "~" {
			return home
		}
		if strings.HasPrefix(p, "~/") {
			return filepath.Join(home, p[2:])
		}
		return p
	}
	path, cwd = expand(path), expand(cwd)
	if !filepath.IsAbs(path) {
		if cwd == "" {
			return "", repoRoot{}, "external"
		}
		path = filepath.Join(cwd, path)
	}
	path = filepath.Clean(path)
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
	// repository directory; several mean the directory name was reused.
	knownUnder := func(prefix string) (repoRoot, bool) {
		var found repoRoot
		for _, root := range roots {
			if root.ID == "" || !strings.HasPrefix(filepath.Clean(expand(root.Location))+"/", prefix) {
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
