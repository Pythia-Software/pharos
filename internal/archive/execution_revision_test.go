package archive

import (
	"context"
	"database/sql"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func sourceGeneration(t *testing.T, c *Catalog) int64 {
	t.Helper()
	g, tracked, err := executionSourceGeneration(t.Context(), c.DB)
	if err != nil || !tracked {
		t.Fatalf("source generation: %v tracked=%v", err, tracked)
	}
	return g
}

func TestExecutionSourceGenerationFollowsSourcesNotDerivatives(t *testing.T) {
	c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "child", depth: 1})
	fixtureLaunch(t, c, "launch", "root", "s-root", "", "", 0)
	fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES('note','child','note','assistant','message','x','x')`)
	// Derived publication and accounting writes leave decisions alone.
	before := sourceGeneration(t, c)
	for _, statement := range []string{
		`UPDATE agent_sessions SET total_tokens=total_tokens+5,input_tokens=input_tokens+5 WHERE id='s-root'`,
		`UPDATE messages SET raw_text='raw' WHERE id='note'`,
		`UPDATE conversations SET origin_host_id='host' WHERE id='child'`,
		`INSERT INTO parallelism_activity(conversation_id,turns,model_spans) VALUES('child',x'',x'')`,
		`UPDATE workspaces SET activity_at='2026-02-01' WHERE id='work'`,
	} {
		fixtureExec(t, c, statement)
	}
	if after := sourceGeneration(t, c); after != before {
		t.Fatalf("derived writes advanced the source generation: %d → %d", before, after)
	}
	var triggered []string
	rows, err := queryMaps(c.DB, `SELECT DISTINCT tbl_name FROM sqlite_schema WHERE type='trigger' AND name GLOB 'execution_source_*' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		triggered = append(triggered, firstString(row["tbl_name"]))
	}
	for _, derived := range []string{"message_labels", "message_labels_dirty", "agent_events", "agent_event_messages", "parallelism_activity", "parallelism_dirty", "conversation_origins"} {
		if slices.Contains(triggered, derived) {
			t.Fatalf("derived table %s invalidates execution decisions", derived)
		}
	}
	// Every source input, including ones another transcript holds, records
	// the conversation that owns it.
	changes := func(statement string, args ...any) []string {
		t.Helper()
		g := sourceGeneration(t, c)
		fixtureExec(t, c, statement, args...)
		if sourceGeneration(t, c) <= g {
			t.Fatalf("%s did not advance the source generation", statement)
		}
		changed, err := executionChangedSince(t.Context(), c.DB, g)
		if err != nil {
			t.Fatal(err)
		}
		return changed
	}
	for _, step := range []struct {
		statement string
		want      []string
	}{
		{`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,call_id,content_hash) VALUES('ack','root','ack','tool','delegation_result','{"agent_id":"child"}','call-launch','a')`, []string{"root"}},
		{`UPDATE messages SET text='{"agent_id":"other"}' WHERE id='ack'`, []string{"root"}},
		{`DELETE FROM messages WHERE id='ack'`, []string{"root"}},
		{`INSERT INTO origin_observations(conversation_id,evidence_key,priority,evidence,parent_native_id) VALUES('child','k',1,'test','root')`, []string{"child"}},
		{`UPDATE origin_observations SET evidence_key='k2' WHERE conversation_id='child'`, []string{"child"}},
		{`UPDATE messages SET evidence_locator='/r/x.jsonl:9' WHERE id='note'`, []string{"child"}},
		{`UPDATE agent_sessions SET total_tokens=0 WHERE id='s-root'`, []string{"root"}},
		{`UPDATE conversations SET parent_id='root' WHERE id='child'`, []string{"child"}},
		{`INSERT INTO agent_session_messages(agent_session_id,message_id,source_order) VALUES('launch','note',0)`, []string{"root"}},
		{`INSERT INTO conversation_identity_links(left_id,right_id,relationship,confidence,evidence_json) VALUES('root','child','mirror',1,'{}')`, []string{"child", "root"}},
		// Every conversation in the workspace reads its source kind.
		{`UPDATE workspaces SET source_kind='claude' WHERE id='work'`, []string{"child", "root"}},
		{`UPDATE workspaces SET title='A useful source rename' WHERE id='work'`, []string{"child", "root"}},
	} {
		if got := changes(step.statement); !reflect.DeepEqual(got, step.want) {
			t.Fatalf("%s recorded %v, want %v", step.statement, got, step.want)
		}
	}
}

