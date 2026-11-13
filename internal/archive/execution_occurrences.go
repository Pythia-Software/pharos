package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
)

// occurrenceIndex answers occurrence ownership for the conversations it was
// loaded for. It holds only copy, candidate and duplicate claims; every other
// observation is an original owned by the execution its stream binds to.
// Tool calls are looked up through the message that recorded them
// (tool_calls.call_message_id); model requests are classified from the
// messages that carried their usage (requestOccurrenceClaims).
type occurrenceIndex struct {
	r              *executionResolution
	claims         map[sourceRef]executionClaim
	byConversation map[string][]executionClaim
	conversations  map[string]bool // nil: the whole catalog
	unresolved     int
	// partial names transcripts whose copied history is unaccounted for, by
	// the rule that says so (see occurrenceCoverageClaims).
	partial map[string]string
}

// Occurrences loads occurrence claims for conversations (nil: all). It reads
// through q, which should be the snapshot the caller reads observations from.
func (r *executionResolution) Occurrences(ctx context.Context, q queryer, conversations []string) (*occurrenceIndex, error) {
	return loadOccurrenceIndex(ctx, q, r, conversations)
}

// occurrenceDependencyVersion names the rules that decide the facts
// occurrenceDependencyLines returns. It is not part of any digest: the facts
// themselves are, so a rule change supersedes only the derivatives of
// conversations whose facts it changes. Recorded with the marking cursor
// (executionDependentsVersions), a change makes the next mark compare every
// derivative with its conversation's facts under the new rules. A variable
// so tests can stand in a later version.
// v4: a provider read without a history declaration is not partial by itself.
var occurrenceDependencyVersion = "occurrence-dependency/v4-undeclared-history"

// occurrenceDependencyEncoding names how occurrenceDependencyLines writes
// its facts, and is part of every digest that includes them. Change it only
// when the lines' format changes: that supersedes every stored digest, and
// so rebuilds every conversation's labels and agent events. Its value is the
// rules version it was split from, which keeps the digests stored under it.
const occurrenceDependencyEncoding = "occurrence-dependency/v4-undeclared-history"

// occurrenceDependencyLines returns, per conversation (the copy's
// transcript), the occurrence facts Lookup and Partial decide its
// observations' roles from, one sorted line each: for every observation the
// index classifies (after its one-claim-per-observation choice), its kind,
// the occurrence it copies or duplicates and the stream that recorded that
// original (the claim's context), then the transcript's partial coverage
// reason. Owners are left out: which execution a stream binds to is
// resolution, which the execution evidence digest already covers, as are the
// copied launch calls the resolution adds. A conversation with no facts has
// no entry.
//
// Facts are read for conversations only (nil: every conversation), through
// their own rows and their recorded ancestry by index: a copy's facts depend
// on its ancestors' event IDs and memberships, so a change there changes the
// copy's lines while an unrelated append (an ID no copy shares) does not. The
// lines of a copy of a copy name the occurrence it copies, not the chain's
// end: each link's own lines carry the rest.
func occurrenceDependencyLines(ctx context.Context, q queryer, conversations []string) (map[string][]string, error) {
	claims, err := loadOccurrenceClaims(ctx, q, conversations)
	if err != nil {
		return nil, err
	}
	o := newOccurrenceIndex(nil, conversations, claims)
	out := map[string][]string{}
	for cid := range o.byConversation {
		if conversations == nil || o.conversations[cid] {
			out[cid] = o.dependencyLines(cid)
		}
	}
	for cid := range o.partial {
		if conversations == nil || o.conversations[cid] {
			out[cid] = o.dependencyLines(cid)
		}
	}
	return out, nil
}

