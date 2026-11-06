package archive

import (
	"encoding/json"
	"net/http"
	"testing"

	qt "github.com/Pythia-Software/query-table/backends/go"
	"github.com/gbdubs/pharos/internal/querytable"
)

func TestCompactionEvents(t *testing.T) {
	c, config := testCatalog(t)
	seedToolCalls(t, c, 0)
	tx := seedTx(t, c.DB)
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE conversations SET provider='codex',harness='codex' WHERE id='conv-1'`)
	exec(`INSERT INTO conversation_group_membership(conversation_id,workspace_id,active) VALUES('conv-3','ws-3',0)`)
	exec(`INSERT INTO conversation_origins(conversation_id,channel,machine_id,machine) VALUES('conv-0','conductor','host-a','My Mac')`)
	events := []struct{ id, conversation, body, at string }{
		{"manual", "conv-0", `{"subtype":"compact_boundary","compactMetadata":{"trigger":"manual"}}`, "2026-11-01T12:00:00Z"},
		{"auto", "conv-0", `{"subtype":"compact_boundary","compactMetadata":{"trigger":"auto"}}`, "2026-11-01T12:01:00Z"},
		{"unknown", "conv-0", `{"subtype":"compact_boundary"}`, "2026-11-02T12:00:00Z"},
		{"no-clock", "conv-0", `{"subtype":"compact_boundary","compactMetadata":{"trigger":"other"}}`, ""},
		{"codex-manual", "conv-1", `{"type":"compacted","compaction_trigger":"manual"}`, "2026-11-01T12:02:00Z"},
		{"codex-auto", "conv-1", `{"type":"compacted","compaction_trigger":"auto"}`, "2026-11-02T12:03:00Z"},
		{"replayed", "conv-1", `{"type":"compacted"}`, "2026-11-01T12:04:00Z"},
		{"inactive", "conv-3", `{"subtype":"compact_boundary"}`, "2026-11-01T12:05:00Z"},
		{"prose", "conv-0", `not JSON`, "2026-11-01T12:06:00Z"},
	}
	for i, e := range events {
		exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES(?,?,?,'system','metadata',?,'hash',?,?)`, e.id, e.conversation, e.id, e.body, e.at, i)
	}
	commitSeed(t, tx)
	// No model request is needed; consecutive and terminal boundaries count.
	document, err := querySchemaDocument("compactions")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := querytable.LoadSchema(document)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := compactionDataset.Rows(t.Context(), c.reader(), querytable.Query{Select: []string{"id", "trigger", "trigger_evidence", "channel", "machine_id"}, Limit: 20}, schema)
	if err != nil || rows.Total != 6 {
		t.Fatalf("events: %+v / %v", rows, err)
	}
	counts := map[string]int{}
	for _, row := range rows.Rows {
		counts[firstString(row["trigger"])]++
		if row["id"] == "manual" && (row["trigger_evidence"] != "recorded" || row["channel"] != "conductor" || row["machine_id"] != "host-a") {
			t.Fatal(row)
		}
		if row["id"] == "codex-auto" && row["trigger_evidence"] != "inferred" {
			t.Fatal(row)
		}
	}
	for _, trigger := range []string{"human", "machine", "unknown"} {
		if counts[trigger] != 2 {
			t.Fatal(counts)
		}
	}
	server := testServer(t, config, c)
	query := groupRequest("trigger", qt.SummaryColumn{ID: "events", Op: "count"})
	query.Source.Where = []qt.WhereTerm{{Field: "provider", Op: "=", Value: "codex"}}
	w := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/compactions/query-v3", query)
	var result qt.QueryResultV3
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.SourceTotal != 2 || len(result.Rows) != 2 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := apiCall(t, server, "", http.MethodGet, "/api/query/compactions/capabilities", nil); w.Code != 401 {
		t.Fatal(w.Code)
	}
	t.Run("native aliases without tool calls", func(t *testing.T) {
		catalog, config := testCatalog(t)
		seedToolCalls(t, catalog, 0)
		tx := seedTx(t, catalog.DB)
		exec := func(statement string, args ...any) {
			t.Helper()
			if _, err := tx.Exec(statement, args...); err != nil {
				t.Fatal(err)
			}
		}
		exec(`UPDATE workspaces SET source_kind=CASE id WHEN 'ws-0' THEN 'claude' WHEN 'ws-3' THEN 'claude' ELSE 'conductor' END`)
		// Two mirrored transcripts form a transitive alias family. A native
		// transcript with no boundaries must not hide the family's actual events.
		exec(`INSERT INTO conversation_identity_links(left_id,right_id,relationship,confidence,evidence_json) VALUES
   ('conv-0','conv-1','native-alias',1,'{}'),('conv-2','conv-1','native-alias',1,'{}'),('conv-3','conv-2','native-alias',1,'{}')`)
		exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('independent','ws-1','claude','local','independent')`)
		exec(`INSERT INTO conversation_identity_links(left_id,right_id,relationship,confidence,evidence_json) VALUES('conv-0','independent','continuation',1,'{}')`)
		for _, conv := range []string{"conv-0", "conv-1", "conv-2", "independent"} {
			for i, trigger := range []string{"manual", "auto"} {
				if conv == "independent" && i > 0 {
					continue
				}
				stamp := "2026-11-01T12:00:00Z"
				if conv == "conv-1" || conv == "conv-2" {
					stamp = "2026-11-02T12:00:00Z"
				}
				exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES(?,?,?,'system','metadata',?,'hash',?,?)`, conv+trigger, conv, trigger, `{"subtype":"compact_boundary","compactMetadata":{"trigger":"`+trigger+`"}}`, stamp, i)
			}
		}
		commitSeed(t, tx)
		read := func(where []querytable.WhereTerm) map[string]bool {
			t.Helper()
			result, err := compactionDataset.Rows(t.Context(), catalog.reader(), querytable.Query{Select: []string{"id", "conversation_id"}, Where: where, Limit: 20}, schema)
			if err != nil {
				t.Fatal(err)
			}
			ids := map[string]bool{}
			for _, row := range result.Rows {
				ids[firstString(row["id"])] = true
			}
			return ids
		}
		expectNative := func() {
			t.Helper()
			got := read(nil)
			if len(got) != 3 || !got["conv-0manual"] || !got["conv-0auto"] || !got["independentmanual"] {
				t.Fatalf("native family and unrelated conversation: %v", got)
			}
		}
		expectNative()
		if err := catalog.ensureToolRollup(t.Context()); err != nil {
			t.Fatal(err)
		}
		expectNative()
		server := testServer(t, config, catalog)
		query := groupRequest("trigger", qt.SummaryColumn{ID: "events", Op: "count"})
		w := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/compactions/query-v3", query)
		var grouped qt.QueryResultV3
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &grouped) != nil || grouped.SourceTotal != 3 || len(grouped.Rows) != 2 {
			t.Fatal(w.Code, w.Body.String())
		}
		for _, row := range grouped.Rows {
			want := int64(1)
			if row["trigger"] == "human" {
				want = 2
			}
			if integer(row["events"]) != want {
				t.Fatalf("compaction chart totals: %v", grouped.Rows)
			}
		}
		if got := read([]querytable.WhereTerm{{Field: "source_kind", Op: "=", Value: "conductor"}}); len(got) != 1 || !got["independentmanual"] {
			t.Fatalf("source filters must not resurrect mirrors: %v", got)
		}
		if got := read([]querytable.WhereTerm{{Field: "occurred_at", Op: ">=", Value: "2026-11-02T00:00:00Z"}}); len(got) != 0 {
			t.Fatalf("date filters must follow representative selection: %v", got)
		}
		execTx(t, catalog.DB, `INSERT INTO conversation_group_membership(conversation_id,workspace_id,active) VALUES('conv-0','ws-0',0)`)
		if got := read(nil); len(got) != 3 || !got["conv-2manual"] || !got["conv-2auto"] || !got["independentmanual"] {
			t.Fatalf("inactive native falls back to an active boundary transcript: %v", got)
		}
	})
}