func TestExecutionResolutionCacheIsIncrementalAndConsistent(t *testing.T) {
	c := executionFixture(t, executionFixtureConversation{id: "root", workspace: "root-work"}, executionFixtureConversation{id: "child", workspace: "child-work", depth: 1},
		executionFixtureConversation{id: "other"}, executionFixtureConversation{id: "fork", parent: "child", depth: 2})
	fixtureLaunch(t, c, "launch", "root", "s-root", "", "", 0)
	uncached := func() map[string]string {
		t.Helper()
		return executionSummary(fixtureResolution(t, c))
	}
	first, err := c.executionResolution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := c.executionResolution(t.Context()); again != first || c.execution.stats.fullLoads != 1 {
		t.Fatal("an unchanged generation must reuse the snapshot")
	}
	// A derived write keeps the same resolution.
	fixtureExec(t, c, `UPDATE agent_sessions SET total_tokens=total_tokens+1 WHERE id='s-root'`)
	if again, _ := c.executionResolution(t.Context()); again != first {
		t.Fatal("accounting writes must not re-resolve")
	}
	// An ordinary message is a source change whose rows leave the evidence
	// as it was: the decisions and their revision stand.
	fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES('chat','root','chat','assistant','message','hello','c')`)
	if again, _ := c.executionResolution(t.Context()); again != first || c.execution.stats.reused != 1 || c.execution.stats.incrementalLoads != 1 {
		t.Fatalf("an append that changes no evidence re-resolved: %+v", c.execution.stats)
	}
	// A transaction opened before a late acknowledgement keeps its own view.
	old, err := c.reader().BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer old.Rollback()
	if _, _, err := executionSourceGeneration(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,call_id,content_hash) VALUES('ack','root','ack','tool','delegation_result','{"agent_id":"child"}','call-launch','a')`)
	late, err := c.executionResolution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if late.Executions["conversation:child"].Parent.ParentID != "conversation:root" || !reflect.DeepEqual(executionSummary(late), uncached()) {
		t.Fatalf("incremental resolution differs from a full one: %v", executionSummary(late))
	}
	if c.execution.stats.incrementalLoads != 2 || c.execution.stats.fullLoads != 1 || c.execution.stats.reloadedConversations > 2 {
		t.Fatalf("a commit to one transcript reloads that transcript: %+v", c.execution.stats)
	}
	stale, err := c.executionResolutionAt(t.Context(), old)
	if err != nil {
		t.Fatal(err)
	}
	if stale != first || c.execution.stats.fullLoads != 1 {
		t.Fatal("an older transaction must see the kept resolution of its own rows")
	}
	// A transaction between two kept snapshots is brought forward from the
	// earlier one, not resolved whole.
	between, err := c.reader().BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer between.Rollback()
	if _, _, err := executionSourceGeneration(t.Context(), between); err != nil {
		t.Fatal(err)
	}
	fixtureExec(t, c, `UPDATE conversations SET agent_nickname='renamed' WHERE id='other'`)
	fixtureExec(t, c, `UPDATE conversations SET agent_nickname='again' WHERE id='other'`)
	if _, err := c.executionResolution(t.Context()); err != nil {
		t.Fatal(err)
	}
	c.execution.mu.Lock()
	c.execution.earlier = slices.DeleteFunc(c.execution.earlier, func(s *executionSnapshot) bool { return s.resolution == late })
	c.execution.mu.Unlock()
	middle, err := c.executionResolutionAt(t.Context(), between)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := resolveExecutionsFrom(t.Context(), between); !reflect.DeepEqual(executionSummary(middle), executionSummary(want)) || c.execution.stats.fullLoads != 1 {
		t.Fatalf("an older transaction resolved whole or differently: %+v", c.execution.stats)
	}
	between.Rollback()
	// Moving a transcript, deleting evidence and adding a conversation, each
	// incrementally, still equal a full resolution.
	for _, statement := range []string{
		`UPDATE conversations SET parent_id='other' WHERE id='fork'`,
		`DELETE FROM messages WHERE id='ack'`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth) VALUES('late','root-work','codex','local','late','root',1)`,
		`INSERT INTO conversation_identity_links(left_id,right_id,relationship,confidence,evidence_json) VALUES('child','other','mirror',1,'{}')`,
	} {
		fixtureExec(t, c, statement)
		got, err := c.executionResolution(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(executionSummary(got), uncached()) {
			t.Fatalf("after %s the incremental resolution differs", statement)
		}
	}
	// A fork's copy of its ancestor's launch follows the ancestor's launch
	// call, not only the fork's own rows; other appends leave it alone.
	fixtureLaunch(t, c, "original", "other", "s-other", "", "", 0)
	fixtureLaunch(t, c, "copy", "fork", "s-fork", "", "", 0)
	fixtureExec(t, c, `UPDATE agent_sessions SET native_id='call-original' WHERE id='copy';
		UPDATE messages SET call_id='call-original',text=(SELECT text FROM messages WHERE id='m-original') WHERE id='m-copy'`)
	for _, statement := range []string{
		"",
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES('note','other','note','assistant','message','more','n')`,
		`UPDATE messages SET text='{"tool":"spawn_agent","input":{"task_name":"edited"}}' WHERE id='m-original'`,
		`UPDATE messages SET text=(SELECT text FROM messages WHERE id='m-copy') WHERE id='m-original'`,
	} {
		if statement != "" {
			fixtureExec(t, c, statement)
		}
		before := c.execution.stats.reloadedConversations
		got, err := c.executionResolution(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		want := uncached()
		if !reflect.DeepEqual(executionSummary(got), want) {
			t.Fatalf("after %q the incremental copied launches differ: %v", statement, executionSummary(got))
		}
		if strings.HasPrefix(statement, "INSERT") && c.execution.stats.reloadedConversations-before != 1 {
			t.Fatalf("an append reloaded descendants' copies: %+v", c.execution.stats)
		}
		if copied := want["bind:session:copy"]; !strings.Contains(statement, "edited") != strings.HasPrefix(copied, string(bindingInherited)) {
			t.Fatalf("after %q copy binding %s", statement, copied)
		}
	}
}

