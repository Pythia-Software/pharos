package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// The Library shows derived per-workspace columns (turn and tool counts, PR
// lists, changed files, ...). Computing them scans each workspace's messages,
// which is far too slow to repeat for every workspace on every request, so
// they are stored in workspace_library:
//
//   - Triggers on every table those columns read mark the affected workspace
//     in workspace_library_dirty. No write path has to remember to refresh.
//   - Reads combine stored rows for clean workspaces with a live computation
//     for dirty ones in a single statement, so results are never stale.
//   - RefreshLibrary recomputes dirty workspaces in small write batches.
//
// To add a derived column, add it to libraryColumns; if it reads a table not
// in libraryDependencies, add that table too. Each column is versioned on its
// own (see ensureLibrary): adding one, or changing one's expression, computes
// that column again for every stored workspace in the background, and leaves
// the other columns' stored values and the marks alone.

type libraryColumn struct{ name, expr string }

func currentGroupConversation(alias string) string {
	return "NOT EXISTS(SELECT 1 FROM conversation_group_membership gm WHERE gm.conversation_id=" + alias + ".id AND gm.active=0)"
}

// libraryColumns are evaluated per workspace with the workspace aliased as w.
var libraryColumns = []libraryColumn{
	{"conversation_count", "(SELECT COUNT(*) FROM conversations cx WHERE cx.workspace_id=w.id AND " + currentGroupConversation("cx") + ")"},
	// Every user message, including one on a branch since abandoned: it was
	// sent. Both indexes cover what is read of them, so no message row is
	// fetched: each of the conversation's messages is looked up, by time and
	// rowid, among the user messages in messages_prose_idx.
	// messages_label_conversation_idx would be one read, but it leaves out
	// abandoned branches.
	{"turn_count", `(SELECT COUNT(*) FROM conversations tx JOIN messages tm INDEXED BY messages_conversation_idx ON tm.conversation_id=tx.id
		WHERE tx.workspace_id=w.id AND ` + currentGroupConversation("tx") + `
		AND EXISTS(SELECT 1 FROM messages tu INDEXED BY messages_prose_idx WHERE tu.role='user' AND tu.kind='message' AND tu.created_at IS tm.created_at AND tu.rowid=tm.rowid))`},
	{"changed_file_count", "(SELECT COUNT(*) FROM change_sets csx JOIN change_files cfx ON cfx.change_set_id=csx.id WHERE csx.workspace_id=w.id)"},
	// A unary + on name keeps metrics found through their workspace, not
	// metrics_name_value_idx, which holds every workspace's metric of that name.
	{"is_estimate", `EXISTS(SELECT 1 FROM agent_sessions ax JOIN conversations cx ON cx.id=ax.conversation_id WHERE ax.workspace_id=w.id AND ` + currentGroupConversation("cx") + ` AND ax.is_estimate=1)`},
	{"token_count", `COALESCE(
		(SELECT MAX(mx.value) FROM metrics mx WHERE mx.workspace_id=w.id AND +mx.name='total_tokens'),
		(SELECT SUM(mx.value) FROM metrics mx WHERE mx.workspace_id=w.id AND +mx.name IN ('input_tokens','output_tokens','cache_tokens')), CASE WHEN EXISTS(SELECT 1 FROM agent_sessions ax WHERE ax.workspace_id=w.id AND ax.is_estimate=1) THEN 0 END) + COALESCE((SELECT SUM(ax.total_tokens) FROM agent_sessions ax JOIN conversations cx ON cx.id=ax.conversation_id WHERE ax.workspace_id=w.id AND ` + currentGroupConversation("cx") + ` AND ax.is_estimate=1),0)`},
	// Tool calls and their failures are the tool ledger's (tool_calls), as
	// in the workspace metrics. It holds each call once, where a transcript
	// that replays earlier entries (a resumed or forked Codex session) holds
	// the call's messages again. A failure is a call the ledger gave the
	// status error: a result flagged as an error, a nonzero exit, a timeout,
	// a rejection. It excuses an exit status of 1 that only reports nothing
	// found (rg, grep, diff) for a command that runs one program, so a
	// chained command ending that way still counts.
	{"tool_use_count", `MAX(
		(SELECT COUNT(*) FROM conversations tc JOIN tool_calls tl ON tl.conversation_id=tc.id WHERE tc.workspace_id=w.id AND ` + currentGroupConversation("tc") + `),
		COALESCE((SELECT SUM(mx.value) FROM metrics mx WHERE mx.workspace_id=w.id AND +mx.name='tool_calls' AND mx.extractor_version<>'core-v1'),0))`},
	{"tool_error_count", `MAX(
		(SELECT COUNT(*) FROM conversations ec JOIN tool_calls el ON el.conversation_id=ec.id WHERE ec.workspace_id=w.id AND ` + currentGroupConversation("ec") + ` AND el.status='error'),
		COALESCE((SELECT SUM(mx.value) FROM metrics mx WHERE mx.workspace_id=w.id AND +mx.name='tool_errors' AND mx.extractor_version<>'core-v1'),0))`},
	// compaction_boundaries holds the compactions that count (compactions.go):
	// Claude's compact_boundary events, and of Codex's, which replays earlier
	// ones when a session resumes or forks, those attributed to a turn.
	{"compaction_count", `(SELECT COUNT(*) FROM conversations kc JOIN compaction_boundaries kb ON kb.conversation_id=kc.id WHERE kc.workspace_id=w.id AND ` + currentGroupConversation("kc") + `)`},
	// A child conversation's main session replaces its linked delegation in
	// this count. Unlinked children may belong to another workspace or still
	// have a delegation without a result.
	{"subagent_count", `(SELECT COUNT(*) FROM agent_sessions ax JOIN conversations cx ON cx.id=ax.conversation_id
		WHERE ax.workspace_id=w.id AND ` + currentGroupConversation("cx") + ` AND ((ax.native_id<>'main' AND ax.usage_status<>'in-child-conversation')
		OR (ax.native_id='main' AND cx.agent_depth>0 AND EXISTS
			(SELECT 1 FROM agent_sessions delegation WHERE delegation.child_conversation_id=cx.id))))`},
	{"subagent_depth", `COALESCE(
		(SELECT MAX(ax.depth) FROM agent_sessions ax JOIN conversations cx ON cx.id=ax.conversation_id WHERE ax.workspace_id=w.id AND ` + currentGroupConversation("cx") + `
			AND ((ax.native_id<>'main' AND ax.usage_status<>'in-child-conversation') OR
				(ax.native_id='main' AND cx.agent_depth>0 AND EXISTS
					(SELECT 1 FROM agent_sessions delegation WHERE delegation.child_conversation_id=cx.id))))
		-(SELECT MIN(ax.depth) FROM agent_sessions ax WHERE ax.workspace_id=w.id),0)`},
	{"pr_count", "(SELECT COUNT(DISTINCT lx.pr_id) FROM work_pr_links lx WHERE lx.workspace_id=w.id)"},
	{"pr_numbers", "(SELECT GROUP_CONCAT(prx.number) FROM work_pr_links lx JOIN pull_requests prx ON prx.id=lx.pr_id WHERE lx.workspace_id=w.id)"},
	{"pr_details", "(SELECT json_group_array(json_object('number',prx.number,'title',prx.title,'url',prx.url,'host',prx.host)) FROM work_pr_links lx JOIN pull_requests prx ON prx.id=lx.pr_id WHERE lx.workspace_id=w.id)"},
	{"providers", "(SELECT GROUP_CONCAT(DISTINCT cx.provider) FROM conversations cx WHERE cx.workspace_id=w.id)"},
	{"models", `(SELECT GROUP_CONCAT(DISTINCT model) FROM (
		SELECT cx.model FROM conversations cx WHERE cx.workspace_id=w.id AND cx.model IS NOT NULL
		UNION SELECT mx.model FROM conversations cx JOIN messages mx ON mx.conversation_id=cx.id
		WHERE cx.workspace_id=w.id AND cx.harness='claude-ai-export' AND mx.model IS NOT NULL AND mx.role='assistant' AND NULLIF(TRIM(mx.model),'') IS NOT NULL
		UNION SELECT ` + claudeSelectionValue("cx", "model") + ` FROM conversations cx WHERE cx.workspace_id=w.id AND cx.harness='claude-ai-export' AND ` + claudeNoRecordedModel("cx") + `))`},
	{"efforts", `(SELECT GROUP_CONCAT(DISTINCT effort) FROM (
		SELECT cx.effort FROM conversations cx WHERE cx.workspace_id=w.id AND cx.effort IS NOT NULL
		UNION SELECT mx.effort FROM messages mx JOIN conversations cx ON cx.id=mx.conversation_id WHERE cx.workspace_id=w.id AND mx.effort IS NOT NULL
		UNION SELECT CASE WHEN ` + claudeSelectionValue("cx", "model") + ` IS NOT NULL
			THEN COALESCE(` + claudeSelectionValue("cx", "effort_level") + `,'Unknown effort')
			ELSE ` + claudeSelectionValue("cx", "effort_level") + ` END
		FROM conversations cx WHERE cx.workspace_id=w.id AND cx.harness='claude-ai-export' AND ` + claudeNoRecordedModel("cx") + `))`},
	// De-duplicate raw pairs before crossing into the Go formatting function.
	// Most messages repeat a configuration; formatting each one costs far more
	// than scanning their pairs in the covering index.
	{"model_efforts", `(SELECT GROUP_CONCAT(configuration, ', ') FROM (
		SELECT DISTINCT model_effort(model,effort) configuration FROM (
			SELECT cx.model,cx.effort FROM conversations cx WHERE cx.workspace_id=w.id AND (cx.model IS NOT NULL OR cx.effort IS NOT NULL)
			UNION SELECT COALESCE(mx.model,cx.model),mx.effort FROM messages mx JOIN conversations cx ON cx.id=mx.conversation_id
			WHERE cx.workspace_id=w.id AND (mx.model IS NOT NULL OR mx.effort IS NOT NULL)
			UNION SELECT ` + claudeSelectionValue("cx", "model") + `,` + claudeSelectionValue("cx", "effort_level") + ` FROM conversations cx
			WHERE cx.workspace_id=w.id AND cx.harness='claude-ai-export' AND ` + claudeNoRecordedModel("cx") + `) WHERE model IS NOT NULL OR effort IS NOT NULL ORDER BY configuration))`},
	{"harnesses", "(SELECT GROUP_CONCAT(DISTINCT cx.harness) FROM conversations cx WHERE cx.workspace_id=w.id AND cx.harness IS NOT NULL)"},
	{"harness_versions", "(SELECT GROUP_CONCAT(DISTINCT cx.harness_version_last) FROM conversations cx WHERE cx.workspace_id=w.id AND cx.harness_version_last IS NOT NULL)"},
	{"changed_files", "(SELECT GROUP_CONCAT(DISTINCT cfx.path) FROM change_sets csx JOIN change_files cfx ON cfx.change_set_id=csx.id WHERE csx.workspace_id=w.id)"},
	{"error_types", "(SELECT GROUP_CONCAT(DISTINCT mlx.error_type) FROM metric_ledger mlx WHERE mlx.workspace_id=w.id AND mlx.error_type IS NOT NULL)"},
	{"summary_initiation", "(SELECT sx.initiation FROM summaries sx WHERE sx.workspace_id=w.id)"},
	{"summary_outcome", "(SELECT sx.outcome FROM summaries sx WHERE sx.workspace_id=w.id)"},
}

