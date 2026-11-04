package archive

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

// usageEvent is a Claude Code assistant event in a session with usage, on
// branch when one is given ("-" leaves gitBranch out).
func usageEvent(session, id, branch, at string, tokens int) string {
	extra := ""
	if branch != "-" {
		extra = fmt.Sprintf(`,"gitBranch":%q`, branch)
	}
	return fmt.Sprintf(`{"type":"assistant","uuid":"%s-%s","sessionId":%q%s,"timestamp":%q,"message":{"id":%q,"role":"assistant","model":"claude-test","content":"Done.","usage":{"input_tokens":%d,"output_tokens":0}}}`+"\n",
		session, id, session, extra, at, id, tokens)
}

func TestBranchShares(t *testing.T) {
	event := func(id, branch, at string, tokens int) branchEvent {
		return branchEvent{Session: "s", CreatedAt: at, Model: "m", Raw: strings.TrimSpace(usageEvent("s", id, branch, at, tokens))}
	}
	shares := branchShares([]branchEvent{
		event("r1", "-", "2026-09-01T10:00:00Z", 100), // before the first branch
		event("r2", "feature/a", "2026-09-01T10:10:00Z", 200),
		event("r2", "feature/a", "2026-09-01T10:10:00Z", 200), // the same response, on its next block
		event("r3", "-", "2026-09-01T10:20:00Z", 300),         // stays on feature/a
		event("r4", "feature/b", "2026-09-01T11:00:00Z", 400),
		event("r5", "HEAD", "2026-09-01T11:30:00Z", 50), // detached: no branch
	})
	got := fmt.Sprint(shares)
	want := fmt.Sprint(map[branchShareKey]map[string]int64{
		{"s", "2026-09-01T10:00:00Z", "m"}: {"": 100, "feature/a": 500},
		{"s", "2026-09-01T11:00:00Z", "m"}: {"feature/b": 400, "": 50},
	})
	if got != want {
		t.Fatalf("shares\n got %s\nwant %s", got, want)
	}
	// A conversation that never recorded a branch has no shares.
	if shares := branchShares([]branchEvent{event("r1", "-", "2026-09-01T10:00:00Z", 100)}); shares != nil {
		t.Fatalf("no branch: %v", shares)
	}
}

