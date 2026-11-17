package archive

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestTeamsPrivacyPolicyAndRevisions(t *testing.T) {
	c, _ := testCatalog(t)
	ctx := context.Background()
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','test','local','w','Synthetic','2026-01-01')`, `INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','test','local','c')`)
	read := func() teamsPrivacy {
		t.Helper()
		p, err := c.teamsPrivacy(ctx, "c")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := read()
	if p.Private || !p.SensitivityHold || p.Revision != 0 {
		t.Fatalf("initial: %+v", p)
	}
	p, err := c.setTeamsPrivate(ctx, "c", true)
	if err != nil || !p.Private || p.Revision != 1 {
		t.Fatalf("private: %+v %v", p, err)
	}
	p, err = c.setTeamsPrivate(ctx, "c", true)
	if err != nil || p.Revision != 1 {
		t.Fatalf("idempotent: %+v %v", p, err)
	}
	p, err = c.setTeamsClassification(ctx, "c", "v1", "pending")
	if err != nil || !p.Private || !p.SensitivityHold {
		t.Fatalf("pending: %+v %v", p, err)
	}
	p, err = c.setTeamsClassification(ctx, "c", "v1", "flagged")
	if err != nil || !p.Private || !p.SensitivityHold {
		t.Fatalf("flagged: %+v %v", p, err)
	}
	p, err = c.reviewTeamsClassification(ctx, "c", "v1", false)
	if err != nil || !p.Private || !p.UserPrivate || p.SensitivityHold {
		t.Fatalf("review: %+v %v", p, err)
	}
	reviewRevision := p.Revision
	if _, err = c.setTeamsPrivate(ctx, "c", false); err != nil {
		t.Fatal(err)
	}
	if _, err = c.setTeamsPrivate(ctx, "c", true); err != nil {
		t.Fatal(err)
	}
	if _, err = c.reviewTeamsClassification(ctx, "c", "v1", false, &reviewRevision); !errors.Is(err, errTeamsPrivacyStale) {
		t.Fatalf("stale review: %v", err)
	}
	if p = read(); !p.Private {
		t.Fatalf("stale review cleared private mark: %+v", p)
	}
	if _, err = c.reviewTeamsClassification(ctx, "c", "v1", false); !errors.Is(err, errTeamsPrivacyStale) {
		t.Fatalf("repeated review: %v", err)
	}
	if _, err = c.setTeamsPrivate(ctx, "c", false); err != nil {
		t.Fatal(err)
	}
	p, err = c.setTeamsClassification(ctx, "c", "v2", "pending")
	if err != nil || !p.Private || !p.SensitivityHold || p.ClassifierReviewed {
		t.Fatalf("new input: %+v %v", p, err)
	}
	rev := p.Revision
	if _, err = c.setTeamsClassification(ctx, "c", "v1", "clear"); err == nil {
		t.Fatal("stale classifier completed")
	}
	if _, err = c.reviewTeamsClassification(ctx, "c", "v1", false); err == nil {
		t.Fatal("stale review accepted")
	}
	if p = read(); p.Revision != rev || !p.Private {
		t.Fatalf("stale result changed policy: %+v", p)
	}
	p, err = c.setTeamsClassification(ctx, "c", "v2", "clear")
	if err != nil || p.Private || p.SensitivityHold {
		t.Fatalf("clear: %+v %v", p, err)
	}
	if _, err = c.setTeamsPrivate(ctx, "missing", true); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown conversation: %v", err)
	}
	// Privacy survives deletion and re-indexing of the same conversation identity.
	if _, err = c.setTeamsPrivate(ctx, "c", true); err != nil {
		t.Fatal(err)
	}
	execTx(t, c.DB, `DELETE FROM conversations WHERE id='c'`, `INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('new-local-id','w','test','local','c')`)
	p, err = c.teamsPrivacy(ctx, "new-local-id")
	if err != nil || !p.Private {
		t.Fatalf("mark lost on reindex: %+v", p)
	}
}

func TestTeamsPrivacyTransfersWithConversation(t *testing.T) {
	source, _ := testCatalog(t)
	target, _ := testCatalog(t)
	ctx := context.Background()
	for _, c := range []*Catalog{source, target} {
		execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','test','local','w','Synthetic','2026-01-01')`, `INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','test','local','c')`)
	}
	if _, err := source.setTeamsClassification(ctx, "c", "input", "pending"); err != nil {
		t.Fatal(err)
	}
	if _, err := target.setTeamsClassification(ctx, "c", "target", "pending"); err != nil {
		t.Fatal(err)
	}
	if _, err := target.setTeamsClassification(ctx, "c", "target", "clear"); err != nil {
		t.Fatal(err)
	}
	if err := mergeHistory(ctx, target, source.Path); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, target, `SELECT count(*) FROM teams_protection_assertions WHERE kind='legacy_hold' AND evidence='unreconciled_import' AND active=1`); n != 1 {
		t.Fatalf("import provenance hold: %d", n)
	}
	p, err := target.teamsPrivacy(ctx, "c")
	if err != nil || !p.Private || p.UserPrivate || !p.SensitivityHold || p.ClassifierState != "flagged" || p.Revision < 3 {
		t.Fatalf("transferred policy: %+v %v", p, err)
	}
}

