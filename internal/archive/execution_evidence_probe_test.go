//go:build probe

package archive

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Times the execution dependency digests, publication guards, freshness
// reads and the retained-row backfill on a synthetic library of the size of
// the largest one measured (about 48,000 conversations): families of a root
// and its launched agents, one family large enough to be the biggest
// component, results whose handles name transcripts, and transcripts
// arriving later. It writes only to a catalog under t.TempDir().
//
//	go test -tags probe -run '^TestExecutionEvidenceProbe$' -v -timeout 60m ./internal/archive
//
// PHAROS_EXECUTION_COPY='/path/to/copy/catalog.sqlite3' also times the
// read-only digests on a disposable catalog copy, opened immutable.
func TestExecutionEvidenceProbe(t *testing.T) {
	ctx := context.Background()
	families, children := 6000, 7
	if n := os.Getenv("PHAROS_EXECUTION_PROBE_FAMILIES"); n != "" {
		fmt.Sscan(n, &families)
	}
	catalog, _ := freshCatalog(t)
	var ids []string
	evidenceProbeStep(t, fmt.Sprintf("seed %d families × %d agents + one of 2000", families, children), func() (err error) {
		ids, err = seedExecutionProbe(ctx, catalog, families, children, 2000)
		return
	})
	var conversations, messages, sessions int
	_ = catalog.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM conversations),(SELECT COUNT(*) FROM messages),(SELECT COUNT(*) FROM agent_sessions)`).Scan(&conversations, &messages, &sessions)
	t.Logf("conversations=%d messages=%d sessions=%d", conversations, messages, sessions)
	big, typical := ids[0], ids[len(ids)/2]
	measure := func(label string) {
		t.Logf("--- %s", label)
		var digests map[string]string
		evidenceProbeStep(t, "catalog-wide digests (cold, admitted from a read-only snapshot)", func() (err error) { digests, err = executionDependencyDigests(ctx, catalog.DB); return })
		evidenceProbeStep(t, "catalog-wide digests (warm)", func() (err error) { _, err = executionDependencyDigests(ctx, catalog.DB); return })
		evidenceProbeStep(t, "catalog-wide digests in a write tx before it writes (cached lookup)", func() error {
			return catalog.writeTransaction(ctx, "probe digests", func(tx *sql.Tx) error { _, err := executionDependencyDigests(ctx, tx); return err })
		})
		evidenceProbeStep(t, "catalog-wide digests in a write tx after it writes (computed, not stored)", func() error {
			tx, err := catalog.DB.Begin()
			if err != nil {
				return err
			}
			defer tx.Rollback()
			if _, err := tx.Exec(`UPDATE conversations SET agent_nickname='probe' WHERE id=?`, typical); err != nil {
				return err
			}
			_, err = executionDependencyDigests(ctx, tx)
			return err
		})
		evidenceProbeStep(t, "inverse index in a write tx after it writes (patched, not stored)", func() error {
			tx, err := catalog.DB.Begin()
			if err != nil {
				return err
			}
			defer tx.Rollback()
			if _, err := tx.Exec(`UPDATE conversations SET agent_nickname='probe' WHERE id=?`, typical); err != nil {
				return err
			}
			_, err = dependencyInverseIndex(ctx, tx)
			return err
		})
		sizes := map[string]int{}
		for _, digest := range digests {
			sizes[digest]++
		}
		largest := 0
		for _, n := range sizes {
			largest = max(largest, n)
		}
		t.Logf("components=%d largest=%d", len(sizes), largest)
		evidenceProbeStep(t, "component walk: typical conversation", func() error {
			got, err := executionDependencyDigestsFor(ctx, catalog.DB, []string{typical})
			if err == nil && got[typical] != digests[typical] {
				err = fmt.Errorf("walk %q != catalog-wide %q", got[typical], digests[typical])
			}
			return err
		})
		evidenceProbeStep(t, "component walk: largest family", func() error {
			got, err := executionDependencyDigestsFor(ctx, catalog.DB, []string{big})
			if err == nil && got[big] != digests[big] {
				err = fmt.Errorf("walk %q != catalog-wide %q", got[big], digests[big])
			}
			return err
		})
		// A publication guard checks a run's batch inside the write transaction.
		batch := map[string]string{}
		for _, id := range ids[len(ids)/2 : len(ids)/2+200] {
			batch[id] = digests[id]
		}
		if _, err := catalog.DB.Exec(agentEventsLedgerDDL + ";" + executionDependencyMembersDDL); err != nil {
			t.Fatal(err)
		}
		evidenceProbeStep(t, "publication guard + members: 200 conversations in a write tx", func() error {
			return catalog.writeTransaction(ctx, "probe guard", func(tx *sql.Tx) error {
				publication, err := publishExecutionDependents(ctx, tx, "agent_events_conversations", batch)
				if err != nil {
					return err
				}
				for id, ok := range publication.current {
					if !ok {
						return fmt.Errorf("%s reads as stale", id)
					}
				}
				return err
			})
		})
		evidenceProbeStep(t, "publication guard + members: largest family in a write tx", func() error {
			return catalog.writeTransaction(ctx, "probe guard", func(tx *sql.Tx) error {
				_, err := publishExecutionDependents(ctx, tx, "agent_events_conversations", map[string]string{big: digests[big]})
				return err
			})
		})
		if _, err := catalog.DB.Exec(`INSERT INTO agent_events_conversations(conversation_id,structure,built_at,execution_digest) VALUES(?,'',?,?)
		 ON CONFLICT(conversation_id) DO UPDATE SET execution_digest=excluded.execution_digest`, typical, "now", digests[typical]); err != nil {
			t.Fatal(err)
		}
		evidenceProbeStep(t, "read freshness: one conversation", func() error {
			current, err := storedExecutionCurrent(ctx, catalog.DB, "agent_events_conversations", typical)
			if err == nil && !current {
				err = fmt.Errorf("reads as stale")
			}
			return err
		})
		evidenceProbeStep(t, "stale count (catalog-wide)", func() error {
			_, err := agentEventsExecutionStale(ctx, catalog.DB)
			return err
		})
	}
	measure("legacy evidence (no typed claims)")
	var done int
	evidenceProbeStep(t, "backfill typed evidence from retained rows", func() (err error) { done, err = catalog.BackfillExecutionEvidence(ctx, 500); return })
	t.Logf("backfilled %d conversations", done)
	evidenceProbeStep(t, "backfill again (nothing pending)", func() (err error) { done, err = catalog.BackfillExecutionEvidence(ctx, 500); return })
	measure("typed evidence current")
	// Ingestion: one transcript arriving that a family's result names.
	evidenceProbeStep(t, "ingest a late transcript + its family's walk", func() error {
		ingestRecordTx(t, catalog, WorkspaceRecord{SourceID: "late", SourceKind: "codex", Account: "local", Conversations: []ConversationRecord{
			{NativeID: "late-agent", Provider: "codex", Account: "local", Messages: []MessageRecord{{NativeID: "u", Role: "user", Kind: "message", Text: "late"}}}}})
		_, err := executionDependencyDigestsFor(ctx, catalog.DB, []string{typical})
		return err
	})
	// Write-time propagation: every conversation has published events, so a
	// commit marks the members whose digest it changed.
	t.Log("--- dependents marked at write time")
	digests, err := executionDependencyDigests(ctx, catalog.DB)
	if err != nil {
		t.Fatal(err)
	}
	evidenceProbeStep(t, "publish events for every conversation", func() error {
		return catalog.writeTransaction(ctx, "probe publish", func(tx *sql.Tx) error {
			for id, digest := range digests {
				if _, err := tx.ExecContext(ctx, `INSERT INTO agent_events_conversations(conversation_id,structure,built_at,execution_digest) VALUES(?,'',?,?)
				 ON CONFLICT(conversation_id) DO UPDATE SET execution_digest=excluded.execution_digest`, id, "now", digest); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO execution_dependency_members(digest,conversation_id) VALUES(?,?) ON CONFLICT DO NOTHING`, digest, id); err != nil {
					return err
				}
			}
			return markExecutionDependents(ctx, tx)
		})
	})
	dirty := func() int {
		var n int
		_ = catalog.DB.QueryRow(`SELECT COUNT(*) FROM agent_events_dirty`).Scan(&n)
		return n
	}
	evidenceProbeStep(t, "ingest an unrelated transcript (marks nothing)", func() error {
		ingestRecordTx(t, catalog, WorkspaceRecord{SourceID: "alone", SourceKind: "codex", Account: "local", Conversations: []ConversationRecord{
			{NativeID: "alone", Provider: "codex", Account: "local", Messages: []MessageRecord{{NativeID: "u", Role: "user", Kind: "message", Text: "alone"}}}}})
		return nil
	})
	t.Logf("dirty after it: %d", dirty())
	evidenceProbeStep(t, "ingest a child of a typical family (marks the family)", func() error {
		ingestRecordTx(t, catalog, WorkspaceRecord{SourceID: "typical-child", SourceKind: "codex", Account: "local", Conversations: []ConversationRecord{
			{NativeID: "typical-child", Provider: "codex", Account: "local", ParentNativeID: fmt.Sprintf("thread-%d", families/2), AgentDepth: 1,
				Messages: []MessageRecord{{NativeID: "u", Role: "user", Kind: "message", Text: "child"}}}}})
		return nil
	})
	t.Logf("dirty after it: %d", dirty())
	evidenceProbeStep(t, "ingest a child of the largest family (marks the family)", func() error {
		ingestRecordTx(t, catalog, WorkspaceRecord{SourceID: "large-child", SourceKind: "codex", Account: "local", Conversations: []ConversationRecord{
			{NativeID: "large-child", Provider: "codex", Account: "local", ParentNativeID: "thread-0", AgentDepth: 1,
				Messages: []MessageRecord{{NativeID: "u", Role: "user", Kind: "message", Text: "child"}}}}})
		return nil
	})
	t.Logf("dirty after it: %d", dirty())
	// Read side: what the next mark would mark, after writers that mark nothing.
	t.Log("--- unmarked set (read side)")
	unmarked := func(label string) {
		evidenceProbeStep(t, label, func() error {
			ids, err := executionUnmarked(ctx, catalog.DB, "agent_events_conversations")
			t.Logf("unmarked=%d", len(ids))
			return err
		})
	}
	unmarkedWrite := func(statement string, args ...any) {
		if err := catalog.writeTransaction(ctx, "probe unmarked", func(tx *sql.Tx) error { _, err := tx.Exec(statement, args...); return err }); err != nil {
			t.Fatal(err)
		}
	}
	unmarkedWrite(`UPDATE conversations SET parent_id=NULL WHERE id=?`, ids[len(ids)/2+1])
	unmarked("unmarked set: one typical family changed (cold)")
	unmarked("unmarked set: same state (warm)")
	unmarkedWrite(`UPDATE conversations SET parent_id=NULL WHERE id=(SELECT MIN(id) FROM conversations WHERE parent_id=?)`, big)
	unmarked("unmarked set: largest family changed (cold)")
	unmarkedWrite(`DELETE FROM meta WHERE key=?`, executionDependentsKey)
	unmarked("unmarked set: no cursor, whole catalog compared (cold)")
	evidenceProbeStep(t, "first mark with no cursor (whole catalog, in a write tx)", func() error {
		return catalog.writeTransaction(ctx, "probe mark", func(tx *sql.Tx) error { return markExecutionDependents(ctx, tx) })
	})
	t.Logf("dirty after it: %d", dirty())
	// Copied history: every child declares a copy of its root's transcript.
	t.Log("--- copied history (one copy claim per child)")
	if _, err := catalog.DB.Exec(`INSERT INTO execution_claims(conversation_id,claim_key,source,kind,subject,object,origin,authority,rule,namespace,id_kind,locator,detail_json)
	 SELECT c.id,'probe-copy','record','history_copy','conversation','native:codex|local|'||p.native_id,'native','verified','codex-session-meta/v1','codex/local','native-event','','{}'
	 FROM conversations c JOIN conversations p ON p.id=c.parent_id`); err != nil {
		t.Fatal(err)
	}
	var claims []executionClaim
	evidenceProbeStep(t, "occurrence claims: every copy", func() (err error) { claims, err = persistedOccurrenceClaims(ctx, catalog.DB, nil); return })
	t.Logf("occurrence claims=%d", len(claims))
	family := []string{}
	for _, id := range ids {
		if id == big || strings.HasPrefix(id, "c-thread-0-") {
			family = append(family, id)
		}
	}
	evidenceProbeStep(t, fmt.Sprintf("occurrence claims: largest family (%d)", len(family)), func() (err error) {
		_, err = persistedOccurrenceClaims(ctx, catalog.DB, family)
		return
	})
	if path := os.Getenv("PHAROS_EXECUTION_COPY"); path != "" {
		probeCopyDigests(t, path)
	}
}

