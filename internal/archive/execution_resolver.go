package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
)

// executionResolution is the immutable result for one evidence generation.
// It holds compact identity and structure only; transcript bodies and
// occurrence claims stay out of it (see Occurrences). Never modify one after
// resolveExecutions returns it: cached copies are shared between requests.
type executionResolution struct {
	Revision   executionRevision
	Executions map[executionID]*execution
	Attempts   map[sourceRef]*launchAttempt
	Conflicts  []identityConflict
	Coverage   resolutionCoverage

	streams        map[sourceRef]*executionStream
	bindings       map[sourceRef]executionBinding
	mainSessions   map[string]sourceRef // main agent_sessions.id → its conversation stream
	inConversation map[string][]sourceRef
	children       map[executionID][]executionID
	ancestry       map[executionID]executionAncestry
	// detached lists, per known family root, the topmost executions of its
	// family whose direct parent is unresolved.
	detached map[executionID][]executionID
	// launches is each launch session's issuing claim (its launch message
	// and owning stream); copies, each copied launch's copy claim.
	launches map[sourceRef]executionClaim
	copies   map[sourceRef]executionClaim
}

// Parent precedence after provenance validation: originating launch
// ownership, recorded session parent, provider-typed delegation parent, then
// inferred command launch. Lower ranks win; strength breaks ties within one.
var parentClaimRank = map[claimKind]int{
	claimLaunchOwner:      1,
	claimSessionParent:    2,
	claimTranscriptParent: 3,
	claimCommandLaunch:    4,
}

// identityAuthority is how much weight a claim carries when identity claims
// and creation edges disagree. Provenance is not authority: a launch is never
// the stream that issued it, and a verified acknowledgement is the provider's
// own record that the launch created an execution, even when matching its
// handle to a transcript was a parser decision. Either outranks native-
// labelled continuation or mirror metadata only when that is weaker; an equal
// contradiction stays an unresolved conflict. Legacy links keep their strength.
func identityAuthority(claim executionClaim) evidenceStrength {
	switch claim.Kind {
	case claimLaunchOwner, claimLaunchAccepted, claimChildLink:
		if claim.Strength >= evidenceDerived {
			return evidenceNative
		}
	}
	return claim.Strength
}

func streamClaim(kind claimKind) bool {
	switch kind {
	case claimInheritedCopy, claimCopyCandidate, claimDuplicateObservation:
		return false
	}
	return true
}

// normalizeExecutionClaims maps main sessions onto their conversation stream,
// keeps one claim per (kind, subject, object) and sorts the result, so the
// resolution depends on the admitted evidence, never on the order rows loaded.
// A synthesized duplicate overrides an unknown-provenance (legacy) reading of
// the same relationship: knowing that a stored edge was a fallback is stronger
// than not knowing how it was made. Otherwise the strongest duplicate wins.
func normalizeExecutionClaims(claims []executionClaim, mains map[string]sourceRef) []executionClaim {
	ref := func(r sourceRef) sourceRef {
		if r.Kind == sourceSession {
			if main, ok := mains[r.ID]; ok {
				return main
			}
		}
		return r
	}
	type key struct {
		kind            claimKind
		subject, object sourceRef
	}
	kept := map[key]executionClaim{}
	for _, claim := range claims {
		claim.Subject, claim.Object, claim.Context = ref(claim.Subject), ref(claim.Object), ref(claim.Context)
		if claim.Subject.IsZero() || claim.Strength == 0 {
			continue
		}
		k := key{claim.Kind, claim.Subject, claim.Object}
		prior, seen := kept[k]
		switch {
		case !seen:
			kept[k] = claim
		case claim.Strength == evidenceSynthesized && prior.Strength == evidenceLegacy:
			kept[k] = claim
		case prior.Strength == evidenceSynthesized && claim.Strength == evidenceLegacy:
		case claim.Strength > prior.Strength || claim.Strength == prior.Strength && claim.Rule < prior.Rule:
			kept[k] = claim
		}
	}
	out := make([]executionClaim, 0, len(kept))
	for _, claim := range kept {
		out = append(out, claim)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if x, y := identityAuthority(a), identityAuthority(b); x != y {
			return x > y
		}
		// Within one authority, creation records bind first, so contradicting
		// identity metadata is what gets flagged rather than applied.
		if x, y := a.Kind == claimLaunchAccepted || a.Kind == claimChildLink, b.Kind == claimLaunchAccepted || b.Kind == claimChildLink; x != y {
			return x
		}
		if a.Strength != b.Strength {
			return a.Strength > b.Strength
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Subject != b.Subject {
			return a.Subject.String() < b.Subject.String()
		}
		return a.Object.String() < b.Object.String()
	})
	return out
}

// executionUnion is a union-find over streams that also remembers which
// components evidence says are distinct executions (one created the other).
type executionUnion struct {
	parent   map[sourceRef]sourceRef
	distinct map[sourceRef]map[sourceRef]evidenceStrength // stream → stream it must differ from
}

func (u *executionUnion) find(r sourceRef) sourceRef {
	for {
		p, ok := u.parent[r]
		if !ok || p == r {
			return r
		}
		if gp := u.parent[p]; gp != p {
			u.parent[r] = gp
		}
		r = p
	}
}

// constraint is the strongest creation edge between two components.
func (u *executionUnion) constraint(x, y sourceRef, members map[sourceRef][]sourceRef) evidenceStrength {
	strongest := evidenceStrength(0)
	small, other := x, y
	if len(members[x]) > len(members[y]) {
		small, other = y, x
	}
	for _, m := range members[small] {
		for peer, s := range u.distinct[m] {
			if s > strongest && u.find(peer) == other {
				strongest = s
			}
		}
	}
	return strongest
}

