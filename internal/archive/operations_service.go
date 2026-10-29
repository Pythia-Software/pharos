package archive

import (
	"context"
	"errors"
	"runtime/debug"
	"time"
)

// The library service's side of the registry: start and begin register
// structural work (§2.3), daemon registers the loops that are not operations
// (§3.4), and Catalog-level work reaches the same registry through
// Catalog.beginOp and Catalog.startBackground.

var errServiceStopping = errors.New("Pharos is stopping")

// start registers spec's operation and runs work in the background, as spawn
// does: counted in the service's workers, with SetPanicOnFault, and refused
// (registering nothing) once the service is stopping. It ends the operation
// with work's result.
func (s *Server) start(spec OperationSpec, work func(ctx context.Context, op *Op) error) bool {
	var op *Op
	return s.spawnRegistered(func() { _, op = s.ops.begin(s.life.ctx, spec) }, func() { runOperation(op, work) })
}

// launch runs work in the background for an operation begun inline, as
// start does. When the service is stopping it ends the operation and
// reports false.
func (s *Server) launch(op *Op, work func(ctx context.Context, op *Op) error) bool {
	if s.spawnRegistered(nil, func() { runOperation(op, work) }) {
		return true
	}
	op.Finish(errServiceStopping)
	return false
}

// runOperation runs work at op's priority class (Op.RunIO) and finishes op
// with its result. A panic fails the operation and is raised again.
func runOperation(op *Op, work func(ctx context.Context, op *Op) error) {
	defer func() {
		if value := recover(); value != nil {
			op.finishPanic(value)
			panic(value)
		}
	}()
	var err error
	op.RunIO(func() { err = work(op.Context(), op) })
	op.Finish(err)
}

// begin registers an operation that runs on the caller's goroutine (an HTTP
// handler, a chain stage). The caller calls Finish, normally deferred.
func (s *Server) begin(ctx context.Context, spec OperationSpec) (context.Context, *Op) {
	return s.ops.begin(ctx, spec)
}

// daemonNames lists the service's loops that are daemons, not operations
// (§3.4): they hold nothing and are never shown, and each pass that does real
// work starts an operation. The unregistered-work test accounts for them.
var daemonNames = []string{"maintainLibrary", "maintainSubstringIndex", "maintainToolOutputIndex", "maintainTextSearch", "maintainToolSearch", "maintainQueryTextIndexes",
	"keepWALSmall", "automaticLoop", "cloudScheduleLoop", "journalWriter", "leaseReconciler", "watchFeatureFlags", "compressCaptures"}

// daemon runs a registered daemon loop in the background.
func (s *Server) daemon(name string, loop func(ctx context.Context)) bool {
	_ = name // the name is for the unregistered-work test and readers
	return s.spawn(loop)
}

// spawnRegistered is spawn with register run first under the lifecycle lock,
// so no stop can begin between registering and counting the worker.
func (s *Server) spawnRegistered(register func(), work func()) bool {
	s.life.mu.Lock()
	defer s.life.mu.Unlock()
	if s.life.stopping {
		return false
	}
	if register != nil {
		register()
	}
	s.life.workers.Add(1)
	go func() {
		defer s.life.workers.Done()
		defer s.exitOnFault()
		debug.SetPanicOnFault(true)
		work()
	}()
	return true
}

// releasing reports a release (or another stop) in progress. It takes no
// lock: begin calls it with the lifecycle lock held.
func (s *Server) releasing() bool {
	return s.life.ctx.Err() != nil
}

// serviceStopping reports that the service this catalog serves is
// stopping (a release, a quit): optional work then gives way.
func (c *Catalog) serviceStopping() bool {
	return c != nil && c.ops != nil && c.ops.releasing != nil && c.ops.releasing()
}

// beginOp registers an operation for Catalog-level work, as a child of the
// operation ctx carries if any. Without a registry (a catalog opened by
// tests or a read-only command) it returns ctx and a nil handle, which
// records nothing.
func (c *Catalog) beginOp(ctx context.Context, spec OperationSpec) (context.Context, *Op) {
	if c == nil || c.ops == nil {
		return ctx, nil
	}
	return c.ops.begin(ctx, spec)
}

// startBackground starts Catalog-level background work as an operation:
// with the service's start when the catalog has a service, or in a goroutine
// counted in unowned otherwise. It reports false if the service is stopping.
func (c *Catalog) startBackground(spec OperationSpec, work func(ctx context.Context, op *Op) error) bool {
	if c.background != nil {
		return c.background(spec, work)
	}
	ctx, op := c.beginOp(context.Background(), spec)
	c.unowned.Go(func() { runOperation(op, func(context.Context, *Op) error { return work(ctx, op) }) })
	return true
}

// flushBudget is what the final journal round may take when a stop that
// began at started with timeout ends at now (§6.2 Flush on release): at most
// 1 s, and what is left of the timeout; a stop that already timed out still
// gets 1 s.
func flushBudget(started time.Time, timeout time.Duration, now time.Time) time.Duration {
	left := started.Add(timeout).Sub(now)
	if left <= 0 {
		return time.Second
	}
	return min(time.Second, left)
}

// daemonPass is the operation of a daemon's stretch of real work (§3.4): it
// is registered before the work runs, published once the work does
// something, and forgotten if the stretch turned out to have nothing to do.
type daemonPass struct {
	op  *Op
	did bool
}

// beginPass starts a pass of kind for a daemon of c.
func (c *Catalog) beginPass(ctx context.Context, kind, trigger string) *daemonPass {
	if c == nil || c.ops == nil {
		return &daemonPass{}
	}
	_, op := c.ops.beginTentative(ctx, OperationSpec{Kind: kind, Trigger: trigger})
	return &daemonPass{op: op}
}

// worked records n units of real work.
func (p *daemonPass) worked(n int) {
	if p == nil || p.op == nil {
		return
	}
	p.did = true
	p.op.Advance(int64(n))
}

// end finishes the pass: discarded when it did nothing and failed nothing.
func (p *daemonPass) end(err error) {
	if p == nil || p.op == nil {
		return
	}
	if !p.did && err == nil {
		p.op.discard()
		return
	}
	p.op.Finish(err)
}

// runIO runs one batch of the pass at its operation's priority class
// (Op.RunIO); without an operation it runs fn as is.
func (p *daemonPass) runIO(fn func()) {
	if p == nil {
		fn()
		return
	}
	p.op.RunIO(fn)
}

// active reports a pass with an operation open.
func (p *daemonPass) active() bool { return p != nil && p.op != nil }

// defaultError is err, or fallback when err is nil.
func defaultError(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}
