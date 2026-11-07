package archive

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unsafe"
)

// Population work execution (docs/work-execution-plan.md §3.1, §6). A task
// kind's adapter answers two questions: what is the next bounded batch for a
// population (Next, cheap), and run that admitted batch to its safe boundary
// (RunBatch). The runner in work_execution_runner.go owns ordering,
// admission, cancellation and status; domains keep their algorithms, dirty
// state and publication. Nothing here is persisted: on restart, work is
// rediscovered from the domains' own pending state.

// workAllocation is the CPU worker parallelism an admitted batch may use for
// its Go pools and native worker threads, instead of NumCPU.
type workAllocation struct {
	CPUs int
}

// workMember is one natural unit (a conversation, source group, workspace or
// the library) and the opaque input revision a request targets. Every task in
// a request reports the same revision token for a unit, so a producer and a
// consumer at different revisions never combine into covered work.
type workMember struct {
	ID, Revision string
}

// workMemberSize is a member's slice slot; its strings are charged separately.
const workMemberSize = int64(unsafe.Sizeof(workMember{}))

// workInventory is a closed membership: sorted by ID, unique, immutable once
// frozen, so identical requests may share it by reference. Slice position is
// the ordinal that coverage bitsets index.
type workInventory struct {
	members  []workMember
	bytes    int64
	frozenAt time.Time
}

func (inv *workInventory) index(id string) (int, bool) {
	return slices.BinarySearchFunc(inv.members, id, func(m workMember, id string) int { return cmp.Compare(m.ID, id) })
}

// workPopulation is what Next sees: a request's closed inventory (or the
// whole library) plus the runner's view of which members this task may work
// on now. It is a snapshot for one Next call; do not keep it afterwards.
type workPopulation struct {
	inventory *workInventory
	// excluded maps unit ID to the revision in retry backoff or exhausted
	// failure. It is a snapshot owned by this call.
	excluded map[string]string
	// failures includes remembered attempts whose delay has elapsed too.
	// Adapters keep their canonical identity until the attempt is retired.
	failures map[string]bool
	// eligible marks the ordinals this task can work on now: not settled,
	// not yet covered by this task, not excluded, and covered by every
	// prerequisite at the target revision. nil means every member. The
	// runner refills it only between Next calls.
	eligible         workBits
	pending, waiting int
	// target returns a replaced member's revision; nil when the request had
	// no replacement when the snapshot was taken.
	target func(i int) (string, bool)
}

// Library reports whole-library demand: the domain pages its own dirty
// queue rather than a closed inventory.
func (p *workPopulation) Library() bool { return p == nil || p.inventory == nil }

// Len is the size of the whole frozen inventory, eligible or not.
func (p *workPopulation) Len() int {
	if p.Library() {
		return 0
	}
	return len(p.inventory.members)
}

// Member is the member at an ordinal, at the revision the request targets.
func (p *workPopulation) Member(i int) workMember {
	m := p.inventory.members[i]
	if p.target != nil {
		if revision, ok := p.target(i); ok {
			m.Revision = revision
		}
	}
	return m
}

// Index is a member's ordinal.
func (p *workPopulation) Index(id string) (int, bool) {
	if p.Library() {
		return 0, false
	}
	return p.inventory.index(id)
}

// Lookup returns the member with id at the revision the request targets,
// whether or not this task can work on it now.
func (p *workPopulation) Lookup(id string) (workMember, bool) {
	i, ok := p.Index(id)
	if !ok {
		return workMember{}, false
	}
	return p.Member(i), true
}

// Eligible returns the member with id when this task can work on it now. A
// member that is not eligible is already covered by this task, settled,
// excluded, or waiting for a prerequisite to cover it.
func (p *workPopulation) Eligible(id string) (workMember, bool) {
	i, ok := p.Index(id)
	if !ok || (p.eligible != nil && !p.eligible.has(i)) {
		return workMember{}, false
	}
	return p.Member(i), true
}

