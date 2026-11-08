package archive

// Shared execution identity (docs/execution-resolver-design.md). Providers
// supply normalized evidence; resolveExecutions turns it into one immutable
// result that every consumer projects. These declarations are the contract
// between the evidence loader, the resolver and its consumers: extend them,
// but do not change the meaning of an existing field.

// executionResolverVersion names the resolution rules. It is not part of any
// execution ID, so a rule change re-resolves without renaming executions.
// v2: ancestry certainty, inference and identity conflict propagate along
// the chain (executionAncestry) and enter Fingerprint.
// v3: a launch identifies only transcripts named at its strongest authority
// across all its targets; equally strong targets join only as one execution.
// v4: a launch on cyclic recorded ancestry is never a copied launch.
// v5: v4 missed launches matched by call ID and acknowledged child
// (sameCallCopies); those on a cycle are no longer copies either.
// v6: a launch whose result ends in a validated harness usage footer
// (readLegacyLaunchOutcome) created the agent it names, retained or not.
// v7: scoped source names and first human/request excerpts replace native-ID
// labels, consistently across mirrored and continued recordings.
const executionResolverVersion = "execution-v7"

// sourceKind names what a sourceRef locates.
type sourceKind string

const (
	sourceConversation sourceKind = "conversation" // a conversation's own (main) stream
	sourceSession      sourceKind = "session"      // agent_sessions.id other than a main stream
	sourceMessage      sourceKind = "message"      // messages.id
	sourceToolCall     sourceKind = "tool_call"    // tool_calls.id
	sourceRequest      sourceKind = "request"      // model_requests.id
)

// sourceRef locates one preserved observation. It is also the alias key that
// old URLs and stored session IDs resolve through.
type sourceRef struct {
	Kind sourceKind
	ID   string
}

func (r sourceRef) String() string { return string(r.Kind) + ":" + r.ID }
func (r sourceRef) IsZero() bool   { return r.ID == "" }

// executionID is "conversation:<id>" when the execution has a retained
// transcript, otherwise "agent:<session id>" for the originating launch or
// embedded stream. These match the identities usage analytics already
// published. Labels, numbering, workspace order and tokens never enter it.
type executionID string

// evidenceStrength orders provenance, strongest first. Synthesized evidence
// (for example a root fallback written by an older ingest) is a discovery
// hint only and never resolves a parent or identity by itself.
type evidenceStrength int

const (
	evidenceSynthesized evidenceStrength = iota + 1 // fallback written by a resolver, not observed
	evidenceLegacy                                  // a stored relationship whose provenance is unknown
	evidenceDerived                                 // parser-derived inference with a named rule
	evidenceNative                                  // asserted by the provider's own records
)

func (s evidenceStrength) String() string {
	switch s {
	case evidenceNative:
		return "native"
	case evidenceDerived:
		return "derived"
	case evidenceLegacy:
		return "legacy"
	case evidenceSynthesized:
		return "synthesized"
	}
	return "unknown"
}

// claimKind is the typed relationship a claim asserts. Subject and Object are
// sourceRefs; their roles per kind are listed beside each constant.
type claimKind string

const (
	// Launch outcomes. Subject: the launch stream (sourceSession).
	claimLaunchOwner    claimKind = "launch_owner"    // Object: the stream that issued the launch
	claimLaunchAccepted claimKind = "launch_accepted" // Object: the child transcript (conversation), or zero for a verified handle without one
	claimLaunchRejected claimKind = "launch_rejected" // Object: zero. Creation was refused.
	// A persisted explicit child link (agent_sessions.child_conversation_id).
	// Subject: launch stream; Object: child conversation. Same meaning as an
	// accepted launch; its strength says how far it was verified.
	claimChildLink claimKind = "child_link"
	// Recorded ancestry. Subject: child stream; Object: parent stream.
	claimSessionParent    claimKind = "session_parent"    // a main session's recorded parent session
	claimTranscriptParent claimKind = "transcript_parent" // provider-typed delegation parent of a conversation
	claimCommandLaunch    claimKind = "command_launch"    // inferred command launch (may be mixed-provider)
	// Family membership without a known direct parent. Subject: conversation;
	// Object: the family's root conversation (for example a Claude agent file
	// under its root's subagents directory). It never resolves a parent: it
	// keeps the family root known while the direct parent stays unresolved.
	claimFamilyMember claimKind = "family_member"
	// Identity. Subject and Object are conversations naming one execution.
	claimContinuation claimKind = "continuation" // Subject continues Object as another segment
	claimMirror       claimKind = "mirror"       // another retained copy of the same transcript
	// Copied history. Subject: the copy; Object: the original, or zero when only
	// the copy survives. For launch streams the copy binds to the original's
	// attempt; for messages/tool calls/requests see executionOccurrence.
	claimInheritedCopy claimKind = "inherited_copy"
	// A possible copy without enough evidence to prove one physical occurrence
	// (for example a generated line-N locator). Object: the candidate original.
	claimCopyCandidate claimKind = "copy_candidate"
	// The same physical occurrence observed twice inside one execution.
	claimDuplicateObservation claimKind = "duplicate_observation"
)

