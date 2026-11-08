package archive

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	labelrt "github.com/gbdubs/pharos/internal/labeler"
)

// The adapter table declares input order; the runner owns policy and capacity.
// These functions keep domain-specific pending/publication rules with their
// domains. Adding an independent task needs a declaration and adapter, not a
// scheduler branch or a new daemon.
func (s *Server) workDefinitions(authority *workAuthority) ([]workDefinition, error) {
	c := s.Catalog
	writing := &conversationWorkTask{server: s, kind: "analysis.authorship", cpus: 1,
		pending: c.authorshipPendingFor, filterFailures: true,
		prepareVersion: func(ctx context.Context) (string, error) {
			want, _, err := c.authorshipGenerationsContext(ctx)
			return want, err
		},
		prepare:     c.prepareAuthorshipEstimates,
		tailPending: c.authorshipOrphansPending,
		tail:        c.refreshAuthorshipOrphans,
		selectSQL:   `SELECT m.conversation_id id,p.revision revision FROM authorship_pending p JOIN messages m ON m.id=p.message_id WHERE m.conversation_id NOT IN (SELECT value FROM json_each(?)) ORDER BY p.sent_at DESC,m.conversation_id LIMIT ?`,
		step: func(ctx context.Context, ids []string, _ workAllocation) (workDomainStep, error) {
			want, _, err := c.authorshipGenerationsContext(ctx)
			if err != nil {
				return workDomainStep{}, err
			}
			return c.estimateAuthorshipFor(ctx, ids, want, 100)
		},
	}
	timing := &conversationWorkTask{server: s, kind: "maintenance.parallelism", cpus: 1, pending: c.parallelismPendingFor, filterFailures: true,
		selectSQL: `SELECT conversation_id id,revision FROM parallelism_dirty WHERE conversation_id NOT IN (SELECT value FROM json_each(?)) ORDER BY conversation_id LIMIT ?`, step: func(ctx context.Context, ids []string, _ workAllocation) (workDomainStep, error) {
			return c.refreshParallelismFor(ctx, ids)
		}}
	facts := &conversationWorkTask{server: s, kind: "maintenance.labels.query", cpus: 1, pending: c.messageLabelFactsPendingFor, filterFailures: true,
		prepare:   func(ctx context.Context, _ string) error { return c.ensureMessageLabelFacts(ctx) },
		selectSQL: `SELECT conversation_id id FROM message_label_facts_dirty WHERE conversation_id NOT IN (SELECT value FROM json_each(?)) ORDER BY conversation_id LIMIT ?`, step: func(ctx context.Context, ids []string, _ workAllocation) (workDomainStep, error) {
			return c.refreshMessageLabelFactsFor(ctx, ids, 0)
		}}
	labels := &modelWorkTask{server: s, authority: authority, kind: "analysis.labels", limit: labelsChainLimit,
		config: func(ctx context.Context, power powerState) modelWorkConfig {
			cfg := s.Config()
			settings, bulk := labelsForPower(cfg.Labels, power)
			return modelWorkConfig{settings: settings, signature: jsonText(settings), enabled: cfg.Labels.Enabled, bulk: bulk}
		},
		create: func(settings LabelerSettings, allocation workAllocation) (workModelSession, error) {
			return c.newLabelsSession(settings, allocation)
		},
	}
	return []workDefinition{
		{Kind: writing.kind, Task: writing},
		{Kind: "analysis.labels", Dependencies: []string{writing.kind}, Task: labels},
		{Kind: facts.kind, Dependencies: []string{writing.kind, "analysis.labels"}, Task: facts,
			// Projection publishes stored labels as they are: an unpausable
			// projection request must not drag inference or authorship
			// along with it.
			DependenciesForRequest: func(spec workRequestSpec) []string {
				if spec.Mode == "projection" {
					return nil
				}
				if s.Config().Labels.Enabled {
					return []string{writing.kind, "analysis.labels"}
				}
				return []string{writing.kind}
			}},
		{Kind: timing.kind, Task: timing},
	}, nil
}

var workDomainWindow = 128