// libraryDependency maps a changed row (referenced as NEW or OLD) to the
// workspaces whose derived columns it can affect, selected as a column "id".
type libraryDependency struct {
	table      string
	workspaces func(row string) string
	insertOnly bool
}

func workspaceColumn(row string) string { return "SELECT " + row + ".workspace_id AS id" }

// conversationWorkspace finds the workspace of the conversation a row is in.
func conversationWorkspace(row string) string {
	return "SELECT workspace_id AS id FROM conversations WHERE id=" + row + ".conversation_id"
}

var libraryDependencies = []libraryDependency{
	// Workspace columns are read live; a new workspace only needs a stored row.
	{table: "workspaces", workspaces: func(row string) string { return "SELECT " + row + ".id AS id" }, insertOnly: true},
	{table: "conversations", workspaces: workspaceColumn},
	{table: "claude_chat_settings_observations", workspaces: func(row string) string {
		return "SELECT workspace_id AS id FROM conversations WHERE harness='claude-ai-export' AND account=" + row + ".account AND native_id=" + row + ".conversation_uuid"
	}},
	{table: "conversation_group_membership", workspaces: workspaceColumn},
	{table: "messages", workspaces: conversationWorkspace},
	// Both are counted through their conversations, as messages are.
	{table: "tool_calls", workspaces: conversationWorkspace},
	{table: "compaction_boundaries", workspaces: conversationWorkspace},
	{table: "change_sets", workspaces: workspaceColumn},
	{table: "change_files", workspaces: func(row string) string {
		return "SELECT workspace_id AS id FROM change_sets WHERE id=" + row + ".change_set_id"
	}},
	{table: "agent_sessions", workspaces: workspaceColumn},
	{table: "metrics", workspaces: workspaceColumn},
	{table: "metric_ledger", workspaces: workspaceColumn},
	{table: "work_pr_links", workspaces: workspaceColumn},
	{table: "pull_requests", workspaces: func(row string) string {
		return "SELECT workspace_id AS id FROM work_pr_links WHERE pr_id=" + row + ".id"
	}},
	{table: "summaries", workspaces: workspaceColumn},
}

func libraryTableDDL() string {
	columns := []string{"workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE"}
	for _, column := range libraryColumns {
		columns = append(columns, column.name)
	}
	return "CREATE TABLE workspace_library (\n  " + strings.Join(columns, ",\n  ") + "\n)"
}

// libraryViewStatements are the statements the projection evaluates: each
// stored column for a workspace w, and the preview IDs.
func libraryViewStatements() []string {
	statements := make([]string, 0, len(libraryColumns)+1)
	for _, column := range libraryColumns {
		statements = append(statements, "SELECT "+column.expr+" FROM workspaces w")
	}
	return append(statements, libraryPreviewSelect("1"))
}

// libraryTriggerDDL marks the workspaces a write can change. An insert or a
// delete always can. An update can only when it changes a column the view
// reads (projectionChangeCondition), so a writer that stores a row again
// unchanged, or advances a column the view never reads, marks nothing.
func libraryTriggerDDL(q queryer) ([]string, error) {
	statements := []string{}
	view := libraryViewStatements()
	for _, dependency := range libraryDependencies {
		name := "workspace_library_" + dependency.table
		statements = append(statements, fmt.Sprintf("CREATE TRIGGER %s_insert AFTER INSERT ON %s BEGIN %s END", name, dependency.table, dependency.mark("NEW")))
		if dependency.insertOnly {
			continue
		}
		changed, err := projectionChangeCondition(q, dependency.table, append(slices.Clone(view), dependency.workspaces("NEW")))
		if err != nil {
			return nil, err
		}
		statements = append(statements,
			fmt.Sprintf("CREATE TRIGGER %s_update AFTER UPDATE ON %s WHEN %s BEGIN %s %s END", name, dependency.table, changed, dependency.mark("OLD"), dependency.mark("NEW")),
			fmt.Sprintf("CREATE TRIGGER %s_delete AFTER DELETE ON %s BEGIN %s END", name, dependency.table, dependency.mark("OLD")))
		// A write to a workspace a refresh is computing (refreshLibrary)
		// releases its claim, so what was computed is not stored. Claims
		// exist only while one computes, so these bodies nearly never run.
		const claimed = "EXISTS(SELECT 1 FROM workspace_library_claims)"
		name = "workspace_library_release_" + dependency.table
		statements = append(statements,
			fmt.Sprintf("CREATE TRIGGER %s_insert AFTER INSERT ON %s WHEN %s BEGIN %s END", name, dependency.table, claimed, dependency.release("NEW")),
			fmt.Sprintf("CREATE TRIGGER %s_update AFTER UPDATE ON %s WHEN %s AND (%s) BEGIN %s %s END", name, dependency.table, claimed, changed, dependency.release("OLD"), dependency.release("NEW")),
			fmt.Sprintf("CREATE TRIGGER %s_delete AFTER DELETE ON %s WHEN %s BEGIN %s END", name, dependency.table, claimed, dependency.release("OLD")))
	}
	// So does marking it by hand, as a repair does for what no trigger saw.
	// BEFORE: an insert that is ignored because the workspace is already
	// marked still gets here.
	statements = append(statements, `CREATE TRIGGER workspace_library_release_marked BEFORE INSERT ON workspace_library_dirty
		WHEN EXISTS(SELECT 1 FROM workspace_library_claims) BEGIN DELETE FROM workspace_library_claims WHERE workspace_id=NEW.workspace_id; END`)
	return statements, nil
}

func (d libraryDependency) release(row string) string {
	return "DELETE FROM workspace_library_claims WHERE workspace_id IN (" + d.workspaces(row) + ");"
}

// An outer INSERT OR REPLACE/ABORT would override a trigger's OR IGNORE,
// so insert only unmarked IDs and never raise a conflict at all.
func (d libraryDependency) mark(row string) string {
	return "INSERT INTO workspace_library_dirty(workspace_id) SELECT DISTINCT id FROM (" +
		d.workspaces(row) + ") WHERE id IS NOT NULL AND id NOT IN (SELECT workspace_id FROM workspace_library_dirty);"
}

// libraryDerivedSelect computes stored-column values for workspaces matching
// filter, in workspace_library's column order.
func libraryDerivedSelect(filter string) string {
	columns := []string{"w.id workspace_id"}
	for _, column := range libraryColumns {
		columns = append(columns, column.expr+" "+column.name)
	}
	return "SELECT " + strings.Join(columns, ",\n\t") + "\n\tFROM workspaces w WHERE " + filter
}

// libraryVersion identifies how the projection is stored: a row per workspace
// with a column per libraryColumns entry, each versioned on its own in
// workspace_library_columns. It changes only with that layout, which rebuilds
// the whole projection. What a column holds is not part of it.
func libraryVersion() string { return "columns-v1" }

// digest identifies what a stored column holds: its expression.
func (column libraryColumn) digest() string {
	digest := sha256.Sum256([]byte(column.expr))
	return hex.EncodeToString(digest[:])
}

