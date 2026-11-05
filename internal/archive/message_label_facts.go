package archive

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

// Independent of inference: rebuilding this disposable query projection must
// never invalidate the classifier's probabilities or start the encoder.
const messageLabelFactsAlgorithm = "1"

var messageLabelFactsBatch = 20 // conversations, committed atomically

func messageLabelFactsVersion() string {
	// day is local, as on the other message dashboards. Timezone/rule changes
	// invalidate the calendar without relabeling any message.
	janName, janOffset := time.Date(2020, 1, 1, 12, 0, 0, 0, time.Local).Zone()
	julName, julOffset := time.Date(2020, 7, 1, 12, 0, 0, 0, time.Local).Zone()
	return fmt.Sprintf("%s/%s/%s:%d/%s:%d", messageLabelFactsAlgorithm, toolRollupTimezone(), janName, janOffset, julName, julOffset)
}

const messageLabelFactsDDL = `
CREATE TABLE IF NOT EXISTS message_label_facts (
 rowid INTEGER PRIMARY KEY,
 message_id TEXT NOT NULL UNIQUE,
 conversation_id TEXT NOT NULL,
 occurred_at TEXT, day TEXT, kind TEXT, role TEXT, model TEXT,
 source TEXT NOT NULL, segment TEXT, label_model TEXT, labeled_at TEXT,
 label_status TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS message_label_facts_conversation_idx ON message_label_facts(conversation_id);
CREATE INDEX IF NOT EXISTS message_label_facts_trends_idx ON message_label_facts(occurred_at,day,message_id,conversation_id,source,model,label_status);
DROP INDEX IF EXISTS message_label_facts_time_idx;
CREATE TABLE IF NOT EXISTS message_label_memberships (
 message_rowid INTEGER NOT NULL, label_id INTEGER NOT NULL,
 PRIMARY KEY(message_rowid,label_id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS message_label_names (
 id INTEGER PRIMARY KEY, label TEXT NOT NULL UNIQUE,
 family TEXT NOT NULL, function_role TEXT NOT NULL, label_depth INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS message_label_facts_dirty (conversation_id TEXT PRIMARY KEY) WITHOUT ROWID;
`

// IDs and small query facts are stored once per message; taxonomy strings once
// per label. Transcript bodies, titles and repository metadata stay at source.
var messageLabelFactFields = []string{"message_id", "conversation_id", "occurred_at", "day", "kind", "role", "model", "source", "segment", "label_model", "labeled_at", "label_status"}
var messageLabelNameFields = []string{"label", "family", "function_role", "label_depth"}

func messageLabelSelect(table sqlDataset, fields []string) string {
	table = table.project(fields)
	columns := make([]string, 0, len(fields))
	for _, field := range fields {
		columns = append(columns, table.columns[field]+" AS "+sqlIdentifier(field))
	}
	where := ""
	if table.base != "" {
		where = " WHERE " + table.base
	}
	return "SELECT " + strings.Join(columns, ",") + " " + table.from + where
}

func messageLabelWithoutLedger(table sqlDataset) sqlDataset {
	at := strings.Index(table.from, " LEFT JOIN message_labels l")
	table.from = table.from[:at] + ` LEFT JOIN (SELECT NULL message_id,NULL labels,NULL segment,NULL model,NULL labeled_at WHERE 0) l ON 0
 LEFT JOIN (SELECT NULL model WHERE 0) lm ON 0 LEFT JOIN (SELECT NULL value WHERE 0) tag ON 0`
	return table
}