// Page returns up to limit eligible members after the ID after ("" starts),
// in ID order, at their target revisions. Only eligible members appear, so
// an adapter that takes the first page advances as the request is covered.
func (p *workPopulation) Page(after string, limit int) []workMember {
	if p.Library() || limit <= 0 {
		return nil
	}
	i := 0
	if after != "" {
		var found bool
		if i, found = p.inventory.index(after); found {
			i++
		}
	}
	var page []workMember
	for n := len(p.inventory.members); i < n && len(page) < limit; i++ {
		if p.eligible != nil {
			if i = p.eligible.next(i); i < 0 {
				break
			}
		}
		page = append(page, p.Member(i))
	}
	return page
}

// Pending is how many members are eligible now.
func (p *workPopulation) Pending() int {
	if p.Library() {
		return 0
	}
	if p.eligible == nil {
		return len(p.inventory.members)
	}
	return p.pending
}

// Waiting is how many unsettled members this task has not covered and
// cannot work on until a prerequisite covers them. An empty Next while
// members wait never covers them.
func (p *workPopulation) Waiting() int {
	if p.Library() {
		return 0
	}
	return p.waiting
}

// FrozenAt is when membership was actually read, not when it was requested.
func (p *workPopulation) FrozenAt() time.Time {
	if p.Library() {
		return time.Time{}
	}
	return p.inventory.frozenAt
}

// Excluded reports a unit Next must not select now: its revision is in retry
// backoff or has exhausted its attempts. A newer revision is not excluded.
func (p *workPopulation) Excluded(m workMember) bool {
	if p == nil {
		return false
	}
	revision, ok := p.excluded[m.ID]
	return ok && revision == m.Revision
}

// workInventoryTooLarge refuses a membership that cannot fit its request
// limit, before the request claims a finite population.
type workInventoryTooLarge struct {
	Members int
	Bytes   int64
	Limit   int64
}

func (e *workInventoryTooLarge) Error() string {
	return fmt.Sprintf("request membership needs more than %d bytes (%d units read, at least %d bytes); narrow the request", e.Limit, e.Members, e.Bytes)
}

// workInventoryBuilder constructs a membership, charging slice capacity and
// copied string bytes before it allocates them, so an oversized request
// fails without first building it. Growing the slice briefly holds the old
// and the new array, so growth is checked at that peak.
type workInventoryBuilder struct {
	members []workMember
	bytes   int64
	limit   int64
}

// Add records one unit. Units may arrive in any order; a duplicate with the
// same revision is dropped at freeze and a conflicting one is an error.
func (b *workInventoryBuilder) Add(id, revision string) error {
	if id == "" {
		return errors.New("a work unit needs an identity")
	}
	cost := int64(len(id) + len(revision))
	if len(b.members) == cap(b.members) {
		grown := max(16, 2*cap(b.members))
		if peak := b.bytes + cost + int64(grown)*workMemberSize; peak > b.limit {
			return &workInventoryTooLarge{Members: len(b.members) + 1, Bytes: peak, Limit: b.limit}
		}
		b.members = append(make([]workMember, 0, grown), b.members...)
		b.bytes += int64(grown-len(b.members)) * workMemberSize
	} else if b.bytes+cost > b.limit {
		return &workInventoryTooLarge{Members: len(b.members) + 1, Bytes: b.bytes + cost, Limit: b.limit}
	}
	b.bytes += cost
	b.members = append(b.members, workMember{ID: strings.Clone(id), Revision: strings.Clone(revision)})
	return nil
}

func (b *workInventoryBuilder) freeze(at time.Time) (*workInventory, error) {
	slices.SortFunc(b.members, func(x, y workMember) int { return cmp.Compare(x.ID, y.ID) })
	unique := b.members[:0]
	for i, m := range b.members {
		if i > 0 && m.ID == b.members[i-1].ID {
			if m.Revision != b.members[i-1].Revision {
				return nil, fmt.Errorf("work unit %s appears at two revisions in one membership read", m.ID)
			}
			continue
		}
		unique = append(unique, m)
	}
	clear(b.members[len(unique):])
	return &workInventory{members: unique, bytes: b.bytes, frozenAt: at}, nil
}