// libraryLegacyVersion is workspace_library_version as the last build that
// versioned the projection as a whole wrote it, and libraryLegacyDigests are
// the digests of the column expressions its rows were computed with. A
// catalog arriving from that build keeps its rows; only the columns defined
// differently since are computed again. Both record that build: a later
// change to a column never updates them.
const libraryLegacyVersion = "09dc2efccd6c2ead41b28d3c71c3504554c7fb1fd3477d1c749fb38f75aa9076"

var libraryLegacyDigests = map[string]string{
	"conversation_count": "24e4d417eb3baf5c00d369f554c0e4112c116aa2a01cfb6d5c43617f38ed51ce",
	"turn_count":         "9d7f7c01d9c0197b3064be8037ebff9b4d7710700149b4bb2ee10dd253532d8d",
	"changed_file_count": "138a616e3b3a8087be3a3fec888a81d3f20fa7fd6a6bbb61ca4a32ba567e92f8",
	"is_estimate":        "92872124b8438365cd9c292f88dc080f4a4dd34f17ddb42bcd46058e64f3ecd2",
	"token_count":        "81c97a35bddaa8f04cdcf12bdd8eaa048d1eef68a418ba62698ef3ea691a4c37",
	"tool_use_count":     "7609f0781f32aa3b775acfbba3dbf9a429c26fe3e3d433eb177b5eb9b544eac4",
	"tool_error_count":   "998d6827cad7fdc762c7ec7082e7f631ce9262dcc6770536afb239cd8a3f530f",
	"compaction_count":   "b0abd79c3e85809cbc9d1f2d30e6bbca30eb51a2aae98b61408b81ed16d94e00",
	"subagent_count":     "8c4ec8801328d282b4a4dd91f0d69582c783827c442ea79b13819be351792323",
	"subagent_depth":     "a19ed465b756a9577a02b6054c40b7f3a5468d2031455188dd5e82336d0f22da",
	"pr_count":           "490e27e8f761d10de18f666bb3c92cdd055dcce8eb9911e1b49099d16e4a63ed",
	"pr_numbers":         "ffb71300867c29ad1296940959bcc1dea47bb0740d4819e1653ac18d56ad7f6e",
	"pr_details":         "b6754efb3a8acec9a4d000633c821bf9f05310800b03f2fedf1ed075aba7d894",
	"providers":          "2bbaefd531ed1695a2d55b2cd021620dbdedd212c2d29ef852b97a3c1b7cd88f",
	"models":             "a4ab57cc013c8953b0b7e9ca0b26abda3450d05976432e86eb70d4c0d72edc3c",
	"efforts":            "3fdf4037d3219579baee939eeb5f03400d7208b465866493dee863689cbbb9fe",
	"model_efforts":      "8c11c78436c24654e41857b1211f6042044c60226778c6d922d8f0e639d90c0f",
	"harnesses":          "ff968fa662e89be091d1fcd45b4d8148e952fe45115c692957df3771addb010b",
	"harness_versions":   "059bb443383b660bb704a8183a7b88bf58878e222e7642eb082b76d41c90662c",
	"changed_files":      "422a0d7056134dd1a43e1f6a1769062364292fa53cf2ce04307438be79a9ec34",
	"error_types":        "a0a08940774e4f1c7ff8251323fb6d0df52eaa534d338e9cfbd23b317f73be0d",
	"summary_initiation": "5027be95de2d774e9dd07465c2eace2b5a1299d689aa5a6bde63487aaf235350",
	"summary_outcome":    "b0104d3734d03a0d3ffcbe5c584e41cd5a233572c73a5dc7eb23e18cbe5f835a",
}

// libraryChanges is how the stored projection differs from this build's.
type libraryChanges struct {
	// rebuild: nothing stored is usable. The table is created again and
	// every workspace marked.
	rebuild bool
	// legacy: the rows were stored by libraryLegacyVersion.
	legacy bool
	// add are columns the table lacks; compute are those and the columns
	// stored under another definition; remove are stored columns no longer
	// defined.
	add, compute []libraryColumn
	remove       []string
	// triggers: the installed marking triggers are not this build's.
	triggers bool
}

func (l libraryChanges) none() bool {
	return !l.rebuild && !l.legacy && !l.triggers && len(l.add)+len(l.compute)+len(l.remove) == 0
}

func findLibraryChanges(q queryer, triggers []string) (libraryChanges, error) {
	var changes libraryChanges
	version := libraryVersion()
	meta := map[string]string{}
	rows, err := queryMaps(q, "SELECT key,value FROM meta WHERE key IN ('workspace_library_version',?)", libraryTriggersKey)
	if err != nil {
		return changes, err
	}
	for _, row := range rows {
		meta[firstString(row["key"])] = firstString(row["value"])
	}
	changes.triggers = meta[libraryTriggersKey] != version+":"+triggerDigest(triggers)
	if rows, err = queryMaps(q, "SELECT name FROM pragma_table_info('workspace_library')"); err != nil {
		return changes, err
	}
	stored := map[string]bool{}
	for _, row := range rows {
		stored[firstString(row["name"])] = true
	}
	digests := map[string]string{}
	switch current := meta["workspace_library_version"]; {
	case len(stored) == 0:
		changes.rebuild = true
	case current == version:
		if rows, err = queryMaps(q, "SELECT name,digest FROM workspace_library_columns"); err != nil {
			return changes, err
		}
		for _, row := range rows {
			digests[firstString(row["name"])] = firstString(row["digest"])
		}
	case current == libraryLegacyVersion:
		changes.legacy, digests = true, libraryLegacyDigests
	default:
		changes.rebuild = true
	}
	if changes.rebuild {
		return changes, nil
	}
	defined := map[string]bool{}
	for _, column := range libraryColumns {
		defined[column.name] = true
		if !stored[column.name] {
			changes.add = append(changes.add, column)
		}
		if !stored[column.name] || digests[column.name] != column.digest() {
			changes.compute = append(changes.compute, column)
		}
	}
	for name := range stored {
		if name != "workspace_id" && !defined[name] {
			changes.remove = append(changes.remove, name)
		}
	}
	slices.Sort(changes.remove)
	return changes, nil
}

// ensureLibrary creates the stored projection and its triggers, and brings
// stored rows to this build's column definitions without computing whole rows
// again:
//
//   - A column whose expression changed keeps its stored values, and every
//     stored workspace is queued in workspace_library_column_pending to have
//     that one column computed again (refreshLibraryColumns). Readers see the
//     earlier value until then.
//   - A new column is added empty, NULL meaning not yet known, and queued the
//     same way. A removed one is dropped.
//   - Changed triggers are replaced. When a workspace is marked does not
//     change what is stored for it.
//
// None of these marks a workspace. Only a catalog with no rows this build can
// account for (new, or from before libraryLegacyVersion) is rebuilt, which
// marks every workspace; reads stay correct meanwhile.
func (c *Catalog) ensureLibrary() error {
	if _, err := c.DB.Exec(`CREATE TABLE IF NOT EXISTS workspace_library_dirty (
		workspace_id TEXT PRIMARY KEY) WITHOUT ROWID;
		CREATE TABLE IF NOT EXISTS workspace_library_claims (
		workspace_id TEXT PRIMARY KEY, token TEXT NOT NULL, claimed_at INTEGER NOT NULL) WITHOUT ROWID;
		CREATE TABLE IF NOT EXISTS workspace_library_columns (
		name TEXT PRIMARY KEY, digest TEXT NOT NULL) WITHOUT ROWID;
		CREATE TABLE IF NOT EXISTS workspace_library_column_pending (
		column_name TEXT NOT NULL, workspace_id TEXT NOT NULL, PRIMARY KEY(column_name,workspace_id)) WITHOUT ROWID;
		CREATE INDEX IF NOT EXISTS workspace_library_column_pending_workspace ON workspace_library_column_pending(workspace_id)`); err != nil {
		return err
	}
	if err := c.ensurePreviewQueue(); err != nil {
		return err
	}
	triggers, err := libraryTriggerDDL(c.DB)
	if err != nil {
		return err
	}
	// Nearly every open finds nothing to change, without waiting for the writer.
	if changes, err := findLibraryChanges(c.DB, triggers); err != nil || changes.none() {
		return err
	}
	tx, err := c.beginWrite(context.Background())
	if err != nil {
		return err
	}
	defer tx.Rollback()
	changes, err := findLibraryChanges(tx, triggers)
	if err != nil {
		return err
	}
	exec := func(statement string, args ...any) error {
		if _, err := tx.Exec(statement, args...); err != nil {
			return fmt.Errorf("update library projection: %w", err)
		}
		return nil
	}
	record := func(name, digest string) error {
		return exec("INSERT INTO workspace_library_columns(name,digest) VALUES(?,?) ON CONFLICT(name) DO UPDATE SET digest=excluded.digest", name, digest)
	}
	// workspace_query_stats held token_count before workspace_library did.
	statements := []string{"DROP TABLE IF EXISTS workspace_query_stats", "DELETE FROM meta WHERE key='workspace_query_stats_version'"}
	if changes.rebuild || changes.legacy {
		statements = append(statements, "DELETE FROM workspace_library_columns", "DELETE FROM workspace_library_column_pending")
	}
	if changes.rebuild {
		statements = append(statements, "DROP TABLE IF EXISTS workspace_library", libraryTableDDL())
	}
	for _, statement := range statements {
		if err := exec(statement); err != nil {
			return err
		}
	}
	switch {
	case changes.rebuild:
		for _, column := range libraryColumns {
			if err := record(column.name, column.digest()); err != nil {
				return err
			}
		}
	case changes.legacy:
		for name, digest := range libraryLegacyDigests {
			if err := record(name, digest); err != nil {
				return err
			}
		}
	}
	for _, column := range changes.add {
		if err := exec(`ALTER TABLE workspace_library ADD COLUMN "` + column.name + `"`); err != nil {
			return err
		}
	}
	for _, column := range changes.compute {
		if err := record(column.name, column.digest()); err != nil {
			return err
		}
		// A marked workspace has every column computed by its refresh.
		if err := exec(`INSERT OR IGNORE INTO workspace_library_column_pending(column_name,workspace_id)
			SELECT ?,workspace_id FROM workspace_library WHERE workspace_id NOT IN (SELECT workspace_id FROM workspace_library_dirty)`, column.name); err != nil {
			return err
		}
	}
	for _, name := range changes.remove {
		for _, statement := range []string{"DELETE FROM workspace_library_columns WHERE name=?", "DELETE FROM workspace_library_column_pending WHERE column_name=?"} {
			if err := exec(statement, name); err != nil {
				return err
			}
		}
		// Nothing reads a column that is not defined, so one that SQLite
		// will not drop only stays.
		_, _ = tx.Exec(`ALTER TABLE workspace_library DROP COLUMN "` + name + `"`)
	}
	if changes.triggers || changes.rebuild {
		if err := installLibraryTriggers(tx, libraryVersion(), triggers); err != nil {
			return err
		}
	}
	if changes.rebuild {
		if err := exec("INSERT OR IGNORE INTO workspace_library_dirty(workspace_id) SELECT id FROM workspaces"); err != nil {
			return err
		}
	}
	if err := exec("INSERT OR REPLACE INTO meta(key,value) VALUES('workspace_library_version',?)", libraryVersion()); err != nil {
		return err
	}
	return tx.Commit()
}

