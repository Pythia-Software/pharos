package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// A small stable core goes through the ordinary writer. Supplemental source
// observations use typed SQLite values (including exact integers, NULL, blobs)
// and an explicit vocabulary. Unknown columns remain opaque evidence; captured
// SQL, triggers, FTS and application credentials are never recovery inputs.
type historyObservation struct {
	Kind    string          `json:"kind"`
	Columns []string        `json:"columns"`
	Rows    [][]sqliteValue `json:"rows"`
}
type historyRecovery struct {
	ParserVersions []map[string]string  `json:"original_parser_versions,omitempty"`
	Version        int                  `json:"version"`
	Tier           string               `json:"provenance_tier"`
	Schema         string               `json:"pharos_schema"`
	Parser         string               `json:"exporter"`
	OracleHash     string               `json:"oracle_sha256"`
	OriginalID     string               `json:"original_id"`
	Kind           string               `json:"kind"`
	Record         WorkspaceRecord      `json:"record"`
	Observations   []historyObservation `json:"observations"`
	Missing        []string             `json:"missing_fields"`
}

// Order is dependency order for restoration. These are source observations,
// not arbitrary tables from the exporting schema. Values that can only be
// derived today are retained as observed values, never certified as raw input.
var historyWorkspaceTables = []string{
	"workspaces", "repositories", "hosts", "conversations", "messages", "workspace_sightings", "conversation_sightings", "copy_versions", "copy_evidence", "declared_native_aliases", "conversation_identity_links", "conversation_group_membership",
	"agent_sessions", "agent_session_usage", "agent_session_messages", "model_requests", "tool_calls", "tool_commands", "tool_urls", "skill_usages", "conversation_instructions", "work_items", "task_attempts", "handoffs", "change_sets", "change_files", "pull_requests", "work_pr_links", "metrics", "metric_ledger",
}

func historyKnownTable(name string) bool {
	return name == "ask_native_sessions" || slices.Contains(historyWorkspaceTables, name) || name == "tl1_installations" || slices.Contains(tl1SnapshotTables, name)
}
func historyCatalogBoundary(ctx context.Context, db *sql.DB) ([]map[string]any, error) {
	rows, e := queryMapsContext(ctx, db, "SELECT name,type FROM sqlite_schema WHERE type IN ('table','view') ORDER BY name")
	if e != nil {
		return nil, e
	}
	for _, row := range rows {
		name := firstString(row["name"])
		policy := "outside recovery contract: retained by full catalog backup; not certified by session recovery"
		if historyKnownTable(name) {
			policy = "source observations: typed recovery contract"
		} else if strings.HasPrefix(name, "tl1_") {
			policy = "unresolved unknown TL1 table: full catalog backup retains it"
		}
		row["policy"] = policy
	}
	return rows, nil
}

var errHistoryEntityLimit = errors.New("catalog recovery entity exceeds memory budget; retained in rollback backup, unresolved")

func historyRows(ctx context.Context, tx *sql.Tx, kind, statement string, used *int64, args ...any) (historyObservation, error) {
	result := historyObservation{Kind: kind}
	rows, e := tx.QueryContext(ctx, statement, args...)
	if e != nil {
		return result, e
	}
	defer rows.Close()
	result.Columns, e = rows.Columns()
	if e != nil {
		return result, e
	}
	values := make([]any, len(result.Columns))
	ptrs := make([]any, len(values))
	for i := range values {
		ptrs[i] = &values[i]
	}
	for rows.Next() {
		if e = ctx.Err(); e != nil {
			return result, e
		}
		if e = rows.Scan(ptrs...); e != nil {
			return result, e
		}
		row := make([]sqliteValue, len(values))
		for i, v := range values {
			row[i], e = encodeSQLiteValue(v)
			if e != nil {
				return result, e
			}
			*used += int64(len(row[i].Data)) + 64
			if *used > historyEntityBytes {
				return result, errHistoryEntityLimit
			}
		}
		result.Rows = append(result.Rows, row)
	}
	return result, rows.Err()
}
func (o historyObservation) maps() ([]map[string]any, error) {
	var rows []map[string]any
	for _, row := range o.Rows {
		if len(row) != len(o.Columns) {
			return nil, errors.New("invalid recovery observation row")
		}
		m := map[string]any{}
		for i, v := range row {
			x, e := v.decode()
			if e != nil {
				return nil, e
			}
			m[o.Columns[i]] = x
		}
		rows = append(rows, m)
	}
	return rows, nil
}
func recoveryMaps(e historyRecovery, kind string) []map[string]any {
	for _, o := range e.Observations {
		if o.Kind == kind {
			rows, _ := o.maps()
			return rows
		}
	}
	return nil
}

