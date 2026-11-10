package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

// Independent of inference: rebuilding this disposable query projection must
// never invalidate the classifier's probabilities or start the encoder.
const messageLabelFactsAlgorithm = "1"

var messageLabelFactsBatch = 128 // maximum conversations; also bounded by eligible messages

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

// unmarked is executionUnmarkedSQL for the labels ledger.
func messageLabelStoredFrom(ledger bool, fields []string, unmarked string) string {
	// Conversations with unmarked execution changes are projected live like
	// dirty ones, where their labels read as pending. Without a ledger there
	// are no labels to hold back.
	pending, hold := "SELECT conversation_id FROM message_label_facts_dirty", ""
	if ledger {
		pending += " UNION " + unmarked
		hold = "\n AND stored.conversation_id NOT IN (" + unmarked + ")"
	} else {
		unmarked = ""
	}
	live := messageLabelLiveDatasetFor(pending, unmarked)
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
 WHERE NOT EXISTS(SELECT 1 FROM message_label_facts_dirty dirty WHERE dirty.conversation_id=stored.conversation_id)` + hold + `
 UNION ALL ` + liveSQL + `) f
 LEFT JOIN conversations c ON c.id=f.conversation_id
 LEFT JOIN workspaces w ON w.id=c.workspace_id
 LEFT JOIN repositories repo ON repo.id=w.repository_id
 LEFT JOIN messages body ON body.id=f.message_id`
}

func messageLabelStoredDataset(ledger bool, unmarked string) sqlDataset {
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
	return sqlDataset{from: messageLabelStoredFrom(ledger, fields, unmarked), columns: columns, projectFrom: func(consumed []string) string {
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
		return messageLabelStoredFrom(ledger, needed, unmarked)
	}, optionalJoins: map[string]string{
		"c":    " LEFT JOIN conversations c ON c.id=f.conversation_id",
		"w":    " LEFT JOIN workspaces w ON w.id=c.workspace_id",
		"repo": " LEFT JOIN repositories repo ON repo.id=w.repository_id",
		"body": " LEFT JOIN messages body ON body.id=f.message_id",
	}, timestamps: map[string]string{"occurred_at": "f.occurred_at"}, lookups: map[string]bool{"message_id": true, "conversation_id": true, "workspace_id": true},
		// The physical facts themselves are narrow; this index supplies range access,
		// and the covered population budget doesn't permit transcript-body scans.
		newestBounds: map[string]newestBound{"occurred_at": {
			nth:    "SELECT occurred_at FROM message_label_facts ORDER BY occurred_at DESC LIMIT 1 OFFSET ?",
			column: "f.occurred_at",
			join:   [2]string{" JOIN message_label_memberships membership", " CROSS JOIN message_label_memberships membership"},
		}},
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
				err = advanceCacheGeneration(ctx, tx, "labels")
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

// Preparation uses a read snapshot on a retained connection. Only TEMP tables
// are written here, so inference and ingestion can keep using the catalog writer.
// The shared source generation is checked after acquiring the writer; stale
// preparations are discarded without clearing any dirty marks.
type messageLabelFactsBuild struct {
	generation                           int64
	conversations, messages, memberships int
	ready                                bool
	completed                            func(error)
}

type messageLabelFactsTiming struct {
	prepare, wait, facts, memberships, commit, hold time.Duration
	changedFacts, changedMemberships                int64
}

var messageLabelFactsMessages = 500 // soft limit: one conversation remains atomic
var messageLabelFactsTarget = 250 * time.Millisecond

var errMessageLabelFactsStale = errors.New("message label query inputs changed during preparation")

func messageLabelFactsSource(selector string, ledger bool) sqlDataset {
	// Stored facts keep the labels as built; readers hold back the
	// conversations whose execution changes are unmarked (messageLabelStoredFrom).
	live := messageLabelLiveDatasetFor(selector, "")
	// These indexes are installed on writable catalogs. Keep the ordinary live
	// dataset compatible with older read-only snapshots that do not have them.
	live.from = strings.Replace(live.from, "INDEXED BY messages_conversation_idx", "INDEXED BY messages_label_conversation_idx", 1)
	if !ledger {
		return messageLabelWithoutLedger(live)
	}
	live.from = strings.Replace(live.from, " LEFT JOIN message_labels l ON", " LEFT JOIN message_labels l INDEXED BY message_labels_query_idx ON", 1)
	live.from = strings.Replace(live.from, "SELECT DISTINCT model,labels FROM message_labels WHERE", "SELECT DISTINCT model,labels FROM message_labels INDEXED BY message_labels_masks_idx WHERE", 1)
	return live
}

// prepareMessageLabelFacts stages the next dirty conversations' facts within
// a message budget. A nil members list takes them from the whole queue in
// conversation order; a non-nil one only those conversations, and never
// reports the facts ready (an empty selection is not the whole queue).
func prepareMessageLabelFacts(ctx context.Context, tx *sql.Tx, messages int, members []string) (build messageLabelFactsBuild, err error) {
	if err = tx.QueryRowContext(ctx, "SELECT value FROM query_cache_generation WHERE id=1").Scan(&build.generation); err != nil {
		return build, err
	}
	// Drop only this connection's prior preparation, including after a stale
	// snapshot or cancellation. A failed transaction rolls its TEMP changes back.
	for _, table := range []string{"message_label_batch", "message_label_build", "message_label_old", "message_label_removed", "message_label_new_memberships"} {
		if _, err = tx.ExecContext(ctx, "DROP TABLE IF EXISTS temp."+table); err != nil {
			return build, err
		}
	}
	selected, args := "", []any{messageLabelFactsBatch, max(1, messages)}
	if members != nil {
		selected, args = " WHERE conversation_id IN (SELECT value FROM json_each(?))", []any{jsonText(members), messageLabelFactsBatch, max(1, messages)}
	}
	if _, err = tx.ExecContext(ctx, `CREATE TEMP TABLE message_label_batch(conversation_id TEXT PRIMARY KEY) WITHOUT ROWID;
 INSERT INTO message_label_batch
 WITH candidates AS MATERIALIZED (
 SELECT conversation_id,(SELECT count(*) FROM messages m INDEXED BY messages_label_conversation_idx
 WHERE m.conversation_id=dirty.conversation_id AND `+messageLabelMessagePredicate+`) messages
 FROM message_label_facts_dirty dirty`+selected+` ORDER BY conversation_id LIMIT ?
 ), budget AS (
 SELECT conversation_id,COALESCE(sum(messages) OVER (ORDER BY conversation_id ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING),0) previous
 FROM candidates
 ) SELECT conversation_id FROM budget WHERE previous<?`, args...); err != nil {
		return build, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM message_label_batch").Scan(&build.conversations); err != nil {
		return build, err
	}
	if build.conversations == 0 && members != nil {
		return build, nil
	}
	if build.conversations == 0 {
		var version string
		err = tx.QueryRowContext(ctx, "SELECT COALESCE((SELECT value FROM meta WHERE key='message_label_facts_ready'),'')").Scan(&version)
		build.ready = version == messageLabelFactsVersion()
		return build, err
	}
	build.completed = maintenanceBatch(ctx, tx, "Indexing message labels", "conversation", build.conversations,
		"SELECT count(*) FROM message_label_facts_dirty")
	const selector = "SELECT conversation_id FROM message_label_batch"
	var ledger bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='message_labels')").Scan(&ledger); err != nil {
		return build, err
	}
	fields := append(slices.Clone(messageLabelFactFields), messageLabelNameFields...)
	// Preserve moved-message cleanup regardless of queue order, and stage its
	// old identities once, outside the writer, for both facts and memberships.
	replaced := "conversation_id IN (" + selector + ") OR message_id IN (SELECT id FROM messages WHERE conversation_id IN (" + selector + "))"
	for _, statement := range []string{
		"CREATE TEMP TABLE message_label_build AS " + messageLabelSelect(messageLabelFactsSource(selector, ledger), fields),
		"CREATE INDEX temp.message_label_build_identity ON message_label_build(message_id,label)",
		"CREATE TEMP TABLE message_label_old AS SELECT rowid,message_id FROM message_label_facts WHERE " + replaced,
		"CREATE TEMP TABLE message_label_removed AS SELECT rowid FROM message_label_old old WHERE NOT EXISTS(SELECT 1 FROM message_label_build new WHERE new.message_id=old.message_id)",
	} {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return build, err
		}
	}
	err = tx.QueryRowContext(ctx, "SELECT count(DISTINCT message_id),count(*) FROM message_label_build").Scan(&build.messages, &build.memberships)
	return build, err
}

// Publish a valid preparation atomically. Surviving facts keep their rowids;
// unchanged facts and memberships cause no persistent row/index rewrites.
func publishMessageLabelFacts(ctx context.Context, tx *sql.Tx, build messageLabelFactsBuild, timing *messageLabelFactsTiming) error {
	var generation int64
	if err := tx.QueryRowContext(ctx, "SELECT value FROM query_cache_generation WHERE id=1").Scan(&generation); err != nil {
		return err
	}
	if generation != build.generation {
		return errMessageLabelFactsStale
	}
	if build.conversations == 0 {
		result, err := tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES('message_label_facts_ready',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value WHERE value<>excluded.value", messageLabelFactsVersion())
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err == nil && n > 0 {
			err = advanceCacheGeneration(ctx, tx, "labels")
		}
		return err
	}
	exec := func(statement string, changed *int64) error {
		result, err := tx.ExecContext(ctx, statement)
		if err == nil && changed != nil {
			n, e := result.RowsAffected()
			*changed += n
			err = e
		}
		return err
	}
	started := time.Now()
	if err := exec("DELETE FROM message_label_memberships WHERE message_rowid IN (SELECT rowid FROM message_label_removed)", &timing.changedMemberships); err != nil {
		return err
	}
	if err := exec("DELETE FROM message_label_facts WHERE rowid IN (SELECT rowid FROM message_label_removed)", &timing.changedFacts); err != nil {
		return err
	}
	updates, changes := []string{}, []string{}
	for _, field := range messageLabelFactFields[1:] {
		updates = append(updates, field+"=excluded."+field)
		changes = append(changes, "message_label_facts."+field+" IS NOT excluded."+field)
	}
	if err := exec("INSERT INTO message_label_facts("+strings.Join(messageLabelFactFields, ",")+") SELECT DISTINCT "+strings.Join(messageLabelFactFields, ",")+" FROM message_label_build WHERE 1 ON CONFLICT(message_id) DO UPDATE SET "+strings.Join(updates, ",")+" WHERE "+strings.Join(changes, " OR "), &timing.changedFacts); err != nil {
		return err
	}
	timing.facts = time.Since(started)
	started = time.Now()
	for _, statement := range []string{
		"INSERT OR IGNORE INTO message_label_names(" + strings.Join(messageLabelNameFields, ",") + ") SELECT DISTINCT " + strings.Join(messageLabelNameFields, ",") + " FROM message_label_build",
		"CREATE TEMP TABLE message_label_new_memberships(message_rowid INTEGER,label_id INTEGER,PRIMARY KEY(message_rowid,label_id)) WITHOUT ROWID",
		"INSERT INTO message_label_new_memberships SELECT facts.rowid,name.id FROM message_label_build observations JOIN message_label_names name ON name.label=observations.label JOIN message_label_facts facts ON facts.message_id=observations.message_id",
	} {
		if err := exec(statement, nil); err != nil {
			return err
		}
	}
	if err := exec(`DELETE FROM message_label_memberships WHERE message_rowid IN (SELECT rowid FROM message_label_old)
 AND NOT EXISTS(SELECT 1 FROM message_label_new_memberships new WHERE new.message_rowid=message_label_memberships.message_rowid AND new.label_id=message_label_memberships.label_id)`, &timing.changedMemberships); err != nil {
		return err
	}
	if err := exec("INSERT OR IGNORE INTO message_label_memberships SELECT message_rowid,label_id FROM message_label_new_memberships", &timing.changedMemberships); err != nil {
		return err
	}
	timing.memberships = time.Since(started)
	return exec("DELETE FROM message_label_facts_dirty WHERE conversation_id IN (SELECT conversation_id FROM message_label_batch)", nil)
}

func (c *Catalog) refreshMessageLabelFacts(ctx context.Context) (int, error) {
	count, _, err := c.refreshMessageLabelFactsBudget(ctx, messageLabelFactsMessages)
	return count, err
}

// messageLabelFactsReadyPending is whether the whole queue is empty but the
// current version's readiness is not yet published.
func (c *Catalog) messageLabelFactsReadyPending(ctx context.Context) (bool, error) {
	var pending bool
	err := c.reader().QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM message_label_facts_dirty)
 AND COALESCE((SELECT value FROM meta WHERE key='message_label_facts_ready'),'')<>?`, messageLabelFactsVersion()).Scan(&pending)
	return pending, err
}

