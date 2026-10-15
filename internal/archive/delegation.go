package archive

// The delegation read model sits above the existing transcript/session ledgers.
// It reconciles source aliases without rewriting them, and never loads transcript
// bodies (only bounded delegation-result evidence). Cached snapshots are immutable.
import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type delegationMetric struct {
	Value   *float64 `json:"value"`
	Quality string   `json:"quality"`
	Known   int      `json:"known"`
	Total   int      `json:"total"`
}
type delegationMetricSpec struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Unit       string `json:"unit"`
	Definition string `json:"definition"`
}

var delegationMetrics = []delegationMetricSpec{
	{"start", "First evidence", "date", "Earliest retained timestamp, not a lifecycle start."},
	{"tokens", "Tokens", "tokens", "Reconciled session usage. Cache is a subset of input; reasoning is a subset of output."},
	{"cost", "API cost", "usd", "API-rate estimate on the date of use, not billing. Partial values are lower bounds."},
	{"calls", "Tool calls", "count", "Distinct retained tool invocations."}, {"errors", "Errors", "count", "Reported tool failures; not the outcome of the agent."},
	{"files", "Files Edited", "count", "Distinct file paths recorded by successful edit/write tools, including adds/deletes. Repeated edits count once; Self + children deduplicates across sessions. Shell edits and extra paths in multi-file patches may be unavailable."},
	{"messages", "Messages", "count", "Retained session message membership."}, {"requests", "Requests", "count", "Distinct retained model requests."}, {"turns", "User turns", "count", "Retained user message starts; injected agent assignments do not count as user turns."},
	{"clock", "Clock span", "ms", "Earliest to latest evidence across included sessions, including unclassified time."},
	{"total", "Session spans", "ms", "Sum of session evidence envelopes. Parallel and inactive time accumulate."},
	{"toolTime", "Tool elapsed", "ms", "Union of call-to-result intervals across included sessions. Does not measure CPU time."},
	{"toolAgent", "Agent tool time", "ms", "Sum of each session's tool interval union; parallel agents accumulate."},
	{"toolSum", "Tool duration sum", "ms", "Sum of recorded call durations; overlapping calls accumulate."},
	{"wait", "Blocking wait", "ms", "Requires explicit blocking boundaries. Launch acknowledgements and polling are excluded."},
	{"modelTime", "Model processing", "ms", "Requires measured model start/end boundaries. Inferred timeline backgrounds are excluded."},
	{"backgroundTime", "Background process", "ms", "Requires process lifecycle boundaries. A background launch is insufficient."},
	{"episodes", "Evidence episodes", "count", "Clusters of tool/request timestamps and message-envelope endpoints separated by more than 30 minutes; inferred, not completed runs."},
	{"children", "Children", "count", "Self counts direct ownership children; Self + children counts all distinct descendants."},
	{"leaves", "Leaves", "count", "Included sessions with no known ownership children."}, {"depth", "Depth", "count", "Maximum ownership depth of included sessions."},
	{"input", "Input", "tokens", "Input including cached input."}, {"uncached", "Uncached input", "tokens", "Input excluding cache read and creation."},
	{"cacheRead", "Cache read", "tokens", "Subset of input."}, {"cacheWrite", "Cache creation", "tokens", "Subset of input."},
	{"output", "Output", "tokens", "Output including reported reasoning."}, {"reasoning", "Reasoning", "tokens", "Subset of output."},
	{"remainder", "Unallocated usage", "tokens", "Session total beyond retained request usage. Remains at session level."},
}

type delegationReference struct {
	ConversationID string `json:"conversation_id"`
	WorkspaceID    string `json:"workspace_id"`
	Label          string `json:"label"`
	Provider       string `json:"provider"`
	MessageID      string `json:"message_id,omitempty"`
}
type delegationNode struct {
	ID             string                      `json:"id"`
	ConversationID string                      `json:"conversation_id"`
	WorkspaceID    string                      `json:"workspace_id"`
	ParentID       string                      `json:"parent_id,omitempty"`
	ParentEvidence string                      `json:"parent_evidence,omitempty"`
	ExternalParent *delegationReference        `json:"external_parent,omitempty"`
	ExternalChild  *delegationReference        `json:"external_child,omitempty"`
	MessageID      string                      `json:"message_id,omitempty"`
	Label          string                      `json:"label"`
	Provider       string                      `json:"provider"`
	Model          string                      `json:"model"`
	Effort         string                      `json:"effort"`
	Coverage       string                      `json:"coverage"`
	Usage          string                      `json:"usage_status"`
	Depth          int                         `json:"depth"`
	Outline        string                      `json:"outline"`
	Order          int                         `json:"order"`
	ChildCount     int                         `json:"child_count"`
	Start          *float64                    `json:"start"`
	End            *float64                    `json:"end"`
	SubtreeStart   *float64                    `json:"subtree_start"`
	SubtreeEnd     *float64                    `json:"subtree_end"`
	Self           map[string]delegationMetric `json:"self"`
	All            map[string]delegationMetric `json:"all"`
	children       []*delegationNode
	tools          []delegationTool
	requests       []float64
	points         []float64
	episodes       []delegationSpan
	editedFiles    map[string]bool
	fileCalls      int
	fileKnown      int
	filesPartial   bool
}
type delegationSpan struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}
type delegationTool struct {
	ID        string   `json:"id"`
	MessageID string   `json:"message_id"`
	Name      string   `json:"name"`
	Category  string   `json:"category"`
	Status    string   `json:"status"`
	Start     *float64 `json:"start"`
	End       *float64 `json:"end"`
	Duration  *float64 `json:"duration"`
}
type delegationSnapshot struct {
	ID         string
	Nodes      []*delegationNode
	ByID       map[string]*delegationNode
	Aliases    map[string]string
	Roots      []*delegationNode
	Maxima     map[string]map[string]float64
	Totals     map[string]delegationMetric
	Start, End *float64
	Density    []int
	Warnings   []string
}