// Identifier kinds for executionClaim.IDKind and executionStream.IDKind.
const (
	idKindNativeEvent = "native-event" // a provider event ID, unique in Namespace
	idKindCallID      = "call-id"      // a provider call ID, scoped as Namespace documents
	idKindLocator     = "locator"      // a synthesized position (line-N); locates, never matches
	idKindUnknown     = ""             // legacy rows that did not record it
)

// executionClaim is one typed piece of evidence. Every claim keeps the source
// observation that supports it (Locator), the namespace its identifiers are
// valid in, and the rule/parser version that derived it.
type executionClaim struct {
	Kind     claimKind
	Subject  sourceRef
	Object   sourceRef
	Strength evidenceStrength
	// Namespace is provider/account (or a narrower documented scope) for the
	// identifiers that matched. Claims never match across namespaces, except
	// claimCommandLaunch, which is its own typed relationship.
	Namespace string
	IDKind    string
	Rule      string // derivation rule and version, e.g. "codex-task-path-ack/v1"
	Locator   sourceRef
	// Context names the stream that owns Object when Object is an occurrence
	// (inherited copies), so ownership survives when the original is gone.
	Context sourceRef
}

// executionStream is one candidate stream: a conversation's own stream or a
// non-main agent session (a launch handle or an embedded agent).
type executionStream struct {
	Ref            sourceRef // conversation:<id> or session:<id>
	ConversationID string    // the transcript that retains this stream
	WorkspaceID    string
	Provider       string
	Account        string
	NativeID       string // conversation native ID, or the session's native ID (call ID)
	MainSessionID  string // a conversation's own agent_sessions row, when one exists
	// RecordedDepth is the stored agent depth; a positive value without a
	// qualifying parent claim means ancestry is missing, not a root.
	RecordedDepth int
	Subagent      bool // recorded as a subagent stream
	// Presentation inputs, chosen once by the resolver.
	AuthoredName string // a supported user/provider-authored name
	NameSource   string // source, linked or override (never an automated request)
	NameRank     int    // native conversation (0) before app session (1)
	HumanName    string // first classified human-written words
	RequestName  string // recorded automated/agent assignment excerpt
	ContextName  string // explicitly scoped task/attempt context
	Assignment   string // the originating launch's task (launch streams)
	Nickname     string
	AgentPath    string
	// Ordering inputs: launch time for launch streams, else first activity.
	At             string
	SourcePosition int  // launch position in its parent transcript; -1 unknown
	Launch         bool // the stream originates from a retained launch call
	// Observed independent activity (messages in the stream, or reported
	// usage). Tokens never decide identity; they establish only presence.
	Observed       bool
	ReportedTokens int64
	// Typed is true when the retaining transcript's typed evidence is
	// current; otherwise its claims are a legacy reading.
	Typed bool
}

// executionEvidence is the complete admitted evidence for one generation.
type executionEvidence struct {
	Generation int64
	Streams    []executionStream
	Claims     []executionClaim
	// Capabilities the source could not supply (for example an offline
	// snapshot without launch outcomes) appear as coverage gaps.
	Missing []string
}

type bindingState string

const (
	bindingResolved   bindingState = "resolved"   // the source names this execution
	bindingAttempt    bindingState = "attempt"    // a launch attempt that is not an execution
	bindingInherited  bindingState = "inherited"  // copied history; ExecutionID is the original's, if known
	bindingUnresolved bindingState = "unresolved" // conflicting or split identity; no single execution
	bindingUnknown    bindingState = ""           // not in the evidence
)

type executionBinding struct {
	ExecutionID executionID // empty unless State resolves to one execution
	State       bindingState
	Evidence    []executionClaim
}

type attemptOutcome string

const (
	attemptRejected   attemptOutcome = "rejected"   // explicit creation refusal, no independent execution evidence
	attemptUnresolved attemptOutcome = "unresolved" // launch call only; excluded from confirmed counts
)

type launchAttempt struct {
	Ref      sourceRef
	Outcome  attemptOutcome
	Owner    executionID // the execution that issued it, when resolved
	Evidence []executionClaim
}

type parentState string

const (
	parentRoot      parentState = "root"
	parentResolved  parentState = "resolved"
	parentAmbiguous parentState = "ambiguous" // equally authoritative claims disagree
	parentMissing   parentState = "missing"   // a child whose parent evidence is absent
	parentCycle     parentState = "cycle"     // member of an ownership cycle
	parentConflict  parentState = "conflict"  // identity (cardinality) is itself unresolved
)

type parentClaim struct {
	Parent executionID
	Claim  executionClaim
	Reason string // why a candidate did not win, if it did not
}

