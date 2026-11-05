package archive

import (
	"context"
	"fmt"
	"math"
	"slices"
	"testing"
)

// An outcomes row prices its session as the Library does and names the
// model that took its cost.
func TestOutcomeRowsCarryCostAndMainModel(t *testing.T) {
	catalog, _ := testCatalog(t)
	doc := testPricing()
	doc.Changes = append(doc.Changes, priceChange{Provider: "openai", Model: "gpt-6-sol", EffectiveFrom: "2026-01-01", Input: rate(10), CacheRead: rate(1), Output: rate(100), Status: "confirmed", SourceURL: "https://example.com/g"})
	if err := catalog.replacePricing(doc, "test"); err != nil {
		t.Fatal(err)
	}
	ingestUsageFixture(t, catalog)
	execTx(t, catalog.DB, `UPDATE agent_sessions SET model='gpt-6-sol'`)
	rows, err := catalog.outcomeRows(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v %v", rows, err)
	}
	library, err := catalog.searchRows(SearchOptions{}, nil)
	if err != nil || len(library) != 1 {
		t.Fatalf("%v %v", library, err)
	}
	row := rows[0]
	if cost, ok := row["cost_usd"].(float64); !ok || math.Abs(cost-library[0]["cost_usd"].(float64)) > 1e-12 || row["price_status"] != "priced" {
		t.Fatalf("outcome cost %#v, library %#v", row["cost_usd"], library[0]["cost_usd"])
	}
	if row["model_family"] != modelFamily("gpt-6-sol") || row["provider"] == nil || row["started_at"] == "" || integer(row["tokens"]) != 3050 {
		t.Fatalf("outcome usage: %#v", row)
	}
}

// Each session's evidence tier, branch and kept change, across Git and
// folders: branch belongs to repository sessions only, and a kept change is
// one whose label says its changes stayed.
func TestOutcomeRowsTiersAndBranches(t *testing.T) {
	f := newGitFixture(t)
	// On local main, which is mainline without a remote: landed.
	f.session("committed", "")
	f.edit("committed", "main.go", 5)
	f.repo.commit(4.99, "Work", map[string]string{"main.go": "package main\n"})
	f.session("idle", "")
	execTx(t, f.catalog.DB, `UPDATE workspaces SET branch='feature/x' WHERE id='committed'`)
	f.outcomeFixture.session("notes", "claude")
	f.outcomeFixture.edit("notes", "/notes/a.md", "", "", 20, "", code("note", 0, 3))
	execTx(t, f.catalog.DB, `UPDATE workspaces SET project_folder='~/notes',branch='main' WHERE id='notes'`)
	f.outcomeFixture.session("chat", "chatgpt")
	// A repository this Mac never checked out: not checked, and why.
	execTx(t, f.catalog.DB, `INSERT INTO repositories(id,display_name,created_at,updated_at) VALUES('repo-gone','gone','x','x')`)
	f.outcomeFixture.session("gone", "claude")
	execTx(t, f.catalog.DB, `UPDATE workspaces SET repository_id='repo-gone' WHERE id='gone'`)
	f.outcomeFixture.edit("gone", "/gone/main.go", "repo-gone", "main.go", 5, "", code("gone", 0, 3))
	f.refresh()
	rows, err := f.catalog.outcomeRows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, row := range rows {
		got[firstString(row["id"])] = fmt.Sprintf("%v %v %v %v %v %v", row["outcome_label"], row["evidence_tier"], row["git_check"], row["git_check_reason"], row["branch"], row["kept_change"])
	}
	want := map[string]string{
		"committed": "landed local_git checked <nil> feature/x 1",
		"idle":      "no_changes transcript not_applicable <nil> <nil> 0",
		"notes":     "changed_kept transcript not_applicable <nil> <nil> 1",
		"chat":      "unknown none not_applicable <nil> <nil> 0",
		"gone":      "changed_recent transcript not_checked No checkout of this repository was recorded on this Mac. <nil> 0",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("outcome rows:\n got %v\nwant %v", got, want)
	}
	// Its cells read the project's kind and whether and why Git checked it,
	// whichever columns a query shows.
	if fields := queryRowActionFields("outcomes", []string{"branch", "linked_commits"}); !slices.Contains(fields, "project_kind") || !slices.Contains(fields, "git_check") || !slices.Contains(fields, "git_check_reason") {
		t.Fatalf("outcome row fields: %v", fields)
	}
}