func delegationTime(value any) *float64 {
	if t, ok := parseTime(firstString(value)); ok {
		v := float64(t.UnixMilli())
		return &v
	}
	return nil
}
func delegationNumber(value float64, quality string, known, total int) delegationMetric {
	return delegationMetric{&value, quality, known, total}
}
func delegationUnknown() delegationMetric { return delegationMetric{Quality: "unknown"} }
func delegationUnion(spans []delegationSpan) float64 {
	if len(spans) == 0 {
		return 0
	}
	spans = append([]delegationSpan(nil), spans...)
	sort.Slice(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
	start, end, total := spans[0].Start, spans[0].End, 0.0
	for _, s := range spans[1:] {
		if s.Start > end {
			total += end - start
			start, end = s.Start, s.End
		} else {
			end = max(end, s.End)
		}
	}
	return total + end - start
}
func (c *Catalog) delegationSnapshot(ctx context.Context, workspace string) (*delegationSnapshot, error) {
	return cachedValue(ctx, c, "delegation:"+workspace, func(ctx context.Context) (*delegationSnapshot, error) {
		return c.readDelegationSnapshot(ctx, workspace)
	})
}

// Materialize compact result references once per conversation. Joining messages
// directly by conversation_id and call_id scans the whole transcript for every
// launch: the conversation index does not cover call_id. Keep bodies outside the
// temporary table and fetch only the matching results, bounded to 8192 characters.
const delegationResultEvidenceSQL = `WITH launches AS MATERIALIZED (
 SELECT a.id,d.conversation_id,d.call_id FROM agent_sessions a
 JOIN messages d ON d.id=a.delegation_message_id
 WHERE a.workspace_id=? AND a.child_conversation_id IS NULL AND a.native_id<>'main'
), results AS MATERIALIZED (
 SELECT m.id,m.conversation_id,m.call_id FROM messages m
 WHERE m.conversation_id IN (SELECT conversation_id FROM launches)
 AND +m.kind IN ('delegation_result','tool_result')
)
SELECT l.id,substr(m.text,1,8192) text FROM launches l
 JOIN results r ON r.conversation_id=l.conversation_id AND r.call_id=l.call_id
 JOIN messages m ON m.id=r.id`

// Copy only the small accounting fields before joining session membership.
// Looking up every message by its hashed ID makes long transcripts perform
// thousands of random reads of the wide message table on external libraries.
const delegationMessageCountsSQL = `WITH message_facts AS MATERIALIZED (
 SELECT m.id,m.role,m.kind,m.created_at FROM conversations c
 JOIN messages m ON m.conversation_id=c.id WHERE c.workspace_id=?
)
SELECT am.agent_session_id,COUNT(*) messages,
 SUM(CASE WHEN m.role='user' AND m.kind='message' THEN 1 ELSE 0 END) turns,
 MIN(m.created_at) start,MAX(m.created_at) end
 FROM agent_sessions a JOIN agent_session_messages am ON am.agent_session_id=a.id
 JOIN message_facts m ON m.id=am.message_id WHERE a.workspace_id=?
 GROUP BY am.agent_session_id`

func (c *Catalog) readDelegationSnapshot(ctx context.Context, workspace string) (*delegationSnapshot, error) {
	tx, err := c.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var workspaceTitle string
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(title,'Untitled work') FROM workspaces WHERE id=?", workspace).Scan(&workspaceTitle); err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	conversations, err := queryMapsContext(ctx, tx, `SELECT id,provider,model,effort,native_id,parent_id,agent_nickname,coverage,started_at,ended_at,
 (SELECT substr(d.initiation,1,180) FROM conversation_documents d WHERE d.conversation_id=c.id) prompt
 FROM conversations c WHERE workspace_id=? ORDER BY started_at,id LIMIT 10001`, workspace)
	if err != nil {
		return nil, err
	}
	// Providers normalize launch arguments into input, but use different task
	// fields. Codex can retain a readable task_name beside an encrypted message.
	// Extract before clipping: the name may follow a very large message. Unknown
	// structured payloads and opaque messages must not become display labels.
	sessions, err := queryMapsContext(ctx, tx, `SELECT a.*,substr(CASE WHEN json_valid(m.text) THEN
 (SELECT value FROM json_each(m.text,'$.input')
  WHERE type='text' AND key IN ('task_name','description','prompt','Prompt','task','message')
   AND trim(value,char(9)||char(10)||char(13)||' ')<>'' AND value NOT GLOB 'gAAAA*'
  ORDER BY CASE key WHEN 'task_name' THEN 0 WHEN 'description' THEN 1 WHEN 'prompt' THEN 2 WHEN 'Prompt' THEN 3 WHEN 'task' THEN 4 ELSE 5 END
  LIMIT 1)
 ELSE m.text END,1,180) assignment
 FROM agent_sessions a LEFT JOIN messages m ON m.id=a.delegation_message_id
 WHERE a.workspace_id=? ORDER BY a.started_at,a.id LIMIT 10001`, workspace)
	if err != nil {
		return nil, err
	}
	if len(sessions) > 10000 || len(conversations) > 10000 {
		return nil, fmt.Errorf("workspace exceeds the 10,000-session analysis limit")
	}
	s := &delegationSnapshot{ByID: map[string]*delegationNode{}, Aliases: map[string]string{}, Maxima: map[string]map[string]float64{"self": {}, "all": {}}, Density: make([]int, 120), Warnings: []string{}}
	conv := map[string]map[string]any{}
	roots := map[string]string{}
	raw := map[string]map[string]any{}
	for _, cv := range conversations {
		conv[firstString(cv["id"])] = cv
	}
	for _, row := range sessions {
		id, cid := firstString(row["id"]), firstString(row["conversation_id"])
		raw[id] = row
		if row["native_id"] == "main" {
			roots[cid] = id
		}
	}
	// Conversations without a derived session remain navigable with unknown usage.
	for _, cv := range conversations {
		cid := firstString(cv["id"])
		if roots[cid] == "" {
			id := "conversation:" + cid
			roots[cid] = id
			row := map[string]any{"id": id, "conversation_id": cid, "provider": cv["provider"], "model": cv["model"], "started_at": cv["started_at"], "ended_at": cv["ended_at"], "usage_status": "unavailable", "native_id": "main"}
			raw[id] = row
			sessions = append(sessions, row)
		}
	}
	// Bounded reads of launch results recover exact async handles, including
	// nested children whose catalog parent was flattened to the root.
	aliasesChild := map[string]string{}
	for _, row := range sessions {
		if child := firstString(row["child_conversation_id"]); roots[child] != "" {
			aliasesChild[firstString(row["id"])] = roots[child]
		}
	}
	evidence, err := queryMapsContext(ctx, tx, delegationResultEvidenceSQL, workspace)
	if err != nil {
		return nil, err
	}
	for _, row := range evidence {
		var p map[string]any
		if json.Unmarshal([]byte(firstString(row["text"])), &p) != nil {
			continue
		}
		agent := firstString(mapValueDefault(p["details"])["agentId"])
		if agent == "" {
			parts, _ := sliceValue(p["content"])
			for _, part := range parts {
				text := firstString(mapValueDefault(part)["text"])
				if _, after, ok := strings.Cut(text, "agentId:"); ok {
					fields := strings.Fields(after)
					if len(fields) > 0 {
						agent = fields[0]
					}
				}
			}
		}
		if agent == "" {
			continue
		}
		child := ""
		matches := 0
		for _, cv := range conversations {
			if strings.HasSuffix(firstString(cv["native_id"]), "agent-"+agent) {
				child = roots[firstString(cv["id"])]
				matches++
			}
		}
		if matches == 1 && child != "" {
			aliasesChild[firstString(row["id"])] = child
		}
	}
	external := map[string]*delegationReference{}
	reference := func(id string) (*delegationReference, error) {
		if id == "" || conv[id] != nil {
			return nil, nil
		}
		if ref := external[id]; ref != nil {
			return ref, nil
		}
		var ref delegationReference
		err := tx.QueryRowContext(ctx, `SELECT c.id,c.workspace_id,COALESCE(c.agent_nickname,w.title,c.native_id),c.provider FROM conversations c JOIN workspaces w ON w.id=c.workspace_id WHERE c.id=?`, id).Scan(&ref.ConversationID, &ref.WorkspaceID, &ref.Label, &ref.Provider)
		if err == sql.ErrNoRows {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		external[id] = &ref
		return &ref, nil
	}
	for _, row := range sessions {
		if _, err = reference(firstString(row["child_conversation_id"])); err != nil {
			return nil, err
		}
	}
	for _, cv := range conversations {
		if _, err = reference(firstString(cv["parent_id"])); err != nil {
			return nil, err
		}
	}
	tokenFields := map[string]string{"tokens": "total_tokens", "input": "input_tokens", "uncached": "uncached_input_tokens", "cacheRead": "cache_read_input_tokens", "cacheWrite": "cache_creation_input_tokens", "output": "output_tokens", "reasoning": "reasoning_output_tokens"}
	for _, row := range sessions {
		id := firstString(row["id"])
		if child := aliasesChild[id]; child != "" && (firstString(row["usage_status"]) == "in-child-conversation" || integer(row["total_tokens"]) == 0) {
			s.Aliases[id] = child
			continue
		}
		cid := firstString(row["conversation_id"])
		cv := conv[cid]
		label := firstString(row["assignment"])
		if row["native_id"] == "main" {
			label = firstString(cv["agent_nickname"], label, cv["prompt"])
		}
		if label == "" {
			label = firstString(row["native_id"], cv["native_id"])
			if row["native_id"] == "main" {
				label = firstString(cv["native_id"], label)
			}
		}
		n := &delegationNode{ID: id, WorkspaceID: workspace, ConversationID: cid, Provider: firstString(row["provider"], cv["provider"]), Model: firstString(row["model"], cv["model"]), Label: label, Coverage: firstString(cv["coverage"]), Usage: firstString(row["usage_status"]), Start: delegationTime(row["started_at"]), End: delegationTime(row["ended_at"]), Self: map[string]delegationMetric{}, All: map[string]delegationMetric{}}
		n.Effort = firstString(row["effort"])
		// Embedded executions must not inherit the parent agent's effort.
		if row["native_id"] == "main" && n.Effort == "" {
			n.Effort = firstString(cv["effort"])
		}
		if ref := external[firstString(row["child_conversation_id"])]; ref != nil {
			copy := *ref
			n.ExternalChild = &copy
		}
		if n.Usage == "in-child-conversation" {
			n.Usage = "unavailable"
		}
		for _, m := range delegationMetrics {
			n.Self[m.ID] = delegationUnknown()
		}
		for key, field := range tokenFields {
			if n.Usage != "unavailable" {
				n.Self[key] = delegationNumber(float64(integer(row[field])), "reported", 1, 1)
				if n.Usage == "reported-partial" {
					v := n.Self[key]
					v.Quality = "partial"
					n.Self[key] = v
				}
			}
		}
		for _, key := range []string{"calls", "errors", "messages", "requests", "turns", "toolSum"} {
			n.Self[key] = delegationNumber(0, "observed", 0, 0)
		}
		s.Nodes = append(s.Nodes, n)
		s.ByID[id] = n
	}
	canonical := func(id string) string {
		if mapped := s.Aliases[id]; mapped != "" {
			return mapped
		}
		return id
	}
	// Source session parents, then transcript parents; exact launch aliases win.
	for _, n := range s.Nodes {
		row := raw[n.ID]
		parent := canonical(firstString(row["parent_id"]))
		proof := "session-parent"
		if parent == "" && n.ID == roots[n.ConversationID] {
			parent = canonical(roots[firstString(conv[n.ConversationID]["parent_id"])])
			if ref := external[firstString(conv[n.ConversationID]["parent_id"])]; ref != nil {
				n.ExternalParent = ref
			}
			proof = "conversation-parent"
		}
		if parent == "" && n.ExternalParent == nil {
			proof = ""
		}
		n.ParentID, n.ParentEvidence = parent, proof
		n.MessageID = firstString(row["delegation_message_id"])
	}
	for _, row := range sessions {
		id := firstString(row["id"])
		child := s.ByID[aliasesChild[id]]
		if child == nil {
			continue
		}
		parent := canonical(firstString(row["parent_id"]))
		if parent == "" {
			parent = canonical(roots[firstString(row["conversation_id"])])
		}
		if parent == child.ID {
			continue
		}
		child.ParentID = parent
		if s.ByID[parent] != nil {
			child.ExternalParent = nil
		}
		if assignment := firstString(row["assignment"]); assignment != "" {
			child.Label = assignment
		}
		child.MessageID = firstString(row["delegation_message_id"])
		child.ParentEvidence = "exact-delegation-handle"
		if s.Aliases[id] == "" {
			s.Warnings = append(s.Warnings, "A linked delegation retains independent usage; both source sessions are shown.")
		}
	}
	launches, err := queryMapsContext(ctx, tx, `SELECT l.child_id,l.parent_id,t.call_message_id FROM conversation_launches l JOIN conversations c ON c.id=l.child_id LEFT JOIN tool_calls t ON t.id=l.tool_call_id WHERE c.workspace_id=?`, workspace)
	if err != nil {
		return nil, err
	}
	for _, l := range launches {
		n := s.ByID[canonical(roots[firstString(l["child_id"])])]
		if n != nil && n.ParentID == "" {
			n.ParentID = canonical(roots[firstString(l["parent_id"])])
			n.ParentEvidence = "inferred-command-launch"
			n.MessageID = firstString(l["call_message_id"])
			if ref, refErr := reference(firstString(l["parent_id"])); refErr != nil {
				return nil, refErr
			} else if ref != nil {
				copy := *ref
				copy.MessageID = n.MessageID
				n.ExternalParent = &copy
			}
		}
	}
	for _, n := range s.Nodes {
		seen := map[string]bool{n.ID: true}
		for p := s.ByID[n.ParentID]; p != nil; p = s.ByID[p.ParentID] {
			if seen[p.ID] {
				n.ParentID = ""
				s.Warnings = append(s.Warnings, "A cyclic ownership link was excluded.")
				break
			}
			seen[p.ID] = true
		}
		if n.ParentID != "" && s.ByID[n.ParentID] == nil {
			s.Warnings = append(s.Warnings, "A parent outside this workspace is not included in its totals.")
		}
	}
	for _, n := range s.Nodes {
		if p := s.ByID[n.ParentID]; p != nil {
			p.children = append(p.children, n)
		} else {
			s.Roots = append(s.Roots, n)
		}
	}
	// Resolve launches before counting top-level conversations: a child stream
	// may omit its conversation parent but still have exact delegation evidence.
	var topLevel []*delegationNode
	for _, n := range s.Roots {
		if raw[n.ID]["native_id"] == "main" && n.ParentID == "" && n.ExternalParent == nil && firstString(conv[n.ConversationID]["parent_id"]) == "" {
			topLevel = append(topLevel, n)
		}
	}
	for _, n := range topLevel {
		n.Label = firstString(conv[n.ConversationID]["prompt"], n.Label)
		if len(topLevel) == 1 {
			n.Label = firstString(workspaceTitle, n.Label)
		}
	}
	owner := func(sid, cid string) *delegationNode {
		if n := s.ByID[canonical(sid)]; n != nil {
			return n
		}
		return s.ByID[canonical(roots[cid])]
	}
	bump := func(n *delegationNode, key string, value float64) {
		m := n.Self[key]
		if m.Value == nil {
			m = delegationNumber(0, "observed", 0, 0)
		}
		v := *m.Value + value
		m.Value = &v
		m.Known++
		m.Total++
		n.Self[key] = m
	}
	point := func(n *delegationNode, at *float64) {
		if at == nil {
			return
		}
		n.points = append(n.points, *at)
		if n.Start == nil || *at < *n.Start {
			v := *at
			n.Start = &v
		}
		if n.End == nil || *at > *n.End {
			v := *at
			n.End = &v
		}
	}
	// Only compact ledger fields are streamed; transcript bodies never enter the snapshot.
	tools, err := tx.QueryContext(ctx, `SELECT id,agent_session_id,conversation_id,call_message_id,tool_name,tool_category,command_category,status,started_at,ended_at,duration_ms,file_path,path_absolute,repo_path,path_repository_id FROM tool_calls WHERE workspace_id=?`, workspace)
	if err != nil {
		return nil, err
	}
	eventCount := 0
	for tools.Next() {
		eventCount++
		if eventCount > 500000 {
			tools.Close()
			return nil, fmt.Errorf("workspace exceeds the 500,000-event analysis limit")
		}
		var id, cid, name, cat, status string
		var sid, msg, command, start, end, file, absolute, repoPath, repoID sql.NullString
		var duration sql.NullFloat64
		if err = tools.Scan(&id, &sid, &cid, &msg, &name, &cat, &command, &status, &start, &end, &duration, &file, &absolute, &repoPath, &repoID); err != nil {
			tools.Close()
			return nil, err
		}
		n := owner(sid.String, cid)
		if n == nil {
			continue
		}
		t := delegationTool{ID: id, MessageID: msg.String, Name: name, Category: delegationCategory(name, cat, command.String), Status: status, Start: delegationTime(start.String), End: delegationTime(end.String)}
		if duration.Valid {
			v := duration.Float64
			t.Duration = &v
			bump(n, "toolSum", v)
		}
		category, _ := toolCategory(name, "tool_call")
		if (cat == "edit" || cat == "write" || category == "edit") && status != "error" {
			n.fileCalls++
			key := delegationFileKey(file.String, absolute.String, repoPath.String, repoID.String, cid)
			if status == "ok" && key != "" {
				if n.editedFiles == nil {
					n.editedFiles = map[string]bool{}
				}
				n.editedFiles[key] = true
				n.fileKnown++
				// The compact ledger currently retains only the first patch path.
				if lower := strings.ToLower(name); lower == "apply_patch" || lower == "applypatch" {
					n.filesPartial = true
				}
			}
		}
		n.tools = append(n.tools, t)
		bump(n, "calls", 1)
		if status == "error" {
			bump(n, "errors", 1)
		}
		point(n, t.Start)
		point(n, t.End)
	}
	err = tools.Err()
	tools.Close()
	if err != nil {
		return nil, err
	}
	requests, err := tx.QueryContext(ctx, `SELECT r.agent_session_id,r.conversation_id,r.requested_at,r.total_tokens FROM conversations c JOIN model_requests r ON r.conversation_id=c.id WHERE c.workspace_id=?`, workspace)
	if err != nil {
		return nil, err
	}
	requestTokens := map[string]float64{}
	for requests.Next() {
		eventCount++
		if eventCount > 500000 {
			requests.Close()
			return nil, fmt.Errorf("workspace exceeds the 500,000-event analysis limit")
		}
		var sid, at sql.NullString
		var cid string
		var tokens float64
		if err = requests.Scan(&sid, &cid, &at, &tokens); err != nil {
			requests.Close()
			return nil, err
		}
		if n := owner(sid.String, cid); n != nil {
			bump(n, "requests", 1)
			t := delegationTime(at.String)
			point(n, t)
			if t != nil {
				n.requests = append(n.requests, *t)
			}
			requestTokens[n.ID] += tokens
		}
	}
	err = requests.Err()
	requests.Close()
	if err != nil {
		return nil, err
	}
	counts, err := queryMapsContext(ctx, tx, delegationMessageCountsSQL, workspace, workspace)
	if err != nil {
		return nil, err
	}
	for _, row := range counts {
		if n := owner(firstString(row["agent_session_id"]), ""); n != nil {
			bump(n, "messages", float64(integer(row["messages"])))
			bump(n, "turns", float64(integer(row["turns"])))
			point(n, delegationTime(row["start"]))
			point(n, delegationTime(row["end"]))
		}
	}
	// Legacy transcripts may predate session-message membership. Only their root gets the fallback.
	for _, cv := range conversations {
		n := owner(roots[firstString(cv["id"])], firstString(cv["id"]))
		if n == nil || *n.Self["messages"].Value > 0 {
			continue
		}
		var count, turns int
		var start, end sql.NullString
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN role='user' AND kind='message' THEN 1 ELSE 0 END),0),MIN(created_at),MAX(created_at) FROM messages m WHERE conversation_id=? AND NOT EXISTS(SELECT 1 FROM agent_session_messages am WHERE am.message_id=m.id)`, cv["id"]).Scan(&count, &turns, &start, &end); err != nil {
			return nil, err
		}
		bump(n, "messages", float64(count))
		bump(n, "turns", float64(turns))
		point(n, delegationTime(start.String))
		point(n, delegationTime(end.String))
	}
	book, err := c.loadPriceBookFrom(ctx, tx)
	if err != nil {
		return nil, err
	}
	ledger, err := queryMapsContext(ctx, tx, `SELECT u.*,u.model usage_model,a.model session_model FROM agent_sessions a JOIN agent_session_usage u ON u.agent_session_id=a.id WHERE a.workspace_id=?`, workspace)
	if err != nil {
		return nil, err
	}
	priceStatus := map[string]string{}
	for _, row := range ledger {
		n := owner(firstString(row["agent_session_id"]), "")
		if n == nil {
			continue
		}
		day := firstString(row["usage_hour"])
		if len(day) > 10 {
			day = day[:10]
		}
		cost := book.cost(ledgerModel(row), day, ledgerTokens(row))
		priceStatus[n.ID] = worsePriceStatus(priceStatus[n.ID], cost.status)
		if cost.cost != nil {
			bump(n, "cost", *cost.cost)
		}
	}
	for _, n := range s.Nodes {
		if v := n.Self["cost"]; v.Value != nil {
			v.Quality = "estimated"
			if priceStatus[n.ID] == "partial" || priceStatus[n.ID] == "unpriced" {
				v.Quality = "partial"
			}
			n.Self["cost"] = v
		}
		if t := n.Self["tokens"]; t.Value != nil {
			n.Self["remainder"] = delegationNumber(max(0, *t.Value-requestTokens[n.ID]), t.Quality, 1, 1)
		}
		if n.Start != nil && n.End != nil {
			span := max(0, *n.End-*n.Start)
			n.Self["start"] = delegationNumber(*n.Start, "observed", 1, 1)
			n.Self["clock"] = delegationNumber(span, "observed", 1, 1)
			n.Self["total"] = n.Self["clock"]
			if s.Start == nil || *n.Start < *s.Start {
				v := *n.Start
				s.Start = &v
			}
			if s.End == nil || *n.End > *s.End {
				v := *n.End
				s.End = &v
			}
		}
		spans, known := []delegationSpan{}, 0
		for _, t := range n.tools {
			if t.Start != nil && t.End != nil && *t.End >= *t.Start {
				spans = append(spans, delegationSpan{*t.Start, *t.End})
				known++
			}
		}
		if known > 0 || len(n.tools) == 0 {
			quality := "observed"
			if known < len(n.tools) {
				quality = "partial"
			}
			n.Self["toolTime"] = delegationNumber(delegationUnion(spans), quality, known, len(n.tools))
			n.Self["toolAgent"] = n.Self["toolTime"]
		}
		v := n.Self["toolSum"]
		v.Total = len(n.tools)
		if v.Known < len(n.tools) {
			v.Quality = "partial"
			if v.Known == 0 {
				v.Value = nil
				v.Quality = "unknown"
			}
		}
		n.Self["toolSum"] = v
		sort.Float64s(n.points)
		for _, at := range n.points {
			if len(n.episodes) == 0 || at-n.episodes[len(n.episodes)-1].End > 30*60*1000 {
				n.episodes = append(n.episodes, delegationSpan{at, at})
			} else {
				n.episodes[len(n.episodes)-1].End = at
			}
		}
		if n.fileCalls == 0 || n.fileKnown > 0 {
			quality := "observed"
			if n.filesPartial || n.fileKnown < n.fileCalls {
				quality = "partial"
			}
			n.Self["files"] = delegationNumber(float64(len(n.editedFiles)), quality, n.fileKnown, n.fileCalls)
		} else {
			n.Self["files"] = delegationMetric{Quality: "unknown", Total: n.fileCalls}
		}
		n.Self["episodes"] = delegationNumber(float64(len(n.episodes)), "inferred", len(n.episodes), len(n.episodes))
		n.ChildCount = len(n.children)
		n.Self["children"] = delegationNumber(float64(n.ChildCount), "observed", 1, 1)
		leaf := 0.0
		if n.ChildCount == 0 {
			leaf = 1
		}
		n.Self["leaves"] = delegationNumber(leaf, "observed", 1, 1)
	}
	less := func(a, b *delegationNode) bool {
		if a.Start == nil {
			return b.Start == nil && a.ID < b.ID
		}
		if b.Start == nil {
			return true
		}
		if *a.Start != *b.Start {
			return *a.Start < *b.Start
		}
		return a.ID < b.ID
	}
	ordered := []*delegationNode{}
	var walk func([]*delegationNode, int, string)
	walk = func(nodes []*delegationNode, depth int, prefix string) {
		sort.Slice(nodes, func(i, j int) bool { return less(nodes[i], nodes[j]) })
		for i, n := range nodes {
			n.Depth = depth
			n.Outline = fmt.Sprintf("%s%d", prefix, i+1)
			n.Order = len(ordered)
			n.Self["depth"] = delegationNumber(float64(depth), "observed", 1, 1)
			ordered = append(ordered, n)
			walk(n.children, depth+1, n.Outline+".")
			n.All = delegationAggregate(append([]*delegationNode{n}, delegationDescendants(n)...))
			n.SubtreeStart, n.SubtreeEnd = n.Start, n.End
			for _, child := range n.children {
				if child.SubtreeStart != nil && (n.SubtreeStart == nil || *child.SubtreeStart < *n.SubtreeStart) {
					n.SubtreeStart = child.SubtreeStart
				}
				if child.SubtreeEnd != nil && (n.SubtreeEnd == nil || *child.SubtreeEnd > *n.SubtreeEnd) {
					n.SubtreeEnd = child.SubtreeEnd
				}
			}
		}
	}
	walk(s.Roots, 0, "")
	s.Nodes = ordered
	s.Totals = delegationAggregate(s.Nodes)
	s.Totals["children"] = delegationNumber(float64(len(s.Nodes)-len(s.Roots)), "observed", len(s.Nodes), len(s.Nodes))
	for _, n := range s.Nodes {
		for _, m := range delegationMetrics {
			for scope, values := range map[string]map[string]delegationMetric{"self": n.Self, "all": n.All} {
				if v := values[m.ID].Value; v != nil {
					s.Maxima[scope][m.ID] = max(s.Maxima[scope][m.ID], *v)
				}
			}
		}
	}
	if s.Start != nil && s.End != nil {
		width := max(1, *s.End-*s.Start)
		for _, n := range s.Nodes {
			for _, at := range append(append([]float64{}, n.requests...), delegationToolPoints(n.tools)...) {
				index := min(119, max(0, int((at-*s.Start)/width*120)))
				s.Density[index]++
			}
		}
	}
	// A content hash rejects continuations after sync or a different filter/sort.
	hashInput := []any{s.Nodes, s.Aliases}
	for _, n := range s.Nodes {
		hashInput = append(hashInput, n.tools, n.requests)
	}
	payload, _ := json.Marshal(hashInput)
	hash := sha256.Sum256(payload)
	s.ID = fmt.Sprintf("%x", hash[:12])
	return s, nil
}