func exportHistoryRecovery(ctx context.Context, tx *sql.Tx, u historyUnit, oracleHash string) (historyRecovery, error) {
	result := historyRecovery{Version: historyFormat, Tier: "catalog-derived", Parser: "catalog-recovery-v1", OracleHash: oracleHash, OriginalID: u.Original, Kind: u.Kind, Missing: []string{"original source columns, discarded payloads and already lost revisions cannot be reconstructed", "raw association and globally chosen multi-host copy are unknown unless independently verified"}}
	if e := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='schema_version'").Scan(&result.Schema); e != nil {
		return result, e
	}
	var used int64
	add := func(kind, where string, args ...any) error {
		name, e := quoteRecordIdentifier(kind)
		if e != nil {
			return e
		}
		obs, e := historyRows(ctx, tx, kind, "SELECT * FROM "+name+" WHERE "+where, &used, args...)
		if e != nil {
			return e
		}
		result.Observations = append(result.Observations, obs)
		return nil
	}
	if u.Kind == "catalog-exclusions" {
		observation, e := historyRows(ctx, tx, "ask_native_sessions", "SELECT provider,native_id,host_id FROM ask_native_sessions ORDER BY provider,native_id", &used)
		if e != nil {
			return result, e
		}
		result.Observations = append(result.Observations, observation)
		return result, nil
	}
	if u.Kind == "catalog-tl1" {
		for _, table := range append([]string{"tl1_installations"}, tl1SnapshotTables...) {
			if e := add(table, "installation_id=?", u.Original); e != nil {
				return result, e
			}
		}
		return result, nil
	}
	// Explicit dependency selectors also retain orphan rows attached to this
	// workspace. Shared rows outside it are covered by the TL1 installation unit.
	selectors := map[string]string{
		"workspaces": "id=?",
		// Retained PRs can still reference a repository the workspace left.
		// Include that dependency closure before restoring any PR observations.
		"repositories": `id IN (SELECT repository_id FROM workspaces WHERE id=?1
			UNION SELECT p.repository_id FROM pull_requests p JOIN work_pr_links l ON l.pr_id=p.id WHERE l.workspace_id=?1) ORDER BY id`,
		"hosts":         "id IN (SELECT host_id FROM workspace_sightings WHERE workspace_id=?)",
		"conversations": "workspace_id=?", "messages": "conversation_id IN (SELECT id FROM conversations WHERE workspace_id=?) ORDER BY conversation_id,source_order IS NULL,source_order,created_at,id",
		"workspace_sightings": "workspace_id=?", "conversation_sightings": "conversation_id IN (SELECT id FROM conversations WHERE workspace_id=?)",
		"copy_versions": "workspace_id=?", "copy_evidence": "workspace_id=?", "declared_native_aliases": "conversation_id IN (SELECT id FROM conversations WHERE workspace_id=?)",
		"conversation_identity_links": "left_id IN (SELECT id FROM conversations WHERE workspace_id=?)", "conversation_group_membership": "workspace_id=?",
		"agent_sessions": "workspace_id=?", "agent_session_usage": "agent_session_id IN (SELECT id FROM agent_sessions WHERE workspace_id=?)", "agent_session_messages": "agent_session_id IN (SELECT id FROM agent_sessions WHERE workspace_id=?)",
		"model_requests": "conversation_id IN (SELECT id FROM conversations WHERE workspace_id=?)",
		"tool_calls":     "workspace_id=?", "tool_commands": "tool_call_id IN (SELECT id FROM tool_calls WHERE workspace_id=?)", "tool_urls": "tool_call_id IN (SELECT id FROM tool_calls WHERE workspace_id=?)", "skill_usages": "conversation_id IN (SELECT id FROM conversations WHERE workspace_id=?)", "conversation_instructions": "conversation_id IN (SELECT id FROM conversations WHERE workspace_id=?)", "work_items": "workspace_id=?", "task_attempts": "work_item_id IN (SELECT id FROM work_items WHERE workspace_id=?)",
		"handoffs": "workspace_id=?", "change_sets": "workspace_id=?", "change_files": "change_set_id IN (SELECT id FROM change_sets WHERE workspace_id=?)",
		"pull_requests": "id IN (SELECT pr_id FROM work_pr_links WHERE workspace_id=?)", "work_pr_links": "workspace_id=?", "metrics": "workspace_id=?", "metric_ledger": "workspace_id=?",
	}
	for _, table := range historyWorkspaceTables {
		if e := add(table, selectors[table], u.Original); e != nil {
			return result, e
		}
	}
	// Keep both directions of incident edges. Whichever workspace is restored
	// second can resolve the link, without importing an unselected parent's body
	// or relying on provider/account guesses from a native ID alone.
	parents, err := historyRows(ctx, tx, "conversation_parents", `
		SELECT c.id conversation_id,c.workspace_id,c.parent_id,p.workspace_id parent_workspace_id
		FROM conversations c JOIN conversations p ON p.id=c.parent_id WHERE c.workspace_id=?
		UNION
		SELECT c.id conversation_id,c.workspace_id,c.parent_id,p.workspace_id parent_workspace_id
		FROM conversations p JOIN conversations c ON c.parent_id=p.id WHERE p.workspace_id=?`, &used, u.Original, u.Original)
	if err != nil {
		return result, err
	}
	result.Observations = append(result.Observations, parents)
	versions, err := historyRows(ctx, tx, "parser_versions", `SELECT DISTINCT s.host_id,s.source_name,s.index_version FROM source_states s JOIN workspace_sightings w ON w.host_id=s.host_id AND w.source_name=s.source_name WHERE w.workspace_id=? ORDER BY s.host_id,s.source_name`, &used, u.Original)
	if err != nil {
		return result, err
	}
	versionRows, err := versions.maps()
	if err != nil {
		return result, err
	}
	for _, row := range versionRows {
		result.ParserVersions = append(result.ParserVersions, map[string]string{"host": firstString(row["host_id"]), "source": firstString(row["source_name"]), "index_version": firstString(row["index_version"])})
	}
	if len(result.ParserVersions) == 0 {
		result.Missing = append(result.Missing, "original parser version not recorded")
	}
	workspaces := recoveryMaps(result, "workspaces")
	if len(workspaces) != 1 {
		return result, errors.New("catalog workspace disappeared from fixed snapshot")
	}
	w := workspaces[0]
	record := WorkspaceRecord{SourceKind: firstString(w["source_kind"]), SourceID: firstString(w["source_id"]), Account: firstString(w["source_account"]), Title: firstString(w["title"]), Purpose: firstString(w["purpose"]), Outcome: firstString(w["outcome"]), Location: firstString(w["location"]), ActivityAt: firstString(w["activity_at"]), Metadata: map[string]any{}}
	// Source title is distinct from Pharos's override or derived display name.
	var title string
	if e := tx.QueryRowContext(ctx, "SELECT source_title FROM workspace_names WHERE workspace_id=?", u.Original).Scan(&title); e == nil {
		record.Title = title
	} else if !errors.Is(e, sql.ErrNoRows) {
		return result, e
	}
	for _, key := range []string{"base_ref", "head_ref", "branch", "owner", "flavor", "version", "lifecycle", "terminal", "custody_version", "main_merge_commit", "main_merge_title", "main_merge_url", "main_merge_method"} {
		record.Metadata[key] = w[key]
	}
	for _, repo := range recoveryMaps(result, "repositories") {
		if repo["id"] == w["repository_id"] {
			record.Repository = repo
			break
		}
	}
	byID := map[string]string{}
	for _, c := range recoveryMaps(result, "conversations") {
		byID[firstString(c["id"])] = firstString(c["native_id"])
	}
	msgs := recoveryMaps(result, "messages")
	for _, c := range recoveryMaps(result, "conversations") {
		conversation := ConversationRecord{NativeID: firstString(c["native_id"]), Provider: firstString(c["provider"]), Account: firstString(c["account"]), Origin: firstString(c["origin"]), Model: firstString(c["model"]), Effort: firstString(c["effort"]), Coverage: firstString(c["coverage"]), StartedAt: firstString(c["started_at"]), EndedAt: firstString(c["ended_at"]), ParentNativeID: byID[firstString(c["parent_id"])], AgentDepth: int(integer(c["agent_depth"])), AgentPath: firstString(c["agent_path"]), AgentNickname: firstString(c["agent_nickname"]), Harness: firstString(c["harness"]), HarnessVersionFirst: firstString(c["harness_version_first"]), HarnessVersionLast: firstString(c["harness_version_last"]), HarnessVersionSource: firstString(c["harness_version_source"])}
		_ = json.Unmarshal([]byte(firstString(c["aliases_json"])), &conversation.Aliases)
		var selected []map[string]any
		for _, m := range msgs {
			if m["conversation_id"] == c["id"] {
				selected = append(selected, m)
			}
		}
		conversation.Messages = storedMessageRecords(selected)
		record.Conversations = append(record.Conversations, conversation)
	}
	result.Record = record
	return result, nil
}