// dependencyLines are one transcript's occurrence facts as
// occurrenceDependencyLines writes them, from whatever claims o holds: with a
// resolution, the copied launch calls it adds are among them.
func (o *occurrenceIndex) dependencyLines(cid string) []string {
	lines := []string{}
	for _, claim := range o.byConversation[cid] {
		lines = append(lines, strings.Join([]string{"occurrence", claim.Subject.String(), string(claim.Kind), claim.Object.String(), claim.Context.String()}, "\x1f"))
	}
	if reason := o.Partial(cid); reason != "" {
		lines = append(lines, "partial\x1f"+reason)
	}
	sort.Strings(lines)
	return lines
}

// copiedMessages lists the messages of cid that are copies or duplicate
// observations of another occurrence: what a transcript's own activity
// excludes. o must have been loaded for cid.
func (o *occurrenceIndex) copiedMessages(cid string) map[string]bool {
	copies := map[string]bool{}
	for _, claim := range o.byConversation[cid] {
		if claim.Subject.Kind == sourceMessage && (claim.Kind == claimInheritedCopy || claim.Kind == claimDuplicateObservation) {
			copies[claim.Subject.ID] = true
		}
	}
	return copies
}

func loadOccurrenceIndex(ctx context.Context, q queryer, r *executionResolution, conversations []string) (*occurrenceIndex, error) {
	claims, err := loadOccurrenceClaims(ctx, q, conversations)
	if err != nil {
		return nil, err
	}
	if claims, err = withCopiedLaunchCalls(ctx, q, r, conversations, claims); err != nil {
		return nil, err
	}
	return newOccurrenceIndex(r, conversations, claims), nil
}

// withCopiedLaunchCalls adds to claims, read for conversations (nil: all),
// the launch calls r proves copied (copiedLaunchCalls), and classifies the
// requests of the transcripts holding them again from the complete message
// decisions: a request whose usage events are a copied launch call is copied
// history too. Without a resolution claims are returned unchanged.
func withCopiedLaunchCalls(ctx context.Context, q queryer, r *executionResolution, conversations []string, claims []executionClaim) ([]executionClaim, error) {
	calls := r.copiedLaunchCalls(conversations)
	if len(calls) == 0 {
		return claims, nil
	}
	held := map[string]bool{}
	for _, call := range calls {
		held[occurrenceClaimConversation(call)] = true
	}
	out, messages := make([]executionClaim, 0, len(claims)+len(calls)), slices.Clone(calls)
	for _, claim := range claims {
		if !held[occurrenceClaimConversation(claim)] {
			out = append(out, claim)
		} else if claim.Subject.Kind != sourceRequest {
			out = append(out, claim)
			messages = append(messages, claim)
		}
	}
	requests, err := requestOccurrenceClaims(ctx, q, messages)
	if err != nil {
		return nil, err
	}
	return append(append(out, calls...), requests...), nil
}

// loadOccurrenceClaims reads the occurrence claims recorded for observations
// in conversations (nil: all): legacy ancestry matches and registered sources.
func loadOccurrenceClaims(ctx context.Context, q queryer, conversations []string) ([]executionClaim, error) {
	var claims []executionClaim
	if conversations != nil && len(conversations) == 0 {
		return nil, nil
	}
	if legacyExecutionScans["copied-occurrences"] {
		legacy, err := legacyOccurrenceClaims(ctx, q, conversations)
		if err != nil {
			return nil, err
		}
		claims = append(claims, legacy...)
	}
	coverage, err := occurrenceCoverageClaims(ctx, q, conversations)
	if err != nil {
		return nil, err
	}
	claims = append(claims, coverage...)
	for _, source := range occurrenceClaimSources {
		more, err := source(ctx, q, conversations)
		if err != nil {
			return nil, err
		}
		claims = append(claims, more...)
	}
	requests, err := requestOccurrenceClaims(ctx, q, claims)
	if err != nil {
		return nil, err
	}
	return append(claims, requests...), nil
}

