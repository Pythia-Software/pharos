package archive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
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
	reg *opRegistry
	// leases are the command's lease files, one per directory it must be
	// seen from (a transfer: both libraries'); one instance, all held.
	leases  []*processLease
	journal *journalWriter
	stop    context.CancelFunc
	done    chan struct{}
	// declared are the kinds' holds, with this Mac resolved.
	declared []Hold
}

// cliFinalBudget bounds the final journal round at exit.
var cliFinalBudget = time.Second

// beginCLIOperations takes command's lease (beside the catalog when it has
// one, under the capture root otherwise) and, with a catalog, starts its
// journal. A writing command that cannot take its lease does not run: the
// service could not tell it is writing (fail closed, §6.5).
func beginCLIOperations(config Config, catalog *Catalog, command string, kinds ...string) (*cliOperations, error) {
	dirs := leaseDirs(config)
	dir := dirs[len(dirs)-1]
	if catalog != nil {
		dir = dirs[0]
	}
	return beginCLIOperationsIn([]string{dir}, nil, catalog, command, kinds...)
}

// beginCLIOperationsIn takes command's lease in each of dirs, naming the
// library configurations it touches.
func beginCLIOperationsIn(dirs, libraries []string, catalog *Catalog, command string, kinds ...string) (*cliOperations, error) {
	reg := newOpRegistry("cli:" + command)
	ops := &cliOperations{reg: reg}
	for _, kind := range kinds {
		if row, ok := lookupOperationKind(kind); ok {
			ops.declared = append(ops.declared, resolveHolds(parseHolds(row.Holds))...)
		}
	}
	for _, dir := range dirs {
		lease, err := acquireProcessLease(dir, leaseInfo{Instance: reg.instance, PID: os.Getpid(), HostID: currentHost().ID, Process: reg.process,
			Kinds: kinds, Holds: ops.declared, Libraries: libraries, StartedAt: now()})
		if err != nil {
			for _, taken := range ops.leases {
				taken.release()
			}
			return nil, fmt.Errorf("Pharos could not take its operation lease in %s, so a running Pharos could not see this command writing; nothing was changed: %w", dir, err)
		}
		ops.leases = append(ops.leases, lease)
	}
	// The locks it takes are named in the lease as it takes them.
	reg.holdsChanged = ops.refreshLeaseHolds
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
	return ops, nil
}

// resolveHolds names this Mac in holds declared for "this" (C(this)).
func resolveHolds(holds []Hold) []Hold {
	resolved := slices.Clone(holds)
	for i := range resolved {
		if resource, ok := strings.CutSuffix(resolved[i].Resource, ":this"); ok {
			resolved[i].Resource = resource + ":" + currentHost().ID
		}
	}
	return resolved
}

// refreshLeaseHolds writes the declared holds and the locks the command's
// running operations hold now into its lease.
func (o *cliOperations) refreshLeaseHolds() {
	holds := slices.Clone(o.declared)
	for _, view := range o.reg.views() {
		if opTerminal(view.State) {
			continue
		}
		for _, hold := range view.Spec.Holds {
			if !slices.Contains(holds, hold) {
				holds = append(holds, hold)
			}
		}
	}
	o.updateLeases(func(info *leaseInfo) { info.Holds = holds })
}

func (o *cliOperations) updateLeases(change func(*leaseInfo)) {
	for _, lease := range o.leases {
		_ = lease.update(change)
	}
}

func (o *cliOperations) releaseLeases() {
	for _, lease := range o.leases {
		lease.release()
	}
}

// begin registers the command's operation and names it in the lease.
func (o *cliOperations) begin(ctx context.Context, spec OperationSpec) (context.Context, *Op) {
	ctx, op := o.reg.begin(ctx, spec)
	if o.journal != nil {
		o.updateLeases(func(info *leaseInfo) { info.Operations = append(info.Operations, string(op.ID())) })
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
		o.releaseLeases()
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
		o.releaseLeases()
		return
	}
	outcomes := map[string]leaseOutcome{}
	for _, view := range o.reg.views() {
		if opTerminal(view.State) {
			outcomes[string(view.ID)] = leaseOutcome{State: view.State, Reason: view.Reason, Error: view.Error, EndedAt: formatTime(view.Ended)}
		}
	}
	for _, lease := range o.leases {
		_ = lease.abandon(outcomes)
	}
}

// runCLIOperation runs work as command's operation spec, with a lease and,
// given a writable catalog, a journal.
func runCLIOperation(config Config, catalog *Catalog, command string, spec OperationSpec, work func(ctx context.Context, op *Op) error) error {
	ops, err := beginCLIOperations(config, catalog, command, spec.Kind)
	if err != nil {
		return err
	}
	return ops.run(spec, work)
}

// runCLIOperationIn runs work with leases in dirs and no journal: a
// command that writes libraries it does not open as a catalog (a transfer,
// a recovery), naming their configurations.
func runCLIOperationIn(dirs, libraries []string, command string, spec OperationSpec, work func(ctx context.Context, op *Op) error) error {
	ops, err := beginCLIOperationsIn(dirs, libraries, nil, command, spec.Kind)
	if err != nil {
		return err
	}
	return ops.run(spec, work)
}

func (ops *cliOperations) run(spec OperationSpec, work func(ctx context.Context, op *Op) error) (err error) {
	if spec.Trigger == "" {
		spec.Trigger = triggerCLI
	}
	defer ops.close()
	ctx, op := ops.begin(context.Background(), spec)
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
