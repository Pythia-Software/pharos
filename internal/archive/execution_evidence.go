package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Typed execution evidence (docs/execution-resolver-design.md, "Evidence
// extraction"; docs/execution-evidence-backfill.md). Ingestion records what a
// provider asserted, or what a named parser rule derived, about launches,
// parents and copied history in execution_claims, before reconciliation or
// filtering discards the distinction. The resolver reads these claims; it
// never reads the graph an older ingest materialized as if it were proof.

// executionEvidenceVersion names the extraction rules below. A conversation
// whose execution_evidence_state carries another version has claims of
// unknown completeness, and is re-derived (from retained rows where that is
// enough; see docs/execution-evidence-backfill.md). v2 records authority
// apart from origin (parsed envelopes are derived) and marks Codex
// forked_from history, both derivable from retained rows. v3 records
// Copilot session-parent fallbacks and bounds Cursor copies whose proof is
// partial (a provider read; ingestStillComplete). v4 accepts a launch whose
// result ends in a validated harness usage footer (agentUsageFooterOf),
// derivable from retained rows. v5 records Workflow run acknowledgements:
// one parent for each immediate member, never one shared child execution.
const executionEvidenceVersion = "execution-evidence-v5"

// Persisted claim kinds. Subject is relative to the owning conversation
// ("conversation", "launch:<call id>", "composer:<id>"); Object is a lookup
// key (executionNativeKey, claudeAgentKey, "task-path:<path>") or empty.
const (
	// The conversation's parent as the provider recorded it, or as Claude's
	// group lineage derived or (synthesized) fell back to.
	evidenceParent = "parent"
	// A launch call's outcome: detail.outcome accepted, rejected or unknown.
	evidenceLaunchOutcome = "launch_outcome"
	// A Workflow acknowledgement names a run containing several child agents.
	evidenceWorkflowLaunch = "workflow_launch"
	// The conversation begins with a copy of Object's history; detail gives
	// the boundary and how its observations map to the original's.
	evidenceHistoryCopy = "history_copy"
	// The conversation was rebuilt from another's copy of it: its messages
	// are surviving copies whose original source is gone.
	evidenceCopyReconstruction = "copy_reconstruction"
	// A source record ingestion left out as a copy of another (Subject),
	// retained here so the omission stays inspectable.
	evidenceSuppressedRecord = "suppressed_record"
)

// Writers of execution_claims. Each replaces only its own rows.
const (
	evidenceSourceRecord  = "record"       // derived from the conversation's own record or rows
	evidenceSourceAdapter = "adapter"      // supplied by the adapter before it filtered the source
	evidenceSourceClaude  = "claude-group" // Claude group lineage (linkClaudeGroupDelegations)
)

// EvidenceRecord is a claim an adapter records on a ConversationRecord before
// it filters or reconciles what the source held. Origin is native, derived,
// legacy or synthesized (evidenceStrength.String()).
type EvidenceRecord struct {
	Kind    string `json:"Kind"`
	Subject string `json:"Subject"`
	Object  string `json:"Object,omitempty"`
	Origin  string `json:"Origin"`
	// Authority is what the evidence establishes, apart from how it was
	// extracted (evidenceAuthority): a parsed envelope is derived but can
	// verify creation. Empty takes the default for Origin and Rule.
	Authority string `json:"Authority,omitempty"`
	Rule      string `json:"Rule"`
	IDKind    string `json:"IDKind,omitempty"`
	// Namespace scopes the identifiers the claim names; empty means the
	// owning conversation's provider|account.
	Namespace string         `json:"Namespace,omitempty"`
	Locator   string         `json:"Locator,omitempty"`
	Detail    map[string]any `json:"Detail,omitempty"`
}

// executionNativeKey is the lookup key of a transcript's native identity in
// its provider/account namespace.
func executionNativeKey(provider, account, native string) string {
	if native == "" {
		return ""
	}
	return "native:" + provider + "|" + account + "|" + native
}

// claudeAgentKey is the key a Claude launch acknowledgement's agentId names:
// the subagent file agent-<id> under one root session. It never matches
// outside that provider, account and root (unlike a bare suffix).
func claudeAgentKey(account, root, agent string) string {
	if agent == "" || root == "" {
		return ""
	}
	return "claude-agent:" + account + "|" + root + "|" + agent
}

// executionLookupKeys are the keys a conversation answers to.
func executionLookupKeys(provider, account, native string, aliases []string) []string {
	keys := []string{executionNativeKey(provider, account, native)}
	for _, alias := range aliases {
		if alias != "" && alias != native {
			keys = append(keys, executionNativeKey(provider, account, alias))
		}
	}
	if provider == "claude" {
		if root, run, ok := claudeWorkflowOf(native); ok {
			keys = append(keys, claudeWorkflowKey(account, root, run))
		}
		if root, rest, found := strings.Cut(native, ":subagent:"); found {
			// The agent file's name, as the legacy launch-result reading
			// takes it: after the last path or nesting separator.
			name := rest[strings.LastIndexAny(rest, "/:")+1:]
			if agent, ok := strings.CutPrefix(name, "agent-"); ok && agent != "" {
				keys = append(keys, claudeAgentKey(account, root, agent))
			}
		}
	}
	return keys
}

// executionIDPolicy declares how an extractor names messages, so a reader
// can tell a provider's event ID from a position it synthesized. Only Codex
// and TL1 synthesize: "line-N" for an event without an ID, where N is the
// line its evidence locator names.
func executionIDPolicy(provider string) string {
	switch provider {
	case "codex", "tl1":
		return "line-locator/v1"
	case "claude", "cursor":
		return "provider-event/v1"
	}
	return "unclassified/v1"
}

// messageIDKind classifies a stored message's native ID: a provider event ID
// (matchable within its namespace), a synthesized locator (locates one
// observation in one source revision; never proves equivalence) or unknown.
// The locator test is recoverable from retained rows: the adapter writes the
// line into both native_id and evidence_locator.
func messageIDKind(policy, nativeID, locator string) string {
	switch policy {
	case "line-locator/v1":
		if synthesizedLineID(nativeID, locator) {
			return idKindLocator
		}
		return idKindNativeEvent
	case "provider-event/v1":
		return idKindNativeEvent
	}
	if synthesizedLineID(nativeID, locator) {
		return idKindLocator
	}
	return idKindUnknown
}

func synthesizedLineID(nativeID, locator string) bool {
	rest, ok := strings.CutPrefix(nativeID, "line-")
	if !ok {
		return false
	}
	digits := rest
	if cut := strings.IndexByte(rest, ':'); cut >= 0 {
		digits = rest[:cut]
	}
	if _, err := strconv.Atoi(digits); err != nil || digits == "" {
		return false
	}
	// A locator ending in the same line corroborates the synthesis; a legacy
	// row without one still uses the declared shape.
	return locator == "" || strings.HasSuffix(locator, ":"+digits)
}

// launchOutcome is what a launch call's result envelope says about creation.
type launchOutcome struct {
	Outcome string // accepted, rejected, unknown
	Handle  string // agent/thread ID the envelope returned
	Path    string // a Codex task path (unique within the parent)
	Origin  evidenceStrength
	Rule    string
}

// parseLaunchOutcome reads a delegation result envelope. Only structured
// fields and harness acknowledgement formats count: report prose mentioning
// agentId: is not an acknowledgement (audit F7). Error fields in a decoded
// subagent report describe the task, so only the envelope's mean refusal.
func parseLaunchOutcome(provider, text string) launchOutcome {
	return parseLaunchOutcomeFor(provider, "", text)
}

// parseLaunchOutcomeFor is parseLaunchOutcome with the launch call's body
// (launch, "" when unknown), which a harness usage footer needs: the result
// of a completed Agent or Task call that names the agent it ran and reports
// its tokens (agentUsageFooterOf) proves creation.
func parseLaunchOutcomeFor(provider, launch, text string) launchOutcome {
	var payload map[string]any
	if json.Unmarshal([]byte(text), &payload) != nil {
		return launchOutcome{Outcome: "unknown"}
	}
	failed := truthy(payload["is_error"]) || truthy(payload["error"])
	// A rule reads the envelope, so the reading is derived; a structured
	// handle or refusal still verifies the outcome (evidenceAuthority).
	out := launchOutcome{Origin: evidenceDerived, Rule: "launch-envelope/v1"}
	var read func(any, int)
	read = func(value any, depth int) {
		if depth > 4 {
			return
		}
		switch v := value.(type) {
		case map[string]any:
			out.Handle = firstString(out.Handle, v["agentId"], v["agent_id"], v["thread_id"])
			if path := firstString(v["task_name"]); strings.HasPrefix(path, "/") && out.Path == "" {
				out.Path = path
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
			failed = failed || strings.HasPrefix(v, "collab spawn failed:") || (strings.HasPrefix(v, "agent path `") && strings.HasSuffix(v, "` already exists"))
		}
	}
	read(payload, 0)
	if out.Handle == "" && out.Path == "" && provider == "claude" && !failed {
		// The asynchronous launch acknowledgement some Claude versions keep
		// only as text. It proves creation; binding it to a transcript also
		// needs the assignment match the group lineage checks.
		if agent := claudeTextLaunchAgent(payload["content"]); agent != "" {
			out.Handle, out.Origin, out.Rule = agent, evidenceDerived, "claude-async-text-ack/v1"
		}
	}
	if out.Handle == "" && out.Path == "" && !failed && launch != "" {
		if agent, _, ok := agentUsageFooterOf(launch, text); ok {
			out.Handle, out.Origin, out.Rule = agent, evidenceDerived, "harness-usage-footer/v1"
		}
	}
	switch {
	case out.Handle != "" || out.Path != "":
		// A returned handle establishes creation even if the task failed.
		out.Outcome = "accepted"
	case failed:
		out.Outcome = "rejected"
	default:
		out.Outcome, out.Origin, out.Rule = "unknown", evidenceDerived, "launch-envelope/v1"
	}
	return out
}

// conversationExecutionEvidence derives a conversation's own claims from its
// record: the provider's parent field, each launch's outcome, and copied
// history its retained session metadata declares. It reads only what is
// retained in messages and conversation_parent_refs as well, so the same
// function re-derives them from stored rows. Claude's group lineage (whose
// stored parent may be a fallback) is written by writeClaudeLineageEvidence.
func conversationExecutionEvidence(conversation ConversationRecord) []EvidenceRecord {
	claims := conversationWorkflowEvidence(conversation)
	namespace := conversation.Provider + "/" + conversation.Account
	// Claude's own parent field is written after group lineage chose it
	// (writeClaudeLineageEvidence records that); a parent another provider
	// recorded explicitly (an export naming a Codex parent) is that
	// provider's record, a typed mixed-provider relationship.
	explicitOther := conversation.ParentProvider != "" && conversation.ParentProvider != conversation.Provider
	// An adapter that defaulted the parent field says so (sessionParentFallback):
	// the field is then its default, not the provider's record.
	if conversation.ParentNativeID != "" && (conversation.Provider != "claude" || explicitOther) && !parentFallback(conversation.Evidence) {
		provider, account := defaultString(conversation.ParentProvider, conversation.Provider), defaultString(conversation.ParentAccount, conversation.Account)
		claims = append(claims, EvidenceRecord{Kind: evidenceParent, Subject: "conversation", Object: executionNativeKey(provider, account, conversation.ParentNativeID),
			Origin: evidenceNative.String(), Rule: "provider-parent-field/v1", IDKind: idKindNativeEvent,
			Detail: map[string]any{"agent_depth": conversation.AgentDepth, "agent_path": conversation.AgentPath}})
	}
	launches := map[string]MessageRecord{}
	order := []string{}
	results := map[string][]MessageRecord{}
	for _, message := range conversation.Messages {
		switch {
		case message.Kind == "delegation" && message.CallID != "":
			if _, seen := launches[message.CallID]; !seen {
				order = append(order, message.CallID)
			}
			launches[message.CallID] = message
		case (message.Kind == "delegation_result" || message.Kind == "tool_result") && message.CallID != "":
			results[message.CallID] = append(results[message.CallID], message)
		case message.Kind == "metadata" && conversation.Provider == "codex":
			if claim, ok := codexHistoryEvidence(conversation, message); ok {
				claims = append(claims, claim)
			}
		}
	}
	for _, call := range order {
		launch := launches[call]
		outcomes := map[string]launchOutcome{}
		resultIDs := []string{}
		for _, result := range results[call] {
			if result.Kind != "delegation_result" && conversation.Provider != "codex" {
				continue
			}
			// Only a delegation result can carry a usage footer, as in
			// conversationTokenEvidence and the stored-row reading; a Codex
			// tool_result keeps its structured acknowledgements.
			body := ""
			if result.Kind == "delegation_result" {
				body = launch.Text
			}
			parsed := parseLaunchOutcomeFor(conversation.Provider, body, result.Text)
			resultIDs = append(resultIDs, result.NativeID)
			outcomes[parsed.Outcome+"\x00"+parsed.Handle+"\x00"+parsed.Path] = parsed
		}
		keys := mapsKeys(outcomes)
		slices.Sort(keys)
		known := []launchOutcome{}
		for _, key := range keys {
			if outcomes[key].Outcome != "unknown" {
				known = append(known, outcomes[key])
			}
		}
		locator := "message:" + launch.NativeID
		if len(known) == 0 {
			detail := map[string]any{"outcome": "unknown", "results": len(resultIDs)}
			claims = append(claims, EvidenceRecord{Kind: evidenceLaunchOutcome, Subject: "launch:" + call, Origin: evidenceDerived.String(), Rule: "launch-envelope/v1",
				IDKind: idKindCallID, Locator: locator, Detail: detail})
			continue
		}
		for _, outcome := range known {
			object := ""
			switch {
			case outcome.Path != "":
				object = "task-path:" + outcome.Path
			case outcome.Handle != "" && conversation.Provider == "claude":
				// A text acknowledgement or usage footer leaves the transcript
				// to the group lineage, which checks the assignment.
				if outcome.Rule == "launch-envelope/v1" {
					object = claudeAgentKey(conversation.Account, claudeRootNative(conversation.NativeID), outcome.Handle)
				}
			case outcome.Handle != "":
				object = executionNativeKey(conversation.Provider, conversation.Account, outcome.Handle)
			}
			detail := map[string]any{"outcome": outcome.Outcome, "results": len(resultIDs)}
			if outcome.Handle != "" {
				detail["handle"] = outcome.Handle
			}
			if outcome.Path != "" {
				detail["path"] = outcome.Path
			}
			if len(known) > 1 {
				// Conflicting envelopes for one call: every reading stays, and
				// resolution reports the launch ambiguous.
				detail["conflicting"] = len(known)
			}
			claims = append(claims, EvidenceRecord{Kind: evidenceLaunchOutcome, Subject: "launch:" + call, Object: object, Origin: outcome.Origin.String(), Rule: outcome.Rule,
				IDKind: idKindCallID, Namespace: namespace, Locator: locator, Detail: detail})
		}
	}
	return claims
}

// sessionParentFallback is the parent claim of an agent whose adapter could
// not find the call that started it and stored its session as the parent
// (Copilot CLI and VS Code Copilot logs): the session names the agent's
// family, and is at most a hint about its parent.
func sessionParentFallback(provider, account, session, reason string) EvidenceRecord {
	return EvidenceRecord{Kind: evidenceParent, Subject: "conversation", Object: executionNativeKey(provider, account, session),
		Origin: evidenceSynthesized.String(), Authority: authorityFallback, Rule: "copilot-session-fallback/v1", Detail: map[string]any{"reason": reason}}
}

func parentFallback(evidence []EvidenceRecord) bool {
	return slices.ContainsFunc(evidence, func(claim EvidenceRecord) bool {
		return claim.Kind == evidenceParent && evidenceAuthority(claim) == authorityFallback
	})
}

// sessionParentProviders store an agent's session as its parent when the
// starting call is missing. Before their adapters recorded that
// (copilotExtractor v3, vscodeCopilotExtractor v3), a retained session parent
// cannot be told from a real one.
var sessionParentProviders = map[string]bool{"copilot": true, "vscode-copilot": true}

func mapsKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

func claudeRootNative(native string) string {
	root, _, _ := strings.Cut(native, ":subagent:")
	return root
}

// codexHistoryEvidence reads the session_meta note the Codex adapter keeps
// for a rollout that continues or forks another thread's history.
func codexHistoryEvidence(conversation ConversationRecord, message MessageRecord) (EvidenceRecord, bool) {
	var note struct {
		Type    string         `json:"type"`
		Payload map[string]any `json:"payload"`
	}
	if json.Unmarshal([]byte(message.Text), &note) != nil || note.Type != "session_meta" || note.Payload == nil {
		return EvidenceRecord{}, false
	}
	if !continuesHistory(note.Payload) {
		return EvidenceRecord{}, false
	}
	detail := map[string]any{}
	for _, key := range []string{"forked_from_ordinal_exclusive", "subagent_history_start_ordinal"} {
		if value, ok := number(note.Payload[key]); ok {
			detail[key] = int64(value)
		}
	}
	if base := mapValue(note.Payload["history_base"]); base != nil {
		detail["history_base"] = base
	}
	// The boundary is an ordinal mapping only when the note gives one; a
	// history base alone says that history was copied, not which items.
	_, ordinal := detail["forked_from_ordinal_exclusive"]
	_, start := detail["subagent_history_start_ordinal"]
	detail["mapping"] = map[bool]string{true: "ordinal", false: "unmapped"}[ordinal || start]
	object := ""
	if from := firstString(note.Payload["forked_from_id"]); from != "" {
		object = executionNativeKey(conversation.Provider, conversation.Account, from)
		// The thread this one was forked from, as opposed to the parent
		// field's delegating thread.
		detail["forked_from"] = true
	} else if conversation.ParentNativeID != "" {
		object = executionNativeKey(conversation.Provider, conversation.Account, conversation.ParentNativeID)
	}
	return EvidenceRecord{Kind: evidenceHistoryCopy, Subject: "conversation", Object: object, Origin: evidenceNative.String(), Rule: "codex-session-meta/v1",
		IDKind: idKindNativeEvent, Locator: "message:" + message.NativeID, Detail: detail}, true
}

// Claim authorities: what a claim establishes, recorded beside its origin.
const (
	authorityVerified     = "verified"     // the provider's record of the fact, or an envelope that verifies it
	authorityCorroborated = "corroborated" // a derived reading another observation confirms
	authorityInferred     = "inferred"     // a derived reading without confirmation
	authorityUnverified   = "unverified"   // a stored decision of unknown provenance
	authorityFallback     = "fallback"     // an extractor's default; a hint, never proof
)

// evidenceAuthority is a claim's authority: its own when set, else what its
// origin and rule establish. Origin says how a claim was extracted; a
// verified acknowledgement parsed by a rule is derived and verified, so it
// is not weaker than recorded continuation metadata that contradicts it.
func evidenceAuthority(claim EvidenceRecord) string {
	if claim.Authority != "" {
		return claim.Authority
	}
	switch claim.Origin {
	case "native":
		return authorityVerified
	case "legacy":
		return authorityUnverified
	case "synthesized":
		return authorityFallback
	}
	switch claim.Rule {
	case "launch-envelope/v1":
		if outcome := firstString(claim.Detail["outcome"]); outcome == "accepted" || outcome == "rejected" {
			return authorityVerified
		}
	case "claude-async-text-ack/v1", "claude-launch-ack/v1":
		// The acknowledgement verifies creation (and, in group lineage, the
		// launch it names); binding a text acknowledgement to a transcript
		// needs the assignment as well.
		return authorityVerified
	case "claude-async-text-ack+assignment/v1":
		return authorityCorroborated
	}
	return authorityInferred
}

// evidenceClaimKey identifies a claim within its conversation: the same
// claim from the same writer always gets the same key, so a re-derivation is
// idempotent and only a real change bumps the evidence generation.
func evidenceClaimKey(source string, claim EvidenceRecord) string {
	detail := ""
	if claim.Kind == evidenceLaunchOutcome {
		// One row per reading of a launch.
		detail = firstString(claim.Detail["outcome"]) + "\x00" + firstString(claim.Detail["handle"]) + "\x00" + firstString(claim.Detail["path"])
	}
	return hashBytes([]byte(strings.Join([]string{source, claim.Kind, claim.Subject, claim.Object, claim.Rule, detail}, "\x00")))[:24]
}

// replaceExecutionClaims makes source's claims on conversation exactly
// claims. Unchanged rows are left alone; replace=false only adds (a copy
// from a host that does not own the conversation never retracts).
func replaceExecutionClaims(tx *sql.Tx, conversationID, source, namespace string, claims []EvidenceRecord, replace bool) error {
	keep := map[string]bool{}
	for _, claim := range claims {
		key := evidenceClaimKey(source, claim)
		keep[key] = true
		claimNamespace := defaultString(claim.Namespace, namespace)
		detail := "{}"
		if len(claim.Detail) > 0 {
			detail = jsonText(claim.Detail)
		}
		conflict := `ON CONFLICT(conversation_id,claim_key) DO UPDATE SET kind=excluded.kind,subject=excluded.subject,object=excluded.object,origin=excluded.origin,
		 authority=excluded.authority,rule=excluded.rule,namespace=excluded.namespace,id_kind=excluded.id_kind,locator=excluded.locator,detail_json=excluded.detail_json
		 WHERE (kind,subject,object,origin,authority,rule,namespace,id_kind,locator,detail_json) IS NOT
		 (excluded.kind,excluded.subject,excluded.object,excluded.origin,excluded.authority,excluded.rule,excluded.namespace,excluded.id_kind,excluded.locator,excluded.detail_json)`
		if !replace {
			conflict = `ON CONFLICT(conversation_id,claim_key) DO NOTHING`
		}
		if _, err := tx.Exec(`INSERT INTO execution_claims(conversation_id,claim_key,source,kind,subject,object,origin,authority,rule,namespace,id_kind,locator,detail_json)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) `+conflict, conversationID, key, source, claim.Kind, claim.Subject, claim.Object, claim.Origin, evidenceAuthority(claim), claim.Rule,
			claimNamespace, claim.IDKind, claim.Locator, detail); err != nil {
			return err
		}
	}
	if !replace {
		return nil
	}
	rows, err := tx.Query(`SELECT claim_key FROM execution_claims WHERE conversation_id=? AND source=?`, conversationID, source)
	if err != nil {
		return err
	}
	stale := []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return err
		}
		if !keep[key] {
			stale = append(stale, key)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, key := range stale {
		if _, err := tx.Exec(`DELETE FROM execution_claims WHERE conversation_id=? AND claim_key=?`, conversationID, key); err != nil {
			return err
		}
	}
	return nil
}

// writeExecutionEvidence records a conversation's claims as the writer of
// its record: its own derived claims and those its adapter retained before
// filtering. input is 'ingest' for a provider record, 'retained' for stored
// rows. A host that only merges messages into a conversation it does not
// own adds the adapter's claims and re-derives the rest from what is stored.
func writeExecutionEvidence(tx *sql.Tx, conversationID string, conversation ConversationRecord, input string, writer bool) error {
	namespace := conversation.Provider + "/" + conversation.Account
	if !writer {
		if err := replaceExecutionClaims(tx, conversationID, evidenceSourceAdapter, namespace, conversation.Evidence, false); err != nil {
			return err
		}
		return backfillExecutionEvidence(context.Background(), tx, []string{conversationID}, false)
	}
	if err := replaceExecutionClaims(tx, conversationID, evidenceSourceRecord, namespace, conversationExecutionEvidence(conversation), true); err != nil {
		return err
	}
	if err := replaceExecutionClaims(tx, conversationID, evidenceSourceAdapter, namespace, conversation.Evidence, true); err != nil {
		return err
	}
	return recordExecutionEvidenceState(tx, conversationID, input, executionIDPolicy(conversation.Provider))
}

func recordExecutionEvidenceState(tx *sql.Tx, conversationID, input, policy string) error {
	_, err := tx.Exec(`INSERT INTO execution_evidence_state(conversation_id,version,input,id_policy) VALUES(?,?,?,?)
	 ON CONFLICT(conversation_id) DO UPDATE SET version=excluded.version,input=excluded.input,id_policy=excluded.id_policy
	 WHERE (version,input,id_policy) IS NOT (excluded.version,excluded.input,excluded.id_policy)`, conversationID, executionEvidenceVersion, input, policy)
	return err
}

// claudeLineageEvidence turns the group lineage decision into claims: an
// accepted acknowledgement is a derived parent tied to its launch; a child
// without one keeps the root only as a synthesized fallback, with the
// reason, so it never becomes verified ancestry.
func claudeLineageEvidence(conversations []ConversationRecord) map[int][]EvidenceRecord {
	root := -1
	for index, conversation := range conversations {
		if conversation.Provider == "claude" && conversation.AgentDepth == 0 {
			root = index
			break
		}
	}
	out := map[int][]EvidenceRecord{}
	if root < 0 {
		return out
	}
	edges, reasons := claudeLineageDecisions(conversations)
	for index, conversation := range conversations {
		if index == root || conversation.Provider != "claude" {
			continue
		}
		if edge, ok := edges[index]; ok {
			parent := conversations[edge.parent]
			rule, origin := "claude-launch-ack/v1", evidenceDerived
			if edge.textual {
				rule = "claude-async-text-ack+assignment/v1"
			}
			out[index] = []EvidenceRecord{{Kind: evidenceParent, Subject: "conversation", Object: executionNativeKey("claude", parent.Account, parent.NativeID),
				Origin: origin.String(), Rule: rule, IDKind: idKindCallID, Locator: "launch:" + edge.callID,
				Detail: map[string]any{"launch_conversation": parent.NativeID, "call_id": edge.callID}}}
			continue
		}
		reason := defaultString(reasons[index], "no-acknowledgement")
		out[index] = []EvidenceRecord{{Kind: evidenceParent, Subject: "conversation", Object: executionNativeKey("claude", conversations[root].Account, conversations[root].NativeID),
			Origin: evidenceSynthesized.String(), Rule: "claude-root-fallback/v1", Detail: map[string]any{"reason": reason}}}
	}
	return out
}

// writeClaudeLineageEvidence replaces the group lineage claims of a Claude
// root group (called wherever linkClaudeGroupDelegations runs).
func writeClaudeLineageEvidence(tx *sql.Tx, conversations []ConversationRecord, ids []string) error {
	claims := claudeLineageEvidence(conversations)
	for index, conversation := range conversations {
		if index >= len(ids) || ids[index] == "" || conversation.Provider != "claude" {
			continue
		}
		if err := replaceExecutionClaims(tx, ids[index], evidenceSourceClaude, "claude/"+conversation.Account, claims[index], true); err != nil {
			return err
		}
	}
	return nil
}

// backfillExecutionEvidence re-derives claims from retained rows for
// conversations whose evidence is missing or of another version: launch
// outcomes from stored results, Codex history notes, and the provider parent
// field from conversation_parent_refs (the raw field, kept apart from the
// resolved parent_id). Claude groups re-run their lineage over retained
// delegation messages. What ingestion never stored (Cursor's filtered copies
// and suppressed echoes) is not recoverable here: those conversations record
// input 'retained' and need a provider read (docs/execution-evidence-backfill.md).
// onlyMissing skips conversations already current.
func backfillExecutionEvidence(ctx context.Context, tx *sql.Tx, ids []string, onlyMissing bool) error {
	claudeRoots := map[[2]string]bool{}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		row := struct {
			provider, account, native, workspace string
			depth                                int
			parentRef                            sql.NullString
			version, input                       sql.NullString
		}{}
		err := tx.QueryRowContext(ctx, `SELECT c.provider,c.account,c.native_id,c.workspace_id,c.agent_depth,r.parent_native_id,s.version,s.input
		 FROM conversations c LEFT JOIN conversation_parent_refs r ON r.provider=c.provider AND r.account=c.account AND r.native_id=c.native_id
		 LEFT JOIN execution_evidence_state s ON s.conversation_id=c.id WHERE c.id=?`, id).
			Scan(&row.provider, &row.account, &row.native, &row.workspace, &row.depth, &row.parentRef, &row.version, &row.input)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return err
		}
		if onlyMissing && row.version.String == executionEvidenceVersion {
			continue
		}
		conversation := ConversationRecord{Provider: row.provider, Account: row.account, NativeID: row.native, AgentDepth: row.depth, ParentNativeID: row.parentRef.String}
		// What the adapter retained stays the adapter's: a parent it recorded
		// as a fallback is not re-derived as the provider's field.
		retained, err := loadStoredExecutionClaims(ctx, tx, `WHERE conversation_id=? AND source=? AND kind=?`, id, evidenceSourceAdapter, evidenceParent)
		if err != nil {
			return err
		}
		for _, claim := range retained {
			conversation.Evidence = append(conversation.Evidence, EvidenceRecord{Kind: claim.Kind, Origin: claim.Origin, Authority: claim.Authority, Rule: claim.Rule})
		}
		read := row.input.String == "ingest" && ingestStillComplete(row.provider, row.version.String)
		established := true
		if conversation.ParentNativeID != "" {
			if conversation.ParentProvider, conversation.ParentAccount, established, err = retainedParentNamespace(ctx, tx, id, conversation); err != nil {
				return err
			}
			if conversation.Provider == "claude" && (conversation.ParentProvider == "" || conversation.ParentProvider == "claude") {
				// Only an explicitly recorded other-provider parent is kept;
				// group lineage writes Claude's own.
				conversation.ParentNativeID, conversation.ParentProvider, conversation.ParentAccount = "", "", ""
			}
		}
		messages, err := tx.QueryContext(ctx, `SELECT native_id,kind,COALESCE(call_id,''),text FROM messages WHERE conversation_id=?
		 AND (kind IN ('delegation','delegation_result') OR (`+claudeWorkflowCallSQL("")+`) OR (kind='tool_result' AND call_id<>'') OR (kind='metadata' AND text LIKE '{"payload":%"type":"session_meta"%'))
		 ORDER BY source_order IS NULL,source_order,created_at,id`, id)
		if err != nil {
			return err
		}
		for messages.Next() {
			var message MessageRecord
			if err := messages.Scan(&message.NativeID, &message.Kind, &message.CallID, &message.Text); err != nil {
				messages.Close()
				return err
			}
			conversation.Messages = append(conversation.Messages, message)
		}
		messages.Close()
		if err := messages.Err(); err != nil {
			return err
		}
		namespace := row.provider + "/" + row.account
		claims := conversationExecutionEvidence(conversation)
		if !established {
			for i := range claims {
				if claims[i].Kind == evidenceParent && claims[i].Rule == "provider-parent-field/v1" {
					// The stored reference keeps the parent's ID but not its
					// namespace: the child's is a guess, not the provider's record.
					claims[i].Origin, claims[i].Authority, claims[i].Rule = evidenceLegacy.String(), authorityUnverified, "retained-parent-ref/v1"
					claims[i].Detail["parent_namespace"] = "unrecorded"
				}
			}
		}
		if sessionParentProviders[row.provider] && row.depth > 0 && !read {
			// Ingested before the adapter recorded its fallback: a stored
			// session parent may be that default.
			var session string
			if err := tx.QueryRowContext(ctx, `SELECT source_id FROM workspaces WHERE id=?`, row.workspace).Scan(&session); err != nil && err != sql.ErrNoRows {
				return err
			}
			for i := range claims {
				if claims[i].Kind == evidenceParent && claims[i].Rule == "provider-parent-field/v1" && conversation.ParentNativeID == session {
					claims[i].Origin, claims[i].Authority, claims[i].Rule = evidenceLegacy.String(), authorityUnverified, "copilot-retained-session-parent/v1"
				}
			}
		}
		if err := replaceExecutionClaims(tx, id, evidenceSourceRecord, namespace, claims, true); err != nil {
			return err
		}
		// A provider read whose adapter evidence is still complete stays the
		// conversation's input: a merge or a version change re-derives from
		// stored rows without losing what the adapter retained.
		input := "retained"
		if read {
			input = "ingest"
		}
		if err := recordExecutionEvidenceState(tx, id, input, executionIDPolicy(row.provider)); err != nil {
			return err
		}
		if row.provider == "claude" {
			claudeRoots[[2]string{row.workspace, claudeRootNative(row.native)}] = true
		}
	}
	for _, key := range slices.SortedFunc(mapsKeysAny(claudeRoots), func(a, b [2]string) int { return strings.Compare(a[0]+"\x00"+a[1], b[0]+"\x00"+b[1]) }) {
		if err := backfillClaudeLineageEvidence(ctx, tx, key[0], key[1]); err != nil {
			return err
		}
	}
	return nil
}