// requestOccurrenceClaims classifies the model requests of transcripts whose
// messages are classified, from the stored messages of each request's first
// and last usage events (model_requests.source_message_id, final_message_id):
//   - both originals: the request is the transcript's own (no claim);
//   - both copies, or both duplicate observations: a copy of the request whose
//     first usage event is the original message, when exactly one exists, in
//     the original's stream;
//   - otherwise (a request whose events straddle copied history, an
//     unresolved event, or no stored link): an unresolved candidate.
//
// Request IDs, times and text are never matched: only the stored messages and
// their own occurrence decisions are. A Codex token_count event is stored as
// a line-N metadata message, which proves no copy, so a request a fork copied
// that way stays unresolved: counted and placed in the fork, with coverage
// partial.
func requestOccurrenceClaims(ctx context.Context, q queryer, claims []executionClaim) ([]executionClaim, error) {
	messages := newOccurrenceIndex(nil, nil, claims)
	transcripts := []string{}
	for cid, list := range messages.byConversation {
		if slices.ContainsFunc(list, func(claim executionClaim) bool { return claim.Subject.Kind == sourceMessage }) {
			transcripts = append(transcripts, cid)
		}
	}
	if len(transcripts) == 0 {
		return nil, nil
	}
	sort.Strings(transcripts)
	rows, err := queryMapsContext(ctx, q, `SELECT id,conversation_id,COALESCE(source_message_id,'') source,COALESCE(final_message_id,'') final
 FROM model_requests WHERE is_estimate=0 AND conversation_id IN (SELECT value FROM json_each(?)) ORDER BY id`, jsonText(transcripts))
	recorded := "is_estimate=0 AND "
	if err != nil && strings.Contains(err.Error(), "no such column: is_estimate") {
		recorded = "" // These legacy tables predate predictions entirely.
		rows, err = queryMapsContext(ctx, q, `SELECT id,conversation_id,COALESCE(source_message_id,'') source,COALESCE(final_message_id,'') final FROM model_requests WHERE conversation_id IN (SELECT value FROM json_each(?)) ORDER BY id`, jsonText(transcripts))
	}
	if err != nil && strings.Contains(err.Error(), "no such column") {
		// A ledger from before requests recorded their messages.
		rows, err = queryMapsContext(ctx, q, `SELECT id,conversation_id,'' source,'' final FROM model_requests WHERE `+recorded+`conversation_id IN (SELECT value FROM json_each(?)) ORDER BY id`, jsonText(transcripts))
		if err != nil && strings.Contains(err.Error(), "no such column: is_estimate") {
			recorded = ""
			rows, err = queryMapsContext(ctx, q, `SELECT id,conversation_id,'' source,'' final FROM model_requests WHERE conversation_id IN (SELECT value FROM json_each(?)) ORDER BY id`, jsonText(transcripts))
		}
	}
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, err
	}
	type copied struct {
		request, transcript string
		first               executionClaim
		strength            evidenceStrength
	}
	var out []executionClaim
	var copies []copied
	originals := map[string]bool{}
	for _, row := range rows {
		id, cid := firstString(row["id"]), firstString(row["conversation_id"])
		source, final := firstString(row["source"]), firstString(row["final"])
		if final == "" {
			final = source
		}
		candidate := func(rule string) {
			out = append(out, executionClaim{Kind: claimCopyCandidate, Subject: sourceRef{sourceRequest, id}, Strength: evidenceSynthesized,
				Rule: rule, Locator: sourceRef{sourceConversation, cid}})
		}
		if source == "" {
			candidate("request-usage-unlinked/v1")
			continue
		}
		first, firstClassified := messages.claims[sourceRef{sourceMessage, source}]
		last, lastClassified := messages.claims[sourceRef{sourceMessage, final}]
		switch {
		case !firstClassified && !lastClassified:
			continue // its own request
		case firstClassified && lastClassified && first.Kind == last.Kind && first.Kind != claimCopyCandidate:
			copies = append(copies, copied{request: id, transcript: cid, first: first, strength: min(first.Strength, last.Strength)})
			if first.Object.Kind == sourceMessage {
				originals[first.Object.ID] = true
			}
		default:
			candidate("request-usage-straddles-copy/v1")
		}
	}
	// The original's request: the one its original message opened, when
	// exactly one did.
	opened := map[string][]string{}
	if len(originals) > 0 {
		found, err := queryMapsContext(ctx, q, `SELECT id,source_message_id FROM model_requests WHERE `+recorded+`source_message_id IN (SELECT value FROM json_each(?)) ORDER BY id`,
			jsonText(slices.Sorted(maps.Keys(originals))))
		if err != nil {
			return nil, err
		}
		for _, row := range found {
			opened[firstString(row["source_message_id"])] = append(opened[firstString(row["source_message_id"])], firstString(row["id"]))
		}
	}
	for _, c := range copies {
		object := sourceRef{}
		if c.first.Object.Kind == sourceMessage && len(opened[c.first.Object.ID]) == 1 {
			object = sourceRef{sourceRequest, opened[c.first.Object.ID][0]}
		}
		out = append(out, executionClaim{Kind: c.first.Kind, Subject: sourceRef{sourceRequest, c.request}, Object: object, Strength: c.strength,
			Namespace: c.first.Namespace, IDKind: c.first.IDKind, Rule: "request-usage-messages/v1", Locator: sourceRef{sourceConversation, c.transcript},
			Context: c.first.Context})
	}
	return out, nil
}

