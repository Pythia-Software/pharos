package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// The operation journal (docs/background-work.md §6) is the durable record
// of operations, written into the catalog's operation_journal table by one
// writer per process through a low-priority lane: a round at most every 3 s
// that takes the catalog writer gate only if it is free, writes every
// pending row image in one short transaction with a 50 ms busy timeout, and
// otherwise skips. Handles never wait for it, and a failed round is dropped
// and counted, never retried while holding the gate.

// journalRoundInterval spaces the writer's rounds; tests shorten it.
var journalRoundInterval = 3 * time.Second

// journalBusyTimeout bounds how long a round waits for SQLite's lock held by
// another process.
var journalBusyTimeout = 50 * time.Millisecond

// Retention (§6.1): ended rows are kept for journalKeepDays and at most
// journalKeepRows per host, whichever is fewer; children go with their root.
var (
	journalKeepDays = 30
	journalKeepRows = 2000
)

var errJournalBusy = errors.New("operation journal: the catalog writer is busy")

// journalImage is one row as the journal stores it.
type journalImage struct {
	op      *Op
	version uint64
	id, parent, root, kind, variant, subject, trigger, process, instance, state string
	reason, err, stepName, stepUnit, legacy                                    string
	stepIndex, stepCount                                                       int
	stepDone, stepTotal                                                        float64
	stepKnown                                                                  bool
	started, updated, ended                                                    string
	detail                                                                     string
}

// journalReconcile is a conditional update of a row another instance owns
// (§6.2): it applies only while the row is still open.
type journalReconcile struct {
	ID       string
	Instance string
	State    string
	Reason   string
	Error    string
	EndedAt  string
}

type journalWriter struct {
	catalog    *Catalog
	reg        *opRegistry
	roundMu    sync.Mutex
	mu         sync.Mutex
	reconciles map[string]journalReconcile
	prune      bool
	dropped    atomic.Int64
	skipped    atomic.Int64
	loggedAt   time.Time
	// beforeRow runs before each row is written; tests use it to start a
	// competing writer mid-round.
	beforeRow func()
}

// newJournalWriter returns a writer for catalog, or nil for a catalog that
// must not be written (opened for queries only).
func newJournalWriter(catalog *Catalog, reg *opRegistry) *journalWriter {
	if catalog == nil || catalog.queryOnly || reg == nil {
		return nil
	}
	reg.mu.Lock()
	reg.journaled = true
	reg.mu.Unlock()
	return &journalWriter{catalog: catalog, reg: reg, reconciles: map[string]journalReconcile{}}
}

// journalPolicyFor is the kind's policy for spec: "always for user" where the
// row says so (§3.3, analysis.findings).
func journalPolicyFor(kind *operationKind, spec OperationSpec) journalPolicy {
	if kind.Kind == "analysis.findings" && spec.Trigger == triggerUser {
		return journalAlways
	}
	return kind.Journal
}

// journalEligibleLocked reports whether op's row is written: a child follows
// its root, and an if-longer-than root is written once it has run long
// enough or wrote.
func (r *opRegistry) journalEligibleLocked(op *Op) bool {
	root := op.root
	policy := journalPolicyFor(root.kind, root.spec)
	switch policy.Mode {
	case "never":
		return false
	case "longer":
		if root.wrote || root.writtenEver {
			return true
		}
		end := r.clock()
		if opTerminal(root.state) {
			end = root.ended
		}
		return end.Sub(root.started) >= policy.After
	}
	return true
}

// pendingImages builds the row image of every operation changed since its
// last write that its policy lets the journal write.
func (r *opRegistry) pendingImages() []journalImage {
	r.mu.Lock()
	views := []opView{}
	ops := []*Op{}
	for _, op := range r.ops {
		if op.version != op.written && r.journalEligibleLocked(op) {
			views = append(views, op.viewLocked())
			ops = append(ops, op)
		}
	}
	r.mu.Unlock()
	images := make([]journalImage, 0, len(views))
	for index, view := range views {
		images = append(images, imageOf(ops[index], view))
	}
	return images
}

