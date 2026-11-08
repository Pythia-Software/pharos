package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// Compactions are boundary events, not the requests that happen to follow
// them. This retains consecutive boundaries and boundaries without later usage.
// Codex replayed boundaries have no attributed trigger and are left out, as
// they are in the Library's compaction count.
const compactionBodySQL = "CASE WHEN json_valid(m.text) THEN m.text ELSE '{}' END"
const compactionBoundarySQL = "m.kind='metadata' AND (json_extract(" + compactionBodySQL + ",'$.subtype')='compact_boundary' OR json_extract(" + compactionBodySQL + ",'$.compaction_trigger') IS NOT NULL)"

// compactionBoundaryVersion names the classification compaction_boundaries
// stores: compactionBoundarySQL and the trigger and evidence expressions in
// compactionBoundaryRows. Changing any of them must change this name, which
// rebuilds the table.
const compactionBoundaryVersion = "compactions-v1"

// compactionBoundaryColumns are compaction_boundaries' columns, in the order
// compactionBoundaryRows returns them.
const compactionBoundaryColumns = "message_id,conversation_id,created_at,model,boundary_trigger,trigger_evidence"

// compactionBoundaryRows classifies each boundary among the messages, aliased
// m, that {from} and the optional extra condition select. It is the only
// place that parses message bodies, at write time for the stored table and at
// query time only until that table's backfill completes.
func compactionBoundaryRows(from, where string) string {
	body := compactionBodySQL
	trigger := "COALESCE(json_extract(" + body + ",'$.compactMetadata.trigger'),json_extract(" + body + ",'$.compaction_trigger'))"
	if where != "" {
		where += " AND "
	}
	return "SELECT m.id message_id,m.conversation_id,m.created_at,m.model," +
		"CASE " + trigger + " WHEN 'manual' THEN 'human' WHEN 'auto' THEN 'machine' ELSE 'unknown' END boundary_trigger," +
		"CASE WHEN json_extract(" + body + ",'$.compactMetadata.trigger') IN ('manual','auto') THEN 'recorded' WHEN json_extract(" + body + ",'$.compaction_trigger') IN ('manual','auto') THEN 'inferred' ELSE 'unknown' END trigger_evidence" +
		" FROM " + from + " WHERE " + where + compactionBoundarySQL
}

// Select a transcript within each explicit native-alias family before applying
// chart filters. Only active conversations with boundary evidence compete, so
// an empty native transcript cannot hide its mirror's events. Prefer native
// evidence, then the fuller boundary transcript, with a stable identity tie.
// This population is independent of model requests and the tool-call rollup.
// A family is identified by its least member, and only the families of
// aliased conversations with boundaries are walked.
func compactionAliasExclusion(boundaries string) string {
	return `WITH RECURSIVE
 alias_edges(a,b) AS (SELECT left_id,right_id FROM conversation_identity_links WHERE relationship='native-alias'
 UNION SELECT right_id,left_id FROM conversation_identity_links WHERE relationship='native-alias'),
 boundaries AS (SELECT b.conversation_id,COUNT(*) n FROM (SELECT DISTINCT a FROM alias_edges) e JOIN ` + boundaries + ` b ON b.conversation_id=e.a GROUP BY b.conversation_id),
 reach(start,id) AS (SELECT conversation_id,conversation_id FROM boundaries UNION SELECT r.start,e.b FROM reach r JOIN alias_edges e ON e.a=r.id),
 families AS (SELECT start id,MIN(id) root FROM reach GROUP BY start)
 SELECT id FROM (SELECT c.id,ROW_NUMBER() OVER (PARTITION BY f.root ORDER BY
 (w.source_kind=c.provider) DESC,b.n DESC,c.id DESC) choice
 FROM boundaries b JOIN families f ON f.id=b.conversation_id JOIN conversations c ON c.id=b.conversation_id
 JOIN workspaces w ON w.id=c.workspace_id WHERE ` + currentGroupConversation("c") + `) WHERE choice>1`
}

