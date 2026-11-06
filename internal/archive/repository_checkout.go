package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// A repository's checkout on this Mac (docs/projects-and-outcomes.md, "The
// file viewer's checkout"): the folder the person chose for the Files page
// to read a file's contents, its blame and commits' diffs from. Pharos runs
// read-only Git there, and only there, when such a view is opened, and
// stores nothing it reads (file_git.go). Without one the Files page shows
// stored facts: the tree, sessions, their edits and the commits.

// checkoutCandidateLimit bounds the recorded checkouts offered to choose from.
var checkoutCandidateLimit = 8

// RepositoryCheckout is a repository's chosen checkout on this Mac, whether
// Git can still read it there, and the checkouts this Mac recorded for its
// sessions, to choose from.
func (c *Catalog) RepositoryCheckout(ctx context.Context, repository string) (map[string]any, error) {
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT display_name,local_locations_json FROM repositories WHERE id=?`, repository)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	result := map[string]any{"repository_id": repository, "name": rows[0]["display_name"], "path": nil, "status": "unset",
		"reason": "Choose this repository's checkout on this Mac to see its files' contents, blame and diffs."}
	var chosen string
	err = c.reader().QueryRowContext(ctx, `SELECT path FROM repository_checkouts WHERE repository_id=? AND host_id=?`, repository, currentHost().ID).Scan(&chosen)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if chosen != "" {
		result["path"] = chosen
		if _, reason := checkoutUsable(chosen); reason != "" {
			result["status"], result["reason"] = "unavailable", reason
		} else {
			result["status"], result["reason"] = "set", nil
		}
	}
	candidates, err := c.checkoutCandidates(ctx, repository, firstString(rows[0]["local_locations_json"]))
	if err != nil {
		return nil, err
	}
	result["candidates"] = candidates
	return result, nil
}

// checkoutCandidates are the checkouts of a repository this Mac recorded that
// still hold it: where its sessions ran (a worktree's main checkout first)
// and the locations its identity records, newest first.
func (c *Catalog) checkoutCandidates(ctx context.Context, repository, recorded string) ([]string, error) {
	locations, err := c.gitLocations(ctx)
	if err != nil {
		return nil, err
	}
	var listed []string
	_ = json.Unmarshal([]byte(recorded), &listed)
	candidates := []string{}
	for _, location := range append(slices.Clone(locations[repository]), listed...) {
		for _, path := range []string{repositoryCheckout(location), location} {
			if len(candidates) >= checkoutCandidateLimit || slices.Contains(candidates, path) {
				continue
			}
			if top, reason := checkoutUsable(path); reason == "" && top == path {
				candidates = append(candidates, path)
			}
		}
	}
	// A main checkout (its .git a directory) before worktrees of it.
	main := func(path string) bool {
		info, err := os.Stat(filepath.Join(path, ".git"))
		return err == nil && info.IsDir()
	}
	slices.SortStableFunc(candidates, func(a, b string) int {
		return map[bool]int{true: 0, false: 1}[main(a)] - map[bool]int{true: 0, false: 1}[main(b)]
	})
	return candidates, nil
}

// checkoutUsable is the top of the Git work tree at path, or why Git cannot
// read a repository there. It reads only the file system.
func checkoutUsable(path string) (string, string) {
	if !filepath.IsAbs(path) {
		return "", "The checkout must be an absolute path."
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return "", "The checkout is gone from this Mac: " + path
	}
	common, err := integrationCommonDir(path)
	if err != nil {
		return "", "No Git repository is at " + path
	}
	if _, err := os.Stat(filepath.Join(common, "HEAD")); err != nil {
		return "", "The Git repository at " + path + " is gone."
	}
	for dir := filepath.Clean(path); ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, ""
		}
		if dir == filepath.Dir(dir) {
			return "", "No Git repository is at " + path
		}
	}
}

// SetRepositoryCheckout chooses a repository's checkout on this Mac, after
// checking that it is one: a remote, its first commit or a session there
// must match the repository. It reads the checkout's config and history
// read-only; nothing else is run.
func (c *Catalog) SetRepositoryCheckout(ctx context.Context, repository, path string) (map[string]any, error) {
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT display_name,COALESCE(normalized_remote,'') remote,COALESCE(root_commit,'') root,local_locations_json FROM repositories WHERE id=?`, repository)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no repository %q in this library", repository)
	}
	path = strings.TrimSpace(path)
	if rest, ok := strings.CutPrefix(path, "~/"); ok || path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(home, rest)
	}
	path = filepath.Clean(path)
	top, reason := checkoutUsable(path)
	if reason != "" {
		return nil, fmt.Errorf("%s", reason)
	}
	if !c.checkoutMatches(ctx, repository, top, rows[0]) {
		return nil, fmt.Errorf("%s does not look like a checkout of %s: none of its remotes or first commits match, and no session of %s ran there", top, firstString(rows[0]["display_name"]), firstString(rows[0]["display_name"]))
	}
	err = c.writeTransaction(ctx, "repository checkout", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO repository_checkouts(repository_id,host_id,path,set_at) VALUES(?,?,?,?)
			ON CONFLICT(repository_id,host_id) DO UPDATE SET path=excluded.path,set_at=excluded.set_at`, repository, currentHost().ID, top, now())
		return err
	})
	if err != nil {
		return nil, err
	}
	return c.RepositoryCheckout(ctx, repository)
}

// ClearRepositoryCheckout forgets a repository's checkout on this Mac.
func (c *Catalog) ClearRepositoryCheckout(ctx context.Context, repository string) (map[string]any, error) {
	err := c.writeTransaction(ctx, "repository checkout", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM repository_checkouts WHERE repository_id=? AND host_id=?`, repository, currentHost().ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return c.RepositoryCheckout(ctx, repository)
}

