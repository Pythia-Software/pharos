//go:build probe

// Replays the audited single-Haiku mismatch cohort in a disposable SQLite
// catalog. The source is attached with mode=ro; no initialization or source
// acquisition runs. PHAROS_SESSION_MODEL_CATALOG=/path/catalog.sqlite3
// go test -tags probe -run '^TestSessionModelCohortReplay$' -v -timeout 20m ./internal/archive
package archive

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestSessionModelCohortReplay(t *testing.T) {
	path := os.Getenv("PHAROS_SESSION_MODEL_CATALOG")
	if path == "" {
		t.Skip("set PHAROS_SESSION_MODEL_CATALOG")
	}
	uri, err := auditCatalogURI(path)
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), "cohort.sqlite3")
	db, err := sql.Open("sqlite", copyPath)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err = db.Exec(`ATTACH DATABASE ? AS audit`, uri); err != nil {
		t.Fatal(err)
	}
	copyTx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer copyTx.Rollback()
	for _, statement := range []string{
		`CREATE TEMP TABLE cohort AS WITH u AS (SELECT agent_session_id,COUNT(DISTINCT model) n,MIN(model) model FROM audit.agent_session_usage WHERE total_tokens>0 GROUP BY 1)
		SELECT s.id FROM audit.agent_sessions s JOIN u ON u.agent_session_id=s.id WHERE s.kind='subagent' AND u.n=1 AND u.model LIKE '%haiku%' AND COALESCE(s.model,'') NOT LIKE '%haiku%'`,
		`CREATE TABLE agent_sessions AS SELECT * FROM audit.agent_sessions WHERE id IN (SELECT id FROM cohort)`,
		`CREATE TABLE conversations AS SELECT * FROM audit.conversations WHERE id IN (SELECT conversation_id FROM agent_sessions)`,
		`CREATE UNIQUE INDEX session_id ON agent_sessions(id)`,
		`CREATE UNIQUE INDEX conversation_id ON conversations(id)`,
		`CREATE INDEX session_conversation ON agent_sessions(conversation_id,native_id)`,
		`CREATE INDEX session_workspace ON agent_sessions(workspace_id)`,
		`CREATE TABLE workspaces AS SELECT * FROM audit.workspaces WHERE id IN (SELECT workspace_id FROM agent_sessions)`,
		`CREATE TABLE agent_session_usage AS SELECT * FROM audit.agent_session_usage WHERE agent_session_id IN (SELECT id FROM cohort)`,
		`CREATE TABLE agent_session_messages AS SELECT * FROM audit.agent_session_messages WHERE agent_session_id IN (SELECT id FROM cohort)`,
		`CREATE INDEX membership_session ON agent_session_messages(agent_session_id)`,
		`CREATE TABLE model_requests AS SELECT * FROM audit.model_requests WHERE agent_session_id IN (SELECT id FROM cohort)`,
		`CREATE INDEX request_conversation ON model_requests(conversation_id,agent_session_id)`,
		`CREATE TABLE messages AS SELECT * FROM audit.messages WHERE id IN (SELECT message_id FROM agent_session_messages UNION SELECT delegation_message_id FROM agent_sessions UNION SELECT source_message_id FROM model_requests UNION SELECT final_message_id FROM model_requests)`,
		`CREATE UNIQUE INDEX message_id ON messages(id)`,
		`CREATE INDEX message_call ON messages(conversation_id,call_id)`,
		`CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT)`,
		`CREATE TABLE cost_on_date AS SELECT * FROM audit.cost_on_date`,
		`CREATE TABLE model_alias_on_date AS SELECT * FROM audit.model_alias_on_date`,
	} {
		if _, err = copyTx.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err = copyTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DETACH DATABASE audit`); err != nil {
		t.Fatal(err)
	}
	c := &Catalog{DB: db, Path: copyPath}
	book, err := c.loadPriceBook()
	if err != nil {
		t.Fatal(err)
	}
	ledger := func() string {
		t.Helper()
		rows, err := queryMaps(db, `SELECT u.*,u.model usage_model,s.model session_model FROM agent_session_usage u JOIN agent_sessions s ON s.id=u.agent_session_id ORDER BY u.agent_session_id,u.usage_hour,u.model,u.effort,u.attribution`)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			day := firstString(row["usage_hour"])
			if len(day) > 10 {
				day = day[:10]
			}
			price := book.cost(ledgerModel(row), day, ledgerTokens(row))
			row["price"] = price.cost
			row["price_status"] = price.status
			delete(row, "session_model")
		}
		return jsonText(rows)
	}
	before := ledger()
	counts := func() string {
		t.Helper()
		rows, err := queryMaps(db, `SELECT w.source_kind,COUNT(*) sessions,SUM(CASE WHEN s.model LIKE '%haiku%' THEN 1 ELSE 0 END) haiku,SUM(CASE WHEN s.model='Multiple models' THEN 1 ELSE 0 END) multiple,SUM(CASE WHEN s.model='Unknown model' THEN 1 ELSE 0 END) unknown FROM agent_sessions s JOIN workspaces w ON w.id=s.workspace_id GROUP BY w.source_kind`)
		if err != nil {
			t.Fatal(err)
		}
		return jsonText(rows)
	}
	t.Log("before", counts())
	report, err := c.RepairAgentSessionModels(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("after %s; repair %+v", counts(), report)
	if before != ledger() {
		t.Fatal("repair changed ledger rows or prices")
	}
	again, err := c.RepairAgentSessionModels(t.Context(), nil)
	if err != nil || again.Updated != 0 {
		t.Fatalf("repeat=%+v: %v", again, err)
	}
	t.Log("per-model token categories, ledger rows and prices unchanged; repeat updated zero rows")
}