func (c *Catalog) refreshMessageLabelFactsBudget(ctx context.Context, messages int) (int, messageLabelFactsTiming, error) {
	return c.refreshMessageLabelFactsOf(ctx, messages, nil)
}

// refreshMessageLabelFactsOf is refreshMessageLabelFactsBudget over members
// (nil: the whole queue; see prepareMessageLabelFacts).
func (c *Catalog) refreshMessageLabelFactsOf(ctx context.Context, messages int, members []string) (int, messageLabelFactsTiming, error) {
	var timing messageLabelFactsTiming
	conn, err := c.DB.Conn(ctx)
	if err != nil {
		return 0, timing, err
	}
	defer conn.Close()
	// TEMP tables survive a committed read preparation. Clean them even when
	// publication fails, before returning the connection to the shared pool.
	defer func() {
		for _, table := range []string{"message_label_batch", "message_label_build", "message_label_old", "message_label_removed", "message_label_new_memberships"} {
			_, _ = conn.ExecContext(context.Background(), "DROP TABLE IF EXISTS temp."+table)
		}
	}()
	for attempt := 0; ; attempt++ {
		started := time.Now()
		read, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return 0, timing, err
		}
		build, err := prepareMessageLabelFacts(ctx, read, messages, members)
		if err != nil {
			_ = read.Rollback()
			return 0, timing, err
		}
		err = read.Commit()
		timing.prepare += time.Since(started)
		if err != nil || build.conversations == 0 && (build.ready || members != nil) {
			return 0, timing, err
		}
		started = time.Now()
		tx, finish, err := c.beginTrackedWriteUsing(ctx, "message-label-query-facts", conn)
		timing.wait += time.Since(started)
		if err != nil {
			return 0, timing, err
		}
		started = time.Now()
		if attempt >= 2 {
			var generation int64
			err = tx.QueryRowContext(ctx, "SELECT value FROM query_cache_generation WHERE id=1").Scan(&generation)
			// Sustained source writes must not starve maintenance. After two
			// stale snapshots, prepare this bounded batch under the writer.
			if err == nil && generation != build.generation {
				build, err = prepareMessageLabelFacts(ctx, tx, messages, members)
			}
		}
		// A selection another writer already published leaves nothing to
		// store, and is no claim about the whole queue.
		if err == nil && (members == nil || build.conversations > 0) {
			err = publishMessageLabelFacts(ctx, tx, build, &timing)
		}
		if errors.Is(err, errMessageLabelFactsStale) {
			_ = tx.Rollback()
			finish()
			continue
		}
		if err == nil {
			committing := time.Now()
			err = tx.Commit()
			timing.commit = time.Since(committing)
		}
		_ = tx.Rollback()
		timing.hold = time.Since(started)
		finish()
		if build.completed != nil {
			build.completed(err)
		}
		if timing.hold >= time.Second || timing.prepare >= time.Second {
			fmt.Fprintf(os.Stderr, "Message label query facts: conversations=%d messages=%d memberships=%d changed_facts=%d changed_memberships=%d prepare=%s writer_wait=%s facts=%s memberships=%s commit=%s writer_hold=%s error=%v\n",
				build.conversations, build.messages, build.memberships, timing.changedFacts, timing.changedMemberships,
				timing.prepare.Round(time.Millisecond), timing.wait.Round(time.Millisecond), timing.facts.Round(time.Millisecond), timing.memberships.Round(time.Millisecond), timing.commit.Round(time.Millisecond), timing.hold.Round(time.Millisecond), err)
		}
		return build.conversations, timing, err
	}
}

