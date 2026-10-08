package archive

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Tokens, rather than counters reset after publication, prevent an old build
// from clearing a partition changed (or deleted and recreated) by another writer.
const analysisPartitionsSQL = `
CREATE TABLE IF NOT EXISTS tool_rollup_dirty(workspace_id TEXT PRIMARY KEY, revision TEXT NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS finding_message_revisions(conversation_id TEXT PRIMARY KEY, revision TEXT NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS finding_tool_revisions(conversation_id TEXT PRIMARY KEY, revision TEXT NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS finding_feature_partitions(feature TEXT NOT NULL, conversation_id TEXT NOT NULL, revision TEXT NOT NULL, rows_json TEXT NOT NULL,
 PRIMARY KEY(feature,conversation_id)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS tool_usage_daily_day_idx ON tool_usage_daily(day);
CREATE TABLE IF NOT EXISTS finding_feature_builds(feature TEXT PRIMARY KEY, version TEXT NOT NULL) WITHOUT ROWID;
`

func (c *Catalog) ensureAnalysisPartitions() error {
	if _, err := c.DB.Exec(analysisPartitionsSQL); err != nil {
		return err
	}
	// Raw-call changes cover additions, corrections, pruning, path attribution,
	// usage revisions and conversation/workspace moves, within the source transaction.
	for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
		refs := []string{"NEW"}
		if event == "DELETE" {
			refs = []string{"OLD"}
		} else if event == "UPDATE" {
			refs = []string{"OLD", "NEW"}
		}
		body := ""
		for _, ref := range refs {
			body += fmt.Sprintf(`INSERT INTO tool_rollup_dirty VALUES(%s.workspace_id,hex(randomblob(16))) ON CONFLICT(workspace_id) DO UPDATE SET revision=excluded.revision;
INSERT INTO finding_tool_revisions VALUES(%s.conversation_id,hex(randomblob(16))) ON CONFLICT(conversation_id) DO UPDATE SET revision=excluded.revision;`, ref, ref)
		}
		if _, err := c.DB.Exec(fmt.Sprintf("CREATE TRIGGER IF NOT EXISTS analysis_tools_%s AFTER %s ON tool_calls BEGIN %s END", event, event, body)); err != nil {
			return err
		}
	}
	for _, spec := range []struct{ name, table, columns, body string }{
		{"workspace", "workspaces", "title,source_kind,repository_id,activity_at", `INSERT INTO tool_rollup_dirty VALUES(NEW.id,hex(randomblob(16))) ON CONFLICT(workspace_id) DO UPDATE SET revision=excluded.revision;`},
		{"session", "agent_sessions", "depth,kind,conversation_id,workspace_id", `INSERT INTO tool_rollup_dirty VALUES(OLD.workspace_id,hex(randomblob(16))) ON CONFLICT(workspace_id) DO UPDATE SET revision=excluded.revision;
INSERT INTO tool_rollup_dirty VALUES(NEW.workspace_id,hex(randomblob(16))) ON CONFLICT(workspace_id) DO UPDATE SET revision=excluded.revision;
INSERT INTO finding_tool_revisions VALUES(OLD.conversation_id,hex(randomblob(16))) ON CONFLICT(conversation_id) DO UPDATE SET revision=excluded.revision;
INSERT INTO finding_tool_revisions VALUES(NEW.conversation_id,hex(randomblob(16))) ON CONFLICT(conversation_id) DO UPDATE SET revision=excluded.revision;`},
		{"repository", "repositories", "display_name", `INSERT INTO tool_rollup_dirty SELECT id,hex(randomblob(16)) FROM workspaces WHERE repository_id=NEW.id
ON CONFLICT(workspace_id) DO UPDATE SET revision=excluded.revision;`},
	} {
		if _, err := c.DB.Exec(fmt.Sprintf("CREATE TRIGGER IF NOT EXISTS analysis_%s_update AFTER UPDATE OF %s ON %s BEGIN %s END", spec.name, spec.columns, spec.table, spec.body)); err != nil {
			return err
		}
	}

	for _, event := range []string{"INSERT", "DELETE"} {
		ref := "NEW"
		if event == "DELETE" {
			ref = "OLD"
		}
		body := fmt.Sprintf(`INSERT INTO tool_rollup_dirty VALUES(%s.workspace_id,hex(randomblob(16))) ON CONFLICT(workspace_id) DO UPDATE SET revision=excluded.revision;
  INSERT INTO finding_tool_revisions VALUES(%s.conversation_id,hex(randomblob(16))) ON CONFLICT(conversation_id) DO UPDATE SET revision=excluded.revision;`, ref, ref)
		if _, err := c.DB.Exec(fmt.Sprintf("CREATE TRIGGER IF NOT EXISTS analysis_session_%s AFTER %s ON agent_sessions BEGIN %s END", event, event, body)); err != nil {
			return err
		}
	}
	if _, err := c.DB.Exec(`CREATE TRIGGER IF NOT EXISTS analysis_conversation_workspace_update AFTER UPDATE OF workspace_id ON conversations WHEN OLD.workspace_id<>NEW.workspace_id BEGIN
 INSERT INTO meta(key,value) VALUES('tool_rollup_full_revision',hex(randomblob(16))) ON CONFLICT(key) DO UPDATE SET value=excluded.value; END`); err != nil {
		return err
	}
	for _, table := range []string{"conversation_identity_links", "cost_changes", "model_aliases"} {
		for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
			if _, err := c.DB.Exec(fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS analysis_full_%s_%s AFTER %s ON %s BEGIN
   INSERT INTO meta(key,value) VALUES('tool_rollup_full_revision',hex(randomblob(16))) ON CONFLICT(key) DO UPDATE SET value=excluded.value; END`, table, event, event, table)); err != nil {
				return err
			}
		}
	}
	for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
		refs := []string{"NEW"}
		if event == "DELETE" {
			refs = []string{"OLD"}
		} else if event == "UPDATE" {
			refs = []string{"OLD", "NEW"}
		}
		body := ""
		for _, ref := range refs {
			body += fmt.Sprintf(`INSERT INTO finding_tool_revisions SELECT conversation_id,hex(randomblob(16)) FROM tool_calls WHERE id=%s.tool_call_id
  ON CONFLICT(conversation_id) DO UPDATE SET revision=excluded.revision;`, ref)
		}
		if _, err := c.DB.Exec(fmt.Sprintf("CREATE TRIGGER IF NOT EXISTS analysis_urls_%s AFTER %s ON tool_urls BEGIN %s END", event, event, body)); err != nil {
			return err
		}
	}
	for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
		refs := []string{"NEW"}
		if event == "DELETE" {
			refs = []string{"OLD"}
		} else if event == "UPDATE" {
			refs = []string{"OLD", "NEW"}
		}
		body := ""
		for _, ref := range refs {
			body += fmt.Sprintf(`INSERT INTO finding_message_revisions VALUES(%s.id,hex(randomblob(16))) ON CONFLICT(conversation_id) DO UPDATE SET revision=excluded.revision;`, ref)
		}
		if _, err := c.DB.Exec(fmt.Sprintf("CREATE TRIGGER IF NOT EXISTS analysis_conversations_%s AFTER %s ON conversations BEGIN %s END", event, event, body)); err != nil {
			return err
		}
	}
	for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
		refs := []string{"NEW"}
		if event == "DELETE" {
			refs = []string{"OLD"}
		} else if event == "UPDATE" {
			refs = []string{"OLD", "NEW"}
		}
		body, conditions := "", []string{}
		for _, ref := range refs {
			conditions = append(conditions, ref+".role='user' AND "+ref+".kind='message'")
			body += fmt.Sprintf(`INSERT INTO finding_message_revisions VALUES(%s.conversation_id,hex(randomblob(16))) ON CONFLICT(conversation_id) DO UPDATE SET revision=excluded.revision;`, ref)
		}
		if _, err := c.DB.Exec(fmt.Sprintf("CREATE TRIGGER IF NOT EXISTS analysis_messages_%s AFTER %s ON messages WHEN %s BEGIN %s END", event, event, strings.Join(conditions, " OR "), body)); err != nil {
			return err
		}
	}
	if _, err := c.DB.Exec(`INSERT INTO finding_message_revisions SELECT id,'' FROM conversations
 WHERE NOT EXISTS(SELECT 1 FROM meta WHERE key='finding_message_revisions_seeded') ON CONFLICT(conversation_id) DO NOTHING;
 INSERT OR IGNORE INTO meta(key,value) VALUES('finding_message_revisions_seeded','1')`); err != nil {
		return err
	}
	// Existing rows need one revision seed on upgrade; tombstones are retained so
	// deletions, even the last call of a conversation, invalidate cached features.
	if _, err := c.DB.Exec(`INSERT INTO finding_tool_revisions SELECT DISTINCT conversation_id,'' FROM tool_calls
 WHERE NOT EXISTS(SELECT 1 FROM meta WHERE key='finding_tool_revisions_seeded')
 ON CONFLICT(conversation_id) DO NOTHING;
 INSERT OR IGNORE INTO meta(key,value) VALUES('finding_tool_revisions_seeded','1')`); err != nil {
		return err
	}
	return nil
}

// Broad callers (identity reconciliation and repair) retain a full fallback.
// Ingest calls bumpToolLedgerPartitionGeneration because its changed partitions
// are already recorded by the authoritative-row triggers.
func bumpToolLedgerPartitionGeneration(tx execer) error {
	_, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('tool_ledger_generation','1')
 ON CONFLICT(key) DO UPDATE SET value=CAST(CAST(value AS INTEGER)+1 AS TEXT)`)
	return err
}

func readMeta(q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, ctx context.Context, key string) (string, error) {
	var value string
	err := q.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}
