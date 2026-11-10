package archive

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
)

// Admission limits for request bookkeeping (§6.9). Package variables so a
// test can lower them with override.
var (
	workRequestMemoryLimit   int64 = 32 << 20
	workProcessMemoryLimit   int64 = 128 << 20
	workExplicitRequestLimit       = 8
)

// errWorkGrantExceedsCap refuses a minimum allocation the cap can never
// satisfy, rather than queueing it forever.
var errWorkGrantExceedsCap = errors.New("the requested CPU allocation exceeds this process's allowance")

// workAuthority is the per-process allocation authority: one CPU allowance
// shared by every managed structural stage in this process (§7.2), and the
// memory budget for request bookkeeping. A CPU slot is cooperative runnable
// parallelism, not a core. It is "this library service" or "this command";
// separate processes do not share it.
type workAuthority struct {
	mu      sync.Mutex
	cpuCap  int
	cpuUsed int
	waiters []*workCPUWaiter
	// arrivals numbers inline waiters. A foreground runner batch that
	// waits claims its place in that order: waiters that arrived earlier
	// still go first, later ones wait behind it, so inline callers cannot
	// starve explicit work.
	arrivals uint64
	claimed  bool
	claimAt  uint64
	memUsed  int64
	// notify is told that capacity may have become available. It must not
	// block or take the authority's lock.
	notify func()
}

type workCPUWaiter struct {
	seq         uint64
	least, most int
	granted     int
	err         error
	ready       chan struct{}
}

func newWorkAuthority(cpus int) *workAuthority {
	return &workAuthority{cpuCap: max(cpus, 1)}
}

// workAuthorityFacts is the authority's state for inspection. Allocated
// CPUs are a grant, not measured usage.
type workAuthorityFacts struct {
	CPUCap          int   `json:"cpu_cap"`
	CPUAllocated    int   `json:"cpu_allocated"`
	CPUOverBudget   int   `json:"cpu_over_budget,omitempty"`
	CPUWaiters      int   `json:"cpu_waiters,omitempty"`
	MemoryReserved  int64 `json:"memory_reserved"`
	MemoryLimit     int64 `json:"memory_limit"`
	RequestMemory   int64 `json:"request_memory_limit"`
	ExplicitRequest int   `json:"explicit_request_limit"`
}

func (a *workAuthority) facts() workAuthorityFacts {
	a.mu.Lock()
	defer a.mu.Unlock()
	return workAuthorityFacts{CPUCap: a.cpuCap, CPUAllocated: a.cpuUsed, CPUOverBudget: max(0, a.cpuUsed-a.cpuCap),
		CPUWaiters: len(a.waiters), MemoryReserved: a.memUsed, MemoryLimit: workProcessMemoryLimit,
		RequestMemory: workRequestMemoryLimit, ExplicitRequest: workExplicitRequestLimit}
}

// cap is the current CPU allowance.
func (a *workAuthority) cap() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cpuCap
}

// SetCPUs changes the allowance (a setting or power change). Shrinking
// revokes nothing: grants drain at their batch boundary, the excess shows as
// over budget, and nothing new is admitted until usage is under the cap.
// Waiters whose minimum the new cap can never satisfy fail.
func (a *workAuthority) SetCPUs(cpus int) {
	a.mu.Lock()
	a.cpuCap = max(cpus, 1)
	kept := a.waiters[:0]
	for _, w := range a.waiters {
		if w.least > a.cpuCap {
			w.err = errWorkGrantExceedsCap
			close(w.ready)
			continue
		}
		kept = append(kept, w)
	}
	a.waiters = kept
	a.grantWaitersLocked()
	a.mu.Unlock()
	a.signal()
}

// tryCPUs grants between least and most CPUs now, or nothing. reserve is
// capacity kept for a fixed envelope that waits: the minimum must fit beside
// it, and nothing beyond the minimum comes out of it. It never overtakes an
// inline caller already waiting ahead of it. With claim (foreground work), a
// refusal for capacity keeps the runner's place: inline callers that arrive
// later wait until the claim is granted or dropped. reserved reports a
// refusal that only the reserve caused.
func (a *workAuthority) tryCPUs(least, most, reserve int, claim bool) (granted int, ok, reserved bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	free := a.cpuCap - a.cpuUsed
	if a.waiterAheadLocked() || least > free {
		if claim && !a.claimed {
			a.claimed, a.claimAt = true, a.arrivals
		}
		return 0, false, false
	}
	if least > free-reserve {
		return 0, false, true
	}
	granted = min(most, free-reserve)
	a.cpuUsed += granted
	if claim && a.claimed {
		a.claimed = false
		a.grantWaitersLocked()
	}
	return granted, true, false
}

