package archive

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
)

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

func mergeRepositoryGroup(db *sql.DB, group repositoryMergeGroup) error {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	all := append([]repositoryIdentity{group.Survivor}, group.Losers...)
	aliases, locations := []string{}, []string{}
	for _, item := range all {
		aliases = repositoryUnion(aliases, item.Aliases, []string{item.Remote})
		locations = repositoryUnion(locations, item.Locations)
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
				if _, err := tx.Exec(`UPDATE pull_requests SET repository_id=? WHERE id=?`, group.Survivor.ID, entry.id); err != nil {
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
		if _, err := tx.Exec(`DELETE FROM repositories WHERE id=?`, loser.ID); err != nil {
			return err
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
	for _, key := range []string{"tool_rollup_generation"} {
		if _, err := tx.Exec(`DELETE FROM meta WHERE key=?`, key); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('repository_merge_version','1') ON CONFLICT(key) DO UPDATE SET value=excluded.value`); err != nil {
		return err
	}
	return tx.Commit()
}

func saveRepositoryEvidence(db *sql.DB, items []repositoryIdentity) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range items {
		if _, err := tx.Exec(`UPDATE repositories SET normalized_remote=COALESCE(normalized_remote,?),root_commit=COALESCE(root_commit,?),forge_id=COALESCE(forge_id,?) WHERE id=?`, nilIfEmpty(item.Normalized), nilIfEmpty(item.Root), nilIfEmpty(item.Forge), item.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
