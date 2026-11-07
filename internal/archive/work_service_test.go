package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
	labelrt "github.com/gbdubs/pharos/internal/labeler"
)

// The real adapter path keeps the domain's pending/publication state and
// reaches the same product query that people use, without a global claim
// from selected progress.
func TestWorkServicePipelinesRealSelectedDomains(t *testing.T) {
	c := labeledCatalog(t)
	config := testConfig(t)
	config.CatalogPath, config.WorkEngine, config.BackgroundCPUs = c.Path, "scheduled", 2
	config.Labels = labelsTestSettings(t, "ok")
	config.Labels.Enabled = true
	s := testServer(t, config, c)
	if s.workError != nil {
		t.Fatal(s.workError)
	}
	w := s.work
	w.once.Do(func() {}) // deterministic Pump; no service schedules/collector
	execTx(t, c.DB, `UPDATE messages SET text='Please make this work on tablets too',content_hash='selected-edit' WHERE id='m-a1'`)
	_, request, err := w.submit(t.Context(), workRequestSpec{Kinds: []string{"maintenance.parallelism", "maintenance.labels.query"}, Priority: workPriorityForeground, Explicit: true,
		Build: func(ctx context.Context, b *workInventoryBuilder) error {
			return c.freezeWorkPopulation(ctx, b, time.Time{}, time.Time{}, []string{"c-root"})
		}})
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 10*time.Second, "the selected producer and consumers", func() bool {
		w.runner.Pump()
		select {
		case <-request.done:
			return true
		default:
			return false
		}
	})
	if err := w.wait(t.Context(), request); err != nil {
		t.Fatalf("%v\n%+v", err, w.runner.Inspect())
	}
	for name, pending := range map[string]func(context.Context, []string) ([]string, error){"writing": c.authorshipPendingFor, "timing": c.parallelismPendingFor, "query facts": c.messageLabelFactsPendingFor} {
		if ids, err := pending(t.Context(), []string{"c-root"}); err != nil || len(ids) != 0 {
			t.Fatalf("%s remains pending: %v %v", name, ids, err)
		}
	}
	q := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Rows: true,
		Result: qt.WireQuery{Where: []qt.WhereTerm{{Field: "message_id", Op: "=", Value: "m-a1"}}, Select: []string{"message_id", "label_status"}, Limit: 100}}
	response := apiCall(t, s, config.APIToken, http.MethodPost, "/api/query/message_labels/query-v3", q)
	var result qt.QueryResultV3
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Rows) == 0 {
		t.Fatalf("product query: %d %s", response.Code, response.Body.String())
	}
	for _, row := range result.Rows {
		if row["label_status"] == "unlabeled" {
			t.Fatalf("a covered consumer has missing labels: %+v", row)
		}
	}
	if generation, err := c.metaValue(t.Context(), "labels_generation"); err != nil || generation != "" {
		t.Fatalf("selected progress published global freshness: %q %v", generation, err)
	}
}

type heldServiceTask struct {
	started chan context.Context
	done    atomic.Bool
}

func (task *heldServiceTask) Next(context.Context, *workPopulation) (workSelection, error) {
	if task.done.Load() {
		return workSelection{}, nil
	}
	return workSelection{Units: []workMember{{ID: "unit", Revision: "1"}}, CPUs: 1}, nil
}
func (task *heldServiceTask) RunBatch(ctx context.Context, _ workSelection, _ workAllocation) (workBatchResult, error) {
	task.started <- ctx
	<-ctx.Done()
	return workBatchResult{}, ctx.Err()
}