// Only source observations in the contract can be applied. Unknown columns are
// kept in the envelope and reported by verification, never executed as SQL.
func restoreHistoryObservation(ctx context.Context, tx *sql.Tx, o historyObservation, replace bool) error {
	if !historyKnownTable(o.Kind) {
		return fmt.Errorf("unknown recovery observation %q", o.Kind)
	}
	cols, e := tableColumns(tx, o.Kind)
	if e != nil {
		return e
	}
	names := []string{}
	indices := []int{}
	for i, col := range o.Columns {
		if col == "id" && (o.Kind == "metrics" || o.Kind == "metric_ledger") {
			continue
		}
		if cols[col] {
			q, e := quoteRecordIdentifier(col)
			if e != nil {
				return e
			}
			names = append(names, q)
			indices = append(indices, i)
		}
	}
	if len(indices) == 0 {
		return errors.New("recovery observation has no compatible columns")
	}
	table, _ := quoteRecordIdentifier(o.Kind)
	verb := "INSERT OR IGNORE"
	if replace {
		verb = "INSERT"
	}
	updates := []string{}
	for _, name := range names {
		updates = append(updates, name+"=excluded."+name)
	}
	conflict := ""
	if replace {
		conflict = " ON CONFLICT DO UPDATE SET " + strings.Join(updates, ",")
	}
	for _, row := range o.Rows {
		if len(row) != len(o.Columns) {
			return errors.New("invalid recovery observation")
		}
		args := []any{}
		for _, i := range indices {
			x, e := row[i].decode()
			if e != nil {
				return e
			}
			args = append(args, x)
		}
		if _, e = tx.ExecContext(ctx, verb+" INTO "+table+" ("+strings.Join(names, ",")+") VALUES("+placeholders(len(args))+")"+conflict, args...); e != nil {
			return e
		}
	}
	return nil
}

