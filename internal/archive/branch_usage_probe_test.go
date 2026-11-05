//go:build probe

// Times spend by branch's first pass on a real library and reports its
// coverage split, from a catalog backup opened immutable and read-only. It
// reads what refreshBranchUsage reads and derives the same shares, but
// writes nothing:
//
//	PHAROS_BRANCH_SOURCE="/Volumes/euclid/Pharos Backup …/catalog/catalog.sqlite3" PHAROS_TEST_PROCESSES=1 \
//	  go test -tags probe -run TestBranchUsageProbe -v -timeout 60m ./internal/archive
//
// A backup older than projects names repositories by repository_id only.

package archive

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestBranchUsageProbe(t *testing.T) {
	path := os.Getenv("PHAROS_BRANCH_SOURCE")
	if path == "" {
		t.Skip("set PHAROS_BRANCH_SOURCE to a catalog backup")
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String()+"?immutable=1&mode=ro&_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	started := time.Now()
	conversations, err := queryMapsContext(ctx, db, `SELECT c.id FROM conversations c JOIN workspaces w ON w.id=c.workspace_id
		WHERE c.provider='claude' AND w.repository_id IS NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d Claude conversations in repositories, listed in %s", len(conversations), time.Since(started).Round(time.Millisecond))
	started = time.Now()
	shares := map[string]map[branchShareKey]map[string]int64{}
	branched, events, slowest := 0, 0, time.Duration(0)
	for index, row := range conversations {
		one := time.Now()
		rows, err := queryMapsContext(ctx, db, `SELECT am.agent_session_id,m.created_at,m.model,m.raw_text FROM messages m
			JOIN agent_session_messages am ON am.message_id=m.id
			WHERE m.conversation_id=? AND m.raw_text IS NOT NULL
			ORDER BY m.source_order IS NULL,m.source_order,m.created_at,m.id`, row["id"])
		if err != nil {
			t.Fatal(err)
		}
		read := make([]branchEvent, 0, len(rows))
		for _, event := range rows {
			read = append(read, branchEvent{Session: firstString(event["agent_session_id"]), CreatedAt: firstString(event["created_at"]),
				Model: firstString(event["model"]), Raw: firstString(event["raw_text"])})
		}
		events += len(read)
		if found := branchShares(read); found != nil {
			branched++
			for key, branches := range found {
				if shares[key.Session] == nil {
					shares[key.Session] = map[branchShareKey]map[string]int64{}
				}
				shares[key.Session][key] = branches
			}
		}
		slowest = max(slowest, time.Since(one))
		if (index+1)%2000 == 0 {
			t.Logf("%d/%d read in %s", index+1, len(conversations), time.Since(started).Round(time.Second))
		}
	}
	t.Logf("read %d events, %d conversations recorded a branch, in %s (slowest conversation %s)", events, branched, time.Since(started).Round(time.Second), slowest.Round(time.Millisecond))

	// The coverage split of repository sessions' tokens, as the dataset
	// splits them (tokens rather than cost: pricing needs the current schema).
	ledger, err := queryMapsContext(ctx, db, `SELECT u.agent_session_id,u.usage_hour,u.model,u.attribution,u.total_tokens,w.branch
		FROM agent_session_usage u JOIN agent_sessions a ON a.id=u.agent_session_id JOIN workspaces w ON w.id=a.workspace_id
		WHERE w.repository_id IS NOT NULL AND u.total_tokens>0`)
	if err != nil {
		t.Fatal(err)
	}
	coverage := map[string]int64{}
	for _, entry := range ledger {
		session, tokens := firstString(entry["agent_session_id"]), integer(entry["total_tokens"])
		turns := shares[session]
		switch {
		case turns == nil && firstString(entry["branch"]) != "":
			coverage["on_branch (session)"] += tokens
		case turns == nil:
			coverage["no_branch_data"] += tokens
		default:
			split := turns[branchShareKey{session, firstString(entry["usage_hour"]), firstString(entry["model"])}]
			if firstString(entry["attribution"]) != "request" || len(split) == 0 {
				coverage["unknown (unplaced)"] += tokens
				continue
			}
			total := int64(0)
			for _, used := range split {
				total += used
			}
			for branch, used := range split {
				name := "on_branch (turn)"
				if branch == "" {
					name = "unknown (before first / detached)"
				}
				coverage[name] += int64(float64(tokens) * float64(used) / float64(total))
			}
		}
	}
	all := int64(0)
	for _, tokens := range coverage {
		all += tokens
	}
	for name, tokens := range coverage {
		t.Logf("%-36s %16d tokens %5.1f%%", name, tokens, 100*float64(tokens)/float64(all))
	}
}