// sameCallAncestryLimit bounds each ancestry walk in sameCallCopies.
var sameCallAncestryLimit = 256

// sameCallCopies finds launch calls a transcript retains from one it
// descends from when no other evidence matched them (the original's row is
// outside the recorded ancestry walk, or its body was normalized
// differently). Two launches are one call when they share a provider call ID
// in one namespace, one transcript descends from the other (recorded
// ancestry or declared history), and both acknowledge creating the same
// transcript: the descendant retains the ancestor's call. A shared call ID
// alone is not enough (TestDelegationCopiedLaunchesAcrossWorkspaces); without
// all three both launches stay, and the child's parent stays ambiguous. On
// cyclic ancestry each transcript descends from the other, so neither is the
// original: both launches stay (TestExecutionOccurrencesOnCyclicAncestry). A
// walk cut off at sameCallAncestryLimit proves no ancestry either way, so it
// leaves both launches too.
func (r *executionResolution) sameCallCopies(claims []executionClaim) []executionClaim {
	targets := map[sourceRef]map[sourceRef]bool{}
	for _, claim := range claims {
		if (claim.Kind == claimLaunchAccepted || claim.Kind == claimChildLink) && !claim.Object.IsZero() && claim.Strength >= evidenceLegacy {
			if targets[claim.Subject] == nil {
				targets[claim.Subject] = map[sourceRef]bool{}
			}
			targets[claim.Subject][claim.Object] = true
		}
	}
	sameTarget := func(a, b sourceRef) bool {
		return len(targets[a]) == 1 && maps.Equal(targets[a], targets[b])
	}
	type call struct{ namespace, id string }
	launches := map[call][]*executionStream{}
	for _, s := range r.streams {
		if s.Ref.Kind == sourceSession && s.Launch && s.NativeID != "" && providerEventIDs[s.Provider] {
			k := call{s.Provider + "/" + s.Account, s.NativeID}
			launches[k] = append(launches[k], s)
		}
	}
	conversationOf := func(ref sourceRef) string {
		if s := r.streams[ref]; s != nil {
			return s.ConversationID
		}
		return ""
	}
	above := map[string][]string{}
	for _, claim := range claims {
		switch claim.Kind {
		case claimTranscriptParent, claimSessionParent, claimContinuation, claimMirror, claimCommandLaunch, claimFamilyMember:
			if child, parent := conversationOf(claim.Subject), conversationOf(claim.Object); child != "" && parent != "" && child != parent {
				above[child] = append(above[child], parent)
			}
		}
	}
	// descends reports whether to is above from, and whether the walk saw all
	// of from's ancestry.
	descends := func(from, to string) (found, complete bool) {
		seen := map[string]bool{from: true}
		queue := []string{from}
		complete = true
		for len(queue) > 0 {
			at := queue[0]
			queue = queue[1:]
			for _, next := range above[at] {
				if next == to {
					return true, true
				}
				if seen[next] {
					continue
				}
				if len(seen) >= sameCallAncestryLimit {
					complete = false
					continue
				}
				seen[next] = true
				queue = append(queue, next)
			}
		}
		return false, complete
	}
	var out []executionClaim
	for _, group := range launches {
		if len(group) < 2 {
			continue
		}
		for _, copy := range group {
			var originals []*executionStream
			for _, original := range group {
				if original == copy || original.ConversationID == copy.ConversationID || !sameTarget(copy.Ref, original.Ref) {
					continue
				}
				below, _ := descends(copy.ConversationID, original.ConversationID)
				cyclic, complete := descends(original.ConversationID, copy.ConversationID)
				if below && !cyclic && complete {
					originals = append(originals, original)
				}
			}
			// The nearest is the one no other candidate descends from.
			sort.Slice(originals, func(i, j int) bool { return originals[i].Ref.String() < originals[j].Ref.String() })
			for _, original := range originals {
				nearest := true
				for _, other := range originals {
					// other's whole ancestry was walked to admit it, so this
					// walk is complete too.
					if closer, _ := descends(other.ConversationID, original.ConversationID); other != original && closer {
						nearest = false
					}
				}
				if nearest {
					out = append(out, executionClaim{Kind: claimInheritedCopy, Subject: copy.Ref, Object: original.Ref, Strength: evidenceDerived,
						Namespace: copy.Provider + "/" + copy.Account, IDKind: idKindCallID, Rule: "descendant-same-call-id/v1", Locator: copy.Ref})
					break
				}
			}
		}
	}
	return out
}