// libraryTriggersKey holds the digest of the installed marking triggers.
const libraryTriggersKey = "workspace_library_triggers"

// installLibraryTriggers replaces the projection's triggers, including an
// earlier build's, and records which are installed. It marks nothing.
func installLibraryTriggers(tx *sql.Tx, version string, triggers []string) error {
	names, err := queryMaps(tx, "SELECT name FROM sqlite_master WHERE type='trigger' AND (name LIKE 'workspace_library_%' OR name LIKE 'workspace_query_stats_%')")
	if err != nil {
		return err
	}
	for _, row := range names {
		if _, err := tx.Exec(`DROP TRIGGER IF EXISTS "` + firstString(row["name"]) + `"`); err != nil {
			return fmt.Errorf("rebuild library projection: %w", err)
		}
	}
	for _, statement := range triggers {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("rebuild library projection: %w", err)
		}
	}
	_, err = tx.Exec("INSERT OR REPLACE INTO meta(key,value) VALUES(?,?)", libraryTriggersKey, version+":"+triggerDigest(triggers))
	return err
}

// RefreshLibrary recomputes up to batch dirty workspaces' Search view rows and
// then up to batch dirty workspaces' metrics, and reports the larger count.
// The service maintains the two separately (maintainLibrary,
// maintainWorkspaceMetrics); this is for commands and tests without one.
func (c *Catalog) RefreshLibrary(ctx context.Context, batch int) (int, error) {
	count, err := c.refreshLibrary(ctx, batch, nil)
	if err != nil {
		return count, err
	}
	metrics, err := c.refreshMetricProjections(ctx, batch)
	return max(count, metrics), err
}

func (c *Catalog) refreshLibraryWorkspaces(ctx context.Context, workspaces []string) error {
	reportWork(ctx, "refreshing the Library view", "workspace", 0, len(workspaces))
	for offset := 0; offset < len(workspaces); offset += 10 {
		if _, err := c.refreshLibrary(ctx, 10, workspaces[offset:min(offset+10, len(workspaces))]); err != nil {
			return err
		}
		reportWork(ctx, "refreshing the Library view", "workspace", min(offset+10, len(workspaces)), len(workspaces))
	}
	return nil
}

// libraryClaimAbandoned is how old a claim is before another refresh may
// take its workspaces: longer than any batch takes to compute.
var libraryClaimAbandoned = 10 * time.Minute

// libraryComputedHook, when set (tests), runs after a refresh has computed
// its batch and before it stores it.
var libraryComputedHook func()

// refreshLibrary stores the Search view rows and previews of up to batch
// dirty workspaces (among workspaces, when given) and reports how many it
// attempted.
func (c *Catalog) refreshLibrary(ctx context.Context, batch int, workspaces []string) (int, error) {
	attempted, _, err := c.refreshLibraryBatch(ctx, batch, workspaces)
	return attempted, err
}

// refreshLibraryBatch is refreshLibrary, also reporting how many workspaces
// it stored. Marked workspaces come first. With none left to refresh, it
// brings stored rows to changed column definitions instead
// (refreshLibraryColumns), so every caller that keeps the view current
// finishes that too.
func (c *Catalog) refreshLibraryBatch(ctx context.Context, batch int, workspaces []string) (attempted, stored int, err error) {
	attempted, stored, err = c.refreshMarkedLibraryBatch(ctx, batch, workspaces)
	if err != nil || attempted > 0 || workspaces != nil {
		return attempted, stored, err
	}
	pass, err := c.refreshLibraryColumns(ctx, libraryColumnBatch)
	return pass.workspaces, pass.workspaces, err
}

