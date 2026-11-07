package archive

import (
	"context"
	"sync"
)

// Leaf CPU admission (docs/work-execution-plan): a structural leaf that
// computes (an ingest of one source, the encoding of one captured file)
// takes CPU slots from its process's one workAuthority before it opens a
// writer, a read snapshot or a domain lock, and gives them back only after
// every worker it started has exited. Coordinators do not admit: they hold
// no CPU of their own and may wait on prerequisites, so a grant held across
// them would double count and could deadlock.
//
// Which authority answers, in order:
//  1. a grant already in ctx (withWorkAdmission): the leaf runs inside it,
//     never above it, and takes nothing more;
//  2. an authority in ctx with no grant yet (withWorkAuthority): a scheduled
//     command's, which has no service;
//  3. the catalog's attached work service (Catalog.work);
//  4. none (the classic engine): the leaf runs as it always has, at its own
//     most.

// workAuthorityKey carries an authority without a grant.
type workAuthorityKey struct{}

// withWorkAuthority makes a the authority leaves under ctx admit from. A nil a
// leaves ctx unchanged.
func withWorkAuthority(ctx context.Context, a *workAuthority) context.Context {
	if a == nil {
		return ctx
	}
	return context.WithValue(ctx, workAuthorityKey{}, a)
}

// workAuthorityFor is the authority a leaf under ctx answers to, or nil.
func workAuthorityFor(ctx context.Context, c *Catalog) *workAuthority {
	if value, ok := ctx.Value(workAdmissionKey{}).(workAdmission); ok && value.authority != nil {
		return value.authority
	}
	if a, ok := ctx.Value(workAuthorityKey{}).(*workAuthority); ok {
		return a
	}
	if c != nil && c.work != nil {
		return c.work.authority
	}
	return nil
}

// workCPUGrant is an admitted leaf's allocation. Ctx carries the grant, so
// nested leaves run inside it. Release is idempotent; call it only after the
// leaf's workers have all exited.
type workCPUGrant struct {
	Ctx        context.Context
	Allocation workAllocation
	release    func()
}

func (g *workCPUGrant) Release() {
	if g != nil && g.release != nil {
		g.release()
	}
}

// acquireWorkCPU admits a leaf for between least and most CPU slots (see the
// order above). It waits in arrival order, records the wait and the grant in
// ctx's task resources, and fails with ctx's cause if ctx ends first, or with
// errWorkGrantExceedsCap if least can never fit. The caller must not hold a
// catalog writer, a read transaction or a domain lock while it waits.
func acquireWorkCPU(ctx context.Context, c *Catalog, least, most int) (*workCPUGrant, error) {
	least = max(least, 1)
	most = max(most, least)
	if value, ok := ctx.Value(workAdmissionKey{}).(workAdmission); ok {
		// Reentrant: the caller's grant already counts these slots.
		return &workCPUGrant{Ctx: ctx, Allocation: workAllocation{CPUs: max(1, min(most, value.allocation.CPUs))}, release: func() {}}, nil
	}
	a := workAuthorityFor(ctx, c)
	if a == nil {
		return &workCPUGrant{Ctx: ctx, Allocation: workAllocation{CPUs: most}, release: func() {}}, nil
	}
	if ctx.Err() != nil {
		// Free slots are granted without waiting; an ended leaf takes none.
		return nil, context.Cause(ctx)
	}
	res := taskResourcesFrom(ctx)
	endWait := res.waitSpan(waitCapacity)
	granted, err := a.acquireCPUs(ctx, least, most)
	endWait()
	if err != nil {
		return nil, err
	}
	allocation := workAllocation{CPUs: granted}
	endGrant := res.allocate(taskAllocation{CPUSlots: granted})
	endActive := res.activeSpan()
	var once sync.Once
	return &workCPUGrant{Ctx: withWorkAdmission(ctx, a, allocation), Allocation: allocation, release: func() {
		once.Do(func() {
			endActive()
			endGrant()
			a.releaseCPUs(granted)
		})
	}}, nil
}

// admitWorkCPU runs fn on the caller's goroutine as an admitted leaf, and
// releases after fn returns (fn joins its own workers), also on a panic.
func admitWorkCPU(ctx context.Context, c *Catalog, least, most int, fn func(context.Context, workAllocation) error) error {
	grant, err := acquireWorkCPU(ctx, c, least, most)
	if err != nil {
		return err
	}
	defer grant.Release()
	return fn(grant.Ctx, grant.Allocation)
}

// admittedIngest runs one source's ingest (single-threaded: one slot) as an
// admitted leaf. An admission that fails (a stop while waiting) is that
// source's pending result, as an ingest cancelled at its start would be.
func (c *Catalog) admittedIngest(ctx context.Context, source, host string, ingest func(context.Context) IngestResult) IngestResult {
	var result IngestResult
	if err := admitWorkCPU(ctx, c, 1, 1, func(ctx context.Context, _ workAllocation) error {
		result = ingest(ctx)
		return nil
	}); err != nil {
		return IngestResult{Source: source, Host: host, Pending: true, Error: err.Error()}
	}
	return result
}

// cliWorkAuthority is this command process's one CPU authority under the
// scheduled engine, shared by every command the process runs (an app process
// running several direct calls included); nil under the classic engine. The
// latest command's background_cpu_slots is the allowance; a lowered one drains
// at the running leaves' boundaries.
func cliWorkAuthority(config Config) *workAuthority {
	if config.WorkEngine != "scheduled" {
		return nil
	}
	cliAuthorityMu.Lock()
	defer cliAuthorityMu.Unlock()
	cpus := max(1, config.BackgroundCPUs)
	if cliAuthority == nil {
		cliAuthority = newWorkAuthority(cpus)
	} else if cliAuthority.cap() != cpus {
		cliAuthority.SetCPUs(cpus)
	}
	return cliAuthority
}

// cliAuthority is package state so a test can start from none with override.
var (
	cliAuthorityMu sync.Mutex
	cliAuthority   *workAuthority
)