type conversationWorkTask struct {
	server         *Server
	kind           string
	cpus           int
	selectSQL      string
	filterFailures bool // selectSQL filters held IDs before its page limit
	pending        func(context.Context, []string) ([]string, error)
	step           func(context.Context, []string, workAllocation) (workDomainStep, error)
	prepare        func(context.Context, string) error
	prepareVersion func(context.Context) (string, error)
	preparedAt     string
	prepared       bool
	tailPending    func(context.Context) (bool, error)
	tail           func(context.Context) (int, error)
	failed         workFailedRevisions
}

func populationWorkMembers(pop *workPopulation, limit int) []workMember {
	if pop.Library() {
		return nil
	}
	members := []workMember{}
	for _, m := range pop.Page("", limit) {
		if !pop.Excluded(m) && !slices.ContainsFunc(members, func(existing workMember) bool { return existing.ID == m.ID }) {
			members = append(members, m)
		}
	}
	return members
}

func (t *conversationWorkTask) Next(ctx context.Context, pop *workPopulation) (workSelection, error) {
	if t.prepare != nil {
		version, err := t.preparationVersion(ctx)
		if err != nil {
			return workSelection{}, err
		}
		if !t.prepared || version != t.preparedAt {
			return workSelection{Phase: "prepare", CPUs: t.cpus}, nil
		}
	}
	if !pop.Library() {
		members := populationWorkMembers(pop, workDomainWindow)
		if len(members) == 0 {
			return workSelection{}, nil
		}
		return workSelection{Units: members, CPUs: t.cpus}, nil
	}
	t.failed.begin(pop)
	now := workRevisionNow(t.server.Catalog)
	args := []any{workDomainWindow + len(pop.excluded)}
	if t.filterFailures {
		held := t.failed.heldIDs(pop, now)
		if t.failed.wanted() {
			return workFailedRevisionsPhase, nil
		}
		args = append([]any{jsonText(held)}, args...)
	}
	rows, err := queryMapsContext(ctx, t.server.Catalog.reader(), t.selectSQL, args...)
	if err != nil {
		return workSelection{}, err
	}
	members, seen := []workMember{}, map[string]bool{}
	for _, row := range rows {
		// A unit's first row names it: a domain with a row per message
		// (authorship) gives one conversation one token per page.
		id := firstString(row["id"])
		if seen[id] {
			continue
		}
		seen[id] = true
		if m, ok := t.failed.member(pop, workMember{ID: id, Revision: firstString(row["revision"])}, now); ok && !pop.Excluded(m) {
			members = append(members, m)
		}
	}
	if t.failed.wanted() {
		return workFailedRevisionsPhase, nil
	}
	if len(rows) == 0 && t.tail != nil {
		pending, err := t.tailPending(ctx)
		if err != nil {
			return workSelection{}, err
		}
		if pending {
			return workSelection{Phase: "cleanup", CPUs: t.cpus}, nil
		}
	}
	return workSelection{Units: members, CPUs: t.cpus}, nil
}

func (t *conversationWorkTask) RunBatch(ctx context.Context, selection workSelection, allocation workAllocation) (workBatchResult, error) {
	if selection.Phase == workFailedRevisionsPhase.Phase {
		return t.failed.read(ctx, t.server.Catalog)
	}
	if selection.Phase == "cleanup" {
		n, err := t.tail(ctx)
		if errors.Is(err, errWorkDomainBusy) {
			err = workBlocked(workBlocker{Reason: workBlockedWriter, Detail: err.Error()})
		}
		return workBatchResult{Work: int64(n), Remaining: n > 0}, err
	}
	if selection.Phase == "prepare" {
		version, err := t.preparationVersion(ctx)
		if err != nil {
			return workBatchResult{}, err
		}
		if err := t.prepare(ctx, version); err != nil {
			if errors.Is(err, errWorkDomainBusy) {
				return workBatchResult{}, workBlocked(workBlocker{Reason: workBlockedWriter, Detail: err.Error()})
			}
			return workBatchResult{}, err
		}
		t.prepared, t.preparedAt = true, version
		return workBatchResult{Work: 1, Remaining: true}, nil
	}
	out, err := runConversationWork(ctx, t.server.Catalog, selection, allocation, func(ctx context.Context, ids []string, alloc workAllocation) (workDomainStep, error) {
		pending, err := t.pending(ctx, ids)
		if err != nil {
			return workDomainStep{}, err
		}
		result := workDomainStep{}
		if len(pending) > 0 {
			result, err = t.step(ctx, pending, alloc)
		}
		for _, id := range ids {
			if !slices.Contains(pending, id) {
				result.Done = append(result.Done, id)
			}
		}
		return result, err
	})
	t.failed.charged(selection, err, workRevisionNow(t.server.Catalog))
	return out, err
}