// refreshMarkedLibraryBatch stores the rows of up to batch marked workspaces.
// Computing a workspace reads all its messages, seconds for a large one on a
// busy drive, so that happens on a read snapshot. The catalog writer is held
// twice, briefly: to claim the workspaces, and to store what was computed for
// those still claimed. A workspace stays marked meanwhile, so reads keep
// computing it live; a write to it releases its claim (see
// libraryTriggerDDL), and it stays marked for the next pass.
func (c *Catalog) refreshMarkedLibraryBatch(ctx context.Context, batch int, workspaces []string) (attempted, stored int, err error) {
	// A workspace another refresh has claimed (the service's loop and a
	// command's overlap) is that refresh's to store.
	abandoned := time.Now().Add(-libraryClaimAbandoned).Unix()
	where, args := " WHERE workspace_id NOT IN (SELECT workspace_id FROM workspace_library_claims WHERE claimed_at>=?)", []any{abandoned}
	if workspaces != nil {
		where += " AND workspace_id IN (SELECT value FROM json_each(?))"
		args = append(args, jsonText(workspaces))
	}
	// Nothing to do: return without waiting for the writer.
	pending, err := queryMapsContext(ctx, c.reader(), `SELECT workspace_id FROM workspace_library_dirty`+where+` LIMIT ?`, append(args, batch)...)
	if err != nil || len(pending) == 0 {
		return 0, 0, err
	}
	ids := make([]string, 0, len(pending))
	for _, row := range pending {
		ids = append(ids, firstString(row["workspace_id"]))
	}
	token := randomToken()
	if err := c.writeTransaction(ctx, "library-refresh claim", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM workspace_library_claims WHERE claimed_at<?`, abandoned); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO workspace_library_claims(workspace_id,token,claimed_at)
			SELECT workspace_id,?,? FROM workspace_library_dirty WHERE workspace_id IN (SELECT value FROM json_each(?))`, token, time.Now().Unix(), jsonText(ids))
		return err
	}); err != nil {
		return 0, 0, err
	}
	released := false
	defer func() {
		// A batch given up (stopped, or failed) frees its workspaces for the
		// next pass rather than leaving them to be abandoned.
		if !released {
			_ = c.writeTransaction(context.WithoutCancel(ctx), "library-refresh release", func(tx *sql.Tx) error {
				_, err := tx.Exec(`DELETE FROM workspace_library_claims WHERE token=?`, token)
				return err
			})
		}
	}()
	// Compute what the claim covers from one snapshot that includes it.
	var claimed string
	var rows, previews [][]any
	completed := func(error) {}
	if err := func() error {
		snapshot, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return err
		}
		defer snapshot.Rollback()
		held, err := queryMapsContext(ctx, snapshot, `SELECT workspace_id FROM workspace_library_claims WHERE token=?`, token)
		if err != nil || len(held) == 0 {
			return err
		}
		completed = maintenanceBatch(ctx, snapshot, "Indexing the Search view", "workspace", len(held),
			"SELECT count(*) FROM workspace_library_dirty")
		chosen := make([]string, 0, len(held))
		for _, row := range held {
			chosen = append(chosen, firstString(row["workspace_id"]))
		}
		claimed = jsonText(chosen)
		// Deleted workspaces produce no row; their stored rows cascaded away.
		if rows, err = queryValues(ctx, snapshot, libraryDerivedSelect("w.id IN (SELECT value FROM json_each(?))"), claimed); err != nil {
			return err
		}
		previews, err = queryValues(ctx, snapshot, libraryPreviewSelect("w.id IN (SELECT value FROM json_each(?))"), claimed)
		return err
	}(); err != nil {
		return 0, 0, err
	}
	if libraryComputedHook != nil {
		libraryComputedHook()
	}
	if claimed == "" {
		// Every claim was released, or taken, before the snapshot.
		return len(ids), 0, nil
	}
	tx, finish, err := c.beginTrackedWrite(ctx, "library-refresh")
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback(); finish() }()
	// Released since the claim: something the workspace's row reads was
	// written, so what was computed may be obsolete. It is still marked.
	held, err := queryMapsContext(ctx, tx, `SELECT workspace_id FROM workspace_library_claims WHERE token=?`, token)
	if err != nil {
		return 0, 0, err
	}
	still := make(map[string]bool, len(held))
	for _, row := range held {
		still[firstString(row["workspace_id"])] = true
	}
	names := []string{"workspace_id"}
	for _, column := range libraryColumns {
		names = append(names, column.name)
	}
	store := "INSERT OR REPLACE INTO workspace_library(" + strings.Join(names, ",") + ") VALUES(" + placeholders(len(names)) + ")"
	for _, row := range rows {
		if id, _ := row[0].(string); still[id] {
			if _, err := tx.ExecContext(ctx, store, row...); err != nil {
				return 0, 0, err
			}
		}
	}
	// Previews go in before the mark is cleared: clearing the mark of a
	// workspace without them queues it for refreshPreviews.
	for _, row := range previews {
		if id, _ := row[0].(string); still[id] {
			if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO workspace_previews(workspace_id,first_input_id,last_response_id) VALUES(?,?,?)", row...); err != nil {
				return 0, 0, err
			}
		}
	}
	// Every column was just computed as now defined, so none is still owed.
	for _, statement := range []string{`DELETE FROM workspace_previews_pending WHERE workspace_id IN (SELECT workspace_id FROM workspace_library_claims WHERE token=?)`,
		`DELETE FROM workspace_library_column_pending WHERE workspace_id IN (SELECT workspace_id FROM workspace_library_claims WHERE token=?)`,
		`DELETE FROM workspace_library_dirty WHERE workspace_id IN (SELECT workspace_id FROM workspace_library_claims WHERE token=?)`} {
		if _, err := tx.ExecContext(ctx, statement, token); err != nil {
			return 0, 0, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspace_library_claims WHERE token=?`, token); err != nil {
		return 0, 0, err
	}
	err = tx.Commit()
	completed(err)
	if err != nil {
		return 0, 0, err
	}
	released = true
	// Workspaces written to meanwhile stay marked and count as attempted, so
	// the caller comes back for them.
	return len(ids), len(still), nil
}

// libraryColumnBatch is how many workspaces one pass computes a column for.
// One column of a workspace is far cheaper than its row, and a pass that is
// stopped loses only its batch.
var libraryColumnBatch = 50

const (
	// libraryColumnsOwedSQL selects the column values a pass can compute
	// now. A marked workspace waits for its refresh, which computes them all.
	libraryColumnsOwedSQL = `FROM workspace_library_column_pending p WHERE p.workspace_id NOT IN (SELECT workspace_id FROM workspace_library_dirty)`
	// libraryColumnWorkspacesSQL counts, up to a limit, the stored workspaces
	// with a column still to compute.
	libraryColumnWorkspacesSQL = `SELECT COUNT(*) FROM (SELECT DISTINCT p.workspace_id ` + libraryColumnsOwedSQL + ` LIMIT ?)`
)

// libraryColumnPass is what one refreshLibraryColumns pass did.
type libraryColumnPass struct {
	column string
	// workspaces had the column computed, changed of them stored a
	// different value, and held is how long the catalog writer was held.
	workspaces, changed int
	held                time.Duration
}

// refreshLibraryColumns computes one changed or new column for up to batch
// stored workspaces queued for it (see ensureLibrary), and no other column.
// The values are computed on a read snapshot and the writer is held only to
// store them. The queue is the progress: a pass that is stopped, or a service
// that restarts, carries on from what is left.
//
// A workspace written to meanwhile is marked, and one refreshed meanwhile
// has left the queue; neither is stored here, since the refresh computes
// every column from what is there now.
func (c *Catalog) refreshLibraryColumns(ctx context.Context, batch int) (libraryColumnPass, error) {
	var pass libraryColumnPass
	// Idle: the queue is empty.
	owed, err := queryMapsContext(ctx, c.reader(), `SELECT p.column_name,p.workspace_id `+libraryColumnsOwedSQL+` ORDER BY p.column_name,p.workspace_id LIMIT ?`, batch)
	if err != nil || len(owed) == 0 {
		return pass, err
	}
	pass.column = firstString(owed[0]["column_name"])
	ids := []string{}
	for _, row := range owed {
		if firstString(row["column_name"]) == pass.column {
			ids = append(ids, firstString(row["workspace_id"]))
		}
	}
	index := slices.IndexFunc(libraryColumns, func(column libraryColumn) bool { return column.name == pass.column })
	var values [][]any
	completed := func(error) {}
	if index >= 0 {
		completed = maintenanceBatch(ctx, c.reader(), "Updating Search view columns", "column value", len(ids), "SELECT count(*) FROM workspace_library_column_pending")
		// Deleted workspaces produce no row; their stored rows cascaded away.
		if values, err = queryValues(ctx, c.reader(), "SELECT w.id,"+libraryColumns[index].expr+" FROM workspaces w WHERE w.id IN (SELECT value FROM json_each(?))", jsonText(ids)); err != nil {
			return pass, err
		}
	}
	tx, finish, err := c.beginTrackedWrite(ctx, "library-columns")
	if err != nil {
		return pass, err
	}
	started := time.Now()
	defer func() { _ = tx.Rollback(); finish() }()
	const unmarked = ` AND workspace_id NOT IN (SELECT workspace_id FROM workspace_library_dirty)`
	store := `UPDATE workspace_library SET "` + pass.column + `"=?2 WHERE workspace_id=?1 AND "` + pass.column + `" IS NOT ?2
		AND EXISTS(SELECT 1 FROM workspace_library_column_pending WHERE column_name=?3 AND workspace_id=?1)` + unmarked
	for _, row := range values {
		result, err := tx.ExecContext(ctx, store, row[0], row[1], pass.column)
		if err != nil {
			return pass, err
		}
		changed, _ := result.RowsAffected()
		pass.changed += int(changed)
	}
	// A column this build does not define (another build queued it) is not
	// one to compute: its entries only leave.
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspace_library_column_pending WHERE column_name=? AND workspace_id IN (SELECT value FROM json_each(?))`+unmarked,
		pass.column, jsonText(ids)); err != nil {
		return pass, err
	}
	// Requests cache what they read of the view, and a stored value that
	// changes is not a write any cache trigger sees.
	if pass.changed > 0 {
		if _, err := tx.ExecContext(ctx, "UPDATE query_cache_generation SET value=value+1 WHERE id=1"); err != nil {
			return pass, err
		}
	}
	err = tx.Commit()
	pass.held = time.Since(started)
	completed(err)
	if err != nil {
		return pass, err
	}
	pass.workspaces = len(ids)
	return pass, nil
}

// queryValues reads a statement's rows as the driver returns them, in column
// order, so that storing them again keeps each value's type.
func queryValues(ctx context.Context, q queryer, statement string, args ...any) ([][]any, error) {
	rows, err := q.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := [][]any{}
	for rows.Next() {
		values, pointers := make([]any, len(columns)), make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		out = append(out, values)
	}
	return out, rows.Err()
}

// A refresh stores a workspace's previews with its row, so one is without
// them only when something else cleared its mark: a build that predates
// previews, or one that does not store them. workspace_previews_pending is
// the queue of those, kept by a trigger, so that finding none costs one
// probe of an empty table rather than a scan of every stored row.
const (
	// previewsQueuedSQL selects queued workspaces a pass can settle now. A
	// marked one waits for its refresh, which stores its previews.
	previewsQueuedSQL = `FROM workspace_previews_pending p WHERE p.workspace_id NOT IN (SELECT workspace_id FROM workspace_library_dirty)`
	// previewsLargeSQL narrows them to those worth storing previews for.
	previewsLargeSQL = previewsQueuedSQL + ` AND p.workspace_id NOT IN (SELECT workspace_id FROM workspace_previews)
		AND p.workspace_id IN (SELECT l.workspace_id FROM workspace_library l WHERE l.turn_count+l.tool_use_count>200)`
)

