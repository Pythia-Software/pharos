package archive

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// repositoryMergeVersion is the version of the repository merge. A library
// whose stored repository_merge_version differs is offered the merge again:
// version 1 left the duplicate rows that ingest kept creating for repositories
// without a remote or whose remote had moved.
const repositoryMergeVersion = "2"

func prepareRepositoryIdentities(items []repositoryIdentity) {
	for i := range items {
		if items[i].Root == "" {
			for _, location := range items[i].Locations {
				if root := repositoryRootCommit(location); root != "" {
					items[i].Root = root
					break
				}
			}
		}
	}
}

func printRepositoryMergePlan(output io.Writer, groups []repositoryMergeGroup) {
	fmt.Fprintf(output, "%d merge groups\n", len(groups))
	for _, group := range groups {
		fmt.Fprintf(output, "%s: %d rows -> %s (%d workspaces); signals: %s\n", group.Name, len(group.Losers)+1, group.Survivor.ID, group.Survivor.Workspaces, strings.Join(group.Signals, ", "))
		for _, loser := range group.Losers {
			fmt.Fprintf(output, "  %s  %s  %d workspaces\n", loser.ID, loser.Remote, loser.Workspaces)
		}
	}
}

// mergeRepositoryGroup folds one group in a single write transaction. It
// reads before it writes, so the transaction must hold the write lock from its
// start (see beginWrite): upgrading a read transaction fails at once with
// SQLITE_BUSY while any other connection is writing.
func (c *Catalog) mergeRepositoryGroup(ctx context.Context, group repositoryMergeGroup) error {
	return c.writeTransaction(ctx, "repository-merge "+group.Name, func(tx *sql.Tx) error { return mergeRepositoryGroupTx(tx, group) })
}