func imageOf(op *Op, view opView) journalImage {
	image := journalImage{op: op, version: view.Version, id: string(view.ID), parent: string(view.Parent), root: string(view.Root), kind: view.Spec.Kind,
		variant: view.Spec.Variant, subject: view.Spec.Subject, trigger: defaultString(view.Spec.Trigger, triggerUser), process: view.Process,
		instance: view.Instance, state: view.State, reason: view.Reason, err: view.Error, legacy: view.Spec.Legacy,
		started: formatTime(view.Started), updated: formatTime(view.Updated)}
	if !view.Ended.IsZero() {
		image.ended = formatTime(view.Ended)
	}
	image.stepIndex, image.stepCount = stepPosition(view)
	image.stepName, image.stepUnit = view.Step.Name, view.Step.Unit
	image.stepDone, image.stepTotal, image.stepKnown = view.Step.Done, view.Step.Total, view.Step.Total >= 0
	detail := map[string]any{"holds": view.Spec.Holds, "affects": view.Spec.Affects, "priority": view.Spec.Priority}
	if len(view.Plan) > 0 {
		detail["plan"] = view.Plan
	}
	if view.Step.Source != "" {
		detail["source"] = view.Step.Source
	}
	if view.Detail != "" {
		detail["detail"] = view.Detail
	}
	if view.DuringRelease {
		detail["during_release"] = true
	}
	if view.Wrote {
		detail["wrote"] = true
	}
	data, _ := json.Marshal(detail)
	image.detail = string(data)
	return image
}

// stepPosition is "step N of M": the forecast's planned steps for a run
// that has one, the declared plan, or the steps begun so far (M unknown, 0).
func stepPosition(view opView) (index, count int) {
	if view.Forecast != nil {
		if estimate := view.Forecast.estimate(time.Now(), false); estimate != nil && estimate.TotalSteps > 0 {
			return estimate.CurrentStep, estimate.TotalSteps
		}
	}
	return view.Step.Index, len(view.Plan)
}

// markWritten records the versions a round wrote; a later change is pending
// again.
func (r *opRegistry) markWritten(images []journalImage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, image := range images {
		image.op.writtenEver = true
		if image.op.written < image.version {
			image.op.written = image.version
		}
	}
	r.pruneLocked()
}

// queueReconcile adds a conditional update for the next round.
func (j *journalWriter) queueReconcile(entry journalReconcile) {
	j.mu.Lock()
	j.reconciles[entry.ID] = entry
	j.mu.Unlock()
}

// requestPrune asks the next round to apply the retention rule.
func (j *journalWriter) requestPrune() {
	j.mu.Lock()
	j.prune = true
	j.mu.Unlock()
}

func (j *journalWriter) pendingReconciles() ([]journalReconcile, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	entries := make([]journalReconcile, 0, len(j.reconciles))
	for _, entry := range j.reconciles {
		entries = append(entries, entry)
	}
	return entries, j.prune
}

func (j *journalWriter) clearReconciles(entries []journalReconcile, pruned bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, entry := range entries {
		if current, ok := j.reconciles[entry.ID]; ok && current == entry {
			delete(j.reconciles, entry.ID)
		}
	}
	if pruned {
		j.prune = false
	}
}

// pending reports whether anything is waiting for a round.
func (j *journalWriter) pending() bool {
	entries, prune := j.pendingReconciles()
	return len(entries) > 0 || prune || len(j.reg.pendingImages()) > 0
}

// round writes everything pending in one transaction. With wait 0 it is the
// low-priority lane: it skips (errJournalBusy) when the writer gate is taken,
// a tracked write is in flight, another process holds SQLite's lock, or a
// writer starts waiting for the gate while it writes. With wait > 0 (the
// final flush) it may wait that long for the gate and the lock.
func (j *journalWriter) round(ctx context.Context, wait time.Duration) error {
	if j == nil {
		return nil
	}
	if wait == 0 {
		if !j.roundMu.TryLock() {
			return errJournalBusy
		}
	} else {
		j.roundMu.Lock()
	}
	defer j.roundMu.Unlock()
	images := j.reg.pendingImages()
	reconciles, prune := j.pendingReconciles()
	if len(images) == 0 && len(reconciles) == 0 && !prune {
		return nil
	}
	err := j.write(ctx, wait, images, reconciles, prune)
	switch {
	case err == nil:
		j.reg.markWritten(images)
		j.clearReconciles(reconciles, prune)
	case errors.Is(err, errJournalBusy):
		j.skipped.Add(1)
	default:
		// Dropped: the images stay pending, so the next round writes the
		// latest of them.
		j.dropped.Add(1)
		j.mu.Lock()
		log := time.Since(j.loggedAt) >= time.Minute
		if log {
			j.loggedAt = time.Now()
		}
		j.mu.Unlock()
		if log {
			fmt.Fprintf(os.Stderr, "Operation journal: a round was dropped (%d so far): %v\n", j.dropped.Load(), err)
		}
	}
	return err
}

