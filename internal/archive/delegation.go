package archive

// The delegation read model sits above the existing transcript/session ledgers.
// It projects the shared execution resolution onto one workspace without
// rewriting sources, and never loads transcript bodies. Cached snapshots are
// immutable.
import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/http"
	"path/filepath"
	"slices"
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
	{"start", "First evidence", "date", "Earliest saved timestamp; the agent may have started earlier."},
	{"tokens", "Tokens", "tokens", "Self shows usage attributed to this agent session. ≥ marks a reported minimum. Self + children includes unallocated branch usage and reconciles overlapping observations; cached input is included."},
	{"cost", "API cost", "usd", "API-rate estimate on the date of use, not billing. Partial values are lower bounds."},
	{"calls", "Tool calls", "count", "Distinct retained tool invocations."}, {"errors", "Errors", "count", "Reported tool failures; not the outcome of the agent."},
	{"events", "Agent events", "count", "Retained agent events at the display probability threshold, attributed through session message membership. Unprocessed messages may contain additional events."},
	{"files", "Files Edited", "count", "Distinct file paths recorded by successful edit/write tools, including adds/deletes. Repeated edits count once; Self + children deduplicates across sessions. Shell edits and extra paths in multi-file patches may be unavailable."},
	{"filesRead", "Files read", "count", "Distinct file paths recorded by successful read tools. Repeated reads count once; Self + children deduplicates across sessions. Reads without retained paths are incomplete; search/list tools do not count as file reads."},
	{"messages", "Messages", "count", "Messages recorded in the agent session."}, {"requests", "Model requests", "count", "Distinct recorded model requests."}, {"turns", "User turns", "count", "Retained user message starts; injected agent assignments do not count as user turns."},
	{"clock", "Clock span", "ms", "Earliest to latest evidence across included sessions, including unclassified time."},
	{"total", "Agent session spans", "ms", "Sum of the time from each agent session’s first to last recorded activity. Overlapping sessions and gaps both add to this total."},
	{"toolTime", "Tool elapsed", "ms", "Clock time covered by tool calls across included agent sessions. Overlap counts once; this does not measure CPU time."},
	{"toolAgent", "Agent tool time", "ms", "Sum of each agent session’s tool time, counting overlap once within each session. Parallel sessions add to the total."},
	{"toolSum", "Tool duration sum", "ms", "Sum of recorded call durations; overlapping calls accumulate."},
	{"wait", "Blocking wait", "ms", "Requires explicit blocking boundaries. Launch acknowledgements and polling are excluded."},
	{"modelTime", "Model processing", "ms", "Requires measured model start/end boundaries. Inferred timeline backgrounds are excluded."},
	{"backgroundTime", "Background process", "ms", "Requires process lifecycle boundaries. A background launch is insufficient."},
	{"episodes", "Activity periods", "count", "Groups of tool calls, model requests and first/last message timestamps separated by more than 30 minutes. These are estimated activity periods; they do not establish when an agent finished."},
	{"children", "Children", "count", "Self counts directly delegated agent sessions; Self + children counts all delegated descendants once."},
	{"leaves", "Agents without children", "count", "Included agent sessions with no known delegated children."}, {"depth", "Depth", "count", "Greatest number of delegation levels below the main agent among included agent sessions."},
	{"input", "Input", "tokens", "Input including cached input."}, {"uncached", "Uncached input", "tokens", "Input excluding cache read and creation."},
	{"cacheRead", "Cache read", "tokens", "Subset of input."}, {"cacheWrite", "Cache creation", "tokens", "Subset of input."},
	{"output", "Output", "tokens", "Output including reported reasoning."}, {"reasoning", "Reasoning", "tokens", "Subset of output."},
	{"remainder", "Unallocated usage", "tokens", "Aggregate usage recorded here whose consuming agent session is unknown within this branch. It is included once in Self + children, and may overlap reported child minima."},
}

type delegationReference struct {
	ConversationID string `json:"conversation_id"`
	WorkspaceID    string `json:"workspace_id"`
	Label          string `json:"label"`
	Provider       string `json:"provider"`
	MessageID      string `json:"message_id,omitempty"`
}