func resolveExecutions(e *executionEvidence) *executionResolution {
	r := &executionResolution{
		Revision:   executionRevision{Resolver: executionResolverVersion, Generation: e.Generation},
		Executions: map[executionID]*execution{}, Attempts: map[sourceRef]*launchAttempt{},
		streams: map[sourceRef]*executionStream{}, bindings: map[sourceRef]executionBinding{},
		mainSessions: map[string]sourceRef{}, inConversation: map[string][]sourceRef{},
		children: map[executionID][]executionID{}, ancestry: map[executionID]executionAncestry{},
		detached: map[executionID][]executionID{}, launches: map[sourceRef]executionClaim{},
	}
	r.Coverage.Missing = append([]string(nil), e.Missing...)
	sort.Strings(r.Coverage.Missing)
	refs := []sourceRef{}
	for i := range e.Streams {
		s := e.Streams[i]
		if s.Ref.IsZero() || r.streams[s.Ref] != nil {
			continue
		}
		r.streams[s.Ref] = &s
		refs = append(refs, s.Ref)
		if s.Ref.Kind == sourceConversation && s.MainSessionID != "" {
			r.mainSessions[s.MainSessionID] = s.Ref
		}
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].String() < refs[j].String() })
	for _, ref := range refs {
		cid := r.streams[ref].ConversationID
		r.inConversation[cid] = append(r.inConversation[cid], ref)
	}
	claims := normalizeExecutionClaims(e.Claims, r.mainSessions)
	bySubject := map[sourceRef][]executionClaim{}
	for _, claim := range claims {
		if streamClaim(claim.Kind) || claim.Subject.Kind == sourceSession {
			bySubject[claim.Subject] = append(bySubject[claim.Subject], claim)
		}
	}

	// Copied launch streams bind to their original instead of forming an
	// execution. Independently reported usage or observed stream activity is
	// execution evidence of its own.
	copies := map[sourceRef]executionClaim{}
	for _, claim := range append(slices.Clone(claims), r.sameCallCopies(claims)...) {
		s := r.streams[claim.Subject]
		if claim.Kind == claimInheritedCopy && claim.Subject.Kind == sourceSession && s != nil && claim.Strength >= evidenceLegacy && s.ReportedTokens == 0 && !s.Observed {
			if _, dup := copies[claim.Subject]; !dup {
				copies[claim.Subject] = claim
			}
		}
	}
	r.copies = copies
	for _, claim := range claims {
		if claim.Kind == claimLaunchOwner && claim.Locator.Kind == sourceMessage && claim.Strength >= evidenceLegacy {
			if _, dup := r.launches[claim.Subject]; !dup {
				r.launches[claim.Subject] = claim
			}
		}
	}

	u := &executionUnion{parent: map[sourceRef]sourceRef{}, distinct: map[sourceRef]map[sourceRef]evidenceStrength{}}
	members := map[sourceRef][]sourceRef{}
	for _, ref := range refs {
		if _, copied := copies[ref]; copied {
			continue
		}
		u.parent[ref] = ref
		members[ref] = []sourceRef{ref}
	}
	// A verified creation edge keeps its two ends distinct executions.
	for _, claim := range claims {
		if _, ok := parentClaimRank[claim.Kind]; !ok || claim.Strength < evidenceLegacy || claim.Object.IsZero() {
			continue
		}
		if _, ok := u.parent[claim.Subject]; !ok {
			continue
		}
		if _, ok := u.parent[claim.Object]; !ok {
			continue
		}
		for _, pair := range [][2]sourceRef{{claim.Subject, claim.Object}, {claim.Object, claim.Subject}} {
			if u.distinct[pair[0]] == nil {
				u.distinct[pair[0]] = map[sourceRef]evidenceStrength{}
			}
			u.distinct[pair[0]][pair[1]] = max(u.distinct[pair[0]][pair[1]], identityAuthority(claim))
		}
	}
	// A launch identifies only the transcripts named at its strongest level
	// across all of its targets: a weaker acknowledgement or link naming
	// another transcript contradicts it and joins nothing (it stays in the
	// launch's evidence). Several transcripts named at that level identify one
	// execution only if other evidence makes them one; otherwise the launch
	// identifies none of them, a conflict rather than a merge.
	launchTargets := map[sourceRef]map[sourceRef]evidenceStrength{}
	for _, claim := range claims {
		if (claim.Kind == claimLaunchAccepted || claim.Kind == claimChildLink) && !claim.Object.IsZero() && claim.Strength >= evidenceLegacy {
			if launchTargets[claim.Subject] == nil {
				launchTargets[claim.Subject] = map[sourceRef]evidenceStrength{}
			}
			launchTargets[claim.Subject][claim.Object] = max(launchTargets[claim.Subject][claim.Object], identityAuthority(claim))
		}
	}
	launchBest := map[sourceRef]evidenceStrength{}
	// A verified acknowledgement whose handle no retained transcript answers
	// still names the execution the launch created: a retained target named
	// only more weakly contradicts it, and joins nothing. (At equal strength
	// the retained target may be the same handle read another way, so it
	// still decides.)
	for _, claim := range claims {
		if claim.Kind == claimLaunchAccepted && claim.Object.IsZero() && claim.Strength >= evidenceLegacy {
			launchBest[claim.Subject] = max(launchBest[claim.Subject], identityAuthority(claim))
		}
	}
	splitTargets := map[sourceRef][]sourceRef{}
	for launch, targets := range launchTargets {
		best, objects := evidenceStrength(0), []sourceRef{}
		for object, s := range targets {
			if s > best {
				best, objects = s, []sourceRef{object}
			} else if s == best {
				objects = append(objects, object)
			}
		}
		if best < launchBest[launch] {
			continue // only an unanswered handle is named at the strongest level
		}
		launchBest[launch] = best
		if len(objects) > 1 {
			sort.Slice(objects, func(i, j int) bool { return objects[i].String() < objects[j].String() })
			splitTargets[launch] = objects
		}
	}
	conflicted := map[sourceRef]bool{}
	rejected := map[executionClaim]string{}
	// join applies one identity claim: it unions the two components unless a
	// creation edge between them is as strong or stronger.
	join := func(claim executionClaim) {
		if _, ok := u.parent[claim.Subject]; !ok {
			return
		}
		if _, ok := u.parent[claim.Object]; !ok {
			return
		}
		x, y := u.find(claim.Subject), u.find(claim.Object)
		if x == y {
			return
		}
		switch limit, authority := u.constraint(x, y, members), identityAuthority(claim); {
		case limit > authority:
			rejected[claim] = "contradicts a stronger creation edge"
			return
		case limit == authority:
			// Equally authoritative evidence disagrees about how many executions
			// exist. Keep both partitions and mark their cardinality unresolved.
			conflict := identityConflict{Claims: []executionClaim{claim}, Reason: "identity contradicts an equally strong creation edge"}
			for _, m := range append(append([]sourceRef{}, members[x]...), members[y]...) {
				conflicted[m] = true
				conflict.Sources = append(conflict.Sources, m)
			}
			sort.Slice(conflict.Sources, func(i, j int) bool { return conflict.Sources[i].String() < conflict.Sources[j].String() })
			r.Conflicts = append(r.Conflicts, conflict)
			return
		}
		if len(members[x]) < len(members[y]) {
			x, y = y, x
		}
		u.parent[y] = x
		members[x] = append(members[x], members[y]...)
		delete(members, y)
	}
	for _, claim := range claims {
		if claim.Object.IsZero() || claim.Strength < evidenceLegacy {
			continue
		}
		switch claim.Kind {
		case claimLaunchAccepted, claimChildLink:
			authority := identityAuthority(claim)
			switch {
			case launchTargets[claim.Subject][claim.Object] != authority:
				// A weaker duplicate of a target a stronger claim names.
			case authority < launchBest[claim.Subject]:
				rejected[claim] = "contradicts a stronger acknowledgement of the same launch"
			case splitTargets[claim.Subject] == nil:
				join(claim)
			}
		case claimContinuation, claimMirror:
			join(claim)
		}
	}
	// Equally strong targets of one launch, once every other identity is
	// applied: one execution under several names (aliases) is identified;
	// several executions are a conflict, whatever order the claims came in.
	for _, launch := range slices.SortedFunc(maps.Keys(splitTargets), func(a, b sourceRef) int { return strings.Compare(a.String(), b.String()) }) {
		objects, best := splitTargets[launch], launchBest[launch]
		var strongest []executionClaim
		for _, claim := range bySubject[launch] {
			if (claim.Kind == claimLaunchAccepted || claim.Kind == claimChildLink) && !claim.Object.IsZero() && claim.Strength >= evidenceLegacy && identityAuthority(claim) == best {
				strongest = append(strongest, claim)
			}
		}
		one := true
		for _, object := range objects {
			_, known := u.parent[object]
			one = one && known && u.find(object) == u.find(objects[0])
		}
		if one {
			for _, claim := range strongest {
				join(claim)
			}
			continue
		}
		conflicted[launch] = true
		r.Conflicts = append(r.Conflicts, identityConflict{Sources: append([]sourceRef{launch}, objects...), Claims: strongest, Reason: "one launch acknowledges several transcripts"})
	}
	// Conflicts are found by iterating maps: list them in a stable order.
	for i := range r.Conflicts {
		sort.Slice(r.Conflicts[i].Claims, func(a, b int) bool {
			x, y := r.Conflicts[i].Claims[a], r.Conflicts[i].Claims[b]
			return fmt.Sprint(x.Kind, x.Subject, x.Object, x.Strength) < fmt.Sprint(y.Kind, y.Subject, y.Object, y.Strength)
		})
	}
	sort.SliceStable(r.Conflicts, func(i, j int) bool {
		a, b := r.Conflicts[i], r.Conflicts[j]
		if a.Reason != b.Reason {
			return a.Reason < b.Reason
		}
		return fmt.Sprint(a.Sources) < fmt.Sprint(b.Sources)
	})
	r.Coverage.Conflicts = len(r.Conflicts)

	// Existence: a retained transcript, verified creation or observed activity
	// makes an execution. Otherwise the component is an attempt.
	roots := make([]sourceRef, 0, len(members))
	for root := range members {
		roots = append(roots, root)
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].String() < roots[j].String() })
	componentExecution := map[sourceRef]executionID{}
	continuing := map[sourceRef]bool{}
	for _, claim := range claims {
		if claim.Kind == claimContinuation && rejected[claim] == "" && u.find(claim.Subject) == u.find(claim.Object) {
			continuing[claim.Subject] = true
		}
	}
	for _, root := range roots {
		ms := members[root]
		sort.Slice(ms, func(i, j int) bool { return ms[i].String() < ms[j].String() })
		var conversations, sessions []sourceRef
		presence, accepted, rejectedLaunch := "", false, false
		for _, m := range ms {
			s := r.streams[m]
			if m.Kind == sourceConversation {
				conversations = append(conversations, m)
				presence = "observed"
				continue
			}
			sessions = append(sessions, m)
			// A session that is not a launch call exists only because the
			// provider recorded its stream; a launch needs an outcome or activity.
			if s.Observed || s.ReportedTokens > 0 || !s.Launch {
				presence = "observed"
			}
			for _, claim := range bySubject[m] {
				switch {
				case claim.Strength < evidenceLegacy:
				case claim.Kind == claimLaunchAccepted || claim.Kind == claimChildLink:
					accepted = true
				case claim.Kind == claimLaunchRejected:
					rejectedLaunch = true
				}
			}
		}
		if presence == "" && accepted {
			presence = "verified-creation"
		}
		if presence == "" {
			outcome := attemptUnresolved
			if rejectedLaunch {
				outcome = attemptRejected
			}
			for _, m := range ms {
				a := &launchAttempt{Ref: m, Outcome: outcome, Evidence: bySubject[m]}
				r.Attempts[m] = a
				r.bindings[m] = executionBinding{State: bindingAttempt, Evidence: bySubject[m]}
			}
			continue
		}
		// The origin transcript anchors the execution: not a continuation of
		// another member, earliest ID among equals.
		var id executionID
		x := &execution{Presence: presence}
		if len(conversations) > 0 {
			origin := conversations[0]
			for _, c := range conversations {
				if !continuing[c] {
					origin = c
					break
				}
			}
			segments := append([]sourceRef(nil), conversations...)
			sort.SliceStable(segments, func(i, j int) bool {
				a, b := segments[i], segments[j]
				if (a == origin) != (b == origin) {
					return a == origin
				}
				if continuing[a] != continuing[b] {
					return !continuing[a]
				}
				if r.streams[a].At != r.streams[b].At {
					return r.streams[a].At < r.streams[b].At
				}
				return a.ID < b.ID
			})
			for _, c := range segments {
				x.Segments = append(x.Segments, c.ID)
			}
			id = executionID("conversation:" + origin.ID)
			x.Conversation = origin.ID
		} else {
			anchor := sessions[0]
			for _, m := range sessions {
				for _, claim := range bySubject[m] {
					if claim.Kind == claimLaunchAccepted && claim.Strength >= evidenceLegacy {
						anchor = m
						break
					}
				}
			}
			id = executionID("agent:" + anchor.ID)
		}
		x.ID = id
		x.Streams = ms
		lead := r.streams[ms[0]]
		if x.Conversation != "" {
			lead = r.streams[sourceRef{sourceConversation, x.Conversation}]
		}
		x.WorkspaceID, x.Provider = lead.WorkspaceID, lead.Provider
		typed := 0
		for _, m := range ms {
			if conflicted[m] {
				x.Conflicted = true
			}
			if r.streams[m].Typed {
				typed++
			}
		}
		x.Evidence = "mixed"
		switch typed {
		case len(ms):
			x.Evidence = "typed"
		case 0:
			x.Evidence = "legacy"
		}
		componentExecution[root] = id
		r.Executions[id] = x
		for _, m := range ms {
			r.bindings[m] = executionBinding{ExecutionID: id, State: bindingResolved, Evidence: bySubject[m]}
		}
	}
	// Copies bind through their original, following copy chains.
	for copy, claim := range copies {
		binding := executionBinding{State: bindingInherited, Evidence: []executionClaim{claim}}
		at, seen := claim.Object, map[sourceRef]bool{copy: true}
		for !at.IsZero() && !seen[at] {
			seen[at] = true
			if next, ok := copies[at]; ok {
				at = next.Object
				continue
			}
			if b, ok := r.bindings[at]; ok && b.State == bindingResolved {
				binding.ExecutionID = b.ExecutionID
			}
			break
		}
		r.bindings[copy] = binding
	}

	// Parents, chosen from every member's claims after identity is settled.
	for _, id := range sortedExecutionIDs(r.Executions) {
		x := r.Executions[id]
		type candidate struct {
			parent executionID
			claim  executionClaim
			rank   int
		}
		var qualified []candidate
		hints, selfCycle := false, false
		families := map[executionID]evidenceStrength{}
		for _, m := range x.Streams {
			for _, claim := range bySubject[m] {
				if claim.Kind == claimFamilyMember && claim.Strength >= evidenceLegacy {
					if b := r.bindings[claim.Object]; b.State == bindingResolved && b.ExecutionID != id {
						families[b.ExecutionID] = max(families[b.ExecutionID], claim.Strength)
					}
					continue
				}
				rank, ok := parentClaimRank[claim.Kind]
				if !ok {
					continue
				}
				if claim.Subject == claim.Object && claim.Strength >= evidenceLegacy {
					// A source that names itself as its own parent is a cycle.
					selfCycle = true
					x.Parent.Candidates = append(x.Parent.Candidates, parentClaim{Parent: id, Claim: claim, Reason: "names itself"})
					continue
				}
				parent := r.Bind(claim.Object)
				if parent.State != bindingResolved {
					hints = true
					x.Parent.Candidates = append(x.Parent.Candidates, parentClaim{Claim: claim, Reason: "the named parent is not a resolved execution"})
					continue
				}
				if parent.ExecutionID == id {
					x.Parent.Candidates = append(x.Parent.Candidates, parentClaim{Parent: id, Claim: claim, Reason: "names the same execution"})
					continue
				}
				if claim.Strength < evidenceLegacy {
					hints = true
					x.Parent.Candidates = append(x.Parent.Candidates, parentClaim{Parent: parent.ExecutionID, Claim: claim, Reason: "synthesized; a discovery hint only"})
					continue
				}
				qualified = append(qualified, candidate{parent.ExecutionID, claim, rank})
			}
		}
		sort.SliceStable(qualified, func(i, j int) bool {
			a, b := qualified[i], qualified[j]
			if a.rank != b.rank {
				return a.rank < b.rank
			}
			return a.claim.Strength > b.claim.Strength
		})
		switch {
		case x.Conflicted:
			x.Parent.State = parentConflict
			for _, c := range qualified {
				x.Parent.Candidates = append(x.Parent.Candidates, parentClaim{Parent: c.parent, Claim: c.claim, Reason: "identity is unresolved"})
			}
		case len(qualified) > 0:
			best := qualified[0]
			parents := map[executionID]bool{}
			for _, c := range qualified {
				if c.rank == best.rank && c.claim.Strength == best.claim.Strength {
					parents[c.parent] = true
					x.Parent.Evidence = append(x.Parent.Evidence, c.claim)
				} else {
					x.Parent.Candidates = append(x.Parent.Candidates, parentClaim{Parent: c.parent, Claim: c.claim, Reason: "outranked"})
				}
			}
			x.Parent.Certainty = best.claim.Strength
			if len(parents) == 1 {
				x.Parent.State, x.Parent.ParentID = parentResolved, best.parent
			} else {
				x.Parent.State = parentAmbiguous
				for _, c := range x.Parent.Evidence {
					x.Parent.Candidates = append(x.Parent.Candidates, parentClaim{Parent: r.bindings[c.Object].ExecutionID, Claim: c, Reason: "equally authoritative"})
				}
				x.Parent.Evidence = nil
			}
		case selfCycle:
			x.Parent.State = parentCycle
			r.Coverage.Cycles++
		default:
			x.Parent.State = parentRoot
			for _, m := range x.Streams {
				s := r.streams[m]
				if hints || len(families) > 0 || s.RecordedDepth > 0 || m.Kind == sourceSession {
					x.Parent.State = parentMissing
				}
			}
		}
		if x.Parent.State != parentResolved && x.Parent.State != parentRoot && len(families) == 1 {
			for family, strength := range families {
				x.FamilyRoot, x.FamilyCertainty = family, strength
			}
		}
	}
	// Cycles are found after identity; a cycle stays unresolved as a whole.
	state := map[executionID]int{}
	for _, id := range sortedExecutionIDs(r.Executions) {
		path := []executionID{}
		at := id
		for at != "" && state[at] == 0 {
			state[at] = 1
			path = append(path, at)
			x := r.Executions[at]
			if x.Parent.State != parentResolved {
				at = ""
				break
			}
			at = x.Parent.ParentID
		}
		if at != "" && state[at] == 1 {
			inCycle := false
			for _, p := range path {
				if p == at {
					inCycle = true
				}
				if inCycle {
					x := r.Executions[p]
					x.Parent.Candidates = append(x.Parent.Candidates, parentClaim{Parent: x.Parent.ParentID, Claim: firstClaim(x.Parent.Evidence), Reason: "part of an ownership cycle"})
					x.Parent.State, x.Parent.ParentID = parentCycle, ""
					r.Coverage.Cycles++
				}
			}
		}
		for _, p := range path {
			state[p] = 2
		}
	}
	for _, id := range sortedExecutionIDs(r.Executions) {
		x := r.Executions[id]
		x.Label = r.chooseLabel(x, bySubject)
		x.Order = r.chooseOrder(x, bySubject)
		if x.Parent.State == parentResolved {
			r.children[x.Parent.ParentID] = append(r.children[x.Parent.ParentID], id)
		} else if x.Parent.State != parentRoot {
			r.Coverage.UnresolvedParents++
		}
	}
	for parent := range r.children {
		sort.Slice(r.children[parent], func(i, j int) bool { return r.Less(r.children[parent][i], r.children[parent][j]) })
	}
	for _, id := range sortedExecutionIDs(r.Executions) {
		if a := r.resolveAncestry(id); !a.Known && a.Root != "" && (len(a.Chain) == 0 || r.Executions[a.Chain[len(a.Chain)-1]].Parent.State != parentResolved) {
			top := id
			if len(a.Chain) > 0 {
				top = a.Chain[len(a.Chain)-1]
			}
			if !containsExecution(r.detached[a.Root], top) {
				r.detached[a.Root] = append(r.detached[a.Root], top)
			}
		}
	}
	for root := range r.detached {
		sort.Slice(r.detached[root], func(i, j int) bool { return r.Less(r.detached[root][i], r.detached[root][j]) })
	}
	for ref, a := range r.Attempts {
		for _, claim := range bySubject[ref] {
			if claim.Kind == claimLaunchOwner && r.bindings[claim.Object].State == bindingResolved {
				a.Owner = r.bindings[claim.Object].ExecutionID
			}
		}
	}
	r.Coverage.Attempts = len(r.Attempts)
	return r
}

