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

// conductorWorkspaceDir is the repository directory of a location that is
// itself a Conductor workspace (conductor/workspaces/<dir>/<workspace>), not a
// path nested inside one, such as a scratch repository under .context.
func conductorWorkspaceDir(path string) string {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
	if n := len(parts); n >= 4 && parts[n-4] == "conductor" && parts[n-3] == "workspaces" && parts[n-2] != "" && parts[n-1] != "" {
		return parts[n-2]
	}
	return ""
}

// repositoryCheckout maps a worktree in a known in-clone layout
// (<clone>/.conductor/<name>, <clone>/.task-worktrees/<name>,
// <clone>/.claude/worktrees/<name>) to its clone. Any other location is
// returned as is; nested paths are never truncated to an ancestor.
func repositoryCheckout(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	path = filepath.Clean(path)
	parent := filepath.Dir(path)
	switch filepath.Base(parent) {
	case ".conductor", ".task-worktrees":
		return filepath.Dir(parent)
	case "worktrees":
		if filepath.Base(filepath.Dir(parent)) == ".claude" {
			return filepath.Dir(filepath.Dir(parent))
		}
	}
	return path
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

func repositorySeparate(item repositoryIdentity, separate []string) bool {
	for _, id := range separate {
		if id == item.ID {
			return true
		}
	}
	return false
}

// repositoryMatch compares two rows on forge, remote, alias, and root-commit
// evidence. Locations are handled per group by repositoryGrouping.attach.
func repositoryMatch(a, b repositoryIdentity, aliases map[string]string, separate []string) string {
	return repositoryMatchKeys(a, b, repositoryKeys(a, aliases), repositoryKeys(b, aliases), separate)
}

func repositoryMatchKeys(a, b repositoryIdentity, aKeys, bKeys []string, separate []string) string {
	if repositorySeparate(a, separate) || repositorySeparate(b, separate) {
		return ""
	}
	if a.Forge != "" && a.Forge == b.Forge {
		return "forge_id"
	}
	for _, left := range aKeys {
		for _, right := range bKeys {
			if left == right {
				return "remote/alias"
			}
		}
	}
	if a.Root != "" && a.Root == b.Root {
		for _, left := range aKeys {
			for _, right := range bKeys {
				lp, rp := strings.Split(left, "/"), strings.Split(right, "/")
				if len(lp) == 3 && len(rp) == 3 && lp[0] == rp[0] && lp[1] == rp[1] {
					return "root+host/owner"
				}
			}
		}
	}
	return ""
}

// repositoryGrouping unions rows on forge/remote/alias/root evidence, then
// indexes the local paths of rows that carry a remote so a remoteless row can
// join the one group its location points to.
type repositoryGrouping struct {
	items    []repositoryIdentity
	keys     [][]string
	parent   []int
	aliases  map[string]string
	separate []string
	// Keyed row indices by clone path and by Conductor repository directory
	// (only locations that are Conductor workspaces themselves).
	byCheckout, byDirectory map[string][]int
}

func newRepositoryGrouping(items []repositoryIdentity, aliases map[string]string, separate []string, signal func(i, j int, signal string)) *repositoryGrouping {
	g := &repositoryGrouping{items: items, keys: make([][]string, len(items)), parent: make([]int, len(items)), aliases: aliases, separate: separate, byCheckout: map[string][]int{}, byDirectory: map[string][]int{}}
	for i, item := range items {
		g.parent[i] = i
		g.keys[i] = repositoryKeys(item, aliases)
	}
	for i := range items {
		for j := i + 1; j < len(items); j++ {
			if matched := repositoryMatchKeys(items[i], items[j], g.keys[i], g.keys[j], separate); matched != "" {
				g.union(i, j)
				if signal != nil {
					signal(i, j, matched)
				}
			}
		}
	}
	for i, item := range items {
		if !g.keyed(i) {
			continue
		}
		for _, path := range item.Locations {
			if checkout := repositoryCheckout(path); checkout != "" {
				g.byCheckout[checkout] = append(g.byCheckout[checkout], i)
			}
			// A repository nested inside a workspace (for example a TL1
			// scratch origin.git under .context) does not claim its directory.
			if dir := conductorWorkspaceDir(path); dir != "" {
				g.byDirectory[dir] = append(g.byDirectory[dir], i)
			}
		}
	}
	return g
}

func (g *repositoryGrouping) root(i int) int {
	if g.parent[i] != i {
		g.parent[i] = g.root(g.parent[i])
	}
	return g.parent[i]
}

func (g *repositoryGrouping) union(i, j int) {
	if a, b := g.root(i), g.root(j); a != b {
		g.parent[b] = a
	}
}

func (g *repositoryGrouping) keyed(i int) bool {
	return len(g.keys[i]) > 0 || g.items[i].Forge != ""
}

// repositoryAttachable is a row with no remote at all. A row whose remote is a
// local path (a TL1 scratch origin.git) has no forge key but is never attached.
func repositoryAttachable(item repositoryIdentity, keys []string, separate []string) bool {
	return strings.TrimSpace(item.Remote) == "" && item.Normalized == "" && item.Forge == "" && len(keys) == 0 && !repositorySeparate(item, separate)
}

// attach returns a keyed row whose group the remoteless item joins, or -1. The
// item's clone path, or the Conductor directory of a location that is itself a
// Conductor workspace, must point to exactly one group. Rows in different
// groups sharing the path make it ambiguous. When a location is a checkout on
// this Mac, its origin and root commit must agree with the group.
func (g *repositoryGrouping) attach(item repositoryIdentity, self int) (int, []string) {
	targets := map[int]int{}
	var signals []string
	add := func(indices []int, signal string) {
		for _, j := range indices {
			if j == self {
				continue
			}
			if _, ok := targets[g.root(j)]; !ok {
				targets[g.root(j)] = j
			}
			if !containsString(signals, signal) {
				signals = append(signals, signal)
			}
		}
	}
	for _, path := range item.Locations {
		add(g.byCheckout[repositoryCheckout(path)], "local checkout")
		if dir := conductorWorkspaceDir(path); dir != "" {
			add(g.byDirectory[dir], "conductor location")
		}
	}
	if len(targets) != 1 {
		return -1, nil
	}
	target := -1
	for _, j := range targets {
		target = j
	}
	if repositorySeparate(g.items[target], g.separate) {
		return -1, nil
	}
	group := g.root(target)
	keys, roots := map[string]bool{}, map[string]bool{}
	for i := range g.items {
		if g.root(i) != group {
			continue
		}
		for _, key := range g.keys[i] {
			keys[key] = true
		}
		if canonical := g.items[i].ForgeCanonical; canonical != "" {
			keys[canonicalRepositorySlug(canonical, g.aliases)] = true
		}
		if g.items[i].Root != "" {
			roots[g.items[i].Root] = true
		}
	}
	if item.Root != "" && len(roots) > 0 && !roots[item.Root] {
		return -1, nil
	}
	for _, path := range item.Locations {
		if origin := repositoryOriginSlug(path); origin != "" && !keys[canonicalRepositorySlug(origin, g.aliases)] {
			return -1, nil
		}
	}
	return target, signals
}

// repositoryPreferred orders rows for a group's survivor: rows with a remote
// first, then the most workspaces, then ID.
func repositoryPreferred(a, b repositoryIdentity, aKeyed, bKeyed bool) bool {
	if aKeyed != bKeyed {
		return aKeyed
	}
	return a.Workspaces > b.Workspaces || a.Workspaces == b.Workspaces && a.ID < b.ID
}

func containsString(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

// Ingest may update a row marked separate when the incoming record describes
// that same remote. Separation only prevents linking it to a different row.
func repositorySameIdentity(existing, incoming repositoryIdentity, aliases map[string]string, separate []string) bool {
	if incoming.Normalized != "" {
		for _, raw := range repositoryUnion(existing.Aliases, []string{existing.Remote, existing.Normalized}) {
			if normalizeRepositoryRemote(raw) == incoming.Normalized {
				return true
			}
		}
	}
	return repositoryMatch(existing, incoming, aliases, separate) != ""
}

// repositoryRemotelessSibling links a remoteless capture to a remoteless row of
// the same name when its Conductor directory belongs to that row alone.
func repositoryRemotelessSibling(existing, incoming repositoryIdentity, items []repositoryIdentity) bool {
	if existing.Remote == "" && incoming.Remote == "" && strings.EqualFold(existing.Name, incoming.Name) &&
		(existing.Root == "" || incoming.Root == "" || existing.Root == incoming.Root) {
		for _, path := range incoming.Locations {
			dir := conductorRepositoryDir(path)
			if dir == "" {
				continue
			}
			for _, old := range existing.Locations {
				if conductorRepositoryDir(old) != dir {
					continue
				}
				matches := 0
				for _, candidate := range items {
					for _, location := range candidate.Locations {
						if conductorRepositoryDir(location) == dir {
							matches++
							break
						}
					}
				}
				if matches == 1 {
					return true
				}
			}
		}
	}
	return false
}

type repositoryMergeGroup struct {
	Survivor repositoryIdentity
	Losers   []repositoryIdentity
	Signals  []string
	Name     string
}

func planRepositoryMerges(items []repositoryIdentity, aliases map[string]string, separate ...string) []repositoryMergeGroup {
	signals := map[int][]string{}
	g := newRepositoryGrouping(items, aliases, separate, func(i, _ int, signal string) {
		signals[i] = append(signals[i], signal)
	})
	// Remoteless rows join the groups formed by remote evidence. Attachments
	// are decided against those groups only, never chained through one another.
	attachments := map[int]int{}
	for i, item := range items {
		if !repositoryAttachable(item, g.keys[i], separate) {
			continue
		}
		if target, matched := g.attach(item, i); target >= 0 {
			attachments[i] = target
			signals[i] = append(signals[i], matched...)
		}
	}
	for i, target := range attachments {
		g.union(target, i)
	}
	clusters := map[int][]int{}
	for i := range items {
		clusters[g.root(i)] = append(clusters[g.root(i)], i)
	}
	var result []repositoryMergeGroup
	for _, members := range clusters {
		if len(members) < 2 {
			continue
		}
		// Rows with a remote come first, so a larger remoteless row never
		// becomes the survivor or names the group.
		sort.Slice(members, func(i, j int) bool {
			a, b := members[i], members[j]
			return repositoryPreferred(items[a], items[b], g.keyed(a), g.keyed(b))
		})
		cluster := make([]repositoryIdentity, len(members))
		for i, index := range members {
			cluster[i] = items[index]
		}
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
		var matched []string
		for _, index := range members {
			for _, signal := range signals[index] {
				if !containsString(matched, signal) {
					matched = append(matched, signal)
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
