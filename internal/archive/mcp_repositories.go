package archive

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// listRepositories is the list_repositories tool: the repositories an agent
// can pass as the repository filter, most recently active first, with when
// Pharos last captured each source. A query matches a repository's name,
// remote, aliases, or checkout paths; a path matches the repository checked
// out at or above it.
func (c *Catalog) listRepositories(args map[string]any) (map[string]any, error) {
	query := strings.ToLower(strings.TrimSpace(firstString(args["query"])))
	path := filepath.Clean(strings.TrimSpace(firstString(args["path"])))
	if path == "." {
		path = ""
	}
	limit := clamp(int(integer(valueOr(args["limit"], 20))), 1, 100)
	budget := mcpBudget(args, 1500)
	rows, err := queryMaps(c.DB, `SELECT r.display_name repository,r.canonical_remote remote,
		r.aliases_json,r.local_locations_json,COUNT(c.id) conversations,
		MAX(COALESCE(c.ended_at,c.started_at,w.activity_at)) last_message_at,
		MAX(w.indexed_at) last_indexed_at
		FROM repositories r LEFT JOIN workspaces w ON w.repository_id=r.id
		LEFT JOIN conversations c ON c.workspace_id=w.id
		GROUP BY r.id ORDER BY last_message_at DESC,r.display_name`)
	if err != nil {
		return nil, err
	}
	items := []map[string]any{}
	for _, row := range rows {
		var aliases, locations []string
		_ = json.Unmarshal([]byte(firstString(row["aliases_json"])), &aliases)
		_ = json.Unmarshal([]byte(firstString(row["local_locations_json"])), &locations)
		delete(row, "aliases_json")
		delete(row, "local_locations_json")
		if query != "" && !strings.Contains(strings.ToLower(strings.Join(append(append([]string{firstString(row["repository"]), firstString(row["remote"])}, aliases...), locations...), "\n")), query) {
			continue
		}
		if path != "" && !checkedOutAt(locations, path) {
			continue
		}
		items = append(items, row)
	}
	total := len(items)
	result := map[string]any{"total": total}
	if total > limit {
		items = items[:limit]
		result["truncated"] = true
	}
	result["items"] = items
	// Conversations outside any repository never match a repository filter.
	unassigned, err := queryMaps(c.DB, `SELECT COUNT(*) n FROM conversations c JOIN workspaces w ON w.id=c.workspace_id WHERE w.repository_id IS NULL`)
	if err != nil {
		return nil, err
	}
	result["conversations_without_repository"] = unassigned[0]["n"]
	sync, err := queryMaps(c.DB, `SELECT COALESCE(h.label,s.host_id) host,s.source_name source,s.succeeded_at,
		CASE WHEN s.attempted_at>COALESCE(s.succeeded_at,'') THEN s.error END error
		FROM automatic_source_states s LEFT JOIN hosts h ON h.id=s.host_id ORDER BY host,source`)
	if err != nil {
		return nil, err
	}
	result["last_sync"] = sync
	fitConversationItems(result, budget)
	return result, nil
}

// checkedOutAt reports whether path is one of locations or inside one.
func checkedOutAt(locations []string, path string) bool {
	for _, location := range locations {
		location = filepath.Clean(location)
		if path == location || strings.HasPrefix(path, location+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// checkRepositoryFilter reports an error when a repository filter matches no
// repository by name or remote, as the search filters match. The error names
// the repository for a checkout path or a worktree's directory name, which
// agents often pass by mistake, or else points to list_repositories.
func (c *Catalog) checkRepositoryFilter(value string) error {
	matches, err := queryMaps(c.DB, `SELECT 1 FROM repositories WHERE display_name LIKE ?1 OR canonical_remote LIKE ?1 LIMIT 1`, "%"+value+"%")
	if err != nil || len(matches) > 0 {
		return err
	}
	rows, err := queryMaps(c.DB, `SELECT display_name,local_locations_json FROM repositories ORDER BY display_name`)
	if err != nil {
		return err
	}
	lower := strings.ToLower(value)
	path := filepath.Clean(value)
	checkouts := []string{}
	for _, row := range rows {
		var locations []string
		_ = json.Unmarshal([]byte(firstString(row["local_locations_json"])), &locations)
		found := filepath.IsAbs(value) && checkedOutAt(locations, path)
		for _, location := range locations {
			for _, part := range strings.Split(strings.ToLower(filepath.Clean(location)), string(filepath.Separator)) {
				found = found || part == lower
			}
		}
		if found {
			checkouts = append(checkouts, fmt.Sprintf("%q", firstString(row["display_name"])))
		}
	}
	message := fmt.Sprintf("no repository named %q", value)
	// A directory shared by many checkouts, such as ~/src, names none of them.
	if len(checkouts) == 1 {
		return fmt.Errorf("%s: it is a checkout of %s; pass that repository name instead", message, checkouts[0])
	}
	if len(checkouts) > 1 && len(checkouts) <= 3 {
		return fmt.Errorf("%s: it is a checkout of %s; pass one of those repository names instead, or the full checkout path to list_repositories to tell which", message, strings.Join(checkouts, " or "))
	}
	return fmt.Errorf("%s. Pass the repository's name from its Git remote, not a worktree or directory name. list_repositories lists the names Pharos knows, and its path argument finds the repository checked out at a directory", message)
}