// Prefer repository identity so the same logical file across checkouts counts
// once. Otherwise retain absolute identity; unresolved relative paths are scoped
// to their conversation to avoid merging unrelated working directories.
func delegationFileKey(file, absolute, repoPath, repoID, conversation string) string {
	if file == "" || len(file) >= 500 {
		return ""
	}
	if repoID != "" && repoPath != "" {
		return "repo:" + repoID + ":" + filepath.Clean(repoPath)
	}
	if absolute != "" {
		return "absolute:" + filepath.Clean(absolute)
	}
	if filepath.IsAbs(file) {
		return "absolute:" + filepath.Clean(file)
	}
	return "conversation:" + conversation + ":" + filepath.Clean(file)
}

func delegationToolPoints(tools []delegationTool) []float64 {
	p := []float64{}
	for _, t := range tools {
		if t.Start != nil {
			p = append(p, *t.Start)
		}
	}
	return p
}
func delegationDescendants(n *delegationNode) []*delegationNode {
	out := []*delegationNode{}
	for _, c := range n.children {
		out = append(out, c)
		out = append(out, delegationDescendants(c)...)
	}
	return out
}
func delegationAggregate(nodes []*delegationNode) map[string]delegationMetric {
	result := map[string]delegationMetric{}
	for _, spec := range delegationMetrics {
		value, known := 0.0, 0
		quality := "observed"
		for _, n := range nodes {
			m := n.Self[spec.ID]
			if m.Value == nil {
				continue
			}
			known++
			switch spec.ID {
			case "start":
				if known == 1 || *m.Value < value {
					value = *m.Value
				}
			case "depth":
				value = max(value, *m.Value)
			default:
				value += *m.Value
			}
			if m.Quality == "partial" {
				quality = "partial"
			} else if m.Quality != "observed" && quality != "partial" {
				quality = m.Quality
			}
		}
		if known == 0 {
			result[spec.ID] = delegationUnknown()
			continue
		}
		if known < len(nodes) {
			quality = "partial"
		}
		switch spec.ID {
		case "files":
			files := map[string]bool{}
			for _, n := range nodes {
				for key := range n.editedFiles {
					files[key] = true
				}
			}
			value = float64(len(files))
		case "clock":
			var start, end *float64
			for _, n := range nodes {
				if n.Start != nil && (start == nil || *n.Start < *start) {
					start = n.Start
				}
				if n.End != nil && (end == nil || *n.End > *end) {
					end = n.End
				}
			}
			if start != nil && end != nil {
				value = *end - *start
			}
		case "toolTime":
			spans := []delegationSpan{}
			for _, n := range nodes {
				for _, t := range n.tools {
					if t.Start != nil && t.End != nil && *t.End >= *t.Start {
						spans = append(spans, delegationSpan{*t.Start, *t.End})
					}
				}
			}
			value = delegationUnion(spans)
		case "children":
			value = float64(max(0, len(nodes)-1))
		}
		result[spec.ID] = delegationNumber(value, quality, known, len(nodes))
	}
	return result
}
func delegationCategory(name, cat, command string) string {
	name = strings.ToLower(name)
	cat = strings.ToLower(cat)
	command = strings.ToLower(command)
	switch {
	case name == "wait_agent" || name == "wait" || name == "taskoutput":
		return "wait"
	case strings.Contains(name, "sleep") || strings.Contains(name, "poll") || name == "write_stdin":
		return "poll"
	case cat == "agent" || strings.Contains(name, "delegate") || strings.Contains(name, "spawn") || name == "task":
		return "delegate"
	case cat == "read" || cat == "search" || strings.Contains(name, "read") || strings.Contains(name, "search") || strings.Contains(name, "glob"):
		return "read"
	case cat == "edit" || cat == "write" || strings.Contains(name, "patch") || strings.Contains(name, "write") || name == "edit":
		return "write"
	case strings.Contains(command, "git") || strings.Contains(name, "git"):
		return "git"
	case cat == "network" || strings.Contains(name, "web") || strings.Contains(name, "fetch"):
		return "network"
	case cat == "shell" || cat == "command" || strings.Contains(name, "exec") || name == "bash" || strings.Contains(name, "terminal"):
		return "run"
	default:
		return "other"
	}
}