func (j *journalWriter) write(ctx context.Context, wait time.Duration, images []journalImage, reconciles []journalReconcile, prune bool) (err error) {
	c := j.catalog
	var release func()
	if wait == 0 {
		var ok bool
		if release, ok = c.tryWriteGate(); !ok {
			return errJournalBusy
		}
		if c.writerActive() {
			release()
			return errJournalBusy
		}
	} else {
		gateCtx, cancel := context.WithTimeout(ctx, wait)
		release, err = c.acquireWriteGate(gateCtx)
		cancel()
		if err != nil {
			return errJournalBusy
		}
	}
	defer release()
	busy := journalBusyTimeout
	if wait > 0 {
		busy = wait
	}
	connCtx, cancel := context.WithTimeout(ctx, busy)
	conn, err := c.DB.Conn(connCtx)
	cancel()
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return errJournalBusy
		}
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout=%d", busy.Milliseconds())); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), fmt.Sprintf("PRAGMA busy_timeout=%d", busyTimeoutMS))
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		if catalogBusy(err) {
			return errJournalBusy
		}
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	yield := func() bool { return wait == 0 && c.writersWaiting() }
	host := currentHost().ID
	pid := os.Getpid()
	for _, image := range images {
		if j.beforeRow != nil {
			j.beforeRow()
		}
		if yield() {
			return errJournalBusy
		}
		var total any
		if image.stepKnown {
			total = image.stepTotal
		}
		var index, count any
		if image.stepIndex > 0 {
			index = image.stepIndex
		}
		if image.stepCount > 0 {
			count = image.stepCount
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO operation_journal(id,parent_id,root_id,kind,variant,subject,trigger,host_id,process,pid,instance,state,reason,error,
 step_index,step_count,step_name,step_unit,step_done,step_total,started_at,updated_at,ended_at,legacy_ref,detail_json)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(id) DO UPDATE SET state=excluded.state,reason=excluded.reason,error=excluded.error,step_index=excluded.step_index,
 step_count=excluded.step_count,step_name=excluded.step_name,step_unit=excluded.step_unit,step_done=excluded.step_done,step_total=excluded.step_total,
 updated_at=excluded.updated_at,ended_at=excluded.ended_at,legacy_ref=excluded.legacy_ref,detail_json=excluded.detail_json
 WHERE operation_journal.instance=excluded.instance`,
			image.id, nilIfEmpty(image.parent), image.root, image.kind, image.variant, image.subject, image.trigger, host, image.process, pid, image.instance,
			image.state, nilIfEmpty(image.reason), nilIfEmpty(image.err), index, count, nilIfEmpty(image.stepName), nilIfEmpty(image.stepUnit),
			image.stepDone, total, image.started, image.updated, nilIfEmpty(image.ended), nilIfEmpty(image.legacy), image.detail); err != nil {
			if catalogBusy(err) {
				return errJournalBusy
			}
			return err
		}
	}
	for _, entry := range reconciles {
		if yield() {
			return errJournalBusy
		}
		// The row and its open children, only while they are still open.
		if _, err := conn.ExecContext(ctx, `UPDATE operation_journal SET state=?,reason=?,error=?,ended_at=?,updated_at=?
 WHERE (id=? OR root_id=?) AND instance=? AND ended_at IS NULL`, entry.State, nilIfEmpty(entry.Reason), nilIfEmpty(entry.Error), entry.EndedAt, now(),
			entry.ID, entry.ID, entry.Instance); err != nil {
			if catalogBusy(err) {
				return errJournalBusy
			}
			return err
		}
	}
	if prune {
		cutoff := formatTime(time.Now().AddDate(0, 0, -journalKeepDays))
		if _, err := conn.ExecContext(ctx, `DELETE FROM operation_journal WHERE host_id=? AND root_id IN (
 SELECT id FROM operation_journal WHERE host_id=? AND parent_id IS NULL AND ended_at IS NOT NULL AND (ended_at<? OR id NOT IN (
  SELECT id FROM operation_journal WHERE host_id=? AND parent_id IS NULL AND ended_at IS NOT NULL ORDER BY started_at DESC LIMIT ?)))`,
			host, host, cutoff, host, journalKeepRows); err != nil {
			if catalogBusy(err) {
				return errJournalBusy
			}
			return err
		}
	}
	if yield() {
		return errJournalBusy
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		if catalogBusy(err) {
			return errJournalBusy
		}
		return err
	}
	committed = true
	return nil
}

// run is the writer's loop: a round every journalRoundInterval until ctx
// ends. The final flush is the stopper's (flush).
func (j *journalWriter) run(ctx context.Context) {
	ticker := time.NewTicker(journalRoundInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		_ = j.round(ctx, 0)
	}
}

// flush runs one final round that may wait up to budget for the gate.
func (j *journalWriter) flush(budget time.Duration) error {
	if j == nil || budget <= 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	return j.round(ctx, budget)
}

// droppedRounds counts dropped rounds, for the snapshot.
func (j *journalWriter) droppedRounds() int64 {
	if j == nil {
		return 0
	}
	return j.dropped.Load()
}

// journalRow is a row read back from the journal.
type journalRow struct {
	ID        string          `json:"id"`
	ParentID  string          `json:"parent_id,omitempty"`
	RootID    string          `json:"root_id"`
	Kind      string          `json:"kind"`
	Variant   string          `json:"variant,omitempty"`
	Subject   string          `json:"subject,omitempty"`
	Trigger   string          `json:"trigger"`
	Process   string          `json:"process"`
	PID       int             `json:"pid"`
	Instance  string          `json:"instance"`
	State     string          `json:"state"`
	Reason    string          `json:"reason,omitempty"`
	Error     string          `json:"error,omitempty"`
	StepIndex int             `json:"step_index,omitempty"`
	StepCount int             `json:"step_count,omitempty"`
	StepName  string          `json:"step_name,omitempty"`
	StepUnit  string          `json:"step_unit,omitempty"`
	StepDone  float64         `json:"step_done"`
	StepTotal *float64        `json:"step_total"`
	StartedAt string          `json:"started_at"`
	UpdatedAt string          `json:"updated_at"`
	EndedAt   string          `json:"ended_at,omitempty"`
	Legacy    string          `json:"legacy_ref,omitempty"`
	Detail    json.RawMessage `json:"detail"`
}

const journalColumns = `id,COALESCE(parent_id,''),root_id,kind,variant,subject,trigger,process,pid,instance,state,COALESCE(reason,''),COALESCE(error,''),
 COALESCE(step_index,0),COALESCE(step_count,0),COALESCE(step_name,''),COALESCE(step_unit,''),COALESCE(step_done,0),step_total,started_at,updated_at,
 COALESCE(ended_at,''),COALESCE(legacy_ref,''),detail_json`

func scanJournalRows(rows *sql.Rows) ([]journalRow, error) {
	defer rows.Close()
	result := []journalRow{}
	for rows.Next() {
		var row journalRow
		var total sql.NullFloat64
		var detail string
		if err := rows.Scan(&row.ID, &row.ParentID, &row.RootID, &row.Kind, &row.Variant, &row.Subject, &row.Trigger, &row.Process, &row.PID, &row.Instance,
			&row.State, &row.Reason, &row.Error, &row.StepIndex, &row.StepCount, &row.StepName, &row.StepUnit, &row.StepDone, &total,
			&row.StartedAt, &row.UpdatedAt, &row.EndedAt, &row.Legacy, &detail); err != nil {
			return nil, err
		}
		if total.Valid {
			row.StepTotal = &total.Float64
		}
		row.Detail = json.RawMessage(defaultString(detail, "{}"))
		result = append(result, row)
	}
	return result, rows.Err()
}

// recentJournal reads this host's newest rows, top level first.
func (c *Catalog) recentJournal(ctx context.Context, limit int) ([]journalRow, error) {
	rows, err := c.reader().QueryContext(ctx, "SELECT "+journalColumns+" FROM operation_journal WHERE host_id=? ORDER BY started_at DESC LIMIT ?", currentHost().ID, limit)
	if err != nil {
		return nil, err
	}
	return scanJournalRows(rows)
}

// openJournal reads this host's open rows that another instance owns.
func (c *Catalog) openJournal(ctx context.Context, exceptInstance string) ([]journalRow, error) {
	rows, err := c.reader().QueryContext(ctx, "SELECT "+journalColumns+" FROM operation_journal WHERE host_id=? AND ended_at IS NULL AND instance<>? ORDER BY started_at",
		currentHost().ID, exceptInstance)
	if err != nil {
		return nil, err
	}
	return scanJournalRows(rows)
}