func TestExecutionResolutionCacheUnderConcurrentWrites(t *testing.T) {
	c := executionFixture(t, executionFixtureConversation{id: "root"})
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 20 {
			id := "agent-" + string(rune('a'+i))
			if _, err := c.DB.Exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth) VALUES(?,'work','codex','local',?,'root',1)`, id, id); err != nil {
				errs <- err
				return
			}
		}
	}()
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				tx, err := c.reader().BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
				if err != nil {
					errs <- err
					return
				}
				r, err := c.executionResolutionAt(context.Background(), tx)
				if err == nil {
					var want *executionResolution
					if want, err = resolveExecutionsFrom(context.Background(), tx); err == nil && !reflect.DeepEqual(executionSummary(r), executionSummary(want)) {
						t.Error("a reader saw a resolution of other rows than its own")
					}
				}
				tx.Rollback()
				if err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	final, err := c.executionResolution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(final.Children("conversation:root")) != 20 {
		t.Fatalf("children: %d", len(final.Children("conversation:root")))
	}
}

func TestCatalogOccurrencesFollowChangedConversations(t *testing.T) {
	c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "fork", parent: "root", depth: 1},
		executionFixtureConversation{id: "unrelated"})
	insert := func(id, cv, native string) {
		fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES(?,?,?,'user','message','hello','h')`, id, cv, native)
	}
	insert("fork-copy", "fork", "evt_1")
	global := func() *occurrenceIndex {
		t.Helper()
		_, o, err := c.usageConversationRootsAndOccurrences(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	full := func() *occurrenceIndex {
		t.Helper()
		o, err := fixtureResolution(t, c).Occurrences(t.Context(), c.DB, nil)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	same := func(step string) {
		t.Helper()
		got, want := global(), full()
		if !slices.Equal(got.Classified(), want.Classified()) || got.Unresolved() != want.Unresolved() {
			t.Fatalf("%s: catalog occurrences %v, full %v", step, got.Classified(), want.Classified())
		}
		for _, ref := range want.Classified() {
			if g, w := got.Lookup(ref, sourceRef{}), want.Lookup(ref, sourceRef{}); g.Role != w.Role || g.Owner != w.Owner || g.Navigate != w.Navigate {
				t.Fatalf("%s: %s is %+v, want %+v", step, ref, g, w)
			}
		}
		// The scoped lookup an explorer or timeline makes for one transcript
		// agrees with the catalog-wide index on it.
		r, err := c.executionResolution(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, cid := range []string{"root", "fork", "unrelated"} {
			scoped, err := r.Occurrences(t.Context(), c.DB, []string{cid})
			if err != nil {
				t.Fatal(err)
			}
			if scoped.Partial(cid) != got.Partial(cid) {
				t.Fatalf("%s: %s partial %q scoped, %q global", step, cid, scoped.Partial(cid), got.Partial(cid))
			}
			messages, err := queryMaps(c.DB, `SELECT id FROM messages WHERE conversation_id=?`, cid)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range messages {
				ref := sourceRef{sourceMessage, firstString(row["id"])}
				if g, w := scoped.Lookup(ref, sourceRef{}), got.Lookup(ref, sourceRef{}); g.Role != w.Role || g.Owner != w.Owner || g.Navigate != w.Navigate {
					t.Fatalf("%s: %s scoped %+v, global %+v", step, ref, g, w)
				}
			}
		}
	}
	same("initial")
	// The original arrives later, in the ancestor only: the descendant's copy
	// becomes inherited without the fork itself changing.
	insert("root-original", "root", "evt_1")
	same("late original")
	if global().Lookup(sourceRef{sourceMessage, "fork-copy"}, sourceRef{}).Role != occurrenceInherited {
		t.Fatal("the copy of a late original is inherited")
	}
	// An unrelated conversation changing reuses the claims; moving the fork
	// away from its ancestry drops them.
	insert("other", "unrelated", "evt_2")
	same("unrelated change")
	fixtureExec(t, c, `UPDATE conversations SET parent_id=NULL,agent_depth=0 WHERE id='fork'`)
	same("moved")
	fixtureExec(t, c, `DELETE FROM messages WHERE id='root-original'`)
	same("deleted original")
}

func TestExecutionCacheFollowsBackfillButNotPublication(t *testing.T) {
	c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "child", parent: "root", depth: 1})
	fixtureLaunch(t, c, "launch", "root", "s-root", "", `{"agent_id":"child"}`, 0)
	fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES('work','child','work','assistant','message','x','x','2026-01-01T00:00:30Z')`)
	warm, err := c.executionResolution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Derived publication: parallelism activity for every transcript.
	fixtureExec(t, c, `INSERT INTO parallelism_dirty SELECT id,'probe' FROM conversations WHERE true ON CONFLICT(conversation_id) DO NOTHING`)
	if _, err := c.refreshParallelism(t.Context(), 100, true); err != nil {
		t.Fatal(err)
	}
	if again, _ := c.executionResolution(t.Context()); again != warm {
		t.Fatal("derived publication re-resolved the catalog")
	}
	// Backfill writes only typed evidence, which must reach the next reader.
	if _, err := c.BackfillExecutionEvidence(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	tx, err := c.reader().BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	after, err := c.executionResolutionAt(t.Context(), tx)
	if err != nil {
		t.Fatal(err)
	}
	full, err := resolveExecutionsFrom(t.Context(), tx)
	if err != nil {
		t.Fatal(err)
	}
	if after == warm || after.Revision.Generation == warm.Revision.Generation || !reflect.DeepEqual(executionSummary(after), executionSummary(full)) {
		t.Fatal("backfilled evidence did not reach the cached resolution")
	}
	if slices.Contains(after.Coverage.Missing, missingTypedEvidence) {
		t.Fatalf("coverage still names untyped evidence: %v", after.Coverage.Missing)
	}
}

func TestParallelismDirtiesEveryDependentTranscript(t *testing.T) {
	c := executionFixture(t, executionFixtureConversation{id: "grand"}, executionFixtureConversation{id: "parent", parent: "grand", depth: 1},
		executionFixtureConversation{id: "grandchild", parent: "parent", depth: 2}, executionFixtureConversation{id: "fork"}, executionFixtureConversation{id: "other"})
	// fork declares grand as its copied-history source without a parent.
	fixtureExec(t, c, `INSERT INTO execution_claims(conversation_id,claim_key,source,kind,subject,object,origin,rule) VALUES('fork','k','record',?,'conversation','native:codex|local|grand','native','codex-session-meta/v1')`, evidenceHistoryCopy)
	tx, err := c.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dirtyParallelismDependents(t.Context(), tx, "grand"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rows, err := queryMaps(c.DB, `SELECT conversation_id FROM parallelism_dirty ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	var dirty []string
	for _, row := range rows {
		dirty = append(dirty, firstString(row["conversation_id"]))
	}
	if !slices.Equal(dirty, []string{"fork", "grandchild", "parent"}) {
		t.Fatalf("dirtied %v", dirty)
	}
}

func TestPartialOccurrenceCoverageReachesActivityAndParallelism(t *testing.T) {
	c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "fork", parent: "root", depth: 1})
	now := c.clock()
	at := now.Add(-time.Hour).UTC().Format(time.RFC3339)
	fixtureExec(t, c, `UPDATE conversations SET started_at=?,ended_at=?`, at, at)
	for _, cv := range []string{"root", "fork"} {
		fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES(?,?,'line-1','user','message','hi','h',?)`, "m-"+cv, cv, at)
		fixtureExec(t, c, `INSERT INTO message_authorship(message_id,conversation_id,workspace_id,sent_at,day,total_chars,spans_json) VALUES(?,?,'work',?,?,2,'[]')`, "m-"+cv, cv, at, at[:10])
	}
	rows, err := c.conversationActivityRows(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	coverage := map[string]any{}
	for _, row := range rows {
		if row["event_kind"] == "human" {
			coverage[firstString(row["conversation_id"])] = row["occurrence_coverage"]
		}
	}
	if coverage["fork"] != "partial" || coverage["root"] != "complete" {
		t.Fatalf("activity coverage: %v", coverage)
	}
	span := parallelismActivity{Turns: parallelismSpans{{now.Add(-time.Hour).UnixMilli(), now.Add(-50 * time.Minute).UnixMilli()}}}
	data, err := c.parallelismThreads(t.Context(), now, map[string]parallelismActivity{"root": span, "fork": span})
	if err != nil {
		t.Fatal(err)
	}
	partial := map[string]string{}
	for _, thread := range data.Threads {
		partial[thread.ConversationID] = thread.OccurrenceCoverage
	}
	if partial["fork"] != "partial" || partial["root"] != "" || data.Coverage.Occurrences != "partial" || data.Coverage.OccurrencePartial != 1 {
		t.Fatalf("parallelism coverage: %v %+v", partial, data.Coverage)
	}
	// A provider read with no history declaration no longer makes these same
	// observations partial. They still count as separate messages.
	fixtureExec(t, c, `INSERT INTO execution_evidence_state(conversation_id,version,input,id_policy) VALUES('fork',?,'ingest','line-locator/v1')`, executionEvidenceVersion)
	counts := fixtureCounts(t, c)
	if counts.Messages != 2 || counts.PartialConversations != 0 || counts.OccurrenceCoverage != "complete" {
		t.Fatalf("quick stats after provider read: %+v", counts)
	}
	rows, err = c.conversationActivityRows(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row["event_kind"] == "human" && row["occurrence_coverage"] != "complete" {
			t.Fatalf("activity after provider read: %v", row)
		}
	}
	data, err = c.parallelismThreads(t.Context(), now, map[string]parallelismActivity{"root": span, "fork": span})
	if err != nil {
		t.Fatal(err)
	}
	if data.Coverage.Occurrences != "complete" || data.Coverage.OccurrencePartial != 0 {
		t.Fatalf("parallelism after provider read: %+v", data.Coverage)
	}
}

// Typed evidence written alone (a backfill, or one claim) reaches every
// cached view built on the resolution without any other write; derived
// publication leaves them warm.
func TestExecutionEvidenceReachesCachedViews(t *testing.T) {
	c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "child", parent: "root", depth: 1})
	fixtureLaunch(t, c, "launch", "root", "s-root", "", `{"agent_id":"child"}`, 0)
	fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES('ask','root','ask','user','message','hello','h','2026-01-01T00:00:05Z');
		INSERT INTO message_authorship(message_id,conversation_id,workspace_id,sent_at,day,total_chars,spans_json) VALUES('ask','root','work','2026-01-01T00:00:05Z','2026-01-01',5,'[]')`)
	ctx := t.Context()
	type views struct {
		roots       *usageConversationAttribution
		parallelism *concurrencyData
		activity    []map[string]any
		delegation  *delegationSnapshot
	}
	read := func() views {
		t.Helper()
		var v views
		var err error
		if v.roots, err = c.localUsageRoots(ctx); err != nil {
			t.Fatal(err)
		}
		if v.parallelism, err = c.parallelismData(ctx); err != nil {
			t.Fatal(err)
		}
		if v.activity, err = c.conversationActivityRows(ctx); err != nil {
			t.Fatal(err)
		}
		if v.delegation, err = c.delegationSnapshot(ctx, "work"); err != nil {
			t.Fatal(err)
		}
		return v
	}
	same := func(a, b views) [4]bool {
		return [4]bool{a.roots == b.roots, a.parallelism == b.parallelism,
			reflect.ValueOf(a.activity).Pointer() == reflect.ValueOf(b.activity).Pointer() && len(a.activity) == len(b.activity), a.delegation == b.delegation}
	}
	warm := read()
	if len(warm.activity) == 0 {
		t.Fatal("the fixture has no activity to cache")
	}
	if same(warm, read()) != [4]bool{true, true, true, true} {
		t.Fatal("the views are not cached")
	}
	// Derived publication keeps every view.
	fixtureExec(t, c, `INSERT INTO parallelism_dirty SELECT id,'probe' FROM conversations WHERE true ON CONFLICT(conversation_id) DO NOTHING`)
	if _, err := c.refreshParallelism(ctx, 100, true); err != nil {
		t.Fatal(err)
	}
	if got := same(warm, read()); got != [4]bool{true, true, true, true} {
		t.Fatalf("derived publication invalidated views: %v", got)
	}
	// A backfill writes only claims and state.
	if _, err := c.BackfillExecutionEvidence(ctx, 10); err != nil {
		t.Fatal(err)
	}
	after := read()
	if got := same(warm, after); got != [4]bool{} {
		t.Fatalf("a backfill left cached views (roots, parallelism, activity, delegation) stale: %v", got)
	}
	if after.roots.resolution == warm.roots.resolution || after.delegation.Revision == warm.delegation.Revision {
		t.Fatal("views were recomputed against the old resolution")
	}
	// One claim, directly.
	fixtureExec(t, c, `INSERT INTO execution_claims(conversation_id,claim_key,source,kind,subject,object,origin,rule) VALUES('child','probe','record',?,'conversation','native:codex|local|root','native','codex-session-meta/v1')`, evidenceHistoryCopy)
	if got := same(after, read()); got != [4]bool{} {
		t.Fatalf("a claim write left cached views stale: %v", got)
	}
}

