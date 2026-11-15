//go:build probe

package archive

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Times a rules change on a writable copy of a real catalog: what
// propagateExecutionChanges reads before it takes the writer, what its
// reconciliation marks, and how long it holds the writer. It WRITES the copy
// (marks, recorded results, meta), so give it a disposable clone.
//
//	PHAROS_EXECUTION_WRITABLE_COPY='/path/to/scratch/catalog/catalog.sqlite3' \
//	  go test -tags probe -run '^TestExecutionResultsProbe$' -v -timeout 60m ./internal/archive
//
// PHAROS_EXECUTION_RULES='execution-vN/execution-evidence-vN' stands in a
// rules version for this executable's, to measure a bump that changes no
// result. PHAROS_EXECUTION_RESULTS='/path/out.tsv' writes each conversation's
// component digest and that component's result (sorted), to compare what two
// builds resolve; with PHAROS_EXECUTION_PLAN_ONLY=1 nothing is written to
// the catalog after it opens.
func TestExecutionResultsProbe(t *testing.T) {
	path := os.Getenv("PHAROS_EXECUTION_WRITABLE_COPY")
	if path == "" {
		t.Skip("set PHAROS_EXECUTION_WRITABLE_COPY to a disposable writable catalog copy")
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(path, "/Pharos/catalog/") || strings.Contains(path, "Pharos Backup") || strings.HasPrefix(path, "/Applications/") {
		t.Fatal("probe requires a copy, never the live catalog or its backup")
	}
	if rules := os.Getenv("PHAROS_EXECUTION_RULES"); rules != "" {
		executionRulesVersion = rules
	}
	ctx := context.Background()
	var c *Catalog
	evidenceProbeStep(t, "open + Initialize", func() (err error) { c, err = OpenCatalog(path); return })
	defer c.Close()
	state, err := executionPropagationState(ctx, c.reader())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rules %q; recorded %q; results recorded under %q (%s); consistent=%v comparable=%v; generation %d, marked through %d",
		executionDependentsVersions(), state.recorded, state.stamped, state.encoding, state.consistent(), state.comparable(), state.generation, state.through)
	ledgers, err := executionLedgersPresent(ctx, c.reader())
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, ledger := range ledgers {
		names = append(names, ledger[0])
	}
	counts := func(label string) {
		t.Helper()
		for _, ledger := range ledgers {
			var built, dirty, stamps int
			if err := c.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM `+ledger[0]+`),(SELECT COUNT(*) FROM `+ledger[1]+`)`).Scan(&built, &dirty); err != nil {
				t.Fatal(err)
			}
			_ = c.DB.QueryRow(`SELECT COUNT(*) FROM execution_result_stamps`).Scan(&stamps)
			t.Logf("%s: %s built %d, marked %d; results recorded %d", label, ledger[0], built, dirty, stamps)
		}
	}
	counts("before")

	var digests map[string]string
	evidenceProbeStep(t, "catalog-wide digests (committed snapshot)", func() (err error) { digests, err = executionDependencyDigests(ctx, c.reader()); return })
	evidenceProbeStep(t, "results of held components (cached resolution + occurrences)", func() error { return c.primeExecutionResults(ctx, state) })
	var plan *executionReconcilePlan
	evidenceProbeStep(t, "plan (what the reconciliation marks)", func() (err error) {
		tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return err
		}
		defer tx.Rollback()
		plan, err = planExecutionReconcile(ctx, committedReads{tx}, names, state)
		return err
	})
	for _, name := range names {
		t.Logf("plan: %s to mark %d", name, len(plan.marks[name]))
	}
	if out := os.Getenv("PHAROS_EXECUTION_RESULTS"); out != "" {
		evidenceProbeStep(t, "write every component's result", func() error { return probeWriteResults(ctx, c, digests, out) })
	}
	if os.Getenv("PHAROS_EXECUTION_PLAN_ONLY") == "1" {
		return
	}
	c.primeDependencyInverse(ctx)
	resolved := 0
	executionResultResolveHook = func() { resolved++ }
	defer func() { executionResultResolveHook = nil }()
	var held time.Duration
	evidenceProbeStep(t, "reconcile (write transaction, queueing included)", func() error {
		return c.writeTransaction(ctx, "propagate execution evidence changes", func(tx *sql.Tx) error {
			started := time.Now()
			defer func() { held = time.Since(started) }()
			return markExecutionDependentsIn(ctx, tx, true)
		})
	})
	t.Logf("writer held %v; results were resolved %d more times", held.Round(time.Millisecond), resolved)
	counts("after")
	evidenceProbeStep(t, "propagateExecutionChanges again (nothing to do)", func() error { return c.propagateExecutionChanges(ctx) })
	var usage syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &usage)
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	t.Logf("peak RSS %d MB; heap in use %d MB; CPU user %.1fs system %.1fs", usage.Maxrss>>20, memory.HeapInuse>>20,
		float64(usage.Utime.Sec)+float64(usage.Utime.Usec)/1e6, float64(usage.Stime.Sec)+float64(usage.Stime.Usec)/1e6)
}

// probeWriteResults writes conversation, component digest and the
// component's result under this executable's rules, one line each.
func probeWriteResults(ctx context.Context, c *Catalog, digests map[string]string, path string) error {
	tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	snapshot, err := c.executionSnapshotAt(ctx, tx)
	if err != nil {
		return err
	}
	occurrences, err := snapshot.catalogOccurrences(ctx, tx)
	if err != nil {
		return err
	}
	if digests, err = executionDependencyDigests(ctx, committedReads{tx}); err != nil {
		return err
	}
	want := map[string]bool{}
	for _, digest := range digests {
		want[digest] = true
	}
	results := executionComponentResults(digests, snapshot.resolution, occurrences, want)
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	w := bufio.NewWriter(file)
	ids := make([]string, 0, len(digests))
	for id := range digests {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		fmt.Fprintf(w, "%s\t%s\t%s\n", id, digests[id], results[digests[id]])
	}
	return w.Flush()
}
