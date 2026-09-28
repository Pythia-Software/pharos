package archive

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type repoRoot struct{ Location, Repository string }

func repoRelativePath(path, cwd string, roots []repoRoot) (rel, repo, scope string) {
	if path == "" {
		return "", "", ""
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
			return "", "", "external"
		}
		path = filepath.Join(cwd, path)
	}
	path = filepath.Clean(path)
	parts := strings.Split(strings.Trim(path, "/"), "/")
	best := -1
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
			if parts[i] == "conductor" {
				repo = parts[i+2]
			} else if i > 0 {
				repo = parts[i-1]
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
				repo = root.Repository
			}
		}
	}
	if longest >= 0 {
		return filepath.ToSlash(rel), repo, "repo"
	}
	if strings.HasPrefix(path, "/tmp/") || strings.HasPrefix(path, "/private/tmp/") || strings.HasPrefix(path, "/var/folders/") {
		return "", "", "temp"
	}
	if strings.HasPrefix(path, filepath.Join(home, ".claude")+"/") || strings.HasPrefix(path, filepath.Join(home, ".codex")+"/") {
		return "", "", "agent_home"
	}
	return "", "", "external"
}

func toolRepositoryLocations(tx queryer) ([]repoRoot, error) {
	rows, err := queryMapsContext(context.Background(), tx, `SELECT display_name,local_locations_json FROM repositories`)
	if err != nil {
		return nil, err
	}
	roots := []repoRoot{}
	for _, row := range rows {
		var locations []string
		if json.Unmarshal([]byte(firstString(row["local_locations_json"])), &locations) == nil {
			for _, location := range locations {
				roots = append(roots, repoRoot{location, firstString(row["display_name"])})
			}
		}
	}
	return roots, nil
}

func toolRepoRoots(tx queryer, workspaceID string, locations []repoRoot) ([]repoRoot, error) {
	rows, err := queryMapsContext(context.Background(), tx, `SELECT w.location,r.display_name FROM workspaces w LEFT JOIN repositories r ON r.id=w.repository_id WHERE w.id=?`, workspaceID)
	if err != nil {
		return nil, err
	}
	roots := make([]repoRoot, 0, len(locations)+1)
	if len(rows) > 0 {
		roots = append(roots, repoRoot{firstString(rows[0]["location"]), firstString(rows[0]["display_name"])})
	}
	return append(roots, locations...), nil
}
