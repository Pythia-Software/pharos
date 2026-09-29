package archive

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// stubGitHub points the gh lookup at a script that answers `gh api repos/<slug>`
// from repos (slug to JSON) and fails for any other repository. signedIn sets
// what `gh auth status` says. It returns the file recording each call.
func stubGitHub(t *testing.T, signedIn bool, repos map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls.log")
	var cases strings.Builder
	for slug, answer := range repos {
		fmt.Fprintf(&cases, "    repos/%s) echo '%s' ;;\n", slug, answer)
	}
	auth := "exit 0"
	if !signedIn {
		auth = "exit 1"
	}
	script := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> '%s'\ncase \"$1\" in\n  auth) %s ;;\n  api) case \"$2\" in\n%s    *) exit 1 ;; esac ;;\nesac\n", calls, auth, cases.String())
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	previous := githubCLI
	githubCLI = path
	t.Cleanup(func() { githubCLI = previous })
	return calls
}

func stubbedCalls(t *testing.T, calls string) string {
	t.Helper()
	data, err := os.ReadFile(calls)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

func upgradePending(t *testing.T, catalog *Catalog) map[string]int64 {
	t.Helper()
	steps, err := catalog.UpgradeSteps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pending := map[string]int64{}
	for _, step := range steps {
		pending[step.ID] = step.Pending
	}
	return pending
}

// legacyUpgradeCatalog returns a catalog that looks indexed by an older
// Pharos: every upgrade step has work to do.
func legacyUpgradeCatalog(t *testing.T) *Catalog {
	t.Helper()
	catalog, _ := testCatalog(t)
	ingestUsageFixture(t, catalog)
	record := WorkspaceRecord{SourceID: "upgrade-claude", SourceKind: "claude", Account: "local", Conversations: []ConversationRecord{{
		NativeID: "upgrade-claude", Provider: "claude", Model: "opus", Account: "local",
		Messages: []MessageRecord{{NativeID: "request", Kind: "metadata", Text: `{"type":"assistant","version":"2.1.280","message":{"id":"request","usage":{"input_tokens":10,"output_tokens":1}}}`}},
	}}}
	tx, err := catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = ingestWorkspace(tx, record, false); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// A library indexed entirely by this version has nothing to upgrade.
	for id, pending := range upgradePending(t, catalog) {
		if pending != 0 {
			t.Fatalf("fresh library: %s has %d pending", id, pending)
		}
	}

	// Imitate a catalog indexed by an older Pharos.
	for _, statement := range []string{
		"DELETE FROM usage_attribution_state",
		"UPDATE tool_ledger_state SET version='tools-v2'",
		"DELETE FROM conversation_instructions_state",
		"UPDATE conversations SET harness=NULL,harness_version_first=NULL,harness_version_last=NULL",
		"DELETE FROM meta WHERE key IN ('repository_merge_version','harness_version_upgrade')",
		`INSERT INTO repositories(id,canonical_remote,display_name,created_at,updated_at) VALUES
			('repo-ssh','git@github.com:owner/dup.git','dup','2026-01-01','2026-01-01'),
			('repo-https','https://github.com/owner/dup.git','dup','2026-01-01','2026-01-01'),
			('repo-renamed','https://github.com/Owner/Dup','dup-before-rename','2026-01-01','2026-01-01')`,
	} {
		if _, err := catalog.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return catalog
}

func TestLibraryUpgrade(t *testing.T) {
	catalog := legacyUpgradeCatalog(t)
	pending := upgradePending(t, catalog)
	if pending["repositories"] == 0 || pending["usage"] != 1 || pending["harness"] == 0 || pending["instructions"] == 0 || pending["tools"] != 2 {
		t.Fatalf("legacy library pending: %v", pending)
	}
	preview, err := catalog.RepositoryMergePreview(context.Background())
	if err != nil || len(preview) != 1 || preview[0].Name != "dup" || len(preview[0].Merged) != 2 {
		t.Fatalf("merge preview = %#v, %v", preview, err)
	}
	steps := []string{}
	if err := catalog.RunUpgrade(context.Background(), func(step string, done, total int) {
		if len(steps) == 0 || steps[len(steps)-1] != step {
			steps = append(steps, step)
		}
	}); err != nil {
		t.Fatal(err)
	}
	// Repositories merge before the ledger, which records file repositories.
	want := []string{"repositories", "usage", "harness", "instructions", "tools", "rollup"}
	if len(steps) != len(want) {
		t.Fatalf("steps ran in order %v, want %v", steps, want)
	}
	for index := range want {
		if steps[index] != want[index] {
			t.Fatalf("steps ran in order %v, want %v", steps, want)
		}
	}
	for id, pending := range upgradePending(t, catalog) {
		if pending != 0 {
			t.Fatalf("after upgrade: %s has %d pending", id, pending)
		}
	}
	var rows int
	if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM repositories WHERE display_name LIKE 'dup%'").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("dup repositories = %d, %v", rows, err)
	}
	status, err := catalog.UpgradeStatus(context.Background())
	if err != nil || status["needed"] != false || status["running"] != false {
		t.Fatalf("status = %v, %v", status, err)
	}
	// Saved views filtering on a retired name are pointed at the survivor.
	if renames := status["repository_renames"].(map[string]string); len(renames) != 1 || renames["dup-before-rename"] != "dup" {
		t.Fatalf("renames = %v", renames)
	}
	// A rename alone (different names, both with remotes) still runs the
	// merge step, which only the plan can recognize.
	for _, statement := range []string{
		"DELETE FROM meta WHERE key='repository_merge_version'",
		`INSERT INTO repositories(id,canonical_remote,display_name,created_at,updated_at) VALUES
			('repo-moved','https://github.com/owner/moved.git','moved','2026-01-01','2026-01-01')`,
	} {
		if _, err := catalog.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	catalog.RepositoryAliases = map[string]string{"github.com/owner/moved": "github.com/owner/dup"}
	if pending := upgradePending(t, catalog); pending["repositories"] == 0 {
		t.Fatalf("rename-only library pending: %v", pending)
	}
	if err := catalog.RunUpgrade(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM repositories WHERE display_name IN ('dup','moved')").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("rename-only merge left %d rows, %v", rows, err)
	}
	// Running it again finds nothing to do.
	if err := catalog.RunUpgrade(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

// The service refreshes the Library view back to back while it has dirty
// workspaces, which after an upgrade of Pharos is every workspace. The upgrade
// must get the write lock between those refreshes instead of failing with
// SQLITE_BUSY.
func TestLibraryUpgradeWhileLibraryRefreshes(t *testing.T) {
	catalog := legacyUpgradeCatalog(t)
	ctx, cancel := context.WithCancel(context.Background())
	started, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		for first := true; ctx.Err() == nil; {
			// Keep every workspace dirty, as a projection rebuild does, and
			// hold the lock a while, as a refresh on a large catalog does.
			tx, finish, err := catalog.beginTrackedWrite(ctx, "test-dirty")
			if err != nil {
				continue
			}
			if _, err := tx.Exec("INSERT OR IGNORE INTO workspace_library_dirty(workspace_id) SELECT id FROM workspaces"); err == nil {
				time.Sleep(5 * time.Millisecond)
				_ = tx.Commit()
			}
			_ = tx.Rollback()
			finish()
			if n, err := catalog.RefreshLibrary(ctx, 1); err == nil && n > 0 && first {
				first = false
				close(started)
			}
		}
	}()
	<-started
	var step string
	err := catalog.RunUpgrade(context.Background(), func(current string, _, _ int) { step = current })
	cancel()
	<-stopped
	if err != nil {
		t.Fatalf("upgrade alongside Library refreshes, in %s: %v", step, err)
	}
	for id, pending := range upgradePending(t, catalog) {
		if pending != 0 {
			t.Fatalf("after upgrade: %s has %d pending", id, pending)
		}
	}
}

func TestLibraryUpgradeAsksGitHubWithoutAFlag(t *testing.T) {
	calls := stubGitHub(t, true, map[string]string{
		"gbdubs/alexandria":      `{"id":1377317940,"full_name":"Pythia-Software/pharos"}`,
		"gbdubs/pharos":          `{"id":1377317940,"full_name":"Pythia-Software/pharos"}`,
		"pythia-software/pharos": `{"id":1377317940,"full_name":"Pythia-Software/pharos"}`,
		"acme/unrelated":         `{"id":5,"full_name":"acme/unrelated"}`,
	})
	catalog, _ := testCatalog(t)
	for _, statement := range []string{
		`INSERT INTO repositories(id,canonical_remote,normalized_remote,display_name,created_at,updated_at) VALUES
			('old','https://github.com/gbdubs/alexandria.git','github.com/gbdubs/alexandria','alexandria','t','t'),
			('mid','https://github.com/gbdubs/pharos.git','github.com/gbdubs/pharos','pharos','t','t'),
			('new','https://github.com/Pythia-Software/pharos.git','github.com/pythia-software/pharos','pharos','t','t'),
			('other','https://github.com/acme/unrelated.git','github.com/acme/unrelated','unrelated','t','t')`,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at,repository_id) VALUES('w','codex','local','w','work','t','old')`,
		"DELETE FROM meta WHERE key='repository_merge_version'",
	} {
		if _, err := catalog.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	preview, err := catalog.RepositoryMergePreview(context.Background())
	if err != nil || len(preview) != 1 || preview[0].Name != "pharos" || len(preview[0].Merged) != 2 || !containsString(preview[0].Signals, "forge_id") {
		t.Fatalf("preview = %#v, %v", preview, err)
	}
	if got := stubbedCalls(t, calls); !strings.Contains(got, "api repos/gbdubs/alexandria") {
		t.Fatalf("preview did not ask GitHub: %q", got)
	}
	if err := os.Remove(calls); err != nil {
		t.Fatal(err)
	}
	if err := catalog.RunUpgrade(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := stubbedCalls(t, calls); !strings.Contains(got, "api repos/gbdubs/alexandria") {
		t.Fatalf("upgrade did not ask GitHub: %q", got)
	}
	var rows int
	var forge string
	if err := catalog.DB.QueryRow(`SELECT COUNT(*),COALESCE(MAX(forge_id),'') FROM repositories WHERE display_name='pharos'`).Scan(&rows, &forge); err != nil || rows != 1 || forge != "1377317940" {
		t.Fatalf("pharos rows=%d forge=%s err=%v", rows, forge, err)
	}
	status, err := catalog.UpgradeStatus(context.Background())
	if err != nil || status["github"] != githubReady {
		t.Fatalf("status = %v, %v", status, err)
	}
}

func TestLibraryUpgradeWithoutGitHub(t *testing.T) {
	for name, signedIn := range map[string]bool{"missing": false, "signed_out": false} {
		t.Run(name, func(t *testing.T) {
			want := githubMissing
			if name == "signed_out" {
				stubGitHub(t, signedIn, nil)
				want = githubSignedOut
			}
			catalog := legacyUpgradeCatalog(t)
			if err := catalog.RunUpgrade(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			// The merge that needs no GitHub still happened, and the status says
			// renamed repositories may stay separate.
			var rows int
			if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM repositories WHERE display_name LIKE 'dup%'").Scan(&rows); err != nil || rows != 1 {
				t.Fatalf("dup repositories = %d, %v", rows, err)
			}
			status, err := catalog.UpgradeStatus(context.Background())
			if err != nil || status["needed"] != false || status["github"] != want {
				t.Fatalf("status = %v, %v", status, err)
			}
		})
	}
}

// A catalog upgraded by 0.4.0 has merge version 1, and ingest has since split
// repositories again.
func TestLibraryUpgradeRepairsSplitRepositories(t *testing.T) {
	catalog, _ := testCatalog(t)
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := catalog.DB.Exec(statement, args...); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	clone := "/Users/t/gbdubs/excel-corpus"
	exec(`INSERT INTO repositories(id,canonical_remote,normalized_remote,display_name,root_commit,local_locations_json,created_at,updated_at) VALUES
		('gbdubs','git@github.com:gbdubs/excel-corpus.git','github.com/gbdubs/excel-corpus','excel-corpus','r1',?,'t','t'),
		('pythia','git@github.com:Pythia-Software/excel-corpus.git','github.com/pythia-software/excel-corpus','excel-corpus','r1',?,'t','t'),
		('old-local','','', 'excel-corpus',NULL,?,'t','t'),
		('big-local',NULL,NULL,'excel-corpus',NULL,?,'t','t'),
		('agy-1',NULL,NULL,'xlsxl-agy',NULL,'["/Users/t/conductor/workspaces/xlsxl-agy/one"]','t','t'),
		('agy-2',NULL,NULL,'xlsxl-agy',NULL,'["/Users/t/conductor/workspaces/xlsxl-agy/two"]','t','t')`,
		jsonText([]string{clone}), jsonText([]string{clone + "/.task-worktrees/e1"}), jsonText([]string{clone}), jsonText([]string{clone + "/.task-worktrees/e2"}))
	for i := 0; i < 10; i++ {
		exec(`INSERT INTO repositories(id,canonical_remote,display_name,root_commit,local_locations_json,created_at,updated_at) VALUES(?,?,'origin','r9',?,'t','t')`,
			fmt.Sprintf("origin-%d", i), fmt.Sprintf("/Users/t/conductor/workspaces/tl1/ws/.context/run%d/origin.git", i), jsonText([]string{"/Users/t/conductor/workspaces/tl1/ws/.context/repo/.candidate-worktrees/c" + fmt.Sprint(i)}))
	}
	workspaces := map[string]string{"w1": "gbdubs", "w2": "pythia", "w3": "old-local", "w4": "big-local", "w5": "big-local", "w6": "agy-1", "w7": "agy-2", "w8": "origin-0"}
	for id, repository := range workspaces {
		exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at,repository_id) VALUES(?,'codex','local',?,'work','t',?)`, id, id, repository)
	}
	exec(`INSERT INTO pull_requests(id,host,repository_id,number,title) VALUES('p1','github.com','big-local',7,'seven'),('p2','github.com','gbdubs',8,'eight')`)
	exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,coverage) VALUES('c1','w4','codex','local','c1','complete')`)
	exec(`INSERT INTO tool_calls(id,workspace_id,conversation_id,sequence,provider,kind,tool_name,tool_category,status,path_repository,path_repository_id) VALUES('t1','w4','c1',1,'codex','call','Edit','edit','ok','excel-corpus','big-local')`)
	exec(`INSERT INTO tool_ledger_state(conversation_id,version,tool_calls,updated_at) VALUES('c1',?,1,'t')`, toolLedgerVersion)
	exec(`DELETE FROM meta WHERE key IN ('repository_merge_version')`)
	exec(`INSERT INTO meta(key,value) VALUES('repository_merge_version','1')`)
	// The rest of the upgrade is current; only the repository step is pending.
	exec(`INSERT INTO meta(key,value) VALUES('harness_version_upgrade','1') ON CONFLICT(key) DO NOTHING`)
	pending := upgradePending(t, catalog)
	if pending["repositories"] != 16 || pending["tools"] != 0 {
		t.Fatalf("pending after a version 1 merge: %v", pending)
	}
	if err := catalog.RunUpgrade(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if after := upgradePending(t, catalog); after["repositories"] != 0 {
		t.Fatalf("pending after the repair: %v", after)
	}
	rows, err := catalog.DB.Query(`SELECT display_name,COUNT(*) FROM repositories GROUP BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for rows.Next() {
		var name string
		var count int
		if err := rows.Scan(&name, &count); err != nil {
			t.Fatal(err)
		}
		counts[name] = count
	}
	rows.Close()
	if counts["excel-corpus"] != 1 || counts["xlsxl-agy"] != 1 || counts["origin"] != 10 {
		t.Fatalf("repositories after the repair: %v", counts)
	}
	repositoryOf := func(query string) string {
		t.Helper()
		var id string
		if err := catalog.DB.QueryRow(query).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	survivor := repositoryOf(`SELECT id FROM repositories WHERE display_name='excel-corpus'`)
	var ids []string
	for _, workspace := range []string{"w1", "w2", "w3", "w4", "w5"} {
		ids = append(ids, repositoryOf(`SELECT repository_id FROM workspaces WHERE id='`+workspace+`'`))
	}
	sort.Strings(ids)
	if ids[0] != survivor || ids[4] != survivor {
		t.Fatalf("workspaces point at %v, want %s", ids, survivor)
	}
	if got := repositoryOf(`SELECT path_repository_id FROM tool_calls WHERE id='t1'`); got != survivor {
		t.Fatalf("tool call repository = %s, want %s", got, survivor)
	}
	var prs int
	if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM pull_requests WHERE repository_id=?`, survivor).Scan(&prs); err != nil || prs != 2 {
		t.Fatalf("pull requests on the survivor = %d, %v", prs, err)
	}
	if got := repositoryOf(`SELECT repository_id FROM workspaces WHERE id='w6'`); got != repositoryOf(`SELECT repository_id FROM workspaces WHERE id='w7'`) {
		t.Fatal("xlsxl-agy workspaces still point at different rows")
	}
}

// New rows are resolved by the background job, never during ingest, and the
// moves it finds are merged without another upgrade.
func TestRefreshRepositoryForgeIDsMergesNewMoves(t *testing.T) {
	calls := stubGitHub(t, true, map[string]string{
		"acme/old": `{"id":42,"full_name":"Acme/new"}`,
		"acme/new": `{"id":42,"full_name":"Acme/new"}`,
	})
	catalog, _ := testCatalog(t)
	exec := func(statement string) {
		t.Helper()
		if _, err := catalog.DB.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	exec(`INSERT INTO repositories(id,canonical_remote,normalized_remote,display_name,created_at,updated_at) VALUES
		('old','https://github.com/acme/old.git','github.com/acme/old','old','t','t'),
		('new','https://github.com/Acme/new.git','github.com/acme/new','new','t','t'),
		('private','https://github.com/acme/private.git','github.com/acme/private','private','t','t')`)
	exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at,repository_id) VALUES('w','codex','local','w','work','t','old')`)
	exec(`DELETE FROM meta WHERE key='repository_merge_version'`)
	count := func() int {
		var rows int
		if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM repositories`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	// Before the library upgrade has merged, the job only records forge IDs.
	if err := catalog.RefreshRepositoryForgeIDs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count() != 3 {
		t.Fatalf("merged before the upgrade: %d rows", count())
	}
	var forge string
	if err := catalog.DB.QueryRow(`SELECT forge_id FROM repositories WHERE id='old'`).Scan(&forge); err != nil || forge != "42" {
		t.Fatalf("forge id = %q, %v", forge, err)
	}
	exec(`INSERT INTO meta(key,value) VALUES('repository_merge_version','` + repositoryMergeVersion + `')`)
	exec(`UPDATE repositories SET forge_id=NULL WHERE id='old'`)
	if err := catalog.RefreshRepositoryForgeIDs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count() != 2 {
		t.Fatalf("%d rows after the refresh", count())
	}
	var repository string
	if err := catalog.DB.QueryRow(`SELECT repository_id FROM workspaces WHERE id='w'`).Scan(&repository); err != nil || repository != "new" {
		t.Fatalf("workspace repository = %q, %v", repository, err)
	}
	// A repository GitHub cannot resolve is not asked about again.
	before := strings.Count(stubbedCalls(t, calls), "repos/acme/private")
	if err := catalog.RefreshRepositoryForgeIDs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := strings.Count(stubbedCalls(t, calls), "repos/acme/private"); before != 1 || after != 1 {
		t.Fatalf("private repository asked %d then %d times", before, after)
	}
}