// workSelection is Next's answer: one ready batch, a blocker, or (neither)
// nothing pending for the population.
type workSelection struct {
	// Phase names a discovery, preparation, compute or publication step of
	// the same task ("" is compute). A unit-less step needs a phase.
	Phase string
	// Units are the natural units this batch owns. Bounded by
	// workSelectionLimit.
	Units []workMember
	// CPUs is the minimum useful allocation (0 means 1). MaxCPUs lets a
	// simple Go task take more when free; leave it 0 for a fixed envelope
	// such as a stable model session, which waits rather than shrinks.
	CPUs, MaxCPUs int
	// Coarse marks an indivisible unit that runs to its own safe boundary.
	Coarse  bool
	Blocker workBlocker
}

func (s workSelection) ready() bool {
	return s.Blocker.Reason == "" && (s.Phase != "" || len(s.Units) > 0)
}

func (s workSelection) grant() (least, most int) {
	least = max(s.CPUs, 1)
	return least, max(s.MaxCPUs, least)
}

// workBatchResult is what an admitted batch committed. Only Committed units
// count as progress for a request; dispatching a unit proves nothing.
type workBatchResult struct {
	// Committed are units whose output and dirty acknowledgment committed,
	// at the revision acknowledged.
	Committed []workMember
	// Superseded are units whose input changed before publication, at the
	// newer revision observed.
	Superseded []workMember
	// Work counts domain work (messages, rows): progress for unit-less phases.
	Work int64
	// Remaining reports that relevant pending work remains for the task.
	// Informational (inspection and adapters): the runner asks Next again
	// after every batch regardless.
	Remaining bool
	// Blocker is a concrete reason the task cannot continue now. It waits
	// for Wake or the reconcile tick and consumes no retry attempt.
	Blocker workBlocker
}

// workBlocker is a structured reason; surfaces render prose from it.
type workBlocker struct {
	Reason    string `json:"reason"`
	Detail    string `json:"detail,omitempty"`
	DependsOn string `json:"depends_on,omitempty"`
}

// Blocker reasons. Domains also return dependency, power, policy, writer and
// external; the runner adds the rest.
const (
	workBlockedDependency     = "dependency"
	workBlockedCapacity       = "capacity"
	workBlockedPower          = "power"
	workBlockedPolicy         = "policy"
	workBlockedWriter         = "writer"
	workBlockedExternal       = "external"
	workBlockedHeld           = "held"
	workBlockedFailed         = "failed"
	workBlockedFailuresFull   = "failures-full"
	workBlockedBackoff        = "retry-backoff"
	workBlockedExceedsCap     = "allocation-exceeds-cap"
	workBlockedRequestMemory  = "request-memory"
	workBlockedRequestLimit   = "request-limit"
	workBlockedRecentFirst    = "recent-first"
	workBlockedPaused         = "paused"
	workInputsKeepChanging    = "inputs-keep-changing"
	workUnitsFailed           = "units-failed"
	workDependencyFailed      = "dependency-failed"
	workMembershipUnavailable = "membership-unavailable"
)

// workError classifies a RunBatch error. A plain error is transient.
type workError struct {
	err       error
	units     []workMember
	permanent bool
	blocker   workBlocker
}

func (e *workError) Error() string {
	if e.err == nil {
		return "blocked: " + e.blocker.Reason
	}
	return e.err.Error()
}
func (e *workError) Unwrap() error { return e.err }

// workPermanent marks a configuration, missing-asset or unsupported-input
// failure: no automatic retry until the input changes or Retry.
func workPermanent(err error) error { return &workError{err: err, permanent: true} }

