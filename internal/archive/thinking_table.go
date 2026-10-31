package archive

// The timing projection deliberately uses the first retained usage-bearing
// response event, not an unverified request-send clock. Previous response and
// incoming-tool evidence are computed before dashboard date/model filters.
// Keep ordered requests as the outer evidence loop: otherwise SQLite can
// materialize and index every wide request row merely to rejoin its conversation.
const thinkingSource = `WITH RECURSIVE
 alias_edges(a,b) AS (SELECT left_id,right_id FROM conversation_identity_links WHERE relationship='native-alias'
 UNION SELECT right_id,left_id FROM conversation_identity_links WHERE relationship='native-alias'),
 alias_members(id,root) AS (SELECT a,a FROM alias_edges UNION SELECT e.b,m.root FROM alias_members m JOIN alias_edges e ON e.a=m.id),
 families AS (SELECT id,MIN(root) root FROM alias_members GROUP BY id),
 candidates AS (SELECT c.id,ROW_NUMBER() OVER (PARTITION BY COALESCE(f.root,c.id) ORDER BY
 (w.source_kind=c.provider) DESC,(w.id NOT IN (SELECT workspace_id FROM tool_mirror_workspaces)) DESC,
 (SELECT COUNT(*) FROM model_requests mr WHERE mr.conversation_id=c.id) DESC,c.id DESC) choice
 FROM conversations c JOIN workspaces w ON w.id=c.workspace_id LEFT JOIN families f ON f.id=c.id
 WHERE NOT EXISTS(SELECT 1 FROM conversation_group_membership gm WHERE gm.conversation_id=c.id AND gm.active=0)
 AND EXISTS(SELECT 1 FROM model_requests mr WHERE mr.conversation_id=c.id)),
 incoming AS (SELECT next_request_id,MAX(unixepoch(ended_at,'subsec')) anchor,
 SUM(CASE WHEN unixepoch(ended_at,'subsec') IS NULL THEN 1 ELSE 0 END) missing,
 COUNT(DISTINCT COALESCE(agent_session_id,'')) streams,MIN(COALESCE(agent_session_id,'')) stream,
 CASE WHEN COUNT(DISTINCT tool_category)=1 THEN MIN(tool_category) ELSE 'mixed' END preceding_tool
 FROM tool_calls WHERE next_request_id IS NOT NULL GROUP BY next_request_id),
 ordered AS (SELECT mr.id,mr.conversation_id,mr.agent_session_id,mr.native_id,mr.sequence,mr.requested_at,mr.model,mr.effort,
 mr.input_tokens,mr.cache_read_input_tokens,mr.output_tokens,mr.reasoning_output_tokens,mr.compacted_before,LAG(mr.id) OVER (PARTITION BY conversation_id,COALESCE(agent_session_id,'') ORDER BY mr.sequence,mr.id) previous_request_id,LAG(unixepoch(requested_at,'subsec')) OVER
 (PARTITION BY conversation_id,COALESCE(agent_session_id,'') ORDER BY mr.sequence,mr.id) previous_at
 FROM model_requests mr CROSS JOIN candidates c ON c.id=mr.conversation_id AND c.choice=1),
 evidence AS (SELECT p.*,unixepoch(p.requested_at,'subsec') observed_at,i.anchor,i.missing,i.streams,i.stream,
 COALESCE(i.preceding_tool,'none') preceding_tool,c.workspace_id,c.provider,c.harness,c.agent_depth,
 w.source_kind,r.display_name repository_name,COALESCE(o.execution_machine,o.machine,'Unknown') machine
 FROM ordered p CROSS JOIN conversations c ON c.id=p.conversation_id JOIN workspaces w ON w.id=c.workspace_id
 LEFT JOIN repositories r ON r.id=w.repository_id LEFT JOIN incoming i ON i.next_request_id=p.id
 LEFT JOIN conversation_origins o ON o.conversation_id=c.id),
 durations AS (SELECT *,ROUND((observed_at-anchor)*1000)/1000.0 observed_gap FROM evidence),
 qualified AS (SELECT *,CASE
 WHEN observed_at IS NULL THEN 'Missing response timestamp'
 WHEN anchor IS NULL THEN 'No linked tool timestamp'
 WHEN missing>0 THEN 'Incomplete tool timestamps'
 WHEN streams<>1 OR (stream<>'' AND stream<>COALESCE(agent_session_id,'')) THEN 'Different agent stream'
 WHEN previous_at IS NOT NULL AND anchor<=previous_at THEN 'Stale tool anchor'
 WHEN observed_gap<=0 THEN 'Nonpositive gap'
 WHEN observed_gap<0.1 THEN 'Below 100 ms'
 WHEN observed_gap>600 THEN 'Above 600 seconds'
 ELSE 'Usable timing' END timing_quality FROM durations)
 SELECT id,conversation_id,agent_session_id,workspace_id,native_id,previous_request_id,sequence,requested_at,CAST(ROUND(observed_at*1000) AS INTEGER) observed_millis,COALESCE(NULLIF(model,''),'Unreported') model,COALESCE(NULLIF(effort,''),'Unreported') effort,
 input_tokens,cache_read_input_tokens,output_tokens,reasoning_output_tokens,compacted_before,provider,harness,agent_depth,
 source_kind,repository_name,machine,preceding_tool,timing_quality,
 CASE WHEN timing_quality='Usable timing' THEN observed_gap END gap_seconds,
 strftime('%Y-%m-%dT%H:%M:%fZ',anchor,'unixepoch') tool_result_at FROM qualified`

var thinkingRequestDataset = func() sqlDataset {
	columns := map[string]string{}
	for _, name := range []string{"id", "conversation_id", "agent_session_id", "workspace_id", "native_id", "sequence", "requested_at", "model", "effort", "input_tokens", "cache_read_input_tokens", "output_tokens", "reasoning_output_tokens", "compacted_before", "provider", "harness", "agent_depth", "source_kind", "repository_name", "machine", "preceding_tool", "timing_quality", "gap_seconds", "tool_result_at"} {
		columns[name] = "d." + name
	}
	columns["model_effort"] = "model_effort(d.model,d.effort)"
	columns["day"] = "date(d.requested_at,'localtime')"
	columns["week"] = "date(d.requested_at,'localtime','-6 days','weekday 1')"
	columns["month"] = "strftime('%Y-%m',d.requested_at,'localtime')"
	return sqlDataset{from: "FROM (" + thinkingSource + ") d", columns: columns, timestamps: map[string]string{"requested_at": "d.requested_at"}}
}()