// copiedLaunchCalls classifies the launch call of each copied launch
// stream in conversations (nil: all): the copy proven by the copied launch
// (same call, body and namespace, up the ancestry) is an inherited copy of
// the original's launch call, which belongs to the stream that issued it.
func (r *executionResolution) copiedLaunchCalls(conversations []string) []executionClaim {
	if r == nil {
		return nil
	}
	var scope map[string]bool
	if conversations != nil {
		scope = map[string]bool{}
		for _, cid := range conversations {
			scope[cid] = true
		}
	}
	var out []executionClaim
	for _, ref := range slices.SortedFunc(maps.Keys(r.copies), func(a, b sourceRef) int { return strings.Compare(a.String(), b.String()) }) {
		stream := r.streams[ref]
		if stream == nil || scope != nil && !scope[stream.ConversationID] {
			continue
		}
		// The original at the end of a copy chain.
		original, seen := r.copies[ref].Object, map[sourceRef]bool{ref: true}
		for next, ok := r.copies[original]; ok && !seen[original]; next, ok = r.copies[original] {
			seen[original] = true
			original = next.Object
		}
		copyCall, originalCall := r.launches[ref], r.launches[original]
		if copyCall.Locator.IsZero() || originalCall.Locator.IsZero() || copyCall.Locator == originalCall.Locator {
			continue
		}
		claim := r.copies[ref]
		out = append(out, executionClaim{Kind: claimInheritedCopy, Subject: copyCall.Locator, Object: originalCall.Locator, Strength: claim.Strength,
			Namespace: claim.Namespace, IDKind: idKindCallID, Rule: "copied-launch-call/v1", Locator: sourceRef{sourceConversation, stream.ConversationID},
			Context: originalCall.Object})
	}
	return out
}

// occurrenceClaimConversation is the conversation whose observation a claim
// classifies: every source locates its claims in the copy's transcript.
func occurrenceClaimConversation(claim executionClaim) string {
	if claim.Locator.Kind == sourceConversation {
		return claim.Locator.ID
	}
	return claim.Context.ID
}