// delegationSegment is one transcript of an execution, where it is retained.
type delegationSegment struct {
	ConversationID string `json:"conversation_id"`
	WorkspaceID    string `json:"workspace_id"`
}
type delegationAttempt struct {
	ID        string `json:"id"`
	Outcome   string `json:"outcome"`
	MessageID string `json:"message_id,omitempty"`
}
type delegationNode struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	WorkspaceID    string `json:"workspace_id"`
	ParentID       string `json:"parent_id,omitempty"`
	ParentEvidence string `json:"parent_evidence,omitempty"`
	// ParentCertainty is the winning parent claim's provenance: native,
	// derived, or legacy (a stored link of unknown provenance, not verified).
	ParentCertainty string               `json:"parent_certainty,omitempty"`
	ExternalParent  *delegationReference `json:"external_parent,omitempty"`
	ExternalChild   *delegationReference `json:"external_child,omitempty"`
	Navigation      *delegationReference `json:"navigation,omitempty"`
	ContextOnly     bool                 `json:"context_only,omitempty"`
	MessageID       string               `json:"message_id,omitempty"`
	// Label is the execution's one name in every reader and projection
	// (delegationProjection.label). Subtitle is local context, such as the
	// workspace title, that never replaces it.
	Label       string `json:"label"`
	LabelSource string `json:"label_source"`
	NativeID    string `json:"native_id,omitempty"`
	Subtitle    string `json:"subtitle,omitempty"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Effort      string `json:"effort"`
	Coverage    string `json:"coverage"`
	Usage       string `json:"usage_status"`
	// Depth is layout indentation within this snapshot. Ancestry carries the
	// semantic parent state; its depth is absent unless the chain reaches a
	// known root. AncestorIDs are the resolved chain, nearest first.
	Depth       int                `json:"depth"`
	Ancestry    delegationAncestry `json:"ancestry"`
	AncestorIDs []string           `json:"ancestor_ids,omitempty"`
	// Segments are the execution's transcripts in segment order when it has
	// more than one (continuations); each stays navigable.
	Segments []delegationSegment `json:"segments,omitempty"`
	// Attempts are launches this execution issued that created no confirmed
	// execution (rejected, or with no outcome). They are not child rows.
	Attempts []delegationAttempt `json:"attempts,omitempty"`
	// OccurrencePartial names why copied history may still be counted in
	// this execution's own activity (occurrenceIndex.Partial); its
	// observation metrics are then partial.
	OccurrencePartial string `json:"occurrence_partial,omitempty"`
	// EventsPending: some of its agent events are withheld (built on
	// superseded execution evidence) or awaiting a rebuild.
	EventsPending bool `json:"events_pending,omitempty"`
	// ChildrenOmitted counts this row's children the snapshot leaves out at
	// the family bound; its navigation opens a reader that includes them.
	ChildrenOmitted   int                      `json:"children_omitted,omitempty"`
	Order             int                      `json:"order"`
	ChildCount        int                      `json:"child_count"`
	Start             *float64                 `json:"start"`
	End               *float64                 `json:"end"`
	SubtreeStart      *float64                 `json:"subtree_start"`
	SubtreeEnd        *float64                 `json:"subtree_end"`
	TokenAttribution  *sessionTokenAttribution `json:"token_attribution,omitempty"`
	Peer              *delegationPeer          `json:"peer,omitempty"` // related work's session (delegation_peers.go)
	accountedTokens   map[string]delegationMetric
	unallocatedTokens tokenCounts
	unallocatedCost   float64
	Self              map[string]delegationMetric `json:"self"`
	All               map[string]delegationMetric `json:"all"`
	children          []*delegationNode
	execution         executionID
	tools             []delegationTool
	requests          []float64
	points            []float64
	// The streams' recorded envelope (session or conversation start and
	// end), which spans copied history too. It is the span only when no
	// timed observation of the streams exists to say otherwise.
	envelopeStart, envelopeEnd *float64
	// timed: some timed observation (own or inherited) of the streams exists.
	timed        bool
	episodes     []delegationSpan
	editedFiles  map[string]bool
	fileCalls    int
	fileKnown    int
	filesPartial bool
	readFiles    map[string]bool
	readCalls    int
	readKnown    int
}
type delegationAncestry struct {
	State string `json:"state"` // root, resolved, ambiguous, missing, cycle or conflict
	Depth *int   `json:"depth,omitempty"`
	// The resolver's ancestry quality (executionAncestry), never a local
	// policy. Certainty is CertaintyName: native, derived, legacy, synthesized
	// or inferred. DepthQuality and RootQuality are observed, inferred or
	// unverified ("" when unknown). Conflicted: the execution or an ancestor
	// is in an unresolved identity conflict.
	Certainty    string `json:"certainty,omitempty"`
	DepthQuality string `json:"depth_quality,omitempty"`
	RootQuality  string `json:"root_quality,omitempty"`
	Conflicted   bool   `json:"conflicted,omitempty"`
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
	ID       string
	Revision executionRevision
	// FamilyComplete is false when a family exceeded delegationFamilyLimit.
	FamilyComplete bool
	// Coverage is what the resolution could not establish for these rows.
	Coverage   delegationCoverage
	projection delegationProjection
	// members are the source rows each execution row combines.
	members map[string][]string
	Nodes   []*delegationNode
	ByID    map[string]*delegationNode
	Aliases map[string]string
	Roots   []*delegationNode
	Maxima  map[string]map[string]float64
	Totals  map[string]delegationMetric
	// GroupTotals also counts related work; nil without any.
	GroupTotals map[string]delegationMetric
	Start, End  *float64
	Density     []int
	Warnings    []string
}

// delegationCoverage counts the snapshot's rows whose ancestry or accounting
// the evidence leaves uncertain, and the evidence capabilities the library
// lacks, so uncertain answers are never shown as confirmed.
type delegationCoverage struct {
	Missing           []string `json:"missing"`
	UnresolvedParents int      `json:"unresolved_parents"`
	UnverifiedParents int      `json:"unverified_parents"`
	Conflicts         int      `json:"conflicts"`
	Unqueried         int      `json:"unqueried"`
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

// errDelegationSnapshotChanged: the execution evidence a cached snapshot was
// resolved from has changed since. Its lanes are not combined with newer
// rows; the client refreshes the explorer instead.
// delegationAnalysisLimit bounds the rows one explorer snapshot holds; family
// context never takes it past this (a package variable so tests can lower it).
var delegationAnalysisLimit = 10000

func errDelegationTooLarge() error {
	return fmt.Errorf("workspace exceeds the %s-agent-session analysis limit", thousands(delegationAnalysisLimit))
}

var errDelegationSnapshotChanged = errors.New("execution evidence changed since the explorer snapshot")

func (c *Catalog) delegationSnapshot(ctx context.Context, workspace string) (*delegationSnapshot, error) {
	return c.delegationSnapshotWith(ctx, workspace, nil)
}

// delegationSnapshotWith also loads the relatives that the family bound left
// out below the given rows (the explorer's delegation_relatives).
func (c *Catalog) delegationSnapshotWith(ctx context.Context, workspace string, relatives []string) (*delegationSnapshot, error) {
	relatives = slices.Compact(slices.Sorted(slices.Values(relatives)))
	// The key names the execution source generation as well as the derived
	// cache's version: typed evidence can change without advancing the latter,
	// and a refresh after a snapshot_changed answer must see the new graph.
	key := "delegation:" + workspace
	if generation, tracked, err := executionSourceGeneration(ctx, c.reader()); err != nil {
		return nil, err
	} else if tracked {
		key += "@" + strconv.FormatInt(generation, 10)
	}
	if len(relatives) > 0 {
		key += "+" + strings.Join(relatives, ",")
	}
	return cachedValue(ctx, c, key, func(ctx context.Context) (*delegationSnapshot, error) {
		return c.readDelegationSnapshotWith(ctx, workspace, relatives)
	})
}

// Use the retained launch's assignment in both outgoing rows and incoming
// ancestor context, so opening a separate child workspace keeps its label.
const delegationAssignmentCandidatesSQL = `CASE WHEN json_valid(m.text) THEN
 (SELECT json_group_array(substr(value,1,180)) FROM (SELECT value FROM json_each(m.text,'$.input')
  WHERE type='text' AND key IN ('task_name','description','prompt','Prompt','task','message')
   AND trim(value,char(9)||char(10)||char(13)||' ')<>'' AND value NOT GLOB 'gAAAA*'
  ORDER BY CASE key WHEN 'task_name' THEN 0 WHEN 'description' THEN 1 WHEN 'prompt' THEN 2 WHEN 'Prompt' THEN 3 WHEN 'task' THEN 4 ELSE 5 END
 ))
 ELSE json_array(substr(m.text,1,180)) END`

// Validate each field before choosing one: an internal task path or UUID
// must not hide readable prose in the launch's other retained fields.
func delegationAssignment(candidates string) string {
	var texts []string
	if json.Unmarshal([]byte(candidates), &texts) != nil {
		return ""
	}
	for _, text := range texts {
		if meaningfulWorkspaceTitle(text, "") {
			return text
		}
	}
	return ""
}

// Copy only the small accounting fields before joining session membership.
// Looking up every message by its hashed ID makes long transcripts perform
// thousands of random reads of the wide message table on external libraries.
// Rows stay per message so copied history can be left out of each count.
const delegationMessageFactsSQL = `WITH message_facts AS MATERIALIZED (
 SELECT m.id,m.conversation_id,m.role,m.kind,m.created_at FROM conversations c
 JOIN messages m ON m.conversation_id=c.id WHERE c.workspace_id=?
)
SELECT am.agent_session_id,m.id,m.conversation_id,m.role='user' AND m.kind='message' turn,m.created_at
 FROM agent_sessions a JOIN agent_session_messages am ON am.agent_session_id=a.id
 JOIN message_facts m ON m.id=am.message_id WHERE a.workspace_id=?`

func (c *Catalog) readDelegationSnapshot(ctx context.Context, workspace string) (*delegationSnapshot, error) {
	return c.readDelegationSnapshotWith(ctx, workspace, nil)
}

func (c *Catalog) readDelegationSnapshotWith(ctx context.Context, workspace string, relatives []string) (*delegationSnapshot, error) {
	tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
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
	conversations, err := queryMapsContext(ctx, tx, `SELECT id,provider,account,model,effort,native_id,parent_id,agent_path,agent_nickname,coverage,started_at,ended_at,
 (SELECT substr(d.initiation,1,180) FROM conversation_documents d WHERE d.conversation_id=c.id) prompt
 FROM conversations c WHERE workspace_id=? ORDER BY started_at,id LIMIT ?`, workspace, delegationAnalysisLimit+1)
	if err != nil {
		return nil, err
	}
	// Providers normalize launch arguments into input, but use different task
	// fields. Codex can retain a readable task_name beside an encrypted message.
	// Extract before clipping: the name may follow a very large message. Unknown
	// structured payloads and opaque messages must not become display labels.
	sessions, err := queryMapsContext(ctx, tx, `SELECT a.*,`+delegationAssignmentCandidatesSQL+` assignment_candidates
 FROM agent_sessions a LEFT JOIN messages m ON m.id=a.delegation_message_id
 WHERE a.workspace_id=? ORDER BY a.started_at,a.id LIMIT ?`, workspace, delegationAnalysisLimit+1)
	if err != nil {
		return nil, err
	}
	for _, session := range sessions {
		session["assignment"] = delegationAssignment(firstString(session["assignment_candidates"]))
	}
	if len(sessions) > delegationAnalysisLimit || len(conversations) > delegationAnalysisLimit {
		return nil, errDelegationTooLarge()
	}
	s := &delegationSnapshot{ByID: map[string]*delegationNode{}, Aliases: map[string]string{}, Maxima: map[string]map[string]float64{"self": {}, "all": {}}, Density: make([]int, 120), Warnings: []string{}}
	res, err := c.executionResolutionAt(ctx, tx)
	if err != nil {
		return nil, err
	}
	p := delegationProjection{res: res}
	s.projection, s.Revision, s.members = p, res.Revision, map[string][]string{}
	conv := map[string]map[string]any{}
	roots := map[string]string{}
	for _, cv := range conversations {
		conv[firstString(cv["id"])] = cv
	}
	for _, row := range sessions {
		if row["native_id"] == "main" {
			roots[firstString(row["conversation_id"])] = firstString(row["id"])
		}
	}
	// Conversations without a derived session remain navigable with unknown usage.
	for _, cv := range conversations {
		cid := firstString(cv["id"])
		if roots[cid] == "" {
			id := "conversation:" + cid
			roots[cid] = id
			sessions = append(sessions, map[string]any{"id": id, "conversation_id": cid, "provider": cv["provider"], "model": cv["model"], "started_at": cv["started_at"], "ended_at": cv["ended_at"], "usage_status": "unavailable", "native_id": "main"})
		}
	}
	if len(sessions) > delegationAnalysisLimit {
		return nil, errDelegationTooLarge()
	}
	// One row per execution with a stream here. Attempts and copied launches are
	// source evidence, not executions: their IDs remain aliases, and a row the
	// evidence does not know stays visible with unresolved ancestry.
	members := map[string][]map[string]any{}
	order, attempts := []string{}, []map[string]any{}
	for _, row := range sessions {
		id := firstString(row["id"])
		nid := id
		if x := p.execution(id); x != nil {
			nid = p.nodeID(x.ID)
		} else if canonical, known := p.alias(id); known {
			s.Aliases[id] = canonical
			if res.Attempts[sourceRef{sourceSession, id}] != nil {
				attempts = append(attempts, row)
			}
			continue
		}
		if nid != id {
			s.Aliases[id] = nid
		}
		if members[nid] == nil {
			order = append(order, nid)
		}
		members[nid] = append(members[nid], row)
		s.members[nid] = append(s.members[nid], id)
	}
	// Each local execution brings its ancestors and then its family as context,
	// so opening any member shows the same executions in the same order.
	local, context := map[executionID]bool{}, []executionID{}
	for _, nid := range order {
		if x := p.execution(firstString(members[nid][0]["id"])); x != nil {
			local[x.ID] = true
		}
	}
	localIDs := slices.Sorted(maps.Keys(local))
	// An unknown family root leaves rows' ancestry unresolved; only the bound
	// makes this snapshot partial. Context fits the analysis limit with the
	// local rows, nearest relatives first.
	expand := []executionID{}
	for _, id := range relatives {
		if x := p.execution(id); x != nil {
			expand = append(expand, x.ID)
		}
	}
	capacity := max(0, delegationAnalysisLimit-len(order))
	context, s.FamilyComplete = p.familyContext(localIDs, expand, min(delegationFamilyLimit, capacity), capacity)
	if !s.FamilyComplete {
		s.Warnings = append(s.Warnings, "Only the nearest part of this work's execution family is shown; open a related session to see its own relatives.")
	}
	elsewhere := []string{}
	for _, id := range append(localIDs, context...) {
		x := res.Executions[id]
		if x.Conversation == "" && local[id] {
			continue
		}
		cid := x.Conversation
		if cid == "" && len(x.Streams) > 0 {
			cid = res.Stream(x.Streams[0]).ConversationID
		}
		if conv[cid] == nil {
			elsewhere = append(elsewhere, cid)
		}
	}
	metas, err := loadDelegationConversationMeta(ctx, tx, uniqueStrings(elsewhere))
	if err != nil {
		return nil, err
	}
	// The opening request of an execution's own transcript, wherever it is
	// retained: subtitle context, never a label.
	initiation := func(x *execution) string {
		if cv := conv[x.Conversation]; cv != nil {
			return firstString(cv["prompt"])
		}
		return metas[x.Conversation].Initiation
	}
	// Executions with streams in other workspaces: this snapshot reads only
	// the local ones, so their metrics are partial or unknown, never complete.
	unqueried := map[*delegationNode]bool{}
	tokenFields := map[string]string{"tokens": "total_tokens", "input": "input_tokens", "uncached": "uncached_input_tokens", "cacheRead": "cache_read_input_tokens", "cacheWrite": "cache_creation_input_tokens", "output": "output_tokens", "reasoning": "reasoning_output_tokens"}
	for _, nid := range order {
		rows := members[nid]
		// The execution's own row leads, so metadata never depends on load order.
		sort.SliceStable(rows, func(i, j int) bool {
			a, b := firstString(rows[i]["id"]), firstString(rows[j]["id"])
			if (a == nid) != (b == nid) {
				return a == nid
			}
			return a < b
		})
		lead := rows[0]
		x := p.execution(firstString(lead["id"]))
		cid := firstString(lead["conversation_id"])
		cv := conv[cid]
		main := lead["native_id"] == "main"
		label := firstString(lead["assignment"])
		if main {
			label = firstString(cv["agent_nickname"], label, cv["prompt"])
		}
		label = firstString(label, lead["native_id"], cv["native_id"])
		if main {
			label = firstString(cv["native_id"], label)
		}
		n := &delegationNode{ID: nid, WorkspaceID: workspace, ConversationID: cid, Provider: firstString(lead["provider"], cv["provider"]), Model: firstString(lead["model"], cv["model"]), Label: label, Coverage: firstString(cv["coverage"]), Usage: "unavailable", envelopeStart: delegationTime(lead["started_at"]), envelopeEnd: delegationTime(lead["ended_at"]), MessageID: firstString(lead["delegation_message_id"]), Self: map[string]delegationMetric{}, All: map[string]delegationMetric{}}
		// Embedded executions must not inherit the parent agent's effort.
		n.Effort = firstString(lead["effort"])
		if main && n.Effort == "" {
			n.Effort = firstString(cv["effort"])
		}
		if x != nil {
			n.execution = x.ID
			n.Label = x.Label.Text
			n.LabelSource = x.Label.Source
			if stream := res.Stream(sourceRef{sourceConversation, x.Conversation}); stream != nil {
				n.NativeID = stream.NativeID
			} else {
				n.NativeID = firstString(lead["native_id"])
			}
			if len(x.Segments) > 1 {
				n.Segments = p.segments(x)
			}
			// The execution's transcript is archived elsewhere; its launch is the
			// stream here. A local segment (a continuation) keeps this row in
			// its own transcript; the other segments stay navigable.
			if meta, ok := metas[x.Conversation]; ok && !p.hasTranscriptIn(x, workspace) {
				n.ConversationID, n.WorkspaceID, n.Provider = meta.ID, meta.WorkspaceID, meta.Provider
				n.Model, n.Effort, n.Coverage = meta.Model, meta.Effort, meta.Coverage
				n.ExternalChild = &delegationReference{ConversationID: meta.ID, WorkspaceID: meta.WorkspaceID, Label: n.Label, Provider: meta.Provider}
			}
		}
		if x != nil && p.unqueried(x, workspace) {
			unqueried[n] = true
		}
		// Accounting stays per stream. Resolving two streams to one execution
		// does not decide whether their reported usage overlaps.
		reporting := 0
		for _, row := range rows {
			usage := firstString(row["usage_status"])
			if usage == "" || usage == "unavailable" || usage == "in-child-conversation" || row["id"] != lead["id"] && integer(row["total_tokens"]) == 0 {
				continue
			}
			reporting++
			if n.Usage == "unavailable" || usage == "reported-partial" {
				n.Usage = usage
			}
			for key, field := range tokenFields {
				v := float64(integer(row[field]))
				if prior := n.Self[key].Value; prior != nil {
					v += *prior
				}
				n.Self[key] = delegationNumber(v, "reported", 1, 1)
			}
			if t := delegationTime(row["started_at"]); t != nil && (n.envelopeStart == nil || *t < *n.envelopeStart) {
				n.envelopeStart = t
			}
			if t := delegationTime(row["ended_at"]); t != nil && (n.envelopeEnd == nil || *t > *n.envelopeEnd) {
				n.envelopeEnd = t
			}
		}
		if reporting > 1 {
			s.Warnings = append(s.Warnings, "An execution has independently reported usage in more than one source stream; each is counted and their overlap is unresolved.")
		}
		for _, m := range delegationMetrics {
			if _, ok := n.Self[m.ID]; !ok {
				n.Self[m.ID] = delegationUnknown()
			}
		}
		if n.Usage == "reported-partial" {
			for key := range tokenFields {
				v := n.Self[key]
				v.Quality = "partial"
				n.Self[key] = v
			}
		}
		for _, key := range []string{"calls", "errors", "messages", "events", "requests", "turns", "toolSum"} {
			n.Self[key] = delegationNumber(0, "observed", 0, 0)
		}
		s.Nodes = append(s.Nodes, n)
		s.ByID[nid] = n
	}
	for _, id := range context {
		x := res.Executions[id]
		cid := x.Conversation
		if cid == "" && len(x.Streams) > 0 {
			cid = res.Stream(x.Streams[0]).ConversationID
		}
		meta := metas[cid]
		n := &delegationNode{ID: p.nodeID(id), execution: id, ConversationID: cid, WorkspaceID: firstString(meta.WorkspaceID, x.WorkspaceID), Provider: firstString(meta.Provider, x.Provider),
			Model: meta.Model, Coverage: meta.Coverage, Label: x.Label.Text, LabelSource: x.Label.Source, Usage: "unavailable", ContextOnly: true, Self: map[string]delegationMetric{}, All: map[string]delegationMetric{}}
		if stream := res.Stream(sourceRef{sourceConversation, cid}); stream != nil {
			n.NativeID = stream.NativeID
		}
		if x.Conversation != "" && meta.Title != n.Label {
			n.Subtitle = meta.Title
		}
		if x.Conversation != "" {
			n.Effort = meta.Effort
		}
		n.Navigation = &delegationReference{ConversationID: cid, WorkspaceID: n.WorkspaceID, Provider: n.Provider, Label: n.Label}
		for _, spec := range delegationMetrics {
			n.Self[spec.ID] = delegationUnknown()
		}
		if s.ByID[n.ID] == nil {
			s.Nodes = append(s.Nodes, n)
			s.ByID[n.ID] = n
		}
	}
	if len(s.Nodes) > delegationAnalysisLimit {
		return nil, errDelegationTooLarge()
	}
	// Attempts stay visible on the execution that issued them.
	unowned := 0
	for _, row := range attempts {
		id := firstString(row["id"])
		a := res.Attempts[sourceRef{sourceSession, id}]
		if n := s.ByID[p.nodeID(a.Owner)]; a.Owner != "" && n != nil {
			n.Attempts = append(n.Attempts, delegationAttempt{ID: id, Outcome: string(a.Outcome), MessageID: firstString(row["delegation_message_id"])})
		} else {
			unowned++
		}
	}
	s.Coverage.Missing = append([]string{}, res.Coverage.Missing...)
	if unowned > 0 {
		s.Warnings = append(s.Warnings, fmt.Sprintf("%d launch attempts have no resolved issuer.", unowned))
	}
	// Parents, ancestry and evidence come from the resolution. A conflicting,
	// missing or cyclic parent leaves the row at the top of the layout with its
	// ancestry unresolved; it never becomes a known root.
	launchOf, commands := map[string]string{}, []string{}
	for _, n := range s.Nodes {
		x := res.Executions[n.execution]
		if x == nil {
			n.Ancestry = delegationAncestry{State: string(parentMissing)}
			continue
		}
		n.Ancestry, n.AncestorIDs = p.ancestry(x.ID)
		if x.Parent.State != parentResolved {
			continue
		}
		var launch sourceRef
		n.ParentID = p.nodeID(x.Parent.ParentID)
		n.ParentEvidence, launch = p.parentEvidence(x)
		n.ParentCertainty = x.Parent.Certainty.String()
		switch {
		case launch.Kind == sourceSession:
			launchOf[n.ID] = launch.ID
		case n.ParentEvidence == "inferred-command-launch" && x.Conversation != "":
			commands = append(commands, x.Conversation)
		}
	}
	messages, err := delegationLaunchMessages(ctx, tx, slices.Sorted(maps.Values(launchOf)))
	if err != nil {
		return nil, err
	}
	commandMessages := map[string]string{}
	if len(commands) > 0 {
		encoded, _ := json.Marshal(commands)
		rows, err := queryMapsContext(ctx, tx, `SELECT l.child_id,t.call_message_id FROM conversation_launches l JOIN tool_calls t ON t.id=l.tool_call_id WHERE l.child_id IN (SELECT value FROM json_each(?)) AND t.call_message_id IS NOT NULL`, string(encoded))
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			commandMessages[firstString(row["child_id"])] = firstString(row["call_message_id"])
		}
	}
	outside := false
	for _, n := range s.Nodes {
		if n.ParentID == "" {
			continue
		}
		if message := firstString(messages[launchOf[n.ID]], commandMessages[res.Executions[n.execution].Conversation]); message != "" {
			n.MessageID = message
		}
		parent := s.ByID[n.ParentID]
		if n.ContextOnly || parent == nil || n.ExternalChild != nil || !parent.ContextOnly && parent.WorkspaceID == n.WorkspaceID {
			continue
		}
		outside = true
		n.ExternalParent = &delegationReference{ConversationID: parent.ConversationID, WorkspaceID: parent.WorkspaceID, Provider: parent.Provider, Label: parent.Label, MessageID: n.MessageID}
	}
	if outside {
		s.Warnings = append(s.Warnings, "A parent outside this workspace is not included in its totals.")
	}
	for state, warning := range map[parentState]string{parentCycle: "Cyclic ownership links leave some parents unresolved.", parentConflict: "Conflicting identity evidence leaves some executions unresolved.", parentAmbiguous: "Equally strong evidence names different parents for some executions."} {
		for _, n := range s.Nodes {
			if n.Ancestry.State == string(state) {
				s.Warnings = append(s.Warnings, warning)
				break
			}
		}
	}
	sort.Strings(s.Warnings)
	for _, n := range s.Nodes {
		if parent := s.ByID[n.ParentID]; parent != nil {
			parent.children = append(parent.children, n)
		} else {
			s.Roots = append(s.Roots, n)
		}
	}
	// Children left out at the family bound stay reachable from their parent.
	for _, n := range s.Nodes {
		if n.execution != "" {
			n.ChildrenOmitted = max(0, len(res.Children(n.execution))-len(n.children))
		}
	}
	// The work's title is local context for its user-started transcripts: a
	// subtitle, never a replacement for the execution's shared label.
	for _, n := range s.Nodes {
		if x := res.Executions[n.execution]; !n.ContextOnly && x != nil && x.Parent.State == parentRoot && conv[x.Conversation] != nil && workspaceTitle != n.Label {
			n.Subtitle = workspaceTitle
		}
		// Otherwise the execution's own opening request, when the label is not it.
		if x := res.Executions[n.execution]; x != nil && n.Subtitle == "" && x.Conversation != "" {
			if prompt := initiation(x); prompt != n.Label {
				n.Subtitle = prompt
			}
		}
	}
	// Activity belongs to the execution its stream binds to. A launch attempt's
	// call belongs to the execution that issued it; a copied launch is not
	// activity of the execution that inherited it.
	owner := func(sid, cid string) *delegationNode {
		if sid == "" {
			sid = roots[cid]
		}
		b := res.ResolveAlias(sid)
		switch b.State {
		case bindingResolved:
		case bindingAttempt:
			if a := res.Attempts[sourceRef{sourceSession, sid}]; a != nil && a.Owner != "" {
				b.ExecutionID = a.Owner
				break
			}
			return nil
		case bindingUnknown:
			if n := s.ByID[sid]; n != nil {
				return n
			}
			return nil
		default:
			return nil
		}
		if n := s.ByID[p.nodeID(b.ExecutionID)]; n != nil && !n.ContextOnly {
			return n
		}
		return nil
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
	cids := make([]string, 0, len(conversations))
	for _, cv := range conversations {
		cids = append(cids, firstString(cv["id"]))
	}
	occurrences, err := res.Occurrences(ctx, tx, cids)
	if err != nil {
		return nil, err
	}
	// Copied history and repeated observations stay inspectable in the
	// transcript but are not an execution's own activity: they are neither
	// counted nor allowed to extend its span. Observations whose equivalence
	// is unresolved still count, and leave their metric partial.
	// A transcript whose copied history the evidence cannot account for
	// (occurrenceIndex.Partial: untyped ancestry, retained rows without a
	// history declaration, a missing copy source) leaves every metric its
	// observations feed partial too, as quick stats does.
	partial := map[*delegationNode]map[string]bool{}
	reasons := map[*delegationNode]map[string]bool{}
	mark := func(n *delegationNode, reason string, metrics []string) {
		if partial[n] == nil {
			partial[n], reasons[n] = map[string]bool{}, map[string]bool{}
		}
		for _, metric := range append(metrics, "clock", "total", "start") {
			partial[n][metric] = true
		}
		reasons[n][reason] = true
	}
	own := func(n *delegationNode, obs sourceRef, sid, cid string, metrics ...string) bool {
		stream := sourceRef{sourceSession, sid}
		if sid == "" {
			stream = sourceRef{sourceConversation, cid}
		}
		switch occurrences.Lookup(obs, stream).Role {
		case occurrenceInherited, occurrenceDuplicate:
			return false
		case occurrenceUnresolved:
			mark(n, "unresolved-observations", metrics)
		}
		transcript := cid
		if st := res.Stream(stream); transcript == "" && st != nil {
			transcript = st.ConversationID
		}
		if reason := occurrences.Partial(transcript); reason != "" {
			mark(n, reason, metrics)
		}
		return true
	}
	// seen records a timed observation before ownership decides whether it
	// is the execution's own: a copy still proves the envelope spans more
	// than the execution's own activity.
	seen := func(n *delegationNode, times ...*float64) {
		if n != nil && slices.ContainsFunc(times, func(at *float64) bool { return at != nil }) {
			n.timed = true
		}
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
		obs := sourceRef{sourceToolCall, id}
		if msg.String != "" {
			obs = sourceRef{sourceMessage, msg.String}
		}
		seen(n, delegationTime(start.String), delegationTime(end.String))
		if n == nil || !own(n, obs, sid.String, cid, "calls", "errors", "toolSum", "files", "filesRead") {
			continue
		}
		t := delegationTool{ID: id, MessageID: msg.String, Name: name, Category: delegationCategory(name, cat, command.String), Status: status, Start: delegationTime(start.String), End: delegationTime(end.String)}
		if duration.Valid {
			v := duration.Float64
			t.Duration = &v
			bump(n, "toolSum", v)
		}
		category, _ := toolCategory(name, "tool_call")
		// Listing tools share the read category and retain directory paths.
		// Exclude them from both path counts and read-coverage accounting.
		listing := command.String == "list"
		switch strings.ToLower(name) {
		case "ls", "list_dir", "list_directory":
			listing = true
		}
		if !listing && (cat == "read" || category == "read" || command.String == "read") && status != "error" {
			n.readCalls++
			key := delegationFileKey(file.String, absolute.String, repoPath.String, repoID.String, cid)
			if status == "ok" && key != "" {
				if n.readFiles == nil {
					n.readFiles = map[string]bool{}
				}
				n.readFiles[key] = true
				n.readKnown++
			}
		}
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
	requests, err := tx.QueryContext(ctx, `SELECT r.id,r.agent_session_id,r.conversation_id,r.requested_at,r.total_tokens FROM conversations c JOIN model_requests r ON r.conversation_id=c.id WHERE c.workspace_id=?`, workspace)
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
		var id, cid string
		var tokens float64
		if err = requests.Scan(&id, &sid, &cid, &at, &tokens); err != nil {
			requests.Close()
			return nil, err
		}
		n := owner(sid.String, cid)
		seen(n, delegationTime(at.String))
		if n == nil {
			continue
		}
		// Token facts stay per stream row, copies included: the session total
		// they reconcile against counts them. Only the occurrence (the request
		// count and its activity point) follows ownership.
		requestTokens[n.ID] += tokens
		if own(n, sourceRef{sourceRequest, id}, sid.String, cid, "requests") {
			bump(n, "requests", 1)
			t := delegationTime(at.String)
			point(n, t)
			if t != nil {
				n.requests = append(n.requests, *t)
			}
		}
	}
	err = requests.Err()
	requests.Close()
	if err != nil {
		return nil, err
	}
	// counted marks the transcripts whose messages an execution already has
	// by recorded membership.
	counted := map[*delegationNode]map[string]bool{}
	message := func(n *delegationNode, id string, turn bool, at any, sid, cid string) {
		seen(n, delegationTime(at))
		if !own(n, sourceRef{sourceMessage, id}, sid, cid, "messages", "turns") {
			return
		}
		bump(n, "messages", 1)
		if turn {
			bump(n, "turns", 1)
		}
		point(n, delegationTime(at))
	}
	facts, err := queryMapsContext(ctx, tx, delegationMessageFactsSQL, workspace, workspace)
	if err != nil {
		return nil, err
	}
	for _, row := range facts {
		sid := firstString(row["agent_session_id"])
		if n := owner(sid, ""); n != nil {
			if counted[n] == nil {
				counted[n] = map[string]bool{}
			}
			counted[n][firstString(row["conversation_id"])] = true
			message(n, firstString(row["id"]), integer(row["turn"]) != 0, row["created_at"], sid, "")
		}
	}
	// Legacy transcripts may predate session-message membership: their
	// messages belong to the execution whose own stream is the transcript,
	// unless it already has that transcript's messages by membership. Each
	// segment of a continuation is its own transcript and counts.
	for _, cv := range conversations {
		cid := firstString(cv["id"])
		n := owner(roots[cid], cid)
		if n == nil || counted[n][cid] {
			continue
		}
		rows, err := queryMapsContext(ctx, tx, `SELECT id,role='user' AND kind='message' turn,created_at FROM messages m WHERE conversation_id=? AND NOT EXISTS(SELECT 1 FROM agent_session_messages am WHERE am.message_id=m.id)`, cid)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			message(n, firstString(row["id"]), integer(row["turn"]) != 0, row["created_at"], "", cid)
		}
	}
	// Events resting on superseded execution evidence are withheld, as the
	// Agent Experience datasets withhold them (agentEventsWithheld), and
	// events awaiting a rebuild are pending: either leaves the events metric
	// of every row reading that transcript partial, never a confirmed count.
	eventsWithheld, eventsPending, err := delegationEventFreshness(ctx, tx)
	if err != nil {
		return nil, err
	}
	// Count the same retained events shown by the reader and timeline. Stream
	// only identities; alias memberships must not count an event twice.
	events, err := tx.QueryContext(ctx, `SELECT e.id,e.conversation_id,e.message_id,am.agent_session_id
 FROM conversations c JOIN agent_events e ON e.conversation_id=c.id
 JOIN messages m ON m.id=e.message_id
 LEFT JOIN agent_session_messages am ON am.message_id=m.id
 WHERE c.workspace_id=? AND e.probability>=?`, workspace, agentEventThreshold)
	if err != nil {
		return nil, err
	}
	seenEvents := map[string]map[int64]bool{}
	for events.Next() {
		eventCount++
		if eventCount > 500000 {
			events.Close()
			return nil, fmt.Errorf("workspace exceeds the 500,000-event analysis limit")
		}
		var id int64
		var cid, message string
		var sid sql.NullString
		if err = events.Scan(&id, &cid, &message, &sid); err != nil {
			events.Close()
			return nil, err
		}
		if eventsWithheld[cid] {
			continue
		}
		if n := owner(sid.String, cid); n != nil && own(n, sourceRef{sourceMessage, message}, sid.String, cid, "events") {
			if seenEvents[n.ID] == nil {
				seenEvents[n.ID] = map[int64]bool{}
			}
			if !seenEvents[n.ID][id] {
				bump(n, "events", 1)
				seenEvents[n.ID][id] = true
			}
		}
	}
	err = events.Err()
	events.Close()
	if err != nil {
		return nil, err
	}
	book, err := c.loadPriceBookFrom(ctx, tx)
	if err != nil {
		return nil, err
	}
	ledger, err := queryMapsContext(ctx, tx, `SELECT u.*,u.model usage_model,a.model session_model FROM agent_sessions a JOIN agent_session_usage u ON u.agent_session_id=a.id WHERE a.workspace_id=?`, workspace)
	if err != nil {
		return nil, err
	}
	evidence, err := queryMapsContext(ctx, tx, `SELECT e.*,m.conversation_id,a.workspace_id FROM agent_session_token_evidence e JOIN agent_sessions a ON a.id=e.agent_session_id JOIN messages m ON m.id=e.message_id WHERE a.workspace_id=? ORDER BY e.agent_session_id,e.message_id`, workspace)
	if err != nil {
		return nil, err
	}
	for _, row := range evidence {
		// An observation describes the agent a launch ran. A launch that
		// created no confirmed execution has none here, and its issuer
		// (owner's attempt fallback) did not consume it.
		if res.ResolveAlias(firstString(row["agent_session_id"])).State == bindingAttempt {
			continue
		}
		if n := owner(firstString(row["agent_session_id"]), ""); n != nil {
			// The launch is a navigation placeholder for usage held in another
			// workspace, or the part read here of an execution whose transcript
			// is archived there. Keep its source observation in storage, but do
			// not turn it into a second workspace's consumed-token total.
			if n.ExternalChild != nil && (n.Usage == "unavailable" || unqueried[n]) {
				continue
			}
			if n.TokenAttribution == nil {
				n.TokenAttribution = &sessionTokenAttribution{}
			}
			e := sessionTokenEvidence{Scope: firstString(row["scope"]), TotalTokens: integer(row["total_tokens"]), MessageID: firstString(row["message_id"]), ConversationID: firstString(row["conversation_id"]), WorkspaceID: firstString(row["workspace_id"])}
			n.TokenAttribution.Evidence = append(n.TokenAttribution.Evidence, e)
			n.TokenAttribution.Observed = max(n.TokenAttribution.Observed, float64(e.TotalTokens))
		}
	}
	var attributionVersion string
	if err := tx.QueryRowContext(ctx, "SELECT version FROM usage_attribution_state WHERE workspace_id=?", workspace).Scan(&attributionVersion); err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	// Version 6 introduced scoped remainder provenance. Older ledgers used
	// request for both requests and remainders, so retain their previous view.
	scopedLedger := integer(attributionVersion) >= 6
	scopedNodes := map[string]bool{}
	priceStatus := map[string]string{}
	for _, row := range ledger {
		n := owner(firstString(row["agent_session_id"]), "")
		if n == nil {
			continue
		}
		remainder := strings.HasSuffix(firstString(row["attribution"]), "-remainder")
		if scopedLedger || remainder {
			scopedNodes[n.ID] = true
			if n.TokenAttribution == nil {
				n.TokenAttribution = &sessionTokenAttribution{}
			}
		}
		sharedRemainder := remainder && (len(n.children) > 0 || n.ExternalChild != nil)
		if sharedRemainder {
			if n.unallocatedTokens == nil {
				n.unallocatedTokens = tokenCounts{}
			}
			for key, field := range tokenFields {
				n.unallocatedTokens[key] += float64(integer(row[field]))
			}
		}
		day := firstString(row["usage_hour"])
		if len(day) > 10 {
			day = day[:10]
		}
		cost := book.cost(ledgerModel(row), day, ledgerTokens(row))
		priceStatus[n.ID] = worsePriceStatus(priceStatus[n.ID], cost.status)
		if cost.cost != nil {
			bump(n, "cost", *cost.cost)
			if sharedRemainder {
				n.unallocatedCost += *cost.cost
			}
		}
	}
	for _, n := range s.Nodes {
		if n.ContextOnly {
			n.ChildCount = len(n.children)
			continue
		}
		if v := n.Self["cost"]; v.Value != nil {
			v.Quality = "estimated"
			if priceStatus[n.ID] == "partial" || priceStatus[n.ID] == "unpriced" {
				v.Quality = "partial"
			}
			n.Self["cost"] = v
		}
		if a := n.TokenAttribution; a != nil {
			n.accountedTokens = map[string]delegationMetric{}
			for key := range tokenFields {
				m := n.Self[key]
				n.accountedTokens[key] = m
				if m.Value != nil {
					n.Self[key] = delegationNumber(max(0, *m.Value-n.unallocatedTokens[key]), m.Quality, m.Known, m.Total)
				}
			}
			if m := n.accountedTokens["tokens"]; m.Value != nil {
				a.Accounted = *m.Value
				a.Unallocated = min(a.Accounted, n.unallocatedTokens["tokens"])
				a.Direct = max(0, a.Accounted-a.Unallocated)
				n.Self["remainder"] = delegationNumber(a.Unallocated, "reported", 1, 1)
			}
			if m := n.Self["cost"]; m.Value != nil {
				n.accountedTokens["cost"] = m
				if a.Direct > 0 {
					n.Self["cost"] = delegationNumber(max(0, *m.Value-n.unallocatedCost), m.Quality, m.Known, m.Total)
				} else {
					n.Self["cost"] = delegationUnknown()
				}
			}
			if a.Unallocated > 0 {
				if m := n.Self["cost"]; m.Value != nil {
					m.Quality = "partial"
					n.Self["cost"] = m
				}
				for key := range tokenFields {
					m := n.Self[key]
					if a.Direct == 0 {
						n.Self[key] = delegationUnknown()
					} else if m.Value != nil {
						m.Quality = "partial"
						n.Self[key] = m
					}
				}
			}
			if a.Observed > a.Direct {
				n.Self["tokens"] = delegationNumber(a.Observed, "reported-minimum", 1, 1)
				if n.Usage == "unavailable" {
					n.Usage = "reported-minimum"
				}
			}
		}
		if !scopedNodes[n.ID] {
			t := n.Self["tokens"]
			if n.TokenAttribution != nil {
				t = n.accountedTokens["tokens"]
			}
			// Legacy rows without ledger provenance keep their previous view
			// until the stored-message repair supplies attribution.
			if t.Value != nil {
				n.Self["remainder"] = delegationNumber(max(0, *t.Value-requestTokens[n.ID]), t.Quality, 1, 1)
			}
		}
		// The span is the execution's own timed activity. Only a stream with no
		// timed observation at all (a handle or tokenless usage) falls back to
		// its recorded envelope, reported rather than observed; one whose timed
		// observations are all copies has no span of its own.
		quality := "observed"
		if n.Start == nil && !n.timed && (n.envelopeStart != nil || n.envelopeEnd != nil) {
			n.Start, n.End, quality = n.envelopeStart, n.envelopeEnd, "reported"
		}
		if n.Start != nil && n.End != nil {
			span := max(0, *n.End-*n.Start)
			n.Self["start"] = delegationNumber(*n.Start, quality, 1, 1)
			n.Self["clock"] = delegationNumber(span, quality, 1, 1)
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
		if n.readCalls == 0 || n.readKnown > 0 {
			quality := "observed"
			if n.readKnown < n.readCalls {
				quality = "partial"
			}
			n.Self["filesRead"] = delegationNumber(float64(len(n.readFiles)), quality, n.readKnown, n.readCalls)
		} else {
			n.Self["filesRead"] = delegationMetric{Quality: "unknown", Total: n.readCalls}
		}
		n.Self["episodes"] = delegationNumber(float64(len(n.episodes)), "inferred", len(n.episodes), len(n.episodes))
		if unqueried[n] {
			if n.Self["tokens"].Value == nil && len(n.tools) == 0 && *n.Self["messages"].Value == 0 && *n.Self["requests"].Value == 0 {
				// Nothing of it was read here: unknown, not zero.
				for _, spec := range delegationMetrics {
					n.Self[spec.ID] = delegationUnknown()
				}
			} else {
				for key, m := range n.Self {
					if m.Value != nil && m.Quality != "unknown" {
						m.Quality = "partial"
						n.Self[key] = m
					}
				}
			}
		}
		n.ChildCount = len(n.children)
		n.Self["children"] = delegationNumber(float64(n.ChildCount), "observed", 1, 1)
		leaf := 0.0
		if n.ChildCount == 0 {
			leaf = 1
		}
		n.Self["leaves"] = delegationNumber(leaf, "observed", 1, 1)
	}
	for n, metrics := range partial {
		for key := range metrics {
			if m := n.Self[key]; m.Value != nil {
				m.Quality = "partial"
				n.Self[key] = m
			}
		}
		n.OccurrencePartial = slices.Sorted(maps.Keys(reasons[n]))[0]
	}
	for _, n := range s.Nodes {
		for _, cid := range delegationNodeTranscripts(n) {
			if eventsWithheld[cid] || eventsPending[cid] {
				m := n.Self["events"]
				if m.Value == nil {
					m = delegationNumber(0, "partial", 0, 1)
				}
				m.Quality = "partial"
				n.Self["events"] = m
				n.EventsPending = true
				break
			}
		}
	}
	s.finish()
	for _, n := range s.Nodes {
		switch {
		case n.Ancestry.State == string(parentConflict):
			s.Coverage.Conflicts++
			s.Coverage.UnresolvedParents++
		case n.Ancestry.State != string(parentRoot) && n.Ancestry.State != string(parentResolved):
			s.Coverage.UnresolvedParents++
		case n.ParentCertainty == evidenceLegacy.String():
			s.Coverage.UnverifiedParents++
		}
		if unqueried[n] {
			s.Coverage.Unqueried++
		}
	}
	return s, nil
}

// finish orders the snapshot's trees in the resolution's sibling order,
// and computes subtree totals, scale maxima, density and the content hash.
// A snapshot without a resolution orders siblings by start time.
func (s *delegationSnapshot) finish() {
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
	if s.projection.res != nil {
		less = s.projection.less
	}
	ordered := []*delegationNode{}
	var walk func([]*delegationNode, int)
	walk = func(nodes []*delegationNode, depth int) {
		sort.Slice(nodes, func(i, j int) bool { return less(nodes[i], nodes[j]) })
		for _, n := range nodes {
			n.Depth = depth
			n.Order = len(ordered)
			if n.Ancestry.Depth != nil {
				// Indentation is layout; the depth metric carries the
				// resolver's depth quality.
				n.Self["depth"] = delegationNumber(float64(*n.Ancestry.Depth), n.Ancestry.DepthQuality, 1, 1)
			} else {
				n.Self["depth"] = delegationMetric{Quality: "unknown", Total: 1}
			}
			ordered = append(ordered, n)
			walk(n.children, depth+1)
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
	walk(s.Roots, 0)
	s.Nodes = ordered
	s.Totals = delegationWorkspaceTotals(s.Nodes)
	s.Maxima = map[string]map[string]float64{"self": {}, "all": {}}
	s.Density = make([]int, 120)
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
	hashInput := []any{s.Revision, s.Nodes, s.Aliases}
	for _, n := range s.Nodes {
		hashInput = append(hashInput, n.tools, n.requests)
	}
	payload, _ := json.Marshal(hashInput)
	hash := sha256.Sum256(payload)
	s.ID = fmt.Sprintf("%x", hash[:12])
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

// Incoming ancestors explain ownership but are not executions accounted in
// this workspace. Outside descendants retain their existing partial coverage.
func delegationWorkspaceTotals(nodes []*delegationNode) map[string]delegationMetric {
	included := []*delegationNode{}
	ids := map[string]bool{}
	for _, n := range nodes {
		if !n.ContextOnly {
			included = append(included, n)
			ids[n.ID] = true
		}
	}
	totals := delegationAggregate(included)
	children := 0
	for _, n := range included {
		if ids[n.ParentID] {
			children++
		}
	}
	totals["children"] = delegationNumber(float64(children), "observed", len(included), len(included))
	return totals
}

func delegationAggregate(nodes []*delegationNode) map[string]delegationMetric {
	result := map[string]delegationMetric{}
	for _, spec := range delegationMetrics {
		value, known := 0.0, 0
		quality := "observed"
		for _, n := range nodes {
			m := n.Self[spec.ID]
			if accounted, ok := n.accountedTokens[spec.ID]; ok {
				m = accounted
			}
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
		case "files", "filesRead":
			files := map[string]bool{}
			for _, n := range nodes {
				paths := n.editedFiles
				if spec.ID == "filesRead" {
					paths = n.readFiles
				}
				for key := range paths {
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
	result["tokens"] = delegationTokenTotal(nodes)
	return result
}

// delegationCategory groups a call by its tool, or for a shell call by what
// its primary command does, so "cat" and "rg" read and "git" is Git.
func delegationCategory(name, cat, commandCategory string) string {
	name = strings.ToLower(name)
	cat = strings.ToLower(cat)
	switch {
	case name == "wait_agent" || name == "wait" || name == "taskoutput":
		return "wait"
	case strings.Contains(name, "sleep") || strings.Contains(name, "poll") || name == "write_stdin":
		return "poll"
	case cat == "agent" || strings.Contains(name, "delegate") || strings.Contains(name, "spawn") || name == "task":
		return "delegate"
	case cat == "read" || cat == "search" || commandCategory == "read" || commandCategory == "search" || commandCategory == "list" ||
		strings.Contains(name, "read") || strings.Contains(name, "search") || strings.Contains(name, "glob"):
		return "read"
	case cat == "edit" || cat == "write" || commandCategory == "file-write" || strings.Contains(name, "patch") || strings.Contains(name, "write") || name == "edit":
		return "write"
	case commandCategory == "vcs" || strings.Contains(name, "git"):
		return "git"
	case cat == "network" || commandCategory == "network" || strings.Contains(name, "web") || strings.Contains(name, "fetch"):
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
	var relatives []string
	for _, id := range strings.Split(r.URL.Query().Get("relatives"), ",") {
		if id != "" && len(relatives) < 100 {
			relatives = append(relatives, id)
		}
	}
	snapshot, err := s.Catalog.workGroupSnapshot(r.Context(), workspace, relatives, r.URL.Query().Get("include_uncertain") == "1")
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
		if canonical, ok := snapshot.resolveAlias(id); ok && canonical != "" {
			id = canonical
		}
		collapsed[id] = true
	}
	nodes := []*delegationNode{}
	matched := map[string]bool{}
	for _, n := range snapshot.Nodes {
		if (provider == "" || n.Provider == provider) && (search == "" || strings.Contains(strings.ToLower(n.Label+" "+n.Provider+" "+n.Model+" "+n.NativeID+" "+n.ID+" "+n.ConversationID), search)) {
			matched[n.ID] = true
		}
	}
	visible := map[string]bool{}
	for id := range matched {
		visible[id] = true
		if mode == "hierarchy" {
			for _, ancestor := range snapshot.ByID[id].AncestorIDs {
				visible[ancestor] = true
			}
		}
	}
	for _, n := range snapshot.Nodes {
		if !visible[n.ID] {
			continue
		}
		hidden := false
		if mode == "hierarchy" {
			for _, ancestor := range n.AncestorIDs {
				if collapsed[ancestor] {
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
		totals = delegationWorkspaceTotals(included)
		childCount := 0
		for _, n := range included {
			if parent := snapshot.ByID[n.ParentID]; !n.ContextOnly && parent != nil && !parent.ContextOnly {
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
		for _, ancestor := range n.AncestorIDs {
			p := snapshot.ByID[ancestor]
			if p != nil && !seen[p.ID] {
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
	// Sessions in an unresolved identity conflict are counted once per
	// partition the resolver kept, so the count is not exact.
	sessionCount, uncertain := 0, 0
	// Every row with withheld or rebuilding events, including rows a collapse
	// or page leaves out, so the Timeline can say so where no lane is drawn.
	eventsPendingSessions := 0
	for _, n := range snapshot.Nodes {
		if n.EventsPending {
			eventsPendingSessions++
		}
		if !n.ContextOnly {
			sessionCount++
			// The same scope as usage's run_identity: a conflict on the session
			// or on any ancestor makes its run and cohort counts inexact.
			if n.Ancestry.State == string(parentConflict) || n.Ancestry.Conflicted {
				uncertain++
			}
		}
		if n.ConversationID == q.Get("conversation") || slices.ContainsFunc(n.Segments, func(s delegationSegment) bool { return s.ConversationID == q.Get("conversation") }) {
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
	response := map[string]any{"focus": focus, "focus_truncated": focusTruncated, "snapshot": snapshot.ID, "rows": page, "ancestors": ancestors, "ancestors_truncated": ancestorsTruncated, "matched": pageMatches, "total": len(nodes), "session_count": sessionCount, "session_count_uncertain": uncertain, "events_pending_sessions": eventsPendingSessions, "offset": offset, "next_cursor": next, "metrics": delegationMetrics, "maxima": snapshot.Maxima, "totals": totals, "group_totals": snapshot.GroupTotals, "start": snapshot.Start, "end": snapshot.End, "density": snapshot.Density, "warnings": snapshot.Warnings, "family_complete": snapshot.FamilyComplete, "coverage": snapshot.Coverage}
	// Saved links can name sessions by IDs that now alias an execution. Answer
	// only for IDs the client asks about: canonical (itself for a current
	// row, so colon-containing disclosure keys split unambiguously), or null
	// when unresolved.
	if requested := q["resolve"]; len(requested) > 0 {
		aliases := map[string]any{}
		// A saved ID that names no row here (split, an attempt, a copy whose
		// original is unknown, or unknown) is null: the client keeps it
		// visibly unresolved instead of choosing a session for it.
		for _, id := range requested[:min(len(requested), 200)] {
			canonical, _ := snapshot.resolveAlias(id)
			switch {
			case canonical == "" || snapshot.ByID[canonical] == nil:
				aliases[id] = nil
			default:
				aliases[id] = canonical
			}
		}
		response["aliases"] = aliases
	}
	writeJSON(w, response, 200)
}

// resolveAlias returns the row an earlier session ID now names. ok is false
// for unknown IDs; an empty canonical ID marks a known alias that no longer
// identifies one row.
func (s *delegationSnapshot) resolveAlias(id string) (canonical string, ok bool) {
	if s.ByID[id] != nil {
		return id, true
	}
	if canonical, ok := s.Aliases[id]; ok {
		return canonical, true
	}
	if s.projection.res == nil {
		return "", false
	}
	return s.projection.alias(id)
}

type delegationMark struct {
	Start            float64 `json:"start"`
	End              float64 `json:"end"`
	EndKnown         bool    `json:"end_known"`
	Category         string  `json:"category"`
	Count            int     `json:"count"`
	Error            bool    `json:"error"`
	ToolID           string  `json:"tool_id,omitempty"`
	MessageID        string  `json:"message_id,omitempty"`
	Name             string  `json:"name"`
	Preview          string  `json:"preview,omitempty"`
	PreviewTruncated bool    `json:"preview_truncated,omitempty"`
	Status           string  `json:"status,omitempty"`
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
	var messageLanes map[string]map[string]any
	var taxonomy []map[string]any
	if q.Get("labels") == "1" {
		var err error
		messageLanes, taxonomy, err = s.Catalog.delegationTimelineMessages(r.Context(), snapshot, ids, start, end)
		if errors.Is(err, errDelegationSnapshotChanged) {
			writeJSON(w, map[string]any{"error": "Archive changed; refresh the explorer.", "code": "snapshot_changed"}, 409)
			return
		}
		if err != nil {
			writeError(w, err, 500)
			return
		}
	}
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
			if q.Get("labels") != "1" && stop-*t.Start < bucket && len(marks) < 2000 && !mark.Error {
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
		if q.Get("labels") == "1" {
			if err := s.Catalog.delegationToolPreviews(r.Context(), n.WorkspaceID, marks); err != nil {
				writeError(w, err, 500)
				return
			}
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
		lane := map[string]any{"session": n.ID, "marks": marks, "model_inferred": phases, "episodes": n.episodes, "truncated": truncated}
		for key, value := range messageLanes[n.ID] {
			lane[key] = value
		}
		lanes = append(lanes, lane)
	}
	body := map[string]any{"snapshot": snapshot.ID, "start": start, "end": end, "lanes": lanes, "inference": "Model/orchestration between nearby retained observations (at most 90 seconds); not measured processing or streaming. Blank time is unclassified."}
	if messageLanes != nil {
		body["taxonomy"] = taxonomy
	}
	writeJSON(w, body, 200)
}

// delegationEventFreshness is the transcripts whose stored agent events are
// withheld (agentEventsWithheld, the set the Agent Experience datasets leave
// out) and those awaiting a rebuild (agent_events_dirty).
func delegationEventFreshness(ctx context.Context, q queryer) (withheld, pending map[string]bool, err error) {
	withheld, pending = map[string]bool{}, map[string]bool{}
	present, err := agentEventsLedgerPresent(ctx, q)
	if err != nil || !present {
		return withheld, pending, err
	}
	ids, err := agentEventsWithheld(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	for _, id := range ids {
		withheld[id] = true
	}
	rows, err := queryMapsContext(ctx, q, `SELECT conversation_id FROM agent_events_dirty`)
	if err != nil && !strings.Contains(err.Error(), "no such table") {
		return nil, nil, err
	}
	for _, row := range rows {
		pending[firstString(row["conversation_id"])] = true
	}
	return withheld, pending, nil
}

// delegationNodeTranscripts is every transcript a row reads: its own and its
// segments'.
func delegationNodeTranscripts(n *delegationNode) []string {
	out := []string{n.ConversationID}
	for _, segment := range n.Segments {
		out = append(out, segment.ConversationID)
	}
	return out
}