func mergeRepositoryGroupTx(tx *sql.Tx, group repositoryMergeGroup) error {
	all := append([]repositoryIdentity{group.Survivor}, group.Losers...)
	aliases, locations := []string{}, []string{}
	for _, item := range all {
		aliases = repositoryUnion(aliases, item.Aliases, []string{item.Remote})
		locations = repositoryUnion(locations, item.Locations)
		// Ingest may have added to the row since the plan was made.
		var storedAliases, storedLocations sql.NullString
		err := tx.QueryRow(`SELECT aliases_json,local_locations_json FROM repositories WHERE id=?`, item.ID).Scan(&storedAliases, &storedLocations)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		aliases = repositoryUnion(aliases, repositoryStrings(storedAliases.String))
		locations = repositoryUnion(locations, repositoryStrings(storedLocations.String))
	}
	for _, loser := range group.Losers {
		// Repoint PR links before removing a duplicate host/repository/number.
		rows, err := tx.Query(`SELECT p.id,p.host,p.number FROM pull_requests p WHERE p.repository_id=?`, loser.ID)
		if err != nil {
			return err
		}
		type pr struct {
			id, host string
			number   int
		}
		var prs []pr
		for rows.Next() {
			var entry pr
			if err := rows.Scan(&entry.id, &entry.host, &entry.number); err != nil {
				rows.Close()
				return err
			}
			prs = append(prs, entry)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, entry := range prs {
			var winner string
			err := tx.QueryRow(`SELECT id FROM pull_requests WHERE host=? AND repository_id=? AND number=?`, entry.host, group.Survivor.ID, entry.number).Scan(&winner)
			if err != nil && err != sql.ErrNoRows {
				return err
			}
			if winner == "" {
				id := stableID("pr", entry.host, group.Survivor.ID, entry.number)
				if err := movePullRequest(tx, entry.id, group.Survivor.ID, id); err != nil {
					return err
				}
				continue
			}
			// Keep the row with more populated descriptive fields.
			var oldScore, newScore int
			for _, pair := range []struct {
				id    string
				score *int
			}{{entry.id, &oldScore}, {winner, &newScore}} {
				if err := tx.QueryRow(`SELECT (url IS NOT NULL)+(title IS NOT NULL)+(state IS NOT NULL)+(base_ref IS NOT NULL)+(head_ref IS NOT NULL)+(commit_refs_json IS NOT NULL AND commit_refs_json<>'[]') FROM pull_requests WHERE id=?`, pair.id).Scan(pair.score); err != nil {
					return err
				}
			}
			if oldScore > newScore {
				if _, err := tx.Exec(`UPDATE pull_requests SET url=(SELECT url FROM pull_requests WHERE id=?),title=(SELECT title FROM pull_requests WHERE id=?),state=(SELECT state FROM pull_requests WHERE id=?),base_ref=(SELECT base_ref FROM pull_requests WHERE id=?),head_ref=(SELECT head_ref FROM pull_requests WHERE id=?),commit_refs_json=(SELECT commit_refs_json FROM pull_requests WHERE id=?),observed_at=(SELECT observed_at FROM pull_requests WHERE id=?) WHERE id=?`, entry.id, entry.id, entry.id, entry.id, entry.id, entry.id, entry.id, winner); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(`INSERT OR IGNORE INTO work_pr_links(workspace_id,pr_id,relationship,confidence,evidence_json) SELECT workspace_id,?,relationship,confidence,evidence_json FROM work_pr_links WHERE pr_id=?`, winner, entry.id); err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM pull_requests WHERE id=?`, entry.id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`UPDATE workspaces SET repository_id=? WHERE repository_id=?`, group.Survivor.ID, loser.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO protections(scope_type,scope_id,mode,until_at,reason,created_at)
			SELECT scope_type,?,mode,until_at,reason,created_at FROM protections WHERE scope_type='repository' AND scope_id=? AND 1
			ON CONFLICT(scope_type,scope_id,mode) DO UPDATE SET until_at=CASE WHEN protections.until_at IS NULL OR excluded.until_at IS NULL THEN NULL ELSE MAX(protections.until_at,excluded.until_at) END,
			reason=COALESCE(protections.reason,excluded.reason)`, group.Survivor.ID, loser.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM protections WHERE scope_type='repository' AND scope_id=?`, loser.ID); err != nil {
			return err
		}
		// The tool ledger names the repository each file belongs to.
		if _, err := tx.Exec(`UPDATE tool_calls SET path_repository_id=?,path_repository=? WHERE path_repository_id=?`, group.Survivor.ID, group.Name, loser.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM repositories WHERE id=?`, loser.ID); err != nil {
			return err
		}
		// Findings scoped to the retired row follow the survivor (see storeFindings).
		if _, err := tx.Exec(`UPDATE repository_retirements SET new_id=? WHERE new_id=?`, group.Survivor.ID, loser.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO repository_retirements(old_id,new_id,retired_at) VALUES(?,?,?)
			ON CONFLICT(old_id) DO UPDATE SET new_id=excluded.new_id,retired_at=excluded.retired_at`, loser.ID, group.Survivor.ID, now()); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE tool_calls SET path_repository=? WHERE path_repository_id=? AND path_repository IS NOT ?`, group.Name, group.Survivor.ID, group.Name); err != nil {
		return err
	}
	if err := recordRepositoryRenames(tx, group); err != nil {
		return err
	}
	// A pre-existing PR on the survivor may also have an older ID. Ingest
	// derives IDs from the current repository ID, so normalize every PR here.
	rows, err := tx.Query(`SELECT id,host,number FROM pull_requests WHERE repository_id=?`, group.Survivor.ID)
	if err != nil {
		return err
	}
	type pullRequestID struct {
		id, host string
		number   int
	}
	var stored []pullRequestID
	for rows.Next() {
		var entry pullRequestID
		if err := rows.Scan(&entry.id, &entry.host, &entry.number); err != nil {
			rows.Close()
			return err
		}
		stored = append(stored, entry)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, entry := range stored {
		id := stableID("pr", entry.host, group.Survivor.ID, entry.number)
		if entry.id != id {
			if err := movePullRequest(tx, entry.id, group.Survivor.ID, id); err != nil {
				return err
			}
		}
	}
	root, forge := group.Survivor.Root, group.Survivor.Forge
	for _, item := range all {
		if root == "" {
			root = item.Root
		}
		if forge == "" {
			forge = item.Forge
		}
	}
	remote := group.Survivor.Remote
	if group.Survivor.ForgeCanonical != "" {
		for _, item := range all {
			if item.Normalized == group.Survivor.ForgeCanonical {
				remote = item.Remote
				break
			}
		}
	}
	if _, err := tx.Exec(`UPDATE repositories SET canonical_remote=?,normalized_remote=?,display_name=?,root_commit=?,forge_id=?,aliases_json=?,local_locations_json=?,updated_at=? WHERE id=?`, nilIfEmpty(remote), nilIfEmpty(normalizeRepositoryRemote(remote)), group.Name, nilIfEmpty(root), nilIfEmpty(forge), jsonText(aliases), jsonText(locations), now(), group.Survivor.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO workspace_library_dirty(workspace_id) SELECT id FROM workspaces WHERE repository_id=?`, group.Survivor.ID); err != nil {
		return err
	}
	// Fewer repositories may now have checkouts in a Conductor repository
	// directory, so the tool calls in those directories may resolve anew.
	directories := []string{}
	for _, location := range locations {
		if directory, _ := conductorCheckout(location); directory != "" {
			location = directory
		}
		directories = append(directories, filepath.Clean(expandHome(location)))
	}
	if err := reresolveToolPaths(tx, directories); err != nil {
		return err
	}
	_, err = tx.Exec(`DELETE FROM meta WHERE key='tool_rollup_generation'`)
	return err
}

func movePullRequest(tx *sql.Tx, oldID, repositoryID, newID string) error {
	if oldID == newID {
		_, err := tx.Exec(`UPDATE pull_requests SET repository_id=? WHERE id=?`, repositoryID, oldID)
		return err
	}
	// Temporarily free the host/repository/number key before inserting the
	// canonical ID. Links stay on the old row until the new row exists.
	if _, err := tx.Exec(`UPDATE pull_requests SET repository_id=NULL WHERE id=?`, oldID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO pull_requests(id,host,repository_id,number,url,title,state,base_ref,head_ref,commit_refs_json,observed_at)
		SELECT ?,host,?,number,url,title,state,base_ref,head_ref,commit_refs_json,observed_at FROM pull_requests WHERE id=?`, newID, repositoryID, oldID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO work_pr_links(workspace_id,pr_id,relationship,confidence,evidence_json)
		SELECT workspace_id,?,relationship,confidence,evidence_json FROM work_pr_links WHERE pr_id=?`, newID, oldID); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM pull_requests WHERE id=?`, oldID)
	return err
}

func (c *Catalog) saveRepositoryEvidence(ctx context.Context, items []repositoryIdentity) error {
	return c.writeTransaction(ctx, "repository-evidence", func(tx *sql.Tx) error {
		for _, item := range items {
			if _, err := tx.Exec(`UPDATE repositories SET normalized_remote=COALESCE(normalized_remote,?),root_commit=COALESCE(root_commit,?),forge_id=COALESCE(forge_id,?) WHERE id=?`, nilIfEmpty(item.Normalized), nilIfEmpty(item.Root), nilIfEmpty(item.Forge), item.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

// forgeRefreshEvery is how often the service looks for repositories GitHub has
// not been asked about yet, forgeRetry how long it waits before asking again
// about one GitHub could not resolve (a private repository, a deleted one).
const (
	forgeRefreshEvery = 10 * time.Minute
	forgeRetry        = 6 * time.Hour
)

// forgeAttempts remembers, per catalog and repository, when GitHub was last
// asked and had no answer. Ingest never asks; this job does.
var forgeAttempts sync.Map

// keepRepositoryForgeIDs runs for the life of the service, resolving the
// repositories that indexing has added since the last pass.
func (c *Catalog) keepRepositoryForgeIDs(ctx context.Context) {
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if err := c.RefreshRepositoryForgeIDs(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "Repository forge refresh: %v\n", err)
		}
		timer.Reset(forgeRefreshEvery)
	}
}

// RefreshRepositoryForgeIDs asks GitHub, through the gh tool, about every
// github.com repository that has no forge ID yet, then merges the rows that
// turn out to be one repository (a move between owners, a rename). It leaves the
// rows alone when gh is missing or signed out, and while the library upgrade
// has not merged yet or is running, since the upgrade does that merge itself.
func (c *Catalog) RefreshRepositoryForgeIDs(ctx context.Context) error {
	items, err := loadRepositoryIdentities(c.DB)
	if err != nil {
		return err
	}
	prepareRepositoryIdentities(items)
	pending := map[string]bool{}
	for _, item := range items {
		if _, tried := forgeAttempts.Load(c.Path + "\x00" + item.Normalized); item.Forge == "" && strings.HasPrefix(item.Normalized, "github.com/") && !tried {
			pending[item.ID] = true
		}
	}
	if len(pending) == 0 {
		return nil
	}
	if resolveGitHubRepositories(ctx, items, func(item repositoryIdentity) bool { return pending[item.ID] }) != githubReady || ctx.Err() != nil {
		return ctx.Err()
	}
	resolved := map[string]bool{}
	forges := map[string]bool{}
	for _, item := range items {
		if !pending[item.ID] {
			continue
		}
		if item.Forge == "" {
			forgeAttempts.Store(c.Path+"\x00"+item.Normalized, true)
			time.AfterFunc(forgeRetry, func() { forgeAttempts.Delete(c.Path + "\x00" + item.Normalized) })
			continue
		}
		resolved[item.ID], forges[item.Forge] = true, true
	}
	if len(resolved) == 0 {
		return nil
	}
	// A row that already had its forge ID has no current name yet; the group's
	// name comes from GitHub's answer for it, so ask for those rows too.
	resolveGitHubRepositories(ctx, items, func(item repositoryIdentity) bool { return item.ForgeCanonical == "" && forges[item.Forge] })
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := c.saveRepositoryEvidence(ctx, items); err != nil {
		return err
	}
	if merged, err := c.metaValue(ctx, "repository_merge_version"); err != nil || merged != repositoryMergeVersion || c.upgradeRunning() {
		return err
	}
	for _, group := range planRepositoryMerges(items, c.RepositoryAliases, c.RepositorySeparate...) {
		if err := ctx.Err(); err != nil {
			return err
		}
		affected := resolved[group.Survivor.ID]
		for _, loser := range group.Losers {
			affected = affected || resolved[loser.ID]
		}
		if affected {
			if err := c.mergeRepositoryGroup(ctx, group); err != nil {
				return err
			}
		}
	}
	return nil
}
