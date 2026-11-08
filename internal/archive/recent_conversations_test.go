package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestRecentConversationSelection(t *testing.T) {
	items := []recentConversation{}
	for i := range 24 {
		items = append(items, recentConversation{ID: fmt.Sprint(i), RepositoryID: "common", Provider: "codex", Model: "gpt",
			ActivityAt: time.Date(2026, 11, 8, 12-i, 0, 0, 0, time.UTC).Format(time.RFC3339)})
	}
	// Less recent variety should survive a run of similar conversations.
	for i := 12; i < 20; i++ {
		items[i].SubagentDepth = 1 + i%3
	}
	items[13].RepositoryID, items[13].Provider, items[13].Model = "other", "claude", "opus"
	items[22].LabelsReady, items[22].EventsReady = true, true
	selected := selectRecentConversations(items)
	if len(selected) != 10 || selected[0].ID != "22" {
		t.Fatalf("size/enrichment preference: %+v", selected)
	}
	seen, repos, providers, depths := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[int]bool{}
	withAgents := 0
	for _, item := range selected {
		if seen[item.ID] {
			t.Fatalf("duplicate %s", item.ID)
		}
		seen[item.ID], repos[item.RepositoryID], providers[item.Provider], depths[item.SubagentDepth] = true, true, true, true
		if item.SubagentDepth > 0 {
			withAgents++
		}
	}
	if withAgents < 5 || len(repos) < 2 || len(providers) < 2 || len(depths) < 3 || !depths[0] {
		t.Fatalf("missing variety: agents=%d repos=%v providers=%v depths=%v", withAgents, repos, providers, depths)
	}
	if !reflect.DeepEqual(selected, selectRecentConversations(items)) {
		t.Fatal("unchanged candidates should give stable results")
	}
	if got := selectRecentConversations(items[:3]); len(got) != 3 {
		t.Fatalf("small library: %+v", got)
	}
	if got := selectRecentConversations(nil); got == nil || len(got) != 0 {
		t.Fatalf("empty library should encode as []: %+v", got)
	}
	// Enrichment cannot bring an ancient conversation back into a full
	// recent pool. A small library can still fill from older history.
	items[22].ActivityAt = "2020-01-01T00:00:00Z"
	items = append(items[:22], items[22])
	for _, item := range selectRecentConversations(items) {
		if item.ID == "22" {
			t.Fatal("ancient enrichment displaced recent work")
		}
	}
	oldLibrary := []recentConversation{items[0], items[22]}
	if len(selectRecentConversations(oldLibrary)) != 2 {
		t.Fatal("older imported libraries should still have conversations")
	}
}

