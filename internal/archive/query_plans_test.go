package archive

import (
	"strings"
	"testing"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

// TestQueryPlansUseIndexes checks that the product's own queries keep the
// plans that make them fast on a large catalog: which index serves them, and
// which they must not walk. These are performance guards on SQLite's planner,
// so expect to revisit a row when SQLite or modernc.org/sqlite is upgraded.
// Every query is the product's: a test-written copy of one could not catch a
// regression in it.
func TestQueryPlansUseIndexes(t *testing.T) {
	catalog, _ := testCatalog(t)
	type plan struct {
		name  string
		query string
		args  []any
		want  []string // each must appear in the plan
		avoid []string // none may appear in the plan
	}
	plans := []plan{
		{name: "authorship inputs", query: authorshipInputsQuery, want: []string{"messages_prose_idx"}, avoid: []string{"TEMP B-TREE"}},
		{name: "authorship replies", query: authorshipRepliesQuery, args: []any{"2026-01-01"}, want: []string{"messages_prose_idx"}, avoid: []string{"TEMP B-TREE"}},
		// A conversation's own messages come from its index, not from every
		// prose message in the catalog.
		{name: "document initiation", query: documentInitiationQuery, args: []any{"x"}, want: []string{"(conversation_id=?)"}, avoid: []string{"messages_prose_idx"}},
		{name: "document outcome", query: documentOutcomeQuery, args: []any{"x"}, want: []string{"(conversation_id=?)"}, avoid: []string{"messages_prose_idx"}},
		{name: "library previews", query: libraryPreviewSelect("w.id=?"), args: []any{"x"}, want: []string{"(conversation_id=?)"}, avoid: []string{"messages_prose_idx"}},
		// The Library projection reads effort and model pairs from the
		// covering message index, not from message rows.
		{name: "library projection", query: libraryDerivedSelect("w.id=?"), args: []any{"x"},
			want: []string{"(conversation_id=?)", "COVERING INDEX messages_conversation_model_effort_idx"}, avoid: []string{"messages_prose_idx"}},
		{name: "native alias lookup", query: nativeAliasMatchQuery, args: []any{"local", "session", "wrapper"}, want: []string{"conversations_native_alias_idx"}, avoid: []string{"SCAN conversations"}},
		{name: "conversation children", query: childConversationQuery, args: []any{"x"}, want: []string{"COVERING INDEX conversations_parent_idx"}},
		{name: "usage summary authorship", query: usageSummaryAuthorshipSQL, want: []string{"COVERING INDEX message_authorship_summary_idx"}},
		{name: "usage summary ledger", query: usageSummaryLedgerSQL, want: []string{"COVERING INDEX agent_sessions_summary_idx", "COVERING INDEX workspaces_library_idx"}},
	}
	for _, column := range libraryColumns {
		if column.name == "model_efforts" {
			plans = append(plans, plan{name: "library model and effort pairs", query: "SELECT " + column.expr + " FROM workspaces w WHERE w.id=?", args: []any{"x"},
				want: []string{"COVERING INDEX messages_conversation_model_effort_idx"}})
		}
	}
	for _, setting := range []struct{ model, effort string }{{"opus-5-5", "high"}, {"opus-5-5", ""}, {"", "high"}} {
		label := setting.model + "/" + setting.effort
		work, args := workspaceConfigurationFilter(setting.model, setting.effort)
		plans = append(plans, plan{name: "work filter " + label, query: "SELECT w.id FROM workspaces w WHERE " + work, args: args,
			want: []string{"COVERING INDEX messages_conversation_model_effort_idx"}})
		conversations, args := conversationConfigurationFilter(setting.model, setting.effort)
		plans = append(plans, plan{name: "conversation filter " + label, query: "SELECT c.id FROM conversations c WHERE " + conversations, args: args,
			want: []string{"COVERING INDEX messages_conversation_model_effort_idx"}})
	}
	schema, err := sqliteQuerySchema("tool_calls")
	if err != nil {
		t.Fatal(err)
	}
	search := toolCallDataset
	search.toolSearchIndexed = true
	source, args := search.v2IndexedSource(schema, []qt.WhereTerm{{Field: "command", Op: "contains", Value: "go test"}})
	plans = append(plans,
		plan{name: "command search", query: "SELECT count(*) FROM (" + source + ")", args: args, want: []string{"VIRTUAL TABLE INDEX"}},
		// A projection joins only the tables its fields need.
		plan{name: "tool calls without session fields", query: "SELECT count(*) FROM (" + toolCallDataset.project([]string{"id", "started_at", "provider"}).v2Source(schema) + ")",
			avoid: []string{"agent_sessions", "repositories"}},
		plan{name: "tool calls with session fields", query: "SELECT count(*) FROM (" + toolCallDataset.project([]string{"repository_name", "session_kind"}).v2Source(schema) + ")",
			want: []string{"agent_sessions", "repositories"}},
	)
	for _, plan := range plans {
		t.Run(plan.name, func(t *testing.T) {
			rows, err := queryMaps(catalog.DB, "EXPLAIN QUERY PLAN "+plan.query, plan.args...)
			if err != nil {
				t.Fatal(err)
			}
			steps := []string{}
			for _, row := range rows {
				steps = append(steps, firstString(row["detail"]))
			}
			text := strings.Join(steps, "\n")
			for _, want := range plan.want {
				if !strings.Contains(text, want) {
					t.Errorf("plan lacks %q:\n%s", want, text)
				}
			}
			for _, avoid := range plan.avoid {
				if strings.Contains(text, avoid) {
					t.Errorf("plan uses %q:\n%s", avoid, text)
				}
			}
		})
	}
}

// TestInitializeAddsPlanIndexesToExistingCatalogs: catalogs created before
// an index existed get it on Initialize, not only new installs.
func TestInitializeAddsPlanIndexesToExistingCatalogs(t *testing.T) {
	catalog, _ := testCatalog(t)
	indexes := []string{"conversations_parent_idx", "message_authorship_summary_idx", "agent_sessions_summary_idx"}
	for _, index := range indexes {
		if _, err := catalog.DB.Exec("DROP INDEX " + index); err != nil {
			t.Fatal(err)
		}
	}
	if err := catalog.Initialize(); err != nil {
		t.Fatal(err)
	}
	for _, index := range indexes {
		var found int
		if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?", index).Scan(&found); err != nil || found != 1 {
			t.Errorf("Initialize did not restore %s: %d %v", index, found, err)
		}
	}
	execTx(t, catalog.DB,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','codex','local','w','Work','x')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('cb','w','codex','local','parent'),('cc','w','codex','local','child')`,
		`UPDATE conversations SET parent_id='cb' WHERE id='cc'`)
	for id, want := range map[string]bool{"cb": true, "cc": false, "missing": false} {
		var got bool
		if err := catalog.DB.QueryRow(childConversationQuery, id).Scan(&got); err != nil || got != want {
			t.Fatalf("children of %s: %v, %v; want %v", id, got, err, want)
		}
	}
}
