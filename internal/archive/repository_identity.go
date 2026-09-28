package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// normalizeRepositoryRemote leaves unknown local paths without a remote key.
func normalizeRepositoryRemote(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, ":") && !strings.Contains(raw, "://") && strings.Contains(raw, "@") {
		at := strings.Index(raw, "@")
		hostPath := raw[at+1:]
		if host, path, ok := strings.Cut(hostPath, ":"); ok {
			raw = host + "/" + path
		}
	} else if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" {
			return ""
		}
		raw = parsed.Hostname() + parsed.Path
	}
	raw = strings.ToLower(strings.TrimRight(raw, "/"))
	raw = strings.TrimSuffix(raw, ".git")
	parts := strings.Split(raw, "/")
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" || !strings.Contains(parts[0], ".") {
		return ""
	}
	return strings.Join(parts[:3], "/")
}

func repositorySlug(raw string) string {
	if slug := normalizeRepositoryRemote(raw); slug != "" {
		return slug
	}
	raw = strings.ToLower(strings.TrimSpace(raw))
	if strings.Count(raw, "/") == 2 && strings.Contains(strings.Split(raw, "/")[0], ".") {
		return strings.TrimSuffix(strings.TrimRight(raw, "/"), ".git")
	}
	return raw
}

func canonicalRepositorySlug(slug string, aliases map[string]string) string {
	slug = repositorySlug(slug)
	for i := 0; i < 10; i++ {
		next := repositorySlug(aliases[slug])
		if next == "" || next == slug {
			break
		}
		slug = next
	}
	return slug
}

type repositoryIdentity struct {
	ID, Remote, Normalized, Name, Owner, Root, Forge, ForgeCanonical string
	Aliases, Locations                                               []string
	Workspaces                                                       int
}

type repositoryOptions struct {
	aliases  map[string]string
	separate []string
}

func repositoryStrings(raw string) []string {
	var values []string
	_ = json.Unmarshal([]byte(raw), &values)
	return values
}

func repositoryUnion(groups ...[]string) []string {
	seen := map[string]bool{}
	var result []string
	for _, group := range groups {
		for _, value := range group {
			value = strings.TrimSpace(value)
			if value != "" && !seen[value] {
				seen[value] = true
				result = append(result, value)
			}
		}
	}
	sort.Strings(result)
	return result
}

func repositoryFromValue(value map[string]any) repositoryIdentity {
	remote := firstString(value["canonical_remote"])
	identity := repositoryIdentity{ID: firstString(value["id"]), Remote: remote, Normalized: firstString(value["normalized_remote"]), Name: firstString(value["display_name"]), Owner: firstString(value["owner"]), Root: firstString(value["root_commit"]), Forge: firstString(value["forge_id"])}
	if identity.Normalized == "" {
		identity.Normalized = normalizeRepositoryRemote(remote)
	}
	for _, key := range []string{"aliases", "local_locations"} {
		var values []string
		switch item := value[key].(type) {
		case []string:
			values = item
		case []any:
			for _, entry := range item {
				if text, ok := entry.(string); ok {
					values = append(values, text)
				}
			}
		}
		if key == "aliases" {
			identity.Aliases = values
		} else {
			identity.Locations = values
		}
	}
	identity.Aliases = repositoryUnion(identity.Aliases, []string{remote})
	if identity.Root == "" {
		for _, location := range identity.Locations {
			if identity.Root = repositoryRootCommit(location); identity.Root != "" {
				break
			}
		}
	}
	return identity
}

type repositoryQuerier interface {
	Query(string, ...any) (*sql.Rows, error)
}