func probeCopyDigests(t *testing.T, path string) {
	ctx := context.Background()
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
	t.Log("--- catalog copy (read-only)")
	var digests map[string]string
	evidenceProbeStep(t, "catalog-wide digests", func() (err error) { digests, err = executionDependencyDigests(ctx, db); return })
	sizes := map[string][]string{}
	for id, digest := range digests {
		sizes[digest] = append(sizes[digest], id)
	}
	components := make([][]string, 0, len(sizes))
	for _, members := range sizes {
		components = append(components, members)
	}
	sort.Slice(components, func(i, j int) bool { return len(components[i]) > len(components[j]) })
	t.Logf("conversations=%d components=%d largest=%d", len(digests), len(components), len(components[0]))
	evidenceProbeStep(t, "component walk: largest component", func() error {
		_, err := executionDependencyDigestsFor(ctx, db, components[0][:1])
		return err
	})
	if len(components) > 1 {
		mid := components[len(components)/2]
		evidenceProbeStep(t, "component walk: median component", func() error {
			_, err := executionDependencyDigestsFor(ctx, db, mid[:1])
			return err
		})
	}
}

// seedExecutionProbe writes families of a Codex root that launched children
// (a delegation, its result naming the child's thread, the child at depth
// 1) and one family with large children, in one transaction. It returns the
// conversation IDs, the large family's root first.
func seedExecutionProbe(ctx context.Context, catalog *Catalog, families, children, large int) ([]string, error) {
	ids := []string{}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	err := catalog.writeTransaction(ctx, "seed probe", func(tx *sql.Tx) error {
		statements := map[string]*sql.Stmt{}
		for name, statement := range map[string]string{
			"workspace":    `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES(?,'codex','local',?,?,?)`,
			"conversation": `INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth,started_at) VALUES(?,?,'codex','local',?,?,?,?)`,
			"message":      `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,call_id,created_at,source_order) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			"session":      `INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,parent_id,delegation_message_id,total_tokens) VALUES(?,?,?,?,?,'codex',?,?,?)`,
			"ref":          `INSERT INTO conversation_parent_refs(provider,account,native_id,parent_native_id) VALUES('codex','local',?,?)`,
		} {
			s, err := tx.PrepareContext(ctx, statement)
			if err != nil {
				return err
			}
			defer s.Close()
			statements[name] = s
		}
		exec := func(name string, args ...any) error {
			_, err := statements[name].ExecContext(ctx, args...)
			return err
		}
		family := func(f, n int) error {
			root := fmt.Sprintf("thread-%d", f)
			workspace, rootID := "ws-"+root, "c-"+root
			when := at.Add(time.Duration(f) * time.Minute).Format(time.RFC3339)
			if err := exec("workspace", workspace, root, root, when); err != nil {
				return err
			}
			if err := exec("conversation", rootID, workspace, root, nil, 0, when); err != nil {
				return err
			}
			ids = append(ids, rootID)
			main := "s-main-" + root
			if err := exec("session", main, workspace, rootID, "main", "main", nil, nil, 100); err != nil {
				return err
			}
			if err := exec("message", "m-u-"+root, rootID, "u-"+root, "user", "message", "Split the work", "h-u", "", when, 0); err != nil {
				return err
			}
			for k := range n {
				child, call := fmt.Sprintf("%s-agent-%d", root, k), fmt.Sprintf("call-%d", k)
				delegation, result := "m-d-"+child, "m-r-"+child
				if err := exec("message", delegation, rootID, "d-"+child, "assistant", "delegation", `{"tool":"spawn_agent","input":{"message":"Review"}}`, "h-d-"+child, call, when, 2*k+1); err != nil {
					return err
				}
				if err := exec("message", result, rootID, "r-"+child, "tool", "delegation_result", `{"tool":"spawn_agent","content":"{\"agent_id\":\"`+child+`\"}"}`, "h-r-"+child, call, when, 2*k+2); err != nil {
					return err
				}
				if err := exec("session", "s-"+child, workspace, rootID, call, "subagent", main, delegation, 0); err != nil {
					return err
				}
				// Every tenth child has not arrived yet: its handle is an
				// unanswered key.
				if k%10 == 9 {
					continue
				}
				childID := "c-" + child
				if err := exec("conversation", childID, workspace, child, rootID, 1, when); err != nil {
					return err
				}
				if err := exec("ref", child, root); err != nil {
					return err
				}
				if err := exec("message", "m-u-"+child, childID, "u-"+child, "user", "message", "Review", "h-u", "", when, 0); err != nil {
					return err
				}
				ids = append(ids, childID)
			}
			return nil
		}
		if err := family(0, large); err != nil {
			return err
		}
		for f := 1; f <= families; f++ {
			if err := family(f, children); err != nil {
				return err
			}
		}
		return nil
	})
	return ids, err
}

func evidenceProbeStep(t *testing.T, name string, run func() error) {
	t.Helper()
	start := time.Now()
	if err := run(); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	t.Logf("%-58s %v", name, time.Since(start).Round(time.Millisecond))
}