// A shared executor root belongs to the service. Removing the original
// requester cannot hide it from status, Stop, journaling, metrics or eject.
func TestWorkServiceSharedExecutionOutlivesItsFirstRequester(t *testing.T) {
	c, config := testCatalog(t)
	config.WorkEngine, config.BackgroundCPUs = "scheduled", 1
	s := testServer(t, config, c)
	w := s.work
	w.once.Do(func() {})
	task := &heldServiceTask{started: make(chan context.Context, 1)}
	runner, err := newWorkRunner(s.life.ctx, workSystemClock{}, w.authority, workHooks{Start: w.start, BatchEnded: w.batchEnded, DemandChanged: w.demandChanged, RequestEnded: w.requestEnded, ExecutionEnded: w.executionEnded}, []workDefinition{{Kind: "analysis.labels", Task: task}})
	if err != nil {
		t.Fatal(err)
	}
	w.runner = runner
	firstID, first, err := w.submit(t.Context(), workRequestSpec{Key: "R1", Kinds: []string{"analysis.labels"}, Priority: workPriorityLive})
	if err != nil {
		t.Fatal(err)
	}
	runner.Pump()
	var batchCtx context.Context
	select {
	case batchCtx = <-task.started:
	case <-time.After(5 * time.Second):
		t.Fatal("batch did not start")
	}
	secondID, second, err := w.submit(t.Context(), workRequestSpec{Key: "R2", Kinds: []string{"analysis.labels"}, Priority: workPriorityForeground, Explicit: true})
	if err != nil {
		t.Fatal(err)
	}
	runner.Pump()
	if !runner.Cancel(firstID) {
		t.Fatal("first request was not cancelled")
	}
	if batchCtx.Err() != nil {
		t.Fatal("cancelling R1 cancelled R2's work")
	}
	s.ops.markWritten([]journalImage{imageOf(first.op, first.op.view())})
	var root *Op
	for _, op := range s.ops.running() {
		if op.spec.Kind == "analysis.labels" && op.parent == nil {
			root = op
		}
	}
	if root == nil || !slices.Equal(root.view().NeededBy, []OpID{second.op.ID()}) {
		t.Fatalf("shared root/demand not retained: %+v", s.ops.views())
	}
	for i := 0; i < 205; i++ {
		_, op := s.begin(t.Context(), OperationSpec{Kind: "analysis.chain"})
		op.Finish(nil)
	}
	if s.ops.find(first.op.ID()) != nil || s.ops.find(root.ID()) == nil {
		t.Fatal("request pruning lost the independent execution")
	}
	view := root.view()
	entry := operationEntry(view, nil, time.Now())
	if entry["stoppable"] != true || view.Resources == nil || len(view.Children) != 1 {
		t.Fatalf("shared execution missing control/accounting: %+v", entry)
	}
	image := imageOf(root, view)
	if !strings.Contains(image.detail, string(second.op.ID())) || image.parent != "" {
		t.Fatalf("shared root lost journal identity/references: %+v", image)
	}
	verdict := computeEjectVerdict(verdictInput{ops: s.ops.views()})
	if !slices.ContainsFunc(verdict.Blockers, func(blocker ejectBlocker) bool {
		op := s.ops.find(OpID(blocker.Operation))
		return op != nil && op.root == root
	}) {
		t.Fatalf("eject forgot active shared work: %+v", verdict)
	}
	response := apiCall(t, s, config.APIToken, http.MethodPost, "/api/operations/stop", map[string]any{"id": root.ID()})
	if response.Code != http.StatusOK {
		t.Fatalf("stop: %d %s", response.Code, response.Body.String())
	}
	waitUntil(t, 5*time.Second, "shared workers to drain", func() bool { return opTerminal(root.State()) })
	if root.State() != opCancelled || w.authority.facts().CPUAllocated != 0 {
		t.Fatalf("stop did not drain the grant: %s %+v", root.State(), w.authority.facts())
	}
	if child := s.ops.find(view.Children[0]); child == nil || child.State() != opCancelled || child.view().Resources.Allocation.PeakSlots != 1 {
		t.Fatal("batch outcome or final resource record is incorrect")
	}
	runner.Cancel(secondID)
}

func TestWorkServiceRefusesNewRequestsWhileStopping(t *testing.T) {
	c, config := testCatalog(t)
	config.WorkEngine = "scheduled"
	s := testServer(t, config, c)
	s.beginStop()
	_, _, err := s.work.submit(t.Context(), workRequestSpec{Kinds: []string{"analysis.authorship"}, Priority: workPriorityLive})
	if !errors.Is(err, errServiceStopping) {
		t.Fatalf("shutdown accepted demand: %v", err)
	}
}