type delegationCursor struct {
	Snapshot string `json:"s"`
	Query    string `json:"q"`
	Offset   int    `json:"o"`
}

func (s *Server) getDelegation(w http.ResponseWriter, r *http.Request, workspace string, timeline bool) {
	snapshot, err := s.Catalog.delegationSnapshot(r.Context(), workspace)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	if snapshot == nil {
		writeJSON(w, map[string]any{"error": "workspace not found"}, 404)
		return
	}
	q := r.URL.Query()
	if wanted := q.Get("snapshot"); wanted != "" && wanted != snapshot.ID {
		writeJSON(w, map[string]any{"error": "Archive changed; refresh the explorer.", "code": "snapshot_changed"}, 409)
		return
	}
	if timeline {
		s.writeDelegationTimeline(w, r, snapshot)
		return
	}
	sortKey, scope, mode := q.Get("sort"), q.Get("scope"), q.Get("mode")
	if sortKey == "" {
		sortKey = "start"
	}
	if scope != "all" {
		scope = "self"
	}
	if mode != "rank" {
		mode = "hierarchy"
	}
	valid := false
	for _, spec := range delegationMetrics {
		if sortKey == spec.ID {
			valid = true
		}
	}
	if !valid {
		writeJSON(w, map[string]any{"error": "unknown metric"}, 400)
		return
	}
	search := strings.ToLower(strings.TrimSpace(q.Get("search")))
	provider := q.Get("provider")
	collapsed := map[string]bool{}
	for _, id := range strings.Split(q.Get("collapsed"), ",") {
		collapsed[id] = true
	}
	nodes := []*delegationNode{}
	matched := map[string]bool{}
	for _, n := range snapshot.Nodes {
		if (provider == "" || n.Provider == provider) && (search == "" || strings.Contains(strings.ToLower(n.Label+" "+n.Provider+" "+n.Model), search)) {
			matched[n.ID] = true
		}
	}
	visible := map[string]bool{}
	for id := range matched {
		visible[id] = true
		if mode == "hierarchy" {
			for p := snapshot.ByID[snapshot.ByID[id].ParentID]; p != nil; p = snapshot.ByID[p.ParentID] {
				visible[p.ID] = true
			}
		}
	}
	for _, n := range snapshot.Nodes {
		if !visible[n.ID] {
			continue
		}
		hidden := false
		if mode == "hierarchy" {
			for p := snapshot.ByID[n.ParentID]; p != nil; p = snapshot.ByID[p.ParentID] {
				if collapsed[p.ID] {
					hidden = true
					break
				}
			}
		}
		if !hidden {
			nodes = append(nodes, n)
		}
	}
	totals := snapshot.Totals
	if q.Get("only_matching") == "1" {
		included := []*delegationNode{}
		for _, n := range snapshot.Nodes {
			if matched[n.ID] {
				included = append(included, n)
			}
		}
		totals = delegationAggregate(included)
		childCount := 0
		for _, n := range included {
			if snapshot.ByID[n.ParentID] != nil {
				childCount++
			}
		}
		totals["children"] = delegationNumber(float64(childCount), "observed", 1, 1)
		for i, n := range nodes {
			copy := *n
			copy.All = map[string]delegationMetric{}
			subtree := []*delegationNode{}
			for _, child := range append([]*delegationNode{n}, delegationDescendants(n)...) {
				if matched[child.ID] {
					subtree = append(subtree, child)
				}
			}
			copy.All = delegationAggregate(subtree)
			childCount := 0
			for _, child := range delegationDescendants(n) {
				if matched[child.ID] {
					childCount++
				}
			}
			copy.All["children"] = delegationNumber(float64(childCount), "observed", 1, 1)
			if !matched[n.ID] {
				copy.Self = map[string]delegationMetric{}
				for _, spec := range delegationMetrics {
					copy.Self[spec.ID] = delegationMetric{Quality: "excluded"}
				}
			}
			nodes[i] = &copy
		}
	}
	if mode != "hierarchy" {
		sort.SliceStable(nodes, func(i, j int) bool {
			a, b := nodes[i], nodes[j]
			av, bv := a.Self[sortKey].Value, b.Self[sortKey].Value
			if scope == "all" {
				av, bv = a.All[sortKey].Value, b.All[sortKey].Value
			}
			if av == nil {
				return false
			}
			if bv == nil {
				return true
			}
			if *av == *bv {
				return a.Order < b.Order
			}
			if q.Get("direction") == "asc" {
				return *av < *bv
			}
			return *av > *bv
		})
	}
	query := strings.Join([]string{mode, sortKey, scope, q.Get("direction"), search, provider, q.Get("collapsed"), q.Get("only_matching")}, "|")
	offset := 0
	if cursor := q.Get("cursor"); cursor != "" {
		var cur delegationCursor
		bytes, decodeErr := base64.RawURLEncoding.DecodeString(cursor)
		if decodeErr != nil || json.Unmarshal(bytes, &cur) != nil || cur.Offset < 0 {
			writeJSON(w, map[string]any{"error": "invalid cursor"}, 400)
			return
		}
		if cur.Snapshot != snapshot.ID || cur.Query != query {
			writeJSON(w, map[string]any{"error": "Archive or query changed; restart pagination.", "code": "snapshot_changed"}, 409)
			return
		}
		offset = cur.Offset
	}
	limit := min(100, max(1, queryInt(r, "limit", 40)))
	offset = min(offset, len(nodes))
	end := min(len(nodes), offset+limit)
	page := nodes[offset:end]
	next := ""
	if end < len(nodes) {
		bytes, _ := json.Marshal(delegationCursor{snapshot.ID, query, end})
		next = base64.RawURLEncoding.EncodeToString(bytes)
	}
	displayNodes := map[string]*delegationNode{}
	for _, n := range nodes {
		displayNodes[n.ID] = n
	}
	ancestors := []*delegationNode{}
	ancestorsTruncated := false
	seen := map[string]bool{}
	for _, n := range page {
		seen[n.ID] = true
	}
	for _, n := range page {
		for p := snapshot.ByID[n.ParentID]; p != nil; p = snapshot.ByID[p.ParentID] {
			if !seen[p.ID] {
				seen[p.ID] = true
				if len(ancestors) >= 100 {
					ancestorsTruncated = true
					continue
				}
				if display := displayNodes[p.ID]; display != nil {
					ancestors = append(ancestors, display)
				} else {
					ancestors = append(ancestors, p)
				}
			}
		}
	}
	sort.Slice(ancestors, func(i, j int) bool { return ancestors[i].Order < ancestors[j].Order })
	focus := []*delegationNode{}
	focusSeen := map[string]bool{}
	focusTruncated := false
	addFocus := func(n *delegationNode) {
		if n != nil && !focusSeen[n.ID] && len(focus) >= 100 {
			focusTruncated = true
		}
		if n != nil && !focusSeen[n.ID] && len(focus) < 100 {
			focusSeen[n.ID] = true
			focus = append(focus, n)
		}
	}
	for _, n := range snapshot.Nodes {
		if n.ConversationID == q.Get("conversation") {
			addFocus(n)
			addFocus(snapshot.ByID[n.ParentID])
			for _, child := range n.children {
				addFocus(child)
			}
		}
	}
	pageMatches := map[string]bool{}
	for _, n := range append(append([]*delegationNode{}, page...), ancestors...) {
		pageMatches[n.ID] = matched[n.ID]
	}
	writeJSON(w, map[string]any{"focus": focus, "focus_truncated": focusTruncated, "snapshot": snapshot.ID, "rows": page, "ancestors": ancestors, "ancestors_truncated": ancestorsTruncated, "matched": pageMatches, "total": len(nodes), "session_count": len(snapshot.Nodes), "offset": offset, "next_cursor": next, "metrics": delegationMetrics, "maxima": snapshot.Maxima, "totals": totals, "start": snapshot.Start, "end": snapshot.End, "density": snapshot.Density, "warnings": snapshot.Warnings}, 200)
}

