package archive

import (
	"context"
)

type sqliteAcquisitionKey struct{}
type sqliteAcquisitionBoundary struct {
	root       string
	reconciled map[string]bool
	identities map[string]map[string]string
}

// Planning and execution share one boundary. Service sync retains executable
// plan groups before IngestContext, so reconciliation must run during planning
// and its identities must still be present when those groups are executed.
func (c *Catalog) withSQLiteAcquisition(ctx context.Context, adapter Adapter) context.Context {
	if !isConductorSource(adapter.Config().Kind) || adapter.Config().Options["sqlite_record_capture"] != true || captureViewOf(adapter) != nil {
		return ctx
	}
	boundary, _ := ctx.Value(sqliteAcquisitionKey{}).(*sqliteAcquisitionBoundary)
	if plan := modeOf(ctx).plan; plan != nil && plan.sqliteAcquisition != nil {
		boundary = plan.sqliteAcquisition
	}
	if boundary == nil {
		boundary = &sqliteAcquisitionBoundary{root: c.captureRootPath(), reconciled: map[string]bool{}, identities: map[string]map[string]string{}}
	}
	return context.WithValue(ctx, sqliteAcquisitionKey{}, boundary)
}

// acquireConductorSession is the shared capture-before-parse boundary. Its
// caller may have done a metadata-only planning pass. Parsing uses only the
// committed revision, after the capture lock is released. Automatic rollout
// remains opt-in until the production-size performance gate is met.
func acquireConductorSession(ctx context.Context, root string, source SourceConfig, database, sid string, emit func(WorkspaceRecord) error) error {
	c, e := beginSQLiteRecordSource(ctx, root, source)
	if e != nil {
		return e
	}
	session, dir, m := c.session, c.dir, c.manifest
	closed := false
	defer func() {
		if !closed {
			session.Close()
		}
	}()
	state, e := sqliteDependencyState(database)
	if e != nil {
		return e
	}
	if e = reserveCaptureAcquisition(dir, min(state.Size, recordMaxBytes)); e != nil {
		return e
	}
	s := newRecordStore(c)
	ref, e := s.exportSQLite(database, database, "session:"+sid, state, sid, nil)
	if e != nil {
		return e
	}
	if e = c.commit(false); e != nil {
		return e
	}
	owner := "reader:" + randomToken()
	if e = s.pinRevision(owner, ref); e != nil {
		return e
	}
	v, e := s.loadRevision(ref)
	if e != nil {
		return e
	}
	session.Close()
	closed = true
	defer func() {
		if lock, e := lockCaptureTarget(context.Background(), dir); e == nil {
			defer lock.Close()
			_ = releasePreservationOwner(dir, owner)
		}
	}()
	reader := &sqliteReplayAdapter{baseAdapter: baseAdapter{ctx: ctx, config: source, capability: "retrieval-only", view: newCaptureView(session.host, dir, m)}, store: s}
	return reader.replay(retainedSQLiteRevision{Ref: ref, Revision: v}, emit)
}
