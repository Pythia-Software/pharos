package archive

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMainIntegrationFindsMergeAndDirectHistory(t *testing.T) {
	slowTest(t)
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", repo}, args...)...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "-b", "main")
	git("config", "user.name", "Archive Test")
	git("config", "user.email", "archive@example.com")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "Base")
	git("branch", "feature")
	git("checkout", "feature")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "Feature work")
	feature := git("rev-parse", "HEAD")
	git("checkout", "main")
	if err := os.WriteFile(filepath.Join(repo, "main.txt"), []byte("main"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "Main work")
	git("merge", "--no-ff", "feature", "-m", "Merge feature into main")
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	merged := mainIntegration(repo, feature, "git@github.com:acme/archive.git")
	if merged == nil || merged["main_merge_method"] != "merge" || merged["main_merge_title"] != "Merge feature into main" || !strings.HasSuffix(firstString(merged["main_merge_url"]), firstString(merged["main_merge_commit"])) {
		t.Fatalf("merge integration: %#v", merged)
	}
	git("checkout", "-b", "unmerged")
	if err := os.WriteFile(filepath.Join(repo, "unmerged.txt"), []byte("unmerged"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "Unmerged work")
	unmerged := git("rev-parse", "HEAD")
	if got := mainIntegration(repo, unmerged, ""); got != nil {
		t.Fatalf("unmerged commit attributed to main: %#v", got)
	}
	git("checkout", "main")
	git("merge", "--ff-only", "unmerged")
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	direct := mainIntegration(repo, unmerged, "")
	if direct == nil || direct["main_merge_method"] != "direct" || direct["main_merge_commit"] != unmerged {
		t.Fatalf("direct integration: %#v", direct)
	}
	catalog, _ := testCatalog(t)
	if _, err := catalog.DB.Exec(`INSERT INTO repositories(id,canonical_remote,display_name,local_locations_json,created_at,updated_at) VALUES(?,?,?,?,?,?)`, "repo", "git@github.com:acme/archive.git", "archive", jsonText([]string{repo}), now(), now()); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,repository_id,title,head_ref,indexed_at) VALUES(?,?,?,?,?,?,?,?)`, "work", "conductor", "local", "source", "repo", "Feature work", feature, now()); err != nil {
		t.Fatal(err)
	}
	// Only paths recorded on this host are consulted; another Mac's sighting of
	// the same path must not run Git here.
	if _, err := catalog.DB.Exec(`INSERT INTO workspace_sightings(workspace_id,host_id,source_name,repository_locations_json,first_seen_at,last_seen_at)
		VALUES('work','host_elsewhere','conductor',?,?,?)`, jsonText([]string{repo}), now(), now()); err != nil {
		t.Fatal(err)
	}
	if err := catalog.refreshMainIntegrations(context.Background()); err != nil {
		t.Fatal(err)
	}
	var unattributed sql.NullString
	if err := catalog.DB.QueryRow(`SELECT main_merge_commit FROM workspaces WHERE id='work'`).Scan(&unattributed); err != nil || unattributed.Valid {
		t.Fatalf("another host's path was used: %v %v", unattributed, err)
	}
	if _, err := catalog.DB.Exec(`INSERT INTO workspace_sightings(workspace_id,host_id,source_name,location,first_seen_at,last_seen_at)
		VALUES('work',?,'conductor',?,?,?)`, currentHost().ID, repo, now(), now()); err != nil {
		t.Fatal(err)
	}
	if err := catalog.refreshMainIntegrations(context.Background()); err != nil {
		t.Fatal(err)
	}
	var storedCommit, storedTitle string
	if err := catalog.DB.QueryRow(`SELECT main_merge_commit,main_merge_title FROM workspaces WHERE id='work'`).Scan(&storedCommit, &storedTitle); err != nil || storedCommit != merged["main_merge_commit"] || storedTitle != "Merge feature into main" {
		t.Fatalf("stored integration: %q %q, %v", storedCommit, storedTitle, err)
	}
}

func TestMainIntegrationNegativeLookupCache(t *testing.T) {
	slowTest(t)
	useHost(t, "git-cache-host")
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		output, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "-b", "main")
	git("config", "user.name", "Archive Test")
	git("config", "user.email", "archive@example.com")
	git("commit", "--allow-empty", "-m", "Base")
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	git("checkout", "-b", "feature")
	git("commit", "--allow-empty", "-m", "Feature")
	head := git("rev-parse", "HEAD")

	catalog, _ := testCatalog(t)
	if _, err := catalog.DB.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,head_ref,indexed_at)
		VALUES('work','conductor','local','source','Feature',?,?)`, head, now()); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec(`INSERT INTO workspace_sightings(workspace_id,host_id,source_name,location,first_seen_at,last_seen_at)
		VALUES('work',?,'conductor',?,?,?)`, currentHost().ID, repo, now(), now()); err != nil {
		t.Fatal(err)
	}

	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(t.TempDir(), "git-calls")
	other := ""
	shimDir := t.TempDir()
	shim := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$PHAROS_GIT_TRACE\"\nif [ \"$PHAROS_GIT_FAIL_BATCH\" = 1 ]; then case \"$*\" in *cat-file*) exit 1 ;; esac; fi\nexec '" + strings.ReplaceAll(gitPath, "'", "'\\''") + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(shim), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PHAROS_GIT_TRACE", trace)
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	refresh := func() []string {
		t.Helper()
		if err := os.WriteFile(trace, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := catalog.refreshMainIntegrations(context.Background()); err != nil {
			t.Fatal(err)
		}
		traced, err := os.ReadFile(trace)
		if err != nil {
			t.Fatal(err)
		}
		// Only the lookups in this repository: not Git that anything else in
		// the process runs.
		calls := []string{}
		for _, call := range strings.Split(strings.TrimSpace(string(traced)), "\n") {
			if strings.HasPrefix(call, "-C "+repo+" ") || other != "" && strings.HasPrefix(call, "-C "+other+" ") {
				calls = append(calls, call)
			}
		}
		return calls
	}
	cached := func() bool {
		t.Helper()
		var rows int
		if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM git_main_negative_lookups WHERE workspace_id='work'").Scan(&rows); err != nil {
			t.Fatal(err)
		}
		return rows == 1
	}
	// A Git call that times out on a busy machine leaves the scan
	// inconclusive, and so uncached: scan again until it concludes.
	for attempt := 1; ; attempt++ {
		calls := refresh()
		if cached() {
			if len(calls) < 2 {
				t.Fatalf("first negative scan made only %d Git calls: %v", len(calls), calls)
			}
			break
		}
		if attempt == 5 {
			t.Fatalf("no negative lookup was cached after %d scans: %v", attempt, calls)
		}
	}
	if calls := refresh(); len(calls) != 1 || !strings.Contains(calls[0], "for-each-ref") {
		t.Fatalf("unchanged tip should need only one ref lookup, got %v", calls)
	}
	git("checkout", "main")
	git("commit", "--allow-empty", "-m", "More main work")
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	advanced := refresh()
	if len(advanced) < 2 {
		t.Fatalf("advanced main tip did not inspect the graph delta: %v", advanced)
	}
	for _, call := range advanced {
		if strings.Contains(call, "merge-base --is-ancestor "+head+" ") {
			t.Fatalf("unchanged negative head was individually rechecked after a fast-forward: %v", advanced)
		}
	}
	git("checkout", "feature")
	git("commit", "--allow-empty", "-m", "More feature work")
	newHead := git("rev-parse", "HEAD")
	if _, err := catalog.DB.Exec(`UPDATE workspaces SET head_ref=? WHERE id='work'`, newHead); err != nil {
		t.Fatal(err)
	}
	if calls := refresh(); len(calls) < 2 {
		t.Fatalf("changed workspace head did not repeat ancestry check: %v", calls)
	}
	git("checkout", "main")
	git("merge", "--no-ff", "feature", "-m", "Merge feature")
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	refresh()
	var merged string
	if err := catalog.DB.QueryRow(`SELECT main_merge_commit FROM workspaces WHERE id='work'`).Scan(&merged); err != nil || merged == "" {
		t.Fatalf("merge after cached negatives was not recorded: %q, %v", merged, err)
	}
	// A failed ref probe must leave its repository retryable without crashing
	// the scan or preventing a healthy sibling from being integrated.
	broken := t.TempDir()
	if err := os.Mkdir(filepath.Join(broken, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	execTx(t, catalog.DB,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,head_ref,indexed_at) SELECT 'broken','conductor','local','broken','Broken',head_ref,'t' FROM workspaces WHERE id='work'`,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,head_ref,indexed_at) SELECT 'healthy','conductor','local','healthy','Healthy',head_ref,'t' FROM workspaces WHERE id='work'`)
	if _, err := catalog.DB.Exec(`INSERT INTO workspace_sightings(workspace_id,host_id,source_name,location,first_seen_at,last_seen_at) VALUES('broken',?,'conductor',?,'t','t'),('healthy',?,'conductor',?,'t','t')`, currentHost().ID, broken, currentHost().ID, repo); err != nil {
		t.Fatal(err)
	}
	refresh()
	if countRows(t, catalog, `SELECT COUNT(*) FROM workspaces WHERE id='healthy' AND main_merge_commit IS NOT NULL`) != 1 || countRows(t, catalog, `SELECT COUNT(*) FROM git_main_negative_lookups WHERE workspace_id='broken'`) != 0 {
		t.Fatal("a damaged repository hid healthy results or supplied durable negative evidence")
	}
	if err := os.Remove(filepath.Join(broken, ".git")); err != nil {
		t.Fatal(err)
	}
	git("worktree", "add", "--detach", broken, "HEAD")
	refresh()
	if countRows(t, catalog, `SELECT COUNT(*) FROM workspaces WHERE id='broken' AND main_merge_commit IS NOT NULL`) != 1 {
		t.Fatal("a repaired repository was not retried")
	}

	// A second worktree and many workspaces share one object inventory and
	// history. Missing objects remain retryable without a subprocess per head.
	other = filepath.Join(t.TempDir(), "worktree")
	git("worktree", "add", "--detach", other, "HEAD")
	statements := []string{}
	for index := 0; index < 36; index++ {
		id := fmt.Sprintf("batch-%d", index)
		candidate := newHead
		if index >= 24 {
			candidate = fmt.Sprintf("%040x", index)
		}
		statements = append(statements, fmt.Sprintf(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,head_ref,indexed_at) VALUES('%s','conductor','local','%s','Batch','%s','t')`, id, id, candidate), fmt.Sprintf(`INSERT INTO workspace_sightings(workspace_id,host_id,source_name,location,first_seen_at,last_seen_at) VALUES('%s','%s','conductor','%s','t','t')`, id, currentHost().ID, other))
	}
	execTx(t, catalog.DB, statements...)
	t.Setenv("PHAROS_GIT_FAIL_BATCH", "1")
	if failed := refresh(); len(failed) > 5 {
		t.Fatalf("failed object inventory was retried for each head: %v", failed)
	}
	t.Setenv("PHAROS_GIT_FAIL_BATCH", "")
	calls := refresh()
	t.Logf("36 workspace heads sharing a repository: %d Git commands", len(calls))
	if len(calls) > 10 {
		t.Fatalf("shared repository repeated Git work for each workspace: %d calls: %v", len(calls), calls)
	}
	var integrated int
	if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM workspaces WHERE id LIKE 'batch-%' AND main_merge_commit IS NOT NULL").Scan(&integrated); err != nil || integrated != 24 {
		t.Fatalf("batched integration lost results: %d %v", integrated, err)
	}
	calls = refresh()
	for _, call := range calls {
		if strings.Contains(call, "merge-base") {
			t.Fatalf("missing heads were checked individually: %v", calls)
		}
	}
	// The remote's declared default branch can be master or another name.
	git("update-ref", "refs/remotes/origin/master", "HEAD")
	git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/master")
	git("update-ref", "-d", "refs/remotes/origin/main")
	if value := mainIntegration(repo, newHead, ""); value == nil {
		t.Fatal("declared default branch was ignored")
	}

}
