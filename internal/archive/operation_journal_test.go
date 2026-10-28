package archive

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// journalRows reads the journal's rows for this host by kind.
func journalRows(t *testing.T, catalog *Catalog) map[string]journalRow {
	t.Helper()
	rows, err := catalog.recentJournal(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	byKind := map[string]journalRow{}
	for _, row := range rows {
		byKind[row.Kind] = row
	}
	return byKind
}

func testJournal(t *testing.T) (*Catalog, *opRegistry, *journalWriter) {
	t.Helper()
	catalog, _ := testCatalog(t)
	reg := newOpRegistry("service")
	return catalog, reg, newJournalWriter(catalog, reg)
}

// Starts and ends are written; step changes between rounds collapse into one
// write; an if-longer-than kind is written only once it has run that long
// or wrote, and not at all if it ends sooner.
func TestJournalWritesStartsAndEndsAndCoalescesSteps(t *testing.T) {
	catalog, reg, journal := testJournal(t)
	clock := time.Now()
	reg.now = func() time.Time { return clock }
	_, index := reg.begin(context.Background(), OperationSpec{Kind: "index.captures", Trigger: triggerUser, Legacy: "run-1"})
	_, quick := reg.begin(context.Background(), OperationSpec{Kind: "maintenance.search.text", Trigger: triggerVersion})
	_, slow := reg.begin(context.Background(), OperationSpec{Kind: "maintenance.search.tools", Trigger: triggerVersion})
	_, wrote := reg.begin(context.Background(), OperationSpec{Kind: "maintenance.checkpoint", Trigger: triggerSchedule})
	wrote.Wrote()
	if err := journal.round(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	rows := journalRows(t, catalog)
	if row := rows["index.captures"]; row.State != opRunning || row.Legacy != "run-1" || row.Process != "service" || row.Instance != reg.instance || row.EndedAt != "" {
		t.Fatalf("start row: %+v", row)
	}
	if _, ok := rows["maintenance.search.text"]; ok {
		t.Fatal("an if-longer-than operation was written before it ran long enough")
	}
	if _, ok := rows["maintenance.checkpoint"]; !ok {
		t.Fatal("an operation that wrote was not journaled")
	}
	index.Step("Index captured inputs", "conversation", 100)
	for range 100 {
		index.Advance(1)
	}
	quick.Finish(nil)
	clock = clock.Add(31 * time.Second)
	index.Finish(nil)
	if err := journal.round(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	rows = journalRows(t, catalog)
	row := rows["index.captures"]
	if row.State != opSucceeded || row.EndedAt == "" || row.StepName != "Index captured inputs" || row.StepDone != 100 || row.StepTotal == nil || *row.StepTotal != 100 {
		t.Fatalf("end row: %+v", row)
	}
	if _, ok := rows["maintenance.search.text"]; ok {
		t.Fatal("an if-longer-than operation that ended sooner was written")
	}
	if row := rows["maintenance.search.tools"]; row.State != opRunning {
		t.Fatalf("an if-longer-than operation past its threshold: %+v", row)
	}
	if count := countRows(t, catalog, "SELECT COUNT(*) FROM operation_journal"); count != 3 {
		t.Fatalf("%d rows; each operation has one", count)
	}
	slow.Finish(nil)

	// A daemon pass past its threshold is not written while tentative, so
	// one discarded later leaves no open row.
	_, pass := reg.beginTentative(context.Background(), OperationSpec{Kind: "maintenance.search.query", Trigger: triggerVersion})
	clock = clock.Add(time.Minute)
	if err := journal.round(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	pass.discard()
	if _, ok := journalRows(t, catalog)["maintenance.search.query"]; ok {
		t.Fatal("a tentative pass was journaled")
	}
}

// While the journal cannot write, ended operations wait in memory for it,
// up to a cap; past it the oldest are forgotten and counted as dropped.
func TestUnwrittenOperationsAreCappedWhileTheJournalFails(t *testing.T) {
	catalog, reg, journal := testJournal(t)
	override(t, &maxUnwrittenOperations, 3)
	reg.keepEnded = 0
	if _, err := catalog.DB.Exec("DROP TABLE operation_journal"); err != nil {
		t.Fatal(err)
	}
	for range 10 {
		_, op := reg.begin(context.Background(), OperationSpec{Kind: "backup.local", Trigger: triggerUser})
		op.Finish(nil)
	}
	_ = journal.round(context.Background(), 0)
	reg.begin(context.Background(), OperationSpec{Kind: "backup.local", Trigger: triggerUser})
	if views := reg.views(); len(views) > 4 {
		t.Fatalf("%d operations kept; the cap is 3 ended plus the running one", len(views))
	}
	if dropped := journal.droppedRounds(); dropped < 7 {
		t.Fatalf("dropped %d; forgotten operations count", dropped)
	}
}

// A round that fails is dropped and counted, and never fails or blocks the
// operation it describes; the next round writes the latest state.
func TestJournalFailureNeverFailsTheOperation(t *testing.T) {
	catalog, reg, journal := testJournal(t)
	// A catalog opened for queries (MCP's) gets no writer at all.
	if newJournalWriter(&Catalog{queryOnly: true}, newOpRegistry("mcp")) != nil {
		t.Fatal("a query-only catalog has a journal writer")
	}
	if _, err := catalog.DB.Exec("ALTER TABLE operation_journal RENAME TO operation_journal_away"); err != nil {
		t.Fatal(err)
	}
	_, op := reg.begin(context.Background(), OperationSpec{Kind: "backup.local", Trigger: triggerUser})
	if err := journal.round(context.Background(), 0); err == nil || errors.Is(err, errJournalBusy) {
		t.Fatalf("round on a catalog without the table: %v", err)
	}
	op.Step("Copy the catalog", "byte", 10)
	op.Advance(10)
	op.Finish(nil)
	if op.State() != opSucceeded || journal.droppedRounds() != 1 {
		t.Fatalf("state %q, dropped %d", op.State(), journal.droppedRounds())
	}
	if _, err := catalog.DB.Exec("ALTER TABLE operation_journal_away RENAME TO operation_journal"); err != nil {
		t.Fatal(err)
	}
	if err := journal.round(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if row := journalRows(t, catalog)["backup.local"]; row.State != opSucceeded || row.StepDone != 10 {
		t.Fatalf("after the drop: %+v", row)
	}
}

// The journal never queues ahead of real work (§6.2): with the writer gate
// taken it skips its round at once; a writer that starts waiting while it
// writes makes it give the gate back; another process holding SQLite's lock
// makes it skip within its 50 ms busy timeout.
func TestJournalSkipsItsRoundForCatalogWriters(t *testing.T) {
	catalog, reg, journal := testJournal(t)
	for range 3 {
		reg.begin(context.Background(), OperationSpec{Kind: "backup.local", Trigger: triggerUser})
	}
	release, err := catalog.acquireWriteGate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := journal.round(context.Background(), 0); !errors.Is(err, errJournalBusy) {
		t.Fatalf("round with the gate taken: %v", err)
	}
	if waited := time.Since(started); waited > 200*time.Millisecond {
		t.Fatalf("the round waited %s for the gate", waited)
	}
	release()

	// An ingest-like writer arrives while the round writes its rows.
	acquired := make(chan time.Duration, 1)
	journal.beforeRow = func() {
		if journal.beforeRow == nil {
			return
		}
		journal.beforeRow = nil
		go func() {
			began := time.Now()
			release, err := catalog.acquireWriteGate(context.Background())
			if err == nil {
				acquired <- time.Since(began)
				release()
			}
		}()
		for !catalog.writersWaiting() {
			time.Sleep(time.Millisecond)
		}
	}
	if err := journal.round(context.Background(), 0); !errors.Is(err, errJournalBusy) {
		t.Fatalf("round with a writer waiting: %v", err)
	}
	if waited := <-acquired; waited > time.Second {
		t.Fatalf("the writer waited %s behind the journal", waited)
	}
	if count := countRows(t, catalog, "SELECT COUNT(*) FROM operation_journal"); count != 0 {
		t.Fatalf("a round that yielded wrote %d rows", count)
	}

	// Another process's write transaction.
	other, err := sql.Open("sqlite", catalogDSN(catalog.Path))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	conn, err := other.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	// It does not wait for that lock (busy timeout 0), so it never holds the
	// writer gate while another process writes, and gives it back at once.
	journal.beforeRow = nil
	started = time.Now()
	if err := journal.round(context.Background(), 0); !errors.Is(err, errJournalBusy) {
		t.Fatalf("round while another connection writes: %v", err)
	}
	if waited := time.Since(started); waited > 100*time.Millisecond {
		t.Fatalf("the round waited %s for another process's lock", waited)
	}
	if release, ok := catalog.tryWriteGate(); !ok {
		t.Fatal("a skipped round kept the writer gate")
	} else {
		release()
	}
	if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if err := journal.round(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if count := countRows(t, catalog, "SELECT COUNT(*) FROM operation_journal"); count != 3 {
		t.Fatalf("%d rows once the catalog was free", count)
	}
	if journal.droppedRounds() != 0 {
		t.Fatal("a busy catalog counted as a dropped round")
	}
}

// Pruning keeps open rows and, of ended ones, the newest per host within
// the limits, with children going with their root; other Macs' rows are
// left alone.
func TestJournalPrunesThisMacsOldRows(t *testing.T) {
	useHost(t, "host-a")
	catalog, reg, journal := testJournal(t)
	override(t, &journalKeepRows, 2)
	old := formatTime(time.Now().AddDate(0, 0, -40))
	recent := func(minutes int) string { return formatTime(time.Now().Add(-time.Duration(minutes) * time.Minute)) }
	insert := func(id, parent, root, host, started, ended string) {
		t.Helper()
		if _, err := catalog.DB.Exec(`INSERT INTO operation_journal(id,parent_id,root_id,kind,trigger,host_id,process,pid,instance,state,started_at,updated_at,ended_at)
 VALUES(?,?,?,'backup.local','user',?,'service',1,'old',?,?,?,?)`, id, nilIfEmpty(parent), root, host, map[bool]string{true: "running", false: "succeeded"}[ended == ""], started, started, nilIfEmpty(ended)); err != nil {
			t.Fatal(err)
		}
	}
	insert("expired", "", "expired", "host-a", old, old)
	insert("expired-child", "expired", "expired", "host-a", old, old)
	insert("third", "", "third", "host-a", recent(30), recent(29))
	insert("second", "", "second", "host-a", recent(20), recent(19))
	insert("first", "", "first", "host-a", recent(10), recent(9))
	insert("open", "", "open", "host-a", old, "")
	insert("elsewhere", "", "elsewhere", "host-b", old, old)
	_ = reg
	journal.requestPrune()
	if err := journal.round(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	rows, err := queryMaps(catalog.DB, "SELECT id FROM operation_journal ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, row := range rows {
		got = append(got, firstString(row["id"]))
	}
	if want := []string{"elsewhere", "first", "open", "second"}; !equalStrings(got, want) {
		t.Fatalf("after pruning %v, want %v", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The service writes its journal's last round after its work has stopped
// and before it closes the catalog, including the release itself.
func TestServiceStopFlushesTheJournalBeforeClosingTheCatalog(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	_, op := server.begin(context.Background(), OperationSpec{Kind: "library.upgrade", Trigger: triggerUser})
	op.Step("Rebuild the Tools rollup", "", -1)
	op.Finish(nil)
	if err := server.stop(stopTimeout); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenCatalog(catalog.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	rows := journalRows(t, reopened)
	if row := rows["library.upgrade"]; row.State != opSucceeded || row.StepName != "Rebuild the Tools rollup" {
		t.Fatalf("the upgrade's row: %+v", row)
	}
	if row := rows["library.release"]; row.State != opSucceeded {
		t.Fatalf("the release's row: %+v", row)
	}
	if budget := flushBudget(time.Now(), stopTimeout, time.Now().Add(stopTimeout-300*time.Millisecond)); budget > 300*time.Millisecond || budget <= 0 {
		t.Fatalf("budget near the end of a release: %s", budget)
	}
	if budget := flushBudget(time.Now(), stopTimeout, time.Now().Add(stopTimeout+time.Second)); budget != time.Second {
		t.Fatalf("budget after a release timed out: %s", budget)
	}
}

// A restarted service ends what its previous instance left open, and what
// processes that died left open, as interrupted (or with the outcome they
// recorded in their lease); live owners and other Macs' rows are untouched,
// and a row its owner ended meanwhile is never overwritten.
func TestReconcileEndsRowsWhoseOwnerIsGone(t *testing.T) {
	useHost(t, "host-a")
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	dirs := leaseDirs(config)
	insert := func(id, instance, host, process string) {
		t.Helper()
		if _, err := catalog.DB.Exec(`INSERT INTO operation_journal(id,root_id,kind,trigger,host_id,process,pid,instance,state,started_at,updated_at)
 VALUES(?,?,'index.captures','user',?,?,1,?,'running',?,?)`, id, id, host, process, instance, now(), now()); err != nil {
			t.Fatal(err)
		}
	}
	insert("previous", "previous-service", "host-a", "service")
	insert("other-mac", "previous-service", "host-b", "service")
	live, err := acquireProcessLease(dirs[0], leaseInfo{Instance: "live-cli", HostID: "host-a", Process: "cli:index", Kinds: []string{"index.captures"}, StartedAt: now()})
	if err != nil {
		t.Fatal(err)
	}
	defer live.release()
	insert("alive", "live-cli", "host-a", "cli:index")
	dead, err := acquireProcessLease(dirs[0], leaseInfo{Instance: "dead-cli", HostID: "host-a", Process: "cli:index", Kinds: []string{"index.captures"}, StartedAt: now()})
	if err != nil {
		t.Fatal(err)
	}
	// A direct MCP call's lease names no host, and another Mac's lease
	// names its own: both left behind by processes that died.
	for _, info := range []leaseInfo{{Instance: "dead-mcp", Process: "cli:mcp", Kinds: []string{"maintenance.mcp-history"}, StartedAt: now(), volatile: true},
		{Instance: "dead-other-mac", HostID: "host-b", Process: "cli:index", Kinds: []string{"index.captures"}, StartedAt: now()}} {
		lease, err := acquireProcessLease(dirs[0], info)
		if err != nil {
			t.Fatal(err)
		}
		if err := lease.abandon(nil); err != nil {
			t.Fatal(err)
		}
	}
	insert("recorded", "dead-cli", "host-a", "cli:index")
	insert("unrecorded", "dead-cli", "host-a", "cli:index")
	if err := dead.abandon(map[string]leaseOutcome{"recorded": {State: opFailed, Error: "disk full", EndedAt: now()}}); err != nil {
		t.Fatal(err)
	}
	// A pass whose round cannot write (the gate is busy) keeps the dead
	// lease, and does not leave it claimed: a probe still sees it dead.
	gate, err := catalog.acquireWriteGate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if server.reconcileOperations(context.Background()) {
		t.Fatal("a pass wrote with the gate taken")
	}
	gate()
	if probe := probeLease(filepath.Join(dirs[0], "dead-cli.json")); probe.Live || probe.Info == nil {
		t.Fatalf("the dead lease after a pass that could not write: %+v", probe)
	}
	if !server.reconcileOperations(context.Background()) {
		t.Fatal("the reconcile pass did not write")
	}
	states := map[string]string{}
	rows, err := queryMaps(catalog.DB, "SELECT id,state,COALESCE(reason,'') reason,COALESCE(error,'') error FROM operation_journal")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		states[firstString(row["id"])] = firstString(row["state"]) + "/" + firstString(row["reason"]) + firstString(row["error"])
	}
	want := map[string]string{"previous": "interrupted/owner-exited", "other-mac": "running/", "alive": "running/", "recorded": "failed/disk full", "unrecorded": "interrupted/owner-exited"}
	for id, state := range want {
		if states[id] != state {
			t.Errorf("%s: %q, want %q", id, states[id], state)
		}
	}
	if _, err := os.Stat(filepath.Join(dirs[0], "dead-cli.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the dead lease was kept after its outcomes were written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dirs[0], "live-cli.json")); err != nil {
		t.Fatalf("the live lease was touched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dirs[0], "dead-mcp.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a dead lease without a host was kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dirs[0], "dead-other-mac.json")); err != nil {
		t.Fatalf("another Mac's dead lease was touched: %v", err)
	}
	// A reconcile is conditional: it never overwrites an ended row.
	server.journal.queueReconcile(journalReconcile{ID: "previous", Instance: "previous-service", State: opFailed, EndedAt: now()})
	if err := server.journal.round(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := catalog.DB.QueryRow("SELECT state FROM operation_journal WHERE id='previous'").Scan(&state); err != nil || state != opInterrupted {
		t.Fatalf("an ended row was overwritten: %q %v", state, err)
	}
}

// A writer that keeps the gate saturated cannot starve the journal: after
// journalStarveRounds skipped rounds one queues for the gate in its normal
// order and writes the starts and ends only.
func TestJournalIsNotStarvedByASaturatedGate(t *testing.T) {
	catalog, reg, journal := testJournal(t)
	override(t, &journalStarveRounds, 3)
	_, running := reg.begin(context.Background(), OperationSpec{Kind: "backup.local", Trigger: triggerUser})
	if err := journal.round(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	running.Step("Copy captures", "byte", 100) // a progress change only
	_, started := reg.begin(context.Background(), OperationSpec{Kind: "library.upgrade", Trigger: triggerUser})
	_, ended := reg.begin(context.Background(), OperationSpec{Kind: "tools.ledger.build", Trigger: triggerUser})
	ended.Finish(nil)
	// Two writers hand the gate to each other, so it is never free.
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	saturated := make(chan struct{})
	for range 2 {
		go func() {
			for ctx.Err() == nil {
				release, err := catalog.acquireWriteGate(ctx)
				if err != nil {
					return
				}
				select {
				case <-saturated:
				default:
					close(saturated)
				}
				time.Sleep(2 * time.Millisecond)
				release()
			}
		}()
	}
	<-saturated
	journal.requestPrune()
	for range journalStarveRounds - 1 {
		if err := journal.round(context.Background(), 0); !errors.Is(err, errJournalBusy) {
			t.Fatalf("a round with the gate saturated: %v", err)
		}
	}
	if err := journal.round(context.Background(), 0); err != nil {
		t.Fatalf("the starved round: %v", err)
	}
	if _, prune := journal.pendingReconciles(); !prune {
		t.Fatal("a priority round pruned")
	}
	stop()
	rows := journalRows(t, catalog)
	if rows["library.upgrade"].State != opRunning || rows["tools.ledger.build"].State != opSucceeded {
		t.Fatalf("starts and ends after starvation: %+v", rows)
	}
	if rows["backup.local"].StepName != "" {
		t.Fatalf("a starved round wrote a progress change: %+v", rows["backup.local"])
	}
	// Once the gate is free again, the next round is a normal one: the
	// progress-only change lands, the prune runs, and nothing is starved.
	waitUntil(t, 5*time.Second, "the saturating writers to stop", func() bool {
		release, ok := catalog.tryWriteGate()
		if ok {
			release()
		}
		return ok
	})
	if err := journal.round(context.Background(), 0); err != nil {
		t.Fatalf("the first round after saturation: %v", err)
	}
	if row := journalRows(t, catalog)["backup.local"]; row.StepName != "Copy captures" {
		t.Fatalf("progress after saturation: %+v", row)
	}
	if _, prune := journal.pendingReconciles(); prune || journal.starved.Load() != 0 {
		t.Fatalf("after saturation: prune pending %v, starved %d", prune, journal.starved.Load())
	}
	started.Finish(nil)
}

// How long a round holds the writer gate on this Mac's disk: logged for the
// report and the contract, and bounded by the round's deadline.
func TestJournalGateHoldIsShort(t *testing.T) {
	t.Run("test durability", func(t *testing.T) { measureJournalGateHold(t) })
	t.Run("service durability", func(t *testing.T) {
		useDurableCatalog(t) // the service's fsync settings
		measureJournalGateHold(t)
	})
}

func measureJournalGateHold(t *testing.T) {
	catalog, config := testCatalog(t)
	catalog.Close()
	catalog = reopenCatalog(t, config.CatalogPath)
	t.Cleanup(func() { catalog.Close() })
	reg := newOpRegistry("service")
	journal := newJournalWriter(catalog, reg)
	ops := []*Op{}
	for range 20 {
		_, op := reg.begin(context.Background(), OperationSpec{Kind: "index.captures", Trigger: triggerUser})
		op.Step("Index captured inputs", "conversation", 1000)
		ops = append(ops, op)
	}
	for round := range 200 {
		for _, op := range ops {
			op.Advance(1)
		}
		if err := journal.round(context.Background(), 0); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
	}
	holds := journal.gateHolds()
	p50, p95, worst := holds[len(holds)/2], holds[len(holds)*95/100], holds[len(holds)-1]
	t.Logf("gate held per round (%d rounds, 20 rows each): p50 %s, p95 %s, max %s", len(holds), p50, p95, worst)
	if p95 > journalRoundDeadline {
		t.Fatalf("p95 %s exceeds the round deadline %s", p95, journalRoundDeadline)
	}
	for _, op := range ops {
		op.Finish(nil)
	}
}