func repositoryColumns(db repositoryQuerier) (map[string]bool, error) {
	rows, err := db.Query("PRAGMA table_info(repositories)")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var number, notNull, primary int
		var name, kind string
		var defaultValue sql.NullString
		if err := rows.Scan(&number, &name, &kind, &notNull, &defaultValue, &primary); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func loadRepositoryIdentities(db repositoryQuerier) ([]repositoryIdentity, error) {
	columns, err := repositoryColumns(db)
	if err != nil {
		return nil, err
	}
	column := func(name string) string {
		if columns[name] {
			return "r." + name
		}
		return "NULL"
	}
	query := fmt.Sprintf(`SELECT r.id,r.canonical_remote,%s,r.display_name,r.owner,%s,%s,r.aliases_json,r.local_locations_json,COUNT(w.id)
		FROM repositories r LEFT JOIN workspaces w ON w.repository_id=r.id GROUP BY r.id`, column("normalized_remote"), column("root_commit"), column("forge_id"))
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []repositoryIdentity
	for rows.Next() {
		var id, aliases, locations string
		var remote, normalized, name, owner, root, forge sql.NullString
		var count int
		if err := rows.Scan(&id, &remote, &normalized, &name, &owner, &root, &forge, &aliases, &locations, &count); err != nil {
			return nil, err
		}
		item := repositoryIdentity{ID: id, Remote: remote.String, Normalized: normalized.String, Name: name.String, Owner: owner.String, Root: root.String, Forge: forge.String, Aliases: repositoryStrings(aliases), Locations: repositoryStrings(locations), Workspaces: count}
		if item.Normalized == "" {
			item.Normalized = normalizeRepositoryRemote(item.Remote)
		}
		item.Aliases = repositoryUnion(item.Aliases, []string{item.Remote})
		result = append(result, item)
	}
	return result, rows.Err()
}

func conductorRepositoryDir(path string) string {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] == "conductor" && parts[i+1] == "workspaces" {
			return parts[i+2]
		}
	}
	return ""
}

func repositoryKeys(item repositoryIdentity, aliases map[string]string) []string {
	keys := []string{}
	for _, raw := range repositoryUnion(item.Aliases, []string{item.Remote, item.Normalized}) {
		if slug := canonicalRepositorySlug(raw, aliases); strings.Contains(slug, "/") {
			keys = append(keys, slug)
		}
	}
	if target := aliases[strings.ToLower(item.Name)]; target != "" {
		if slug := canonicalRepositorySlug(target, aliases); strings.Contains(slug, "/") {
			keys = append(keys, slug)
		}
	}
	return repositoryUnion(keys)
}

func repositoryMatch(a, b repositoryIdentity, aliases map[string]string, directoryOwners map[string]map[string]bool, separate []string) string {
	for _, id := range separate {
		if id == a.ID || id == b.ID {
			return ""
		}
	}
	if a.Forge != "" && a.Forge == b.Forge {
		return "forge_id"
	}
	for _, left := range repositoryKeys(a, aliases) {
		for _, right := range repositoryKeys(b, aliases) {
			if left == right {
				return "remote/alias"
			}
		}
	}
	if a.Root != "" && a.Root == b.Root {
		for _, left := range repositoryKeys(a, aliases) {
			for _, right := range repositoryKeys(b, aliases) {
				lp, rp := strings.Split(left, "/"), strings.Split(right, "/")
				if len(lp) == 3 && len(rp) == 3 && lp[0] == rp[0] && lp[1] == rp[1] {
					return "root+host/owner"
				}
			}
		}
	}
	// A missing-remote Conductor capture can use the directory only when it
	// points to exactly one repository with a remote. Names alone never match.
	if len(repositoryKeys(a, aliases)) == 0 || len(repositoryKeys(b, aliases)) == 0 {
		for _, path := range a.Locations {
			if dir := conductorRepositoryDir(path); dir != "" && len(directoryOwners[dir]) == 1 {
				for _, other := range b.Locations {
					if conductorRepositoryDir(other) == dir {
						return "conductor location"
					}
				}
			}
		}
	}
	return ""
}

func repositoryDirectoryOwners(items []repositoryIdentity, aliases map[string]string) map[string]map[string]bool {
	owners := map[string]map[string]bool{}
	for _, item := range items {
		if len(repositoryKeys(item, aliases)) == 0 {
			continue
		}
		for _, path := range item.Locations {
			if dir := conductorRepositoryDir(path); dir != "" {
				if owners[dir] == nil {
					owners[dir] = map[string]bool{}
				}
				owners[dir][item.ID] = true
			}
		}
	}
	return owners
}