// waiterAheadLocked reports an inline waiter that goes before the runner.
func (a *workAuthority) waiterAheadLocked() bool {
	return len(a.waiters) > 0 && (!a.claimed || a.waiters[0].seq <= a.claimAt)
}

// dropClaim ends a foreground claim the runner no longer needs.
func (a *workAuthority) dropClaim() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.claimed {
		a.claimed = false
		a.grantWaitersLocked()
	}
}

func (a *workAuthority) releaseCPUs(n int) {
	if n <= 0 {
		return
	}
	a.mu.Lock()
	a.cpuUsed -= n
	if a.cpuUsed < 0 {
		panic("work authority released more CPUs than it granted")
	}
	a.grantWaitersLocked()
	a.mu.Unlock()
	a.signal()
}

// grantWaitersLocked serves inline callers in arrival order, up to a
// foreground claim.
func (a *workAuthority) grantWaitersLocked() {
	for len(a.waiters) > 0 {
		w := a.waiters[0]
		free := a.cpuCap - a.cpuUsed
		if free < w.least || (a.claimed && w.seq > a.claimAt) {
			return
		}
		w.granted = min(w.most, free)
		a.cpuUsed += w.granted
		close(w.ready)
		a.waiters = a.waiters[1:]
	}
}

func (a *workAuthority) signal() {
	if a.notify != nil {
		a.notify()
	}
}

// acquireCPUs waits for between least and most CPUs, in arrival order.
func (a *workAuthority) acquireCPUs(ctx context.Context, least, most int) (int, error) {
	least = max(least, 1)
	most = max(most, least)
	a.mu.Lock()
	if cpuCap := a.cpuCap; least > cpuCap {
		a.mu.Unlock()
		return 0, fmt.Errorf("%w: needs %d, allowance %d", errWorkGrantExceedsCap, least, cpuCap)
	}
	if free := a.cpuCap - a.cpuUsed; len(a.waiters) == 0 && !a.claimed && free >= least {
		granted := min(most, free)
		a.cpuUsed += granted
		a.mu.Unlock()
		return granted, nil
	}
	a.arrivals++
	w := &workCPUWaiter{seq: a.arrivals, least: least, most: most, ready: make(chan struct{})}
	a.waiters = append(a.waiters, w)
	a.mu.Unlock()
	select {
	case <-w.ready:
		return w.granted, w.err
	case <-ctx.Done():
	}
	a.mu.Lock()
	select {
	case <-w.ready:
		// Granted while the context ended: give it back.
		a.cpuUsed -= w.granted
	default:
		if i := slices.Index(a.waiters, w); i >= 0 {
			a.waiters = append(a.waiters[:i], a.waiters[i+1:]...)
		}
	}
	a.grantWaitersLocked()
	a.mu.Unlock()
	a.signal()
	return 0, context.Cause(ctx)
}

// Call runs fn inline on the caller's goroutine with an allocation from the
// same authority, for existing structural callers not yet driven by the
// runner. It waits in arrival order and releases only after fn returns.
func (a *workAuthority) Call(ctx context.Context, least, most int, fn func(context.Context, workAllocation) error) error {
	granted, err := a.acquireCPUs(ctx, least, most)
	if err != nil {
		return err
	}
	defer a.releaseCPUs(granted)
	return fn(ctx, workAllocation{CPUs: granted})
}

// reserveMemory charges request bookkeeping before it is allocated, leaving
// headroom unreserved (room kept for foreground requests).
func (a *workAuthority) reserveMemory(n, headroom int64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.memUsed+n+headroom > workProcessMemoryLimit {
		return false
	}
	a.memUsed += n
	return true
}

func (a *workAuthority) releaseMemory(n int64) {
	if n <= 0 {
		return
	}
	a.returnMemory(n)
	a.signal()
}

// returnMemory rolls back a reservation that was never used, without
// signalling: no capacity became available, so nothing should reconsider.
func (a *workAuthority) returnMemory(n int64) {
	a.mu.Lock()
	a.memUsed -= n
	a.mu.Unlock()
}