func TestTeamsPrivacyHTTP(t *testing.T) {
	c, cfg := testCatalog(t)
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','test','local','w','Synthetic','2026-01-01')`, `INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','test','local','c')`)
	s := testServer(t, cfg, c)
	path := "/api/teams/privacy?conversation=c"
	if w := apiCall(t, s, "", "GET", path, nil); w.Code != 401 {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
	if w := apiCall(t, s, cfg.APIToken, "POST", path, `{"action":"mark","private":true,"revision":0}`); w.Code != 200 {
		t.Fatalf("mark: %d %s", w.Code, w.Body.String())
	}
	if w := apiCall(t, s, cfg.APIToken, "POST", path, `{"action":"mark","private":false,"revision":0}`); w.Code != 409 {
		t.Fatalf("stale mark: %d", w.Code)
	}
	p, err := c.teamsPrivacy(t.Context(), "c")
	if err != nil || !p.Private {
		t.Fatalf("stored: %+v %v", p, err)
	}
	if w := apiCall(t, s, cfg.APIToken, "POST", path, `{"action":"mark","private":false,"revision":1,"extra":1}`); w.Code != 400 {
		t.Fatalf("unknown field: %d", w.Code)
	}
	if w := apiCall(t, s, cfg.APIToken, "POST", path, `{"action":"review","private":false,"revision":1,"classifierInput":"stale"}`); w.Code != 409 {
		t.Fatalf("stale review: %d", w.Code)
	}
	if p, err = c.teamsPrivacy(t.Context(), "c"); err != nil || !p.Private {
		t.Fatalf("invalid request changed policy: %+v %v", p, err)
	}
}

func TestTeamsPrivacyTransferInvalidatesConflictingClassifier(t *testing.T) {
	source, _ := testCatalog(t)
	target, _ := testCatalog(t)
	ctx := context.Background()
	for _, c := range []*Catalog{source, target} {
		execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','test','local','w','Synthetic','2026-01-01')`, `INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','test','local','c')`)
	}
	for _, item := range []struct {
		c            *Catalog
		input, state string
	}{{source, "new", "failed"}, {target, "old", "clear"}} {
		if _, err := item.c.setTeamsClassification(ctx, "c", item.input, "pending"); err != nil {
			t.Fatal(err)
		}
		if _, err := item.c.setTeamsClassification(ctx, "c", item.input, item.state); err != nil {
			t.Fatal(err)
		}
	}
	if err := mergeHistory(ctx, target, source.Path); err != nil {
		t.Fatal(err)
	}
	p, err := target.teamsPrivacy(ctx, "c")
	if err != nil || !p.SensitivityHold || !p.Private || p.UserPrivate || p.ClassifierState != "flagged" {
		t.Fatalf("conflicting evidence: %+v %v", p, err)
	}
}

