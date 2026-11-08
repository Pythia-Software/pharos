package archive

import (
	"context"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"
)

// Shadow extraction of execution evidence from rows earlier ingests wrote.
// Their provenance is mostly unknown, so stored relationships become legacy
// claims and parsed launch results become derived claims with a named rule.
// Nothing here is promoted to native proof. Persisted typed evidence is added
// by executionClaimSources (execution_evidence.go), which may upgrade or mark
// as synthesized the same relationships.
//
// Loading is split in two so a cached snapshot can reload only what changed
// (execution_revision.go): fetchExecutionSourceRows reads compact rows owned
// by a set of conversations, and assemble turns all of them into evidence in
// memory, where the matching that spans conversations happens.

// executionClaimSource adds streams or claims to the evidence of one
// generation. It reads through q, the same snapshot as the legacy rows.
type executionClaimSource func(ctx context.Context, q queryer, e *executionEvidence) error

// occurrenceClaimSource returns occurrence claims (inherited copies,
// candidates, duplicates) for the given conversations; nil means all.
type occurrenceClaimSource func(ctx context.Context, q queryer, conversations []string) ([]executionClaim, error)

var (
	executionClaimSources  []executionClaimSource
	occurrenceClaimSources []occurrenceClaimSource
	// legacyExecutionScans names the legacy scans still needed. A persisted
	// evidence source that covers one completely can turn it off; until then
	// the shadow extraction keeps it, so results never lose evidence.
	legacyExecutionScans = map[string]bool{"launch-results": true, "copied-launches": true, "copied-occurrences": true}
)

// executionEvidenceGeneration is the source generation evidence is read at, in
// q's snapshot (see executionSourceGeneration); zero when q does not track it.
func executionEvidenceGeneration(ctx context.Context, q queryer) (int64, error) {
	generation, _, err := executionSourceGeneration(ctx, q)
	return generation, err
}

func loadExecutionEvidence(ctx context.Context, q queryer) (*executionEvidence, error) {
	return loadExecutionEvidenceWith(ctx, q, nil)
}

// loadExecutionEvidenceWith uses launches (child_id, parent_id) in place of
// the stored command-launch ledger when not nil, as offline labeling does.
func loadExecutionEvidenceWith(ctx context.Context, q queryer, launches []map[string]any) (*executionEvidence, error) {
	generation, err := executionEvidenceGeneration(ctx, q)
	if err != nil {
		return nil, err
	}
	rows, err := fetchExecutionSourceRows(ctx, q, nil, nil)
	if err != nil {
		return nil, err
	}
	if launches != nil {
		rows.launches = map[string][]executionLaunchRow{}
		for _, launch := range launches {
			child := firstString(launch["child_id"])
			rows.launches[child] = append(rows.launches[child], executionLaunchRow{child: child, parent: firstString(launch["parent_id"])})
		}
	}
	return rows.evidence(ctx, q, generation)
}

// Compact stored rows, grouped by the conversation that owns them.
type executionConversationRow struct {
	id, workspace, provider, account, native, parent, path, nickname, started string
	aliases                                                                   []string
	depth                                                                     int
	mainSession, mainParent                                                   string
	typed                                                                     bool // typed evidence is current for the transcript
	name, nameSource, humanName, requestName, contextName                     string
	nameRank                                                                  int
}

type executionSessionRow struct {
	id, conversation, workspace, provider, account, native, parent, child, delegation, kind, assignment, at string
	launch                                                                                                  string // digest of the launch call's ID, role, kind and body
	position                                                                                                int
	observed                                                                                                bool
	tokens                                                                                                  int64
}

type executionLinkRow struct{ left, right, relationship string }

type executionLaunchRow struct{ child, parent string }

type executionOriginRow struct {
	conversation, parentNative, parentProvider, parentAccount string
	// sameSource: the observation came from an adapter of the conversation's
	// own provider, which names parents only in that provider.
	sameSource bool
}

// executionResultRow is a recognized launch result envelope, already parsed.
type executionResultRow struct {
	launch, result string
	handle         legacyLaunchResult
}

type executionCopyRow struct{ copy, original, message, provider, account string }

// executionSourceRows holds every stored row evidence is assembled from:
// conversations and their main sessions by ID; launch sessions, legacy launch
// results and legacy copied launches by the conversation that retains them;
// identity links by left_id; command launches by child_id; retained native
// parents by conversation. Cached copies are shared and never modified.
type executionSourceRows struct {
	conversations map[string]*executionConversationRow
	sessions      map[string][]executionSessionRow
	links         map[string][]executionLinkRow
	launches      map[string][]executionLaunchRow
	origins       map[string][]executionOriginRow
	results       map[string][]executionResultRow
	copies        map[string][]executionCopyRow
	// typed digests each conversation's stored typed claims and evidence
	// state, which registered claim sources read: equal digests and equal
	// rows mean equal evidence.
	typed map[string]string
}

// executionScope restricts a query to rows owned by scope (nil: all).
func executionScope(column string, scope []string) (string, []any) {
	if scope == nil {
		return "", nil
	}
	return " AND " + column + " IN (SELECT value FROM json_each(?))", []any{jsonText(scope)}
}