// checkoutMatches reports whether a work tree is the repository's: one of
// its remotes normalizes to the repository's remote, its first commit is
// the repository's, or a session of the repository ran inside it.
func (c *Catalog) checkoutMatches(ctx context.Context, repository, top string, row map[string]any) bool {
	if remote := firstString(row["remote"]); remote != "" {
		if output, err := outcomeGitCommand(ctx, top, outcomeGitShortTimeout, nil, "config", "--get-regexp", `^remote\..*\.url$`); err == nil {
			for _, line := range strings.Split(string(output), "\n") {
				if _, url, ok := strings.Cut(line, " "); ok && normalizeRepositoryRemote(url) == remote {
					return true
				}
			}
		}
	}
	if root := firstString(row["root"]); root != "" {
		if output, err := outcomeGitCommand(ctx, top, outcomeGitShortTimeout, nil, "rev-list", "--max-parents=0", "HEAD"); err == nil && slices.Contains(strings.Fields(string(output)), root) {
			return true
		}
	}
	locations, err := c.gitLocations(ctx)
	if err != nil {
		return false
	}
	var listed []string
	_ = json.Unmarshal([]byte(firstString(row["local_locations_json"])), &listed)
	for _, location := range append(locations[repository], listed...) {
		if location = filepath.Clean(location); location == top || strings.HasPrefix(location, top+string(filepath.Separator)) || repositoryCheckout(location) == top {
			return true
		}
	}
	return false
}

// viewerCheckout is where the Files page's Git reads a repository: its
// chosen checkout on this Mac, or why there is none to read.
func (c *Catalog) viewerCheckout(ctx context.Context, repository string) (string, string, error) {
	var chosen string
	err := c.reader().QueryRowContext(ctx, `SELECT path FROM repository_checkouts WHERE repository_id=? AND host_id=?`, repository, currentHost().ID).Scan(&chosen)
	if err == sql.ErrNoRows {
		return "", "Choose this repository's checkout on this Mac to see its files' contents, blame and diffs.", nil
	} else if err != nil {
		return "", "", err
	}
	if _, reason := checkoutUsable(chosen); reason != "" {
		return "", reason, nil
	}
	return chosen, "", nil
}