// Catalog fallbacks fill absent entities only. They never grant a later source
// version, prune a richer native entity or replace any accepted native copy.
func (c *Catalog) applyHistoryRecovery(ctx context.Context, e historyRecovery) (string, error) {
	if e.Version != historyFormat || e.Tier != "catalog-derived" || e.Parser != "catalog-recovery-v1" {
		return "", errors.New("unsupported catalog recovery contract")
	}
	outcome := "catalog-derived"
	err := c.writeTransaction(ctx, "replay catalog-derived history", func(tx *sql.Tx) error {
		if e.Kind == "catalog-exclusions" {
			for _, o := range e.Observations {
				if o.Kind != "ask_native_sessions" {
					return errors.New("invalid replay exclusion contract")
				}
				if err := restoreHistoryObservation(ctx, tx, o, false); err != nil {
					return err
				}
			}
			return nil
		}
		if e.Kind == "catalog-tl1" {
			var n int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM tl1_installations WHERE installation_id=?", e.OriginalID).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				outcome = "native-or-existing-kept"
				return nil
			}
			for _, o := range e.Observations {
				if o.Kind != "tl1_installations" && !slices.Contains(tl1SnapshotTables, o.Kind) {
					return errors.New("invalid TL1 recovery scope")
				}
				if err := restoreHistoryObservation(ctx, tx, o, false); err != nil {
					return err
				}
			}
			return nil
		}
		if e.Kind != "catalog-workspace" {
			return errors.New("unsupported recovery entity kind")
		}
		id, err := storedWorkspaceID(tx, e.Record)
		if err != nil {
			return err
		}
		if id != "" {
			// Pending relationships come from the child's selected observation.
			// Reconciliation can resolve them without accepting this older body.
			outcome = "native-or-existing-kept"
			return nil
		}
		// A provider mirror can resolve a core into another existing workspace.
		// Keeping the recovery envelope still preserves the conflicting observation.
		for _, conv := range e.Record.Conversations {
			var count int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM conversations WHERE provider=? AND account=? AND native_id=?", conv.Provider, conv.Account, conv.NativeID).Scan(&count); err != nil {
				return err
			}
			if count > 0 {
				outcome = "identity-conflict-kept"
				return nil
			}
		}
		host, source := "", ""
		for _, s := range recoveryMaps(e, "workspace_sightings") {
			host, source = firstString(s["host_id"]), firstString(s["source_name"])
			break
		}
		if host == "" {
			outcome = "unresolved-original-host"
			return nil
		}
		for _, o := range e.Observations {
			if o.Kind == "hosts" || o.Kind == "repositories" {
				if err = restoreHistoryObservation(ctx, tx, o, false); err != nil {
					return err
				}
			}
		}
		// Repository observations already contain exact JSON arrays, identity
		// fields and locations. Restore them directly: the ordinary repository
		// writer expects parser-shaped values and may probe a local checkout.
		record := e.Record
		record.Repository = nil
		record.catalogRecovery = true
		if _, _, err = ingestCopyModeContext(c.withIndexQueryWait(ctx), tx, record, false, host, source, true, false); err != nil {
			return err
		}
		id, err = storedWorkspaceID(tx, e.Record)
		if err != nil {
			return err
		}
		if id != e.OriginalID {
			return errors.New("recovery identity changed; observation retained without application")
		}
		for _, workspace := range recoveryMaps(e, "workspaces") {
			if firstString(workspace["id"]) != id {
				return errors.New("recovery workspace observation identity mismatch")
			}
			if _, err = tx.ExecContext(ctx, "UPDATE workspaces SET repository_id=?,location_history_json=? WHERE id=?",
				nilIfEmpty(firstString(workspace["repository_id"])), defaultString(workspace["location_history_json"], "[]"), id); err != nil {
				return err
			}
		}
		// The ordinary writer necessarily observes this whole core under one
		// host. Replace those generated observations with the exact original
		// per-conversation sightings and versions, including legacy absence.
		if _, err = tx.ExecContext(ctx, "DELETE FROM copy_versions WHERE workspace_id=?", id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM workspace_sightings WHERE workspace_id=?", id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM conversation_sightings WHERE conversation_id IN (SELECT id FROM conversations WHERE workspace_id=?)", id); err != nil {
			return err
		}
		for _, conversation := range recoveryMaps(e, "conversations") {
			if _, err = tx.ExecContext(ctx, "UPDATE conversations SET origin_host_id=? WHERE id=? AND workspace_id=?", conversation["origin_host_id"], conversation["id"], id); err != nil {
				return err
			}
		}
		// The parser writer numbers a single transcript from zero. Catalog
		// observations can instead contain sparse, duplicate or NULL positions
		// from divergent host copies. Restore those exact values before readers
		// and supplemental session membership use the recovered messages.
		for _, message := range recoveryMaps(e, "messages") {
			result, err := tx.ExecContext(ctx, `UPDATE messages SET source_order=? WHERE id=? AND conversation_id=?
				AND conversation_id IN (SELECT id FROM conversations WHERE workspace_id=?)`,
				message["source_order"], message["id"], message["conversation_id"], id)
			if err != nil {
				return err
			}
			if count, err := result.RowsAffected(); err != nil {
				return err
			} else if count != 1 {
				return errors.New("recovery message observation identity mismatch")
			}
		}
		for _, conversation := range recoveryMaps(e, "conversations") {
			conversationID := firstString(conversation["id"])
			if err = repairMessageSearch(ctx, tx, conversationID); err != nil {
				return err
			}
			if err = upsertConversationDocument(tx, conversationID); err != nil {
				return err
			}
		}
		// Base entities are made by the production writer; their exact source
		// fields and supplemental observations are retained separately. FTS,
		// vectors, tool caches and checkpoints are regenerated, never copied.
		skip := []string{"workspaces", "repositories", "hosts", "conversations", "messages", "conversation_identity_links", "copy_evidence", "conversation_parents"}
		for _, o := range e.Observations {
			if slices.Contains(skip, o.Kind) {
				continue
			}
			if o.Kind == "agent_sessions" {
				if err = restoreHistoryAgentSessions(ctx, tx, o, id); err != nil {
					return err
				}
				continue
			}
			if err = restoreHistoryObservation(ctx, tx, o, true); err != nil {
				return err
			}
		}
		// Work items have now been restored with their original IDs. Preserve
		// each conversation's actual association, including an absent one.
		for _, conversation := range recoveryMaps(e, "conversations") {
			workItem := conversation["work_item_id"]
			if _, err = tx.ExecContext(ctx, `UPDATE conversations SET work_item_id=? WHERE id=? AND workspace_id=?
				AND (? IS NULL OR EXISTS(SELECT 1 FROM work_items WHERE id=?))`,
				workItem, conversation["id"], id, workItem, workItem); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, "UPDATE workspaces SET preservation_completeness='catalog-derived' WHERE id=?", id); err != nil {
			return err
		}
		if err = restoreHistoryParents(ctx, tx, e); err != nil {
			return err
		}
		return reconcileHistoryAgentTargets(ctx, tx, e)
	})
	return outcome, err
}