// compactionDatasetOver is the compactions dataset over a boundary population
// with compactionBoundaryColumns: the stored table, or the same rows derived
// from message bodies.
func compactionDatasetOver(boundaries string) sqlDataset {
	columns := map[string]string{
		"id": "b.message_id", "message_id": "b.message_id", "conversation_id": "c.id", "workspace_id": "c.workspace_id",
		"occurred_at": "b.created_at", "day": "date(b.created_at,'localtime')", "week": "date(b.created_at,'localtime','-6 days','weekday 1')", "month": "strftime('%Y-%m',b.created_at,'localtime')",
		"trigger": "b.boundary_trigger", "trigger_evidence": "b.trigger_evidence",
		"provider": "c.provider", "harness": "c.harness", "model": "COALESCE(NULLIF(b.model,''),NULLIF(c.model,''),'Unreported')",
		"repository_name": "r.display_name", "title": "w.title", "source_kind": "w.source_kind", "agent_depth": "c.agent_depth",
		"channel": "o.channel", "machine_id": "o.machine_id", "machine": "o.machine", "execution_machine_id": "o.execution_machine_id", "execution_machine": "o.execution_machine",
	}
	return sqlDataset{from: `FROM ` + boundaries + ` b JOIN conversations c ON c.id=b.conversation_id
 JOIN workspaces w ON w.id=c.workspace_id LEFT JOIN repositories r ON r.id=w.repository_id
 LEFT JOIN conversation_origins o ON o.conversation_id=c.id`,
		base:    currentGroupConversation("c") + " AND c.id NOT IN (" + compactionAliasExclusion(boundaries) + ")",
		columns: columns, timestamps: map[string]string{"occurred_at": "b.created_at"}, lookups: map[string]bool{"conversation_id": true, "workspace_id": true, "message_id": true}}
}

// compactionDataset derives boundaries from message bodies on every query. It
// answers only until the stored table is complete, or for a catalog opened
// without Initialize; see Catalog.sqlDatasetFor.
var compactionDataset = compactionDatasetOver("(" + compactionBoundaryRows("messages m", "") + ")")

// compactionStoredDataset reads the few stored boundaries instead.
var compactionStoredDataset = compactionDatasetOver("compaction_boundaries")