// retainedParentNamespace is the namespace of a conversation's retained
// parent reference (conversation_parent_refs keeps only the parent's native
// ID). An origin observation of that parent records the parent's provider
// and account when ingestion was told them; an adapter whose source is the
// conversation's own provider names parents only in that provider. Otherwise
// the namespace was never recorded: established is false.
func retainedParentNamespace(ctx context.Context, tx *sql.Tx, id string, conversation ConversationRecord) (provider, account string, established bool, err error) {
	rows, err := queryMapsContext(ctx, tx, `SELECT evidence_key,COALESCE(parent_provider,'') parent_provider,COALESCE(parent_account,'') parent_account
	 FROM origin_observations WHERE conversation_id=? AND parent_native_id=? ORDER BY evidence_key`, id, conversation.ParentNativeID)
	if err != nil && !strings.Contains(err.Error(), "no such table") {
		return "", "", false, err
	}
	sameSource := false
	for _, row := range rows {
		if recorded := firstString(row["parent_provider"]); recorded != "" {
			return recorded, firstString(row["parent_account"], conversation.Account), true, nil
		}
		if parts := strings.SplitN(firstString(row["evidence_key"]), "|", 3); len(parts) == 3 && parts[1] == conversation.Provider {
			sameSource = true
		}
	}
	if !sameSource {
		var kind string
		if err := tx.QueryRowContext(ctx, `SELECT w.source_kind FROM conversations c JOIN workspaces w ON w.id=c.workspace_id WHERE c.id=?`, id).Scan(&kind); err != nil && err != sql.ErrNoRows {
			return "", "", false, err
		}
		sameSource = kind == conversation.Provider
	}
	return "", "", sameSource, nil
}

