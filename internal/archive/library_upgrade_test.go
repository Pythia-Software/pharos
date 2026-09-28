package archive

import (
	"context"
	"testing"
)

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

func TestLibraryUpgrade(t *testing.T) {
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
	pending := upgradePending(t, catalog)
	if pending["repositories"] == 0 || pending["usage"] != 1 || pending["harness"] == 0 || pending["tools"] != 2 {
		t.Fatalf("legacy library pending: %v", pending)
	}
	preview, err := catalog.RepositoryMergePreview(context.Background(), false)
	if err != nil || len(preview) != 1 || preview[0].Name != "dup" || len(preview[0].Merged) != 2 {
		t.Fatalf("merge preview = %#v, %v", preview, err)
	}
	steps := []string{}
	if err := catalog.RunUpgrade(context.Background(), false, func(step string, done, total int) {
		if len(steps) == 0 || steps[len(steps)-1] != step {
			steps = append(steps, step)
		}
	}); err != nil {
		t.Fatal(err)
	}
	// Repositories merge before the ledger, which records file repositories.
	want := []string{"repositories", "usage", "harness", "tools", "rollup"}
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
	if err := catalog.RunUpgrade(context.Background(), false, nil); err != nil {
		t.Fatal(err)
	}
	if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM repositories WHERE display_name IN ('dup','moved')").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("rename-only merge left %d rows, %v", rows, err)
	}
	// Running it again finds nothing to do.
	if err := catalog.RunUpgrade(context.Background(), false, nil); err != nil {
		t.Fatal(err)
	}
}