func (t *conversationWorkTask) preparationVersion(ctx context.Context) (string, error) {
	if t.prepareVersion == nil {
		return "", nil
	}
	return t.prepareVersion(ctx)
}

// runConversationWork validates canonical revisions around the domain's own
// atomic publication guard. A later edit supersedes coverage rather than
// combining a producer's old result with a consumer's newer inputs.
func runConversationWork(ctx context.Context, c *Catalog, selection workSelection, allocation workAllocation, step func(context.Context, []string, workAllocation) (workDomainStep, error)) (workBatchResult, error) {
	ids := make([]string, 0, len(selection.Units))
	for _, m := range selection.Units {
		ids = append(ids, m.ID)
	}
	before, err := workRevisions(ctx, c, ids)
	if err != nil {
		return workBatchResult{}, err
	}
	result, err := step(ctx, ids, allocation)
	if err != nil {
		var classified *workError
		switch {
		case errors.Is(err, errWorkDomainBusy) || errors.Is(err, errLabelsRunning) || errors.Is(err, errAgentEventsRunning):
			// Another holder (a CLI pass, say) has the lock: wait for it.
			// Only contention wraps these sentinels, never a failed lock.
			return workBatchResult{}, workBlocked(workBlocker{Reason: workBlockedWriter, Detail: err.Error()})
		case errors.Is(err, labelrt.ErrBudget):
			// The model cache (memory, the shared GPU, a draining runner) is
			// full: capacity, not a failed attempt. CPUs are the grant's.
			return workBatchResult{}, workBlocked(workBlocker{Reason: workBlockedCapacity, Detail: err.Error()})
		case errors.As(err, &classified) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
			return workBatchResult{}, err
		}
		// Charged at the canonical revisions, as finite targets are.
		return workRetargetedFailures(selection.Units, before), workUnitFailed(err, canonicalMembers(selection.Units, before)...)
	}
	// Only finished or superseded units publish coverage. Pending and failed
	// units use their before revision below; rereading the whole selection
	// for a domain-limited step repeatedly scans units it has not reached.
	afterIDs := workDomainIDs(append(slices.Clone(result.Done), result.Superseded...))
	after, err := workRevisions(ctx, c, afterIDs)
	if err != nil {
		return workBatchResult{}, err
	}
	out := workBatchResult{Remaining: len(result.Remaining) > 0 || result.Rediscover}
	for _, id := range result.Done {
		m := workMember{ID: id, Revision: after[id]}
		if before[id] != after[id] {
			out.Superseded = append(out.Superseded, m)
		} else {
			out.Committed = append(out.Committed, m)
		}
	}
	for _, id := range result.Superseded {
		out.Superseded = append(out.Superseded, workMember{ID: id, Revision: after[id]})
	}
	if len(result.Failed) > 0 {
		failed := []workMember{}
		for _, member := range canonicalMembers(selection.Units, before) {
			if slices.Contains(result.Failed, member.ID) {
				failed = append(failed, member)
			}
		}
		out.Superseded = append(out.Superseded, workRetargetedFailures(selection.Units, before, result.Failed...).Superseded...)
		return out, workUnitFailed(fmt.Errorf("%d conversations could not be processed; their domain work remains pending", len(failed)), failed...)
	}
	// Domain-limited progress may leave a conversation pending without changing
	// its revision. It is work, not a transient error and not supersession,
	// but only when the domain reports it: a step that leaves its selection
	// pending and stores nothing would otherwise be reselected forever, so its
	// units are charged as failed and take the runner's backoff.
	if len(out.Committed) == 0 && len(out.Superseded) == 0 && (len(result.Remaining) > 0 || result.Rediscover) {
		if !result.Progress && !result.Rediscover {
			stalled := []workMember{}
			for _, member := range canonicalMembers(selection.Units, before) {
				if slices.Contains(result.Remaining, member.ID) {
					stalled = append(stalled, member)
				}
			}
			out.Superseded = append(out.Superseded, workRetargetedFailures(selection.Units, before, result.Remaining...).Superseded...)
			return out, workUnitFailed(fmt.Errorf("%d conversations made no progress; their domain work remains pending", len(stalled)), stalled...)
		}
		out.Work = 1
	}
	return out, nil
}