type parentResolution struct {
	State      parentState
	ParentID   executionID // set only for parentResolved
	Certainty  evidenceStrength
	Evidence   []executionClaim
	Candidates []parentClaim
}

// executionAncestry is the chain above an execution. Depth is known only when
// every edge to a root resolved; Chain still lists the known part. Root is the
// family root whenever it is known, which family membership evidence can
// establish even when a direct parent is unresolved.
type executionAncestry struct {
	State parentState // parentRoot or parentResolved when the chain is complete; otherwise the first unresolved state
	Chain []executionID
	Root  executionID // empty when unknown
	Depth int
	Known bool // the chain reaches a root: Depth is meaningful
	// Certainty is the weakest evidence establishing Root and Depth: each
	// resolved edge's, and family membership's when that names the root. A
	// root or depth reached through a legacy link is no more certain than it.
	Certainty evidenceStrength
	// Inferred is whether an edge on the chain is an inferred command launch.
	Inferred bool
	// Conflicted is whether the execution or an ancestor is a member of an
	// unresolved identity conflict: how many executions its sources make, and
	// so its run and cohort counts, are not established.
	Conflicted bool
}

// Ancestry qualities, shared by every projection of a root, cohort or depth:
// observed through native or derived evidence, through an inferred command
// launch, or through stored links of unknown provenance.
const (
	ancestryObserved   = "observed"
	ancestryInferred   = "inferred"
	ancestryUnverified = "unverified"
)

// CertaintyName names Certainty, "inferred" when an inferred command launch
// is the weakest reason on an otherwise observed chain.
func (a executionAncestry) CertaintyName() string {
	if a.Inferred && a.Certainty >= evidenceDerived {
		return ancestryInferred
	}
	return a.Certainty.String()
}

// RootQuality is how the family root was established; "" when it is unknown.
// A root reached by an execution in an unresolved identity conflict, its own
// or an ancestor's, is unverified: the cohort is known but which executions
// its sources make is not, so no projection may present it as observed.
func (a executionAncestry) RootQuality() string {
	switch {
	case a.Root == "":
		return ""
	case a.Conflicted, a.Certainty < evidenceDerived:
		return ancestryUnverified
	case a.Inferred:
		return ancestryInferred
	}
	return ancestryObserved
}

// DepthQuality is how Depth was established; "" when it is unknown. It is
// RootQuality's classification, conflicts included.
func (a executionAncestry) DepthQuality() string {
	if !a.Known {
		return ""
	}
	return a.RootQuality()
}

type executionLabel struct {
	Text   string
	Source string // authored, override, typed, assignment, nickname, context, unavailable
}

// executionOrderKey is the total sibling order, derived before any projection.
type executionOrderKey struct {
	TimeMissing     bool
	At              string // launch time, else first activity (RFC 3339)
	OriginStream    string // the stream that launched it
	PositionMissing bool
	Position        int
	Anchor          string // the stable execution anchor
}

type execution struct {
	ID       executionID
	Presence string // "observed" or "verified-creation"
	Parent   parentResolution
	// Streams are every source bound to the execution: launch handles, main
	// streams and embedded sessions. Accounting stays per stream.
	Streams []sourceRef
	// Segments are its transcripts in segment order: the origin first, then
	// continuations; mirrors follow the copy they mirror.
	Segments     []string
	Conversation string // the origin transcript, empty for an embedded or handle-only execution
	WorkspaceID  string
	Provider     string
	Label        executionLabel
	Order        executionOrderKey
	Conflicted   bool // a member of an unresolved identity conflict
	// FamilyRoot is a family root named by membership evidence when the
	// direct parent is unresolved; empty otherwise.
	FamilyRoot executionID
	// FamilyCertainty is the strength of the membership naming FamilyRoot.
	FamilyCertainty evidenceStrength
	// Evidence is "typed" when every bound stream's transcript has current
	// typed evidence, "legacy" when none does, else "mixed".
	Evidence string
}

type identityConflict struct {
	Claims  []executionClaim
	Sources []sourceRef
	Reason  string
}

type resolutionCoverage struct {
	Missing           []string // capabilities absent from the evidence
	UnresolvedParents int
	Conflicts         int
	Cycles            int
	Attempts          int
}

type executionRevision struct {
	Resolver   string
	Generation int64
}

// Occurrence ownership: see execution_occurrences.go.
type occurrenceRole string

const (
	occurrenceOriginal   occurrenceRole = "original"
	occurrenceInherited  occurrenceRole = "inherited_copy"
	occurrenceDuplicate  occurrenceRole = "duplicate_observation"
	occurrenceUnresolved occurrenceRole = "unresolved"
)

type executionOccurrence struct {
	Observation sourceRef
	Role        occurrenceRole
	// Canonical is the original observation when known; zero otherwise.
	Canonical sourceRef
	// Owner is the owning execution, empty when ownership is unknown.
	Owner executionID
	// Navigate is the original's location when it survives, else the
	// surviving observation itself.
	Navigate sourceRef
	Evidence []executionClaim
}
