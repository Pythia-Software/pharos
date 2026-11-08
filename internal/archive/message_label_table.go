package archive

import (
	"context"
	"strings"
)

// One row per most-specific accepted message label, or one coverage row when
// none exists. Distinct message counts preserve overlapping labels; dirty
// classifications never enter the accepted population. This is a read-only
// projection of the stored ledger, not an inference pass.
// Decode each distinct mask once, rather than expanding the taxonomy for every
// message. The query-local CTEs also keep metrics over large libraries practical.
const messageLabelKindsSQL = "('message','reasoning','thinking','delegation','delegation_result')"
const messageLabelMessagePredicate = "m.kind IN " + messageLabelKindsSQL + " AND m.selected=1"

// Most timeline records are metadata or tool payloads. Cover the selected prose
// inputs so label counts need not visit the large transcript-body pages.
const messageLabelMessageIndexSQL = "CREATE INDEX IF NOT EXISTS messages_label_modes_idx ON messages(created_at,conversation_id,id,role,kind,model) WHERE kind IN " + messageLabelKindsSQL + " AND selected=1"

// Incremental refreshes seek conversations, rather than a time range. Keep
// their inputs covered too, without visiting tool payloads or transcript pages.
const messageLabelConversationIndexSQL = "CREATE INDEX IF NOT EXISTS messages_label_conversation_idx ON messages(conversation_id,created_at,id,role,kind,model) WHERE kind IN " + messageLabelKindsSQL + " AND selected=1"

// The classifier ledger contains probabilities and other per-message blobs;
// dashboard joins consume just this small accepted-label projection.
const messageLabelLedgerIndexSQL = "CREATE INDEX IF NOT EXISTS message_labels_query_idx ON message_labels(message_id,model,labels,segment,labeled_at)"
const messageLabelMaskIndexSQL = "CREATE INDEX IF NOT EXISTS message_labels_masks_idx ON message_labels(conversation_id,model,labels)"

var messageLabelDataset = messageLabelLiveDataset("")

// messageLabelLiveDataset is messageLabelLiveDatasetFor with the unmarked
// fallback, for SQL read without the catalog (executionUnmarkedFallbackSQL).
func messageLabelLiveDataset(conversations string) sqlDataset {
	return messageLabelLiveDatasetFor(conversations, executionUnmarkedFallbackSQL("message_labels_conversations"))
}

