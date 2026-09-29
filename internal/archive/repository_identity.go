package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
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
// <clone>/.candidate-worktrees/<name>, <clone>/.claude/worktrees/<name>) to its
// clone. Any other location is returned as is; nested paths are never
// truncated to an ancestor.
func repositoryCheckout(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	path = filepath.Clean(path)
	parent := filepath.Dir(path)
	switch filepath.Base(parent) {
	case ".conductor", ".task-worktrees", ".candidate-worktrees":
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

// repositoryMatch compares two rows on forge, remote, alias, root-commit, and
// shared-checkout evidence. A remoteless row's locations are handled per group
// by repositoryGrouping.attach.
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
	if repositorySharedCheckout(a, b) {
		return "shared checkout"
	}
	return ""
}

// repositorySharedCheckout recognizes a repository whose remote moved between
// owners or was renamed: two rows with forge remotes that share a root commit
// and a clone on this Mac. One checkout that has had both remotes over time is
// one repository, while a fork lives in a checkout of its own. A remote that
// is a local path (a TL1 scratch origin.git) never counts.
func repositorySharedCheckout(a, b repositoryIdentity) bool {
	return a.Root != "" && a.Root == b.Root && a.Normalized != "" && b.Normalized != "" && repositoryCheckoutsOverlap(a, b)
}

// repositoryCheckoutsOverlap reports whether two rows have a clone in common,
// counting a worktree as its clone.
func repositoryCheckoutsOverlap(a, b repositoryIdentity) bool {
	seen := map[string]bool{}
	for _, path := range a.Locations {
		if checkout := repositoryCheckout(path); checkout != "" {
			seen[checkout] = true
		}
	}
	for _, path := range b.Locations {
		if seen[repositoryCheckout(path)] {
			return true
		}
	}
	return false
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
// groups sharing the path make it ambiguous, unless a checkout on this Mac
// names one of them as its current origin. When a location is a checkout on
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
		if dir := repositoryConductorDir(item, path); dir != "" {
			add(g.byDirectory[dir], "conductor location")
		}
	}
	if len(targets) > 1 {
		if group, ok := g.byOrigin(item, targets); ok {
			targets = map[int]int{group: targets[group]}
			signals = append(signals, "checkout origin")
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
	keys, roots := g.evidence(g.root(target))
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

// evidence is every remote key and root commit of the rows in a group.
func (g *repositoryGrouping) evidence(group int) (keys, roots map[string]bool) {
	keys, roots = map[string]bool{}, map[string]bool{}
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
	return keys, roots
}

// byOrigin breaks a tie between groups that all claim the item's checkout: the
// one group whose remotes include the current origin of a checkout of the item
// that exists on this Mac. Without such an answer the item stays ambiguous.
func (g *repositoryGrouping) byOrigin(item repositoryIdentity, targets map[int]int) (int, bool) {
	winner, found := 0, false
	for _, path := range item.Locations {
		origin := repositoryOriginSlug(path)
		if origin == "" {
			continue
		}
		origin = canonicalRepositorySlug(origin, g.aliases)
		for group := range targets {
			if keys, _ := g.evidence(group); !keys[origin] {
				continue
			}
			if found && winner != group {
				return 0, false
			}
			winner, found = group, true
		}
	}
	return winner, found
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

// repositoryClosest reports whether row a fits the incoming item better than
// row b, when several rows describe it: the row with the item's own remote,
// then the usual survivor order.
func repositoryClosest(a, b, item repositoryIdentity) bool {
	own := func(row repositoryIdentity) bool { return item.Normalized != "" && row.Normalized == item.Normalized }
	if own(a) != own(b) {
		return own(a)
	}
	return repositoryPreferred(a, b, strings.TrimSpace(a.Remote) != "", strings.TrimSpace(b.Remote) != "")
}

// repositoryIdentityKey is what a new row's ID is derived from: its remote, or
// for a remoteless repository its name and anchor.
func repositoryIdentityKey(item repositoryIdentity) string {
	if item.Normalized != "" {
		return item.Normalized
	}
	if remote := strings.TrimSpace(item.Remote); remote != "" {
		return remote
	}
	if anchor := repositoryAnchor(item); anchor != "" {
		return item.Name + "/" + anchor
	}
	return item.Name
}

// repositoryRemotelessSibling is a remoteless row of the same name that has the
// incoming row's checkout (a worktree counts as its clone) or its Conductor
// repository directory. Rows with a remote never qualify: a path shared with
// them is decided by repositoryGrouping.attach, which knows about forks.
func repositoryRemotelessSibling(existing, incoming repositoryIdentity) bool {
	if existing.Remote != "" || incoming.Remote != "" || !strings.EqualFold(existing.Name, incoming.Name) ||
		existing.Root != "" && incoming.Root != "" && existing.Root != incoming.Root {
		return false
	}
	if repositoryCheckoutsOverlap(existing, incoming) {
		return true
	}
	dirs := repositoryConductorDirs(existing)
	for dir := range repositoryConductorDirs(incoming) {
		if dirs[dir] {
			return true
		}
	}
	return false
}

// repositoryConductorDir is the Conductor repository directory of one of a
// row's locations: that of a location which is a Conductor workspace, and of a
// location inside one (a subfolder, a scratch repository) only for a row named
// after the directory, since Pharos names a row without a remote after its
// Conductor directory when Git says nothing else. A scratch repository that
// Git can read is named after itself and stays out.
func repositoryConductorDir(item repositoryIdentity, path string) string {
	if dir := conductorRepositoryDir(path); dir != "" && (conductorWorkspaceDir(path) == dir || strings.EqualFold(item.Name, dir)) {
		return dir
	}
	return ""
}

func repositoryConductorDirs(item repositoryIdentity) map[string]bool {
	dirs := map[string]bool{}
	for _, path := range item.Locations {
		if dir := repositoryConductorDir(item, path); dir != "" {
			dirs[dir] = true
		}
	}
	return dirs
}

// repositoryAnchor names where a remoteless repository lives without naming
// each of its worktrees: its Conductor repository directory, else its clone.
func repositoryAnchor(item repositoryIdentity) string {
	var conductor, clones []string
	for _, path := range item.Locations {
		if dir := conductorWorkspaceDir(path); dir != "" {
			conductor = append(conductor, "conductor/"+dir)
		} else if clone := repositoryCheckout(path); clone != "" {
			clones = append(clones, clone)
		}
	}
	sort.Strings(conductor)
	sort.Strings(clones)
	if len(conductor) > 0 {
		return conductor[0]
	}
	if len(clones) > 0 {
		return clones[0]
	}
	return ""
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
	var loose []int
	for i, item := range items {
		if !repositoryAttachable(item, g.keys[i], separate) {
			continue
		}
		if target, matched := g.attach(item, i); target >= 0 {
			attachments[i] = target
			signals[i] = append(signals[i], matched...)
		} else {
			loose = append(loose, i)
		}
	}
	for i, target := range attachments {
		g.union(target, i)
	}
	// Remoteless rows that no group claims still merge with their remoteless
	// siblings, and with the group those siblings attached to when it is one.
	for a, i := range loose {
		for _, j := range loose[a+1:] {
			if repositoryRemotelessSibling(items[i], items[j]) {
				g.union(i, j)
				signals[i] = append(signals[i], "same name and location")
			}
		}
	}
	// A cluster of loose rows whose siblings attached to two different groups
	// stays apart, so siblings never bridge groups.
	claimed := map[int]map[int]bool{}
	for _, i := range loose {
		for j := range attachments {
			if repositoryRemotelessSibling(items[i], items[j]) {
				if claimed[g.root(i)] == nil {
					claimed[g.root(i)] = map[int]bool{}
				}
				claimed[g.root(i)][g.root(j)] = true
				signals[i] = append(signals[i], "same name and location")
			}
		}
	}
	for cluster, groups := range claimed {
		if len(groups) == 1 {
			for group := range groups {
				g.union(group, cluster)
			}
		}
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

// githubCLI is the GitHub command-line tool. Tests point it at a stub.
var githubCLI = "gh"

// The state of the GitHub lookup: it needs the gh tool, signed in.
const (
	githubReady     = "ready"
	githubMissing   = "missing"
	githubSignedOut = "signed_out"
)

// githubBinary finds the gh tool. An app started from Finder has a minimal
// PATH without Homebrew's directories, so the usual install locations are
// tried too.
func githubBinary() (string, bool) {
	if path, err := exec.LookPath(githubCLI); err == nil {
		return path, true
	}
	if strings.Contains(githubCLI, "/") {
		return "", false
	}
	dirs := []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin", "/usr/bin"}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"))
	}
	for _, dir := range dirs {
		if path, err := exec.LookPath(filepath.Join(dir, githubCLI)); err == nil {
			return path, true
		}
	}
	return "", false
}

// githubAvailability says whether the gh tool is installed and signed in, and
// where it is.
func githubAvailability(ctx context.Context) (string, string) {
	binary, ok := githubBinary()
	if !ok {
		return githubMissing, ""
	}
	authContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if exec.CommandContext(authContext, binary, "auth", "status").Run() != nil {
		return githubSignedOut, binary
	}
	return githubReady, binary
}

// resolveGitHubRepositories asks GitHub, through the gh tool, for the numeric
// ID and current name of every github.com repository that want selects (all,
// when want is nil), which is how a moved or renamed repository is recognized.
// It reports whether the tool could be used; without it the rows are left as
// they are. A cache and a pause keep redirect lookups bounded even for large
// catalogs, and ctx stops it between requests.
func resolveGitHubRepositories(ctx context.Context, items []repositoryIdentity, want func(repositoryIdentity) bool) string {
	status, binary := githubAvailability(ctx)
	if status != githubReady {
		return status
	}
	type answer struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	}
	cache := map[string]answer{}
	for i := range items {
		if ctx.Err() != nil {
			return githubReady
		}
		slug := items[i].Normalized
		if !strings.HasPrefix(slug, "github.com/") || want != nil && !want(items[i]) {
			continue
		}
		if _, ok := cache[slug]; !ok {
			request, stop := context.WithTimeout(ctx, 5*time.Second)
			output, err := exec.CommandContext(request, binary, "api", "repos/"+strings.TrimPrefix(slug, "github.com/")).Output()
			stop()
			var data answer
			if err == nil {
				_ = json.Unmarshal(output, &data)
			}
			cache[slug] = data
			select {
			case <-ctx.Done():
				return githubReady
			case <-time.After(githubPause):
			}
		}
		if data := cache[slug]; data.ID != 0 {
			items[i].Forge = fmt.Sprint(data.ID)
			items[i].ForgeCanonical = "github.com/" + strings.ToLower(data.FullName)
		}
	}
	return githubReady
}

// githubPause separates requests to GitHub.
var githubPause = 200 * time.Millisecond