// newOccurrenceIndex keeps one claim per observation: the strongest, an
// explicit copy before a candidate at equal strength, then a stable rule
// order. The result does not depend on the order of claims. With a
// resolution, claims must already hold its copied launch calls
// (withCopiedLaunchCalls).
func newOccurrenceIndex(r *executionResolution, conversations []string, claims []executionClaim) *occurrenceIndex {
	o := &occurrenceIndex{r: r, claims: map[sourceRef]executionClaim{}, byConversation: map[string][]executionClaim{}, partial: map[string]string{}}
	if conversations != nil {
		o.conversations = map[string]bool{}
		for _, cid := range conversations {
			o.conversations[cid] = true
		}
	}
	claims = slices.Clone(claims)
	sort.SliceStable(claims, func(i, j int) bool {
		a, b := claims[i], claims[j]
		if a.Strength != b.Strength {
			return a.Strength > b.Strength
		}
		if a.Kind != b.Kind {
			return a.Kind == claimInheritedCopy || a.Kind == claimDuplicateObservation && b.Kind == claimCopyCandidate
		}
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		if a.Subject != b.Subject {
			return a.Subject.String() < b.Subject.String()
		}
		return a.Object.String() < b.Object.String()
	})
	for _, claim := range claims {
		if claim.Subject.Kind == sourceConversation {
			// A transcript-level claim: its copied history is unaccounted for.
			if !streamClaim(claim.Kind) && o.partial[claim.Subject.ID] == "" {
				o.partial[claim.Subject.ID] = claim.Rule
			}
			continue
		}
		if claim.Subject.IsZero() || streamClaim(claim.Kind) || claim.Strength < evidenceLegacy && claim.Kind != claimCopyCandidate {
			continue
		}
		if _, dup := o.claims[claim.Subject]; dup {
			continue
		}
		o.claims[claim.Subject] = claim
		cid := occurrenceClaimConversation(claim)
		o.byConversation[cid] = append(o.byConversation[cid], claim)
		if claim.Kind == claimCopyCandidate {
			o.unresolved++
		}
	}
	return o
}

// Lookup classifies one observation. stream is its stored membership: the
// agent session that recorded it, or its conversation's stream.
func (o *occurrenceIndex) Lookup(obs, stream sourceRef) executionOccurrence {
	out := executionOccurrence{Observation: obs, Role: occurrenceOriginal, Canonical: obs, Navigate: obs}
	claim, ok := o.claims[obs]
	if !ok {
		if o.r == nil {
			return out
		}
		if b := o.r.Bind(stream); b.State == bindingResolved || b.State == bindingInherited {
			out.Owner = b.ExecutionID
			if b.State == bindingInherited {
				// The stream itself is copied history (a copied launch).
				out.Role = occurrenceInherited
			}
		}
		return out
	}
	out.Evidence = []executionClaim{claim}
	switch claim.Kind {
	case claimCopyCandidate:
		out.Role, out.Canonical, out.Owner = occurrenceUnresolved, sourceRef{}, ""
		return out
	case claimDuplicateObservation:
		out.Role = occurrenceDuplicate
	default:
		out.Role = occurrenceInherited
	}
	// Follow copies of copies to the original that survives.
	at, context, seen := claim.Object, claim.Context, map[sourceRef]bool{obs: true}
	for !at.IsZero() && !seen[at] {
		seen[at] = true
		next, ok := o.claims[at]
		if !ok || next.Kind == claimCopyCandidate {
			break
		}
		out.Evidence = append(out.Evidence, next)
		if next.Object.IsZero() {
			at, context = sourceRef{}, next.Context
			break
		}
		at, context = next.Object, next.Context
	}
	out.Canonical, out.Navigate = at, at
	if at.IsZero() {
		// Only the copy survives: navigate to it, and keep known ownership.
		out.Navigate = obs
	}
	if o.r != nil {
		if b := o.r.Bind(context); b.State == bindingResolved {
			out.Owner = b.ExecutionID
		}
	}
	return out
}

// Counts reports whether an observation is the stream execution's own
// activity. Copies and duplicate observations are not; unresolved candidates
// are kept as separate observations (see Unresolved).
func (o *occurrenceIndex) Counts(obs, stream sourceRef) bool {
	switch o.Lookup(obs, stream).Role {
	case occurrenceInherited, occurrenceDuplicate:
		return false
	}
	return true
}

// Classified lists the observations that are not plain originals: copies,
// duplicate observations and unresolved candidates, in a stable order.
func (o *occurrenceIndex) Classified() []sourceRef {
	refs := make([]sourceRef, 0, len(o.claims))
	for ref := range o.claims {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].String() < refs[j].String() })
	return refs
}

