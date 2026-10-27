package archive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// A Terminal command that writes runs its operation through runCLIOperation
// (docs/background-work.md §6.5): it takes a lease before its first journal
// row and keeps it until its last is written, so the service on this Mac sees
// it as a live holder of the library and never mistakes it for a crash. A
// command that opens the catalog writable also journals its operation
// through the same low-priority writer as the service; one that never opens
// it (capture, backup) has only the lease, under the capture root.

// cliOperations is a command's registry, lease and journal writer.
type cliOperations struct {
	reg     *opRegistry
	lease   *processLease
	journal *journalWriter
	stop    context.CancelFunc
	done    chan struct{}
}

// cliFinalBudget bounds the final journal round at exit.
var cliFinalBudget = time.Second

// beginCLIOperations prepares command's lease (beside the catalog when it
// has one, under the capture root otherwise) and, with a catalog, its
// journal. Failing to take a lease never fails the command: it then runs
// with an in-memory registry only, as before leases existed.
func beginCLIOperations(config Config, catalog *Catalog, command string, kinds ...string) *cliOperations {
	reg := newOpRegistry("cli:" + command)
	ops := &cliOperations{reg: reg}
	holds := []Hold{}
	for _, kind := range kinds {
		if row, ok := lookupOperationKind(kind); ok {
			holds = append(holds, parseHolds(row.Holds)...)
		}
	}
	dirs := leaseDirs(config)
	dir := dirs[len(dirs)-1]
	if catalog != nil {
		dir = dirs[0]
	}
	lease, err := acquireProcessLease(dir, leaseInfo{Instance: reg.instance, PID: os.Getpid(), HostID: currentHost().ID, Process: reg.process,
		Kinds: kinds, Holds: holds, StartedAt: now()})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Pharos: could not take an operation lease (%v); the service will not see this command\n", err)
		return ops
	}
	ops.lease = lease
	if catalog != nil {
		catalog.ops = reg
		if ops.journal = newJournalWriter(catalog, reg); ops.journal != nil {
			catalog.recordInitialize(reg, triggerCLI)
			ctx, cancel := context.WithCancel(context.Background())
			ops.stop, ops.done = cancel, make(chan struct{})
			go func() {
				defer close(ops.done)
				ops.journal.run(ctx)
			}()
		}
	}
	return ops
}

// begin registers the command's operation and names it in the lease.
func (o *cliOperations) begin(ctx context.Context, spec OperationSpec) (context.Context, *Op) {
	ctx, op := o.reg.begin(ctx, spec)
	if o.lease != nil && o.journal != nil {
		_ = o.lease.update(func(info *leaseInfo) { info.Operations = append(info.Operations, string(op.ID())) })
	}
	return ctx, op
}

// close writes the last journal rows and ends the lease: unlinked when they
// were written; otherwise their outcomes are recorded in the lease, which is
// left unheld for the service's reconcile pass.
func (o *cliOperations) close() {
	if o == nil {
		return
	}
	if o.stop != nil {
		o.stop()
		<-o.done
	}
	if o.journal == nil {
		o.lease.release()
		return
	}
	deadline := time.Now().Add(cliFinalBudget)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	err := o.journal.round(ctx, 0)
	if errors.Is(err, errJournalBusy) {
		if left := time.Until(deadline); left > 0 {
			err = o.journal.round(ctx, left)
		}
	}
	if err == nil && !o.journal.pending() {
		o.lease.release()
		return
	}
	outcomes := map[string]leaseOutcome{}
	for _, view := range o.reg.views() {
		if opTerminal(view.State) {
			outcomes[string(view.ID)] = leaseOutcome{State: view.State, Reason: view.Reason, Error: view.Error, EndedAt: formatTime(view.Ended)}
		}
	}
	_ = o.lease.abandon(outcomes)
}

// runCLIOperation runs work as command's operation spec, with a lease and,
// given a writable catalog, a journal.
func runCLIOperation(config Config, catalog *Catalog, command string, spec OperationSpec, work func(ctx context.Context, op *Op) error) error {
	if spec.Trigger == "" {
		spec.Trigger = triggerCLI
	}
	ops := beginCLIOperations(config, catalog, command, spec.Kind)
	defer ops.close()
	ctx, op := ops.begin(context.Background(), spec)
	var err error
	func() {
		defer func() {
			if value := recover(); value != nil {
				op.finishPanic(value)
				panic(value)
			}
		}()
		err = work(ctx, op)
		op.Finish(err)
	}()
	return err
}
