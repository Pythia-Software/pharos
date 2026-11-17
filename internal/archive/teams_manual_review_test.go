package archive

import (
	"context"
	"errors"
	"testing"
)

func TestTeamsManualReviewBindsExactInputAndNeverClearsPrivateMark(t *testing.T) {
	c, _ := testCatalog(t)
	ctx := context.Background()
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','test','local','w','Synthetic','2026-01-01')`, `INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','test','local','native')`, `INSERT INTO messages(id,conversation_id,native_id,role,text,content_hash) VALUES('m','c','m','user','synthetic first','h')`)
	input, err := teamsManualReviewInput(ctx, c.reader(), "c")
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.manuallyClearTeamsInput(ctx, "c", input, 0)
	if err != nil || p.ClassifierState != "clear" || p.ClassifierInput != input {
		t.Fatalf("clear: %+v %v", p, err)
	}
	execTx(t, c.DB, `UPDATE messages SET text='synthetic changed' WHERE id='m'`)
	if _, err = c.manuallyClearTeamsInput(ctx, "c", input, p.Revision); !errors.Is(err, errTeamsPrivacyStale) {
		t.Fatalf("stale input accepted: %v", err)
	}
	current, err := teamsManualReviewInput(ctx, c.reader(), "c")
	if err != nil || current == input {
		t.Fatalf("changed digest: %v", err)
	}
	p, err = c.setTeamsPrivate(ctx, "c", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.manuallyClearTeamsInput(ctx, "c", current, p.Revision); err == nil {
		t.Fatal("cleared explicit private mark")
	}
	p, err = c.teamsPrivacy(ctx, "c")
	if err != nil || !p.UserPrivate {
		t.Fatalf("private mark lost: %+v %v", p, err)
	}
}