// Pending links are derived index state, committed atomically with recovery.
// They contain only relationships from the chosen retained envelope, so a later
// native index can resolve them without rediscovering unrelated recovery jobs.
const historyParentPrefix = "history_parent:"
const historyAgentPrefix = "history_agent:"

type historyAgentLink struct {
	Version      int    `json:"version"`
	Agent        string `json:"agent"`
	Workspace    string `json:"workspace"`
	Conversation string `json:"conversation"`
	Column       string `json:"column"`
	Target       string `json:"target"`
}

func historyAgentTargetTable(column string) string {
	switch column {
	case "parent_id":
		return "agent_sessions"
	case "delegation_message_id":
		return "messages"
	case "child_conversation_id":
		return "conversations"
	}
	return ""
}

func (link historyAgentLink) key() string {
	// Index by target so restoring one workspace only visits links whose
	// endpoints it can supply, not all unresolved links in the library.
	return historyAgentPrefix + link.Target + ":" + link.Agent + ":" + link.Column
}

func restoreHistoryAgentSessions(ctx context.Context, tx *sql.Tx, o historyObservation, workspace string) error {
	// Insert the base rows before their nullable relationships. A referenced
	// session can occur later even within the same envelope; an external child
	// may not be in this recovery scope at all. Never disable FK enforcement.
	base := historyObservation{Kind: o.Kind, Columns: o.Columns}
	var links []historyAgentLink
	for _, row := range o.Rows {
		if len(row) != len(o.Columns) {
			return errors.New("invalid recovery agent observation")
		}
		baseRow := slices.Clone(row)
		link := historyAgentLink{Version: 1, Workspace: workspace}
		for i, column := range o.Columns {
			x, err := row[i].decode()
			if err != nil {
				return err
			}
			switch column {
			case "id":
				link.Agent = firstString(x)
			case "conversation_id":
				link.Conversation = firstString(x)
			case "workspace_id":
				if firstString(x) != workspace {
					return errors.New("recovery agent observation scope mismatch")
				}
			}
			if historyAgentTargetTable(column) != "" {
				baseRow[i], _ = encodeSQLiteValue(nil)
			}
		}
		for i, column := range o.Columns {
			if historyAgentTargetTable(column) == "" {
				continue
			}
			x, err := row[i].decode()
			if err != nil {
				return err
			}
			if target := firstString(x); target != "" {
				pending := link
				pending.Column, pending.Target = column, target
				links = append(links, pending)
			}
		}
		base.Rows = append(base.Rows, baseRow)
	}
	if err := restoreHistoryObservation(ctx, tx, base, true); err != nil {
		return err
	}
	for _, link := range links {
		if _, err := tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO NOTHING", link.key(), jsonText(link)); err != nil {
			return err
		}
	}
	return nil
}

