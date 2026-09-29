package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// A library upgrade brings the derived data of a catalog indexed by an older
// Pharos up to date: repository identities, token attribution, harness
// versions, loaded instructions, and the tool ledger. Each step reads only the
// catalog (and, for repository identities, GitHub through the gh tool; for
// harness versions and loaded instructions, source transcripts and captures),
// commits in small units, and resumes where it stopped, so the whole upgrade
// is one job that can be interrupted by an eject and started again.
//
// The order matters. Repositories merge first because the tool ledger and
// the loaded-instructions record store the repository-relative path of each
// file, and token attribution and the ledger read only stored messages, so
// they can run on any Mac.

type upgradeStep struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Detail  string `json:"detail"`
	Pending int64  `json:"pending"`
	Unit    string `json:"unit"`
	// Seconds is how long the step took the last time it ran in this process.
	Seconds float64 `json:"seconds,omitempty"`
}

type upgradeState struct {
	mu          sync.Mutex
	running     bool
	step        string
	done, total int
	startedAt   string
	lastError   string
	seconds     map[string]float64
	// github is the last answer of githubAvailability, and when it was asked.
	github   string
	githubAt time.Time
}

type upgradeMergeGroup struct {
	Name       string   `json:"name"`
	Survivor   string   `json:"survivor"`
	Remote     string   `json:"remote"`
	Merged     []string `json:"merged"`
	Workspaces int      `json:"workspaces"`
	Signals    []string `json:"signals"`
}

// libraryUpgrades holds the running upgrade for each open catalog path, so the
// Catalog struct needs no new field.
var libraryUpgrades sync.Map

func (c *Catalog) upgrade() *upgradeState {
	value, _ := libraryUpgrades.LoadOrStore(c.Path, &upgradeState{seconds: map[string]float64{}})
	return value.(*upgradeState)
}

func (c *Catalog) upgradeRunning() bool {
	state := c.upgrade()
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.running
}

// UpgradeSteps reports what an upgrade would do. A library indexed entirely by
// this version has nothing pending.
func (c *Catalog) UpgradeSteps(ctx context.Context) ([]upgradeStep, error) {
	count := func(query string, args ...any) (int64, error) {
		var value int64
		err := c.DB.QueryRowContext(ctx, query, args...).Scan(&value)
		return value, err
	}
	merged, err := c.metaValue(ctx, "repository_merge_version")
	if err != nil {
		return nil, err
	}
	// Until the merge of this version has run, every repository is checked:
	// rows with different names and remotes can still be one repository (a
	// rename found by root commit, an alias, or GitHub), which only the merge
	// plan can tell. An older merge left duplicates that ingest kept creating.
	var repositories int64
	if merged != repositoryMergeVersion {
		if repositories, err = count(`SELECT COUNT(*) FROM repositories`); err != nil {
			return nil, err
		}
	}
	usage, err := count(`SELECT COUNT(*) FROM workspaces w WHERE EXISTS
		(SELECT 1 FROM conversations c WHERE c.workspace_id=w.id AND c.provider='claude')
		AND NOT EXISTS (SELECT 1 FROM usage_attribution_state s WHERE s.workspace_id=w.id AND s.version=?)`, usageAttributionVersion)
	if err != nil {
		return nil, err
	}
	// Some conversations have no version to find (a Codex session whose file
	// is gone), so the step is done once one full backfill pass completes.
	var harness int64
	if passed, err := c.metaValue(ctx, "harness_version_upgrade"); err != nil {
		return nil, err
	} else if passed == "" {
		if harness, err = count(`SELECT COUNT(*) FROM conversations c JOIN workspaces w ON w.id=c.workspace_id
			WHERE c.harness IS NULL OR (w.source_kind IN ('claude','codex') AND c.harness_version_first IS NULL)`); err != nil {
			return nil, err
		}
	}
	instructions, err := c.pendingInstructions(ctx)
	if err != nil {
		return nil, err
	}
	tools, err := count(`SELECT COUNT(*) FROM conversations c LEFT JOIN tool_ledger_state s ON s.conversation_id=c.id
		WHERE s.conversation_id IS NULL OR s.version<>?`, toolLedgerVersion)
	if err != nil {
		return nil, err
	}
	state := c.upgrade()
	state.mu.Lock()
	defer state.mu.Unlock()
	steps := []upgradeStep{
		{ID: "repositories", Label: "Merge repository identities", Unit: "repository",
			Detail: "Merges the rows Pharos holds for one repository: different remote URLs, worktrees, and checkouts without a remote. It asks GitHub, through the gh command-line tool, which repositories were renamed or moved, so their work is counted together.", Pending: repositories},
		{ID: "usage", Label: "Correct token attribution", Unit: "workspace",
			Detail: "Recounts Claude sessions whose sub-agents were counted twice. Uses retained messages only.", Pending: usage},
		{ID: "harness", Label: "Record harness versions", Unit: "conversation",
			Detail: "Notes which Claude Code or Codex version ran each conversation, from retained messages or source files where they still exist.", Pending: harness},
		{ID: "instructions", Label: "Record loaded instructions", Unit: "conversation",
			Detail: "Notes which instruction files (CLAUDE.md, AGENTS.md, memory) and skills each Claude Code or Codex conversation loaded, from source files or captures where they still exist.", Pending: instructions},
		{ID: "tools", Label: "Rebuild the tool ledger", Unit: "conversation",
			Detail: "Re-reads retained messages to store error signatures, test failures, and repository-relative paths. The longest step.", Pending: tools},
	}
	for index := range steps {
		steps[index].Seconds = state.seconds[steps[index].ID]
	}
	return steps, nil
}