// A forward rebuild never waits for an earlier snapshot's catalog occurrence
// build, and the later global build still equals a full one.
func TestExecutionRebuildDoesNotWaitForOccurrenceBuild(t *testing.T) {
	c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "fork", parent: "root", depth: 1})
	fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES('copy','fork','evt_1','user','message','hi','h'),('original','root','evt_1','user','message','hi','h')`)
	if _, err := c.executionResolution(t.Context()); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	override(t, &executionOccurrenceBuildHook, func() {
		once.Do(func() { close(entered); <-release })
	})
	done := make(chan error, 1)
	go func() {
		_, _, err := c.usageConversationRootsAndOccurrences(context.Background(), nil)
		done <- err
	}()
	<-entered
	fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES('more','root','evt_2','assistant','message','x','x')`)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := c.executionResolution(ctx); err != nil {
		t.Fatal("the next generation waited for the global occurrence build:", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_, global, err := c.usageConversationRootsAndOccurrences(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	full, err := fixtureResolution(t, c).Occurrences(t.Context(), c.DB, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(global.Classified(), full.Classified()) || global.Lookup(sourceRef{sourceMessage, "copy"}, sourceRef{}).Role != occurrenceInherited {
		t.Fatalf("global %v, full %v", global.Classified(), full.Classified())
	}
}

// Readers at one older generation, with no kept snapshot at or below it,
// share one full load.
func TestExecutionOlderTransactionsShareOneBuild(t *testing.T) {
	c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "child", parent: "root", depth: 1})
	override(t, &executionCacheEarlier, 0)
	const readers = 4
	var txs []*sql.Tx
	for range readers {
		tx, err := c.reader().BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, _, err := executionSourceGeneration(t.Context(), tx); err != nil {
			t.Fatal(err)
		}
		txs = append(txs, tx)
	}
	fixtureExec(t, c, `UPDATE conversations SET agent_nickname='later' WHERE id='child'`)
	if _, err := c.executionResolution(t.Context()); err != nil {
		t.Fatal(err)
	}
	old, _, _ := executionSourceGeneration(t.Context(), txs[0])
	release := make(chan struct{})
	override(t, &executionSnapshotBuildHook, func(generation int64) {
		if generation == old {
			<-release
		}
	})
	before := c.execution.stats.fullLoads
	results := make(chan *executionResolution, readers)
	errs := make(chan error, readers)
	for _, tx := range txs {
		go func() {
			r, err := c.executionResolutionAt(context.Background(), tx)
			results <- r
			errs <- err
		}()
	}
	// Release the leader once every other reader waits on it.
	for {
		c.execution.mu.Lock()
		waiting := c.execution.stats.waits
		c.execution.mu.Unlock()
		if waiting >= readers-1 {
			break
		}
		runtime.Gosched()
	}
	close(release)
	var first *executionResolution
	for range readers {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		r := <-results
		if first == nil {
			first = r
		} else if r != first {
			t.Fatal("readers at one generation got different resolutions")
		}
	}
	if loads := c.execution.stats.fullLoads - before; loads != 1 {
		t.Fatalf("%d full loads for one older generation", loads)
	}
	if first.Executions["conversation:child"].Label.Text == "later" {
		t.Fatal("an older transaction saw a later rename")
	}
}