type repositoryMergeGroup struct {
	Survivor repositoryIdentity
	Losers   []repositoryIdentity
	Signals  []string
	Name     string
}

func planRepositoryMerges(items []repositoryIdentity, aliases map[string]string, separate ...string) []repositoryMergeGroup {
	owners := repositoryDirectoryOwners(items, aliases)
	parent := make([]int, len(items))
	for i := range parent {
		parent[i] = i
	}
	var root func(int) int
	root = func(i int) int {
		if parent[i] != i {
			parent[i] = root(parent[i])
		}
		return parent[i]
	}
	signals := map[int][]string{}
	for i := range items {
		for j := i + 1; j < len(items); j++ {
			if signal := repositoryMatch(items[i], items[j], aliases, owners, separate); signal != "" {
				a, b := root(i), root(j)
				if a != b {
					parent[b] = a
				}
				signals[i] = append(signals[i], signal)
			}
		}
	}
	clusters := map[int][]repositoryIdentity{}
	for i, item := range items {
		clusters[root(i)] = append(clusters[root(i)], item)
	}
	var result []repositoryMergeGroup
	for index, cluster := range clusters {
		if len(cluster) < 2 {
			continue
		}
		sort.Slice(cluster, func(i, j int) bool {
			return cluster[i].Workspaces > cluster[j].Workspaces || cluster[i].Workspaces == cluster[j].Workspaces && cluster[i].ID < cluster[j].ID
		})
		// A forge redirect identifies the latest slug, even when the older row has more workspaces.
		canonical := ""
		for _, item := range cluster {
			if item.ForgeCanonical != "" {
				canonical = item.ForgeCanonical
				break
			}
			if target := canonicalRepositorySlug(item.Normalized, aliases); target != item.Normalized {
				canonical = target
			}
		}
		for i, item := range cluster {
			if canonical != "" && item.Normalized == canonical {
				cluster[0], cluster[i] = cluster[i], cluster[0]
				break
			}
		}
		name := cluster[0].Name
		slug := canonical
		if slug == "" {
			slug = canonicalRepositorySlug(cluster[0].Normalized, aliases)
		}
		if strings.Contains(slug, "/") {
			name = filepath.Base(slug)
		}
		seen := map[string]bool{}
		var matched []string
		for i := range items {
			if root(i) == index {
				for _, signal := range signals[i] {
					if !seen[signal] {
						seen[signal] = true
						matched = append(matched, signal)
					}
				}
			}
		}
		sort.Strings(matched)
		result = append(result, repositoryMergeGroup{Survivor: cluster[0], Losers: cluster[1:], Signals: matched, Name: name})
	}
	sort.Slice(result, func(i, j int) bool {
		if len(result[i].Losers) != len(result[j].Losers) {
			return len(result[i].Losers) > len(result[j].Losers)
		}
		return result[i].Name < result[j].Name
	})
	return result
}

// resolveGitHubRepositories runs only from the explicit repository command.
// Its cache and pause keep redirect lookups bounded even for large catalogs.
func resolveGitHubRepositories(ctx context.Context, items []repositoryIdentity) {
	if _, err := exec.LookPath("gh"); err != nil {
		return
	}
	authContext, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if exec.CommandContext(authContext, "gh", "auth", "status").Run() != nil {
		return
	}
	type answer struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	}
	cache := map[string]answer{}
	for i := range items {
		if ctx.Err() != nil {
			return
		}
		slug := items[i].Normalized
		if !strings.HasPrefix(slug, "github.com/") {
			continue
		}
		if _, ok := cache[slug]; !ok {
			request, stop := context.WithTimeout(ctx, 5*time.Second)
			output, err := exec.CommandContext(request, "gh", "api", "repos/"+strings.TrimPrefix(slug, "github.com/")).Output()
			stop()
			var data answer
			if err == nil {
				_ = json.Unmarshal(output, &data)
			}
			cache[slug] = data
			select {
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
		}
		if data := cache[slug]; data.ID != 0 {
			items[i].Forge = fmt.Sprint(data.ID)
			items[i].ForgeCanonical = "github.com/" + strings.ToLower(data.FullName)
		}
	}
}
