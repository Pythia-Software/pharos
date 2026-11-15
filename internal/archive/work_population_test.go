package archive

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestWorkPopulationUsesActivityAndFreezesMembership(t *testing.T) {
	c, _ := testCatalog(t)
	seedLabelingCatalog(t, c)
	from := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	until := from.Add(48 * time.Hour)
	execTx(t, c.DB, `UPDATE messages SET created_at='2026-11-06T12:00:00.000Z' WHERE id='m-a1'`)
	// Several short read transactions build the same membership as one.
	override(t, &workPopulationPage, 1)
	b := &workInventoryBuilder{limit: 1 << 20}
	if err := c.freezeWorkPopulation(t.Context(), b, from, until, nil); err != nil {
		t.Fatal(err)
	}
	inv, err := b.freeze(until)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := inv.index("c-root"); !ok {
		t.Fatal("old conversation with new activity was omitted")
	}
	n := len(inv.members)
	execTx(t, c.DB, `INSERT INTO conversations(id,workspace_id,provider,account,native_id,started_at) VALUES('late-insert','ws-main','codex','local','late-insert','2026-11-06T00:00:00.000Z')`,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES('late-message','late-insert','late-message','user','message','late historical arrival','late-hash','2026-11-06T00:00:00.000Z',9999)`,
		`UPDATE messages SET created_at='2020-01-01T00:00:00.000Z' WHERE id='m-a1'`)
	if len(inv.members) != n {
		t.Fatal("closed membership grew")
	}
	if _, ok := inv.index("late-insert"); ok {
		t.Fatal("late matching insert joined closed request")
	}
	if _, ok := inv.index("c-root"); !ok {
		t.Fatal("activity edit dropped a closed member")
	}
	fresh := &workInventoryBuilder{limit: 1 << 20}
	if err := c.freezeWorkPopulation(t.Context(), fresh, from, until, nil); err != nil {
		t.Fatal(err)
	}
	next, err := fresh.freeze(until.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := next.index("late-insert"); !ok {
		t.Fatal("new demand did not see late historical insert")
	}
}

func TestWorkPopulationKeepsExplicitDeletedMembersAndInputRevisions(t *testing.T) {
	c, _ := testCatalog(t)
	seedLabelingCatalog(t, c)
	before, err := workConversationRevision(t.Context(), c.reader(), "c-root")
	if err != nil {
		t.Fatal(err)
	}
	execTx(t, c.DB, `UPDATE messages SET text='changed input',content_hash='changed-hash' WHERE id='m-a1'`)
	after, err := workConversationRevision(t.Context(), c.reader(), "c-root")
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("canonical input edit did not supersede revision")
	}
	b := &workInventoryBuilder{limit: 1 << 20}
	if err := c.freezeWorkPopulation(t.Context(), b, time.Time{}, time.Time{}, []string{"missing", "c-root"}); err != nil {
		t.Fatal(err)
	}
	inv, err := b.freeze(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.members) != 2 {
		t.Fatal("explicit deleted member silently disappeared")
	}
	again := &workInventoryBuilder{limit: 1 << 20}
	if err := c.freezeWorkPopulation(t.Context(), again, time.Time{}, time.Time{}, []string{"missing"}); err != nil {
		t.Fatal(err)
	}
	if missing, ok := inv.index("missing"); !ok || again.members[0].Revision != inv.members[missing].Revision {
		t.Fatal("a deleted member's revision is not stable")
	}
}

// ingestRevisionFixture ingests one conversation through the supported path,
// which stores its input digest, and returns the record and conversation ID.
func ingestRevisionFixture(t *testing.T, c *Catalog) (WorkspaceRecord, string) {
	t.Helper()
	record := searchFixture()[0]
	ingestRecords(t, c, record)
	var id string
	if err := c.DB.QueryRow(`SELECT id FROM conversations WHERE native_id='query-thread'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return record, id
}

func workRevisionFor(t *testing.T, c *Catalog, id string) string {
	t.Helper()
	revisions, err := c.readWorkRevisions(t.Context(), []string{id})
	if err != nil {
		t.Fatal(err)
	}
	return revisions[id]
}

func storedInputDigest(t *testing.T, c *Catalog, id string) bool {
	t.Helper()
	var n int
	if err := c.DB.QueryRow(`SELECT count(*) FROM conversation_derivation_inputs WHERE conversation_id=?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestWorkRevisionTrustsStoredDigestAcrossDerivedOutput(t *testing.T) {
	c, _ := testCatalog(t)
	_, id := ingestRevisionFixture(t, c)
	if !storedInputDigest(t, c, id) {
		t.Fatal("ingest did not store its input digest")
	}
	b := &workInventoryBuilder{limit: 1 << 20}
	report, err := c.freezeWorkPopulationReport(t.Context(), b, time.Time{}, time.Time{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Fallbacks != 0 || report.Members == 0 {
		t.Fatalf("membership read message rows despite a stored digest: %+v", report)
	}
	inv, err := b.freeze(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	member, ok := inv.index(id)
	if !ok {
		t.Fatal("ingested conversation missing from membership")
	}
	revision := workRevisionFor(t, c, id)
	if inv.members[member].Revision != revision {
		t.Fatal("membership and batch revisions disagree")
	}
	// Derived output, and source fields the revision does not cover, keep both
	// the digest and the revision: publishing cannot supersede its own request.
	if _, err := c.estimateAuthorshipFor(t.Context(), []string{id}, formatTime(c.clock()), 100); err != nil {
		t.Fatal(err)
	}
	execTx(t, c.DB,
		fmt.Sprintf(`UPDATE messages SET raw_text='usage copy',speed='fast',speed_source='reported' WHERE conversation_id='%s'`, id),
		fmt.Sprintf(`UPDATE agent_sessions SET depth=depth+1,input_tokens=input_tokens+5 WHERE conversation_id='%s'`, id),
		fmt.Sprintf(`UPDATE messages SET text=text,content_hash=content_hash,source_order=source_order WHERE conversation_id='%s'`, id))
	if !storedInputDigest(t, c, id) {
		t.Fatal("derived or unchanged writes dropped the input digest")
	}
	if workRevisionFor(t, c, id) != revision {
		t.Fatal("derived output superseded the canonical revision")
	}
}

// The scheduler never writes ingest's digest table. Row inserts and deletes,
// session links and the conversation row move the revision even while a
// digest is kept; repairs that rewrite rows in place delete the digest, and
// the row hash then sees their edit.
func TestWorkRevisionSeesCanonicalEditsWithoutTouchingDigests(t *testing.T) {
	c, _ := testCatalog(t)
	record, id := ingestRevisionFixture(t, c)
	var member string
	if err := c.DB.QueryRow(`SELECT am.message_id FROM agent_session_messages am JOIN agent_sessions a ON a.id=am.agent_session_id
 WHERE a.conversation_id=? ORDER BY am.message_id LIMIT 1`, id).Scan(&member); err != nil {
		t.Fatalf("fixture has no session membership: %v", err)
	}
	seen := map[string]bool{workRevisionFor(t, c, id): true}
	for _, edit := range []struct{ name, sql string }{
		{"session membership", `DELETE FROM agent_session_messages WHERE message_id='%[2]s'`},
		{"message delete", `DELETE FROM messages WHERE id='%[2]s'`},
		{"message insert", `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order)
 VALUES('direct-insert','%[1]s','direct-insert','user','message','direct','direct-hash','2026-09-20T13:00:00Z',99)`},
		{"session link", `UPDATE agent_sessions SET kind='edited' WHERE conversation_id='%[1]s'`},
		{"conversation row", `UPDATE conversations SET ended_at='2030-01-01T00:00:00Z' WHERE id='%[1]s'`},
		{"repair in place", `UPDATE messages SET text='repaired',content_hash='repaired-hash' WHERE id='direct-insert';
 DELETE FROM conversation_derivation_inputs WHERE conversation_id='%[1]s'`},
	} {
		before := workRevisionFor(t, c, id)
		execTx(t, c.DB, strings.NewReplacer("%[1]s", id, "%[2]s", member).Replace(edit.sql))
		if edit.name != "repair in place" && !storedInputDigest(t, c, id) {
			t.Fatalf("%s: a canonical edit removed ingest's digest", edit.name)
		}
		after := workRevisionFor(t, c, id)
		if after == before || seen[after] {
			t.Fatalf("%s: canonical edit did not produce a new revision", edit.name)
		}
		seen[after] = true
	}
	// Supported ingest stores a digest again; the revision is new and stable.
	record.Conversations[0].Messages = append(record.Conversations[0].Messages,
		MessageRecord{NativeID: "q4", Role: "user", Kind: "message", Text: "One more question.", Selected: true, CreatedAt: "2026-09-20T12:00:09Z"})
	ingestRecords(t, c, record)
	if !storedInputDigest(t, c, id) {
		t.Fatal("supported ingest did not restore the input digest")
	}
	restored := workRevisionFor(t, c, id)
	if seen[restored] || workRevisionFor(t, c, id) != restored {
		t.Fatal("restored revision is not new and stable")
	}
	b := &workInventoryBuilder{limit: 1 << 20}
	report, err := c.freezeWorkPopulationReport(t.Context(), b, time.Time{}, time.Time{}, []string{id})
	if err != nil {
		t.Fatal(err)
	}
	if report.Fallbacks != 0 || b.members[0].Revision != restored {
		t.Fatalf("restored conversation still needs the row hash: %+v", report)
	}
}

// Development builds installed triggers that deleted ingest's digests on
// every message write. Initialize removes them, so classic and older writes
// keep their digests afterwards.
func TestWorkRevisionInitializeDropsRetiredTriggers(t *testing.T) {
	c, _ := testCatalog(t)
	_, id := ingestRevisionFixture(t, c)
	statements := []string{}
	for _, name := range retiredWorkRevisionTriggers {
		statements = append(statements, "CREATE TRIGGER "+name+" AFTER INSERT ON messages BEGIN DELETE FROM conversation_derivation_inputs WHERE conversation_id=NEW.conversation_id; END")
	}
	execTx(t, c.DB, statements...)
	reopened := reopenCatalog(t, c.Path)
	closeAtEnd(t, reopened)
	if err := reopened.Initialize(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := reopened.DB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'work_revision_%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("Initialize kept %d retired revision triggers: %v", n, err)
	}
	execTx(t, reopened.DB, fmt.Sprintf(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order)
 VALUES('classic-insert','%s','classic-insert','user','message','classic','classic-hash','2026-09-20T13:00:00Z',99)`, id))
	if !storedInputDigest(t, reopened, id) {
		t.Fatal("a classic message write still removed ingest's digest")
	}
}
