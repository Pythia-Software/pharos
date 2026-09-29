package archive

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// detectorCall is one tool call for a detector fixture.
type detectorCall struct {
	category, tool, program, subcommand, command, path, status, signature string
	resultTokens, carried                                                 int64
}

// addConversation writes one top-level conversation with its calls.
func (fixture *findingFixture) addConversation(day time.Time, provider string, calls []detectorCall, requests, compactions, peak int) string {
	fixture.t.Helper()
	fixture.next++
	id := fmt.Sprint(fixture.next)
	exec := func(statement string, args ...any) {
		fixture.t.Helper()
		if _, err := fixture.catalog.DB.Exec(statement, args...); err != nil {
			fixture.t.Fatal(err)
		}
	}
	started := day.Add(9 * time.Hour)
	exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,repository_id,title,indexed_at) VALUES(?,?,?,?,?,?,?)`,
		"ws-"+id, provider, "local", "source-"+id, "repo-1", "Work "+id, formatTime(started))
	exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,started_at) VALUES(?,?,?,?,?,?)`,
		"conv-"+id, "ws-"+id, provider, "local", "native-"+id, formatTime(started))
	for index, call := range calls {
		status := defaultString(call.status, "ok")
		exec(`INSERT INTO tool_calls(id,workspace_id,conversation_id,sequence,provider,kind,tool_name,tool_category,command,program,subcommand,file_path,repo_path,
			path_repository_id,started_at,status,error_signature,test_failure,result_tokens,carried_tokens) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,?,?)`,
			fmt.Sprintf("call-%s-%d", id, index), "ws-"+id, "conv-"+id, index, provider, "tool_call", defaultString(call.tool, "Bash"), defaultString(call.category, "command"),
			nilIfEmpty(call.command), nilIfEmpty(call.program), nilIfEmpty(call.subcommand), nilIfEmpty(call.path), nilIfEmpty(call.path), map[bool]any{true: "repo-1", false: nil}[call.path != ""],
			formatTime(started.Add(time.Duration(index)*time.Minute)), status, nilIfEmpty(call.signature), call.resultTokens, call.carried)
	}
	for request := range requests {
		exec(`INSERT INTO model_requests(id,conversation_id,sequence,input_tokens,compacted_before) VALUES(?,?,?,?,?)`,
			fmt.Sprintf("req-%s-%d", id, request), "conv-"+id, request, map[bool]int{true: peak, false: 1000}[request == 0], map[bool]int{true: 1, false: 0}[request < compactions])
	}
	return "conv-" + id
}

// monthOf calls add for each of the last 28 days.
func (fixture *findingFixture) monthOf(now time.Time, add func(day time.Time, index int)) {
	for back := 27; back >= 0; back-- {
		add(dayTime(now.Format("2006-01-02")).AddDate(0, 0, -back), 27-back)
	}
}