// ensurePreviewQueue creates the queue and, the first time, fills it with
// the large workspaces already without previews (one scan of the stored rows).
func (c *Catalog) ensurePreviewQueue() error {
	var existed, stored int
	if err := c.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='workspace_previews_pending'),
		(SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='workspace_library')`).Scan(&existed, &stored); err != nil {
		return err
	}
	if _, err := c.DB.Exec(`CREATE TABLE IF NOT EXISTS workspace_previews_pending (workspace_id TEXT PRIMARY KEY) WITHOUT ROWID;
		CREATE TRIGGER IF NOT EXISTS workspace_previews_unstored AFTER DELETE ON workspace_library_dirty
		WHEN NOT EXISTS(SELECT 1 FROM workspace_previews WHERE workspace_id=OLD.workspace_id)
			AND EXISTS(SELECT 1 FROM workspaces WHERE id=OLD.workspace_id)
		BEGIN INSERT INTO workspace_previews_pending(workspace_id) SELECT OLD.workspace_id
			WHERE OLD.workspace_id NOT IN (SELECT workspace_id FROM workspace_previews_pending); END`); err != nil {
		return err
	}
	if existed == 0 && stored == 1 {
		_, err := c.DB.Exec(`INSERT OR IGNORE INTO workspace_previews_pending(workspace_id) SELECT l.workspace_id FROM workspace_library l
			WHERE l.turn_count+l.tool_use_count>200 AND l.workspace_id NOT IN (SELECT workspace_id FROM workspace_previews)`)
		return err
	}
	return nil
}

// refreshPreviews stores the preview IDs of up to batch queued large
// workspaces, largest first, and reports how many it stored. A small one is
// quick to preview without, so it only leaves the queue. The IDs are found
// before the write lock is taken, since a large workspace takes seconds to
// read, and stored only for workspaces nothing has changed since: a change
// marks the workspace dirty, and RefreshLibrary stores its previews.
func (c *Catalog) refreshPreviews(ctx context.Context, batch int) (int, error) {
	// Idle: the queue is empty.
	queued, err := queryMapsContext(ctx, c.reader(), "SELECT 1 "+previewsQueuedSQL+" LIMIT 1")
	if err != nil || len(queued) == 0 {
		return 0, err
	}
	selected, err := queryMapsContext(ctx, c.reader(), `SELECT p.workspace_id `+previewsLargeSQL+`
		ORDER BY (SELECT l.turn_count+l.tool_use_count FROM workspace_library l WHERE l.workspace_id=p.workspace_id) DESC LIMIT ?`, batch)
	if err != nil {
		return 0, err
	}
	completed := func(error) {}
	ids := make([]any, len(selected))
	rows := []map[string]any{}
	if len(selected) > 0 {
		completed = maintenanceBatch(ctx, c.reader(), "Filling in previews", "workspace", len(selected), "SELECT count(*) "+previewsLargeSQL)
		for i, row := range selected {
			ids[i] = row["workspace_id"]
		}
		if rows, err = queryMapsContext(ctx, c.reader(), libraryPreviewSelect("w.id IN ("+placeholders(len(ids))+")"), ids...); err != nil {
			return 0, err
		}
	}
	tx, finish, err := c.beginTrackedWrite(ctx, "library-previews")
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(); finish() }()
	for _, row := range rows {
		id := row["workspace_id"]
		if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_previews(workspace_id,first_input_id,last_response_id) SELECT ?,?,?
			WHERE ? IN (SELECT id FROM workspaces) AND ? NOT IN (SELECT workspace_id FROM workspace_library_dirty)
			ON CONFLICT(workspace_id) DO NOTHING`, id, row["first_input_id"], row["last_response_id"], id, id); err != nil {
			return 0, err
		}
	}
	// What is settled leaves the queue: workspaces with previews (those
	// just stored among them), too small to want them, or gone. One marked
	// meanwhile stays for its refresh.
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspace_previews_pending WHERE workspace_id NOT IN (SELECT workspace_id FROM workspace_library_dirty)
		AND (workspace_id IN (SELECT workspace_id FROM workspace_previews)
			OR workspace_id NOT IN (SELECT l.workspace_id FROM workspace_library l WHERE l.turn_count+l.tool_use_count>200))`); err != nil {
		return 0, err
	}
	err = tx.Commit()
	completed(err)
	return len(rows), err
}

// refreshAllLibrary clears every dirty workspace, for commands that ingest
// without a running service to maintain the projection.
func (c *Catalog) refreshAllLibrary(ctx context.Context) error {
	for {
		count, err := c.RefreshLibrary(ctx, 10)
		if err != nil || count == 0 {
			return err
		}
	}
}

// libraryCatchUpShown is how long maintainLibrary must have been refreshing
// without catching up before the drive panel lists it. Each workspace an
// index writes is dirty only until the next pass, which clears it within about
// two seconds, and listing that would flash on and off throughout the index.
const libraryCatchUpShown = 15 * time.Second

// libraryCatchUp is when maintainLibrary last started refreshing after it
// found nothing dirty; zero while it is caught up or stopped.
type libraryCatchUp struct {
	mu    sync.Mutex
	since time.Time
	// stopped is set while the user has stopped the refresh: maintainLibrary
	// leaves dirty workspaces dirty (reads compute them live) until it is
	// resumed or the service restarts. Every active pass is cancellable,
	// including overlapping continuous and explicitly requested catch-up.
	stopped  bool
	nextPass uint64
	passes   map[uint64]context.CancelFunc
}

func (c *Catalog) noteLibraryRefresh(count int, err error) {
	c.catchUp.mu.Lock()
	defer c.catchUp.mu.Unlock()
	switch {
	case c.catchUp.stopped:
	case err == nil && count == 0:
		c.catchUp.since = time.Time{}
	case count > 0 && c.catchUp.since.IsZero():
		c.catchUp.since = c.clock()
	}
}

// libraryCatchingUp reports whether maintainLibrary has been refreshing for
// long enough to list.
func (c *Catalog) libraryCatchingUp() bool {
	c.catchUp.mu.Lock()
	defer c.catchUp.mu.Unlock()
	return !c.catchUp.stopped && !c.catchUp.since.IsZero() && c.clock().Sub(c.catchUp.since) >= libraryCatchUpShown
}

// stopLibraryRefresh stops maintainLibrary, abandoning the batch in progress
// (its workspaces stay dirty), and reports whether it was running.
func (c *Catalog) stopLibraryRefresh() bool {
	c.catchUp.mu.Lock()
	defer c.catchUp.mu.Unlock()
	if c.catchUp.stopped {
		return false
	}
	c.catchUp.stopped, c.catchUp.since = true, time.Time{}
	for _, cancel := range c.catchUp.passes {
		cancel()
	}
	return true
}

// resumeLibraryRefresh undoes stopLibraryRefresh and reports whether it was
// stopped.
func (c *Catalog) resumeLibraryRefresh() bool {
	c.catchUp.mu.Lock()
	defer c.catchUp.mu.Unlock()
	stopped := c.catchUp.stopped
	c.catchUp.stopped = false
	if c.work != nil {
		c.work.runner.Wake()
	}
	return stopped
}

func (c *Catalog) libraryRefreshStopped() bool {
	c.catchUp.mu.Lock()
	defer c.catchUp.mu.Unlock()
	return c.catchUp.stopped
}

// libraryPass starts one maintainLibrary pass that stopLibraryRefresh can
// cancel, or reports false while the refresh is stopped.
func (c *Catalog) libraryPass(ctx context.Context) (context.Context, func(), bool) {
	c.catchUp.mu.Lock()
	defer c.catchUp.mu.Unlock()
	if c.catchUp.stopped {
		return nil, nil, false
	}
	pass, cancel := context.WithCancel(ctx)
	if c.catchUp.passes == nil {
		c.catchUp.passes = map[uint64]context.CancelFunc{}
	}
	c.catchUp.nextPass++
	id := c.catchUp.nextPass
	c.catchUp.passes[id] = cancel
	return pass, func() {
		c.catchUp.mu.Lock()
		delete(c.catchUp.passes, id)
		c.catchUp.mu.Unlock()
		cancel()
	}, true
}

// originsRunInterval spaces origin publications while this process ingests:
// each one reads every conversation, and the next commit leaves the projection
// dirty again. Pages then show attribution up to this far behind, like their
// cached values. Tests shorten it.
var originsRunInterval = time.Minute

// originsPass publishes conversation origins as a maintenance.origins pass,
// an operation only when the projection was out of date (§3.4).
func (c *Catalog) originsPass(ctx context.Context) {
	origins := c.beginPass(ctx, "maintenance.origins", triggerChained)
	var published bool
	var err error
	origins.runIO(ctx, func(ctx context.Context) { published, err = c.publishOrigins(ctx) })
	if published {
		origins.worked(1)
	}
	if ctx.Err() != nil {
		err = nil
	}
	origins.end(err)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Conversation origins: %v\n", err)
	}
}

// maintainLibrary keeps the stored projection current, including after
// ingests run by other processes against the same catalog. It also publishes
// conversation origins, which requests only read, including while an ingest
// pauses the rest of the loop: a long ingest otherwise leaves the conversations
// it adds without channel or machine until it ends.
//
// Each stretch of real work is an operation (§3.4): the Search view's catch-up
// lasts from the first pass that refreshes a workspace to the first that finds
// none; filling previews and measuring parallelism likewise. A publication
// that stores origins is one too. A pass that does nothing is forgotten.
func (c *Catalog) maintainLibrary(ctx context.Context) {
	schedule := searchScheduleGate{}
	var view, previews, parallelism, shape, names *daemonPass
	settle := func(pass **daemonPass, more bool, err error) {
		if *pass != nil && (!more || err != nil) {
			(*pass).end(err)
			*pass = nil
		}
	}
	begin := func(pass **daemonPass, kind string) {
		if *pass == nil {
			*pass = c.beginPass(ctx, kind, triggerChained)
		}
	}
	defer func() {
		settle(&view, false, nil)
		settle(&previews, false, nil)
		settle(&parallelism, false, nil)
		settle(&shape, false, nil)
		settle(&names, false, nil)
	}()
	var originsAt time.Time
	var resumeProjections func()
	var projectionWaitReason string
	defer func() {
		if resumeProjections != nil {
			resumeProjections()
		}
	}()
	for {
		workCtx := ctx
		for _, active := range []*daemonPass{view, previews, parallelism, shape, names} {
			if active.active() {
				workCtx = active.context(ctx)
				break
			}
		}
		if schedule.wait(ctx, c, &view, &previews, &parallelism, &shape, &names) != nil || c.queries.wait(workCtx) != nil {
			return
		}
		if !c.libraryRefreshStopped() && (c.ingestRuns.Load() == 0 || time.Since(originsAt) >= originsRunInterval) {
			c.originsPass(ctx)
			originsAt = time.Now()
		}
		if c.projectionBatches.Load() > 0 || c.libraryRefreshStopped() {
			// These passes can have committed a batch before the index took
			// over. Keep that progress, but do not present the time spent
			// waiting through its analysis chain as active measurement.
			reason, detail := "ingest-busy", "source indexing and its follow-up analysis to finish"
			if c.libraryRefreshStopped() {
				reason, detail = "search-stopped", "the Search view refresh to resume"
			}
			if resumeProjections == nil || projectionWaitReason != reason {
				if resumeProjections != nil {
					resumeProjections()
				}
				resumeProjections = waitForDaemonPasses(reason, detail, view, previews, parallelism, shape, names)
				projectionWaitReason = reason
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
				continue
			}
		}
		if resumeProjections != nil {
			resumeProjections()
			resumeProjections = nil
		}
		pass, done, running := c.libraryPass(ctx)
		if !running {
			continue
		}
		// Names advance even while a large Search projection is catching up.
		begin(&names, kindNames)
		var named int
		var nameErr error
		names.runIO(pass, func(ctx context.Context) { named, nameErr = c.refreshConversationNames(ctx, 25) })
		names.worked(named)
		settle(&names, named > 0, nameErr)
		if nameErr != nil && pass.Err() == nil {
			fmt.Fprintf(os.Stderr, "Conversation names: %v\n", nameErr)
		}
		begin(&view, "maintenance.library-view")
		var count, stored int
		var err error
		view.runIO(pass, func(ctx context.Context) { count, stored, err = c.refreshLibraryBatch(ctx, 10, nil) })
		c.noteLibraryRefresh(count, err)
		view.worked(stored)
		settle(&view, count > 0, err)
		// Session facts must also advance during a large Search refresh. Doing
		// one conversation between view batches avoids waiting for every dirty
		// workspace and preview before an upgrade's backfill can even start.
		if count > 0 && err == nil {
			begin(&shape, "maintenance.session-shape")
			var shaped int
			var shapeErr error
			shape.runIO(pass, func(ctx context.Context) { shaped, shapeErr = c.refreshSessionShape(ctx, 1, false) })
			shape.worked(shaped)
			settle(&shape, shaped > 0, shapeErr)
			if shapeErr != nil && pass.Err() == nil {
				fmt.Fprintf(os.Stderr, "Session shape: %v\n", shapeErr)
			}
		}
		wait := time.Duration(0)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if pass.Err() == nil {
				fmt.Fprintf(os.Stderr, "Library refresh: %v\n", err)
				wait = 5 * time.Second
			}
		} else if count > 0 && stored == 0 {
			// Everything it computed was written meanwhile: let that writer
			// finish rather than recompute as fast as it writes.
			wait = time.Second
		} else if count == 0 {
			// Idle: fill in previews, then keep the page caches warm.
			begin(&previews, "maintenance.previews")
			var filled int
			previews.runIO(pass, func(ctx context.Context) { filled, err = c.refreshPreviews(ctx, 25) })
			previews.worked(filled)
			settle(&previews, filled > 0, err)
			if err != nil || filled == 0 {
				if err != nil && pass.Err() == nil {
					fmt.Fprintf(os.Stderr, "Library previews: %v\n", err)
				}
				// Then store changed conversations' parallelism timing, so a
				// page computes only what changed since (all, after an upgrade).
				begin(&parallelism, "maintenance.parallelism")
				var derived int
				parallelism.runIO(pass, func(ctx context.Context) { derived, err = c.refreshParallelism(ctx, parallelismBatch, false) })
				parallelism.worked(derived)
				settle(&parallelism, derived > 0, err)
				if err != nil || derived == 0 {
					if err != nil && pass.Err() == nil {
						fmt.Fprintf(os.Stderr, "Parallelism activity: %v\n", err)
					}
					// Then changed conversations' session shape, likewise.
					begin(&shape, "maintenance.session-shape")
					var shaped int
					shape.runIO(pass, func(ctx context.Context) { shaped, err = c.refreshSessionShape(ctx, sessionShapeBatch, false) })
					shape.worked(shaped)
					settle(&shape, shaped > 0, err)
					if err != nil || shaped == 0 {
						if err != nil && pass.Err() == nil {
							fmt.Fprintf(os.Stderr, "Session shape: %v\n", err)
						}
						if named > 0 && nameErr == nil && err == nil {
							done()
							continue
						}
						wait = 2 * time.Second
						// Warming only reads; its operation is never published.
						warm := c.beginPass(ctx, "maintenance.cache-warm", triggerChained)
						warm.runIO(ctx, func(ctx context.Context) { c.warmCaches(ctx) })
						warm.end(nil)
					}
				}
			}
		}
		done()
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// A Library request reads only the fields it filters, sorts, groups, or
// aggregates on: a full row carries tens of megabytes of purpose and outcome
// text across the catalog. libraryPage then fills in whole rows for the page
// the UI shows, which renders fields it never selected (pr_details,
// canonical_remote, main_merge_*, and purpose/outcome as preview fallbacks).

// libraryFields is a set of Library field names; nil means every column.
type libraryFields map[string]bool

var (
	// libraryWorkspaceFields are Library fields read straight from workspaces.
	// workspaces_library_idx holds them all (see Initialize); keep it so.
	libraryWorkspaceFields = []string{"id", "title", "source_kind", "activity_at", "branch", "owner", "flavor",
		"version", "lifecycle", "preservation_completeness", "main_merge_title", "location"}
	// libraryGoFields are added in Go after the read: relevance, mirror
	// suppression, and costs.
	libraryGoFields = []string{"channel", "machine_id", "machine", "machine_kind", "execution_machine_id", "execution_machine", "execution_machine_kind", "channels", "machines", "execution_machines", "score", "match_types", "source_aliases", "mirrored_workspace_ids",
		"cost_usd", "price_status"}
	libraryCostFields = []string{"cost_usd", "price_status"}
	// libraryLargeFields hold long text, so only requests that use them read them.
	libraryLargeFields = []string{"purpose", "outcome", "changed_files", "pr_details", "summary_initiation", "summary_outcome"}
	// libraryCompactFields is every field except long text and costs, which
	// take a ledger scan: enough for any request that doesn't filter, sort, or
	// group on those.
	libraryCompactFields = func() libraryFields {
		fields := libraryFields{}
		for name := range libraryKnownFields() {
			fields[name] = true
		}
		for _, name := range append(append([]string{}, libraryLargeFields...), libraryCostFields...) {
			delete(fields, name)
		}
		return fields
	}()
)

// libraryExtraColumns are row columns computed from derived and joined values.
func libraryExtraColumns(derived map[string]string) []libraryColumn {
	return []libraryColumn{
		{"purpose", "COALESCE(w.purpose," + derived["summary_initiation"] + ")"},
		{"outcome", "COALESCE(w.outcome," + derived["summary_outcome"] + ")"},
		{"repository_name", "r.display_name"},
		{"project_id", projectIDSQL("w")},
		{"project_name", projectNameSQL("w")},
		{"project_kind", projectKindSQL("w")},
		{"canonical_remote", "r.canonical_remote"},
		{"file_edit_count", derived["changed_file_count"]},
		// Branches and pull requests are Git facts: without any, a folder or
		// no project has none to count, which is not applicable rather than
		// zero. Git data a session did record always shows.
		{"branch_count", "CASE WHEN TRIM(COALESCE(w.branch,''))<>'' THEN 1 WHEN pj.id IS NULL THEN NULL ELSE 0 END"},
		{"pr_count", "CASE WHEN " + derived["pr_count"] + ">0 THEN " + derived["pr_count"] + " WHEN pj.id IS NULL THEN NULL ELSE 0 END"},
		// The session's outcome (outcomes.go): its label, what decided it,
		// and its Tier 1 facts.
		{"outcome_label", outcomeLabelSQL("w")},
		{"outcome_why", outcomeWhySQL("w")},
		{"files_changed", outcomeFactSQL("w", "so.files_changed")},
		{"lines_added", outcomeFactSQL("w", "so.lines_added")},
		{"lines_removed", outcomeFactSQL("w", "so.lines_removed")},
		{"verification", outcomeFactSQL("w", "so.verification")},
		{"recorded_commits", outcomeFactSQL("w", "json_array_length(so.commits_json)")},
		{"reworked_files", outcomeFactSQL("w", "so.reworked_files")},
		// Local Git (outcome_git.go): whether it was asked, and the commits
		// linked to the session and on mainline, empty unless it was.
		{"git_check", gitCheckSQL("w")},
		{"git_check_reason", gitCheckReasonSQL("w")},
		{"linked_commits", gitFactSQL("w", "sg.linked_commits")},
		{"landed_commits", gitFactSQL("w", "sg.landed_commits")},
	}
}

func libraryKnownFields() libraryFields {
	known := libraryFields{}
	for _, name := range append(append([]string{}, libraryWorkspaceFields...), libraryGoFields...) {
		known[name] = true
	}
	for _, column := range append(append([]libraryColumn{}, libraryColumns...), libraryExtraColumns(nil)...) {
		known[column.name] = true
	}
	return known
}

// libraryFieldsFor returns the fields a request needs, plus those the search
// pipeline itself reads. An unknown name selects every column, so a field
// missing from these lists costs speed, never correctness.
func libraryFieldsFor(names ...string) libraryFields {
	known := libraryKnownFields()
	fields := libraryFields{"id": true, "source_kind": true, "activity_at": true, "score": true}
	for _, name := range names {
		if !known[name] {
			return nil
		}
		fields[name] = true
	}
	return fields
}

func (f libraryFields) has(name string) bool { return f == nil || f[name] }

func (f libraryFields) hasAny(names []string) bool {
	return slices.ContainsFunc(names, f.has)
}

// covers reports whether rows read with f carry every field in other.
func (f libraryFields) covers(other libraryFields) bool {
	if f == nil {
		return true
	}
	if other == nil {
		return false
	}
	for name := range other {
		if !f[name] {
			return false
		}
	}
	return true
}

func (f libraryFields) union(other libraryFields) libraryFields {
	if f == nil || other == nil {
		return nil
	}
	fields := maps.Clone(f)
	maps.Copy(fields, other)
	return fields
}

// libraryRowSelect lists a Library row's columns in fields (nil: all),
// reading each derived column through value (a stored column or its live
// expression). w.id is always read.
func libraryRowSelect(value func(libraryColumn) string, fields libraryFields) string {
	columns := []string{"w.*"}
	if fields != nil {
		columns = []string{"w.id"}
		for _, name := range libraryWorkspaceFields {
			if name != "id" && fields[name] {
				columns = append(columns, "w."+name)
			}
		}
	}
	derived := map[string]string{}
	for _, column := range libraryColumns {
		derived[column.name] = value(column)
	}
	extras := libraryExtraColumns(derived)
	for _, column := range libraryColumns {
		// An extra column of the same name reads the stored one itself.
		redefined := slices.ContainsFunc(extras, func(extra libraryColumn) bool { return extra.name == column.name })
		if fields.has(column.name) && !redefined {
			columns = append(columns, derived[column.name]+" "+column.name)
		}
	}
	for _, column := range extras {
		if fields.has(column.name) {
			columns = append(columns, column.expr+" "+column.name)
		}
	}
	return "SELECT " + strings.Join(columns, ",\n\t") + "\n\tFROM workspaces w LEFT JOIN repositories r ON r.id=w.repository_id" + projectJoin("w")
}

// libraryRows reads Library rows with fields (nil: every column) for
// workspaces matching the optional predicate (over w and r). Stored values
// serve clean workspaces; the same expressions are evaluated live for dirty or
// missing ones. Both reads share one transaction, so a concurrent refresh
// cannot drop or duplicate a row.
func (c *Catalog) libraryRows(ctx context.Context, predicate string, args []any, fields libraryFields) ([]map[string]any, error) {
	if predicate != "" {
		predicate = " AND (" + predicate + ")"
	}
	stored := libraryRowSelect(func(column libraryColumn) string { return "l." + column.name }, fields) +
		" JOIN workspace_library l ON l.workspace_id=w.id" +
		" WHERE w.id NOT IN (SELECT workspace_id FROM workspace_library_dirty)" + predicate
	live := libraryRowSelect(func(column libraryColumn) string { return "(" + column.expr + ")" }, fields) +
		" WHERE (w.id IN (SELECT workspace_id FROM workspace_library_dirty)" +
		" OR w.id NOT IN (SELECT workspace_id FROM workspace_library))" + predicate
	tx, err := c.reader().BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := queryMapsContext(ctx, tx, stored, args...)
	if err != nil {
		return nil, err
	}
	dirty, err := queryMapsContext(ctx, tx, live, args...)
	if err != nil {
		return nil, err
	}
	return append(rows, dirty...), nil
}

// libraryPage returns copies of a page of Library rows completed with every
// column, costs, and the first request and latest response text. The input
// rows may be cached, so they are left unchanged.
func (c *Catalog) libraryPage(ctx context.Context, rows []map[string]any) ([]map[string]any, error) {
	if len(rows) == 0 {
		return rows, nil
	}
	args := make([]any, len(rows))
	for index, row := range rows {
		args[index] = firstString(row["id"])
	}
	full, err := c.libraryRows(ctx, "w.id IN ("+placeholders(len(args))+")", args, nil)
	if err != nil {
		return nil, err
	}
	if err := c.attachWorkspaceCosts(ctx, full); err != nil {
		return nil, err
	}
	byID := make(map[string]map[string]any, len(full))
	for _, row := range full {
		byID[firstString(row["id"])] = row
	}
	page := make([]map[string]any, len(rows))
	for index, row := range rows {
		page[index] = maps.Clone(row)
		// Relevance and mirror fields come from the search, not the read.
		maps.Copy(page[index], byID[firstString(row["id"])])
	}
	if err := c.attachWorkspaceOrigins(ctx, page, nil); err != nil {
		return nil, err
	}
	return page, c.attachLibraryPreviews(ctx, page)
}

// Library pages preview each workspace's first request and latest
// response. Finding them reads every message of the workspace, seconds for a
// page of large ones, so workspace_previews keeps their message IDs:
// RefreshLibrary stores them with the projection and refreshPreviews fills in
// large workspaces. Marking a workspace dirty drops its row (see schema.sql),
// so a page reads the IDs of a workspace without one from its messages.

// libraryPreviewSelect finds the preview message IDs of each workspace filter
// selects. The unary + on role keeps SQLite from walking messages_prose_idx,
// which orders every prose message in the catalog by time, until it happens
// on one of this workspace's.
func libraryPreviewSelect(filter string) string {
	return `SELECT w.id workspace_id,
		(SELECT fm.id FROM conversations fc JOIN messages fm INDEXED BY messages_conversation_idx ON fm.conversation_id=fc.id WHERE fc.workspace_id=w.id AND +fm.role='user' AND fm.kind='message' ORDER BY fm.created_at,fm.id LIMIT 1) first_input_id,
		(SELECT lm.id FROM conversations lc JOIN messages lm INDEXED BY messages_conversation_idx ON lm.conversation_id=lc.id WHERE lc.workspace_id=w.id AND +lm.role='assistant' AND lm.kind='message' ORDER BY lm.created_at DESC,lm.id DESC LIMIT 1) last_response_id
		FROM workspaces w WHERE ` + filter
}

// attachLibraryPreviews adds the first request and latest response, with
// their message IDs, to page rows. The text is large, so only displayed rows carry it.
func (c *Catalog) attachLibraryPreviews(ctx context.Context, page []map[string]any) error {
	if len(page) == 0 {
		return nil
	}
	ids := make([]any, len(page))
	for index, row := range page {
		ids[index] = firstString(row["id"])
	}
	listed := placeholders(len(ids))
	previews, err := queryMapsContext(ctx, c.reader(), `SELECT workspace_id,first_input_id,last_response_id FROM workspace_previews
		WHERE workspace_id IN (`+listed+`) UNION ALL `+libraryPreviewSelect(`w.id IN (`+listed+`)
		AND w.id NOT IN (SELECT workspace_id FROM workspace_previews)`), append(ids, ids...)...)
	if err != nil {
		return err
	}
	messageIDs, byWorkspace := []any{}, map[string]map[string]any{}
	for _, preview := range previews {
		byWorkspace[firstString(preview["workspace_id"])] = preview
		for _, field := range []string{"first_input_id", "last_response_id"} {
			if id := firstString(preview[field]); id != "" {
				messageIDs = append(messageIDs, id)
			}
		}
	}
	texts := map[string]any{}
	timestamps := map[string]time.Time{}
	if len(messageIDs) > 0 {
		messages, err := queryMapsContext(ctx, c.reader(), "SELECT id,text,created_at FROM messages WHERE id IN ("+placeholders(len(messageIDs))+")", messageIDs...)
		if err != nil {
			return err
		}
		for _, message := range messages {
			texts[firstString(message["id"])] = message["text"]
			if stamp, ok := parseTime(firstString(message["created_at"])); ok {
				timestamps[firstString(message["id"])] = stamp
			}
		}
	}
	for _, row := range page {
		preview := byWorkspace[firstString(row["id"])]
		firstID, firstText, firstAt, err := c.firstHumanPreview(ctx, firstString(row["id"]), "")
		if err != nil {
			return err
		}
		preview["first_input_id"] = firstID
		if firstID != "" {
			texts[firstID] = firstText
			if stamp, ok := parseTime(firstAt); ok {
				timestamps[firstID] = stamp
			}
		}
		row["first_input"], row["last_response"] = nilIfEmpty(firstText), texts[firstString(preview["last_response_id"])]
		// The card view opens its conversation browser at these messages.
		for _, field := range []string{"first_input_id", "last_response_id"} {
			row[field] = nil
			if id := firstString(preview[field]); texts[id] != nil {
				row[field] = id
			}
		}
		row["preview_elapsed_ms"] = nil
		first, firstOK := timestamps[firstString(preview["first_input_id"])]
		last, lastOK := timestamps[firstString(preview["last_response_id"])]
		if firstOK && lastOK && !last.Before(first) {
			row["preview_elapsed_ms"] = last.Sub(first).Milliseconds()
		}
	}
	return nil
}