// Spend by branch splits a session's usage among the branches its turns
// were on, keeps a repository session without a branch apart, and leaves out
// sessions outside a repository.
func TestBranchUsageRows(t *testing.T) {
	library := mixedLibrary(t)
	app := library.folders["app"]
	writeClaudeSession(t, library.claude, "s-switch", strings.Join([]string{
		fmt.Sprintf(`{"type":"user","uuid":"s-switch-u","sessionId":"s-switch","cwd":%q,"timestamp":"2026-09-02T10:00:00Z","message":{"role":"user","content":"Work on two branches"}}`, app) + "\n",
		usageEvent("s-switch", "r1", "-", "2026-09-02T10:01:00Z", 1000),
		usageEvent("s-switch", "r2", "feature/a", "2026-09-02T10:20:00Z", 3000),
		usageEvent("s-switch", "r3", "feature/b", "2026-09-02T11:05:00Z", 2000),
		usageEvent("s-switch", "r4", "feature/b", "2026-09-02T15:30:00Z", 1000),
	}, ""))
	writeClaudeSession(t, library.claude, "s-plain", strings.Join([]string{
		fmt.Sprintf(`{"type":"user","uuid":"s-plain-u","sessionId":"s-plain","cwd":%q,"timestamp":"2026-09-02T12:00:00Z","message":{"role":"user","content":"No branch here"}}`, app) + "\n",
		usageEvent("s-plain", "p1", "-", "2026-09-02T12:01:00Z", 500),
	}, ""))
	writeClaudeSession(t, library.claude, "s-notes-usage", strings.Join([]string{
		fmt.Sprintf(`{"type":"user","uuid":"s-n-u","sessionId":"s-notes-usage","cwd":%q,"timestamp":"2026-09-02T13:00:00Z","message":{"role":"user","content":"Notes"}}`, library.folders["notes"]) + "\n",
		usageEvent("s-notes-usage", "n1", "-", "2026-09-02T13:01:00Z", 700),
	}, ""))
	library.ingestClaude(t)
	doc := testPricing()
	doc.Changes = append(doc.Changes, priceChange{Provider: "anthropic", Model: "claude-test", EffectiveFrom: "2026-01-01", Input: rate(10), Output: rate(50), Status: "confirmed", SourceURL: "https://example.com/t"})
	if err := library.catalog.replacePricing(doc, "test"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := library.catalog.refreshBranchUsage(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := library.catalog.branchUsageRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, row := range queryRows(t, library.catalog, `SELECT id,source_id FROM workspaces`) {
		ids[firstString(row["id"])] = filepath.Base(firstString(row["source_id"]))
	}
	// Rows keep the ledger's hours, for hourly trends and time ranges.
	tokens := map[string]int64{}
	for _, row := range rows {
		session := ids[firstString(row["workspace_id"])]
		if session == "s-notes-usage" {
			t.Fatalf("a folder session has spend by branch: %#v", row)
		}
		hour := strings.TrimPrefix(firstString(row["usage_at"]), "2026-09-02T")
		tokens[fmt.Sprintf("%s %v %v %v %s", session, row["branch"], row["coverage"], row["branch_source"], hour)] += integer(row["tokens"])
	}
	want := map[string]int64{
		"s-switch <nil> unknown <nil> 10:00:00Z":       1000,
		"s-switch feature/a on_branch turn 10:00:00Z":  3000,
		"s-switch feature/b on_branch turn 11:00:00Z":  2000,
		"s-switch feature/b on_branch turn 15:00:00Z":  1000,
		"s-plain <nil> no_branch_data <nil> 12:00:00Z": 500,
	}
	for key, value := range want {
		if tokens[key] != value {
			t.Fatalf("branch usage\n got %v\nwant %v", tokens, want)
		}
	}

	// Rows hold the session's whole cost: their sum is the Library's.
	search, err := library.catalog.searchRows(SearchOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	checked := false
	for _, item := range search {
		if ids[firstString(item["id"])] != "s-switch" {
			continue
		}
		checked = true
		total := 0.0
		for _, row := range rows {
			if row["workspace_id"] == item["id"] {
				total += floatOr(row["cost_usd"])
			}
		}
		// 7,000 input tokens at $10 a million.
		if math.Abs(total-floatOr(item["cost_usd"])) > 1e-9 || math.Abs(total-0.07) > 1e-9 {
			t.Fatalf("branch cost %v, library %v", total, item["cost_usd"])
		}
	}
	if !checked {
		t.Fatal("the switching session is not in the Library")
	}

	// Agents read the same numbers through get_branch_spend: per branch, and
	// a coverage split adding up to the repository sessions' whole spend.
	spend := callProjectTool(t, library.catalog, "get_branch_spend", map[string]any{"project": "app"})
	branches := map[string]string{}
	for _, item := range spend["items"].([]any) {
		row := item.(map[string]any)
		branches[firstString(row["branch"])] = fmt.Sprintf("%v %v %v", row["tokens"], row["sessions"], row["recorded"])
	}
	if branches["feature/a"] != "3000 1 [turn]" || branches["feature/b"] != "2000 1 [turn]" || len(branches) != 2 {
		t.Fatalf("get_branch_spend branches: %#v", spend)
	}
	coverage := spend["coverage"].(map[string]any)
	if integer(coverage["on_branch"].(map[string]any)["tokens"]) != 5000 || integer(coverage["unknown"].(map[string]any)["tokens"]) != 1000 ||
		integer(coverage["no_branch_data"].(map[string]any)["tokens"]) != 500 {
		t.Fatalf("get_branch_spend coverage: %#v", coverage)
	}
	if narrowed := callProjectTool(t, library.catalog, "get_branch_spend", map[string]any{"project": "app", "branch": "feature/b", "from": "2026-09-02T11:00:00Z"}); len(narrowed["items"].([]any)) != 1 {
		t.Fatalf("get_branch_spend narrowed: %#v", narrowed)
	}
	if folder := callProjectTool(t, library.catalog, "get_branch_spend", map[string]any{"project": library.folders["notes"]}); integer(folder["total"]) != 0 || len(folder["coverage"].(map[string]any)) != 0 {
		t.Fatalf("a folder has spend by branch: %#v", folder)
	}

	// Read once, the conversation is not read again until its usage changes.
	var scans int
	if err := library.catalog.DB.QueryRow(`SELECT COUNT(*) FROM branch_usage_scans`).Scan(&scans); err != nil || scans == 0 {
		t.Fatalf("scans %d %v", scans, err)
	}
	before := queryRows(t, library.catalog, `SELECT * FROM agent_session_branch_shares ORDER BY 1,2,3,4`)
	if err := library.catalog.refreshBranchUsage(ctx); err != nil {
		t.Fatal(err)
	}
	if after := queryRows(t, library.catalog, `SELECT * FROM agent_session_branch_shares ORDER BY 1,2,3,4`); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("shares changed without new usage")
	}
}

func queryRows(t *testing.T, catalog *Catalog, statement string) []map[string]any {
	t.Helper()
	rows, err := queryMaps(catalog.DB, statement)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// A derivation whose rules changed runs on the next background pass, with
// nothing new to index, and only once.
func TestToolDerivationsScheduleTheToolsStage(t *testing.T) {
	catalog, _ := testCatalog(t)
	execTx(t, catalog.DB, `INSERT INTO sync_deferred(name,generation,completed_generation,changed_at) VALUES('tools',1,1,'x')
		ON CONFLICT(name) DO UPDATE SET completed_generation=generation`)
	pending := func() int {
		return countRows(t, catalog, `SELECT generation-completed_generation FROM sync_deferred WHERE name='tools'`)
	}
	if err := catalog.scheduleToolDerivations(); err != nil || pending() != 0 {
		t.Fatalf("unchanged rules: %d pending, %v", pending(), err)
	}
	execTx(t, catalog.DB, `UPDATE meta SET value='older' WHERE key='tool_derivations_version'`)
	if err := catalog.scheduleToolDerivations(); err != nil || pending() != 0 {
		t.Fatalf("an empty library: %d pending, %v", pending(), err)
	}
	execTx(t, catalog.DB, `UPDATE meta SET value='older' WHERE key='tool_derivations_version'`,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,activity_at,indexed_at) VALUES('w','claude','local','w','w','x','x')`)
	for range 2 {
		if err := catalog.scheduleToolDerivations(); err != nil || pending() != 1 {
			t.Fatalf("changed rules: %d pending, %v", pending(), err)
		}
	}
}
