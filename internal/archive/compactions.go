package archive

// Compactions are boundary events, not the requests that happen to follow
// them. This retains consecutive boundaries and boundaries without later usage.
// Codex replayed boundaries have no attributed trigger and are left out, as
// they are in the Library's compaction count.
const compactionBodySQL = "CASE WHEN json_valid(m.text) THEN m.text ELSE '{}' END"
const compactionBoundarySQL = "m.kind='metadata' AND (json_extract(" + compactionBodySQL + ",'$.subtype')='compact_boundary' OR json_extract(" + compactionBodySQL + ",'$.compaction_trigger') IS NOT NULL)"

// Select a transcript within each explicit native-alias family before applying
// chart filters. Only active conversations with boundary evidence compete, so
// an empty native transcript cannot hide its mirror's events. Prefer native
// evidence, then the fuller boundary transcript, with a stable identity tie.
// This population is independent of model requests and the tool-call rollup.
var compactionAliasExclusionSQL = `WITH RECURSIVE
 alias_edges(a,b) AS (SELECT left_id,right_id FROM conversation_identity_links WHERE relationship='native-alias'
 UNION SELECT right_id,left_id FROM conversation_identity_links WHERE relationship='native-alias'),
 alias_members(id,root) AS (SELECT a,a FROM alias_edges UNION SELECT e.b,m.root FROM alias_members m JOIN alias_edges e ON e.a=m.id),
 families AS (SELECT id,MIN(root) root FROM alias_members GROUP BY id),
 boundaries AS (SELECT m.conversation_id,COUNT(*) n FROM families f JOIN messages m ON m.conversation_id=f.id
 WHERE ` + compactionBoundarySQL + ` GROUP BY m.conversation_id)
 SELECT id FROM (SELECT c.id,ROW_NUMBER() OVER (PARTITION BY f.root ORDER BY
 (w.source_kind=c.provider) DESC,b.n DESC,c.id DESC) choice
 FROM boundaries b JOIN families f ON f.id=b.conversation_id JOIN conversations c ON c.id=b.conversation_id
 JOIN workspaces w ON w.id=c.workspace_id WHERE ` + currentGroupConversation("c") + `) WHERE choice>1`

var compactionDataset = func() sqlDataset {
	body := compactionBodySQL
	trigger := "COALESCE(json_extract(" + body + ",'$.compactMetadata.trigger'),json_extract(" + body + ",'$.compaction_trigger'))"
	columns := map[string]string{
		"id": "m.id", "message_id": "m.id", "conversation_id": "c.id", "workspace_id": "c.workspace_id",
		"occurred_at": "m.created_at", "day": "date(m.created_at,'localtime')", "week": "date(m.created_at,'localtime','-6 days','weekday 1')", "month": "strftime('%Y-%m',m.created_at,'localtime')",
		"trigger":          "CASE " + trigger + " WHEN 'manual' THEN 'human' WHEN 'auto' THEN 'machine' ELSE 'unknown' END",
		"trigger_evidence": "CASE WHEN json_extract(" + body + ",'$.compactMetadata.trigger') IN ('manual','auto') THEN 'recorded' WHEN json_extract(" + body + ",'$.compaction_trigger') IN ('manual','auto') THEN 'inferred' ELSE 'unknown' END",
		"provider":         "c.provider", "harness": "c.harness", "model": "COALESCE(NULLIF(m.model,''),NULLIF(c.model,''),'Unreported')",
		"repository_name": "r.display_name", "title": "w.title", "source_kind": "w.source_kind", "agent_depth": "c.agent_depth",
		"channel": "o.channel", "machine_id": "o.machine_id", "machine": "o.machine", "execution_machine_id": "o.execution_machine_id", "execution_machine": "o.execution_machine",
	}
	return sqlDataset{from: `FROM messages m JOIN conversations c ON c.id=m.conversation_id
 JOIN workspaces w ON w.id=c.workspace_id LEFT JOIN repositories r ON r.id=w.repository_id
 LEFT JOIN conversation_origins o ON o.conversation_id=c.id`,
		base:    compactionBoundarySQL + " AND " + currentGroupConversation("c") + " AND c.id NOT IN (" + compactionAliasExclusionSQL + ")",
		columns: columns, timestamps: map[string]string{"occurred_at": "m.created_at"}, lookups: map[string]bool{"conversation_id": true, "workspace_id": true, "message_id": true}}
}()
