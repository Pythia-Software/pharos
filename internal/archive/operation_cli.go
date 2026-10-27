package archive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
	// leaseLocks are the locks the lease last listed; leaseStale is set by a
	// release not yet written.
	leaseMu    sync.Mutex
	leaseLocks []Hold
	leaseStale atomic.Bool
	// leaseWrites counts lease rewrites, for tests.
	leaseWrites atomic.Int64
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
	cliLeasesHeld.Add(1)
	// The locks it takes are named in the lease as it takes them.
	reg.holdsChanged = ops.locksChanged
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

// locksChanged rewrites the lease at once when a lock is taken, so the
// service sees it before the command writes under it. A release only marks
// the lease stale: the journal's next tick (or the command's end) rewrites
// it, so a loop of short locks costs one lease write per lock, not two. A
// command without a journal rewrites at once either way.
func (o *cliOperations) locksChanged(acquired bool) {
	if acquired || o.journal == nil {
		o.refreshLeaseHolds()
		return
	}
	o.leaseStale.Store(true)
}

// flushLeaseLocks rewrites the lease if a release left it stale.
func (o *cliOperations) flushLeaseLocks() {
	if o.leaseStale.Swap(false) {
		o.refreshLeaseHolds()
	}
}

// refreshLeaseHolds writes the locks the command's running operations hold
// now into its lease, apart from its declared holds, unless they are what
// the lease already says.
func (o *cliOperations) refreshLeaseHolds() {
	locks := []Hold{}
	for _, view := range o.reg.views() {
		if opTerminal(view.State) {
			continue
		}
		for _, hold := range view.Locks {
			if !slices.Contains(locks, hold) {
				locks = append(locks, hold)
			}
		}
	}
	o.leaseMu.Lock()
	defer o.leaseMu.Unlock()
	if slices.Equal(locks, o.leaseLocks) {
		return
	}
	o.leaseLocks = locks
	o.updateLeases(func(info *leaseInfo) { info.Locks = locks })
}

func (o *cliOperations) updateLeases(change func(*leaseInfo)) {
	o.leaseWrites.Add(1)
	for _, lease := range o.leases {
		_ = lease.update(change)
	}
}

func (o *cliOperations) releaseLeases() {
	for _, lease := range o.leases {
		lease.release()
	}
	cliLeasesHeld.Add(-1)
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
func (o *cliOperations) close() { o.closeWith(nil) }

// closeWith is close, running closeCatalog after the last journal round and
// before the lease ends.
func (o *cliOperations) closeWith(closeCatalog func()) {
	if o == nil {
		return
	}
	if closeCatalog == nil {
		closeCatalog = func() {}
	}
	if o.stop != nil {
		o.stop()
		<-o.done
	}
	if o.journal == nil {
		closeCatalog()
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
	written := err == nil && !o.journal.pending()
	closeCatalog()
	if written {
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
	cliLeasesHeld.Add(-1)
}

// runCLIOperation runs work as command's operation spec, with a lease and,
// given a writable catalog, a journal.
//
// It is for commands that never open the catalog (lease under the capture
// root); one that does runs through runLeasedCatalogCommand, so its lease
// covers the open and the close too.
func runCLIOperation(config Config, command string, spec OperationSpec, work func(ctx context.Context, op *Op) error) error {
	ops, err := beginCLIOperations(config, nil, command, spec.Kind)
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

// runLeasedCatalogCommand runs a command that opens the catalog: it takes
// the lease beside the catalog first, opens it with Initialize recorded as a
// live child operation, runs work as spec's operation (a read command, with
// no kind, runs as the catalog's open itself), and releases the lease only
// after the catalog has closed, its checkpoint included. A command that
// cannot take its lease opens nothing.
func runLeasedCatalogCommand(config Config, command string, spec OperationSpec, work func(ctx context.Context, op *Op, catalog *Catalog) error) error {
	return runLeasedCatalog(config, command, spec, OpenCatalog, work)
}

// runLeasedCatalog is runLeasedCatalogCommand with the catalog's open (a
// command that must not run Initialize opens it without).
func runLeasedCatalog(config Config, command string, spec OperationSpec, open func(string) (*Catalog, error), work func(ctx context.Context, op *Op, catalog *Catalog) error) (err error) {
	read := spec.Kind == ""
	if read {
		spec = OperationSpec{Kind: "catalog.initialize"}
	}
	if spec.Trigger == "" {
		spec.Trigger = triggerCLI
	}
	ops, err := beginCLIOperationsIn([]string{leaseDirs(config)[0]}, nil, nil, command, spec.Kind, "catalog.initialize")
	if err != nil {
		return err
	}
	ctx, op := ops.begin(context.Background(), spec)
	initialize := op
	if !read {
		_, initialize = op.Child(OperationSpec{Kind: "catalog.initialize", Trigger: triggerCLI})
	}
	catalog, err := open(config.CatalogPath)
	if !read || err != nil {
		initialize.Finish(err)
	}
	if err != nil {
		op.Finish(err)
		ops.close()
		return err
	}
	ops.attach(catalog)
	defer func() {
		ops.closeCatalog(catalog)
	}()
	func() {
		defer func() {
			if value := recover(); value != nil {
				op.finishPanic(value)
				panic(value)
			}
		}()
		err = work(ctx, op, catalog)
		op.Finish(err)
	}()
	return err
}

// attach starts journaling into catalog, once it is open.
func (o *cliOperations) attach(catalog *Catalog) {
	catalog.ops = o.reg
	if o.journal = newJournalWriter(catalog, o.reg); o.journal == nil {
		return
	}
	o.journal.onTick = o.flushLeaseLocks
	ids := []string{}
	for _, view := range o.reg.views() {
		ids = append(ids, string(view.ID))
	}
	o.updateLeases(func(info *leaseInfo) { info.Operations = append(info.Operations, ids...) })
	ctx, cancel := context.WithCancel(context.Background())
	o.stop, o.done = cancel, make(chan struct{})
	go func() {
		defer close(o.done)
		o.journal.run(ctx)
	}()
}

// closeCatalog writes the last journal rows, closes the catalog (and its
// checkpoint) while the lease is still held, then ends the lease.
func (o *cliOperations) closeCatalog(catalog *Catalog) {
	o.closeWith(func() { _ = catalog.Close() })
}

// cliLeasesHeld counts the commands of this process holding their leases.
var cliLeasesHeld atomic.Int32

// strictCLILeases makes a catalog write, Initialize, checkpoint or close
// without a held lease a recorded violation. Tests of Terminal commands set
// it; a service never does.
var (
	strictCLILeases   = false
	cliLeaseViolation struct {
		sync.Mutex
		seen []string
	}
)

func cliWriteCheck(what string) {
	if !strictCLILeases || cliLeasesHeld.Load() > 0 {
		return
	}
	cliLeaseViolation.Lock()
	cliLeaseViolation.seen = append(cliLeaseViolation.seen, what)
	cliLeaseViolation.Unlock()
}