// compactionBoundariesReady reports whether compaction_boundaries holds every
// boundary by the current classification.
func compactionBoundariesReady(ctx context.Context, db *sql.DB) (bool, error) {
	var ready bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM meta WHERE key='compaction_boundaries_version' AND value=?)
 AND NOT EXISTS(SELECT 1 FROM meta WHERE key='compaction_boundaries_cursor')`, compactionBoundaryVersion).Scan(&ready)
	return ready, err
}

// currentCompactionBoundaries is the boundary backfill for a page, which never
// waits for it or writes: queries derive the boundaries themselves until it
// finishes, so it only starts the tools stage that runs it, unless a sync has
// already deferred that stage.
func (c *Catalog) currentCompactionBoundaries(ctx context.Context) error {
	var pending, deferred bool
	if err := c.reader().QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM meta WHERE key='compaction_boundaries_cursor')").Scan(&pending); err != nil || !pending {
		return err
	}
	if err := c.reader().QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sync_deferred WHERE name='tools' AND generation>completed_generation)").Scan(&deferred); err != nil {
		return err
	}
	if !deferred {
		c.refreshToolRollup()
	}
	return nil
}

// ensureCompactionBoundaries installs the stored boundary population. Triggers
// classify every metadata message as it is written, in the writer's
// transaction, whichever build or process writes. A catalog that predates the
// table, or an older classification, starts over: the triggers are replaced,
// the rows cleared, and backfillCompactionBoundaries fills in the existing
// messages, which queries derive themselves until it finishes.
func (c *Catalog) ensureCompactionBoundaries() error {
	// Opening a current catalog need not wait for the writer.
	var current bool
	if err := c.DB.QueryRow("SELECT EXISTS(SELECT 1 FROM meta WHERE key='compaction_boundaries_version' AND value=?)", compactionBoundaryVersion).Scan(&current); err != nil || current {
		return err
	}
	tx, finish, err := c.beginTrackedWrite(context.Background(), "compaction-boundaries-schema")
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(); finish() }()
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS compaction_boundaries(message_id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL,
 created_at TEXT, model TEXT, boundary_trigger TEXT NOT NULL, trigger_evidence TEXT NOT NULL) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS compaction_boundaries_conversation_idx ON compaction_boundaries(conversation_id)`); err != nil {
		return err
	}
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM meta WHERE key='compaction_boundaries_version' AND value=?)", compactionBoundaryVersion).Scan(&current); err != nil || current {
		return err
	}
	insert := func(ref string) string {
		message := "(SELECT " + ref + ".id id," + ref + ".conversation_id conversation_id," + ref + ".kind kind," +
			ref + ".text text," + ref + ".created_at created_at," + ref + ".model model) m"
		return "INSERT OR REPLACE INTO compaction_boundaries(" + compactionBoundaryColumns + ") " + compactionBoundaryRows(message, "") + ";"
	}
	columns := []string{"id", "conversation_id", "kind", "text", "created_at", "model"}
	changed := make([]string, len(columns))
	for i, column := range columns {
		changed[i] = "OLD." + column + " IS NOT NEW." + column
	}
	statements := []string{}
	for _, trigger := range [][2]string{
		// An INSERT OR REPLACE of a message does not fire the delete trigger.
		{"insert", "AFTER INSERT ON messages WHEN NEW.kind='metadata' BEGIN DELETE FROM compaction_boundaries WHERE message_id=NEW.id; " + insert("NEW") + " END"},
		// Upserts name every column, so UPDATE OF alone fires for unchanged rows.
		{"update", "AFTER UPDATE OF " + strings.Join(columns, ",") + " ON messages WHEN (OLD.kind='metadata' OR NEW.kind='metadata') AND (" +
			strings.Join(changed, " OR ") + ") BEGIN DELETE FROM compaction_boundaries WHERE message_id=OLD.id; " + insert("NEW") + " END"},
		{"delete", "AFTER DELETE ON messages WHEN OLD.kind='metadata' BEGIN DELETE FROM compaction_boundaries WHERE message_id=OLD.id; END"},
	} {
		name := "compaction_boundaries_" + trigger[0]
		statements = append(statements, "DROP TRIGGER IF EXISTS "+name, "CREATE TRIGGER "+name+" "+trigger[1])
	}
	// The triggers keep every later write current, so the backfill only needs
	// the messages that exist now. A catalog without any metadata message,
	// such as a new one, is complete at once.
	statements = append(statements, "DELETE FROM compaction_boundaries",
		`INSERT INTO meta(key,value) VALUES('compaction_boundaries_version','`+compactionBoundaryVersion+`') ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		`INSERT INTO meta(key,value) SELECT 'compaction_boundaries_cursor','' WHERE EXISTS(SELECT 1 FROM messages WHERE kind='metadata')
 ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		`DELETE FROM meta WHERE key='compaction_boundaries_cursor' AND NOT EXISTS(SELECT 1 FROM messages WHERE kind='metadata')`)
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// compactionBoundaryBatch bounds each backfill transaction, so a sync waits
// for at most one batch of conversations.
var compactionBoundaryBatch = 200

// backfillCompactionBoundaries classifies the boundaries of existing messages
// a batch of conversations at a time, in conversation order, resuming from its
// cursor. It reads only metadata messages (messages_timing_metadata_idx).
func (c *Catalog) backfillCompactionBoundaries(ctx context.Context) error {
	insert := "INSERT OR REPLACE INTO compaction_boundaries(" + compactionBoundaryColumns + ") " +
		compactionBoundaryRows("messages m", "m.conversation_id IN (SELECT value FROM json_each(?1))")
	// Most runs have nothing to do; finding that out need not take the writer.
	var pending bool
	if err := c.reader().QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM meta WHERE key='compaction_boundaries_cursor')").Scan(&pending); err != nil || !pending {
		return err
	}
	for {
		done := false
		if err := c.writeTransaction(ctx, "compaction-boundaries", func(tx *sql.Tx) error {
			var cursor string
			if err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='compaction_boundaries_cursor'").Scan(&cursor); err == sql.ErrNoRows {
				done = true
				return nil
			} else if err != nil {
				return err
			}
			rows, err := queryMapsContext(ctx, tx, "SELECT id FROM conversations WHERE id>? ORDER BY id LIMIT ?", cursor, compactionBoundaryBatch)
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				done = true
				_, err := tx.ExecContext(ctx, "DELETE FROM meta WHERE key='compaction_boundaries_cursor'")
				return err
			}
			ids := make([]string, len(rows))
			for i, row := range rows {
				ids[i] = firstString(row["id"])
			}
			batch, err := json.Marshal(ids)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, insert, string(batch)); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, "UPDATE meta SET value=? WHERE key='compaction_boundaries_cursor'", ids[len(ids)-1])
			return err
		}); err != nil || done {
			return err
		}
	}
}