// ingestStillComplete is whether a provider read recorded at version holds
// every adapter claim the current version expects. v3 added Copilot's
// session fallbacks and Cursor's unmapped and over-long copies; nothing
// else an adapter retains changed since v1.
func ingestStillComplete(provider, version string) bool {
	switch {
	case version == executionEvidenceVersion, version == "execution-evidence-v3", version == "execution-evidence-v4":
		// v4 and v5 add only evidence recoverable from retained messages;
		// a provider read that already supplied v3's copy/parent evidence
		// remains complete through those upgrades.
		return true
	case sessionParentProviders[provider], provider == "cursor":
		return false
	}
	return version == "execution-evidence-v1" || version == "execution-evidence-v2"
}

func mapsKeysAny[K comparable, V any](m map[K]V) func(func(K) bool) {
	return func(yield func(K) bool) {
		for key := range m {
			if !yield(key) {
				return
			}
		}
	}
}

// backfillClaudeLineageEvidence re-runs one root group's lineage over its
// retained delegation messages, writing claims only: the stored parent_id
// and depth stay as they are (repairStoredClaudeLineage owns those).
func backfillClaudeLineageEvidence(ctx context.Context, tx *sql.Tx, workspace, root string) error {
	record, ids, found, err := storedClaudeGroup(ctx, tx, workspace, root)
	if err != nil || !found {
		return err
	}
	ordered := make([]string, len(record.Conversations))
	for index, conversation := range record.Conversations {
		ordered[index] = ids[conversation.NativeID]
	}
	return writeClaudeLineageEvidence(tx, record.Conversations, ordered)
}