func TestRecentConversationsCatalog(t *testing.T) {
	catalog, config := testCatalog(t)
	tx := seedTx(t, catalog.DB)
	for i := range 8 {
		id := fmt.Sprint(i)
		for _, query := range []string{
			`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,activity_at,indexed_at) VALUES(?,'codex','local',?,'Conversation preview','2026-11-08','x')`,
			`INSERT INTO conversations(id,workspace_id,provider,account,native_id,started_at) VALUES(?1,?2,'codex','local',?1,'2026-11-08T00:00:00Z')`,
			`INSERT INTO conversation_documents(conversation_id,initiation,outcome,vector_json,model,indexed_at) VALUES(?,?,'Stored reply','[]','test','x')`,
		} {
			if _, err := tx.Exec(query, id, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	commitSeed(t, tx)
	execTx(t, catalog.DB,
		`UPDATE conversations SET parent_id='0',agent_depth=1 WHERE id='6'`,
		`INSERT INTO conversation_group_membership VALUES('7','7',0)`,
		`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,depth) VALUES('helper','1','1','helper','subagent','codex',2)`,
		`DELETE FROM conversation_documents WHERE conversation_id='5'`)
	read := func(labels, events bool) []recentConversation {
		t.Helper()
		items, err := catalog.recentConversations(context.Background(), labels, events)
		if err != nil {
			t.Fatal(err)
		}
		return items
	}
	// No optional ledgers and no raw messages are needed. Reads don't build
	// them and skip helpers, retired group members and missing previews.
	items := read(true, true)
	if len(items) != 5 {
		t.Fatalf("eligibility: %+v", items)
	}
	byID := func(items []recentConversation) map[string]recentConversation {
		result := map[string]recentConversation{}
		for _, item := range items {
			result[item.ID] = item
		}
		return result
	}
	if got := byID(items); got["0"].SubagentDepth != 1 || got["1"].SubagentDepth != 2 || got["0"].FirstInput != "0" || got["0"].LastResponse != "Stored reply" {
		t.Fatalf("stored previews and helper shapes: %+v", got)
	}
	execTx(t, catalog.DB, labelsSchemaDDL, agentEventsLedgerDDL,
		`INSERT INTO message_labels_conversations VALUES('2','s',2,'x'),('3','s',2,'x'),('4','s',0,'x')`,
		`INSERT INTO agent_events_conversations(conversation_id,structure,built_at) VALUES('2','s','x'),('3','s','x')`,
		`INSERT INTO message_labels_dirty VALUES('3','changed')`,
		`INSERT INTO agent_events_dirty VALUES('3','changed')`)
	items = read(true, true)
	if items[0].ID != "2" || !items[0].LabelsReady || !items[0].EventsReady {
		t.Fatalf("completed labels and zero-event analysis should rise first: %+v", items)
	}
	if got := byID(items); got["3"].LabelsReady || got["3"].EventsReady || got["4"].LabelsReady {
		t.Fatalf("dirty and zero-unit labels are not completed enrichment: %+v", got)
	}
	for _, item := range read(false, false) {
		if item.LabelsReady || item.EventsReady {
			t.Fatalf("disabled enrichment affected discovery: %+v", item)
		}
	}
	// Separate child transcripts (including other workspaces) contribute their
	// whole helper hierarchy. Stored session depths already include the child
	// conversation's base: don't add that base twice.
	execTx(t, catalog.DB,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth)
		 VALUES('nested','5','opencode','local','nested','6',2)`,
		`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,depth)
		 VALUES('nested-main','5','nested','main','subagent','opencode',2)`)
	if got := byID(read(false, false))["0"].SubagentDepth; got != 2 {
		t.Fatalf("nested child conversation depth=%d, want 2", got)
	}
	execTx(t, catalog.DB,
		`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,depth)
		 VALUES('nested-helper','5','nested','helper','subagent','opencode',4)`,
		`UPDATE conversations SET parent_id='0',agent_depth=20 WHERE id='7'`)
	if got := byID(read(false, false))["0"].SubagentDepth; got != 4 {
		t.Fatalf("embedded helpers on descendants (excluding retired members): depth=%d, want 4", got)
	}

	// Command-launched helpers can look like roots in conversations. Exclude
	// them before the recent limit, but follow their descendants for depth.
	execTx(t, catalog.DB,
		`UPDATE conversations SET ended_at='2099-01-01T00:00:00Z' WHERE id='4'`,
		`INSERT INTO conversation_launches VALUES('4','1','launch-call','codex')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth)
		 VALUES('launched-child','4','codex','local','launched-child','4',1)`,
		`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,depth)
		 VALUES('launched-helper','4','launched-child','helper','subagent','codex',2)`)
	if got := byID(read(false, false)); got["4"].ID != "" || got["1"].SubagentDepth != 3 {
		t.Fatalf("command-launched family: %+v", got)
	}

	// A Codex filename fork continues the same execution. It adds no level,
	// though real helpers in its transcript still belong to the original root.
	execTx(t, catalog.DB,
		`UPDATE conversations SET parent_id='2' WHERE id='3'`,
		`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,depth)
		 VALUES('continued-main','3','3','main','root','codex',0)`)
	if got := byID(read(false, false)); got["2"].SubagentDepth != 0 || got["3"].ID != "" {
		t.Fatalf("continuation became a helper: %+v", got)
	}
	execTx(t, catalog.DB,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth)
		 VALUES('continued-child','3','codex','local','continued-child','3',1)`,
		`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,depth)
		 VALUES('continued-helper','3','continued-child','helper','subagent','codex',2)`)
	if got := byID(read(false, false))["2"].SubagentDepth; got != 2 {
		t.Fatalf("helper under a continuation: depth=%d, want 2", got)
	}
	t.Run("family budget", func(t *testing.T) {
		override(t, &recentConversationFamilyLimit, 2)
		if _, err := catalog.recentConversations(t.Context(), false, false); err == nil {
			t.Fatal("a truncated family must not be presented as an exact depth")
		}
	})
	// Limit the pool in the fixture, instead of seeding a large catalog.
	override(t, &recentConversationCandidates, 2)
	if got := read(true, true); len(got) != 2 {
		t.Fatalf("bounded candidates: %+v", got)
	} else if got := byID(got); got["0"].ID == "" || got["1"].ID == "" {
		t.Fatalf("command launches consumed a recent candidate slot: %+v", got)
	}

	config.Labels.Enabled, config.AgentEvents.Enabled = true, true
	srv := testServer(t, config, catalog)
	request := httptest.NewRequest(http.MethodGet, "/api/library/recent-conversations", nil)
	request.Header.Set("Authorization", "Bearer "+config.APIToken)
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, request)
	var body struct{ Items []recentConversation }
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusOK || len(body.Items) != 2 {
		t.Fatalf("API: status=%d body=%s err=%v", response.Code, response.Body.String(), err)
	}
}