func messageLabelStoredFrom(ledger bool, fields []string) string {
	live := messageLabelLiveDataset("SELECT conversation_id FROM message_label_facts_dirty")
	if !ledger {
		live = messageLabelWithoutLedger(live)
	}
	stored := make([]string, 0, len(fields))
	for _, field := range fields {
		switch {
		case slices.Contains(messageLabelNameFields, field):
			stored = append(stored, "name."+field)
		default:
			stored = append(stored, "stored."+field)
		}
	}
	liveSQL := messageLabelSelect(live, fields)
	// Separate the inner and outer aliases so optional metadata joins can be
	// pruned independently when a chart consumes only label/time fields.
	liveSQL = strings.ReplaceAll(strings.ReplaceAll(liveSQL, "c.", "live_conversation."), "conversations c ", "conversations live_conversation ")
	return `FROM (SELECT ` + strings.Join(stored, ",") + ` FROM message_label_facts stored
 JOIN message_label_memberships membership ON membership.message_rowid=stored.rowid
 JOIN message_label_names name ON name.id=membership.label_id
 WHERE NOT EXISTS(SELECT 1 FROM message_label_facts_dirty dirty WHERE dirty.conversation_id=stored.conversation_id)
 UNION ALL ` + liveSQL + `) f
 LEFT JOIN conversations c ON c.id=f.conversation_id
 LEFT JOIN workspaces w ON w.id=c.workspace_id
 LEFT JOIN repositories repo ON repo.id=w.repository_id
 LEFT JOIN messages body ON body.id=f.message_id`
}

func messageLabelStoredDataset(ledger bool) sqlDataset {
	fields := append(slices.Clone(messageLabelFactFields), messageLabelNameFields...)
	columns := map[string]string{}
	for _, field := range fields {
		columns[field] = "f." + field
	}
	for field, expression := range map[string]string{
		"id": "f.message_id||':'||f.label", "workspace_id": "c.workspace_id", "fingerprint": "f.conversation_id",
		"title": "COALESCE(NULLIF(c.agent_nickname,''),w.title,c.id)", "repository_name": "repo.display_name",
		"provider": "c.provider", "subagent": "c.parent_id IS NOT NULL",
		"week": "date(f.day,'-6 days','weekday 1')", "month": "substr(f.day,1,7)",
		"text": "substr(" + timelineContentSQL("body.text") + ",1,640)",
	} {
		columns[field] = expression
	}
	covered := map[string]bool{}
	for field := range columns {
		covered[field] = field != "text"
	}
	return sqlDataset{from: messageLabelStoredFrom(ledger, fields), columns: columns, projectFrom: func(consumed []string) string {
		needed := []string{"conversation_id", "message_id"}
		for _, field := range fields {
			if field == "conversation_id" || field == "message_id" {
				continue
			}
			for _, name := range consumed {
				if strings.Contains(columns[name], "f."+field) {
					needed = append(needed, field)
					break
				}
			}
		}
		return messageLabelStoredFrom(ledger, needed)
	}, optionalJoins: map[string]string{
		"c":    " LEFT JOIN conversations c ON c.id=f.conversation_id",
		"w":    " LEFT JOIN workspaces w ON w.id=c.workspace_id",
		"repo": " LEFT JOIN repositories repo ON repo.id=w.repository_id",
		"body": " LEFT JOIN messages body ON body.id=f.message_id",
	}, timestamps: map[string]string{"occurred_at": "f.occurred_at"}, lookups: map[string]bool{"message_id": true, "conversation_id": true, "workspace_id": true},
		// The physical facts themselves are narrow; this index supplies range access,
		// and the covered population budget doesn't permit transcript-body scans.
		pageFrom: "FROM message_label_facts stored", coveringIndex: "message_label_facts_trends_idx", rangeCoveringIndex: "message_label_facts_trends_idx", coveredFields: covered}
}