func reconcileHistoryAgentLink(ctx context.Context, tx *sql.Tx, key, value string) error {
	var link historyAgentLink
	if err := json.Unmarshal([]byte(value), &link); err != nil {
		return err
	}
	table := historyAgentTargetTable(link.Column)
	if link.Version != 1 || table == "" || link.Agent == "" || link.Workspace == "" || link.Conversation == "" || link.Target == "" || key != link.key() {
		return errors.New("invalid pending recovery agent relationship")
	}
	// Identifiers come only from the allowlist above. Fill an absent relation
	// on the selected recovery row; accepted links and native rows keep authority.
	column, _ := quoteRecordIdentifier(link.Column)
	if _, err := tx.ExecContext(ctx, `UPDATE agent_sessions SET `+column+`=? WHERE id=? AND workspace_id=? AND conversation_id=? AND `+column+` IS NULL
		AND EXISTS(SELECT 1 FROM workspaces WHERE id=? AND preservation_completeness='catalog-derived')
		AND EXISTS(SELECT 1 FROM `+table+` WHERE id=?)`, link.Target, link.Agent, link.Workspace, link.Conversation, link.Workspace, link.Target); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE key=? AND NOT EXISTS(
		SELECT 1 FROM agent_sessions a JOIN workspaces w ON w.id=a.workspace_id
		WHERE a.id=? AND a.workspace_id=? AND a.conversation_id=? AND a.`+column+` IS NULL AND w.preservation_completeness='catalog-derived')`,
		key, link.Agent, link.Workspace, link.Conversation)
	return err
}

func reconcileHistoryAgentTargets(ctx context.Context, tx *sql.Tx, e historyRecovery) error {
	targets := map[string]bool{}
	for _, kind := range []string{"conversations", "messages", "agent_sessions"} {
		for _, row := range recoveryMaps(e, kind) {
			targets[firstString(row["id"])] = true
			if kind == "agent_sessions" {
				for _, column := range []string{"parent_id", "delegation_message_id", "child_conversation_id"} {
					targets[firstString(row[column])] = true
				}
			}
		}
	}
	delete(targets, "")
	for target := range targets {
		lower, upper := historyAgentPrefix+target+":", historyAgentPrefix+target+";"
		cursor := ""
		for {
			keys, err := reconcileHistoryLinkBatch(ctx, tx, lower, upper, cursor, reconcileHistoryAgentLink)
			if err != nil || len(keys) < 128 {
				if err != nil {
					return err
				}
				break
			}
			cursor = firstString(keys[len(keys)-1]["key"])
		}
	}
	return nil
}

type historyParentLink struct {
	Version         int    `json:"version"`
	Child           string `json:"child"`
	Workspace       string `json:"workspace"`
	Parent          string `json:"parent"`
	ParentWorkspace string `json:"parent_workspace,omitempty"`
}

// Relationships use original catalog IDs and explicit workspace endpoints.
// Only missing links on catalog-derived children can be filled; a richer native
// workspace or an already different accepted parent always keeps its state.
func restoreHistoryParents(ctx context.Context, tx *sql.Tx, e historyRecovery) error {
	parents := recoveryMaps(e, "conversation_parents")
	for _, row := range recoveryMaps(e, "conversations") {
		parents = append(parents, map[string]any{"conversation_id": row["id"], "workspace_id": row["workspace_id"], "parent_id": row["parent_id"]})
	}
	for _, edge := range parents {
		link := historyParentLink{Version: 1, Child: firstString(edge["conversation_id"]), Workspace: firstString(edge["workspace_id"]), Parent: firstString(edge["parent_id"]), ParentWorkspace: firstString(edge["parent_workspace_id"])}
		if link.Parent == "" {
			continue
		}
		if link.Child == "" || link.Workspace == "" || (link.Workspace != e.OriginalID && link.ParentWorkspace != e.OriginalID) {
			return errors.New("invalid recovery parent relationship scope")
		}
		key := historyParentPrefix + link.Child
		// Only the child's selected observation may introduce a pending link.
		// Incoming edges can resolve that link, never invent one for a child
		// whose chosen observation recorded no parent.
		if link.Workspace == e.OriginalID {
			if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) SELECT ?,?
				WHERE EXISTS(SELECT 1 FROM conversations c JOIN workspaces w ON w.id=c.workspace_id
					WHERE c.id=? AND c.workspace_id=? AND c.parent_id IS NULL AND w.preservation_completeness='catalog-derived')
				ON CONFLICT(key) DO NOTHING`, key, jsonText(link), link.Child, link.Workspace); err != nil {
				return err
			}
		}
		var value string
		if err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", key).Scan(&value); errors.Is(err, sql.ErrNoRows) {
			continue
		} else if err != nil {
			return err
		}
		if err := reconcileHistoryParent(ctx, tx, key, value); err != nil {
			return err
		}
	}
	return nil
}