func findingsBy(t *testing.T, catalog *Catalog, detector string) map[string]*findingRow {
	t.Helper()
	if err := catalog.RefreshFindings(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	rows, err := loadFindingRows(context.Background(), catalog.DB, "WHERE detector=?", detector)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestDriftDetector(t *testing.T) {
	now := time.Date(2026, 9, 28, 15, 0, 0, 0, time.Local)
	fixture := newFindingFixture(t, now)
	fixture.monthOf(now, func(day time.Time, index int) {
		calls := []detectorCall{{program: "go", subcommand: "test", command: "go test ./..."}}
		if index%3 == 0 {
			calls = append([]detectorCall{{program: "rg", command: "rg --files -g 'AGENTS.md'", status: "error"}, {program: "cat", command: "cat CLAUDE.md"}}, calls...)
		}
		fixture.addConversation(day, "codex", calls, 3, 0, 1000)
		fixture.addConversation(day, "claude", []detectorCall{{program: "go", command: "go test ./..."}}, 3, 0, 1000)
	})
	rows := findingsBy(t, fixture.catalog, "drift")
	var codex *findingRow
	for _, row := range rows {
		if row.Pattern == "provider:codex" {
			codex = row
		}
		if row.Pattern == "provider:claude" && row.Stats.Affected > 0 {
			t.Fatalf("Claude read nothing of Codex's: %+v", row.Stats)
		}
	}
	if codex == nil || codex.Stats.Affected != 10 || codex.Stats.Exposure != 28 {
		t.Fatalf("codex drift: %+v", codex)
	}
	if !strings.Contains(codex.Card.Title, "Codex can't find alpha's instructions") || !strings.Contains(codex.Card.Explanation, "searching for AGENTS.md and reading CLAUDE.md by hand") {
		t.Fatalf("wording: %q / %q", codex.Card.Title, codex.Card.Explanation)
	}
	if codex.Lever != "repo-tooling" || len(codex.Card.Steps) < 2 {
		t.Fatalf("lever %q steps %v", codex.Lever, codex.Card.Steps)
	}
}

func TestCLIFrictionDetector(t *testing.T) {
	now := time.Date(2026, 9, 28, 15, 0, 0, 0, time.Local)
	fixture := newFindingFixture(t, now)
	fixture.monthOf(now, func(day time.Time, index int) {
		calls := []detectorCall{{program: "deployctl", command: "deployctl status"}}
		if index%2 == 0 {
			calls = append([]detectorCall{{program: "deployctl", command: "deployctl --help"}, {program: "deployctl", command: "deployctl push --fast", status: "error", signature: "error: unknown flag: --fast"}}, calls...)
		}
		fixture.addConversation(day, "claude", calls, 3, 0, 1000)
	})
	rows := findingsBy(t, fixture.catalog, "cli")
	var row *findingRow
	for _, item := range rows {
		if item.Pattern == "program:deployctl" {
			row = item
		}
	}
	if row == nil || row.Stats.Affected != 14 || row.Metric.Unit != "engagements" || !row.Metric.GuardSuccess {
		t.Fatalf("deployctl friction: %+v", row)
	}
	if !strings.Contains(row.Card.Explanation, "read its help 14 times and called it wrongly 14 times") || row.Lever != "repo-tooling" {
		t.Fatalf("wording %q lever %q", row.Card.Explanation, row.Lever)
	}
	// General tools are nobody's to fix.
	for _, item := range rows {
		if item.Pattern == "program:go" || item.Pattern == "program:git" {
			t.Fatalf("general tool found: %s", item.Pattern)
		}
	}
}

func TestHeavyOutputAndOutlierDetectors(t *testing.T) {
	now := time.Date(2026, 9, 28, 15, 0, 0, 0, time.Local)
	fixture := newFindingFixture(t, now)
	fixture.monthOf(now, func(day time.Time, index int) {
		calls := []detectorCall{{program: "sed", command: "sed -n '1,120p' main.go", resultTokens: 900}}
		if index%2 == 0 {
			calls = append(calls, detectorCall{program: "sed", command: "sed -n '1,900p' main.go", resultTokens: 9000, carried: 90000})
		}
		requests, compactions := 20, 0
		if index%4 == 0 {
			requests, compactions = 1200, 12
		}
		fixture.addConversation(day, "codex", calls, requests, compactions, 1000)
	})
	heavy := findingsBy(t, fixture.catalog, "heavy-output")
	var sed *findingRow
	for _, row := range heavy {
		if strings.HasPrefix(row.Pattern, "shape:long-range:") {
			sed = row
		}
	}
	if sed == nil || sed.Stats.Affected != 14 || sed.Metric.Kind != "mean" || !strings.Contains(sed.Card.Title, "reads files hundreds of lines at a time") {
		t.Fatalf("long sed reads: %+v", sed)
	}
	outliers, _ := loadFindingRows(context.Background(), fixture.catalog.DB, "WHERE detector='outlier'")
	var runaway *findingRow
	for _, row := range outliers {
		if row.Pattern == "cause:runaway" {
			runaway = row
		}
	}
	if runaway == nil || runaway.Stats.Affected != 7 || runaway.Metric.Kind != "share" {
		t.Fatalf("runaway conversations: %+v", runaway)
	}
}

func TestOrientationDetector(t *testing.T) {
	now := time.Date(2026, 9, 28, 15, 0, 0, 0, time.Local)
	fixture := newFindingFixture(t, now)
	fixture.monthOf(now, func(day time.Time, index int) {
		calls := []detectorCall{{category: "read", tool: "Read", path: "README.md", resultTokens: 500}}
		if index%2 == 0 {
			calls = append(calls, detectorCall{category: "read", tool: "Read", path: "src/big_helpers.rs", resultTokens: 20000})
		}
		calls = append(calls, detectorCall{category: "edit", tool: "Edit", path: "src/lib.rs"})
		fixture.addConversation(day, "claude", calls, 5, 0, 1000)
	})
	rows := findingsBy(t, fixture.catalog, "orientation")
	if len(rows) != 1 {
		t.Fatalf("orientation findings: %d", len(rows))
	}
	for _, row := range rows {
		if row.Stats.Affected != 14 || !strings.Contains(row.Card.Title, "`big_helpers.rs`") {
			t.Fatalf("orientation: %+v %q", row.Stats, row.Card.Title)
		}
		hot := row.Facts["hot_files"].([]any)
		if len(hot) != 1 || mapValueDefault(hot[0])["path"] != "src/big_helpers.rs" {
			t.Fatalf("README is expected reading, not a hot file: %v", hot)
		}
	}
}

func TestFindingsFollowRepositoryMerges(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 28, 15, 0, 0, 0, time.Local)
	fixture := newFindingFixture(t, now)
	for back := 27; back >= 0; back-- {
		fixture.conversation(dayTime(now.Format("2006-01-02")).AddDate(0, 0, -back), back%2 == 0)
	}
	catalog := fixture.catalog
	rows := findingsBy(t, catalog, "failure")
	oldID := ""
	for id := range rows {
		oldID = id
	}
	if err := catalog.FindingAction(ctx, oldID, map[string]any{"action": "dismiss", "reason": "wont_fix"}); err != nil {
		t.Fatal(err)
	}
	// repo-1 merges into repo-2.
	if _, err := catalog.DB.Exec(`INSERT INTO repositories(id,display_name,created_at,updated_at) VALUES('repo-2','alpha',?,?)`, formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	group := repositoryMergeGroup{Name: "alpha", Survivor: repositoryIdentity{ID: "repo-2", Name: "alpha"}, Losers: []repositoryIdentity{{ID: "repo-1", Name: "alpha"}}}
	if err := catalog.mergeRepositoryGroup(ctx, group); err != nil {
		t.Fatal(err)
	}
	rows = findingsBy(t, catalog, "failure")
	if len(rows) != 1 {
		retired, _ := queryMaps(catalog.DB, "SELECT * FROM repository_retirements")
		for id, row := range rows {
			t.Logf("%s active=%v", id, row.Active)
		}
		t.Fatalf("one finding after the merge: %d (retired %v)", len(rows), retired)
	}
	for id := range rows {
		if !strings.HasPrefix(id, "failure:repo:repo-2:") {
			t.Fatalf("the finding follows the survivor: %s", id)
		}
		if detail, _ := catalog.FindingDetail(ctx, id); detail["state"] != findingDismissed {
			t.Fatalf("the dismissal follows it through its alias: %v", detail["state"])
		}
		if detail, _ := catalog.FindingDetail(ctx, oldID); detail == nil || detail["id"] != id {
			t.Fatal("the old ID resolves to the new finding")
		}
	}
}
