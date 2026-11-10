package archive

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"

	qt "github.com/Pythia-Software/query-table/backends/go"
	"github.com/gbdubs/pharos/internal/querytable"
)

func TestScatterMatchesIndependentSources(t *testing.T) {
	catalog, config := testCatalog(t)
	tx := seedTx(t, catalog.DB)
	for _, sql := range []string{
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('work','claude','local','work','Work','2026-10-15')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,model) VALUES('a','work','claude','local','a','claude-sonnet-4-5'),('b','work','claude','local','b','claude-sonnet-4-5')`,
		`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,model,started_at) VALUES('s-a','work','a','main','root','claude','claude-sonnet-4-5','2026-10-15T12:00:00.000Z')`,
		`INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,attribution,input_tokens,uncached_input_tokens,total_tokens,output_tokens) VALUES('s-a','2026-10-15T12:00:00.000Z','claude-sonnet-4-5','request',100,100,110,10),('s-a','2026-10-15T13:00:00.000Z','claude-sonnet-4-5','request',200,200,220,20)`,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES('m1','a','m1','user','message','One two three','m1','2026-10-15T12:15:00.000Z'),('m2','a','m2','user','message','One two three four five','m2','2026-10-15T13:15:00.000Z'),('m3','b','m3','user','message','One two three four five six seven','m3','2026-10-15T13:15:00.000Z'),('old','a','old','user','message','Earlier words','old','2026-10-14T12:15:00.000Z'),('reply','a','reply','assistant','message','Done   with'||char(10)||'checks','reply','2026-10-15T13:16:00.000Z')`,
		`INSERT INTO message_authorship(message_id,conversation_id,workspace_id,sent_at,day,words,typed_words,total_chars,spans_json) VALUES('m1','a','work','2026-10-15T12:15:00.000Z','2026-10-15',3,3,12,'[]'),('m2','a','work','2026-10-15T13:15:00.000Z','2026-10-15',5,5,20,'[]'),('m3','b','work','2026-10-15T13:15:00.000Z','2026-10-15',7,7,28,'[]'),('old','a','work','2026-10-14T12:15:00.000Z','2026-10-14',100,100,400,'[]')`,
	} {
		if _, err := tx.Exec(sql); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, catalog)
	fields := fieldSet("human_typed", "tokens_tokens", "tokens_cost", "human_words_per_message")
	window := []querytable.WhereTerm{{Field: "last_at", Op: ">=", Value: "2026-10-15T00:00:00.000Z"}, {Field: "first_at", Op: "<", Value: "2026-10-16T00:00:00.000Z"}}
	for _, unit := range []string{"conversation", "work", "message"} {
		rows, err := server.scatterRows(context.Background(), fields, append(window, querytable.WhereTerm{Field: "point_kind", Op: "=", Value: unit}))
		if err != nil {
			t.Fatal(unit, err)
		}
		switch unit {
		case "conversation":
			if len(rows) != 2 {
				t.Fatal(rows)
			}
			a, b := rows[0], rows[1]
			cost, _ := number(a["tokens_cost"])
			if cost < 0.00134999 || cost > 0.00135001 {
				t.Fatalf("API cost was multiplied or lost: %v", cost)
			}
			if a["entity_id"] != "a" || integer(a["human_typed"]) != 8 || integer(a["tokens_tokens"]) != 330 || integer(a["human_words_per_message"]) != 4 {
				t.Fatalf("independent totals and pooled mean: %#v", rows)
			}
			if b["tokens_tokens"] != nil || integer(b["human_typed"]) != 7 {
				t.Fatalf("missing usage was invented: %#v", b)
			}
		case "work":
			if len(rows) != 1 || integer(rows[0]["human_typed"]) != 15 || integer(rows[0]["tokens_tokens"]) != 330 || integer(rows[0]["human_words_per_message"]) != 5 {
				t.Fatalf("workspace totals: %#v", rows)
			}
		case "message":
			if len(rows) != 3 {
				t.Fatal(rows)
			}
			for _, row := range rows {
				if row["tokens_tokens"] != nil {
					t.Fatal("conversation tokens were assigned to a message", row)
				}
			}
		}
	}

	// Exercise the real application source hook and grouped Query Table path,
	// including the independent source fields needed by each axis.
	schema, _ := sqliteQuerySchema("scatter")
	request := groupRequest("entity_id", qt.SummaryColumn{ID: "words", Op: "max", Field: "human_typed"}, qt.SummaryColumn{ID: "tokens", Op: "max", Field: "tokens_tokens"})
	request.Source.Where = window
	request.Result.Where = []qt.WhereTerm{{Field: "words", Op: "is_not_null"}, {Field: "tokens", Op: "is_not_null"}}
	result, _, err := server.executeQueryV3(context.Background(), "scatter", url.Values{}, schema, request)
	if err != nil || len(result.Rows) != 1 || integer(result.Rows[0]["words"]) != 8 || integer(result.Rows[0]["tokens"]) != 330 {
		t.Fatal(result.Rows, err)
	}

	// A conversation can contain a main session and an embedded helper. Even
	// when both list a message, its helper ownership must win exactly once.
	execTx(t, catalog.DB,
		`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,model,started_at) VALUES('helper','work','a','helper','subagent','claude','claude-sonnet-4-5','2026-10-15T13:20:00.000Z')`,
		`INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,attribution,input_tokens,uncached_input_tokens,total_tokens,output_tokens) VALUES('helper','2026-10-15T13:00:00.000Z','claude-sonnet-4-5','request',1000,1000,1100,100)`,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES('helper-prompt','a','helper-prompt','user','message','Six words supplied to the helper','helper-prompt','2026-10-15T13:20:00.000Z'),('helper-call','a','helper-call','assistant','tool_call','Read','helper-call','2026-10-15T13:21:00.000Z'),('main-call','a','main-call','assistant','tool_call','Read','main-call','2026-10-15T12:30:00.000Z')`,
		`INSERT INTO message_authorship(message_id,conversation_id,workspace_id,sent_at,day,words,typed_words,total_chars,spans_json) VALUES('helper-prompt','a','work','2026-10-15T13:20:00.000Z','2026-10-15',6,6,33,'[]')`,
		`INSERT INTO agent_session_messages(agent_session_id,message_id,source_order) VALUES('s-a','helper-prompt',1),('helper','helper-prompt',1),('helper','helper-call',2)`,
		`INSERT INTO tool_calls(id,workspace_id,conversation_id,agent_session_id,sequence,provider,kind,tool_name,tool_category,status,call_message_id,started_at,duration_ms) VALUES('helper-tool','work','a','helper',2,'claude','tool_call','Read','read','ok','helper-call','2026-10-15T13:21:00.000Z',200),('main-tool','work','a','s-a',1,'claude','tool_call','Read','read','ok','main-call','2026-10-15T12:30:00.000Z',100)`,
		`INSERT INTO tool_calls(id,workspace_id,conversation_id,sequence,provider,kind,tool_name,tool_category,status,started_at) VALUES('unattributed-tool','work','a',3,'claude','tool_call','Read','read','ok','2026-10-15T13:22:00.000Z')`,
		`INSERT INTO model_requests(id,conversation_id,agent_session_id,sequence,requested_at,model,input_tokens) VALUES('main-request','a','s-a',1,'2026-10-15T12:30:00.000Z','claude-sonnet-4-5',500),('helper-request','a','helper',2,'2026-10-15T13:21:00.000Z','claude-sonnet-4-5',900)`,
	)
	if err := catalog.ensureToolRollup(t.Context()); err != nil {
		t.Fatal(err)
	}
	sessionFields := fieldSet("human_typed", "tokens_tokens", "tokens_cost", "calls_calls", "session_shape_wall_seconds", "session_shape_autonomy", "session_shape_context_peak_tokens")
	sessionWindow := append(slices.Clone(window), querytable.WhereTerm{Field: "point_kind", Op: "=", Value: "agent_session"})
	rows, err := server.scatterRows(t.Context(), sessionFields, sessionWindow)
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	bySession := map[string]map[string]any{}
	for _, row := range rows {
		bySession[firstString(row["entity_id"])] = row
	}
	main, helper := bySession["s-a"], bySession["helper"]
	if integer(main["human_typed"]) != 8 || integer(main["tokens_tokens"]) != 330 || integer(main["calls_calls"]) != 1 || integer(helper["human_typed"]) != 6 || integer(helper["tokens_tokens"]) != 1100 || integer(helper["calls_calls"]) != 1 {
		t.Fatalf("session ownership was mixed or duplicated: %#v", rows)
	}
	if integer(main["session_shape_wall_seconds"]) != 90060 || integer(main["session_shape_context_peak_tokens"]) != 500 {
		t.Fatalf("own-stream shape was not paired with its recorded main session: %#v", main)
	}
	if autonomy, ok := number(main["session_shape_autonomy"]); !ok || autonomy < 0.3333 || autonomy > 0.3334 {
		t.Fatalf("main-session autonomy: %#v", main)
	}
	if helper["session_shape_wall_seconds"] != nil || helper["session_shape_context_peak_tokens"] != nil {
		t.Fatalf("main-session shape was copied onto an embedded helper: %#v", helper)
	}
	// Conflicting helper memberships are unknown. Usage and direct ledger
	// attribution remain available without inventing a word count for either.
	execTx(t, catalog.DB,
		`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider) VALUES('other-helper','work','a','other-helper','subagent','claude')`,
		`INSERT INTO agent_session_messages(agent_session_id,message_id,source_order) VALUES('other-helper','helper-prompt',1)`,
	)
	rows, err = server.scatterRows(t.Context(), fieldSet("human_typed", "tokens_tokens"), sessionWindow)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row["entity_id"] == "helper" && row["human_typed"] != nil {
			t.Fatal("ambiguous ownership was guessed", row)
		}
	}
	// Incomplete results must fail instead of joining two different truncated
	// populations. Lower the limit to keep the fixture tiny.
	override(t, &scatterMaxEntities, 1)
	_, err = server.scatterRows(context.Background(), fields, window)
	// The page shows a diagnostic's message; any other error reads as a
	// failed query.
	var limit *qt.PlanDiagnostic
	if !errors.As(err, &limit) || limit.Code != qt.ErrorCodeResourceLimit || !strings.Contains(limit.Message, "shorter range") {
		t.Fatal(err)
	}
}

func TestScatterDefinitionsUsePublishedSourceFields(t *testing.T) {
	for id, definition := range scatterDefinitions {
		schema, err := sqliteQuerySchema(definition.Dataset)
		if err != nil {
			t.Fatal(id, err)
		}
		for _, key := range definition.Keys {
			if _, ok := schema.Fields[key]; !ok {
				t.Fatalf("%s: missing identity %s", id, key)
			}
		}
		q := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Rows: true, Grouping: &qt.GroupingDefinition{Keys: []string{definition.Keys["conversation"]}, Summaries: []qt.SummaryColumn{definition.Summary}}, Source: qt.WireQuery{Where: definition.Where}, Result: qt.WireQuery{Select: []string{"value"}, Limit: 1}}
		// This validates actual released expressions and their source dependencies.
		d := queryV3Dataset(schema)
		d.SourceSQL = (sqlDataset{}).querySourceSQL(schema)
		if _, err := d.SourcePlan(context.Background(), q, qt.PlanOptions{}); err != nil {
			t.Fatal(id, err)
		}
	}
}