// Unresolved counts observations whose equivalence the evidence cannot
// decide; consumers counting them report partial coverage.
func (o *occurrenceIndex) Unresolved() int { return o.unresolved }

// Partial says why a transcript's own activity may still include history
// copied from another ("" when its coverage is complete): copies the evidence
// cannot account for, or observations whose equivalence is unresolved.
func (o *occurrenceIndex) Partial(cid string) string {
	if reason := o.partial[cid]; reason != "" {
		return reason
	}
	for _, claim := range o.byConversation[cid] {
		if claim.Kind == claimCopyCandidate {
			return "unresolved-observations"
		}
	}
	return ""
}

// Fingerprint summarizes the occurrence claims recorded for a transcript.
func (o *occurrenceIndex) Fingerprint(cid string) string {
	claims := append([]executionClaim(nil), o.byConversation[cid]...)
	sort.Slice(claims, func(i, j int) bool { return claims[i].Subject.String() < claims[j].Subject.String() })
	h := sha256.New()
	fmt.Fprintf(h, "partial=%s\x00", o.Partial(cid))
	for _, claim := range claims {
		owner := o.Lookup(claim.Subject, sourceRef{})
		fmt.Fprintf(h, "%s|%s|%s|%s|%s\x00", claim.Subject, claim.Kind, owner.Canonical, owner.Owner, owner.Role)
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
}

// legacyOccurrenceClaims matches messages a transcript retains from its
// recorded ancestry within one provider/account: a provider event ID equal to
// one in an ancestor is a copy of that occurrence. Equal synthesized line-N
// locators, or IDs of a provider without an event-ID policy, prove nothing,
// and coincide between unrelated transcripts (a launched agent's first lines
// look like its parent's), so they are not claims. What the rows cannot
// prove is reported per transcript instead (occurrenceCoverageClaims).
//
// Recorded ancestry that returns to the transcript (a self-parent, or A→B→A)
// proves no original: every transcript on the cycle is above the others, so a
// shared event ID among them is an unresolved candidate in each, counted and
// left partial, never a copy that loses both originals. A message is never
// matched with itself. A transcript below a cycle still copies through its
// own acyclic link to its nearest ancestor.
func legacyOccurrenceClaims(ctx context.Context, q queryer, conversations []string) ([]executionClaim, error) {
	where, args := executionScope("id", conversations)
	providers := slices.Sorted(maps.Keys(providerEventIDs))
	args = append(args, jsonText(providers))
	// Match on the (conversation, native ID) index alone, then read the two
	// IDs of each match: copies are few, the transcripts' messages are not.
	rows, err := queryMapsContext(ctx, q, `WITH RECURSIVE ancestors(conversation_id,ancestor_id,depth) AS (
 SELECT id,parent_id,1 FROM conversations WHERE parent_id IS NOT NULL`+where+`
 UNION SELECT a.conversation_id,p.parent_id,a.depth+1 FROM ancestors a JOIN conversations p ON p.id=a.ancestor_id
 WHERE p.parent_id IS NOT NULL AND a.ancestor_id<>a.conversation_id AND a.depth<32
), cyclic(id) AS MATERIALIZED (
 SELECT DISTINCT conversation_id FROM ancestors WHERE ancestor_id=conversation_id
), matches AS MATERIALIZED (
 SELECT m.rowid copy_row,o.rowid original_row,an.depth,c.provider,c.account,c.id IN (SELECT id FROM cyclic) cyclic
 FROM ancestors an JOIN conversations c ON c.id=an.conversation_id AND an.ancestor_id<>an.conversation_id
 JOIN conversations p ON p.id=an.ancestor_id AND p.provider=c.provider AND p.account=c.account
 JOIN messages m ON m.conversation_id=c.id
 JOIN messages o ON o.conversation_id=p.id AND o.native_id=m.native_id AND o.rowid<>m.rowid
 WHERE c.provider IN (SELECT value FROM json_each(?)) AND m.native_id<>''
 AND m.native_id NOT GLOB 'line-[0-9]*' AND m.native_id NOT GLOB 'skills-[0-9]*' AND m.native_id NOT GLOB 'compact-[0-9]*'
)
SELECT m.id copy_id,m.conversation_id,m.native_id,o.id original_id,o.conversation_id original_conversation,x.provider,x.account,x.cyclic,
 (SELECT am.agent_session_id FROM agent_session_messages am WHERE am.message_id=o.id ORDER BY am.agent_session_id LIMIT 1) original_session
 FROM matches x JOIN messages m ON m.rowid=x.copy_row JOIN messages o ON o.rowid=x.original_row
 ORDER BY m.id,x.depth`, args...)
	if err != nil {
		return nil, err
	}
	out, seen := []executionClaim{}, map[string]bool{}
	for _, row := range rows {
		id := firstString(row["copy_id"])
		if seen[id] || syntheticMessageID(firstString(row["native_id"])) {
			continue
		}
		seen[id] = true
		context := sourceRef{sourceConversation, firstString(row["original_conversation"])}
		if session := firstString(row["original_session"]); session != "" {
			context = sourceRef{sourceSession, session}
		}
		claim := executionClaim{Kind: claimInheritedCopy, Subject: sourceRef{sourceMessage, id}, Object: sourceRef{sourceMessage, firstString(row["original_id"])},
			Strength: evidenceDerived, Namespace: firstString(row["provider"]) + "/" + firstString(row["account"]), IDKind: idKindNativeEvent,
			Rule: "ancestor-native-message-id/v1", Locator: sourceRef{sourceConversation, firstString(row["conversation_id"])}, Context: context}
		if integer(row["cyclic"]) != 0 {
			claim.Kind, claim.Strength, claim.Rule = claimCopyCandidate, evidenceSynthesized, "ancestor-cycle/v1"
		}
		out = append(out, claim)
	}
	return out, nil
}

// occurrenceCoverageClaims names transcripts whose copied history the
// evidence cannot account for. Without current typed evidence
// (untyped-ancestry), or with typed evidence re-derived from retained rows
// (retained-history-undeclared), the history declaration was not read. A
// provider read without a declaration is counted as recorded, without marking
// coverage partial solely for that absence. A declared copy whose source is
// not retained, or not uniquely retained, is also partial. Each partial case
// is a copy_candidate whose subject is the conversation: its own observations
// stay counted, and consumers report coverage as partial.
func occurrenceCoverageClaims(ctx context.Context, q queryer, conversations []string) ([]executionClaim, error) {
	typed, err := executionEvidenceTablesPresent(ctx, q)
	if err != nil {
		return nil, err
	}
	where, args := executionScope("c.id", conversations)
	state := `NULL`
	if typed {
		state = `(SELECT CASE WHEN EXISTS(SELECT 1 FROM execution_claims e WHERE e.conversation_id=c.id AND e.kind=?) THEN 'declared'
 WHEN s.input='retained' THEN 'retained' ELSE 'undeclared' END FROM execution_evidence_state s WHERE s.conversation_id=c.id AND s.version=?)`
		args = append([]any{evidenceHistoryCopy, executionEvidenceVersion}, args...)
	}
	rows, err := queryMapsContext(ctx, q, `SELECT c.id,`+state+` state FROM conversations c WHERE c.parent_id IS NOT NULL`+where, args...)
	if err != nil {
		return nil, err
	}
	var out []executionClaim
	for _, row := range rows {
		id, rule := firstString(row["id"]), ""
		switch firstString(row["state"]) {
		case "declared", "undeclared":
			continue
		case "retained":
			rule = "retained-history-undeclared/v1"
		default:
			rule = "untyped-ancestry/v1"
		}
		out = append(out, executionClaim{Kind: claimCopyCandidate, Subject: sourceRef{sourceConversation, id}, Strength: evidenceSynthesized,
			Rule: rule, Locator: sourceRef{sourceConversation, id}})
	}
	if !typed {
		return out, nil
	}
	where, args = executionScope("e.conversation_id", conversations)
	declared, err := queryMapsContext(ctx, q, `SELECT e.conversation_id,e.object,e.rule,e.detail_json FROM execution_claims e WHERE e.kind=?`+where,
		append([]any{evidenceHistoryCopy}, args...)...)
	if err != nil {
		return nil, err
	}
	// Retained sources of every declared object, in one lookup.
	var keys [][3]string
	for _, row := range declared {
		if parts := strings.SplitN(strings.TrimPrefix(firstString(row["object"]), "native:"), "|", 3); len(parts) == 3 {
			keys = append(keys, [3]string{parts[0], parts[1], parts[2]})
		}
	}
	sources := map[[3]string]int{}
	if len(keys) > 0 {
		found, err := queryMapsContext(ctx, q, `SELECT c.provider,c.account,c.native_id,COUNT(*) n FROM conversations c
 JOIN (SELECT DISTINCT value->>0 provider,value->>1 account,value->>2 native_id FROM json_each(?)) k
 ON c.provider=k.provider AND c.account=k.account AND c.native_id=k.native_id GROUP BY 1,2,3`, jsonText(keys))
		if err != nil {
			return nil, err
		}
		for _, row := range found {
			sources[[3]string{firstString(row["provider"]), firstString(row["account"]), firstString(row["native_id"])}] = int(integer(row["n"]))
		}
	}
	for _, row := range declared {
		id := firstString(row["conversation_id"])
		var detail map[string]any
		_ = json.Unmarshal([]byte(firstString(row["detail_json"])), &detail)
		if historyDeclaresNoCopies(firstString(row["rule"]), detail) {
			continue
		}
		n := 0
		if parts := strings.SplitN(strings.TrimPrefix(firstString(row["object"]), "native:"), "|", 3); len(parts) == 3 {
			n = sources[[3]string{parts[0], parts[1], parts[2]}]
		}
		if n != 1 {
			out = append(out, executionClaim{Kind: claimCopyCandidate, Subject: sourceRef{sourceConversation, id}, Strength: evidenceSynthesized,
				Rule: "declared-copy-source-missing/v1", Locator: sourceRef{sourceConversation, id}})
		}
	}
	return out, nil
}

// historyDeclaresNoCopies reports a history_copy declaration after which the
// transcript holds no copy to account for: the child starts without copied
// history (mapping "none"), or Cursor copies a mapping proved were left out
// of the fork at ingestion ("shared-native-ids"). Every other declaration,
// Cursor's unmapped, ordinal and unverified copies included, leaves copies in
// the transcript, so its source must be retained (uniquely) to account for
// them. Occurrence coverage and the persisted copy reading share it.
func historyDeclaresNoCopies(rule string, detail map[string]any) bool {
	mapping := firstString(detail["mapping"])
	return mapping == "none" || rule == "cursor-fork-length-at-spawn/v1" && mapping == "shared-native-ids"
}

// providerEventIDs are providers whose stored message IDs are the provider's
// own event IDs except for synthesized locators (the evidence extractor's
// executionIDPolicy declares the same).
var providerEventIDs = map[string]bool{"codex": true, "claude": true, "cursor": true, "tl1": true}

// syntheticMessageID reports a locator an adapter generated from a line
// number when the source recorded no event ID (adapters.go, adapter_tl1.go).
func syntheticMessageID(native string) bool {
	for _, prefix := range []string{"line-", "skills-", "compact-"} {
		rest, ok := strings.CutPrefix(native, prefix)
		if !ok || rest == "" {
			continue
		}
		digits := true
		for _, r := range rest {
			digits = digits && r >= '0' && r <= '9'
		}
		if digits {
			return true
		}
	}
	return false
}