type delegationMark struct {
	Start     float64 `json:"start"`
	End       float64 `json:"end"`
	EndKnown  bool    `json:"end_known"`
	Category  string  `json:"category"`
	Count     int     `json:"count"`
	Error     bool    `json:"error"`
	ToolID    string  `json:"tool_id,omitempty"`
	MessageID string  `json:"message_id,omitempty"`
	Name      string  `json:"name"`
}

func (s *Server) writeDelegationTimeline(w http.ResponseWriter, r *http.Request, snapshot *delegationSnapshot) {
	q := r.URL.Query()
	start, end := 0.0, 0.0
	if snapshot.Start != nil {
		start = *snapshot.Start
	}
	if snapshot.End != nil {
		end = *snapshot.End
	}
	// A single retained instant still has a valid, one-millisecond viewport.
	fullStart, fullEnd := start, max(start+1, end)
	end = fullEnd
	for key, target := range map[string]*float64{"start": &start, "end": &end} {
		if text := q.Get(key); text != "" {
			v, err := strconv.ParseFloat(text, 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				writeJSON(w, map[string]any{"error": "invalid timeline bounds"}, 400)
				return
			}
			*target = v
		}
	}
	if end <= start {
		writeJSON(w, map[string]any{"error": "timeline end must follow start"}, 400)
		return
	}
	if end <= fullStart || start >= fullEnd {
		writeJSON(w, map[string]any{"error": "timeline window is outside the workspace evidence range"}, 400)
		return
	}
	start, end = max(start, fullStart), min(end, fullEnd)
	ids := strings.Split(q.Get("sessions"), ",")
	if len(ids) > 100 {
		writeJSON(w, map[string]any{"error": "at most 100 timeline lanes per request"}, 400)
		return
	}
	lanes := []map[string]any{}
	width := min(1600, max(100, queryInt(r, "width", 900)))
	bucket := (end - start) / float64(width/4)
	for _, id := range ids {
		n := snapshot.ByID[id]
		if n == nil {
			continue
		}
		groups := map[string]*delegationMark{}
		marks := []delegationMark{}
		knots := []float64{}
		for _, at := range n.requests {
			if at >= start-90000 && at <= end+90000 {
				knots = append(knots, at)
			}
		}
		for _, t := range n.tools {
			if t.Start == nil {
				continue
			}
			stop := *t.Start
			if t.End != nil && *t.End >= *t.Start {
				stop = *t.End
			}
			if stop < start || *t.Start > end {
				continue
			}
			knots = append(knots, *t.Start, stop)
			mark := delegationMark{Start: *t.Start, End: stop, EndKnown: t.End != nil && *t.End >= *t.Start, Category: t.Category, Count: 1, Error: t.Status == "error", ToolID: t.ID, MessageID: t.MessageID, Name: t.Name}
			// Small neighboring calls share a mark. Errors retain their own reference.
			if stop-*t.Start < bucket && len(marks) < 2000 && !mark.Error {
				key := fmt.Sprintf("%s:%d", t.Category, int((*t.Start-start)/bucket))
				if existing := groups[key]; existing != nil {
					existing.End = max(existing.End, stop)
					existing.EndKnown = existing.EndKnown && mark.EndKnown
					if *t.Start < existing.Start {
						existing.Start = *t.Start
						existing.ToolID = t.ID
						existing.MessageID = t.MessageID
						existing.Name = t.Name
					}
					existing.Count++
					continue
				}
				copy := mark
				groups[key] = &copy
			} else {
				marks = append(marks, mark)
			}
		}
		for _, mark := range groups {
			marks = append(marks, *mark)
		}
		sort.Slice(marks, func(i, j int) bool { return marks[i].Start < marks[j].Start })
		truncated := len(marks) > 2400
		if truncated {
			marks = marks[:2400]
		}
		sort.Float64s(knots)
		phases := []delegationSpan{}
		for i := 1; i < len(knots); i++ {
			a, b := max(start, knots[i-1]), min(end, knots[i])
			if b <= a || b-a > 90000 {
				continue
			}
			if len(phases) > 0 && a <= phases[len(phases)-1].End {
				phases[len(phases)-1].End = b
			} else {
				phases = append(phases, delegationSpan{a, b})
			}
		}
		lanes = append(lanes, map[string]any{"session": n.ID, "marks": marks, "model_inferred": phases, "episodes": n.episodes, "truncated": truncated})
	}
	writeJSON(w, map[string]any{"snapshot": snapshot.ID, "start": start, "end": end, "lanes": lanes, "inference": "Model/orchestration between nearby retained observations (at most 90 seconds); not measured processing or streaming. Blank time is unclassified."}, 200)
}