// workUnitFailed charges a transient failure to the named units only, so
// one bad unit does not hold back the rest of its batch.
func workUnitFailed(err error, units ...workMember) error {
	return &workError{err: err, units: units}
}

// workBlocked reports that the batch could not proceed (a lock owner, power,
// policy) without counting as a failed attempt.
func workBlocked(blocker workBlocker) error { return &workError{blocker: blocker} }

// workTask is a kind's adapter. Next is a bounded lookup over a prepared
// inventory and indexed readiness metadata; it must not scan the library,
// build corpus context or claim units, because the runner may call it again
// before running anything. Discovery and preparation are RunBatch phases.
// RunBatch joins every worker it starts before it returns.
type workTask interface {
	Next(context.Context, *workPopulation) (workSelection, error)
	RunBatch(context.Context, workSelection, workAllocation) (workBatchResult, error)
}

// workDefinition is one row of the static task table: a registered
// operation kind, its real prerequisites and its adapter. A Coarse task is
// a global/library-scope algorithm: it always receives the library
// population and covers a request only when Next reports nothing pending.
type workDefinition struct {
	Kind         string
	Dependencies []string
	// DependenciesForRequest may choose a subset of the declared edges for
	// optional analyses. It is evaluated once at Submit, so a request's
	// accepted input policy stays fixed. Policy changes interrupt old demand.
	DependenciesForRequest func() []string
	Coarse                 bool
	Task                   workTask
}

// orderWorkDefinitions validates the task table and returns it in a stable
// dependency order: table order among kinds whose prerequisites are met.
func orderWorkDefinitions(defs []workDefinition) ([]workDefinition, error) {
	byKind := map[string]int{}
	for i, def := range defs {
		if _, ok := lookupOperationKind(def.Kind); !ok {
			return nil, fmt.Errorf("work task %q is not a registered operation kind", def.Kind)
		}
		if def.Task == nil {
			return nil, fmt.Errorf("work task %s has no adapter", def.Kind)
		}
		if _, ok := byKind[def.Kind]; ok {
			return nil, fmt.Errorf("work task %s is defined twice", def.Kind)
		}
		byKind[def.Kind] = i
	}
	for _, def := range defs {
		for _, dependency := range def.Dependencies {
			if _, ok := byKind[dependency]; !ok {
				return nil, fmt.Errorf("work task %s depends on %q, which is not in the task table", def.Kind, dependency)
			}
		}
	}
	ordered := make([]workDefinition, 0, len(defs))
	placed := make([]bool, len(defs))
	for len(ordered) < len(defs) {
		progressed := false
		for i, def := range defs {
			if placed[i] || slices.ContainsFunc(def.Dependencies, func(d string) bool { return !placed[byKind[d]] }) {
				continue
			}
			placed[i], progressed = true, true
			ordered = append(ordered, def)
		}
		if !progressed {
			cycle := []string{}
			for i, def := range defs {
				if !placed[i] {
					cycle = append(cycle, def.Kind)
				}
			}
			return nil, fmt.Errorf("work task dependencies form a cycle among %s", strings.Join(cycle, ", "))
		}
	}
	return ordered, nil
}

// workPriority classifies remaining work (§6.2): a request's class applies
// to its batches, not to everything its kind does afterwards.
type workPriority int

const (
	workPrioritySafety workPriority = iota + 1
	workPriorityForeground
	workPriorityLive
	workPriorityBackfill
	workPriorityOptional
)

var workPriorityNames = map[workPriority]string{workPrioritySafety: "safety", workPriorityForeground: "foreground",
	workPriorityLive: "live", workPriorityBackfill: "backfill", workPriorityOptional: "optional"}

func (p workPriority) String() string { return workPriorityNames[p] }

// workClock is the runner's time source; tests advance a fake one.
type workClock interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
}

type workSystemClock struct{}

func (workSystemClock) Now() time.Time                         { return time.Now() }
func (workSystemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