// UpgradeStatus is the steps plus any upgrade running in this process.
func (c *Catalog) UpgradeStatus(ctx context.Context) (map[string]any, error) {
	steps, err := c.UpgradeSteps(ctx)
	if err != nil {
		return nil, err
	}
	var pending int64
	for _, step := range steps {
		pending += step.Pending
	}
	renames, err := repositoryRenames(c.DB)
	if err != nil {
		return nil, err
	}
	// Whether GitHub can be asked about renamed repositories matters only
	// while the repository step is pending or has just run.
	github := ""
	for _, step := range steps {
		if step.ID == "repositories" && (step.Pending > 0 || step.Seconds > 0) {
			github = c.githubStatus(ctx)
		}
	}
	state := c.upgrade()
	state.mu.Lock()
	defer state.mu.Unlock()
	return map[string]any{"needed": pending > 0, "steps": steps, "running": state.running, "step": nilIfEmpty(state.step),
		"done": state.done, "total": state.total, "started_at": nilIfEmpty(state.startedAt), "error": nilIfEmpty(state.lastError),
		"repository_renames": renames, "github": nilIfEmpty(github)}, nil
}

// githubStatus is githubReady, githubMissing or githubSignedOut. The answer is
// kept for a minute, so a polling panel doesn't run gh each time.
func (c *Catalog) githubStatus(ctx context.Context) string {
	state := c.upgrade()
	state.mu.Lock()
	status, fresh := state.github, time.Since(state.githubAt) < time.Minute
	state.mu.Unlock()
	if status != "" && fresh {
		return status
	}
	status, _ = githubAvailability(ctx)
	c.rememberGitHubStatus(status)
	return status
}

func (c *Catalog) rememberGitHubStatus(status string) {
	state := c.upgrade()
	state.mu.Lock()
	state.github, state.githubAt = status, time.Now()
	state.mu.Unlock()
}

// repositoryMergeGroups plans the merges. Every github.com repository is
// resolved through the gh tool first; without it the plan still runs on remotes,
// root commits, and checkouts, and renamed repositories may stay separate.
func (c *Catalog) repositoryMergeGroups(ctx context.Context) ([]repositoryIdentity, []repositoryMergeGroup, error) {
	items, err := loadRepositoryIdentities(c.DB)
	if err != nil {
		return nil, nil, err
	}
	prepareRepositoryIdentities(items)
	c.rememberGitHubStatus(resolveGitHubRepositories(ctx, items, nil))
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return items, planRepositoryMerges(items, c.RepositoryAliases, c.RepositorySeparate...), nil
}

// RepositoryMergePreview lists the merges an upgrade would make. It asks
// GitHub (through the gh CLI, when it is installed and signed in) which
// repositories were renamed or moved.
func (c *Catalog) RepositoryMergePreview(ctx context.Context) ([]upgradeMergeGroup, error) {
	_, groups, err := c.repositoryMergeGroups(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]upgradeMergeGroup, 0, len(groups))
	for _, group := range groups {
		view := upgradeMergeGroup{Name: group.Name, Survivor: group.Survivor.ID, Remote: group.Survivor.Remote, Signals: group.Signals, Workspaces: group.Survivor.Workspaces}
		for _, loser := range group.Losers {
			label := loser.Name
			if loser.Remote != "" {
				label = loser.Remote
			}
			view.Merged = append(view.Merged, label)
			view.Workspaces += loser.Workspaces
		}
		out = append(out, view)
	}
	return out, nil
}