// Labels of a conversation that is dirty, or whose execution evidence changed
// without a mark yet (unmarked: executionUnmarkedSQL, "" for none), read as
// unlabeled: pending, never old attribution presented as current.
//
// Restrict both messages and mask decoding when only a pending subset needs
// live projection. The selector is host-owned SQL, never request text.
func messageLabelLiveDatasetFor(conversations, unmarked string) sqlDataset {
	if unmarked != "" {
		unmarked = "\n AND m.conversation_id NOT IN (" + unmarked + ")"
	}
	source := `FROM messages m
 JOIN conversations c ON c.id=m.conversation_id
 LEFT JOIN workspaces w ON w.id=c.workspace_id LEFT JOIN repositories repo ON repo.id=w.repository_id
 LEFT JOIN message_authorship ma ON ma.message_id=m.id
 LEFT JOIN message_labels l ON l.message_id=m.id AND EXISTS(SELECT 1 FROM message_labels_conversations complete WHERE complete.conversation_id=m.conversation_id) AND NOT EXISTS(SELECT 1 FROM message_labels_dirty dirty WHERE dirty.conversation_id=m.conversation_id)
 ` + unmarked + `
 LEFT JOIN message_labels_models lm ON lm.model=l.model
 LEFT JOIN (
 WITH taxonomy AS MATERIALIZED (
 SELECT lm.model,tag.value,(1 << CAST(tag.key AS INTEGER)) bit,
 COALESCE((SELECT SUM(1 << CAST(child.key AS INTEGER)) FROM json_each(lm.labels_json) child
 WHERE CAST(child.key AS INTEGER)<64 AND substr(child.value,1,length(tag.value)+1)=tag.value||'.'),0) descendants
 FROM message_labels_models lm JOIN json_each(CASE WHEN json_valid(lm.labels_json) THEN lm.labels_json ELSE '[]' END) tag
 WHERE CAST(tag.key AS INTEGER)<64 AND (tag.value IN ('P','C') OR tag.value LIKE 'P.%' OR tag.value LIKE 'C.%')
 ), masks AS MATERIALIZED (SELECT DISTINCT model,labels FROM message_labels),
 decoded AS MATERIALIZED (SELECT masks.model,masks.labels,taxonomy.value FROM masks JOIN taxonomy
 ON taxonomy.model=masks.model AND (masks.labels & taxonomy.bit)<>0 AND (masks.labels & taxonomy.descendants)=0)
 SELECT * FROM decoded
 ) tag ON tag.model=l.model AND tag.labels=l.labels`
	label := "COALESCE(tag.value,'?')"
	family := `CASE WHEN tag.value IS NULL THEN '?' WHEN instr(tag.value,'.')=0 THEN tag.value ELSE
 substr(tag.value,1,instr(tag.value,'.')+CASE WHEN instr(substr(tag.value,instr(tag.value,'.')+1),'.')=0 THEN length(tag.value) ELSE instr(substr(tag.value,instr(tag.value,'.')+1),'.')-1 END) END`
	columns := map[string]string{
		"id": "m.id||':'||" + label, "message_id": "m.id", "conversation_id": "c.id", "workspace_id": "c.workspace_id",
		"fingerprint": "c.id", "title": "COALESCE(NULLIF(c.agent_nickname,''),w.title,c.id)", "repository_name": "repo.display_name",
		"provider": "c.provider", "model": "CASE WHEN m.role IN ('assistant','agent') THEN COALESCE(NULLIF(m.model,''),c.model) ELSE NULL END",
		"day": "date(m.created_at,'localtime')", "week": "date(m.created_at,'localtime','-6 days','weekday 1')", "month": "strftime('%Y-%m',m.created_at,'localtime')", "occurred_at": "m.created_at", "kind": "m.kind", "role": "m.role", "subagent": "c.parent_id IS NOT NULL",
		"label": label, "family": family, "function_role": "CASE WHEN substr(tag.value,1,1)='P' THEN 'Sender' WHEN substr(tag.value,1,1)='C' THEN 'Receiver' ELSE 'Coverage' END",
		"label_status": "CASE WHEN tag.value IS NOT NULL THEN 'matched' WHEN l.message_id IS NULL OR lm.model IS NULL THEN 'unlabeled' ELSE 'no_match' END",
		"source":       "CASE WHEN m.role IN ('assistant','agent') AND m.kind IN ('thinking','reasoning') THEN 'thinking' WHEN m.role IN ('assistant','agent') THEN 'output' WHEN m.role='user' AND (COALESCE(ma.typed_words,0)>0 OR l.segment IN ('H-typed','H-typed+ctx','H-feedback','H-feedback+ctx','H-resent','H-resent+ctx')) THEN 'human' WHEN m.role='user' THEN 'supplied' WHEN m.role='tool' THEN 'tool' ELSE 'unknown' END",
		"segment":      "l.segment", "label_model": "l.model", "labeled_at": "l.labeled_at",
		"label_depth": "CASE WHEN tag.value IS NULL THEN 0 ELSE length(tag.value)-length(replace(tag.value,'.',''))+1 END",
		"text":        "substr(" + timelineContentSQL("m.text") + ",1,640)",
	}
	base := messageLabelMessagePredicate
	if conversations != "" {
		base += " AND m.conversation_id IN (" + conversations + ")"
		// A pending set is usually empty or a few conversations. SQLite can
		// otherwise prefer scanning the entire eligible-message time index,
		// even for an empty queue, defeating incremental reads and refreshes.
		source = strings.Replace(source, "FROM messages m\n", "FROM messages m INDEXED BY messages_conversation_idx\n", 1)
		source = strings.Replace(source, "SELECT DISTINCT model,labels FROM message_labels", "SELECT DISTINCT model,labels FROM message_labels WHERE conversation_id IN ("+conversations+")", 1)
	}
	return sqlDataset{from: source, base: base, columns: columns,
		optionalJoins: map[string]string{
			"ma":   " LEFT JOIN message_authorship ma ON ma.message_id=m.id",
			"lm":   " LEFT JOIN message_labels_models lm ON lm.model=l.model",
			"w":    " LEFT JOIN workspaces w ON w.id=c.workspace_id",
			"repo": " LEFT JOIN repositories repo ON repo.id=w.repository_id",
		}, timestamps: map[string]string{"occurred_at": "m.created_at"}, lookups: map[string]bool{"message_id": true, "conversation_id": true, "workspace_id": true}}
}