func TestWorkServiceLabelPolicyChangeInterruptsOldCoverage(t *testing.T) {
	c, config := testCatalog(t)
	if err := os.WriteFile(config.Path, []byte("labels_enabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config.WorkEngine, config.Labels.Enabled = "scheduled", false
	s := testServer(t, config, c)
	w := s.work
	w.once.Do(func() {})
	w.runner.SetPaused(true)
	firstID, first, err := w.submit(t.Context(), workRequestSpec{Kinds: []string{"maintenance.labels.query"}, Priority: workPriorityLive})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(w.runner.Inspect().Requests[0].Expanded, "analysis.labels") {
		t.Fatal("disabled inference blocked query-fact work")
	}
	if err := s.setLabelsEnabled(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if first.facts.State != opInterrupted || first.facts.ID != firstID {
		t.Fatalf("old input policy was not retired: %+v", first.facts)
	}
	_, _, err = w.submit(t.Context(), workRequestSpec{Kinds: []string{"maintenance.labels.query"}, Priority: workPriorityLive})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(w.runner.Inspect().Requests[0].Expanded, "analysis.labels") {
		t.Fatal("enabling labels did not update the new request's dependency")
	}
	if err := s.setLabelsEnabled(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if len(w.runner.Inspect().Requests) != 0 {
		t.Fatal("disabled producer stranded an old request")
	}
}

// fakeModelSession is a workModelSession whose inventory a test sets.
type fakeModelSession struct {
	alloc       workAllocation
	discovered  bool
	discoveries int
	pending     map[string]bool
	page        []workMember
}

func (f *fakeModelSession) Allocation() workAllocation { return f.alloc }
func (f *fakeModelSession) Discovered() bool           { return f.discovered }
func (f *fakeModelSession) Discover(context.Context) error {
	f.discoveries++
	f.discovered = true
	return nil
}
func (f *fakeModelSession) Close() { f.discovered = false }
func (f *fakeModelSession) Run(context.Context, []string, int) (workDomainStep, error) {
	return workDomainStep{}, nil
}
func (f *fakeModelSession) PendingFor(_ context.Context, ids []string) ([]string, error) {
	pending := []string{}
	for _, id := range ids {
		if f.pending[id] {
			pending = append(pending, id)
		}
	}
	return pending, nil
}
func (f *fakeModelSession) PendingPage(_ context.Context, _ string, limit int, excluded func(workMember) bool) (workDomainPage, error) {
	page := workDomainPage{}
	for _, m := range f.page {
		if len(page.Units) < limit && (excluded == nil || !excluded(m)) {
			page.Units = append(page.Units, m)
		}
	}
	return page, nil
}

func fakeModelTask(t *testing.T, cpus int) (*modelWorkTask, *Catalog, *[]*fakeModelSession) {
	t.Helper()
	c, config := testCatalog(t)
	s := testServer(t, config, c)
	s.background.mu.Lock()
	s.background.power, s.background.powerAt = powerState{Percent: -1}, time.Now().Add(time.Hour)
	s.background.mu.Unlock()
	sessions := &[]*fakeModelSession{}
	task := &modelWorkTask{server: s, authority: newWorkAuthority(cpus), kind: "analysis.labels",
		config: func(context.Context, powerState) modelWorkConfig {
			return modelWorkConfig{settings: LabelerSettings{Threads: 2}, signature: "settings", enabled: true, bulk: true}
		},
		create: func(_ LabelerSettings, allocation workAllocation) (workModelSession, error) {
			session := &fakeModelSession{alloc: allocation, pending: map[string]bool{}}
			*sessions = append(*sessions, session)
			return session, nil
		}}
	return task, c, sessions
}

func runModelPhase(t *testing.T, task *modelWorkTask, sel workSelection) {
	t.Helper()
	if _, err := task.RunBatch(t.Context(), sel, workAllocation{CPUs: sel.CPUs}); err != nil {
		t.Fatal(err)
	}
}

// Discovery is admitted at one CPU and the session keeps its own compute
// envelope (M2). A library page is a coverage claim only for an inventory
// discovered at the current input generation: a mirror or structure change
// that leaves no dirty mark sends the session back to discovery once, not
// on every batch (H1).
func TestModelWorkTaskDiscoversOncePerInputGeneration(t *testing.T) {
	task, c, sessions := fakeModelTask(t, 8)
	library := &workPopulation{}
	sel, err := task.Next(t.Context(), library)
	if err != nil || sel.Phase != "discover" || sel.CPUs != 1 {
		t.Fatalf("first selection %+v %v", sel, err)
	}
	runModelPhase(t, task, sel)
	if len(*sessions) != 1 || (*sessions)[0].alloc.CPUs != 4 {
		t.Fatalf("the session's envelope came from the discovery grant: %+v", *sessions)
	}
	for range 3 {
		sel, err = task.Next(t.Context(), library)
		if err != nil || sel.Phase != "" || len(sel.Units) != 0 || sel.CPUs != 4 {
			t.Fatalf("a current inventory's empty page: %+v %v", sel, err)
		}
	}
	// An ingest moves the record writer's generation without a dirty mark.
	if err := c.writeTransaction(t.Context(), "test ingest", func(tx *sql.Tx) error { return bumpAuthorshipInputs(tx) }); err != nil {
		t.Fatal(err)
	}
	sel, err = task.Next(t.Context(), library)
	if err != nil || sel.Phase != "discover" || sel.CPUs != 1 {
		t.Fatalf("an older inventory claimed coverage: %+v %v", sel, err)
	}
	runModelPhase(t, task, sel)
	for range 3 {
		if sel, err = task.Next(t.Context(), library); err != nil || sel.Phase != "" {
			t.Fatalf("rediscovered without a new generation: %+v %v", sel, err)
		}
	}
	if len(*sessions) != 2 || (*sessions)[1].alloc.CPUs != 4 || (*sessions)[1].discoveries != 1 {
		t.Fatalf("discoveries %+v", *sessions)
	}
	// While work remains, a moved generation is not a reason to rediscover:
	// each step refreshes its own conversations. Running out of it is.
	(*sessions)[1].page = []workMember{{ID: "c-1", Revision: "token"}}
	if err := c.writeTransaction(t.Context(), "test ingest", func(tx *sql.Tx) error { return bumpAuthorshipInputs(tx) }); err != nil {
		t.Fatal(err)
	}
	if sel, err = task.Next(t.Context(), library); err != nil || sel.Phase != "" || len(sel.Units) != 1 {
		t.Fatalf("an ingest rediscovered a draining session: %+v %v", sel, err)
	}
	(*sessions)[1].page = nil
	if sel, err = task.Next(t.Context(), library); err != nil || sel.Phase != "discover" {
		t.Fatalf("an empty page claimed coverage for an older inventory: %+v %v", sel, err)
	}
	runModelPhase(t, task, sel)
	// An idle session keeps its context for a while, then releases it.
	idle := task.session.(*fakeModelSession)
	task.Idle(task.used.Add(workModelSessionIdle / 2))
	if task.session == nil || !idle.discovered {
		t.Fatal("a briefly idle session was closed")
	}
	task.Idle(task.used.Add(workModelSessionIdle))
	if task.session != nil || idle.discovered {
		t.Fatal("an idle session kept its context")
	}
	if sel, err = task.Next(t.Context(), library); err != nil || sel.Phase != "discover" {
		t.Fatalf("after an idle close: %+v %v", sel, err)
	}
	runModelPhase(t, task, sel)
	// A finite selection refreshes its own conversations: no generation check.
	if err := c.writeTransaction(t.Context(), "test ingest", func(tx *sql.Tx) error { return bumpAuthorshipInputs(tx) }); err != nil {
		t.Fatal(err)
	}
	b := &workInventoryBuilder{limit: 1 << 20}
	b.Add("c-1", "r1")
	inv, err := b.freeze(time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if sel, err = task.Next(t.Context(), &workPopulation{inventory: inv}); err != nil || sel.Phase != "" || len(sel.Units) != 1 || sel.CPUs != 4 {
		t.Fatalf("finite selection %+v %v", sel, err)
	}
}

// A finite model batch infers at most modelWorkPending conversations but
// checks its whole window, so a current population costs one batch per
// window (M3).
func TestModelWorkTaskSkipsCurrentConversationsInItsWindow(t *testing.T) {
	task, _, sessions := fakeModelTask(t, 8)
	sel, _ := task.Next(t.Context(), &workPopulation{})
	runModelPhase(t, task, sel)
	session := (*sessions)[0]
	b := &workInventoryBuilder{limit: 1 << 20}
	for i := range 300 {
		b.Add(fmt.Sprintf("c%03d", i), "r")
	}
	for _, id := range []string{"c001", "c002", "c010", "c050", "c051", "c099"} {
		session.pending[id] = true
	}
	inv, err := b.freeze(time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	sel, err = task.Next(t.Context(), &workPopulation{inventory: inv})
	if err != nil || len(sel.Units) != workDomainWindow-2 {
		t.Fatalf("window %d units: %v", len(sel.Units), err)
	}
	inferred := []string{}
	for _, m := range sel.Units {
		if session.pending[m.ID] {
			inferred = append(inferred, m.ID)
		}
	}
	if !slices.Equal(inferred, []string{"c001", "c002", "c010", "c050"}) {
		t.Fatalf("pending in one batch: %v", inferred)
	}
	// Entirely current: about population ÷ window batches.
	clear(session.pending)
	batches := 0
	for after := ""; ; batches++ {
		units := modelWorkWindow((&workPopulation{inventory: inv}).Page(after, workDomainWindow), nil, modelWorkPending)
		if len(units) == 0 {
			break
		}
		after = units[len(units)-1].ID
	}
	if batches != 3 {
		t.Fatalf("a current population of 300 took %d batches", batches)
	}
}

// Failures are keyed by the canonical conversation revision on the finite
// and library paths alike (runner review #11): a unit a library backoff
// holds is held in a finite request at the same revision, and a library
// selection names an excluded unit by that revision, so a changed unit
// retires its old failure instead of hiding behind a domain token. Next
// reads no revision itself; the admitted revisions phase does, and its
// answer lasts no longer than the longest retry delay.
func TestConversationWorkChargesFailuresAtCanonicalRevisions(t *testing.T) {
	c := labeledCatalog(t)
	revisions, err := c.readWorkRevisions(t.Context(), []string{"c-root"})
	if err != nil || revisions["c-root"] == "" {
		t.Fatalf("revision %v %v", revisions, err)
	}
	canonical := revisions["c-root"]
	library := workSelection{Units: []workMember{{ID: "c-root", Revision: "dirty-token"}}, CPUs: 1}
	failedUnits := func(err error) []workMember {
		t.Helper()
		var classified *workError
		if !errors.As(err, &classified) {
			t.Fatalf("unclassified %v", err)
		}
		return classified.units
	}
	_, err = runConversationWork(t.Context(), c, library, workAllocation{CPUs: 1}, func(context.Context, []string, workAllocation) (workDomainStep, error) {
		return workDomainStep{Failed: []string{"c-root"}}, nil
	})
	if units := failedUnits(err); !slices.Equal(units, []workMember{{ID: "c-root", Revision: canonical}}) {
		t.Fatalf("a domain failure was charged at %v", units)
	}
	_, err = runConversationWork(t.Context(), c, library, workAllocation{CPUs: 1}, func(context.Context, []string, workAllocation) (workDomainStep, error) {
		return workDomainStep{}, errors.New("the worker crashed")
	})
	if units := failedUnits(err); !slices.Equal(units, []workMember{{ID: "c-root", Revision: canonical}}) {
		t.Fatalf("a batch error was charged at %v", units)
	}
	// A step that leaves its selection pending and stores nothing made no
	// progress: charged, never counted as work and reselected at once.
	_, err = runConversationWork(t.Context(), c, library, workAllocation{CPUs: 1}, func(context.Context, []string, workAllocation) (workDomainStep, error) {
		return workDomainStep{Remaining: []string{"c-root"}}, nil
	})
	if units := failedUnits(err); !slices.Equal(units, []workMember{{ID: "c-root", Revision: canonical}}) {
		t.Fatalf("a step without progress was charged at %v", units)
	}
	if out, err := runConversationWork(t.Context(), c, library, workAllocation{CPUs: 1}, func(context.Context, []string, workAllocation) (workDomainStep, error) {
		return workDomainStep{Remaining: []string{"c-root"}, Progress: true}, nil
	}); err != nil || out.Work != 1 || !out.Remaining {
		t.Fatalf("a limit-cut step that stored part of its unit: %+v %v", out, err)
	}
	// The model cache being full is capacity, not a failed attempt.
	_, err = runConversationWork(t.Context(), c, library, workAllocation{CPUs: 1}, func(context.Context, []string, workAllocation) (workDomainStep, error) {
		return workDomainStep{}, fmt.Errorf("%w: the shared GPU is in use", labelrt.ErrBudget)
	})
	var classified *workError
	if !errors.As(err, &classified) || classified.blocker.Reason != workBlockedCapacity || len(classified.units) != 0 {
		t.Fatalf("a full model cache: %v", err)
	}

	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	task := &conversationWorkTask{server: &Server{Catalog: c}, kind: "test", cpus: 1, selectSQL: `SELECT 'c-root' id,'dirty-token' revision LIMIT ?`}
	held := &workPopulation{excluded: map[string]string{"c-root": canonical}}
	readRevisions := func(pop *workPopulation) {
		t.Helper()
		sel, err := task.Next(t.Context(), pop)
		if err != nil || sel.Phase != "revisions" || len(sel.Units) != 0 || sel.CPUs != 1 {
			t.Fatalf("an unread failed candidate did not ask for the revisions phase: %+v %v", sel, err)
		}
		if _, err := task.RunBatch(t.Context(), sel, workAllocation{CPUs: 1}); err != nil {
			t.Fatal(err)
		}
	}
	readRevisions(held)
	if sel, err := task.Next(t.Context(), held); err != nil || sel.Phase != "" || len(sel.Units) != 0 {
		t.Fatalf("a library selection repeated a held unit: %+v %v", sel, err)
	}
	now = now.Add(workRetryDelays[len(workRetryDelays)-1])
	readRevisions(held) // read again: the token alone cannot show every change
	changed := &workPopulation{excluded: map[string]string{"c-root": "an older revision"}}
	if sel, err := task.Next(t.Context(), changed); err != nil || !slices.Equal(sel.Units, []workMember{{ID: "c-root", Revision: canonical}}) {
		t.Fatalf("a changed unit's selection: %+v %v", sel, err)
	}
	if sel, err := task.Next(t.Context(), &workPopulation{}); err != nil || !slices.Equal(sel.Units, []workMember{{ID: "c-root", Revision: "dirty-token"}}) {
		t.Fatalf("a selection without failures named a canonical revision: %+v %v", sel, err)
	}
	if len(task.failed.entries) != 0 {
		t.Fatalf("revisions outlived their failures: %v", task.failed.entries)
	}
}

// nextMeteredTask counts what Next reads through the task's resource meter.
type nextMeteredTask struct {
	workTask
	meter *taskResources
}

func (m nextMeteredTask) Next(ctx context.Context, pop *workPopulation) (workSelection, error) {
	return m.workTask.Next(withTaskResources(ctx, m.meter), pop)
}

// A library failure is held under the domain's token without Next hashing
// the conversation, shares its tries with a finite request at the same
// input, and a new input is retried inside the backoff once the admitted
// phase has read its revision.
func TestConversationWorkLibraryFailuresShareTriesAndSeeNewInput(t *testing.T) {
	c := labeledCatalog(t)
	// No stored digest: every revision read hashes the conversation's rows.
	execTx(t, c.DB, `DELETE FROM conversation_derivation_inputs WHERE conversation_id='c-root'`,
		`CREATE TABLE test_dirty(id TEXT PRIMARY KEY, revision TEXT)`, `INSERT INTO test_dirty VALUES('c-root','token-1')`)
	canonical := workRevisionFor(t, c, "c-root")
	const kind = "maintenance.parallelism"
	steps := 0
	task := &conversationWorkTask{server: &Server{Catalog: c}, kind: kind, cpus: 1,
		selectSQL: `SELECT id,revision FROM test_dirty ORDER BY id LIMIT ?`,
		pending:   func(_ context.Context, ids []string) ([]string, error) { return ids, nil },
		step: func(_ context.Context, ids []string, _ workAllocation) (workDomainStep, error) {
			steps++
			return workDomainStep{Failed: ids}, nil
		}}
	meter := &taskResources{}
	h := newWorkHarness(t, 2, workDefinition{Kind: kind, Task: nextMeteredTask{task, meter}})
	c.now = h.clock.Now
	failure := func() workFailure {
		t.Helper()
		if facts := h.task(kind); facts.Failures == 1 {
			return facts.Examples[0]
		}
		t.Fatalf("task %+v", h.task(kind))
		return workFailure{}
	}

	h.submit(workRequestSpec{Kinds: []string{kind}, Priority: workPriorityLive})
	h.runner.Pump()
	h.complete(kind)
	if f := failure(); f.Unit != (workMember{ID: "c-root", Revision: canonical}) || f.Attempts != 1 || steps != 1 {
		t.Fatalf("the library failure %+v after %d steps", f, steps)
	}
	for range 3 {
		if h.runner.Pump() != 0 {
			t.Fatalf("a held unit ran again: %+v", h.jobs)
		}
	}
	finite := h.submit(workRequestSpec{Kinds: []string{kind}, Priority: workPriorityForeground,
		Build: workMembers(workMember{ID: "c-root", Revision: canonical})})
	h.runner.Pump()
	h.build(finite)
	if q := h.request(finite); q.State != opBlocked || q.Blocker.Reason != workBlockedBackoff || len(h.jobs) != 0 {
		t.Fatalf("a finite request at the failed input did not share its backoff: %+v %+v", q, h.jobs)
	}
	h.clock.Advance(workRetryDelays[0])
	h.runner.Pump()
	h.complete(kind)
	if f := failure(); f.Attempts != 2 || steps != 2 {
		t.Fatalf("tries were not shared: %+v after %d steps", f, steps)
	}
	if h.runner.Pump() != 0 {
		t.Fatalf("a held unit ran again: %+v", h.jobs)
	}

	// New input inside the 30-second backoff: a new token and revision.
	execTx(t, c.DB, `UPDATE test_dirty SET revision='token-2'`, `UPDATE conversations SET ended_at='2030-01-01T00:00:00Z' WHERE id='c-root'`)
	changed := workRevisionFor(t, c, "c-root")
	h.runner.Pump()
	if len(h.jobs) != 1 || h.jobs[0].job.Phase != "revisions" || h.jobs[0].job.CPUs != 1 {
		t.Fatalf("a new token did not ask for the admitted revisions phase: %+v", h.jobs)
	}
	h.complete(kind)
	h.complete(kind)
	if f := failure(); f.Unit != (workMember{ID: "c-root", Revision: changed}) || f.Attempts != 1 || steps != 3 {
		t.Fatalf("the new input was not retried at once: %+v after %d steps", f, steps)
	}
	if read := meter.logicalRead.Load(); read != 0 {
		t.Fatalf("Next hashed %d bytes of conversation rows", read)
	}
}

func TestConversationWorkRetriesAreBoundedAcrossSelectionAndInputChanges(t *testing.T) {
	for _, finite := range []bool{false, true} {
		for _, crash := range []bool{false, true} {
			t.Run(fmt.Sprintf("finite=%t/crash=%t", finite, crash), func(t *testing.T) {
				c := labeledCatalog(t)
				canonical := workRevisionFor(t, c, "c-root")
				steps := 0
				task := &conversationWorkTask{server: &Server{Catalog: c}, kind: "maintenance.parallelism", cpus: 1,
					selectSQL: `SELECT 'c-root' id,'domain-token' revision LIMIT ?`,
					pending:   func(_ context.Context, ids []string) ([]string, error) { return ids, nil },
					step: func(_ context.Context, ids []string, _ workAllocation) (workDomainStep, error) {
						steps++
						if crash {
							return workDomainStep{}, errors.New("worker failed")
						}
						return workDomainStep{Failed: ids}, nil
					}}
				h := newWorkHarness(t, 1, workDefinition{Kind: task.kind, Task: task})
				c.now = h.clock.Now
				spec := workRequestSpec{Kinds: []string{task.kind}, Priority: workPriorityLive}
				if finite {
					spec.Build = workMembers(workMember{ID: "c-root", Revision: canonical})
				}
				id := h.submit(spec)
				h.runner.Pump()
				if finite {
					h.build(id)
					// The selected target changes before the worker starts.
					execTx(t, c.DB, `UPDATE conversations SET ended_at='2040-01-01T00:00:00Z' WHERE id='c-root'`)
				}
				for attempt := 0; attempt <= len(workRetryDelays); attempt++ {
					if attempt > 0 {
						h.clock.Advance(workRetryDelays[attempt-1])
					}
					h.runner.Pump()
					// Expired revision-cache entries get their own admitted read.
					for phase := 0; phase < 3; phase++ {
						job := h.take(task.kind, 0)
						job.run(context.Background())
						h.runner.Pump()
						if job.job.Phase != "revisions" {
							break
						}
					}
					if steps != attempt+1 {
						t.Fatalf("attempt %d ran %d domain steps", attempt+1, steps)
					}
					if facts := h.task(task.kind); len(facts.Examples) != 1 || facts.Examples[0].Attempts != attempt+1 {
						t.Fatalf("retry time reset an input's failure count: %+v", facts)
					}
				}
				if request := h.request(id); request.State != opFailed || h.pending(task.kind) {
					t.Fatalf("exhausted input kept its request alive: %+v", request)
				}
			})
		}
	}
}

func TestConversationWorkFiltersHeldConversationsBeforeItsMessageLimit(t *testing.T) {
	override(t, &workDomainWindow, 2)
	c := labeledCatalog(t)
	execTx(t, c.DB, `INSERT INTO conversations(id,workspace_id,provider,account,native_id,started_at) SELECT 'healthy-work',workspace_id,provider,account,'healthy-work','2020-01-01T00:00:00Z' FROM conversations LIMIT 1`,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES('healthy-turn','healthy-work','healthy-turn','user','message','Healthy work','healthy','2020-01-01T00:00:00Z',1)`,
		`WITH RECURSIVE n(v) AS (SELECT 1 UNION ALL SELECT v+1 FROM n WHERE v<3)
 INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order)
 SELECT 'held-turn-'||v,'c-root','held-turn-'||v,'user','message','Held work','held-'||v,'2030-01-01T00:00:00Z',v FROM n`)
	s := &Server{Catalog: c}
	defs, err := s.workDefinitions(newWorkAuthority(1))
	if err != nil {
		t.Fatal(err)
	}
	var writing *conversationWorkTask
	for _, def := range defs {
		if row, ok := lookupOperationKind(def.Kind); !ok || row.Class != workClassPopulation {
			t.Errorf("%s uses a conversation population but has execution class %q", def.Kind, row.Class)
		}
		if def.Kind == "analysis.authorship" {
			writing = def.Task.(*conversationWorkTask)
		}
	}
	writing.preparedAt, err = writing.preparationVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	writing.prepared = true
	canonical := workRevisionFor(t, c, "c-root")
	writing.failed.record(workMember{ID: "c-root", Revision: "native"}, canonical, workRevisionNow(c))
	pop := &workPopulation{excluded: map[string]string{"c-root": canonical}, failures: map[string]bool{"c-root": true}}
	sel, err := writing.Next(t.Context(), pop)
	if err != nil || sel.Phase != "" || len(sel.Units) == 0 || slices.ContainsFunc(sel.Units, func(member workMember) bool { return member.ID == "c-root" }) {
		t.Fatalf("a held conversation hid healthy work behind its messages: %+v %v", sel, err)
	}
}