func workRevisions(ctx context.Context, c *Catalog, ids []string) (map[string]string, error) {
	return c.readWorkRevisions(ctx, ids)
}

// An edit can arrive after selection but before the batch starts. Failed
// work must retarget finite demand before its new revision is charged, just
// as successful publication retargets it. Otherwise the request would keep
// retrying its old target without ever sharing the new input's backoff.
func workRetargetedFailures(selected []workMember, current map[string]string, only ...string) workBatchResult {
	var out workBatchResult
	for _, member := range selected {
		if len(only) > 0 && !slices.Contains(only, member.ID) {
			continue
		}
		if revision := current[member.ID]; revision != member.Revision {
			out.Superseded = append(out.Superseded, workMember{ID: member.ID, Revision: revision})
		}
	}
	return out
}

// Retry age is execution policy, independent of the catalog's as-of clock.
func workRevisionNow(c *Catalog) time.Time {
	if c.work != nil && c.work.runner != nil {
		return c.work.runner.clock.Now()
	}
	return c.clock()
}

// workFailedRevisions names a library page's failed candidates by their
// canonical revision, the one runConversationWork charges failures at
// (runner review #11), so a library backoff holds a finite request at the
// same input and a changed unit retires its old failure. Next never reads a
// revision: without a stored digest that hashes every message row, which is
// admitted work. A failed batch records the revisions it read before its
// step; a candidate a failure names with no entry for its current domain
// token, or one older than the longest retry delay, asks for the
// "revisions" phase, which reads them at one CPU. The age bound is what lets
// an exhausted unit see a change its token does not carry (label facts have
// no token; an older message of an authorship conversation is not its first
// row). Entries are kept only for units a failure excludes, one per unit.
type workFailedRevisions struct {
	entries map[string]workFailedRevision
	want    []workMember // candidates the next revisions phase reads
}

type workFailedRevision struct {
	token, canonical string
	at               time.Time
}

// workFailedRevisionsPhase is the selection that reads wanted revisions.
var workFailedRevisionsPhase = workSelection{Phase: "revisions", CPUs: 1}

// begin starts a Next: entries for units no failure excludes are dropped.
func (r *workFailedRevisions) begin(pop *workPopulation) {
	r.want = r.want[:0]
	for id := range r.entries {
		if _, excluded := pop.excluded[id]; !excluded && !pop.failures[id] {
			delete(r.entries, id)
		}
	}
}

// member is a library candidate at its canonical revision when a failure
// excludes its unit; ok is false when that revision must be read first.
func (r *workFailedRevisions) member(pop *workPopulation, m workMember, now time.Time) (workMember, bool) {
	if _, excluded := pop.excluded[m.ID]; !excluded && !pop.failures[m.ID] {
		return m, true
	}
	entry, ok := r.entries[m.ID]
	if !ok || entry.token != m.Revision || now.Sub(entry.at) >= workRetryDelays[len(workRetryDelays)-1] {
		if len(r.want) < workDomainWindow && !slices.ContainsFunc(r.want, func(w workMember) bool { return w.ID == m.ID }) {
			r.want = append(r.want, m)
		}
		return workMember{}, false
	}
	return workMember{ID: m.ID, Revision: entry.canonical}, true
}

