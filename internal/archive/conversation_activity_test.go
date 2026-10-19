package archive

import (
	"context"
	"testing"

	"github.com/gbdubs/pharos/internal/querytable"
)

func TestConversationActivityPreservesHumanGrainAndTime(t *testing.T) {
	catalog, config := testCatalog(t)
	run := func(sql string, args ...any) {
		t.Helper()
		if _, err := catalog.DB.Exec(sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	run(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('work','codex','local','work','Work','2026-10-15')`)
	for _, cv := range []struct {
		id, parent string
		depth      int
	}{{"root", "", 0}, {"child", "root", 1}, {"unclassified", "", 0}, {"human-only", "", 0}, {"orphan", "", 1}} {
		run(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth,model,effort) VALUES(?,'work','codex','local',?,?,?,'gpt-6-sol','high')`, cv.id, cv.id, nilIfEmpty(cv.parent), cv.depth)
		run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,model,started_at) VALUES(?,'work',?,'main','root','codex','gpt-6-sol','2026-10-14T12:00:00Z')`, "s-"+cv.id, cv.id)
	}
	for _, fact := range []struct {
		session, at string
		out         int
	}{{"root", "2026-10-14T12:00:00Z", 10}, {"root", "2026-10-15T12:30:00Z", 20}, {"root", "2026-10-15T13:00:00Z", 30}, {"child", "2026-10-15T13:00:00Z", 7}, {"unclassified", "2026-10-15T13:00:00Z", 2}, {"orphan", "2026-10-15T13:00:00Z", 99}} {
		run(`INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,effort,attribution,input_tokens,total_tokens,output_tokens) VALUES(?,?,'unpriced-child','low','request',100,?,?)`, "s-"+fact.session, fact.at, 100+fact.out, fact.out)
	}
	for _, message := range []struct {
		id, cv, at                string
		chars, harness, automated int
	}{{"mixed", "root", "2026-10-15T12:15:00Z", 12, 4, 2}, {"paste", "root", "2026-10-15T13:05:00Z", 4, 0, 0}, {"old", "root", "2026-10-14T12:15:00Z", 8, 0, 0}, {"machine", "root", "2026-10-15T12:00:00Z", 8, 8, 0}, {"human-only", "human-only", "2026-10-15T12:00:00Z", 8, 0, 0}, {"orphan", "orphan", "2026-10-15T12:00:00Z", 100, 0, 0}} {
		run(`INSERT INTO message_authorship(message_id,conversation_id,workspace_id,sent_at,day,total_chars,harness_chars,automated_chars,spans_json) VALUES(?,?,'work',?,'2026-10-15',?,?,?,'[]')`, message.id, message.cv, message.at, message.chars, message.harness, message.automated)
	}
	rows, err := catalog.conversationActivityRows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	schema, err := testServer(t, config, catalog).queryTableSchema("conversation_activity")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := querytable.Filter(rows, []querytable.WhereTerm{{Field: "event_at", Op: ">=", Value: "2026-10-15T00:00:00Z"}, {Field: "event_at", Op: "<", Value: "2026-10-16T00:00:00Z"}}, schema)
	if err != nil {
		t.Fatal(err)
	}
	totals := map[string][3]int64{}
	for _, row := range selected {
		root := firstString(row["root_conversation_id"])
		total := totals[root]
		total[0] += integer(row["human_input_tokens_estimate"])
		total[1] += integer(row["output_tokens"])
		total[2] += integer(row["human_messages"])
		totals[root] = total
		if root == "root" && (row["root_model_family"] != "gpt-6-sol" || row["root_effort"] != "high") {
			t.Fatalf("child output changed root cohort: %#v", row)
		}
	}
	if totals["root"] != [3]int64{3, 57, 2} {
		t.Fatalf("human messages must count once with their own timestamp, regardless of pricing: %v", totals)
	}
	if totals["human-only"] != [3]int64{2, 0, 1} || totals["unclassified"] != [3]int64{0, 2, 0} || len(totals) != 3 {
		t.Fatalf("missing output/classification must stay inspectable and orphan lineage excluded: %v", totals)
	}
}