func (c *Catalog) ensureMessageLabelFacts(ctx context.Context) error {
	return c.writeTransaction(ctx, "initialize message label query facts", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, messageLabelFactsDDL); err != nil {
			return err
		}
		if err := messageLabelFactTriggers(ctx, tx); err != nil {
			return err
		}
		var version string
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE((SELECT value FROM meta WHERE key='message_label_facts_version'),'')").Scan(&version); err != nil {
			return err
		}
		if version == messageLabelFactsVersion() {
			return nil
		}
		// All old rows remain hidden behind the queue until replaced. A restart
		// resumes this queue rather than restarting the backfill.
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO message_label_facts_dirty SELECT id FROM conversations;
   INSERT OR IGNORE INTO message_label_facts_dirty SELECT DISTINCT conversation_id FROM message_label_facts;
   DELETE FROM meta WHERE key='message_label_facts_ready';
   DELETE FROM meta WHERE key='message_label_facts_version'`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES('message_label_facts_version',?)", messageLabelFactsVersion())
		if err == nil {
			var generation int
			err = tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name='query_cache_generation'").Scan(&generation)
			if err == nil && generation > 0 {
				_, err = tx.ExecContext(ctx, "UPDATE query_cache_generation SET value=value+1 WHERE id=1")
			}
		}
		return err
	})
}

// Install after both core schema migration and optional classifier-ledger
// creation. These triggers queue only real changes, in the source transaction,
// including writes from another process or an older executable.
func messageLabelFactTriggers(ctx context.Context, tx *sql.Tx) error {
	mark := func(expression string) string {
		return "INSERT INTO message_label_facts_dirty(conversation_id) SELECT " + expression + " WHERE " + expression + " IS NOT NULL ON CONFLICT(conversation_id) DO NOTHING;"
	}
	add := func(table, event, columns, expression string) error {
		name := "message_label_facts_dirty_" + table + "_" + strings.ToLower(event)
		of, when := "", ""
		if event == "UPDATE" && columns != "" {
			of = " OF " + columns
			changed := []string{}
			for _, column := range strings.Split(columns, ",") {
				changed = append(changed, "OLD."+column+" IS NOT NEW."+column)
			}
			when = " WHEN " + strings.Join(changed, " OR ")
		}
		body := ""
		for _, ref := range map[string][]string{"INSERT": {"NEW"}, "DELETE": {"OLD"}, "UPDATE": {"OLD", "NEW"}}[event] {
			body += mark(strings.ReplaceAll(expression, "$", ref))
		}
		_, err := tx.ExecContext(ctx, "DROP TRIGGER IF EXISTS "+name+"; CREATE TRIGGER "+name+" AFTER "+event+of+" ON "+table+when+" BEGIN "+body+" END")
		return err
	}
	for table, fields := range map[string]string{
		"messages":                     "conversation_id,role,kind,created_at,selected,model",
		"message_authorship":           "message_id,conversation_id,typed_words",
		"conversations":                "model",
		"message_labels":               "message_id,conversation_id,segment,labels,model,labeled_at",
		"message_labels_conversations": "conversation_id,structure,units,built_at",
		"message_labels_dirty":         "conversation_id,token",
	} {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			continue
		}
		expression := "$.conversation_id"
		if table == "conversations" {
			expression = "$.id"
		}
		for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
			if err := add(table, event, fields, expression); err != nil {
				return err
			}
		}
	}
	var ledger int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='message_labels_models'").Scan(&ledger); err != nil {
		return err
	}
	if ledger > 0 {
		for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
			name := "message_label_facts_dirty_models_" + strings.ToLower(event)
			models := map[string]string{"INSERT": "NEW.model", "DELETE": "OLD.model", "UPDATE": "OLD.model,NEW.model"}[event]
			when := ""
			if event == "UPDATE" {
				when = " WHEN OLD.model IS NOT NEW.model OR OLD.labels_json IS NOT NEW.labels_json"
			}
			if _, err := tx.ExecContext(ctx, "DROP TRIGGER IF EXISTS "+name+"; CREATE TRIGGER "+name+" AFTER "+event+" ON message_labels_models"+when+" BEGIN INSERT INTO message_label_facts_dirty SELECT DISTINCT conversation_id FROM message_labels WHERE model IN ("+models+") ON CONFLICT(conversation_id) DO NOTHING; END"); err != nil {
				return err
			}
		}
	}
	return nil
}

// One bounded batch; no inference, network, message text or probability blobs.
// Queue reads, replacement and removal share one writer transaction, so source
// changes cannot race publication. Rollback leaves the whole batch pending.
func (c *Catalog) refreshMessageLabelFacts(ctx context.Context) (int, error) {
	tx, finish, err := c.beginTrackedWrite(ctx, "message-label-query-facts")
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(); finish() }()
	selector := fmt.Sprintf("SELECT conversation_id FROM message_label_facts_dirty ORDER BY conversation_id LIMIT %d", messageLabelFactsBatch)
	var pending int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM ("+selector+")").Scan(&pending); err != nil {
		return 0, err
	}
	if pending == 0 {
		// Publishing a new projection version also invalidates cached query results.
		result, e := tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES('message_label_facts_ready',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value WHERE value<>excluded.value", messageLabelFactsVersion())
		if e != nil {
			return 0, e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return 0, e
		}
		if n > 0 {
			if _, e = tx.ExecContext(ctx, "UPDATE query_cache_generation SET value=value+1 WHERE id=1"); e != nil {
				return 0, e
			}
		}
		return 0, tx.Commit()
	}
	completed := maintenanceBatch(ctx, tx, "Indexing message labels", "conversation", pending,
		"SELECT count(*) FROM message_label_facts_dirty")
	live := messageLabelLiveDataset(selector)
	var ledger int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='message_labels'").Scan(&ledger); err != nil {
		return 0, err
	}
	if ledger == 0 {
		live = messageLabelWithoutLedger(live)
	}
	// A message can move between conversations before either queued refresh
	// runs. Remove its former fact too, regardless of queue order.
	replaced := "conversation_id IN (" + selector + ") OR message_id IN (SELECT id FROM messages WHERE conversation_id IN (" + selector + "))"
	fields := append(slices.Clone(messageLabelFactFields), messageLabelNameFields...)
	statements := []string{
		// Decode the batch once, in small connection-local staging; its text and
		// probability blobs are never read or copied. Rollback removes it too.
		"CREATE TEMP TABLE message_label_build AS " + messageLabelSelect(live, fields),
		"DELETE FROM message_label_memberships WHERE message_rowid IN (SELECT rowid FROM message_label_facts WHERE " + replaced + ")",
		"DELETE FROM message_label_facts WHERE " + replaced,
		"INSERT INTO message_label_facts(" + strings.Join(messageLabelFactFields, ",") + ") SELECT DISTINCT " + strings.Join(messageLabelFactFields, ",") + " FROM message_label_build",
		"INSERT OR IGNORE INTO message_label_names(" + strings.Join(messageLabelNameFields, ",") + ") SELECT DISTINCT " + strings.Join(messageLabelNameFields, ",") + " FROM message_label_build",
		"INSERT INTO message_label_memberships(message_rowid,label_id) SELECT facts.rowid,name.id FROM message_label_build observations JOIN message_label_names name ON name.label=observations.label JOIN message_label_facts facts ON facts.message_id=observations.message_id",
		"DROP TABLE message_label_build",
		"DELETE FROM message_label_facts_dirty WHERE conversation_id IN (" + selector + ")",
	}
	for _, statement := range statements {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return 0, err
		}
	}
	err = tx.Commit()
	completed(err)
	return pending, err
}

func (c *Catalog) maintainMessageLabelFacts(ctx context.Context) {
	var pass *daemonPass
	defer func() {
		if pass != nil {
			pass.end(nil)
		}
	}()
	for {
		if c.queries.wait(pass.context(ctx)) != nil {
			return
		}
		if pass == nil {
			pass = c.beginPass(ctx, "maintenance.labels.query", triggerChained)
		}
		var count int
		var err error
		pass.runIO(ctx, func(ctx context.Context) { count, err = c.refreshMessageLabelFacts(ctx) })
		if err == nil {
			pass.worked(count, "conversation")
		}
		delay := 20 * time.Millisecond
		if count == 0 || err != nil {
			pass.end(err)
			pass = nil
			delay = time.Second
		}
		if err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "Message label query facts: %v\n", err)
			delay = 5 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}