func firstClaim(claims []executionClaim) executionClaim {
	if len(claims) == 0 {
		return executionClaim{}
	}
	return claims[0]
}

func sortedExecutionIDs(m map[executionID]*execution) []executionID {
	ids := make([]executionID, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (r *executionResolution) resolveAncestry(id executionID) executionAncestry {
	if a, ok := r.ancestry[id]; ok {
		return a
	}
	x := r.Executions[id]
	var a executionAncestry
	switch {
	case x == nil:
		a = executionAncestry{State: parentMissing}
	case x.Parent.State == parentRoot:
		a = executionAncestry{State: parentRoot, Root: id, Known: true, Certainty: evidenceNative, Conflicted: x.Conflicted}
	case x.Parent.State == parentResolved:
		// Cycles were removed above, so this recursion terminates. The chain
		// is only as certain as its weakest edge.
		above := r.resolveAncestry(x.Parent.ParentID)
		a = executionAncestry{State: above.State, Chain: append([]executionID{x.Parent.ParentID}, above.Chain...), Root: above.Root, Depth: above.Depth + 1, Known: above.Known,
			Certainty: min(x.Parent.Certainty, above.Certainty), Inferred: above.Inferred || len(x.Parent.Evidence) > 0 && x.Parent.Evidence[0].Kind == claimCommandLaunch,
			Conflicted: x.Conflicted || above.Conflicted}
		if a.State == parentRoot {
			a.State = parentResolved
		}
		if !a.Known {
			a.Depth = 0
		}
	default:
		a = executionAncestry{State: x.Parent.State, Conflicted: x.Conflicted}
		if x.FamilyRoot != "" {
			// Membership names the family root; the depth stays unknown.
			r.ancestry[id] = a // guards a membership loop
			if family := r.resolveAncestry(x.FamilyRoot); family.Root != "" && family.Root != id {
				a.Root, a.Certainty, a.Inferred = family.Root, min(x.FamilyCertainty, family.Certainty), family.Inferred
				a.Conflicted = a.Conflicted || family.Conflicted
			}
		}
	}
	r.ancestry[id] = a
	return a
}

// chooseLabel picks presentation once across the original's recordings.
// Human text is the first excerpt fallback, even when it arrives after an
// automated opening. No native identifier is ever a presentation fallback.
func (r *executionResolution) chooseLabel(x *execution, bySubject map[sourceRef][]executionClaim) executionLabel {
	var origin *executionStream
	if x.Conversation != "" {
		origin = r.streams[sourceRef{sourceConversation, x.Conversation}]
	}
	streams := []*executionStream{}
	if origin != nil {
		streams = append(streams, origin)
	}
	seen := map[sourceRef]bool{}
	if origin != nil {
		seen[origin.Ref] = true
	}
	for _, cid := range x.Segments {
		ref := sourceRef{sourceConversation, cid}
		if s := r.streams[ref]; s != nil && !seen[ref] {
			streams = append(streams, s)
			seen[ref] = true
		}
	}
	for _, m := range x.Streams {
		if s := r.streams[m]; s != nil && !seen[m] {
			streams = append(streams, s)
			seen[m] = true
		}
	}
	for _, s := range streams {
		if s.NameSource == "override" && meaningfulWorkspaceTitle(s.AuthoredName, s.NativeID) {
			return executionLabel{s.AuthoredName, "override"}
		}
	}
	var named *executionStream
	for _, s := range streams {
		if meaningfulWorkspaceTitle(s.AuthoredName, s.NativeID) && (named == nil || s.NameRank < named.NameRank) {
			named = s
		}
	}
	if named != nil {
		return executionLabel{named.AuthoredName, "authored"}
	}
	for _, s := range streams {
		if meaningfulWorkspaceTitle(s.HumanName, s.NativeID) {
			return executionLabel{s.HumanName, "typed"}
		}
	}
	if launch := r.originatingLaunch(x, bySubject); launch != nil && meaningfulWorkspaceTitle(launch.Assignment, launch.NativeID) {
		return executionLabel{launch.Assignment, "assignment"}
	}
	for _, s := range streams {
		if meaningfulWorkspaceTitle(s.Assignment, s.NativeID) {
			return executionLabel{s.Assignment, "assignment"}
		}
		if meaningfulWorkspaceTitle(s.RequestName, s.NativeID) {
			return executionLabel{s.RequestName, "assignment"}
		}
	}
	for _, s := range streams {
		if meaningfulWorkspaceTitle(s.Nickname, s.NativeID) {
			return executionLabel{s.Nickname, "nickname"}
		}
	}
	for _, s := range streams {
		if meaningfulWorkspaceTitle(s.ContextName, s.NativeID) {
			return executionLabel{s.ContextName, "context"}
		}
	}
	return executionLabel{"Conversation without retained request", "unavailable"}
}

// originatingLaunch is the launch stream that created the execution: one with
// an accepted or linked acknowledgement first, otherwise any member launch;
// the earliest in its own launch order among equals.
func (r *executionResolution) originatingLaunch(x *execution, bySubject map[sourceRef][]executionClaim) *executionStream {
	var best *executionStream
	bestAccepted := false
	for _, m := range x.Streams {
		s := r.streams[m]
		if m.Kind != sourceSession || !s.Launch {
			continue
		}
		accepted := false
		for _, claim := range bySubject[m] {
			if (claim.Kind == claimLaunchAccepted || claim.Kind == claimChildLink) && claim.Strength >= evidenceLegacy {
				accepted = true
			}
		}
		switch {
		case best == nil, accepted && !bestAccepted:
		case accepted == bestAccepted && (s.At < best.At || s.At == best.At && s.Ref.ID < best.Ref.ID):
		default:
			continue
		}
		best, bestAccepted = s, accepted
	}
	return best
}

func (r *executionResolution) chooseOrder(x *execution, bySubject map[sourceRef][]executionClaim) executionOrderKey {
	key := executionOrderKey{Anchor: string(x.ID), PositionMissing: true, TimeMissing: true}
	if launch := r.originatingLaunch(x, bySubject); launch != nil {
		key.At, key.OriginStream = launch.At, launch.ConversationID
		if launch.SourcePosition >= 0 {
			key.Position, key.PositionMissing = launch.SourcePosition, false
		}
	}
	if key.At == "" {
		// The execution's own first activity; never a copy's timestamp.
		for _, m := range x.Streams {
			if at := r.streams[m].At; at != "" && (key.At == "" || at < key.At) {
				key.At = at
			}
		}
	}
	if key.OriginStream == "" && x.Parent.State == parentResolved {
		key.OriginStream = string(x.Parent.ParentID)
	}
	key.TimeMissing = key.At == ""
	return key
}

// Less is the total sibling order shared by every view.
func (r *executionResolution) Less(a, b executionID) bool {
	x, y := r.Executions[a], r.Executions[b]
	if x == nil || y == nil {
		return a < b
	}
	p, q := x.Order, y.Order
	switch {
	case p.TimeMissing != q.TimeMissing:
		return !p.TimeMissing
	case p.At != q.At:
		return p.At < q.At
	case p.OriginStream != q.OriginStream:
		return p.OriginStream < q.OriginStream
	case p.PositionMissing != q.PositionMissing:
		return !p.PositionMissing
	case p.Position != q.Position:
		return p.Position < q.Position
	}
	return p.Anchor < q.Anchor
}

func (r *executionResolution) Bind(ref sourceRef) executionBinding {
	if ref.Kind == sourceSession {
		if main, ok := r.mainSessions[ref.ID]; ok {
			ref = main
		}
	}
	return r.bindings[ref]
}

func (r *executionResolution) BindSession(id string) executionBinding {
	return r.Bind(sourceRef{sourceSession, id})
}

func (r *executionResolution) BindConversation(cid string) executionBinding {
	return r.Bind(sourceRef{sourceConversation, cid})
}

// ResolveAlias accepts the session identifiers older links and analytics
// stored: agent_sessions IDs, "conversation:<id>", "agent:<session>" and the
// explorer's synthetic external/ancestor conversation rows.
func (r *executionResolution) ResolveAlias(legacy string) executionBinding {
	for _, prefix := range []string{"conversation:", "external-conversation:", "ancestor-conversation:"} {
		if cid, ok := strings.CutPrefix(legacy, prefix); ok {
			return r.BindConversation(cid)
		}
	}
	if sid, ok := strings.CutPrefix(legacy, "agent:"); ok {
		return r.BindSession(sid)
	}
	if b := r.BindSession(legacy); b.State != bindingUnknown {
		return b
	}
	return r.BindConversation(legacy)
}

func (r *executionResolution) Execution(id executionID) *execution { return r.Executions[id] }

func (r *executionResolution) Ancestry(id executionID) executionAncestry {
	if a, ok := r.ancestry[id]; ok {
		return a
	}
	return executionAncestry{State: parentMissing}
}

func (r *executionResolution) Children(id executionID) []executionID { return r.children[id] }

// Family lists an execution's family in pre-order from its root (or its
// topmost known ancestor when the root is unknown), followed by the family's
// members whose direct parent is unresolved. complete is false when the limit
// cut it short or the family root is unknown.
func (r *executionResolution) Family(id executionID, limit int) ([]executionID, bool) {
	a := r.Ancestry(id)
	top := id
	if len(a.Chain) > 0 {
		top = a.Chain[len(a.Chain)-1]
	}
	out, complete := []executionID{}, a.Root != ""
	var walk func(executionID)
	walk = func(at executionID) {
		if limit > 0 && len(out) >= limit {
			complete = false
			return
		}
		out = append(out, at)
		for _, child := range r.children[at] {
			walk(child)
		}
	}
	if a.Root != "" && !a.Known {
		top = a.Root
	}
	if r.Executions[top] != nil {
		walk(top)
		// Members whose direct parent is unresolved follow the resolved tree.
		for _, member := range r.detached[top] {
			walk(member)
		}
	}
	return out, complete
}

func containsExecution(ids []executionID, id executionID) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// StreamsIn lists the streams a transcript retains, including attempts and
// copied launches.
func (r *executionResolution) StreamsIn(cid string) []sourceRef { return r.inConversation[cid] }

func (r *executionResolution) Stream(ref sourceRef) *executionStream {
	return r.streams[r.canonicalRef(ref)]
}

func (r *executionResolution) canonicalRef(ref sourceRef) sourceRef {
	if ref.Kind == sourceSession {
		if main, ok := r.mainSessions[ref.ID]; ok {
			return main
		}
	}
	return ref
}

// Fingerprint summarizes what a transcript's derived outputs depend on in the
// resolution: the binding of each stream it retains, and each bound
// execution's identity, segments, parent, ancestry and label. A late
// acknowledgement elsewhere changes it even when this transcript's own rows
// did not change. Occurrence ownership has its own fingerprint (see
// occurrenceIndex.Fingerprint).
func (r *executionResolution) Fingerprint(cid string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00", executionResolverVersion)
	seen := map[executionID]bool{}
	for _, ref := range r.inConversation[cid] {
		b := r.bindings[ref]
		fmt.Fprintf(h, "%s=%s:%s\x00", ref, b.State, b.ExecutionID)
		if b.ExecutionID == "" || seen[b.ExecutionID] {
			continue
		}
		seen[b.ExecutionID] = true
		x := r.Executions[b.ExecutionID]
		if x == nil {
			continue
		}
		a := r.Ancestry(x.ID)
		fmt.Fprintf(h, "%s|%s|%s|%s|%v|%d|%s|%s|%v|%v|%s|%s|%+v|%s\x00", x.ID, strings.Join(x.Segments, ","), x.Parent.State, x.Parent.ParentID,
			a.Known, a.Depth, a.Root, a.CertaintyName(), a.Conflicted, x.Conflicted, x.Label.Text, strings.Join(executionIDStrings(a.Chain), ","), x.Order,
			strings.Join(executionIDStrings(r.Children(x.ID)), ","))
	}
	if len(r.inConversation[cid]) == 0 {
		fmt.Fprintf(h, "absent:%s\x00", cid)
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
}

func executionIDStrings(ids []executionID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	return out
}