// restoreHistoryExecutionEvidence restores a recovered workspace's typed
// evidence after the ordinary writer re-ingested its record. Only the
// adapter's claims are restored: they were retained from a provider read
// that may be gone, while the record's and group lineage's are re-derived
// from the restored rows, so an obsolete one is never resurrected beside its
// replacement. The recovered state says which read the adapter's claims
// came from; with it the re-derivation keeps input 'ingest' only when that
// read is still complete for the current version, and an export without it
// records 'retained'.
func restoreHistoryExecutionEvidence(ctx context.Context, tx *sql.Tx, e historyRecovery, workspace string) error {
	present, err := executionEvidenceTablesPresent(ctx, tx)
	if err != nil || !present {
		return err
	}
	conversations := map[string]bool{}
	for _, conversation := range recoveryMaps(e, "conversations") {
		conversations[firstString(conversation["id"])] = true
	}
	ids := slices.Sorted(mapsKeysAny(conversations))
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM execution_evidence_state WHERE conversation_id=?`, id); err != nil {
			return err
		}
	}
	for _, o := range e.Observations {
		if o.Kind != "execution_claims" && o.Kind != "execution_evidence_state" {
			continue
		}
		column := map[string]int{}
		for index, name := range o.Columns {
			column[name] = index
		}
		filtered := historyObservation{Kind: o.Kind, Columns: o.Columns}
		for _, row := range o.Rows {
			value := func(name string) string {
				index, ok := column[name]
				if !ok {
					return ""
				}
				decoded, err := row[index].decode()
				if err != nil {
					return ""
				}
				return firstString(decoded)
			}
			if !conversations[value("conversation_id")] || o.Kind == "execution_claims" && value("source") != evidenceSourceAdapter {
				continue
			}
			filtered.Rows = append(filtered.Rows, row)
		}
		if len(filtered.Rows) > 0 {
			if err := restoreHistoryObservation(ctx, tx, filtered, true); err != nil {
				return err
			}
		}
	}
	if err := backfillExecutionEvidence(ctx, tx, ids, false); err != nil {
		return err
	}
	return markExecutionDependents(ctx, tx)
}

// executionEvidenceBackfillCandidates lists conversations whose evidence is
// absent or of another version, oldest first, at most limit.
func executionEvidenceBackfillCandidates(ctx context.Context, q queryer, limit int) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT c.id FROM conversations c LEFT JOIN execution_evidence_state s ON s.conversation_id=c.id
	 WHERE s.version IS NOT ? ORDER BY c.id LIMIT ?`, executionEvidenceVersion, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// BackfillExecutionEvidence re-derives typed evidence from retained rows in
// batches, resumably: each batch commits its conversations' claims and state.
// It never reads a provider and never changes a conversation's stored
// parent, messages or usage. It is explicit (pharos execution-evidence run),
// never run on open or by background work: see
// docs/execution-evidence-backfill.md.
func (c *Catalog) BackfillExecutionEvidence(ctx context.Context, batch int) (int, error) {
	return c.backfillExecutionEvidenceBounded(ctx, batch, 0, nil)
}

// executionBackfillBudget is about how long one backfill batch may hold the
// writer; a package variable so a test can lower it.
var executionBackfillBudget = time.Second

// backfillExecutionEvidenceBounded is BackfillExecutionEvidence stopping
// after limit conversations (0: none left), reporting each committed batch.
func (c *Catalog) backfillExecutionEvidenceBounded(ctx context.Context, batch, limit int, progress func(done int)) (int, error) {
	if batch <= 0 {
		batch = 500
	}
	done, step := 0, batch
	for limit <= 0 || done < limit {
		size := step
		if limit > 0 {
			size = min(step, limit-done)
		}
		ids, err := executionEvidenceBackfillCandidates(ctx, c.reader(), size)
		if err != nil || len(ids) == 0 {
			return done, err
		}
		c.primeDependencyInverse(ctx)
		started := time.Now()
		if err := c.writeTransaction(ctx, "derive execution evidence from retained rows", func(tx *sql.Tx) error {
			if err := backfillExecutionEvidence(ctx, tx, ids, true); err != nil {
				return err
			}
			return markExecutionDependents(ctx, tx)
		}); err != nil {
			return done, err
		}
		// Keep each batch's hold on the writer near the budget: halve the
		// next batch after a slow one, grow it back after a quick one.
		switch held := time.Since(started); {
		case held > executionBackfillBudget:
			step = max(1, len(ids)/2)
		case held < executionBackfillBudget/4:
			step = min(batch, step*2)
		}
		done += len(ids)
		// Bring the committed dependency index forward past this batch, so the
		// next batch's marking patches only its own changes.
		if _, err := dependencyInverseIndex(ctx, c.reader()); err != nil {
			return done, err
		}
		if progress != nil {
			progress(done)
		}
		if len(ids) < size {
			break
		}
	}
	return done, nil
}

// storedExecutionClaim is one execution_claims row.
type storedExecutionClaim struct {
	ConversationID, Source, Kind, Subject, Object, Origin, Authority, Rule, Namespace, IDKind, Locator string
	Detail                                                                                             map[string]any
}

func loadStoredExecutionClaims(ctx context.Context, q queryer, where string, args ...any) ([]storedExecutionClaim, error) {
	// A snapshot from a build before authorities were recorded (opened
	// without Initialize, as offline labeling does) reads them as unknown.
	authority := "authority"
	if columns, err := queryMapsContext(ctx, q, `SELECT 1 FROM pragma_table_info('execution_claims') WHERE name='authority'`); err != nil {
		return nil, err
	} else if len(columns) == 0 {
		authority = "'' authority"
	}
	rows, err := q.QueryContext(ctx, `SELECT conversation_id,source,kind,subject,object,origin,`+authority+`,rule,namespace,id_kind,locator,detail_json FROM execution_claims `+where+
		` ORDER BY conversation_id,claim_key`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []storedExecutionClaim{}
	for rows.Next() {
		var claim storedExecutionClaim
		var detail string
		if err := rows.Scan(&claim.ConversationID, &claim.Source, &claim.Kind, &claim.Subject, &claim.Object, &claim.Origin, &claim.Authority, &claim.Rule, &claim.Namespace, &claim.IDKind, &claim.Locator, &detail); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(detail), &claim.Detail)
		out = append(out, claim)
	}
	return out, rows.Err()
}

func strengthOf(origin string) evidenceStrength {
	switch origin {
	case "native":
		return evidenceNative
	case "derived":
		return evidenceDerived
	case "legacy":
		return evidenceLegacy
	case "synthesized":
		return evidenceSynthesized
	}
	return evidenceLegacy
}

// strength is the strength resolution gives a stored claim: its origin's,
// capped by what its authority establishes. Provenance and authority stay
// distinct: a parsed envelope is derived yet verified (identityAuthority
// lifts it), while a field the provider wrote that an adapter only defaulted
// (authority fallback) is a hint whatever its origin, and an unverified
// stored decision is never stronger than legacy. A row from before
// authorities were recorded keeps its origin's strength.
func (claim storedExecutionClaim) strength() evidenceStrength {
	strength := strengthOf(claim.Origin)
	limit := evidenceNative
	switch claim.Authority {
	case authorityInferred:
		limit = evidenceDerived
	case authorityUnverified:
		limit = evidenceLegacy
	case authorityFallback:
		limit = evidenceSynthesized
	}
	return min(strength, limit)
}

func init() {
	executionClaimSources = append(executionClaimSources, persistedExecutionClaims)
	occurrenceClaimSources = append(occurrenceClaimSources, persistedOccurrenceClaims)
}

// Coverage names persistedExecutionClaims reports in executionEvidence.Missing.
const (
	// Some conversations have no typed evidence of the current version: their
	// relationships are only the legacy rows (unverified).
	missingTypedEvidence = "typed-execution-evidence"
	// Cursor conversations whose claims were re-derived from retained rows
	// alone: copies and echoes the old reader filtered are not recoverable
	// without a provider read.
	missingCursorCopyEvidence = "cursor-copy-evidence"
)

// executionKeyIndex resolves lookup keys to conversations.
type executionKeyIndex struct {
	byKey    map[string][]string
	children map[[2]string][]string // parent conversation, agent path
	sessions map[[2]string]string   // conversation, launch call ID: session ID
	info     map[string]executionStream
}

func newExecutionKeyIndex(ctx context.Context, q queryer, e *executionEvidence) (*executionKeyIndex, error) {
	index := &executionKeyIndex{byKey: map[string][]string{}, children: map[[2]string][]string{}, sessions: map[[2]string]string{}, info: map[string]executionStream{}}
	rows, err := queryMapsContext(ctx, q, `SELECT id,aliases_json,COALESCE(parent_id,'') parent_id,COALESCE(agent_path,'') agent_path FROM conversations
	 WHERE aliases_json NOT IN ('','[]') OR (agent_path<>'' AND parent_id IS NOT NULL)`)
	if err != nil {
		return nil, err
	}
	aliases := map[string][]string{}
	for _, row := range rows {
		id := firstString(row["id"])
		var list []string
		_ = json.Unmarshal([]byte(firstString(row["aliases_json"])), &list)
		aliases[id] = list
		if path, parent := firstString(row["agent_path"]), firstString(row["parent_id"]); path != "" && parent != "" {
			index.children[[2]string{parent, path}] = append(index.children[[2]string{parent, path}], id)
		}
	}
	for _, stream := range e.Streams {
		switch stream.Ref.Kind {
		case sourceConversation:
			index.info[stream.Ref.ID] = stream
			for _, key := range executionLookupKeys(stream.Provider, stream.Account, stream.NativeID, aliases[stream.Ref.ID]) {
				if !slices.Contains(index.byKey[key], stream.Ref.ID) {
					index.byKey[key] = append(index.byKey[key], stream.Ref.ID)
				}
			}
		case sourceSession:
			index.sessions[[2]string{stream.ConversationID, stream.NativeID}] = stream.Ref.ID
		}
	}
	for key := range index.byKey {
		sort.Strings(index.byKey[key])
	}
	return index, nil
}

// resolve returns the one conversation a key names from owner, or "" when
// it names none or several (several reports true: those stay unresolved,
// never chosen).
func (x *executionKeyIndex) resolve(owner, key string) (string, bool) {
	if path, ok := strings.CutPrefix(key, "task-path:"); ok {
		matches := []string{}
		source := x.info[owner]
		for _, id := range x.children[[2]string{owner, path}] {
			if child := x.info[id]; child.Provider == source.Provider && child.Account == source.Account {
				matches = append(matches, id)
			}
		}
		if len(matches) == 1 {
			return matches[0], false
		}
		return "", len(matches) > 1
	}
	matches := x.byKey[key]
	if len(matches) == 1 {
		return matches[0], false
	}
	return "", len(matches) > 1
}

// gradeClaudeMaterializedLinks caps the legacy reading of what Claude group
// lineage materialized: a launch's stored child (claude-accepted-launch-link)
// and a non-root stored parent (claude-accepted-lineage). Both are copies of
// one acknowledgement inference, not observations of their own. When the
// child has group evidence, they stay derived only while it accepts the same
// launch (lineageChild) or parent. Otherwise they become synthesized hints:
// the group was re-derived as a fallback, or its claims are withdrawn as
// stale. A child with no group evidence at all (a catalog before typed
// evidence) keeps the legacy reading, as every legacy relationship does.
func gradeClaudeMaterializedLinks(e *executionEvidence, stored []storedExecutionClaim, stale map[string]bool, lineageChild map[[2]string]string) {
	grouped, accepted := map[string]bool{}, map[[2]string]bool{}
	for _, claim := range stored {
		if claim.Kind == evidenceParent && claim.Source == evidenceSourceClaude {
			grouped[claim.ConversationID] = true
		}
	}
	for launch, child := range lineageChild {
		accepted[[2]string{child, launch[0]}] = true
	}
	streams := map[sourceRef]executionStream{}
	for _, stream := range e.Streams {
		streams[stream.Ref] = stream
	}
	for i := range e.Claims {
		claim := &e.Claims[i]
		var child string
		var supported bool
		switch {
		case claim.Kind == claimChildLink && claim.Rule == "claude-accepted-launch-link":
			stream := streams[claim.Subject]
			child = claim.Object.ID
			supported = lineageChild[[2]string{stream.ConversationID, stream.NativeID}] == child
		case claim.Kind == claimTranscriptParent && claim.Rule == "claude-accepted-lineage":
			child = claim.Subject.ID
			supported = accepted[[2]string{child, claim.Object.ID}]
		default:
			continue
		}
		if stale[child] || grouped[child] && !supported {
			claim.Strength, claim.Rule = evidenceSynthesized, claim.Rule+"-unsupported"
		}
	}
}

// persistedExecutionClaims adds the typed claims ingestion persisted. Each
// either upgrades the legacy reading of the same relationship (native or
// derived provenance) or, for a Claude root fallback, marks it synthesized
// so it stays a discovery hint. Conversations without current typed evidence
// keep only their legacy claims, and the evidence says so.
func persistedExecutionClaims(ctx context.Context, q queryer, e *executionEvidence) error {
	present, err := executionEvidenceTablesPresent(ctx, q)
	if err != nil {
		return err
	}
	if !present {
		e.Missing = appendMissing(e.Missing, missingTypedEvidence)
		return nil
	}
	index, err := newExecutionKeyIndex(ctx, q, e)
	if err != nil {
		return err
	}
	states := map[string]string{}
	stateRows, err := queryMapsContext(ctx, q, `SELECT conversation_id,input FROM execution_evidence_state WHERE version=?`, executionEvidenceVersion)
	if err != nil {
		return err
	}
	for _, row := range stateRows {
		states[firstString(row["conversation_id"])] = firstString(row["input"])
	}
	// Group lineage claims are read whatever a member's own state, except
	// where they were re-derived from stored rows that changed since. One
	// run writes every member's claims from every member's rows, so a stale
	// member withdraws the whole group's, including members a partial first
	// backfill has not reached yet (they have no state row to mark).
	stale := map[string]bool{}
	root := claudeGroupRootSQL
	staleRows, err := queryMapsContext(ctx, q, `SELECT s.conversation_id FROM execution_evidence_state s WHERE substr(s.version,1,?)=?
	 UNION SELECT g.id FROM execution_evidence_state s JOIN conversations c ON c.id=s.conversation_id AND c.provider='claude'
	 JOIN conversations g ON g.account=c.account AND g.workspace_id=c.workspace_id AND g.provider='claude'
	 AND (g.native_id=`+root+` OR g.native_id>`+root+`||':subagent:' AND g.native_id<`+root+`||':subagent;')
	 WHERE substr(s.version,1,?)=?`, len(executionEvidenceStale), executionEvidenceStale, len(executionEvidenceStale), executionEvidenceStale)
	if err != nil {
		return err
	}
	for _, row := range staleRows {
		stale[firstString(row["conversation_id"])] = true
	}
	for id, stream := range index.info {
		if _, ok := states[id]; !ok {
			e.Missing = appendMissing(e.Missing, missingTypedEvidence)
		} else if stream.Provider == "cursor" && states[id] == "retained" {
			e.Missing = appendMissing(e.Missing, missingCursorCopyEvidence)
		}
	}
	stored, err := loadStoredExecutionClaims(ctx, q, `WHERE kind IN (?,?,?,?)`, evidenceParent, evidenceLaunchOutcome, evidenceHistoryCopy, evidenceWorkflowLaunch)
	if err != nil {
		return err
	}
	// A text acknowledgement binds to the child whose group lineage accepted
	// the same launch (the assignment matched).
	lineageChild := map[[2]string]string{}
	for _, claim := range stored {
		if claim.Kind == evidenceParent && claim.Source == evidenceSourceClaude && claim.Origin == "derived" && !stale[claim.ConversationID] {
			if parent, ok := index.resolve(claim.ConversationID, claim.Object); ok && parent != "" {
				lineageChild[[2]string{parent, strings.TrimPrefix(claim.Locator, "launch:")}] = claim.ConversationID
			}
		}
	}
	gradeClaudeMaterializedLinks(e, stored, stale, lineageChild)
	conflicting := map[[2]string]bool{}
	for _, claim := range stored {
		if claim.Kind == evidenceLaunchOutcome && integer(claim.Detail["conflicting"]) > 1 {
			conflicting[[2]string{claim.ConversationID, claim.Subject}] = true
		}
	}
	conversationRef := func(id string) sourceRef { return sourceRef{sourceConversation, id} }
	appendWorkflowParents := workflowParentAppender(e)
	for _, claim := range stored {
		if _, current := states[claim.ConversationID]; !current && (claim.Source != evidenceSourceClaude || stale[claim.ConversationID]) {
			continue
		}
		if _, known := index.info[claim.ConversationID]; !known {
			continue
		}
		strength := claim.strength()
		rule := claim.Rule
		locator := conversationRef(claim.ConversationID)
		if message, ok := strings.CutPrefix(claim.Locator, "message:"); ok {
			locator = sourceRef{sourceMessage, messageCatalogID(claim.ConversationID, message)}
		}
		switch claim.Kind {
		case evidenceWorkflowLaunch:
			appendWorkflowParents(claim.ConversationID, claim.Object, locator, strength)
		case evidenceParent:
			parent, _ := index.resolve(claim.ConversationID, claim.Object)
			if parent == "" || parent == claim.ConversationID {
				// An absent parent leaves ancestry missing; the claim stays as
				// a dependency (executionDependencyDigests) for when it arrives.
				continue
			}
			kind := claimTranscriptParent
			depth := integer(claim.Detail["agent_depth"])
			if claim.Source == evidenceSourceRecord && depth == 0 {
				switch index.info[claim.ConversationID].Provider {
				case "codex", "cursor":
					// A rollout or composer forked from another without being
					// launched by it: another segment of the same execution.
					kind, rule = claimContinuation, index.info[claim.ConversationID].Provider+"-fork-parent/v1"
				default:
					// The field's meaning is not declared for this provider.
					continue
				}
			}
			e.Claims = append(e.Claims, executionClaim{Kind: kind, Subject: conversationRef(claim.ConversationID), Object: conversationRef(parent), Strength: strength,
				Namespace: claim.Namespace, IDKind: claim.IDKind, Rule: rule, Locator: locator})
		case evidenceHistoryCopy:
			// A Codex rollout forked from another thread without a delegating
			// parent continues that thread's execution in a new segment. With
			// a parent field it is a forked subagent: the parent claim decides.
			info := index.info[claim.ConversationID]
			if claim.Rule != "codex-session-meta/v1" || claim.Detail["forked_from"] != true || info.RecordedDepth != 0 {
				continue
			}
			source, several := index.resolve(claim.ConversationID, claim.Object)
			if source == "" || several || source == claim.ConversationID {
				continue
			}
			e.Claims = append(e.Claims, executionClaim{Kind: claimContinuation, Subject: conversationRef(claim.ConversationID), Object: conversationRef(source),
				Strength: strength, Namespace: claim.Namespace, IDKind: claim.IDKind, Rule: "codex-forked-from/v1", Locator: locator})
		case evidenceLaunchOutcome:
			call := strings.TrimPrefix(claim.Subject, "launch:")
			session := index.sessions[[2]string{claim.ConversationID, call}]
			if session == "" || conflicting[[2]string{claim.ConversationID, claim.Subject}] {
				// Conflicting envelopes for one launch identify nothing.
				continue
			}
			launch := sourceRef{sourceSession, session}
			switch firstString(claim.Detail["outcome"]) {
			case "rejected":
				e.Claims = append(e.Claims, executionClaim{Kind: claimLaunchRejected, Subject: launch, Strength: strength, Namespace: claim.Namespace,
					IDKind: claim.IDKind, Rule: rule, Locator: locator})
			case "accepted":
				object := sourceRef{}
				if claim.Object != "" {
					child, several := index.resolve(claim.ConversationID, claim.Object)
					if several {
						// More than one transcript answers the handle: the launch
						// stays an attempt whose identity is undecided.
						continue
					}
					if child == claim.ConversationID {
						continue
					}
					if child != "" {
						object = conversationRef(child)
					}
				} else if child := lineageChild[[2]string{claim.ConversationID, call}]; child != "" {
					object = conversationRef(child)
				}
				e.Claims = append(e.Claims, executionClaim{Kind: claimLaunchAccepted, Subject: launch, Object: object, Strength: strength, Namespace: claim.Namespace,
					IDKind: claim.IDKind, Rule: rule, Locator: locator})
			}
		}
	}
	return nil
}

// copyObservation is a stored message as copy classification reads it.
type copyObservation struct{ id, native, locator, hash, kind string }

// codexMissingOriginalCopies classifies a Codex history copy whose original
// is not retained (several: more than one transcript answers it). Absence of
// the original does not make the copy the conversation's own activity: an
// ordinal names rollout items, which stored messages follow only
// approximately, so the first observations it covers stay unresolved
// candidates (partial coverage), as does every observation when the note
// gives no boundary at all. Context names the missing original's key
// ("missing:<key>"), which binds to no execution, so ownership stays unknown
// rather than borrowed.
func codexMissingOriginalCopies(copy storedExecutionClaim, rows []copyObservation, several bool) []executionClaim {
	context := sourceRef{sourceConversation, "missing:" + copy.Object}
	if several {
		context = sourceRef{}
	}
	locator := sourceRef{sourceConversation, copy.ConversationID}
	bound := -1
	if firstString(copy.Detail["mapping"]) == "ordinal" {
		bound = int(integer(firstNonNil(copy.Detail["forked_from_ordinal_exclusive"], copy.Detail["subagent_history_start_ordinal"])))
	}
	out, position := []executionClaim{}, 0
	for _, row := range rows {
		if row.kind == "system/metadata" {
			continue // the session_meta note is the copy's own record
		}
		if bound >= 0 && position >= bound {
			break
		}
		position++
		out = append(out, executionClaim{Kind: claimCopyCandidate, Subject: sourceRef{sourceMessage, row.id}, Strength: evidenceSynthesized,
			Namespace: copy.Namespace, IDKind: idKindUnknown, Rule: "codex-history-missing-original/v1", Locator: locator, Context: context})
	}
	return out
}

// cursorBubble is the bubble a stored Cursor message came from: the adapter
// names a bubble's call, result, thinking, usage and error records
// <bubble>:<part> (a bubble ID may itself hold colons).
func cursorBubble(native string) string {
	for _, part := range []string{":call", ":result", ":thinking", ":usage", ":error"} {
		if bubble, ok := strings.CutSuffix(native, part); ok {
			return bubble
		}
	}
	return native
}

// cursorCopyRegion is the bubbles a Cursor fork holds as copies: proven are
// placed within the copy length, suspected may be copies. A claim from v3 on
// lists them (ids, suspected), so the region is exact however much of the
// fork was captured. An earlier claim gives only the length and its first
// and last IDs: the first copied stored bubbles are proven only when both
// ends are where the length puts them, and otherwise only suspected, so a
// missing end never takes the fork's own later messages and a missing start
// never leaves the copies as the fork's own.
func cursorCopyRegion(copy storedExecutionClaim, rows []copyObservation) (proven, suspected map[string]bool) {
	proven, suspected = map[string]bool{}, map[string]bool{}
	if ids, ok := copy.Detail["ids"].([]any); ok {
		for _, id := range ids {
			proven[firstString(id)] = true
		}
		if more, ok := copy.Detail["suspected"].([]any); ok {
			for _, id := range more {
				suspected[firstString(id)] = true
			}
		}
		return proven, suspected
	}
	n := int(integer(copy.Detail["copied"]))
	first, last := firstString(copy.Detail["first_id"]), firstString(copy.Detail["last_id"])
	region := []string{}
	for _, row := range rows {
		bubble := cursorBubble(row.native)
		if len(region) > 0 && region[len(region)-1] == bubble {
			continue
		}
		if len(region) == n || len(region) > 0 && region[len(region)-1] == last {
			break
		}
		region = append(region, bubble)
	}
	exact := len(region) == n && n > 0 && region[0] == first && region[n-1] == last
	for _, bubble := range region {
		if exact {
			proven[bubble] = true
		} else {
			suspected[bubble] = true
		}
	}
	return proven, suspected
}

// cursorCopyOccurrences classifies a Cursor fork's copies left in the fork
// (an unverified or unmapped mapping; mapped copies were left out). A proven
// copy is the original's occurrence when it carries the original's bubble
// ID; one whose ID diverged, and every suspected copy, stays an unresolved
// candidate, never the fork's own. With the original not retained, a proven
// copy is an inherited copy of a missing original: the copy is its
// navigation target, and no owner is borrowed (context "missing:<key>").
func cursorCopyOccurrences(copy storedExecutionClaim, copied, originals []copyObservation, source string, several bool) []executionClaim {
	proven, suspected := cursorCopyRegion(copy, copied)
	context := sourceRef{sourceConversation, source}
	switch {
	case source != "":
	case several:
		context = sourceRef{}
	default:
		context = sourceRef{sourceConversation, "missing:" + copy.Object}
	}
	byNative, byContent := map[string]copyObservation{}, map[string][]copyObservation{}
	for _, original := range originals {
		byNative[original.native] = original
		byContent[original.kind+"\x00"+original.hash] = append(byContent[original.kind+"\x00"+original.hash], original)
	}
	locator := sourceRef{sourceConversation, copy.ConversationID}
	out := []executionClaim{}
	for _, observed := range copied {
		bubble := cursorBubble(observed.native)
		if !proven[bubble] && !suspected[bubble] {
			continue
		}
		subject := sourceRef{sourceMessage, observed.id}
		if proven[bubble] {
			if original, ok := byNative[observed.native]; ok && source != "" {
				out = append(out, executionClaim{Kind: claimInheritedCopy, Subject: subject, Object: sourceRef{sourceMessage, original.id}, Strength: copy.strength(),
					Namespace: copy.Namespace, IDKind: idKindNativeEvent, Rule: copy.Rule, Locator: locator, Context: context})
				continue
			}
			if source == "" {
				out = append(out, executionClaim{Kind: claimInheritedCopy, Subject: subject, Strength: copy.strength(),
					Namespace: copy.Namespace, IDKind: idKindNativeEvent, Rule: "cursor-fork-missing-original/v1", Locator: locator, Context: context})
				continue
			}
		}
		rule := "cursor-copy-unmatched/v1" // a placed copy whose ID is not the original's
		if !proven[bubble] {
			rule = "cursor-copy-unplaced/v1" // the length's bound is not established
		}
		candidates := byContent[observed.kind+"\x00"+observed.hash]
		if len(candidates) == 0 {
			candidates = []copyObservation{{}}
		}
		for _, candidate := range candidates {
			object := sourceRef{}
			if candidate.id != "" {
				object = sourceRef{sourceMessage, candidate.id}
			}
			out = append(out, executionClaim{Kind: claimCopyCandidate, Subject: subject, Object: object, Strength: evidenceSynthesized,
				Namespace: copy.Namespace, IDKind: idKindNativeEvent, Rule: rule, Locator: locator, Context: context})
		}
	}
	return out
}

// messageCatalogID is the stored ID of a conversation's message by native
// ID (see upsertMessage).
func messageCatalogID(conversationID, native string) string {
	return stableID("message", conversationID, native)
}

func appendMissing(missing []string, name string) []string {
	if slices.Contains(missing, name) {
		return missing
	}
	return append(missing, name)
}

// executionEvidenceTablesPresent is false for a snapshot of a catalog from
// before typed evidence: offline labeling then declares the capability
// missing instead of failing.
func executionEvidenceTablesPresent(ctx context.Context, q queryer) (bool, error) {
	rows, err := queryMapsContext(ctx, q, `SELECT COUNT(*) n FROM sqlite_master WHERE type='table' AND name IN ('execution_claims','execution_evidence_state')`)
	if err != nil {
		return false, err
	}
	return len(rows) > 0 && integer(rows[0]["n"]) == 2, nil
}

// persistedOccurrenceClaims turns copied-history claims into occurrence
// claims for the given conversations (nil: all). A copy's observation is
// matched to its original only by a provider event ID in the same namespace
// (Codex response items, Cursor bubbles): equal IDs within a declared copy
// are one physical occurrence. A synthesized line-N locator never matches;
// with equal content it is a copy candidate, left unresolved. Originals,
// policies and transcripts are read once for every copy (one query each),
// so a whole-catalog build costs the transcripts it classifies, not a query
// per copy.
func persistedOccurrenceClaims(ctx context.Context, q queryer, conversations []string) ([]executionClaim, error) {
	present, err := executionEvidenceTablesPresent(ctx, q)
	if err != nil || !present {
		return nil, err
	}
	// Copies re-derived from stored rows that changed since are not read.
	where, args := `WHERE kind=? AND conversation_id NOT IN (SELECT conversation_id FROM execution_evidence_state WHERE substr(version,1,?)=?)`,
		[]any{evidenceHistoryCopy, len(executionEvidenceStale), executionEvidenceStale}
	if conversations != nil {
		if len(conversations) == 0 {
			return nil, nil
		}
		where += ` AND conversation_id IN (SELECT value FROM json_each(?))`
		args = append(args, jsonText(conversations))
	}
	stored, err := loadStoredExecutionClaims(ctx, q, where, args...)
	if err != nil {
		return nil, err
	}
	copies := []storedExecutionClaim{}
	keys := map[string]bool{}
	for _, copy := range stored {
		// A declaration of no copies in the transcript (Cursor copies a
		// mapping proved were left out of the fork, or an explicit none).
		if historyDeclaresNoCopies(copy.Rule, copy.Detail) {
			continue
		}
		if len(strings.SplitN(strings.TrimPrefix(copy.Object, "native:"), "|", 3)) != 3 {
			continue
		}
		copies = append(copies, copy)
		keys[copy.Object] = true
	}
	if len(copies) == 0 {
		return nil, nil
	}
	triples := [][]string{}
	for _, key := range slices.Sorted(mapsKeysAny(keys)) {
		triples = append(triples, strings.SplitN(strings.TrimPrefix(key, "native:"), "|", 3))
	}
	sourceRows, err := queryMapsContext(ctx, q, `SELECT c.id, 'native:'||c.provider||'|'||c.account||'|'||c.native_id key FROM json_each(?) k
	 CROSS JOIN conversations c ON c.provider=json_extract(k.value,'$[0]') AND c.account=json_extract(k.value,'$[1]') AND c.native_id=json_extract(k.value,'$[2]')`, jsonText(triples))
	if err != nil {
		return nil, err
	}
	sources := map[string][]string{}
	for _, row := range sourceRows {
		sources[firstString(row["key"])] = append(sources[firstString(row["key"])], firstString(row["id"]))
	}
	read := map[string]bool{}
	for _, copy := range copies {
		read[copy.ConversationID] = true
		if found := sources[copy.Object]; len(found) == 1 {
			read[found[0]] = true
		}
	}
	ids := jsonText(slices.Sorted(mapsKeysAny(read)))
	policies := map[string]string{}
	policyRows, err := queryMapsContext(ctx, q, `SELECT conversation_id,id_policy FROM execution_evidence_state WHERE conversation_id IN (SELECT value FROM json_each(?))`, ids)
	if err != nil {
		return nil, err
	}
	for _, row := range policyRows {
		policies[firstString(row["conversation_id"])] = firstString(row["id_policy"])
	}
	transcripts := map[string][]copyObservation{}
	rows, err := q.QueryContext(ctx, `SELECT conversation_id,id,native_id,COALESCE(evidence_locator,''),content_hash,role||'/'||kind FROM messages
	 WHERE conversation_id IN (SELECT value FROM json_each(?)) ORDER BY conversation_id,source_order IS NULL,source_order,id`, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var conversation string
		var o copyObservation
		if err := rows.Scan(&conversation, &o.id, &o.native, &o.locator, &o.hash, &o.kind); err != nil {
			rows.Close()
			return nil, err
		}
		transcripts[conversation] = append(transcripts[conversation], o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	type originalIndex struct {
		native  map[string]copyObservation
		content map[string][]copyObservation
	}
	originals := map[string]*originalIndex{}
	out := []executionClaim{}
	for _, copy := range copies {
		found := sources[copy.Object]
		source := ""
		if len(found) == 1 && found[0] != copy.ConversationID {
			source = found[0]
		}
		copied := transcripts[copy.ConversationID]
		if copy.Rule == "cursor-fork-length-at-spawn/v1" {
			out = append(out, cursorCopyOccurrences(copy, copied, transcripts[source], source, len(found) > 1)...)
			continue
		}
		if source == "" {
			if copy.Rule == "codex-session-meta/v1" {
				out = append(out, codexMissingOriginalCopies(copy, copied, len(found) > 1)...)
			}
			continue
		}
		policy := policies[copy.ConversationID]
		// Many forks copy one original: index it once per policy.
		index := originals[source+"\x00"+policy]
		if index == nil {
			index = &originalIndex{native: map[string]copyObservation{}, content: map[string][]copyObservation{}}
			for _, original := range transcripts[source] {
				if messageIDKind(policy, original.native, original.locator) == idKindNativeEvent {
					index.native[original.native] = original
				}
				index.content[original.kind+"\x00"+original.hash] = append(index.content[original.kind+"\x00"+original.hash], original)
			}
			originals[source+"\x00"+policy] = index
		}
		byNative, byContent := index.native, index.content
		locator, context := sourceRef{sourceConversation, copy.ConversationID}, sourceRef{sourceConversation, source}
		for _, observed := range copied {
			subject := sourceRef{sourceMessage, observed.id}
			if messageIDKind(policy, observed.native, observed.locator) == idKindNativeEvent {
				if original, ok := byNative[observed.native]; ok {
					out = append(out, executionClaim{Kind: claimInheritedCopy, Subject: subject, Object: sourceRef{sourceMessage, original.id},
						Strength: copy.strength(), Namespace: copy.Namespace, IDKind: idKindNativeEvent, Rule: copy.Rule, Locator: locator, Context: context})
				}
				continue
			}
			// A synthesized locator, at the same or a shifted line: equal
			// content corroborates nothing alone, and repeated content has
			// several candidates. Each stays unresolved.
			for _, candidate := range byContent[observed.kind+"\x00"+observed.hash] {
				out = append(out, executionClaim{Kind: claimCopyCandidate, Subject: subject, Object: sourceRef{sourceMessage, candidate.id},
					Strength: evidenceSynthesized, Namespace: copy.Namespace, IDKind: idKindLocator, Rule: "copy-candidate-locator/v1", Locator: locator, Context: context})
			}
		}
	}
	return out, nil
}
