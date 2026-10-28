package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The initial Git ancestry scan can take minutes on a large catalog. Keep it
// out of OpenCatalog so the local API can become ready before it runs.
func (c *Catalog) backfillMainIntegrations(ctx context.Context) error {
	var completed string
	if err := c.DB.QueryRow("SELECT value FROM meta WHERE key='git_main_backfill'").Scan(&completed); err == nil {
		return nil
	} else if err != sql.ErrNoRows {
		return err
	}
	if err := c.refreshMainIntegrations(ctx); err != nil {
		return err
	}
	_, err := c.DB.Exec("INSERT OR REPLACE INTO meta(key,value) VALUES('git_main_backfill','1')")
	return err
}

var gitCommitID = regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`)

// mainIntegration uses only local Git objects. A squash merge cannot be
// established by commit ancestry, so it is deliberately left unclassified.
func mainIntegration(repository, head, remote string) map[string]any {
	if repository == "" || !gitCommitID.MatchString(head) {
		return nil
	}
	tip, err := integrationTip(context.Background(), repository)
	if err != nil {
		return nil
	}
	integration, _ := mainIntegrationAtTip(repository, head, remote, tip)
	return integration
}

func gitOutput(repository string, args ...string) (string, error) {
	return gitOutputContext(context.Background(), repository, args...)
}

func gitOutputContext(parent context.Context, repository string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "git", append([]string{"-C", repository}, args...)...)
	command.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1")
	output, err := command.Output()
	return strings.TrimSpace(string(output)), err
}

type integrationResult struct {
	value   map[string]any
	checked bool
}

// One repository's immutable tip and first-parent history are shared by all
// workspace heads in a refresh. Failed commands never become negative evidence.
type integrationRepository struct {
	ctx               context.Context
	path, common, tip string
	shallow           bool
	commits           []string
	positions         map[string]int
	results           map[string]integrationResult
	heads             map[string]bool
	available         map[string]bool
	failed            bool
}

func (r *integrationRepository) lookup(head, remote string) (map[string]any, bool) {
	head = strings.ToLower(head)
	if !gitCommitID.MatchString(head) || !gitCommitID.MatchString(r.tip) {
		return nil, false
	}
	if result, ok := r.results[head]; ok {
		return integrationURL(result.value, remote), result.checked
	}
	if r.failed {
		return nil, false
	}
	if r.heads == nil {
		r.heads = map[string]bool{head: true}
	}
	if r.available == nil {
		if err := r.readObjects(); err != nil {
			return nil, false
		}
	}
	if !r.available[head] {
		return nil, false
	}
	value, checked := r.resolve(head)
	if r.results == nil {
		r.results = map[string]integrationResult{}
	}
	r.results[head] = integrationResult{value, checked}
	return integrationURL(value, remote), checked
}

// On a fast-forward, a previously absent head can only become reachable if
// it is in the newly reachable commit set. One graph delta replaces an
// ancestry subprocess for every historical workspace head.
func (r *integrationRepository) reuseNegatives(rows []map[string]any) {
	if r.shallow || r.tip == "" {
		return
	}
	byTip := map[string][]string{}
	for _, row := range rows {
		if firstString(row["location"]) != "common-v2:"+r.common {
			continue
		}
		head, tip := strings.ToLower(firstString(row["head_ref"])), firstString(row["main_tip"])
		if r.heads[head] && gitCommitID.MatchString(head) && gitCommitID.MatchString(tip) {
			byTip[tip] = append(byTip[tip], head)
		}
	}
	if r.results == nil {
		r.results = map[string]integrationResult{}
	}
	for tip, heads := range byTip {
		newly := map[string]bool{}
		if tip != r.tip {
			if _, err := gitOutputContext(r.ctx, r.path, "merge-base", "--is-ancestor", tip, r.tip); err != nil {
				continue
			}
			delta, err := gitOutputContext(r.ctx, r.path, "rev-list", r.tip, "^"+tip)
			if err != nil {
				continue
			}
			for _, commit := range strings.Fields(delta) {
				newly[commit] = true
			}
		}
		for _, head := range heads {
			if !newly[head] {
				r.results[head] = integrationResult{checked: true}
			}
		}
	}
}

func (r *integrationRepository) readObjects() error {
	// Remember a failed inventory for this refresh too; another head must not
	// retry the same failing command. No negative result is persisted.
	r.available = map[string]bool{}
	heads := make([]string, 0, len(r.heads)+1)
	for head := range r.heads {
		if gitCommitID.MatchString(head) {
			heads = append(heads, head)
		}
	}
	// Standalone callers have only one head; lookup inserts it before this call.
	sort.Strings(heads)
	ctx, cancel := context.WithTimeout(r.ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "git", "-C", r.path, "cat-file", "--batch-check=%(objectname) %(objecttype)")
	command.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1")
	command.Stdin = strings.NewReader(strings.Join(heads, "\n") + "\n")
	output, err := command.Output()
	if err != nil {
		return err
	}
	r.available = map[string]bool{}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == "commit" {
			r.available[fields[0]] = true
		}
	}
	return nil
}

func integrationURL(value map[string]any, remote string) map[string]any {
	if value == nil {
		return nil
	}
	result := map[string]any{}
	for key, v := range value {
		result[key] = v
	}
	if slug := githubSlug(remote); slug != "" {
		result["main_merge_url"] = "https://github.com/" + slug + "/commit/" + firstString(value["main_merge_commit"])
	}
	return result
}

func (r *integrationRepository) resolve(head string) (map[string]any, bool) {
	if _, err := gitOutputContext(r.ctx, r.path, "merge-base", "--is-ancestor", head, r.tip); err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return nil, !r.shallow
		}
		r.failed = true
		return nil, false
	}
	if r.commits == nil {
		lineage, err := gitOutputContext(r.ctx, r.path, "rev-list", "--first-parent", "--reverse", r.tip)
		if err != nil || lineage == "" {
			r.failed = true
			return nil, false
		}
		r.commits, r.positions = strings.Split(lineage, "\n"), map[string]int{}
		for i, commit := range r.commits {
			r.positions[commit] = i
		}
	}
	position, direct := r.positions[head]
	if !direct {
		low, high := 0, len(r.commits)-1
		for low < high {
			middle := low + (high-low)/2
			_, err := gitOutputContext(r.ctx, r.path, "merge-base", "--is-ancestor", head, r.commits[middle])
			if err == nil {
				high = middle
			} else if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
				low = middle + 1
			} else {
				r.failed = true
				return nil, false
			}
		}
		position = low
		if position == 0 && r.shallow {
			return nil, false
		}
	}
	commit := r.commits[position]
	details, err := gitOutputContext(r.ctx, r.path, "show", "-s", "--format=%P%n%s", commit)
	if err != nil {
		r.failed = true
		return nil, false
	}
	parents, title, ok := strings.Cut(details, "\n")
	// TrimSpace removes the empty parent line of a root commit.
	if !ok {
		parents, title = "", details
	}
	method := "direct"
	if len(strings.Fields(parents)) > 1 {
		method = "merge"
	}
	return map[string]any{"main_merge_commit": commit, "main_merge_title": title, "main_merge_method": method}, true
}

// checked says Git determined ancestry conclusively. Shallow and unavailable
// objects cannot establish a durable negative result.
func mainIntegrationAtTip(repository, head, remote, tip string) (map[string]any, bool) {
	common, err := integrationCommonDir(repository)
	if err != nil {
		return nil, false
	}
	r := integrationRepository{ctx: context.Background(), path: repository, common: common, tip: tip, shallow: integrationShallow(common)}
	return r.lookup(head, remote)
}

// Resolve worktree indirection with filesystem reads rather than launching a
// process for each of the thousands of historical workspace paths.
func integrationCommonDir(location string) (string, error) {
	location, err := filepath.Abs(location)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(location); err != nil || !info.IsDir() {
		return "", fmt.Errorf("repository unavailable: %s", location)
	}
	for dir := location; ; dir = filepath.Dir(dir) {
		gitdir := filepath.Join(dir, ".git")
		if info, err := os.Stat(gitdir); err == nil {
			if !info.IsDir() {
				data, err := os.ReadFile(gitdir)
				if err != nil {
					return "", err
				}
				target, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
				if !ok {
					return "", fmt.Errorf("invalid Git directory: %s", gitdir)
				}
				gitdir = target
				if !filepath.IsAbs(gitdir) {
					gitdir = filepath.Join(dir, gitdir)
				}
			}
			if data, err := os.ReadFile(filepath.Join(gitdir, "commondir")); err == nil {
				common := strings.TrimSpace(string(data))
				if !filepath.IsAbs(common) {
					common = filepath.Join(gitdir, common)
				}
				gitdir = common
			} else if !os.IsNotExist(err) {
				return "", err
			}
			return filepath.EvalSymlinks(gitdir)
		}
		if filepath.Dir(dir) == dir {
			return "", fmt.Errorf("not a Git checkout: %s", location)
		}
	}
}

func integrationShallow(common string) bool {
	info, err := os.Stat(filepath.Join(common, "shallow"))
	return err != nil && !os.IsNotExist(err) || err == nil && info.Size() > 0
}

func integrationTip(ctx context.Context, location string) (string, error) {
	refs, err := gitOutputContext(ctx, location, "for-each-ref", "--format=%(refname) %(objectname) %(symref)", "refs/remotes/origin/HEAD", "refs/remotes/origin/main", "refs/remotes/origin/master")
	if err != nil {
		return "", err
	}
	tips, target := map[string]string{}, ""
	for _, line := range strings.Split(refs, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		tips[fields[0]] = fields[1]
		if fields[0] == "refs/remotes/origin/HEAD" {
			target = fields[0]
		}
	}
	for _, ref := range []string{target, "refs/remotes/origin/main", "refs/remotes/origin/master"} {
		if tip := tips[ref]; gitCommitID.MatchString(tip) {
			return tip, nil
		}
	}
	return "", nil
}

// refreshMainIntegrations only runs Git in paths this host recorded. Another
// Mac's path may not exist here, or may be an unrelated checkout: Conductor
// reuses workspace directory names.
func (c *Catalog) refreshMainIntegrations(ctx context.Context) error {
	return c.completeStage(ctx, "git", func() error { return c.lookUpMainIntegrations(ctx) })
}

func (c *Catalog) lookUpMainIntegrations(ctx context.Context) error {
	c.gitMainMu.Lock()
	defer c.gitMainMu.Unlock()
	rows, err := queryMapsContext(ctx, c.DB, `SELECT w.id,w.head_ref,r.canonical_remote,s.location,s.repository_locations_json
		FROM workspaces w JOIN workspace_sightings s ON s.workspace_id=w.id AND s.host_id=?
		LEFT JOIN repositories r ON r.id=w.repository_id
		WHERE w.head_ref IS NOT NULL AND w.main_merge_commit IS NULL ORDER BY w.id,s.source_name`, currentHost().ID)
	if err != nil {
		return err
	}
	negativeRows, err := queryMapsContext(ctx, c.DB, `SELECT workspace_id,location,head_ref,main_tip FROM git_main_negative_lookups WHERE host_id=?`, currentHost().ID)
	if err != nil {
		return err
	}
	negative := make(map[string]map[string]any, len(negativeRows))
	for _, row := range negativeRows {
		negative[firstString(row["workspace_id"])+"\x00"+firstString(row["location"])] = row
	}
	// Consolidate sightings before planning so one workspace is handled once.
	grouped := []map[string]any{}
	for _, row := range rows {
		var paths []string
		_ = json.Unmarshal([]byte(firstString(row["repository_locations_json"])), &paths)
		paths = append(paths, firstString(row["location"]))
		if len(grouped) > 0 && grouped[len(grouped)-1]["id"] == row["id"] {
			previous := grouped[len(grouped)-1]
			paths = append(previous["locations"].([]string), paths...)
			previous["locations"] = uniqueStrings(paths)
		} else {
			row["locations"] = uniqueStrings(paths)
			grouped = append(grouped, row)
		}
	}
	rows = grouped
	type gitWrite struct {
		workspace, location, head, tip string
		integration                    map[string]any
	}
	writes := []gitWrite{}
	// Group worktrees by their shared object store before asking Git anything.
	repositories := map[string]*integrationRepository{}
	locations := map[string]*integrationRepository{}
	// Resolve all locations first, so shared repositories batch all candidate
	// heads (including missing objects) before doing any ancestry work.
	reportWork(ctx, "checking Git repositories", "workspace", 0, len(rows))
	for rowIndex, row := range rows {
		for _, location := range row["locations"].([]string) {
			if err := ctx.Err(); err != nil {
				return err
			}
			r, seen := locations[location]
			if !seen {
				locations[location] = nil
				common, err := integrationCommonDir(location)
				if err != nil {
					continue
				}
				var probed bool
				r, probed = repositories[common]
				if !probed {
					repositories[common] = nil
					tip, err := integrationTip(ctx, location)
					if err != nil {
						continue
					}
					r = &integrationRepository{ctx: ctx, path: location, common: common, tip: tip, shallow: integrationShallow(common), heads: map[string]bool{}}
					repositories[common] = r
				}
				locations[location] = r
			}
			if r != nil {
				r.heads[strings.ToLower(firstString(row["head_ref"]))] = true
			}
		}
		reportWork(ctx, "checking Git repositories", "workspace", rowIndex+1, len(rows))
	}
	reportWork(ctx, "checking Git history", "workspace", 0, len(rows))
	for _, repository := range repositories {
		if repository != nil {
			repository.reuseNegatives(negativeRows)
		}
	}
	for rowIndex, row := range rows {

		for _, location := range row["locations"].([]string) {
			if err := ctx.Err(); err != nil {
				return err
			}
			r := locations[location]
			if r == nil || r.tip == "" {
				continue
			}
			head := firstString(row["head_ref"])
			// The versioned common-directory key also avoids reusing legacy shallow
			// negatives, and lets worktrees share durable negative evidence.
			cacheLocation := "common-v2:" + r.common
			if cached := negative[firstString(row["id"])+"\x00"+cacheLocation]; !r.shallow && cached != nil && firstString(cached["head_ref"]) == head && firstString(cached["main_tip"]) == r.tip {
				continue
			}
			integration, checked := r.lookup(head, firstString(row["canonical_remote"]))
			if integration == nil {
				if checked {
					writes = append(writes, gitWrite{workspace: firstString(row["id"]), location: cacheLocation, head: head, tip: r.tip})
					negative[firstString(row["id"])+"\x00"+cacheLocation] = map[string]any{"head_ref": head, "main_tip": r.tip}
				}
				continue
			}
			writes = append(writes, gitWrite{workspace: firstString(row["id"]), integration: integration})
			break
		}
		reportWork(ctx, "checking Git history", "workspace", rowIndex+1, len(rows))
	}

	reportWork(ctx, "saving Git results", "workspace", 0, len(writes))
	for start := 0; start < len(writes); start += 250 {
		stop := min(start+250, len(writes))
		if err := c.writeTransaction(ctx, "git integration results", func(tx *sql.Tx) error {
			for _, write := range writes[start:stop] {
				if write.integration == nil {
					if _, err := tx.ExecContext(ctx, `INSERT INTO git_main_negative_lookups(workspace_id,host_id,location,head_ref,main_tip,checked_at)
      VALUES(?,?,?,?,?,?) ON CONFLICT(workspace_id,host_id,location) DO UPDATE SET head_ref=excluded.head_ref,main_tip=excluded.main_tip,checked_at=excluded.checked_at`, write.workspace, currentHost().ID, write.location, write.head, write.tip, now()); err != nil {
						return err
					}
				} else {
					value := write.integration
					if _, err := tx.ExecContext(ctx, `UPDATE workspaces SET main_merge_commit=?,main_merge_title=?,main_merge_url=?,main_merge_method=? WHERE id=?`, value["main_merge_commit"], value["main_merge_title"], value["main_merge_url"], value["main_merge_method"], write.workspace); err != nil {
						return err
					}
				}
			}
			return nil
		}); err != nil {
			return err
		}
		reportWork(ctx, "saving Git results", "workspace", stop, len(writes))
	}

	return nil
}