func reconcileHistoryParent(ctx context.Context, tx *sql.Tx, key, value string) error {
	var link historyParentLink
	if err := json.Unmarshal([]byte(value), &link); err != nil {
		return err
	}
	if link.Version != 1 || link.Child == "" || link.Workspace == "" || link.Parent == "" || key != historyParentPrefix+link.Child {
		return errors.New("invalid pending recovery parent relationship")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE conversations AS child SET parent_id=?
		WHERE child.id=? AND child.workspace_id=? AND child.parent_id IS NULL
		AND EXISTS(SELECT 1 FROM workspaces w WHERE w.id=child.workspace_id AND w.preservation_completeness='catalog-derived')
		AND EXISTS(SELECT 1 FROM conversations p WHERE p.id=? AND (?='' OR p.workspace_id=?))`,
		link.Parent, link.Child, link.Workspace, link.Parent, link.ParentWorkspace, link.ParentWorkspace); err != nil {
		return err
	}
	// Retire resolved links, deleted children and children since replaced by
	// native evidence. Missing parents remain pending across process restarts.
	_, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE key=? AND NOT EXISTS(
		SELECT 1 FROM conversations c JOIN workspaces w ON w.id=c.workspace_id
		WHERE c.id=? AND c.workspace_id=? AND c.parent_id IS NULL AND w.preservation_completeness='catalog-derived')`, key, link.Child, link.Workspace)
	return err
}