func TestTeamsProtectionAssertionsRetainFlagsAndLegacyHolds(t *testing.T) {
	c, _ := testCatalog(t)
	ctx := context.Background()
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','test','local','w','Synthetic','2026-01-01')`, `INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','codex','local','thread')`)
	if _, err := c.setTeamsPrivate(ctx, "c", true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.setTeamsClassification(ctx, "c", "proof-a", "pending"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.setTeamsClassification(ctx, "c", "proof-a", "flagged"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.reviewTeamsClassification(ctx, "c", "proof-a", false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.setTeamsClassification(ctx, "c", "proof-b", "pending"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.setTeamsClassification(ctx, "c", "proof-b", "clear"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.setTeamsPrivate(ctx, "c", false); err != nil {
		t.Fatal(err)
	}
	beforeNoop, err := c.teamsPrivacy(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.setTeamsPrivate(ctx, "c", false); err != nil {
		t.Fatal(err)
	}
	afterNoop, err := c.teamsPrivacy(ctx, "c")
	if err != nil || afterNoop.Revision != beforeNoop.Revision {
		t.Fatalf("no-op changed legacy revision: %+v %v", afterNoop, err)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_protection_actions WHERE kind='explicit_unmark'`); n != 2 {
		t.Fatalf("unmark evidence count %d", n)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_protection_assertions WHERE kind='positive_flag' AND evidence='proof-a' AND active=1`); n != 1 {
		t.Fatalf("flag lost after pending/clear: %d", n)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_protection_assertions WHERE kind='explicit_private' AND active=0 AND retirement_evidence='explicit_unmark'`); n != 1 {
		t.Fatalf("explicit retirement missing: %d", n)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_protection_baseline WHERE state='current'`); n != 0 {
		t.Fatal("legacy changes established coverage")
	}
	// Simulate upgrade from a pre-ledger clear record whose prior flag history is
	// unknown. Schema age alone does not clear that provenance gap.
	execTx(t, c.DB, `DELETE FROM meta WHERE key='teams_protection_legacy_assertions_v1'`)
	if err := c.ensureTeamsProtectionProofSchema(); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_protection_assertions WHERE kind='legacy_hold' AND active=1`); n != 1 {
		t.Fatalf("legacy provenance gap lost: %d", n)
	}
	if err := c.ensureTeamsProtectionProofSchema(); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_protection_assertions WHERE kind='legacy_hold' AND active=1`); n != 1 {
		t.Fatalf("legacy migration repeated: %d", n)
	}
}

func TestTeamsProtectionExactIdentityRetirement(t *testing.T) {
	c, _ := testCatalog(t)
	execTx(t, c.DB,
		`INSERT INTO teams_conversation_privacy(conversation_id,provider,account,native_id,user_private) VALUES('c','codex','old','thread',1)`,
		`UPDATE teams_conversation_privacy SET account='new' WHERE conversation_id='c'`,
		`INSERT INTO teams_conversation_privacy(conversation_id,provider,account,native_id,user_private) VALUES('other','codex','','',1)`)
	tx, err := c.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	p := teamsPrivacy{ConversationID: "c", provider: "codex", account: "new", native: "thread"}
	if err := teamsRetireExplicitProtection(tx, p); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_protection_assertions WHERE conversation_id='c' AND account='old' AND active=1`); n != 1 {
		t.Fatalf("old identity lost: %d", n)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_protection_assertions a JOIN teams_protection_actions x ON x.action_id=a.retired_by_action_id WHERE a.conversation_id='c' AND a.account='new' AND a.active=0 AND x.retired_count=1`); n != 1 {
		t.Fatalf("action linkage missing: %d", n)
	}
	tx, err = c.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := teamsRetireExplicitProtection(tx, teamsPrivacy{ConversationID: "other", provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_protection_assertions WHERE conversation_id='other' AND active=0`); n != 1 {
		t.Fatalf("empty identity did not retire: %d", n)
	}
}

func TestTeamsProtectionPreLedgerUpgrade(t *testing.T) {
	c, _ := testCatalog(t)
	rows, err := c.DB.Query(`SELECT name FROM sqlite_master WHERE type='trigger' AND name LIKE 'teams_protection_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if _, err := c.DB.Exec(`DROP TRIGGER "` + name + `"`); err != nil {
			t.Fatal(err)
		}
	}
	execTx(t, c.DB, `DROP TABLE teams_protection_observations`, `DROP TABLE teams_protection_assertions`, `DROP TABLE teams_protection_actions`, `DROP TABLE teams_protection_baseline`, `DELETE FROM meta WHERE key='teams_protection_legacy_assertions_v1'`,
		`INSERT INTO teams_conversation_privacy(conversation_id,classifier_state) VALUES('pending','pending'),('clear','clear'),('pristine','unclassified'),('flag','flagged')`)
	if err := c.Initialize(); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_protection_assertions WHERE kind='legacy_hold' AND conversation_id IN ('pending','clear')`); n != 2 {
		t.Fatalf("missing legacy holds: %d", n)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_protection_assertions WHERE conversation_id='pristine'`); n != 0 {
		t.Fatalf("pristine held: %d", n)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_protection_assertions WHERE conversation_id='flag' AND kind='positive_flag'`); n != 1 {
		t.Fatalf("flag lost: %d", n)
	}
	if n := countRows(t, c, `SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'teams_protection_%'`); n != 11 {
		t.Fatalf("missing triggers: %d", n)
	}
}