func (r *workFailedRevisions) wanted() bool { return len(r.want) > 0 }

// Filter before a SQL page limit. A per-message queue can have many rows
// for one held conversation; filtering afterwards would hide healthy work
// beyond that conversation's first page. Canonical checks remain admitted.
func (r *workFailedRevisions) heldIDs(pop *workPopulation, now time.Time) []string {
	ids := []string{}
	for id := range pop.excluded {
		member, ready := r.member(pop, workMember{ID: id, Revision: r.entries[id].token}, now)
		if ready && pop.Excluded(member) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// read is the revisions phase.
func (r *workFailedRevisions) read(ctx context.Context, c *Catalog) (workBatchResult, error) {
	ids := make([]string, len(r.want))
	for i, m := range r.want {
		ids[i] = m.ID
	}
	revisions, err := workRevisions(ctx, c, ids)
	if errors.Is(err, errWorkDomainBusy) {
		return workBatchResult{}, workBlocked(workBlocker{Reason: workBlockedWriter, Detail: err.Error()})
	}
	if err != nil {
		return workBatchResult{}, err
	}
	now := workRevisionNow(c)
	for _, m := range r.want {
		r.record(m, revisions[m.ID], now)
	}
	r.want = r.want[:0]
	return workBatchResult{Work: 1, Remaining: true}, nil
}

// charged records the revisions a failed library batch charged its units
// at, under the tokens it named them by. A finite selection names units by
// that revision already and leaves the library's entry alone.
func (r *workFailedRevisions) charged(selection workSelection, err error, now time.Time) {
	var failed *workError
	if !errors.As(err, &failed) {
		return
	}
	for _, m := range failed.units {
		if i := slices.IndexFunc(selection.Units, func(s workMember) bool { return s.ID == m.ID }); i >= 0 && selection.Units[i].Revision != m.Revision {
			r.record(selection.Units[i], m.Revision, now)
		}
	}
}

func (r *workFailedRevisions) record(m workMember, canonical string, now time.Time) {
	if r.entries == nil {
		r.entries = map[string]workFailedRevision{}
	}
	r.entries[m.ID] = workFailedRevision{token: m.Revision, canonical: canonical, at: now}
}

// canonicalMember is m at its canonical revision when one was read.
func canonicalMember(canonical map[string]string, m workMember) workMember {
	if revision, ok := canonical[m.ID]; ok {
		m.Revision = revision
	}
	return m
}

// canonicalMembers is units at the revisions read before a step.
func canonicalMembers(units []workMember, before map[string]string) []workMember {
	out := make([]workMember, len(units))
	for i, m := range units {
		out[i] = canonicalMember(before, m)
	}
	return out
}

// Both local models share scheduling and session ownership. Framing, dirty
// closure and publication stay in their separate domain sessions.
type workModelSession interface {
	Allocation() workAllocation
	Discovered() bool
	Discover(context.Context) error
	PendingFor(context.Context, []string) ([]string, error)
	PendingPage(context.Context, string, int, func(workMember) bool) (workDomainPage, error)
	Run(context.Context, []string, int) (workDomainStep, error)
	Close()
}

type modelWorkConfig struct {
	settings      LabelerSettings
	signature     string
	enabled, bulk bool
}

type modelWorkTask struct {
	server    *Server
	authority *workAuthority
	kind      string
	config    func(context.Context, powerState) modelWorkConfig
	create    func(LabelerSettings, workAllocation) (workModelSession, error)
	// inputs is the native generation of what discovery alone sees (mirror
	// representatives, structure, forks, a horizon): no dirty mark carries
	// those. Default: Catalog.labelingInputsGeneration.
	inputs  func(context.Context) (string, error)
	limit   int
	session workModelSession
	// signature is the settings and compute envelope the session was made
	// for; discoveredAt is the input generation its discovery read first.
	signature, discoveredAt string
	failed                  workFailedRevisions
	// used is when the runner last asked for the session's work; Idle
	// closes a session unused for workModelSessionIdle.
	used time.Time
}

// workModelSessionIdle is how long an idle model session keeps its
// library-sized item context and inventory. The model's workers have their
// own, shorter idle period in the labeler manager.
var workModelSessionIdle = 10 * time.Minute

// closeSession drops the session and everything it keeps.
func (t *modelWorkTask) closeSession() {
	if t.session != nil {
		t.session.Close()
		t.session = nil
	}
}

// Idle closes a session nobody has asked for work recently, so a finished
// backlog or a quiet library does not keep the item context resident.
func (t *modelWorkTask) Idle(now time.Time) {
	if t.session != nil && now.Sub(t.used) >= workModelSessionIdle {
		t.closeSession()
	}
}

// modelWorkPending bounds the conversations one finite model batch may send
// to inference; the rest of its window is only checked as current.
const modelWorkPending = 4

func (t *modelWorkTask) envelope(settings LabelerSettings) int {
	return min(t.authority.cap(), max(1, settings.Threads)+min(4, max(1, settings.Threads)))
}

func (t *modelWorkTask) inputGeneration(ctx context.Context) (string, error) {
	if t.inputs != nil {
		return t.inputs(ctx)
	}
	return t.server.Catalog.labelingInputsGeneration(ctx)
}

// modelWorkWindow is members in order with at most most of the pending
// ones (pending sorted): Run reports the others Done from the ledger
// without inference, so a mostly current population costs a batch per
// window rather than per few conversations.
func modelWorkWindow(members []workMember, pending []string, most int) []workMember {
	units := []workMember{}
	n := 0
	for _, m := range members {
		if _, ok := slices.BinarySearch(pending, m.ID); ok {
			if n == most {
				continue
			}
			n++
		}
		units = append(units, m)
	}
	return units
}

func (t *modelWorkTask) Next(ctx context.Context, pop *workPopulation) (workSelection, error) {
	t.used = workRevisionNow(t.server.Catalog)
	power, fresh := t.server.workPowerSnapshot()
	config := t.config(ctx, power)
	if !config.enabled {
		t.closeSession()
		return workSelection{Blocker: workBlocker{Reason: workBlockedPolicy, Detail: "this analysis is disabled"}}, nil
	}
	if !fresh {
		return workSelection{Phase: "power", CPUs: 1}, nil
	}
	if pop.Library() && !config.bulk {
		return workSelection{Blocker: workBlocker{Reason: workBlockedPower, Detail: "historical model work waits for external power"}}, nil
	}
	// Discovery is serial and starts no worker: one CPU. The compute
	// envelope is the session's own, fixed when it is made.
	discover := workSelection{Phase: "discover", CPUs: 1}
	signature := fmt.Sprintf("%s:%d", config.signature, t.envelope(config.settings))
	if t.session == nil || t.signature != signature || !t.session.Discovered() {
		return discover, nil
	}
	compute := t.session.Allocation().CPUs
	if !pop.Library() {
		// A finite selection's refresh reads its own conversations' live
		// state, so it needs no generation check.
		members := populationWorkMembers(pop, workDomainWindow)
		if len(members) == 0 {
			return workSelection{CPUs: compute}, nil
		}
		ids := make([]string, len(members))
		for i, m := range members {
			ids[i] = m.ID
		}
		pending, err := t.session.PendingFor(ctx, ids)
		if err != nil {
			return workSelection{}, err
		}
		return workSelection{Units: modelWorkWindow(members, workDomainIDs(pending), modelWorkPending), CPUs: compute}, nil
	}
	t.failed.begin(pop)
	now := workRevisionNow(t.server.Catalog)
	page, err := t.session.PendingPage(ctx, "", modelWorkPending, func(m workMember) bool {
		m, ok := t.failed.member(pop, m, now)
		return !ok || pop.Excluded(m)
	})
	if err != nil {
		return workSelection{}, err
	}
	if t.failed.wanted() {
		return workFailedRevisionsPhase, nil
	}
	if page.Rediscover {
		return discover, nil
	}
	// A library page is a coverage claim when empty: the inventory must
	// have been discovered at the current input generation (H1). A page with
	// units is not a claim, and each step's refresh reads its own
	// conversations' live state, so a generation that moved while work
	// remains is checked only once the session runs out of it: an ingest
	// does not rediscover a draining session. One meta read.
	if len(page.Units) == 0 {
		inputs, err := t.inputGeneration(ctx)
		if err != nil {
			return workSelection{}, err
		}
		if inputs != t.discoveredAt {
			return discover, nil
		}
	}
	for i, m := range page.Units {
		page.Units[i], _ = t.failed.member(pop, m, now)
	}
	return workSelection{Units: page.Units, CPUs: compute}, nil
}

func (t *modelWorkTask) RunBatch(ctx context.Context, selection workSelection, allocation workAllocation) (workBatchResult, error) {
	if selection.Phase == "power" {
		t.server.cachedPower(ctx)
		return workBatchResult{Work: 1, Remaining: true}, ctx.Err()
	}
	if selection.Phase == workFailedRevisionsPhase.Phase {
		return t.failed.read(ctx, t.server.Catalog)
	}
	if selection.Phase == "discover" {
		config := t.config(ctx, t.server.cachedPower(ctx))
		envelope := t.envelope(config.settings)
		// Read first: a change while discovery runs is a newer generation.
		inputs, err := t.inputGeneration(ctx)
		if err != nil {
			return workBatchResult{}, err
		}
		t.closeSession()
		// The session's envelope is computed, not this one-CPU grant.
		session, err := t.create(config.settings, workAllocation{CPUs: envelope})
		if err != nil {
			return workBatchResult{}, workPermanent(err)
		}
		if err := session.Discover(ctx); err != nil {
			session.Close()
			if errors.Is(err, errLabelsRunning) || errors.Is(err, errAgentEventsRunning) {
				return workBatchResult{}, workBlocked(workBlocker{Reason: workBlockedWriter, Detail: err.Error()})
			}
			return workBatchResult{}, err
		}
		t.session, t.discoveredAt = session, inputs
		t.signature = fmt.Sprintf("%s:%d", config.signature, envelope)
		return workBatchResult{Work: 1, Remaining: true}, nil
	}
	out, err := runConversationWork(ctx, t.server.Catalog, selection, allocation, func(ctx context.Context, ids []string, _ workAllocation) (workDomainStep, error) {
		return t.session.Run(ctx, ids, t.limit)
	})
	t.failed.charged(selection, err, workRevisionNow(t.server.Catalog))
	if errors.Is(err, errLabelsSessionStale) || errors.Is(err, errAgentEventsSessionStale) {
		t.closeSession()
		t.server.work.runner.InterruptKinds([]string{t.kind}, "the analysis model changed; reissue the request for its new input policy")
		return workBatchResult{Work: 1, Remaining: true}, nil
	}
	return out, err
}

// Explicit request helpers use declared task kinds; their historical remainder
// is submitted separately at backfill priority, so it cannot inherit a click's
// foreground class indefinitely.
func (s *Server) currentPopulationKinds() []string {
	kinds := []string{"analysis.authorship", "maintenance.parallelism", "maintenance.labels.query"}
	if s.Config().Labels.Enabled {
		kinds = append(kinds, "analysis.labels")
	}
	if s.work != nil && s.work.runner.byKind[kindSessionShape] != nil {
		kinds = append(kinds, kindSessionShape)
	}
	if s.work != nil && s.work.runner.byKind["analysis.agent_events"] != nil && s.agentEventsEnabled(s.life.ctx) {
		kinds = append(kinds, "analysis.agent_events")
	}
	slices.Sort(kinds)
	return kinds
}
func (s *Server) recentWork(ctx context.Context) (uint64, *workOperation, error) {
	if s.work == nil {
		return 0, nil, errWorkUnavailable
	}
	until := s.Catalog.clock()
	return s.work.requestRecent(ctx, s.currentPopulationKinds(), until.Add(-48*time.Hour), until, true)
}