// Optional labeling ledgers are created by the background labeler. Querying an
// unlabelled library must expose its missing coverage without creating tables.
//
// Datasets resting on execution evidence read the current unmarked set
// (executionUnmarkedSQL) when they are routed: it is computed once per source
// generation and propagation state, and both move the cached-view generation.
func (c *Catalog) sqlDatasetFor(dataset string) (table sqlDataset, ok bool) {
	defer func() {
		if ok {
			table = withAgentSessionIdentity(dataset, table)
		}
	}()
	ctx := context.Background()
	table, ok = sqlDatasetFor(dataset)
	if (dataset == "agent_events" || dataset == "agent_event_messages" || dataset == "agent_event_rates") && ok {
		// Leave out events resting on superseded execution evidence once the
		// events ledger exists (agent_events.go); imports alone have none.
		// Rates leave out the conversation's messages from both sides.
		if present, err := agentEventsLedgerPresent(ctx, c.reader()); err == nil && present {
			alias := map[string]string{"agent_events": "e", "agent_event_messages": "a", "agent_event_rates": "f"}[dataset]
			return withheldAgentEvents(table, alias, agentEventsWithheldSQLFor(ctx, c.reader())), true
		}
		return table, ok
	}
	if dataset == "compactions" {
		// Read stored boundaries once they are complete; until then, and in a
		// catalog opened without Initialize, derive them from message bodies.
		if ready, err := compactionBoundariesReady(ctx, c.reader()); err == nil && ready {
			return compactionStoredDataset, true
		}
		return table, ok
	}
	if dataset != "message_labels" || !ok {
		return table, ok
	}
	unmarked := executionUnmarkedSQL(ctx, c.reader(), "message_labels_conversations")
	var ready string
	if err := c.reader().QueryRow("SELECT value FROM meta WHERE key='message_label_facts_ready'").Scan(&ready); err == nil && ready == messageLabelFactsVersion() {
		return messageLabelStoredDataset(c.hasLabelsLedger(), unmarked), true
	}
	table = messageLabelLiveDatasetFor("", unmarked)
	// Older read-only snapshots need not have the additive index yet. Retain
	// their valid query path instead of forcing an unavailable index.
	var indexed int
	if err := c.reader().QueryRow("SELECT count(*) FROM sqlite_master WHERE type='index' AND name='messages_label_modes_idx'").Scan(&indexed); err == nil && indexed == 1 {
		table.pageFrom = "FROM messages m"
		table.coveringIndex, table.rangeCoveringIndex = "messages_label_modes_idx", "messages_label_modes_idx"
		table.coveredFields = map[string]bool{}
		for field := range table.columns {
			if field != "text" {
				table.coveredFields[field] = true
			}
		}
	}
	var count int
	err := c.reader().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('message_labels','message_labels_models','message_labels_dirty','message_labels_conversations')`).Scan(&count)
	if err == nil && count < 4 {
		table = messageLabelWithoutLedger(table)
	}
	return table, ok
}