// A message budget adapts to the actual publication cost. Conversations stay
// atomic, so a single unusually large one can exceed this soft target.
func messageLabelFactsNextBudget(messages int, hold time.Duration) int {
	if hold <= 0 {
		return messages
	}
	next := int(float64(messages) * float64(messageLabelFactsTarget) / float64(hold))
	return max(1, min(next, messages*2, messageLabelFactsMessages*4))
}

func (c *Catalog) maintainMessageLabelFacts(ctx context.Context) {
	budget := messageLabelFactsMessages
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
			// Coalesce an idle-to-busy burst before preparing its first batch.
			// No transaction or writer gate is held during this window.
			select {
			case <-ctx.Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
			pass = c.beginPass(ctx, "maintenance.labels.query", triggerChained)
		}
		var count int
		var err error
		var timing messageLabelFactsTiming
		pass.runIO(ctx, func(ctx context.Context) { count, timing, err = c.refreshMessageLabelFactsBudget(ctx, budget) })
		if err == nil {
			pass.worked(count, "conversation")
			if count > 0 {
				budget = messageLabelFactsNextBudget(budget, timing.hold)
			}
		}
		// The writer gate is FIFO: waiting label saves run before the next
		// batch. An idle writer needs no fixed pause for every small commit.
		delay := time.Duration(0)
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