// fetchExecutionSourceRows reads the rows owned by scope (nil: every
// conversation). Legacy copied launches depend on ancestors' rows too, so
// they are read for copyScope, which a caller widens to the descendants of
// scope (nil copyScope with non-nil scope: the same as scope).
func fetchExecutionSourceRows(ctx context.Context, q queryer, scope, copyScope []string) (*executionSourceRows, error) {
	if scope != nil && copyScope == nil {
		copyScope = scope
	}
	out := &executionSourceRows{conversations: map[string]*executionConversationRow{}, sessions: map[string][]executionSessionRow{},
		links: map[string][]executionLinkRow{}, launches: map[string][]executionLaunchRow{}, origins: map[string][]executionOriginRow{},
		results: map[string][]executionResultRow{}, copies: map[string][]executionCopyRow{}, typed: map[string]string{}}
	typed, args, err := executionTypedState(ctx, q, "c.id")
	if err != nil {
		return nil, err
	}
	where, scoped := executionScope("c.id", scope)
	conversations, err := queryMapsContext(ctx, q, `SELECT c.id,c.workspace_id,c.provider,c.account,c.native_id,c.parent_id,c.agent_depth,
 c.agent_path,c.agent_nickname,c.started_at,c.aliases_json,a.id main_session_id,a.parent_id main_parent_id,`+typed+` typed
 FROM conversations c LEFT JOIN agent_sessions a ON a.conversation_id=c.id AND a.native_id='main' WHERE 1`+where, append(args, scoped...)...)
	if err != nil {
		return nil, err
	}
	for _, cv := range conversations {
		row := &executionConversationRow{id: firstString(cv["id"]), workspace: firstString(cv["workspace_id"]), provider: firstString(cv["provider"]),
			account: firstString(cv["account"]), native: firstString(cv["native_id"]), parent: firstString(cv["parent_id"]),
			path: firstString(cv["agent_path"]), nickname: firstString(cv["agent_nickname"]), started: firstString(cv["started_at"]),
			depth: int(integer(cv["agent_depth"])), mainSession: firstString(cv["main_session_id"]), mainParent: firstString(cv["main_parent_id"]),
			typed: integer(cv["typed"]) != 0}
		_ = json.Unmarshal([]byte(firstString(cv["aliases_json"])), &row.aliases)
		out.conversations[row.id] = row
	}
	if err := loadExecutionNames(ctx, q, scope, out); err != nil {
		return nil, err
	}
	// Only identity fields: no transcript bodies besides the clipped assignment.
	where, args = executionScope("a.conversation_id", scope)
	sessions, err := queryMapsContext(ctx, q, `SELECT a.id,a.conversation_id,a.workspace_id,a.provider,c.account,a.native_id,a.parent_id,
 a.child_conversation_id,a.delegation_message_id,a.kind,a.started_at,a.total_tokens,m.created_at launch_at,m.source_order launch_order,
 m.call_id launch_call,m.role launch_role,m.kind launch_kind,m.text launch_text,
 `+delegationAssignmentCandidatesSQL+` assignment_candidates,
 EXISTS(SELECT 1 FROM agent_session_messages am WHERE am.agent_session_id=a.id) observed
 FROM agent_sessions a JOIN conversations c ON c.id=a.conversation_id
 LEFT JOIN messages m ON m.id=a.delegation_message_id
 WHERE a.native_id<>'main'`+where, args...)
	if err != nil {
		return nil, err
	}
	for _, row := range sessions {
		position := -1
		if v, ok := row["launch_order"]; ok && v != nil {
			position = int(integer(v))
		}
		s := executionSessionRow{id: firstString(row["id"]), conversation: firstString(row["conversation_id"]), workspace: firstString(row["workspace_id"]),
			provider: firstString(row["provider"]), account: firstString(row["account"]), native: firstString(row["native_id"]), parent: firstString(row["parent_id"]),
			child: firstString(row["child_conversation_id"]), delegation: firstString(row["delegation_message_id"]), kind: firstString(row["kind"]),
			assignment: delegationAssignment(firstString(row["assignment_candidates"])), at: firstString(row["launch_at"], row["started_at"]), position: position,
			observed: integer(row["observed"]) != 0, tokens: integer(row["total_tokens"])}
		if s.delegation != "" {
			s.launch = hashBytes([]byte(strings.Join([]string{firstString(row["launch_call"]), firstString(row["launch_role"]),
				firstString(row["launch_kind"]), firstString(row["launch_text"])}, "\x00")))
		}
		out.sessions[s.conversation] = append(out.sessions[s.conversation], s)
	}
	where, args = executionScope("left_id", scope)
	links, err := queryMapsContext(ctx, q, `SELECT left_id,right_id,relationship FROM conversation_identity_links WHERE 1`+where, args...)
	if err != nil {
		return nil, err
	}
	for _, link := range links {
		l := executionLinkRow{firstString(link["left_id"]), firstString(link["right_id"]), firstString(link["relationship"])}
		out.links[l.left] = append(out.links[l.left], l)
	}
	where, args = executionScope("child_id", scope)
	launches, err := queryMapsContext(ctx, q, `SELECT child_id,parent_id FROM conversation_launches WHERE 1`+where, args...)
	if err != nil {
		return nil, err
	}
	for _, launch := range launches {
		l := executionLaunchRow{firstString(launch["child_id"]), firstString(launch["parent_id"])}
		out.launches[l.child] = append(out.launches[l.child], l)
	}
	if err := fetchRetainedNativeParents(ctx, q, scope, out); err != nil {
		return nil, err
	}
	if err := fetchTypedDigests(ctx, q, scope, out); err != nil {
		return nil, err
	}
	if legacyExecutionScans["launch-results"] {
		if err := fetchLegacyLaunchResults(ctx, q, scope, out); err != nil {
			return nil, err
		}
	}
	if legacyExecutionScans["copied-launches"] {
		if err := fetchLegacyCopiedLaunches(ctx, q, copyScope, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// merge returns rows with every entry owned by scope (and copies owned by
// copyScope) replaced by fresh's. Neither input is modified.
func (rows *executionSourceRows) merge(fresh *executionSourceRows, scope, copyScope []string) *executionSourceRows {
	out := &executionSourceRows{conversations: maps.Clone(rows.conversations), sessions: maps.Clone(rows.sessions), links: maps.Clone(rows.links),
		launches: maps.Clone(rows.launches), origins: maps.Clone(rows.origins), results: maps.Clone(rows.results), copies: maps.Clone(rows.copies),
		typed: maps.Clone(rows.typed)}
	for _, id := range scope {
		delete(out.typed, id)
		delete(out.conversations, id)
		delete(out.sessions, id)
		delete(out.links, id)
		delete(out.launches, id)
		delete(out.origins, id)
		delete(out.results, id)
	}
	for _, id := range copyScope {
		delete(out.copies, id)
	}
	maps.Copy(out.typed, fresh.typed)
	maps.Copy(out.conversations, fresh.conversations)
	maps.Copy(out.sessions, fresh.sessions)
	maps.Copy(out.links, fresh.links)
	maps.Copy(out.launches, fresh.launches)
	maps.Copy(out.origins, fresh.origins)
	maps.Copy(out.results, fresh.results)
	maps.Copy(out.copies, fresh.copies)
	return out
}

// sameEvidence reports whether fresh, read for the conversations in scope
// and the copies in copyScope, holds exactly the rows rows has for them, so
// the evidence, and its resolution, are unchanged.
func (rows *executionSourceRows) sameEvidence(fresh *executionSourceRows, scope, copyScope []string) bool {
	for _, id := range scope {
		a, b := rows.conversations[id], fresh.conversations[id]
		if (a == nil) != (b == nil) || a != nil && !reflect.DeepEqual(*a, *b) || rows.typed[id] != fresh.typed[id] ||
			!slices.Equal(rows.sessions[id], fresh.sessions[id]) || !slices.Equal(rows.links[id], fresh.links[id]) ||
			!slices.Equal(rows.launches[id], fresh.launches[id]) || !slices.Equal(rows.origins[id], fresh.origins[id]) ||
			!reflect.DeepEqual(rows.results[id], fresh.results[id]) {
			return false
		}
	}
	for _, id := range copyScope {
		if !slices.Equal(rows.copies[id], fresh.copies[id]) {
			return false
		}
	}
	return true
}

// fetchTypedDigests digests the typed claims and evidence state of the
// conversations in scope.
func fetchTypedDigests(ctx context.Context, q queryer, scope []string, out *executionSourceRows) error {
	present, err := executionEvidenceTablesPresent(ctx, q)
	if err != nil || !present {
		return err
	}
	authority := "''"
	if columns, err := queryMapsContext(ctx, q, `SELECT 1 FROM pragma_table_info('execution_claims') WHERE name='authority'`); err != nil {
		return err
	} else if len(columns) > 0 {
		authority = "authority"
	}
	where, args := executionScope("conversation_id", scope)
	rows, err := q.QueryContext(ctx, `SELECT conversation_id,'state',version||char(0)||input||char(0)||id_policy FROM execution_evidence_state WHERE 1`+where+`
 UNION ALL SELECT conversation_id,claim_key,json_array(source,kind,subject,object,origin,`+authority+`,rule,namespace,id_kind,locator,detail_json) FROM execution_claims WHERE 1`+where+`
 ORDER BY 1,2`, append(args, args...)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	digests := map[string]*strings.Builder{}
	for rows.Next() {
		var conversation, key, value string
		if err := rows.Scan(&conversation, &key, &value); err != nil {
			return err
		}
		b := digests[conversation]
		if b == nil {
			b = &strings.Builder{}
			digests[conversation] = b
		}
		b.WriteString(key + "\x00" + value + "\x00")
	}
	for conversation, b := range digests {
		out.typed[conversation] = hashBytes([]byte(b.String()))
	}
	return rows.Err()
}

// copyInputs is what a descendant's copied launches read from conversation
// id: its place in the ancestry, namespace and launch calls. A commit that
// leaves it unchanged (new messages, results, usage) cannot change any
// descendant's copies.
func (rows *executionSourceRows) copyInputs(id string) string {
	cv := rows.conversations[id]
	if cv == nil {
		return ""
	}
	parts := []string{"present", cv.parent, cv.provider, cv.account}
	for _, row := range rows.sessions[id] {
		if row.delegation != "" {
			parts = append(parts, row.id, row.native, row.delegation, row.launch)
		}
	}
	return strings.Join(parts, "\x00")
}

// descendants lists ids and every conversation recorded below them, in either
// rows (a parent may have moved), sorted.
func executionDescendants(ids []string, rows ...*executionSourceRows) []string {
	children := map[string][]string{}
	for _, r := range rows {
		for id, cv := range r.conversations {
			if cv.parent != "" && cv.parent != id {
				children[cv.parent] = append(children[cv.parent], id)
			}
		}
	}
	seen := map[string]bool{}
	queue := slices.Clone(ids)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		queue = append(queue, children[id]...)
	}
	out := slices.Collect(maps.Keys(seen))
	sort.Strings(out)
	return out
}

// evidence assembles the rows into the evidence of one generation, then adds
// registered (typed) sources read through q.
func (rows *executionSourceRows) evidence(ctx context.Context, q queryer, generation int64) (*executionEvidence, error) {
	e := rows.assemble()
	e.Generation = generation
	for _, source := range executionClaimSources {
		if err := source(ctx, q, e); err != nil {
			return nil, err
		}
	}
	return e, nil
}

// assemble converts the stored rows into streams and claims. Its output does
// not depend on map order: the resolver normalizes claims, and streams are
// emitted sorted.
func (rows *executionSourceRows) assemble() *executionEvidence {
	e := &executionEvidence{}
	conversationRef := func(id string) sourceRef { return sourceRef{sourceConversation, id} }
	ids := slices.Sorted(maps.Keys(rows.conversations))
	mains := map[string]string{}
	claudeRoots := map[string]string{} // provider/account|native root → conversation
	for _, id := range ids {
		cv := rows.conversations[id]
		if cv.mainSession != "" {
			mains[cv.mainSession] = id
		}
		if cv.provider == "claude" && !strings.Contains(cv.native, ":subagent:") {
			claudeRoots[cv.provider+"/"+cv.account+"|"+cv.native] = id
		}
	}
	sessionRef := func(id string) sourceRef {
		if cid := mains[id]; cid != "" {
			return conversationRef(cid)
		}
		return sourceRef{sourceSession, id}
	}
	for _, id := range ids {
		cv := rows.conversations[id]
		e.Streams = append(e.Streams, executionStream{Ref: conversationRef(id), ConversationID: id, WorkspaceID: cv.workspace,
			Provider: cv.provider, Account: cv.account, NativeID: cv.native, MainSessionID: cv.mainSession, RecordedDepth: cv.depth, Subagent: cv.depth > 0,
			Nickname: cv.nickname, AgentPath: cv.path, At: cv.started, SourcePosition: -1, Observed: true, Typed: cv.typed,
			AuthoredName: cv.name, NameSource: cv.nameSource, NameRank: cv.nameRank, HumanName: cv.humanName, RequestName: cv.requestName, ContextName: cv.contextName})
		namespace := cv.provider + "/" + cv.account
		claudeRoot, familyRule := "", ""
		switch cv.provider {
		case "claude":
			if root, _, ok := strings.Cut(cv.native, ":subagent:"); ok {
				claudeRoot, familyRule = claudeRoots[namespace+"|"+root], "claude-subagents-directory"
			}
		case "copilot":
			// A Copilot agent's parent falls back to its session when the call
			// that started it was not found (adapter_copilot.go): a stored
			// session parent names the family, not necessarily the parent.
			if root := rows.conversations[cv.parent]; root != nil && cv.depth > 0 && root.parent == "" && root.depth == 0 &&
				root.provider == cv.provider && root.account == cv.account {
				claudeRoot, familyRule = cv.parent, "copilot-session-log"
			}
		}
		if claudeRoot != "" && claudeRoot != id {
			// Every agent sits under its root's record (Claude's subagents
			// directory, Copilot's session log): the family is the provider's
			// record even when the parent is not.
			e.Claims = append(e.Claims, executionClaim{Kind: claimFamilyMember, Subject: conversationRef(id), Object: conversationRef(claudeRoot),
				Strength: evidenceNative, Namespace: namespace, Rule: familyRule, Locator: conversationRef(id)})
		}
		if parent := cv.parent; parent != "" {
			switch {
			case claudeRoot != "" && parent == claudeRoot:
				// Ingestion stored the root for every agent without an accepted
				// launch edge (Claude lineage reconciliation, Copilot's session
				// fallback), so a stored root parent may be that fallback. It is
				// a hint until a launch corroborates it.
				e.Claims = append(e.Claims, executionClaim{Kind: claimTranscriptParent, Subject: conversationRef(id), Object: conversationRef(parent),
					Strength: evidenceSynthesized, Namespace: namespace, Rule: cv.provider + "-root-parent-unverified", Locator: conversationRef(id)})
			case claudeRoot != "" && cv.provider == "claude":
				// Reconciliation stores a non-root parent only for an accepted,
				// corroborated launch acknowledgement.
				e.Claims = append(e.Claims, executionClaim{Kind: claimTranscriptParent, Subject: conversationRef(id), Object: conversationRef(parent),
					Strength: evidenceDerived, Namespace: namespace, Rule: "claude-accepted-lineage", Locator: conversationRef(id)})
			// Codex records a continued (forked) conversation at depth zero with
			// its source as parent: the same execution, not a delegation.
			case cv.provider == "codex" && cv.depth == 0 && parent != id:
				e.Claims = append(e.Claims, executionClaim{Kind: claimContinuation, Subject: conversationRef(id), Object: conversationRef(parent),
					Strength: evidenceLegacy, Namespace: namespace, Rule: "codex-depth0-parent-continuation/v1", Locator: conversationRef(id)})
			default:
				e.Claims = append(e.Claims, executionClaim{Kind: claimTranscriptParent, Subject: conversationRef(id), Object: conversationRef(parent),
					Strength: evidenceLegacy, Namespace: namespace, Rule: "stored-conversation-parent", Locator: conversationRef(id)})
			}
		}
		if cv.mainParent != "" {
			e.Claims = append(e.Claims, executionClaim{Kind: claimSessionParent, Subject: conversationRef(id), Object: sessionRef(cv.mainParent),
				Strength: evidenceLegacy, Namespace: namespace, Rule: "stored-session-parent", Locator: sourceRef{sourceSession, cv.mainSession}})
		}
	}
	for _, cid := range slices.Sorted(maps.Keys(rows.sessions)) {
		cv := rows.conversations[cid]
		if cv == nil {
			cv = &executionConversationRow{}
		}
		for _, row := range rows.sessions[cid] {
			if mains[row.id] != "" || row.native == "main" {
				continue
			}
			ref := sourceRef{sourceSession, row.id}
			account := firstString(row.account, cv.account)
			provider := firstString(row.provider, cv.provider)
			e.Streams = append(e.Streams, executionStream{Ref: ref, ConversationID: cid, WorkspaceID: firstString(row.workspace, cv.workspace),
				Provider: provider, Account: account, NativeID: row.native, Subagent: row.kind == "subagent", Assignment: row.assignment, At: row.at,
				SourcePosition: row.position, Launch: row.delegation != "", Observed: row.observed, ReportedTokens: row.tokens, Typed: cv.typed})
			namespace := provider + "/" + account
			owner := conversationRef(cid)
			if row.parent != "" {
				owner = sessionRef(row.parent)
			}
			// The launch call sits in its owner's transcript: membership is the
			// provider's record, its session parent a parser derivation.
			e.Claims = append(e.Claims, executionClaim{Kind: claimLaunchOwner, Subject: ref, Object: owner, Strength: evidenceDerived,
				Namespace: namespace, Rule: "session-derivation/v1", Locator: sourceRef{sourceMessage, row.delegation}})
			if row.child != "" && row.child != cid {
				strength, rule := evidenceLegacy, "stored-child-link"
				if provider == "claude" {
					// Claude group lineage stores a child link only for a unique,
					// corroborated launch acknowledgement (linkClaudeGroupDelegations).
					strength, rule = evidenceDerived, "claude-accepted-launch-link"
				}
				e.Claims = append(e.Claims, executionClaim{Kind: claimChildLink, Subject: ref, Object: conversationRef(row.child), Strength: strength,
					Namespace: namespace, Rule: rule, Locator: ref})
			}
		}
	}
	for _, left := range slices.Sorted(maps.Keys(rows.links)) {
		for _, link := range rows.links[left] {
			if link.left == "" || link.right == "" || link.left == link.right {
				continue
			}
			// Declared native aliases name the same transcript under another ID.
			strength := evidenceLegacy
			if link.relationship == "native-alias" {
				strength = evidenceNative
			}
			e.Claims = append(e.Claims, executionClaim{Kind: claimMirror, Subject: conversationRef(link.right), Object: conversationRef(link.left), Strength: strength,
				Rule: "identity-link:" + link.relationship, Locator: conversationRef(link.right)})
		}
	}
	for _, child := range slices.Sorted(maps.Keys(rows.launches)) {
		for _, launch := range rows.launches[child] {
			if launch.child == "" || launch.parent == "" || launch.child == launch.parent {
				continue
			}
			e.Claims = append(e.Claims, executionClaim{Kind: claimCommandLaunch, Subject: conversationRef(launch.child), Object: conversationRef(launch.parent),
				Strength: evidenceDerived, Rule: "command-launch", Locator: conversationRef(launch.child)})
		}
	}
	rows.addRetainedNativeParents(e)
	rows.addLegacyLaunchResults(e)
	rows.addLegacyCopiedLaunches(e)
	return e
}

// legacyLaunchResult reads a retained launch result envelope. Only a
// recognized acknowledgement or creation failure counts: arbitrary report
// prose that mentions an agent ID is not one (audit F7).
type legacyLaunchResult struct {
	agent, path string
	failed      bool
	textual     bool // a Claude text acknowledgement, which needs corroboration
}

func readLegacyLaunchResult(text string) legacyLaunchResult {
	var payload map[string]any
	if json.Unmarshal([]byte(text), &payload) != nil {
		return legacyLaunchResult{}
	}
	h := legacyLaunchResult{failed: truthy(payload["is_error"]) || truthy(payload["error"])}
	var read func(any, int)
	read = func(value any, depth int) {
		if depth > 4 {
			return
		}
		switch v := value.(type) {
		case map[string]any:
			h.agent = firstString(h.agent, v["agentId"], v["agent_id"], v["thread_id"])
			if path := firstString(v["task_name"]); strings.HasPrefix(path, "/") {
				h.path = path
			}
			for _, key := range []string{"details", "content", "output", "text"} {
				read(v[key], depth+1)
			}
		case []any:
			for _, part := range v {
				read(part, depth+1)
			}
		case string:
			var decoded any
			if json.Unmarshal([]byte(v), &decoded) == nil {
				read(decoded, depth+1)
				return
			}
			h.failed = h.failed || strings.HasPrefix(v, "collab spawn failed:") || strings.HasPrefix(v, "agent path `") && strings.HasSuffix(v, "` already exists")
			if agent := claudeTextLaunchAgent(v); agent != "" && h.agent == "" {
				h.agent, h.textual = agent, true
			}
		}
	}
	read(payload, 0)
	h.failed = h.failed && h.agent == "" && h.path == ""
	return h
}

// readLegacyLaunchOutcome is readLegacyLaunchResult with the harness usage
// footer of a completed Agent or Task call (agentUsageFooterOf), the result
// a run that reports its own tokens returns: launch is the call's body, ""
// when unread. The footer's agent is the handle the launch created, read
// only when the result names no other handle.
func readLegacyLaunchOutcome(launch, result string) legacyLaunchResult {
	h := readLegacyLaunchResult(result)
	if h.agent == "" && h.path == "" && launch != "" {
		if agent, _, ok := agentUsageFooterOf(launch, result); ok {
			return legacyLaunchResult{agent: agent}
		}
	}
	return h
}

// legacyLaunchResultSQL selects what readLegacyLaunchOutcome reads of result
// r: its first 8 KiB, or all of it when it may end in a usage footer, whose
// validation needs the whole document.
func legacyLaunchResultSQL(r string) string {
	return `CASE WHEN ` + legacyLaunchFooterSQL(r) + ` THEN ` + r + `.text ELSE substr(` + r + `.text,1,8192) END`
}

// legacyLaunchFooterSQL is true when result r may carry a usage footer.
func legacyLaunchFooterSQL(r string) string {
	return r + `.kind='delegation_result' AND instr(` + r + `.text,'` + agentUsageFooterMarker + `')>0`
}

// executionTypedState returns an SQL expression true when the transcript in
// column has current typed evidence, and its arguments. A catalog from before
// typed evidence has none.
func executionTypedState(ctx context.Context, q queryer, column string) (string, []any, error) {
	table, err := queryMapsContext(ctx, q, `SELECT 1 FROM sqlite_master WHERE type='table' AND name='execution_evidence_state'`)
	if err != nil || len(table) == 0 {
		return "0", nil, err
	}
	return `EXISTS(SELECT 1 FROM execution_evidence_state s WHERE s.conversation_id=` + column + ` AND s.version=?)`, []any{executionEvidenceVersion}, nil
}

// fetchLegacyLaunchResults reads acknowledgements retained beside launches in
// the launching transcript, through the (conversation, call ID) index, whether
// or not a child link is stored: an acknowledgement naming another transcript
// contradicts the link, and the resolver weighs both (as it does once the same
// result is typed). The handle is read in that transcript's namespace, the
// rows dependencyLaunchResults digests.
// Transcripts whose typed evidence is current already supply structured
// launch outcomes; a second, prose-tolerant reading could only disagree, so
// they are not read.
func fetchLegacyLaunchResults(ctx context.Context, q queryer, scope []string, out *executionSourceRows) error {
	typed, args, err := executionTypedState(ctx, q, "a.conversation_id")
	if err != nil {
		return err
	}
	current := ` AND NOT ` + typed
	where, scoped := executionScope("a.conversation_id", scope)
	rows, err := q.QueryContext(ctx, `SELECT a.id,a.conversation_id,r.id result_id,`+legacyLaunchResultSQL("r")+` text,
 CASE WHEN `+legacyLaunchFooterSQL("r")+` THEN d.text ELSE '' END launch FROM agent_sessions a
 JOIN messages d ON d.id=a.delegation_message_id
 JOIN messages r ON r.conversation_id=a.conversation_id AND r.call_id=d.call_id AND r.call_id<>''
 WHERE a.native_id<>'main' AND d.call_id<>''
 AND r.kind IN ('delegation_result','tool_result')`+current+where+` ORDER BY a.id,r.id`, append(args, scoped...)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var launch, conversation, result, text, call string
		if err := rows.Scan(&launch, &conversation, &result, &text, &call); err != nil {
			return err
		}
		h := readLegacyLaunchOutcome(call, text)
		if h.agent == "" && h.path == "" && !h.failed {
			continue
		}
		out.results[conversation] = append(out.results[conversation], executionResultRow{launch: launch, result: result, handle: h})
	}
	return rows.Err()
}

// addLegacyLaunchResults turns parsed results into launch outcomes. Handles
// match only inside the launch's provider/account namespace: a task path among
// the launching transcript's children, a native thread ID, or a Claude agent
// file under the same root.
func (rows *executionSourceRows) addLegacyLaunchResults(e *executionEvidence) {
	if len(rows.results) == 0 {
		return
	}
	type key struct{ provider, account, native string }
	byNative := map[key][]string{}
	byPath := map[[2]string][]string{} // parent conversation, agent path
	// Claude agent files live under their root: <root>:subagent:[…/]agent-<id>.
	claudeAgents := map[[3]string][]string{} // provider/account, root native, agent id
	for _, id := range slices.Sorted(maps.Keys(rows.conversations)) {
		cv := rows.conversations[id]
		byNative[key{cv.provider, cv.account, cv.native}] = append(byNative[key{cv.provider, cv.account, cv.native}], id)
		if cv.path != "" && cv.parent != "" {
			byPath[[2]string{cv.parent, cv.path}] = append(byPath[[2]string{cv.parent, cv.path}], id)
		}
		if root, rest, ok := strings.Cut(cv.native, ":subagent:"); ok {
			name := rest[strings.LastIndexAny(rest, "/:")+1:]
			if agent, ok := strings.CutPrefix(name, "agent-"); ok && agent != "" {
				k := [3]string{cv.provider + "/" + cv.account, root, agent}
				claudeAgents[k] = append(claudeAgents[k], id)
			}
		}
	}
	for _, cid := range slices.Sorted(maps.Keys(rows.results)) {
		source := rows.conversations[cid]
		if source == nil {
			continue
		}
		byLaunch := map[string][]executionResultRow{}
		for _, row := range rows.results[cid] {
			byLaunch[row.launch] = append(byLaunch[row.launch], row)
		}
		for _, id := range slices.Sorted(maps.Keys(byLaunch)) {
			hs := byLaunch[id]
			h := hs[0].handle
			consistent := true
			for _, other := range hs[1:] {
				consistent = consistent && other.handle == h
			}
			// Conflicting acknowledgements for one launch identify nothing.
			if !consistent {
				continue
			}
			launch, locator := sourceRef{sourceSession, id}, sourceRef{sourceMessage, hs[0].result}
			namespace := source.provider + "/" + source.account
			if h.failed {
				e.Claims = append(e.Claims, executionClaim{Kind: claimLaunchRejected, Subject: launch, Strength: evidenceDerived, Namespace: namespace,
					Rule: "legacy-launch-failure/v1", Locator: locator})
				continue
			}
			var matches []string
			idKind := idKindNativeEvent
			switch {
			case h.path != "":
				idKind = "task-path"
				for _, m := range byPath[[2]string{cid, h.path}] {
					if child := rows.conversations[m]; child.provider == source.provider && child.account == source.account {
						matches = append(matches, m)
					}
				}
			case h.agent != "":
				matches = append(matches, byNative[key{source.provider, source.account, h.agent}]...)
				root, _, _ := strings.Cut(source.native, ":subagent:")
				for _, m := range claudeAgents[[3]string{namespace, root, h.agent}] {
					if !slices.Contains(matches, m) {
						matches = append(matches, m)
					}
				}
			}
			sort.Strings(matches)
			switch {
			case h.textual:
				// Claude ingestion already judged its text acknowledgements against
				// the child's assignment and stored accepted ones as child links.
			case len(matches) == 1 && matches[0] != cid:
				e.Claims = append(e.Claims, executionClaim{Kind: claimLaunchAccepted, Subject: launch, Object: sourceRef{sourceConversation, matches[0]},
					Strength: evidenceDerived, Namespace: namespace, IDKind: idKind, Rule: "legacy-launch-acknowledgement/v1", Locator: locator})
			case len(matches) == 0:
				// A returned handle establishes creation even when the child
				// transcript was not retained.
				e.Claims = append(e.Claims, executionClaim{Kind: claimLaunchAccepted, Subject: launch, Strength: evidenceDerived, Namespace: namespace,
					IDKind: idKind, Rule: "legacy-launch-acknowledgement/v1", Locator: locator})
			}
			// Several matches stay unresolved: the launch is an attempt whose
			// identity the evidence cannot decide.
		}
	}
}

// fetchLegacyCopiedLaunches finds launch calls a transcript copied from an
// ancestor (a fork retains its parent's history, including the launch that
// created it). Within one provider/account, the copy must repeat the
// original's call ID, role, kind and full body anywhere up the recorded
// ancestry: an intermediate transcript may have compacted the call away.
// A copy with its own reported usage is not matched; it is an execution.
// Ancestry that returns to the copy's transcript (a self-parent or a cycle)
// proves no original, so a launch there is never a copy (it stays its own
// stream), and no launch matches itself.
func fetchLegacyCopiedLaunches(ctx context.Context, q queryer, scope []string, out *executionSourceRows) error {
	where, args := executionScope("a.conversation_id", scope)
	rows, err := queryMapsContext(ctx, q, `WITH RECURSIVE copies AS MATERIALIZED (
 SELECT a.id,a.conversation_id,a.native_id,a.delegation_message_id FROM agent_sessions a
 JOIN conversations c ON c.id=a.conversation_id
 WHERE a.native_id<>'main' AND a.total_tokens=0 AND a.delegation_message_id IS NOT NULL AND c.parent_id IS NOT NULL`+where+`
), ancestors(conversation_id,ancestor_id,depth) AS (
 SELECT DISTINCT c.id,c.parent_id,1 FROM conversations c WHERE c.id IN (SELECT conversation_id FROM copies)
 UNION SELECT a.conversation_id,p.parent_id,a.depth+1 FROM ancestors a JOIN conversations p ON p.id=a.ancestor_id
 WHERE p.parent_id IS NOT NULL AND a.ancestor_id<>a.conversation_id AND a.depth<32
)
SELECT copy.id copy_id,copy.conversation_id,original.id original_id,an.depth,m.id message_id,c.provider,c.account FROM copies copy
 JOIN ancestors an ON an.conversation_id=copy.conversation_id
 JOIN conversations c ON c.id=copy.conversation_id
 JOIN conversations p ON p.id=an.ancestor_id AND p.provider=c.provider AND p.account=c.account
 JOIN agent_sessions original ON original.conversation_id=p.id AND original.native_id=copy.native_id
 JOIN messages m ON m.id=copy.delegation_message_id
 JOIN messages prior ON prior.id=original.delegation_message_id
 WHERE m.call_id<>'' AND m.call_id=prior.call_id AND m.role=prior.role AND m.kind=prior.kind AND m.text=prior.text
 AND original.id<>copy.id AND NOT EXISTS(SELECT 1 FROM ancestors z WHERE z.conversation_id=copy.conversation_id AND z.ancestor_id=copy.conversation_id)
 ORDER BY copy.id,an.depth,original.id`, args...)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, row := range rows {
		id := firstString(row["copy_id"])
		if seen[id] {
			continue
		}
		seen[id] = true
		cid := firstString(row["conversation_id"])
		out.copies[cid] = append(out.copies[cid], executionCopyRow{copy: id, original: firstString(row["original_id"]), message: firstString(row["message_id"]),
			provider: firstString(row["provider"]), account: firstString(row["account"])})
	}
	return nil
}

func (rows *executionSourceRows) addLegacyCopiedLaunches(e *executionEvidence) {
	for _, cid := range slices.Sorted(maps.Keys(rows.copies)) {
		for _, row := range rows.copies[cid] {
			// The nearest retained ancestor copy; chains resolve to the original.
			e.Claims = append(e.Claims, executionClaim{Kind: claimInheritedCopy, Subject: sourceRef{sourceSession, row.copy},
				Object: sourceRef{sourceSession, row.original}, Strength: evidenceDerived, Namespace: row.provider + "/" + row.account, IDKind: idKindCallID,
				Rule: "ancestor-launch-copy/v1", Locator: sourceRef{sourceMessage, row.message}})
		}
	}
}

// fetchRetainedNativeParents reads the parent native IDs origin observations
// retained from the source.
func fetchRetainedNativeParents(ctx context.Context, q queryer, scope []string, out *executionSourceRows) error {
	where, args := executionScope("conversation_id", scope)
	rows, err := queryMapsContext(ctx, q, `SELECT o.conversation_id,o.parent_native_id,o.parent_provider,o.parent_account,o.evidence_key,c.provider,w.source_kind
 FROM origin_observations o JOIN conversations c ON c.id=o.conversation_id LEFT JOIN workspaces w ON w.id=c.workspace_id
 WHERE o.parent_native_id IS NOT NULL AND o.parent_native_id<>''`+strings.ReplaceAll(where, "conversation_id", "o.conversation_id")+` ORDER BY o.conversation_id,o.evidence_key`, args...)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil
		}
		return err
	}
	for _, o := range rows {
		// As retainedParentNamespace (execution_evidence.go) decides it.
		provider := firstString(o["provider"])
		parts := strings.SplitN(firstString(o["evidence_key"]), "|", 3)
		row := executionOriginRow{firstString(o["conversation_id"]), firstString(o["parent_native_id"]), firstString(o["parent_provider"]), firstString(o["parent_account"]),
			len(parts) == 3 && parts[1] == provider || firstString(o["source_kind"]) == provider}
		out.origins[row.conversation] = append(out.origins[row.conversation], row)
	}
	return nil
}

// addRetainedNativeParents adds retained parent native IDs, so a child that
// arrived before its parent resolves once the parent does. A wrapper's
// declared alias names a transcript only when no other wrapper claims it; an
// unknown parent stays an unresolved reference ("missing:"), which leaves the
// parent missing.
func (rows *executionSourceRows) addRetainedNativeParents(e *executionEvidence) {
	if len(rows.origins) == 0 {
		return
	}
	native := map[string]string{}
	byNative := map[string][]string{} // native ID or alias → conversations, any namespace
	claims, ambiguous := map[string]string{}, map[string]bool{}
	for _, id := range slices.Sorted(maps.Keys(rows.conversations)) {
		cv := rows.conversations[id]
		native[cv.provider+"|"+cv.account+"|"+cv.native] = id
		byNative[cv.native] = append(byNative[cv.native], id)
		for _, alias := range cv.aliases {
			if alias != cv.native {
				byNative[alias] = append(byNative[alias], id)
			}
			key := cv.provider + "|" + cv.account + "|" + alias
			if prior := claims[key]; prior != "" && prior != id {
				ambiguous[key] = true
			}
			claims[key] = id
		}
	}
	for key, id := range claims {
		if native[key] == "" && !ambiguous[key] {
			native[key] = id
		}
	}
	for _, id := range slices.Sorted(maps.Keys(rows.origins)) {
		cv := rows.conversations[id]
		if cv == nil {
			continue
		}
		for _, o := range rows.origins[id] {
			// Claude's stored parent within Claude is written after lineage
			// reconciliation, which assigns the root as a fallback: a past
			// decision, not the provider's record. A parent another provider
			// recorded (a Codex thread that launched Claude) is not one.
			if cv.provider == "claude" && (o.parentProvider == "" || o.parentProvider == "claude") {
				continue
			}
			// The parent's namespace: recorded with the observation, or the
			// conversation's own when its own provider's adapter named it.
			// Otherwise it was never recorded, and the native ID names the
			// parent only where it is unique across namespaces; several
			// matches stay equally plausible (ambiguous).
			var objects []string
			key := firstString(o.parentProvider, cv.provider) + "|" + firstString(o.parentAccount, cv.account) + "|" + o.parentNative
			rule := "retained-native-parent"
			if o.parentProvider != "" || o.sameSource {
				if parent := native[key]; parent != "" {
					objects = []string{parent}
				}
			} else {
				rule, key = "retained-parent-unrecorded-namespace", "?|?|"+o.parentNative
				objects = slices.Compact(slices.Sorted(slices.Values(byNative[o.parentNative])))
			}
			if len(objects) == 0 {
				objects = []string{"missing:" + key}
			}
			// A depth-0 parent field names a fork or continuation source;
			// typed evidence declares which, and outranks this reading.
			for _, object := range objects {
				e.Claims = append(e.Claims, executionClaim{Kind: claimTranscriptParent, Subject: sourceRef{sourceConversation, id}, Object: sourceRef{sourceConversation, object},
					Strength: evidenceLegacy, Namespace: cv.provider + "/" + cv.account, IDKind: idKindNativeEvent,
					Rule: rule, Locator: sourceRef{sourceConversation, id}})
			}
		}
	}
}