type historyLinkReconciler func(context.Context, *sql.Tx, string, string) error

func reconcileHistoryLinkBatch(ctx context.Context, tx *sql.Tx, lower, upper, cursor string, reconcile historyLinkReconciler) ([]map[string]any, error) {
	rows, err := queryMapsContext(ctx, tx, "SELECT key FROM meta WHERE key>=? AND key<? AND key>? ORDER BY key LIMIT 128", lower, upper, cursor)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		key := firstString(row["key"])
		// Keep at most one relationship payload in memory at a time.
		var value string
		if err = tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", key).Scan(&value); err != nil {
			return nil, err
		}
		if err = reconcile(ctx, tx, key, value); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

func (c *Catalog) reconcileHistoryLinks(ctx context.Context) error {
	for _, kind := range []struct {
		prefix    string
		reconcile historyLinkReconciler
	}{{historyParentPrefix, reconcileHistoryParent}, {historyAgentPrefix, reconcileHistoryAgentLink}} {
		cursor := ""
		for {
			var keys []map[string]any
			err := c.writeTransaction(ctx, "reconcile recovered relationships", func(tx *sql.Tx) error {
				// ';' follows the prefix's final ':', bounding its exact range.
				var err error
				keys, err = reconcileHistoryLinkBatch(ctx, tx, kind.prefix, strings.TrimSuffix(kind.prefix, ":")+";", cursor, kind.reconcile)
				return err
			})
			if err != nil {
				return err
			}
			if len(keys) < 128 {
				break
			}
			cursor = firstString(keys[len(keys)-1]["key"])
		}
	}
	return nil
}
