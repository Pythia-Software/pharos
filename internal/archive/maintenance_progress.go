package archive

import (
	"context"
	"database/sql"
	"time"
)

// A maintenance pass keeps its inventory beside the worker, not in the status
// request. Counts are refreshed at most once a second, or when the next batch
// would exhaust the last inventory. New arrivals can increase the total, once
// they amount to more than a few percent of it; the fraction measures this
// pass's work, not elapsed time or a fixed-size library.
type maintenanceProgress struct {
	phases map[string]*maintenancePhase
}

type maintenancePhase struct {
	done, total int
	counted     time.Time
}

type maintenanceProgressKey struct{}

var maintenanceInventoryInterval = time.Second

// A reported total may lag arrivals by one part in maintenanceGrowthTolerance.
const maintenanceGrowthTolerance = 32

type maintenanceCounter interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// maintenanceBatch announces real work before an expensive batch and returns a
// completion callback. Call it with the commit's result: failed/rolled-back
// batches never advance. inventory counts pending work INCLUDING this batch in
// the caller's transaction/snapshot. Callers that already removed their queue
// entries add the selected batch back in that query.
//
// Only daemon passes collect this inventory. Other uses of the same batch
// functions, including tests and synchronous per-workspace projection refreshes,
// do not gain database queries or overwrite their enclosing progress.
func maintenanceBatch(ctx context.Context, q maintenanceCounter, phase, unit string, count int, inventory string, args ...any) func(error) {
	progress, _ := ctx.Value(maintenanceProgressKey{}).(*maintenanceProgress)
	if progress == nil || opFrom(ctx) == nil || count <= 0 {
		return func(error) {}
	}
	if progress.phases == nil {
		progress.phases = map[string]*maintenancePhase{}
	}
	state := progress.phases[phase]
	if state == nil {
		state = &maintenancePhase{total: -1}
		progress.phases[phase] = state
	}
	if state.counted.IsZero() || time.Since(state.counted) >= maintenanceInventoryInterval || state.total >= 0 && state.done+count >= state.total {
		var pending int
		if err := q.QueryRowContext(ctx, inventory, args...).Scan(&pending); err == nil {
			total := state.done + max(count, pending)
			// The estimator measures a rate against one total and starts over
			// when the total changes. Work that arrives while a queue drains
			// would restart it every second, so slight growth waits: until it
			// passes one part in maintenanceGrowthTolerance of the total, or
			// the batch that would otherwise appear to finish the queue.
			if state.total < 0 || total < state.total || total-state.total > state.total/maintenanceGrowthTolerance || state.done+count >= state.total {
				state.total = total
			}
		} else {
			// Instrumentation must never make otherwise useful work fail, nor
			// continue presenting an inventory that could not be read.
			state.total = -1
		}
		state.counted = time.Now()
	}
	reportWork(ctx, phase, unit, state.done, state.total)
	return func(err error) {
		if err == nil {
			state.done += count
			reportWork(ctx, phase, unit, state.done, state.total)
		}
	}
}
