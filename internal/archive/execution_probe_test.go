//go:build probe

package archive

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Times the shared execution resolution on a copy of a real catalog and
// reports what it decided. The copy is opened immutable and read-only; no
// Initialize, migration or write runs.
//
//	PHAROS_EXECUTION_COPY='/path/to/copy/catalog/catalog.sqlite3' \
//	  go test -tags probe -run '^TestExecutionResolutionProbe$' -v -timeout 60m ./internal/archive
func TestExecutionResolutionProbe(t *testing.T) {
	path := os.Getenv("PHAROS_EXECUTION_COPY")
	if path == "" {
		t.Skip("set PHAROS_EXECUTION_COPY to a disposable catalog copy")
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(path, "/Pharos/catalog/") || strings.HasPrefix(path, "/Applications/") {
		t.Fatal("probe requires a copy, never the live catalog")
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String()+"?immutable=1&mode=ro&_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	var rows *executionSourceRows
	var e *executionEvidence
	var r *executionResolution
	var o *occurrenceIndex
	probeStep(t, "source rows", func() (err error) { rows, err = fetchExecutionSourceRows(ctx, db, nil, nil); return })
	probeStep(t, "assemble + typed sources", func() (err error) { e, err = rows.evidence(ctx, db, 0); return })
	probeStep(t, "resolve", func() error { r = resolveExecutions(e); return nil })
	probeStep(t, "occurrences (catalog)", func() (err error) { o, err = r.Occurrences(ctx, db, nil); return })
	probeStep(t, "usage attribution", func() (err error) { _, err = usageConversationRootsOf(ctx, db, r, nil); return })
	probeReport(t, e, r, o)
}

// Measures the cached, incremental resolution on a writable disposable copy:
// upgrade (index and trigger installation), cold and warm full loads,
// derived writes, single-transcript commits, sustained ingestion with
// concurrent readers, trigger overhead, and the same after typed evidence is
// backfilled. It writes to the copy; never point it at a library.
//
//	cp -c /path/to/copy/catalog.sqlite3 /path/to/scratch/catalog/catalog.sqlite3   # APFS clone
//	PHAROS_EXECUTION_WRITABLE_COPY='/path/to/scratch/catalog/catalog.sqlite3' \
//	  go test -tags probe -run '^TestExecutionCacheProbe$' -v -timeout 120m ./internal/archive
//
// PHAROS_EXECUTION_BACKFILL=1 also backfills typed evidence on the copy and
// repeats the measurements.
func TestExecutionCacheProbe(t *testing.T) {
	path := os.Getenv("PHAROS_EXECUTION_WRITABLE_COPY")
	if path == "" {
		t.Skip("set PHAROS_EXECUTION_WRITABLE_COPY to a disposable writable catalog copy")
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(path, "/Pharos/catalog/") || strings.HasPrefix(path, "/Applications/") || !strings.Contains(strings.ToLower(path), "writable") {
		t.Fatal("probe writes: it requires a disposable copy whose path says writable, never a library")
	}
	ctx := context.Background()
	var c *Catalog
	probeStep(t, "open + Initialize (upgrade)", func() (err error) { c, err = OpenCatalog(path); return })
	defer c.Close()
	measure := func(label string) {
		t.Logf("--- %s", label)
		c.execution.mu.Lock()
		c.execution.current, c.execution.earlier = nil, nil
		c.execution.mu.Unlock()
		var r *executionResolution
		var o *occurrenceIndex
		probeStep(t, "full resolution", func() (err error) { r, err = c.executionResolution(ctx); return })
		probeStep(t, "catalog occurrences", func() (err error) { _, o, err = c.usageConversationRootsAndOccurrences(ctx, nil); return })
		probeStep(t, "cached resolution", func() (err error) { _, err = c.executionResolution(ctx); return })
		probeStep(t, "cached roots + occurrences", func() (err error) { _, _, err = c.usageConversationRootsAndOccurrences(ctx, nil); return })
		probeReport(t, nil, r, o)
		// Derived writes: accounting and publication must not invalidate.
		probeStep(t, "derived write + resolution", func() error {
			if _, err := c.DB.Exec(`UPDATE agent_sessions SET total_tokens=total_tokens WHERE id=(SELECT id FROM agent_sessions WHERE total_tokens>0 LIMIT 1)`); err != nil {
				return err
			}
			if _, err := c.DB.Exec(`UPDATE agent_sessions SET input_tokens=input_tokens+0, total_tokens=total_tokens+1 WHERE id=(SELECT id FROM agent_sessions WHERE total_tokens>0 LIMIT 1)`); err != nil {
				return err
			}
			got, err := c.executionResolution(ctx)
			if err == nil && got != r {
				return fmt.Errorf("derived write re-resolved")
			}
			return err
		})
		// One commit to the busiest transcript (most children).
		var busy string
		if err := c.DB.QueryRow(`SELECT parent_id FROM conversations WHERE parent_id IS NOT NULL GROUP BY parent_id ORDER BY COUNT(*) DESC LIMIT 1`).Scan(&busy); err != nil {
			t.Fatal(err)
		}
		for i := range 3 {
			probeStep(t, fmt.Sprintf("commit to busiest transcript #%d + resolution + occurrences", i+1), func() error {
				if _, err := c.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES(?,?,?,'assistant','message','probe','probe')`,
					fmt.Sprintf("probe-%s-%d-%d", label, i, time.Now().UnixNano()), busy, fmt.Sprintf("probe-%d-%d", i, time.Now().UnixNano())); err != nil {
					return err
				}
				if _, err := c.executionResolution(ctx); err != nil {
					return err
				}
				_, _, err := c.usageConversationRootsAndOccurrences(ctx, nil)
				return err
			})
		}
		t.Logf("cache stats: %+v", c.execution.stats)
		ingestion(t, c, busy)
	}
	measure("legacy evidence")
	if os.Getenv("PHAROS_EXECUTION_BACKFILL") == "1" {
		var done int
		probeStep(t, "backfill typed evidence", func() (err error) { done, err = c.BackfillExecutionEvidence(ctx, 500); return })
		t.Logf("backfilled %d conversations", done)
		measure("typed evidence current")
	}
}

// ingestion commits small batches to a few transcripts for a while, as a
// running sync does, while readers resolve and aggregate concurrently.
func ingestion(t *testing.T, c *Catalog, busy string) {
	ctx := context.Background()
	var targets []string
	rows, err := queryMaps(c.DB, `SELECT id FROM conversations WHERE parent_id=? ORDER BY id LIMIT 3`, busy)
	if err != nil {
		t.Fatal(err)
	}
	targets = append(targets, busy)
	for _, row := range rows {
		targets = append(targets, firstString(row["id"]))
	}
	const commits, batch = 40, 200
	var writes, reads []time.Duration
	var mu sync.Mutex
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				start := time.Now()
				if _, _, err := c.usageConversationRootsAndOccurrences(ctx, nil); err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				reads = append(reads, time.Since(start))
				mu.Unlock()
			}
		}()
	}
	before := c.execution.stats
	for i := range commits {
		start := time.Now()
		err := c.writeTransaction(ctx, "probe ingestion", func(tx *sql.Tx) error {
			for j := range batch {
				target := targets[(i+j)%len(targets)]
				native := fmt.Sprintf("probe-ingest-%d-%d-%d", time.Now().UnixNano(), i, j)
				if _, err := tx.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES(?,?,?,'assistant','message','probe','probe')`,
					"m-"+native, target, native); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		writes = append(writes, time.Since(start))
		time.Sleep(100 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
	after := c.execution.stats
	t.Logf("ingestion: %d commits × %d messages to %d transcripts with 3 readers", commits, batch, len(targets))
	t.Logf("  commit latency p50=%v p95=%v max=%v", percentile(writes, 50), percentile(writes, 95), percentile(writes, 100))
	t.Logf("  reader latency (roots + catalog occurrences) n=%d p50=%v p95=%v max=%v", len(reads), percentile(reads, 50), percentile(reads, 95), percentile(reads, 100))
	t.Logf("  incremental loads=%d reloaded conversations=%d full loads=%d", after.incrementalLoads-before.incrementalLoads,
		after.reloadedConversations-before.reloadedConversations, after.fullLoads-before.fullLoads)
	// Trigger overhead: the same batch with the execution triggers removed.
	var with, without time.Duration
	insert := func() time.Duration {
		start := time.Now()
		if err := c.writeTransaction(ctx, "probe trigger overhead", func(tx *sql.Tx) error {
			for j := range 2000 {
				native := fmt.Sprintf("probe-overhead-%d-%d", time.Now().UnixNano(), j)
				if _, err := tx.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES(?,?,?,'assistant','message','probe','probe')`,
					"m-"+native, targets[j%len(targets)], native); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return time.Since(start)
	}
	with = insert()
	if _, err := c.DB.Exec(`DROP TRIGGER execution_source_messages_insert`); err != nil {
		t.Fatal(err)
	}
	without = insert()
	if err := c.ensureExecutionSourceGeneration(); err != nil {
		t.Fatal(err)
	}
	t.Logf("  2000-message commit: %v with execution triggers, %v without", with, without)
}

func percentile(values []time.Duration, p int) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	i := (len(sorted) - 1) * p / 100
	return sorted[i].Round(time.Millisecond)
}

func probeStep(t *testing.T, name string, run func() error) {
	t.Helper()
	start := time.Now()
	if err := run(); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	t.Logf("%-58s %v", name, time.Since(start).Round(time.Millisecond))
}

func probeReport(t *testing.T, e *executionEvidence, r *executionResolution, o *occurrenceIndex) {
	t.Helper()
	transcripts := 0
	states := map[parentState]int{}
	for _, x := range r.Executions {
		if x.Conversation != "" {
			transcripts++
		}
		states[x.Parent.State]++
	}
	attempts := map[attemptOutcome]int{}
	for _, a := range r.Attempts {
		attempts[a.Outcome]++
	}
	if e != nil {
		t.Logf("streams=%d claims=%d", len(e.Streams), len(e.Claims))
	}
	t.Logf("executions=%d (transcript-backed %d) attempts=%v conflicts=%d missing=%v", len(r.Executions), transcripts, attempts, len(r.Conflicts), r.Coverage.Missing)
	t.Logf("parent states=%v", states)
	if o != nil {
		t.Logf("classified occurrences=%d unresolved=%d", len(o.Classified()), o.Unresolved())
	}
}

// Times each phase of one incremental reload after a commit to the busiest
// transcript, on a disposable writable copy (see TestExecutionCacheProbe).
func TestExecutionIncrementalPhases(t *testing.T) {
	path := os.Getenv("PHAROS_EXECUTION_WRITABLE_COPY")
	if path == "" || !strings.Contains(strings.ToLower(path), "writable") {
		t.Skip("set PHAROS_EXECUTION_WRITABLE_COPY to a disposable writable catalog copy")
	}
	ctx := context.Background()
	c, err := OpenCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, _, err := c.usageConversationRootsAndOccurrences(ctx, nil); err != nil {
		t.Fatal(err)
	}
	var busy string
	if err := c.DB.QueryRow(`SELECT parent_id FROM conversations WHERE parent_id IS NOT NULL GROUP BY parent_id ORDER BY COUNT(*) DESC LIMIT 1`).Scan(&busy); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if _, err := c.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES(?,?,?,'assistant','message','probe','probe')`,
			fmt.Sprintf("phase-%d-%d", i, time.Now().UnixNano()), busy, fmt.Sprintf("phase-%d-%d", i, time.Now().UnixNano())); err != nil {
			t.Fatal(err)
		}
		tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		previous := c.execution.current
		var changed, below []string
		var fresh, copies *executionSourceRows
		var e *executionEvidence
		var r *executionResolution
		probeStep(t, "changed since", func() (err error) { changed, err = executionChangedSince(ctx, tx, previous.generation); return })
		probeStep(t, fmt.Sprintf("fetch %d changed", len(changed)), func() (err error) { fresh, err = fetchExecutionSourceRows(ctx, tx, changed, []string{}); return })
		probeStep(t, "descendants", func() error { below = executionDescendants(changed, previous.rows, fresh); return nil })
		// The copied-launch reload is narrowed to changed copy inputs; the
		// occurrence reload still widens to every descendant.
		probeStep(t, fmt.Sprintf("fetch copies of %d descendants", len(below)), func() (err error) { copies, err = fetchExecutionSourceRows(ctx, tx, []string{}, below); return })
		fresh.copies = copies.copies
		var rows *executionSourceRows
		probeStep(t, "merge", func() error { rows = previous.rows.merge(fresh, changed, below); return nil })
		probeStep(t, "assemble", func() error { e = rows.assemble(); return nil })
		for i, source := range executionClaimSources {
			probeStep(t, fmt.Sprintf("claim source %d", i), func() error { return source(ctx, tx, e) })
		}
		probeStep(t, "resolve", func() error { r = resolveExecutions(e); return nil })
		_ = r
		probeStep(t, fmt.Sprintf("legacy occurrences of %d descendants", len(below)), func() (err error) { _, err = legacyOccurrenceClaims(ctx, tx, below); return })
		probeStep(t, "coverage of descendants", func() (err error) { _, err = occurrenceCoverageClaims(ctx, tx, below); return })
		for _, source := range occurrenceClaimSources {
			probeStep(t, "typed occurrences of descendants", func() (err error) { _, err = source(ctx, tx, below); return })
		}
		probeStep(t, "snapshot (cache path)", func() (err error) { _, err = c.executionSnapshotAt(ctx, tx); return })
		probeStep(t, "lineage (cache path)", func() (err error) { _, err = c.usageConversationRoots(t.Context(), nil); return })
		var cvs, sessions []map[string]any
		probeStep(t, "lineage conversation rows", func() (err error) {
			cvs, err = queryMapsContext(ctx, tx, `SELECT c.id,c.workspace_id,c.provider,
		COALESCE(NULLIF(c.model,''),a.model) model,COALESCE(NULLIF(c.effort,''),a.effort) effort,
		w.title root_title,r.display_name root_repository_name
		FROM conversations c LEFT JOIN agent_sessions a ON a.conversation_id=c.id AND a.native_id='main'
		LEFT JOIN workspaces w ON w.id=c.workspace_id LEFT JOIN repositories r ON r.id=w.repository_id ORDER BY c.id`)
			return
		})
		probeStep(t, "lineage session rows", func() (err error) {
			sessions, err = queryMapsContext(ctx, tx, `SELECT id,native_id,kind,conversation_id FROM agent_sessions ORDER BY id`)
			return
		})
		s, _ := c.executionSnapshotAt(ctx, tx)
		probeStep(t, "lineage projection", func() error { usageAttributionFrom(s.resolution, cvs, sessions, nil); return nil })
		probeStep(t, "roots + occurrences (cache path)", func() (err error) { _, _, err = c.usageConversationRootsAndOccurrences(ctx, nil); return })
		tx.Rollback()
	}
}

// Compares the legacy ancestry event-ID rule with the typed occurrence
// claims on a backfilled writable copy: which legacy copies typed evidence
// does not reproduce, by provider and rule.
func TestExecutionLegacyOccurrenceOverlap(t *testing.T) {
	path := os.Getenv("PHAROS_EXECUTION_WRITABLE_COPY")
	if path == "" || !strings.Contains(strings.ToLower(path), "writable") {
		t.Skip("set PHAROS_EXECUTION_WRITABLE_COPY to a disposable writable catalog copy")
	}
	ctx := context.Background()
	c, err := OpenCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var legacy, typed []executionClaim
	probeStep(t, "legacy occurrence claims (all)", func() (err error) { legacy, err = legacyOccurrenceClaims(ctx, c.reader(), nil); return })
	for _, source := range occurrenceClaimSources {
		probeStep(t, "typed occurrence claims (all)", func() error {
			more, err := source(ctx, c.reader(), nil)
			typed = append(typed, more...)
			return err
		})
	}
	var coverage []executionClaim
	probeStep(t, "coverage claims (all)", func() (err error) { coverage, err = occurrenceCoverageClaims(ctx, c.reader(), nil); return })
	bySubject := map[sourceRef]executionClaim{}
	rules := map[string]int{}
	for _, claim := range typed {
		bySubject[claim.Subject] = claim
		rules[string(claim.Kind)+"/"+claim.Rule]++
	}
	missing := map[string]int{}
	for _, claim := range legacy {
		if other, ok := bySubject[claim.Subject]; !ok || other.Kind != claimInheritedCopy || other.Object != claim.Object {
			missing[claim.Namespace]++
		}
	}
	t.Logf("legacy=%d typed=%d coverage=%d typed rules=%v legacy not reproduced by namespace=%v", len(legacy), len(typed), len(coverage), rules, missing)
}

// Reports how the shared resolution reads the originally reported family on
// an immutable copy: the parent and child transcripts, the launch, the
// child's main session and the copied launch.
//
//	PHAROS_EXECUTION_COPY='/path/to/copy/catalog/catalog.sqlite3' \
//	  go test -tags probe -run '^TestExecutionReportedFamilyProbe$' -v -timeout 60m ./internal/archive
func TestExecutionReportedFamilyProbe(t *testing.T) {
	path := os.Getenv("PHAROS_EXECUTION_COPY")
	if path == "" {
		t.Skip("set PHAROS_EXECUTION_COPY to a catalog copy")
	}
	if strings.Contains(path, "/Pharos/catalog/") || strings.HasPrefix(path, "/Applications/") {
		t.Fatal("probe requires a copy, never the live catalog")
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String()+"?immutable=1&mode=ro&_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	const (
		parentWork, parentCV = "workspace_90c27e87037c5486a5dfbd69735170e6", "conversation_be73f80bfaed51fa959ffac4a310d8a3"
		childWork, childCV   = "workspace_dfcacb2105495136a1461fe4dfa212d8", "conversation_256f3d6bd4bf512f9c09b125ac572a47"
	)
	sessions := []string{"agent-session_530bb3c387f056c284e494a539447e4a", "agent-session_987a6e2a83945a458c2f767adb7623aa", "agent-session_986929c9cdb052eabe0223c9c50a21a5"}
	dump := func(label, query string, args ...any) {
		rows, err := queryMapsContext(ctx, db, query, args...)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			t.Logf("%s %v", label, row)
		}
	}
	dump("conversation", `SELECT id,workspace_id,provider,account,native_id,parent_id,agent_depth,agent_path,agent_nickname,started_at FROM conversations WHERE id IN (?,?)`, parentCV, childCV)
	dump("session", `SELECT a.id,a.conversation_id,a.native_id,a.parent_id,a.child_conversation_id,a.delegation_message_id,a.kind,a.total_tokens,
 (SELECT COUNT(*) FROM agent_session_messages am WHERE am.agent_session_id=a.id) members,m.call_id,m.source_order,length(m.text) launch_len
 FROM agent_sessions a LEFT JOIN messages m ON m.id=a.delegation_message_id WHERE a.id IN (?,?,?)`, sessions[0], sessions[1], sessions[2])
	dump("origin", `SELECT conversation_id,evidence_key,parent_native_id,parent_provider,parent_account FROM origin_observations WHERE conversation_id IN (?,?) AND parent_native_id IS NOT NULL`, parentCV, childCV)
	if present, _ := executionEvidenceTablesPresent(ctx, db); present {
		dump("typed", `SELECT conversation_id,version,input FROM execution_evidence_state WHERE conversation_id IN (?,?)`, parentCV, childCV)
	}
	var r *executionResolution
	probeStep(t, "resolve", func() (err error) { r, err = resolveExecutionsFrom(ctx, db); return })
	t.Logf("coverage missing=%v", r.Coverage.Missing)
	for _, id := range sessions {
		b := r.BindSession(id)
		t.Logf("bind session %s: state=%s execution=%s", id, b.State, b.ExecutionID)
		if a := r.Attempts[sourceRef{sourceSession, id}]; a != nil {
			t.Logf("  attempt outcome=%s", a.Outcome)
		}
	}
	for _, cid := range []string{parentCV, childCV} {
		b := r.BindConversation(cid)
		t.Logf("bind conversation %s: state=%s execution=%s", cid, b.State, b.ExecutionID)
	}
	child := r.BindConversation(childCV).ExecutionID
	holding := 0
	for _, x := range r.Executions {
		if slices.Contains(x.Segments, childCV) || x.Conversation == childCV {
			holding++
		}
	}
	t.Logf("executions holding the child transcript: %d", holding)
	if x := r.Executions[child]; x != nil {
		a := r.Ancestry(child)
		t.Logf("child: parent state=%s parent=%s certainty=%s label=%q order=%+v streams=%v segments=%v evidence=%s", x.Parent.State, x.Parent.ParentID,
			x.Parent.Certainty, x.Label.Text, x.Order, x.Streams, x.Segments, x.Evidence)
		t.Logf("child ancestry: known=%v depth=%d root=%s chain=%v", a.Known, a.Depth, a.Root, a.Chain)
		for _, candidate := range x.Parent.Candidates {
			t.Logf("  candidate parent=%s reason=%s claim=%s/%s %s", candidate.Parent, candidate.Reason, candidate.Claim.Kind, candidate.Claim.Rule, candidate.Claim.Strength)
		}
		if a.Root != "" {
			family, complete := r.Family(a.Root, 50)
			t.Logf("family from %s (complete=%v): %v", a.Root, complete, family)
		}
	}
	// Reader selection: lineage with either workspace retained.
	for _, kept := range []string{parentWork, childWork} {
		lineage, err := usageConversationRootsOf(ctx, db, r, []map[string]any{{"workspace_id": kept}})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("reader %s: child root=%v run=%q launch run=%q depth=%v parent=%q", kept, lineage.roots[childCV]["id"], lineage.runs[childCV],
			lineage.delegatedRuns[sessions[0]], lineage.delegatedDepth[sessions[0]], lineage.delegatedParents[sessions[0]])
	}
	// Spawn occurrences and copied self activity, scoped as an explorer reads.
	var o *occurrenceIndex
	probeStep(t, "scoped occurrences", func() (err error) { o, err = r.Occurrences(ctx, db, []string{parentCV, childCV}); return })
	t.Logf("partial parent=%q child=%q", o.Partial(parentCV), o.Partial(childCV))
	launches, err := queryMapsContext(ctx, db, `SELECT a.id session,a.conversation_id,a.delegation_message_id message FROM agent_sessions a WHERE a.id IN (?,?) AND a.delegation_message_id IS NOT NULL`, sessions[0], sessions[2])
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range launches {
		ref := sourceRef{sourceMessage, firstString(row["message"])}
		occ := o.Lookup(ref, sourceRef{sourceSession, firstString(row["session"])})
		t.Logf("spawn message %s in %s (session %s): role=%s owner=%s navigate=%s", ref.ID, firstString(row["conversation_id"]), firstString(row["session"]), occ.Role, occ.Owner, occ.Navigate)
	}
	for _, cid := range []string{parentCV, childCV} {
		messages, err := queryMapsContext(ctx, db, `SELECT m.id,(SELECT am.agent_session_id FROM agent_session_messages am WHERE am.message_id=m.id LIMIT 1) session FROM messages m WHERE m.conversation_id=?`, cid)
		if err != nil {
			t.Fatal(err)
		}
		roles := map[occurrenceRole]int{}
		owners := map[executionID]int{}
		for _, row := range messages {
			stream := sourceRef{sourceConversation, cid}
			if session := firstString(row["session"]); session != "" {
				stream = sourceRef{sourceSession, session}
			}
			occ := o.Lookup(sourceRef{sourceMessage, firstString(row["id"])}, stream)
			roles[occ.Role]++
			owners[occ.Owner]++
		}
		t.Logf("messages in %s: %d roles=%v owners=%v", cid, len(messages), roles, owners)
	}
}