// RunUpgrade runs every pending step in order. It returns when ctx is
// cancelled, after the unit of work in progress commits; running it again
// resumes.
func (c *Catalog) RunUpgrade(ctx context.Context, progress func(step string, done, total int)) error {
	state := c.upgrade()
	state.mu.Lock()
	if state.running {
		state.mu.Unlock()
		return fmt.Errorf("an upgrade is already running")
	}
	state.running, state.startedAt, state.lastError = true, now(), ""
	state.mu.Unlock()
	report := func(step string, done, total int) {
		state.mu.Lock()
		state.step, state.done, state.total = step, done, total
		state.mu.Unlock()
		if progress != nil {
			progress(step, done, total)
		}
	}
	finish := func(err error) error {
		state.mu.Lock()
		state.running, state.step = false, ""
		if err != nil && ctx.Err() == nil {
			state.lastError = err.Error()
		}
		state.mu.Unlock()
		return err
	}
	timed := func(id string, work func() error) error {
		started := time.Now()
		report(id, 0, 0)
		err := work()
		state.mu.Lock()
		state.seconds[id] += time.Since(started).Seconds()
		state.mu.Unlock()
		return err
	}
	steps, err := c.UpgradeSteps(ctx)
	if err != nil {
		return finish(err)
	}
	pending := map[string]int64{}
	for _, step := range steps {
		pending[step.ID] = step.Pending
	}
	if pending["repositories"] > 0 {
		if err := timed("repositories", func() error { return c.applyRepositoryMerges(ctx, report) }); err != nil {
			return finish(err)
		}
	}
	if pending["usage"] > 0 {
		if err := timed("usage", func() error {
			_, err := c.RepairUsageAttribution(ctx, func(done, total int) { report("usage", done, total) })
			return err
		}); err != nil {
			return finish(err)
		}
	}
	if pending["harness"] > 0 {
		if err := timed("harness", func() error {
			if err := c.backfillHarnessVersions(); err != nil {
				return err
			}
			// Conductor conversations take the version of the native session
			// they wrap, as an index's reconciliation does.
			if err := c.inheritHarnessAliases(); err != nil {
				return err
			}
			return c.writeTransaction(ctx, "harness-upgrade", func(tx *sql.Tx) error { return setMeta(tx, "harness_version_upgrade", "1") })
		}); err != nil {
			return finish(err)
		}
	}
	if pending["instructions"] > 0 {
		if err := timed("instructions", func() error {
			_, err := c.BackfillInstructions(ctx, func(done, total int) { report("instructions", done, total) })
			return err
		}); err != nil {
			return finish(err)
		}
	}
	if pending["tools"] > 0 {
		if err := timed("tools", func() error {
			_, err := c.BackfillToolLedger(ctx, func(done, total int) { report("tools", done, total) })
			return err
		}); err != nil {
			return finish(err)
		}
	}
	if err := ctx.Err(); err != nil {
		return finish(err)
	}
	// The Tools rollup and the Library view follow the ledger and repository
	// changes on their own; rebuilding the rollup now saves the first page view
	// the wait.
	err = timed("rollup", func() error { return c.ensureToolRollup(ctx) })
	return finish(err)
}

func (c *Catalog) applyRepositoryMerges(ctx context.Context, report func(string, int, int)) error {
	items, groups, err := c.repositoryMergeGroups(ctx)
	if err != nil {
		return err
	}
	if err := c.saveRepositoryEvidence(ctx, items); err != nil {
		return err
	}
	for index, group := range groups {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := c.mergeRepositoryGroup(ctx, group); err != nil {
			return err
		}
		report("repositories", index+1, len(groups))
	}
	return c.writeTransaction(ctx, "repository-merge", func(tx *sql.Tx) error { return setMeta(tx, "repository_merge_version", repositoryMergeVersion) })
}

// recordRepositoryRenames remembers the display names a merge retires, so
// the UI can point saved views that filter on them at the surviving name.
func recordRepositoryRenames(tx *sql.Tx, group repositoryMergeGroup) error {
	renames, err := repositoryRenames(tx)
	if err != nil {
		return err
	}
	for _, item := range append([]repositoryIdentity{group.Survivor}, group.Losers...) {
		if item.Name != "" && item.Name != group.Name {
			renames[item.Name] = group.Name
		}
	}
	// A name retired earlier that now points at a retired name follows it.
	for old, name := range renames {
		if next, ok := renames[name]; ok && next != old {
			renames[old] = next
		}
	}
	value, err := json.Marshal(renames)
	if err != nil {
		return err
	}
	return setMeta(tx, "repository_renames", string(value))
}

func repositoryRenames(db queryer) (map[string]string, error) {
	renames := map[string]string{}
	rows, err := queryMapsContext(context.Background(), db, `SELECT value FROM meta WHERE key='repository_renames'`)
	if err != nil || len(rows) == 0 {
		return renames, err
	}
	_ = json.Unmarshal([]byte(firstString(rows[0]["value"])), &renames)
	return renames, nil
}

func setMeta(db execer, key, value string) error {
	_, err := db.Exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}
