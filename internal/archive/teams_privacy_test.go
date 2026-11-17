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
	if err != nil || p.Private || p.SensitivityHold {
		t.Fatalf("review: %+v %v", p, err)
	}
	reviewRevision := p.Revision
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
	execTx(t, c.DB, `DELETE FROM conversations WHERE id='c'`, `INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','test','local','c')`)
	if p = read(); !p.Private {
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
	p, err := target.teamsPrivacy(ctx, "c")
	if err != nil || !p.Private || !p.UserPrivate || !p.SensitivityHold || p.ClassifierState != "unclassified" || p.Revision < 3 {
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
	if err != nil || !p.SensitivityHold || p.ClassifierState != "unclassified" {
		t.Fatalf("conflicting evidence: %+v %v", p, err)
	}
}
