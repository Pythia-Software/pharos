package archive

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

func teamsTestRegistration(t *testing.T, snapshot teamsSourceSnapshot, owned []string, privacy int64) teamsRegistration {
	t.Helper()
	sources := make([]teams.Source, len(snapshot.Entries))
	for i, e := range snapshot.Entries {
		sources[i] = teams.Source{Token: e.Token, EvidenceRevision: e.EvidenceRevision, Protection: e.Protection}
	}
	m := teams.Manifest{LocalRevision: 1, Complete: true, SourceManifest: sources}
	_, hash, err := m.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	r, err := newTeamsRegistration(m, teams.SourcesResponse{ManifestSHA256: hash, OwnershipRevision: 3, OwnedSourceTokens: owned}, teams.PrivacySnapshot{PrivacyRevision: privacy, PrivateSourceTokens: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func teamsSyntheticResolver(provider, account, native string) (string, string, string, error) {
	return provider, account, native, nil
}
func TestTeamsNumericSnapshotAndOwnership(t *testing.T) {
	c, _ := testCatalog(t)
	ctx := context.Background()
	key := make([]byte, 32)
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w1','codex','synthetic-account','w1','Synthetic','2026-01-01'),('w2','codex','synthetic-account','w2','Synthetic','2026-01-01')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c1','w1','codex','synthetic-account','c1'),('c2','w2','codex','synthetic-account','c2')`,
		`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,model,started_at,total_tokens) VALUES('s1','w1','c1','main','root','codex','model','2026-01-01T00:00:00Z',100),('s2','w2','c2','main','root','codex','model','2026-01-01T00:00:00Z',200)`,
		`INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,effort,attribution,total_tokens,output_tokens,is_estimate) VALUES('s1','2026-01-01T00:00:00Z','model','','request',100,10,0),('s2','2026-01-01T00:00:00Z','model','','request',200,20,1)`)
	if _, err := c.setTeamsPrivate(ctx, "c2", true); err != nil {
		t.Fatal(err)
	}
	plan, err := compileTeamsQuery(teamsExampleQuery)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := c.teamsSourceSnapshot(ctx, key, teamsSyntheticResolver)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Entries) != 2 {
		t.Fatalf("manifest: %+v", snap.Entries)
	}
	owned := []string{}
	var privateToken string
	for _, e := range snap.Entries {
		owned = append(owned, e.Token)
		wantReason := "provisional"
		wantProtection := "unknown"
		if snap.conversationByToken[e.Token] == "c2" {
			privateToken = e.Token
			wantReason = "explicit"
			wantProtection = "private"
		}
		if e.Protection != wantProtection {
			t.Fatalf("source protection: %+v", e)
		}
		if snap.protectionReasonByToken[e.Token] != wantReason {
			t.Fatalf("privacy reason: %+v", snap.protectionReasonByToken)
		}
	}
	if privateToken == "" {
		t.Fatal("private mark absent from manifest")
	}
	registration := teamsTestRegistration(t, snap, owned, 4)
	result, err := c.teamsExecuteNumeric(ctx, plan, key, teamsSyntheticResolver, snap, registration)
	if err != nil || result.Values["total_tokens"] != 300 || !result.Estimated || !result.Measured {
		t.Fatalf("all owned: %+v %v", result, err)
	}
	if err = c.teamsMarkRecoveryUnproved(ctx); err != nil {
		t.Fatal(err)
	}
	result, err = c.teamsExecuteNumeric(ctx, plan, key, teamsSyntheticResolver, snap, registration)
	if err != nil || result.Values["total_tokens"] != 300 || !result.Estimated || !result.Measured {
		t.Fatalf("numeric query with unscoped recovery hold: %+v %v", result, err)
	}
	rows, err := c.usageRows(time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	var native int64
	for _, r := range rows {
		native += integer(r["total_tokens"])
	}
	if result.Values["total_tokens"] != native {
		t.Fatalf("native %d vs Teams %d", native, result.Values["total_tokens"])
	}
	result, err = c.teamsExecuteNumeric(ctx, plan, key, teamsSyntheticResolver, snap, teamsTestRegistration(t, snap, []string{privateToken}, 4))
	if err != nil || result.Values["total_tokens"] != 200 {
		t.Fatalf("private stats and owner: %+v %v", result, err)
	}
	if _, err = c.teamsExecuteNumeric(ctx, plan, key, teamsSyntheticResolver, snap, teamsRegistration{valid: true, Manifest: registration.Manifest, Owned: []string{"unknown"}}); !errors.Is(err, errTeamsSourceUnavailable) {
		t.Fatalf("unknown owner: %v", err)
	}
	if _, err = c.setTeamsPrivate(ctx, "c1", true); err != nil {
		t.Fatal(err)
	}
	if _, err = c.teamsExecuteNumeric(ctx, plan, key, teamsSyntheticResolver, snap, registration); !errors.Is(err, errTeamsSnapshotChanged) {
		t.Fatalf("changed privacy: %v", err)
	}
}

func TestTeamsNumericRejectsUntrustedAndMirroredSources(t *testing.T) {
	c, _ := testCatalog(t)
	ctx := context.Background()
	key := make([]byte, 32)
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('a','codex','local','a','A','2026-01-01'),('b','conductor','local','b','B','2026-01-01')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('a','a','codex','local','a'),('b','b','codex','local','b')`,
		`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider) VALUES('a','a','a','main','root','codex'),('b','b','b','main','root','codex')`,
		`INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,effort,attribution,total_tokens) VALUES('a','2026-01-01T00:00:00Z','m','','request',10),('b','2026-01-01T00:00:00Z','m','','request',10)`,
		`INSERT INTO conversation_identity_links(left_id,right_id,relationship,confidence,evidence_json) VALUES('a','b','mirror',1,'{}')`)
	if _, err := c.teamsSourceSnapshot(ctx, key, teamsSyntheticResolver); !errors.Is(err, errTeamsSourceUnavailable) {
		t.Fatalf("unproved local account: %v", err)
	}
	execTx(t, c.DB, `UPDATE conversations SET account='synthetic-account'`)
	for _, id := range []string{"a", "b"} {
		if _, err := c.setTeamsClassification(ctx, id, "synthetic", "pending"); err != nil {
			t.Fatal(err)
		}
		if _, err := c.setTeamsClassification(ctx, id, "synthetic", "clear"); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := c.teamsSourceSnapshot(ctx, key, teamsSyntheticResolver)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := compileTeamsQuery(teamsExampleQuery)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.teamsExecuteNumeric(ctx, plan, key, teamsSyntheticResolver, snap, teamsTestRegistration(t, snap, []string{}, 0)); !errors.Is(err, errTeamsSourceUnavailable) {
		t.Fatalf("mirror: %v", err)
	}
}

func TestTeamsLegacyOrphanPrivacyNeedsReconciliation(t *testing.T) {
	c, _ := testCatalog(t)
	ctx := context.Background()
	execTx(t, c.DB, `INSERT INTO teams_conversation_privacy(conversation_id,user_private) VALUES('legacy-orphan',1)`)
	if _, err := c.teamsSourceSnapshot(ctx, make([]byte, 32), teamsSyntheticResolver); !errors.Is(err, errTeamsSourceUnavailable) {
		t.Fatalf("orphan privacy was ignored: %v", err)
	}
}
